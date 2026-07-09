'use client'

import { useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { CODE_FILES, TERMINAL_LINES } from '@/lib/worksflow/mock-data'
import {
  ChevronDown,
  ChevronRight,
  FileCode2,
  FileJson,
  Files,
  Folder,
  FolderOpen,
  Search,
  Terminal,
  X,
} from 'lucide-react'

const CONSOLE_LINES: Record<'worksflow' | 'publish' | 'terminal', string[]> = {
  worksflow: [
    'Worksflow completed task tracking.',
    'Created src/data/tasks.ts',
    'Created src/components/TaskInput.tsx',
    'Verified production build passes.',
  ],
  publish: [
    'Publish output is idle.',
    'No deployment target is connected in prototype mode.',
    'Connect GitHub or export a source bundle to continue.',
  ],
  terminal: TERMINAL_LINES,
}

export function CodePanel() {
  const { t } = useI18n()
  const [selected, setSelected] = useState(CODE_FILES[4].path) // App.tsx
  const [consoleOpen, setConsoleOpen] = useState(true)
  const [tab, setTab] = useState<'worksflow' | 'publish' | 'terminal'>('terminal')

  const file = CODE_FILES.find((f) => f.path === selected)
  const consoleLines = CONSOLE_LINES[tab]

  return (
    <div className="flex h-full flex-col">
      <div className="flex min-h-0 flex-1 max-md:flex-col">
        {/* File explorer */}
        <div className="flex w-56 shrink-0 flex-col border-r border-border bg-panel max-md:h-40 max-md:w-full max-md:border-b max-md:border-r-0">
          <div className="flex items-center gap-3 border-b border-border px-3 py-2 text-[12px] font-medium">
            <button type="button" className="flex items-center gap-1.5 text-foreground">
              <Files className="h-3.5 w-3.5" />
              {t('code.files')}
            </button>
            <button
              type="button"
              className="flex items-center gap-1.5 text-faint-foreground hover:text-foreground"
            >
              <Search className="h-3.5 w-3.5" />
              {t('code.search')}
            </button>
          </div>
          <div className="flex-1 overflow-y-auto scrollbar-thin p-1.5 text-[13px]">
            <TreeRow depth={0} folder label=".worksflow" />
            <TreeRow depth={0} folder open label="src" />
            <TreeRow depth={1} folder open label="components" />
            {CODE_FILES.filter((f) => f.path.startsWith('src/components')).map((f) => (
              <FileRow
                key={f.path}
                depth={2}
                file={f.name}
                active={selected === f.path}
                onClick={() => setSelected(f.path)}
              />
            ))}
            <TreeRow depth={1} folder open label="data" />
            {CODE_FILES.filter((f) => f.path.startsWith('src/data')).map((f) => (
              <FileRow
                key={f.path}
                depth={2}
                file={f.name}
                active={selected === f.path}
                onClick={() => setSelected(f.path)}
              />
            ))}
            <FileRow
              depth={1}
              file="App.tsx"
              active={selected === 'src/App.tsx'}
              onClick={() => setSelected('src/App.tsx')}
            />
            <FileRow
              depth={0}
              file="package.json"
              json
              active={selected === 'package.json'}
              onClick={() => setSelected('package.json')}
            />
          </div>
        </div>

        {/* Editor */}
        <div className="flex min-w-0 flex-1 flex-col bg-background max-md:min-h-[280px]">
          <div className="flex h-9 shrink-0 items-center border-b border-border bg-panel px-3 text-[12px]">
            <span className="flex items-center gap-1.5 rounded-t-md border-b-2 border-primary bg-white/5 px-2.5 py-1.5 text-foreground">
              <FileCode2 className="h-3.5 w-3.5 text-primary-bright" />
              {file?.name}
            </span>
          </div>
          {file ? (
            <CodeEditor content={file.content} />
          ) : (
            <div className="flex flex-1 items-center justify-center text-[13px] text-faint-foreground">
              {t('code.selectFile')}
            </div>
          )}
        </div>
      </div>

      {/* Bottom console */}
      <div className="shrink-0 border-t border-border bg-panel">
        <div className="flex h-9 items-center gap-1 overflow-x-auto px-2 scrollbar-thin">
          <ConsoleTab active={tab === 'worksflow'} onClick={() => setTab('worksflow')}>
            Worksflow
          </ConsoleTab>
          <ConsoleTab active={tab === 'publish'} onClick={() => setTab('publish')}>
            {t('code.publishOutput')}
          </ConsoleTab>
          <ConsoleTab active={tab === 'terminal'} onClick={() => setTab('terminal')}>
            <Terminal className="h-3.5 w-3.5" />
            {t('code.terminal')}
          </ConsoleTab>
          <button
            type="button"
            className="flex h-6 w-6 items-center justify-center rounded text-faint-foreground hover:bg-white/5 hover:text-foreground"
            aria-label={t('code.newTerminal')}
          >
            +
          </button>
          <button
            type="button"
            onClick={() => setConsoleOpen((v) => !v)}
            className="ml-auto flex h-6 w-6 shrink-0 items-center justify-center rounded text-faint-foreground hover:bg-white/5 hover:text-foreground"
            aria-label={consoleOpen ? t('code.collapseTerminal') : t('code.expandTerminal')}
          >
            {consoleOpen ? <X className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
          </button>
        </div>
        {consoleOpen && (
          <div className="h-32 overflow-y-auto scrollbar-thin border-t border-border bg-background px-3 py-2 font-mono text-[12px] leading-relaxed">
            {consoleLines.map((line, i) => (
              <div
                key={i}
                className={cn(
                  line.startsWith('➜') ? 'text-success' : 'text-muted-foreground',
                )}
              >
                {line}
              </div>
            ))}
            <div className="text-foreground">
              <span className="text-success">➜</span> <span className="text-primary-bright">~</span>{' '}
              <span className="animate-pulse">▋</span>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function CodeEditor({ content }: { content: string }) {
  const lines = content.split('\n')
  return (
    <div className="flex-1 overflow-auto scrollbar-thin">
      <div className="flex min-w-max font-mono text-[12.5px] leading-6">
        <div className="select-none border-r border-border bg-panel px-3 py-3 text-right text-faint-foreground">
          {lines.map((_, i) => (
            <div key={i}>{i + 1}</div>
          ))}
        </div>
        <pre className="px-4 py-3 text-muted-foreground">
          <code>{content}</code>
        </pre>
      </div>
    </div>
  )
}

function TreeRow({
  depth,
  label,
  folder,
  open,
}: {
  depth: number
  label: string
  folder?: boolean
  open?: boolean
}) {
  const Icon = folder ? (open ? FolderOpen : Folder) : FileCode2
  return (
    <div
      className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-muted-foreground"
      style={{ paddingLeft: 6 + depth * 14 }}
    >
      {folder ? (
        open ? (
          <ChevronDown className="h-3 w-3 text-faint-foreground" />
        ) : (
          <ChevronRight className="h-3 w-3 text-faint-foreground" />
        )
      ) : (
        <span className="w-3" />
      )}
      <Icon className="h-3.5 w-3.5 text-faint-foreground" />
      {label}
    </div>
  )
}

function FileRow({
  depth,
  file,
  active,
  json,
  onClick,
}: {
  depth: number
  file: string
  active?: boolean
  json?: boolean
  onClick?: () => void
}) {
  const Icon = json ? FileJson : FileCode2
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex w-full items-center gap-1.5 rounded-md py-1 text-left',
        active ? 'bg-primary/15 text-foreground' : 'text-muted-foreground hover:bg-white/5',
      )}
      style={{ paddingLeft: 6 + depth * 14 + 18 }}
    >
      <Icon className={cn('h-3.5 w-3.5', active ? 'text-primary-bright' : 'text-faint-foreground')} />
      {file}
    </button>
  )
}

function ConsoleTab({
  children,
  active,
  onClick,
}: {
  children: React.ReactNode
  active?: boolean
  onClick?: () => void
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
