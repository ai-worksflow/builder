'use client'

import { useEffect, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { ArrowUp, ChevronDown, Plus, Sparkles, Square, TextSelect, Workflow } from 'lucide-react'

export function PromptComposer() {
  const {
    phase,
    isGenerating,
    planMode,
    setPlanMode,
    stopBuild,
    submitPrompt,
    composerDraft,
    setComposerDraft,
    activeBlueprintContext,
  } = useWorksflow()
  const { t } = useI18n()
  const [value, setValue] = useState('')

  useEffect(() => {
    if (composerDraft) setValue(composerDraft)
  }, [composerDraft])

  const placeholder =
    phase === 'planning'
      ? t('composer.placeholder.plan')
      : t('composer.placeholder.default')

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSubmit()
    }
  }

  function handleSubmit() {
    if (!value.trim()) return
    submitPrompt(value)
    setValue('')
    setComposerDraft('')
  }

  return (
    <div className="rounded-lg border border-border-strong bg-card p-2.5 focus-within:border-primary/60 focus-within:ring-1 focus-within:ring-primary/40">
      <textarea
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder={placeholder}
        rows={2}
        className="h-[52px] w-full resize-none bg-transparent px-1 text-[13px] leading-relaxed text-foreground placeholder:text-faint-foreground focus:outline-none"
        aria-label={t('composer.placeholder.default')}
      />

      <div className="flex flex-wrap items-center gap-1.5">
        <CircleButton label={t('composer.addContext')}>
          <Plus className="h-4 w-4" />
        </CircleButton>

        <ComposerChip>
          <Sparkles className="h-3.5 w-3.5" />
          {t('composer.standard')}
          <ChevronDown className="h-3 w-3 opacity-60" />
        </ComposerChip>

        <ComposerChip>
          <TextSelect className="h-3.5 w-3.5" />
          {t('composer.select')}
        </ComposerChip>

        {activeBlueprintContext && (
          <ComposerChip>
            <Workflow className="h-3.5 w-3.5 text-primary-bright" />
            Blueprint
          </ComposerChip>
        )}

        <button
          type="button"
          onClick={() => setPlanMode(!planMode)}
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
              onClick={handleSubmit}
              disabled={value.trim().length === 0}
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

function CircleButton({
  children,
  label,
}: {
  children: React.ReactNode
  label: string
}) {
  return (
    <button
      type="button"
      className="flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-white/5 hover:text-foreground"
      aria-label={label}
      title={label}
    >
      {children}
    </button>
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
