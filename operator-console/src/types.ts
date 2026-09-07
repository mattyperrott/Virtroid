export type SystemStatus = "healthy" | "degraded" | "critical";
export type RuntimeStatus = "running" | "stopped" | "attention";
export type Severity = "info" | "warning" | "critical";

export interface Metric {
  label: string;
  value: string;
  detail: string;
  trend?: string;
  tone: "mint" | "neutral" | "amber" | "red";
}

export interface RuntimeSummary {
  id: string;
  shortId: string;
  name: string;
  accountId: string;
  accountShortId: string;
  status: RuntimeStatus;
  desiredState: "running" | "stopped";
  connection: "online" | "offline";
  host: string;
  persona: number;
  androidVersion: string;
  deviceProfile: string;
  activeSession: boolean;
  sessionAge?: string;
  cpu: number;
  memory: string;
  storage: string;
  snapshot: string;
  cleanupPending: boolean;
  updated: string;
  lastError?: string;
}

export interface Incident {
  id: string;
  title: string;
  detail: string;
  severity: Severity;
  age: string;
  source: string;
}

export interface HygieneCheck {
  label: string;
  detail: string;
  status: "passed" | "attention" | "scheduled";
}

export interface ActivityPoint {
  label: string;
  sessions: number;
  operations: number;
}

export interface OverviewResponse {
  generatedAt: string;
  environment: string;
  readOnly: boolean;
  status: SystemStatus;
  headline: string;
  subline: string;
  metrics: Metric[];
  runtimes: RuntimeSummary[];
  incidents: Incident[];
  hygiene: HygieneCheck[];
  activity: ActivityPoint[];
  fleet: {
    ready: number;
    total: number;
    name: string;
    heartbeat: string;
    capacity: number;
  };
  deployment: {
    version: string;
    commit: string;
    schema: string;
    deployed: string;
  };
}
