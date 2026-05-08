# Virtroid Falco Profile

Falco runs as a host/container runtime sensor for the VPS node. It watches Docker
container events, shell starts inside Virtroid-managed containers, Docker socket
use, and unexpected access to the runtime root.

Start it on a VPS node from `deploy/vps`:

```sh
docker compose --profile edge --profile falco up -d falco
```

Read alerts:

```sh
docker logs --tail=100 -f virtroid-falco
```

Falco is intentionally scoped to node-side detection. It does not replace APK
intake scanning, runtime entitlement, node identity, remote attestation, or
encrypted snapshot handling.
