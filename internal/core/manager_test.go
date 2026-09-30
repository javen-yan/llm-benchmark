package core

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/javen-yan/llm-benchmark/internal/spec"
	"github.com/javen-yan/llm-benchmark/internal/store"

	// 注册 mock 引擎（init 注册，与 cmd/server 一致）
	_ "github.com/javen-yan/llm-benchmark/internal/engine/mock"
)

// openTestDB 打开隔离的内存 SQLite（每测试独立命名，避免串扰）。
func openTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := store.Open("file:" + name + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取 sql.DB 失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1) // 内存库串行写，避免 SQLITE_BUSY
	return db
}

// degradedSpec 构造 mock 退化模拟 spec：endpoint 指向不可达地址，
// 走本地延迟模拟，不依赖外部网络。
func degradedSpec() spec.Spec {
	stream := true
	return spec.Spec{
		Name:   "manager-test",
		Target: spec.Target{Endpoint: "http://127.0.0.1:1", Model: "mock-llm"},
		Engine: spec.EngineRef{Type: "mock"},
		Dataset: spec.Dataset{
			Type: spec.DatasetRandom, InputTokens: 8, OutputTokens: 4,
		},
		Load: spec.Load{
			Mode: spec.LoadClosedLoop, Concurrency: 2,
			Requests: 8, Stream: &stream,
		},
	}
}

// waitStatus 轮询运行状态直到终态或超时；返回最终状态。
func waitStatus(t *testing.T, db *gorm.DB, id string, timeout time.Duration) store.RunStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var run store.Run
		if err := db.Where("id = ?", id).First(&run).Error; err != nil {
			t.Fatalf("查询运行状态失败: %v", err)
		}
		if run.Status.IsTerminal() {
			return run.Status
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("等待运行 %s 进入终态超时（%v）", id, timeout)
	return ""
}

// TestSubmitRunsToCompleted 提交 mock 运行，全链路跑到 completed。
func TestSubmitRunsToCompleted(t *testing.T) {
	db := openTestDB(t, "core_submit")
	m := NewManager(db, 1)

	id, err := m.Submit(degradedSpec())
	if err != nil {
		t.Fatalf("Submit 失败: %v", err)
	}
	if id == "" {
		t.Fatalf("Submit 应返回非空 runID")
	}

	if st := waitStatus(t, db, id, 30*time.Second); st != store.StatusCompleted {
		t.Fatalf("期望终态 completed，实际 %s", st)
	}

	// metric_value 有聚合行
	var mvCount int64
	if err := db.Model(&store.MetricValue{}).Where("run_id = ?", id).Count(&mvCount).Error; err != nil {
		t.Fatalf("统计 metric_value 失败: %v", err)
	}
	if mvCount == 0 {
		t.Fatalf("completed 后 metric_value 应有数据")
	}

	// snapshot 存在
	var snap store.Snapshot
	if err := db.Where("run_id = ?", id).First(&snap).Error; err != nil {
		t.Fatalf("snapshot 应存在: %v", err)
	}
	if snap.Model != "mock-llm" {
		t.Fatalf("snapshot.model 应为 mock-llm，实际 %q", snap.Model)
	}

	// request_event 恰好 8 条（requests=8）
	var evCount int64
	if err := db.Model(&store.RequestEvent{}).Where("run_id = ?", id).Count(&evCount).Error; err != nil {
		t.Fatalf("统计 request_event 失败: %v", err)
	}
	if evCount != 8 {
		t.Fatalf("request_event 应为 8 条，实际 %d", evCount)
	}
}

// TestSubmitInvalidSpec 非法 spec 提交应直接报错。
func TestSubmitInvalidSpec(t *testing.T) {
	db := openTestDB(t, "core_invalid")
	m := NewManager(db, 1)

	s := degradedSpec()
	s.Name = ""
	if _, err := m.Submit(s); err == nil {
		t.Fatalf("非法 spec 应提交失败")
	}
}

// TestCancelRunningRun 取消长时间运行，状态转为 cancelled。
func TestCancelRunningRun(t *testing.T) {
	db := openTestDB(t, "core_cancel")
	m := NewManager(db, 1)

	s := degradedSpec()
	s.Load.Requests = 0
	s.Load.Duration = "20s" // 时长模式，不取消会跑 20s

	id, err := m.Submit(s)
	if err != nil {
		t.Fatalf("Submit 失败: %v", err)
	}

	// 等运行真正启动（离开 created）再取消，避免 Cancel 竞态
	deadline := time.Now().Add(5 * time.Second)
	for {
		var run store.Run
		if err := db.Where("id = ?", id).First(&run).Error; err != nil {
			t.Fatalf("查询运行状态失败: %v", err)
		}
		if run.Status != store.StatusCreated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("运行 %s 迟迟未启动", id)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 取消可能撞上启动瞬间，失败则短暂重试
	cancelled := false
	for i := 0; i < 30; i++ {
		if err := m.Cancel(id); err == nil {
			cancelled = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !cancelled {
		t.Fatalf("Cancel 始终失败")
	}

	if st := waitStatus(t, db, id, 10*time.Second); st != store.StatusCancelled {
		t.Fatalf("期望终态 cancelled，实际 %s", st)
	}
}

// TestCancelUnknownRun 取消不存在的运行应报错。
func TestCancelUnknownRun(t *testing.T) {
	db := openTestDB(t, "core_cancel_unknown")
	m := NewManager(db, 1)
	if err := m.Cancel("no-such-run"); err == nil {
		t.Fatalf("取消不存在的运行应报错")
	}
}
