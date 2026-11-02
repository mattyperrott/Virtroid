// SPDX-License-Identifier: GPL-3.0-or-later
//
// The user's own wallet, as the EIP-1193 provider the SDK signs and sends
// with. The app carries each request to the wallet over WalletConnect, so the
// user approves every signature and transaction in their wallet. Reads go to
// the chain's RPC instead: wallets don't all answer them, and they don't need
// the user.

import type { Bridge } from "./bridge.js";

export interface Eip1193Provider {
    request(args: { method: string; params?: unknown }): Promise<unknown>;
    on(event: string, listener: (...args: unknown[]) => void): void;
    removeListener(event: string, listener: (...args: unknown[]) => void): void;
}

/** What only the wallet can do. Everything else is a chain read. */
const WALLET_METHODS = new Set([
    "eth_requestAccounts",
    "eth_accounts",
    "eth_sendTransaction",
    "eth_signTypedData_v4",
    "personal_sign",
    "wallet_switchEthereumChain",
]);

export function walletProvider(
    bridge: Bridge,
    chainId: number,
    owner: string,
    read: (method: string, params: unknown) => Promise<unknown>,
): Eip1193Provider {
    return {
        async request({ method, params }) {
            if (method === "eth_chainId") return `0x${chainId.toString(16)}`;
            if (method === "eth_accounts" || method === "eth_requestAccounts") return [owner];
            if (!WALLET_METHODS.has(method)) return read(method, params ?? []);
            bridge.emit("wallet", { method, approval: isApproval(params) });
            return bridge.call("wallet", { chainId, method, params: params ?? [] });
        },
        on() {},
        removeListener() {},
    };
}

/** ERC-20 approve(address,uint256): the wallet's prompt is an allowance, not the operation itself. */
function isApproval(params: unknown): boolean {
    const tx = Array.isArray(params) ? (params[0] as { data?: unknown } | undefined) : undefined;
    return typeof tx?.data === "string" && tx.data.toLowerCase().startsWith("0x095ea7b3");
}
