### 结构

```json
{
  "type": "urltest",
  "tag": "auto",
  
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "providers": ["subscription"],
  "use_all_providers": false,
  "include": "",
  "exclude": "",
  "url": "",
  "interval": "",
  "tolerance": 50,
  "idle_timeout": "",
  "interrupt_exist_connections": false,
  "fallback": {"enabled": false, "max_delay": "1s"}
}
```

### 字段

#### outbounds

至少指定一个出站或提供者。

用于测试的出站标签列表。

#### providers / use_all_providers / include / exclude

见[选择器的提供者成员配置](/zh/configuration/outbound/selector/#providers)。提供者刷新时自动更新成员，空组使用直连出站 `Compatible`。

#### fallback

启用 `enabled` 后，优先使用延迟不超过 `max_delay` 的第一个可用成员。如果全部可用成员超过阈值，则使用最快的成员。未填写或为零时不限制延迟。


#### url

用于测试的链接。默认使用 `https://www.gstatic.com/generate_204`。

#### interval

测试间隔。 默认使用 `3m`。

#### tolerance

以毫秒为单位的测试容差。 默认使用 `50`。

#### idle_timeout

空闲超时。默认使用 `30m`。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。