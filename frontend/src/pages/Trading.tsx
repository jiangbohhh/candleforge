import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Col,
  Form,
  Input,
  InputNumber,
  Layout,
  Modal,
  Radio,
  Row,
  Select,
  Space,
  Statistic,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import {
  api,
  connectWS,
  type AccountSummary,
  type Order,
  type OrderSide,
  type OrderType,
  type Position,
  type RiskStatus,
  type Symbol,
  type Ticker,
  type Trade,
} from '../api/client'
import TopNav, { type Tab } from '../components/TopNav'

const { Header, Content } = Layout
const { Text } = Typography

interface Props {
  onNav: (t: Tab) => void
}

export default function Trading({ onNav }: Props) {
  const [symbols, setSymbols] = useState<Symbol[]>([])
  const [tickers, setTickers] = useState<Record<string, Ticker>>({})
  const [summary, setSummary] = useState<AccountSummary | null>(null)
  const [orders, setOrders] = useState<Order[]>([])
  const [positions, setPositions] = useState<Position[]>([])
  const [trades, setTrades] = useState<Trade[]>([])
  const [risk, setRisk] = useState<RiskStatus | null>(null)

  const [form] = Form.useForm()
  const symbol = Form.useWatch('symbol', form) as string | undefined
  const side = (Form.useWatch('side', form) as OrderSide | undefined) ?? 'buy'
  const orderType = (Form.useWatch('type', form) as OrderType | undefined) ?? 'market'
  const quantity = Form.useWatch('quantity', form) as number | undefined
  const limitPrice = Form.useWatch('price', form) as number | undefined

  const tickersRef = useRef(tickers)
  tickersRef.current = tickers

  const refreshAll = async () => {
    const [sum, ords, poss, trs] = await Promise.all([
      api.getAccountSummary().catch(() => null),
      api.listOrders().catch(() => [] as Order[]),
      api.listPositions().catch(() => [] as Position[]),
      api.listTrades().catch(() => [] as Trade[]),
    ])
    if (sum) setSummary(sum)
    setOrders(ords)
    setPositions(poss)
    setTrades(trs)
  }

  useEffect(() => {
    api.listSymbols().then(setSymbols).catch(() => {})
    api.getTickers().then((ts) => {
      const m: Record<string, Ticker> = {}
      ts.forEach((t) => (m[t.symbol] = t))
      setTickers(m)
    })
    api.getRiskStatus().then(setRisk).catch(() => {})
    refreshAll()
  }, [])

  // 实时：ticker 更新价格；order/trade 触发数据刷新
  useEffect(() => {
    const disconnect = connectWS({
      onTicker: (t) => {
        setTickers((prev) => ({ ...prev, [t.symbol]: t }))
      },
      onOrder: () => {
        refreshAll()
      },
      onTrade: () => {
        refreshAll()
      },
    })
    return disconnect
  }, [])

  const symbolOptions = useMemo(
    () =>
      symbols.map((s) => ({
        value: s.symbol,
        label: `${s.symbol.replace('CRYPTO.', '')} ${s.name ? `· ${s.name}` : ''}`,
      })),
    [symbols],
  )

  const currentPrice = symbol ? tickers[symbol]?.price ?? 0 : 0
  const estNotional = (() => {
    if (!quantity || quantity <= 0) return 0
    const px = orderType === 'limit' ? limitPrice ?? 0 : currentPrice
    return px * quantity
  })()

  const submitOrder = async () => {
    try {
      const values = await form.validateFields()
      await api.placeOrder({
        symbol: values.symbol,
        side: values.side,
        type: values.type,
        price: values.type === 'limit' ? Number(values.price) : undefined,
        quantity: Number(values.quantity),
      })
      message.success('下单成功')
      form.resetFields(['quantity', 'price'])
      refreshAll()
    } catch (e) {
      const msg =
        (e as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        (e as { message?: string })?.message
      if (msg) message.error(msg)
    }
  }

  const cancelOrder = async (id: number) => {
    try {
      await api.cancelOrder(id)
      message.success(`已撤单 #${id}`)
      refreshAll()
    } catch {
      message.error('撤单失败')
    }
  }

  const toggleHalt = async (halt: boolean) => {
    try {
      const next = await api.setRiskHalt(halt, halt ? 'manual halt from UI' : '')
      setRisk(next)
      message.success(halt ? '已紧急停止' : '已恢复交易')
    } catch {
      message.error('风控切换失败')
    }
  }

  const confirmHalt = (halt: boolean) => {
    Modal.confirm({
      title: halt ? '确认紧急停止？' : '确认恢复交易？',
      content: halt
        ? '所有新下单请求将被风控拒绝，已成交的不受影响。'
        : '风控将放行下单请求。',
      okText: '确认',
      cancelText: '取消',
      onOk: () => toggleHalt(halt),
    })
  }

  const sideTag = (s: OrderSide) => (
    <Tag color={s === 'buy' ? 'green' : 'red'} style={{ margin: 0 }}>
      {s === 'buy' ? '买入' : '卖出'}
    </Tag>
  )

  const statusTag = (s: Order['status']) => {
    const map: Record<Order['status'], { color: string; text: string }> = {
      new: { color: 'blue', text: '挂单' },
      filled: { color: 'green', text: '已成交' },
      canceled: { color: 'default', text: '已撤销' },
      rejected: { color: 'red', text: '已拒绝' },
    }
    const m = map[s]
    return <Tag color={m.color}>{m.text}</Tag>
  }

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ background: '#001529', display: 'flex', alignItems: 'center' }}>
        <TopNav active="trading" onNav={onNav} />
      </Header>
      <Content style={{ padding: 16 }}>
        {risk?.halted && (
          <Alert
            type="error"
            showIcon
            message={`紧急停止已激活：${risk.haltReason || 'manual'} — 所有新下单将被拒绝`}
            style={{ marginBottom: 12 }}
          />
        )}

        <Row gutter={[12, 12]}>
          {/* 账户总览 */}
          <Col span={24}>
            <Card size="small">
              <Row gutter={16}>
                <Col span={6}>
                  <Statistic
                    title="账户总值 (USDT)"
                    value={summary?.totalValue ?? 0}
                    precision={2}
                  />
                </Col>
                <Col span={6}>
                  <Statistic
                    title="可用现金"
                    value={summary?.account?.cash ?? 0}
                    precision={2}
                  />
                </Col>
                <Col span={6}>
                  <Statistic
                    title="浮动盈亏"
                    value={summary?.unrealized ?? 0}
                    precision={2}
                    valueStyle={{
                      color: (summary?.unrealized ?? 0) >= 0 ? '#3f8600' : '#cf1322',
                    }}
                  />
                </Col>
                <Col span={6}>
                  <Space>
                    <Text>紧急停止</Text>
                    <Switch
                      checked={!!risk?.halted}
                      onChange={(checked) => confirmHalt(checked)}
                    />
                    {risk && (
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        单笔上限 {risk.maxNotional > 0 ? risk.maxNotional.toLocaleString() : '∞'}
                      </Text>
                    )}
                  </Space>
                </Col>
              </Row>
            </Card>
          </Col>

          {/* 下单面板 */}
          <Col span={8}>
            <Card title="下单" size="small">
              <Form
                form={form}
                layout="vertical"
                initialValues={{
                  side: 'buy',
                  type: 'market',
                  symbol: 'CRYPTO.BTC-USDT',
                }}
              >
                <Form.Item name="symbol" label="交易对" rules={[{ required: true }]}>
                  <Select
                    showSearch
                    options={symbolOptions}
                    placeholder="选择交易对"
                    filterOption={(input, option) =>
                      (option?.label as string)?.toLowerCase().includes(input.toLowerCase())
                    }
                  />
                </Form.Item>
                <Form.Item name="side" label="方向">
                  <Radio.Group buttonStyle="solid">
                    <Radio.Button value="buy">买入</Radio.Button>
                    <Radio.Button value="sell">卖出</Radio.Button>
                  </Radio.Group>
                </Form.Item>
                <Form.Item name="type" label="类型">
                  <Radio.Group>
                    <Radio value="market">市价</Radio>
                    <Radio value="limit">限价</Radio>
                  </Radio.Group>
                </Form.Item>
                {orderType === 'limit' && (
                  <Form.Item
                    name="price"
                    label="限价"
                    rules={[{ required: true, message: '请输入限价' }]}
                  >
                    <InputNumber style={{ width: '100%' }} min={0} step={0.01} />
                  </Form.Item>
                )}
                <Form.Item
                  name="quantity"
                  label="数量"
                  rules={[{ required: true, message: '请输入数量' }]}
                >
                  <InputNumber style={{ width: '100%' }} min={0} step={0.001} />
                </Form.Item>
                <Form.Item label="参考价">
                  <Input
                    value={currentPrice ? currentPrice.toFixed(2) : '—'}
                    disabled
                    addonAfter="USDT"
                  />
                </Form.Item>
                <Form.Item label="预计金额">
                  <Input
                    value={estNotional ? estNotional.toFixed(2) : '—'}
                    disabled
                    addonAfter="USDT"
                  />
                </Form.Item>
                <Button
                  type="primary"
                  block
                  danger={side === 'sell'}
                  onClick={submitOrder}
                  disabled={!!risk?.halted}
                >
                  {side === 'buy' ? '买入' : '卖出'} {symbol?.replace('CRYPTO.', '')}
                </Button>
              </Form>
            </Card>
          </Col>

          {/* 持仓 */}
          <Col span={16}>
            <Card title="持仓" size="small">
              <Table
                size="small"
                rowKey="id"
                dataSource={positions}
                pagination={false}
                columns={[
                  {
                    title: '交易对',
                    dataIndex: 'symbol',
                    render: (s: string) => s.replace('CRYPTO.', ''),
                  },
                  {
                    title: '数量',
                    dataIndex: 'quantity',
                    align: 'right',
                    render: (v: number) => v.toFixed(6),
                  },
                  {
                    title: '均价',
                    dataIndex: 'avgPrice',
                    align: 'right',
                    render: (v: number) => v.toFixed(2),
                  },
                  {
                    title: '现价',
                    dataIndex: 'marketPrice',
                    align: 'right',
                    render: (v: number) => (v ? v.toFixed(2) : '—'),
                  },
                  {
                    title: '浮动盈亏',
                    dataIndex: 'unrealized',
                    align: 'right',
                    render: (v: number) => (
                      <Text type={v >= 0 ? 'success' : 'danger'}>{v.toFixed(2)}</Text>
                    ),
                  },
                ]}
              />
            </Card>
          </Col>

          {/* 订单 */}
          <Col span={12}>
            <Card title="订单 (近 100)" size="small">
              <Table
                size="small"
                rowKey="id"
                dataSource={orders}
                pagination={{ pageSize: 8, size: 'small' }}
                columns={[
                  { title: '#', dataIndex: 'id', width: 60 },
                  {
                    title: '交易对',
                    dataIndex: 'symbol',
                    render: (s: string) => s.replace('CRYPTO.', ''),
                  },
                  {
                    title: '方向',
                    dataIndex: 'side',
                    render: (s: OrderSide) => sideTag(s),
                  },
                  { title: '类型', dataIndex: 'orderType' },
                  {
                    title: '价格',
                    dataIndex: 'price',
                    align: 'right',
                    render: (v: number) => (v > 0 ? v.toFixed(2) : '—'),
                  },
                  {
                    title: '数量',
                    dataIndex: 'quantity',
                    align: 'right',
                    render: (v: number) => v.toFixed(6),
                  },
                  {
                    title: '状态',
                    dataIndex: 'status',
                    render: (s: Order['status']) => statusTag(s),
                  },
                  {
                    title: '',
                    render: (_, r: Order) =>
                      r.status === 'new' ? (
                        <a onClick={() => cancelOrder(r.id)}>撤单</a>
                      ) : null,
                  },
                ]}
              />
            </Card>
          </Col>

          {/* 成交 */}
          <Col span={12}>
            <Card title="成交 (近 100)" size="small">
              <Table
                size="small"
                rowKey="id"
                dataSource={trades}
                pagination={{ pageSize: 8, size: 'small' }}
                columns={[
                  { title: '#', dataIndex: 'id', width: 60 },
                  {
                    title: '交易对',
                    dataIndex: 'symbol',
                    render: (s: string) => s.replace('CRYPTO.', ''),
                  },
                  {
                    title: '方向',
                    dataIndex: 'side',
                    render: (s: OrderSide) => sideTag(s),
                  },
                  {
                    title: '价格',
                    dataIndex: 'price',
                    align: 'right',
                    render: (v: number) => v.toFixed(2),
                  },
                  {
                    title: '数量',
                    dataIndex: 'quantity',
                    align: 'right',
                    render: (v: number) => v.toFixed(6),
                  },
                  {
                    title: '时间',
                    dataIndex: 'tradedAt',
                    render: (v: string) => new Date(v).toLocaleString(),
                  },
                ]}
              />
            </Card>
          </Col>
        </Row>
      </Content>
    </Layout>
  )
}
