// SPDX-License-Identifier: GPL-3.0-or-later
//
// The engine's entry point in the app's WebView page.

import type { HostPort } from "./bridge.js";
import { start } from "./app.js";
import { CHAINS } from "./chains.js";

declare global {
    // androidx.webkit's addWebMessageListener("shieldHost", ...) defines it.
    var shieldHost: HostPort | undefined;
}

const port = globalThis.shieldHost;
if (!port) throw new Error("shieldHost is missing: the engine only runs inside the app");
start(port, CHAINS);
