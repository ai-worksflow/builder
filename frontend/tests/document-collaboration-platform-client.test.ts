import assert from 'node:assert/strict'
import { PlatformClient } from '../lib/platform/client'
import type { FetchLike } from '../lib/platform/http'
import { parseWsServerMessage } from '../lib/platform/websocket'

type Call = {
  readonly path: string
  readonly method: string
  readonly headers: Headers
  readonly body?: unknown
}

const calls: Call[] = []
const fetch: FetchLike = async (input, init) => {
  const url = new URL(input.toString())
  calls.push({
    path: url.pathname,
    method: init?.method ?? 'GET',
    headers: new Headers(init?.headers),
    body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
  })
  if (url.pathname.endsWith('/document-graph')) {
    return Response.json({ projectId: 'project-1', nodes: [], edges: [] })
  }
  if (url.pathname.endsWith('/member-bindings') && (init?.method ?? 'GET') === 'GET') {
    return Response.json({
      artifactId: 'doc-1', projectId: 'project-1', version: 1,
      etag: '"artifact-bindings:doc-1:v1"', items: [],
    }, { headers: { ETag: '"artifact-bindings:doc-1:v1"' } })
  }
  if (url.pathname.endsWith('/member-bindings')) {
    return Response.json({
      artifactId: 'doc-1', projectId: 'project-1', version: 2,
      etag: '"artifact-bindings:doc-1:v2"', items: [],
    }, { headers: { ETag: '"artifact-bindings:doc-1:v2"' } })
  }
  if (url.pathname.endsWith('/generate-downstream')) {
    return Response.json({ commandId: 'command-1', resolvedOwnerIds: ['user-2'], proposal: { id: 'proposal-1' } }, { status: 201 })
  }
  return Response.json({ proposal: { id: 'proposal-2' }, inputManifest: { id: 'manifest-2' } }, { status: 201 })
}

async function main() {
  const client = new PlatformClient({
    http: { baseUrl: 'https://platform.example.test', fetch, requestIdFactory: () => 'generated-command-key' },
  })
  const sourceRevision = {
    artifactId: 'doc-1', revisionId: 'revision-3', revisionNumber: 3,
    contentHash: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  }

  await client.documents.graph('project-1')
  const bindings = await client.documents.memberBindings('doc-1')
  await client.documents.replaceMemberBindings('doc-1', [
    { userId: 'user-1', role: 'owner' },
    { userId: 'user-2', role: 'downstreamOwner' },
  ], { ifMatch: bindings.data.etag, idempotencyKey: 'bindings-command-1' })
  await client.documents.generateDownstream('project-1', {
    sourceRevision, targetKind: 'api_contract', targetTitle: 'Orders API',
    instruction: 'Generate exact reviewed API contract.',
  }, { idempotencyKey: 'downstream-command-1' })
  await client.documents.createSyncBackProposal('project-1', {
    targetRevision: sourceRevision,
    provenance: { kind: 'implementationProposal', id: 'implementation-1' },
    instruction: 'Sync the applied implementation facts.',
  }, { idempotencyKey: 'sync-back-command-1' })

  assert.deepEqual(calls.map(({ path, method }) => ({ path, method })), [
    { path: '/v1/projects/project-1/document-graph', method: 'GET' },
    { path: '/v1/artifacts/doc-1/member-bindings', method: 'GET' },
    { path: '/v1/artifacts/doc-1/member-bindings', method: 'PUT' },
    { path: '/v1/projects/project-1/documents/generate-downstream', method: 'POST' },
    { path: '/v1/projects/project-1/documents/sync-back', method: 'POST' },
  ])
  assert.equal(calls[2]?.headers.get('if-match'), '"artifact-bindings:doc-1:v1"')
  assert.equal(calls[2]?.headers.get('idempotency-key'), 'bindings-command-1')
  assert.deepEqual(calls[2]?.body, { items: [
    { userId: 'user-1', role: 'owner' },
    { userId: 'user-2', role: 'downstreamOwner' },
  ] })
  assert.equal(calls[3]?.headers.get('idempotency-key'), 'downstream-command-1')
  assert.deepEqual(calls[3]?.body, {
    sourceRevision: {
      artifactId: 'doc-1', revisionId: 'revision-3',
      contentHash: sourceRevision.contentHash,
    },
    targetKind: 'api_contract', targetTitle: 'Orders API',
    instruction: 'Generate exact reviewed API contract.',
  })
  assert.equal(calls[4]?.headers.get('idempotency-key'), 'sync-back-command-1')
  assert.deepEqual(calls[4]?.body, {
    targetRevision: {
      artifactId: 'doc-1', revisionId: 'revision-3',
      contentHash: sourceRevision.contentHash,
    },
    provenance: { kind: 'implementationProposal', id: 'implementation-1' },
    instruction: 'Sync the applied implementation facts.',
  })

  for (const type of ['artifact.member_bindings_replaced', 'document.downstream_generated'] as const) {
    const message = parseWsServerMessage(JSON.stringify({
      type: 'event',
      event: {
        id: `event-${type}`, type, cursor: '42', subscriptionId: 'project:project-1',
        projectId: 'project-1', occurredAt: '2026-07-11T00:00:00Z',
        payload: { projectId: 'project-1', artifactId: 'doc-1' },
      },
    }))
    assert.equal(message.type, 'event')
    if (message.type === 'event') assert.equal(message.event.type, type)
  }

  console.log('document collaboration platform client tests passed')
}

void main()
