// SPDX-License-Identifier: GPL-3.0-or-later
//
// A wallet for the interop test: WalletConnect's own SDK (@walletconnect/
// sign-client 2.21.8, the last Apache-2.0 release) pairs with the app's
// WalletConnect client through Reown's live relay, approves its session and
// answers its requests by having the test harness (../harness/server.mjs)
// sign with its throwaway key on the local chain. Never shipped.
//
//   SHIELD_WALLETCONNECT_PROJECT_ID=… node wallet.mjs '<wc: pairing URI>'

import { SignClient } from "@walletconnect/sign-client";

const say = (line) => console.log(`${new Date().toISOString()} ${line}`);

const uri = process.argv[2];
const projectId = process.env.SHIELD_WALLETCONNECT_PROJECT_ID;
const harness = process.env.HARNESS_URL ?? "http://127.0.0.1:8099";
if (!uri?.startsWith("wc:") || !projectId) throw new Error("usage: SHIELD_WALLETCONNECT_PROJECT_ID=… node wallet.mjs <wc: uri>");

async function harnessCall(path, m, p) {
    const response = await fetch(`${harness}/${path}`, { method: "POST", body: JSON.stringify({ m, p }) });
    const answer = await response.json();
    if (answer.e) throw new Error(answer.e.message);
    return answer.r;
}

const { owner } = await harnessCall("control", "fund", {});
const client = await SignClient.init({
    projectId,
    metadata: { name: "Shield test wallet", description: "Interop test", url: "https://example.invalid", icons: [] },
});

client.on("session_proposal", async ({ id, params }) => {
    const wanted = [params.requiredNamespaces?.eip155, params.optionalNamespaces?.eip155].filter(Boolean);
    const chains = [...new Set(wanted.flatMap((n) => n.chains ?? []))];
    const methods = [...new Set(wanted.flatMap((n) => n.methods ?? []))];
    const events = [...new Set(wanted.flatMap((n) => n.events ?? []))];
    say(`wallet: proposal for ${chains.join(",")} methods ${methods.join(",")}`);
    const { acknowledged } = await client.approve({
        id,
        namespaces: { eip155: { chains, methods, events, accounts: chains.map((c) => `${c}:${owner}`) } },
    });
    await acknowledged();
    say(`wallet: approved for ${owner} (the relay took the approval)`);
});

client.on("session_request", async ({ topic, id, params }) => {
    const { request, chainId } = params;
    say(`wallet: ${request.method} on ${chainId}`);
    try {
        const result = await harnessCall("host", "wallet", {
            chainId: Number(chainId.split(":")[1]),
            method: request.method,
            params: request.params,
        });
        await client.respond({ topic, response: { id, jsonrpc: "2.0", result } });
    } catch (error) {
        await client.respond({ topic, response: { id, jsonrpc: "2.0", error: { code: 5000, message: String(error.message) } } });
    }
});

client.on("session_delete", () => say("wallet: session deleted"));

await client.pair({ uri });
say(`wallet: paired as ${owner}`);
