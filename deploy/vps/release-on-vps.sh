#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

readonly trusted_helper=/usr/local/sbin/virtroid-release-on-vps
readonly source_root=/opt/virtroid-source
readonly artifact_root=/var/lib/virtroid-release-bundles
readonly build_state_root=/var/lib/virtroid-build
readonly build_lock=${build_state_root}/build.lock
readonly fingerprint_file=/var/lib/virtroid-deploy/node-fingerprint.sha256

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

if [ "$(id -u)" -ne 0 ]; then
  die "run as root"
fi
if [ "$(readlink -f -- "$0")" != "${trusted_helper}" ]; then
  die "run the installed root-owned VPS release helper: ${trusted_helper}"
fi
if [ "$#" -ne 0 ]; then
  die "usage: virtroid-release-on-vps"
fi
if [ ! -r "/proc/$$/fd/255" ]; then
  die "cannot verify the executing VPS release helper descriptor"
fi

for command_name in awk docker find flock git install mktemp readlink rsync sha256sum sort stat tar tr; do
  command -v "${command_name}" >/dev/null 2>&1 ||
    die "missing VPS release command: ${command_name}"
done

install -d -o root -g root -m 0700 "${artifact_root}" "${build_state_root}"
for safe_dir in "${source_root}" "${artifact_root}" "${build_state_root}" /var/lib/virtroid-deploy; do
  [ -d "${safe_dir}" ] && [ ! -L "${safe_dir}" ] || die "unsafe VPS release directory: ${safe_dir}"
  [ "$(stat -c '%u' "${safe_dir}")" -eq 0 ] || die "VPS release directory is not root-owned: ${safe_dir}"
  (( (8#$(stat -c '%a' "${safe_dir}") & 0022) == 0 )) ||
    die "VPS release directory is group/world writable: ${safe_dir}"
done
[ -f "${trusted_helper}" ] && [ ! -L "${trusted_helper}" ] || die "installed VPS release helper is unsafe"
[ "$(stat -c '%u' "${trusted_helper}")" -eq 0 ] || die "installed VPS release helper is not root-owned"
(( (8#$(stat -c '%a' "${trusted_helper}") & 0022) == 0 )) || die "installed VPS release helper is writable by an untrusted account"
executing_digest="$(sha256sum "/proc/$$/fd/255" | awk '{print $1}')"
installed_digest="$(sha256sum "${trusted_helper}" | awk '{print $1}')"
[ "${executing_digest}" = "${installed_digest}" ] || die "VPS release helper changed while this process was starting"

[ -d "${source_root}/.git" ] && [ ! -L "${source_root}/.git" ] || die "offline VPS source repository is missing"
[ ! -e "${source_root}/.github" ] && [ ! -L "${source_root}/.github" ] || die "GitHub automation must not exist on the production VPS"
[ -z "$(git -C "${source_root}" remote)" ] || die "production VPS source repository must not have a Git remote"
unsafe_path="$(find "${source_root}" -xdev -mindepth 1 \( -type l -o ! -type f ! -type d \) -print -quit)"
[ -z "${unsafe_path}" ] || die "offline VPS source contains a symlink or special path: ${unsafe_path}"
foreign_path="$(find "${source_root}" -xdev -mindepth 0 ! -user root -print -quit)"
[ -z "${foreign_path}" ] || die "offline VPS source is not entirely root-owned: ${foreign_path}"
writable_path="$(find "${source_root}" -xdev -mindepth 0 -perm /022 -print -quit)"
[ -z "${writable_path}" ] || die "offline VPS source is group/world writable: ${writable_path}"
if ! git -C "${source_root}" diff --quiet --ignore-submodules -- ||
   ! git -C "${source_root}" diff --cached --quiet --ignore-submodules -- ||
   [ -n "$(git -C "${source_root}" ls-files --others --exclude-standard)" ]; then
  die "offline VPS source must be clean and committed"
fi

[ -f "${fingerprint_file}" ] && [ ! -L "${fingerprint_file}" ] || die "trusted node fingerprint file is missing"
[ "$(stat -c '%u' "${fingerprint_file}")" -eq 0 ] || die "trusted node fingerprint file is not root-owned"
case "$(stat -c '%a' "${fingerprint_file}")" in 400|600) ;; *) die "trusted node fingerprint file has an unsafe mode" ;; esac
expected_fingerprint="$(tr -d '\n' < "${fingerprint_file}")"
[[ "${expected_fingerprint}" =~ ^[0-9a-f]{64}$ ]] || die "trusted node fingerprint is invalid"

exec 9>"${build_lock}"
flock -n 9 || die "another VPS release build is running"
source_sha="$(git -C "${source_root}" rev-parse --verify HEAD)"
[[ "${source_sha}" =~ ^[0-9a-f]{40}$ ]] || die "offline VPS source commit is invalid"
bundle_dir="${artifact_root}/${source_sha}"
if [ ! -d "${bundle_dir}" ]; then
  source_snapshot="$(mktemp -d "${build_state_root}/source.XXXXXXXXXX")"
  cleanup_source() {
    if [ -n "${source_snapshot:-}" ] && [ -d "${source_snapshot}" ]; then
      find "${source_snapshot}" -xdev -depth -delete
    fi
  }
  trap cleanup_source EXIT HUP INT TERM
  rsync -a --one-file-system "${source_root}/" "${source_snapshot}/"
  "${source_snapshot}/deploy/vps/build-local-release.sh" "${artifact_root}"
  cleanup_source
  source_snapshot=
  trap - EXIT HUP INT TERM
fi

for required_file in backend-image.tar deployment-tree.tar.gz release.env SHA256SUMS; do
  [ -f "${bundle_dir}/${required_file}" ] && [ ! -L "${bundle_dir}/${required_file}" ] ||
    die "VPS release bundle is missing ${required_file}"
done
(
  cd "${bundle_dir}"
  sha256sum --check --strict SHA256SUMS
)
bundle_source="$(awk -F= '$1 == "VIRTROID_SOURCE_SHA" {found++; value=$2} END {if (found != 1) exit 1; print value}' "${bundle_dir}/release.env")"
tree_digest="$(awk -F= '$1 == "VIRTROID_DEPLOYMENT_TREE_SHA256" {found++; value=$2} END {if (found != 1) exit 1; print value}' "${bundle_dir}/release.env")"
[ "${bundle_source}" = "${source_sha}" ] || die "VPS release bundle source does not match the offline checkout"
[[ "${tree_digest}" =~ ^[0-9a-f]{64}$ ]] || die "VPS release bundle tree digest is invalid"

stage="$(mktemp -d /tmp/virtroid-local-release.XXXXXXXXXX)"
cleanup_stage() {
  if [ -d "${stage}" ]; then
    find "${stage}" -xdev -depth -delete
  fi
}
trap cleanup_stage EXIT HUP INT TERM
rsync -a --one-file-system "${bundle_dir}/" "${stage}/"
install -d -o root -g root -m 0700 "${stage}/tree"
tar -xzf "${stage}/deployment-tree.tar.gz" -C "${stage}/tree"
installer_digest_before="$(sha256sum /usr/local/sbin/virtroid-install-reviewed-deployment-tree | awk '{print $1}')"
/usr/local/sbin/virtroid-install-reviewed-deployment-tree "${stage}/tree/vps" "${tree_digest}"
installer_digest_after="$(sha256sum /usr/local/sbin/virtroid-install-reviewed-deployment-tree | awk '{print $1}')"
if [ "${installer_digest_after}" != "${installer_digest_before}" ]; then
  # The installer is itself part of the reviewed deployment tree. Re-run the
  # newly installed copy before applying the release so additions to the
  # runtime integration list cannot be skipped by the prior installer.
  /usr/local/sbin/virtroid-install-reviewed-deployment-tree "${stage}/tree/vps" "${tree_digest}"
fi
VIRTROID_PROFILES=edge /usr/local/sbin/virtroid-apply-local-release "${stage}" "${expected_fingerprint}"
trap - EXIT HUP INT TERM
cleanup_stage

printf 'VPS-local release completed source=%s bundle=%s\n' "${source_sha}" "${bundle_dir}"
