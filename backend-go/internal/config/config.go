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

	// 风控 / 撮合（E2/E3）
	RiskMaxNotional float64 // 单笔名义额上限 USDT；<=0 不设上限
	SimCommission   float64 // 模拟盘手续费率；默认 0.001

	// 安全（WP2）
	AuthToken        string   // 静态 API Token；空 → 鉴权关闭（仅本地开发）
	CORSOrigins      []string // CORS 白名单；空 → 反射任意来源（仅本地开发）
	CredentialEncKey string   // 子账户凭证加密主密钥（64-hex 或 base64 的 32 字节）；空 → 明文落库（仅本地开发）
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

		RiskMaxNotional: getfloat("RISK_MAX_NOTIONAL", 50000),
		SimCommission:   getfloat("SIM_COMMISSION", 0.001),

		AuthToken:        os.Getenv("AUTH_TOKEN"),
		CORSOrigins:      getlist("CORS_ORIGINS"),
		CredentialEncKey: os.Getenv("CREDENTIAL_ENC_KEY"),
	}
}

// getlist 读取逗号分隔的环境变量为字符串切片（去空白，忽略空项）。
func getlist(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// getfloat 读取浮点环境变量；空/非法/负数回落默认值。
func getfloat(key string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return fallback
	}
	return f
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
