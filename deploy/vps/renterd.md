# Virtroid renterd production procedure

Virtroid keeps `NODE_BLOB_STORE_KIND=local-disk` until every gate in this
procedure passes. Sia is used only for authenticated, encrypted runtime-userdata
blob generations. It does not host the control plane or live Android runtimes.

The upstream Sia documentation now says renterd still works but is no longer the
preferred starting point for new general-purpose storage deployments. Virtroid
continues to use the pinned renterd API because that is the implemented storage
adapter. The deployment pins renterd v2.9.3 and uses isolated MySQL rather than
the upstream development-oriented SQLite default.

Official references:

- <https://docs.sia.tech/store-your-data/setting-up-renterd>
- <https://docs.sia.tech/get-started-with-sia/the-importance-of-your-seed>
- <https://github.com/SiaFoundation/renterd/tree/v2.9.3>
- <https://hub.docker.com/_/mysql>

## Security and privacy boundary

The stored Sia objects are encrypted before renterd receives them. Individual
Sia hosts receive erasure-coded encrypted pieces and do not receive the runtime
blob key. That does not make the current system anonymous or trustless:

- VPS root can read the online renterd seed, control the operational SC wallet,
  inspect live runtime plaintext, and access short-lived runtime blob keys.
- the VPS provider, network peers, and Sia hosts can observe the VPS IP and
  timing/volume metadata;
- Sia wallet funding and contract activity are public, pseudonymous on-chain
  activity and can be linked to an exchange withdrawal or treasury wallet;
- the same VPS still runs the control plane and ReDroid runtimes;
- confidential runtime execution is a future architecture, not the current
  operating mode.

The online renterd wallet must therefore be a limited-purpose hot wallet. Keep
treasury funds in a separate wallet, preferably hardware-backed, and transfer
only the operating allowance renterd needs.

## Approved initial policy

| Control | Production value |
| --- | --- |
| Network | Sia mainnet |
| Data / total shards | 10 / 30 |
| Required active contracts | At least 30 |
| Autopilot contract target | Upstream default: 50 |
| Contract period / renew window | Upstream default: 6 weeks / 2 weeks |
| Bucket | `virtroid`, `publicReadAccess=false` |
| renterd S3 API | Disabled |
| Unauthenticated downloads | Disabled |
| Third-party explorer integration | Disabled |
| renterd admin API | VPS loopback and private Docker blob network only |
| Sia gateway host bind | VPS loopback; renterd makes outbound peer/host connections |

The 10-of-30 policy is renterd's mainnet upload default: each slab has ten data
shards and twenty parity shards. Virtroid passes these values explicitly on
every upload and refuses production activation with a different policy.

## Gate 1: create two genuinely offline seed copies

Do this on a dedicated temporary, non-networked device. Do not use the VPS or
the development Mac as the backup location.

1. On a separately prepared offline Linux environment, verify the official
   renterd v2.9.3 artifact and its checksum before disconnecting networking.
2. Run `renterd seed`. It prints one 12-word BIP-39 recovery phrase and its
   corresponding 76-character hexadecimal wallet address.
3. Transcribe the phrase exactly onto two durable physical copies. Metal is
   preferable for the primary recovery copy; sealed archival paper is an
   acceptable second copy.
4. Store the copies in two separate, access-controlled physical locations with
   different loss domains. Do not photograph, scan, print, email, sync, or save
   the phrase in a password manager or cloud drive.
5. Record the public wallet address separately, wipe the temporary environment,
   and power the device off.

The Sia seed controls the wallet and recovery. Loss is unrecoverable; theft lets
an attacker spend its SC. A hardware wallet is appropriate for the separate
treasury wallet, but renterd still needs its own online operational seed to form
and renew contracts.

## Gate 2: install the operational copy on the VPS

Open a private direct SSH session yourself. Do not provide the seed to Codex or
place it in a shell command. The helper reads hidden input from `/dev/tty`,
requires the wallet address twice and the seed twice, generates independent API
and MySQL passwords, and records only a non-secret ceremony marker.

```bash
sudo /usr/local/sbin/virtroid-configure-renterd-secrets configure
```

The helper writes root-only files under `/etc/virtroid/secrets` and removes the
legacy seed/password variables from `.env`. The seed is mounted into renterd as
a YAML config file; it is not present in Docker environment metadata, Git, the
Android client, a command argument, the development Mac, or Codex output.

This step deliberately does not start renterd.

Verify the non-secret gate result:

```bash
sudo /usr/local/sbin/virtroid-configure-renterd-secrets verify-secrets
```

## Gate 3: fund before starting renterd

Send mainnet SC from the separate treasury wallet to the offline-derived public
address. A wallet need not be online to receive an on-chain transfer.

The exact amount depends on host prices and the configured storage/transfer
budget. renterd v2.9.3 uses a minimum allowance of 10 SC per contract and its
default target is 50 contracts. Upstream recommends keeping roughly twice the
allowance available, so 1,000 SC is a practical initial floor for that default,
not a guaranteed long-term budget. The operator must explicitly authorize and
perform the transfer; deployment automation never buys or sends SC.

Funding from a KYC exchange or a reused wallet can link the renterd address to
an identity. A separate operational funding path reduces accidental reuse but
does not make public blockchain activity anonymous.

After independently confirming that the transaction was sent to the exact
address, record the assertion:

```bash
sudo /usr/local/sbin/virtroid-configure-renterd-secrets confirm-funded
sudo /usr/local/sbin/virtroid-configure-renterd-secrets verify-activation
```

The first on-node balance check can happen only after renterd starts and syncs.
The activation helper refuses to start it before the offline-backup and funding
markers exist.

## Gate 4: start renterd and its isolated MySQL database

Keep live runtime blob storage on `local-disk`:

```bash
cd /opt/virtroid/deploy/vps
sudo VIRTROID_PROFILES=edge,renterd ./deploy.sh up
```

The renterd profile starts:

- `virtroid-renterd-mysql` on an internal database-only Docker network;
- `virtroid-renterd` with a read-only root filesystem, all Linux capabilities
  dropped, `no-new-privileges`, S3 disabled, and API port 9980 bound only to
  host loopback;
- the ordinary Virtroid services, still configured for `local-disk` blobs.

Check progress without printing the seed, API password, wallet address, or exact
balance:

```bash
sudo /usr/local/sbin/virtroid-renterd-admin status
```

Wait for all of these:

```text
consensus_synced=true
wallet_address_match=true
wallet_spendable_positive=true
```

## Gate 5: create the private bucket and enable contract maintenance

Once synced and funded:

```bash
sudo /usr/local/sbin/virtroid-renterd-admin activate-policy
```

This creates `virtroid` with public reads disabled and enables autopilot. It does
not replace the upstream contract capacity/budget defaults. Re-run status until:

```text
autopilot_enabled=true
private_bucket=true
active_contracts=<at least 30>
```

More than 30 contracts is expected with the upstream target of 50. The live
smoke helper refuses fewer than 30 because a 10-of-30 upload cannot be placed
across fewer active hosts.

## Gate 6: live encrypted write/restore/delete smoke test

```bash
sudo /usr/local/sbin/virtroid-renterd-smoke-test run
sudo /usr/local/sbin/virtroid-renterd-smoke-test verify
```

The helper:

- rechecks the offline-backup, funding, wallet-address, sync, bucket, autopilot,
  contract-count, and 10-of-30 gates;
- runs the real `virtnoded` renterd preflight;
- writes an encrypted authenticated test snapshot under an opaque namespace;
- restores and byte-compares it;
- deletes its manifest-known remote chunks;
- records evidence bound to the exact backend source SHA and deployment-tree
  digest.

It leaves live runtime blobs on `local-disk`.

## Gate 7: enable Sia with rollback

Only after Gate 6 passes:

```bash
sudo /usr/local/sbin/virtroid-renterd-smoke-test enable
```

The helper takes a consistent local recovery backup, atomically changes
`NODE_BLOB_STORE_KIND` to `sia-renterd`, deploys the current immutable release,
rechecks renterd readiness, and verifies the live node environment. A failed
cutover restores `local-disk` and redeploys it.

Existing local manifests remain restorable because each manifest records its
store kind. A runtime migrates after a complete start/stop cycle: Virtroid
restores the local encrypted generation, commits a new authenticated Sia
generation, retains the local generation as a signed migration fallback, and
deletes that fallback only after a later verified Sia restore.

Failed remote deletes are durably journaled at
`/srv/virtroid/runtimes/_blobstore/pending-renterd-deletes.json` and retried by
preflight. A backlog reports degraded storage rather than being hidden.

## Backup and recovery requirements

The daily local backup stops renterd before capture and includes:

- a logical MySQL dump of `renterd` and `renterd_metrics`;
- renterd's `/data` volume, including partial slab buffers;
- the control-plane database and `/srv/virtroid` state;
- checksums and immutable release identity.

It intentionally excludes `/etc/virtroid/secrets`; the seed recovery source is
the two physical offline copies, not another plaintext seed inside a VPS backup.

The local backup is a rollback copy, not disaster recovery. Before treating Sia
storage as final production, store an encrypted renterd metadata backup on a
separate machine/provider and test restoration. Seed alone restores wallet
control but not renterd's object metadata. Do not store the only metadata backup
on Sia itself because recovery would be circular.

renterd must remain online often enough to renew contracts, scan hosts, repair
degraded slabs, and migrate data away from failed hosts. Monitor consensus,
spendable balance, contract count, object health, deferred deletes, MySQL health,
and local/off-machine backup freshness.

## Future confidential runtimes

The current VPS is trusted with live runtime plaintext and short-lived blob keys
because it hosts ReDroid. A later confidential-VM node can keep the same blob
interface while releasing lease-scoped keys only after attestation. renterd
remains encrypted object storage; it does not become the control plane, runtime,
or anonymity layer.
