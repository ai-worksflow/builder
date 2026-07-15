'use client'

import { useState } from 'react'
import {
  Boxes,
  FilePlus2,
  FileText,
  GitFork,
  Loader2,
  MonitorPlay,
  RefreshCw,
  Rocket,
  Users2,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import { useWorksflow } from '@/lib/worksflow/store'
import { useI18n } from '@/lib/i18n'

export function TeamDashboard() {
  const { locale, t } = useI18n()
  const collaboration = useCollaboration()
  const workspace = useArtifactWorkspace()
  const { setSurface, setTeamView, setSelectedDocId } = useWorksflow()
  const [busyAction, setBusyAction] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const project = collaboration.project
  const projectBrief = workspace.documents.find((item) =>
    item.artifact.kind === 'project_brief'
    && item.artifact.lifecycle !== 'archived'
    && item.artifact.status !== 'archived',
  )
  const canCreateBlueprint = workspace.documents.some((item) =>
    item.artifact.kind === 'product_requirements' && item.approvedRevision,
  )

  async function openProjectBrief() {
    if (projectBrief) {
      setSelectedDocId(projectBrief.artifact.id)
      setTeamView('editor')
      return
    }
    setBusyAction('document')
    setError(null)
    try {
      const id = await workspace.createDocument(t('teamPlatform.dashboard.projectBrief'), 'projectBrief')
      if (id) {
        setSelectedDocId(id)
        setTeamView('editor')
      }
    } catch (cause) {
      setError(message(cause, t('teamPlatform.dashboard.operationFailed')))
    } finally {
      setBusyAction(null)
    }
  }

  async function createBlueprint() {
    setBusyAction('blueprint')
    setError(null)
    try {
      await workspace.createBlueprint(t('teamPlatform.dashboard.blueprintName', { project: project?.name ?? t('teamPlatform.common.project') }))
      setTeamView('blueprint')
    } catch (cause) {
      setError(message(cause, t('teamPlatform.dashboard.operationFailed')))
    } finally {
      setBusyAction(null)
    }
  }

  if (!collaboration.session.signedIn || !project) {
    return <DashboardState title={t('teamPlatform.dashboard.signInTitle')} detail={t('teamPlatform.dashboard.signInDetail')} />
  }
  if (workspace.status === 'loading') {
    return <DashboardState loading title={t('teamPlatform.dashboard.loadingTitle')} detail={t('teamPlatform.dashboard.loadingDetail')} />
  }
  if (workspace.status === 'error') {
    return <DashboardState title={t('teamPlatform.dashboard.unavailableTitle')} detail={workspace.error ?? t('teamPlatform.dashboard.loadFailed')} onRetry={workspace.refresh} retryLabel={t('common.retry')} />
  }

  const approvedDocuments = workspace.documents.filter((item) => item.approvedRevision).length
  const pendingReviews = collaboration.reviews.filter((item) => item.state === 'pending' || item.decision === 'request_changes')
  const latestActivity = collaboration.auditEvents.slice(0, 8)

  return (
    <div className="h-full overflow-y-auto bg-canvas scrollbar-thin">
      <div className="mx-auto max-w-6xl px-6 py-6 max-sm:px-4">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <p className="text-[10px] font-semibold uppercase tracking-wider text-primary-bright">{t('teamPlatform.dashboard.serverProject')}</p>
            <h1 className="mt-1 text-xl font-semibold text-foreground">{project.name}</h1>
            <p className="mt-1 text-[12px] text-muted-foreground">{t('teamPlatform.dashboard.deliveryPath')}</p>
          </div>
          <div className="flex flex-wrap gap-2">
            <ActionButton
              icon={projectBrief ? FileText : FilePlus2}
              label={projectBrief
                ? `${t('common.open')} ${t('teamPlatform.dashboard.projectBrief')}`
                : t('teamPlatform.dashboard.createProjectBrief')}
              onClick={() => void openProjectBrief()}
              disabled={(!projectBrief && !collaboration.can('edit')) || Boolean(busyAction)}
              busy={busyAction === 'document'}
            />
            {workspace.blueprints.length === 0 && canCreateBlueprint ? (
              <ActionButton icon={Boxes} label={t('teamPlatform.dashboard.createBlueprint')} onClick={() => void createBlueprint()} disabled={!collaboration.can('edit') || Boolean(busyAction)} busy={busyAction === 'blueprint'} secondary />
            ) : workspace.blueprints.length > 0 ? (
              <ActionButton icon={Boxes} label={t('teamPlatform.dashboard.openBlueprint')} onClick={() => setTeamView('blueprint')} secondary />
            ) : null}
            <ActionButton icon={MonitorPlay} label={t('prototype.studioTitle')} onClick={() => setTeamView('prototype')} secondary />
            <ActionButton icon={Rocket} label={t('teamPlatform.dashboard.applicationWorkbench')} onClick={() => setSurface('workbench')} secondary />
          </div>
        </div>

        {error && <div role="alert" className="mt-4 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">{error}</div>}

        <div className="mt-6 grid grid-cols-2 gap-3 lg:grid-cols-5">
          <Stat label={t('teamPlatform.dashboard.documents')} value={workspace.documents.length.toLocaleString(locale)} />
          <Stat label={t('teamPlatform.dashboard.approvedDocs')} value={approvedDocuments.toLocaleString(locale)} />
          <Stat label={t('teamPlatform.dashboard.blueprints')} value={workspace.blueprints.length.toLocaleString(locale)} />
          <Stat label={t('teamPlatform.dashboard.pageSpecsPrototypes')} value={`${workspace.pageSpecs.length.toLocaleString(locale)} / ${workspace.prototypes.length.toLocaleString(locale)}`} />
          <Stat label={t('teamPlatform.dashboard.pendingReviews')} value={pendingReviews.length.toLocaleString(locale)} warning={pendingReviews.length > 0} />
        </div>

        <div className="mt-5 grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(280px,1fr)]">
          <section className="overflow-hidden rounded-lg border border-border bg-panel">
            <SectionHeader icon={FileText} title={t('teamPlatform.dashboard.canonicalDocuments')} action={t('teamPlatform.dashboard.openEditor')} onAction={() => setTeamView('editor')} />
            <div className="divide-y divide-border">
              {workspace.documents.map((item) => {
                const revision = item.approvedRevision ?? item.latestRevision
                const sourceCount = item.draft?.sourceVersions.length ?? item.latestRevision?.sourceVersions?.length ?? 0
                return (
                  <button key={item.artifact.id} type="button" onClick={() => { setSelectedDocId(item.artifact.id); setTeamView('editor') }} className="grid w-full grid-cols-[minmax(0,1fr)_100px_110px] items-center gap-3 px-4 py-3 text-left hover:bg-white/[0.03] max-sm:grid-cols-[minmax(0,1fr)_90px]">
                    <span className="min-w-0"><span className="block truncate text-[12px] font-medium text-foreground">{item.artifact.title}</span><span className="mt-0.5 block truncate text-[9px] text-faint-foreground">{item.artifact.id} · {t('teamPlatform.dashboard.pinnedSources', { count: sourceCount.toLocaleString(locale) })}</span></span>
                    <span className="rounded bg-white/5 px-2 py-1 text-center text-[9px] text-muted-foreground">{artifactStatusLabel(item.artifact.status, t)}</span>
                    <span className="text-right font-mono text-[9px] text-faint-foreground max-sm:hidden">{revision ? `r${revision.revisionNumber.toLocaleString(locale)} · ${revision.contentHash.slice(0, 8)}` : t('teamPlatform.dashboard.draftOnly')}</span>
                  </button>
                )
              })}
              {workspace.documents.length === 0 && <div className="p-8 text-center text-[11px] text-faint-foreground">{t('teamPlatform.dashboard.noDocuments')}</div>}
            </div>
          </section>

          <section className="overflow-hidden rounded-lg border border-border bg-panel">
            <SectionHeader icon={RefreshCw} title={t('teamPlatform.dashboard.auditActivity')} />
            <div className="divide-y divide-border">
              {latestActivity.map((item) => (
                <div key={item.id} className="px-4 py-3"><div className="truncate text-[10px] font-medium text-foreground">{item.action}</div><div className="mt-1 truncate font-mono text-[8px] text-faint-foreground">{item.targetType}:{item.targetId}</div><time className="mt-1 block text-[8px] text-faint-foreground" dateTime={item.createdAt}>{formatDate(item.createdAt, locale)}</time></div>
              ))}
              {latestActivity.length === 0 && <div className="p-6 text-center text-[10px] text-faint-foreground">{t('teamPlatform.dashboard.noAuditActivity')}</div>}
            </div>
          </section>
        </div>

        <section className="mt-4 rounded-lg border border-border bg-panel">
          <SectionHeader icon={GitFork} title={t('teamPlatform.dashboard.workflowReadiness')} action={t('teamPlatform.dashboard.openDocumentGraph')} onAction={() => setTeamView('graph')} />
          <div className="grid gap-3 p-4 sm:grid-cols-3">
            <Readiness label={t('teamPlatform.dashboard.immutableInput')} ready={approvedDocuments > 0} detail={t('teamPlatform.dashboard.immutableInputDetail')} readyLabel={t('teamPlatform.common.ready')} waitingLabel={t('teamPlatform.common.waiting')} />
            <Readiness label={t('teamPlatform.dashboard.productStructure')} ready={workspace.blueprints.some((item) => item.approvedRevision)} detail={t('teamPlatform.dashboard.productStructureDetail')} readyLabel={t('teamPlatform.common.ready')} waitingLabel={t('teamPlatform.common.waiting')} />
            <Readiness label={t('teamPlatform.dashboard.executableDesign')} ready={workspace.prototypes.some((item) => item.approvedRevision)} detail={t('teamPlatform.dashboard.executableDesignDetail')} readyLabel={t('teamPlatform.common.ready')} waitingLabel={t('teamPlatform.common.waiting')} />
          </div>
        </section>

        <div className="mt-4 flex items-center gap-2 text-[9px] text-faint-foreground"><Users2 className="size-3" />{t('teamPlatform.dashboard.memberRoleSummary', { count: collaboration.members.length.toLocaleString(locale), role: projectRoleLabel(project.role, t) })}</div>
      </div>
    </div>
  )
}

function ActionButton({ icon: Icon, label, onClick, disabled, busy, secondary }: { icon: typeof FilePlus2; label: string; onClick: () => void; disabled?: boolean; busy?: boolean; secondary?: boolean }) {
  return <button type="button" onClick={onClick} disabled={disabled} className={`inline-flex h-9 items-center gap-1.5 rounded-md px-3 text-[11px] font-semibold disabled:opacity-40 ${secondary ? 'border border-border bg-panel text-muted-foreground hover:text-foreground' : 'bg-primary text-primary-foreground'}`}>{busy ? <Loader2 className="size-3.5 animate-spin" /> : <Icon className="size-3.5" />}{label}</button>
}

function Stat({ label, value, warning }: { label: string; value: string | number; warning?: boolean }) {
  return <div className="rounded-lg border border-border bg-panel p-4"><div className="text-[9px] font-semibold uppercase tracking-wide text-faint-foreground">{label}</div><div className={`mt-1 text-xl font-semibold ${warning ? 'text-warning' : 'text-foreground'}`}>{value}</div></div>
}

function SectionHeader({ icon: Icon, title, action, onAction }: { icon: typeof FileText; title: string; action?: string; onAction?: () => void }) {
  return <div className="flex h-11 items-center gap-2 border-b border-border px-4"><Icon className="size-3.5 text-primary-bright" /><h2 className="text-[11px] font-semibold text-foreground">{title}</h2>{action && onAction && <button type="button" onClick={onAction} className="ml-auto text-[9px] text-primary-bright hover:underline">{action}</button>}</div>
}

function Readiness({ label, ready, detail, readyLabel, waitingLabel }: { label: string; ready: boolean; detail: string; readyLabel: string; waitingLabel: string }) {
  return <div className={`rounded-md border p-3 ${ready ? 'border-success/25 bg-success/5' : 'border-warning/25 bg-warning/5'}`}><div className={`text-[10px] font-semibold ${ready ? 'text-success' : 'text-warning'}`}>{ready ? readyLabel : waitingLabel} · {label}</div><p className="mt-1 text-[9px] text-faint-foreground">{detail}</p></div>
}

function DashboardState({ title, detail, loading, onRetry, retryLabel }: { title: string; detail: string; loading?: boolean; onRetry?: () => Promise<void>; retryLabel?: string }) {
  return <div className="flex h-full items-center justify-center bg-canvas p-6 text-center"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6">{loading && <Loader2 className="mx-auto size-6 animate-spin text-primary-bright" />}<h1 className="mt-3 text-base font-semibold text-foreground">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{detail}</p>{onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground">{retryLabel}</button>}</div></div>
}

function formatDate(value: string, locale: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale)
}

function message(cause: unknown, fallback: string) {
  return cause instanceof Error ? cause.message : fallback
}

type Translate = ReturnType<typeof useI18n>['t']

function artifactStatusLabel(status: string, t: Translate) {
  const labels: Record<string, string> = {
    draft: t('doc.status.draft'),
    readyForReview: t('doc.status.readyForReview'),
    changesRequested: t('doc.status.changesRequested'),
    approved: t('doc.status.approved'),
    needsSync: t('doc.status.needsSync'),
    archived: t('doc.status.archived'),
  }
  return labels[status] ?? status
}

function projectRoleLabel(role: string, t: Translate) {
  const labels: Record<string, string> = {
    owner: t('common.owner'),
    admin: t('team.role.admin'),
    editor: t('common.editor'),
    commenter: t('common.commenter'),
    viewer: t('common.viewer'),
  }
  return labels[role] ?? role
}
