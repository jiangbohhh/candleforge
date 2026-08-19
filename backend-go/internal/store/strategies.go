// strategies.go — M6 策略/子账户/intent-log 的 store 方法（与 store.go 同包）。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ── 策略行 ──

type StrategyRow struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"` // grid | dual_ma | …
	AccountID  int64           `json:"accountId"`
	Symbol     string          `json:"symbol"`
	MarketType string          `json:"marketType"`
	Direction  string          `json:"direction"`
	Leverage   float64         `json:"leverage"`
	Params     json.RawMessage `json:"params"`
	Status     string          `json:"status"` // stopped | running | error
	State      json.RawMessage `json:"state"`
	LastError  string          `json:"lastError"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

func (s *Store) InsertStrategy(ctx context.Context, r StrategyRow) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO strategies
			(name, kind, account_id, symbol, market_type, direction, leverage, params, status, state, last_error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id`,
		r.Name, r.Kind, r.AccountID, r.Symbol,
		r.MarketType, r.Direction, r.Leverage,
		r.Params, r.Status,
		nullableJSON(r.State), r.LastError).Scan(&id)
	return id, err
}

func (s *Store) GetStrategy(ctx context.Context, id int64) (*StrategyRow, error) {
	var r StrategyRow
	var acctID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, kind, COALESCE(account_id,0), COALESCE(symbol,''),
		       COALESCE(market_type,'spot'), COALESCE(direction,'long'), COALESCE(leverage,1),
		       params, status, COALESCE(state,'{}'), COALESCE(last_error,''),
		       created_at, updated_at
		FROM strategies WHERE id=$1`, id).Scan(
		&r.ID, &r.Name, &r.Kind, &acctID, &r.Symbol,
		&r.MarketType, &r.Direction, &r.Leverage,
		&r.Params, &r.Status, &r.State, &r.LastError,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	r.AccountID = acctID.Int64
	return &r, nil
}

func (s *Store) ListStrategies(ctx context.Context) ([]StrategyRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, COALESCE(account_id,0), COALESCE(symbol,''),
		       COALESCE(market_type,'spot'), COALESCE(direction,'long'), COALESCE(leverage,1),
		       params, status, COALESCE(state,'{}'), COALESCE(last_error,''),
		       created_at, updated_at
		FROM strategies ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategyRow
	for rows.Next() {
		var r StrategyRow
		var acctID sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &acctID, &r.Symbol,
			&r.MarketType, &r.Direction, &r.Leverage,
			&r.Params, &r.Status, &r.State, &r.LastError,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.AccountID = acctID.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListRunningStrategies(ctx context.Context) ([]StrategyRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, COALESCE(account_id,0), COALESCE(symbol,''),
		       COALESCE(market_type,'spot'), COALESCE(direction,'long'), COALESCE(leverage,1),
		       params, status, COALESCE(state,'{}'), COALESCE(last_error,''),
		       created_at, updated_at
		FROM strategies WHERE status='running' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategyRow
	for rows.Next() {
		var r StrategyRow
		var acctID sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &acctID, &r.Symbol,
			&r.MarketType, &r.Direction, &r.Leverage,
			&r.Params, &r.Status, &r.State, &r.LastError,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.AccountID = acctID.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CountRunningStrategies(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM strategies WHERE status='running'`).Scan(&n)
	return n, err
}

func (s *Store) UpdateStrategyStatus(ctx context.Context, id int64, status, lastError string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE strategies SET status=$1, last_error=$2, updated_at=now() WHERE id=$3`,
		status, lastError, id)
	return err
}

// TryMarkStrategyRunning 原子地把策略从非 running 迁移到 running，作为启动的权威闸门。
// 返回 true 表示本次调用赢得了迁移；false 表示该策略已处于 running（并发/重复启动被拦截）。
func (s *Store) TryMarkStrategyRunning(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE strategies SET status='running', last_error='', updated_at=now() WHERE id=$1 AND status<>'running'`,
		id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *Store) UpdateStrategyState(ctx context.Context, id int64, state json.RawMessage) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE strategies SET state=$1, updated_at=now() WHERE id=$2`, state, id)
	return err
}

func (s *Store) DeleteStrategy(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM strategies WHERE id=$1`, id)
	return err
}

// ── 子账户 ──

// SubAccount 是 accounts 表中 parent_id IS NOT NULL 的行（含凭证列）。
type SubAccount struct {
	Account
	ParentID  int64  `json:"parentId"`
	SubEmail  string `json:"subEmail"`
	APIKey    string `json:"-"` // 不回显
	APISecret string `json:"-"`
}

// CreateVirtualSubAccount 在事务中创建虚拟子账户并从父账户划款。
func (s *Store) CreateVirtualSubAccount(ctx context.Context, parentID int64, name string, allocation float64) (*Account, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 锁定父账户并检查余额
	var parentCash float64
	if err := tx.QueryRowContext(ctx, `SELECT cash FROM accounts WHERE id=$1 FOR UPDATE`, parentID).Scan(&parentCash); err != nil {
		return nil, fmt.Errorf("parent account %d: %w", parentID, err)
	}
	if allocation > 0 && parentCash < allocation {
		return nil, fmt.Errorf("insufficient funds: need %.2f, have %.2f", allocation, parentCash)
	}

	// 创建子账户（cash=allocation）
	var subID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO accounts (name, kind, broker, cash, parent_id)
		VALUES ($1,'sub','sim',$2,$3)
		RETURNING id`,
		name, allocation, parentID).Scan(&subID)
	if err != nil {
		return nil, fmt.Errorf("create sub account: %w", err)
	}

	if allocation > 0 {
		// 扣父账户
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET cash=cash-$1 WHERE id=$2`, allocation, parentID); err != nil {
			return nil, err
		}
		// 划转台账
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO account_transfers (from_account_id, to_account_id, amount, reason)
			VALUES ($1,$2,$3,'allocate')`, parentID, subID, allocation); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetAccount(ctx, subID)
}

// RegisterRealSubAccount 登记一个已存在的 Binance 真实子账户。
// api_key/api_secret 在落库前经应用层加密（若已配置主密钥）。
func (s *Store) RegisterRealSubAccount(ctx context.Context, parentID int64, name, subEmail, apiKey, apiSecret string) (*SubAccount, error) {
	encKey, err := s.sec.Encrypt(apiKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt api_key: %w", err)
	}
	encSecret, err := s.sec.Encrypt(apiSecret)
	if err != nil {
		return nil, fmt.Errorf("encrypt api_secret: %w", err)
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO accounts (name, kind, broker, cash, parent_id, sub_email, api_key, api_secret)
		VALUES ($1,'sub','binance',0,$2,$3,$4,$5)
		ON CONFLICT (name) DO NOTHING
		RETURNING id`,
		name, parentID, subEmail, encKey, encSecret).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetSubAccount(ctx, id)
}

func (s *Store) GetSubAccount(ctx context.Context, id int64) (*SubAccount, error) {
	var sa SubAccount
	var parentID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, kind, broker, cash, created_at,
		       COALESCE(parent_id,0), COALESCE(sub_email,''), COALESCE(api_key,''), COALESCE(api_secret,'')
		FROM accounts WHERE id=$1`, id).Scan(
		&sa.ID, &sa.Name, &sa.Kind, &sa.Broker, &sa.Cash, &sa.CreatedAt,
		&parentID, &sa.SubEmail, &sa.APIKey, &sa.APISecret)
	if err != nil {
		return nil, err
	}
	sa.ParentID = parentID.Int64
	if err := s.decryptCreds(&sa); err != nil {
		return nil, err
	}
	return &sa, nil
}

// decryptCreds 就地解密子账户的 api_key/api_secret（历史明文原样返回）。
func (s *Store) decryptCreds(sa *SubAccount) error {
	k, err := s.sec.Decrypt(sa.APIKey)
	if err != nil {
		return fmt.Errorf("decrypt api_key (account %d): %w", sa.ID, err)
	}
	sec, err := s.sec.Decrypt(sa.APISecret)
	if err != nil {
		return fmt.Errorf("decrypt api_secret (account %d): %w", sa.ID, err)
	}
	sa.APIKey, sa.APISecret = k, sec
	return nil
}

func (s *Store) ListSubAccounts(ctx context.Context, parentID int64) ([]SubAccount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, broker, cash, created_at,
		       COALESCE(parent_id,0), COALESCE(sub_email,''), COALESCE(api_key,''), COALESCE(api_secret,'')
		FROM accounts WHERE parent_id=$1 ORDER BY id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubAccount
	for rows.Next() {
		var sa SubAccount
		var parentID sql.NullInt64
		if err := rows.Scan(&sa.ID, &sa.Name, &sa.Kind, &sa.Broker, &sa.Cash, &sa.CreatedAt,
			&parentID, &sa.SubEmail, &sa.APIKey, &sa.APISecret); err != nil {
			return nil, err
		}
		sa.ParentID = parentID.Int64
		if err := s.decryptCreds(&sa); err != nil {
			return nil, err
		}
		out = append(out, sa)
	}
	return out, rows.Err()
}

// TransferFunds 在父子账户间划转（amount>0=父→子，amount<0=子→父）。
// 仅 sim 内部账务；live 资金划转由 universalTransfer 在 broker 层完成后再调本方法更新台账。
func (s *Store) TransferFunds(ctx context.Context, fromID, toID int64, amount float64, reason string) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var fromCash float64
	if err := tx.QueryRowContext(ctx, `SELECT cash FROM accounts WHERE id=$1 FOR UPDATE`, fromID).Scan(&fromCash); err != nil {
		return err
	}
	if fromCash < amount {
		return fmt.Errorf("insufficient funds in account %d: need %.2f, have %.2f", fromID, amount, fromCash)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET cash=cash-$1 WHERE id=$2`, amount, fromID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET cash=cash+$1 WHERE id=$2`, amount, toID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account_transfers (from_account_id, to_account_id, amount, reason)
		VALUES ($1,$2,$3,$4)`, fromID, toID, amount, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// ── strategy_orders intent-log ──

type StrategyOrderRow struct {
	ID         int64     `json:"id"`
	StrategyID int64     `json:"strategyId"`
	GridLevel  int       `json:"gridLevel"`
	Side       string    `json:"side"`
	Price      float64   `json:"price"`
	OrderID    int64     `json:"orderId"` // 0 = intent 未落单
	Purpose    string    `json:"purpose"`
	Active     bool      `json:"active"`
	Processed  bool      `json:"processed"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// InsertIntent 写入一条 intent（步骤①），同时将该 gridLevel 旧活动 intent 设为 inactive。
// 在事务中执行保证原子性。
func (s *Store) InsertIntent(ctx context.Context, strategyID int64, level int, side string, price float64, purpose string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 旧 intent 失活（仅 purpose='grid' 受唯一索引约束，但 init/liquidate 也统一清理）
	if _, err := tx.ExecContext(ctx, `
		UPDATE strategy_orders SET active=false, processed=true, updated_at=now()
		WHERE strategy_id=$1 AND grid_level=$2 AND active=true`, strategyID, level); err != nil {
		return 0, err
	}

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO strategy_orders (strategy_id, grid_level, side, price, purpose, active)
		VALUES ($1,$2,$3,$4,$5,true)
		RETURNING id`, strategyID, level, side, price, purpose).Scan(&id)
	if err != nil {
		return 0, err
	}

	return id, tx.Commit()
}

// AttachOrderID 完成步骤③：intent 绑定 broker 落单 ID。
func (s *Store) AttachOrderID(ctx context.Context, intentID, orderID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE strategy_orders SET order_id=$1, updated_at=now() WHERE id=$2`, orderID, intentID)
	return err
}

// MarkIntentProcessed 标记 intent 已被引擎消费（幂等）。
func (s *Store) MarkIntentProcessed(ctx context.Context, intentID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE strategy_orders SET processed=true, updated_at=now() WHERE id=$1`, intentID)
	return err
}

// DeactivateAllIntents 停止策略时将全部活动 intent 置为 inactive。
func (s *Store) DeactivateAllIntents(ctx context.Context, strategyID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE strategy_orders SET active=false, processed=true, updated_at=now()
		 WHERE strategy_id=$1 AND active=true`, strategyID)
	return err
}

// ListActiveIntents 列出某策略所有活动 intent，join orders 取最新状态。
func (s *Store) ListActiveIntents(ctx context.Context, strategyID int64) ([]StrategyOrderRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT so.id, so.strategy_id, so.grid_level, so.side, so.price::float8,
		       COALESCE(so.order_id,0), so.purpose, so.active, so.processed,
		       so.created_at, so.updated_at
		FROM strategy_orders so
		WHERE so.strategy_id=$1 AND so.active=true
		ORDER BY so.grid_level`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategyOrderRow
	for rows.Next() {
		var r StrategyOrderRow
		if err := rows.Scan(&r.ID, &r.StrategyID, &r.GridLevel, &r.Side, &r.Price,
			&r.OrderID, &r.Purpose, &r.Active, &r.Processed,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListUnprocessedIntents 取未消费的已成交 intent（order filled 且 !processed）。
func (s *Store) ListUnprocessedIntents(ctx context.Context, strategyID int64) ([]StrategyOrderRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT so.id, so.strategy_id, so.grid_level, so.side, so.price::float8,
		       COALESCE(so.order_id,0), so.purpose, so.active, so.processed,
		       so.created_at, so.updated_at
		FROM strategy_orders so
		JOIN orders o ON o.id = so.order_id
		WHERE so.strategy_id=$1 AND so.processed=false AND o.status='filled'
		ORDER BY so.grid_level`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategyOrderRow
	for rows.Next() {
		var r StrategyOrderRow
		if err := rows.Scan(&r.ID, &r.StrategyID, &r.GridLevel, &r.Side, &r.Price,
			&r.OrderID, &r.Purpose, &r.Active, &r.Processed,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListOrphanStrategyOrders 找出 strategy_id=X 且 status='new' 但不在任何 active intent 中的订单（崩溃孤儿）。
func (s *Store) ListOrphanStrategyOrders(ctx context.Context, strategyID int64) ([]OrderRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, o.account_id, o.symbol, o.side, o.type,
		       COALESCE(o.price,0), o.quantity, COALESCE(o.filled_qty,0),
		       o.status, COALESCE(o.broker_order_id,''), COALESCE(o.strategy_id,0), o.created_at
		FROM orders o
		WHERE o.strategy_id=$1 AND o.status='new'
		  AND NOT EXISTS (
		      SELECT 1 FROM strategy_orders so
		      WHERE so.order_id=o.id AND so.active=true
		  )
		ORDER BY o.id`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderRow
	for rows.Next() {
		var o OrderRow
		if err := rows.Scan(&o.ID, &o.AccountID, &o.Symbol, &o.Side, &o.OrderType,
			&o.Price, &o.Quantity, &o.FilledQty, &o.Status, &o.BrokerOrderID, &o.StrategyID, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ── helpers ──

func nullableJSON(v json.RawMessage) interface{} {
	if len(v) == 0 {
		return "{}"
	}
	return v
}
