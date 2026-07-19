# Virtroid VPS Deploy

This folder is intended to be deployable on a fresh Ubuntu VPS with:

- Docker + Docker Compose
- binderfs for ReDroid containers
- a filled `deploy/vps/.env`
- a TLS PEM bundle for HAProxy when the `edge` profile is used

The default layout is:

```text
/opt/virtroid                 project checkout
/opt/virtroid/deploy/vps/.env deployment configuration
/srv/virtroid                 runtime data, assets, TLS bundle
/srv/virtroid/tls/virtroid.pem HAProxy certificate bundle
```

## Fresh Server

Copy the project to `/opt/virtroid`, prepare the host, then create the dedicated
deploy account with an SSH public key:

```bash
cd /opt/virtroid/deploy/vps
sudo bash ./prepare-redroid-host.sh
./generate-env.sh https://your-domain.example your-node-id
sudo VIRTROID_AUTHORIZED_KEY_FILE=/tmp/virtroid-deploy.pub \
  bash ./create-deploy-user.sh
```

The default deploy account is `virtroid`. Its password is locked and SSH must
use the installed public key. It can read the root-owned deployment environment
and belongs to the Docker and certificate groups. It also has passwordless sudo
for host administration. Docker control is already root-equivalent, so this
account is an auditable administrative identity, not a security sandbox.

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

The generated production configuration keeps public account bootstrap disabled
and allows control-plane callbacks only to the Compose `virtnoded` service.
Do not enable `BOOTSTRAP_ENABLED` until a durable invite or billing gate exists;
`deploy.sh` rejects that unsafe setting for this topology.

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

Then start the stack:

```bash
./deploy.sh up
```

The helper starts:

- `postgres`
- `virtroidd`
- `virtnoded`
- `edge` via HAProxy by default

Host preparation also enables Fail2ban, conservative SSH attempt/grace limits,
bounded container logs, and a daily root-only local backup timer. Daily backups
retain the seven most recent successful PostgreSQL dumps, `/srv/virtroid`
archives, and deployment-environment copies under `/var/backups/virtroid`. They
are rollback copies on the same host, not a substitute for encrypted off-host
disaster recovery. Deleted account data can remain in those retained copies.

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
./deploy.sh health
./deploy.sh ps
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
./deploy.sh up
./deploy.sh restart
./deploy.sh logs
./deploy.sh logs virtroidd
./deploy.sh logs virtnoded
./deploy.sh ps
./deploy.sh health
sudo systemctl start virtroid-backup.service
sudo systemctl status virtroid-backup.timer
```

Optional profiles:

```bash
VIRTROID_PROFILES=edge,falco ./deploy.sh up
VIRTROID_PROFILES=edge,renterd ./deploy.sh up
```

## Android Domain Note

Release Android builds enforce HTTPS and certificate pins for the configured
control-plane host. If you deploy to a new domain, update the Android default
control-plane URL and TLS pins before building a release APK for that domain.
