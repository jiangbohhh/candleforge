// Package grid 实现现货/永续网格策略，含纯内存状态机和实盘/回测 adapter。
package grid

import (
	"encoding/json"
	"fmt"
	"math"
)

// Params 是网格策略的完整参数集。
// M6.5：marketType=futures（USDT-M 永续）解锁，direction 三方向解锁，leverage 仍锁定 1x。
type Params struct {
	MarketType string  `json:"marketType"` // spot | futures（USDT-M 永续）
	Direction  string  `json:"direction"`  // long | short | neutral（spot 仅 long）
	Leverage   float64 `json:"leverage"`   // 锁定 1（字段保留供后续解锁）

	// 网格参数
	GridType   string  `json:"gridType"`   // arithmetic | geometric
	LowerPrice float64 `json:"lowerPrice"` // 下边界（含）
	UpperPrice float64 `json:"upperPrice"` // 上边界（含）
	GridCount  int     `json:"gridCount"`  // 网格数 N，线数=N+1
	QtyPerGrid float64 `json:"qtyPerGrid"` // 每格交易数量（base asset）
}

// ParseParams 从 JSON 解析并校验参数。
func ParseParams(raw json.RawMessage) (*Params, error) {
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params JSON: %w", err)
	}
	return &p, p.Validate()
}

// Dir 返回归一化方向（空 → long）。
func (p *Params) Dir() string {
	if p.Direction == "" {
		return "long"
	}
	return p.Direction
}

// IsFutures 返回是否 USDT-M 永续。
func (p *Params) IsFutures() bool {
	return p.MarketType == "futures"
}

// Validate 校验参数合法性。
func (p *Params) Validate() error {
	switch p.MarketType {
	case "", "spot", "futures":
	default:
		return fmt.Errorf("marketType %q not supported; must be 'spot' or 'futures'", p.MarketType)
	}
	switch p.Dir() {
	case "long", "short", "neutral":
	default:
		return fmt.Errorf("direction %q not supported; must be long/short/neutral", p.Direction)
	}
	if !p.IsFutures() && p.Dir() != "long" {
		return fmt.Errorf("spot grid only supports direction=long; use marketType=futures for %s", p.Dir())
	}
	if p.Leverage != 0 && p.Leverage != 1 {
		return fmt.Errorf("leverage %.2f not supported yet; locked at 1x", p.Leverage)
	}
	// 必填项
	if p.GridType != "arithmetic" && p.GridType != "geometric" {
		return fmt.Errorf("gridType must be 'arithmetic' or 'geometric', got %q", p.GridType)
	}
	if p.LowerPrice <= 0 {
		return fmt.Errorf("lowerPrice must be > 0")
	}
	if p.UpperPrice <= p.LowerPrice {
		return fmt.Errorf("upperPrice %.8f must be > lowerPrice %.8f", p.UpperPrice, p.LowerPrice)
	}
	if p.GridCount < 2 {
		return fmt.Errorf("gridCount must be >= 2, got %d", p.GridCount)
	}
	if p.GridCount > 200 {
		return fmt.Errorf("gridCount must be <= 200, got %d", p.GridCount)
	}
	if p.QtyPerGrid <= 0 {
		return fmt.Errorf("qtyPerGrid must be > 0")
	}
	return nil
}

// GridLines 返回 N+1 条网格线价格（升序）。
func (p *Params) GridLines() []float64 {
	n := p.GridCount
	lines := make([]float64, n+1)
	for i := 0; i <= n; i++ {
		switch p.GridType {
		case "geometric":
			// P_i = lower * (upper/lower)^(i/N)
			lines[i] = p.LowerPrice * math.Pow(p.UpperPrice/p.LowerPrice, float64(i)/float64(n))
		default: // arithmetic
			lines[i] = p.LowerPrice + float64(i)*(p.UpperPrice-p.LowerPrice)/float64(n)
		}
		// 截断到 8 位小数（ExchangeInfo 精度校验留后续迭代）
		lines[i] = math.Round(lines[i]*1e8) / 1e8
	}
	return lines
}

// ShortLiqEstimate 估算满仓做空（N×qty，均价≈卖线均值）后的强平价（1x 粗略模型）：
// liq ≈ avgEntry + margin/positionQty。short/neutral 启动前用于安全余量校验。
func (p *Params) ShortLiqEstimate(margin float64) float64 {
	lines := p.GridLines()
	sum := 0.0
	for k := 0; k < p.GridCount; k++ {
		sum += lines[k+1] // 各区间开空价（上边界）
	}
	avgEntry := sum / float64(p.GridCount)
	qty := float64(p.GridCount) * p.QtyPerGrid
	if qty <= 0 {
		return math.Inf(1)
	}
	return avgEntry + margin/qty
}

// ── strategy schema 导出（供 /api/strategies/schemas 动态渲染表单）──

// ParamField 描述一个策略参数字段。
type ParamField struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // number | integer | select | boolean
	Default  any      `json:"default,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required"`
}

func fptr(v float64) *float64 { return &v }

// Schema 返回网格策略的参数描述列表。
func Schema() []ParamField {
	return []ParamField{
		{Name: "marketType", Label: "市场类型", Type: "select",
			Options: []string{"spot", "futures"}, Default: "spot", Required: true},
		{Name: "direction", Label: "方向", Type: "select",
			Options: []string{"long", "short", "neutral"}, Default: "long", Required: true},
		{Name: "gridType", Label: "网格类型", Type: "select",
			Options: []string{"arithmetic", "geometric"}, Default: "arithmetic", Required: true},
		{Name: "lowerPrice", Label: "下边界", Type: "number", Min: fptr(0), Required: true},
		{Name: "upperPrice", Label: "上边界", Type: "number", Min: fptr(0), Required: true},
		{Name: "gridCount", Label: "网格数", Type: "integer",
			Min: fptr(2), Max: fptr(200), Default: 20, Required: true},
		{Name: "qtyPerGrid", Label: "每格数量", Type: "number", Min: fptr(0), Required: true},
	}
}
