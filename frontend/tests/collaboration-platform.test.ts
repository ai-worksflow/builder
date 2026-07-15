import assert from 'node:assert/strict'
import {
  collaborationBackendUnavailable,
  collaborationErrorMessage,
  PlatformCollaborationGateway,
} from '../lib/collaboration/platform-adapter'
import type { CollaborationVersionRef } from '../lib/collaboration/types'
import { PlatformClient } from '../lib/platform/client'
import { PlatformHttpError, PlatformNetworkError, type FetchLike } from '../lib/platform/http'
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

test('forbidden origin errors are not mislabeled as project-role failures', () => {
  const error = new PlatformHttpError({
    type: 'about:blank',
    title: 'Origin forbidden',
    status: 403,
    detail: 'Origin is not allowed.',
    code: 'origin_forbidden',
  })

  assert.equal(collaborationErrorMessage(error, 'Request failed.'), 'Origin is not allowed.')
})

test('transient gateway HTTP failures mark the collaboration backend unavailable', () => {
  const unavailable = new PlatformHttpError({
    type: 'about:blank',
    title: 'Service unavailable',
    status: 503,
  })
  const forbidden = new PlatformHttpError({
    type: 'about:blank',
    title: 'Forbidden',
    status: 403,
  })

  assert.equal(collaborationBackendUnavailable(unavailable), true)
  assert.equal(collaborationBackendUnavailable(forbidden), false)
})

function project(role: 'owner' | 'admin' | 'editor' | 'commenter' | 'viewer' = 'owner') {
  return {
    id: 'project-1',
    name: 'Platform project',
    lifecycle: 'active',
    governanceMode: 'team' as const,
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

test('project rename and archive use server ETags and never mutate a browser catalog', async () => {
  const calls: Array<{ method: string; path: string; ifMatch: string | null; idempotencyKey: string | null }> = []
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input, init) => {
        const headers = new Headers(init?.headers)
        calls.push({
          method: init?.method ?? 'GET',
          path: new URL(input.toString()).pathname,
          ifMatch: headers.get('if-match'),
          idempotencyKey: headers.get('idempotency-key'),
        })
        if (init?.method === 'DELETE') return new Response(null, { status: 204 })
        return json({ ...project(), name: 'Renamed project', etag: '"project-2"' })
      }) as FetchLike,
    },
  })
  const gateway = new PlatformCollaborationGateway(client)
  const target = {
    id: 'project-1',
    name: 'Platform project',
    createdAt: '2026-07-10T00:00:00Z',
    updatedAt: '2026-07-10T00:00:00Z',
    memberCount: 1,
    role: 'owner' as const,
    governanceMode: 'team' as const,
    etag: '"project-1"',
  }
  const renamed = await gateway.renameProject(target, 'Renamed project')
  await gateway.archiveProject(renamed)

  assert.equal(renamed.name, 'Renamed project')
  assert.deepEqual(calls.map((call) => ({ ...call, idempotencyKey: Boolean(call.idempotencyKey) })), [
    { method: 'PATCH', path: '/v1/projects/project-1', ifMatch: '"project-1"', idempotencyKey: true },
    { method: 'DELETE', path: '/v1/projects/project-1', ifMatch: '"project-2"', idempotencyKey: true },
  ])
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
      contentHash: 'sha256:abc123',
    },
    anchor: {
      revision: {
        artifactId: 'artifact-1',
        revisionId: 'revision-3',
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
      contentHash: 'sha256:def456',
    },
    summary: 'Please verify the acceptance criteria.',
    requiredReviewerIds: ['reviewer-2'],
  })
})

test('solo review requests and decisions send explicit self-approval intent', async () => {
  const calls: Array<{ path: string; body: unknown }> = []
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input, init) => {
        calls.push({
          path: new URL(input.toString()).pathname,
          body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
        })
        return json({ id: 'review-1' }, 200)
      }) as FetchLike,
    },
  })
  const gateway = new PlatformCollaborationGateway(client)
  const target: CollaborationVersionRef = {
    artifactId: 'artifact-1',
    revisionId: 'revision-4',
    contentHash: 'sha256:def456',
  }

  await gateway.requestReview('project-1', target, 'Solo review', ['owner-1'], true)
  await gateway.decideReview('review-1', 'approve', 'Checked locally', '"review:1"', true)

  assert.deepEqual(calls, [
    {
      path: '/v1/projects/project-1/reviews',
      body: {
        target,
        summary: 'Solo review',
        requiredReviewerIds: ['owner-1'],
        allowSelfApproval: true,
      },
    },
    {
      path: '/v1/reviews/review-1/decision',
      body: {
        decision: 'approved',
        summary: 'Checked locally',
        soloReviewConfirmed: true,
      },
    },
  ])
})

test('canonical backend comment threads and review requests map without legacy fields', async () => {
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input) => {
        const path = new URL(input.toString()).pathname
        if (path === '/v1/projects/project-1') return json(project())
        if (path.endsWith('/members')) {
          return json({ items: [{
            projectId: 'project-1',
            user: user(),
            role: 'owner',
            joinedAt: '2026-07-10T00:00:00Z',
            etag: '"member-1"',
          }] })
        }
        if (path.endsWith('/comments')) {
          return json({ items: [{
            id: 'thread-1',
            projectId: 'project-1',
            artifactId: 'artifact-1',
            revisionId: 'revision-4',
            anchor: {
              revision: {
                artifactId: 'artifact-1',
                revisionId: 'revision-4',
                contentHash: 'sha256:def456',
              },
              blockId: 'requirement-7',
            },
            severity: 'blocking',
            createdBy: 'user-1',
            createdAt: '2026-07-10T01:00:00Z',
            messages: [
              {
                id: 'message-1',
                body: 'Pin the acceptance criterion.',
                mentions: [],
                createdBy: 'user-1',
                createdAt: '2026-07-10T01:00:00Z',
              },
              {
                id: 'message-2',
                parentId: 'message-1',
                body: 'Pinned in this revision.',
                mentions: [],
                createdBy: 'user-1',
                createdAt: '2026-07-10T01:05:00Z',
              },
            ],
            etag: '"comment-thread:thread-1:2"',
          }] })
        }
        if (path.endsWith('/reviews')) {
          return json({ items: [{
            id: 'review-1',
            projectId: 'project-1',
            artifactId: 'artifact-1',
            revisionId: 'revision-4',
            contentHash: 'sha256:def456',
            status: 'open',
            policy: {
              reviewerIds: ['user-1'],
              minimumApprovals: 1,
              prohibitSelfReview: true,
              governanceMode: 'solo',
              soloSelfReviewOwnerId: 'user-1',
            },
            requestedBy: 'user-1',
            requestedAt: '2026-07-10T01:10:00Z',
            decisions: [],
            etag: '"review-request:review-1:1"',
          }] })
        }
        if (path.endsWith('/artifacts')) {
          return json({ items: [{
            id: 'artifact-1',
            projectId: 'project-1',
            kind: 'product_requirements',
            title: 'Requirements',
            status: 'inReview',
            latestRevisionId: 'revision-4',
            createdBy: 'user-1',
            createdAt: '2026-07-10T00:00:00Z',
            updatedAt: '2026-07-10T01:10:00Z',
            etag: '"artifact-1"',
          }] })
        }
        if (path === '/v1/revisions/revision-4') {
          return json({
            id: 'revision-4',
            artifactId: 'artifact-1',
            revisionNumber: 4,
            contentHash: 'sha256:def456',
            content: {},
            createdBy: 'user-1',
            createdAt: '2026-07-10T01:00:00Z',
          })
        }
        return json({ items: [] })
      }) as FetchLike,
    },
  })

  const snapshot = await new PlatformCollaborationGateway(client).loadProject('project-1')
  assert.equal(snapshot.comments[0].body, 'Pin the acceptance criterion.')
  assert.equal(snapshot.comments[0].replies[0].body, 'Pinned in this revision.')
  assert.equal(snapshot.comments[0].etag, '"comment-thread:thread-1:2"')
  assert.deepEqual(snapshot.comments[0].target, {
    artifactId: 'artifact-1',
    revisionId: 'revision-4',
    contentHash: 'sha256:def456',
  })
  assert.equal(snapshot.reviews[0].state, 'pending')
  assert.equal(snapshot.reviews[0].etag, '"review-request:review-1:1"')
  assert.deepEqual(snapshot.reviews[0].requiredReviewerIds, ['user-1'])
  assert.deepEqual(snapshot.reviews[0].policy, {
    governanceMode: 'solo',
    soloSelfReviewOwnerId: 'user-1',
  })
  assert.equal(snapshot.reviewTargets[0].title, 'Requirements')
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
  sockets[0].message({
    type: 'event',
    event: {
      id: 'event-review-approved',
      type: 'artifact.revision_approved',
      cursor: 'cursor-3',
      subscriptionId: 'project:project-1',
      projectId: 'project-1',
      occurredAt: '2026-07-10T00:00:02Z',
      payload: {
        projectId: 'project-1',
        artifactId: 'artifact-1',
        revisionId: 'revision-1',
        reviewId: 'review-1',
      },
    },
  })
  sockets[0].message({
    type: 'cursor.reset',
    subscriptionId: 'project:project-1',
  })

  assert.deepEqual(invalidations, ['members', 'all', 'all'])
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
