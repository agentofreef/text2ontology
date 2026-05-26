// ==================== Common ====================

export interface ListResponse<T> {
  data: T[]
  total: number
}

// ==================== User & Project ====================

export interface User {
  id: string
  username: string
  displayName: string
  role: 'admin' | 'user'
  isActive: boolean
  createdAt: string
  updatedAt: string
}

export interface Project {
  id: string
  name: string
  description: string
  ownerId: string
  sourceType: string
  sourceFile: string
  compatibility: number
  status: 'active' | 'archived'
  lakehouseSchema?: string
  pbitConfig?: unknown
  createdAt: string
  updatedAt: string
}

// ==================== Model Layer ====================


// ==================== Runtime Layer ====================

export interface PromptConfig {
  id: string
  projectId: string
  configKey: string
  configValue: string
  version: number
  isActive: boolean
  mark: boolean
  note: string
  createdBy: string
  createdAt: string
  updatedAt: string
}

// ==================== LLM Config ====================

export interface LLMConfig {
  id: string
  configType: 'chat' | 'embedding'
  vendor: string
  baseUrl: string
  apiKey: string
  modelName: string
  alias?: string
  isThinking: boolean
  isToolCall: boolean
  vectorDim: number | null
  isActive: boolean
  proxyUrl: string
  note: string
  createdAt: string
  updatedAt: string
}

export interface LLMRoleBinding {
  roleName: string
  configId: string
  modelName?: string
  vendor?: string
  baseUrl?: string
  isThinking?: boolean
  isToolCall?: boolean
  vectorDim?: number | null
  updatedAt: string
}

// ==================== Ontology Layer ====================

export interface OntObjectType {
  id: string
  projectId: string
  name: string
  displayName: string
  kind: 'entity' | 'event' | 'attribute'
  description: string
  sourceTable: string
  sourceType: string
  // dataSourceId links an object to its originating connector data source.
  // Backend is rolling this out — treat missing/empty as null (folder grouping).
  dataSourceId?: string | null
  bridgedFrom: string
  semanticSql: string
  canonicalQuery: string
  validatedAt: string
  properties: OntProperty[]
  mark: boolean
  note: string
  createdAt: string
  updatedAt: string
}

export interface OntProperty {
  id: string
  objectTypeId: string
  name: string
  displayName: string
  dataType: string
  sourceColumn: string
  isFilterable: boolean
  isGroupable: boolean
  description: string
  shortDescription: string
  bridgedFrom: string
  isMachineCode: boolean
  keywordsSyncedAt: string
  sampleValues: string
  mark: boolean
  note: string
  createdAt: string
  updatedAt: string
}

export interface OntLinkType {
  id: string
  projectId: string
  fromObjectId: string
  toObjectId: string
  fromObjectName: string
  toObjectName: string
  linkName: string
  fkColumn: string
  cardinality: string
  rejectReason: string
  description: string
  bridgedFrom: string
  mark: boolean
  note: string
  createdAt: string
  updatedAt: string
}

// Metric (口径) — lakehouse "query metric shortcut" (Order.Total, Order.Real, ...).
// Maps natural-language trigger terms (via lakehouse_keyword) to a canonical
// smartquery template so the LLM doesn't have to re-derive filter/groupBy.
export interface OntMetricIntentFilter {
  prop: string
  op: string
  value: string
}
export interface OntMetricIntent {
  id: string
  projectId: string
  objectId: string
  objectName: string
  name: string
  displayName: string
  canonicalMetric: string
  canonicalFilters: OntMetricIntentFilter[]
  autoGroupBy: string[]
  // Pivot config: if pivotOn is set, the smartquery executor post-processes
  // long-format results into wide-format on this column. pivotValues fixes
  // column order (empty = data-derived), pivotTotalLabel names the sum column.
  pivotOn: string
  pivotValues: string[]
  pivotColumnLabels: string[]
  pivotTotalLabel: string
  pivotPercentAxis: string // "row" | "column"
  pivotPercentScope: string // "filtered" | "global"
  pivotPercentSuffix: string // column suffix for percent columns, default "占比"
  pivotWithPercent: boolean
  pivotAppendGrandTotal: boolean
  responseTemplate: string
  description: string
  priority: number
  mark: boolean
  createdAt: string
  updatedAt: string
}

// ─── Unified Metric (口径 — lakehouse_metric) ─────────────────
// The new first-class "口径" concept (table lakehouse_metric). Coexists with
// OntMetricIntent (lakehouse_metric_intent). Adds typed `parameters` (a metric
// declares typed params; required ones the agent asks the user about) plus an
// optional `level` (simple|plan) and advanced raw `plan` object.
export interface OntMetricParameter {
  name: string
  type: 'int' | 'string' | 'property_filter' | 'enum_ref'
  property?: string
  op?: string
  optional?: boolean
  default?: unknown
  description?: string
}
export interface OntMetric {
  id: string
  projectId: string
  objectId: string
  objectName: string
  // odIds: the full SELECTED OD set for a multi-OD metric (a metric can span
  // several ODs via JOINs). object_id is the primary (= odIds[0]); odIds is
  // persisted in extra and returned by GET-by-id. Edit hydrates form.odIds from
  // this (fallback: [objectId] for legacy rows that predate odIds).
  odIds?: string[]
  name: string
  displayName: string
  description: string
  // 'simple' = structured (canonicalMetric + filters/groupBy/pivot); 'sql' =
  // hand-written passthrough SQL stored in querySql. 'plan' is the legacy
  // multi-step variant (kept for back-compat with older rows).
  level: 'simple' | 'plan' | 'sql'
  canonicalMetric: string
  // Hand-written SQL for level='sql'. The backend stores a sentinel in
  // canonicalMetric for SQL-mode metrics; this is the real query.
  querySql?: string
  canonicalFilters: OntMetricIntentFilter[]
  autoGroupBy: string[]
  replaceGroupBy: boolean
  defaultOrderByLabel: string
  defaultOrderByDir: 'ASC' | 'DESC' | ''
  defaultLimit: number | null
  pivotOn: string
  pivotValues: string[]
  pivotColumnLabels: string[]
  pivotTotalLabel: string
  pivotWithPercent: boolean
  pivotAppendGrandTotal: boolean
  pivotPercentAxis: 'row' | 'column'
  pivotPercentScope: 'filtered' | 'global'
  pivotPercentSuffix: string
  parameters: OntMetricParameter[]
  plan?: Record<string, unknown> | null
  responseTemplate: string
  priority: number
  mark: boolean
  triggerKeywords: string[]
  createdAt: string
  updatedAt: string
}

// ─── Knowledge Ontology (Ok) ─────────────────────────────────

export interface OntKnowledge {
  id: string
  projectId: string
  topicId?: string
  topicName?: string
  parentId: string
  title: string
  summary: string
  content: string
  entryType: 'concept' | 'playbook'
  anchorType: 'version' | 'object' | 'metric' | 'link' | 'property'
  anchorId: string
  anchorName: string
  skillConfig: Record<string, unknown>
  sortOrder: number
  mark: boolean
  note: string
  linkedPropertyId?: string
  linkedPropertyName?: string
  definitionCount?: number
  exampleCount?: number
  createdAt: string
  updatedAt: string
}

export interface OntCausality {
  id: string
  projectId: string
  fromKnowledgeId: string
  fromKnowledgeTitle: string
  toKnowledgeId: string
  toKnowledgeTitle: string
  relationType: 'causes' | 'correlates' | 'composes' | 'join_key'
  direction: 'positive' | 'negative' | 'neutral'
  description: string
  sortOrder: number
  mark: boolean
  note: string
  createdAt: string
  updatedAt: string
}

// ─── Learned Facts Ontology (Ol) ────────────────────────────

export interface OntLearnedFact {
  id: string
  projectId: string
  title: string         // short abstract name (≤10 chars)
  summary: string
  content: string
  keywords: string      // legacy pipe-separated: "接单|订单" (kept for backward compat)
  tags: string[]        // N searchable tags: business concepts, Od names, knowledge types, dimensions
  factType: 'business_rule' | 'calibration' | 'misconception' | 'filter_hint' | 'calculation_note'
  confidence: 'pending' | 'confirmed' | 'rejected'
  sourceThreadId: string
  sourceType: 'workbench' | 'agent' | 'manual'
  sortOrder: number
  mark: boolean
  note: string
  links?: OntFactLink[]
  definitionCount?: number
  linkCount?: number
  createdAt: string
  updatedAt: string
}

export interface OntFactLink {
  id: string
  factId: string
  targetType: 'object' | 'metric' | 'property' | 'link' | 'knowledge' | 'fact'
  targetId: string
  targetName?: string
  role: 'about' | 'supports' | 'contradicts' | 'derived_from' | 'related'
  note: string
  createdAt: string
}

// ==================== PBIT Lakehouse (new parallel path) ====================

export interface PbitTablePreview {
  name: string
  sourceType: 'sql' | 'excel' | 'derived' | 'constant' | 'unsupported' | 'pbix' | 'calculated'
  columnCount: number
  columns: Array<{ name: string; dataType: string }>
  partitionKind: 'combine' | 'constant_csv' | 'unpivot' | 'unsupported' | 'external'
  requiredFiles?: string[]  // for Table.Combine, list of source tables
  rawM?: string
}

export interface PbitRelationshipPreview {
  fromTable: string
  fromColumn: string
  toTable: string
  toColumn: string
  cardinality: 'M:1' | '1:M' | '1:1' | 'M:M'
  isActive: boolean
  crossFilteringBehavior: 'single' | 'both'
}

export interface PbitMeasurePreview {
  name: string
  table: string
  description?: string
}

export interface PbitPreview {
  importId: string
  sourceFilename: string
  tables: PbitTablePreview[]
  relationships: PbitRelationshipPreview[]
  measures: PbitMeasurePreview[]
  derivedCount: number
  parsedAt: string
}

export type XlsxBindingState = 'unmatched' | 'suggested' | 'confirmed' | 'skipped' | 'unrelated'

export interface XlsxBindingCandidate {
  tableName: string
  score: number
}

export interface XlsxBinding {
  fileName: string
  tableName: string           // the currently bound table (may be empty if unmatched)
  score: number
  state: XlsxBindingState
  headers: string[]
  allCandidates: XlsxBindingCandidate[]  // populated when conflicting matches exist
}

// SSE progress events (discriminated union on phase)
export type ImportProgressEvent =
  | { phase: 'precheck_ok' }
  | { phase: 'schema_created'; schema: string }
  | { phase: 'table_loading'; tableName: string }
  | { phase: 'table_loaded'; tableName: string; rowCount: number }
  | { phase: 'table_failed'; tableName: string; error: string }
  | { phase: 'views_built'; viewCount: number; warnings: string[] }
  | { phase: 'committing' }
  | { phase: 'committed' }
  | { phase: 'done'; projectId: string; status: 'success' | 'partial'; importLogId: string }
  | { phase: 'error'; error: string }

// ER diagram
export interface ErNode {
  id: string
  label: string
  rowCount: number | null   // nullable — render as em-dash in UI
  columnCount: number
  origin: 'pbit-bootstrap' | 'pbix-data' | 'manual-upload' | 'derived-view' | ''
  warning?: string          // for unsupported M expression tables
  columns: Array<{ name: string; dataType: string }>
}

export interface ErEdge {
  id: string
  fromTable: string
  toTable: string
  fromColumn: string
  toColumn: string
  cardinality: 'M:1' | '1:M' | '1:1' | 'M:M'
  isActive: boolean
}

// ==================== Per-table PBIT import progress ====================

export interface LakehouseTableStatus {
  tableName: string
  sourceType: string
  partitionKind: string
  status: 'pending' | 'header_matched' | 'loaded' | 'skipped' | 'error'
  fileName?: string
  rowCount?: number
  columnCount?: number
  errorMessage?: string
}

export interface ImportProgressResponse {
  pbitConfig: PbitPreview | null
  tables: LakehouseTableStatus[]
}

export interface HeaderMatchResult {
  matched: boolean
  tableName: string
  fileName: string
  mapping: Record<string, string>  // fileCol → pbitCol
  missingCols: string[]
  extraCols: string[]
  preview: string[][]  // first 5 rows
}

