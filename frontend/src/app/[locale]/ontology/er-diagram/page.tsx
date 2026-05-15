'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback, useMemo } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type Edge,
  type NodeTypes,
  useNodesState,
  useEdgesState,
  MarkerType,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import dagre from '@dagrejs/dagre'
import { useProject } from '@/lib/project'
import { api } from '@/lib/api'
import { useStyleMode } from '@/lib/style-mode'
import { Button } from '@/components/ui/Button'
import { TableNode } from './components/TableNode'
import { SidePanel } from './components/SidePanel'
import { AddTableDialog } from './components/AddTableDialog'
import type { OntObjectType, OntLinkType, ErNode, ErEdge, ImportProgressResponse, PbitTablePreview } from '@/types/api'
import { MotionFade, DataLoader } from '@/lib/motion'
import { Plus, Network } from 'lucide-react'

const nodeTypes: NodeTypes = {
  erTable: TableNode as unknown as NodeTypes['erTable'],
}

const NODE_WIDTH = 220
const NODE_HEIGHT = 100

function layoutGraph(nodes: Node[], edges: Edge[]): Node[] {
  const g = new dagre.graphlib.Graph()
  g.setDefaultEdgeLabel(() => ({}))
  g.setGraph({ rankdir: 'LR', nodesep: 60, ranksep: 100 })

  for (const node of nodes) {
    g.setNode(node.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
  }
  for (const edge of edges) {
    g.setEdge(edge.source, edge.target)
  }

  dagre.layout(g)

  return nodes.map((node) => {
    const pos = g.node(node.id)
    return {
      ...node,
      position: {
        x: pos.x - NODE_WIDTH / 2,
        y: pos.y - NODE_HEIGHT / 2,
      },
    }
  })
}

export default function ErDiagramPageMinimal() {
  const t = useTranslations('er')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])
  const [selectedNode, setSelectedNode] = useState<ErNode | null>(null)
  const [selectedEdge, setSelectedEdge] = useState<ErEdge | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [hideIslands, setHideIslands] = useState(true)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [edgeMap, setEdgeMap] = useState<Map<string, ErEdge>>(new Map())

  const fetchDiagram = useCallback(async () => {
    if (!currentProject) return
    setLoading(true)
    setError('')
    try {
      const proj = currentProject as unknown as Record<string, unknown>
      const isLakehouse = proj.sourceType === 'pbit-lakehouse' || proj.sourceType === 'pbix-lakehouse' || proj.pbitConfig != null

      let erNodes: ErNode[] = []
      let erEdges: ErEdge[] = []
      let linkIdToFromTo: Map<string, { from: string; to: string }> = new Map()

      if (isLakehouse) {
        const progress = await api<ImportProgressResponse>(
          `/connector/pbit/progress?projectId=${currentProject.id}`
        )
        const cfg = progress.pbitConfig
        if (!cfg) {
          setError('No PBIT config found. Upload a PBIT file first.')
          setLoading(false)
          return
        }

        const statusMap = new Map<string, { status: string; rowCount?: number }>()
        for (const t of progress.tables || []) {
          statusMap.set(t.tableName, { status: t.status, rowCount: t.rowCount })
        }

        const sourceToOrigin = (t: PbitTablePreview): ErNode['origin'] => {
          if (t.sourceType === 'derived') return 'derived-view'
          if (t.sourceType === 'constant') return 'derived-view'
          if (t.sourceType === 'unsupported') return ''
          if (t.sourceType === 'pbix') return 'pbix-data'
          if (t.sourceType === 'calculated') return 'derived-view'
          return 'pbit-bootstrap'
        }

        const sourceToWarning = (t: PbitTablePreview): string | undefined => {
          const st = statusMap.get(t.name)
          if (t.sourceType === 'unsupported') return 'unsupported M expression'
          if (st?.status === 'error') return 'load error'
          if (st?.status === 'pending') return 'data not yet uploaded'
          return undefined
        }

        const nameToId = new Map<string, string>()
        cfg.tables.forEach((t, i) => { nameToId.set(t.name, `pbit-${i}`) })

        erNodes = cfg.tables.map((t, i) => ({
          id: `pbit-${i}`,
          label: t.name,
          rowCount: statusMap.get(t.name)?.rowCount ?? null,
          columnCount: t.columnCount,
          origin: sourceToOrigin(t),
          warning: sourceToWarning(t),
          columns: (t.columns || []).map((c) => ({
            name: c.name || (c as Record<string, string>)['name'] || '',
            dataType: c.dataType || (c as Record<string, string>)['dataType'] || 'text',
          })),
        }))

        erEdges = (cfg.relationships || []).map((r, i) => {
          const fromId = nameToId.get(r.fromTable) || ''
          const toId = nameToId.get(r.toTable) || ''
          const edgeId = `rel-${i}`
          linkIdToFromTo.set(edgeId, { from: fromId, to: toId })
          return {
            id: edgeId,
            fromTable: r.fromTable,
            toTable: r.toTable,
            fromColumn: r.fromColumn,
            toColumn: r.toColumn,
            cardinality: (r.cardinality as ErEdge['cardinality']) || 'M:1',
            isActive: r.isActive,
          }
        })
      } else {
        const [objectsRes, linksRes] = await Promise.all([
          api<{ data: OntObjectType[] }>(`/ontology/objects?projectId=${currentProject.id}`),
          api<{ data: OntLinkType[] }>(`/ontology/links?projectId=${currentProject.id}`),
        ])

        const objects = objectsRes.data || []
        const links = linksRes.data || []

        erNodes = objects.map((o) => ({
          id: o.id,
          label: o.displayName || o.name,
          rowCount: null,
          columnCount: o.properties?.length ?? 0,
          origin: (o as OntObjectType & { origin?: ErNode['origin'] }).origin || '',
          warning: undefined,
          columns: (o.properties || []).map((p) => ({
            name: p.displayName || p.name,
            dataType: p.dataType,
          })),
        }))

        erEdges = links.map((l) => {
          linkIdToFromTo.set(l.id, { from: l.fromObjectId, to: l.toObjectId })
          return {
            id: l.id,
            fromTable: l.fromObjectName,
            toTable: l.toObjectName,
            fromColumn: l.fkColumn || '',
            toColumn: '',
            cardinality: (l.cardinality as ErEdge['cardinality']) || 'M:1',
            isActive: l.mark,
          }
        })
      }

      const newEdgeMap = new Map<string, ErEdge>()
      for (const e of erEdges) { newEdgeMap.set(e.id, e) }
      setEdgeMap(newEdgeMap)

      const connectedIds = new Set<string>()
      for (const e of erEdges) {
        const ft = linkIdToFromTo.get(e.id)
        if (ft) { connectedIds.add(ft.from); connectedIds.add(ft.to) }
      }
      const visibleNodes = hideIslands ? erNodes.filter((n) => connectedIds.has(n.id)) : erNodes

      const flowNodes: Node[] = visibleNodes.map((n) => ({
        id: n.id,
        type: 'erTable',
        position: { x: 0, y: 0 },
        data: n as unknown as Record<string, unknown>,
      }))

      const flowEdges: Edge[] = erEdges.map((e) => {
        const ft = linkIdToFromTo.get(e.id)
        return {
          id: e.id,
          source: ft?.from || '',
          target: ft?.to || '',
          label: e.cardinality,
          style: { stroke: e.isActive ? '#374151' : '#D1D5DB', strokeWidth: 1.5 },
          markerEnd: { type: MarkerType.ArrowClosed, color: e.isActive ? '#374151' : '#D1D5DB' },
          labelStyle: { fontFamily: 'sans-serif', fontSize: 10, fill: '#6B7280' },
          labelBgStyle: { fill: '#FFFFFF', fillOpacity: 0.9 },
        }
      })

      const laid = layoutGraph(flowNodes, flowEdges)
      setNodes(laid)
      setEdges(flowEdges)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load diagram')
    } finally {
      setLoading(false)
    }
  }, [currentProject, setNodes, setEdges, hideIslands])

  useEffect(() => { fetchDiagram() }, [fetchDiagram])

  const handleNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    setSelectedEdge(null)
    setSelectedNode(node.data as unknown as ErNode)
  }, [])

  const handleEdgeClick = useCallback((_: React.MouseEvent, edge: Edge) => {
    setSelectedNode(null)
    const erEdge = edgeMap.get(edge.id)
    if (erEdge) setSelectedEdge(erEdge)
  }, [edgeMap])

  const handlePaneClick = useCallback(() => {
    setSelectedNode(null)
    setSelectedEdge(null)
  }, [])

  const sidePanelSelected = useMemo<ErNode | ErEdge | null>(() => {
    if (selectedNode) return selectedNode
    if (selectedEdge) return selectedEdge
    return null
  }, [selectedNode, selectedEdge])

  if (!currentProject) {
    return (
      <div className="flex items-center justify-center h-full p-8">
        <div className="text-sm text-gray-400">No project selected.</div>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      {/* Header — h-14 to align with Sidebar brand row; industrial uses 2px ink rule */}
      <MotionFade className={`flex h-14 items-center justify-between bg-white px-6 ${industrial ? 'border-b-2 border-ink' : 'border-b border-gray-100'}`}>
        <div className="flex items-center gap-3">
          {industrial ? (
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // LAKEHOUSE ER DIAGRAM
            </span>
          ) : (
            <>
              <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-gray-100">
                <Network size={16} className="text-gray-600" />
              </div>
              <div>
                <h1 className="font-sans text-sm font-semibold text-gray-900">Lakehouse ER Diagram</h1>
                {!loading && (
                  <p className="text-xs text-gray-400">{nodes.length} tables · {edges.length} relationships</p>
                )}
              </div>
            </>
          )}
          {industrial && !loading && (
            <span className="font-mono text-[10px] tracking-[0.14em] text-ink-muted tabular-nums">
              {nodes.length} TABLES · {edges.length} RELATIONSHIPS
            </span>
          )}
          <div className={`flex items-center gap-3 ml-2 pl-3 ${industrial ? 'border-l border-ink/30' : 'border-l border-gray-100'}`}>
            {[
              ['PBIX',    industrial ? 'bg-ink'      : 'bg-blue-200'],
              ['EXCEL',   industrial ? 'bg-ink/60'   : 'bg-green-200'],
              ['DERIVED', industrial ? 'bg-ink/30'   : 'bg-gray-300'],
            ].map(([label, dotCls]) => (
              <div key={label} className="flex items-center gap-1">
                <span className={`inline-block h-2 w-2 ${industrial ? '' : 'rounded-full'} ${dotCls}`} />
                <span className={industrial ? 'font-mono text-[9px] tracking-[0.18em] text-ink-muted' : 'text-[9px] text-gray-400'}>
                  {industrial ? label : label.charAt(0) + label.slice(1).toLowerCase()}
                </span>
              </div>
            ))}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button variant={hideIslands ? 'primary' : 'ghost'} size="sm" onClick={() => setHideIslands((v) => !v)}>
            {hideIslands ? t('show_all') : t('hide_islands')}
          </Button>
          <Button variant="ghost" size="sm" onClick={fetchDiagram} disabled={loading}>Refresh</Button>
          <Button variant="primary" size="sm" onClick={() => setAddOpen(true)} disabled={loading}>
            <Plus size={14} /> Add Table
          </Button>
        </div>
      </MotionFade>

      {error && (
        <div
          className={`px-6 py-2 text-xs text-red-600 ${
            industrial ? 'border-b-2 border-danger bg-danger/5 font-mono tracking-[0.06em]' : 'border-b border-red-100 bg-red-50'
          }`}
        >
          {industrial ? `// ERROR · ${error}` : error}
        </div>
      )}

      <DataLoader loading={loading} message={t('loading')} minHeight="50vh">
      <div className="flex flex-1 overflow-hidden">
        <div className="flex-1 relative">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onNodeClick={handleNodeClick}
            onEdgeClick={handleEdgeClick}
            onPaneClick={handlePaneClick}
            fitView
            fitViewOptions={{ padding: 0.2 }}
            minZoom={0.1}
            maxZoom={2}
          >
            <Background gap={16} color="#F3F4F6" />
            <Controls />
            <MiniMap
              nodeColor={(n) => {
                const origin = (n.data as unknown as ErNode).origin
                if (origin === 'pbix-data') return '#BFDBFE'
                if (origin === 'manual-upload') return '#0A0A0A'
                if (origin === 'derived-view') return '#D1D5DB'
                return '#E5E7EB'
              }}
              style={{ border: '1px solid #E5E7EB', borderRadius: 8 }}
            />
          </ReactFlow>
        </div>

        {sidePanelSelected && (
          <SidePanel
            selected={sidePanelSelected}
            onClose={() => { setSelectedNode(null); setSelectedEdge(null) }}
          />
        )}
      </div>
      </DataLoader>

      <AddTableDialog
        open={addOpen}
        projectId={currentProject.id}
        onClose={() => setAddOpen(false)}
        onSuccess={fetchDiagram}
      />
    </div>
  )
}
