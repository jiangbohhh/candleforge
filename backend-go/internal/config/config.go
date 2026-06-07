// Package config 负责从环境变量加载运行配置。
package config

import (
	"fmt"
	"os"
)

// Config 是 Go 主服务的运行配置。
type Config struct {
	HTTPAddr    string // HTTP 监听地址，如 :8080
	QuantAddr   string // Python 回测服务 gRPC 地址，如 quant-py:50051
	DatabaseURL string // PostgreSQL 连接串
}

// Load 从环境变量读取配置，缺省时回落到适合本地开发的默认值。
func Load() *Config {
	return &Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		QuantAddr:   getenv("QUANT_ADDR", "localhost:50051"),
		DatabaseURL: getenv("DATABASE_URL", defaultDatabaseURL()),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultDatabaseURL() string {
	host := getenv("DB_HOST", "localhost")
	port := getenv("DB_PORT", "5432")
	user := getenv("DB_USER", "candleforge")
	pass := getenv("DB_PASSWORD", "candleforge")
	name := getenv("DB_NAME", "candleforge")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		user, pass, host, port, name)
}
