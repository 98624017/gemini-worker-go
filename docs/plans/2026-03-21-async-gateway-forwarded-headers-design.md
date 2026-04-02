# Async Gateway Forwarded Headers Design

## 背景

当前 `async-gateway` 在接收异步提交请求时，只会保存并转发以下请求头：

- `Content-Type`
- `Accept`
- `X-Request-ID`

这意味着即使前置代理（例如 Nginx、Zeabur Ingress）已经正确写入了
`X-Forwarded-For`、`X-Real-IP`、`X-Forwarded-Proto`、`Forwarded`，
这些信息也不会被异步层继续传给下游 `NewAPI`。结果是 `NewAPI` 无法拿到
真实来源 IP 或完整的代理链信息。

## 目标

在不扩大信任边界的前提下，让 `async-gateway` 能够把**可信上游代理已经写好的**
转发相关头原样带给 `NewAPI`。

## 非目标

本次不做以下事情：

- 不从 `RemoteAddr` 自行推导客户端 IP
- 不自动补 `X-Real-IP`
- 不自动拼接、覆盖、改写 `X-Forwarded-For`
- 不做“透传所有 `X-Forwarded-*`”的宽泛策略
- 不引入新的环境变量或配置项
- 不改数据库 schema

## 方案比较

### 方案 A：扩展固定白名单

在现有白名单上新增以下头：

- `X-Forwarded-For`
- `X-Real-IP`
- `X-Forwarded-Proto`
- `Forwarded`

优点：

- 边界清晰
- 风险最小
- 与现有代码结构兼容

缺点：

- 以后如果要新增其他可信头，还需要再改代码

### 方案 B：透传所有 `X-Forwarded-*`

优点：

- 更灵活

缺点：

- 边界过宽
- 后续更难审计到底哪些头会流向 `NewAPI`

### 方案 C：通过配置定义透传白名单

优点：

- 最灵活

缺点：

- 对当前需求过度设计
- 增加配置成本和误配风险

## 推荐方案

选择 **方案 A：扩展固定白名单**。

原因：

- 精准满足“保留真实 IP 和代理信息”的需求
- 不扩大信任边界
- 不引入新的配置复杂度

## 设计

### 保存阶段

`submit_handler` 在 `extractForwardHeaders` 阶段，除了现有的
`Content-Type`、`Accept`、`X-Request-ID` 之外，再额外保存：

- `X-Forwarded-For`
- `X-Real-IP`
- `X-Forwarded-Proto`
- `Forwarded`

保存策略保持与当前一致：

- 只读取请求里已经存在的值
- 空值不保存
- 不做拼接和重写

### 转发阶段

`forwarder` 继续复用现有 `copyForwardHeaders` 逻辑，把存下来的
`ForwardHeaders` 写到发往 `NewAPI` 的请求头中。

保留现有的保护逻辑，不允许覆盖这些敏感头：

- `Authorization`
- `Content-Encoding`
- `Content-Length`
- `Host`

### 信任边界

本次实现仅传递“上游已经写好的可信代理头”，不新增任何由
`async-gateway` 自己推导的来源 IP 信息。

因此，真实 IP 的正确性仍依赖于前置代理层的配置是否正确，例如：

- Nginx 写入 `X-Forwarded-For`
- Ingress 写入 `X-Real-IP`
- 统一代理写入 `Forwarded`

如果前置层没有写这些头，`async-gateway` 也不会补。

## 测试

需要覆盖两类测试：

1. `submit_handler` 测试
   验证可信代理头会被保存到 `ForwardHeaders`
2. `forwarder` 测试
   验证这些头会被原样带到 `NewAPI`

## 风险

### 风险 1：客户端伪造代理头

如果系统直接暴露给公网，且没有可信前置代理做头部治理，那么客户端有机会自行传入
`X-Forwarded-For` 等值。

这个风险并不是本次改动新引入的，而是部署信任边界问题。本次设计默认：

- `async-gateway` 运行在受控代理之后
- 头部的可信性由前置层保障

### 风险 2：代理链头长度增加

`X-Forwarded-For` 或 `Forwarded` 可能变长，但本次只是透传既有值，不做额外扩展，
风险可接受。

## 结论

本次按固定白名单扩展可信代理头透传，是满足需求且最稳妥的最小方案。
