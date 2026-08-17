// Package config 负责从环境变量加载运行配置。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config 是 Go 主服务的运行配置。
type Config struct {
	HTTPAddr    string // HTTP 监听地址，如 :8080
	QuantAddr   string // Python 回测服务 gRPC 地址，如 quant-py:50051
	DatabaseURL string // PostgreSQL 连接串

	// Binance 实盘（M4）；任一为空 → 不构造 live broker
	BinanceAPIKey    string
	BinanceAPISecret string
	BinanceMainnet   bool   // true → 主网，false → testnet（默认）
	BinanceConfirm   string // 主网启用时必须等于 "I_UNDERSTAND_REAL_MONEY"
	BinanceRESTBase  string // 可选；为空走 testnet/mainnet 默认
	BinanceWSBase    string // 可选；为空走 testnet/mainnet 默认

	// Binance USDT-M 永续（M6.5）；合约 testnet 与现货 testnet 是两套独立密钥。
	// 任一为空 → 不构造 futures broker。主网复用 BINANCE_MAINNET + CONFIRM 双开关。
	BinanceFuturesAPIKey    string
	BinanceFuturesAPISecret string

	// M6 策略引擎
	MaxRunningStrategies int // 并行策略上限；默认 5
}

// Load 从环境变量读取配置，缺省时回落到适合本地开发的默认值。
func Load() *Config {
	return &Config{
		HTTPAddr:             getenv("HTTP_ADDR", ":8080"),
		QuantAddr:            getenv("QUANT_ADDR", "localhost:50051"),
		DatabaseURL:          getenv("DATABASE_URL", defaultDatabaseURL()),
		BinanceAPIKey:        os.Getenv("BINANCE_API_KEY"),
		BinanceAPISecret:     os.Getenv("BINANCE_API_SECRET"),
		BinanceMainnet:       getbool("BINANCE_MAINNET", false),
		BinanceConfirm:       os.Getenv("BINANCE_MAINNET_CONFIRM"),
		BinanceRESTBase:      os.Getenv("BINANCE_REST_BASE"),
		BinanceWSBase:        os.Getenv("BINANCE_WS_BASE"),

		BinanceFuturesAPIKey:    os.Getenv("BINANCE_FUTURES_API_KEY"),
		BinanceFuturesAPISecret: os.Getenv("BINANCE_FUTURES_API_SECRET"),

		MaxRunningStrategies: getint("MAX_RUNNING_STRATEGIES", 5),
	}
}

// LiveBrokerEnabled 报告是否能构造 BinanceBroker（仅看 key/secret）。
func (c *Config) LiveBrokerEnabled() bool {
	return c.BinanceAPIKey != "" && c.BinanceAPISecret != ""
}

// FuturesBrokerEnabled 报告是否能构造 BinanceFuturesBroker（仅看 key/secret）。
func (c *Config) FuturesBrokerEnabled() bool {
	return c.BinanceFuturesAPIKey != "" && c.BinanceFuturesAPISecret != ""
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getbool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func getint(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
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
