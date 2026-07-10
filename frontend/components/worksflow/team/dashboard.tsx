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

export function TeamDashboard() {
  const collaboration = useCollaboration()
  const workspace = useArtifactWorkspace()
  const { setSurface, setTeamView, setSelectedDocId } = useWorksflow()
  const [busyAction, setBusyAction] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const project = collaboration.project

  async function createDocument() {
    setBusyAction('document')
    setError(null)
    try {
      const id = await workspace.createDocument('Project Brief', 'projectBrief')
      if (id) {
        setSelectedDocId(id)
        setTeamView('editor')
      }
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusyAction(null)
    }
  }

  async function createBlueprint() {
    setBusyAction('blueprint')
    setError(null)
    try {
      await workspace.createBlueprint(`${project?.name ?? 'Project'} Blueprint`)
      setTeamView('blueprint')
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusyAction(null)
    }
  }

  if (!collaboration.session.signedIn || !project) {
    return <DashboardState title="Sign in and select a server project" detail="The team overview has no browser project fallback." />
  }
  if (workspace.status === 'loading') {
    return <DashboardState loading title="Loading project truth" detail="Fetching canonical documents, blueprints, PageSpecs and prototypes." />
  }
  if (workspace.status === 'error') {
    return <DashboardState title="Artifact service unavailable" detail={workspace.error ?? 'Unable to load project artifacts.'} onRetry={workspace.refresh} />
  }

  const approvedDocuments = workspace.documents.filter((item) => item.approvedRevision).length
  const pendingReviews = collaboration.reviews.filter((item) => item.state === 'pending' || item.decision === 'request_changes')
  const latestActivity = collaboration.auditEvents.slice(0, 8)

  return (
    <div className="h-full overflow-y-auto bg-canvas scrollbar-thin">
      <div className="mx-auto max-w-6xl px-6 py-6 max-sm:px-4">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <p className="text-[10px] font-semibold uppercase tracking-wider text-primary-bright">Server project</p>
            <h1 className="mt-1 text-xl font-semibold text-foreground">{project.name}</h1>
            <p className="mt-1 text-[12px] text-muted-foreground">Document truth → Blueprint/PageSpec → Prototype → Workbench application</p>
          </div>
          <div className="flex flex-wrap gap-2">
            <ActionButton icon={FilePlus2} label="Create Project Brief" onClick={() => void createDocument()} disabled={!collaboration.can('edit') || Boolean(busyAction)} busy={busyAction === 'document'} />
            {workspace.blueprints.length === 0 ? (
              <ActionButton icon={Boxes} label="Create blueprint" onClick={() => void createBlueprint()} disabled={!collaboration.can('edit') || Boolean(busyAction)} busy={busyAction === 'blueprint'} secondary />
            ) : (
              <ActionButton icon={Boxes} label="Open blueprint" onClick={() => setTeamView('blueprint')} secondary />
            )}
            <ActionButton icon={MonitorPlay} label="Prototype Studio" onClick={() => setTeamView('prototype')} secondary />
            <ActionButton icon={Rocket} label="Application Workbench" onClick={() => setSurface('workbench')} secondary />
          </div>
        </div>

        {error && <div role="alert" className="mt-4 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">{error}</div>}

        <div className="mt-6 grid grid-cols-2 gap-3 lg:grid-cols-5">
          <Stat label="Documents" value={workspace.documents.length} />
          <Stat label="Approved docs" value={approvedDocuments} />
          <Stat label="Blueprints" value={workspace.blueprints.length} />
          <Stat label="PageSpecs / Prototypes" value={`${workspace.pageSpecs.length} / ${workspace.prototypes.length}`} />
          <Stat label="Pending reviews" value={pendingReviews.length} warning={pendingReviews.length > 0} />
        </div>

        <div className="mt-5 grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(280px,1fr)]">
          <section className="overflow-hidden rounded-lg border border-border bg-panel">
            <SectionHeader icon={FileText} title="Canonical documents" action="Open editor" onAction={() => setTeamView('editor')} />
            <div className="divide-y divide-border">
              {workspace.documents.map((item) => {
                const revision = item.approvedRevision ?? item.latestRevision
                const sourceCount = item.draft?.sourceVersions.length ?? item.latestRevision?.sourceVersions?.length ?? 0
                return (
                  <button key={item.artifact.id} type="button" onClick={() => { setSelectedDocId(item.artifact.id); setTeamView('editor') }} className="grid w-full grid-cols-[minmax(0,1fr)_100px_110px] items-center gap-3 px-4 py-3 text-left hover:bg-white/[0.03] max-sm:grid-cols-[minmax(0,1fr)_90px]">
                    <span className="min-w-0"><span className="block truncate text-[12px] font-medium text-foreground">{item.artifact.title}</span><span className="mt-0.5 block truncate text-[9px] text-faint-foreground">{item.artifact.id} · {sourceCount} pinned source(s)</span></span>
                    <span className="rounded bg-white/5 px-2 py-1 text-center text-[9px] text-muted-foreground">{item.artifact.status}</span>
                    <span className="text-right font-mono text-[9px] text-faint-foreground max-sm:hidden">{revision ? `r${revision.revisionNumber} · ${revision.contentHash.slice(0, 8)}` : 'draft only'}</span>
                  </button>
                )
              })}
              {workspace.documents.length === 0 && <div className="p-8 text-center text-[11px] text-faint-foreground">No server documents yet. Create a Project Brief to start the minimum loop.</div>}
            </div>
          </section>

          <section className="overflow-hidden rounded-lg border border-border bg-panel">
            <SectionHeader icon={RefreshCw} title="Audit activity" />
            <div className="divide-y divide-border">
              {latestActivity.map((item) => (
                <div key={item.id} className="px-4 py-3"><div className="truncate text-[10px] font-medium text-foreground">{item.action}</div><div className="mt-1 truncate font-mono text-[8px] text-faint-foreground">{item.targetType}:{item.targetId}</div><time className="mt-1 block text-[8px] text-faint-foreground" dateTime={item.createdAt}>{formatDate(item.createdAt)}</time></div>
              ))}
              {latestActivity.length === 0 && <div className="p-6 text-center text-[10px] text-faint-foreground">No auditable project activity yet.</div>}
            </div>
          </section>
        </div>

        <section className="mt-4 rounded-lg border border-border bg-panel">
          <SectionHeader icon={GitFork} title="Workflow readiness" action="Open document graph" onAction={() => setTeamView('graph')} />
          <div className="grid gap-3 p-4 sm:grid-cols-3">
            <Readiness label="Immutable business input" ready={approvedDocuments > 0} detail="At least one approved document revision" />
            <Readiness label="Product structure" ready={workspace.blueprints.some((item) => item.approvedRevision)} detail="Approved Blueprint before formal PageSpecs" />
            <Readiness label="Executable design" ready={workspace.prototypes.some((item) => item.approvedRevision)} detail="Approved Prototype before build manifest" />
          </div>
        </section>

        <div className="mt-4 flex items-center gap-2 text-[9px] text-faint-foreground"><Users2 className="size-3" />{collaboration.members.length} project member(s) · role {project.role}</div>
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

function Readiness({ label, ready, detail }: { label: string; ready: boolean; detail: string }) {
  return <div className={`rounded-md border p-3 ${ready ? 'border-success/25 bg-success/5' : 'border-warning/25 bg-warning/5'}`}><div className={`text-[10px] font-semibold ${ready ? 'text-success' : 'text-warning'}`}>{ready ? 'Ready' : 'Waiting'} · {label}</div><p className="mt-1 text-[9px] text-faint-foreground">{detail}</p></div>
}

function DashboardState({ title, detail, loading, onRetry }: { title: string; detail: string; loading?: boolean; onRetry?: () => Promise<void> }) {
  return <div className="flex h-full items-center justify-center bg-canvas p-6 text-center"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6">{loading && <Loader2 className="mx-auto size-6 animate-spin text-primary-bright" />}<h1 className="mt-3 text-base font-semibold text-foreground">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{detail}</p>{onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground">Retry</button>}</div></div>
}

function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function message(cause: unknown) {
  return cause instanceof Error ? cause.message : 'Project operation failed.'
}
