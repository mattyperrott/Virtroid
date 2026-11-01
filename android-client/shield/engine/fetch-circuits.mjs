// SPDX-License-Identifier: GPL-3.0-or-later
//
// Downloads the circuits the app bundles (src/artifacts.ts's BUNDLED) into
// the app's assets: from GitHub's LFS storage for the commit sdk.lock pins,
// each file checked against the SHA-256 the SDK's DEFAULT_CIRCUIT_MANIFEST
// pins for the trusted-setup ceremony's keys. A file already there that
// matches isn't downloaded again.

import { createHash } from "node:crypto";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const lock = Object.fromEntries(
    (await readFile(join(here, "sdk.lock"), "utf8"))
        .split("\n")
        .filter((line) => line && !line.startsWith("#"))
        .map((line) => line.split("=")),
);
const sdk = await import(join(here, ".sdk/pp2/packages/evm-sdk/dist/index.js"));
const manifest = sdk.DEFAULT_CIRCUIT_MANIFEST;
const artifactsSource = await readFile(join(here, "src/artifacts.ts"), "utf8");
const bundled = JSON.parse(artifactsSource.match(/export const BUNDLED = (\[[^\]]*\])/)[1]);
const out = join(here, "../src/main/assets/circuits");

const kinds = [
    ["wasm", (name) => `${name}_js/${name}.wasm`, (name) => `${name}.wasm`],
    ["provingKey", () => "groth16_pkey.zkey", () => "groth16_pkey.zkey"],
    ["verificationKey", () => "groth16_vkey.json", () => "groth16_vkey.json"],
];

for (const name of bundled) {
    const pins = manifest[name];
    if (!pins) throw new Error(`The SDK's manifest has no ${name}`);
    for (const [kind, repoPath, assetName] of kinds) {
        const expected = pins[`${kind}Sha256`].toLowerCase();
        const target = join(out, name, assetName(name));
        if (await matches(target, expected)) continue;
        // groth16_vkey.json is a plain file in git, the others are LFS objects.
        const host = kind === "verificationKey" ? "raw.githubusercontent.com" : "media.githubusercontent.com/media";
        const repo = lock.repo.replace(/^https:\/\/github.com\//, "").replace(/\.git$/, "");
        const url = `https://${host}/${repo}/${lock.commit}/packages/circuits/build/${name}/${repoPath(name)}`;
        const response = await fetch(url);
        if (!response.ok) throw new Error(`${url}: ${response.status}`);
        const bytes = Buffer.from(await response.arrayBuffer());
        const actual = sha256(bytes);
        if (actual !== expected) throw new Error(`${name} ${kind}: got ${actual}, the manifest pins ${expected}`);
        await mkdir(dirname(target), { recursive: true });
        await writeFile(`${target}.part`, bytes);
        await rename(`${target}.part`, target);
        console.log(`fetch-circuits: ${name}/${assetName(name)} (${(bytes.length / 1048576).toFixed(1)} MB)`);
    }
}
console.log(`fetch-circuits: ${bundled.length} circuits in ${out}`);

async function matches(path, expected) {
    try {
        return sha256(await readFile(path)) === expected;
    } catch {
        return false;
    }
}

function sha256(bytes) {
    return createHash("sha256").update(bytes).digest("hex");
}
