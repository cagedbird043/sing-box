---
icon: material/new-box
---

!!! question "自 sing-box 1.15.0-cagedbird 起"

# Race

### 结构

```json
{
  "type": "race",
  "tag": "edgex",
  "servers": ["edgex-1", "edgex-2"]
}
```

### 字段

#### servers

必填，其他 DNS 服务器标签的非空列表。支持任意服务器类型，包括嵌套 race；各服务器保留自己的传输、TLS、detour 和 domain_resolver 设置。不存在的标签、自引用和依赖循环会被拒绝。

Race 将查询的独立副本并发交给所有列出的服务器。首个无错误、返回 NOERROR 且 Answer 中至少包含一条与问题类型相同的记录的响应获胜，随后取消其余查询。对于 A/AAAA 问题，仅含 CNAME 的响应不会获胜。

若所有响应均不满足条件，则等待全部查询结束，在无错误的响应中优先返回 NXDOMAIN，其次是没有匹配答案的 NOERROR，最后是其他返回码。同优先级采用完成顺序。若全部查询出错，则返回合并后的错误。调用方取消时会取消所有查询。

被引用服务器作为 DNS 传输依赖，由管理器优先启动并计入引用。Race 不会启动、关闭或重置这些服务器的传输。
