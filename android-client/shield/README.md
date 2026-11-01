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
- **Widget**: a shortcut into the app. It shows the chain and waiting
  deposits; the balance only if the user turns that on.

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
4. WalletConnect, end to end through Reown's relay: as 3, but write
   `walletconnect` in place of the address. The scenario pairs first and
   writes `files/pairing-uri`; give that to the test wallet, WalletConnect's
   own SDK (sign-client 2.21.8, the last Apache-2.0 release), which signs
   with the harness's key:
   `cd engine/test/walletconnect && pnpm install --ignore-scripts && SHIELD_WALLETCONNECT_PROJECT_ID=… node wallet.mjs '<wc: uri>'`.

The test mode exists only in debug builds.
