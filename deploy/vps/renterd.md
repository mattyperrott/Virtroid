# Virtroid renterd blob storage

This deployment keeps `NODE_BLOB_STORE_KIND=local-disk` until renterd is fully
configured, funded, synced, and has active contracts. Do not switch live runtime
blob storage to `sia-renterd` before the smoke test passes.

## Funding model

The current production path uses an operator-pooled renterd wallet per runtime
node. This matches how renterd forms storage contracts: the renterd daemon owns
the wallet that pays hosts and stores encrypted runtime chunks.

The account-facing API still keeps storage metadata scoped to the account, but
it does not claim per-account renterd custody:

- renterd runs with a node/operator Sia wallet seed
- the node reports renterd wallet funding, consensus, contract status, and the
  configured deposit address through signed host heartbeats
- the backend exposes that status through `GET /api/v1/me/storage`
- the Android client shows direct SC funding status and copies the Sia deposit
  address; it does not show a fake USDT/swap quote
- live runtime blobs stay on `local-disk` until renterd preflight and smoke
  tests pass, then new snapshots can be switched to `sia-renterd`

A future per-account wallet model is still possible, but it requires explicit
accounting, contract allocation, and wallet custody design. Do not store raw
account wallet seeds in the control plane.

If a user-owned seed backup is later added, the safe constraint remains:

- the app generates or imports a user-owned Sia seed
- the app encrypts that seed with the user's identity password before sending any
  backup blob to the backend
- the backend stores only the wallet address, funding status, provider choice,
  and optional client-encrypted seed backup
- live runtime blobs stay on `local-disk` until the account storage profile and
  renterd preflight both report ready

The signed account API for this state is:

```text
GET /api/v1/me/storage
PUT /api/v1/me/storage
```

For operator-pooled Sia storage, the account-facing state looks like:

```json
{
  "provider": "sia-renterd",
  "funding_model": "operator-pooled",
  "funding_address": "<renterd deposit address>",
  "wallet_address": "<renterd deposit address>",
  "status": "funding_required"
}
```

## 1. Configure renterd secrets

Edit `/opt/virtroid/deploy/vps/.env` on the VPS:

```dotenv
RENTERD_API_PASSWORD=<long random API password>
RENTERD_SEED=<node/operator Sia wallet seed>

NODE_SIA_RENTERD_WORKER_URL=http://renterd:9980
NODE_SIA_RENTERD_PASSWORD=<same API password>
NODE_SIA_RENTERD_BUCKET=virtroid
NODE_SIA_RENTERD_WALLET_ADDRESS=<renterd receive address shown in the UI>
```

The renterd seed controls the wallet used for storage contracts. Generate and
back it up outside this repository before funding it. The wallet address is
reported to the app through `NODE_SIA_RENTERD_WALLET_ADDRESS` unless the renterd
API exposes a receive address that the node can discover automatically.

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
ssh -L 9980:127.0.0.1:9980 codex@176.126.70.76
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
- wallet endpoint is reachable and appears funded when balance is reported
- active contracts endpoint is reachable and returns at least one contract

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

This writes an encrypted test snapshot, restores it, compares restored files,
and deletes the manifest-known test chunks.

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
records its store kind. New snapshots will use `sia-renterd`.
