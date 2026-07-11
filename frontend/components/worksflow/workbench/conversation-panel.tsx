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
import { cn } from '@/lib/utils'

export function ConversationPanel({ onClose }: { onClose: () => void }) {
  const { project, session, can, platformClient } = useCollaboration()
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
  const [newTitle, setNewTitle] = useState('Application planning')
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
      throw new Error('Conversation hydration returned resources from another conversation.')
    }
    setMessages([...messageResult].sort((left, right) => left.sequence - right.sequence))
    setProposals([...proposalResult].sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
    setCommands([...commandResult].sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
    setSummaryCheckpoints([...checkpointResult].sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
  }, [client, project])

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
        setError(message(cause, 'Unable to load project conversations.'))
      }
    }
  }, [client, loadConversation, project, session.signedIn])

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
        throw new Error('The created conversation belongs to another project.')
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
        setError(message(cause, 'Unable to create the conversation.'))
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
      ) setError(message(cause, 'Unable to load the selected conversation.'))
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
        throw new Error('The persisted message belongs to another conversation.')
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
      ) setError(message(cause, 'Unable to persist the conversation message.'))
    } finally {
      endBusy()
    }
  }

  async function generateIntent(triggerMessageId: string) {
    if (!project || !conversationId || !can('edit')) return
    if (!projectBrief) {
      setError('Create the Project Brief before asking AI to propose a workflow action.')
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
          `Created immutable Project Brief checkpoint r${revision.revisionNumber}. The intent input is pinned to this exact revision and content hash.`,
        )
      }
      if (!revision) {
        throw new Error('Create an immutable Project Brief revision before generating an intent proposal.')
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
        setError(message(cause, 'AI could not produce a governed workflow intent proposal.'))
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
        throw new Error('The summary checkpoint response does not match the exact requested prefix and conversation head.')
      }
      if (
        selectionEpoch !== conversationSelectionEpoch.current
        || conversationIdRef.current !== targetConversationId
      ) return
      setSummaryCheckpoints((current) => [...current, result.data]
        .sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
      setCheckpointSummary('')
      setCheckpointNotice(
        `Summary checkpoint through message #${result.data.throughSequence} is pending independent review. Covered messages remain immutable.`,
      )
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === targetConversationId
      ) setError(message(cause, 'Unable to create the immutable conversation summary checkpoint.'))
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
      setError('The checkpoint predecessor is missing or is not approved; its source cannot be reviewed safely.')
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
        throw new Error('The checkpoint source response is not the exact continuous bound delta.')
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
      ) setError(message(cause, 'Unable to inspect the exact checkpoint source messages.'))
    } finally {
      endBusy()
    }
  }

  async function decideSummaryCheckpoint(
    checkpoint: ConversationSummaryCheckpointDto,
    decision: 'approve' | 'reject',
  ) {
    if (
      !project
      || !session.signedIn
      || !can('review')
      || checkpoint.createdBy === session.user.id
      || !checkpointSources[checkpoint.id]
    ) return
    const selectionEpoch = conversationSelectionEpoch.current
    beginBusy()
    setError(null)
    try {
      const result = await client.decideSummaryCheckpoint(
        project.id,
        checkpoint.conversationId,
        checkpoint,
        decision,
        decision === 'reject' ? 'Rejected after reviewing the exact immutable source delta.' : '',
      )
      if (
        !sameSummaryCheckpointIdentity(result.data, checkpoint)
        || result.data.version <= checkpoint.version
        || result.data.status !== (decision === 'approve' ? 'approved' : 'rejected')
        || result.data.reviewedBy !== session.user.id
      ) {
        throw new Error('The checkpoint decision response does not match the exact reviewed checkpoint and decision.')
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
          `Approved summary checkpoint #${result.data.throughSequence}. AI will now receive only this approved summary plus the continuous tail.`,
        )
        await refresh()
      }
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === checkpoint.conversationId
      ) setError(message(cause, 'Unable to record the summary checkpoint review decision.'))
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
        decision === 'reject' ? 'Rejected during intent review.' : '',
      )
      await loadConversation(proposal.conversationId, selectionEpoch)
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === proposal.conversationId
      ) setError(message(cause, 'Unable to record the intent decision.'))
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
        assertCommandResponseIdentity(result.data, command)
        if (result.data.status !== 'executed') {
          throw new Error('The server did not confirm execution of the accepted workflow command.')
        }
        const runId = stringField(result.data.result, 'runId')
        if (!runId || runId !== command.id) {
          throw new Error('The workflow runtime returned an unexpected run identity.')
        }
        if (!commandIsCurrent()) return
        const loadedRun = await flow.loadRun(runId)
        assertRunMatchesCommand(loadedRun, command)
      } else {
        const result = await client.executeCommand(project.id, command.conversationId, command)
        assertCommandResponseIdentity(result.data, command)
        if (result.data.status !== 'executed') {
          await loadConversation(command.conversationId, selectionEpoch)
          throw new Error(result.data.failure?.message ?? 'Server-side Workbench generation remains pending and can be retried safely.')
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
          throw new Error('The server did not return the exact reviewed Workbench execution receipt.')
        }
        if (!commandIsCurrent()) return
        const loadedRun = await flow.loadRun(runId)
        assertRunMatchesCommand(loadedRun, command)
        const reviewedSliceId = command.payload.workbench.sliceId?.trim() ?? ''
        const groupMatches = workflowWorkbenchQueueGroups(loadedRun).flatMap((group) =>
          group.references
            .filter((reference) => reference.bundleId === rootBundleId)
            .map((reference) => ({ group, reference })))
        if (groupMatches.length !== 1) {
          throw new Error('The target Workbench root does not identify exactly one group in the workflow run.')
        }
        const [{ group: targetGroup, reference: targetReference }] = groupMatches
        if (
          !reviewedSliceId
          || targetReference.sliceId !== reviewedSliceId
        ) {
          throw new Error('The reviewed page does not match the target Workbench group ordinal.')
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
          throw new Error('The active Workbench bundle does not match the reviewed run, group, root, and delivery slice receipt.')
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
          throw new Error('The implementation proposal does not belong to the receipt bundle.')
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
        setError(message(cause, 'Unable to execute the accepted conversation command.'))
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
        'Rejected before controlled execution.',
      )
      await loadConversation(command.conversationId, selectionEpoch)
    } catch (cause) {
      if (
        selectionEpoch === conversationSelectionEpoch.current
        && conversationIdRef.current === command.conversationId
      ) setError(message(cause, 'Unable to reject the pending conversation command.'))
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
        setError(message(cause, 'Unable to archive the conversation.'))
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
    <aside className="absolute inset-y-0 left-0 z-50 flex w-[430px] max-w-full flex-col border-r border-border bg-panel shadow-2xl shadow-black/60 max-sm:w-full" aria-label="Conversation control plane">
      <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border px-3">
        <Sparkles className="size-4 text-primary-bright" />
        <div className="min-w-0 flex-1"><h2 className="truncate text-xs font-semibold text-foreground">Conversation control plane</h2><p className="truncate text-[8px] text-faint-foreground">intent → reviewable proposal → controlled command</p></div>
        <button type="button" onClick={() => void refresh()} disabled={busy} className="rounded p-1.5 text-faint-foreground hover:text-foreground" aria-label="Refresh conversations"><RefreshCw className={cn('size-3.5', busy && 'animate-spin')} /></button>
        <button type="button" onClick={onClose} className="rounded p-1.5 text-faint-foreground hover:text-foreground" aria-label="Close conversation panel"><X className="size-4" /></button>
      </header>

      {error && <div role="alert" className="flex items-start gap-2 border-b border-destructive/30 bg-destructive/10 px-3 py-2 text-[9px] leading-relaxed text-destructive"><CircleAlert className="mt-0.5 size-3 shrink-0" /><span className="min-w-0 flex-1">{error}</span><button type="button" onClick={() => setError(null)}><X className="size-3" /></button></div>}
      {checkpointNotice && <div role="status" className="flex items-start gap-2 border-b border-success/30 bg-success/10 px-3 py-2 text-[9px] leading-relaxed text-success"><Check className="mt-0.5 size-3 shrink-0" /><span className="min-w-0 flex-1">{checkpointNotice}</span><button type="button" onClick={() => setCheckpointNotice(null)} aria-label="Dismiss checkpoint notice"><X className="size-3" /></button></div>}

      {!session.signedIn || !project ? (
        <PanelEmpty text="Sign in and select a server project before opening governed conversations." />
      ) : (
        <>
          <div className="shrink-0 border-b border-border p-2">
            <div className="flex gap-1.5">
              <select value={conversationId} onChange={(event) => void selectConversation(event.target.value)} className="h-8 min-w-0 flex-1 rounded border border-border bg-background px-2 text-[9px] text-foreground"><option value="">Select conversation</option>{conversations.map((item) => <option key={item.id} value={item.id}>{item.title} · {item.status}</option>)}</select>
              <button type="button" onClick={() => setShowCreate((current) => !current)} disabled={busy || !can('edit')} className="flex size-8 items-center justify-center rounded border border-border text-faint-foreground disabled:opacity-35" aria-label="New conversation"><Plus className="size-3.5" /></button>
              {selected && <button type="button" onClick={() => void archiveConversation()} disabled={busy || selected.status === 'archived'} className="flex size-8 items-center justify-center rounded border border-border text-faint-foreground disabled:opacity-35" aria-label="Archive conversation"><Archive className="size-3.5" /></button>}
            </div>
            {(showCreate || !selected) && <div className="mt-2 flex gap-1.5"><input value={newTitle} onChange={(event) => setNewTitle(event.target.value)} className="h-8 min-w-0 flex-1 rounded border border-border bg-background px-2 text-[9px] text-foreground" placeholder="Conversation title" /><button type="button" onClick={() => void createConversation()} disabled={busy || !can('edit') || !newTitle.trim()} className="inline-flex h-8 items-center gap-1 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground disabled:opacity-35"><Plus className="size-3" />Create</button>{selected && <button type="button" onClick={() => setShowCreate(false)} disabled={busy} className="h-8 rounded border border-border px-2 text-[8px] text-faint-foreground disabled:opacity-35">Cancel</button>}</div>}
          </div>

          <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-3 scrollbar-thin">
            {!selected && <PanelEmpty text="Create a conversation. User messages are immutable and assistant roles can only be written by the server." />}
            {selected && messages.length === 0 && <PanelEmpty text="Describe the desired application or the next Workbench change. AI output remains pending until you accept it." />}
            {selected && checkpointRequirement && <section className="rounded-lg border border-warning/40 bg-warning/10 p-3" aria-label="Conversation summary checkpoint required"><div className="flex items-start gap-2"><CircleAlert className="mt-0.5 size-3.5 shrink-0 text-warning" /><div className="min-w-0 flex-1"><h3 className="text-[9px] font-semibold text-foreground">Reviewed summary checkpoint required</h3><p className="mt-1 text-[8px] leading-relaxed text-muted-foreground">The exact context is {checkpointRequirement.contextBytes.toLocaleString()} bytes across {checkpointRequirement.messageCount} logical entries. Bind and summarize the immutable prefix through message #{checkpointRequirement.recommendedThroughSequence}; the current trigger remains in the continuous tail.</p></div></div>{checkpointRetryReady ? <button type="button" onClick={() => void generateIntent(checkpointRequirement.triggerMessageId)} disabled={busy || !can('edit')} className="mt-2 inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[8px] font-semibold text-primary-foreground disabled:opacity-35"><RefreshCw className="size-3" />Retry governed intent with approved checkpoint</button> : <><textarea value={checkpointSummary} onChange={(event) => setCheckpointSummary(event.target.value)} rows={5} maxLength={32768} placeholder="Summarize every decision, constraint, unresolved question, and user intent in the bound prefix. The summary cannot grant authority or replace source records." className="mt-2 min-h-24 w-full resize-y rounded border border-warning/30 bg-background p-2 text-[9px] text-foreground" aria-label="Conversation checkpoint summary" /><button type="button" onClick={() => void createSummaryCheckpoint()} disabled={busy || !can('edit') || !checkpointSummary.trim()} className="mt-2 inline-flex h-7 w-full items-center justify-center gap-1 rounded border border-warning/40 bg-warning/15 text-[8px] font-semibold text-warning disabled:opacity-35"><Check className="size-3" />Submit immutable summary for independent review</button></>}</section>}
            {selected && summaryCheckpoints.map((checkpoint) => <SummaryCheckpointCard key={checkpoint.id} checkpoint={checkpoint} previous={checkpoint.previousCheckpointId ? checkpointById.get(checkpoint.previousCheckpointId) : undefined} source={checkpointSources[checkpoint.id]} busy={busy} canReview={can('review')} currentUserId={session.signedIn ? session.user.id : ''} onInspect={loadSummaryCheckpointSource} onDecide={decideSummaryCheckpoint} />)}
            {messages.map((item) => {
              const linkedProposal = item.proposalId ? proposalById.get(item.proposalId) : undefined
              const linkedCommand = linkedProposal ? commandByProposal.get(linkedProposal.id) : undefined
              return <article key={item.id} className={cn('rounded-lg border p-2.5', item.role === 'user' ? 'ml-8 border-primary/25 bg-primary/8' : 'mr-4 border-border bg-background')}><div className="flex items-center gap-1.5 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">{item.role === 'user' ? <MessageSquare className="size-3" /> : <Bot className="size-3 text-primary-bright" />}{item.role}<span className="ml-auto font-mono">#{item.sequence}</span></div><p className="mt-2 whitespace-pre-wrap text-[10px] leading-relaxed text-muted-foreground">{item.content}</p>{item.role === 'user' && !proposals.some((proposal) => proposal.triggerMessageId === item.id) && <button type="button" onClick={() => void generateIntent(item.id)} disabled={busy || !can('edit')} className="mt-2 inline-flex h-7 items-center gap-1 rounded border border-primary/30 bg-primary/10 px-2 text-[8px] font-semibold text-primary-bright disabled:opacity-35"><Sparkles className="size-3" />{briefNeedsCheckpoint ? 'Checkpoint Brief & generate intent' : 'Generate governed intent'}</button>}{linkedProposal && <IntentCard proposal={linkedProposal} command={linkedCommand} busy={busy} canEdit={can('edit')} onDecide={decideProposal} onExecute={executeCommand} onRejectCommand={rejectCommand} />}</article>
            })}
            {commands.filter((command) => !proposals.some((proposal) => proposal.id === command.proposalId && messages.some((item) => item.proposalId === proposal.id))).map((command) => <CommandCard key={command.id} command={command} busy={busy} canEdit={can('edit')} onExecute={executeCommand} onReject={rejectCommand} />)}
          </div>

          {selected?.status === 'active' && <footer className="shrink-0 border-t border-border p-3"><div className="mb-2 flex items-center gap-1.5"><label className="text-[8px] text-faint-foreground">Model</label><input value={model} onChange={(event) => setModel(event.target.value)} className="h-7 w-28 rounded border border-border bg-background px-2 text-[8px] text-foreground" /><span className="ml-auto text-[8px] text-faint-foreground">AI cannot execute directly</span></div><div className="flex items-end gap-1.5"><textarea value={draft} onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); void sendMessage() } }} rows={3} placeholder="Describe requirements or a controlled next action…" className="min-h-20 min-w-0 flex-1 resize-none rounded border border-border bg-background p-2 text-[10px] text-foreground" /><button type="button" onClick={() => void sendMessage()} disabled={busy || !can('comment') || !draft.trim()} className="flex size-9 items-center justify-center rounded bg-primary text-primary-foreground disabled:opacity-35" aria-label="Send immutable message">{busy ? <LoaderCircle className="size-4 animate-spin" /> : <Send className="size-4" />}</button></div></footer>}
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
  onInspect,
  onDecide,
}: {
  checkpoint: ConversationSummaryCheckpointDto
  previous?: ConversationSummaryCheckpointDto
  source?: readonly ConversationMessageDto[]
  busy: boolean
  canReview: boolean
  currentUserId: string
  onInspect: (checkpoint: ConversationSummaryCheckpointDto) => Promise<void>
  onDecide: (checkpoint: ConversationSummaryCheckpointDto, decision: 'approve' | 'reject') => Promise<void>
}) {
  const authorCannotReview = checkpoint.createdBy === currentUserId
  const reviewReady = checkpoint.status === 'pending_review'
    && canReview
    && !authorCannotReview
    && source !== undefined
  return <section className="rounded-lg border border-border bg-background p-2.5" aria-label={`Summary checkpoint ${checkpoint.throughSequence}`}><div className="flex items-center gap-1.5"><Check className={cn('size-3', checkpoint.status === 'approved' ? 'text-success' : 'text-faint-foreground')} /><span className="text-[9px] font-semibold text-foreground">Summary checkpoint · through #{checkpoint.throughSequence}</span><span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 text-[7px] font-semibold uppercase text-faint-foreground">{checkpoint.status.replace('_', ' ')}</span></div><p className="mt-2 whitespace-pre-wrap text-[9px] leading-relaxed text-muted-foreground">{checkpoint.summary}</p>{previous && <details className="mt-2 rounded border border-border/70"><summary className="cursor-pointer px-2 py-1 text-[7px] text-faint-foreground">Previous approved summary · #{previous.throughSequence}</summary><p className="border-t border-border/70 p-2 text-[8px] leading-relaxed text-muted-foreground">{previous.summary}</p></details>}<div className="mt-2 space-y-0.5 font-mono text-[7px] text-faint-foreground"><p>prefix {checkpoint.prefixHash}</p><p>summary {checkpoint.summaryHash}</p><p>{checkpoint.messageCount} immutable messages · {checkpoint.contentBytes.toLocaleString()} source bytes</p></div>{source === undefined ? <button type="button" onClick={() => void onInspect(checkpoint)} disabled={busy} className="mt-2 inline-flex h-7 w-full items-center justify-center gap-1 rounded border border-border text-[8px] font-semibold text-muted-foreground disabled:opacity-35"><MessageSquare className="size-3" />Inspect exact bound source delta</button> : <details open={checkpoint.status === 'pending_review'} className="mt-2 rounded border border-border/70"><summary className="cursor-pointer px-2 py-1 text-[7px] text-faint-foreground">Exact immutable source delta · {source.length} messages</summary><div className="max-h-48 space-y-1 overflow-y-auto border-t border-border/70 p-2 scrollbar-thin">{source.map((item) => <div key={item.id} className="rounded bg-black/15 p-1.5"><div className="font-mono text-[7px] text-faint-foreground">#{item.sequence} · {item.role}</div><p className="mt-1 whitespace-pre-wrap text-[8px] leading-relaxed text-muted-foreground">{item.content}</p></div>)}</div></details>}{checkpoint.status === 'pending_review' && <>{authorCannotReview && <p className="mt-2 text-[8px] text-warning">Authors cannot approve or reject their own checkpoint.</p>}{!source && <p className="mt-2 text-[8px] text-faint-foreground">Inspect the exact bound source before recording a review decision.</p>}<div className="mt-2 grid grid-cols-2 gap-1"><button type="button" onClick={() => void onDecide(checkpoint, 'approve')} disabled={busy || !reviewReady} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[8px] font-semibold text-success disabled:opacity-35" aria-label={`Approve summary checkpoint ${checkpoint.throughSequence}`}><Check className="size-3" />Approve</button><button type="button" onClick={() => void onDecide(checkpoint, 'reject')} disabled={busy || !reviewReady} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-destructive/10 text-[8px] font-semibold text-destructive disabled:opacity-35" aria-label={`Reject summary checkpoint ${checkpoint.throughSequence}`}><X className="size-3" />Reject</button></div></>}</section>
}

function IntentCard({ proposal, command, busy, canEdit, onDecide, onExecute, onRejectCommand }: { proposal: WorkflowIntentProposalDto; command?: ConversationCommandDto; busy: boolean; canEdit: boolean; onDecide: (proposal: WorkflowIntentProposalDto, decision: 'accept' | 'reject') => Promise<void>; onExecute: (command: ConversationCommandDto) => Promise<void>; onRejectCommand: (command: ConversationCommandDto) => Promise<void> }) {
  const slice = proposal.kind === 'workbench_instruction' ? proposal.workbenchInstruction : undefined
  return <div className="mt-2 rounded border border-primary/25 bg-primary/5 p-2"><div className="flex items-center gap-1.5"><Workflow className="size-3 text-primary-bright" /><span className="text-[9px] font-semibold text-foreground">{proposal.kind === 'start_workflow' ? 'Start workflow' : 'Workbench instruction'}</span><span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 text-[7px] font-semibold uppercase text-faint-foreground">{proposal.status}</span></div>{slice?.sliceTitle && <div className="mt-1 rounded border border-primary/20 bg-background px-2 py-1"><span className="text-[8px] font-semibold text-foreground">Page: {slice.sliceTitle}</span>{slice.sliceKey && <span className="ml-1.5 font-mono text-[7px] text-faint-foreground">{slice.sliceKey}</span>}</div>}<p className="mt-1 text-[8px] leading-relaxed text-muted-foreground">{proposal.workbenchInstruction.objective || `Use published definition ${proposal.suggestedDefinitionVersionId}`}</p><div className="mt-1 truncate font-mono text-[7px] text-faint-foreground">definition {proposal.suggestedDefinitionVersionId} · manifest {proposal.manifestIntent.inputManifest.id}</div><details className="mt-1 rounded border border-border/70 bg-background"><summary className="cursor-pointer px-2 py-1 text-[7px] text-faint-foreground">Inspect frozen intent payload and conversation provenance</summary><pre className="max-h-40 overflow-auto whitespace-pre-wrap border-t border-border/70 p-2 font-mono text-[7px] leading-relaxed text-faint-foreground scrollbar-thin">{JSON.stringify({ scope: proposal.scope, sourceRefs: proposal.sourceRefs, manifestIntent: proposal.manifestIntent, workbenchInstruction: proposal.workbenchInstruction, conversationContext: proposal.conversationContext }, null, 2)}</pre></details>{proposal.status === 'pending' && <div className="mt-2 grid grid-cols-2 gap-1"><button type="button" onClick={() => void onDecide(proposal, 'accept')} disabled={busy || !canEdit} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[8px] font-semibold text-success disabled:opacity-35"><Check className="size-3" />Accept</button><button type="button" onClick={() => void onDecide(proposal, 'reject')} disabled={busy || !canEdit} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-destructive/10 text-[8px] font-semibold text-destructive disabled:opacity-35"><X className="size-3" />Reject</button></div>}{command && <CommandCard command={command} busy={busy} canEdit={canEdit} onExecute={onExecute} onReject={onRejectCommand} />}</div>
}

function CommandCard({ command, busy, canEdit, onExecute, onReject }: { command: ConversationCommandDto; busy: boolean; canEdit: boolean; onExecute: (command: ConversationCommandDto) => Promise<void>; onReject: (command: ConversationCommandDto) => Promise<void> }) {
  return <div className="mt-2 rounded border border-border bg-background p-2"><div className="flex items-center gap-1"><span className="text-[8px] font-semibold text-foreground">Controlled command</span><code className="ml-auto text-[7px] text-faint-foreground">{command.status}</code></div>{command.failure && <p className="mt-1 text-[8px] text-destructive">{command.failure.code}: {command.failure.message}</p>}{command.result && <pre className="mt-1 whitespace-pre-wrap rounded bg-black/20 p-1.5 font-mono text-[7px] text-faint-foreground">{JSON.stringify(command.result, null, 2)}</pre>}{command.status === 'pending' && <div className="mt-2 grid grid-cols-[1fr_auto] gap-1"><button type="button" onClick={() => void onExecute(command)} disabled={busy || !canEdit} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-primary text-[8px] font-semibold text-primary-foreground disabled:opacity-35"><Send className="size-3" />{command.kind === 'start_workflow' ? 'Execute and open run' : 'Generate Workbench proposal'}</button><button type="button" onClick={() => void onReject(command)} disabled={busy || !canEdit} className="flex size-7 items-center justify-center rounded border border-destructive/30 text-destructive disabled:opacity-35" aria-label="Reject command"><X className="size-3" /></button></div>}</div>
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
) {
  if (
    response.id !== command.id
    || response.projectId !== command.projectId
    || response.conversationId !== command.conversationId
    || response.proposalId !== command.proposalId
    || response.kind !== command.kind
  ) throw new Error('The command execution response does not match the exact accepted command.')
}

function assertRunMatchesCommand(
  run: WorkflowRunDto | null,
  command: ConversationCommandDto,
): asserts run is WorkflowRunDto {
  if (!run || run.id !== (command.kind === 'start_workflow' ? command.id : command.payload.workbench.expectedRunId)) {
    throw new Error('The expected workflow run could not be loaded exactly.')
  }
  if (run.definitionVersionId !== command.payload.definitionVersionId) {
    throw new Error('The workflow run does not use the definition version pinned by the accepted command.')
  }
  if (command.kind === 'start_workflow') {
    const expectedManifest = command.payload.manifestIntent.inputManifest
    if (
      run.inputManifest?.id !== expectedManifest.id
      || run.inputManifest?.hash !== expectedManifest.hash
    ) {
      throw new Error('The workflow run does not use the input manifest pinned by the accepted command.')
    }
  }
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
