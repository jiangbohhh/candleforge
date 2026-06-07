#!/usr/bin/env bash
# 从 proto/quant.proto 生成 Go 与 Python 的 gRPC 代码。
# 用法：在仓库根目录执行  bash proto/gen.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

export PATH="$PATH:$(go env GOPATH)/bin"

echo "==> 生成 Go pb..."
protoc --proto_path=proto \
  --go_out=. --go_opt=module=github.com/jiangbohhh/candleforge \
  --go-grpc_out=. --go-grpc_opt=module=github.com/jiangbohhh/candleforge \
  proto/quant.proto

echo "==> 生成 Python pb..."
python3 -m grpc_tools.protoc --proto_path=proto \
  --python_out=quant-py/pb --grpc_python_out=quant-py/pb \
  proto/quant.proto
touch quant-py/pb/__init__.py

# 修正 Python 生成代码的绝对导入为包内相对导入
# (grpc_tools 默认生成 `import quant_pb2`，在包内无法解析)
if [[ "$(uname)" == "Darwin" ]]; then
  sed -i '' 's/^import quant_pb2 as/from . import quant_pb2 as/' quant-py/pb/quant_pb2_grpc.py
else
  sed -i 's/^import quant_pb2 as/from . import quant_pb2 as/' quant-py/pb/quant_pb2_grpc.py
fi

echo "✅ 完成。Go -> backend-go/pb/  Python -> quant-py/pb/"
