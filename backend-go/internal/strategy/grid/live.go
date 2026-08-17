// live.go — 网格策略实盘 adapter（实现 strategy.Strategy 接口）。
// 通过 intent-log 三步保证挂单崩溃安全，通过 broker.Broker 与交易所交互。
package grid

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/jiangbohhh/candleforge/backend-go/internal/broker"
	"github.com/jiangbohhh/candleforge/backend-go/internal/risk"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// LiveGrid 是网格策略的实盘实现（实现 strategy.Strategy）。
type LiveGrid struct {
	row     store.StrategyRow
	params  *Params
	eng     *Engine
	br      broker.Broker
	risk    *risk.Engine
	st      *store.Store
	priceFn func(string) float64
	emitFn  func(event string, data any)
}

// NewLiveGrid 从 DB 行构建 LiveGrid 实例（恢复或全新）。
func NewLiveGrid(
	ctx context.Context,
	row store.StrategyRow,
	br broker.Broker,
	riskEngine *risk.Engine,
	st *store.Store,
	priceFn func(string) float64,
	emitFn func(string, any),
) (*LiveGrid, error) {
	p, err := ParseParams(row.Params)
	if err != nil {
		return nil, err
	}
	eng, err := New(p)
	if err != nil {
		return nil, err
	}
	// 从 state 快照恢复内存状态（若有）
	if len(row.State) > 2 { // "{}" 算空
		var s State
		if err := json.Unmarshal(row.State, &s); err == nil && len(s.Sides) == p.GridCount {
			_ = eng.Restore(s.Sides, s.BaseSides, s.Inventory, s.RealizedPnl, s.MatchedCount)
		}
	}
	return &LiveGrid{
		row:     row,
		params:  p,
		eng:     eng,
		br:      br,
		risk:    riskEngine,
		st:      st,
		priceFn: priceFn,
		emitFn:  emitFn,
	}, nil
}

// Start 初始化建仓并铺单（全新策略调用；恢复场景跳过 Start 直接 Reconcile）。
func (g *LiveGrid) Start(ctx context.Context) error {
	p := g.priceFn(g.row.Symbol)
	if p <= 0 {
		return fmt.Errorf("no market price for %s; cannot start grid", g.row.Symbol)
	}

	initQty, orders := g.eng.PlanInit(p)

	// 资金预检（1x 下最坏情况占用 = 初始建仓名义 + 全部"开仓方向"挂单名义；
	// SimBroker 挂单不冻结资金，需主动检查）
	acct, err := g.st.GetAccount(ctx, g.row.AccountID)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}
	openSides := map[string]bool{"buy": true} // long: buy 开仓
	switch g.params.Dir() {
	case "short":
		openSides = map[string]bool{"sell": true}
	case "neutral":
		openSides = map[string]bool{"buy": true, "sell": true}
	}
	needed := initQty * p
	for _, o := range orders {
		if openSides[o.Side] {
			needed += o.Price * o.Qty
		}
	}
	if needed > acct.Cash {
		return fmt.Errorf("insufficient funds: need %.2f, have %.2f", needed, acct.Cash)
	}

	// short/neutral 强平安全余量校验（1x 下强平价远高于网格上界才放行）
	if g.params.Dir() != "long" {
		liq := g.params.ShortLiqEstimate(acct.Cash)
		if liq < g.params.UpperPrice*1.15 {
			return fmt.Errorf("liquidation risk: estimated short liq price %.2f is within 15%% of upperPrice %.2f; add margin or shrink grid", liq, g.params.UpperPrice)
		}
	}

	// 初始建仓（long 市价买 / short 市价卖开空；neutral 无）
	if initQty > 0 && g.eng.InitSideFor() != "" {
		side := g.eng.InitSideFor()
		if err := g.risk.Validate(ctx, g.row.AccountID, g.row.Symbol,
			side, "market", 0, initQty, g.priceFn); err != nil {
			return fmt.Errorf("risk: %w", err)
		}
		_, err := g.br.PlaceOrder(ctx, broker.PlaceOrderRequest{
			AccountID:  g.row.AccountID,
			Symbol:     g.row.Symbol,
			Side:       side,
			OrderType:  "market",
			Quantity:   initQty,
			StrategyID: g.row.ID,
		})
		if err != nil {
			return fmt.Errorf("initial market %s: %w", side, err)
		}
	}

	// 铺所有网格限价单（intent-log 三步）
	for _, o := range SortedLevelOrders(orders) {
		if err := g.placeGridOrder(ctx, o.Level, o.Side, o.Price, o.Qty); err != nil {
			log.Printf("grid %d: place level %d %s@%.2f: %v", g.row.ID, o.Level, o.Side, o.Price, err)
		}
	}

	return g.saveSnapshot(ctx)
}

// OnOrderUpdate 处理一笔订单成交（由 runner 串行调用）。
func (g *LiveGrid) OnOrderUpdate(ctx context.Context, o *store.OrderRow) error {
	if o.Status != "filled" {
		return nil
	}

	// 查找该订单对应的 intent
	intents, err := g.st.ListActiveIntents(ctx, g.row.ID)
	if err != nil {
		return err
	}
	var matched *store.StrategyOrderRow
	for i, intent := range intents {
		if intent.OrderID == o.ID {
			matched = &intents[i]
			break
		}
	}
	if matched == nil {
		return nil // 非本策略订单或已处理
	}
	if matched.Processed {
		return nil
	}

	// 更新引擎状态
	next := g.eng.OnFill(matched.GridLevel, matched.Side)

	// 标记 intent 已处理
	if err := g.st.MarkIntentProcessed(ctx, matched.ID); err != nil {
		return err
	}

	// 挂反向补单
	if next.Qty > 0 {
		if err := g.placeGridOrder(ctx, next.Level, next.Side, next.Price, next.Qty); err != nil {
			log.Printf("grid %d:補單 level %d %s@%.2f: %v", g.row.ID, next.Level, next.Side, next.Price, err)
		}
	}

	// 广播状态更新
	snap, _ := g.eng.Snapshot()
	_ = g.st.UpdateStrategyState(ctx, g.row.ID, snap)
	g.emitFn("strategy", map[string]any{
		"id":    g.row.ID,
		"state": json.RawMessage(snap),
	})
	return nil
}

// Reconcile 30s 周期兜底对账（检查停机期间的成交）。
func (g *LiveGrid) Reconcile(ctx context.Context) error {
	unprocessed, err := g.st.ListUnprocessedIntents(ctx, g.row.ID)
	if err != nil {
		return err
	}
	for _, intent := range unprocessed {
		o, err := g.st.GetOrder(ctx, intent.OrderID)
		if err != nil {
			continue
		}
		if err := g.OnOrderUpdate(ctx, o); err != nil {
			log.Printf("grid %d reconcile intent %d: %v", g.row.ID, intent.ID, err)
		}
	}
	return nil
}

// Stop 撤单 + 可选清仓（liquidate=true 清仓）。
func (g *LiveGrid) Stop(ctx context.Context, liquidate bool) error {
	// 撤全部活动挂单
	intents, err := g.st.ListActiveIntents(ctx, g.row.ID)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if intent.OrderID != 0 {
			if err := g.br.CancelOrder(ctx, g.row.AccountID, intent.OrderID); err != nil {
				log.Printf("grid %d: cancel order %d: %v", g.row.ID, intent.OrderID, err)
			}
		}
	}
	if err := g.st.DeactivateAllIntents(ctx, g.row.ID); err != nil {
		return err
	}

	// 可选清仓（签名仓位：多头市价卖，空头市价买回）
	if liquidate && g.eng.Inventory != 0 {
		side := "sell"
		qty := g.eng.Inventory
		if qty < 0 {
			side = "buy"
			qty = -qty
		}
		if err := g.risk.Validate(ctx, g.row.AccountID, g.row.Symbol,
			side, "market", 0, qty, g.priceFn); err == nil {
			_, _ = g.br.PlaceOrder(ctx, broker.PlaceOrderRequest{
				AccountID:  g.row.AccountID,
				Symbol:     g.row.Symbol,
				Side:       side,
				OrderType:  "market",
				Quantity:   qty,
				StrategyID: g.row.ID,
			})
		}
	}
	return nil
}

// ── 内部 helpers ──

// placeGridOrder 执行 intent-log 三步：①写 intent → ②broker.PlaceOrder → ③回写 order_id。
func (g *LiveGrid) placeGridOrder(ctx context.Context, level int, side string, price, qty float64) error {
	// 步骤①
	intentID, err := g.st.InsertIntent(ctx, g.row.ID, level, side, price, "grid")
	if err != nil {
		return fmt.Errorf("insert intent: %w", err)
	}

	// 步骤② — 过风控
	if err := g.risk.Validate(ctx, g.row.AccountID, g.row.Symbol,
		side, "limit", price, qty, g.priceFn); err != nil {
		return fmt.Errorf("risk block: %w", err)
	}
	ord, err := g.br.PlaceOrder(ctx, broker.PlaceOrderRequest{
		AccountID:  g.row.AccountID,
		Symbol:     g.row.Symbol,
		Side:       side,
		OrderType:  "limit",
		Price:      price,
		Quantity:   qty,
		StrategyID: g.row.ID,
	})
	if err != nil {
		return fmt.Errorf("place order: %w", err)
	}

	// 步骤③
	if err := g.st.AttachOrderID(ctx, intentID, ord.ID); err != nil {
		return fmt.Errorf("attach order id: %w", err)
	}
	return nil
}

func (g *LiveGrid) saveSnapshot(ctx context.Context) error {
	snap, err := g.eng.Snapshot()
	if err != nil {
		return err
	}
	return g.st.UpdateStrategyState(ctx, g.row.ID, snap)
}

// SortedLevelOrders re-exports from engine.go (same package, no import needed).
func SortedLevelOrders(orders []LevelOrder) []LevelOrder {
	out := append([]LevelOrder{}, orders...)
	// Sort by level ascending
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Level < out[i].Level {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
