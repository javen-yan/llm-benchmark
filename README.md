# LLM Benchmark 平台

对 LLM 推理服务做压测与性能评估的一体化平台：声明式 YAML Spec → 压测执行 → 指标聚合（TTFT / TPOT / 端到端延迟 / RPS / TPS / P50/P99）→ Web 可视化 + 报告导出。

```
┌──────────────┐   ┌──────────────┐   ┌───────────────────┐   ┌────────────┐
│  Web UI / CLI │──▶│  平台服务    │──▶│ 引擎层            │──▶│ 目标 LLM   │
│  (Spec 提交)  │   │ (Gin+Runner) │   │ mock / guidellm   │   │ 服务       │
└──────────────┘   └──────┬───────┘   └───────────────────┘   └────────────┘
                          │ 事件归一化
                   ┌──────▼───────┐   ┌──────────────┐
                   │ Analyzer     │──▶│ SQLite/PG    │
                   │ (聚合+P99)   │   │ Run/事件/曲线 │
                   └──────────────┘   └──────────────┘
```

## 构建

前端构建产物不进仓库，clone 后按顺序构建一次即可。完整步骤见 **[BUILD.md](BUILD.md)**。

```bash
cd web && npm ci && npm run build && cd ..  # 先前端
go build -o bin/server ./cmd/server          # 后 Go（含 Web UI 单二进制）
go build -o bin/bench ./cmd/bench
```

## 快速开始

```bash
# 1. 启动模拟目标服务（OpenAI Compatible，无需 GPU）
bench mock-target --listen :9000

# 2. 启动平台服务（Web UI: http://127.0.0.1:8080）
server

# 3. 提交一次压测
bench run -f examples/spec-mock.yaml

# 4. 查看报告
bench report <run-id>
```

或用 Web UI：打开 http://127.0.0.1:8080 → 新建 Benchmark → 填写表单提交 → 详情页看曲线。

## Benchmark Spec（YAML v2，对应参考设计 §4）

```yaml
name: mock-smoke-test
target:
  endpoint: http://127.0.0.1:9000   # OpenAI Compatible 地址
  protocol: openai
  model: mock-llm
engine:
  type: mock                        # mock | guidellm
dataset:
  type: random
  input_tokens: 128
  output_tokens: 32
  seed: 42
load:
  mode: closed_loop                 # closed_loop | open_loop | poisson
  concurrency: 4
  requests: 40                      # 与 duration 二选一
  # rate: 20                        # open_loop / poisson 必填（req/s）
  stream: true
# warmup:
#   duration: 30s                   # 预热请求不计入指标
thresholds:                         # SLO 判定
  ttft_p99: 2000
  min_tps: 10
  error_rate: 0.01
  p99_latency_ms: 5000
```

## CLI

| 命令 | 说明 |
|---|---|
| `bench run -f spec.yaml` | 提交压测（默认连本地 :8080，用 `--server` 指定） |
| `bench list` | 运行列表 |
| `bench get <id>` | 运行状态 |
| `bench report <id>` | 聚合报告（50+ 指标明细 + SLO 判定） |
| `bench cancel <id>` | 取消运行中的任务 |
| `bench compare <id1> <id2>` | 双运行对比（公平性校验 + 核心指标对照） |
| `bench mock-target --listen :9000` | 本地 OpenAI Compatible 模拟目标（可调 `--ttft-ms` / `--tpot-ms`） |

## REST API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/runs | 提交（JSON 或 YAML body） |
| GET | /api/v1/runs | 列表（`?status=` 过滤） |
| GET | /api/v1/runs/:id | 详情（含 spec 与 snapshot 摘要） |
| POST | /api/v1/runs/:id/cancel | 取消 |
| GET | /api/v1/runs/:id/metrics | 实时曲线采样点 |
| GET | /api/v1/runs/:id/system | 系统采集样本（CPU/内存/负载/网络） |
| GET | /api/v1/runs/:id/report | 聚合报告（`?format=csv` 导出） |
| GET | /api/v1/runs/:id/snapshot | 运行快照（spec/engine/数据集/硬件） |
| GET | /api/v1/runs/compare?ids=a,b | 多运行对比 + 公平性校验 |
| GET | /api/v1/engines | 引擎列表（含 capabilities） |
| GET | /api/v1/metrics/definitions | Metric Registry 指标定义 |

## 核心设计（对齐参考设计）

- **Metric Registry**：指标动态注册（`llm.ttft/tpot/itl/e2e/queue_time` 直方图，`llm.request_rate/input_tps/output_tps/total_tps/error_rate` 等），`metric_value` 表按行存储，无固定指标列。
- **归一化**：所有引擎输出统一为 `RequestEvent`（ScheduledAt/StartedAt/FirstTokenAt/FinishedAt、逐 token 延迟、HTTP 状态），Analyzer 只认归一化事件；引擎无法提供的数据不伪造（capabilities 如实声明）。
- **Snapshot**：每次运行保存 spec、引擎版本、数据集、硬件信息，保证可复现；Compare 据此做公平性校验。
- **Run 生命周期**：`created → preparing → warming_up → running → normalizing → analyzing → completed`（任意阶段可 `failed`/`cancelled`）。
- **Collector / Probe 插件**：`Collector` / `Probe` 接口 + 注册表；内置 `system` collector（CPU/内存/负载/网络，被动采集）；probe 具体实现（iperf3/RDMA/NCCL）Phase 4 接入。

## 引擎

- **mock**（内置）：直接打目标的 `/v1/chat/completions`（SSE），零依赖，用于联调与 CI。
- **guidellm**：调外部 `guidellm` CLI 做生产级压测（需自行安装，`pip install guidellm`），结果自动归一化为统一事件。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `LISTEN` | `:8080` | 服务监听地址 |
| `DATABASE_URL` | `./bench.db` | SQLite 文件路径；`postgres://…` 切 PostgreSQL |

## 目录结构

```
cmd/server        平台服务入口（Gin + Web UI 嵌入单二进制）
cmd/bench         CLI（含 mock-target、compare）
internal/spec     Benchmark Spec v2 解析与校验
internal/store    GORM 模型（Run / Snapshot / RequestEvent / MetricValue / MetricPoint / SystemSample）
internal/metric   Metric Registry（指标定义 + 动态注册）
internal/engine   BenchmarkEngine 接口（Name/Capabilities/Prepare/Run/Cancel）+ 注册表
internal/engine/mock      内置压测引擎（closed_loop/open_loop/poisson/warmup）
internal/engine/guidellm  GuideLLM 适配器
internal/core     RunManager（多阶段状态机 + collector 编排）
internal/analyzer 指标聚合（全分位）+ SLO 判定
internal/collector Collector 插件接口 + system 实现
internal/probe    Probe 插件接口（预留）
internal/api      REST API（含 compare/snapshot/definitions）
internal/webui    Web UI 嵌入（Vite 构建产物）
web/              前端源码（React + Ant Design + ECharts）
examples/         示例 Spec
```
