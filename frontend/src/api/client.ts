import axios from 'axios'

const API_BASE = import.meta.env.VITE_API_BASE ?? 'http://localhost:8080'

export const http = axios.create({ baseURL: API_BASE })

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
}

// ── WebSocket（实时 ticker）──
export interface WSMessage {
  type: string
  data: Ticker
}

export function connectWS(onTicker: (t: Ticker) => void): () => void {
  const wsBase = API_BASE.replace(/^http/, 'ws')
  let ws: WebSocket | null = null
  let closed = false

  const connect = () => {
    ws = new WebSocket(`${wsBase}/ws`)
    ws.onmessage = (ev) => {
      try {
        const msg: WSMessage = JSON.parse(ev.data)
        if (msg.type === 'ticker') onTicker(msg.data)
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
