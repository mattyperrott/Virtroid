import {
  Activity,
  AlertTriangle,
  ArrowRight,
  Bell,
  Boxes,
  CalendarClock,
  Check,
  ChevronRight,
  Clock3,
  Command,
  Database,
  Download,
  FileClock,
  FileText,
  Filter,
  Fingerprint,
  HardDrive,
  History,
  KeyRound,
  LayoutDashboard,
  ListTree,
  Menu,
  MonitorCheck,
  PackageCheck,
  Search,
  Server,
  ShieldCheck,
  Sparkles,
  UserRound,
  Users,
  Wrench,
  X,
} from "lucide-react";
import { useEffect, useId, useMemo, useState } from "react";
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
  HygieneCheck,
  Incident,
  Metric,
  OverviewResponse,
  RuntimeSummary,
  Severity,
} from "./types";

const primaryNavigation = [
  { label: "Command centre", icon: LayoutDashboard },
  { label: "Runtimes", icon: Boxes },
  { label: "Sessions", icon: Activity },
  { label: "Accounts", icon: Users },
  { label: "Fleet", icon: Server },
];

const systemNavigation = [
  { label: "Security", icon: ShieldCheck },
  { label: "Logs", icon: ListTree },
  { label: "Data hygiene", icon: Wrench },
  { label: "Storage", icon: HardDrive },
  { label: "Releases", icon: PackageCheck },
  { label: "Reports", icon: FileClock },
];

function VirtroidMark() {
  const logoId = `virtroid-${useId().replace(/:/g, "")}`;

  return (
    <div className="brand-mark" aria-hidden="true">
      <svg viewBox="140 122 152 190" fill="none" xmlns="http://www.w3.org/2000/svg" focusable="false">
        <defs>
          <linearGradient id={`${logoId}-screen`} gradientUnits="userSpaceOnUse" x1="160" y1="158" x2="255" y2="260">
            <stop stopColor="#afd135" />
            <stop offset="1" stopColor="#92b52d" />
          </linearGradient>
          <linearGradient id={`${logoId}-facet`} gradientUnits="userSpaceOnUse" x1="186" y1="227" x2="247" y2="275">
            <stop stopColor="#b4d938" />
            <stop offset="1" stopColor="#9abf29" />
          </linearGradient>
          <linearGradient id={`${logoId}-white`} x1="0" y1="0" x2="1" y2="1">
            <stop stopColor="#ffffff" />
            <stop offset=".4" stopColor="#f0f3ee" />
            <stop offset="1" stopColor="#ccd7cb" />
          </linearGradient>
          <clipPath id={`${logoId}-shape`}>
            <path d="M156 158H270V192L156 172ZM156 178L270 198V207L156 249ZM160 255L273 211L275 258L160 265Z" />
          </clipPath>
          <linearGradient id={`${logoId}-shine`} x1="0" x2="1">
            <stop offset="0" stopColor="#ffffff" stopOpacity="0" />
            <stop offset=".5" stopColor="#ffffff" stopOpacity=".7" />
            <stop offset="1" stopColor="#ffffff" stopOpacity="0" />
          </linearGradient>
        </defs>
        <g className="brand-mark__body">
          <path className="brand-mark__energy" d="M148 257L284 207" />
          <g className="brand-mark__piece brand-mark__top">
            <path d="M156 151V145Q156 132 169 132H257Q270 132 271 146V151Z" fill={`url(#${logoId}-white)`} />
            <path d="M158 149H269" stroke="#f0f3ee" strokeWidth="2" strokeOpacity=".8" />
          </g>
          <g className="brand-mark__piece brand-mark__upper">
            <path d="M156 158H270V192L156 172Z" fill={`url(#${logoId}-screen)`} />
            <path d="M158 159H269V191" stroke="#c8e160" strokeWidth="2" strokeOpacity=".6" />
          </g>
          <g className="brand-mark__piece brand-mark__middle">
            <path d="M156 178L270 198V207L156 249Z" fill={`url(#${logoId}-screen)`} />
            <path d="M269 199V206L159 246" stroke="#c6db68" strokeOpacity=".35" />
          </g>
          <g className="brand-mark__piece brand-mark__lower">
            <path d="M160 255L273 211L275 258L160 265Z" fill={`url(#${logoId}-facet)`} />
            <path d="M163 258L271 216L272 255L166 262" stroke="#c4df54" strokeWidth="2" strokeOpacity=".35" />
            <path d="M161 265L275 258L274 254L163 262Z" fill="#91b528" />
          </g>
          <g className="brand-mark__piece brand-mark__bottom">
            <path d="M161 274L275 268V287Q275 300 262 301H175Q162 301 161 288Z" fill={`url(#${logoId}-white)`} />
            <path d="M164 276L272 271V286Q272 297 261 298H176Q165 298 164 287Z" fill="#f0f3ee" fillOpacity=".3" />
            <path d="M208 282L233 280Q237 280 237 283Q237 286 233 287L208 289Q204 289 204 286Q204 283 208 282Z" fill="#18221b" />
          </g>
          <g clipPath={`url(#${logoId}-shape)`}>
            <path className="brand-mark__glint" d="M145 110H170L240 325H215Z" fill={`url(#${logoId}-shine)`} />
          </g>
        </g>
      </svg>
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
  overview,
}: {
  active: string;
  onNavigate: (label: string) => void;
  mobileOpen: boolean;
  compact: boolean;
  onClose: () => void;
  onLogout: () => void;
  overview: OverviewResponse;
}) {
  const securityBadgeCount = overview.incidents.filter((incident) => incident.severity !== "info").length;
  const hygieneBadgeCount = overview.hygiene.filter((check) => check.status === "attention").length;
  const badges: Record<string, string | undefined> = {
    Security: securityBadgeCount ? String(securityBadgeCount) : undefined,
    "Data hygiene": hygieneBadgeCount ? String(hygieneBadgeCount) : undefined,
  };
  const renderItems = (
    items: Array<{
      label: string;
      icon: React.ComponentType<{ size?: number; strokeWidth?: number }>;
      badge?: string;
    }>,
  ) =>
    items.map(({ label, icon: Icon, badge }) => {
      const displayBadge = badges[label] ?? badge;
      return (
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
        {displayBadge ? <span className="nav-badge">{displayBadge}</span> : null}
      </button>
      );
    });

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
              <span><StatusDot status={overview.status === "healthy" ? "good" : overview.status === "degraded" ? "warn" : "bad"} /> {overview.environment}</span>
              <span>{overview.deployment.version === "Release candidate" ? "RC" : overview.deployment.version.slice(0, 8)}</span>
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
  onOpenNotifications,
}: {
  onOpenSearch: () => void;
  onOpenNav: () => void;
  onOpenNotifications: () => void;
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
        <button className="icon-button notification-button" type="button" aria-label="Notifications" onClick={onOpenNotifications}>
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
            <stop offset="0" stopColor="#78951e" stopOpacity="0.16" />
            <stop offset="0.5" stopColor="#afd135" stopOpacity="0.9" />
            <stop offset="1" stopColor="#78951e" stopOpacity="0.16" />
          </linearGradient>
          <filter id="softGlow">
            <feGaussianBlur stdDeviation="4" result="blur" />
            <feMerge><feMergeNode in="blur" /><feMergeNode in="SourceGraphic" /></feMerge>
          </filter>
        </defs>
        <path className="topology__line topology__line--one" d="M152 122 C202 122, 210 74, 260 74" />
        <path className="topology__line topology__line--two" d="M260 74 C310 74, 318 122, 368 122" />
        <path className="topology__line topology__line--three" d="M260 74 C260 124, 260 142, 260 182" />
        <circle className="topology__pulse topology__pulse--one" r="3" fill="#afd135" filter="url(#softGlow)" />
        <circle className="topology__pulse topology__pulse--two" r="3" fill="#afd135" filter="url(#softGlow)" />
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
            <stop offset="0" stopColor="#afd135" stopOpacity="0.22" />
            <stop offset="1" stopColor="#afd135" stopOpacity="0" />
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
  onAction,
}: {
  eyebrow?: string;
  title: string;
  action?: string;
  onAction?: () => void;
}) {
  return (
    <div className="panel-header">
      <div>
        {eyebrow ? <span className="eyebrow">{eyebrow}</span> : null}
        <h2>{title}</h2>
      </div>
      {action ? (
        <button className="text-button" type="button" onClick={onAction}>
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
          <strong title={incident.title}>{incident.title}</strong>
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
  onNavigate,
}: {
  overview: OverviewResponse;
  source: OperatorApiResult["source"];
  onSelectRuntime: (runtime: RuntimeSummary) => void;
  onNavigate: (section: string) => void;
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
          <PanelHeader eyebrow="Last 24 hours" title="Runtime activity" action="Open analytics" onAction={() => onNavigate("Reports")} />
          <div className="activity-summary">
            <div><strong>{totalSessions}</strong><span>Sessions started</span></div>
            <div><strong>{totalOperations}</strong><span>Runtime starts</span></div>
            <div className="activity-legend"><span><i className="legend-dot legend-dot--mint" /> Sessions</span><span><i className="legend-dot" /> Operations</span></div>
          </div>
          <ActivityChart points={overview.activity} />
        </section>

        <section className="panel attention-panel reveal" style={{ "--delay": "340ms" } as React.CSSProperties}>
          <PanelHeader eyebrow="Triage queue" title="Needs attention" action="View all" onAction={() => onNavigate("Security")} />
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
        <PanelHeader eyebrow="Observed resources" title="Recent runtimes" action="View all runtimes" onAction={() => onNavigate("Runtimes")} />
        <RuntimeTable runtimes={overview.runtimes} onSelect={onSelectRuntime} />
      </section>

      <div className="dashboard-grid dashboard-grid--secondary">
        <section className="panel hygiene-panel reveal" style={{ "--delay": "450ms" } as React.CSSProperties}>
          <PanelHeader eyebrow="Last scan · 18 min ago" title="Data hygiene" action="Open checks" onAction={() => onNavigate("Data hygiene")} />
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
          <PanelHeader eyebrow="Deployment identity" title="Current release" action="Release details" onAction={() => onNavigate("Releases")} />
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

type WorkspaceTone = "good" | "warn" | "bad" | "muted";

function WorkspaceHeader({
  eyebrow,
  title,
  description,
  overview,
  icon: Icon,
  children,
}: {
  eyebrow: string;
  title: string;
  description: string;
  overview: OverviewResponse;
  icon: React.ComponentType<{ size?: number; strokeWidth?: number }>;
  children?: React.ReactNode;
}) {
  return (
    <header className="workspace-header reveal">
      <div className="workspace-header__icon"><Icon size={23} strokeWidth={1.6} /></div>
      <div className="workspace-header__copy">
        <span className="eyebrow">{eyebrow}</span>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      <div className="workspace-header__meta">
        {children}
        <span><StatusDot status={overview.status === "healthy" ? "good" : overview.status === "degraded" ? "warn" : "bad"} /> {overview.environment}</span>
        <span><Clock3 size={13} /> Snapshot {new Date(overview.generatedAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</span>
      </div>
    </header>
  );
}

function SummaryCards({ items }: { items: Array<{ label: string; value: string | number; detail: string; tone?: WorkspaceTone }> }) {
  return (
    <section className="workspace-summary" aria-label="Workspace summary">
      {items.map((item, index) => (
        <article className="workspace-summary__card reveal" style={{ "--delay": `${70 + index * 45}ms` } as React.CSSProperties} key={item.label}>
          <div><span>{item.label}</span><StatusDot status={item.tone ?? "muted"} /></div>
          <strong>{item.value}</strong>
          <small>{item.detail}</small>
        </article>
      ))}
    </section>
  );
}

function WorkspaceToolbar({
  query,
  onQuery,
  placeholder,
  filters,
  activeFilter,
  onFilter,
}: {
  query: string;
  onQuery: (value: string) => void;
  placeholder: string;
  filters?: Array<{ value: string; label: string }>;
  activeFilter?: string;
  onFilter?: (value: string) => void;
}) {
  return (
    <div className="workspace-toolbar">
      <label className="workspace-search">
        <Search size={15} />
        <span className="sr-only">Search this workspace</span>
        <input value={query} onChange={(event) => onQuery(event.target.value)} placeholder={placeholder} />
      </label>
      {filters && onFilter ? (
        <div className="filter-tabs" aria-label="Filter results"><Filter size={14} />
          {filters.map((filter) => (
            <button className={activeFilter === filter.value ? "filter-tab filter-tab--active" : "filter-tab"} type="button" key={filter.value} onClick={() => onFilter(filter.value)}>
              {filter.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function EmptyWorkspace({ title, detail }: { title: string; detail: string }) {
  return <div className="workspace-empty"><Search size={20} /><strong>{title}</strong><p>{detail}</p></div>;
}

function RuntimesWorkspace({ overview, onSelectRuntime }: { overview: OverviewResponse; onSelectRuntime: (runtime: RuntimeSummary) => void }) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return overview.runtimes.filter((runtime) =>
      (filter === "all" || runtime.status === filter)
      && `${runtime.name} ${runtime.id} ${runtime.accountId} ${runtime.host} ${runtime.deviceProfile}`.toLowerCase().includes(normalized),
    );
  }, [filter, overview.runtimes, query]);
  const running = overview.runtimes.filter((runtime) => runtime.status === "running").length;
  const attention = overview.runtimes.filter((runtime) => runtime.status === "attention").length;

  return <>
    <WorkspaceHeader eyebrow="Runtime inventory" title="Runtimes" description="Inspect desired state, node assignment, viewer attachment and the latest sanitized runtime observation." overview={overview} icon={Boxes} />
    <SummaryCards items={[
      { label: "Observed", value: overview.runtimes.length, detail: "latest runtime records", tone: "muted" },
      { label: "Running", value: running, detail: "currently executing", tone: "good" },
      { label: "Needs attention", value: attention, detail: "reconciliation or cleanup", tone: attention ? "warn" : "good" },
      { label: "Snapshot coverage", value: `${overview.runtimes.filter((runtime) => runtime.snapshot !== "not available").length}/${overview.runtimes.length}`, detail: "latest snapshot observed", tone: "muted" },
    ]} />
    <section className="panel workspace-panel reveal">
      <WorkspaceToolbar query={query} onQuery={setQuery} placeholder="Search runtime, account, host or profile…" filters={[
        { value: "all", label: "All" }, { value: "running", label: "Running" }, { value: "stopped", label: "Stopped" }, { value: "attention", label: "Attention" },
      ]} activeFilter={filter} onFilter={setFilter} />
      {filtered.length ? <RuntimeTable runtimes={filtered} onSelect={onSelectRuntime} /> : <EmptyWorkspace title="No runtimes match" detail="Clear the search or choose another status filter." />}
    </section>
  </>;
}

function SessionsWorkspace({ overview, onSelectRuntime }: { overview: OverviewResponse; onSelectRuntime: (runtime: RuntimeSummary) => void }) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const sessionRows = overview.runtimes.filter((runtime) => runtime.activeSession || filter === "all").filter((runtime) => {
    const normalized = query.trim().toLowerCase();
    return `${runtime.name} ${runtime.shortId} ${runtime.accountShortId} ${runtime.host}`.toLowerCase().includes(normalized);
  });
  const active = overview.runtimes.filter((runtime) => runtime.activeSession).length;
  const unattached = overview.runtimes.filter((runtime) => runtime.status === "running" && !runtime.activeSession).length;

  return <>
    <WorkspaceHeader eyebrow="Viewer activity" title="Sessions" description="Track authenticated viewer attachment without exposing relay credentials, device keys or network endpoints." overview={overview} icon={Activity} />
    <SummaryCards items={[
      { label: "Live sessions", value: active, detail: "authenticated viewers", tone: active ? "good" : "muted" },
      { label: "Running unattached", value: unattached, detail: "runtime online without viewer", tone: unattached ? "warn" : "good" },
      { label: "Session coverage", value: `${overview.runtimes.length ? Math.round(active / overview.runtimes.length * 100) : 0}%`, detail: "of observed runtimes", tone: "muted" },
      { label: "Credential scope", value: "Hidden", detail: "relay secrets excluded", tone: "good" },
    ]} />
    <section className="panel workspace-panel reveal">
      <WorkspaceToolbar query={query} onQuery={setQuery} placeholder="Search runtime, account or host…" filters={[{ value: "all", label: "All runtimes" }, { value: "active", label: "Live only" }]} activeFilter={filter} onFilter={setFilter} />
      {sessionRows.length ? <div className="data-list">
        {sessionRows.map((runtime) => <button className="data-row" type="button" key={runtime.id} onClick={() => onSelectRuntime(runtime)}>
          <span className="data-row__icon"><MonitorCheck size={17} /></span>
          <span className="data-row__identity"><strong>{runtime.name}</strong><small>{runtime.shortId} · account {runtime.accountShortId}</small></span>
          <span className="data-row__cell"><small>Viewer</small><strong className={runtime.activeSession ? "tone-good" : "tone-muted"}>{runtime.activeSession ? "Attached" : "No session"}</strong></span>
          <span className="data-row__cell"><small>Age</small><strong>{runtime.sessionAge ?? "—"}</strong></span>
          <span className="data-row__cell"><small>Host</small><strong>{runtime.host}</strong></span>
          <ChevronRight size={16} />
        </button>)}
      </div> : <EmptyWorkspace title="No sessions match" detail="No current viewer attachment matches this search." />}
    </section>
  </>;
}

interface AccountObservation {
  id: string;
  shortId: string;
  runtimes: RuntimeSummary[];
}

function AccountsWorkspace({ overview, onSelectRuntime }: { overview: OverviewResponse; onSelectRuntime: (runtime: RuntimeSummary) => void }) {
  const [query, setQuery] = useState("");
  const accounts = useMemo(() => {
    const grouped = new Map<string, AccountObservation>();
    overview.runtimes.forEach((runtime) => {
      const account = grouped.get(runtime.accountId) ?? { id: runtime.accountId, shortId: runtime.accountShortId, runtimes: [] };
      account.runtimes.push(runtime);
      grouped.set(runtime.accountId, account);
    });
    const normalized = query.trim().toLowerCase();
    return [...grouped.values()].filter((account) => `${account.id} ${account.runtimes.map((runtime) => runtime.name).join(" ")}`.toLowerCase().includes(normalized));
  }, [overview.runtimes, query]);
  const activeAccounts = overview.metrics.find((metric) => metric.label === "Active accounts")?.value ?? String(accounts.length);

  return <>
    <WorkspaceHeader eyebrow="Identity inventory" title="Accounts" description="Correlate opaque account identifiers with their observed runtimes and viewer activity. Personal profile data is intentionally absent." overview={overview} icon={Users} />
    <SummaryCards items={[
      { label: "Active accounts", value: activeAccounts, detail: "reported by control plane", tone: "good" },
      { label: "In current snapshot", value: accounts.length, detail: "with observed runtimes", tone: "muted" },
      { label: "Multi-runtime", value: accounts.filter((account) => account.runtimes.length > 1).length, detail: "accounts with multiple profiles", tone: "muted" },
      { label: "Identity model", value: "Opaque", detail: "no personal details exposed", tone: "good" },
    ]} />
    <section className="panel workspace-panel reveal">
      <WorkspaceToolbar query={query} onQuery={setQuery} placeholder="Search account ID or runtime…" />
      {accounts.length ? <div className="account-grid">
        {accounts.map((account) => {
          const live = account.runtimes.filter((runtime) => runtime.activeSession).length;
          const needsAttention = account.runtimes.some((runtime) => runtime.status === "attention");
          return <article className="account-card" key={account.id}>
            <div className="account-card__top"><span><UserRound size={17} /></span><div><strong>Account {account.shortId}</strong><small title={account.id}>{account.id}</small></div><StatusDot status={needsAttention ? "warn" : "good"} /></div>
            <div className="account-card__metrics"><span><strong>{account.runtimes.length}</strong> runtimes</span><span><strong>{live}</strong> live viewers</span></div>
            <div className="account-card__runtimes">{account.runtimes.map((runtime) => <button type="button" key={runtime.id} onClick={() => onSelectRuntime(runtime)}><RuntimeStatus runtime={runtime} /><span>{runtime.name}</span><ChevronRight size={14} /></button>)}</div>
          </article>;
        })}
      </div> : <EmptyWorkspace title="No accounts match" detail="The account identifier or runtime name was not found in this snapshot." />}
    </section>
  </>;
}

function FleetWorkspace({ overview }: { overview: OverviewResponse }) {
  const assigned = overview.runtimes.filter((runtime) => runtime.host === overview.fleet.name || overview.fleet.total === 1).length;
  const online = overview.runtimes.filter((runtime) => runtime.connection === "online").length;
  return <>
    <WorkspaceHeader eyebrow="Node fabric" title="Fleet" description="Monitor approved-node readiness, recent heartbeats and the workloads currently assigned to the runtime fabric." overview={overview} icon={Server} />
    <SummaryCards items={[
      { label: "Ready nodes", value: `${overview.fleet.ready}/${overview.fleet.total}`, detail: "approved and observed", tone: overview.fleet.ready === overview.fleet.total ? "good" : "warn" },
      { label: "Assigned runtimes", value: assigned, detail: "in the latest inventory", tone: "muted" },
      { label: "Online runtimes", value: online, detail: "reporting connected", tone: online ? "good" : "muted" },
      { label: "Latest heartbeat", value: overview.fleet.heartbeat, detail: overview.fleet.name, tone: overview.fleet.ready ? "good" : "bad" },
    ]} />
    <div className="workspace-columns">
      <section className="panel workspace-panel reveal">
        <PanelHeader eyebrow="Approved node" title={overview.fleet.name} />
        <div className="node-card">
          <div className="node-card__visual"><Server size={30} /><span className={overview.fleet.ready ? "pulse-ring" : ""} /></div>
          <div className="node-card__state"><strong>{overview.fleet.ready ? "Ready for placement" : "Placement unavailable"}</strong><span><StatusDot status={overview.fleet.ready ? "good" : "bad"} /> heartbeat {overview.fleet.heartbeat}</span></div>
          <dl className="node-facts"><div><dt>Observed capacity</dt><dd>{overview.fleet.capacity}</dd></div><div><dt>Runtime assignments</dt><dd>{assigned}</dd></div><div><dt>Connected</dt><dd>{online}</dd></div><div><dt>Trust boundary</dt><dd>Approved node</dd></div></dl>
        </div>
      </section>
      <section className="panel workspace-panel reveal">
        <PanelHeader eyebrow="Workload distribution" title="Runtime state" />
        <div className="distribution-list">
          {["running", "stopped", "attention"].map((status) => {
            const count = overview.runtimes.filter((runtime) => runtime.status === status).length;
            const percent = overview.runtimes.length ? count / overview.runtimes.length * 100 : 0;
            return <div key={status}><span><strong>{status}</strong><small>{count}</small></span><i><b style={{ width: `${percent}%` }} /></i></div>;
          })}
        </div>
      </section>
    </div>
  </>;
}

function SeverityPill({ severity }: { severity: Severity }) {
  return <span className={`severity-pill severity-pill--${severity}`}>{severity}</span>;
}

function SecurityWorkspace({ overview }: { overview: OverviewResponse }) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const incidents = overview.incidents.filter((incident) => {
    const normalized = query.trim().toLowerCase();
    return (filter === "all" || incident.severity === filter) && `${incident.id} ${incident.title} ${incident.detail} ${incident.source}`.toLowerCase().includes(normalized);
  });
  const important = overview.incidents.filter((incident) => incident.severity !== "info").length;
  return <>
    <WorkspaceHeader eyebrow="Security posture" title="Security" description="Review sanitized control-plane signals. Raw event payloads and sensitive infrastructure details remain in protected backend logs." overview={overview} icon={ShieldCheck} />
    <SummaryCards items={[
      { label: "Recent signals", value: overview.incidents.length, detail: "within the retained snapshot", tone: overview.incidents.length ? "muted" : "good" },
      { label: "Needs triage", value: important, detail: "warning or critical", tone: important ? "warn" : "good" },
      { label: "Critical", value: overview.incidents.filter((incident) => incident.severity === "critical").length, detail: "highest severity", tone: overview.incidents.some((incident) => incident.severity === "critical") ? "bad" : "good" },
      { label: "Console access", value: "Read only", detail: "privileged actions locked", tone: "good" },
    ]} />
    <section className="panel workspace-panel reveal">
      <WorkspaceToolbar query={query} onQuery={setQuery} placeholder="Search signal, source or ID…" filters={[{ value: "all", label: "All" }, { value: "critical", label: "Critical" }, { value: "warning", label: "Warning" }, { value: "info", label: "Info" }]} activeFilter={filter} onFilter={setFilter} />
      {incidents.length ? <div className="event-table">{incidents.map((incident) => <article className="event-row" key={incident.id}>
        <span className={`event-row__marker event-row__marker--${incident.severity}`}><ShieldCheck size={16} /></span>
        <div><span><strong>{incident.title}</strong><SeverityPill severity={incident.severity} /></span><p>{incident.detail}</p><small>{incident.id} · {incident.source}</small></div>
        <time>{incident.age}</time>
      </article>)}</div> : <EmptyWorkspace title="No security signals match" detail="Try a different severity or search term." />}
    </section>
  </>;
}

function LogsWorkspace({ overview, onSelectRuntime }: { overview: OverviewResponse; onSelectRuntime: (runtime: RuntimeSummary) => void }) {
  const [query, setQuery] = useState("");
  const entries = useMemo(() => {
    const signals = overview.incidents.map((incident) => ({ id: incident.id, title: incident.title, detail: incident.detail, source: incident.source, age: incident.age, tone: incident.severity as WorkspaceTone | Severity, runtime: undefined as RuntimeSummary | undefined }));
    const observations = overview.runtimes.map((runtime) => ({ id: `OBS-${runtime.shortId}`, title: `${runtime.name} observation`, detail: `${runtime.status} · desired ${runtime.desiredState} · ${runtime.connection}`, source: runtime.host, age: runtime.updated, tone: runtime.status === "attention" ? "warning" : "info", runtime }));
    const normalized = query.trim().toLowerCase();
    return [...signals, ...observations].filter((entry) => `${entry.id} ${entry.title} ${entry.detail} ${entry.source}`.toLowerCase().includes(normalized));
  }, [overview.incidents, overview.runtimes, query]);
  return <>
    <WorkspaceHeader eyebrow="Sanitized event stream" title="Logs" description="A correlated read model of recent security signals and runtime observations. Raw application and guest logs are not exposed here." overview={overview} icon={ListTree} />
    <SummaryCards items={[
      { label: "Visible entries", value: entries.length, detail: "after local filtering", tone: "muted" },
      { label: "Runtime observations", value: overview.runtimes.length, detail: "latest per runtime", tone: "good" },
      { label: "Security signals", value: overview.incidents.length, detail: "sanitized records", tone: overview.incidents.some((incident) => incident.severity !== "info") ? "warn" : "good" },
      { label: "Sensitive fields", value: "Redacted", detail: "secrets and endpoints excluded", tone: "good" },
    ]} />
    <section className="panel workspace-panel reveal">
      <WorkspaceToolbar query={query} onQuery={setQuery} placeholder="Search events, runtime, source or ID…" />
      {entries.length ? <div className="log-stream">{entries.map((entry) => <button type="button" key={entry.id} onClick={() => entry.runtime && onSelectRuntime(entry.runtime)} disabled={!entry.runtime}>
        <span className={`log-level log-level--${entry.tone}`}>{entry.tone === "critical" ? "CRT" : entry.tone === "warning" ? "WRN" : "INF"}</span>
        <time>{entry.age}</time><div><strong>{entry.title}</strong><small>{entry.detail}</small></div><code>{entry.source}</code>{entry.runtime ? <ChevronRight size={15} /> : <span />}
      </button>)}</div> : <EmptyWorkspace title="No log entries match" detail="Clear the search to restore the sanitized event stream." />}
    </section>
  </>;
}

function HygieneWorkspace({ overview }: { overview: OverviewResponse }) {
  const attention = overview.hygiene.filter((check) => check.status === "attention").length;
  const affected = overview.runtimes.filter((runtime) => runtime.cleanupPending || runtime.status === "attention");
  const iconFor = (item: HygieneCheck) => item.status === "passed" ? <Check size={17} /> : item.status === "attention" ? <AlertTriangle size={17} /> : <CalendarClock size={17} />;
  return <>
    <WorkspaceHeader eyebrow="Integrity controls" title="Data hygiene" description="Review referential integrity, session reaping and runtime sanitation checks from the latest control-plane scan." overview={overview} icon={Wrench} />
    <SummaryCards items={[
      { label: "Checks", value: overview.hygiene.length, detail: "reported controls", tone: "muted" },
      { label: "Passed", value: overview.hygiene.filter((check) => check.status === "passed").length, detail: "no issue observed", tone: "good" },
      { label: "Attention", value: attention, detail: "operator review suggested", tone: attention ? "warn" : "good" },
      { label: "Affected runtimes", value: affected.length, detail: "cleanup or reconciliation", tone: affected.length ? "warn" : "good" },
    ]} />
    <div className="workspace-columns">
      <section className="panel workspace-panel reveal"><PanelHeader eyebrow="Latest scan" title="Control checks" /><div className="check-list">{overview.hygiene.map((item) => <article key={item.label} className={`check-card check-card--${item.status}`}><span>{iconFor(item)}</span><div><strong>{item.label}</strong><p>{item.detail}</p></div><small>{item.status}</small></article>)}</div></section>
      <section className="panel workspace-panel reveal"><PanelHeader eyebrow="Reconciliation scope" title="Affected resources" />{affected.length ? <div className="compact-runtime-list">{affected.map((runtime) => <div key={runtime.id}><span><Boxes size={15} /></span><div><strong>{runtime.name}</strong><small>{runtime.lastError ?? "Cleanup remains pending"}</small></div><RuntimeStatus runtime={runtime} /></div>)}</div> : <div className="success-state"><Check size={22} /><strong>No affected runtimes</strong><p>The latest snapshot has no pending cleanup or runtime reconciliation.</p></div>}</section>
    </div>
  </>;
}

function StorageWorkspace({ overview }: { overview: OverviewResponse }) {
  const providers = useMemo(() => {
    const grouped = new Map<string, number>();
    overview.runtimes.forEach((runtime) => grouped.set(runtime.storage, (grouped.get(runtime.storage) ?? 0) + 1));
    return [...grouped.entries()];
  }, [overview.runtimes]);
  const snapshotted = overview.runtimes.filter((runtime) => runtime.snapshot !== "not available");
  const missing = overview.runtimes.filter((runtime) => runtime.snapshot === "not available");
  return <>
    <WorkspaceHeader eyebrow="Persistence inventory" title="Storage" description="Monitor storage provider assignment and snapshot recency without exposing encrypted manifests, wallet details or blob keys." overview={overview} icon={HardDrive} />
    <SummaryCards items={[
      { label: "Snapshot coverage", value: `${snapshotted.length}/${overview.runtimes.length}`, detail: "runtimes with observed snapshot", tone: missing.length ? "warn" : "good" },
      { label: "Providers", value: providers.length, detail: "storage kinds in use", tone: "muted" },
      { label: "Missing snapshot", value: missing.length, detail: "no timestamp reported", tone: missing.length ? "warn" : "good" },
      { label: "Key material", value: "Protected", detail: "never returned to console", tone: "good" },
    ]} />
    <div className="workspace-columns">
      <section className="panel workspace-panel reveal"><PanelHeader eyebrow="Provider mix" title="Storage assignment" /><div className="provider-list">{providers.map(([provider, count]) => <div key={provider}><span><Database size={17} /></span><div><strong>{provider}</strong><small>{count} {count === 1 ? "runtime" : "runtimes"}</small></div><b>{overview.runtimes.length ? Math.round(count / overview.runtimes.length * 100) : 0}%</b></div>)}</div></section>
      <section className="panel workspace-panel reveal"><PanelHeader eyebrow="Latest per runtime" title="Snapshot ledger" /><div className="snapshot-list">{overview.runtimes.map((runtime) => <div key={runtime.id}><span className={runtime.snapshot === "not available" ? "tone-warn" : "tone-good"}><History size={15} /></span><div><strong>{runtime.name}</strong><small>{runtime.storage}</small></div><time>{runtime.snapshot}</time></div>)}</div></section>
    </div>
  </>;
}

function ReleasesWorkspace({ overview }: { overview: OverviewResponse }) {
  const checks = [
    { label: "Release source", detail: overview.deployment.commit, good: overview.deployment.commit !== "unavailable" },
    { label: "Schema identity", detail: overview.deployment.schema, good: overview.deployment.schema !== "unavailable" },
    { label: "Runtime configuration", detail: "Read model responding", good: true },
    { label: "Node compatibility", detail: `${overview.fleet.ready}/${overview.fleet.total} ready`, good: overview.fleet.ready === overview.fleet.total },
  ];
  return <>
    <WorkspaceHeader eyebrow="Deployment identity" title="Releases" description="Verify the control-plane source, database schema and node readiness attached to the currently served operator build." overview={overview} icon={PackageCheck} />
    <SummaryCards items={[
      { label: "Current release", value: overview.deployment.version, detail: overview.deployment.deployed, tone: "good" },
      { label: "Commit", value: overview.deployment.commit.slice(0, 8), detail: "release source identity", tone: overview.deployment.commit === "unavailable" ? "warn" : "good" },
      { label: "Schema", value: overview.deployment.schema, detail: "database contract", tone: overview.deployment.schema === "unavailable" ? "warn" : "good" },
      { label: "Ready nodes", value: `${overview.fleet.ready}/${overview.fleet.total}`, detail: "compatible fleet", tone: overview.fleet.ready === overview.fleet.total ? "good" : "warn" },
    ]} />
    <section className="panel workspace-panel release-workspace reveal">
      <div className="release-hero"><span><PackageCheck size={31} /></span><div><small>Serving now</small><h2>{overview.deployment.version}</h2><p>Verified against the source and schema identities reported by the running control plane.</p></div><span className="verified-chip"><Check size={12} /> Observed</span></div>
      <div className="release-checks">{checks.map((check) => <div key={check.label}><span className={check.good ? "tone-good" : "tone-warn"}>{check.good ? <Check size={16} /> : <AlertTriangle size={16} />}</span><div><strong>{check.label}</strong><small>{check.detail}</small></div></div>)}</div>
    </section>
  </>;
}

function ReportsWorkspace({ overview }: { overview: OverviewResponse }) {
  const active = overview.runtimes.filter((runtime) => runtime.status === "running").length;
  const attention = overview.runtimes.filter((runtime) => runtime.status === "attention").length;
  const exportCsv = () => {
    const cells = (value: string | number) => `"${String(value).replace(/"/g, '""')}"`;
    const rows = [["runtime_id", "name", "status", "desired_state", "connection", "host", "account_id", "snapshot", "updated"], ...overview.runtimes.map((runtime) => [runtime.id, runtime.name, runtime.status, runtime.desiredState, runtime.connection, runtime.host, runtime.accountId, runtime.snapshot, runtime.updated])];
    const blob = new Blob([rows.map((row) => row.map(cells).join(",")).join("\n")], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `virtroid-operator-${overview.generatedAt.slice(0, 10)}.csv`;
    link.click();
    URL.revokeObjectURL(url);
  };
  return <>
    <WorkspaceHeader eyebrow="Operational reporting" title="Reports" description="Create a point-in-time, sanitized operating record from the same snapshot shown throughout Observatory." overview={overview} icon={FileClock}>
      <div className="header-actions"><button type="button" onClick={exportCsv}><Download size={14} /> Export CSV</button><button type="button" onClick={() => window.print()}><FileText size={14} /> Print report</button></div>
    </WorkspaceHeader>
    <SummaryCards items={[
      { label: "System status", value: overview.status, detail: overview.headline, tone: overview.status === "healthy" ? "good" : overview.status === "degraded" ? "warn" : "bad" },
      { label: "Runtime availability", value: `${overview.runtimes.length ? Math.round(active / overview.runtimes.length * 100) : 0}%`, detail: `${active} of ${overview.runtimes.length} running`, tone: "good" },
      { label: "Exceptions", value: attention, detail: "runtimes needing attention", tone: attention ? "warn" : "good" },
      { label: "Fleet readiness", value: `${overview.fleet.ready}/${overview.fleet.total}`, detail: overview.fleet.name, tone: overview.fleet.ready === overview.fleet.total ? "good" : "warn" },
    ]} />
    <section className="panel workspace-panel report-sheet reveal">
      <div className="report-sheet__heading"><div><VirtroidMark /><span><strong>Virtroid Observatory</strong><small>Operational snapshot</small></span></div><time>{new Date(overview.generatedAt).toLocaleString()}</time></div>
      <div className="report-narrative"><h2>{overview.headline}</h2><p>{overview.subline}</p></div>
      <div className="report-grid"><section><h3>Runtime posture</h3><dl><div><dt>Observed</dt><dd>{overview.runtimes.length}</dd></div><div><dt>Running</dt><dd>{active}</dd></div><div><dt>Attention</dt><dd>{attention}</dd></div><div><dt>Live viewers</dt><dd>{overview.runtimes.filter((runtime) => runtime.activeSession).length}</dd></div></dl></section><section><h3>Control posture</h3><dl><div><dt>Environment</dt><dd>{overview.environment}</dd></div><div><dt>Release</dt><dd>{overview.deployment.version}</dd></div><div><dt>Schema</dt><dd>{overview.deployment.schema}</dd></div><div><dt>Ready nodes</dt><dd>{overview.fleet.ready}/{overview.fleet.total}</dd></div></dl></section></div>
      <section className="report-exceptions"><h3>Exceptions and checks</h3>{overview.hygiene.map((check) => <div key={check.label}><StatusDot status={check.status === "passed" ? "good" : check.status === "attention" ? "warn" : "muted"} /><span><strong>{check.label}</strong><small>{check.detail}</small></span></div>)}</section>
      <footer>Generated from sanitized operator telemetry. This report does not contain credentials, relay tokens, network addresses or raw security payloads.</footer>
    </section>
  </>;
}

function OperatorWorkspace({ section, overview, onSelectRuntime }: { section: string; overview: OverviewResponse; onSelectRuntime: (runtime: RuntimeSummary) => void }) {
  switch (section) {
    case "Runtimes": return <RuntimesWorkspace overview={overview} onSelectRuntime={onSelectRuntime} />;
    case "Sessions": return <SessionsWorkspace overview={overview} onSelectRuntime={onSelectRuntime} />;
    case "Accounts": return <AccountsWorkspace overview={overview} onSelectRuntime={onSelectRuntime} />;
    case "Fleet": return <FleetWorkspace overview={overview} />;
    case "Security": return <SecurityWorkspace overview={overview} />;
    case "Logs": return <LogsWorkspace overview={overview} onSelectRuntime={onSelectRuntime} />;
    case "Data hygiene": return <HygieneWorkspace overview={overview} />;
    case "Storage": return <StorageWorkspace overview={overview} />;
    case "Releases": return <ReleasesWorkspace overview={overview} />;
    case "Reports": return <ReportsWorkspace overview={overview} />;
    default: return null;
  }
}

function LoadingState() {
  return (
    <div className="loading-state" aria-label="Loading operator overview">
      <VirtroidMark />
      <span>Establishing operating picture…</span>
    </div>
  );
}

function LoginGatewayIllustration() {
  return (
    <div className="login-gateway" aria-hidden="true">
      <div className="login-gateway__beam" />
      <div className="login-gateway__scanner" />
      <svg viewBox="0 0 760 560" preserveAspectRatio="none" focusable="false">
        <defs>
          <linearGradient id="login-gateway-line" x1="0" y1="0" x2="1" y2="0">
            <stop offset="0" stopColor="#78951e" stopOpacity="0.14" />
            <stop offset="0.5" stopColor="#afd135" stopOpacity="0.88" />
            <stop offset="1" stopColor="#78951e" stopOpacity="0.14" />
          </linearGradient>
          <filter id="login-gateway-glow">
            <feGaussianBlur stdDeviation="5" result="blur" />
            <feMerge><feMergeNode in="blur" /><feMergeNode in="SourceGraphic" /></feMerge>
          </filter>
        </defs>
        <path className="login-gateway__line" d="M90 280 C232 280 252 72 380 72" />
        <path className="login-gateway__line login-gateway__line--reverse" d="M380 72 C508 72 528 280 670 280" />
        <path className="login-gateway__line" d="M380 72 C380 220 380 364 380 510" />
        <circle className="login-gateway__pulse" r="4" fill="#d0ec62" filter="url(#login-gateway-glow)">
          <animateMotion dur="5.6s" repeatCount="indefinite" path="M90 280 C232 280 252 72 380 72" />
        </circle>
        <circle className="login-gateway__pulse" r="4" fill="#d0ec62" filter="url(#login-gateway-glow)">
          <animateMotion begin="1.7s" dur="5.6s" repeatCount="indefinite" path="M380 72 C508 72 528 280 670 280" />
        </circle>
        <circle className="login-gateway__pulse" r="4" fill="#d0ec62" filter="url(#login-gateway-glow)">
          <animateMotion begin="3.1s" dur="5.6s" repeatCount="indefinite" path="M380 72 C380 220 380 364 380 510" />
        </circle>
      </svg>

      <div className="login-gateway__node login-gateway__node--operator">
        <span><Fingerprint size={22} /></span>
        <strong>Operator identity</strong>
        <small>Challenge ready</small>
      </div>
      <div className="login-gateway__node login-gateway__node--gate">
        <span><ShieldCheck size={25} /></span>
        <strong>Secure gateway</strong>
        <small>Verifying access</small>
      </div>
      <div className="login-gateway__node login-gateway__node--plane">
        <span><Server size={22} /></span>
        <strong>Control plane</strong>
        <small>Protected</small>
      </div>
      <div className="login-gateway__node login-gateway__node--session">
        <span><KeyRound size={22} /></span>
        <strong>Session scope</strong>
        <small>Read only</small>
      </div>
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
      <LoginGatewayIllustration />
      <section className="login-card">
        <div className="login-card__brand"><VirtroidMark /><span><strong>Virtroid</strong><small>Operator control plane</small></span></div>
        <span className="hero-kicker"><ShieldCheck size={13} /> Restricted operations</span>
        <h1>Observatory</h1>
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
        overview={result.data}
        onLogout={async () => {
          await deleteSession();
          if (isLiveOperatorMode()) {
            setResult(null);
            setAuthenticated(false);
          }
        }}
      />
      <div className="workspace">
        <Topbar onOpenSearch={() => setPaletteOpen(true)} onOpenNav={() => setMobileNavOpen(true)} onOpenNotifications={() => navigate("Security")} />
        <main className="content">
          {activeSection === "Command centre" ? (
            <CommandCentre overview={result.data} source={result.source} onSelectRuntime={setSelectedRuntime} onNavigate={navigate} />
          ) : (
            <OperatorWorkspace section={activeSection} overview={result.data} onSelectRuntime={setSelectedRuntime} />
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
