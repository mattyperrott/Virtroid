# Security Policy

## Supported code

Virtroid is under active development. Security fixes are made only on the
current `main` branch; older commits and unreleased snapshots are not supported.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability or include secrets,
tokens, user data, exploit details, or production identifiers in public GitHub
content.

Use the repository's **Security** tab and **Report a vulnerability** to create a
private GitHub Security Advisory. Include:

- the affected commit and component;
- prerequisites and a minimal reproduction;
- the expected and observed security boundary;
- impact and any known exploitation; and
- suggested remediation, if available.

If private vulnerability reporting is unavailable, contact the repository owner
through an established private channel and ask for a private advisory to be
opened before sending sensitive details.

Receipt will be acknowledged when an operator is available. No response or
remediation deadline is promised while the project remains pre-production.

## Current security boundary

ReDroid and the node agent run inside a trusted privileged host boundary. The
runtime node can access active Android plaintext and restore material. Transport
and stopped-snapshot encryption do not protect an active runtime from a
compromised VPS, node agent, Docker administrator, or infrastructure operator.

See [`docs/current-status-and-roadmap.md`](docs/current-status-and-roadmap.md)
for the current evidence and unsupported claims.
