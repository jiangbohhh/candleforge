"""CandleForge Python 回测服务 — gRPC server 入口。

M0 阶段：实现 Ping 健康检查与 RunBacktest 占位，打通 Go <-> Python 链路。
真正的 backtrader 回测逻辑在 M2 实现。
"""

from __future__ import annotations

import logging
import os
from concurrent import futures

import grpc

from pb import quant_pb2, quant_pb2_grpc

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("candleforge-quant")

SERVICE_NAME = "candleforge-quant"


class QuantService(quant_pb2_grpc.QuantServiceServicer):
    """回测服务实现。"""

    def Ping(self, request, context):  # noqa: N802 (gRPC 命名约定)
        log.info("Ping received: %s", request.message)
        return quant_pb2.PingResponse(
            message=f"pong: {request.message}",
            service=SERVICE_NAME,
        )

    def RunBacktest(self, request, context):  # noqa: N802
        # M0 占位：返回桩绩效数据，验证序列化往返。
        # M2 接入 backtrader 后替换为真实回测。
        log.info(
            "RunBacktest stub: symbol=%s strategy=%s klines=%d",
            request.symbol,
            request.strategy,
            len(request.klines),
        )
        metrics = quant_pb2.BacktestMetrics(
            total_return=0.0,
            annual_return=0.0,
            max_drawdown=0.0,
            sharpe=0.0,
            trade_count=0,
            win_rate=0.0,
        )
        return quant_pb2.BacktestResponse(metrics=metrics, equity_curve=[])


def serve() -> None:
    addr = os.getenv("GRPC_ADDR", "[::]:50051")
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    quant_pb2_grpc.add_QuantServiceServicer_to_server(QuantService(), server)
    server.add_insecure_port(addr)
    server.start()
    log.info("%s gRPC server listening on %s", SERVICE_NAME, addr)
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
