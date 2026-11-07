---
icon: material/new-box
---

!!! question "自 sing-box 1.14.0 起"

# sing-box API

sing-box API 服务是用于观察与控制正在运行的 sing-box 实例的 gRPC 服务器。

它可以由 iOS、macOS 和 Android 上的 [sing-box 图形客户端](/zh/clients/)（通过 Remote Control 功能）或 [sing-box dashboard](https://github.com/SagerNet/sing-box-dashboard) 访问。

服务器同时接受 [gRPC-Web](https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-WEB.md) 请求。

### ProviderService

原生 `daemon.ProviderService` 与其他 sing-box API 方法共用监听器、传输和 Bearer 认证。
请配置非空 `secret` 以保护订阅信息。

`ListProviders`、`GetProvider` 和 `SubscribeProviders` 返回的 `Provider.url`
字段（protobuf 字段号 7）对远程提供商返回原配置中的完整订阅 URL，对本地和内联提供商返回空字符串。
响应不包含请求头、本地文件路径或其他提供商配置。已有的订阅流量、到期时间、节点、刷新和健康检查字段保持不变。

订阅 URL 可能包含凭据。客户端应默认隐藏，仅在用户明确操作时查看或复制，不得将其写入日志。

### Probe 分组

`probe` 分组通过 `probe` 指定一个 `node-probe` 服务，通过 `policy` 指定该服务中的一条策略。服务经每个成员执行测量（`measures`：`download` 按 Range 下载 `bytes` 字节；`request` 收到任意 HTTP 响应即算成功），按成员和测量保留最近 `window` 个采样，并在 `traffic_window` 内统计各 probe 分组经各成员承载的真实连接。策略中的表达式（[expr](https://expr-lang.org/) 语言，加载时编译并做类型检查）决定 `eligible`、`qualify`、`score`（越小越好）、`switch` 和 `penalty`（秒）。引用未定义的测量、未知变量或结果类型不符时，`sing-box check` 失败。

筛选（启动、`screen.interval` 到期、开启 `screen.on_network_change` 时的网络变化，或调用 `URLTest`）测量所有参选成员并逐个求值；分组没有当前成员或当前成员不参选时直接选用合格且未冷却的最优成员，否则仅在 `switch` 为真时切换。检查（`check.interval` 到期，或开启 `check.on_traffic_failure` 时经当前成员的连接失败，最短间隔 `check.cooldown`）只测量当前成员；当前成员不再合格时，按得分顺序逐个测量其余成员，选用第一个合格者。成员合格之前不会被选用。配置 `domestic` 时，直连检查失败则跳过判定和切换。

表达式变量（时长单位为秒，吞吐单位为字节/秒）：`m.<测量名>` 含 `count`、`ok_rate`、`fail_streak`、`age`、`last_ok`、`last_error`，`first_byte`/`transfer`/`total`/`throughput` 各含 `last`/`p50`/`p90`/`min`/`max`/`mean`，`targets["<url>"]` 为按目标拆分的同样字段；`traffic` 含 `conns`、`ok`、`dial_failed`、`aborted`、`fail_rate`、`bytes_down`、`bytes_up`、`last_failure_age`；`node`（`tag`、`index`、`type`）；`is_current`、`selected_for`、`penalized`、`penalty_left`；`domestic`（`ok`、`configured`）；`now`（`hour`、`minute`、`weekday`、`unix`，本地时间）；`group`（`tag`、`size`、`current`）；常量 `ms`、`s`、`minute`、`hour`、`KiB`、`MiB`。`penalty` 另可用 `qualified` 和 `score`。`switch` 可用 `current` 和 `best`，各含 `tag`、`index`、`qualified`、`score`、`penalized`、`m`、`traffic`，以及 `domestic`、`now`、`group` 和常量。默认值：eligible `true`、qualify `true`、score `node.index`、switch `!current.qualified || best.score < current.score`、penalty `0`。

`StartedService.SubscribeGroups` 为 `probe` 分组返回 `Group.probe`，包含 `policy`、生效的 `expressions`（依次为 eligible、qualify、score、switch、penalty）、`screenMeasures`、`checkMeasures`、`selectedAt`、`switchReason`、`trafficWindowMs`、`running`、`generation`、`screenedAt` 和 `error`。

对 probe 分组调用 `StartedService.URLTest` 会请求一次该策略的筛选；排队或正在运行的请求会合并。筛选开始和结束均推送更新，即使没有切换节点。每次尝试结束都会递增 `generation`、清除 `running` 并更新 `screenedAt`（Unix 毫秒），包括离线、暂停、取消或跳过。客户端应记录请求前的 `generation`，等待 `running == false` 且 `generation` 增大，并展示非空 `error`。

成员既没有求值结果、采样，也没有统计到连接时，`GroupItem.probe` 缺失。否则包含最近一次判定（`time`、`eligible`、`ok` = 合格且未冷却、`error` 为未通过的 qualify 子句或求值错误、`hasScore`、`score`、`penaltyUntil`）、各测量的统计（`measures`：`count`、`okRate`、`lastTime`、`lastOk`、`lastError`，以及成功采样的 `firstByteMs`、`transferMs`、`totalMs` 和 `throughput`（字节/秒）的 `last/p50/p90/min/max`），以及窗口内的真实连接统计 `traffic`（`conns`、`ok`、`dialFailed`、`aborted`、`failRate`、`bytesDown`、`bytesUp`）。

### 结构

```json
{
  "type": "api",
  
  ... // 监听字段
  
  "secret": "",
  "access_control_allow_origin": [],
  "access_control_allow_private_network": false,
  "dashboard": {
    "enabled": true,
    "path": "",
    "download_url": "",
    "http_client": "", // 或 {}
    "update_interval": ""
  },
  "tls": {}
}
```

### 监听字段

参阅 [监听字段](/zh/configuration/shared/listen/)。

### 字段

#### secret

API 密钥。

客户端通过标准的 `authorization: Bearer <secret>` gRPC metadata 头认证。

默认无需认证。

#### access_control_allow_origin

允许的 CORS 来源,默认使用 `*`。

#### access_control_allow_private_network

允许从私有网络访问。

#### dashboard

下载并通过 API 监听器在 `/dashboard/` 提供的 Web 仪表板；其他浏览器请求将被重定向到该路径。

!!! info ""

    该对象可以替换为布尔值（等同于 `{ "enabled": <bool> }`），
    或字符串路径（等同于 `{ "enabled": true, "path": "<string>" }`）。

##### enabled

启用仪表板。

##### path

存放仪表板文件的目录。

默认使用工作目录下的 `dashboard`。

如果目录中包含非 sing-box 下载的文件，将按原样提供。

##### download_url

仪表板压缩包（zip）的下载 URL。

默认使用 `https://github.com/SagerNet/sing-box-dashboard/archive/refs/heads/gh-pages.zip`。

##### http_client

用于下载仪表板的 HTTP 客户端。

参阅 [HTTP 客户端字段](/zh/configuration/shared/http-client/)。

##### update_interval

仪表板的更新间隔。

默认使用 `1d`。

#### tls

TLS 配置,参阅 [TLS](/zh/configuration/shared/tls/#inbound)。
