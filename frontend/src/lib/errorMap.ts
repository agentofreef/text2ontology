// Pattern-match backend error strings (Chinese) and surface zh+en translations.
// Per consensus plan v3.2 R11: a frontend pattern map is the accepted mitigation
// for 3rd-party errors and any backend error site we have not yet given a `code`
// field. Cheap and works without touching the Go services.
//
// Strategy: substring matching only (no regex, per project memory).
// Returns { key, vars } so caller can pass to next-intl's t(key, vars).

export interface ErrorTranslation {
  /** dotted key path under `errors.*` namespace */
  key: string
  /** ICU placeholder values to inject */
  vars?: Record<string, string | number>
}

interface Pattern {
  match: (raw: string) => boolean
  key: string
  extract?: (raw: string) => Record<string, string | number>
}

// Order matters: more specific patterns first.
const PATTERNS: Pattern[] = [
  // Auth
  { match: (s) => s === '未认证', key: 'errors.auth.unauthenticated' },
  { match: (s) => s === '登录失败', key: 'errors.auth.login_failed' },
  { match: (s) => s === '登录已过期，请重新登录', key: 'errors.auth.token_expired' },
  { match: (s) => s.includes('网络错误') && s.includes('服务器连接'), key: 'errors.auth.network' },

  // Project / Ontology config
  { match: (s) => s.includes('数据湖仓 schema'), key: 'errors.project.no_lakehouse_schema' },
  { match: (s) => s.includes('未配置任何 Ontology 对象') || s.includes('未完成 canonical_query'), key: 'errors.project.no_ontology_objects' },

  // Validation
  { match: (s) => s === 'label 必填', key: 'errors.validation.label_required' },
  { match: (s) => s.includes('label 不能超过 64 字符'), key: 'errors.validation.label_too_long' },
  { match: (s) => s === 'allowedTools 必须是字符串数组', key: 'errors.validation.allowed_tools_must_be_array' },

  // MCP keys
  { match: (s) => s === 'key 不存在或已撤销', key: 'errors.mcp_key.not_found_or_revoked' },

  // Object / property
  { match: (s) => s.includes('属性知识节点不能在此处删除'), key: 'errors.object.property_knowledge_not_deletable' },

  // Intent
  {
    match: (s) => s.includes('Intent 校验失败'),
    key: 'errors.intent.validation_failed',
    extract: (s) => {
      const open = s.indexOf('(')
      const close = s.lastIndexOf(')')
      const reason = open > -1 && close > open ? s.slice(open + 1, close) : ''
      return { reason }
    },
  },

  // Tool dispatch
  {
    match: (s) => s.startsWith('未知 tool:'),
    key: 'errors.tool.unknown',
    extract: (s) => ({ tool: s.slice('未知 tool: '.length) }),
  },

  // SQL passthrough
  { match: (s) => s.includes('查询中未引用任何 Ontology 对象'), key: 'errors.sql.no_ontology_object' },
  {
    match: (s) => s.startsWith('未知的 Ontology 对象:'),
    key: 'errors.sql.unknown_ontology_object',
    extract: (s) => {
      // Format: "未知的 Ontology 对象: X. 可用对象: Y"
      const after = s.slice('未知的 Ontology 对象: '.length)
      const dot = after.indexOf('.')
      return dot > -1 ? { name: after.slice(0, dot) } : { name: after }
    },
  },
  { match: (s) => s.includes('只读查询') && s.includes('禁止的语句'), key: 'errors.sql.read_only_forbidden' },
  { match: (s) => s.includes('只允许 SELECT / WITH 查询'), key: 'errors.sql.only_select_with_allowed' },

  // Common verbs (used by many handlers as terse fallback)
  { match: (s) => s === '加载失败', key: 'errors.common.load_failed' },
  { match: (s) => s === '保存失败', key: 'errors.common.save_failed' },
  { match: (s) => s === '删除失败', key: 'errors.common.delete_failed' },
  { match: (s) => s === '创建失败', key: 'errors.common.create_failed' },
  { match: (s) => s === '更新失败', key: 'errors.common.update_failed' },
  { match: (s) => s === '操作失败', key: 'errors.common.op_failed' },

  // 3rd-party Postgres patterns (English errors from lib/pq) — keep English as keys
  { match: (s) => s.includes('duplicate key value violates'), key: 'errors.db.unique_violation' },
  { match: (s) => s.includes('foreign key constraint'), key: 'errors.db.fk_violation' },
  { match: (s) => s.includes('relation') && s.includes('does not exist'), key: 'errors.db.relation_not_exist' },
  { match: (s) => s.includes('connection refused'), key: 'errors.db.connection_refused' },
]

/**
 * Try to translate a raw backend error string into a `{ key, vars }` pair.
 * Returns null when no pattern matched — caller should fall back to raw string.
 */
export function mapErrorString(raw: string): ErrorTranslation | null {
  if (!raw) return null
  for (const p of PATTERNS) {
    if (p.match(raw)) {
      return { key: p.key, vars: p.extract?.(raw) }
    }
  }
  return null
}
