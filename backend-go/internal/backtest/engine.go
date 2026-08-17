// Package backtest 实现 Go-native 回测撮合引擎（网格/MM/套利通用件）。
package backtest

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/jiangbohhh/candleforge/backend-go/internal/strategy/grid"
)

// Fill 记录一次成交。
type Fill struct {
	Ref      string
	Side     string
	Price    float64
	Qty      float64
	OpenTime int64 // bar 的 open_time（ms）
}

// SimExecutor 实现 grid.Executor，在内存中模拟限价单簿。
// 撮合规则：
//  1. 开盘 gap：bar 开始时 buy.price>=open 的买单、sell.price<=open 的卖单按 open 价成交。
//  2. bar 内路径假设：阳线(close>=open)→open→low→high→close；阴线反之。
//  3. 路径内分腿遍历：每笔成交立刻触发 OnFill 回调并把补单入簿（级联）。
//
// commission：手续费率（双边各扣一次，如 0.001 = 0.1%）。
//
// M6.5 永续：inventory 为签名仓位（空为负）。1x 全额保证金下，
// 现金记账与现货完全同构（equity = cash + inventory×price），
// 额外提供 ApplyFunding（资金费结算）与 CheckLiquidation（bar 极值强平判定）。
type SimExecutor struct {
	orders     map[string]*pendingOrder // ref → order
	refSeq     int
	cash       float64
	inventory  float64
	commission float64
	fills      []Fill
	onFill     func(ref string, side string, price, qty float64) // 回调通知引擎

	fundingPaid float64 // 资金费净支出（正=付出，负=收入）
	liquidated  bool

	// 当前 bar 信息（匹配时用）
	curOpen  float64
	curLow   float64
	curHigh  float64
	curClose float64
	curTime  int64
}

type pendingOrder struct {
	ref   string
	side  string
	price float64
	qty   float64
}

// NewSimExecutor 构造回测撮合器。
// onFill 供网格引擎在成交时插入反向补单（级联是关键）。
func NewSimExecutor(initialCash, commission float64, onFill func(ref, side string, price, qty float64)) *SimExecutor {
	return &SimExecutor{
		orders:     make(map[string]*pendingOrder),
		cash:       initialCash,
		commission: commission,
		onFill:     onFill,
	}
}

// PlaceLimit 挂限价单，返回 ref（内部自增序列号）。
func (e *SimExecutor) PlaceLimit(_ context.Context, side string, price, qty float64) (string, error) {
	e.refSeq++
	ref := fmt.Sprintf("bt-%d", e.refSeq)
	e.orders[ref] = &pendingOrder{ref: ref, side: side, price: price, qty: qty}
	return ref, nil
}

// PlaceMarket 按当前 bar open 价即时成交（初始建仓用）。
func (e *SimExecutor) PlaceMarket(_ context.Context, side string, qty float64) (float64, error) {
	fillPrice := e.curOpen
	if fillPrice <= 0 {
		return 0, fmt.Errorf("no current bar price for market order")
	}
	e.applyFill("mkt", side, fillPrice, qty, e.curTime)
	return fillPrice, nil
}

// Cancel 撤销挂单。
func (e *SimExecutor) Cancel(_ context.Context, ref string) error {
	delete(e.orders, ref)
	return nil
}

// ProcessBar 用一根 K 线撮合所有挂单（级联：新补单在同 bar 内继续匹配）。
func (e *SimExecutor) ProcessBar(openTime int64, open, high, low, close_ float64) {
	e.curOpen, e.curHigh, e.curLow, e.curClose, e.curTime = open, high, low, close_, openTime

	// 步骤①：开盘 gap 成交
	e.matchAtPrice(open, openTime)

	// 步骤②：bar 内路径成交
	if close_ >= open {
		// 阳线：open → low → high → close
		e.matchDownLeg(open, low, openTime)
		e.matchUpLeg(low, high, openTime)
	} else {
		// 阴线：open → high → low → close
		e.matchUpLeg(open, high, openTime)
		e.matchDownLeg(high, low, openTime)
	}
}

// matchAtPrice 撮合所有价格条件满足开仓价的挂单（gap 成交）。
func (e *SimExecutor) matchAtPrice(price float64, t int64) {
	for {
		filled := false
		for ref, o := range e.orders {
			if o.side == "buy" && o.price >= price || o.side == "sell" && o.price <= price {
				delete(e.orders, ref)
				e.applyFill(ref, o.side, price, o.qty, t)
				filled = true
				break
			}
		}
		if !filled {
			break
		}
	}
}

// matchDownLeg 模拟下行段 [from, to]（to < from）。
func (e *SimExecutor) matchDownLeg(from, to float64, t int64) {
	for {
		best := e.bestBuy(to) // 最高的 buy 单且 price >= to
		if best == nil || best.price > from {
			break
		}
		delete(e.orders, best.ref)
		e.applyFill(best.ref, "buy", best.price, best.qty, t)
	}
}

// matchUpLeg 模拟上行段 [from, to]（to > from）。
func (e *SimExecutor) matchUpLeg(from, to float64, t int64) {
	for {
		best := e.bestSell(to) // 最低的 sell 单且 price <= to
		if best == nil || best.price < from {
			break
		}
		delete(e.orders, best.ref)
		e.applyFill(best.ref, "sell", best.price, best.qty, t)
	}
}

// bestBuy 找 price>=minPrice 的最低买单（升序匹配路径中先触及的）。
func (e *SimExecutor) bestBuy(minPrice float64) *pendingOrder {
	var best *pendingOrder
	for _, o := range e.orders {
		if o.side != "buy" || o.price < minPrice {
			continue
		}
		if best == nil || o.price < best.price {
			best = o
		}
	}
	return best
}

// bestSell 找 price<=maxPrice 的最低卖单（升序，先触及低价单）。
func (e *SimExecutor) bestSell(maxPrice float64) *pendingOrder {
	var best *pendingOrder
	for _, o := range e.orders {
		if o.side != "sell" || o.price > maxPrice {
			continue
		}
		if best == nil || o.price < best.price {
			best = o
		}
	}
	return best
}

func (e *SimExecutor) applyFill(ref, side string, price, qty float64, t int64) {
	fee := e.commission * price * qty
	if side == "buy" {
		e.cash -= price*qty + fee
		e.inventory += qty
	} else {
		e.cash += price*qty - fee
		e.inventory -= qty
	}
	f := Fill{Ref: ref, Side: side, Price: price, Qty: qty, OpenTime: t}
	e.fills = append(e.fills, f)
	if e.onFill != nil {
		e.onFill(ref, side, price, qty)
	}
}

// Cash 返回当前现金（供回测结束后计算净值）。
func (e *SimExecutor) Cash() float64 { return e.cash }

// Inventory 返回当前签名仓位数量（空为负）。
func (e *SimExecutor) Inventory() float64 { return e.inventory }

// Fills 返回所有成交记录。
func (e *SimExecutor) Fills() []Fill { return e.fills }

// FundingPaid 返回累计资金费净支出（正=付出）。
func (e *SimExecutor) FundingPaid() float64 { return e.fundingPaid }

// Liquidated 返回是否已触发强平。
func (e *SimExecutor) Liquidated() bool { return e.liquidated }

// ApplyFunding 结算一期资金费：多头付正费率、空头收正费率（负费率反之）。
// refPrice 用结算时标记价（无则用邻近 bar 价）。
func (e *SimExecutor) ApplyFunding(rate, refPrice float64) {
	pay := e.inventory * refPrice * rate
	e.cash -= pay
	e.fundingPaid += pay
}

// CheckLiquidation 用当前 bar 极值做强平判定（近似：以 bar 处理后的仓位对整根 bar 的
// 最不利价估算最低权益）。触发则按极值价强平全部仓位、撤全部挂单，返回 true。
// mmr 是维持保证金率（如 0.005）。仅永续回测调用。
func (e *SimExecutor) CheckLiquidation(mmr float64) bool {
	if e.liquidated || e.inventory == 0 {
		return false
	}
	worst := e.curLow // 多头最不利 = bar 最低
	if e.inventory < 0 {
		worst = e.curHigh // 空头最不利 = bar 最高
	}
	if worst <= 0 {
		return false
	}
	notional := math.Abs(e.inventory) * worst
	equity := e.cash + e.inventory*worst
	if equity > mmr*notional {
		return false
	}
	// 强平：按最不利价平仓（不走 onFill 回调——策略已失去仓位控制权）
	fee := e.commission * notional
	e.cash += e.inventory*worst - fee
	side := "sell"
	if e.inventory < 0 {
		side = "buy"
	}
	e.fills = append(e.fills, Fill{Ref: "liq", Side: side, Price: worst, Qty: math.Abs(e.inventory), OpenTime: e.curTime})
	e.inventory = 0
	e.orders = make(map[string]*pendingOrder)
	e.liquidated = true
	return true
}

// ── ref→level 映射（供网格回测查找 OnFill 入口）──

// LevelRef 维护 ref → gridLevel 的双向映射。
type LevelRef struct {
	refToLevel map[string]int // ref → level
	levelToRef map[int]string // level → 当前活动 ref
}

func NewLevelRef() *LevelRef {
	return &LevelRef{
		refToLevel: make(map[string]int),
		levelToRef: make(map[int]string),
	}
}

func (lr *LevelRef) Set(ref string, level int) {
	if old, ok := lr.levelToRef[level]; ok {
		delete(lr.refToLevel, old)
	}
	lr.levelToRef[level] = ref
	lr.refToLevel[ref] = level
}

func (lr *LevelRef) GetLevel(ref string) (int, bool) {
	l, ok := lr.refToLevel[ref]
	return l, ok
}

// ── 工具函数 ──

// SortedKeys 返回升序的区间序号列表（给 PlanInit 铺单用）。
func SortedLevelOrders(orders []grid.LevelOrder) []grid.LevelOrder {
	out := append([]grid.LevelOrder{}, orders...)
	sort.Slice(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out
}
