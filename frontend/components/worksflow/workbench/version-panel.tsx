'use client'

import { useEffect, useMemo, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { compareCheckpoints } from '@/lib/worksflow/workspace-model'
import { useWorksflow } from '@/lib/worksflow/store'
import { useCollaboration } from '@/lib/collaboration/provider'
import { GitBranch, GitCompareArrows, RotateCcw } from 'lucide-react'

export function VersionPanel() {
  const { session, can, authorize } = useCollaboration()
  const canEdit = session.signedIn && can('edit')
  const { t } = useI18n()
  const {
    workspace,
    restoreWorkspaceCheckpoint,
    createWorkspaceBranch,
    undoWorkspaceRestore,
    canUndoWorkspaceRestore,
  } = useWorksflow()
  const [fromId, setFromId] = useState('')
  const [toId, setToId] = useState('')
  const [selectedPath, setSelectedPath] = useState('')
  const [branchName, setBranchName] = useState('')

  useEffect(() => {
    const checkpoints = workspace.checkpoints
    if (checkpoints.length === 0) return
    const latest = checkpoints[checkpoints.length - 1]
    const previous = checkpoints[checkpoints.length - 2] ?? latest
    if (!checkpoints.some((checkpoint) => checkpoint.id === fromId)) setFromId(previous.id)
    if (!checkpoints.some((checkpoint) => checkpoint.id === toId)) setToId(latest.id)
  }, [fromId, toId, workspace.checkpoints])

  const comparison = useMemo(() => {
    if (!fromId || !toId || fromId === toId) return null
    try {
      return compareCheckpoints(workspace, fromId, toId)
    } catch {
      return null
    }
  }, [fromId, toId, workspace])
  const changedFiles = comparison?.files.filter((file) => file.status !== 'unchanged') ?? []
  const selected =
    changedFiles.find((file) => file.path === selectedPath) ?? changedFiles[0]
  const activeBranch = workspace.branches.find((branch) => branch.id === workspace.activeBranchId)

  if (workspace.checkpoints.length === 0) {
    return <p className="px-2 py-3 text-[11px] text-faint-foreground">{t('code.noCheckpoints')}</p>
  }

  return (
    <div className="grid min-h-full grid-cols-[220px_1fr] gap-2 max-md:grid-cols-1">
      <div className="space-y-2">
        <div className="flex items-center gap-2 rounded-md border border-border bg-card px-2.5 py-2 text-[10px] text-muted-foreground">
          <GitBranch className="h-3.5 w-3.5 text-primary-bright" />
          <span className="truncate">{activeBranch?.name ?? workspace.activeBranchId}</span>
          <span className="ml-auto text-faint-foreground">{workspace.checkpoints.length}</span>
        </div>

        <label className="block text-[10px] text-faint-foreground">
          {t('code.compareFrom')}
          <select value={fromId} onChange={(event) => setFromId(event.target.value)} className="mt-1 h-8 w-full rounded-md border border-border bg-background px-2 text-[10px] text-foreground outline-none">
            {workspace.checkpoints.map((checkpoint) => <option key={checkpoint.id} value={checkpoint.id}>{checkpoint.label}</option>)}
          </select>
        </label>
        <label className="block text-[10px] text-faint-foreground">
          {t('code.compareTo')}
          <select value={toId} onChange={(event) => setToId(event.target.value)} className="mt-1 h-8 w-full rounded-md border border-border bg-background px-2 text-[10px] text-foreground outline-none">
            {workspace.checkpoints.map((checkpoint) => <option key={checkpoint.id} value={checkpoint.id}>{checkpoint.label}</option>)}
          </select>
        </label>

        <div className="flex gap-1.5">
          <button type="button" disabled={!canEdit} onClick={() => {
            if (!toId || !window.confirm(t('code.confirmRestore'))) return
            void authorize('edit').then((allowed) => allowed && restoreWorkspaceCheckpoint(toId))
          }} className="inline-flex flex-1 items-center justify-center gap-1 rounded-md border border-border px-2 py-1.5 text-[10px] text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40">
            <RotateCcw className="h-3 w-3" />{t('code.restore')}
          </button>
          <button
            type="button"
            disabled={!canEdit}
            onClick={() => {
              const nextName = branchName.trim() || `branch-${workspace.branches.length + 1}`
              void authorize('edit').then((allowed) => {
                if (!allowed) return
                createWorkspaceBranch(nextName, toId)
                setBranchName('')
              })
            }}
            className="inline-flex flex-1 items-center justify-center gap-1 rounded-md border border-border px-2 py-1.5 text-[10px] text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
          >
            <GitBranch className="h-3 w-3" />{t('code.branch')}
          </button>
        </div>
        {canUndoWorkspaceRestore && (
          <button
            type="button"
            disabled={!canEdit}
            onClick={() => void authorize('edit').then((allowed) => allowed && undoWorkspaceRestore())}
            className="inline-flex w-full items-center justify-center gap-1 rounded-md border border-warning/30 bg-warning/10 px-2 py-1.5 text-[10px] text-warning hover:bg-warning/15"
          >
            <RotateCcw className="h-3 w-3" />{t('code.undoRestore')}
          </button>
        )}
        <input value={branchName} onChange={(event) => setBranchName(event.target.value)} disabled={!canEdit} placeholder={t('code.branchName')} className="h-8 w-full rounded-md border border-border bg-background px-2 text-[10px] text-foreground outline-none disabled:opacity-40" />
      </div>

      <div className="min-w-0 rounded-md border border-border bg-background">
        {!comparison ? (
          <div className="flex h-full items-center justify-center p-4 text-[10px] text-faint-foreground">{t('code.selectDifferentVersions')}</div>
        ) : (
          <div className="flex h-full min-h-0 flex-col">
            <div className="flex items-center gap-3 border-b border-border px-2.5 py-1.5 text-[10px]">
              <GitCompareArrows className="h-3.5 w-3.5 text-primary-bright" />
              <span className="text-success">+{comparison.files.reduce((sum, file) => sum + file.diff.additions, 0)}</span>
              <span className="text-destructive">-{comparison.files.reduce((sum, file) => sum + file.diff.deletions, 0)}</span>
              <span className="text-faint-foreground">{changedFiles.length} {t('code.changedFiles')}</span>
            </div>
            <div className="grid min-h-0 flex-1 grid-cols-[180px_1fr] max-md:grid-cols-1">
              <div className="overflow-y-auto border-r border-border p-1 scrollbar-thin max-md:max-h-20 max-md:border-b max-md:border-r-0">
                {changedFiles.map((file) => (
                  <button
                    key={file.path}
                    type="button"
                    onClick={() => setSelectedPath(file.path)}
                    className={cn('flex w-full items-center gap-1 rounded px-1.5 py-1 text-left text-[10px]', selected?.path === file.path ? 'bg-primary/15 text-foreground' : 'text-muted-foreground hover:bg-white/5')}
                  >
                    <span className={cn('w-4 shrink-0 font-semibold', file.status === 'added' ? 'text-success' : file.status === 'deleted' ? 'text-destructive' : 'text-warning')}>{file.status[0].toUpperCase()}</span>
                    <span className="truncate">{file.path}</span>
                  </button>
                ))}
              </div>
              <pre className="min-h-0 overflow-auto p-2 font-mono text-[10px] leading-4 scrollbar-thin">
                {selected?.diff.lines.map((line, index) => (
                  <div key={`${index}-${line.kind}`} className={cn('grid grid-cols-[28px_28px_12px_1fr] px-1', line.kind === 'add' ? 'bg-emerald-500/10 text-success' : line.kind === 'remove' ? 'bg-red-500/10 text-destructive' : 'text-faint-foreground')}>
                    <span>{line.oldLineNumber ?? ''}</span><span>{line.newLineNumber ?? ''}</span><span>{line.kind === 'add' ? '+' : line.kind === 'remove' ? '-' : ' '}</span><code className="whitespace-pre">{line.content}</code>
                  </div>
                ))}
              </pre>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
