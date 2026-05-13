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

echo "Android submodule commit: ${current_short}"
echo "Checking Android submodule contains ${official_url} ${official_ref}"

git -C "${android_dir}" fetch --quiet --depth=200 "${official_url}" "${official_ref}"
official_commit="$(git -C "${android_dir}" rev-parse FETCH_HEAD)"
official_short="$(git -C "${android_dir}" rev-parse --short FETCH_HEAD)"

echo "Official Android ${official_ref}: ${official_short}"

if ! git -C "${android_dir}" merge-base --is-ancestor "${official_commit}" "${current_commit}"; then
  cat >&2 <<MSG
Android submodule is behind official ${official_ref}.

Current submodule: ${current_short}
Official ${official_ref}: ${official_short}

Update the Android fork branch by rebasing/cherry-picking local fixes on top of
${official_url} ${official_ref}, then update the parent repository submodule pointer.
MSG
  exit 1
fi

core_version="$(scripts/ci/resolve-cagedbird-version.sh)"
core_base="${core_version%%-cagedbird.*}"
android_version="$(awk -F= '$1 == "VERSION_NAME" {print $2}' "${android_dir}/version.properties" | tail -n1)"
android_base="${android_version%%-cagedbird.*}"

if [[ -n "${android_base}" && "${android_base}" != "${core_base}" ]]; then
  cat >&2 <<MSG
Android app version baseline does not match core release baseline.

Core release base: ${core_base}
Android VERSION_NAME: ${android_version}

Update clients/android to the matching upstream Android app baseline before release.
MSG
  exit 1
fi

echo "Android submodule is synced: ${current_short} contains official ${official_ref} ${official_short}, version base ${core_base}."
