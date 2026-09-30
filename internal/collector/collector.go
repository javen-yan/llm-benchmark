// Package collector 是 Collector 插件层（对应参考设计 §9）：被动采集主机/外部指标。
//
// 与引擎不同，Collector 不产生压测流量，只在后台周期性采样（如系统 CPU/内存、
// 目标服务 /metrics 端点），产出 MetricSample，由上层写入 system_sample 表或
// 转成 registry 指标。插件通过 Register 注册，线程安全。
package collector

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MetricSample 是采集到的一条指标样本。
type MetricSample struct {
	Name   string            // 如 system.cpu_util_pct
	Value  float64           // 如无单位约定，百分比类为 0~100
	Labels map[string]string // 如 {"host": "gpu-01"}
	Ts     time.Time
}

// Collector 是被动采集器插件接口。
type Collector interface {
	Name() string
	Start(ctx context.Context) error
	Collect(ctx context.Context) ([]MetricSample, error)
	Stop(ctx context.Context) error
}

var (
	mu       sync.RWMutex
	registry = map[string]func() Collector{}
)

// Register 注册采集器构造器。
func Register(name string, f func() Collector) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = f
}

// Get 按名获取采集器实例。
func Get(name string) (Collector, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("未知采集器 %q", name)
	}
	return f(), nil
}

// Names 返回已注册的采集器名。
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}

// ---------------------------------------------------------------------------
// system collector：纯标准库读 Linux /proc，非 Linux 返回零值不报错。
// 样本名固定：system.cpu_util_pct、system.mem_used_pct、system.load1、
// system.net_rx_bytes、system.net_tx_bytes。
// ---------------------------------------------------------------------------

func init() {
	Register("system", func() Collector { return &systemCollector{} })
}

// systemCollector 是主机系统指标采集器。
type systemCollector struct {
	mu        sync.Mutex
	prevTotal uint64
	prevIdle  uint64
	hasPrev   bool
}

// Name 返回采集器名。
func (c *systemCollector) Name() string { return "system" }

// Start 空实现：/proc 按需读取，无需预热。
func (c *systemCollector) Start(_ context.Context) error { return nil }

// Stop 空实现：无后台 goroutine，无需清理。
func (c *systemCollector) Stop(_ context.Context) error { return nil }

// Collect 采集一次系统样本；非 Linux 返回零值样本，不报错。
func (c *systemCollector) Collect(_ context.Context) ([]MetricSample, error) {
	now := time.Now()
	rx, tx := netBytes()
	return []MetricSample{
		{Name: "system.cpu_util_pct", Value: c.cpuUtilPct(), Ts: now},
		{Name: "system.mem_used_pct", Value: memUsedPct(), Ts: now},
		{Name: "system.load1", Value: load1(), Ts: now},
		{Name: "system.net_rx_bytes", Value: float64(rx), Ts: now},
		{Name: "system.net_tx_bytes", Value: float64(tx), Ts: now},
	}, nil
}

// cpuUtilPct 读 /proc/stat 算 CPU 利用率（两次采样的差值）。
// 首次调用无上次快照，返回 0。
func (c *systemCollector) cpuUtilPct() float64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	total, idle, ok := readCPUStat()
	if !ok {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasPrev {
		c.prevTotal, c.prevIdle, c.hasPrev = total, idle, true
		return 0
	}
	dTotal := total - c.prevTotal
	dIdle := idle - c.prevIdle
	c.prevTotal, c.prevIdle = total, idle
	if dTotal == 0 {
		return 0
	}
	return (1 - float64(dIdle)/float64(dTotal)) * 100
}

// readCPUStat 解析 /proc/stat 首行 cpu，返回总 jiffies 与空闲 jiffies。
func readCPUStat() (total, idle uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 4 {
			return 0, 0, false
		}
		vals := make([]uint64, 0, len(fields))
		for _, f := range fields {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			vals = append(vals, v)
		}
		for _, v := range vals {
			total += v
		}
		idle = vals[3] // idle
		if len(vals) >= 5 {
			idle += vals[4] // iowait 也算空闲
		}
		return total, idle, true
	}
	return 0, 0, false
}

// memUsedPct 读 /proc/meminfo 算内存使用率（百分比）；非 Linux 返回 0。
func memUsedPct() float64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total, avail uint64
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total, _ = strconv.ParseUint(f[1], 10, 64)
		case "MemAvailable:":
			avail, _ = strconv.ParseUint(f[1], 10, 64)
		}
	}
	if total == 0 || avail > total {
		return 0
	}
	return float64(total-avail) / float64(total) * 100
}

// load1 读 /proc/loadavg 取 1 分钟平均负载；非 Linux 返回 0。
func load1() float64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// netBytes 读 /proc/net/dev 累加所有网卡（含 lo）的收发字节数；
// 非 Linux 或读取失败返回 0。
func netBytes() (rx, tx uint64) {
	if runtime.GOOS != "linux" {
		return 0, 0
	}
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		f := strings.Fields(line[idx+1:])
		if len(f) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64) // receive bytes
		t, err2 := strconv.ParseUint(f[8], 10, 64) // transmit bytes
		if err1 != nil || err2 != nil {
			continue
		}
		rx += r
		tx += t
	}
	return rx, tx
}
