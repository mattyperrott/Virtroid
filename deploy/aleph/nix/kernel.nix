{ pkgs, lib, alephNix, extraFragment, patches ? [ ], ... }:

# The SEV-SNP guest kernel: Aleph's whitelist configuration
# (aleph-vm nix/kernel-config.fragment, applied over `allnoconfig`) with
# Virtroid's options. Adapted from aleph-vm's nix/kernel.nix (MIT licence,
# Copyright (c) 2021-2024 ALEPH.IM SAS). The changes: for every symbol our
# fragment sets, Aleph's own line is removed first, so ours replaces it
# instead of contradicting it; and `patches` (e.g. kernel-ashmem.patch) apply
# to both the configuration and the build, so their options exist in both.
let
  base = pkgs.linuxPackages_6_18.kernel;

  fragment = pkgs.runCommand "kernel-config-virtroid.fragment" { } ''
    cp ${alephNix}/kernel-config.fragment fragment
    chmod u+w fragment
    for symbol in $(grep -oE '^CONFIG_[A-Z0-9_]+' ${extraFragment} | sort -u); do
      sed -i -E "/^''${symbol}=/d; /^# ''${symbol} is not set$/d" fragment
    done
    cat fragment ${extraFragment} > $out
  '';

  # allnoconfig seeded with the fragment, then olddefconfig. Kconfig silently
  # drops an option whose dependencies are unmet, so every requested line is
  # checked against the result and the build fails on any divergence.
  configfile = pkgs.stdenv.mkDerivation {
    pname = "linux-config-virtroid-snp-guest";
    inherit (base) version src;
    nativeBuildInputs = with pkgs; [ bison flex perl ];
    inherit patches;
    dontFixup = true;
    buildPhase = ''
      export KCONFIG_NOTIMESTAMP=1
      make ARCH=x86_64 KCONFIG_ALLCONFIG=${fragment} allnoconfig
      make ARCH=x86_64 olddefconfig
    '';
    doCheck = true;
    checkPhase = ''
      status=0
      while IFS= read -r line; do
        case "$line" in
          CONFIG_*=*)
            if ! grep -qxF "$line" .config; then
              echo "kernel config: requested '$line', got '$(grep -E "^(# )?''${line%%=*}[= ]" .config || echo "<absent>")'" >&2
              status=1
            fi
            ;;
          "# CONFIG_"*" is not set")
            sym=''${line#\# }
            sym=''${sym%% is not set}
            if grep -qE "^$sym=" .config; then
              echo "kernel config: requested '$line', got '$(grep -E "^$sym=" .config)'" >&2
              status=1
            fi
            ;;
        esac
      done < ${fragment}
      [ "$status" -eq 0 ] || { echo "kernel config: the fragment was not honoured" >&2; exit 1; }
    '';
    installPhase = "cp .config $out";
  };

  kernel = pkgs.linuxKernel.manualConfig {
    inherit (base) version src modDirVersion;
    inherit configfile;
    config = { CONFIG_MODULES = "y"; };
    kernelPatches = map (patch: { name = baseNameOf patch; inherit patch; }) patches;
  };
in
assert lib.assertMsg (lib.versionAtLeast base.version "6.18")
  "SNP guest kernel ${base.version} is below the 6.18 security floor";
kernel
