import { useEffect, useState } from 'react'
import {
  Button,
  Card,
  Col,
  Form,
  InputNumber,
  Layout,
  Row,
  Segmented,
  Select,
  Statistic,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import {
  api,
  type BacktestResult,
  type BacktestRunSummary,
  type Kline,
  type Symbol,
} from '../api/client'
import KlineChart from '../components/KlineChart'
import EquityChart from '../components/EquityChart'
import TopNav, { type Tab } from '../components/TopNav'

const { Header, Content } = Layout
const { Text } = Typography

const INTERVALS = ['1m', '5m', '15m', '1h', '4h', '1d']

interface Props {
  onNav: (t: Tab) => void
}

export default function Backtest({ onNav }: Props) {
  const [symbols, setSymbols] = useState<Symbol[]>([])
  const [runs, setRuns] = useState<BacktestRunSummary[]>([])
  const [result, setResult] = useState<BacktestResult | null>(null)
  const [klines, setKlines] = useState<Kline[]>([])
  const [loading, setLoading] = useState(false)
  const [form] = Form.useForm()

  useEffect(() => {
    api.listSymbols().then(setSymbols).catch(() => {})
    refreshRuns()
  }, [])

  const refreshRuns = () => {
    api.listRuns().then(setRuns).catch(() => {})
  }

  // 展示某次回测结果 + 对应 K 线
  const showResult = async (r: BacktestResult) => {
    setResult(r)
    try {
      const ks = await api.getKlines(r.symbol, r.interval, 500)
      setKlines(ks)
    } catch {
      setKlines([])
    }
  }

  const onRun = async (values: {
    symbol: string
    interval: string
    strategy: string
    fast: number
    slow: number
    initialCash: number
    commission: number
  }) => {
    setLoading(true)
    try {
      const res = await api.runBacktest({
        symbol: values.symbol,
        strategy: values.strategy,
        interval: values.interval,
        params: { fast: String(values.fast), slow: String(values.slow) },
        initialCash: values.initialCash,
        commission: values.commission,
        limit: 500,
      })
      await showResult(res)
      refreshRuns()
      message.success('回测完成')
    } catch (e) {
      message.error('回测失败：' + String(e))
    }
    setLoading(false)
  }

  const m = result?.metrics
  const pct = (v: number) => `${(v * 100).toFixed(2)}%`

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ background: '#001529', display: 'flex', alignItems: 'center' }}>
        <TopNav active="backtest" onNav={onNav} />
      </Header>
      <Content style={{ padding: 16 }}>
        <Row gutter={16}>
          {/* 左：配置 + 历史 */}
          <Col span={7}>
            <Card title="回测配置" size="small">
              <Form
                form={form}
                layout="vertical"
                onFinish={onRun}
                initialValues={{
                  symbol: 'CRYPTO.BTC-USDT',
                  interval: '1h',
                  strategy: 'dual_ma',
                  fast: 10,
                  slow: 30,
                  initialCash: 10000,
                  commission: 0.001,
                }}
              >
                <Form.Item name="symbol" label="币种">
                  <Select
                    options={symbols.map((s) => ({
                      value: s.symbol,
                      label: `${s.name} (${s.symbol.replace('CRYPTO.', '')})`,
                    }))}
                  />
                </Form.Item>
                <Form.Item name="interval" label="周期">
                  <Segmented options={INTERVALS} />
                </Form.Item>
                <Form.Item name="strategy" label="策略">
                  <Select options={[{ value: 'dual_ma', label: '双均线 (Dual MA)' }]} />
                </Form.Item>
                <Row gutter={8}>
                  <Col span={12}>
                    <Form.Item name="fast" label="快线周期">
                      <InputNumber min={2} max={200} style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                  <Col span={12}>
                    <Form.Item name="slow" label="慢线周期">
                      <InputNumber min={3} max={400} style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                </Row>
                <Row gutter={8}>
                  <Col span={12}>
                    <Form.Item name="initialCash" label="初始资金">
                      <InputNumber min={100} style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                  <Col span={12}>
                    <Form.Item name="commission" label="手续费率">
                      <InputNumber min={0} max={0.01} step={0.0001} style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                </Row>
                <Button type="primary" htmlType="submit" loading={loading} block>
                  运行回测
                </Button>
              </Form>
            </Card>

            <Card title="历史回测" size="small" style={{ marginTop: 16 }}>
              <Table
                size="small"
                pagination={false}
                rowKey="id"
                dataSource={runs}
                onRow={(r) => ({
                  onClick: () => api.getRun(r.id).then(showResult),
                  style: { cursor: 'pointer' },
                })}
                columns={[
                  { title: 'ID', dataIndex: 'id', width: 50 },
                  {
                    title: '币种',
                    dataIndex: 'symbol',
                    render: (s: string) => s.replace('CRYPTO.', ''),
                  },
                  { title: '周期', dataIndex: 'interval', width: 60 },
                  {
                    title: '收益',
                    dataIndex: 'metrics',
                    width: 80,
                    align: 'right',
                    render: (mm: BacktestRunSummary['metrics']) => (
                      <Tag color={mm.totalReturn >= 0 ? 'green' : 'red'} style={{ margin: 0 }}>
                        {pct(mm.totalReturn)}
                      </Tag>
                    ),
                  },
                ]}
              />
            </Card>
          </Col>

          {/* 右：结果 */}
          <Col span={17}>
            <Card
              title={
                result ? (
                  <Text strong>
                    {result.symbol.replace('CRYPTO.', '')} · {result.strategy} · {result.interval}
                  </Text>
                ) : (
                  '回测结果'
                )
              }
              size="small"
            >
              {!result ? (
                <Text type="secondary">运行回测或点击历史记录查看结果</Text>
              ) : (
                <>
                  <Row gutter={16} style={{ marginBottom: 16 }}>
                    <Col span={4}>
                      <Statistic
                        title="总收益"
                        value={pct(m!.totalReturn)}
                        valueStyle={{ color: m!.totalReturn >= 0 ? '#3f8600' : '#cf1322' }}
                      />
                    </Col>
                    <Col span={4}>
                      <Statistic title="年化" value={pct(m!.annualReturn)} />
                    </Col>
                    <Col span={4}>
                      <Statistic title="最大回撤" value={pct(m!.maxDrawdown)} />
                    </Col>
                    <Col span={4}>
                      <Statistic title="夏普" value={m!.sharpe.toFixed(2)} />
                    </Col>
                    <Col span={4}>
                      <Statistic title="成交数" value={m!.tradeCount} />
                    </Col>
                    <Col span={4}>
                      <Statistic title="胜率" value={pct(m!.winRate)} />
                    </Col>
                  </Row>

                  <Text type="secondary">净值曲线</Text>
                  <EquityChart equity={result.equityCurve} />

                  <Text type="secondary" style={{ marginTop: 12, display: 'block' }}>
                    K 线 + 买卖点（B 买 / S 卖）
                  </Text>
                  <KlineChart klines={klines} trades={result.trades} />
                </>
              )}
            </Card>
          </Col>
        </Row>
      </Content>
    </Layout>
  )
}
