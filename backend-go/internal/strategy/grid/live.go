// live.go — 网格策略实盘 adapter（实现 strategy.Strategy 接口）。
// 通过 intent-log 三步保证挂单崩溃安全，通过 broker.Broker 与交易所交互。
package grid

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"

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
	priceFn  func(string) float64
	emitFn   func(event string, data any)
	stopping atomic.Bool
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

	// 铺所有网格限价单（intent-log 三步）。
	// H11: 任一层失败即补偿性回滚整批——撤全部已下单 + 清掉初始建仓，返回错误。
	// 否则旧单残留交易所、其后续成交不被跟踪（真金白银无人认领）。
	for _, o := range SortedLevelOrders(orders) {
		if err := g.placeGridOrder(ctx, o.Level, o.Side, o.Price, o.Qty); err != nil {
			log.Printf("grid %d: place level %d %s@%.2f failed, rolling back batch: %v", g.row.ID, o.Level, o.Side, o.Price, err)
			if serr := g.Stop(ctx, true); serr != nil {
				log.Printf("grid %d: compensating stop after start failure: %v", g.row.ID, serr)
			}
			return fmt.Errorf("place level %d %s@%.2f: %w", o.Level, o.Side, o.Price, err)
		}
	}

	return g.saveSnapshot(ctx)
}

// OnOrderUpdate 处理一笔订单状态更新（由 runner 串行调用）。
// 部分成交只推进 consumed_qty；完全成交才 OnFill 翻层；部成后撤单重挂剩余量。
func (g *LiveGrid) OnOrderUpdate(ctx context.Context, o *store.OrderRow) error {
	intent, err := g.st.GetIntentByOrderID(ctx, g.row.ID, o.ID)
	if err != nil {
		return err
	}
	if intent == nil {
		return nil
	}

	d := decideGridFill(o.Status, o.Quantity, o.FilledQty, intent.ConsumedQty, g.params.QtyPerGrid)
	if d.trackOnly {
		return g.st.TouchIntentConsumed(ctx, intent.ID, d.newConsumed)
	}
	if !d.markProcessed {
		return nil
	}
	if intent.Processed {
		return nil
	}

	if d.applyEngine {
		return g.commitFill(ctx, intent, d.newConsumed)
	}

	// 撤单/拒单：认领 intent，必要时空档重挂（Stop 期间禁止重挂以免和清仓打架）。
	claimed, err := g.st.ClaimIntentAndSaveState(ctx, intent.ID, g.row.ID, d.newConsumed, nil)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if d.replaceQty > 0 && !g.stopping.Load() {
		if err := g.placeGridOrder(ctx, intent.GridLevel, intent.Side, intent.Price, d.replaceQty); err != nil {
			log.Printf("grid %d: re-place remaining level %d %s qty=%.8f: %v",
				g.row.ID, intent.GridLevel, intent.Side, d.replaceQty, err)
		}
	}
	return nil
}

// commitFill 在引擎副本上 OnFill，与 intent 认领、状态快照同一事务落库后再补反向单。
func (g *LiveGrid) commitFill(ctx context.Context, intent *store.StrategyOrderRow, consumed float64) error {
	clone, err := cloneEngine(g.eng)
	if err != nil {
		return err
	}
	next := clone.OnFill(intent.GridLevel, intent.Side)
	newSnap, err := clone.Snapshot()
	if err != nil {
		return err
	}
	claimed, err := g.st.ClaimIntentAndSaveState(ctx, intent.ID, g.row.ID, consumed, newSnap)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	g.eng = clone

	if next.Qty > 0 && !g.stopping.Load() {
		if err := g.placeGridOrder(ctx, next.Level, next.Side, next.Price, next.Qty); err != nil {
			log.Printf("grid %d:補單 level %d %s@%.2f: %v", g.row.ID, next.Level, next.Side, next.Price, err)
		}
	}

	g.emitFn("strategy", map[string]any{
		"id":    g.row.ID,
		"state": json.RawMessage(newSnap),
	})
	return nil
}

// cloneEngine 返回引擎的深拷贝（sides 等切片独立分配），用于在副本上试探 OnFill
// 后再原子落库（H8）。副本与原引擎共享只读的 cfg/levels，但 sides 与 State 各自独立。
func cloneEngine(src *Engine) (*Engine, error) {
	snap, err := src.Snapshot()
	if err != nil {
		return nil, err
	}
	clone, err := New(src.cfg)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(snap, &s); err != nil {
		return nil, err
	}
	clone.State = s
	clone.sides = append([]string{}, s.Sides...)
	return clone, nil
}

// Reconcile 30s 周期兜底对账（交易所挂单 vs 本地 → 消化未处理 intent → 补挂空洞）。
func (g *LiveGrid) Reconcile(ctx context.Context) error {
	if err := g.br.ReconcileOrders(ctx); err != nil {
		log.Printf("grid %d broker reconcile: %v", g.row.ID, err)
	}

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

	// C1：补挂「active 但未落单」的悬空 intent（下单/风控失败留下的网格空洞）。
	dangling, err := g.st.ListDanglingIntents(ctx, g.row.ID)
	if err != nil {
		return err
	}
	for _, intent := range dangling {
		qty := intent.Qty
		if qty <= 0 {
			qty = g.params.QtyPerGrid
		}
		if err := g.retryDanglingIntent(ctx, intent, qty); err != nil {
			log.Printf("grid %d reconcile re-place level %d %s@%.8f: %v",
				g.row.ID, intent.GridLevel, intent.Side, intent.Price, err)
		}
	}
	return nil
}

// retryDanglingIntent 对「已有 intent、尚未落单」的空洞直接重试 PlaceOrder，
// 不再 InsertIntent（否则会先失活原 intent，失败后该层永久无单）。
func (g *LiveGrid) retryDanglingIntent(ctx context.Context, intent store.StrategyOrderRow, qty float64) error {
	if err := g.risk.Validate(ctx, g.row.AccountID, g.row.Symbol,
		intent.Side, "limit", intent.Price, qty, g.priceFn); err != nil {
		return fmt.Errorf("risk block: %w", err)
	}
	ord, err := g.br.PlaceOrder(ctx, broker.PlaceOrderRequest{
		AccountID:  g.row.AccountID,
		Symbol:     g.row.Symbol,
		Side:       intent.Side,
		OrderType:  "limit",
		Price:      intent.Price,
		Quantity:   qty,
		StrategyID: g.row.ID,
	})
	if err != nil {
		return fmt.Errorf("place order: %w", err)
	}
	return g.st.AttachOrderID(ctx, intent.ID, ord.ID)
}

// Stop 撤单 + 可选清仓（liquidate=true 清仓）。
func (g *LiveGrid) Stop(ctx context.Context, liquidate bool) error {
	g.stopping.Store(true)
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
// C1：任一步（②③）失败即失活该 intent，避免「active 但 order_id=0」的悬空 intent
// 被部分唯一索引长期挡住该层重试。
func (g *LiveGrid) placeGridOrder(ctx context.Context, level int, side string, price, qty float64) error {
	// 步骤①
	intentID, err := g.st.InsertIntent(ctx, g.row.ID, level, side, price, qty, "grid")
	if err != nil {
		return fmt.Errorf("insert intent: %w", err)
	}
	fail := func(e error) error {
		_ = g.st.DeactivateIntent(ctx, intentID)
		return e
	}

	// 步骤② — 过风控
	if err := g.risk.Validate(ctx, g.row.AccountID, g.row.Symbol,
		side, "limit", price, qty, g.priceFn); err != nil {
		return fail(fmt.Errorf("risk block: %w", err))
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
		return fail(fmt.Errorf("place order: %w", err))
	}

	// 步骤③
	if err := g.st.AttachOrderID(ctx, intentID, ord.ID); err != nil {
		return fail(fmt.Errorf("attach order id: %w", err))
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
