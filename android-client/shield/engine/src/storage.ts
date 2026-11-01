// SPDX-License-Identifier: GPL-3.0-or-later
//
// The SDK's storage (notes, payment requests, sync cursors) and the engine's
// own (the account's keys) live in the app, which encrypts each value with a
// key the Android Keystore holds. Values cross the bridge as JSON text.

import type { Bridge } from "./bridge.js";
import { bigintReplacer } from "./bridge.js";

export class HostStorage {
    constructor(
        private readonly bridge: Bridge,
        /** Keeps one account's values apart from another's. */
        private readonly prefix: string,
    ) {}

    async get<T>(key: string): Promise<T | null> {
        const text = await this.bridge.call<string | null>("store.get", { key: this.prefix + key });
        return text === null ? null : (JSON.parse(text) as T);
    }

    async set<T>(key: string, value: T): Promise<void> {
        await this.bridge.call("store.set", {
            key: this.prefix + key,
            value: JSON.stringify(value, bigintReplacer),
        });
    }

    async delete(key: string): Promise<void> {
        await this.bridge.call("store.delete", { key: this.prefix + key });
    }

    async clear(): Promise<void> {
        await this.bridge.call("store.clear", { prefix: this.prefix });
    }
}
