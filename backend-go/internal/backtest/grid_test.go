package backtest

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// syntheticKlines 生成 N 根等差振荡 K 线（用于手算验证）。
// 每根 bar: open=mid, high=mid+amp, low=mid-amp, close=mid。
func syntheticKlines(n int, midPrice, amplitude float64) []store.Kline {
	ks := make([]store.Kline, n)
	for i := range ks {
		ks[i] = store.Kline{
			OpenTime: int64(i) * 3600_000, // 每 1h
			Open:     midPrice,
			High:     midPrice + amplitude,
			Low:      midPrice - amplitude,
			Close:    midPrice,
			Volume:   1000,
		}
	}
	return ks
}

// TestGridBacktestBasic 跑一个简单网格：100bar 振荡在中间；
// 期望 matchedCount > 0，equity 变化，trades 非空。
func TestGridBacktestBasic(t *testing.T) {
	// 网格：下界 90，上界 110，10格，每格 1 个，等差
	params, _ := json.Marshal(map[string]any{
		"gridType":   "arithmetic",
		"lowerPrice": 90.0,
		"upperPrice": 110.0,
		"gridCount":  10,
		"qtyPerGrid": 1.0,
	})

	// 振荡幅度 3，市价 100；会反复穿越网格线
	klines := syntheticKlines(100, 100, 3)
	resp, err := RunGridBacktest(klines, nil, params, 10000, 0.001)
	if err != nil {
		t.Fatalf("RunGridBacktest: %v", err)
	}

	m := resp.GetMetrics()
	if m == nil {
		t.Fatal("nil metrics")
	}
	if m.TradeCount == 0 {
		t.Error("expected > 0 matched trades")
	}
	if len(resp.GetEquityCurve()) == 0 {
		t.Error("expected equity curve")
	}
	if len(resp.GetTrades()) == 0 {
		t.Error("expected trades in output")
	}
	t.Logf("tradeCount=%d totalReturn=%.4f maxDrawdown=%.4f sharpe=%.4f",
		m.TradeCount, m.TotalReturn, m.MaxDrawdown, m.Sharpe)
}

// TestGridBacktestNoFills 市价远低于网格范围 → 零成交，初始建仓=0。
func TestGridBacktestNoFills(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"gridType":   "arithmetic",
		"lowerPrice": 200.0,
		"upperPrice": 300.0,
		"gridCount":  5,
		"qtyPerGrid": 0.01,
	})
	// 市价恒 50，远低于网格下界 200
	klines := syntheticKlines(50, 50, 1)
	resp, err := RunGridBacktest(klines, nil, params, 10000, 0)
	if err != nil {
		t.Fatalf("RunGridBacktest: %v", err)
	}
	// 无成交（市价不触网格）
	if resp.GetMetrics().TradeCount != 0 {
		t.Errorf("expected 0 trades, got %d", resp.GetMetrics().TradeCount)
	}
}

// TestGridBacktestMetricsDirection totalReturn 在振荡市场下应 >= 0（网格盈利）。
func TestGridBacktestMetricsDirection(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"gridType":   "arithmetic",
		"lowerPrice": 95.0,
		"upperPrice": 105.0,
		"gridCount":  5,
		"qtyPerGrid": 0.1,
	})
	// 长时间振荡，手续费为 0
	klines := syntheticKlines(200, 100, 4)
	resp, err := RunGridBacktest(klines, nil, params, 5000, 0)
	if err != nil {
		t.Fatalf("RunGridBacktest: %v", err)
	}
	m := resp.GetMetrics()
	if m.TotalReturn < 0 {
		t.Errorf("expected non-negative totalReturn in oscillating market, got %.4f", m.TotalReturn)
	}
	// maxDrawdown 在 [0,1]
	if m.MaxDrawdown < 0 || m.MaxDrawdown > 1 {
		t.Errorf("maxDrawdown=%.4f out of [0,1]", m.MaxDrawdown)
	}
	// winRate 在 [0,1]
	if m.WinRate < 0 || m.WinRate > 1 {
		t.Errorf("winRate=%.4f out of [0,1]", m.WinRate)
	}
}

// TestGridBacktestGeometric 几何网格不报错且有成交。
func TestGridBacktestGeometric(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"gridType":   "geometric",
		"lowerPrice": 90.0,
		"upperPrice": 110.0,
		"gridCount":  10,
		"qtyPerGrid": 1.0,
	})
	klines := syntheticKlines(100, 100, 3)
	resp, err := RunGridBacktest(klines, nil, params, 10000, 0.001)
	if err != nil {
		t.Fatalf("RunGridBacktest geometric: %v", err)
	}
	if resp.GetMetrics() == nil {
		t.Fatal("nil metrics")
	}
}

// TestGridBacktestAnnualReturnFinite annualReturn 应为有限数。
func TestGridBacktestAnnualReturnFinite(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"gridType":   "arithmetic",
		"lowerPrice": 90.0,
		"upperPrice": 110.0,
		"gridCount":  5,
		"qtyPerGrid": 0.5,
	})
	klines := syntheticKlines(500, 100, 5)
	resp, err := RunGridBacktest(klines, nil, params, 10000, 0)
	if err != nil {
		t.Fatalf("RunGridBacktest: %v", err)
	}
	ar := resp.GetMetrics().GetAnnualReturn()
	if math.IsNaN(ar) || math.IsInf(ar, 0) {
		t.Errorf("annualReturn is not finite: %v", ar)
	}
}

// ── M6.5 永续 ──

// 做空网格：振荡市场、零手续费下应有正收益，且回合数 > 0。
func TestGridBacktestShortDirection(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"marketType": "futures",
		"direction":  "short",
		"gridType":   "arithmetic",
		"lowerPrice": 90.0,
		"upperPrice": 110.0,
		"gridCount":  10,
		"qtyPerGrid": 1.0,
	})
	klines := syntheticKlines(100, 100, 3)
	resp, err := RunGridBacktest(klines, nil, params, 10000, 0)
	if err != nil {
		t.Fatalf("short backtest: %v", err)
	}
	m := resp.GetMetrics()
	if m.TradeCount == 0 {
		t.Error("expected matched rounds in short grid")
	}
	if m.TotalReturn < 0 {
		t.Errorf("short grid in oscillation should be non-negative, got %.4f", m.TotalReturn)
	}
	if m.Liquidated {
		t.Error("should not liquidate at 1x in-range oscillation")
	}
}

// 中性网格：振荡市场应有回合、无强平。
func TestGridBacktestNeutral(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"marketType": "futures",
		"direction":  "neutral",
		"gridType":   "arithmetic",
		"lowerPrice": 90.0,
		"upperPrice": 110.0,
		"gridCount":  10,
		"qtyPerGrid": 1.0,
	})
	klines := syntheticKlines(100, 100, 3)
	resp, err := RunGridBacktest(klines, nil, params, 10000, 0)
	if err != nil {
		t.Fatalf("neutral backtest: %v", err)
	}
	m := resp.GetMetrics()
	if m.TradeCount == 0 {
		t.Error("expected matched rounds in neutral grid")
	}
	if m.Liquidated {
		t.Error("should not liquidate")
	}
}

// 资金费回放：多头持仓付正费率，空头持仓收正费率。
func TestGridBacktestFundingCost(t *testing.T) {
	// 平坦行情 95，网格 100-120 → long 满仓建仓后无任何成交，纯持仓吃 funding
	longParams, _ := json.Marshal(map[string]any{
		"marketType": "futures", "direction": "long",
		"gridType": "arithmetic", "lowerPrice": 100.0, "upperPrice": 120.0,
		"gridCount": 5, "qtyPerGrid": 1.0,
	})
	klines := syntheticKlines(24, 95, 0.5) // 24h 平坦
	funding := []store.FundingRate{
		{FundingTime: 0, Rate: 0.0001},
		{FundingTime: 8 * 3600_000, Rate: 0.0001},
		{FundingTime: 16 * 3600_000, Rate: 0.0001},
	}
	resp, err := RunGridBacktest(klines, funding, longParams, 10000, 0)
	if err != nil {
		t.Fatalf("long funding backtest: %v", err)
	}
	if fc := resp.GetMetrics().GetFundingCost(); fc <= 0 {
		t.Errorf("long position should PAY positive funding, got %.6f", fc)
	}

	// 平坦行情 125，网格 100-120 → short 满仓开空后无成交，收 funding
	shortParams, _ := json.Marshal(map[string]any{
		"marketType": "futures", "direction": "short",
		"gridType": "arithmetic", "lowerPrice": 100.0, "upperPrice": 120.0,
		"gridCount": 5, "qtyPerGrid": 1.0,
	})
	klines2 := syntheticKlines(24, 125, 0.5)
	resp2, err := RunGridBacktest(klines2, funding, shortParams, 10000, 0)
	if err != nil {
		t.Fatalf("short funding backtest: %v", err)
	}
	if fc := resp2.GetMetrics().GetFundingCost(); fc >= 0 {
		t.Errorf("short position should RECEIVE positive funding, got %.6f", fc)
	}
}

// 强平：保证金远小于名义敞口的做空网格，在价格突破上界后应触发强平并终止回测。
func TestGridBacktestLiquidation(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"marketType": "futures", "direction": "short",
		"gridType": "arithmetic", "lowerPrice": 90.0, "upperPrice": 110.0,
		"gridCount": 5, "qtyPerGrid": 10.0, // 满仓名义 ~5500，初始资金仅 500
	})
	klines := []store.Kline{
		{OpenTime: 0, Open: 95, High: 95, Low: 95, Close: 95},
		{OpenTime: 3600_000, Open: 95, High: 111, Low: 95, Close: 111},   // 扫穿全部开空线
		{OpenTime: 7200_000, Open: 111, High: 116, Low: 111, Close: 116}, // 继续上冲 → 权益击穿
		{OpenTime: 10800_000, Open: 116, High: 117, Low: 115, Close: 116},
	}
	resp, err := RunGridBacktest(klines, nil, params, 500, 0)
	if err != nil {
		t.Fatalf("liquidation backtest: %v", err)
	}
	m := resp.GetMetrics()
	if !m.Liquidated {
		t.Fatal("expected liquidation flag")
	}
	// 强平后回测终止：净值曲线不应包含最后一根 bar
	last := resp.GetEquityCurve()[len(resp.GetEquityCurve())-1]
	if last.Time > 7200_000 {
		t.Errorf("equity curve should stop at liquidation bar, last time=%d", last.Time)
	}
	// 强平成交应出现在 trades 中（买回平空）
	foundLiq := false
	for _, tr := range resp.GetTrades() {
		if tr.Side == "buy" && tr.Quantity == 50 {
			foundLiq = true
		}
	}
	if !foundLiq {
		t.Error("expected forced buy-back fill of full 50 short position")
	}
}
