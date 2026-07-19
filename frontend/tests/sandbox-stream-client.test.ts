import assert from 'node:assert/strict'
import { PlatformClient } from '../lib/platform/client'
import type { SandboxStreamCursorStore } from '../lib/platform/sandbox-stream'
import {
  parseSandboxConnectionTicket,
  type SandboxStreamChannel,
} from '../lib/platform/sandbox-contract'
import type { FetchLike } from '../lib/platform/http'
import type { WebSocketLike } from '../lib/platform/websocket'

const sessionId = '11111111-1111-4111-8111-111111111111'

function ticketId(index: number) {
  return `22222222-2222-4222-8222-${String(index).padStart(12, '0')}`
}

function ticketSecret(index: number) {
  return `${String(index).padStart(2, '0')}${'A'.repeat(41)}`
}

function ticketExpiry(offsetMs = 30_000) {
  return new Date(Date.now() + offsetMs).toISOString()
}

class FakeSocket implements WebSocketLike {
  readyState = 0
  binaryType: BinaryType = 'blob'
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  readonly sent: Array<string | ArrayBufferLike | Blob | ArrayBufferView> = []
  readonly closes: Array<{ code?: number; reason?: string }> = []

  open() {
    this.readyState = 1
    this.onopen?.({} as Event)
  }

  message(value: unknown) {
    this.onmessage?.({ data: JSON.stringify(value) } as MessageEvent)
  }

  binaryMessage(value: ArrayBuffer) {
    this.onmessage?.({ data: value } as MessageEvent)
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
    this.sent.push(data)
  }

  close(code?: number, reason?: string) {
    this.closes.push({ code, reason })
    this.readyState = 3
  }
}

function cursorStore() {
  const values = new Map<string, number>()
  const key = (id: string, epoch: number, channel: SandboxStreamChannel) => `${id}:${epoch}:${channel}`
  return {
    values,
    store: {
      get: (id, epoch, channel) => values.get(key(id, epoch, channel)) ?? 0,
      set: (id, epoch, channel, sequence) => values.set(key(id, epoch, channel), sequence),
      clear: (id) => {
        for (const value of values.keys()) {
          if (value.startsWith(`${id}:`)) values.delete(value)
        }
      },
    } satisfies SandboxStreamCursorStore,
  }
}

async function tick(delay = 0) {
  await new Promise((resolve) => setTimeout(resolve, delay))
}

async function main() {
  const ticketBodies: unknown[] = []
  let tickets = 0
  const fetch: FetchLike = async (_input, init) => {
    tickets += 1
    const body = JSON.parse(String(init?.body)) as {
      channels: SandboxStreamChannel[]
      cursors: Array<{ channel: SandboxStreamChannel; lastAckedSeq: number }>
    }
    ticketBodies.push(body)
    const ticketEpoch = tickets < 3 ? 4 : 5
    return Response.json({
      schemaVersion: 'sandbox-connection-ticket/v1', id: ticketId(tickets),
      ticket: ticketSecret(tickets), sessionId, sessionEpoch: ticketEpoch,
      channels: body.channels,
      cursors: body.cursors,
      webSocketPath: '/v1/sandbox-stream', expiresAt: ticketExpiry(),
    }, { status: 201 })
  }
  const sockets: FakeSocket[] = []
  const urls: string[] = []
  const protocols: Array<string | string[] | undefined> = []
  const cursors = cursorStore()
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test/api/platform', fetch,
      csrfTokenStore: { get: () => 'csrf', set: () => undefined, clear: () => undefined },
    },
  })
  const stream = client.sandbox.stream(sessionId, ['control', 'pty'], {
    cursorStore: cursors.store,
    webSocketFactory: (url, protocol) => {
      urls.push(url)
      protocols.push(protocol)
      const socket = new FakeSocket()
      sockets.push(socket)
      return socket
    },
    reconnectMinDelayMs: 1,
    reconnectMaxDelayMs: 1,
    reconnectJitter: 0,
    heartbeatIntervalMs: 60_000,
    heartbeatTimeoutMs: 120_000,
    requestIdFactory: () => 'stream-request',
  })
  const events: unknown[] = []
  const resets: unknown[] = []
  const terminalOutput: Uint8Array[] = []
  stream.onEvent((event) => events.push(event))
  stream.onReset((event) => resets.push(event))
  stream.onTerminalOutput((output) => terminalOutput.push(output.value))
  stream.connect()
  await tick()

  assert.equal(tickets, 1)
  assert.equal(
    urls[0],
    `wss://platform.example.test/api/platform/v1/sandbox-stream?ticket=${ticketSecret(1)}`,
  )
  assert.equal(protocols[0], 'worksflow.sandbox.v1')
  const first = sockets[0]
  assert.ok(first)
  first.open()
  assert.equal(stream.state, 'open')
  first.message({
    schemaVersion: 'sandbox-stream/v1', sessionId, sessionEpoch: 4,
    channel: 'control', eventType: 'stream.connected', sequence: 1, aggregateVersion: 0,
    timestamp: '2026-07-16T08:00:00Z', payload: { connectionId: 'connection-1' },
  })
  await tick()
  assert.equal(events.length, 1)
  assert.equal(cursors.store.get(sessionId, 4, 'control'), 1)
  assert.deepEqual(JSON.parse(String(first.sent[0] ?? '')), {
    schemaVersion: 'sandbox-stream-command/v1', sessionId, sessionEpoch: 4,
    channel: 'control', eventType: 'stream.ack', ack: 1, requestId: 'stream-request',
  })

  first.message({
    schemaVersion: 'sandbox-stream/v1', sessionId, sessionEpoch: 4,
    channel: 'control', eventType: 'stream.connected', sequence: 1, aggregateVersion: 0,
    timestamp: '2026-07-16T08:00:00Z', payload: {},
  })
  first.message({
    schemaVersion: 'sandbox-stream/v1', sessionId, sessionEpoch: 4,
    channel: 'pty', eventType: 'stream.reset', sequence: 7, aggregateVersion: 0,
    timestamp: '2026-07-16T08:00:01Z',
    payload: {
      channel: 'pty', reason: 'cursor_outside_retained_window',
      sessionPath: `/v1/sandbox-sessions/${sessionId}`,
      treePath: `/v1/sandbox-sessions/${sessionId}/tree`,
    },
  })
  await tick()
  assert.equal(events.length, 2)
  assert.equal(resets.length, 1)
  assert.equal(cursors.store.get(sessionId, 4, 'pty'), 7)

  const terminalId = '33333333-3333-4333-8333-333333333333'
  stream.attachTerminal(terminalId)
  const attach = first.sent[3]
  assert.ok(attach instanceof ArrayBuffer)
  const attachView = new DataView(attach)
  assert.equal(attachView.getUint8(5), 1)
  assert.equal(attachView.getUint32(20), 1)
  assert.equal(attachView.getUint32(28), 7)
  const value = new TextEncoder().encode('ready\r\n')
  const outputFrame = new Uint8Array(64 + value.byteLength)
  outputFrame.set(new Uint8Array(attach), 0)
  outputFrame[5] = 0x81
  const outputView = new DataView(outputFrame.buffer)
  outputView.setUint32(20, 8)
  outputView.setUint32(28, 1)
  outputFrame.set(value, 64)
  first.binaryMessage(outputFrame.buffer)
  await tick()
  assert.equal(new TextDecoder().decode(terminalOutput[0]), 'ready\r\n')
  assert.equal(cursors.store.get(sessionId, 4, 'pty'), 8)

  first.onclose?.({} as CloseEvent)
  await tick(5)
  assert.equal(tickets, 2)
  assert.equal(
    urls[1],
    `wss://platform.example.test/api/platform/v1/sandbox-stream?ticket=${ticketSecret(2)}`,
  )
  assert.deepEqual(ticketBodies[1], {
    channels: ['control', 'pty'],
    cursors: [{ channel: 'control', lastAckedSeq: 1 }, { channel: 'pty', lastAckedSeq: 8 }],
  })
  const second = sockets[1]
  assert.ok(second)
  second.open()

  // The first ticket after epoch rotation still contains epoch-4 cursors.
  // It must be burned unused; only the freshly re-signed epoch-5 zero scope
  // may create a WebSocket, whose low sequences must not be compared to E4.
  second.onclose?.({} as CloseEvent)
  await tick(5)
  assert.equal(tickets, 4)
  assert.equal(sockets.length, 3)
  assert.equal(
    urls[2],
    `wss://platform.example.test/api/platform/v1/sandbox-stream?ticket=${ticketSecret(4)}`,
  )
  assert.ok(!urls.some((url) => url.includes(`ticket=${ticketSecret(3)}`)))
  assert.deepEqual(ticketBodies[2], {
    channels: ['control', 'pty'],
    cursors: [{ channel: 'control', lastAckedSeq: 1 }, { channel: 'pty', lastAckedSeq: 8 }],
  })
  assert.deepEqual(ticketBodies[3], {
    channels: ['control', 'pty'],
    cursors: [{ channel: 'control', lastAckedSeq: 0 }, { channel: 'pty', lastAckedSeq: 0 }],
  })
  const third = sockets[2]
  assert.ok(third)
  third.open()
  third.message({
    schemaVersion: 'sandbox-stream/v1', sessionId, sessionEpoch: 5,
    channel: 'control', eventType: 'stream.reset', sequence: 1, aggregateVersion: 0,
    timestamp: '2026-07-16T08:01:00Z',
    payload: {
      channel: 'control', reason: 'cursor_outside_retained_window',
      sessionPath: `/v1/sandbox-sessions/${sessionId}`,
      treePath: `/v1/sandbox-sessions/${sessionId}/tree`,
    },
  })
  third.message({
    schemaVersion: 'sandbox-stream/v1', sessionId, sessionEpoch: 5,
    channel: 'pty', eventType: 'stream.reset', sequence: 1, aggregateVersion: 0,
    timestamp: '2026-07-16T08:01:00Z',
    payload: {
      channel: 'pty', reason: 'cursor_outside_retained_window',
      sessionPath: `/v1/sandbox-sessions/${sessionId}`,
      treePath: `/v1/sandbox-sessions/${sessionId}/tree`,
    },
  })
  await tick()
  assert.equal(resets.length, 3)
  assert.equal(cursors.store.get(sessionId, 5, 'control'), 1)
  assert.equal(cursors.store.get(sessionId, 5, 'pty'), 1)
  assert.equal(cursors.store.get(sessionId, 4, 'pty'), 8)
  stream.disconnect()

  let raceTicketCalls = 0
  let firstRaceBody: {
    channels: SandboxStreamChannel[]
    cursors: Array<{ channel: SandboxStreamChannel; lastAckedSeq: number }>
  } | undefined
  let resolveOldTicket!: (response: Response) => void
  const delayedOldTicket = new Promise<Response>((resolve) => { resolveOldTicket = resolve })
  const raceResponse = (
    index: number,
    body: NonNullable<typeof firstRaceBody>,
  ) => Response.json({
    schemaVersion: 'sandbox-connection-ticket/v1', id: ticketId(index),
    ticket: ticketSecret(index), sessionId, sessionEpoch: 6,
    channels: body.channels, cursors: body.cursors,
    webSocketPath: '/v1/sandbox-stream', expiresAt: ticketExpiry(),
  }, { status: 201 })
  const raceFetch: FetchLike = async (_input, init) => {
    raceTicketCalls += 1
    const body = JSON.parse(String(init?.body)) as NonNullable<typeof firstRaceBody>
    if (raceTicketCalls === 1) {
      firstRaceBody = body
      return delayedOldTicket
    }
    return raceResponse(6, body)
  }
  const raceSockets: FakeSocket[] = []
  const raceUrls: string[] = []
  const raceClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test/api/platform', fetch: raceFetch,
      csrfTokenStore: { get: () => 'csrf', set: () => undefined, clear: () => undefined },
    },
  })
  const raceStream = raceClient.sandbox.stream(sessionId, ['control'], {
    cursorStore: cursorStore().store,
    webSocketFactory: (url) => {
      raceUrls.push(url)
      const socket = new FakeSocket()
      raceSockets.push(socket)
      return socket
    },
    reconnectMinDelayMs: 1,
    reconnectMaxDelayMs: 1,
    reconnectJitter: 0,
  })
  raceStream.connect()
  await tick()
  assert.equal(raceTicketCalls, 1)
  raceStream.disconnect()
  raceStream.connect()
  await tick()
  assert.equal(raceTicketCalls, 2)
  assert.deepEqual(raceUrls, [
    `wss://platform.example.test/api/platform/v1/sandbox-stream?ticket=${ticketSecret(6)}`,
  ])
  const currentRaceSocket = raceSockets[0]
  assert.ok(currentRaceSocket)
  currentRaceSocket.open()
  assert.equal(raceStream.state, 'open')
  assert.ok(firstRaceBody)
  resolveOldTicket(raceResponse(5, firstRaceBody))
  await tick()
  assert.equal(raceSockets.length, 1, 'a stale ticket response must never invoke the socket factory')
  assert.equal(currentRaceSocket.closes.length, 0)
  assert.equal(raceStream.state, 'open')
  raceStream.disconnect()

  const validTicket = {
    schemaVersion: 'sandbox-connection-ticket/v1', id: ticketId(99), ticket: ticketSecret(99),
    sessionId, sessionEpoch: 5, channels: ['control'],
    cursors: [{ channel: 'control', lastAckedSeq: 0 }],
    webSocketPath: '/v1/sandbox-stream', expiresAt: ticketExpiry(),
  }
  for (const mutate of [
    (value: Record<string, unknown>) => { value.schemaVersion = 'sandbox-connection-ticket/v0' },
    (value: Record<string, unknown>) => { value.id = '' },
    (value: Record<string, unknown>) => { value.id = 'ticket-valid' },
    (value: Record<string, unknown>) => { value.ticket = '' },
    (value: Record<string, unknown>) => { value.ticket = `${'A'.repeat(42)}B` },
    (value: Record<string, unknown>) => { value.sessionId = 'session-valid' },
    (value: Record<string, unknown>) => { value.webSocketPath = '/v1/ws' },
    (value: Record<string, unknown>) => { value.expiresAt = 'not-a-time' },
    (value: Record<string, unknown>) => { value.expiresAt = ticketExpiry(-1_000) },
    (value: Record<string, unknown>) => { value.expiresAt = ticketExpiry(3 * 60_000) },
    (value: Record<string, unknown>) => { value.cursors = undefined },
  ]) {
    const candidate = structuredClone(validTicket) as unknown as Record<string, unknown>
    mutate(candidate)
    assert.throws(() => parseSandboxConnectionTicket(candidate), /malformed connection ticket/)
  }

  async function rejectsScope(ticket: Record<string, unknown>) {
    const scopeClient = new PlatformClient({
      http: {
        baseUrl: 'https://platform.example.test/api/platform',
        fetch: async () => Response.json(ticket, { status: 201 }),
        csrfTokenStore: { get: () => 'csrf', set: () => undefined, clear: () => undefined },
      },
    })
    await assert.rejects(
      scopeClient.sandbox.createConnectionTicket(sessionId, {
        channels: ['control'], cursors: [{ channel: 'control', lastAckedSeq: 0 }],
      }),
      /different session or replay scope/,
    )
  }
  await rejectsScope({
    ...validTicket,
    sessionId: '11111111-1111-4111-8111-111111111112',
  })
  await rejectsScope({
    ...validTicket,
    channels: ['pty'],
    cursors: [{ channel: 'pty', lastAckedSeq: 0 }],
  })
  await rejectsScope({
    ...validTicket,
    cursors: [{ channel: 'control', lastAckedSeq: 9 }],
  })
}

void main()
