'use client'

import { useMemo } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { sql, PostgreSQL } from '@codemirror/lang-sql'
import {
  EditorView, Decoration, ViewPlugin, WidgetType,
  type DecorationSet, type ViewUpdate,
} from '@codemirror/view'
import { RangeSetBuilder } from '@codemirror/state'

interface SQLEditorProps {
  value: string
  onChange: (value: string) => void
  schema?: Record<string, string[]> // tableName → column names for autocomplete
  height?: string
  readOnly?: boolean
  // When true, inline parameter tokens `{sys.req.NAME}` / `{sys.opt.NAME}` are
  // syntax-highlighted: required → RED+bold, optional → AMBER+bold. Default off
  // so other SQLEditor call sites (object detail, sql-passthrough) are untouched.
  highlightSysParams?: boolean
}

// ── {sys.req/opt.NAME} highlighter ──────────────────────────────────────────
// A ViewPlugin that scans the visible document for inline metric-parameter
// tokens and applies a mark decoration with an inline colour:
//   {sys.req.X} → #DC2626 (text-danger)  · {sys.opt.X} → #F59E0B (text-warning)
// Both bold. Decorations are recomputed on doc/viewport change.
const SYS_PARAM_RE = /\{sys\.(req|opt)\.([A-Za-z_][A-Za-z0-9_]*)\}/g

// SysParamWidget renders a `{sys.req/opt.NAME}` token as a colored PILL showing
// ONLY NAME — the `{sys.req.}` / `{sys.opt.}` syntax + braces are hidden. Red =
// required, amber = optional. It's a REPLACE decoration: the raw token text is
// hidden and this widget shown in its place. Backspacing into the token breaks the
// `{sys.…}` pattern, which reveals the raw text again so it can be edited.
class SysParamWidget extends WidgetType {
  constructor(readonly name: string, readonly required: boolean) { super() }
  eq(other: SysParamWidget) { return other.name === this.name && other.required === this.required }
  toDOM() {
    const span = document.createElement('span')
    span.className = this.required ? 'cm-sysparam-pill cm-sysparam-req' : 'cm-sysparam-pill cm-sysparam-opt'
    span.textContent = this.name
    span.title = this.required ? `必填参数 ${this.name}` : `选填参数 ${this.name}`
    return span
  }
  ignoreEvent() { return false }
}

function buildSysParamDecorations(view: EditorView): DecorationSet {
  const builder = new RangeSetBuilder<Decoration>()
  for (const { from, to } of view.visibleRanges) {
    const text = view.state.doc.sliceString(from, to)
    SYS_PARAM_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = SYS_PARAM_RE.exec(text)) !== null) {
      const start = from + m.index
      const end = start + m[0].length
      builder.add(start, end, Decoration.replace({ widget: new SysParamWidget(m[2], m[1] === 'req') }))
    }
  }
  return builder.finish()
}

const sysParamHighlighter = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet
    constructor(view: EditorView) {
      this.decorations = buildSysParamDecorations(view)
    }
    update(update: ViewUpdate) {
      if (update.docChanged || update.viewportChanged) {
        this.decorations = buildSysParamDecorations(update.view)
      }
    }
  },
  { decorations: v => v.decorations },
)

export function SQLEditor({ value, onChange, schema, height = '200px', readOnly = false, highlightSysParams = false }: SQLEditorProps) {
  const accent = '#0A0A0A'

  const editorTheme = useMemo(() => EditorView.theme({
    '&': {
      fontSize: '12px',
      fontFamily: 'JetBrains Mono, monospace',
      border: '1px solid #E5E5E5',
      backgroundColor: '#FFFFFF',
    },
    '.cm-content': {
      fontFamily: 'JetBrains Mono, monospace',
      caretColor: accent,
      padding: '4px 0',
    },
    '.cm-cursor': {
      borderLeftColor: accent,
      borderLeftWidth: '2px',
    },
    '&.cm-focused': {
      outline: 'none',
      borderColor: accent,
    },
    '.cm-gutters': {
      backgroundColor: '#F5F5F5',
      borderRight: '1px solid #E5E5E5',
      color: '#999',
      fontFamily: 'JetBrains Mono, monospace',
      fontSize: '10px',
    },
    '.cm-activeLineGutter': {
      backgroundColor: '#EBEBEB',
    },
    '.cm-activeLine': {
      backgroundColor: 'rgba(0,0,0,0.04)',
    },
    '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, .cm-content ::selection': {
      backgroundColor: '#E5E5E5 !important',
    },
    '.cm-tooltip': {
      border: '1px solid #E5E5E5',
      borderRadius: '6px',
      backgroundColor: '#FAFAFA',
    },
    '.cm-tooltip-autocomplete': {
      fontFamily: 'JetBrains Mono, monospace',
      fontSize: '11px',
      border: '1px solid #E5E5E5',
      borderRadius: '6px',
    },
    '.cm-completionIcon': {
      display: 'none',
    },
  }), [])

  const extensions = useMemo(() => {
    const sqlExt = sql({
      dialect: PostgreSQL,
      upperCaseKeywords: true,
      schema: schema || {},
    })
    const exts = [sqlExt, EditorView.lineWrapping]
    if (highlightSysParams) exts.push(sysParamHighlighter)
    return exts
  }, [schema, highlightSysParams])

  return (
    <CodeMirror
      value={value}
      onChange={onChange}
      extensions={extensions}
      theme={editorTheme}
      height={height}
      readOnly={readOnly}
      basicSetup={{
        lineNumbers: true,
        foldGutter: false,
        highlightActiveLine: true,
        autocompletion: true,
        bracketMatching: true,
        closeBrackets: true,
        indentOnInput: true,
      }}
    />
  )
}
