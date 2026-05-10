#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${CAGEDBIRD_VERSION:-}" && "${CAGEDBIRD_VERSION}" != "auto" ]]; then
  printf '%s\n' "${CAGEDBIRD_VERSION}"
  exit 0
fi

find_base_tag() {
  git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*cagedbird*' 2>/dev/null || true
}

base_tag="$(find_base_tag)"
if [[ -z "${base_tag}" && "${CAGEDBIRD_FETCH_UPSTREAM_TAGS:-1}" != "0" ]]; then
  upstream_url="${CAGEDBIRD_UPSTREAM_URL:-https://github.com/SagerNet/sing-box.git}"
  git fetch --quiet --tags "${upstream_url}" >/dev/null 2>&1 || true
  base_tag="$(find_base_tag)"
fi

if [[ -z "${base_tag}" ]]; then
  base="0.0.0"
else
  base="${base_tag#v}"
fi
short_commit="$(git rev-parse --short HEAD)"
printf '%s-cagedbird.%s\n' "${base}" "${short_commit}"
