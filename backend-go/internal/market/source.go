// Package market 负责加密行情的采集（Binance），含历史回填与实时订阅。
package market

import (
	"context"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// Tick 是一次实时行情快照。
type Tick struct {
	Symbol string  `json:"symbol"` // 统一 symbol
	Price  float64 `json:"price"`
	Open   float64 `json:"open"`   // 24h 开盘
	High   float64 `json:"high"`   // 24h 最高
	Low    float64 `json:"low"`    // 24h 最低
	Volume float64 `json:"volume"` // 24h 成交量
	Time   int64   `json:"time"`   // 事件时间 (Unix 毫秒)
}

// MarketDataSource 抽象行情数据源；当前实现为 Binance，未来可加 A 股等。
type MarketDataSource interface {
	// GetKlines 取历史 K 线（统一 symbol，limit 上限 1000）。
	GetKlines(ctx context.Context, symbol, interval string, limit int) ([]store.Kline, error)
	// SubscribeTicker 订阅实时行情；返回 stop 函数用于停止。
	SubscribeTicker(symbols []string, onTick func(Tick)) (stop func(), err error)
}
