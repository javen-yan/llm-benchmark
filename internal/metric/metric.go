// Package metric 是 Metric Registry（对应参考设计 §7）。
//
// 核心原则（参考设计 §21-4）：指标动态注册，禁止把 TTFT P95/P99 写成固定数据库列。
// 所有指标先在 Registry 注册 Definition（含类型/单位/聚合方式），
// Analyzer 按 Definition 把归一化事件聚合成 MetricValue 行写入 metric_value 表。
package metric

import (
	"fmt"
	"sync"
	"time"
)

// MetricType 指标类型。
type MetricType string

const (
	TypeCounter   MetricType = "counter"   // 累计计数
	TypeGauge     MetricType = "gauge"     // 瞬时值
	TypeHistogram MetricType = "histogram" // 分布
)

// 指标来源。
const (
	SourceBenchmark = "benchmark" // 压测引擎
	SourceCollector = "collector" // 被动采集
	SourceProbe     = "probe"     // 主动探测
)

// 指标作用域。
const (
	ScopeRequest = "request" // 请求级
	ScopeRun     = "run"     // 运行级
	ScopeHost    = "host"    // 主机级
)

// 直方图默认聚合（参考设计 §7）。
var HistogramAggregations = []string{"min", "avg", "p50", "p75", "p90", "p95", "p99", "max"}

// Definition 是指标定义。
type Definition struct {
	ID           string     `json:"id"`           // 如 llm.ttft
	Name         string     `json:"name"`         // 如 Time To First Token
	Type         MetricType `json:"type"`         // counter / gauge / histogram
	Unit         string     `json:"unit"`         // ms, tok/s, rps, ratio, count
	Source       string     `json:"source"`       // benchmark / collector / probe
	Scope        string     `json:"scope"`        // request / run / host
	Aggregations []string   `json:"aggregations"` // histogram 用；counter/gauge 为 ["value"/"count"]
}

// Value 是一条聚合指标值（对应 metric_value 表的一行）。
type Value struct {
	RunID       string    `json:"run_id"`
	MetricID    string    `json:"metric_id"`
	Aggregation string    `json:"aggregation"` // min/avg/p50/.../value/count
	Value       float64   `json:"value"`
	Labels      string    `json:"labels,omitempty"` // JSON
	Timestamp   time.Time `json:"timestamp"`
}

// Registry 是指标注册表（线程安全）。
type Registry struct {
	mu   sync.RWMutex
	defs map[string]Definition
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{defs: map[string]Definition{}}
}

// Register 注册指标定义；ID 重复返回错误。
func (r *Registry) Register(d Definition) error {
	if d.ID == "" {
		return fmt.Errorf("指标 ID 不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.defs[d.ID]; ok {
		return fmt.Errorf("指标 %q 已注册", d.ID)
	}
	r.defs[d.ID] = d
	return nil
}

// MustRegister 注册，失败时 panic（内置指标用）。
func (r *Registry) MustRegister(d Definition) {
	if err := r.Register(d); err != nil {
		panic(err)
	}
}

// Get 按 ID 取定义。
func (r *Registry) Get(id string) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[id]
	return d, ok
}

// All 返回全部定义（按 ID 排序稳定输出）。
func (r *Registry) All() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	// 简单插入排序保证稳定
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// DefaultRegistry 返回带核心指标的注册表（对应参考设计 §7 Core Metrics）。
func DefaultRegistry() *Registry {
	r := NewRegistry()
	hist := func(id, name, unit, source, scope string) {
		r.MustRegister(Definition{
			ID: id, Name: name, Type: TypeHistogram, Unit: unit,
			Source: source, Scope: scope, Aggregations: HistogramAggregations,
		})
	}
	gauge := func(id, name, unit, source, scope string) {
		r.MustRegister(Definition{
			ID: id, Name: name, Type: TypeGauge, Unit: unit,
			Source: source, Scope: scope, Aggregations: []string{"value"},
		})
	}
	counter := func(id, name, source, scope string) {
		r.MustRegister(Definition{
			ID: id, Name: name, Type: TypeCounter, Unit: "count",
			Source: source, Scope: scope, Aggregations: []string{"count"},
		})
	}

	// Latency（histogram，ms）
	hist("llm.ttft", "Time To First Token", "ms", SourceBenchmark, ScopeRequest)
	hist("llm.tpot", "Time Per Output Token", "ms", SourceBenchmark, ScopeRequest)
	hist("llm.itl", "Inter-Token Latency", "ms", SourceBenchmark, ScopeRequest)
	hist("llm.e2e", "End-to-End Latency", "ms", SourceBenchmark, ScopeRequest)
	hist("llm.queue_time", "Queue Time", "ms", SourceBenchmark, ScopeRequest)
	// Throughput（gauge）
	gauge("llm.request_rate", "Request Rate", "rps", SourceBenchmark, ScopeRun)
	gauge("llm.input_tps", "Input Tokens Per Second", "tok/s", SourceBenchmark, ScopeRun)
	gauge("llm.output_tps", "Output Tokens Per Second", "tok/s", SourceBenchmark, ScopeRun)
	gauge("llm.total_tps", "Total Tokens Per Second", "tok/s", SourceBenchmark, ScopeRun)
	// Reliability
	gauge("llm.error_rate", "Error Rate", "ratio", SourceBenchmark, ScopeRun)
	counter("llm.request_count", "Request Count", SourceBenchmark, ScopeRun)
	counter("llm.success_count", "Success Count", SourceBenchmark, ScopeRun)
	counter("llm.timeout_count", "Timeout Count", SourceBenchmark, ScopeRun)
	counter("http.429", "HTTP 429 Count", SourceBenchmark, ScopeRun)
	counter("http.5xx", "HTTP 5xx Count", SourceBenchmark, ScopeRun)
	return r
}
