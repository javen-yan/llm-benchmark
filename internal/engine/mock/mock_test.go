package mock

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// fakeTarget 起一个 OpenAI Compatible 假目标：
// /health 返回 200；/v1/chat/completions 返回 3 个 SSE chunk + [DONE]。
func fakeTarget(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok%d\"}}]}\n\n", i)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	return httptest.NewServer(mux)
}

// testSpec 构造测试用 Spec。
func testSpec(endpoint string, requests int) spec.Spec {
	stream := true
	return spec.Spec{
		Name:   "mock-test",
		Target: spec.Target{Endpoint: endpoint, Model: "fake-llm"},
		Engine: spec.EngineRef{Type: "mock"},
		Dataset: spec.Dataset{
			Type: spec.DatasetRandom, InputTokens: 8, OutputTokens: 4,
		},
		Load: spec.Load{
			Mode: spec.LoadClosedLoop, Concurrency: 2,
			Requests: requests, Stream: &stream,
		},
	}
}

// collectSink 线程安全的事件收集器。
type collectSink struct {
	mu     sync.Mutex
	events []engine.RequestEvent
}

func (c *collectSink) fn() engine.EventSink {
	return func(e engine.RequestEvent) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.events = append(c.events, e)
	}
}

func (c *collectSink) all() []engine.RequestEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]engine.RequestEvent(nil), c.events...)
}

// TestClosedLoopAgainstFakeTarget 闭环打假目标：4 请求全成功且 ITL 非空。
func TestClosedLoopAgainstFakeTarget(t *testing.T) {
	srv := fakeTarget(t)
	defer srv.Close()

	eng, err := engine.Get("mock")
	if err != nil {
		t.Fatalf("获取 mock 引擎失败: %v", err)
	}
	s := testSpec(srv.URL, 4)
	if err := eng.Prepare(context.Background(), s); err != nil {
		t.Fatalf("Prepare 不应报错: %v", err)
	}

	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.Run(ctx, s, sink.fn()); err != nil {
		t.Fatalf("Run 不应报错: %v", err)
	}
	events := sink.all()
	if len(events) != 4 {
		t.Fatalf("期望 4 个事件，实际 %d", len(events))
	}
	for i, e := range events {
		if !e.Success() {
			t.Fatalf("事件 %d 应成功: %+v", i, e)
		}
		if e.OutputTokens != 3 {
			t.Fatalf("事件 %d 输出 token 应为 3（3 个 chunk），实际 %d", i, e.OutputTokens)
		}
		if len(e.ITLMs) != 2 {
			t.Fatalf("事件 %d ITLMs 应有 2 条（3 chunk 首个为首 token），实际 %d", i, len(e.ITLMs))
		}
		if e.TTFTMs() <= 0 {
			t.Fatalf("事件 %d TTFT 应 > 0，实际 %.2f", i, e.TTFTMs())
		}
	}
}

// TestUnreachableDegrades 目标不可达时退化为本地模拟，请求仍成功。
func TestUnreachableDegrades(t *testing.T) {
	eng, err := engine.Get("mock")
	if err != nil {
		t.Fatalf("获取 mock 引擎失败: %v", err)
	}
	// 127.0.0.1:1 必然连接被拒，模拟目标不可达
	s := testSpec("http://127.0.0.1:1", 2)
	if err := eng.Prepare(context.Background(), s); err != nil {
		t.Fatalf("不可达时 Prepare 也不应报错（退化模拟）: %v", err)
	}

	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.Run(ctx, s, sink.fn()); err != nil {
		t.Fatalf("Run 不应报错: %v", err)
	}
	events := sink.all()
	if len(events) != 2 {
		t.Fatalf("期望 2 个事件，实际 %d", len(events))
	}
	for i, e := range events {
		if !e.Success() {
			t.Fatalf("退化模拟事件 %d 应成功: %+v", i, e)
		}
		if e.OutputTokens != s.Dataset.OutputTokens {
			t.Fatalf("退化模拟事件 %d 输出 token 应为 %d，实际 %d",
				i, s.Dataset.OutputTokens, e.OutputTokens)
		}
		if len(e.ITLMs) == 0 {
			t.Fatalf("退化模拟事件 %d 应有合成 ITLMs", i)
		}
	}
}

// TestCancelInterruptsRun ctx 取消能中断长时间运行。
func TestCancelInterruptsRun(t *testing.T) {
	eng, err := engine.Get("mock")
	if err != nil {
		t.Fatalf("获取 mock 引擎失败: %v", err)
	}
	s := testSpec("http://127.0.0.1:1", 0)
	s.Load.Duration = "30s" // 时长模式，不取消会跑 30s

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.Run(ctx, s, func(engine.RequestEvent) {})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // 让 Run 先跑起来
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消后 Run 返回错误: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("ctx 取消后 Run 未在 10s 内退出")
	}
}

// TestEngineCancel 主动 Cancel 不报错。
func TestEngineCancel(t *testing.T) {
	eng, err := engine.Get("mock")
	if err != nil {
		t.Fatalf("获取 mock 引擎失败: %v", err)
	}
	if err := eng.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel 不应报错: %v", err)
	}
}
