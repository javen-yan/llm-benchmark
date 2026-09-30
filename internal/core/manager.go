// Package core 是平台核心层（对应架构图 Go Platform Core）。
// RunManager：任务生命周期管理（pending→running→succeeded/failed/cancelled 状态机），
// 异步执行；Scheduler 在 Phase 1 简化为串行队列 + 可配并发上限；
// Snapshot：运行创建时记录 spec 与环境快照（存 SpecJSON）。
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/javen-yan/llm-benchmark/internal/analyzer"
	"github.com/javen-yan/llm-benchmark/internal/engine"
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

// Submit 提交一次运行：落库（pending）后异步执行，立即返回 runID。
func (m *Manager) Submit(s spec.Spec) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if _, err := engine.Get(s.Engine); err != nil {
		return "", err
	}
	specJSON, _ := json.Marshal(s)
	run := store.Run{
		ID:        uuid.NewString(),
		Name:      s.Name,
		Engine:    s.Engine,
		Status:    store.StatusPending,
		SpecJSON:  string(specJSON),
		CreatedAt: time.Now(),
	}
	if err := m.db.Create(&run).Error; err != nil {
		return "", fmt.Errorf("创建运行记录失败: %w", err)
	}
	go m.execute(run.ID, s)
	return run.ID, nil
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

// execute 是实际执行体：状态机流转 + 指标采样 + 结果落库。
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

	now := time.Now()
	m.db.Model(&store.Run{}).Where("id = ?", id).Updates(map[string]any{
		"status":     store.StatusRunning,
		"started_at": now,
	})

	eng, _ := engine.Get(s.Engine)
	var lastPt time.Time
	progress := func(done, total int, events []engine.RequestEvent) {
		// 节流：最多每 2 秒写一个采样点（实时曲线用）。
		if time.Since(lastPt) < 2*time.Second {
			return
		}
		lastPt = time.Now()
		pt := analyzer.SnapshotPoint(id, time.Now(), events)
		_ = m.db.Create(&pt).Error
	}

	// 包装 progress 以收集事件（Mock 引擎不直接暴露事件，改从 Result 取）。
	result, err := eng.Run(ctx, s, progress)
	finish := time.Now()

	finalStatus := store.StatusSucceeded
	errMsg := ""
	if err != nil {
		if ctx.Err() == context.Canceled {
			finalStatus = store.StatusCancelled
		} else {
			finalStatus = store.StatusFailed
		}
		errMsg = err.Error()
	}

	// 落库 Summary：引擎预聚合优先，否则从事件计算。
	var summary store.Summary
	if result.Summary != nil {
		st := result.Summary
		summary = store.Summary{
			RunID: id, TotalRequests: st.TotalRequests, SuccessCount: st.SuccessCount,
			FailCount: st.FailCount, DurationSec: st.DurationSec, RPS: st.RPS, TPS: st.TPS,
			TTFTP50Ms: st.TTFTP50Ms, TTFTP99Ms: st.TTFTP99Ms,
			TPOTP50Ms: st.TPOTP50Ms, TPOTP99Ms: st.TPOTP99Ms,
			EndToEndP50Ms: st.E2EP50Ms, EndToEndP99Ms: st.E2EP99Ms,
			OutputTokens: st.OutputTokens,
		}
	} else {
		summary = analyzer.Summarize(id, result.Events)
	}
	// SLO 判定，结果存进 SummaryJSON。
	sloResult := analyzer.CheckSLO(summary, s.SLO)
	if b, err := json.Marshal(sloResult); err == nil {
		summary.SummaryJSON = string(b)
	}
	_ = m.db.Create(&summary).Error
	// 收尾再补一个采样点，保证曲线有终点。
	_ = m.db.Create(&store.MetricPoint{
		RunID: id, Ts: finish, RPS: summary.RPS, TPS: summary.TPS,
		TTFTP50Ms: summary.TTFTP50Ms, TPOTP50Ms: summary.TPOTP50Ms, P99Ms: summary.EndToEndP99Ms,
	}).Error

	m.db.Model(&store.Run{}).Where("id = ?", id).Updates(map[string]any{
		"status":      finalStatus,
		"error":       errMsg,
		"finished_at": finish,
	})
}
