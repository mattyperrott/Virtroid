// SPDX-License-Identifier: GPL-3.0-or-later
//
// Stablecoins reaching the pool with no ETH at the address, against the real
// contracts on a fork of mainnet (the router, pool, keystore, Aave, USDT and
// USDC, EntryPoint 0.8 and the 7702 delegate), with a stand-in gas service.

const { engine, control, report } = globalThis;
const USD = 10n ** 6n;

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

try {
    await control("gasPrice", { gwei: 0.2 });
    const wallet = await step("create the wallet", () => engine("create").then(({ address, owner }) => ({ address, owner })));
    const token = (symbol) => globalThis.localChain.assets.find((a) => a.symbol === symbol).token;

    await control("giveToken", { symbol: "USDT", address: wallet.address, amount: (9n * USD).toString() });
    await step("9 USDT is below the minimum: it waits", async () => {
        const r = await engine("check");
        if (r.incoming[0]?.state !== "below_minimum") throw new Error(JSON.stringify(r));
        return r.incoming[0];
    });

    await control("giveToken", { symbol: "USDT", address: wallet.address, amount: (25n * USD).toString() });
    const usdt = await step("25 USDT goes in with no ETH, registering the owner", async () => {
        const r = await engine("check");
        if (!r.moved) throw new Error(JSON.stringify(r));
        return r;
    });
    const measured = await step("what it left and paid", () => control("afterSweep", { symbol: "USDT", address: wallet.address }));
    if (BigInt(measured.left) >= 3n * USD) throw new Error(`left ${measured.left}`);
    await step("the owner is registered", () => control("registered", { owner: wallet.owner }));

    await control("gasPrice", { gwei: 0.2 });
    await control("giveToken", { symbol: "USDC", address: usdt.address, amount: (15n * USD).toString() });
    const usdc = await step("15 USDC goes in with no ETH", async () => {
        const r = await engine("check");
        if (!r.moved) throw new Error(JSON.stringify(r));
        return r;
    });
    await step("what it left and paid", () => control("afterSweep", { symbol: "USDC", address: usdt.address }));

    // A second deposit to a used address: USDT's approve needs its leftovers reset.
    await control("gasPrice", { gwei: 0.2 });
    await control("giveToken", { symbol: "USDT", address: wallet.address, amount: (30n * USD).toString() });
    await step("30 more USDT at the first address, which has allowances left", async () => {
        for (let i = 0; i < 3; i++) {
            const r = await engine("check");
            if (r.moved) return r;
        }
        throw new Error("didn't go in");
    });

    await control("gasPrice", { gwei: 200 });
    await control("giveToken", { symbol: "USDC", address: usdc.address, amount: (20n * USD).toString() });
    await step("at 200 gwei, 20 USDC waits rather than lose it all to gas", async () => {
        const r = await engine("check");
        const entry = r.incoming.find((i) => i.symbol === "USDC");
        if (entry?.state !== "fee_high") throw new Error(JSON.stringify(r));
        return entry;
    });
    await control("gasPrice", { gwei: 0.2 });
    await step("once gas falls it goes in", async () => {
        const r = await engine("check");
        if (!r.moved) throw new Error(JSON.stringify(r));
        return r;
    });

    await control("gasPrice", { gwei: 0.2 });
    const eth = await engine("status");
    await control("giveEth", { address: eth.address, wei: (2n * 10n ** 16n).toString() });
    await step("0.02 ETH pays its own gas", async () => {
        const r = await engine("check");
        if (!r.moved) throw new Error(JSON.stringify(r));
        return r;
    });
    await step("the ETH left behind is dust", () => control("afterSweep", { symbol: "ETH", address: eth.address }));

    // A stablecoin at an address that also has ETH pays its gas from the ETH, with no service.
    await control("gasPrice", { gwei: 0.2 });
    const both = await engine("status");
    await control("giveToken", { symbol: "USDC", address: both.address, amount: (12n * USD).toString() });
    await control("giveEth", { address: both.address, wei: (10n ** 16n).toString() });
    await step("USDC with ETH beside it uses the ETH, then the ETH goes in too", async () => {
        const r = await engine("check");
        if (!r.moved) throw new Error(JSON.stringify(r));
        return r;
    });

    await step("every deposit shows, waiting for screening", async () => {
        const balances = await engine("sync");
        const count = balances.reduce((n, b) => n + b.pendingDeposits.length, 0);
        if (count < 6) throw new Error(`${count} deposits: ${JSON.stringify(balances)}`);
        return { balances: balances.map((b) => `${b.symbol} ${b.pending}`) };
    });
    await report("scenario done", true);
} catch (error) {
    await report("scenario failed", false, { message: String(error?.message ?? error) });
}
