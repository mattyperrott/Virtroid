# Virtroid Suricata profile

The optional `nids` profile runs Suricata on the VPS host network. It forwards
only explicit EVE `alert` records from reviewed signatures through the existing
signed security forwarder. Generic protocol-parser anomalies remain local
diagnostic telemetry and are not sent to client devices. Packet payloads are
not included in the forwarded event.

The bundled rules provide a deliberately small reviewed baseline for TCP SYN
scan patterns. Runtime ADB is loopback-bound and protected by the host firewall;
it is not classified from host-network packet captures because legitimate node
traffic and post-NAT probes cannot be reliably distinguished there. Production
deployments should add only a separately reviewed and pinned ruleset, then tune
it against expected traffic before enabling user-facing warnings.

Set `SURICATA_INTERFACE` to the reviewed host ingress interface, resolve and pin
`SURICATA_IMAGE`, then start the profile with:

```sh
VIRTROID_PROFILES=edge,falco,nids ./deploy.sh up
```

The sensor is detection-only. It does not block traffic.

The long-running sensor remains read-only apart from its bounded runtime files
and event-log volume. Its capabilities are allow-listed, including only the
filesystem override needed to read the pinned image configuration and write the
shared event stream after all other capabilities are dropped. A deployment does
not pass until that event stream health check succeeds.
