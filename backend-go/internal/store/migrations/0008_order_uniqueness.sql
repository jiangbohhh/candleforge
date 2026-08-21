-- WP6 一致性加固（F1/M1）：broker_order_id 唯一化
-- 交易所订单号在本地应唯一，供 UserDataStream 反查时无歧义。
-- 部分唯一索引：历史订单 broker_order_id 为 NULL 不受约束（Postgres 视多个 NULL 互异）。
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_broker_order_id_unique
    ON orders (broker_order_id)
    WHERE broker_order_id IS NOT NULL AND broker_order_id <> '';
