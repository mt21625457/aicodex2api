## Why

OpenAI `/v1/responses` HTTP 转发路径（包括 SSE 流式、非流式、透传模式）是网关最核心的热路径，每个请求都会经过完整的 handler → service → upstream → response 链路。经过对全部 ~12000 行 OpenAI 相关代码的逐函数审查，发现当前实现在以下层面存在可量化的非必要开销：

1. **请求体处理**：非透传模式下对所有请求体执行 `json.Unmarshal → map[string]any → json.Marshal` 全量反序列化/序列化，即使只需要修改 1-2 个字段。对于携带大量 `input` 数组的 Codex 请求（常见 50-500KB），这会产生大量中间 `map/slice/interface{}` 临时对象，触发高频 GC。
2. **SSE 流式转发**：逐行 `fmt.Fprintf` + `Flush` 导致每个 SSE 事件都触发一次系统调用；模型名替换和 usage 解析在绝大多数行上做了不必要的 JSON 解析。
3. **账号调度**：每次调度执行 4 次候选列表遍历 + 1 次全量排序；运行时统计使用全局写锁；会话哈希使用加密级 SHA-256。
4. **连接池锁竞争**：WS 连接池 `acquire` 在持有互斥锁期间执行清理逻辑，高并发下成为瓶颈。
5. **Handler 层冗余**：ops 上下文被设置两次。

这些问题在单请求视角下各自开销不大（μs~ms 级），但在高并发、大请求体、长流式响应的生产场景下会叠加放大，直接影响 TTFT（首 token 时间）、P95/P99 尾延迟和 GC 停顿。

## What Changes

本提案针对 OpenAI HTTP 转发全路径提出 13 项性能优化，按优先级分为三档。

### P0：热路径核心优化（每请求直接命中）

#### 1. 用 sjson 点操作替代全量 json.Unmarshal/Marshal

- **问题定位**: `openai_gateway_service.go:1413-1547` `Forward()` 方法
- **现状**: 非透传模式下，`getOpenAIRequestBodyMap()` 调用 `json.Unmarshal` 将 `body []byte` 反序列化为 `map[string]any`，修改若干字段后再 `json.Marshal` 回 `[]byte`。
- **量化影响**: 对 200KB 请求体，`Unmarshal + Marshal` 耗时 ~2-5ms，产生 ~1000+ 次堆分配和 ~500KB 临时内存。
- **优化方案**: 使用已引入的 `tidwall/sjson` 库做精确字节级修改：
  - `sjson.SetBytes(body, "model", mappedModel)` 替代 `reqBody["model"] = mappedModel` + `json.Marshal`
  - `sjson.DeleteBytes(body, "max_output_tokens")` 替代 `delete(reqBody, "max_output_tokens")` + `json.Marshal`
  - 仅在 `bodyModified == true` 时才执行任何操作，且无需整体反序列化
- **保留条件**: 涉及复杂嵌套修改（如 `input` 数组内字段校正）的场景仍保留 `map[string]any` 路径作为降级
- **预估收益**: 大请求体场景 CPU 降低 30-50%，allocs/op 降低 60%+

#### 2. SSE 流式响应批量写入与智能 Flush

- **问题定位**: `openai_gateway_service.go:2762-2787` `handleStreamingResponse()` 方法
- **现状**: 每读取一行 SSE 事件都执行一次 `fmt.Fprintf(w, "%s\n", line)` + `flusher.Flush()`，在高频 token 输出时每秒可触发数百次系统调用。
- **量化影响**: 每次 Flush 约 1-5μs 系统调用开销，100 token/s 场景下仅 Flush 就消耗 ~100-500μs/s。
- **优化方案**:
  - 引入 `bufio.Writer` 包装 `c.Writer`，缓冲区 4KB
  - 仅在 channel 队列为空时（`len(events) == 0`）执行 Flush，实现"尽快但不过度"的语义
  - 保留 keepalive ticker Flush 逻辑不变
- **风险控制**: 缓冲区不影响 TTFT（第一个事件仍立即 Flush），仅在后续高频事件时生效
- **预估收益**: 高吞吐流式场景系统调用减少 50-80%

#### 3. 模型名替换增加 bytes.Contains 快速门控

- **问题定位**: `openai_gateway_service.go:2840-2868` `replaceModelInSSELine()` 方法
- **现状**: 当 `needModelReplace == true` 时，对每行 SSE 事件执行两次 `gjson.Get` + 可能的 `sjson.Set`。但实际上 SSE 流中仅 `response.created`、`response.completed` 等少数事件类型包含 `model` 字段。
- **优化方案**: 在调用 `replaceModelInSSELine` 前增加 `strings.Contains(data, fromModel)` 快速判断：
  ```go
  if needModelReplace && strings.Contains(data, mappedModel) {
      line = s.replaceModelInSSELine(line, mappedModel, originalModel)
  }
  ```
- **预估收益**: ~90% 的 SSE 行可跳过 JSON 解析，该函数整体耗时降低 80%+

#### 4. parseSSEUsageBytes 增加长度门控

- **问题定位**: `openai_gateway_service.go:2887-2902`
- **现状**: 对每行 SSE 数据执行 `bytes.Contains(data, []byte("response.completed"))` 扫描。
- **优化方案**: `response.completed` 事件通常包含完整 usage 数据，payload 长度远大于普通 token 事件。增加短行快速跳过：
  ```go
  if len(data) < 80 || !bytes.Contains(data, []byte(`"response.completed"`)) {
      return
  }
  ```
- **预估收益**: 减少 95%+ 行的 `bytes.Contains` 扫描开销

#### 5. 消除 setOpsRequestContext 双重调用

- **问题定位**: `openai_gateway_handler.go:115, 143`
- **现状**: 第 115 行以空 model 调用一次，第 143 行解析出 model 后再调用一次。
- **优化方案**: 删除第 115 行的调用，将所有 ops context 设置延迟到模型解析完成后一次性完成。
- **预估收益**: 每请求减少一次 context 写入（~200ns）

### P1：调度与连接池路径优化

#### 6. selectByLoadBalance 合并遍历 + 使用堆替代排序

- **问题定位**: `openai_account_scheduler.go:319-488`
- **现状**: 4 次遍历候选列表 + 1 次 `sort.SliceStable` 全量排序，仅为选出 TopK 最优候选。
- **优化方案**:
  - 将过滤、负载收集、分数计算合并为一次遍历
  - 使用 `container/heap` 维护 TopK 最小堆，时间复杂度从 O(n log n) 降至 O(n log k)
  - 当候选数 ≤ TopK 时直接跳过排序
- **预估收益**: 20+ 账号场景调度延迟降低 40-60%

#### 7. openAIAccountRuntimeStats 消除全局写锁

- **问题定位**: `openai_account_scheduler.go:113-142`
- **现状**: `report()` 使用 `sync.Mutex` 全局写锁更新 EWMA 统计，每请求完成时调用。
- **优化方案**: 将 `map[int64]*openAIAccountRuntimeStat` 改为 `sync.Map`，每个 `openAIAccountRuntimeStat` 内部使用 `atomic.Uint64`（配合 `math.Float64bits/Float64frombits`）实现无锁 EWMA 更新：
  ```go
  type openAIAccountRuntimeStat struct {
      errorRateEWMABits atomic.Uint64
      ttftEWMABits      atomic.Uint64
      hasTTFT           atomic.Bool
  }
  ```
- **风险**: CAS 循环可能在极端竞争下略有重试，但远优于互斥锁
- **预估收益**: 消除每请求一次的全局锁竞争

#### 8. GenerateSessionHash 使用非加密哈希

- **问题定位**: `openai_gateway_service.go:842-860`
- **现状**: 对 session_id 执行 `crypto/sha256.Sum256` + `hex.EncodeToString`。会话哈希仅用作缓存 key，不需要抗碰撞。
- **优化方案**: 使用 `hash/fnv` 或 `xxhash`（第三方）替代 SHA-256。FNV-128 在短字符串上比 SHA-256 快 5-10 倍：
  ```go
  h := fnv.New128a()
  h.Write([]byte(sessionID))
  return hex.EncodeToString(h.Sum(nil))
  ```
- **预估收益**: 每请求节省 ~0.5-1μs

#### 9. WS 连接池 acquire 清理逻辑外移

- **问题定位**: `openai_ws_pool.go:746-751`
- **现状**: `acquire()` 在持有 `ap.mu.Lock()` 期间按时间间隔触发 `cleanupAccountLocked()`，内部遍历所有连接、排序空闲连接、执行驱逐。
- **优化方案**: 已有 `runBackgroundCleanupWorker(30s)` 后台清理机制，将 `acquire()` 中的按需清理移除（或大幅延长触发间隔到 30s+），让清理完全由后台 worker 负责：
  ```go
  // acquire() 中删除以下代码块：
  // if ap.lastCleanupAt.IsZero() || now.Sub(ap.lastCleanupAt) >= openAIWSAcquireCleanupInterval {
  //     evicted = p.cleanupAccountLocked(ap, now, effectiveMaxConns)
  //     ap.lastCleanupAt = now
  // }
  ```
- **风险**: 极端场景下过期连接可能多存活最多 30s，但 health check 和 maxAge 检查会在使用时兜底
- **预估收益**: acquire 锁持有时间降低 30-50%（尤其在连接数多时）

### P2：低影响但零风险优化

#### 10. io.ReadAll 预分配 buffer

- **问题定位**: `openai_gateway_handler.go:100`
- **现状**: `io.ReadAll(c.Request.Body)` 未利用 `Content-Length` 做容量预估，较大请求体下会发生多次扩容与拷贝。
- **优化方案**: 根据 `Content-Length` 预分配：
  ```go
  size := c.Request.ContentLength
  if size <= 0 || size > maxBodySize { size = 512 }
  buf := bytes.NewBuffer(make([]byte, 0, size))
  _, err := io.Copy(buf, io.LimitReader(c.Request.Body, maxBodySize))
  body := buf.Bytes()
  ```
- **预估收益**: 大请求体减少 3-5 次 slice grow，节省 ~100μs + 减少碎片

#### 11. nextConnID 避免 fmt.Sprintf

- **问题定位**: `openai_ws_pool.go:1278-1281`
- **现状**: `fmt.Sprintf("oa_ws_%d_%d", accountID, seq)` 使用反射机制。
- **优化方案**: 使用 `strconv.AppendInt` 手动拼接：
  ```go
  buf := make([]byte, 0, 32)
  buf = append(buf, "oa_ws_"...)
  buf = strconv.AppendInt(buf, accountID, 10)
  buf = append(buf, '_')
  buf = strconv.AppendUint(buf, seq, 10)
  return string(buf)
  ```
- **预估收益**: ~50-100ns/次

#### 12. handleStreamingResponse 条件性省略 goroutine

- **问题定位**: `openai_gateway_service.go:2637-2662`
- **现状**: 所有流式请求都创建读取 goroutine + channel 用于超时/keepalive 监控。
- **优化方案**: 当 `streamInterval == 0 && keepaliveInterval == 0` 时（即未配置超时和 keepalive），退化为主 goroutine 同步读取，省去 goroutine 调度和 channel 同步开销。
- **预估收益**: 无超时配置场景每请求省 ~2-5μs goroutine 创建/调度开销

#### 13. listSchedulableAccounts 确认本地缓存 TTL

- **问题定位**: `openai_gateway_service.go:1265-1283` 及 `SchedulerSnapshotService` 实现
- **现状**: 每次调度调用 `listSchedulableAccounts()`，依赖 `schedulerSnapshot` 提供缓存。需确认 snapshot 内部有短 TTL 内存缓存（建议 1-5s）。
- **优化方案**: 审查并确认 `SchedulerSnapshotService.ListSchedulableAccounts()` 内部确实有 local cache；若无，增加 1-3s TTL 的 `sync.Map` 或 `atomic.Value` 缓存。
- **预估收益**: 降低每请求 Redis 读取压力，并进一步减少缓存抖动时的回源概率

## Deferred（本次不改，后续独立推进）

- **WS Forwarder 双向消息代理热路径优化**：`openai_ws_forwarder.go` 的事件循环（~2800 行）需要独立分析，已在 `openai-ws-v2-performance` spec 中跟踪。
- **HTTP Transport 连接池参数调优**：`httpUpstream.Do()` 底层的 `http.Transport` 参数（MaxIdleConnsPerHost、IdleConnTimeout 等）需要结合压测数据决定，不在本提案范围。
- **gjson → sonic/jsoniter 替代**：全局切换 JSON 库影响面太大，需独立评估兼容性。
- **账号列表排序结果缓存**：调度器排序结果短 TTL 缓存涉及一致性语义，需独立设计。

## Capabilities

### Modified Capabilities

- `openai-oauth-performance`：本提案扩展 HTTP 转发路径的性能约束，与已有 OAuth 性能 spec 互补。

### New Capabilities（建议新增至 spec）

- **HTTP 转发路径请求体处理**：系统 MUST 避免在仅需修改少量字段时对完整请求体执行全量反序列化/序列化。
- **SSE 流式转发写入效率**：系统 MUST 在 SSE 流式转发中使用批量写入策略，避免逐事件触发系统调用。
- **调度器时间复杂度约束**：系统 SHALL 以 O(n) 或 O(n log k) 复杂度完成账号选择，不得在热路径执行全量排序。

## Impact

- **影响模块**:
  - `backend/internal/handler/openai_gateway_handler.go` — P0#5
  - `backend/internal/service/openai_gateway_service.go` — P0#1, P0#2, P0#3, P0#4, P1#8, P2#10, P2#13
  - `backend/internal/service/openai_account_scheduler.go` — P1#6, P1#7
  - `backend/internal/service/openai_ws_pool.go` — P1#9, P2#11
  - `backend/internal/service/scheduler_snapshot_service.go` — P2#13
- **影响类型**: 热路径 CPU / GC / 系统调用开销、调度延迟、连接池锁竞争。
- **API 兼容性**: 对外 API 路由与协议完全不变，零 Breaking Change。所有优化均为内部实现级别。
- **风险等级**: 低。P0 优化均有明确的快速路径门控和降级条件；P1/P2 改动独立可控。
- **验收标准**:
  - P0 实施后：热路径 allocs/op 降低 ≥40%，200KB 请求体 Forward 延迟降低 ≥30%
  - SSE 流式场景 Flush 系统调用减少 ≥50%
  - P1 实施后：20 账号调度延迟降低 ≥30%，运行时统计无全局锁竞争
