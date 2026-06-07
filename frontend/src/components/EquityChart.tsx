import { useEffect, useRef } from 'react'
import { createChart, LineSeries, type IChartApi } from 'lightweight-charts'
import type { EquityPoint } from '../api/client'

interface Props {
  equity: EquityPoint[]
}

// EquityChart 渲染净值曲线（折线）。
export default function EquityChart({ equity }: Props) {
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const seriesRef = useRef<any>(null)

  useEffect(() => {
    if (!containerRef.current) return
    const chart = createChart(containerRef.current, {
      height: 240,
      layout: { background: { color: '#ffffff' }, textColor: '#333' },
      grid: {
        vertLines: { color: '#f5f5f5' },
        horzLines: { color: '#f5f5f5' },
      },
      timeScale: { timeVisible: true, secondsVisible: false },
    })
    const series = chart.addSeries(LineSeries, {
      color: '#1677ff',
      lineWidth: 2,
    })
    chartRef.current = chart
    seriesRef.current = series

    const handleResize = () => {
      if (containerRef.current) {
        chart.applyOptions({ width: containerRef.current.clientWidth })
      }
    }
    handleResize()
    window.addEventListener('resize', handleResize)
    return () => {
      window.removeEventListener('resize', handleResize)
      chart.remove()
    }
  }, [])

  useEffect(() => {
    if (!seriesRef.current) return
    // 去重（同一秒可能多点）并排序
    const seen = new Set<number>()
    const data = equity
      .map((p) => ({ time: Math.floor(Number(p.time) / 1000), value: p.value }))
      .filter((p) => {
        if (seen.has(p.time)) return false
        seen.add(p.time)
        return true
      })
      .sort((a, b) => a.time - b.time)
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      .map((p) => ({ time: p.time as any, value: p.value }))
    seriesRef.current.setData(data)
    chartRef.current?.timeScale().fitContent()
  }, [equity])

  return <div ref={containerRef} style={{ width: '100%' }} />
}
