// Command server 是 CandleForge 的 Go 主服务入口。
package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jiangbohhh/candleforge/backend-go/internal/api"
	"github.com/jiangbohhh/candleforge/backend-go/internal/broker"
	"github.com/jiangbohhh/candleforge/backend-go/internal/config"
	"github.com/jiangbohhh/candleforge/backend-go/internal/grpcclient"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/risk"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
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

	// Python 回测服务 gRPC 客户端
	quant, err := grpcclient.New(cfg.QuantAddr)
	if err != nil {
		log.Fatalf("dial quant service: %v", err)
	}
	defer quant.Close()

	// Binance 数据源
	src := market.NewBinanceSource()

	// 预置标的 + 历史回填（空表才回填）
	ctx := context.Background()
	if err := market.SeedAndBackfill(ctx, st, src); err != nil {
		log.Printf("seed/backfill warning: %v", err)
	}

	// 实时行情：Binance ticker -> 缓存 -> 前端 WS Hub
	hub := ws.NewHub()
	go hub.Run()
	live := market.NewLiveFeed(src, hub)
	live.Start(ctx)

	// 模拟盘撮合引擎 (M3)
	priceFn := func(symbol string) float64 {
		for _, t := range live.Snapshot() {
			if t.Symbol == symbol {
				return t.Price
			}
		}
		return 0
	}
	simBroker := broker.NewSimBroker(db, st, priceFn, hub)

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
		bb, err := broker.NewBinanceBroker(ctx, st, hub, liveAcct.ID, opts)
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

	// HTTP 服务
	srv := api.New(db, st, quant, src, live, hub, brokers, riskEngine, envInfo)
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
