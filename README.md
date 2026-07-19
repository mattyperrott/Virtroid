# Virtroid

Virtroid is an experimental system for starting disposable Android runtimes,
streaming them to an Android client, and preserving selected encrypted state
between sessions. The repository contains the Android client, Go control plane
and node agent, and the hardened single-host deployment configuration.

## Release status

Virtroid is suitable for development and tightly controlled trusted-operator
testing with disposable data. It is **not ready for public beta, hostile
multi-tenancy, high-threat use, or claims of end-to-end/confidential execution**.

The last verified VPS snapshot on 2026-07-19 ran database schema `2026071902`.
The current `codex/secure-release-deployment` working branch adds schema
`2026071903`, an operator-approved node registry, an immutable release/deploy
path, and encrypted off-site backup automation, but that work is not yet
deployed. Real remote backup credentials and a clean-host restore drill are
still required before promotion.

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

Release verification is run from the trusted local checkout. The source remote
is passive safekeeping and has no production credentials, registry role, or
deployment authority. Local entry points include:

```bash
cd backend && go test -count=1 ./...
cd android-client && ./gradlew --no-daemon :app:testDebugUnitTest :app:lintDebug
bash deploy/vps/test-env-safety.sh
bash deploy/vps/test-compose-config.sh
bash deploy/vps/test-offsite-backup.sh
```

Production setup and recovery procedures are documented in
[`deploy/vps/README.md`](deploy/vps/README.md).

## Security

Do not place credentials, signing keys, generated APKs, compiled binaries, or
deployment environment files in source control. Follow the private reporting
guidance in [`SECURITY.md`](SECURITY.md).
