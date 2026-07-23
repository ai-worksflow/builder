'use client'

import { useEffect, useMemo, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { listGenerationModels } from '@/lib/generation/client'
import { attachmentId, type ComposerAttachment } from '@/lib/worksflow/composer-context'
import {
  applySlashCommand,
  localizeSlashCommands,
  suggestSlashCommands,
  type SlashCommand,
} from '@/lib/worksflow/prompt-library'
import { ContextManager } from './context-manager'
import { PromptLibraryMenu } from './prompt-library-menu'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useLocalizedLabels } from '../use-localized-labels'
import { ArrowUp, AtSign, ChevronDown, Command, Sparkles, Square, Workflow } from 'lucide-react'

type ComposerSuggestion =
  | { id: string; kind: 'command'; command: SlashCommand }
  | {
      id: string
      kind: 'mention'
      attachment: ComposerAttachment
      description: string
    }

export function PromptComposer() {
  const {
    session: collaborationSession,
    can: canCollaborate,
    authorize: authorizeCollaboration,
  } = useCollaboration()
  const {
    phase,
    selectedProductProjectId,
    isGenerating,
    planMode,
    setPlanMode,
    stopBuild,
    submitPrompt,
    composerDraft,
    setComposerDraft,
    activeBlueprintContext,
    workspace,
    documents,
    attachments,
    addAttachment,
    removeAttachment,
    toggleAttachmentIncluded,
    generationModel,
    setGenerationModel,
    generationMode,
    setGenerationMode,
    promptHistory,
    promptTemplates,
    promptWorkflows,
    savePromptTemplate,
    deletePromptTemplate,
    savePromptWorkflow,
    deletePromptWorkflow,
  } = useWorksflow()
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const [value, setValue] = useState('')
  const [activeSuggestion, setActiveSuggestion] = useState(0)
  const [dismissedValue, setDismissedValue] = useState<string | null>(null)
  const [availableModels, setAvailableModels] = useState<string[]>([generationModel])
  const [modelListError, setModelListError] = useState<string | null>(null)
  const canViewProject = collaborationSession.signedIn && canCollaborate('view')

  useEffect(() => {
    if (composerDraft) setValue(composerDraft)
  }, [composerDraft])

  useEffect(() => {
    if (!canViewProject) return
    let active = true
    void listGenerationModels(selectedProductProjectId)
      .then((result) => {
        if (!active) return
        setAvailableModels(result.models)
        setModelListError(null)
        if (!result.models.includes(generationModel)) setGenerationModel(result.defaultModel)
      })
      .catch((cause) => {
        if (!active) return
        setModelListError(cause instanceof Error ? cause.message : t('composer.modelsLoadFailed'))
      })
    return () => {
      active = false
    }
  }, [canViewProject, generationModel, selectedProductProjectId, setGenerationModel, t])

  const placeholder =
    phase === 'planning'
      ? t('composer.placeholder.plan')
      : t('composer.placeholder.default')

  const suggestions = useMemo<ComposerSuggestion[]>(() => {
    const commands = suggestSlashCommands(value, localizeSlashCommands(t))
    if (commands.length > 0) {
      return commands.map((command) => ({ id: command.command, kind: 'command', command }))
    }

    const mentionMatch = value.match(/(?:^|\s)@([^\s@]*)$/)
    if (!mentionMatch) return []
    const query = mentionMatch[1].toLowerCase()
    const sources: ComposerSuggestion[] = [
      ...workspace.files.map((file) => ({
        id: `workspace:${file.path}`,
        kind: 'mention' as const,
        attachment: {
          id: attachmentId('workspace'),
          kind: 'workspace' as const,
          name: file.path,
          content: file.content,
          sourceId: file.path,
        },
        description: `${file.language} · r${file.revision}`,
      })),
      ...documents.map((document) => ({
        id: `document:${document.id}`,
        kind: 'mention' as const,
        attachment: {
          id: attachmentId('document'),
          kind: 'document' as const,
          name: document.title,
          content: [
            document.summary,
            ...document.sections.map((section) => `${section.title}\n${section.body}`),
          ].join('\n\n'),
          sourceId: document.id,
        },
        description: labels.docStatus(document.status),
      })),
    ]
    return sources
      .filter((source) =>
        source.kind === 'mention' &&
        source.attachment.name.toLowerCase().includes(query) &&
        !attachments.some(
          (attachment) =>
            attachment.kind === source.attachment.kind &&
            attachment.sourceId === source.attachment.sourceId,
        ),
      )
      .slice(0, 7)
  }, [attachments, documents, labels, t, value, workspace.files])

  const suggestionsVisible = suggestions.length > 0 && dismissedValue !== value

  function applyMode(mode: SlashCommand['mode']) {
    if (mode === 'plan') {
      setPlanMode(true)
      return
    }
    setPlanMode(false)
    setGenerationMode(mode)
  }

  function selectSuggestion(suggestion: ComposerSuggestion) {
    if (suggestion.kind === 'command') {
      const applied = applySlashCommand(value, suggestion.command)
      setValue(`${applied.prompt} `)
      applyMode(applied.mode)
    } else {
      const mentionStart = value.lastIndexOf('@')
      const prefix = mentionStart >= 0 ? value.slice(0, mentionStart) : value
      setValue(`${prefix}@${suggestion.attachment.name} `)
      addAttachment(suggestion.attachment)
    }
    setActiveSuggestion(0)
    setDismissedValue(null)
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return
    if (suggestionsVisible && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault()
      setActiveSuggestion((current) => {
        const offset = e.key === 'ArrowDown' ? 1 : -1
        return (current + offset + suggestions.length) % suggestions.length
      })
      return
    }
    if (suggestionsVisible && e.key === 'Escape') {
      e.preventDefault()
      setDismissedValue(value)
      return
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      if (suggestionsVisible) {
        selectSuggestion(suggestions[activeSuggestion] ?? suggestions[0])
        return
      }
      void handleSubmit()
    }
  }

  async function handleSubmit() {
    if (!value.trim()) return
    if (!collaborationSession.signedIn || !(await authorizeCollaboration('edit'))) return
    submitPrompt(value)
    setValue('')
    setComposerDraft('')
  }

  return (
    <div className="relative rounded-lg border border-border-strong bg-card p-2.5 focus-within:border-primary/60 focus-within:ring-1 focus-within:ring-primary/40">
      {suggestionsVisible && (
        <div
          id="composer-suggestions"
          role="listbox"
          aria-label={t('composer.suggestions.label')}
          className="absolute inset-x-0 bottom-[calc(100%+8px)] z-40 max-h-64 overflow-y-auto rounded-lg border border-border-strong bg-popover p-1.5 shadow-2xl shadow-black/40 scrollbar-thin"
        >
          <div className="px-2 py-1 text-[9px] font-semibold uppercase tracking-wide text-faint-foreground">
            {suggestions[0]?.kind === 'command'
              ? t('composer.suggestions.commands')
              : t('composer.suggestions.mentions')}
          </div>
          {suggestions.map((suggestion, index) => (
            <button
              key={suggestion.id}
              id={`composer-suggestion-${index}`}
              type="button"
              role="option"
              aria-selected={index === activeSuggestion}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => selectSuggestion(suggestion)}
              className={cn(
                'flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left',
                index === activeSuggestion ? 'bg-primary/15 text-foreground' : 'text-muted-foreground hover:bg-white/5',
              )}
            >
              {suggestion.kind === 'command' ? (
                <Command className="h-3.5 w-3.5 shrink-0 text-primary-bright" />
              ) : (
                <AtSign className="h-3.5 w-3.5 shrink-0 text-primary-bright" />
              )}
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[11px] font-medium text-foreground">
                  {suggestion.kind === 'command'
                    ? `${suggestion.command.command} · ${suggestion.command.title}`
                    : suggestion.attachment.name}
                </span>
                <span className="block truncate text-[10px] text-faint-foreground">
                  {suggestion.kind === 'command'
                    ? suggestion.command.description
                    : suggestion.description}
                </span>
              </span>
            </button>
          ))}
        </div>
      )}
      <textarea
        value={value}
        onChange={(e) => {
          setValue(e.target.value)
          setActiveSuggestion(0)
          setDismissedValue(null)
        }}
        onKeyDown={handleKeyDown}
        placeholder={placeholder}
        rows={2}
        className="h-[52px] w-full resize-none bg-transparent px-1 text-[13px] leading-relaxed text-foreground placeholder:text-faint-foreground focus:outline-none"
        aria-label={t('composer.placeholder.default')}
        aria-controls={suggestionsVisible ? 'composer-suggestions' : undefined}
        aria-activedescendant={
          suggestionsVisible ? `composer-suggestion-${activeSuggestion}` : undefined
        }
        aria-autocomplete="list"
        readOnly={collaborationSession.signedIn && !canCollaborate('edit')}
      />

      {!collaborationSession.signedIn && (
        <p className="mb-1 px-1 text-[10px] text-warning">{t('composer.signInRequired')}</p>
      )}

      <div className="flex flex-wrap items-center gap-1.5">
        <ContextManager
          attachments={attachments}
          workspaceFiles={workspace.files.map((file) => ({
            id: file.path,
            name: file.path,
            description: `${file.language} · r${file.revision}`,
            content: file.content,
          }))}
          documents={documents.map((document) => ({
            id: document.id,
            name: document.title,
            description: document.status,
            content: [
              document.summary,
              ...document.sections.map((section) => `${section.title}\n${section.body}`),
            ].join('\n\n'),
          }))}
          onAdd={addAttachment}
          onRemove={removeAttachment}
          onToggle={toggleAttachmentIncluded}
        />

        <PromptLibraryMenu
          value={value}
          history={promptHistory}
          templates={promptTemplates}
          workflows={promptWorkflows}
          onSelect={(prompt, mode) => {
            setValue(prompt)
            applyMode(mode)
          }}
          onSave={savePromptTemplate}
          onDelete={deletePromptTemplate}
          onSaveWorkflow={savePromptWorkflow}
          onDeleteWorkflow={deletePromptWorkflow}
        />

        <label className="flex items-center gap-1 rounded-md px-1.5 text-xs text-muted-foreground hover:bg-white/5">
          <Sparkles className="h-3.5 w-3.5" />
          <select
            value={generationModel}
            onChange={(event) => setGenerationModel(event.target.value)}
            className="h-8 max-w-[110px] appearance-none bg-transparent pr-4 text-xs font-medium text-muted-foreground outline-none"
            aria-label={t('composer.model')}
          >
            {[...new Set([generationModel, ...availableModels])].map((model) => (
              <option key={model} value={model}>{model}</option>
            ))}
          </select>
          <ChevronDown className="-ml-4 h-3 w-3 pointer-events-none opacity-60" />
        </label>
        {modelListError && (
          <span className="max-w-48 truncate text-[9px] text-warning" title={modelListError}>
            Approved model list unavailable
          </span>
        )}

        <label className="flex items-center gap-1 rounded-md px-1.5 text-xs text-muted-foreground hover:bg-white/5">
          <select
            value={generationMode}
            onChange={(event) =>
              setGenerationMode(event.target.value as typeof generationMode)
            }
            disabled={planMode}
            className="h-8 max-w-[92px] appearance-none bg-transparent pr-4 text-xs font-medium text-muted-foreground outline-none disabled:opacity-50"
            aria-label={t('composer.mode')}
          >
            <option value="build">{t('composer.mode.build')}</option>
            <option value="iterate">{t('composer.mode.iterate')}</option>
            <option value="fix">{t('composer.mode.fix')}</option>
          </select>
          <ChevronDown className="-ml-4 h-3 w-3 pointer-events-none opacity-60" />
        </label>

        {activeBlueprintContext && (
          <ComposerChip>
            <Workflow className="h-3.5 w-3.5 text-primary-bright" />
            Blueprint
          </ComposerChip>
        )}

        <button
          type="button"
          onClick={() => setPlanMode(!planMode)}
          disabled={collaborationSession.signedIn && !canCollaborate('edit')}
          className={cn(
            'flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors',
            planMode
              ? 'bg-primary/15 text-primary-bright ring-1 ring-primary/40'
              : 'text-muted-foreground hover:bg-white/5 hover:text-foreground',
          )}
          aria-pressed={planMode}
        >
          {t('composer.plan')}
        </button>

        <div className="ml-auto max-sm:ml-0">
          {isGenerating ? (
            <button
              type="button"
              onClick={stopBuild}
              className="flex h-8 w-8 items-center justify-center rounded-md bg-secondary text-foreground hover:bg-white/10"
              aria-label={t('composer.stop')}
              title={t('composer.stop')}
            >
              <Square className="h-3.5 w-3.5 fill-current" />
            </button>
          ) : (
            <button
              type="button"
              onClick={() => void handleSubmit()}
              disabled={
                value.trim().length === 0 ||
                !collaborationSession.signedIn ||
                !canCollaborate('edit')
              }
              className="flex h-8 w-8 items-center justify-center rounded-md bg-primary text-primary-foreground transition-colors hover:bg-primary-bright disabled:cursor-not-allowed disabled:bg-white/10 disabled:text-faint-foreground"
              aria-label={t('composer.send')}
            >
              <ArrowUp className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

function ComposerChip({ children }: { children: React.ReactNode }) {
  return (
    <button
      type="button"
      className="flex shrink-0 items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
    >
      {children}
    </button>
  )
}
