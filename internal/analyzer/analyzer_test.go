package analyzer

import (
	"math"
	"testing"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/metric"
	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// syntheticEvents 构造 10 个合成事件：8 成功 + 2 失败。
// 成功事件 i：TTFT=100+10i ms，输出 10 token，TPOT=20ms，E2E=TTFT+180ms。
// 失败事件：无首 token，带 Error，时间落在成功事件区间内。
func syntheticEvents() []engine.RequestEvent {
	base := time.Now().Truncate(time.Second)
	events := make([]engine.RequestEvent, 0, 10)
	for i := 0; i < 8; i++ {
		ttft := time.Duration(100+i*10) * time.Millisecond
		e2e := ttft + 180*time.Millisecond
		started := base.Add(time.Duration(i*100) * time.Millisecond)
		events = append(events, engine.RequestEvent{
			RunID:        "run-1",
			RequestID:    "req-ok",
			StartedAt:    started,
			FirstTokenAt: started.Add(ttft),
			FinishedAt:   started.Add(e2e),
			InputTokens:  16,
			OutputTokens: 10,
			Status:       200,
		})
	}
	for i := 0; i < 2; i++ {
		started := base.Add(time.Duration(800+i*50) * time.Millisecond)
		events = append(events, engine.RequestEvent{
			RunID:       "run-1",
			RequestID:   "req-fail",
			StartedAt:   started,
			FinishedAt:  started.Add(100 * time.Millisecond),
			InputTokens: 16,
			Status:      500,
			Error:       "target error",
		})
	}
	return events
}

// lookup 把 Analyze 输出转成 metricID+aggregation → 值。
func lookup(vals []metric.Value) map[string]float64 {
	m := make(map[string]float64, len(vals))
	for _, v := range vals {
		m[v.MetricID+"\x00"+v.Aggregation] = v.Value
	}
	return m
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s: 期望 %.6f，实际 %.6f", name, want, got)
	}
}

// TestAnalyzeHistogram 断言直方图聚合值（已知 TTFT/TPOT/E2E 分布）。
func TestAnalyzeHistogram(t *testing.T) {
	m := lookup(Analyze("run-1", syntheticEvents()))
	approx(t, "llm.ttft p50", m["llm.ttft\x00p50"], 135)   // (130+140)/2
	approx(t, "llm.ttft p99", m["llm.ttft\x00p99"], 169.3) // 160+0.93*10
	approx(t, "llm.ttft max", m["llm.ttft\x00max"], 170)
	approx(t, "llm.tpot p50", m["llm.tpot\x00p50"], 20)
	approx(t, "llm.e2e max", m["llm.e2e\x00max"], 350)
	approx(t, "llm.e2e p99", m["llm.e2e\x00p99"], 349.3)
}

// TestAnalyzeThroughputAndError 断言吞吐、错误率与计数器。
func TestAnalyzeThroughputAndError(t *testing.T) {
	m := lookup(Analyze("run-1", syntheticEvents()))
	// 墙钟 1.05s：最早 StartedAt=base，最晚 FinishedAt=base+1050ms
	approx(t, "llm.output_tps", m["llm.output_tps\x00value"], 80/1.05)
	approx(t, "llm.request_rate", m["llm.request_rate\x00value"], 8/1.05)
	approx(t, "llm.error_rate", m["llm.error_rate\x00value"], 0.2) // 2/10 失败
	approx(t, "llm.request_count", m["llm.request_count\x00count"], 10)
	approx(t, "llm.success_count", m["llm.success_count\x00count"], 8)
	approx(t, "http.5xx", m["http.5xx\x00count"], 2)
}

// TestCheckThresholds 阈值违例检出与通过判定。
func TestCheckThresholds(t *testing.T) {
	vals := Analyze("run-1", syntheticEvents())

	bad := CheckThresholds(vals, spec.Thresholds{
		TTFTP99Ms: 100,  // 实际 169.3 → 违例
		MinTPS:    1000, // 实际 ~76 → 违例
		ErrorRate: 0.1,  // 实际 0.2 → 违例
	})
	if bad.Passed {
		t.Fatalf("应判定不通过")
	}
	if len(bad.Violations) != 3 {
		t.Fatalf("应有 3 条违例，实际 %d: %v", len(bad.Violations), bad.Violations)
	}

	good := CheckThresholds(vals, spec.Thresholds{
		TTFTP99Ms: 10000,
		MinTPS:    1,
		ErrorRate: 0.5,
	})
	if !good.Passed || len(good.Violations) != 0 {
		t.Fatalf("宽松阈值应通过，实际: %+v", good)
	}

	// 未配置阈值不误报
	empty := CheckThresholds(vals, spec.Thresholds{})
	if !empty.Passed {
		t.Fatalf("空阈值应通过，实际: %+v", empty)
	}
}

// TestAnalyzeEmpty 空事件不 panic，返回空。
func TestAnalyzeEmpty(t *testing.T) {
	if got := Analyze("run-1", nil); len(got) != 0 {
		t.Fatalf("空事件应返回空切片，实际 %d 条", len(got))
	}
	ts := time.Now()
	pt := RealtimePoint("run-1", ts, nil)
	if pt.RunID != "run-1" || !pt.Ts.Equal(ts) {
		t.Fatalf("空事件采样点应保留 RunID/Ts: %+v", pt)
	}
	if pt.RPS != 0 || pt.TPS != 0 || pt.TTFTP50Ms != 0 {
		t.Fatalf("空事件采样点数值应为 0: %+v", pt)
	}
}

// TestRealtimePoint 实时采样点字段正确。
func TestRealtimePoint(t *testing.T) {
	ts := time.Now()
	pt := RealtimePoint("run-1", ts, syntheticEvents())
	approx(t, "RPS", pt.RPS, 8/1.05)
	approx(t, "TPS", pt.TPS, 80/1.05)
	approx(t, "TTFT P50", pt.TTFTP50Ms, 135)
	approx(t, "TPOT P50", pt.TPOTP50Ms, 20)
	approx(t, "E2E P99", pt.P99Ms, 349.3)
	if pt.RunID != "run-1" || !pt.Ts.Equal(ts) {
		t.Fatalf("采样点 RunID/Ts 不对: %+v", pt)
	}
}
