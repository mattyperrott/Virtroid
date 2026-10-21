#!/usr/bin/env bash
# check.sh <workdir> <label> : test-applies ReDroid android-16.0.0_r2 patches to each project
# Projects come from $D/projects-<label>.txt: "<path> <url> <ref>"
set -u
D=$1; label=$2
P=$D/redroid-patches/android-16.0.0_r2
out=$D/src-$label; mkdir -p "$out"
while read -r path url ref; do
  repo=$out/${path//\//_}
  files=$(cat "$P/$path"/*.patch | sed -n 's#^diff --git a/\([^ ]*\) b/.*#\1#p' | sort -u)
  if [ ! -d "$repo" ]; then
    git clone -q --depth 1 --filter=blob:none --no-checkout -b "$ref" "$url" "$repo" 2>&1 | tail -1
    git -C "$repo" sparse-checkout set --no-cone $files >/dev/null 2>&1
    git -C "$repo" checkout -q 2>&1 | tail -1
  fi
  for p in "$P/$path"/*.patch; do
    n=$(basename "$p")
    if git -C "$repo" apply --check "$p" 2>/dev/null; then
      git -C "$repo" apply "$p"; echo "OK      $path/$n"
    elif git -C "$repo" apply --check -C1 "$p" 2>/dev/null; then
      git -C "$repo" apply -C1 "$p"; echo "FUZZ    $path/$n"
    else
      echo "FAIL    $path/$n"
      git -C "$repo" apply --check "$p" 2>&1 | sed 's/^/          /' | head -6
    fi
  done
done < "$D/projects-$label.txt"
