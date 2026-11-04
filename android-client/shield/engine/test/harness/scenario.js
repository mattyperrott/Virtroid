// SPDX-License-Identifier: GPL-3.0-or-later
//
// What a user does in the app, against the local stack: make the wallet, send
// ETH and a yield stablecoin to the deposit address, watch them go in, wait for
// screening, send out some and all to fresh addresses, across deposits, take a
// declined deposit back out, and restore the wallet from its words.

const { engine, control, report } = globalThis;
const ETH = 10n ** 18n;
const USDC = 10n ** 6n;

async function step(name, fn) {
    const started = performance.now();
    try {
        const result = await fn();
        await report(name, true, { ms: Math.round(performance.now() - started), ...(result && typeof result === "object" ? result : { result }) });
        return result;
    } catch (error) {
        await report(name, false, { ms: Math.round(performance.now() - started), code: error.code, message: error.message });
        throw error;
    }
}

const find = (balances, symbol) => balances.find((b) => b.symbol === symbol);
const usdcToken = () => globalThis.localChain.assets.find((a) => a.symbol === "USDC").token;

/** Approves what's waiting and returns the balance once it's spendable. */
async function screen(symbol) {
    const waiting = await step(`${symbol} waits for screening`, async () => {
        const b = find(await engine("sync"), symbol);
        if (b.pendingDeposits.length === 0) throw new Error(`no ${symbol} deposit waiting: ${JSON.stringify(b)}`);
        return b;
    });
    for (const deposit of waiting.pendingDeposits) await step(`approve ${symbol} label`, () => control("approve", { label: deposit.label }));
    return step(`${symbol} spendable`, async () => {
        const b = find(await engine("sync"), symbol);
        if (BigInt(b.spendable) === 0n) throw new Error(`${symbol} still not spendable: ${JSON.stringify(b)}`);
        return b;
    });
}

try {
    const empty = await step("status before anything", () => engine("status"));
    if (empty.ready) throw new Error("a wallet before create");

    const made = await step("create the wallet", async () => {
        const r = await engine("create");
        if (r.words.split(" ").length !== 12) throw new Error(`words: ${r.words}`);
        return { address: r.address, owner: r.owner, words: r.words, wordCount: 12 };
    });
    const firstAddress = made.address;
    await step("create twice is refused", async () => {
        try {
            await engine("create");
        } catch (error) {
            if (error.code === "wallet_exists") return { refused: true };
            throw error;
        }
        throw new Error("made a second wallet");
    });
    await step("nothing to do yet", async () => {
        const r = await engine("check");
        if (r.incoming.length !== 0 || r.moved) throw new Error(JSON.stringify(r));
        return r;
    });

    // ETH below the minimum stays where it is and says so.
    await control("send", { address: firstAddress, eth: (ETH / 1000n).toString() });
    await step("too little ETH waits at the address", async () => {
        const r = await engine("check");
        if (r.incoming[0]?.state !== "below_minimum" && r.moved) throw new Error(JSON.stringify(r));
        return r;
    });

    await control("send", { address: firstAddress, eth: ETH.toString() });
    const afterEth = await step("1 ETH goes into the pool, registering the owner", async () => {
        const r = await engine("check");
        if (!r.moved) throw new Error(JSON.stringify(r));
        if (r.address === firstAddress) throw new Error("the address didn't move on");
        return r;
    });
    await screen("ETH");

    // USDC at an address with some ETH for gas: no gas service on the local chain.
    await control("send", { address: afterEth.address, eth: (ETH / 10n).toString(), usdc: (100n * USDC).toString() });
    const afterUsdc = await step("100 USDC goes in, paying gas from the address's ETH", async () => {
        const r = await engine("check");
        if (!r.moved) throw new Error(JSON.stringify(r));
        return r;
    });
    await screen("USDC");

    // The first address still gets looked at.
    await control("send", { address: firstAddress, eth: ETH.toString() });
    await step("funds sent to an old address still go in", async () => {
        // One older address is looked at per check.
        for (let i = 0; i < 3; i++) {
            const r = await engine("check");
            if (r.moved) return r;
        }
        throw new Error("never looked at the old address");
    });
    await screen("ETH");

    const out1 = await control("fresh");
    const quote = await step("quote sending 40 USDC out", async () => {
        const q = await engine("quoteOut", { symbol: "USDC", amount: (40n * USDC).toString(), recipient: out1 });
        if (q.parts !== 1) throw new Error(JSON.stringify(q));
        return q;
    });
    await step("send it, and it warns it's recent", async () => {
        if (!quote.recent) throw new Error("no warning right after a deposit");
        const r = await engine("sendOut", { id: quote.id });
        const got = BigInt(await control("balance", { address: out1, token: usdcToken() }));
        if (got < 40n * USDC) throw new Error(`got ${got}`);
        return { got: got.toString(), ...r };
    });

    // Several ETH deposits (what gas left at the USDC's address went in too): one withdrawal each.
    const out2 = await control("fresh");
    const before = find(await engine("sync"), "ETH");
    const two = await step("quote sending all the ETH: several deposits, a withdrawal each", async () => {
        const q = await engine("quoteOut", { symbol: "ETH", amount: "max", recipient: out2 });
        if (q.parts < 2) throw new Error(JSON.stringify(q));
        return q;
    });
    await step("a stale quote is refused", async () => {
        try {
            await engine("sendOut", { id: two.id + 1 });
        } catch (error) {
            if (error.code === "quote_replaced") return { refused: true };
            throw error;
        }
        throw new Error("sent a quote that isn't current");
    });
    const twoAgain = await engine("quoteOut", { symbol: "ETH", amount: "max", recipient: out2 });
    await step("send all the ETH", async () => {
        const r = await engine("sendOut", { id: twoAgain.id });
        if (r.txHashes.length !== twoAgain.parts) throw new Error(JSON.stringify(r));
        const got = BigInt(await control("balance", { address: out2 }));
        if (got === 0n) throw new Error("nothing arrived");
        return { got: got.toString(), before: before.spendable, ...r };
    });
    await step("ETH is empty", async () => {
        const b = find(await engine("sync"), "ETH");
        if (BigInt(b.spendable) !== 0n) throw new Error(JSON.stringify(b));
        return b;
    });

    // A declined deposit: approved, then revoked, is the one state the SDK calls declined.
    const status = await engine("status");
    await control("send", { address: status.address, eth: (ETH / 10n).toString(), usdc: (50n * USDC).toString() });
    await step("50 USDC goes in", async () => {
        const r = await engine("check");
        if (!r.moved) throw new Error(JSON.stringify(r));
        return r;
    });
    const waiting = find(await engine("sync"), "USDC").pendingDeposits[0];
    await control("approve", { label: waiting.label });
    await engine("sync");
    await control("revoke", { label: waiting.label });
    const declined = await step("screening declines it", async () => {
        const b = find(await engine("sync"), "USDC");
        if (b.declined.length !== 1) throw new Error(JSON.stringify(b));
        return b.declined[0];
    });
    const plan = await step("the way out asks for a little ETH at the owner address", async () => {
        const p = await engine("exitPlan");
        if (BigInt(p.balance) !== 0n) throw new Error(JSON.stringify(p));
        return p;
    });
    await control("send", { address: plan.owner, eth: (ETH / 10n).toString() });
    const out3 = await control("fresh");
    await step("exit it to a fresh address, as USDC", async () => {
        const r = await engine("exit", { commitment: declined.commitment, recipient: out3, symbol: "USDC" });
        const got = BigInt(await control("balance", { address: out3, token: usdcToken() }));
        if (got < 49n * USDC) throw new Error(`got ${got}`);
        return { got: got.toString(), ...r };
    });

    // A new phone: the same words, nothing else.
    const left = find(await engine("sync"), "USDC");
    const reached = await engine("status");
    await control("wipe");
    await step("after a wipe there's no wallet", async () => {
        // The engine keeps its opened account in memory; a new phone is a new page.
        const r = await engine("status");
        if (r.ready) throw new Error("still ready");
        return r;
    });
    await step("bad words are refused", async () => {
        try {
            await engine("restore", { words: "one two three" });
        } catch (error) {
            if (error.code === "bad_words") return { refused: true };
            throw error;
        }
        throw new Error("took bad words");
    });
    const restored = await step("restore from the 12 words", () => engine("restore", { words: made.words }));
    await step("it finds where the addresses had got to", async () => {
        if (restored.address !== reached.address) throw new Error(`${restored.address} isn't ${reached.address}`);
        return { address: restored.address };
    });
    await step("and the notes come back", async () => {
        const b = find(await engine("sync"), "USDC");
        if (b.spendable !== left.spendable) throw new Error(`${b.spendable} isn't ${left.spendable}`);
        return b;
    });
    await step("the exited deposit drops off once its exit is final", async () => {
        await control("mine", { blocks: 100 });
        const b = find(await engine("sync"), "USDC");
        if (b.pendingDeposits.length !== 0 || b.declined.length !== 0) throw new Error(JSON.stringify(b));
        return b;
    });

    await report("scenario done", true);
} catch (error) {
    await report("scenario failed", false, { message: String(error?.message ?? error), stack: String(error?.stack ?? "").slice(0, 800) });
}
