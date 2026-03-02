# OpenAI OAuth 性能优化签核与灰度发布手册

> 变更：`optimize-openai-oauth-performance`  
> 更新时间：`2026-02-12`

## 0. 审核签核门禁（0.1 / 0.2 / 0.3）

### 0.1 基线窗口与压测场景冻结（已确认）

- 基线窗口：`2026-02-12 09:00` ~ `2026-02-12 12:00`（本地/预发同口径）
- 压测脚本：`tools/perf/openai_oauth_responses_k6.js`
- 压测模型：
  - 非流式：`NON_STREAM_RPS=8`
  - 流式：`STREAM_RPS=4`
  - 时长：`DURATION=3m`
- 样本请求：`/v1/responses`（Codex CLI UA，短文本 + stream/非 stream）
- 报告模板：`docs/perf/openai-oauth-baseline-template.md`

### 0.2 性能目标阈值冻结（已确认）

- SLA 下限：`99.5%`
- TTFT P99 上限：`900ms`
- 请求错误率上限：`2%`
- 上游错误率上限：`2%`

接口固化路径：

- `GET /api/v1/admin/ops/settings/metric-thresholds`
- `PUT /api/v1/admin/ops/settings/metric-thresholds`

示例阈值文件：`docs/perf/openai-oauth-metric-thresholds.example.json`

### 0.3 灰度策略与回滚阈值冻结（已确认）

- 灰度批次：`5% -> 20% -> 50% -> 100%`
- 每批观察窗口：`15~30 分钟`
- 回滚触发（任一满足即回滚）：
  - `TTFT P99 > 1200ms` 持续 `3 分钟`
  - `请求错误率 > 5%` 持续 `3 分钟`
  - `上游错误率 > 5%` 持续 `3 分钟`

---

## 6. 灰度发布与验收（6.1 / 6.2 / 6.3）

### 6.1 批次灰度执行记录模板

| 批次 | 流量比例 | 开始时间 | 结束时间 | 结果 | 备注 |
|---|---:|---|---|---|---|
| A | 5% |  |  |  |  |
| B | 20% |  |  |  |  |
| C | 50% |  |  |  |  |
| D | 100% |  |  |  |  |

每批必填观察项：

- `duration.p99_ms`
- `ttft.p99_ms`
- `error_rate`
- `upstream_error_rate`
- CPU / 内存 / Redis RT

### 6.2 阈值守护与快速回滚操作

#### 自动守护脚本

```bash
python tools/perf/openai_oauth_gray_guard.py \
  --base-url http://127.0.0.1:5231 \
  --admin-token <admin_token> \
  --platform openai \
  --time-range 30m
```

- 返回 `0`：指标通过，可继续观察/扩量
- 返回 `2`：超阈值，立即停止扩量并回滚

#### 建议回滚步骤

1. 停止当前批次扩量；冻结发布。
2. 回退到上一批稳定比例（或直接回到 0%）。
3. 按 `request_id/account_id` 抽样最近 30 分钟失败请求。
4. 导出 `ops dashboard overview + error trend + upstream errors`。
5. 在复盘会中确认根因后再重启灰度。

### 6.3 最终验收报告输出要求

最终报告使用：`openspec/changes/optimize-openai-oauth-performance/final-acceptance-report.md`

必含内容：

- 优化前后对比（P50/P95/P99、TTFT、错误率、CPU/内存、Redis RT）
- 各批次灰度收益与风险记录
- 回滚演练结果（成功/失败、耗时）
- 最终结论（是否关闭变更）


---

## 附：演练产物

- 灰度守护演练报告：`docs/perf/openai-oauth-gray-drill-report.md`
- 演练脚本：`tools/perf/openai_oauth_gray_drill.py`
- 守护脚本：`tools/perf/openai_oauth_gray_guard.py`
