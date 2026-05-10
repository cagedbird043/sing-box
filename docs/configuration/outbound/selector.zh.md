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
  "providers": [
    "sub"
  ],
  "exclude": "",
  "include": "",
  "use_all_providers": false,
  "default": "proxy-c",
  "interrupt_exist_connections": false
}
```

!!! quote ""

    选择器目前只能通过 [Clash API](/zh/configuration/experimental/clash-api/) 来控制。

### 字段

#### outbounds

用于选择的出站标签列表。

`outbounds`、`providers` 或 `use_all_providers` 至少需要提供一个可选成员。

#### providers

要加入选择器的 [提供者](/zh/configuration/provider/) 标签列表。

提供者成员标签会生成为 `<provider-tag>/<member-tag>`。

#### exclude

正则表达式。匹配该表达式的提供者生成出站标签会被排除。

#### include

正则表达式。非空时，只加入匹配该表达式的提供者生成出站标签。

#### use_all_providers

把所有已配置的提供者加入选择器。

启用后，启动时会用当前顶层提供者列表替代 `providers`。

#### default

默认的出站标签。默认使用第一个出站。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。