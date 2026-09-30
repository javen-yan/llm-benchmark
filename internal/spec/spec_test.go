package spec

import (
	"strings"
	"testing"
)

// validSpec 构造一份合法的 Spec v2。
func validSpec() Spec {
	stream := true
	return Spec{
		Name:   "smoke",
		Target: Target{Endpoint: "http://127.0.0.1:9000", Model: "mock-llm"},
		Engine: EngineRef{Type: "mock"},
		Dataset: Dataset{
			Type:         DatasetRandom,
			InputTokens:  128,
			OutputTokens: 32,
			Seed:         42,
		},
		Load: Load{Mode: LoadClosedLoop, Concurrency: 4, Requests: 40, Stream: &stream},
	}
}

// TestValidateOK 合法 spec 应通过校验。
func TestValidateOK(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("合法 spec 不应报错: %v", err)
	}
}

// TestValidateMissingTarget 缺 endpoint / model 应报错。
func TestValidateMissingTarget(t *testing.T) {
	s := validSpec()
	s.Target.Endpoint = ""
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("缺 endpoint 应报错，实际: %v", err)
	}
	s = validSpec()
	s.Target.Model = ""
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("缺 model 应报错，实际: %v", err)
	}
}

// TestValidateBadEngine 非法 engine type 应报错。
func TestValidateBadEngine(t *testing.T) {
	s := validSpec()
	s.Engine.Type = "vllm"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "vllm") {
		t.Fatalf("非法 engine 应报错，实际: %v", err)
	}
}

// TestValidateOpenLoopNoRate open_loop 不填 rate 应报错；填了则通过。
func TestValidateOpenLoopNoRate(t *testing.T) {
	s := validSpec()
	s.Load.Mode = LoadOpenLoop
	s.Load.Requests = 10
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "rate") {
		t.Fatalf("open_loop 无 rate 应报错，实际: %v", err)
	}
	s.Load.Rate = 5
	if err := s.Validate(); err != nil {
		t.Fatalf("open_loop 填 rate 后不应报错: %v", err)
	}
}

// TestValidateBadDuration 非法 duration 字符串应报错。
func TestValidateBadDuration(t *testing.T) {
	s := validSpec()
	s.Load.Requests = 0
	s.Load.Duration = "not-a-duration"
	if err := s.Validate(); err == nil {
		t.Fatalf("非法 duration 应报错")
	}
	// requests 与 duration 都不填也应报错
	s.Load.Duration = ""
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "requests") {
		t.Fatalf("requests/duration 全空应报错，实际: %v", err)
	}
}

// TestValidateErrorRateOutOfRange error_rate 越界应报错。
func TestValidateErrorRateOutOfRange(t *testing.T) {
	for _, r := range []float64{-0.1, 1.1} {
		s := validSpec()
		s.Thresholds.ErrorRate = r
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "error_rate") {
			t.Fatalf("error_rate=%v 应报错，实际: %v", r, err)
		}
	}
	s := validSpec()
	s.Thresholds.ErrorRate = 0.05
	if err := s.Validate(); err != nil {
		t.Fatalf("error_rate=0.05 不应报错: %v", err)
	}
}

// TestLoadBodyJSON JSON 请求体解析。
func TestLoadBodyJSON(t *testing.T) {
	body := `{"name":"j","target":{"endpoint":"http://127.0.0.1:9000","model":"m"},
		"engine":{"type":"mock"},"dataset":{"type":"random","input_tokens":8,"output_tokens":4},
		"load":{"mode":"closed_loop","concurrency":2,"requests":5}}`
	s, err := LoadBody("application/json", []byte(body))
	if err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if s.Name != "j" || s.Target.Model != "m" || s.Load.Requests != 5 {
		t.Fatalf("JSON 解析字段不对: %+v", s)
	}
}

// TestLoadBodyYAML YAML 请求体解析（Content-Type 含 yaml）。
func TestLoadBodyYAML(t *testing.T) {
	body := `
name: y
target:
  endpoint: http://127.0.0.1:9000
  model: m
engine:
  type: mock
dataset:
  type: random
  input_tokens: 8
  output_tokens: 4
load:
  mode: closed_loop
  concurrency: 2
  requests: 5
`
	s, err := LoadBody("application/x-yaml", []byte(body))
	if err != nil {
		t.Fatalf("YAML 解析失败: %v", err)
	}
	if s.Name != "y" || s.Dataset.InputTokens != 8 {
		t.Fatalf("YAML 解析字段不对: %+v", s)
	}
}

// TestLoadBodyInvalid 非法 body 应报错。
func TestLoadBodyInvalid(t *testing.T) {
	if _, err := LoadBody("application/json", []byte("{bad json")); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
	if _, err := LoadBody("application/json", []byte(`{"name":"x"}`)); err == nil {
		t.Fatalf("缺必填字段的 JSON 应在校验阶段报错")
	}
}

// TestDefaults 默认值：协议/负载模式/流式开关。
func TestDefaults(t *testing.T) {
	var tg Target
	if tg.ProtocolName() != ProtocolOpenAI {
		t.Fatalf("协议默认应为 openai，实际 %q", tg.ProtocolName())
	}
	var l Load
	if l.ModeName() != LoadClosedLoop {
		t.Fatalf("负载模式默认应为 closed_loop，实际 %q", l.ModeName())
	}
	if !l.StreamEnabled() {
		t.Fatalf("stream 默认应为 true")
	}
	if d, err := l.DurationParsed(); err != nil || d != 0 {
		t.Fatalf("空 duration 应返回 0, nil，实际 %v, %v", d, err)
	}
}
