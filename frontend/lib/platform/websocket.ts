import type {
  ArtifactDto,
  ArtifactRevisionDto,
  BlueprintContentDto,
  CommentDto,
  DocumentContentDto,
  JsonValue,
  NotificationDto,
  PageSpecContentDto,
  PresenceDto,
  ProblemDetailsDto,
  ProjectDto,
  ProjectMemberDto,
  ProposalDto,
  PrototypeContentDto,
  ReviewDto,
  RunDto,
  RunEventDto,
  TraceLinkDto,
  VersionedArtifactDto,
  WorkflowDto,
} from './dto'

const SOCKET_OPEN = 1

export interface WebSocketLike {
  readonly readyState: number
  binaryType: BinaryType
  onopen: ((event: Event) => void) | null
  onmessage: ((event: MessageEvent) => void) | null
  onerror: ((event: Event) => void) | null
  onclose: ((event: CloseEvent) => void) | null
  send(data: string): void
  close(code?: number, reason?: string): void
}

export type WebSocketFactory = (
  url: string,
  protocols?: string | string[],
) => WebSocketLike

export interface WsAuthDto {
  readonly bearerToken?: string
  readonly sessionId?: string
  readonly csrfToken?: string
}

export interface WsCursorStore {
  get(subscriptionId: string): string | undefined
  set(subscriptionId: string, cursor: string): void
  remove(subscriptionId: string): void
}

export interface WsTimer {
  now(): number
  setTimeout(handler: () => void, delayMs: number): unknown
  clearTimeout(handle: unknown): void
}

export interface PlatformWebSocketOptions {
  readonly url?: string
  readonly protocols?: string | string[]
  readonly webSocketFactory?: WebSocketFactory
  readonly getAuth?: () => WsAuthDto | Promise<WsAuthDto>
  readonly cursorStore?: WsCursorStore
  readonly timer?: WsTimer
  readonly random?: () => number
  readonly reconnectMinDelayMs?: number
  readonly reconnectMaxDelayMs?: number
  readonly reconnectJitter?: number
  readonly authTimeoutMs?: number
  readonly heartbeatIntervalMs?: number
  readonly heartbeatTimeoutMs?: number
  readonly maxMessageBytes?: number
  readonly networkEventTarget?: EventTarget
  readonly isOnline?: () => boolean
  readonly requestIdFactory?: () => string
}

export type WsConnectionState =
  | 'idle'
  | 'connecting'
  | 'authenticating'
  | 'open'
  | 'reconnecting'
  | 'offline'
  | 'closed'

interface DomainEvent<TType extends string, TPayload> {
  readonly id: string
  readonly type: TType
  readonly cursor: string
  readonly subscriptionId: string
  readonly projectId: string
  readonly occurredAt: string
  readonly payload: TPayload
}

export type ProjectProjectionEventType =
  | 'artifact.created'
  | 'artifact.draft_updated'
  | 'artifact.revision_created'
  | 'artifact.revision_approved'
  | 'artifact.member_bindings_replaced'
  | 'dependency.created'
  | 'trace.created'
  | 'manifest.created'
  | 'proposal.created'
  | 'proposal.operation_decided'
  | 'proposal.applied'
  | 'review.submitted'
  | 'review.stale'
  | 'review.decision_recorded'
  | 'workbench.bundle_created'
  | 'implementation.proposal_created'
  | 'implementation.operation_decided'
  | 'implementation.applied'
  | 'deployment.requested'
  | 'deployment.completed'
  | 'deployment.failed'
  | 'deployment.runtime_activation_failed'
  | 'document.downstream_generated'
  | 'document.downstream_generation_failed'
  | 'document.sync_back_proposed'

export type PlatformDomainEvent =
  | DomainEvent<'project.updated', ProjectDto>
  | DomainEvent<'member.updated', ProjectMemberDto>
  | DomainEvent<'artifact.updated', ArtifactDto>
  | DomainEvent<'revision.created', ArtifactRevisionDto<JsonValue>>
  | DomainEvent<'document.updated', VersionedArtifactDto<DocumentContentDto>>
  | DomainEvent<'blueprint.updated', VersionedArtifactDto<BlueprintContentDto>>
  | DomainEvent<'pageSpec.updated', VersionedArtifactDto<PageSpecContentDto>>
  | DomainEvent<'prototype.updated', VersionedArtifactDto<PrototypeContentDto>>
  | DomainEvent<'review.updated', ReviewDto>
  | DomainEvent<'comment.created', CommentDto>
  | DomainEvent<'comment.updated', CommentDto>
  | DomainEvent<'notification.updated', NotificationDto>
  | DomainEvent<'proposal.updated', ProposalDto>
  | DomainEvent<'workflow.updated', WorkflowDto>
  | DomainEvent<'run.updated', RunDto>
  | DomainEvent<'run.event', RunEventDto>
  | DomainEvent<'trace.updated', TraceLinkDto>
  | DomainEvent<'presence.updated', PresenceDto>
  | DomainEvent<ProjectProjectionEventType, Record<string, JsonValue>>

export type WsServerMessageDto =
  | {
      readonly type: 'auth.ack'
      readonly connectionId: string
      readonly heartbeatIntervalMs?: number
    }
  | { readonly type: 'auth.error'; readonly problem: ProblemDetailsDto }
  | { readonly type: 'heartbeat'; readonly sentAt: string }
  | { readonly type: 'subscription.ack'; readonly subscriptionId: string; readonly cursor?: string }
  | { readonly type: 'cursor.reset'; readonly subscriptionId: string }
  | { readonly type: 'event'; readonly event: PlatformDomainEvent }
  | { readonly type: 'error'; readonly problem: ProblemDetailsDto }

export type WsClientMessageDto =
  | {
      readonly type: 'auth'
      readonly requestId: string
      readonly bearerToken?: string
      readonly sessionId?: string
      readonly csrfToken?: string
    }
  | {
      readonly type: 'subscribe'
      readonly requestId: string
      readonly subscriptionId: string
      readonly topic: 'project' | 'artifact' | 'run'
      readonly projectId: string
      readonly artifactId?: string
      readonly runId?: string
      readonly cursor?: string
    }
  | { readonly type: 'unsubscribe'; readonly requestId: string; readonly subscriptionId: string }
  | { readonly type: 'heartbeat'; readonly sentAt: string }
  | { readonly type: 'heartbeat.ack'; readonly sentAt: string }

export class PlatformWebSocketError extends Error {
  readonly code: string
  readonly problem?: ProblemDetailsDto

  constructor(message: string, code = 'platform_websocket_error', problem?: ProblemDetailsDto) {
    super(message)
    this.name = 'PlatformWebSocketError'
    this.code = code
    this.problem = problem
  }
}

class MemoryCursorStore implements WsCursorStore {
  private readonly cursors = new Map<string, string>()

  get(subscriptionId: string) {
    return this.cursors.get(subscriptionId)
  }

  set(subscriptionId: string, cursor: string) {
    this.cursors.set(subscriptionId, cursor)
  }

  remove(subscriptionId: string) {
    this.cursors.delete(subscriptionId)
  }
}

export function createSessionStorageCursorStore(prefix = 'worksflow.platform.cursor'): WsCursorStore {
  const memory = new MemoryCursorStore()
  const key = (subscriptionId: string) => `${prefix}.${subscriptionId}`

  return {
    get(subscriptionId) {
      try {
        return typeof sessionStorage === 'undefined'
          ? memory.get(subscriptionId)
          : sessionStorage.getItem(key(subscriptionId)) ?? undefined
      } catch {
        return memory.get(subscriptionId)
      }
    },
    set(subscriptionId, cursor) {
      memory.set(subscriptionId, cursor)
      try {
        if (typeof sessionStorage !== 'undefined') sessionStorage.setItem(key(subscriptionId), cursor)
      } catch {
        // Browser privacy modes may reject storage. The in-memory cursor remains usable.
      }
    },
    remove(subscriptionId) {
      memory.remove(subscriptionId)
      try {
        if (typeof sessionStorage !== 'undefined') sessionStorage.removeItem(key(subscriptionId))
      } catch {
        // Ignore unavailable storage.
      }
    },
  }
}

const defaultTimer: WsTimer = {
  now: () => Date.now(),
  setTimeout: (handler, delayMs) => setTimeout(handler, delayMs),
  clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
}

function requestId() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `ws_${Date.now().toString(36)}_${Math.random().toString(36).slice(2)}`
}

function configuredUrl() {
  if (typeof process !== 'undefined') {
    const value = process.env.NEXT_PUBLIC_PLATFORM_WS_URL?.trim()
    if (value) return value
  }
  if (typeof window === 'undefined') {
    throw new PlatformWebSocketError(
      'A WebSocket URL is required outside the browser.',
      'platform_websocket_url_required',
    )
  }
  return '/api/platform/v1/ws'
}

export function resolveWebSocketUrl(
  value: string,
  location?: { readonly protocol: string; readonly host: string },
) {
  if (!value.startsWith('/')) return value
  const browserLocation = location ?? (typeof window !== 'undefined' ? window.location : undefined)
  if (!browserLocation) {
    throw new PlatformWebSocketError(
      'A relative WebSocket URL requires a browser location.',
      'platform_websocket_url_required',
    )
  }
  const protocol = browserLocation.protocol === 'https:' ? 'wss:' : 'ws:'
  return protocol + '//' + browserLocation.host + value
}

function defaultFactory(url: string, protocols?: string | string[]) {
  if (typeof WebSocket === 'undefined') {
    throw new PlatformWebSocketError(
      'A WebSocket implementation is required.',
      'platform_websocket_unavailable',
    )
  }
  return new WebSocket(url, protocols) as WebSocketLike
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

const DOMAIN_EVENT_TYPES = new Set<PlatformDomainEvent['type']>([
  'project.updated',
  'member.updated',
  'artifact.updated',
  'revision.created',
  'document.updated',
  'blueprint.updated',
  'pageSpec.updated',
  'prototype.updated',
  'review.updated',
  'comment.created',
  'comment.updated',
  'notification.updated',
  'proposal.updated',
  'workflow.updated',
  'run.updated',
  'run.event',
  'trace.updated',
  'presence.updated',
  'artifact.created',
  'artifact.draft_updated',
  'artifact.revision_created',
  'artifact.revision_approved',
  'artifact.member_bindings_replaced',
  'dependency.created',
  'trace.created',
  'manifest.created',
  'proposal.created',
  'proposal.operation_decided',
  'proposal.applied',
  'review.submitted',
  'review.stale',
  'review.decision_recorded',
  'workbench.bundle_created',
  'implementation.proposal_created',
  'implementation.operation_decided',
  'implementation.applied',
  'deployment.requested',
  'deployment.completed',
  'deployment.failed',
  'deployment.runtime_activation_failed',
  'document.downstream_generated',
  'document.downstream_generation_failed',
  'document.sync_back_proposed',
])

export function isPlatformDomainEvent(value: unknown): value is PlatformDomainEvent {
  if (!isRecord(value)) return false
  return (
    typeof value.id === 'string' &&
    typeof value.type === 'string' &&
    DOMAIN_EVENT_TYPES.has(value.type as PlatformDomainEvent['type']) &&
    typeof value.cursor === 'string' &&
    typeof value.subscriptionId === 'string' &&
    typeof value.projectId === 'string' &&
    typeof value.occurredAt === 'string' &&
    isRecord(value.payload)
  )
}

export function parseWsServerMessage(text: string): WsServerMessageDto {
  let value: unknown
  try {
    value = JSON.parse(text) as unknown
  } catch {
    throw new PlatformWebSocketError('The WebSocket returned malformed JSON.', 'invalid_json')
  }
  if (!isRecord(value) || typeof value.type !== 'string') {
    throw new PlatformWebSocketError('The WebSocket returned an invalid message.', 'invalid_message')
  }

  if (
    value.type === 'auth.ack' &&
    typeof value.connectionId === 'string' &&
    (value.heartbeatIntervalMs === undefined || typeof value.heartbeatIntervalMs === 'number')
  ) return value as unknown as WsServerMessageDto
  if (
    value.type === 'auth.error' &&
    isRecord(value.problem)
  ) return value as unknown as WsServerMessageDto
  if (value.type === 'heartbeat' && typeof value.sentAt === 'string') {
    return value as unknown as WsServerMessageDto
  }
  if (
    value.type === 'subscription.ack' &&
    typeof value.subscriptionId === 'string' &&
    (value.cursor === undefined || typeof value.cursor === 'string')
  ) return value as unknown as WsServerMessageDto
  if (value.type === 'cursor.reset' && typeof value.subscriptionId === 'string') {
    return value as unknown as WsServerMessageDto
  }
  if (value.type === 'event' && isPlatformDomainEvent(value.event)) {
    return value as unknown as WsServerMessageDto
  }
  if (value.type === 'error' && isRecord(value.problem)) {
    return value as unknown as WsServerMessageDto
  }
  throw new PlatformWebSocketError('The WebSocket returned an unknown message.', 'unknown_message')
}

interface SubscriptionRecord {
  readonly id: string
  readonly topic: 'project' | 'artifact' | 'run'
  readonly projectId: string
  readonly artifactId?: string
  readonly runId?: string
  count: number
  readonly listeners: Set<(event: PlatformDomainEvent) => void>
  readonly resetListeners: Set<(subscription: WsSubscriptionReset) => void>
}

export interface WsSubscriptionReset {
  readonly subscriptionId: string
  readonly topic: SubscriptionRecord['topic']
  readonly projectId: string
  readonly artifactId?: string
  readonly runId?: string
}

function subscriptionId(
  topic: SubscriptionRecord['topic'],
  projectId: string,
  resourceId?: string,
) {
  return [topic, projectId, resourceId]
    .filter((value): value is string => Boolean(value))
    .map((value) => encodeURIComponent(value))
    .join(':')
}

function loopbackUrl(value: string) {
  try {
    const url = new URL(value)
    return url.hostname === 'localhost' || url.hostname === '127.0.0.1' || url.hostname === '[::1]'
  } catch {
    return false
  }
}

export class PlatformWebSocketClient {
  private readonly options: Required<Pick<
    PlatformWebSocketOptions,
    | 'reconnectMinDelayMs'
    | 'reconnectMaxDelayMs'
    | 'reconnectJitter'
    | 'authTimeoutMs'
    | 'heartbeatIntervalMs'
    | 'heartbeatTimeoutMs'
    | 'maxMessageBytes'
  >> & PlatformWebSocketOptions
  private readonly webSocketFactory: WebSocketFactory
  private readonly cursorStore: WsCursorStore
  private readonly timer: WsTimer
  private readonly random: () => number
  private readonly requestIdFactory: () => string
  private readonly subscriptions = new Map<string, SubscriptionRecord>()
  private readonly eventListeners = new Set<(event: PlatformDomainEvent) => void>()
  private readonly stateListeners = new Set<(state: WsConnectionState) => void>()
  private readonly errorListeners = new Set<(error: PlatformWebSocketError) => void>()
  private readonly seenEventIds = new Set<string>()
  private readonly seenEventOrder: string[] = []
  private socket?: WebSocketLike
  private stateValue: WsConnectionState = 'idle'
  private shouldRun = false
  private reconnectAttempt = 0
  private reconnectHandle?: unknown
  private authHandle?: unknown
  private heartbeatHandle?: unknown
  private lastMessageAt = 0
  private serverHeartbeatIntervalMs?: number
  private readonly onOnline = () => this.handleOnline()
  private readonly onOffline = () => this.handleOffline()

  constructor(options: PlatformWebSocketOptions = {}) {
    this.options = {
      reconnectMinDelayMs: 500,
      reconnectMaxDelayMs: 30_000,
      reconnectJitter: 0.25,
      authTimeoutMs: 10_000,
      heartbeatIntervalMs: 15_000,
      heartbeatTimeoutMs: 45_000,
      maxMessageBytes: 1_048_576,
      ...options,
    }
    this.webSocketFactory = options.webSocketFactory ?? defaultFactory
    this.cursorStore = options.cursorStore ?? createSessionStorageCursorStore()
    this.timer = options.timer ?? defaultTimer
    this.random = options.random ?? Math.random
    this.requestIdFactory = options.requestIdFactory ?? requestId
    this.networkTarget()?.addEventListener('online', this.onOnline)
    this.networkTarget()?.addEventListener('offline', this.onOffline)
  }

  get state() {
    return this.stateValue
  }

  connect() {
    if (this.shouldRun && ['connecting', 'authenticating', 'open', 'reconnecting'].includes(this.stateValue)) {
      return
    }
    this.shouldRun = true
    if (!this.online()) {
      this.transition('offline')
      return
    }
    this.openSocket(false)
  }

  disconnect() {
    this.shouldRun = false
    this.clearTimers()
    const socket = this.socket
    this.socket = undefined
    if (socket && socket.readyState <= SOCKET_OPEN) socket.close(1000, 'client disconnect')
    this.transition('closed')
  }

  destroy() {
    this.disconnect()
    this.networkTarget()?.removeEventListener('online', this.onOnline)
    this.networkTarget()?.removeEventListener('offline', this.onOffline)
    this.eventListeners.clear()
    this.stateListeners.clear()
    this.errorListeners.clear()
  }

  onEvent(listener: (event: PlatformDomainEvent) => void) {
    this.eventListeners.add(listener)
    return () => this.eventListeners.delete(listener)
  }

  onState(listener: (state: WsConnectionState) => void) {
    this.stateListeners.add(listener)
    return () => this.stateListeners.delete(listener)
  }

  onError(listener: (error: PlatformWebSocketError) => void) {
    this.errorListeners.add(listener)
    return () => this.errorListeners.delete(listener)
  }

  subscribeProject(
    projectId: string,
    listener?: (event: PlatformDomainEvent) => void,
    onReset?: (subscription: WsSubscriptionReset) => void,
  ) {
    return this.subscribe({ topic: 'project', projectId }, listener, onReset)
  }

  subscribeArtifact(
    projectId: string,
    artifactId: string,
    listener?: (event: PlatformDomainEvent) => void,
    onReset?: (subscription: WsSubscriptionReset) => void,
  ) {
    return this.subscribe({ topic: 'artifact', projectId, artifactId }, listener, onReset)
  }

  subscribeRun(
    projectId: string,
    runId: string,
    listener?: (event: PlatformDomainEvent) => void,
    onReset?: (subscription: WsSubscriptionReset) => void,
  ) {
    return this.subscribe({ topic: 'run', projectId, runId }, listener, onReset)
  }

  private subscribe(
    input: Pick<SubscriptionRecord, 'topic' | 'projectId' | 'artifactId' | 'runId'>,
    listener?: (event: PlatformDomainEvent) => void,
    onReset?: (subscription: WsSubscriptionReset) => void,
  ) {
    const resourceId = input.artifactId ?? input.runId
    const id = subscriptionId(input.topic, input.projectId, resourceId)
    let record = this.subscriptions.get(id)
    if (!record) {
      record = {
        ...input,
        id,
        count: 0,
        listeners: new Set(),
        resetListeners: new Set(),
      }
      this.subscriptions.set(id, record)
      if (this.stateValue === 'open') this.sendSubscription(record)
    }
    record.count += 1
    if (listener) record.listeners.add(listener)
    if (onReset) record.resetListeners.add(onReset)

    let active = true
    return () => {
      if (!active) return
      active = false
      const current = this.subscriptions.get(id)
      if (!current) return
      if (listener) current.listeners.delete(listener)
      if (onReset) current.resetListeners.delete(onReset)
      current.count -= 1
      if (current.count > 0) return
      this.subscriptions.delete(id)
      if (this.stateValue === 'open') {
        this.send({ type: 'unsubscribe', requestId: this.requestIdFactory(), subscriptionId: id })
      }
    }
  }

  private openSocket(reconnecting: boolean) {
    this.clearReconnect()
    const url = resolveWebSocketUrl(this.options.url?.trim() || configuredUrl())
    this.transition(reconnecting ? 'reconnecting' : 'connecting')

    let socket: WebSocketLike
    try {
      socket = this.webSocketFactory(url, this.options.protocols ?? 'worksflow.platform.v1')
    } catch (error) {
      this.emitError(
        error instanceof PlatformWebSocketError
          ? error
          : new PlatformWebSocketError(
              error instanceof Error ? error.message : 'Unable to create a WebSocket.',
              'socket_creation_failed',
            ),
      )
      this.scheduleReconnect()
      return
    }
    socket.binaryType = 'arraybuffer'
    this.socket = socket
    socket.onopen = () => void this.authenticate(socket, url)
    socket.onmessage = (event) => void this.handleMessage(event.data)
    socket.onerror = () => {
      this.emitError(new PlatformWebSocketError('The WebSocket connection failed.', 'socket_error'))
    }
    socket.onclose = () => this.handleClose(socket)
  }

  private async authenticate(socket: WebSocketLike, url: string) {
    if (socket !== this.socket || !this.shouldRun) return
    this.transition('authenticating')
    try {
      const auth = await this.options.getAuth?.() ?? {}
      if (auth.bearerToken && url.startsWith('ws://') && !loopbackUrl(url)) {
        throw new PlatformWebSocketError(
          'Bearer authentication requires a secure WebSocket.',
          'insecure_websocket_auth',
        )
      }
      if (socket !== this.socket || socket.readyState !== SOCKET_OPEN) return
      this.send({ type: 'auth', requestId: this.requestIdFactory(), ...auth })
      this.authHandle = this.timer.setTimeout(() => {
        this.emitError(new PlatformWebSocketError('WebSocket authentication timed out.', 'auth_timeout'))
        socket.close(4408, 'authentication timeout')
      }, this.options.authTimeoutMs)
    } catch (error) {
      this.emitError(
        error instanceof PlatformWebSocketError
          ? error
          : new PlatformWebSocketError(
              error instanceof Error ? error.message : 'Unable to authenticate the WebSocket.',
              'auth_failed',
            ),
      )
      socket.close(4401, 'authentication failed')
    }
  }

  private async handleMessage(data: unknown) {
    let text: string
    if (typeof data === 'string') {
      text = data
    } else if (data instanceof ArrayBuffer) {
      if (data.byteLength > this.options.maxMessageBytes) return this.rejectOversizedMessage()
      text = new TextDecoder().decode(data)
    } else if (typeof Blob !== 'undefined' && data instanceof Blob) {
      if (data.size > this.options.maxMessageBytes) return this.rejectOversizedMessage()
      text = await data.text()
    } else {
      this.emitError(new PlatformWebSocketError('Unsupported WebSocket message.', 'invalid_message'))
      return
    }
    if (new TextEncoder().encode(text).byteLength > this.options.maxMessageBytes) {
      return this.rejectOversizedMessage()
    }
    this.lastMessageAt = this.timer.now()

    let message: WsServerMessageDto
    try {
      message = parseWsServerMessage(text)
    } catch (error) {
      this.emitError(error as PlatformWebSocketError)
      return
    }
    this.handleServerMessage(message)
  }

  private handleServerMessage(message: WsServerMessageDto) {
    if (
      this.stateValue === 'authenticating' &&
      !['auth.ack', 'auth.error', 'heartbeat', 'error'].includes(message.type)
    ) {
      this.emitError(new PlatformWebSocketError(
        'The server sent data before authentication completed.',
        'message_before_authentication',
      ))
      this.socket?.close(4400, 'authentication required')
      return
    }
    if (message.type === 'auth.ack') {
      this.clearAuth()
      this.reconnectAttempt = 0
      this.serverHeartbeatIntervalMs = message.heartbeatIntervalMs
      this.transition('open')
      for (const subscription of this.subscriptions.values()) this.sendSubscription(subscription)
      this.scheduleHeartbeat()
      return
    }
    if (message.type === 'auth.error') {
      this.emitError(new PlatformWebSocketError(
        message.problem.detail ?? message.problem.title,
        message.problem.code ?? 'auth_rejected',
        message.problem,
      ))
      this.shouldRun = false
      this.socket?.close(4401, 'authentication rejected')
      return
    }
    if (message.type === 'heartbeat') {
      this.send({ type: 'heartbeat.ack', sentAt: message.sentAt })
      return
    }
    if (message.type === 'cursor.reset') {
      this.cursorStore.remove(message.subscriptionId)
      const subscription = this.subscriptions.get(message.subscriptionId)
      if (subscription) {
        const reset = {
          subscriptionId: subscription.id,
          topic: subscription.topic,
          projectId: subscription.projectId,
          ...(subscription.artifactId ? { artifactId: subscription.artifactId } : {}),
          ...(subscription.runId ? { runId: subscription.runId } : {}),
        } satisfies WsSubscriptionReset
        for (const listener of subscription.resetListeners) listener(reset)
        if (this.stateValue === 'open') this.sendSubscription(subscription)
      }
      return
    }
    if (message.type === 'error') {
      this.emitError(new PlatformWebSocketError(
        message.problem.detail ?? message.problem.title,
        message.problem.code ?? 'server_error',
        message.problem,
      ))
      return
    }
    if (message.type === 'event') this.dispatchEvent(message.event)
  }

  private dispatchEvent(event: PlatformDomainEvent) {
    if (this.seenEventIds.has(event.id)) return
    this.seenEventIds.add(event.id)
    this.seenEventOrder.push(event.id)
    if (this.seenEventOrder.length > 512) {
      const expiredId = this.seenEventOrder.shift()
      if (expiredId) this.seenEventIds.delete(expiredId)
    }
    this.cursorStore.set(event.subscriptionId, event.cursor)
    for (const listener of this.eventListeners) listener(event)
    const subscription = this.subscriptions.get(event.subscriptionId)
    if (subscription) {
      for (const listener of subscription.listeners) listener(event)
    }
  }

  private rejectOversizedMessage() {
    this.emitError(new PlatformWebSocketError('The WebSocket message is too large.', 'message_too_large'))
    this.socket?.close(1009, 'message too large')
  }

  private sendSubscription(subscription: SubscriptionRecord) {
    this.send({
      type: 'subscribe',
      requestId: this.requestIdFactory(),
      subscriptionId: subscription.id,
      topic: subscription.topic,
      projectId: subscription.projectId,
      artifactId: subscription.artifactId,
      runId: subscription.runId,
      cursor: this.cursorStore.get(subscription.id),
    })
  }

  private send(message: WsClientMessageDto) {
    if (!this.socket || this.socket.readyState !== SOCKET_OPEN) return
    this.socket.send(JSON.stringify(message))
  }

  private handleClose(socket: WebSocketLike) {
    if (socket !== this.socket) return
    this.socket = undefined
    this.clearAuth()
    this.clearHeartbeat()
    if (!this.shouldRun) {
      this.transition('closed')
      return
    }
    if (!this.online()) {
      this.transition('offline')
      return
    }
    this.scheduleReconnect()
  }

  private scheduleReconnect() {
    if (!this.shouldRun || this.reconnectHandle !== undefined) return
    const exponential = Math.min(
      this.options.reconnectMaxDelayMs,
      this.options.reconnectMinDelayMs * 2 ** this.reconnectAttempt,
    )
    const jitter = 1 + (this.random() * 2 - 1) * this.options.reconnectJitter
    const delay = Math.max(0, Math.round(exponential * jitter))
    this.reconnectAttempt += 1
    this.transition('reconnecting')
    this.reconnectHandle = this.timer.setTimeout(() => {
      this.reconnectHandle = undefined
      if (this.shouldRun && this.online()) this.openSocket(true)
    }, delay)
  }

  private scheduleHeartbeat() {
    this.clearHeartbeat()
    const interval = this.serverHeartbeatIntervalMs ?? this.options.heartbeatIntervalMs
    this.heartbeatHandle = this.timer.setTimeout(() => {
      this.heartbeatHandle = undefined
      if (this.stateValue !== 'open') return
      if (this.timer.now() - this.lastMessageAt > this.options.heartbeatTimeoutMs) {
        this.emitError(new PlatformWebSocketError('The WebSocket heartbeat timed out.', 'heartbeat_timeout'))
        this.socket?.close(4000, 'heartbeat timeout')
        return
      }
      this.send({ type: 'heartbeat', sentAt: new Date(this.timer.now()).toISOString() })
      this.scheduleHeartbeat()
    }, interval)
  }

  private handleOnline() {
    if (!this.shouldRun || this.socket) return
    this.openSocket(this.stateValue !== 'idle')
  }

  private handleOffline() {
    if (!this.shouldRun) return
    this.clearReconnect()
    this.transition('offline')
    this.socket?.close(4001, 'browser offline')
  }

  private online() {
    if (this.options.isOnline) return this.options.isOnline()
    return typeof navigator === 'undefined' || navigator.onLine !== false
  }

  private networkTarget() {
    if (this.options.networkEventTarget) return this.options.networkEventTarget
    return typeof window === 'undefined' ? undefined : window
  }

  private transition(state: WsConnectionState) {
    if (this.stateValue === state) return
    this.stateValue = state
    for (const listener of this.stateListeners) listener(state)
  }

  private emitError(error: PlatformWebSocketError) {
    for (const listener of this.errorListeners) listener(error)
  }

  private clearReconnect() {
    if (this.reconnectHandle === undefined) return
    this.timer.clearTimeout(this.reconnectHandle)
    this.reconnectHandle = undefined
  }

  private clearAuth() {
    if (this.authHandle === undefined) return
    this.timer.clearTimeout(this.authHandle)
    this.authHandle = undefined
  }

  private clearHeartbeat() {
    if (this.heartbeatHandle === undefined) return
    this.timer.clearTimeout(this.heartbeatHandle)
    this.heartbeatHandle = undefined
  }

  private clearTimers() {
    this.clearReconnect()
    this.clearAuth()
    this.clearHeartbeat()
  }
}
