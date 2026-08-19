-- WP3: 下单幂等（clientOrderId）+ 成交去重（trade_id）

-- C2: 每笔订单一个全局唯一 client_order_id，用于超时后按其查单、避免重复下单。
ALTER TABLE orders ADD COLUMN IF NOT EXISTS client_order_id TEXT;
-- 部分唯一索引：仅对非空 client_order_id 生效（历史订单为 NULL 不受约束）。
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_client_order_id
    ON orders (client_order_id)
    WHERE client_order_id IS NOT NULL AND client_order_id <> '';

-- H2: 交易所成交 ID，去重 UDS 断线重放/重复推送。
-- Binance trade id 在同一 symbol 内唯一；用 (symbol, trade_id) 唯一约束足够。
-- 历史成交无 trade_id（NULL），Postgres 视多个 NULL 为互异，不会冲突。
ALTER TABLE trades ADD COLUMN IF NOT EXISTS trade_id BIGINT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_trades_symbol_trade_id
    ON trades (symbol, trade_id);
