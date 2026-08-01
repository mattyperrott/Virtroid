# Virtroid monitoring

The `monitoring` Compose profile runs Prometheus and Alertmanager on loopback.
Prometheus scrapes the control plane and node over the internal control network,
and evaluates the reviewed rules in `virtroid-alerts.yml`.

The committed Alertmanager receiver intentionally has no external notifier: a
repository cannot safely guess or commit an operator webhook, mail credential,
or paging destination. Alerts are still evaluated and visible in Alertmanager.
Before calling the deployment paged, replace `operator-config-required` with a
reviewed receiver mounted from `/etc/virtroid/secrets` and test one synthetic
alert. This is a deployment requirement, not a hidden successful default.

HTTP requests use W3C `traceparent` propagation between the Android-facing
control plane and node callbacks. Sampled server/client spans are emitted as
single-line structured log records containing the service, a reduced trace-ID
hash, span ID, bounded target class, status, and duration. Raw request URLs and
raw trace IDs are not logged.
