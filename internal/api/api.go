// Package api 是 REST API 层（对应架构图用户层 REST API）。
//
//	GET    /health                           健康检查
//	GET    /api/v1/engines                   已注册引擎列表（含 capabilities）
//	POST   /api/v1/runs                      提交一次 benchmark（body 为 Spec JSON/YAML）
//	GET    /api/v1/runs                      运行列表（?status= 可选过滤）
//	GET    /api/v1/runs/compare?ids=a,b      多运行对比（含公平性校验）
//	GET    /api/v1/runs/:id                  运行详情（含 spec、snapshot 摘要）
//	POST   /api/v1/runs/:id/cancel           取消运行中的任务
//	GET    /api/v1/runs/:id/metrics          实时采样点（运行中曲线）
//	GET    /api/v1/runs/:id/system           系统采集样本（collector 产出）
//	GET    /api/v1/runs/:id/snapshot         运行快照全量（含硬件信息）
//	GET    /api/v1/runs/:id/report           聚合报告（JSON；?format=csv 导出 CSV；SLO 实时计算）
//	GET    /api/v1/metrics/definitions       指标注册表定义
package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/javen-yan/llm-benchmark/internal/analyzer"
	"github.com/javen-yan/llm-benchmark/internal/core"
	"github.com/javen-yan/llm-benchmark/internal/engine"
	_ "github.com/javen-yan/llm-benchmark/internal/engine/guidellm" // 注册 guidellm
	_ "github.com/javen-yan/llm-benchmark/internal/engine/mock"     // 注册 mock
	"github.com/javen-yan/llm-benchmark/internal/metric"
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
	v1.GET("/metrics/definitions", h.metricDefinitions)
	v1.POST("/runs", h.submitRun)
	v1.GET("/runs", h.listRuns)
	v1.GET("/runs/compare", h.compareRuns)
	v1.GET("/runs/:id", h.getRun)
	v1.POST("/runs/:id/cancel", h.cancelRun)
	v1.GET("/runs/:id/metrics", h.getMetrics)
	v1.GET("/runs/:id/system", h.getSystem)
	v1.GET("/runs/:id/snapshot", h.getSnapshot)
	v1.GET("/runs/:id/report", h.getReport)
}

// listEngines 返回已注册引擎及其能力声明。
func (h *Handler) listEngines(c *gin.Context) {
	out := make([]gin.H, 0)
	for _, name := range engine.Names() {
		eng, err := engine.Get(name)
		if err != nil {
			continue
		}
		out = append(out, gin.H{"name": name, "capabilities": eng.Capabilities()})
	}
	c.JSON(http.StatusOK, gin.H{"engines": out})
}

// metricDefinitions 返回指标注册表全部定义。
func (h *Handler) metricDefinitions(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"definitions": metric.DefaultRegistry().All()})
}

// submitRun 提交运行：body 为 Spec（Content-Type 含 yaml 时按 YAML 解析，否则 JSON）。
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
	c.JSON(http.StatusAccepted, gin.H{"id": id, "status": store.StatusCreated})
}

// listRuns 运行列表（?status= 可选过滤）。
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

// getRun 运行详情：基本信息 + spec + snapshot 摘要。
func (h *Handler) getRun(c *gin.Context) {
	run, snap := h.loadRun(c.Param("id"))
	if run == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "运行不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": run.ID, "name": run.Name, "engine": run.Engine, "status": run.Status,
		"error": run.Error, "spec": run.SpecMap(),
		"snapshot": gin.H{
			"model": snap.Model, "engine": snap.Engine, "engine_version": snap.EngineVersion,
			"protocol": snap.Protocol, "dataset_type": snap.DatasetType, "seed": snap.Seed,
			"input_tokens": snap.InputTokens, "output_tokens": snap.OutputTokens,
		},
		"created_at": run.CreatedAt, "started_at": run.StartedAt, "finished_at": run.FinishedAt,
	})
}

// cancelRun 取消运行中的任务。
func (h *Handler) cancelRun(c *gin.Context) {
	if err := h.manager.Cancel(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// getMetrics 实时采样点（运行中曲线）。
func (h *Handler) getMetrics(c *gin.Context) {
	var pts []store.MetricPoint
	if err := h.db.Where("run_id = ?", c.Param("id")).Order("ts ASC").Find(&pts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"points": pts})
}

// getSystem 系统采集样本（collector 产出）。
func (h *Handler) getSystem(c *gin.Context) {
	var samples []store.SystemSample
	if err := h.db.Where("run_id = ?", c.Param("id")).Order("ts ASC").Find(&samples).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"samples": samples})
}

// getSnapshot 运行快照全量（含硬件信息）。
func (h *Handler) getSnapshot(c *gin.Context) {
	_, snap := h.loadRun(c.Param("id"))
	if snap == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "快照不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"run_id": snap.RunID, "engine": snap.Engine, "engine_version": snap.EngineVersion,
		"model": snap.Model, "protocol": snap.Protocol, "dataset_type": snap.DatasetType,
		"seed": snap.Seed, "input_tokens": snap.InputTokens, "output_tokens": snap.OutputTokens,
		"hardware": snap.HardwareMap(), "created_at": snap.CreatedAt,
	})
}

// metricRow 是报告里的一行指标（含单位与名称）。
type metricRow struct {
	MetricID    string  `json:"metric_id"`
	Aggregation string  `json:"aggregation"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	Name        string  `json:"name"`
}

// getReport 聚合报告：metrics 明细 + SLO 实时判定；?format=csv 导出 CSV。
func (h *Handler) getReport(c *gin.Context) {
	id := c.Param("id")
	var run store.Run
	if err := h.db.Where("id = ?", id).First(&run).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "运行不存在"})
		return
	}
	var mvs []store.MetricValue
	if err := h.db.Where("run_id = ?", id).Order("metric_id ASC, aggregation ASC").Find(&mvs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	reg := metric.DefaultRegistry()
	rows := make([]metricRow, 0, len(mvs))
	vals := make([]metric.Value, 0, len(mvs))
	for _, mv := range mvs {
		unit, name := "", mv.MetricID
		if d, ok := reg.Get(mv.MetricID); ok {
			unit, name = d.Unit, d.Name
		}
		rows = append(rows, metricRow{
			MetricID: mv.MetricID, Aggregation: mv.Aggregation,
			Value: mv.Value, Unit: unit, Name: name,
		})
		vals = append(vals, metric.Value{
			RunID: mv.RunID, MetricID: mv.MetricID, Aggregation: mv.Aggregation,
			Value: mv.Value, Labels: mv.Labels, Timestamp: mv.Timestamp,
		})
	}
	// SLO 按运行 spec 的 thresholds 实时计算，不落库。
	var s spec.Spec
	_ = json.Unmarshal([]byte(run.SpecJSON), &s)
	slo := analyzer.CheckThresholds(vals, s.Thresholds)

	if c.Query("format") == "csv" {
		c.Header("Content-Type", "text/csv")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=report-%s.csv", id))
		w := csv.NewWriter(c.Writer)
		_ = w.Write([]string{"metric", "aggregation", "value"})
		for _, r := range rows {
			_ = w.Write([]string{r.MetricID, r.Aggregation, strconv.FormatFloat(r.Value, 'f', -1, 64)})
		}
		w.Flush()
		return
	}
	c.JSON(http.StatusOK, gin.H{"run_id": id, "metrics": rows, "slo": slo})
}

// fairnessItem 是一项公平性比对：某字段在各运行间的值是否一致。
type fairnessItem struct {
	Field  string `json:"field"`
	Values []any  `json:"values"`
	Match  bool   `json:"match"`
}

// compareRunItem 是 compare 接口里单个运行的条目。
type compareRunItem struct {
	ID       string                        `json:"id"`
	Name     string                        `json:"name"`
	Status   store.RunStatus               `json:"status"`
	Snapshot map[string]any                `json:"snapshot"`
	Fairness []fairnessItem                `json:"fairness"`
	Metrics  map[string]map[string]float64 `json:"metrics"`
}

// compareRuns 对比多次运行：?ids=a,b。snapshot 做公平性校验，metrics 按指标聚合值对照。
func (h *Handler) compareRuns(c *gin.Context) {
	idsParam := c.Query("ids")
	if idsParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 ids 参数（逗号分隔的运行 ID，如 ?ids=a,b）"})
		return
	}
	ids := strings.Split(idsParam, ",")

	fields := []string{"model", "engine", "dataset_type", "input_tokens", "output_tokens", "seed", "hardware.cpu"}
	fairVals := make(map[string][]any, len(fields))
	items := make([]compareRunItem, 0, len(ids))

	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		run, snap := h.loadRun(id)
		if run == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "运行不存在: " + id})
			return
		}
		hw := snap.HardwareMap()
		snapMap := map[string]any{
			"model": snap.Model, "engine": snap.Engine, "dataset_type": snap.DatasetType,
			"seed": snap.Seed, "input_tokens": snap.InputTokens, "output_tokens": snap.OutputTokens,
			"hardware": hw,
		}
		fairVals["model"] = append(fairVals["model"], snap.Model)
		fairVals["engine"] = append(fairVals["engine"], snap.Engine)
		fairVals["dataset_type"] = append(fairVals["dataset_type"], snap.DatasetType)
		fairVals["input_tokens"] = append(fairVals["input_tokens"], snap.InputTokens)
		fairVals["output_tokens"] = append(fairVals["output_tokens"], snap.OutputTokens)
		fairVals["seed"] = append(fairVals["seed"], snap.Seed)
		fairVals["hardware.cpu"] = append(fairVals["hardware.cpu"], hw["cpu_count"])

		var mvs []store.MetricValue
		_ = h.db.Where("run_id = ?", id).Find(&mvs).Error
		metrics := make(map[string]map[string]float64, len(mvs))
		for _, mv := range mvs {
			if metrics[mv.MetricID] == nil {
				metrics[mv.MetricID] = make(map[string]float64)
			}
			metrics[mv.MetricID][mv.Aggregation] = mv.Value
		}
		items = append(items, compareRunItem{
			ID: run.ID, Name: run.Name, Status: run.Status,
			Snapshot: snapMap, Metrics: metrics,
		})
	}
	if len(items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids 参数无效"})
		return
	}

	// 公平性：同一字段在各运行间是否一致。
	fairness := make([]fairnessItem, 0, len(fields))
	for _, f := range fields {
		vs := fairVals[f]
		match := true
		for i := 1; i < len(vs); i++ {
			if fmt.Sprint(vs[i]) != fmt.Sprint(vs[0]) {
				match = false
				break
			}
		}
		fairness = append(fairness, fairnessItem{Field: f, Values: vs, Match: match})
	}
	for i := range items {
		items[i].Fairness = fairness
	}
	c.JSON(http.StatusOK, gin.H{"runs": items})
}

// loadRun 读取运行与其快照；快照缺失时返回空快照（不报错）。
func (h *Handler) loadRun(id string) (*store.Run, *store.Snapshot) {
	var run store.Run
	if err := h.db.Where("id = ?", id).First(&run).Error; err != nil {
		return nil, nil
	}
	var snap store.Snapshot
	if err := h.db.Where("run_id = ?", id).First(&snap).Error; err != nil {
		snap = store.Snapshot{RunID: id}
	}
	return &run, &snap
}
