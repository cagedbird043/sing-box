#!/usr/bin/env bash
set -euo pipefail

android_dir="${CAGEDBIRD_ANDROID_DIR:-clients/android}"
android_branch="${CAGEDBIRD_ANDROID_BRANCH:-fix/per-app-proxy-vpn-builder}"
official_remote="${CAGEDBIRD_ANDROID_UPSTREAM_REMOTE:-upstream}"
official_url="${CAGEDBIRD_ANDROID_UPSTREAM_URL:-https://github.com/SagerNet/sing-box-for-android.git}"
official_ref="${CAGEDBIRD_ANDROID_UPSTREAM_REF:-dev}"
fork_remote="${CAGEDBIRD_ANDROID_FORK_REMOTE:-origin}"

if [[ ! -d "${android_dir}/.git" && ! -f "${android_dir}/.git" ]]; then
  git submodule update --init --recursive "${android_dir}"
fi

if ! git -C "${android_dir}" remote get-url "${official_remote}" >/dev/null 2>&1; then
  git -C "${android_dir}" remote add "${official_remote}" "${official_url}"
fi

git -C "${android_dir}" fetch "${official_remote}" "${official_ref}" --tags
git -C "${android_dir}" fetch "${fork_remote}" "${android_branch}" || true

if git -C "${android_dir}" show-ref --verify --quiet "refs/heads/${android_branch}"; then
  git -C "${android_dir}" switch "${android_branch}"
else
  git -C "${android_dir}" switch -c "${android_branch}" "${fork_remote}/${android_branch}"
fi

git -C "${android_dir}" rebase "${official_remote}/${official_ref}"
scripts/ci/check-android-submodule-sync.sh

if [[ "${CAGEDBIRD_PUSH_ANDROID_BRANCH:-0}" == "1" ]]; then
  git -C "${android_dir}" push --force-with-lease "${fork_remote}" "${android_branch}"
fi

echo "Android submodule synced to $(git -C "${android_dir}" rev-parse --short HEAD)."
echo "Parent repository now has an updated ${android_dir} gitlink; commit it in the parent repo."
