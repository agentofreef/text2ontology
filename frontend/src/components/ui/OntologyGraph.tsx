'use client'

// OntologyGraph — ECharts force-graph renderer for the lakehouse ontology
// (Od + Property + Ol + join-key / fact-link edges).
//
// Design goals (vs. the previous inlined version in page.tsx):
//   1. Init waits for container to have non-zero dimensions (avoids the
//      "refresh → empty graph" failure mode where echarts.init grabs a 0×0
//      canvas before flex layout has settled).
//   2. Chart instance is disposed on unmount — no leaks across route nav.
//   3. Click + resize handlers are registered exactly once; they read the
//      latest nodes/links/highlight via refs, so data refetches don't wipe
//      selection or re-register handlers.
//   4. setOption uses merge (default) on data updates — force-layout state
//      persists across refetches so nodes don't "jump around".
//   5. Highlight + click-selection are applied as a style overlay on top of
//      the cached base nodes/links; no layout recompute.

import { useEffect, useRef, useState } from 'react'
import * as echarts from 'echarts'
import { useTranslations } from 'next-intl'
import type {
  OntObjectType,
  OntKnowledge,
  OntCausality,
  OntLearnedFact,
  OntFactLink,
  OntLinkType,
} from '@/types/api'

// ─── Types ──────────────────────────────────────────────────────

export interface GraphHighlight {
  kind: 'lookup' | 'smartquery'
  tokens?: string[]
  odNames: string[]
  propertyKeys: Array<{ odName: string; propName: string }>
  okTitles?: string[]
  chain?: Array<Record<string, string>>
}

// Layout modes:
//   force-all       — original force-directed layout, every node (Od + Property + Ol)
//   circular-all    — ECharts circular layout, every node on the ring
//   circular-od     — circular layout, only Od nodes (and the join-key edges between them)
//   circular-od-ol  — circular layout, Od + Ol (hide Property nodes)
//   force-webkit    — webkit-dep style: tight force params + global roam + minimap
//                     thumbnail; property labels are suppressed so Od balls form
//                     the dense puffball look from the official example.
export type GraphLayoutMode =
  | 'force-all'
  | 'circular-all'
  | 'circular-od'
  | 'circular-od-ol'
  | 'force-webkit'

interface Props {
  markedObjects: OntObjectType[]
  // Optional full OD list (mark=true and mark=false). If supplied, the graph
  // shows every OD as a node; mark=true ones are solid filled, mark=false ones
  // appear as ghost outlines so the structural picture stays complete in
  // projects where most ODs haven't been activated yet. Falls back to
  // markedObjects for backward compatibility.
  objects?: OntObjectType[]
  // Optional FK relationships between ODs (ont_link_type). Rendered as
  // accent-coloured edges directly between OD nodes regardless of either
  // endpoint's mark state — graph view is structural, not activation-gated.
  odLinks?: OntLinkType[]
  knowledge: OntKnowledge[]
  causalities: OntCausality[]
  learnedFacts: OntLearnedFact[]
  factLinks: OntFactLink[]
  highlight?: GraphHighlight | null
  className?: string
  layoutMode?: GraphLayoutMode
}

const COLORS = {
  od: '#64748b',
  odGhostFill: '#FFFFFF',
  odGhostBorder: '#A1A1A1',
  property: '#7c3aed',
  joinKey: '#FF4500',
  odLink: '#FF4500',
  ownership: '#94a3b8',
  learnedFact: '#0ea5e9',
  factLink: '#0ea5e9',
}

// ECharts rich-text treats { } | as delimiters; strip them from label text.
const esc = (s: string): string => (s || '').replace(/[{}|]/g, '_')

// Internal shapes (kept loose — ECharts option values are deeply recursive)
/* eslint-disable @typescript-eslint/no-explicit-any */
type GraphNode = {
  id: string
  name: string
  parentOdName?: string  // for 'prop:*' nodes — used by highlight resolver
  symbolSize: number
  category: number
  symbol: string
  itemStyle: any
  label: any
  tooltip: any
}
type GraphLink = {
  source: string
  target: string
  lineStyle: any
  label?: any
  tooltip?: any
}

// MAX_GRAPH_NODES caps how many nodes the force layout renders. The ECharts
// force-graph cost grows super-linearly with node count; past a few thousand
// nodes the layout pins the main thread and the page locks up. OD nodes are
// appended first (then properties, then Ol facts), so the cap preserves the
// structurally most-important nodes and drops the long tail. Links whose
// endpoints were dropped are filtered out so no edge dangles.
const MAX_GRAPH_NODES = 1500

// capGraph trims nodes to MAX_GRAPH_NODES and removes any link that references a
// dropped node. Returns the (possibly) trimmed graph plus whether a cap applied.
function capGraph(
  nodes: GraphNode[],
  links: GraphLink[],
): { nodes: GraphNode[]; links: GraphLink[]; capped: boolean } {
  if (nodes.length <= MAX_GRAPH_NODES) return { nodes, links, capped: false }
  const kept = nodes.slice(0, MAX_GRAPH_NODES)
  const keptIds = new Set(kept.map(n => n.id))
  const keptLinks = links.filter(l => keptIds.has(l.source) && keptIds.has(l.target))
  return { nodes: kept, links: keptLinks, capped: true }
}

// ─── Component ──────────────────────────────────────────────────

export function OntologyGraph({
  markedObjects,
  objects,
  odLinks,
  knowledge,
  causalities,
  learnedFacts,
  factLinks,
  highlight,
  className,
  layoutMode = 'force-all',
}: Props) {
  const t = useTranslations('ui')
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<echarts.ECharts | null>(null)
  const nodesRef = useRef<GraphNode[]>([])
  const linksRef = useRef<GraphLink[]>([])
  const selectedRef = useRef<string | null>(null)
  const highlightRef = useRef<GraphHighlight | null | undefined>(highlight)
  // Track the previously rendered layout mode. When it changes we pass
  // `replace: true` to setOption so stale force-layout x/y don't leak into
  // a circular render (or vice-versa). On same-mode refetches we keep the
  // default merge so user-dragged node positions survive.
  const prevLayoutModeRef = useRef<GraphLayoutMode | null>(null)
  // `inited` flips true once echarts.init succeeds, forcing the data effect
  // to re-run with a now-valid chart instance. The initial attempt may fail
  // if the container is 0×0 at mount (flex still measuring), in which case
  // we retry via requestAnimationFrame.
  const [inited, setInited] = useState(false)

  // keep highlight ref current for handlers that were registered once
  useEffect(() => { highlightRef.current = highlight }, [highlight])

  // ─── Mount: init + wire interactions + resize + dispose ──────
  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    let cancelled = false
    let rafId = 0

    const handleClick = (params: any) => {
      if (!chartRef.current) return
      if (params.dataType === 'node' && params.data?.id) {
        const id = String(params.data.id)
        selectedRef.current = selectedRef.current === id ? null : id
      } else if (
        params.dataType === 'edge' &&
        params.data?.source &&
        params.data?.target
      ) {
        const key = `edge:${params.data.source}->${params.data.target}`
        selectedRef.current = selectedRef.current === key ? null : key
      } else {
        return
      }
      applyStyle()
    }

    const handleBlank = (e: { target?: unknown }) => {
      if (!e.target && selectedRef.current) {
        selectedRef.current = null
        applyStyle()
      }
    }

    const tryInit = () => {
      if (cancelled || chartRef.current) return
      if (el.clientWidth === 0 || el.clientHeight === 0) {
        rafId = requestAnimationFrame(tryInit)
        return
      }
      chartRef.current = echarts.init(el, undefined, { renderer: 'canvas' })
      chartRef.current.on('click', handleClick)
      chartRef.current.getZr().on('click', handleBlank)
      setInited(true)
    }
    tryInit()

    const onResize = () => chartRef.current?.resize()
    const ro = new ResizeObserver(onResize)
    ro.observe(el)
    window.addEventListener('resize', onResize)

    return () => {
      cancelled = true
      if (rafId) cancelAnimationFrame(rafId)
      ro.disconnect()
      window.removeEventListener('resize', onResize)
      if (chartRef.current) {
        chartRef.current.dispose()
        chartRef.current = null
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // ─── Data / highlight / layout change → rebuild & render ─────
  useEffect(() => {
    if (!inited || !chartRef.current) return
    renderGraph()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    inited,
    markedObjects,
    objects,
    odLinks,
    knowledge,
    causalities,
    learnedFacts,
    factLinks,
    highlight,
    layoutMode,
  ])

  // ─── Helpers (closures over current props) ───────────────────

  function buildBaseGraph(): { nodes: GraphNode[]; links: GraphLink[] } {
    const propertyOkMap = new Map<string, OntKnowledge>()
    for (const k of knowledge) {
      if (k.anchorType === 'property' && k.anchorId) {
        propertyOkMap.set(k.anchorId, k)
      }
    }
    const okToProperty = new Map<
      string,
      { propName: string; odName: string; propId: string }
    >()
    for (const obj of markedObjects) {
      for (const p of obj.properties || []) {
        const ok = propertyOkMap.get(p.id)
        if (ok) {
          okToProperty.set(ok.id, {
            propName: p.name,
            odName: obj.name,
            propId: p.id,
          })
        }
      }
    }
    const joinKeyLinks = causalities.filter(c => c.relationType === 'join_key')

    // Full OD set: prefer the explicit `objects` prop (includes mark=false) so
    // the structural graph stays complete; fall back to markedObjects for
    // backward compatibility with callers that haven't migrated yet.
    const allObjects: OntObjectType[] = (objects && objects.length > 0) ? objects : markedObjects
    const markedIds = new Set(markedObjects.map(o => o.id))

    const nodes: GraphNode[] = []
    const links: GraphLink[] = []

    // OD nodes — render every OD. mark=true gets the standard solid fill +
    // size-by-property-count; mark=false renders as a small ghost outline so
    // FK edges into unactivated entities still have an endpoint to land on.
    for (const obj of allObjects) {
      const propCount = obj.properties?.length || 0
      const mapped = obj.properties?.filter(p => p.sourceColumn).length || 0
      const isActive = markedIds.has(obj.id)
      nodes.push({
        id: `od:${obj.id}`,
        name: obj.name,
        symbolSize: isActive ? Math.max(30, Math.min(55, 30 + propCount * 2.5)) : 22,
        category: 0,
        symbol: 'circle',
        itemStyle: {
          color: isActive ? COLORS.od : COLORS.odGhostFill,
          borderColor: isActive ? '#E5E5E5' : COLORS.odGhostBorder,
          borderWidth: isActive ? 1 : 1.2,
          borderType: isActive ? 'solid' : 'dashed',
        },
        label: {
          show: true,
          formatter: isActive
            ? `{name|${esc(obj.name)}}\n{tag|${esc((obj.kind || '').toUpperCase())}}  {props|${mapped}/${propCount}p}`
            : `{ghost|${esc(obj.name)}}`,
          rich: {
            name: { fontSize: 12, fontWeight: 'bold', fontFamily: 'JetBrains Mono, monospace', color: '#000', lineHeight: 16 },
            tag: { fontSize: 8, fontFamily: 'JetBrains Mono, monospace', color: '#333' },
            props: { fontSize: 8, fontFamily: 'JetBrains Mono, monospace', color: '#333' },
            ghost: { fontSize: 10, fontFamily: 'JetBrains Mono, monospace', color: '#666', lineHeight: 12 },
          },
        },
        tooltip: {
          formatter: [
            `<b>${esc(obj.name)}</b> [${esc(obj.kind || '')}]`,
            `Properties: ${propCount}`,
            `Mapped: ${mapped}`,
            isActive ? '' : `<span style="color:#A1A1A1">${t('graph_node_inactive')}</span>`,
          ].filter(Boolean).join('<br/>'),
        },
      })
    }

    // Property nodes + ownership edges — only for marked ODs (avoids 200-node
    // explosion in projects where most ODs are still mark=false).
    for (const obj of markedObjects) {
      for (const p of obj.properties || []) {
        const ok = propertyOkMap.get(p.id)
        const hasOk = !!ok
        const isMC = !!p.isMachineCode
        nodes.push({
          id: `prop:${p.id}`,
          name: p.name,
          parentOdName: obj.name,
          symbolSize: 16,
          category: 1,
          symbol: 'rect',
          itemStyle: {
            color: isMC ? '#f59e0b' : COLORS.property,
            borderColor: hasOk ? COLORS.property : '#ccc',
            borderWidth: hasOk ? 1.5 : 1,
            borderType: hasOk ? 'solid' : 'dashed',
          },
          label: {
            show: true,
            formatter: `{name|${esc(p.name)}}`,
            rich: { name: { fontSize: 9, fontFamily: 'JetBrains Mono, monospace', color: '#000', lineHeight: 12 } },
          },
          tooltip: {
            formatter: [
              `<b>${esc(p.name)}</b>`,
              `Type: ${esc(p.dataType || 'text')}`,
              p.sourceColumn ? `Source: ${esc(p.sourceColumn)}` : '',
              isMC ? '<span style="color:#f59e0b">Machine Code</span>' : '',
              hasOk ? '<span style="color:#7c3aed">Has Ok entry</span>' : '<span style="color:#ccc">No Ok entry</span>',
            ].filter(Boolean).join('<br/>'),
          },
        })

        links.push({
          source: `od:${obj.id}`,
          target: `prop:${p.id}`,
          lineStyle: { color: COLORS.ownership, width: 1, type: 'dotted', opacity: 0.4 },
          label: { show: false },
        })
      }
    }

    // Join-key edges (Property → Property)
    for (const link of joinKeyLinks) {
      const fromInfo = okToProperty.get(link.fromKnowledgeId)
      const toInfo = okToProperty.get(link.toKnowledgeId)
      if (!fromInfo || !toInfo) continue
      links.push({
        source: `prop:${fromInfo.propId}`,
        target: `prop:${toInfo.propId}`,
        lineStyle: {
          color: COLORS.joinKey,
          width: 3,
          type: 'solid',
          curveness: 0.15,
          opacity: 0.8,
        },
        label: {
          show: true,
          formatter: link.direction || '1:N',
          fontSize: 10,
          fontWeight: 'bold',
          fontFamily: 'JetBrains Mono, monospace',
          color: '#FF4500',
        },
        tooltip: {
          formatter: `${esc(fromInfo.odName)}.${esc(fromInfo.propName)} → ${esc(toInfo.odName)}.${esc(toInfo.propName)}<br/><b>${esc(link.direction || '1:N')}</b>`,
        },
      })
    }

    // OD → OD FK edges from ont_link_type. Endpoint mark is NOT a filter —
    // graph view is structural, mark is runtime activation. Visual distinction
    // (solid vs dashed) reflects the link's own mark state, not the endpoints'.
    const allOdIdSet = new Set(allObjects.map(o => o.id))
    for (const l of (odLinks || [])) {
      if (!allOdIdSet.has(l.fromObjectId) || !allOdIdSet.has(l.toObjectId)) continue
      const isPending = !l.mark
      links.push({
        source: `od:${l.fromObjectId}`,
        target: `od:${l.toObjectId}`,
        lineStyle: {
          color: COLORS.odLink,
          width: isPending ? 1.5 : 2.5,
          type: isPending ? 'dashed' : 'solid',
          curveness: 0.2,
          opacity: isPending ? 0.55 : 0.9,
        },
        label: {
          show: true,
          formatter: l.fkColumn || l.linkName || (l.cardinality === 'many_to_one' ? 'N:1' : l.cardinality),
          fontSize: 9,
          fontWeight: 'bold',
          fontFamily: 'JetBrains Mono, monospace',
          color: COLORS.odLink,
        },
        tooltip: {
          formatter: [
            `<b>${esc(l.fromObjectName)} → ${esc(l.toObjectName)}</b>`,
            l.fkColumn ? `FK: ${esc(l.fkColumn)}` : '',
            `Cardinality: ${esc(l.cardinality)}`,
            l.description ? esc(l.description) : '',
            isPending ? `<span style="color:#A1A1A1">${t('graph_link_pending')}</span>` : '',
          ].filter(Boolean).join('<br/>'),
        },
      })
    }

    // Ol (learned facts) — diamond, sky blue
    const activeFacts = (learnedFacts || []).filter(f => f.confidence !== 'rejected')
    for (const fact of activeFacts) {
      const label = fact.title || (fact.summary?.length > 20 ? fact.summary.slice(0, 20) + '…' : fact.summary) || 'Ol'
      nodes.push({
        id: `ol:${fact.id}`,
        name: label,
        symbolSize: 18,
        category: 2,
        symbol: 'diamond',
        itemStyle: {
          color: COLORS.learnedFact,
          borderColor: fact.confidence === 'pending' ? '#f59e0b' : '#E5E5E5',
          borderWidth: fact.confidence === 'pending' ? 2 : 1,
        },
        label: {
          show: true,
          formatter: `{name|${esc(label)}}`,
          rich: { name: { fontSize: 8, fontFamily: 'JetBrains Mono, monospace', color: '#000', lineHeight: 10 } },
        },
        tooltip: {
          formatter: [
            `<b>${esc(fact.title || t('graph_fact_no_title'))}</b>`,
            esc(fact.summary || ''),
            `Type: ${esc(fact.factType || 'business_rule')}`,
            `Status: ${esc(fact.confidence)}`,
          ].filter(Boolean).join('<br/>'),
        },
      })
    }

    // Fact-link edges
    const nodeIdSet = new Set(nodes.map(n => n.id))
    for (const fl of factLinks || []) {
      const sourceId = `ol:${fl.factId}`
      let targetId = ''
      if (fl.targetType === 'object') targetId = `od:${fl.targetId}`
      else if (fl.targetType === 'property') targetId = `prop:${fl.targetId}`
      else if (fl.targetType === 'fact') targetId = `ol:${fl.targetId}`
      if (!targetId || !nodeIdSet.has(sourceId) || !nodeIdSet.has(targetId)) continue
      links.push({
        source: sourceId,
        target: targetId,
        lineStyle: { color: COLORS.factLink, width: 1.5, type: 'dashed', opacity: 0.7 },
        label: { show: false },
        tooltip: { formatter: `${esc(fl.role || 'about')}` },
      })
    }

    // Cap the node count so a very large ontology can't freeze the force layout.
    const capped = capGraph(nodes, links)
    if (capped.capped && typeof console !== 'undefined') {
      console.warn(
        `OntologyGraph: ${nodes.length} nodes exceeds the ${MAX_GRAPH_NODES} render cap; showing the first ${MAX_GRAPH_NODES}.`,
      )
    }
    return { nodes: capped.nodes, links: capped.links }
  }

  // Style overlay: applies highlight (from props) + click-selection (ref) on
  // top of cached base nodes/links. Returns fresh arrays so ECharts picks up
  // the new itemStyle / lineStyle values on the next setOption call.
  function styleOverlay(baseNodes: GraphNode[], baseLinks: GraphLink[]) {
    const hl = highlightRef.current
    const sel = selectedRef.current

    // 1) Click selection → adjacency lookup
    let adjNodes: Set<string> | null = null
    let adjEdge: { src: string; tgt: string } | null = null
    if (sel?.startsWith('edge:')) {
      const rest = sel.slice('edge:'.length)
      const arrow = rest.indexOf('->')
      if (arrow > 0) {
        adjEdge = { src: rest.slice(0, arrow), tgt: rest.slice(arrow + 2) }
        adjNodes = new Set([adjEdge.src, adjEdge.tgt])
      }
    } else if (sel) {
      adjNodes = new Set([sel])
      for (const l of baseLinks) {
        if (l.source === sel) adjNodes.add(l.target)
        if (l.target === sel) adjNodes.add(l.source)
      }
    }

    // 2) Highlight from tool-trace
    const hlOdNames = new Set<string>(hl?.odNames || [])
    const hlPropKeys = new Set<string>()
    const odsWithPropHL = new Set<string>()
    for (const pk of hl?.propertyKeys || []) {
      hlPropKeys.add(`${pk.odName}.${pk.propName}`)
      odsWithPropHL.add(pk.odName)
    }
    const hlActive = hlOdNames.size > 0 || hlPropKeys.size > 0

    const isNodeHL = (n: GraphNode): boolean => {
      if (!hlActive) return false
      if (n.id.startsWith('od:')) return hlOdNames.has(n.name)
      if (n.id.startsWith('prop:')) {
        const parent = n.parentOdName || ''
        const key = `${parent}.${n.name}`
        return hlPropKeys.has(key) || (hlOdNames.has(parent) && !odsWithPropHL.has(parent))
      }
      return false // Ol nodes aren't filtered by tool-trace
    }

    const styledNodes = baseNodes.map(n => {
      const hlMatch = isNodeHL(n)
      // Visibility: click-selection (if present) dominates; otherwise highlight
      // filter dims non-matching nodes (except Ol, which stays visible).
      let visible = true
      if (adjNodes) {
        visible = adjNodes.has(n.id)
      } else if (hlActive && !n.id.startsWith('ol:')) {
        visible = hlMatch
      }
      const emphasised = (adjNodes?.has(n.id)) || hlMatch
      return {
        ...n,
        itemStyle: {
          ...n.itemStyle,
          opacity: visible ? 1 : 0.08,
          borderColor: emphasised ? '#FF4500' : n.itemStyle.borderColor,
          borderWidth: emphasised
            ? Math.max(2.5, Number(n.itemStyle.borderWidth) || 1)
            : n.itemStyle.borderWidth,
        },
        label: { ...n.label, show: visible },
      }
    })

    const nodeVisible = new Map<string, boolean>()
    for (const n of styledNodes) nodeVisible.set(n.id, n.itemStyle.opacity !== 0.08)

    const styledLinks = baseLinks.map(l => {
      let visible = true
      if (adjEdge) {
        visible = l.source === adjEdge.src && l.target === adjEdge.tgt
      } else if (adjNodes) {
        visible = adjNodes.has(l.source) || adjNodes.has(l.target)
      } else if (hlActive) {
        visible = !!(nodeVisible.get(l.source) && nodeVisible.get(l.target))
      }
      const baseOpacity = Number(l.lineStyle?.opacity ?? 0.8)
      return {
        ...l,
        lineStyle: {
          ...l.lineStyle,
          opacity: visible ? baseOpacity : 0.05,
        },
      }
    })

    return { nodes: styledNodes, links: styledLinks }
  }

  // Drop nodes (and any edges that touch them) that the current layoutMode
  // hides. Keeps `prop:` nodes for force-all + circular-all; for the two
  // Od-only / Od+Ol circular variants we strip everything else so the ring
  // shows only the high-level entities.
  function applyLayoutFilter(
    nodes: GraphNode[],
    links: GraphLink[],
  ): { nodes: GraphNode[]; links: GraphLink[] } {
    if (
      layoutMode === 'force-all' ||
      layoutMode === 'circular-all' ||
      layoutMode === 'force-webkit'
    ) {
      return { nodes, links }
    }
    const keep = (id: string): boolean => {
      if (id.startsWith('od:')) return true
      if (id.startsWith('ol:')) return layoutMode === 'circular-od-ol'
      return false
    }
    const keptNodes = nodes.filter(n => keep(n.id))
    const keptIds = new Set(keptNodes.map(n => n.id))
    const keptLinks = links.filter(
      l => keptIds.has(String(l.source)) && keptIds.has(String(l.target)),
    )
    return { nodes: keptNodes, links: keptLinks }
  }

  function renderGraph() {
    const chart = chartRef.current
    if (!chart) return
    const built = buildBaseGraph()
    const base = applyLayoutFilter(built.nodes, built.links)
    nodesRef.current = base.nodes
    linksRef.current = base.links

    if (base.nodes.length === 0) {
      // Keep instance alive, but drop graph data. Using merge so the outer
      // tooltip / legend options (set on first render) are preserved.
      try {
        chart.setOption({ series: [{ type: 'graph', data: [], links: [] }] })
      } catch {
        chart.clear()
      }
      return
    }

    const categories = [
      { name: 'Object (Od)', itemStyle: { color: COLORS.od } },
      { name: 'Property (Ok)', itemStyle: { color: COLORS.property } },
      { name: 'Learned Fact (Ol)', itemStyle: { color: COLORS.learnedFact } },
    ]
    let styled = styleOverlay(base.nodes, base.links)

    // Webkit-dep aesthetic: hide Property labels so the Od / Ol balls cluster
    // form the puffball look from the official example. Per-node label.show
    // overrides series-level label.show, so we patch each prop:* node here.
    if (layoutMode === 'force-webkit') {
      styled = {
        ...styled,
        nodes: styled.nodes.map(n =>
          n.id.startsWith('prop:')
            ? { ...n, label: { ...n.label, show: false } }
            : n,
        ),
      }
    }

    // Per-mode series knobs. Force-webkit mirrors the official graph-webkit-dep
    // example: 'force' layout, animation off, very tight params (edgeLength=5,
    // repulsion=20, gravity=0.2), label position=right, plus a thumbnail
    // minimap and global roam trigger with zoom bounds.
    const isForce = layoutMode === 'force-all' || layoutMode === 'force-webkit'
    const isWebkit = layoutMode === 'force-webkit'
    const seriesForce = isWebkit
      ? { edgeLength: 5, repulsion: 20, gravity: 0.2 }
      : { repulsion: 300, edgeLength: [50, 140], gravity: 0.08, friction: 0.6 }
    const seriesLineStyle = isForce
      ? (isWebkit ? { opacity: 0.7, width: 1, color: 'source' } : { opacity: 0.8 })
      : { opacity: 0.8, curveness: 0.3 }
    const seriesLabel = isWebkit
      ? { show: true, position: 'right', formatter: '{b}', distance: 4 }
      : { show: true, position: 'bottom', distance: 6 }

    try {
      // Merge = default; force-layout state (node positions, user drags) is
      // preserved across refetches for nodes whose id is unchanged.
      chart.setOption({
        tooltip: {
          trigger: 'item',
          backgroundColor: '#fff',
          borderColor: '#E5E5E5',
          borderWidth: 1,
          textStyle: { fontFamily: 'JetBrains Mono, monospace', fontSize: 10, color: '#000' },
        },
        legend: {
          data: categories.map(c => c.name),
          bottom: 5,
          left: 'center',
          textStyle: { fontFamily: 'JetBrains Mono, monospace', fontSize: 9, color: '#666' },
          itemWidth: 10,
          itemHeight: 10,
        },
        // Webkit-dep mode disables animation for snappier render on dense
        // clusters — matches the official example's `animation: false`.
        animationDuration: isWebkit ? 0 : 300,
        // Minimap thumbnail (ECharts ≥6 / 5.6+) — only for webkit mode.
        ...(isWebkit
          ? {
              thumbnail: {
                width: '15%',
                height: '15%',
                windowStyle: {
                  color: 'rgba(140, 212, 250, 0.5)',
                  borderColor: 'rgba(30, 64, 175, 0.7)',
                  opacity: 1,
                },
              },
            }
          : {}),
        series: [
          {
            type: 'graph',
            // ECharts treats `layout` as a one-shot config; switching modes
            // triggers a full position recompute (we set notMerge=true below
            // to make sure stale x/y don't leak across mode transitions).
            layout: isForce ? 'force' : 'circular',
            data: styled.nodes,
            links: styled.links,
            categories,
            roam: true,
            // Webkit-dep uses `roamTrigger: 'global'` + scaleLimit so the
            // minimap window can drive pan/zoom across the whole canvas.
            ...(isWebkit
              ? { roamTrigger: 'global', scaleLimit: { max: 8, min: 0.5 } }
              : {}),
            // Both force modes support drag; circular fixes nodes on the ring.
            draggable: isForce,
            // Webkit: tight cluster params; default force: original spacing.
            force: seriesForce,
            // Rotate labels along the ring tangent for circular modes —
            // mirrors the official graph-circular-layout example.
            circular: { rotateLabel: true },
            edgeSymbol: ['none', 'arrow'],
            edgeSymbolSize: [0, 7],
            label: seriesLabel,
            emphasis: {
              focus: 'none',
              lineStyle: { width: 3 },
              itemStyle: { borderWidth: 3, borderColor: '#FF4500' },
              // In webkit mode default labels are sparse, so make hover bring
              // back a readable tag for context.
              ...(isWebkit ? { label: { show: true } } : {}),
            },
            // Webkit: edges colored by source category (signature look).
            // Circular: curved edges read better than straight chords.
            // Force-all: keep the original opacity-only style.
            lineStyle: seriesLineStyle,
          },
        ],
      // notMerge = true ONLY on layout-mode transitions; same-mode refetches
      // use the default merge so force-layout drag positions persist.
      }, prevLayoutModeRef.current !== null && prevLayoutModeRef.current !== layoutMode)
      prevLayoutModeRef.current = layoutMode
    } catch (e) {
      console.warn('OntologyGraph render error:', e)
    }
  }

  // Style-only update path — invoked by click handler. Reuses cached base
  // nodes/links; does NOT rebuild or touch force-layout state.
  function applyStyle() {
    const chart = chartRef.current
    if (!chart) return
    if (nodesRef.current.length === 0) return
    const styled = styleOverlay(nodesRef.current, linksRef.current)
    try {
      chart.setOption({ series: [{ data: styled.nodes, links: styled.links }] })
    } catch (e) {
      console.warn('OntologyGraph style patch error:', e)
    }
  }

  return <div ref={containerRef} className={className} />
}
