// Package store 是数据存储层（对应架构图数据存储层）。
// GORM 实现：默认 SQLite（零依赖本地跑），DATABASE_URL 以 postgres:// 开头时切 PostgreSQL。
// 时序指标存 MetricPoint 表；原始请求事件 JSON 进 RawEvents（对象存储 Phase 2 再接 S3）。
package store

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// RunStatus 运行状态机：pending → running → succeeded | failed | cancelled。
type RunStatus string

const (
	StatusPending   RunStatus = "pending"
	StatusRunning   RunStatus = "running"
	StatusSucceeded RunStatus = "succeeded"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

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

// MetricPoint 采样点（时序）：引擎执行过程中按固定间隔写入，供实时曲线用。
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

// Summary 聚合结果（Analyzer 产出，存 JSON）。
type Summary struct {
	RunID         string  `gorm:"primaryKey" json:"run_id"`
	TotalRequests int     `json:"total_requests"`
	SuccessCount  int     `json:"success_count"`
	FailCount     int     `json:"fail_count"`
	DurationSec   float64 `json:"duration_sec"`
	RPS           float64 `json:"rps"`
	TPS           float64 `json:"tps"`
	TTFTP50Ms     float64 `json:"ttft_p50_ms"`
	TTFTP99Ms     float64 `json:"ttft_p99_ms"`
	TPOTP50Ms     float64 `json:"tpot_p50_ms"`
	TPOTP99Ms     float64 `json:"tpot_p99_ms"`
	EndToEndP50Ms float64 `json:"e2e_p50_ms"`
	EndToEndP99Ms float64 `json:"e2e_p99_ms"`
	OutputTokens  int64   `json:"output_tokens"`
	SummaryJSON   string  `gorm:"type:text" json:"-"`
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
	if err := db.AutoMigrate(&Run{}, &MetricPoint{}, &Summary{}); err != nil {
		return nil, err
	}
	return db, nil
}
