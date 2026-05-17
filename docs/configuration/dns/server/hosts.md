---
icon: material/new-box
---

!!! question "Since sing-box 1.12.0"

# Hosts

### Structure

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

    You can ignore the JSON Array [] tag when the content is only one item

### Fields

#### path

List of paths to hosts files.

`/etc/hosts` is used by default.

`C:\Windows\System32\Drivers\etc\hosts` is used by default on Windows.

Example:

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

Predefined hosts.

Example:

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

!!! question "Since sing-box 1.14.0-cagedbird"

Remote hosts sources.

Downloaded provider contents are saved to local cache files and parsed in the same way as files from [path](#path). If a provider update fails, the previous cache file is kept and sing-box continues to use it.

Only standard hosts file syntax is supported:

```text
142.251.111.188 mtalk.google.com
::1 localhost
```

AdGuard, wildcard and Clash rule syntaxes are not supported by hosts providers.

Structure:

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

Fields:

- `type`: Provider type. Only `remote` is supported.
- `tag`: Provider tag used in logs.
- `url`: Remote hosts URL.
- `path`: Local cache path. The downloaded hosts file is saved here.
- `user_agent`: HTTP User-Agent. Defaults to `sing-box <version>`.
- `http_client`: HTTP client used for downloading.
- `update_interval`: Update interval. Defaults to `24h`, minimum `1h`.
- `required`: If enabled, sing-box fails to start when the initial download fails and no cache file exists.
- `max_size`: Maximum remote hosts file size. Defaults to `16MiB`.

Example:

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

### Examples

=== "Use hosts if available"

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
