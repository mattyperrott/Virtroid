# Virtroid VPS Deploy

> **Release target (2026-07-21):** The public signed-bootstrap release uses schema
> `2026072102`. The previously verified node-registry release used schema `2026071903` with the
> approved production node registry. Production builds, release bundles,
> rollback state, operational secrets, and local backups remain on the VPS.
> A renterd recovery seed is the deliberate exception: two physical offline
> copies must exist outside both the VPS and developer machines. Developer
> machines and source-hosting services are not release runners or backup targets.

This folder is intended to be deployable on a fresh Ubuntu VPS with:

- Docker + Docker Compose
- binderfs for ReDroid containers
- a filled `deploy/vps/.env`
- a TLS PEM bundle for HAProxy when the `edge` profile is used

The default layout is:

```text
/opt/virtroid-source          root-owned offline production source checkout
/opt/virtroid/deploy/vps/.env deployment configuration
/var/lib/virtroid-release-bundles root-only release image archives
/srv/virtroid                 runtime data, assets, TLS bundle
/srv/virtroid/tls/virtroid.pem HAProxy certificate bundle
```

## Fresh Server

Install a reviewed source snapshot at `/opt/virtroid-source`, remove `.github`,
initialize it as a root-owned offline Git repository with no remote, and copy
only its deployment tree to the runtime location. Then prepare the host and
create the dedicated administrative account with an SSH public key:

```bash
sudo test -z "$(sudo git -C /opt/virtroid-source remote)"
sudo test ! -e /opt/virtroid-source/.github
sudo install -d -o root -g root -m 0755 /opt/virtroid/deploy
sudo cp -a /opt/virtroid-source/deploy/vps /opt/virtroid/deploy/vps
sudo chown -R root:root /opt/virtroid /opt/virtroid-source
sudo chmod -R go-w /opt/virtroid /opt/virtroid-source
cd /opt/virtroid/deploy/vps
sudo bash ./prepare-redroid-host.sh
sudo ./generate-env.sh https://your-domain.example your-node-id
sudo VIRTROID_AUTHORIZED_KEY_FILE=/tmp/virtroid-deploy.pub \
  bash ./create-deploy-user.sh
```

The default deploy account is `virtroid`. Its password is locked and SSH must
use the installed public key. The deployment tree and environment remain
root-owned; the account uses passwordless sudo for release and host
administration and belongs to the Docker and certificate groups. Docker control
is already root-equivalent, so this account is an auditable administrative
identity, not a security sandbox.

Test a completely fresh key login and `sudo -n true` before enabling key-only
SSH. Keep the original root session open until the second fresh login succeeds:

```bash
ssh -i ~/.ssh/virtroid-cp-ed25519 virtroid@your-server.example
sudo -n true
sudo /opt/virtroid/deploy/vps/enable-key-only-ssh.sh
```

The final helper disables direct root SSH and all password authentication. It
refuses to run unless the deploy account has a valid authorized key and rolls
back if SSH validation or reload fails.

The generated production configuration enables public account bootstrap and
disables shared-secret node self-enrollment. Production node requests are
accepted only after an operator-controlled node/key approval exists. The
control plane signs node callbacks with a separate P-256 key, and nodes accept
only pinned, fresh, non-replayed callback signatures.
Public bootstrap does not require operator approval or a shared secret. Every
request must carry a fresh signature from the P-256 Android Keystore key whose
public half is in that exact request body; account ID, device ID, request path,
timestamp, nonce, and body hash are all covered. Bootstrap creates only the
account, default entitlement, and signing device--never a runtime. Request-size
limits and a bounded transient rate limiter reduce anonymous abuse without
persisting IP addresses as identity metadata. Set `BOOTSTRAP_ENABLED=false`
only for an intentional maintenance freeze.

## VPS-local releases

The production source checkout is `/opt/virtroid-source`. It is root-owned,
offline, and has no Git remote or `.github` directory. Git is used only as a
local content/version ledger on the VPS. GitHub may hold a development copy for
safekeeping, but the VPS never fetches from it and GitHub has no credential or
release role.

Only reviewed source code enters the offline checkout. Online production
secrets, database dumps, Docker image archives, and release state never enter a
developer checkout or source host. The renterd wallet seed has separate physical
offline recovery copies as described in `renterd.md`.
After updating the source snapshot, make a local VPS commit with no remote:

```bash
sudo git -C /opt/virtroid-source status --short
sudo git -C /opt/virtroid-source remote   # must print nothing
sudo git -C /opt/virtroid-source add --all
sudo git -C /opt/virtroid-source commit -m 'Reviewed production source update'
sudo chown -R root:root /opt/virtroid-source
sudo chmod -R go-w /opt/virtroid-source
```

The first setup records the existing node-key fingerprint in a root-only VPS
file. The private key is never printed or copied:

```bash
sudo /usr/local/sbin/virtroid-derive-node-fingerprint |
  awk -F= '$1 == "fingerprint_sha256" {print $2}' |
  sudo tee /var/lib/virtroid-deploy/node-fingerprint.sha256 >/dev/null
sudo chown root:root /var/lib/virtroid-deploy/node-fingerprint.sha256
sudo chmod 0400 /var/lib/virtroid-deploy/node-fingerprint.sha256
```

Build and release entirely on the VPS:

```bash
sudo /usr/local/sbin/virtroid-release-on-vps
```

The helper refuses a configured Git remote, `.github` content, non-root source
ownership, a dirty checkout, or group/world-writable source. It snapshots the
source, builds the `linux/amd64` backend image on the VPS, creates a root-only
checksummed bundle under `/var/lib/virtroid-release-bundles`, installs the
reviewed deployment tree atomically, takes a consistent local backup, applies
the schema, verifies the stored node fingerprint and fresh heartbeat, and opens
HTTPS ingress last. An interrupted or uncertain cutover leaves ingress stopped.

The image tag is only a readable local name. The bundle records both the
portable config ID and manifest digest because Docker engines expose different
one of those values as `.Image`; the helper accepts only those two exact
identities and checks every backend container. Third-party infrastructure images
remain pinned to registry digests in `.env`; no Virtroid backend image registry
is used.

`deploy.sh up`, `pull`, `restart`, and `down` are root-only recovery commands.
They use the same maintenance lock, accept only the installed root-owned tree,
verify the active local image ID, and cannot race a backup. Normal releases use
the bundle helpers above.

The node-registry migration used schema `2026071903`; public signed bootstrap
uses `2026072102`. Retained VPS backups remain the recovery
boundary; an older schema image must never be started automatically against a
newer database.

Put a full PEM bundle at `/srv/virtroid/tls/virtroid.pem`. It must contain the
certificate chain and private key in one file:

```bash
cert_gid="$(getent group virtroid-cert | cut -d: -f3)"
sudo install -o root -g "${cert_gid}" -m 0640 \
  fullchain-plus-private-key.pem /srv/virtroid/tls/virtroid.pem
```

For Certbot-managed certificates, install the atomic deploy hook after running
`prepare-redroid-host.sh` and generating `.env`:

```bash
sudo install -m 0755 ./certbot-deploy-hook.sh \
  /etc/letsencrypt/renewal-hooks/deploy/virtroid-haproxy.sh
```

The hook verifies the renewed certificate and matching private key, replaces
the combined PEM atomically with mode `0640`, validates HAProxy, restarts only
the edge container, and waits for HTTPS health.

The direct release helper starts:

- `postgres`
- `virtroidd`
- `virtnoded`
- `edge` via HAProxy by default

Host preparation also enables Fail2ban, conservative SSH attempt/grace limits,
bounded container logs, and a daily root-only local backup timer. Daily backups
refuse active sessions or running managed guests, drain ingress, and pause the
control plane, node, and renterd writers while capturing a PostgreSQL dump,
`/srv/virtroid`, renterd's MySQL databases and partial-slab data when configured,
the reviewed deploy tree, and current/previous release state. The first
legacy-to-immutable backup also saves
the exact immutable Docker image IDs used by each backend container. Portable
checksums cover the complete set, and the seven newest successful sets are kept
under `/var/backups/virtroid`. They are rollback copies on the same host and
never leave it. Deleted account data can remain in retained backups.

## Operator revocation and reactivation

Operator changes are explicit, audited database actions. Revoking an operator
immediately removes all of that operator's nodes and keys from authorization
queries while preserving their history. It does not rotate a compromised key
or erase a guest that is already running on a compromised host, so incident
containment should first drain ingress and stop the affected node agent.

Run registry commands from the protected current image:

```bash
sudo docker stop --time 30 virtroid-edge
sudo docker stop --time 60 virtnoded

sudo docker compose \
  --env-file /opt/virtroid/deploy/vps/.env \
  --env-file /var/lib/virtroid-deploy/current.env \
  -f /opt/virtroid/deploy/vps/docker-compose.yml \
  run --rm --no-deps -T virtroid-admin operator-revoke \
  --operator production \
  --actor 'incident-commander@example' \
  --reason 'operator credential compromise'
```

Do not reactivate until the operator identity has been independently verified,
compromised credentials have been replaced, and affected node keys have been
rotated or re-approved as appropriate. `operator-approve` creates a new
operator or reactivates a revoked one; the supplied actor and reason become an
append-only audit record:

```bash
sudo docker compose \
  --env-file /opt/virtroid/deploy/vps/.env \
  --env-file /var/lib/virtroid-deploy/current.env \
  -f /opt/virtroid/deploy/vps/docker-compose.yml \
  run --rm --no-deps -T virtroid-admin operator-approve \
  --operator production \
  --name 'Virtroid production operator' \
  --actor 'security-admin@example' \
  --reason 'new credentials independently verified'
```

Review the latest operator actions before restoring services:

```bash
sudo docker exec virtroid-postgres psql -U virtroid -d virtroid -x -c \
  'SELECT operator_id, action, actor, reason, created_at
     FROM operator_registry_audit
    ORDER BY created_at DESC
    LIMIT 50;'
```

## VPS-local backups

`virtroid-backup.timer` writes root-only checksummed recovery sets under
`/var/backups/virtroid`. No Mac, GitHub service, or external storage
participates. These backups intentionally do not survive total VPS or provider
loss. That is an explicit limitation of the requested isolation boundary, not
off-site disaster recovery.

## Preinstalled Runtime Apps

`virtnoded` installs only trusted app artifacts. A file copied into
`/srv/virtroid/apks` is ignored unless it is referenced by
`/srv/virtroid/apks/manifest.json` with a SHA-256 pin.

Built-in defaults are limited to pinned entries in the node binary. To add a
local APK or APKM, create a manifest like:

```json
{
  "version": 1,
  "apps": [
    {
      "package_name": "projekt.launcher",
      "display_name": "hyperion launcher",
      "artifact": "projekt.launcher.apk",
      "install_mode": "single",
      "sha256": "<64 lowercase hex sha256>",
      "default": true,
      "set_as_home": true,
      "home_activity": "projekt.launcher.ProjektLauncher"
    }
  ]
}
```

Calculate the pin on the server before adding the manifest entry:

```bash
sha256sum /srv/virtroid/apks/projekt.launcher.apk
```

Supported install modes are `single` for `.apk` and `apkm` for `.apkm`.
Split-directory installs are intentionally not trusted.
`set_as_home` makes the node call Android's package manager to set the supplied
`home_activity` as the default launcher after installation.

## Health Checks

Local checks:

```bash
sudo ./deploy.sh health
sudo ./deploy.sh ps
```

Before moving DNS, test the new IP directly from another machine:

```bash
curl --resolve your-domain.example:443:NEW_SERVER_IP https://your-domain.example/healthz
```

Expected response:

```json
{"ok":true}
```

## DNS Cutover

After the new server is healthy, update DNS:

```text
your-domain.example A NEW_SERVER_IP
www.your-domain.example CNAME your-domain.example
```

Keep the old server available until Android onboarding, runtime creation,
runtime start, viewer connect, shutdown, wipe, and delete have been tested.

## Fresh Database vs Migration

A fresh deploy starts with an empty database. Android clients that already have
local account/device identity from another backend will need local app data
cleared or a migration of the old Postgres database.

For development, a clean database plus app re-bootstrap is usually simpler.
For production, migrate Postgres before DNS cutover.

## Common Commands

```bash
sudo ./deploy.sh up
sudo ./deploy.sh restart
sudo ./deploy.sh logs
sudo ./deploy.sh logs virtroidd
sudo ./deploy.sh logs virtnoded
sudo ./deploy.sh ps
sudo ./deploy.sh health
sudo systemctl start virtroid-backup.service
sudo systemctl status virtroid-backup.timer
```

Optional profiles:

```bash
sudo VIRTROID_PROFILES=edge,falco ./deploy.sh up
sudo VIRTROID_PROFILES=edge,renterd ./deploy.sh up
```

The renterd profile is fail-closed: it will not start until the installed
ceremony helper verifies two offline seed copies, root-only mounted secret
files, the immutable MySQL/renterd images, an operator funding assertion, and
the approved 10-of-30 mainnet shard policy. Follow `renterd.md`; do not bypass
the gate with raw Compose commands.

## Android Domain Note

Release Android builds enforce HTTPS and certificate pins for the configured
control-plane host. If you deploy to a new domain, update the Android default
control-plane URL and TLS pins before building a release APK for that domain.
