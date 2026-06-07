"""策略注册表：name -> 策略类。新增策略只需建文件 + 注册一行。"""

from __future__ import annotations

import backtrader as bt

from .dual_ma import DualMA

_REGISTRY: dict[str, type[bt.Strategy]] = {
    "dual_ma": DualMA,
}


def get_strategy(name: str) -> type[bt.Strategy]:
    if name not in _REGISTRY:
        raise KeyError(f"unknown strategy: {name}")
    return _REGISTRY[name]


def list_strategies() -> list[str]:
    return list(_REGISTRY)
