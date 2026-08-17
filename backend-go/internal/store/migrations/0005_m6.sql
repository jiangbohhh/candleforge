-- M6 策略引擎 + 现货网格 + 子账户（预留合约扩展位）

-- 子账户列（NULL parent_id = 主账户）
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS parent_id  BIGINT REFERENCES accounts(id);
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS sub_email  TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS api_key    TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS api_secret TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_accounts_parent ON accounts(parent_id) WHERE parent_id IS NOT NULL;

-- 资金划转台账
CREATE TABLE IF NOT EXISTS account_transfers (
    id              BIGSERIAL PRIMARY KEY,
    from_account_id BIGINT NOT NULL REFERENCES accounts(id),
    to_account_id   BIGINT NOT NULL REFERENCES accounts(id),
    amount          NUMERIC NOT NULL CHECK (amount > 0),
    reason          TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- strategies 补列（0001 已有 id/name/kind/params/status/created_at）
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS account_id  BIGINT REFERENCES accounts(id);
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS symbol      TEXT NOT NULL DEFAULT '';
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS market_type TEXT NOT NULL DEFAULT 'spot';
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS direction   TEXT NOT NULL DEFAULT 'long';
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS leverage    NUMERIC NOT NULL DEFAULT 1;
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS state       JSONB NOT NULL DEFAULT '{}';
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS last_error  TEXT NOT NULL DEFAULT '';
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS updated_at  TIMESTAMPTZ NOT NULL DEFAULT now();

-- 订单归属策略（删策略保留订单史）
ALTER TABLE orders ADD COLUMN IF NOT EXISTS strategy_id BIGINT REFERENCES strategies(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_orders_strategy ON orders(strategy_id) WHERE strategy_id IS NOT NULL;

-- 网格订单映射 / intent log
CREATE TABLE IF NOT EXISTS strategy_orders (
    id          BIGSERIAL PRIMARY KEY,
    strategy_id BIGINT NOT NULL REFERENCES strategies(id) ON DELETE CASCADE,
    grid_level  INT NOT NULL,
    side        TEXT NOT NULL,
    price       NUMERIC NOT NULL,
    order_id    BIGINT REFERENCES orders(id),
    purpose     TEXT NOT NULL DEFAULT 'grid',
    active      BOOLEAN NOT NULL DEFAULT true,
    processed   BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_strategy_orders_active_level
    ON strategy_orders(strategy_id, grid_level) WHERE (active AND purpose = 'grid');
CREATE INDEX IF NOT EXISTS idx_strategy_orders_order  ON strategy_orders(order_id);
CREATE INDEX IF NOT EXISTS idx_strategy_orders_active ON strategy_orders(strategy_id) WHERE active;
