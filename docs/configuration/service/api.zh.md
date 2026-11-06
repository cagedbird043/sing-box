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

`StartedService.SubscribeGroups` 为 `probe` 分组返回 `Group.probe`，包含请求型组的 `url`（下载型组为空）、`maxRTTRatio`、`running`、`generation`、`screenedAt` 和 `error`。

对 probe 分组调用 `StartedService.URLTest` 会触发所属 node-probe service 的共享完整筛选；排队或正在运行的请求会合并。下载型组保留仍合格的当前节点；请求型组保留各自的 URL 与 `prefer_order` 策略，测量结果按组隔离。

筛选开始和结束均推送更新，即使没有切换节点。每次尝试结束都会递增 `generation`、清除 `running` 并更新 `screenedAt`（Unix 毫秒），包括离线、暂停、取消或全部失败。客户端应记录请求前的 `generation`，等待 `running == false` 且 `generation` 增大，并展示非空 `error`。不要等待每个成员都有结果，`prefer_order` 可能提前结束。

`GroupItem.probe` 缺失表示本轮未检查，不是失败。报告含 `time`（Unix 毫秒）、`firstByteMs`、`transferMs`、`ratio`、`ok` 和 `error`。毫秒时长经过截断，下载判定与 `ratio` 使用原始精度时长；前端直接使用 `ratio`，不要从整数毫秒反推。普通请求的 `transferMs` 和 `ratio` 为零。

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
