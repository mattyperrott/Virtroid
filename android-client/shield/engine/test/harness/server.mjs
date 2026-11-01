// SPDX-License-Identifier: GPL-3.0-or-later
//
// Runs the engine end to end in a real Chromium against 0xbow's local e2e
// stack (anvil with their contracts, their relayer, their mock ASP), with
// this server playing the app's part: the network, the encrypted store (a
// plain map here), and the user's wallet (a throwaway key on the local
// chain). Open http://127.0.0.1:8099/?run=1 and the page runs scenario.js,
// posting each step here; the steps print and land in harness-report.json.
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

const deployment = JSON.parse(await readFile(process.argv[2], "utf8"));
const RPC = "http://127.0.0.1:8545";
// 0xbow's e2e keys (their packages/evm-sdk/e2e network.ts and relayer.ts): local chain only.
const DEPLOYER_KEY = "0x77d28084a5fdd3067a2f59f001775187199e1f1da27ffb5765545135cb21b7b3";
const RELAYER_KEY = "0xb9dacf9a665e631e115b28d780600d4238ff202e6c37c661d5c20c86005ec0f8";

const chain = defineChain({ id: 11155111, name: "local", nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 }, rpcUrls: { default: { http: [RPC] } } });
const publicClient = createPublicClient({ chain, transport: http(RPC), pollingInterval: 200 });
const user = privateKeyToAccount(generatePrivateKey());
const wallet = createWalletClient({ account: user, chain, transport: http(RPC) });

const localChainConfig = localChain(deployment, "127.0.0.1", privateKeyToAccount(RELAYER_KEY).address);

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
    async wallet({ method, params }) {
        if (method === "eth_signTypedData_v4") {
            const typed = typeof params[1] === "string" ? JSON.parse(params[1]) : params[1];
            const { EIP712Domain: _domain, ...types } = typed.types;
            return user.signTypedData({ domain: typed.domain, types, primaryType: typed.primaryType, message: typed.message });
        }
        if (method === "eth_sendTransaction") {
            const tx = params[0];
            return wallet.sendTransaction({ to: tx.to, data: tx.data, value: tx.value ? BigInt(tx.value) : 0n, ...(tx.gas ? { gas: BigInt(tx.gas) } : {}) });
        }
        if (method === "personal_sign") return user.signMessage({ message: { raw: params[0] } });
        if (method === "wallet_switchEthereumChain") return null;
        throw new Error(`The test wallet doesn't do ${method}`);
    },
};

// Test-only controls the scenario uses: funding, the ASP's approvals, chain reads.
const control = {
    async fund() {
        await rpc("anvil_setBalance", [user.address, "0x56BC75E2D63100000"]); // 100 ETH
        const mint = await wallet.writeContract({ address: deployment.yieldUnderlyingAddress, abi: erc20, functionName: "mint", args: [user.address, 1_000_000_000n] });
        await publicClient.waitForTransactionReceipt({ hash: mint });
        const mintPlain = await wallet.writeContract({ address: deployment.mockErc20Address, abi: erc20, functionName: "mint", args: [user.address, 10n ** 21n] });
        await publicClient.waitForTransactionReceipt({ hash: mintPlain });
        return { owner: user.address };
    },
    async approve({ label }) {
        const response = await fetch("http://127.0.0.1:3001/admin/approve-label", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ label }) });
        if (!response.ok) throw new Error(`approve-label: ${response.status} ${await response.text()}`);
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

const bundle = join(here, ".out/harness.js");
await build(engineOptions(join(here, "entry.ts"), bundle));

const types = { ".html": "text/html", ".js": "text/javascript", ".json": "application/json", ".wasm": "application/wasm", ".zkey": "application/octet-stream" };
const json = (res, status, value) => {
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify(value ?? null));
};

createServer(async (req, res) => {
    const url = new URL(req.url, "http://127.0.0.1:8099");
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
            : url.pathname === "/scenario.js" ? join(here, "scenario.js")
            : url.pathname.startsWith("/assets/circuits/") ? normalize(join(assets, url.pathname.slice("/assets/".length)))
            : null;
        if (!file || (file.startsWith(assets) === false && !file.startsWith(here))) {
            res.writeHead(404).end();
            return;
        }
        const ext = file.slice(file.lastIndexOf("."));
        res.writeHead(200, { "content-type": types[ext] ?? "application/octet-stream", "cache-control": "no-store" });
        res.end(await readFile(file));
    } catch (error) {
        res.writeHead(500).end(String(error));
    }
}).listen(8099, "127.0.0.1", () => console.log(`harness: http://127.0.0.1:8099/?run=1 (test wallet ${user.address})`));
