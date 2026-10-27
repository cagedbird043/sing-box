### 结构

```json
{
  "type": "selector",
  "tag": "select",

  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "providers": ["subscription"],
  "use_all_providers": false,
  "include": "",
  "exclude": "",
  "default": "proxy-c",
  "interrupt_exist_connections": false
}
```

!!! quote ""

    选择器目前只能通过 [Clash API](/zh/configuration/experimental/clash-api/) 来控制。

### 字段

#### outbounds

至少指定一个出站或提供者。

用于选择的出站标签列表。

#### providers

[出站提供者](/zh/configuration/provider/) 标签列表。订阅刷新时自动更新成员。

#### use_all_providers

使用全部已配置的出站提供者。

#### include / exclude

匹配提供者成员标签（`provider/tag`）的正则表达式，排除优先。不会过滤静态 `outbounds`。

提供者组为空时使用直连出站 `Compatible`。


#### default

默认的出站标签。默认使用第一个出站。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。