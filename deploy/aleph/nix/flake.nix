{
  description = "Virtroid confidential runtime VM: measured firmware, kernel, initrd and read-only root";

  inputs = {
    # aleph-vm 2.1 line, the runtime the aleph CLI v0.21.0 measures.
    aleph-vm.url = "github:aleph-im/aleph-vm/7e3c73720e9f203fbf310092238a713d03bdd68e?dir=nix";
    aleph-vm-src = {
      url = "github:aleph-im/aleph-vm/7e3c73720e9f203fbf310092238a713d03bdd68e";
      flake = false;
    };
    nixpkgs.follows = "aleph-vm/nixpkgs";
    # Go 1.27 for virtnoded only (the Sia SDK needs it); the rest of the image
    # keeps aleph-vm's nixpkgs.
    nixpkgs-go.url = "github:NixOS/nixpkgs/7993f34ee47e28a3d607a525aadc6c94b73be728";
    # The deployment's own inputs (node.env, runtime-image.tar, apks/). Build
    # with --override-input assets path:/path/to/assets.
    assets = {
      url = "path:./assets-example";
      flake = false;
    };
  };

  outputs = { self, nixpkgs, nixpkgs-go, aleph-vm, aleph-vm-src, assets, ... }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      lib = pkgs.lib;
      aleph = aleph-vm.packages.${system};
      alephNix = "${aleph-vm-src}/nix";

      kernel = pkgs.callPackage ./kernel.nix {
        inherit alephNix;
        extraFragment = ../kernel-virtroid.fragment;
      };

      # virtnoded and viewercrypt, built from this repository's backend.
      pkgsGo = nixpkgs-go.legacyPackages.${system};
      virtroid-node = (pkgsGo.buildGoModule.override { go = pkgsGo.go_1_27; }) {
        pname = "virtroid-node";
        version = self.shortRev or "dirty";
        src = lib.cleanSource ../../../backend;
        vendorHash = "sha256-PqyJ5/boVdE64zocPmCCr6x8sxAR3MHxgHTzKm4vM+Q=";
        subPackages = [ "cmd/virtnoded" "cmd/viewercrypt" ];
        env.CGO_ENABLED = 0;
        ldflags = [ "-s" "-w" ];
        doCheck = false;
      };

      # The Android runtime image, measured into the root: the assets'
      # runtime-image.tar (our own AOSP build) when present, otherwise the
      # image runtime-image.json pins, fetched by digest. Inside the VM it is
      # named localhost/virtroid-runtime, which no registry can serve.
      runtimeImageSpec = builtins.fromJSON (builtins.readFile "${assets}/runtime-image.json");
      runtimeImageRef = "localhost/virtroid-runtime:${runtimeImageSpec.tag}";
      runtimeImageTar =
        if builtins.pathExists "${assets}/runtime-image.tar"
        then "${assets}/runtime-image.tar"
        else pkgs.dockerTools.pullImage {
          inherit (runtimeImageSpec) imageName imageDigest;
          hash = if runtimeImageSpec.hash == "" then lib.fakeHash else runtimeImageSpec.hash;
          finalImageName = "localhost/virtroid-runtime";
          finalImageTag = runtimeImageSpec.tag;
          os = "linux";
          arch = "amd64";
        };

      rootfs = pkgs.callPackage ./rootfs.nix {
        inherit virtroid-node assets runtimeImageTar runtimeImageRef;
        vmInit = ./vm-init.sh;
      };

      # The root disk Aleph boots: the filesystem followed by its dm-verity
      # hash tree, with a fixed salt and UUID so the root hash is reproducible.
      rootImage = pkgs.runCommand "virtroid-root-image" {
        nativeBuildInputs = [ pkgs.cryptsetup ];
      } ''
        mkdir -p $out
        cp ${rootfs} $out/root.img
        chmod u+w $out/root.img
        # The hash tree starts on a 4 KiB block boundary after the filesystem.
        truncate -s %4096 $out/root.img
        size=$(stat -c %s $out/root.img)
        veritysetup format \
          --data-blocks=$(( size / 4096 )) --hash-offset=$size \
          --salt=0000000000000000000000000000000000000000000000000000000000000000 \
          --uuid=00000000-0000-0000-0000-000000000000 \
          $out/root.img $out/root.img \
          | tee /dev/stderr | awk '/Root hash:/ {print $NF}' | tr -d '\n' > $out/roothash
        printf '%s' "$size" > $out/hash-offset
        [ -s $out/roothash ]
      '';

      # The root hash goes into the initrd, which the launch measurement covers.
      initScript = pkgs.runCommand "init-virtroid.sh" { } ''
        substitute ${./init-virtroid.sh} $out \
          --replace-fail @ROOTHASH@ "$(cat ${rootImage}/roothash)" \
          --replace-fail @HASH_OFFSET@ "$(cat ${rootImage}/hash-offset)"
        chmod +x $out
      '';

      initrd = pkgs.callPackage "${alephNix}/initrd.nix" {
        inherit (aleph) attest-agent;
        inherit kernel;
        init-script = initScript;
        init-common-script = "${alephNix}/init-common.sh";
        udhcpc-script = "${alephNix}/udhcpc.script";
        udhcpc6-script = "${alephNix}/udhcpc6.script";
        withVerity = true;
        withNft = false;
        withLuks = true;
      };

      # Same layout as aleph-vm's instanceImage, so its bundle script packages
      # it (scripts/vprogram_bundle.py build --flavor instance).
      runtimeImage = pkgs.runCommand "virtroid-snp-runtime" { } ''
        mkdir -p $out
        ln -s ${kernel}/bzImage $out/bzImage
        ln -s ${initrd}/initrd $out/initrd
        cp ${aleph.ovmf}/OVMF.fd $out/OVMF.fd
        echo "${self.rev or self.dirtyRev or "unknown"}" > $out/source-rev
      '';
    in
    {
      packages.${system} = {
        inherit kernel virtroid-node rootfs rootImage initrd runtimeImage;
        default = runtimeImage;
      };
    };
}
