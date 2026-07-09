'use client'

import { useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import {
  BUILD_SUMMARY,
  PLAN_GROUPS,
  PLAN_SUMMARY,
  PLAN_TITLE,
  USER_PROMPT,
  WHAT_WAS_BUILT,
} from '@/lib/worksflow/mock-data'
import { PromptComposer } from './prompt-composer'
import {
  BlueprintContextCard,
  LinkedDocsCard,
  ResponseActions,
  TaskChecklist,
  VersionCard,
} from './chat-blocks'
import { Check, ChevronsLeft, ChevronsRight, FileSearch, Loader2, RotateCcw, Sparkles } from 'lucide-react'

export function ChatPanel() {
  const { phase, versions, followUps, toggleVersionStar, startBuild, resetWorkbench } =
    useWorksflow()
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
            onClick={startBuild}
            className="flex w-full items-center justify-center gap-2 rounded-lg bg-primary py-2.5 text-[13px] font-semibold text-primary-foreground hover:bg-primary-bright"
          >
            <Sparkles className="h-4 w-4" />
            {t('chat.implementPlan')}
          </button>
        )}

        {phase === 'planReady' && (
          <VersionCard version={versions[0]} onToggleStar={toggleVersionStar} />
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
                <VersionCard key={v.id} version={v} onToggleStar={toggleVersionStar} />
              ))}
            </div>
            <button
              type="button"
              onClick={resetWorkbench}
              className="flex w-full items-center justify-center gap-2 rounded-md border border-border py-2 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
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
  const { t } = useI18n()
  return (
    <div className="space-y-2 rounded-lg border border-border bg-card p-3">
      <div className="text-[12px] font-medium text-foreground">{t('chat.currentIterationQueue')}</div>
      {followUps.slice(-3).map((item) => (
        <div key={item.id} className="rounded-md bg-white/5 px-2.5 py-2">
          <div className="flex items-center justify-between gap-2">
            <span className="text-[11px] font-medium text-primary-bright">
              {item.mode === 'plan' ? t('chat.planFirst') : t('chat.buildDirectly')}
            </span>
            <span className="text-[10px] text-faint-foreground">{item.createdAt}</span>
          </div>
          <p className="mt-1 text-[12px] leading-relaxed text-muted-foreground">{item.text}</p>
        </div>
      ))}
    </div>
  )
}

function UserPrompt() {
  return (
    <div className="flex justify-end">
      <div className="max-w-[85%] rounded-lg rounded-tr-sm bg-secondary px-3.5 py-2.5 text-[13px] leading-relaxed text-foreground">
        {USER_PROMPT}
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
  const { t } = useI18n()
  return (
    <div className="space-y-3">
      <p className="text-[13px] leading-relaxed text-muted-foreground">
        I&apos;ll start by reading the current project files to understand the setup before
        creating a plan.
      </p>
      <div className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2 text-[12px] text-muted-foreground">
        <FileSearch className="h-4 w-4 text-primary-bright" />
        <span className="font-mono">{t('chat.filesRead', { read: 0, total: 9 })}</span>
        <Loader2 className="ml-auto h-3.5 w-3.5 animate-spin text-primary-bright" />
      </div>
      <div className="flex items-center gap-2 text-[13px] font-medium text-primary-bright">
        <Loader2 className="h-4 w-4 animate-spin" />
        {t('chat.planning')}
      </div>
    </div>
  )
}

function PlanBlock() {
  const { t } = useI18n()
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-1.5 text-[11px] text-muted-foreground">
        <Check className="h-3.5 w-3.5 text-success" />
        <span className="font-mono">{t('chat.filesRead', { read: 9, total: 9 })}</span>
      </div>

      <h3 className="text-[15px] font-semibold text-foreground text-balance">{PLAN_TITLE}</h3>

      <ol className="space-y-3">
        {PLAN_GROUPS.map((group, i) => (
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

      <p className="text-[12px] leading-relaxed text-muted-foreground">{PLAN_SUMMARY}</p>
      <ResponseActions />
    </div>
  )
}

function BuildBlock() {
  const { phase, tasks } = useWorksflow()
  const { t } = useI18n()
  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-lg rounded-tr-sm bg-secondary px-3.5 py-2.5 text-[13px] leading-relaxed text-foreground">
          Perfect, you can start implementing this plan!
        </div>
      </div>

      <AssistantHeader />
      <p className="text-[13px] leading-relaxed text-muted-foreground">
        I&apos;ll implement the plan now. Let me set up task tracking and start building.
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

      {phase === 'complete' && (
        <div className="space-y-3">
          <p className="text-[13px] leading-relaxed text-foreground">{BUILD_SUMMARY}</p>
          <div>
            <p className="mb-1.5 text-[12px] font-medium text-foreground">{t('chat.whatWasBuilt')}</p>
            <ul className="space-y-1">
              {WHAT_WAS_BUILT.map((item) => (
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
