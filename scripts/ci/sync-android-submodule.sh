#!/usr/bin/env bash
set -euo pipefail

android_dir="${CAGEDBIRD_ANDROID_DIR:-clients/android}"
android_branch="${CAGEDBIRD_ANDROID_BRANCH:-fix/per-app-proxy-vpn-builder}"
official_remote="${CAGEDBIRD_ANDROID_UPSTREAM_REMOTE:-upstream}"
official_url="${CAGEDBIRD_ANDROID_UPSTREAM_URL:-https://github.com/SagerNet/sing-box-for-android.git}"
official_ref="${CAGEDBIRD_ANDROID_UPSTREAM_REF:-dev}"
target_commit="${CAGEDBIRD_ANDROID_TARGET_COMMIT:-}"
fork_remote="${CAGEDBIRD_ANDROID_FORK_REMOTE:-origin}"

if [[ ! -d "${android_dir}/.git" && ! -f "${android_dir}/.git" ]]; then
  git submodule update --init --recursive "${android_dir}"
fi

if ! git -C "${android_dir}" remote get-url "${official_remote}" >/dev/null 2>&1; then
  git -C "${android_dir}" remote add "${official_remote}" "${official_url}"
fi

git -C "${android_dir}" fetch "${official_remote}" "${official_ref}" --tags
git -C "${android_dir}" fetch "${fork_remote}" "${android_branch}" || true

if [[ -z "${target_commit}" ]]; then
  core_version="$(scripts/ci/resolve-cagedbird-version.sh)"
  core_base="${core_version%%-cagedbird.*}"
  while read -r commit; do
    version="$(git -C "${android_dir}" show "${commit}:version.properties" 2>/dev/null | awk -F= '$1 == "VERSION_NAME" {print $2}' | tail -n1 || true)"
    if [[ "${version}" == "${core_base}" ]]; then
      target_commit="${commit}"
      break
    fi
  done < <(git -C "${android_dir}" rev-list --max-count=200 "${official_remote}/${official_ref}")
  if [[ -z "${target_commit}" ]]; then
    echo "Could not find Android ${official_ref} commit matching core base ${core_base}" >&2
    exit 1
  fi
  echo "Using Android baseline $(git -C "${android_dir}" rev-parse --short "${target_commit}") for core ${core_base}."
else
  echo "Using explicit Android baseline $(git -C "${android_dir}" rev-parse --short "${target_commit}")."
fi

if git -C "${android_dir}" show-ref --verify --quiet "refs/heads/${android_branch}"; then
  git -C "${android_dir}" switch "${android_branch}"
else
  git -C "${android_dir}" switch -c "${android_branch}" "${fork_remote}/${android_branch}"
fi

git -C "${android_dir}" rebase "${target_commit}"
scripts/ci/check-android-submodule-sync.sh

if [[ "${CAGEDBIRD_PUSH_ANDROID_BRANCH:-0}" == "1" ]]; then
  git -C "${android_dir}" push --force-with-lease "${fork_remote}" "${android_branch}"
fi

echo "Android submodule synced to $(git -C "${android_dir}" rev-parse --short HEAD)."
echo "Parent repository now has an updated ${android_dir} gitlink; commit it in the parent repo."
