Real AMD SEV-SNP test data for `SnpAttestationVerifierTest`.

- `report_milan.hex`, `vcek_milan.der`: an attestation report from a Milan host
  and the VCEK that signed it, from the aleph-rs test data (MIT licence,
  https://github.com/aleph-im/aleph-rs, `crates/aleph-sdk/src/attest/testdata`).
- `milan-ask.der`, `milan-ark.der`, `genoa-ark.der`: AMD's certificate chains
  from https://kdsintf.amd.com/vcek/v1/{Milan,Genoa}/cert_chain, identical to
  the certificates built into the `sev` crate 8.0.0.
