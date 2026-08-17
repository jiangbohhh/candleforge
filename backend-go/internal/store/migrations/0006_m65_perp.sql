-- M6.5 USDT-M 永续合约网格：资金费率历史
-- 永续标的复用 symbols/klines 表（symbol 带 .PERP 后缀），仅新增 funding_rates。

CREATE TABLE IF NOT EXISTS funding_rates (
    symbol       TEXT    NOT NULL,              -- 统一格式 CRYPTO.BTC-USDT.PERP
    funding_time BIGINT  NOT NULL,              -- 结算时间 Unix 毫秒 (UTC)，每 8h 一期
    rate         NUMERIC NOT NULL,              -- 当期费率（小数，如 0.0001 = 0.01%）
    mark_price   NUMERIC NOT NULL DEFAULT 0,    -- 结算时标记价格（fapi 返回，可为 0）
    PRIMARY KEY (symbol, funding_time)
);

CREATE INDEX IF NOT EXISTS idx_funding_rates_lookup ON funding_rates (symbol, funding_time DESC);
