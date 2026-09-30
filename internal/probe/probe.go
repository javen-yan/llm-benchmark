// Package probe 是 Probe 插件层（对应参考设计 §10）：主动探测环境资源与网络。
//
// 当前为接口预留阶段：只定义 Probe 接口与注册表，无内置实现。
// Phase 4 再接入 iperf3（带宽探测）、RDMA（ib_write_bw）、NCCL（nccl-tests）等，
// 实现方按本包接口提供 Available/Discover/Run 即可被平台调度。
//
// 与 collector 的区别：Collector 是被动周期采样（后台常驻），Probe 是主动
// 按需探测（运行前/运行后执行一次，产出环境基线）。
package probe

import (
	"context"
	"fmt"
	"sync"

	"github.com/javen-yan/llm-benchmark/internal/collector"
)

// Resource 是探测发现的资源（如网卡、GPU、交换机端口）。
type Resource struct {
	Name string            // 如 "eth0"、"mlx5_0"
	Kind string            // 如 "nic"、"gpu"、"switch"
	Info map[string]string // 额外信息，如 {"speed": "200Gb/s"}
}

// ProbeConfig 是单次探测运行的参数。
type ProbeConfig struct {
	Params map[string]string // 如 {"duration_sec": "10", "target": "10.0.0.2"}
}

// Probe 是主动探测器插件接口。
type Probe interface {
	// Name 返回探测器名，如 "iperf3"。
	Name() string
	// Available 检查探测依赖是否就绪（如二进制是否存在、网卡是否就绪）。
	Available(ctx context.Context) bool
	// Discover 发现可探测的资源。
	Discover(ctx context.Context) ([]Resource, error)
	// Run 执行一次探测，产出复用 collector.MetricSample 的指标样本。
	Run(ctx context.Context, cfg ProbeConfig) ([]collector.MetricSample, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]func() Probe{}
)

// Register 注册探测器构造器。
func Register(name string, f func() Probe) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = f
}

// Get 按名获取探测器实例。
func Get(name string) (Probe, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("未知探测器 %q", name)
	}
	return f(), nil
}

// Names 返回已注册的探测器名。
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
