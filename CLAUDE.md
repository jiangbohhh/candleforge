# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Snapshot

CandleForge is a crypto quantitative trading system covering the full loop: market data → backtest → paper trading → live auto-trading. **M0–M4, M6, and M6.5 are shipped**: Binance spot + USDT-M perpetual, strategy engine, grid strategy (spot long + perp long/short/neutral at 1x), Go backtest engine with funding-rate replay and liquidation detection, sub-account model. Gate.io was **deleted from scope (2026-08-17)** — multi-exchange is a future candidate only. Next milestones: M7 (auth), M8 (market-making; cross-exchange arb is pending a second exchange), M9 (Tailwind+shadcn frontend). The dual backtest engine is live: Go-native for grid (routed by strategy type), Python `backtrader` for CTA. See `PLAN.md` §16 for the living roadmap and user-level memory for refactor decisions.

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

### Enable Binance live trading (M4)∏
- Set `BINANCE_API_KEY` + `BINANCE_API_SECRET` in `.env` → testnet auto-enables.
- For mainnet, **both** are required: `BINANCE_MAINNET=true` and `BINANCE_MAINNET_CONFIRM=I_UNDERSTAND_REAL_MONEY`. Missing either keeps you on testnet.
- If the container can't reach Binance, pass through a proxy: `HTTPS_PROXY=http://host.docker.internal:7890`.

### Database migrations
Auto-applied on startup. Files live in `backend-go/internal/store/migrations/*.sql`, executed in filename order, idempotent via the `schema_migrations` table. **To add one: create the next `00NN_xxx.sql`. Never edit or delete a released migration** — for column renames use the "add new → dual-write → cut reads → drop old" multi-migration pattern.

### Tests
M6 added Go tests. Run with `go test ./...` from `backend-go/`. Passing suites: `internal/strategy/grid` (9 tests), `internal/backtest` (5 tests). When you add tests, follow stock Go / pytest conventions.

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
5. Build `events.Bus` → `SimBroker` (always present, receives bus) + conditionally build `BinanceBroker` (only if env is complete, also receives bus) and register both in a `brokers` map.
6. Build `risk.Engine`.
7. Build `BrokerFor` resolver (routes by accountID to correct broker instance).
8. Build `strategy.Manager` → `ResumeAll` (restores running strategies from DB).
9. `api.New(...)` + `srv.SetManager(mgr)` starts the HTTP server.

### Pluggable Broker
`internal/broker/sim.go` defines the unified interface: `Broker { PlaceOrder / CancelOrder / ListOrders / GetPositions }`. Both `SimBroker` (in-process matching) and `BinanceBroker` (go-binance + User Data Stream) implement it. The API layer dispatches via `?account=default|live`. **Adding an exchange = implement `Broker` + register in the `brokers` map**, no churn elsewhere.

The `BrokerFor(accountID int64) (Broker, error)` resolver in `main.go` routes strategy sub-accounts to the correct broker. Sim sub-accounts share the global `SimBroker`; live sub-accounts currently also fall back to `SimBroker` (true per-sub-account `BinanceBroker` dynamic instantiation is a post-M6 item).

### Unified symbol scheme
- Internal canonical form: `CRYPTO.BTC-USDT` (spot), `CRYPTO.BTC-USDT.PERP` (USDT-M perpetual, M6.5).
- Native exchange form: `BTCUSDT` for both (spot = api.binance.com, perp = fapi.binance.com).
- Translation lives in `internal/market/symbol.go` (`ToNative` strips `.PERP`; `IsPerp` / `PerpOf` helpers). `market.Router` dispatches klines/tickers to `BinanceSource` or `BinanceFuturesSource` by suffix. **Never let native symbols leak into `store` / `api` / frontend**, and **never mix spot and perp klines** — they are separate feeds with diverging prices.

### Perpetual futures (M6.5)
- Grid directions: spot = long only; futures = long/short/neutral. Leverage locked at 1x (`params.Validate` rejects >1). One-way position mode enforced on Binance.
- Key 1x simplification: futures cash accounting ≡ spot accounting with signed (negative = short) positions; equity = cash + position×price. No separate margin ledger.
- Funding rates: `funding_rates` table (0006), synced at startup + every 8h (`market.SyncFundingRates`, 2-year backfill). Backtests replay them per settlement; `BacktestMetrics.funding_cost` / `liquidated` were added to the proto (regen via `gen.sh`).
- Backtest liquidation: `SimExecutor.CheckLiquidation` at bar extremes with 0.5% maintenance margin; triggers terminate the run and flag metrics.
- `BinanceFuturesBroker` (`brokers["binance_futures"]`, account `live_futures`): env guard `BINANCE_FUTURES_API_KEY/SECRET` — futures testnet (testnet.binancefuture.com) keys are separate from spot testnet. Its UDS is a hand-rolled listenKey WebSocket — do NOT switch it to `futures.WsUserDataServe`, whose endpoint depends on the `futures.UseTestnet` package-global that would also flip the market-data WS.
- Grid engine settles a round (`RealizedPnl += q×spread`, `matchedCount++`) only when an interval flips back to its `baseSides` entry — the point where cash is actually banked.

### Risk gate
`internal/risk.Engine` runs **before** any Broker call inside the `placeOrder` handler. It enforces single-order notional cap and an emergency halt switch, shared by Sim and Live. New risk rules go here, not scattered across Broker implementations.

### Realtime fan-out
`internal/market/live.go` (Binance WS) → `market.Tick` → `internal/ws.Hub` → frontend `/ws`. The Hub carries four message types over the same socket: `ticker`, `order`, `trade`, `strategy`. Use `hub.BroadcastEvent(type, data)` helper (added in M6) — do not call `hub.Broadcast([]byte)` directly.

### Backtest call chain (dual-engine, M6+)
Frontend `POST /api/backtest` → `api.runBacktest`:
- **Grid strategies**: `strategy.Lookup(kind)` → `d.Backtest(klines, params, cash, commission)` → Go `backtest.RunGridBacktest` → returns `*pb.BacktestResponse`.
- **CTA strategies** (e.g. dual_ma): `quant.RunBacktest` (gRPC) → Python `backtest/runner.py` runs backtrader → returns `*pb.BacktestResponse`.

Both paths feed the same `protoMarshaler` (`protojson.MarshalOptions{EmitUnpopulated:true}`) → persist to `backtest_runs` → frontend renders. **int64 fields serialize as JSON strings** via protojson — both engines must return `*pb.BacktestResponse`, not custom structs.

### Strategy engine (M6)
- `internal/events/bus.go`: in-process pub/sub `bus.PublishOrder(o)` → `bus.Subscribe(strategyID, ch)`. Broker implementations call `PublishOrder` in their `broadcastOrder` path.
- `internal/strategy/registry.go`: `Register(Descriptor)` / `Lookup(kind)` / `AllSchemas()`. init() registers "grid" (Go backtest, Runnable=true) and "dual_ma" (Python backtest, Runnable=false).
- `internal/strategy/manager.go`: `Manager` holds a map of running `runner`s. `ResumeAll` called at startup restores `status='running'` strategies.
- `internal/strategy/runner.go`: one goroutine per strategy; serial event queue from `events.Bus`; 30s `Reconcile` tick.
- `internal/strategy/grid/`: `params.go` (schema+validation) / `engine.go` (pure state machine, no IO) / `live.go` (live adapter: intent-log 3-step + risk + broker).
- `internal/backtest/`: `engine.go` (bar-path `SimExecutor`) / `grid.go` (`RunGridBacktest`) / `metrics.go` (metrics aligned to Python runner).

### Sub-account model (M6)
- Every strategy runs in its own sub-account (`accounts` row with `parent_id`).
- Sim: virtual sub-accounts created automatically at strategy creation; cash transferred from parent.
- Live: Binance real sub-accounts registered via `POST /api/accounts/sub`; credentials stored in `accounts.api_key/api_secret`.
- `store/strategies.go`: `InsertIntent / AttachOrderID / MarkIntentProcessed` implement crash-safe 3-step order placement. Partial unique index `(strategy_id, grid_level) WHERE active AND purpose='grid'` enforces one active order per grid level.

## Gotchas

- **Pin numpy `<2.0`**: `quant-py/pyproject.toml` keeps `numpy==1.26.4`. backtrader still references `np.bool` and other aliases removed in numpy 2.0; upgrading hard-crashes the worker.
- **Always regenerate proto via `gen.sh`**, not by invoking `protoc` per-language. The script holds the import-rewrite step that makes Python imports work inside the `pb` package.
- **Migrations are append-only.** Never modify a shipped migration; pick the next number.
- **Adding an exchange follows three steps**: (1) implement `MarketDataSource` and symbol mapping in `internal/market`; (2) implement `Broker` in `internal/broker`; (3) wire it up in `main.go` and add an env guard in `config.go` (missing keys must skip construction, not panic).
- **Live broker is opt-in.** If API keys are missing, only `sim` exists. Never assume `brokers["binance"]` is present — look it up and return "no broker available" otherwise.
- **The WebSocket Hub is a singleton** shared by market and order pushes. Add a new push type via `hub.Broadcast(msgType, payload)`; don't open a parallel connection.

## Refactor-period notes

- **Gate.io integration was deleted from scope (2026-08-17).** Do not build multi-exchange abstractions speculatively; Binance (spot + USDT-M perpetual) is the only exchange.
- **M6.5 futures grid decisions**: USDT-M perpetual only; long/short/neutral all three directions; leverage fixed at 1x (params reject >1, field reserved); one-way position mode enforced; historical funding rates pulled for backtest replay; liquidation-distance safety check kept for short/neutral.
- Spot and perpetual are two separate market-data feeds with diverging prices. Perpetual symbols get an explicit suffix (e.g. `CRYPTO.BTC-USDT.PERP`); never mix spot and perp klines.
- `quant-py/strategies/dual_ma.py` is backtest-only. The M6 strategy engine is live; every new runnable strategy must implement `strategy.Strategy` and export `[]grid.ParamField` via `strategy.Register`.
- Before the M9 frontend rewrite, keep new pages on Antd. **Don't half-migrate** — two UI libraries side by side is worse than either alone.
- ExchangeInfo precision validation (tickSize/stepSize/minNotional) is intentionally deferred — current grid uses 8-decimal truncation only.

## Key files

- `PLAN.md` — roadmap, milestone state, decision log. **Keep it in sync with any scope change.**
- `proto/quant.proto` — the only Go↔Python contract.
- `backend-go/cmd/server/main.go` — main wiring.
- `backend-go/internal/api/api.go` — every HTTP route lives here.
- `backend-go/internal/api/strategies.go` — strategy + sub-account API handlers.
- `backend-go/internal/broker/sim.go` — Broker interface definition.
- `backend-go/internal/store/migrations/` — single source of truth for DDL.
- `backend-go/internal/store/strategies.go` — strategy/sub-account/intent-log store methods.
- `backend-go/internal/strategy/registry.go` — strategy registry (kind → Descriptor).
- `backend-go/internal/strategy/grid/engine.go` — pure grid state machine.
- `backend-go/internal/backtest/grid.go` — Go-native grid backtest entry point.
- `backend-go/internal/events/bus.go` — in-process order event bus.
- `quant-py/server.py` + `quant-py/backtest/runner.py` — backtest entry points.
