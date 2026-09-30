// Package mock 是内置 Mock 引擎：零依赖的基准引擎实现。
// 用途：本机端到端验证（无 GPU、无外部 LLM 服务时跑通全链路）。
// 它按 workload 并发向 target 发真实 HTTP 请求（OpenAI Compatible SSE），
// 用真实测到的延迟计算指标；若目标不可达则退化为本地延迟模拟，保证链路可演示。
package mock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// Engine 是 Mock 引擎。
type Engine struct{}

func init() { engine.Register("mock", func() engine.Engine { return &Engine{} }) }

// Name 实现 engine.Engine。
func (*Engine) Name() string { return "mock" }

// Run 实现 engine.Engine：固定并发闭环压测。
func (*Engine) Run(ctx context.Context, s spec.Spec, progress engine.Progress) (engine.Result, error) {
	total := s.Workload.Requests
	deadline := s.Deadline()

	var (
		mu     sync.Mutex
		events = make([]engine.RequestEvent, 0, total)
		done   int64
	)
	addEvent := func(e engine.RequestEvent) {
		mu.Lock()
		events = append(events, e)
		cp := append([]engine.RequestEvent(nil), events...)
		mu.Unlock()
		n := atomic.AddInt64(&done, 1)
		if progress != nil && total > 0 {
			progress(int(n), total, cp)
		}
	}

	sem := make(chan struct{}, s.Workload.Concurrency)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if deadline > 0 {
		var cancel2 context.CancelFunc
		ctx, cancel2 = context.WithTimeout(ctx, deadline)
		defer cancel2()
	}

	client := &http.Client{Timeout: 120 * time.Second}
	reachable := probeTarget(client, s)

loop:
	for i := 0; total <= 0 || i < total; i++ {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			var ev engine.RequestEvent
			if reachable {
				ev = doHTTPRequest(client, s)
			} else {
				ev = simulateLocal(s)
			}
			addEvent(ev)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	return engine.Result{Events: events}, nil
}

// probeTarget 快速探测目标是否可达（1.5s 超时）。
func probeTarget(client *http.Client, s spec.Spec) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(s.Target.BaseURL, "/")+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
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

// doHTTPRequest 向目标服务发一次真实 SSE 请求并计时。
func doHTTPRequest(client *http.Client, s spec.Spec) (ev engine.RequestEvent) {
	ev = engine.RequestEvent{StartTime: time.Now(), InputTokens: s.Workload.InputTokens}
	defer func() { ev.EndTime = time.Now() }()

	prompt := strings.Repeat("hello ", (s.Workload.InputTokens+5)/6)
	body, _ := json.Marshal(chatRequest{
		Model:     s.Target.Model,
		Messages:  []message{{Role: "user", Content: prompt}},
		MaxTokens: s.Workload.OutputTokens,
		Stream:    s.Workload.StreamEnabled(),
	})
	url := strings.TrimSuffix(s.Target.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		ev.Error = err.Error()
		return ev
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Target.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.Target.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		ev.Error = err.Error()
		return ev
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		ev.Error = fmt.Sprintf("目标服务返回 %d", resp.StatusCode)
		return ev
	}

	tokens := 0
	if s.Workload.StreamEnabled() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if ev.FirstTokenTime.IsZero() {
				ev.FirstTokenTime = time.Now()
			}
			// 每个 SSE chunk 计 1 个 token。
			for _, ch := range chunk.Choices {
				if ch.Delta.Content != "" {
					tokens++
				}
			}
		}
	} else {
		var r struct {
			Choices []struct {
				Message message `json:"message"`
			} `json:"choices"`
			Usage struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			ev.Error = err.Error()
			return ev
		}
		tokens = r.Usage.CompletionTokens
		if tokens == 0 && len(r.Choices) > 0 {
			tokens = len(strings.Fields(r.Choices[0].Message.Content)) + 1
		}
		ev.FirstTokenTime = ev.StartTime // 非流式无首 token 计时，记为 0 延迟
	}
	if tokens <= 0 {
		tokens = s.Workload.OutputTokens
	}
	ev.OutputTokens = tokens
	ev.Success = true
	return ev
}

// simulateLocal 目标不可达时的本地延迟模拟（保证演示链路不断）。
func simulateLocal(s spec.Spec) engine.RequestEvent {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	start := time.Now()
	// TTFT 80~250ms，TPOT 15~40ms，叠加正态抖动。
	ttft := time.Duration(80+r.Intn(170)) * time.Millisecond
	time.Sleep(ttft)
	first := time.Now()
	tpot := time.Duration(15+r.Intn(25)) * time.Millisecond
	time.Sleep(tpot * time.Duration(s.Workload.OutputTokens))
	return engine.RequestEvent{
		StartTime:      start,
		FirstTokenTime: first,
		EndTime:        time.Now(),
		InputTokens:    s.Workload.InputTokens,
		OutputTokens:   s.Workload.OutputTokens,
		Success:        true,
	}
}
