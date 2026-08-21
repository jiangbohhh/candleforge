import axios from 'axios'

const API_BASE = import.meta.env.VITE_API_BASE ?? 'http://localhost:8080'
// 鉴权 token（与后端 AUTH_TOKEN 同值）；留空 = 不鉴权（本地开发）。
const AUTH_TOKEN = import.meta.env.VITE_AUTH_TOKEN ?? ''

export const http = axios.create({
  baseURL: API_BASE,
  headers: AUTH_TOKEN ? { 'X-Auth-Token': AUTH_TOKEN } : {},
})

// ── 类型 ──
export interface Symbol {
  symbol: string
  market: string
  baseAsset: string
  quoteAsset: string
  nativeSymbol: string
  name: string
  status: string
}

export interface Kline {
  symbol: string
  interval: string
  openTime: number
  open: number
  high: number
  low: number
  close: number
  volume: number
  closeTime: number
  tradeNum: number
}

export interface Ticker {
  symbol: string
  price: number
  open: number
  high: number
  low: number
  volume: number
  time: number
}

// ── REST ──
// ── 回测 ──
export interface BacktestMetrics {
  totalReturn: number
  annualReturn: number
  maxDrawdown: number
  sharpe: number
  tradeCount: number | string // protojson int64 → string
  winRate: number
  fundingCost?: number // 永续：资金费净支出（正=付出，负=收入）
  liquidated?: boolean // 永续：回测期间是否触发强平
}

// ── 策略 schema（/api/strategies/schemas，动态渲染参数表单）──
export interface ParamField {
  name: string
  label: string
  type: 'number' | 'integer' | 'select' | 'boolean'
  default?: number | string | boolean
  min?: number
  max?: number
  options?: string[]
  required: boolean
}

export interface StrategySchema {
  kind: string
  name: string
  runnable: boolean
  backtestEngine: 'go' | 'python'
  params: ParamField[]
}

export interface EquityPoint {
  time: number | string // protojson 的 int64 可能是 string
  value: number
}

export interface BacktestTrade {
  time: number | string
  side: 'buy' | 'sell'
  price: number
  quantity: number
}

export interface BacktestResult {
  id: number
  symbol: string
  strategy: string
  interval: string
  metrics: BacktestMetrics
  equityCurve: EquityPoint[]
  trades: BacktestTrade[]
}

export interface BacktestRunSummary {
  id: number
  symbol: string
  strategy: string
  interval: string
  metrics: BacktestMetrics
  initialCash: number
  commission: number
  createdAt: string
}

export interface RunBacktestBody {
  symbol: string
  strategy: string
  interval: string
  params: Record<string, string | number | boolean>
  initialCash: number
  commission: number
  limit?: number
}

// ── M3 模拟盘 / M4 实盘 ──
export type AccountName = 'default' | 'live' | string

export interface Account {
  id: number
  name: string
  kind: string
  broker: string
  cash: number
  createdAt: string
}

export interface BrokerInfo {
  name: AccountName
  kind: string // sim | live
  broker: string // sim | binance
  available: boolean
  env?: string // testnet | mainnet (live only)
}

export type OrderSide = 'buy' | 'sell'
export type OrderType = 'market' | 'limit'
export type OrderStatus = 'new' | 'partially_filled' | 'filled' | 'canceled' | 'rejected'

export interface Order {
  id: number
  accountId: number
  symbol: string
  side: OrderSide
  orderType: OrderType
  price: number
  quantity: number
  filledQty: number
  status: OrderStatus
  createdAt: string
}

export interface Position {
  id: number
  accountId: number
  symbol: string
  quantity: number
  avgPrice: number
  marketPrice: number
  unrealized: number
}

export interface Trade {
  id: number
  orderId: number
  symbol: string
  side: OrderSide
  price: number
  quantity: number
  fee: number
  tradedAt: string
}

export interface AccountSummary {
  account: Account
  totalValue: number
  unrealized: number
  positions: Position[]
}

export interface RiskStatus {
  halted: boolean
  haltReason: string
  maxNotional: number
}

export interface PlaceOrderBody {
  symbol: string
  side: OrderSide
  type: OrderType
  price?: number
  quantity: number
}

export const api = {
  listSymbols: () => http.get<Symbol[]>('/api/symbols').then((r) => r.data),
  getKlines: (symbol: string, interval: string, limit = 500) =>
    http
      .get<Kline[]>('/api/klines', { params: { symbol, interval, limit } })
      .then((r) => r.data),
  getTickers: () => http.get<Ticker[]>('/api/tickers').then((r) => r.data),
  getWatchlist: () => http.get<string[]>('/api/watchlist').then((r) => r.data),
  addWatch: (symbol: string) => http.post('/api/watchlist', { symbol }),
  removeWatch: (symbol: string) =>
    http.delete(`/api/watchlist/${encodeURIComponent(symbol)}`),

  runBacktest: (body: RunBacktestBody) =>
    http.post<BacktestResult>('/api/backtest', body).then((r) => r.data),
  listStrategySchemas: () =>
    http.get<StrategySchema[]>('/api/strategies/schemas').then((r) => r.data),
  listRuns: () =>
    http.get<BacktestRunSummary[]>('/api/backtest/runs').then((r) => r.data),
  getRun: (id: number) =>
    http.get<BacktestResult>(`/api/backtest/runs/${id}`).then((r) => r.data),

  // M3 / M4
  getBrokers: () => http.get<BrokerInfo[]>('/api/brokers').then((r) => r.data),
  getAccountSummary: (account?: AccountName) =>
    http
      .get<AccountSummary>('/api/account/summary', { params: { account } })
      .then((r) => r.data),
  listOrders: (account?: AccountName) =>
    http.get<Order[]>('/api/orders', { params: { account } }).then((r) => r.data),
  listPositions: (account?: AccountName) =>
    http
      .get<Position[]>('/api/positions', { params: { account } })
      .then((r) => r.data),
  listTrades: (account?: AccountName) =>
    http.get<Trade[]>('/api/trades', { params: { account } }).then((r) => r.data),
  placeOrder: (body: PlaceOrderBody, account?: AccountName) =>
    http
      .post<Order>('/api/orders', body, { params: { account } })
      .then((r) => r.data),
  cancelOrder: (id: number, account?: AccountName) =>
    http.delete(`/api/orders/${id}`, { params: { account } }),

  getRiskStatus: () => http.get<RiskStatus>('/api/risk/status').then((r) => r.data),
  setRiskHalt: (halt: boolean, reason = '') =>
    http.post<RiskStatus>('/api/risk/halt', { halt, reason }).then((r) => r.data),
}

// ── WebSocket（实时 ticker + 订单/成交事件）──
export interface StrategyEvent {
  id: number
  status: 'running' | 'stopped' | 'error'
  state: { inventory?: number; realizedPnl?: number; matchedCount?: number }
  lastError: string
}

export interface WSHandlers {
  onTicker?: (t: Ticker) => void
  onOrder?: (o: Order) => void
  onTrade?: (t: Partial<Trade> & { orderId: number; symbol: string; side: OrderSide; price: number; quantity: number }) => void
  onStrategy?: (s: StrategyEvent) => void
}

export function connectWS(handlers: WSHandlers): () => void {
  const wsBase = API_BASE.replace(/^http/, 'ws')
  let ws: WebSocket | null = null
  let closed = false

  const connect = () => {
    // 配了 token 则用查询参数鉴权（浏览器无法自定义 WS 握手头）。
    const wsURL = AUTH_TOKEN
      ? `${wsBase}/ws?token=${encodeURIComponent(AUTH_TOKEN)}`
      : `${wsBase}/ws`
    ws = new WebSocket(wsURL)
    ws.onmessage = (ev) => {
      try {
        const msg: { type: string; data: unknown } = JSON.parse(ev.data)
        switch (msg.type) {
          case 'ticker':
            handlers.onTicker?.(msg.data as Ticker)
            break
          case 'order':
            handlers.onOrder?.(msg.data as Order)
            break
          case 'trade':
            handlers.onTrade?.(msg.data as Parameters<NonNullable<WSHandlers['onTrade']>>[0])
            break
          case 'strategy':
            handlers.onStrategy?.(msg.data as StrategyEvent)
            break
        }
      } catch {
        // ignore malformed
      }
    }
    ws.onclose = () => {
      if (!closed) setTimeout(connect, 2000) // 自动重连
    }
  }
  connect()

  return () => {
    closed = true
    ws?.close()
  }
}
