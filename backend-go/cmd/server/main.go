// Command server 是 CandleForge 的 Go 主服务入口。
package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jiangbohhh/candleforge/backend-go/internal/api"
	"github.com/jiangbohhh/candleforge/backend-go/internal/config"
	"github.com/jiangbohhh/candleforge/backend-go/internal/grpcclient"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
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

	// HTTP 服务
	srv := api.New(db, st, quant, src, live, hub)
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
