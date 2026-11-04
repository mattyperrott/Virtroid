// SPDX-License-Identifier: GPL-3.0-or-later
//
// What the app asks the engine to do, on 0xbow's SDK. One wallet on the phone
// (keys.ts): the user sends ETH, USDC or USDT to its deposit address, the
// engine moves what arrives into the pool, and later sends it out to any
// address through the relayer.
//
// The note owner's protocol keys come from a signature over the same EIP-712
// message 0xbow's own app asks for, so importing the 12 words into a wallet
// opens the same account there (a way back to the funds if this app is ever
// gone). A deposit is built by the owner's session and sent from the deposit
// address: the proof doesn't name who pays, so the addresses aren't linked to
// the owner, or to each other, by anything but the first one, whose
// transaction also registers the owner's keys.
//
// Sessions are built per operation, for the route it relays on: the relayer
// binds the asset's router as the processor of a yield withdrawal, and a proof
// bound to anything else reverts. Every session reads and writes the same
// stored notes.

import {
    CryptoService,
    DEFAULT_CIRCUIT_MANIFEST,
    DEFAULT_IPFS_GATEWAY_URLS,
    HTTPClient,
    IpfsCircuitArtifacts,
    NoteStatus,
    PoolSessionBuilder,
    PoseidonHashService,
    RelayerInteractor,
    ViemRPCInteractor,
    APP_IDENTIFIER,
    AddressSchema,
} from "@privacy-pools-v2/evm-sdk";
import type {
    Address,
    EvmPoolSession,
    Hex,
    ICacheStore,
    IHashService,
    ILogger,
    Note,
    ProtocolKeys,
    RelayerInfo,
    YieldSessions,
} from "@privacy-pools-v2/evm-sdk";
import { createPublicClient, defineChain, encodeFunctionData, erc20Abi, fallback, http, keccak256, parseAbi, toBytes, toHex } from "viem";
import type { PublicClient } from "viem";
import type { PrivateKeyAccount } from "viem/accounts";

import type { Bridge } from "./bridge.js";
import { EngineError } from "./bridge.js";
import { AppCircuitArtifacts } from "./artifacts.js";
import { NATIVE } from "./chains.js";
import type { Asset, ChainConfig } from "./chains.js";
import { Gasless, tokenCost } from "./gasless.js";
import type { Call, GasQuote } from "./gasless.js";
import { FIRST_DEPOSIT_INDEX, OWNER_INDEX, accountAt, newWords, readWords } from "./keys.js";
import { HostStorage } from "./storage.js";
import { feesNow, ownerProvider, sendFrom } from "./wallet.js";
import type { Fees } from "./wallet.js";

/** The words 0xbow's app and SDK sample sign; they are part of the derived keys. */
const DERIVATION_PURPOSE =
    "This signature is used to deterministically derive application-specific secrets from your master seed. It is not a transaction and will not cost any gas.";

/** The entrypoint's per-asset terms: the smallest deposit and the screening (vetting) fee. */
const ENTRYPOINT_ASSETS_ABI = [
    {
        type: "function",
        name: "assets",
        inputs: [{ name: "_asset", type: "address" }],
        outputs: [
            {
                name: "config_",
                type: "tuple",
                components: [
                    { name: "enabled", type: "bool" },
                    { name: "minAmount", type: "uint256" },
                    { name: "vettingFeeBPS", type: "uint256" },
                    { name: "maxRelayFee", type: "uint256" },
                ],
            },
        ],
        stateMutability: "view",
    },
] as const;

const KEYSTORE_ABI = parseAbi(["function setAuthPolicyWithSig(address,uint256,uint256,uint256,bytes)"]);
const ZAP_ABI = parseAbi(["function redeemToUnderlying(uint256 shares, address receiver) returns (uint256)"]);
const WETH_ABI = parseAbi(["function withdraw(uint256 wad)"]);

// Gas the planning reserves before there's a transaction to estimate; the
// figures are measured on mainnet and on a fork of it (test/fork).
/** A router deposit: about 945k through ETH and USDC, 961k through USDT. */
const DEPOSIT_GAS = 1_050_000n;
/** Registering the owner's keys, once, with the first deposit: about 300k. */
const REGISTER_GAS = 350_000n;
const APPROVE_GAS = 70_000n;
/**
 * A UserOperation's own validation, calldata and paymaster gas, with its
 * approvals. Kept tight, because what's left for gas past what it uses stays at
 * the address: a bundler that wants more says so, and the deposit is resized.
 */
const GASLESS_OVERHEAD = 400_000n;
/** A ragequit, then turning its shares back into the asset. */
const EXIT_GAS = 1_300_000n;

/** Above this share of the amount, a deposit waits for cheaper gas unless the user says go. */
const MAX_FEE_PERCENT = 10n;
/** How long a sent transaction can stay unseen before the sweep is tried again. */
const SEND_TIMEOUT_MS = 15 * 60_000;
const WAIT_MS = 180_000;
/** After a deposit fails on chain, how long before trying again; it doubles each time, as each try can cost gas. */
const RETRY_BACKOFF_MS = 10 * 60_000;
/** How long after a deposit the same-looking withdrawal still points back at it. */
const RECENT_MS = 24 * 3_600_000;

/** A deposit sent and not yet seen in a block. */
interface PendingSweep {
    index: number;
    symbol: string;
    amount: string;
    kind: "tx" | "userOp";
    hash: Hex;
    at: number;
}

/** A deposit that went in, for the warning a too-similar withdrawal gets. */
interface Shielded {
    symbol: string;
    amount: string;
    at: number;
}

interface WalletRecord {
    words: string;
    /** The deposit address the user is shown: the first with nothing swept from it. */
    next: number;
    pending: PendingSweep | null;
    shielded: Shielded[];
    /** Deposits in a row that failed on chain, and when to try again. */
    failures?: number;
    retryAt?: number;
}

interface Account {
    chain: ChainConfig;
    owner: PrivateKeyAccount;
    keys: ProtocolKeys;
    storage: HostStorage;
}

export type IncomingState =
    /** Less than the pool takes; more sent to the same address adds to it. */
    | "below_minimum"
    /** Gas would take too much of it now; `fee` is what it'd cost. */
    | "fee_high"
    /** The stablecoin gas service isn't answering. The address's funds are safe and it's tried again. */
    | "gas_unavailable"
    /** On the way into the pool. */
    | "shielding";

export interface Incoming {
    symbol: string;
    decimals: number;
    amount: string;
    state: IncomingState;
    minimum?: string;
    fee?: string;
    txHash?: Hex;
    message?: string;
}

export interface Balance {
    symbol: string;
    decimals: number;
    /** Spendable now, in the asset the user holds (USDT, not ppUSDT shares). */
    spendable: string;
    /** Deposited, waiting for 0xbow's screening. */
    pending: string;
    /** The deposits waiting, each with the label 0xbow's screening approves it by. */
    pendingDeposits: { commitment: Hex; label: Hex; value: string }[];
    /** Deposits screening declined, which only an exit can take back out. */
    declined: { commitment: Hex; value: string }[];
}

/** One relayed withdrawal: notes of one deposit, at most two (the bundled circuits). */
interface Part {
    commitments: Hex[];
    total: bigint;
    /** Shares to deliver, or null for all of it less the fee. */
    units: bigint | null;
}

interface PlannedSend {
    id: number;
    symbol: string;
    recipient: Address;
    parts: Part[];
}

export class Engine {
    private hashService: IHashService | null = null;
    private account_: Account | null = null;
    private plan: PlannedSend | null = null;
    /** Numbers each quote, so a send can only go out as the quote the user is looking at. */
    private quotes = 0;
    private busy = false;
    /** Which older deposit address gets looked at next, one per check. */
    private olderCursor = 0;
    private readonly store: HostStorage;
    private readonly chain: ChainConfig;
    private readonly client: PublicClient;
    private readonly gasless: Gasless | null;

    constructor(
        private readonly bridge: Bridge,
        chains: ChainConfig[],
        private readonly circuitsBase: string,
        private readonly cacheStore: ICacheStore,
        private readonly logger: ILogger,
    ) {
        this.chain = chains[0]!;
        this.store = new HostStorage(bridge, "wallet:");
        this.client = createPublicClient({
            chain: defineChain({
                id: this.chain.chainId,
                name: this.chain.name,
                nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
                rpcUrls: { default: { http: this.chain.rpcUrls } },
            }),
            transport: fallback(this.chain.rpcUrls.map((url) => http(url, { retryCount: 1, timeout: 30_000 }))),
            pollingInterval: 2_000,
        }) as PublicClient;
        this.gasless = this.chain.gasless ? new Gasless(this.client, this.chain.chainId, this.chain.gasless) : null;
    }

    /** What the app needs to know to watch the address without the engine. */
    info() {
        return {
            chainId: this.chain.chainId,
            name: this.chain.name,
            explorerUrl: this.chain.explorerUrl,
            rpcUrls: this.chain.rpcUrls,
            assets: this.chain.assets.map((a) => ({ symbol: a.symbol, token: a.token, decimals: a.decimals })),
        };
    }

    // --- the wallet ---------------------------------------------------------

    /** Where the wallet stands; `address` is the one to show. */
    async status() {
        const record = await this.record();
        if (!record) return { ready: false };
        return { ready: true, ...this.addresses(record) };
    }

    /** Makes the wallet. The words come back once here, for the user to write down. */
    async create() {
        if (await this.record()) throw new EngineError("wallet_exists", "There's already a wallet here");
        const record: WalletRecord = { words: newWords(), next: FIRST_DEPOSIT_INDEX, pending: null, shielded: [] };
        await this.save(record);
        return { words: record.words, ...this.addresses(record) };
    }

    /** Brings the wallet back from its words, and finds where its deposit addresses had got to. */
    async restore(text: string) {
        if (await this.record()) throw new EngineError("wallet_exists", "There's already a wallet here");
        const words = readWords(text);
        const record: WalletRecord = { words, next: await this.firstUnused(words), pending: null, shielded: [] };
        await this.save(record);
        this.account_ = null;
        return this.addresses(record);
    }

    /** The backup words, for the screen that shows them again. */
    async words() {
        return { words: (await this.needRecord()).words };
    }

    private addresses(record: WalletRecord) {
        const watch: string[] = [];
        for (let i = FIRST_DEPOSIT_INDEX; i <= record.next; i++) watch.push(accountAt(record.words, i).address);
        return {
            owner: accountAt(record.words, OWNER_INDEX).address,
            address: watch[watch.length - 1]!,
            /** Every address that's been shown, the current one last. */
            watch,
        };
    }

    /** Past the last address that's been used, with a gap in case one went unused. */
    private async firstUnused(words: string): Promise<number> {
        let next = FIRST_DEPOSIT_INDEX;
        let misses = 0;
        for (let i = FIRST_DEPOSIT_INDEX; misses < 5; i++) {
            const address = accountAt(words, i).address;
            if ((await this.client.getTransactionCount({ address })) > 0) {
                next = i + 1;
                misses = 0;
            } else {
                misses++;
            }
        }
        return next;
    }

    private async record(): Promise<WalletRecord | null> {
        return this.store.get<WalletRecord>("record");
    }

    private async needRecord(): Promise<WalletRecord> {
        const record = await this.record();
        if (!record) throw new EngineError("no_wallet", "There's no wallet here yet");
        return record;
    }

    private save(record: WalletRecord): Promise<void> {
        return this.store.set("record", record);
    }

    // --- deposits -----------------------------------------------------------

    /**
     * Looks at the deposit address, and one older one each time, and moves
     * what can go into the pool. `force` names assets the user has said to
     * shield despite the fee. Safe to call as often as the app likes: one at a
     * time, and a deposit already sent isn't sent again.
     */
    async check(force: string[] = []): Promise<{ address: string; watch: string[]; incoming: Incoming[]; moved: boolean }> {
        if (this.busy) throw new EngineError("busy", "Shield is already working on this");
        this.busy = true;
        try {
            const record = await this.needRecord();
            const account = await this.openAccount(record);
            const incoming: Incoming[] = [];
            let moved = false;

            if (record.pending) {
                const pending = record.pending;
                const outcome = await this.settle(record, pending, account);
                if (outcome === "waiting") {
                    const asset = this.asset(pending.symbol);
                    incoming.push({ symbol: pending.symbol, decimals: asset.decimals, amount: pending.amount, state: "shielding", txHash: pending.hash });
                    return { ...this.addresses(record), incoming, moved };
                }
                moved = outcome === "done";
                if (outcome === "failed") await this.noteFailure(record);
            }

            const current = record.next;
            const indexes = [current];
            const older = current - FIRST_DEPOSIT_INDEX;
            if (older > 0) indexes.push(FIRST_DEPOSIT_INDEX + this.olderCursor++ % older);

            for (const index of indexes) {
                const burner = accountAt(record.words, index);
                // Tokens first: what ETH the address holds can pay their gas.
                const assets = [...this.chain.assets].sort((a, b) => Number(a.token === NATIVE) - Number(b.token === NATIVE));
                for (const asset of assets) {
                    const amount = await this.holding(burner.address, asset);
                    if (amount === 0n) continue;
                    const forced = force.includes(asset.symbol);
                    if (!forced && (record.retryAt ?? 0) > Date.now()) {
                        incoming.push({ symbol: asset.symbol, decimals: asset.decimals, amount: amount.toString(), state: "gas_unavailable" });
                        continue;
                    }
                    let result: Incoming;
                    try {
                        result = await this.sweep(record, account, index, burner, asset, amount, forced);
                    } catch (error) {
                        if (!(error instanceof EngineError && error.code === "reverted")) throw error;
                        await this.noteFailure(record);
                        incoming.push({ symbol: asset.symbol, decimals: asset.decimals, amount: amount.toString(), state: "gas_unavailable", message: error.message });
                        continue;
                    }
                    const landed = result.state === "shielding" && !result.txHash;
                    if (landed) moved = true;
                    // An old address keeps a little dust from its gas; only the current one reports that.
                    else if (index === current || !isDust(result)) incoming.push(result);
                    if (record.pending) return { ...this.addresses(record), incoming, moved };
                }
            }
            return { ...this.addresses(record), incoming, moved };
        } finally {
            this.busy = false;
        }
    }

    private async holding(address: Address, asset: Asset): Promise<bigint> {
        if (asset.token === NATIVE) return this.client.getBalance({ address });
        return this.client.readContract({ address: asset.token, abi: erc20Abi, functionName: "balanceOf", args: [address] });
    }

    /** Sees how a sent deposit ended. */
    private async settle(record: WalletRecord, pending: PendingSweep, account: Account): Promise<"done" | "failed" | "waiting"> {
        let success: boolean | null = null;
        if (pending.kind === "tx") {
            const receipt = await this.client.getTransactionReceipt({ hash: pending.hash }).catch(() => null);
            if (receipt) success = receipt.status === "success";
        } else if (this.gasless) {
            const receipt = await this.gasless.receipt(pending.hash).catch(() => null);
            if (receipt) success = receipt.success;
        }
        if (success === null && Date.now() - pending.at < SEND_TIMEOUT_MS) return "waiting";
        record.pending = null;
        if (success) this.noteShielded(record, pending);
        await this.save(record);
        if (success) await this.refreshNotes(account);
        return success ? "done" : "failed";
    }

    private async noteFailure(record: WalletRecord): Promise<void> {
        const failures = (record.failures ?? 0) + 1;
        record.failures = failures;
        record.retryAt = Date.now() + RETRY_BACKOFF_MS * 2 ** Math.min(failures - 1, 6);
        await this.save(record);
    }

    private noteShielded(record: WalletRecord, pending: PendingSweep): void {
        record.failures = 0;
        record.retryAt = 0;
        if (pending.index === record.next) record.next = pending.index + 1;
        record.shielded = [...record.shielded.filter((s) => Date.now() - s.at < RECENT_MS), { symbol: pending.symbol, amount: pending.amount, at: Date.now() }];
    }

    /** Moves `amount` of `asset` from a deposit address into the pool, or says why it can't yet. */
    private async sweep(
        record: WalletRecord,
        account: Account,
        index: number,
        burner: PrivateKeyAccount,
        asset: Asset,
        amount: bigint,
        force: boolean,
    ): Promise<Incoming> {
        const base = { symbol: asset.symbol, decimals: asset.decimals, amount: amount.toString() };
        const session = await this.session(account, asset.yield.ppRouter);
        const y = this.yieldOf(session, asset);
        const terms = await this.assetTerms(asset.yield.ppUSDC);
        const smallest = terms.minAmount > 0n ? await y.token.previewRedeem(terms.minAmount) : 0n;
        const registered = await session.isKeystoreRegistered();
        let work = DEPOSIT_GAS + (registered ? 0n : REGISTER_GAS);
        const fees = await feesNow(this.client);
        const native = asset.token === NATIVE;
        const eth = native ? amount : await this.client.getBalance({ address: burner.address });
        const direct = native || eth >= (work + 2n * APPROVE_GAS) * fees.maxFee;

        let budget = amount;
        let fee = 0n;
        let quote: GasQuote | null = null;
        if (native) {
            fee = work * fees.maxFee;
            budget = amount - fee;
        } else if (!direct) {
            if (!this.gasless) return { ...base, state: "gas_unavailable", message: "This chain has no gas service" };
            try {
                quote = await this.gasless.quote(asset.token);
            } catch (error) {
                return { ...base, state: "gas_unavailable", message: (error as Error).message };
            }
            fee = tokenCost(work + GASLESS_OVERHEAD, quote.maxFee, quote.rate);
            budget = amount - fee;
        }

        for (let attempt = 0; ; attempt++) {
            const sized = budget > 0n ? await y.deposit.sizeSharesForBudget(budget).catch(() => null) : null;
            if (!sized || sized.shares <= 0n || sized.shares < terms.minAmount) {
                // Enough for the pool but for what gas would take: that's the fee's doing, and it passes.
                if (amount >= smallest && fee * 100n > amount * MAX_FEE_PERCENT) return { ...base, state: "fee_high", fee: fee.toString() };
                return { ...base, state: "below_minimum", minimum: (smallest + fee).toString() };
            }
            if (!force && fee * 100n > amount * MAX_FEE_PERCENT) return { ...base, state: "fee_high", fee: fee.toString() };

            this.bridge.emit("progress", { stage: "proving", symbol: asset.symbol, amount: amount.toString() });
            const prepared = await y.deposit.prepareYieldDeposit({ value: sized.shares, maxUnderlyingIn: sized.maxUnderlyingIn });
            const deposit: Call = {
                to: prepared.depositTx.to as Address,
                data: prepared.depositTx.data as Hex,
                value: BigInt(prepared.depositTx.value ?? 0),
            };
            const before: Call[] = [];
            if (!registered) before.push(await this.registrationCall(session));
            if (!native) before.push(...(await this.approvals(asset.token, burner.address, asset.yield.ppRouter, sized.maxUnderlyingIn)));
            const hold = (await y.token.previewRedeem(sized.shares)).toString();

            this.bridge.emit("progress", { stage: "sending", symbol: asset.symbol, amount: amount.toString() });
            if (direct) {
                try {
                    return await this.sendDirect(record, account, index, burner, asset, base, hold, before, deposit, native ? fees : undefined);
                } catch (error) {
                    if (error instanceof EngineError && error.code === "gas_moved" && attempt === 0) {
                        // The chain wants more gas than was left for it: leave that, and prove again.
                        work = BigInt(error.message);
                        fee = work * fees.maxFee;
                        budget = amount - fee;
                        continue;
                    }
                    throw error;
                }
            }
            try {
                const hash = await this.gasless!.send(burner, asset.token, [...before, deposit], fee, quote!);
                return await this.follow(record, account, { index, symbol: asset.symbol, amount: hold, kind: "userOp", hash, at: Date.now() }, base);
            } catch (error) {
                if (error instanceof EngineError && error.code === "gas_cap" && attempt === 0) {
                    // The bundler wants more room than was left: leave what it asked for, and prove again.
                    fee = BigInt(error.message);
                    budget = amount - fee;
                    continue;
                }
                if (error instanceof EngineError && (error.code === "gas_service" || error.code === "gas_cap")) {
                    return { ...base, state: "gas_unavailable", message: error.message };
                }
                throw error;
            }
        }
    }

    /** The deposit address pays its own gas: a transaction per call. */
    private async sendDirect(
        record: WalletRecord,
        account: Account,
        index: number,
        burner: PrivateKeyAccount,
        asset: Asset,
        base: Pick<Incoming, "symbol" | "decimals" | "amount">,
        hold: string,
        before: Call[],
        deposit: Call,
        fees: Fees | undefined,
    ): Promise<Incoming> {
        const { chainId } = this.chain;
        for (const call of before) {
            const hash = await sendFrom(this.client, chainId, burner, { to: call.to, data: call.data, value: call.value, fees });
            const receipt = await this.client.waitForTransactionReceipt({ hash, timeout: WAIT_MS });
            if (receipt.status !== "success") throw new EngineError("reverted", `Transaction ${hash} reverted`);
        }
        let gas: bigint | undefined;
        if (fees) {
            // Spends the address's whole ETH, so the gas it can buy is what's left past the deposit.
            const estimate = await this.client.estimateGas({ account: burner.address, to: deposit.to, data: deposit.data, value: deposit.value ?? 0n });
            const balance = await this.client.getBalance({ address: burner.address });
            const room = (balance - (deposit.value ?? 0n)) / fees.maxFee;
            gas = (estimate * 23n) / 20n;
            if (gas > room) throw new EngineError("gas_moved", gas.toString());
        }
        const hash = await sendFrom(this.client, chainId, burner, { to: deposit.to, data: deposit.data, value: deposit.value, gas, fees });
        return this.follow(record, account, { index, symbol: asset.symbol, amount: hold, kind: "tx", hash, at: Date.now() }, base);
    }

    /** Keeps the sent deposit in the store first, so a restart can't send it twice, and waits for it. */
    private async follow(
        record: WalletRecord,
        account: Account,
        pending: PendingSweep,
        base: Pick<Incoming, "symbol" | "decimals" | "amount">,
    ): Promise<Incoming> {
        record.pending = pending;
        await this.save(record);
        this.bridge.emit("progress", { stage: "confirming", symbol: pending.symbol, txHash: pending.hash });
        const started = Date.now();
        while (Date.now() - started < WAIT_MS) {
            const outcome = await this.settle(record, pending, account);
            if (outcome === "done") return { ...base, state: "shielding" };
            if (outcome === "failed") throw new EngineError("reverted", `The deposit ${pending.hash} didn't go through`);
            await new Promise((resolve) => setTimeout(resolve, 3_000));
        }
        return { ...base, state: "shielding", txHash: pending.hash };
    }

    /** Lets the router take the asset; USDT's approve reverts unless a leftover allowance is reset first. */
    private async approvals(token: Address, owner: Address, spender: Address, amount: bigint): Promise<Call[]> {
        const allowance = await this.client.readContract({ address: token, abi: erc20Abi, functionName: "allowance", args: [owner, spender] });
        if (allowance >= amount) return [];
        const approve = (value: bigint): Call => ({ to: token, data: encodeFunctionData({ abi: erc20Abi, functionName: "approve", args: [spender, value] }) });
        return [...(allowance > 0n ? [approve(0n)] : []), approve(amount)];
    }

    /** The owner signs its keys into the keystore; whoever pays for the call sends it. */
    private async registrationCall(session: EvmPoolSession): Promise<Call> {
        const deadline = toHex(Math.floor(Date.now() / 1000) + 3_600);
        const registration = await session.prepareRelayedRegistration({ deadline });
        return {
            to: this.chain.keystore,
            data: encodeFunctionData({
                abi: KEYSTORE_ABI,
                functionName: "setAuthPolicyWithSig",
                args: [
                    registration.account,
                    BigInt(registration.authDigest),
                    BigInt(registration.nullifyingKeyHash),
                    BigInt(registration.deadline),
                    registration.signature as Hex,
                ],
            }),
        };
    }

    // --- what's in the pool -------------------------------------------------

    /** Reads the chain for the account's notes and adds up what it holds. */
    async sync(): Promise<Balance[]> {
        const account = await this.openAccount(await this.needRecord());
        const session = await this.session(account, this.chain.assets[0]!.yield.ppRouter);
        this.bridge.emit("progress", { stage: "sync" });
        await session.discoverNotes();
        const notes = (await session.exportAccount()).notes as Note[];
        const inflight = await this.inflight(account);
        return Promise.all(this.chain.assets.map((asset) => this.balance(session, asset, notes, inflight)));
    }

    private async refreshNotes(account: Account): Promise<void> {
        await (await this.session(account, this.chain.assets[0]!.yield.ppRouter)).discoverNotes();
    }

    /**
     * What sending `amount` of `symbol` ("max" for all of it) to `recipient`
     * comes to. A spend can't mix deposits, so an amount no one deposit covers
     * goes as several withdrawals, each paying the relayer's flat fee. Nothing
     * is proved yet: the relayer's fee commitments don't outlast a proof each.
     */
    async quoteOut(symbol: string, amount: string, recipient: Address) {
        const account = await this.openAccount(await this.needRecord());
        const asset = this.asset(symbol);
        this.plan = null;
        const router = asset.yield.ppRouter;
        const session = await this.session(account, router);
        const y = this.yieldOf(session, asset);
        const relayer = this.relayerFor(router);
        const notes = await this.spendableNotes(account, session, asset.yield.ppUSDC);
        const deposits = lineages(notes);
        const largest = deposits[0];
        if (!largest) throw new EngineError("nothing_spendable", "Nothing is ready to send yet");
        const asked = amount === "max" ? null : await sharesAtLeast(y, positive(amount));
        const fee = await this.withdrawalFee(relayer, asset.yield.ppUSDC, recipient, asked ?? largest.total);
        const parts = planParts(deposits, asked, fee);
        const delivered = parts.reduce((sum, p) => sum + (p.units ?? p.total - fee), 0n);
        const id = ++this.quotes;
        this.plan = { id, symbol, recipient, parts };
        const receive = await y.token.previewRedeem(delivered);
        const record = await this.needRecord();
        // A withdrawal soon after a deposit of about the same amount, or very soon after any, points back at it.
        const recent = record.shielded.some((s) => {
            if (s.symbol !== symbol || Date.now() - s.at > RECENT_MS) return false;
            const deposited = BigInt(s.amount);
            const gap = deposited > receive ? deposited - receive : receive - deposited;
            return gap * 50n <= deposited || Date.now() - s.at < RECENT_MS / 8;
        });
        return { id, receive: receive.toString(), fee: (await y.token.previewRedeem(fee * BigInt(parts.length))).toString(), parts: parts.length, recent };
    }

    /** Sends the quote the user accepted, a withdrawal at a time, each proved just before it goes. */
    async sendOut(id: number) {
        const plan = this.plan;
        if (!plan) throw new EngineError("no_quote", "Nothing is waiting to be sent");
        if (plan.id !== id) throw new EngineError("quote_replaced", "A newer quote replaced this one");
        this.plan = null;
        const account = await this.openAccount(await this.needRecord());
        const asset = this.asset(plan.symbol);
        const router = asset.yield.ppRouter;
        const relayer = this.relayerFor(router);
        const txHashes: Hex[] = [];
        for (const [i, part] of plan.parts.entries()) {
            const session = await this.session(account, router);
            const y = this.yieldOf(session, asset);
            const step = { part: i + 1, parts: plan.parts.length, sent: txHashes.length };
            try {
                this.bridge.emit("progress", { stage: "proving", ...step });
                const fee = await this.withdrawalFee(relayer, asset.yield.ppUSDC, plan.recipient, part.units ?? part.total);
                const units = part.units ?? part.total - fee;
                if (units <= 0n) throw new EngineError("below_fee", "The balance is smaller than the relayer's fee");
                const quote = await this.relayerInteractor(relayer).getWithdrawalQuote(relayer, {
                    asset: asset.yield.ppUSDC,
                    commit: true,
                    recipient: plan.recipient,
                    extraGas: false,
                    amount: units.toString(),
                });
                if (!quote.feeCommitment) throw new EngineError("no_quote", "The relayer didn't commit to a fee");
                if (BigInt(quote.amountSent) > part.total) throw new EngineError("fee_changed", "The relayer's fee went up; try again");
                const prepared = await y.withdraw.prepareYieldWithdrawRelay({
                    inputCommitments: part.commitments,
                    amount: BigInt(quote.amountSent),
                    amountOut: BigInt(quote.amountSent),
                    recipient: plan.recipient,
                    signedFeeCommitment: quote.feeCommitment,
                });
                // Hidden from the balance while the relay runs. If it fails the notes
                // come back: spending them again can't spend twice (their nullifiers
                // can only be used once), so the worst a retry after a relay that did
                // land costs is a refused request.
                await this.markInflight(account, part.commitments, "relaying");
                this.bridge.emit("progress", { stage: "relaying", ...step });
                const txHash = await this.relayerInteractor(relayer).relayWithdrawal(relayer, prepared.relayParams).catch(async (error: unknown) => {
                    await this.clearInflight(account, part.commitments);
                    throw error;
                });
                await this.markInflight(account, part.commitments, txHash);
                this.bridge.emit("progress", { stage: "confirming", txHash, ...step });
                const receipt = await this.client.waitForTransactionReceipt({ hash: txHash, timeout: WAIT_MS });
                await session.discoverNotes();
                await this.clearInflight(account, part.commitments);
                if (receipt.status !== "success") throw new EngineError("reverted", `Transaction ${txHash} reverted`);
                txHashes.push(txHash);
            } catch (error) {
                if (txHashes.length > 0) {
                    throw new EngineError("partly_sent", `${txHashes.length} of ${plan.parts.length} went out: ${(error as Error).message}`);
                }
                throw error;
            }
        }
        return { txHashes };
    }

    /** The relayer's current flat fee for a withdrawal, in shares. */
    private async withdrawalFee(relayer: RelayerInfo, asset: Address, recipient: Address, amount: bigint): Promise<bigint> {
        if (amount <= 0n) throw new EngineError("nothing_spendable", "Nothing is ready to send yet");
        const quote = await this.relayerInteractor(relayer).getWithdrawalQuote(relayer, {
            asset,
            commit: true,
            recipient,
            extraGas: false,
            amount: amount.toString(),
        });
        return BigInt(quote.feeCommitment.feeAmount);
    }

    /** What taking a declined deposit back out needs: the owner address pays the gas, so it needs a little ETH. */
    async exitPlan() {
        const record = await this.needRecord();
        const owner = accountAt(record.words, OWNER_INDEX).address;
        const fees = await feesNow(this.client);
        const balance = await this.client.getBalance({ address: owner });
        return { owner, needed: (EXIT_GAS * fees.maxFee).toString(), balance: balance.toString() };
    }

    /**
     * Takes a deposit back out publicly, without screening, the way out for
     * one 0xbow declined: the pool pays the owner address the shares, which
     * turn back into the asset and go to `recipient`.
     */
    async exit(commitment: Hex, recipient: Address, symbol: string) {
        const record = await this.needRecord();
        const account = await this.openAccount(record);
        const asset = this.asset(symbol);
        const session = await this.session(account, asset.yield.ppRouter);
        const { owner } = account;
        const { chainId } = this.chain;
        const share = asset.yield.ppUSDC;
        const before = await this.client.readContract({ address: share, abi: erc20Abi, functionName: "balanceOf", args: [owner.address] });
        this.bridge.emit("progress", { stage: "proving" });
        const receipt = await session.rageQuit({ commitment });
        const txHashes: Hex[] = [receipt.txHash];
        const shares = (await this.client.readContract({ address: share, abi: erc20Abi, functionName: "balanceOf", args: [owner.address] })) - before;
        const run = async (to: Address, data: Hex, value = 0n) => {
            const hash = await sendFrom(this.client, chainId, owner, { to, data, value });
            this.bridge.emit("progress", { stage: "confirming", txHash: hash });
            const mined = await this.client.waitForTransactionReceipt({ hash, timeout: WAIT_MS });
            if (mined.status !== "success") throw new EngineError("reverted", `Transaction ${hash} reverted`);
            txHashes.push(hash);
        };
        if (shares > 0n) {
            const zap = asset.yield.zap!;
            await run(share, encodeFunctionData({ abi: erc20Abi, functionName: "approve", args: [zap, shares] }));
            const native = asset.token === NATIVE;
            const underlying = asset.yield.underlying;
            const held = native ? await this.client.readContract({ address: underlying, abi: erc20Abi, functionName: "balanceOf", args: [owner.address] }) : 0n;
            await run(zap, encodeFunctionData({ abi: ZAP_ABI, functionName: "redeemToUnderlying", args: [shares, native ? owner.address : recipient] }));
            if (native) {
                // The zap pays WETH; the user sent ETH, and gets ETH back.
                const out = (await this.client.readContract({ address: underlying, abi: erc20Abi, functionName: "balanceOf", args: [owner.address] })) - held;
                await run(underlying, encodeFunctionData({ abi: WETH_ABI, functionName: "withdraw", args: [out] }));
                await run(recipient, "0x", out);
            }
        }
        await session.discoverNotes();
        return { txHashes };
    }

    // --- sessions -----------------------------------------------------------

    private async openAccount(record: WalletRecord): Promise<Account> {
        if (this.account_) return this.account_;
        const owner = accountAt(record.words, OWNER_INDEX);
        const addressHash = keccak256(toBytes(owner.address));
        // A private key's signatures are deterministic, so this makes the same keys every time.
        const signature = await owner.signTypedData({
            domain: {
                name: "Standardized Secret Derivation",
                version: "1",
                verifyingContract: "0x0000000000000000000000000000000000000000",
                salt: keccak256(toHex(APP_IDENTIFIER)),
            },
            message: { purpose: DERIVATION_PURPOSE, addressHash },
            primaryType: "SecretDerivation",
            types: {
                SecretDerivation: [
                    { name: "purpose", type: "string" },
                    { name: "addressHash", type: "bytes32" },
                ],
            },
        });
        const derived = new CryptoService().deriveKeysFromSignature({
            signature,
            signerAddress: owner.address,
            addressHash,
            revocableKeyIndex: "0x0",
        });
        const keys: ProtocolKeys = {
            privateNullifyingKey: derived.privateNullifyingKey,
            privateRevocableKey: derived.privateRevocableKey,
            revocableKeyIndex: "0x0" as Hex,
            viewingPrivateKey: derived.viewingPrivateKey,
            viewingPublicKey: derived.viewingPublicKey,
        };
        const storage = new HostStorage(this.bridge, `acct:${this.chain.chainId}:${owner.address.toLowerCase()}:`);
        this.account_ = { chain: this.chain, owner, keys, storage };
        return this.account_;
    }

    private async session(account: Account, router: Address): Promise<EvmPoolSession> {
        const { chain } = account;
        this.hashService ??= await PoseidonHashService.create();
        const rpcInteractor = new ViemRPCInteractor(
            { publicClient: this.client, chunkSize: chain.logsChunk, minFromBlock: BigInt(chain.fromBlock) },
            { logger: this.logger },
        );
        const ipfs = new IpfsCircuitArtifacts(
            { ipfsGatewayUrls: [...DEFAULT_IPFS_GATEWAY_URLS], circuitManifest: DEFAULT_CIRCUIT_MANIFEST, httpClient: new HTTPClient() },
            { cacheStore: this.cacheStore, logger: this.logger },
        );
        const builder = PoolSessionBuilder.fromConfig({
            chainId: chain.chainId,
            rpcUrl: chain.rpcUrls[0]!,
            ownerAddress: account.owner.address,
            deployment: {
                poolAddress: chain.pool,
                entrypointAddress: chain.entrypoint,
                keystoreAddress: chain.keystore,
                aspRegistryAddress: chain.aspRegistry,
                relaySwapsAddress: chain.relaySwaps,
            },
            deploymentBlock: chain.fromBlock,
            protocolKeys: account.keys,
            aspUrl: chain.aspUrl,
            relayers: [this.relayerFor(router)],
            logger: this.logger,
            walletInteractor: { type: "EIP1193", provider: ownerProvider(this.client, chain.chainId, account.owner) as never },
        })
            .withRpcInteractor(rpcInteractor)
            .withHashService(this.hashService)
            .withStorageService(account.storage)
            .withCircuitArtifacts(new AppCircuitArtifacts(DEFAULT_CIRCUIT_MANIFEST, this.circuitsBase, ipfs))
            .withYieldDeployments(chain.assets.map((a) => a.yield));
        return (await builder.create()) as EvmPoolSession;
    }

    private relayerFor(router: Address): RelayerInfo {
        const { chain } = this;
        return {
            name: "0xbow",
            url: chain.relayerUrl,
            chainId: chain.chainId,
            chainType: "evm",
            status: "active",
            address: chain.relayerSigner,
            processorAddress: router,
        };
    }

    private relayerInteractor(relayer: RelayerInfo): RelayerInteractor {
        return new RelayerInteractor({ relayers: [relayer], httpClient: new HTTPClient(), addressSchema: AddressSchema });
    }

    // --- notes --------------------------------------------------------------

    private async balance(session: EvmPoolSession, asset: Asset, notes: Note[], inflight: Set<string>): Promise<Balance> {
        const tokenId = asset.yield.ppUSDC.toLowerCase();
        const mine = notes.filter((n) => n.tokenId.toLowerCase() === tokenId && BigInt(n.value) > 0n);
        const active = mine.filter((n) => n.status === NoteStatus.ACTIVE && !inflight.has(n.commitment.toLowerCase()));
        const sum = (list: Note[]) => list.reduce((total, n) => total + BigInt(n.value), 0n);
        const y = this.yieldOf(session, asset);
        const value = async (units: bigint) => (units > 0n ? await y.token.previewRedeem(units) : units);
        // INACTIVE is a deposit the chain hasn't confirmed yet; PENDING one waiting for screening.
        const waiting = mine.filter((n) => n.status === NoteStatus.PENDING || n.status === NoteStatus.INACTIVE);
        const declined = mine.filter((n) => n.status === NoteStatus.REJECTED);
        return {
            symbol: asset.symbol,
            decimals: asset.decimals,
            spendable: (await value(sum(active))).toString(),
            pending: (await value(sum(waiting))).toString(),
            pendingDeposits: await Promise.all(
                waiting.map(async (n) => ({ commitment: n.commitment, label: n.label as Hex, value: (await value(BigInt(n.value))).toString() })),
            ),
            declined: await Promise.all(
                declined.map(async (n) => ({ commitment: n.commitment, value: (await value(BigInt(n.value))).toString() })),
            ),
        };
    }

    private async spendableNotes(account: Account, session: EvmPoolSession, tokenId: Address): Promise<Note[]> {
        await session.discoverNotes();
        const inflight = await this.inflight(account);
        const notes = (await session.exportAccount()).notes as Note[];
        return notes.filter(
            (n) =>
                n.tokenId.toLowerCase() === tokenId.toLowerCase() &&
                n.status === NoteStatus.ACTIVE &&
                BigInt(n.value) > 0n &&
                !inflight.has(n.commitment.toLowerCase()),
        );
    }

    // Notes a relay is spending, kept until the scan sees them spent, so a
    // second send can't pick them while the first is still in flight.
    private async inflight(account: Account): Promise<Set<string>> {
        return new Set(Object.keys((await account.storage.get<Record<string, string>>("inflight")) ?? {}));
    }

    private async markInflight(account: Account, commitments: Hex[], txHash: string): Promise<void> {
        const map = (await account.storage.get<Record<string, string>>("inflight")) ?? {};
        for (const c of commitments) map[c.toLowerCase()] = txHash;
        await account.storage.set("inflight", map);
    }

    private async clearInflight(account: Account, commitments: Hex[]): Promise<void> {
        const map = (await account.storage.get<Record<string, string>>("inflight")) ?? {};
        for (const c of commitments) delete map[c.toLowerCase()];
        await account.storage.set("inflight", map);
    }

    // --- lookups ------------------------------------------------------------

    private asset(symbol: string): Asset {
        const asset = this.chain.assets.find((a) => a.symbol === symbol);
        if (!asset) throw new EngineError("unknown_asset", `${symbol} isn't served on ${this.chain.name}`);
        return asset;
    }

    private async assetTerms(token: Address): Promise<{ minAmount: bigint; vettingFeeBPS: bigint }> {
        const config = await this.client.readContract({
            address: this.chain.entrypoint,
            abi: ENTRYPOINT_ASSETS_ABI,
            functionName: "assets",
            args: [token],
        });
        if (!config.enabled) throw new EngineError("unknown_asset", "The pool isn't taking this asset");
        return { minAmount: config.minAmount, vettingFeeBPS: config.vettingFeeBPS };
    }

    private yieldOf(session: EvmPoolSession, asset: Asset): YieldSessions {
        const y = session.yieldFor(asset.yield.ppUSDC);
        if (!y) throw new EngineError("unknown_asset", `${asset.symbol} has no yield session`);
        return y;
    }
}

/** Too little to ever be worth moving on its own. */
function isDust(item: Incoming): boolean {
    return item.state === "below_minimum" || (item.state === "fee_high" && BigInt(item.fee ?? 0) >= BigInt(item.amount));
}

function positive(amount: string): bigint {
    let value: bigint;
    try {
        value = BigInt(amount);
    } catch {
        throw new EngineError("bad_amount", `${amount} isn't an amount`);
    }
    if (value <= 0n) throw new EngineError("bad_amount", "The amount must be more than zero");
    return value;
}

/**
 * The fewest shares worth at least `underlying`: previewDeposit rounds down,
 * so without this the recipient of "40 USDC" gets 39.999999.
 */
async function sharesAtLeast(y: YieldSessions, underlying: bigint): Promise<bigint> {
    let shares = await y.deposit.sharesForUnderlying(underlying);
    for (let i = 0; i < 4 && (await y.token.previewRedeem(shares)) < underlying; i++) {
        shares += 10n ** BigInt(Math.max(0, y.deployment.ppUSDCDecimals - y.deployment.underlyingDecimals)) / 2n + 1n;
    }
    return shares;
}

/** The SDK spends notes of one deposit lineage at a time, at most two here (the bundled circuits). */
const MAX_INPUTS = 2;

/** Each deposit's spendable notes, the biggest deposit first. */
export function lineages(notes: Note[]): { commitments: Hex[]; total: bigint }[] {
    const byLabel = new Map<string, Note[]>();
    for (const n of notes) byLabel.set(n.label, [...(byLabel.get(n.label) ?? []), n]);
    return [...byLabel.values()]
        .map((lineage) => {
            const top = [...lineage].sort((x, y) => (BigInt(y.value) > BigInt(x.value) ? 1 : -1)).slice(0, MAX_INPUTS);
            return { commitments: top.map((n) => n.commitment), total: top.reduce((sum, n) => sum + BigInt(n.value), 0n) };
        })
        .sort((x, y) => (y.total > x.total ? 1 : -1));
}

/**
 * The withdrawals that deliver `asked` shares (null for everything), each
 * paying `fee`: one deposit if one covers it, the smallest that does, so the
 * rest stay whole; otherwise as many as it takes, biggest first.
 */
export function planParts(deposits: { commitments: Hex[]; total: bigint }[], asked: bigint | null, fee: bigint): Part[] {
    const worthIt = deposits.filter((d) => d.total > fee);
    if (worthIt.length === 0) throw new EngineError("below_fee", "The balance is smaller than the relayer's fee");
    if (asked === null) return worthIt.map((d) => ({ ...d, units: null }));
    const one = [...worthIt].reverse().find((d) => d.total >= asked + fee);
    if (one) return [{ ...one, units: asked }];
    const parts: Part[] = [];
    let left = asked;
    for (const d of worthIt) {
        if (left <= 0n) break;
        const take = d.total - fee < left ? d.total - fee : left;
        parts.push({ ...d, units: take });
        left -= take;
    }
    if (left > 0n) throw new EngineError("too_much", "That's more than the balance after the relayer's fees");
    return parts;
}
