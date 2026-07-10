import assert from 'node:assert/strict'
import {
  PlatformCollaborationGateway,
} from '../lib/collaboration/platform-adapter'
import type { CollaborationVersionRef } from '../lib/collaboration/types'
import { PlatformClient } from '../lib/platform/client'
import { PlatformNetworkError, type FetchLike } from '../lib/platform/http'
import type { WebSocketFactory, WebSocketLike } from '../lib/platform/websocket'

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

function user() {
  return {
    id: 'user-1',
    displayName: 'Morgan Chen',
    email: 'morgan@example.com',
    createdAt: '2026-07-10T00:00:00Z',
  }
}

function project(role: 'owner' | 'admin' | 'editor' | 'commenter' | 'viewer' = 'owner') {
  return {
    id: 'project-1',
    name: 'Platform project',
    lifecycle: 'active',
    currentUserRole: role,
    memberCount: 1,
    createdBy: 'user-1',
    createdAt: '2026-07-10T00:00:00Z',
    updatedAt: '2026-07-10T00:00:00Z',
    etag: '"project-1"',
  }
}

test('injected PlatformClient keeps registration and sign-in as distinct backend operations', async () => {
  const calls: Array<{ readonly path: string; readonly method: string; readonly body?: unknown }> = []
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input, init) => {
        calls.push({
          path: new URL(input.toString()).pathname,
          method: init?.method ?? 'GET',
          body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
        })
        return json({
          state: 'authenticated',
          user: user(),
          sessionId: 'session-1',
          expiresAt: '2026-07-11T00:00:00Z',
          csrfToken: 'csrf-token',
        })
      }) as FetchLike,
    },
  })
  const gateway = new PlatformCollaborationGateway(client)

  const registered = await gateway.signUp('Morgan Chen', 'morgan@example.com', 'long-password')
  const signedIn = await gateway.signIn('morgan@example.com', 'long-password')
  assert.equal(registered.signedIn, true)
  assert.equal(signedIn.signedIn, true)
  assert.deepEqual(calls, [
    {
      path: '/v1/session/register',
      method: 'POST',
      body: { displayName: 'Morgan Chen', email: 'morgan@example.com', password: 'long-password' },
    },
    {
      path: '/v1/session',
      method: 'POST',
      body: { email: 'morgan@example.com', password: 'long-password' },
    },
  ])
})

test('project creation requires the backend to return the creator as owner', async () => {
  const ownerClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => json(project('owner'), 201)) as FetchLike,
    },
  })
  const created = await new PlatformCollaborationGateway(ownerClient).createProject('Platform project')
  assert.equal(created.id, 'project-1')
  assert.equal(created.role, 'owner')

  const invalidClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => json(project('editor'), 201)) as FetchLike,
    },
  })
  await assert.rejects(
    new PlatformCollaborationGateway(invalidClient).createProject('Invalid project'),
    /creator as owner/,
  )
})

test('authorization always asks the backend and preserves a denied result', async () => {
  let requested = ''
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input) => {
        requested = input.toString()
        return json({ projectId: 'project-1', action: 'admin', allowed: false, role: 'editor' })
      }) as FetchLike,
    },
  })
  const allowed = await new PlatformCollaborationGateway(client).authorize('project-1', 'admin')
  assert.equal(allowed, false)
  assert.equal(requested, 'https://platform.example.test/v1/projects/project-1/authorization?action=admin')
})

test('formal comments pin the exact artifact revision and content hash', async () => {
  let body: unknown
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (_, init) => {
        body = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
        return json({ id: 'comment-1' }, 201)
      }) as FetchLike,
    },
  })
  const target = {
    artifactId: 'artifact-1',
    revisionId: 'revision-3',
    revisionNumber: 3,
    contentHash: 'sha256:abc123',
    title: 'Requirement',
  }
  await new PlatformCollaborationGateway(client).addComment(
    'project-1',
    'Review this requirement.',
    target,
  )
  assert.deepEqual(body, {
    body: 'Review this requirement.',
    artifactId: 'artifact-1',
    target: {
      artifactId: 'artifact-1',
      revisionId: 'revision-3',
      revisionNumber: 3,
      contentHash: 'sha256:abc123',
    },
    anchor: {
      revision: {
        artifactId: 'artifact-1',
        revisionId: 'revision-3',
        revisionNumber: 3,
        contentHash: 'sha256:abc123',
      },
      revisionId: 'revision-3',
    },
  })
})

test('review requests pin a revision and assign explicit reviewers without self-approving', async () => {
  let path = ''
  let body: unknown
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input, init) => {
        path = new URL(input.toString()).pathname
        body = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
        return json({ id: 'review-1', decision: 'pending' }, 201)
      }) as FetchLike,
    },
  })
  const target: CollaborationVersionRef = {
    artifactId: 'artifact-1',
    revisionId: 'revision-4',
    revisionNumber: 4,
    contentHash: 'sha256:def456',
    title: 'Requirement v4',
  }
  await new PlatformCollaborationGateway(client).requestReview(
    'project-1',
    target,
    'Please verify the acceptance criteria.',
    ['reviewer-2'],
  )

  assert.equal(path, '/v1/projects/project-1/reviews')
  assert.deepEqual(body, {
    target: {
      artifactId: 'artifact-1',
      revisionId: 'revision-4',
      revisionNumber: 4,
      contentHash: 'sha256:def456',
    },
    summary: 'Please verify the acceptance criteria.',
    requiredReviewerIds: ['reviewer-2'],
  })
})

test('backend outages propagate and never fall back to local collaboration data', async () => {
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => { throw new TypeError('connection refused') }) as FetchLike,
    },
  })
  await assert.rejects(
    new PlatformCollaborationGateway(client).listProjects(),
    PlatformNetworkError,
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

  send(data: string) {
    this.sent.push(JSON.parse(data) as unknown)
  }

  close(code?: number, reason?: string) {
    this.readyState = 3
    this.onclose?.({ code: code ?? 1000, reason: reason ?? '' } as CloseEvent)
  }

  open() {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }

  message(message: unknown) {
    this.onmessage?.({ data: JSON.stringify(message) } as MessageEvent)
  }
}

test('project WebSocket events invalidate server state and update presence directly', async () => {
  const sockets: FakeSocket[] = []
  const factory: WebSocketFactory = () => {
    const socket = new FakeSocket()
    sockets.push(socket)
    return socket
  }
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => json({})) as FetchLike,
    },
    websocket: {
      url: 'wss://platform.example.test/v1/ws',
      webSocketFactory: factory,
      requestIdFactory: () => 'request-1',
    },
  })
  const gateway = new PlatformCollaborationGateway(client)
  const invalidations: string[] = []
  const presence: string[] = []
  const unsubscribe = gateway.watchProject(
    'project-1',
    (scope) => invalidations.push(scope),
    (entry) => presence.push(`${entry.user.id}:${entry.status}`),
  )
  sockets[0].open()
  await Promise.resolve()
  sockets[0].message({ type: 'auth.ack', connectionId: 'connection-1' })
  sockets[0].message({
    type: 'event',
    event: {
      id: 'event-member',
      type: 'member.updated',
      cursor: 'cursor-1',
      subscriptionId: 'project:project-1',
      projectId: 'project-1',
      occurredAt: '2026-07-10T00:00:00Z',
      payload: { projectId: 'project-1' },
    },
  })
  sockets[0].message({
    type: 'event',
    event: {
      id: 'event-presence',
      type: 'presence.updated',
      cursor: 'cursor-2',
      subscriptionId: 'project:project-1',
      projectId: 'project-1',
      occurredAt: '2026-07-10T00:00:01Z',
      payload: {
        projectId: 'project-1',
        user: user(),
        state: 'active',
        updatedAt: '2026-07-10T00:00:01Z',
      },
    },
  })

  assert.deepEqual(invalidations, ['members'])
  assert.deepEqual(presence, ['user-1:active'])
  unsubscribe()
  gateway.disconnectRealtime()
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
    console.error(`${failed} collaboration platform test(s) failed.`)
    process.exitCode = 1
    return
  }
  console.log(`${tests.length} collaboration platform test(s) passed.`)
}

void main()
