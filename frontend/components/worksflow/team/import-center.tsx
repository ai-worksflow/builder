'use client'

import { useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import { SYNC_STATUS_CLASS } from '@/lib/worksflow/labels'
import type { ImportSource } from '@/lib/worksflow/types'
import { useLocalizedLabels } from '../use-localized-labels'
import { StatusPill, memberById, Avatar } from '../shared'
import {
  FileUp,
  Frame,
  Link2,
  PenTool,
  Plus,
  RefreshCw,
  Shapes,
  Component,
} from 'lucide-react'

const SOURCE_ICON: Record<ImportSource, typeof Frame> = {
  figma: Frame,
  penpot: PenTool,
  excalidraw: Shapes,
  tldraw: Frame,
  storybook: Component,
  upload: FileUp,
}

const SOURCES: ImportSource[] = [
  'figma',
  'penpot',
  'excalidraw',
  'tldraw',
  'storybook',
  'upload',
]

export function ImportCenter() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const {
    openDoc,
    importAssets,
    syncImportAsset,
    detachImportAsset,
    updateDocumentStatus,
    createDocument,
  } = useWorksflow()
  const [connectSource, setConnectSource] = useState<ImportSource | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  return (
    <div className="h-full overflow-y-auto scrollbar-thin bg-canvas">
      <div className="mx-auto max-w-5xl px-6 py-6 max-sm:px-4">
        <div className="flex items-center justify-between gap-3 max-sm:flex-wrap">
          <div>
            <h1 className="text-lg font-semibold text-foreground">{t('imports.title')}</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              {t('imports.description')}
            </p>
          </div>
          <button
            onClick={() => setConnectSource('figma')}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-white hover:bg-primary/90"
          >
            <Plus className="size-4" /> {t('imports.new')}
          </button>
        </div>

        {/* Connect sources */}
        <div className="mt-6 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
          {SOURCES.map((src) => {
            const Icon = SOURCE_ICON[src]
            return (
              <button
                key={src}
                onClick={() => setConnectSource(src)}
                className="flex flex-col items-center gap-2 rounded-lg border border-border bg-surface p-4 text-center transition-colors hover:border-primary/40"
              >
                <span className="flex size-10 items-center justify-center rounded-lg bg-surface-2">
                  <Icon className="size-5 text-primary-bright" />
                </span>
                <span className="text-xs font-medium text-foreground">
                  {labels.importSource(src)}
                </span>
              </button>
            )
          })}
        </div>

        {/* Connected assets */}
        <div className="mt-8 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-foreground">{t('imports.connectedAssets')}</h2>
          <span className="text-xs text-faint-foreground">
            {t('imports.count', { count: importAssets.length })}
          </span>
        </div>

        {notice && (
          <div className="mt-3 rounded-lg border border-primary/30 bg-primary/10 px-3 py-2 text-xs text-primary-bright">
            {notice}
          </div>
        )}

        <div className="mt-3 overflow-x-auto rounded-lg border border-border scrollbar-thin">
          <table className="min-w-[920px] w-full text-left text-sm">
            <thead className="bg-surface-2 text-[11px] uppercase tracking-wide text-faint-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">{t('imports.asset')}</th>
                <th className="px-4 py-2.5 font-medium">{t('common.source')}</th>
                <th className="px-4 py-2.5 font-medium">{t('common.status')}</th>
                <th className="px-4 py-2.5 font-medium">{t('imports.linkedDoc')}</th>
                <th className="px-4 py-2.5 font-medium">{t('common.owner')}</th>
                <th className="px-4 py-2.5 font-medium">{t('imports.lastSynced')}</th>
                <th className="px-4 py-2.5" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {importAssets.map((asset) => {
                const Icon = SOURCE_ICON[asset.source]
                const owner = memberById(asset.ownerId)
                return (
                  <tr key={asset.id} className="bg-surface hover:bg-white/[0.02]">
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2">
                        <Frame className="size-4 text-faint-foreground" />
                        <span className="font-medium text-foreground">{asset.name}</span>
                      </div>
                    </td>
                    <td className="px-4 py-3">
                      <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                        <Icon className="size-3.5" />
                        {labels.importSource(asset.source)}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <StatusPill
                        label={labels.syncStatus(asset.syncStatus)}
                        className={SYNC_STATUS_CLASS[asset.syncStatus]}
                      />
                    </td>
                    <td className="px-4 py-3">
                      {asset.linkedDocTitle ? (
                        <button
                          onClick={() => openDoc('d6')}
                          className="inline-flex items-center gap-1 text-xs text-primary-bright hover:underline"
                        >
                          <Link2 className="size-3" />
                          {asset.linkedDocTitle}
                        </button>
                      ) : (
                        <span className="text-xs text-faint-foreground">{t('imports.notLinked')}</span>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      {owner && (
                        <div className="flex items-center gap-1.5">
                          <Avatar member={owner} size={20} />
                          <span className="text-xs text-muted-foreground">{owner.name}</span>
                        </div>
                      )}
                    </td>
                    <td className="px-4 py-3 text-xs text-faint-foreground">
                      {asset.lastSyncedAt ?? '—'}
                    </td>
                    <td className="px-4 py-3 text-right">
                      <div className="flex justify-end gap-1.5">
                        <button
                          onClick={() => setNotice(t('imports.mappedNotice', { asset: asset.name }))}
                          className="rounded-md border border-border px-2 py-1 text-[11px] font-medium text-muted-foreground hover:bg-white/5"
                        >
                          {t('imports.mapFrames')}
                        </button>
                        <button
                          onClick={() => {
                            const docId =
                              asset.linkedDocId ??
                              createDocument(
                                'uiPrototype',
                                `${asset.name} UI prototype`,
                                'readyForReview',
                              )
                            updateDocumentStatus(docId, 'readyForReview')
                            openDoc(docId)
                          }}
                          className="rounded-md border border-border px-2 py-1 text-[11px] font-medium text-muted-foreground hover:bg-white/5"
                        >
                          {t('imports.generateUiDoc')}
                        </button>
                        <button
                          onClick={() => syncImportAsset(asset.id)}
                          className={cn(
                            'inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-[11px] font-medium text-muted-foreground hover:bg-white/5',
                            asset.syncStatus === 'syncing' && 'text-primary-bright',
                          )}
                        >
                          <RefreshCw
                            className={cn(
                              'size-3',
                              asset.syncStatus === 'syncing' && 'animate-spin',
                            )}
                          />
                          {t('imports.sync')}
                        </button>
                        <button
                          onClick={() => {
                            detachImportAsset(asset.id)
                            setNotice(t('imports.detachedNotice', { asset: asset.name }))
                          }}
                          className="rounded-md border border-border px-2 py-1 text-[11px] font-medium text-muted-foreground hover:bg-white/5"
                        >
                          {t('imports.detach')}
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>
      {connectSource && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
          <div className="w-full max-w-lg rounded-lg border border-border bg-popover p-4 shadow-2xl">
            <h3 className="text-sm font-semibold text-foreground">
              {t('imports.connectSourceTitle', { source: labels.importSource(connectSource) })}
            </h3>
            <p className="mt-2 text-[12px] leading-relaxed text-muted-foreground">
              {t('imports.connectCopy')}
            </p>
            <input
              placeholder={
                connectSource === 'upload'
                  ? t('imports.uploadPlaceholder')
                  : t('imports.urlPlaceholder', { source: labels.importSource(connectSource) })
              }
              className="mt-3 w-full rounded-md border border-border bg-background px-3 py-2 text-[13px] text-foreground outline-none placeholder:text-faint-foreground focus:border-primary/60 focus:ring-1 focus:ring-primary/40"
            />
            <div className="mt-4 flex justify-end gap-2">
              <button
                onClick={() => setConnectSource(null)}
                className="rounded-md border border-border px-3 py-1.5 text-[12px] font-medium text-muted-foreground hover:bg-white/5"
              >
                {t('common.cancel')}
              </button>
              <button
                onClick={() => {
                  setNotice(t('imports.connectedNotice', { source: labels.importSource(connectSource) }))
                  setConnectSource(null)
                }}
                className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright"
              >
                {t('imports.connectSource')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
