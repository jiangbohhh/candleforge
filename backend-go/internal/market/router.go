package market

import (
	"context"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// Router 按 symbol 后缀把请求路由到现货或永续数据源。
// 现货/永续是两套行情两套价格，任何调用方都不应绕过路由直接混用。
type Router struct {
	Spot MarketDataSource
	Perp MarketDataSource
}

func NewRouter(spot, perp MarketDataSource) *Router {
	return &Router{Spot: spot, Perp: perp}
}

func (r *Router) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]store.Kline, error) {
	if IsPerp(symbol) {
		return r.Perp.GetKlines(ctx, symbol, interval, limit)
	}
	return r.Spot.GetKlines(ctx, symbol, interval, limit)
}

func (r *Router) SubscribeTicker(symbols []string, onTick func(Tick)) (func(), error) {
	var spot, perp []string
	for _, s := range symbols {
		if IsPerp(s) {
			perp = append(perp, s)
		} else {
			spot = append(spot, s)
		}
	}
	var stops []func()
	if len(spot) > 0 {
		stop, err := r.Spot.SubscribeTicker(spot, onTick)
		if err != nil {
			return nil, err
		}
		stops = append(stops, stop)
	}
	if len(perp) > 0 {
		stop, err := r.Perp.SubscribeTicker(perp, onTick)
		if err != nil {
			for _, s := range stops {
				s()
			}
			return nil, err
		}
		stops = append(stops, stop)
	}
	return func() {
		for _, s := range stops {
			s()
		}
	}, nil
}
