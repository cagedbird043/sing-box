#!/usr/bin/env bash
# Usage: verify-cagedbird-artifact.sh <artifact-id> <downloaded-dir> <commit> <version>
# Checks that a downloaded artifact directory was built from <commit> as <version>.
set -euo pipefail

artifact_id="${1:?artifact id is required}"
dir="${2:?artifact directory is required}"
commit="${3:?commit is required}"
version="${4:?version is required}"

fail() {
  echo "${artifact_id}: $*" >&2
  exit 1
}

build_info_value() {
  sed -n "s/^$1=//p" <<<"${build_info}" | head -n 1
}

case "${artifact_id}" in
  sing-box-cagedbird-android-apk)
    [[ -f "${dir}/BUILD-INFO.txt" ]] || fail "missing BUILD-INFO.txt"
    [[ -f "${dir}/SFA-version.properties" ]] || fail "missing SFA-version.properties"
    [[ -f "${dir}/libbox.aar" ]] || fail "missing libbox.aar"
    compgen -G "${dir}/*.apk" >/dev/null || fail "missing APK"
    build_info="$(cat "${dir}/BUILD-INFO.txt")"
    [[ "$(build_info_value signing)" == "secret-stable-key" ]] || fail "APK is not signed with the stable key"
    ;;
  sing-box-cagedbird-*)
    tarball="${dir}/${artifact_id}.tar.gz"
    [[ -f "${tarball}" ]] || fail "missing ${artifact_id}.tar.gz"
    build_info="$(tar -xzOf "${tarball}" "${artifact_id}/BUILD-INFO.txt")" || fail "missing BUILD-INFO.txt in tarball"
    tar -tzf "${tarball}" "${artifact_id}/sing-box" >/dev/null || fail "missing sing-box binary in tarball"
    ;;
  *)
    fail "unknown artifact"
    ;;
esac

[[ "$(build_info_value commit)" == "${commit}" ]] || fail "commit $(build_info_value commit) != ${commit}"
[[ "$(build_info_value version)" == "${version}" ]] || fail "version $(build_info_value version) != ${version}"
