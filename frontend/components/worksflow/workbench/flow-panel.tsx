'use client'

import { useMemo, useState } from 'react'
import {
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  GitBranch,
  History as HistoryIcon,
  PencilLine,
  Play,
  RefreshCw,
  RotateCcw,
  Save,
  Send,
  ShieldCheck,
  Square,
  UploadCloud,
  Workflow,
  X,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n } from '@/lib/i18n'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import { useWorksflow } from '@/lib/worksflow/store'
import type {
  CreateWorkflowDefinitionInputDto,
  WorkflowDefinitionRecordDto,
  WorkflowCapabilitiesDto,
  WorkflowNodeRunDto,
} from '@/lib/platform/flow-contract'
import {
  exactArtifactRefsEqual,
  parseEditableDefinition as parseWorkflowContract,
  reviewGateApprovalReadiness,
  resolveCandidateSelection as resolveLineageSelection,
  revisionCandidates as resolveLineageCandidates,
  starterWorkflowDefinition,
  workflowRoleSatisfies,
} from '@/lib/platform/workflow-ui-contract'
import { cn } from '@/lib/utils'
import { requiresSoloReviewConfirmation } from '@/lib/worksflow/project-governance'
import { WorkflowGraphEditor } from './workflow-graph-editor'

type EditorMode = 'closed' | 'create' | 'version'

export function FlowPanel() {
  const flow = usePlatformFlow()
  const { can, session, project } = useCollaboration()
  const { locale, t } = useI18n()
  const [expanded, setExpanded] = useState(true)
  const [editorMode, setEditorMode] = useState<EditorMode>('closed')
  const [draftKey, setDraftKey] = useState('custom-application-flow')
  const [draftTitle, setDraftTitle] = useState('')
  const [draftDescription, setDraftDescription] = useState('')
  const [definitionJSON, setDefinitionJSON] = useState('{}')
  const [editorError, setEditorError] = useState<string | null>(null)
  const [selectedVersionId, setSelectedVersionId] = useState('')

  const selectedVersion = flow.definitionVersions.find((item) => item.versionId === selectedVersionId)
    ?? flow.definitionVersions.find((item) => item.published)
    ?? flow.definitionVersions[0]
    ?? flow.selectedDefinition

  function openEditor(mode: Exclude<EditorMode, 'closed'>) {
    const source = selectedVersion?.definition
    setEditorMode(mode)
    setEditorError(null)
    if (mode === 'version' && source) {
      setDefinitionJSON(JSON.stringify({
        name: source.name,
        schemaVersion: source.schemaVersion,
        inputContract: source.inputContract ?? flow.capabilities?.inputContracts.at(0),
        outputContract: source.outputContract ?? flow.capabilities?.outputContracts.at(0),
        nodes: source.nodes,
        edges: source.edges,
      }, null, 2))
      return
    }
    setDraftTitle(t('flow.defaultTitle'))
    setDraftDescription(t('flow.defaultDescription'))
    setDefinitionJSON(JSON.stringify(starterWorkflowDefinition(), null, 2))
  }

  async function saveDefinition() {
    setEditorError(null)
    try {
      const parsed = parseDefinitionJSON(definitionJSON, flow.capabilities, t)
      if (editorMode === 'create') {
        const input: CreateWorkflowDefinitionInputDto = {
          key: draftKey.trim(),
          title: draftTitle.trim(),
          description: draftDescription.trim(),
          ...parsed,
        }
        const created = await flow.createDefinition(input)
        if (created) {
          setSelectedVersionId(created.versionId)
          setEditorMode('closed')
        }
        return
      }
      if (editorMode === 'version' && flow.selectedDefinition) {
        const created = await flow.createDefinitionVersion(flow.selectedDefinition.id, parsed)
        if (created) {
          setSelectedVersionId(created.versionId)
          setEditorMode('closed')
        }
      }
    } catch (cause) {
      setEditorError(cause instanceof Error ? cause.message : t('flow.error.invalidDefinition'))
    }
  }

  const canStart = session.signedIn && Boolean(project) && can('edit') && flow.status === 'ready'

  return (
    <aside className={cn(
      'flex shrink-0 flex-col border-r border-border bg-panel transition-[width] max-lg:w-full max-lg:border-b max-lg:border-r-0',
      expanded ? 'w-[340px] max-lg:max-h-[520px]' : 'w-12 max-lg:max-h-12',
    )}>
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-border px-2.5">
        <Workflow className="size-4 text-primary-bright" />
        {expanded && (
          <>
            <div className="min-w-0 flex-1">
              <div className="truncate text-xs font-semibold text-foreground">{t('flow.title')}</div>
              <div className="truncate text-[9px] text-faint-foreground">{t('flow.subtitle')}</div>
            </div>
            <button
              type="button"
              onClick={() => void flow.refresh()}
              disabled={flow.busy}
              className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-30"
              aria-label={t('flow.refresh')}
            >
              <RefreshCw className={cn('size-3.5', flow.busy && 'animate-spin')} />
            </button>
          </>
        )}
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="rounded p-1 text-faint-foreground hover:bg-white/5 hover:text-foreground"
          aria-label={expanded ? t('flow.collapse') : t('flow.expand')}
        >
          {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        </button>
      </div>

      {expanded && (
        <div className="min-h-0 flex-1 overflow-y-auto p-3 scrollbar-thin">
          {flow.error && (
            <div role="alert" className="mb-3 rounded-lg border border-destructive/30 bg-destructive/10 p-2.5 text-[10px] leading-relaxed text-destructive">
              <div className="flex gap-2">
                <CircleAlert className="mt-0.5 size-3.5 shrink-0" />
                <span className="min-w-0 flex-1">{flow.error}</span>
                <button type="button" onClick={flow.clearError} aria-label={t('flow.dismissError')}><X className="size-3" /></button>
              </div>
            </div>
          )}

          {!session.signedIn || !project ? (
            <EmptyState text={t('flow.empty.signIn')} />
          ) : flow.status === 'loading' ? (
            <EmptyState text={t('flow.empty.loading')} loading />
          ) : flow.status === 'error' ? (
            <EmptyState text={t('flow.empty.unavailable')} />
          ) : (
            <>
              <Section title={t('flow.section.definition')} icon={GitBranch}>
                <select
                  value={flow.selectedDefinition?.id ?? ''}
                  onChange={(event) => void flow.selectDefinition(event.target.value)}
                  className="h-8 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none"
                  aria-label={t('flow.definition')}
                >
                  {flow.definitions.length === 0 && <option value="">{t('flow.noDefinition')}</option>}
                  {flow.definitions.map((definition) => (
                    <option key={definition.id} value={definition.id}>
                      {definition.title} · v{definition.version.toLocaleString(locale)}{definition.published ? ` · ${t('flow.status.published')}` : ''}
                    </option>
                  ))}
                </select>

                <div className="mt-2 flex gap-1.5">
                  <select
                    value={selectedVersion?.versionId ?? ''}
                    onChange={(event) => setSelectedVersionId(event.target.value)}
                    className="h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-[10px] text-foreground outline-none"
                    aria-label={t('flow.version')}
                  >
                    {flow.definitionVersions.map((version) => (
                      <option key={version.versionId} value={version.versionId}>
                        v{version.version.toLocaleString(locale)} · {version.contentHash.slice(0, 12)}{version.executionProfile?.version ? ` · ${version.executionProfile.version}` : ` · ${t('flow.legacyProfile')}`}{version.published ? ` · ${t('flow.status.published')}` : ''}
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    onClick={() => selectedVersion && void flow.publishDefinitionVersion(selectedVersion.id, selectedVersion.versionId)}
                    disabled={!selectedVersion || selectedVersion.published || !can('publish') || flow.busy}
                    className="inline-flex h-8 items-center gap-1 rounded-md border border-border px-2 text-[10px] text-muted-foreground hover:border-primary/40 hover:text-foreground disabled:opacity-35"
                    title={t('flow.publishTitle')}
                  >
                    <UploadCloud className="size-3" /> {t('flow.publish')}
                  </button>
                </div>

                {selectedVersion?.executionProfile && (
                  <p className="mt-1 truncate font-mono text-[8px] text-faint-foreground" title={selectedVersion.executionProfile.hash}>
                    {t('flow.execution')} {selectedVersion.executionProfile.version} · {selectedVersion.executionProfile.hash.slice(0, 12)}
                    {flow.capabilities?.analysisLimits
                      ? ` · ${t('flow.registry')} v${flow.capabilities.version.toLocaleString(locale)} · ${t('flow.semanticMax')} ${flow.capabilities.analysisLimits.maxSemanticPathStates.toLocaleString(locale)}`
                      : ''}
                  </p>
                )}

                {can('admin') && (
                  <div className="mt-2 grid grid-cols-2 gap-1.5">
                    <button type="button" onClick={() => openEditor('create')} className="inline-flex h-8 items-center justify-center gap-1 rounded-md border border-border text-[10px] text-muted-foreground hover:border-primary/40 hover:text-foreground">
                      <PencilLine className="size-3" /> {t('flow.newDefinition')}
                    </button>
                    <button type="button" onClick={() => openEditor('version')} disabled={!selectedVersion} className="inline-flex h-8 items-center justify-center gap-1 rounded-md border border-border text-[10px] text-muted-foreground hover:border-primary/40 hover:text-foreground disabled:opacity-35">
                      <GitBranch className="size-3" /> {t('flow.newVersion')}
                    </button>
                  </div>
                )}

                <button
                  type="button"
                  onClick={() => void flow.startFromProjectBrief({
                    definitionVersionId: selectedVersion?.published ? selectedVersion.versionId : undefined,
                  })}
                  disabled={!canStart || flow.busy || (selectedVersion ? !selectedVersion.published : false)}
                  className="mt-2 inline-flex h-9 w-full items-center justify-center gap-1.5 rounded-md bg-primary px-3 text-[11px] font-semibold text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-40"
                  title={t('flow.startTitle')}
                >
                  <Play className="size-3.5" />
                  {t('flow.start')}
                </button>
                <p className="mt-1.5 text-[9px] leading-relaxed text-faint-foreground">
                  {t('flow.startCopy')}
                </p>
              </Section>

              {flow.manifest && (
                <Section title={t('flow.section.frozenInput')} icon={ShieldCheck}>
                  <Fact label={t('flow.manifest')} value={flow.manifest.id} mono />
                  <Fact label={t('flow.hash')} value={flow.manifest.hash} mono />
                  <Fact label={t('flow.sources')} value={flow.manifest.sources.length.toLocaleString(locale)} />
                </Section>
              )}

              {flow.runs.length > 0 && (
                <Section title={t('flow.section.runHistory')} icon={HistoryIcon}>
                  <div className="max-h-36 space-y-1 overflow-y-auto pr-1 scrollbar-thin">
                    {flow.runs.map((item) => (
                      <button
                        key={item.id}
                        type="button"
                        onClick={() => void flow.loadRun(item.id)}
                        className={cn(
                          'flex w-full items-center gap-2 rounded border px-2 py-1.5 text-left',
                          flow.run?.id === item.id
                            ? 'border-primary/35 bg-primary/10'
                            : 'border-border bg-background hover:border-white/20',
                        )}
                      >
                        <RunStatusIcon status={item.status} />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-mono text-[9px] text-foreground">{item.id}</span>
                          <span className="block text-[8px] text-faint-foreground">{workflowStatusLabel(item.status, t)} · {new Date(item.updatedAt).toLocaleString(locale)}</span>
                        </span>
                      </button>
                    ))}
                  </div>
                </Section>
              )}

              {flow.run && (
                <Section title={t('flow.section.run')} icon={Workflow}>
                  <div className="mb-2 flex items-center gap-2 rounded-md border border-border bg-background p-2">
                    <RunStatusIcon status={flow.run.status} />
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-mono text-[10px] text-foreground">{flow.run.id}</div>
                      <div className="mt-0.5 text-[9px] text-faint-foreground">
                        {workflowStatusLabel(flow.run.status, t)} · {t('flow.eventNumber', { number: flow.run.eventCursor.toLocaleString(locale) })}
                      </div>
                    </div>
                    {!['completed', 'failed', 'cancelled', 'stale'].includes(flow.run.status) && (
                      <button type="button" onClick={() => void flow.cancelRun()} disabled={!can('edit') || flow.busy} className="rounded p-1 text-faint-foreground hover:text-destructive" aria-label={t('flow.cancelRun')}>
                        <Square className="size-3" />
                      </button>
                    )}
                  </div>

                  <div className="space-y-1.5">
                    {[...flow.run.nodes]
                      .sort((left, right) => left.createdAt.localeCompare(right.createdAt) || left.key.localeCompare(right.key))
                      .map((node) => (
                        <RunNodeCard key={node.id} node={node} />
                      ))}
                  </div>
                </Section>
              )}

              {flow.events.length > 0 && (
                <Section title={t('flow.section.events')} icon={RefreshCw}>
                  <div className="max-h-36 space-y-1 overflow-y-auto pr-1 scrollbar-thin">
                    {flow.events.slice(-20).toReversed().map((event) => (
                      <div key={event.id} className="rounded border border-border bg-background px-2 py-1.5">
                        <div className="flex gap-2 text-[9px]">
                          <span className="font-mono text-primary-bright">#{event.sequence.toLocaleString(locale)}</span>
                          <span className="min-w-0 flex-1 truncate text-muted-foreground">{event.type}</span>
                          <span className="text-faint-foreground">{event.nodeKey}</span>
                        </div>
                      </div>
                    ))}
                  </div>
                </Section>
              )}
            </>
          )}
        </div>
      )}

      {editorMode !== 'closed' && (
        <DefinitionEditor
          mode={editorMode}
          definition={selectedVersion}
          draftKey={draftKey}
          draftTitle={draftTitle}
          draftDescription={draftDescription}
          definitionJSON={definitionJSON}
          error={editorError}
          busy={flow.busy}
          capabilities={flow.capabilities}
          onKeyChange={setDraftKey}
          onTitleChange={setDraftTitle}
          onDescriptionChange={setDraftDescription}
          onJSONChange={setDefinitionJSON}
          onClose={() => setEditorMode('closed')}
          onSave={() => void saveDefinition()}
        />
      )}
    </aside>
  )
}

function RunNodeCard({ node }: { node: WorkflowNodeRunDto }) {
  const flow = usePlatformFlow()
  const artifacts = useArtifactWorkspace()
  const { can, project } = useCollaboration()
  const { setSelectedDocId, setSurface, setTeamView } = useWorksflow()
  const { t } = useI18n()
  const [revisionKey, setRevisionKey] = useState('')
  const [reason, setReason] = useState('')
  const [soloReviewConfirmed, setSoloReviewConfirmed] = useState(false)
  const [selectionError, setSelectionError] = useState<string | null>(null)
  const definitionNode = flow.runDefinition?.definition.nodes.find(
    (item) => item.id === node.definitionNodeId,
  )
  const candidateResolution = useMemo(
    () => resolveLineageCandidates(definitionNode, node, flow.run, artifacts),
    [artifacts, definitionNode, flow.run, node],
  )
  const candidates = candidateResolution.candidates
  const editorTarget = candidateResolution.editorTarget
  const selected = resolveLineageSelection(candidates, revisionKey)
  const staleSelection = Boolean(revisionKey && !selected)
  const selectionRequired = candidates.length > 1 && !selected
  const submitError = candidateResolution.error
    ?? (staleSelection ? t('flow.error.staleRevision') : undefined)
    ?? (selectionRequired ? t('flow.error.multipleRevisions') : undefined)
  const active = ['waiting_input', 'waiting_review', 'failed'].includes(node.status)
  const reviewRequiredRole = definitionNode?.reviewGate?.requiredRole ?? 'editor'
  const canApproveReview = Boolean(project && workflowRoleSatisfies(project.role, reviewRequiredRole))
  const canonicalReview = useMemo(
    () => reviewGateApprovalReadiness(
      flow.runDefinition?.definition,
      node,
      flow.run,
      artifacts,
    ),
    [artifacts, flow.run, flow.runDefinition?.definition, node],
  )
  const soloApprovalRequiresConfirmation = project
    ? requiresSoloReviewConfirmation(flow.run?.governanceMode ?? project.governanceMode, 'approve')
    : false

  async function approveReview() {
    if (!canonicalReview.ready) return
    const approved = await flow.resolveReview(
      node,
      'approve',
      reason,
      soloReviewConfirmed,
    )
    if (approved) setSoloReviewConfirmed(false)
  }

  function submitSelectedRevision() {
    setSelectionError(null)
    if (!selected) {
      setSelectionError(submitError ?? t('flow.error.selectRevision'))
      return
    }
    const stillAllowed = candidateResolution.candidates.find((candidate) =>
      candidate.key === selected.key && exactArtifactRefsEqual(candidate.ref, selected.ref),
    )
    if (!stillAllowed) {
      setSelectionError(t('flow.error.revisionRemoved'))
      return
    }
    void flow.submitNodeRevision(node, stillAllowed.ref)
  }

  function openLinkedEditor() {
    if (!editorTarget) return
    setWorkflowArtifactReference(
      editorTarget.artifactId,
      editorTarget.proposalId,
      node.runId,
      node.key,
    )
    if (
      editorTarget.artifactKind === 'blueprint'
      || editorTarget.artifactKind === 'page_spec'
      || definitionNode?.humanEdit?.artifactType === 'blueprint'
    ) {
      setTeamView('blueprint')
    } else if (
      editorTarget.artifactKind === 'prototype'
      || editorTarget.artifactKind === 'prototype_flow'
      || definitionNode?.humanEdit?.artifactType === 'prototype'
    ) {
      setTeamView('prototype')
    } else {
      setSelectedDocId(editorTarget.artifactId)
      setTeamView('editor')
    }
    setSurface('team')
  }

  return (
    <div className={cn(
      'rounded-md border p-2',
      active ? 'border-primary/35 bg-primary/5' : 'border-border bg-background',
    )}>
      <div className="flex items-start gap-2">
        <NodeStatusIcon status={node.status} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[10px] font-medium text-foreground">
            {definitionNode?.name ?? node.definitionNodeId}
          </div>
          <div className="mt-0.5 flex flex-wrap gap-x-2 text-[9px] text-faint-foreground">
            <span>{workflowNodeTypeLabel(node.type, t)}</span>
            <span>{workflowStatusLabel(node.status, t)}</span>
            {node.sliceId && <span>{t('flow.slice')} {node.sliceId.slice(0, 8)}</span>}
          </div>
        </div>
        {node.status === 'failed' && (
          <button type="button" onClick={() => void flow.retryNode(node)} disabled={!can('edit') || flow.busy} className="rounded p-1 text-warning hover:bg-warning/10" aria-label={t('flow.retryNode')}>
            <RotateCcw className="size-3" />
          </button>
        )}
      </div>

      {node.status === 'waiting_input' && node.type === 'human_edit' && (
        <div className="mt-2 border-t border-border pt-2">
          {candidates.length > 0 ? (
            <>
              <select
                value={selected?.key ?? ''}
                onChange={(event) => {
                  setRevisionKey(event.target.value)
                  setSelectionError(null)
                }}
                className="h-7 w-full rounded border border-border bg-panel px-1.5 text-[9px] text-foreground outline-none"
                aria-label={t('flow.exactRevision')}
              >
                {candidates.length > 1 && <option value="">{t('flow.selectLineageRevision')}</option>}
                {candidates.map((candidate) => (
                  <option key={candidate.key} value={candidate.key}>{candidate.label}</option>
                ))}
              </select>
              <button
                type="button"
                onClick={submitSelectedRevision}
                disabled={!selected || Boolean(submitError) || !can('edit') || flow.busy}
                className="mt-1.5 inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[9px] font-semibold text-primary-foreground disabled:opacity-40"
              >
                <Send className="size-3" /> {t('flow.submitPinnedRevision')}
              </button>
              {(submitError || selectionError) && (
                <p className="mt-1 text-[9px] leading-relaxed text-warning">
                  {selectionError ?? submitError}
                </p>
              )}
            </>
          ) : (
            <>
              {editorTarget && (
                <button
                  type="button"
                  onClick={openLinkedEditor}
                  disabled={!can('edit') || flow.busy}
                  className="mb-1.5 inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[9px] font-semibold text-primary-foreground disabled:opacity-40"
                >
                  <PencilLine className="size-3" /> {t('flow.openLinkedEditor')}
                </button>
              )}
              <p className="text-[9px] leading-relaxed text-warning">
                {candidateResolution.error ?? t('flow.error.noRevision')}
              </p>
            </>
          )}
        </div>
      )}

      {node.status === 'waiting_input'
        && (node.type === 'quality_gate' || node.type === 'publish') && (
        <div className="mt-2 border-t border-border pt-2">
          <button
            type="button"
            onClick={() => void flow.authorizeExecution(node)}
            disabled={!(node.type === 'publish' ? can('publish') : can('edit')) || flow.busy}
            className="inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[9px] font-semibold text-primary-foreground disabled:opacity-40"
          >
            <ShieldCheck className="size-3" />
            {node.type === 'publish' ? t('flow.authorizePublish') : t('flow.authorizeQuality')}
          </button>
          <p className="mt-1 text-[8px] leading-relaxed text-faint-foreground">
            {t('flow.authorizationCopy')}
          </p>
        </div>
      )}

      {node.status === 'waiting_input' && node.type === 'workbench_build' && (
        <p className="mt-2 border-t border-border pt-2 text-[9px] leading-relaxed text-primary-bright">
          {t('flow.reviewBuildCopy')}
        </p>
      )}

      {node.status === 'waiting_review' && (
        <div className="mt-2 border-t border-border pt-2">
          {!canonicalReview.ready && (
            <div
              role="alert"
              className="mb-2 rounded border border-warning/35 bg-warning/10 p-2 text-[9px] leading-relaxed text-warning"
              data-testid={`workflow-review-canonical-blocker-${node.key}`}
            >
              <div className="flex items-start gap-1.5">
                <CircleAlert className="mt-0.5 size-3 shrink-0" />
                <span>{t('flow.canonicalReviewRequired')}</span>
              </div>
            </div>
          )}
          {soloApprovalRequiresConfirmation && (
            <div role="alert" className="mb-2 rounded border border-warning/35 bg-warning/10 p-2 text-[9px] leading-relaxed text-warning" data-testid={`solo-review-warning-${node.key}`}>
              <div className="flex items-start gap-1.5">
                <CircleAlert className="mt-0.5 size-3 shrink-0" />
                <span>{t('flow.soloReview.warning')}</span>
              </div>
              <label className="mt-2 flex cursor-pointer items-start gap-1.5 text-foreground">
                <input
                  type="checkbox"
                  checked={soloReviewConfirmed}
                  onChange={(event) => setSoloReviewConfirmed(event.target.checked)}
                  className="mt-0.5"
                  data-testid={`solo-review-confirm-${node.key}`}
                />
                <span>{t('flow.soloReview.confirm')}</span>
              </label>
            </div>
          )}
          <input
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            placeholder={t('flow.reviewReason')}
            className="h-7 w-full rounded border border-border bg-panel px-2 text-[9px] text-foreground outline-none placeholder:text-faint-foreground"
          />
          <div className="mt-1.5 grid grid-cols-2 gap-1">
            <button type="button" data-testid={`workflow-review-approve-${node.key}`} onClick={() => void approveReview()} disabled={!canApproveReview || !canonicalReview.ready || flow.busy || (soloApprovalRequiresConfirmation && (!soloReviewConfirmed || !reason.trim()))} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[9px] font-medium text-success disabled:opacity-35">
              <Check className="size-3" /> {t('flow.approve')}
            </button>
            <button type="button" onClick={() => void flow.resolveReview(node, 'changes_requested', reason || t('flow.defaultChangeRequest'))} disabled={!can('edit') || flow.busy} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-warning/15 text-[9px] font-medium text-warning disabled:opacity-35">
              <PencilLine className="size-3" /> {t('flow.requestChanges')}
            </button>
          </div>
          <p className="mt-1 text-[8px] leading-relaxed text-faint-foreground">
            {t('flow.reviewerCopy', { role: workflowRoleLabel(reviewRequiredRole, t) })}
          </p>
        </div>
      )}
    </div>
  )
}

function setWorkflowArtifactReference(
  artifactId: string,
  proposalId: string,
  runId: string,
  nodeKey: string,
) {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  url.searchParams.set('artifactId', artifactId)
  url.searchParams.set('proposalId', proposalId)
  url.searchParams.set('runId', runId)
  url.searchParams.set('workbenchNodeKey', nodeKey)
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}

function DefinitionEditor({
  mode,
  definition,
  draftKey,
  draftTitle,
  draftDescription,
  definitionJSON,
  error,
  busy,
  capabilities,
  onKeyChange,
  onTitleChange,
  onDescriptionChange,
  onJSONChange,
  onClose,
  onSave,
}: {
  mode: Exclude<EditorMode, 'closed'>
  definition?: WorkflowDefinitionRecordDto | null
  draftKey: string
  draftTitle: string
  draftDescription: string
  definitionJSON: string
  error: string | null
  busy: boolean
  capabilities?: WorkflowCapabilitiesDto | null
  onKeyChange: (value: string) => void
  onTitleChange: (value: string) => void
  onDescriptionChange: (value: string) => void
  onJSONChange: (value: string) => void
  onClose: () => void
  onSave: () => void
}) {
  const { locale, t } = useI18n()
  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/70 p-4" role="dialog" aria-modal="true" aria-label={t('flow.editor.label')}>
      <div className="flex max-h-[92vh] w-full max-w-4xl flex-col overflow-hidden rounded-xl border border-border bg-panel shadow-2xl">
        <header className="flex items-center gap-3 border-b border-border px-4 py-3">
          <GitBranch className="size-4 text-primary-bright" />
          <div className="min-w-0 flex-1">
            <h2 className="text-sm font-semibold text-foreground">
              {mode === 'create'
                ? t('flow.editor.create')
                : t('flow.editor.createVersion', { version: ((definition?.version ?? 0) + 1).toLocaleString(locale) })}
            </h2>
            <p className="text-[10px] text-faint-foreground">{t('flow.editor.copy')}</p>
          </div>
          <button type="button" onClick={onClose} className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground" aria-label={t('flow.editor.close')}><X className="size-4" /></button>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto p-4 scrollbar-thin">
          {mode === 'create' && (
            <div className="mb-3 grid grid-cols-2 gap-2 max-md:grid-cols-1">
              <label className="text-[10px] font-semibold uppercase tracking-wider text-faint-foreground [&_input]:mt-1 [&_input]:h-9 [&_input]:w-full [&_input]:rounded-md [&_input]:border [&_input]:border-border [&_input]:bg-background [&_input]:px-2 [&_input]:text-xs [&_input]:font-normal [&_input]:normal-case [&_input]:text-foreground [&_input]:outline-none">{t('flow.editor.key')}<input value={draftKey} onChange={(event) => onKeyChange(event.target.value)} pattern="[a-z][a-z0-9-]{2,63}" /></label>
              <label className="text-[10px] font-semibold uppercase tracking-wider text-faint-foreground [&_input]:mt-1 [&_input]:h-9 [&_input]:w-full [&_input]:rounded-md [&_input]:border [&_input]:border-border [&_input]:bg-background [&_input]:px-2 [&_input]:text-xs [&_input]:font-normal [&_input]:normal-case [&_input]:text-foreground [&_input]:outline-none">{t('flow.editor.title')}<input value={draftTitle} onChange={(event) => onTitleChange(event.target.value)} /></label>
              <label className="col-span-2 text-[10px] font-semibold uppercase tracking-wider text-faint-foreground max-md:col-span-1 [&_input]:mt-1 [&_input]:h-9 [&_input]:w-full [&_input]:rounded-md [&_input]:border [&_input]:border-border [&_input]:bg-background [&_input]:px-2 [&_input]:text-xs [&_input]:font-normal [&_input]:normal-case [&_input]:text-foreground [&_input]:outline-none">{t('flow.editor.description')}<input value={draftDescription} onChange={(event) => onDescriptionChange(event.target.value)} /></label>
            </div>
          )}
          <div className="text-[10px] font-semibold uppercase tracking-wider text-faint-foreground">
            {t('flow.editor.graphAndContracts')}
            <div className="mt-2 normal-case tracking-normal">
              <WorkflowGraphEditor value={definitionJSON} onChange={onJSONChange} capabilities={capabilities} />
            </div>
          </div>
          {error && <p role="alert" className="mt-2 text-[10px] text-destructive">{error}</p>}
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
          <button type="button" onClick={onClose} className="rounded-md border border-border px-3 py-2 text-[11px] text-muted-foreground hover:text-foreground">{t('flow.editor.cancel')}</button>
          <button type="button" onClick={onSave} disabled={busy} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground disabled:opacity-40"><Save className="size-3.5" /> {t('flow.editor.save')}</button>
        </footer>
      </div>
    </div>
  )
}

type Translate = ReturnType<typeof useI18n>['t']

export function parseDefinitionJSON(
  value: string,
  capabilities?: WorkflowCapabilitiesDto | null,
  t?: Translate,
) {
  const parsed = parseWorkflowContract(value, true, capabilities)
  if (!parsed.definition) throw new Error(parsed.error ?? t?.('flow.error.invalidDefinition') ?? 'Definition JSON is invalid.')
  return parsed.definition
}

function Section({ title, icon: Icon, children }: { title: string; icon: typeof Workflow; children: React.ReactNode }) {
  return (
    <section className="mb-4">
      <h3 className="mb-2 flex items-center gap-1.5 text-[9px] font-semibold uppercase tracking-wider text-faint-foreground"><Icon className="size-3" />{title}</h3>
      {children}
    </section>
  )
}

function Fact({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return <div className="mb-1 flex gap-2 rounded border border-border bg-background px-2 py-1.5 text-[9px]"><span className="text-faint-foreground">{label}</span><span className={cn('min-w-0 flex-1 truncate text-right text-muted-foreground', mono && 'font-mono')} title={value}>{value}</span></div>
}

function EmptyState({ text, loading }: { text: string; loading?: boolean }) {
  return <div className="rounded-lg border border-dashed border-border bg-background p-4 text-center text-[10px] leading-relaxed text-faint-foreground">{loading && <CircleDashed className="mx-auto mb-2 size-4 animate-spin text-primary-bright" />}{text}</div>
}

function workflowStatusLabel(status: string, t: Translate) {
  switch (status) {
    case 'ready': return t('flow.status.ready')
    case 'queued': return t('flow.status.queued')
    case 'pending': return t('flow.status.pending')
    case 'running': return t('flow.status.running')
    case 'waiting_input': return t('flow.status.waitingInput')
    case 'waiting_review': return t('flow.status.waitingReview')
    case 'completed': return t('flow.status.completed')
    case 'failed': return t('flow.status.failed')
    case 'cancelled': return t('flow.status.cancelled')
    case 'stale': return t('flow.status.stale')
    case 'blocked': return t('flow.status.blocked')
    case 'published': return t('flow.status.published')
    default: return status.replaceAll('_', ' ')
  }
}

function workflowNodeTypeLabel(type: string, t: Translate) {
  switch (type) {
    case 'artifact_input': return t('flow.nodeType.artifactInput')
    case 'ai_transform': return t('flow.nodeType.aiTransform')
    case 'human_edit': return t('flow.nodeType.humanEdit')
    case 'review_gate': return t('flow.nodeType.reviewGate')
    case 'condition': return t('flow.nodeType.condition')
    case 'fan_out': return t('flow.nodeType.fanOut')
    case 'merge': return t('flow.nodeType.merge')
    case 'manifest_compiler': return t('flow.nodeType.manifestCompiler')
    case 'workbench_build': return t('flow.nodeType.workbenchBuild')
    case 'quality_gate': return t('flow.nodeType.qualityGate')
    case 'publish': return t('flow.nodeType.publish')
    case 'transform': return t('flow.nodeType.transform')
    default: return type.replaceAll('_', ' ')
  }
}

function workflowRoleLabel(role: string, t: Translate) {
  switch (role) {
    case 'viewer': return t('flow.role.viewer')
    case 'commenter': return t('flow.role.commenter')
    case 'editor': return t('flow.role.editor')
    case 'admin': return t('flow.role.admin')
    case 'owner': return t('flow.role.owner')
    default: return role
  }
}

function RunStatusIcon({ status }: { status: string }) {
  if (status === 'completed') return <CircleCheck className="size-4 text-success" />
  if (status === 'failed' || status === 'cancelled' || status === 'stale') return <CircleAlert className="size-4 text-destructive" />
  return <CircleDashed className="size-4 animate-spin text-primary-bright" />
}

function NodeStatusIcon({ status }: { status: WorkflowNodeRunDto['status'] }) {
  if (status === 'completed') return <CircleCheck className="mt-0.5 size-3.5 shrink-0 text-success" />
  if (status === 'failed' || status === 'cancelled' || status === 'stale') return <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-destructive" />
  if (status === 'waiting_input' || status === 'waiting_review') return <CircleDashed className="mt-0.5 size-3.5 shrink-0 text-warning" />
  return <CircleDashed className="mt-0.5 size-3.5 shrink-0 text-faint-foreground" />
}
