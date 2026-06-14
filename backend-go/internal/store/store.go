// Package store 封装 PostgreSQL 数据访问。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Store 持有数据库连接。
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// ── 领域类型 ──

type Symbol struct {
	Symbol       string `json:"symbol"`
	Market       string `json:"market"`
	BaseAsset    string `json:"baseAsset"`
	QuoteAsset   string `json:"quoteAsset"`
	NativeSymbol string `json:"nativeSymbol"`
	Name         string `json:"name"`
	Status       string `json:"status"`
}

type Kline struct {
	Symbol    string  `json:"symbol"`
	Interval  string  `json:"interval"`
	OpenTime  int64   `json:"openTime"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	Volume    float64 `json:"volume"`
	CloseTime int64   `json:"closeTime"`
	TradeNum  int64   `json:"tradeNum"`
}

// ── symbols ──

func (s *Store) UpsertSymbol(ctx context.Context, sym Symbol) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO symbols (symbol, market, base_asset, quote_asset, native_symbol, name, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (symbol) DO UPDATE SET
			native_symbol=EXCLUDED.native_symbol, name=EXCLUDED.name, status=EXCLUDED.status`,
		sym.Symbol, sym.Market, sym.BaseAsset, sym.QuoteAsset, sym.NativeSymbol, sym.Name, sym.Status)
	return err
}

func (s *Store) ListSymbols(ctx context.Context) ([]Symbol, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT symbol, market, base_asset, quote_asset, native_symbol, name, status
		FROM symbols ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Symbol
	for rows.Next() {
		var sym Symbol
		if err := rows.Scan(&sym.Symbol, &sym.Market, &sym.BaseAsset, &sym.QuoteAsset,
			&sym.NativeSymbol, &sym.Name, &sym.Status); err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}

// ── klines ──

// UpsertKlines 批量写入 K 线（冲突即更新，幂等）。
func (s *Store) UpsertKlines(ctx context.Context, ks []Kline) error {
	if len(ks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO klines (symbol, interval, open_time, open, high, low, close, volume, close_time, trade_num)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (symbol, interval, open_time) DO UPDATE SET
			open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low,
			close=EXCLUDED.close, volume=EXCLUDED.volume,
			close_time=EXCLUDED.close_time, trade_num=EXCLUDED.trade_num`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, k := range ks {
		if _, err := stmt.ExecContext(ctx, k.Symbol, k.Interval, k.OpenTime,
			k.Open, k.High, k.Low, k.Close, k.Volume, k.CloseTime, k.TradeNum); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// GetKlines 取某标的某周期最近 limit 根 K 线（按时间升序返回）。
func (s *Store) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT symbol, interval, open_time, open, high, low, close, volume, close_time, trade_num
		FROM klines WHERE symbol=$1 AND interval=$2
		ORDER BY open_time DESC LIMIT $3`, symbol, interval, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Kline
	for rows.Next() {
		var k Kline
		if err := rows.Scan(&k.Symbol, &k.Interval, &k.OpenTime, &k.Open, &k.High,
			&k.Low, &k.Close, &k.Volume, &k.CloseTime, &k.TradeNum); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 反转为升序（图表需要）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// CountKlines 返回某标的某周期已存的 K 线数（用于判断是否需要回填）。
func (s *Store) CountKlines(ctx context.Context, symbol, interval string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM klines WHERE symbol=$1 AND interval=$2`, symbol, interval).Scan(&n)
	return n, err
}

// ── watchlist ──

func (s *Store) ListWatchlist(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT symbol FROM watchlist ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sym string
		if err := rows.Scan(&sym); err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}

func (s *Store) AddWatch(ctx context.Context, symbol string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO watchlist (symbol) VALUES ($1) ON CONFLICT DO NOTHING`, symbol)
	return err
}

func (s *Store) RemoveWatch(ctx context.Context, symbol string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM watchlist WHERE symbol=$1`, symbol)
	return err
}

// Ping 校验数据库连通。
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ── backtest_runs ──

type BacktestRun struct {
	ID          int64           `json:"id"`
	Symbol      string          `json:"symbol"`
	Strategy    string          `json:"strategy"`
	Interval    string          `json:"interval"`
	Params      json.RawMessage `json:"params"`
	Metrics     json.RawMessage `json:"metrics"`
	EquityCurve json.RawMessage `json:"equityCurve,omitempty"`
	Trades      json.RawMessage `json:"trades,omitempty"`
	InitialCash float64         `json:"initialCash"`
	Commission  float64         `json:"commission"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// InsertBacktestRun 写入一条回测记录，返回新 id。
func (s *Store) InsertBacktestRun(ctx context.Context, r BacktestRun) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO backtest_runs
			(symbol, strategy, interval, params, metrics, equity_curve, trades, initial_cash, commission)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`,
		r.Symbol, r.Strategy, r.Interval, r.Params, r.Metrics,
		r.EquityCurve, r.Trades, r.InitialCash, r.Commission).Scan(&id)
	return id, err
}

// ListBacktestRuns 返回历史回测列表（不含 equity_curve/trades，轻量）。
func (s *Store) ListBacktestRuns(ctx context.Context, limit int) ([]BacktestRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, symbol, strategy, interval, params, metrics, initial_cash, commission, created_at
		FROM backtest_runs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BacktestRun
	for rows.Next() {
		var r BacktestRun
		var initCash, comm sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.Symbol, &r.Strategy, &r.Interval,
			&r.Params, &r.Metrics, &initCash, &comm, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.InitialCash = initCash.Float64
		r.Commission = comm.Float64
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetBacktestRun 返回单条回测记录（含 equity_curve/trades）。
func (s *Store) GetBacktestRun(ctx context.Context, id int64) (BacktestRun, error) {
	var r BacktestRun
	var initCash, comm sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, symbol, strategy, interval, params, metrics, equity_curve, trades, initial_cash, commission, created_at
		FROM backtest_runs WHERE id=$1`, id).Scan(
		&r.ID, &r.Symbol, &r.Strategy, &r.Interval, &r.Params, &r.Metrics,
		&r.EquityCurve, &r.Trades, &initCash, &comm, &r.CreatedAt)
	if err != nil {
		return r, err
	}
	r.InitialCash = initCash.Float64
	r.Commission = comm.Float64
	return r, nil
}

// ── accounts ──

type Account struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Cash      float64   `json:"cash"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) GetAccount(ctx context.Context, id int64) (*Account, error) {
	var a Account
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, kind, cash, created_at FROM accounts WHERE id=$1`, id).Scan(
		&a.ID, &a.Name, &a.Kind, &a.Cash, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// EnsureSimAccount returns the existing sim account matching name, or creates a new one.
func (s *Store) EnsureSimAccount(ctx context.Context, name string, initialCash float64) (*Account, error) {
	// Try insert first (DO NOTHING if exists)
	s.db.ExecContext(ctx,
		`INSERT INTO accounts (name, kind, cash) VALUES ($1, 'sim', $2) ON CONFLICT DO NOTHING`,
		name, initialCash)

	var a Account
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, kind, cash, created_at FROM accounts WHERE name=$1 AND kind='sim'`, name).Scan(
		&a.ID, &a.Name, &a.Kind, &a.Cash, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ── orders ──

type InsertOrderParams struct {
	AccountID int64
	Symbol    string
	Side      string
	OrderType string
	Price     float64
	Quantity  float64
}

type OrderRow struct {
	ID        int64     `json:"id"`
	AccountID int64     `json:"accountId"`
	Symbol    string    `json:"symbol"`
	Side      string    `json:"side"`
	OrderType string    `json:"orderType"`
	Price     float64   `json:"price"`
	Quantity  float64   `json:"quantity"`
	FilledQty float64   `json:"filledQty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// InsertOrder writes a new order and returns orderID, accountID.
func (s *Store) InsertOrder(ctx context.Context, p InsertOrderParams) (orderID, accountID int64, err error) {
	var price interface{}
	if p.OrderType == "limit" {
		price = p.Price
	}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO orders (account_id, symbol, side, type, price, quantity, status)
		VALUES ($1,$2,$3,$4,$5,$6,'new')
		RETURNING id, account_id`, p.AccountID, p.Symbol, p.Side, p.OrderType, price, p.Quantity).Scan(&orderID, &accountID)
	return
}

func (s *Store) UpdateOrderStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE orders SET status=$1 WHERE id=$2`, status, id)
	return err
}

func (s *Store) GetOrder(ctx context.Context, id int64) (*OrderRow, error) {
	var o OrderRow
	var price, filledQty sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, account_id, symbol, side, type, price, quantity, COALESCE(filled_qty,0), status, created_at
		 FROM orders WHERE id=$1`, id).Scan(
		&o.ID, &o.AccountID, &o.Symbol, &o.Side, &o.OrderType,
		&price, &o.Quantity, &filledQty, &o.Status, &o.CreatedAt)
	if err != nil {
		return nil, err
	}
	o.Price = price.Float64
	o.FilledQty = filledQty.Float64
	return &o, nil
}

func (s *Store) ListOrders(ctx context.Context, accountID int64, limit int) ([]OrderRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, account_id, symbol, side, type, COALESCE(price,0), quantity, COALESCE(filled_qty,0), status, created_at
		FROM orders WHERE account_id=$1
		ORDER BY id DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderRow
	for rows.Next() {
		var o OrderRow
		if err := rows.Scan(&o.ID, &o.AccountID, &o.Symbol, &o.Side, &o.OrderType,
			&o.Price, &o.Quantity, &o.FilledQty, &o.Status, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PendingOrder is a simplified view used by SimBroker for matching.
type PendingOrder struct {
	ID        int64
	AccountID int64
	Symbol    string
	Side      string
	Price    float64 // limit price
	Quantity float64
}

func (s *Store) ListPendingLimitOrders(ctx context.Context) ([]PendingOrder, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, account_id, symbol, side, COALESCE(price,0), quantity
		FROM orders WHERE type='limit' AND status='new'
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingOrder
	for rows.Next() {
		var o PendingOrder
		if err := rows.Scan(&o.ID, &o.AccountID, &o.Symbol, &o.Side, &o.Price, &o.Quantity); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ── positions ──

type Position struct {
	ID        int64   `json:"id"`
	AccountID int64   `json:"accountId"`
	Symbol    string  `json:"symbol"`
	Quantity  float64 `json:"quantity"`
	AvgPrice  float64 `json:"avgPrice"`
}

// PositionView is a position enriched with current market price.
type PositionView struct {
	Position
	MarketPrice float64 `json:"marketPrice"`
	Unrealized  float64 `json:"unrealized"`
}

func (s *Store) GetPosition(ctx context.Context, accountID int64, symbol string) (*Position, error) {
	var p Position
	err := s.db.QueryRowContext(ctx,
		`SELECT id, account_id, symbol, quantity, avg_price FROM positions
		 WHERE account_id=$1 AND symbol=$2`, accountID, symbol).Scan(
		&p.ID, &p.AccountID, &p.Symbol, &p.Quantity, &p.AvgPrice)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ListPositions(ctx context.Context, accountID int64) ([]PositionView, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, account_id, symbol, quantity, avg_price FROM positions
		 WHERE account_id=$1 AND quantity > 0
		 ORDER BY symbol`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PositionView
	for rows.Next() {
		var pv PositionView
		if err := rows.Scan(&pv.ID, &pv.AccountID, &pv.Symbol, &pv.Quantity, &pv.AvgPrice); err != nil {
			return nil, err
		}
		out = append(out, pv)
	}
	return out, rows.Err()
}

// ── trades ──

type TradeRow struct {
	ID       int64     `json:"id"`
	OrderID  int64     `json:"orderId"`
	Symbol   string    `json:"symbol"`
	Side     string    `json:"side"`
	Price    float64   `json:"price"`
	Quantity float64   `json:"quantity"`
	Fee      float64   `json:"fee"`
	TradedAt time.Time `json:"tradedAt"`
}

func (s *Store) ListTrades(ctx context.Context, accountID int64, limit int) ([]TradeRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.order_id, t.symbol, t.side, t.price, t.quantity, COALESCE(t.fee,0), t.traded_at
		FROM trades t JOIN orders o ON t.order_id = o.id
		WHERE o.account_id=$1
		ORDER BY t.id DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TradeRow
	for rows.Next() {
		var tr TradeRow
		if err := rows.Scan(&tr.ID, &tr.OrderID, &tr.Symbol, &tr.Side, &tr.Price, &tr.Quantity, &tr.Fee, &tr.TradedAt); err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// ── account summary ──

type AccountSummary struct {
	Account     *Account       `json:"account"`
	TotalValue  float64        `json:"totalValue"`
	Unrealized  float64        `json:"unrealized"`
	Positions   []PositionView `json:"positions"`
}

// AccountSummary computes total value, unrealized P&L, and enriches positions with market prices.
func (s *Store) AccountSummary(ctx context.Context, accountID int64, priceFn func(string) float64) (*AccountSummary, error) {
	acct, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	positions, err := s.ListPositions(ctx, accountID)
	if err != nil {
		return nil, err
	}

	var (
		totalValue  float64
		totalUnreal float64
	)
	for i := range positions {
		mp := priceFn(positions[i].Symbol)
		unrealized := (mp - positions[i].AvgPrice) * positions[i].Quantity
		totalValue += mp * positions[i].Quantity
		totalUnreal += unrealized
		positions[i].MarketPrice = mp
		positions[i].Unrealized = unrealized
	}

	return &AccountSummary{
		Account:    acct,
		TotalValue: acct.Cash + totalValue,
		Unrealized: totalUnreal,
		Positions:  positions,
	}, nil
}
