// Command server 是 CandleForge 的 Go 主服务入口。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jiangbohhh/candleforge/backend-go/internal/api"
	"github.com/jiangbohhh/candleforge/backend-go/internal/broker"
	"github.com/jiangbohhh/candleforge/backend-go/internal/config"
	"github.com/jiangbohhh/candleforge/backend-go/internal/events"
	"github.com/jiangbohhh/candleforge/backend-go/internal/grpcclient"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/risk"
	"github.com/jiangbohhh/candleforge/backend-go/internal/credcrypto"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/strategy"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

func main() {
	cfg := config.Load()
	log.Printf("candleforge-go starting | http=%s quant=%s", cfg.HTTPAddr, cfg.QuantAddr)

	// 数据库连接（惰性，真正连通在首次查询/Ping 时校验）
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	// 等待数据库就绪后跑迁移
	if err := waitDB(db, 30*time.Second); err != nil {
		log.Fatalf("database not ready: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("migrations applied")

	st := store.New(db)

	// 凭证加密（WP2/C3）：配置了主密钥则加密子账户 API 密钥，否则明文（仅本地开发）。
	cipher, err := credcrypto.FromString(cfg.CredentialEncKey)
	if err != nil {
		log.Fatalf("credential encryption key: %v", err)
	}
	st.SetCipher(cipher)
	if cipher == nil {
		log.Println("⚠️  CREDENTIAL_ENC_KEY 未设置：子账户 API 密钥将以明文落库（仅本地开发）")
	}
	if cfg.AuthToken == "" {
		log.Println("⚠️  AUTH_TOKEN 未设置：API 无鉴权（仅本地开发；实盘前必须配置）")
	}
	if len(cfg.CORSOrigins) == 0 {
		log.Println("⚠️  CORS_ORIGINS 未设置：反射任意来源（仅本地开发）")
	}
	// WS 升级来源白名单（与 HTTP CORS 收紧一致）。
	ws.SetAllowedOrigins(cfg.CORSOrigins)

	// Python 回测服务 gRPC 客户端
	quant, err := grpcclient.New(cfg.QuantAddr)
	if err != nil {
		log.Fatalf("dial quant service: %v", err)
	}
	defer quant.Close()

	// Binance 数据源：现货(api) + USDT-M 永续(fapi)，按 .PERP 后缀路由
	perpSrc := market.NewBinanceFuturesSource()
	src := market.NewRouter(market.NewBinanceSource(), perpSrc)

	// 预置标的 + 历史回填（空表才回填）
	ctx := context.Background()
	if err := market.SeedAndBackfill(ctx, st, src); err != nil {
		log.Printf("seed/backfill warning: %v", err)
	}

	// 资金费率同步：启动增量 + 每 8h 一次（永续每 8h 结算一期）
	syncFunding := func() {
		if err := market.SyncFundingRates(ctx, st, perpSrc, time.Now().UnixMilli()); err != nil {
			log.Printf("funding sync warning: %v", err)
		}
	}
	syncFunding()
	go func() {
		t := time.NewTicker(8 * time.Hour)
		defer t.Stop()
		for range t.C {
			syncFunding()
		}
	}()

	// 实时行情：Binance ticker -> 缓存 -> 前端 WS Hub
	hub := ws.NewHub()
	go hub.Run()
	live := market.NewLiveFeed(src, hub)
	live.Start(ctx)

	// 进程内事件总线（broker → 策略引擎）
	eventBus := events.NewBus()

	// 模拟盘撮合引擎 (M3)
	priceFn := func(symbol string) float64 {
		for _, t := range live.Snapshot() {
			if t.Symbol == symbol {
				return t.Price
			}
		}
		return 0
	}
	simBroker := broker.NewSimBroker(db, st, priceFn, hub, eventBus)

	// 风控引擎：默认单笔上限 50,000 USDT，未启用紧急停止
	riskEngine := risk.NewEngine(st, 50000)

	// 实盘 Binance Broker (M4) — 仅在 BINANCE_API_KEY/SECRET 配置时构造
	brokers := map[string]broker.Broker{"sim": simBroker}
	envInfo := map[string]string{}
	if cfg.LiveBrokerEnabled() {
		// 主网必须二次确认；testnet 不需要。
		if cfg.BinanceMainnet && cfg.BinanceConfirm != "I_UNDERSTAND_REAL_MONEY" {
			log.Fatalf("BINANCE_MAINNET=true 需要 BINANCE_MAINNET_CONFIRM=I_UNDERSTAND_REAL_MONEY 显式确认实盘风险")
		}

		opts := broker.BinanceOptions{
			APIKey:           cfg.BinanceAPIKey,
			APISecret:        cfg.BinanceAPISecret,
			Mainnet:          cfg.BinanceMainnet,
			RESTBaseOverride: cfg.BinanceRESTBase,
			WSBaseOverride:   cfg.BinanceWSBase,
		}
		liveAcct, err := st.EnsureLiveAccount(ctx, "live", "binance")
		if err != nil {
			log.Fatalf("ensure live account: %v", err)
		}
		bb, err := broker.NewBinanceBroker(ctx, st, hub, eventBus, liveAcct.ID, opts)
		if err != nil {
			log.Fatalf("init Binance broker: %v", err)
		}
		brokers["binance"] = bb
		envInfo["binance"] = opts.EnvLabel()
		warn := ""
		if opts.Mainnet {
			warn = " ⚠️  MAINNET (real money)"
		}
		log.Printf("Binance live broker enabled: %s%s", opts.EnvLabel(), warn)
	} else {
		log.Printf("Binance live broker disabled (BINANCE_API_KEY missing); only sim available")
	}

	// USDT-M 永续 Broker (M6.5) — 仅在 BINANCE_FUTURES_API_KEY/SECRET 配置时构造。
	// 合约 testnet 与现货 testnet 是两套独立密钥；主网复用同一对二次确认开关。
	if cfg.FuturesBrokerEnabled() {
		if cfg.BinanceMainnet && cfg.BinanceConfirm != "I_UNDERSTAND_REAL_MONEY" {
			log.Fatalf("BINANCE_MAINNET=true 需要 BINANCE_MAINNET_CONFIRM=I_UNDERSTAND_REAL_MONEY 显式确认实盘风险")
		}
		fOpts := broker.BinanceFuturesOptions{
			APIKey:    cfg.BinanceFuturesAPIKey,
			APISecret: cfg.BinanceFuturesAPISecret,
			Mainnet:   cfg.BinanceMainnet,
		}
		futAcct, err := st.EnsureLiveAccount(ctx, "live_futures", "binance_futures")
		if err != nil {
			log.Fatalf("ensure live_futures account: %v", err)
		}
		fb, err := broker.NewBinanceFuturesBroker(ctx, st, hub, eventBus, futAcct.ID, fOpts)
		if err != nil {
			log.Fatalf("init Binance futures broker: %v", err)
		}
		brokers["binance_futures"] = fb
		envInfo["binance_futures"] = fOpts.EnvLabel()
		warn := ""
		if fOpts.Mainnet {
			warn = " ⚠️  MAINNET (real money)"
		}
		log.Printf("Binance USDT-M futures broker enabled: %s%s", fOpts.EnvLabel(), warn)
	} else {
		log.Printf("Binance futures broker disabled (BINANCE_FUTURES_API_KEY missing)")
	}

	// BrokerFor 解析器：按 accountID 查 accounts 行，路由到对应 broker 实例。
	// 当前实现：sim 子账户走共享 SimBroker；live 主账户走 brokers["binance"]（若有）。
	// Phase D 后续迭代：live 子账户按 api_key 动态构建独立 BinanceBroker。
	brokerFor := func(accountID int64) (broker.Broker, error) {
		if accountID == 0 {
			return simBroker, nil
		}
		acct, err := st.GetAccount(ctx, accountID)
		if err != nil {
			return nil, fmt.Errorf("account %d: %w", accountID, err)
		}
		if b, ok := brokers[acct.Broker]; ok {
			return b, nil
		}
		// sub 账户的 broker 列跟随父账户
		return simBroker, nil
	}

	// M6 策略引擎
	mgr := strategy.NewManager(strategy.Env{
		Store:   st,
		Brokers: brokerFor,
		Risk:    riskEngine,
		PriceFn: priceFn,
		Hub:     hub,
	}, eventBus, cfg.MaxRunningStrategies)
	mgr.ResumeAll(ctx)

	// HTTP 服务
	srv := api.New(db, st, quant, src, live, hub, brokers, riskEngine, envInfo)
	srv.SetManager(mgr)
	srv.SetSecurity(cfg.AuthToken, cfg.CORSOrigins)
	if err := srv.Router().Run(cfg.HTTPAddr); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

// waitDB 轮询直到数据库可连或超时。
func waitDB(db *sql.DB, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := db.PingContext(ctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
}
