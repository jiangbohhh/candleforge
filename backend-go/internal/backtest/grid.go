// grid.go — 网格策略 Go-native 回测入口（现货 + USDT-M 永续三方向）。
// 使用与实盘相同的 grid.Engine 状态机，通过 SimExecutor 驱动，
// 输出 *pb.BacktestResponse，走同一 protoMarshaler 序列化（与 Python 引擎口径对齐）。
package backtest

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/strategy/grid"
	"github.com/jiangbohhh/candleforge/backend-go/pb"
)

// maintenanceMarginRate 是永续强平判定的维持保证金率（近似 Binance USDT-M 第一档）。
const maintenanceMarginRate = 0.005

// RunGridBacktest 运行网格回测，返回 *pb.BacktestResponse。
// funding 是回测窗口内的资金费率历史（现货传 nil）；永续按期结算进现金，
// 并在每根 bar 后做强平判定——触发强平即终止回测并在 metrics 里标记。
func RunGridBacktest(klines []store.Kline, funding []store.FundingRate, params json.RawMessage, initialCash, commission float64) (*pb.BacktestResponse, error) {
	if len(klines) == 0 {
		return nil, fmt.Errorf("no klines provided")
	}

	p, err := grid.ParseParams(params)
	if err != nil {
		return nil, fmt.Errorf("params: %w", err)
	}

	eng, err := grid.New(p)
	if err != nil {
		return nil, err
	}

	lr := NewLevelRef()

	// SimExecutor 的 onFill 回调：更新引擎状态并把补单入簿
	var ex *SimExecutor
	ex = NewSimExecutor(initialCash, commission, func(ref, side string, price, qty float64) {
		level, ok := lr.GetLevel(ref)
		if !ok {
			return
		}
		next := eng.OnFill(level, side)
		if next.Level < 0 || next.Qty <= 0 {
			return
		}
		// 把补单挂入撮合器
		newRef, err := ex.PlaceLimit(context.Background(), next.Side, next.Price, next.Qty)
		if err == nil {
			lr.Set(newRef, next.Level)
		}
	})

	// 首 bar open 作为当前价做 PlanInit
	firstBar := klines[0]
	ex.curOpen = firstBar.Open
	ex.curTime = firstBar.OpenTime

	initQty, orders := eng.PlanInit(firstBar.Open)

	// 初始建仓（long 市价买 / short 市价卖开空，按首 bar open 价）
	if initQty > 0 && eng.InitSideFor() != "" {
		if _, err := ex.PlaceMarket(context.Background(), eng.InitSideFor(), initQty); err != nil {
			return nil, fmt.Errorf("initial market %s: %w", eng.InitSideFor(), err)
		}
	}

	// 铺初始挂单
	for _, o := range SortedLevelOrders(orders) {
		ref, err := ex.PlaceLimit(context.Background(), o.Side, o.Price, o.Qty)
		if err != nil {
			return nil, err
		}
		lr.Set(ref, o.Level)
	}

	// 逐 bar 撮合；永续先结算到期资金费，bar 后做强平判定
	isFutures := p.IsFutures()
	fi := 0
	equityCurve := []EquityPoint{{Time: firstBar.OpenTime, Value: initialCash}}
	for _, k := range klines {
		if isFutures {
			for fi < len(funding) && funding[fi].FundingTime <= k.OpenTime {
				refPrice := funding[fi].MarkPrice
				if refPrice <= 0 {
					refPrice = k.Open
				}
				ex.ApplyFunding(funding[fi].Rate, refPrice)
				fi++
			}
		}

		ex.ProcessBar(k.OpenTime, k.Open, k.High, k.Low, k.Close)

		if isFutures && ex.CheckLiquidation(maintenanceMarginRate) {
			equityCurve = append(equityCurve, EquityPoint{Time: k.OpenTime, Value: ex.Cash()})
			break
		}

		equity := ex.Cash() + ex.Inventory()*k.Close
		equityCurve = append(equityCurve, EquityPoint{Time: k.OpenTime, Value: equity})
	}

	// 指标计算
	matched := eng.State.MatchedCount
	metricsMap := Metrics(equityCurve, initialCash, matched, matched) // 每个闭合回合价差为正（全部胜）

	// 组装 *pb.BacktestResponse
	pbMetrics := &pb.BacktestMetrics{
		TotalReturn:  metricsMap["totalReturn"],
		AnnualReturn: metricsMap["annualReturn"],
		MaxDrawdown:  metricsMap["maxDrawdown"],
		Sharpe:       metricsMap["sharpe"],
		TradeCount:   int64(matched),
		WinRate:      metricsMap["winRate"],
		FundingCost:  ex.FundingPaid(),
		Liquidated:   ex.Liquidated(),
	}

	pbEquity := make([]*pb.EquityPoint, len(equityCurve))
	for i, ep := range equityCurve {
		pbEquity[i] = &pb.EquityPoint{Time: ep.Time, Value: ep.Value}
	}

	pbTrades := make([]*pb.Trade, 0, len(ex.Fills()))
	for _, f := range ex.Fills() {
		pbTrades = append(pbTrades, &pb.Trade{
			Time:     f.OpenTime,
			Side:     f.Side,
			Price:    f.Price,
			Quantity: f.Qty,
		})
	}

	return &pb.BacktestResponse{
		Metrics:     pbMetrics,
		EquityCurve: pbEquity,
		Trades:      pbTrades,
	}, nil
}
