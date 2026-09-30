// Package mock 是内置 Mock 引擎：零依赖的基准引擎实现。
//
// 用途：本机端到端验证（无 GPU、无外部 LLM 服务时跑通全链路）。
// 它按 Spec 的负载策略（closed_loop / open_loop / poisson）向目标服务
// 发真实 HTTP 请求（OpenAI Compatible /v1/chat/completions），逐事件上报
// RequestEvent；目标不可达时退化为本地延迟模拟，保证链路可演示。
// 指标聚合全部由平台根据事件计算，引擎本身不产出 Aggregates。
package mock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// Engine 是 Mock 引擎。
type Engine struct {
	client *http.Client

	reachable atomic.Bool // Prepare 探测到的目标可达性
	probed    atomic.Bool // 是否已探测过

	mu     sync.Mutex
	cancel context.CancelFunc
}

// New 构造 Mock 引擎。
func New() *Engine {
	return &Engine{client: &http.Client{Timeout: 120 * time.Second}}
}

func init() { engine.Register("mock", func() engine.BenchmarkEngine { return New() }) }

// Name 实现 engine.BenchmarkEngine。
func (*Engine) Name() string { return "mock" }

// Capabilities 实现 engine.BenchmarkEngine。
func (*Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Streaming:   true,
		OpenLoop:    true,
		Poisson:     true,
		Sweep:       false,
		Warmup:      true,
		ProvidesITL: true,
	}
}

// Prepare 探测目标可达性（GET {endpoint}/health，1.5s 超时）。
// 不可达也不报错：Run 时自动退化为本地延迟模拟。
func (e *Engine) Prepare(ctx context.Context, s spec.Spec) error {
	reqCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	url := strings.TrimSuffix(s.Target.Endpoint, "/") + "/health"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		e.reachable.Store(false)
		e.probed.Store(true)
		return nil
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.reachable.Store(false)
	} else {
		resp.Body.Close()
		e.reachable.Store(resp.StatusCode < 500)
	}
	e.probed.Store(true)
	return nil
}

// Run 实现 engine.BenchmarkEngine：先预热（可选），再按负载策略执行。
func (e *Engine) Run(ctx context.Context, s spec.Spec, sink engine.EventSink) (*engine.BenchmarkResult, error) {
	if !e.probed.Load() {
		// 未调过 Prepare 时补一次探测，保证可达性已知。
		if err := e.Prepare(ctx, s); err != nil {
			return nil, err
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancel = cancel
	e.mu.Unlock()
	defer cancel()

	// 预热：闭环跑满时长，事件直接丢弃不计入指标。
	if d, err := s.Warmup.DurationParsed(); err != nil {
		return nil, err
	} else if d > 0 {
		wctx, wcancel := context.WithTimeout(runCtx, d)
		e.closedLoop(wctx, s, nil)
		wcancel()
	}

	switch s.Load.ModeName() {
	case spec.LoadClosedLoop:
		e.closedLoop(runCtx, s, sink)
	case spec.LoadOpenLoop:
		e.openLoop(runCtx, s, sink, false)
	case spec.LoadPoisson:
		e.openLoop(runCtx, s, sink, true)
	default:
		return nil, fmt.Errorf("mock 引擎不支持负载模式 %q", s.Load.ModeName())
	}
	return &engine.BenchmarkResult{}, nil
}

// Cancel 实现 engine.BenchmarkEngine：取消正在进行的 Run。
func (e *Engine) Cancel(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
	return nil
}

// closedLoop 固定并发闭环：跑满 Requests 个或 Duration 超时。
// sink 为 nil 时丢弃事件（预热用）。
func (e *Engine) closedLoop(ctx context.Context, s spec.Spec, sink engine.EventSink) {
	deadline, _ := s.Load.DurationParsed()
	var dl <-chan time.Time
	if deadline > 0 {
		t := time.NewTimer(deadline)
		defer t.Stop()
		dl = t.C
	}
	sem := make(chan struct{}, s.Load.Concurrency)
	var wg sync.WaitGroup
	var seq atomic.Int64
	for i := 0; ; i++ {
		if s.Load.Requests > 0 && i >= s.Load.Requests {
			break
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-dl: // deadline 未设置时 dl 为 nil，此分支永不触发
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		id := seq.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ev := e.doRequest(ctx, s, id, time.Now())
			if sink != nil {
				sink(ev)
			}
		}()
	}
	wg.Wait()
}

// openLoop 开环：按 Rate(req/s) 恒定速率发送；poisson=true 时到达间隔服从
// 指数分布（均值 1/Rate 秒）。并发上限为 Concurrency；Requests 或 Duration
// 先达到者结束。ScheduledAt 为计划发送时间，StartedAt 为实际发送时间。
func (e *Engine) openLoop(ctx context.Context, s spec.Spec, sink engine.EventSink, poisson bool) {
	deadline, _ := s.Load.DurationParsed()
	var dl <-chan time.Time
	if deadline > 0 {
		t := time.NewTimer(deadline)
		defer t.Stop()
		dl = t.C
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano() ^ s.Dataset.Seed))
	interval := time.Duration(float64(time.Second) / s.Load.Rate)
	next := time.Now()
	sem := make(chan struct{}, s.Load.Concurrency)
	var wg sync.WaitGroup
	var seq atomic.Int64
	for i := 0; ; i++ {
		if s.Load.Requests > 0 && i >= s.Load.Requests {
			break
		}
		if poisson {
			// 指数分布：-ln(1-U)/rate；Float64()∈[0,1)，1-U∈(0,1]，安全。
			next = next.Add(time.Duration(-math.Log(1-rng.Float64()) * float64(interval)))
		} else {
			next = next.Add(interval)
		}
		if wait := time.Until(next); wait > 0 {
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case <-dl:
				wg.Wait()
				return
			case <-time.After(wait):
			}
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-dl:
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		id := seq.Add(1)
		scheduled := next
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ev := e.doRequest(ctx, s, id, scheduled)
			if sink != nil {
				sink(ev)
			}
		}()
	}
	wg.Wait()
}

// doRequest 执行一次请求并生成归一化事件。RequestID 用原子计数器保证唯一。
func (e *Engine) doRequest(ctx context.Context, s spec.Spec, id int64, scheduled time.Time) (ev engine.RequestEvent) {
	ev = engine.RequestEvent{
		RequestID:   fmt.Sprintf("req-%d", id),
		ScheduledAt: scheduled,
		StartedAt:   time.Now(),
		InputTokens: s.Dataset.InputTokens,
	}
	defer func() { ev.FinishedAt = time.Now() }()

	if !e.reachable.Load() {
		e.simulateLocal(ctx, s, &ev)
		return ev
	}

	prompt := strings.Repeat("hello ", (s.Dataset.InputTokens+5)/6)
	body, _ := json.Marshal(chatRequest{
		Model:     s.Target.Model,
		Messages:  []message{{Role: "user", Content: prompt}},
		MaxTokens: s.Dataset.OutputTokens,
		Stream:    s.Load.StreamEnabled(),
	})
	url := strings.TrimSuffix(s.Target.Endpoint, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		ev.Error = err.Error()
		return ev
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Target.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.Target.APIKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			ev.Timeout = true
		}
		ev.Error = err.Error()
		return ev
	}
	defer resp.Body.Close()
	ev.Status = resp.StatusCode
	if resp.StatusCode >= 400 {
		ev.Error = fmt.Sprintf("目标服务返回 HTTP %d", resp.StatusCode)
		return ev
	}

	tokens := 0
	if s.Load.StreamEnabled() {
		tokens = e.readSSE(resp.Body, &ev)
	} else {
		var r chatCompletionResp
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			ev.Error = fmt.Sprintf("解析响应失败: %v", err)
			return ev
		}
		tokens = r.Usage.CompletionTokens
		if tokens <= 0 && len(r.Choices) > 0 {
			tokens = len(strings.Fields(r.Choices[0].Message.Content)) + 1
		}
	}
	if tokens <= 0 {
		tokens = s.Dataset.OutputTokens
	}
	ev.OutputTokens = tokens
	return ev
}

// readSSE 逐 chunk 读取 SSE 流：记录首 token 到达时间与相邻 chunk 间隔（毫秒）。
// 返回统计到的输出 token 数（每个含有效 content 的 chunk 计 1 个）。
func (e *Engine) readSSE(body io.Reader, ev *engine.RequestEvent) int {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	tokens := 0
	var prev time.Time
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // 无效 chunk 跳过，不计入
		}
		now := time.Now()
		if ev.FirstTokenAt.IsZero() {
			ev.FirstTokenAt = now
		} else {
			ev.ITLMs = append(ev.ITLMs, float64(now.Sub(prev).Microseconds())/1000)
		}
		prev = now
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				tokens++
			}
		}
	}
	if err := sc.Err(); err != nil && tokens == 0 {
		ev.Error = fmt.Sprintf("读取 SSE 流失败: %v", err)
	}
	return tokens
}

// simulateLocal 目标不可达时的本地延迟模拟：TTFT 80~250ms，TPOT 15~40ms。
// 事件照常上报（含合成的逐 token 间隔），保证演示链路不断。
func (e *Engine) simulateLocal(ctx context.Context, s spec.Spec, ev *engine.RequestEvent) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	if !sleep(time.Duration(80+rng.Intn(170)) * time.Millisecond) {
		return
	}
	ev.FirstTokenAt = time.Now()
	for i := 0; i < s.Dataset.OutputTokens; i++ {
		start := time.Now()
		if !sleep(time.Duration(15+rng.Intn(25)) * time.Millisecond) {
			break
		}
		ev.ITLMs = append(ev.ITLMs, float64(time.Since(start).Microseconds())/1000)
	}
	ev.OutputTokens = s.Dataset.OutputTokens
	ev.Status = http.StatusOK
}

// chatRequest 是 OpenAI Compatible 的 chat completions 请求体。
type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// sseChunk 是 SSE 流中的单个 data chunk。
type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// chatCompletionResp 是非流式 chat completions 响应。
type chatCompletionResp struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
