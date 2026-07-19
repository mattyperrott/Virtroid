#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
umask 0077

usage() {
  cat >&2 <<'EOF'
Usage:
  VIRTROID_EXPECTED_NODE_FINGERPRINT=<64-hex> \
  ./deploy/vps/send-local-release.sh <bundle-directory> <user@host>

Optional:
  VIRTROID_SSH_IDENTITY=/absolute/path/to/private-key
  VIRTROID_SSH_PORT=22
  VIRTROID_PROFILES=edge

Transfers a locally built bundle over the operator's authenticated SSH session,
installs its reviewed deployment tree, and invokes the locked VPS release
helper. It never reads credentials from or sends credentials to a source host.
EOF
}

if [ "${1:-}" = -h ] || [ "${1:-}" = --help ]; then
  usage
  exit 0
fi
if [ "$#" -ne 2 ]; then
  usage
  exit 64
fi

bundle_dir="$(cd "$1" 2>/dev/null && pwd -P)" || {
  echo "bundle directory does not exist: $1" >&2
  exit 1
}
destination="$2"
expected_fingerprint="${VIRTROID_EXPECTED_NODE_FINGERPRINT:-}"
ssh_port="${VIRTROID_SSH_PORT:-22}"
profiles="${VIRTROID_PROFILES:-edge}"

[[ "${destination}" =~ ^[a-z_][a-z0-9_-]{0,31}@[A-Za-z0-9][A-Za-z0-9.-]{0,252}$ ]] || {
  echo "destination must be a simple user@host value" >&2
  exit 64
}
[[ "${expected_fingerprint}" =~ ^[0-9a-f]{64}$ ]] || {
  echo "VIRTROID_EXPECTED_NODE_FINGERPRINT must be the independently recorded 64-character fingerprint" >&2
  exit 64
}
[[ "${ssh_port}" =~ ^[0-9]+$ ]] && [ "${ssh_port}" -ge 1 ] && [ "${ssh_port}" -le 65535 ] || {
  echo "VIRTROID_SSH_PORT must be between 1 and 65535" >&2
  exit 64
}
[[ "${profiles}" =~ ^[a-z][a-z0-9-]*(,[a-z][a-z0-9-]*)*$ ]] || {
  echo "VIRTROID_PROFILES is invalid" >&2
  exit 64
}

for required_file in backend-image.tar deployment-tree.tar.gz release.env SHA256SUMS; do
  [ -f "${bundle_dir}/${required_file}" ] && [ ! -L "${bundle_dir}/${required_file}" ] || {
    echo "release bundle is missing ${required_file}" >&2
    exit 1
  }
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "${bundle_dir}" && sha256sum --check --strict SHA256SUMS)
  installer_digest="$(tar -xOf "${bundle_dir}/deployment-tree.tar.gz" vps/install-reviewed-deployment-tree.sh | sha256sum | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  (cd "${bundle_dir}" && shasum -a 256 --check SHA256SUMS)
  installer_digest="$(tar -xOf "${bundle_dir}/deployment-tree.tar.gz" vps/install-reviewed-deployment-tree.sh | shasum -a 256 | awk '{print $1}')"
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

read_release_value() {
  local wanted="$1"
  awk -v wanted="${wanted}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      separator = index($0, "=")
      if (!separator) next
      if (substr($0, 1, separator - 1) == wanted) {
        found++
        value = substr($0, separator + 1)
      }
    }
    END { if (found != 1) exit 1; print value }
  ' "${bundle_dir}/release.env"
}

source_sha="$(read_release_value VIRTROID_SOURCE_SHA)"
tree_digest="$(read_release_value VIRTROID_DEPLOYMENT_TREE_SHA256)"
[[ "${source_sha}" =~ ^[0-9a-f]{40}$ ]] && [[ "${tree_digest}" =~ ^[0-9a-f]{64}$ ]] || {
  echo "release bundle identity is invalid" >&2
  exit 1
}
[[ "${installer_digest}" =~ ^[0-9a-f]{64}$ ]] || {
  echo "could not identify the bundled deployment-tree installer" >&2
  exit 1
}

ssh_args=(-o BatchMode=yes -o StrictHostKeyChecking=yes -p "${ssh_port}")
rsync_ssh="ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -p ${ssh_port}"
if [ -n "${VIRTROID_SSH_IDENTITY:-}" ]; then
  [ "${VIRTROID_SSH_IDENTITY#/}" != "${VIRTROID_SSH_IDENTITY}" ] &&
    [[ "${VIRTROID_SSH_IDENTITY}" =~ ^/[A-Za-z0-9._/-]+$ ]] &&
    [ -f "${VIRTROID_SSH_IDENTITY}" ] && [ ! -L "${VIRTROID_SSH_IDENTITY}" ] || {
    echo "VIRTROID_SSH_IDENTITY must be a shell-safe absolute regular-file path" >&2
    exit 64
  }
  ssh_args+=(-o IdentitiesOnly=yes -i "${VIRTROID_SSH_IDENTITY}")
  rsync_ssh+=" -o IdentitiesOnly=yes -i ${VIRTROID_SSH_IDENTITY}"
fi

remote_stage="$(ssh "${ssh_args[@]}" "${destination}" 'mktemp -d /tmp/virtroid-local-release.XXXXXXXXXX')"
[[ "${remote_stage}" =~ ^/tmp/virtroid-local-release\.[A-Za-z0-9]{10}$ ]] || {
  echo "remote host returned an unsafe staging path" >&2
  exit 1
}
cleanup_remote() {
  ssh "${ssh_args[@]}" "${destination}" \
    "find '${remote_stage}' -xdev -depth -delete" >/dev/null 2>&1 || true
}
trap cleanup_remote EXIT HUP INT TERM

ssh "${ssh_args[@]}" "${destination}" 'sudo -n true'
rsync -a -e "${rsync_ssh}" \
  "${bundle_dir}/" "${destination}:${remote_stage}/"
ssh "${ssh_args[@]}" "${destination}" \
  "install -d -m 0700 '${remote_stage}/tree' && tar -xzf '${remote_stage}/deployment-tree.tar.gz' -C '${remote_stage}/tree'"
# The first direct release may be replacing an older installer. Bootstrap only
# this self-verifying root entrypoint, after checking it against the operator's
# local bundle; it installs every other runtime copy from the reviewed tree.
ssh "${ssh_args[@]}" "${destination}" \
  "test \"\$(sha256sum '${remote_stage}/tree/vps/install-reviewed-deployment-tree.sh' | awk '{print \$1}')\" = '${installer_digest}' && sudo install -o root -g root -m 0755 '${remote_stage}/tree/vps/install-reviewed-deployment-tree.sh' /usr/local/sbin/virtroid-install-reviewed-deployment-tree"
ssh "${ssh_args[@]}" "${destination}" \
  "sudo /usr/local/sbin/virtroid-install-reviewed-deployment-tree '${remote_stage}/tree/vps' '${tree_digest}'"
ssh "${ssh_args[@]}" "${destination}" \
  "sudo VIRTROID_PROFILES='${profiles}' /usr/local/sbin/virtroid-apply-local-release '${remote_stage}' '${expected_fingerprint}'"

trap - EXIT HUP INT TERM
cleanup_remote
printf 'release %s deployed through the direct operator SSH path\n' "${source_sha}"
