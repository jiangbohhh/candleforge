// funding.go — 永续合约资金费率历史（M6.5）的 store 方法。
package store

import (
	"context"
)

// FundingRate 是一期资金费率结算记录。
type FundingRate struct {
	Symbol      string  `json:"symbol"`      // 统一格式 CRYPTO.BTC-USDT.PERP
	FundingTime int64   `json:"fundingTime"` // Unix 毫秒 (UTC)
	Rate        float64 `json:"rate"`        // 小数费率（0.0001 = 0.01%）
	MarkPrice   float64 `json:"markPrice"`
}

// UpsertFundingRates 批量写入资金费率（幂等）。
func (s *Store) UpsertFundingRates(ctx context.Context, rs []FundingRate) error {
	if len(rs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO funding_rates (symbol, funding_time, rate, mark_price)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (symbol, funding_time) DO UPDATE SET
			rate=EXCLUDED.rate, mark_price=EXCLUDED.mark_price`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, r := range rs {
		if _, err := stmt.ExecContext(ctx, r.Symbol, r.FundingTime, r.Rate, r.MarkPrice); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ListFundingRates 取 [startTime, endTime] 区间内的资金费率，按时间升序。
func (s *Store) ListFundingRates(ctx context.Context, symbol string, startTime, endTime int64) ([]FundingRate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT symbol, funding_time, rate, mark_price
		FROM funding_rates
		WHERE symbol = $1 AND funding_time >= $2 AND funding_time <= $3
		ORDER BY funding_time ASC`, symbol, startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FundingRate
	for rows.Next() {
		var r FundingRate
		if err := rows.Scan(&r.Symbol, &r.FundingTime, &r.Rate, &r.MarkPrice); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestFundingTime 返回已存的最新一期结算时间；无数据返回 0。
func (s *Store) LatestFundingTime(ctx context.Context, symbol string) (int64, error) {
	var t int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(funding_time), 0) FROM funding_rates WHERE symbol = $1`, symbol).Scan(&t)
	return t, err
}
