'use client'

import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type Edge,
  type NodeTypes,
  type OnNodesChange,
  type OnEdgesChange,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import dagre from '@dagrejs/dagre'

const DEFAULT_W = 220
const DEFAULT_H = 100

export type LayoutOpts = {
  width?: number
  height?: number
  rankdir?: 'LR' | 'TB'
  nodesep?: number
  ranksep?: number
}

// layoutGraph positions nodes with Dagre. Extracted from the ER diagram page so
// both the ontology ER view and the lakehouse data-architecture view share one
// layout implementation. Pass per-call dims for differently sized node types.
export function layoutGraph(nodes: Node[], edges: Edge[], opts: LayoutOpts = {}): Node[] {
  const w = opts.width ?? DEFAULT_W
  const h = opts.height ?? DEFAULT_H
  const g = new dagre.graphlib.Graph()
  g.setDefaultEdgeLabel(() => ({}))
  g.setGraph({
    rankdir: opts.rankdir ?? 'LR',
    nodesep: opts.nodesep ?? 60,
    ranksep: opts.ranksep ?? 100,
  })
  for (const node of nodes) g.setNode(node.id, { width: w, height: h })
  for (const edge of edges) g.setEdge(edge.source, edge.target)
  dagre.layout(g)
  return nodes.map((node) => {
    const pos = g.node(node.id)
    return { ...node, position: { x: pos.x - w / 2, y: pos.y - h / 2 } }
  })
}

export type ErCanvasProps = {
  nodes: Node[]
  edges: Edge[]
  nodeTypes: NodeTypes
  onNodesChange?: OnNodesChange<Node>
  onEdgesChange?: OnEdgesChange<Edge>
  onNodeClick?: (e: React.MouseEvent, node: Node) => void
  onEdgeClick?: (e: React.MouseEvent, edge: Edge) => void
  onPaneClick?: () => void
  miniMapNodeColor?: (n: Node) => string
}

// ErCanvas is the shared ReactFlow render surface (Background + Controls +
// MiniMap, fitView, zoom bounds). Callers own node/edge state and supply the
// nodeTypes appropriate to the level (ER tables vs data-source nodes).
export function ErCanvas({
  nodes,
  edges,
  nodeTypes,
  onNodesChange,
  onEdgesChange,
  onNodeClick,
  onEdgeClick,
  onPaneClick,
  miniMapNodeColor,
}: ErCanvasProps) {
  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      onNodesChange={onNodesChange}
      onEdgesChange={onEdgesChange}
      onNodeClick={onNodeClick}
      onEdgeClick={onEdgeClick}
      onPaneClick={onPaneClick}
      fitView
      fitViewOptions={{ padding: 0.2 }}
      minZoom={0.1}
      maxZoom={2}
      // Only mount nodes/edges intersecting the viewport. On large ER graphs
      // (hundreds of tables/columns) this keeps the DOM bounded and avoids the
      // render blowup that froze the canvas when every node mounted at once.
      onlyRenderVisibleElements
    >
      <Background gap={16} color="#F3F4F6" />
      <Controls />
      <MiniMap
        nodeColor={miniMapNodeColor ?? (() => '#E5E7EB')}
        style={{ border: '1px solid #E5E7EB', borderRadius: 8 }}
      />
    </ReactFlow>
  )
}
