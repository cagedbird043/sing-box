#!/usr/bin/env bash
# Decides, per release artifact, whether an earlier run already built it for this commit.
#
# Searches Cagedbird Binaries, Cagedbird Android APK and earlier Cagedbird Release runs
# with the same head SHA, newest first, regardless of each run's overall conclusion.
# An unexpired artifact counts only after it is downloaded and verified against the
# commit and version. Everything else is listed for rebuilding.
#
# Env: GH_TOKEN, REPO (owner/name), HEAD_SHA, RELEASE_VERSION, optional CURRENT_RUN_ID.
# Writes reuse ({artifact: run_id}), build ([artifact]) and provider_core (true/false)
# to GITHUB_OUTPUT when set, and a table to GITHUB_STEP_SUMMARY when set.
set -euo pipefail

: "${REPO:?REPO is required}"
: "${HEAD_SHA:?HEAD_SHA is required}"
: "${RELEASE_VERSION:?RELEASE_VERSION is required}"
current_run_id="${CURRENT_RUN_ID:-}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
artifacts=(
  sing-box-cagedbird-linux-amd64
  sing-box-cagedbird-linux-amd64-v4
  sing-box-cagedbird-linux-arm64
  sing-box-cagedbird-android-arm64
  sing-box-cagedbird-android-amd64
  sing-box-cagedbird-darwin-arm64
  sing-box-cagedbird-android-apk
)

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

# id<TAB>workflow file, newest first; the current run is excluded.
runs="$(gh api --paginate "repos/${REPO}/actions/runs?head_sha=${HEAD_SHA}&per_page=100" \
  --jq '.workflow_runs[] | [.id, .created_at, (.path | sub("@.*$"; "") | sub("^.*/"; ""))] | @tsv' \
  | sort -t $'\t' -k2,2r | awk -F '\t' -v cur="${current_run_id}" '$1 != cur { print $1 "\t" $3 }')"

artifact_runs="$(awk -F '\t' '$2 == "cagedbird-binaries.yml" || $2 == "cagedbird-android-apk.yml" || $2 == "cagedbird-release.yml" { print $1 }' <<<"${runs}")"

declare -A run_artifacts=()
list_run_artifacts() {
  local run_id="$1"
  if [[ -z "${run_artifacts[${run_id}]+set}" ]]; then
    run_artifacts["${run_id}"]="$(gh api --paginate "repos/${REPO}/actions/runs/${run_id}/artifacts?per_page=100" \
      --jq '.artifacts[] | select(.expired | not) | .name')"
  fi
  printf '%s\n' "${run_artifacts[${run_id}]}"
}

reuse='{}'
build='[]'
summary_rows=()
for artifact in "${artifacts[@]}"; do
  source_run=""
  for run_id in ${artifact_runs}; do
    grep -qxF "${artifact}" <(list_run_artifacts "${run_id}") || continue
    dest="${work_dir}/${run_id}/${artifact}"
    if ! gh run download "${run_id}" -R "${REPO}" -n "${artifact}" -D "${dest}" >/dev/null 2>&1; then
      echo "${artifact}: download from run ${run_id} failed; trying older runs" >&2
      continue
    fi
    if "${script_dir}/verify-cagedbird-artifact.sh" "${artifact}" "${dest}" "${HEAD_SHA}" "${RELEASE_VERSION}"; then
      source_run="${run_id}"
      rm -rf "${dest}"
      break
    fi
    rm -rf "${dest}"
  done
  if [[ -n "${source_run}" ]]; then
    reuse="$(jq -c --arg a "${artifact}" --arg r "${source_run}" '. + {($a): ($r | tonumber)}' <<<"${reuse}")"
    summary_rows+=("| \`${artifact}\` | reused from [run ${source_run}](https://github.com/${REPO}/actions/runs/${source_run}) |")
  else
    build="$(jq -c --arg a "${artifact}" '. + [$a]' <<<"${build}")"
    summary_rows+=("| \`${artifact}\` | build in this run |")
  fi
done

# Provider core checks: skip when a Core run or an earlier Release provider-core job passed.
provider_core=true
provider_core_source=""
while IFS=$'\t' read -r run_id workflow; do
  [[ -n "${run_id}" ]] || continue
  case "${workflow}" in
    cagedbird-core.yml)
      conclusion="$(gh api "repos/${REPO}/actions/runs/${run_id}" --jq '.conclusion // ""')"
      [[ "${conclusion}" == "success" ]] || continue
      ;;
    cagedbird-release.yml)
      gh api --paginate "repos/${REPO}/actions/runs/${run_id}/jobs?per_page=100" \
        --jq '.jobs[] | select(.name == "Provider core checks" and .conclusion == "success") | .id' \
        | grep -q . || continue
      ;;
    *)
      continue
      ;;
  esac
  provider_core=false
  provider_core_source="${run_id}"
  break
done <<<"${runs}"

if [[ "${provider_core}" == "true" ]]; then
  summary_rows+=("| provider core checks | run in this run |")
else
  summary_rows+=("| provider core checks | passed in [run ${provider_core_source}](https://github.com/${REPO}/actions/runs/${provider_core_source}) |")
fi

echo "reuse=${reuse}"
echo "build=${build}"
echo "provider_core=${provider_core}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "reuse=${reuse}"
    echo "build=${build}"
    echo "provider_core=${provider_core}"
  } >> "${GITHUB_OUTPUT}"
fi
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "### Artifact plan for \`${RELEASE_VERSION}\`"
    echo
    echo "| Item | Source |"
    echo "|---|---|"
    printf '%s\n' "${summary_rows[@]}"
  } >> "${GITHUB_STEP_SUMMARY}"
fi
