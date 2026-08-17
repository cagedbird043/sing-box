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
  "providers": [
    "sub"
  ],
  "exclude": "",
  "include": "",
  "use_all_providers": false,
  "url": "",
  "interval": "",
  "tolerance": 50,
  "idle_timeout": "",
  "interrupt_exist_connections": false
}
```

### 字段

#### outbounds

用于测试的出站标签列表。

`outbounds`、`providers` 或 `use_all_providers` 至少需要提供一个测试成员。

#### providers

要加入 URLTest 组的 [提供者](/zh/configuration/provider/) 标签列表。

提供者成员标签会生成为 `<provider-tag>/<member-tag>`。

#### exclude

正则表达式。匹配该表达式的提供者生成出站标签会被排除，不参与 URL 测试。

#### include

正则表达式。非空时，只有匹配该表达式的提供者生成出站标签会参与 URL 测试。

#### use_all_providers

把所有已配置的提供者加入 URLTest 组。

启用后，启动时会用当前顶层提供者列表替代 `providers`。

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