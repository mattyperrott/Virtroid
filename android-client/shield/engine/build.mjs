// SPDX-License-Identifier: GPL-3.0-or-later
//
// Bundles the engine and the SDK it runs on into one file for the app's
// WebView page (src/main/assets/engine/), next to index.html. The SDK's
// Node-only pieces (file storage, local circuit files) are never reached in
// the app, so their Node built-ins become empty modules. A license list of
// everything bundled goes alongside, for the app's About screen.

import { build } from "esbuild";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { engineOptions } from "./build-options.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, "../src/main/assets/engine");

const result = await build(engineOptions(join(here, "src/main.ts"), join(out, "engine.js")));

// Every package that ended up in the bundle, from its path under node_modules.
const packages = new Set();
for (const input of Object.keys(result.metafile.inputs)) {
    const match = input.match(/node_modules\/((?:@[^/]+\/)?[^/]+)\//g);
    if (match) packages.add(match.at(-1).replace(/^node_modules\//, "").replace(/\/$/, ""));
    else if (input.includes("packages/core-sdk/")) packages.add("@privacy-pools-v2/core-sdk");
    else if (input.includes("packages/evm-sdk/")) packages.add("@privacy-pools-v2/evm-sdk");
}
await mkdir(out, { recursive: true });
await writeFile(join(out, "bundled-packages.txt"), [...packages].sort().join("\n") + "\n");
const bytes = result.metafile.outputs[Object.keys(result.metafile.outputs)[0]].bytes;
console.log(`build: engine.js ${(bytes / 1048576).toFixed(2)} MB, ${packages.size} packages`);
