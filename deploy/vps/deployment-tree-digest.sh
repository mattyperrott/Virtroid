#!/bin/bash
set -euo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

tree_root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
tree_root="$(cd "${tree_root}" && pwd -P)"

for command_name in find mktemp sha256sum sort stat; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "missing deployment digest command: ${command_name}" >&2
    exit 1
  }
done

[ -d "${tree_root}" ] && [ ! -L "${tree_root}" ] || {
  echo "deployment tree must be a real directory" >&2
  exit 1
}

unsafe_path="$(find "${tree_root}" -xdev -mindepth 1 \
  \( -type l -o ! -type f ! -type d \) -print -quit)"
if [ -n "${unsafe_path}" ]; then
  echo "deployment tree contains a symlink or special path: ${unsafe_path}" >&2
  exit 1
fi

listing_file="$(mktemp)"
cleanup() {
  find "${listing_file}" -delete
}
trap cleanup EXIT

find "${tree_root}" -xdev -mindepth 1 \
  ! -path "${tree_root}/.env" \
  ! -path "${tree_root}/release.env" \
  -print0 > "${listing_file}"
[ -s "${listing_file}" ] || {
  echo "deployment tree contains no distributable paths" >&2
  exit 1
}

sort -z "${listing_file}" |
  while IFS= read -r -d '' absolute_path; do
    relative_path="${absolute_path#"${tree_root}/"}"
    mode="$(stat -c '%a' "${absolute_path}" 2>/dev/null || stat -f '%Lp' "${absolute_path}")"
    if [ -d "${absolute_path}" ]; then
      kind=directory
      content_digest=-
    else
      kind=file
      content_digest="$(sha256sum "${absolute_path}" | awk '{print $1}')"
    fi
    printf '%s\0%s\0%s\0%s\0' \
      "${kind}" "${mode}" "${relative_path}" "${content_digest}"
  done |
  sha256sum | awk '{print $1}'
