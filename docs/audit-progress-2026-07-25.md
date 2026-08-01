# Virtroid Audit and Remediation Progress

Assessment date: 2026-07-25

This is the current audit progress ledger. `Audit.md` and
`android-security-privacy-audit-2026-05-09.md` are retained as historical
evidence and should not be quoted as the current system state.

## Decision

Current release posture: **development and tightly controlled trusted-operator
testing**.

Not approved for claims of:

- trustless operation;
- end-to-end encrypted runtime persistence;
- confidential or anonymous runtime execution;
- resistance to a malicious VPS/runtime-node operator;
- independent/distributed durable storage.

## Evidence refreshed in this pass

- live public DNS and HTTPS health;
- live VPS services, containers, disk, and immutable release state;
- aggregate database lifecycle/orphan checks;
- public F-Droid catalog content and APK hash field length;
- backend tests, vet, command builds, and dependency vulnerability scan;
- Android unit tests, lint, debug build, and release-manifest security gate;
- real emulator log filtering, clearing, re-entry, and process restart;
- deployment script syntax, environment-safety, and Compose checks;
- current code paths for camera mode, relay/viewer, ReDroid creation, and guest
  device exposure.

## Historical finding progress

| Historical finding | Current status | Current evidence and residue |
| --- | --- | --- |
| SR-01: cleartext transport | Substantially remediated externally | Release client requires HTTPS, public edge exposes 443, and control/node host ports are loopback. Same-host internal traffic and the host itself remain trusted. |
| SR-02: ID-based public authorization | Substantially remediated | Main account/device routes use signed device requests; runtime operations can use signed scoped capabilities. Compatibility/public route review remains part of assurance. |
| SR-03: device public key was cosmetic | Remediated for request proof-of-possession | Device keys sign canonical method/path/context/timestamp/nonce/body-hash inputs; replay tables exist. Hardware-backed attestation is still absent. |
| SR-04: legacy route bypasses | Improved, residual review required | Current client uses signed paths. A route-by-route negative authorization matrix should remain in CI. |
| SR-05: arbitrary runtime images | Remediated in current code | Runtime images are normalized through an allowlist. Production should ultimately pin approved digests and measurements. |
| SR-06: backend/node key custody | Partially remediated | Raw key storage columns were removed and node-encrypted short-lived envelopes are used. The selected node can still decrypt active state; this is not E2E encryption. |
| SR-07: replayable relay tokens | Substantially improved | Hashing, expiry, close/heartbeat, consumption, and runtime-scoped capability controls exist. Full adversarial relay replay testing remains. |
| SR-08: weak Android local security | Substantially improved | Keystore-bound vault, PIN/passphrase retry controls, biometric unlock, secure windows, encrypted local stores, and release gates exist. Device compromise remains out of scope. |
| SR-09: UI claims exceeded implementation | Improved, still open | Unavailable file/camera actions are hidden or disabled and security language is more constrained. Camera passthrough remains unimplemented. |
| SR-10: public viewer ports | Remediated in current deployment | Only the edge exposes 443; control plane and node host mappings are loopback. |
| SR-11: repository/deployment secrets and signing | Substantially improved | Release secrets/signing material are external and ignored; VPS release state is root-owned and reproducible. Ongoing secret scanning and rotation evidence should remain release work. |
| SR-12: no automated coverage | Remediated as a baseline | Backend, race, Android, lint, release-manifest, vulnerability, and deployment checks exist in CI. Fault-injection and emulator lifecycle coverage are not complete. |

## Defects fixed in this pass

### Android application logs

Root causes:

- the top notification icon passed `errorsOnly=true`;
- “clear” stored only an in-memory screen timestamp;
- reopening the activity reset that timestamp and resurrected persisted logs;
- the unread badge counted the unchanged persistent store;
- opening the log screen wrote two new self-referential log entries.

Remediation:

- every log entry point now opens the All filter;
- Clear deletes the complete encrypted/legacy/vault-backed app log store;
- clearing emits an empty state, which resets the unread error/critical count;
- the screen no longer logs its own opening;
- dead acknowledge/viewer-clear code and strings were removed;
- regression tests cover All/Errors/Warn projections, bounded append, unread
  counts, and clear-to-zero behavior.

Dynamic emulator result:

- All displayed mixed-severity history on first open;
- Clear displayed the empty state;
- leaving and reopening remained empty;
- after a force-stop, only the newly generated `App startup` event appeared.

### Repository cleanup

Removed:

- 47 standalone image/drawable resources proven unreachable by Android Lint;
- 13 unused style definitions;
- obsolete log-viewer code and strings;
- the superseded dated remediation report, replaced by this ledger.

Retained deliberately:

- release signing/configuration files outside Git tracking;
- build inputs and Gradle wrapper files;
- historical audits/specifications, because they remain provenance and target
  context;
- current Android build artifacts for continued development.

Ignored Finder metadata, stale backend executables, and empty distribution
output were cleaned after verification. Current Android build output and Gradle
caches were retained for development and are ignored by Git.

## Open audit blockers

### Critical trust boundary

- `virtnoded` manages privileged ReDroid containers and Docker;
- the current runtime node is on the same VPS as the control plane and storage;
- the runtime node controls live plaintext;
- there is no confidential VM or remote attestation.

### Persistence and key custody

- local-disk encrypted snapshots are authenticated but remain on the VPS;
- the node can use the active envelope/key material;
- there is no client-verifiable, lease-scoped release from an attested runtime;
- renterd/Sia is deliberately inactive.

### Reliability evidence

- current live relational orphan counts are zero;
- the full restart/disk-pressure/corruption/fork/cleanup fault matrix remains to
  be executed against disposable runtimes;
- filesystem, Docker-network, capability, and blob-namespace reconciliation
  must be recorded after each failure.

### Camera passthrough

The current `camera_mode` column and disabled UI do not constitute an
implementation. A true solution needs a client capture pipeline, authenticated
upstream media channel, per-runtime V4L2 device, proven ReDroid camera HAL, and
strict lifecycle cleanup. The product switch must remain disabled until those
parts pass end-to-end isolation and privacy tests.

## Release delta

Deployed VPS source:

`ee7ee673c7f7916a53cd07a7728649adf3eddf88`

Local committed source before this working pass:

`df0ae36` — updates `golang.org/x/text` to `v0.39.0`.

Current working tree additionally contains the verified Android log fix,
dead-resource cleanup, and updated documentation. None of these newer local
changes should be described as deployed until a reviewed VPS release and APK
installation are completed.

## Required next audit evidence

1. Review and commit the maintenance delta.
2. Release the backend dependency update to the VPS and verify immutable image
   labels, schema state, health, and logs.
3. Run the lifecycle/fault matrix in
   `current-status-and-roadmap.md`.
4. Record filesystem, container, network, session, snapshot, and database
   reconciliation after every case.
5. Run the isolated camera feasibility milestone without exposing its UI/API in
   production.
6. Begin confidential-runtime and attested key-release work only after the
   current runtime reliability evidence is complete.
