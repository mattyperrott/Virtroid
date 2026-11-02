# Shield

The cloud phone's app for shielding, privately sending and withdrawing the
user's own crypto, through 0xbow's [Privacy Pools v2](https://0xbow.io). It
holds no funds and no wallet keys: the user connects their own wallet over
WalletConnect, and every deposit, signature and exit is approved there.

Licence: **GPL-3.0-or-later** ([LICENSE](LICENSE)), unlike the rest of
Virtroid. The engine bundles snarkjs and circomlib, which are GPL-3.0, and
uses the Privacy Pools circuits, which are too.

## How it works

- **Engine** (`engine/`): 0xbow's MIT core and EVM SDKs, built from the
  commit [`engine/sdk.lock`](engine/sdk.lock) pins (SDK 1.0.2, the release
  0xbow's own app vendors), wrapped in a small TypeScript layer
  (`engine/src`) and bundled into one file. It runs in a WebView nobody
  sees, loaded from the app's assets under a content policy that allows
  nothing else.
- **Bridge**: the engine reaches the network, storage and the wallet only
  through the app (`engine/src/bridge.ts`, `EngineHost.kt`,
  `HostCalls.kt`). Requests go out with OkHttp over the cloud phone's own
  network, so Tor carries them when it's on, and only to the hosts the
  engine's chains name. Values at rest are sealed with an Android Keystore
  key (`SecureStore.kt`).
- **Account keys** come from one EIP-712 signature by the user's wallet,
  over the same message 0xbow's app signs. The wallet signs it twice and
  the two must match, or the keys couldn't be made again. Because the
  message is the same, the account opens in 0xbow's app too: a way back to
  the funds if this app is ever gone.
- **Chains** (`engine/src/chains.ts`): Arbitrum (default), Base, Optimism and
  Ethereum. Stablecoins and ETH shield through 0xbow's Aave routers as
  ppUSDC, ppUSDT and ppETH; withdrawals unwrap back. `node
  engine/verify-chains.mjs` reads every address back from its chain and
  from the relayer; run it before changing that file.
- **Relayer**: sends and withdrawals go through 0xbow's relayer, which pays
  the gas and charges a flat fee on top of the amount. Each proof binds the
  processor its route needs (the relayer for a send, PrivacyPoolRelay for a
  plain withdrawal, the asset's router for a yield one).
- **WalletConnect** (`wallet/`): written from the public
  [specs](https://github.com/WalletConnect/walletconnect-specs) (MIT),
  because Reown's Kotlin SDK is under the WalletConnect Community License,
  which a GPL app can't include. `WcCryptoTest` checks it against
  WalletConnect's own test vectors.
- **The transaction card** (`ui/card`, Compose): where the user shields,
  withdraws, sends privately and takes back a declined deposit, a step at a
  time, in Virtroid's palette ([design-brief.md](design-brief.md)). It opens
  over whatever is behind it: from the home-screen widget it grows out of the
  widget, from the app over its screen. A transaction runs in the app
  (`Operations.kt`), not the card, so closing the card doesn't stop it, and
  opening it again shows where it got to. Sends and withdrawals are proved
  and priced before their review, and a confirm only sends the quote on
  screen.
- **Widget**: the way into the card. It shows the chain and deposits waiting
  for screening or declined, the balance only if the user turns that on, and
  opens the card on Shield, Withdraw or Send.

## Building

```bash
cd android-client/shield/engine
pnpm install --ignore-scripts
pnpm sdk          # clone and build 0xbow's SDK at the pinned commit
pnpm circuits     # download the bundled circuits, checked against the SDK's SHA-256 pins
pnpm build        # src/main/assets/engine/engine.js
cd ../.. && ./gradlew :shield:assembleDebug
```

A release build needs the Virtroid release signing values (as the other
modules) and `SHIELD_WALLETCONNECT_PROJECT_ID`, a free project ID from
[Reown](https://cloud.reown.com), in `local.properties` or the environment.

## Testing

Against 0xbow's own local e2e stack (anvil with their contracts, their
relayer, their mock ASP):

1. In the SDK checkout's `packages/evm-sdk`:
   `EVM_E2E=1 EVM_E2E_WORKERS=1 EVM_E2E_KEEP_ALIVE=1 npx vitest run --config e2e/vitest.config.ts -t none`
   (needs Foundry), then the mock ASP from `packages/mock-asp`:
   `start:evm` with `POSTMAN_PRIVATE_KEY` set to the e2e deployer key and
   the deployment's registry and entrypoint.
2. Browser: `node engine/test/harness/server.mjs <…>/e2e/deployments/local-8545.json`
   and open `http://127.0.0.1:8099/?run=1`. The scenario shields three
   assets, withdraws some and all, sends privately and exits an unscreened
   deposit; the steps print in the server's output.
3. Emulator: `node engine/test/harness/build-device.mjs <same json>`, build
   and install the debug APK, write the harness URL and the test wallet's
   address into the app's files (see `src/debug/.../BuildHooks.kt`), start
   the app (`adb shell am start -n io.virtroid.shield/.ui.MainActivity`),
   then `adb shell am broadcast -n io.virtroid.shield/.DebugScenario` and
   `adb shell run-as io.virtroid.shield cat files/scenario.log`.
4. By hand: as 3, then open the card from the widget or the app.
   `adb shell am broadcast -n io.virtroid.shield/.DebugScreening` has the
   mock screening approve the deposits the account waits on (`--es verdict
   decline` approves and then revokes them: the SDK only declines a deposit
   it had approved), with Shield in the foreground. Start the harness with
   `HARNESS_KEY=0x…` to keep its test wallet across restarts.
5. WalletConnect, end to end through Reown's relay: as 3, but write
   `walletconnect` in place of the address. The scenario pairs first and
   writes `files/pairing-uri`; give that to the test wallet, WalletConnect's
   own SDK (sign-client 2.21.8, the last Apache-2.0 release), which signs
   with the harness's key:
   `cd engine/test/walletconnect && pnpm install --ignore-scripts && SHIELD_WALLETCONNECT_PROJECT_ID=… node wallet.mjs '<wc: uri>'`.

The test mode exists only in debug builds.
