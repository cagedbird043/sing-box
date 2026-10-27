---
icon: material/new-box
---

!!! question "Cagedbird fork extension"

    Outbound providers are maintained by the cagedbird fork as a native core extension.
    They are not available in official upstream sing-box unless the feature is merged upstream later.

# Provider

Providers load outbound and endpoint definitions from subscription profiles and expose them to group outbounds such as [Selector](/configuration/outbound/selector/) and [URLTest](/configuration/outbound/urltest/).

Generated outbound and endpoint tags are prefixed with the provider tag:

```text
<provider-tag>/<subscription-node-tag>
```

For example, a provider tagged `sub` containing a node tagged `hk-01` creates the selectable tag `sub/hk-01`.

### Structure

```json
{
  "providers": [
    {
      "type": "remote",
      "tag": "sub",
      "url": "https://example.com/clash.yaml",
      "path": "./sub-cache.json",
      "user_agent": "clash.meta",
      "http_client": "download-direct",
      "update_interval": "12h",
      "include": "",
      "exclude": "",
      "health_check": {
        "enabled": false,
        "url": "https://www.gstatic.com/generate_204",
        "interval": "10m",
        "timeout": "3s"
      },
      "override_dialer": {},
      "override_tls": {}
    }
  ],
  "outbounds": [
    {
      "type": "selector",
      "tag": "select",
      "providers": [
        "sub"
      ]
    }
  ]
}
```

### Provider Types

| Type     | Description                         |
|----------|-------------------------------------|
| `remote` | Download a remote subscription URL. |
| `local`  | Load a local subscription file.     |
| `inline` | Define provider members inline.     |

### Supported subscription formats

Providers try the following parsers in order:

| Format | Description |
|--------|-------------|
| sing-box JSON | A JSON document containing `outbounds` and/or `endpoints`. Group and special outbounds such as `selector`, `urltest`, `direct`, `block`, and `dns` are ignored when parsing a provider document. |
| Clash YAML | A Clash-compatible document containing `proxies`. Supported proxy types include Shadowsocks, VMess, VLESS, Trojan, SOCKS5, HTTP, TUIC, Hysteria, Hysteria2, AnyTLS, SSH, and WireGuard. |
| SIP008 | A Shadowsocks SIP008 JSON document. |
| Raw links | A plain or base64-encoded line list of supported share links, including `ss`, `vmess`, `trojan`, `vless`, `hysteria`, `hy2`/`hysteria2`, `tuic`, and `anytls`. |

### Common Fields

#### type

==Required==

The provider type.

#### tag

==Required==

The provider tag.

Provider member tags are generated with this tag as a prefix.

### Remote Fields

#### url

==Required==

The subscription URL.

#### path

The local cache path for the parsed provider document.

If set, the downloaded subscription is parsed and saved as a sing-box JSON provider cache. On startup, the provider can restore from this cache before the next successful remote update.

#### user_agent

The HTTP `User-Agent` used to download the subscription.

`sing-box <version>` is used if empty.

#### http_client

The HTTP client used to download the subscription.

A string value references a top-level [HTTP Client](/configuration/shared/http-client/) tag. An object value defines an inline HTTP client.

!!! note "Deprecated compatibility"

    The legacy `download_detour` field is still accepted for compatibility, but it is deprecated. Use `http_client` instead.

#### update_interval

The remote update interval.

`24h` is used if empty. Values below `1h` are raised to `1h`.

#### include

A regular expression. Only parsed outbounds/endpoints whose original subscription tag matches this expression are kept.

#### exclude

A regular expression. Parsed outbounds/endpoints whose original subscription tag matches this expression are dropped.

#### health_check

See [Health Check Fields](#health-check-fields).

#### override_dialer

Override dialer options for parsed provider members.

Only non-empty override fields are applied. If a parsed member detours to another member from the same provider, the detour is rewritten to the generated provider tag form.

#### override_tls

Override outbound TLS options for parsed provider members that use TLS.

Only non-empty override fields are applied.

### Local Fields

#### path

==Required==

The local subscription file path.

The file is loaded on startup and watched for changes.

#### health_check

See [Health Check Fields](#health-check-fields).

#### override_dialer

Same as [Remote Fields](#override_dialer).

#### override_tls

Same as [Remote Fields](#override_tls).

### Inline Fields

#### outbounds

Inline outbound definitions.

#### endpoints

Inline endpoint definitions.

#### health_check

See [Health Check Fields](#health-check-fields).

### Health Check Fields

#### enabled

Enable provider member health checks.

#### url

The URL used to test provider members.

#### interval

The health check interval. `10m` is used if empty. Values below `1m` are raised to `1m`.

#### timeout

The per-member health check timeout. `3s` is used if empty.

### Using providers in groups

The usual pattern is to add providers to `selector` or `urltest` groups and route traffic to the group:

```json
{
  "providers": [
    {
      "type": "remote",
      "tag": "sub",
      "url": "https://example.com/clash.yaml",
      "path": "./sub-cache.json",
      "http_client": {
        "detour": "direct"
      }
    }
  ],
  "outbounds": [
    {
      "type": "direct",
      "tag": "direct"
    },
    {
      "type": "selector",
      "tag": "select",
      "providers": [
        "sub"
      ]
    },
    {
      "type": "urltest",
      "tag": "auto",
      "providers": [
        "sub"
      ],
      "include": "香港|HK"
    }
  ],
  "route": {
    "final": "select"
  }
}
```
