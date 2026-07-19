# Virtroid remediation status — 2026-07-19

This document separates verified production observations from implementation in
the current working branch. Code, configuration, database state, reproducible
tests, and recorded runtime checks are the evidence sources; a route, schema,
workflow file, or passing build alone is not proof of a working end-to-end
feature. This is a remediation ledger, not an independent security
certification.

## Release decision

Virtroid is suitable for development and tightly controlled trusted-operator
testing with disposable data. It is **not ready for public beta, hostile
multi-tenancy, production use with irreplaceable data, or high-threat/OPSEC
use**.

Do not describe the current system as end-to-end encrypted or confidential.
Snapshot encryption protects stored blobs, and the viewer has an inner
encrypted channel, but the selected runtime node receives usable material and
controls the live execution environment.

## Status boundary

| State | Evidence | Meaning |
| --- | --- | --- |
| Live production snapshot | Read-only inventory and deployment checks on `virtroid-cp`, 2026-07-19 | The deployed database was schema `2026071902`. This is a dated observation, not a claim about the current branch. |
| Working branch | `codex/secure-release-deployment`, based on `1d31688eda5d705d50537a6d95ff8244ea5a93da` | Node/operator registry, schema `2026071903`, immutable direct-release controls, and Restic automation are implemented here but are not yet deployed. |
| Target architecture | `docs/final-product-specification.md` and `docs/runtime-poc/` | CVM isolation, runtime attestation, lease-scoped key release, and related claims remain design targets, not implemented controls. |

Every branch claim below must be revalidated against the exact clean local
commit and exact built image selected for deployment.

## Dated live snapshot: schema `2026071902`

The hardened schema-v2 stack was deployed on 2026-07-19 after a migration
rehearsal against a disposable PostgreSQL clone. The deployment pass recorded:

- two accounts, two devices, and two runtimes preserved after migration;
- healthy `virtroidd`, `virtnoded`, PostgreSQL, and edge services;
- successful node heartbeat, local control-plane health, and public HTTPS
  health;
- a TLS SPKI matching the Android pin;
- public bootstrap and account-storage mutation failing closed;
- container CPU, memory, and PID limits, separated Docker networks, and no
  unexpected public listeners;
- key-only `virtroid` administration, disabled direct root SSH and password
  authentication, Fail2ban, bounded container logs, unattended security
  upgrades, Certbot renewal, and the local backup timer;
- schema version `2026071902`, not the branch's `2026071903` registry schema.

The deployment pass created a root-only rollback bundle at
`/var/backups/virtroid/20260719T083246Z` and a separately checked daily set at
`/var/backups/virtroid/daily-20260719T094142Z`. A later read-only inventory on
the same date found three local backup sets and verified the newest set's
checksums. These are same-host rollback copies, not off-host disaster recovery.

The same read-only inventory found no configured remote Restic repository or
evidence of an independent restore drill. It also found no schema-v3
node/operator registry in production. Those are rollout prerequisites, not
completed production controls.

The live node agent remains root-equivalent through Docker control and creates
privileged shared-kernel ReDroid guests. The schema-v2 production snapshot must
therefore be treated as a trusted-operator, single-host deployment.

## Controls already verified in the schema-v2 remediation pass

The earlier remediation pass and its recorded checks substantially improved the
development and trusted-operator baseline:

- signed device requests use method, path, body hash, timestamp, nonce, and
  replay protection;
- session/runtime/envelope/capability bindings, ownership checks, lifecycle row
  locking, stale-observation rejection, cleanup acknowledgement, and deletion
  ownership were hardened;
- the control plane stores durable short-lived handoffs as node-encrypted
  envelopes rather than raw blob keys;
- snapshot creation/restoration added size, count, path, chunk, manifest,
  symlink, hard-link, special-file, sparse-file, and cross-runtime binding
  defenses;
- production runtime images require immutable digests and configured resource
  limits; app installation requires pinned artifacts and package verification;
- public bootstrap, catalog synchronization without an independently supplied
  index digest, and unsafe password-verifier replacement fail closed;
- Android release signing fails closed, v1 signing is disabled, sensitive local
  material is cleared more aggressively, and release manifest/privacy gates
  are enforced;
- SSH, TLS renewal, Compose network separation, bounded logs, local backups,
  and release configuration received operational hardening.

Recorded verification for that pass included backend unit/race/vet/build,
PostgreSQL integration, Android unit/lint/debug-build/release-manifest checks,
deployment configuration tests, SSH hardening checks, a production local
backup inspection, a staging certificate renewal, and an offline vulnerability
scan. Those results apply to the code and deployed state tested at that time;
they do not automatically validate the newer branch.

## Implemented on the working branch, not yet released

### Node and operator trust registry

- Schema `2026071903` adds approved operators, approved nodes, versioned node
  keys, and append-only registry audit records.
- Production shared-secret self-enrollment is disabled. Signed node requests
  must verify against an active operator-approved key; the development-only
  enrollment path is explicitly gated.
- The administrative command supports operator approval, revocation, and
  reactivation. Operator revocation removes its nodes and keys from subsequent
  authorization queries.
- The migration/deploy gate binds the expected production node fingerprint and
  requires a fresh heartbeat from the approved node/key.

This is an authentication and operator-governance improvement. It is **not
mTLS**, it does not isolate the node from the control plane, and it does not
make a malicious or compromised node confidential. The remaining
`NODE_SHARED_SECRET` still protects control-plane-to-node callbacks.

### Immutable release and deployment path

- The trusted operator workstation builds one `linux/amd64` image from an exact
  clean local commit and creates a private checksummed release bundle.
- The bundle binds the local Docker image tag, exact config ID and manifest
  digest, source commit, schema version, and digest of the complete reviewed
  deployment tree.
- The human `virtroid` administrator transfers the bundle directly over the
  pinned key-only SSH connection. The source remote has no production
  credential, registry role, or deployment authority.
- A preinstalled root helper snapshots the unprivileged staging files once,
  verifies checksums, architecture, image labels/ID, and deployment-tree
  identity, coordinates the maintenance/backup lock, approves only the
  independently recorded node key, and opens ingress last.
- Manual mutating deployment commands are root-only, accept only the installed
  root-owned tree, and share the same lock, so they cannot race a backup.

These controls provide local build/deployment integrity. They are not runtime
attestation, a confidential-VM measurement, or proof that the VPS is executing
without operator access.

The direct release helper has not yet crossed the schema-v3 migration/recovery
boundary on production.

### Backup and recovery automation

- Local backup logic now drains ingress, refuses active sessions or managed
  guests, stops writers, records release/deployment state, captures PostgreSQL
  and `/srv`, includes renterd data when configured, verifies portable
  checksums, and coordinates through maintenance locks.
- Restic wrappers and systemd units validate root-owned literal credentials,
  reject local and explicit plaintext endpoints, upload only fresh verified
  local sets, check repository data, and perform a scheduled materialized
  restore/checksum/archive inspection plus a full `pg_restore` stream.
- Deletion/pruning remains disabled by default so append-only or object-locked
  storage can be used.

This is implementation, not disaster-recovery evidence. No independent remote
repository credentials have been provisioned, no real off-host upload has been
recorded, and no clean-host restore has been completed. A restore check on the
source VPS also does not prove recovery from loss or compromise of that VPS.

### Current branch verification evidence

The latest recorded branch checks include:

- complete backend unit tests, race detector, `go vet`, command builds, and a
  disposable PostgreSQL registry/lifecycle integration run;
- Android debug unit tests, debug and release lint, debug assembly, and release
  security-manifest verification;
- production-parity `linux/amd64` image construction and inspection before the
  final backend changes.

The final release image, all deployment/off-site shell suites, vulnerability
scan, secret scan, direct-transfer checks, and production deployment must be
rerun against the eventual immutable local commit. A previous passing result
does not carry across subsequent code changes.

## Promotion gates still required

Before the schema-v3 branch can be promoted even to the existing
trusted-operator VPS:

1. Finish review of the release, installer, rollback, certificate, backup, and
   restore paths; rerun every shell/configuration test after the last change.
2. Rerun backend, PostgreSQL, Android, final-image, vulnerability, and secret
   checks against the exact clean local commit selected for release.
3. Build the private local release bundle, verify its checksums and exact image
   ID, and independently verify the pinned SSH host key and node fingerprint.
4. Exercise the direct SSH transfer, root-owned tree installer, maintenance
   lock, fail-closed cutover, and human recovery path on a disposable target.
5. Provision an independent encrypted Restic repository with separately
   escrowed credentials; complete an actual upload and clean-host restore,
   including PostgreSQL startup and application-level data checks.
6. Take and verify the final pre-migration backup before applying schema
   `2026071903`; preserve the schema-v2 recovery path because the old image is
   not a valid automatic rollback after this migration.
7. Verify post-deploy schema, registry records, approved fingerprint and fresh
   heartbeat, exact image digest/labels, service identity, public health, and
   backup/restore evidence.

Until all seven steps have recorded evidence, schema-v3 branch work must not be
described as deployed or production-proven.

## Unresolved production and architecture blockers

The following are not fixed by the schema-v3 release work:

- **Isolation:** replace privileged shared-kernel ReDroid containers and
  Docker-socket node authority with VM/CVM-grade tenant isolation. Add enforced
  disk quotas and deny-by-default metadata/private-network egress without
  blocking intended internet access.
- **Runtime attestation and key release:** implement measured runtime images,
  a verifier, client-approved policy, replay-resistant leases, and
  lease-scoped release of runtime/viewer/snapshot material. The trusted node
  operator can still observe live plaintext and usable keys.
- **Account key architecture:** replace device-derived snapshot-key custody
  with a versioned account DEK wrapped to authorized devices, plus tested
  recovery, rotation, and transactional rewrap flows.
- **Mutual service authentication:** replace the remaining shared-secret
  control-plane-to-node callback channel with mTLS or an equivalent
  mutually authenticated, rotatable, revocable transport. The registry alone
  does not provide channel authentication in both directions.
- **Snapshot authenticity and anti-rollback:** authenticate manifest
  generation/key identity and expose client-verifiable rollback state.
- **App supply chain:** validate F-Droid's signed metadata root and APK signer
  lineage. Operator-pinned index/artifact hashes and package-name checks are
  containment, not full publisher identity.
- **Abuse and resource enforcement:** add cumulative trial-time accounting,
  real storage-byte and filesystem quota enforcement, durable invite/billing
  controls, and production scheduler policy. Public bootstrap must remain off.
- **Recovery evidence:** provision independent off-host credentials, complete
  clean-host restore drills, and add durable external monitoring/alert delivery.
- **Operational assurance:** obtain real renterd staging/canary evidence,
  Android instrumentation and connected-device evidence, full
  lifecycle/viewer/storage fault tests, multi-host/multi-replica staging, and an
  independent penetration/privacy review.

No public claim of confidential execution, end-to-end encryption, anonymity,
unlinkability, forensic erasure, or resistance to a malicious infrastructure
operator is justified until the relevant architecture and independent evidence
exist.
