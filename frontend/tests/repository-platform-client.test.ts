import assert from 'node:assert/strict'
import { PlatformClient } from '../lib/platform/client'
import { PlatformProtocolError, type CsrfTokenStore, type FetchLike } from '../lib/platform/http'
import {
  REPOSITORY_SNAPSHOT_RECEIPT_SCHEMA_VERSION,
  REPOSITORY_SNAPSHOT_RECEIPT_SUBJECT_SCHEMA_VERSION,
  REPOSITORY_SNAPSHOT_TREE_COMMITMENT_SCHEMA_VERSION,
  candidateSearchResultMatchesCandidate,
  computeRepositorySnapshotContentHash,
  type RepositorySnapshotDto,
  type RepositorySnapshotReceiptDto,
} from '../lib/platform/repository-contract'

const treeHash = `sha256:${'a'.repeat(64)}`
const contentHash = `sha256:${'b'.repeat(64)}`
const searchProjectId = '11111111-1111-4111-8111-111111111111'
const searchCandidateId = '22222222-2222-4222-8222-222222222222'
const snapshotProjectId = '33333333-3333-4333-8333-333333333333'
const snapshotId = '44444444-4444-4444-8444-444444444444'
const snapshotCandidateId = '55555555-5555-4555-8555-555555555555'
const snapshotManifestId = '66666666-6666-4666-8666-666666666666'
const snapshotContractId = '77777777-7777-4777-8777-777777777777'
const snapshotTemplateId = '88888888-8888-4888-8888-888888888888'
const snapshotArtifactId = '99999999-9999-4999-8999-999999999999'
const snapshotRevisionId = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
const snapshotActorId = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
const snapshotCreatedAt = '2026-07-16T12:00:00.123456Z'

function digest(character: string) {
  return `sha256:${character.repeat(64)}`
}

function repositorySnapshotSubject(): RepositorySnapshotDto {
  return {
    schemaVersion: REPOSITORY_SNAPSHOT_RECEIPT_SUBJECT_SCHEMA_VERSION,
    id: snapshotId,
    projectId: snapshotProjectId,
    buildManifest: { id: snapshotManifestId, contentHash: digest('1') },
    buildContract: { id: snapshotContractId, contentHash: digest('2') },
    fullStackTemplate: { id: snapshotTemplateId, contentHash: digest('3') },
    baseWorkspaceRevision: {
      artifactId: snapshotArtifactId,
      revisionId: snapshotRevisionId,
      contentHash: digest('4'),
    },
    tree: {
      schemaVersion: REPOSITORY_SNAPSHOT_TREE_COMMITMENT_SCHEMA_VERSION,
      treeHash,
      contentObjectHash: digest('5'),
      fileCount: 1,
      byteSize: 7,
    },
    templateReleases: [{
      role: 'api',
      mountPath: 'services/api',
      release: {
        id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
        contentHash: digest('6'),
        subjectHash: digest('7'),
      },
      source: {
        repository: 'https://github.com/ai-worksflow/templates.git',
        branch: 'main',
        commit: '1'.repeat(40),
        treeHash: digest('8'),
      },
      sbomDigest: digest('9'),
      signatureBundleDigest: digest('a'),
      authorityReceipt: {
        id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
        contentHash: digest('b'),
        policyHash: digest('c'),
      },
    }, {
      role: 'web',
      mountPath: 'apps/web',
      release: {
        id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
        contentHash: digest('d'),
        subjectHash: digest('e'),
      },
      source: {
        repository: 'https://github.com/ai-worksflow/templates.git',
        branch: 'main',
        commit: '2'.repeat(40),
        treeHash: digest('f'),
      },
      sbomDigest: digest('0'),
      signatureBundleDigest: digest('1'),
      authorityReceipt: {
        id: 'ffffffff-ffff-4fff-8fff-ffffffffffff',
        contentHash: digest('2'),
        policyHash: digest('3'),
      },
    }],
    createdBy: snapshotActorId,
    createdAt: snapshotCreatedAt,
  }
}

async function repositorySnapshotReceipt(
  snapshot = repositorySnapshotSubject(),
): Promise<RepositorySnapshotReceiptDto> {
  return {
    schemaVersion: REPOSITORY_SNAPSHOT_RECEIPT_SCHEMA_VERSION,
    contentHash: await computeRepositorySnapshotContentHash(snapshot),
    snapshot,
  }
}

function snapshotCandidate(snapshot = repositorySnapshotSubject()) {
  return {
    schemaVersion: 'candidate-workspace/v1',
    id: snapshotCandidateId,
    projectId: snapshot.projectId,
    repositorySnapshotId: snapshot.id,
    buildManifest: snapshot.buildManifest,
    buildContract: snapshot.buildContract,
    fullStackTemplate: snapshot.fullStackTemplate,
    baseWorkspaceRevision: snapshot.baseWorkspaceRevision,
    baseTreeHash: snapshot.tree.treeHash,
    currentTree: {
      schemaVersion: 'repository-tree/v1',
      treeHash: snapshot.tree.treeHash,
      files: [{ path: 'apps/web/page.tsx', mode: '100644', contentHash, byteSize: 7 }],
    },
    status: 'active',
    version: 1,
    journalSequence: 0,
    sessionEpoch: 1,
    writerLeaseEpoch: 0,
    dirty: false,
    conflicted: false,
    stale: false,
    rebaseRequired: false,
    createdBy: snapshot.createdBy,
    createdAt: snapshot.createdAt,
    updatedAt: snapshot.createdAt,
  }
}

function candidateSearchPayload(candidateId = searchCandidateId) {
  return {
    schemaVersion: 'repository-candidate-search/v1',
    projectId: searchProjectId,
    head: { candidateId, generation: 7, rootHash: treeHash },
    query: 'Exact',
    caseSensitive: false,
    includeGlobs: ['apps/*'],
    truncated: true,
    limits: {
      maxQueryBytes: 256,
      maxIncludeGlobs: 16,
      maxGlobBytes: 256,
      maxFiles: 2_000,
      maxBytes: 8 * 1024 * 1024,
      maxMatches: 25,
      maxPreviewBytes: 320,
    },
    stats: { filesScanned: 4, bytesScanned: 2048, binaryFilesSkipped: 1 },
    matches: [{
      path: 'apps/web/page.tsx',
      line: 3,
      column: 5,
      preview: 'const Exact = true',
      previewTruncated: false,
      contentHash,
    }],
  }
}

function tokenStore(): CsrfTokenStore {
  return { get: () => 'csrf-repository', set: () => undefined, clear: () => undefined }
}

function searchOnlyPlatform(payload: unknown) {
  return new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: async () => Response.json(payload),
      csrfTokenStore: tokenStore(),
      requestIdFactory: () => 'search-contract-request',
    },
  })
}

function repositoryOnlyPlatform(payload: unknown) {
  return new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: async () => Response.json(payload),
      csrfTokenStore: tokenStore(),
      requestIdFactory: () => 'repository-contract-request',
    },
  })
}

async function main() {
  const calls: Array<{
    readonly method: string
    readonly path: string
    readonly search: string
    readonly headers: Headers
    readonly body?: BodyInit | null
  }> = []
  const snapshot = repositorySnapshotSubject()
  const snapshotReceipt = await repositorySnapshotReceipt(snapshot)
  const bootstrapCandidate = snapshotCandidate(snapshot)
  const candidate = {
    schemaVersion: 'candidate-workspace/v1',
    id: 'candidate-1',
    projectId: 'project/one',
    repositorySnapshotId: 'snapshot-1',
    buildManifest: { id: 'manifest-1', contentHash },
    buildContract: { id: 'contract-1', contentHash },
    fullStackTemplate: { id: 'template-1', contentHash },
    baseWorkspaceRevision: {
      artifactId: 'workspace-1', revisionId: 'revision-1', contentHash,
    },
    baseTreeHash: treeHash,
    currentTree: {
      schemaVersion: 'repository-tree/v1', treeHash,
      files: [{ path: 'apps/web/page.tsx', mode: '100644', contentHash, byteSize: 7 }],
    },
    status: 'active',
    version: 4,
    journalSequence: 2,
    sessionEpoch: 3,
    writerLeaseEpoch: 5,
    lease: { ownerId: 'actor-1', epoch: 5, expiresAt: '2026-07-16T12:15:00Z' },
    dirty: true,
    conflicted: false,
    stale: false,
    rebaseRequired: false,
    createdBy: 'actor-1',
    createdAt: '2026-07-16T12:00:00Z',
    updatedAt: '2026-07-16T12:01:00Z',
  }
  const rebase = {
    schemaVersion: 'candidate-rebase/v1',
    id: 'rebase-1',
    projectId: 'project/one',
    operationId: 'rebase-operation-1',
    predecessorCandidateId: 'candidate/one',
    successorCandidateId: 'candidate-1',
    targetBuildManifestId: 'manifest/target',
    ancestorTreeHash: treeHash,
    predecessorTreeHash: `sha256:${'c'.repeat(64)}`,
    targetTreeHash: `sha256:${'d'.repeat(64)}`,
    plannedTreeHash: `sha256:${'e'.repeat(64)}`,
    planHash: `sha256:${'f'.repeat(64)}`,
    state: 'conflicted',
    version: 2,
    operations: null,
    conflicts: [{
      schemaVersion: 'candidate-rebase-conflict/v1',
      id: 'conflict-1',
      ordinal: 0,
      path: 'apps/web/page.tsx',
      ancestorFile: null,
      predecessorFile: { path: 'apps/web/page.tsx', mode: '100755', contentHash, byteSize: 7 },
      targetFile: { path: 'apps/web/page.tsx', mode: '100644', contentHash: treeHash, byteSize: 8 },
      state: 'open',
      version: 1,
      resolutionStrategy: null,
      createdAt: '2026-07-16T12:02:00Z',
    }],
    createdBy: 'actor-1',
    createdAt: '2026-07-16T12:02:00Z',
    updatedAt: '2026-07-16T12:03:00Z',
  }
  const fetch: FetchLike = async (input, init) => {
    const url = new URL(input.toString())
    const method = init?.method ?? 'GET'
    calls.push({
      method,
      path: url.pathname,
      search: url.search,
      headers: new Headers(init?.headers),
      body: init?.body,
    })
    if (url.pathname.endsWith('/search')) {
      return Response.json(candidateSearchPayload())
    }
    if (url.pathname.endsWith('/content')) {
      return Response.json({
        schemaVersion: 'candidate-rebase-conflict-content/v1',
        rebaseId: 'rebase-1', conflictId: 'conflict-1', path: 'apps/web/page.tsx',
        ancestor: null,
        predecessor: { contentHash, byteSize: 7, encoding: 'base64', data: 'Y29udGVudA==' },
        target: { contentHash: treeHash, byteSize: 8, encoding: 'base64', data: 'dGFyZ2V0ISE=' },
      })
    }
    if (url.pathname.includes('/candidate-rebases/')) {
      return Response.json({ rebase, candidate, created: false, recovered: null, finalizationPending: null })
    }
    if (url.pathname.endsWith('/rebases')) {
      return Response.json({ rebase, candidate, created: true, recovered: false, finalizationPending: false }, { status: 201 })
    }
    if (url.pathname.includes('/repository-snapshots/')) {
      return Response.json(snapshotReceipt)
    }
    if (url.pathname.endsWith('/repository-candidates') && method === 'GET') {
      return Response.json({
        schemaVersion: 'repository-candidate-head-list/v1',
        candidates: [{ candidate, rebaseId: null }],
      })
    }
    if (url.pathname.endsWith('/repository-candidates') && method === 'POST') {
      return Response.json({
        candidate: bootstrapCandidate,
        repositorySnapshotReceipt: snapshotReceipt,
        created: true,
        recovered: false,
        finalizationPending: false,
      }, { status: 201 })
    }
    return Response.json(candidate)
  }
  const platform = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch,
      csrfTokenStore: tokenStore(),
      requestIdFactory: () => 'generated-repository-key',
    },
  })

  const bootstrapped = await platform.repository.bootstrapCandidate(
    snapshotProjectId,
    snapshotManifestId,
    { idempotencyKey: 'candidate-bootstrap-1' },
  )
  const loadedSnapshot = await platform.repository.getRepositorySnapshot(
    snapshotProjectId,
    snapshotId,
    snapshotReceipt.contentHash,
  )
  const loaded = await platform.repository.getCandidate('project/one', 'candidate/one')
  const heads = await platform.repository.listCandidateHeads('project/one')
  const searched = await platform.repository.searchCandidate(
    searchProjectId,
    searchCandidateId,
    {
      expectedHeadGeneration: 7,
      expectedRootHash: treeHash,
      query: 'Exact',
      caseSensitive: false,
      includeGlobs: ['apps/*'],
      maxMatches: 25,
    },
  )
  const started = await platform.repository.startCandidateRebase(
    'project/one',
    'candidate/one',
    {
      targetBuildManifestId: 'manifest/target', expectedCandidateVersion: 4,
      expectedSessionEpoch: 3, expectedWriterLeaseEpoch: 5,
    },
    { idempotencyKey: 'rebase-1' },
  )
  const loadedRebase = await platform.repository.getCandidateRebase('project/one', 'rebase/one')
  const conflictContent = await platform.repository.getCandidateRebaseConflictContent(
    'project/one', 'rebase/one', 'conflict/one',
  )
  const resolved = await platform.repository.resolveCandidateRebaseConflict(
    'project/one', 'rebase/one', 'conflict/one',
    { expectedConflictVersion: 1, strategy: 'current', content: '', mode: '100755' },
    { idempotencyKey: 'resolve-1' },
  )

  assert.equal(bootstrapped.data.created, true)
  assert.equal(bootstrapped.data.recovered, false)
  assert.equal(bootstrapped.data.finalizationPending, false)
  assert.equal(bootstrapped.data.candidate.currentTree.files[0]?.path, 'apps/web/page.tsx')
  assert.equal(bootstrapped.data.repositorySnapshotReceipt.contentHash, snapshotReceipt.contentHash)
  assert.equal(bootstrapped.data.repositorySnapshotReceipt.snapshot.tree.fileCount, 1)
  assert.equal(bootstrapped.data.repositorySnapshotReceipt.snapshot.templateReleases[0]?.role, 'api')
  assert.equal(loadedSnapshot.data.snapshot.id, snapshotId)
  assert.equal(loadedSnapshot.data.snapshot.tree.contentObjectHash, digest('5'))
  assert.equal(loaded.data.baseWorkspaceRevision?.revisionId, 'revision-1')
  assert.equal(heads.data.candidates[0]?.candidate.id, 'candidate-1')
  assert.equal(heads.data.candidates[0]?.rebaseId, undefined)
  assert.equal(searched.data.schemaVersion, 'repository-candidate-search/v1')
  assert.equal(searched.data.head.candidateId, searchCandidateId)
  assert.equal(searched.data.head.generation, 7)
  assert.equal(searched.data.head.rootHash, treeHash)
  assert.equal(searched.data.truncated, true)
  assert.equal(searched.data.stats.binaryFilesSkipped, 1)
  assert.equal(searched.data.matches[0]?.path, 'apps/web/page.tsx')
  assert.equal(searched.data.matches[0]?.contentHash, contentHash)
  assert.equal(started.data.created, true)
  assert.equal(started.data.rebase.operations.length, 0)
  assert.equal(started.data.rebase.conflicts[0]?.predecessorFile?.mode, '100755')
  assert.equal(started.data.rebase.conflicts[0]?.resolutionStrategy, undefined)
  assert.equal(loadedRebase.data.rebase.state, 'conflicted')
  assert.equal(conflictContent.data.ancestor, undefined)
  assert.equal(conflictContent.data.predecessor?.data, 'Y29udGVudA==')
  assert.equal(resolved.data.finalizationPending, false)
  assert.deepEqual(calls.map((call) => [call.method, call.path]), [
    ['POST', `/v1/projects/${snapshotProjectId}/repository-candidates`],
    ['GET', `/v1/projects/${snapshotProjectId}/repository-snapshots/${snapshotId}`],
    ['GET', '/v1/projects/project%2Fone/repository-candidates/candidate%2Fone'],
    ['GET', '/v1/projects/project%2Fone/repository-candidates'],
    ['POST', `/v1/projects/${searchProjectId}/repository-candidates/${searchCandidateId}/search`],
    ['POST', '/v1/projects/project%2Fone/repository-candidates/candidate%2Fone/rebases'],
    ['GET', '/v1/projects/project%2Fone/candidate-rebases/rebase%2Fone'],
    ['GET', '/v1/projects/project%2Fone/candidate-rebases/rebase%2Fone/conflicts/conflict%2Fone/content'],
    ['POST', '/v1/projects/project%2Fone/candidate-rebases/rebase%2Fone/conflicts/conflict%2Fone/resolve'],
  ])
  assert.deepEqual(JSON.parse(calls[0]?.body as string), { buildManifestId: snapshotManifestId })
  assert.equal(calls[0]?.headers.get('idempotency-key'), 'candidate-bootstrap-1')
  assert.equal(calls[0]?.headers.get('x-csrf-token'), 'csrf-repository')
  assert.equal(calls[1]?.search, `?contentHash=${encodeURIComponent(snapshotReceipt.contentHash)}`)
  assert.equal(calls[2]?.headers.get('idempotency-key'), null)
  assert.deepEqual(JSON.parse(calls[4]?.body as string), {
    expectedHeadGeneration: 7,
    expectedRootHash: treeHash,
    query: 'Exact',
    caseSensitive: false,
    includeGlobs: ['apps/*'],
    maxMatches: 25,
  })
  assert.equal(calls[4]?.headers.get('idempotency-key'), null)
  assert.deepEqual(JSON.parse(calls[5]?.body as string), {
    targetBuildManifestId: 'manifest/target', expectedCandidateVersion: 4,
    expectedSessionEpoch: 3, expectedWriterLeaseEpoch: 5,
  })
  assert.equal(calls[5]?.headers.get('idempotency-key'), 'rebase-1')
  assert.deepEqual(JSON.parse(calls[8]?.body as string), {
    expectedConflictVersion: 1, strategy: 'current', content: '', mode: '100755',
  })
  assert.equal(calls[8]?.headers.get('idempotency-key'), 'resolve-1')

  const exactSearchInput = {
    expectedHeadGeneration: 7,
    expectedRootHash: treeHash,
    query: 'Exact',
    caseSensitive: false,
    includeGlobs: ['apps/*'],
    maxMatches: 25,
  } as const
  await assert.rejects(
    () => searchOnlyPlatform({ ...candidateSearchPayload(), matches: null }).repository.searchCandidate(
      searchProjectId,
      searchCandidateId,
      exactSearchInput,
    ),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.code === 'platform_protocol_error'
      && cause.message.includes('malformed Candidate search'),
    'malformed search DTOs must fail closed',
  )
  await assert.rejects(
    () => searchOnlyPlatform(candidateSearchPayload('33333333-3333-4333-8333-333333333333'))
      .repository.searchCandidate(searchProjectId, searchCandidateId, exactSearchInput),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('different exact request'),
    'a valid DTO for a different Candidate identity must be rejected',
  )
  const unfilteredPayload = candidateSearchPayload()
  const unfiltered = await searchOnlyPlatform({
    ...unfilteredPayload,
    includeGlobs: null,
    limits: { ...unfilteredPayload.limits, maxMatches: 100 },
  }).repository.searchCandidate(searchProjectId, searchCandidateId, {
    expectedHeadGeneration: 7,
    expectedRootHash: treeHash,
    query: 'Exact',
    caseSensitive: false,
  })
  assert.deepEqual(
    unfiltered.data.includeGlobs,
    [],
    'the server zero-slice representation is the exact optional no-filter state',
  )

  const exactCandidate = {
    id: searchCandidateId,
    projectId: searchProjectId,
    version: 7,
    treeHash,
    currentTree: { schemaVersion: 'repository-tree/v1', treeHash, files: [] },
  }
  assert.equal(candidateSearchResultMatchesCandidate(searched.data, exactCandidate), true)
  assert.equal(
    candidateSearchResultMatchesCandidate(searched.data, { ...exactCandidate, version: 8 }),
    false,
    'a stale generation must prevent the UI from opening a match',
  )
  assert.equal(
    candidateSearchResultMatchesCandidate(searched.data, {
      ...exactCandidate,
      treeHash: `sha256:${'c'.repeat(64)}`,
      currentTree: {
        ...exactCandidate.currentTree,
        treeHash: `sha256:${'c'.repeat(64)}`,
      },
    }),
    false,
    'a stale root hash must prevent the UI from opening a match',
  )

  const missingCreatedBy = structuredClone(snapshotReceipt) as unknown as {
    snapshot: Record<string, unknown>
  }
  delete missingCreatedBy.snapshot.createdBy
  const oldFullTreeShape = {
    ...snapshotReceipt,
    snapshot: {
      ...snapshotReceipt.snapshot,
      tree: { ...snapshotReceipt.snapshot.tree, files: [] },
    },
  }
  const unsortedReleases = {
    ...snapshotReceipt,
    snapshot: {
      ...snapshotReceipt.snapshot,
      templateReleases: [
        snapshotReceipt.snapshot.templateReleases[1]!,
        snapshotReceipt.snapshot.templateReleases[0]!,
      ],
    },
  }
  const duplicatedRoles = {
    ...snapshotReceipt,
    snapshot: {
      ...snapshotReceipt.snapshot,
      templateReleases: [
        snapshotReceipt.snapshot.templateReleases[0]!,
        { ...snapshotReceipt.snapshot.templateReleases[1]!, role: 'api' },
      ],
    },
  }
  const malformedSnapshots: readonly [string, unknown][] = [
    ['null receipt', null],
    ['missing subject field', missingCreatedBy],
    ['null release collection', {
      ...snapshotReceipt,
      snapshot: { ...snapshotReceipt.snapshot, templateReleases: null },
    }],
    ['additional receipt field', { ...snapshotReceipt, unexpected: true }],
    ['legacy full Tree disclosure', oldFullTreeShape],
    ['noncanonical UUID', {
      ...snapshotReceipt,
      snapshot: { ...snapshotReceipt.snapshot, createdBy: snapshotActorId.toUpperCase() },
    }],
    ['noncanonical hash', { ...snapshotReceipt, contentHash: snapshotReceipt.contentHash.toUpperCase() }],
    ['noncanonical timestamp', {
      ...snapshotReceipt,
      snapshot: { ...snapshotReceipt.snapshot, createdAt: '2026-07-16T12:00:00+00:00' },
    }],
    ['unsorted roles', unsortedReleases],
    ['duplicated roles', duplicatedRoles],
    ['receipt hash mismatch', { ...snapshotReceipt, contentHash: digest('f') }],
    ['tampered subject', {
      ...snapshotReceipt,
      snapshot: {
        ...snapshotReceipt.snapshot,
        createdBy: '12121212-1212-4121-8121-121212121212',
      },
    }],
  ]
  for (const [name, payload] of malformedSnapshots) {
    await assert.rejects(
      () => repositoryOnlyPlatform(payload).repository.getRepositorySnapshot(
        snapshotProjectId,
        snapshotId,
        snapshotReceipt.contentHash,
      ),
      (cause: unknown) => cause instanceof PlatformProtocolError
        && cause.code === 'platform_protocol_error'
        && cause.message.includes('malformed RepositorySnapshot receipt'),
      `${name} must fail closed as a protocol error`,
    )
  }

  await assert.rejects(
    () => repositoryOnlyPlatform(snapshotReceipt).repository.getRepositorySnapshot(
      snapshotProjectId,
      '13131313-1313-4131-8131-131313131313',
      snapshotReceipt.contentHash,
    ),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('different exact RepositorySnapshot'),
    'a valid receipt for a different requested Snapshot identity must be rejected',
  )

  const bootstrapPayload = {
    candidate: bootstrapCandidate,
    repositorySnapshotReceipt: snapshotReceipt,
    created: true,
    recovered: false,
    finalizationPending: false,
  }
  await assert.rejects(
    () => repositoryOnlyPlatform({
      ...bootstrapPayload,
      candidate: {
        ...bootstrapCandidate,
        repositorySnapshotId: '14141414-1414-4141-8141-141414141414',
      },
    }).repository.bootstrapCandidate(snapshotProjectId, snapshotManifestId),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('bootstrap Candidate differs'),
    'bootstrap must bind the Candidate to its exact receipt',
  )
  await assert.rejects(
    () => repositoryOnlyPlatform({ ...bootstrapPayload, recovered: null })
      .repository.bootstrapCandidate(snapshotProjectId, snapshotManifestId),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('bootstrap state flags'),
    'null bootstrap flags must not be normalized to false',
  )
  await assert.rejects(
    () => repositoryOnlyPlatform({ ...bootstrapPayload, additional: true })
      .repository.bootstrapCandidate(snapshotProjectId, snapshotManifestId),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('missing or additional fields'),
    'bootstrap response keys must be exact',
  )
  await assert.rejects(
    () => repositoryOnlyPlatform(bootstrapPayload).repository.bootstrapCandidate(
      snapshotProjectId,
      '15151515-1515-4151-8151-151515151515',
    ),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('different exact project or BuildManifest'),
    'bootstrap must bind the response to the exact request',
  )
}

void main().then(() => {
  process.stdout.write('repository platform client tests passed\n')
}).catch((error) => {
  console.error(error)
  process.exitCode = 1
})
