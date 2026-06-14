-- M4 实盘：账户路由 + 关联 Binance 订单号
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS broker TEXT NOT NULL DEFAULT 'sim';
ALTER TABLE orders   ADD COLUMN IF NOT EXISTS broker_order_id TEXT;
CREATE INDEX IF NOT EXISTS idx_orders_broker_order ON orders(broker_order_id);

-- M3 遗留：EnsureSimAccount 的 ON CONFLICT 没有目标列，每次启动都新增一行。
-- 对每个 name，保留有 orders/positions 引用的那行（如有），否则保留最小 id；其余无引用的全部删掉。
WITH winners AS (
    SELECT DISTINCT ON (name) id
    FROM (
        SELECT a.id, a.name,
               (EXISTS (SELECT 1 FROM orders    WHERE account_id = a.id)
             OR EXISTS (SELECT 1 FROM positions WHERE account_id = a.id))::int AS has_usage
        FROM accounts a
    ) u
    ORDER BY name, has_usage DESC, id ASC
)
DELETE FROM accounts a
WHERE NOT EXISTS (SELECT 1 FROM winners w WHERE w.id = a.id)
  AND NOT EXISTS (SELECT 1 FROM orders    WHERE account_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM positions WHERE account_id = a.id);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'accounts_name_key'
    ) THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_name_key UNIQUE (name);
    END IF;
END$$;
