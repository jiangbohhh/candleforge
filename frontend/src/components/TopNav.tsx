import { Segmented, Typography } from 'antd'

const { Title, Text } = Typography

export type Tab = 'market' | 'backtest' | 'trading' | 'strategies'

interface Props {
  active: Tab
  onNav: (t: Tab) => void
}

// TopNav 是行情/回测/交易的顶部导航，复用在各页 Header。
export default function TopNav({ active, onNav }: Props) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', width: '100%' }}>
      <Title level={3} style={{ color: '#fff', margin: 0 }}>
        🕯️🔨 CandleForge
      </Title>
      <Segmented
        style={{ marginLeft: 24 }}
        value={active}
        onChange={(v) => onNav(v as Tab)}
        options={[
          { label: '行情', value: 'market' },
          { label: '回测', value: 'backtest' },
          { label: '交易', value: 'trading' },
          { label: '策略', value: 'strategies' },
        ]}
      />
      <Text style={{ color: '#8c8c8c', marginLeft: 'auto' }}>
        加密量化交易系统
      </Text>
    </div>
  )
}
