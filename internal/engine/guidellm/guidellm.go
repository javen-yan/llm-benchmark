// Package guidellm 是 GuideLLM 适配器：把 Spec 翻译成 `guidellm` CLI 参数，
// 执行其 benchmark 子命令并解析 JSON 报告，归一化为平台指标（metric.Value）。
// 平台只依赖本接口，不依赖 GuideLLM 内部实现；guidellm 未安装时给出明确中文提示。
package guidellm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/metric"
	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// Adapter 是 GuideLLM 引擎适配器。
type Adapter struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

func init() { engine.Register("guidellm", func() engine.BenchmarkEngine { return &Adapter{} }) }

// Name 实现 engine.BenchmarkEngine。
func (*Adapter) Name() string { return "guidellm" }

// Capabilities 实现 engine.BenchmarkEngine。
// Poisson 保守填 false：未确认当前 guidellm 版本的 --rate-type 是否支持
// poisson 分布，开环场景已用 constant 速率覆盖。
func (*Adapter) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Streaming:   true,
		OpenLoop:    true,
		Poisson:     false,
		Sweep:       false,
		Warmup:      false,
		ProvidesITL: false,
	}
}

// Prepare 实现 engine.BenchmarkEngine：检查 guidellm 可执行文件是否存在。
func (*Adapter) Prepare(_ context.Context, _ spec.Spec) error {
	if _, err := exec.LookPath("guidellm"); err != nil {
		return fmt.Errorf("未找到 guidellm 可执行文件，请先 pip install guidellm")
	}
	return nil
}

// Run 实现 engine.BenchmarkEngine：调 guidellm CLI 跑 benchmark，解析报告为聚合指标。
// GuideLLM 不提供逐请求事件流，sink 在此引擎中不使用。
func (a *Adapter) Run(ctx context.Context, s spec.Spec, _ engine.EventSink) (*engine.BenchmarkResult, error) {
	bin, err := exec.LookPath("guidellm")
	if err != nil {
		return nil, fmt.Errorf("未找到 guidellm 可执行文件，请先 pip install guidellm")
	}
	outDir, err := os.MkdirTemp("", "guidellm-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outDir)
	reportPath := filepath.Join(outDir, "report.json")

	// exec.CommandContext 随 ctx 取消自动杀掉子进程；Cancel 额外提供主动取消入口。
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, buildArgs(s, reportPath)...)
	cmd.Env = append(os.Environ(), "GUIDELLM__DISABLE_TELEMETRY=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("guidellm 执行失败: %w\n输出:\n%s", err, truncate(string(out), 4000))
	}

	aggs, err := parseReport(reportPath)
	if err != nil {
		return nil, err
	}
	return &engine.BenchmarkResult{
		EngineVersion: engineVersion(bin),
		Aggregates:    aggs,
	}, nil
}

// Cancel 实现 engine.BenchmarkEngine：取消正在执行的 guidellm 子进程。
func (a *Adapter) Cancel(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	return nil
}

// buildArgs 把 Spec 翻译成 `guidellm benchmark` CLI 参数。
func buildArgs(s spec.Spec, reportPath string) []string {
	args := []string{
		"benchmark",
		"--target", s.Target.Endpoint,
		"--model", s.Target.Model,
		"--data", fmt.Sprintf("prompt_tokens=%d,output_tokens=%d", s.Dataset.InputTokens, s.Dataset.OutputTokens),
		"--output-path", reportPath,
	}
	if s.Target.APIKey != "" {
		args = append(args, "--target-key", s.Target.APIKey)
	}
	if s.Load.Requests > 0 {
		args = append(args, "--num-prompts", strconv.Itoa(s.Load.Requests))
	}
	if d, err := s.Load.DurationParsed(); err == nil && d > 0 {
		args = append(args, "--max-seconds", strconv.FormatFloat(d.Seconds(), 'f', 0, 64))
	}
	switch s.Load.ModeName() {
	case spec.LoadOpenLoop, spec.LoadPoisson:
		args = append(args, "--rate-type", "constant", "--rate", strconv.FormatFloat(s.Load.Rate, 'f', -1, 64))
	default: // closed_loop：用 constant 速率近似，速率为并发数
		args = append(args, "--rate-type", "constant", "--rate", strconv.Itoa(s.Load.Concurrency))
	}
	if !s.Load.StreamEnabled() {
		args = append(args, "--no-stream")
	}
	return args
}

// parseReport 解析 guidellm JSON 报告为归一化聚合指标。
// 对字段名做宽容匹配（不同版本字段可能差异）；延迟类字段做秒→毫秒启发式换算
// （值落在 (0,10) 区间视为秒）。RunID 由调用方补填，Timestamp 取解析时刻。
func parseReport(path string) ([]metric.Value, error) {
	return parseReportAt(path, time.Now())
}

func parseReportAt(path string, now time.Time) ([]metric.Value, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 guidellm 报告失败: %w", err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("解析 guidellm 报告失败: %w", err)
	}
	// benchmarks 可能是数组（多 rate）或单个对象；取第一个。
	b := report
	if arr, ok := report["benchmarks"].([]any); ok && len(arr) > 0 {
		if m, ok := arr[0].(map[string]any); ok {
			b = m
		}
	}
	num := func(keys ...string) (float64, bool) {
		for _, k := range keys {
			switch n := b[k].(type) {
			case float64:
				return n, true
			case int:
				return float64(n), true
			case json.Number:
				if f, err := n.Float64(); err == nil {
					return f, true
				}
			}
		}
		return 0, false
	}
	// 秒→毫秒启发式换算：延迟值在 (0,10) 视为秒。
	toMs := func(v float64) float64 {
		if v > 0 && v < 10 {
			return v * 1000
		}
		return v
	}
	type mapping struct {
		id   string
		agg  string
		ms   bool // 是否做秒→毫秒换算
		keys []string
	}
	mappings := []mapping{
		{"llm.ttft", "p50", true, []string{"ttft_p50", "time_to_first_token_p50"}},
		{"llm.ttft", "p99", true, []string{"ttft_p99", "time_to_first_token_p99"}},
		{"llm.tpot", "p50", true, []string{"tpot_p50", "time_per_output_token_p50"}},
		{"llm.tpot", "p99", true, []string{"tpot_p99", "time_per_output_token_p99"}},
		{"llm.e2e", "p50", true, []string{"request_latency_p50", "e2e_p50", "end_to_end_latency_p50"}},
		{"llm.e2e", "p99", true, []string{"request_latency_p99", "e2e_p99", "end_to_end_latency_p99"}},
		{"llm.request_rate", "value", false, []string{"request_throughput", "requests_per_second"}},
		{"llm.output_tps", "value", false, []string{"output_token_throughput", "tokens_per_second", "output_tokens_per_second"}},
	}
	var out []metric.Value
	for _, m := range mappings {
		v, ok := num(m.keys...)
		if !ok {
			continue
		}
		if m.ms {
			v = toMs(v)
		}
		out = append(out, metric.Value{
			MetricID:    m.id,
			Aggregation: m.agg,
			Value:       v,
			Timestamp:   now,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("guidellm 报告中未找到可解析的聚合指标（报告可能为空或格式不兼容）")
	}
	return out, nil
}

// engineVersion 尝试获取 guidellm 版本号，失败返回空字符串。
func engineVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
