#!/usr/bin/env bash
# CandleForge Cloud Agent install script.
# Idempotent, non-interactive dependency refresh run after checkout.
# Installs toolchains missing from the base image (Go 1.23, PostgreSQL,
# python venv tooling) and refreshes each service's dependencies.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

GO_VERSION=1.23.3

# ── 1. Go toolchain ──
# backend-go/go.mod pins go 1.23.3; the default image ships an older Go.
if ! command -v go >/dev/null 2>&1 || [[ "$(go env GOVERSION 2>/dev/null)" != "go${GO_VERSION}"* ]]; then
  echo "==> Installing Go ${GO_VERSION}"
  curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf /tmp/go.tgz
  sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go
  sudo ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  rm -f /tmp/go.tgz
fi
go version

# ── 2. System packages ──
# PostgreSQL (server + client) backs the whole app; python3-venv is needed
# to build the isolated backtest-worker virtualenv.
NEED_APT=0
command -v psql >/dev/null 2>&1 || NEED_APT=1
command -v pg_ctlcluster >/dev/null 2>&1 || NEED_APT=1
python3 -c 'import ensurepip' >/dev/null 2>&1 || NEED_APT=1
if [[ "$NEED_APT" == "1" ]]; then
  echo "==> Installing system packages (postgresql, python3-venv)"
  sudo apt-get update -qq
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    postgresql postgresql-client python3-venv python3-dev build-essential
fi

# ── 3. Go main service: warm module cache + compile ──
echo "==> Building backend-go"
( cd backend-go && go build ./... )

# ── 4. Python backtest worker: isolated venv ──
# Versions mirror quant-py/Dockerfile (numpy MUST stay <2 for backtrader).
echo "==> Setting up quant-py venv"
( cd quant-py
  python3 -m venv .venv
  ./.venv/bin/pip install --quiet --upgrade pip
  ./.venv/bin/pip install --quiet \
    grpcio grpcio-tools backtrader==1.9.78.123 numpy==1.26.4 pandas==2.2.3
)

# ── 5. Frontend deps ──
echo "==> Installing frontend deps"
( cd frontend && npm ci )

echo "==> CandleForge install complete."
