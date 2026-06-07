FROM python:3.12-slim

WORKDIR /app

# 先装依赖利用缓存
COPY quant-py/pyproject.toml ./
RUN pip install --no-cache-dir grpcio grpcio-tools

# 拷贝服务代码（含已生成的 pb）
COPY quant-py/ ./

EXPOSE 50051
CMD ["python", "server.py"]
