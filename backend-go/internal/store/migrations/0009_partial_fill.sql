-- H15：网格按增量消化部分成交。
-- qty = 该 intent 对应订单数量（补单失败重试时不能默认回 QtyPerGrid，
-- 否则部成后撤单的剩余量会被整格重挂）。
-- consumed_qty = 已计入引擎的累计成交量（只增）。
ALTER TABLE strategy_orders ADD COLUMN IF NOT EXISTS qty NUMERIC NOT NULL DEFAULT 0;
ALTER TABLE strategy_orders ADD COLUMN IF NOT EXISTS consumed_qty NUMERIC NOT NULL DEFAULT 0;
