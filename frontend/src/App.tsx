import { useState } from 'react'
import { Button, Card, Space, Tag, Typography } from 'antd'
import axios from 'axios'

const { Title, Text } = Typography

// 后端基址：开发期默认直连 Go 服务 8080。
const API_BASE = import.meta.env.VITE_API_BASE ?? 'http://localhost:8080'

type CheckResult = { ok: boolean; detail: string }

function App() {
  const [health, setHealth] = useState<CheckResult | null>(null)
  const [quant, setQuant] = useState<CheckResult | null>(null)
  const [loading, setLoading] = useState(false)

  const runChecks = async () => {
    setLoading(true)
    try {
      const h = await axios.get(`${API_BASE}/healthz`)
      setHealth({ ok: true, detail: JSON.stringify(h.data) })
    } catch (e) {
      setHealth({ ok: false, detail: String(e) })
    }
    try {
      const q = await axios.get(`${API_BASE}/api/ping-quant`)
      setQuant({ ok: true, detail: JSON.stringify(q.data) })
    } catch (e) {
      setQuant({ ok: false, detail: String(e) })
    }
    setLoading(false)
  }

  const renderResult = (label: string, r: CheckResult | null) => (
    <Card size="small" title={label} style={{ marginBottom: 12 }}>
      {r === null ? (
        <Tag>未检查</Tag>
      ) : (
        <Space direction="vertical">
          <Tag color={r.ok ? 'green' : 'red'}>{r.ok ? '连通' : '失败'}</Tag>
          <Text code style={{ fontSize: 12 }}>
            {r.detail}
          </Text>
        </Space>
      )}
    </Card>
  )

  return (
    <div style={{ maxWidth: 680, margin: '40px auto', padding: 24 }}>
      <Title level={2}>🕯️🔨 CandleForge</Title>
      <Text type="secondary">M0 脚手架 · 三端连通自检</Text>
      <div style={{ marginTop: 24 }}>
        <Button type="primary" loading={loading} onClick={runChecks}>
          运行连通检查
        </Button>
      </div>
      <div style={{ marginTop: 24 }}>
        {renderResult('后端 Go /healthz（含 PostgreSQL）', health)}
        {renderResult('Go → Python gRPC /api/ping-quant', quant)}
      </div>
    </div>
  )
}

export default App
