'use client'

import { useState, useMemo } from 'react'
import { BarChart3, PieChart, TrendingUp, Table2, Download } from 'lucide-react'
import { useTranslations } from 'next-intl'
import ReactEChartsCore from 'echarts-for-react/lib/core'
import * as echarts from 'echarts/core'
import { BarChart, PieChart as EPieChart, LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { ChartSpec } from '@/components/lakehouse-agent/answerChart'

echarts.use([BarChart, EPieChart, LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

type ViewMode = 'table' | 'bar' | 'pie' | 'line'

interface ResultViewerProps {
  data: string // JSON string of rows
  maxRows?: number
  initialMode?: ViewMode
  /**
   * Chart schema authored by the AI (图表表达式). When present it OVERRIDES the
   * auto X/Y column detection — the AI, which understands the question, decides
   * which column is the axis / measure / series and which chart type fits. The
   * data still comes from `data` (this result's rows); the schema only names
   * columns, never values.
   */
  chartSpec?: ChartSpec
}

const num = (v: unknown): number => {
  if (typeof v === 'number') return Number.isFinite(v) ? v : 0
  if (typeof v === 'string') {
    const n = Number(v.replace(/,/g, '').trim())
    return Number.isFinite(n) ? n : 0
  }
  return 0
}

// displayName strips a trailing "[alias]" suffix some result columns carry.
const displayName = (k: string): string => {
  const m = k.match(/\[(.+)\]$/)
  return m ? m[1] : k
}

export function ResultViewer({ data, maxRows = 50, initialMode, chartSpec }: ResultViewerProps) {
  const t = useTranslations('ui')
  const accentColor = '#0A0A0A'

  const parsed = useMemo(() => {
    try {
      const rows = JSON.parse(data) as Record<string, unknown>[]
      if (!Array.isArray(rows) || rows.length === 0) return null
      const keys = Object.keys(rows[0])
      const cols = keys.map(displayName)
      const labelIdx = keys.findIndex(k => typeof rows[0][k] !== 'number')
      const valueKeys = keys.filter(k => typeof rows[0][k] === 'number')
      return { rows, keys, cols, labelIdx: labelIdx >= 0 ? labelIdx : 0, valueKeys }
    } catch { return null }
  }, [data])

  // Resolve a requested column name (from the AI schema) to an actual row key.
  // Tolerant: exact key, exact display name, then unambiguous case-insensitive
  // / substring match — mirrors dataTemplate.resolveColumn so the AI naming the
  // column slightly differently still binds.
  const resolveKey = useMemo(() => {
    return (requested: string): string | null => {
      if (!parsed || !requested) return null
      const { keys, cols } = parsed
      if (keys.includes(requested)) return requested
      const di = cols.indexOf(requested)
      if (di >= 0) return keys[di]
      const lower = requested.toLowerCase()
      const ciKey = keys.filter(k => k.toLowerCase() === lower)
      if (ciKey.length === 1) return ciKey[0]
      const ciCol = cols.findIndex(c => c.toLowerCase() === lower)
      if (ciCol >= 0) return keys[ciCol]
      const sub = keys.filter(k => {
        const kl = k.toLowerCase()
        return kl.includes(lower) || lower.includes(kl)
      })
      return sub.length === 1 ? sub[0] : null
    }
  }, [parsed])

  // The resolved axis/measure/series keys: from the AI schema when valid, else
  // the legacy auto-detection (first text column = label, number columns = y).
  const resolved = useMemo(() => {
    if (!parsed) return null
    if (chartSpec) {
      const xKey = resolveKey(chartSpec.x)
      const yKeys = chartSpec.y.map(resolveKey).filter((k): k is string => !!k)
      const seriesKey = chartSpec.series ? resolveKey(chartSpec.series) : null
      if (xKey && yKeys.length > 0) {
        return { xKey, yKeys, seriesKey, fromSpec: true }
      }
    }
    const { keys, labelIdx, valueKeys } = parsed
    return { xKey: keys[labelIdx], yKeys: valueKeys, seriesKey: null as string | null, fromSpec: false }
  }, [parsed, chartSpec, resolveKey])

  const hasChart = !!resolved && resolved.yKeys.length > 0 && !!parsed && parsed.rows.length > 0

  const specType: ViewMode | null = chartSpec
    ? (chartSpec.type === 'area' ? 'line' : chartSpec.type)
    : null
  const [mode, setMode] = useState<ViewMode>(specType ?? (initialMode || 'table'))
  const isArea = chartSpec?.type === 'area' && mode === 'line'

  // Apply chartSpec.filter to source rows before plotting. Each clause names
  // a column (tolerantly resolved) and a literal value; rows must satisfy
  // every clause (AND). Mismatched columns skip silently — same forgiveness
  // posture as x/y/series resolution above. The table view (when toggled
  // off-chart) keeps showing every row, on the principle that filter is a
  // chart-specific scoping hint, not a global slice of the result.
  const filteredChartRows = useMemo(() => {
    if (!parsed) return null
    if (!chartSpec?.filter || chartSpec.filter.length === 0) return parsed.rows
    const clauses: Array<{ key: string; val: string }> = []
    for (const f of chartSpec.filter) {
      const k = resolveKey(f.col)
      if (!k) continue // unknown column — drop the clause, don't drop all rows
      clauses.push({ key: k, val: f.val.trim().toLowerCase() })
    }
    if (clauses.length === 0) return parsed.rows
    return parsed.rows.filter(r => clauses.every(c => {
      const v = r[c.key]
      const sv = v === null || v === undefined ? '' : String(v).trim().toLowerCase()
      return sv === c.val
    }))
  }, [parsed, chartSpec, resolveKey])

  const chartOption = useMemo(() => {
    if (!parsed || !resolved || !hasChart) return {}
    const rows = filteredChartRows ?? parsed.rows
    if (rows.length === 0) return {}
    const { xKey, yKeys, seriesKey } = resolved
    const labels = rows.map(r => String(r[xKey] ?? ''))

    if (mode === 'pie') {
      const vk = yKeys[0]
      return {
        // Legend is fixed at the TOP for every chart (a frontend styling
        // decision — never driven by the AI schema, which only maps columns).
        tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
        legend: { top: 0, type: 'scroll', textStyle: { fontFamily: 'JetBrains Mono, monospace', fontSize: 10 } },
        series: [{
          type: 'pie', radius: ['35%', '60%'], center: ['50%', '58%'],
          label: { formatter: '{b}\n{d}%', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
          data: rows.map(r => ({ name: String(r[xKey] ?? ''), value: num(r[vk]) })),
        }],
      }
    }

    // Grouped (pivot) series: one series per distinct value of seriesKey,
    // single measure. Only when the AI named a series column.
    let series: Record<string, unknown>[]
    let xData: string[]
    if (seriesKey && yKeys.length === 1) {
      const yk = yKeys[0]
      const xVals: string[] = []
      const sVals: string[] = []
      const lut = new Map<string, number>()
      for (const r of rows) {
        const xv = String(r[xKey] ?? '')
        const sv = String(r[seriesKey] ?? '')
        if (!xVals.includes(xv)) xVals.push(xv)
        if (!sVals.includes(sv)) sVals.push(sv)
        lut.set(JSON.stringify([xv, sv]), num(r[yk]))
      }
      xData = xVals
      series = sVals.map(sv => ({
        name: sv,
        type: mode === 'line' ? 'line' : 'bar',
        data: xVals.map(xv => lut.get(JSON.stringify([xv, sv])) ?? null),
        itemStyle: mode === 'bar' ? undefined : undefined,
        ...(isArea ? { areaStyle: { opacity: 0.08 } } : {}),
      }))
    } else {
      xData = labels
      series = yKeys.map(yk => ({
        name: displayName(yk),
        type: mode === 'line' ? 'line' : 'bar',
        data: rows.map(r => num(r[yk])),
        itemStyle: mode === 'bar' && yKeys.length === 1 ? { color: accentColor } : undefined,
        lineStyle: mode === 'line' && yKeys.length === 1 ? { color: accentColor, width: 2 } : undefined,
        ...(isArea ? { areaStyle: { color: 'rgba(0,0,0,0.04)' } } : {}),
      }))
    }

    const multi = series.length > 1
    return {
      // Legend fixed at the TOP, plot area pushed down to make room. Fixed
      // frontend styling — the AI schema only maps columns, never layout.
      tooltip: { trigger: 'axis', textStyle: { fontFamily: 'JetBrains Mono, monospace', fontSize: 11 } },
      legend: multi ? { top: 0, type: 'scroll', textStyle: { fontFamily: 'JetBrains Mono, monospace', fontSize: 10 } } : undefined,
      grid: { left: 60, right: 20, top: multi ? 40 : 20, bottom: 20 },
      xAxis: {
        type: 'category', data: xData,
        axisLabel: { fontSize: 10, fontFamily: 'JetBrains Mono, monospace', rotate: xData.length > 6 ? 30 : 0 },
      },
      yAxis: { type: 'value', axisLabel: { fontSize: 10, fontFamily: 'JetBrains Mono, monospace' } },
      series,
    }
  }, [mode, parsed, resolved, hasChart, isArea])

  if (!parsed) return null
  const { rows, keys, cols } = parsed

  const modes: { key: ViewMode; icon: typeof Table2; label: string }[] = [
    { key: 'table', icon: Table2, label: 'TABLE' },
    ...(hasChart ? [
      { key: 'bar' as ViewMode, icon: BarChart3, label: 'BAR' },
      { key: 'pie' as ViewMode, icon: PieChart, label: 'PIE' },
      { key: 'line' as ViewMode, icon: TrendingUp, label: 'LINE' },
    ] : []),
  ]

  const downloadCsv = () => {
    const esc = (v: unknown) => {
      const s = v == null ? '' : String(v)
      return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
    }
    const csv = [
      cols.map(esc).join(','),
      ...rows.map(r => keys.map(k => esc(r[k])).join(',')),
    ].join('\n')
    // BOM so Excel reads UTF-8 (CJK) correctly.
    const blob = new Blob(['\ufeff' + csv], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'result.csv'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div>
      {/* Mode switcher + download */}
      <div className="flex items-center gap-0 mb-1">
        {modes.map(m => (
          <button key={m.key} onClick={() => setMode(m.key)}
            className={`flex items-center gap-1 px-2 py-0.5 font-mono text-[9px] border ${
              mode === m.key ? 'border-ink bg-ink text-white' : 'border-border text-ink-ghost hover:border-ink-muted'
            }`}>
            <m.icon className="h-3 w-3" />{m.label}
          </button>
        ))}
        <button onClick={downloadCsv} title="CSV"
          className="flex items-center gap-1 px-2 py-0.5 font-mono text-[9px] border border-border text-ink-ghost hover:border-ink-muted ml-1">
          <Download className="h-3 w-3" />CSV
        </button>
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
          <ReactEChartsCore echarts={echarts} option={chartOption} notMerge style={{ height: '100%' }} />
        </div>
      )}
    </div>
  )
}
