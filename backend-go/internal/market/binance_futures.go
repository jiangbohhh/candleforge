package market

import (
	"context"
	"log"
	"time"

	"github.com/adshao/go-binance/v2/futures"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// BinanceFuturesSource 是 USDT-M 永续（fapi）的公共行情数据源（免 API key）。
// 只接受带 .PERP 后缀的统一 symbol。
type BinanceFuturesSource struct {
	client *futures.Client
}

func NewBinanceFuturesSource() *BinanceFuturesSource {
	return &BinanceFuturesSource{client: futures.NewClient("", "")}
}

// GetKlines 取永续历史 K 线。
func (b *BinanceFuturesSource) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]store.Kline, error) {
	native, err := ToNative(symbol)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	raw, err := b.client.NewKlinesService().
		Symbol(native).Interval(interval).Limit(limit).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.Kline, 0, len(raw))
	for _, k := range raw {
		out = append(out, store.Kline{
			Symbol:    symbol,
			Interval:  interval,
			OpenTime:  k.OpenTime,
			Open:      parseFloat(k.Open),
			High:      parseFloat(k.High),
			Low:       parseFloat(k.Low),
			Close:     parseFloat(k.Close),
			Volume:    parseFloat(k.Volume),
			CloseTime: k.CloseTime,
			TradeNum:  k.TradeNum,
		})
	}
	return out, nil
}

// GetFundingRates 分页拉取 [startTime, now] 的历史资金费率（fapi 单页上限 1000）。
// startTime=0 表示从交易所最早记录开始。
func (b *BinanceFuturesSource) GetFundingRates(ctx context.Context, symbol string, startTime int64) ([]store.FundingRate, error) {
	native, err := ToNative(symbol)
	if err != nil {
		return nil, err
	}
	var out []store.FundingRate
	cursor := startTime
	for {
		svc := b.client.NewFundingRateService().Symbol(native).Limit(1000)
		if cursor > 0 {
			svc = svc.StartTime(cursor)
		}
		page, err := svc.Do(ctx)
		if err != nil {
			return out, err
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			out = append(out, store.FundingRate{
				Symbol:      symbol,
				FundingTime: r.FundingTime,
				Rate:        parseFloat(r.FundingRate),
				MarkPrice:   parseFloat(r.MarkPrice),
			})
		}
		if len(page) < 1000 {
			break // 最后一页
		}
		cursor = page[len(page)-1].FundingTime + 1
	}
	return out, nil
}

// SubscribeTicker 用 fapi mini-ticker 流订阅永续实时行情，断线自动重连。
func (b *BinanceFuturesSource) SubscribeTicker(symbols []string, onTick func(Tick)) (func(), error) {
	stops := make([]chan struct{}, 0, len(symbols))

	for _, sym := range symbols {
		native, err := ToNative(sym)
		if err != nil {
			log.Printf("skip bad perp symbol %s: %v", sym, err)
			continue
		}
		unified := sym
		stop := make(chan struct{})
		stops = append(stops, stop)

		go b.runMiniTicker(native, unified, onTick, stop)
	}

	return func() {
		for _, s := range stops {
			close(s)
		}
	}, nil
}

func (b *BinanceFuturesSource) runMiniTicker(native, unified string, onTick func(Tick), stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}

		handler := func(e *futures.WsMiniMarketTickerEvent) {
			onTick(Tick{
				Symbol: unified,
				Price:  parseFloat(e.ClosePrice),
				Open:   parseFloat(e.OpenPrice),
				High:   parseFloat(e.HighPrice),
				Low:    parseFloat(e.LowPrice),
				Volume: parseFloat(e.Volume),
				Time:   e.Time,
			})
		}
		errHandler := func(err error) {
			log.Printf("fapi ws %s error: %v", native, err)
		}

		doneC, stopC, err := futures.WsMiniMarketTickerServe(native, handler, errHandler)
		if err != nil {
			log.Printf("fapi ws %s dial failed: %v; retry in 3s", native, err)
			if waitOrStop(stop, 3*time.Second) {
				return
			}
			continue
		}

		select {
		case <-stop:
			close(stopC)
			return
		case <-doneC:
			log.Printf("fapi ws %s disconnected; reconnecting", native)
			if waitOrStop(stop, 2*time.Second) {
				return
			}
		}
	}
}
