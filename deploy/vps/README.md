# Virtroid VPS Deploy

> **Rollout status (2026-07-19):** The last verified VPS snapshot runs schema
> `2026071902`. The schema-`2026071903` node registry, direct local release
> path, and Restic units described below exist in the current working branch but
> have not yet been promoted to that host. Do not run the schema boundary until
> the exact local commit passes all release gates, a final local backup is
> verified, and a real independent off-host upload plus clean-host restore drill
> has succeeded.

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

The generated production configuration keeps public account bootstrap disabled
and disables shared-secret node self-enrollment. Production node requests are
accepted only after an operator-controlled node/key approval exists; the
remaining `NODE_SHARED_SECRET` protects control-plane-to-node callbacks until
that separate path is replaced with mutually authenticated transport.
Do not enable `BOOTSTRAP_ENABLED` until a durable invite or billing gate exists;
`deploy.sh` rejects that unsafe setting for this topology.

## Direct local releases

The source remote is passive safekeeping only. It has no VPS key, deployment
credential, image-registry role, approval environment, or place in the release
path. Production releases are built on the trusted operator workstation and
sent directly to the human `virtroid` account over its pinned, key-only SSH
connection. The VPS never checks out or builds source.

The workstation helper requires a clean local commit, builds one
`linux/amd64` image, binds its labels to that commit, schema version, and the
complete reviewed deployment-tree digest, then writes the exact Docker image ID
and portable checksums into a private bundle:

```bash
./deploy/vps/build-local-release.sh
```

Before the first release, derive the node fingerprint through the existing
administrative connection without printing the private key. Record the
`fingerprint_sha256` independently; future releases must match it exactly:

```bash
ssh virtroid@your-server.example \
  'sudo /usr/local/sbin/virtroid-derive-node-fingerprint'
```

Send the resulting bundle from the workstation. Use the exact bundle directory
printed by the build command and the already verified administrative key:

```bash
VIRTROID_EXPECTED_NODE_FINGERPRINT=REPLACE_WITH_64_HEX \
VIRTROID_SSH_IDENTITY="$HOME/.ssh/virtroid-cp-ed25519" \
./deploy/vps/send-local-release.sh \
  ./dist/releases/REPLACE_WITH_40_HEX_COMMIT_SHA \
  virtroid@your-server.example
```

The transfer helper verifies the bundle locally, uses strict host-key checking,
installs the reviewed tree atomically, and invokes the preinstalled root helper.
The root helper snapshots the unprivileged staging files once, verifies their
checksums, exact local image ID, architecture, labels, schema, and tree digest,
and takes the maintenance lock shared by releases and backups. It then makes a
verified pre-deploy backup, imports the image, migrates the control plane,
approves only the independently recorded node key, waits for a fresh matching
heartbeat, and opens ingress last. An interrupted or uncertain cutover leaves
ingress stopped for explicit operator recovery. It does not automatically
restart an older image across a database-schema boundary.

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

The first node-registry release remains a supervised schema boundary from live
schema `2026071902` to branch schema `2026071903`. The old pre-registry image is
not a valid automatic rollback after that migration. Keep the verified
schema-v2 recovery set until schema-v3 data and recovery have been independently
checked.

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
`/srv/virtroid`, renterd data when configured, the reviewed deploy tree, and
current/previous release state. The first legacy-to-immutable backup also saves
the exact immutable Docker image IDs used by each backend container. Portable
checksums cover the complete set, and the seven newest successful sets are kept
under `/var/backups/virtroid`. They are rollback copies on the same host. The
optional Restic layer described below encrypts and copies the newest verified
set to independent storage. Deleted account data can remain in either retained
backup tier.

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

## Encrypted Off-site Backups

The scripts and units in this section are tooling, not evidence that off-site
recovery exists. As of the 2026-07-19 live inventory, the VPS had no Restic
binary, remote repository credentials, completed off-host upload, or clean-host
restore record. Supplying an independent repository, escrowing its credentials,
and recording a successful restore on a clean host are release blockers.

Host preparation installs Restic and the off-site backup units, but deliberately
does not enable them until a remote repository and root-only credentials exist.
The wrapper accepts Restic's HTTPS REST, S3, B2, Azure, Google Cloud Storage,
OpenStack Swift, and SFTP remote repository schemes. Local repositories,
indirection through general-purpose remotes, and explicit plaintext HTTP
endpoints are rejected because they do not satisfy
the off-host, encrypted-transport requirement.

Create an independent bucket or repository first. Prefer object lock or an
append-only Restic server and credentials that can write backups but cannot
delete them. Generate a unique Restic repository password and keep an
independent recovery copy outside this VPS; losing it makes the encrypted data
unrecoverable:

```bash
sudo install -d -o root -g root -m 0700 /etc/virtroid
openssl rand -base64 48 | sudo tee /etc/virtroid/restic-password >/dev/null
sudo chown root:root /etc/virtroid/restic-password
sudo chmod 0600 /etc/virtroid/restic-password
sudo install -o root -g root -m 0600 \
  /usr/local/share/virtroid/restic.env.example \
  /etc/virtroid/restic.env
sudoedit /etc/virtroid/restic.env
```

`restic.env` is parsed as literal `KEY=VALUE` records, not sourced as shell
code. Do not add quotes, shell expansion, commands, proxy variables, or
unsupported keys. It must remain a regular root-owned file with mode `0400` or
`0600`. The separate `RESTIC_PASSWORD_FILE` must meet the same rules and contain
exactly one non-empty line. Inline `RESTIC_PASSWORD` and executable
`RESTIC_PASSWORD_COMMAND` values are rejected. Before Restic starts, the wrapper
clears inherited exported variables and provides allowed credentials only in
the child environment, not as command-line arguments.

The example uses S3 variables. Other accepted credential sets are Restic REST
username/password, B2 account ID/key, Azure account values, Google project and
root-only application-credential file, or OpenStack `OS_*` credentials. The
canonical backend syntax and variables are
listed in the [Restic repository documentation](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html).
SFTP must use noninteractive public-key authentication configured under
`/etc/ssh`; the hardened service cannot read keys from a home directory.

Initialize a new repository exactly once, or use `snapshots` to verify an
existing one. Neither the repository password nor backend credentials belong in
Git:

```bash
sudo /usr/local/sbin/virtroid-offsite-backup.sh init
sudo /usr/local/sbin/virtroid-offsite-backup.sh snapshots
```

Run the first upload and restore check manually before enabling automation:

```bash
sudo systemctl start virtroid-backup.service
sudo systemctl start virtroid-offsite-backup.service
sudo systemctl start virtroid-offsite-restore-check.service
sudo systemctl enable --now virtroid-offsite-backup.timer
sudo systemctl enable --now virtroid-offsite-restore-check.timer
systemctl list-timers 'virtroid-*backup*' 'virtroid-*restore*'
```

The daily off-site job refuses missing, corrupt, future-dated, or older-than-12-
hour local sets. After uploading it runs a repository metadata check and reads a
configurable percentage of encrypted pack data. The weekly check downloads the
latest tagged snapshot into a private temporary directory, verifies the
portable SHA-256 manifest, lists the `/srv` archive, validates the PostgreSQL
dump end to end with `pg_restore --file=/dev/null`, and removes the restored
files. A clean-host drill must still restore into and start an isolated
PostgreSQL instance.

Deletion is disabled by default so append-only or object-locked credentials can
be used. If `VIRTROID_RESTIC_PRUNE_ENABLED=true` is intentionally selected, the
configured daily/weekly/monthly/yearly retention policy is restricted to this
host and the `virtroid-offsite` tag before pruning. Prefer server-side lifecycle
rules or separate maintenance credentials when the storage provider supports
them.

Useful manual verification commands are:

```bash
sudo /usr/local/sbin/virtroid-offsite-backup.sh check
sudo /usr/local/sbin/virtroid-offsite-backup.sh check-full
sudo /usr/local/sbin/virtroid-offsite-backup.sh restore-check
sudo journalctl -u virtroid-offsite-backup.service
sudo journalctl -u virtroid-offsite-restore-check.service
```

`check-full` reads the whole remote repository and may have significant time and
egress cost. The scheduled restore check proves that this host can decrypt and
materialize a valid backup, but it is not the complete disaster-recovery drill.
Periodically copy only the independently escrowed credentials to a clean host,
restore there, start an isolated PostgreSQL instance, and record recovery time
and application-level data checks. See the [Restic integrity-check guidance](https://restic.readthedocs.io/en/stable/045_working_with_repos.html#checking-integrity-and-consistency)
and [restore procedure](https://restic.readthedocs.io/en/stable/050_restore.html).

Before promoting schema `2026071903`, retain evidence for all of the following:

- the exact clean local source SHA, backend Docker image ID, schema label,
  release-bundle checksums, and reviewed deployment-tree digest;
- a green final-image smoke/vulnerability result and all deployment shell tests
  from that same commit;
- an independently verified SSH host key and production node-key fingerprint;
- a verified final schema-v2 local backup;
- an encrypted upload to an actually independent repository; and
- a clean-host restore that starts isolated PostgreSQL and checks application
  data, not only archive readability.

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
sudo systemctl status virtroid-offsite-backup.timer
sudo systemctl status virtroid-offsite-restore-check.timer
```

Optional profiles:

```bash
sudo VIRTROID_PROFILES=edge,falco ./deploy.sh up
sudo VIRTROID_PROFILES=edge,renterd ./deploy.sh up
```

## Android Domain Note

Release Android builds enforce HTTPS and certificate pins for the configured
control-plane host. If you deploy to a new domain, update the Android default
control-plane URL and TLS pins before building a release APK for that domain.
