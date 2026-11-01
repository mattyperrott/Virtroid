// SPDX-License-Identifier: GPL-3.0-or-later
//
// Reads every address in src/chains.ts back from its chain and from 0xbow's
// relayer, and fails on the first that doesn't hold: code at each contract,
// the pool's own keystore and ASP registry, the relay's pool and entrypoint,
// each router's share, entrypoint and pool, each token's decimals, and the
// relayer's signer and yield routers. Run it before changing src/chains.ts:
//   node verify-chains.mjs
// It bundles src/chains.ts with esbuild to read it, so `pnpm sdk` comes first.

import { build } from "esbuild";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";

const here = dirname(fileURLToPath(import.meta.url));
const viem = await import(pathToFileURL(join(here, ".sdk/pp2/packages/evm-sdk/node_modules/viem/_esm/index.js")));
const { createPublicClient, http, parseAbi, getAddress } = viem;

const temp = await mkdtemp(join(tmpdir(), "verify-chains-"));
const out = join(temp, "chains.mjs");
await build({ entryPoints: [join(here, "src/chains.ts")], bundle: true, format: "esm", outfile: out, logLevel: "error" });
const { CHAINS, NATIVE } = await import(pathToFileURL(out));
await rm(temp, { recursive: true });

const abi = parseAbi([
    "function keystore() view returns (address)",
    "function aspRegistry() view returns (address)",
    "function POOL() view returns (address)",
    "function ENTRYPOINT() view returns (address)",
    "function VAULT() view returns (address)",
    "function POOL_VAULT() view returns (address)",
    "function decimals() view returns (uint8)",
]);

let failures = 0;
const check = (ok, what) => {
    console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
    if (!ok) failures++;
};
const same = (x, y) => getAddress(x) === getAddress(y);

for (const chain of CHAINS) {
    console.log(`\n${chain.name} (${chain.chainId})`);
    const client = createPublicClient({ transport: http(chain.rpcUrls[0]) });
    check((await client.getChainId()) === chain.chainId, `RPC ${chain.rpcUrls[0]} is chain ${chain.chainId}`);
    const read = (address, functionName) => client.readContract({ address, abi, functionName });
    const hasCode = async (address) => ((await client.getCode({ address })) ?? "0x").length > 2;

    for (const name of ["entrypoint", "pool", "keystore", "aspRegistry", "relay"]) {
        check(await hasCode(chain[name]), `code at ${name} ${chain[name]}`);
    }
    check(same(await read(chain.pool, "keystore"), chain.keystore), "pool.keystore() is the keystore");
    check(same(await read(chain.pool, "aspRegistry"), chain.aspRegistry), "pool.aspRegistry() is the ASP registry");
    check(same(await read(chain.relay, "POOL"), chain.pool), "relay.POOL() is the pool");
    check(same(await read(chain.relay, "ENTRYPOINT"), chain.entrypoint), "relay.ENTRYPOINT() is the entrypoint");

    for (const asset of chain.assets) {
        if (asset.token !== NATIVE) {
            check(Number(await read(asset.token, "decimals")) === asset.decimals, `${asset.symbol} has ${asset.decimals} decimals`);
        }
        const y = asset.yield;
        if (!y) continue;
        check(same(await read(y.ppRouter, "VAULT"), y.ppUSDC), `${asset.symbol} router's VAULT() is its share ${y.ppUSDC}`);
        check(same(await read(y.ppRouter, "ENTRYPOINT"), chain.entrypoint), `${asset.symbol} router's ENTRYPOINT() is the entrypoint`);
        check(same(await read(y.ppRouter, "POOL_VAULT"), chain.pool), `${asset.symbol} router's POOL_VAULT() is the pool`);
        check(Number(await read(y.ppUSDC, "decimals")) === y.ppUSDCDecimals, `${asset.symbol} share has ${y.ppUSDCDecimals} decimals`);
        check(Number(await read(y.underlying, "decimals")) === y.underlyingDecimals, `${asset.symbol} underlying has ${y.underlyingDecimals} decimals`);
        if (y.zap) check(await hasCode(y.zap), `code at ${asset.symbol} zap ${y.zap}`);
    }

    const response = await fetch(`${chain.relayerUrl}/v1/details/evm/${chain.chainId}`);
    check(response.ok, `relayer details for ${chain.chainId} (${response.status})`);
    if (response.ok) {
        const details = await response.json();
        const signer = details.relayerAddress ?? details.feeReceiverAddress;
        check(signer && same(signer, chain.relayerSigner), `relayer signer is ${chain.relayerSigner} (says ${signer})`);
        const routers = (details.yield ?? []).map((w) => getAddress(w.router));
        for (const asset of chain.assets.filter((x) => x.yield)) {
            check(routers.includes(getAddress(asset.yield.ppRouter)), `relayer relays ${asset.symbol}'s router`);
        }
    }
}

console.log(failures ? `\n${failures} check(s) failed` : "\nall checks passed");
process.exit(failures ? 1 : 0);
