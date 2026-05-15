'use client'

import { useState, Fragment, ReactNode, useEffect, useMemo } from 'react'
import { ChevronDown, ChevronRight, Search, ArrowUp, ArrowDown } from 'lucide-react'

export interface Column<T = Record<string, unknown>> {
  key: string
  title: string
  width?: string
  sortable?: boolean
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  render?: (value: any, record: T, index: number) => ReactNode
}

type MarkFilter = 'all' | 'marked'
type SortDir = 'asc' | 'desc'

interface DataTableProps<T = Record<string, unknown>> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  columns: Column<any>[]
  data: T[]
  rowKey: string
  searchable?: boolean
  searchPlaceholder?: string
  expandable?: (record: T) => ReactNode
  extra?: ReactNode
  markable?: boolean
  onMarkToggle?: (id: string, newValue: boolean) => void
  batchActions?: (selectedIds: string[], clearSelection: () => void) => ReactNode
  onRowClick?: (record: T) => void
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function DataTable<T extends Record<string, any>>({
  columns,
  data,
  rowKey,
  searchable,
  searchPlaceholder = '>_ SEARCH...',
  expandable,
  extra,
  markable,
  onMarkToggle,
  batchActions,
  onRowClick,
}: DataTableProps<T>) {
  const [search, setSearch] = useState('')
  const [expandedRows, setExpandedRows] = useState<Set<string>>(new Set())
  const [markFilter, setMarkFilter] = useState<MarkFilter>('all')
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const [sortKey, setSortKey] = useState<string | null>(null)
  const [sortDir, setSortDir] = useState<SortDir>('asc')

  const getKey = (record: T) => String(record[rowKey])

  // Clear selection when data changes
  useEffect(() => {
    setSelectedIds(new Set())
  }, [data])

  const handleSort = (key: string) => {
    if (sortKey === key) {
      if (sortDir === 'asc') {
        setSortDir('desc')
      } else {
        // Third click: clear sort
        setSortKey(null)
        setSortDir('asc')
      }
    } else {
      setSortKey(key)
      setSortDir('asc')
    }
  }

  let filtered = data
  if (searchable && search) {
    filtered = filtered.filter((row) =>
      Object.values(row).some((val) =>
        String(val).toLowerCase().includes(search.toLowerCase())
      )
    )
  }
  if (markable && markFilter === 'marked') {
    filtered = filtered.filter((row) => row.mark === true)
  }

  // Sort
  const sorted = useMemo(() => {
    if (!sortKey) return filtered
    return [...filtered].sort((a, b) => {
      const av = a[sortKey]
      const bv = b[sortKey]
      // Handle nulls
      if (av == null && bv == null) return 0
      if (av == null) return sortDir === 'asc' ? -1 : 1
      if (bv == null) return sortDir === 'asc' ? 1 : -1
      // Numeric comparison
      if (typeof av === 'number' && typeof bv === 'number') {
        return sortDir === 'asc' ? av - bv : bv - av
      }
      // String comparison
      const sa = String(av).toLowerCase()
      const sb = String(bv).toLowerCase()
      const cmp = sa.localeCompare(sb)
      return sortDir === 'asc' ? cmp : -cmp
    })
  }, [filtered, sortKey, sortDir])

  const markedCount = data.filter((row) => row.mark === true).length
  const filteredKeys = sorted.map(getKey)
  const allSelected = filteredKeys.length > 0 && filteredKeys.every((k) => selectedIds.has(k))
  const someSelected = selectedIds.size > 0

  const toggleSelectAll = () => {
    if (allSelected) {
      setSelectedIds(new Set())
    } else {
      setSelectedIds(new Set(filteredKeys))
    }
  }

  const toggleSelect = (key: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      next.has(key) ? next.delete(key) : next.add(key)
      return next
    })
  }

  const clearSelection = () => setSelectedIds(new Set())

  const toggleExpand = (key: string) => {
    setExpandedRows((prev) => {
      const next = new Set(prev)
      next.has(key) ? next.delete(key) : next.add(key)
      return next
    })
  }

  const showCheckboxes = !!batchActions

  const allColumns = markable
    ? [
        {
          key: '__mark',
          title: 'MARK',
          width: '56px',
          render: (_: unknown, record: T) => (
            <button
              onClick={(e) => {
                e.stopPropagation()
                onMarkToggle?.(getKey(record), !record.mark)
              }}
              className="flex items-center justify-center"
            >
              <span
                className={`inline-block h-4 w-4 border ${
                  record.mark
                    ? 'border-accent bg-accent'
                    : 'border-border bg-white hover:border-ink-muted'
                }`}
              />
            </button>
          ),
        } as Column<T>,
        ...columns,
      ]
    : columns

  return (
    <div>
      {(searchable || extra || markable || showCheckboxes) && (
        <div className="mb-3 flex items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            {searchable && (
              <div className="flex items-center gap-2 border border-border bg-canvas-alt px-3 py-2">
                <Search className="h-3.5 w-3.5 text-ink-ghost" />
                <input
                  type="text"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder={searchPlaceholder.replace(/^>_\s*/, '')}
                  className="bg-transparent text-xs text-ink outline-none placeholder:text-ink-ghost font-sans"
                />
              </div>
            )}
            {markable && (
              <div className="flex items-center gap-0.5 font-mono text-[10px]">
                <button
                  onClick={() => setMarkFilter('all')}
                  className={`border px-2 py-1 ${
                    markFilter === 'all'
                      ? 'border-ink bg-ink text-white'
                      : 'border-border text-ink-muted hover:border-ink-muted'
                  }`}
                >
                  ALL
                </button>
                <button
                  onClick={() => setMarkFilter('marked')}
                  className={`border px-2 py-1 ${
                    markFilter === 'marked'
                      ? 'border-accent bg-accent text-white'
                      : 'border-border text-ink-muted hover:border-accent'
                  }`}
                >
                  MARKED
                </button>
              </div>
            )}
            {showCheckboxes && (
              <div className={`flex items-center gap-2 ${someSelected ? '' : 'opacity-40 pointer-events-none'}`}>
                <span className="font-mono text-[10px] font-semibold text-ink-muted">
                  {someSelected ? `${selectedIds.size} SELECTED` : '0 SELECTED'}
                </span>
                <div className="h-3 w-px bg-border" />
                {batchActions(Array.from(selectedIds), clearSelection)}
              </div>
            )}
          </div>
          {extra && <div className="flex items-center gap-2">{extra}</div>}
        </div>
      )}

      <div className="overflow-x-auto border border-border">
        <table className="w-full">
          <thead>
            <tr className="border-b-2 border-ink bg-canvas-alt">
              {showCheckboxes && (
                <th className="w-10 px-3 py-2">
                  <button onClick={toggleSelectAll} className="flex items-center justify-center">
                    <span
                      className={`inline-block h-3.5 w-3.5 border ${
                        allSelected
                          ? 'border-ink bg-ink'
                          : someSelected
                            ? 'border-ink bg-ink/30'
                            : 'border-border bg-white hover:border-ink-muted'
                      }`}
                    />
                  </button>
                </th>
              )}
              {expandable && <th className="w-8 px-3 py-2" />}
              {allColumns.map((col) => {
                const isSorted = sortKey === col.key
                const canSort = col.sortable === true
                return (
                  <th
                    key={col.key}
                    className={`px-3 py-2 text-left text-[10px] font-bold uppercase tracking-wider text-ink-muted font-sans ${canSort ? 'cursor-pointer select-none hover:text-ink' : ''}`}
                    style={col.width ? { width: col.width } : undefined}
                    onClick={canSort ? () => handleSort(col.key) : undefined}
                  >
                    <span className="inline-flex items-center gap-1">
                      {col.title}
                      {canSort && isSorted && sortDir === 'asc' && <ArrowUp className="h-3 w-3 text-accent" />}
                      {canSort && isSorted && sortDir === 'desc' && <ArrowDown className="h-3 w-3 text-accent" />}
                      {canSort && !isSorted && <ArrowUp className="h-3 w-3 text-ink-ghost/30" />}
                    </span>
                  </th>
                )
              })}
            </tr>
          </thead>
          <tbody>
            {sorted.length === 0 ? (
              <tr>
                <td colSpan={allColumns.length + (expandable ? 1 : 0) + (showCheckboxes ? 1 : 0)} className="px-3 py-8 text-center">
                  <div className="font-mono text-xs text-ink-ghost">{'>_ NO_DATA'}</div>
                </td>
              </tr>
            ) : (
              sorted.map((record, i) => {
                const key = getKey(record)
                const isExpanded = expandedRows.has(key)
                const isSelected = selectedIds.has(key)
                const totalCols = allColumns.length + (expandable ? 1 : 0) + (showCheckboxes ? 1 : 0)
                return (
                  <Fragment key={key}>
                    <tr
                      className={`border-b border-border-light transition-colors duration-100 hover:bg-canvas-alt ${i % 2 === 0 ? '' : 'bg-canvas/50'} ${isSelected ? '!bg-accent/5' : ''} ${onRowClick ? 'cursor-pointer' : ''}`}
                      onClick={onRowClick ? () => onRowClick(record) : undefined}
                    >
                      {showCheckboxes && (
                        <td className="w-10 px-3 py-2">
                          <button
                            onClick={(e) => { e.stopPropagation(); toggleSelect(key) }}
                            className="flex items-center justify-center"
                          >
                            <span
                              className={`inline-block h-3.5 w-3.5 border ${
                                isSelected
                                  ? 'border-accent bg-accent'
                                  : 'border-border bg-white hover:border-ink-muted'
                              }`}
                            />
                          </button>
                        </td>
                      )}
                      {expandable && (
                        <td className="w-8 px-3 py-2">
                          <button
                            onClick={(e) => { e.stopPropagation(); toggleExpand(key) }}
                            className="text-ink-ghost hover:text-ink"
                          >
                            {isExpanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
                          </button>
                        </td>
                      )}
                      {allColumns.map((col) => (
                        <td key={col.key} className="px-3 py-2 text-sm">
                          {col.render
                            ? col.render(record[col.key], record, i)
                            : String(record[col.key] ?? '')}
                        </td>
                      ))}
                    </tr>
                    {expandable && isExpanded && (
                      <tr>
                        <td colSpan={totalCols} className="border-b border-border bg-canvas-alt p-4">
                          {expandable(record)}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                )
              })
            )}
          </tbody>
        </table>
      </div>
      <div className="mt-2 flex items-center justify-between font-mono text-[10px] text-ink-ghost">
        <span>RECORDS: {sorted.length}/{data.length}</span>
        {markable && <span>MARKED: {markedCount}/{data.length}</span>}
      </div>
    </div>
  )
}
