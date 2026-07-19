# Virtroid

Virtroid is an experimental system for starting disposable Android runtimes,
streaming them to an Android client, and preserving selected encrypted state
between sessions. The repository contains the Android client, Go control plane
and node agent, and the hardened single-host deployment configuration.

## Release status

Virtroid is suitable for development and tightly controlled trusted-operator
testing with disposable data. It is **not ready for public beta, hostile
multi-tenancy, high-threat use, or claims of end-to-end/confidential execution**.

The verified VPS deployment on 2026-07-19 runs database schema `2026071903`,
the operator-approved node registry, and an immutable release/deploy path.
Production source, image builds, release bundles, secrets, rollback state, and
backups are isolated on the VPS. Same-host backup retention is an explicit
availability limitation: total VPS or provider loss is not recoverable from
those copies.

The current node still controls the container runtime and can observe live
plaintext. Privileged shared-kernel ReDroid guests must be replaced with
VM/CVM-grade isolation before untrusted tenants are accepted. The full verified
status and remaining blockers are recorded in
[`docs/remediation-status-2026-07-19.md`](docs/remediation-status-2026-07-19.md).

## Repository layout

- `android-client/` — Android application and security tests.
- `backend/` — control plane, runtime node, snapshot storage, and Go tests.
- `deploy/vps/` — production-oriented Compose, host preparation, deployment,
  backup, TLS, and safety checks.
- `docs/` — target specification, current status, and runtime research notes.

## Verification

Developer checks can run from a Mac checkout. The source remote is passive
safekeeping and has no production credentials, registry role, or deployment
authority. Developer entry points include:

```bash
cd backend && go test -count=1 ./...
cd android-client && ./gradlew --no-daemon :app:testDebugUnitTest :app:lintDebug
bash deploy/vps/test-env-safety.sh
bash deploy/vps/test-compose-config.sh
```

Production releases are built and applied only on the VPS from its root-owned,
offline source checkout with no Git remote or `.github` directory.

Production setup and recovery procedures are documented in
[`deploy/vps/README.md`](deploy/vps/README.md).

## Security

Do not place credentials, signing keys, generated APKs, compiled binaries, or
deployment environment files in source control. Follow the private reporting
guidance in [`SECURITY.md`](SECURITY.md).
