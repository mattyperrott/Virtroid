# Contributing to VirtRoid

VirtRoid runs Android remotely, with a phone as its controller and viewer.
Contributions can improve the Android client, runtime agent, Go services,
operator console, VM images, deployment tooling, documentation, or accessibility.

Please follow the [Code of Conduct](CODE_OF_CONDUCT.md) in issues, reviews, and
other project spaces. Report suspected vulnerabilities privately using the
[security policy](SECURITY.md).

## Start with the problem

- Search [existing issues](https://github.com/mattyperrott/Virtroid/issues) and
  [pull requests](https://github.com/mattyperrott/Virtroid/pulls) before opening
  one.
- Use the bug, feature, or accessibility form when it fits. Include the affected
  version or commit and enough detail to reproduce the problem.
- Discuss substantial architecture, protocol, dependency, or deployment changes
  in an issue before building them. A small, focused fix can go straight to a PR.
- Documentation corrections, reproducible bug reports, and accessibility feedback
  are useful contributions even without a code change.

VirtRoid is maintained by an individual. Review and support depend on available
time; opening an issue or PR does not guarantee acceptance or a release date.

## Asking for help

Read the [README](README.md), including its build instructions and repository
map. If you are still blocked, open an issue with the command or task you tried,
your environment, and the sanitized error. State what you have already checked.
Do not post identity passwords, account keys, wallet recovery phrases, signing
keys, session credentials, database contents, or other people's data.

## Set up a development environment

Fork the repository, clone your fork, and create a branch from the current
`main`. Keep each PR focused on one problem.

The pinned toolchain and commands are in the
[README's build instructions](README.md#building-from-source) and
[CI workflow](.github/workflows/ci.yml). At present they include:

| Component | Requirements |
| :--- | :--- |
| Go services | Go 1.27.2; PostgreSQL 18 for integration tests |
| Android modules | JDK 17, Android SDK 36, and the checked-in Gradle wrapper |
| Operator console | Node.js and npm; install with `npm ci` |
| Confidential VM image | Nix and a Linux machine with `/dev/kvm`; see the [Aleph guide](deploy/aleph/README.md) |

Use disposable accounts, runtimes, and infrastructure for development. Point
Android debug builds at your own control plane: without an override, they use
the production URL. For an Android emulator and a local API on port 8080:

```sh
cd android-client
VIRTROID_DEBUG_CONTROL_PLANE_URL=http://10.0.2.2:8080 ./gradlew :app:assembleDebug
```

The [README](README.md#backend) explains how to start that local API. Release
signing keys are not needed for ordinary debug development.

## Validate your change

Run the checks for the components you changed. Report the commands and results
in the PR, including skipped checks and what they leave unverified.

### Go services

From `backend/`:

```sh
gofmt -w path/to/changed.go
go test -p 1 ./...
go test -p 1 -race ./...
go vet ./...
go build ./cmd/...
```

Set `VIRTROID_TEST_DATABASE_URL` to a dedicated, disposable PostgreSQL database
to run database integration tests. They are skipped when it is unset. Test
packages share the database schema, so keep `-p 1`; never use a development
database containing data you need or a production database. CI also runs
`govulncheck` with the version pinned in the workflow.

### Android modules

From `android-client/`, the client checks are:

```sh
./gradlew :app:testDebugUnitTest :app:lintDebug :app:lintRelease \
  :app:assembleDebug :app:verifyReleaseSecurityManifest
```

For runtime-agent changes, also run:

```sh
./gradlew :runtime-agent:testDebugUnitTest :runtime-agent:lintDebug \
  :runtime-agent:lintRelease :runtime-agent:assembleDebug
```

For scrcpy-server changes, follow the
[source and rebuild instructions](third_party/scrcpy-server/PROVENANCE.md), run
the module checks in CI, and verify the embedded asset from the repository root:

```sh
bash backend/third_party/verify-scrcpy-server-asset.sh
```

Exercise changed UI flows on an emulator or device. For lifecycle or streaming
changes, describe which create, start, connect, stop, restore, reset, or delete
flows you actually tested and which deployment mode you used.

### Operator console

From `operator-console/`:

```sh
npm ci
npm run typecheck
npm run build
```

Review changed screens at narrow and wide widths and check keyboard navigation,
focus, labels, and error states. Its local fixture data does not establish that
the live operator API works.

### Deployment and VM images

Use the checks relevant to the changed scripts or configuration in
[CI](.github/workflows/ci.yml), the [VPS guide](deploy/vps/README.md), and the
[Aleph guide](deploy/aleph/README.md). A syntax check or local unit test does not
verify a real SEV-SNP boot, attestation, restore, or teardown. State the boundary
of your validation rather than treating those checks as equivalent.

For documentation-only changes, check links, commands, formatting, and any
claims affected by the edit; application builds are usually unnecessary.

## Keep changes reviewable

- Follow the surrounding code style. Add regression coverage when behavior
  changes, especially for failure handling and security boundaries.
- Explain changes to authentication, attestation, TLS pinning, key handling,
  snapshot integrity, deletion, billing, or permissions. Keep failure paths
  closed and preserve the existing release security gates.
- Describe database migrations, protocol compatibility, operational steps, and
  rollback limits when relevant. Keep dependency changes and lockfiles together.
- Keep secrets, local SDK paths, production configuration, APKs, and generated
  distribution bundles out of commits. Preserve the intentional embedded scrcpy
  asset and its provenance when following its documented rebuild process.
- For UI changes, consider the [accessibility statement](ACCESSIBILITY.md) and
  attach sanitized screenshots or a short description when useful.
- Preserve upstream copyright, license, and provenance notices. Only submit
  material you have the right to contribute; explain the origin of copied or
  vendored code.

## Open a pull request

Target `main` unless a maintainer has identified another integration branch.
Use a title that describes the resulting behavior, fill in the PR template,
and link the relevant issue. Explain the problem, the change, validation, and
any remaining limitations. Respond to review on the substance of the change.

Maintainers handle merging and releases. A merged PR does not itself establish
that a change has been deployed, signed, or validated in production.

## Contribution licensing

Original contributions are submitted under the project terms in [LICENSE](LICENSE):
Apache License 2.0 with Commons Clause v1.0. This is a source-available license
with a restriction on selling the software as defined in the Commons Clause.
Read [LICENSING.md](LICENSING.md) before contributing. Keep separately licensed
third-party material under its existing terms and identify it in your PR.

You retain copyright in your contribution. These guidelines do not transfer
ownership or grant a separate commercial license to the maintainer. If your
contribution needs different terms or additional commercial permissions,
discuss and agree on those before it is merged.
