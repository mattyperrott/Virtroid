// SPDX-License-Identifier: GPL-3.0-or-later
//
// What a user does in the app, against the local stack: set up the account,
// shield ETH and a yield stablecoin and a plain token, wait for screening,
// withdraw through the relayer to a fresh address, send privately, and take
// an unscreened deposit back out with an exit.

const { engine, control, report } = globalThis;
const chainId = 11155111;

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

const sign = async (owner, typed) => {
    const r = await fetch("/host", { method: "POST", body: JSON.stringify({ m: "wallet", p: { method: "eth_signTypedData_v4", params: [owner, JSON.stringify(typed)] } }) });
    const answer = await r.json();
    if (answer.e) throw new Error(answer.e.message);
    return answer.r;
};

const find = (balances, symbol) => balances.find((b) => b.symbol === symbol);

/** Shields, has the mock ASP approve the deposit's label, and waits until it's spendable. */
async function shieldAndApprove(owner, symbol, amount) {
    await step(`shield ${amount} ${symbol}`, () => engine("shield", { chainId, owner, symbol, amount }));
    const waiting = await step(`sync after ${symbol} shield`, async () => {
        const b = find(await engine("sync", { chainId, owner }), symbol);
        if (b.pendingDeposits.length === 0) throw new Error(`no ${symbol} deposit waiting for screening`);
        return b;
    });
    for (const deposit of waiting.pendingDeposits) {
        await step(`approve ${symbol} label`, () => control("approve", { label: deposit.label }));
    }
    return step(`${symbol} spendable after approval`, async () => {
        const b = find(await engine("sync", { chainId, owner }), symbol);
        if (BigInt(b.spendable) === 0n) throw new Error(`${symbol} still not spendable: ${JSON.stringify(b)}`);
        return b;
    });
}

try {
    const { owner } = await step("fund the test wallet", () => control("fund"));
    await step("list chains", async () => ({ chains: (await engine("chains")).length }));

    const typed = await engine("derivation", { owner });
    const signatures = [await sign(owner, typed), await sign(owner, typed)];
    await step("refuse two different signatures", async () => {
        try {
            // The same signature with one hex digit of r changed.
            const other = signatures[0].slice(0, 10) + (signatures[0][10] === "0" ? "1" : "0") + signatures[0].slice(11);
            await engine("open", { chainId, owner: await control("fresh"), signatures: [signatures[0], other] });
        } catch (error) {
            if (error.code === "wallet_not_deterministic") return { refused: true };
            throw error;
        }
        throw new Error("opened with mismatched signatures");
    });
    const opened = await step("open the account", () => engine("open", { chainId, owner, signatures }));
    if (!opened.registered) await step("register the keystore", () => engine("register", { chainId, owner }));
    await step("open again: registered", async () => {
        const again = await engine("open", { chainId, owner });
        if (!again.registered) throw new Error("not registered after register");
        return again;
    });

    await shieldAndApprove(owner, "USDC", "100000000"); // 100 USDC (6 decimals) as yield shares
    await shieldAndApprove(owner, "ETH", "1000000000000000000"); // 1 ETH as ppETH
    await shieldAndApprove(owner, "mUSD", "10000000000000000000"); // 10 mUSD, a plain pool

    const usdc = globalThis.localChain.assets.find((a) => a.symbol === "USDC").token;
    const fresh = await control("fresh");
    const quote = await step("quote a 40 USDC withdrawal", () =>
        engine("quoteWithdraw", { chainId, owner, symbol: "USDC", amount: "40000000", recipient: fresh }),
    );
    await step("confirm the USDC withdrawal", () => engine("confirm"));
    await step("fresh address got the USDC", async () => {
        const got = BigInt(await control("balance", { address: fresh, token: usdc }));
        if (got === 0n) throw new Error("nothing arrived");
        return { got: got.toString(), quoted: quote.receive };
    });

    const freshMax = await control("fresh");
    await step("quote withdrawing all the rest of the USDC", () =>
        engine("quoteWithdraw", { chainId, owner, symbol: "USDC", amount: "max", recipient: freshMax }),
    );
    await step("confirm the USDC max withdrawal", () => engine("confirm"));
    await step("USDC is empty after max", async () => {
        const b = find(await engine("sync", { chainId, owner }), "USDC");
        const got = await control("balance", { address: freshMax, token: usdc });
        if (BigInt(b.spendable) !== 0n) throw new Error(`USDC left: ${b.spendable}`);
        return { got, left: b.spendable };
    });

    await step("quote a private send of 0.25 ETH to the owner's own address", () =>
        engine("quoteSend", { chainId, owner, symbol: "ETH", amount: "250000000000000000", recipient: owner }),
    );
    await step("confirm the ETH send", () => engine("confirm"));
    await step("sync after the send", async () => find(await engine("sync", { chainId, owner }), "ETH"));

    const freshPlain = await control("fresh");
    await step("quote withdrawing all mUSD", () =>
        engine("quoteWithdraw", { chainId, owner, symbol: "mUSD", amount: "max", recipient: freshPlain }),
    );
    await step("confirm the mUSD withdrawal", () => engine("confirm"));
    await step("fresh address got the mUSD", async () => {
        const token = globalThis.localChain.assets.find((a) => a.symbol === "mUSD").token;
        const got = BigInt(await control("balance", { address: freshPlain, token }));
        if (got === 0n) throw new Error("nothing arrived");
        return { got: got.toString() };
    });

    await step("shield 1 mUSD that screening never approves", () =>
        engine("shield", { chainId, owner, symbol: "mUSD", amount: "1000000000000000000" }),
    );
    const unscreened = find(await engine("sync", { chainId, owner }), "mUSD").pendingDeposits[0];
    await step("exit the unscreened deposit", () => engine("exit", { chainId, owner, commitment: unscreened.commitment }));
    await step("final balances", async () => ({ balances: await engine("sync", { chainId, owner }) }));
    await report("scenario finished", true);
} catch (error) {
    await report("scenario stopped", false, { message: error.message });
}
