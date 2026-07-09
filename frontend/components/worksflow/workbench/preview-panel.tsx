'use client'

import { useState } from 'react'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import type { TodoTask } from '@/lib/worksflow/types'
import {
  Check,
  Copy,
  ExternalLink,
  Loader2,
  Maximize2,
  Monitor,
  RotateCw,
  Smartphone,
  TriangleAlert,
} from 'lucide-react'

export function PreviewPanel() {
  const { t } = useI18n()
  const { phase, startBuild } = useWorksflow()
  const [device, setDevice] = useState<'desktop' | 'mobile'>('desktop')
  const [fullscreen, setFullscreen] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)

  function showNotice(message: string) {
    setNotice(message)
    window.setTimeout(() => setNotice(null), 1800)
  }

  function refreshPreview() {
    setRefreshing(true)
    window.setTimeout(() => {
      setRefreshing(false)
      showNotice(t('preview.refreshed'))
    }, 700)
  }

  return (
    <div
      className={cn(
        'relative flex h-full flex-col bg-panel',
        fullscreen &&
          'fixed inset-4 z-[70] overflow-hidden rounded-lg border border-border shadow-2xl shadow-black/60',
      )}
    >
      <PreviewToolbar
        device={device}
        fullscreen={fullscreen}
        refreshing={refreshing}
        onRefresh={refreshPreview}
        onCopy={() => {
          const url = `${window.location.origin}/`
          void navigator.clipboard?.writeText(url)
          showNotice(t('preview.urlCopied'))
        }}
        onDevice={setDevice}
        onFullscreen={() => setFullscreen((value) => !value)}
        onOpen={() => {
          const opened = window.open('/', '_blank', 'noopener,noreferrer')
          showNotice(opened ? t('preview.opened') : t('preview.openBlocked'))
        }}
      />
      <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin bg-background">
        {refreshing ? (
          <LoadingState />
        ) : phase === 'complete' ? (
          <TodoApp device={device} />
        ) : phase === 'building' ? (
          <LoadingState />
        ) : phase === 'error' ? (
          <ErrorState onRetry={startBuild} />
        ) : (
          <EmptyState onHelp={(target) => showNotice(t('preview.openedTarget', { target }))} />
        )}
      </div>
      {notice && (
        <div className="absolute bottom-4 left-1/2 z-20 -translate-x-1/2 rounded-md border border-border bg-popover px-3 py-2 text-xs text-muted-foreground shadow-2xl">
          {notice}
        </div>
      )}
    </div>
  )
}

function PreviewToolbar({
  device,
  fullscreen,
  refreshing,
  onRefresh,
  onCopy,
  onDevice,
  onFullscreen,
  onOpen,
}: {
  device: 'desktop' | 'mobile'
  fullscreen: boolean
  refreshing: boolean
  onRefresh: () => void
  onCopy: () => void
  onDevice: (device: 'desktop' | 'mobile') => void
  onFullscreen: () => void
  onOpen: () => void
}) {
  const { t } = useI18n()

  return (
    <div className="flex h-10 shrink-0 items-center gap-2 border-b border-border bg-panel px-2.5">
      <div className="flex items-center gap-1">
        <ToolbarButton label={t('preview.refresh')} onClick={onRefresh}>
          <RotateCw className={cn('h-3.5 w-3.5', refreshing && 'animate-spin')} />
        </ToolbarButton>
      </div>
      <div className="flex min-w-0 flex-1 items-center rounded-md bg-white/5 px-2.5 py-1 text-[12px] text-muted-foreground">
        /
      </div>
      <div className="flex items-center gap-1">
        <ToolbarButton label={t('preview.copyLink')} onClick={onCopy}>
          <Copy className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton
          label={t('preview.mobileView')}
          active={device === 'mobile'}
          onClick={() => onDevice('mobile')}
        >
          <Smartphone className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton
          label={t('preview.desktopView')}
          active={device === 'desktop'}
          onClick={() => onDevice('desktop')}
        >
          <Monitor className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton label={t('preview.fullscreen')} active={fullscreen} onClick={onFullscreen}>
          <Maximize2 className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton label={t('preview.openNewWindow')} onClick={onOpen}>
          <ExternalLink className="h-3.5 w-3.5" />
        </ToolbarButton>
      </div>
    </div>
  )
}

function ToolbarButton({
  children,
  label,
  active,
  onClick,
}: {
  children: React.ReactNode
  label: string
  active?: boolean
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex h-7 w-7 items-center justify-center rounded-md hover:bg-white/5 hover:text-foreground',
        active ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground',
      )}
      aria-label={label}
      aria-pressed={active}
      title={label}
    >
      {children}
    </button>
  )
}

function EmptyState({ onHelp }: { onHelp: (target: string) => void }) {
  const { t } = useI18n()

  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 px-6">
      <div className="flex h-14 w-14 items-center justify-center rounded-lg border border-border bg-panel">
        <svg
          aria-hidden="true"
          className="h-7 w-7 text-muted-foreground"
          viewBox="0 0 20 20"
          fill="none"
          stroke="currentColor"
          strokeWidth="0.6"
        >
          <path
            d="M14.2 14.2H17V6.9375C17 4.76288 15.2371 3 13.0625 3H5.8V5.8M14.2 14.2V7.79063L7.79062 14.2H14.2ZM14.2 14.2V17H6.9375C4.76288 17 3 15.2371 3 13.0625V5.8H5.8M5.8 5.8V12.2313L12.2313 5.8H5.8Z"
            strokeLinejoin="round"
          />
        </svg>
      </div>
      <p className="text-[13px] text-muted-foreground">{t('preview.emptyTitle')}</p>
      <p className="max-w-xs text-center text-[12px] text-faint-foreground">
        {t('preview.emptyHintPrefix')}{' '}
        <span className="text-primary-bright">{t('chat.implementPlan')}</span>{' '}
        {t('preview.emptyHintSuffix')}
      </p>
      <div className="mt-6 flex items-center gap-6 text-[12px] text-faint-foreground">
        <button
          type="button"
          onClick={() => onHelp(t('preview.helpCenter'))}
          className="hover:text-foreground"
        >
          {t('preview.helpCenter')}
        </button>
        <button
          type="button"
          onClick={() => onHelp(t('preview.community'))}
          className="hover:text-foreground"
        >
          {t('preview.community')}
        </button>
      </div>
    </div>
  )
}

function LoadingState() {
  const { t } = useI18n()

  return (
    <div className="flex h-full flex-col items-center justify-center gap-3">
      <Loader2 className="h-6 w-6 animate-spin text-primary-bright" />
      <p className="text-[13px] text-muted-foreground">{t('preview.loading')}</p>
    </div>
  )
}

function ErrorState({ onRetry }: { onRetry: () => void }) {
  const { t } = useI18n()

  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-lg border border-red-500/30 bg-red-500/10">
        <TriangleAlert className="h-5 w-5 text-destructive" />
      </div>
      <div>
        <p className="text-[13px] font-medium text-foreground">{t('preview.errorTitle')}</p>
        <p className="mt-1 max-w-sm text-[12px] leading-relaxed text-muted-foreground">
          {t('preview.errorCopy')}
        </p>
      </div>
      <button
        type="button"
        onClick={onRetry}
        className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright"
      >
        {t('preview.retryBuild')}
      </button>
    </div>
  )
}

// ---- The actual generated Todo app (Frame 04) ----

function TodoApp({ device }: { device: 'desktop' | 'mobile' }) {
  const { t } = useI18n()
  const { todos, toggleTodo, addTodo, todoFilter, setTodoFilter } = useWorksflow()
  const [draft, setDraft] = useState('')
  const [priority, setPriority] = useState<TodoTask['priority']>('Med')

  const active = todos.filter((t) => !t.done).length
  const done = todos.filter((t) => t.done).length
  const total = todos.length

  const visible = todos.filter((t) =>
    todoFilter === 'all' ? true : todoFilter === 'active' ? !t.done : t.done,
  )

  function submit(e: React.FormEvent) {
    e.preventDefault()
    addTodo(draft, priority)
    setDraft('')
  }

  return (
    <div
      className={cn(
        'mx-auto min-h-full px-5 py-6 transition-[max-width] duration-200',
        device === 'mobile' ? 'max-w-[390px]' : 'max-w-2xl',
      )}
    >
      {/* Top nav */}
      <header className="mb-6 flex items-center justify-between gap-3 max-sm:flex-col max-sm:items-start">
        <div className="flex items-center gap-2">
          <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary text-sm font-bold text-primary-foreground">
            T
          </span>
          <span className="text-lg font-semibold">Taskflow</span>
        </div>
        <div className="flex flex-wrap items-center gap-3 text-[12px]">
          <Stat value={active} label={t('preview.active')} className="text-primary-bright" />
          <Stat value={done} label={t('preview.done')} className="text-success" />
          <Stat value={total} label={t('preview.total')} className="text-muted-foreground" />
        </div>
      </header>

      {/* Input */}
      <form onSubmit={submit} className="mb-4 flex flex-wrap items-center gap-2">
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={t('preview.addTask')}
          className="min-w-[180px] flex-1 rounded-lg border border-border bg-card px-3 py-2 text-[13px] placeholder:text-faint-foreground focus:border-primary/60 focus:outline-none focus:ring-1 focus:ring-primary/40 max-sm:w-full"
          aria-label={t('preview.addTaskAria')}
        />
        <div className="flex items-center gap-1 rounded-lg bg-white/5 p-0.5">
          {(['Low', 'Med', 'High'] as const).map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => setPriority(p)}
              className={cn(
                'rounded-md px-2.5 py-1.5 text-[12px] font-medium transition-colors',
                priority === p
                  ? 'bg-secondary text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
              aria-pressed={priority === p}
            >
              {t(priorityLabelKey[p])}
            </button>
          ))}
        </div>
      </form>

      {/* Filters */}
      <div className="mb-4 flex items-center gap-1">
        {(['all', 'active', 'completed'] as const).map((f) => (
          <button
            key={f}
            type="button"
            onClick={() => setTodoFilter(f)}
            className={cn(
              'rounded-md px-3 py-1.5 text-[12px] font-medium capitalize transition-colors',
              todoFilter === f
                ? 'bg-primary/15 text-primary-bright'
                : 'text-muted-foreground hover:bg-white/5 hover:text-foreground',
            )}
            aria-pressed={todoFilter === f}
          >
            {t(filterLabelKey[f])}
          </button>
        ))}
      </div>

      {/* List */}
      {visible.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border py-12 text-center">
          <p className="text-[13px] text-muted-foreground">{t('preview.noTasks')}</p>
          <p className="mt-1 text-[12px] text-faint-foreground">
            {todoFilter === 'completed'
              ? t('preview.emptyCompleted')
              : t('preview.emptyDefault')}
          </p>
        </div>
      ) : (
        <ul className="space-y-2">
          {visible.map((task) => (
            <li
              key={task.id}
              className="flex items-center gap-3 rounded-lg border border-border bg-card px-3.5 py-3"
            >
              <button
                type="button"
                onClick={() => toggleTodo(task.id)}
                className={cn(
                  'flex h-5 w-5 shrink-0 items-center justify-center rounded-md border transition-colors',
                  task.done
                    ? 'border-success bg-success/20 text-success'
                    : 'border-border-strong text-transparent hover:border-primary',
                )}
                aria-label={task.done ? t('preview.markIncomplete') : t('preview.markComplete')}
                aria-pressed={task.done}
              >
                <Check className="h-3.5 w-3.5" />
              </button>
              <div className="min-w-0 flex-1">
                <p
                  className={cn(
                    'truncate text-[13px]',
                    task.done && 'text-faint-foreground line-through',
                  )}
                >
                  {task.title}
                </p>
                <p className="text-[11px] text-faint-foreground">{task.when}</p>
              </div>
              <PriorityBadge priority={task.priority} />
            </li>
          ))}
        </ul>
      )}

      <footer className="mt-8 flex items-center justify-center gap-1.5 text-[11px] text-faint-foreground">
        <span className="flex h-4 w-4 items-center justify-center rounded bg-primary/20 text-[8px] font-bold text-primary-bright">
          W
        </span>
        {t('preview.madeIn')}
      </footer>
    </div>
  )
}

function Stat({
  value,
  label,
  className,
}: {
  value: number
  label: string
  className?: string
}) {
  return (
    <div className="flex items-center gap-1">
      <span className={cn('font-semibold', className)}>{value}</span>
      <span className="text-faint-foreground">{label}</span>
    </div>
  )
}

function PriorityBadge({ priority }: { priority: TodoTask['priority'] }) {
  const { t } = useI18n()
  const map = {
    High: 'text-destructive bg-red-500/10 border-red-500/30',
    Med: 'text-warning bg-amber-400/10 border-amber-400/30',
    Low: 'text-muted-foreground bg-white/5 border-border',
  } as const
  return (
    <span
      className={cn(
        'shrink-0 rounded-md border px-2 py-0.5 text-[11px] font-medium',
        map[priority],
      )}
    >
      {t(priorityLabelKey[priority])}
    </span>
  )
}

const filterLabelKey: Record<'all' | 'active' | 'completed', MessageKey> = {
  all: 'preview.filter.all',
  active: 'preview.filter.active',
  completed: 'preview.filter.completed',
}

const priorityLabelKey: Record<TodoTask['priority'], MessageKey> = {
  Low: 'preview.priority.Low',
  Med: 'preview.priority.Med',
  High: 'preview.priority.High',
}
