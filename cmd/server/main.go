// Command server 是 LLM Benchmark Platform 的服务端入口：Gin 单二进制，
// 内嵌 Web UI（web/ 经 npm run build 输出到 internal/webui/dist），
// 对外提供 REST API + 静态页面。
//
// 环境变量：LISTEN（默认 :8080）、DATABASE_URL（空则用 ./bench.db SQLite）、
// MAX_CONCURRENT（默认 1）。
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/javen-yan/llm-benchmark/internal/api"
	"github.com/javen-yan/llm-benchmark/internal/config"
	"github.com/javen-yan/llm-benchmark/internal/core"
	"github.com/javen-yan/llm-benchmark/internal/store"
	"github.com/javen-yan/llm-benchmark/internal/webui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return err
	}

	db, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}

	manager := core.NewManager(db, cfg.MaxConcurrent)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	api.New(db, manager).RegisterRoutes(r)
	webui.Mount(r)

	addr := cfg.Listen
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	fmt.Printf("LLM Benchmark Platform 已启动\n  Web UI: http://%s/\n  API:    http://%s/api/v1\n", addr, addr)
	return r.Run(cfg.Listen)
}
