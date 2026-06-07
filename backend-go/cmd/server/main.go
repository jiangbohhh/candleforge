// Command server 是 CandleForge 的 Go 主服务入口。
package main

import (
	"database/sql"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jiangbohhh/candleforge/backend-go/internal/api"
	"github.com/jiangbohhh/candleforge/backend-go/internal/config"
	"github.com/jiangbohhh/candleforge/backend-go/internal/grpcclient"
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

	// Python 回测服务 gRPC 客户端
	quant, err := grpcclient.New(cfg.QuantAddr)
	if err != nil {
		log.Fatalf("dial quant service: %v", err)
	}
	defer quant.Close()

	// HTTP 服务
	srv := api.New(db, quant)
	if err := srv.Router().Run(cfg.HTTPAddr); err != nil {
		log.Fatalf("http server: %v", err)
	}
}
