'use client'

import { useTranslations } from 'next-intl'
import { useState, useMemo, useRef, useEffect, useCallback } from 'react'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { Badge } from '@/components/ui/Badge'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { Modal } from '@/components/ui/Modal'
import { useFetch } from '@/lib/hooks'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import { api } from '@/lib/api'
import type { OntObjectType, OntKnowledge, OntCausality, OntLearnedFact, OntFactLink, OntLinkType } from '@/types/api'
import { Network, Maximize2, Minimize2, Plus, Trash2, X, RefreshCw } from 'lucide-react'
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
  ownership: '#E5E5E5',       // border — faint od→prop line
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

export default function LakehouseGraphPageMinimal() {
  const t = useTranslations('graph')
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const [fullscreen, setFullscreen] = useState(false)
  const [selectedNode, setSelectedNode] = useState<{ type: string; id: string; name: string; detail: string } | null>(null)
  const [selectedEdge, setSelectedEdge] = useState<{ id: string; from: string; to: string; cardinality: string } | null>(null)
  const [linkModalOpen, setLinkModalOpen] = useState(false)
  const chartRef = useRef<HTMLDivElement>(null)
  const chartInstance = useRef<echarts.ECharts | null>(null)

  const [fromOdId, setFromOdId] = useState('')
  const [fromPropId, setFromPropId] = useState('')
  const [toOdId, setToOdId] = useState('')
  const [toPropId, setToPropId] = useState('')
  const [cardinality, setCardinality] = useState('1:N')
  const [linkDesc, setLinkDesc] = useState('')

  const { data: objects, loading: objectsLoading, refetch: refetchObjects } = useFetch<OntObjectType>('/ontology/objects')
  const { data: knowledge, loading: knowledgeLoading, refetch: refetchKnowledge } = useFetch<OntKnowledge>('/ontology/knowledge')
  const { data: causalities, refetch: refetchCausalities } = useFetch<OntCausality>('/ontology/causality')
  const { data: odLinks, refetch: refetchOdLinks } = useFetch<OntLinkType>('/ontology/links')
  const { data: learnedFacts } = useFetch<OntLearnedFact>('/ontology/learned-facts')
  const { data: factLinks } = useFetch<OntFactLink>('/ontology/fact-links')

  const refetchAll = useCallback(() => {
    refetchObjects(); refetchKnowledge(); refetchCausalities(); refetchOdLinks()
  }, [refetchObjects, refetchKnowledge, refetchCausalities, refetchOdLinks])

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
          focus: 'adjacency',
          lineStyle: { width: 4 },
          itemStyle: { borderWidth: 3, borderColor: COLORS.textPrimary },
        },
        lineStyle: { opacity: 0.85 },
      }],
    }

    chart.setOption(option, true)

    chart.off('click')
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    chart.on('click', (params: any) => {
      if (params.dataType === 'node' && params.data) {
        setSelectedNode({
          type: params.data._type || '',
          id: params.data._id || '',
          name: params.data.name || '',
          detail: params.data._detail || '',
        })
        setSelectedEdge(null)
      } else if (params.dataType === 'edge' && params.data?._linkId) {
        setSelectedEdge({
          id: params.data._linkId,
          from: params.data._from,
          to: params.data._to,
          cardinality: params.data._cardinality,
        })
        setSelectedNode(null)
      }
    })

    const handleResize = () => chart.resize()
    window.addEventListener('resize', handleResize)
    return () => window.removeEventListener('resize', handleResize)
  }, [markedObjects, propertyOkMap, joinKeyLinks, okToProperty, learnedFacts, factLinks, odLinks])

  useEffect(() => { chartInstance.current?.resize() }, [fullscreen])

  const stats = useMemo(() => {
    const odIdSet = new Set(objects.map(o => o.id))
    const visibleOdLinks = (odLinks || []).filter(
      l => odIdSet.has(l.fromObjectId) && odIdSet.has(l.toObjectId)
    )
    return {
      objects: objects.length,
      objectsActive: markedObjects.length,
      properties: markedObjects.reduce((s, o) => s + (o.properties?.length || 0), 0),
      odLinks: visibleOdLinks.length,
      joinKeys: joinKeyLinks.length,
      okEntries: [...propertyOkMap.values()].length,
      facts: (learnedFacts || []).filter(f => f.confidence !== 'rejected').length,
    }
  }, [objects, markedObjects, odLinks, joinKeyLinks, propertyOkMap, learnedFacts])

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

  const deleteLink = async (id: string) => {
    if (!confirm(t('confirm_delete_link'))) return
    try {
      await api(`/ontology/causality/${id}?projectId=${currentProject?.id}`, { method: 'DELETE' })
      msg.success(t('msg_link_deleted'))
      setSelectedEdge(null)
      refetchAll()
    } catch (e) { msg.error(e instanceof Error ? e.message : t('err_delete_failed')) }
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

  // ─────────────────────────────────────────────────────────────
  // SV Minimal full-screen graph canvas
  //   - 非全屏：依赖 fullHeightExactPaths 白名单放行 AppShell padding
  //   - 全屏：fixed inset-0 z-50，覆盖 Sidebar
  // ─────────────────────────────────────────────────────────────
  return (
    <div
      className={`flex flex-col bg-canvas ${
        fullscreen ? 'fixed inset-0 z-50' : 'h-full min-h-0'
      }`}
    >
      {/* Header */}
      <motion.header
        initial={reduce ? undefined : { opacity: 0, y: -4 }}
        animate={reduce ? undefined : { opacity: 1, y: 0 }}
        transition={{ duration: 0.2, ease: 'easeOut' }}
        className="flex flex-shrink-0 items-center justify-between gap-3 border-b border-border bg-white px-6 py-3 shadow-sm"
      >
        <div className="flex min-w-0 items-center gap-3">
          <Network size={18} className="text-ink" aria-hidden="true" />
          <h1 className="text-base font-semibold tracking-tight text-ink whitespace-nowrap">
            {t('title')}
          </h1>
          <span className="text-xs text-ink-ghost truncate">
            {t('stats', { active: stats.objectsActive, total: stats.objects, props: stats.properties, fk: stats.odLinks, jk: stats.joinKeys, facts: stats.facts })}
          </span>
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
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
          <motion.button
            onClick={() => setFullscreen(!fullscreen)}
            whileHover={reduce ? undefined : { scale: 1.05 }}
            whileTap={reduce ? undefined : { scale: 0.95 }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            aria-label={fullscreen ? t('exit_fullscreen') : t('fullscreen')}
            title={fullscreen ? t('exit_fullscreen') : t('fullscreen')}
            className="inline-flex h-7 w-7 items-center justify-center rounded-md border border-border bg-white text-ink-muted outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
          >
            {fullscreen
              ? <Minimize2 size={14} aria-hidden="true" />
              : <Maximize2 size={14} aria-hidden="true" />}
          </motion.button>
        </div>
      </motion.header>

      {/* Graph + Detail panel */}
      <div className="flex flex-1 min-h-0">
        {loading ? (
          <div className="flex h-full w-full items-center justify-center">
            <InlineLoader text={t('loading')} />
          </div>
        ) : (
          <>
            <div ref={chartRef} className="flex-1 bg-white" />

            <AnimatePresence>
              {selectedNode && (
                <motion.aside
                  key="node-detail"
                  initial={reduce ? undefined : { opacity: 0, x: 20 }}
                  animate={reduce ? undefined : { opacity: 1, x: 0 }}
                  exit={reduce ? undefined : { opacity: 0, x: 20 }}
                  transition={{ duration: 0.2, ease: 'easeOut' }}
                  className="w-72 flex-shrink-0 overflow-y-auto border-l border-border bg-white p-4"
                >
                  <div className="mb-3 flex items-center justify-between">
                    <Badge>
                      {selectedNode.type === 'object' ? t('detail_od') : selectedNode.type === 'fact' ? t('detail_ol') : t('detail_prop')}
                    </Badge>
                    <CloseButton onClick={() => setSelectedNode(null)} />
                  </div>
                  <h3 className="mb-1 text-sm font-semibold text-ink">{selectedNode.name}</h3>
                  <p className="mb-2 text-xs text-ink-muted">{selectedNode.detail}</p>
                  <p className="text-[11px] text-ink-ghost font-mono">
                    ID: {selectedNode.id.slice(0, 8)}…
                  </p>

                  {selectedNode.type === 'property' && joinKeyLinks.length > 0 && (
                    <div className="mt-3 border-t border-border-light pt-3">
                      <div className="mb-1.5 text-[11px] font-medium text-ink-muted">{t('join_links')}</div>
                      {joinKeyLinks.filter(l => {
                        const f = okToProperty.get(l.fromKnowledgeId)
                        const t = okToProperty.get(l.toKnowledgeId)
                        return f?.propId === selectedNode.id || t?.propId === selectedNode.id
                      }).map(l => {
                        const f = okToProperty.get(l.fromKnowledgeId)
                        const t = okToProperty.get(l.toKnowledgeId)
                        return (
                          <div key={l.id} className="py-0.5 text-xs text-ink-muted">
                            {f?.odName}.{f?.propName}
                            <span className="mx-1 font-semibold text-ink">{l.direction}</span>
                            {t?.odName}.{t?.propName}
                          </div>
                        )
                      })}
                    </div>
                  )}
                </motion.aside>
              )}

              {selectedEdge && (
                <motion.aside
                  key="edge-detail"
                  initial={reduce ? undefined : { opacity: 0, x: 20 }}
                  animate={reduce ? undefined : { opacity: 1, x: 0 }}
                  exit={reduce ? undefined : { opacity: 0, x: 20 }}
                  transition={{ duration: 0.2, ease: 'easeOut' }}
                  className="w-72 flex-shrink-0 overflow-y-auto border-l border-border bg-white p-4"
                >
                  <div className="mb-3 flex items-center justify-between">
                    <Badge>join_key</Badge>
                    <CloseButton onClick={() => setSelectedEdge(null)} />
                  </div>
                  <h3 className="mb-2 text-sm font-semibold text-ink">{selectedEdge.cardinality}</h3>
                  <div className="mb-4 space-y-1 text-xs text-ink-muted">
                    <div>{t('edge_from')}<span className="text-ink">{selectedEdge.from}</span></div>
                    <div>{t('edge_to')}<span className="text-ink">{selectedEdge.to}</span></div>
                  </div>
                  <AnimatedButton
                    variant="danger"
                    size="sm"
                    onClick={() => deleteLink(selectedEdge.id)}
                    aria-label={t('delete_link')}
                  >
                    <Trash2 size={12} aria-hidden="true" /> {t('delete_link')}
                  </AnimatedButton>
                </motion.aside>
              )}
            </AnimatePresence>
          </>
        )}
      </div>

      {/* Legend (flex-shrink-0 footer) */}
      <div className="flex flex-shrink-0 flex-wrap items-center gap-4 border-t border-border bg-white px-6 py-2">
        <LegendItem color={COLORS.od} shape="circle" label={t('legend_od')} />
        <LegendItem color={COLORS.property} shape="rect" label={t('legend_prop_ok')} />
        <LegendItem color={COLORS.propertyGhost} shape="rect" label={t('legend_prop_no_ok')} dashed />
        <LegendItem color={COLORS.propertyMc} shape="rect" label={t('legend_mc')} />
        <LegendItem color={COLORS.joinKey} shape="line" label={t('legend_join_key')} />
        <LegendItem color={COLORS.learnedFact} shape="diamond" label={t('legend_fact')} />
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
    </div>
  )
}

// ─────────────────────────────────────────────────────────────
// Sub-components
// ─────────────────────────────────────────────────────────────

function CloseButton({ onClick }: { onClick: () => void }) {
  const reduce = useReducedMotion()
  const t = useTranslations('graph')
  return (
    <motion.button
      onClick={onClick}
      whileHover={reduce ? undefined : { scale: 1.15 }}
      whileTap={reduce ? undefined : { scale: 0.9 }}
      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
      aria-label={t('close')}
      className="inline-flex h-6 w-6 items-center justify-center rounded-full text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
    >
      <X size={14} aria-hidden="true" />
    </motion.button>
  )
}

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
