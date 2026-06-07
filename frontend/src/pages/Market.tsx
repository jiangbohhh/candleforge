import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AutoComplete,
  Card,
  Layout,
  Segmented,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import { api, connectWS, type Kline, type Symbol, type Ticker } from '../api/client'
import KlineChart from '../components/KlineChart'

const { Header, Content, Sider } = Layout
const { Title, Text } = Typography

const INTERVALS = ['1m', '5m', '15m', '1h', '4h', '1d']

export default function Market() {
  const [symbols, setSymbols] = useState<Symbol[]>([])
  const [watchlist, setWatchlist] = useState<string[]>([])
  const [tickers, setTickers] = useState<Record<string, Ticker>>({})
  const [selected, setSelected] = useState<string>('CRYPTO.BTC-USDT')
  const [interval, setInterval] = useState<string>('1h')
  const [klines, setKlines] = useState<Kline[]>([])
  const [search, setSearch] = useState('')
  const tickersRef = useRef(tickers)
  tickersRef.current = tickers

  // 初始化：标的、自选、首屏 tickers
  useEffect(() => {
    api.listSymbols().then(setSymbols).catch(() => {})
    api.getWatchlist().then(setWatchlist).catch(() => {})
    api.getTickers().then((ts) => {
      const m: Record<string, Ticker> = {}
      ts.forEach((t) => (m[t.symbol] = t))
      setTickers(m)
    })
  }, [])

  // WebSocket 实时更新
  useEffect(() => {
    const disconnect = connectWS((t) => {
      setTickers((prev) => ({ ...prev, [t.symbol]: t }))
    })
    return disconnect
  }, [])

  // 选中标的/周期变化 → 拉 K 线
  useEffect(() => {
    if (!selected) return
    api.getKlines(selected, interval, 500).then(setKlines).catch(() => {})
  }, [selected, interval])

  const addWatch = async (symbol: string) => {
    try {
      await api.addWatch(symbol)
      setWatchlist(await api.getWatchlist())
      message.success(`已添加 ${symbol}`)
    } catch {
      message.error('添加失败')
    }
  }

  const removeWatch = async (symbol: string) => {
    try {
      await api.removeWatch(symbol)
      setWatchlist(await api.getWatchlist())
      message.success(`已移除 ${symbol}`)
    } catch {
      message.error('移除失败')
    }
  }

  const symbolName = useMemo(() => {
    const m: Record<string, string> = {}
    symbols.forEach((s) => (m[s.symbol] = s.name || s.symbol))
    return m
  }, [symbols])

  // 自选表格数据
  const watchData = watchlist.map((sym) => {
    const t = tickers[sym]
    const changePct =
      t && t.open > 0 ? ((t.price - t.open) / t.open) * 100 : 0
    return { key: sym, symbol: sym, name: symbolName[sym] ?? sym, price: t?.price, changePct }
  })

  const searchOptions = symbols
    .filter((s) => s.symbol.toLowerCase().includes(search.toLowerCase()))
    .map((s) => ({ value: s.symbol, label: `${s.symbol} (${s.name})` }))

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ background: '#001529', display: 'flex', alignItems: 'center' }}>
        <Title level={3} style={{ color: '#fff', margin: 0 }}>
          🕯️🔨 CandleForge
        </Title>
        <Text style={{ color: '#8c8c8c', marginLeft: 16 }}>行情看板</Text>
      </Header>
      <Layout>
        <Sider width={340} style={{ background: '#fff', padding: 16 }}>
          <AutoComplete
            style={{ width: '100%', marginBottom: 12 }}
            options={searchOptions}
            placeholder="搜索币种添加自选"
            value={search}
            onChange={setSearch}
            onSelect={(v) => {
              addWatch(v)
              setSearch('')
            }}
          />
          <Table
            size="small"
            pagination={false}
            dataSource={watchData}
            onRow={(r) => ({ onClick: () => setSelected(r.symbol) })}
            rowClassName={(r) => (r.symbol === selected ? 'ant-table-row-selected' : '')}
            columns={[
              {
                title: '币种',
                dataIndex: 'name',
                render: (_: string, r) => (
                  <Space direction="vertical" size={0}>
                    <Text strong>{r.name}</Text>
                    <Text type="secondary" style={{ fontSize: 11 }}>
                      {r.symbol.replace('CRYPTO.', '')}
                    </Text>
                  </Space>
                ),
              },
              {
                title: '价格',
                dataIndex: 'price',
                align: 'right',
                render: (p?: number) => (p != null ? p.toLocaleString() : '—'),
              },
              {
                title: '24h',
                dataIndex: 'changePct',
                align: 'right',
                render: (c: number) => (
                  <Tag color={c >= 0 ? 'green' : 'red'} style={{ margin: 0 }}>
                    {c >= 0 ? '+' : ''}
                    {c.toFixed(2)}%
                  </Tag>
                ),
              },
              {
                title: '',
                dataIndex: 'op',
                render: (_: unknown, r) => (
                  <a
                    onClick={(e) => {
                      e.stopPropagation()
                      removeWatch(r.symbol)
                    }}
                  >
                    删
                  </a>
                ),
              },
            ]}
          />
        </Sider>
        <Content style={{ padding: 16 }}>
          <Card
            title={
              <Space>
                <Text strong>{symbolName[selected] ?? selected}</Text>
                <Text type="secondary">{selected.replace('CRYPTO.', '')}</Text>
              </Space>
            }
            extra={
              <Segmented
                options={INTERVALS}
                value={interval}
                onChange={(v) => setInterval(v as string)}
              />
            }
          >
            <Space size="large" style={{ marginBottom: 16 }}>
              <Statistic
                title="最新价"
                value={tickers[selected]?.price ?? klines[klines.length - 1]?.close ?? 0}
                precision={2}
              />
              <Statistic title="24h 高" value={tickers[selected]?.high ?? 0} precision={2} />
              <Statistic title="24h 低" value={tickers[selected]?.low ?? 0} precision={2} />
            </Space>
            <KlineChart klines={klines} />
          </Card>
        </Content>
      </Layout>
    </Layout>
  )
}
