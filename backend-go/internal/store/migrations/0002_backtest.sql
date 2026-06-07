-- 给 backtest_runs 补充回测所需列（M2）
ALTER TABLE backtest_runs ADD COLUMN IF NOT EXISTS interval     TEXT    NOT NULL DEFAULT '1h';
ALTER TABLE backtest_runs ADD COLUMN IF NOT EXISTS initial_cash NUMERIC;
ALTER TABLE backtest_runs ADD COLUMN IF NOT EXISTS commission   NUMERIC;
ALTER TABLE backtest_runs ADD COLUMN IF NOT EXISTS equity_curve JSONB;
ALTER TABLE backtest_runs ADD COLUMN IF NOT EXISTS trades       JSONB;
