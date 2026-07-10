import assert from 'node:assert/strict'
import { PlatformClient } from '../lib/platform/client'
import type { FetchLike } from '../lib/platform/http'

type Call = {
  readonly path: string
  readonly method: string
  readonly headers: Headers
  readonly body?: unknown
}

const calls: Call[] = []
const importRecord = {
  id: 'import-1',
  projectId: 'project-1',
  status: 'open',
  version: 4,
  etag: '"design-import:import-1:4"',
  snapshot: {
    contentHash: 'sha256:snapshot',
    rawContentHash: 'sha256:raw',
    sourceKind: 'figma',
    sourceName: 'Home',
    mode: 'upload',
    fileName: 'home.json',
    mediaType: 'application/json',
    byteSize: 2,
    capturedAt: '2026-07-11T00:00:00Z',
    selectedFrameIds: [],
  },
  pageSpecRevision: {
    artifactId: 'page-1',
    revisionId: 'page-r1',
    contentHash: 'sha256:page',
  },
  createsPrototype: true,
  createdBy: 'user-1',
  createdAt: '2026-07-11T00:00:00Z',
  updatedAt: '2026-07-11T00:00:00Z',
}

const fetch: FetchLike = async (input, init) => {
  const url = new URL(input.toString())
  const headers = new Headers(init?.headers)
  calls.push({
    path: `${url.pathname}${url.search}`,
    method: init?.method ?? 'GET',
    headers,
    body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
  })
  if (url.pathname.endsWith('/design-import-capabilities')) {
    return Response.json({ snapshotPolicy: 'immutable', trustPolicy: 'external is not fact', sources: [] })
  }
  if (url.pathname.endsWith('/design-imports') && (init?.method ?? 'GET') === 'GET') {
    return Response.json({ items: [importRecord], total: 1 })
  }
  return Response.json(importRecord, { status: url.pathname.endsWith('/decision') ? 200 : 201, headers: { ETag: importRecord.etag } })
}

async function main() {
  const client = new PlatformClient({
    http: { baseUrl: 'https://platform.example.test', fetch, requestIdFactory: () => 'generated-key' },
  })

  await client.designImports.capabilities('project-1')
  await client.designImports.list('project-1', { status: 'open' }, { limit: 20 })
  await client.designImports.create('project-1', {
    sourceKind: 'figma',
    mode: 'upload',
    title: 'Home',
    pageSpecRevision: {
      artifactId: 'page-1', revisionId: 'page-r1', revisionNumber: 1, contentHash: 'sha256:page',
    },
    file: { name: 'home.json', mediaType: 'application/json', contentBase64: 'e30=' },
  })
  await client.designImports.decide('import-1', { decision: 'approve', version: 4 }, {
    ifMatch: importRecord.etag,
    idempotencyKey: 'approve-import-1',
  })

  assert.equal(calls[0]?.path, '/v1/projects/project-1/design-import-capabilities')
  assert.equal(calls[1]?.path, '/v1/projects/project-1/design-imports?status=open&limit=20')
  assert.deepEqual(calls[2]?.body, {
    sourceKind: 'figma',
    mode: 'upload',
    title: 'Home',
    pageSpecRevision: { artifactId: 'page-1', revisionId: 'page-r1', contentHash: 'sha256:page' },
    file: { name: 'home.json', mediaType: 'application/json', contentBase64: 'e30=' },
  })
  assert.equal(calls[2]?.headers.get('idempotency-key'), 'generated-key')
  assert.equal(calls[3]?.headers.get('if-match'), importRecord.etag)
  assert.equal(calls[3]?.headers.get('idempotency-key'), 'approve-import-1')
  assert.deepEqual(calls[3]?.body, { decision: 'approve', version: 4 })

  console.log('design import platform client tests passed')
}

void main()
