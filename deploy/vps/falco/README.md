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

Falco also writes JSON events into the `falco-events` volume. The
`falco-forwarder` service tails that file and submits each event to the control
plane through the existing signed node-request path.

Falco is intentionally scoped to node-side detection. It does not replace APK
intake scanning, runtime entitlement, node identity, remote attestation, or
encrypted snapshot handling.
