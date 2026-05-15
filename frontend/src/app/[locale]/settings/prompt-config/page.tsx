'use client'

import { useTranslations } from 'next-intl'
import { useState } from 'react'
import { DataTable, Column } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Card } from '@/components/ui/Card'
import { Modal } from '@/components/ui/Modal'
import { Input, Textarea } from '@/components/ui/Input'
import { MotionFade, DataLoader } from '@/lib/motion'
import { useFetch, useCreate } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { useStyleMode } from '@/lib/style-mode'
import { api } from '@/lib/api'
import type { PromptConfig } from '@/types/api'
import { Plus, Power, Loader2 } from 'lucide-react'

const columns: Column<PromptConfig>[] = [
  {
    key: 'configKey',
    title: 'Config Key',
    width: '180px',
    render: (_, r) => <span className="text-sm font-semibold">{r.configKey}</span>,
  },
  {
    key: 'version',
    title: 'Ver',
    width: '60px',
    render: (_, r) => <span className="text-sm tabular-nums text-gray-500">v{r.version}</span>,
  },
  {
    key: 'isActive',
    title: 'Status',
    width: '100px',
    render: (_, r) => (
      <div className="flex items-center gap-1.5">
        <span className={`inline-block h-2 w-2 rounded-full ${r.isActive ? 'bg-emerald-500' : 'bg-amber-400'}`} />
        <span className="text-xs text-gray-500">{r.isActive ? 'Active' : 'Inactive'}</span>
      </div>
    ),
  },
  {
    key: 'configValue',
    title: 'Preview',
    render: (_, r) => (
      <span className="text-sm text-gray-500 line-clamp-2">
        {r.configValue?.slice(0, 100)}{r.configValue?.length > 100 ? '...' : ''}
      </span>
    ),
  },
  {
    key: 'updatedAt',
    title: 'Updated',
    width: '120px',
    render: (_, r) => (
      <span className="text-sm text-gray-400">{r.updatedAt?.slice(0, 10)}</span>
    ),
  },
]

export default function PromptConfigPage() {
  const t = useTranslations('settings.prompt')
  const industrial = useStyleMode().mode === 'industrial'
  const { data, loading, error, refetch } = useFetch<PromptConfig>('/prompt-config')
  const { create } = useCreate<PromptConfig>('/api/prompt-config')
  const [showModal, setShowModal] = useState(false)
  const [selectedConfig, setSelectedConfig] = useState<PromptConfig | null>(null)
  const msg = useMessage()

  const handleActivate = async (id: string) => {
    try {
      await api(`/prompt-config/${id}/activate`, { method: 'PUT' })
      msg.success(t('version_activated'))
      refetch()
    } catch {
      msg.error(t('activate_failed'))
    }
  }

  const grouped: Record<string, PromptConfig[]> = {}
  for (const cfg of data) {
    if (!grouped[cfg.configKey]) grouped[cfg.configKey] = []
    grouped[cfg.configKey].push(cfg)
  }

  return (
    <MotionFade className="space-y-6">
      <div className={`flex items-center justify-between ${industrial ? 'border-b-2 border-ink pb-4' : ''}`}>
        <div>
          {industrial ? (
            <>
              <div className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
                // PROMPT ENGINEERING
              </div>
              <p className="mt-1 font-mono text-[11px] tracking-[0.04em] text-ink-muted">{t('page_subtitle')}</p>
            </>
          ) : (
            <>
              <h1 className="text-xl font-semibold">{t('page_title')}</h1>
              <p className="mt-1 text-sm text-gray-500">{t('page_subtitle')}</p>
            </>
          )}
        </div>
        <Button variant="primary" size="sm" onClick={() => setShowModal(true)}>
          <Plus className="h-3.5 w-3.5" />
          {t('new_version')}
        </Button>
      </div>

      <DataLoader loading={loading} message={t('loading')} minHeight={400}>
        {error ? (
          <div className="flex h-64 flex-col items-center justify-center gap-3">
            <span className="text-sm text-red-500">{error}</span>
            <Button variant="ghost" size="sm" onClick={refetch}>{t('retry')}</Button>
          </div>
        ) : (
          <div className="space-y-6">
      {/* Grouped cards */}
      {Object.entries(grouped).map(([key, configs]) => (
        <Card key={key} title={key} titlePrefix={key.slice(0, 3).toUpperCase()}>
          <div className="space-y-3">
            {configs
              .sort((a, b) => b.version - a.version)
              .map((cfg) => (
                <div
                  key={cfg.id}
                  className={`flex items-start justify-between border p-3 ${
                    industrial ? '' : 'rounded-lg'
                  } ${
                    cfg.isActive
                      ? (industrial ? 'border-ink bg-canvas-alt' : 'border-blue-200 bg-blue-50/50')
                      : (industrial ? 'border-ink/30' : 'border-border-light')
                  }`}
                >
                  <div className="flex-1 space-y-1">
                    <div className="flex items-center gap-2">
                      <span className={`font-semibold ${
                        industrial ? 'font-mono text-[12px] tracking-[0.04em]' : 'text-sm'
                      }`}>v{cfg.version}</span>
                      {cfg.isActive && <Badge variant="accent">{industrial ? 'ACTIVE' : 'Active'}</Badge>}
                      {cfg.mark && <Badge variant="success">{industrial ? 'MARKED' : 'Marked'}</Badge>}
                    </div>
                    <pre
                      className={`cursor-pointer text-xs line-clamp-3 transition-colors duration-150 whitespace-pre-wrap ${
                        industrial ? 'font-mono tracking-[0.02em] text-ink-muted hover:text-ink' : 'text-gray-500 hover:text-gray-900'
                      }`}
                      onClick={() => setSelectedConfig(cfg)}
                    >
                      {cfg.configValue}
                    </pre>
                    <div className={`flex gap-3 ${
                      industrial ? 'font-mono text-[10px] tabular-nums tracking-[0.08em] text-ink-ghost' : 'text-xs text-gray-400'
                    }`}>
                      <span>{cfg.updatedAt?.slice(0, 10)}</span>
                      {cfg.note && <span>{cfg.note}</span>}
                    </div>
                  </div>
                  {!cfg.isActive && (
                    <Button variant="ghost" size="sm" onClick={() => handleActivate(cfg.id)}>
                      <Power className="h-3 w-3" />
                      {t('activate')}
                    </Button>
                  )}
                </div>
              ))}
          </div>
        </Card>
      ))}

      {/* Full table view */}
      <DataTable
        columns={columns}
        data={data}
        rowKey="id"
        searchable
        searchPlaceholder="Search configs..."
        markable
        onMarkToggle={async (id, newValue) => {
          try {
            await api(`/prompt-config/${id}/mark`, { method: 'PUT', body: { mark: newValue } })
            msg.success(newValue ? t('marked') : t('unmarked'))
            refetch()
          } catch {
            msg.error(t('mark_update_failed'))
          }
        }}
        expandable={(record) => {
          const cfg = record as PromptConfig
          return (
            <div className="space-y-2">
              <div className="text-xs font-semibold text-gray-500">Config Value</div>
              <pre className="max-h-64 overflow-auto rounded-lg border border-border-light bg-gray-950 p-3 text-xs leading-relaxed text-green-400">
                {cfg.configValue}
              </pre>
              {cfg.note && (
                <div>
                  <div className="text-xs font-semibold text-gray-500 mb-1">Note</div>
                  <div className="text-xs text-gray-600">{cfg.note}</div>
                </div>
              )}
            </div>
          )
        }}
      />
          </div>
        )}
      </DataLoader>

      {/* Create modal */}
      <Modal open={showModal} onClose={() => setShowModal(false)} title={t('new_version_modal_title')}>
        <div className="space-y-4">
          <Input label="Config Key" placeholder="system_instruction / business_context / output_format" />
          <Textarea label="Config Value" placeholder={t('prompt_content_placeholder')} rows={10} />
          <Textarea label="Note" placeholder={t('version_note_placeholder')} rows={2} />
          <div className="flex justify-end gap-2 border-t border-border-light pt-4">
            <Button variant="ghost" onClick={() => setShowModal(false)}>{t('cancel')}</Button>
            <Button variant="primary" onClick={async () => {
              try {
                await create({} as PromptConfig)
                msg.success(t('version_created'))
                setShowModal(false)
                refetch()
              } catch {
                msg.error(t('create_failed'))
              }
            }}>{t('save')}</Button>
          </div>
        </div>
      </Modal>

      {/* View modal */}
      {selectedConfig && (
        <Modal open={!!selectedConfig} onClose={() => setSelectedConfig(null)} title={`${selectedConfig.configKey} v${selectedConfig.version}`}>
          <pre className="max-h-96 overflow-auto rounded-lg border border-border-light bg-gray-950 p-4 text-xs leading-relaxed text-green-400">
            {selectedConfig.configValue}
          </pre>
        </Modal>
      )}
    </MotionFade>
  )
}
