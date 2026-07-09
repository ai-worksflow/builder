'use client'

import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { LanguageToggle } from './language-toggle'
import { Clock, FileText, GitFork, Link2, Star, Zap } from 'lucide-react'

export function RecentProjects() {
  const { projects, openProject, toggleProjectStar, setSurface } = useWorksflow()
  const { t } = useI18n()

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
            onClick={() => setSurface('workbench')}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright"
          >
            <Zap className="h-3.5 w-3.5" />
            {t('recent.newGeneration')}
          </button>
        </div>
      </header>

      <main className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
        <div className="mx-auto max-w-5xl px-6 py-6 max-sm:px-4">
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
              <div className="grid grid-cols-[1fr_160px_150px_120px] border-b border-border px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wide text-faint-foreground">
                <span>{t('recent.project')}</span>
                <span>{t('recent.phase')}</span>
                <span>{t('recent.latest')}</span>
                <span className="text-right">{t('recent.updated')}</span>
              </div>
              <div className="divide-y divide-border">
                {projects.map((project) => (
                  <div
                    key={project.id}
                    className="grid grid-cols-[1fr_160px_150px_120px] items-center gap-3 px-4 py-3 hover:bg-white/[0.03]"
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
                        onClick={() => toggleProjectStar(project.id)}
                        className="flex h-7 w-7 items-center justify-center rounded-md hover:bg-white/5"
                        aria-label={project.starred ? t('recent.unstar') : t('recent.star')}
                      >
                        <Star
                          className={cn(
                            'h-4 w-4',
                            project.starred ? 'fill-warning text-warning' : 'text-faint-foreground',
                          )}
                        />
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </section>
        </div>
      </main>
    </div>
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
