// SPDX-License-Identifier: GPL-3.0-or-later
//
// Moves USDC or USDT out of a deposit address that holds no ETH. The address
// delegates to a smart-account contract (EIP-7702) and sends one ERC-4337
// UserOperation; a bundler pays the gas and a token paymaster takes the cost
// back in the same token after the calls run.
//
// The paymaster is only ever allowed `cap`, which the caller has left in the
// address for it, so a wrong answer from the service can cost at most that.

import { erc20Abi, encodeFunctionData, toHex } from "viem";
import type { PublicClient } from "viem";
import type { PrivateKeyAccount } from "viem/accounts";
import { entryPoint08Address, formatUserOperationRequest, toSimple7702SmartAccount } from "viem/account-abstraction";
import type { Address, Hex } from "@privacy-pools-v2/evm-sdk";

import { EngineError } from "./bridge.js";
import type { GaslessConfig } from "./chains.js";

export interface Call {
    to: Address;
    data: Hex;
    value?: bigint;
}

export interface GasQuote {
    /** The token's smallest units one ETH costs at the paymaster. */
    rate: bigint;
    maxFee: bigint;
    tip: bigint;
}

export interface UserOpReceipt {
    success: boolean;
    txHash: Hex | null;
    /** The gas it paid for, in wei. */
    gasCost: bigint;
}

const WEI_PER_ETH = 10n ** 18n;
const addPercent = (value: bigint, percent: bigint) => value + (value * percent) / 100n;

/** The most a UserOperation with these limits can cost, in the token. */
export function tokenCost(gas: bigint, maxFee: bigint, rate: bigint): bigint {
    return (gas * maxFee * rate) / WEI_PER_ETH + 1n;
}

export class Gasless {
    constructor(
        private readonly client: PublicClient,
        private readonly chainId: number,
        private readonly config: GaslessConfig,
    ) {}

    private get chainHex() {
        return toHex(this.chainId);
    }

    private async rpc<T>(method: string, params: unknown[]): Promise<T> {
        let response: Response;
        try {
            response = await fetch(this.config.url, {
                method: "POST",
                headers: { "content-type": "application/json" },
                body: JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }),
            });
        } catch (error) {
            throw new EngineError("gas_service", `The gas service didn't answer: ${String(error)}`);
        }
        const body = (await response.json().catch(() => null)) as { result?: T; error?: { message?: string } } | null;
        if (!body || body.error || body.result === undefined) {
            throw new EngineError("gas_service", `${method}: ${body?.error?.message ?? `HTTP ${response.status}`}`);
        }
        return body.result;
    }

    /** What gas costs in `token` right now. */
    async quote(token: Address): Promise<GasQuote> {
        const supported = await this.rpc<{
            paymasterMetadata: { address: string };
            tokens: { address: string; exchangeRate: string }[];
        }>("pm_supportedERC20Tokens", [entryPoint08Address]);
        if (supported.paymasterMetadata.address.toLowerCase() !== this.config.paymaster.toLowerCase()) {
            throw new EngineError("gas_service", `The gas service names a paymaster the app doesn't know: ${supported.paymasterMetadata.address}`);
        }
        const entry = supported.tokens.find((t) => t.address.toLowerCase() === token.toLowerCase());
        if (!entry) throw new EngineError("gas_service", `The gas service doesn't take ${token}`);
        const fees = await this.rpc<{ maxFeePerGas: string; maxPriorityFeePerGas: string }>("voltaire_feesPerGas", []);
        return { rate: BigInt(entry.exchangeRate), maxFee: BigInt(fees.maxFeePerGas), tip: BigInt(fees.maxPriorityFeePerGas) };
    }

    /**
     * Sends `calls` from `owner`'s address, paying for gas in `token`, at most
     * `cap` of it. Returns the UserOperation's hash, or throws `gas_cap` with
     * the cost it needs when the bundler's limits come to more than `cap`.
     */
    async send(owner: PrivateKeyAccount, token: Address, calls: Call[], cap: bigint, quote: GasQuote): Promise<Hex> {
        const account = await toSimple7702SmartAccount({ client: this.client, owner, implementation: this.config.delegate });
        const paymaster = this.config.paymaster;
        const allowance = await this.client.readContract({
            address: token,
            abi: erc20Abi,
            functionName: "allowance",
            args: [owner.address, paymaster],
        });
        const approve = (amount: bigint): Call => ({
            to: token,
            data: encodeFunctionData({ abi: erc20Abi, functionName: "approve", args: [paymaster, amount] }),
        });
        // USDT's approve reverts unless the allowance is zero first, so a leftover is reset.
        const all = [...(allowance > 0n ? [approve(0n)] : []), approve(cap), ...calls];

        const code = await this.client.getCode({ address: owner.address });
        const delegated = code?.toLowerCase() === `0xef0100${this.config.delegate.slice(2).toLowerCase()}`;
        const authorization = delegated
            ? undefined
            : await owner.signAuthorization({
                  chainId: this.chainId,
                  contractAddress: this.config.delegate,
                  nonce: await this.client.getTransactionCount({ address: owner.address }),
              });

        const op: Record<string, unknown> = {
            sender: owner.address,
            nonce: await account.getNonce(),
            callData: await account.encodeCalls(all.map((c) => ({ to: c.to, data: c.data, value: c.value ?? 0n }))),
            callGasLimit: 0n,
            verificationGasLimit: 0n,
            preVerificationGas: 0n,
            maxFeePerGas: quote.maxFee,
            maxPriorityFeePerGas: quote.tip,
            signature: await account.getStubSignature(),
            ...(authorization ? { authorization } : {}),
        };
        const context = { token };
        const format = () => formatUserOperationRequest(op as never);
        const applyPaymaster = (fields: Record<string, string | undefined>) => {
            if (fields.paymaster) op.paymaster = fields.paymaster;
            if (fields.paymasterData) op.paymasterData = fields.paymasterData;
            if (fields.paymasterVerificationGasLimit) op.paymasterVerificationGasLimit = BigInt(fields.paymasterVerificationGasLimit);
            if (fields.paymasterPostOpGasLimit) op.paymasterPostOpGasLimit = BigInt(fields.paymasterPostOpGasLimit);
        };

        applyPaymaster(await this.rpc("pm_getPaymasterStubData", [format(), entryPoint08Address, this.chainHex, context]));
        // Zero fees while estimating, as the service's own client sends: it can refuse a sender that can't cover the fees.
        op.maxFeePerGas = 0n;
        op.maxPriorityFeePerGas = 0n;
        const estimate = await this.rpc<Record<string, string | undefined>>("eth_estimateUserOperationGas", [format(), entryPoint08Address]);
        op.maxFeePerGas = quote.maxFee;
        op.maxPriorityFeePerGas = quote.tip;
        op.callGasLimit = addPercent(BigInt(estimate.callGasLimit!), 10n);
        op.verificationGasLimit = addPercent(BigInt(estimate.verificationGasLimit!), 10n);
        op.preVerificationGas = addPercent(BigInt(estimate.preVerificationGas!), 5n);
        applyPaymaster(estimate);

        const needed = tokenCost(limits(op), quote.maxFee, quote.rate);
        if (needed > cap) throw new EngineError("gas_cap", needed.toString());

        const final = await this.rpc<Record<string, string | undefined>>("pm_getPaymasterData", [format(), entryPoint08Address, this.chainHex, context]);
        applyPaymaster(final);
        // The approval above names this paymaster; a response naming another would run with no allowance.
        if (String(op.paymaster).toLowerCase() !== paymaster.toLowerCase()) {
            throw new EngineError("gas_service", `The gas service named another paymaster: ${String(op.paymaster)}`);
        }

        op.signature = await account.signUserOperation(op as never);
        return this.rpc<Hex>("eth_sendUserOperation", [format(), entryPoint08Address]);
    }

    /** The UserOperation's result once it's in a block, else null. */
    async receipt(hash: Hex): Promise<UserOpReceipt | null> {
        const r = await this.rpc<null | {
            success: boolean;
            actualGasCost: string;
            receipt?: { transactionHash?: Hex };
        }>("eth_getUserOperationReceipt", [hash]).catch((error: unknown) => {
            if (error instanceof EngineError && /not found/i.test(error.message)) return null;
            throw error;
        });
        if (!r) return null;
        return { success: r.success, txHash: r.receipt?.transactionHash ?? null, gasCost: BigInt(r.actualGasCost) };
    }
}

function limits(op: Record<string, unknown>): bigint {
    const gas = (name: string) => (op[name] as bigint | undefined) ?? 0n;
    return (
        gas("callGasLimit") +
        gas("verificationGasLimit") +
        gas("preVerificationGas") +
        gas("paymasterVerificationGasLimit") +
        gas("paymasterPostOpGasLimit")
    );
}
