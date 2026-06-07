"""backtrader 回测编排：把 gRPC 请求跑成绩效 + 净值曲线 + 买卖点。"""

from __future__ import annotations

from datetime import timezone

import backtrader as bt
import pandas as pd

from pb import quant_pb2
from strategies import get_strategy


class EquityRecorder(bt.Analyzer):
    """每根 bar 记录账户净值，得到净值曲线。"""

    def start(self):
        self.points: list[tuple] = []

    def next(self):
        dt = self.strategy.datetime.datetime(0)  # naive UTC（feed 保留了 tz 信息）
        # backtrader 的 datetime(0) 返回 naive，按 UTC 解释
        ts = dt.replace(tzinfo=timezone.utc).timestamp()
        self.points.append((int(ts * 1000), self.strategy.broker.getvalue()))

    def get_analysis(self):
        return self.points


def _klines_to_feed(klines) -> bt.feeds.PandasData:
    rows = [
        {
            "datetime": pd.to_datetime(k.open_time, unit="ms", utc=True),
            "open": k.open,
            "high": k.high,
            "low": k.low,
            "close": k.close,
            "volume": k.volume,
        }
        for k in klines
    ]
    df = pd.DataFrame(rows).set_index("datetime").sort_index()
    df.index = df.index.tz_convert(None)  # 转 naive（按 UTC），回转时统一加 UTC
    return bt.feeds.PandasData(dataname=df)


def _parse_params(params, strat_cls) -> dict:
    """把 gRPC map<string,string> 按策略 params 默认值类型转换。"""
    defaults = dict(strat_cls.params._getpairs())
    out: dict = {}
    for key, raw in params.items():
        if key not in defaults:
            continue
        ref = defaults[key]
        try:
            if isinstance(ref, bool):
                out[key] = raw.lower() in ("1", "true", "yes")
            elif isinstance(ref, int):
                out[key] = int(float(raw))
            elif isinstance(ref, float):
                out[key] = float(raw)
            else:
                out[key] = raw
        except (ValueError, TypeError):
            pass
    return out


def _metrics(strat, initial_cash, final_value) -> quant_pb2.BacktestMetrics:
    sharpe = strat.analyzers.sharpe.get_analysis().get("sharperatio")
    dd = strat.analyzers.dd.get_analysis()
    ret = strat.analyzers.ret.get_analysis()
    ta = strat.analyzers.ta.get_analysis()

    closed = ta.get("total", {}).get("closed", 0) or 0
    won = ta.get("won", {}).get("total", 0) or 0

    return quant_pb2.BacktestMetrics(
        total_return=(final_value - initial_cash) / initial_cash if initial_cash else 0.0,
        annual_return=ret.get("rnorm", 0.0) or 0.0,
        max_drawdown=(dd.get("max", {}).get("drawdown", 0.0) or 0.0) / 100.0,
        sharpe=float(sharpe) if sharpe is not None else 0.0,
        trade_count=int(closed),
        win_rate=(won / closed) if closed else 0.0,
    )


def run_backtest(req) -> quant_pb2.BacktestResponse:
    if len(req.klines) < 2:
        raise ValueError("not enough klines for backtest")

    cerebro = bt.Cerebro()
    cerebro.adddata(_klines_to_feed(req.klines))

    strat_cls = get_strategy(req.strategy)
    kwargs = _parse_params(req.params, strat_cls)
    cerebro.addstrategy(strat_cls, **kwargs)

    cerebro.broker.setcash(req.initial_cash or 10000.0)
    cerebro.broker.setcommission(commission=req.commission or 0.0)
    cerebro.addsizer(bt.sizers.PercentSizer, percents=95)

    cerebro.addanalyzer(
        bt.analyzers.SharpeRatio, _name="sharpe",
        timeframe=bt.TimeFrame.Days, riskfreerate=0.0,
    )
    cerebro.addanalyzer(bt.analyzers.DrawDown, _name="dd")
    cerebro.addanalyzer(bt.analyzers.TradeAnalyzer, _name="ta")
    cerebro.addanalyzer(bt.analyzers.Returns, _name="ret")
    cerebro.addanalyzer(EquityRecorder, _name="equity")

    initial = cerebro.broker.getvalue()
    results = cerebro.run()
    strat = results[0]
    final = cerebro.broker.getvalue()

    equity = [
        quant_pb2.EquityPoint(time=t, value=v)
        for t, v in strat.analyzers.equity.get_analysis()
    ]
    trades = [quant_pb2.Trade(**t) for t in strat.executed]

    return quant_pb2.BacktestResponse(
        metrics=_metrics(strat, initial, final),
        equity_curve=equity,
        trades=trades,
    )
