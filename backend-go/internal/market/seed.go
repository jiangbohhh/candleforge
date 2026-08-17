package market

import (
	"context"
	"log"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// PresetSymbols 是预置跟踪标的：USDT 现货对 + USDT-M 永续（.PERP 后缀）。
var PresetSymbols = []store.Symbol{
	{Symbol: "CRYPTO.BTC-USDT", Market: "CRYPTO", BaseAsset: "BTC", QuoteAsset: "USDT", NativeSymbol: "BTCUSDT", Name: "Bitcoin", Status: "TRADING"},
	{Symbol: "CRYPTO.ETH-USDT", Market: "CRYPTO", BaseAsset: "ETH", QuoteAsset: "USDT", NativeSymbol: "ETHUSDT", Name: "Ethereum", Status: "TRADING"},
	{Symbol: "CRYPTO.BNB-USDT", Market: "CRYPTO", BaseAsset: "BNB", QuoteAsset: "USDT", NativeSymbol: "BNBUSDT", Name: "BNB", Status: "TRADING"},
	{Symbol: "CRYPTO.SOL-USDT", Market: "CRYPTO", BaseAsset: "SOL", QuoteAsset: "USDT", NativeSymbol: "SOLUSDT", Name: "Solana", Status: "TRADING"},
	{Symbol: "CRYPTO.XRP-USDT", Market: "CRYPTO", BaseAsset: "XRP", QuoteAsset: "USDT", NativeSymbol: "XRPUSDT", Name: "XRP", Status: "TRADING"},
	{Symbol: "CRYPTO.DOGE-USDT", Market: "CRYPTO", BaseAsset: "DOGE", QuoteAsset: "USDT", NativeSymbol: "DOGEUSDT", Name: "Dogecoin", Status: "TRADING"},
	{Symbol: "CRYPTO.BTC-USDT.PERP", Market: "CRYPTO", BaseAsset: "BTC", QuoteAsset: "USDT", NativeSymbol: "BTCUSDT", Name: "Bitcoin Perp", Status: "TRADING"},
	{Symbol: "CRYPTO.ETH-USDT.PERP", Market: "CRYPTO", BaseAsset: "ETH", QuoteAsset: "USDT", NativeSymbol: "ETHUSDT", Name: "Ethereum Perp", Status: "TRADING"},
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

// fundingBackfillFrom 是空表首次回填资金费率的起点（约 2 年，~2200 期/标的，3 页请求）。
const fundingBackfillDays = 730

// SyncFundingRates 对全部永续预置标的做资金费率增量同步：
// 空表从 fundingBackfillDays 前开始回填，否则从最新一期之后续拉。
func SyncFundingRates(ctx context.Context, st *store.Store, perp *BinanceFuturesSource, now int64) error {
	for _, sym := range PresetSymbols {
		if !IsPerp(sym.Symbol) {
			continue
		}
		last, err := st.LatestFundingTime(ctx, sym.Symbol)
		if err != nil {
			return err
		}
		start := last + 1
		if last == 0 {
			start = now - int64(fundingBackfillDays)*24*3600*1000
		}
		rs, err := perp.GetFundingRates(ctx, sym.Symbol, start)
		if err != nil {
			log.Printf("funding sync %s failed: %v", sym.Symbol, err)
			continue
		}
		if err := st.UpsertFundingRates(ctx, rs); err != nil {
			return err
		}
		if len(rs) > 0 {
			log.Printf("funding synced %s: %d rates", sym.Symbol, len(rs))
		}
	}
	return nil
}
