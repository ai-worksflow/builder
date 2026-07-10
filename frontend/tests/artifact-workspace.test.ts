import assert from 'node:assert/strict'
import { ArtifactWorkspaceConflictError, ArtifactWorkspaceGateway } from '../lib/platform/artifact-workspace'
import {
  materializeBlueprintContent,
  normalizeBlueprintContent,
} from '../lib/platform/blueprint-content'
import { PlatformClient } from '../lib/platform/client'
import type { BlueprintContentDto, DocumentContentDto } from '../lib/platform/dto'
import { PlatformNetworkError, type FetchLike } from '../lib/platform/http'

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

function documentContent(): DocumentContentDto {
  return {
    kind: 'requirement',
    summary: 'Build a collaborative editor.',
    blocks: [{ id: 'block-stable', type: 'paragraph', text: 'A stable requirement.' }],
    requirements: [{
      id: 'req-stable',
      title: 'Concurrent editing',
      statement: 'Editors must preserve each other’s work.',
      priority: 'must',
      acceptanceCriterionIds: ['ac-stable'],
      sourceBlockIds: ['block-stable'],
    }],
    acceptanceCriteria: [{
      id: 'ac-stable',
      statement: 'A stale ETag produces a conflict.',
      priority: 'must',
      status: 'open',
    }],
    openQuestions: [],
    assumptions: [],
  }
}

function versionedDocument(content = documentContent()) {
  return {
    artifact: {
      id: 'document-1',
      projectId: 'project-1',
      kind: 'document',
      title: 'Requirements',
      status: 'draft',
      activeDraftId: 'draft-1',
      createdBy: 'user-1',
      createdAt: '2026-07-10T00:00:00Z',
      updatedAt: '2026-07-10T00:00:01Z',
      etag: '"artifact-1"',
    },
    draft: {
      id: 'draft-1',
      artifactId: 'document-1',
      sourceVersions: [],
      revision: 2,
      content,
      contentHash: 'sha256:document-draft',
      updatedBy: 'user-1',
      updatedAt: '2026-07-10T00:00:01Z',
      etag: '"draft-2"',
    },
  }
}

test('workspace loading uses only the six platform artifact collections', async () => {
  const paths: string[] = []
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input) => {
        paths.push(new URL(input.toString()).pathname)
        const path = new URL(input.toString()).pathname
        if (path.endsWith('/documents')) return json({ items: [versionedDocument()] })
        return json({ items: [] })
      }) as FetchLike,
    },
  })

  const snapshot = await new ArtifactWorkspaceGateway(client).load('project-1')
  assert.equal(snapshot.documents.length, 1)
  assert.equal(snapshot.blueprints.length, 0)
  assert.deepEqual(paths.sort(), [
    '/v1/projects/project-1/blueprints',
    '/v1/projects/project-1/documents',
    '/v1/projects/project-1/page-specs',
    '/v1/projects/project-1/proposals',
    '/v1/projects/project-1/prototypes',
    '/v1/projects/project-1/traces',
  ])
})

test('document autosave sends the draft ETag and stable structured IDs', async () => {
  let requestPath = ''
  let requestBody: unknown
  let requestHeaders = new Headers()
  const content = documentContent()
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input, init) => {
        requestPath = new URL(input.toString()).pathname
        requestHeaders = new Headers(init?.headers)
        requestBody = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
        return json(versionedDocument(content), 200, { etag: '"draft-3"' })
      }) as FetchLike,
    },
  })

  await new ArtifactWorkspaceGateway(client).saveDocumentDraft(
    'document-1',
    content,
    '"draft-2"',
  )

  assert.equal(requestPath, '/v1/documents/document-1/draft')
  assert.equal(requestHeaders.get('if-match'), '"draft-2"')
  assert.deepEqual(requestBody, { content, sourceVersions: [] })
})

test('stale draft ETags become an explicit conflict while retaining local content', async () => {
  const content = { ...documentContent(), summary: 'Unsaved local summary.' }
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => json({
        title: 'Precondition failed',
        status: 412,
        detail: 'The draft changed on the server.',
      }, 412, { 'content-type': 'application/problem+json' })) as FetchLike,
    },
  })

  await assert.rejects(
    new ArtifactWorkspaceGateway(client).saveDocumentDraft('document-1', content, '"stale"'),
    (error: unknown) => {
      assert.ok(error instanceof ArtifactWorkspaceConflictError)
      assert.equal(error.artifactId, 'document-1')
      assert.deepEqual(error.localContent, content)
      return true
    },
  )
})

test('immutable revisions carry the draft precondition and partial proposal application is explicit', async () => {
  const calls: Array<{ path: string; headers: Headers; body: unknown }> = []
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      requestIdFactory: (() => {
        let index = 0
        return () => `request-${++index}`
      })(),
      fetch: (async (input, init) => {
        const path = new URL(input.toString()).pathname
        calls.push({
          path,
          headers: new Headers(init?.headers),
          body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
        })
        if (path.endsWith('/revisions')) return json({ id: 'revision-1', artifactId: 'document-1' }, 201)
        return json({ id: 'proposal-1', status: 'partiallyApplied', operations: [] })
      }) as FetchLike,
    },
  })
  const gateway = new ArtifactWorkspaceGateway(client)
  await gateway.createDocumentRevision('document-1', '"draft-8"', 'Requirements checkpoint')
  await gateway.applyProposal('proposal-1', [0, 2], '"draft-7"')

  assert.equal(calls[0].path, '/v1/documents/document-1/revisions')
  assert.deepEqual(calls[0].body, {
    changeSummary: 'Requirements checkpoint',
    changeSource: 'human',
  })
  assert.equal(calls[0].headers.get('if-match'), '"draft-8"')
  assert.ok(calls[0].headers.get('idempotency-key'))
  assert.equal(calls[1].path, '/v1/proposals/proposal-1/apply')
  assert.deepEqual(calls[1].body, { operationIndexes: [0, 2] })
  assert.equal(calls[1].headers.get('if-match'), '"draft-7"')
  assert.ok(calls[1].headers.get('idempotency-key'))
})

test('blueprints keep semantic facts independent from canvas layout', () => {
  const legacy: BlueprintContentDto = {
    nodes: [{
      id: 'page-orders',
      kind: 'page',
      title: 'Orders',
      position: { x: 48, y: 96 },
      requirementIds: ['req-stable'],
      assignedMemberIds: [],
    }],
    edges: [],
    validation: [],
  }
  const normalized = normalizeBlueprintContent(legacy)
  const semanticNode = normalized.semantic?.nodes[0]
  assert.ok(semanticNode)
  assert.equal(Object.hasOwn(semanticNode, 'position'), false)
  assert.deepEqual(normalized.layout?.nodePositions['page-orders'], { x: 48, y: 96 })

  const moved = materializeBlueprintContent(
    normalized,
    normalized.semantic?.nodes ?? [],
    normalized.semantic?.edges ?? [],
    { ...(normalized.layout!), nodePositions: { 'page-orders': { x: 240, y: 120 } } },
  )
  assert.equal(Object.hasOwn(moved.semantic?.nodes[0] ?? {}, 'position'), false)
  assert.deepEqual(moved.nodes[0].position, { x: 240, y: 120 })
})

test('impact lens exposes downstream needs_sync without mutating the blueprint', async () => {
  const report = {
    projectId: 'project-1',
    blueprintArtifactId: 'blueprint-1',
    basedOnRevisionId: 'blueprint-revision-2',
    generatedAt: '2026-07-10T00:00:00Z',
    items: [{
      source: {
        artifactId: 'blueprint-1',
        revisionId: 'blueprint-revision-2',
        revisionNumber: 2,
        contentHash: 'sha256:blueprint-2',
      },
      targetArtifactId: 'page-spec-3',
      targetKind: 'pageSpec',
      reason: 'The pinned blueprint revision is older than the current approved revision.',
      severity: 'blocking',
      needsSync: true,
    }],
  }
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => json(report)) as FetchLike,
    },
  })

  const result = await new ArtifactWorkspaceGateway(client).impact('blueprint-1')
  assert.equal(result.data.items[0].needsSync, true)
  assert.equal(result.data.items[0].targetKind, 'pageSpec')
})

test('backend outages propagate and never load browser artifact fixtures', async () => {
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => { throw new TypeError('connection refused') }) as FetchLike,
    },
  })
  await assert.rejects(
    new ArtifactWorkspaceGateway(client).load('project-1'),
    PlatformNetworkError,
  )
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
    console.error(`${failed} artifact workspace test(s) failed.`)
    process.exitCode = 1
    return
  }
  console.log(`${tests.length} artifact workspace test(s) passed.`)
}

void main()
