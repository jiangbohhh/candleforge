package market

import (
	"context"
	"log"
	"strconv"
	"time"

	binance "github.com/adshao/go-binance/v2"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// BinanceSource 是基于 go-binance 的公共行情数据源（免 API key）。
type BinanceSource struct {
	client *binance.Client
	// native -> unified 映射，供 WS 回调反查
	nativeToUnified map[string]string
}

func NewBinanceSource() *BinanceSource {
	return &BinanceSource{
		client:          binance.NewClient("", ""),
		nativeToUnified: map[string]string{},
	}
}

// GetKlines 取历史 K 线。
func (b *BinanceSource) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]store.Kline, error) {
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

// SubscribeTicker 用 mini-ticker 流订阅给定标的的实时行情。
// 每个 symbol 起一个 WS 连接，含错误重连。
func (b *BinanceSource) SubscribeTicker(symbols []string, onTick func(Tick)) (func(), error) {
	stops := make([]chan struct{}, 0, len(symbols))

	for _, sym := range symbols {
		native, err := ToNative(sym)
		if err != nil {
			log.Printf("skip bad symbol %s: %v", sym, err)
			continue
		}
		b.nativeToUnified[native] = sym

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

// runMiniTicker 维持单个 symbol 的 mini-ticker 订阅，断线自动重连，直到 stop。
func (b *BinanceSource) runMiniTicker(native, unified string, onTick func(Tick), stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}

		handler := func(e *binance.WsMarketStatEvent) {
			onTick(Tick{
				Symbol: unified,
				Price:  parseFloat(e.LastPrice),
				Open:   parseFloat(e.OpenPrice),
				High:   parseFloat(e.HighPrice),
				Low:    parseFloat(e.LowPrice),
				Volume: parseFloat(e.BaseVolume),
				Time:   e.Time,
			})
		}
		errHandler := func(err error) {
			log.Printf("ws %s error: %v", native, err)
		}

		doneC, stopC, err := binance.WsMarketStatServe(native, handler, errHandler)
		if err != nil {
			log.Printf("ws %s dial failed: %v; retry in 3s", native, err)
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
			// 连接断开，稍等后重连
			log.Printf("ws %s disconnected; reconnecting", native)
			if waitOrStop(stop, 2*time.Second) {
				return
			}
		}
	}
}

// waitOrStop 等待 d 或 stop；返回 true 表示收到 stop。
func waitOrStop(stop chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return true
	case <-t.C:
		return false
	}
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
