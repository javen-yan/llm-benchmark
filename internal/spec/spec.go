// Package spec 定义 Benchmark Spec v2：统一测试定义（对应参考设计 §4）。
//
// 参考设计形态（与 v1 不兼容，属 pre-1.0 重构）：
//
//	name        运行名称
//	target      目标服务（endpoint / protocol / model）
//	engine      引擎选择（type: guidellm / mock）
//	dataset     数据集（类型 / 输入输出 token / seed）
//	load        负载策略（closed_loop / open_loop / poisson）
//	warmup      预热（时长；预热请求不计入指标）
//	metrics     指标白名单（空 = 全部核心指标）
//	collectors  启用的 collector 插件（空 = 默认 system）
//	probes      启用的 probe 插件（默认不启用，preflight 场景手动开）
//	thresholds  SLO 阈值
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

// 负载模式。
const (
	LoadClosedLoop = "closed_loop" // 固定并发闭环
	LoadOpenLoop   = "open_loop"   // 固定速率开环
	LoadPoisson    = "poisson"     // 泊松到达开环
)

// 数据集类型。
const (
	DatasetRandom = "random"
)

// 目标协议。
const (
	ProtocolOpenAI = "openai"
)

// Spec 是统一测试定义的根结构。
type Spec struct {
	Name       string     `yaml:"name" json:"name"`
	Target     Target     `yaml:"target" json:"target"`
	Engine     EngineRef  `yaml:"engine" json:"engine"`
	Dataset    Dataset    `yaml:"dataset" json:"dataset"`
	Load       Load       `yaml:"load" json:"load"`
	Warmup     Warmup     `yaml:"warmup,omitempty" json:"warmup,omitempty"`
	Metrics    []string   `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	Collectors []string   `yaml:"collectors,omitempty" json:"collectors,omitempty"`
	Probes     []string   `yaml:"probes,omitempty" json:"probes,omitempty"`
	Thresholds Thresholds `yaml:"thresholds,omitempty" json:"thresholds,omitempty"`
}

// Target 描述被测服务：OpenAI Compatible endpoint。
type Target struct {
	// Endpoint 如 http://127.0.0.1:9000，程序自动拼接 /v1/chat/completions。
	Endpoint string `yaml:"endpoint" json:"endpoint"`
	// Protocol 目标协议，默认 openai。
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	// Model 传给目标服务的模型名。
	Model string `yaml:"model" json:"model"`
	// APIKey 可选，目标服务需要鉴权时使用。
	APIKey string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
}

// ProtocolName 返回协议名（默认 openai）。
func (t Target) ProtocolName() string {
	if t.Protocol == "" {
		return ProtocolOpenAI
	}
	return t.Protocol
}

// EngineRef 引擎选择。
type EngineRef struct {
	// Type: guidellm / mock（vllm / native 预留）。
	Type string `yaml:"type" json:"type"`
}

// Dataset 数据集定义（V1 仅 random：固定输入输出长度 + seed）。
type Dataset struct {
	Type         string `yaml:"type" json:"type"`
	InputTokens  int    `yaml:"input_tokens" json:"input_tokens"`
	OutputTokens int    `yaml:"output_tokens" json:"output_tokens"`
	Seed         int64  `yaml:"seed,omitempty" json:"seed,omitempty"`
}

// Load 负载策略。
type Load struct {
	// Mode: closed_loop / open_loop / poisson，默认 closed_loop。
	Mode string `yaml:"mode,omitempty" json:"mode"`
	// Concurrency 并发客户端数（闭环）/ 最大并发（开环）。
	Concurrency int `yaml:"concurrency" json:"concurrency"`
	// Requests 总请求数；与 Duration 二选一（都填时以先达到者为准）。
	Requests int `yaml:"requests,omitempty" json:"requests,omitempty"`
	// Duration 持续时长，如 "300s"；与 Requests 二选一。
	Duration string `yaml:"duration,omitempty" json:"duration,omitempty"`
	// Rate 开环目标速率（req/s）；open_loop / poisson 必填。
	Rate float64 `yaml:"rate,omitempty" json:"rate,omitempty"`
	// Stream 是否用流式请求（SSE），默认 true。
	Stream *bool `yaml:"stream,omitempty" json:"stream,omitempty"`
}

// ModeName 返回负载模式（默认 closed_loop）。
func (l Load) ModeName() string {
	if l.Mode == "" {
		return LoadClosedLoop
	}
	return l.Mode
}

// DurationParsed 解析 Duration（如 "300s"）；为空返回 0。
func (l Load) DurationParsed() (time.Duration, error) {
	if l.Duration == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(l.Duration)
	if err != nil {
		return 0, fmt.Errorf("load.duration 解析失败 %q: %w", l.Duration, err)
	}
	return d, nil
}

// StreamEnabled 返回流式开关（默认 true）。
func (l Load) StreamEnabled() bool {
	if l.Stream == nil {
		return true
	}
	return *l.Stream
}

// Warmup 预热：预热请求不计入指标。
type Warmup struct {
	// Duration 预热时长，如 "30s"；为空表示不预热。
	Duration string `yaml:"duration,omitempty" json:"duration,omitempty"`
}

// DurationParsed 解析预热时长；为空返回 0。
func (w Warmup) DurationParsed() (time.Duration, error) {
	if w.Duration == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(w.Duration)
	if err != nil {
		return 0, fmt.Errorf("warmup.duration 解析失败 %q: %w", w.Duration, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("warmup.duration 不能为负")
	}
	return d, nil
}

// Thresholds SLO 阈值（对应参考设计 §4 thresholds）。
type Thresholds struct {
	TTFTP95Ms    float64 `yaml:"ttft_p95,omitempty" json:"ttft_p95,omitempty"`
	TTFTP99Ms    float64 `yaml:"ttft_p99,omitempty" json:"ttft_p99,omitempty"`
	MaxTTFTMs    float64 `yaml:"max_ttft_ms,omitempty" json:"max_ttft_ms,omitempty"`
	MinTPS       float64 `yaml:"min_tps,omitempty" json:"min_tps,omitempty"`
	ErrorRate    float64 `yaml:"error_rate,omitempty" json:"error_rate,omitempty"` // 0~1
	P99LatencyMs float64 `yaml:"p99_latency_ms,omitempty" json:"p99_latency_ms,omitempty"`
}

// HasThresholds 是否配置了任意阈值。
func (t Thresholds) HasThresholds() bool {
	return t.TTFTP95Ms > 0 || t.TTFTP99Ms > 0 || t.MaxTTFTMs > 0 ||
		t.MinTPS > 0 || t.ErrorRate > 0 || t.P99LatencyMs > 0
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
	if s.Target.Endpoint == "" {
		return fmt.Errorf("spec.target.endpoint 不能为空")
	}
	if s.Target.Model == "" {
		return fmt.Errorf("spec.target.model 不能为空")
	}
	switch s.Engine.Type {
	case "guidellm", "mock":
		// V1 支持的引擎；vllm/native 预留。
	default:
		return fmt.Errorf("不支持的引擎 %q（可选 guidellm/mock）", s.Engine.Type)
	}
	if s.Dataset.Type == "" {
		s.Dataset.Type = DatasetRandom
	}
	if s.Dataset.Type != DatasetRandom {
		return fmt.Errorf("不支持的 dataset.type %q（V1 仅支持 random）", s.Dataset.Type)
	}
	if s.Dataset.InputTokens <= 0 || s.Dataset.OutputTokens <= 0 {
		return fmt.Errorf("spec.dataset.input_tokens/output_tokens 必须 > 0")
	}
	switch s.Load.ModeName() {
	case LoadClosedLoop, LoadOpenLoop, LoadPoisson:
	default:
		return fmt.Errorf("不支持的 load.mode %q（可选 closed_loop/open_loop/poisson）", s.Load.Mode)
	}
	if s.Load.Concurrency <= 0 {
		return fmt.Errorf("spec.load.concurrency 必须 > 0")
	}
	if s.Load.Requests <= 0 && s.Load.Duration == "" {
		return fmt.Errorf("spec.load.requests 与 load.duration 至少填一个")
	}
	if d, err := s.Load.DurationParsed(); err != nil {
		return err
	} else if s.Load.Duration != "" && d <= 0 {
		return fmt.Errorf("spec.load.duration 必须 > 0")
	}
	if m := s.Load.ModeName(); m == LoadOpenLoop || m == LoadPoisson {
		if s.Load.Rate <= 0 {
			return fmt.Errorf("load.mode=%s 时 spec.load.rate 必须 > 0", m)
		}
	}
	if _, err := s.Warmup.DurationParsed(); err != nil {
		return err
	}
	if s.Thresholds.ErrorRate < 0 || s.Thresholds.ErrorRate > 1 {
		return fmt.Errorf("spec.thresholds.error_rate 必须在 0~1 之间")
	}
	return nil
}
