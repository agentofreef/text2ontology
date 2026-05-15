'use client'

import { useState, useMemo } from 'react'
import { BarChart3, PieChart, TrendingUp, Table2 } from 'lucide-react'
import { useTranslations } from 'next-intl'
import ReactEChartsCore from 'echarts-for-react/lib/core'
import * as echarts from 'echarts/core'
import { BarChart, PieChart as EPieChart, LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([BarChart, EPieChart, LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

type ViewMode = 'table' | 'bar' | 'pie' | 'line'

interface ResultViewerProps {
  data: string // JSON string of rows
  maxRows?: number
  initialMode?: ViewMode
}

export function ResultViewer({ data, maxRows = 50, initialMode }: ResultViewerProps) {
  const [mode, setMode] = useState<ViewMode>(initialMode || 'table')
  const t = useTranslations('ui')
  const accentColor = '#0A0A0A'

  const parsed = useMemo(() => {
    try {
      const rows = JSON.parse(data) as Record<string, unknown>[]
      if (!Array.isArray(rows) || rows.length === 0) return null
      const keys = Object.keys(rows[0])
      const cols = keys.map(k => { const m = k.match(/\[(.+)\]$/); return m ? m[1] : k })
      // Detect label column (first text) and value columns (numbers)
      const labelIdx = keys.findIndex(k => typeof rows[0][k] !== 'number')
      const valueIdxs = keys.map((k, i) => ({ key: k, col: cols[i], idx: i })).filter(x => typeof rows[0][x.key] === 'number')
      return { rows, keys, cols, labelIdx: labelIdx >= 0 ? labelIdx : 0, valueIdxs }
    } catch { return null }
  }, [data])

  const chartOption = useMemo(() => {
    if (!parsed) return {}
    const { rows, keys, labelIdx, valueIdxs } = parsed
    const hasChart = valueIdxs.length > 0 && rows.length > 0
    if (!hasChart) return {}
    const labelKey = keys[labelIdx]
    const labels = rows.map(r => String(r[labelKey] ?? ''))

    if (mode === 'pie') {
      // Use first value column
      const vk = valueIdxs[0]
      return {
        tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
        legend: { bottom: 0, type: 'scroll', textStyle: { fontFamily: 'JetBrains Mono, monospace', fontSize: 10 } },
        series: [{
          type: 'pie', radius: ['35%', '65%'], center: ['50%', '45%'],
          label: { formatter: '{b}\n{d}%', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
          data: rows.map(r => ({ name: String(r[labelKey] ?? ''), value: r[vk.key] as number })),
        }],
      }
    }

    // Bar or Line
    return {
      tooltip: { trigger: 'axis', textStyle: { fontFamily: 'JetBrains Mono, monospace', fontSize: 11 } },
      legend: valueIdxs.length > 1 ? {
        bottom: 0, textStyle: { fontFamily: 'JetBrains Mono, monospace', fontSize: 10 },
      } : undefined,
      grid: { left: 60, right: 20, top: 20, bottom: valueIdxs.length > 1 ? 40 : 20 },
      xAxis: {
        type: 'category', data: labels,
        axisLabel: { fontSize: 10, fontFamily: 'JetBrains Mono, monospace', rotate: labels.length > 6 ? 30 : 0 },
      },
      yAxis: {
        type: 'value',
        axisLabel: { fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
      },
      series: valueIdxs.map(vk => ({
        name: vk.col,
        type: mode === 'line' ? 'line' : 'bar',
        data: rows.map(r => r[vk.key] as number),
        itemStyle: mode === 'bar' ? { color: accentColor } : undefined,
        lineStyle: mode === 'line' ? { color: accentColor, width: 2 } : undefined,
        areaStyle: mode === 'line' ? { color: 'rgba(0,0,0,0.04)' } : undefined,
      })),
    }
  }, [mode, parsed])

  if (!parsed) return null
  const { rows, keys, cols, valueIdxs } = parsed
  const hasChart = valueIdxs.length > 0 && rows.length > 0

  const modes: { key: ViewMode; icon: typeof Table2; label: string }[] = [
    { key: 'table', icon: Table2, label: 'TABLE' },
    ...(hasChart ? [
      { key: 'bar' as ViewMode, icon: BarChart3, label: 'BAR' },
      { key: 'pie' as ViewMode, icon: PieChart, label: 'PIE' },
      { key: 'line' as ViewMode, icon: TrendingUp, label: 'LINE' },
    ] : []),
  ]

  return (
    <div>
      {/* Mode switcher */}
      <div className="flex items-center gap-0 mb-1">
        {modes.map(m => (
          <button key={m.key} onClick={() => setMode(m.key)}
            className={`flex items-center gap-1 px-2 py-0.5 font-mono text-[9px] border ${
              mode === m.key ? 'border-ink bg-ink text-white' : 'border-border text-ink-ghost hover:border-ink-muted'
            }`}>
            <m.icon className="h-3 w-3" />{m.label}
          </button>
        ))}
        <span className="font-mono text-[8px] text-ink-ghost ml-2">{t('rows_display', { count: rows.length })}</span>
      </div>

      {/* Table view */}
      {mode === 'table' && (
        <div className="overflow-x-auto max-h-64 overflow-y-auto">
          <table className="w-full">
            <thead className="sticky top-0"><tr className="bg-ink text-white">
              {cols.map((c, ci) => <th key={ci} className="px-3 py-1.5 text-left text-sm whitespace-nowrap font-sans">{c}</th>)}
            </tr></thead>
            <tbody>{rows.slice(0, maxRows).map((row, ri) => (
              <tr key={ri} className="border-b border-border-light">
                {keys.map((key, ci) => {
                  const val = row[key]; const isNum = typeof val === 'number'
                  return <td key={ci} className={`px-3 py-1.5 font-mono text-base ${isNum ? 'text-right text-accent' : 'text-ink-muted'}`}>
                    {val == null ? '' : isNum ? val.toLocaleString() : String(val)}
                  </td>
                })}
              </tr>
            ))}</tbody>
          </table>
          {rows.length > maxRows && <div className="px-3 py-1 font-mono text-sm text-ink-ghost">{t('table_showing', { max: maxRows, total: rows.length })}</div>}
        </div>
      )}

      {/* Chart views */}
      {(mode === 'bar' || mode === 'pie' || mode === 'line') && hasChart && (
        <div style={{ height: 280 }}>
          <ReactEChartsCore echarts={echarts} option={chartOption} style={{ height: '100%' }} />
        </div>
      )}
    </div>
  )
}
