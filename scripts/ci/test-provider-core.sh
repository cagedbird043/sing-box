#!/usr/bin/env bash
set -euo pipefail

git diff --check

go test ./adapter/provider ./provider/parser ./provider/remote ./provider/local ./protocol/group ./option

go list ./... \
  | grep -Ev '^github.com/sagernet/sing-box/(common/netns|experimental/(boxdd|libbox))$' \
  | xargs -r go test

scripts/ci/check-provider-sample.sh
