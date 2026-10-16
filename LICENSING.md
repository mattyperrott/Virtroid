# VirtRoid licensing

## Project terms

Original VirtRoid code and documentation are provided under the Apache License
2.0 with Commons Clause License Condition v1.0, reproduced together in
[LICENSE](LICENSE), except where separate terms are identified. This is a
source-available license, not an OSI-approved open-source license.

The Commons Clause restricts the right to sell the software. Its definition of
"Sell" includes charging for a product or service whose value derives entirely
or substantially from the software's functionality, including relevant hosting
or consulting/support services. A rebranded VirtRoid clone or paid hosted
VirtRoid service would require separate permission under these project terms.

The restriction is not a blanket ban on commercial activity. Internal business
use and some larger products with their own substantial value can be permitted
under the terms. The [Commons Clause text and FAQ](https://commonsclause.com/)
explain this distinction; the actual terms in [LICENSE](LICENSE) govern.

For separate commercial licensing requests concerning rights held by the
VirtRoid maintainer, contact [email@mattyp.com.au](mailto:email@mattyp.com.au)
with the subject `VirtRoid commercial licensing`. No commercial permission is
granted by a request or by silence. Contributor-owned material may require
additional permission from its copyright holders.

## Third-party material

The project license does not replace upstream licenses or restrict rights
granted directly by upstream copyright holders. Preserve the notices and
terms for dependencies, vendored source, and embedded assets.

In particular:

- [`third_party/scrcpy-server/LICENSE`](third_party/scrcpy-server/LICENSE)
  contains the Apache License 2.0 notice supplied with the upstream server.
- [`third_party/scrcpy-server/UPSTREAM_ROOT_LICENSE`](third_party/scrcpy-server/UPSTREAM_ROOT_LICENSE)
  retains the upstream repository's GPL-3.0 text. Both notices and the upstream
  commit are documented in
  [`PROVENANCE.md`](third_party/scrcpy-server/PROVENANCE.md).
- The embedded scrcpy server payload in
  `backend/cmd/virtnoded/assets/scrcpy-server.jar` is built from that vendored
  source. It retains the applicable upstream terms; the root project license
  does not relicense it.
- Dependencies, runtime images, and applications installed inside a runtime
  retain their own licenses. Availability through VirtRoid does not make them
  subject to the project license.
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) is adapted from the Contributor
  Covenant 2.1 and retains its attribution. The upstream covenant text remains
  available under its [CC BY 4.0 terms][covenant-license].

The retained GPL notice and the scope of the upstream server's Apache notice
need to be reconciled before asserting that every combined build can be
redistributed under the project terms. These community files do not constitute
a completed dependency-license compatibility review or resolve that upstream
licensing question.

## Contributions

See [contribution licensing](CONTRIBUTING.md#contribution-licensing) before
submitting original work or copying code from another project. Keep existing
license notices with third-party material and identify its origin.

[covenant-license]: https://github.com/EthicalSource/contributor_covenant/blob/91532d80ea5f39b6685aff4892bc6854bf2f6d6f/LICENSE.md
