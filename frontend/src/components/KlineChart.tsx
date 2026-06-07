import { useEffect, useRef } from 'react'
import { createChart, CandlestickSeries, type IChartApi } from 'lightweight-charts'
import type { Kline } from '../api/client'

interface Props {
  klines: Kline[]
}

// KlineChart 用 TradingView Lightweight Charts 渲染蜡烛图。
export default function KlineChart({ klines }: Props) {
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const seriesRef = useRef<any>(null)

  // 初始化图表（一次）
  useEffect(() => {
    if (!containerRef.current) return
    const chart = createChart(containerRef.current, {
      height: 420,
      layout: { background: { color: '#ffffff' }, textColor: '#333' },
      grid: {
        vertLines: { color: '#f0f0f0' },
        horzLines: { color: '#f0f0f0' },
      },
      timeScale: { timeVisible: true, secondsVisible: false },
    })
    const series = chart.addSeries(CandlestickSeries, {
      upColor: '#26a69a',
      downColor: '#ef5350',
      borderVisible: false,
      wickUpColor: '#26a69a',
      wickDownColor: '#ef5350',
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

  // 数据变化时更新
  useEffect(() => {
    if (!seriesRef.current) return
    const data = klines.map((k) => ({
      time: Math.floor(k.openTime / 1000) as never, // 秒级时间戳
      open: k.open,
      high: k.high,
      low: k.low,
      close: k.close,
    }))
    seriesRef.current.setData(data)
    chartRef.current?.timeScale().fitContent()
  }, [klines])

  return <div ref={containerRef} style={{ width: '100%' }} />
}
