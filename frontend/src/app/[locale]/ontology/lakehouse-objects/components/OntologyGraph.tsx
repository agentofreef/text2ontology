'use client'

import { useTranslations } from 'next-intl'
import { useState, useMemo, useRef, useEffect, useCallback } from 'react'
import { motion, useReducedMotion } from 'motion/react'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { Modal } from '@/components/ui/Modal'
import { useFetch } from '@/lib/hooks'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import { api } from '@/lib/api'
import type { OntObjectType, OntKnowledge, OntCausality, OntLearnedFact, OntFactLink, OntLinkType } from '@/types/api'
import { Plus, RefreshCw } from 'lucide-react'
import * as echarts from 'echarts'

/**
 * SV Minimal palette · 黑白灰绿红硬约束
 * echarts 接受 hex 不接受 CSS custom props，因此此处硬编码解析值，
 * 必须与 frontend/src/app/globals.css 里 minimal 主题的 token 一致。
 * 见 docs/design/design-system.md v2 §1。
 */
const COLORS = {
  od: '#0A0A0A',              // ink — Object Od
  property: '#525252',        // ink-muted — Property with Ok entry
  propertyGhost: '#A1A1A1',   // ink-ghost — Property without Ok entry
  propertyMc: '#000000',      // pure black — Machine Code property
  joinKey: '#0A0A0A',         // ink — join_key edge (causality)
  odLink: '#FF4500',          // accent — OD→OD link (ont_link_type FK)
  odLinkGhost: '#FFB199',     // accent-faded — proposed (mark=false) link
  ownership: '#8C8C8C',       // medium gray — od→prop line (was #E5E5E5, near-invisible on white)
  learnedFact: '#16A34A',     // success 绿 — Learned Fact (验证过的知识)
  factLink: '#16A34A',        // 同上
  pendingBorder: '#A1A1A1',   // pending fact 用灰 border（替换 amber）
  border: '#E5E5E5',
  borderLight: '#F0F0F0',
  textPrimary: '#0A0A0A',
  textMuted: '#525252',
  textGhost: '#A1A1A1',
  canvasAlt: '#FAFAFA',
}

interface OntologyGraphProps {
  selectedId: string | null
  onSelectNode: (n: { type: string; id: string; name: string; detail: string }) => void
  onSelectEdge: (e: { id: string; from: string; to: string; cardinality: string }) => void
  refreshKey: number
}

export function OntologyGraph({ selectedId, onSelectNode, onSelectEdge, refreshKey }: OntologyGraphProps) {
  const t = useTranslations('graph')
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const [linkModalOpen, setLinkModalOpen] = useState(false)
  const chartRef = useRef<HTMLDivElement>(null)
  const chartInstance = useRef<echarts.ECharts | null>(null)

  const [fromOdId, setFromOdId] = useState('')
  const [fromPropId, setFromPropId] = useState('')
  const [toOdId, setToOdId] = useState('')
  const [toPropId, setToPropId] = useState('')
  const [cardinality, setCardinality] = useState('1:N')
  const [linkDesc, setLinkDesc] = useState('')

  // Clicking a relationship line opens a targeted delete popup. kind routes the
  // delete to the right table: join_key causality (prop↔prop) vs ont_link_type
  // (OD→OD FK). Edges without a _linkId (ownership / learned-fact lines) are not
  // deletable and never open this popup.
  const [edgeToDelete, setEdgeToDelete] = useState<
    { id: string; kind: 'causality' | 'link_type'; from: string; to: string; cardinality: string } | null
  >(null)
  const [deletingEdge, setDeletingEdge] = useState(false)

  const { data: objects, loading: objectsLoading, refetch: refetchObjects } = useFetch<OntObjectType>('/ontology/objects')
  const { data: knowledge, loading: knowledgeLoading, refetch: refetchKnowledge } = useFetch<OntKnowledge>('/ontology/knowledge')
  const { data: causalities, refetch: refetchCausalities } = useFetch<OntCausality>('/ontology/causality')
  const { data: odLinks, refetch: refetchOdLinks } = useFetch<OntLinkType>('/ontology/links')
  const { data: learnedFacts } = useFetch<OntLearnedFact>('/ontology/learned-facts')
  const { data: factLinks } = useFetch<OntFactLink>('/ontology/fact-links')

  const refetchAll = useCallback(() => {
    refetchObjects(); refetchKnowledge(); refetchCausalities(); refetchOdLinks()
  }, [refetchObjects, refetchKnowledge, refetchCausalities, refetchOdLinks])

  // Refetch when the parent bumps refreshKey (after list mutations). Skip the
  // first run so we don't double-fetch on mount (useFetch already fetches once).
  const refreshKeyMounted = useRef(false)
  useEffect(() => {
    if (!refreshKeyMounted.current) { refreshKeyMounted.current = true; return }
    refetchAll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshKey])

  // Reflow the ECharts canvas whenever its container box changes — not just on
  // window resize. The inspector drawer docks at the bottom of this same column,
  // so opening/closing it shrinks/grows the chart container; without a
  // ResizeObserver the canvas would keep its old height and clip or leave gaps.
  useEffect(() => {
    const el = chartRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => chartInstance.current?.resize())
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const [ensured, setEnsured] = useState(false)
  useEffect(() => {
    if (!currentProject || ensured) return
    api<{ created: number }>('/connector/pbit/ensure-property-knowledge', {
      method: 'POST', body: { projectId: currentProject.id },
    }).then(res => {
      if (res.created > 0) refetchKnowledge()
      setEnsured(true)
    }).catch(() => setEnsured(true))
  }, [currentProject, ensured, refetchKnowledge])

  const markedObjects = useMemo(() => objects.filter(o => o.mark), [objects])

  const propertyOkMap = useMemo(() => {
    const map = new Map<string, OntKnowledge>()
    for (const k of knowledge) {
      if (k.anchorType === 'property' && k.anchorId) map.set(k.anchorId, k)
    }
    return map
  }, [knowledge])

  const joinKeyLinks = useMemo(() => causalities.filter(c => c.relationType === 'join_key'), [causalities])

  const okToProperty = useMemo(() => {
    const map = new Map<string, { propName: string; odName: string; odId: string; propId: string }>()
    for (const obj of markedObjects) {
      for (const p of obj.properties || []) {
        const ok = propertyOkMap.get(p.id)
        if (ok) map.set(ok.id, { propName: p.name, odName: obj.name, odId: obj.id, propId: p.id })
      }
    }
    return map
  }, [markedObjects, propertyOkMap])

  // ─── echarts render ───────────────────────────────────────────
  useEffect(() => {
    if (!chartRef.current) return
    if (!chartInstance.current) {
      chartInstance.current = echarts.init(chartRef.current, undefined, { renderer: 'canvas' })
    }
    const chart = chartInstance.current

    const categories = [
      { name: t('cat_od'), itemStyle: { color: COLORS.od } },
      { name: t('cat_property'), itemStyle: { color: COLORS.property } },
      { name: t('cat_ol'), itemStyle: { color: COLORS.learnedFact } },
    ]

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const nodes: any[] = []
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const graphLinks: any[] = []

    // OD nodes — render ALL objects regardless of mark. The graph view is a
    // *structural* picture of the ontology; mark is a runtime activation flag
    // and does not belong as a hard filter here. Visual distinction (solid
    // black for mark=true, ghost outline for mark=false) keeps both states
    // visible without conflating them.
    for (const obj of objects) {
      const propCount = obj.properties?.length || 0
      const mapped = obj.properties?.filter(p => p.sourceColumn).length || 0
      const isActive = obj.mark
      nodes.push({
        id: `od:${obj.id}`,
        name: obj.name,
        symbolSize: isActive
          ? Math.max(30, Math.min(55, 30 + propCount * 2.5))
          : 22,
        category: 0,
        symbol: 'circle',
        itemStyle: {
          color: isActive ? COLORS.od : '#FFFFFF',
          borderColor: isActive ? COLORS.border : COLORS.textGhost,
          borderWidth: isActive ? 1 : 1.2,
          borderType: isActive ? ('solid' as const) : ('dashed' as const),
        },
        label: {
          show: true,
          formatter: isActive
            ? `{name|${obj.name}}\n{tag|${obj.kind.toUpperCase()}}  {props|${mapped}/${propCount}p}`
            : `{ghost|${obj.name}}`,
          rich: {
            name: { fontSize: 12, fontWeight: 'bold' as const, fontFamily: 'sans-serif', color: COLORS.textPrimary, lineHeight: 16 },
            tag: { fontSize: 9, fontFamily: 'sans-serif', color: COLORS.textMuted },
            props: { fontSize: 9, fontFamily: 'sans-serif', color: COLORS.textMuted },
            ghost: { fontSize: 11, fontFamily: 'sans-serif', color: COLORS.textGhost, lineHeight: 14 },
          },
        },
        tooltip: {
          formatter: [
            `<b>${obj.name}</b> [${obj.kind}]`,
            `${t('tt_props')}${propCount}`,
            `${t('tt_mapped')}${mapped}`,
            isActive ? '' : `<span style="color:#A1A1A1">${t('tt_inactive')}</span>`,
          ].filter(Boolean).join('<br/>'),
        },
        _type: 'object', _id: obj.id,
        _detail: `${obj.kind} · ${propCount} ${t('detail_props')} · ${mapped} ${t('detail_mapped')}${isActive ? '' : ` · ${t('detail_inactive')}`}`,
      })
    }

    // Property nodes + ownership edges — only for marked ODs. Rendering all
    // properties of a 50-OD project would clutter the graph; the user can
    // activate an OD to expand it.
    for (const obj of markedObjects) {
      for (const p of obj.properties || []) {
        const ok = propertyOkMap.get(p.id)
        const hasOk = !!ok
        const isMC = p.isMachineCode
        // 黑白灰绿红：MC = 黑填充 / 有 Ok = 中灰 / 无 Ok = 浅灰 dashed border
        const fillColor = isMC ? COLORS.propertyMc : hasOk ? COLORS.property : COLORS.propertyGhost
        nodes.push({
          id: `prop:${p.id}`,
          name: p.name,
          symbolSize: 16,
          category: 1,
          symbol: 'rect',
          itemStyle: {
            color: fillColor,
            borderColor: hasOk ? COLORS.textPrimary : COLORS.textGhost,
            borderWidth: hasOk ? 1.5 : 1,
            borderType: hasOk ? 'solid' as const : 'dashed' as const,
          },
          label: {
            show: true,
            formatter: isMC ? `{name|${p.name} · MC}` : `{name|${p.name}}`,
            rich: { name: { fontSize: 10, fontFamily: 'sans-serif', color: COLORS.textPrimary, lineHeight: 13 } },
          },
          tooltip: {
            formatter: [
              `<b>${p.name}</b>`,
              `${t('tt_type')}${p.dataType || 'text'}`,
              p.sourceColumn ? `${t('tt_source')}${p.sourceColumn}` : '',
              isMC ? '<span>Machine Code</span>' : '',
              hasOk ? `<span>${t('tt_ok_linked')}</span>` : `<span style="color:${COLORS.textGhost}">${t('tt_ok_unlinked')}</span>`,
            ].filter(Boolean).join('<br/>'),
          },
          _type: 'property', _id: p.id,
          _detail: `${p.dataType || 'text'} · ${t('detail_source')} ${p.sourceColumn || '—'}${isMC ? ' · MC' : ''}`,
        })

        graphLinks.push({
          source: `od:${obj.id}`,
          target: `prop:${p.id}`,
          lineStyle: { color: COLORS.ownership, width: 1, type: 'dotted' as const },
          label: { show: false },
        })
      }
    }

    // OD → OD links from ont_link_type. Distinct from join_key causality (which
    // travels prop ↔ prop): these are FK relationships at the entity level and
    // visualised as accent-coloured edges directly between OD nodes.
    //
    // We do NOT filter by node mark — both endpoints are guaranteed to exist
    // as nodes in the graph (we render every OD, marked or not), so any FK
    // link with two valid endpoint OD ids will render. Visual distinction
    // (solid vs dashed) reflects the link's own mark state.
    const odIdSet = new Set(objects.map(o => o.id))
    for (const l of (odLinks || [])) {
      if (!odIdSet.has(l.fromObjectId) || !odIdSet.has(l.toObjectId)) continue
      const isPending = !l.mark
      graphLinks.push({
        source: `od:${l.fromObjectId}`,
        target: `od:${l.toObjectId}`,
        lineStyle: {
          color: COLORS.odLink,
          width: isPending ? 1.5 : 2.5,
          type: isPending ? ('dashed' as const) : ('solid' as const),
          curveness: 0.2,
          opacity: isPending ? 0.6 : 0.95,
        },
        label: {
          show: true,
          formatter: l.fkColumn || l.linkName || (l.cardinality === 'many_to_one' ? 'N:1' : l.cardinality),
          fontSize: 10,
          fontWeight: 'bold' as const,
          fontFamily: 'sans-serif',
          color: COLORS.odLink,
        },
        tooltip: {
          formatter: [
            `<b>${l.fromObjectName} → ${l.toObjectName}</b>`,
            l.fkColumn ? `FK：${l.fkColumn}` : '',
            `${t('tt_cardinality')}${l.cardinality}`,
            l.description || '',
            isPending ? `<span>${t('tt_pending')}</span>` : '',
          ].filter(Boolean).join('<br/>'),
        },
        _linkId: l.id,
        _linkKind: 'link_type',
        _from: l.fromObjectName,
        _to: l.toObjectName,
        _cardinality: l.cardinality,
      })
    }

    for (const link of joinKeyLinks) {
      const fromInfo = okToProperty.get(link.fromKnowledgeId)
      const toInfo = okToProperty.get(link.toKnowledgeId)
      if (!fromInfo || !toInfo) continue
      graphLinks.push({
        source: `prop:${fromInfo.propId}`,
        target: `prop:${toInfo.propId}`,
        lineStyle: { color: COLORS.joinKey, width: 2.5, type: 'solid' as const, curveness: 0.15 },
        label: {
          show: true,
          formatter: link.direction || '1:N',
          fontSize: 10,
          fontWeight: 'bold' as const,
          fontFamily: 'sans-serif',
          color: COLORS.joinKey,
        },
        tooltip: {
          formatter: `<b>${fromInfo.odName}.${fromInfo.propName}</b> → <b>${toInfo.odName}.${toInfo.propName}</b><br/>${t('tt_cardinality')}${link.direction}<br/>${link.description || ''}`,
        },
        _linkId: link.id,
        _linkKind: 'causality',
        _from: `${fromInfo.odName}.${fromInfo.propName}`,
        _to: `${toInfo.odName}.${toInfo.propName}`,
        _cardinality: link.direction,
      })
    }

    const activeFacts = (learnedFacts || []).filter(f => f.confidence !== 'rejected')
    for (const fact of activeFacts) {
      const label = fact.title || (fact.summary?.length > 25 ? fact.summary.slice(0, 25) + '…' : fact.summary) || 'Ol'
      const isPending = fact.confidence === 'pending'
      nodes.push({
        id: `ol:${fact.id}`,
        name: label,
        symbolSize: 22,
        category: 2,
        symbol: 'diamond',
        itemStyle: {
          color: COLORS.learnedFact,
          borderColor: isPending ? COLORS.pendingBorder : COLORS.border,
          borderWidth: isPending ? 2 : 1,
          borderType: isPending ? 'dashed' as const : 'solid' as const,
        },
        label: {
          show: true,
          formatter: `{name|${label}}`,
          rich: { name: { fontSize: 10, fontFamily: 'sans-serif', color: COLORS.textPrimary, lineHeight: 13 } },
        },
        tooltip: {
          formatter: [
            `<b>${fact.title || t('tt_untitled')}</b>`,
            fact.summary || '',
            `${t('tt_confidence')}${fact.confidence}`,
            `${t('tt_source_type')}${fact.sourceType}`,
          ].filter(Boolean).join('<br/>'),
        },
        _type: 'fact', _id: fact.id,
        _detail: `${fact.confidence} · ${fact.sourceType}${fact.keywords ? ' · ' + fact.keywords : ''}`,
      })
    }

    const nodeIdSet = new Set(nodes.map((n: { id: string }) => n.id))
    for (const fl of (factLinks || [])) {
      const sourceId = `ol:${fl.factId}`
      let targetId = ''
      if (fl.targetType === 'object') targetId = `od:${fl.targetId}`
      else if (fl.targetType === 'property') targetId = `prop:${fl.targetId}`
      else if (fl.targetType === 'fact') targetId = `ol:${fl.targetId}`
      if (!targetId || !nodeIdSet.has(sourceId) || !nodeIdSet.has(targetId)) continue
      graphLinks.push({
        source: sourceId,
        target: targetId,
        lineStyle: { color: COLORS.factLink, width: 1.5, type: 'dashed' as const, curveness: 0.1 },
        label: {
          show: true,
          formatter: fl.role || 'about',
          fontSize: 9,
          fontFamily: 'sans-serif',
          color: COLORS.factLink,
        },
      })
    }

    const safeNodes = nodes.filter((n: { id?: string; name?: string }) => n.id && n.name)
    const safeNodeIds = new Set(safeNodes.map((n: { id: string }) => n.id))
    const safeLinks = graphLinks.filter((l: { source?: string; target?: string }) =>
      l.source && l.target && safeNodeIds.has(l.source) && safeNodeIds.has(l.target))

    const option: echarts.EChartsOption = {
      tooltip: {
        trigger: 'item',
        backgroundColor: '#ffffff',
        borderColor: COLORS.border,
        borderWidth: 1,
        textStyle: { fontFamily: 'sans-serif', fontSize: 11, color: COLORS.textPrimary },
      },
      legend: {
        data: categories.map(c => c.name),
        bottom: 10,
        left: 'center',
        textStyle: { fontFamily: 'sans-serif', fontSize: 11, color: COLORS.textMuted },
        itemWidth: 12,
        itemHeight: 12,
      },
      animationDuration: 800,
      animationEasingUpdate: 'quinticInOut' as const,
      series: [{
        type: 'graph',
        layout: 'force',
        data: safeNodes,
        links: safeLinks,
        categories,
        roam: true,
        draggable: true,
        force: { repulsion: 300, edgeLength: [50, 160], gravity: 0.1, friction: 0.6 },
        edgeSymbol: ['none', 'arrow'],
        edgeSymbolSize: [0, 8],
        label: { show: true, position: 'bottom', distance: 5 },
        emphasis: {
          // 'none' (not 'adjacency'): hovering a node must NOT dim the rest of
          // the graph. With many nodes, moving the cursor across them made the
          // whole canvas flash dark/light repeatedly. We still thicken the
          // hovered node's border/edges, just without fading everything else.
          focus: 'none',
          lineStyle: { width: 4 },
          itemStyle: { borderWidth: 3, borderColor: COLORS.textPrimary },
        },
        lineStyle: { opacity: 0.85 },
      }],
    }

    chart.setOption(option, true)

    // Re-apply selection highlight after a full setOption (which clears state).
    // Same logic as the selectedId effect below — find the node whose _id
    // matches and emphasize it. Tolerant of not-found (ghost still has a node).
    chart.dispatchAction({ type: 'downplay', seriesIndex: 0 })
    if (selectedId) {
      const idx = safeNodes.findIndex((n: { _id?: string; _type?: string }) => n._id === selectedId && n._type === 'object')
      if (idx >= 0) chart.dispatchAction({ type: 'highlight', seriesIndex: 0, dataIndex: idx })
    }

    chart.off('click')
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    chart.on('click', (params: any) => {
      if (params.dataType === 'node' && params.data) {
        onSelectNode({
          type: params.data._type || '',
          id: params.data._id || '',
          name: params.data.name || '',
          detail: params.data._detail || '',
        })
      } else if (params.dataType === 'edge' && params.data?._linkId) {
        onSelectEdge({
          id: params.data._linkId,
          from: params.data._from,
          to: params.data._to,
          cardinality: params.data._cardinality,
        })
        // Open the targeted delete popup for this specific relationship line.
        setEdgeToDelete({
          id: params.data._linkId,
          kind: params.data._linkKind === 'link_type' ? 'link_type' : 'causality',
          from: params.data._from || '',
          to: params.data._to || '',
          cardinality: params.data._cardinality || '',
        })
      }
    })

    // One-shot resize after (re)building so the chart fills its column even
    // when it mounts inside a flex layout that settles after paint.
    chart.resize()
    const handleResize = () => chart.resize()
    window.addEventListener('resize', handleResize)
    return () => window.removeEventListener('resize', handleResize)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [markedObjects, propertyOkMap, joinKeyLinks, okToProperty, learnedFacts, factLinks, odLinks])

  // Selection highlight — when the parent's selectedId changes (e.g. a list
  // row was clicked), emphasize the matching OBJECT node. Object node _ids are
  // the raw object id with _type==='object'. Downplay first to clear any prior
  // emphasis, then highlight by dataIndex. Guarded so it only reacts to
  // selectedId (not a re-render of the chart option, which handles its own
  // re-highlight above).
  useEffect(() => {
    const chart = chartInstance.current
    if (!chart) return
    chart.dispatchAction({ type: 'downplay', seriesIndex: 0 })
    if (!selectedId) return
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const opt = chart.getOption() as any
    const data = opt?.series?.[0]?.data || []
    const idx = data.findIndex((n: { _id?: string; _type?: string }) => n._id === selectedId && n._type === 'object')
    if (idx < 0) return
    chart.dispatchAction({ type: 'highlight', seriesIndex: 0, dataIndex: idx })

    // Centre the camera on the node. Use the series' ABSOLUTE `center` (in the
    // node's layout coordinate from getItemLayout) — NOT a relative graphRoam.
    // A relative roam accumulates: getItemLayout returns layout coords that the
    // roam transform does not write back, so re-applying the same delta pans the
    // whole graph off-screen (the earlier blank-on-click bug). center is
    // idempotent, so repeated selections re-frame correctly. Guarded: a bad
    // value is skipped, never thrown.
    try {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const series = (chart as any).getModel?.().getSeriesByIndex?.(0)
      const layout = series?.getData?.().getItemLayout?.(idx) as [number, number] | undefined
      if (layout && Number.isFinite(layout[0]) && Number.isFinite(layout[1])) {
        chart.setOption({ series: [{ center: [layout[0], layout[1]] }] })
      }
    } catch { /* best-effort camera centring */ }
  }, [selectedId])

  const fromOd = markedObjects.find(o => o.id === fromOdId)
  const toOd = markedObjects.find(o => o.id === toOdId)
  const fromProps = fromOd?.properties?.filter(p => p.sourceColumn) || []
  const toProps = toOd?.properties?.filter(p => p.sourceColumn) || []

  const createLink = async () => {
    if (!fromPropId || !toPropId || !currentProject) return
    const fromOk = propertyOkMap.get(fromPropId)
    const toOk = propertyOkMap.get(toPropId)
    if (!fromOk || !toOk) {
      msg.error(t('err_no_ok'))
      return
    }
    try {
      await api(`/ontology/causality?projectId=${currentProject.id}`, {
        method: 'POST',
        body: {
          fromKnowledgeId: fromOk.id,
          toKnowledgeId: toOk.id,
          relationType: 'join_key',
          direction: cardinality,
          description: linkDesc,
        },
      })
      msg.success(t('msg_link_created'))
      setLinkModalOpen(false)
      setFromOdId(''); setFromPropId(''); setToOdId(''); setToPropId(''); setLinkDesc('')
      refetchAll()
    } catch (e) { msg.error(e instanceof Error ? e.message : t('err_create_failed')) }
  }

  // Targeted edge deletion — confirmed via the popup opened on edge click.
  // Routes to the correct backend table by edge kind: join_key causality
  // (prop↔prop) vs ont_link_type (OD→OD FK).
  const confirmDeleteEdge = async () => {
    if (!edgeToDelete || !currentProject) return
    const path = edgeToDelete.kind === 'link_type'
      ? `/ontology/links/${edgeToDelete.id}?projectId=${currentProject.id}`
      : `/ontology/causality/${edgeToDelete.id}?projectId=${currentProject.id}`
    setDeletingEdge(true)
    try {
      await api(path, { method: 'DELETE' })
      msg.success(t('msg_link_deleted'))
      setEdgeToDelete(null)
      refetchAll()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('err_delete_failed'))
    } finally {
      setDeletingEdge(false)
    }
  }

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
        <div className="text-sm text-ink-muted">{t('no_project')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
  }

  const loading = (objectsLoading && objects.length === 0) || (knowledgeLoading && knowledge.length === 0)

  return (
    <div className="relative flex h-full min-h-0 flex-col">
      {/* No top bar — the graph runs full-bleed. The page's 湖仓对象 panel owns the
          top-left; these controls float over the top-right corner. */}
      <div className="absolute right-3 top-3 z-10 flex items-center gap-2">
        <motion.button
          onClick={refetchAll}
          whileHover={reduce ? undefined : { scale: 1.05 }}
          whileTap={reduce ? undefined : { scale: 0.95 }}
          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
          aria-label={t('refresh')}
          title={t('refresh')}
          className="inline-flex h-7 w-7 items-center justify-center rounded-md border border-border bg-white text-ink-muted outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
        >
          <RefreshCw size={12} aria-hidden="true" />
        </motion.button>
        <AnimatedButton
          variant="primary"
          size="sm"
          onClick={() => setLinkModalOpen(true)}
          aria-label={t('new_link')}
        >
          <Plus size={12} aria-hidden="true" /> {t('new_link')}
        </AnimatedButton>
      </div>

      {/* Chart */}
      {loading ? (
        <div className="flex flex-1 items-center justify-center">
          <InlineLoader text={t('loading')} />
        </div>
      ) : (
        <div ref={chartRef} className="flex-1 min-h-0 bg-white" />
      )}

      {/* Legend (flex-shrink-0 footer) */}
      <div className="flex flex-shrink-0 flex-wrap items-center gap-4 border-t border-border bg-white px-6 py-2">
        <LegendItem color={COLORS.od} shape="circle" label={t('legend_od')} />
        <LegendItem color={COLORS.property} shape="rect" label={t('legend_prop_ok')} />
        <LegendItem color={COLORS.propertyGhost} shape="rect" label={t('legend_prop_no_ok')} dashed />
        <LegendItem color={COLORS.propertyMc} shape="rect" label={t('legend_mc')} />
        <LegendItem color={COLORS.joinKey} shape="line" label={t('legend_join_key')} />
        <LegendItem color={COLORS.learnedFact} shape="diamond" label={t('legend_fact')} />
        <span className="ml-auto text-[11px] text-ink-ghost">{t('delete_hint')}</span>
      </div>

      {/* Link modal */}
      <Modal open={linkModalOpen} onClose={() => setLinkModalOpen(false)} title={t('modal_title')}>
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <FieldSelect
              id="from-od"
              label={t('modal_from_od')}
              value={fromOdId}
              onChange={(v) => { setFromOdId(v); setFromPropId('') }}
              options={[['', t('modal_select_od')], ...markedObjects.map(o => [o.id, o.name] as [string, string])]}
            />
            <FieldSelect
              id="from-prop"
              label={t('modal_from_prop')}
              value={fromPropId}
              onChange={setFromPropId}
              disabled={!fromOdId}
              options={[['', t('modal_select_prop')], ...fromProps.map(p => [p.id, `${p.name} (${p.sourceColumn})`] as [string, string])]}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <FieldSelect
              id="to-od"
              label={t('modal_to_od')}
              value={toOdId}
              onChange={(v) => { setToOdId(v); setToPropId('') }}
              options={[['', t('modal_select_od')], ...markedObjects.map(o => [o.id, o.name] as [string, string])]}
            />
            <FieldSelect
              id="to-prop"
              label={t('modal_to_prop')}
              value={toPropId}
              onChange={setToPropId}
              disabled={!toOdId}
              options={[['', t('modal_select_prop')], ...toProps.map(p => [p.id, `${p.name} (${p.sourceColumn})`] as [string, string])]}
            />
          </div>
          <div>
            <label className="mb-1.5 block text-sm font-medium text-ink">{t('modal_cardinality')}</label>
            <CardinalitySelector value={cardinality} onChange={setCardinality} />
          </div>
          <div>
            <label htmlFor="link-desc" className="mb-1.5 block text-sm font-medium text-ink">{t('modal_desc')}</label>
            <input
              id="link-desc"
              value={linkDesc}
              onChange={(e) => setLinkDesc(e.target.value)}
              placeholder={t('modal_desc_placeholder')}
              aria-label={t('modal_desc_aria')}
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10"
            />
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <AnimatedButton
              variant="ghost"
              size="md"
              onClick={() => setLinkModalOpen(false)}
              aria-label={t('cancel')}
            >
              {t('cancel')}
            </AnimatedButton>
            <AnimatedButton
              variant="primary"
              size="md"
              onClick={createLink}
              disabled={!fromPropId || !toPropId}
              aria-label={t('create_link')}
            >
              {t('create_link')}
            </AnimatedButton>
          </div>
        </div>
      </Modal>

      {/* Targeted delete popup — opened by clicking a relationship line. */}
      <Modal open={!!edgeToDelete} onClose={() => setEdgeToDelete(null)} title={t('delete_link')}>
        <div className="space-y-4">
          <p className="text-sm text-ink">{t('confirm_delete_link')}</p>
          {edgeToDelete && (
            <div className="rounded-md border border-border bg-canvas-alt px-3 py-2">
              <div className="flex items-center gap-2 font-mono text-[13px] text-ink">
                <span className="truncate">{edgeToDelete.from}</span>
                <span className="flex-shrink-0 text-ink-ghost">→</span>
                <span className="truncate">{edgeToDelete.to}</span>
              </div>
              {edgeToDelete.cardinality && (
                <div className="mt-1 text-[11px] text-ink-muted">
                  {t('modal_cardinality')}：{edgeToDelete.cardinality}
                </div>
              )}
            </div>
          )}
          <div className="flex justify-end gap-2 pt-2">
            <AnimatedButton
              variant="ghost"
              size="md"
              onClick={() => setEdgeToDelete(null)}
              aria-label={t('cancel')}
            >
              {t('cancel')}
            </AnimatedButton>
            <AnimatedButton
              variant="danger"
              size="md"
              onClick={confirmDeleteEdge}
              disabled={deletingEdge}
              aria-label={t('delete_link')}
              data-testid="confirm-delete-edge"
            >
              {t('delete_link')}
            </AnimatedButton>
          </div>
        </div>
      </Modal>
    </div>
  )
}

// ─────────────────────────────────────────────────────────────
// Sub-components
// ─────────────────────────────────────────────────────────────

function InlineLoader({ text }: { text: string }) {
  const reduce = useReducedMotion()
  return (
    <div className="flex items-center gap-2 text-sm text-ink-muted">
      <motion.span
        animate={reduce ? undefined : { rotate: 360 }}
        transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
        className="inline-flex"
      >
        <RefreshCw size={14} aria-hidden="true" />
      </motion.span>
      <span>{text}</span>
    </div>
  )
}

function LegendItem({
  color, shape, label, dashed,
}: {
  color: string
  shape: 'circle' | 'rect' | 'diamond' | 'line'
  label: string
  dashed?: boolean
}) {
  const style: React.CSSProperties = { backgroundColor: color }
  if (dashed) {
    style.backgroundColor = 'transparent'
    style.border = `1px dashed ${color}`
  }
  let cls = 'inline-block'
  switch (shape) {
    case 'circle': cls += ' h-3 w-3 rounded-full'; break
    case 'rect': cls += ' h-2.5 w-3.5 rounded-[2px]'; break
    case 'diamond': cls += ' h-2.5 w-2.5 rotate-45'; break
    case 'line': cls += ' h-[2px] w-5'; break
  }
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cls} style={style} aria-hidden="true" />
      <span className="text-[11px] text-ink-muted">{label}</span>
    </span>
  )
}

function FieldSelect({
  id, label, value, onChange, options, disabled,
}: {
  id: string
  label: string
  value: string
  onChange: (v: string) => void
  options: [string, string][]
  disabled?: boolean
}) {
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-ink">{label}</label>
      <select
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        aria-label={label}
        className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10 disabled:opacity-50 disabled:cursor-not-allowed"
      >
        {options.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
      </select>
    </div>
  )
}

function CardinalitySelector({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const reduce = useReducedMotion()
  const [layoutId] = useState(() => `card-${Math.random().toString(36).slice(2, 9)}`)
  const options = ['1:N', 'N:1', '1:1', 'N:N']
  return (
    <div role="radiogroup" className="relative flex h-8 w-fit items-center gap-0 rounded-md border border-border bg-canvas-alt p-0.5">
      {options.map(c => {
        const selected = value === c
        return (
          <button
            key={c}
            role="radio"
            aria-checked={selected}
            onClick={() => onChange(c)}
            className="relative h-full rounded-[5px] px-3 text-xs font-medium outline-none focus-visible:ring-1 focus-visible:ring-ink"
          >
            {selected && (
              <motion.span
                layoutId={layoutId}
                className="absolute inset-0 rounded-[5px] bg-ink shadow-sm"
                transition={reduce ? { duration: 0 } : { type: 'spring', stiffness: 500, damping: 35 }}
              />
            )}
            <span className={`relative ${selected ? 'text-white' : 'text-ink-muted hover:text-ink'}`}>
              {c}
            </span>
          </button>
        )
      })}
    </div>
  )
}
