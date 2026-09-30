// Package spec 定义 Benchmark Spec：统一测试定义（对应架构图用户层 Benchmark Spec）。
//
// 四段式结构：
//
//	name    运行名称
//	target  目标服务（OpenAI Compatible endpoint）
//	engine  引擎选择（guidellm / mock，vllm/native 预留）
//	workload 负载定义（并发、请求数/时长、输入输出长度）
//	slo     SLO 阈值（Phase 2 判定用，Phase 1 仅记录）
//
// 示例见 examples/spec-mock.yaml。
package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Spec 是统一测试定义的根结构。
type Spec struct {
	Name     string   `yaml:"name" json:"name"`
	Target   Target   `yaml:"target" json:"target"`
	Engine   string   `yaml:"engine" json:"engine"`
	Workload Workload `yaml:"workload" json:"workload"`
	SLO      SLO      `yaml:"slo,omitempty" json:"slo,omitempty"`
}

// Target 描述被测服务：任意 OpenAI Compatible 的 /v1/chat/completions。
type Target struct {
	// BaseURL 如 http://127.0.0.1:9000，程序自动拼接 /v1/chat/completions。
	BaseURL string `yaml:"base_url" json:"base_url"`
	// Model 传给目标服务的模型名。
	Model string `yaml:"model" json:"model"`
	// APIKey 可选，目标服务需要鉴权时使用。
	APIKey string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
}

// Workload 描述负载策略（开环/闭环由引擎实现，Phase 1 先做闭环固定并发）。
type Workload struct {
	// Concurrency 并发客户端数。
	Concurrency int `yaml:"concurrency" json:"concurrency"`
	// Requests 总请求数；与 DurationSeconds 二选一（都填时以先达到者为准）。
	Requests int `yaml:"requests,omitempty" json:"requests,omitempty"`
	// DurationSeconds 持续秒数。
	DurationSeconds int `yaml:"duration_seconds,omitempty" json:"duration_seconds,omitempty"`
	// InputTokens 输入 token 数（固定值；范围模式 Phase 2）。
	InputTokens int `yaml:"input_tokens" json:"input_tokens"`
	// OutputTokens 期望输出 token 数。
	OutputTokens int `yaml:"output_tokens" json:"output_tokens"`
	// Stream 是否用流式请求（SSE），默认 true。
	Stream *bool `yaml:"stream,omitempty" json:"stream,omitempty"`
}

// SLO 预留给 Phase 2 的判定字段，Phase 1 只记录不判定。
type SLO struct {
	P99LatencyMs float64 `yaml:"p99_latency_ms,omitempty" json:"p99_latency_ms,omitempty"`
	MinTPS       float64 `yaml:"min_tps,omitempty" json:"min_tps,omitempty"`
	MaxTTFTMs    float64 `yaml:"max_ttft_ms,omitempty" json:"max_ttft_ms,omitempty"`
}

// StreamEnabled 返回流式开关（默认 true）。
func (w Workload) StreamEnabled() bool {
	if w.Stream == nil {
		return true
	}
	return *w.Stream
}

// LoadFile 从 YAML 文件加载并校验 Spec。
func LoadFile(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("读取 spec 文件失败: %w", err)
	}
	var s Spec
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Spec{}, fmt.Errorf("解析 spec YAML 失败: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// LoadBody 从请求体解析 Spec：Content-Type 含 yaml 时按 YAML，否则按 JSON。
func LoadBody(contentType string, body []byte) (Spec, error) {
	var s Spec
	if strings.Contains(strings.ToLower(contentType), "yaml") {
		if err := yaml.Unmarshal(body, &s); err != nil {
			return Spec{}, fmt.Errorf("解析 spec YAML 失败: %w", err)
		}
	} else {
		if err := json.Unmarshal(body, &s); err != nil {
			return Spec{}, fmt.Errorf("解析 spec JSON 失败: %w", err)
		}
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// Validate 校验 Spec 的必填项与合法性。
func (s Spec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("spec.name 不能为空")
	}
	if s.Target.BaseURL == "" {
		return fmt.Errorf("spec.target.base_url 不能为空")
	}
	if s.Target.Model == "" {
		return fmt.Errorf("spec.target.model 不能为空")
	}
	switch s.Engine {
	case "guidellm", "mock":
		// Phase 1 支持的引擎；vllm/native 预留。
	default:
		return fmt.Errorf("不支持的引擎 %q（可选 guidellm/mock）", s.Engine)
	}
	if s.Workload.Concurrency <= 0 {
		return fmt.Errorf("spec.workload.concurrency 必须 > 0")
	}
	if s.Workload.Requests <= 0 && s.Workload.DurationSeconds <= 0 {
		return fmt.Errorf("spec.workload.requests 与 duration_seconds 至少填一个")
	}
	if s.Workload.InputTokens <= 0 || s.Workload.OutputTokens <= 0 {
		return fmt.Errorf("spec.workload.input_tokens/output_tokens 必须 > 0")
	}
	return nil
}

// Deadline 返回负载的截止时间（requests 模式无时间上限时返回零值调用方另行处理）。
func (s Spec) Deadline() time.Duration {
	if s.Workload.DurationSeconds > 0 {
		return time.Duration(s.Workload.DurationSeconds) * time.Second
	}
	return 0
}
