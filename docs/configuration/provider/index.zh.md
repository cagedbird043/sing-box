---
icon: material/new-box
---

!!! question "cagedbird 分支扩展"

    出站提供者是 cagedbird 分支维护的原生内核扩展。
    除非以后合入上游，否则它不是官方上游 sing-box 的可用字段。

# 提供者

提供者用于从订阅配置中加载出站和端点定义，并把这些成员交给 [Selector](/zh/configuration/outbound/selector/) 和 [URLTest](/zh/configuration/outbound/urltest/) 等组出站使用。

生成的出站和端点标签会带上提供者标签前缀：

```text
<provider-tag>/<subscription-node-tag>
```

例如，标签为 `sub` 的提供者中有一个标签为 `hk-01` 的节点，则生成的可选标签是 `sub/hk-01`。

### 结构

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

### 提供者类型

| 类型     | 描述             |
|----------|------------------|
| `remote` | 下载远程订阅链接。 |
| `local`  | 加载本地订阅文件。 |
| `inline` | 内联定义提供者成员。 |

### 支持的订阅格式

提供者会按顺序尝试以下解析器：

| 格式 | 描述 |
|------|------|
| sing-box JSON | 包含 `outbounds` 和/或 `endpoints` 的 JSON 文档。解析提供者文档时会忽略 `selector`、`urltest`、`direct`、`block`、`dns` 等组出站或特殊出站。 |
| Clash YAML | 包含 `proxies` 的 Clash 兼容文档。支持的代理类型包括 Shadowsocks、VMess、VLESS、Trojan、SOCKS5、HTTP、TUIC、Hysteria、Hysteria2、AnyTLS、SSH 和 WireGuard。 |
| SIP008 | Shadowsocks SIP008 JSON 文档。 |
| Raw links | 明文或 base64 编码的分享链接列表，支持 `ss`、`vmess`、`trojan`、`vless`、`hysteria`、`hy2`/`hysteria2`、`tuic` 和 `anytls`。 |

### 通用字段

#### type

==必填==

提供者类型。

#### tag

==必填==

提供者标签。

提供者成员标签会使用这个标签作为前缀生成。

### Remote 字段

#### url

==必填==

订阅链接。

#### path

本地缓存路径。

设置后，下载到的订阅会先被解析，再保存为 sing-box JSON 形式的提供者缓存。启动时，提供者可以先从这个缓存恢复，再等待下一次远程更新成功。

#### user_agent

下载订阅时使用的 HTTP `User-Agent`。

留空时使用 `sing-box <version>`。

#### http_client

下载订阅时使用的 HTTP 客户端。

字符串值表示引用顶层 [HTTP 客户端](/zh/configuration/shared/http-client/) 标签；对象值表示内联 HTTP 客户端。

!!! note "废弃兼容字段"

    旧的 `download_detour` 字段仍为兼容保留，但已经废弃。请使用 `http_client`。

#### update_interval

远程更新间隔。

留空时使用 `24h`。小于 `1h` 的值会被提升到 `1h`。

#### include

正则表达式。只保留原始订阅标签匹配该表达式的出站或端点。

#### exclude

正则表达式。丢弃原始订阅标签匹配该表达式的出站或端点。

#### health_check

参阅 [健康检查字段](#健康检查字段)。

#### override_dialer

覆盖解析出的提供者成员的拨号选项。

只会应用非空覆盖字段。如果解析出的成员 detour 到同一提供者中的另一个成员，该 detour 会被改写成带提供者前缀的生成标签。

#### override_tls

覆盖解析出的、使用 TLS 的提供者成员的出站 TLS 选项。

只会应用非空覆盖字段。

### Local 字段

#### path

==必填==

本地订阅文件路径。

该文件会在启动时加载，并被监听变更。

#### health_check

参阅 [健康检查字段](#健康检查字段)。

#### override_dialer

同 [Remote 字段](#override_dialer)。

#### override_tls

同 [Remote 字段](#override_tls)。

### Inline 字段

#### outbounds

内联出站定义。

#### endpoints

内联端点定义。

#### health_check

参阅 [健康检查字段](#健康检查字段)。

### 健康检查字段

#### enabled

启用提供者成员健康检查。

#### url

用于测试提供者成员的 URL。

#### interval

健康检查间隔。留空时使用 `10m`。小于 `1m` 的值会被提升到 `1m`。

#### timeout

单个成员的健康检查超时。留空时使用 `3s`。

### 在组出站中使用提供者

常规用法是把提供者加入 `selector` 或 `urltest` 组，再把流量路由到组出站：

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
