'use client'

import { useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { useCollaboration } from '@/lib/collaboration/provider'
import { LanguageToggle } from './language-toggle'
import {
  Clock,
  Copy,
  FileText,
  GitFork,
  Link2,
  Pencil,
  Plus,
  Star,
  Trash2,
  Upload,
  Zap,
} from 'lucide-react'

export function RecentProjects() {
  const { session, can, authorize } = useCollaboration()
  const canAdminister = session.signedIn && can('admin')
  const canEdit = session.signedIn && can('edit')
  const {
    projects,
    openProject,
    toggleProjectStar,
    createProductProject,
    cloneProductProject,
    renameProductProject,
    importProductProject,
    deleteProductProject,
  } = useWorksflow()
  const { t } = useI18n()
  const importRef = useRef<HTMLInputElement | null>(null)
  const [newDialog, setNewDialog] = useState(false)
  const [projectName, setProjectName] = useState('Untitled project')
  const [renameProject, setRenameProject] = useState<{ id: string; name: string } | null>(null)
  const [deleteProjectId, setDeleteProjectId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function importWorkspace(file: File | undefined) {
    if (!file) return
    if (!(await authorize('admin'))) return
    setError(null)
    try {
      const value: unknown = JSON.parse(await file.text())
      importProductProject(value, projectName)
      setNewDialog(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('recent.importFailed'))
    } finally {
      if (importRef.current) importRef.current.value = ''
    }
  }

  return (
    <div className="flex h-full flex-col bg-background">
      <header className="flex min-h-[50px] shrink-0 items-center justify-between gap-3 border-b border-border bg-panel px-5 py-2 max-sm:px-4">
        <div className="flex items-center gap-2">
          <Clock className="h-4 w-4 text-primary-bright" />
          <span className="text-sm font-semibold text-foreground">{t('recent.title')}</span>
        </div>
        <div className="flex items-center gap-2">
          <LanguageToggle />
          <button
            type="button"
            onClick={async () => {
              if (!(await authorize('admin'))) return
              setProjectName(`Untitled project ${projects.length + 1}`)
              setError(null)
              setNewDialog(true)
            }}
            disabled={!canAdminister}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:cursor-not-allowed disabled:opacity-40"
          >
            <Plus className="h-3.5 w-3.5" />
            {t('recent.newGeneration')}
          </button>
        </div>
      </header>

      <main className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
        <div className="mx-auto max-w-5xl px-6 py-6 max-sm:px-4">
          {!session.signedIn && (
            <div className="mb-4 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-[11px] text-warning">
              Sign in from Settings before creating or administering projects.
            </div>
          )}
          <div className="mb-5 grid grid-cols-1 gap-3 md:grid-cols-3">
            <SummaryCard label={t('recent.activeProjects')} value={String(projects.length)} />
            <SummaryCard
              label={t('recent.linkedDocuments')}
              value={String(projects.reduce((sum, item) => sum + item.linkedDocs, 0))}
            />
            <SummaryCard
              label={t('recent.starred')}
              value={String(projects.filter((item) => item.starred).length)}
            />
          </div>

          <section className="overflow-x-auto rounded-lg border border-border bg-panel scrollbar-thin">
            <div className="min-w-[720px]">
              <div className="grid grid-cols-[1fr_140px_140px_200px] border-b border-border px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wide text-faint-foreground">
                <span>{t('recent.project')}</span>
                <span>{t('recent.phase')}</span>
                <span>{t('recent.latest')}</span>
                <span className="text-right">{t('recent.updated')}</span>
              </div>
              <div className="divide-y divide-border">
                {projects.map((project) => (
                  <div
                    key={project.id}
                    className="grid grid-cols-[1fr_140px_140px_200px] items-center gap-3 px-4 py-3 hover:bg-white/[0.03]"
                  >
                    <button
                      type="button"
                      onClick={() => openProject(project.id)}
                      className="flex min-w-0 items-center gap-3 text-left"
                    >
                      <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/15 text-primary-bright">
                        {project.id === 'p2' ? (
                          <GitFork className="h-4 w-4" />
                        ) : (
                          <FileText className="h-4 w-4" />
                        )}
                      </span>
                      <span className="min-w-0">
                        <span className="block truncate text-sm font-medium text-foreground">
                          {project.name}
                        </span>
                        <span className="mt-0.5 flex items-center gap-1 text-[11px] text-faint-foreground">
                          {project.teamName}
                          <span>·</span>
                          <Link2 className="h-3 w-3" />
                          {t('recent.docsCount', { count: project.linkedDocs })}
                        </span>
                      </span>
                    </button>

                    <span className="text-[12px] text-muted-foreground">{project.phase}</span>
                    <span className="text-[12px] text-muted-foreground">{project.latestVersion}</span>
                    <div className="flex items-center justify-end gap-2">
                      <span className="text-[12px] text-faint-foreground">{project.updatedAt}</span>
                      <button
                        type="button"
                        onClick={() => void authorize('edit').then((allowed) => allowed && toggleProjectStar(project.id))}
                        disabled={!canEdit}
                        className="flex h-7 w-7 items-center justify-center rounded-md hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40"
                        aria-label={project.starred ? t('recent.unstar') : t('recent.star')}
                      >
                        <Star
                          className={cn(
                            'h-4 w-4',
                            project.starred ? 'fill-warning text-warning' : 'text-faint-foreground',
                          )}
                        />
                      </button>
                      <button
                        type="button"
                        onClick={async () => {
                          if (await authorize('admin')) cloneProductProject(project.id)
                        }}
                        disabled={!canAdminister}
                        className="flex h-7 w-7 items-center justify-center rounded-md text-faint-foreground hover:bg-white/5 hover:text-foreground"
                        aria-label={t('recent.cloneProject', { name: project.name })}
                      >
                        <Copy className="h-3.5 w-3.5" />
                      </button>
                      <button
                        type="button"
                        onClick={async () => {
                          if (await authorize('admin')) setRenameProject({ id: project.id, name: project.name })
                        }}
                        disabled={!canAdminister}
                        className="flex h-7 w-7 items-center justify-center rounded-md text-faint-foreground hover:bg-white/5 hover:text-foreground"
                        aria-label={t('recent.renameProject', { name: project.name })}
                      >
                        <Pencil className="h-3.5 w-3.5" />
                      </button>
                      <button
                        type="button"
                        onClick={async () => {
                          if (await authorize('admin')) setDeleteProjectId(project.id)
                        }}
                        disabled={!canAdminister}
                        className="flex h-7 w-7 items-center justify-center rounded-md text-faint-foreground hover:bg-destructive/10 hover:text-destructive"
                        aria-label={t('recent.deleteProject', { name: project.name })}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </section>
        </div>
      </main>

      {newDialog && (
        <Modal title={t('recent.createProject')} onClose={() => setNewDialog(false)}>
          <label className="block text-[11px] text-muted-foreground">
            {t('recent.projectName')}
            <input
              value={projectName}
              onChange={(event) => setProjectName(event.target.value)}
              autoFocus
              className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-3 text-[12px] text-foreground outline-none focus:border-primary/60"
            />
          </label>
          <div className="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
            <CreationButton
              icon={Zap}
              title={t('recent.blankProject')}
              description={t('recent.blankProjectDescription')}
              onClick={() => {
                void authorize('admin').then((allowed) => {
                  if (!allowed) return
                  createProductProject(projectName, 'blank')
                  setNewDialog(false)
                })
              }}
            />
            <CreationButton
              icon={Copy}
              title={t('recent.templateProject')}
              description={t('recent.templateProjectDescription')}
              onClick={() => {
                void authorize('admin').then((allowed) => {
                  if (!allowed) return
                  createProductProject(projectName, 'template')
                  setNewDialog(false)
                })
              }}
            />
            <CreationButton
              icon={Upload}
              title={t('recent.importProject')}
              description={t('recent.importProjectDescription')}
              onClick={() => importRef.current?.click()}
            />
          </div>
          <input
            ref={importRef}
            type="file"
            accept="application/json,.json"
            onChange={(event) => void importWorkspace(event.target.files?.[0])}
            className="hidden"
          />
          {error && <p role="alert" className="mt-3 text-[11px] text-destructive">{error}</p>}
        </Modal>
      )}

      {renameProject && (
        <Modal title={t('recent.renameTitle')} onClose={() => setRenameProject(null)}>
          <form
            onSubmit={(event) => {
              event.preventDefault()
              void authorize('admin').then((allowed) => {
                if (!allowed) return
                renameProductProject(renameProject.id, renameProject.name)
                setRenameProject(null)
              })
            }}
          >
            <input
              value={renameProject.name}
              onChange={(event) => setRenameProject({ ...renameProject, name: event.target.value })}
              autoFocus
              className="h-9 w-full rounded-md border border-border bg-background px-3 text-[12px] text-foreground outline-none focus:border-primary/60"
            />
            <div className="mt-3 flex justify-end gap-2">
              <button type="button" onClick={() => setRenameProject(null)} className="rounded-md border border-border px-3 py-1.5 text-[11px] text-muted-foreground">{t('common.cancel')}</button>
              <button type="submit" className="rounded-md bg-primary px-3 py-1.5 text-[11px] font-semibold text-primary-foreground">{t('common.save')}</button>
            </div>
          </form>
        </Modal>
      )}

      {deleteProjectId && (
        <Modal title={t('recent.deleteTitle')} onClose={() => setDeleteProjectId(null)}>
          <p className="text-[12px] leading-relaxed text-muted-foreground">{t('recent.deleteCopy')}</p>
          <div className="mt-3 flex justify-end gap-2">
            <button type="button" onClick={() => setDeleteProjectId(null)} className="rounded-md border border-border px-3 py-1.5 text-[11px] text-muted-foreground">{t('common.cancel')}</button>
            <button
              type="button"
              onClick={() => {
                void authorize('admin').then((allowed) => {
                  if (!allowed) return
                  deleteProductProject(deleteProjectId)
                  setDeleteProjectId(null)
                })
              }}
              className="rounded-md bg-destructive px-3 py-1.5 text-[11px] font-semibold text-destructive-foreground"
            >
              {t('common.delete')}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}

function Modal({
  title,
  children,
  onClose,
}: {
  title: string
  children: React.ReactNode
  onClose: () => void
}) {
  const { t } = useI18n()
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-4">
      <div role="dialog" aria-modal="true" aria-label={title} className="w-full max-w-xl rounded-lg border border-border bg-popover p-4 shadow-2xl">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-foreground">{title}</h2>
          <button type="button" onClick={onClose} className="rounded-md px-2 py-1 text-[11px] text-faint-foreground hover:bg-white/5 hover:text-foreground">{t('common.close')}</button>
        </div>
        {children}
      </div>
    </div>
  )
}

function CreationButton({
  icon: Icon,
  title,
  description,
  onClick,
}: {
  icon: typeof Zap
  title: string
  description: string
  onClick: () => void
}) {
  return (
    <button type="button" onClick={onClick} className="rounded-lg border border-border bg-card p-3 text-left hover:border-primary/40">
      <Icon className="h-4 w-4 text-primary-bright" />
      <span className="mt-2 block text-[12px] font-medium text-foreground">{title}</span>
      <span className="mt-1 block text-[10px] leading-relaxed text-faint-foreground">{description}</span>
    </button>
  )
}

function SummaryCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-border bg-panel p-4">
      <div className="text-[11px] font-semibold uppercase tracking-wide text-faint-foreground">
        {label}
      </div>
      <div className="mt-1 text-2xl font-semibold text-foreground">{value}</div>
    </div>
  )
}
