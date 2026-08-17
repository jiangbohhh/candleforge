// Package risk provides pre-trade validation for the trading engine.
package risk

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

// PriceFunc returns the current price for a symbol, or 0 if unknown.
type PriceFunc func(symbol string) float64

// Status is the current risk engine state, exposed to clients.
type Status struct {
	Halted      bool    `json:"halted"`
	HaltReason  string  `json:"haltReason"`
	MaxNotional float64 `json:"maxNotional"`
}

// Engine carries mutable risk state (halt switch, limits) and validates orders.
type Engine struct {
	st          *store.Store
	mu          sync.RWMutex
	halted      bool
	haltReason  string
	maxNotional float64 // per-order notional cap (USDT); 0 = no cap
}

// NewEngine constructs a risk engine with a per-order notional cap.
// Set maxNotional <= 0 to disable the cap.
func NewEngine(st *store.Store, maxNotional float64) *Engine {
	return &Engine{st: st, maxNotional: maxNotional}
}

// Halt flips the kill switch on with a reason; all new orders will be rejected.
func (e *Engine) Halt(reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.halted = true
	e.haltReason = reason
}

// Unhalt clears the kill switch.
func (e *Engine) Unhalt() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.halted = false
	e.haltReason = ""
}

// SetMaxNotional updates the per-order notional cap at runtime.
func (e *Engine) SetMaxNotional(v float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.maxNotional = v
}

// Status returns a snapshot of the engine state.
func (e *Engine) Status() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Status{Halted: e.halted, HaltReason: e.haltReason, MaxNotional: e.maxNotional}
}

// Validate checks whether an order can be placed.
// Returns nil if the order passes all checks, or an error describing why it was rejected.
func (e *Engine) Validate(ctx context.Context, accountID int64, symbol, side, orderType string, price, quantity float64, priceFn PriceFunc) error {
	e.mu.RLock()
	halted, reason, maxNotional := e.halted, e.haltReason, e.maxNotional
	e.mu.RUnlock()

	if halted {
		return fmt.Errorf("trading halted: %s", reason)
	}
	if quantity <= 0 {
		return fmt.Errorf("quantity must be positive")
	}
	if side != "buy" && side != "sell" {
		return fmt.Errorf("side must be buy or sell")
	}

	acct, err := e.st.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("account not found: %w", err)
	}

	currentPrice := priceFn(symbol)

	// Resolve the notional value of this order for cap + cash checks.
	var refPrice float64
	switch orderType {
	case "market":
		if currentPrice <= 0 {
			return fmt.Errorf("no price available for %s", symbol)
		}
		refPrice = currentPrice
	case "limit":
		if price <= 0 {
			return fmt.Errorf("limit price required")
		}
		refPrice = price
	default:
		return fmt.Errorf("unsupported order type: %s", orderType)
	}
	notional := refPrice * quantity

	if maxNotional > 0 && notional > maxNotional {
		return fmt.Errorf("order notional %.2f exceeds per-order cap %.2f", notional, maxNotional)
	}

	switch side {
	case "buy":
		if acct.Cash < notional {
			return fmt.Errorf("insufficient cash: need %.8f, have %.8f", notional, acct.Cash)
		}
	case "sell":
		if market.IsPerp(symbol) {
			// 永续允许卖出开空（1x 全额保证金）：超出现有多仓的部分按名义额校验现金。
			// 注：sim 台账中空头开仓所得现金先入账，此处为近似校验；
			// 策略级最坏占用预检（grid live Start）是主防线。
			held := 0.0
			if pos, err := e.st.GetPosition(ctx, accountID, symbol); err == nil && pos != nil {
				held = math.Max(pos.Quantity, 0)
			}
			opening := quantity - held
			if opening > 0 {
				need := refPrice * opening
				if acct.Cash < need {
					return fmt.Errorf("insufficient margin for short: need %.8f, have %.8f", need, acct.Cash)
				}
			}
		} else {
			pos, err := e.st.GetPosition(ctx, accountID, symbol)
			if err != nil || pos == nil {
				return fmt.Errorf("no position for %s", symbol)
			}
			if pos.Quantity < quantity {
				return fmt.Errorf("insufficient quantity: have %.8f, want %.8f", pos.Quantity, quantity)
			}
		}
	}

	return nil
}
