#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${CAGEDBIRD_VERSION:-}" && "${CAGEDBIRD_VERSION}" != "auto" ]]; then
  printf '%s\n' "${CAGEDBIRD_VERSION}"
  exit 0
fi

base_tag="$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*cagedbird*' 2>/dev/null || true)"
if [[ -z "${base_tag}" ]]; then
  base="0.0.0"
else
  base="${base_tag#v}"
fi
short_commit="$(git rev-parse --short HEAD)"
printf '%s-cagedbird.%s\n' "${base}" "${short_commit}"
