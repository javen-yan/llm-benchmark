# LLM Benchmark Platform — 架构分析与落地规划

> 基于架构图（2026-09-30）的分析。目标：按 Roadmap Phase 1（1-2 个月）
> 在本机落地一个可运行的 MVP：Go 平台核心、GuideLLM Adapter、基础 Web UI、核心指标与存储。

## 一、架构图分层解读

| 层 | 图中组件 | MVP 落地方式 |
|---|---|---|
| 用户层 | Web UI / CLI / REST API / Benchmark Spec | 全部做：React+AntD+ECharts 三页面；Cobra CLI；Gin REST API；YAML Spec |
| 平台核心层 | Run Manager / Scheduler / Snapshot / Analyzer / Plugin Manager | RunManager（异步生命周期状态机）；Scheduler 简化为串行队列+并发上限；Snapshot 记录 spec+环境快照；Analyzer 做 TTFT/TPOT/P50/P90/P95/P99/TPS 聚合；Plugin Manager 简化为 Engine 注册表（接口） |
| 数据存储层 | PostgreSQL / 时序存储 / 对象存储 | GORM：默认 SQLite（零依赖本地跑），`DATABASE_URL` 切 PostgreSQL；时序=MetricPoint 表；对象存储=原始事件 JSON 落盘（Phase 2 再接 S3） |
| 引擎适配层 | GuideLLM Adapter（V1 推荐）/ vLLM Adapter / Native Engine | GuideLLM Adapter 真实实现（调 `guidellm` CLI，解析 JSON 输出→归一化）；vLLM/Native 为接口占位；内置 **Mock 引擎**（零依赖，用于本机端到端验证） |
| 目标服务层 | LLM Gateway / vLLM / OpenAI Compatible / Anthropic | 目标=任意 OpenAI Compatible `/v1/chat/completions`；附带 `mock-target`（本地模拟服务，可配延迟分布） |
| 观测与指标层 | 事件归一化 / Metric Registry / Collector / Probe | 事件归一化=Engine→统一 Result 结构；MetricRegistry=指标定义注册表；Collector（DCGM/NVML）与 Probe（iperf3/NCCL）进 Phase 3，留接口 |
| 结果与展示层 | 实时监控 / 结果对比 / 容量分析 / 报告导出 | 运行详情页 ECharts 实时曲线（轮询）；对比/容量进 Phase 2；报告导出先给 JSON/CSV |

## 二、核心设计决策

1. **BenchmarkEngine 接口**：平台不依赖具体引擎实现，
   `Run(ctx, spec, progress) (Result, error)`，所有引擎输出归一化为统一 `Result`（请求级事件 + 聚合指标）。
2. **有效期/并发**：与 xianyu-manager 一致的务实路线——先串行队列（`max_concurrent=1` 可配），分布式 Agent 进 Phase 3。
3. **指标口径**（对齐 GuideLLM）：
   - TTFT：首 token 延迟；TPOT：每输出 token 延迟；ITL：token 间延迟
   - 吞吐：RPS（请求/秒）、TPS（token/秒，含输出 token）
   - 分位：P50/P90/P95/P99（TTFT、TPOT、端到端延迟）
4. **Benchmark Spec (YAML)**：`name/target/engine/workload/slo` 四段式，
   workload 描述并发、时长/请求数、输入输出长度分布；slo 为 Phase 2 判定预留字段。
5. **单二进制交付**：前端 `npm run build` → `web/dist` → `go:embed` 进 server；
   CLI 独立 `bench` 二进制。Docker/K8s 部署进 Phase 2。

## 三、Phase 1 MVP 范围（2026-09-30，v2 重构后落地）

- [x] Go 模块骨架，Gin + GORM + SQLite/PostgreSQL
- [x] Benchmark Spec v2 YAML 解析与校验（target/engine/dataset/load/warmup/thresholds）
- [x] Engine 接口 + 注册表：Name/Capabilities/Prepare/Run/Cancel；GuideLLM Adapter（CLI 检测+参数生成+报告解析，fixture 测试）；Mock 引擎（closed_loop/open_loop/poisson/warmup/SSE 逐 token 计时）
- [x] RunManager：`created→preparing→warming_up→running→normalizing→analyzing→completed` 状态机（可 failed/cancelled），异步执行
- [x] 事件归一化：所有引擎输出统一为 `RequestEvent`；Analyzer 全分位聚合（min/avg/p50/p75/p90/p95/p99/max）
- [x] Metric Registry：动态指标注册，`metric_value` 按行存储（50+ 指标行/运行），无固定指标列
- [x] Snapshot：每次运行保存 spec/引擎/数据集/硬件，可复现
- [x] Collector 插件接口 + 内置 system 采集（CPU/内存/负载/网络）；Probe 接口预留
- [x] REST API：runs CRUD/cancel、metrics 实时点、system 样本、report（JSON/CSV）、snapshot、compare（含公平性校验）、engines、metrics/definitions
- [x] CLI：`bench run/list/get/report/cancel/compare/mock-target`
- [x] Web UI：运行列表 / 新建（v2 表单）/ 运行详情（指标总表+ECharts+系统曲线+Snapshot+SLO）/ 对比页，已嵌入 Go 单二进制
- [x] 端到端验证：mock-target → Mock 引擎（closed_loop 40 请求 + open_loop 20 请求）→ API 确认 50 指标行 → Web UI 冒烟通过；`go test ./...` 6 个包全过

## 四、Phase 2+ 预留（接口已留，不实现）

- 自动容量边界搜索、报告 PDF/HTML、Prometheus 时序、对象存储
- Collector 插件（DCGM/NVML）、Probe 具体实现（iperf3/NCCL/RDMA 预检）
- vLLM Adapter、Native Engine、分布式 Agent
- 多租户/权限、Docker/Kubernetes 部署 manifests

> 说明：GuideLLM Adapter 已实现 CLI 契约（检测/参数生成/报告解析）并通过 fixture 测试，
> 但尚未在真实 GPU 环境跑通 `guidellm` CLI 全流程；生产使用前需在目标环境实测验证。
