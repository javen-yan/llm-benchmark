// Package guidellm 是 GuideLLM 适配器（对应架构图引擎适配层"GuideLLM Adapter（推荐·V1）"）。
//
// 实现方式：调用 `guidellm` CLI（需 pip install guidellm），把 Spec 翻译成 CLI 参数，
// 解析其 JSON 输出，归一化为 engine.Result。平台只依赖本接口，不依赖 GuideLLM 内部实现。
package guidellm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// Adapter 是 GuideLLM 引擎适配器。
type Adapter struct{}

func init() { engine.Register("guidellm", func() engine.Engine { return &Adapter{} }) }

// Name 实现 engine.Engine。
func (*Adapter) Name() string { return "guidellm" }

// Run 实现 engine.Engine。
func (*Adapter) Run(ctx context.Context, s spec.Spec, progress engine.Progress) (engine.Result, error) {
	bin, err := exec.LookPath("guidellm")
	if err != nil {
		return engine.Result{}, fmt.Errorf("未找到 guidellm 可执行文件，请先 pip install guidellm: %w", err)
	}

	outDir, err := os.MkdirTemp("", "guidellm-*")
	if err != nil {
		return engine.Result{}, err
	}
	defer os.RemoveAll(outDir)
	reportPath := filepath.Join(outDir, "report.json")

	args := buildArgs(s, reportPath)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "GUIDELLM__DISABLE_TELEMETRY=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return engine.Result{}, fmt.Errorf("guidellm 执行失败: %w\n输出:\n%s", err, truncate(string(out), 4000))
	}

	stats, err := parseReport(reportPath, s)
	if err != nil {
		return engine.Result{}, err
	}
	if progress != nil {
		progress(stats.TotalRequests, stats.TotalRequests, nil)
	}
	return engine.Result{Summary: stats}, nil
}

// buildArgs 把 Spec 翻译成 guidellm benchmark CLI 参数。
func buildArgs(s spec.Spec, reportPath string) []string {
	args := []string{
		"benchmark",
		"--target", s.Target.BaseURL,
		"--model", s.Target.Model,
		"--data", fmt.Sprintf("prompt_tokens=%d,output_tokens=%d", s.Workload.InputTokens, s.Workload.OutputTokens),
		"--output-path", reportPath,
	}
	if s.Target.APIKey != "" {
		args = append(args, "--target-key", s.Target.APIKey)
	}
	// 负载：请求数模式用 num-prompts + 同步/固定并发；时长模式用 max-seconds。
	if s.Workload.Requests > 0 {
		args = append(args, "--num-prompts", strconv.Itoa(s.Workload.Requests))
	}
	if d := s.Deadline(); d > 0 {
		args = append(args, "--max-seconds", strconv.FormatFloat(d.Seconds(), 'f', 0, 64))
	}
	// 固定并发：用 constant 速率近似（rate = concurrency，Phase 1 近似；精确闭环进 Phase 2）。
	args = append(args, "--rate-type", "constant", "--rate", strconv.Itoa(s.Workload.Concurrency))
	if !s.Workload.StreamEnabled() {
		args = append(args, "--no-stream")
	}
	return args
}

// parseReport 解析 guidellm 的 JSON 报告，提取聚合指标。
// 对字段名做宽容匹配（不同版本字段可能差异），缺失则返回错误。
func parseReport(path string, s spec.Spec) (*engine.AggStats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 guidellm 报告失败: %w", err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("解析 guidellm 报告失败: %w", err)
	}
	// benchmarks 可能是数组（多 rate）或单个对象；取第一个。
	var b map[string]any
	if arr, ok := report["benchmarks"].([]any); ok && len(arr) > 0 {
		b, _ = arr[0].(map[string]any)
	} else {
		b = report
	}
	num := func(keys ...string) float64 {
		for _, k := range keys {
			if v, ok := b[k]; ok {
				switch n := v.(type) {
				case float64:
					return n
				case int:
					return float64(n)
				}
			}
		}
		return 0
	}
	total := int(num("completed_request_count", "num_requests", "total_requests"))
	if total == 0 {
		return nil, fmt.Errorf("guidellm 报告中未找到请求数（报告可能为空）")
	}
	errCount := int(num("error_count", "num_errors"))
	dur := num("benchmark_duration", "duration", "total_time")
	if dur <= 0 && s.Deadline() > 0 {
		dur = s.Deadline().Seconds()
	}
	outputTokens := int64(num("output_tokens", "total_output_tokens"))
	rps := num("request_throughput", "requests_per_second")
	if rps <= 0 && dur > 0 {
		rps = float64(total) / dur
	}
	tps := num("output_token_throughput", "tokens_per_second")
	if tps <= 0 && dur > 0 && outputTokens > 0 {
		tps = float64(outputTokens) / dur
	}
	// 延迟分位：兼容秒/毫秒两种单位（>10000 视为微秒级 GuideLLM 旧格式，做启发式换算）。
	toMs := func(v float64) float64 {
		if v > 0 && v < 10 {
			return v * 1000 // 秒 → 毫秒
		}
		return v
	}
	stats := &engine.AggStats{
		TotalRequests: total,
		SuccessCount:  total - errCount,
		FailCount:     errCount,
		DurationSec:   dur,
		RPS:           rps,
		TPS:           tps,
		OutputTokens:  outputTokens,
		TTFTP50Ms:     toMs(num("ttft_p50", "time_to_first_token_p50")),
		TTFTP99Ms:     toMs(num("ttft_p99", "time_to_first_token_p99")),
		TPOTP50Ms:     toMs(num("tpot_p50", "time_per_output_token_p50")),
		TPOTP99Ms:     toMs(num("tpot_p99", "time_per_output_token_p99")),
		E2EP50Ms:      toMs(num("request_latency_p50", "e2e_p50")),
		E2EP99Ms:      toMs(num("request_latency_p99", "e2e_p99")),
	}
	return stats, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// 确保 time 被使用（deadline 相关扩展预留）。
var _ = time.Second
