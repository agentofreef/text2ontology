'use client'

import { useMemo } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { sql, PostgreSQL } from '@codemirror/lang-sql'
import { EditorView } from '@codemirror/view'

interface SQLEditorProps {
  value: string
  onChange: (value: string) => void
  schema?: Record<string, string[]> // tableName → column names for autocomplete
  height?: string
  readOnly?: boolean
}

export function SQLEditor({ value, onChange, schema, height = '200px', readOnly = false }: SQLEditorProps) {
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
    return [sqlExt, EditorView.lineWrapping]
  }, [schema])

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
