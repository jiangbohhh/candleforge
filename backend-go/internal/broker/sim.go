// Package broker provides a pluggable trading gateway interface.
// SimBroker implements in-process matching for paper trading;
// future BinanceBroker will implement the same interface against real Binance API.
package broker

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

// PlaceOrderRequest carries the fields needed to place an order.
type PlaceOrderRequest struct {
	AccountID int64
	Symbol    string
	Side      string
	OrderType string
	Price     float64 // limit price; ignored for market orders
	Quantity  float64
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
	mu      sync.Mutex
	stopCh  chan struct{}
}

// NewSimBroker creates a SimBroker and starts its limit-order matching loop.
// hub may be nil for tests; when non-nil it receives order/trade event broadcasts.
func NewSimBroker(db *sql.DB, st *store.Store, priceFn PriceFunc, hub *ws.Hub) *SimBroker {
	sb := &SimBroker{
		db:      db,
		store:   st,
		priceFn: priceFn,
		hub:     hub,
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

	// Upsert position
	if o.Side == "buy" {
		_, err = tx.Exec(`
			INSERT INTO positions (account_id, symbol, quantity, avg_price)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (account_id, symbol) DO UPDATE SET
				quantity = positions.quantity + EXCLUDED.quantity,
				avg_price = CASE
					WHEN positions.quantity + EXCLUDED.quantity = 0 THEN 0
					ELSE (positions.avg_price * positions.quantity + EXCLUDED.avg_price * EXCLUDED.quantity) / (positions.quantity + EXCLUDED.quantity)
				END,
				updated_at = now()`,
			o.AccountID, o.Symbol, o.Quantity, fillPrice)
	} else {
		_, err = tx.Exec(`
			UPDATE positions SET quantity = quantity - $1, updated_at = now()
			WHERE account_id=$2 AND symbol=$3`,
			o.Quantity, o.AccountID, o.Symbol)
	}
	if err != nil {
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
	b.broadcastEvent("trade", map[string]any{
		"orderId":  o.ID,
		"symbol":   o.Symbol,
		"side":     o.Side,
		"price":    fillPrice,
		"quantity": o.Quantity,
		"tradedAt": time.Now().UTC(),
	})
	return nil
}

// PlaceOrder creates an order. Market orders fill immediately; limit orders wait for matching.
func (b *SimBroker) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*store.OrderRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	orderID, accountID, err := b.store.InsertOrder(ctx, store.InsertOrderParams{
		AccountID: req.AccountID,
		Symbol:    req.Symbol,
		Side:      req.Side,
		OrderType: req.OrderType,
		Price:     req.Price,
		Quantity:  req.Quantity,
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

// broadcastOrder fetches the order's latest state and pushes it on the WS hub.
func (b *SimBroker) broadcastOrder(orderID int64) {
	if b.hub == nil {
		return
	}
	o, err := b.store.GetOrder(context.Background(), orderID)
	if err != nil {
		return
	}
	b.broadcastEvent("order", o)
}

// broadcastEvent encodes {type, data} and sends to the hub (non-blocking).
func (b *SimBroker) broadcastEvent(eventType string, data any) {
	if b.hub == nil {
		return
	}
	msg, err := json.Marshal(map[string]any{"type": eventType, "data": data})
	if err != nil {
		return
	}
	b.hub.Broadcast(msg)
}
