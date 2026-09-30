// Package webui 内嵌 Web UI 构建产物（web/ 经 npm run build 输出到 internal/webui/dist），
// 挂载静态文件 + SPA 回退。无产物时优雅降级（API 照常可用）。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var dist embed.FS

// Mount 挂载前端路由。
func Mount(r *gin.Engine) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		fallback(r, "Web UI 未构建")
		return
	}
	if _, err := sub.Open("index.html"); err != nil {
		fallback(r, "Web UI 未构建：请先 cd web && npm run build")
		return
	}
	if assets, err := fs.Sub(sub, "assets"); err == nil {
		r.StaticFS("/assets", http.FS(assets))
	}
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || p == "/health" {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.FileFromFS("/", http.FS(sub))
	})
}

func fallback(r *gin.Engine, msg string) {
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || p == "/health" {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.String(http.StatusOK, msg)
	})
}
