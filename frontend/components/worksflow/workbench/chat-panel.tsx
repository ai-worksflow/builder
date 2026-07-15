'use client'

import { useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { PromptComposer } from './prompt-composer'
import {
  BlueprintContextCard,
  LinkedDocsCard,
  ResponseActions,
  TaskChecklist,
  VersionCard,
} from './chat-blocks'
import { Check, ChevronsLeft, ChevronsRight, FileSearch, Loader2, RotateCcw, Sparkles } from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'

export function ChatPanel() {
  const { phase, versions, followUps, toggleVersionStar, startBuild, resetWorkbench } =
    useWorksflow()
  const { session, can, authorize } = useCollaboration()
  const { t } = useI18n()
  const [collapsed, setCollapsed] = useState(false)

  return (
    <aside
      className={cn(
        'flex shrink-0 flex-col border-r border-border bg-panel transition-[width,height] duration-200 max-lg:w-full max-lg:flex-none max-lg:border-b max-lg:border-r-0',
        collapsed
          ? 'w-[56px] max-lg:h-[52px]'
          : 'w-[448px] max-xl:w-[392px] max-lg:h-[360px] max-sm:h-[350px]',
      )}
    >
      <div
        className={cn(
          'flex h-10 shrink-0 items-center border-b border-border px-3',
          collapsed ? 'justify-center max-lg:justify-between' : 'justify-between',
        )}
      >
        {!collapsed && (
          <div className="flex min-w-0 items-center gap-2 text-[12px] font-semibold text-foreground">
            <Sparkles className="h-3.5 w-3.5 text-primary-bright" />
            <span className="truncate">{t('chat.panelTitle')}</span>
          </div>
        )}
        {collapsed && (
          <Sparkles className="hidden h-3.5 w-3.5 text-primary-bright max-lg:block" />
        )}
        <button
          type="button"
          onClick={() => setCollapsed((value) => !value)}
          className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-white/5 hover:text-foreground"
          aria-label={collapsed ? t('common.expandSidebar') : t('common.collapseSidebar')}
          aria-expanded={!collapsed}
          title={collapsed ? t('common.expandSidebar') : t('common.collapseSidebar')}
        >
          {collapsed ? <ChevronsRight className="h-4 w-4" /> : <ChevronsLeft className="h-4 w-4" />}
        </button>
      </div>

      {collapsed ? (
        <button
          type="button"
          onClick={() => setCollapsed(false)}
          className="flex min-h-0 flex-1 flex-col items-center gap-2 px-2 py-3 text-faint-foreground hover:bg-white/[0.03] hover:text-foreground max-lg:hidden"
          aria-label={t('common.expandSidebar')}
          title={t('common.expandSidebar')}
        >
          <Sparkles className="h-4 w-4 text-primary-bright" />
          <span className="[writing-mode:vertical-rl] text-[11px] font-medium tracking-wide">
            {t('chat.panelTitle')}
          </span>
        </button>
      ) : (
        <>
          <div className="flex-1 space-y-4 overflow-y-auto scrollbar-thin p-4">
        {/* User prompt bubble */}
        <UserPrompt />

        {/* Assistant intro */}
        <AssistantHeader />

        <BlueprintContextCard />

        {phase === 'planning' && <PlanningState />}

        {(phase === 'planReady' ||
          phase === 'building' ||
          phase === 'complete' ||
          phase === 'error') && <PlanBlock />}

        {phase === 'planReady' && (
          <button
            type="button"
            onClick={async () => {
              if (session.signedIn && (await authorize('edit'))) startBuild()
            }}
            disabled={!session.signedIn || !can('edit')}
            className="flex w-full items-center justify-center gap-2 rounded-lg bg-primary py-2.5 text-[13px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:cursor-not-allowed disabled:opacity-50"
          >
            <Sparkles className="h-4 w-4" />
            {t('chat.implementPlan')}
          </button>
        )}

        {phase === 'planReady' && (
          <VersionCard
            version={versions[0]}
            onToggleStar={(versionId) => {
              void authorize('edit').then((allowed) => allowed && toggleVersionStar(versionId))
            }}
          />
        )}

        {(phase === 'building' || phase === 'complete' || phase === 'error') && (
          <BuildBlock />
        )}

        {followUps.length > 0 && <FollowUpList />}

        {phase === 'complete' && (
          <>
            <LinkedDocsCard />
            <div className="space-y-2">
              {versions.map((v) => (
                <VersionCard
                  key={v.id}
                  version={v}
                  onToggleStar={(versionId) => {
                    void authorize('edit').then((allowed) => allowed && toggleVersionStar(versionId))
                  }}
                />
              ))}
            </div>
            <button
              type="button"
              onClick={() => void authorize('edit').then((allowed) => allowed && resetWorkbench())}
              disabled={!session.signedIn || !can('edit')}
              className="flex w-full items-center justify-center gap-2 rounded-md border border-border py-2 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
            >
              <RotateCcw className="h-3.5 w-3.5" />
              {t('chat.replayDemo')}
            </button>
          </>
        )}
          </div>

          <div className="border-t border-border p-3">
            <PromptComposer />
          </div>
        </>
      )}
    </aside>
  )
}

function FollowUpList() {
  const { followUps } = useWorksflow()
  const { formatDate, t } = useI18n()
  return (
    <div className="space-y-2 rounded-lg border border-border bg-card p-3">
      <div className="text-[12px] font-medium text-foreground">{t('chat.currentIterationQueue')}</div>
      {followUps.slice(-3).map((item) => (
        <div key={item.id} className="rounded-md bg-white/5 px-2.5 py-2">
          <div className="flex items-center justify-between gap-2">
            <span className="text-[11px] font-medium text-primary-bright">
              {item.mode === 'plan' ? t('chat.planFirst') : t('chat.buildDirectly')}
            </span>
            <span className="text-[10px] text-faint-foreground">{formatDate(item.createdAt, { timeStyle: 'short' })}</span>
          </div>
          <p className="mt-1 text-[12px] leading-relaxed text-muted-foreground">{item.text}</p>
        </div>
      ))}
    </div>
  )
}

function UserPrompt() {
  const { t } = useI18n()
  return (
    <div className="flex justify-end">
      <div className="max-w-[85%] rounded-lg rounded-tr-sm bg-secondary px-3.5 py-2.5 text-[13px] leading-relaxed text-foreground">
        {t('chat.demo.userPrompt')}
      </div>
    </div>
  )
}

function AssistantHeader() {
  return (
    <div className="flex items-center gap-2">
      <span className="flex h-6 w-6 items-center justify-center rounded-md bg-gradient-to-br from-primary to-primary-bright text-[10px] font-bold text-white">
        W
      </span>
      <span className="text-[13px] font-semibold text-foreground">Worksflow</span>
    </div>
  )
}

function PlanningState() {
  const { workspace, generationEvents } = useWorksflow()
  const { t } = useI18n()
  const latestLog = [...generationEvents].reverse().find((event) => event.type === 'log')
  return (
    <div className="space-y-3">
      <p className="text-[13px] leading-relaxed text-muted-foreground">
        {t('chat.demo.planningIntro')}
      </p>
      <div className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2 text-[12px] text-muted-foreground">
        <FileSearch className="h-4 w-4 text-primary-bright" />
        <span className="font-mono">
          {t('chat.filesRead', { read: workspace.files.length, total: workspace.files.length })}
        </span>
        <Loader2 className="ml-auto h-3.5 w-3.5 animate-spin text-primary-bright" />
      </div>
      <div className="flex items-center gap-2 text-[13px] font-medium text-primary-bright">
        <Loader2 className="h-4 w-4 animate-spin" />
        {t('chat.planning')}
      </div>
      {latestLog?.type === 'log' && (
        <p className="rounded-md bg-black/20 px-2.5 py-2 font-mono text-[10px] leading-relaxed text-faint-foreground">
          {latestLog.message}
        </p>
      )}
    </div>
  )
}

function PlanBlock() {
  const { generationPlan, workspace } = useWorksflow()
  const { t } = useI18n()
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-1.5 text-[11px] text-muted-foreground">
        <Check className="h-3.5 w-3.5 text-success" />
        <span className="font-mono">
          {t('chat.filesRead', { read: workspace.files.length, total: workspace.files.length })}
        </span>
      </div>

      <h3 className="text-[15px] font-semibold text-foreground text-balance">
        {generationPlan?.title ?? t('chat.demo.planTitle')}
      </h3>

      <ol className="space-y-3">
        {(generationPlan
          ? generationPlan.tasks.map((task) => ({
              title: task.title,
              items: [task.description],
            }))
          : demoPlanGroups(t)
        ).map((group, i) => (
          <li key={group.title}>
            <div className="flex items-center gap-2">
              <span className="flex h-5 w-5 items-center justify-center rounded-full bg-white/5 text-[11px] font-semibold text-muted-foreground">
                {i + 1}
              </span>
              <span className="text-[13px] font-medium text-foreground">{group.title}</span>
            </div>
            <ul className="ml-7 mt-1 space-y-1">
              {group.items.map((item) => (
                <li
                  key={item}
                  className="relative pl-3 text-[12px] leading-relaxed text-muted-foreground before:absolute before:left-0 before:top-2 before:h-1 before:w-1 before:rounded-full before:bg-faint-foreground"
                >
                  {item}
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ol>

      <p className="text-[12px] leading-relaxed text-muted-foreground">
        {generationPlan?.summary ?? t('chat.demo.planSummary')}
      </p>
      <ResponseActions />
    </div>
  )
}

function BuildBlock() {
  const { session, can, authorize } = useCollaboration()
  const {
    phase,
    tasks,
    generationSummary,
    generationPlan,
    generationError,
    generationErrorCode,
    generationErrorStatus,
    generationErrorRetryable,
    generationErrorAction,
    generationErrorRetryAfterSeconds,
    generationEvents,
    generationLifecycleEvents,
    generationProvider,
    generationModel,
    generationUsage,
    generationDurationMs,
    generationCost,
    generationLimits,
    retryGeneration,
  } = useWorksflow()
  const { formatNumber, t } = useI18n()
  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-lg rounded-tr-sm bg-secondary px-3.5 py-2.5 text-[13px] leading-relaxed text-foreground">
          {t('chat.demo.startImplementation')}
        </div>
      </div>

      <AssistantHeader />
      <p className="text-[13px] leading-relaxed text-muted-foreground">
        {t('chat.demo.buildIntro')}
      </p>

      <div className="rounded-lg border border-border bg-card p-3">
        <div className="mb-2 flex items-center justify-between">
          <span className="text-[12px] font-medium text-foreground">{t('chat.tasks')}</span>
          <span
            className={cn(
              'text-[11px] font-medium',
              phase === 'complete'
                ? 'text-success'
                : phase === 'error'
                  ? 'text-destructive'
                  : 'text-primary-bright',
            )}
          >
            {phase === 'complete'
              ? t('chat.planCompleted')
              : phase === 'error'
                ? t('chat.generationStopped')
                : t('chat.building')}
          </span>
        </div>
        <TaskChecklist tasks={tasks} />
      </div>

      {(generationEvents.some((event) => event.type === 'log') || generationLifecycleEvents.length > 0) && (
        <div className="max-h-28 space-y-1 overflow-y-auto rounded-md border border-border bg-black/20 p-2 font-mono text-[10px] text-faint-foreground scrollbar-thin">
          {generationLifecycleEvents.slice(-2).map((event) => (
            <div key={`lifecycle-${event.sequence}`}>
              <span className="text-primary-bright">[lifecycle]</span>{' '}{event.status}
            </div>
          ))}
          {generationEvents
            .filter((event) => event.type === 'log')
            .slice(-6)
            .map((event) =>
              event.type === 'log' ? (
                <div key={event.sequence}>
                  <span className="text-primary-bright">[{generationProvider ?? event.provider ?? 'run'}]</span>{' '}
                  {event.message}
                </div>
              ) : null,
            )}
        </div>
      )}

      {(generationProvider || generationDurationMs > 0) && (
        <div className="grid grid-cols-3 gap-1.5 sm:grid-cols-6">
          {[
            [t('chat.runProvider'), generationProvider ?? '—'],
            [t('chat.runModel'), generationProvider === 'local' ? t('chat.localProvider') : generationModel],
            [
              t('chat.runTokens'),
              generationUsage
                ? `${formatNumber(generationUsage.totalTokens)}${generationUsage.estimated ? '~' : ''}`
                : '—',
            ],
            [
              t('chat.runDuration'),
              generationDurationMs ? t('chat.seconds', { count: formatNumber(generationDurationMs / 1000, { maximumFractionDigits: 1 }) }) : '—',
            ],
            [
              t('chat.runCost'),
              generationCost
                ? `${generationCost.estimated ? '~' : ''}$${generationCost.amount.toFixed(4)}`
                : t('chat.notConfigured'),
            ],
            [
              t('chat.runLimit'),
              generationLimits?.maxTotalTokens
                ? formatNumber(generationLimits.maxTotalTokens)
                : t('chat.notConfigured'),
            ],
          ].map(([label, value]) => (
            <div key={label} className="min-w-0 rounded-md border border-border bg-card px-2 py-1.5">
              <div className="truncate text-[9px] uppercase tracking-wide text-faint-foreground">
                {label}
              </div>
              <div className="mt-0.5 truncate text-[10px] font-medium text-muted-foreground">
                {value}
              </div>
            </div>
          ))}
        </div>
      )}

      {phase === 'error' && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-3">
          <p className="text-[12px] leading-relaxed text-destructive">
            {generationError ?? t('preview.errorCopy')}
          </p>
          <p className="mt-1 text-[10px] leading-relaxed text-muted-foreground">
            {generationErrorAction ?? generationRecoveryHint(generationErrorCode, generationErrorStatus, t)}
            {generationErrorRetryAfterSeconds !== undefined
              ? ` ${t('chat.retryAfter', { seconds: generationErrorRetryAfterSeconds })}`
              : ''}
          </p>
          {generationErrorRetryable && (
            <button
              type="button"
              onClick={() => void authorize('edit').then((allowed) => allowed && retryGeneration())}
              disabled={!session.signedIn || !can('edit')}
              className="mt-2 inline-flex items-center gap-1.5 rounded-md border border-red-400/30 px-2.5 py-1.5 text-[11px] font-medium text-foreground hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40"
            >
              <RotateCcw className="h-3.5 w-3.5" />
              {t('common.retry')}
            </button>
          )}
        </div>
      )}

      {phase === 'complete' && (
        <div className="space-y-3">
          <p className="text-[13px] leading-relaxed text-foreground">
            {generationSummary || t('chat.demo.buildSummary')}
          </p>
          <div>
            <p className="mb-1.5 text-[12px] font-medium text-foreground">{t('chat.whatWasBuilt')}</p>
            <ul className="space-y-1">
              {(generationPlan?.tasks.map((task) => task.title) ?? demoBuiltItems(t)).map((item) => (
                <li key={item} className="flex items-center gap-2 text-[12px] text-muted-foreground">
                  <Check className="h-3.5 w-3.5 text-success" />
                  {item}
                </li>
              ))}
            </ul>
          </div>
          <p className="text-[12px] text-faint-foreground">
            {t('chat.continueIterating')}
          </p>
          <ResponseActions />
        </div>
      )}
    </div>
  )
}

function demoPlanGroups(t: ReturnType<typeof useI18n>['t']) {
  return [
    {
      title: t('chat.demo.plan.1.title'),
      items: [
        t('chat.demo.plan.1.item1'),
        t('chat.demo.plan.1.item2'),
        t('chat.demo.plan.1.item3'),
      ],
    },
    {
      title: t('chat.demo.plan.2.title'),
      items: [t('chat.demo.plan.2.item1'), t('chat.demo.plan.2.item2')],
    },
    {
      title: t('chat.demo.plan.3.title'),
      items: [t('chat.demo.plan.3.item1'), t('chat.demo.plan.3.item2')],
    },
    {
      title: t('chat.demo.plan.4.title'),
      items: [t('chat.demo.plan.4.item1'), t('chat.demo.plan.4.item2')],
    },
    {
      title: t('chat.demo.plan.5.title'),
      items: [t('chat.demo.plan.5.item1'), t('chat.demo.plan.5.item2')],
    },
  ]
}

function demoBuiltItems(t: ReturnType<typeof useI18n>['t']) {
  return [
    t('chat.demo.built.topNav'),
    t('chat.demo.built.taskInput'),
    t('chat.demo.built.taskList'),
    t('chat.demo.built.filters'),
    t('chat.demo.built.emptyStates'),
  ]
}

function generationRecoveryHint(
  code: string | null,
  status: number | undefined,
  t: ReturnType<typeof useI18n>['t'],
) {
  if (status === 429 || code?.includes('rate')) return t('chat.recovery.rateLimit')
  if (code?.includes('context') || code?.includes('too_large')) return t('chat.recovery.contextLimit')
  if (code?.includes('quota') || code?.includes('billing')) return t('chat.recovery.quota')
  if (code?.includes('unreachable') || status === undefined) return t('chat.recovery.network')
  if (status && status >= 500) return t('chat.recovery.provider')
  return t('chat.recovery.adjust')
}
