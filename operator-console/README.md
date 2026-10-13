# Virtroid Operator Console

Read-only Observatory interface for Virtroid's operator control plane. It
includes the Command Centre plus dedicated runtime, session, account, payments,
fleet, runtime VM, security, log, data-hygiene, storage, release, and reporting
workspaces. The
console supports resource filtering, runtime inspection, global search,
responsive navigation, printable reports, and sanitized CSV export.

The local development server deliberately uses visibly labelled fixture data;
it does not connect directly to PostgreSQL, Docker, or a node. Production uses
the same-origin operator API and fails closed if live telemetry is unavailable.

## Run locally

```bash
npm install
npm run dev
```

Open `http://127.0.0.1:4173`.

## Read-only API contract

The production bundle is served below `/operator/` and automatically uses the
same-origin API. For live development against another origin, set
`VITE_OPERATOR_API_BASE`.

```text
POST   /operator/v1/session
GET    /operator/v1/session
DELETE /operator/v1/session
GET    /operator/v1/overview
```

The response shape is defined in `src/types.ts`. Authentication is held in a
short-lived HttpOnly, Secure, SameSite=Strict cookie. An unavailable live API
fails closed and never silently falls back to fixture data.

The Command Centre also shows the platform services: whether the payments
service and the VM provisioner have checked in during the last two minutes,
and the Sia indexer's sync, wallet and contracts, which virtroidd reads from
indexd's admin API in the background. Payments shows deposits (linked to
Tronscan), balances and usage charges; Runtime VMs shows the Aleph VMs with an
estimated cost from `RUNTIME_VM_COST_PER_HOUR_MICRO`.

The first deployed slice is intentionally read-only. It excludes relay
credentials, blob manifests, network addresses, raw security-event output, and
internal error details. Privileged moderation and sanitation actions require a
later RBAC, step-up approval, CSRF, and immutable-audit slice.
