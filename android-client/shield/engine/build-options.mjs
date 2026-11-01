// SPDX-License-Identifier: GPL-3.0-or-later
//
// esbuild options shared by build.mjs (the app's bundle) and the test
// harness (the same engine on a local chain).

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const sdk = join(here, ".sdk/pp2/packages");

const nodeOnly = ["path", "node:path", "fs", "fs/promises", "node:fs/promises", "os", "node:os", "worker_threads", "readline"];

export function engineOptions(entry, outfile) {
    return {
        entryPoints: [entry],
        bundle: true,
        format: "esm",
        platform: "browser",
        // Android 16's WebView is a current Chromium; it updates with the image.
        target: "chrome120",
        outfile,
        minify: true,
        sourcemap: false,
        legalComments: "none",
        metafile: true,
        conditions: ["browser", "import", "module"],
        mainFields: ["browser", "module", "main"],
        define: { "process.env.NODE_ENV": '"production"', global: "globalThis" },
        inject: [join(here, "shims/globals.js")],
        alias: {
            "@privacy-pools-v2/evm-sdk": join(sdk, "evm-sdk/dist/index.js"),
            "@privacy-pools-v2/core-sdk": join(sdk, "core-sdk/dist/index.js"),
            // Browser stand-ins for the Node built-ins bundled code still imports.
            assert: join(here, "shims/assert.js"),
            crypto: join(here, "shims/crypto.js"),
            events: join(here, "node_modules/events/events.js"),
        },
        nodePaths: [join(sdk, "evm-sdk/node_modules"), join(sdk, "core-sdk/node_modules"), join(sdk, "../node_modules/.pnpm/node_modules")],
        plugins: [
            {
                name: "node-only",
                setup(b) {
                    const filter = new RegExp(`^(${nodeOnly.map((m) => m.replace("/", "\\/")).join("|")})$`);
                    b.onResolve({ filter }, (args) => ({ path: args.path, namespace: "node-only" }));
                    // Named exports that throw if anything ever calls them in the app.
                    const names = ["readFile", "writeFile", "rename", "mkdir", "rm", "readdir", "stat", "join", "dirname", "resolve", "basename", "tmpdir", "homedir"];
                    const stub = names.map((n) => `export function ${n}() { throw new Error("${n} is Node-only"); }`).join("\n") + "\nexport default {};";
                    b.onLoad({ filter: /.*/, namespace: "node-only" }, () => ({ contents: stub, loader: "js" }));
                },
            },
        ],
        logLevel: "warning",
    };
}
