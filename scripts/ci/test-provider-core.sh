#!/usr/bin/env bash
set -euo pipefail

git diff --check

go test ./adapter/provider ./provider/parser ./provider/remote ./provider/local ./protocol/group ./option

go list ./... \
  | grep -v '^github.com/sagernet/sing-box/experimental/libbox$' \
  | xargs -r go test

scripts/ci/check-provider-sample.sh
