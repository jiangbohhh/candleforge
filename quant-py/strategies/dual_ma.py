"""双均线策略：快慢 SMA 金叉买入、死叉卖出。"""

from __future__ import annotations

from datetime import timezone

import backtrader as bt


class DualMA(bt.Strategy):
    params = (
        ("fast", 10),
        ("slow", 30),
    )

    def __init__(self):
        sma_fast = bt.ind.SMA(period=self.p.fast)
        sma_slow = bt.ind.SMA(period=self.p.slow)
        self.crossover = bt.ind.CrossOver(sma_fast, sma_slow)  # +1 金叉, -1 死叉
        self.executed: list[dict] = []  # 已成交记录（买卖点）

    def next(self):
        if not self.position:
            if self.crossover > 0:
                self.buy()
        elif self.crossover < 0:
            self.close()

    def notify_order(self, order):
        # 只记录已完成成交（Submitted/Accepted 也会回调，需过滤）
        if order.status != order.Completed:
            return
        dt = bt.num2date(order.executed.dt).replace(tzinfo=timezone.utc)
        self.executed.append(
            {
                "time": int(dt.timestamp() * 1000),
                "side": "buy" if order.isbuy() else "sell",
                "price": float(order.executed.price),
                "quantity": float(abs(order.executed.size)),
            }
        )
