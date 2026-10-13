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

export type ServiceStatus = "good" | "warn" | "bad" | "off";

export interface ServiceHealth {
  name: string;
  status: ServiceStatus;
  detail: string;
}

export interface DepositSummary {
  txId: string;
  shortTxId: string;
  explorerUrl: string;
  accountId: string;
  accountShortId: string;
  amount: string;
  age: string;
}

export interface AccountBalance {
  accountId: string;
  accountShortId: string;
  plan: string;
  balance: string;
  deposited: string;
  charged: string;
  lastActivity: string;
  overdrawn: boolean;
}

export interface PaymentsOverview {
  enabled: boolean;
  runtimePricePerHour: string;
  storagePricePerGbMonth: string;
  walletAccounts: number;
  paidAccounts: number;
  depositCount: number;
  depositsTotal: string;
  deposits24h: string;
  balancesHeld: string;
  charges24h: string;
  runtimeCharges24h: string;
  storageCharges24h: string;
  chargesTotal: string;
  overdrawnAccounts: number;
  lastDeposit: string;
  recentDeposits: DepositSummary[];
  accounts: AccountBalance[];
}

export interface RuntimeVM {
  id: string;
  shortId: string;
  accountShortId: string;
  region: string;
  state: string;
  desired: string;
  image: string;
  progress?: string;
  age: string;
  ready: string;
  lastUsed: string;
  deleted?: string;
  retireReason?: string;
  failed: boolean;
}

export interface VmsOverview {
  enabled: boolean;
  regions: string[];
  maxVms: number;
  warmSpares: number;
  live: number;
  ready: number;
  starting: number;
  retiring: number;
  failed: number;
  vmHours24h: string;
  estimatedCost24h: string;
  hourlyBurn: string;
  vms: RuntimeVM[];
}

export interface SiaOverview {
  enabled: boolean;
  reachable: boolean;
  checked: string;
  network?: string;
  version?: string;
  synced: boolean;
  syncHeight: number;
  wallet: string;
  contractsEnabled: boolean;
  wantedContracts: number;
  contracts: number;
  indexerAccounts: number;
  pinnedData: string;
  linkedAccounts: number;
  storedByKind: Array<{ kind: string; bytes: string }>;
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
  // Absent from control planes older than the platform services release.
  services?: ServiceHealth[];
  payments?: PaymentsOverview;
  vms?: VmsOverview;
  sia?: SiaOverview;
}
