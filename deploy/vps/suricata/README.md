# Virtroid Suricata profile

The optional `nids` profile runs Suricata on the VPS host network. It forwards
only EVE `alert` and `anomaly` records through the existing signed security
forwarder. Packet payloads are not included in the forwarded event.

The bundled rules provide a deliberately small reviewed baseline for TCP SYN
scan patterns and unexpected ADB probes. Production deployments should
add only a separately reviewed and pinned ruleset, then tune it against expected
traffic before enabling user-facing warnings.

Set `SURICATA_INTERFACE` to the reviewed host ingress interface, resolve and pin
`SURICATA_IMAGE`, then start the profile with:

```sh
VIRTROID_PROFILES=edge,falco,nids,monitoring ./deploy.sh up
```

The sensor is detection-only. It does not block traffic.
