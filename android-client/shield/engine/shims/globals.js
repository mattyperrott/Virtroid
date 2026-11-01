// SPDX-License-Identifier: GPL-3.0-or-later
// Node globals that bundled libraries (ffjavascript, circomlibjs, readable-stream)
// reach for: esbuild injects these wherever the bundle names them.
import { Buffer } from "buffer";

export { Buffer };
export const process = {
    env: {},
    browser: true,
    version: "",
    versions: {},
    nextTick: (fn, ...args) => queueMicrotask(() => fn(...args)),
};
