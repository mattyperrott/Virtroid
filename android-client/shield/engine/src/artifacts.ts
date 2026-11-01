// SPDX-License-Identifier: GPL-3.0-or-later
//
// The circuits' proving files. The common ones ship inside the app
// (fetch-circuits.mjs lists them), so a shield, send or withdrawal needs no
// download. A rarer shape (more inputs or outputs than BUNDLED covers) falls
// back to IPFS. Either way every file is checked against the SHA-256 the SDK
// pins for the trusted-setup ceremony's keys before snarkjs sees it.

import type { CircuitManifest } from "@privacy-pools-v2/evm-sdk";

/** The circuits the app ships: deposits, exits, and spends of up to two notes into up to two. */
export const BUNDLED = ["deposit", "ragequit", "transact_1x1", "transact_1x2", "transact_2x1", "transact_2x2"];

type Kind = "wasm" | "provingKey" | "verificationKey";

export interface ArtifactSource {
    getWasm(circuitName: string): Promise<Uint8Array>;
    getProvingKey(circuitName: string): Promise<Uint8Array>;
    getVerificationKey(circuitName: string): Promise<Uint8Array>;
}

export class AppCircuitArtifacts implements ArtifactSource {
    private readonly cache = new Map<string, Promise<Uint8Array>>();

    constructor(
        private readonly manifest: CircuitManifest,
        /** Where the page serves the bundled files, e.g. https://appassets.androidplatform.net/assets/circuits */
        private readonly bundledBase: string,
        private readonly fallback: ArtifactSource,
    ) {}

    getWasm(circuitName: string): Promise<Uint8Array> {
        return this.load(circuitName, "wasm");
    }

    getProvingKey(circuitName: string): Promise<Uint8Array> {
        return this.load(circuitName, "provingKey");
    }

    getVerificationKey(circuitName: string): Promise<Uint8Array> {
        return this.load(circuitName, "verificationKey");
    }

    private load(circuitName: string, kind: Kind): Promise<Uint8Array> {
        const key = `${circuitName}/${kind}`;
        let loading = this.cache.get(key);
        if (!loading) {
            loading = this.fetchChecked(circuitName, kind);
            // A failed load is retried next time rather than remembered.
            loading.catch(() => this.cache.delete(key));
            this.cache.set(key, loading);
        }
        return loading;
    }

    private async fetchChecked(circuitName: string, kind: Kind): Promise<Uint8Array> {
        const pins = this.manifest[circuitName];
        if (!pins) throw new Error(`Unknown circuit ${circuitName}`);
        const expected = pins[`${kind}Sha256`];
        if (!expected) throw new Error(`No SHA-256 pinned for ${circuitName} ${kind}`);

        if (!BUNDLED.includes(circuitName)) {
            // IpfsCircuitArtifacts checks the same digests itself.
            if (kind === "wasm") return this.fallback.getWasm(circuitName);
            if (kind === "provingKey") return this.fallback.getProvingKey(circuitName);
            return this.fallback.getVerificationKey(circuitName);
        }

        const response = await fetch(`${this.bundledBase}/${circuitName}/${fileName(circuitName, kind)}`);
        if (!response.ok) throw new Error(`Bundled ${circuitName} ${kind} is missing (${response.status})`);
        const bytes = new Uint8Array(await response.arrayBuffer());
        const actual = await sha256Hex(bytes);
        if (actual !== expected.toLowerCase()) {
            throw new Error(`Bundled ${circuitName} ${kind} doesn't match its pinned SHA-256`);
        }
        return bytes;
    }
}

/** The file names the SDK's own layout uses (LocalCircuitArtifacts). */
export function fileName(circuitName: string, kind: Kind): string {
    if (kind === "wasm") return `${circuitName}.wasm`;
    if (kind === "provingKey") return "groth16_pkey.zkey";
    return "groth16_vkey.json";
}

async function sha256Hex(bytes: Uint8Array): Promise<string> {
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes as BufferSource));
    return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}
