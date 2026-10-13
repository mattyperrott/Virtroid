# Sia storage through indexd

Runtime snapshots can be kept on Sia instead of a runtime VM's data disk, so a
runtime survives its VM and an idle VM can be retired. The pieces:

- `indexd` on this VPS, with its own PostgreSQL. It forms and renews contracts
  with Sia hosts, pays them from its wallet, repairs data as hosts come and go,
  and keeps the sealed metadata of every snapshot.
- Each Virtroid account gets its own indexd account. `virtroidd` derives the
  account's app key and one-use connect key from `SIA_KEY_SECRET` and registers
  them through indexd's admin API the first time a node asks.
- A runtime VM fetches its account's key from the control plane, then uploads
  the encrypted snapshot straight to the hosts. Only signed requests for the
  routes nodes use reach indexd's application API, through the edge on port
  9982.

Sia is off until `SIA_INDEXER_APP_URL` is set; the release then also starts the
`indexd` Compose profile. The pinned image is upstream commit `f6ae138`: 0.6.1
plus the fix for running with the explorer off, which 0.6.1 crashes on.

## What each party sees

- Sia hosts store erasure-coded shards of data that the node encrypted with the
  runtime's key before upload. They see this VPS's and the VMs' IP addresses
  and how much is stored.
- indexd sees account keys, object sizes and slab locations, never contents.
  Object metadata (which runtime and snapshot an object is) is sealed with the
  account's app key.
- VPS root can read the wallet recovery phrase and `SIA_KEY_SECRET`, and with
  them every account's ciphertext. Contents stay protected by the runtime keys
  that only the phone and the runtime's node hold.
- The wallet's funding and contract transactions are public on the Sia chain.

## Before relying on it

indexd's database is the only map from a snapshot to its shards and holds the
host contracts. Losing it, or this VPS, loses every snapshot on Sia, even
though the shards are still on the hosts. Virtroid makes no automated backups
(README.md), so decide how that database would be recovered, for example a
regular off-server dump, before runtime VMs keep their only copy on Sia.

## Turning it on

1. Release images built with Go 1.27: set `GO_BUILD_IMAGE` in `.env` to
   `golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61`.
2. A dedicated group for the secrets `virtroidd` reads:

   ```sh
   sudo groupadd --system virtroid-sia
   getent group virtroid-sia | cut -d: -f3
   ```

3. The secrets, using the pinned image from `.env`:

   ```sh
   sudo virtroid-configure-indexd-secrets "$INDEXD_IMAGE" https://<domain>:9982 <gid>
   ```

   Then read the `recoveryPhrase` line of `/etc/virtroid/secrets/indexd.yml`
   once and copy it to two offline places. It controls the wallet.
4. In `.env`:

   ```sh
   SIA_INDEXER_APP_URL=https://<domain>:9982
   SIA_APP_PUBLISH=9982
   SIA_SECRET_GID=<gid>
   SIA_INDEXER_ADMIN_PASSWORD_FILE=/etc/virtroid/secrets/sia-indexer-admin-password
   SIA_KEY_SECRET_FILE=/etc/virtroid/secrets/sia-key-secret
   ```

5. Open the port: `sudo virtroid-configure-host-firewall` (with `SIA_APP_PUBLISH=9982`
   in `.env`, it and the release's hardening check expect port 9982 open).
6. Release as usual. indexd syncs the chain itself (the explorer is off, so it
   learns neither the wallet address nor this server's IP); that takes hours.
7. When it is synced, send Siacoin to the wallet address that `/wallet` shows,
   then turn contracts on. The admin API (and indexd's web UI, through an SSH
   tunnel) is on this host's loopback:

   ```sh
   admin() { local route="$1"; shift; curl -s "$@" \
     -u ":$(sudo cat /etc/virtroid/secrets/sia-indexer-admin-password)" "http://127.0.0.1:9990/api${route}"; }
   admin /state
   admin /wallet
   admin /settings/contracts -X PUT -H 'Content-Type: application/json' \
     -d '{"enabled":true,"period":6048,"renewWindow":2016,"wantedContracts":50}'
   ```

8. Once at least 30 contracts are active, runtime VM images built with
   `NODE_BLOB_STORE_KIND=sia-indexd` store their snapshots on Sia. A runtime's
   first save there keeps its old local snapshot as a fallback until the Sia
   copy has been restored once.

## Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `SIA_DATA_SHARDS` / `SIA_TOTAL_SHARDS` | 10 / 30 | Redundancy of every upload. indexd only accepts 1.5x to 4x with a 99.99% chance of recovering each slab. |
| `SIA_MAX_PINNED_BYTES` | 64 GiB | Most one account may store, enforced by indexd. Virtroid's own quota applies first. |
| `SIA_FUND_TARGET_BYTES` | 1 GiB | How much transfer each account is funded for on each host. |

## Deleted accounts

A node never gets a deleted account's key. `virtroidd` removes deleted accounts
from indexd itself, which drops their snapshots there, and then finishes the
deletion of their runtimes. indexd keeps a deleted account's connect key, which
only `SIA_KEY_SECRET` can produce and which allows a single registration.
