// Package strategy 实现 M6 策略引擎框架。
package strategy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jiangbohhh/candleforge/backend-go/internal/backtest"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/strategy/grid"
	"github.com/jiangbohhh/candleforge/backend-go/pb"
)

// BacktestFn 是 Go-native 回测函数签名（网格/MM 用，CTA 走 Python gRPC）。
// funding 是回测窗口内的资金费率历史（仅永续标的非空，由 api 层按 symbol 取出传入）。
type BacktestFn func(klines []store.Kline, funding []store.FundingRate, params json.RawMessage, initialCash, commission float64) (*pb.BacktestResponse, error)

// Descriptor 描述一种策略类型。
type Descriptor struct {
	Kind     string            // grid | dual_ma | …
	Name     string            // 展示名称
	Runnable bool              // 是否支持实盘运行
	Params   []grid.ParamField // 参数 schema（供前端动态渲染表单）
	Backtest BacktestFn        // nil → 走 Python gRPC
	// CheckSymbol 校验 symbol 与参数的一致性（如 futures ⇔ .PERP）；nil = 不校验。
	CheckSymbol func(symbol string, params json.RawMessage) error
}

// Schema 包装 Descriptor 用于 /api/strategies/schemas 响应。
type Schema struct {
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Runnable bool              `json:"runnable"`
	Backtest string            `json:"backtestEngine"` // "go" | "python"
	Params   []grid.ParamField `json:"params"`
}

var registry = map[string]*Descriptor{}

// Register 注册一个策略类型。
func Register(d *Descriptor) {
	registry[d.Kind] = d
}

// Lookup 按 kind 查找策略描述符；未找到返回 nil。
func Lookup(kind string) *Descriptor {
	return registry[kind]
}

// AllSchemas 返回所有已注册策略的 schema 列表。
func AllSchemas() []Schema {
	out := make([]Schema, 0, len(registry))
	for _, d := range registry {
		engine := "python"
		if d.Backtest != nil {
			engine = "go"
		}
		out = append(out, Schema{
			Kind:     d.Kind,
			Name:     d.Name,
			Runnable: d.Runnable,
			Backtest: engine,
			Params:   d.Params,
		})
	}
	return out
}

// ── 策略接口（M6 运行时，Phase D 实现）──

// Strategy 是所有可运行策略的统一接口。
type Strategy interface {
	Start(ctx context.Context) error
	OnOrderUpdate(ctx context.Context, o *store.OrderRow) error
	Reconcile(ctx context.Context) error
	// Stop 停止策略；liquidate=true 则在停止后市价清仓。
	Stop(ctx context.Context, liquidate bool) error
}

// init 注册内置策略。
func init() {
	// 网格（现货/USDT-M 永续；Go 回测引擎，可实盘运行）
	Register(&Descriptor{
		Kind:     "grid",
		Name:     "网格（现货/永续）",
		Runnable: true,
		Params:   grid.Schema(),
		Backtest: backtest.RunGridBacktest,
		CheckSymbol: func(symbol string, params json.RawMessage) error {
			p, err := grid.ParseParams(params)
			if err != nil {
				return err
			}
			if p.IsFutures() != market.IsPerp(symbol) {
				return fmt.Errorf("marketType %q requires %s symbol (got %s); use .PERP suffix for futures",
					p.MarketType, map[bool]string{true: "perpetual", false: "spot"}[p.IsFutures()], symbol)
			}
			return nil
		},
	})

	// 双均线（Python gRPC，不可实盘运行）
	Register(&Descriptor{
		Kind:     "dual_ma",
		Name:     "双均线",
		Runnable: false,
		Params: []grid.ParamField{
			{Name: "fast", Label: "快线周期", Type: "integer", Default: 10, Required: true},
			{Name: "slow", Label: "慢线周期", Type: "integer", Default: 30, Required: true},
		},
		Backtest: nil, // 走 Python
	})
}
