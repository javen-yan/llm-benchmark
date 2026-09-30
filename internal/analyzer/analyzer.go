// Package analyzer 是结果分析器（对应架构图平台核心层 Analyzer）。
// 输入归一化的 RequestEvent，产出聚合指标：
// TTFT/TPOT/端到端延迟的 P50/P90/P95/P99，RPS/TPS 吞吐。
package analyzer

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/spec"
	"github.com/javen-yan/llm-benchmark/internal/store"
)

// Summarize 从请求事件计算聚合指标。
func Summarize(runID string, events []engine.RequestEvent) store.Summary {
	s := store.Summary{RunID: runID, TotalRequests: len(events)}
	var (
		lat, ttft, tpot []float64
		outTokens       int64
		start, end      time.Time
	)
	for _, e := range events {
		if !e.Success {
			s.FailCount++
			continue
		}
		s.SuccessCount++
		lat = append(lat, e.LatencyMs())
		if v := e.TTFTMs(); v >= 0 {
			ttft = append(ttft, v)
		}
		if v := e.TPOTMs(); v >= 0 {
			tpot = append(tpot, v)
		}
		outTokens += int64(e.OutputTokens)
		if start.IsZero() || e.StartTime.Before(start) {
			start = e.StartTime
		}
		if end.IsZero() || e.EndTime.After(end) {
			end = e.EndTime
		}
	}
	s.OutputTokens = outTokens
	if !start.IsZero() && !end.IsZero() && end.After(start) {
		s.DurationSec = end.Sub(start).Seconds()
	}
	if s.DurationSec > 0 {
		s.RPS = float64(s.SuccessCount) / s.DurationSec
		s.TPS = float64(outTokens) / s.DurationSec
	}
	s.TTFTP50Ms, s.TTFTP99Ms = percentile(ttft, 50), percentile(ttft, 99)
	s.TPOTP50Ms, s.TPOTP99Ms = percentile(tpot, 50), percentile(tpot, 99)
	s.EndToEndP50Ms, s.EndToEndP99Ms = percentile(lat, 50), percentile(lat, 99)
	return s
}

// SnapshotPoint 把截至目前的事件聚合成一个 MetricPoint（实时曲线用）。
func SnapshotPoint(runID string, ts time.Time, events []engine.RequestEvent) store.MetricPoint {
	sum := Summarize(runID, events)
	return store.MetricPoint{
		RunID:     runID,
		Ts:        ts,
		RPS:       sum.RPS,
		TPS:       sum.TPS,
		TTFTP50Ms: sum.TTFTP50Ms,
		TPOTP50Ms: sum.TPOTP50Ms,
		P99Ms:     sum.EndToEndP99Ms,
	}
}

// SLOResult 是 SLO 判定结果。
type SLOResult struct {
	Passed     bool     `json:"passed"`
	Violations []string `json:"violations,omitempty"`
}

// CheckSLO 按 spec.slo 判定 summary 是否达标。slo 全零时视为通过。
func CheckSLO(s store.Summary, slo spec.SLO) SLOResult {
	r := SLOResult{Passed: true}
	if slo.P99LatencyMs > 0 && s.EndToEndP99Ms > slo.P99LatencyMs {
		r.Passed = false
		r.Violations = append(r.Violations,
			formatViolation("p99_latency_ms", s.EndToEndP99Ms, slo.P99LatencyMs, "ms"))
	}
	if slo.MinTPS > 0 && s.TPS < slo.MinTPS {
		r.Passed = false
		r.Violations = append(r.Violations,
			formatViolation("min_tps", s.TPS, slo.MinTPS, "tok/s"))
	}
	if slo.MaxTTFTMs > 0 && s.TTFTP99Ms > slo.MaxTTFTMs {
		r.Passed = false
		r.Violations = append(r.Violations,
			formatViolation("max_ttft_ms", s.TTFTP99Ms, slo.MaxTTFTMs, "ms"))
	}
	return r
}

func formatViolation(name string, actual, limit float64, unit string) string {
	return name + ": 实际 " + formatFloat(actual) + unit + "，要求 " + formatFloat(limit) + unit
}

func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}

// percentile 计算百分位（线性插值）；空切片返回 0。
func percentile(sorted_ []float64, p float64) float64 {
	if len(sorted_) == 0 {
		return 0
	}
	vals := append([]float64(nil), sorted_...)
	sort.Float64s(vals)
	if len(vals) == 1 {
		return vals[0]
	}
	rank := p / 100 * float64(len(vals)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return vals[lo]
	}
	frac := rank - float64(lo)
	return vals[lo]*(1-frac) + vals[hi]*frac
}
