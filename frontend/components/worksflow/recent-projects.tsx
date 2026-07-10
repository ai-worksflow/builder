'use client'

import { useState, type ReactNode } from 'react'
import {
  Archive,
  Clock,
  FileText,
  Loader2,
  Pencil,
  Plus,
  Users2,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import type { CollaborationProject } from '@/lib/collaboration/types'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { LanguageToggle } from './language-toggle'

export function RecentProjects() {
  const {
    session,
    projects,
    project: selectedProject,
    loading,
    backendStatus,
    error: platformError,
    createProject,
    selectProject,
    renameProject,
    archiveProject,
    refresh,
  } = useCollaboration()
  const { setSurface } = useWorksflow()
  const { t } = useI18n()
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('Untitled project')
  const [description, setDescription] = useState('')
  const [renameTarget, setRenameTarget] = useState<CollaborationProject | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [archiveTarget, setArchiveTarget] = useState<CollaborationProject | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)

  async function openProject(projectId: string) {
    setLocalError(null)
    if (await selectProject(projectId)) setSurface('team')
  }

  async function submitCreate() {
    const normalized = name.trim()
    if (!normalized) {
      setLocalError('Enter a project name.')
      return
    }
    const createdId = await createProject(normalized, description.trim() || undefined)
    if (!createdId) return
    setCreateOpen(false)
    setName('Untitled project')
    setDescription('')
    setSurface('team')
  }

  async function submitRename() {
    if (!renameTarget || !renameValue.trim()) return
    if (await renameProject(renameTarget.id, renameValue)) setRenameTarget(null)
  }

  async function submitArchive() {
    if (!archiveTarget) return
    if (await archiveProject(archiveTarget.id)) setArchiveTarget(null)
  }

  const totalMembers = projects.reduce((sum, item) => sum + item.memberCount, 0)
  const error = localError ?? platformError

  return (
    <div className="flex h-full flex-col bg-background">
      <header className="flex min-h-[50px] shrink-0 items-center justify-between gap-3 border-b border-border bg-panel px-5 py-2 max-sm:px-4">
        <div className="flex items-center gap-2">
          <Clock className="size-4 text-primary-bright" />
          <span className="text-sm font-semibold text-foreground">{t('recent.title')}</span>
          {loading && <Loader2 className="size-3.5 animate-spin text-primary-bright" aria-label="Loading projects" />}
        </div>
        <div className="flex items-center gap-2">
          <LanguageToggle />
          <button
            type="button"
            onClick={() => {
              setLocalError(null)
              setCreateOpen(true)
            }}
            disabled={!session.signedIn || backendStatus !== 'online' || loading}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground disabled:cursor-not-allowed disabled:opacity-40"
          >
            <Plus className="size-3.5" /> New project
          </button>
        </div>
      </header>

      <main className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
        <div className="mx-auto max-w-5xl px-6 py-6 max-sm:px-4">
          {!session.signedIn && (
            <Notice>Sign in from Settings to load projects from the shared Go platform.</Notice>
          )}
          {error && <Notice destructive>{error}</Notice>}

          <div className="mb-5 grid grid-cols-1 gap-3 sm:grid-cols-3">
            <SummaryCard label="Server projects" value={String(projects.length)} />
            <SummaryCard label="Project members" value={String(totalMembers)} />
            <SummaryCard label="Current project" value={selectedProject?.name ?? 'None'} compact />
          </div>

          <section className="overflow-hidden rounded-lg border border-border bg-panel">
            <div className="grid grid-cols-[minmax(0,1fr)_110px_110px_180px] border-b border-border px-4 py-2.5 text-[10px] font-semibold uppercase tracking-wide text-faint-foreground max-md:grid-cols-[minmax(0,1fr)_100px_120px]">
              <span>Project</span>
              <span>Role</span>
              <span className="max-md:hidden">Members</span>
              <span className="text-right">Updated</span>
            </div>
            <div className="divide-y divide-border">
              {projects.map((item) => {
                const manageable = item.role === 'owner' || item.role === 'admin'
                return (
                  <div key={item.id} className="grid grid-cols-[minmax(0,1fr)_110px_110px_180px] items-center gap-2 px-4 py-3 hover:bg-white/[0.03] max-md:grid-cols-[minmax(0,1fr)_100px_120px]">
                    <button type="button" onClick={() => void openProject(item.id)} className="flex min-w-0 items-center gap-3 text-left">
                      <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/15 text-primary-bright"><FileText className="size-4" /></span>
                      <span className="min-w-0">
                        <span className="block truncate text-sm font-medium text-foreground">{item.name}</span>
                        <span className="block truncate font-mono text-[9px] text-faint-foreground">{item.id}</span>
                      </span>
                    </button>
                    <span className="text-[11px] capitalize text-muted-foreground">{item.role}</span>
                    <span className="inline-flex items-center gap-1 text-[11px] text-muted-foreground max-md:hidden"><Users2 className="size-3" />{item.memberCount}</span>
                    <div className="flex items-center justify-end gap-1.5">
                      <time className="mr-1 truncate text-[10px] text-faint-foreground" dateTime={item.updatedAt}>{formatTime(item.updatedAt)}</time>
                      <button
                        type="button"
                        onClick={() => {
                          setRenameTarget(item)
                          setRenameValue(item.name)
                        }}
                        disabled={!manageable || loading}
                        className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-30"
                        aria-label={`Rename ${item.name}`}
                      ><Pencil className="size-3.5" /></button>
                      <button
                        type="button"
                        onClick={() => setArchiveTarget(item)}
                        disabled={!manageable || loading}
                        className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:opacity-30"
                        aria-label={`Archive ${item.name}`}
                      ><Archive className="size-3.5" /></button>
                    </div>
                  </div>
                )
              })}
              {session.signedIn && projects.length === 0 && (
                <div className="p-8 text-center text-sm text-faint-foreground">No active server projects. Create one to start the document → blueprint → prototype workflow.</div>
              )}
            </div>
          </section>

          {session.signedIn && backendStatus === 'error' && (
            <button type="button" onClick={() => void refresh()} className="mt-3 rounded-md border border-border px-3 py-2 text-[11px] text-muted-foreground">Retry server projects</button>
          )}
        </div>
      </main>

      {createOpen && (
        <Modal title="Create server project" onClose={() => setCreateOpen(false)}>
          <form onSubmit={(event) => { event.preventDefault(); void submitCreate() }} className="space-y-3">
            <Field label="Project name"><input value={name} onChange={(event) => setName(event.target.value)} autoFocus className={inputClass} /></Field>
            <Field label="Description"><textarea value={description} onChange={(event) => setDescription(event.target.value)} rows={3} className={inputClass} /></Field>
            <ModalActions onCancel={() => setCreateOpen(false)} submitLabel="Create project" busy={loading} />
          </form>
        </Modal>
      )}

      {renameTarget && (
        <Modal title={`Rename ${renameTarget.name}`} onClose={() => setRenameTarget(null)}>
          <form onSubmit={(event) => { event.preventDefault(); void submitRename() }}>
            <input value={renameValue} onChange={(event) => setRenameValue(event.target.value)} autoFocus className={inputClass} />
            <ModalActions onCancel={() => setRenameTarget(null)} submitLabel="Save name" busy={loading} />
          </form>
        </Modal>
      )}

      {archiveTarget && (
        <Modal title={`Archive ${archiveTarget.name}`} onClose={() => setArchiveTarget(null)}>
          <p className="text-[12px] leading-relaxed text-muted-foreground">This sends an If-Match guarded archive command to the platform. It does not delete browser-only data because this list has no browser project fallback.</p>
          <ModalActions onCancel={() => setArchiveTarget(null)} onSubmit={() => void submitArchive()} submitLabel="Archive project" destructive busy={loading} />
        </Modal>
      )}
    </div>
  )
}

const inputClass = 'mt-1.5 w-full rounded-md border border-border bg-background px-3 py-2 text-[12px] text-foreground outline-none focus:border-primary/60'

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block text-[11px] text-muted-foreground">{label}{children}</label>
}

function Modal({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) {
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-4"><div role="dialog" aria-modal="true" aria-label={title} className="w-full max-w-lg rounded-lg border border-border bg-popover p-4 shadow-2xl"><div className="mb-3 flex items-center justify-between"><h2 className="text-sm font-semibold text-foreground">{title}</h2><button type="button" onClick={onClose} className="rounded px-2 py-1 text-[11px] text-faint-foreground hover:bg-white/5">Close</button></div>{children}</div></div>
}

function ModalActions({ onCancel, onSubmit, submitLabel, busy, destructive }: { onCancel: () => void; onSubmit?: () => void; submitLabel: string; busy: boolean; destructive?: boolean }) {
  return <div className="mt-4 flex justify-end gap-2"><button type="button" onClick={onCancel} className="rounded-md border border-border px-3 py-1.5 text-[11px] text-muted-foreground">Cancel</button><button type={onSubmit ? 'button' : 'submit'} onClick={onSubmit} disabled={busy} className={`rounded-md px-3 py-1.5 text-[11px] font-semibold disabled:opacity-40 ${destructive ? 'bg-destructive text-destructive-foreground' : 'bg-primary text-primary-foreground'}`}>{busy ? 'Saving…' : submitLabel}</button></div>
}

function Notice({ children, destructive }: { children: ReactNode; destructive?: boolean }) {
  return <div role={destructive ? 'alert' : undefined} className={`mb-4 rounded-md border px-3 py-2 text-[11px] ${destructive ? 'border-destructive/30 bg-destructive/10 text-destructive' : 'border-warning/30 bg-warning/10 text-warning'}`}>{children}</div>
}

function SummaryCard({ label, value, compact }: { label: string; value: string; compact?: boolean }) {
  return <div className="min-w-0 rounded-lg border border-border bg-panel p-4"><div className="text-[10px] font-semibold uppercase tracking-wide text-faint-foreground">{label}</div><div className={`${compact ? 'truncate text-base' : 'text-2xl'} mt-1 font-semibold text-foreground`}>{value}</div></div>
}

function formatTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString()
}
