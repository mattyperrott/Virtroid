# Security policy

## Report a vulnerability privately

Use GitHub's [Report a vulnerability form](https://github.com/mattyperrott/Virtroid/security/advisories/new)
for suspected security or privacy vulnerabilities in VirtRoid. Private
vulnerability reporting is enabled for this repository. You need a GitHub
account to submit a report through that form.

If you cannot use GitHub private reporting, email
[email@mattyp.com.au](mailto:email@mattyp.com.au) with the subject
`VirtRoid security report`. Start with a sanitized description; do not send
secrets or sensitive user data over ordinary email.

Keep exploit details and sensitive evidence out of public issues, pull
requests, and comments while a report is being assessed. Ordinary bugs and
feature requests belong in the [issue tracker](https://github.com/mattyperrott/Virtroid/issues).

## Versions covered

Reports are accepted against the latest published release and the current
`main` branch. The project does not maintain a guaranteed backport schedule or
long-term support branches. If you find a problem in an older version, include
its version and whether it also affects the current release, if you can check
safely.

VirtRoid is under active development. This reporting policy is not a claim of
independent audit, certification, or security for every deployment. Include
the deployment mode and affected component so the report can be assessed
against the actual trust boundary.

## What to include

- The affected component, version or commit, and deployment mode, such as an
  Aleph SEV-SNP runtime or a trusted-host development environment.
- A concise description of the vulnerability, expected behavior, actual
  behavior, and practical impact.
- Reproduction steps or a minimal proof of concept using accounts and systems
  you control or have permission to test.
- Required access and preconditions, including whether the issue crosses an
  account, device, runtime, node, or operator boundary.
- Sanitized logs or screenshots, with timestamps and time zone when useful.
- A suggested fix, if you have one. A patch is not required to report a problem.

Relevant areas include the Android client and runtime agent; identity and
recovery; device signatures and session capabilities; attestation and key
release; streaming and TLS pinning; snapshot integrity and rollback protection;
storage; runtime cleanup; payment crediting; the operator console; provisioning;
and deployment or release tooling.

Never include real identity passwords, private keys, wallet seeds, recovery
phrases, live credentials, or another person's data. Use test values and
redacted evidence. If a credential has been exposed, tell the maintainer which
kind so its revocation can be coordinated without reposting the value.

## During testing

Use your own accounts and disposable environments. Avoid accessing another
person's data, disrupting a shared service, deleting resources you do not own,
or spending someone else's funds. If you encounter other people's data, stop
and report the minimum evidence needed to explain the issue.

## What happens next

The maintainer will review the report, ask for missing reproduction details,
and coordinate remediation and disclosure through the private advisory.
Reports are prioritized by impact, exploitability, and affected users.

VirtRoid is maintained by an individual, so a fixed response or remediation
deadline cannot be guaranteed. Use the same private thread for follow-up.
Coordinate public disclosure with the maintainer so affected users can receive
a fix or mitigation. Reporter credit will be discussed before publication;
you may ask to remain anonymous. There is no standing paid bug bounty.
