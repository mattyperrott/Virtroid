Placeholder assets so the flake evaluates. A real build overrides them:

    nix build "git+file://$REPO?dir=deploy/aleph/nix#rootImage" \
      --override-input assets path:/path/to/assets

The assets directory holds `node.env` (from this one, filled in),
`runtime-image.json` (a registry image pinned by digest) or `runtime-image.tar` (our own Android build, `docker save`) and `apks/` (the
VPS's `/srv/virtroid/apks`). All of it is measured into the root.
