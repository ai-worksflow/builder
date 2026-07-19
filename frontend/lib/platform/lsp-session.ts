import {
  decodeLSPServerBound,
  decodeLSPServerEnvelope,
  isLSPBrowserRequestMethod,
  isMonotonicLSPHeadSuccessor,
  LSP_CLOSE_BINDING_STALE,
  LSP_CLOSE_MESSAGE_MALFORMED,
  LSP_CLOSE_RUNTIME_UNAVAILABLE,
  LSP_ENVELOPE_SCHEMA_VERSION,
  normalizeLSPBrowserRequestParams,
  normalizeLSPDocumentFence,
  normalizeLSPHeadFence,
  sameLSPDocumentFence,
  SandboxLSPError,
  type LSPClientBindDto,
  type LSPConnectionHelloDto,
  type LSPClientEnvelopeKind,
  type LSPDocumentFenceDto,
  type LSPEnvelopePingExpectation,
  type LSPEnvelopeRequestExpectation,
  type LSPProfileIdentityDto,
  type LSPPublishDiagnosticsDto,
  type LSPServerBoundDto,
  type LSPServerEnvelopeDto,
  type LSPServerResponsePayloadDto,
  type LSPTicketDto,
  type LSPTicketHandshakeScopeDto,
  type SandboxHeadFenceDto,
} from './lsp-contract'
import { PlatformLSPClient } from './lsp-client'

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const SOCKET_OPEN = 1

export type ProductionLSPSessionStatus =
  | 'connecting'
  | 'ready'
  | 'stale'
  | 'failed'
  | 'closed'

export type ProductionLSPHandshakePhase =
  | 'ticket-claimed'
  | 'hello-accepted'
  | 'bind-sent'
  | 'bound'

export interface LSPWebSocketLike {
  readonly readyState: number
  binaryType: BinaryType
  onopen: ((event: Event) => void) | null
  onmessage: ((event: { readonly data: unknown }) => void) | null
  onerror: ((event: Event) => void) | null
  onclose: ((event: { readonly code: number; readonly reason: string }) => void) | null
  send(data: string): void
  close(code?: number, reason?: string): void
}

export type LSPWebSocketFactory = (
  url: string,
  subprotocol: string,
) => LSPWebSocketLike

export interface ProductionLSPSessionSnapshot {
  readonly status: ProductionLSPSessionStatus
  readonly handshakePhase: ProductionLSPHandshakePhase
  readonly connectionId: string | null
  readonly bindingId: string | null
  readonly profileId: string
  readonly clientSequence: number
  readonly serverSequence: number
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly documents: readonly LSPDocumentFenceDto[]
  readonly pendingRequestCount: number
  readonly closeCode: number | null
}

export interface ProductionLSPSessionCallbacks {
  readonly onStateChange?: (snapshot: ProductionLSPSessionSnapshot) => void
  readonly onDiagnostics?: (diagnostics: LSPPublishDiagnosticsDto) => void
  readonly onEnvelope?: (envelope: LSPServerEnvelopeDto) => void
}

export interface ConnectProductionLSPSessionOptions {
  readonly client: PlatformLSPClient
  readonly ticket: LSPTicketDto
  readonly profileId: string
  readonly documents: readonly LSPDocumentFenceDto[]
  readonly socketFactory?: LSPWebSocketFactory
  readonly browserOrigin?: string
  readonly messageIdFactory?: () => string
  readonly callbacks?: ProductionLSPSessionCallbacks
}

export interface LSPRequestHandle {
  readonly messageId: string
  readonly response: Promise<LSPServerResponsePayloadDto>
  cancel(): void
}

export interface LSPPingHandle {
  readonly messageId: string
  readonly response: Promise<void>
}

interface PendingRequest extends LSPEnvelopeRequestExpectation {
  readonly resolve: (value: LSPServerResponsePayloadDto) => void
  readonly reject: (error: SandboxLSPError) => void
}

interface PendingPing extends LSPEnvelopePingExpectation {
  readonly resolve: () => void
  readonly reject: (error: SandboxLSPError) => void
}

interface SynchronizedDocument {
  fence: LSPDocumentFenceDto
  languageId: string | null
  textBytes: number
  synced: boolean
}

function defaultSocketFactory(url: string, subprotocol: string): LSPWebSocketLike {
  if (typeof WebSocket === 'undefined') throw new SandboxLSPError('lsp_session_closed')
  return new WebSocket(url, subprotocol) as unknown as LSPWebSocketLike
}

function defaultMessageIdFactory() {
  const value = globalThis.crypto?.randomUUID?.()
  if (!value) throw new SandboxLSPError('lsp_session_closed')
  return value
}

function publicTicketScope(ticket: LSPTicketDto): LSPTicketHandshakeScopeDto {
  // Copy only non-secret fields. The bearer and its URL never become session state.
  return Object.freeze({
    id: ticket.id,
    mode: ticket.mode,
    sandboxHeadFence: normalizeLSPHeadFence(ticket.sandboxHeadFence),
    templateRelease: Object.freeze({ ...ticket.templateRelease }),
    profiles: immutableClone(ticket.profiles),
  })
}

function immutableClone<T>(value: T): T {
  if (Array.isArray(value)) {
    return Object.freeze(value.map((entry) => immutableClone(entry))) as T
  }
  if (value && typeof value === 'object') {
    return Object.freeze(Object.fromEntries(Object.entries(value).map(([key, entry]) => [
      key,
      immutableClone(entry),
    ]))) as T
  }
  return value
}

function sortedDocuments(
  documents: Iterable<SynchronizedDocument>,
): LSPDocumentFenceDto[] {
  return Array.from(documents, (entry) => entry.fence)
    .sort((left, right) => left.modelUri < right.modelUri ? -1 : left.modelUri === right.modelUri ? 0 : 1)
}

function contextFree(
  error: unknown,
  fallback: ConstructorParameters<typeof SandboxLSPError>[0] = 'lsp_session_closed',
) {
  return error instanceof SandboxLSPError ? error : new SandboxLSPError(fallback)
}

function byteLength(value: string) {
  return new TextEncoder().encode(value).byteLength
}

/**
 * Stateful Production LSP v1 browser endpoint. It owns only ephemeral
 * connection/binding state; Candidate CAS and Monaco model ownership remain
 * outside this class.
 */
export class ProductionLSPSession {
  private status: ProductionLSPSessionStatus = 'connecting'
  private handshakePhase: ProductionLSPHandshakePhase = 'ticket-claimed'
  private readonly ticketScope: LSPTicketHandshakeScopeDto
  private readonly profile: LSPProfileIdentityDto
  private readonly documents = new Map<string, SynchronizedDocument>()
  private readonly pending = new Map<string, PendingRequest>()
  private readonly stalePending = new Map<string, LSPEnvelopeRequestExpectation>()
  private readonly pendingPings = new Map<string, PendingPing>()
  private readonly seenMessageIds = new Set<string>()
  private readonly callbacks: ProductionLSPSessionCallbacks
  private readonly messageIdFactory: () => string
  private inbound: Promise<void> = Promise.resolve()
  private hello: LSPConnectionHelloDto | null = null
  private bind: LSPClientBindDto | null = null
  private bound: LSPServerBoundDto | null = null
  private clientSequence = 0
  private serverSequence = 0
  private head: SandboxHeadFenceDto
  private totalTextBytes = 0
  private closeStarted = false
  private closeCode: number | null = null

  private constructor(
    private readonly client: PlatformLSPClient,
    private readonly socket: LSPWebSocketLike,
    ticket: LSPTicketDto,
    profileId: string,
    documents: readonly LSPDocumentFenceDto[],
    messageIdFactory: () => string,
    callbacks: ProductionLSPSessionCallbacks,
  ) {
    this.ticketScope = publicTicketScope(ticket)
    const profile = this.ticketScope.profiles.find((entry) => entry.id === profileId)
    if (!profile) throw new SandboxLSPError('lsp_binding_stale')
    this.profile = profile
    this.head = this.ticketScope.sandboxHeadFence
    this.messageIdFactory = messageIdFactory
    this.callbacks = callbacks
    if (!Array.isArray(documents) || documents.length === 0 ||
      documents.length > profile.effectiveLimits.maxOpenDocuments) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    for (const candidate of documents) {
      const fence = normalizeLSPDocumentFence(candidate, this.head)
      if (this.documents.has(fence.modelUri)) throw new SandboxLSPError('lsp_binding_stale')
      this.documents.set(fence.modelUri, {
        fence,
        languageId: null,
        textBytes: 0,
        synced: false,
      })
    }
    const canonical = sortedDocuments(this.documents.values())
    if (canonical.some((entry, index) => entry.modelUri !== documents[index]?.modelUri)) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    socket.binaryType = 'arraybuffer'
    socket.onmessage = (event) => {
      const value = event.data
      this.inbound = this.inbound
        .then(() => this.receive(value))
        .catch((error: unknown) => this.fail(contextFree(error, 'lsp_message_malformed')))
    }
    socket.onerror = () => this.fail(new SandboxLSPError('lsp_session_closed'))
    socket.onclose = (event) => this.socketClosed(event.code)
    this.notifyState()
  }

  static connect(options: ConnectProductionLSPSessionOptions): ProductionLSPSession {
    const factory = options.socketFactory ?? defaultSocketFactory
    const descriptor = options.client.claimWebSocket(options.ticket, options.browserOrigin)
    let socket: LSPWebSocketLike
    try {
      socket = factory(descriptor.url, descriptor.subprotocol)
    } catch {
      throw new SandboxLSPError('lsp_session_closed')
    }
    try {
      return new ProductionLSPSession(
        options.client,
        socket,
        options.ticket,
        options.profileId,
        options.documents,
        options.messageIdFactory ?? defaultMessageIdFactory,
        options.callbacks ?? {},
      )
    } catch (error) {
      try {
        socket.close(LSP_CLOSE_MESSAGE_MALFORMED, 'lsp_message_malformed')
      } catch {
        // The only useful state is the context-free construction error below.
      }
      throw contextFree(error, 'lsp_message_malformed')
    }
  }

  snapshot(): ProductionLSPSessionSnapshot {
    return Object.freeze({
      status: this.status,
      handshakePhase: this.handshakePhase,
      connectionId: this.bind?.connectionId ?? null,
      bindingId: this.bound?.bindingId ?? null,
      profileId: this.profile.id,
      clientSequence: this.clientSequence,
      serverSequence: this.serverSequence,
      sandboxHeadFence: this.head,
      documents: Object.freeze(sortedDocuments(this.documents.values())),
      pendingRequestCount: this.pending.size,
      closeCode: this.closeCode,
    })
  }

  openDocument(document: LSPDocumentFenceDto, languageId: string, text: string) {
    this.requireReady()
    const fence = normalizeLSPDocumentFence(document, this.head)
    const current = this.documents.get(fence.modelUri)
    const textBytes = byteLength(text)
    if (!current || current.synced || !sameLSPDocumentFence(current.fence, fence) ||
      !this.profile.languageIds.includes(languageId) || textBytes > this.profile.effectiveLimits.maxDocumentBytes ||
      this.totalTextBytes + textBytes > this.profile.effectiveLimits.maxTotalSyncBytes) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    this.sendEnvelope('client.document.open', 'textDocument/didOpen', null, fence, {
      languageId,
      text,
    })
    current.languageId = languageId
    current.textBytes = textBytes
    current.synced = true
    this.totalTextBytes += textBytes
  }

  changeDocument(document: LSPDocumentFenceDto, text: string) {
    this.requireReady()
    const fence = normalizeLSPDocumentFence(document, this.head)
    const current = this.documents.get(fence.modelUri)
    const textBytes = byteLength(text)
    if (!current?.synced || fence.openId !== current.fence.openId ||
      fence.savedContentHash !== current.fence.savedContentHash ||
      fence.modelVersion !== current.fence.modelVersion + 1 ||
      textBytes > this.profile.effectiveLimits.maxDocumentBytes ||
      this.totalTextBytes - current.textBytes + textBytes >
        this.profile.effectiveLimits.maxTotalSyncBytes) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    this.sendEnvelope('client.document.change', 'textDocument/didChange', null, fence, { text })
    this.totalTextBytes = this.totalTextBytes - current.textBytes + textBytes
    current.textBytes = textBytes
    current.fence = fence
  }

  closeDocument(document: LSPDocumentFenceDto) {
    this.requireReady()
    const fence = normalizeLSPDocumentFence(document, this.head)
    const current = this.documents.get(fence.modelUri)
    if (!current?.synced || !sameLSPDocumentFence(current.fence, fence) ||
      Array.from(this.pending.values()).some((entry) => entry.documentFence.modelUri === fence.modelUri)) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    this.sendEnvelope('client.document.close', 'textDocument/didClose', null, fence, {})
    this.totalTextBytes -= current.textBytes
    this.documents.delete(fence.modelUri)
  }

  request(
    method: string,
    document: LSPDocumentFenceDto,
    params: unknown,
  ): LSPRequestHandle {
    this.requireReady()
    if (!isLSPBrowserRequestMethod(method) || !this.bound?.effectiveCapabilities.includes(method) ||
      this.pending.size >= this.profile.effectiveLimits.maxConcurrentRequests) {
      throw new SandboxLSPError('lsp_session_not_ready')
    }
    const fence = normalizeLSPDocumentFence(document, this.head)
    const current = this.documents.get(fence.modelUri)
    if (!current?.synced || !sameLSPDocumentFence(current.fence, fence)) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    const normalized = normalizeLSPBrowserRequestParams(method, params, this.head, fence)
    const messageId = this.nextMessageId()
    let resolve!: (value: LSPServerResponsePayloadDto) => void
    let reject!: (error: SandboxLSPError) => void
    const response = new Promise<LSPServerResponsePayloadDto>((accept, decline) => {
      resolve = accept
      reject = decline
    })
    const pending: PendingRequest = {
      messageId,
      method,
      sandboxHeadFence: this.head,
      documentFence: fence,
      resolve,
      reject,
    }
    this.pending.set(messageId, pending)
    try {
      this.sendEnvelope('client.request', method, null, fence, { params: normalized }, messageId)
    } catch (error) {
      this.pending.delete(messageId)
      reject(contextFree(error))
      throw error
    }
    return Object.freeze({
      messageId,
      response,
      cancel: () => this.cancelRequest(messageId),
    })
  }

  headRebind(
    nextHead: SandboxHeadFenceDto,
    nextDocuments: readonly LSPDocumentFenceDto[],
  ) {
    this.requireReady()
    const head = normalizeLSPHeadFence(nextHead)
    if (!isMonotonicLSPHeadSuccessor(this.head, head) ||
      nextDocuments.length !== this.documents.size || this.pendingPings.size > 0) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    const parsed = nextDocuments.map((entry) => normalizeLSPDocumentFence(entry, head))
    parsed.sort((left, right) => left.modelUri < right.modelUri ? -1 : left.modelUri === right.modelUri ? 0 : 1)
    for (const document of parsed) {
      const current = this.documents.get(document.modelUri)
      if (!current || current.fence.openId !== document.openId ||
        current.fence.modelVersion !== document.modelVersion) {
        throw new SandboxLSPError('lsp_binding_stale')
      }
    }
    this.sendEnvelope('client.headRebind', 'worksflow/headRebind', null, null, {
      documents: parsed,
    }, undefined, head)
    for (const request of this.pending.values()) {
      this.stalePending.set(request.messageId, request)
      request.reject(new SandboxLSPError('lsp_binding_stale'))
    }
    this.pending.clear()
    for (const document of parsed) this.documents.get(document.modelUri)!.fence = document
    this.head = head
    this.notifyState()
  }

  ping(nonce: string): LSPPingHandle {
    this.requireReady()
    if (!nonce || nonce !== nonce.trim() || nonce.length > 128 || /[\r\n\0]/u.test(nonce)) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    const messageId = this.nextMessageId()
    let resolve!: () => void
    let reject!: (error: SandboxLSPError) => void
    const response = new Promise<void>((accept, decline) => {
      resolve = accept
      reject = decline
    })
    this.pendingPings.set(messageId, {
      messageId,
      nonce,
      sandboxHeadFence: this.head,
      resolve,
      reject,
    })
    try {
      this.sendEnvelope('client.ping', 'worksflow/ping', null, null, { nonce }, messageId)
    } catch (error) {
      this.pendingPings.delete(messageId)
      reject(contextFree(error))
      throw error
    }
    return Object.freeze({ messageId, response })
  }

  close() {
    if (this.status === 'closed') return
    this.closeStarted = true
    this.closeCode = 1000
    this.rejectAll(new SandboxLSPError('lsp_session_closed'))
    this.status = 'closed'
    this.notifyState()
    try {
      this.socket.close(1000, 'lsp_client_close')
    } catch {
      // Local state is already terminal.
    }
  }

  private async receive(value: unknown) {
    if (this.status === 'closed' || this.status === 'failed' || this.status === 'stale') return
    if (typeof value !== 'string') throw new SandboxLSPError('lsp_message_malformed')
    if (this.handshakePhase === 'ticket-claimed') {
      const hello = this.client.acceptHello(value, this.ticketScope)
      this.hello = hello
      this.handshakePhase = 'hello-accepted'
      this.notifyState()
      const bind = this.client.createBind(
        this.ticketScope,
        hello,
        this.profile.id,
        sortedDocuments(this.documents.values()),
      )
      this.sendRaw(JSON.stringify(bind))
      this.bind = bind
      this.clientSequence = 1
      this.handshakePhase = 'bind-sent'
      this.notifyState()
      return
    }
    if (this.handshakePhase === 'bind-sent') {
      const bound = await decodeLSPServerBound(value, this.hello!, this.bind!)
      this.bound = bound
      this.head = bound.sandboxHeadFence
      this.serverSequence = 1
      this.handshakePhase = 'bound'
      this.status = 'ready'
      this.notifyState()
      return
    }
    if (this.handshakePhase !== 'bound' || !this.bound) {
      throw new SandboxLSPError('lsp_session_not_ready')
    }
    const envelope = decodeLSPServerEnvelope(value, {
      connectionId: this.bound.connectionId,
      bindingId: this.bound.bindingId,
      sequence: this.serverSequence + 1,
      sandboxHeadFence: this.head,
      documents: sortedDocuments(this.documents.values()),
      pendingRequests: Array.from(this.pending.values()),
      staleRequests: Array.from(this.stalePending.values()),
      pendingPings: Array.from(this.pendingPings.values()),
      seenMessageIds: this.seenMessageIds,
      limits: this.bound.limits,
    })
    this.serverSequence = envelope.sequence
    this.seenMessageIds.add(envelope.messageId)
    this.applyServerEnvelope(envelope)
    this.safeCallback(() => this.callbacks.onEnvelope?.(envelope))
  }

  private applyServerEnvelope(envelope: LSPServerEnvelopeDto) {
    if (envelope.kind === 'server.response') {
      const request = this.pending.get(envelope.replyTo!)
      if (!request) return
      this.pending.delete(request.messageId)
      request.resolve(envelope.payload as LSPServerResponsePayloadDto)
    } else if (envelope.kind === 'server.stale') {
      const request = this.pending.get(envelope.replyTo!)
      this.pending.delete(envelope.replyTo!)
      this.stalePending.delete(envelope.replyTo!)
      request?.reject(new SandboxLSPError('lsp_binding_stale'))
    } else if (envelope.kind === 'server.diagnostics') {
      const payload = envelope.payload as { readonly diagnostics: LSPPublishDiagnosticsDto }
      this.safeCallback(() => this.callbacks.onDiagnostics?.(payload.diagnostics))
    } else if (envelope.kind === 'server.pong') {
      const ping = this.pendingPings.get(envelope.replyTo!)
      this.pendingPings.delete(envelope.replyTo!)
      ping?.resolve()
    }
  }

  private cancelRequest(messageId: string) {
    this.requireReady()
    const request = this.pending.get(messageId)
    if (!request) return
    this.sendEnvelope(
      'client.cancel',
      '$/cancelRequest',
      messageId,
      request.documentFence,
      {},
    )
    this.pending.delete(messageId)
    this.stalePending.set(messageId, request)
    request.reject(new SandboxLSPError('lsp_request_cancelled'))
  }

  private sendEnvelope(
    kind: LSPClientEnvelopeKind,
    method: string,
    replyTo: string | null,
    documentFence: LSPDocumentFenceDto | null,
    payload: unknown,
    suppliedMessageId?: string,
    suppliedHead?: SandboxHeadFenceDto,
  ) {
    this.requireReady()
    if (this.clientSequence >= Number.MAX_SAFE_INTEGER) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    const messageId = suppliedMessageId ?? this.nextMessageId()
    const nextSequence = this.clientSequence + 1
    const frame = JSON.stringify({
      schemaVersion: LSP_ENVELOPE_SCHEMA_VERSION,
      connectionId: this.bound!.connectionId,
      bindingId: this.bound!.bindingId,
      sequence: nextSequence,
      messageId,
      replyTo,
      kind,
      method,
      sandboxHeadFence: suppliedHead ?? this.head,
      documentFence,
      payload,
    })
    if (byteLength(frame) > this.profile.effectiveLimits.maxFrameBytes) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    this.sendRaw(frame)
    this.clientSequence = nextSequence
    this.seenMessageIds.add(messageId)
  }

  private sendRaw(frame: string) {
    if (this.socket.readyState !== SOCKET_OPEN) throw new SandboxLSPError('lsp_session_closed')
    try {
      this.socket.send(frame)
    } catch {
      throw new SandboxLSPError('lsp_session_closed')
    }
  }

  private nextMessageId() {
    const value = this.messageIdFactory()
    if (!UUID_PATTERN.test(value) || this.seenMessageIds.has(value) ||
      value === this.bind?.connectionId || value === this.bound?.bindingId) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    return value
  }

  private requireReady() {
    if (this.status !== 'ready' || this.handshakePhase !== 'bound' || !this.bound) {
      throw new SandboxLSPError(this.status === 'closed' ? 'lsp_session_closed' : 'lsp_session_not_ready')
    }
  }

  private fail(error: SandboxLSPError) {
    if (this.status === 'closed' || this.status === 'failed' || this.status === 'stale') return
    const stale = error.code === 'lsp_binding_stale' ||
      error.code === 'lsp_connection_identity_mismatch'
    this.status = stale ? 'stale' : 'failed'
    const code = stale ? LSP_CLOSE_BINDING_STALE :
      error.code === 'lsp_session_closed' ? LSP_CLOSE_RUNTIME_UNAVAILABLE : LSP_CLOSE_MESSAGE_MALFORMED
    this.closeCode = code
    this.rejectAll(error)
    this.notifyState()
    try {
      this.socket.close(code, stale ? 'lsp_binding_stale' : error.code)
    } catch {
      // State is already fail-closed.
    }
  }

  private socketClosed(code: number) {
    if (this.closeStarted) return
    const stale = code === LSP_CLOSE_BINDING_STALE
    this.closeCode = code
    this.status = stale ? 'stale' : code === 1000 ? 'closed' : 'failed'
    this.rejectAll(new SandboxLSPError(stale ? 'lsp_binding_stale' : 'lsp_session_closed'))
    this.notifyState()
  }

  private rejectAll(error: SandboxLSPError) {
    for (const request of this.pending.values()) request.reject(error)
    for (const ping of this.pendingPings.values()) ping.reject(error)
    this.pending.clear()
    this.stalePending.clear()
    this.pendingPings.clear()
  }

  private notifyState() {
    this.safeCallback(() => this.callbacks.onStateChange?.(this.snapshot()))
  }

  private safeCallback(action: () => void) {
    try {
      action()
    } catch {
      // UI callback failures do not mutate protocol state or expose frames.
    }
  }
}
