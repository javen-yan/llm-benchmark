// Package engine 是引擎适配层（对应参考设计 §3、§6）。
//
// 核心原则（参考设计 §21）：
//  1. GuideLLM 只是 Engine，不是平台核心依赖；所有引擎实现统一 BenchmarkEngine 接口。
//  2. 引擎输出必须经过归一化：统一为 RequestEvent（Raw Event），平台只认归一化事件。
//  3. 某引擎无法提供完整 Raw Event 时，记录 capability，不伪造指标（参考设计 §8）。
//
// 接口形态对齐参考设计 §3：
//
//	type BenchmarkEngine interface {
//	    Name() string
//	    Capabilities() Capabilities
//	    Prepare(ctx context.Context, spec BenchmarkSpec) error
//	    Run(ctx context.Context, spec BenchmarkSpec, sink EventSink) (*BenchmarkResult, error)
//	    Cancel(ctx context.Context) error
//	}
package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/metric"
	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// Capabilities 声明引擎能力。调用方按 capability 决定可用功能，
// 不可用时给出明确提示而非降级伪造（参考设计 §8）。
type Capabilities struct {
	Streaming   bool `json:"streaming"`    // 流式请求
	OpenLoop    bool `json:"open_loop"`    // 开环固定速率
	Poisson     bool `json:"poisson"`      // 泊松到达
	Sweep       bool `json:"sweep"`        // 并发扫描（V1 未实现，引擎如实声明）
	Warmup      bool `json:"warmup"`       // 预热
	ProvidesITL bool `json:"provides_itl"` // 能否提供逐 token 延迟
}

// RequestEvent 是归一化的请求级事件（对应参考设计 §8 Raw Event）。
// 无论哪个引擎，最终都转成这个结构，Analyzer/Metric Pipeline 只认它。
type RequestEvent struct {
	RunID        string    `json:"run_id"`
	RequestID    string    `json:"request_id"`
	ScheduledAt  time.Time `json:"scheduled_at"`   // 计划发送时间（开环排队起点）
	StartedAt    time.Time `json:"started_at"`     // 实际发送时间
	FirstTokenAt time.Time `json:"first_token_at"` // 首 token 到达（零值=无）
	FinishedAt   time.Time `json:"finished_at"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	// ITLMs 逐 token 间隔延迟（毫秒）；引擎无法提供时为空，不伪造。
	ITLMs   []float64 `json:"itl_ms,omitempty"`
	Status  int       `json:"status"` // HTTP 状态码；0 表示不适用
	Timeout bool      `json:"timeout"`
	Error   string    `json:"error,omitempty"`
}

// Success 请求是否成功。
func (e RequestEvent) Success() bool {
	return e.Error == "" && !e.Timeout && (e.Status == 0 || e.Status < 400)
}

// E2EMs 端到端延迟（毫秒）。
func (e RequestEvent) E2EMs() float64 {
	return float64(e.FinishedAt.Sub(e.StartedAt).Microseconds()) / 1000
}

// QueueMs 排队延迟（毫秒）：计划发送 → 实际发送；闭环恒为 0。
func (e RequestEvent) QueueMs() float64 {
	if e.ScheduledAt.IsZero() || e.ScheduledAt.After(e.StartedAt) {
		return 0
	}
	return float64(e.StartedAt.Sub(e.ScheduledAt).Microseconds()) / 1000
}

// TTFTMs 首 token 延迟（毫秒）；无首 token 时返回 -1。
func (e RequestEvent) TTFTMs() float64 {
	if e.FirstTokenAt.IsZero() {
		return -1
	}
	return float64(e.FirstTokenAt.Sub(e.StartedAt).Microseconds()) / 1000
}

// TPOTMs 平均每输出 token 延迟（毫秒）；token 数不足 2 或无 TTFT 时返回 -1。
func (e RequestEvent) TPOTMs() float64 {
	ttft := e.TTFTMs()
	if ttft < 0 || e.OutputTokens < 2 {
		return -1
	}
	rest := e.E2EMs() - ttft
	if rest < 0 {
		return -1
	}
	return rest / float64(e.OutputTokens-1)
}

// EventSink 是引擎执行中的事件流回调：归一化事件边产生边上报，
// 平台侧负责落库 request_event。引擎不得直接写业务数据库（参考设计 §6）。
type EventSink func(RequestEvent)

// BenchmarkResult 是引擎一次运行的返回。
type BenchmarkResult struct {
	// EngineVersion 引擎版本（guidellm --version 等），写入 snapshot。
	EngineVersion string `json:"engine_version"`
	// Aggregates 引擎自带聚合指标（如 guidellm 报告解析出的分位值），
	// 已按 Metric Registry 的指标 ID 归一化。平台将其与事件派生指标合并；
	// 同一 metric+aggregation 冲突时以引擎自带聚合为准（引擎自身计算更精确）。
	Aggregates []metric.Value `json:"aggregates,omitempty"`
}

// BenchmarkEngine 是基准引擎统一接口（对齐参考设计 §3）。
// Run 必须尊重 ctx 取消；Cancel 用于主动中断。
type BenchmarkEngine interface {
	Name() string
	Capabilities() Capabilities
	Prepare(ctx context.Context, s spec.Spec) error
	Run(ctx context.Context, s spec.Spec, sink EventSink) (*BenchmarkResult, error)
	Cancel(ctx context.Context) error
}

var (
	mu       sync.RWMutex
	registry = map[string]func() BenchmarkEngine{}
)

// Register 注册引擎构造器。
func Register(name string, factory func() BenchmarkEngine) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = factory
}

// Get 按名获取引擎实例。
func Get(name string) (BenchmarkEngine, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("未知引擎 %q", name)
	}
	return f(), nil
}

// Names 返回已注册的引擎名。
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
