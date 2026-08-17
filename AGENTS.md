# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## Project Snapshot

CandleForge is a crypto quantitative trading system covering the full loop: market data → backtest → paper trading → live auto-trading. **M0–M4 are shipped** against Binance spot. We are now in the **M5–M9 platform refactor**: a second exchange (Gate.io), multi-strategy types (CTA + grid + market-making + cross-exchange arbitrage), multi-symbol, single-user password auth, frontend rewrite on Tailwind + shadcn/ui, and a dual backtest engine (Go-native for grid/MM/arb, Python `backtrader` for CTA, routed by strategy type). See `PLAN.md` for the living roadmap and user-level memory for refactor decisions.

## Core Commands

### Full stack (preferred)
```bash
cp .env.example .env
docker compose up --build      # frontend :5173 · backend :8080 · quant :50051 · pg :5432
```

### Local dev (run services separately)
```bash
docker compose up -d postgres                                       # DB only
cd quant-py && pip install grpcio grpcio-tools && python server.py  # Python backtest
cd backend-go && go run ./cmd/server                                # Go main service
cd frontend && npm install && npm run dev                           # Vite frontend
```

### Regenerate gRPC code (mandatory after editing `proto/quant.proto`)
```bash
bash proto/gen.sh   # writes both backend-go/pb/ and quant-py/pb/
```
The script also rewrites `import quant_pb2 as` → `from . import quant_pb2 as` in the Python output (the default `grpc_tools` output is not resolvable inside the `pb` package).

### Enable Binance live trading (M4)
- Set `BINANCE_API_KEY` + `BINANCE_API_SECRET` in `.env` → testnet auto-enables.
- For mainnet, **both** are required: `BINANCE_MAINNET=true` and `BINANCE_MAINNET_CONFIRM=I_UNDERSTAND_REAL_MONEY`. Missing either keeps you on testnet.
- If the container can't reach Binance, pass through a proxy: `HTTPS_PROXY=http://host.docker.internal:7890`.

### Database migrations
Auto-applied on startup. Files live in `backend-go/internal/store/migrations/*.sql`, executed in filename order, idempotent via the `schema_migrations` table. **To add one: create the next `00NN_xxx.sql`. Never edit or delete a released migration** — for column renames use the "add new → dual-write → cut reads → drop old" multi-migration pattern.

### Tests
Repo currently has no `*_test.go` or `test_*.py`. When you add tests, follow stock Go / pytest conventions.

## Architecture (essentials that require reading multiple files)

### Responsibility split (the load-bearing invariant)
- **Go main service (`backend-go`)** owns the entire hot path: market ingestion, WebSocket fan-out, orders / accounts / positions, risk gating, Web API, both Sim and Binance Brokers.
- **Python backtest service (`quant-py`)** is a thin worker exposing only `Ping` + `RunBacktest` over gRPC. It does **not** talk to the frontend, does **not** touch exchanges, and does **not** write the DB. Klines are passed in by Go as gRPC request payload.
- **`proto/quant.proto`** is the single coupling surface between Go and Python.

### Wiring (one file to understand the whole boot sequence)
`backend-go/cmd/server/main.go` composes every dependency:
1. Load env (`internal/config`) → connect PG → run migrations (`store.Migrate`).
2. Dial the Python backtest service (`grpcclient`).
3. Construct `BinanceSource` → `SeedAndBackfill` to pre-populate symbols and history.
4. Start `ws.Hub` → `LiveFeed` pumps Binance tickers through the hub to the frontend.
5. Build `SimBroker` (always present) + conditionally build `BinanceBroker` (only if env is complete) and register both in a `brokers` map.
6. Build `risk.Engine`.
7. `api.New(...)` receives everything and starts the HTTP server.

### Pluggable Broker
`internal/broker/sim.go` defines the unified interface: `Broker { PlaceOrder / CancelOrder / ListOrders / GetPositions }`. Both `SimBroker` (in-process matching) and `BinanceBroker` (go-binance + User Data Stream) implement it. The API layer dispatches via `?account=default|live`. **Adding an exchange = implement `Broker` + register in the `brokers` map**, no churn elsewhere.

### Unified symbol scheme
- Internal canonical form: `CRYPTO.BTC-USDT`.
- Native exchange form: `BTCUSDT` for Binance.
- Translation lives in `internal/market/symbol.go`. When adding an exchange, extend this layer with bidirectional mapping; **never let native symbols leak into `store` / `api` / frontend**.

### Risk gate
`internal/risk.Engine` runs **before** any Broker call inside the `placeOrder` handler. It enforces single-order notional cap and an emergency halt switch, shared by Sim and Live. New risk rules go here, not scattered across Broker implementations.

### Realtime fan-out
`internal/market/live.go` (Binance WS) → `market.Tick` → `internal/ws.Hub` → frontend `/ws`. The Hub carries three message types over the same socket: market ticks, order events, trade events.

### Backtest call chain
Frontend `POST /api/backtest` → `api.runBacktest` reads klines from PG → converts to `pb.Kline` → `quant.RunBacktest` (gRPC) → Python `backtest/runner.py` runs backtrader → returns metrics / equity / trades → Go serializes via `protojson` and persists to `backtest_runs` → frontend renders.

## Gotchas

- **Pin numpy `<2.0`**: `quant-py/pyproject.toml` keeps `numpy==1.26.4`. backtrader still references `np.bool` and other aliases removed in numpy 2.0; upgrading hard-crashes the worker.
- **Always regenerate proto via `gen.sh`**, not by invoking `protoc` per-language. The script holds the import-rewrite step that makes Python imports work inside the `pb` package.
- **Migrations are append-only.** Never modify a shipped migration; pick the next number.
- **Adding an exchange follows three steps**: (1) implement `MarketDataSource` and symbol mapping in `internal/market`; (2) implement `Broker` in `internal/broker`; (3) wire it up in `main.go` and add an env guard in `config.go` (missing keys must skip construction, not panic).
- **Live broker is opt-in.** If API keys are missing, only `sim` exists. Never assume `brokers["binance"]` is present — look it up and return "no broker available" otherwise.
- **The WebSocket Hub is a singleton** shared by market and order pushes. Add a new push type via `hub.Broadcast(msgType, payload)`; don't open a parallel connection.

## Refactor-period notes (M5–M9)

- `internal/market/symbol.go` only handles Binance. When Gate.io lands in M5, switch to per-exchange routing rather than stacking `if-else` inside `ToNative`.
- The `accounts.broker` column today is `sim` / `binance`. M5 will repurpose it into an `exchange_id`; plan migrations to keep the old values readable.
- `quant-py/strategies/dual_ma.py` is currently a backtest-only script. Once the M6 strategy engine lands, every strategy must export a parameter schema so the frontend can render a dynamic config form.
- Before the M9 frontend rewrite, keep new pages on Antd. **Don't half-migrate** — two UI libraries side by side is worse than either alone.

## Key files

- `PLAN.md` — roadmap, milestone state, decision log. **Keep it in sync with any scope change.**
- `proto/quant.proto` — the only Go↔Python contract.
- `backend-go/cmd/server/main.go` — main wiring.
- `backend-go/internal/api/api.go` — every HTTP route lives here.
- `backend-go/internal/broker/sim.go` — Broker interface definition.
- `backend-go/internal/store/migrations/` — single source of truth for DDL.
- `quant-py/server.py` + `quant-py/backtest/runner.py` — backtest entry points.
