package analyzer

import (
	"testing"

	"github.com/javen-yan/llm-benchmark/internal/store"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/spec"
)

func TestPercentile(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(xs, 50); got != 5.5 {
		t.Errorf("p50=%v, 期望 5.5（线性插值）", got)
	}
	if got := percentile(xs, 99); got < 9.9 || got > 10 {
		t.Errorf("p99=%v, 期望约 9.91", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("空切片 p50=%v, 期望 0", got)
	}
}

func TestSummarize(t *testing.T) {
	now := time.Now()
	events := []engine.RequestEvent{
		{StartTime: now, EndTime: now.Add(1000 * time.Millisecond), FirstTokenTime: now.Add(100 * time.Millisecond), InputTokens: 10, OutputTokens: 20, Success: true},
		{StartTime: now, EndTime: now.Add(1200 * time.Millisecond), FirstTokenTime: now.Add(200 * time.Millisecond), InputTokens: 10, OutputTokens: 20, Success: true},
		{StartTime: now, EndTime: now.Add(1100 * time.Millisecond), Error: "boom"},
	}
	s := Summarize("run-1", events)
	if s.TotalRequests != 3 || s.SuccessCount != 2 || s.FailCount != 1 {
		t.Errorf("计数错误: %+v", s)
	}
	if s.DurationSec <= 0 {
		t.Errorf("DurationSec 应 > 0, 得 %v", s.DurationSec)
	}
	if s.TTFTP50Ms <= 0 {
		t.Errorf("TTFT 应 > 0, 得 %v", s.TTFTP50Ms)
	}
	if s.RPS <= 0 || s.TPS <= 0 {
		t.Errorf("RPS/TPS 应 > 0, 得 %v/%v", s.RPS, s.TPS)
	}
}

func TestCheckSLO(t *testing.T) {
	s := store.Summary{EndToEndP99Ms: 100, TPS: 50}
	if r := CheckSLO(s, spec.SLO{}); !r.Passed {
		t.Error("无 SLO 应通过")
	}
	slo := spec.SLO{P99LatencyMs: 50, MinTPS: 100}
	r := CheckSLO(s, slo)
	if r.Passed || len(r.Violations) != 2 {
		t.Errorf("应有 2 项违例, 得 %+v", r)
	}
}
