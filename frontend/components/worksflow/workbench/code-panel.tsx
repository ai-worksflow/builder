'use client'

import { useEffect, useMemo, useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { TERMINAL_LINES } from '@/lib/worksflow/mock-data'
import { searchFiles } from '@/lib/worksflow/workspace-model'
import { useWorksflow } from '@/lib/worksflow/store'
import { VersionPanel } from './version-panel'
import { QualityPanel } from './quality-panel'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  ChevronDown,
  FileCode2,
  FileJson,
  Files,
  FolderOpen,
  GitCompareArrows,
  Pencil,
  Plus,
  Save,
  Search,
  ShieldCheck,
  Terminal,
  Trash2,
  X,
} from 'lucide-react'

type ConsoleTab = 'worksflow' | 'publish' | 'terminal' | 'quality' | 'versions'

export function CodePanel() {
  const { t } = useI18n()
  const { session, can } = useCollaboration()
  const readOnly = !session.signedIn || !can('edit')
  const {
    workspace,
    selectedWorkspaceFile,
    setSelectedWorkspaceFile,
    updateWorkspaceFile,
    createWorkspaceFile,
    deleteWorkspaceFile,
    renameWorkspaceFile,
    createWorkspaceCheckpoint,
    generationEvents,
    workspaceHydrationStatus,
    workspacePersistenceError,
    workspaceIsSaving,
    workspaceLastSavedAt,
    resetWorkspacePersistence,
    workspaceHasExternalConflict,
    resolveWorkspaceExternalConflict,
    runWorkspaceQuality,
    deliveryLogs,
    deliveryError,
    deliveryStatus,
  } = useWorksflow()
  const file = workspace.files.find((item) => item.path === selectedWorkspaceFile)
  const [draft, setDraft] = useState(file?.content ?? '')
  const [consoleOpen, setConsoleOpen] = useState(true)
  const [tab, setTab] = useState<ConsoleTab>('terminal')
  const [searchOpen, setSearchOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [fileAction, setFileAction] = useState<'create' | 'rename' | null>(null)
  const [fileNameDraft, setFileNameDraft] = useState('')
  const [terminalCommand, setTerminalCommand] = useState('')
  const [terminalOutput, setTerminalOutput] = useState<string[]>(TERMINAL_LINES)

  useEffect(() => setDraft(file?.content ?? ''), [file?.content, file?.path])

  const searchResults = useMemo(
    () => (query.trim() ? searchFiles(workspace, query, { maxResults: 80 }) : []),
    [query, workspace],
  )
  const dirty = Boolean(file && (draft !== file.content || file.dirty))
  const generationLogs = generationEvents
    .filter((event) => event.type === 'log')
    .map((event) => (event.type === 'log' ? event.message : ''))

  function save() {
    if (!file || !dirty || readOnly) return
    updateWorkspaceFile(file.path, draft)
    setTerminalOutput((current) => [...current, `saved ${file.path}`])
  }

  function submitFileAction() {
    const name = fileNameDraft.trim()
    if (!name || readOnly) return
    try {
      if (fileAction === 'create') createWorkspaceFile(name)
      if (fileAction === 'rename' && file) renameWorkspaceFile(file.path, name)
      setFileAction(null)
      setFileNameDraft('')
    } catch (error) {
      setTerminalOutput((current) => [
        ...current,
        `error: ${error instanceof Error ? error.message : 'file operation failed'}`,
      ])
      setTab('terminal')
      setConsoleOpen(true)
    }
  }

  async function runTerminalCommand() {
    const command = terminalCommand.trim()
    if (!command) return
    setTerminalCommand('')
    if (command === 'clear') {
      setTerminalOutput([])
      return
    }

    let output: string[]
    if (command === 'help') {
      output = ['Supported commands: help, files, check, checkpoint, preview, clear']
    } else if (command === 'files') {
      output = workspace.files.map((item) => `${item.dirty ? '*' : ' '} ${item.path}`)
    } else if (command === 'check') {
      setTerminalOutput((current) => [...current, `➜ ${command}`, '… running 12 static quality gates'])
      const result = await runWorkspaceQuality()
      output = result
        ? [
            `${result.passed ? '✓' : '✗'} quality score ${result.score.percentage}/100`,
            ...result.checks.map((check) =>
              `${check.status === 'passed' ? '✓' : check.status === 'failed' ? '✗' : '!'} ${check.title}: ${check.status}`,
            ),
            `${result.diagnostics.length} diagnostic${result.diagnostics.length === 1 ? '' : 's'} · run ${result.metadata.runId}`,
          ]
        : ['✗ quality service failed; open the Quality tab for recovery details']
      setTerminalOutput((current) => [...current, ...output])
      return
    } else if (command === 'checkpoint') {
      if (readOnly) {
        output = ['✗ your project role does not allow workspace checkpoints']
        setTerminalOutput((current) => [...current, `➜ ${command}`, ...output])
        return
      }
      createWorkspaceCheckpoint(`Manual checkpoint ${workspace.checkpoints.length + 1}`)
      output = ['✓ checkpoint created']
    } else if (command === 'preview') {
      output = ['✓ preview document rebuilt from the current workspace']
    } else {
      output = [`command not found: ${command}`, 'Run "help" for supported safe workspace commands.']
    }
    setTerminalOutput((current) => [...current, `➜ ${command}`, ...output])
  }

  const consoleLines =
    tab === 'worksflow'
      ? generationLogs.length > 0
        ? generationLogs
        : ['No generation logs yet.']
      : tab === 'publish'
        ? deliveryLogs.length > 0
          ? [
              `Delivery status: ${deliveryStatus}`,
              ...deliveryLogs,
              ...(deliveryError ? [`error: ${deliveryError}`] : []),
            ]
          : [
              `Workspace persistence: ${workspaceHydrationStatus}`,
              workspacePersistenceError ?? 'No deployment has been created yet.',
            ]
        : terminalOutput

  return (
    <div className="flex h-full flex-col">
      <div className="flex min-h-0 flex-1 max-md:flex-col">
        <aside className="flex w-60 shrink-0 flex-col border-r border-border bg-panel max-md:h-44 max-md:w-full max-md:border-b max-md:border-r-0">
          <div className="flex items-center gap-1 border-b border-border px-2 py-1.5 text-[12px] font-medium">
            <span className="flex items-center gap-1.5 px-1 text-foreground">
              <Files className="h-3.5 w-3.5" />
              {t('code.files')}
            </span>
            <button
              type="button"
              onClick={() => setSearchOpen((value) => !value)}
              className={cn('ml-auto rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground', searchOpen && 'bg-white/5 text-foreground')}
              aria-label={t('code.search')}
            >
              <Search className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              onClick={() => {
                setFileAction('create')
                setFileNameDraft('src/new-file.ts')
              }}
              disabled={readOnly}
              className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground"
              aria-label={t('code.newFile')}
            >
              <Plus className="h-3.5 w-3.5" />
            </button>
          </div>

          {searchOpen && (
            <div className="border-b border-border p-2">
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t('code.searchPlaceholder')}
                autoFocus
                className="h-8 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none focus:border-primary/60"
              />
            </div>
          )}

          <div className="flex-1 overflow-y-auto scrollbar-thin p-1.5 text-[12px]">
            {searchOpen && query.trim() ? (
              searchResults.length > 0 ? (
                searchResults.map((result, index) => (
                  <button
                    key={`${result.path}-${result.line ?? 0}-${index}`}
                    type="button"
                    onClick={() => setSelectedWorkspaceFile(result.path)}
                    className="block w-full rounded-md px-2 py-1.5 text-left hover:bg-white/5"
                  >
                    <span className="block truncate text-[11px] text-foreground">{result.path}</span>
                    <span className="block truncate font-mono text-[10px] text-faint-foreground">{result.preview}</span>
                  </button>
                ))
              ) : (
                <p className="px-2 py-4 text-center text-[11px] text-faint-foreground">{t('code.noResults')}</p>
              )
            ) : (
              <>
                <div className="flex items-center gap-1.5 px-1.5 py-1 text-muted-foreground">
                  <ChevronDown className="h-3 w-3" />
                  <FolderOpen className="h-3.5 w-3.5" />
                  {workspace.name}
                </div>
                {workspace.files.map((item) => (
                  <FileRow
                    key={item.path}
                    file={item.path}
                    json={item.language === 'json'}
                    dirty={item.dirty}
                    active={selectedWorkspaceFile === item.path}
                    onClick={() => setSelectedWorkspaceFile(item.path)}
                  />
                ))}
              </>
            )}
          </div>
        </aside>

        <section className="flex min-w-0 flex-1 flex-col bg-background max-md:min-h-[280px]">
          <div className="flex h-10 shrink-0 items-center gap-2 border-b border-border bg-panel px-2 text-[12px]">
            {file ? (
              <>
                <span className="flex min-w-0 items-center gap-1.5 rounded-t-md border-b-2 border-primary bg-white/5 px-2.5 py-1.5 text-foreground">
                  <FileCode2 className="h-3.5 w-3.5 shrink-0 text-primary-bright" />
                  <span className="truncate">{file.path}</span>
                  {dirty && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-warning" />}
                </span>
                <button
                  type="button"
                  onClick={save}
                  disabled={!dirty || readOnly}
                  className="ml-auto rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-30"
                  aria-label={t('common.save')}
                >
                  <Save className="h-3.5 w-3.5" />
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setFileAction('rename')
                    setFileNameDraft(file.path)
                  }}
                  disabled={readOnly}
                  className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground"
                  aria-label={t('code.renameFile')}
                >
                  <Pencil className="h-3.5 w-3.5" />
                </button>
                <button
                  type="button"
                  onClick={() => {
                    if (window.confirm(t('code.confirmDeleteFile', { file: file.path }))) {
                      deleteWorkspaceFile(file.path)
                    }
                  }}
                  disabled={readOnly}
                  className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive"
                  aria-label={t('code.deleteFile')}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </>
            ) : (
              <span className="text-faint-foreground">{t('code.selectFile')}</span>
            )}
          </div>

          {file ? (
            <div className="relative flex min-h-0 flex-1 flex-col">
              {workspaceHasExternalConflict && (
                <div role="alert" className="flex flex-wrap items-center gap-2 border-b border-warning/30 bg-warning/10 px-3 py-2 text-[10px] text-warning">
                  <span className="min-w-48 flex-1">
                    Another tab saved a different project version. Local edits are paused until you choose a version.
                  </span>
                  <button
                    type="button"
                    onClick={() => resolveWorkspaceExternalConflict('use-external')}
                    className="rounded border border-warning/40 px-2 py-1 hover:bg-warning/10"
                  >
                    Use other tab
                  </button>
                  <button
                    type="button"
                    onClick={() => resolveWorkspaceExternalConflict('keep-local')}
                    className="rounded bg-warning px-2 py-1 font-semibold text-background hover:opacity-90"
                  >
                    Keep local
                  </button>
                </div>
              )}
              {workspacePersistenceError && !workspaceHasExternalConflict && (
                <div role="alert" className="flex flex-wrap items-center gap-2 border-b border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">
                  <span className="min-w-48 flex-1">{workspacePersistenceError}</span>
                  <button
                    type="button"
                    onClick={resetWorkspacePersistence}
                    className="rounded border border-destructive/40 px-2 py-1 hover:bg-destructive/10"
                  >
                    Reset local data
                  </button>
                </div>
              )}
              {readOnly && (
                <div className="border-b border-warning/30 bg-warning/10 px-3 py-1.5 text-[10px] text-warning">
                  Your collaboration role is read-only. Server authorization still applies to shared edits.
                </div>
              )}
              <textarea
                value={draft}
                onChange={(event) => {
                  const next = event.target.value
                  setDraft(next)
                  if (file && !readOnly) updateWorkspaceFile(file.path, next, true)
                }}
                onKeyDown={(event) => {
                  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
                    event.preventDefault()
                    save()
                  }
                }}
                readOnly={readOnly}
                spellCheck={false}
                className="min-h-0 flex-1 resize-none bg-background p-4 font-mono text-[12.5px] leading-6 text-muted-foreground outline-none selection:bg-primary/30 read-only:cursor-not-allowed read-only:opacity-75"
                aria-label={t('code.editor', { file: file.path })}
              />
              <div className="pointer-events-none absolute bottom-2 right-3 rounded bg-background/85 px-2 py-1 text-[9px] text-faint-foreground shadow-sm">
                {workspaceIsSaving
                  ? 'Saving…'
                  : workspaceLastSavedAt
                    ? `Saved ${new Date(workspaceLastSavedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`
                    : `Storage ${workspaceHydrationStatus}`}
              </div>
            </div>
          ) : (
            <div className="flex flex-1 items-center justify-center text-[13px] text-faint-foreground">{t('code.selectFile')}</div>
          )}
        </section>
      </div>

      <div className="shrink-0 border-t border-border bg-panel">
        <div className="flex h-9 items-center gap-1 overflow-x-auto px-2 scrollbar-thin">
          <ConsoleTabButton active={tab === 'worksflow'} onClick={() => setTab('worksflow')}>Worksflow</ConsoleTabButton>
          <ConsoleTabButton active={tab === 'publish'} onClick={() => setTab('publish')}>{t('code.publishOutput')}</ConsoleTabButton>
          <ConsoleTabButton active={tab === 'terminal'} onClick={() => setTab('terminal')}>
            <Terminal className="h-3.5 w-3.5" />{t('code.terminal')}
          </ConsoleTabButton>
          <ConsoleTabButton active={tab === 'quality'} onClick={() => setTab('quality')}>
            <ShieldCheck className="h-3.5 w-3.5" />{t('code.quality')}
          </ConsoleTabButton>
          <ConsoleTabButton active={tab === 'versions'} onClick={() => setTab('versions')}>
            <GitCompareArrows className="h-3.5 w-3.5" />{t('code.versions')}
          </ConsoleTabButton>
          <button
            type="button"
            onClick={() => setConsoleOpen((value) => !value)}
            className="ml-auto flex h-6 w-6 shrink-0 items-center justify-center rounded text-faint-foreground hover:bg-white/5 hover:text-foreground"
            aria-label={consoleOpen ? t('code.collapseTerminal') : t('code.expandTerminal')}
          >
            {consoleOpen ? <X className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
          </button>
        </div>

        {consoleOpen && tab === 'versions' && (
          <div className="h-64 overflow-y-auto border-t border-border bg-background p-2 scrollbar-thin">
            <VersionPanel />
          </div>
        )}

        {consoleOpen && tab === 'quality' && (
          <div className="h-72 overflow-y-auto border-t border-border bg-background scrollbar-thin">
            <QualityPanel />
          </div>
        )}

        {consoleOpen && tab !== 'versions' && tab !== 'quality' && (
          <div className="h-36 overflow-y-auto border-t border-border bg-background px-3 py-2 font-mono text-[11px] leading-relaxed scrollbar-thin">
            {consoleLines.map((line, index) => (
              <div key={`${index}-${line}`} className={cn(line.startsWith('✓') || line.startsWith('➜') ? 'text-success' : line.startsWith('✗') || line.startsWith('error') ? 'text-destructive' : 'text-muted-foreground')}>
                {line}
              </div>
            ))}
            {tab === 'terminal' && (
              <div className="mt-1 flex items-center gap-2 text-foreground">
                <span className="text-success">➜</span>
                <input
                  value={terminalCommand}
                  onChange={(event) => setTerminalCommand(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') void runTerminalCommand()
                  }}
                  className="min-h-6 min-w-0 flex-1 bg-transparent font-mono text-[11px] outline-none"
                  aria-label={t('code.terminalCommand')}
                  autoComplete="off"
                />
              </div>
            )}
          </div>
        )}
      </div>

      {fileAction && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-4">
          <form
            onSubmit={(event) => {
              event.preventDefault()
              submitFileAction()
            }}
            className="w-full max-w-md rounded-lg border border-border bg-popover p-4 shadow-2xl"
          >
            <h3 className="text-sm font-semibold text-foreground">{fileAction === 'create' ? t('code.newFile') : t('code.renameFile')}</h3>
            <input
              value={fileNameDraft}
              onChange={(event) => setFileNameDraft(event.target.value)}
              autoFocus
              className="mt-3 w-full rounded-md border border-border bg-background px-3 py-2 font-mono text-[12px] text-foreground outline-none focus:border-primary/60"
            />
            <div className="mt-4 flex justify-end gap-2">
              <button type="button" onClick={() => setFileAction(null)} className="rounded-md border border-border px-3 py-1.5 text-[11px] text-muted-foreground">{t('common.cancel')}</button>
              <button type="submit" className="rounded-md bg-primary px-3 py-1.5 text-[11px] font-semibold text-primary-foreground">{t('common.save')}</button>
            </div>
          </form>
        </div>
      )}
    </div>
  )
}

function FileRow({
  file,
  active,
  json,
  dirty,
  onClick,
}: {
  file: string
  active: boolean
  json: boolean
  dirty: boolean
  onClick: () => void
}) {
  const Icon = json ? FileJson : FileCode2
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-left',
        active ? 'bg-primary/15 text-foreground' : 'text-muted-foreground hover:bg-white/5',
      )}
    >
      <Icon className={cn('h-3.5 w-3.5 shrink-0', active ? 'text-primary-bright' : 'text-faint-foreground')} />
      <span className="truncate">{file}</span>
      {dirty && <span className="ml-auto h-1.5 w-1.5 shrink-0 rounded-full bg-warning" />}
    </button>
  )
}

function ConsoleTabButton({
  children,
  active,
  onClick,
}: {
  children: React.ReactNode
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex shrink-0 items-center gap-1.5 rounded-md px-2.5 py-1 text-[12px] font-medium transition-colors',
        active ? 'bg-white/5 text-foreground' : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}
