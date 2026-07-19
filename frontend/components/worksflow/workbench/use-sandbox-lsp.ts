'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { Monaco, OnMount } from '@monaco-editor/react'
import type {
  CancellationToken,
  IDisposable,
  IPosition,
  editor as MonacoEditorNamespace,
  languages as MonacoLanguageNamespace,
} from 'monaco-editor'
import {
  isMonotonicLSPHeadSuccessor,
  lspTemplateProfileSupportsPath,
  sameLSPHeadFence,
  SandboxLSPError,
  type LSPTemplateProfileDto,
  type LSPServerEnvelopeDto,
} from '@/lib/platform/lsp-contract'
import type { PlatformLSPClient } from '@/lib/platform/lsp-client'
import {
  ProductionLSPMonacoAdapter,
  type ProductionLSPMonacoModel,
  type ProductionLSPMonacoNamespace,
  type ProductionLSPNavigationLocation,
} from '@/lib/platform/lsp-monaco'
import { ProductionLSPSession } from '@/lib/platform/lsp-session'
import {
  parseSafeCompletionResult,
  parseSafeDocumentHighlights,
  parseSafeDocumentSymbols,
  parseSafeHoverResult,
  parseSafeSignatureHelpResult,
  requireExactMonacoRange,
  type SafeMonacoRange,
} from '@/lib/platform/lsp-monaco-providers'
import type { SandboxLSPAdmissionDecision, SandboxLSPAdmission } from './sandbox-lsp-admission'
import type { SandboxLSPUIView } from './sandbox-lsp-status'

interface MonacoMount {
  readonly editor: Parameters<OnMount>[0]
  readonly monaco: Monaco
}

interface ActiveLSPBinding {
  readonly session: ProductionLSPSession
  readonly adapter: ProductionLSPMonacoAdapter
  readonly model: ProductionLSPMonacoModel
  readonly monaco: ProductionLSPMonacoNamespace
  readonly runtime: Monaco
  readonly profile: LSPTemplateProfileDto
  readonly profileId: string
  readonly navigationProviders: IDisposable[]
  readonly navigationRequests: Map<string, PendingNavigationRequest>
  admission: SandboxLSPAdmission
  opened: boolean
  heartbeatTimer: number | null
  heartbeatTimeout: number | null
  heartbeatPending: boolean
}

interface PendingNavigationRequest {
  readonly method: string
  readonly projection: 'navigation' | 'result'
  readonly resolve: (value: unknown) => void
  readonly timeout: number
  readonly cancellation: IDisposable
}

export interface SandboxLSPMarkerSummary {
  readonly severity: number
  readonly message: string
  readonly startLineNumber: number
  readonly startColumn: number
}

export interface UseSandboxLSPOptions {
  readonly client?: PlatformLSPClient
  readonly admission: SandboxLSPAdmissionDecision
  readonly languageId: string
  readonly onRefreshExactHead: () => Promise<void>
  readonly onMarkers: (markers: readonly SandboxLSPMarkerSummary[]) => void
}

function disabledView(detail: string): SandboxLSPUIView {
  return { status: 'disabled', detail, closeCode: null }
}

function sameRelease(left: SandboxLSPAdmission, right: SandboxLSPAdmission) {
  return left.templateRelease.id === right.templateRelease.id &&
    left.templateRelease.contentHash === right.templateRelease.contentHash
}

function profileDiscoveryKey(admission: SandboxLSPAdmission, languageId: string) {
  return [
    admission.templateRelease.id,
    admission.templateRelease.contentHash,
    admission.serviceId,
    admission.path,
    languageId,
  ].join(':')
}

function stopHeartbeat(active: ActiveLSPBinding) {
  if (active.heartbeatTimer !== null) window.clearInterval(active.heartbeatTimer)
  if (active.heartbeatTimeout !== null) window.clearTimeout(active.heartbeatTimeout)
  active.heartbeatTimer = null
  active.heartbeatTimeout = null
  active.heartbeatPending = false
}

function settleNavigation(
  active: ActiveLSPBinding,
  messageId: string,
  value: unknown,
) {
  const pending = active.navigationRequests.get(messageId)
  if (!pending) return false
  active.navigationRequests.delete(messageId)
  window.clearTimeout(pending.timeout)
  pending.cancellation.dispose()
  pending.resolve(value)
  return true
}

function clearNavigation(active: ActiveLSPBinding) {
  for (const provider of active.navigationProviders.splice(0)) provider.dispose()
  for (const messageId of [...active.navigationRequests.keys()]) {
    settleNavigation(active, messageId, undefined)
  }
}

function connectionErrorView(error: unknown): SandboxLSPUIView {
  if (error instanceof SandboxLSPError && error.code === 'lsp_binding_stale') {
    return {
      status: 'stale',
      detail: 'The exact Candidate binding is stale. Refresh authority before reconnecting.',
      closeCode: 4409,
    }
  }
  if (error instanceof SandboxLSPError && error.code === 'lsp_session_closed') {
    return {
      status: 'unavailable',
      detail: 'The isolated language-server runtime is unavailable; editing and autosave still work.',
      closeCode: 4500,
    }
  }
  return {
    status: 'unavailable',
    detail: 'Language intelligence is unavailable or failed strict identity checks; Candidate editing and autosave remain active.',
    closeCode: null,
  }
}

export function projectSandboxLSPDiagnostics(
  active: Pick<ActiveLSPBinding, 'adapter' | 'monaco'>,
  envelope: LSPServerEnvelopeDto,
  onMarkers: (markers: readonly SandboxLSPMarkerSummary[]) => void,
  onUnsafe: () => void,
) {
  if (envelope.kind !== 'server.diagnostics') return false
  try {
    if (!active.adapter.projectDiagnostics(active.monaco, envelope)) return true
  } catch {
    onUnsafe()
    return true
  }
  const payload = envelope.payload as unknown as {
    readonly diagnostics: {
      readonly diagnostics: readonly {
        readonly severity?: number
        readonly message: string
        readonly range: { readonly start: { readonly line: number; readonly character: number } }
      }[]
    }
  }
  onMarkers(payload.diagnostics.diagnostics.map((diagnostic) => ({
    severity: diagnostic.severity === 1
      ? active.monaco.MarkerSeverity.Error
      : diagnostic.severity === 2
        ? active.monaco.MarkerSeverity.Warning
        : diagnostic.severity === 3
          ? active.monaco.MarkerSeverity.Info
          : active.monaco.MarkerSeverity.Hint,
    message: diagnostic.message,
    startLineNumber: diagnostic.range.start.line + 1,
    startColumn: diagnostic.range.start.character + 1,
  })))
  return true
}

export function useSandboxLSP({
  client,
  admission,
  languageId,
  onRefreshExactHead,
  onMarkers,
}: UseSandboxLSPOptions) {
  const [enabled, setEnabled] = useState(false)
  const [profiles, setProfiles] = useState<readonly LSPTemplateProfileDto[]>([])
  const [selectedProfileId, setSelectedProfileId] = useState('')
  const [view, setView] = useState<SandboxLSPUIView>(() =>
    admission.eligible
      ? disabledView('Select an approved profile and explicitly enable LSP.')
      : { status: 'blocked', detail: admission.reason, closeCode: null })
  const [mount, setMount] = useState<MonacoMount | null>(null)
  const [modelGeneration, setModelGeneration] = useState(0)
  const [retryGeneration, setRetryGeneration] = useState(0)
  const [heartbeatGeneration, setHeartbeatGeneration] = useState(0)
  const [retrying, setRetrying] = useState(false)
  const activeRef = useRef<ActiveLSPBinding | null>(null)
  const ticketAbortRef = useRef<AbortController | null>(null)
  const connectionGenerationRef = useRef(0)
  const connectingKeyRef = useRef('')
  const reconnectBlockedRef = useRef(false)
  const openIdsRef = useRef(new WeakMap<object, string>())
  const mountedRef = useRef(true)

  const exactAdmission = admission.eligible ? admission.admission : null
  const discoveryKey = exactAdmission ? profileDiscoveryKey(exactAdmission, languageId) : ''

  const clearActive = useCallback(() => {
    ticketAbortRef.current?.abort()
    ticketAbortRef.current = null
    connectingKeyRef.current = ''
    connectionGenerationRef.current += 1
    const active = activeRef.current
    activeRef.current = null
    if (!active) return
    stopHeartbeat(active)
    clearNavigation(active)
    try {
      if (active.opened && active.session.snapshot().status === 'ready') {
        active.session.closeDocument(active.adapter.fenceFor(active.model))
      }
    } catch {
      // Closing a stale overlay is best-effort; Candidate state is untouched.
    }
    try {
      active.adapter.clearDiagnostics(active.monaco, active.model)
    } catch {
      // Monaco may already have released the previous file model.
    }
    active.session.close()
    onMarkers([])
  }, [onMarkers])

  useEffect(() => {
    if (!client || !exactAdmission) {
      setProfiles([])
      setSelectedProfileId('')
      if (!enabled) {
        setView(exactAdmission
          ? { status: 'unavailable', detail: 'This deployment does not expose the LSP client.', closeCode: null }
          : { status: 'blocked', detail: admission.eligible ? '' : admission.reason, closeCode: null })
      }
      return
    }
    const controller = new AbortController()
    setView((current) => enabled ? current : {
      status: 'discovering',
      detail: 'Loading exact approved language-server profiles…',
      closeCode: null,
    })
    void client.discoverProfiles(exactAdmission.templateRelease, { signal: controller.signal })
      .then((result) => {
        if (controller.signal.aborted || !mountedRef.current) return
        const admitted = result.data.profiles.filter((profile) =>
          profile.serviceId === exactAdmission.serviceId &&
          profile.languageIds.includes(languageId) &&
          lspTemplateProfileSupportsPath(profile, exactAdmission.path))
        setProfiles(admitted)
        setSelectedProfileId((current) => admitted.some((profile) => profile.id === current)
          ? current
          : admitted[0]?.id ?? '')
        if (!enabled) setView(admitted.length > 0
          ? disabledView('Approved profiles loaded. Enabling opens a dedicated read-only LSP binding.')
          : {
              status: 'blocked',
              detail: 'This exact TemplateRelease has no approved profile for the selected service, language, and file.',
              closeCode: null,
            })
      })
      .catch(() => {
        if (controller.signal.aborted || !mountedRef.current) return
        setProfiles([])
        setSelectedProfileId('')
        if (!enabled) setView({
          status: 'unavailable',
          detail: 'Approved profile discovery is unavailable or failed strict identity checks.',
          closeCode: null,
        })
      })
    return () => controller.abort()
  }, [admission, client, discoveryKey, enabled, exactAdmission, languageId])

  const quarantineBinding = useCallback((active: ActiveLSPBinding, closeSession: boolean) => {
    if (activeRef.current !== active) return
    activeRef.current = null
    reconnectBlockedRef.current = true
    connectionGenerationRef.current += 1
    stopHeartbeat(active)
    clearNavigation(active)
    try {
      active.adapter.clearDiagnostics(active.monaco, active.model)
    } catch {
      // Monaco may already have released the previous file model.
    }
    onMarkers([])
    if (closeSession) active.session.close()
  }, [onMarkers])

  const projectEnvelope = useCallback((
    active: ActiveLSPBinding,
    envelope: LSPServerEnvelopeDto,
  ) => {
    if (projectSandboxLSPDiagnostics(active, envelope, onMarkers, () => {
      quarantineBinding(active, true)
      setView({
        status: 'disconnected',
        detail: 'The language server returned a diagnostic range outside the exact Monaco model.',
        closeCode: null,
      })
    })) return
    if (envelope.kind !== 'server.response' || !envelope.replyTo) return
    const pending = active.navigationRequests.get(envelope.replyTo)
    if (!pending || pending.method !== envelope.method) return
    const projected = pending.projection === 'navigation'
      ? active.adapter.projectNavigation(
          envelope,
          active.profile.limits.maxNavigationLocations,
          (locations, model) => {
            if (model === active.model) settleNavigation(active, envelope.replyTo!, locations)
          },
        )
      : active.adapter.projectResult(envelope, (response, model) => {
          if (model === active.model) {
            settleNavigation(
              active,
              envelope.replyTo!,
              response.status === 'ok' ? response.result : undefined,
            )
          }
        })
    if (!projected) settleNavigation(active, envelope.replyTo, undefined)
  }, [onMarkers, quarantineBinding])

  const startHeartbeat = useCallback((active: ActiveLSPBinding) => {
    if (active.heartbeatTimer !== null) return
    const pulse = () => {
      if (activeRef.current !== active || active.heartbeatPending ||
        active.session.snapshot().status !== 'ready') return
      active.heartbeatPending = true
      try {
        const heartbeat = active.session.ping(globalThis.crypto.randomUUID())
        active.heartbeatTimeout = window.setTimeout(() => {
          if (activeRef.current !== active || !active.heartbeatPending) return
          active.heartbeatPending = false
          active.heartbeatTimeout = null
          quarantineBinding(active, true)
          setView({
            status: 'disconnected',
            detail: 'The LSP editor lease heartbeat timed out. Refresh the exact head before reconnecting.',
            closeCode: null,
          })
          setHeartbeatGeneration((value) => value + 1)
        }, 8_000)
        void heartbeat.response
          .catch((error: unknown) => {
            if (activeRef.current === active && mountedRef.current &&
              active.session.snapshot().closeCode !== 1000) {
              setView(connectionErrorView(error))
            }
          })
          .finally(() => {
            if (active.heartbeatTimeout !== null) {
              window.clearTimeout(active.heartbeatTimeout)
              active.heartbeatTimeout = null
            }
            active.heartbeatPending = false
            if (mountedRef.current) setHeartbeatGeneration((value) => value + 1)
          })
      } catch (error) {
        active.heartbeatPending = false
        quarantineBinding(active, true)
        setView(connectionErrorView(error))
        setHeartbeatGeneration((value) => value + 1)
      }
    }
    active.heartbeatTimer = window.setInterval(pulse, 10_000)
  }, [quarantineBinding])

  const startConnection = useCallback(async (
    currentAdmission: SandboxLSPAdmission,
    profile: LSPTemplateProfileDto,
    model: ProductionLSPMonacoModel,
    runtime: Monaco,
  ) => {
    if (!client) return
    const key = [
      profile.id,
      currentAdmission.templateRelease.id,
      currentAdmission.templateRelease.contentHash,
      currentAdmission.modelUri,
      currentAdmission.sandboxHeadFence.version,
      currentAdmission.sandboxHeadFence.treeHash,
      retryGeneration,
    ].join(':')
    if (connectingKeyRef.current === key) return
    clearActive()
    reconnectBlockedRef.current = false
    connectingKeyRef.current = key
    const generation = connectionGenerationRef.current
    const controller = new AbortController()
    let createdActive: ActiveLSPBinding | null = null
    ticketAbortRef.current = controller
    setView({
      status: 'connecting',
      detail: 'Issuing a one-time ticket and binding the exact Candidate model…',
      closeCode: null,
    })
    try {
      const ticket = await client.issueTicket(currentAdmission.sandboxHeadFence.sessionId, {
        mode: 'editor',
        sandboxHeadFence: currentAdmission.sandboxHeadFence,
        templateRelease: currentAdmission.templateRelease,
        profileIds: [profile.id],
      }, { signal: controller.signal })
      if (controller.signal.aborted || generation !== connectionGenerationRef.current ||
        !mountedRef.current) return
      let openId = openIdsRef.current.get(model as object)
      if (!openId) {
        openId = globalThis.crypto.randomUUID()
        openIdsRef.current.set(model as object, openId)
      }
      const adapter = new ProductionLSPMonacoAdapter(
        currentAdmission.sandboxHeadFence,
        profile.id,
        () => openId!,
      )
      const document = adapter.attachModel(
        model,
        languageId,
        currentAdmission.contentHash,
        openId,
      )
      const productionSession = ProductionLSPSession.connect({
        client,
        ticket: ticket.data,
        profileId: profile.id,
        documents: [document],
        callbacks: {
          onStateChange: (snapshot) => {
            const active = activeRef.current
            if (!active || active.session !== productionSession || !mountedRef.current) return
            if (snapshot.status === 'ready' && !active.opened) {
              try {
                active.session.openDocument(
                  active.adapter.fenceFor(active.model),
                  languageId,
                  active.model.getValue(),
                )
                active.opened = true
                startHeartbeat(active)
                setView({
                  status: 'ready',
                  detail: `${profile.serverInfo.name} ${profile.serverInfo.version} is bound to the exact Candidate head.`,
                  closeCode: null,
                })
              } catch (error) {
                quarantineBinding(active, true)
                setView(connectionErrorView(error))
              }
            } else if (snapshot.status === 'stale' || snapshot.closeCode === 4409) {
              quarantineBinding(active, false)
              setView({
                status: 'stale',
                detail: 'The server rejected this exact head. Refresh before reconnecting.',
                closeCode: snapshot.closeCode ?? 4409,
              })
            } else if (snapshot.status === 'failed') {
              quarantineBinding(active, false)
              setView(snapshot.closeCode === 4500
                ? {
                    status: 'unavailable',
                    detail: 'The isolated language-server runtime is unavailable; editing and autosave still work.',
                    closeCode: 4500,
                  }
                : {
                    status: 'disconnected',
                    detail: 'The LSP WebSocket disconnected. No protocol messages are replayed.',
                    closeCode: snapshot.closeCode,
                  })
            } else if (snapshot.status === 'closed') {
              quarantineBinding(active, false)
              setView({
                status: 'disconnected',
                detail: 'The LSP WebSocket closed. No protocol messages are replayed.',
                closeCode: snapshot.closeCode,
              })
            }
          },
          onEnvelope: (envelope) => {
            const active = activeRef.current
            if (active?.session === productionSession) projectEnvelope(active, envelope)
          },
        },
      })
      const monaco = runtime as unknown as ProductionLSPMonacoNamespace
      const active: ActiveLSPBinding = {
        session: productionSession,
        adapter,
        model,
        monaco,
        runtime,
        profile,
        profileId: profile.id,
        navigationProviders: [],
        navigationRequests: new Map(),
        admission: currentAdmission,
        opened: false,
        heartbeatTimer: null,
        heartbeatTimeout: null,
        heartbeatPending: false,
      }
      activeRef.current = active
      createdActive = active
      const browserRequest = (
        method: string,
        params: unknown,
        token: CancellationToken,
        projection: 'navigation' | 'result',
      ): Promise<unknown> => {
        if (activeRef.current !== active || !active.opened ||
          active.session.snapshot().status !== 'ready' || token.isCancellationRequested) {
          return Promise.resolve(undefined)
        }
        let request: ReturnType<ProductionLSPSession['request']>
        try {
          request = active.session.request(method, active.adapter.fenceFor(active.model), params)
        } catch {
          return Promise.resolve(undefined)
        }
        return new Promise<unknown>((resolve) => {
          const cancellation = token.onCancellationRequested(() => {
            try {
              request.cancel()
            } catch {
              // A terminal binding has already made cancellation unnecessary.
            }
            settleNavigation(active, request.messageId, undefined)
          })
          const timeout = window.setTimeout(() => {
            try {
              request.cancel()
            } catch {
              // The server/session timeout remains authoritative.
            }
            settleNavigation(active, request.messageId, undefined)
          }, Math.min(profile.limits.requestTimeoutMillis, 10_000))
          active.navigationRequests.set(request.messageId, {
            method,
            projection,
            resolve,
            timeout,
            cancellation,
          })
          void request.response.catch(() => {
            settleNavigation(active, request.messageId, undefined)
          })
        })
      }
      const navigationProvider = (
        method: string,
        position: IPosition,
        token: CancellationToken,
        includeDeclaration?: boolean,
      ) => browserRequest(method, {
        textDocument: { uri: active.admission.modelUri },
        position: { line: position.lineNumber - 1, character: position.column - 1 },
        ...(method === 'textDocument/references'
          ? { context: { includeDeclaration: includeDeclaration ?? false } }
          : {}),
      }, token, 'navigation').then((value) => {
        const locations = value as readonly ProductionLSPNavigationLocation[] | undefined
        return locations?.map((location) => ({
          uri: runtime.Uri.parse(location.modelUri),
          range: new runtime.Range(
            location.range.startLineNumber,
            location.range.startColumn,
            location.range.endLineNumber,
            location.range.endColumn,
          ),
        }))
      })
      const monacoRange = (
        target: MonacoEditorNamespace.ITextModel,
        value: SafeMonacoRange,
      ) => {
        const requested = new runtime.Range(
          value.startLineNumber,
          value.startColumn,
          value.endLineNumber,
          value.endColumn,
        )
        requireExactMonacoRange(requested, target.validateRange(requested))
        return requested
      }
      if (profile.methods.includes('textDocument/definition')) {
        active.navigationProviders.push(runtime.languages.registerDefinitionProvider(languageId, {
          provideDefinition: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            token: CancellationToken,
          ) => (target as unknown as ProductionLSPMonacoModel) === model
            ? navigationProvider('textDocument/definition', position, token)
            : undefined,
        }))
      }
      if (profile.methods.includes('textDocument/declaration')) {
        active.navigationProviders.push(runtime.languages.registerDeclarationProvider(languageId, {
          provideDeclaration: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            token: CancellationToken,
          ) => (target as unknown as ProductionLSPMonacoModel) === model
            ? navigationProvider('textDocument/declaration', position, token)
            : undefined,
        }))
      }
      if (profile.methods.includes('textDocument/implementation')) {
        active.navigationProviders.push(runtime.languages.registerImplementationProvider(languageId, {
          provideImplementation: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            token: CancellationToken,
          ) => (target as unknown as ProductionLSPMonacoModel) === model
            ? navigationProvider('textDocument/implementation', position, token)
            : undefined,
        }))
      }
      if (profile.methods.includes('textDocument/typeDefinition')) {
        active.navigationProviders.push(runtime.languages.registerTypeDefinitionProvider(languageId, {
          provideTypeDefinition: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            token: CancellationToken,
          ) => (target as unknown as ProductionLSPMonacoModel) === model
            ? navigationProvider('textDocument/typeDefinition', position, token)
            : undefined,
        }))
      }
      if (profile.methods.includes('textDocument/references')) {
        active.navigationProviders.push(runtime.languages.registerReferenceProvider(languageId, {
          provideReferences: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            context: MonacoLanguageNamespace.ReferenceContext,
            token: CancellationToken,
          ) => (target as unknown as ProductionLSPMonacoModel) === model
            ? navigationProvider(
                'textDocument/references',
                position,
                token,
                context.includeDeclaration,
              )
            : undefined,
        }))
      }
      if (profile.methods.includes('textDocument/completion')) {
        active.navigationProviders.push(runtime.languages.registerCompletionItemProvider(languageId, {
          provideCompletionItems: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            context: MonacoLanguageNamespace.CompletionContext,
            token: CancellationToken,
          ) => {
            if ((target as unknown as ProductionLSPMonacoModel) !== model) return undefined
            const word = target.getWordUntilPosition(position)
            const currentRange = {
              start: { line: position.lineNumber - 1, character: word.startColumn - 1 },
              end: { line: position.lineNumber - 1, character: word.endColumn - 1 },
            }
            return browserRequest('textDocument/completion', {
              textDocument: { uri: active.admission.modelUri },
              position: { line: position.lineNumber - 1, character: position.column - 1 },
              context: {
                triggerKind: context.triggerKind + 1,
                ...(context.triggerKind === runtime.languages.CompletionTriggerKind.TriggerCharacter
                  ? { triggerCharacter: context.triggerCharacter }
                  : {}),
              },
            }, token, 'result').then((value) => {
              if (value === undefined) return undefined
              try {
                const result = parseSafeCompletionResult(
                  value,
                  currentRange,
                  profile.limits.maxCompletionItems,
                  profile.limits.maxDocumentBytes,
                )
                return {
                  incomplete: result.incomplete,
                  suggestions: result.suggestions.map((item) => ({
                    ...item,
                    kind: item.kind as MonacoLanguageNamespace.CompletionItemKind,
                    range: monacoRange(target, item.range),
                    documentation: item.documentation
                      ? { ...item.documentation, isTrusted: false, supportHtml: false }
                      : undefined,
                  })),
                }
              } catch {
                quarantineBinding(active, true)
                setView({
                  status: 'disconnected',
                  detail: 'The language server returned an unsafe completion edit.',
                  closeCode: null,
                })
                return undefined
              }
            })
          },
        }))
      }
      if (profile.methods.includes('textDocument/hover')) {
        active.navigationProviders.push(runtime.languages.registerHoverProvider(languageId, {
          provideHover: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            token: CancellationToken,
          ) => {
            if ((target as unknown as ProductionLSPMonacoModel) !== model) return undefined
            return browserRequest('textDocument/hover', {
              textDocument: { uri: active.admission.modelUri },
              position: { line: position.lineNumber - 1, character: position.column - 1 },
            }, token, 'result').then((value) => {
              if (value === undefined) return undefined
              try {
                const result = parseSafeHoverResult(value)
                return result ? {
                  contents: result.contents.map((entry) => ({
                    ...entry,
                    isTrusted: false,
                    supportHtml: false,
                  })),
                  ...(result.range ? { range: monacoRange(target, result.range) } : {}),
                } : undefined
              } catch {
                quarantineBinding(active, true)
                setView({
                  status: 'disconnected',
                  detail: 'The language server returned malformed hover content.',
                  closeCode: null,
                })
                return undefined
              }
            })
          },
        }))
      }
      if (profile.methods.includes('textDocument/signatureHelp')) {
        active.navigationProviders.push(runtime.languages.registerSignatureHelpProvider(languageId, {
          provideSignatureHelp: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            token: CancellationToken,
            context: MonacoLanguageNamespace.SignatureHelpContext,
          ) => {
            if ((target as unknown as ProductionLSPMonacoModel) !== model) return undefined
            return browserRequest('textDocument/signatureHelp', {
              textDocument: { uri: active.admission.modelUri },
              position: { line: position.lineNumber - 1, character: position.column - 1 },
              context: {
                triggerKind: context.triggerKind,
                isRetrigger: context.isRetrigger,
                ...(context.triggerKind === runtime.languages.SignatureHelpTriggerKind.TriggerCharacter
                  ? { triggerCharacter: context.triggerCharacter }
                  : {}),
              },
            }, token, 'result').then((value) => {
              if (value === undefined) return undefined
              try {
                const result = parseSafeSignatureHelpResult(
                  value,
                  profile.limits.maxCompletionItems,
                )
                return result ? {
                  value: {
                    ...result,
                    signatures: result.signatures.map((signature) => ({
                      ...signature,
                      documentation: signature.documentation
                        ? { ...signature.documentation, isTrusted: false, supportHtml: false }
                        : undefined,
                      parameters: signature.parameters.map((parameter) => ({
                        ...parameter,
                        documentation: parameter.documentation
                          ? { ...parameter.documentation, isTrusted: false, supportHtml: false }
                          : undefined,
                      })),
                    })),
                  },
                  dispose: () => undefined,
                } : undefined
              } catch {
                quarantineBinding(active, true)
                setView({
                  status: 'disconnected',
                  detail: 'The language server returned malformed signature help.',
                  closeCode: null,
                })
                return undefined
              }
            })
          },
        }))
      }
      if (profile.methods.includes('textDocument/documentHighlight')) {
        active.navigationProviders.push(runtime.languages.registerDocumentHighlightProvider(languageId, {
          provideDocumentHighlights: (
            target: MonacoEditorNamespace.ITextModel,
            position: IPosition,
            token: CancellationToken,
          ) => {
            if ((target as unknown as ProductionLSPMonacoModel) !== model) return undefined
            return browserRequest('textDocument/documentHighlight', {
              textDocument: { uri: active.admission.modelUri },
              position: { line: position.lineNumber - 1, character: position.column - 1 },
            }, token, 'result').then((value) => {
              if (value === undefined) return undefined
              try {
                return parseSafeDocumentHighlights(
                  value,
                  profile.limits.maxNavigationLocations,
                ).map((entry) => ({
                  range: monacoRange(target, entry.range),
                  ...(entry.kind === undefined
                    ? {}
                    : { kind: entry.kind as MonacoLanguageNamespace.DocumentHighlightKind }),
                }))
              } catch {
                quarantineBinding(active, true)
                setView({
                  status: 'disconnected',
                  detail: 'The language server returned malformed document highlights.',
                  closeCode: null,
                })
                return undefined
              }
            })
          },
        }))
      }
      if (profile.methods.includes('textDocument/documentSymbol')) {
        active.navigationProviders.push(runtime.languages.registerDocumentSymbolProvider(languageId, {
          displayName: profile.serverInfo.name,
          provideDocumentSymbols: (
            target: MonacoEditorNamespace.ITextModel,
            token: CancellationToken,
          ) => {
            if ((target as unknown as ProductionLSPMonacoModel) !== model) return undefined
            const materialize = (
              symbol: ReturnType<typeof parseSafeDocumentSymbols>[number],
            ): MonacoLanguageNamespace.DocumentSymbol => {
              const { children, ...value } = symbol
              return {
                ...value,
                kind: symbol.kind as MonacoLanguageNamespace.SymbolKind,
                tags: [...symbol.tags] as MonacoLanguageNamespace.SymbolTag[],
                range: monacoRange(target, symbol.range),
                selectionRange: monacoRange(target, symbol.selectionRange),
                ...(children
                  ? { children: children.map((child) => materialize(child)) }
                  : {}),
              }
            }
            return browserRequest('textDocument/documentSymbol', {
              textDocument: { uri: active.admission.modelUri },
            }, token, 'result').then((value) => {
              if (value === undefined) return undefined
              try {
                return parseSafeDocumentSymbols(
                  value,
                  profile.limits.maxNavigationLocations,
                ).map((symbol) => materialize(symbol))
              } catch {
                quarantineBinding(active, true)
                setView({
                  status: 'disconnected',
                  detail: 'The language server returned malformed document symbols.',
                  closeCode: null,
                })
                return undefined
              }
            })
          },
        }))
      }
      ticketAbortRef.current = null
      connectingKeyRef.current = ''
    } catch (error) {
      if (controller.signal.aborted || generation !== connectionGenerationRef.current ||
        !mountedRef.current) return
      if (createdActive) quarantineBinding(createdActive, true)
      connectingKeyRef.current = ''
      reconnectBlockedRef.current = true
      setView(connectionErrorView(error))
    }
  }, [
    clearActive,
    client,
    languageId,
    projectEnvelope,
    quarantineBinding,
    retryGeneration,
    startHeartbeat,
  ])

  useEffect(() => {
    if (retrying) return
    if (!enabled) {
      clearActive()
      if (admission.eligible) setView((current) => current.status === 'discovering'
        ? current
        : disabledView('Language intelligence is disabled; Candidate autosave remains authoritative.'))
      return
    }
    if (!exactAdmission) {
      clearActive()
      setView({ status: 'blocked', detail: admission.eligible ? '' : admission.reason, closeCode: null })
      return
    }
    if (reconnectBlockedRef.current) return
    const profile = profiles.find((entry) => entry.id === selectedProfileId)
    const model = mount?.editor.getModel() as unknown as ProductionLSPMonacoModel | null
    if (!profile || !mount || !model || model.uri.toString() !== exactAdmission.modelUri) {
      clearActive()
      setView({
        status: 'blocked',
        detail: profile
          ? 'Waiting for the exact Candidate Monaco model.'
          : 'Select a matching approved language-server profile.',
        closeCode: null,
      })
      return
    }
    const active = activeRef.current
    if (!active) {
      void startConnection(exactAdmission, profile, model, mount.monaco)
      return
    }
    if (active.session.snapshot().status !== 'ready') return
    if (active.profileId !== profile.id || active.model !== model ||
      active.admission.modelUri !== exactAdmission.modelUri) {
      void startConnection(exactAdmission, profile, model, mount.monaco)
      return
    }
    if (!sameRelease(active.admission, exactAdmission)) {
      quarantineBinding(active, true)
      setView({
        status: 'stale',
        detail: 'The exact TemplateRelease changed. Refresh authority before reconnecting.',
        closeCode: 4409,
      })
      return
    }
    if (sameLSPHeadFence(active.admission.sandboxHeadFence, exactAdmission.sandboxHeadFence)) {
      if (active.admission.contentHash !== exactAdmission.contentHash) {
        quarantineBinding(active, true)
        setView({
          status: 'stale',
          detail: 'The saved file hash changed without a matching Candidate head transition.',
          closeCode: 4409,
        })
      }
      return
    }
    if (!isMonotonicLSPHeadSuccessor(
      active.admission.sandboxHeadFence,
      exactAdmission.sandboxHeadFence,
    )) {
      quarantineBinding(active, true)
      setView({
        status: 'stale',
        detail: 'The Candidate head did not advance monotonically. Refresh before reconnecting.',
        closeCode: 4409,
      })
      return
    }
    if (active.heartbeatPending) {
      setView({
        status: 'connecting',
        detail: 'Waiting for the in-flight editor lease heartbeat before rebinding the exact Candidate head…',
        closeCode: null,
      })
      return
    }
    try {
      const document = {
        ...active.adapter.fenceFor(model),
        savedContentHash: exactAdmission.contentHash,
      }
      active.session.headRebind(exactAdmission.sandboxHeadFence, [document])
      active.adapter.rebindHead(exactAdmission.sandboxHeadFence, [document])
      active.admission = exactAdmission
      setView({
        status: 'ready',
        detail: `${profile.serverInfo.name} ${profile.serverInfo.version} rebound after Candidate CAS without reloading Monaco.`,
        closeCode: null,
      })
    } catch {
      quarantineBinding(active, true)
      setView({
        status: 'stale',
        detail: 'The exact Candidate head could not be rebound. Refresh before reconnecting.',
        closeCode: 4409,
      })
    }
  }, [
    admission,
    clearActive,
    enabled,
    exactAdmission,
    heartbeatGeneration,
    modelGeneration,
    mount,
    profiles,
    quarantineBinding,
    retryGeneration,
    retrying,
    selectedProfileId,
    startConnection,
  ])

  useEffect(() => {
    if (!mount) return
    const modelSubscription = mount.editor.onDidChangeModel(() => {
      clearActive()
      setModelGeneration((value) => value + 1)
    })
    const contentSubscription = mount.editor.onDidChangeModelContent(() => {
      const active = activeRef.current
      const model = mount.editor.getModel() as unknown as ProductionLSPMonacoModel | null
      if (!active?.opened || !model || active.model !== model ||
        active.session.snapshot().status !== 'ready') return
      try {
        active.session.changeDocument(active.adapter.fenceFor(model), model.getValue())
      } catch {
        quarantineBinding(active, true)
        setView({
          status: 'stale',
          detail: 'The Monaco overlay no longer matches its exact document fence.',
          closeCode: 4409,
        })
      }
    })
    return () => {
      modelSubscription.dispose()
      contentSubscription.dispose()
    }
  }, [clearActive, mount, quarantineBinding])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      clearActive()
    }
  }, [clearActive])

  const onMount = useCallback<OnMount>((editor, monaco) => {
    setMount({ editor, monaco })
  }, [])

  const enable = useCallback(() => {
    if (!exactAdmission || !selectedProfileId ||
      !profiles.some((profile) => profile.id === selectedProfileId)) return
    reconnectBlockedRef.current = false
    setEnabled(true)
  }, [exactAdmission, profiles, selectedProfileId])

  const disable = useCallback(() => {
    reconnectBlockedRef.current = false
    setEnabled(false)
    clearActive()
    setView(disabledView('Language intelligence is disabled; Candidate autosave remains authoritative.'))
  }, [clearActive])

  const retry = useCallback(() => {
    if (retrying) return
    setRetrying(true)
    clearActive()
    setView({
      status: 'connecting',
      detail: 'Refreshing exact Sandbox/Candidate authority before reconnecting…',
      closeCode: null,
    })
    void onRefreshExactHead()
      .then(() => {
        if (!mountedRef.current) return
        reconnectBlockedRef.current = false
        setRetryGeneration((value) => value + 1)
      })
      .catch(() => {
        if (!mountedRef.current) return
        reconnectBlockedRef.current = true
        setView({
          status: 'unavailable',
          detail: 'The exact Sandbox/Candidate head could not be refreshed.',
          closeCode: null,
        })
      })
      .finally(() => {
        if (!mountedRef.current) return
        setRetrying(false)
      })
  }, [clearActive, onRefreshExactHead, retrying])

  const publicProfiles = exactAdmission ? profiles : []
  const publicView: SandboxLSPUIView = exactAdmission
    ? view
    : { status: 'blocked', detail: admission.eligible ? '' : admission.reason, closeCode: null }

  return useMemo(() => ({
    enabled,
    profiles: publicProfiles,
    selectedProfileId,
    view: publicView,
    onMount,
    setSelectedProfileId,
    enable,
    disable,
    retry,
  }), [
    disable,
    enable,
    enabled,
    onMount,
    publicProfiles,
    publicView,
    retry,
    selectedProfileId,
  ])
}
