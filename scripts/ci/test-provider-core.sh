#!/usr/bin/env bash
set -euo pipefail

git diff --check

ldflags="$(< release/LDFLAGS)"

go test -ldflags "${ldflags}" ./provider/... ./adapter/... ./protocol/group/... ./daemon/... ./service/api ./experimental/cachefile ./option ./schema ./route ./common/interrupt

go list ./... \
  | grep -Ev '^github.com/sagernet/sing-box/common/netns$' \
  | xargs -r go test -ldflags "${ldflags}"

scripts/ci/check-provider-sample.sh
