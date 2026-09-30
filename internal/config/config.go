// Package config 读取服务配置（环境变量）。
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config 服务配置。
type Config struct {
	// Listen 监听地址，默认 :8080。
	Listen string
	// DatabaseURL 为空用 ./bench.db (SQLite)；postgres:// 开头用 PostgreSQL。
	DatabaseURL string
	// MaxConcurrent 最大并发运行数，默认 1。
	MaxConcurrent int
}

// Load 从环境变量加载配置。
func Load() Config {
	c := Config{
		Listen:        getenv("LISTEN", ":8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		MaxConcurrent: 1,
	}
	if v := os.Getenv("MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxConcurrent = n
		}
	}
	return c
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Validate 校验（目前无必填项，预留）。
func (c Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("LISTEN 不能为空")
	}
	return nil
}
