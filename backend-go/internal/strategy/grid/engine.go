// engine.go — 纯内存网格核心状态机（无 IO，无外部依赖）。
// 实盘和回测均使用此状态机；执行层通过 Executor 接口注入。
//
// M6.5 起支持三方向（long/short/neutral），仓位为签名数量（多正空负）。
// 区间 k = [levels[k], levels[k+1]]，两态翻转：
//   buy@levels[k] 成交 → 仓位 +q → 挂 sell@levels[k+1]
//   sell@levels[k+1] 成交 → 仓位 −q → 挂 buy@levels[k]
// 回合结算：当区间翻回它的基准方向（PlanInit 时的初始方向）时，
// 该区间完成一次完整往返，RealizedPnl += q×价差（现金已实际落袋）。
package grid

import (
	"context"
	"encoding/json"
	"fmt"
)

// Executor 是网格引擎对执行层的唯一依赖（无 IO 的 engine.go 不引用此接口；
// 由实盘 adapter 和回测 adapter 分别实现）。
type Executor interface {
	PlaceLimit(ctx context.Context, side string, price, qty float64) (ref string, err error)
	PlaceMarket(ctx context.Context, side string, qty float64) (avgPrice float64, err error)
	Cancel(ctx context.Context, ref string) error
}

// LevelOrder 描述某区间应挂的一张单。
type LevelOrder struct {
	Level int
	Side  string
	Price float64
	Qty   float64
}

// State 是序列化进 strategies.state 的快照。
type State struct {
	Sides        []string `json:"sides"`               // 每区间当前挂单方向 "buy"/"sell"
	BaseSides    []string `json:"baseSides,omitempty"` // 每区间基准方向（翻回基准=结算一回合）
	Inventory    float64  `json:"inventory"`           // 签名仓位（多为正，空为负；base asset）
	InitQty      float64  `json:"initQty"`             // 初始建仓绝对数量
	InitSide     string   `json:"initSide,omitempty"`  // 初始建仓方向 buy/sell（neutral 为空）
	InitPrice    float64  `json:"initPrice"`           // 初始建仓均价
	RealizedPnl  float64  `json:"realizedPnl"`
	MatchedCount int      `json:"matchedCount"` // 闭合回合数
}

// Engine 是纯内存网格状态机。
type Engine struct {
	cfg    *Params
	levels []float64 // N+1 条网格线
	sides  []string  // N 个区间，sides[k] = 当前应挂的单方向
	State
}

// New 构建 Engine：校验参数、计算网格线；sides/baseSides 由 PlanInit 或 Restore 填充。
func New(cfg *Params) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	lines := cfg.GridLines()
	sides := make([]string, cfg.GridCount)
	for i := range sides {
		sides[i] = "buy"
	}
	e := &Engine{cfg: cfg, levels: lines, sides: sides}
	e.State.Sides = append([]string{}, sides...)
	e.State.BaseSides = append([]string{}, sides...)
	return e, nil
}

// PlanInit 根据当前市价 p 与方向规划初始形态：
//   - long:    市价上方区间需持多仓（sell@上边界休眠于市价上方），其余挂 buy@下边界；
//     initQty = q×上方区间数，方向 buy（市价买入建仓）。
//   - short:   市价下方区间需持空仓（buy@下边界休眠于市价下方），其余挂 sell@上边界；
//     initQty = q×下方区间数，方向 sell（市价卖出开空）。
//   - neutral: 上方挂 sell（开空）、下方挂 buy（开多），零初始仓。
//
// 所有限价单都休眠在市价远端，不会穿价立即成交。
// p 所在区间归入休眠一侧（long/neutral 挂 buy 于下方，short 挂 sell 于上方），
// 等价于币安"跳过最近一线"。
func (e *Engine) PlanInit(p float64) (initQty float64, orders []LevelOrder) {
	n := e.cfg.GridCount
	q := e.cfg.QtyPerGrid
	dir := e.cfg.Dir()

	for k := 0; k < n; k++ {
		aboveMarket := e.levels[k] >= p   // 整个区间在市价上方（含下边界==p）
		belowMarket := e.levels[k+1] <= p // 整个区间在市价下方

		var side string
		switch dir {
		case "short":
			if belowMarket {
				side = "buy" // 已持空，等待 buy@下边界平空
				initQty += q
			} else {
				side = "sell" // 等待 sell@上边界开空
			}
		default: // long / neutral
			if aboveMarket {
				side = "sell" // long: 持多待卖；neutral: 等待开空
				if dir == "long" {
					initQty += q
				}
			} else {
				side = "buy"
			}
		}

		e.sides[k] = side
		if side == "buy" {
			orders = append(orders, LevelOrder{Level: k, Side: "buy", Price: e.levels[k], Qty: q})
		} else {
			orders = append(orders, LevelOrder{Level: k, Side: "sell", Price: e.levels[k+1], Qty: q})
		}
	}

	e.State.Sides = append([]string{}, e.sides...)
	e.State.BaseSides = append([]string{}, e.sides...)
	e.State.InitQty = initQty
	switch dir {
	case "long":
		e.State.InitSide = "buy"
		e.State.Inventory = initQty
	case "short":
		e.State.InitSide = "sell"
		e.State.Inventory = -initQty
	default: // neutral
		e.State.InitSide = ""
		e.State.Inventory = 0
	}
	if initQty == 0 {
		e.State.InitSide = ""
	}
	return initQty, orders
}

// InitSideFor 返回初始建仓的市价单方向（"buy"/"sell"；无需建仓返回 ""）。
func (e *Engine) InitSideFor() string { return e.State.InitSide }

// OnFill 处理某区间某方向成交，更新内部状态，返回应补挂的下一张单。
// OnFill 是纯函数：只改内存，不做 IO；实盘和回测通过 adapter 调用。
func (e *Engine) OnFill(level int, side string) LevelOrder {
	if level < 0 || level >= len(e.sides) {
		return LevelOrder{}
	}
	q := e.cfg.QtyPerGrid

	var newSide string
	var next LevelOrder
	switch side {
	case "buy":
		e.Inventory += q
		newSide = "sell"
		next = LevelOrder{Level: level, Side: "sell", Price: e.levels[level+1], Qty: q}
	case "sell":
		e.Inventory -= q
		newSide = "buy"
		next = LevelOrder{Level: level, Side: "buy", Price: e.levels[level], Qty: q}
	default:
		return LevelOrder{}
	}

	e.sides[level] = newSide
	e.State.Sides[level] = newSide

	// 翻回基准方向 = 完成一次完整往返（低买高卖或高卖低买），现金落袋
	if level < len(e.State.BaseSides) && newSide == e.State.BaseSides[level] {
		spread := e.levels[level+1] - e.levels[level]
		e.RealizedPnl += q * spread
		e.MatchedCount++
	}
	return next
}

// Restore 用持久化快照恢复引擎内存状态（ResumeAll 调用）。
// baseSides 为空时（旧版快照）默认全 "buy"（等价于旧版"卖出即结算"的下半区语义）。
func (e *Engine) Restore(sides, baseSides []string, inv, pnl float64, matched int) error {
	if len(sides) != e.cfg.GridCount {
		return fmt.Errorf("sides length %d != gridCount %d", len(sides), e.cfg.GridCount)
	}
	if len(baseSides) == 0 {
		baseSides = make([]string, e.cfg.GridCount)
		for i := range baseSides {
			baseSides[i] = "buy"
		}
	}
	if len(baseSides) != e.cfg.GridCount {
		return fmt.Errorf("baseSides length %d != gridCount %d", len(baseSides), e.cfg.GridCount)
	}
	e.sides = append([]string{}, sides...)
	e.State.Sides = append([]string{}, sides...)
	e.State.BaseSides = append([]string{}, baseSides...)
	e.Inventory = inv
	e.RealizedPnl = pnl
	e.MatchedCount = matched
	return nil
}

// Snapshot 返回当前状态的序列化副本，可直接写入 strategies.state。
func (e *Engine) Snapshot() (json.RawMessage, error) {
	b, err := json.Marshal(&e.State)
	return b, err
}

// Levels 返回网格线价格（只读）。
func (e *Engine) Levels() []float64 { return e.levels }

// SideAt 返回区间 k 当前方向。
func (e *Engine) SideAt(k int) string {
	if k < 0 || k >= len(e.sides) {
		return ""
	}
	return e.sides[k]
}
