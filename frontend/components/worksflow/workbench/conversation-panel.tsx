'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'
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
  WorkflowIntentProposalDto,
} from '@/lib/platform/conversation-contract'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import type { WorkflowRunDto } from '@/lib/platform/flow-contract'
import { projectBriefIntentCandidateVersionIds } from '@/lib/platform/workflow-entry'
import {
  workbenchRootBundleId,
  workflowWorkbenchQueueGroups,
} from '@/lib/platform/flow-queue'
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
  const [draft, setDraft] = useState('')
  const [newTitle, setNewTitle] = useState('Application planning')
  const [showCreate, setShowCreate] = useState(false)
  const [model, setModel] = useState('gpt-5')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [checkpointNotice, setCheckpointNotice] = useState<string | null>(null)
  const selected = conversations.find((item) => item.id === conversationId)
  const projectBrief = artifacts.documents.find((item) => item.artifact.kind === 'project_brief')
  const briefRevision = projectBrief?.latestRevision ?? projectBrief?.approvedRevision
  const briefNeedsCheckpoint = Boolean(
    projectBrief?.draft
    && (!briefRevision || projectBrief.draft.contentHash !== briefRevision.contentHash),
  )
  const client = platformClient.conversation

  const loadConversation = useCallback(async (id: string) => {
    if (!project) return
    const [messageResult, proposalResult, commandResult] = await Promise.all([
      client.listMessages(project.id, id, { limit: 200 }),
      client.listIntentProposals(project.id, id, { limit: 200 }),
      client.listCommands(project.id, id, { limit: 200 }),
    ])
    setMessages([...messageResult.data.items].sort((left, right) => left.sequence - right.sequence))
    setProposals([...proposalResult.data.items].sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
    setCommands([...commandResult.data.items].sort((left, right) => left.createdAt.localeCompare(right.createdAt)))
  }, [client, project])

  const refresh = useCallback(async () => {
    if (!project || !session.signedIn) {
      setConversations([])
      setConversationId('')
      return
    }
    setError(null)
    try {
      const result = await client.list(project.id, { limit: 200 })
      const items = [...result.data.items].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt))
      setConversations(items)
      const queryId = conversationReference()
      const next = items.find((item) => item.id === (conversationId || queryId))
        ?? items.find((item) => item.status === 'active')
        ?? items[0]
      if (next) {
        setConversationId(next.id)
        setConversationReference(next.id)
        await loadConversation(next.id)
      } else {
        setConversationId('')
        setMessages([])
        setProposals([])
        setCommands([])
      }
    } catch (cause) {
      setError(message(cause, 'Unable to load project conversations.'))
    }
  }, [client, conversationId, loadConversation, project, session.signedIn])

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
    setBusy(true)
    setError(null)
    try {
      const result = await client.create(project.id, newTitle.trim())
      setConversations((current) => [result.data, ...current])
      setConversationId(result.data.id)
      setConversationReference(result.data.id)
      setMessages([])
      setProposals([])
      setCommands([])
      setShowCreate(false)
    } catch (cause) {
      setError(message(cause, 'Unable to create the conversation.'))
    } finally {
      setBusy(false)
    }
  }

  async function selectConversation(id: string) {
    if (!id) return
    setConversationId(id)
    setConversationReference(id)
    setShowCreate(false)
    setBusy(true)
    setError(null)
    try {
      await loadConversation(id)
    } catch (cause) {
      setError(message(cause, 'Unable to load the selected conversation.'))
    } finally {
      setBusy(false)
    }
  }

  async function sendMessage() {
    if (!project || !conversationId || !draft.trim() || !can('comment')) return
    setBusy(true)
    setError(null)
    try {
      const result = await client.addMessage(project.id, conversationId, draft.trim())
      setMessages((current) => [...current, result.data].sort((left, right) => left.sequence - right.sequence))
      setDraft('')
    } catch (cause) {
      setError(message(cause, 'Unable to persist the conversation message.'))
    } finally {
      setBusy(false)
    }
  }

  async function generateIntent(triggerMessageId: string) {
    if (!project || !conversationId || !can('edit')) return
    if (!projectBrief) {
      setError('Create the Project Brief before asking AI to propose a workflow action.')
      return
    }
    setBusy(true)
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
          conversationId,
          triggerMessageId,
          projectBriefArtifactId: sourceRef.artifactId,
          projectBriefRevisionId: sourceRef.revisionId,
          projectBriefContentHash: sourceRef.contentHash,
        },
        outputSchemaVersion: 'workflow-intent-input/v1',
      })
      const candidateDefinitionVersionIds = projectBriefIntentCandidateVersionIds([
        ...flow.definitionVersions,
        ...flow.definitions,
      ])
      if (candidateDefinitionVersionIds.length === 0) {
        throw new Error('Publish at least one workflow definition version before generating an intent proposal.')
      }
      await client.generateIntentProposal(project.id, conversationId, {
        triggerMessageId,
        candidateDefinitionVersionIds,
        sourceRefs: [sourceRef],
        manifestIntent: {
          mode: 'use_existing',
          inputManifest: { id: manifest.data.id, hash: manifest.data.hash },
          purpose: 'start_or_continue_application_workflow',
        },
        model: model.trim() || undefined,
      })
      await loadConversation(conversationId)
    } catch (cause) {
      setError(message(cause, 'AI could not produce a governed workflow intent proposal.'))
    } finally {
      setBusy(false)
    }
  }

  async function decideProposal(proposal: WorkflowIntentProposalDto, decision: 'accept' | 'reject') {
    if (!project || !can('edit')) return
    setBusy(true)
    setError(null)
    try {
      await client.decideIntentProposal(
        project.id,
        proposal.conversationId,
        proposal,
        decision,
        decision === 'reject' ? 'Rejected during intent review.' : '',
      )
      await loadConversation(proposal.conversationId)
    } catch (cause) {
      setError(message(cause, 'Unable to record the intent decision.'))
    } finally {
      setBusy(false)
    }
  }

  async function executeCommand(command: ConversationCommandDto) {
    if (!project || !can('edit') || command.status !== 'pending') return
    setBusy(true)
    setError(null)
    try {
      if (command.kind === 'start_workflow') {
        const result = await client.executeCommand(project.id, command.conversationId, command)
        if (result.data.status !== 'executed') {
          throw new Error('The server did not confirm execution of the accepted workflow command.')
        }
        const runId = stringField(result.data.result, 'runId')
        if (!runId || runId !== command.id) {
          throw new Error('The workflow runtime returned an unexpected run identity.')
        }
        const loadedRun = await flow.loadRun(runId)
        assertRunMatchesCommand(loadedRun, command)
      } else {
        const runId = command.payload.workbench.expectedRunId
        if (!runId) {
          throw new Error('The accepted Workbench instruction does not pin an expected workflow run.')
        }
        const targetRun = await flow.loadRun(runId)
        assertRunMatchesCommand(targetRun, command)

        const bundleId = command.payload.workbench.expectedBundleId ?? flow.bundle?.id
        if (!bundleId) {
          throw new Error('Load the frozen Workbench bundle linked to the expected workflow run before executing this instruction.')
        }
        const workbenchGroups = workflowWorkbenchQueueGroups(targetRun)
        const targetGroup = workbenchGroups.find((group) =>
          group.references.some((reference) => reference.bundleId === bundleId),
        )
        if (workbenchGroups.length > 0 && !targetGroup) {
          throw new Error('The expected frozen Workbench bundle is not owned by any Workbench node in this run.')
        }
        if (targetGroup) {
          await flow.selectWorkbenchGroup(targetGroup.nodeKey)
        }
        const targetBundle = await flow.loadBundle(bundleId)
        if (
          !targetBundle
          || (targetBundle.id !== bundleId && workbenchRootBundleId(targetBundle) !== bundleId)
        ) {
          throw new Error('The expected frozen Workbench bundle is unavailable.')
        }
        if (targetBundle.workflowRunId !== runId) {
          throw new Error('The frozen Workbench bundle is not linked to the expected workflow run.')
        }
        const constraints = command.payload.workbench.constraints ?? []
        const instruction = [command.payload.workbench.objective, ...constraints.map((item) => `Constraint: ${item}`)].join('\n')
        const proposal = await flow.generateImplementation(instruction, model.trim() || undefined, bundleId)
        if (!proposal) throw new Error('Workbench did not return a reviewable implementation proposal.')
        await client.executeCommand(project.id, command.conversationId, command, {
          workbenchResult: {
            runId,
            bundleId: proposal.buildManifestId,
            implementationProposalId: proposal.id,
          },
        })
      }
      await loadConversation(command.conversationId)
    } catch (cause) {
      setError(message(cause, 'Unable to execute the accepted conversation command.'))
    } finally {
      setBusy(false)
    }
  }

  async function rejectCommand(command: ConversationCommandDto) {
    if (!project || !can('edit') || command.status !== 'pending') return
    setBusy(true)
    setError(null)
    try {
      await client.rejectCommand(
        project.id,
        command.conversationId,
        command,
        'Rejected before controlled execution.',
      )
      await loadConversation(command.conversationId)
    } catch (cause) {
      setError(message(cause, 'Unable to reject the pending conversation command.'))
    } finally {
      setBusy(false)
    }
  }

  async function archiveConversation() {
    if (!project || !selected || !can('edit')) return
    setBusy(true)
    try {
      await client.update(project.id, selected, { status: 'archived' })
      await refresh()
    } catch (cause) {
      setError(message(cause, 'Unable to archive the conversation.'))
    } finally {
      setBusy(false)
    }
  }

  const proposalById = useMemo(() => new Map(proposals.map((item) => [item.id, item])), [proposals])
  const commandByProposal = useMemo(() => new Map(commands.map((item) => [item.proposalId, item])), [commands])

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

function IntentCard({ proposal, command, busy, canEdit, onDecide, onExecute, onRejectCommand }: { proposal: WorkflowIntentProposalDto; command?: ConversationCommandDto; busy: boolean; canEdit: boolean; onDecide: (proposal: WorkflowIntentProposalDto, decision: 'accept' | 'reject') => Promise<void>; onExecute: (command: ConversationCommandDto) => Promise<void>; onRejectCommand: (command: ConversationCommandDto) => Promise<void> }) {
  return <div className="mt-2 rounded border border-primary/25 bg-primary/5 p-2"><div className="flex items-center gap-1.5"><Workflow className="size-3 text-primary-bright" /><span className="text-[9px] font-semibold text-foreground">{proposal.kind === 'start_workflow' ? 'Start workflow' : 'Workbench instruction'}</span><span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 text-[7px] font-semibold uppercase text-faint-foreground">{proposal.status}</span></div><p className="mt-1 text-[8px] leading-relaxed text-muted-foreground">{proposal.workbenchInstruction.objective || `Use published definition ${proposal.suggestedDefinitionVersionId}`}</p><div className="mt-1 truncate font-mono text-[7px] text-faint-foreground">definition {proposal.suggestedDefinitionVersionId} · manifest {proposal.manifestIntent.inputManifest.id}</div><details className="mt-1 rounded border border-border/70 bg-background"><summary className="cursor-pointer px-2 py-1 text-[7px] text-faint-foreground">Inspect frozen intent payload</summary><pre className="max-h-40 overflow-auto whitespace-pre-wrap border-t border-border/70 p-2 font-mono text-[7px] leading-relaxed text-faint-foreground scrollbar-thin">{JSON.stringify({ scope: proposal.scope, sourceRefs: proposal.sourceRefs, manifestIntent: proposal.manifestIntent, workbenchInstruction: proposal.workbenchInstruction }, null, 2)}</pre></details>{proposal.status === 'pending' && <div className="mt-2 grid grid-cols-2 gap-1"><button type="button" onClick={() => void onDecide(proposal, 'accept')} disabled={busy || !canEdit} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[8px] font-semibold text-success disabled:opacity-35"><Check className="size-3" />Accept</button><button type="button" onClick={() => void onDecide(proposal, 'reject')} disabled={busy || !canEdit} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-destructive/10 text-[8px] font-semibold text-destructive disabled:opacity-35"><X className="size-3" />Reject</button></div>}{command && <CommandCard command={command} busy={busy} canEdit={canEdit} onExecute={onExecute} onReject={onRejectCommand} />}</div>
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
