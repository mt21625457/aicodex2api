## 1. Forwarder 热路径与错误语义

- [x] 1.1 将 `normalizeOpenAIWSLogValue` 的 `strings.NewReplacer` 提升为包级变量
- [x] 1.2 `error` 事件后统一 `lease.MarkBroken()`
- [x] 1.3 `BindResponseAccount` 返回错误增加 `warn` 级日志（4 处调用点）
- [x] 1.4 客户端断连后跳过非必要 model/tool 修正
- [x] 1.5 usage 解析增加事件门控（`response.completed`）
- [x] 1.6 ingress WS 客户端断连改为继续 drain 上游至 terminal

## 2. Pool 并发与后台维护

- [x] 2.1 写超时增加父 context 继承能力（`WriteJSONWithContextTimeout`）
- [x] 2.2 连接锁拆分为 `readMu/writeMu`
- [x] 2.3 增加后台 ping worker（30s）
- [x] 2.4 增加后台 cleanup worker（30s）
- [x] 2.5 `queueLimitPerConn` 兜底默认值下调为 16

## 3. 入口与依赖保护

- [x] 3.1 代理 Transport 增加 `TLSHandshakeTimeout: 10s`
- [x] 3.2 WS 消息读取上限从 128MB 下调到 16MB（client + ingress）
- [x] 3.3 ingress 非首轮 turn 统一使用调度器并发参数
- [x] 3.4 协议决策器补齐未知认证类型回退 HTTP
- [x] 3.5 Redis `set/get/delete` 增加独立 3s 超时

## 4. 测试与回归

- [x] 4.1 协议决策新增未知认证类型用例
- [x] 4.2 StateStore 新增 Redis 超时上下文用例
- [x] 4.3 Pool 新增父 context 写超时/读写并发/后台 sweep 用例
- [x] 4.4 Client 新增 TLSHandshakeTimeout 配置用例
- [x] 4.5 通过 service/handler 定向回归测试

## 5. 延后项（已确认，待后续提案）

- [ ] 5.1 terminal 后脏数据探测改为“无副作用”的安全方案
- [ ] 5.2 prewarm `creating` 计数语义重构与压测验收
- [ ] 5.3 `replaceOpenAIWSMessageModel` 双 `sjson.SetBytes` 深度优化
- [ ] 5.4 `GetResponseAccount` Redis 命中后的本地回填一致性方案

