'use client'

import { useState, useEffect, useCallback } from 'react'
import {
  type Node,
  type Edge,
  type NodeTypes,
  useNodesState,
  useEdgesState,
  MarkerType,
} from '@xyflow/react'
import { ErCanvas, layoutGraph } from '@/components/er-canvas/ErCanvas'
import { useProject } from '@/lib/project'
import { api } from '@/lib/api'
import { useStyleMode } from '@/lib/style-mode'
import { Button } from '@/components/ui/Button'
import { TableNode } from '../../er-diagram/components/TableNode'
import type { OntObjectType, OntLinkType, ErNode, ErEdge } from '@/types/api'
import { ChevronLeft } from 'lucide-react'

const nodeTypes: NodeTypes = {
  erTable: TableNode as unknown as NodeTypes['erTable'],
}

export interface SourceErViewGroup {
  label: string
  memberIds: string[]
}

// SourceErView drills into one data-source group and renders its objects as an
// ER diagram. Reuses the exact ErNode/ErEdge mapping from er-diagram (non-pbit
// branch): objects → erTable nodes, links → edges, but filtered to the group's
// members (edges kept only when BOTH endpoints are members).
export function SourceErView({ group, onBack }: { group: SourceErViewGroup; onBack: () => void }) {
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const fetchDiagram = useCallback(async () => {
    if (!currentProject) return
    setLoading(true)
    setError('')
    try {
      const memberSet = new Set(group.memberIds)

      const [objectsRes, linksRes] = await Promise.all([
        api<{ data: OntObjectType[] }>(`/ontology/objects?projectId=${currentProject.id}`),
        api<{ data: OntLinkType[] }>(`/ontology/links?projectId=${currentProject.id}`),
      ])

      const objects = (objectsRes.data || []).filter((o) => memberSet.has(o.id))
      const links = linksRes.data || []

      const erNodes: ErNode[] = objects.map((o) => ({
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

      const linkIdToFromTo = new Map<string, { from: string; to: string }>()
      const erEdges: ErEdge[] = links
        .filter((l) => memberSet.has(l.fromObjectId) && memberSet.has(l.toObjectId))
        .map((l) => {
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

      const flowNodes: Node[] = erNodes.map((n) => ({
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
  }, [currentProject, group.memberIds, setNodes, setEdges])

  useEffect(() => { fetchDiagram() }, [fetchDiagram])

  return (
    <div className="flex h-full flex-col">
      {/* Header — breadcrumb + back */}
      <header
        className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${
          industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'
        }`}
      >
        <div className="flex min-w-0 items-center gap-2">
          <button
            onClick={onBack}
            className={`flex items-center gap-1 transition-colors duration-150 hover:text-ink ${
              industrial ? 'font-mono text-[10px] uppercase tracking-[0.14em] text-ink-ghost' : 'text-xs text-ink-ghost'
            }`}
          >
            <ChevronLeft className="h-3.5 w-3.5" />
            {industrial ? 'DATA ARCHITECTURE' : '数据架构'}
          </button>
          <span className="text-ink-ghost">/</span>
          <span className={`truncate font-medium text-ink ${industrial ? 'font-mono text-[12px] tracking-[0.04em]' : 'text-sm'}`}>
            {group.label}
          </span>
          {!loading && (
            <span className={industrial ? 'font-mono text-[10px] tracking-[0.14em] text-ink-muted tabular-nums' : 'text-xs text-ink-ghost tabular-nums'}>
              · {nodes.length} {nodes.length === 1 ? 'table' : 'tables'}
            </span>
          )}
        </div>
        <Button variant="ghost" size="sm" onClick={onBack}>
          {industrial ? 'BACK' : '返回'}
        </Button>
      </header>

      {error && (
        <div
          className={`px-6 py-2 text-xs text-red-600 ${
            industrial ? 'border-b-2 border-danger bg-danger/5 font-mono tracking-[0.06em]' : 'border-b border-red-100 bg-red-50'
          }`}
        >
          {industrial ? `// ERROR · ${error}` : error}
        </div>
      )}

      <div className="relative flex flex-1 min-h-0">
        <ErCanvas
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          miniMapNodeColor={(n) => {
            const origin = (n.data as unknown as ErNode).origin
            if (origin === 'pbix-data') return '#BFDBFE'
            if (origin === 'manual-upload') return '#0A0A0A'
            if (origin === 'derived-view') return '#D1D5DB'
            return '#E5E7EB'
          }}
        />
        {loading && (
          <div className="absolute inset-0 z-10 flex items-center justify-center bg-white/70 text-sm text-ink-ghost">
            Loading…
          </div>
        )}
      </div>
    </div>
  )
}
