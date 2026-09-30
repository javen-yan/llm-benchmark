// Package core 是平台核心层：RunManager 管理一次 benchmark 运行的完整生命周期。
//
// 状态机（对应参考设计 §5）：
// created → preparing → warming_up(可选) → running → normalizing → analyzing → completed
// 任意阶段可转 failed / cancelled。
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/javen-yan/llm-benchmark/internal/analyzer"
	"github.com/javen-yan/llm-benchmark/internal/collector"
	"github.com/javen-yan/llm-benchmark/internal/engine"
	"github.com/javen-yan/llm-benchmark/internal/metric"
	"github.com/javen-yan/llm-benchmark/internal/spec"
	"github.com/javen-yan/llm-benchmark/internal/store"
)

// Manager 管理 benchmark 运行的生命周期。
type Manager struct {
	db            *gorm.DB
	maxConcurrent int
	sem           chan struct{}
	mu            sync.Mutex
	cancels       map[string]context.CancelFunc
}

// NewManager 创建 Manager。maxConcurrent<=0 时默认为 1（串行）。
func NewManager(db *gorm.DB, maxConcurrent int) *Manager {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Manager{
		db:            db,
		maxConcurrent: maxConcurrent,
		sem:           make(chan struct{}, maxConcurrent),
		cancels:       map[string]context.CancelFunc{},
	}
}

// Submit 提交一次运行：校验 → 引擎能力检查 → 建 Run/Snapshot → 异步执行，立即返回 runID。
func (m *Manager) Submit(s spec.Spec) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	eng, err := engine.Get(s.Engine.Type)
	if err != nil {
		return "", err
	}
	// 负载模式能力校验：不支持时直接拒绝，不降级伪造。
	caps := eng.Capabilities()
	switch s.Load.ModeName() {
	case spec.LoadOpenLoop:
		if !caps.OpenLoop {
			return "", fmt.Errorf("引擎 %q 不支持 open_loop 负载模式", s.Engine.Type)
		}
	case spec.LoadPoisson:
		if !caps.Poisson {
			return "", fmt.Errorf("引擎 %q 不支持 poisson 负载模式", s.Engine.Type)
		}
	case "sweep":
		return "", fmt.Errorf("sweep 负载模式暂未实现")
	}

	specJSON, _ := json.Marshal(s)
	hwJSON, _ := json.Marshal(collectHardware())
	runID := uuid.NewString()
	now := time.Now()
	run := store.Run{
		ID:        runID,
		Name:      s.Name,
		Engine:    s.Engine.Type,
		Status:    store.StatusCreated,
		SpecJSON:  string(specJSON),
		CreatedAt: now,
	}
	if err := m.db.Create(&run).Error; err != nil {
		return "", fmt.Errorf("创建运行记录失败: %w", err)
	}
	snap := store.Snapshot{
		RunID:        runID,
		SpecJSON:     string(specJSON),
		Engine:       s.Engine.Type,
		Model:        s.Target.Model,
		Protocol:     s.Target.ProtocolName(),
		DatasetType:  s.Dataset.Type,
		Seed:         s.Dataset.Seed,
		InputTokens:  s.Dataset.InputTokens,
		OutputTokens: s.Dataset.OutputTokens,
		HardwareJSON: string(hwJSON),
		CreatedAt:    now,
	}
	if err := m.db.Create(&snap).Error; err != nil {
		return "", fmt.Errorf("创建运行快照失败: %w", err)
	}

	go m.execute(runID, s)
	return runID, nil
}

// Cancel 取消运行中的任务。
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	cancel, ok := m.cancels[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("运行 %s 不在执行中", id)
	}
	cancel()
	return nil
}

// execute 是实际执行体：按状态机流转，任意阶段可 failed/cancelled。
func (m *Manager) execute(id string, s spec.Spec) {
	m.sem <- struct{}{}
	defer func() { <-m.sem }()

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[id] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, id)
		m.mu.Unlock()
	}()

	eng, err := engine.Get(s.Engine.Type)
	if err != nil {
		m.setTerminal(id, store.StatusFailed, err.Error())
		return
	}

	// ---- preparing ----
	m.setStatus(id, store.StatusPreparing, nil)
	if err := eng.Prepare(ctx, s); err != nil {
		m.setTerminal(id, store.StatusFailed, fmt.Sprintf("引擎准备失败: %v", err))
		return
	}

	// ---- warming_up（可选）：配置了预热时长且引擎支持时才进入 ----
	if d, _ := s.Warmup.DurationParsed(); d > 0 && eng.Capabilities().Warmup {
		m.setStatus(id, store.StatusWarmingUp, nil)
		warmCtx, warmCancel := context.WithTimeout(ctx, d)
		var warmSink engine.EventSink = func(engine.RequestEvent) {} // 预热事件直接丢弃
		if _, err := eng.Run(warmCtx, s, warmSink); err != nil {
			log.Printf("run %s 预热异常（继续主流程）: %v", id, err)
		}
		warmCancel()
		if ctx.Err() == context.Canceled {
			m.setTerminal(id, store.StatusCancelled, "预热阶段被取消")
			return
		}
	}

	// ---- running ----
	startedAt := time.Now()
	m.setStatus(id, store.StatusRunning, &startedAt)

	collectors := m.startCollectors(ctx, id, s)
	defer m.stopCollectors(ctx, id, collectors)

	buf := &eventBuffer{}
	var sink engine.EventSink = func(e engine.RequestEvent) {
		e.RunID = id
		if buf.add(e) {
			m.flushEvents(id, buf)
		}
	}

	runDone := make(chan struct{})
	var result *engine.BenchmarkResult
	var runErr error
	go func() {
		defer close(runDone)
		result, runErr = eng.Run(ctx, s, sink)
	}()

	pointTick := time.NewTicker(2 * time.Second)
	defer pointTick.Stop()
	collectTick := time.NewTicker(5 * time.Second)
	defer collectTick.Stop()
	m.writePoint(id, buf) // 先写一个起点，曲线不空

	for {
		select {
		case <-ctx.Done():
			<-runDone // 等引擎感知取消后退出
			m.setTerminal(id, store.StatusCancelled, "用户取消")
			return
		case <-runDone:
			goto runFinished
		case t := <-pointTick.C:
			m.flushEvents(id, buf)
			m.writePointAt(id, buf, t)
		case t := <-collectTick.C:
			m.collectSystem(id, collectors, t, ctx)
		}
	}

runFinished:
	m.flushEvents(id, buf) // 兜底落库
	if ctx.Err() == context.Canceled {
		m.setTerminal(id, store.StatusCancelled, "用户取消")
		return
	}
	if runErr != nil {
		m.setTerminal(id, store.StatusFailed, runErr.Error())
		return
	}
	if result == nil {
		result = &engine.BenchmarkResult{}
	}

	// ---- normalizing：事件归一化 → 聚合 → metric_value 落库 ----
	m.setStatus(id, store.StatusNormalizing, nil)
	if result.EngineVersion != "" {
		_ = m.db.Model(&store.Snapshot{}).Where("run_id = ?", id).
			Update("engine_version", result.EngineVersion).Error
	}
	events := m.loadEvents(id)
	analyzed := analyzer.Analyze(id, events)
	merged := mergeValues(result.Aggregates, analyzed)
	if len(merged) > 0 {
		now := time.Now()
		rows := make([]store.MetricValue, 0, len(merged))
		for _, v := range merged {
			if v.RunID == "" {
				v.RunID = id
			}
			if v.Timestamp.IsZero() {
				v.Timestamp = now
			}
			rows = append(rows, store.MetricValue{
				RunID:       v.RunID,
				MetricID:    v.MetricID,
				Aggregation: v.Aggregation,
				Value:       v.Value,
				Labels:      v.Labels,
				Timestamp:   v.Timestamp,
			})
		}
		if err := m.db.Create(&rows).Error; err != nil {
			log.Printf("run %s 指标落库失败: %v", id, err)
		}
	}

	// ---- analyzing：SLO 在 report 接口实时计算，不落库 ----
	m.setStatus(id, store.StatusAnalyzing, nil)

	// ---- completed：补最后一个采样点，保证曲线有终点 ----
	m.writePoint(id, buf)
	m.setTerminal(id, store.StatusCompleted, "")
}

// loadEvents 从 DB 读全量请求事件并转回 engine.RequestEvent。
func (m *Manager) loadEvents(runID string) []engine.RequestEvent {
	var rows []store.RequestEvent
	if err := m.db.Where("run_id = ?", runID).Order("id ASC").Find(&rows).Error; err != nil {
		log.Printf("run %s 读取事件失败: %v", runID, err)
		return nil
	}
	events := make([]engine.RequestEvent, 0, len(rows))
	for _, r := range rows {
		e := engine.RequestEvent{
			RunID:        r.RunID,
			RequestID:    r.RequestID,
			ScheduledAt:  r.ScheduledAt,
			StartedAt:    r.StartedAt,
			FirstTokenAt: r.FirstTokenAt,
			FinishedAt:   r.FinishedAt,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
			Status:       r.Status,
			Timeout:      r.Timeout,
			Error:        r.Error,
		}
		if r.ITLMs != "" {
			_ = json.Unmarshal([]byte(r.ITLMs), &e.ITLMs)
		}
		events = append(events, e)
	}
	return events
}

// setStatus 更新运行状态；running 阶段同时记录 StartedAt。
func (m *Manager) setStatus(id string, st store.RunStatus, ts *time.Time) {
	upd := map[string]any{"status": st}
	if ts != nil && st == store.StatusRunning {
		upd["started_at"] = *ts
	}
	if err := m.db.Model(&store.Run{}).Where("id = ?", id).Updates(upd).Error; err != nil {
		log.Printf("run %s 状态更新失败 %s: %v", id, st, err)
	}
}

// setTerminal 更新为终态（completed/failed/cancelled），记录 FinishedAt 与错误信息。
func (m *Manager) setTerminal(id string, st store.RunStatus, errMsg string) {
	upd := map[string]any{"status": st, "error": errMsg, "finished_at": time.Now()}
	if err := m.db.Model(&store.Run{}).Where("id = ?", id).Updates(upd).Error; err != nil {
		log.Printf("run %s 终态更新失败 %s: %v", id, st, err)
	}
}

// startCollectors 按 spec 启动 collectors；为空时默认 ["system"]，失败的跳过并记录。
func (m *Manager) startCollectors(ctx context.Context, runID string, s spec.Spec) []collector.Collector {
	names := s.Collectors
	if len(names) == 0 {
		names = []string{"system"}
	}
	var out []collector.Collector
	for _, n := range names {
		c, err := collector.Get(n)
		if err != nil {
			log.Printf("run %s collector %q 不可用，已跳过: %v", runID, n, err)
			continue
		}
		if err := c.Start(ctx); err != nil {
			log.Printf("run %s collector %q 启动失败，已跳过: %v", runID, n, err)
			continue
		}
		out = append(out, c)
	}
	return out
}

// stopCollectors 停止所有 collector（取消/完成时调用）。
func (m *Manager) stopCollectors(ctx context.Context, runID string, collectors []collector.Collector) {
	for _, c := range collectors {
		if err := c.Stop(ctx); err != nil {
			log.Printf("run %s collector %q 停止失败: %v", runID, c.Name(), err)
		}
	}
}

// collectSystem 从 collectors 采集一轮系统样本并落库。
func (m *Manager) collectSystem(runID string, collectors []collector.Collector, ts time.Time, ctx context.Context) {
	for _, c := range collectors {
		samples, err := c.Collect(ctx)
		if err != nil {
			log.Printf("run %s collector %s 采集失败: %v", runID, c.Name(), err)
			continue
		}
		row := store.SystemSample{RunID: runID, Ts: ts}
		for _, sm := range samples {
			applySample(&row, sm)
		}
		if err := m.db.Create(&row).Error; err != nil {
			log.Printf("run %s 系统样本写入失败: %v", runID, err)
		}
	}
}

// applySample 把 collector 样本名映射到 SystemSample 字段。
func applySample(dst *store.SystemSample, s collector.MetricSample) {
	switch s.Name {
	case "system.cpu_util_pct":
		dst.CPUUtilPct = s.Value
	case "system.mem_used_pct":
		dst.MemUsedPct = s.Value
	case "system.load1":
		dst.Load1 = s.Value
	case "system.net_rx_bytes":
		dst.NetRxBytes = uint64(s.Value)
	case "system.net_tx_bytes":
		dst.NetTxBytes = uint64(s.Value)
	}
}

// flushEvents 把未落库事件批量写入 request_event 表（ITLMs 转 JSON）。
func (m *Manager) flushEvents(runID string, buf *eventBuffer) {
	batch := buf.takeUnflushed()
	if len(batch) == 0 {
		return
	}
	rows := make([]store.RequestEvent, 0, len(batch))
	for _, e := range batch {
		itl, _ := json.Marshal(e.ITLMs)
		if string(itl) == "null" {
			itl = []byte("[]")
		}
		rows = append(rows, store.RequestEvent{
			RunID:        runID,
			RequestID:    e.RequestID,
			ScheduledAt:  e.ScheduledAt,
			StartedAt:    e.StartedAt,
			FirstTokenAt: e.FirstTokenAt,
			FinishedAt:   e.FinishedAt,
			InputTokens:  e.InputTokens,
			OutputTokens: e.OutputTokens,
			ITLMs:        string(itl),
			Status:       e.Status,
			Timeout:      e.Timeout,
			Error:        e.Error,
		})
	}
	if err := m.db.Create(&rows).Error; err != nil {
		log.Printf("run %s 事件落库失败: %v", runID, err)
	}
}

// writePoint 写一个实时采样点（运行中曲线用）。
func (m *Manager) writePoint(runID string, buf *eventBuffer) {
	m.writePointAt(runID, buf, time.Now())
}

func (m *Manager) writePointAt(runID string, buf *eventBuffer, ts time.Time) {
	pt := analyzer.RealtimePoint(runID, ts, buf.snapshot())
	if err := m.db.Create(&pt).Error; err != nil {
		log.Printf("run %s 采样点写入失败: %v", runID, err)
	}
}

// mergeValues 合并引擎自带聚合与事件派生指标；
// 同一 metric_id+aggregation 冲突时以引擎自带聚合为准（引擎自身计算更精确）。
func mergeValues(aggregates, analyzed []metric.Value) []metric.Value {
	key := func(v metric.Value) string { return v.MetricID + "\x00" + v.Aggregation }
	seen := make(map[string]bool, len(aggregates))
	merged := make([]metric.Value, 0, len(aggregates)+len(analyzed))
	for _, a := range aggregates {
		seen[key(a)] = true
		merged = append(merged, a)
	}
	for _, v := range analyzed {
		if !seen[key(v)] {
			merged = append(merged, v)
		}
	}
	return merged
}

// eventBuffer 是运行期事件内存缓冲：sink 高频写入，后台按批量/定时落库。
type eventBuffer struct {
	mu      sync.Mutex
	all     []engine.RequestEvent
	flushed int // all[:flushed] 已落库
}

// add 追加事件；返回 true 表示达到批量落库阈值（200 个）。
func (b *eventBuffer) add(e engine.RequestEvent) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.all = append(b.all, e)
	return len(b.all)-b.flushed >= 200
}

// snapshot 返回全量事件拷贝（实时采样点计算用）。
func (b *eventBuffer) snapshot() []engine.RequestEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]engine.RequestEvent(nil), b.all...)
}

// takeUnflushed 取出尚未落库的事件并推进标记。
func (b *eventBuffer) takeUnflushed() []engine.RequestEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]engine.RequestEvent(nil), b.all[b.flushed:]...)
	b.flushed = len(b.all)
	return out
}

// collectHardware 采集运行宿主硬件信息（尽力而为，失败不阻塞提交）。
func collectHardware() map[string]any {
	hw := map[string]any{
		"cpu_count": runtime.NumCPU(),
		"goos":      runtime.GOOS,
		"goarch":    runtime.GOARCH,
	}
	// 内存总量：Linux /proc/meminfo
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					var kb int64
					if _, err := fmt.Sscanf(line, "MemTotal: %d kB", &kb); err == nil {
						hw["mem_total_bytes"] = kb * 1024
					}
					break
				}
			}
		}
	}
	// 显卡列表：nvidia-smi -L
	if out, err := exec.Command("nvidia-smi", "-L").Output(); err == nil {
		var gpus []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				gpus = append(gpus, line)
			}
		}
		if len(gpus) > 0 {
			hw["gpus"] = gpus
		}
	}
	return hw
}
