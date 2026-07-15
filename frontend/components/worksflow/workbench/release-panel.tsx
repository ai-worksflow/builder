'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Archive,
  CheckCircle2,
  CircleAlert,
  ExternalLink,
  GitBranch,
  LoaderCircle,
  RefreshCw,
  Rocket,
  RotateCcw,
  ShieldCheck,
  X,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  DeliveryClient,
  downloadBlob,
  type DeliveryEnvironment,
  type DeploymentMetadata,
} from '@/lib/delivery/client'
import {
  selectLatestPassingQualityRun,
  selectReleaseBuildManifestId,
} from '@/lib/delivery/release-provenance'
import { QualityClient } from '@/lib/quality/client'
import type { QualityRunResult } from '@/lib/quality/types'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import { useI18n } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { GitHubPanel } from './github-panel'

export function ReleasePanel({ onClose }: { readonly onClose: () => void }) {
  const { locale, t } = useI18n()
  const { platformClient, project, can } = useCollaboration()
  const flow = usePlatformFlow()
  const qualityClient = useMemo(() => new QualityClient(platformClient.http), [platformClient.http])
  const deliveryClient = useMemo(() => new DeliveryClient(platformClient.http), [platformClient.http])
  const [qualityRuns, setQualityRuns] = useState<QualityRunResult[]>([])
  const [deployments, setDeployments] = useState<DeploymentMetadata[]>([])
  const [environment, setEnvironment] = useState<DeliveryEnvironment>('preview')
  const [busy, setBusy] = useState<'refresh' | 'quality' | 'export' | 'publish' | 'rollback' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const requestId = useRef(0)
  const workspace = flow.workspaceRevision
  const exactWorkspace = workspace
    ? {
        artifactId: workspace.artifactId,
        revisionId: workspace.id,
        revisionNumber: workspace.revisionNumber,
        contentHash: workspace.contentHash,
      }
    : null

  const refresh = useCallback(async () => {
    if (!project || !workspace) {
      setQualityRuns([])
      setDeployments([])
      return
    }
    const current = ++requestId.current
    setBusy('refresh')
    setError(null)
    try {
      const [nextQuality, nextDeployments] = await Promise.all([
        qualityClient.list(project.id, { workspaceRevisionId: workspace.id }),
        deliveryClient.list(project.id),
      ])
      if (current !== requestId.current) return
      setQualityRuns(nextQuality)
      setDeployments(nextDeployments)
    } catch (cause) {
      if (current === requestId.current) {
        setError(cause instanceof Error ? cause.message : t('release.error.load'))
      }
    } finally {
      if (current === requestId.current) setBusy(null)
    }
  }, [deliveryClient, project, qualityClient, t, workspace])

  useEffect(() => {
    void refresh()
    return () => {
      requestId.current += 1
    }
  }, [refresh])

  const latestQuality = qualityRuns[0]
  const latestPassingQuality = selectLatestPassingQualityRun(qualityRuns, exactWorkspace)
  const releaseManifestId = selectReleaseBuildManifestId(
    flow.workbenchQueue,
    flow.bundle,
    flow.proposal,
  )
  const selectedDeployment = deployments.find((item) => item.environment === environment)

  async function runQuality() {
    if (!project || !exactWorkspace) return
    setBusy('quality')
    setError(null)
    try {
      const result = await qualityClient.run(project.id, exactWorkspace)
      setQualityRuns((current) => [result, ...current.filter((item) => item.metadata.runId !== result.metadata.runId)])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('release.error.quality'))
    } finally {
      setBusy(null)
    }
  }

  async function exportSource() {
    if (!project || !exactWorkspace) return
    setBusy('export')
    setError(null)
    try {
      const result = await deliveryClient.exportArchive(project.id, {
        kind: 'source',
        revision: exactWorkspace,
        redactSensitive: true,
      })
      downloadBlob(result.blob, result.filename)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('release.error.export'))
    } finally {
      setBusy(null)
    }
  }

  async function publish() {
    if (!project || !exactWorkspace || !latestPassingQuality || !releaseManifestId) return
    setBusy('publish')
    setError(null)
    try {
      const result = await deliveryClient.publish(project.id, {
        deploymentId: selectedDeployment?.deploymentId,
        environment,
        workspaceRevision: exactWorkspace,
        buildManifestId: releaseManifestId,
        qualityRunId: latestPassingQuality.metadata.runId,
        environmentRef: `data-runtime:${environment}`,
        message: t('release.publishMessage', { revision: workspace!.revisionNumber }),
      }, { ifMatch: selectedDeployment?.etag })
      setDeployments((current) => [
        result.deployment,
        ...current.filter((item) => item.deploymentId !== result.deployment.deploymentId),
      ])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('release.error.publish'))
    } finally {
      setBusy(null)
    }
  }

  async function rollback(deployment: DeploymentMetadata, versionId: string) {
    setBusy('rollback')
    setError(null)
    try {
      const next = await deliveryClient.rollback(deployment.deploymentId, versionId, {
        ifMatch: deployment.etag,
        message: t('release.rollbackMessage', { version: versionId }),
      })
      setDeployments((current) => [next, ...current.filter((item) => item.deploymentId !== next.deploymentId)])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('release.error.rollback'))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-black/55 backdrop-blur-sm" role="dialog" aria-modal="true" aria-label={t('release.dialog')}>
      <div className="h-full w-full max-w-2xl overflow-y-auto border-l border-border bg-panel shadow-2xl scrollbar-thin">
        <header className="sticky top-0 z-10 flex h-12 items-center gap-2 border-b border-border bg-panel/95 px-4 backdrop-blur">
          <Rocket className="size-4 text-primary-bright" />
          <div className="min-w-0 flex-1">
            <h2 className="text-xs font-semibold text-foreground">{t('release.center')}</h2>
            <p className="truncate font-mono text-[8px] text-faint-foreground">
              {workspace ? `${workspace.artifactId} · r${formatNumber(workspace.revisionNumber, locale)} · ${workspace.contentHash}` : t('release.noWorkspace')}
            </p>
          </div>
          <button type="button" onClick={() => void refresh()} disabled={busy !== null} className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-35" aria-label={t('release.refresh')}>
            <RefreshCw className={cn('size-3.5', busy === 'refresh' && 'animate-spin')} />
          </button>
          <button type="button" onClick={onClose} className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground" aria-label={t('release.close')}><X className="size-4" /></button>
        </header>

        <div className="space-y-4 p-4">
          {!workspace && <Notice text={t('release.workspaceRequired')} />}
          {error && <div role="alert" className="flex gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-[10px] text-destructive"><CircleAlert className="mt-0.5 size-3.5 shrink-0" /><span className="min-w-0 flex-1">{error}</span><button type="button" onClick={() => setError(null)}><X className="size-3" /></button></div>}

          <section className="rounded-lg border border-border bg-background/45 p-3">
            <div className="flex items-center gap-2">
              <ShieldCheck className="size-4 text-primary-bright" />
              <h3 className="text-[11px] font-semibold text-foreground">{t('release.qualityGate')}</h3>
              {latestQuality && <span className={cn('ml-auto rounded px-2 py-0.5 text-[9px] font-medium', latestQuality.passed ? 'bg-success/15 text-success' : 'bg-destructive/15 text-destructive')}>{latestQuality.passed ? t('workbenchPlatform.status.passed') : t('workbenchPlatform.status.blocked')} · {formatPercentage(latestQuality.score.percentage, locale)}</span>}
            </div>
            <p className="mt-1 text-[9px] leading-relaxed text-faint-foreground">{t('release.qualityDescription')}</p>
            <button type="button" onClick={() => void runQuality()} disabled={!workspace || !can('edit') || busy !== null} className="mt-2 inline-flex h-8 items-center gap-1.5 rounded bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-35">
              {busy === 'quality' ? <LoaderCircle className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />} {t('release.runQuality')}
            </button>
            {latestQuality && (
              <div className="mt-3 grid gap-1.5 sm:grid-cols-2">
                {latestQuality.checks.map((check) => (
                  <div key={check.id} className="flex items-center gap-2 rounded border border-border bg-panel px-2 py-1.5 text-[9px]">
                    {check.status === 'passed' || check.status === 'skipped' ? <CheckCircle2 className="size-3 text-success" /> : <CircleAlert className="size-3 text-destructive" />}
                    <span className="min-w-0 flex-1 truncate text-muted-foreground">{check.title}</span>
                    <span className="text-faint-foreground">{qualityStatusLabel(check.status, t)}</span>
                  </div>
                ))}
              </div>
            )}
          </section>

          <section className="rounded-lg border border-border bg-background/45 p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Archive className="size-4 text-primary-bright" />
              <h3 className="text-[11px] font-semibold text-foreground">{t('release.exportDeploy')}</h3>
              <select value={environment} onChange={(event) => setEnvironment(event.target.value as DeliveryEnvironment)} className="ml-auto h-8 rounded border border-border bg-panel px-2 text-[10px] text-foreground">
                <option value="preview">{t('workbenchPlatform.environment.preview')}</option>
                <option value="production">{t('workbenchPlatform.environment.production')}</option>
              </select>
            </div>
            <div className="mt-2 flex flex-wrap gap-2">
              <button type="button" onClick={() => void exportSource()} disabled={!workspace || busy !== null} className="inline-flex h-8 items-center gap-1.5 rounded border border-border px-3 text-[10px] text-muted-foreground hover:text-foreground disabled:opacity-35">
                {busy === 'export' ? <LoaderCircle className="size-3 animate-spin" /> : <Archive className="size-3" />} {t('release.exportRedacted')}
              </button>
              <button type="button" onClick={() => void publish()} disabled={!workspace || !latestPassingQuality || !releaseManifestId || busy !== null || (environment === 'production' ? !can('publish') : !can('edit'))} className="inline-flex h-8 items-center gap-1.5 rounded bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-35" title={!latestPassingQuality ? t('release.passingRequired') : !releaseManifestId ? t('release.manifestRequired') : undefined}>
                {busy === 'publish' ? <LoaderCircle className="size-3 animate-spin" /> : <Rocket className="size-3" />} {t('release.publishEnvironment', { environment: environmentLabel(environment, t) })}
              </button>
            </div>
            {selectedDeployment && <DeploymentCard deployment={selectedDeployment} busy={busy !== null} onRollback={rollback} />}
          </section>

          <section className="rounded-lg border border-border bg-background/45 p-3">
            <div className="mb-3 flex items-center gap-2"><GitBranch className="size-4 text-primary-bright" /><h3 className="text-[11px] font-semibold text-foreground">{t('release.githubDelivery')}</h3></div>
            <GitHubPanel
              projectId={project?.id}
              files={workspace?.content.files ?? []}
            />
          </section>
        </div>
      </div>
    </div>
  )
}

function DeploymentCard({ deployment, busy, onRollback }: { readonly deployment: DeploymentMetadata; readonly busy: boolean; readonly onRollback: (deployment: DeploymentMetadata, versionId: string) => Promise<void> }) {
  const { locale, t } = useI18n()
  return (
    <div className="mt-3 rounded border border-border bg-panel p-2.5">
      <div className="flex items-center gap-2 text-[10px]">
        <span className="font-medium text-foreground">{environmentLabel(deployment.environment, t)}</span>
        <span className="rounded bg-white/5 px-1.5 py-0.5 text-[8px] text-muted-foreground">{deliveryStatusLabel(deployment.status, t)}</span>
        {deployment.publicPath && <a href={deployment.publicPath} target="_blank" rel="noopener noreferrer" className="ml-auto inline-flex items-center gap-1 text-primary-bright hover:underline">{t('release.openDeployment')} <ExternalLink className="size-3" /></a>}
      </div>
      <div className="mt-2 max-h-36 space-y-1 overflow-y-auto scrollbar-thin">
        {deployment.versions.map((version) => (
          <div key={version.id} className="flex items-center gap-2 rounded bg-background px-2 py-1.5 text-[9px]">
            <span className="font-mono text-faint-foreground">v{formatNumber(version.number, locale)}</span>
            <span className="min-w-0 flex-1 truncate text-muted-foreground">{deliveryActionLabel(version.action, t)} · {deliveryStatusLabel(version.status, t)} · {version.checksum || t('release.checksumPending')}</span>
            {version.status === 'ready' && version.id !== deployment.activeVersionId && (
              <button type="button" onClick={() => void onRollback(deployment, version.id)} disabled={busy} className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-1 text-[8px] text-muted-foreground hover:text-foreground disabled:opacity-35"><RotateCcw className="size-2.5" /> {t('release.rollback')}</button>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

function Notice({ text }: { readonly text: string }) {
  return <div className="flex gap-2 rounded-lg border border-warning/30 bg-warning/10 p-3 text-[10px] leading-relaxed text-warning"><CircleAlert className="mt-0.5 size-3.5 shrink-0" />{text}</div>
}

function environmentLabel(environment: DeliveryEnvironment, t: ReturnType<typeof useI18n>['t']) {
  return environment === 'production'
    ? t('workbenchPlatform.environment.production')
    : t('workbenchPlatform.environment.preview')
}

function qualityStatusLabel(status: QualityRunResult['checks'][number]['status'], t: ReturnType<typeof useI18n>['t']) {
  if (status === 'passed') return t('workbenchPlatform.status.passed')
  if (status === 'skipped') return t('workbenchPlatform.status.skipped')
  if (status === 'warning') return t('workbenchPlatform.status.warning')
  return t('workbenchPlatform.status.failed')
}

function deliveryActionLabel(action: 'publish' | 'rollback', t: ReturnType<typeof useI18n>['t']) {
  return action === 'publish'
    ? t('workbenchPlatform.status.publish')
    : t('workbenchPlatform.status.rollback')
}

function deliveryStatusLabel(status: string, t: ReturnType<typeof useI18n>['t']) {
  if (status === 'ready') return t('workbenchPlatform.status.ready')
  if (status === 'pending') return t('workbenchPlatform.status.pending')
  if (status === 'failed') return t('workbenchPlatform.status.failed')
  return status.replaceAll('_', ' ')
}

function formatNumber(value: number, locale: string) {
  return new Intl.NumberFormat(locale).format(value)
}

function formatPercentage(value: number, locale: string) {
  return new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 2,
  }).format(value / 100)
}
