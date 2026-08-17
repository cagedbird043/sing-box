#!/usr/bin/env bash
set -euo pipefail

workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT

cat > "${workdir}/provider-sample.json" <<'JSON'
{
  "log": {
    "disabled": true
  },
  "providers": [
    {
      "type": "inline",
      "tag": "sub",
      "outbounds": [
        {
          "type": "http",
          "tag": "local-http",
          "server": "127.0.0.1",
          "server_port": 8080
        }
      ],
      "health_check": {
        "enabled": false
      }
    }
  ],
  "outbounds": [
    {
      "type": "selector",
      "tag": "select",
      "providers": [
        "sub"
      ]
    },
    {
      "type": "direct",
      "tag": "direct"
    }
  ],
  "route": {
    "final": "select"
  }
}
JSON

go run ./cmd/sing-box check -D "${workdir}" -c "${workdir}/provider-sample.json"
