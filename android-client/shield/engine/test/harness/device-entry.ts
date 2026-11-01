// SPDX-License-Identifier: GPL-3.0-or-later
//
// The engine for a debug build's local test mode: the app's own start() on
// 0xbow's e2e deployment, reached from an emulator at 10.0.2.2.
// build-device.mjs bakes the chain in as __LOCAL_CHAIN__.

import type { HostPort } from "../../src/bridge.js";
import { start } from "../../src/app.js";
import type { ChainConfig } from "../../src/chains.js";

declare const __LOCAL_CHAIN__: Omit<ChainConfig, "logsChunk"> & { logsChunk: number };

start(globalThis.shieldHost as HostPort, [{ ...__LOCAL_CHAIN__, logsChunk: BigInt(__LOCAL_CHAIN__.logsChunk) }]);
