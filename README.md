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

## Benchmark Spec（YAML）

```yaml
name: mock-smoke-test
target:
  base_url: http://127.0.0.1:9000   # OpenAI Compatible 地址
  model: mock-llm
engine: mock                        # mock | guidellm
workload:
  concurrency: 4
  requests: 40
  input_tokens: 128
  output_tokens: 32
  stream: true
slo:                                # 可选，报告里判定通过/违例
  p99_latency_ms: 5000
  min_tps: 10
```

## CLI

| 命令 | 说明 |
|---|---|
| `bench run -f spec.yaml` | 提交压测（默认连本地 :8080，用 `--server` 指定） |
| `bench list` | 运行列表 |
| `bench get <id>` | 运行状态 |
| `bench report <id>` | 聚合报告 JSON（含 SLO 判定） |
| `bench mock-target --listen :9000` | 本地 OpenAI Compatible 模拟目标（可调 `--ttft-ms` / `--tpot-ms`） |

## REST API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/runs | 提交（JSON 或 YAML body） |
| GET | /api/v1/runs | 列表 |
| GET | /api/v1/runs/:id | 详情 |
| POST | /api/v1/runs/:id/cancel | 取消 |
| GET | /api/v1/runs/:id/metrics | 实时曲线采样点 |
| GET | /api/v1/runs/:id/report | 聚合报告（`?format=csv` 导出） |
| GET | /api/v1/engines | 引擎列表 |

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
cmd/bench         CLI（含 mock-target）
internal/spec     YAML Spec 解析与校验
internal/store    GORM 模型（Run / Summary / MetricPoint）
internal/engine   引擎接口 + 注册表
internal/engine/mock      内置压测引擎
internal/engine/guidellm  GuideLLM 适配器
internal/core     RunManager（异步状态机）
internal/analyzer 指标聚合 + SLO 判定
internal/api      REST API
internal/webui    Web UI 嵌入（Vite 构建产物）
web/              前端源码（React + Ant Design + ECharts）
examples/         示例 Spec
```
