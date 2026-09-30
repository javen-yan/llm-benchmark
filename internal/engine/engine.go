// Package engine 是引擎适配层（对应架构图 Benchmark Engine）。
// 平台不依赖具体引擎实现：所有引擎实现 Engine 接口，输出归一化的 Result。
// Plugin Manager 在 Phase 1 简化为这里的注册表；vLLM / Native Engine 预留接口占位。
package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// RequestEvent 是归一化的请求级事件（对应架构图"事件归一化层"）。
// 无论哪个引擎，最终都转成这个结构，Analyzer 只认它。
type RequestEvent struct {
	StartTime      time.Time
	FirstTokenTime time.Time // 零值表示未收到首 token（失败或非流式无首 token 计时）
	EndTime        time.Time
	OutputTokens   int
	InputTokens    int
	Success        bool
	Error          string
}

// LatencyMs 端到端延迟（毫秒）。
func (e RequestEvent) LatencyMs() float64 {
	return float64(e.EndTime.Sub(e.StartTime).Microseconds()) / 1000
}

// TTFTMs 首 token 延迟（毫秒）；无首 token 时返回 -1。
func (e RequestEvent) TTFTMs() float64 {
	if e.FirstTokenTime.IsZero() {
		return -1
	}
	return float64(e.FirstTokenTime.Sub(e.StartTime).Microseconds()) / 1000
}

// TPOTMs 每输出 token 延迟（毫秒）；token 数不足 2 时返回 -1。
func (e RequestEvent) TPOTMs() float64 {
	ttft := e.TTFTMs()
	if ttft < 0 || e.OutputTokens < 2 {
		return -1
	}
	rest := e.LatencyMs() - ttft
	if rest < 0 {
		return -1
	}
	return rest / float64(e.OutputTokens-1)
}

// Progress 是引擎执行中的进度回调（已完成请求数/总请求数/截至目前的事件）。
type Progress func(done, total int, events []RequestEvent)

// Result 是引擎一次运行的归一化结果。
type Result struct {
	Events []RequestEvent
	// Summary 预聚合指标：引擎已做聚合时（如 GuideLLM）直接给出，
	// 为空则由 Analyzer 从 Events 计算。
	Summary *AggStats
}

// AggStats 预聚合指标（与 store.Summary 对齐，便于 RunManager 直接落库）。
type AggStats struct {
	TotalRequests int
	SuccessCount  int
	FailCount     int
	DurationSec   float64
	RPS           float64
	TPS           float64
	TTFTP50Ms     float64
	TTFTP99Ms     float64
	TPOTP50Ms     float64
	TPOTP99Ms     float64
	E2EP50Ms      float64
	E2EP99Ms      float64
	OutputTokens  int64
}

// Engine 是基准引擎接口。Run 必须尊重 ctx 取消。
type Engine interface {
	// Name 返回引擎名（guidellm / mock / vllm / native）。
	Name() string
	// Run 执行一次 benchmark，期间通过 progress 上报进度。
	Run(ctx context.Context, s spec.Spec, progress Progress) (Result, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]func() Engine{}
)

// Register 注册引擎构造器（Plugin Manager 的 Phase 1 形态）。
func Register(name string, factory func() Engine) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = factory
}

// Get 按名获取引擎实例。
func Get(name string) (Engine, error) {
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
