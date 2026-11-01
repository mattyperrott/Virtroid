// SPDX-License-Identifier: GPL-3.0-or-later
//
// All of the engine's network traffic goes through the app: the SDK's HTTP
// client and viem both call the global fetch, which this replaces. The app
// sends each request itself, so it decides which hosts are reachable, there
// are no browser CORS rules to meet, and the runtime's own network (Tor when
// it's on) carries everything. Only the page's own files (the circuits) are
// fetched by the WebView itself.

import type { Bridge } from "./bridge.js";

interface HostResponse {
    status: number;
    headers: Record<string, string>;
    /** Base64 of the body. */
    body: string;
}

export function installFetch(bridge: Bridge, ownOrigin: string): void {
    const pageFetch = globalThis.fetch.bind(globalThis);

    globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const request = new Request(input, init);
        if (new URL(request.url).origin === ownOrigin) {
            return pageFetch(request);
        }
        if (request.signal.aborted) throw abortError();

        const body = request.method === "GET" || request.method === "HEAD"
            ? null
            : toBase64(new Uint8Array(await request.arrayBuffer()));
        const headers: Record<string, string> = {};
        request.headers.forEach((value, name) => {
            headers[name] = value;
        });

        const answer = bridge.call<HostResponse>("http", {
            url: request.url,
            method: request.method,
            headers,
            body,
        });
        const response = await raceAbort(answer, request.signal);
        const responseBody = response.status === 204 || response.status === 304 ? null : (fromBase64(response.body) as BodyInit);
        return new Response(responseBody, {
            status: response.status,
            headers: response.headers,
        });
    };
}

function raceAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
    // The app can't cancel a request it already sent; an abort only stops the
    // waiting here, which is all viem and the SDK's timeouts need.
    return new Promise<T>((resolve, reject) => {
        const onAbort = () => reject(abortError());
        signal.addEventListener("abort", onAbort, { once: true });
        promise.then(
            (value) => {
                signal.removeEventListener("abort", onAbort);
                resolve(value);
            },
            (error: unknown) => {
                signal.removeEventListener("abort", onAbort);
                reject(error instanceof Error ? error : new TypeError(String(error)));
            },
        );
    });
}

function abortError(): Error {
    return new DOMException("The request was aborted", "AbortError");
}

export function toBase64(bytes: Uint8Array): string {
    let binary = "";
    for (let i = 0; i < bytes.length; i += 0x8000) {
        binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    }
    return btoa(binary);
}

export function fromBase64(text: string): Uint8Array {
    const binary = atob(text);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    return bytes;
}
