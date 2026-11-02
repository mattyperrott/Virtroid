// SPDX-License-Identifier: GPL-3.0-or-later
//
// What the app asks the engine to do, on 0xbow's SDK. One account is one
// wallet address on one chain; its protocol keys come from a signature by
// that wallet over the same EIP-712 message 0xbow's own app asks for, so the
// account is the same one there (a way back to the funds if this app is
// ever gone).
//
// Sessions are built per operation, for the route it relays on: the relayer
// binds a different processor into each proof (its signer for a transfer,
// PrivacyPoolRelay for a plain withdrawal, the asset's router for a yield
// withdrawal), and a proof bound to the wrong one reverts. Every session
// reads and writes the same stored notes.

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
    PrepareTransferResult,
    PrepareWithdrawResult,
    ProtocolKeys,
    RelayerInfo,
    WithdrawalRelayParams,
    YieldSessions,
} from "@privacy-pools-v2/evm-sdk";
import { createPublicClient, erc20Abi, fallback, http, keccak256, toBytes, toHex } from "viem";

import type { Bridge } from "./bridge.js";
import { EngineError } from "./bridge.js";
import { AppCircuitArtifacts } from "./artifacts.js";
import { NATIVE } from "./chains.js";
import type { Asset, ChainConfig } from "./chains.js";
import { HostStorage } from "./storage.js";
import { walletProvider } from "./wallet.js";

/** The words 0xbow's app and SDK sample sign; they are part of the derived keys. */
const DERIVATION_PURPOSE =
    "This signature is used to deterministically derive application-specific secrets from your master seed. It is not a transaction and will not cost any gas.";

type Route = "transfer" | "withdraw" | { router: Address };

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

/** The keystore's published viewing keys: zero for an address that hasn't opened an account. */
const KEYSTORE_VIEWING_KEYS_ABI = [
    {
        type: "function",
        name: "viewingKeys",
        inputs: [{ name: "_account", type: "address" }],
        outputs: [{ name: "viewingKey_", type: "bytes32" }],
        stateMutability: "view",
    },
] as const;

/**
 * Gas a shield can take, approvals included (a yield deposit through a
 * router runs to about 700k), for what to leave in the wallet when shielding ETH.
 */
const SHIELD_GAS_RESERVE = 1_000_000n;

interface Account {
    chain: ChainConfig;
    owner: Address;
    keys: ProtocolKeys;
    storage: HostStorage;
}

/** A quote the user is looking at, kept until they confirm or it's replaced. */
type Pending = { id: number } & (
    | { kind: "withdraw"; account: Account; session: EvmPoolSession; prepared: PrepareWithdrawResult }
    | { kind: "yieldWithdraw"; account: Account; router: Address; relayParams: WithdrawalRelayParams; spent: Hex[] }
    | { kind: "transfer"; account: Account; session: EvmPoolSession; prepared: PrepareTransferResult }
);

export interface Balance {
    symbol: string;
    decimals: number;
    /** Spendable now, in the asset the user holds (USDT, not ppUSDT shares). */
    spendable: string;
    /** Deposited, waiting for 0xbow's screening. */
    pending: string;
    /** The deposits waiting, each with the label 0xbow's screening approves it by. */
    pendingDeposits: { commitment: Hex; label: Hex; value: string }[];
    /** The most one send or withdrawal can move: the largest deposit lineage. */
    maxSingle: string;
    /** Deposits screening declined, which only an exit can take back out. */
    declined: { commitment: Hex; value: string }[];
}

export class Engine {
    private hashService: IHashService | null = null;
    private readonly accounts = new Map<string, Account>();
    private pending: Pending | null = null;
    /** Numbers each quote, so a confirm can only send the quote the user is looking at. */
    private quotes = 0;

    constructor(
        private readonly bridge: Bridge,
        private readonly chains: ChainConfig[],
        private readonly circuitsBase: string,
        private readonly cacheStore: ICacheStore,
        private readonly logger: ILogger,
    ) {}

    listChains() {
        return this.chains.map((c) => ({
            chainId: c.chainId,
            name: c.name,
            explorerUrl: c.explorerUrl,
            assets: c.assets.map((a) => ({ symbol: a.symbol, token: a.token, decimals: a.decimals, yield: a.yield !== null })),
        }));
    }

    /** The EIP-712 message the wallet signs once to make the account's keys. */
    derivationRequest(owner: Address) {
        return {
            domain: {
                name: "Standardized Secret Derivation",
                version: "1",
                verifyingContract: "0x0000000000000000000000000000000000000000",
                salt: keccak256(toHex(APP_IDENTIFIER)),
            },
            message: { purpose: DERIVATION_PURPOSE, addressHash: keccak256(toBytes(owner)) },
            primaryType: "SecretDerivation",
            types: {
                EIP712Domain: [
                    { name: "name", type: "string" },
                    { name: "version", type: "string" },
                    { name: "verifyingContract", type: "address" },
                    { name: "salt", type: "bytes32" },
                ],
                SecretDerivation: [
                    { name: "purpose", type: "string" },
                    { name: "addressHash", type: "bytes32" },
                ],
            },
        };
    }

    /**
     * Opens the account, making its keys from `signatures` the first time.
     * The wallet signs twice and both must match: keys from a wallet whose
     * signatures change each time (some smart-contract and MPC wallets) could
     * never be made again, and the on-chain nullifying key can't be changed.
     */
    async open(chainId: number, owner: Address, signatures?: Hex[]) {
        const chain = this.chain(chainId);
        const storage = new HostStorage(this.bridge, `acct:${chainId}:${owner.toLowerCase()}:`);
        let keys = await storage.get<ProtocolKeys>("keys");
        if (!keys) {
            if (!signatures || signatures.length !== 2) {
                throw new EngineError("needs_signature", "Sign the account message in your wallet twice");
            }
            const [first, second] = signatures as [Hex, Hex];
            if (first.toLowerCase() !== second.toLowerCase()) {
                throw new EngineError(
                    "wallet_not_deterministic",
                    "Your wallet signed the same message two different ways, so the account's keys couldn't be made again. Use a wallet with standard signatures.",
                );
            }
            const derived = new CryptoService().deriveKeysFromSignature({
                signature: first,
                signerAddress: owner,
                addressHash: keccak256(toBytes(owner)),
                revocableKeyIndex: "0x0",
            });
            keys = {
                privateNullifyingKey: derived.privateNullifyingKey,
                privateRevocableKey: derived.privateRevocableKey,
                revocableKeyIndex: "0x0" as Hex,
                viewingPrivateKey: derived.viewingPrivateKey,
                viewingPublicKey: derived.viewingPublicKey,
            };
            await storage.set("keys", keys);
        }
        const account: Account = { chain, owner, keys, storage };
        this.accounts.set(accountKey(chainId, owner), account);
        const session = await this.session(account, "transfer");
        return { registered: await session.isKeystoreRegistered() };
    }

    /** Publishes the account's keys on chain, from the user's wallet. Needed once before spending. */
    async register(chainId: number, owner: Address) {
        const session = await this.session(this.account(chainId, owner), "transfer");
        const receipts = await session.registerKeystore();
        return { txHashes: receipts.map((r) => r.txHash) };
    }

    /** Scans the chain for the account's notes and adds up what it holds. */
    async sync(chainId: number, owner: Address): Promise<Balance[]> {
        const account = this.account(chainId, owner);
        const session = await this.session(account, "transfer");
        this.bridge.emit("progress", { stage: "sync" });
        await session.discoverNotes({ fromBlock: account.chain.fromBlock });
        const notes = (await session.exportAccount()).notes as Note[];
        const inflight = await this.inflight(account);
        return Promise.all(account.chain.assets.map((asset) => this.balance(session, asset, notes, inflight)));
    }

    /** What the wallet holds of `symbol`, and for ETH what to leave in it for gas. */
    async walletBalance(chainId: number, owner: Address, symbol: string) {
        const chain = this.chain(chainId);
        const asset = this.asset(chain, symbol);
        const client = this.publicClient(chain);
        if (asset.token === NATIVE) {
            const [balance, gasPrice] = await Promise.all([client.getBalance({ address: owner }), client.getGasPrice()]);
            return { balance: balance.toString(), reserve: (gasPrice * SHIELD_GAS_RESERVE).toString() };
        }
        const balance = await client.readContract({ address: asset.token, abi: erc20Abi, functionName: "balanceOf", args: [owner] });
        return { balance: balance.toString(), reserve: "0" };
    }

    /**
     * What shielding `amount` (the most it takes from the wallet, in the
     * asset's smallest unit) comes to, without sending anything: what the
     * account will hold, the screening fee, what leaves the wallet (`exact`,
     * or at most that: a yield deposit's router pulls what the shares cost
     * when it runs), how many approvals the wallet asks for before the
     * shield, and the smallest shield the pool takes.
     */
    async shieldPlan(chainId: number, owner: Address, symbol: string, amount: string) {
        const account = this.account(chainId, owner);
        const asset = this.asset(account.chain, symbol);
        const budget = positive(amount);
        const session = await this.session(account, "transfer");
        if (asset.yield) {
            const y = this.yieldOf(session, asset);
            const terms = await this.assetTerms(account.chain, asset.yield.ppUSDC);
            const minimum = terms.minAmount > 0n ? await y.token.previewRedeem(terms.minAmount) : 0n;
            let shares: bigint;
            let maxUnderlyingIn: bigint;
            try {
                ({ shares, maxUnderlyingIn } = await y.deposit.sizeSharesForBudget(budget));
            } catch {
                throw new EngineError("below_minimum", minimum.toString());
            }
            if (shares <= 0n || shares < terms.minAmount) throw new EngineError("below_minimum", minimum.toString());
            const prepared = await y.deposit.prepareYieldDeposit({ value: shares, maxUnderlyingIn });
            // The router mints the note's shares plus the screening fee's, and pulls at most maxUnderlyingIn for them.
            const feeShares = (shares * terms.vettingFeeBPS) / 10_000n;
            return {
                hold: (await y.token.previewRedeem(shares)).toString(),
                fee: (feeShares > 0n ? await y.token.previewRedeem(feeShares) : 0n).toString(),
                cost: maxUnderlyingIn.toString(),
                exact: false,
                approvals: prepared.approvalTxs.length,
                minimum: minimum.toString(),
            };
        }
        const terms = await this.assetTerms(account.chain, asset.token);
        const note = plainNote(budget, terms.vettingFeeBPS);
        if (note.value <= 0n || note.value < terms.minAmount) throw new EngineError("below_minimum", terms.minAmount.toString());
        let approvals = 0;
        if (asset.token !== NATIVE) {
            const allowance = await this.publicClient(account.chain).readContract({
                address: asset.token,
                abi: erc20Abi,
                functionName: "allowance",
                args: [owner, account.chain.entrypoint],
            });
            if (allowance < note.value + note.fee) approvals = 1;
        }
        return {
            hold: note.value.toString(),
            fee: note.fee.toString(),
            cost: (note.value + note.fee).toString(),
            exact: true,
            approvals,
            minimum: terms.minAmount.toString(),
        };
    }

    /** Whether `recipient` has a Privacy Pools account on the chain, so a private send can reach it. */
    async canReceive(chainId: number, recipient: Address) {
        const chain = this.chain(chainId);
        const key = await this.publicClient(chain).readContract({
            address: chain.keystore,
            abi: KEYSTORE_VIEWING_KEYS_ABI,
            functionName: "viewingKeys",
            args: [recipient],
        });
        return { registered: BigInt(key) !== 0n };
    }

    /**
     * Shields from the user's wallet: `amount`, in the asset's smallest unit,
     * is the most it takes, the screening fee included (the account holds a
     * little less; shieldPlan says how much).
     */
    async shield(chainId: number, owner: Address, symbol: string, amount: string) {
        const account = this.account(chainId, owner);
        const asset = this.asset(account.chain, symbol);
        const session = await this.session(account, "transfer");
        const value = positive(amount);
        let txHashes: Hex[];
        if (asset.yield) {
            const y = this.yieldOf(session, asset);
            const sized = await y.deposit.sizeSharesForBudget(value);
            const prepared = await y.deposit.prepareYieldDeposit({ value: sized.shares, maxUnderlyingIn: sized.maxUnderlyingIn });
            txHashes = [];
            const provider = this.provider(account);
            for (const tx of [...prepared.approvalTxs, prepared.depositTx]) {
                const hash = (await provider.request({
                    method: "eth_sendTransaction",
                    params: [{ from: owner, to: tx.to, data: tx.data, value: toHex(BigInt(tx.value ?? 0)) }],
                })) as Hex;
                txHashes.push(hash);
                this.bridge.emit("progress", { stage: "confirming", txHash: hash });
                const receipt = await this.publicClient(account.chain).waitForTransactionReceipt({ hash });
                if (receipt.status !== "success") throw new EngineError("reverted", `Transaction ${hash} reverted`);
            }
        } else {
            const terms = await this.assetTerms(account.chain, asset.token);
            const note = plainNote(value, terms.vettingFeeBPS);
            const receipt = await session.deposit({ tokenId: asset.token, value: toHex(note.value) });
            txHashes = [receipt.txHash];
        }
        await session.discoverNotes({ fromBlock: account.chain.fromBlock });
        return { txHashes };
    }

    /**
     * Prepares a withdrawal to `recipient` (a fresh address is best) and
     * returns the relayer's fee for the user to accept. `amount`, in the
     * asset's smallest unit, is what arrives; the relayer's flat fee comes on
     * top. "max" sends the largest single lineage, less the fee.
     */
    async quoteWithdraw(chainId: number, owner: Address, symbol: string, amount: string, recipient: Address) {
        const account = this.account(chainId, owner);
        const asset = this.asset(account.chain, symbol);
        this.pending = null;
        if (asset.yield) {
            const router = asset.yield.ppRouter;
            const session = await this.session(account, { router });
            const y = this.yieldOf(session, asset);
            const relayer = this.relayerFor(account.chain, { router });
            const notes = await this.spendableNotes(account, session, asset.yield.ppUSDC);
            const asked = amount === "max" ? null : await sharesAtLeast(y, positive(amount));
            const fee = await this.withdrawalFee(relayer, asset.yield.ppUSDC, recipient, asked ?? largestLineage(notes));
            const { inputs, units: shares } = coverWithFee(notes, asked, fee);
            const quote = await this.relayerInteractor(relayer).getWithdrawalQuote(relayer, {
                asset: asset.yield.ppUSDC,
                commit: true,
                recipient,
                extraGas: false,
                amount: shares.toString(),
            });
            if (!quote.feeCommitment) throw new EngineError("no_quote", "The relayer didn't commit to a fee");
            if (BigInt(quote.amountSent) > inputs.total) throw new EngineError("fee_changed", "The relayer's fee went up; try again");
            this.bridge.emit("progress", { stage: "proving" });
            const prepared = await y.withdraw.prepareYieldWithdrawRelay({
                inputCommitments: inputs.commitments,
                amount: BigInt(quote.amountSent),
                amountOut: BigInt(quote.amountSent),
                recipient,
                signedFeeCommitment: quote.feeCommitment,
            });
            this.pending = { id: ++this.quotes, kind: "yieldWithdraw", account, router, relayParams: prepared.relayParams, spent: inputs.commitments };
            return {
                id: this.quotes,
                receive: prepared.quotedUnderlyingOut.toString(),
                fee: (await y.token.previewRedeem(BigInt(quote.feeCommitment.feeAmount))).toString(),
                expiresAt: quote.feeCommitment.expiration,
            };
        }

        const session = await this.session(account, "withdraw");
        const relayer = this.relayerFor(account.chain, "withdraw");
        const notes = await this.spendableNotes(account, session, asset.token);
        const asked = amount === "max" ? null : positive(amount);
        const fee = await this.withdrawalFee(relayer, asset.token, recipient, asked ?? largestLineage(notes));
        const { inputs, units } = coverWithFee(notes, asked, fee);
        const prepared = await session.prepareWithdraw({
            inputCommitments: inputs.commitments,
            amount: toHex(units),
            tokenId: asset.token,
            recipientAddress: recipient,
        });
        const option = prepared.relayerOptions[0];
        if (!option) throw new EngineError("no_quote", "No relayer quoted this withdrawal");
        const { quote } = option.selectedQuote;
        if (BigInt(quote.amountSent) > inputs.total) throw new EngineError("fee_changed", "The relayer's fee went up; try again");
        this.pending = { id: ++this.quotes, kind: "withdraw", account, session, prepared };
        return { id: this.quotes, receive: quote.amountReceived, fee: quote.feeCommitment.feeAmount, expiresAt: quote.feeCommitment.expiration };
    }

    /**
     * Prepares a private send to another wallet address; the recipient finds
     * it with their own keys. `amount` is what they get; the relayer's fee
     * comes on top, and "max" sends the largest single lineage less the fee.
     */
    async quoteSend(chainId: number, owner: Address, symbol: string, amount: string, recipient: Address) {
        const account = this.account(chainId, owner);
        const asset = this.asset(account.chain, symbol);
        this.pending = null;
        const session = await this.session(account, "transfer");
        const relayer = this.relayerFor(account.chain, "transfer");
        const tokenId = asset.yield ? asset.yield.ppUSDC : asset.token;
        const notes = await this.spendableNotes(account, session, tokenId);
        const y = asset.yield ? this.yieldOf(session, asset) : null;
        const asked = amount === "max" ? null : y ? await sharesAtLeast(y, positive(amount)) : positive(amount);
        // A committed quote is the shape the SDK accepts; this one only tells the fee and expires unused.
        const quoted = await this.relayerInteractor(relayer).getTransferQuote(relayer, { asset: tokenId, commit: true });
        const { inputs, units } = coverWithFee(notes, asked, BigInt(quoted.feeCommitment.feeAmount));
        this.bridge.emit("progress", { stage: "proving" });
        if (!(await this.canReceive(chainId, recipient)).registered) {
            throw new EngineError("recipient_unregistered", "This address has no Privacy Pools account on this chain");
        }
        const prepared = await session.prepareTransfer({
            inputCommitments: inputs.commitments,
            amount: toHex(units),
            tokenId,
            recipientDiscoveryData: { evmAddress: recipient },
        });
        const option = prepared.relayOptions[0];
        if (!option) throw new EngineError("no_quote", "No relayer quoted this send");
        const { feeCommitment } = option.selectedQuote.quote;
        const fee = BigInt(feeCommitment.feeAmount);
        if (units + fee > inputs.total) throw new EngineError("fee_changed", "The relayer's fee went up; try again");
        this.pending = { id: ++this.quotes, kind: "transfer", account, session, prepared };
        const value = (shares: bigint) => (y ? y.token.previewRedeem(shares) : Promise.resolve(shares));
        return {
            id: this.quotes,
            receive: (await value(units)).toString(),
            fee: (await value(fee)).toString(),
            expiresAt: feeCommitment.expiration,
        };
    }

    /**
     * The relayer's current fee for a withdrawal. A committed quote is the
     * shape the SDK accepts; this one only tells the fee and expires unused.
     */
    private async withdrawalFee(relayer: RelayerInfo, asset: Address, recipient: Address, amount: bigint): Promise<bigint> {
        if (amount <= 0n) throw new EngineError("nothing_spendable", "Nothing is ready to spend yet");
        const quote = await this.relayerInteractor(relayer).getWithdrawalQuote(relayer, {
            asset,
            commit: true,
            recipient,
            extraGas: false,
            amount: amount.toString(),
        });
        return BigInt(quote.feeCommitment.feeAmount);
    }

    /** Sends the quote the user accepted (`id`, as its quote returned it) through the relayer. */
    async confirm(id?: number) {
        const pending = this.pending;
        if (!pending) throw new EngineError("no_quote", "Nothing is waiting to be sent");
        if (id !== undefined && pending.id !== id) throw new EngineError("quote_replaced", "A newer quote replaced this one");
        this.pending = null;
        const { account } = pending;
        const spent =
            pending.kind === "withdraw"
                ? pending.prepared.relayerOptions[0]!.inputCommitments
                : pending.kind === "transfer"
                  ? pending.prepared.relayOptions[0]!.inputCommitments
                  : pending.spent;
        // Hidden from the balance while the relay runs. If it fails the notes
        // come back: spending them again can't spend twice (their nullifiers
        // can only be used once), so the worst a retry after a relay that did
        // land costs is a refused request.
        await this.markInflight(account, spent, "relaying");
        this.bridge.emit("progress", { stage: "relaying" });
        let txHash: Hex;
        try {
            if (pending.kind === "withdraw") {
                this.bridge.emit("progress", { stage: "proving" });
                txHash = (await pending.session.relayWithdraw(pending.prepared.relayerOptions[0]!)).txHash;
            } else if (pending.kind === "transfer") {
                this.bridge.emit("progress", { stage: "proving" });
                txHash = (await pending.session.relayTransfer(pending.prepared.relayOptions[0]!)).txReceipt.txHash;
            } else {
                const relayer = this.relayerFor(account.chain, { router: pending.router });
                txHash = await this.relayerInteractor(relayer).relayWithdrawal(relayer, pending.relayParams);
            }
        } catch (error) {
            await this.clearInflight(account, spent);
            throw error;
        }
        await this.markInflight(account, spent, txHash);
        this.bridge.emit("progress", { stage: "confirming", txHash });
        const receipt = await this.publicClient(account.chain).waitForTransactionReceipt({ hash: txHash });
        const session = await this.session(account, "transfer");
        await session.discoverNotes({ fromBlock: account.chain.fromBlock });
        await this.clearInflight(account, spent);
        if (receipt.status !== "success") throw new EngineError("reverted", `Transaction ${txHash} reverted`);
        return { txHash };
    }

    /**
     * Takes a deposit back out to the address it came from, publicly, without
     * screening: the way out for a deposit 0xbow declined, or whenever the
     * user wants it. The wallet sends it and pays the gas.
     */
    async exit(chainId: number, owner: Address, commitment: Hex) {
        const account = this.account(chainId, owner);
        const session = await this.session(account, "transfer");
        const receipt = await session.rageQuit({ commitment });
        await session.discoverNotes({ fromBlock: account.chain.fromBlock });
        return { txHash: receipt.txHash };
    }

    // --- sessions -----------------------------------------------------------

    private async session(account: Account, route: Route): Promise<EvmPoolSession> {
        const { chain } = account;
        this.hashService ??= await PoseidonHashService.create();
        const rpcInteractor = new ViemRPCInteractor(
            { publicClient: this.publicClient(chain), chunkSize: chain.logsChunk, minFromBlock: BigInt(chain.fromBlock) },
            { logger: this.logger },
        );
        const relayer = this.relayerFor(chain, route);
        const ipfs = new IpfsCircuitArtifacts(
            { ipfsGatewayUrls: [...DEFAULT_IPFS_GATEWAY_URLS], circuitManifest: DEFAULT_CIRCUIT_MANIFEST, httpClient: new HTTPClient() },
            { cacheStore: this.cacheStore, logger: this.logger },
        );
        const builder = PoolSessionBuilder.fromConfig({
            chainId: chain.chainId,
            rpcUrl: chain.rpcUrls[0]!,
            ownerAddress: account.owner,
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
            relayers: [relayer],
            logger: this.logger,
            walletInteractor: { type: "EIP1193", provider: this.provider(account) as never },
        })
            .withRpcInteractor(rpcInteractor)
            .withHashService(this.hashService)
            .withStorageService(account.storage)
            .withCircuitArtifacts(new AppCircuitArtifacts(DEFAULT_CIRCUIT_MANIFEST, this.circuitsBase, ipfs))
            .withYieldDeployments(chain.assets.flatMap((a) => (a.yield ? [a.yield] : [])));
        return (await builder.create()) as EvmPoolSession;
    }

    private relayerFor(chain: ChainConfig, route: Route): RelayerInfo {
        const processorAddress = route === "transfer" ? chain.relayerSigner : route === "withdraw" ? chain.relay : route.router;
        return {
            name: "0xbow",
            url: chain.relayerUrl,
            chainId: chain.chainId,
            chainType: "evm",
            status: "active",
            address: chain.relayerSigner,
            processorAddress,
        };
    }

    private relayerInteractor(relayer: RelayerInfo): RelayerInteractor {
        return new RelayerInteractor({ relayers: [relayer], httpClient: new HTTPClient(), addressSchema: AddressSchema });
    }

    private publicClient(chain: ChainConfig) {
        return createPublicClient({
            transport: fallback(chain.rpcUrls.map((url) => http(url, { retryCount: 1, timeout: 30_000 }))),
            pollingInterval: 2_000,
        });
    }

    private provider(account: Account) {
        const client = this.publicClient(account.chain);
        return walletProvider(this.bridge, account.chain.chainId, account.owner, (method, params) =>
            client.request({ method, params } as never),
        );
    }

    // --- notes --------------------------------------------------------------

    private async balance(session: EvmPoolSession, asset: Asset, notes: Note[], inflight: Set<string>): Promise<Balance> {
        const tokenId = (asset.yield ? asset.yield.ppUSDC : asset.token).toLowerCase();
        const mine = notes.filter((n) => n.tokenId.toLowerCase() === tokenId && BigInt(n.value) > 0n);
        const active = mine.filter((n) => n.status === NoteStatus.ACTIVE && !inflight.has(n.commitment.toLowerCase()));
        const sum = (list: Note[]) => list.reduce((total, n) => total + BigInt(n.value), 0n);
        const lineages = new Map<string, bigint>();
        for (const n of active) lineages.set(n.label, (lineages.get(n.label) ?? 0n) + BigInt(n.value));
        const largest = [...lineages.values()].reduce((max, v) => (v > max ? v : max), 0n);
        const y = asset.yield ? this.yieldOf(session, asset) : null;
        const value = async (units: bigint) => (y && units > 0n ? await y.token.previewRedeem(units) : units);
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
            maxSingle: (await value(largest)).toString(),
            declined: await Promise.all(
                declined.map(async (n) => ({ commitment: n.commitment, value: (await value(BigInt(n.value))).toString() })),
            ),
        };
    }

    private async spendableNotes(account: Account, session: EvmPoolSession, tokenId: Address): Promise<Note[]> {
        await session.discoverNotes({ fromBlock: account.chain.fromBlock });
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

    private chain(chainId: number): ChainConfig {
        const chain = this.chains.find((c) => c.chainId === chainId);
        if (!chain) throw new EngineError("unknown_chain", `Chain ${chainId} isn't served`);
        return chain;
    }

    private account(chainId: number, owner: Address): Account {
        const account = this.accounts.get(accountKey(chainId, owner));
        if (!account) throw new EngineError("not_open", "Open the account first");
        return account;
    }

    private asset(chain: ChainConfig, symbol: string): Asset {
        const asset = chain.assets.find((a) => a.symbol === symbol);
        if (!asset) throw new EngineError("unknown_asset", `${symbol} isn't served on ${chain.name}`);
        return asset;
    }

    private async assetTerms(chain: ChainConfig, token: Address): Promise<{ minAmount: bigint; vettingFeeBPS: bigint }> {
        const config = await this.publicClient(chain).readContract({
            address: chain.entrypoint,
            abi: ENTRYPOINT_ASSETS_ABI,
            functionName: "assets",
            args: [token],
        });
        if (!config.enabled) throw new EngineError("unknown_asset", "The pool isn't taking this asset");
        return { minAmount: config.minAmount, vettingFeeBPS: config.vettingFeeBPS };
    }

    private yieldOf(session: EvmPoolSession, asset: Asset): YieldSessions {
        const y = asset.yield ? session.yieldFor(asset.yield.ppUSDC) : undefined;
        if (!y) throw new EngineError("unknown_asset", `${asset.symbol} has no yield session`);
        return y;
    }
}

function accountKey(chainId: number, owner: Address): string {
    return `${chainId}:${owner.toLowerCase()}`;
}

/**
 * A plain deposit's note out of the most the wallet gives: the entrypoint
 * takes the note's value plus a screening fee of `bps` on top.
 */
export function plainNote(budget: bigint, bps: bigint): { value: bigint; fee: bigint } {
    let value = (budget * 10_000n) / (10_000n + bps);
    while (value > 0n && value + (value * bps) / 10_000n > budget) value -= 1n;
    return { value, fee: (value * bps) / 10_000n };
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

/** The total of the largest single lineage, the most one spend can move. */
function largestLineage(notes: Note[]): bigint {
    const totals = new Map<string, bigint[]>();
    for (const n of notes) totals.set(n.label, [...(totals.get(n.label) ?? []), BigInt(n.value)]);
    let best = 0n;
    for (const values of totals.values()) {
        const top = values.sort((x, y) => (y > x ? 1 : -1)).slice(0, MAX_INPUTS).reduce((a, b) => a + b, 0n);
        if (top > best) best = top;
    }
    return best;
}

/**
 * Inputs that cover `asked` plus the relayer's `fee`, and the amount that
 * goes out: `asked`, or for "max" (null) the largest lineage less the fee.
 */
export function coverWithFee(notes: Note[], asked: bigint | null, fee: bigint): { inputs: { commitments: Hex[]; total: bigint }; units: bigint } {
    if (asked === null) {
        const inputs = pickLineage(notes, null);
        const units = inputs.total - fee;
        if (units <= 0n) throw new EngineError("below_fee", "The balance is smaller than the relayer's fee");
        return { inputs, units };
    }
    return { inputs: pickLineage(notes, asked + fee), units: asked };
}

/** The SDK spends notes of one deposit lineage at a time, at most two here (the bundled circuits). */
const MAX_INPUTS = 2;

/**
 * Picks inputs for one spend: notes of a single lineage (a spend can't mix
 * deposits), smallest set that covers `wanted`, or the whole of the largest
 * lineage that fits the input limit when `wanted` is null ("max").
 */
export function pickLineage(notes: Note[], wanted: bigint | null): { commitments: Hex[]; total: bigint } {
    const byLabel = new Map<string, Note[]>();
    for (const n of notes) byLabel.set(n.label, [...(byLabel.get(n.label) ?? []), n]);
    let best: { commitments: Hex[]; total: bigint } | null = null;
    for (const lineage of byLabel.values()) {
        const sorted = [...lineage].sort((x, y) => (BigInt(y.value) > BigInt(x.value) ? 1 : -1)).slice(0, MAX_INPUTS);
        let total = 0n;
        const chosen: Hex[] = [];
        for (const n of sorted) {
            if (wanted !== null && total >= wanted) break;
            chosen.push(n.commitment);
            total += BigInt(n.value);
        }
        if (wanted !== null && total < wanted) continue;
        const better = !best || (wanted === null ? total > best.total : chosen.length < best.commitments.length || total < best.total);
        if (better) best = { commitments: chosen, total };
    }
    if (!best) {
        throw new EngineError(
            wanted === null ? "nothing_spendable" : "split_needed",
            wanted === null ? "Nothing is ready to spend yet" : "No single deposit covers this amount; send it in parts",
        );
    }
    return best;
}
