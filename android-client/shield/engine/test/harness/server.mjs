// SPDX-License-Identifier: GPL-3.0-or-later
//
// Runs the engine end to end in a real Chromium against 0xbow's local e2e
// stack (anvil with their contracts, their relayer, their mock ASP), with
// this server playing the app's part: the network and the encrypted store (a
// plain map here), and the user's other wallet, which sends to the deposit
// address. Open http://127.0.0.1:8099/?run=1 (HARNESS_PORT to move it) and the
// page runs scenario.js, posting each step here; the steps print and land in
// harness-report.json.
//
//   node test/harness/server.mjs <0xbow monorepo>/packages/evm-sdk/e2e/deployments/local-8545.json
//
// Start the stack first: in the monorepo's packages/evm-sdk,
//   EVM_E2E=1 EVM_E2E_WORKERS=1 EVM_E2E_KEEP_ALIVE=1 npx vitest run --config e2e/vitest.config.ts -t none
// then the mock ASP on :3001 (packages/mock-asp: start:evm, with POSTMAN_PRIVATE_KEY the e2e deployer).

import { createServer } from "node:http";
import { readFile, writeFile } from "node:fs/promises";
import { dirname, join, normalize } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { build } from "esbuild";

import { engineOptions } from "../../build-options.mjs";
import { localChain } from "./local-chain.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const engineDir = join(here, "../..");
const assets = join(engineDir, "../src/main/assets");
const viemDir = join(engineDir, ".sdk/pp2/packages/evm-sdk/node_modules/viem/_esm");
const viem = await import(pathToFileURL(join(viemDir, "index.js")));
const { privateKeyToAccount, generatePrivateKey } = await import(pathToFileURL(join(viemDir, "accounts/index.js")));
const { createPublicClient, createWalletClient, http, parseAbi, defineChain } = viem;

// HARNESS_FORK=<rpc url>: the real contracts on a fork of mainnet at that url,
// with test/fork's stand-in gas service, and test/fork/scenario.js.
const FORK = process.env.HARNESS_FORK;
const PORT = Number(process.env.HARNESS_PORT ?? 8099);
const deployment = FORK ? null : JSON.parse(await readFile(process.argv[2], "utf8"));
const RPC = FORK ?? "http://127.0.0.1:8545";
// 0xbow's e2e keys (their packages/evm-sdk/e2e network.ts and relayer.ts): local chain only.
const DEPLOYER_KEY = "0x77d28084a5fdd3067a2f59f001775187199e1f1da27ffb5765545135cb21b7b3";
const RELAYER_KEY = "0xb9dacf9a665e631e115b28d780600d4238ff202e6c37c661d5c20c86005ec0f8";

const chain = defineChain({ id: FORK ? 1 : 11155111, name: "local", nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 }, rpcUrls: { default: { http: [RPC] } } });
const publicClient = createPublicClient({ chain, transport: http(RPC), pollingInterval: 200 });
// HARNESS_KEY keeps the paying wallet across restarts.
const user = privateKeyToAccount(process.env.HARNESS_KEY ?? generatePrivateKey());
const wallet = createWalletClient({ account: user, chain, transport: http(RPC) });

const localChainConfig = FORK ? await forkChain() : localChain(deployment, "127.0.0.1", privateKeyToAccount(RELAYER_KEY).address);

async function forkChain() {
    const out = join(here, ".out/chains.mjs");
    await build({ entryPoints: [join(engineDir, "src/chains.ts")], bundle: true, format: "esm", outfile: out, logLevel: "error" });
    const { CHAINS } = await import(pathToFileURL(out));
    const real = CHAINS[0];
    const head = await publicClient.getBlockNumber();
    return {
        ...real,
        rpcUrls: [RPC],
        // Nothing of this wallet's is older than the fork.
        fromBlock: `0x${(head - 2n).toString(16)}`,
        logsChunk: 5000,
        aspUrl: `http://localhost:${PORT}/asp`,
        gasless: { ...real.gasless, url: `http://localhost:${PORT}/gas`, paymaster: process.env.HARNESS_PAYMASTER },
    };
}

const store = new Map();
const cache = new Map();
const report = [];

const erc20 = parseAbi([
    "function mint(address to, uint256 amount)",
    "function balanceOf(address) view returns (uint256)",
]);

async function rpc(method, params) {
    const response = await fetch(RPC, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }) });
    const body = await response.json();
    if (body.error) throw new Error(`${method}: ${body.error.message}`);
    return body.result;
}

// What the app's Kotlin host does, for the engine's calls.
const host = {
    async http({ url, method, headers, body }) {
        const response = await fetch(url, { method, headers, body: body ? Buffer.from(body, "base64") : undefined });
        const responseHeaders = {};
        response.headers.forEach((value, name) => (responseHeaders[name] = value));
        return { status: response.status, headers: responseHeaders, body: Buffer.from(await response.arrayBuffer()).toString("base64") };
    },
    "store.get": ({ key }) => store.get(key) ?? null,
    "store.set": ({ key, value }) => void store.set(key, value),
    "store.delete": ({ key }) => void store.delete(key),
    "store.clear": ({ prefix }) => {
        for (const key of [...store.keys()]) if (key.startsWith(prefix)) store.delete(key);
    },
    "cache.get": ({ key }) => cache.get(key) ?? null,
    "cache.set": ({ key, value }) => void cache.set(key, value),
};

// Test-only controls the scenario uses: funding, the ASP's approvals, chain reads.
const control = {
    /** The user's other wallet sends ETH and test USDC to `address`, as a user would. */
    async send({ address, eth, usdc }) {
        await rpc("anvil_setBalance", [user.address, "0x56BC75E2D63100000"]); // 100 ETH
        if (eth) await publicClient.waitForTransactionReceipt({ hash: await wallet.sendTransaction({ to: address, value: BigInt(eth) }) });
        if (usdc) {
            const mint = await wallet.writeContract({ address: deployment.yieldUnderlyingAddress, abi: erc20, functionName: "mint", args: [address, BigInt(usdc)] });
            await publicClient.waitForTransactionReceipt({ hash: mint });
        }
        return null;
    },
    /** A wipe of the app's data, as on a new phone. */
    wipe() {
        store.clear();
        return null;
    },
    async approve({ label }) {
        const response = await fetch("http://127.0.0.1:3001/admin/approve-label", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ label }) });
        if (!response.ok) throw new Error(`approve-label: ${response.status} ${await response.text()}`);
        return response.json();
    },
    // Takes an approved label back out of the set: the SDK then marks its notes declined.
    async revoke({ label }) {
        const response = await fetch("http://127.0.0.1:3001/admin/revoke-label", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ label }) });
        if (!response.ok) throw new Error(`revoke-label: ${response.status} ${await response.text()}`);
        return response.json();
    },
    async balance({ address, token }) {
        if (!token) return (await publicClient.getBalance({ address })).toString();
        return (await publicClient.readContract({ address: token, abi: erc20, functionName: "balanceOf", args: [address] })).toString();
    },
    fresh() {
        return privateKeyToAccount(generatePrivateKey()).address;
    },
    async mine({ blocks = 1 }) {
        await rpc("anvil_mine", [`0x${blocks.toString(16)}`]);
        return null;
    },
};

if (FORK) await forkControls();

async function forkControls() {
    const { gasService } = await import(pathToFileURL(join(here, "../fork/service.mjs")));
    const assets = localChainConfig.assets;
    const tokens = Object.fromEntries(assets.map((a) => [a.symbol, a.token]));
    // Where each token keeps balances (mapping slot), for the fork's cheat code.
    const SLOTS = { USDT: 2n, USDC: 9n };
    const bundler = createWalletClient({ account: privateKeyToAccount("0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"), chain, transport: http(RPC) });
    const rates = {
        [tokens.USDT.toLowerCase()]: 2_445_000_000n,
        [tokens.USDC.toLowerCase()]: 2_440_000_000n,
    };
    const service = gasService({ publicClient, bundler, paymaster: process.env.HARNESS_PAYMASTER, rates, log: (line) => console.log(`     gas service: ${line}`) });
    host.gas = async ({ method, params }) => {
        const fn = service[method];
        if (!fn) throw new Error(`no ${method}`);
        return fn(params);
    };
    const realHttp = host.http;
    host.http = async (request) => {
        if (request.url.endsWith("/gas")) {
            const { id, method, params } = JSON.parse(Buffer.from(request.body, "base64").toString("utf8"));
            let answer;
            try {
                answer = { jsonrpc: "2.0", id, result: await host.gas({ method, params }) };
            } catch (error) {
                console.log(`     gas service ${method} failed: ${error.message}`);
                answer = { jsonrpc: "2.0", id, error: { code: -32500, message: error.message.slice(0, 300) } };
            }
            return { status: 200, headers: { "content-type": "application/json" }, body: Buffer.from(JSON.stringify(answer)).toString("base64") };
        }
        // The real ASP, but for its cache of the chain's events, which can't hold the fork's.
        if (request.url.includes("/asp/")) {
            if (request.url.includes("/note-events")) return { status: 503, headers: {}, body: "" };
            return realHttp({ ...request, url: request.url.replace(/^.*\/asp/, "https://api-v2.0xbow.io") });
        }
        return realHttp(request);
    };
    const { keccak256, encodeAbiParameters, toHex } = viem;
    Object.assign(control, {
        async giveToken({ symbol, address, amount }) {
            const slot = keccak256(encodeAbiParameters([{ type: "address" }, { type: "uint256" }], [address, SLOTS[symbol]]));
            const have = await control.balance({ address, token: tokens[symbol] });
            await rpc("anvil_setStorageAt", [tokens[symbol], slot, toHex(BigInt(have) + BigInt(amount), { size: 32 })]);
            return null;
        },
        async giveEth({ address, wei }) {
            await rpc("anvil_setBalance", [address, toHex(BigInt(wei))]);
            return null;
        },
        async gasPrice({ gwei }) {
            await rpc("anvil_setNextBlockBaseFeePerGas", [toHex(BigInt(Math.round(gwei * 1e9)))]);
            await rpc("anvil_mine", ["0x1"]);
            return null;
        },
        async afterSweep({ symbol, address }) {
            const token = tokens[symbol];
            const left = token.toLowerCase().startsWith("0xeeee") ? await control.balance({ address }) : await control.balance({ address, token });
            const paid = token.toLowerCase().startsWith("0xeeee") ? "0" : await control.balance({ address: process.env.HARNESS_PAYMASTER, token });
            return { left, paymasterHolds: paid };
        },
        async registered({ owner }) {
            const key = await publicClient.readContract({
                address: localChainConfig.keystore,
                abi: parseAbi(["function nullifyingKeys(address) view returns (uint256)"]),
                functionName: "nullifyingKeys",
                args: [owner],
            });
            if (key === 0n) throw new Error("not registered");
            return { nullifyingKey: toHex(key) };
        },
    });
}

const bundle = join(here, ".out/harness.js");
await build(engineOptions(join(here, "entry.ts"), bundle));

const types = { ".html": "text/html", ".js": "text/javascript", ".json": "application/json", ".wasm": "application/wasm", ".zkey": "application/octet-stream" };
const json = (res, status, value) => {
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify(value ?? null));
};

createServer(async (req, res) => {
    const url = new URL(req.url, `http://127.0.0.1:${PORT}`);
    try {
        if (req.method === "POST") {
            const chunks = [];
            for await (const chunk of req) chunks.push(chunk);
            const body = JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
            if (url.pathname === "/host" || url.pathname === "/control") {
                const table = url.pathname === "/host" ? host : control;
                const fn = table[body.m];
                if (!fn) return json(res, 200, { e: { code: "no_method", message: body.m } });
                try {
                    return json(res, 200, { r: (await fn(body.p ?? {})) ?? null });
                } catch (error) {
                    return json(res, 200, { e: { code: "host_error", message: String(error?.message ?? error) } });
                }
            }
            if (url.pathname === "/report") {
                report.push(body);
                console.log(`${body.ok === false ? "FAIL" : body.ok === true ? "ok  " : "    "} ${body.step}${body.detail ? `  ${JSON.stringify(body.detail)}` : ""}`);
                await writeFile(join(here, ".out/harness-report.json"), JSON.stringify(report, null, 2));
                return json(res, 200, null);
            }
        }
        if (url.pathname === "/local-chain.json") return json(res, 200, localChainConfig);
        const file =
            url.pathname === "/" ? join(here, "page.html")
            : url.pathname === "/harness.js" ? bundle
            : url.pathname === "/scenario.js" ? join(here, FORK ? "../fork/scenario.js" : "scenario.js")
            : url.pathname.startsWith("/assets/circuits/") ? normalize(join(assets, url.pathname.slice("/assets/".length)))
            : null;
        if (!file || (file.startsWith(assets) === false && !normalize(file).startsWith(normalize(join(here, ".."))))) {
            res.writeHead(404).end();
            return;
        }
        const ext = file.slice(file.lastIndexOf("."));
        res.writeHead(200, { "content-type": types[ext] ?? "application/octet-stream", "cache-control": "no-store" });
        res.end(await readFile(file));
    } catch (error) {
        res.writeHead(500).end(String(error));
    }
}).listen(PORT, "127.0.0.1", () => console.log(`harness: http://127.0.0.1:${PORT}/?run=1`));
