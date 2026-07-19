# Security policy

## Supported status

Virtroid currently has no supported public production release. Do not use the
current system for hostile multi-tenancy, high-threat anonymity, or
irreplaceable data.

The verified VPS deployment on 2026-07-19 uses schema `2026071903`, the approved
node/operator registry, and the immutable VPS-local release path. Production
source, builds, release bundles, secrets, rollback state, and backups remain on
the VPS; GitHub has no production credential or deployment authority. No
independent backup exists by explicit isolation choice, so total VPS or
provider loss remains an accepted availability risk.

## Reporting a vulnerability

Send reports through a private channel agreed with the operator. Include the
affected component, version or commit, reproduction steps, impact, and any
suggested mitigation.

Do not open a public issue containing credentials, private keys, access tokens,
customer data, or an exploitable proof of concept. If a secret was exposed,
revoke or rotate it immediately; deleting it from the latest commit is not
enough because Git history and existing clones may retain it.

## Security boundaries

The current deployment assumes a trusted VPS operator. Runtime containers share
the host kernel, and the node agent has root-equivalent Docker control. Snapshot
encryption protects stored data but does not prevent the active node from
seeing live plaintext or usable keys. See the current remediation status for
the authoritative list of controls and unresolved architecture work.

VPS-local release checksums and exact Docker image IDs concern build/deployment
integrity only. Virtroid does not yet implement confidential-VM runtime
attestation, measured key release, client-approved leases, or an account DEK
wrapped to authorized devices. The remaining control-plane-to-node callback
path also uses a shared secret rather than mTLS. These are known architecture
boundaries, not vulnerability-reporting exclusions.
