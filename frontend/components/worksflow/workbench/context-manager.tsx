'use client'

import { useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import {
  attachmentFromFile,
  attachmentId,
  attachmentSetIssue,
  attachmentSummary,
  isSafeContextUrl,
  type ComposerAttachment,
  type ComposerContextSource,
} from '@/lib/worksflow/composer-context'
import { useDropdown } from '../use-dropdown'
import {
  FileCode2,
  FileText,
  Globe2,
  Eye,
  EyeOff,
  Image as ImageIcon,
  Link2,
  Loader2,
  Plus,
  Upload,
  X,
} from 'lucide-react'

type ContextTab = 'add' | 'workspace' | 'documents'

export function ContextManager({
  attachments,
  workspaceFiles,
  documents,
  onAdd,
  onRemove,
  onToggle,
}: {
  attachments: ComposerAttachment[]
  workspaceFiles: ComposerContextSource[]
  documents: ComposerContextSource[]
  onAdd: (attachment: ComposerAttachment) => void
  onRemove: (id: string) => void
  onToggle: (id: string) => void
}) {
  const { t } = useI18n()
  const dropdown = useDropdown()
  const inputRef = useRef<HTMLInputElement | null>(null)
  const [tab, setTab] = useState<ContextTab>('add')
  const [url, setUrl] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [reading, setReading] = useState(false)

  async function addFiles(files: FileList | null) {
    if (!files?.length) return
    setReading(true)
    setError(null)
    try {
      const nextAttachments = [...attachments]
      for (const file of Array.from(files)) {
        const attachment = await attachmentFromFile(file)
        const issue = attachmentSetIssue(nextAttachments, attachment)
        if (issue) {
          setError(issue)
          break
        }
        onAdd(attachment)
        nextAttachments.push(attachment)
      }
    } catch (cause) {
      const code = cause instanceof Error ? cause.message : 'read-failed'
      setError(t(code === 'image-too-large' ? 'composer.context.imageTooLarge' : code === 'file-too-large' ? 'composer.context.fileTooLarge' : code === 'unsupported-file' ? 'composer.context.unsupported' : 'composer.context.readFailed'))
    } finally {
      setReading(false)
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  function addUrl() {
    const value = url.trim()
    if (!isSafeContextUrl(value)) {
      setError(t('composer.context.invalidUrl'))
      return
    }
    const attachment: ComposerAttachment = {
      id: attachmentId('url'),
      kind: 'url',
      name: new URL(value).hostname,
      content: value,
      included: true,
    }
    const issue = attachmentSetIssue(attachments, attachment)
    if (issue) {
      setError(issue)
      return
    }
    onAdd(attachment)
    setUrl('')
    setError(null)
  }

  function addSource(kind: 'workspace' | 'document', source: ComposerContextSource) {
    if (attachments.some((item) => item.kind === kind && item.sourceId === source.id)) return
    const attachment: ComposerAttachment = {
      id: attachmentId(kind),
      kind,
      name: source.name,
      content: source.content,
      sourceId: source.id,
      included: true,
    }
    const issue = attachmentSetIssue(attachments, attachment)
    if (issue) {
      setError(issue)
      return
    }
    onAdd(attachment)
  }

  return (
    <div className="contents">
      <div ref={dropdown.ref} className="relative">
        <button
          type="button"
          onClick={() => dropdown.setOpen((value) => !value)}
          className={cn(
            'relative flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-white/5 hover:text-foreground',
            dropdown.open && 'bg-white/5 text-foreground',
          )}
          aria-label={t('composer.addContext')}
          aria-haspopup="dialog"
          aria-expanded={dropdown.open}
          title={t('composer.addContext')}
        >
          <Plus className="h-4 w-4" />
          {attachments.length > 0 && (
            <span className="absolute -right-1 -top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[9px] font-semibold text-primary-foreground">
              {attachments.length}
            </span>
          )}
        </button>

        {dropdown.open && (
          <div
            role="dialog"
            aria-label={t('composer.context.title')}
            className="absolute bottom-[calc(100%+8px)] left-0 z-50 w-[360px] overflow-hidden rounded-lg border border-border-strong bg-popover shadow-2xl shadow-black/50 max-sm:fixed max-sm:inset-x-3 max-sm:bottom-20 max-sm:w-auto"
          >
            <div className="flex items-center justify-between border-b border-border px-3 py-2.5">
              <span className="text-[12px] font-semibold text-foreground">{t('composer.context.title')}</span>
              <button
                type="button"
                onClick={() => dropdown.setOpen(false)}
                className="rounded p-1 text-faint-foreground hover:bg-white/5 hover:text-foreground"
                aria-label={t('common.close')}
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>

            <div className="flex border-b border-border p-1">
              <ContextTabButton active={tab === 'add'} onClick={() => setTab('add')}>
                {t('composer.context.add')}
              </ContextTabButton>
              <ContextTabButton active={tab === 'workspace'} onClick={() => setTab('workspace')}>
                {t('composer.context.workspace')}
              </ContextTabButton>
              <ContextTabButton active={tab === 'documents'} onClick={() => setTab('documents')}>
                {t('composer.context.documents')}
              </ContextTabButton>
            </div>

            <div className="max-h-72 overflow-y-auto scrollbar-thin p-3">
              {tab === 'add' && (
                <div className="space-y-3">
                  <button
                    type="button"
                    onClick={() => inputRef.current?.click()}
                    disabled={reading}
                    className="flex w-full items-center gap-3 rounded-lg border border-dashed border-border-strong bg-card px-3 py-4 text-left hover:border-primary/50 disabled:opacity-60"
                  >
                    <span className="flex h-9 w-9 items-center justify-center rounded-md bg-primary/10 text-primary-bright">
                      {reading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Upload className="h-4 w-4" />}
                    </span>
                    <span>
                      <span className="block text-[12px] font-medium text-foreground">{t('composer.context.upload')}</span>
                      <span className="mt-0.5 block text-[10px] text-faint-foreground">{t('composer.context.uploadHint')}</span>
                    </span>
                  </button>
                  <input
                    ref={inputRef}
                    type="file"
                    multiple
                    accept="image/png,image/jpeg,image/webp,image/gif,.css,.csv,.html,.js,.jsx,.json,.md,.sql,.svg,.ts,.tsx,.txt,.xml,.yaml,.yml,text/*"
                    onChange={(event) => void addFiles(event.target.files)}
                    className="hidden"
                  />

                  <div className="flex gap-2">
                    <div className="flex min-w-0 flex-1 items-center gap-2 rounded-md border border-border bg-background px-2.5">
                      <Globe2 className="h-3.5 w-3.5 shrink-0 text-faint-foreground" />
                      <input
                        value={url}
                        onChange={(event) => setUrl(event.target.value)}
                        onKeyDown={(event) => {
                          if (event.key === 'Enter') {
                            event.preventDefault()
                            addUrl()
                          }
                        }}
                        placeholder="https://"
                        className="h-9 min-w-0 flex-1 bg-transparent text-[12px] text-foreground outline-none placeholder:text-faint-foreground"
                        aria-label={t('composer.context.url')}
                      />
                    </div>
                    <button
                      type="button"
                      onClick={addUrl}
                      className="rounded-md bg-primary px-3 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright"
                    >
                      {t('common.add')}
                    </button>
                  </div>
                </div>
              )}

              {tab === 'workspace' && (
                <ContextSourceList
                  sources={workspaceFiles}
                  empty={t('composer.context.noWorkspaceFiles')}
                  icon={FileCode2}
                  isAdded={(source) => attachments.some((item) => item.kind === 'workspace' && item.sourceId === source.id)}
                  onAdd={(source) => addSource('workspace', source)}
                />
              )}

              {tab === 'documents' && (
                <ContextSourceList
                  sources={documents}
                  empty={t('composer.context.noDocuments')}
                  icon={FileText}
                  isAdded={(source) => attachments.some((item) => item.kind === 'document' && item.sourceId === source.id)}
                  onAdd={(source) => addSource('document', source)}
                />
              )}

              {error && <p className="mt-2 text-[11px] text-destructive">{error}</p>}
            </div>
          </div>
        )}
      </div>

      {attachments.map((attachment) => (
        <span
          key={attachment.id}
          className={cn('flex max-w-[170px] items-center gap-1 rounded-md border border-primary/25 bg-primary/10 px-2 py-1 text-[10px] text-primary-bright', attachment.included === false && 'opacity-55')}
          title={`${attachment.name} · ${attachmentSummary(attachment)} · ${attachment.included === false ? 'excluded' : 'included'}`}
        >
          {attachment.kind === 'image' ? <ImageIcon className="h-3 w-3 shrink-0" /> : attachment.kind === 'url' ? <Globe2 className="h-3 w-3 shrink-0" /> : attachment.kind === 'document' ? <Link2 className="h-3 w-3 shrink-0" /> : <FileText className="h-3 w-3 shrink-0" />}
          <span className={cn('truncate', attachment.included === false && 'line-through')}>{attachment.name}</span>
          <button
            type="button"
            onClick={() => onToggle(attachment.id)}
            className="shrink-0 rounded hover:bg-white/10"
            aria-label={`${attachment.included === false ? 'Include' : 'Exclude'} ${attachment.name}`}
            aria-pressed={attachment.included !== false}
          >
            {attachment.included === false ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
          </button>
          <button
            type="button"
            onClick={() => onRemove(attachment.id)}
            className="shrink-0 rounded hover:bg-white/10"
            aria-label={t('composer.context.remove', { name: attachment.name })}
          >
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
    </div>
  )
}

function ContextTabButton({
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
        'flex-1 rounded-md px-2 py-1.5 text-[11px] font-medium',
        active ? 'bg-white/5 text-foreground' : 'text-faint-foreground hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}

function ContextSourceList({
  sources,
  empty,
  icon: Icon,
  isAdded,
  onAdd,
}: {
  sources: ComposerContextSource[]
  empty: string
  icon: typeof FileText
  isAdded: (source: ComposerContextSource) => boolean
  onAdd: (source: ComposerContextSource) => void
}) {
  const { t } = useI18n()
  if (sources.length === 0) return <p className="py-6 text-center text-[11px] text-faint-foreground">{empty}</p>

  return (
    <div className="space-y-1">
      {sources.map((source) => {
        const added = isAdded(source)
        return (
          <button
            key={source.id}
            type="button"
            onClick={() => onAdd(source)}
            disabled={added}
            className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left hover:bg-white/5 disabled:cursor-default disabled:opacity-60"
          >
            <Icon className="h-3.5 w-3.5 shrink-0 text-faint-foreground" />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[11px] font-medium text-foreground">{source.name}</span>
              {source.description && <span className="block truncate text-[10px] text-faint-foreground">{source.description}</span>}
            </span>
            <span className="text-[10px] text-primary-bright">{added ? t('composer.context.added') : t('common.add')}</span>
          </button>
        )
      })}
    </div>
  )
}
