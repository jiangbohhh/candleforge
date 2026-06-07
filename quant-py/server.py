"""CandleForge Python 回测服务 — gRPC server 入口。

Ping 健康检查 + RunBacktest（backtrader 双均线等策略）。
"""

from __future__ import annotations

import logging
import os
from concurrent import futures

import grpc

from backtest.runner import run_backtest
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
        log.info(
            "RunBacktest: symbol=%s strategy=%s klines=%d",
            request.symbol, request.strategy, len(request.klines),
        )
        try:
            resp = run_backtest(request)
            log.info(
                "RunBacktest done: trades=%d equity_points=%d",
                len(resp.trades), len(resp.equity_curve),
            )
            return resp
        except Exception as e:  # noqa: BLE001
            log.exception("backtest failed")
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(str(e))
            return quant_pb2.BacktestResponse()


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
