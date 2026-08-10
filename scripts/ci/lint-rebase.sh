#!/usr/bin/env bash
# Post-rebase lint: catches submodule drift and common rebase mistakes.
# Run before pushing cagedbird/alpha.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

FAIL=0

check_submodule() {
  local sm_path="$1"
  local upstream_tag="${2:-$(git describe --tags --abbrev=0 upstream/release)}"

  local head_commit
  head_commit=$(git ls-tree HEAD "$sm_path" | awk '{print $3}')
  local tag_commit
  tag_commit=$(git ls-tree "$upstream_tag" "$sm_path" | awk '{print $3}')

  if [ "$head_commit" != "$tag_commit" ]; then
    echo "FAIL: submodule $sm_path drifted from upstream tag $upstream_tag"
    echo "  HEAD:   $head_commit"
    echo "  $upstream_tag: $tag_commit"
    echo "  Fix: git checkout $upstream_tag -- $sm_path"
    FAIL=1
  else
    echo "OK:   $sm_path ($head_commit)"
  fi
}

echo "=== submodule drift check (vs upstream tag) ==="
check_submodule clients/android
check_submodule clients/apple

exit $FAIL
