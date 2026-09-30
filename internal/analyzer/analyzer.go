// Package analyzer 从归一化 RequestEvent 计算核心指标（对应参考设计 §5）。
//
// 输入：引擎产出的归一化事件；输出：按 metric.Registry 定义的 MetricValue 列表，
// 由上层写入 metric_value 表。直方图与吞吐只统计成功事件，失败事件只影响
// error_rate 与各类计数器——不伪造、不插值。
package analyzer

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/metric"
	"github.com/javen-yan/llm-benchmark/internal/spec"
	"github.com/javen-yan/llm-benchmark/internal/store"
)

// percentile 线性插值分位数；xs 必须已升序排列；空切片返回 0。
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	if p <= 0 {
		return xs[0]
	}
	if p >= 100 {
		return xs[len(xs)-1]
	}
	rank := p / 100 * float64(len(xs)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return xs[lo]
	}
	frac := rank - float64(lo)
	return xs[lo] + frac*(xs[hi]-xs[lo])
}

// histogramStats 对样本计算 min/avg/p50/p75/p90/p95/p99/max；空样本返回空 map。
func histogramStats(xs []float64) map[string]float64 {
	out := map[string]float64{}
	if len(xs) == 0 {
		return out
	}
	sort.Float64s(xs)
	sum := 0.0
	for _, v := range xs {
		sum += v
	}
	out["min"] = xs[0]
	out["avg"] = sum / float64(len(xs))
	out["p50"] = percentile(xs, 50)
	out["p75"] = percentile(xs, 75)
	out["p90"] = percentile(xs, 90)
	out["p95"] = percentile(xs, 95)
	out["p99"] = percentile(xs, 99)
	out["max"] = xs[len(xs)-1]
	return out
}

// Analyze 从归一化事件计算全部核心指标，Timestamp 统一为 now。
// 事件为空时返回空切片（不 panic）。
func Analyze(runID string, events []engine.RequestEvent) []metric.Value {
	if len(events) == 0 {
		return nil
	}
	now := time.Now()
	out := make([]metric.Value, 0, 64)
	add := func(id, agg string, v float64) {
		out = append(out, metric.Value{
			RunID:       runID,
			MetricID:    id,
			Aggregation: agg,
			Value:       v,
			Timestamp:   now,
		})
	}

	// 成功事件子集：直方图与吞吐只看成功事件，失败请求的 token 不计入有效吞吐。
	ok := make([]engine.RequestEvent, 0, len(events))
	var success, timeouts, n429, n5xx int
	var inTokens, outTokens int64
	var minStart, maxFinish time.Time
	for _, e := range events {
		if !e.StartedAt.IsZero() && (minStart.IsZero() || e.StartedAt.Before(minStart)) {
			minStart = e.StartedAt
		}
		if !e.FinishedAt.IsZero() && (maxFinish.IsZero() || e.FinishedAt.After(maxFinish)) {
			maxFinish = e.FinishedAt
		}
		if e.Timeout {
			timeouts++
		}
		if e.Success() {
			success++
			ok = append(ok, e)
			inTokens += int64(e.InputTokens)
			outTokens += int64(e.OutputTokens)
		}
		if e.Status == 429 {
			n429++
		} else if e.Status >= 500 {
			n5xx++
		}
	}

	// 直方图（仅成功事件）。
	ttfts := make([]float64, 0, len(ok))
	tpots := make([]float64, 0, len(ok))
	var itls []float64
	e2es := make([]float64, 0, len(ok))
	queues := make([]float64, 0, len(ok))
	for _, e := range ok {
		if v := e.TTFTMs(); v >= 0 {
			ttfts = append(ttfts, v)
		}
		if v := e.TPOTMs(); v >= 0 {
			tpots = append(tpots, v)
		}
		itls = append(itls, e.ITLMs...)
		e2es = append(e2es, e.E2EMs())
		queues = append(queues, e.QueueMs())
	}
	hists := []struct {
		id string
		xs []float64
	}{
		{"llm.ttft", ttfts},
		{"llm.tpot", tpots},
		{"llm.itl", itls},
		{"llm.e2e", e2es},
		{"llm.queue_time", queues},
	}
	for _, h := range hists {
		stats := histogramStats(h.xs)
		if len(stats) == 0 {
			continue // 无样本不伪造，直接跳过
		}
		for _, agg := range metric.HistogramAggregations {
			add(h.id, agg, stats[agg])
		}
	}

	// Gauge：吞吐与错误率；墙钟为最早 StartedAt → 最晚 FinishedAt。
	if wallSec := maxFinish.Sub(minStart).Seconds(); wallSec > 0 {
		add("llm.request_rate", "value", float64(success)/wallSec)
		add("llm.input_tps", "value", float64(inTokens)/wallSec)
		add("llm.output_tps", "value", float64(outTokens)/wallSec)
		add("llm.total_tps", "value", float64(inTokens+outTokens)/wallSec)
	}
	add("llm.error_rate", "value", float64(len(events)-success)/float64(len(events)))

	// Counter：累计计数。
	add("llm.request_count", "count", float64(len(events)))
	add("llm.success_count", "count", float64(success))
	add("llm.timeout_count", "count", float64(timeouts))
	add("http.429", "count", float64(n429))
	add("http.5xx", "count", float64(n5xx))
	return out
}

// SLOResult 是阈值判定结果。
type SLOResult struct {
	Passed     bool     `json:"passed"`
	Violations []string `json:"violations,omitempty"`
}

// CheckThresholds 按 metricID+aggregation 查值做 SLO 判定。
// 阈值未配置（<=0）或缺失对应指标值时，该项直接跳过，不误报。
func CheckThresholds(values []metric.Value, t spec.Thresholds) SLOResult {
	lookup := make(map[string]float64, len(values))
	for _, v := range values {
		lookup[v.MetricID+"\x00"+v.Aggregation] = v.Value
	}
	get := func(id, agg string) (float64, bool) {
		v, ok := lookup[id+"\x00"+agg]
		return v, ok
	}

	res := SLOResult{Passed: true}
	// 上限类：实际值 > 阈值则违例。
	upper := func(label, id, agg string, threshold float64) {
		if threshold <= 0 {
			return // 未配置
		}
		v, ok := get(id, agg)
		if !ok {
			return // 缺失指标，不误报
		}
		if v > threshold {
			res.Passed = false
			res.Violations = append(res.Violations,
				fmt.Sprintf("%s=%.2f 超过阈值 %.2f", label, v, threshold))
		}
	}
	// 下限类：实际值 < 阈值则违例。
	lower := func(label, id, agg string, threshold float64) {
		if threshold <= 0 {
			return
		}
		v, ok := get(id, agg)
		if !ok {
			return
		}
		if v < threshold {
			res.Passed = false
			res.Violations = append(res.Violations,
				fmt.Sprintf("%s=%.2f 低于阈值 %.2f", label, v, threshold))
		}
	}

	upper("ttft_p95(ms)", "llm.ttft", "p95", t.TTFTP95Ms)
	upper("ttft_p99(ms)", "llm.ttft", "p99", t.TTFTP99Ms)
	upper("max_ttft(ms)", "llm.ttft", "max", t.MaxTTFTMs)
	lower("output_tps(tok/s)", "llm.output_tps", "value", t.MinTPS)
	upper("error_rate", "llm.error_rate", "value", t.ErrorRate)
	upper("e2e_p99(ms)", "llm.e2e", "p99", t.P99LatencyMs)
	return res
}

// RealtimePoint 供运行中实时曲线：从当前已产生事件计算采样点
// （RPS/TPS/TTFT P50/TPOT P50/E2E P99）。事件为空时返回零值点
// （RunID/Ts 照填，不 panic）。
func RealtimePoint(runID string, ts time.Time, events []engine.RequestEvent) store.MetricPoint {
	pt := store.MetricPoint{RunID: runID, Ts: ts}
	if len(events) == 0 {
		return pt
	}
	var success int
	var outTokens int64
	var minStart, maxFinish time.Time
	ttfts := make([]float64, 0, len(events))
	tpots := make([]float64, 0, len(events))
	e2es := make([]float64, 0, len(events))
	for _, e := range events {
		if !e.StartedAt.IsZero() && (minStart.IsZero() || e.StartedAt.Before(minStart)) {
			minStart = e.StartedAt
		}
		if !e.FinishedAt.IsZero() && (maxFinish.IsZero() || e.FinishedAt.After(maxFinish)) {
			maxFinish = e.FinishedAt
		}
		if !e.Success() {
			continue
		}
		success++
		outTokens += int64(e.OutputTokens)
		if v := e.TTFTMs(); v >= 0 {
			ttfts = append(ttfts, v)
		}
		if v := e.TPOTMs(); v >= 0 {
			tpots = append(tpots, v)
		}
		e2es = append(e2es, e.E2EMs())
	}
	if wallSec := maxFinish.Sub(minStart).Seconds(); wallSec > 0 {
		pt.RPS = float64(success) / wallSec
		pt.TPS = float64(outTokens) / wallSec
	}
	sort.Float64s(ttfts)
	sort.Float64s(tpots)
	sort.Float64s(e2es)
	pt.TTFTP50Ms = percentile(ttfts, 50)
	pt.TPOTP50Ms = percentile(tpots, 50)
	pt.P99Ms = percentile(e2es, 99)
	return pt
}
