# Virtroid Falco Profile

Falco runs as a host/container runtime sensor for the VPS node. It watches Docker
container events, shell starts inside Virtroid-managed containers, Docker socket
use, and unexpected access to the runtime root.

Start it on a VPS node from `deploy/vps`:

```sh
docker compose --profile edge --profile falco up -d falco falco-forwarder
```

Read sensor logs:

```sh
docker logs --tail=100 -f virtroid-falco
```

Falco posts JSON events directly to the local `falco-forwarder` over Docker
network HTTP. The forwarder signs each accepted event with the node key and
submits it to the control plane through the existing signed node-request path.

The profile intentionally avoids a persistent Falco JSONL queue. Forwarding is
bounded by `FALCO_FORWARD_MAX_EVENTS_PER_MINUTE` and
`FALCO_FORWARD_DEDUP_WINDOW`, while the control plane also applies per-node
ingest limits and retention before inserting rows into `security_events`.

Falco is intentionally scoped to node-side detection. It does not replace APK
intake scanning, runtime entitlement, node identity, remote attestation, or
encrypted snapshot handling.
