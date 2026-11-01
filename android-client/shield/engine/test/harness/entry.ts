// SPDX-License-Identifier: GPL-3.0-or-later
//
// The engine on the test page: the same start() the app runs, on the local
// chain server.mjs describes (0xbow's e2e deployment on anvil).

import type { HostPort } from "../../src/bridge.js";
import { start } from "../../src/app.js";
import type { ChainConfig } from "../../src/chains.js";

declare global {
    var localChain: (Omit<ChainConfig, "logsChunk"> & { logsChunk: number }) | undefined;
}

const port = globalThis.shieldHost as HostPort;
const raw = globalThis.localChain!;
start(port, [{ ...raw, logsChunk: BigInt(raw.logsChunk) }]);
