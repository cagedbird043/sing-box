#!/usr/bin/env bash
set -euo pipefail

android_dir="${CAGEDBIRD_ANDROID_DIR:-clients/android}"
official_url="${CAGEDBIRD_ANDROID_UPSTREAM_URL:-https://github.com/SagerNet/sing-box-for-android.git}"
official_ref="${CAGEDBIRD_ANDROID_UPSTREAM_REF:-dev}"

if [[ ! -d "${android_dir}/.git" && ! -f "${android_dir}/.git" ]]; then
  echo "Android submodule is not initialized at ${android_dir}" >&2
  echo "Run: git submodule update --init --recursive ${android_dir}" >&2
  exit 1
fi

current_commit="$(git -C "${android_dir}" rev-parse HEAD)"
current_short="$(git -C "${android_dir}" rev-parse --short HEAD)"
core_version="$(scripts/ci/resolve-cagedbird-version.sh)"
core_base="${core_version%%-cagedbird.*}"
android_version="$(awk -F= '$1 == "VERSION_NAME" {print $2}' "${android_dir}/version.properties" | tail -n1)"
android_base="${android_version%%-cagedbird.*}"

echo "Android submodule commit: ${current_short}"
echo "Core release base: ${core_base}"
echo "Android VERSION_NAME: ${android_version}"
echo "Checking Android submodule against ${official_url} ${official_ref}"

git -C "${android_dir}" fetch --quiet --depth=200 "${official_url}" "${official_ref}"
official_head="$(git -C "${android_dir}" rev-parse FETCH_HEAD)"
official_head_short="$(git -C "${android_dir}" rev-parse --short FETCH_HEAD)"
official_head_version="$(git -C "${android_dir}" show "${official_head}:version.properties" | awk -F= '$1 == "VERSION_NAME" {print $2}' | tail -n1)"
echo "Official Android ${official_ref} head: ${official_head_short} (${official_head_version})"

required_commit=""
while read -r commit; do
  version="$(git -C "${android_dir}" show "${commit}:version.properties" 2>/dev/null | awk -F= '$1 == "VERSION_NAME" {print $2}' | tail -n1 || true)"
  if [[ "${version}" == "${core_base}" ]]; then
    required_commit="${commit}"
    break
  fi
done < <(git -C "${android_dir}" rev-list --max-count=200 FETCH_HEAD)

if [[ -z "${required_commit}" ]]; then
  cat >&2 <<MSG
Could not find an official Android ${official_ref} commit with VERSION_NAME=${core_base}
within the fetched history. Increase fetch depth or update the Android baseline manually.
MSG
  exit 1
fi
required_short="$(git -C "${android_dir}" rev-parse --short "${required_commit}")"
echo "Required official Android baseline for core ${core_base}: ${required_short}"

if ! git -C "${android_dir}" merge-base --is-ancestor "${required_commit}" "${current_commit}"; then
  cat >&2 <<MSG
Android submodule is behind the required official Android baseline for this core version.

Current submodule: ${current_short}
Required official baseline: ${required_short} (${core_base})
Official ${official_ref} head: ${official_head_short} (${official_head_version})

Run: CAGEDBIRD_ANDROID_TARGET_COMMIT=${required_commit} scripts/ci/sync-android-submodule.sh
then update the parent repository submodule pointer.
MSG
  exit 1
fi

if [[ -n "${android_base}" && "${android_base}" != "${core_base}" ]]; then
  cat >&2 <<MSG
Android app version baseline does not match core release baseline.

Core release base: ${core_base}
Android VERSION_NAME: ${android_version}

Update clients/android to the matching upstream Android app baseline before release.
MSG
  exit 1
fi

echo "Android submodule is synced: ${current_short} contains official baseline ${required_short}, version base ${core_base}."
