package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/javen-yan/llm-benchmark/internal/core"
	"github.com/javen-yan/llm-benchmark/internal/spec"
	"github.com/javen-yan/llm-benchmark/internal/store"
)

// testRouter 搭一套内存库 + gin 测试路由。
func testRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := store.Open("file:api_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	m := core.NewManager(db, 1)
	h := New(db, m)
	r := gin.New()
	h.RegisterRoutes(r)
	return r, db
}

// doReq 发一个测试请求。
func doReq(r *gin.Engine, method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// submitSpec 提交一份 mock 退化模拟 spec，返回 runID。
func submitSpec(t *testing.T, r *gin.Engine) string {
	t.Helper()
	stream := true
	s := spec.Spec{
		Name:   "api-test",
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
	body, _ := json.Marshal(s)
	rec := doReq(r, http.MethodPost, "/api/v1/runs", body, "application/json")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("提交应返回 202，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析提交响应失败: %v", err)
	}
	if resp.ID == "" {
		t.Fatalf("提交响应应含 runID")
	}
	return resp.ID
}

// waitCompleted 轮询运行详情直到终态。
func waitCompleted(t *testing.T, r *gin.Engine, id string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rec := doReq(r, http.MethodGet, "/api/v1/runs/"+id, nil, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("查询运行详情失败: %d", rec.Code)
		}
		var resp struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析运行详情失败: %v", err)
		}
		switch resp.Status {
		case "completed", "failed", "cancelled":
			return resp.Status
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("等待运行 %s 完成超时", id)
	return ""
}

// TestSubmitAndList 提交后列表应包含新运行。
func TestSubmitAndList(t *testing.T) {
	r, _ := testRouter(t)
	id := submitSpec(t, r)

	rec := doReq(r, http.MethodGet, "/api/v1/runs", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应返回 200，实际 %d", rec.Code)
	}
	var resp struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	found := false
	for _, run := range resp.Runs {
		if run.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("列表中未找到新提交的运行 %s", id)
	}
}

// TestReportAfterCompleted 完成后报告含指标明细与 SLO 判定。
func TestReportAfterCompleted(t *testing.T) {
	r, _ := testRouter(t)
	id := submitSpec(t, r)
	if st := waitCompleted(t, r, id); st != "completed" {
		t.Fatalf("期望 completed，实际 %s", st)
	}

	rec := doReq(r, http.MethodGet, "/api/v1/runs/"+id+"/report", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("报告应返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		RunID   string `json:"run_id"`
		Metrics []struct {
			MetricID string `json:"metric_id"`
		} `json:"metrics"`
		SLO struct {
			Passed bool `json:"passed"`
		} `json:"slo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析报告失败: %v", err)
	}
	if len(resp.Metrics) == 0 {
		t.Fatalf("报告应含指标明细")
	}
	// SLO 无阈值配置时应通过
	if !resp.SLO.Passed {
		t.Fatalf("无阈值时 SLO 应通过")
	}
}

// TestCompareFairness 对比接口返回公平性校验。
func TestCompareFairness(t *testing.T) {
	r, _ := testRouter(t)
	id := submitSpec(t, r)
	if st := waitCompleted(t, r, id); st != "completed" {
		t.Fatalf("期望 completed，实际 %s", st)
	}

	rec := doReq(r, http.MethodGet, "/api/v1/runs/compare?ids="+id, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("对比应返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Runs []struct {
			ID       string `json:"id"`
			Fairness []struct {
				Field string `json:"field"`
				Match bool   `json:"match"`
			} `json:"fairness"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析对比结果失败: %v", err)
	}
	if len(resp.Runs) != 1 || resp.Runs[0].ID != id {
		t.Fatalf("对比结果应含提交的运行，实际 %+v", resp.Runs)
	}
	if len(resp.Runs[0].Fairness) == 0 {
		t.Fatalf("对比结果应含公平性校验")
	}
}

// TestMetricDefinitions 指标定义接口非空。
func TestMetricDefinitions(t *testing.T) {
	r, _ := testRouter(t)
	rec := doReq(r, http.MethodGet, "/api/v1/metrics/definitions", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("指标定义应返回 200，实际 %d", rec.Code)
	}
	var resp struct {
		Definitions []struct {
			ID string `json:"id"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析指标定义失败: %v", err)
	}
	if len(resp.Definitions) == 0 {
		t.Fatalf("指标定义不应为空")
	}
}

// TestCancelUnknownRun 取消不存在的运行返回 400。
func TestCancelUnknownRun(t *testing.T) {
	r, _ := testRouter(t)
	rec := doReq(r, http.MethodPost, "/api/v1/runs/no-such-id/cancel", nil, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("取消不存在的运行应返回 400，实际 %d", rec.Code)
	}
}

// TestSubmitInvalidSpec 提交非法 spec 返回 400。
func TestSubmitInvalidSpec(t *testing.T) {
	r, _ := testRouter(t)
	rec := doReq(r, http.MethodPost, "/api/v1/runs",
		[]byte(`{"name":"bad"}`), "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 spec 应返回 400，实际 %d", rec.Code)
	}
}

// TestHealth 健康检查。
func TestHealth(t *testing.T) {
	r, _ := testRouter(t)
	rec := doReq(r, http.MethodGet, "/health", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("健康检查应返回 200，实际 %d", rec.Code)
	}
}
