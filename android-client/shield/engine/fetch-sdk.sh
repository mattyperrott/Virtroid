#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Clones 0xbow's Privacy Pools v2 monorepo at the commit sdk.lock pins into
# .sdk/pp2 and builds its core and EVM SDKs, which build.mjs bundles. The
# install runs no package scripts and keeps to the repo's own lockfile.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
lock() { sed -n "s/^$1=//p" "$here/sdk.lock"; }
repo="$(lock repo)"
commit="$(lock commit)"
pnpm_version="$(lock pnpm)"
dir="$here/.sdk/pp2"

if [[ ! -d "$dir/.git" ]]; then
    mkdir -p "$here/.sdk"
    git init -q "$dir"
    git -C "$dir" remote add origin "$repo"
fi
# The circuits' build artifacts are Git LFS objects; the engine doesn't need
# them from here (fetch-circuits.mjs downloads and checks the ones it bundles).
git -C "$dir" -c filter.lfs.smudge= -c filter.lfs.process= -c filter.lfs.required=false \
    fetch -q --depth 1 origin "$commit"
git -C "$dir" -c filter.lfs.smudge= -c filter.lfs.process= -c filter.lfs.required=false \
    checkout -q --force --detach FETCH_HEAD
actual="$(git -C "$dir" rev-parse HEAD)"
if [[ "$actual" != "$commit" ]]; then
    echo "fetch-sdk.sh: got $actual, sdk.lock pins $commit" >&2
    exit 1
fi

cd "$dir"
npx -y "pnpm@$pnpm_version" install --frozen-lockfile --ignore-scripts \
    --filter '@privacy-pools-v2/evm-sdk...'
npx -y "pnpm@$pnpm_version" --filter @privacy-pools-v2/core-sdk --filter @privacy-pools-v2/evm-sdk run build
echo "fetch-sdk.sh: SDK $commit built in $dir"
