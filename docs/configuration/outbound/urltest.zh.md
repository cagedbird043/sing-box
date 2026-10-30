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

测速历史与自动选路按出站和实际测速 URL 隔离。同一出站参与不同目标的测速组时，结果不会互相覆盖或使对方失效。空 URL 与默认 URL 共享历史。嵌套 URL-test 组保留各自配置的测速目标。

提供者健康检查和手动 API 测速仅在目标 URL 相同时共享结果。组内成员延迟使用该组的实际目标；普通节点的最近成功延迟仅用于展示，不作为专项选路的回退。提供者节点替换或移除时清理其全部目标历史，并忽略已退役节点世代的迟到结果。

#### interval

测试间隔。 默认使用 `3m`。

#### tolerance

以毫秒为单位的测试容差。 默认使用 `50`。

#### idle_timeout

空闲超时。默认使用 `30m`。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。