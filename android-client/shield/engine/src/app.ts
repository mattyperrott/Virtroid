// SPDX-License-Identifier: GPL-3.0-or-later
//
// Starts the engine on a message port: connects the bridge, routes fetch
// through the app, and answers the app's requests. main.ts starts it in the
// app; the test page starts it on a local chain.

import { DEFAULT_IPFS_GATEWAY_URLS } from "@privacy-pools-v2/evm-sdk";
import type { Address, Hex, ICacheStore, ILogger } from "@privacy-pools-v2/evm-sdk";

import { Bridge, EngineError } from "./bridge.js";
import type { HostPort } from "./bridge.js";
import type { ChainConfig } from "./chains.js";
import { Engine } from "./engine.js";
import { fromBase64, installFetch, toBase64 } from "./net.js";

interface AccountParams {
    chainId: number;
    owner: Address;
}

export function start(port: HostPort, chains: ChainConfig[]): void {
    const bridge = new Bridge(port);
    installFetch(bridge, location.origin);

    /**
     * Only warnings and errors leave the engine, and only their message: the
     * SDK's debug output carries amounts and commitments, and the app keeps
     * what it gets in memory for its own log screen, never in logcat.
     */
    const hostLogger = (service = "engine"): ILogger => {
        const send = (level: string) => (msg: string) => bridge.emit("log", { level, service, msg });
        const quiet = () => {};
        return {
            level: "warn",
            child: (name: string) => hostLogger(`${service}/${name}`),
            debug: quiet,
            info: quiet,
            warn: send("warn"),
            error: send("error"),
            fatal: send("error"),
        };
    };

    /** Circuits the app doesn't bundle, once downloaded, kept in the app's cache directory. */
    const cacheStore: ICacheStore = {
        async get(key) {
            const text = await bridge.call<string | null>("cache.get", { key });
            return text === null ? null : fromBase64(text);
        },
        async set(key, value) {
            await bridge.call("cache.set", { key, value: toBase64(value) });
        },
    };

    const engine = new Engine(bridge, chains, `${location.origin}/assets/circuits`, cacheStore, hostLogger());

    bridge.serve(async (method, raw) => {
        const p = (raw ?? {}) as Record<string, unknown> & AccountParams;
        switch (method) {
            case "chains":
                return engine.listChains();
            case "derivation":
                return engine.derivationRequest(p.owner);
            case "open":
                return engine.open(p.chainId, p.owner, p.signatures as Hex[] | undefined);
            case "register":
                return engine.register(p.chainId, p.owner);
            case "sync":
                return engine.sync(p.chainId, p.owner);
            case "shield":
                return engine.shield(p.chainId, p.owner, String(p.symbol), String(p.amount));
            case "quoteWithdraw":
                return engine.quoteWithdraw(p.chainId, p.owner, String(p.symbol), String(p.amount), p.recipient as Address);
            case "quoteSend":
                return engine.quoteSend(p.chainId, p.owner, String(p.symbol), String(p.amount), p.recipient as Address);
            case "confirm":
                return engine.confirm();
            case "exit":
                return engine.exit(p.chainId, p.owner, p.commitment as Hex);
            default:
                throw new EngineError("unknown_method", `No engine method ${method}`);
        }
    });

    // The hosts the app lets the engine reach: the chains' RPCs, 0xbow's
    // relayer and ASP, and the IPFS gateway rarer circuits come from.
    const hosts = new Set<string>();
    for (const chain of chains) {
        for (const url of [...chain.rpcUrls, chain.relayerUrl, chain.aspUrl]) hosts.add(new URL(url).host);
    }
    for (const url of DEFAULT_IPFS_GATEWAY_URLS) hosts.add(new URL(url).host);
    bridge.emit("ready", { chains: chains.length, hosts: [...hosts] });
}
