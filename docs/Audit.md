> **Historical report:** This audit records an earlier repository state. Its
> findings and line references are not the current implementation status. Use
> [`remediation-status-2026-07-19.md`](./remediation-status-2026-07-19.md) for
> the dated live schema-v2 snapshot, unreleased branch work, and unresolved
> production blockers. The remaining isolation, runtime-attestation, key-release,
> account-DEK, mTLS, and off-host recovery gaps are still material.

**Overall**
Current risk rating: **Critical**.  
Current maturity: **alpha / pre-production**.  
What exists today is a usable single-VPS remote Android runtime system. What does **not** exist yet is the spec’s promised model of authenticated user access, trusted-device proof, distributed distrustful storage, and end-to-end encrypted persistence.

Audit basis:
- Static review of backend, Android client, deploy assets, and project specs
- Build verification:
  - `backend`: `go test ./...` passed, but there are no Go tests
  - `android-client`: `./gradlew :app:assembleDebug` passed
- No automated tests found in backend or Android

Assumption:
- This report is based on the repo and deployment assets only. If you have TLS, firewalling, or access control outside this repo, that may partially reduce transport exposure, but it does not fix the core auth, key-custody, and node-privilege issues below.

## **Formal Security Report**

### **Severity Table**

| ID | Severity | Finding | Primary Owner | Evidence |
|---|---|---|---|---|
| SR-01 | Critical | Public auth/authz is mostly identifier-based (`account_id`, `device_id`) rather than real authenticated user/device sessions | Backend | `backend/internal/httpapi/api.go:32-52,159-208,340-375,471-560,594-626`; `backend/internal/store/store.go:578-635,1284-1326` |
| SR-02 | Critical | Cleartext transport is enabled for API, relay, node-control traffic, and default app base URLs | Platform, Backend, Android | `android-client/app/src/main/AndroidManifest.xml:6-13`; `android-client/app/src/main/java/io/virtroid/client/data/SessionStore.kt:10-12,40`; `backend/internal/config/config.go:34-40,64-79`; `backend/internal/httpapi/api.go:830-895`; `deploy/vps/docker-compose.yml:28-31,45-51` |
| SR-03 | Critical | `virtnoded` is effectively host-root infrastructure: root user, Docker socket, `/dev` mount, privileged runtime containers | Platform | `deploy/vps/docker-compose.yml:39-40,58-61`; `backend/cmd/virtnoded/main.go:945-967`; `backend/Dockerfile:21-26` |
| SR-04 | Critical | User-controlled `android_image` can be pulled and run as a privileged container | Backend, Platform | `backend/internal/httpapi/api.go:229-308`; `backend/internal/store/store.go:1413-1444`; `backend/cmd/virtnoded/main.go:844-897,938-940` |
| SR-05 | High | Internal trust boundary is one shared secret, hard-coded in deploy, with public listeners and self-asserted host identity/capabilities | Backend, Platform | `backend/internal/httpapi/api.go:629-693,798-803`; `backend/internal/config/config.go:40,75`; `deploy/vps/docker-compose.yml:30-31,46-51`; `backend/cmd/virtnoded/main.go:250-253,416-447` |
| SR-06 | High | Blob encryption is not end-to-end: control plane stores active blob key, node receives raw key, storage is local-disk on same VPS | Backend, Security, Android | `backend/internal/store/schema.sql:52-55`; `backend/internal/store/store.go:687-709,755,804,1259`; `backend/cmd/virtnoded/blobstore.go:75-80,269-280,317-355,530-650`; `Markdown-Specification.txt:150-191` |
| SR-07 | High | Session relay model uses plaintext replayable tokens and weak ownership binding | Backend | `backend/internal/store/schema.sql:96-104`; `backend/internal/store/store.go:1141-1185,1284-1326`; `backend/internal/httpapi/api.go:520-560` |
| SR-08 | High | Android local secret handling is weak: salted SHA-256 app lock, in-process unlock cache, plaintext prefs for identifiers, no screen privacy | Android | `android-client/app/src/main/java/io/virtroid/client/security/AppLockStore.kt:12-33,55-58,74-75`; `android-client/app/src/main/java/io/virtroid/client/security/IdentityPasswordStore.kt:32-68`; `android-client/app/src/main/java/io/virtroid/client/data/SessionStore.kt:6-20`; `android-client/app/src/main/java/io/virtroid/client/UnlockActivity.kt:64-85`; `android-client/app/src/main/java/io/virtroid/client/SessionActivity.kt:76-81` |
| SR-09 | Medium | Security/UI claims exceed implementation: “Verified”, “Encrypted”, “Direct”, upload/camera, trusted-device UX are incomplete or misleading | Android, Product | `android-client/app/src/main/res/values/strings.xml:102-109,122-149,175-186`; `android-client/app/src/main/java/io/virtroid/client/OnboardingActivity.kt:165-177,246-250`; `android-client/app/src/main/java/io/virtroid/client/SessionActivity.kt:104-113`; `android-client/app/src/main/java/io/virtroid/client/NewRuntimeActivity.kt:68-85` |
| SR-10 | Medium | Runtime viewer ports are still published on `0.0.0.0` even though relay mediation exists | Platform, Backend | `backend/cmd/virtnoded/main.go:941-944,960-964`; `backend/internal/store/store.go:26-27,1446-1475` |
| SR-11 | High | Secrets are embedded in deploy files and sensitive signing material appears to live in the repo tree | Platform, Android | `deploy/vps/docker-compose.yml:7-9,29-31,45-51`; `android-client/app/build.gradle.kts:35-58`; `android-client/release.keystore`; `android-client/signing/virtroid-release.jks` |
| SR-12 | Medium | No automated tests or security regression coverage | Backend, Android | No `_test.go`, `src/test`, or `src/androidTest` files found |

### **Deep-Dive by Area**

#### **1. Architecture**
- Current deployed model is single VPS:
  - `virtroidd` control plane on `:8080`
  - `virtnoded` node/relay on `:8090`
  - `postgres`
  - privileged ReDroid runtime containers launched by node
- Runtime state lives in:
  - Postgres metadata
  - `/srv/virtroid/runtimes/...`
  - `_blobstore/local` chunk storage on the same VPS
- The project is architecturally a control plane + node + relay + Android client, but operationally it is still a one-box system with a very broad trust boundary.

#### **2. Authentication**
- There is no true user sign-in/session layer in the current code.
- Public API protection today is mostly:
  - `account_id`
  - `device_id`
  - optional `blob_access_key`
- Legacy routes are weaker:
  - `GET /api/v1/runtimes`
  - `POST /api/v1/runtimes/{id}/start`
  - `POST /api/v1/runtimes/{id}/stop`
  - `POST /api/v1/sessions`
- Device `public_key` is stored at bootstrap but not used later for request signing or proof-of-possession.

#### **3. Verification / Trusted Device Model**
- The Android app generates an EC keypair in Android Keystore and uploads the public key.
- The backend stores that key.
- I found no subsequent device signature flow, nonce challenge, attestation, or verified-device cryptographic binding.
- The onboarding UI shows “Verified” and “Device fingerprint”, but it does not display or validate a real cryptographic fingerprint.

#### **4. Session Handling**
- Session creation path:
  - app calls `POST /api/v1/me/runtimes/{id}/session`
  - control plane verifies `blob_access_key`
  - control plane creates a `sessions` row with a relay token
  - control plane tells node to prepare viewer
  - app connects to relay host/port/path/token
- Relay protocol:
  - TCP socket to node relay
  - HTTP `CONNECT /api/v1/relay/{id}`
  - `X-Virtroid-Relay-Token` header
  - node resolves target from control plane
  - node opens a tunnel into the guest’s `127.0.0.1:7007`
- Weaknesses:
  - relay tokens are plaintext in DB
  - tokens are replayable until expiry
  - no explicit session close/revoke endpoint
  - legacy session creation does not verify runtime/device ownership

#### **5. End-to-End Encryption**
- **Answer: no, the system does not currently provide end-to-end encryption.**
- What exists:
  - client derives a blob key from password with PBKDF2
  - backend stores a verifier for that key
  - client sends raw `blob_access_key` during runtime actions
  - control plane stores `active_blob_key_b64`
  - node receives raw active key and uses it to decrypt/encrypt snapshots
- Result:
  - server compromise or node compromise can expose historical snapshot confidentiality
  - this is a trusted-server model, not a client-only-decryption model

#### **6. Storage / Persistence**
- Snapshot format is custom and reasonably structured:
  - tar
  - gzip
  - AES-CTR stream encryption
  - HMAC-SHA256 integrity tag
  - 4 MiB chunking
  - manifest metadata
- But:
  - storage backend is only `local-disk`
  - chunks live on same VPS
  - manifest and active key both remain inside the same trust domain
- This does not match the spec’s distributed untrusted-node storage model.

#### **7. Transport Layer**
- Public API uses HTTP by default.
- Relay uses raw TCP plus HTTP CONNECT.
- Android app allows cleartext globally.
- Websocket base logic downgrades `http` to `ws`.
- Internal node/control-plane traffic is also plain HTTP.
- Postgres DSN disables TLS.
- If intercepted, an attacker can observe:
  - account/device identifiers
  - blob access keys
  - relay tokens
  - runtime control traffic
  - live session stream/control traffic

#### **8. Android Client Security**
- Positives:
  - `allowBackup=false`
  - non-exported internal components
  - identity password itself is not stored at rest
- Risks:
  - app lock is only salted SHA-256 with no throttle
  - 6-digit PIN option is weak for offline or device-local guessing
  - app remains unlocked for whole process lifetime
  - blob key stays cached in-process
  - account/device/base URL are stored in plain SharedPreferences
  - no `FLAG_SECURE`, so screenshots/app-switcher previews remain possible
  - release build has no minification/obfuscation

#### **9. UI / UX Audit**
- Good:
  - cohesive dark visual style
  - clear separation of runtime list, controls, and live session
  - session relay integration is now wired
- Not good:
  - security language is ahead of truth
  - upload and camera actions are visible but not implemented
  - persistent vs ephemeral model is visually presented but not functionally selectable
  - no real trusted-device management or revocation
  - dashboard stats like load/ping/tunnel are placeholders or misleading
  - restart/persona reset/factory reset are not implemented to spec

#### **10. Deployment / Operational Posture**
- `virtnoded` is the most dangerous component:
  - root
  - Docker socket
  - `/dev` mount
  - privileged guest containers
- Host prep scripts require:
  - root
  - systemd
  - kernel module load
  - binderfs mount
- Public attack surface in current assets:
  - `8080`
  - `8090`
  - plus runtime viewer ports `46000-46099` via host port allocation
- Secrets are in compose, not a secret manager.

### **Implemented Features and Protocols**

#### **Protocol Inventory**
- `POST /api/v1/bootstrap`
  - creates account, device, primary runtime
- `POST /api/v1/me/identity/register`
  - stores `blob_key_verifier`
- `GET/POST/PATCH/DELETE /api/v1/me/runtimes...`
  - runtime list/control/update/delete
- `POST /api/v1/internal/hosts/heartbeat`
  - node heartbeat and capability reporting
- `GET /api/v1/internal/hosts/{id}/assignments`
  - node fetches runtime assignments and active blob key
- `POST /api/v1/internal/viewer/prepare`
  - control plane tells node to prep scrcpy
- `CONNECT /api/v1/relay/{id}`
  - app-to-node live session tunnel
- Snapshot protocol:
  - `VRTBLOB1` header
  - salt + nonce
  - AES-CTR ciphertext
  - trailing HMAC-SHA256 tag
  - manifest-driven chunk storage

#### **Feature Status**
| Capability | Status | Notes |
|---|---|---|
| New account/device bootstrap | Implemented | Creates account, device, and primary runtime |
| Existing access import by IDs | Implemented | No strong verification |
| User sign-in / authenticated session | Not implemented | No bearer token or user auth layer |
| Trusted device proof-of-possession after bootstrap | Not implemented | Public key uploaded but not used later |
| Runtime create/update/start/stop/wipe/delete | Implemented | Some routes protected only by IDs |
| Live session relay/connect | Implemented | Node CONNECT relay works |
| Reconnect to running runtime | Partial | Manual reuse via runtime still running |
| Restart runtime | Partial | Start/stop exists, no explicit restart flow |
| Persona reset | Not implemented | Persona version bumps on start, but no user-facing reset flow |
| Factory reset | Partial | Wipe exists, not full spec behavior |
| File upload into runtime | Not implemented | UI placeholder only |
| Camera import into runtime | Not implemented | UI placeholder only |
| Trusted device management / revoke | Not implemented | No full UX or backend flow found |
| Encrypted userdata snapshot | Implemented | AES-CTR + HMAC local blob |
| Distributed shard storage | Not implemented | Local-disk only |
| End-to-end encrypted persistence | Not implemented | Server/node possess raw active key |
| TLS transport | Not implemented in repo | Defaults are HTTP/WS |
| App lock | Partial | Weak local model, process-based unlock |

---

## **Concrete Hardening Backlog**

Owner roles:
- **Platform**: deploy, network, Docker/host isolation, secrets
- **Backend**: API, authz, session logic, storage logic
- **Android**: client security, local storage, session UI
- **Security**: trust model, crypto/key-custody design review
- **Product**: UI wording, feature scoping, truthfulness

### **Implementation Order**

| Order | Work Item | Primary Owner | Why First | Done When |
|---|---|---|---|---|
| 1 | Put public traffic behind HTTPS/WSS and block direct cleartext listeners | Platform | Everything else still leaks over the network today | App base URL is `https://...`, node relay is `wss` or HTTPS CONNECT behind TLS, `usesCleartextTraffic=false`, direct public `8080/8090` disabled or proxied |
| 2 | Remove or lock down legacy insecure routes and metadata leaks | Backend | Current legacy routes bypass newer blob-key path | `/api/v1/runtimes/*`, `/api/v1/sessions`, and public `/api/v1/hosts` are removed or made fully authenticated/internal-only |
| 3 | Rotate all exposed secrets and move them out of repo/compose | Platform, Android | Secrets are already exposed in deployment assets | New DB creds, new node credentials, no plaintext shared secret in compose, release signing keys moved out of repo |
| 4 | Close unnecessary viewer port exposure | Platform, Backend | Relay exists; public runtime ports are extra attack surface | Guest `7007` is not published on `0.0.0.0`; only relay path is externally reachable |
| 5 | Introduce real authentication and authorization | Backend, Security | ID-based access is the biggest application-layer flaw | Protected routes require authenticated account context, not raw `account_id`; all mutations are ownership-checked |
| 6 | Bind device identity to signed requests | Backend, Android, Security | Trusted-device model is currently cosmetic | Android signs nonce/challenge with keystore key, backend verifies against stored public key, replay protection exists |
| 7 | Enforce account/device/runtime ownership everywhere | Backend | Needed even after auth is added | `CreateSession`, start/stop/wipe/delete, logs, and update paths all verify same-account ownership |
| 8 | Remove arbitrary runtime image execution | Backend, Platform | Current image path can lead to host compromise | Only allowlisted image digests are accepted; user cannot submit arbitrary `android_image` values |
| 9 | Reduce node blast radius | Platform, Backend | `virtnoded` is currently too close to host-root | Node moved off public edge, private network only, reduced privileges where possible, dedicated host(s), capability/seccomp/AppArmor review |
| 10 | Redesign blob key custody | Backend, Android, Security | Current model is not E2E and server can decrypt | `active_blob_key_b64` no longer stored in DB, keys are wrapped/enveloped, control plane cannot decrypt snapshots by itself |
| 11 | Add real remote storage backend behind the blob interface | Backend, Platform, Security | Needed to move toward spec architecture | Manifest + wrapped keys stay in control plane, ciphertext chunks move to external backend, local-disk remains dev fallback only |
| 12 | Harden session relay tokens | Backend | Tokens are replayable and stored in plaintext | Relay tokens are hashed at rest, shorter TTL, explicit session close/revoke, single-use or resumable tokens with replay control |
| 13 | Harden Android local security | Android | Current local lock model is weak | Keystore-backed lock or biometrics, retry throttling, re-lock on background, blob key cleared on background/timeout, `FLAG_SECURE` added |
| 14 | Make the UI truthful | Android, Product | Security claims currently exceed implementation | Remove fake “Verified/Encrypted/Direct” claims unless backed by real state; show actual trust posture and actual runtime state |
| 15 | Either implement or hide incomplete features | Android, Backend, Product | Visible TODO actions damage trust | Upload/camera/runtime-model controls are either fully wired end-to-end or removed from the UI |
| 16 | Add tests and security regression coverage | Backend, Android | There is no guardrail against regressions | Backend auth/session/storage tests exist, Android local-security/session tests exist, CI runs them |
| 17 | Write a real threat model and deployment hardening guide | Security, Platform, Backend | Needed to align team and docs | Repo contains current trust model, attacker model, key-custody design, network topology, and production hardening checklist |

### **Suggested Phase Breakdown**

#### **Phase 0: Stop the Bleeding**
- Items 1, 2, 3, 4
- Goal: remove obvious public exposure and secret leakage

#### **Phase 1: Real Access Control**
- Items 5, 6, 7
- Goal: replace ID-based access with actual authenticated trusted-device access

#### **Phase 2: Node and Runtime Safety**
- Items 8, 9
- Goal: reduce host-compromise risk and runtime blast radius

#### **Phase 3: Storage and Crypto Truth**
- Items 10, 11
- Goal: move from “encrypted on same VPS” to a real key-custody and storage architecture

#### **Phase 4: Session Security**
- Item 12
- Goal: harden relay authorization and token lifecycle

#### **Phase 5: Client Hardening and UX Integrity**
- Items 13, 14, 15
- Goal: make the Android client defensible and honest

#### **Phase 6: Assurance**
- Items 16, 17
- Goal: keep the system from regressing

## **Recommended Owners by Domain**

| Domain | Owner |
|---|---|
| API auth/authz, session ownership, legacy route removal | Backend |
| Device proof-of-possession and signed requests | Backend + Android + Security |
| TLS, reverse proxy, firewalling, port exposure, secret management | Platform |
| Node privilege reduction and runtime isolation | Platform + Backend |
| Key custody and storage redesign | Backend + Android + Security |
| Android app lock, screen privacy, secure local storage | Android |
| UI wording and incomplete feature exposure | Android + Product |
| Threat model and security acceptance criteria | Security |

## **Bottom-Line Recommendation**
Do **not** market or position the current build as:
- end-to-end encrypted
- trust-minimized
- distributed protected storage
- authenticated trusted-device access

You can accurately describe it today as:
- a single-VPS remote Android runtime controller
- with encrypted local userdata snapshots
- a node-side relay
- and early client-side password-derived blob access control

If you want, I can turn this into a next-step artifact in either of these formats:

1. a GitHub issue backlog with one issue per remediation item  
2. a 30/60/90 day hardening roadmap with milestones and acceptance criteria
