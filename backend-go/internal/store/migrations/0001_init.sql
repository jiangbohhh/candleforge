-- CandleForge 初始 schema
-- 行情类表为 M1 主体；交易类表先建前瞻性最小结构，M3/M4 用后续迁移增量演进。

-- ── 标的元数据 ──
CREATE TABLE IF NOT EXISTS symbols (
    symbol        TEXT PRIMARY KEY,           -- 统一格式 CRYPTO.BTC-USDT
    market        TEXT NOT NULL,              -- CRYPTO / CN ...
    base_asset    TEXT NOT NULL,              -- BTC
    quote_asset   TEXT NOT NULL,              -- USDT
    native_symbol TEXT NOT NULL,              -- 交易所原生符号 BTCUSDT
    name          TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'TRADING',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── K 线（单表 + interval 列；PK 保证去重）──
CREATE TABLE IF NOT EXISTS klines (
    symbol     TEXT   NOT NULL,
    interval   TEXT   NOT NULL,               -- 1m/5m/15m/1h/4h/1d
    open_time  BIGINT NOT NULL,               -- Unix 毫秒 (UTC)
    open       NUMERIC NOT NULL,
    high       NUMERIC NOT NULL,
    low        NUMERIC NOT NULL,
    close      NUMERIC NOT NULL,
    volume     NUMERIC NOT NULL,
    close_time BIGINT NOT NULL,
    trade_num  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (symbol, interval, open_time)
);

CREATE INDEX IF NOT EXISTS idx_klines_lookup ON klines (symbol, interval, open_time DESC);

-- ── 自选列表 ──
CREATE TABLE IF NOT EXISTS watchlist (
    symbol   TEXT PRIMARY KEY REFERENCES symbols(symbol) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── 账户（M3 模拟盘 / M4 实盘）──
CREATE TABLE IF NOT EXISTS accounts (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'sim',   -- sim / live
    cash       NUMERIC NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── 持仓 ──
CREATE TABLE IF NOT EXISTS positions (
    id         BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    symbol     TEXT NOT NULL,
    quantity   NUMERIC NOT NULL DEFAULT 0,
    avg_price  NUMERIC NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, symbol)
);

-- ── 订单 ──
CREATE TABLE IF NOT EXISTS orders (
    id          BIGSERIAL PRIMARY KEY,
    account_id  BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    symbol      TEXT NOT NULL,
    side        TEXT NOT NULL,                -- buy / sell
    type        TEXT NOT NULL DEFAULT 'market',
    price       NUMERIC,
    quantity    NUMERIC NOT NULL,
    status      TEXT NOT NULL DEFAULT 'new',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── 成交流水 ──
CREATE TABLE IF NOT EXISTS trades (
    id         BIGSERIAL PRIMARY KEY,
    order_id   BIGINT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    symbol     TEXT NOT NULL,
    side       TEXT NOT NULL,
    price      NUMERIC NOT NULL,
    quantity   NUMERIC NOT NULL,
    fee        NUMERIC NOT NULL DEFAULT 0,
    traded_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── 策略配置 ──
CREATE TABLE IF NOT EXISTS strategies (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL,                 -- dual_ma / rsi ...
    params     JSONB NOT NULL DEFAULT '{}',
    status     TEXT NOT NULL DEFAULT 'stopped',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── 回测记录 ──
CREATE TABLE IF NOT EXISTS backtest_runs (
    id          BIGSERIAL PRIMARY KEY,
    symbol      TEXT NOT NULL,
    strategy    TEXT NOT NULL,
    params      JSONB NOT NULL DEFAULT '{}',
    metrics     JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
