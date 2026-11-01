// SPDX-License-Identifier: GPL-3.0-or-later
//
// The message channel between the engine and the app. The app's WebView
// exposes `shieldHost` (androidx.webkit's addWebMessageListener), which only
// this page's origin can reach. Every message is one JSON string:
//
//   app → engine   {t:"req", id, m, p}        a request the engine answers
//   engine → app   {t:"res", id, r} | {t:"res", id, e}
//   engine → app   {t:"call", id, m, p}       the engine asks the app (network,
//                                             storage, the user's wallet)
//   app → engine   {t:"ret", id, r} | {t:"ret", id, e}
//   engine → app   {t:"evt", m, p}            progress the screens show

export interface HostPort {
    postMessage(message: string): void;
    onmessage: ((event: { data: string }) => void) | null;
}

export interface BridgeError {
    code: string;
    message: string;
}

type Inbound =
    | { t: "req"; id: number; m: string; p: unknown }
    | { t: "ret"; id: number; r?: unknown; e?: BridgeError };

export type RequestHandler = (method: string, params: unknown) => Promise<unknown>;

/** An error the app gets back by code, so screens can tell a cancel from a failure. */
export class EngineError extends Error {
    constructor(
        readonly code: string,
        message: string,
    ) {
        super(message);
    }
}

export class Bridge {
    private nextId = 1;
    private readonly pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();
    private handler: RequestHandler | null = null;

    constructor(private readonly port: HostPort) {
        port.onmessage = (event) => this.receive(event.data);
    }

    serve(handler: RequestHandler): void {
        this.handler = handler;
    }

    /** Asks the app for something and waits for its answer. */
    call<T>(method: string, params: unknown): Promise<T> {
        const id = this.nextId++;
        return new Promise<T>((resolve, reject) => {
            this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject });
            this.send({ t: "call", id, m: method, p: params });
        });
    }

    emit(event: string, params: unknown): void {
        this.send({ t: "evt", m: event, p: params });
    }

    private send(message: unknown): void {
        this.port.postMessage(JSON.stringify(message, bigintReplacer));
    }

    private receive(raw: string): void {
        let message: Inbound;
        try {
            message = JSON.parse(raw) as Inbound;
        } catch {
            return;
        }
        if (message.t === "ret") {
            const waiter = this.pending.get(message.id);
            if (!waiter) return;
            this.pending.delete(message.id);
            if (message.e) waiter.reject(new EngineError(message.e.code, message.e.message));
            else waiter.resolve(message.r);
        } else if (message.t === "req") {
            void this.answer(message.id, message.m, message.p);
        }
    }

    private async answer(id: number, method: string, params: unknown): Promise<void> {
        try {
            if (!this.handler) throw new EngineError("not_ready", "The engine is still starting");
            const result = await this.handler(method, params);
            this.send({ t: "res", id, r: result ?? null });
        } catch (error) {
            this.send({ t: "res", id, e: toBridgeError(error) });
        }
    }
}

/** bigints cross the bridge as decimal strings. */
export function bigintReplacer(_key: string, value: unknown): unknown {
    return typeof value === "bigint" ? value.toString() : value;
}

export function toBridgeError(error: unknown): BridgeError {
    if (error instanceof EngineError) return { code: error.code, message: error.message };
    if (error instanceof Error) {
        // The SDK's errors are classes named after what went wrong.
        return { code: error.name || "error", message: error.message };
    }
    return { code: "error", message: String(error) };
}
