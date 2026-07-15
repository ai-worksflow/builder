'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Archive,
  Bot,
  Check,
  CircleAlert,
  LoaderCircle,
  MessageSquare,
  Plus,
  RefreshCw,
  Send,
  Sparkles,
  Workflow,
  X,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import type {
  ConversationCommandDto,
  ConversationDto,
  ConversationMessageDto,
  ConversationSummaryCheckpointDto,
  ConversationSummaryCheckpointRequiredExtensionsDto,
  WorkflowIntentProposalDto,
} from '@/lib/platform/conversation-contract'
import { collectConversationPages } from '@/lib/platform/conversation-client'
import { workbenchRootBundleId, workflowWorkbenchQueueGroups } from '@/lib/platform/flow-queue'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import type { WorkflowRunDto } from '@/lib/platform/flow-contract'
import { PlatformHttpError } from '@/lib/platform/http'
import { canSoloSelfReview } from '@/lib/worksflow/project-governance'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'

export function ConversationPanel({ onClose }: { onClose: () => void }) {
  const { locale, t } = useI18n()
  const { project, session, members, can, platformClient } = useCollaboration()
  const artifacts = useArtifactWorkspace()
  const flow = usePlatformFlow()
  const [conversations, setConversations] = useState<readonly ConversationDto[]>([])
  const [conversationId, setConversationId] = useState('')
  const [messages, setMessages] = useState<readonly ConversationMessageDto[]>([])
  const [proposals, setProposals] = useState<readonly WorkflowIntentProposalDto[]>([])
  const [commands, setCommands] = useState<readonly ConversationCommandDto[]>([])
  const [summaryCheckpoints, setSummaryCheckpoints] = useState<readonly ConversationSummaryCheckpointDto[]>([])
  const [checkpointSources, setCheckpointSources] = useState<Readonly<Record<string, readonly ConversationMessageDto[]>>>({})
  const [checkpointRequirement, setCheckpointRequirement] = useState<ConversationSummaryCheckpointRequiredExtensionsDto | null>(null)
  const [checkpointSummary, setCheckpointSummary] = useState('')
  const [draft, setDraft] = useState('')
  const [newTitle, setNewTitle] = useState(() => t('conversation.defaultTitle'))
  const [showCreate, setShowCreate] = useState(false)
  const [model, setModel] = useState('gpt-5')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [checkpointNotice, setCheckpointNotice] = useState<string | null>(null)
  const conversationIdRef = useRef('')
  const conversationProjectIdRef = useRef('')
  const conversationSelectionEpoch = useRef(0)
  const conversationHydrationRequest = useRef(0)
  const conversationListRequest = useRef(0)
  const busyOperations = useRef(0)
  const currentUserId = session.signedIn ? session.user.id : ''
  const selected = conversations.find((item) => item.id === conversationId)
  const projectBrief = artifacts.documents.find((item) => item.artifact.kind === 'project_brief')
  const briefRevision = projectBrief?.latestRevision ?? projectBrief?.approvedRevision
  const briefNeedsCheckpoint = Boolean(
    projectBrief?.draft
    && (!briefRevision || projectBrief.draft.contentHash !== briefRevision.contentHash),
  )
  const client = platformClient.conversation

  function beginBusy() {
    busyOperations.current += 1
    setBusy(true)
  }

  function endBusy() {
    busyOperations.current = Math.max(0, busyOperations.current - 1)
    if (busyOperations.current === 0) setBusy(false)
  }

  const loadConversation = useCallback(async (id: string, expectedSelectionEpoch?: number) => {
    if (!project) return
    const projectId = project.id
    const selectionEpoch = expectedSelectionEpoch ?? conversationSelectionEpoch.current
    const hydrationRequest = ++conversationHydrationRequest.current
    const [messageResult, proposalResult, commandResult, checkpointResult] = await Promise.all([
      collectConversationPages((cursor) => client.listMessages(projectId, id, { limit: 200, cursor })),
      collectConversationPages((cursor) => client.listIntentProposals(projectId, id, { limit: 200, cursor })),
      collectConversationPages((cursor) => client.listCommands(projectId, id, { limit: 200, cursor })),
      collectConversationPages((cursor) => client.listSummaryCheckpoints(projectId, id, { limit: 200, cursor })),
    ])
    if (
      hydrationRequest !== conversationHydrationRequest.current
      || selectionEpoch !== conversationSelectionEpoch.current
      || conversationIdRef.current !== id
      || conversationProjectIdRef.current !== projectId
    ) return
    if (
      messageResult.some((item) => item.conversationId !== id)
      || proposalResult.some((item) => item.conversationId !== id)
      || commandResult.some((item) => item.conversationId !== id)
      || checkpointResult.some((item) => item.conversationId !== id)
    ) {
      throw new Error(t('conversation.error.hydrationMismatch'))
    }
    setMessages([...messageResult].sort((left, right) => left.sequence - right.sequence))
    setProposals([...proposalResult].sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
    setCommands([...commandResult].sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
    setSummaryCheckpoints([...checkpointResult].sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
  }, [client, project, t])

  const refresh = useCallback(async () => {
    const listRequest = ++conversationListRequest.current
    if (!project || !session.signedIn) {
      conversationSelectionEpoch.current += 1
      conversationHydrationRequest.current += 1
      conversationProjectIdRef.current = ''
      conversationIdRef.current = ''
      setConversations([])
      setConversationId('')
      return
    }
    setError(null)
    try {
      const projectId = project.id
      if (conversationProjectIdRef.current !== projectId) {
        conversationSelectionEpoch.current += 1
        conversationHydrationRequest.current += 1
        conversationProjectIdRef.current = projectId
        conversationIdRef.current = ''
        setConversationId('')
        setMessages([])
        setProposals([])
        setCommands([])
        setSummaryCheckpoints([])
        setCheckpointSources({})
        setCheckpointRequirement(null)
      }
      const result = await collectConversationPages((cursor) =>
        client.list(projectId, { limit: 200, cursor }))
      if (
        listRequest !== conversationListRequest.current
        || conversationProjectIdRef.current !== projectId
      ) return
      const items = [...result].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt))
      setConversations(items)
      const queryId = conversationReference()
      const next = items.find((item) => item.id === (conversationIdRef.current || queryId))
        ?? items.find((item) => item.status === 'active')
        ?? items[0]
      if (next) {
        if (conversationIdRef.current !== next.id) {
          conversationSelectionEpoch.current += 1
          conversationHydrationRequest.current += 1
          setMessages([])
          setProposals([])
          setCommands([])
          setSummaryCheckpoints([])
          setCheckpointSources({})
          setCheckpointRequirement(null)
        }
        conversationIdRef.current = next.id
        setConversationId(next.id)
        setConversationReference(next.id)
        await loadConversation(next.id, conversationSelectionEpoch.current)
      } else {
        conversationSelectionEpoch.current += 1
        conversationHydrationRequest.current += 1
        conversationIdRef.current = ''
        setConversationId('')
        setMessages([])
        setProposals([])
        setCommands([])
        setSummaryCheckpoints([])
        setCheckpointSources({})
        setCheckpointRequirement(null)
      }
    } catch (cause) {
      if (
        listRequest === conversationListRequest.current
        && conversationProjectIdRef.current === project.id
      ) {
        setError(message(cause, t('conversation.error.loadProject')))
      }
    }
  }, [client, loadConversation, project, session.signedIn, t])

  useEffect(() => {
    void refresh()
  }, [project?.id, session.signedIn])

  useEffect(() => {
    if (!conversationId || !project || !session.signedIn) return
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') {
        void loadConversation(conversationId).catch(() => {
          // The next explicit refresh exposes the actionable server error.
        })
      }
    }, 5_000)
    return () => window.clearInterval(timer)
  }, [conversationId, loadConversation, project, session.signedIn])

  async function createConversation() {
    if (!project || !can('edit') || !newTitle.trim()) return
    const targetProjectId = project.id
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    setError(null)
    try {
      const result = await client.create(targetProjectId, newTitle.trim())
      if (result.data.projectId !== targetProjectId) {
        throw new Error(t('conversation.error.createdWrongProject'))
      }
      if (
        selectionEpoch !== conversationSelectionEpoch.current
        || conversationProjectIdRef.current !== targetProjectId
      ) return
      setConversations((current) => [result.data, ...current])
      conversationListRequest.current += 1
      conversationSelectionEpoch.current += 1
      conversationHydrationRequest.current += 1
      conversationIdRef.current = result.data.id
      setConversationId(result.data.id)
      setConversationReference(result.data.id)
      setMessages([])
      setProposals([])
      setCommands([])
      setSummaryCheckpoints([])
      setCheckpointSources({})
      setCheckpointRequirement(null)
      setShowCreate(false)
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationProjectIdRef.current === targetProjectId
      ) {
        setError(message(cause, t('conversation.error.create')))
      }
    } finally {
      endBusy()
    }
  }

  async function selectConversation(id: string) {
    if (!id) return
    conversationListRequest.current += 1
    conversationSelectionEpoch.current += 1
    conversationHydrationRequest.current += 1
    const selectionEpoch = conversationSelectionEpoch.current
    conversationIdRef.current = id
    setConversationId(id)
    setConversationReference(id)
    setShowCreate(false)
    setMessages([])
    setProposals([])
    setCommands([])
    setSummaryCheckpoints([])
    setCheckpointSources({})
    setCheckpointRequirement(null)
    beginBusy()
    setError(null)
    try {
      await loadConversation(id, selectionEpoch)
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === id
      ) setError(message(cause, t('conversation.error.loadSelected')))
    } finally {
      endBusy()
    }
  }

  async function sendMessage() {
    if (!project || !conversationId || !draft.trim() || !can('comment')) return
    const targetConversationId = conversationIdRef.current
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    setError(null)
    try {
      const result = await client.addMessage(project.id, targetConversationId, draft.trim())
      if (result.data.conversationId !== targetConversationId) {
        throw new Error(t('conversation.error.persistedWrongConversation'))
      }
      if (
        selectionEpoch !== conversationSelectionEpoch.current
        || conversationIdRef.current !== targetConversationId
      ) return
      setMessages((current) => [...current, result.data].sort((left, right) => left.sequence - right.sequence))
      setDraft('')
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === targetConversationId
      ) setError(message(cause, t('conversation.error.persist')))
    } finally {
      endBusy()
    }
  }

  async function generateIntent(triggerMessageId: string) {
    if (!project || !conversationId || !can('edit')) return
    if (!projectBrief) {
      setError(t('conversation.error.createBrief'))
      return
    }
    const targetConversationId = conversationIdRef.current
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    setError(null)
    setCheckpointNotice(null)
    try {
      let revision = projectBrief.latestRevision ?? projectBrief.approvedRevision
      if (
        projectBrief.draft
        && (!revision || projectBrief.draft.contentHash !== revision.contentHash)
      ) {
        revision = await artifacts.createDocumentRevision(
          projectBrief.artifact.id,
          projectBrief.draft.content,
        )
        setCheckpointNotice(
          t('conversation.notice.briefCheckpoint', { revision: formatNumber(revision.revisionNumber, locale) }),
        )
      }
      if (!revision) {
        throw new Error(t('conversation.error.briefRevision'))
      }
      const sourceRef = {
        artifactId: revision.artifactId,
        revisionId: revision.id,
        contentHash: revision.contentHash,
      }
      const manifest = await platformClient.flow.createManifest(project.id, {
        jobType: 'conversation.workflow_intent',
        baseRevision: sourceRef,
        sources: [{ ref: sourceRef, purpose: 'project_brief' }],
        constraints: {
          conversationId: targetConversationId,
          triggerMessageId,
          projectBriefArtifactId: sourceRef.artifactId,
          projectBriefRevisionId: sourceRef.revisionId,
          projectBriefContentHash: sourceRef.contentHash,
        },
        outputSchemaVersion: 'workflow-intent-input/v1',
      })
      await client.generateIntentProposal(project.id, targetConversationId, {
        triggerMessageId,
        desiredOutputCapability: 'application',
        sourceRefs: [sourceRef],
        manifestIntent: {
          mode: 'use_existing',
          inputManifest: { id: manifest.data.id, hash: manifest.data.hash },
          purpose: 'start_or_continue_application_workflow',
        },
        ...(flow.run && flow.bundle && flow.bundle.workflowRunId === flow.run.id
          ? {
              workbenchTargetHint: {
                runId: flow.run.id,
                rootBundleId: workbenchRootBundleId(flow.bundle),
              },
            }
          : {}),
        model: model.trim() || undefined,
      })
      setCheckpointRequirement(null)
      setCheckpointSummary('')
      await loadConversation(targetConversationId, selectionEpoch)
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === targetConversationId
      ) {
        const requirement = conversationCheckpointRequirement(cause)
        if (requirement) {
          setCheckpointRequirement(requirement)
          setCheckpointSummary('')
        }
        setError(message(cause, t('conversation.error.intent')))
      }
    } finally {
      endBusy()
    }
  }

  async function createSummaryCheckpoint() {
    if (
      !project
      || !session.signedIn
      || !selected
      || !checkpointRequirement
      || !checkpointSummary.trim()
      || !can('edit')
    ) return
    const targetConversationId = selected.id
    const selectionEpoch = conversationSelectionEpoch.current
    const submittedSummary = checkpointSummary.trim()
    const expectedPreviousCheckpointId = selected.summaryCheckpointHeadId?.trim() ?? ''
    beginBusy()
    setError(null)
    try {
      const result = await client.createSummaryCheckpoint(project.id, selected, {
        throughMessageId: checkpointRequirement.recommendedThroughMessageId,
        summary: submittedSummary,
      })
      if (
        result.data.projectId !== project.id
        || result.data.conversationId !== targetConversationId
        || result.data.throughMessageId !== checkpointRequirement.recommendedThroughMessageId
        || result.data.throughSequence !== checkpointRequirement.recommendedThroughSequence
        || result.data.summary !== submittedSummary
        || (result.data.previousCheckpointId?.trim() ?? '') !== expectedPreviousCheckpointId
        || result.data.status !== 'pending_review'
        || result.data.createdBy !== session.user.id
      ) {
        throw new Error(t('conversation.error.checkpointResponse'))
      }
      if (
        selectionEpoch !== conversationSelectionEpoch.current
        || conversationIdRef.current !== targetConversationId
      ) return
      setSummaryCheckpoints((current) => [...current, result.data]
        .sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
      setCheckpointSummary('')
      setCheckpointNotice(
        t('conversation.notice.checkpointPending', { sequence: formatNumber(result.data.throughSequence, locale) }),
      )
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === targetConversationId
      ) setError(message(cause, t('conversation.error.createCheckpoint')))
    } finally {
      endBusy()
    }
  }

  async function loadSummaryCheckpointSource(checkpoint: ConversationSummaryCheckpointDto) {
    if (!project) return
    const previous = checkpoint.previousCheckpointId
      ? summaryCheckpoints.find((item) => item.id === checkpoint.previousCheckpointId)
      : undefined
    if (checkpoint.previousCheckpointId && (!previous || previous.status !== 'approved')) {
      setError(t('conversation.error.predecessor'))
      return
    }
    const expectedStartSequence = (previous?.throughSequence ?? 0) + 1
    const expectedMessageCount = checkpoint.throughSequence - expectedStartSequence + 1
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    setError(null)
    try {
      const source = await collectConversationPages((cursor) =>
        client.listSummaryCheckpointSourceMessages(
          project.id,
          checkpoint.conversationId,
          checkpoint.id,
          { limit: 200, cursor },
        ))
      if (
        source.some((item) => item.conversationId !== checkpoint.conversationId)
        || expectedMessageCount < 1
        || source.length !== expectedMessageCount
        || source[0]?.sequence !== expectedStartSequence
        || source.some((item, index) => index > 0 && item.sequence !== source[index - 1].sequence + 1)
        || source.at(-1)?.sequence !== checkpoint.throughSequence
        || source.at(-1)?.id !== checkpoint.throughMessageId
      ) {
        throw new Error(t('conversation.error.sourceMismatch'))
      }
      if (
        selectionEpoch !== conversationSelectionEpoch.current
        || conversationIdRef.current !== checkpoint.conversationId
      ) return
      setCheckpointSources((current) => ({ ...current, [checkpoint.id]: source }))
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === checkpoint.conversationId
      ) setError(message(cause, t('conversation.error.inspectSource')))
    } finally {
      endBusy()
    }
  }

  async function decideSummaryCheckpoint(
    checkpoint: ConversationSummaryCheckpointDto,
    decision: 'approve' | 'reject',
    reason = '',
    soloReviewConfirmed = false,
  ) {
    const isSelfReview = session.signedIn && checkpoint.createdBy === session.user.id
    const isSoloSelfReview = Boolean(
      project
      && session.signedIn
      && canSoloSelfReview(
        project.governanceMode,
        members,
        session.user.id,
        checkpoint.createdBy,
      ),
    )
    if (
      !project
      || !session.signedIn
      || !can('review')
      || !checkpointSources[checkpoint.id]
      || (isSelfReview && !isSoloSelfReview)
      || (isSoloSelfReview && (!soloReviewConfirmed || !reason.trim()))
    ) return
    const reviewReason = isSoloSelfReview
      ? reason.trim()
      : decision === 'reject' ? t('conversation.reject.checkpoint') : ''
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    setError(null)
    try {
      const result = await client.decideSummaryCheckpoint(
        project.id,
        checkpoint.conversationId,
        checkpoint,
        decision,
        reviewReason,
        isSoloSelfReview && soloReviewConfirmed,
      )
      if (
        !sameSummaryCheckpointIdentity(result.data, checkpoint)
        || result.data.version <= checkpoint.version
        || result.data.status !== (decision === 'approve' ? 'approved' : 'rejected')
        || result.data.reviewedBy !== session.user.id
      ) {
        throw new Error(t('conversation.error.decisionMismatch'))
      }
      if (
        selectionEpoch !== conversationSelectionEpoch.current
        || conversationIdRef.current !== checkpoint.conversationId
      ) return
      setSummaryCheckpoints((current) => current.map((item) => (
        item.id === result.data.id ? result.data : item
      )))
      if (decision === 'approve') {
        setCheckpointNotice(
          t('conversation.notice.checkpointApproved', { sequence: formatNumber(result.data.throughSequence, locale) }),
        )
        await refresh()
      }
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === checkpoint.conversationId
      ) setError(message(cause, t('conversation.error.recordCheckpointDecision')))
    } finally {
      endBusy()
    }
  }

  async function decideProposal(proposal: WorkflowIntentProposalDto, decision: 'accept' | 'reject') {
    if (!project || !can('edit')) return
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    setError(null)
    try {
      await client.decideIntentProposal(
        project.id,
        proposal.conversationId,
        proposal,
        decision,
        decision === 'reject' ? t('conversation.reject.intent') : '',
      )
      await loadConversation(proposal.conversationId, selectionEpoch)
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === proposal.conversationId
      ) setError(message(cause, t('conversation.error.intentDecision')))
    } finally {
      endBusy()
    }
  }

  async function executeCommand(command: ConversationCommandDto) {
    if (!project || !can('edit') || command.status !== 'pending') return
    const selectionEpoch = conversationSelectionEpoch.current
    const commandIsCurrent = () => (
      selectionEpoch === conversationSelectionEpoch.current
      && conversationIdRef.current === command.conversationId
    )
    beginBusy()
    setError(null)
    try {
      if (command.kind === 'start_workflow') {
        const result = await client.executeCommand(project.id, command.conversationId, command)
        assertCommandResponseIdentity(result.data, command, t)
        if (result.data.status !== 'executed') {
          throw new Error(t('conversation.error.commandNotExecuted'))
        }
        const runId = stringField(result.data.result, 'runId')
        if (!runId || runId !== command.id) {
          throw new Error(t('conversation.error.unexpectedRun'))
        }
        if (!commandIsCurrent()) return
        const loadedRun = await flow.loadRun(runId)
        assertRunMatchesCommand(loadedRun, command, t)
      } else {
        const result = await client.executeCommand(project.id, command.conversationId, command)
        assertCommandResponseIdentity(result.data, command, t)
        if (result.data.status !== 'executed') {
          await loadConversation(command.conversationId, selectionEpoch)
          throw new Error(result.data.failure?.message ?? t('conversation.error.generationPending'))
        }
        const runId = stringField(result.data.result, 'runId')
        const rootBundleId = stringField(result.data.result, 'rootBundleId')
        const bundleId = stringField(result.data.result, 'bundleId')
        const proposalId = stringField(result.data.result, 'implementationProposalId')
        const instructionHash = stringField(result.data.result, 'instructionHash')
        const desiredOutputCapability = stringField(result.data.result, 'desiredOutputCapability')
        if (
          !runId
          || runId !== command.payload.workbench.expectedRunId
          || !rootBundleId
          || rootBundleId !== command.payload.workbench.expectedBundleId
          || !bundleId
          || !proposalId
          || proposalId !== command.id
          || !instructionHash
          || desiredOutputCapability !== command.payload.desiredOutputCapability
        ) {
          throw new Error(t('conversation.error.executionReceipt'))
        }
        if (!commandIsCurrent()) return
        const loadedRun = await flow.loadRun(runId)
        assertRunMatchesCommand(loadedRun, command, t)
        const reviewedSliceId = command.payload.workbench.sliceId?.trim() ?? ''
        const groupMatches = workflowWorkbenchQueueGroups(loadedRun).flatMap((group) =>
          group.references
            .filter((reference) => reference.bundleId === rootBundleId)
            .map((reference) => ({ group, reference })))
        if (groupMatches.length !== 1) {
          throw new Error(t('conversation.error.targetGroup'))
        }
        const [{ group: targetGroup, reference: targetReference }] = groupMatches
        if (
          !reviewedSliceId
          || targetReference.sliceId !== reviewedSliceId
        ) {
          throw new Error(t('conversation.error.pageMismatch'))
        }
        await flow.selectWorkbenchGroup(targetGroup.nodeKey)
        const loadedBundle = await flow.loadBundle(bundleId, {
          runId,
          rootBundleId,
          deliverySliceId: reviewedSliceId,
          ...(targetGroup.manifestGroupKey
            ? { manifestGroupKey: targetGroup.manifestGroupKey }
            : {}),
        })
        if (
          !loadedBundle
          || (loadedBundle.rootBuildManifestId || loadedBundle.id) !== rootBundleId
          || loadedBundle.workflowRunId !== runId
          || loadedBundle.deliverySliceId !== reviewedSliceId
          || (
            targetGroup.manifestGroupKey
            && loadedBundle.manifestGroupKey !== targetGroup.manifestGroupKey
          )
          || (
            loadedBundle.deliverySliceId
            && loadedBundle.workflowContext?.deliverySliceId
            && loadedBundle.deliverySliceId !== loadedBundle.workflowContext.deliverySliceId
          )
        ) {
          throw new Error(t('conversation.error.bundleMismatch'))
        }
        const loadedProposal = await flow.loadProposal(proposalId, {
          proposalId,
          buildManifestId: bundleId,
          runId,
          rootBundleId,
          deliverySliceId: reviewedSliceId,
          conversationCommandId: command.id,
          instructionHash,
          ...(targetGroup.manifestGroupKey
            ? { manifestGroupKey: targetGroup.manifestGroupKey }
            : {}),
        })
        if (
          !loadedProposal
          || loadedProposal.buildManifestId !== bundleId
          || loadedProposal.executionSource !== 'conversation_command'
          || loadedProposal.conversationCommandId !== command.id
          || loadedProposal.instructionHash !== instructionHash
        ) {
          throw new Error(t('conversation.error.proposalReceipt'))
        }
      }
      await loadConversation(command.conversationId, selectionEpoch)
    } catch (cause) {
      try {
        await loadConversation(command.conversationId, selectionEpoch)
      } catch {
        // Preserve the execution error; a later manual refresh can retry hydration.
      }
      if (commandIsCurrent()) {
        setError(message(cause, t('conversation.error.execute')))
      }
    } finally {
      endBusy()
    }
  }

  async function rejectCommand(command: ConversationCommandDto) {
    if (!project || !can('edit') || command.status !== 'pending') return
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    setError(null)
    try {
      await client.rejectCommand(
        project.id,
        command.conversationId,
        command,
        t('conversation.reject.command'),
      )
      await loadConversation(command.conversationId, selectionEpoch)
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === command.conversationId
      ) setError(message(cause, t('conversation.error.rejectCommand')))
    } finally {
      endBusy()
    }
  }

  async function archiveConversation() {
    if (!project || !selected || !can('edit')) return
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    try {
      await client.update(project.id, selected, { status: 'archived' })
      await refresh()
    } catch (cause) {
      if (selectionEpoch === conversationSelectionEpoch.current) {
        setError(message(cause, t('conversation.error.archive')))
      }
    } finally {
      endBusy()
    }
  }

  const proposalById = useMemo(() => new Map(proposals.map((item) => [item.id, item])), [proposals])
  const commandByProposal = useMemo(() => new Map(commands.map((item) => [item.proposalId, item])), [commands])
  const checkpointById = useMemo(() => new Map(summaryCheckpoints.map((item) => [item.id, item])), [summaryCheckpoints])
  const checkpointRetryReady = Boolean(
    checkpointRequirement
    && summaryCheckpoints.some((item) =>
      item.status === 'approved'
      && item.throughSequence >= checkpointRequirement.recommendedThroughSequence),
  )

  return (
    <aside className="absolute inset-y-0 left-0 z-50 flex w-[430px] max-w-full flex-col border-r border-border bg-panel shadow-2xl shadow-black/60 max-sm:w-full" aria-label={t('conversation.panelAria')}>
      <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border px-3">
        <Sparkles className="size-4 text-primary-bright" />
        <div className="min-w-0 flex-1"><h2 className="truncate text-xs font-semibold text-foreground">{t('conversation.title')}</h2><p className="truncate text-[8px] text-faint-foreground">{t('conversation.subtitle')}</p></div>
        <button type="button" onClick={() => void refresh()} disabled={busy} className="rounded p-1.5 text-faint-foreground hover:text-foreground" aria-label={t('conversation.refresh')}><RefreshCw className={cn('size-3.5', busy && 'animate-spin')} /></button>
        <button type="button" onClick={onClose} className="rounded p-1.5 text-faint-foreground hover:text-foreground" aria-label={t('conversation.close')}><X className="size-4" /></button>
      </header>

      {error && <div role="alert" className="flex items-start gap-2 border-b border-destructive/30 bg-destructive/10 px-3 py-2 text-[9px] leading-relaxed text-destructive"><CircleAlert className="mt-0.5 size-3 shrink-0" /><span className="min-w-0 flex-1">{error}</span><button type="button" onClick={() => setError(null)}><X className="size-3" /></button></div>}
      {checkpointNotice && <div role="status" className="flex items-start gap-2 border-b border-success/30 bg-success/10 px-3 py-2 text-[9px] leading-relaxed text-success"><Check className="mt-0.5 size-3 shrink-0" /><span className="min-w-0 flex-1">{checkpointNotice}</span><button type="button" onClick={() => setCheckpointNotice(null)} aria-label={t('conversation.dismissNotice')}><X className="size-3" /></button></div>}

      {!session.signedIn || !project ? (
        <PanelEmpty text={t('conversation.signInRequired')} />
      ) : (
        <>
          <div className="shrink-0 border-b border-border p-2">
            <div className="flex gap-1.5">
              <select value={conversationId} onChange={(event) => void selectConversation(event.target.value)} className="h-8 min-w-0 flex-1 rounded border border-border bg-background px-2 text-[9px] text-foreground"><option value="">{t('conversation.select')}</option>{conversations.map((item) => <option key={item.id} value={item.id}>{item.title} · {conversationStatusLabel(item.status, t)}</option>)}</select>
              <button type="button" onClick={() => setShowCreate((current) => !current)} disabled={busy || !can('edit')} className="flex size-8 items-center justify-center rounded border border-border text-faint-foreground disabled:opacity-35" aria-label={t('conversation.new')}><Plus className="size-3.5" /></button>
              {selected && <button type="button" onClick={() => void archiveConversation()} disabled={busy || selected.status === 'archived'} className="flex size-8 items-center justify-center rounded border border-border text-faint-foreground disabled:opacity-35" aria-label={t('conversation.archive')}><Archive className="size-3.5" /></button>}
            </div>
            {(showCreate || !selected) && <div className="mt-2 flex gap-1.5"><input value={newTitle} onChange={(event) => setNewTitle(event.target.value)} className="h-8 min-w-0 flex-1 rounded border border-border bg-background px-2 text-[9px] text-foreground" placeholder={t('conversation.titlePlaceholder')} /><button type="button" onClick={() => void createConversation()} disabled={busy || !can('edit') || !newTitle.trim()} className="inline-flex h-8 items-center gap-1 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground disabled:opacity-35"><Plus className="size-3" />{t('conversation.create')}</button>{selected && <button type="button" onClick={() => setShowCreate(false)} disabled={busy} className="h-8 rounded border border-border px-2 text-[8px] text-faint-foreground disabled:opacity-35">{t('conversation.cancel')}</button>}</div>}
          </div>

          <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-3 scrollbar-thin">
            {!selected && <PanelEmpty text={t('conversation.empty.noConversation')} />}
            {selected && messages.length === 0 && <PanelEmpty text={t('conversation.empty.noMessages')} />}
            {selected && checkpointRequirement && (
              <section className="rounded-lg border border-warning/40 bg-warning/10 p-3" aria-label={t('conversation.checkpoint.requiredAria')}>
                <div className="flex items-start gap-2">
                  <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-warning" />
                  <div className="min-w-0 flex-1">
                    <h3 className="text-[9px] font-semibold text-foreground">{t('conversation.checkpoint.requiredTitle')}</h3>
                    <p className="mt-1 text-[8px] leading-relaxed text-muted-foreground">
                      {t('conversation.checkpoint.requiredDescription', {
                        bytes: formatNumber(checkpointRequirement.contextBytes, locale),
                        count: formatNumber(checkpointRequirement.messageCount, locale),
                        sequence: formatNumber(checkpointRequirement.recommendedThroughSequence, locale),
                      })}
                    </p>
                  </div>
                </div>
                {checkpointRetryReady ? (
                  <button type="button" onClick={() => void generateIntent(checkpointRequirement.triggerMessageId)} disabled={busy || !can('edit')} className="mt-2 inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[8px] font-semibold text-primary-foreground disabled:opacity-35">
                    <RefreshCw className="size-3" />{t('conversation.checkpoint.retry')}
                  </button>
                ) : (
                  <>
                    <textarea value={checkpointSummary} onChange={(event) => setCheckpointSummary(event.target.value)} rows={5} maxLength={32768} placeholder={t('conversation.checkpoint.placeholder')} className="mt-2 min-h-24 w-full resize-y rounded border border-warning/30 bg-background p-2 text-[9px] text-foreground" aria-label={t('conversation.checkpoint.summaryAria')} />
                    <button type="button" onClick={() => void createSummaryCheckpoint()} disabled={busy || !can('edit') || !checkpointSummary.trim()} className="mt-2 inline-flex h-7 w-full items-center justify-center gap-1 rounded border border-warning/40 bg-warning/15 text-[8px] font-semibold text-warning disabled:opacity-35">
                      <Check className="size-3" />{t('conversation.checkpoint.submit')}
                    </button>
                  </>
                )}
              </section>
            )}
            {selected && summaryCheckpoints.map((checkpoint) => <SummaryCheckpointCard key={checkpoint.id} checkpoint={checkpoint} previous={checkpoint.previousCheckpointId ? checkpointById.get(checkpoint.previousCheckpointId) : undefined} source={checkpointSources[checkpoint.id]} busy={busy} canReview={can('review')} currentUserId={currentUserId} soloSelfReviewAllowed={Boolean(project && canSoloSelfReview(project.governanceMode, members, currentUserId, checkpoint.createdBy))} onInspect={loadSummaryCheckpointSource} onDecide={decideSummaryCheckpoint} />)}
            {messages.map((item) => {
              const linkedProposal = item.proposalId ? proposalById.get(item.proposalId) : undefined
              const linkedCommand = linkedProposal ? commandByProposal.get(linkedProposal.id) : undefined
              return <article key={item.id} className={cn('rounded-lg border p-2.5', item.role === 'user' ? 'ml-8 border-primary/25 bg-primary/8' : 'mr-4 border-border bg-background')}><div className="flex items-center gap-1.5 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">{item.role === 'user' ? <MessageSquare className="size-3" /> : <Bot className="size-3 text-primary-bright" />}{conversationRoleLabel(item.role, t)}<span className="ml-auto font-mono">#{formatNumber(item.sequence, locale)}</span></div><p className="mt-2 whitespace-pre-wrap text-[10px] leading-relaxed text-muted-foreground">{item.content}</p>{item.role === 'user' && !proposals.some((proposal) => proposal.triggerMessageId === item.id) && <button type="button" onClick={() => void generateIntent(item.id)} disabled={busy || !can('edit')} className="mt-2 inline-flex h-7 items-center gap-1 rounded border border-primary/30 bg-primary/10 px-2 text-[8px] font-semibold text-primary-bright disabled:opacity-35"><Sparkles className="size-3" />{briefNeedsCheckpoint ? t('conversation.generate.withCheckpoint') : t('conversation.generate.intent')}</button>}{linkedProposal && <IntentCard proposal={linkedProposal} command={linkedCommand} busy={busy} canEdit={can('edit')} onDecide={decideProposal} onExecute={executeCommand} onRejectCommand={rejectCommand} />}</article>
            })}
            {commands.filter((command) => !proposals.some((proposal) => proposal.id === command.proposalId && messages.some((item) => item.proposalId === proposal.id))).map((command) => <CommandCard key={command.id} command={command} busy={busy} canEdit={can('edit')} onExecute={executeCommand} onReject={rejectCommand} />)}
          </div>

          {selected?.status === 'active' && <footer className="shrink-0 border-t border-border p-3"><div className="mb-2 flex items-center gap-1.5"><label className="text-[8px] text-faint-foreground">{t('conversation.model')}</label><input value={model} onChange={(event) => setModel(event.target.value)} className="h-7 w-28 rounded border border-border bg-background px-2 text-[8px] text-foreground" /><span className="ml-auto text-[8px] text-faint-foreground">{t('conversation.noDirectExecution')}</span></div><div className="flex items-end gap-1.5"><textarea value={draft} onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); void sendMessage() } }} rows={3} placeholder={t('conversation.messagePlaceholder')} className="min-h-20 min-w-0 flex-1 resize-none rounded border border-border bg-background p-2 text-[10px] text-foreground" /><button type="button" onClick={() => void sendMessage()} disabled={busy || !can('comment') || !draft.trim()} className="flex size-9 items-center justify-center rounded bg-primary text-primary-foreground disabled:opacity-35" aria-label={t('conversation.sendAria')}>{busy ? <LoaderCircle className="size-4 animate-spin" /> : <Send className="size-4" />}</button></div></footer>}
        </>
      )}
    </aside>
  )
}

function SummaryCheckpointCard({
  checkpoint,
  previous,
  source,
  busy,
  canReview,
  currentUserId,
  soloSelfReviewAllowed,
  onInspect,
  onDecide,
}: {
  checkpoint: ConversationSummaryCheckpointDto
  previous?: ConversationSummaryCheckpointDto
  source?: readonly ConversationMessageDto[]
  busy: boolean
  canReview: boolean
  currentUserId: string
  soloSelfReviewAllowed: boolean
  onInspect: (checkpoint: ConversationSummaryCheckpointDto) => Promise<void>
  onDecide: (
    checkpoint: ConversationSummaryCheckpointDto,
    decision: 'approve' | 'reject',
    reason?: string,
    soloReviewConfirmed?: boolean,
  ) => Promise<void>
}) {
  const { locale, t } = useI18n()
  const [reviewReason, setReviewReason] = useState('')
  const [soloReviewConfirmed, setSoloReviewConfirmed] = useState(false)
  const isCurrentUserAuthor = checkpoint.createdBy === currentUserId
  const isSoloSelfReview = isCurrentUserAuthor && soloSelfReviewAllowed
  const authorCannotReview = isCurrentUserAuthor && !isSoloSelfReview
  const reviewReady = checkpoint.status === 'pending_review'
    && canReview
    && !authorCannotReview
    && source !== undefined
  const decisionReady = reviewReady
    && (!isSoloSelfReview || Boolean(reviewReason.trim() && soloReviewConfirmed))
  const sequence = formatNumber(checkpoint.throughSequence, locale)
  return (
    <section className="rounded-lg border border-border bg-background p-2.5" aria-label={t('conversation.checkpoint.cardAria', { sequence })}>
      <div className="flex items-center gap-1.5">
        <Check className={cn('size-3', checkpoint.status === 'approved' ? 'text-success' : 'text-faint-foreground')} />
        <span className="text-[9px] font-semibold text-foreground">{t('conversation.checkpoint.cardTitle', { sequence })}</span>
        <span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 text-[7px] font-semibold uppercase text-faint-foreground">{conversationStatusLabel(checkpoint.status, t)}</span>
      </div>
      <p className="mt-2 whitespace-pre-wrap text-[9px] leading-relaxed text-muted-foreground">{checkpoint.summary}</p>
      {previous && (
        <details className="mt-2 rounded border border-border/70">
          <summary className="cursor-pointer px-2 py-1 text-[7px] text-faint-foreground">{t('conversation.checkpoint.previous', { sequence: formatNumber(previous.throughSequence, locale) })}</summary>
          <p className="border-t border-border/70 p-2 text-[8px] leading-relaxed text-muted-foreground">{previous.summary}</p>
        </details>
      )}
      <div className="mt-2 space-y-0.5 font-mono text-[7px] text-faint-foreground">
        <p>{t('conversation.checkpoint.prefix')} {checkpoint.prefixHash}</p>
        <p>{t('conversation.checkpoint.summary')} {checkpoint.summaryHash}</p>
        <p>{t('conversation.checkpoint.metrics', { count: formatNumber(checkpoint.messageCount, locale), bytes: formatNumber(checkpoint.contentBytes, locale) })}</p>
      </div>
      {source === undefined ? (
        <button type="button" onClick={() => void onInspect(checkpoint)} disabled={busy} className="mt-2 inline-flex h-7 w-full items-center justify-center gap-1 rounded border border-border text-[8px] font-semibold text-muted-foreground disabled:opacity-35">
          <MessageSquare className="size-3" />{t('conversation.checkpoint.inspect')}
        </button>
      ) : (
        <details open={checkpoint.status === 'pending_review'} className="mt-2 rounded border border-border/70">
          <summary className="cursor-pointer px-2 py-1 text-[7px] text-faint-foreground">{t('conversation.checkpoint.source', { count: formatNumber(source.length, locale) })}</summary>
          <div className="max-h-48 space-y-1 overflow-y-auto border-t border-border/70 p-2 scrollbar-thin">
            {source.map((item) => (
              <div key={item.id} className="rounded bg-black/15 p-1.5">
                <div className="font-mono text-[7px] text-faint-foreground">#{formatNumber(item.sequence, locale)} · {conversationRoleLabel(item.role, t)}</div>
                <p className="mt-1 whitespace-pre-wrap text-[8px] leading-relaxed text-muted-foreground">{item.content}</p>
              </div>
            ))}
          </div>
        </details>
      )}
      {checkpoint.status === 'pending_review' && (
        <>
          {authorCannotReview && <p className="mt-2 text-[8px] text-warning">{t('conversation.checkpoint.authorCannotReview')}</p>}
          {isSoloSelfReview && (
            <div role="alert" className="mt-2 rounded border border-warning/35 bg-warning/10 p-2 text-[8px] leading-relaxed text-warning" data-testid={`solo-checkpoint-review-${checkpoint.id}`}>
              <div className="flex items-start gap-1.5">
                <CircleAlert className="mt-0.5 size-3 shrink-0" />
                <span>{t('conversation.checkpoint.soloReviewWarning')}</span>
              </div>
              <input
                value={reviewReason}
                onChange={(event) => setReviewReason(event.target.value)}
                maxLength={1000}
                placeholder={t('conversation.checkpoint.reviewReasonPlaceholder')}
                aria-label={t('conversation.checkpoint.reviewReasonAria')}
                className="mt-2 h-7 w-full rounded border border-warning/30 bg-background px-2 text-[8px] text-foreground outline-none placeholder:text-faint-foreground"
              />
              <label className="mt-2 flex cursor-pointer items-start gap-1.5 text-foreground">
                <input
                  type="checkbox"
                  checked={soloReviewConfirmed}
                  onChange={(event) => setSoloReviewConfirmed(event.target.checked)}
                  className="mt-0.5"
                  data-testid={`solo-checkpoint-confirm-${checkpoint.id}`}
                />
                <span>{t('conversation.checkpoint.soloReviewConfirm')}</span>
              </label>
            </div>
          )}
          {!source && <p className="mt-2 text-[8px] text-faint-foreground">{t('conversation.checkpoint.inspectBeforeDecision')}</p>}
          <div className="mt-2 grid grid-cols-2 gap-1">
            <button type="button" onClick={() => void onDecide(checkpoint, 'approve', reviewReason, soloReviewConfirmed)} disabled={busy || !decisionReady} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[8px] font-semibold text-success disabled:opacity-35" aria-label={t('conversation.checkpoint.approveAria', { sequence })}>
              <Check className="size-3" />{t('common.approve')}
            </button>
            <button type="button" onClick={() => void onDecide(checkpoint, 'reject', reviewReason, soloReviewConfirmed)} disabled={busy || !decisionReady} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-destructive/10 text-[8px] font-semibold text-destructive disabled:opacity-35" aria-label={t('conversation.checkpoint.rejectAria', { sequence })}>
              <X className="size-3" />{t('conversation.intent.reject')}
            </button>
          </div>
        </>
      )}
    </section>
  )
}

function IntentCard({ proposal, command, busy, canEdit, onDecide, onExecute, onRejectCommand }: { proposal: WorkflowIntentProposalDto; command?: ConversationCommandDto; busy: boolean; canEdit: boolean; onDecide: (proposal: WorkflowIntentProposalDto, decision: 'accept' | 'reject') => Promise<void>; onExecute: (command: ConversationCommandDto) => Promise<void>; onRejectCommand: (command: ConversationCommandDto) => Promise<void> }) {
  const { t } = useI18n()
  const slice = proposal.kind === 'workbench_instruction' ? proposal.workbenchInstruction : undefined
  return <div className="mt-2 rounded border border-primary/25 bg-primary/5 p-2"><div className="flex items-center gap-1.5"><Workflow className="size-3 text-primary-bright" /><span className="text-[9px] font-semibold text-foreground">{proposal.kind === 'start_workflow' ? t('conversation.intent.startWorkflow') : t('conversation.intent.workbenchInstruction')}</span><span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 text-[7px] font-semibold uppercase text-faint-foreground">{conversationStatusLabel(proposal.status, t)}</span></div>{slice?.sliceTitle && <div className="mt-1 rounded border border-primary/20 bg-background px-2 py-1"><span className="text-[8px] font-semibold text-foreground">{t('conversation.intent.page', { title: slice.sliceTitle })}</span>{slice.sliceKey && <span className="ml-1.5 font-mono text-[7px] text-faint-foreground">{slice.sliceKey}</span>}</div>}<p className="mt-1 text-[8px] leading-relaxed text-muted-foreground">{proposal.workbenchInstruction.objective || t('conversation.intent.useDefinition', { id: proposal.suggestedDefinitionVersionId })}</p><div className="mt-1 truncate font-mono text-[7px] text-faint-foreground">{t('conversation.intent.provenance', { definition: proposal.suggestedDefinitionVersionId, manifest: proposal.manifestIntent.inputManifest.id })}</div><details className="mt-1 rounded border border-border/70 bg-background"><summary className="cursor-pointer px-2 py-1 text-[7px] text-faint-foreground">{t('conversation.intent.inspect')}</summary><pre className="max-h-40 overflow-auto whitespace-pre-wrap border-t border-border/70 p-2 font-mono text-[7px] leading-relaxed text-faint-foreground scrollbar-thin">{JSON.stringify({ scope: proposal.scope, sourceRefs: proposal.sourceRefs, manifestIntent: proposal.manifestIntent, workbenchInstruction: proposal.workbenchInstruction, conversationContext: proposal.conversationContext }, null, 2)}</pre></details>{proposal.status === 'pending' && <div className="mt-2 grid grid-cols-2 gap-1"><button type="button" onClick={() => void onDecide(proposal, 'accept')} disabled={busy || !canEdit} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[8px] font-semibold text-success disabled:opacity-35"><Check className="size-3" />{t('conversation.intent.accept')}</button><button type="button" onClick={() => void onDecide(proposal, 'reject')} disabled={busy || !canEdit} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-destructive/10 text-[8px] font-semibold text-destructive disabled:opacity-35"><X className="size-3" />{t('conversation.intent.reject')}</button></div>}{command && <CommandCard command={command} busy={busy} canEdit={canEdit} onExecute={onExecute} onReject={onRejectCommand} />}</div>
}

function CommandCard({ command, busy, canEdit, onExecute, onReject }: { command: ConversationCommandDto; busy: boolean; canEdit: boolean; onExecute: (command: ConversationCommandDto) => Promise<void>; onReject: (command: ConversationCommandDto) => Promise<void> }) {
  const { t } = useI18n()
  return <div className="mt-2 rounded border border-border bg-background p-2"><div className="flex items-center gap-1"><span className="text-[8px] font-semibold text-foreground">{t('conversation.command.title')}</span><code className="ml-auto text-[7px] text-faint-foreground">{conversationStatusLabel(command.status, t)}</code></div>{command.failure && <p className="mt-1 text-[8px] text-destructive">{command.failure.code}: {command.failure.message}</p>}{command.result && <pre className="mt-1 whitespace-pre-wrap rounded bg-black/20 p-1.5 font-mono text-[7px] text-faint-foreground">{JSON.stringify(command.result, null, 2)}</pre>}{command.status === 'pending' && <div className="mt-2 grid grid-cols-[1fr_auto] gap-1"><button type="button" onClick={() => void onExecute(command)} disabled={busy || !canEdit} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-primary text-[8px] font-semibold text-primary-foreground disabled:opacity-35"><Send className="size-3" />{command.kind === 'start_workflow' ? t('conversation.command.executeRun') : t('conversation.command.generateProposal')}</button><button type="button" onClick={() => void onReject(command)} disabled={busy || !canEdit} className="flex size-7 items-center justify-center rounded border border-destructive/30 text-destructive disabled:opacity-35" aria-label={t('conversation.command.rejectAria')}><X className="size-3" /></button></div>}</div>
}

function PanelEmpty({ text }: { text: string }) {
  return <div className="m-auto flex max-w-sm flex-1 items-center justify-center p-6 text-center"><p className="rounded-lg border border-dashed border-border bg-background p-5 text-[9px] leading-relaxed text-faint-foreground">{text}</p></div>
}

function stringField(value: unknown, key: string) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return ''
  const field = (value as Record<string, unknown>)[key]
  return typeof field === 'string' ? field : ''
}

function conversationCheckpointRequirement(
  cause: unknown,
): ConversationSummaryCheckpointRequiredExtensionsDto | null {
  if (
    !(cause instanceof PlatformHttpError)
    || cause.code !== 'conversation_summary_checkpoint_required'
  ) return null
  const extensions = cause.problem.extensions
  if (!extensions) return null
  const triggerMessageId = scalarString(extensions.triggerMessageId)
  const triggerSequence = scalarNumber(extensions.triggerSequence)
  const messageCount = scalarNumber(extensions.messageCount)
  const messageContentBytes = scalarNumber(extensions.messageContentBytes)
  const contextBytes = scalarNumber(extensions.contextBytes)
  const recommendedThroughMessageId = scalarString(extensions.recommendedThroughMessageId)
  const recommendedThroughSequence = scalarNumber(extensions.recommendedThroughSequence)
  const createHref = scalarString(extensions.createHref)
  if (
    !triggerMessageId
    || triggerSequence < 1
    || messageCount < 1
    || contextBytes < 1
    || !recommendedThroughMessageId
    || recommendedThroughSequence < 1
    || !createHref
  ) return null
  const currentApprovedCheckpointId = scalarString(extensions.currentApprovedCheckpointId)
  const currentThroughSequence = scalarNumber(extensions.currentThroughSequence)
  return {
    triggerMessageId,
    triggerSequence,
    messageCount,
    messageContentBytes,
    contextBytes,
    recommendedThroughMessageId,
    recommendedThroughSequence,
    createHref,
    ...(currentApprovedCheckpointId ? { currentApprovedCheckpointId } : {}),
    ...(currentThroughSequence > 0 ? { currentThroughSequence } : {}),
  }
}

function scalarString(value: unknown) {
  return typeof value === 'string' ? value : ''
}

function scalarNumber(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function sameSummaryCheckpointIdentity(
  left: ConversationSummaryCheckpointDto,
  right: ConversationSummaryCheckpointDto,
) {
  return left.id === right.id
    && left.projectId === right.projectId
    && left.conversationId === right.conversationId
    && (left.previousCheckpointId ?? '') === (right.previousCheckpointId ?? '')
    && left.throughMessageId === right.throughMessageId
    && left.throughSequence === right.throughSequence
    && left.messageCount === right.messageCount
    && left.contentBytes === right.contentBytes
    && left.prefixHash === right.prefixHash
    && left.hashAlgorithm === right.hashAlgorithm
    && left.summary === right.summary
    && left.summaryHash === right.summaryHash
    && left.createdBy === right.createdBy
    && left.createdAt === right.createdAt
}

function assertCommandResponseIdentity(
  response: ConversationCommandDto,
  command: ConversationCommandDto,
  t: ReturnType<typeof useI18n>['t'],
) {
  if (
    response.id !== command.id
    || response.projectId !== command.projectId
    || response.conversationId !== command.conversationId
    || response.proposalId !== command.proposalId
    || response.kind !== command.kind
  ) throw new Error(t('conversation.error.commandResponse'))
}

function assertRunMatchesCommand(
  run: WorkflowRunDto | null,
  command: ConversationCommandDto,
  t: ReturnType<typeof useI18n>['t'],
): asserts run is WorkflowRunDto {
  if (!run || run.id !== (command.kind === 'start_workflow' ? command.id : command.payload.workbench.expectedRunId)) {
    throw new Error(t('conversation.error.runNotLoaded'))
  }
  if (run.definitionVersionId !== command.payload.definitionVersionId) {
    throw new Error(t('conversation.error.definitionMismatch'))
  }
  if (command.kind === 'start_workflow') {
    const expectedManifest = command.payload.manifestIntent.inputManifest
    if (
      run.inputManifest?.id !== expectedManifest.id
      || run.inputManifest?.hash !== expectedManifest.hash
    ) {
      throw new Error(t('conversation.error.manifestMismatch'))
    }
  }
}

function formatNumber(value: number, locale: string) {
  return new Intl.NumberFormat(locale).format(value)
}

function conversationStatusLabel(status: string, t: ReturnType<typeof useI18n>['t']) {
  if (status === 'active') return t('workbenchPlatform.status.active')
  if (status === 'archived') return t('workbenchPlatform.status.archived')
  if (status === 'pending') return t('workbenchPlatform.status.pending')
  if (status === 'pending_review') return t('workbenchPlatform.status.pendingReview')
  if (status === 'approved' || status === 'accepted') return t('workbenchPlatform.status.approved')
  if (status === 'rejected') return t('workbenchPlatform.status.rejected')
  if (status === 'executed') return t('workbenchPlatform.status.executed')
  if (status === 'failed') return t('workbenchPlatform.status.failed')
  return status.replaceAll('_', ' ')
}

function conversationRoleLabel(role: ConversationMessageDto['role'], t: ReturnType<typeof useI18n>['t']) {
  return role === 'user'
    ? t('workbenchPlatform.role.user')
    : t('workbenchPlatform.role.assistant')
}

function conversationReference() {
  if (typeof window === 'undefined') return ''
  return new URLSearchParams(window.location.search).get('conversationId') ?? ''
}

function setConversationReference(conversationId: string) {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  url.searchParams.set('conversationId', conversationId)
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}

function message(cause: unknown, fallback: string) {
  return cause instanceof Error ? cause.message : fallback
}
