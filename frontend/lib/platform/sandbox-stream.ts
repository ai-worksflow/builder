import type { ClientMutationOptions } from './clients'
import {
  PlatformWebSocketError,
  type WebSocketFactory,
  type WebSocketLike,
  type WsConnectionState,
  type WsTimer,
} from './websocket'
import type {
  SandboxConnectionCursorDto,
  SandboxConnectionTicketDto,
  SandboxStreamChannel,
} from './sandbox-contract'
import { SANDBOX_STREAM_CHANNELS } from './sandbox-contract'

const SOCKET_OPEN = 1
const STREAM_PROTOCOL = 'worksflow.sandbox.v1'
const STREAM_COMMAND_SCHEMA = 'sandbox-stream-command/v1'
const STREAM_ENVELOPE_SCHEMA = 'sandbox-stream/v1'
const PTY_HEADER_BYTES = 64
const PTY_MAX_PAYLOAD_BYTES = 60 << 10
const PTY_MAGIC = [0x57, 0x46, 0x50, 0x54] as const

const enum PTYFrameType {
  Attach = 0x01,
  Input = 0x02,
  Resize = 0x03,
  Signal = 0x04,
  Detach = 0x05,
  Output = 0x81,
}

export interface SandboxStreamEnvelopeDto<TPayload = unknown> {
  readonly schemaVersion: 'sandbox-stream/v1'
  readonly sessionId: string
  readonly sessionEpoch: number
  readonly channel: SandboxStreamChannel
  readonly eventType: string
  readonly sequence: number
  readonly aggregateVersion: number
  readonly requestId?: string
  readonly correlationId?: string
  readonly timestamp: string
  readonly payload: TPayload
}

export interface SandboxStreamCursorStore {
  get(sessionId: string, sessionEpoch: number, channel: SandboxStreamChannel): number
  set(sessionId: string, sessionEpoch: number, channel: SandboxStreamChannel, sequence: number): void
  clear(sessionId: string): void
}

export interface SandboxStreamOptions {
  readonly webSocketFactory?: WebSocketFactory
  readonly cursorStore?: SandboxStreamCursorStore
  readonly timer?: WsTimer
  readonly reconnectMinDelayMs?: number
  readonly reconnectMaxDelayMs?: number
  readonly reconnectJitter?: number
  readonly heartbeatIntervalMs?: number
  readonly heartbeatTimeoutMs?: number
  readonly maxMessageBytes?: number
  readonly random?: () => number
  readonly requestIdFactory?: () => string
  readonly mutationOptions?: ClientMutationOptions
}

export interface SandboxTerminalOutput {
  readonly terminalId: string
  readonly requestId: string
  readonly sessionEpoch: number
  readonly sequence: number
  readonly ack: number
  readonly value: Uint8Array
}

type TicketIssuer = (
  sessionId: string,
  input: {
    readonly channels: readonly SandboxStreamChannel[]
    readonly cursors: readonly SandboxConnectionCursorDto[]
  },
  options?: ClientMutationOptions,
) => Promise<{ readonly data: SandboxConnectionTicketDto }>

function defaultRequestId() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const bytes = new Uint8Array(16)
  for (let index = 0; index < bytes.length; index += 1) bytes[index] = Math.floor(Math.random() * 256)
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80
  return bytesUuid(bytes)
}

const defaultTimer: WsTimer = {
  now: () => Date.now(),
  setTimeout: (handler, delayMs) => setTimeout(handler, delayMs),
  clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
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

const STREAM_CHANNELS = SANDBOX_STREAM_CHANNELS

const STREAM_CHANNEL_SET = new Set<string>(STREAM_CHANNELS)

function createSessionCursorStore(): SandboxStreamCursorStore {
  const prefix = 'worksflow:sandbox-stream-cursor:'
  const storage = typeof window !== 'undefined' ? window.sessionStorage : undefined
  const memory = new Map<string, number>()
  const key = (sessionId: string, sessionEpoch: number, channel: SandboxStreamChannel) => (
    `${prefix}${sessionId}:${sessionEpoch}:${channel}`
  )
  return {
    get(sessionId, sessionEpoch, channel) {
      if (!Number.isSafeInteger(sessionEpoch) || sessionEpoch < 1) return 0
      const cursorKey = key(sessionId, sessionEpoch, channel)
      const value = storage?.getItem(cursorKey) ?? String(memory.get(cursorKey) ?? '')
      const parsed = Number(value)
      return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : 0
    },
    set(sessionId, sessionEpoch, channel, sequence) {
      if (!Number.isSafeInteger(sessionEpoch) || sessionEpoch < 1 ||
        !Number.isSafeInteger(sequence) || sequence < 0) return
      const cursorKey = key(sessionId, sessionEpoch, channel)
      storage?.setItem(cursorKey, String(sequence))
      memory.set(cursorKey, sequence)
    },
    clear(sessionId) {
      const sessionPrefix = `${prefix}${sessionId}:`
      if (storage) {
        const keys: string[] = []
        for (let index = 0; index < storage.length; index += 1) {
          const value = storage.key(index)
          if (value?.startsWith(sessionPrefix)) keys.push(value)
        }
        for (const value of keys) storage.removeItem(value)
      }
      for (const value of memory.keys()) {
        if (value.startsWith(sessionPrefix)) memory.delete(value)
      }
    },
  }
}

function sameCursors(
  left: readonly SandboxConnectionCursorDto[],
  right: readonly SandboxConnectionCursorDto[],
) {
  return left.length === right.length && left.every((cursor, index) => (
    cursor.channel === right[index]?.channel && cursor.lastAckedSeq === right[index]?.lastAckedSeq
  ))
}

function resolveSandboxStreamUrl(baseUrl: string, path: string, ticket: string) {
  const normalizedBase = baseUrl.replace(/\/+$/, '')
  const normalizedPath = path.replace(/^\/+/, '')
  const joined = `${normalizedBase}/${normalizedPath}`
  let url: URL
  try {
    if (/^https?:\/\//i.test(joined)) {
      url = new URL(joined)
    } else if (typeof window !== 'undefined') {
      url = new URL(joined, window.location.origin)
    } else {
      throw new Error('relative URL outside browser')
    }
  } catch {
    throw new PlatformWebSocketError(
      'A valid sandbox WebSocket URL is required.',
      'sandbox_websocket_url_required',
    )
  }
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  url.searchParams.set('ticket', ticket)
  return url.toString()
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function parseEnvelope(value: unknown): SandboxStreamEnvelopeDto {
  if (!isRecord(value)) throw new PlatformWebSocketError('Invalid sandbox stream frame.', 'invalid_message')
  const required = new Set([
    'schemaVersion', 'sessionId', 'sessionEpoch', 'channel', 'eventType', 'sequence',
    'aggregateVersion', 'timestamp', 'payload',
  ])
  const optional = new Set(['requestId', 'correlationId'])
  if (Object.keys(value).some((key) => !required.has(key) && !optional.has(key)) ||
    [...required].some((key) => !Object.prototype.hasOwnProperty.call(value, key))) {
    throw new PlatformWebSocketError('Sandbox stream envelope shape is not exact.', 'invalid_message')
  }
  const sequence = value.sequence
  const epoch = value.sessionEpoch
  const aggregateVersion = value.aggregateVersion
  if (
    value.schemaVersion !== STREAM_ENVELOPE_SCHEMA || typeof value.sessionId !== 'string' ||
    typeof value.channel !== 'string' || !STREAM_CHANNEL_SET.has(value.channel) ||
    typeof value.eventType !== 'string' || !/^[a-z][a-z0-9_.-]{0,79}$/.test(value.eventType) ||
    !Number.isSafeInteger(sequence) || (sequence as number) < 1 ||
    !Number.isSafeInteger(epoch) || (epoch as number) < 1 ||
    !Number.isSafeInteger(aggregateVersion) || (aggregateVersion as number) < 0 ||
    typeof value.timestamp !== 'string' || !Number.isFinite(Date.parse(value.timestamp)) ||
    !isRecord(value.payload) ||
    (Object.prototype.hasOwnProperty.call(value, 'requestId') && (
      typeof value.requestId !== 'string' || value.requestId.length === 0 || value.requestId !== value.requestId.trim()
    )) ||
    (Object.prototype.hasOwnProperty.call(value, 'correlationId') && (
      typeof value.correlationId !== 'string' || value.correlationId.length === 0 ||
      value.correlationId !== value.correlationId.trim()
    ))
  ) {
    throw new PlatformWebSocketError('Invalid sandbox stream envelope.', 'invalid_message')
  }
  return value as unknown as SandboxStreamEnvelopeDto
}

async function messageText(data: unknown, maxBytes: number) {
  if (typeof data === 'string') {
    if (new TextEncoder().encode(data).byteLength > maxBytes) throw new Error('oversized')
    return data
  }
  throw new Error('unsupported')
}

async function messageBytes(data: unknown, maxBytes: number) {
  if (data instanceof ArrayBuffer) {
    if (data.byteLength > maxBytes) throw new Error('oversized')
    return new Uint8Array(data)
  }
  if (typeof Blob !== 'undefined' && data instanceof Blob) {
    if (data.size > maxBytes) throw new Error('oversized')
    return new Uint8Array(await data.arrayBuffer())
  }
  if (ArrayBuffer.isView(data)) {
    if (data.byteLength > maxBytes) throw new Error('oversized')
    return new Uint8Array(data.buffer, data.byteOffset, data.byteLength)
  }
  throw new Error('unsupported')
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

function uuidBytes(value: string) {
  if (!UUID_PATTERN.test(value)) throw new Error('invalid UUID')
  const hex = value.replaceAll('-', '')
  const bytes = new Uint8Array(16)
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(hex.slice(index * 2, index * 2 + 2), 16)
  }
  return bytes
}

function bytesUuid(value: Uint8Array) {
  const hex = Array.from(value, (byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

function writeSafeInteger(view: DataView, offset: number, value: number) {
  if (!Number.isSafeInteger(value) || value < 0) throw new Error('unsafe integer')
  view.setUint32(offset, Math.floor(value / 0x1_0000_0000))
  view.setUint32(offset + 4, value >>> 0)
}

function readSafeInteger(view: DataView, offset: number) {
  const value = view.getUint32(offset) * 0x1_0000_0000 + view.getUint32(offset + 4)
  if (!Number.isSafeInteger(value)) throw new Error('unsafe integer')
  return value
}

function encodePTYFrame(input: {
  type: PTYFrameType
  sessionEpoch: number
  sequence: number
  ack: number
  terminalId: string
  requestId: string
  payload?: Uint8Array
}) {
  const payload = input.payload ?? new Uint8Array()
  if (payload.byteLength > PTY_MAX_PAYLOAD_BYTES) throw new Error('oversized PTY payload')
  const value = new Uint8Array(PTY_HEADER_BYTES + payload.byteLength)
  value.set(PTY_MAGIC, 0)
  value[4] = 1
  value[5] = input.type
  const view = new DataView(value.buffer)
  view.setUint16(6, PTY_HEADER_BYTES)
  writeSafeInteger(view, 8, input.sessionEpoch)
  writeSafeInteger(view, 16, input.sequence)
  writeSafeInteger(view, 24, input.ack)
  value.set(uuidBytes(input.terminalId), 32)
  value.set(uuidBytes(input.requestId), 48)
  value.set(payload, PTY_HEADER_BYTES)
  return value.buffer
}

function decodePTYOutput(value: Uint8Array): SandboxTerminalOutput {
  if (value.byteLength < PTY_HEADER_BYTES || value.byteLength > PTY_HEADER_BYTES + PTY_MAX_PAYLOAD_BYTES ||
    PTY_MAGIC.some((byte, index) => value[index] !== byte) || value[4] !== 1 || value[5] !== PTYFrameType.Output) {
    throw new Error('invalid PTY frame')
  }
  const view = new DataView(value.buffer, value.byteOffset, value.byteLength)
  if (view.getUint16(6) !== PTY_HEADER_BYTES) throw new Error('invalid PTY header')
  const terminalId = bytesUuid(value.subarray(32, 48))
  const requestId = bytesUuid(value.subarray(48, 64))
  if (!UUID_PATTERN.test(terminalId) || !UUID_PATTERN.test(requestId)) throw new Error('invalid PTY UUID')
  return {
    terminalId, requestId,
    sessionEpoch: readSafeInteger(view, 8), sequence: readSafeInteger(view, 16),
    ack: readSafeInteger(view, 24), value: value.slice(PTY_HEADER_BYTES),
  }
}

export class SandboxStreamConnection {
  private readonly options: Required<Pick<
    SandboxStreamOptions,
    | 'reconnectMinDelayMs'
    | 'reconnectMaxDelayMs'
    | 'reconnectJitter'
    | 'heartbeatIntervalMs'
    | 'heartbeatTimeoutMs'
    | 'maxMessageBytes'
  >> & SandboxStreamOptions
  private readonly webSocketFactory: WebSocketFactory
  private readonly cursorStore: SandboxStreamCursorStore
  private readonly timer: WsTimer
  private readonly random: () => number
  private readonly requestIdFactory: () => string
  private readonly eventListeners = new Set<(event: SandboxStreamEnvelopeDto) => void>()
  private readonly resetListeners = new Set<(event: SandboxStreamEnvelopeDto) => void>()
  private readonly stateListeners = new Set<(state: WsConnectionState) => void>()
  private readonly errorListeners = new Set<(error: PlatformWebSocketError) => void>()
  private readonly terminalListeners = new Set<(output: SandboxTerminalOutput) => void>()
  private readonly terminalAttachments = new Set<string>()
  private readonly terminalSequences = new Map<string, number>()
  private socket?: WebSocketLike
  private stateValue: WsConnectionState = 'idle'
  private shouldRun = false
  private reconnectAttempt = 0
  private reconnectHandle?: unknown
  private heartbeatHandle?: unknown
  private lastMessageAt = 0
  private currentEpoch = 0
  private lifecycleGeneration = 0
  private openGeneration = 0

  constructor(
    private readonly sessionId: string,
    private readonly channels: readonly SandboxStreamChannel[],
    private readonly platformBaseUrl: string,
    private readonly issueTicket: TicketIssuer,
    options: SandboxStreamOptions = {},
  ) {
    if (!sessionId.trim() || channels.length === 0 || new Set(channels).size !== channels.length ||
      channels.some((channel) => !STREAM_CHANNEL_SET.has(channel))) {
      throw new PlatformWebSocketError('Sandbox stream scope is invalid.', 'invalid_sandbox_stream_scope')
    }
    const requested = new Set(channels)
    this.channels = STREAM_CHANNELS.filter((channel) => requested.has(channel))
    this.options = {
      reconnectMinDelayMs: 500, reconnectMaxDelayMs: 30_000, reconnectJitter: 0.25,
      heartbeatIntervalMs: 15_000, heartbeatTimeoutMs: 45_000, maxMessageBytes: 1_048_576,
      ...options,
    }
    this.webSocketFactory = options.webSocketFactory ?? defaultFactory
    this.cursorStore = options.cursorStore ?? createSessionCursorStore()
    this.timer = options.timer ?? defaultTimer
    this.random = options.random ?? Math.random
    this.requestIdFactory = options.requestIdFactory ?? defaultRequestId
  }

  get state() {
    return this.stateValue
  }

  connect() {
    if (this.shouldRun && ['connecting', 'open', 'reconnecting'].includes(this.stateValue)) return
    this.shouldRun = true
    void this.openSocket(false, this.lifecycleGeneration)
  }

  disconnect() {
    this.shouldRun = false
    this.lifecycleGeneration += 1
    this.openGeneration += 1
    this.clearReconnect()
    this.clearHeartbeat()
    const socket = this.socket
    this.socket = undefined
    if (socket && socket.readyState <= SOCKET_OPEN) socket.close(1000, 'client disconnect')
    this.transition('closed')
  }

  destroy() {
    this.disconnect()
    this.eventListeners.clear()
    this.resetListeners.clear()
    this.stateListeners.clear()
    this.errorListeners.clear()
    this.terminalListeners.clear()
    this.terminalAttachments.clear()
    this.terminalSequences.clear()
  }

  onEvent(listener: (event: SandboxStreamEnvelopeDto) => void) {
    this.eventListeners.add(listener)
    return () => this.eventListeners.delete(listener)
  }

  onReset(listener: (event: SandboxStreamEnvelopeDto) => void) {
    this.resetListeners.add(listener)
    return () => this.resetListeners.delete(listener)
  }

  onState(listener: (state: WsConnectionState) => void) {
    this.stateListeners.add(listener)
    return () => this.stateListeners.delete(listener)
  }

  onError(listener: (error: PlatformWebSocketError) => void) {
    this.errorListeners.add(listener)
    return () => this.errorListeners.delete(listener)
  }

  onTerminalOutput(listener: (output: SandboxTerminalOutput) => void) {
    this.terminalListeners.add(listener)
    return () => this.terminalListeners.delete(listener)
  }

  attachTerminal(terminalId: string) {
    this.requireTerminal(terminalId)
    this.terminalAttachments.add(terminalId)
    if (this.stateValue === 'open') this.sendPTY(PTYFrameType.Attach, terminalId)
  }

  writeTerminal(terminalId: string, value: string | Uint8Array) {
    this.requireAttachedTerminal(terminalId)
    const bytes = typeof value === 'string' ? new TextEncoder().encode(value) : value
    if (bytes.byteLength === 0) return
    for (let offset = 0; offset < bytes.byteLength; offset += PTY_MAX_PAYLOAD_BYTES) {
      this.sendPTY(PTYFrameType.Input, terminalId, bytes.slice(offset, offset + PTY_MAX_PAYLOAD_BYTES))
    }
  }

  resizeTerminal(terminalId: string, rows: number, columns: number) {
    this.requireAttachedTerminal(terminalId)
    if (!Number.isInteger(rows) || rows < 2 || rows > 500 ||
      !Number.isInteger(columns) || columns < 2 || columns > 500) {
      throw new PlatformWebSocketError('Terminal dimensions are invalid.', 'invalid_terminal_dimensions')
    }
    const payload = new Uint8Array(4)
    const view = new DataView(payload.buffer)
    view.setUint16(0, rows)
    view.setUint16(2, columns)
    this.sendPTY(PTYFrameType.Resize, terminalId, payload)
  }

  signalTerminal(terminalId: string, signal: 'INT' | 'TERM' | 'KILL' | 'HUP') {
    this.requireAttachedTerminal(terminalId)
    this.sendPTY(PTYFrameType.Signal, terminalId, new TextEncoder().encode(signal))
  }

  detachTerminal(terminalId: string) {
    this.requireTerminal(terminalId)
    if (this.terminalAttachments.has(terminalId) && this.stateValue === 'open') {
      this.sendPTY(PTYFrameType.Detach, terminalId)
    }
    this.terminalAttachments.delete(terminalId)
    this.terminalSequences.delete(terminalId)
  }

  private isCurrentOpen(lifecycleGeneration: number, openGeneration: number) {
    return this.shouldRun && lifecycleGeneration === this.lifecycleGeneration &&
      openGeneration === this.openGeneration
  }

  private async openSocket(reconnecting: boolean, lifecycleGeneration: number) {
    if (!this.shouldRun || lifecycleGeneration !== this.lifecycleGeneration) return
    const openGeneration = ++this.openGeneration
    this.clearReconnect()
    this.transition(reconnecting ? 'reconnecting' : 'connecting')
    try {
      let cursorEpoch = this.currentEpoch
      let ticket: SandboxConnectionTicketDto | undefined
      for (let attempt = 0; attempt < 4; attempt += 1) {
        const cursors = this.channels.map((channel) => ({
          channel,
          lastAckedSeq: cursorEpoch > 0
            ? this.cursorStore.get(this.sessionId, cursorEpoch, channel)
            : 0,
        }))
        const result = await this.issueTicket(
          this.sessionId, { channels: this.channels, cursors }, this.options.mutationOptions,
        )
        if (!this.isCurrentOpen(lifecycleGeneration, openGeneration)) return
        const candidate = result.data
        if (candidate.sessionId !== this.sessionId || candidate.channels.length !== this.channels.length ||
          candidate.channels.some((channel, index) => channel !== this.channels[index]) ||
          candidate.sessionEpoch < 1 || !sameCursors(candidate.cursors, cursors)) {
          throw new PlatformWebSocketError('Ticket scope differs from the requested stream.', 'sandbox_ticket_scope_mismatch')
        }
        if (cursorEpoch > 0 && candidate.sessionEpoch < cursorEpoch) {
          throw new PlatformWebSocketError('Ticket epoch moved backwards.', 'sandbox_ticket_scope_mismatch')
        }
        if (cursorEpoch > 0 && candidate.sessionEpoch !== cursorEpoch) {
          // The ticket already captured cursors from the prior epoch. Never
          // open it against the new Redis stream; burn it unused and reissue
          // from the cursor namespace belonging to the returned epoch.
          cursorEpoch = candidate.sessionEpoch
          continue
        }
        if (cursorEpoch === 0) {
          const persisted = this.channels.map((channel) => ({
            channel,
            lastAckedSeq: this.cursorStore.get(this.sessionId, candidate.sessionEpoch, channel),
          }))
          if (!sameCursors(persisted, cursors)) {
            cursorEpoch = candidate.sessionEpoch
            continue
          }
        }
        ticket = candidate
        break
      }
      if (!ticket) throw new PlatformWebSocketError(
        'The sandbox session epoch changed repeatedly while issuing a ticket.',
        'sandbox_ticket_scope_mismatch',
      )
      this.currentEpoch = ticket.sessionEpoch
      const url = resolveSandboxStreamUrl(this.platformBaseUrl, ticket.webSocketPath, ticket.ticket)
      if (!this.isCurrentOpen(lifecycleGeneration, openGeneration)) return
      const socket = this.webSocketFactory(url, STREAM_PROTOCOL)
      if (!this.isCurrentOpen(lifecycleGeneration, openGeneration)) {
        if (socket.readyState <= SOCKET_OPEN) socket.close(1000, 'stale connection attempt')
        return
      }
      socket.binaryType = 'arraybuffer'
      this.socket = socket
      socket.onopen = () => {
        if (socket !== this.socket || !this.shouldRun) return
        this.reconnectAttempt = 0
        this.lastMessageAt = this.timer.now()
        this.transition('open')
        this.terminalSequences.clear()
        for (const terminalId of this.terminalAttachments) this.sendPTY(PTYFrameType.Attach, terminalId)
        this.scheduleHeartbeat()
      }
      socket.onmessage = (event) => void this.handleMessage(socket, event.data)
      socket.onerror = () => this.emitError(new PlatformWebSocketError(
        'The sandbox WebSocket connection failed.',
        'sandbox_socket_error',
      ))
      socket.onclose = () => this.handleClose(socket, lifecycleGeneration, openGeneration)
    } catch (error) {
      if (!this.isCurrentOpen(lifecycleGeneration, openGeneration)) return
      this.emitError(error instanceof PlatformWebSocketError
        ? error
        : new PlatformWebSocketError(
            error instanceof Error ? error.message : 'Unable to open the sandbox stream.',
            'sandbox_stream_open_failed',
          ))
      this.scheduleReconnect(lifecycleGeneration, openGeneration)
    }
  }

  private async handleMessage(socket: WebSocketLike, data: unknown) {
    try {
      if (typeof data !== 'string') {
        const bytes = await messageBytes(data, this.options.maxMessageBytes)
        if (socket !== this.socket) return
        this.handlePTYOutput(bytes)
        return
      }
      const text = await messageText(data, this.options.maxMessageBytes)
      if (socket !== this.socket) return
      const event = parseEnvelope(JSON.parse(text) as unknown)
      if (event.sessionId !== this.sessionId || event.sessionEpoch !== this.currentEpoch ||
        !this.channels.includes(event.channel)) {
        throw new PlatformWebSocketError('Sandbox stream event is fenced.', 'sandbox_stream_fenced')
      }
      const previous = this.cursorStore.get(this.sessionId, this.currentEpoch, event.channel)
      if (event.sequence <= previous) {
        this.sendAck(event.channel, previous)
        return
      }
      if (event.eventType !== 'stream.reset' && previous > 0 && event.sequence !== previous + 1) {
        throw new PlatformWebSocketError('Sandbox stream sequence has a gap.', 'sandbox_stream_gap')
      }
      if (event.eventType === 'stream.reset') {
        const payload = event.payload as Record<string, unknown>
        const expectedKeys = ['channel', 'reason', 'sessionPath', 'treePath']
        if (Object.keys(payload).length !== expectedKeys.length ||
          expectedKeys.some((key) => !Object.prototype.hasOwnProperty.call(payload, key)) ||
          payload.channel !== event.channel || payload.reason !== 'cursor_outside_retained_window' ||
          payload.sessionPath !== `/v1/sandbox-sessions/${this.sessionId}` ||
          payload.treePath !== `/v1/sandbox-sessions/${this.sessionId}/tree`) {
          throw new PlatformWebSocketError('Sandbox stream reset does not bind the exact snapshot routes.', 'invalid_message')
        }
      }
      this.lastMessageAt = this.timer.now()
      this.cursorStore.set(this.sessionId, this.currentEpoch, event.channel, event.sequence)
      this.sendAck(event.channel, event.sequence)
      if (event.eventType === 'stream.reset') {
        for (const listener of this.resetListeners) listener(event)
      }
      for (const listener of this.eventListeners) listener(event)
    } catch (error) {
      this.emitError(error instanceof PlatformWebSocketError
        ? error
        : new PlatformWebSocketError('The sandbox stream returned malformed data.', 'invalid_message'))
      socket.close(1007, 'invalid sandbox stream frame')
    }
  }

  private handlePTYOutput(bytes: Uint8Array) {
    const output = decodePTYOutput(bytes)
    if (output.sessionEpoch !== this.currentEpoch || !this.channels.includes('pty') ||
      !this.terminalAttachments.has(output.terminalId)) {
      throw new PlatformWebSocketError('Sandbox PTY output is fenced.', 'sandbox_stream_fenced')
    }
    const previous = this.cursorStore.get(this.sessionId, this.currentEpoch, 'pty')
    if (output.sequence <= previous) {
      this.sendAck('pty', previous)
      return
    }
    if (previous > 0 && output.sequence !== previous + 1) {
      throw new PlatformWebSocketError('Sandbox PTY sequence has a gap.', 'sandbox_stream_gap')
    }
    this.lastMessageAt = this.timer.now()
    this.cursorStore.set(this.sessionId, this.currentEpoch, 'pty', output.sequence)
    this.sendAck('pty', output.sequence)
    for (const listener of this.terminalListeners) listener(output)
  }

  private sendAck(channel: SandboxStreamChannel, sequence: number) {
    this.send({ channel, eventType: 'stream.ack', ack: sequence })
  }

  private send(input: { readonly channel: SandboxStreamChannel; readonly eventType: string; readonly ack: number }) {
    if (!this.socket || this.socket.readyState !== SOCKET_OPEN || this.currentEpoch < 1) return
    this.socket.send(JSON.stringify({
      schemaVersion: STREAM_COMMAND_SCHEMA, sessionId: this.sessionId, sessionEpoch: this.currentEpoch,
      channel: input.channel, eventType: input.eventType, ack: input.ack,
      requestId: this.requestIdFactory(),
    }))
  }

  private sendPTY(type: PTYFrameType, terminalId: string, payload?: Uint8Array) {
    if (!this.socket || this.socket.readyState !== SOCKET_OPEN || this.currentEpoch < 1) return
    const sequence = (this.terminalSequences.get(terminalId) ?? 0) + 1
    const candidate = this.requestIdFactory().toLowerCase()
    const requestId = UUID_PATTERN.test(candidate) ? candidate : defaultRequestId()
    this.socket.send(encodePTYFrame({
      type, sessionEpoch: this.currentEpoch, sequence,
      ack: this.cursorStore.get(this.sessionId, this.currentEpoch, 'pty'), terminalId, requestId, payload,
    }))
    this.terminalSequences.set(terminalId, sequence)
  }

  private requireTerminal(terminalId: string) {
    if (!this.channels.includes('pty') || !UUID_PATTERN.test(terminalId)) {
      throw new PlatformWebSocketError('A canonical PTY terminal ID and channel are required.', 'invalid_terminal')
    }
  }

  private requireAttachedTerminal(terminalId: string) {
    this.requireTerminal(terminalId)
    if (!this.terminalAttachments.has(terminalId)) {
      throw new PlatformWebSocketError('The PTY must be attached before it can be controlled.', 'terminal_not_attached')
    }
  }

  private scheduleHeartbeat() {
    this.clearHeartbeat()
    if (!this.channels.includes('control')) return
    this.heartbeatHandle = this.timer.setTimeout(() => {
      this.heartbeatHandle = undefined
      if (this.stateValue !== 'open') return
      if (this.timer.now() - this.lastMessageAt > this.options.heartbeatTimeoutMs) {
        this.emitError(new PlatformWebSocketError('The sandbox stream heartbeat timed out.', 'sandbox_heartbeat_timeout'))
        this.socket?.close(4000, 'heartbeat timeout')
        return
      }
      this.send({
        channel: 'control', eventType: 'stream.heartbeat',
        ack: this.cursorStore.get(this.sessionId, this.currentEpoch, 'control'),
      })
      this.scheduleHeartbeat()
    }, this.options.heartbeatIntervalMs)
  }

  private handleClose(
    socket: WebSocketLike,
    lifecycleGeneration: number,
    openGeneration: number,
  ) {
    if (socket !== this.socket || lifecycleGeneration !== this.lifecycleGeneration ||
      openGeneration !== this.openGeneration) return
    this.socket = undefined
    this.clearHeartbeat()
    this.terminalSequences.clear()
    if (!this.shouldRun) {
      this.transition('closed')
      return
    }
    this.scheduleReconnect(lifecycleGeneration, openGeneration)
  }

  private scheduleReconnect(lifecycleGeneration: number, openGeneration: number) {
    if (!this.isCurrentOpen(lifecycleGeneration, openGeneration) ||
      this.reconnectHandle !== undefined) return
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
      if (this.isCurrentOpen(lifecycleGeneration, openGeneration)) {
        void this.openSocket(true, lifecycleGeneration)
      }
    }, delay)
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

  private clearHeartbeat() {
    if (this.heartbeatHandle === undefined) return
    this.timer.clearTimeout(this.heartbeatHandle)
    this.heartbeatHandle = undefined
  }
}
