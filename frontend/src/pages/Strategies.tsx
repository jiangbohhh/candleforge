import { useEffect, useState } from 'react'
import {
  Badge,
  Button,
  Card,
  Col,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Layout,
  Modal,
  Row,
  Select,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import {
  PlusOutlined,
  PlayCircleOutlined,
  PauseCircleOutlined,
  DeleteOutlined,
} from '@ant-design/icons'
import { http } from '../api/client'
import TopNav, { type Tab } from '../components/TopNav'

const { Header, Content } = Layout
const { Text } = Typography

interface ParamField {
  name: string
  label: string
  type: 'number' | 'integer' | 'select' | 'boolean'
  default?: number | string | boolean
  min?: number
  max?: number
  options?: string[]
  required: boolean
}

interface StrategySchema {
  kind: string
  name: string
  runnable: boolean
  backtestEngine: 'go' | 'python'
  params: ParamField[]
}

interface StrategyState {
  sides?: string[]
  inventory?: number
  realizedPnl?: number
  matchedCount?: number
}

interface Strategy {
  id: number
  name: string
  kind: string
  accountId: number
  symbol: string
  marketType: string
  direction: string
  leverage: number
  params: Record<string, unknown>
  status: 'stopped' | 'running' | 'error'
  state: StrategyState
  lastError: string
  createdAt: string
  updatedAt: string
}

interface Props {
  onNav: (t: Tab) => void
}

const STATUS_COLOR: Record<string, string> = {
  running: 'green',
  stopped: 'default',
  error: 'red',
}

export default function Strategies({ onNav }: Props) {
  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [schemas, setSchemas] = useState<StrategySchema[]>([])
  const [loading, setLoading] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [selectedSchema, setSelectedSchema] = useState<StrategySchema | null>(null)
  const [form] = Form.useForm()
  const [stopModal, setStopModal] = useState<{ id: number; name: string } | null>(null)

  // 获取 schema 列表
  useEffect(() => {
    http.get<StrategySchema[]>('/api/strategies/schemas').then(r => setSchemas(r.data))
  }, [])

  // 加载策略列表
  const reload = async () => {
    setLoading(true)
    try {
      const r = await http.get<Strategy[]>('/api/strategies')
      setStrategies(r.data ?? [])
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { reload() }, [])

  // WebSocket 实时推送
  useEffect(() => {
    const wsBase = (import.meta.env.VITE_API_BASE ?? 'http://localhost:8080')
      .replace(/^http/, 'ws')
    const sock = new WebSocket(`${wsBase}/ws`)
    sock.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data)
        if (msg.type === 'strategy') {
          setStrategies(prev => prev.map(s =>
            s.id === msg.data.id ? { ...s, ...msg.data } : s
          ))
        }
      } catch { /* ignore */ }
    }
    return () => sock.close()
  }, [])

  const handleCreate = async () => {
    try {
      const vals = await form.validateFields()
      const { name, kind, symbol, parentAccount, allocation, ...paramVals } = vals
      await http.post('/api/strategies', {
        name,
        kind,
        symbol,
        parentAccount: parentAccount || 'default',
        allocation: allocation ?? 0,
        params: paramVals,
      })
      message.success('策略创建成功')
      setCreateOpen(false)
      form.resetFields()
      reload()
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } } }
      if (err?.response?.data?.error) message.error(err.response.data.error)
    }
  }

  const handleStart = async (id: number) => {
    try {
      await http.post(`/api/strategies/${id}/start`)
      message.success('策略已启动')
      reload()
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } } }
      message.error(err?.response?.data?.error ?? '启动失败')
    }
  }

  const handleStop = async (id: number, liquidate: boolean) => {
    try {
      await http.post(`/api/strategies/${id}/stop`, { liquidate })
      message.success(liquidate ? '策略已停止并清仓' : '策略已停止')
      setStopModal(null)
      reload()
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } } }
      message.error(err?.response?.data?.error ?? '停止失败')
    }
  }

  const handleDelete = async (id: number) => {
    try {
      await http.delete(`/api/strategies/${id}`)
      message.success('已删除')
      reload()
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } } }
      message.error(err?.response?.data?.error ?? '删除失败')
    }
  }

  const renderParamForm = () => {
    if (!selectedSchema) return null
    return selectedSchema.params.map(f => {
      let input: React.ReactNode
      if (f.type === 'select' && f.options) {
        input = (
          <Select options={f.options.map(o => ({ label: o, value: o }))} />
        )
      } else if (f.type === 'integer') {
        input = <InputNumber min={f.min} max={f.max} precision={0} style={{ width: '100%' }} />
      } else {
        input = <InputNumber min={f.min} max={f.max} style={{ width: '100%' }} />
      }
      return (
        <Form.Item
          key={f.name}
          name={f.name}
          label={f.label}
          initialValue={f.default}
          rules={f.required ? [{ required: true, message: `${f.label} 必填` }] : []}
        >
          {input}
        </Form.Item>
      )
    })
  }

  const DIRECTION_LABEL: Record<string, string> = { long: '做多', short: '做空', neutral: '中性' }

  const columns = [
    { title: '名称', dataIndex: 'name', key: 'name' },
    { title: '类型', dataIndex: 'kind', key: 'kind' },
    { title: '交易对', dataIndex: 'symbol', key: 'symbol', render: (v: string) => v.replace('CRYPTO.', '') },
    {
      title: '市场/方向',
      key: 'market',
      render: (_: unknown, r: Strategy) => (
        <Space size={4}>
          <Tag color={r.marketType === 'futures' ? 'purple' : 'blue'}>
            {r.marketType === 'futures' ? '永续' : '现货'}
          </Tag>
          <Tag color={r.direction === 'short' ? 'volcano' : r.direction === 'neutral' ? 'gold' : 'green'}>
            {DIRECTION_LABEL[r.direction] ?? r.direction}
          </Tag>
        </Space>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      render: (status: string) => <Tag color={STATUS_COLOR[status]}>{status}</Tag>,
    },
    {
      title: '持仓',
      key: 'inventory',
      render: (_: unknown, r: Strategy) => r.state?.inventory?.toFixed(4) ?? '-',
    },
    {
      title: '已实现PnL',
      key: 'pnl',
      render: (_: unknown, r: Strategy) => {
        const pnl = r.state?.realizedPnl ?? 0
        return <Text type={pnl >= 0 ? 'success' : 'danger'}>{pnl.toFixed(2)}</Text>
      },
    },
    {
      title: '匹配次数',
      key: 'matched',
      render: (_: unknown, r: Strategy) => r.state?.matchedCount ?? 0,
    },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, r: Strategy) => (
        <Space>
          {r.status !== 'running' ? (
            <Button
              size="small"
              type="primary"
              icon={<PlayCircleOutlined />}
              onClick={() => handleStart(r.id)}
              disabled={r.status === 'running'}
            >
              启动
            </Button>
          ) : (
            <Button
              size="small"
              danger
              icon={<PauseCircleOutlined />}
              onClick={() => setStopModal({ id: r.id, name: r.name })}
            >
              停止
            </Button>
          )}
          <Button
            size="small"
            icon={<DeleteOutlined />}
            disabled={r.status === 'running'}
            onClick={() => handleDelete(r.id)}
          >
            删除
          </Button>
        </Space>
      ),
    },
  ]

  const running = strategies.filter(s => s.status === 'running').length

  return (
    <Layout style={{ minHeight: '100vh', background: '#141414' }}>
      <Header style={{ background: '#1f1f1f', padding: '0 24px', display: 'flex', alignItems: 'center' }}>
        <TopNav active="strategies" onNav={onNav} />
      </Header>
      <Content style={{ padding: '24px' }}>
        <Row gutter={16} style={{ marginBottom: 16 }}>
          <Col span={6}>
            <Card>
              <Statistic title="运行中策略" value={running} suffix={`/ ${strategies.length}`} />
            </Card>
          </Col>
          <Col span={6}>
            <Card>
              <Statistic
                title="累计配对"
                value={strategies.reduce((s, r) => s + (r.state?.matchedCount ?? 0), 0)}
              />
            </Card>
          </Col>
        </Row>

        <Card
          title="策略列表"
          extra={
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
              新建策略
            </Button>
          }
        >
          <Table
            rowKey="id"
            loading={loading}
            dataSource={strategies}
            columns={columns}
            pagination={{ pageSize: 20 }}
            expandable={{
              expandedRowRender: (r: Strategy) => (
                <Descriptions size="small" column={3}>
                  <Descriptions.Item label="错误">{r.lastError || '无'}</Descriptions.Item>
                  <Descriptions.Item label="子账户ID">{r.accountId}</Descriptions.Item>
                  <Descriptions.Item label="更新时间">{new Date(r.updatedAt).toLocaleString()}</Descriptions.Item>
                </Descriptions>
              ),
            }}
          />
        </Card>

        {/* 创建策略 Modal */}
        <Modal
          title="新建策略"
          open={createOpen}
          onOk={handleCreate}
          onCancel={() => { setCreateOpen(false); form.resetFields() }}
          width={600}
        >
          <Form form={form} layout="vertical">
            <Form.Item name="name" label="策略名称" rules={[{ required: true }]}>
              <Input placeholder="我的BTC网格" />
            </Form.Item>
            <Form.Item name="kind" label="策略类型" rules={[{ required: true }]}>
              <Select
                options={schemas.filter(s => s.runnable).map(s => ({ label: s.name, value: s.kind }))}
                onChange={(v) => setSelectedSchema(schemas.find(s => s.kind === v) ?? null)}
                placeholder="选择策略类型"
              />
            </Form.Item>
            <Form.Item
              name="symbol"
              label="交易对"
              extra="现货用 CRYPTO.BTC-USDT；永续合约（marketType=futures）用 CRYPTO.BTC-USDT.PERP"
              rules={[{ required: true }]}
            >
              <Input placeholder="CRYPTO.BTC-USDT / CRYPTO.BTC-USDT.PERP" />
            </Form.Item>
            <Form.Item name="parentAccount" label="父账户" initialValue="default">
              <Input placeholder="default" />
            </Form.Item>
            <Form.Item name="allocation" label="划拨金额(USDT)">
              <InputNumber min={0} style={{ width: '100%' }} />
            </Form.Item>
            {renderParamForm()}
          </Form>
        </Modal>

        {/* 停止确认 Modal */}
        <Modal
          title={`停止策略 "${stopModal?.name}"`}
          open={!!stopModal}
          footer={[
            <Button key="cancel" onClick={() => setStopModal(null)}>取消</Button>,
            <Button key="stop" onClick={() => stopModal && handleStop(stopModal.id, false)}>
              停止（保留持仓）
            </Button>,
            <Button key="liquidate" danger onClick={() => stopModal && handleStop(stopModal.id, true)}>
              停止并清仓
            </Button>,
          ]}
          onCancel={() => setStopModal(null)}
        >
          <p>确认停止策略？停止后可选择保留持仓或市价清仓。</p>
          <Badge status={stopModal ? 'processing' : 'default'} text={`策略 #${stopModal?.id}`} />
        </Modal>
      </Content>
    </Layout>
  )
}
