import {
  Activity,
  AlertTriangle,
  ArrowRight,
  Bell,
  Boxes,
  Check,
  ChevronRight,
  Clock3,
  Command,
  Database,
  FileClock,
  Fingerprint,
  HardDrive,
  LayoutDashboard,
  ListTree,
  Menu,
  PackageCheck,
  Search,
  Server,
  ShieldCheck,
  Sparkles,
  Users,
  Wrench,
  X,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  createSession,
  deleteSession,
  getOverview,
  getSession,
  isLiveOperatorMode,
  OperatorApiError,
  type OperatorApiResult,
} from "./api";
import type {
  ActivityPoint,
  Incident,
  Metric,
  OverviewResponse,
  RuntimeSummary,
} from "./types";

const primaryNavigation = [
  { label: "Command centre", icon: LayoutDashboard },
  { label: "Runtimes", icon: Boxes },
  { label: "Sessions", icon: Activity },
  { label: "Accounts", icon: Users },
  { label: "Fleet", icon: Server },
];

const systemNavigation = [
  { label: "Security", icon: ShieldCheck, badge: "2" },
  { label: "Logs", icon: ListTree },
  { label: "Data hygiene", icon: Wrench, badge: "1" },
  { label: "Storage", icon: HardDrive },
  { label: "Releases", icon: PackageCheck },
  { label: "Reports", icon: FileClock },
];

function VirtroidMark() {
  return (
    <div className="brand-mark" aria-hidden="true">
      <span className="brand-mark__diamond" />
      <span className="brand-mark__core" />
    </div>
  );
}

function StatusDot({ status }: { status: "good" | "warn" | "bad" | "muted" }) {
  return <span className={`status-dot status-dot--${status}`} aria-hidden="true" />;
}

function Sidebar({
  active,
  onNavigate,
  mobileOpen,
  compact,
  onClose,
  onLogout,
}: {
  active: string;
  onNavigate: (label: string) => void;
  mobileOpen: boolean;
  compact: boolean;
  onClose: () => void;
  onLogout: () => void;
}) {
  const renderItems = (
    items: Array<{
      label: string;
      icon: React.ComponentType<{ size?: number; strokeWidth?: number }>;
      badge?: string;
    }>,
  ) =>
    items.map(({ label, icon: Icon, badge }) => (
      <button
        className={`nav-item ${active === label ? "nav-item--active" : ""}`}
        type="button"
        key={label}
        onClick={() => {
          onNavigate(label);
          onClose();
        }}
      >
        <Icon size={17} strokeWidth={1.7} />
        <span>{label}</span>
        {badge ? <span className="nav-badge">{badge}</span> : null}
      </button>
    ));

  return (
    <>
      <button
        className={`nav-scrim ${mobileOpen ? "nav-scrim--visible" : ""}`}
        type="button"
        aria-label="Close navigation"
        onClick={onClose}
      />
      <aside
        className={`sidebar ${mobileOpen ? "sidebar--open" : ""}`}
        aria-hidden={compact && !mobileOpen ? true : undefined}
        inert={compact && !mobileOpen ? true : undefined}
      >
        <div className="sidebar__brand">
          <VirtroidMark />
          <div>
            <strong>Virtroid</strong>
            <span>Operator</span>
          </div>
          <button className="icon-button sidebar__close" type="button" onClick={onClose}>
            <X size={18} />
            <span className="sr-only">Close navigation</span>
          </button>
        </div>

        <nav className="sidebar__nav" aria-label="Operator navigation">
          <div className="nav-section">
            <span className="nav-section__label">Operate</span>
            {renderItems(primaryNavigation)}
          </div>
          <div className="nav-section">
            <span className="nav-section__label">System</span>
            {renderItems(systemNavigation)}
          </div>
        </nav>

        <div className="sidebar__footer">
          <div className="environment-card">
            <div className="environment-card__topline">
              <span><StatusDot status="good" /> Production</span>
              <span>RC</span>
            </div>
            <p>Read-only operations</p>
          </div>
          <button className="operator-chip" type="button" aria-label="Sign out" onClick={onLogout}>
            <span className="operator-avatar">MO</span>
            <span>
              <strong>Operator session</strong>
              <small>{isLiveOperatorMode() ? "Sign out" : "Preview mode"}</small>
            </span>
            <ChevronRight size={16} />
          </button>
        </div>
      </aside>
    </>
  );
}

function Topbar({
  onOpenSearch,
  onOpenNav,
}: {
  onOpenSearch: () => void;
  onOpenNav: () => void;
}) {
  return (
    <header className="topbar">
      <button className="icon-button topbar__menu" type="button" onClick={onOpenNav}>
        <Menu size={19} />
        <span className="sr-only">Open navigation</span>
      </button>
      <button className="search-trigger" type="button" onClick={onOpenSearch}>
        <Search size={16} />
        <span>Search accounts, runtimes, nodes…</span>
        <kbd><Command size={12} /> K</kbd>
      </button>
      <div className="topbar__actions">
        <span className="sync-state"><StatusDot status="good" /> Live</span>
        <button className="icon-button notification-button" type="button" aria-label="Notifications">
          <Bell size={18} />
          <span className="notification-button__dot" />
        </button>
      </div>
    </header>
  );
}

function SystemTopology({ overview }: { overview: OverviewResponse }) {
  const activeRuntimeCount = overview.metrics.find((metric) => metric.label === "Active runtimes")?.value
    ?? String(overview.runtimes.filter((runtime) => runtime.status === "running").length);
  return (
    <div className="topology" role="img" aria-label={`Control plane topology is ${overview.status}`}>
      <div className="topology__beam" />
      <svg viewBox="0 0 520 245" role="img" aria-hidden="true">
        <defs>
          <linearGradient id="lineGradient" x1="0" y1="0" x2="1" y2="0">
            <stop offset="0" stopColor="#4b6252" stopOpacity="0.16" />
            <stop offset="0.5" stopColor="#acdabc" stopOpacity="0.9" />
            <stop offset="1" stopColor="#4b6252" stopOpacity="0.16" />
          </linearGradient>
          <filter id="softGlow">
            <feGaussianBlur stdDeviation="4" result="blur" />
            <feMerge><feMergeNode in="blur" /><feMergeNode in="SourceGraphic" /></feMerge>
          </filter>
        </defs>
        <path className="topology__line topology__line--one" d="M152 122 C202 122, 210 74, 260 74" />
        <path className="topology__line topology__line--two" d="M260 74 C310 74, 318 122, 368 122" />
        <path className="topology__line topology__line--three" d="M260 74 C260 124, 260 142, 260 182" />
        <circle className="topology__pulse topology__pulse--one" r="3" fill="#acdabc" filter="url(#softGlow)" />
        <circle className="topology__pulse topology__pulse--two" r="3" fill="#acdabc" filter="url(#softGlow)" />
      </svg>

      <div className="topology-node topology-node--control">
        <div className="topology-node__icon"><Fingerprint size={19} /></div>
        <span>Control plane</span>
        <small>Ready</small>
      </div>
      <div className="topology-node topology-node--host">
        <div className="topology-node__icon topology-node__icon--primary"><Server size={20} /></div>
        <span>{overview.fleet.name}</span>
        <small>{overview.fleet.heartbeat}</small>
      </div>
      <div className="topology-node topology-node--runtime">
        <div className="topology-node__icon"><Boxes size={19} /></div>
        <span>Runtimes</span>
        <small>{activeRuntimeCount} active</small>
      </div>
      <div className="topology-node topology-node--storage">
        <div className="topology-node__icon"><Database size={19} /></div>
        <span>Persistence</span>
        <small>Connected</small>
      </div>
    </div>
  );
}

function MetricCard({ metric, index }: { metric: Metric; index: number }) {
  return (
    <article className="metric-card reveal" style={{ "--delay": `${index * 65}ms` } as React.CSSProperties}>
      <div className="metric-card__topline">
        <span>{metric.label}</span>
        <span className={`metric-card__signal metric-card__signal--${metric.tone}`} />
      </div>
      <div className="metric-card__value">{metric.value}</div>
      <div className="metric-card__footer">
        <span>{metric.detail}</span>
        {metric.trend ? <small>{metric.trend}</small> : null}
      </div>
    </article>
  );
}

function ActivityChart({ points }: { points: ActivityPoint[] }) {
  const makePath = (key: "sessions" | "operations") => {
    const width = 610;
    const height = 150;
    const max = Math.max(...points.map((point) => point[key]), 1);
    return points
      .map((point, index) => {
        const x = (index / (points.length - 1)) * width;
        const y = height - (point[key] / max) * 122;
        return `${index === 0 ? "M" : "L"}${x.toFixed(1)} ${y.toFixed(1)}`;
      })
      .join(" ");
  };

  return (
    <div className="activity-chart">
      <svg viewBox="0 0 610 178" preserveAspectRatio="none" aria-label="Runtime activity during the last 24 hours">
        <defs>
          <linearGradient id="areaFill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="#acdabc" stopOpacity="0.22" />
            <stop offset="1" stopColor="#acdabc" stopOpacity="0" />
          </linearGradient>
        </defs>
        {[30, 70, 110, 150].map((y) => <line key={y} x1="0" x2="610" y1={y} y2={y} className="chart-grid" />)}
        <path d={`${makePath("sessions")} L610 158 L0 158 Z`} fill="url(#areaFill)" />
        <path d={makePath("sessions")} className="chart-line chart-line--primary" />
        <path d={makePath("operations")} className="chart-line chart-line--secondary" />
      </svg>
      <div className="chart-labels">
        {points.map((point) => <span key={point.label}>{point.label}</span>)}
      </div>
    </div>
  );
}

function PanelHeader({
  eyebrow,
  title,
  action,
}: {
  eyebrow?: string;
  title: string;
  action?: string;
}) {
  return (
    <div className="panel-header">
      <div>
        {eyebrow ? <span className="eyebrow">{eyebrow}</span> : null}
        <h2>{title}</h2>
      </div>
      {action ? (
        <button className="text-button" type="button">
          {action}<ArrowRight size={14} />
        </button>
      ) : null}
    </div>
  );
}

function RuntimeStatus({ runtime }: { runtime: RuntimeSummary }) {
  const status = runtime.status === "running" ? "good" : runtime.status === "attention" ? "warn" : "muted";
  return (
    <span className={`runtime-status runtime-status--${runtime.status}`}>
      <StatusDot status={status} />
      {runtime.status === "attention" ? "Attention" : runtime.status[0].toUpperCase() + runtime.status.slice(1)}
    </span>
  );
}

function RuntimeTable({
  runtimes,
  onSelect,
}: {
  runtimes: RuntimeSummary[];
  onSelect: (runtime: RuntimeSummary) => void;
}) {
  return (
    <div className="runtime-table-wrap">
      <table className="runtime-table">
        <thead>
          <tr>
            <th>Runtime</th>
            <th>Status</th>
            <th>Host</th>
            <th>Session</th>
            <th>CPU</th>
            <th>Updated</th>
            <th><span className="sr-only">Open</span></th>
          </tr>
        </thead>
        <tbody>
          {runtimes.map((runtime) => (
            <tr key={runtime.id}>
              <td>
                <button className="runtime-name" type="button" onClick={() => onSelect(runtime)}>
                  <span className="runtime-name__icon"><Boxes size={16} /></span>
                  <span><strong>{runtime.name}</strong><small>{runtime.shortId}</small></span>
                </button>
              </td>
              <td><RuntimeStatus runtime={runtime} /></td>
              <td><span className="table-primary">{runtime.host}</span></td>
              <td>
                <span className="table-primary">{runtime.activeSession ? `Active · ${runtime.sessionAge}` : "No session"}</span>
              </td>
              <td>
                <div className="cpu-meter"><span style={{ width: `${Math.max(runtime.cpu, 2)}%` }} /></div>
                <span className="table-secondary">{runtime.cpu}%</span>
              </td>
              <td><span className="table-secondary">{runtime.updated}</span></td>
              <td>
                <button className="row-button" type="button" onClick={() => onSelect(runtime)} aria-label={`Inspect ${runtime.name}`}>
                  <ChevronRight size={16} />
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function IncidentRow({ incident }: { incident: Incident }) {
  const Icon = incident.severity === "warning" ? AlertTriangle : ShieldCheck;
  return (
    <article className="incident-row">
      <span className={`incident-row__icon incident-row__icon--${incident.severity}`}><Icon size={16} /></span>
      <div>
        <div className="incident-row__topline">
          <strong>{incident.title}</strong>
          <span>{incident.age}</span>
        </div>
        <p>{incident.detail}</p>
        <small>{incident.id} · {incident.source}</small>
      </div>
    </article>
  );
}

function RuntimeDrawer({ runtime, onClose }: { runtime: RuntimeSummary; onClose: () => void }) {
  useEffect(() => {
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);

  return (
    <>
      <button className="drawer-scrim" type="button" aria-label="Close runtime inspector" onClick={onClose} />
      <aside className="runtime-drawer" role="dialog" aria-modal="true" aria-labelledby="runtime-drawer-title">
        <div className="runtime-drawer__header">
          <div>
            <span className="eyebrow">Runtime inspector</span>
            <h2 id="runtime-drawer-title">{runtime.name}</h2>
          </div>
          <button className="icon-button" type="button" onClick={onClose} aria-label="Close runtime inspector">
            <X size={18} />
          </button>
        </div>
        <div className="runtime-drawer__status">
          <RuntimeStatus runtime={runtime} />
          <span>Observed {runtime.updated}</span>
        </div>

        {runtime.lastError ? (
          <div className="drawer-alert">
            <AlertTriangle size={17} />
            <div><strong>Reconciliation required</strong><p>{runtime.lastError}</p></div>
          </div>
        ) : null}

        <section className="drawer-section">
          <h3>Identity</h3>
          <dl className="detail-grid">
            <div><dt>Runtime ID</dt><dd>{runtime.id}</dd></div>
            <div><dt>Account</dt><dd>{runtime.accountId}</dd></div>
            <div><dt>Persona</dt><dd>Generation {runtime.persona}</dd></div>
            <div><dt>Profile</dt><dd>{runtime.deviceProfile}</dd></div>
          </dl>
        </section>

        <section className="drawer-section">
          <h3>Current observation</h3>
          <div className="observation-grid">
            <div><span>Desired</span><strong>{runtime.desiredState}</strong></div>
            <div><span>Connection</span><strong>{runtime.connection}</strong></div>
            <div><span>CPU</span><strong>{runtime.cpu}%</strong></div>
            <div><span>Memory</span><strong>{runtime.memory}</strong></div>
            <div><span>Storage</span><strong>{runtime.storage}</strong></div>
            <div><span>Snapshot</span><strong>{runtime.snapshot}</strong></div>
          </div>
        </section>

        <section className="drawer-section">
          <h3>Recent timeline</h3>
          <ol className="runtime-timeline">
            <li><span /><div><strong>Observation received</strong><small>{runtime.updated} · {runtime.host}</small></div></li>
            {runtime.activeSession ? <li><span /><div><strong>Viewer session attached</strong><small>{runtime.sessionAge} ago · authenticated device</small></div></li> : null}
            <li><span /><div><strong>Snapshot verified</strong><small>{runtime.snapshot}</small></div></li>
          </ol>
        </section>

        <div className="read-only-note">
          <Fingerprint size={18} />
          <div>
            <strong>Read-only foundation</strong>
            <p>Privileged actions remain unavailable until operator authentication, step-up approval and audit execution are connected.</p>
          </div>
        </div>
      </aside>
    </>
  );
}

function CommandPalette({
  overview,
  onClose,
  onSelectRuntime,
  onNavigate,
}: {
  overview: OverviewResponse;
  onClose: () => void;
  onSelectRuntime: (runtime: RuntimeSummary) => void;
  onNavigate: (label: string) => void;
}) {
  const [query, setQuery] = useState("");
  const results = useMemo(() => {
    const navigation = [...primaryNavigation, ...systemNavigation].map((item) => ({
      key: `nav-${item.label}`,
      label: item.label,
      meta: "Open section",
      action: () => onNavigate(item.label),
      icon: item.icon,
    }));
    const runtimes = overview.runtimes.map((runtime) => ({
      key: runtime.id,
      label: runtime.name,
      meta: `${runtime.shortId} · ${runtime.status}`,
      action: () => onSelectRuntime(runtime),
      icon: Boxes,
    }));
    const normalized = query.trim().toLowerCase();
    return [...runtimes, ...navigation].filter((item) =>
      `${item.label} ${item.meta}`.toLowerCase().includes(normalized),
    );
  }, [onNavigate, onSelectRuntime, overview.runtimes, query]);

  useEffect(() => {
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);

  return (
    <div className="palette-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="command-palette" role="dialog" aria-modal="true" aria-label="Global search">
        <div className="command-palette__input">
          <Search size={18} />
          <input
            autoFocus
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search resources or go to…"
            aria-label="Search resources"
          />
          <kbd>ESC</kbd>
        </div>
        <div className="command-palette__results">
          <span className="command-palette__label">{query ? `${results.length} results` : "Suggested"}</span>
          {results.slice(0, 8).map(({ key, label, meta, action, icon: Icon }) => (
            <button
              type="button"
              key={key}
              onClick={() => {
                action();
                onClose();
              }}
            >
              <span><Icon size={16} /></span>
              <div><strong>{label}</strong><small>{meta}</small></div>
              <ChevronRight size={15} />
            </button>
          ))}
          {results.length === 0 ? <p className="command-palette__empty">No matching operator resources.</p> : null}
        </div>
        <footer><span><kbd>↵</kbd> Open</span><span><kbd>ESC</kbd> Close</span><small>Preview data only</small></footer>
      </section>
    </div>
  );
}

function CommandCentre({
  overview,
  source,
  onSelectRuntime,
}: {
  overview: OverviewResponse;
  source: OperatorApiResult["source"];
  onSelectRuntime: (runtime: RuntimeSummary) => void;
}) {
  const headlineWords = overview.headline.replace(/[.!?]+$/, "").split(" ");
  const headlineAccent = headlineWords.pop() ?? "";
  const headlineAnchor = headlineWords.pop() ?? "";
  const headlineLead = headlineWords.join(" ");
  const overviewStatus = overview.status === "healthy" ? "good" : overview.status === "degraded" ? "warn" : "bad";
  const totalSessions = overview.activity.reduce((sum, point) => sum + point.sessions, 0);
  const totalOperations = overview.activity.reduce((sum, point) => sum + point.operations, 0);
  const hasCriticalIncidents = overview.incidents.some((incident) => incident.severity === "critical");
  const hasScrollableIncidents = overview.incidents.length > 2;

  return (
    <>
      <section className="system-hero reveal">
        <div className="system-hero__copy">
          <span className="hero-kicker"><Sparkles size={13} /> System pulse</span>
          <h1>
            {headlineLead ? `${headlineLead} ` : ""}
            <span className="headline-lockup">{headlineAnchor ? `${headlineAnchor} ` : ""}<em>{headlineAccent}.</em></span>
          </h1>
          <p>{overview.subline}</p>
          <div className="hero-meta">
            <span><StatusDot status={overviewStatus} /> {overview.fleet.ready}/{overview.fleet.total} nodes ready</span>
            <span><Clock3 size={14} /> Updated just now</span>
            <span className="preview-pill">{source === "preview-fixture" ? "Preview dataset" : "Operator API"}</span>
          </div>
        </div>
        <SystemTopology overview={overview} />
      </section>

      <section className="metric-grid" aria-label="System metrics">
        {overview.metrics.map((metric, index) => <MetricCard key={metric.label} metric={metric} index={index} />)}
      </section>

      <div className="dashboard-grid dashboard-grid--primary">
        <section className="panel activity-panel reveal" style={{ "--delay": "280ms" } as React.CSSProperties}>
          <PanelHeader eyebrow="Last 24 hours" title="Runtime activity" action="Open analytics" />
          <div className="activity-summary">
            <div><strong>{totalSessions}</strong><span>Sessions started</span></div>
            <div><strong>{totalOperations}</strong><span>Runtime starts</span></div>
            <div className="activity-legend"><span><i className="legend-dot legend-dot--mint" /> Sessions</span><span><i className="legend-dot" /> Operations</span></div>
          </div>
          <ActivityChart points={overview.activity} />
        </section>

        <section className="panel attention-panel reveal" style={{ "--delay": "340ms" } as React.CSSProperties}>
          <PanelHeader eyebrow="Triage queue" title="Needs attention" action="View all" />
          <div
            className={`incident-list${hasScrollableIncidents ? " incident-list--scrollable" : ""}`}
            role="region"
            aria-label="Triage queue incidents"
            tabIndex={hasScrollableIncidents ? 0 : undefined}
          >
            {overview.incidents.map((incident) => <IncidentRow key={incident.id} incident={incident} />)}
          </div>
          <div className="attention-panel__footer">
            {hasCriticalIncidents ? <AlertTriangle size={15} /> : <Check size={15} />}
            {overview.incidents.length} recent {overview.incidents.length === 1 ? "signal" : "signals"} ·{" "}
            {hasCriticalIncidents ? "Critical incident present" : "No critical incidents"}
          </div>
        </section>
      </div>

      <section className="panel runtimes-panel reveal" style={{ "--delay": "390ms" } as React.CSSProperties}>
        <PanelHeader eyebrow="Observed resources" title="Recent runtimes" action="View all runtimes" />
        <RuntimeTable runtimes={overview.runtimes} onSelect={onSelectRuntime} />
      </section>

      <div className="dashboard-grid dashboard-grid--secondary">
        <section className="panel hygiene-panel reveal" style={{ "--delay": "450ms" } as React.CSSProperties}>
          <PanelHeader eyebrow="Last scan · 18 min ago" title="Data hygiene" action="Open checks" />
          <div className="hygiene-list">
            {overview.hygiene.map((item) => (
              <div className="hygiene-row" key={item.label}>
                <span className={`hygiene-row__icon hygiene-row__icon--${item.status}`}>
                  {item.status === "passed" ? <Check size={14} /> : item.status === "attention" ? <AlertTriangle size={14} /> : <Clock3 size={14} />}
                </span>
                <div><strong>{item.label}</strong><small>{item.detail}</small></div>
                <ChevronRight size={15} />
              </div>
            ))}
          </div>
        </section>

        <section className="panel deployment-panel reveal" style={{ "--delay": "510ms" } as React.CSSProperties}>
          <PanelHeader eyebrow="Deployment identity" title="Current release" action="Release details" />
          <div className="deployment-state">
            <span className="release-orbit"><PackageCheck size={22} /></span>
            <div><strong>{overview.deployment.version}</strong><small>Verified {overview.deployment.deployed}</small></div>
            <span className="verified-chip"><Check size={12} /> Verified</span>
          </div>
          <dl className="deployment-details">
            <div><dt>Git commit</dt><dd>{overview.deployment.commit}</dd></div>
            <div><dt>Schema</dt><dd>{overview.deployment.schema}</dd></div>
            <div><dt>Configuration</dt><dd>In sync</dd></div>
          </dl>
        </section>
      </div>
    </>
  );
}

function SectionPlaceholder({ section, onReturn }: { section: string; onReturn: () => void }) {
  return (
    <section className="placeholder-page reveal">
      <span className="hero-kicker"><Sparkles size={13} /> Planned workspace</span>
      <h1>{section}</h1>
      <p>The navigation destination is established. Its live read models and controlled workflows arrive in the next implementation slice.</p>
      <button className="primary-button" type="button" onClick={onReturn}>Return to Command centre <ArrowRight size={15} /></button>
    </section>
  );
}

function LoadingState() {
  return (
    <div className="loading-state" aria-label="Loading operator overview">
      <VirtroidMark />
      <span>Establishing operating picture…</span>
    </div>
  );
}

function LoginScreen({ onAuthenticated }: { onAuthenticated: () => Promise<void> }) {
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await createSession(token);
      setToken("");
      await onAuthenticated();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Unable to authenticate");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <main className="login-page">
      <section className="login-card">
        <div className="login-card__brand"><VirtroidMark /><span><strong>Virtroid</strong><small>Operator control plane</small></span></div>
        <span className="hero-kicker"><ShieldCheck size={13} /> Restricted operations</span>
        <h1>Enter the <em>control room.</em></h1>
        <p>Authenticate with the bootstrap operator token. The first release is read-only and exposes sanitized operational telemetry only.</p>
        <form onSubmit={submit}>
          <label htmlFor="operator-token">Operator access token</label>
          <input
            id="operator-token"
            type="password"
            autoComplete="current-password"
            value={token}
            onChange={(event) => setToken(event.target.value)}
            required
            autoFocus
          />
          {error ? <div className="login-error" role="alert"><AlertTriangle size={15} /> {error}</div> : null}
          <button className="primary-button" type="submit" disabled={submitting}>
            {submitting ? "Authenticating…" : "Open command centre"}<ArrowRight size={15} />
          </button>
        </form>
        <small className="login-card__note">Sessions expire automatically and the token is never stored in browser storage.</small>
      </section>
    </main>
  );
}

function App() {
  const [result, setResult] = useState<OperatorApiResult | null>(null);
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [activeSection, setActiveSection] = useState("Command centre");
  const [selectedRuntime, setSelectedRuntime] = useState<RuntimeSummary | null>(null);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [compact, setCompact] = useState(() => window.matchMedia("(max-width: 760px)").matches);

  const loadOverview = async () => {
    const overview = await getOverview();
    setResult(overview);
    setAuthenticated(true);
  };

  useEffect(() => {
    const controller = new AbortController();
    getSession(controller.signal)
      .then(() => getOverview(controller.signal))
      .then((overview) => {
        setAuthenticated(true);
        setResult(overview);
      }).catch((reason: unknown) => {
      if (reason instanceof DOMException && reason.name === "AbortError") return;
      if (reason instanceof OperatorApiError && reason.status === 401) {
        setAuthenticated(false);
        return;
      }
      setError(reason instanceof Error ? reason.message : "Unable to load the operator overview");
    });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const media = window.matchMedia("(max-width: 760px)");
    const update = () => setCompact(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    const openPalette = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const typing = target?.tagName === "INPUT" || target?.tagName === "TEXTAREA";
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setPaletteOpen(true);
      } else if (event.key === "/" && !typing) {
        event.preventDefault();
        setPaletteOpen(true);
      }
    };
    window.addEventListener("keydown", openPalette);
    return () => window.removeEventListener("keydown", openPalette);
  }, []);

  if (error) {
    return (
      <main className="fatal-state">
        <AlertTriangle size={24} />
        <h1>Operator overview unavailable</h1>
        <p>{error}</p>
      </main>
    );
  }

  if (authenticated === false) {
    return <LoginScreen onAuthenticated={loadOverview} />;
  }

  if (!result) return <LoadingState />;

  const navigate = (section: string) => {
    setActiveSection(section);
    setSelectedRuntime(null);
  };

  return (
    <div className="app-shell">
      <Sidebar
        active={activeSection}
        onNavigate={navigate}
        mobileOpen={mobileNavOpen}
        compact={compact}
        onClose={() => setMobileNavOpen(false)}
        onLogout={async () => {
          await deleteSession();
          if (isLiveOperatorMode()) {
            setResult(null);
            setAuthenticated(false);
          }
        }}
      />
      <div className="workspace">
        <Topbar onOpenSearch={() => setPaletteOpen(true)} onOpenNav={() => setMobileNavOpen(true)} />
        <main className="content">
          {activeSection === "Command centre" ? (
            <CommandCentre overview={result.data} source={result.source} onSelectRuntime={setSelectedRuntime} />
          ) : (
            <SectionPlaceholder section={activeSection} onReturn={() => navigate("Command centre")} />
          )}
        </main>
      </div>

      {selectedRuntime ? <RuntimeDrawer runtime={selectedRuntime} onClose={() => setSelectedRuntime(null)} /> : null}
      {paletteOpen ? (
        <CommandPalette
          overview={result.data}
          onClose={() => setPaletteOpen(false)}
          onSelectRuntime={setSelectedRuntime}
          onNavigate={navigate}
        />
      ) : null}
    </div>
  );
}

export default App;
