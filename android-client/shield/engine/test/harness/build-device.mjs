// SPDX-License-Identifier: GPL-3.0-or-later
//
// Builds the debug build's local-test engine (src/debug/assets/engine/
// engine-local.js) for 0xbow's e2e deployment, reached from an emulator:
//   node test/harness/build-device.mjs <monorepo>/packages/evm-sdk/e2e/deployments/local-8545.json
// The app uses it only when the harness URL file is present (BuildHooks, debug).

import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { build } from "esbuild";

import { engineOptions } from "../../build-options.mjs";
import { localChain } from "./local-chain.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const viem = join(here, "../../.sdk/pp2/packages/evm-sdk/node_modules/viem/_esm");
const { privateKeyToAccount } = await import(pathToFileURL(join(viem, "accounts/index.js")));
const deployment = JSON.parse(await readFile(process.argv[2], "utf8"));
// 0xbow's e2e relayer key (their packages/evm-sdk/e2e/relayer.ts): local chain only.
const relayer = privateKeyToAccount("0xb9dacf9a665e631e115b28d780600d4238ff202e6c37c661d5c20c86005ec0f8").address;

const options = engineOptions(join(here, "device-entry.ts"), join(here, "../../../src/debug/assets/engine/engine-local.js"));
options.define = { ...options.define, __LOCAL_CHAIN__: JSON.stringify(localChain(deployment, "10.0.2.2", relayer)) };
await build(options);
console.log("build-device: src/debug/assets/engine/engine-local.js");
