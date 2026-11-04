// SPDX-License-Identifier: GPL-3.0-or-later
//
// The note owner's key as the EIP-1193 provider the SDK signs with, and the
// small sender every address the app holds keys for uses. Nothing here asks
// the user: the words are on the phone. Reads go to the chain's RPC.

import type { PublicClient } from "viem";
import type { PrivateKeyAccount } from "viem/accounts";
import type { Address, Hex } from "@privacy-pools-v2/evm-sdk";

export interface Eip1193Provider {
    request(args: { method: string; params?: unknown }): Promise<unknown>;
    on(event: string, listener: (...args: unknown[]) => void): void;
    removeListener(event: string, listener: (...args: unknown[]) => void): void;
}

export interface Fees {
    baseFee: bigint;
    maxFee: bigint;
    tip: bigint;
}

/** Below this the public mempool's builders pass a transaction over. */
const MIN_TIP = 20_000_000n; // 0.02 gwei

/** What to bid now: half again the base fee, so a few busy blocks don't strand the transaction. */
export async function feesNow(client: PublicClient): Promise<Fees> {
    const block = await client.getBlock({ blockTag: "latest" });
    const baseFee = block.baseFeePerGas ?? 0n;
    const quoted = await client.estimateMaxPriorityFeePerGas().catch(() => 0n);
    const tip = quoted > MIN_TIP ? quoted : MIN_TIP;
    return { baseFee, tip, maxFee: (baseFee * 3n) / 2n + tip };
}

export interface Send {
    to: Address;
    data?: Hex;
    value?: bigint;
    gas?: bigint;
    fees?: Fees;
}

/** Signs `tx` with `account` and sends it, estimating what it isn't given. */
export async function sendFrom(client: PublicClient, chainId: number, account: PrivateKeyAccount, tx: Send): Promise<Hex> {
    const fees = tx.fees ?? (await feesNow(client));
    // The mined count, so sending again after a stuck transaction replaces it instead of queuing behind it.
    const nonce = await client.getTransactionCount({ address: account.address });
    const value = tx.value ?? 0n;
    const data = tx.data ?? "0x";
    const gas = tx.gas ?? ((await client.estimateGas({ account: account.address, to: tx.to, data, value })) * 6n) / 5n;
    const raw = await account.signTransaction({
        chainId,
        type: "eip1559",
        to: tx.to,
        data,
        value,
        nonce,
        gas,
        maxFeePerGas: fees.maxFee,
        maxPriorityFeePerGas: fees.tip,
    });
    return client.sendRawTransaction({ serializedTransaction: raw });
}

export function ownerProvider(client: PublicClient, chainId: number, owner: PrivateKeyAccount): Eip1193Provider {
    return {
        async request({ method, params }) {
            const args = (params ?? []) as unknown[];
            switch (method) {
                case "eth_chainId":
                    return `0x${chainId.toString(16)}`;
                case "eth_accounts":
                case "eth_requestAccounts":
                    return [owner.address];
                case "wallet_switchEthereumChain":
                    return null;
                case "eth_signTypedData_v4": {
                    const typed = (typeof args[1] === "string" ? JSON.parse(args[1]) : args[1]) as {
                        domain: unknown;
                        types: Record<string, unknown>;
                        primaryType: unknown;
                        message: unknown;
                    };
                    const { EIP712Domain: _domain, ...types } = typed.types;
                    return owner.signTypedData({ domain: typed.domain, types, primaryType: typed.primaryType, message: typed.message } as never);
                }
                case "personal_sign":
                    return owner.signMessage({ message: { raw: args[0] as Hex } });
                case "eth_sendTransaction": {
                    const tx = args[0] as { to: Address; data?: Hex; value?: Hex; gas?: Hex };
                    return sendFrom(client, chainId, owner, {
                        to: tx.to,
                        data: tx.data,
                        value: tx.value ? BigInt(tx.value) : 0n,
                        gas: tx.gas ? BigInt(tx.gas) : undefined,
                    });
                }
                default:
                    return client.request({ method, params: args } as never);
            }
        },
        on() {},
        removeListener() {},
    };
}
