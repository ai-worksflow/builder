'use client'

import { useMemo, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import {
  searchPromptHistory,
  searchPromptTemplates,
  searchPromptWorkflows,
  workflowPrompt,
  type PromptHistoryEntry,
  type PromptTemplate,
  type PromptWorkflow,
} from '@/lib/worksflow/prompt-library'
import { useDropdown } from '../use-dropdown'
import { BookOpen, Clock3, Plus, Search, Star, Trash2, Workflow, X } from 'lucide-react'

type LibraryTab = 'templates' | 'workflows' | 'history'

export function PromptLibraryMenu({
  value,
  history,
  templates,
  workflows,
  onSelect,
  onSave,
  onDelete,
  onSaveWorkflow,
  onDeleteWorkflow,
}: {
  value: string
  history: PromptHistoryEntry[]
  templates: PromptTemplate[]
  workflows: PromptWorkflow[]
  onSelect: (prompt: string, mode: PromptTemplate['mode']) => void
  onSave: (template: PromptTemplate) => void
  onDelete: (id: string) => void
  onSaveWorkflow: (workflow: PromptWorkflow) => void
  onDeleteWorkflow: (id: string) => void
}) {
  const { t } = useI18n()
  const dropdown = useDropdown()
  const [tab, setTab] = useState<LibraryTab>('templates')
  const [query, setQuery] = useState('')
  const [workflowTitle, setWorkflowTitle] = useState('')
  const [stepTitle, setStepTitle] = useState('')
  const [stepPrompt, setStepPrompt] = useState('')
  const [stepMode, setStepMode] = useState<PromptWorkflow['steps'][number]['mode']>('build')
  const [workflowSteps, setWorkflowSteps] = useState<PromptWorkflow['steps']>([])
  const filteredTemplates = useMemo(
    () => searchPromptTemplates(templates, query),
    [query, templates],
  )
  const filteredHistory = useMemo(
    () => searchPromptHistory(history, query),
    [history, query],
  )
  const filteredWorkflows = useMemo(
    () => searchPromptWorkflows(workflows, query),
    [query, workflows],
  )

  function addWorkflowStep() {
    const prompt = stepPrompt.trim() || value.trim()
    if (!prompt) return
    const index = workflowSteps.length + 1
    setWorkflowSteps((current) => [
      ...current,
      {
        id: `step-${Date.now()}-${index}`,
        title: stepTitle.trim() || `Step ${index}`,
        prompt,
        mode: stepMode,
      },
    ])
    setStepTitle('')
    setStepPrompt('')
  }

  function saveWorkflow() {
    if (!workflowTitle.trim() || workflowSteps.length === 0) return
    const title = workflowTitle.trim()
    onSaveWorkflow({
      id: `workflow-${Date.now()}`,
      title,
      description: `${workflowSteps.length}-step custom workflow`,
      tags: ['custom', 'workflow'],
      steps: workflowSteps,
    })
    setWorkflowTitle('')
    setWorkflowSteps([])
    setQuery(title)
  }

  function saveCurrentPrompt() {
    const prompt = value.trim()
    if (!prompt) return
    const title = prompt.length > 48 ? `${prompt.slice(0, 48)}…` : prompt
    onSave({
      id: `template-${Date.now()}`,
      title,
      description: t('composer.library.savedDescription'),
      prompt,
      tags: ['custom'],
      mode: 'iterate',
    })
    setTab('templates')
    setQuery(title)
  }

  return (
    <div ref={dropdown.ref} className="relative">
      <button
        type="button"
        onClick={() => dropdown.setOpen((open) => !open)}
        className={cn(
          'flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-white/5 hover:text-foreground',
          dropdown.open && 'bg-white/5 text-foreground',
        )}
        aria-label={t('composer.library.title')}
        aria-haspopup="dialog"
        aria-expanded={dropdown.open}
        title={t('composer.library.title')}
      >
        <BookOpen className="h-4 w-4" />
      </button>

      {dropdown.open && (
        <div
          role="dialog"
          aria-label={t('composer.library.title')}
          className="absolute bottom-[calc(100%+8px)] left-0 z-50 w-[390px] overflow-hidden rounded-lg border border-border-strong bg-popover shadow-2xl shadow-black/50 max-sm:fixed max-sm:inset-x-3 max-sm:bottom-20 max-sm:w-auto"
        >
          <div className="flex items-center justify-between border-b border-border px-3 py-2.5">
            <div>
              <div className="text-[12px] font-semibold text-foreground">{t('composer.library.title')}</div>
              <div className="text-[10px] text-faint-foreground">{t('composer.library.hint')}</div>
            </div>
            <button
              type="button"
              onClick={() => dropdown.setOpen(false)}
              className="rounded p-1 text-faint-foreground hover:bg-white/5 hover:text-foreground"
              aria-label={t('common.close')}
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>

          <div className="flex gap-1 border-b border-border p-1">
            <TabButton active={tab === 'templates'} onClick={() => setTab('templates')}>
              <Star className="h-3 w-3" />
              {t('composer.library.templates')}
            </TabButton>
            <TabButton active={tab === 'history'} onClick={() => setTab('history')}>
              <Clock3 className="h-3 w-3" />
              {t('composer.library.history')}
            </TabButton>
            <TabButton active={tab === 'workflows'} onClick={() => setTab('workflows')}>
              <Workflow className="h-3 w-3" />
              Workflows
            </TabButton>
          </div>

          <div className="border-b border-border p-2">
            <label className="flex items-center gap-2 rounded-md border border-border bg-background px-2.5">
              <Search className="h-3.5 w-3.5 text-faint-foreground" />
              <span className="sr-only">{t('common.search')}</span>
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t('composer.library.search')}
                className="h-8 min-w-0 flex-1 bg-transparent text-[11px] text-foreground outline-none placeholder:text-faint-foreground"
              />
            </label>
          </div>

          <div className="max-h-72 overflow-y-auto p-2 scrollbar-thin">
            {tab === 'templates' ? (
              <>
                {value.trim() && (
                  <button
                    type="button"
                    onClick={saveCurrentPrompt}
                    className="mb-2 flex w-full items-center justify-center rounded-md border border-dashed border-primary/40 px-3 py-2 text-[11px] font-medium text-primary-bright hover:bg-primary/10"
                  >
                    {t('composer.library.saveCurrent')}
                  </button>
                )}
                {filteredTemplates.map((template) => (
                  <LibraryRow
                    key={template.id}
                    title={template.title}
                    description={template.description}
                    badge={template.mode}
                    onClick={() => {
                      onSelect(template.prompt, template.mode)
                      dropdown.setOpen(false)
                    }}
                    action={
                      template.builtIn ? null : (
                        <button
                          type="button"
                          onClick={(event) => {
                            event.stopPropagation()
                            onDelete(template.id)
                          }}
                          className="rounded p-1 text-faint-foreground hover:bg-destructive/10 hover:text-destructive"
                          aria-label={t('composer.library.deleteTemplate', { name: template.title })}
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </button>
                      )
                    }
                  />
                ))}
                {filteredTemplates.length === 0 && <Empty>{t('composer.library.emptyTemplates')}</Empty>}
              </>
            ) : tab === 'workflows' ? (
              <>
                <div className="mb-2 space-y-2 rounded-md border border-border bg-card p-2">
                  <div className="text-[10px] font-semibold uppercase tracking-wide text-faint-foreground">Workflow builder</div>
                  <input
                    value={workflowTitle}
                    onChange={(event) => setWorkflowTitle(event.target.value)}
                    placeholder="Workflow title"
                    className="h-8 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none"
                  />
                  <div className="grid grid-cols-[1fr_88px] gap-1.5">
                    <input
                      value={stepTitle}
                      onChange={(event) => setStepTitle(event.target.value)}
                      placeholder={`Step ${workflowSteps.length + 1} title`}
                      className="h-8 rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none"
                    />
                    <select
                      value={stepMode}
                      onChange={(event) => setStepMode(event.target.value as typeof stepMode)}
                      className="h-8 rounded-md border border-border bg-background px-2 text-[10px] text-foreground"
                    >
                      <option value="plan">plan</option><option value="build">build</option><option value="iterate">iterate</option><option value="fix">fix</option>
                    </select>
                  </div>
                  <textarea
                    value={stepPrompt}
                    onChange={(event) => setStepPrompt(event.target.value)}
                    placeholder={value.trim() ? 'Leave blank to use the current composer prompt' : 'Step instructions'}
                    rows={2}
                    className="w-full resize-y rounded-md border border-border bg-background p-2 text-[10px] text-foreground outline-none"
                  />
                  {workflowSteps.length > 0 && (
                    <div className="flex flex-wrap gap-1">
                      {workflowSteps.map((step, index) => (
                        <button key={step.id} type="button" onClick={() => setWorkflowSteps((current) => current.filter((_, itemIndex) => itemIndex !== index))} className="rounded bg-white/5 px-1.5 py-1 text-[9px] text-muted-foreground hover:text-destructive" title="Remove step">
                          {index + 1}. {step.title} · {step.mode} ×
                        </button>
                      ))}
                    </div>
                  )}
                  <div className="flex gap-1.5">
                    <button type="button" onClick={addWorkflowStep} disabled={!stepPrompt.trim() && !value.trim()} className="inline-flex flex-1 items-center justify-center gap-1 rounded-md border border-border px-2 py-1.5 text-[10px] text-muted-foreground disabled:opacity-40"><Plus className="h-3 w-3" />Add step</button>
                    <button type="button" onClick={saveWorkflow} disabled={!workflowTitle.trim() || workflowSteps.length === 0} className="flex-1 rounded-md bg-primary px-2 py-1.5 text-[10px] font-semibold text-primary-foreground disabled:opacity-40">Save workflow</button>
                  </div>
                </div>
                {filteredWorkflows.map((workflow) => (
                  <LibraryRow
                    key={workflow.id}
                    title={workflow.title}
                    description={`${workflow.description} · ${workflow.steps.length} steps`}
                    badge="workflow"
                    onClick={() => {
                      onSelect(workflowPrompt(workflow), workflow.steps.at(-1)?.mode ?? 'build')
                      dropdown.setOpen(false)
                    }}
                    action={workflow.builtIn ? null : (
                      <button type="button" onClick={(event) => { event.stopPropagation(); onDeleteWorkflow(workflow.id) }} className="rounded p-1 text-faint-foreground hover:bg-destructive/10 hover:text-destructive" aria-label={`Delete ${workflow.title}`}><Trash2 className="h-3.5 w-3.5" /></button>
                    )}
                  />
                ))}
                {filteredWorkflows.length === 0 && <Empty>No workflows match this search.</Empty>}
              </>
            ) : (
              <>
                {filteredHistory.map((entry) => (
                  <LibraryRow
                    key={entry.id}
                    title={entry.prompt}
                    description={`${entry.model} · ${new Date(entry.createdAt).toLocaleString()}`}
                    badge={entry.status}
                    onClick={() => {
                      onSelect(entry.prompt, entry.mode)
                      dropdown.setOpen(false)
                    }}
                  />
                ))}
                {filteredHistory.length === 0 && <Empty>{t('composer.library.emptyHistory')}</Empty>}
              </>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function TabButton({
  active,
  children,
  onClick,
}: {
  active: boolean
  children: React.ReactNode
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex flex-1 items-center justify-center gap-1.5 rounded-md px-2 py-1.5 text-[11px] font-medium',
        active ? 'bg-white/5 text-foreground' : 'text-faint-foreground hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}

function LibraryRow({
  title,
  description,
  badge,
  action,
  onClick,
}: {
  title: string
  description: string
  badge: string
  action?: React.ReactNode
  onClick: () => void
}) {
  return (
    <div className="group flex w-full items-start gap-1 rounded-md pr-1 hover:bg-white/5">
      <button
        type="button"
        onClick={onClick}
        className="flex min-w-0 flex-1 items-start gap-2 px-2.5 py-2 text-left"
      >
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[11px] font-medium text-foreground">{title}</span>
          <span className="mt-0.5 block truncate text-[10px] text-faint-foreground">{description}</span>
        </span>
        <span className="rounded bg-white/5 px-1.5 py-0.5 text-[9px] text-muted-foreground">{badge}</span>
      </button>
      {action && <span className="mt-1.5 shrink-0">{action}</span>}
    </div>
  )
}

function Empty({ children }: { children: React.ReactNode }) {
  return <p className="px-3 py-8 text-center text-[11px] text-faint-foreground">{children}</p>
}
