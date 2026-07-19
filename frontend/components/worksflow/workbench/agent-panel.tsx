'use client'

import dynamic from 'next/dynamic'
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  AlertTriangle,
  Bot,
  CheckCircle2,
  CircleStop,
  Download,
  FileDiff,
  LoaderCircle,
  RefreshCw,
  RotateCcw,
  Send,
  Undo2,
  X,
} from 'lucide-react'
import {
  agentAttemptEtag,
  agentMergeEtag,
  type AgentClient,
  type AgentCreateAttemptInput,
  type AgentPatchFileResult,
} from '@/lib/platform/agent-client'
import type {
  AgentAttemptDto,
  AgentAttemptEventDto,
  AgentFileOperationDto,
  AgentPatchMergeResultDto,
  AgentPatchUndoResultDto,
  AgentPatchValidationDto,
  AgentPlatformPatchDto,
  AgentStructuredResultDto,
  AgentTaskAttemptResultDto,
} from '@/lib/platform/agent-contract'
import { normalizeAgentAttemptEvent } from '@/lib/platform/agent-contract'
import type { SandboxClient } from '@/lib/platform/sandbox-client'
import { PlatformHttpError, type HttpResult } from '@/lib/platform/http'
import type { WsConnectionState } from '@/lib/platform/websocket'
import { cn } from '@/lib/utils'

const MonacoDiffEditor = dynamic(
  () => import('@monaco-editor/react').then((module) => module.DiffEditor),
  { ssr: false },
)

type AgentReviewTab = 'changes' | 'result' | 'logs' | 'events'

interface MergeReceipt {
  readonly attemptId: string
  readonly mergeId: string
  readonly mergeEtag: string
  readonly planContentHash: string
  readonly beforeTreeHash: string
  readonly afterTreeHash: string
  readonly appliedAt: string
  readonly undoneAt?: string
}

interface PatchFileView extends AgentPatchFileResult {
  readonly binary: boolean
  readonly text?: string
}

interface PatchOperationReview {
  readonly loading: boolean
  readonly base?: PatchFileView
  readonly proposed?: PatchFileView
  readonly error?: string
  readonly acknowledged: boolean
}

export interface AgentPanelProps {
  readonly client: AgentClient
  readonly sandbox: SandboxClient
  readonly sessionId: string
  readonly canEdit: boolean
  readonly workspaceBusy: boolean
  readonly onCreate: (
    input: AgentCreateAttemptInput,
  ) => Promise<HttpResult<AgentTaskAttemptResultDto>>
  readonly onMerge: (
    attemptId: string,
    attemptEtag: string,
  ) => Promise<HttpResult<AgentPatchMergeResultDto>>
  readonly onUndo: (
    mergeId: string,
    mergeEtag: string,
  ) => Promise<HttpResult<AgentPatchUndoResultDto>>
  readonly onClose: () => void
}

const activeStates = new Set<AgentAttemptDto['state']>([
  'pending', 'ready', 'queued', 'claimed', 'running', 'patch_ready', 'validating',
])

const retryableStates = new Set<AgentAttemptDto['state']>([
  'verification_failed', 'failed', 'timed_out', 'cancelled',
])

function errorMessage(cause: unknown) {
  if (cause instanceof PlatformHttpError) return cause.problem.detail || cause.problem.title
  return cause instanceof Error ? cause.message : 'The Agent operation failed.'
}

function shortHash(value: string) {
  if (!value) return '—'
  return value.startsWith('sha256:') ? value.slice(7, 19) : value.slice(0, 12)
}

function displayTime(value?: string) {
  if (!value) return '—'
  const timestamp = Date.parse(value)
  return Number.isNaN(timestamp) ? value : new Date(timestamp).toLocaleString()
}

function stateTone(state: AgentAttemptDto['state']) {
  if (state === 'review_ready') return 'border-success/30 bg-success/10 text-success'
  if (state === 'verification_failed' || state === 'failed' || state === 'timed_out') {
    return 'border-destructive/30 bg-destructive/10 text-destructive'
  }
  if (state === 'cancelled' || state === 'stale') {
    return 'border-border bg-muted text-muted-foreground'
  }
  return 'border-primary/30 bg-primary/10 text-primary-bright'
}

function patchOperationKey(operation: AgentFileOperationDto) {
  return operation.id || `${operation.kind}:${operation.path}`
}

function patchLanguage(path: string) {
  const extension = path.split('.').pop()?.toLowerCase()
  if (extension === 'ts' || extension === 'tsx') return 'typescript'
  if (extension === 'js' || extension === 'jsx' || extension === 'mjs' || extension === 'cjs') return 'javascript'
  if (extension === 'json') return 'json'
  if (extension === 'css') return 'css'
  if (extension === 'html') return 'html'
  if (extension === 'md' || extension === 'mdx') return 'markdown'
  if (extension === 'py') return 'python'
  if (extension === 'go') return 'go'
  if (extension === 'sql') return 'sql'
  if (extension === 'yaml' || extension === 'yml') return 'yaml'
  if (extension === 'sh' || extension === 'bash') return 'shell'
  return 'plaintext'
}

function patchFileView(file: AgentPatchFileResult): PatchFileView {
  if (!file.exists) return { ...file, binary: false, text: '' }
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(file.value)
    for (let index = 0; index < text.length; index += 1) {
      const code = text.charCodeAt(index)
      if (code === 0 || (code < 32 && code !== 9 && code !== 10 && code !== 13)) {
        return { ...file, binary: true }
      }
    }
    return { ...file, binary: false, text }
  } catch {
    return { ...file, binary: true }
  }
}

function downloadPatchFile(file: PatchFileView, path: string, side: 'base' | 'proposed') {
  if (!file.exists) return
  const url = URL.createObjectURL(new Blob([file.value], { type: 'application/octet-stream' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `${path.split('/').pop() || 'file'}.${side}`
  anchor.click()
  URL.revokeObjectURL(url)
}

export function AgentPanel({
  client,
  sandbox,
  sessionId,
  canEdit,
  workspaceBusy,
  onCreate,
  onMerge,
  onUndo,
  onClose,
}: AgentPanelProps) {
  const [taskKey, setTaskKey] = useState('implement-approved-scope')
  const [instruction, setInstruction] = useState('')
  const [executorProfile, setExecutorProfile] = useState('codex-default')
  const [reason, setReason] = useState('')
  const [attempts, setAttempts] = useState<readonly AgentAttemptDto[]>([])
  const [selectedId, setSelectedId] = useState('')
  const [selected, setSelected] = useState<AgentTaskAttemptResultDto | null>(null)
  const [attemptEtag, setAttemptEtag] = useState('')
  const [events, setEvents] = useState<readonly AgentAttemptEventDto[]>([])
  const [streamState, setStreamState] = useState<WsConnectionState>('idle')
  const [recoveryNotice, setRecoveryNotice] = useState<string | null>(null)
  const [patch, setPatch] = useState<AgentPlatformPatchDto | null>(null)
  const [structuredResult, setStructuredResult] = useState<AgentStructuredResultDto | null>(null)
  const [validation, setValidation] = useState<AgentPatchValidationDto | null>(null)
  const [stdout, setStdout] = useState('')
  const [stderr, setStderr] = useState('')
  const [tab, setTab] = useState<AgentReviewTab>('changes')
  const [loading, setLoading] = useState(true)
  const [actionBusy, setActionBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [refreshVersion, setRefreshVersion] = useState(0)
  const [mergeOutcome, setMergeOutcome] = useState<AgentPatchMergeResultDto | null>(null)
  const [undoOutcome, setUndoOutcome] = useState<AgentPatchUndoResultDto | null>(null)
  const [mergeReceipt, setMergeReceipt] = useState<MergeReceipt | null>(null)
  const [patchReviews, setPatchReviews] = useState<Readonly<Record<string, PatchOperationReview>>>({})
  const [selectedPatchOperation, setSelectedPatchOperation] = useState('')
  const evidenceSignatureRef = useRef('')
  const patchReviewIdentityRef = useRef('')
  const patchReviewControllersRef = useRef(new Set<AbortController>())
  const actionBusyRef = useRef(false)
  const eventCursorRef = useRef({ attemptId: '', sequence: 0 })

  const refreshAttempts = useCallback(async (signal?: AbortSignal) => {
    const result = await client.listAttempts(sessionId, 50, { signal })
    setAttempts(result.data)
    setSelectedId((current) => {
      if (current && result.data.some((attempt) => attempt.id === current)) return current
      return result.data[0]?.id ?? ''
    })
  }, [client, sessionId])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError(null)
    void refreshAttempts(controller.signal)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [refreshAttempts, refreshVersion])

  useEffect(() => {
    setMergeReceipt(null)
    setMergeOutcome(null)
    setUndoOutcome(null)
    setSelected(null)
    setAttemptEtag('')
    setEvents([])
    eventCursorRef.current = { attemptId: selectedId, sequence: 0 }
    setRecoveryNotice(null)
    setPatch(null)
    setStructuredResult(null)
    setValidation(null)
    setStdout('')
    setStderr('')
    evidenceSignatureRef.current = ''
  }, [selectedId, sessionId])

  useEffect(() => {
    const identity = selected?.attempt.id && patch?.contentHash
      ? `${selected.attempt.id}:${patch.contentHash}`
      : ''
    patchReviewIdentityRef.current = identity
    for (const controller of patchReviewControllersRef.current) controller.abort()
    patchReviewControllersRef.current.clear()
    setPatchReviews({})
    setSelectedPatchOperation('')
  }, [patch?.contentHash, selected?.attempt.id])

  useEffect(() => {
    if (!selectedId) {
      setSelected(null)
      setAttemptEtag('')
      setEvents([])
      setPatch(null)
      setStructuredResult(null)
      setValidation(null)
      setStdout('')
      setStderr('')
      return
    }
    const controller = new AbortController()
    const stream = sandbox.stream(sessionId, ['agent'])
    let timer: number | undefined
    let stopped = false
    let connectionState: WsConnectionState = 'connecting'
    let refreshInFlight = false
    let refreshPending = false
    let snapshotResetPending = true
    let fallbackPollCount = 0
    let attemptActive = false

    const clearPoll = () => {
      if (timer !== undefined) window.clearTimeout(timer)
      timer = undefined
    }

    const scheduleFallbackPoll = () => {
      clearPoll()
      if (stopped || connectionState === 'open') return
      if (fallbackPollCount >= 12) {
        setError('Agent live recovery exhausted its bounded HTTP fallback window. Reconnect or refresh the exact Attempt.')
        return
      }
      fallbackPollCount += 1
      timer = window.setTimeout(() => void refresh(false), 1500)
    }

    const loadEvidence = async (attempt: AgentAttemptDto) => {
      const signature = [
        attempt.id,
        attempt.evidence.patch?.contentHash ?? '',
        attempt.evidence.structuredResult?.contentHash ?? '',
        attempt.evidence.validation?.contentHash ?? '',
        attempt.evidence.stdout?.contentHash ?? '',
        attempt.evidence.stderr?.contentHash ?? '',
      ].join(':')
      if (evidenceSignatureRef.current === signature) return
      const [nextPatch, nextResult, nextValidation, nextStdout, nextStderr] = await Promise.all([
        attempt.evidence.patch
          ? client.readPatch(attempt.id, { signal: controller.signal }).then((result) => result.data)
          : Promise.resolve(null),
        attempt.evidence.structuredResult
          ? client.readStructuredResult(attempt.id, { signal: controller.signal }).then((result) => result.data)
          : Promise.resolve(null),
        attempt.evidence.validation
          ? client.readValidation(attempt.id, { signal: controller.signal }).then((result) => result.data)
          : Promise.resolve(null),
        attempt.evidence.stdout
          ? client.readStdout(attempt.id, { signal: controller.signal }).then((result) => result.data)
          : Promise.resolve(''),
        attempt.evidence.stderr
          ? client.readStderr(attempt.id, { signal: controller.signal }).then((result) => result.data)
          : Promise.resolve(''),
      ])
      if (stopped) return
      evidenceSignatureRef.current = signature
      setPatch(nextPatch)
      setStructuredResult(nextResult)
      setValidation(nextValidation)
      setStdout(nextStdout)
      setStderr(nextStderr)
    }

    const refresh = async (resetSnapshot: boolean) => {
      if (refreshInFlight) {
        refreshPending = true
        snapshotResetPending = snapshotResetPending || resetSnapshot
        return
      }
      refreshInFlight = true
      const reset = snapshotResetPending || resetSnapshot || eventCursorRef.current.attemptId !== selectedId
      snapshotResetPending = false
      const afterSequence = reset ? 0 : eventCursorRef.current.sequence
      try {
        const [attemptResult, eventResult, historyResult] = await Promise.all([
          client.getAttempt(selectedId, { signal: controller.signal }),
          client.recoverEvents(selectedId, afterSequence, {
            signal: controller.signal, limit: 200, maxPages: 4,
          }),
          client.listMerges(selectedId, 50, { signal: controller.signal }),
        ])
        if (stopped) return
        const attempt = attemptResult.data.attempt
        attemptActive = activeStates.has(attempt.state)
        if (attempt.version !== eventResult.lastSequence + 1) {
          throw new Error('The AgentAttempt snapshot and immutable event cursor do not identify the same version.')
        }
        setSelected(attemptResult.data)
        setAttemptEtag(attemptResult.etag ?? agentAttemptEtag(attempt))
        eventCursorRef.current = { attemptId: selectedId, sequence: eventResult.lastSequence }
        setEvents((current) => reset ? eventResult.events : [...current, ...eventResult.events])
        const latestApplied = historyResult.data.find((item) => item.application)
        setMergeReceipt(latestApplied?.application ? {
          attemptId: attempt.id,
          mergeId: latestApplied.plan.id,
          mergeEtag: agentMergeEtag({ plan: latestApplied.plan }),
          planContentHash: latestApplied.plan.contentHash,
          beforeTreeHash: latestApplied.application.beforeTreeHash,
          afterTreeHash: latestApplied.application.afterTreeHash,
          appliedAt: latestApplied.application.appliedAt,
          undoneAt: latestApplied.undo?.application?.appliedAt,
        } : null)
        setAttempts((current) => {
          const remaining = current.filter((item) => item.id !== attempt.id)
          return [attempt, ...remaining]
        })
        await loadEvidence(attempt)
        if (!stopped && activeStates.has(attempt.state) && connectionState !== 'open') scheduleFallbackPoll()
      } catch (cause) {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      } finally {
        refreshInFlight = false
        if (refreshPending && !stopped) {
          refreshPending = false
          const pendingReset = snapshotResetPending
          snapshotResetPending = false
          void refresh(pendingReset)
        }
      }
    }

    const disposeState = stream.onState((state) => {
      connectionState = state
      setStreamState(state)
      if (state === 'open') {
        fallbackPollCount = 0
        clearPoll()
      } else if (attemptActive) {
        scheduleFallbackPoll()
      }
    })
    const disposeError = stream.onError((cause) => {
      if (!stopped) setError(cause.message)
    })
    const disposeReset = stream.onReset((event) => {
      if (event.channel !== 'agent') return
      eventCursorRef.current = { attemptId: selectedId, sequence: 0 }
      setEvents([])
      setRecoveryNotice('The retained Agent stream cursor was reset; the immutable Attempt snapshot is being rebuilt from sequence 0.')
      snapshotResetPending = true
      void refresh(true)
    })
    const disposeEvent = stream.onEvent((event) => {
      if (event.channel !== 'agent' || event.eventType === 'stream.reset') return
      try {
        const attemptEvent = normalizeAgentAttemptEvent(event.payload)
        if (event.eventType !== `agent.attempt.${attemptEvent.kind}` ||
          event.aggregateVersion !== attemptEvent.versionTo ||
          event.correlationId !== attemptEvent.attemptId) {
          throw new Error('The Agent stream envelope does not bind its immutable AttemptEvent payload.')
        }
        if (attemptEvent.attemptId === selectedId) void refresh(false)
        else void refreshAttempts(controller.signal)
      } catch (cause) {
        setError(errorMessage(cause))
      }
    })
    stream.connect()
    void refresh(true)
    return () => {
      stopped = true
      controller.abort()
      clearPoll()
      disposeState()
      disposeError()
      disposeReset()
      disposeEvent()
      stream.destroy()
    }
  }, [client, refreshAttempts, refreshVersion, sandbox, selectedId, sessionId])

  const runAction = useCallback(async (operation: () => Promise<void>) => {
    if (actionBusyRef.current) return
    actionBusyRef.current = true
    setActionBusy(true)
    setError(null)
    try {
      await operation()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      actionBusyRef.current = false
      setActionBusy(false)
    }
  }, [])

  const createAttempt = () => void runAction(async () => {
    const normalizedTaskKey = taskKey.trim()
    const normalizedInstruction = instruction.trim()
    const normalizedProfile = executorProfile.trim()
    if (!normalizedTaskKey || !normalizedInstruction || !normalizedProfile) {
      throw new Error('Task key, instruction, and qualified executor profile are required.')
    }
    const result = await onCreate({
      taskKey: normalizedTaskKey,
      instruction: normalizedInstruction,
      executorProfile: normalizedProfile,
    })
    setSelected(result.data)
    setAttemptEtag(result.etag ?? agentAttemptEtag(result.data.attempt))
    setAttempts((current) => [
      result.data.attempt,
      ...current.filter((attempt) => attempt.id !== result.data.attempt.id),
    ])
    setSelectedId(result.data.attempt.id)
    setInstruction('')
  })

  const cancelAttempt = () => void runAction(async () => {
    if (!selected || !attemptEtag || !reason.trim()) {
      throw new Error('A cancellation reason is required.')
    }
    await client.cancelAttempt(selected.attempt.id, reason.trim(), {
      ifMatch: attemptEtag,
      idempotencyKey: true,
    })
    setReason('')
    setRefreshVersion((value) => value + 1)
  })

  const retryAttempt = () => void runAction(async () => {
    if (!selected || !attemptEtag || !reason.trim()) {
      throw new Error('A retry reason is required.')
    }
    const result = await client.retryAttempt(selected.attempt.id, reason.trim(), {
      ifMatch: attemptEtag,
      idempotencyKey: true,
    })
    setReason('')
    setSelectedId(result.data.attempt.id)
    setAttempts((current) => [result.data.attempt, ...current])
    setRefreshVersion((value) => value + 1)
  })

  const loadPatchOperation = useCallback(async (operation: AgentFileOperationDto) => {
    const attemptId = selected?.attempt.id
    const patchContentHash = patch?.contentHash
    if (!attemptId || !patchContentHash) return
    const key = patchOperationKey(operation)
    const identity = `${attemptId}:${patchContentHash}`
    setSelectedPatchOperation(key)
    const existing = patchReviews[key]
    if (existing?.base && existing.proposed && !existing.error) return

    const controller = new AbortController()
    patchReviewControllersRef.current.add(controller)
    setPatchReviews((current) => ({
      ...current,
      [key]: { ...current[key], loading: true, error: undefined, acknowledged: false },
    }))
    try {
      const [base, proposed] = await Promise.all([
        client.readPatchFile(attemptId, operation.path, 'base', { signal: controller.signal }),
        client.readPatchFile(attemptId, operation.path, 'proposed', { signal: controller.signal }),
      ])
      if (base.data.patchContentHash !== patchContentHash || proposed.data.patchContentHash !== patchContentHash) {
        throw new Error('The loaded file bodies do not belong to the finalized patch under review.')
      }
      if (
        (operation.expectedHash && (!base.data.exists || base.data.contentHash !== operation.expectedHash))
        || (!operation.expectedHash && base.data.exists)
        || (operation.kind === 'file.delete' && proposed.data.exists)
        || (operation.kind === 'file.upsert' && (
          !proposed.data.exists
          || proposed.data.contentHash !== operation.contentHash
          || proposed.data.byteSize !== operation.byteSize
          || proposed.data.mode !== operation.mode
        ))
      ) {
        throw new Error('The loaded file identity does not match the immutable patch operation.')
      }
      if (patchReviewIdentityRef.current !== identity) return
      setPatchReviews((current) => ({
        ...current,
        [key]: {
          loading: false,
          base: patchFileView(base.data),
          proposed: patchFileView(proposed.data),
          acknowledged: false,
        },
      }))
    } catch (cause) {
      if (!controller.signal.aborted && patchReviewIdentityRef.current === identity) {
        setPatchReviews((current) => ({
          ...current,
          [key]: {
            ...current[key],
            loading: false,
            error: errorMessage(cause),
            acknowledged: false,
          },
        }))
      }
    } finally {
      patchReviewControllersRef.current.delete(controller)
    }
  }, [client, patch?.contentHash, patchReviews, selected?.attempt.id])

  const acknowledgePatchOperation = useCallback((operation: AgentFileOperationDto, acknowledged: boolean) => {
    const key = patchOperationKey(operation)
    setPatchReviews((current) => {
      const review = current[key]
      if (!review?.base || !review.proposed || review.error || review.loading) return current
      return { ...current, [key]: { ...review, acknowledged } }
    })
  }, [])

  const mergePatch = () => void runAction(async () => {
    if (!selected || !attemptEtag) throw new Error('Refresh the exact AgentAttempt before merging.')
    if (!patch || !patch.operations.every((operation) => patchReviews[patchOperationKey(operation)]?.acknowledged)) {
      throw new Error('Review and acknowledge every exact file change before merging.')
    }
    const result = await onMerge(selected.attempt.id, attemptEtag)
    setMergeOutcome(result.data)
    setUndoOutcome(null)
    if (result.data.application) {
      const receipt: MergeReceipt = {
        attemptId: selected.attempt.id,
        mergeId: result.data.plan.id,
        mergeEtag: result.etag ?? agentMergeEtag(result.data),
        planContentHash: result.data.plan.contentHash,
        beforeTreeHash: result.data.application.beforeTreeHash,
        afterTreeHash: result.data.application.afterTreeHash,
        appliedAt: result.data.application.appliedAt,
      }
      setMergeReceipt(receipt)
    }
  })

  const undoPatch = () => void runAction(async () => {
    if (!mergeReceipt || mergeReceipt.undoneAt) throw new Error('No applied merge is available to undo.')
    const result = await onUndo(mergeReceipt.mergeId, mergeReceipt.mergeEtag)
    setUndoOutcome(result.data)
    if (result.data.application || result.data.plan.disposition === 'noop') {
      const receipt = {
        ...mergeReceipt,
        undoneAt: result.data.application?.appliedAt ?? result.data.plan.createdAt,
      }
      setMergeReceipt(receipt)
    }
  })

  const current = selected?.attempt
  const canCancel = Boolean(current && activeStates.has(current.state))
  const canRetry = Boolean(current && retryableStates.has(current.state))
  const reviewedOperationCount = patch?.operations.filter(
    (operation) => patchReviews[patchOperationKey(operation)]?.acknowledged,
  ).length ?? 0
  const allPatchOperationsReviewed = Boolean(
    patch && patch.operations.every(
      (operation) => patchReviews[patchOperationKey(operation)]?.acknowledged,
    ),
  )
  const canMerge = Boolean(
    current?.state === 'review_ready'
      && patch
      && validation?.decision === 'reviewable'
      && allPatchOperationsReviewed,
  )

  return (
    <aside className="flex w-[390px] shrink-0 flex-col border-l border-border bg-panel/80">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
        <Bot className="size-3.5 text-primary-bright" />
        <span className="text-[10px] font-semibold">Project Agent</span>
        <span className="text-[8px] text-faint-foreground">exact-input execution</span>
        <button type="button" onClick={() => setRefreshVersion((value) => value + 1)} className="ml-auto rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground" title="Refresh exact Attempt"><RefreshCw className="size-3" /></button>
        <button type="button" onClick={onClose} className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground" aria-label="Close Agent panel"><X className="size-3" /></button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <section className="space-y-2 border-b border-border p-3">
          <div className="text-[9px] font-semibold uppercase tracking-wide text-muted-foreground">New exact task</div>
          <div className="grid grid-cols-2 gap-2">
            <label className="space-y-1 text-[8px] text-faint-foreground">
              Stable task key
              <input value={taskKey} onChange={(event) => setTaskKey(event.target.value)} className="h-7 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" />
            </label>
            <label className="space-y-1 text-[8px] text-faint-foreground">
              Qualified profile
              <input value={executorProfile} onChange={(event) => setExecutorProfile(event.target.value)} className="h-7 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" />
            </label>
          </div>
          <label className="block space-y-1 text-[8px] text-faint-foreground">
            Instruction
            <textarea value={instruction} onChange={(event) => setInstruction(event.target.value)} rows={3} placeholder="Describe the implementation intent. Approved artifacts and exact constraints remain authoritative." className="w-full resize-y rounded border border-border bg-background px-2 py-1.5 text-[9px] leading-4 text-foreground" />
          </label>
          <button type="button" onClick={createAttempt} disabled={!canEdit || workspaceBusy || actionBusy || !taskKey.trim() || !instruction.trim() || !executorProfile.trim()} className="inline-flex h-7 w-full items-center justify-center gap-1.5 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground disabled:opacity-40">
            {actionBusy ? <LoaderCircle className="size-3 animate-spin" /> : <Send className="size-3" />}
            Save Candidate and start Agent
          </button>
          <p className="text-[8px] leading-3 text-faint-foreground">Starting an Agent never approves a workflow node. The server first freezes a TaskCapsule and ContextPack from the exact Candidate and approved upstream revisions.</p>
        </section>

        {error && (
          <div role="alert" className="m-3 flex gap-2 rounded border border-destructive/30 bg-destructive/10 p-2 text-[9px] leading-4 text-destructive">
            <AlertTriangle className="mt-0.5 size-3 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        <section className="border-b border-border p-3">
          <label className="block space-y-1 text-[8px] text-faint-foreground">
            Attempts in this SandboxSession
            <select value={selectedId} onChange={(event) => setSelectedId(event.target.value)} disabled={loading || attempts.length === 0} className="h-8 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground disabled:opacity-50">
              {attempts.length === 0 && <option value="">No AgentAttempts yet</option>}
              {attempts.map((attempt) => <option key={attempt.id} value={attempt.id}>{attempt.state} · {attempt.id.slice(0, 8)}</option>)}
            </select>
          </label>
        </section>

        {loading && !current ? (
          <div className="flex items-center justify-center gap-2 p-8 text-[9px] text-muted-foreground"><LoaderCircle className="size-3 animate-spin" /> Loading exact Attempt…</div>
        ) : current ? (
          <>
            <section className="space-y-2 border-b border-border p-3">
              <div className="flex items-center gap-2">
                <span className={cn('rounded border px-1.5 py-0.5 text-[8px] font-semibold uppercase', stateTone(current.state))}>{current.state}</span>
                <span className="font-mono text-[8px] text-faint-foreground">v{current.version} · fence {current.fenceEpoch}</span>
                {activeStates.has(current.state) && <LoaderCircle className="ml-auto size-3 animate-spin text-primary-bright" />}
              </div>
              <div className="flex items-center justify-between text-[8px] text-faint-foreground">
                <span>Agent event channel</span>
                <span className="font-mono">{streamState} · seq {eventCursorRef.current.sequence}</span>
              </div>
              {recoveryNotice && (
                <div role="status" className="rounded border border-warning/30 bg-warning/10 p-2 text-[8px] leading-3 text-warning">
                  {recoveryNotice}
                </div>
              )}
              <div className="grid grid-cols-2 gap-x-3 gap-y-1 text-[8px]">
                <span className="text-faint-foreground">Base Candidate</span><span className="truncate text-right font-mono" title={current.baseCandidateTreeHash}>{shortHash(current.baseCandidateTreeHash)}</span>
                <span className="text-faint-foreground">TaskCapsule</span><span className="truncate text-right font-mono" title={current.taskCapsule.contentHash}>{shortHash(current.taskCapsule.contentHash)}</span>
                <span className="text-faint-foreground">ContextPack</span><span className="truncate text-right font-mono" title={current.contextPack.contentHash}>{shortHash(current.contextPack.contentHash)}</span>
                <span className="text-faint-foreground">Updated</span><span className="truncate text-right" title={displayTime(current.updatedAt)}>{displayTime(current.updatedAt)}</span>
              </div>
              {current.exitReason && <div className="rounded border border-border bg-background p-2 text-[8px] leading-3 text-muted-foreground">{current.exitReason}</div>}
              {(canCancel || canRetry) && (
                <div className="space-y-1.5">
                  <input value={reason} onChange={(event) => setReason(event.target.value)} placeholder={canRetry ? 'Required retry reason' : 'Required cancellation reason'} className="h-7 w-full rounded border border-border bg-background px-2 text-[9px]" />
                  <div className="flex gap-1.5">
                    {canCancel && <button type="button" onClick={cancelAttempt} disabled={actionBusy || !reason.trim()} className="inline-flex h-7 flex-1 items-center justify-center gap-1 rounded border border-destructive/30 text-[9px] text-destructive disabled:opacity-40"><CircleStop className="size-3" /> Cancel</button>}
                    {canRetry && <button type="button" onClick={retryAttempt} disabled={actionBusy || !reason.trim()} className="inline-flex h-7 flex-1 items-center justify-center gap-1 rounded border border-border text-[9px] text-muted-foreground disabled:opacity-40"><RotateCcw className="size-3" /> Retry exact request</button>}
                  </div>
                </div>
              )}
            </section>

            <div className="flex h-8 border-b border-border px-2">
              {(['changes', 'result', 'logs', 'events'] as const).map((value) => (
                <button key={value} type="button" onClick={() => setTab(value)} className={cn('border-b-2 px-2 text-[8px] capitalize', tab === value ? 'border-primary text-foreground' : 'border-transparent text-faint-foreground')}>{value}</button>
              ))}
            </div>

            <section className="space-y-2 p-3">
              {tab === 'changes' && (
                <ChangesReview
                  patch={patch}
                  validation={validation}
                  reviews={patchReviews}
                  selectedOperation={selectedPatchOperation}
                  reviewedOperationCount={reviewedOperationCount}
                  onSelect={loadPatchOperation}
                  onAcknowledge={acknowledgePatchOperation}
                />
              )}
              {tab === 'result' && <StructuredReview result={structuredResult} />}
              {tab === 'logs' && <LogReview stdout={stdout} stderr={stderr} />}
              {tab === 'events' && <EventReview events={events} />}

              {mergeOutcome?.plan.disposition === 'conflicted' && (
                <ConflictReview title="Merge conflict — no files were written" conflicts={mergeOutcome.plan.conflicts} />
              )}
              {undoOutcome?.plan.disposition === 'conflicted' && (
                <ConflictReview title="Undo conflict — no files were restored" conflicts={undoOutcome.plan.conflicts} />
              )}
              {mergeOutcome?.application && (
                <div className="flex gap-2 rounded border border-success/30 bg-success/10 p-2 text-[8px] leading-3 text-success"><CheckCircle2 className="size-3 shrink-0" />Atomic merge applied at Candidate v{mergeOutcome.application.candidateVersionTo}. The workspace tree was refreshed without reloading the Blueprint.</div>
              )}
              {undoOutcome?.application && (
                <div className="flex gap-2 rounded border border-success/30 bg-success/10 p-2 text-[8px] leading-3 text-success"><CheckCircle2 className="size-3 shrink-0" />Exact merge paths restored at Candidate v{undoOutcome.application.candidateVersionTo}.</div>
              )}

              <div className="space-y-1.5 border-t border-border pt-3">
                <button type="button" onClick={mergePatch} disabled={!canEdit || workspaceBusy || actionBusy || !canMerge || Boolean(mergeReceipt && !mergeReceipt.undoneAt)} title={!canMerge ? 'Merge opens only after the exact Attempt is review_ready, integrity evidence is loaded, and every file body is explicitly reviewed.' : 'Apply the reviewed immutable patch with current Candidate fences.'} className="inline-flex h-8 w-full items-center justify-center gap-1.5 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground disabled:opacity-40">
                  {actionBusy ? <LoaderCircle className="size-3 animate-spin" /> : <FileDiff className="size-3" />}
                  Explicitly merge reviewed patch
                </button>
                <button type="button" onClick={undoPatch} disabled={!canEdit || workspaceBusy || actionBusy || !mergeReceipt || Boolean(mergeReceipt.undoneAt)} className="inline-flex h-8 w-full items-center justify-center gap-1.5 rounded border border-border px-2 text-[9px] text-muted-foreground disabled:opacity-40">
                  <Undo2 className="size-3" />
                  {mergeReceipt?.undoneAt ? 'Merge already undone' : 'Undo last applied Agent merge'}
                </button>
                <p className="text-[8px] leading-3 text-faint-foreground">Merge is never automatic. A conflict or fence mismatch writes zero files. Immutable Merge and Undo receipts are recovered from the authorized server history after reload or on another browser.</p>
              </div>
            </section>
          </>
        ) : (
          <div className="p-6 text-center text-[9px] leading-4 text-faint-foreground">Create an exact task to start a controlled AgentAttempt.</div>
        )}
      </div>
    </aside>
  )
}

function ChangesReview({
  patch,
  validation,
  reviews,
  selectedOperation,
  reviewedOperationCount,
  onSelect,
  onAcknowledge,
}: {
  readonly patch: AgentPlatformPatchDto | null
  readonly validation: AgentPatchValidationDto | null
  readonly reviews: Readonly<Record<string, PatchOperationReview>>
  readonly selectedOperation: string
  readonly reviewedOperationCount: number
  readonly onSelect: (operation: AgentFileOperationDto) => Promise<void>
  readonly onAcknowledge: (operation: AgentFileOperationDto, acknowledged: boolean) => void
}) {
  if (!patch) return <div className="text-[9px] leading-4 text-faint-foreground">Patch evidence is not finalized yet.</div>
  const operation = patch.operations.find((item) => patchOperationKey(item) === selectedOperation)
  const selectedReview = operation ? reviews[patchOperationKey(operation)] : undefined
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-[8px] text-muted-foreground">
        <span>{patch.operations.length} operation{patch.operations.length === 1 ? '' : 's'}</span>
        <span>·</span>
        <span>{patch.changedBytes} changed bytes</span>
        <span>·</span>
        <span>{reviewedOperationCount}/{patch.operations.length} reviewed</span>
        <span className="ml-auto font-mono" title={patch.contentHash}>{shortHash(patch.contentHash)}</span>
      </div>
      <div className="space-y-1">
        {patch.operations.length === 0 && <div className="rounded border border-border p-2 text-[8px] text-faint-foreground">No file operations.</div>}
        {patch.operations.map((item) => {
          const key = patchOperationKey(item)
          const review = reviews[key]
          return (
          <button
            type="button"
            key={key}
            onClick={() => void onSelect(item)}
            className={cn(
              'w-full rounded border bg-background p-2 text-left transition-colors hover:border-primary/50',
              selectedOperation === key ? 'border-primary/60' : 'border-border',
            )}
          >
            <div className="flex items-center gap-2 text-[8px]">
              <span className={cn('rounded px-1 py-0.5 font-semibold uppercase', item.kind === 'file.delete' ? 'bg-destructive/10 text-destructive' : 'bg-success/10 text-success')}>{item.kind === 'file.delete' ? 'delete' : 'upsert'}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-foreground" title={item.path}>{item.path}</span>
              {review?.loading && <LoaderCircle className="size-3 animate-spin text-primary-bright" />}
              {review?.acknowledged && <CheckCircle2 className="size-3 text-success" />}
              {review?.error && <AlertTriangle className="size-3 text-destructive" />}
            </div>
            <div className="mt-1 flex gap-2 font-mono text-[7px] text-faint-foreground">
              <span title={item.expectedHash}>base {shortHash(item.expectedHash ?? 'absent')}</span>
              <span>→</span>
              <span title={item.contentHash}>next {item.kind === 'file.delete' ? 'absent' : shortHash(item.contentHash ?? '')}</span>
              {item.byteSize !== undefined && <span className="ml-auto">{item.byteSize} B</span>}
            </div>
          </button>
        )})}
      </div>

      {!operation && patch.operations.length > 0 && (
        <div className="rounded border border-dashed border-border p-3 text-center text-[8px] leading-4 text-faint-foreground">Select a file operation to load its authorized base and proposed bytes.</div>
      )}

      {operation && (
        <div className="overflow-hidden rounded border border-border bg-background">
          <div className="flex items-center gap-2 border-b border-border px-2 py-1.5 text-[8px]">
            <FileDiff className="size-3 text-primary-bright" />
            <span className="min-w-0 flex-1 truncate font-mono" title={operation.path}>{operation.path}</span>
            {selectedReview?.base?.exists && (
              <button type="button" onClick={() => downloadPatchFile(selectedReview.base!, operation.path, 'base')} className="inline-flex items-center gap-1 text-faint-foreground hover:text-foreground"><Download className="size-3" /> Base</button>
            )}
            {selectedReview?.proposed?.exists && (
              <button type="button" onClick={() => downloadPatchFile(selectedReview.proposed!, operation.path, 'proposed')} className="inline-flex items-center gap-1 text-faint-foreground hover:text-foreground"><Download className="size-3" /> Proposed</button>
            )}
          </div>

          {selectedReview?.loading && (
            <div className="flex h-32 items-center justify-center gap-2 text-[9px] text-faint-foreground"><LoaderCircle className="size-3 animate-spin" /> Loading authorized base and proposed bytes…</div>
          )}
          {selectedReview?.error && (
            <div className="space-y-2 p-3 text-[8px] text-destructive">
              <div className="flex gap-2"><AlertTriangle className="size-3 shrink-0" />{selectedReview.error}</div>
              <button type="button" onClick={() => void onSelect(operation)} className="h-7 rounded border border-destructive/30 px-2">Retry exact file load</button>
            </div>
          )}
          {!selectedReview && (
            <div className="flex h-28 items-center justify-center text-[9px] text-faint-foreground">Select this operation again to load its exact bytes.</div>
          )}
          {selectedReview?.base && selectedReview.proposed && !selectedReview.loading && !selectedReview.error && (
            <>
              <div className="grid grid-cols-2 border-b border-border text-[7px] text-faint-foreground">
                <PatchSideIdentity title="Base" file={selectedReview.base} />
                <PatchSideIdentity title="Proposed" file={selectedReview.proposed} right />
              </div>
              {selectedReview.base.binary || selectedReview.proposed.binary ? (
                <div className="space-y-2 p-3 text-[8px] leading-4 text-muted-foreground">
                  <div className="flex gap-2"><AlertTriangle className="mt-0.5 size-3 shrink-0" />At least one side is binary. It is never decoded or inserted into the page; use the exact-byte download controls to inspect it.</div>
                </div>
              ) : (
                <div className="h-72">
                  <MonacoDiffEditor
                    key={`${patch.contentHash}:${patchOperationKey(operation)}`}
                    original={selectedReview.base.text ?? ''}
                    modified={selectedReview.proposed.text ?? ''}
                    language={patchLanguage(operation.path)}
                    theme="vs-dark"
                    options={{
                      automaticLayout: true,
                      readOnly: true,
                      originalEditable: false,
                      renderSideBySide: false,
                      fontSize: 10,
                      minimap: { enabled: false },
                      scrollBeyondLastLine: false,
                    }}
                  />
                </div>
              )}
              <label className="flex cursor-pointer items-start gap-2 border-t border-border p-2 text-[8px] leading-3 text-muted-foreground">
                <input
                  type="checkbox"
                  checked={selectedReview.acknowledged}
                  onChange={(event) => onAcknowledge(operation, event.target.checked)}
                  className="mt-0.5"
                />
                I reviewed the authorized exact base and proposed content for this operation.
              </label>
            </>
          )}
        </div>
      )}
      <div className={cn('rounded border p-2 text-[8px] leading-3', validation?.decision === 'reviewable' ? 'border-success/30 bg-success/10 text-success' : 'border-border text-muted-foreground')}>
        {validation ? `${validation.decision} · ${validation.checks.length} integrity checks · independent quality review required` : 'Validation evidence is not finalized yet.'}
      </div>
      <p className="text-[8px] leading-3 text-faint-foreground">Each file is resolved from the finalized patch and exact base tree through an authorized server endpoint. Merge stays closed until every operation is explicitly reviewed; the atomic server merge still rechecks all Candidate fences.</p>
    </div>
  )
}

function PatchSideIdentity({
  title,
  file,
  right = false,
}: {
  readonly title: string
  readonly file: PatchFileView
  readonly right?: boolean
}) {
  return (
    <div className={cn('space-y-0.5 p-2', right && 'border-l border-border text-right')}>
      <div className="font-semibold text-muted-foreground">{title}</div>
      {file.exists ? (
        <>
          <div className="font-mono" title={file.contentHash}>{shortHash(file.contentHash)} · {file.byteSize} B</div>
          <div className="font-mono">{file.mode}{file.binary ? ' · binary' : ' · UTF-8 text'}</div>
        </>
      ) : <div className="font-mono">absent</div>}
    </div>
  )
}

function StructuredReview({ result }: { readonly result: AgentStructuredResultDto | null }) {
  if (!result) return <div className="text-[9px] leading-4 text-faint-foreground">Structured result is not finalized yet.</div>
  return (
    <div className="space-y-2 text-[8px] leading-4">
      <div className="rounded border border-border bg-background p-2 text-muted-foreground">{result.summary || 'No summary was supplied.'}</div>
      {result.verification.map((check) => <div key={check.commandId} className="flex gap-2 rounded border border-border p-2"><span className={check.status === 'passed' ? 'text-success' : check.status === 'failed' ? 'text-destructive' : 'text-muted-foreground'}>{check.status}</span><span className="font-mono">{check.commandId}</span><span className="ml-auto text-faint-foreground">{check.note}</span></div>)}
      {result.blockers.map((blocker, index) => <div key={`${index}:${blocker}`} className="flex gap-2 rounded border border-destructive/30 bg-destructive/10 p-2 text-destructive"><AlertTriangle className="mt-0.5 size-3 shrink-0" />{blocker}</div>)}
      {result.changedPaths.length > 0 && <div className="text-faint-foreground">Changed paths: {result.changedPaths.join(', ')}</div>}
    </div>
  )
}

function LogReview({ stdout, stderr }: { readonly stdout: string; readonly stderr: string }) {
  if (!stdout && !stderr) return <div className="text-[9px] leading-4 text-faint-foreground">No finalized stdout or stderr evidence.</div>
  return (
    <div className="space-y-2">
      {stdout && <LogBlock title="stdout" value={stdout} />}
      {stderr && <LogBlock title="stderr" value={stderr} destructive />}
    </div>
  )
}

function LogBlock({ title, value, destructive = false }: { readonly title: string; readonly value: string; readonly destructive?: boolean }) {
  return <div className="overflow-hidden rounded border border-border"><div className={cn('border-b border-border px-2 py-1 text-[8px] font-semibold', destructive ? 'text-destructive' : 'text-muted-foreground')}>{title}</div><pre className="max-h-52 overflow-auto whitespace-pre-wrap break-all bg-background p-2 font-mono text-[8px] leading-3 text-muted-foreground">{value}</pre></div>
}

function EventReview({ events }: { readonly events: readonly AgentAttemptEventDto[] }) {
  if (events.length === 0) return <div className="text-[9px] leading-4 text-faint-foreground">No lifecycle events returned.</div>
  return <div className="space-y-1">{events.map((event) => <div key={event.sequence} className="rounded border border-border bg-background p-2 text-[8px] leading-3"><div className="flex gap-2"><span className="font-mono text-faint-foreground">#{event.sequence}</span><span>{event.stateFrom} → {event.stateTo}</span><span className="ml-auto text-faint-foreground">{displayTime(event.createdAt)}</span></div><div className="mt-1 text-muted-foreground">{event.reason}</div></div>)}</div>
}

function ConflictReview({
  title,
  conflicts,
}: {
  readonly title: string
  readonly conflicts: readonly { readonly path: string; readonly reason: string }[]
}) {
  return (
    <div className="rounded border border-destructive/30 bg-destructive/10 p-2 text-[8px] text-destructive">
      <div className="mb-1 flex items-center gap-1 font-semibold"><AlertTriangle className="size-3" />{title}</div>
      {conflicts.map((conflict) => <div key={`${conflict.path}:${conflict.reason}`} className="font-mono leading-3">{conflict.path}: {conflict.reason}</div>)}
    </div>
  )
}
