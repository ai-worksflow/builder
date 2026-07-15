import assert from 'node:assert/strict'
import { createPlatformDomainClients } from '../lib/platform/clients'
import {
  HttpClient,
  PlatformAbortError,
  PlatformHttpError,
  PlatformNetworkError,
  PlatformProtocolError,
  resolvePlatformBaseUrl,
  type FetchLike,
  type CsrfTokenStore,
} from '../lib/platform/http'
import {
  PlatformWebSocketClient,
  resolveWebSocketUrl,
  type PlatformDomainEvent,
  type WebSocketFactory,
  type WebSocketLike,
  type WsCursorStore,
  type WsTimer,
} from '../lib/platform/websocket'

type TestCase = {
  readonly name: string
  readonly run: () => void | Promise<void>
}

const tests: TestCase[] = []

function test(name: string, run: TestCase['run']) {
  tests.push({ name, run })
}

function json(data: unknown, status = 200, headers?: HeadersInit) {
  return Response.json(data, { status, headers })
}

test('HTTP resolves configured, local-development, proxied and server platform base URLs', () => {
  assert.equal(
    resolvePlatformBaseUrl('  https://platform.example.test/api  ', { hostname: 'builder.example.test' }),
    'https://platform.example.test/api',
  )
  assert.equal(resolvePlatformBaseUrl(undefined, { hostname: 'localhost' }), 'http://localhost:8080')
  assert.equal(resolvePlatformBaseUrl(undefined, { hostname: '127.0.0.1' }), 'http://127.0.0.1:8080')
  assert.equal(resolvePlatformBaseUrl(undefined, { hostname: 'builder.example.test' }), '/api/platform')
  assert.equal(resolvePlatformBaseUrl(), 'http://127.0.0.1:8080')
})

test('HTTP requests include platform headers, credentials, query values and expose response metadata', async () => {
  let capturedUrl = ''
  let capturedInit: RequestInit | undefined
  const client = new HttpClient({
    baseUrl: 'https://platform.example.test/api/',
    requestIdFactory: () => 'generated-request-id',
    fetch: (async (input, init) => {
      capturedUrl = input.toString()
      capturedInit = init
      return json(
        { id: 'project-1' },
        200,
        { etag: '"project-v2"', 'x-request-id': 'server-request-id' },
      )
    }) as FetchLike,
  })

  const result = await client.request<{ id: string }, { name: string }>('/v1/projects/a b', {
    method: 'PATCH',
    query: { include: ['members', 'artifacts'], archived: false, ignored: undefined },
    body: { name: 'Builder' },
    requestId: 'client-request-id',
    idempotencyKey: 'idem-1',
    ifMatch: '"project-v1"',
  })

  assert.equal(
    capturedUrl,
    'https://platform.example.test/api/v1/projects/a b?include=members&include=artifacts&archived=false',
  )
  assert.equal(capturedInit?.credentials, 'include')
  assert.equal(capturedInit?.method, 'PATCH')
  const headers = new Headers(capturedInit?.headers)
  assert.equal(headers.get('accept'), 'application/json, application/problem+json')
  assert.equal(headers.get('content-type'), 'application/json')
  assert.equal(headers.get('x-request-id'), 'client-request-id')
  assert.equal(headers.get('idempotency-key'), 'idem-1')
  assert.equal(headers.get('if-match'), '"project-v1"')
  assert.equal(capturedInit?.body, JSON.stringify({ name: 'Builder' }))
  assert.equal(result.data.id, 'project-1')
  assert.equal(result.etag, '"project-v2"')
  assert.equal(result.requestId, 'server-request-id')
})

test('domain clients encode identifiers and add idempotency keys to create requests', async () => {
  let capturedUrl = ''
  let capturedHeaders = new Headers()
  const ids = ['request-1', 'idempotency-1']
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    requestIdFactory: () => ids.shift() ?? 'fallback-id',
    fetch: (async (input, init) => {
      capturedUrl = input.toString()
      capturedHeaders = new Headers(init?.headers)
      return json({ id: 'project-1', name: 'Project' }, 201)
    }) as FetchLike,
  })
  const clients = createPlatformDomainClients(http)

  await clients.projects.create({ name: 'Project', teamId: 'team/alpha' })
  assert.equal(capturedUrl, 'https://platform.example.test/v1/projects')
  assert.equal(capturedHeaders.get('x-request-id'), 'request-1')
  assert.equal(capturedHeaders.get('idempotency-key'), 'idempotency-1')

  await clients.projects.get('project/a')
  assert.equal(capturedUrl, 'https://platform.example.test/v1/projects/project%2Fa')
})

test('dependency commands pin exact immutable refs without display-only revision numbers', async () => {
  let capturedUrl = ''
  let capturedBody: unknown
  let capturedHeaders = new Headers()
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async (input, init) => {
      capturedUrl = input.toString()
      capturedBody = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
      capturedHeaders = new Headers(init?.headers)
      return json({ id: 'dependency-1' }, 201)
    }) as FetchLike,
  })

  await createPlatformDomainClients(http).artifacts.createDependency('project/alpha', {
    source: {
      artifactId: 'document-1',
      revisionId: 'revision-7',
      revisionNumber: 7,
      contentHash: 'sha256:source',
    },
    target: {
      artifactId: 'blueprint-1',
      revisionId: 'revision-3',
      revisionNumber: 3,
      contentHash: 'sha256:target',
    },
    relation: 'drives',
    required: true,
  }, { idempotencyKey: 'dependency-command-1' })

  assert.equal(capturedUrl, 'https://platform.example.test/v1/projects/project%2Falpha/dependencies')
  assert.equal(capturedHeaders.get('idempotency-key'), 'dependency-command-1')
  assert.deepEqual(capturedBody, {
    source: {
      artifactId: 'document-1',
      revisionId: 'revision-7',
      contentHash: 'sha256:source',
    },
    target: {
      artifactId: 'blueprint-1',
      revisionId: 'revision-3',
      contentHash: 'sha256:target',
    },
    relation: 'drives',
    required: true,
  })
})

test('Requirement Baseline compilation freezes exact approved refs before Blueprint creation', async () => {
  let capturedUrl = ''
  let capturedBody: unknown
  let capturedHeaders = new Headers()
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async (input, init) => {
      capturedUrl = input.toString()
      capturedBody = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
      capturedHeaders = new Headers(init?.headers)
      return json({
        id: 'baseline-revision-1',
        artifactId: 'baseline-1',
        revisionNumber: 1,
        content: {},
        contentHash: 'sha256:baseline',
        createdBy: 'user-1',
        createdAt: '2026-07-10T00:00:00Z',
      }, 201)
    }) as FetchLike,
  })

  await createPlatformDomainClients(http).artifacts.compileRequirementBaseline(
    'project/alpha',
    [{
      artifactId: 'requirements-1',
      revisionId: 'requirements-revision-4',
      revisionNumber: 4,
      contentHash: 'sha256:requirements',
    }],
    { idempotencyKey: 'compile-baseline-1' },
  )

  assert.equal(capturedUrl, 'https://platform.example.test/v1/projects/project%2Falpha/requirement-baselines')
  assert.equal(capturedHeaders.get('idempotency-key'), 'compile-baseline-1')
  assert.deepEqual(capturedBody, {
    sources: [{
      artifactId: 'requirements-1',
      revisionId: 'requirements-revision-4',
      contentHash: 'sha256:requirements',
    }],
  })
})

test('problem+json responses become structured PlatformHttpError values', async () => {
  const client = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async () => json({
      type: 'https://errors.example.test/conflict',
      title: 'Revision conflict',
      status: 409,
      detail: 'The draft changed on the server.',
      code: 'revision_conflict',
      errors: { etag: ['Use the latest ETag.'] },
    }, 409, {
      'content-type': 'application/problem+json',
      'retry-after': '12',
      'x-request-id': 'problem-request',
    })) as FetchLike,
  })

  await assert.rejects(
    client.get('/v1/documents/doc-1'),
    (error: unknown) => {
      assert.ok(error instanceof PlatformHttpError)
      assert.equal(error.status, 409)
      assert.equal(error.code, 'revision_conflict')
      assert.equal(error.requestId, 'problem-request')
      assert.equal(error.retryAfterSeconds, 12)
      assert.deepEqual(error.problem.errors, { etag: ['Use the latest ETag.'] })
      return true
    },
  )
})

test('HTTP captures CSRF tokens, sends them on mutations, and clears them after auth failure', async () => {
  let token: string | undefined
  const store: CsrfTokenStore = {
    get: () => token,
    set: (value) => { token = value },
    clear: () => { token = undefined },
  }
  const mutationHeaders: Headers[] = []
  let call = 0
  const client = new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: store,
    fetch: (async (_, init) => {
      call += 1
      if (call === 1) return json({ state: 'authenticated', csrfToken: 'csrf-1' })
      mutationHeaders.push(new Headers(init?.headers))
      if (call === 2) return json({ id: 'project-1' })
      if (call === 3) return json({ title: 'Session expired', status: 419 }, 419)
      return json({ title: 'CSRF failed', status: 403, code: 'csrf_failed' }, 403)
    }) as FetchLike,
  })

  await client.get('/v1/session')
  assert.equal(token, 'csrf-1')
  await client.post('/v1/projects', { name: 'Project' })
  assert.equal(mutationHeaders[0].get('x-csrf-token'), 'csrf-1')
  await assert.rejects(client.patch('/v1/projects/project-1', { name: 'Updated' }), PlatformHttpError)
  assert.equal(mutationHeaders[1].get('x-csrf-token'), 'csrf-1')
  assert.equal(token, undefined)

  store.set('csrf-2')
  await assert.rejects(client.post('/v1/projects', { name: 'Retry' }), PlatformHttpError)
  assert.equal(mutationHeaders[2].get('x-csrf-token'), 'csrf-2')
  assert.equal(token, undefined)
})

test('session client uses canonical register and cookie-session routes', async () => {
  const urls: string[] = []
  const methods: string[] = []
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async (input, init) => {
      urls.push(input.toString())
      methods.push(init?.method ?? 'GET')
      return json({ state: 'authenticated', user: { id: 'u1' } })
    }) as FetchLike,
  })
  const session = createPlatformDomainClients(http).session

  await session.signUp({ displayName: 'Morgan', email: 'm@example.com', password: 'long-password' })
  await session.signIn({ email: 'm@example.com', password: 'long-password' })
  assert.deepEqual(urls, [
    'https://platform.example.test/v1/session/register',
    'https://platform.example.test/v1/session',
  ])
  assert.deepEqual(methods, ['POST', 'POST'])
})

test('network failures and malformed successful JSON use distinct error classes', async () => {
  const offline = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async () => {
      throw new TypeError('network unavailable')
    }) as FetchLike,
  })
  await assert.rejects(offline.get('/v1/session'), PlatformNetworkError)

  const malformed = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async () => new Response('{broken', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    })) as FetchLike,
  })
  await assert.rejects(malformed.get('/v1/session'), PlatformProtocolError)
})

test('an external AbortSignal cancels a pending request', async () => {
  const controller = new AbortController()
  const client = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: ((_, init) => new Promise<Response>((_, reject) => {
      init?.signal?.addEventListener('abort', () => {
        reject(new DOMException('aborted', 'AbortError'))
      }, { once: true })
    })) as FetchLike,
  })

  const pending = client.get('/v1/runs/run-1', { signal: controller.signal })
  controller.abort()
  await assert.rejects(
    pending,
    (error: unknown) => {
      assert.ok(error instanceof PlatformAbortError)
      assert.equal(error.timedOut, false)
      return true
    },
  )
})

class FakeSocket implements WebSocketLike {
  readyState = 0
  binaryType: BinaryType = 'blob'
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  readonly sent: unknown[] = []
  readonly closes: Array<{ code?: number; reason?: string }> = []

  send(data: string) {
    this.sent.push(JSON.parse(data) as unknown)
  }

  close(code?: number, reason?: string) {
    this.closes.push({ code, reason })
    this.readyState = 3
    this.onclose?.({ code: code ?? 1000, reason: reason ?? '' } as CloseEvent)
  }

  open() {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }

  message(value: unknown) {
    this.onmessage?.({ data: JSON.stringify(value) } as MessageEvent)
  }

  serverClose() {
    this.readyState = 3
    this.onclose?.({ code: 1006, reason: 'network lost' } as CloseEvent)
  }
}

class FakeCursorStore implements WsCursorStore {
  readonly values = new Map<string, string>()

  get(subscriptionId: string) {
    return this.values.get(subscriptionId)
  }

  set(subscriptionId: string, cursor: string) {
    this.values.set(subscriptionId, cursor)
  }

  remove(subscriptionId: string) {
    this.values.delete(subscriptionId)
  }
}

class FakeTimer implements WsTimer {
  currentTime = Date.parse('2026-07-10T00:00:00Z')
  nextId = 1
  readonly tasks = new Map<number, { readonly handler: () => void; readonly delayMs: number }>()

  now() {
    return this.currentTime
  }

  setTimeout(handler: () => void, delayMs: number) {
    const id = this.nextId++
    this.tasks.set(id, { handler, delayMs })
    return id
  }

  clearTimeout(handle: unknown) {
    this.tasks.delete(handle as number)
  }

  runOnlyTask() {
    assert.equal(this.tasks.size, 1)
    const [id, task] = [...this.tasks.entries()][0]
    this.tasks.delete(id)
    this.currentTime += task.delayMs
    task.handler()
    return task.delayMs
  }
}

function fakeSocketFactory() {
  const sockets: FakeSocket[] = []
  const urls: string[] = []
  const factory: WebSocketFactory = (url) => {
    urls.push(url)
    const socket = new FakeSocket()
    sockets.push(socket)
    return socket
  }
  return { factory, sockets, urls }
}

test('WebSocket resolves same-origin Nginx paths', () => {
  const location = { protocol: 'https:', host: 'builder.example.test' }
  assert.equal(
    resolveWebSocketUrl('/api/platform/v1/ws', location),
    'wss://builder.example.test/api/platform/v1/ws',
  )
  assert.equal(
    resolveWebSocketUrl('wss://platform.example.test/v1/ws', location),
    'wss://platform.example.test/v1/ws',
  )
})

test('WebSocket authenticates after opening and never exposes the bearer token in its URL', async () => {
  const fake = fakeSocketFactory()
  const client = new PlatformWebSocketClient({
    url: 'wss://platform.example.test/v1/ws',
    webSocketFactory: fake.factory,
    getAuth: () => ({ bearerToken: 'secret-bearer', sessionId: 'session-1' }),
    requestIdFactory: () => 'ws-request',
  })

  client.connect()
  assert.equal(client.state, 'connecting')
  assert.equal(fake.urls[0], 'wss://platform.example.test/v1/ws')
  assert.doesNotMatch(fake.urls[0], /secret-bearer/)
  fake.sockets[0].open()
  await Promise.resolve()

  assert.equal(client.state, 'authenticating')
  assert.deepEqual(fake.sockets[0].sent[0], {
    type: 'auth',
    requestId: 'ws-request',
    bearerToken: 'secret-bearer',
    sessionId: 'session-1',
  })
  client.destroy()
})

test('WebSocket subscriptions dispatch discriminated events and persist cursors', async () => {
  const fake = fakeSocketFactory()
  const cursors = new FakeCursorStore()
  cursors.set('project:project-1', 'cursor-before')
  const client = new PlatformWebSocketClient({
    url: 'wss://platform.example.test/v1/ws',
    webSocketFactory: fake.factory,
    cursorStore: cursors,
    requestIdFactory: () => 'ws-request',
  })
  const received: PlatformDomainEvent[] = []
  client.subscribeProject('project-1', (event) => received.push(event))
  client.connect()
  fake.sockets[0].open()
  await Promise.resolve()
  fake.sockets[0].message({ type: 'auth.ack', connectionId: 'connection-1' })

  assert.equal(client.state, 'open')
  assert.deepEqual(fake.sockets[0].sent[1], {
    type: 'subscribe',
    requestId: 'ws-request',
    subscriptionId: 'project:project-1',
    topic: 'project',
    projectId: 'project-1',
    cursor: 'cursor-before',
  })

  const event = {
    id: 'event-1',
    type: 'project.updated',
    cursor: 'cursor-after',
    subscriptionId: 'project:project-1',
    projectId: 'project-1',
    occurredAt: '2026-07-10T00:00:00Z',
    payload: { id: 'project-1' },
  }
  fake.sockets[0].message({ type: 'event', event })
  fake.sockets[0].message({ type: 'event', event })
  assert.equal(received.length, 1)
  assert.equal(received[0].type, 'project.updated')
  assert.equal(cursors.get('project:project-1'), 'cursor-after')

  const canonicalReviewTypes = [
    'review.submitted',
    'review.stale',
    'review.decision_recorded',
    'artifact.revision_approved',
  ] as const
  canonicalReviewTypes.forEach((type, index) => {
    fake.sockets[0].message({
      type: 'event',
      event: {
        id: `event-review-${index}`,
        type,
        cursor: `review-cursor-${index}`,
        subscriptionId: 'project:project-1',
        projectId: 'project-1',
        occurredAt: '2026-07-10T00:00:00Z',
        payload: { projectId: 'project-1', reviewId: 'review-1' },
      },
    })
  })
  assert.deepEqual(received.slice(1).map((item) => item.type), canonicalReviewTypes)

  fake.sockets[0].message({ type: 'heartbeat', sentAt: '2026-07-10T00:00:00Z' })
  assert.deepEqual(fake.sockets[0].sent.at(-1), {
    type: 'heartbeat.ack',
    sentAt: '2026-07-10T00:00:00Z',
  })
  client.destroy()
})

test('WebSocket probes before its timeout when the server advertises a slower heartbeat', async () => {
  const fake = fakeSocketFactory()
  const timer = new FakeTimer()
  const client = new PlatformWebSocketClient({
    url: 'wss://platform.example.test/v1/ws',
    webSocketFactory: fake.factory,
    timer,
    heartbeatIntervalMs: 15_000,
    heartbeatTimeoutMs: 45_000,
  })

  client.connect()
  fake.sockets[0].open()
  await Promise.resolve()
  fake.sockets[0].message({
    type: 'auth.ack',
    connectionId: 'connection-1',
    heartbeatIntervalMs: 50_000,
  })

  assert.equal(timer.runOnlyTask(), 22_500)
  assert.equal(client.state, 'open')
  assert.deepEqual(fake.sockets[0].closes, [])
  assert.deepEqual(fake.sockets[0].sent.at(-1), {
    type: 'heartbeat',
    sentAt: '2026-07-10T00:00:22.500Z',
  })
  client.destroy()
})

test('WebSocket cursor reset clears replay state, notifies the projection, and resubscribes', async () => {
  const fake = fakeSocketFactory()
  const cursors = new FakeCursorStore()
  cursors.set('project:project-1', 'cursor-before')
  const client = new PlatformWebSocketClient({
    url: 'wss://platform.example.test/v1/ws',
    webSocketFactory: fake.factory,
    cursorStore: cursors,
    requestIdFactory: () => 'ws-request',
  })
  const resets: unknown[] = []
  client.subscribeProject('project-1', undefined, (subscription) => resets.push(subscription))
  client.connect()
  fake.sockets[0].open()
  await Promise.resolve()
  fake.sockets[0].message({ type: 'auth.ack', connectionId: 'connection-1' })

  fake.sockets[0].message({
    type: 'cursor.reset',
    subscriptionId: 'project:project-1',
  })

  assert.equal(cursors.get('project:project-1'), undefined)
  assert.deepEqual(resets, [{
    subscriptionId: 'project:project-1',
    topic: 'project',
    projectId: 'project-1',
  }])
  assert.deepEqual(fake.sockets[0].sent.at(-1), {
    type: 'subscribe',
    requestId: 'ws-request',
    subscriptionId: 'project:project-1',
    topic: 'project',
    projectId: 'project-1',
  })
  client.destroy()
})

test('WebSocket reconnects with exponential delay and resumes each subscription cursor', async () => {
  const fake = fakeSocketFactory()
  const timer = new FakeTimer()
  const cursors = new FakeCursorStore()
  const client = new PlatformWebSocketClient({
    url: 'wss://platform.example.test/v1/ws',
    webSocketFactory: fake.factory,
    cursorStore: cursors,
    timer,
    random: () => 0.5,
    reconnectMinDelayMs: 1_000,
    reconnectMaxDelayMs: 8_000,
    requestIdFactory: () => 'ws-request',
  })
  client.subscribeRun('project-1', 'run-1')
  client.connect()
  fake.sockets[0].open()
  await Promise.resolve()
  fake.sockets[0].message({ type: 'auth.ack', connectionId: 'connection-1' })
  fake.sockets[0].message({
    type: 'event',
    event: {
      id: 'event-1',
      type: 'run.updated',
      cursor: 'run-cursor-7',
      subscriptionId: 'run:project-1:run-1',
      projectId: 'project-1',
      occurredAt: '2026-07-10T00:00:00Z',
      payload: { id: 'run-1' },
    },
  })

  fake.sockets[0].serverClose()
  assert.equal(client.state, 'reconnecting')
  assert.equal(timer.runOnlyTask(), 1_000)
  assert.equal(fake.sockets.length, 2)
  fake.sockets[1].open()
  await Promise.resolve()
  fake.sockets[1].message({ type: 'auth.ack', connectionId: 'connection-2' })

  assert.deepEqual(fake.sockets[1].sent[1], {
    type: 'subscribe',
    requestId: 'ws-request',
    subscriptionId: 'run:project-1:run-1',
    topic: 'run',
    projectId: 'project-1',
    runId: 'run-1',
    cursor: 'run-cursor-7',
  })
  client.destroy()
})

test('WebSocket waits while offline and connects when the browser returns online', () => {
  const fake = fakeSocketFactory()
  const network = new EventTarget()
  let online = false
  const client = new PlatformWebSocketClient({
    url: 'wss://platform.example.test/v1/ws',
    webSocketFactory: fake.factory,
    networkEventTarget: network,
    isOnline: () => online,
  })

  client.connect()
  assert.equal(client.state, 'offline')
  assert.equal(fake.sockets.length, 0)
  online = true
  network.dispatchEvent(new Event('online'))
  assert.equal(fake.sockets.length, 1)
  assert.equal(client.state, 'reconnecting')
  client.destroy()
})

test('WebSocket rejects malformed messages without dispatching them', async () => {
  const fake = fakeSocketFactory()
  const errors: string[] = []
  const events: PlatformDomainEvent[] = []
  const client = new PlatformWebSocketClient({
    url: 'wss://platform.example.test/v1/ws',
    webSocketFactory: fake.factory,
  })
  client.onError((error) => errors.push(error.code))
  client.onEvent((event) => events.push(event))
  client.connect()
  fake.sockets[0].open()
  await Promise.resolve()
  fake.sockets[0].message({ type: 'not-a-contract-message' })
  assert.deepEqual(errors, ['unknown_message'])
  assert.equal(events.length, 0)
  client.destroy()
})

async function main() {
  let failed = 0
  for (const { name, run } of tests) {
    try {
      await run()
      console.log(`✓ ${name}`)
    } catch (error) {
      failed += 1
      console.error(`✗ ${name}`)
      console.error(error)
    }
  }
  if (failed > 0) {
    console.error(`${failed} platform client test(s) failed.`)
    process.exitCode = 1
    return
  }
  console.log(`${tests.length} platform client test(s) passed.`)
}

void main()
