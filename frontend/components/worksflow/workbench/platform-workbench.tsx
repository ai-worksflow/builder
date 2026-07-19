'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Check,
  ChevronDown,
  CircleAlert,
  Code2,
  Database,
  Eye,
  FileCode2,
  FilePlus2,
  FileText,
  GitCompareArrows,
  LoaderCircle,
  PackageCheck,
  Monitor,
  PencilLine,
  Play,
  RefreshCw,
  Rocket,
  Save,
  ShieldCheck,
  Sparkles,
  X,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n } from '@/lib/i18n'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import type { BuildContractGateSnapshot } from '@/lib/platform/build-contract-gate'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import type {
  FileOperationDto,
  ImplementationProposalDto,
  WorkspaceRevisionDto,
} from '@/lib/platform/flow-contract'
import {
  workbenchBundleNeedsRebase,
  workbenchQueueItemHasAppliedPredecessors,
  type WorkbenchQueueItem,
} from '@/lib/platform/flow-queue'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import type { WorkbenchView } from '@/lib/worksflow/types'
import { FlowPanel } from './flow-panel'
import { DatabasePanel } from './database-panel'
import { ReleasePanel } from './release-panel'
import { ConversationPanel } from './conversation-panel'
import { BuildContractPanel } from './build-contract-panel'
import { SandboxWorkspace } from './sandbox-workspace'

const VIEWS: readonly { id: WorkbenchView; icon: typeof Monitor }[] = [
  { id: 'preview', icon: Monitor },
  { id: 'code', icon: Code2 },
  { id: 'database', icon: Database },
]

export function PlatformWorkbench() {
  const { t } = useI18n()
  const { view, setView, setSurface } = useWorksflow()
  const { session, project, backendStatus, can } = useCollaboration()
  const flow = usePlatformFlow()
  const artifacts = useArtifactWorkspace()
  const [prototypeId, setPrototypeId] = useState('')
  const [showRelease, setShowRelease] = useState(false)
  const [showConversation, setShowConversation] = useState(false)
  const selectedPrototype = artifacts.prototypes.find((item) => item.artifact.id === prototypeId)
    ?? artifacts.prototypes.find((item) => item.approvedRevision)
    ?? artifacts.prototypes[0]
  const selectedQueueItem = flow.workbenchQueue.find(
    (item) => item.bundleId === flow.selectedBundleId,
  )
  const selectedProposal = flow.proposal ?? selectedQueueItem?.proposal
  const candidateEntryAvailable = !flow.requiresWorkbenchRebase && (
    !selectedProposal
    || selectedProposal.status === 'stale'
    || selectedProposal.status === 'rejected'
  )
  const appliedProposal = proposalApplied(flow.proposal)
    ? flow.proposal
    : proposalApplied(selectedQueueItem?.proposal) ? selectedQueueItem?.proposal : null
  const sandboxBuildManifestId = flow.bundle?.id ?? appliedProposal?.buildManifestId ?? null

  useEffect(() => {
    if (!prototypeId && selectedPrototype) setPrototypeId(selectedPrototype.artifact.id)
  }, [prototypeId, selectedPrototype])

  useEffect(() => {
    if (new URLSearchParams(window.location.search).has('conversationId')) {
      setShowConversation(true)
    }
  }, [])

  const unavailable = !session.signedIn || !project || backendStatus === 'error'

  return (
    <div className="relative flex h-full flex-col">
      <header className="flex min-h-12 shrink-0 items-center gap-3 border-b border-border bg-panel px-3 max-md:flex-wrap">
        <button
          type="button"
          onClick={() => setSurface('recent')}
          className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1 hover:bg-white/5"
          title={t('platform.openProjects')}
        >
          <span className="flex size-6 items-center justify-center rounded bg-primary text-[10px] font-bold text-primary-foreground">
            {(project?.name ?? 'W').slice(0, 1).toUpperCase()}
          </span>
          <span className="max-w-52 truncate text-xs font-semibold text-foreground">
            {project?.name ?? t('platform.selectProject')}
          </span>
        </button>

        <nav className="flex items-center gap-1 rounded-md border border-border bg-background p-0.5" aria-label={t('platform.viewNav')}>
          {VIEWS.map((item) => {
            const Icon = item.icon
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => setView(item.id)}
                className={cn(
                  'inline-flex h-7 items-center gap-1.5 rounded px-2 text-[10px] font-medium',
                  view === item.id
                    ? 'bg-primary/15 text-primary-bright'
                    : 'text-faint-foreground hover:text-foreground',
                )}
              >
                <Icon className="size-3" /> {viewLabel(item.id, t)}
              </button>
            )
          })}
        </nav>

        <div className="ml-auto flex min-w-0 items-center gap-2 max-md:ml-0 max-md:w-full">
          <button
            type="button"
            onClick={() => setShowConversation(true)}
            disabled={unavailable}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-primary/30 bg-primary/10 px-3 text-[10px] font-semibold text-primary-bright disabled:opacity-40"
            title={t('platform.openConversation')}
          >
            <Sparkles className="size-3.5" /> {t('platform.conversation')}
          </button>
          <select
            value={selectedPrototype?.artifact.id ?? ''}
            onChange={(event) => setPrototypeId(event.target.value)}
            disabled={unavailable || artifacts.status !== 'ready' || artifacts.prototypes.length === 0}
            className="h-8 min-w-0 max-w-64 flex-1 rounded-md border border-border bg-background px-2 text-[10px] text-foreground outline-none disabled:opacity-40"
            aria-label={t('platform.prototypeInput')}
          >
            {artifacts.prototypes.length === 0 && <option value="">{t('platform.noPrototype')}</option>}
            {artifacts.prototypes.map((prototype) => (
              <option key={prototype.artifact.id} value={prototype.artifact.id}>
                {prototype.artifact.title} · {prototype.approvedRevision ? t('platform.prototypeApproved', { revision: prototype.approvedRevision.revisionNumber }) : t('platform.prototypeNotApproved')}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={() => selectedPrototype && void flow.createBundle(selectedPrototype)}
            disabled={unavailable || !can('edit') || !selectedPrototype?.approvedRevision || flow.busy}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-40"
            title={t('platform.freezeTitle')}
          >
            <PackageCheck className="size-3.5" /> {t('platform.freeze')}
          </button>
          <button
            type="button"
            onClick={() => setShowRelease(true)}
            disabled={unavailable || !flow.workspaceRevision}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border bg-background px-3 text-[10px] font-semibold text-muted-foreground hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
            title={t('platform.releaseTitle')}
          >
            <Rocket className="size-3.5" /> {t('platform.release')}
          </button>
        </div>
      </header>

      <div className="flex min-h-0 flex-1 max-lg:flex-col">
        <FlowPanel />
        <main className="min-h-0 min-w-0 flex-1 bg-canvas p-3 max-lg:min-h-[520px]">
          <div className="h-full overflow-hidden rounded-lg border border-border bg-panel">
            {unavailable ? (
              <ServiceGate
                title={!session.signedIn ? t('platform.gate.signIn') : !project ? t('platform.gate.selectProject') : t('platform.gate.unavailable')}
                description={t('platform.gate.description')}
              />
            ) : flow.status === 'loading' ? (
              <ServiceGate loading title={t('platform.gate.loading')} description={t('platform.gate.loadingDescription')} />
            ) : flow.status === 'error' ? (
              <ServiceGate title={t('platform.gate.workflowUnavailable')} description={flow.error ?? t('platform.gate.workflowFallback')} onRetry={flow.refresh} />
            ) : view === 'preview' && project && sandboxBuildManifestId ? (
              <div className="flex h-full min-h-0 flex-col">
                <WorkbenchGroupTabs />
                <div className="min-h-0 flex-1">
                  <SandboxWorkspace
                    key={`${project.id}:${sandboxBuildManifestId}`}
                    mode={view}
                    projectId={project.id}
                    buildManifestId={sandboxBuildManifestId}
                  />
                </div>
              </div>
            ) : view === 'preview' ? (
              <PlatformPreview />
            ) : view === 'code' && project && sandboxBuildManifestId && candidateEntryAvailable ? (
              <div className="flex h-full min-h-0 flex-col">
                <WorkbenchGroupTabs />
                <div className="min-h-0 flex-1">
                  <SandboxWorkspace
                    key={`${project.id}:${sandboxBuildManifestId}:code`}
                    mode="code"
                    projectId={project.id}
                    buildManifestId={sandboxBuildManifestId}
                  />
                </div>
              </div>
            ) : view === 'code' ? (
              <ImplementationWorkspace />
            ) : (
              <DatabasePanel />
            )}
          </div>
        </main>
      </div>
      {showConversation && <ConversationPanel onClose={() => setShowConversation(false)} />}
      {showRelease && <ReleasePanel onClose={() => setShowRelease(false)} />}
    </div>
  )
}

function ImplementationWorkspace() {
  const { locale, t } = useI18n()
  const flow = usePlatformFlow()
  const { can } = useCollaboration()
  const files = flow.workspaceRevision?.content.files ?? []
  const [selectedPath, setSelectedPath] = useState('')
  const selectedFile = selectedPath
    ? files.find((item) => item.path === selectedPath)
    : files[0]
  const [draft, setDraft] = useState(selectedFile?.content ?? '')
  const [newPath, setNewPath] = useState('')
  const [showManifest, setShowManifest] = useState(true)
  const [buildContractGate, setBuildContractGate] = useState<BuildContractGateSnapshot>({
    bundleId: '',
    phase: 'loading',
    contract: null,
    ready: false,
    reason: 'missing',
  })
  const selectedQueueIndex = flow.workbenchQueue.findIndex(
    (item) => item.bundleId === flow.selectedBundleId,
  )
  const currentQueueItem = selectedQueueIndex >= 0
    ? flow.workbenchQueue[selectedQueueIndex]
    : undefined
  const orderedGenerationAllowed = workbenchQueueItemHasAppliedPredecessors(
    flow.workbenchQueue,
    selectedQueueIndex,
  )
  const blockingPredecessor = flow.workbenchQueue
    .slice(0, Math.max(selectedQueueIndex, 0))
    .find((item) => !proposalApplied(item.proposal))
  const buildContractReady = buildContractGate.bundleId === flow.bundle?.id
    && buildContractGate.ready
    && buildContractGate.contract?.buildManifestId === flow.bundle?.id
  const updateBuildContractGate = useCallback((next: BuildContractGateSnapshot) => {
    setBuildContractGate(next)
  }, [])
  const buildContractDisabledTitle = buildContractReady
    ? undefined
    : t('platform.buildContract.generationBlocked')

  useEffect(() => {
    if (!selectedPath && selectedFile) setSelectedPath(selectedFile.path)
    setDraft(selectedFile?.content ?? '')
  }, [selectedFile?.content, selectedFile?.path, selectedPath])

  if (!flow.bundle) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        <WorkbenchGroupTabs />
        <ServiceGate
          title={flow.workbenchGroups.length > 0 ? t('platform.bundle.loading') : t('platform.bundle.freezeFirst')}
          description={flow.workbenchGroups.length > 0 ? t('platform.bundle.loadingDescription') : t('platform.bundle.freezeDescription')}
        />
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <WorkbenchGroupTabs />
      <section className="shrink-0 border-b border-border bg-background/40 p-3">
        <div className="flex flex-wrap items-start gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <ShieldCheck className="size-4 text-success" />
              <h2 className="text-xs font-semibold text-foreground">{t('platform.manifest.title')}</h2>
              {flow.workbenchProgress.total > 1 && (
                <span className="rounded bg-primary/10 px-1.5 py-0.5 text-[8px] font-semibold text-primary-bright">
                  {t('platform.manifest.appliedProgress', { applied: formatNumber(flow.workbenchProgress.applied, locale), total: formatNumber(flow.workbenchProgress.total, locale) })}
                </span>
              )}
              <button type="button" onClick={() => setShowManifest((value) => !value)} className="rounded p-1 text-faint-foreground hover:text-foreground" aria-label={t('platform.manifest.toggle')}><ChevronDown className={cn('size-3 transition-transform', !showManifest && '-rotate-90')} /></button>
            </div>
            <p className="mt-1 truncate font-mono text-[9px] text-faint-foreground" title={flow.bundle.contentHash}>
              {currentQueueItem && flow.bundle.id !== currentQueueItem.bundleId
                ? `${currentQueueItem.bundleId} → ${flow.bundle.id}`
                : flow.bundle.id} · {flow.bundle.contentHash}
            </p>
          </div>
          <div role="status" className="flex min-w-[320px] flex-1 items-center gap-2 rounded-md border border-primary/25 bg-primary/5 px-3 py-2 text-[9px] leading-relaxed text-muted-foreground max-md:min-w-0 max-md:basis-full">
            <ShieldCheck className="size-3.5 shrink-0 text-primary-bright" />
            AI changes are created in the durable Candidate sandbox, verified against the exact checkpoint, then frozen into this immutable Proposal for review.
          </div>
        </div>
        {!orderedGenerationAllowed && currentQueueItem && blockingPredecessor && (
          <div role="status" className="mt-3 flex items-center gap-2 rounded-md border border-warning/35 bg-warning/10 px-3 py-2 text-[9px] leading-relaxed text-warning">
            <CircleAlert className="size-4 shrink-0" />
            <span>{t('platform.orderBlocked', { predecessor: blockingPredecessor.sliceId ?? blockingPredecessor.bundleId, current: currentQueueItem.sliceId ?? currentQueueItem.bundleId })}</span>
          </div>
        )}
        {flow.requiresWorkbenchRebase && flow.workspaceRevision && currentQueueItem && (
          <div role="status" className="mt-3 flex items-center gap-3 rounded-md border border-warning/35 bg-warning/10 px-3 py-2">
            <GitCompareArrows className="size-4 shrink-0 text-warning" />
            <p className="min-w-0 flex-1 text-[9px] leading-relaxed text-warning">
              {t('platform.rebaseDescription', { bundle: flow.bundle.id, root: currentQueueItem.bundleId, revision: formatNumber(flow.workspaceRevision.revisionNumber, locale), workspace: flow.workspaceRevision.id })}
            </p>
            <button type="button" onClick={() => void flow.rebaseWorkbenchBundle()} disabled={!can('edit') || flow.busy} className="inline-flex h-7 shrink-0 items-center gap-1 rounded bg-warning px-2 text-[9px] font-semibold text-black disabled:opacity-40">
              <GitCompareArrows className="size-3" /> {t('platform.rebaseNext')}
            </button>
          </div>
        )}
        {flow.workbenchQueue.length > 1 && (
          <div className="mt-3 flex items-center gap-1.5 overflow-x-auto pb-0.5 scrollbar-thin" aria-label={t('platform.pageQueue')}>
            <span className="mr-1 shrink-0 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">{t('platform.manifestOrder')}</span>
            {flow.workbenchQueue.map((item, index) => {
              const state = workbenchQueueItemState(
                flow.workbenchQueue,
                index,
                flow.workspaceRevision,
              )
              return (
                <button
                  key={item.bundleId}
                  type="button"
                  onClick={() => flow.selectWorkbenchBundle(item.bundleId)}
                  className={cn(
                    'inline-flex h-7 shrink-0 items-center gap-1.5 rounded border px-2 text-[9px]',
                    item.bundleId === flow.selectedBundleId
                      ? 'border-primary/50 bg-primary/10 text-primary-bright'
                      : 'border-border bg-panel text-muted-foreground hover:text-foreground',
                  )}
                  title={`${item.sliceId ?? item.bundleId} · ${platformStatusLabel(state, t)}`}
                >
                  <span className="font-semibold">{formatNumber(index + 1, locale)}</span>
                  <span className="max-w-32 truncate">{item.sliceId ?? t('platform.pageNumber', { number: formatNumber(index + 1, locale) })}</span>
                  <span className={cn(
                    'rounded px-1 py-0.5 text-[7px] font-semibold uppercase',
                    state === 'applied'
                      ? 'bg-success/15 text-success'
                      : state === 'ready'
                        ? 'bg-primary/15 text-primary-bright'
                        : 'bg-warning/10 text-warning',
                  )}>{platformStatusLabel(state, t)}</span>
                </button>
              )
            })}
          </div>
        )}
        {showManifest && <ManifestFacts />}
        <BuildContractPanel
          bundleId={flow.bundle.id}
          canCompile={can('edit') && !flow.busy}
          onGateChange={updateBuildContractGate}
        />
      </section>

      <div className="flex min-h-0 flex-1 max-md:flex-col">
        <aside className="flex w-56 shrink-0 flex-col border-r border-border bg-panel max-md:h-40 max-md:w-full max-md:border-b max-md:border-r-0">
          <div className="flex h-9 items-center gap-2 border-b border-border px-2.5 text-[10px] font-semibold text-faint-foreground">
            <FileCode2 className="size-3.5" /> {t('platform.workspace.applied')}
            <span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 font-mono text-[8px]">{formatNumber(files.length, locale)}</span>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-1.5 scrollbar-thin">
            {files.length === 0 && <p className="p-3 text-center text-[9px] leading-relaxed text-faint-foreground">{t('platform.workspace.empty')}</p>}
            {files.map((file) => (
              <button key={file.path} type="button" onClick={() => setSelectedPath(file.path)} className={cn('mb-0.5 flex w-full items-center gap-1.5 rounded px-2 py-1.5 text-left text-[10px]', selectedFile?.path === file.path ? 'bg-primary/10 text-primary-bright' : 'text-muted-foreground hover:bg-white/5 hover:text-foreground')}>
                <FileText className="size-3 shrink-0" /><span className="truncate">{file.path}</span>
              </button>
            ))}
          </div>
          <div className="border-t border-border p-2">
            <div className="flex gap-1">
              <input value={newPath} onChange={(event) => setNewPath(event.target.value)} placeholder={t('platform.newFilePlaceholder')} className="h-7 min-w-0 flex-1 rounded border border-border bg-background px-1.5 font-mono text-[9px] text-foreground outline-none" />
              <button type="button" onClick={() => { if (!newPath.trim()) return; setSelectedPath(newPath.trim()); setDraft(''); }} disabled={!can('edit')} className="flex size-7 items-center justify-center rounded border border-border text-faint-foreground hover:text-foreground disabled:opacity-40" aria-label={t('platform.prepareNewFile')}><FilePlus2 className="size-3" /></button>
            </div>
          </div>
        </aside>

        <section className="flex min-w-0 flex-1 flex-col bg-background">
          <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3 text-[10px] text-muted-foreground">
            <FileCode2 className="size-3 text-primary-bright" />
            <span className="min-w-0 flex-1 truncate font-mono">{selectedPath || selectedFile?.path || t('platform.selectWorkspaceFile')}</span>
            {(selectedPath || selectedFile) && (
              <button
                type="button"
                onClick={() => {
                  const contract = buildContractGate.contract
                  if (!buildContractReady || !contract || contract.buildManifestId !== flow.bundle?.id) return
                  void flow.proposeFileChange(
                    selectedPath || selectedFile!.path,
                    draft,
                    { id: contract.id, contractHash: contract.contractHash },
                    contract.buildManifestId,
                    selectedFile?.language,
                    selectedFile?.contentHash,
                  )
                }}
                disabled={!can('edit') || flow.busy || !buildContractReady || !orderedGenerationAllowed || (!selectedPath && !selectedFile) || draft === selectedFile?.content}
                className="inline-flex h-7 items-center gap-1 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground disabled:opacity-35"
                title={buildContractDisabledTitle ?? (orderedGenerationAllowed ? t('platform.proposeTitle') : t('platform.proposeBlockedTitle'))}
              >
                <Save className="size-3" /> {t('platform.proposeChange')}
              </button>
            )}
          </div>
          {selectedPath || selectedFile ? (
            <textarea
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              readOnly={!can('edit')}
              spellCheck={false}
              className="min-h-0 flex-1 resize-none bg-background p-4 font-mono text-[11px] leading-5 text-muted-foreground outline-none read-only:opacity-70"
              aria-label={t('platform.proposalEditor')}
            />
          ) : (
            <div className="flex flex-1 items-center justify-center text-[10px] text-faint-foreground">{t('platform.workspace.applyFirst')}</div>
          )}
        </section>

        <ProposalReview />
      </div>
    </div>
  )
}

function ManifestFacts() {
  const { locale, t } = useI18n()
  const { bundle } = usePlatformFlow()
  if (!bundle) return null
  const facts = [
    [t('platform.fact.requirements'), formatNumber(bundle.requirementRevisions.length, locale)],
    [t('platform.fact.blueprint'), compactRef(bundle.blueprintRevision)],
    [t('platform.fact.pageSpec'), compactRef(bundle.pageSpecRevision)],
    [t('platform.fact.prototype'), compactRef(bundle.prototypeRevision)],
    [t('platform.fact.contracts'), formatNumber(bundle.contractRevisions.length, locale)],
    [t('platform.fact.designSystem'), formatNumber(bundle.designSystemRevisions.length, locale)],
    [t('platform.fact.context'), formatNumber(bundle.contextRevisions?.length ?? 0, locale)],
    [t('platform.fact.workflowInput'), bundle.workflowContext ? `${bundle.workflowContext.inputManifest.jobType} · ${t('platform.fact.sources', { count: formatNumber(bundle.workflowContext.inputManifest.sources.length, locale) })}` : t('platform.fact.legacyManual')],
    [t('platform.fact.renderedStates'), formatNumber(bundle.renderedFrames.length, locale)],
  ] as const
  return (
    <>
      <div className="mt-3 grid grid-cols-4 gap-1.5 max-xl:grid-cols-2 max-md:grid-cols-1">
        {facts.map(([label, value]) => (
          <div key={label} className="rounded border border-border bg-panel px-2 py-1.5">
            <div className="text-[8px] uppercase tracking-wider text-faint-foreground">{label}</div>
            <div className="mt-0.5 truncate font-mono text-[9px] text-muted-foreground" title={String(value)}>{value}</div>
          </div>
        ))}
      </div>
      {((bundle.contextRevisions?.length ?? 0) > 0 || bundle.workflowContext) && (
        <details className="mt-2 rounded border border-border bg-panel">
          <summary className="cursor-pointer px-2 py-1.5 text-[8px] font-semibold text-faint-foreground">{t('platform.inspectContext')}</summary>
          <div className="space-y-1 border-t border-border p-2 font-mono text-[8px] text-muted-foreground">
            {bundle.contextRevisions?.map((context) => (
              <div key={`${context.kind}:${context.revision.revisionId}:${context.revision.anchorId ?? ''}`} className="truncate" title={JSON.stringify(context.revision)}>
                {t('platform.context')}:{context.kind} · {compactRef(context.revision)}{context.revision.anchorId ? `#${context.revision.anchorId}` : ''}
              </div>
            ))}
            {bundle.workflowContext && (
              <>
                <div className="truncate" title={bundle.workflowContext.definition.hash}>{t('platform.definition')} · {bundle.workflowContext.definition.id}@v{formatNumber(bundle.workflowContext.definition.version, locale)}</div>
                <div className="truncate" title={bundle.workflowContext.inputManifest.hash}>{t('platform.inputManifest')} · {bundle.workflowContext.inputManifest.id} · {bundle.workflowContext.inputManifest.jobType}/{bundle.workflowContext.inputManifest.outputSchemaVersion}</div>
                {bundle.workflowContext.inputManifest.baseRevision && <div className="truncate" title={JSON.stringify(bundle.workflowContext.inputManifest.baseRevision)}>{t('platform.base')} · {compactRef(bundle.workflowContext.inputManifest.baseRevision)}</div>}
                {bundle.workflowContext.inputManifest.sources.map((source, index) => (
                  <div key={`${source.purpose}:${source.ref.revisionId}:${source.ref.anchorId ?? ''}:${index}`} className="truncate" title={JSON.stringify(source.ref)}>
                    {source.purpose} · {compactRef(source.ref)}{source.ref.anchorId ? `#${source.ref.anchorId}` : ''}
                  </div>
                ))}
                <pre className="max-h-28 overflow-auto whitespace-pre-wrap rounded bg-black/20 p-1.5">{t('platform.runScope')} {JSON.stringify(bundle.workflowContext.runScope ?? {}, null, 2)}</pre>
              </>
            )}
          </div>
        </details>
      )}
    </>
  )
}

function ProposalReview() {
  const { locale, t } = useI18n()
  const flow = usePlatformFlow()
  const { can } = useCollaboration()
  const [rejectionReason, setRejectionReason] = useState(() => t('platform.rejection.default'))
  const [quarantineReason, setQuarantineReason] = useState('Replace this unreviewable Proposal with a governed verified Candidate.')
  const proposal = flow.proposal
  const queueIndex = flow.workbenchQueue.findIndex(
    (item) => item.bundleId === flow.selectedBundleId,
  )
  const queueTotal = flow.workbenchQueue.length
  const candidateSource = proposal?.candidateSource
  const unverifiedCandidateProposal = proposal?.executionSource === 'candidate_freeze'
    && (!candidateSource?.verificationReceipt.id || !candidateSource.verificationReceipt.contentHash)
  const exactCandidate = unverifiedCandidateProposal ? undefined : candidateSource
  const exactCandidateFullyAccepted = Boolean(
    proposal
    && proposal.operations.length > 0
    && proposal.operations.every(
      (operation) => operation.decision === 'accepted' || operation.decision === 'applied',
    ),
  )
  const legacyAIProposal = proposal?.executionSource === 'manual_generation'
    || proposal?.executionSource === 'workflow_runner'
    || proposal?.executionSource === 'conversation_command'
  const completionBlockers = proposal
    ? proposal.unimplementedItems.length
      + proposal.diagnostics.filter((finding) => finding.severity === 'blocker').length
    : 0
  const governedReviewBlocked = Boolean(legacyAIProposal || unverifiedCandidateProposal || completionBlockers > 0)
  const quarantineAvailable = Boolean(
    legacyAIProposal
    || unverifiedCandidateProposal
    || proposal?.executionSource === 'manual_submission' && completionBlockers > 0,
  )
  const isLastQueueItem = queueIndex >= 0 && queueIndex === queueTotal - 1
  const applyLabel = flow.canCompleteWorkbench
    ? t('platform.apply.complete')
    : queueTotal > 1
      ? isLastQueueItem ? t('platform.apply.andComplete') : t('platform.apply.andContinue')
      : t('platform.apply.accepted')
  const applyDescription = flow.canCompleteWorkbench
    ? t('platform.apply.completeDescription')
    : flow.requiresWorkbenchRebase
      ? t('platform.apply.rebaseDescription')
      : proposal?.status === 'ready' && !flow.canApplyProposal
        ? t('platform.apply.orderDescription')
        : queueTotal > 1
          ? t('platform.apply.continueDescription')
          : t('platform.apply.singleDescription')

  return (
    <aside className="flex w-[330px] shrink-0 flex-col border-l border-border bg-panel max-xl:w-72 max-md:h-80 max-md:w-full max-md:border-l-0 max-md:border-t">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-2.5">
        <GitCompareArrows className="size-3.5 text-primary-bright" />
        <span className="text-[10px] font-semibold text-foreground">{t('platform.proposal.title')}</span>
        {queueTotal > 1 && queueIndex >= 0 && (
          <span className="ml-auto text-[8px] font-semibold text-faint-foreground">{formatNumber(queueIndex + 1, locale)}/{formatNumber(queueTotal, locale)}</span>
        )}
        {proposal && <span className={cn('rounded bg-white/5 px-1.5 py-0.5 font-mono text-[8px] text-faint-foreground', queueTotal <= 1 && 'ml-auto')}>v{proposal.version}</span>}
      </div>
      {!proposal ? (
        <div className="flex flex-1 items-center justify-center p-4 text-center text-[9px] leading-relaxed text-faint-foreground">
          {t('platform.proposal.empty')}
        </div>
      ) : (
        <>
          <div className="border-b border-border p-2">
            <div className="flex items-center gap-2 text-[9px]">
              <span className={cn('rounded px-1.5 py-0.5 font-semibold', proposal.status === 'ready' ? 'bg-success/15 text-success' : proposal.status.includes('applied') ? 'bg-primary/15 text-primary-bright' : 'bg-warning/15 text-warning')}>{platformStatusLabel(proposal.status, t)}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-faint-foreground" title={proposal.payloadHash}>{proposal.payloadHash}</span>
            </div>
            <div className="mt-2 grid grid-cols-2 gap-1">
              <button type="button" onClick={() => void flow.decideAllPending('accepted')} disabled={!can('edit') || flow.busy || flow.requiresWorkbenchRebase || governedReviewBlocked || proposal.operations.every((operation) => operation.decision !== 'pending')} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[9px] font-medium text-success disabled:opacity-35"><Check className="size-3" /> {t('platform.proposal.acceptPending')}</button>
              <button type="button" onClick={() => void flow.decideAllPending('rejected', rejectionReason)} disabled={!can('edit') || flow.busy || flow.requiresWorkbenchRebase || governedReviewBlocked || proposal.operations.every((operation) => operation.decision !== 'pending')} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-destructive/10 text-[9px] font-medium text-destructive disabled:opacity-35"><X className="size-3" /> {t('platform.proposal.rejectPending')}</button>
            </div>
            <input value={rejectionReason} onChange={(event) => setRejectionReason(event.target.value)} className="mt-1.5 h-7 w-full rounded border border-border bg-background px-2 text-[9px] text-foreground outline-none" aria-label={t('platform.proposal.rejectionReason')} />
          </div>
          <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-2 scrollbar-thin">
            {exactCandidate && (
              <section className="rounded-md border border-primary/30 bg-primary/10 p-2 text-[8px] leading-relaxed text-primary-bright">
                <div className="flex items-center gap-1.5 font-semibold">
                  <PackageCheck className="size-3" /> Exact frozen Candidate
                </div>
                <p className="mt-1 text-faint-foreground">
                  This Proposal is the complete immutable diff for Candidate {exactCandidate.candidateId.slice(0, 12)}.
                  Every operation must be accepted before Apply can create its WorkspaceRevision.
                </p>
                <p className="mt-1 truncate font-mono text-faint-foreground" title={exactCandidate.treeHash}>
                  C{exactCandidate.candidateVersion} · J{exactCandidate.journalSequence} · {exactCandidate.treeHash}
                </p>
              </section>
            )}
            {governedReviewBlocked && (
              <section role="alert" className="rounded-md border border-destructive/35 bg-destructive/10 p-2 text-[8px] leading-relaxed text-destructive">
                <div className="flex items-center gap-1.5 font-semibold">
                  <CircleAlert className="size-3" /> Proposal cannot enter the approval gate
                </div>
                <p className="mt-1">
                  {legacyAIProposal
                    ? 'This Proposal came from the retired direct-model path and has no exact Candidate VerificationReceipt. Quarantine it and create a governed Candidate instead.'
                    : unverifiedCandidateProposal
                      ? 'This historical Candidate Proposal predates the exact VerificationReceipt gate. It cannot be approved or repaired in place; quarantine it and freeze a new verified Candidate.'
                      : `This immutable manual Proposal contains ${completionBlockers} unimplemented or blocking diagnostic item(s). Quarantine it and resolve them in a governed Candidate before freezing a replacement.`}
                </p>
                {quarantineAvailable && (
                  <div className="mt-2 flex gap-1.5">
                    <input
                      value={quarantineReason}
                      onChange={(event) => setQuarantineReason(event.target.value)}
                      maxLength={1000}
                      aria-label="Proposal quarantine reason"
                      className="h-7 min-w-0 flex-1 rounded border border-destructive/30 bg-background px-2 text-[8px] text-foreground outline-none"
                    />
                    <button
                      type="button"
                      onClick={() => void flow.quarantineProposal(quarantineReason)}
                      disabled={!can('edit') || flow.busy || !quarantineReason.trim()}
                      className="h-7 shrink-0 rounded bg-destructive px-2 text-[8px] font-semibold text-destructive-foreground disabled:opacity-35"
                    >
                      Quarantine and open Candidate
                    </button>
                  </div>
                )}
              </section>
            )}
            <ProposalOutputSummary proposal={proposal} />
            <div className="flex items-center gap-2 px-0.5 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">
              <span>{t('platform.proposal.fileOperations')}</span>
              <span className="ml-auto rounded bg-white/5 px-1 py-0.5 font-mono">{formatNumber(proposal.operations.length, locale)}</span>
            </div>
            {proposal.operations.map((operation) => <OperationCard key={operation.id} operation={operation} />)}
          </div>
          <div className="border-t border-border p-2">
            {proposal.unimplementedItems.length > 0 && <p className="mb-2 text-[8px] leading-relaxed text-warning">{t('platform.proposal.unimplemented', { items: proposal.unimplementedItems.join(' · ') })}</p>}
            <button type="button" onClick={() => void flow.applyProposal()} disabled={!can('edit') || flow.busy || governedReviewBlocked || (Boolean(exactCandidate) && !exactCandidateFullyAccepted) || (!flow.canCompleteWorkbench && (proposal.status !== 'ready' || !flow.canApplyProposal))} className="inline-flex h-8 w-full items-center justify-center gap-1.5 rounded bg-primary text-[10px] font-semibold text-primary-foreground disabled:cursor-not-allowed disabled:opacity-35">
              {flow.busy ? <LoaderCircle className="size-3 animate-spin" /> : <Play className="size-3" />} {applyLabel}
            </button>
            <p className="mt-1.5 text-[8px] leading-relaxed text-faint-foreground">
              {governedReviewBlocked
                ? 'This Proposal has no admissible completion evidence and cannot create an immutable revision.'
                : exactCandidate && !exactCandidateFullyAccepted
                ? 'Accept every exact file operation to enable immutable revision creation.'
                : applyDescription}
            </p>
          </div>
        </>
      )}
    </aside>
  )
}

function ProposalOutputSummary({ proposal }: { proposal: ImplementationProposalDto }) {
  const { locale, t } = useI18n()
  const sections: readonly { readonly label: string; readonly values: readonly unknown[] }[] = [
    { label: t('platform.output.routes'), values: proposal.routes },
    { label: t('platform.output.apis'), values: proposal.apis },
    { label: t('platform.output.migrations'), values: proposal.migrations },
    { label: t('platform.output.tests'), values: proposal.tests },
    { label: t('platform.output.previews'), values: proposal.previews },
    { label: t('platform.output.traceLinks'), values: proposal.traceLinks },
    { label: t('platform.output.diagnostics'), values: proposal.diagnostics },
    { label: t('platform.output.assumptions'), values: proposal.assumptions },
    { label: t('platform.output.unimplemented'), values: proposal.unimplementedItems },
  ]

  return (
    <section className="rounded-md border border-border bg-background p-2" aria-label={t('platform.output.summaryAria')}>
      <div className="mb-1.5 flex items-center gap-2 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">
        <PackageCheck className="size-3 text-primary-bright" /> {t('platform.output.contract')}
      </div>
      <div className="grid grid-cols-2 gap-1">
        {sections.map((section) => (
          <details key={section.label} className="group rounded border border-border/70 bg-panel open:col-span-2">
            <summary className="flex cursor-pointer list-none items-center gap-1 px-1.5 py-1 text-[8px] text-muted-foreground hover:text-foreground">
              <ChevronDown className="size-2.5 -rotate-90 transition-transform group-open:rotate-0" />
              <span className="min-w-0 flex-1 truncate">{section.label}</span>
              <span className="rounded bg-white/5 px-1 font-mono text-[7px] text-faint-foreground">{formatNumber(section.values.length, locale)}</span>
            </summary>
            <div className="space-y-1 border-t border-border/70 p-1.5">
              {section.values.length === 0 ? (
                <p className="text-[8px] text-faint-foreground">{t('platform.output.none')}</p>
              ) : section.values.map((value, index) => (
                <pre key={index} className="max-h-32 overflow-auto whitespace-pre-wrap break-words rounded bg-black/20 p-1.5 font-mono text-[8px] leading-relaxed text-faint-foreground scrollbar-thin">
                  {proposalSummaryValue(value)}
                </pre>
              ))}
            </div>
          </details>
        ))}
      </div>
    </section>
  )
}

function OperationCard({ operation }: { operation: FileOperationDto }) {
  const { t } = useI18n()
  const flow = usePlatformFlow()
  const { can } = useCollaboration()
  const [expanded, setExpanded] = useState(false)
  return (
    <article className="rounded-md border border-border bg-background p-2">
      <button type="button" onClick={() => setExpanded((value) => !value)} className="flex w-full items-start gap-2 text-left">
        <OperationIcon kind={operation.kind} />
        <span className="min-w-0 flex-1">
          <span className="block truncate font-mono text-[9px] text-foreground">{operation.path}</span>
          <span className="mt-0.5 block text-[8px] text-faint-foreground">{operationKindLabel(operation.kind, t)} · {platformStatusLabel(operation.decision, t)}</span>
        </span>
        <ChevronDown className={cn('size-3 text-faint-foreground transition-transform', !expanded && '-rotate-90')} />
      </button>
      {expanded && (
        <div className="mt-2 border-t border-border pt-2">
          {operation.rationale && <p className="mb-2 text-[8px] leading-relaxed text-muted-foreground">{operation.rationale}</p>}
          {operation.content !== undefined && <pre className="max-h-36 overflow-auto whitespace-pre-wrap rounded bg-black/20 p-2 font-mono text-[8px] leading-relaxed text-faint-foreground scrollbar-thin">{operation.content}</pre>}
          {operation.decision === 'pending' && (
            <div className="mt-2 grid grid-cols-2 gap-1">
              <button type="button" onClick={() => void flow.decideOperation(operation, 'accepted')} disabled={!can('edit') || flow.busy || flow.requiresWorkbenchRebase} className="inline-flex h-6 items-center justify-center gap-1 rounded bg-success/15 text-[8px] font-medium text-success disabled:opacity-35"><Check className="size-2.5" />{t('platform.operation.accept')}</button>
              <button type="button" onClick={() => void flow.decideOperation(operation, 'rejected', t('platform.operation.rejection'))} disabled={!can('edit') || flow.busy || flow.requiresWorkbenchRebase} className="inline-flex h-6 items-center justify-center gap-1 rounded bg-destructive/10 text-[8px] font-medium text-destructive disabled:opacity-35"><X className="size-2.5" />{t('platform.operation.reject')}</button>
            </div>
          )}
        </div>
      )}
    </article>
  )
}

function PlatformPreview() {
  const { t } = useI18n()
  const flow = usePlatformFlow()
  const files = flow.workspaceRevision?.content.files ?? []
  const [viewport, setViewport] = useState<'desktop' | 'tablet' | 'mobile'>('desktop')
  const preview = useMemo(() => previewDocument(files), [files])
  const width = viewport === 'desktop' ? '100%' : viewport === 'tablet' ? 768 : 390

  if (!flow.workspaceRevision) {
    return <ServiceGate title={t('platform.preview.noRevision')} description={t('platform.preview.noRevisionDescription')} />
  }
  if (!preview) {
    return <ServiceGate title={t('platform.preview.notFound')} description={t('platform.preview.notFoundDescription')} />
  }

  return (
    <div className="flex h-full flex-col bg-canvas">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-border bg-panel px-3">
        <Eye className="size-3.5 text-primary-bright" />
        <span className="text-[10px] font-semibold text-foreground">{t('platform.preview.title')}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-[8px] text-faint-foreground" title={flow.workspaceRevision.contentHash}>{flow.workspaceRevision.id} · {flow.workspaceRevision.contentHash}</span>
        {(['desktop', 'tablet', 'mobile'] as const).map((item) => (
          <button key={item} type="button" onClick={() => setViewport(item)} className={cn('rounded px-2 py-1 text-[9px]', viewport === item ? 'bg-primary/15 text-primary-bright' : 'text-faint-foreground hover:text-foreground')}>{viewportLabel(item, t)}</button>
        ))}
      </div>
      <div className="flex min-h-0 flex-1 justify-center overflow-auto bg-[#08080a] p-3 scrollbar-thin">
        <iframe
          title={t('platform.preview.iframeTitle')}
          srcDoc={preview}
          sandbox="allow-forms allow-modals allow-popups allow-scripts"
          className="h-full rounded-md border border-border bg-white shadow-2xl transition-[width]"
          style={{ width }}
        />
      </div>
    </div>
  )
}

function ServiceGate({ title, description, loading, onRetry }: { title: string; description: string; loading?: boolean; onRetry?: () => Promise<void> }) {
  const { t } = useI18n()
  return (
    <div className="flex h-full items-center justify-center bg-canvas p-6">
      <div className="max-w-lg rounded-xl border border-dashed border-border bg-panel p-7 text-center">
        {loading ? <LoaderCircle className="mx-auto mb-3 size-6 animate-spin text-primary-bright" /> : <CircleAlert className="mx-auto mb-3 size-6 text-faint-foreground" />}
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        <p className="mt-2 text-[10px] leading-relaxed text-faint-foreground">{description}</p>
        {onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground"><RefreshCw className="size-3" /> {t('platform.retryServer')}</button>}
      </div>
    </div>
  )
}

function WorkbenchGroupTabs() {
  const { locale, t } = useI18n()
  const flow = usePlatformFlow()
  if (flow.workbenchGroups.length <= 1) return null
  return (
    <div className="flex shrink-0 items-center gap-1.5 overflow-x-auto border-b border-border bg-background/60 px-3 py-2 scrollbar-thin" aria-label={t('platform.groups.aria')}>
      <span className="mr-1 shrink-0 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">{t('platform.groups.label')}</span>
      {flow.workbenchGroups.map((group, index) => (
        <button
          key={group.nodeKey}
          type="button"
          onClick={() => void flow.selectWorkbenchGroup(group.nodeKey)}
          className={cn(
            'inline-flex h-7 shrink-0 items-center gap-1.5 rounded border px-2 text-[9px]',
            group.nodeKey === flow.selectedWorkbenchNodeKey
              ? 'border-primary/50 bg-primary/10 text-primary-bright'
              : 'border-border bg-panel text-muted-foreground hover:text-foreground',
          )}
          aria-label={t('platform.groups.itemAria', { group: group.nodeKey, status: platformStatusLabel(group.status, t) })}
          title={t('platform.groups.title', { group: group.manifestGroupKey ?? group.nodeKey, count: formatNumber(group.references.length, locale) })}
        >
          <span className="font-semibold">{formatNumber(index + 1, locale)}</span>
          <span>{group.sliceId ?? group.nodeKey}</span>
          <span className="rounded bg-white/5 px-1 py-0.5 text-[7px] uppercase">{platformStatusLabel(group.status, t)}</span>
        </button>
      ))}
    </div>
  )
}

function OperationIcon({ kind }: { kind: FileOperationDto['kind'] }) {
  if (kind === 'file.delete') return <X className="mt-0.5 size-3 shrink-0 text-destructive" />
  if (kind === 'file.rename') return <PencilLine className="mt-0.5 size-3 shrink-0 text-warning" />
  return <FilePlus2 className="mt-0.5 size-3 shrink-0 text-success" />
}

function compactRef(ref: { artifactId: string; revisionId: string }) {
  return `${ref.artifactId.slice(0, 8)}:${ref.revisionId.slice(0, 8)}`
}

function proposalApplied(proposal: ImplementationProposalDto | null | undefined) {
  return proposal?.status === 'applied' || proposal?.status === 'partially_applied'
}

function workbenchQueueItemState(
  queue: readonly WorkbenchQueueItem[],
  index: number,
  workspace: WorkspaceRevisionDto | null,
) {
  const item = queue[index]
  if (proposalApplied(item.proposal)) return 'applied'
  if (!workbenchQueueItemHasAppliedPredecessors(queue, index)) return 'blocked'
  if (workbenchBundleNeedsRebase(item.bundle, workspace)) return 'rebase'
  if (!item.proposal) return 'generate'
  if (item.proposal.status === 'ready') return 'ready'
  if (item.proposal.status === 'stale') return 'stale'
  return 'review'
}

function proposalSummaryValue(value: unknown) {
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

function viewLabel(view: WorkbenchView, t: ReturnType<typeof useI18n>['t']) {
  if (view === 'preview') return t('platform.view.preview')
  if (view === 'code') return t('platform.view.implementation')
  return t('platform.view.data')
}

function viewportLabel(viewport: 'desktop' | 'tablet' | 'mobile', t: ReturnType<typeof useI18n>['t']) {
  if (viewport === 'desktop') return t('workbenchPlatform.viewport.desktop')
  if (viewport === 'tablet') return t('workbenchPlatform.viewport.tablet')
  return t('workbenchPlatform.viewport.mobile')
}

function operationKindLabel(kind: FileOperationDto['kind'], t: ReturnType<typeof useI18n>['t']) {
  if (kind === 'file.delete') return t('platform.operation.delete')
  if (kind === 'file.rename') return t('platform.operation.rename')
  return t('platform.operation.upsert')
}

function platformStatusLabel(status: string, t: ReturnType<typeof useI18n>['t']) {
  const normalized = status.toLowerCase().replaceAll('-', '_')
  if (normalized === 'active') return t('workbenchPlatform.status.active')
  if (normalized === 'archived') return t('workbenchPlatform.status.archived')
  if (normalized === 'pending') return t('workbenchPlatform.status.pending')
  if (normalized === 'pending_review') return t('workbenchPlatform.status.pendingReview')
  if (normalized === 'approved') return t('workbenchPlatform.status.approved')
  if (normalized === 'rejected') return t('workbenchPlatform.status.rejected')
  if (normalized === 'executed') return t('workbenchPlatform.status.executed')
  if (normalized === 'failed') return t('workbenchPlatform.status.failed')
  if (normalized === 'open') return t('workbenchPlatform.status.open')
  if (normalized === 'ready') return t('workbenchPlatform.status.ready')
  if (normalized === 'stale') return t('workbenchPlatform.status.stale')
  if (normalized === 'applied') return t('workbenchPlatform.status.applied')
  if (normalized === 'partially_applied') return t('workbenchPlatform.status.partiallyApplied')
  if (normalized === 'blocked') return t('workbenchPlatform.status.blocked')
  if (normalized === 'rebase') return t('workbenchPlatform.status.rebase')
  if (normalized === 'generate') return t('workbenchPlatform.status.generate')
  if (normalized === 'review') return t('workbenchPlatform.status.review')
  if (normalized === 'accepted') return t('workbenchPlatform.status.accepted')
  return status.replaceAll('_', ' ')
}

function formatNumber(value: number, locale: string) {
  return new Intl.NumberFormat(locale).format(value)
}

function previewDocument(files: readonly { path: string; content: string }[]) {
  const html = files.find((file) => /(^|\/)index\.html$/i.test(file.path))
    ?? files.find((file) => file.path.toLowerCase().endsWith('.html'))
  if (!html) return null
  const css = files.filter((file) => file.path.toLowerCase().endsWith('.css')).map((file) => file.content).join('\n')
  const script = files.filter((file) => /\.(m?js)$/i.test(file.path)).map((file) => file.content).join('\n')
  return html.content
    .replace('</head>', `<style>${css.replaceAll('</style>', '<\\/style>')}</style></head>`)
    .replace('</body>', `<script>${script.replaceAll('</script>', '<\\/script>')}</script></body>`)
}
