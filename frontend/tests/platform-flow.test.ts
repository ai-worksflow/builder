import assert from 'node:assert/strict'
import {
  deliverySliceContext,
  parseEditableDefinition,
  resolveCandidateSelection,
  revisionCandidates,
  starterWorkflowDefinition as starterDefinition,
} from '../lib/platform/workflow-ui-contract'
import { PlatformClient } from '../lib/platform/client'
import type { FetchLike } from '../lib/platform/http'

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

function flowClient(responder: (path: string, init?: RequestInit) => Response | Promise<Response>) {
  return new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      requestIdFactory: (() => {
        let index = 0
        return () => `flow-request-${++index}`
      })(),
      fetch: (async (input, init) => responder(new URL(input.toString()).pathname, init)) as FetchLike,
    },
  }).flow
}

test('workflow definitions and immutable versions use the Go workflow routes', async () => {
  const calls: Array<{ path: string; method: string; body: unknown }> = []
  const client = flowClient((path, init) => {
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
    calls.push({ path, method: init?.method ?? 'GET', body })
    if (path.endsWith('/versions')) {
      return json({ items: [{ id: 'definition-1', versionId: 'version-2', version: 2 }] })
    }
    if (init?.method === 'POST') return json({ id: 'definition-1', versionId: 'version-1' }, 201)
    return json({ items: [] })
  })

  await client.listDefinitions('project-1')
  await client.createDefinition('project-1', {
    key: 'custom-flow',
    title: 'Custom flow',
    name: 'Custom flow',
    schemaVersion: 'workflow/v2',
    nodes: [],
    edges: [],
  })
  await client.listDefinitionVersions('project-1', 'definition-1')
  await client.listRuns('project-1', { status: 'waiting_input' }, { limit: 20 })

  assert.deepEqual(calls.map((call) => call.path), [
    '/v1/projects/project-1/workflow-definitions',
    '/v1/projects/project-1/workflow-definitions',
    '/v1/projects/project-1/workflow-definitions/definition-1/versions',
    '/v1/projects/project-1/workflow-runs',
  ])
  assert.equal(calls[1].method, 'POST')
  assert.deepEqual(calls[1].body, {
    key: 'custom-flow',
    title: 'Custom flow',
    name: 'Custom flow',
    schemaVersion: 'workflow/v2',
    nodes: [],
    edges: [],
  })
})

test('starting a run preserves the exact manifest id and hash', async () => {
  const calls: Array<{ path: string; headers: Headers; body: unknown }> = []
  const client = flowClient((path, init) => {
    calls.push({
      path,
      headers: new Headers(init?.headers),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    })
    if (path.endsWith('/input-manifests')) return json({ id: 'manifest-1', hash: 'a'.repeat(64) }, 201)
    return json({ id: 'run-1', status: 'pending', nodes: [] }, 201)
  })

  await client.createManifest('project-1', {
    jobType: 'workflow_start',
    sources: [{
      ref: {
        artifactId: 'brief-1',
        revisionId: 'brief-r3',
        revisionNumber: 3,
        contentHash: 'b'.repeat(64),
      },
      purpose: 'project_brief',
    }],
    constraints: { entryRevisionId: 'brief-r3' },
    outputSchemaVersion: 'workflow-input/v1',
  })
  await client.startRun('project-1', {
    definitionVersionId: 'definition-version-2',
    inputManifest: { id: 'manifest-1', hash: 'a'.repeat(64) },
    scope: { feature: 'billing' },
  })

  assert.equal(calls[0].path, '/v1/projects/project-1/input-manifests')
  assert.equal(calls[1].path, '/v1/projects/project-1/workflow-runs')
  assert.deepEqual(calls[1].body, {
    definitionVersionId: 'definition-version-2',
    inputManifest: { id: 'manifest-1', hash: 'a'.repeat(64) },
    scope: { feature: 'billing' },
  })
  assert.ok(calls[0].headers.get('idempotency-key'))
  assert.ok(calls[1].headers.get('idempotency-key'))
})

test('human resume, privileged execution, and review commands target a project-scoped run', async () => {
  const calls: Array<{ path: string; body: unknown }> = []
  const client = flowClient((path, init) => {
    calls.push({
      path,
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    })
    return new Response(null, { status: 204 })
  })
  const revision = {
    artifactId: 'prototype-1',
    revisionId: 'prototype-r4',
    revisionNumber: 4,
    contentHash: 'c'.repeat(64),
  }

  await client.resumeRun('project-1', 'run-1', 'prototype-edit:slice-a', {
    artifactRevision: revision,
  })
  await client.authorizeExecution('project-1', 'run-1', 'quality')
  await client.resolveReview('project-1', 'run-1', 'prototype-review:slice-a', 'approve')

  assert.deepEqual(calls, [
    {
      path: '/v1/projects/project-1/workflow-runs/run-1/resume',
      body: {
        nodeKey: 'prototype-edit:slice-a',
        output: { artifactRevision: revision },
      },
    },
    {
      path: '/v1/projects/project-1/workflow-runs/run-1/execute',
      body: { nodeKey: 'quality' },
    },
    {
      path: '/v1/projects/project-1/workflow-runs/run-1/approve',
      body: {
        nodeKey: 'prototype-review:slice-a',
        resolution: 'approve',
        reason: '',
      },
    },
  ])
})

test('implementation decisions carry version ETags and apply returns a workspace revision', async () => {
  const calls: Array<{ path: string; headers: Headers; body: unknown }> = []
  const client = flowClient((path, init) => {
    calls.push({
      path,
      headers: new Headers(init?.headers),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    })
    if (path.endsWith('/decisions')) return json({ id: 'proposal-1', version: 8, status: 'ready' })
    return json({ id: 'workspace-r1', artifactId: 'workspace-1', revisionNumber: 1 }, 200)
  })

  const proposal = { id: 'proposal-1', version: 7 }
  await client.decideImplementationOperation(proposal, 'operation-1', 'accepted')
  await client.applyImplementationProposal({ id: proposal.id, version: 8 })

  assert.equal(calls[0].path, '/v1/implementation-proposals/proposal-1/decisions')
  assert.equal(calls[0].headers.get('if-match'), '"implementation-proposal:proposal-1:7"')
  assert.deepEqual(calls[0].body, {
    operationId: 'operation-1',
    decision: 'accepted',
    reason: '',
    version: 7,
  })
  assert.equal(calls[1].path, '/v1/implementation-proposals/proposal-1/apply')
  assert.equal(calls[1].headers.get('if-match'), '"implementation-proposal:proposal-1:8"')
  assert.deepEqual(calls[1].body, { version: 8 })
})

test('build generation is pinned to a build manifest and has no browser fallback', async () => {
  let call: { path: string; body: unknown } | undefined
  const client = flowClient((path, init) => {
    call = {
      path,
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    }
    return json({ proposal: { id: 'implementation-proposal-1' }, provider: 'openai', model: 'gpt-5' }, 201)
  })
  await client.generateImplementation(
    'build-manifest-1',
    'gpt-5',
    'Build only from the exact frozen input.',
  )
  assert.deepEqual(call, {
    path: '/v1/build-manifests/build-manifest-1/generate',
    body: {
      model: 'gpt-5',
      instruction: 'Build only from the exact frozen input.',
    },
  })
})

test('new workflow definitions start as the complete executable product delivery loop', () => {
  const definition = starterDefinition()
  assert.equal(definition.schemaVersion, '1')
  assert.equal(definition.nodes.length, 18)
  assert.equal(definition.edges.length, 17)
  assert.deepEqual(definition.nodes.map((node) => node.type), [
    'artifact_input',
    'human_edit',
    'review_gate',
    'ai_transform',
    'human_edit',
    'review_gate',
    'ai_transform',
    'human_edit',
    'review_gate',
    'fan_out',
    'ai_transform',
    'human_edit',
    'review_gate',
    'merge',
    'manifest_compiler',
    'workbench_build',
    'quality_gate',
    'publish',
  ])
  assert.deepEqual(definition.edges.at(0), { id: 'edge-01', from: 'source', to: 'project-brief-edit' })
  assert.deepEqual(definition.edges.at(-1), { id: 'edge-17', from: 'quality', to: 'publish' })
  assert.equal(definition.nodes.find((node) => node.id === 'compile-manifest')?.manifestCompiler?.hook, 'application-build-manifest/v1')
  assert.ok(definition.nodes.every((node) => node.inputSchema?.type === 'object' && node.outputSchema?.type === 'object'))
  assert.ok(definition.edges.every((edge) => edge.fromPort === undefined && edge.toPort === undefined))
  assert.ok(definition.nodes.filter((node) => node.type === 'human_edit').every((node) => node.humanEdit?.requiredRole === 'editor'))
  assert.ok(definition.nodes.filter((node) => node.type === 'review_gate').every((node) => node.reviewGate?.requiredRole === 'owner'))
  assert.equal(definition.nodes.find((node) => node.id === 'quality')?.qualityGate?.requiredRole, 'editor')
  assert.equal(definition.nodes.find((node) => node.id === 'publish')?.publish?.requiredRole, 'admin')
  assert.equal(parseEditableDefinition(JSON.stringify(definition), true).error, undefined)
})

test('workflow JSON parsing rejects null and malformed node and edge entries', () => {
  const definition = starterDefinition()
  const nullNode = parseEditableDefinition(JSON.stringify({ ...definition, nodes: [null] }))
  const nullEdge = parseEditableDefinition(JSON.stringify({ ...definition, edges: [null] }))
  const malformedNode = parseEditableDefinition(JSON.stringify({
    ...definition,
    nodes: [{
      id: 'source',
      name: 'Broken',
      type: 'artifact_input',
      inputSchema: { type: 'object' },
      outputSchema: { type: 'object' },
      artifactInput: null,
    }],
    edges: [],
  }))
  assert.match(nullNode.error ?? '', /nodes\[0\].*object/i)
  assert.match(nullEdge.error ?? '', /edges\[0\].*object/i)
  assert.match(malformedNode.error ?? '', /artifactInput.*object/i)
  assert.match(
    parseEditableDefinition(JSON.stringify({ ...definition, nodes: [null] }), true).error ?? '',
    /nodes\[0\].*object/i,
  )
})

test('Human Edit candidates are narrowed to proposal targets and ambiguous lineage requires selection', () => {
  const brief = versionedResource('brief', 'brief-r1', 'Project brief', { kind: 'requirement' })
  const requirementsA = versionedResource('requirements-a', 'requirements-a-r2', 'Requirements A', { kind: 'requirement' })
  const requirementsB = versionedResource('requirements-b', 'requirements-b-r3', 'Requirements B', { kind: 'requirement' })
  const proposals = [
    proposal('proposal-a', 'payload-a', requirementsA.artifact.id, requirementsA.latestRevision),
    proposal('proposal-b', 'payload-b', requirementsB.artifact.id, requirementsB.latestRevision),
  ]
  const node = {
    id: 'node-run',
    runId: 'run-1',
    key: 'requirements-edit',
    definitionNodeId: 'requirements-edit',
    type: 'human_edit',
    status: 'waiting_input',
    attempt: 1,
  }
  const definitionNode = {
    id: 'requirements-edit',
    name: 'Edit requirements',
    type: 'human_edit',
    humanEdit: { artifactType: 'document', requiredRole: 'editor' },
  }
  const run = workflowRun(node, [
    lineageBinding([revisionRefFixture(brief.latestRevision)], { id: 'proposal-a', payloadHash: 'payload-a' }),
    lineageBinding([revisionRefFixture(brief.latestRevision)], { id: 'proposal-b', payloadHash: 'payload-b' }),
  ])
  const artifacts = artifactSnapshot({
    documents: [brief, requirementsA, requirementsB],
    proposals,
  })
  const resolution = revisionCandidates(definitionNode as never, node as never, run as never, artifacts as never)
  assert.deepEqual(resolution.candidates.map((candidate) => candidate.artifactId), [
    'requirements-a',
    'requirements-b',
  ])
  assert.equal(resolveCandidateSelection(resolution.candidates, ''), undefined)
  assert.equal(
    resolveCandidateSelection(resolution.candidates, resolution.candidates.at(1)?.key ?? '')?.artifactId,
    'requirements-b',
  )
})

test('slice Human Edit candidates cannot cross into another delivery-slice proposal', () => {
  const prototypeA = versionedResource('prototype-a', 'prototype-a-r1', 'Prototype A', { pageSpecRevision: { artifactId: 'page-a', revisionId: 'page-a-r1', contentHash: 'sha256:page-a-r1' } })
  const prototypeB = versionedResource('prototype-b', 'prototype-b-r1', 'Prototype B', { pageSpecRevision: { artifactId: 'page-b', revisionId: 'page-b-r1', contentHash: 'sha256:page-b-r1' } })
  const proposals = [
    proposal('prototype-proposal-a', 'prototype-payload-a', prototypeA.artifact.id, prototypeA.latestRevision),
    proposal('prototype-proposal-b', 'prototype-payload-b', prototypeB.artifact.id, prototypeB.latestRevision),
  ]
  const sliceA = deliverySliceRef('slice-a', 'page-a', 'prototype-a')
  const sliceB = deliverySliceRef('slice-b', 'page-b', 'prototype-b')
  const node = {
    id: 'prototype-node-a', runId: 'run-1', key: 'prototype-edit:slice-a',
    definitionNodeId: 'prototype-edit', sliceId: 'slice-a', type: 'human_edit',
    status: 'waiting_input', attempt: 1,
  }
  const definitionNode = {
    id: 'prototype-edit', name: 'Edit prototype', type: 'human_edit',
    humanEdit: { artifactType: 'prototype', requiredRole: 'editor' },
  }
  const run = workflowRun(node, [
    lineageBinding([], { id: 'prototype-proposal-a', payloadHash: 'prototype-payload-a' }, [sliceA]),
    lineageBinding([], { id: 'prototype-proposal-b', payloadHash: 'prototype-payload-b' }, [sliceB]),
  ])
  const resolution = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({ prototypes: [prototypeA, prototypeB], proposals }) as never,
  )
  assert.deepEqual(resolution.candidates.map((candidate) => candidate.artifactId), ['prototype-a'])
})

test('Blueprint delivery slices include only PageSpecs pinned to the selected Blueprint revision', () => {
  const pageA = versionedResource('page-a', 'page-a-r1', 'Page A', {
    blueprintPageNodeId: 'page-node-a',
    title: 'Page A',
  })
  const pageB = versionedResource('page-b', 'page-b-r1', 'Page B', {
    blueprintPageNodeId: 'page-node-b',
    title: 'Page B',
  })
  const pageARef = revisionRefFixture(pageA.latestRevision)
  const blueprint = versionedResource('blueprint-a', 'blueprint-a-r2', 'Blueprint A', {
    nodes: [],
    edges: [],
    validation: [],
    pageSpecRefs: [pageARef],
  })
  const blueprintRef = revisionRefFixture(blueprint.latestRevision)
  const unrelatedBlueprint = versionedResource('blueprint-b', 'blueprint-b-r1', 'Blueprint B', {
    nodes: [],
    edges: [],
    validation: [],
  })
  pageA.latestRevision.sourceVersions = [blueprintRef]
  pageB.latestRevision.sourceVersions = [revisionRefFixture(unrelatedBlueprint.latestRevision)]

  const result = deliverySliceContext(blueprintRef, artifactSnapshot({
    blueprints: [blueprint, unrelatedBlueprint],
    pageSpecs: [pageA, pageB],
  }) as never)
  assert.equal(result.error, undefined)
  assert.equal(result.context?.deliverySlices.length, 1)
  assert.equal(result.context?.deliverySlices.at(0)?.pageSpec.artifactId, 'page-a')
})

test('Blueprint delivery slices derive multiple PageSpecs from exact page-node anchors', () => {
  const pageA = versionedResource('page-a', 'page-a-r1', 'Page A', {
    blueprintPageNodeId: 'page-node-a',
    title: 'Page A',
  })
  const pageB = versionedResource('page-b', 'page-b-r1', 'Page B', {
    blueprintPageNodeId: 'page-node-b',
    title: 'Page B',
  })
  const wrongAnchor = versionedResource('page-wrong', 'page-wrong-r1', 'Wrong anchor', {
    blueprintPageNodeId: 'page-node-b',
    title: 'Wrong anchor',
  })
  const blueprint = versionedResource('blueprint-a', 'blueprint-a-r2', 'Blueprint A', {
    nodes: [
      { id: 'page-node-a', kind: 'page' },
      { id: 'page-node-b', kind: 'page' },
    ],
    edges: [],
    validation: [],
    pageSpecRefs: [],
  })
  const blueprintRef = revisionRefFixture(blueprint.latestRevision)
  pageA.latestRevision.sourceVersions = [{ ...blueprintRef, anchorId: 'page-node-a' }]
  pageB.latestRevision.sourceVersions = [{ ...blueprintRef, anchorId: 'page-node-b' }]
  wrongAnchor.latestRevision.sourceVersions = [{ ...blueprintRef, anchorId: 'page-node-a' }]

  const result = deliverySliceContext(blueprintRef, artifactSnapshot({
    blueprints: [blueprint],
    pageSpecs: [pageA, pageB, wrongAnchor],
  }) as never)

  assert.equal(result.error, undefined)
  assert.deepEqual(
    result.context?.deliverySlices.map((slice) => slice.pageSpec.artifactId),
    ['page-a', 'page-b'],
  )
  assert.deepEqual(
    result.context?.deliverySlices.map((slice) => slice.key),
    ['page-node-a', 'page-node-b'],
  )
})

function versionedResource<TContent>(
  artifactId: string,
  revisionId: string,
  title: string,
  content: TContent,
) {
  return {
    artifact: {
      id: artifactId,
      projectId: 'project-1',
      title,
      status: 'draft',
      createdBy: 'user-1',
      createdAt: '2026-07-10T00:00:00Z',
      updatedAt: '2026-07-10T00:00:00Z',
      etag: `"${artifactId}"`,
    },
    latestRevision: {
      id: revisionId,
      artifactId,
      revisionNumber: 1,
      content,
      contentHash: `sha256:${revisionId}`,
      sourceVersions: [] as Array<ReturnType<typeof revisionRefFixture> & { anchorId?: string }>,
      createdBy: 'user-1',
      createdAt: '2026-07-10T00:00:00Z',
    },
  }
}

function revisionRefFixture(revision: { id: string; artifactId: string; revisionNumber: number; contentHash: string }) {
  return {
    artifactId: revision.artifactId,
    revisionId: revision.id,
    revisionNumber: revision.revisionNumber,
    contentHash: revision.contentHash,
  }
}

function proposal(id: string, payloadHash: string, artifactId: string, revision: { id: string; artifactId: string; revisionNumber: number; contentHash: string }) {
  return {
    id,
    projectId: 'project-1',
    artifactId,
    manifest: { id: `${id}-manifest`, hash: `${id}-manifest-hash` },
    baseRevision: revisionRefFixture(revision),
    payloadHash,
    status: 'open',
    version: 1,
    operations: [],
    assumptions: [],
    questions: [],
    createdBy: 'user-1',
    createdAt: '2026-07-10T00:00:00Z',
  }
}

function lineageBinding(
  artifactRevisions: readonly ReturnType<typeof revisionRefFixture>[],
  outputProposal?: { readonly id: string; readonly payloadHash: string },
  deliverySliceRefs: readonly unknown[] = [],
) {
  return {
    edgeId: `edge-${outputProposal?.id ?? 'lineage'}`,
    fromPort: 'default',
    toPort: 'default',
    source: {
      runId: 'run-1',
      nodeKey: 'predecessor',
      definitionNodeId: 'predecessor',
      artifactRevisions,
      deliverySliceRefs,
      ...(outputProposal ? { outputProposal } : {}),
    },
    output: {},
    outputHash: 'output-hash',
    value: {},
    valueHash: 'value-hash',
  }
}

function deliverySliceRef(id: string, pageSpecArtifactId: string, prototypeArtifactId: string) {
  return {
    id,
    key: id,
    fanOutNodeId: 'pages',
    blueprint: { artifactId: 'blueprint-a', revisionId: 'blueprint-a-r1', contentHash: 'sha256:blueprint-a-r1' },
    pageSpec: { artifactId: pageSpecArtifactId, revisionId: `${pageSpecArtifactId}-r1`, contentHash: `sha256:${pageSpecArtifactId}-r1` },
    prototype: { artifactId: prototypeArtifactId, revisionId: `${prototypeArtifactId}-r1`, contentHash: `sha256:${prototypeArtifactId}-r1` },
  }
}

function workflowRun(node: Record<string, unknown>, bindings: readonly unknown[]) {
  return {
    id: 'run-1',
    context: {
      nodes: {
        [String(node.key)]: {
          definitionNodeId: String(node.definitionNodeId),
          maxAttempts: 1,
          timeoutNanos: 1,
          input: { bindings, hash: 'input-hash' },
        },
      },
      slices: {},
    },
  }
}

function artifactSnapshot(overrides: Record<string, unknown> = {}) {
  return {
    documents: [],
    blueprints: [],
    pageSpecs: [],
    prototypes: [],
    proposals: [],
    traces: [],
    ...overrides,
  }
}

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
  if (failed > 0) process.exitCode = 1
  else console.log(`${tests.length} platform flow test(s) passed.`)
}

void main()
