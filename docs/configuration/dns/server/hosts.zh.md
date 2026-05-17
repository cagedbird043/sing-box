---
icon: material/new-box
---

!!! question "自 sing-box 1.12.0 起"

# Hosts

### 结构

```json
{
  "dns": {
    "servers": [
      {
        "type": "hosts",
        "tag": "",

        "path": [],
        "predefined": {},
        "providers": []
      }
    ]
  }
}
```

!!! note ""

    当内容只有一项时，可以忽略 JSON 数组 [] 标签

### 字段

#### path

hosts 文件路径列表。

默认使用 `/etc/hosts`。

在 Windows 上默认使用 `C:\Windows\System32\Drivers\etc\hosts`。

示例：

```json
{
  // "path": "/etc/hosts"

  "path": [
    "/etc/hosts",
    "$HOME/.hosts"
  ]
}
```

#### predefined

预定义的 hosts。

示例：

```json
{
  "predefined": {
    "www.google.com": "127.0.0.1",
    "localhost": [
      "127.0.0.1",
      "::1"
    ]
  }
}
```

#### providers

!!! question "自 sing-box 1.14.0-cagedbird 起"

远程 hosts 源。

下载后的 provider 内容会保存到本地缓存文件，并以与 [path](#path) 文件相同的方式解析。如果 provider 更新失败，旧缓存文件会被保留，sing-box 会继续使用旧缓存。

仅支持标准 hosts 文件语法：

```text
142.251.111.188 mtalk.google.com
::1 localhost
```

hosts providers 不支持 AdGuard、通配符或 Clash 规则语法。

结构：

```json
{
  "type": "remote",
  "tag": "",
  "url": "",
  "path": "",
  "user_agent": "",
  "http_client": {},
  "update_interval": "24h",
  "required": false,
  "max_size": "16MiB"
}
```

字段：

- `type`：Provider 类型。目前仅支持 `remote`。
- `tag`：用于日志输出的 provider 标签。
- `url`：远程 hosts URL。
- `path`：本地缓存路径。下载后的 hosts 文件会保存到这里。
- `user_agent`：HTTP User-Agent。默认值为 `sing-box <version>`。
- `http_client`：用于下载的 HTTP 客户端。
- `update_interval`：更新间隔。默认 `24h`，最小 `1h`。
- `required`：启用后，如果首次下载失败且没有缓存文件，sing-box 启动失败。
- `max_size`：远程 hosts 文件最大大小。默认 `16MiB`。

示例：

```json
{
  "providers": [
    {
      "type": "remote",
      "tag": "fcm-hosts-next",
      "url": "https://miceworld.top/fcm-hosts-next/fcm_dual.hosts",
      "path": "cache/hosts/fcm_dual.hosts",
      "update_interval": "3h",
      "http_client": {
        "detour": "proxy"
      }
    }
  ]
}
```

### 示例

=== "如果可用则使用 hosts"

    ```json
    {
      "dns": {
        "servers": [
          {
            ...
          },
          {
            "type": "hosts",
            "tag": "hosts"
          }
        ],
        "rules": [
          {
            "ip_accept_any": true,
            "server": "hosts"
          }
        ]
      }
    }
    ```
        }
        ```
