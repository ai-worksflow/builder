'use client'

import { useEffect, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import type { BuildTask, ProjectVersion } from '@/lib/worksflow/types'
import { StatusPill } from '../shared'
import { DOC_STATUS_CLASS } from '@/lib/worksflow/labels'
import { useWorksflow } from '@/lib/worksflow/store'
import { useLocalizedLabels } from '../use-localized-labels'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  Check,
  ChevronRight,
  Circle,
  Copy,
  FileText,
  GitBranch,
  Loader2,
  MoreHorizontal,
  Star,
  ThumbsDown,
  ThumbsUp,
  TriangleAlert,
  Workflow,
} from 'lucide-react'

export function TaskChecklist({ tasks }: { tasks: BuildTask[] }) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState<string | null>(null)
  return (
    <ul className="space-y-0.5">
      {tasks.map((task) => {
        const isExpandable = Boolean(task.subStatus)
        const open = expanded === task.id
        return (
          <li key={task.id}>
            <button
              type="button"
              onClick={() => isExpandable && setExpanded(open ? null : task.id)}
              className="flex w-full items-start gap-2.5 rounded-md px-1.5 py-1.5 text-left hover:bg-white/5"
            >
              <TaskIcon status={task.status} />
              <span className="flex-1">
                <span
                  className={cn(
                    'text-[13px] leading-snug',
                    task.status === 'done'
                      ? 'text-muted-foreground'
                      : task.status === 'error'
                        ? 'text-destructive'
                        : 'text-foreground',
                  )}
                >
                  {task.title}
                </span>
                {task.subStatus && (
                  <span className="mt-0.5 flex items-center gap-1.5 font-mono text-[11px] text-primary-bright">
                    {task.subStatus}
                  </span>
                )}
              </span>
              {isExpandable && (
                <ChevronRight
                  className={cn(
                    'mt-0.5 h-3.5 w-3.5 shrink-0 text-faint-foreground transition-transform',
                    open && 'rotate-90',
                  )}
                />
              )}
            </button>
            {open && (
              <div className="ml-8 mb-1 rounded-md border border-border bg-black/30 p-2 font-mono text-[11px] text-muted-foreground">
                {task.subStatus}
                {'\n'}
                <span className="text-faint-foreground">
                  - {t('chat.runningStep', { task: task.title })}
                </span>
              </div>
            )}
          </li>
        )
      })}
    </ul>
  )
}

function TaskIcon({ status }: { status: BuildTask['status'] }) {
  if (status === 'done')
    return (
      <span className="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-success/15">
        <Check className="h-3 w-3 text-success" />
      </span>
    )
  if (status === 'active')
    return <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin text-primary-bright" />
  if (status === 'error')
    return <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
  return <Circle className="mt-0.5 h-4 w-4 shrink-0 text-faint-foreground" />
}

export function ResponseActions() {
  const { t } = useI18n()
  return (
    <div className="flex items-center gap-1 pt-1 text-faint-foreground">
      <ActionButton label={t('chat.copy')}>
        <Copy className="h-3.5 w-3.5" />
      </ActionButton>
      <ActionButton label={t('chat.thumbsUp')}>
        <ThumbsUp className="h-3.5 w-3.5" />
      </ActionButton>
      <ActionButton label={t('chat.thumbsDown')}>
        <ThumbsDown className="h-3.5 w-3.5" />
      </ActionButton>
      <ActionButton label={t('workbench.more')}>
        <MoreHorizontal className="h-3.5 w-3.5" />
      </ActionButton>
    </div>
  )
}

function ActionButton({
  children,
  label,
}: {
  children: React.ReactNode
  label: string
}) {
  return (
    <button
      type="button"
      className="flex h-6 w-6 items-center justify-center rounded hover:bg-white/5 hover:text-foreground"
      aria-label={label}
      title={label}
    >
      {children}
    </button>
  )
}

export function VersionCard({
  version,
  onToggleStar,
}: {
  version: ProjectVersion
  onToggleStar: (id: string) => void
}) {
  const { t } = useI18n()
  return (
    <div className="flex items-center gap-3 rounded-lg border border-border bg-card p-3">
      <span className="flex h-8 w-8 items-center justify-center rounded-md bg-primary/15 text-primary-bright">
        <FileText className="h-4 w-4" />
      </span>
      <div className="min-w-0 flex-1">
        <p className="truncate text-[13px] font-medium text-foreground">{version.title}</p>
        <p className="text-[11px] text-faint-foreground">{version.subtitle}</p>
      </div>
      <button
        type="button"
        onClick={() => onToggleStar(version.id)}
        className="flex h-7 w-7 items-center justify-center rounded-md hover:bg-white/5"
        aria-label={version.starred ? t('recent.unstar') : t('recent.star')}
        aria-pressed={version.starred}
      >
        <Star
          className={cn(
            'h-4 w-4',
            version.starred ? 'fill-warning text-warning' : 'text-faint-foreground',
          )}
        />
      </button>
    </div>
  )
}

export function BlueprintContextCard() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const {
    activeBlueprintContext,
    blueprint,
    documents,
    setSurface,
    setTeamView,
    setSelectedBlueprintNodeId,
  } = useWorksflow()
  if (!activeBlueprintContext) return null

  const nodes = blueprint.nodes.filter((node) => activeBlueprintContext.nodeIds.includes(node.id))
  const edges = blueprint.edges.filter((edge) => activeBlueprintContext.edgeIds.includes(edge.id))
  const linkedDocs = documents.filter((doc) => activeBlueprintContext.linkedDocIds.includes(doc.id))
  const target = blueprint.nodes.find((node) => node.id === activeBlueprintContext.workbenchTargetId)

  return (
    <div className="rounded-lg border border-primary/30 bg-primary/10 p-3">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-1.5 text-[12px] font-semibold text-primary-bright">
            <Workflow className="h-3.5 w-3.5" />
            {t('blueprint.context')}
          </div>
          <div className="mt-1 truncate text-sm font-semibold text-foreground">
            {activeBlueprintContext.title}
          </div>
        </div>
        <span className="shrink-0 rounded-md border border-primary/30 bg-background/60 px-1.5 py-0.5 text-[10px] font-medium text-primary-bright">
          {activeBlueprintContext.status}
        </span>
      </div>

      <div className="mt-3 grid grid-cols-4 gap-1.5 text-center text-[10px] text-faint-foreground">
        <Metric value={String(nodes.length)} label={t('blueprint.nodes')} />
        <Metric value={String(edges.length)} label={t('blueprint.edgesLabel')} />
        <Metric value={String(linkedDocs.length)} label={t('blueprint.docs')} />
        <Metric value={String(activeBlueprintContext.memberIds.length)} label={t('blueprint.members')} />
      </div>

      <div className="mt-3 space-y-1.5">
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <GitBranch className="h-3.5 w-3.5 text-faint-foreground" />
          <span className="min-w-0 flex-1 truncate">
            {t('blueprint.target', {
              target: target?.title ?? activeBlueprintContext.workbenchTargetId,
            })}
          </span>
        </div>
        <div className="flex flex-wrap gap-1">
          {nodes.slice(0, 5).map((node) => (
            <span
              key={node.id}
              className="rounded border border-border bg-background/60 px-1.5 py-0.5 text-[10px] text-muted-foreground"
            >
              {labels.blueprintNode(node.type)}: {node.title}
            </span>
          ))}
        </div>
      </div>

      {activeBlueprintContext.missingItems.length > 0 && (
        <div className="mt-3 rounded-md border border-amber-400/30 bg-amber-400/10 px-2.5 py-2 text-[11px] leading-relaxed text-warning">
          {t('blueprint.issuesRemain', { count: activeBlueprintContext.missingItems.length })}
        </div>
      )}

      <button
        type="button"
        onClick={() => {
          setSelectedBlueprintNodeId(activeBlueprintContext.selectedNodeId)
          setTeamView('blueprint')
          setSurface('team')
        }}
        className="mt-3 inline-flex w-full items-center justify-center gap-1.5 rounded-md border border-primary/30 px-3 py-1.5 text-[12px] font-medium text-primary-bright hover:bg-primary/10"
      >
        <Workflow className="h-3.5 w-3.5" />
        {t('blueprint.openSelection')}
      </button>
    </div>
  )
}

function Metric({ value, label }: { value: string; label: string }) {
  return (
    <div className="rounded-md border border-primary/20 bg-background/50 px-2 py-1.5">
      <div className="text-[13px] font-semibold text-foreground">{value}</div>
      <div>{label}</div>
    </div>
  )
}

export function LinkedDocsCard() {
  const { documents, linkedDocIds, toggleLinkedDoc, syncWorkbenchBackToDocs } = useWorksflow()
  const { session, can, authorize } = useCollaboration()
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const [synced, setSynced] = useState(false)
  const [overrideAccepted, setOverrideAccepted] = useState(false)
  const linkedDocKey = linkedDocIds.join('|')
  const linkedDocs = linkedDocIds
    .map((id) => documents.find((doc) => doc.id === id))
    .filter((doc): doc is NonNullable<typeof doc> => Boolean(doc))
  const availableDocs = documents.filter((doc) => !linkedDocIds.includes(doc.id))
  const nonApproved = linkedDocs.filter((doc) => doc?.status !== 'approved')
  const canEdit = session.signedIn && can('edit')

  useEffect(() => {
    setOverrideAccepted(false)
  }, [linkedDocKey])

  return (
    <div className="rounded-lg border border-border bg-card p-3">
      <div className="mb-2 flex items-center justify-between">
        <p className="text-[13px] font-medium text-foreground">{t('chat.linkedDocs')}</p>
        <span className="rounded-md bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary-bright">
          {t('chat.contextLocked')}
        </span>
      </div>
      <ul className="space-y-1">
        {linkedDocs.map((doc) => doc && (
          <li
            key={doc.id}
            className="flex items-center justify-between rounded-md px-2 py-1.5 hover:bg-white/5"
          >
            <span className="flex min-w-0 items-center gap-2 text-[12px] text-muted-foreground">
              <FileText className="h-3.5 w-3.5 text-faint-foreground" />
              <span className="truncate">{doc.title}</span>
            </span>
            <div className="ml-2 flex shrink-0 items-center gap-1.5">
              <StatusPill
                label={labels.docStatus(doc.status)}
                className={DOC_STATUS_CLASS[doc.status]}
              />
              <button
                type="button"
                onClick={() => void authorize('edit').then((allowed) => allowed && toggleLinkedDoc(doc.id))}
                disabled={!canEdit}
                className="min-h-6 rounded px-1 text-[10px] text-faint-foreground hover:bg-white/5 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
              >
                {t('common.remove')}
              </button>
            </div>
          </li>
        ))}
      </ul>

      {nonApproved.length > 0 && !overrideAccepted && (
        <div className="mt-2 rounded-md border border-amber-400/30 bg-amber-400/5 px-2.5 py-2 text-[11px] leading-relaxed text-warning">
          {t('chat.nonApprovedWarning', { count: nonApproved.length })}
          <button
            type="button"
            onClick={() => setOverrideAccepted(true)}
            className="ml-2 min-h-6 rounded bg-amber-400/15 px-1.5 py-0.5 font-medium hover:bg-amber-400/25"
          >
            {t('common.confirm')}
          </button>
        </div>
      )}

      {nonApproved.length > 0 && overrideAccepted && (
        <div className="mt-2 rounded-md border border-emerald-400/30 bg-emerald-400/10 px-2.5 py-2 text-[11px] leading-relaxed text-success">
          {t('chat.overrideRecorded')}
        </div>
      )}

      {availableDocs.length > 0 && (
        <details className="mt-2">
          <summary className="cursor-pointer rounded-md px-2 py-1 text-[12px] font-medium text-muted-foreground hover:bg-white/5">
            {t('chat.addLinkedContext')}
          </summary>
          <div className="mt-1 space-y-1">
            {availableDocs.slice(0, 4).map((doc) => (
              <button
                key={doc.id}
                type="button"
                onClick={() => void authorize('edit').then((allowed) => allowed && toggleLinkedDoc(doc.id))}
                disabled={!canEdit}
                className="flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40"
              >
                <span className="text-[12px] text-muted-foreground">{doc.title}</span>
                <span className="text-[10px] text-faint-foreground">
                  {labels.docType(doc.type)}
                </span>
              </button>
            ))}
          </div>
        </details>
      )}

      <button
        type="button"
        onClick={() => {
          void authorize('edit').then((allowed) => {
            if (!allowed) return
            syncWorkbenchBackToDocs()
            setSynced(true)
          })
        }}
        disabled={!canEdit}
        className="mt-2 w-full rounded-md border border-border py-1.5 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
      >
        {synced ? t('chat.syncedSummary') : t('chat.syncBack')}
      </button>
    </div>
  )
}
