import { previewOverview } from "./data/preview";
import type { OverviewResponse } from "./types";

export interface OperatorApiResult {
  data: OverviewResponse;
  source: "operator-api" | "preview-fixture";
}

export class OperatorApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
  }
}

export interface OperatorSession {
  authenticated: boolean;
  expiresAt: string;
}

const configuredApiBase = import.meta.env.VITE_OPERATOR_API_BASE?.replace(/\/$/, "") ?? "";
const liveOperatorMode = Boolean(configuredApiBase) || window.location.pathname.startsWith("/operator");

async function apiFetch(path: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(`${configuredApiBase}${path}`, {
    ...init,
    credentials: "include",
    headers: { Accept: "application/json", ...init?.headers },
  });
  if (!response.ok) {
    let message = `Operator API request failed (${response.status})`;
    try {
      const body = await response.json() as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Preserve the status-based message for non-JSON proxy errors.
    }
    throw new OperatorApiError(message, response.status);
  }
  return response;
}

export function isLiveOperatorMode(): boolean {
  return liveOperatorMode;
}

export async function getSession(signal?: AbortSignal): Promise<OperatorSession> {
  if (!liveOperatorMode) {
    return { authenticated: true, expiresAt: new Date(Date.now() + 8 * 60 * 60 * 1000).toISOString() };
  }
  return apiFetch("/operator/v1/session", { signal }).then((response) => response.json());
}

export async function createSession(token: string): Promise<OperatorSession> {
  const response = await apiFetch("/operator/v1/session", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token }),
  });
  return response.json();
}

export async function deleteSession(): Promise<void> {
  if (!liveOperatorMode) return;
  await apiFetch("/operator/v1/session", { method: "DELETE" });
}

function validateOverview(value: unknown): OverviewResponse {
  if (!value || typeof value !== "object") {
    throw new Error("Operator API returned an invalid overview payload");
  }
  const candidate = value as Partial<OverviewResponse>;
  if (!candidate.generatedAt || !candidate.metrics || !candidate.runtimes) {
    throw new Error("Operator API overview payload is incomplete");
  }
  return candidate as OverviewResponse;
}

export async function getOverview(signal?: AbortSignal): Promise<OperatorApiResult> {
	if (!liveOperatorMode) {
    await new Promise((resolve) => window.setTimeout(resolve, 180));
    return { data: previewOverview, source: "preview-fixture" };
  }

	const response = await apiFetch("/operator/v1/overview", { signal });
  return { data: validateOverview(await response.json()), source: "operator-api" };
}
