// Package broker provides a pluggable trading gateway interface.
// SimBroker implements in-process matching for paper trading;
// future BinanceBroker will implement the same interface against real Binance API.
package broker

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"

	"github.com/jiangbohhh/candleforge/backend-go/internal/events"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// PlaceOrderRequest carries the fields needed to place an order.
type PlaceOrderRequest struct {
	AccountID  int64
	Symbol     string
	Side       string
	OrderType  string
	Price      float64 // limit price; ignored for market orders
	Quantity   float64
	StrategyID int64 // 0 = 手动下单，非零 = 策略自动单
	ReduceOnly bool  // 仅永续有效：只减仓不反向开仓（sim 忽略，Binance futures 透传）
}

// Broker is the unified trading interface.
type Broker interface {
	PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*store.OrderRow, error)
	CancelOrder(ctx context.Context, accountID, orderID int64) error
	ListOrders(ctx context.Context, accountID int64) ([]store.OrderRow, error)
	GetPositions(ctx context.Context, accountID int64) ([]store.PositionView, error)
}

// PriceFunc returns the current price for a symbol, or 0 if unknown.
type PriceFunc func(symbol string) float64

// SimBroker matches orders against live market prices in-process.
type SimBroker struct {
	db      *sql.DB
	store   *store.Store
	priceFn PriceFunc
	hub     *ws.Hub
	bus     *events.Bus
	mu      sync.Mutex
	stopCh  chan struct{}
}

// NewSimBroker creates a SimBroker and starts its limit-order matching loop.
// hub may be nil for tests; when non-nil it receives order/trade event broadcasts.
// bus may be nil; when non-nil filled orders are published to the strategy engine.
func NewSimBroker(db *sql.DB, st *store.Store, priceFn PriceFunc, hub *ws.Hub, bus *events.Bus) *SimBroker {
	sb := &SimBroker{
		db:      db,
		store:   st,
		priceFn: priceFn,
		hub:     hub,
		bus:     bus,
		stopCh:  make(chan struct{}),
	}
	go sb.matchLoop()
	return sb
}

// Stop shuts down the matching loop.
func (b *SimBroker) Stop() {
	close(b.stopCh)
}

// matchLoop periodically checks pending limit orders against current prices.
func (b *SimBroker) matchLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.matchLimitOrders()
		}
	}
}

// matchLimitOrders fetches all pending limit orders and fills those whose price conditions are met.
func (b *SimBroker) matchLimitOrders() {
	b.mu.Lock()
	defer b.mu.Unlock()

	orders, err := b.store.ListPendingLimitOrders(context.Background())
	if err != nil {
		log.Printf("sim broker match: list pending: %v", err)
		return
	}
	for _, o := range orders {
		price := b.priceFn(o.Symbol)
		if price <= 0 {
			continue
		}
		shouldFill := o.Side == "buy" && price <= o.Price || o.Side == "sell" && price >= o.Price
		if !shouldFill {
			continue
		}
		if err := b.executeFill(o, price); err != nil {
			log.Printf("sim broker fill order %d: %v", o.ID, err)
		}
	}
}

// executeFill fills an order at the given price in a single DB transaction.
func (b *SimBroker) executeFill(o store.PendingOrder, fillPrice float64) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Lock account row
	var cash float64
	err = tx.QueryRow(`SELECT cash FROM accounts WHERE id=$1 FOR UPDATE`, o.AccountID).Scan(&cash)
	if err != nil {
		return err
	}

	cost := fillPrice * o.Quantity

	if o.Side == "buy" {
		if cash < cost {
			return nil // insufficient funds — leave order pending
		}
		cash -= cost
	} else {
		cash += cost
	}

	// Update account cash
	if _, err := tx.Exec(`UPDATE accounts SET cash=$1 WHERE id=$2`, cash, o.AccountID); err != nil {
		return err
	}

	// Update order status
	if _, err := tx.Exec(`UPDATE orders SET status='filled', filled_qty=quantity WHERE id=$1`, o.ID); err != nil {
		return err
	}

	// Upsert position（签名仓位：永续可为负=空头；现货禁止卖穿为负）
	var posQty, posAvg float64
	err = tx.QueryRow(`SELECT quantity, avg_price FROM positions WHERE account_id=$1 AND symbol=$2 FOR UPDATE`,
		o.AccountID, o.Symbol).Scan(&posQty, &posAvg)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	delta := o.Quantity
	if o.Side == "sell" {
		delta = -o.Quantity
	}
	newQty := posQty + delta

	if !market.IsPerp(o.Symbol) && newQty < -1e-12 {
		// 现货卖穿：跳过成交（挂单保留；风控层正常时到不了这里）
		log.Printf("sim broker: spot oversell blocked for order %d (%s)", o.ID, o.Symbol)
		return nil
	}

	// 均价规则：同向加仓=加权均价；减仓=均价不变；穿越零点=以本次成交价重置
	var newAvg float64
	switch {
	case newQty == 0:
		newAvg = 0
	case posQty == 0 || (posQty > 0) == (delta > 0): // 开仓或同向加仓
		newAvg = (posAvg*abs(posQty) + fillPrice*abs(delta)) / (abs(posQty) + abs(delta))
	case (posQty > 0) != (newQty > 0): // 穿越零点反向
		newAvg = fillPrice
	default: // 减仓
		newAvg = posAvg
	}

	if _, err := tx.Exec(`
		INSERT INTO positions (account_id, symbol, quantity, avg_price)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (account_id, symbol) DO UPDATE SET
			quantity = EXCLUDED.quantity, avg_price = EXCLUDED.avg_price, updated_at = now()`,
		o.AccountID, o.Symbol, newQty, newAvg); err != nil {
		return err
	}

	// Insert trade record
	if _, err := tx.Exec(`
		INSERT INTO trades (order_id, symbol, side, price, quantity)
		VALUES ($1,$2,$3,$4,$5)`,
		o.ID, o.Symbol, o.Side, fillPrice, o.Quantity); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Post-commit: broadcast order + trade events
	b.broadcastOrder(o.ID)
	if b.hub != nil {
		b.hub.BroadcastEvent("trade", map[string]any{
			"orderId":  o.ID,
			"symbol":   o.Symbol,
			"side":     o.Side,
			"price":    fillPrice,
			"quantity": o.Quantity,
			"tradedAt": time.Now().UTC(),
		})
	}
	return nil
}

// PlaceOrder creates an order. Market orders fill immediately; limit orders wait for matching.
func (b *SimBroker) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*store.OrderRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	orderID, accountID, err := b.store.InsertOrder(ctx, store.InsertOrderParams{
		AccountID:  req.AccountID,
		Symbol:     req.Symbol,
		Side:       req.Side,
		OrderType:  req.OrderType,
		Price:      req.Price,
		Quantity:   req.Quantity,
		StrategyID: req.StrategyID,
	})
	if err != nil {
		return nil, err
	}

	// Market orders fill immediately
	if req.OrderType == "market" {
		price := b.priceFn(req.Symbol)
		if price <= 0 {
			_ = b.store.UpdateOrderStatus(ctx, orderID, "rejected")
			b.broadcastOrder(orderID)
			return b.store.GetOrder(ctx, orderID)
		}
		po := store.PendingOrder{
			ID:        orderID,
			AccountID: accountID,
			Symbol:    req.Symbol,
			Side:      req.Side,
			Price:     price,
			Quantity:  req.Quantity,
		}
		if err := b.executeFill(po, price); err != nil {
			log.Printf("sim broker: fill market order %d: %v", orderID, err)
			_ = b.store.UpdateOrderStatus(ctx, orderID, "rejected")
			b.broadcastOrder(orderID)
		}
	} else {
		// Limit order: announce its creation so the UI shows it pending.
		b.broadcastOrder(orderID)
	}

	return b.store.GetOrder(ctx, orderID)
}

// CancelOrder cancels a pending limit order.
func (b *SimBroker) CancelOrder(ctx context.Context, accountID, orderID int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	o, err := b.store.GetOrder(ctx, orderID)
	if err != nil {
		return err
	}
	if o.AccountID != accountID {
		return sql.ErrNoRows
	}
	if o.Status != "new" {
		return nil // already terminal
	}
	if err := b.store.UpdateOrderStatus(ctx, orderID, "canceled"); err != nil {
		return err
	}
	b.broadcastOrder(orderID)
	return nil
}

// ListOrders returns orders for an account (most recent first).
func (b *SimBroker) ListOrders(ctx context.Context, accountID int64) ([]store.OrderRow, error) {
	return b.store.ListOrders(ctx, accountID, 100)
}

// GetPositions returns current positions for an account.
func (b *SimBroker) GetPositions(ctx context.Context, accountID int64) ([]store.PositionView, error) {
	return b.store.ListPositions(ctx, accountID)
}

// broadcastOrder fetches the order's latest state, pushes it on the WS hub,
// and publishes to the events bus (for strategy engine routing).
func (b *SimBroker) broadcastOrder(orderID int64) {
	o, err := b.store.GetOrder(context.Background(), orderID)
	if err != nil {
		return
	}
	if b.hub != nil {
		b.hub.BroadcastEvent("order", o)
	}
	if b.bus != nil {
		b.bus.PublishOrder(o)
	}
}
