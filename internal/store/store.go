// Package store 是数据存储层（对应参考设计 §14 数据模型）。
//
// V1：PostgreSQL / SQLite（GORM）。核心原则（参考设计 §14）：
//   - 指标用 metric_value 行存储（run_id / metric_id / aggregation / value），
//     不创建 ttft_p95 之类的固定列；
//   - 每次运行保存完整 snapshot（spec / engine / 数据集 / 硬件），保证可复现、可比较。
package store

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// RunStatus 运行状态机（对应参考设计 §5 Run 生命周期）：
// created → preparing → warming_up → running → normalizing → analyzing → completed
// 任意阶段可转 failed / cancelled。
type RunStatus string

const (
	StatusCreated     RunStatus = "created"
	StatusPreparing   RunStatus = "preparing"
	StatusWarmingUp   RunStatus = "warming_up"
	StatusRunning     RunStatus = "running"
	StatusNormalizing RunStatus = "normalizing"
	StatusAnalyzing   RunStatus = "analyzing"
	StatusCompleted   RunStatus = "completed"
	StatusFailed      RunStatus = "failed"
	StatusCancelled   RunStatus = "cancelled"
)

// TerminalStatuses 终态集合。
var TerminalStatuses = []RunStatus{StatusCompleted, StatusFailed, StatusCancelled}

// IsTerminal 是否终态。
func (s RunStatus) IsTerminal() bool {
	for _, t := range TerminalStatuses {
		if s == t {
			return true
		}
	}
	return false
}

// Run 一次 benchmark 运行。
type Run struct {
	ID         string     `gorm:"primaryKey" json:"id"`
	Name       string     `json:"name"`
	Engine     string     `json:"engine"`
	Status     RunStatus  `json:"status"`
	SpecJSON   string     `gorm:"type:text" json:"-"`
	Error      string     `gorm:"type:text" json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// SpecMap 把 SpecJSON 还原为 map（API 直接透出）。
func (r *Run) SpecMap() map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(r.SpecJSON), &m); err != nil {
		return map[string]any{}
	}
	return m
}

// Snapshot 运行快照（对应参考设计 §12）：spec / engine / 数据集 / 硬件全部落库，
// 保证历史 Run 可复现；Compare 用它做公平性校验。
type Snapshot struct {
	ID            uint      `gorm:"primaryKey" json:"-"`
	RunID         string    `gorm:"index" json:"run_id"`
	SpecJSON      string    `gorm:"type:text" json:"-"`
	Engine        string    `json:"engine"`
	EngineVersion string    `json:"engine_version"`
	Model         string    `json:"model"`
	Protocol      string    `json:"protocol"`
	DatasetType   string    `json:"dataset_type"`
	Seed          int64     `json:"seed"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	HardwareJSON  string    `gorm:"type:text" json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

// HardwareMap 还原硬件信息。
func (s *Snapshot) HardwareMap() map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s.HardwareJSON), &m); err != nil {
		return map[string]any{}
	}
	return m
}

// RequestEvent 持久化的归一化请求事件（对应参考设计 §8、§14 request_event）。
type RequestEvent struct {
	ID           uint      `gorm:"primaryKey" json:"-"`
	RunID        string    `gorm:"index" json:"run_id"`
	RequestID    string    `json:"request_id"`
	ScheduledAt  time.Time `json:"scheduled_at"`
	StartedAt    time.Time `json:"started_at"`
	FirstTokenAt time.Time `json:"first_token_at"`
	FinishedAt   time.Time `json:"finished_at"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	ITLMs        string    `gorm:"type:text" json:"itl_ms,omitempty"` // JSON 数组
	Status       int       `json:"status"`
	Timeout      bool      `json:"timeout"`
	Error        string    `gorm:"type:text" json:"error,omitempty"`
}

// MetricValue 聚合指标值（对应参考设计 §14 metric_value）。
// run_id / metric_id / aggregation / value / labels / timestamp，无固定指标列。
type MetricValue struct {
	ID          uint      `gorm:"primaryKey" json:"-"`
	RunID       string    `gorm:"index:idx_run_metric,priority:1" json:"run_id"`
	MetricID    string    `gorm:"index:idx_run_metric,priority:2" json:"metric_id"`
	Aggregation string    `json:"aggregation"`
	Value       float64   `json:"value"`
	Labels      string    `json:"labels,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// MetricPoint 实时采样点（运行中曲线用，非 registry 指标）。
type MetricPoint struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	RunID     string    `gorm:"index" json:"run_id"`
	Ts        time.Time `gorm:"index" json:"ts"`
	RPS       float64   `json:"rps"`
	TPS       float64   `json:"tps"`
	TTFTP50Ms float64   `json:"ttft_p50_ms"`
	TPOTP50Ms float64   `json:"tpot_p50_ms"`
	P99Ms     float64   `json:"p99_ms"`
}

// SystemSample 被动采集的系统样本（collector 产出）。
type SystemSample struct {
	ID         uint      `gorm:"primaryKey" json:"-"`
	RunID      string    `gorm:"index" json:"run_id"`
	Ts         time.Time `gorm:"index" json:"ts"`
	CPUUtilPct float64   `json:"cpu_util_pct"`
	MemUsedPct float64   `json:"mem_used_pct"`
	Load1      float64   `json:"load1"`
	NetRxBytes uint64    `json:"net_rx_bytes"`
	NetTxBytes uint64    `json:"net_tx_bytes"`
}

// Open 打开数据库并建表。dsn 为空时用 ./bench.db（SQLite）。
func Open(dsn string) (*gorm.DB, error) {
	var dialector gorm.Dialector
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		dialector = postgres.Open(dsn)
	} else {
		if dsn == "" {
			dsn = "bench.db"
		}
		dialector = sqlite.Open(dsn)
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&Run{}, &Snapshot{}, &RequestEvent{}, &MetricValue{}, &MetricPoint{}, &SystemSample{},
	); err != nil {
		return nil, err
	}
	return db, nil
}
