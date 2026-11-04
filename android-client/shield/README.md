# Shield

The cloud phone's app for moving the user's own ETH, USDC and USDT through a
privacy pool on Ethereum. The widget shows an address. What's sent to it goes
into 0xbow's [Privacy Pools v2](https://0xbow.io) by itself, and later comes
back out to any address, with no public link between the two.

Licence: **GPL-3.0-or-later** ([LICENSE](LICENSE)), unlike the rest of
Virtroid. The engine bundles snarkjs and circomlib, which are GPL-3.0, and
uses the Privacy Pools circuits, which are too.

## How it works

- **The wallet** (`engine/src/keys.ts`) is one set of 12 words on the phone,
  sealed with an Android Keystore key and shown once to write down. They're
  standard BIP-39 and BIP-44: address 0 owns the notes and never holds money,
  and each address from 1 up is a deposit address. There's no other backup.
- **Deposits** (`engine.ts` `check`): the address changes after each deposit
  and older ones keep working. A deposit is built by the owner's session and
  sent from the deposit address, as the proof doesn't name who pays, so
  deposits aren't publicly linked to each other. The exception is the first,
  whose transaction also registers the owner's keys (`setAuthPolicyWithSig`):
  a yield withdrawal can't be bundled with a relayed registration, so this is
  where it's paid for.
- **Gas**: ETH pays for itself. USDC or USDT at an address with no ETH goes
  as one EIP-7702 + ERC-4337 UserOperation (`gasless.ts`): Candide's public
  bundler and token paymaster take the cost in the same token, after the
  deposit. The paymaster address is pinned in `chains.ts`, and it's only ever
  approved for what's been left for it. What's left past the real cost stays
  at the address as dust. If gas would take more than 10% of the amount, the
  deposit waits unless the user says go. A deposit that fails on chain backs
  off, as each try can cost gas.
- **The watcher** (`watch/WatchService.kt`): a foreground service, as Android
  cuts a background app's network, that looks at the addresses with plain RPC
  reads and starts the engine only when there's something to do. It runs while
  the phone is on; funds wait at the address until then.
- **Sending out** (`quoteOut`, `sendOut`): through 0xbow's relayer, which pays
  the gas and charges a flat fee on top of the amount. A spend can't mix
  deposits, so an amount no one deposit covers goes as several withdrawals
  with a fee each, and a yield withdrawal can't be batched. The review warns
  when the amount or timing could link a withdrawal to its deposit, because
  amounts are public and the pool is small.
- **Screening**: 0xbow's ASP screens each deposit before it can leave
  privately. On Ethereum, on 2026-11-04, half got a screening update within 9
  minutes and 78% within 15 (157 deposits over 7 days, next-root estimate). A
  deposit it declines can only leave publicly: the owner address, which needs
  a little ETH sent to it for gas, takes it back with `ragequit`, unwraps it
  and sends it on (`exit`).
- **Engine** (`engine/`): 0xbow's MIT core and EVM SDKs, built from the
  commit [`engine/sdk.lock`](engine/sdk.lock) pins, wrapped in a small
  TypeScript layer (`engine/src`) and bundled into one file. It runs in a
  WebView nobody sees, loaded from the app's assets under a content policy that
  allows nothing else.
- **Bridge**: the engine reaches the network and storage only through the app
  (`engine/src/bridge.ts`, `EngineHost.kt`, `HostCalls.kt`). Requests go out
  with OkHttp over the cloud phone's own network, so Tor carries them when it's
  on, and only to the hosts the engine's chain names. Values at rest are sealed
  with an Android Keystore key (`SecureStore.kt`).
- **Chain** (`engine/src/chains.ts`): Ethereum, with ETH, USDC and USDT as
  0xbow's Aave-backed ppETH, ppUSDC and ppUSDT. `node engine/verify-chains.mjs`
  reads every address back from the chain and from the relayer; run it before
  changing that file. The RPCs are the public ones that answer historical logs.
- **Recovery**: the owner's keys come from a signature over the same message
  0xbow's app signs, so the 12 words imported into any wallet open the same
  account there. Funds sent to a deposit address on another chain, or in
  another token, are reachable the same way.
- **Widget** (`widget/ShieldWidget.kt`): one row, the same shape as Fauxx's: the
  mark, two lines, and the one thing to do next (copy the address, or send out).
  Amounts show only if the user turns that on.

## Building

```bash
cd android-client/shield/engine
pnpm install --ignore-scripts
pnpm sdk          # clone and build 0xbow's SDK at the pinned commit
pnpm circuits     # download the bundled circuits, checked against the SDK's SHA-256 pins
pnpm build        # src/main/assets/engine/engine.js
cd ../.. && ./gradlew :shield:assembleDebug
```

A release build needs the Virtroid release signing values, as the other
modules do.

## Testing

Against 0xbow's own local e2e stack (anvil with their contracts, their relayer,
their mock ASP):

1. In the SDK checkout's `packages/evm-sdk`:
   `EVM_E2E=1 EVM_E2E_WORKERS=1 EVM_E2E_KEEP_ALIVE=1 npx vitest run --config e2e/vitest.config.ts -t none`
   (needs Foundry), then the mock ASP from `packages/mock-asp`:
   `start:evm` with `POSTMAN_PRIVATE_KEY` set to the e2e deployer key and
   the deployment's registry and entrypoint.
2. Browser: `node engine/test/harness/server.mjs <…>/e2e/deployments/local-8545.json`
   and open `http://127.0.0.1:8099/?run=1` (`HARNESS_PORT` moves it). The
   scenario makes the wallet, sends ETH and USDC to its addresses, sends some
   and all out, across deposits, takes a declined deposit back and restores from
   the words; the steps print in the server's output.
3. Emulator: `node engine/test/harness/build-device.mjs <same json>`, build and
   install the debug APK, start it once, then
   `adb shell "run-as io.virtroid.shield sh -c 'mkdir -p files; echo http://10.0.2.2:8099 > files/harness-url'"`
   and start it again. Send to its address with the harness's `/control` `send`.
   `adb shell am broadcast -n io.virtroid.shield/.DebugScreening` has the mock
   screening approve what's waiting (`--es verdict decline` approves and then
   revokes: the SDK only declines a deposit it had approved).
   `adb shell am broadcast -n io.virtroid.shield/.DebugWidget` puts the widget in
   a state to look at.

Against the real contracts, for stablecoins with no ETH at the address:

4. `anvil --fork-url <an RPC that serves recent state> --chain-id 1 --hardfork prague`,
   deploy `engine/test/fork/TestPaymaster.sol` and fund its `deposit()`, then
   `HARNESS_FORK=http://127.0.0.1:8545 HARNESS_PAYMASTER=0x… node engine/test/harness/server.mjs`.
   The real router, pool, keystore, Aave, tokens, EntryPoint and delegate run;
   `test/fork/service.mjs` stands in for the gas service. Fork soon before
   running: public RPCs drop old state.

The stand-in is the one gap: Candide's service won't sign for an address without
the funds, so its final signature and a real UserOperation are only proved by a
small real deposit.

The test mode exists only in debug builds.
