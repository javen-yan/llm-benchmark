// Package api 是 REST API 层（对应架构图用户层 REST API）。
//
//	POST   /api/v1/runs              提交一次 benchmark（body 为 Benchmark Spec JSON/YAML）
//	GET    /api/v1/runs              运行列表（?status= 可选过滤）
//	GET    /api/v1/runs/:id          运行详情（含 spec）
//	POST   /api/v1/runs/:id/cancel   取消运行中的任务
//	GET    /api/v1/runs/:id/metrics  采样点（实时曲线）
//	GET    /api/v1/runs/:id/report   聚合报告（JSON；?format=csv 导出 CSV）
//	GET    /api/v1/engines           已注册引擎列表
//	GET    /health                   健康检查
package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/javen-yan/llm-benchmark/internal/core"
	"github.com/javen-yan/llm-benchmark/internal/engine"
	_ "github.com/javen-yan/llm-benchmark/internal/engine/guidellm" // 注册 guidellm
	_ "github.com/javen-yan/llm-benchmark/internal/engine/mock"     // 注册 mock
	"github.com/javen-yan/llm-benchmark/internal/spec"
	"github.com/javen-yan/llm-benchmark/internal/store"
)

// Handler 持有依赖。
type Handler struct {
	db      *gorm.DB
	manager *core.Manager
}

// New 创建 Handler。
func New(db *gorm.DB, manager *core.Manager) *Handler {
	return &Handler{db: db, manager: manager}
}

// RegisterRoutes 注册路由。
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	v1 := r.Group("/api/v1")
	v1.GET("/engines", h.listEngines)
	v1.POST("/runs", h.submitRun)
	v1.GET("/runs", h.listRuns)
	v1.GET("/runs/:id", h.getRun)
	v1.POST("/runs/:id/cancel", h.cancelRun)
	v1.GET("/runs/:id/metrics", h.getMetrics)
	v1.GET("/runs/:id/report", h.getReport)
}

func (h *Handler) listEngines(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"engines": engine.Names()})
}

// submitRun 提交运行：body 为 Spec JSON（Content-Type 含 yaml 时按 YAML 解析）。
func (h *Handler) submitRun(c *gin.Context) {
	body, err := c.GetRawData()
	if err != nil || len(body) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体不能为空（Benchmark Spec JSON/YAML）"})
		return
	}
	s, err := spec.LoadBody(c.GetHeader("Content-Type"), body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, err := h.manager.Submit(s)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": id, "status": store.StatusPending})
}

func (h *Handler) listRuns(c *gin.Context) {
	var runs []store.Run
	q := h.db.Order("created_at DESC")
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	if err := q.Limit(100).Find(&runs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

func (h *Handler) getRun(c *gin.Context) {
	var run store.Run
	if err := h.db.Where("id = ?", c.Param("id")).First(&run).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "运行不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": run.ID, "name": run.Name, "engine": run.Engine, "status": run.Status,
		"error": run.Error, "spec": run.SpecMap(),
		"created_at": run.CreatedAt, "started_at": run.StartedAt, "finished_at": run.FinishedAt,
	})
}

func (h *Handler) cancelRun(c *gin.Context) {
	if err := h.manager.Cancel(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) getMetrics(c *gin.Context) {
	var pts []store.MetricPoint
	if err := h.db.Where("run_id = ?", c.Param("id")).Order("ts ASC").Find(&pts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"points": pts})
}

// getReport 聚合报告：默认 JSON，?format=csv 导出 CSV（报告导出的 Phase 1 形态）。
func (h *Handler) getReport(c *gin.Context) {
	var sum store.Summary
	if err := h.db.Where("run_id = ?", c.Param("id")).First(&sum).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "报告尚未生成（运行可能未完成）"})
		return
	}
	if c.Query("format") == "csv" {
		c.Header("Content-Type", "text/csv")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=report-%s.csv", sum.RunID))
		w := csv.NewWriter(c.Writer)
		_ = w.Write([]string{"metric", "value"})
		rows := [][2]string{
			{"run_id", sum.RunID}, {"total_requests", itoa(sum.TotalRequests)},
			{"success_count", itoa(sum.SuccessCount)}, {"fail_count", itoa(sum.FailCount)},
			{"duration_sec", f2(sum.DurationSec)}, {"rps", f2(sum.RPS)}, {"tps", f2(sum.TPS)},
			{"ttft_p50_ms", f2(sum.TTFTP50Ms)}, {"ttft_p99_ms", f2(sum.TTFTP99Ms)},
			{"tpot_p50_ms", f2(sum.TPOTP50Ms)}, {"tpot_p99_ms", f2(sum.TPOTP99Ms)},
			{"e2e_p50_ms", f2(sum.EndToEndP50Ms)}, {"e2e_p99_ms", f2(sum.EndToEndP99Ms)},
			{"output_tokens", strconv.FormatInt(sum.OutputTokens, 10)},
		}
		for _, r := range rows {
			_ = w.Write(r[:])
		}
		w.Flush()
		return
	}
	// 把 SLO 判定结果并进报告。
	out := map[string]any{
		"run_id": sum.RunID, "total_requests": sum.TotalRequests,
		"success_count": sum.SuccessCount, "fail_count": sum.FailCount,
		"duration_sec": sum.DurationSec, "rps": sum.RPS, "tps": sum.TPS,
		"ttft_p50_ms": sum.TTFTP50Ms, "ttft_p99_ms": sum.TTFTP99Ms,
		"tpot_p50_ms": sum.TPOTP50Ms, "tpot_p99_ms": sum.TPOTP99Ms,
		"e2e_p50_ms": sum.EndToEndP50Ms, "e2e_p99_ms": sum.EndToEndP99Ms,
		"output_tokens": sum.OutputTokens,
	}
	if sum.SummaryJSON != "" {
		var slo any
		if err := json.Unmarshal([]byte(sum.SummaryJSON), &slo); err == nil {
			out["slo"] = slo
		}
	}
	c.JSON(http.StatusOK, out)
}

func itoa(n int) string   { return strconv.Itoa(n) }
func f2(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
