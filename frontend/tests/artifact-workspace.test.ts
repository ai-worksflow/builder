import assert from 'node:assert/strict'
import {
  ArtifactWorkspaceConflictError,
  ArtifactWorkspaceGateway,
  approvedRequirementBaselineSources,
  artifactWorkspaceEventRequiresRefresh,
  createEmptyPageSpecContent,
  createEmptyPrototypeContent,
  documentReviewIssues,
  mergeArtifactWorkspaceProposalApply,
  normalizeDocumentContent,
  normalizeProposal,
  replaceArtifactWorkspaceSnapshotResource,
  reviewGateReadyForRequest,
  type ArtifactWorkspaceSnapshot,
} from '../lib/platform/artifact-workspace'
import {
  blueprintGate,
  materializeBlueprintContent,
  normalizeBlueprintContent,
} from '../lib/platform/blueprint-content'
import { PlatformClient } from '../lib/platform/client'
import type {
  ArtifactDraftDto,
  BlueprintContentDto,
  DocumentContentDto,
  PageSpecContentDto,
  ProposalDto,
  VersionedArtifactDto,
} from '../lib/platform/dto'
import { PlatformNetworkError, type FetchLike } from '../lib/platform/http'

type TestCase = {
  readonly name: string
  readonly run: () => void | Promise<void>
}

const tests: TestCase[] = []

function test(name: string, run: TestCase['run']) {
  tests.push({ name, run })
}

test('workspace refreshes for canonical Proposal and artifact projection events', () => {
  for (const type of [
    'proposal.created',
    'proposal.operation_decided',
    'proposal.applied',
    'artifact.revision_created',
    'review.submitted',
    'review.decision_recorded',
    'artifact.revision_approved',
  ]) {
    assert.equal(artifactWorkspaceEventRequiresRefresh(type), true, type)
  }
  assert.equal(artifactWorkspaceEventRequiresRefresh('presence.updated'), false)
})

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

function baselineDocument(
  id: string,
  kind: 'project_brief' | 'product_requirements' | 'decision_record' | 'reference_source',
  approved: boolean,
): VersionedArtifactDto<DocumentContentDto> {
  const revision = {
    id: `${id}-r1`,
    artifactId: id,
    revisionNumber: 1,
    content: documentContent(),
    contentHash: `sha256:${id}`,
    createdBy: 'user-1',
    createdAt: '2026-07-10T00:00:00Z',
  }
  return {
    artifact: {
      id,
      projectId: 'project-1',
      kind,
      artifactKey: id.toUpperCase(),
      title: id,
      lifecycle: 'active',
      status: approved ? 'approved' : 'draft',
      syncStatus: 'current',
      deliveryStatus: 'planning',
      latestRevisionId: revision.id,
      approvedRevisionId: approved ? revision.id : undefined,
      version: 1,
      createdBy: 'user-1',
      createdAt: '2026-07-10T00:00:00Z',
      updatedAt: '2026-07-10T00:00:00Z',
      etag: `"artifact:${id}:1"`,
    },
    latestRevision: revision,
    ...(approved ? { approvedRevision: revision } : {}),
  }
}

function pageSpecContent(): PageSpecContentDto {
  return createEmptyPageSpecContent(
    'page-orders',
    'Orders',
    '/orders',
    'Review and manage customer orders.',
  )
}

function versionedPageSpec(
  content = pageSpecContent(),
  etag = '"page-spec-draft-2"',
): VersionedArtifactDto<PageSpecContentDto> {
  return {
    artifact: {
      id: 'page-spec-1',
      projectId: 'project-1',
      kind: 'page_spec',
      artifactKey: 'PAGE-SPEC-ORDERS',
      title: 'Orders',
      lifecycle: 'active',
      status: 'draft',
      syncStatus: 'current',
      deliveryStatus: 'planning',
      activeDraftId: 'page-spec-draft-1',
      version: 2,
      createdBy: 'user-1',
      createdAt: '2026-07-10T00:00:00Z',
      updatedAt: '2026-07-10T00:00:01Z',
      etag: '"page-spec-artifact-2"',
    },
    draft: {
      id: 'page-spec-draft-1',
      artifactId: 'page-spec-1',
      sourceVersions: [{
        artifactId: 'blueprint-1',
        revisionId: 'blueprint-revision-3',
        contentHash: 'sha256:blueprint-3',
        purpose: 'blueprint',
        required: true,
      }],
      revision: 2,
      content,
      contentHash: 'sha256:page-spec-draft',
      updatedBy: 'user-1',
      updatedAt: '2026-07-10T00:00:01Z',
      etag,
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
    '/v1/projects/project-1/output-proposals',
    '/v1/projects/project-1/page-specs',
    '/v1/projects/project-1/prototypes',
    '/v1/projects/project-1/traces',
  ])
})

test('workspace trace loading paginates within the platform limit', async () => {
  const traceUrls: URL[] = []
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input) => {
        const url = new URL(input.toString())
        if (!url.pathname.endsWith('/traces')) return json({ items: [] })

        traceUrls.push(url)
        const page = traceUrls.length
        const offset = (page - 1) * 200
        return json({
          items: Array.from({ length: 200 }, (_, index) => ({ id: `trace-${offset + index}` })),
          nextCursor: `trace-page-${page + 1}`,
        })
      }) as FetchLike,
    },
  })

  const snapshot = await new ArtifactWorkspaceGateway(client).load('project-1')
  assert.equal(snapshot.traces.length, 500)
  assert.equal(snapshot.traces[499]?.id, 'trace-499')
  assert.deepEqual(
    traceUrls.map((url) => url.searchParams.get('limit')),
    ['200', '200', '100'],
  )
  assert.deepEqual(
    traceUrls.map((url) => url.searchParams.get('cursor')),
    [null, 'trace-page-2', 'trace-page-3'],
  )
})

test('workspace proposal loading follows every pagination cursor', async () => {
  const proposalUrls: URL[] = []
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input) => {
        const url = new URL(input.toString())
        if (!url.pathname.endsWith('/output-proposals')) return json({ items: [] })

        proposalUrls.push(url)
        if (proposalUrls.length === 1) {
          return json({
            items: Array.from({ length: 200 }, (_, index) => ({ id: `proposal-${index}` })),
            nextCursor: 'proposal-page-2',
          })
        }
        return json({ items: [{ id: 'proposal-200' }] })
      }) as FetchLike,
    },
  })

  const snapshot = await new ArtifactWorkspaceGateway(client).load('project-1')
  assert.equal(snapshot.proposals.length, 201)
  assert.equal(snapshot.proposals[200]?.id, 'proposal-200')
  assert.deepEqual(
    proposalUrls.map((url) => url.searchParams.get('limit')),
    ['200', '200'],
  )
  assert.deepEqual(
    proposalUrls.map((url) => url.searchParams.get('cursor')),
    [null, 'proposal-page-2'],
  )
})

test('workspace proposal loading rejects a repeated pagination cursor', async () => {
  let proposalRequests = 0
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input) => {
        const url = new URL(input.toString())
        if (!url.pathname.endsWith('/output-proposals')) return json({ items: [] })
        proposalRequests += 1
        return json({
          items: [{ id: `proposal-${proposalRequests}` }],
          nextCursor: 'repeated-proposal-cursor',
        })
      }) as FetchLike,
    },
  })

  await assert.rejects(
    new ArtifactWorkspaceGateway(client).load('project-1'),
    /Proposal pagination returned a repeated cursor/,
  )
  assert.equal(proposalRequests, 2)
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
  assert.deepEqual(requestBody, { content })
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

test('PageSpec drafts and revisions preserve source pins and concurrency headers', async () => {
  const calls: Array<{ path: string; method: string; headers: Headers; body: unknown }> = []
  const content = pageSpecContent()
  const blueprintRevision = {
    artifactId: 'blueprint-1',
    revisionId: 'blueprint-revision-3',
    revisionNumber: 3,
    contentHash: 'sha256:blueprint-3',
    anchorId: 'page-node-orders',
  }
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input, init) => {
        const path = new URL(input.toString()).pathname
        calls.push({
          path,
          method: init?.method ?? 'GET',
          headers: new Headers(init?.headers),
          body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
        })
        if (path.endsWith('/draft')) {
          return json(versionedPageSpec(content, '"page-spec-draft-3"'), 200, {
            etag: '"page-spec-draft-3"',
          })
        }
        return json({
          id: 'page-spec-revision-3',
          artifactId: 'page-spec-1',
          revisionNumber: 3,
          content,
          contentHash: 'sha256:page-spec-3',
          createdBy: 'user-1',
          createdAt: '2026-07-10T00:00:02Z',
        }, 201)
      }) as FetchLike,
    },
  })
  const gateway = new ArtifactWorkspaceGateway(client)

  await gateway.savePageSpecDraft(
    'page-spec-1',
    content,
    '"page-spec-draft-2"',
    [blueprintRevision],
  )
  await gateway.createPageSpecRevision(
    'page-spec-1',
    '"page-spec-draft-3"',
    'PageSpec checkpoint',
  )

  assert.deepEqual(calls.map(({ path, method }) => ({ path, method })), [
    { path: '/v1/page-specs/page-spec-1/draft', method: 'PATCH' },
    { path: '/v1/page-specs/page-spec-1/revisions', method: 'POST' },
  ])
  assert.equal(calls[0].headers.get('if-match'), '"page-spec-draft-2"')
  assert.deepEqual(calls[0].body, {
    content,
    sourceVersions: [{
      artifactId: blueprintRevision.artifactId,
      revisionId: blueprintRevision.revisionId,
      contentHash: blueprintRevision.contentHash,
      anchorId: blueprintRevision.anchorId,
    }],
  })
  assert.equal(calls[1].headers.get('if-match'), '"page-spec-draft-3"')
  assert.ok(calls[1].headers.get('idempotency-key'))
  assert.deepEqual(calls[1].body, {
    changeSummary: 'PageSpec checkpoint',
    changeSource: 'human',
  })
})

test('PageSpec snapshot updates replace only the targeted canonical resource', () => {
  const current = versionedPageSpec()
  const replacement = versionedPageSpec({
    ...pageSpecContent(),
    userGoal: 'Inspect order health and resolve exceptions.',
  }, '"page-spec-draft-3"')
  const documents: ArtifactWorkspaceSnapshot['documents'] = []
  const snapshot: ArtifactWorkspaceSnapshot = {
    documents,
    blueprints: [],
    pageSpecs: [current],
    prototypes: [],
    proposals: [],
    traces: [],
  }

  const next = replaceArtifactWorkspaceSnapshotResource(
    snapshot,
    'pageSpecs',
    'page-spec-1',
    replacement,
  )

  assert.equal(next.pageSpecs[0].draft?.content.userGoal, replacement.draft?.content.userGoal)
  assert.equal(next.pageSpecs[0].draft?.etag, '"page-spec-draft-3"')
  assert.strictEqual(next.documents, documents)
})

test('Proposal compatibility normalization supplies omitted collection fields', () => {
  const normalized = normalizeProposal({
    id: 'proposal-with-omitted-arrays',
    questions: null,
  } as unknown as ProposalDto)

  assert.deepEqual(normalized.operations, [])
  assert.deepEqual(normalized.assumptions, [])
  assert.deepEqual(normalized.questions, [])
})

test('Proposal apply response updates the local Proposal and returned draft atomically', () => {
  const pageSpec = versionedPageSpec()
  const proposal: ProposalDto = {
    id: 'page-spec-proposal-1',
    projectId: 'project-1',
    artifactId: pageSpec.artifact.id,
    manifest: { id: 'manifest-1', hash: 'sha256:manifest' },
    baseRevision: {
      artifactId: pageSpec.artifact.id,
      revisionId: 'page-spec-revision-1',
      contentHash: 'sha256:page-spec-revision-1',
    },
    payloadHash: 'sha256:proposal',
    status: 'partially_applied',
    version: 4,
    operations: [
      { id: 'operation-1', kind: 'replace', path: '/userGoal', decision: 'applied' },
      { id: 'operation-2', kind: 'remove', path: '/interactions/0', decision: 'rejected' },
    ],
    assumptions: [],
    questions: [],
    createdBy: 'user-1',
    createdAt: '2026-07-10T00:00:00Z',
    appliedAt: '2026-07-10T00:00:02Z',
  }
  const draft: ArtifactDraftDto<PageSpecContentDto> = {
    ...pageSpec.draft!,
    revision: 3,
    content: { ...pageSpec.draft!.content, userGoal: 'Review applied changes.' },
    contentHash: 'sha256:page-spec-applied',
    updatedAt: '2026-07-10T00:00:02Z',
    etag: '"page-spec-draft-3"',
  }
  const snapshot: ArtifactWorkspaceSnapshot = {
    documents: [],
    blueprints: [],
    pageSpecs: [pageSpec],
    prototypes: [],
    proposals: [{ ...proposal, status: 'ready', version: 3 }],
    traces: [],
  }

  const next = mergeArtifactWorkspaceProposalApply(
    snapshot,
    proposal,
    draft,
  )

  assert.equal(next.proposals[0].status, 'partially_applied')
  assert.equal(next.proposals[0].version, 4)
  assert.equal(next.pageSpecs[0].draft?.etag, '"page-spec-draft-3"')
  assert.equal(next.pageSpecs[0].draft?.content.userGoal, 'Review applied changes.')
})

test('PageSpec creation pins an approved Blueprint revision and page-node anchor', async () => {
  let requestBody: unknown
  const content = pageSpecContent()
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (_input, init) => {
        requestBody = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
        return json(versionedPageSpec(content), 201)
      }) as FetchLike,
    },
  })

  await new ArtifactWorkspaceGateway(client).createPageSpec(
    'project-1',
    'Orders PageSpec',
    {
      artifactId: 'blueprint-1',
      revisionId: 'blueprint-r3',
      revisionNumber: 3,
      contentHash: 'sha256:blueprint-r3',
      anchorId: 'page-node-orders',
    },
    'page-node-orders',
    content,
  )

  assert.deepEqual(requestBody, {
    title: 'Orders PageSpec',
    blueprintRevision: {
      artifactId: 'blueprint-1',
      revisionId: 'blueprint-r3',
      contentHash: 'sha256:blueprint-r3',
      anchorId: 'page-node-orders',
    },
    blueprintPageNodeId: 'page-node-orders',
    content,
  })
})

test('empty PageSpecs start with the four server-gated stable states', () => {
  const content = pageSpecContent()
  assert.equal(content.userGoal, 'Review and manage customer orders.')
  assert.deepEqual(content.states.map((state) => ({
    id: state.id,
    key: state.key,
    required: state.required,
  })), [
    { id: 'ready', key: 'ready', required: true },
    { id: 'loading', key: 'loading', required: true },
    { id: 'empty', key: 'empty', required: true },
    { id: 'error', key: 'error', required: true },
  ])
})

test('prototype initialization covers every required PageSpec state at three breakpoints', () => {
  const pageSpec = {
    ...pageSpecContent(),
    states: [
      ...pageSpecContent().states,
      {
        id: 'optional-help',
        key: 'optional-help',
        title: 'Optional help',
        required: false,
        fixtureIds: [],
        acceptanceCriterionIds: [],
      },
    ],
    acceptanceCriterionIds: ['ac-orders-visible'],
  }
  const pageSpecRevision = {
    artifactId: 'page-spec-1',
    revisionId: 'page-spec-revision-3',
    revisionNumber: 3,
    contentHash: 'sha256:page-spec-3',
  }

  const prototype = createEmptyPrototypeContent(pageSpecRevision, pageSpec)

  assert.deepEqual(prototype.pageSpecRevision, {
    artifactId: pageSpecRevision.artifactId,
    revisionId: pageSpecRevision.revisionId,
    contentHash: pageSpecRevision.contentHash,
  })
  assert.deepEqual(prototype.states.map((state) => ({
    id: state.id,
    key: state.key,
    pageStateId: state.pageStateId,
  })), [
    { id: 'ready', key: 'ready', pageStateId: 'ready' },
    { id: 'loading', key: 'loading', pageStateId: 'loading' },
    { id: 'empty', key: 'empty', pageStateId: 'empty' },
    { id: 'error', key: 'error', pageStateId: 'error' },
    { id: 'optional-help', key: 'optional-help', pageStateId: 'optional-help' },
  ])
  assert.deepEqual(prototype.breakpoints.map((breakpoint) => breakpoint.id), [
    'desktop',
    'tablet',
    'mobile',
  ])
  assert.equal(prototype.frames.length, 15)
  assert.deepEqual(
    new Set(prototype.frames.map((frame) => `${frame.stateId}:${frame.breakpointId}`)),
    new Set([
      'ready:desktop', 'ready:tablet', 'ready:mobile',
      'loading:desktop', 'loading:tablet', 'loading:mobile',
      'empty:desktop', 'empty:tablet', 'empty:mobile',
      'error:desktop', 'error:tablet', 'error:mobile',
      'optional-help:desktop', 'optional-help:tablet', 'optional-help:mobile',
    ]),
  )
  assert.equal(new Set(prototype.frames.map((frame) => frame.rootLayerId)).size, 1)
  assert.deepEqual(
    prototype.layers[prototype.frames[0].rootLayerId].acceptanceCriterionIds,
    ['ac-orders-visible'],
  )
})

test('AI proposals freeze an immutable input manifest before generation', async () => {
  const calls: Array<{ path: string; body: unknown; headers: Headers }> = []
  const targetRevision = {
    artifactId: 'document-1',
    revisionId: 'revision-4',
    revisionNumber: 4,
    contentHash: 'sha256:revision-4',
  }
  const upstreamRevision = {
    artifactId: 'requirements-1',
    revisionId: 'requirements-r3',
    revisionNumber: 3,
    contentHash: 'sha256:requirements-r3',
    anchorId: 'REQ-001',
  }
  const proposal = {
    id: 'proposal-generated',
    projectId: 'project-1',
    artifactId: 'document-1',
    manifest: { id: 'manifest-1', hash: 'sha256:manifest' },
    baseRevision: targetRevision,
    payloadHash: 'sha256:proposal',
    status: 'open',
    version: 1,
    operations: [{ id: 'operation-1', kind: 'replace', path: '/summary', value: 'Clearer', decision: 'pending' }],
    assumptions: [],
    questions: [],
    createdBy: 'user-1',
    createdAt: '2026-07-10T00:00:00Z',
  }
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (input, init) => {
        const path = new URL(input.toString()).pathname
        calls.push({
          path,
          body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
          headers: new Headers(init?.headers),
        })
        if (path.endsWith('/input-manifests')) {
          return json({
            id: 'manifest-1',
            projectId: 'project-1',
            jobType: 'document.patch',
            baseRevision: targetRevision,
            sources: [{ ref: upstreamRevision, purpose: 'approved_upstream' }],
            constraints: { instruction: 'Clarify the brief.' },
            outputSchemaVersion: 'document.patch.v1',
            createdBy: 'user-1',
            createdAt: '2026-07-10T00:00:00Z',
            hash: 'sha256:manifest',
          }, 201)
        }
        return json({ proposal, provider: 'openai', model: 'gpt-5' }, 201)
      }) as FetchLike,
    },
  })

  const result = await new ArtifactWorkspaceGateway(client).createProposal('project-1', {
    jobType: 'document.patch',
    targetRevision,
    instruction: 'Clarify the brief.',
    inputVersions: [upstreamRevision],
    constraints: {
      parentSelectionManifest: { id: 'selection-manifest-1', hash: 'sha256:selection' },
      frozenSelectionScope: { selectionId: 'sha256:scope', nodeIds: ['page-orders'] },
    },
    outputSchemaVersion: 'document.patch.v1',
  })

  assert.equal(result.data.id, 'proposal-generated')
  assert.deepEqual(calls.map((call) => call.path), [
    '/v1/projects/project-1/input-manifests',
    '/v1/input-manifests/manifest-1/generate',
  ])
  assert.deepEqual(calls[0].body, {
    jobType: 'document.patch',
    baseRevision: {
      artifactId: targetRevision.artifactId,
      revisionId: targetRevision.revisionId,
      contentHash: targetRevision.contentHash,
    },
    sources: [{
      ref: {
        artifactId: upstreamRevision.artifactId,
        revisionId: upstreamRevision.revisionId,
        contentHash: upstreamRevision.contentHash,
        anchorId: upstreamRevision.anchorId,
      },
      purpose: 'approved_upstream',
    }],
    constraints: {
      instruction: 'Clarify the brief.',
      parentSelectionManifest: { id: 'selection-manifest-1', hash: 'sha256:selection' },
      frozenSelectionScope: { selectionId: 'sha256:scope', nodeIds: ['page-orders'] },
    },
    outputSchemaVersion: 'document.patch.v1',
  })
  assert.deepEqual(calls[1].body, { model: 'gpt-5' })
  assert.ok(calls.every((call) => call.headers.get('idempotency-key')))
})

test('immutable revisions carry the draft precondition and proposal operations are decided before apply', async () => {
  const calls: Array<{ path: string; headers: Headers; body: unknown }> = []
  let proposal = {
    id: 'proposal-1',
    projectId: 'project-1',
    artifactId: 'document-1',
    manifest: { id: 'manifest-1', hash: 'sha256:manifest' },
    baseRevision: { artifactId: 'document-1', revisionId: 'revision-1', revisionNumber: 1, contentHash: 'sha256:base' },
    payloadHash: 'sha256:proposal',
    status: 'open',
    version: 1,
    operations: [
      { id: 'operation-0', kind: 'replace', path: '/summary', value: 'New', decision: 'pending' },
      { id: 'operation-1', kind: 'remove', path: '/openQuestions/0', decision: 'pending' },
      { id: 'operation-2', kind: 'add', path: '/assumptions/-', value: 'Pinned', decision: 'pending' },
    ],
    assumptions: [],
    questions: [],
    createdBy: 'user-1',
    createdAt: '2026-07-10T00:00:00Z',
  }
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
        if (path.endsWith('/decisions')) {
          const body = calls.at(-1)?.body as { operationId: string; decision: 'accepted' | 'rejected' }
          proposal = {
            ...proposal,
            version: proposal.version + 1,
            status: proposal.version >= 3 ? 'ready' : 'reviewing',
            operations: proposal.operations.map((operation) => operation.id === body.operationId
              ? { ...operation, decision: body.decision }
              : operation),
          }
          return json(proposal)
        }
        if (path.endsWith('/apply')) return json({ id: 'draft-9', artifactId: 'document-1', content: documentContent() })
        return json(proposal)
      }) as FetchLike,
    },
  })
  const gateway = new ArtifactWorkspaceGateway(client)
  await gateway.createDocumentRevision('document-1', '"draft-8"', 'Requirements checkpoint')
  const applied = await gateway.applyProposal('proposal-1', ['operation-0', 'operation-2'])

  assert.equal(calls[0].path, '/v1/documents/document-1/revisions')
  assert.deepEqual(calls[0].body, {
    changeSummary: 'Requirements checkpoint',
    changeSource: 'human',
  })
  assert.equal(calls[0].headers.get('if-match'), '"draft-8"')
  assert.ok(calls[0].headers.get('idempotency-key'))
  assert.equal(calls[1].path, '/v1/output-proposals/proposal-1')
  assert.deepEqual(calls.slice(2, 5).map((call) => call.body), [
    { operationId: 'operation-0', decision: 'accepted', version: 1 },
    { operationId: 'operation-1', decision: 'rejected', reason: 'Not selected during proposal review.', version: 2 },
    { operationId: 'operation-2', decision: 'accepted', version: 3 },
  ])
  assert.deepEqual(calls.slice(2, 5).map((call) => call.headers.get('if-match')), [
    '"output-proposal:proposal-1:1"',
    '"output-proposal:proposal-1:2"',
    '"output-proposal:proposal-1:3"',
  ])
  assert.equal(calls[5].path, '/v1/output-proposals/proposal-1/apply')
  assert.deepEqual(calls[5].body, { version: 4 })
  assert.equal(calls[5].headers.get('if-match'), '"output-proposal:proposal-1:4"')
  assert.ok(calls[5].headers.get('idempotency-key'))
  assert.equal(applied.appliedProposal.status, 'partially_applied')
  assert.equal(applied.appliedProposal.version, 5)
  assert.deepEqual(
    applied.appliedProposal.operations.map((operation) => operation.decision),
    ['applied', 'rejected', 'applied'],
  )
})

test('blueprints keep semantic facts independent from canvas layout', () => {
  const legacy: BlueprintContentDto = {
    nodes: [{
      id: 'page-orders',
      key: 'PAGE-ORDERS',
      kind: 'page',
      title: 'Orders',
      route: '/orders',
      userGoal: 'Review open orders.',
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

test('Blueprint normalization hydrates canonical AI graphs without editor-only arrays or layout', () => {
  const generated = {
    nodes: [
      {
        id: 'feature-interview',
        key: 'FEATURE-INTERVIEW',
        kind: 'feature',
        title: 'AI interview',
      },
      {
        id: 'page-interview',
        key: 'PAGE-INTERVIEW',
        kind: 'page',
        title: 'AI interview',
        route: '/interview',
        userGoal: 'Complete an interview.',
        requirementIds: ['req-ai-interview-guidance'],
      },
    ],
    edges: [{
      id: 'edge-feature-page',
      sourceNodeId: 'feature-interview',
      targetNodeId: 'page-interview',
      kind: 'contains',
    }],
  } as unknown as BlueprintContentDto

  const normalized = normalizeBlueprintContent(generated)
  assert.deepEqual(normalized.semantic?.nodes.map((node) => node.assignedMemberIds), [[], []])
  assert.deepEqual(normalized.semantic?.nodes.map((node) => node.requirementIds), [[], ['req-ai-interview-guidance']])
  assert.deepEqual(normalized.semantic?.edges.map((edge) => edge.required), [false])
  assert.deepEqual(normalized.pageSpecRefs, [])
  assert.deepEqual(normalized.validation, [])
  assert.deepEqual(normalized.layout?.nodePositions, {
    'feature-interview': { x: 48, y: 48 },
    'page-interview': { x: 258, y: 48 },
  })
  assert.deepEqual(blueprintGate(normalized), [])
})

test('Blueprint normalization canonicalizes backend wire aliases and nested Page fields', () => {
  const generated = {
    nodes: [
      {
        id: 'feature-interview',
        businessKey: 'FEATURE-INTERVIEW',
        type: 'Feature',
      },
      {
        id: 'page-interview',
        businessKey: 'PAGE-INTERVIEW',
        type: 'Page',
        requirementIds: ['req-ai-interview-guidance'],
        spec: {
          title: 'AI interview',
          route: '/interview',
          userGoal: 'Complete an interview.',
        },
      },
    ],
    edges: [{
      id: 'edge-feature-page',
      from: 'feature-interview',
      to: 'page-interview',
      type: 'contains',
    }],
  } as unknown as BlueprintContentDto

  const normalized = normalizeBlueprintContent(generated)
  assert.deepEqual(normalized.semantic?.nodes.map((node) => ({
    id: node.id,
    key: node.key,
    kind: node.kind,
    title: node.title,
    route: node.route,
    userGoal: node.userGoal,
  })), [
    {
      id: 'feature-interview',
      key: 'FEATURE-INTERVIEW',
      kind: 'feature',
      title: 'FEATURE-INTERVIEW',
      route: undefined,
      userGoal: undefined,
    },
    {
      id: 'page-interview',
      key: 'PAGE-INTERVIEW',
      kind: 'page',
      title: 'AI interview',
      route: '/interview',
      userGoal: 'Complete an interview.',
    },
  ])
  assert.deepEqual(normalized.semantic?.edges, [{
    id: 'edge-feature-page',
    sourceNodeId: 'feature-interview',
    targetNodeId: 'page-interview',
    kind: 'contains',
    required: false,
  }])
  assert.deepEqual(blueprintGate(normalized), [])
})

test('Blueprint normalization repairs localized node and edge values persisted by translated selects', () => {
  const localized = {
    nodes: [
      {
        id: 'feature-interview',
        key: 'FEATURE-INTERVIEW',
        kind: '功能',
        title: 'AI 引导功能',
      },
      {
        id: 'page-interview',
        key: 'PAGE-INTERVIEW',
        kind: '页面',
        title: 'AI 访谈',
        route: '/interview',
        userGoal: '完成 AI 访谈。',
        requirementIds: ['req-ai-interview-guidance'],
      },
    ],
    edges: [{
      id: 'edge-feature-page',
      sourceNodeId: 'feature-interview',
      targetNodeId: 'page-interview',
      kind: '包含',
      required: true,
    }],
  } as unknown as BlueprintContentDto

  const normalized = normalizeBlueprintContent(localized)
  assert.deepEqual(normalized.semantic?.nodes.map((node) => node.kind), ['feature', 'page'])
  assert.equal(normalized.semantic?.edges[0]?.kind, 'contains')
  assert.deepEqual(blueprintGate(normalized), [])
})

test('Blueprint normalization canonicalizes Permission role aliases', () => {
  const legacy: BlueprintContentDto = {
    nodes: [{
      id: 'permission-orders',
      key: 'PERMISSION-ORDERS',
      kind: 'permission',
      title: 'Read orders',
      roles: [' viewer ', 'admin'],
      requiredRoles: ['admin', 'editor'],
      role: 'auditor',
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
  assert.deepEqual(semanticNode.roles, ['viewer', 'admin', 'editor', 'auditor'])
  assert.equal(Object.hasOwn(semanticNode, 'requiredRoles'), false)
  assert.equal(Object.hasOwn(semanticNode, 'role'), false)
  assert.deepEqual(normalized.nodes[0]?.roles, ['viewer', 'admin', 'editor', 'auditor'])
})

test('Blueprint API operations require unique valid contracts and permission edges', () => {
  const base: BlueprintContentDto = {
    nodes: [
      {
        id: 'api-orders',
        key: 'API-ORDERS',
        kind: 'apiOperation',
        title: 'List orders',
        method: 'GET',
        path: '/orders',
        position: { x: 48, y: 48 },
        requirementIds: [],
        assignedMemberIds: [],
      },
      {
        id: 'permission-orders',
        key: 'PERMISSION-ORDERS',
        kind: 'permission',
        title: 'Read orders',
        position: { x: 240, y: 48 },
        requirementIds: [],
        assignedMemberIds: [],
      },
    ],
    edges: [{
      id: 'edge-api-permission',
      sourceNodeId: 'api-orders',
      targetNodeId: 'permission-orders',
      kind: 'requires',
      required: true,
    }],
    validation: [],
  }
  const valid = normalizeBlueprintContent(base)
  assert.deepEqual(blueprintGate(valid), ['At least one semantic Page node is required.'])

  const duplicate = normalizeBlueprintContent({
    ...base,
    nodes: [
      ...base.nodes,
      {
        ...base.nodes[0],
        id: 'api-orders-duplicate',
        key: 'API-ORDERS-DUPLICATE',
        position: { x: 48, y: 160 },
      },
    ],
  })
  const duplicateIssues = blueprintGate(duplicate)
  assert.ok(duplicateIssues.includes('API method/path pairs must be unique.'))
  assert.ok(duplicateIssues.includes('Every API operation must require a Permission node.'))

  const invalid = normalizeBlueprintContent({
    ...base,
    nodes: base.nodes.map((node) => node.id === 'api-orders'
      ? { ...node, method: 'TRACE', path: 'orders' }
      : node),
    edges: [],
  })
  const invalidIssues = blueprintGate(invalid)
  assert.ok(invalidIssues.includes('Every API operation needs a supported HTTP method and absolute path.'))
  assert.ok(invalidIssues.includes('Every API operation must require a Permission node.'))
})

test('review requests require every pre-review server check but not the approval they create', () => {
  assert.equal(reviewGateReadyForRequest({
    passed: false,
    checks: [
      { code: 'artifact_content_valid', severity: 'info', message: 'valid' },
      { code: 'required_trace_coverage', severity: 'info', message: 'covered' },
      { code: 'canonical_review_approved', severity: 'error', message: 'pending review' },
    ],
    unresolvedBlockingCommentIds: [],
    traceCoverage: 1,
  }), true)
  assert.equal(reviewGateReadyForRequest({
    passed: false,
    checks: [
      { code: 'artifact_content_valid', severity: 'error', message: 'invalid' },
      { code: 'canonical_review_approved', severity: 'error', message: 'pending review' },
    ],
    unresolvedBlockingCommentIds: [],
    traceCoverage: 0,
  }), false)
})

test('Project Brief review gate requires a goal and resolves every blocking question', () => {
  const brief: DocumentContentDto = {
    kind: 'projectBrief',
    summary: 'Define the customer support application.',
    blocks: [
      { id: 'goal-1', type: 'goal', text: 'Reduce first-response time.' },
      {
        id: 'question-1',
        type: 'openQuestion',
        text: 'Which teams participate?',
        blocking: true,
        status: 'open',
      },
    ],
    requirements: [],
    acceptanceCriteria: [],
    openQuestions: [],
    assumptions: [],
  }

  assert.deepEqual(documentReviewIssues(brief), [
    'Resolve or waive every blocking open question before review.',
  ])
  assert.deepEqual(documentReviewIssues({
    ...brief,
    blocks: brief.blocks.map((block) =>
      block.type === 'openQuestion' ? { ...block, status: 'answered' as const } : block),
  }), [])
  assert.deepEqual(documentReviewIssues({
    ...brief,
    blocks: brief.blocks.filter((block) => block.type !== 'goal'),
  }), [
    'Project Brief requires at least one non-empty goal block.',
    'Resolve or waive every blocking open question before review.',
  ])
})

test('requirements review gate enforces Must-to-AC and source-block integrity', () => {
  const content = documentContent()
  assert.deepEqual(documentReviewIssues(content), [])
  assert.deepEqual(documentReviewIssues({
    ...content,
    requirements: content.requirements?.map((requirement) => ({
      ...requirement,
      acceptanceCriterionIds: ['ac-missing'],
      sourceBlockIds: ['block-missing'],
    })),
  }), [
    'Every requirement acceptance reference must resolve to an existing criterion.',
    'Every requirement must trace to at least one existing source block.',
  ])
})

test('incomplete Product Requirements are normalized only for rendering and validation', () => {
  const initialPayload = {
    blocks: [],
    kind: 'productRequirements',
    schemaVersion: 1,
  }
  const initialSnapshot = structuredClone(initialPayload)
  const normalizedInitial = normalizeDocumentContent(initialPayload as unknown as DocumentContentDto)

  assert.deepEqual(initialPayload, initialSnapshot)
  assert.equal(normalizedInitial.summary, '')
  assert.deepEqual(normalizedInitial.requirements, [])
  assert.deepEqual(normalizedInitial.acceptanceCriteria, [])
  assert.deepEqual(normalizedInitial.openQuestions, [])
  assert.deepEqual(normalizedInitial.assumptions, [])
  assert.deepEqual(documentReviewIssues(initialPayload as unknown as DocumentContentDto), [
    'Summary is required.',
    'At least one structured block is required.',
    'At least one requirement is required.',
  ])

  const appliedPayload = {
    blocks: [{ id: 'source-brief', type: 'sourceContext', text: 'Reviewed Project Brief.' }],
    kind: 'productRequirements',
    schemaVersion: 1,
    summary: 'Preserve the approved workflow lineage.',
    requirements: [{
      id: 'REQ-001',
      statement: 'Preserve the exact Proposal and Revision lineage.',
      priority: 'Must',
      acceptanceCriterionIds: ['AC-001'],
      sourceBlockIds: ['source-brief'],
    }],
    acceptanceCriteria: [{
      id: 'AC-001',
      statement: 'Every gate references the exact immutable Revision.',
    }],
  }
  const appliedSnapshot = structuredClone(appliedPayload)
  const normalizedApplied = normalizeDocumentContent(appliedPayload as unknown as DocumentContentDto)

  assert.deepEqual(appliedPayload, appliedSnapshot)
  assert.equal(normalizedApplied.blocks[0]?.type, 'sourceContext')
  assert.equal(normalizedApplied.requirements?.[0]?.title, '')
  assert.equal(normalizedApplied.requirements?.[0]?.priority, 'must')
  assert.equal(normalizedApplied.acceptanceCriteria[0]?.priority, 'must')
  assert.equal(normalizedApplied.acceptanceCriteria[0]?.status, 'open')
  assert.deepEqual(normalizedApplied.openQuestions, [])
  assert.deepEqual(normalizedApplied.assumptions, [])
  assert.deepEqual(documentReviewIssues(appliedPayload as unknown as DocumentContentDto), [])
})

test('case-insensitive Must priority still enforces acceptance coverage', () => {
  const content = {
    blocks: [{ id: 'source-brief', type: 'sourceContext', text: 'Reviewed Project Brief.' }],
    kind: 'productRequirements',
    summary: 'Define a stable requirement.',
    requirements: [{
      id: 'REQ-001',
      statement: 'Preserve exact lineage.',
      priority: 'MUST',
      sourceBlockIds: ['source-brief'],
    }],
    acceptanceCriteria: [],
  }

  assert.deepEqual(documentReviewIssues(content as unknown as DocumentContentDto), [
    'Every Must requirement needs at least one acceptance criterion.',
  ])
})

test('Blueprint creation compiles only eligible approved document revisions into its baseline', () => {
  const approvedRequirements = baselineDocument('requirements-1', 'product_requirements', true)
  const approvedBrief = baselineDocument('brief-1', 'project_brief', true)
  const draftDecision = baselineDocument('decision-1', 'decision_record', false)
  const ignoredReference = baselineDocument('reference-1', 'reference_source', true)

  assert.deepEqual(approvedRequirementBaselineSources([
    approvedRequirements,
    approvedBrief,
    draftDecision,
    ignoredReference,
  ]), [
    { artifactId: 'requirements-1', revisionId: 'requirements-1-r1', contentHash: 'sha256:requirements-1' },
    { artifactId: 'brief-1', revisionId: 'brief-1-r1', contentHash: 'sha256:brief-1' },
  ])
  assert.throws(
    () => approvedRequirementBaselineSources([approvedBrief]),
    /Approve Product Requirements/,
  )
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
