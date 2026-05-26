#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_VERSION:?RELEASE_VERSION is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"

repo="${GITHUB_REPOSITORY:-cagedbird043/sing-box}"
tag="v${RELEASE_VERSION}"
dist="${RELEASE_DIST:-dist/release}"
title="${RELEASE_VERSION}"
notes="${dist}/RELEASE-NOTES.md"
release_prerelease="${RELEASE_PRERELEASE:-true}"
release_draft="${RELEASE_DRAFT:-false}"
release_latest="${RELEASE_LATEST:-false}"

bool_true() {
  case "${1,,}" in
    1|true|yes|y|on) return 0 ;;
    *) return 1 ;;
  esac
}

if [[ ! -d "${dist}" ]]; then
  echo "release asset directory does not exist: ${dist}" >&2
  exit 1
fi

mapfile -t assets < <(find "${dist}" -maxdepth 1 -type f ! -name 'RELEASE-NOTES.md' | sort)
if (( ${#assets[@]} == 0 )); then
  echo "no release assets found in ${dist}" >&2
  exit 1
fi

mkdir -p "${dist}"
cat > "${notes}" <<NOTES
Native subscription-provider build from the cagedbird upstream-tracking fork.

- Branch: ${GITHUB_REF_NAME:-unknown}
- Commit: ${GITHUB_SHA}
- Tag: ${tag}
- Release type: prerelease
- Latest marker: disabled
- Android signing: repository LOCAL_PROPERTIES secret when configured; otherwise an ephemeral CI key, which is installable but not stable for upgrades across CI runs.

## Assets
NOTES

for asset in "${assets[@]}"; do
  printf -- '- `%s`\n' "$(basename "${asset}")" >> "${notes}"
done

# Keep the tag anchored to the exact fork commit that produced these assets.
git tag -f "${tag}" "${GITHUB_SHA}"
git push --force origin "refs/tags/${tag}:refs/tags/${tag}"

if gh release view "${tag}" --repo "${repo}" >/dev/null 2>&1; then
  edit_flags=(
    --repo "${repo}"
    --title "${title}"
    --notes-file "${notes}"
  )
  if bool_true "${release_prerelease}"; then
    edit_flags+=(--prerelease)
  fi
  if bool_true "${release_draft}"; then
    edit_flags+=(--draft)
  fi
  gh release edit "${tag}" "${edit_flags[@]}"
  gh release upload "${tag}" --repo "${repo}" --clobber "${assets[@]}"
else
  create_flags=(
    --repo "${repo}"
    --target "${GITHUB_SHA}"
    --title "${title}"
    --notes-file "${notes}"
  )
  if bool_true "${release_prerelease}"; then
    create_flags+=(--prerelease)
  fi
  if bool_true "${release_draft}"; then
    create_flags+=(--draft)
  fi
  if ! bool_true "${release_latest}"; then
    create_flags+=(--latest=false)
  fi
  gh release create "${tag}" "${assets[@]}" "${create_flags[@]}"
fi

echo "Published ${tag} with ${#assets[@]} assets."
