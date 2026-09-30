package guidellm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/javen-yan/llm-benchmark/internal/spec"
)

// testSpec 构造测试用 Spec。
func testSpec() spec.Spec {
	stream := true
	return spec.Spec{
		Name:    "test",
		Target:  spec.Target{Endpoint: "http://127.0.0.1:9000", Model: "mock-llm", APIKey: "sk-test"},
		Engine:  spec.EngineRef{Type: "guidellm"},
		Dataset: spec.Dataset{Type: spec.DatasetRandom, InputTokens: 128, OutputTokens: 32},
		Load:    spec.Load{Mode: spec.LoadClosedLoop, Concurrency: 4, Requests: 40, Stream: &stream},
	}
}

// containsAll 检查 args 是否按顺序包含所有期望片段。
func containsAll(t *testing.T, args []string, want ...string) {
	t.Helper()
	joined := strings.Join(args, " ")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("参数缺失 %q，实际: %v", w, args)
		}
	}
}

func TestBuildArgsClosedLoop(t *testing.T) {
	args := buildArgs(testSpec(), "/tmp/report.json")
	containsAll(t, args,
		"benchmark",
		"--target http://127.0.0.1:9000",
		"--model mock-llm",
		"--data prompt_tokens=128,output_tokens=32",
		"--output-path /tmp/report.json",
		"--num-prompts 40",
		"--rate-type constant --rate 4", // 闭环：速率为并发数
		"--target-key sk-test",
	)
	if strings.Contains(strings.Join(args, " "), "--no-stream") {
		t.Fatalf("流式开启时不应有 --no-stream，实际: %v", args)
	}
}

func TestBuildArgsOpenLoop(t *testing.T) {
	s := testSpec()
	s.Load.Mode = spec.LoadOpenLoop
	s.Load.Rate = 12.5
	s.Load.Requests = 0
	s.Load.Duration = "60s"
	args := buildArgs(s, "/tmp/r.json")
	containsAll(t, args,
		"--rate-type constant --rate 12.5",
		"--max-seconds 60",
	)
	if strings.Contains(strings.Join(args, " "), "--num-prompts") {
		t.Fatalf("Requests=0 时不应有 --num-prompts，实际: %v", args)
	}
}

func TestBuildArgsNoStream(t *testing.T) {
	s := testSpec()
	stream := false
	s.Load.Stream = &stream
	args := buildArgs(s, "/tmp/r.json")
	containsAll(t, args, "--no-stream")
}

// fixtureReport 是内联的 guidellm 报告 fixture：延迟字段用秒，吞吐用原始值。
const fixtureReport = `{
  "benchmarks": [
    {
      "request_throughput": 4.2,
      "output_token_throughput": 135.5,
      "ttft_p50": 0.121,
      "ttft_p99": 0.145,
      "tpot_p50": 0.026,
      "tpot_p99": 0.031,
      "request_latency_p50": 0.939,
      "request_latency_p99": 0.952
    }
  ]
}`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseReportSecondsToMs(t *testing.T) {
	now := time.Now()
	vals, err := parseReportAt(writeFixture(t, fixtureReport), now)
	if err != nil {
		t.Fatalf("parseReport 失败: %v", err)
	}
	type triple struct {
		id, agg string
		v       float64
	}
	got := make([]triple, 0, len(vals))
	for _, mv := range vals {
		got = append(got, triple{mv.MetricID, mv.Aggregation, mv.Value})
		if mv.Timestamp != now {
			t.Fatalf("Timestamp 应为解析时刻，实际 %v", mv.Timestamp)
		}
		if mv.RunID != "" {
			t.Fatalf("RunID 应为空（由调用方补填），实际 %q", mv.RunID)
		}
	}
	lookup := func(id, agg string) (float64, bool) {
		for _, x := range got {
			if x.id == id && x.agg == agg {
				return x.v, true
			}
		}
		return 0, false
	}
	// 秒→毫秒换算检查
	for key, want := range map[[2]string]float64{
		{"llm.ttft", "p50"}: 121,
		{"llm.ttft", "p99"}: 145,
		{"llm.tpot", "p50"}: 26,
		{"llm.tpot", "p99"}: 31,
		{"llm.e2e", "p50"}:  939,
		{"llm.e2e", "p99"}:  952,
	} {
		id, agg := key[0], key[1]
		v, ok := lookup(id, agg)
		if !ok {
			t.Fatalf("缺失指标 %s/%s", id, agg)
		}
		if v != want {
			t.Fatalf("指标 %s/%s = %v，期望 %v（秒→毫秒换算）", id, agg, v, want)
		}
	}
	// 吞吐量不换算
	if v, ok := lookup("llm.request_rate", "value"); !ok || v != 4.2 {
		t.Fatalf("llm.request_rate/value = %v,%v，期望 4.2", v, ok)
	}
	if v, ok := lookup("llm.output_tps", "value"); !ok || v != 135.5 {
		t.Fatalf("llm.output_tps/value = %v,%v，期望 135.5", v, ok)
	}
}

func TestParseReportMsPassthrough(t *testing.T) {
	// 毫秒值（>10）应原样通过，不再乘 1000。
	path := writeFixture(t, `{"ttft_p50": 121.7, "request_throughput": 4.2}`)
	vals, err := parseReportAt(path, time.Now())
	if err != nil {
		t.Fatalf("parseReport 失败: %v", err)
	}
	for _, mv := range vals {
		if mv.MetricID == "llm.ttft" && mv.Aggregation == "p50" && mv.Value != 121.7 {
			t.Fatalf("毫秒值应原样通过，实际 %v", mv.Value)
		}
	}
}

func TestParseReportEmpty(t *testing.T) {
	if _, err := parseReportAt(writeFixture(t, `{}`), time.Now()); err == nil {
		t.Fatalf("空报告应返回错误")
	}
	if _, err := parseReportAt(writeFixture(t, `{"benchmarks": []}`), time.Now()); err == nil {
		t.Fatalf("空 benchmarks 数组应返回错误")
	}
}

func TestPrepareMissingBinary(t *testing.T) {
	// 不依赖真实 guidellm：把 PATH 清空，保证 LookPath 找不到。
	t.Setenv("PATH", "")
	a := &Adapter{}
	err := a.Prepare(t.Context(), spec.Spec{})
	if err == nil {
		t.Fatalf("找不到 guidellm 时 Prepare 应返回错误")
	}
	if !strings.Contains(err.Error(), "pip install guidellm") {
		t.Fatalf("错误信息应包含安装指引，实际: %v", err)
	}
}
