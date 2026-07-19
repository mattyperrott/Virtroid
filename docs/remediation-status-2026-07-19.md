# Virtroid remediation status — 2026-07-19

This is the current implementation-status record for the remediation pass that
followed the 2026-07-19 comprehensive audit. Code and reproducible tests remain
the source of truth.

## Release decision

Virtroid is suitable for development and tightly controlled trusted-operator
testing with disposable data. It is **not ready for public beta, production, a
multi-tenant hostile workload, or high-threat/OPSEC use**.

Production deployment defaults deliberately fail closed:

- public account bootstrap is disabled;
- F-Droid catalog synchronization is disabled unless an independently verified
  index SHA-256 is configured;
- mutable container image references are rejected;
- the control plane accepts node callback destinations only from an explicit
  allowlist;
- Android release artifacts cannot be assembled without the complete release
  signing configuration.

## Implemented in this pass

### Android

- Removed the false ephemeral-runtime selection and now describes the actual
  persistent encrypted-snapshot behavior.
- Removed inert metadata and post-transfer cleanup controls.
- Removed unsafe password rotation until snapshots can be transactionally
  rewrapped.
- Enforces a Unicode-aware 14–256 character identity passphrase policy.
- Clears cached blob-key material on background lock/relock.
- Tracks and removes every runtime-capability Keystore alias during account
  reset.
- Fails release builds closed without the protected signer; v2/v3 signing is
  enabled and v1 signing is disabled.
- Bounds catalog responses and hardens catalog icons with canonical origin,
  redirect, MIME, byte, decode, pixel, concurrency, and LRU controls.

### Control plane and database

- Validates session/runtime/envelope/capability bindings before mutation.
- Enforces one live session per runtime and makes live reservations immutable
  under retries; relay-token refresh remains an explicit authenticated route.
- Serializes runtime transitions with row locks, makes repeated start/stop
  requests idempotent, and rejects stale node observations with a monotonic
  operation generation.
- Preserves cleanup ownership and retry state with explicit
  `cleanup_pending` acknowledgment.
- Routes deletion through `host_id` or `blob_host_id`, retains tombstones until
  cleanup acknowledgment, and lets an eligible node reclaim orphaned remote
  object-store cleanup work. Unknown local-disk ownership remains fail-closed.
- Stores short-lived blob-key handoffs durably in PostgreSQL as only the
  node-encrypted envelope, verifier, binding, and expiry. Raw blob keys are not
  stored by the control plane.
- Prevents node heartbeat data from becoming an account payment destination;
  user-funded storage UI and mutation paths are disabled.
- Disables every previously enabled F-Droid row when a pinned authoritative
  index contains no compatible apps, rather than serving stale catalog state.
- Disables password-verifier replacement that would orphan existing snapshots.
- Caps API request bodies, catalog rows, log fields, and retained runtime log
  rows; internal 5xx responses no longer expose backend errors.
- Adds transaction-scoped advisory locking, a schema-version ledger, rejection
  of newer unknown schema versions, and a real PostgreSQL integration test.
- Production account bootstrap is disabled until a durable invite or billing
  gate exists.
- Node callback targets are syntactically validated and matched against a
  production allowlist before heartbeat persistence and every callback.

### Runtime node and snapshots

- Preflights key/storage, quiesces the guest, commits a snapshot, removes the
  container, removes plaintext and stale generations, then acknowledges final
  cleanup. Failures retain retry ownership.
- Keeps an already fetched key in a zeroed, memory-only per-runtime node cache
  so stale-session cleanup can finish after the control-plane handoff expires.
- Deletes both in-memory and durable encrypted key handoffs after successful
  cleanup/deletion acknowledgment and on account/device revocation paths.
- Streams renterd restores with strict size/hash limits, cleans partial uploads,
  detects list/delete API support, and prunes old generations.
- Writes local chunks and snapshots with file/directory sync plus atomic rename.
- Bounds source size, file count, path length, chunk count, manifest bytes, and
  restored output; rejects symlinks, hard links, special files, traversal, and
  oversized sparse inputs.
- Uses runtime-bound snapshot-key derivation and a versioned runtime identity in
  new manifests; legacy manifests remain readable. Cross-runtime ciphertext
  transplant fails authentication.
- Requires immutable runtime-image digests in production, enforces CPU, RAM,
  PID and shared-memory limits, verifies the expected Android package after
  installation, and discards a tainted runtime on mismatch.
- Bounds APKM expansion by file count, per-file bytes, and total bytes; rejects
  duplicate flattened names and special entries and cleans partial extraction.
- Uses a dedicated labeled Docker network per runtime, reconnects only the node
  agent, rejects shared-network production configuration, and safely removes
  only matching managed networks.

### Delivery and operations

- Pins the Gradle distribution checksum and CI actions.
- Removes stale tracked APK/signature/Go binary artifacts and rejects future
  committed distribution outputs in CI; release files must be produced by the
  protected release pipeline.
- Adds backend unit, race, vet, build, vulnerability, Android unit/lint/build,
  release-manifest, shell-safety, and Compose-validation CI gates.
- Adds Dependabot coverage for Go, Gradle, Actions, Dockerfile, and Compose.
- Requires immutable digests for deployment images and validates profile/storage
  combinations before deployment.
- Separates database, control, blob, monitoring, and edge networks; adds service
  resource limits and hardened HAProxy defaults.
- Uses Docker's bounded `local` log driver for every deployed service and for
  dynamically created runtime guests.
- Restricts the combined HAProxy certificate/private-key bundle to a dedicated
  supplementary group and installs an atomic Certbot deploy hook that validates
  the certificate/key pair, HAProxy configuration, restart, and HTTPS health.
- Enables Fail2ban for SSH and applies shorter login grace and authentication
  attempt limits; key-only access was enabled only after two fresh-login tests.
- Installs a persistent daily timer that creates root-only, checksum-verified
  PostgreSQL and `/srv/virtroid` rollback backups and retains seven daily sets.
- Uses a password-locked, key-only `virtroid` host account for deployment and
  administration; direct root SSH and all SSH password authentication are
  disabled. Docker and passwordless sudo access remain explicitly
  root-equivalent administrative capabilities, not a sandbox boundary.

## Verification completed

- Backend unit suite: passed.
- Backend race-detector suite: passed.
- `go vet ./...`: passed.
- All backend commands build: passed.
- PostgreSQL 18 integration test, including four concurrent schema initializers,
  lifecycle generations, stale-report rejection, encrypted handoff recovery,
  cleanup and deletion: passed.
- Android debug unit tests, debug lint, debug APK, and release security-manifest
  gate: passed.
- Android release lint: passed with zero errors.
- Release assembly correctly fails when protected signing credentials are absent.
- Deployment shell syntax, environment abuse tests, and all-profile Compose
  rendering: passed.
- Fresh password authentication after SSH reload, Fail2ban's `sshd` jail, and
  effective SSH hardening values: passed.
- Fresh `virtroid` key login before and after the key-only SSH reload, non-root
  Compose deployment, passwordless sudo, and expected rejection of a fresh root
  password session: passed.
- Scheduled backup unit validation and a manual production backup with readable
  PostgreSQL dump, readable `/srv` archive, portable checksums, root-only modes,
  and no partial directory: passed.
- Let’s Encrypt staging renewal, including the production deploy hook: passed.
- Offline Trivy scan found zero HIGH/CRITICAL dependency, secret, or Dockerfile
  findings with the locally available 2026-07-18 database.

## Remaining release blockers

The following are architecture or external-evidence work, not claims fixed by
this pass:

- Replace privileged, shared-kernel ReDroid containers with VM/CVM-grade tenant
  isolation; add enforced disk quota and deny-by-default metadata/private-network
  egress without breaking intended internet access.
- Implement attestation, measured images, client-approved policy, and
  lease-scoped key release. The node/operator can still see live plaintext and
  usable keys in the current trusted-operator design.
- Replace device-derived snapshot keys with a versioned account DEK wrapped to
  authorized devices and a tested recovery/rewrap flow.
- Verify the native F-Droid signed metadata root and APK signer lineage. The
  current operator-supplied index digest, origin restrictions, artifact hash,
  package check, downgrade guard, and default-disabled sync are containment,
  not a full publisher-identity chain.
- Add cryptographically authenticated snapshot-manifest generation/key IDs and
  a client-visible anti-rollback mechanism.
- Enforce cumulative trial runtime seconds, storage-byte entitlement and a real
  filesystem disk quota; public bootstrap must remain disabled until durable
  invite/billing/abuse controls exist.
- Replace node shared-secret TOFU with an approved operator registry, mTLS,
  rotation/revocation, and compromise recovery.
- Build a protected positive-path signed Android release with provenance/SBOM
  and verify install/upgrade against the official signer.
- Add durable monitoring delivery, real renterd staging/canary evidence,
  Android instrumentation, full lifecycle/viewer/storage fault tests, and a
  true multi-host/multi-replica staging exercise.
- Add encrypted off-host backups with independent restore drills. The current
  automated copies are intentionally local rollback protection only.
- Complete the confidential-runtime POC and an independent penetration/privacy
  review before making E2E, confidential, anonymity, unlinkability, or high-
  threat claims.

## Deployment state

The hardened stack was deployed to `virtroid-cp` on 2026-07-19 after a
production-data migration rehearsal succeeded against a disposable PostgreSQL
clone. A root-only, checksum-verified rollback bundle is stored at
`/var/backups/virtroid/20260719T083246Z` on the server. A separately verified
scheduled-backup set is stored at
`/var/backups/virtroid/daily-20260719T094142Z`; the persistent daily timer keeps
seven successful sets.

Live verification confirmed schema version `2026071902`, preservation of two
accounts, two devices and two runtimes, successful node heartbeats, local and
public HTTPS health, a TLS SPKI that matches the Android pin, fail-closed public
bootstrap and account-storage mutation, container CPU/RAM/PID limits, separated
Docker networks, and no unexpected public listeners. Catalog sync remains
disabled and no placeholder image digest was used. Fail2ban, bounded Docker
logging, unattended security upgrades, the Certbot renewal timer, and the
Virtroid backup timer are enabled. Deployment now runs through the key-only
`virtroid` host account; `virtroidd` runs unprivileged inside its container,
while `virtnoded` remains root-equivalent because the current ReDroid design
requires Docker-socket control.

This successful deployment does not change the release decision above: the
remaining architecture and assurance blockers must be completed before public
beta, hostile multi-tenancy, or high-threat use.
