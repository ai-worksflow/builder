'use client'

import { useState } from 'react'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import { DOC_STATUS_CLASS } from '@/lib/worksflow/labels'
import { useLocalizedLabels } from '../use-localized-labels'
import { Avatar, StatusPill, memberById } from '../shared'
import { Check, MessageSquare, RefreshCw, X } from 'lucide-react'

const FILTERS = [
  { id: 'all', labelKey: 'reviews.filter.all' },
  { id: 'assigned', labelKey: 'reviews.filter.assigned' },
  { id: 'review', labelKey: 'reviews.filter.review' },
  { id: 'blocked', labelKey: 'reviews.filter.blocked' },
  { id: 'needsSync', labelKey: 'reviews.filter.needsSync' },
  { id: 'approved', labelKey: 'reviews.filter.approved' },
] as const

type FilterId = (typeof FILTERS)[number]['id']

export function ReviewCenter() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const { documents, updateDocumentStatus } = useWorksflow()
  const [filter, setFilter] = useState<FilterId>('all')
  const [selectedId, setSelectedId] = useState('d3')
  const selected = documents.find((doc) => doc.id === selectedId) ?? documents[0]

  const docs = documents.filter((d) => {
    if (filter === 'all') return true
    if (filter === 'assigned') return d.members.some((m) => m.userId === 'm1')
    if (filter === 'review') return d.status === 'readyForReview'
    if (filter === 'blocked') return d.blocking > 0
    if (filter === 'needsSync') return d.status === 'needsSync'
    if (filter === 'approved') return d.status === 'approved'
    return true
  })

  if (!selected) {
    return (
      <div className="flex h-full items-center justify-center bg-canvas p-6 text-center">
        <div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-5">
          <MessageSquare className="mx-auto size-8 text-primary-bright" />
          <h1 className="mt-3 text-base font-semibold text-foreground">{t('graph.emptyTitle')}</h1>
          <p className="mt-2 text-sm text-muted-foreground">{t('graph.emptyBody')}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between border-b border-border px-6 py-4 max-sm:px-4">
        <div>
          <h1 className="text-lg font-semibold text-foreground">{t('reviews.title')}</h1>
          <p className="text-[12px] text-muted-foreground">
            {t('reviews.description')}
          </p>
        </div>
      </div>

      {/* Filters */}
      <div className="flex items-center gap-1.5 overflow-x-auto border-b border-border px-6 py-2.5 scrollbar-thin max-sm:px-4">
        {FILTERS.map((f) => (
          <button
            key={f.id}
            type="button"
            onClick={() => setFilter(f.id)}
            className={cn(
              'shrink-0 rounded-md px-2.5 py-1 text-[12px] font-medium transition-colors',
              filter === f.id
                ? 'bg-primary/15 text-primary-bright'
                : 'text-muted-foreground hover:bg-white/5 hover:text-foreground',
            )}
          >
            {t(f.labelKey as MessageKey)}
          </button>
        ))}
      </div>

      <div className="flex min-h-0 flex-1 max-lg:flex-col">
        {/* List */}
        <div className="flex w-1/2 flex-col overflow-auto scrollbar-thin border-r border-border max-lg:max-h-[360px] max-lg:w-full max-lg:border-b max-lg:border-r-0">
          <table className="min-w-[620px] w-full text-left text-[12px]">
            <thead className="sticky top-0 bg-panel text-faint-foreground">
              <tr className="border-b border-border">
                <th className="px-4 py-2 font-medium">{t('common.document')}</th>
                <th className="px-2 py-2 font-medium">{t('common.status')}</th>
                <th className="px-2 py-2 font-medium">{t('common.owner')}</th>
                <th className="px-2 py-2 font-medium">{t('reviews.reviewer')}</th>
              </tr>
            </thead>
            <tbody>
              {docs.map((doc) => {
                const reviewer = doc.members.find((m) => m.role === 'reviewer')
                return (
                  <tr
                    key={doc.id}
                    onClick={() => setSelectedId(doc.id)}
                    className={cn(
                      'cursor-pointer border-b border-border transition-colors',
                      selected.id === doc.id ? 'bg-primary/10' : 'hover:bg-white/5',
                    )}
                  >
                    <td className="px-4 py-3">
                      <p className="font-medium text-foreground">{doc.title}</p>
                      <p className="text-[11px] text-faint-foreground">
                        {labels.docType(doc.type)} · {doc.updatedAt}
                      </p>
                    </td>
                    <td className="px-2 py-3">
                      <StatusPill
                        label={labels.docStatus(doc.status)}
                        className={DOC_STATUS_CLASS[doc.status]}
                      />
                    </td>
                    <td className="px-2 py-3">
                      <Avatar member={memberById(doc.ownerId)!} size={22} />
                    </td>
                    <td className="px-2 py-3">
                      {reviewer ? (
                        <Avatar member={memberById(reviewer.userId)!} size={22} />
                      ) : (
                        <span className="text-faint-foreground">—</span>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>

        {/* Detail */}
        <div className="flex w-1/2 flex-col overflow-y-auto scrollbar-thin p-5 max-lg:w-full max-sm:p-4">
          <div className="mb-1 flex items-center gap-2">
            <h2 className="text-[15px] font-semibold text-foreground">{selected.title}</h2>
            <StatusPill
              label={labels.docStatus(selected.status)}
              className={DOC_STATUS_CLASS[selected.status]}
            />
          </div>
          <p className="text-[12px] text-muted-foreground">{selected.summary}</p>

          <div className="mt-4 rounded-lg border border-border bg-panel p-3">
            <p className="mb-1 text-[12px] font-medium text-foreground">{t('reviews.changeSummary')}</p>
            <p className="text-[12px] leading-relaxed text-muted-foreground">
              {t('reviews.changeSummaryCopy')}
            </p>
          </div>

          {selected.blocking > 0 && (
            <div className="mt-3 flex items-center gap-2 rounded-lg border border-amber-400/30 bg-amber-400/10 px-3 py-2 text-[12px] text-warning">
              <RefreshCw className="h-3.5 w-3.5" />
              {t('reviews.blocksCount', { count: selected.blocking })}
            </div>
          )}

          {/* Comment thread */}
          <div className="mt-4">
            <p className="mb-2 flex items-center gap-1.5 text-[12px] font-medium text-foreground">
              <MessageSquare className="h-3.5 w-3.5 text-muted-foreground" />
              {t('reviews.commentThread')}
            </p>
            <div className="space-y-3">
              <Comment
                memberId="m6"
                text="边界条件写得清楚，但批量操作上限需要和后端确认性能影响。"
                time="20m ago"
              />
              <Comment
                memberId="m2"
                text="已同步 Emma，API 契约会加上 batch 限制的错误码。"
                time="12m ago"
              />
            </div>
          </div>

          {/* Actions */}
          <div className="mt-auto flex flex-wrap gap-2 pt-5">
            <button
              type="button"
              onClick={() => updateDocumentStatus(selected.id, 'approved')}
              className="flex items-center gap-1.5 rounded-md bg-success px-3 py-2 text-[12px] font-semibold text-success-foreground hover:opacity-90"
            >
              <Check className="h-3.5 w-3.5" />
              {t('common.approve')}
            </button>
            <button
              type="button"
              onClick={() => updateDocumentStatus(selected.id, 'changesRequested')}
              className="flex items-center gap-1.5 rounded-md border border-border px-3 py-2 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" />
              {t('reviews.requestChanges')}
            </button>
            <button
              type="button"
              onClick={() => updateDocumentStatus(selected.id, 'readyForReview')}
              className="flex items-center gap-1.5 rounded-md border border-border px-3 py-2 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
            >
              <RefreshCw className="h-3.5 w-3.5" />
              {t('reviews.markSynced')}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

function Comment({
  memberId,
  text,
  time,
}: {
  memberId: string
  text: string
  time: string
}) {
  const m = memberById(memberId)!
  return (
    <div className="flex gap-2.5">
      <Avatar member={m} size={24} />
      <div className="flex-1 rounded-lg border border-border bg-panel px-3 py-2">
        <div className="flex items-center justify-between">
          <span className="text-[12px] font-medium text-foreground">{m.name}</span>
          <span className="text-[11px] text-faint-foreground">{time}</span>
        </div>
        <p className="mt-0.5 text-[12px] leading-relaxed text-muted-foreground">{text}</p>
      </div>
    </div>
  )
}
