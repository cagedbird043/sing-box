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
  "url": "",
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

#### url

用于显示和手动测试成员延迟的链接。默认使用 `https://www.gstatic.com/generate_204`。

成员延迟只显示本组目标的测速结果，不会被其他组针对不同目标的测速覆盖。手动测试本组时，成员按本组目标测速，同时按包含它的各 URL-test 组的目标测速，测完这些组重新选路。不影响选择器的选路。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。