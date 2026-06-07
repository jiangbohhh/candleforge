package market

import (
	"context"
	"log"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// PresetSymbols 是 M1 预置跟踪的主流币（USDT 现货对）。
var PresetSymbols = []store.Symbol{
	{Symbol: "CRYPTO.BTC-USDT", Market: "CRYPTO", BaseAsset: "BTC", QuoteAsset: "USDT", NativeSymbol: "BTCUSDT", Name: "Bitcoin", Status: "TRADING"},
	{Symbol: "CRYPTO.ETH-USDT", Market: "CRYPTO", BaseAsset: "ETH", QuoteAsset: "USDT", NativeSymbol: "ETHUSDT", Name: "Ethereum", Status: "TRADING"},
	{Symbol: "CRYPTO.BNB-USDT", Market: "CRYPTO", BaseAsset: "BNB", QuoteAsset: "USDT", NativeSymbol: "BNBUSDT", Name: "BNB", Status: "TRADING"},
	{Symbol: "CRYPTO.SOL-USDT", Market: "CRYPTO", BaseAsset: "SOL", QuoteAsset: "USDT", NativeSymbol: "SOLUSDT", Name: "Solana", Status: "TRADING"},
	{Symbol: "CRYPTO.XRP-USDT", Market: "CRYPTO", BaseAsset: "XRP", QuoteAsset: "USDT", NativeSymbol: "XRPUSDT", Name: "XRP", Status: "TRADING"},
	{Symbol: "CRYPTO.DOGE-USDT", Market: "CRYPTO", BaseAsset: "DOGE", QuoteAsset: "USDT", NativeSymbol: "DOGEUSDT", Name: "Dogecoin", Status: "TRADING"},
}

// SupportedIntervals 是支持的 K 线周期。
var SupportedIntervals = []string{"1m", "5m", "15m", "1h", "4h", "1d"}

// SeedAndBackfill 写入预置标的，并对空表的 symbol×interval 回填历史 K 线。
func SeedAndBackfill(ctx context.Context, st *store.Store, src MarketDataSource) error {
	// 1) 预置标的入库
	for _, sym := range PresetSymbols {
		if err := st.UpsertSymbol(ctx, sym); err != nil {
			return err
		}
	}
	// 默认把预置标的加入自选（首次体验更友好）
	for _, sym := range PresetSymbols {
		_ = st.AddWatch(ctx, sym.Symbol)
	}

	// 2) 回填历史 K 线（仅空的组合）
	for _, sym := range PresetSymbols {
		for _, itv := range SupportedIntervals {
			n, err := st.CountKlines(ctx, sym.Symbol, itv)
			if err != nil {
				return err
			}
			if n > 0 {
				continue // 已有数据，跳过
			}
			ks, err := src.GetKlines(ctx, sym.Symbol, itv, 1000)
			if err != nil {
				log.Printf("backfill %s %s failed: %v", sym.Symbol, itv, err)
				continue
			}
			if err := st.UpsertKlines(ctx, ks); err != nil {
				return err
			}
			log.Printf("backfilled %s %s: %d klines", sym.Symbol, itv, len(ks))
		}
	}
	return nil
}

// Backfill 强制对所有预置标的×周期重新拉取（手动触发用）。
func Backfill(ctx context.Context, st *store.Store, src MarketDataSource) error {
	for _, sym := range PresetSymbols {
		for _, itv := range SupportedIntervals {
			ks, err := src.GetKlines(ctx, sym.Symbol, itv, 1000)
			if err != nil {
				log.Printf("backfill %s %s failed: %v", sym.Symbol, itv, err)
				continue
			}
			if err := st.UpsertKlines(ctx, ks); err != nil {
				return err
			}
			log.Printf("backfilled %s %s: %d klines", sym.Symbol, itv, len(ks))
		}
	}
	return nil
}

// WatchedNativeSymbols 返回预置标的的统一 symbol 列表（供实时订阅）。
func WatchedSymbols() []string {
	out := make([]string, 0, len(PresetSymbols))
	for _, s := range PresetSymbols {
		out = append(out, s.Symbol)
	}
	return out
}
