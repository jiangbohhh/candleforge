FROM python:3.12-slim

WORKDIR /app

# 可选 PyPI 镜像源（默认官方；网络受限时用 --build-arg PIP_INDEX_URL=... 注入）
ARG PIP_INDEX_URL=https://pypi.org/simple
COPY quant-py/pyproject.toml ./
RUN pip install --no-cache-dir --index-url ${PIP_INDEX_URL} grpcio grpcio-tools

# 拷贝服务代码（含已生成的 pb）
COPY quant-py/ ./

EXPOSE 50051
CMD ["python", "server.py"]
