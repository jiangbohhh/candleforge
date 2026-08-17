// metrics.go — 回测指标计算，对齐 quant-py backtrader 字段语义。
package backtest

import (
	"math"
	"time"
)

// EquityPoint 是净值曲线上的一个点。
type EquityPoint struct {
	Time  int64   // bar open_time（ms），与 Python 口径一致
	Value float64
}

// Metrics 计算回测指标。
// equityCurve：按 bar 顺序的净值序列（含初始点）。
// initialCash：初始资金。
func Metrics(equityCurve []EquityPoint, initialCash float64, tradeCount, winCount int) map[string]float64 {
	if len(equityCurve) < 2 || initialCash <= 0 {
		return map[string]float64{
			"totalReturn": 0, "annualReturn": 0, "maxDrawdown": 0,
			"sharpe": 0, "tradeCount": float64(tradeCount), "winRate": 0,
		}
	}

	final := equityCurve[len(equityCurve)-1].Value
	totalReturn := (final - initialCash) / initialCash

	// 年化收益（对齐 backtrader Returns.rnorm: compound annualized）
	firstTime := time.UnixMilli(equityCurve[0].Time)
	lastTime := time.UnixMilli(equityCurve[len(equityCurve)-1].Time)
	years := lastTime.Sub(firstTime).Hours() / 24 / 365.25
	var annualReturn float64
	if years > 0 && final > 0 {
		annualReturn = math.Pow(final/initialCash, 1/years) - 1
	}

	// 最大回撤（小数，对齐 runner.py 已 ÷100）
	maxDD := maxDrawdown(equityCurve)

	// 夏普（对齐 backtrader SharpeRatio annualize=False：不年化，按日收益 mean/std）
	sharpe := sharpeRatio(equityCurve)

	// 胜率
	var winRate float64
	if tradeCount > 0 {
		winRate = float64(winCount) / float64(tradeCount)
	}

	return map[string]float64{
		"totalReturn": totalReturn,
		"annualReturn": annualReturn,
		"maxDrawdown": maxDD,
		"sharpe":      sharpe,
		"tradeCount":  float64(tradeCount),
		"winRate":     winRate,
	}
}

func maxDrawdown(curve []EquityPoint) float64 {
	if len(curve) == 0 {
		return 0
	}
	peak := curve[0].Value
	maxDD := 0.0
	for _, p := range curve {
		if p.Value > peak {
			peak = p.Value
		}
		if peak > 0 {
			dd := (peak - p.Value) / peak
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

func sharpeRatio(curve []EquityPoint) float64 {
	if len(curve) < 2 {
		return 0
	}
	// 按 UTC 日重采样（取每日最后一条净值）
	dailyVals := resampleDaily(curve)
	if len(dailyVals) < 2 {
		return 0
	}
	// 日收益率
	returns := make([]float64, len(dailyVals)-1)
	for i := 1; i < len(dailyVals); i++ {
		if dailyVals[i-1] > 0 {
			returns[i-1] = (dailyVals[i] - dailyVals[i-1]) / dailyVals[i-1]
		}
	}
	mean, std := meanStd(returns)
	if std == 0 {
		return 0
	}
	return mean / std
}

// resampleDaily 取每自然日（UTC）的最后净值。
func resampleDaily(curve []EquityPoint) []float64 {
	dayMap := make(map[string]float64)
	var days []string
	for _, p := range curve {
		day := time.UnixMilli(p.Time).UTC().Format("2006-01-02")
		if _, exists := dayMap[day]; !exists {
			days = append(days, day)
		}
		dayMap[day] = p.Value // 覆盖 = 取当日最后
	}
	vals := make([]float64, len(days))
	for i, d := range days {
		vals[i] = dayMap[d]
	}
	return vals
}

func meanStd(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	variance := 0.0
	for _, x := range xs {
		d := x - mean
		variance += d * d
	}
	variance /= float64(len(xs))
	return mean, math.Sqrt(variance)
}
