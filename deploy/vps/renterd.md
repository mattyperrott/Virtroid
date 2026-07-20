# Virtroid renterd blob storage

This deployment keeps `NODE_BLOB_STORE_KIND=local-disk` until renterd is fully
configured, funded, synced, and has active contracts. Do not switch live runtime
blob storage to `sia-renterd` before the smoke test passes.

## Architecture and funding

Sia is only the durable object store for encrypted runtime-userdata blobs. The
VPS remains the current control plane and ReDroid runtime host. renterd must not
receive Android API traffic, control-plane database data, runtime containers, or
plaintext userdata.

The current deployment uses an operator-managed renterd wallet. renterd owns the
wallet that pays hosts and maintains storage contracts; it is infrastructure,
not an account wallet or a user payment destination.

The account-facing API still keeps storage metadata scoped to the account, but
it does not claim per-account renterd custody:

- renterd runs with a node/operator Sia wallet seed
- the node reports renterd readiness, consensus, bucket, autopilot, wallet
  funding, contract status, and deferred-delete health through signed host
  heartbeats
- the backend exposes that status through `GET /api/v1/me/storage`
- the Android client shows storage readiness but never receives or copies the
  operator wallet address
- live runtime blobs stay on `local-disk` until renterd preflight and smoke
  tests pass, then new snapshots can be switched to `sia-renterd`

The signed account API for informational storage state is:

```text
GET /api/v1/me/storage
```

For operator-pooled Sia storage, the account-facing state looks like:

```json
{
  "provider": "sia-renterd",
  "funding_model": "operator-pooled",
  "status": "ready",
  "last_preflight_status": "ready"
}
```

Payment addresses and wallet seeds are never exposed by this endpoint. Funding
is an operator action on the VPS.

## 1. Configure renterd secrets

Edit `/opt/virtroid/deploy/vps/.env` on the VPS:

```dotenv
RENTERD_API_PASSWORD=<long random API password>
RENTERD_SEED=<node/operator Sia wallet seed>

NODE_SIA_RENTERD_WORKER_URL=http://renterd:9980
NODE_SIA_RENTERD_PASSWORD=<same API password>
NODE_SIA_RENTERD_BUCKET=virtroid
NODE_SIA_RENTERD_WALLET_ADDRESS=<operator-only receive address, optional>
NODE_SIA_RENTERD_MIN_SHARDS=<explicit positive integer>
NODE_SIA_RENTERD_TOTAL_SHARDS=<explicit integer greater than or equal to min shards>
```

The renterd seed controls the wallet used for storage contracts. Generate it on
the VPS, back it up in an operator-controlled secret store, and never place it in
source control or the Android client. `NODE_SIA_RENTERD_WALLET_ADDRESS` is
operator-only telemetry and is redacted from account responses.
Choose the shard policy from the number of active contracts and the desired
redundancy; deployment validation intentionally does not invent production
values.

## 2. Start renterd only

```bash
cd /opt/virtroid/deploy/vps
sudo docker compose --env-file .env --profile renterd up -d renterd
sudo docker logs -f virtroid-renterd
```

Keep the core services on `local-disk` while confirming renterd is reachable and
ready.

## 3. Fund and configure contracts

Use the renterd UI or API on the VPS loopback port:

```bash
ssh -L 9980:127.0.0.1:9980 virtroid@185.223.207.157
```

Then open `http://127.0.0.1:9980` locally. Copy the renterd receive address into
`NODE_SIA_RENTERD_WALLET_ADDRESS`, fund the wallet with SC, and form storage
contracts before enabling runtime snapshots.

## 4. Run blob smoke test

After renterd has contracts, run the preflight first:

```bash
cd /opt/virtroid/deploy/vps
sudo docker compose --env-file .env --profile renterd run --rm --no-deps \
  -e NODE_BLOB_PREFLIGHT=1 \
  -e NODE_BLOB_STORE_KIND=sia-renterd \
  -e NODE_RUNTIME_ROOT=/tmp/virtroid-blob-smoke \
  virtnoded
```

The preflight checks:

- worker API URL and API password are configured
- consensus state endpoint is reachable and synced when reported by renterd
- autopilot is enabled so contracts and data health are maintained
- the configured bucket exists and does not permit public reads
- the wallet endpoint reports a non-zero spendable balance
- the active contract count can satisfy the configured total-shard count
- durable deferred-delete records can be drained

Then run the full encrypted write/restore smoke test:

```bash
cd /opt/virtroid/deploy/vps
sudo docker compose --env-file .env --profile renterd run --rm \
  -e NODE_BLOB_SMOKE_TEST=1 \
  -e NODE_BLOB_STORE_KIND=sia-renterd \
  -e NODE_RUNTIME_ROOT=/tmp/virtroid-blob-smoke \
  virtnoded
```

Expected output:

```text
blob smoke test ok: store=sia-renterd
```

This writes an encrypted test snapshot with an authenticated manifest and opaque
object namespace, restores it, compares restored files, and deletes the
manifest-known test chunks. It never uploads plaintext userdata.

## 5. Switch live runtime blobs

Only after the smoke test passes:

```dotenv
NODE_BLOB_STORE_KIND=sia-renterd
```

Then roll `virtnoded`:

```bash
cd /opt/virtroid/deploy/vps
sudo docker compose --env-file .env up -d virtnoded
sudo docker logs -f virtnoded
```

Existing local-disk manifests remain restorable because each runtime manifest
records its store kind. Each runtime migrates naturally after its first complete
start/stop cycle under `sia-renterd`: the local encrypted snapshot is restored,
a new authenticated Sia generation is committed, and the old local generation
is retained inside the authenticated migration manifest. A successful Sia
restore deletes that fallback; a failed Sia restore automatically uses it.
Migrate runtimes in small batches and verify a fresh start before considering
the migration complete.

Failed Sia deletions are recorded at
`/srv/virtroid/runtimes/_blobstore/pending-renterd-deletes.json` and retried by
storage preflight. A pending backlog reports `degraded` instead of being hidden.

## Future confidential runtimes

The current VPS node is trusted with the short-lived runtime blob key because it
hosts the runtime. A later confidential-VM runtime node can implement the same
blob-store interface and receive lease-scoped key material after attestation.
renterd remains only the encrypted object store; it does not become the control
plane or runtime host.
