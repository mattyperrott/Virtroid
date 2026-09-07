# Virtroid Operator Console

Read-only first vertical slice of Virtroid's operator control plane. It includes
the navigation shell, Command Centre, searchable resources, and runtime
inspector. The local development server deliberately uses visibly labelled
fixture data; it does not connect directly to PostgreSQL, Docker, or a node.

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

The first deployed slice is intentionally read-only. It excludes relay
credentials, blob manifests, network addresses, raw security-event output, and
internal error details. Privileged moderation and sanitation actions require a
later RBAC, step-up approval, CSRF, and immutable-audit slice.
