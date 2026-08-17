package grid

import (
	"math"
	"testing"
)

// ── 网格线计算 ──

func TestGridLinesArithmetic(t *testing.T) {
	p := &Params{GridType: "arithmetic", LowerPrice: 100, UpperPrice: 200, GridCount: 4, QtyPerGrid: 1}
	lines := p.GridLines()
	want := []float64{100, 125, 150, 175, 200}
	if len(lines) != len(want) {
		t.Fatalf("len=%d want %d", len(lines), len(want))
	}
	for i, w := range want {
		if math.Abs(lines[i]-w) > 1e-6 {
			t.Errorf("lines[%d]=%.8f want %.8f", i, lines[i], w)
		}
	}
}

func TestGridLinesGeometric(t *testing.T) {
	// lower=100, upper=200, N=2 → 100, 141.42..., 200
	p := &Params{GridType: "geometric", LowerPrice: 100, UpperPrice: 200, GridCount: 2, QtyPerGrid: 1}
	lines := p.GridLines()
	if len(lines) != 3 {
		t.Fatalf("len=%d want 3", len(lines))
	}
	mid := math.Sqrt(100 * 200) // geometric mean
	if math.Abs(lines[1]-mid) > 0.01 {
		t.Errorf("lines[1]=%.8f want ~%.8f", lines[1], mid)
	}
}

func TestGridLinesGeometricN1BoundaryRejected(t *testing.T) {
	p := &Params{GridType: "geometric", LowerPrice: 100, UpperPrice: 200, GridCount: 1, QtyPerGrid: 1}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for gridCount=1")
	}
}

// ── 测试引擎构造 ──

func newEngine(t *testing.T, marketType, direction string) *Engine {
	t.Helper()
	p := &Params{
		MarketType: marketType, Direction: direction,
		GridType: "arithmetic", LowerPrice: 100, UpperPrice: 200,
		GridCount: 4, QtyPerGrid: 1,
	}
	e, err := New(p)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func newTestEngine(t *testing.T) *Engine { return newEngine(t, "", "") } // spot long

// ── PlanInit：long 三种市价位置 ──
// 区间 0=[100,125] 1=[125,150] 2=[150,175] 3=[175,200]

// p=160 在区间 2 内：仅区间 3 整体在市价上方 → 持仓 1，sell@200；
// 其余（含 p 所在区间 2）挂 buy 于下边界，全部休眠在市价远端。
func TestPlanInitLongMid(t *testing.T) {
	e := newTestEngine(t)
	initQty, orders := e.PlanInit(160)
	if initQty != 1 {
		t.Errorf("initQty=%.2f want 1", initQty)
	}
	if len(orders) != 4 {
		t.Errorf("orders count=%d want 4", len(orders))
	}
	if e.SideAt(0) != "buy" || e.SideAt(1) != "buy" || e.SideAt(2) != "buy" || e.SideAt(3) != "sell" {
		t.Errorf("sides wrong: %v", e.State.Sides)
	}
	if e.InitSideFor() != "buy" {
		t.Errorf("initSide=%q want buy", e.InitSideFor())
	}
	// 不允许任何穿价单：buy 价必须 < p，sell 价必须 > p
	for _, o := range orders {
		if o.Side == "buy" && o.Price >= 160 {
			t.Errorf("buy order crosses market: %+v", o)
		}
		if o.Side == "sell" && o.Price <= 160 {
			t.Errorf("sell order crosses market: %+v", o)
		}
	}
	if e.Inventory != 1 {
		t.Errorf("inventory=%.2f want 1", e.Inventory)
	}
}

// p 低于所有区间 → 全部区间在市价上方，满仓建仓，全挂 sell。
func TestPlanInitLongBelowAll(t *testing.T) {
	e := newTestEngine(t)
	initQty, orders := e.PlanInit(90)
	if initQty != 4 {
		t.Errorf("initQty=%.2f want 4", initQty)
	}
	for i, o := range orders {
		if o.Side != "sell" {
			t.Errorf("orders[%d].Side=%q want sell", i, o.Side)
		}
	}
}

// p 高于所有区间 → 零建仓，全挂 buy 休眠于下方。
func TestPlanInitLongAboveAll(t *testing.T) {
	e := newTestEngine(t)
	initQty, orders := e.PlanInit(210)
	if initQty != 0 {
		t.Errorf("initQty=%.2f want 0", initQty)
	}
	for i, o := range orders {
		if o.Side != "buy" {
			t.Errorf("orders[%d].Side=%q want buy", i, o.Side)
		}
	}
	if e.InitSideFor() != "" {
		t.Errorf("initSide=%q want empty", e.InitSideFor())
	}
}

// ── PlanInit：short ──

// p=160：区间 0、1 整体在市价下方 → 已持空 2（buy@100/125 等待平空）；
// 区间 2（含 p）、3 挂 sell 于上边界休眠。
func TestPlanInitShort(t *testing.T) {
	e := newEngine(t, "futures", "short")
	initQty, orders := e.PlanInit(160)
	if initQty != 2 {
		t.Errorf("initQty=%.2f want 2", initQty)
	}
	if e.InitSideFor() != "sell" {
		t.Errorf("initSide=%q want sell", e.InitSideFor())
	}
	if e.Inventory != -2 {
		t.Errorf("inventory=%.2f want -2", e.Inventory)
	}
	if e.SideAt(0) != "buy" || e.SideAt(1) != "buy" || e.SideAt(2) != "sell" || e.SideAt(3) != "sell" {
		t.Errorf("sides wrong: %v", e.State.Sides)
	}
	for _, o := range orders {
		if o.Side == "buy" && o.Price >= 160 {
			t.Errorf("buy order crosses market: %+v", o)
		}
		if o.Side == "sell" && o.Price <= 160 {
			t.Errorf("sell order crosses market: %+v", o)
		}
	}
}

// ── PlanInit：neutral ──

// p=160：零初始仓；上方区间挂 sell（开空），下方（含 p 区间）挂 buy（开多）。
func TestPlanInitNeutral(t *testing.T) {
	e := newEngine(t, "futures", "neutral")
	initQty, _ := e.PlanInit(160)
	if initQty != 0 {
		t.Errorf("initQty=%.2f want 0", initQty)
	}
	if e.Inventory != 0 {
		t.Errorf("inventory=%.2f want 0", e.Inventory)
	}
	if e.InitSideFor() != "" {
		t.Errorf("initSide=%q want empty", e.InitSideFor())
	}
	if e.SideAt(0) != "buy" || e.SideAt(1) != "buy" || e.SideAt(2) != "buy" || e.SideAt(3) != "sell" {
		t.Errorf("sides wrong: %v", e.State.Sides)
	}
}

// ── OnFill 翻转序列 ──

// long：buy 开仓不结算；翻回基准 buy（即 sell 成交）结算一回合。
func TestOnFillFlipSequenceLong(t *testing.T) {
	e := newTestEngine(t)
	e.PlanInit(160) // sides: [buy,buy,buy,sell]; inv=1

	// k2 buy@150 成交 → inv=2，翻 sell，补 sell@175；未结算
	next := e.OnFill(2, "buy")
	if next.Side != "sell" || next.Price != 175 || next.Level != 2 {
		t.Errorf("after buy fill: %+v", next)
	}
	if e.Inventory != 2 {
		t.Errorf("inv=%.2f want 2", e.Inventory)
	}
	if e.MatchedCount != 0 {
		t.Errorf("matched=%d want 0", e.MatchedCount)
	}

	// k2 sell@175 成交 → inv=1，翻回基准 buy → 结算 25
	next = e.OnFill(2, "sell")
	if next.Side != "buy" || next.Price != 150 || next.Level != 2 {
		t.Errorf("after sell fill: %+v", next)
	}
	if e.MatchedCount != 1 {
		t.Errorf("matched=%d want 1", e.MatchedCount)
	}
	if math.Abs(e.RealizedPnl-25) > 1e-9 {
		t.Errorf("pnl=%.2f want 25", e.RealizedPnl)
	}
	if e.Inventory != 1 {
		t.Errorf("inv=%.2f want 1", e.Inventory)
	}
}

// long 上方区间：首次 sell（卖出初始库存）不结算，随后 buy 翻回基准 sell 才结算。
func TestOnFillLongUpperIntervalSettlesOnBuy(t *testing.T) {
	e := newTestEngine(t)
	e.PlanInit(160) // k3 基准 sell，持初始库存

	next := e.OnFill(3, "sell") // 初始库存在 200 卖出：基差落袋，不计回合
	if e.MatchedCount != 0 {
		t.Errorf("matched=%d want 0 after basis sell", e.MatchedCount)
	}
	if next.Side != "buy" || next.Price != 175 {
		t.Errorf("next=%+v", next)
	}
	e.OnFill(3, "buy") // 175 买回 → 翻回基准 sell → 结算 25
	if e.MatchedCount != 1 || math.Abs(e.RealizedPnl-25) > 1e-9 {
		t.Errorf("matched=%d pnl=%.2f want 1/25", e.MatchedCount, e.RealizedPnl)
	}
}

// short：sell 开空不结算；buy 平空翻回基准 sell 结算。
func TestOnFillFlipSequenceShort(t *testing.T) {
	e := newEngine(t, "futures", "short")
	e.PlanInit(160) // sides: [buy,buy,sell,sell]; inv=-2

	// k2 sell@175 成交（开空）→ inv=-3，翻 buy，补 buy@150；未结算
	next := e.OnFill(2, "sell")
	if next.Side != "buy" || next.Price != 150 {
		t.Errorf("after sell fill: %+v", next)
	}
	if e.Inventory != -3 {
		t.Errorf("inv=%.2f want -3", e.Inventory)
	}
	if e.MatchedCount != 0 {
		t.Errorf("matched=%d want 0", e.MatchedCount)
	}

	// k2 buy@150 成交（平空）→ inv=-2，翻回基准 sell → 结算 25
	next = e.OnFill(2, "buy")
	if next.Side != "sell" || next.Price != 175 {
		t.Errorf("after buy fill: %+v", next)
	}
	if e.MatchedCount != 1 || math.Abs(e.RealizedPnl-25) > 1e-9 {
		t.Errorf("matched=%d pnl=%.2f want 1/25", e.MatchedCount, e.RealizedPnl)
	}
	if e.Inventory != -2 {
		t.Errorf("inv=%.2f want -2", e.Inventory)
	}
}

// neutral：下半区做多回合（buy→sell 结算），上半区做空回合（sell→buy 结算），仓位可正可负。
func TestOnFillNeutralBothSides(t *testing.T) {
	e := newEngine(t, "futures", "neutral")
	e.PlanInit(160) // sides: [buy,buy,buy,sell]; inv=0

	// 上半区 k3：开空 → 平空 结算
	e.OnFill(3, "sell")
	if e.Inventory != -1 {
		t.Errorf("inv=%.2f want -1", e.Inventory)
	}
	e.OnFill(3, "buy")
	if e.Inventory != 0 || e.MatchedCount != 1 {
		t.Errorf("inv=%.2f matched=%d want 0/1", e.Inventory, e.MatchedCount)
	}

	// 下半区 k0：开多 → 平多 结算
	e.OnFill(0, "buy")
	if e.Inventory != 1 {
		t.Errorf("inv=%.2f want 1", e.Inventory)
	}
	e.OnFill(0, "sell")
	if e.Inventory != 0 || e.MatchedCount != 2 {
		t.Errorf("inv=%.2f matched=%d want 0/2", e.Inventory, e.MatchedCount)
	}
	if math.Abs(e.RealizedPnl-50) > 1e-9 { // 25+25
		t.Errorf("pnl=%.2f want 50", e.RealizedPnl)
	}
}

// ── Restore 幂等性 ──

func TestRestoreIdempotent(t *testing.T) {
	e := newTestEngine(t)
	e.PlanInit(160)
	e.OnFill(3, "sell")
	e.OnFill(3, "buy")
	snap, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// 重建 engine 并恢复
	e2 := newTestEngine(t)
	if err := e2.Restore(e.State.Sides, e.State.BaseSides, e.Inventory, e.RealizedPnl, e.MatchedCount); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	snap2, _ := e2.Snapshot()
	// InitQty/InitSide/InitPrice 不由 Restore 恢复（挂单形态与盈亏才是对账依据），比较核心字段
	if e2.Inventory != e.Inventory || e2.RealizedPnl != e.RealizedPnl || e2.MatchedCount != e.MatchedCount {
		t.Errorf("core state mismatch after restore:\n  orig=%s\n  rest=%s", snap, snap2)
	}
	for k := 0; k < 4; k++ {
		if e2.SideAt(k) != e.SideAt(k) {
			t.Errorf("side[%d] mismatch: %q vs %q", k, e2.SideAt(k), e.SideAt(k))
		}
	}
}

// 旧版快照（无 baseSides）恢复：默认基准全 buy。
func TestRestoreLegacySnapshotWithoutBaseSides(t *testing.T) {
	e := newTestEngine(t)
	if err := e.Restore([]string{"sell", "sell", "buy", "buy"}, nil, 2, 50, 2); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	// sell 成交翻回 buy（基准）→ 结算
	e.OnFill(0, "sell")
	if e.MatchedCount != 3 {
		t.Errorf("matched=%d want 3", e.MatchedCount)
	}
}

// ── 强平估算 ──

func TestShortLiqEstimate(t *testing.T) {
	p := &Params{
		MarketType: "futures", Direction: "short",
		GridType: "arithmetic", LowerPrice: 100, UpperPrice: 200,
		GridCount: 4, QtyPerGrid: 1,
	}
	// 卖线均值 = (125+150+175+200)/4 = 162.5；qty=4
	// margin=650（≈1x 满仓名义）→ liq = 162.5 + 650/4 = 325 ≈ 2×均价
	liq := p.ShortLiqEstimate(650)
	if math.Abs(liq-325) > 1e-6 {
		t.Errorf("liq=%.4f want 325", liq)
	}
	if liq < p.UpperPrice*1.15 {
		t.Error("1x short liq should clear the 15%% upper-bound buffer")
	}
}

// ── 参数校验 ──

func TestValidate(t *testing.T) {
	base := Params{
		GridType: "arithmetic", LowerPrice: 100, UpperPrice: 200,
		GridCount: 10, QtyPerGrid: 0.001,
	}
	cases := []struct {
		name    string
		mutate  func(*Params)
		wantErr bool
	}{
		{"spot long ok", func(p *Params) {}, false},
		{"futures long ok", func(p *Params) { p.MarketType = "futures" }, false},
		{"futures short ok", func(p *Params) { p.MarketType = "futures"; p.Direction = "short" }, false},
		{"futures neutral ok", func(p *Params) { p.MarketType = "futures"; p.Direction = "neutral" }, false},
		{"spot short rejected", func(p *Params) { p.Direction = "short" }, true},
		{"spot neutral rejected", func(p *Params) { p.Direction = "neutral" }, true},
		{"bad marketType", func(p *Params) { p.MarketType = "margin" }, true},
		{"bad direction", func(p *Params) { p.MarketType = "futures"; p.Direction = "both" }, true},
		{"leverage rejected", func(p *Params) { p.MarketType = "futures"; p.Leverage = 5 }, true},
		{"leverage 1 ok", func(p *Params) { p.MarketType = "futures"; p.Leverage = 1 }, false},
		{"bad gridType", func(p *Params) { p.GridType = "random" }, true},
		{"lower>=upper", func(p *Params) { p.UpperPrice = 100 }, true},
		{"gridCount=1", func(p *Params) { p.GridCount = 1 }, true},
		{"gridCount=201", func(p *Params) { p.GridCount = 201 }, true},
		{"zero qty", func(p *Params) { p.QtyPerGrid = 0 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			err := p.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
