'use client'

import dynamic from 'next/dynamic'
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import {
  AlertTriangle,
  Bot,
  Braces,
  CheckCircle2,
  ChevronRight,
  CircleStop,
  Code2,
  Columns3,
  Download,
  FileDiff,
  FilePlus2,
  FileText,
  GitMerge,
  LoaderCircle,
  MonitorPlay,
  PackageCheck,
  Pause,
  Pencil,
  Play,
  Power,
  RefreshCw,
  Save,
  Search,
  Server,
  ShieldAlert,
  ShieldCheck,
  SquareTerminal,
  Trash2,
  Wifi,
  WifiOff,
  X,
} from 'lucide-react'
import { AgentPanel } from './agent-panel'
import {
  candidateSearchRetryIdentity,
  resolveCandidateSearchFailure,
} from './candidate-search-failure'
import { resolveSandboxLSPAdmission } from './sandbox-lsp-admission'
import { SandboxLSPStatus } from './sandbox-lsp-status'
import { useSandboxLSP } from './use-sandbox-lsp'
import { useCollaboration } from '@/lib/collaboration/provider'
import type { AgentCreateAttemptInput } from '@/lib/platform/agent-client'
import { PlatformHttpError } from '@/lib/platform/http'
import { candidateDocumentURI } from '@/lib/platform/lsp-contract'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import { resolveCandidateHeadSelection } from '@/lib/platform/repository-candidate-head'
import type {
  RepositoryCandidateSearchMatchDto,
  RepositoryCandidateSearchResultDto,
  CandidateRebaseConflictContentDto,
  CandidateRebaseConflictDto,
  CandidateRebaseResolutionStrategy,
  CandidateRebaseResultDto,
  RepositoryCandidateHeadDto,
} from '@/lib/platform/repository-contract'
import { candidateSearchResultMatchesCandidate } from '@/lib/platform/repository-contract'
import {
  sandboxFences,
  type CandidateWorkspaceDto,
  type RepositoryTreeFileDto,
  type SandboxFences,
  type SandboxPortListDto,
  type SandboxPreviewLinkDto,
  type SandboxProcessDto,
  type SandboxRepositoryViewDto,
  type SandboxSessionDto,
  type SandboxTerminalDto,
} from '@/lib/platform/sandbox-contract'
import type { SandboxClient } from '@/lib/platform/sandbox-client'
import { candidateAbandonEntryAllowed } from '@/lib/platform/sandbox-abandon'
import {
  candidateFileReadCommitDecision,
  createExactCandidateFileOpenFence,
  openFileHeadRefreshDisposition,
  type ExactCandidateFileOpenFence,
} from '@/lib/platform/sandbox-file-open'
import type { SandboxStreamConnection } from '@/lib/platform/sandbox-stream'
import type {
  CandidateVerificationReceiptDto,
  CandidateVerificationRunViewDto,
  VerificationProfileSummaryDto,
} from '@/lib/platform/verification-contract'
import { cn } from '@/lib/utils'

const MonacoEditor = dynamic(
  () => import('@monaco-editor/react').then((module) => module.default),
  { ssr: false },
)

const MonacoDiffEditor = dynamic(
  () => import('@monaco-editor/react').then((module) => module.DiffEditor),
  { ssr: false },
)

type SandboxWorkspaceMode = 'code' | 'preview'
type WorkspacePhase = 'idle' | 'opening' | 'select-head' | 'rebase' | 'ready' | 'error'
type SaveState = 'saved' | 'dirty' | 'saving' | 'error' | 'stale'
type CandidateSearchStatus = 'idle' | 'waiting' | 'searching' | 'refreshing' | 'blocked'

interface OpenFile {
  readonly path: string
  readonly mode: '100644' | '100755'
  readonly contentHash: string | 'absent'
  readonly savedContent: string
  readonly draft: string
  readonly binary: boolean
  readonly bytes?: Uint8Array
  readonly openFence?: ExactCandidateFileOpenFence
  readonly stale?: boolean
  readonly staleReason?: string
}

interface PersistedSession {
  readonly candidateId?: string
  readonly sessionId?: string
  readonly sessionKey: string
}

interface PersistedActiveCandidate {
  readonly candidateId: string
  readonly buildManifestId: string
  readonly rebaseId?: string
}


interface PersistedVerificationRun {
  readonly runId: string
}

interface PendingSave {
  readonly sessionId: string
  readonly path: string
  readonly content: string
  readonly expectedHash: string | 'absent'
  readonly mode: '100644' | '100755'
  readonly idempotencyKey: string
  readonly fences: SandboxFences
}

interface MarkerSummary {
  readonly severity: number
  readonly message: string
  readonly startLineNumber: number
  readonly startColumn: number
}

const textDecoder = new TextDecoder('utf-8', { fatal: true })

function decodeCandidateFile(value: ArrayBuffer) {
  const bytes = new Uint8Array(value)
  if (bytes.some((byte) => byte === 0)) return { binary: true as const, bytes }
  try {
    const text = textDecoder.decode(bytes)
    const controls = [...text].filter((character) => {
      const code = character.charCodeAt(0)
      return code < 32 && code !== 9 && code !== 10 && code !== 13
    }).length
    if (controls > Math.max(8, Math.floor(text.length / 100))) {
      return { binary: true as const, bytes }
    }
    return { binary: false as const, text }
  } catch {
    return { binary: true as const, bytes }
  }
}

function randomKey(prefix: string) {
  const value = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${value}`.replace(/[^A-Za-z0-9._:~-]/g, '-').slice(0, 128)
}

function deterministicOperationKey(prefix: string, value: string) {
  const seeds = [0x811c9dc5, 0x9e3779b9, 0x85ebca6b, 0xc2b2ae35]
  const digest = seeds.map((seed) => {
    let hash = seed
    for (let index = 0; index < value.length; index += 1) {
      hash ^= value.charCodeAt(index)
      hash = Math.imul(hash, 0x01000193)
    }
    return (hash >>> 0).toString(16).padStart(8, '0')
  }).join('')
  return `${prefix}-${digest}`.slice(0, 128)
}

function newUUID() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const bytes = new Uint8Array(16)
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Math.floor(Math.random() * 256)
  }
  bytes[6] = (bytes[6]! & 0x0f) | 0x40
  bytes[8] = (bytes[8]! & 0x3f) | 0x80
  const value = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${value.slice(0, 8)}-${value.slice(8, 12)}-${value.slice(12, 16)}-${value.slice(16, 20)}-${value.slice(20)}`
}

function errorMessage(cause: unknown) {
  if (cause instanceof PlatformHttpError) return cause.problem.detail || cause.problem.title
  return cause instanceof Error ? cause.message : 'The sandbox operation failed.'
}

function fileLanguage(path: string) {
  const extension = path.split('.').pop()?.toLowerCase()
  return ({
    ts: 'typescript', tsx: 'typescript', js: 'javascript', jsx: 'javascript',
    json: 'json', css: 'css', scss: 'scss', html: 'html', md: 'markdown',
    py: 'python', go: 'go', sql: 'sql', yaml: 'yaml', yml: 'yaml',
    sh: 'shell', bash: 'shell', toml: 'ini', xml: 'xml',
  } as Record<string, string>)[extension ?? ''] ?? 'plaintext'
}

function fileLSPLanguage(path: string) {
  const extension = path.split('.').pop()?.toLowerCase()
  return ({
    ts: 'typescript', tsx: 'typescriptreact', js: 'javascript', jsx: 'javascriptreact',
    json: 'json', css: 'css', scss: 'scss', html: 'html', md: 'markdown',
    py: 'python', go: 'go', sql: 'sql', yaml: 'yaml', yml: 'yaml',
    sh: 'shellscript', bash: 'shellscript', toml: 'toml', xml: 'xml',
  } as Record<string, string>)[extension ?? ''] ?? 'plaintext'
}

function candidateEditorModelURI(projectId: string, candidateId: string, path: string) {
  try {
    return candidateDocumentURI(projectId, candidateId, path)
  } catch {
    // The exact LSP admission remains blocked; Monaco can still show the draft.
    return path
  }
}

function fileNeedsSave(file: OpenFile) {
  return !file.binary && (file.contentHash === 'absent' || file.draft !== file.savedContent)
}

function candidateSearchHasControl(value: string) {
  return [...value].some((character) => {
    const code = character.codePointAt(0) ?? 0
    return code < 0x20 || code === 0x7f
  })
}

function candidateSearchGlobs(value: string) {
  const globs = value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean)
  if (globs.length > 16) return { globs: [] as string[], error: 'Use at most 16 include patterns.' }
  if (globs.some((glob) => new TextEncoder().encode(glob).byteLength > 256)) {
    return { globs: [] as string[], error: 'Each include pattern is limited to 256 UTF-8 bytes.' }
  }
  if (globs.some((glob) => glob.startsWith('/') || glob.includes('\\') || candidateSearchHasControl(glob))) {
    return { globs: [] as string[], error: 'Include patterns must be canonical repository globs.' }
  }
  return { globs, error: null }
}

function candidateSearchQueryError(query: string, caseSensitive: boolean) {
  if (!query) return null
  if (new TextEncoder().encode(query).byteLength > 256) return 'Literal query is limited to 256 UTF-8 bytes.'
  if (candidateSearchHasControl(query)) return 'Literal query cannot contain control characters.'
  if (!caseSensitive && [...query].some((character) => (character.codePointAt(0) ?? 0) > 0x7f)) {
    return 'Case-insensitive Candidate search currently accepts ASCII literals only.'
  }
  return null
}

function compactByteSize(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`
}

function upsertCandidateTreeFile(
  files: readonly RepositoryTreeFileDto[],
  file: RepositoryTreeFileDto,
) {
  return [...files.filter((entry) => entry.path !== file.path), file]
    .sort((left, right) => left.path.localeCompare(right.path))
}

function storageValue<T>(key: string): T | undefined {
  if (typeof window === 'undefined') return undefined
  try {
    const value = window.localStorage.getItem(key)
    return value ? JSON.parse(value) as T : undefined
  } catch {
    return undefined
  }
}

function setStorageValue(key: string, value?: unknown) {
  if (typeof window === 'undefined') return
  if (value === undefined) window.localStorage.removeItem(key)
  else window.localStorage.setItem(key, JSON.stringify(value))
}

function delay(milliseconds: number, signal?: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('Aborted', 'AbortError'))
      return
    }
    const timer = window.setTimeout(resolve, milliseconds)
    signal?.addEventListener('abort', () => {
      window.clearTimeout(timer)
      reject(new DOMException('Aborted', 'AbortError'))
    }, { once: true })
  })
}

function conflictText(value?: { readonly encoding: 'base64'; readonly data: string }) {
  if (!value) return ''
  try {
    const binary = window.atob(value.data)
    const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0))
    return new TextDecoder('utf-8', { fatal: false }).decode(bytes)
  } catch {
    return ''
  }
}


function verificationRunStorageKey(sessionId: string) {
  return `worksflow:verification-run:${sessionId}`
}

function verificationProfileKey(profile: VerificationProfileSummaryDto) {
  const reference = profile.verificationProfile
  return `${reference.id}@${reference.version}:${reference.contentHash}`
}

function hasExactCandidateCheckpoint(
  session: SandboxSessionDto,
  candidate: CandidateWorkspaceDto,
) {
  const checkpoint = session.latestCheckpoint
  return Boolean(
    checkpoint
    && checkpoint.candidateId === candidate.id
    && checkpoint.candidateVersion === candidate.version
    && checkpoint.journalSequence === candidate.journalSequence
    && checkpoint.sessionEpoch === session.sessionEpoch
    && checkpoint.writerLeaseEpoch === candidate.writerLeaseEpoch
    && checkpoint.treeHash === candidate.treeHash,
  )
}

function exactVerificationSubject(
  view: CandidateVerificationRunViewDto | null,
  session: SandboxSessionDto,
  candidate: CandidateWorkspaceDto,
) {
  const checkpoint = session.latestCheckpoint
  return Boolean(
    view
    && checkpoint
    && view.subject.sessionId === session.id
    && view.subject.sessionVersion === session.version
    && view.subject.candidateId === candidate.id
    && view.subject.candidateSnapshotId === checkpoint.id
    && view.subject.candidateVersion === candidate.version
    && view.subject.journalSequence === candidate.journalSequence
    && view.subject.sessionEpoch === session.sessionEpoch
    && view.subject.writerLeaseEpoch === candidate.writerLeaseEpoch
    && view.subject.treeHash === candidate.treeHash,
  )
}

export function SandboxWorkspace({
  mode,
  projectId,
  buildManifestId,
}: {
  readonly mode: SandboxWorkspaceMode
  readonly projectId: string
  readonly buildManifestId: string
}) {
  const { platformClient, can } = useCollaboration()
  const flow = usePlatformFlow()
  const repository = platformClient.repository
  const sandbox = platformClient.sandbox
  const agent = platformClient.agent
  const verification = platformClient.verification
  const sessionStorageKey = `worksflow:sandbox:${projectId}:${buildManifestId}`
  const activeCandidateStorageKey = `worksflow:sandbox-active-candidate:${projectId}`
  const [phase, setPhase] = useState<WorkspacePhase>('idle')
  const [error, setError] = useState<string | null>(null)
  const [session, setSession] = useState<SandboxSessionDto | null>(null)
  const [candidate, setCandidate] = useState<CandidateWorkspaceDto | null>(null)
  const [fences, setFences] = useState<SandboxFences | null>(null)
  const [lspAuthorityNow, setLSPAuthorityNow] = useState(() => Date.now())
  const [treeFiles, setTreeFiles] = useState<readonly RepositoryTreeFileDto[]>([])
  const [selectedFile, setSelectedFile] = useState<OpenFile | null>(null)
  const [newPath, setNewPath] = useState('')
  const [renamePath, setRenamePath] = useState('')
  const [renameOpen, setRenameOpen] = useState(false)
  const [renameBusy, setRenameBusy] = useState(false)
  const [saveState, setSaveState] = useState<SaveState>('saved')
  const [candidateSearchQuery, setCandidateSearchQuery] = useState('')
  const [candidateSearchCaseSensitive, setCandidateSearchCaseSensitive] = useState(true)
  const [candidateSearchInclude, setCandidateSearchInclude] = useState('')
  const [candidateSearchStatus, setCandidateSearchStatus] = useState<CandidateSearchStatus>('idle')
  const [candidateSearchResult, setCandidateSearchResult] = useState<RepositoryCandidateSearchResultDto | null>(null)
  const [candidateSearchError, setCandidateSearchError] = useState<string | null>(null)
  const [candidateSearchHeadRefreshAllowed, setCandidateSearchHeadRefreshAllowed] = useState(false)
  const [candidateSearchRefreshToken, setCandidateSearchRefreshToken] = useState(0)
  const [candidateSearchRetryToken, setCandidateSearchRetryToken] = useState(0)
  const [markers, setMarkers] = useState<readonly MarkerSummary[]>([])
  const [inspector, setInspector] = useState<'diff' | 'problems'>('diff')
  const [process, setProcess] = useState<SandboxProcessDto | null>(null)
  const [processEtag, setProcessEtag] = useState('')
  const [ports, setPorts] = useState<SandboxPortListDto['ports']>([])
  const [preview, setPreview] = useState<SandboxPreviewLinkDto | null>(null)
  const [terminal, setTerminal] = useState<SandboxTerminalDto | null>(null)
  const [showTerminal, setShowTerminal] = useState(false)
  const [showAgent, setShowAgent] = useState(false)
  const [agentMutationBusy, setAgentMutationBusy] = useState(false)
  const [viewport, setViewport] = useState<'desktop' | 'tablet' | 'mobile'>('desktop')
  const [selectedService, setSelectedService] = useState('')
  const [selectedProfile, setSelectedProfile] = useState('dev')
  const [runtimeBusy, setRuntimeBusy] = useState(false)
  const [lifecycleBusy, setLifecycleBusy] = useState(false)
  const [terminateOpen, setTerminateOpen] = useState(false)
  const [terminationReason, setTerminationReason] = useState('')
  const [abandonOpen, setAbandonOpen] = useState(false)
  const [abandonmentReason, setAbandonmentReason] = useState('')
  const [abandonConfirmed, setAbandonConfirmed] = useState(false)
  const [abandonBusy, setAbandonBusy] = useState(false)
  const [checkpointBusy, setCheckpointBusy] = useState(false)
  const [freezeBusy, setFreezeBusy] = useState(false)
  const [freezeNotice, setFreezeNotice] = useState<string | null>(null)
  const [verificationProfiles, setVerificationProfiles] = useState<readonly VerificationProfileSummaryDto[]>([])
  const [selectedVerificationProfileKey, setSelectedVerificationProfileKey] = useState('')
  const [verificationRun, setVerificationRun] = useState<CandidateVerificationRunViewDto | null>(null)
  const [verificationReceipt, setVerificationReceipt] = useState<CandidateVerificationReceiptDto | null>(null)
  const [verificationBusy, setVerificationBusy] = useState(false)
  const [verificationError, setVerificationError] = useState<string | null>(null)
  const [verificationRetryReason, setVerificationRetryReason] = useState('')
  const [candidateHeads, setCandidateHeads] = useState<readonly RepositoryCandidateHeadDto[]>([])
  const [rebase, setRebase] = useState<CandidateRebaseResultDto | null>(null)
  const [selectedConflictId, setSelectedConflictId] = useState('')
  const [conflictContent, setConflictContent] = useState<CandidateRebaseConflictContentDto | null>(null)
  const [conflictDraft, setConflictDraft] = useState('')
  const [conflictMode, setConflictMode] = useState<'100644' | '100755'>('100644')
  const [rebaseBusy, setRebaseBusy] = useState(false)
  const sessionRef = useRef<SandboxSessionDto | null>(null)
  const candidateRef = useRef<CandidateWorkspaceDto | null>(null)
  const fencesRef = useRef<SandboxFences | null>(null)
  const selectedFileRef = useRef<OpenFile | null>(null)
  const pendingSaveRef = useRef<PendingSave | null>(null)
  const mutationChainRef = useRef<Promise<void>>(Promise.resolve())
  const agentMutationBusyRef = useRef(false)
  const openAbortRef = useRef<AbortController | null>(null)
  const fileOpenAbortRef = useRef<AbortController | null>(null)
  const fileOpenGenerationRef = useRef(0)
  const candidateSearchAbortRef = useRef<AbortController | null>(null)
  const candidateSearchRequestRef = useRef(0)
  const candidateSearchAutomaticRetryIdentityRef = useRef<string | null>(null)
  const autoRestoreRef = useRef(false)
  const canMutateCandidate = Boolean(
    can('edit')
    && !abandonBusy
    && candidate?.status === 'active'
    && session?.allowedActions.includes('edit'),
  )
  const abandonActionBlocked = Boolean(
    abandonBusy
    || saveState !== 'saved'
    || (selectedFile && fileNeedsSave(selectedFile))
    || renameBusy
    || checkpointBusy
    || freezeBusy
    || agentMutationBusy
    || verificationBusy
    || lifecycleBusy
    || runtimeBusy,
  )
  const candidateHeadMutationInFlight = Boolean(
    phase === 'rebase'
    || rebaseBusy
    || renameBusy
    || agentMutationBusy
    || saveState !== 'saved'
    || (selectedFile && fileNeedsSave(selectedFile))
    || abandonBusy
    || freezeBusy,
  )
  const candidateSearchHeadResolved = Boolean(
    phase === 'ready'
    && candidate
    && candidate.projectId === projectId
    && candidate.id
    && candidate.version > 0
    && /^sha256:[0-9a-f]{64}$/.test(candidate.treeHash)
    && candidate.treeHash === candidate.currentTree.treeHash
    && session?.candidate.id === candidate.id
    && session.candidate.version === candidate.version
    && session.candidate.treeHash === candidate.treeHash
    && !candidate.conflicted
    && !candidate.stale
    && !candidate.rebaseRequired,
  )

  const copy = useMemo(() => ({
    open: 'Open development sandbox',
    reconnect: 'Reconnect sandbox',
    opening: 'Materializing exact WorkspaceRevision and starting the isolated runtime…',
    unavailable: 'The authenticated platform client does not expose Repository/Sandbox APIs.',
    description: 'Edits are saved to a durable Candidate. Blueprint and document state are not reloaded by autosave.',
  }), [])

  const updateSession = useCallback((next: SandboxSessionDto, headers: Headers) => {
    const nextFences = sandboxFences(headers, next)
    sessionRef.current = next
    fencesRef.current = nextFences
    setSession(next)
    setFences(nextFences)
  }, [])

  const updateCandidate = useCallback((next: CandidateWorkspaceDto) => {
    candidateRef.current = next
    setCandidate(next)
  }, [])


  const adoptVerificationRun = useCallback(async (
    view: CandidateVerificationRunViewDto,
    signal?: AbortSignal,
  ) => {
    if (!verification) {
      setVerificationError('Verification APIs are unavailable in this deployment.')
      return
    }
    setVerificationRun(view)
    const currentSession = sessionRef.current
    if (currentSession && view.subject.sessionId === currentSession.id) {
      setStorageValue(verificationRunStorageKey(currentSession.id), {
        runId: view.run.id,
      } satisfies PersistedVerificationRun)
    }
    if (!view.receipt) {
      setVerificationReceipt(null)
      return
    }
    try {
      const result = await verification.getReceipt(view.receipt.id, { signal })
      if (
        result.data.id !== view.receipt.id
        || result.data.payloadHash !== view.receipt.contentHash
        || result.data.runId !== view.run.id
      ) {
        throw new Error('The VerificationReceipt does not match the exact Run reference.')
      }
      setVerificationReceipt(result.data)
    } catch (cause) {
      if (signal?.aborted) return
      setVerificationReceipt(null)
      setVerificationError(`VerificationReceipt could not be loaded: ${errorMessage(cause)}`)
    }
  }, [verification])

  const loadVerificationContext = useCallback(async (
    sessionId: string,
    signal?: AbortSignal,
  ) => {
    if (!verification) {
      setVerificationError('Verification APIs are unavailable in this deployment.')
      return
    }
    setVerificationError(null)
    setVerificationRun(null)
    setVerificationReceipt(null)
    try {
      const result = await verification.listProfiles(sessionId, { signal })
      setVerificationProfiles(result.data.profiles)
      setSelectedVerificationProfileKey((current) =>
        result.data.profiles.some((profile) => verificationProfileKey(profile) === current)
          ? current
          : result.data.profiles[0] ? verificationProfileKey(result.data.profiles[0]) : '')
    } catch (cause) {
      if (signal?.aborted) return
      setVerificationProfiles([])
      setSelectedVerificationProfileKey('')
      setVerificationError(`Verification profiles are unavailable: ${errorMessage(cause)}`)
    }

    const persisted = storageValue<PersistedVerificationRun>(verificationRunStorageKey(sessionId))
    try {
      const result = await verification.listRuns(sessionId, 20, { signal })
      const sessionRuns = result.data.runs.filter((view) => view.subject.sessionId === sessionId)
      const currentSession = sessionRef.current
      const currentCandidate = candidateRef.current
      const selected = currentSession && currentCandidate
        ? sessionRuns.find((view) => exactVerificationSubject(view, currentSession, currentCandidate))
          ?? sessionRuns[0]
        : sessionRuns[0]
      if (!selected) {
        setStorageValue(verificationRunStorageKey(sessionId))
        return
      }
      await adoptVerificationRun(selected, signal)
    } catch (cause) {
      if (signal?.aborted) return
      // localStorage is only a recovery hint. The hinted Run must still be
      // loaded and session-scoped by the server before the UI can adopt it.
      if (persisted?.runId) {
        try {
          const hinted = await verification.getRun(persisted.runId, { signal })
          if (hinted.data.subject.sessionId === sessionId) {
            await adoptVerificationRun(hinted.data, signal)
            return
          }
          setStorageValue(verificationRunStorageKey(sessionId))
        } catch (hintCause) {
          if (signal?.aborted) return
          if (hintCause instanceof PlatformHttpError && hintCause.status === 404) {
            setStorageValue(verificationRunStorageKey(sessionId))
          }
        }
      }
      setVerificationError(`Verification history could not be restored: ${errorMessage(cause)}`)
    }
  }, [adoptVerificationRun, verification])

  const enqueueMutation = useCallback(function enqueueMutation<T>(operation: () => Promise<T>) {
    const next = mutationChainRef.current.then(operation, operation)
    mutationChainRef.current = next.then(() => undefined, () => undefined)
    return next
  }, [])

  const pendingStorageKey = useCallback(
    (sessionId: string) => `worksflow:sandbox-pending-save:${sessionId}`,
    [],
  )

  const cancelCandidateFileOpen = useCallback(() => {
    fileOpenGenerationRef.current += 1
    fileOpenAbortRef.current?.abort()
    fileOpenAbortRef.current = null
  }, [])

  const adoptCandidateRepositoryView = useCallback((
    view: SandboxRepositoryViewDto,
    headers: Headers,
  ) => {
    cancelCandidateFileOpen()
    const nextFences = sandboxFences(headers, view.session)
    const currentOpenFile = selectedFileRef.current
    if (currentOpenFile) {
      const dirty = fileNeedsSave(currentOpenFile)
      const disposition = openFileHeadRefreshDisposition({
        path: currentOpenFile.path,
        contentHash: currentOpenFile.contentHash,
        dirty,
        nextFiles: view.tree.files,
      })
      if (disposition === 'rebind') {
        const nextTreeFile = view.tree.files.find((file) => file.path === currentOpenFile.path)
        const nextOpenFence = createExactCandidateFileOpenFence({
          projectId,
          session: view.session,
          candidate: view.candidate,
          fences: nextFences,
          path: currentOpenFile.path,
          observedFile: nextTreeFile,
          expectedContentHash: currentOpenFile.contentHash === 'absent'
            ? undefined
            : currentOpenFile.contentHash,
        })
        if (nextTreeFile && nextOpenFence) {
          const rebound: OpenFile = {
            ...currentOpenFile,
            mode: nextTreeFile.mode === '100755' ? '100755' : '100644',
            openFence: nextOpenFence,
            stale: false,
            staleReason: undefined,
          }
          selectedFileRef.current = rebound
          setSelectedFile(rebound)
          setSaveState(dirty ? 'dirty' : 'saved')
        } else if (dirty) {
          const stale: OpenFile = {
            ...currentOpenFile,
            stale: true,
            staleReason: 'The refreshed Candidate head cannot prove this draft still has the same exact base file.',
          }
          selectedFileRef.current = stale
          setSelectedFile(stale)
          setSaveState('stale')
          setError(stale.staleReason ?? 'The open draft is stale.')
        } else {
          selectedFileRef.current = null
          setSelectedFile(null)
          setMarkers([])
          setSaveState('saved')
        }
      } else if (disposition === 'preserve_stale') {
        const stale: OpenFile = {
          ...currentOpenFile,
          stale: true,
          staleReason: 'The file hash backing this dirty draft is no longer present at the refreshed Candidate head. The draft was preserved but cannot be saved.',
        }
        selectedFileRef.current = stale
        setSelectedFile(stale)
        setSaveState('stale')
        setError(stale.staleReason ?? 'The open draft is stale.')
      } else {
        selectedFileRef.current = null
        setSelectedFile(null)
        setMarkers([])
        setRenameOpen(false)
        setRenamePath('')
        setSaveState('saved')
      }
    }
    updateSession(view.session, headers)
    updateCandidate(view.candidate)
    setTreeFiles(view.tree.files)
  }, [cancelCandidateFileOpen, projectId, updateCandidate, updateSession])

  const loadTree = useCallback(async (sessionId: string, signal?: AbortSignal) => {
    if (!sandbox) throw new Error(copy.unavailable)
    const result = await sandbox.getTree(sessionId, { signal })
    adoptCandidateRepositoryView(result.data, result.headers)
    return result.data
  }, [adoptCandidateRepositoryView, copy.unavailable, sandbox])

  const replayPendingSave = useCallback(async (sessionId: string) => {
    if (!sandbox) return
    const pending = storageValue<PendingSave>(pendingStorageKey(sessionId))
    if (!pending || pending.sessionId !== sessionId) return
    pendingSaveRef.current = pending
    setSaveState('saving')
    try {
      const result = await sandbox.putFile(
        sessionId,
        pending.path,
        pending.content,
        pending.expectedHash,
        {
          fences: pending.fences,
          mode: pending.mode,
          idempotencyKey: pending.idempotencyKey,
        },
      )
      updateSession(result.data.session, result.headers)
      pendingSaveRef.current = null
      setStorageValue(pendingStorageKey(sessionId))
      setSaveState('saved')
      return { session: result.data.session, headers: result.headers }
    } catch (cause) {
      setSaveState('error')
      throw new Error(
        `An interrupted file save still needs reconciliation: ${errorMessage(cause)}`,
        { cause },
      )
    }
  }, [pendingStorageKey, sandbox, updateSession])

  const waitUntilInteractive = useCallback(async (
    initial: SandboxSessionDto,
    initialHeaders: Headers,
    signal: AbortSignal,
  ) => {
    if (!sandbox) throw new Error(copy.unavailable)
    let current = initial
    let headers = initialHeaders
    updateSession(current, headers)
    if (current.state === 'suspended') {
      const currentFences = sandboxFences(headers, current)
      const resumed = await sandbox.resumeSession(current.id, {
        fences: currentFences,
        idempotencyKey: `resume-v${current.version}-e${current.sessionEpoch}`,
        signal,
      })
      current = resumed.data
      headers = resumed.headers
      updateSession(current, headers)
    }
    for (let attempt = 0; attempt < 60; attempt += 1) {
      if (current.state === 'ready') return { session: current, headers }
      if (current.state === 'failed' || current.state === 'terminated') {
        throw new Error(current.failureReason || `Sandbox entered ${current.state}.`)
      }
      await delay(1000, signal)
      const loaded = await sandbox.getSession(current.id, { signal })
      current = loaded.data
      headers = loaded.headers
      updateSession(current, headers)
    }
    throw new Error('Sandbox startup timed out. The durable session can be reconnected.')
  }, [copy.unavailable, sandbox, updateSession])

  const acquireLease = useCallback(async (current: SandboxSessionDto, headers: Headers) => {
    if (!sandbox) throw new Error(copy.unavailable)
    const currentFences = sandboxFences(headers, current)
    const result = await sandbox.acquireWriterLease(current.id, 900, {
      fences: currentFences,
      idempotencyKey: `lease-v${currentFences.candidateVersion}-e${currentFences.sessionEpoch}`,
    })
    updateSession(result.data.session, result.headers)
    updateCandidate(result.data.candidate)
    return result
  }, [copy.unavailable, sandbox, updateCandidate, updateSession])

  const openSandbox = useCallback(async () => {
    if (!repository || !sandbox) {
      setError(copy.unavailable)
      setPhase('error')
      return
    }
    cancelCandidateFileOpen()
    openAbortRef.current?.abort()
    const controller = new AbortController()
    openAbortRef.current = controller
    setPhase('opening')
    setError(null)
    setCandidateHeads([])
    try {
      let persisted = storageValue<PersistedSession>(sessionStorageKey)
      if (!persisted) {
        persisted = { sessionKey: randomKey('session') }
        setStorageValue(sessionStorageKey, persisted)
      }
      const discoverActiveCandidate = async () => {
        const discovered = await repository.listCandidateHeads(projectId, { signal: controller.signal })
        const selection = resolveCandidateHeadSelection(discovered.data.candidates, buildManifestId)
        if (selection.kind === 'ambiguous') {
          setCandidateHeads(selection.heads)
          setPhase('select-head')
          return { blocked: true } as const
        }
        if (selection.kind === 'none') return { blocked: false } as const
        const head = selection.head
        const pointer = {
          candidateId: head.candidate.id,
          buildManifestId: head.candidate.buildManifest.id,
          rebaseId: head.rebaseId,
        } satisfies PersistedActiveCandidate
        setStorageValue(activeCandidateStorageKey, pointer)
        return { blocked: false, pointer } as const
      }
      let active = storageValue<PersistedActiveCandidate>(activeCandidateStorageKey)
      if (!active) {
        const discovery = await discoverActiveCandidate()
        if (discovery.blocked) return
        active = discovery.pointer
      }
      let workspaceCandidate: CandidateWorkspaceDto | undefined
      let activeCandidate: CandidateWorkspaceDto | undefined

      for (let discoveryAttempt = 0; active?.candidateId && discoveryAttempt < 2; discoveryAttempt += 1) {
        try {
          if (active.rebaseId) {
            let loadedRebase = await repository.getCandidateRebase(projectId, active.rebaseId, {
              signal: controller.signal,
            })
            if (loadedRebase.data.rebase.state === 'applying') {
              const predecessor = await repository.getCandidate(
                projectId,
                loadedRebase.data.rebase.predecessorCandidateId,
                { signal: controller.signal },
              )
              loadedRebase = await repository.startCandidateRebase(
                projectId,
                predecessor.data.id,
                {
                  targetBuildManifestId: loadedRebase.data.rebase.targetBuildManifestId,
                  expectedCandidateVersion: predecessor.data.version,
                  expectedSessionEpoch: predecessor.data.sessionEpoch,
                  expectedWriterLeaseEpoch: predecessor.data.writerLeaseEpoch,
                },
                { signal: controller.signal, idempotencyKey: loadedRebase.data.rebase.operationId },
              )
            }
            setRebase(loadedRebase.data)
            updateCandidate(loadedRebase.data.candidate)
            setStorageValue(activeCandidateStorageKey, {
              candidateId: loadedRebase.data.candidate.id,
              buildManifestId: loadedRebase.data.candidate.buildManifest.id,
              rebaseId: loadedRebase.data.rebase.id,
            } satisfies PersistedActiveCandidate)
            if (loadedRebase.data.rebase.state === 'conflicted') {
              setSelectedConflictId(
                loadedRebase.data.rebase.conflicts.find((item) => item.state === 'open')?.id ?? '',
              )
              setPhase('rebase')
              return
            }
            activeCandidate = loadedRebase.data.candidate
          } else {
            const loaded = await repository.getCandidate(projectId, active.candidateId, {
              signal: controller.signal,
            })
            activeCandidate = loaded.data
          }
          break
        } catch (cause) {
          if (!(cause instanceof PlatformHttpError) || cause.status !== 404) throw cause
          setStorageValue(activeCandidateStorageKey)
          const discovery = await discoverActiveCandidate()
          if (discovery.blocked) return
          active = discovery.pointer
        }
      }

      if (activeCandidate && activeCandidate.buildManifest.id === buildManifestId &&
        (activeCandidate.conflicted || activeCandidate.stale || activeCandidate.rebaseRequired)) {
        throw new Error('The active Candidate is blocked but its local rebase lineage is missing. Reconnect using the original browser profile or clear the stale local Candidate pointer.')
      }
      if (activeCandidate && activeCandidate.buildManifest.id === buildManifestId &&
        (activeCandidate.status === 'active' || activeCandidate.status === 'frozen') &&
        !activeCandidate.stale && !activeCandidate.rebaseRequired) {
        workspaceCandidate = activeCandidate
      } else if (activeCandidate && activeCandidate.status === 'active') {
        const rebased = await repository.startCandidateRebase(
          projectId,
          activeCandidate.id,
          {
            targetBuildManifestId: buildManifestId,
            expectedCandidateVersion: activeCandidate.version,
            expectedSessionEpoch: activeCandidate.sessionEpoch,
            expectedWriterLeaseEpoch: activeCandidate.writerLeaseEpoch,
          },
          {
            signal: controller.signal,
            idempotencyKey: `rebase-${activeCandidate.id}-${buildManifestId}`,
          },
        )
        setRebase(rebased.data)
        updateCandidate(rebased.data.candidate)
        setStorageValue(activeCandidateStorageKey, {
          candidateId: rebased.data.candidate.id,
          buildManifestId: rebased.data.candidate.buildManifest.id,
          rebaseId: rebased.data.rebase.id,
        } satisfies PersistedActiveCandidate)
        if (rebased.data.rebase.state === 'conflicted') {
          setSelectedConflictId(rebased.data.rebase.conflicts.find((item) => item.state === 'open')?.id ?? '')
          setPhase('rebase')
          return
        }
        workspaceCandidate = rebased.data.candidate
      }

      if (!workspaceCandidate) {
        let bootstrapped = await repository.bootstrapCandidate(
          projectId,
          buildManifestId,
          {
            signal: controller.signal,
            idempotencyKey: deterministicOperationKey('candidate-bootstrap', JSON.stringify({
              projectId,
              buildManifestId,
              sessionKey: persisted.sessionKey,
            })),
          },
        )
        // A browser may have lost the acknowledgement after an earlier Candidate
        // was abandoned. Rotate the durable operation seed instead of replaying
        // that terminal bootstrap forever.
        if (bootstrapped.data.candidate.status === 'abandoned') {
          persisted = { sessionKey: randomKey('session') }
          setStorageValue(sessionStorageKey, persisted)
          bootstrapped = await repository.bootstrapCandidate(
            projectId,
            buildManifestId,
            {
              signal: controller.signal,
              idempotencyKey: deterministicOperationKey('candidate-bootstrap', JSON.stringify({
                projectId,
                buildManifestId,
                sessionKey: persisted.sessionKey,
              })),
            },
          )
        }
        if (bootstrapped.data.candidate.status !== 'active') {
          throw new Error('The replacement Candidate bootstrap returned a terminal workspace.')
        }
        workspaceCandidate = bootstrapped.data.candidate
        setRebase(null)
        setStorageValue(activeCandidateStorageKey, {
          candidateId: workspaceCandidate.id,
          buildManifestId: workspaceCandidate.buildManifest.id,
        } satisfies PersistedActiveCandidate)
      }
      updateCandidate(workspaceCandidate)
      let sessionResult
      if (persisted.sessionId) {
        try {
          sessionResult = await sandbox.getSession(persisted.sessionId, { signal: controller.signal })
        } catch (cause) {
          if (!(cause instanceof PlatformHttpError) || cause.status !== 404) throw cause
        }
      }
      if (sessionResult?.data.state === 'terminated') sessionResult = undefined
      if (sessionResult && sessionResult.data.candidate.id !== workspaceCandidate.id) sessionResult = undefined
      if (!sessionResult) {
        if (workspaceCandidate.status !== 'active') {
          throw new Error('The immutable Candidate is frozen. Reopen it from the browser profile that owns its exact SandboxSession.')
        }
        const sessionKey = persisted.sessionId ? randomKey('session') : persisted.sessionKey
        sessionResult = await sandbox.createSession(
          projectId,
          workspaceCandidate.id,
          { signal: controller.signal, idempotencyKey: sessionKey },
        )
        setStorageValue(sessionStorageKey, {
          candidateId: workspaceCandidate.id,
          sessionId: sessionResult.data.id,
          sessionKey,
        } satisfies PersistedSession)
      }
      const replayed = workspaceCandidate.status === 'active'
        ? await replayPendingSave(sessionResult.data.id)
        : undefined
      const interactive = await waitUntilInteractive(
        replayed?.session ?? sessionResult.data,
        replayed?.headers ?? sessionResult.headers,
        controller.signal,
      )
      const leased = interactive.session.candidate.status === 'active'
        ? await acquireLease(interactive.session, interactive.headers)
        : undefined
      const openedSession = leased?.data.session ?? interactive.session
      const opened = await loadTree(openedSession.id)
      const services = opened.session.allowedServices
      const defaultService = services.find((service) => service.profiles.includes('dev')) ?? services[0]
      setSelectedService(defaultService?.id ?? '')
      setSelectedProfile(defaultService?.profiles.includes('dev') ? 'dev' : defaultService?.profiles[0] ?? '')
      const processes = await sandbox.listProcesses(opened.session.id, { limit: 50 })
      const running = processes.data.processes.find((item) => item.state === 'running' || item.state === 'starting') ?? null
      if (running) {
        const loadedProcess = await sandbox.getProcess(opened.session.id, running.id, {
          signal: controller.signal,
        })
        setProcess(loadedProcess.data.process)
        setProcessEtag(
          loadedProcess.etag
          ?? loadedProcess.headers.get('x-sandbox-process-etag')
          ?? `"sandbox-process:${running.id}:${running.version}"`,
        )
      } else {
        setProcess(null)
        setProcessEtag('')
      }
      const terminals = await sandbox.listTerminals(opened.session.id, { limit: 20 })
      setTerminal(terminals.data.terminals.find((item) => item.state === 'running' || item.state === 'opening') ?? null)
      const portResult = await sandbox.listPorts(opened.session.id)
      updateSession(portResult.data.session, portResult.headers)
      setPorts(portResult.data.ports)
      await loadVerificationContext(opened.session.id, controller.signal)
      setPhase('ready')
    } catch (cause) {
      if (controller.signal.aborted) return
      setError(errorMessage(cause))
      setPhase('error')
    }
  }, [
    acquireLease,
    activeCandidateStorageKey,
    buildManifestId,
    cancelCandidateFileOpen,
    copy.unavailable,
    loadTree,
    loadVerificationContext,
    projectId,
    replayPendingSave,
    repository,
    sandbox,
    sessionStorageKey,
    updateCandidate,
    updateSession,
    waitUntilInteractive,
  ])

  useEffect(() => {
    sessionRef.current = session
    candidateRef.current = candidate
    fencesRef.current = fences
    selectedFileRef.current = selectedFile
  }, [candidate, fences, selectedFile, session])

  useEffect(() => () => {
    openAbortRef.current?.abort()
    cancelCandidateFileOpen()
    candidateSearchAbortRef.current?.abort()
  }, [cancelCandidateFileOpen])

  useEffect(() => {
    if (phase !== 'ready' || renameBusy || rebaseBusy || abandonBusy) {
      cancelCandidateFileOpen()
    }
  }, [abandonBusy, cancelCandidateFileOpen, phase, rebaseBusy, renameBusy])

  useEffect(() => {
    if (autoRestoreRef.current || !storageValue<PersistedSession>(sessionStorageKey)?.sessionId) return
    autoRestoreRef.current = true
    void openSandbox()
  }, [openSandbox, sessionStorageKey])


  useEffect(() => {
    const runId = verificationRun?.run.id
    const terminal = verificationRun
      ? ['passed', 'failed', 'error', 'cancelled', 'timed_out'].includes(verificationRun.run.state)
      : true
    if (!verification || phase !== 'ready' || !runId || terminal) return
    const controller = new AbortController()
    let timer: number | undefined

    const poll = async () => {
      try {
        const result = await verification.getRun(runId, { signal: controller.signal })
        await adoptVerificationRun(result.data, controller.signal)
      } catch (cause) {
        if (!controller.signal.aborted) {
          setVerificationError(`Verification status refresh failed: ${errorMessage(cause)}`)
        }
      }
      if (!controller.signal.aborted) timer = window.setTimeout(poll, 1500)
    }
    timer = window.setTimeout(poll, 1000)
    return () => {
      controller.abort()
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [
    adoptVerificationRun,
    phase,
    verification,
    verificationRun?.run.id,
    verificationRun?.run.state,
  ])

  const selectedConflict = useMemo(
    () => rebase?.rebase.conflicts.find((item) => item.id === selectedConflictId),
    [rebase, selectedConflictId],
  )

  useEffect(() => {
    if (!repository || phase !== 'rebase' || !rebase || !selectedConflict || selectedConflict.state !== 'open') return
    const controller = new AbortController()
    setConflictContent(null)
    setError(null)
    void repository.getCandidateRebaseConflictContent(
      projectId,
      rebase.rebase.id,
      selectedConflict.id,
      { signal: controller.signal },
    ).then((result) => {
      setConflictContent(result.data)
      setConflictDraft(conflictText(result.data.predecessor))
      const mode = selectedConflict.predecessorFile?.mode ?? selectedConflict.targetFile?.mode
      setConflictMode(mode === '100755' ? '100755' : '100644')
    }).catch((cause) => {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    })
    return () => controller.abort()
  }, [phase, projectId, rebase, repository, selectedConflict])

  const resolveRebaseConflict = useCallback(async (
    strategy: CandidateRebaseResolutionStrategy,
  ) => {
    if (!repository || !rebase || !selectedConflict || selectedConflict.state !== 'open') return
    cancelCandidateFileOpen()
    setRebaseBusy(true)
    setError(null)
    try {
      const resolved = await repository.resolveCandidateRebaseConflict(
        projectId,
        rebase.rebase.id,
        selectedConflict.id,
        {
          expectedConflictVersion: selectedConflict.version,
          strategy,
          ...(strategy === 'current' ? { content: conflictDraft, mode: conflictMode } : {}),
        },
        { idempotencyKey: randomKey(`resolve-${selectedConflict.id}`) },
      )
      setRebase(resolved.data)
      updateCandidate(resolved.data.candidate)
      setStorageValue(activeCandidateStorageKey, {
        candidateId: resolved.data.candidate.id,
        buildManifestId: resolved.data.candidate.buildManifest.id,
        rebaseId: resolved.data.rebase.id,
      } satisfies PersistedActiveCandidate)
      const next = resolved.data.rebase.conflicts.find((item) => item.state === 'open')
      setSelectedConflictId(next?.id ?? '')
      setConflictContent(null)
      if (resolved.data.rebase.state === 'ready') {
        setPhase('idle')
        queueMicrotask(() => void openSandbox())
      }
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setRebaseBusy(false)
    }
  }, [
    activeCandidateStorageKey,
    cancelCandidateFileOpen,
    conflictDraft,
    conflictMode,
    openSandbox,
    projectId,
    rebase,
    repository,
    selectedConflict,
    updateCandidate,
  ])

  const readFile = useCallback(async (path: string, expectedContentHash?: string) => {
    const currentSession = sessionRef.current
    const currentCandidate = candidateRef.current
    const currentFences = fencesRef.current
    if (!sandbox || !currentSession || !currentCandidate || !currentFences) return
    const openFile = selectedFileRef.current
    if (pendingSaveRef.current || (openFile && fileNeedsSave(openFile))) {
      setError(openFile?.stale
        ? openFile.staleReason ?? 'The stale draft must be reconciled before another file can replace it.'
        : 'Wait for the current Candidate file to finish autosaving before switching files.')
      return
    }
    fileOpenAbortRef.current?.abort()
    fileOpenAbortRef.current = null
    const requestGeneration = fileOpenGenerationRef.current + 1
    fileOpenGenerationRef.current = requestGeneration
    const observedFile = treeFiles.find((file) => file.path === path)
    const requestFence = createExactCandidateFileOpenFence({
      projectId,
      session: currentSession,
      candidate: currentCandidate,
      fences: currentFences,
      path,
      observedFile,
      expectedContentHash,
    })
    if (!requestFence) {
      const message = 'The exact Sandbox/Candidate head or tree-file hash is unresolved. Refresh before opening this file.'
      setError(message)
      if (expectedContentHash) {
        setCandidateSearchError(message)
        setCandidateSearchStatus('blocked')
      }
      return
    }

    const controller = new AbortController()
    fileOpenAbortRef.current = controller
    setError(null)
    try {
      const result = await sandbox.readFile(requestFence.sessionId, path, {
        signal: controller.signal,
        fence: requestFence,
      })
      const latestFence = createExactCandidateFileOpenFence({
        projectId,
        session: sessionRef.current,
        candidate: candidateRef.current,
        fences: fencesRef.current,
        path,
        expectedContentHash: requestFence.contentHash,
      })
      const decision = candidateFileReadCommitDecision({
        requestGeneration,
        currentGeneration: fileOpenGenerationRef.current,
        requestFence,
        currentFence: latestFence,
        evidence: {
          sessionEpoch: result.data.fences.sessionEpoch,
          candidateId: result.data.candidateId,
          candidateVersion: result.data.fences.candidateVersion,
          journalSequence: result.data.journalSequence,
          writerLeaseEpoch: result.data.fences.writerLeaseEpoch,
          treeHash: result.data.fences.treeHash,
          contentHash: result.data.contentHash,
        },
      })
      if (decision === 'superseded') return
      if (decision !== 'commit') {
        const message = decision === 'head_changed'
          ? 'The Sandbox/Candidate head changed while this file was opening. Refresh and retry.'
          : 'The file response did not prove the exact session epoch, Candidate head, or tree-file content hash.'
        setError(message)
        if (expectedContentHash) {
          setCandidateSearchError(message)
          setCandidateSearchStatus('blocked')
          setCandidateSearchResult(null)
        }
        return
      }
      const content = decodeCandidateFile(result.data.value)
      const mode = result.data.mode === '100755' || observedFile?.mode === '100755' ? '100755' : '100644'
      const nextOpenFile: OpenFile = content.binary
        ? {
            path,
            mode,
            contentHash: result.data.contentHash,
            savedContent: '',
            draft: '',
            binary: true,
            bytes: content.bytes,
            openFence: requestFence,
            stale: false,
          }
        : {
            path,
            mode,
            contentHash: result.data.contentHash,
            savedContent: content.text,
            draft: content.text,
            binary: false,
            openFence: requestFence,
            stale: false,
          }
      selectedFileRef.current = nextOpenFile
      setSelectedFile(nextOpenFile)
      setRenameOpen(false)
      setRenamePath('')
      setSaveState('saved')
      setMarkers([])
      if (expectedContentHash) setCandidateSearchError(null)
    } catch (cause) {
      if (controller.signal.aborted || requestGeneration !== fileOpenGenerationRef.current) return
      setError(errorMessage(cause))
    } finally {
      if (fileOpenAbortRef.current === controller) fileOpenAbortRef.current = null
    }
  }, [projectId, sandbox, treeFiles])

  useEffect(() => {
    if (phase !== 'ready' || selectedFile || treeFiles.length === 0) return
    void readFile(treeFiles[0]!.path)
  }, [phase, readFile, selectedFile, treeFiles])

  const refreshCandidateSearchHead = useCallback(async (signal?: AbortSignal) => {
    const currentSession = sessionRef.current
    const currentCandidate = candidateRef.current
    if (!sandbox || !currentSession || !currentCandidate) {
      throw new Error('The selected Candidate head is unavailable.')
    }
    // Refresh only the selected Sandbox/Candidate projection. The open editor,
    // its dirty draft, and governed Blueprint/document state stay untouched.
    const result = await sandbox.getTree(currentSession.id, { signal })
    const view = result.data
    const refreshedFences = sandboxFences(result.headers, view.session)
    if (
      view.session.id !== currentSession.id
      || view.session.projectId !== projectId
      || view.candidate.id !== currentCandidate.id
      || view.candidate.projectId !== projectId
      || view.session.candidate.id !== view.candidate.id
      || view.session.candidate.version !== view.candidate.version
      || view.session.candidate.journalSequence !== view.candidate.journalSequence
      || view.session.sessionEpoch !== view.candidate.sessionEpoch
      || view.session.candidate.sessionEpoch !== view.candidate.sessionEpoch
      || view.session.candidate.writerLeaseEpoch !== view.candidate.writerLeaseEpoch
      || view.session.candidate.treeHash !== view.candidate.treeHash
      || view.tree.treeHash !== view.candidate.treeHash
      || view.candidate.currentTree.treeHash !== view.candidate.treeHash
      || refreshedFences.sessionEpoch !== view.session.sessionEpoch
      || refreshedFences.candidateVersion !== view.candidate.version
      || refreshedFences.writerLeaseEpoch !== view.candidate.writerLeaseEpoch
      || refreshedFences.treeHash !== view.candidate.treeHash
    ) {
      throw new Error('The Candidate head refresh returned a different or incomplete exact identity.')
    }
    adoptCandidateRepositoryView(view, result.headers)
    return view.candidate
  }, [adoptCandidateRepositoryView, projectId, sandbox])

  const requestCandidateSearchHeadRefresh = useCallback(() => {
    candidateSearchAbortRef.current?.abort()
    candidateSearchRequestRef.current += 1
    const controller = new AbortController()
    candidateSearchAbortRef.current = controller
    setCandidateSearchResult(null)
    setCandidateSearchError(null)
    setCandidateSearchHeadRefreshAllowed(false)
    setCandidateSearchStatus('refreshing')
    void refreshCandidateSearchHead(controller.signal).then((next) => {
      if (controller.signal.aborted) return
      setCandidateSearchError(`Exact Candidate head refreshed to C${next.version}; editor content was preserved.`)
      setCandidateSearchStatus(candidateSearchQuery ? 'waiting' : 'idle')
      setCandidateSearchRefreshToken((value) => value + 1)
    }).catch((cause) => {
      if (controller.signal.aborted) return
      setCandidateSearchError(`Exact Candidate head refresh failed: ${errorMessage(cause)}`)
      setCandidateSearchHeadRefreshAllowed(true)
      setCandidateSearchStatus('blocked')
    })
  }, [candidateSearchQuery, refreshCandidateSearchHead])

  useEffect(() => {
    candidateSearchAbortRef.current?.abort()
    const requestSequence = candidateSearchRequestRef.current + 1
    candidateSearchRequestRef.current = requestSequence
    setCandidateSearchResult(null)
    setCandidateSearchHeadRefreshAllowed(false)

    const retryIdentity = candidateSearchRetryIdentity({
      projectId,
      candidateId: candidate?.id ?? '',
      generation: candidate?.version ?? 0,
      rootHash: candidate?.treeHash ?? '',
      query: candidateSearchQuery,
      caseSensitive: candidateSearchCaseSensitive,
      include: candidateSearchInclude,
    })
    if (
      candidateSearchAutomaticRetryIdentityRef.current
      && candidateSearchAutomaticRetryIdentityRef.current !== retryIdentity
    ) {
      candidateSearchAutomaticRetryIdentityRef.current = null
    }

    if (!candidateSearchQuery) {
      setCandidateSearchError(null)
      setCandidateSearchStatus('idle')
      return
    }

    const queryError = candidateSearchQueryError(candidateSearchQuery, candidateSearchCaseSensitive)
    const parsedGlobs = candidateSearchGlobs(candidateSearchInclude)
    if (queryError || parsedGlobs.error) {
      setCandidateSearchError(queryError ?? parsedGlobs.error)
      setCandidateSearchStatus('blocked')
      return
    }
    const exactCandidate = candidate
    if (!repository || !exactCandidate || !candidateSearchHeadResolved) {
      setCandidateSearchError('Search is paused until one exact Candidate head is resolved.')
      setCandidateSearchStatus('blocked')
      return
    }
    if (candidateHeadMutationInFlight) {
      setCandidateSearchError('Search is paused while the Candidate head is saving or rebasing.')
      setCandidateSearchStatus('blocked')
      return
    }

    const controller = new AbortController()
    candidateSearchAbortRef.current = controller
    setCandidateSearchError(null)
    setCandidateSearchStatus('waiting')
    const timer = window.setTimeout(() => {
      if (controller.signal.aborted || requestSequence !== candidateSearchRequestRef.current) return
      setCandidateSearchStatus('searching')
      void repository.searchCandidate(
        projectId,
        exactCandidate.id,
        {
          expectedHeadGeneration: exactCandidate.version,
          expectedRootHash: exactCandidate.treeHash,
          query: candidateSearchQuery,
          caseSensitive: candidateSearchCaseSensitive,
          includeGlobs: parsedGlobs.globs,
          maxMatches: 100,
        },
        { signal: controller.signal },
      ).then((result) => {
        if (controller.signal.aborted || requestSequence !== candidateSearchRequestRef.current) return
        if (!candidateSearchResultMatchesCandidate(result.data, candidateRef.current)) {
          setCandidateSearchResult(null)
          setCandidateSearchError('The selected Candidate changed before these results could be adopted. Refresh the exact head.')
          setCandidateSearchHeadRefreshAllowed(true)
          setCandidateSearchStatus('blocked')
          return
        }
        setCandidateSearchResult(result.data)
        setCandidateSearchError(null)
        setCandidateSearchStatus('idle')
      }).catch(async (cause) => {
        if (controller.signal.aborted || requestSequence !== candidateSearchRequestRef.current) return
        const failure = resolveCandidateSearchFailure(
          cause,
          candidateSearchAutomaticRetryIdentityRef.current === retryIdentity,
        )
        if (failure.kind === 'refresh-head') {
          setCandidateSearchResult(null)
          setCandidateSearchStatus('refreshing')
          try {
            const refreshed = await refreshCandidateSearchHead(controller.signal)
            if (controller.signal.aborted || requestSequence !== candidateSearchRequestRef.current) return
            setCandidateSearchError(`Candidate head changed and was refreshed to C${refreshed.version}; stale results were cleared.`)
            setCandidateSearchStatus('waiting')
          } catch (refreshCause) {
            if (controller.signal.aborted || requestSequence !== candidateSearchRequestRef.current) return
            setCandidateSearchError(`Candidate search head changed, and exact refresh failed: ${errorMessage(refreshCause)}`)
            setCandidateSearchHeadRefreshAllowed(true)
            setCandidateSearchStatus('blocked')
          }
          return
        }
        if (failure.kind === 'retry-once') {
          candidateSearchAutomaticRetryIdentityRef.current = retryIdentity
          setCandidateSearchError(failure.message)
          setCandidateSearchStatus('waiting')
          try {
            await delay(failure.retryAfterSeconds * 1_000, controller.signal)
            if (controller.signal.aborted || requestSequence !== candidateSearchRequestRef.current) return
            setCandidateSearchRetryToken((value) => value + 1)
          } catch (retryCause) {
            if (!(retryCause instanceof DOMException && retryCause.name === 'AbortError')) throw retryCause
          }
          return
        }
        if (failure.kind === 'blocked') {
          setCandidateSearchError(failure.message)
          setCandidateSearchStatus('blocked')
          return
        }
        setCandidateSearchError(`Exact Candidate search failed: ${errorMessage(cause)}`)
        setCandidateSearchStatus('blocked')
      })
    }, 350)

    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [
    candidate?.id,
    candidate?.projectId,
    candidate?.treeHash,
    candidate?.version,
    candidateHeadMutationInFlight,
    candidateSearchCaseSensitive,
    candidateSearchHeadResolved,
    candidateSearchInclude,
    candidateSearchQuery,
    candidateSearchRefreshToken,
    candidateSearchRetryToken,
    projectId,
    refreshCandidateSearchHead,
    repository,
  ])

  const openCandidateSearchMatch = useCallback((match: RepositoryCandidateSearchMatchDto) => {
    const result = candidateSearchResult
    if (!result || !candidateSearchResultMatchesCandidate(result, candidateRef.current)) {
      setCandidateSearchError('This match is stale. Refresh the exact Candidate head before opening it.')
      setCandidateSearchHeadRefreshAllowed(true)
      setCandidateSearchStatus('blocked')
      return
    }
    if (candidateHeadMutationInFlight) {
      setCandidateSearchError('Wait for the Candidate head mutation to finish before opening a search result.')
      setCandidateSearchStatus('blocked')
      return
    }
    void readFile(match.path, match.contentHash)
  }, [candidateHeadMutationInFlight, candidateSearchResult, readFile])

  const performSave = useCallback(async (pending: PendingSave) => {
    if (!sandbox) return
    pendingSaveRef.current = pending
    setStorageValue(pendingStorageKey(pending.sessionId), pending)
    setSaveState('saving')
    setError(null)
    try {
      const result = await sandbox.putFile(
        pending.sessionId,
        pending.path,
        pending.content,
        pending.expectedHash,
        {
          fences: pending.fences,
          mode: pending.mode,
          idempotencyKey: pending.idempotencyKey,
        },
      )
      const contentHash = result.data.mutation.entry.operation.contentHash
      if (!contentHash) throw new Error('The file mutation response omitted its exact content hash.')
      const byteSize = new TextEncoder().encode(pending.content).byteLength
      const nextTreeFile = { path: pending.path, mode: pending.mode, contentHash, byteSize }
      const currentOpenFile = selectedFileRef.current
      const currentCandidate = candidateRef.current
      if (!currentCandidate || result.data.session.candidate.id !== currentCandidate.id) {
        throw new Error('The file mutation response did not preserve the exact Candidate identity.')
      }
      const nextTreeFiles = upsertCandidateTreeFile(currentCandidate.currentTree.files, nextTreeFile)
      const nextCandidate: CandidateWorkspaceDto = {
        ...currentCandidate,
        version: result.data.session.candidate.version,
        journalSequence: result.data.session.candidate.journalSequence,
        sessionEpoch: result.data.session.candidate.sessionEpoch,
        treeHash: result.data.session.candidate.treeHash,
        currentTree: {
          ...currentCandidate.currentTree,
          treeHash: result.data.session.candidate.treeHash,
          files: nextTreeFiles,
        },
        dirty: result.data.session.candidate.dirty,
        writerLeaseEpoch: result.data.session.candidate.writerLeaseEpoch,
        updatedAt: result.data.session.candidate.updatedAt,
      }
      const nextFences = sandboxFences(result.headers, result.data.session)
      const nextOpenFence = createExactCandidateFileOpenFence({
        projectId,
        session: result.data.session,
        candidate: nextCandidate,
        fences: nextFences,
        path: pending.path,
        observedFile: nextTreeFile,
        expectedContentHash: contentHash,
      })
      if (!nextOpenFence) {
        throw new Error('The file mutation response did not prove the new exact Candidate file head.')
      }
      updateSession(result.data.session, result.headers)
      updateCandidate(nextCandidate)
      setTreeFiles(nextTreeFiles)
      if (currentOpenFile?.path === pending.path) {
        const nextOpenFile: OpenFile = {
          ...currentOpenFile,
          contentHash,
          savedContent: pending.content,
          openFence: nextOpenFence,
          stale: false,
          staleReason: undefined,
        }
        selectedFileRef.current = nextOpenFile
        setSelectedFile(nextOpenFile)
      }
      pendingSaveRef.current = null
      setStorageValue(pendingStorageKey(pending.sessionId))
      setSaveState('saved')
    } catch (cause) {
      setSaveState('error')
      setError(`Autosave needs reconciliation: ${errorMessage(cause)}`)
      throw cause
    }
  }, [pendingStorageKey, projectId, sandbox, updateCandidate, updateSession])

  const queueSave = useCallback((retry = false) => {
    const current = selectedFileRef.current
    const currentSession = sessionRef.current
    const currentFences = fencesRef.current
    if (!current || !currentSession || !currentFences || !fileNeedsSave(current)) return Promise.resolve()
    if (current.stale) {
      return Promise.reject(new Error(current.staleReason ?? 'This draft is stale and cannot be saved onto a different Candidate head.'))
    }
    if (currentSession.candidate.status !== 'active' || !currentSession.allowedActions.includes('edit')) {
      return Promise.reject(new Error('The Candidate is immutable and no longer accepts file saves.'))
    }
    const existing = pendingSaveRef.current
    if (existing && !retry) return Promise.resolve()
    const pending = existing ?? {
      sessionId: currentSession.id,
      path: current.path,
      content: current.draft,
      expectedHash: current.contentHash,
      mode: current.mode,
      idempotencyKey: randomKey(`file-v${currentFences.candidateVersion}`),
      fences: currentFences,
    }
    return enqueueMutation(() => performSave(pending))
  }, [enqueueMutation, performSave])

  const beginAgentMutation = useCallback(() => {
    if (agentMutationBusyRef.current) throw new Error('Another exact Agent workspace operation is still running.')
    agentMutationBusyRef.current = true
    setAgentMutationBusy(true)
  }, [])

  const finishAgentMutation = useCallback(() => {
    agentMutationBusyRef.current = false
    setAgentMutationBusy(false)
  }, [])

  const requireExactAgentWorkspace = useCallback(() => {
    const currentSession = sessionRef.current
    const currentCandidate = candidateRef.current
    const currentFences = fencesRef.current
    const openFile = selectedFileRef.current
    if (!currentSession || !currentCandidate || !currentFences || currentSession.state !== 'ready') {
      throw new Error('The exact ready SandboxSession is required for this Agent operation.')
    }
    if (currentCandidate.status !== 'active' ||
      !currentSession.allowedActions.includes('edit') ||
      !currentSession.allowedActions.includes('agent')) {
      throw new Error('The server does not currently allow Agent changes to this Candidate.')
    }
    if (pendingSaveRef.current || (openFile && fileNeedsSave(openFile))) {
      throw new Error('The current Candidate must finish autosaving before this Agent operation.')
    }
    if (currentCandidate.id !== currentSession.candidate.id ||
      currentCandidate.version !== currentFences.candidateVersion ||
      currentCandidate.writerLeaseEpoch !== currentFences.writerLeaseEpoch ||
      currentSession.sessionEpoch !== currentFences.sessionEpoch) {
      throw new Error('The local Candidate projection is stale; reconnect before this Agent operation.')
    }
    return { currentSession, currentCandidate, currentFences }
  }, [])

  const createAgentAttempt = useCallback(async (input: AgentCreateAttemptInput) => {
    if (!agent) throw new Error('The authenticated platform client does not expose Agent APIs.')
    beginAgentMutation()
    try {
      await queueSave()
      return await enqueueMutation(async () => {
        const { currentSession, currentCandidate } = requireExactAgentWorkspace()
        const idempotencyKey = deterministicOperationKey('agent-create', JSON.stringify({
          sessionId: currentSession.id,
          candidateVersion: currentCandidate.version,
          treeHash: currentCandidate.treeHash,
          input,
        }))
        return agent.createAttempt(currentSession.id, input, { idempotencyKey })
      })
    } finally {
      finishAgentMutation()
    }
  }, [agent, beginAgentMutation, enqueueMutation, finishAgentMutation, queueSave, requireExactAgentWorkspace])

  const mergeAgentPatch = useCallback(async (attemptId: string, attemptEtag: string) => {
    if (!agent) throw new Error('The authenticated platform client does not expose Agent APIs.')
    beginAgentMutation()
    try {
      await queueSave()
      return await enqueueMutation(async () => {
        const { currentSession, currentCandidate } = requireExactAgentWorkspace()
        const expected = {
          expectedSessionVersion: currentSession.version,
          expectedSessionEpoch: currentSession.sessionEpoch,
          expectedCandidateVersion: currentCandidate.version,
          expectedWriterLeaseEpoch: currentCandidate.writerLeaseEpoch,
        }
        const result = await agent.mergePatch(attemptId, expected, {
          ifMatch: attemptEtag,
          idempotencyKey: deterministicOperationKey('agent-merge', JSON.stringify({ attemptId, ...expected })),
        })
        if (result.data.application) {
          selectedFileRef.current = null
          setSelectedFile(null)
          setMarkers([])
          setSaveState('saved')
          try {
            await loadTree(currentSession.id)
          } catch (cause) {
            setError(`Agent merge was applied, but the Candidate tree refresh failed: ${errorMessage(cause)}`)
          }
        }
        return result
      })
    } finally {
      finishAgentMutation()
    }
  }, [agent, beginAgentMutation, enqueueMutation, finishAgentMutation, loadTree, queueSave, requireExactAgentWorkspace])

  const undoAgentPatch = useCallback(async (mergeId: string, mergeEtag: string) => {
    if (!agent) throw new Error('The authenticated platform client does not expose Agent APIs.')
    beginAgentMutation()
    try {
      await queueSave()
      return await enqueueMutation(async () => {
        const { currentSession, currentCandidate } = requireExactAgentWorkspace()
        const expected = {
          expectedSessionVersion: currentSession.version,
          expectedSessionEpoch: currentSession.sessionEpoch,
          expectedCandidateVersion: currentCandidate.version,
          expectedWriterLeaseEpoch: currentCandidate.writerLeaseEpoch,
        }
        const result = await agent.undoPatch(mergeId, expected, {
          ifMatch: mergeEtag,
          idempotencyKey: deterministicOperationKey('agent-undo', JSON.stringify({ mergeId, ...expected })),
        })
        if (result.data.application) {
          selectedFileRef.current = null
          setSelectedFile(null)
          setMarkers([])
          setSaveState('saved')
          try {
            await loadTree(currentSession.id)
          } catch (cause) {
            setError(`Agent merge was undone, but the Candidate tree refresh failed: ${errorMessage(cause)}`)
          }
        }
        return result
      })
    } finally {
      finishAgentMutation()
    }
  }, [agent, beginAgentMutation, enqueueMutation, finishAgentMutation, loadTree, queueSave, requireExactAgentWorkspace])

  const createCheckpoint = useCallback(() => {
    if (!sandbox || checkpointBusy) return
    setCheckpointBusy(true)
    setError(null)
    void (async () => {
      try {
        await queueSave()
        await enqueueMutation(async () => {
          const currentSession = sessionRef.current
          const currentCandidate = candidateRef.current
          const currentFences = fencesRef.current
          if (!currentSession || !currentCandidate || !currentFences || pendingSaveRef.current) {
            throw new Error('The exact Candidate must finish autosaving before checkpointing.')
          }
          if (!currentSession.allowedActions.includes('checkpoint')) {
            const reason = currentSession.blockingReasons
              .find((item) => item.actions.includes('checkpoint'))?.detail
            throw new Error(reason || 'The server does not currently allow a Candidate checkpoint.')
          }
          const result = await sandbox.checkpoint(
            currentSession.id,
            {
              checkpointId: newUUID(),
              reason: 'User checkpoint from Browser IDE',
            },
            {
              fences: currentFences,
              idempotencyKey: `checkpoint-v${currentCandidate.version}-${currentCandidate.treeHash.slice(7, 23)}`,
            },
          )
          updateSession(result.data.session, result.headers)
        })
      } catch (cause) {
        setError(`Candidate checkpoint failed: ${errorMessage(cause)}`)
      } finally {
        setCheckpointBusy(false)
      }
    })()
  }, [checkpointBusy, enqueueMutation, queueSave, sandbox, updateSession])


  const createVerificationRun = useCallback(() => {
    if (!verification || verificationBusy) return
    setVerificationBusy(true)
    setVerificationError(null)
    void (async () => {
      try {
        await queueSave()
        await enqueueMutation(async () => {
          const currentSession = sessionRef.current
          const currentCandidate = candidateRef.current
          const currentFences = fencesRef.current
          const profile = verificationProfiles.find(
            (entry) => verificationProfileKey(entry) === selectedVerificationProfileKey,
          )
          if (!currentSession || !currentCandidate || !currentFences || !profile) {
            throw new Error('Select an active VerificationProfile for the exact Candidate.')
          }
          if (!currentSession.allowedActions.includes('verify')) {
            const reason = currentSession.blockingReasons
              .find((item) => item.actions.includes('verify'))?.detail
            throw new Error(reason || 'The server does not currently allow Candidate verification.')
          }
          const checkpoint = currentSession.latestCheckpoint
          if (!checkpoint || !hasExactCandidateCheckpoint(currentSession, currentCandidate)) {
            throw new Error('Create an exact checkpoint for the current Candidate tree before verification.')
          }
          const input = {
            candidateId: currentCandidate.id,
            checkpointId: checkpoint.id,
            expectedSessionVersion: currentSession.version,
            expectedSessionEpoch: currentSession.sessionEpoch,
            expectedCandidateVersion: currentCandidate.version,
            expectedWriterLeaseEpoch: currentCandidate.writerLeaseEpoch,
            verificationProfile: profile.verificationProfile,
            reason: 'Verify exact Candidate checkpoint before Proposal review',
          }
          const result = await verification.createRun(
            currentSession.id,
            input,
            {
              idempotencyKey: deterministicOperationKey(
                'candidate-verification',
                JSON.stringify({
                  sessionId: currentSession.id,
                  checkpointId: checkpoint.id,
                  sessionVersion: currentSession.version,
                  candidateVersion: currentCandidate.version,
                  profile: profile.verificationProfile,
                }),
              ),
            },
          )
          await adoptVerificationRun(result.data)
        })
      } catch (cause) {
        setVerificationError(`Candidate verification could not start: ${errorMessage(cause)}`)
      } finally {
        setVerificationBusy(false)
      }
    })()
  }, [
    adoptVerificationRun,
    enqueueMutation,
    queueSave,
    selectedVerificationProfileKey,
    verification,
    verificationBusy,
    verificationProfiles,
  ])

  const cancelVerificationRun = useCallback(() => {
    if (!verification || verificationBusy || !verificationRun?.allowedActions.includes('cancel')) return
    setVerificationBusy(true)
    setVerificationError(null)
    void verification.cancelRun(
      verificationRun.run.id,
      {
        expectedVersion: verificationRun.run.version,
        expectedFenceEpoch: verificationRun.run.fenceEpoch,
        reason: 'User cancelled Candidate verification.',
      },
      {
        idempotencyKey: deterministicOperationKey(
          'cancel-verification',
          `${verificationRun.run.id}:${verificationRun.run.version}:${verificationRun.run.fenceEpoch}`,
        ),
      },
    ).then((result) => adoptVerificationRun(result.data)).catch((cause) => {
      setVerificationError(`Candidate verification could not be cancelled: ${errorMessage(cause)}`)
    }).finally(() => setVerificationBusy(false))
  }, [adoptVerificationRun, verification, verificationBusy, verificationRun])

  const retryVerificationRun = useCallback(() => {
    const reason = verificationRetryReason.trim()
    if (!verification || verificationBusy || !verificationRun?.allowedActions.includes('retry') || !reason) return
    setVerificationBusy(true)
    setVerificationError(null)
    void verification.retryRun(
      verificationRun.run.id,
      reason,
      {
        idempotencyKey: deterministicOperationKey(
          'retry-verification',
          `${verificationRun.run.id}:${reason}`,
        ),
      },
    ).then(async (result) => {
      setVerificationRetryReason('')
      await adoptVerificationRun(result.data)
    }).catch((cause) => {
      setVerificationError(`Candidate verification could not be retried: ${errorMessage(cause)}`)
    }).finally(() => setVerificationBusy(false))
  }, [
    adoptVerificationRun,
    verification,
    verificationBusy,
    verificationRetryReason,
    verificationRun,
  ])

  const freezeCandidate = useCallback(() => {
    if (!sandbox || freezeBusy) return
    setFreezeBusy(true)
    setFreezeNotice(null)
    setError(null)
    void (async () => {
      try {
        await queueSave()
        await enqueueMutation(async () => {
          const currentSession = sessionRef.current
          const currentCandidate = candidateRef.current
          const currentFences = fencesRef.current
          const openFile = selectedFileRef.current
          if (
            !currentSession
            || !currentCandidate
            || !currentFences
            || pendingSaveRef.current
            || (openFile && fileNeedsSave(openFile))
          ) {
            throw new Error('The exact Candidate must finish autosaving before it can be frozen.')
          }
          if (currentCandidate.status !== 'active' || !currentSession.allowedActions.includes('freeze')) {
            const reason = currentSession.blockingReasons
              .find((item) => item.actions.includes('freeze'))?.detail
            throw new Error(reason || 'The server does not currently allow this Candidate to be frozen.')
          }
          const checkpoint = currentSession.latestCheckpoint
          if (
            !checkpoint
            || checkpoint.candidateId !== currentCandidate.id
            || checkpoint.candidateVersion !== currentCandidate.version
            || checkpoint.journalSequence !== currentCandidate.journalSequence
            || checkpoint.sessionEpoch !== currentSession.sessionEpoch
            || checkpoint.writerLeaseEpoch !== currentCandidate.writerLeaseEpoch
            || checkpoint.treeHash !== currentCandidate.treeHash
          ) {
            throw new Error('Create an exact checkpoint for the current Candidate tree before freezing it.')
          }

          const receiptReference = verificationRun?.receipt
          if (
            !verificationRun
            || !verificationRun.allowedActions.includes('freeze')
            || verificationRun.receiptDecision !== 'passed'
            || !receiptReference
            || !exactVerificationSubject(verificationRun, currentSession, currentCandidate)
            || !verificationReceipt
            || verificationReceipt.id !== receiptReference.id
            || verificationReceipt.payloadHash !== receiptReference.contentHash
            || verificationReceipt.runId !== verificationRun.run.id
            || verificationReceipt.decision !== 'passed'
          ) {
            throw new Error('A fresh passing VerificationReceipt for this exact checkpoint is required before creating a Proposal.')
          }
          const reason = 'Freeze exact Candidate into implementation Proposal'
          const result = await sandbox.freezeCandidate(
            currentSession.id,
            {
              checkpointId: checkpoint.id,
              verificationReceiptId: receiptReference.id,
              verificationReceiptHash: receiptReference.contentHash,
              reason,
            },
            {
              fences: currentFences,
              idempotencyKey: deterministicOperationKey('candidate-freeze', JSON.stringify({
                sessionId: currentSession.id,
                candidateId: currentCandidate.id,
                checkpointId: checkpoint.id,
                candidateVersion: currentCandidate.version,
                journalSequence: currentCandidate.journalSequence,
                treeHash: currentCandidate.treeHash,
                verificationReceiptId: receiptReference.id,
                verificationReceiptHash: receiptReference.contentHash,
              })),
            },
          )
          updateSession(result.data.session, result.headers)
          updateCandidate(result.data.candidate)
          setSaveState('saved')
          setShowAgent(false)
          setShowTerminal(false)
          if (!flow.adoptImplementationProposal(result.data.proposal)) {
            throw new Error(`Candidate was frozen as Proposal ${result.data.proposal.id}, but it did not match the active workbench.`)
          }
          setFreezeNotice(
            `Candidate frozen as Proposal ${result.data.proposal.id.slice(0, 12)}. Review every file operation, accept all exact changes, then apply it to create the immutable WorkspaceRevision.`,
          )
        })
      } catch (cause) {
        setError(`Candidate freeze failed: ${errorMessage(cause)}`)
      } finally {
        setFreezeBusy(false)
      }
    })()
  }, [
    enqueueMutation,
    flow,
    freezeBusy,
    queueSave,
    sandbox,
    updateCandidate,
    updateSession,
    verificationReceipt,
    verificationRun,
  ])

  useEffect(() => {
    if (phase !== 'ready' || !canMutateCandidate || !selectedFile ||
      selectedFile.stale || agentMutationBusy || !fileNeedsSave(selectedFile) ||
      saveState === 'saving' || saveState === 'error' || saveState === 'stale') return
    setSaveState('dirty')
    const timer = window.setTimeout(() => void queueSave(), 900)
    return () => window.clearTimeout(timer)
  }, [agentMutationBusy, canMutateCandidate, phase, queueSave, saveState, selectedFile])

  useEffect(() => {
    const expiresAt = candidate?.lease?.expiresAt
    if (phase !== 'ready' || candidate?.status !== 'active' || !expiresAt || !sandbox) return
    const delayMs = Math.max(10_000, Date.parse(expiresAt) - Date.now() - 60_000)
    const timer = window.setTimeout(() => {
      void enqueueMutation(async () => {
        const current = sessionRef.current
        const currentFences = fencesRef.current
        if (!current || !currentFences) return
        try {
          const renewed = await sandbox.acquireWriterLease(current.id, 900, {
            fences: currentFences,
            idempotencyKey: `lease-v${currentFences.candidateVersion}-e${currentFences.sessionEpoch}`,
          })
          updateSession(renewed.data.session, renewed.headers)
          updateCandidate(renewed.data.candidate)
        } catch (cause) {
          setError(`Writer lease renewal failed: ${errorMessage(cause)}`)
        }
      })
    }, delayMs)
    return () => window.clearTimeout(timer)
  }, [candidate?.lease?.expiresAt, candidate?.status, enqueueMutation, phase, sandbox, updateCandidate, updateSession])

  useEffect(() => {
    const now = Date.now()
    const expiresAt = candidate?.lease?.expiresAt
    if (!expiresAt) return
    const expiresAtMs = Date.parse(expiresAt)
    if (!Number.isFinite(expiresAtMs) || expiresAtMs <= now) return
    const timer = window.setTimeout(
      () => setLSPAuthorityNow(Date.now()),
      Math.min(expiresAtMs - now + 1, 2_147_483_647),
    )
    return () => window.clearTimeout(timer)
  }, [candidate?.lease?.expiresAt, lspAuthorityNow])

  const createFile = useCallback(() => {
    if (agentMutationBusyRef.current || !canMutateCandidate) return
    const path = newPath.trim().replace(/^\/+/, '')
    if (!path || treeFiles.some((item) => item.path.toLowerCase() === path.toLowerCase())) return
    cancelCandidateFileOpen()
    const nextOpenFile: OpenFile = {
      path,
      mode: '100644',
      contentHash: 'absent',
      savedContent: '',
      draft: '',
      binary: false,
    }
    selectedFileRef.current = nextOpenFile
    setSelectedFile(nextOpenFile)
    setNewPath('')
    setRenameOpen(false)
    setRenamePath('')
    setSaveState('dirty')
    setMarkers([])
  }, [cancelCandidateFileOpen, canMutateCandidate, newPath, treeFiles])

  const renameSelectedFile = useCallback(() => {
    const targetPath = renamePath.trim().replace(/^\/+/, '')
    const selected = selectedFileRef.current
    const selectedPath = selected?.path
    if (selected?.stale) {
      setError(selected.staleReason ?? 'A stale draft cannot be renamed.')
      return
    }
    if (!targetPath || !selectedPath || targetPath === selectedPath) {
      setRenameOpen(false)
      setRenamePath('')
      return
    }
    if (targetPath.endsWith('/') || targetPath.split('/').some((segment) => !segment || segment === '.' || segment === '..')) {
      setError('Choose a normalized repository file path without empty, dot, or parent segments.')
      return
    }
    if (treeFiles.some((item) => item.path !== selectedPath && item.path.toLowerCase() === targetPath.toLowerCase())) {
      setError(`Candidate file ${targetPath} already exists.`)
      return
    }
    if (!sandbox || renameBusy || agentMutationBusyRef.current || !canMutateCandidate) return
    cancelCandidateFileOpen()
    setRenameBusy(true)
    setError(null)
    void (async () => {
      try {
        await queueSave()
        await enqueueMutation(async () => {
          const currentFile = selectedFileRef.current
          const currentSession = sessionRef.current
          const currentFences = fencesRef.current
          if (!currentFile || currentFile.path !== selectedPath || !currentSession || !currentFences || currentFile.contentHash === 'absent') {
            throw new Error('The exact saved Candidate file is required before it can be renamed.')
          }
          if (!currentSession.allowedActions.includes('edit')) {
            throw new Error('The server does not currently allow Candidate file changes.')
          }
          const result = await sandbox.renameFile(
            currentSession.id,
            currentFile.path,
            targetPath,
            currentFile.contentHash,
            {
              fences: currentFences,
              idempotencyKey: deterministicOperationKey('file-rename', JSON.stringify({
                sessionId: currentSession.id,
                fromPath: currentFile.path,
                targetPath,
                contentHash: currentFile.contentHash,
                candidateVersion: currentFences.candidateVersion,
              })),
            },
          )
          updateSession(result.data.session, result.headers)
          selectedFileRef.current = { ...currentFile, path: targetPath }
          setSelectedFile((file) => file?.path === currentFile.path ? { ...file, path: targetPath } : file)
          await loadTree(currentSession.id)
          setSaveState('saved')
          setRenameOpen(false)
          setRenamePath('')
        })
      } catch (cause) {
        setError(`Candidate file rename failed: ${errorMessage(cause)}`)
      } finally {
        setRenameBusy(false)
      }
    })()
  }, [cancelCandidateFileOpen, canMutateCandidate, enqueueMutation, loadTree, queueSave, renameBusy, renamePath, sandbox, treeFiles, updateSession])

  const deleteSelectedFile = useCallback(() => {
    const selected = selectedFileRef.current
    const selectedPath = selected?.path
    if (selected?.stale) {
      setError(selected.staleReason ?? 'A stale draft cannot be deleted from a different Candidate head.')
      return
    }
    if (agentMutationBusyRef.current || !canMutateCandidate || !sandbox || !selectedPath) return
    cancelCandidateFileOpen()
    void (async () => {
      setSaveState('saving')
      try {
        await queueSave()
        await enqueueMutation(async () => {
          const current = selectedFileRef.current
          const currentSession = sessionRef.current
          const currentFences = fencesRef.current
          if (!current || current.path !== selectedPath || !currentSession || !currentFences || current.contentHash === 'absent') {
            throw new Error('The exact saved Candidate file is required before it can be deleted.')
          }
          const result = await sandbox.deleteFile(
            currentSession.id,
            current.path,
            current.contentHash,
            { fences: currentFences, idempotencyKey: randomKey(`delete-v${currentFences.candidateVersion}`) },
          )
          updateSession(result.data.session, result.headers)
          selectedFileRef.current = null
          setSelectedFile(null)
          setRenameOpen(false)
          setRenamePath('')
          await loadTree(currentSession.id)
          setSaveState('saved')
        })
      } catch (cause) {
        setSaveState('error')
        setError(errorMessage(cause))
      }
    })()
  }, [cancelCandidateFileOpen, canMutateCandidate, enqueueMutation, loadTree, queueSave, sandbox, updateSession])

  const suspendSandbox = useCallback(() => {
    if (!sandbox || lifecycleBusy) return
    setLifecycleBusy(true)
    setError(null)
    void (async () => {
      try {
        await queueSave()
        await enqueueMutation(async () => {
          const currentSession = sessionRef.current
          const currentFences = fencesRef.current
          if (!currentSession || !currentFences) throw new Error('The exact SandboxSession is unavailable.')
          if (!currentSession.allowedActions.includes('suspend')) {
            const reason = currentSession.blockingReasons
              .find((item) => item.actions.includes('suspend'))?.detail
            throw new Error(reason || 'The server does not currently allow this sandbox to be suspended.')
          }
          const result = await sandbox.suspendSession(currentSession.id, {
            fences: currentFences,
            idempotencyKey: deterministicOperationKey('sandbox-suspend', JSON.stringify({
              sessionId: currentSession.id,
              sessionVersion: currentSession.version,
              sessionEpoch: currentSession.sessionEpoch,
            })),
          })
          updateSession(result.data, result.headers)
          setShowAgent(false)
          setShowTerminal(false)
        })
      } catch (cause) {
        setError(`Sandbox suspend failed: ${errorMessage(cause)}`)
      } finally {
        setLifecycleBusy(false)
      }
    })()
  }, [enqueueMutation, lifecycleBusy, queueSave, sandbox, updateSession])

  const resumeSandbox = useCallback(() => {
    if (!sandbox || lifecycleBusy) return
    setLifecycleBusy(true)
    setError(null)
    void enqueueMutation(async () => {
      try {
        const currentSession = sessionRef.current
        const currentFences = fencesRef.current
        if (!currentSession || !currentFences) throw new Error('The exact SandboxSession is unavailable.')
        if (!currentSession.allowedActions.includes('resume')) {
          throw new Error('The server does not currently allow this sandbox to be resumed.')
        }
        const result = await sandbox.resumeSession(currentSession.id, {
          fences: currentFences,
          idempotencyKey: deterministicOperationKey('sandbox-resume', JSON.stringify({
            sessionId: currentSession.id,
            sessionVersion: currentSession.version,
            sessionEpoch: currentSession.sessionEpoch,
          })),
        })
        updateSession(result.data, result.headers)
        await loadTree(currentSession.id)
      } catch (cause) {
        setError(`Sandbox resume failed: ${errorMessage(cause)}`)
      } finally {
        setLifecycleBusy(false)
      }
    })
  }, [enqueueMutation, lifecycleBusy, loadTree, sandbox, updateSession])

  const terminateSandbox = useCallback(() => {
    const reason = terminationReason.trim()
    if (!sandbox || lifecycleBusy || !reason) return
    setLifecycleBusy(true)
    setError(null)
    void (async () => {
      try {
        await queueSave()
        await enqueueMutation(async () => {
          const currentSession = sessionRef.current
          const currentFences = fencesRef.current
          if (!currentSession || !currentFences) throw new Error('The exact SandboxSession is unavailable.')
          if (!currentSession.allowedActions.includes('terminate')) {
            const blocking = currentSession.blockingReasons
              .find((item) => item.actions.includes('terminate'))?.detail
            throw new Error(blocking || 'The server does not currently allow this sandbox to be terminated.')
          }
          await sandbox.terminateSession(currentSession.id, reason, {
            fences: currentFences,
            idempotencyKey: deterministicOperationKey('sandbox-terminate', JSON.stringify({
              sessionId: currentSession.id,
              sessionVersion: currentSession.version,
              sessionEpoch: currentSession.sessionEpoch,
              reason,
            })),
          })
          setStorageValue(sessionStorageKey)
          selectedFileRef.current = null
          setSelectedFile(null)
          setTreeFiles([])
          setShowAgent(false)
          setShowTerminal(false)
          setTerminateOpen(false)
          setTerminationReason('')
          setPhase('idle')
        })
      } catch (cause) {
        setError(`Sandbox termination failed: ${errorMessage(cause)}`)
      } finally {
        setLifecycleBusy(false)
      }
    })()
  }, [enqueueMutation, lifecycleBusy, queueSave, sandbox, sessionStorageKey, terminationReason])

  const abandonCandidate = useCallback(() => {
    const reason = abandonmentReason.trim()
    if (!sandbox || abandonActionBlocked || !abandonConfirmed || !reason || reason.length > 1000) return
    cancelCandidateFileOpen()
    setAbandonBusy(true)
    setError(null)
    void (async () => {
      try {
        // This is deliberately explicit even when the toolbar currently says
        // Saved: a just-queued editor mutation must settle before we bind the
        // terminal Candidate transition to exact fences.
        await queueSave()
        await enqueueMutation(async () => {
          let currentSession = sessionRef.current
          const currentCandidate = candidateRef.current
          let currentFences = fencesRef.current
          const openFile = selectedFileRef.current
          if (
            !currentSession
            || !currentCandidate
            || !currentFences
            || pendingSaveRef.current
            || (openFile && fileNeedsSave(openFile))
          ) {
            throw new Error('The exact Candidate must finish autosaving before it can be abandoned.')
          }
          if (
            currentCandidate.status !== 'active'
            || currentSession.candidate.id !== currentCandidate.id
            || currentSession.candidate.version !== currentCandidate.version
            || currentSession.candidate.journalSequence !== currentCandidate.journalSequence
            || currentSession.candidate.writerLeaseEpoch !== currentCandidate.writerLeaseEpoch
            || currentSession.candidate.treeHash !== currentCandidate.treeHash
            || currentFences.candidateVersion !== currentCandidate.version
            || currentFences.writerLeaseEpoch !== currentCandidate.writerLeaseEpoch
            || currentFences.sessionEpoch !== currentSession.sessionEpoch
            || currentFences.treeHash !== currentCandidate.treeHash
          ) {
            throw new Error('The local Candidate projection is stale; reconnect before abandoning it.')
          }
          let checkpointId: string | undefined
          if (currentCandidate.dirty) {
            if (!hasExactCandidateCheckpoint(currentSession, currentCandidate)) {
              if (!currentSession.allowedActions.includes('checkpoint')) {
                const blocking = currentSession.blockingReasons
                  .find((item) => item.actions.includes('checkpoint'))?.detail
                throw new Error(blocking || 'Create an exact Candidate checkpoint before abandoning these saved changes.')
              }
              const checkpoint = await sandbox.checkpoint(
                currentSession.id,
                {
                  checkpointId: newUUID(),
                  reason: 'Checkpoint exact Candidate before explicit abandonment',
                },
                {
                  fences: currentFences,
                  idempotencyKey: deterministicOperationKey('abandon-checkpoint', JSON.stringify({
                    sessionId: currentSession.id,
                    candidateId: currentCandidate.id,
                    candidateVersion: currentCandidate.version,
                    journalSequence: currentCandidate.journalSequence,
                    treeHash: currentCandidate.treeHash,
                  })),
                },
              )
              currentSession = checkpoint.data.session
              currentFences = sandboxFences(checkpoint.headers, currentSession)
              updateSession(currentSession, checkpoint.headers)
            }
            if (!hasExactCandidateCheckpoint(currentSession, currentCandidate)) {
              throw new Error('The checkpoint response does not bind the exact Candidate being abandoned.')
            }
            checkpointId = currentSession.latestCheckpoint?.id
            if (!checkpointId) {
              throw new Error('The exact Candidate checkpoint identity is unavailable.')
            }
          }
          if (!currentSession.allowedActions.includes('abandon')) {
            const blocking = currentSession.blockingReasons
              .find((item) => item.actions.includes('abandon'))?.detail
            throw new Error(blocking || 'The server does not currently allow this Candidate to be abandoned.')
          }

          const abandonedSessionId = currentSession.id
          const result = await sandbox.abandonCandidate(
            abandonedSessionId,
            {
              candidateId: currentCandidate.id,
              ...(checkpointId ? { checkpointId } : {}),
              reason,
            },
            {
              fences: currentFences,
              idempotencyKey: deterministicOperationKey('candidate-abandon', JSON.stringify({
                sessionId: abandonedSessionId,
                candidateId: currentCandidate.id,
                sessionVersion: currentSession.version,
                sessionEpoch: currentSession.sessionEpoch,
                candidateVersion: currentCandidate.version,
                writerLeaseEpoch: currentCandidate.writerLeaseEpoch,
                treeHash: currentCandidate.treeHash,
                checkpointId: checkpointId ?? null,
                reason,
              })),
            },
          )
          if (
            result.data.session.id !== abandonedSessionId
            || result.data.session.projectId !== projectId
            || result.data.session.state !== 'terminated'
            || result.data.session.candidate.id !== currentCandidate.id
            || result.data.session.candidate.status !== 'abandoned'
            || result.data.candidate.id !== currentCandidate.id
            || result.data.candidate.projectId !== projectId
            || result.data.candidate.status !== 'abandoned'
            || result.data.session.candidate.version !== result.data.candidate.version
            || result.data.session.candidate.writerLeaseEpoch !== result.data.candidate.writerLeaseEpoch
            || result.data.session.candidate.treeHash !== result.data.candidate.treeHash
          ) {
            throw new Error('The abandon response does not prove the exact Candidate and SandboxSession reached terminal state.')
          }

          setStorageValue(activeCandidateStorageKey)
          setStorageValue(sessionStorageKey)
          setStorageValue(pendingStorageKey(abandonedSessionId))
          setStorageValue(verificationRunStorageKey(abandonedSessionId))
          pendingSaveRef.current = null
          sessionRef.current = null
          candidateRef.current = null
          fencesRef.current = null
          selectedFileRef.current = null
          setSession(null)
          setCandidate(null)
          setFences(null)
          setTreeFiles([])
          setSelectedFile(null)
          setNewPath('')
          setRenamePath('')
          setRenameOpen(false)
          setMarkers([])
          setSaveState('saved')
          setProcess(null)
          setProcessEtag('')
          setPorts([])
          setPreview(null)
          setTerminal(null)
          setShowTerminal(false)
          setShowAgent(false)
          setSelectedService('')
          setSelectedProfile('dev')
          setVerificationProfiles([])
          setSelectedVerificationProfileKey('')
          setVerificationRun(null)
          setVerificationReceipt(null)
          setVerificationError(null)
          setVerificationRetryReason('')
          setFreezeNotice(null)
          setCandidateHeads([])
          setRebase(null)
          setSelectedConflictId('')
          setConflictContent(null)
          setConflictDraft('')
          setAbandonOpen(false)
          setAbandonmentReason('')
          setAbandonConfirmed(false)
          setPhase('idle')
        })
        // This reuses Candidate discovery/bootstrap only. It does not refetch or
        // replace Blueprint, PageSpec, Prototype, or other governed documents.
        await openSandbox()
      } catch (cause) {
        setError(`Candidate abandonment failed: ${errorMessage(cause)}`)
      } finally {
        setAbandonBusy(false)
      }
    })()
  }, [
    abandonActionBlocked,
    abandonConfirmed,
    abandonmentReason,
    activeCandidateStorageKey,
    cancelCandidateFileOpen,
    enqueueMutation,
    openSandbox,
    pendingStorageKey,
    projectId,
    queueSave,
    sandbox,
    sessionStorageKey,
    updateSession,
  ])

  const refreshPorts = useCallback(async () => {
    const current = sessionRef.current
    if (!sandbox || !current) return
    try {
      const result = await sandbox.listPorts(current.id)
      updateSession(result.data.session, result.headers)
      setPorts(result.data.ports)
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }, [sandbox, updateSession])

  useEffect(() => {
    if (phase !== 'ready' || !process || (process.state !== 'running' && process.state !== 'starting')) return
    const timer = window.setInterval(() => void refreshPorts(), 2000)
    return () => window.clearInterval(timer)
  }, [phase, process, refreshPorts])

  const startProcess = useCallback(() => {
    const current = sessionRef.current
    const currentFences = fencesRef.current
    if (!sandbox || !current || !currentFences || !current.allowedActions.includes('process') || !selectedService || !selectedProfile) return
    setRuntimeBusy(true)
    void enqueueMutation(async () => {
      try {
        const result = await sandbox.startProcess(
          current.id,
          { serviceId: selectedService, commandId: selectedProfile },
          {
            fences: currentFences,
            idempotencyKey: randomKey(`process-v${currentFences.sessionEpoch}-${current.version}`),
          },
        )
        updateSession(result.data.session, result.headers)
        setProcess(result.data.process)
        setProcessEtag(result.etag ?? result.headers.get('x-sandbox-process-etag') ?? '')
        await refreshPorts()
      } catch (cause) {
        setError(errorMessage(cause))
      } finally {
        setRuntimeBusy(false)
      }
    })
  }, [enqueueMutation, refreshPorts, sandbox, selectedProfile, selectedService, updateSession])

  const stopProcess = useCallback(() => {
    const currentSession = sessionRef.current
    const currentFences = fencesRef.current
    if (!sandbox || !currentSession || !currentFences || !currentSession.allowedActions.includes('process') || !process || !processEtag) return
    setRuntimeBusy(true)
    void enqueueMutation(async () => {
      try {
        const result = await sandbox.signalProcess(currentSession.id, process.id, 'TERM', {
          sessionFences: currentFences,
          processEtag,
          idempotencyKey: randomKey(`process-term-v${process.version}`),
        })
        updateSession(result.data.session, result.headers)
        setProcess(result.data.process)
        setProcessEtag(result.etag ?? processEtag)
      } catch (cause) {
        setError(errorMessage(cause))
      } finally {
        setRuntimeBusy(false)
      }
    })
  }, [enqueueMutation, process, processEtag, sandbox, updateSession])

  const openPreview = useCallback((portName: string) => {
    const current = sessionRef.current
    const currentFences = fencesRef.current
    if (!sandbox || !current || !currentFences || !current.allowedActions.includes('pty')) return
    setRuntimeBusy(true)
    void enqueueMutation(async () => {
      try {
        const result = await sandbox.createPreviewLink(current.id, portName, {
          fences: currentFences,
          idempotencyKey: randomKey(`preview-v${current.version}`),
        })
        setPreview(result.data)
      } catch (cause) {
        setError(errorMessage(cause))
      } finally {
        setRuntimeBusy(false)
      }
    })
  }, [enqueueMutation, sandbox])

  const openTerminal = useCallback(() => {
    const current = sessionRef.current
    const currentFences = fencesRef.current
    if (!sandbox || !current || !currentFences) return
    setShowTerminal(true)
    if (terminal) return
    void enqueueMutation(async () => {
      try {
        const result = await sandbox.createTerminal(
          current.id,
          { workingDirectory: '.', rows: 24, columns: 80 },
          {
            fences: currentFences,
            idempotencyKey: randomKey(`terminal-v${current.version}`),
          },
        )
        updateSession(result.data.session, result.headers)
        setTerminal(result.data.terminal)
      } catch (cause) {
        setError(errorMessage(cause))
      }
    })
  }, [enqueueMutation, sandbox, terminal, updateSession])

  const verificationReadyForFreeze = Boolean(
    session
    && candidate
    && verificationRun
    && verificationRun.allowedActions.includes('freeze')
    && verificationRun.receiptDecision === 'passed'
    && exactVerificationSubject(verificationRun, session, candidate)
    && verificationRun.receipt
    && verificationReceipt
    && verificationReceipt.id === verificationRun.receipt.id
    && verificationReceipt.payloadHash === verificationRun.receipt.contentHash
    && verificationReceipt.runId === verificationRun.run.id
    && verificationReceipt.decision === 'passed',
  )

  const lspDocument = useMemo(() => selectedFile ? {
    path: selectedFile.path,
    contentHash: selectedFile.contentHash,
    binary: selectedFile.binary,
    stale: Boolean(selectedFile.stale),
  } : null, [
    selectedFile?.binary,
    selectedFile?.contentHash,
    selectedFile?.path,
    selectedFile?.stale,
  ])
  const lspAdmission = useMemo(() => resolveSandboxLSPAdmission({
    projectId,
    canEdit: mode === 'code' && canMutateCandidate && !checkpointBusy && !freezeBusy &&
      !agentMutationBusy && !abandonBusy,
    session,
    candidate,
    fences,
    selectedServiceId: selectedService,
    document: lspDocument,
    now: Math.max(lspAuthorityNow, Date.now()),
  }), [
    abandonBusy,
    agentMutationBusy,
    canMutateCandidate,
    candidate,
    checkpointBusy,
    fences,
    freezeBusy,
    lspDocument,
    lspAuthorityNow,
    mode,
    projectId,
    selectedService,
    session,
  ])
  const refreshLSPExactHead = useCallback(async () => {
    const current = sessionRef.current
    if (!current) throw new Error('The exact Sandbox session is unavailable.')
    await loadTree(current.id)
  }, [loadTree])
  const ignoreLSPMarkerSummary = useCallback(() => {
    // Monaco's onValidate callback owns the combined native + exact LSP view.
  }, [])
  const sandboxLSP = useSandboxLSP({
    client: platformClient.lsp,
    admission: lspAdmission,
    languageId: selectedFile ? fileLSPLanguage(selectedFile.path) : 'plaintext',
    onRefreshExactHead: refreshLSPExactHead,
    onMarkers: ignoreLSPMarkerSummary,
  })
  const editorModelURI = selectedFile
    ? candidateEditorModelURI(projectId, candidate?.id ?? '', selectedFile.path)
    : undefined

  if (!repository || !sandbox) {
    return <SandboxGate title="Sandbox unavailable" description={copy.unavailable} />
  }
  if (phase === 'idle') {
    return (
      <SandboxGate
        title={storageValue<PersistedSession>(sessionStorageKey)?.sessionId ? copy.reconnect : copy.open}
        description={copy.description}
        action={copy.open}
        onAction={openSandbox}
      />
    )
  }
  if (phase === 'opening') {
    return <SandboxGate loading title="Opening sandbox" description={copy.opening} />
  }
  if (phase === 'select-head') {
    return (
      <CandidateHeadPicker
        heads={candidateHeads}
        targetBuildManifestId={buildManifestId}
        onSelect={(head) => {
          setStorageValue(activeCandidateStorageKey, {
            candidateId: head.candidate.id,
            buildManifestId: head.candidate.buildManifest.id,
            rebaseId: head.rebaseId,
          } satisfies PersistedActiveCandidate)
          setCandidateHeads([])
          setPhase('idle')
          queueMicrotask(() => void openSandbox())
        }}
        onRefresh={openSandbox}
      />
    )
  }
  if (phase === 'rebase' && rebase) {
    return (
      <RebaseConflictWorkspace
        result={rebase}
        conflict={selectedConflict}
        content={conflictContent}
        draft={conflictDraft}
        mode={conflictMode}
        busy={rebaseBusy}
        error={error}
        canEdit={can('edit')}
        onSelect={setSelectedConflictId}
        onDraft={setConflictDraft}
        onMode={setConflictMode}
        onResolve={(strategy) => void resolveRebaseConflict(strategy)}
        onRetry={openSandbox}
      />
    )
  }
  if (phase === 'error' || !session || !fences || !candidate) {
    return (
      <SandboxGate
        title="Sandbox could not be opened"
        description={error ?? 'The exact Candidate or runtime is unavailable.'}
        action="Retry exact operation"
        onAction={openSandbox}
      />
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <SandboxToolbar
        session={session}
        candidate={candidate}
        saveState={saveState}
        process={process}
        services={session.allowedServices}
        selectedService={selectedService}
        selectedProfile={selectedProfile}
        runtimeBusy={runtimeBusy}
        lifecycleBusy={lifecycleBusy}
        abandonBusy={abandonBusy}
        abandonBlocked={abandonActionBlocked}
        checkpointBusy={checkpointBusy}
        freezeBusy={freezeBusy}
        verificationReadyForFreeze={verificationReadyForFreeze}
        onService={(serviceId) => {
          setSelectedService(serviceId)
          const service = session.allowedServices.find((item) => item.id === serviceId)
          setSelectedProfile(service?.profiles.includes('dev') ? 'dev' : service?.profiles[0] ?? '')
        }}
        onProfile={setSelectedProfile}
        onStart={startProcess}
        onStop={stopProcess}
        onTerminal={openTerminal}
        onSuspend={suspendSandbox}
        onResume={resumeSandbox}
        onTerminate={() => {
          setAbandonOpen(false)
          setAbandonmentReason('')
          setAbandonConfirmed(false)
          setTerminateOpen(true)
        }}
        onAbandon={() => {
          if (abandonActionBlocked) return
          setTerminateOpen(false)
          setTerminationReason('')
          setAbandonOpen(true)
        }}
        agentOpen={showAgent}
        agentBusy={agentMutationBusy}
        agentAvailable={Boolean(agent)}
        onAgent={() => setShowAgent((value) => !value)}
        onCheckpoint={createCheckpoint}
        onFreeze={freezeCandidate}
        onRetrySave={() => void queueSave(true)}
      />
      {terminateOpen && (
        <div className="flex shrink-0 items-center gap-2 border-b border-destructive/30 bg-destructive/5 px-3 py-2">
          <Power className="size-3.5 shrink-0 text-destructive" />
          <label htmlFor="sandbox-termination-reason" className="text-[10px] text-muted-foreground">
            Termination reason
          </label>
          <input
            id="sandbox-termination-reason"
            value={terminationReason}
            onChange={(event) => setTerminationReason(event.target.value)}
            onKeyDown={(event) => { if (event.key === 'Enter' && terminationReason.trim()) terminateSandbox() }}
            placeholder="Why this sandbox is being terminated"
            autoFocus
            className="h-7 min-w-48 flex-1 rounded border border-border bg-background px-2 text-[10px] text-foreground outline-none"
          />
          <button
            type="button"
            onClick={terminateSandbox}
            disabled={lifecycleBusy || !terminationReason.trim()}
            className="inline-flex h-7 items-center gap-1 rounded bg-destructive px-2 text-[9px] font-semibold text-destructive-foreground disabled:opacity-40"
          >
            {lifecycleBusy ? <LoaderCircle className="size-3 animate-spin" /> : <Power className="size-3" />}
            Terminate
          </button>
          <button
            type="button"
            onClick={() => { setTerminateOpen(false); setTerminationReason('') }}
            disabled={lifecycleBusy}
            aria-label="Cancel sandbox termination"
            className="rounded p-1 text-faint-foreground hover:text-foreground disabled:opacity-40"
          >
            <X className="size-3.5" />
          </button>
        </div>
      )}
      {abandonOpen && (
        <div
          role="region"
          aria-label="Confirm Candidate abandonment"
          className="flex shrink-0 flex-wrap items-center gap-2 border-b border-destructive/30 bg-destructive/5 px-3 py-2"
        >
          <Trash2 className="size-3.5 shrink-0 text-destructive" />
          <div className="min-w-52 flex-1">
            <div className="text-[10px] font-semibold text-destructive">Abandon this Candidate permanently</div>
            <div className="text-[9px] text-muted-foreground">
              Saved changes are bound to an exact checkpoint first. A clean successor opens without reloading governed documents.
            </div>
          </div>
          <label htmlFor="candidate-abandonment-reason" className="sr-only">Candidate abandonment reason</label>
          <input
            id="candidate-abandonment-reason"
            value={abandonmentReason}
            onChange={(event) => setAbandonmentReason(event.target.value)}
            placeholder="Why this Candidate is being abandoned"
            maxLength={1000}
            autoFocus
            disabled={abandonBusy}
            className="h-7 min-w-64 flex-1 rounded border border-border bg-background px-2 text-[10px] text-foreground outline-none disabled:opacity-40"
          />
          <label className="flex max-w-72 items-center gap-1.5 text-[9px] text-muted-foreground">
            <input
              type="checkbox"
              checked={abandonConfirmed}
              onChange={(event) => setAbandonConfirmed(event.target.checked)}
              disabled={abandonBusy}
            />
            I understand this Candidate cannot be restored as the active workspace.
          </label>
          <button
            type="button"
            onClick={abandonCandidate}
            disabled={abandonActionBlocked || !abandonConfirmed || !abandonmentReason.trim()}
            className="inline-flex h-7 items-center gap-1 rounded bg-destructive px-2 text-[9px] font-semibold text-destructive-foreground disabled:opacity-40"
          >
            {abandonBusy ? <LoaderCircle className="size-3 animate-spin" /> : <Trash2 className="size-3" />}
            Abandon and start clean
          </button>
          <button
            type="button"
            onClick={() => {
              setAbandonOpen(false)
              setAbandonmentReason('')
              setAbandonConfirmed(false)
            }}
            disabled={abandonBusy}
            aria-label="Cancel Candidate abandonment"
            className="rounded p-1 text-faint-foreground hover:text-foreground disabled:opacity-40"
          >
            <X className="size-3.5" />
          </button>
        </div>
      )}
      <VerificationPanel
        session={session}
        candidate={candidate}
        profiles={verificationProfiles}
        selectedProfileKey={selectedVerificationProfileKey}
        run={verificationRun}
        receipt={verificationReceipt}
        retryReason={verificationRetryReason}
        error={verificationError}
        busy={verificationBusy}
        workspaceBusy={abandonBusy || checkpointBusy || freezeBusy || agentMutationBusy || saveState !== 'saved'}
        canEdit={can('edit')}
        onProfile={setSelectedVerificationProfileKey}
        onVerify={createVerificationRun}
        onCancel={cancelVerificationRun}
        onRetryReason={setVerificationRetryReason}
        onRetry={retryVerificationRun}
      />
      {error && (
        <div role="alert" className="flex shrink-0 items-center gap-2 border-b border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">
          <AlertTriangle className="size-3.5 shrink-0" />
          <span className="min-w-0 flex-1 truncate" title={error}>{error}</span>
          <button type="button" onClick={() => setError(null)} aria-label="Dismiss"><X className="size-3" /></button>
        </div>
      )}
      {freezeNotice && (
        <div role="status" className="flex shrink-0 items-center gap-2 border-b border-success/30 bg-success/10 px-3 py-2 text-[10px] text-success">
          <PackageCheck className="size-3.5 shrink-0" />
          <span className="min-w-0 flex-1">{freezeNotice}</span>
          <button type="button" onClick={() => setFreezeNotice(null)} aria-label="Dismiss"><X className="size-3" /></button>
        </div>
      )}
      {mode === 'code' && (
        <SandboxLSPStatus
          view={sandboxLSP.view}
          profiles={sandboxLSP.profiles}
          selectedProfileId={sandboxLSP.selectedProfileId}
          enabled={sandboxLSP.enabled}
          onProfile={sandboxLSP.setSelectedProfileId}
          onEnable={sandboxLSP.enable}
          onDisable={sandboxLSP.disable}
          onRetry={sandboxLSP.retry}
        />
      )}
      {mode === 'preview' ? (
        <SandboxPreview
          preview={preview}
          ports={ports}
          viewport={viewport}
          onViewport={setViewport}
          onRefresh={refreshPorts}
          onOpen={openPreview}
          busy={runtimeBusy || abandonBusy}
        />
      ) : (
        <div className="flex min-h-0 flex-1">
          <FileTree
            files={treeFiles}
            selected={selectedFile?.path}
            newPath={newPath}
            canEdit={canMutateCandidate && !selectedFile?.stale && !checkpointBusy && !freezeBusy && !agentMutationBusy && !abandonBusy}
            searchQuery={candidateSearchQuery}
            searchCaseSensitive={candidateSearchCaseSensitive}
            searchInclude={candidateSearchInclude}
            searchStatus={candidateSearchStatus}
            searchResult={candidateSearchResult}
            searchError={candidateSearchError}
            searchHeadResolved={candidateSearchHeadResolved}
            searchHeadRefreshAllowed={candidateSearchHeadRefreshAllowed}
            searchMutationBusy={candidateHeadMutationInFlight}
            onNewPath={setNewPath}
            onCreate={createFile}
            onSelect={(path) => void readFile(path)}
            onSearchQuery={setCandidateSearchQuery}
            onSearchCaseSensitive={setCandidateSearchCaseSensitive}
            onSearchInclude={setCandidateSearchInclude}
            onSearchRefresh={requestCandidateSearchHeadRefresh}
            onSearchMatch={openCandidateSearchMatch}
          />
          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3 text-[10px]">
              <Code2 className="size-3.5 text-primary-bright" />
              {selectedFile && renameOpen ? (
                <input
                  value={renamePath}
                  onChange={(event) => setRenamePath(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') renameSelectedFile()
                    if (event.key === 'Escape') { setRenameOpen(false); setRenamePath('') }
                  }}
                  autoFocus
                  aria-label="New Candidate file path"
                  className="h-7 min-w-0 flex-1 rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground outline-none"
                />
              ) : (
                <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground">{selectedFile?.path ?? 'Select a file'}</span>
              )}
              {selectedFile?.stale && (
                <span className="rounded bg-warning/10 px-1.5 py-0.5 text-[8px] font-semibold uppercase text-warning" title={selectedFile.staleReason}>
                  stale draft · save blocked
                </span>
              )}
              {selectedFile && !selectedFile.stale && canMutateCandidate && !checkpointBusy && !freezeBusy && !agentMutationBusy && !abandonBusy && (
                renameOpen ? (
                  <>
                    <button type="button" onClick={renameSelectedFile} disabled={renameBusy || !renamePath.trim()} className="rounded p-1 text-success hover:bg-success/10 disabled:opacity-40" title="Confirm rename"><CheckCircle2 className="size-3.5" /></button>
                    <button type="button" onClick={() => { setRenameOpen(false); setRenamePath('') }} disabled={renameBusy} className="rounded p-1 text-faint-foreground hover:text-foreground disabled:opacity-40" title="Cancel rename"><X className="size-3.5" /></button>
                  </>
                ) : (
                  <button type="button" onClick={() => { setRenamePath(selectedFile.path); setRenameOpen(true) }} className="rounded p-1 text-faint-foreground hover:bg-primary/10 hover:text-primary-bright" title="Rename file"><Pencil className="size-3.5" /></button>
                )
              )}
              {selectedFile && !selectedFile.stale && selectedFile.contentHash !== 'absent' && canMutateCandidate && !checkpointBusy && !freezeBusy && !agentMutationBusy && !abandonBusy && !renameOpen && (
                <button type="button" onClick={deleteSelectedFile} className="rounded p-1 text-faint-foreground hover:bg-destructive/10 hover:text-destructive" title="Delete file"><Trash2 className="size-3.5" /></button>
              )}
            </div>
            <div className="min-h-0 flex-1">
              {selectedFile?.binary ? (
                <BinaryFileViewer file={selectedFile} />
              ) : selectedFile ? (
                <MonacoEditor
                  path={editorModelURI}
                  keepCurrentModel
                  language={fileLanguage(selectedFile.path)}
                  value={selectedFile.draft}
                  onMount={sandboxLSP.onMount}
                  onChange={(value) => {
                    if (selectedFile.stale || agentMutationBusyRef.current || !canMutateCandidate) return
                    cancelCandidateFileOpen()
                    setSelectedFile((current) => {
                      if (!current) return current
                      const nextOpenFile = { ...current, draft: value ?? '' }
                      selectedFileRef.current = nextOpenFile
                      return nextOpenFile
                    })
                  }}
                  onValidate={(values) => setMarkers(values.map((marker) => ({
                    severity: marker.severity,
                    message: marker.message,
                    startLineNumber: marker.startLineNumber,
                    startColumn: marker.startColumn,
                  })))}
                  theme="vs-dark"
                  options={{
                    automaticLayout: true,
                    minimap: { enabled: true },
                    fontSize: 12,
                    lineHeight: 20,
                    readOnly: selectedFile.stale || !canMutateCandidate || checkpointBusy || freezeBusy || agentMutationBusy || abandonBusy,
                    wordWrap: 'off',
                    scrollBeyondLastLine: false,
                    renderWhitespace: 'selection',
                    bracketPairColorization: { enabled: true },
                  }}
                />
              ) : (
                <div className="flex h-full items-center justify-center text-[10px] text-faint-foreground">Select or create a Candidate file.</div>
              )}
            </div>
            {selectedFile && !selectedFile.binary && (
              <InspectorPane
                tab={inspector}
                onTab={setInspector}
                file={selectedFile}
                markers={markers}
              />
            )}
            {showTerminal && (
              <TerminalPane
                sandbox={sandbox}
                sessionId={session.id}
                terminal={terminal}
                onClose={() => setShowTerminal(false)}
              />
            )}
          </div>
          {showAgent && agent && (
            <AgentPanel
              client={agent}
              sandbox={sandbox}
              sessionId={session.id}
              canEdit={!selectedFile?.stale && canMutateCandidate && session.allowedActions.includes('agent')}
              workspaceBusy={abandonBusy || agentMutationBusy || checkpointBusy || freezeBusy || saveState !== 'saved'}
              onCreate={createAgentAttempt}
              onMerge={mergeAgentPatch}
              onUndo={undoAgentPatch}
              onClose={() => setShowAgent(false)}
            />
          )}
        </div>
      )}
    </div>
  )
}

function CandidateHeadPicker({
  heads,
  targetBuildManifestId,
  onSelect,
  onRefresh,
}: {
  readonly heads: readonly RepositoryCandidateHeadDto[]
  readonly targetBuildManifestId: string
  readonly onSelect: (head: RepositoryCandidateHeadDto) => void
  readonly onRefresh: () => void
}) {
  return (
    <div className="flex h-full items-center justify-center overflow-y-auto bg-canvas p-6">
      <section className="w-full max-w-3xl rounded-xl border border-warning/30 bg-panel shadow-2xl">
        <header className="flex items-start gap-3 border-b border-border px-5 py-4">
          <GitMerge className="mt-0.5 size-5 shrink-0 text-warning" />
          <div className="min-w-0 flex-1">
            <h2 className="text-sm font-semibold text-foreground">Choose the durable Candidate head</h2>
            <p className="mt-1 text-[10px] leading-relaxed text-faint-foreground">
              More than one exact project head is available. Worksflow will not choose by timestamp.
              Select the source workspace explicitly; a different BuildManifest will create an immutable rebase successor.
            </p>
          </div>
          <button
            type="button"
            onClick={onRefresh}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded border border-border px-2.5 text-[9px] text-muted-foreground hover:text-foreground"
          >
            <RefreshCw className="size-3" /> Refresh
          </button>
        </header>
        <div className="space-y-2 p-4">
          {heads.map((head) => {
            const candidate = head.candidate
            const exactManifest = candidate.buildManifest.id === targetBuildManifestId
            const blocked = candidate.conflicted || candidate.stale || candidate.rebaseRequired
            return (
              <article key={candidate.id} className="rounded-lg border border-border bg-background p-3">
                <div className="flex flex-wrap items-start gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Code2 className="size-3.5 text-primary-bright" />
                      <span className="font-mono text-[10px] text-foreground" title={candidate.id}>
                        {candidate.id.slice(0, 12)}
                      </span>
                      <span className={cn(
                        'rounded px-1.5 py-0.5 text-[8px] font-semibold uppercase',
                        exactManifest ? 'bg-success/10 text-success' : 'bg-warning/10 text-warning',
                      )}>
                        {exactManifest ? 'exact manifest' : 'rebase required'}
                      </span>
                      {blocked && (
                        <span className="rounded bg-destructive/10 px-1.5 py-0.5 text-[8px] font-semibold uppercase text-destructive">
                          conflict recovery
                        </span>
                      )}
                    </div>
                    <dl className="mt-2 grid gap-x-4 gap-y-1 text-[8px] text-faint-foreground sm:grid-cols-2">
                      <div className="flex min-w-0 gap-1.5">
                        <dt>Manifest</dt>
                        <dd className="truncate font-mono text-muted-foreground" title={candidate.buildManifest.id}>{candidate.buildManifest.id}</dd>
                      </div>
                      <div className="flex min-w-0 gap-1.5">
                        <dt>Tree</dt>
                        <dd className="truncate font-mono text-muted-foreground" title={candidate.treeHash}>{candidate.treeHash}</dd>
                      </div>
                      <div className="flex gap-1.5">
                        <dt>Fence</dt>
                        <dd className="font-mono text-muted-foreground">C{candidate.version} · J{candidate.journalSequence} · E{candidate.writerLeaseEpoch}</dd>
                      </div>
                      <div className="flex min-w-0 gap-1.5">
                        <dt>Updated</dt>
                        <dd className="truncate text-muted-foreground">{candidate.updatedAt || 'unknown'}</dd>
                      </div>
                    </dl>
                  </div>
                  <button
                    type="button"
                    onClick={() => onSelect(head)}
                    className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded bg-primary px-3 text-[9px] font-semibold text-primary-foreground"
                  >
                    {exactManifest ? <MonitorPlay className="size-3" /> : <GitMerge className="size-3" />}
                    {exactManifest ? 'Open this head' : 'Rebase this head'}
                  </button>
                </div>
                {head.rebaseId && (
                  <p className="mt-2 border-t border-border pt-2 font-mono text-[8px] text-faint-foreground" title={head.rebaseId}>
                    Incoming rebase {head.rebaseId}
                  </p>
                )}
              </article>
            )
          })}
        </div>
      </section>
    </div>
  )
}

function RebaseConflictWorkspace({
  result,
  conflict,
  content,
  draft,
  mode,
  busy,
  error,
  canEdit,
  onSelect,
  onDraft,
  onMode,
  onResolve,
  onRetry,
}: {
  readonly result: CandidateRebaseResultDto
  readonly conflict?: CandidateRebaseConflictDto
  readonly content: CandidateRebaseConflictContentDto | null
  readonly draft: string
  readonly mode: '100644' | '100755'
  readonly busy: boolean
  readonly error: string | null
  readonly canEdit: boolean
  readonly onSelect: (id: string) => void
  readonly onDraft: (value: string) => void
  readonly onMode: (value: '100644' | '100755') => void
  readonly onResolve: (strategy: CandidateRebaseResolutionStrategy) => void
  readonly onRetry: () => void
}) {
  const open = result.rebase.conflicts.filter((item) => item.state === 'open')
  const target = conflictText(content?.target)
  const predecessor = conflictText(content?.predecessor)
  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <div className="flex min-h-12 shrink-0 flex-wrap items-center gap-2 border-b border-warning/30 bg-warning/5 px-4 py-2">
        <GitMerge className="size-4 text-warning" />
        <div className="min-w-0">
          <h2 className="text-[11px] font-semibold text-foreground">Resolve exact Candidate rebase</h2>
          <p className="text-[9px] text-faint-foreground">
            A new immutable successor was created. Autosave remains paused until every divergent path has an explicit decision.
          </p>
        </div>
        <div className="ml-auto flex items-center gap-2 font-mono text-[8px] text-faint-foreground">
          <span title={result.rebase.id}>{result.rebase.id.slice(0, 8)}</span>
          <span>{open.length} open</span>
          <span>C{result.candidate.version}</span>
        </div>
      </div>
      {error && (
        <div role="alert" className="flex shrink-0 items-center gap-2 border-b border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">
          <AlertTriangle className="size-3.5" />
          <span className="min-w-0 flex-1">{error}</span>
          <button type="button" onClick={onRetry} className="rounded border border-destructive/30 px-2 py-1 text-[8px]">Retry exact operation</button>
        </div>
      )}
      <div className="flex min-h-0 flex-1">
        <aside className="flex w-64 shrink-0 flex-col border-r border-border bg-panel">
          <div className="border-b border-border px-3 py-2 text-[9px] font-semibold uppercase tracking-wide text-faint-foreground">
            Three-way conflicts
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-1.5 scrollbar-thin">
            {result.rebase.conflicts.map((item) => (
              <button
                key={item.id}
                type="button"
                onClick={() => onSelect(item.id)}
                className={cn(
                  'mb-1 flex w-full items-start gap-2 rounded px-2 py-2 text-left text-[9px]',
                  conflict?.id === item.id ? 'bg-primary/12 text-primary-bright' : 'text-muted-foreground hover:bg-white/5',
                )}
              >
                {item.state === 'resolved'
                  ? <CheckCircle2 className="mt-0.5 size-3 shrink-0 text-success" />
                  : <AlertTriangle className="mt-0.5 size-3 shrink-0 text-warning" />}
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-mono" title={item.path}>{item.path}</span>
                  <span className="mt-0.5 block text-[8px] text-faint-foreground">
                    {item.state === 'resolved' ? item.resolutionStrategy : 'both sides changed'}
                  </span>
                </span>
              </button>
            ))}
          </div>
          <div className="border-t border-border p-3 text-[8px] leading-relaxed text-faint-foreground">
            Plan <span className="font-mono" title={result.rebase.planHash}>{result.rebase.planHash.slice(0, 18)}…</span>
            <br />Target manifest <span className="font-mono">{result.rebase.targetBuildManifestId.slice(0, 8)}</span>
          </div>
        </aside>
        <main className="flex min-w-0 flex-1 flex-col">
          {conflict ? (
            <>
              <div className="flex min-h-10 shrink-0 flex-wrap items-center gap-2 border-b border-border px-3 py-1.5">
                <FileDiff className="size-3.5 text-primary-bright" />
                <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground">{conflict.path}</span>
                <span className="text-[8px] text-faint-foreground">Target → resolved content</span>
                <select
                  value={mode}
                  onChange={(event) => onMode(event.target.value === '100755' ? '100755' : '100644')}
                  disabled={!canEdit || busy || conflict.state !== 'open'}
                  className="h-7 rounded border border-border bg-background px-2 font-mono text-[8px]"
                >
                  <option value="100644">100644</option>
                  <option value="100755">100755</option>
                </select>
              </div>
              <div className="min-h-0 flex-1">
                {content ? (
                  <MonacoDiffEditor
                    original={target}
                    modified={draft}
                    language={fileLanguage(conflict.path)}
                    theme="vs-dark"
                    onMount={(editor) => {
                      editor.getModifiedEditor().onDidChangeModelContent(() => {
                        onDraft(editor.getModifiedEditor().getValue())
                      })
                    }}
                    options={{
                      automaticLayout: true, readOnly: !canEdit || busy || conflict.state !== 'open',
                      originalEditable: false, renderSideBySide: true, fontSize: 11,
                      minimap: { enabled: false }, scrollBeyondLastLine: false,
                    }}
                  />
                ) : (
                  <div className="flex h-full items-center justify-center text-[9px] text-faint-foreground">
                    <LoaderCircle className="mr-2 size-3 animate-spin" /> Loading exact conflict blobs…
                  </div>
                )}
              </div>
              <div className="flex min-h-12 shrink-0 flex-wrap items-center gap-2 border-t border-border bg-panel px-3 py-2">
                <span className="mr-auto text-[8px] text-faint-foreground">
                  Ancestor {conflict.ancestorFile?.contentHash.slice(0, 15) ?? 'deleted'} · predecessor {conflict.predecessorFile?.contentHash.slice(0, 15) ?? 'deleted'} · target {conflict.targetFile?.contentHash.slice(0, 15) ?? 'deleted'}
                </span>
                <button type="button" disabled={!canEdit || busy || conflict.state !== 'open'} onClick={() => onResolve('target')} className="h-8 rounded border border-border px-3 text-[9px] text-muted-foreground disabled:opacity-40">Use target</button>
                <button type="button" disabled={!canEdit || busy || conflict.state !== 'open'} onClick={() => onResolve('predecessor')} className="h-8 rounded border border-border px-3 text-[9px] text-muted-foreground disabled:opacity-40">Use predecessor</button>
                <button type="button" disabled={!canEdit || busy || !content || conflict.state !== 'open'} onClick={() => onResolve('current')} className="inline-flex h-8 items-center gap-1.5 rounded bg-primary px-3 text-[9px] font-semibold text-primary-foreground disabled:opacity-40">
                  {busy ? <LoaderCircle className="size-3 animate-spin" /> : <GitMerge className="size-3" />} Apply edited result
                </button>
                <span className="sr-only">Predecessor content length {predecessor.length}</span>
              </div>
            </>
          ) : (
            <div className="m-auto text-center text-[10px] text-faint-foreground">
              <CheckCircle2 className="mx-auto mb-2 size-6 text-success" /> All conflicts are resolved. Continuing with the successor Candidate…
            </div>
          )}
        </main>
      </div>
    </div>
  )
}


function VerificationPanel({
  session,
  candidate,
  profiles,
  selectedProfileKey,
  run,
  receipt,
  retryReason,
  error,
  busy,
  workspaceBusy,
  canEdit,
  onProfile,
  onVerify,
  onCancel,
  onRetryReason,
  onRetry,
}: {
  readonly session: SandboxSessionDto
  readonly candidate: CandidateWorkspaceDto
  readonly profiles: readonly VerificationProfileSummaryDto[]
  readonly selectedProfileKey: string
  readonly run: CandidateVerificationRunViewDto | null
  readonly receipt: CandidateVerificationReceiptDto | null
  readonly retryReason: string
  readonly error: string | null
  readonly busy: boolean
  readonly workspaceBusy: boolean
  readonly canEdit: boolean
  readonly onProfile: (value: string) => void
  readonly onVerify: () => void
  readonly onCancel: () => void
  readonly onRetryReason: (value: string) => void
  readonly onRetry: () => void
}) {
  const checkpointed = hasExactCandidateCheckpoint(session, candidate)
  const exactRun = Boolean(run && exactVerificationSubject(run, session, candidate))
  const terminal = Boolean(
    run && ['passed', 'failed', 'error', 'cancelled', 'timed_out'].includes(run.run.state),
  )
  const inProgress = Boolean(run && !terminal)
  const canVerify = Boolean(
    canEdit
    && checkpointed
    && session.allowedActions.includes('verify')
    && profiles.some((profile) => verificationProfileKey(profile) === selectedProfileKey)
    && !inProgress
    && !busy
    && !workspaceBusy,
  )
  const canCancel = Boolean(
    run?.allowedActions.includes('cancel') && !busy && !workspaceBusy,
  )
  const canRetry = Boolean(
    run?.allowedActions.includes('retry')
    && retryReason.trim()
    && !busy
    && !workspaceBusy,
  )
  const passed = run?.receiptDecision === 'passed' && run.allowedActions.includes('freeze') && exactRun
  const stateTone = passed
    ? 'text-success'
    : run?.run.state === 'failed' || run?.run.state === 'error'
      ? 'text-destructive'
      : run?.stale
        ? 'text-warning'
        : 'text-muted-foreground'

  return (
    <section className="shrink-0 border-b border-border bg-panel/70" aria-label="Candidate verification">
      <div className="flex min-h-10 flex-wrap items-center gap-2 px-3 py-1.5">
        {passed
          ? <ShieldCheck className="size-3.5 shrink-0 text-success" />
          : <ShieldAlert className={cn('size-3.5 shrink-0', stateTone)} />}
        <div className="min-w-28">
          <div className="text-[9px] font-semibold text-foreground">Exact quality gate</div>
          <div className={cn('font-mono text-[8px]', stateTone)}>
            {run
              ? `${run.run.state}${run.stale || !exactRun ? ' · stale' : ''} · ${run.completedCheckCount}/${run.checkCount} checks`
              : checkpointed ? 'checkpoint ready · not verified' : 'checkpoint required'}
          </div>
        </div>
        <select
          value={selectedProfileKey}
          onChange={(event) => onProfile(event.target.value)}
          disabled={busy || inProgress || profiles.length === 0}
          className="h-7 min-w-48 max-w-72 rounded border border-border bg-background px-2 text-[8px] text-muted-foreground disabled:opacity-40"
          aria-label="Verification profile"
        >
          {profiles.length === 0 && <option value="">No active VerificationProfile</option>}
          {profiles.map((profile) => (
            <option key={verificationProfileKey(profile)} value={verificationProfileKey(profile)}>
              {profile.verificationProfile.id} v{profile.verificationProfile.version}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={onVerify}
          disabled={!canVerify}
          title={!checkpointed
            ? 'Create an exact checkpoint before verification.'
            : !session.allowedActions.includes('verify')
              ? session.blockingReasons.find((reason) => reason.actions.includes('verify'))?.detail
              : profiles.length === 0
                ? 'No platform-qualified active VerificationProfile is available.'
                : 'Compile and execute a deterministic VerificationPlan for this checkpoint.'}
          className="inline-flex h-7 items-center gap-1 rounded border border-primary/30 bg-primary/10 px-2 text-[9px] font-semibold text-primary-bright disabled:opacity-40"
        >
          {busy && !run?.allowedActions.includes('retry')
            ? <LoaderCircle className="size-3 animate-spin" />
            : <ShieldCheck className="size-3" />}
          Verify
        </button>
        {run?.allowedActions.includes('cancel') && (
          <button
            type="button"
            onClick={onCancel}
            disabled={!canCancel}
            className="inline-flex h-7 items-center gap-1 rounded border border-destructive/30 px-2 text-[9px] text-destructive disabled:opacity-40"
          >
            <CircleStop className="size-3" /> Cancel
          </button>
        )}
        {run?.allowedActions.includes('retry') && (
          <>
            <input
              value={retryReason}
              onChange={(event) => onRetryReason(event.target.value)}
              maxLength={1000}
              placeholder="Required retry reason"
              aria-label="Verification retry reason"
              className="h-7 min-w-44 flex-1 rounded border border-border bg-background px-2 text-[8px] text-foreground outline-none"
            />
            <button
              type="button"
              onClick={onRetry}
              disabled={!canRetry}
              className="inline-flex h-7 items-center gap-1 rounded border border-warning/30 px-2 text-[9px] text-warning disabled:opacity-40"
            >
              {busy ? <LoaderCircle className="size-3 animate-spin" /> : <RefreshCw className="size-3" />}
              Retry
            </button>
          </>
        )}
        {run && (
          <details className="ml-auto text-[8px] text-faint-foreground">
            <summary className="cursor-pointer rounded border border-border px-2 py-1.5">
              Evidence
            </summary>
            <div className="absolute right-3 z-30 mt-1 max-h-80 w-[34rem] max-w-[calc(100vw-2rem)] overflow-y-auto rounded-lg border border-border bg-panel p-3 shadow-2xl">
              <dl className="grid gap-1 font-mono text-[8px] sm:grid-cols-2">
                <div><dt className="text-faint-foreground">Run</dt><dd className="truncate text-muted-foreground" title={run.run.id}>{run.run.id}</dd></div>
                <div><dt className="text-faint-foreground">Plan</dt><dd className="truncate text-muted-foreground" title={run.run.plan.contentHash}>{run.run.plan.contentHash}</dd></div>
                <div><dt className="text-faint-foreground">Checkpoint</dt><dd className="truncate text-muted-foreground" title={run.subject.candidateSnapshotId}>{run.subject.candidateSnapshotId}</dd></div>
                <div><dt className="text-faint-foreground">Tree</dt><dd className="truncate text-muted-foreground" title={run.subject.treeHash}>{run.subject.treeHash}</dd></div>
                <div><dt className="text-faint-foreground">Profile</dt><dd className="truncate text-muted-foreground">{run.verificationProfile.id}@{run.verificationProfile.version}</dd></div>
                <div><dt className="text-faint-foreground">Fence</dt><dd className="text-muted-foreground">R{run.run.version} · F{run.run.fenceEpoch}</dd></div>
              </dl>
              <div className="mt-2 grid grid-cols-4 gap-1 text-center">
                <VerificationMetric label="Must" value={`${run.mustPassedCount}/${run.mustCount}`} />
                <VerificationMetric label="Blockers" value={String(run.blockerCount)} />
                <VerificationMetric label="Warnings" value={String(run.warningCount)} />
                <VerificationMetric label="Attempts" value={String(run.attemptCount)} />
              </div>
              {run.blockingReasons.length > 0 && (
                <div className="mt-2 space-y-1">
                  {run.blockingReasons.map((reason) => (
                    <p key={reason.code} className="rounded border border-warning/25 bg-warning/5 p-1.5 text-[8px] leading-relaxed text-warning">
                      {reason.detail}
                    </p>
                  ))}
                </div>
              )}
              {receipt && (
                <div className="mt-2 border-t border-border pt-2">
                  <div className="flex items-center gap-2">
                    <ShieldCheck className="size-3 text-success" />
                    <span className="font-semibold text-success">Immutable Receipt · {receipt.decision}</span>
                    <span className="ml-auto font-mono text-[7px]" title={receipt.payloadHash}>{receipt.id.slice(0, 12)}</span>
                  </div>
                  <div className="mt-2 space-y-1">
                    {receipt.checks.map((check) => (
                      <div key={check.id} className="rounded border border-border bg-background p-1.5">
                        <div className="flex items-center gap-2">
                          <span className={cn(
                            'font-semibold',
                            check.status === 'passed' ? 'text-success' : 'text-destructive',
                          )}>{check.status}</span>
                          <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground" title={check.id}>{check.id}</span>
                          <span>{check.durationMs} ms</span>
                        </div>
                        {check.diagnostics.map((diagnostic) => (
                          <p key={diagnostic.id} className="mt-1 text-[8px] leading-relaxed text-warning">
                            {diagnostic.code}: {diagnostic.message}
                          </p>
                        ))}
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          </details>
        )}
      </div>
      {error && (
        <div role="alert" className="flex items-start gap-2 border-t border-destructive/20 bg-destructive/5 px-3 py-1.5 text-[8px] leading-relaxed text-destructive">
          <AlertTriangle className="mt-0.5 size-3 shrink-0" />
          <span>{error}</span>
        </div>
      )}
    </section>
  )
}

function VerificationMetric({ label, value }: { readonly label: string; readonly value: string }) {
  return (
    <div className="rounded border border-border bg-background p-1.5">
      <div className="font-mono text-[9px] text-foreground">{value}</div>
      <div className="text-[7px] uppercase tracking-wide text-faint-foreground">{label}</div>
    </div>
  )
}

function SandboxToolbar({
  session,
  candidate,
  saveState,
  process,
  services,
  selectedService,
  selectedProfile,
  runtimeBusy,
  lifecycleBusy,
  abandonBusy,
  abandonBlocked,
  checkpointBusy,
  freezeBusy,
  verificationReadyForFreeze,
  onService,
  onProfile,
  onStart,
  onStop,
  onTerminal,
  onSuspend,
  onResume,
  onTerminate,
  onAbandon,
  agentOpen,
  agentBusy,
  agentAvailable,
  onAgent,
  onCheckpoint,
  onFreeze,
  onRetrySave,
}: {
  readonly session: SandboxSessionDto
  readonly candidate: CandidateWorkspaceDto
  readonly saveState: SaveState
  readonly process: SandboxProcessDto | null
  readonly services: SandboxSessionDto['allowedServices']
  readonly selectedService: string
  readonly selectedProfile: string
  readonly runtimeBusy: boolean
  readonly lifecycleBusy: boolean
  readonly abandonBusy: boolean
  readonly abandonBlocked: boolean
  readonly checkpointBusy: boolean
  readonly freezeBusy: boolean
  readonly verificationReadyForFreeze: boolean
  readonly onService: (value: string) => void
  readonly onProfile: (value: string) => void
  readonly onStart: () => void
  readonly onStop: () => void
  readonly onTerminal: () => void
  readonly onSuspend: () => void
  readonly onResume: () => void
  readonly onTerminate: () => void
  readonly onAbandon: () => void
  readonly agentOpen: boolean
  readonly agentBusy: boolean
  readonly agentAvailable: boolean
  readonly onAgent: () => void
  readonly onCheckpoint: () => void
  readonly onFreeze: () => void
  readonly onRetrySave: () => void
}) {
  const profiles = services.find((service) => service.id === selectedService)?.profiles ?? []
  const checkpoint = session.latestCheckpoint
  const checkpointed = Boolean(
    checkpoint
    && checkpoint.candidateId === candidate.id
    && checkpoint.candidateVersion === candidate.version
    && checkpoint.journalSequence === candidate.journalSequence
    && checkpoint.sessionEpoch === session.sessionEpoch
    && checkpoint.writerLeaseEpoch === candidate.writerLeaseEpoch
    && checkpoint.treeHash === candidate.treeHash,
  )
  const canCheckpoint = session.allowedActions.includes('checkpoint')
  const canFreeze = session.allowedActions.includes('freeze') && verificationReadyForFreeze
  const canProcess = session.allowedActions.includes('process')
  const canUseTerminal = session.allowedActions.includes('pty')
  const canUseAgent = session.allowedActions.includes('agent')
  const canSuspend = session.allowedActions.includes('suspend')
  const canResume = session.allowedActions.includes('resume')
  const canTerminate = session.allowedActions.includes('terminate')
  const canAbandon = candidateAbandonEntryAllowed(session, candidate)
  const frozen = candidate.status === 'frozen'
  return (
    <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-b border-border bg-panel px-3 py-1.5">
      <div className="flex min-w-0 items-center gap-2">
        <Server className="size-3.5 text-success" />
        <span className="font-mono text-[9px] text-muted-foreground" title={session.id}>{session.id.slice(0, 8)}</span>
        <span className="rounded bg-success/10 px-1.5 py-0.5 text-[8px] font-semibold uppercase text-success">{session.state}</span>
        <span className={cn(
          'rounded px-1.5 py-0.5 text-[8px] font-semibold uppercase',
          frozen ? 'bg-primary/10 text-primary-bright' : 'bg-white/5 text-faint-foreground',
        )}>{candidate.status}</span>
        <span className="font-mono text-[8px] text-faint-foreground">C{candidate.version} · E{candidate.writerLeaseEpoch}</span>
      </div>
      <div className="ml-auto flex items-center gap-1.5">
        <select value={selectedService} onChange={(event) => onService(event.target.value)} disabled={abandonBusy || !canProcess} className="h-7 max-w-36 rounded border border-border bg-background px-1.5 text-[9px] text-muted-foreground disabled:opacity-40">
          {services.map((service) => <option key={service.id} value={service.id}>{service.id}</option>)}
        </select>
        <select value={selectedProfile} onChange={(event) => onProfile(event.target.value)} disabled={abandonBusy || !canProcess} className="h-7 max-w-28 rounded border border-border bg-background px-1.5 text-[9px] text-muted-foreground disabled:opacity-40">
          {profiles.map((profile) => <option key={profile} value={profile}>{profile}</option>)}
        </select>
        {process && (process.state === 'running' || process.state === 'starting') ? (
          <button type="button" onClick={onStop} disabled={runtimeBusy || agentBusy || abandonBusy || !canProcess} className="inline-flex h-7 items-center gap-1 rounded border border-destructive/30 px-2 text-[9px] text-destructive disabled:opacity-40"><CircleStop className="size-3" /> Stop</button>
        ) : (
          <button type="button" onClick={onStart} disabled={runtimeBusy || agentBusy || abandonBusy || !canProcess || !selectedService || !selectedProfile} className="inline-flex h-7 items-center gap-1 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground disabled:opacity-40">{runtimeBusy ? <LoaderCircle className="size-3 animate-spin" /> : <Play className="size-3" />} Run</button>
        )}
        <button type="button" onClick={onTerminal} disabled={agentBusy || abandonBusy || !canUseTerminal} className="inline-flex h-7 items-center gap-1 rounded border border-border px-2 text-[9px] text-muted-foreground hover:text-foreground disabled:opacity-40"><SquareTerminal className="size-3" /> Terminal</button>
        <button type="button" onClick={onAgent} disabled={abandonBusy || !agentAvailable || !canUseAgent} title={agentAvailable ? 'Open the exact-input Project Agent.' : 'Agent APIs are unavailable in this deployment.'} className={cn('inline-flex h-7 items-center gap-1 rounded border px-2 text-[9px] disabled:opacity-40', agentOpen ? 'border-primary/40 bg-primary/10 text-primary-bright' : 'border-border text-muted-foreground hover:text-foreground')}><Bot className="size-3" /> Agent</button>
        {canResume ? (
          <button type="button" onClick={onResume} disabled={lifecycleBusy || abandonBusy} className="inline-flex h-7 items-center gap-1 rounded border border-success/30 px-2 text-[9px] text-success disabled:opacity-40" title="Resume the durable sandbox from its checkpointed Candidate.">
            {lifecycleBusy ? <LoaderCircle className="size-3 animate-spin" /> : <Play className="size-3" />} Resume
          </button>
        ) : (
          <button type="button" onClick={onSuspend} disabled={lifecycleBusy || abandonBusy || !canSuspend} className="inline-flex h-7 items-center gap-1 rounded border border-border px-2 text-[9px] text-muted-foreground disabled:opacity-40" title={canSuspend ? 'Suspend this sandbox while retaining its durable Candidate.' : 'Create an exact checkpoint before suspending a dirty Candidate.'}>
            {lifecycleBusy ? <LoaderCircle className="size-3 animate-spin" /> : <Pause className="size-3" />} Suspend
          </button>
        )}
        <button type="button" onClick={onTerminate} disabled={lifecycleBusy || abandonBusy || !canTerminate} className="inline-flex size-7 items-center justify-center rounded border border-destructive/30 text-destructive disabled:opacity-40" title={canTerminate ? 'Terminate this sandbox with an auditable reason.' : 'Create an exact checkpoint before terminating a dirty Candidate.'}>
          <Power className="size-3" />
        </button>
        <button
          type="button"
          onClick={onAbandon}
          disabled={abandonBlocked || !canAbandon}
          title={canAbandon
            ? 'Permanently abandon this Candidate and open a clean successor from the same governed inputs.'
            : 'The server does not currently allow this Candidate to be abandoned.'}
          className="inline-flex h-7 items-center gap-1 rounded border border-destructive/30 px-2 text-[9px] text-destructive disabled:opacity-40"
        >
          {abandonBusy ? <LoaderCircle className="size-3 animate-spin" /> : <Trash2 className="size-3" />}
          Abandon
        </button>
        <button
          type="button"
          onClick={onCheckpoint}
          disabled={checkpointBusy || agentBusy || abandonBusy || checkpointed || !canCheckpoint || saveState === 'error' || saveState === 'stale'}
          title={checkpointed ? 'The current exact Candidate tree is checkpointed.' : 'Create a recoverable Candidate checkpoint after autosave.'}
          className="inline-flex h-7 items-center gap-1 rounded border border-border px-2 text-[9px] text-muted-foreground hover:text-foreground disabled:opacity-40"
        >
          {checkpointBusy
            ? <LoaderCircle className="size-3 animate-spin" />
            : checkpointed ? <CheckCircle2 className="size-3 text-success" /> : <Save className="size-3" />}
          {checkpointed ? 'Checkpointed' : 'Checkpoint'}
        </button>
        <button
          type="button"
          onClick={onFreeze}
          disabled={freezeBusy || checkpointBusy || agentBusy || abandonBusy || frozen || !checkpointed || !canFreeze || saveState !== 'saved'}
          title={frozen
            ? 'This Candidate is immutable and its Proposal has already been created.'
            : checkpointed
              ? verificationReadyForFreeze
                ? 'Freeze this exact Candidate and create its reviewable implementation Proposal.'
                : 'Run verification and obtain a fresh passing Receipt before creating a Proposal.'
              : 'Create an exact checkpoint before freezing this Candidate.'}
          className="inline-flex h-7 items-center gap-1 rounded border border-primary/30 bg-primary/10 px-2 text-[9px] font-semibold text-primary-bright disabled:opacity-40"
        >
          {freezeBusy
            ? <LoaderCircle className="size-3 animate-spin" />
            : <PackageCheck className="size-3" />}
          {frozen ? 'Proposal created' : 'Create Proposal'}
        </button>
        <SaveStatus state={saveState} onRetry={onRetrySave} />
      </div>
    </div>
  )
}

function SaveStatus({ state, onRetry }: { readonly state: SaveState; readonly onRetry: () => void }) {
  if (state === 'error') {
    return <button type="button" onClick={onRetry} className="inline-flex h-7 items-center gap-1 rounded bg-destructive/10 px-2 text-[9px] text-destructive"><RefreshCw className="size-3" /> Retry save</button>
  }
  if (state === 'stale') {
    return <span className="inline-flex h-7 items-center gap-1 rounded bg-warning/10 px-2 text-[9px] text-warning"><ShieldAlert className="size-3" /> Stale draft · save blocked</span>
  }
  return (
    <span className={cn('inline-flex h-7 items-center gap-1 px-1.5 text-[9px]', state === 'saved' ? 'text-success' : 'text-warning')}>
      {state === 'saving' ? <LoaderCircle className="size-3 animate-spin" /> : state === 'saved' ? <CheckCircle2 className="size-3" /> : <Save className="size-3" />}
      {state === 'saved' ? 'Saved' : state === 'saving' ? 'Saving' : 'Unsaved'}
    </span>
  )
}

function CandidateSearchPanel({
  query,
  caseSensitive,
  include,
  status,
  result,
  error,
  headResolved,
  headRefreshAllowed,
  mutationBusy,
  onQuery,
  onCaseSensitive,
  onInclude,
  onRefresh,
  onMatch,
}: {
  readonly query: string
  readonly caseSensitive: boolean
  readonly include: string
  readonly status: CandidateSearchStatus
  readonly result: RepositoryCandidateSearchResultDto | null
  readonly error: string | null
  readonly headResolved: boolean
  readonly headRefreshAllowed: boolean
  readonly mutationBusy: boolean
  readonly onQuery: (value: string) => void
  readonly onCaseSensitive: (value: boolean) => void
  readonly onInclude: (value: string) => void
  readonly onRefresh: () => void
  readonly onMatch: (match: RepositoryCandidateSearchMatchDto) => void
}) {
  const pending = status === 'waiting' || status === 'searching' || status === 'refreshing'
  const matchOpenBlocked = !headResolved || mutationBusy || pending
  return (
    <section aria-label="Exact Candidate text search" className="shrink-0 border-b border-border p-2">
      <div className="flex items-center gap-1.5">
        <Search className="size-3 shrink-0 text-primary-bright" />
        <label htmlFor="candidate-literal-search" className="text-[9px] font-semibold text-foreground">
          Literal search
        </label>
        {pending && <LoaderCircle className="ml-auto size-3 animate-spin text-primary-bright" />}
      </div>
      <input
        id="candidate-literal-search"
        type="search"
        value={query}
        onChange={(event) => onQuery(event.target.value)}
        placeholder="Exact text (not regex)"
        autoComplete="off"
        className="mt-1.5 h-7 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground outline-none focus:border-primary/50"
      />
      <div className="mt-1.5 flex items-center gap-2">
        <label className="flex items-center gap-1 text-[8px] text-muted-foreground">
          <input
            type="checkbox"
            checked={caseSensitive}
            onChange={(event) => onCaseSensitive(event.target.checked)}
          />
          Case sensitive
        </label>
        <span className="ml-auto text-[8px] text-faint-foreground">debounced · server exact-head</span>
      </div>
      <input
        aria-label="Candidate search include patterns"
        value={include}
        onChange={(event) => onInclude(event.target.value)}
        placeholder="include globs, comma separated"
        className="mt-1.5 h-7 w-full rounded border border-border bg-background px-2 font-mono text-[8px] text-foreground outline-none focus:border-primary/50"
      />

      {error && (
        <div
          role={status === 'blocked' ? 'alert' : 'status'}
          aria-live={status === 'blocked' ? 'assertive' : 'polite'}
          className="mt-1.5 rounded border border-warning/25 bg-warning/5 p-1.5 text-[8px] leading-3 text-warning"
        >
          {error}
          {headRefreshAllowed && (
            <button
              type="button"
              onClick={onRefresh}
              disabled={status === 'refreshing' || mutationBusy}
              className="mt-1 inline-flex h-5 items-center gap-1 rounded border border-warning/30 px-1.5 text-[8px] disabled:opacity-40"
            >
              <RefreshCw className={cn('size-2.5', status === 'refreshing' && 'animate-spin')} />
              Refresh exact head
            </button>
          )}
        </div>
      )}

      {result && (
        <div className="mt-2 max-h-80 overflow-y-auto rounded border border-border bg-background scrollbar-thin">
          <div className="sticky top-0 z-10 border-b border-border bg-background/95 p-1.5 text-[8px] text-faint-foreground backdrop-blur">
            <div className="flex items-center gap-1">
              <span className="rounded bg-primary/10 px-1 text-primary-bright">exact</span>
              <span className="font-mono" title={result.head.candidateId}>{result.head.candidateId.slice(0, 12)}</span>
              <span className="font-mono">C{result.head.generation}</span>
            </div>
            <div className="mt-0.5 truncate font-mono" title={result.head.rootHash}>{result.head.rootHash}</div>
            <div className="mt-1">
              {result.matches.length} matches · {result.stats.filesScanned} files · {compactByteSize(result.stats.bytesScanned)}
              {' · '}binary skipped {result.stats.binaryFilesSkipped}
              {' · '}{result.caseSensitive ? 'case exact' : 'ASCII folded'}
            </div>
            <div className="mt-0.5" title="Server-enforced Candidate search limits">
              limits: query {result.limits.maxQueryBytes} B · globs {result.limits.maxIncludeGlobs}×{result.limits.maxGlobBytes} B
              {' · '}files {result.limits.maxFiles} · bytes {compactByteSize(result.limits.maxBytes)}
              {' · '}matches {result.limits.maxMatches} · preview {result.limits.maxPreviewBytes} B
            </div>
            {(result.truncated || result.stats.binaryFilesSkipped > 0) && (
              <div role="status" className="mt-1 rounded bg-warning/10 px-1 py-0.5 text-warning">
                {result.truncated ? 'Results hit a server scan or match limit. ' : ''}
                {result.stats.binaryFilesSkipped > 0
                  ? `${result.stats.binaryFilesSkipped} binary file${result.stats.binaryFilesSkipped === 1 ? '' : 's'} skipped.`
                  : ''}
              </div>
            )}
          </div>
          {result.matches.length === 0 ? (
            <p className="p-3 text-center text-[8px] text-faint-foreground">No literal matches in this exact Candidate head.</p>
          ) : result.matches.map((match, index) => (
            <button
              key={`${match.path}:${match.line}:${match.column}:${index}`}
              type="button"
              onClick={() => onMatch(match)}
              disabled={matchOpenBlocked}
              className="block w-full border-b border-border/60 p-1.5 text-left last:border-b-0 hover:bg-primary/5 disabled:cursor-not-allowed disabled:opacity-50"
              title={matchOpenBlocked ? 'Refresh and wait for a stable exact Candidate head before opening.' : `Open exact ${match.path}`}
            >
              <span className="flex min-w-0 items-center gap-1 font-mono text-[8px] text-primary-bright">
                <span className="min-w-0 flex-1 truncate" title={match.path}>{match.path}</span>
                <span className="shrink-0 text-faint-foreground">{match.line}:{match.column}</span>
              </span>
              <span className="mt-0.5 block truncate font-mono text-[8px] text-muted-foreground" title={match.preview}>
                {match.preview}{match.previewTruncated ? '…' : ''}
              </span>
              <span className="mt-0.5 block truncate font-mono text-[7px] text-faint-foreground" title={match.contentHash}>
                {match.contentHash}
              </span>
            </button>
          ))}
        </div>
      )}
    </section>
  )
}

function FileTree({
  files,
  selected,
  newPath,
  canEdit,
  searchQuery,
  searchCaseSensitive,
  searchInclude,
  searchStatus,
  searchResult,
  searchError,
  searchHeadResolved,
  searchHeadRefreshAllowed,
  searchMutationBusy,
  onNewPath,
  onCreate,
  onSelect,
  onSearchQuery,
  onSearchCaseSensitive,
  onSearchInclude,
  onSearchRefresh,
  onSearchMatch,
}: {
  readonly files: readonly RepositoryTreeFileDto[]
  readonly selected?: string
  readonly newPath: string
  readonly canEdit: boolean
  readonly searchQuery: string
  readonly searchCaseSensitive: boolean
  readonly searchInclude: string
  readonly searchStatus: CandidateSearchStatus
  readonly searchResult: RepositoryCandidateSearchResultDto | null
  readonly searchError: string | null
  readonly searchHeadResolved: boolean
  readonly searchHeadRefreshAllowed: boolean
  readonly searchMutationBusy: boolean
  readonly onNewPath: (value: string) => void
  readonly onCreate: () => void
  readonly onSelect: (path: string) => void
  readonly onSearchQuery: (value: string) => void
  readonly onSearchCaseSensitive: (value: boolean) => void
  readonly onSearchInclude: (value: string) => void
  readonly onSearchRefresh: () => void
  readonly onSearchMatch: (match: RepositoryCandidateSearchMatchDto) => void
}) {
  return (
    <aside className="flex w-72 shrink-0 flex-col border-r border-border bg-panel max-md:w-56">
      <div className="flex h-9 items-center gap-2 border-b border-border px-3 text-[9px] font-semibold uppercase tracking-wide text-faint-foreground">
        <Braces className="size-3.5" /> Candidate files
        <span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 font-mono text-[8px]">{files.length}</span>
      </div>
      <CandidateSearchPanel
        query={searchQuery}
        caseSensitive={searchCaseSensitive}
        include={searchInclude}
        status={searchStatus}
        result={searchResult}
        error={searchError}
        headResolved={searchHeadResolved}
        headRefreshAllowed={searchHeadRefreshAllowed}
        mutationBusy={searchMutationBusy}
        onQuery={onSearchQuery}
        onCaseSensitive={onSearchCaseSensitive}
        onInclude={onSearchInclude}
        onRefresh={onSearchRefresh}
        onMatch={onSearchMatch}
      />
      <div className="min-h-0 flex-1 overflow-y-auto p-1.5 scrollbar-thin">
        {files.map((file) => (
          <button key={file.path} type="button" onClick={() => onSelect(file.path)} className={cn('mb-0.5 flex w-full items-center gap-1.5 rounded px-2 py-1.5 text-left text-[9px]', selected === file.path ? 'bg-primary/12 text-primary-bright' : 'text-muted-foreground hover:bg-white/5 hover:text-foreground')}>
            <ChevronRight className="size-2.5 shrink-0 text-faint-foreground" />
            <FileText className="size-3 shrink-0" />
            <span className="truncate" title={file.path}>{file.path}</span>
          </button>
        ))}
      </div>
      {canEdit && (
        <div className="border-t border-border p-2">
          <div className="flex gap-1">
            <input value={newPath} onChange={(event) => onNewPath(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') onCreate() }} placeholder="path/to/file.ts" className="h-7 min-w-0 flex-1 rounded border border-border bg-background px-1.5 font-mono text-[8px] text-foreground outline-none" />
            <button type="button" onClick={onCreate} className="flex size-7 items-center justify-center rounded border border-border text-faint-foreground hover:text-foreground" aria-label="Create file"><FilePlus2 className="size-3" /></button>
          </div>
        </div>
      )}
    </aside>
  )
}

function InspectorPane({
  tab,
  onTab,
  file,
  markers,
}: {
  readonly tab: 'diff' | 'problems'
  readonly onTab: (value: 'diff' | 'problems') => void
  readonly file: OpenFile
  readonly markers: readonly MarkerSummary[]
}) {
  return (
    <section className="h-48 shrink-0 border-t border-border bg-panel">
      <div className="flex h-8 items-center gap-1 border-b border-border px-2">
        <button type="button" onClick={() => onTab('diff')} className={cn('inline-flex h-6 items-center gap-1 rounded px-2 text-[9px]', tab === 'diff' ? 'bg-primary/10 text-primary-bright' : 'text-faint-foreground')}><FileDiff className="size-3" /> Diff</button>
        <button type="button" onClick={() => onTab('problems')} className={cn('inline-flex h-6 items-center gap-1 rounded px-2 text-[9px]', tab === 'problems' ? 'bg-primary/10 text-primary-bright' : 'text-faint-foreground')}><AlertTriangle className="size-3" /> Problems <span className="rounded bg-white/5 px-1">{markers.length}</span></button>
      </div>
      <div className="h-[calc(100%-2rem)]">
        {tab === 'diff' ? (
          <MonacoDiffEditor
            original={file.savedContent}
            modified={file.draft}
            language={fileLanguage(file.path)}
            theme="vs-dark"
            options={{ automaticLayout: true, readOnly: true, renderSideBySide: true, fontSize: 10, minimap: { enabled: false }, scrollBeyondLastLine: false }}
          />
        ) : (
          <div className="h-full overflow-y-auto p-2 scrollbar-thin">
            {markers.length === 0 ? <p className="p-3 text-center text-[9px] text-faint-foreground">No Monaco diagnostics for this model.</p> : markers.map((marker, index) => (
              <div key={`${marker.startLineNumber}:${marker.startColumn}:${index}`} className="mb-1 flex gap-2 rounded border border-border bg-background px-2 py-1.5 text-[9px] text-muted-foreground">
                <AlertTriangle className={cn('mt-0.5 size-3 shrink-0', marker.severity >= 8 ? 'text-destructive' : 'text-warning')} />
                <span className="font-mono text-faint-foreground">{marker.startLineNumber}:{marker.startColumn}</span>
                <span>{marker.message}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

function BinaryFileViewer({ file }: { readonly file: OpenFile }) {
  const download = useCallback(() => {
    const bytes = file.bytes
    if (!bytes) return
    const blob = new Blob([bytes.slice().buffer], { type: 'application/octet-stream' })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = file.path.split('/').pop() || 'candidate-file.bin'
    anchor.click()
    URL.revokeObjectURL(url)
  }, [file])

  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
      <FileText className="size-8 text-faint-foreground" />
      <div>
        <p className="text-xs font-semibold text-foreground">Binary file</p>
        <p className="mt-1 text-[10px] text-faint-foreground">
          This file is read-only in the code editor to preserve its exact bytes.
        </p>
      </div>
      <button
        type="button"
        onClick={download}
        disabled={!file.bytes}
        className="inline-flex h-8 items-center gap-1.5 rounded border border-border px-3 text-[10px] text-muted-foreground hover:text-foreground disabled:opacity-40"
      >
        <Download className="size-3.5" />
        Download {file.bytes?.byteLength ?? 0} bytes
      </button>
    </div>
  )
}

function SandboxPreview({
  preview,
  ports,
  viewport,
  onViewport,
  onRefresh,
  onOpen,
  busy,
}: {
  readonly preview: SandboxPreviewLinkDto | null
  readonly ports: SandboxPortListDto['ports']
  readonly viewport: 'desktop' | 'tablet' | 'mobile'
  readonly onViewport: (value: 'desktop' | 'tablet' | 'mobile') => void
  readonly onRefresh: () => Promise<void>
  readonly onOpen: (portName: string) => void
  readonly busy: boolean
}) {
  const width = viewport === 'desktop' ? '100%' : viewport === 'tablet' ? 768 : 390
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-canvas">
      <div className="flex min-h-10 shrink-0 flex-wrap items-center gap-2 border-b border-border bg-panel px-3 py-1">
        <MonitorPlay className="size-3.5 text-primary-bright" />
        <span className="text-[10px] font-semibold">Isolated Preview</span>
        <button type="button" onClick={() => void onRefresh()} className="rounded p-1 text-faint-foreground hover:text-foreground" title="Probe declared ports"><RefreshCw className="size-3" /></button>
        <div className="flex min-w-0 flex-1 gap-1 overflow-x-auto">
          {ports.map((port) => (
            <button key={port.name} type="button" disabled={!port.previewable || !port.healthy || busy} onClick={() => onOpen(port.name)} className={cn('inline-flex h-7 shrink-0 items-center gap-1 rounded border px-2 text-[8px]', port.healthy ? 'border-success/30 bg-success/10 text-success' : 'border-border text-faint-foreground', 'disabled:opacity-40')}>
              {port.healthy ? <Wifi className="size-2.5" /> : <WifiOff className="size-2.5" />}
              {port.name}:{port.number} · {port.state}
            </button>
          ))}
        </div>
        {(['desktop', 'tablet', 'mobile'] as const).map((value) => (
          <button key={value} type="button" onClick={() => onViewport(value)} className={cn('rounded px-2 py-1 text-[8px]', viewport === value ? 'bg-primary/15 text-primary-bright' : 'text-faint-foreground')}>{value}</button>
        ))}
      </div>
      <div className="flex min-h-0 flex-1 justify-center overflow-auto bg-[#08080a] p-3 scrollbar-thin">
        {preview ? (
          <iframe
            key={preview.url}
            title={`Sandbox preview ${preview.port.name}`}
            src={preview.url}
            sandbox="allow-downloads allow-forms allow-modals allow-popups allow-same-origin allow-scripts"
            referrerPolicy="no-referrer"
            className="h-full rounded-md border border-border bg-white shadow-2xl transition-[width]"
            style={{ width }}
          />
        ) : (
          <div className="m-auto max-w-md rounded-lg border border-dashed border-border bg-panel p-6 text-center">
            <MonitorPlay className="mx-auto mb-3 size-6 text-faint-foreground" />
            <h3 className="text-xs font-semibold">No listening Preview port yet</h3>
            <p className="mt-2 text-[9px] leading-relaxed text-faint-foreground">Run an exact Template profile, wait for a declared HTTP port to become healthy, then open its short-lived capability URL.</p>
          </div>
        )}
      </div>
    </div>
  )
}

function TerminalPane({
  sandbox,
  sessionId,
  terminal,
  onClose,
}: {
  readonly sandbox: SandboxClient
  readonly sessionId: string
  readonly terminal: SandboxTerminalDto | null
  readonly onClose: () => void
}) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const streamRef = useRef<SandboxStreamConnection | null>(null)
  const [connectionState, setConnectionState] = useState('connecting')

  useEffect(() => {
    if (!terminal || !containerRef.current) return
    let disposed = false
    let resizeObserver: ResizeObserver | undefined
    let terminalInstance: import('@xterm/xterm').Terminal | undefined
    let disposeBindings: (() => void) | undefined
    const stream = sandbox.stream(sessionId, ['control', 'pty'])
    streamRef.current = stream
    void Promise.all([
      import('@xterm/xterm'),
      import('@xterm/addon-fit'),
    ]).then(([xterm, addon]) => {
      if (disposed || !containerRef.current) return
      const fit = new addon.FitAddon()
      terminalInstance = new xterm.Terminal({
        cursorBlink: true,
        convertEol: true,
        fontFamily: 'var(--font-mono-code), monospace',
        fontSize: 11,
        lineHeight: 1.2,
        scrollback: 5000,
        theme: { background: '#111114', foreground: '#d6d6da', cursor: '#7c8cff' },
      })
      terminalInstance.loadAddon(fit)
      terminalInstance.open(containerRef.current)
      fit.fit()
      terminalInstance.focus()
      const dataDisposable = terminalInstance.onData((value) => stream.writeTerminal(terminal.id, value))
      const outputDispose = stream.onTerminalOutput((output) => {
        if (output.terminalId === terminal.id) terminalInstance?.write(output.value)
      })
      const stateDispose = stream.onState((state) => setConnectionState(state))
      const errorDispose = stream.onError((cause) => terminalInstance?.writeln(`\r\n[stream] ${cause.message}`))
      stream.attachTerminal(terminal.id)
      stream.connect()
      resizeObserver = new ResizeObserver(() => {
        fit.fit()
        if (terminalInstance && terminalInstance.rows >= 2 && terminalInstance.cols >= 2) {
          try {
            stream.resizeTerminal(terminal.id, terminalInstance.rows, terminalInstance.cols)
          } catch {
            // The reconnect loop will reattach and send the next valid size.
          }
        }
      })
      resizeObserver.observe(containerRef.current)
      disposeBindings = () => {
        dataDisposable.dispose()
        outputDispose()
        stateDispose()
        errorDispose()
      }
    }).catch((cause) => setConnectionState(errorMessage(cause)))
    return () => {
      disposed = true
      disposeBindings?.()
      resizeObserver?.disconnect()
      try {
        stream.detachTerminal(terminal.id)
      } catch {
        // A not-yet-open stream has nothing to detach remotely.
      }
      stream.destroy()
      streamRef.current = null
      terminalInstance?.dispose()
    }
  }, [sandbox, sessionId, terminal])

  return (
    <section className="h-52 shrink-0 border-t border-border bg-background">
      <div className="flex h-8 items-center gap-2 border-b border-border px-2 text-[9px] text-faint-foreground">
        <SquareTerminal className="size-3" /> Terminal
        <span className="font-mono">{terminal?.id.slice(0, 8) ?? 'starting'}</span>
        <span className="ml-auto">{connectionState}</span>
        <button type="button" onClick={onClose} aria-label="Close terminal"><X className="size-3" /></button>
      </div>
      <div ref={containerRef} className="h-[calc(100%-2rem)] p-1" />
    </section>
  )
}

function SandboxGate({
  title,
  description,
  loading,
  action,
  onAction,
}: {
  readonly title: string
  readonly description: string
  readonly loading?: boolean
  readonly action?: string
  readonly onAction?: () => void
}) {
  return (
    <div className="flex h-full items-center justify-center bg-canvas p-6">
      <div className="max-w-lg rounded-xl border border-dashed border-border bg-panel p-7 text-center">
        {loading ? <LoaderCircle className="mx-auto mb-3 size-7 animate-spin text-primary-bright" /> : <Columns3 className="mx-auto mb-3 size-7 text-faint-foreground" />}
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        <p className="mt-2 text-[10px] leading-relaxed text-faint-foreground">{description}</p>
        {action && onAction && <button type="button" onClick={onAction} className="mt-4 inline-flex h-8 items-center gap-1.5 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground"><MonitorPlay className="size-3.5" /> {action}</button>}
      </div>
    </div>
  )
}
