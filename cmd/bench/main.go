// Command bench 是 LLM Benchmark Platform 的 CLI（对应架构图用户层 CLI）：
//
//	bench run -f spec.yaml [--server URL]   提交一次 benchmark
//	bench list [--server URL] [--status s]   运行列表
//	bench get <id> [--server URL]            运行详情
//	bench report <id> [--server URL]         聚合报告（含 SLO 判定）
//	bench cancel <id> [--server URL]         取消运行中的任务
//	bench compare <id1> <id2> [--server URL] 对比多次运行
//	bench mock-target [--listen :9000]       启动本地模拟目标服务（OpenAI Compatible）
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

var serverURL = "http://127.0.0.1:8080"

func main() {
	root := &cobra.Command{Use: "bench", Short: "LLM Benchmark Platform CLI"}
	root.PersistentFlags().StringVar(&serverURL, "server", serverURL, "server 地址")
	root.AddCommand(runCmd(), listCmd(), getCmd(), reportCmd(), cancelCmd(), compareCmd(), mockTargetCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runCmd() *cobra.Command {
	var specFile string
	cmd := &cobra.Command{
		Use:   "run -f spec.yaml",
		Short: "提交一次 benchmark",
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := os.ReadFile(specFile)
			if err != nil {
				return fmt.Errorf("读取 spec 失败: %w", err)
			}
			ct := "application/json"
			if strings.HasSuffix(specFile, ".yaml") || strings.HasSuffix(specFile, ".yml") {
				ct = "application/yaml"
			}
			resp, err := http.Post(serverURL+"/api/v1/runs", ct, bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("提交失败: %w", err)
			}
			defer resp.Body.Close()
			out, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 400 {
				return fmt.Errorf("服务端拒绝 (%d): %s", resp.StatusCode, strings.TrimSpace(string(out)))
			}
			var r map[string]any
			_ = json.Unmarshal(out, &r)
			fmt.Printf("已提交，运行 ID: %s\n查看状态: bench get %s\n", r["id"], r["id"])
			return nil
		},
	}
	cmd.Flags().StringVarP(&specFile, "file", "f", "", "Benchmark Spec 文件 (YAML/JSON)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func listCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "运行列表",
		RunE: func(cmd *cobra.Command, args []string) error {
			u := serverURL + "/api/v1/runs"
			if status != "" {
				u += "?status=" + status
			}
			var r map[string]any
			if err := getJSON(u, &r); err != nil {
				return err
			}
			runs, _ := r["runs"].([]any)
			if len(runs) == 0 {
				fmt.Println("暂无运行")
				return nil
			}
			fmt.Printf("%-36s %-20s %-8s %-12s %s\n", "ID", "名称", "引擎", "状态", "创建时间")
			for _, x := range runs {
				m := x.(map[string]any)
				fmt.Printf("%-36s %-20s %-8s %-12s %s\n",
					m["id"], trunc(fmt.Sprint(m["name"]), 18), m["engine"], m["status"], fmt.Sprint(m["created_at"])[:19])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "按状态过滤")
	return cmd
}

func getCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "运行详情",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r map[string]any
			if err := getJSON(serverURL+"/api/v1/runs/"+args[0], &r); err != nil {
				return err
			}
			pretty, _ := json.MarshalIndent(r, "", "  ")
			fmt.Println(string(pretty))
			return nil
		},
	}
}

// reportJSON 对应 GET /api/v1/runs/:id/report 的返回结构。
type reportJSON struct {
	RunID   string `json:"run_id"`
	Metrics []struct {
		MetricID    string  `json:"metric_id"`
		Aggregation string  `json:"aggregation"`
		Value       float64 `json:"value"`
		Unit        string  `json:"unit"`
		Name        string  `json:"name"`
	} `json:"metrics"`
	SLO struct {
		Passed     bool     `json:"passed"`
		Violations []string `json:"violations"`
	} `json:"slo"`
}

func reportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report <id>",
		Short: "聚合报告（含 SLO 判定）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r reportJSON
			if err := getJSON(serverURL+"/api/v1/runs/"+args[0]+"/report", &r); err != nil {
				return err
			}
			fmt.Printf("运行: %s\n\n", r.RunID)
			fmt.Printf("%-16s %-10s %14s  %s\n", "指标", "聚合", "值", "单位")
			fmt.Println(strings.Repeat("-", 52))
			for _, m := range r.Metrics {
				fmt.Printf("%-16s %-10s %14.2f  %s\n", m.MetricID, m.Aggregation, m.Value, m.Unit)
			}
			fmt.Println()
			if r.SLO.Passed {
				fmt.Println("SLO: 通过 ✓")
			} else {
				fmt.Println("SLO: 未通过 ✗")
				for _, v := range r.SLO.Violations {
					fmt.Println("  -", v)
				}
			}
			return nil
		},
	}
}

func cancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <id>",
		Short: "取消运行中的任务",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := http.NewRequest(http.MethodPost, serverURL+"/api/v1/runs/"+args[0]+"/cancel", nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("请求失败: %w", err)
			}
			defer resp.Body.Close()
			out, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 400 {
				return fmt.Errorf("服务端错误 (%d): %s", resp.StatusCode, strings.TrimSpace(string(out)))
			}
			fmt.Printf("已发送取消请求: %s\n", args[0])
			return nil
		},
	}
}

// compareJSON 对应 GET /api/v1/runs/compare 的返回结构。
type compareJSON struct {
	Runs []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Status   string `json:"status"`
		Fairness []struct {
			Field  string `json:"field"`
			Values []any  `json:"values"`
			Match  bool   `json:"match"`
		} `json:"fairness"`
		Metrics map[string]map[string]float64 `json:"metrics"`
	} `json:"runs"`
}

func compareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compare <id1> <id2> [id3...]",
		Short: "对比多次运行（公平性校验 + 核心指标对照）",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r compareJSON
			if err := getJSON(serverURL+"/api/v1/runs/compare?ids="+strings.Join(args, ","), &r); err != nil {
				return err
			}
			fmt.Println("公平性校验:")
			if len(r.Runs) > 0 {
				for _, f := range r.Runs[0].Fairness {
					mark := "✓"
					if !f.Match {
						mark = "✗"
					}
					fmt.Printf("  [%s] %-14s %v\n", mark, f.Field, f.Values)
				}
			}
			fmt.Println("\n核心指标对照:")
			header := fmt.Sprintf("%-16s", "指标")
			for _, run := range r.Runs {
				header += fmt.Sprintf(" %-18s", trunc(run.Name, 16))
			}
			fmt.Println(header)
			fmt.Println(strings.Repeat("-", len(header)))
			for _, mid := range []string{"llm.ttft", "llm.tpot", "llm.e2e", "llm.output_tps"} {
				for _, agg := range []string{"p50", "p99"} {
					row := fmt.Sprintf("%-16s", mid+"."+agg)
					for _, run := range r.Runs {
						v := 0.0
						if m, ok := run.Metrics[mid]; ok {
							v = m[agg]
						}
						row += fmt.Sprintf(" %-18.2f", v)
					}
					fmt.Println(row)
				}
			}
			return nil
		},
	}
}

// mockTargetCmd 启动本地模拟目标服务：实现 /v1/chat/completions（SSE 流式）与 /health，
// 按配置的延迟回 token，用于本机端到端验证。
func mockTargetCmd() *cobra.Command {
	var listen string
	var ttftMs, tpotMs int
	cmd := &cobra.Command{
		Use:   "mock-target",
		Short: "启动本地模拟目标服务（OpenAI Compatible）",
		RunE: func(cmd *cobra.Command, args []string) error {
			gin.SetMode(gin.ReleaseMode)
			r := gin.New()
			r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
			r.POST("/v1/chat/completions", func(c *gin.Context) {
				var req struct {
					Model     string `json:"model"`
					MaxTokens int    `json:"max_tokens"`
					Stream    bool   `json:"stream"`
				}
				if err := c.ShouldBindJSON(&req); err != nil {
					c.JSON(400, gin.H{"error": err.Error()})
					return
				}
				n := req.MaxTokens
				if n <= 0 {
					n = 32
				}
				time.Sleep(time.Duration(ttftMs) * time.Millisecond)
				if !req.Stream {
					c.JSON(200, gin.H{
						"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": strings.Repeat("token ", n)}}},
						"usage":   map[string]any{"completion_tokens": n},
					})
					return
				}
				c.Header("Content-Type", "text/event-stream")
				c.Header("Cache-Control", "no-cache")
				w := bufio.NewWriter(c.Writer)
				defer w.Flush()
				for i := 0; i < n; i++ {
					chunk := map[string]any{
						"choices": []any{map[string]any{"delta": map[string]any{"content": "token "}}},
					}
					b, _ := json.Marshal(chunk)
					fmt.Fprintf(w, "data: %s\n\n", b)
					// 每块立即刷到网络，保证客户端能逐块收到（真实流式）。
					w.Flush()
					if f, ok := c.Writer.(http.Flusher); ok {
						f.Flush()
					}
					time.Sleep(time.Duration(tpotMs) * time.Millisecond)
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
				w.Flush()
			})
			fmt.Printf("mock-target 已启动: http://127.0.0.1%s (ttft=%dms, tpot=%dms)\n", listen, ttftMs, tpotMs)
			return r.Run(listen)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":9000", "监听地址")
	cmd.Flags().IntVar(&ttftMs, "ttft-ms", 120, "模拟首 token 延迟（毫秒）")
	cmd.Flags().IntVar(&tpotMs, "tpot-ms", 25, "模拟每 token 延迟（毫秒）")
	return cmd
}

func getJSON(url string, out any) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("服务端错误 (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func trunc(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}
