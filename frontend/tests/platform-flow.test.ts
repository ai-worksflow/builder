import assert from 'node:assert/strict'
import {
  deliverySliceContext,
  estimateWorkflowSemanticStates,
  parseEditableDefinition,
  reviewGateApprovalReadiness,
  revisionCandidates,
  starterWorkflowDefinition as starterDefinition,
  workflowRoleSatisfies,
} from '../lib/platform/workflow-ui-contract'
import {
  projectBriefEntryAction,
} from '../lib/platform/workflow-entry'
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
  await client.capabilities('project-1')
  await client.createDefinition('project-1', {
    key: 'custom-flow',
    title: 'Custom flow',
    name: 'Custom flow',
    schemaVersion: 'workflow/v2',
    inputContract: {
      capability: 'project_brief', manifestJobTypes: ['workflow_start'], artifactKinds: ['project_brief'],
      minimumArtifacts: 1, maximumArtifacts: 1, requireApproved: false,
      requiredSourcePurposes: ['project_brief'], manifestSchemaContracts: { workflow_start: 'workflow-input/v1' },
    },
    outputContract: {
      capability: 'application', producedArtifactKinds: ['workspace'], terminalOutcome: 'deployment', terminalNodeType: 'publish',
    },
    nodes: [],
    edges: [],
  })
  await client.listDefinitionVersions('project-1', 'definition-1')
  await client.listRuns('project-1', { status: 'waiting_input' }, { limit: 20 })

  assert.deepEqual(calls.map((call) => call.path), [
    '/v1/projects/project-1/workflow-definitions',
    '/v1/projects/project-1/workflow-capabilities',
    '/v1/projects/project-1/workflow-definitions',
    '/v1/projects/project-1/workflow-definitions/definition-1/versions',
    '/v1/projects/project-1/workflow-runs',
  ])
  assert.equal(calls[2].method, 'POST')
  assert.deepEqual(calls[2].body, {
    key: 'custom-flow',
    title: 'Custom flow',
    name: 'Custom flow',
    schemaVersion: 'workflow/v2',
    inputContract: {
      capability: 'project_brief', manifestJobTypes: ['workflow_start'], artifactKinds: ['project_brief'],
      minimumArtifacts: 1, maximumArtifacts: 1, requireApproved: false,
      requiredSourcePurposes: ['project_brief'], manifestSchemaContracts: { workflow_start: 'workflow-input/v1' },
    },
    outputContract: {
      capability: 'application', producedArtifactKinds: ['workspace'], terminalOutcome: 'deployment', terminalNodeType: 'publish',
    },
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

test('Blueprint selection compile is conditional, idempotent, and pins exact anchors server-side', async () => {
  const calls: Array<{ path: string; headers: Headers; body: unknown }> = []
  const client = flowClient((path, init) => {
    calls.push({
      path,
      headers: new Headers(init?.headers),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    })
    return json({
      id: 'selection-1', projectId: 'project-1', jobType: 'blueprint.selection',
      deliverySliceId: 'selection-hash', sources: [], constraints: {},
      outputSchemaVersion: 'blueprint-selection/v1', createdBy: 'user-1',
      createdAt: new Date(0).toISOString(), hash: 'a'.repeat(64),
    }, 201)
  })

  await client.compileBlueprintSelection('project-1', {
    blueprintRevision: {
      artifactId: 'blueprint-1', revisionId: 'revision-7', revisionNumber: 7,
      contentHash: 'b'.repeat(64),
    },
    nodeIds: ['page-orders', 'api-orders'],
  }, '"artifact:blueprint-1:9"')

  assert.equal(calls[0].path, '/v1/projects/project-1/blueprint-selections/compile')
  assert.equal(calls[0].headers.get('if-match'), '"artifact:blueprint-1:9"')
  assert.ok(calls[0].headers.get('idempotency-key'))
  assert.deepEqual(calls[0].body, {
    blueprintRevision: {
      artifactId: 'blueprint-1', revisionId: 'revision-7', contentHash: 'b'.repeat(64),
    },
    nodeIds: ['page-orders', 'api-orders'],
  })
})

test('Project Brief entry checkpoints newer drafts and fails closed for approved inputs', () => {
  const approvedRevision = { id: 'brief-r1', contentHash: 'approved-hash' }
  const latestRevision = { id: 'brief-r1', contentHash: 'approved-hash' }
  const newerDraft = { contentHash: 'newer-draft-hash' }
  assert.equal(projectBriefEntryAction({
    requireApproved: false,
    approvedRevision,
    latestRevision,
    draft: newerDraft,
  }), 'checkpoint_draft')
  assert.equal(projectBriefEntryAction({
    requireApproved: true,
    approvedRevision,
    latestRevision,
    draft: newerDraft,
  }), 'blocked_unapproved_changes')
  assert.equal(projectBriefEntryAction({
    requireApproved: true,
    approvedRevision,
    latestRevision: { id: 'brief-r2', contentHash: 'unapproved-hash' },
  }), 'blocked_unapproved_changes')
  assert.equal(projectBriefEntryAction({
    requireApproved: false,
    approvedRevision,
    latestRevision,
    draft: { contentHash: approvedRevision.contentHash },
  }), 'use_existing_revision')
})

test('review role ranking does not treat admin as owner', () => {
  assert.equal(workflowRoleSatisfies('owner', 'owner'), true)
  assert.equal(workflowRoleSatisfies('admin', 'owner'), false)
  assert.equal(workflowRoleSatisfies('admin', 'editor'), true)
  assert.equal(workflowRoleSatisfies('editor', 'admin'), false)
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
  await client.resolveReview(
    'project-1',
    'run-1',
    'prototype-review:slice-a',
    'approve',
    'Solo review completed',
    true,
  )

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
    {
      path: '/v1/projects/project-1/workflow-runs/run-1/approve',
      body: {
        nodeKey: 'prototype-review:slice-a',
        resolution: 'approve',
        reason: 'Solo review completed',
        soloReviewConfirmed: true,
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

test('Workbench regeneration sends an exact proposal CAS instead of silently coexisting', async () => {
  let body: unknown
  const client = flowClient((_path, init) => {
    body = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
    return json({ proposal: { id: 'replacement' }, provider: 'openai', model: 'gpt-5' }, 201)
  })
  await client.generateImplementation(
    'build-manifest-1',
    'gpt-5',
    'Regenerate from reviewed feedback.',
    { id: 'active-proposal-1', version: 7 },
  )
  assert.deepEqual(body, {
    model: 'gpt-5',
    instruction: 'Regenerate from reviewed feedback.',
    replaceProposalId: 'active-proposal-1',
    replaceProposalVersion: 7,
  })
})

test('Workbench lineage hydration and exact workspace rebase use root-scoped routes', async () => {
  const calls: Array<{ path: string; method: string; body: unknown }> = []
  const client = flowClient((path, init) => {
    calls.push({
      path,
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    })
    if (path.endsWith('/lineage-state')) {
      return json({ rootBundleId: 'bundle-2', activeBundle: { id: 'bundle-2' }, lineage: [] })
    }
    return json({
      id: 'bundle-2-w1',
      rootBuildManifestId: 'bundle-2',
      derivedFromBuildManifestId: 'bundle-2',
      currentWorkspaceRevision: {
        artifactId: 'workspace-1', revisionId: 'workspace-r1', contentHash: 'b'.repeat(64),
      },
    }, 201)
  })

  await client.getWorkbenchBundleLineageState('bundle-2')
  await client.rebaseWorkbenchBundle('bundle-2', {
    artifactId: 'workspace-1',
    revisionId: 'workspace-r1',
    revisionNumber: 1,
    contentHash: 'b'.repeat(64),
  })

  assert.deepEqual(calls, [
    {
      path: '/v1/build-manifests/bundle-2/lineage-state',
      method: 'GET',
      body: undefined,
    },
    {
      path: '/v1/build-manifests/bundle-2/rebase',
      method: 'POST',
      body: {
        workspaceRevision: {
          artifactId: 'workspace-1',
          revisionId: 'workspace-r1',
          contentHash: 'b'.repeat(64),
        },
      },
    },
  ])
})

test('new workflow definitions start as the complete executable product delivery loop', () => {
  const definition = starterDefinition()
  assert.equal(definition.schemaVersion, '4')
  assert.equal(definition.inputContract.capability, 'project_brief')
  assert.deepEqual([definition.inputContract.minimumArtifacts, definition.inputContract.maximumArtifacts], [1, 1])
  assert.equal(definition.outputContract.capability, 'application')
  assert.equal(definition.nodes.length, 22)
  assert.equal(definition.edges.length, 21)
  assert.deepEqual(definition.nodes.map((node) => node.type), [
    'artifact_input',
    'ai_transform',
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
    'ai_transform',
    'human_edit',
    'review_gate',
    'merge',
    'manifest_compiler',
    'workbench_build',
    'quality_gate',
    'publish',
  ])
  assert.deepEqual(definition.edges.at(0), { id: 'edge-01', from: 'source', to: 'project-brief-ai' })
  assert.deepEqual(definition.edges.at(-1), { id: 'edge-21', from: 'quality', to: 'publish' })
  assert.deepEqual(definition.nodes.find((node) => node.id === 'pages')?.fanOut, {
    itemsPath: '/blueprintPages', sliceKeyPath: '/key', mergeNodeId: 'pages-merged',
    maxParallel: 4, maxItems: 100, itemKind: 'blueprint_page',
  })
  assert.equal(definition.nodes.find((node) => node.id === 'project-brief-ai')?.aiTransform?.jobType, 'refine_project_brief')
  assert.equal(definition.nodes.find((node) => node.id === 'page-spec-ai')?.aiTransform?.jobType, 'generate_page_spec')
  assert.equal(definition.nodes.find((node) => node.id === 'page-spec-edit')?.humanEdit?.artifactKind, 'page_spec')
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

test('workflow authoring estimates Condition state growth before server validation', () => {
  const schema = { type: 'object' as const }
  const nodes = [{
    id: 'entry', name: 'entry', type: 'transform' as const,
    inputSchema: schema, outputSchema: schema, transform: { transform: 'selection_passthrough' },
  }]
  const edges: Array<{ id: string; from: string; to: string; fromPort?: string }> = []
  let previous = 'entry'
  for (let index = 1; index <= 4; index += 1) {
    const condition = `condition-${index}`
    const join = `join-${index}`
    nodes.push({
      id: condition, name: condition, type: 'condition', inputSchema: schema, outputSchema: schema,
      condition: { branches: [{ name: 'yes', expression: '{"==":[1,1]}' }, { name: 'no', default: true }] },
    } as unknown as typeof nodes[number])
    nodes.push({
      id: join, name: join, type: 'transform', inputSchema: schema, outputSchema: schema,
      transform: { transform: 'selection_passthrough' },
    })
    edges.push({ id: `edge-${index}-in`, from: previous, to: condition })
    edges.push({ id: `edge-${index}-yes`, from: condition, fromPort: 'yes', to: join })
    edges.push({ id: `edge-${index}-no`, from: condition, fromPort: 'no', to: join })
    previous = join
  }
  const definition = { nodes, edges } as Parameters<typeof estimateWorkflowSemanticStates>[0]
  assert.deepEqual(estimateWorkflowSemanticStates(definition, 8), {
    peakStates: 9, maximumStates: 8, exceeded: true,
  })
  assert.deepEqual(estimateWorkflowSemanticStates(definition, 32), {
    peakStates: 16, maximumStates: 32, exceeded: false,
  })
})

test('Human Edit rejects ambiguous multi-producer lineage', () => {
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
  assert.equal(resolution.candidates.length, 0)
  assert.match(resolution.error ?? '', /exactly one proposal producer/i)
})

test('Human Edit resolves one proposal pin propagated through a Condition and rejects malformed pins', () => {
  const target = versionedResource('requirements-pin', 'requirements-pin-r2', 'Pinned requirements', { kind: 'requirement' })
  Object.assign(target.artifact, { kind: 'product_requirements' })
  const linkedProposal = proposal('proposal-pin', 'payload-pin', target.artifact.id, target.latestRevision)
  const node = {
    id: 'requirements-pin-node', runId: 'run-1', key: 'requirements-edit',
    definitionNodeId: 'requirements-edit', type: 'human_edit', status: 'waiting_input', attempt: 1,
  }
  const definitionNode = {
    id: 'requirements-edit', name: 'Edit requirements', type: 'human_edit',
    humanEdit: { artifactType: 'document', artifactKind: 'product_requirements', requiredRole: 'editor' },
  }
  const pin = {
    proposal: { id: linkedProposal.id, payloadHash: linkedProposal.payloadHash },
    manifest: { id: 'manifest-pin', hash: 'sha256:manifest-pin' },
    producerNodeKey: 'requirements-ai', producerDefinitionNodeId: 'requirements-ai',
  }
  const run = workflowRun(node, [lineageBinding([], undefined, [], [pin])])
  const resolution = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({ documents: [target], proposals: [linkedProposal] }) as never,
  )
  assert.deepEqual(resolution.candidates.map((candidate) => candidate.artifactId), ['requirements-pin'])

  const unavailable = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({ documents: [target], proposals: [] }) as never,
  )
  assert.match(unavailable.error ?? '', /not available in the current workspace snapshot/i)
  const hashMismatch = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({
      documents: [target],
      proposals: [{ ...linkedProposal, payloadHash: 'different-payload-hash' }],
    }) as never,
  )
  assert.match(hashMismatch.error ?? '', /does not match the payload hash pinned/i)

  const multi = workflowRun(node, [lineageBinding([], undefined, [], [pin, { ...pin, producerNodeKey: 'requirements-ai-2' }])])
  assert.match(revisionCandidates(definitionNode as never, node as never, multi as never, artifactSnapshot({ documents: [target], proposals: [linkedProposal] }) as never).error ?? '', /exactly one proposal producer/i)
  const malformed = workflowRun(node, [lineageBinding([], undefined, [], [{ ...pin, manifest: {} }])])
  assert.match(revisionCandidates(definitionNode as never, node as never, malformed as never, artifactSnapshot({ documents: [target], proposals: [linkedProposal] }) as never).error ?? '', /malformed proposal lineage pin/i)
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

test('Human Edit never offers a Proposal base revision as the edited result', () => {
  const target = versionedResource('requirements-a', 'requirements-base-r1', 'Requirements A', { kind: 'requirement' })
  Object.assign(target.artifact, { kind: 'product_requirements' })
  const linkedProposal = proposal('proposal-a', 'payload-a', target.artifact.id, target.latestRevision)
  delete (target.latestRevision as typeof target.latestRevision & { proposalId?: string }).proposalId
  const node = {
    id: 'requirements-node', runId: 'run-1', key: 'requirements-edit',
    definitionNodeId: 'requirements-edit', type: 'human_edit', status: 'waiting_input', attempt: 1,
  }
  const definitionNode = {
    id: 'requirements-edit', name: 'Edit requirements', type: 'human_edit',
    humanEdit: { artifactType: 'document', artifactKind: 'product_requirements', requiredRole: 'editor' },
  }
  const run = workflowRun(node, [
    lineageBinding([], { id: linkedProposal.id, payloadHash: linkedProposal.payloadHash }),
  ])
  const beforeRevision = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({ documents: [target], proposals: [linkedProposal] }) as never,
  )
  assert.equal(beforeRevision.candidates.length, 0)
  assert.match(beforeRevision.error ?? '', /Apply the linked Proposal.*create an immutable revision/i)

  Object.assign(target.latestRevision, { proposalId: linkedProposal.id, changeSource: 'ai_proposal' })
  const afterRevision = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({ documents: [target], proposals: [linkedProposal] }) as never,
  )
  assert.deepEqual(afterRevision.candidates.map((candidate) => candidate.ref.revisionId), ['requirements-base-r1'])
})

test('PageSpec branch Human Edit selects the exact applied PageSpec revision', () => {
  const pageSpec = versionedResource('page-a', 'page-a-r2', 'Page A specification', {
    blueprintPageNodeId: 'page-node-a',
    title: 'Page A',
  })
  Object.assign(pageSpec.artifact, { kind: 'page_spec' })
  const linkedProposal = proposal('page-spec-proposal-a', 'page-spec-payload-a', pageSpec.artifact.id, pageSpec.latestRevision)
  const node = {
    id: 'page-spec-node-a', runId: 'run-1', key: 'page-spec-edit:slice-a',
    definitionNodeId: 'page-spec-edit', sliceId: 'slice-a', type: 'human_edit',
    status: 'waiting_input', attempt: 1,
  }
  const definitionNode = {
    id: 'page-spec-edit', name: 'Edit PageSpec', type: 'human_edit',
    humanEdit: { artifactType: 'blueprint', artifactKind: 'page_spec', requiredRole: 'editor' },
  }
  const run = workflowRun(node, [lineageBinding(
    [],
    { id: linkedProposal.id, payloadHash: linkedProposal.payloadHash },
    [deliverySliceRef('slice-a', pageSpec.artifact.id, 'prototype-a')],
  )])
  const resolution = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({ pageSpecs: [pageSpec], proposals: [linkedProposal] }) as never,
  )
  assert.deepEqual(resolution.candidates.map((candidate) => candidate.ref), [revisionRefFixture(pageSpec.latestRevision)])
})

test('PageSpec Human Edit exposes the exact open Proposal edit target', () => {
  const pageSpec = versionedResource('page-open', 'page-open-r1', 'Open PageSpec', {
    blueprintPageNodeId: 'page-node-open',
    title: 'Open Page',
  })
  Object.assign(pageSpec.artifact, { kind: 'page_spec' })
  const linkedProposal = {
    ...proposal('page-spec-proposal-open', 'page-spec-payload-open', pageSpec.artifact.id, pageSpec.latestRevision),
    status: 'open' as const,
  }
  delete (pageSpec.latestRevision as typeof pageSpec.latestRevision & { proposalId?: string }).proposalId
  const node = {
    id: 'page-spec-node-open', runId: 'run-1', key: 'page-spec-edit:slice-open',
    definitionNodeId: 'page-spec-edit', sliceId: 'slice-open', type: 'human_edit',
    status: 'waiting_input', attempt: 1,
  }
  const definitionNode = {
    id: 'page-spec-edit', name: 'Edit PageSpec', type: 'human_edit',
    humanEdit: { artifactType: 'blueprint', artifactKind: 'page_spec', requiredRole: 'editor' },
  }
  const run = workflowRun(node, [lineageBinding(
    [],
    { id: linkedProposal.id, payloadHash: linkedProposal.payloadHash },
    [deliverySliceRef('slice-open', pageSpec.artifact.id, 'prototype-open')],
  )])

  const resolution = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({ pageSpecs: [pageSpec], proposals: [linkedProposal] }) as never,
  )

  assert.deepEqual(resolution.editorTarget, {
    artifactId: pageSpec.artifact.id,
    artifactKind: 'page_spec',
    proposalId: linkedProposal.id,
    proposalStatus: 'open',
  })
  assert.equal(resolution.candidates.length, 0)
  assert.match(resolution.error ?? '', /Review and apply the linked Proposal/i)
})

test('PageSpec Human Edit retains the applied Proposal target while awaiting its immutable revision', () => {
  const pageSpec = versionedResource('page-awaiting-revision', 'page-awaiting-revision-r1', 'Awaiting PageSpec', {
    blueprintPageNodeId: 'page-node-awaiting-revision',
    title: 'Awaiting Page',
  })
  Object.assign(pageSpec.artifact, { kind: 'page_spec' })
  const linkedProposal = proposal(
    'page-spec-proposal-awaiting-revision',
    'page-spec-payload-awaiting-revision',
    pageSpec.artifact.id,
    pageSpec.latestRevision,
  )
  delete (pageSpec.latestRevision as typeof pageSpec.latestRevision & { proposalId?: string }).proposalId
  const node = {
    id: 'page-spec-node-awaiting-revision', runId: 'run-1', key: 'page-spec-edit:slice-awaiting-revision',
    definitionNodeId: 'page-spec-edit', sliceId: 'slice-awaiting-revision', type: 'human_edit',
    status: 'waiting_input', attempt: 1,
  }
  const definitionNode = {
    id: 'page-spec-edit', name: 'Edit PageSpec', type: 'human_edit',
    humanEdit: { artifactType: 'blueprint', artifactKind: 'page_spec', requiredRole: 'editor' },
  }
  const run = workflowRun(node, [lineageBinding(
    [],
    { id: linkedProposal.id, payloadHash: linkedProposal.payloadHash },
    [deliverySliceRef('slice-awaiting-revision', pageSpec.artifact.id, 'prototype-awaiting-revision')],
  )])

  const resolution = revisionCandidates(
    definitionNode as never,
    node as never,
    run as never,
    artifactSnapshot({ pageSpecs: [pageSpec], proposals: [linkedProposal] }) as never,
  )

  assert.deepEqual(resolution.editorTarget, {
    artifactId: pageSpec.artifact.id,
    artifactKind: 'page_spec',
    proposalId: linkedProposal.id,
    proposalStatus: 'applied',
  })
  assert.equal(resolution.candidates.length, 0)
  assert.match(resolution.error ?? '', /create an immutable revision before submitting/i)
})

test('Review Gate readiness requires canonical approval of the exact materialized revision', () => {
  const target = versionedResource('brief-review', 'brief-review-r4', 'Project brief', { kind: 'projectBrief' })
  const revision = revisionRefFixture(target.latestRevision)
  const priorApproved = {
    ...target.latestRevision,
    id: 'brief-review-r3',
    revisionNumber: 3,
    contentHash: 'sha256:brief-review-r3',
  }
  Object.assign(target, { approvedRevision: priorApproved })
  const node = reviewGateNode()
  const run = workflowRun(node, [lineageBinding(
    [revision],
    undefined,
    [],
    [],
    { materializedArtifactRevisions: [revision], outputRevisionId: revision.revisionId },
  )])
  const artifacts = artifactSnapshot({ documents: [target] })

  const pending = reviewGateApprovalReadiness(
    reviewGateDefinition(true) as never,
    node as never,
    run as never,
    artifacts as never,
  )
  assert.equal(pending.ready, false)
  assert.deepEqual(pending.revisions, [revision])
  assert.deepEqual(pending.pendingRevisions, [revision])
  assert.match(pending.error ?? '', /not canonically approved/i)

  Object.assign(target, {
    approvedRevision: { ...target.latestRevision, contentHash: 'sha256:different-content' },
  })
  const hashMismatch = reviewGateApprovalReadiness(
    reviewGateDefinition(true) as never,
    node as never,
    run as never,
    artifacts as never,
  )
  assert.equal(hashMismatch.ready, false)
  assert.deepEqual(hashMismatch.pendingRevisions, [revision])

  Object.assign(target, { approvedRevision: { ...target.latestRevision } })
  const approved = reviewGateApprovalReadiness(
    reviewGateDefinition(true) as never,
    node as never,
    run as never,
    artifacts as never,
  )
  assert.equal(approved.ready, true)
  assert.deepEqual(approved.revisions, [revision])
  assert.deepEqual(approved.pendingRevisions, [])
  assert.equal(approved.error, undefined)
})

test('governed Review Gate readiness supports the legacy output revision fallback', () => {
  const upstream = versionedResource('brief-upstream', 'brief-upstream-r1', 'Upstream brief', { kind: 'projectBrief' })
  const target = versionedResource('brief-edited', 'brief-edited-r2', 'Edited brief', { kind: 'projectBrief' })
  const upstreamRevision = revisionRefFixture(upstream.latestRevision)
  const targetRevision = revisionRefFixture(target.latestRevision)
  Object.assign(target, { approvedRevision: { ...target.latestRevision } })
  const node = reviewGateNode()
  const legacyBinding = lineageBinding(
    [upstreamRevision, targetRevision],
    undefined,
    [],
    [],
    { outputRevisionId: targetRevision.revisionId },
  )
  const run = workflowRun(node, [legacyBinding])
  const approved = reviewGateApprovalReadiness(
    reviewGateDefinition(true) as never,
    node as never,
    run as never,
    artifactSnapshot({ documents: [target] }) as never,
  )
  assert.equal(approved.ready, true)
  assert.deepEqual(approved.revisions, [targetRevision])

  const missingOutput = workflowRun(node, [lineageBinding(
    [upstreamRevision, targetRevision],
    undefined,
    [],
    [],
    { outputRevisionId: 'missing-revision' },
  )])
  const unavailable = reviewGateApprovalReadiness(
    reviewGateDefinition(true) as never,
    node as never,
    missingOutput as never,
    artifactSnapshot({ documents: [target] }) as never,
  )
  assert.equal(unavailable.ready, false)
  assert.deepEqual(unavailable.revisions, [])
  assert.match(unavailable.error ?? '', /exactly one current Human Edit materialization/i)
})

test('non-governed Review Gate readiness requires every exact upstream revision', () => {
  const first = versionedResource('document-a', 'document-a-r1', 'Document A', { kind: 'requirement' })
  const second = versionedResource('document-b', 'document-b-r1', 'Document B', { kind: 'requirement' })
  const firstRevision = revisionRefFixture(first.latestRevision)
  const secondRevision = revisionRefFixture(second.latestRevision)
  Object.assign(first, { approvedRevision: { ...first.latestRevision } })
  const node = reviewGateNode()
  const run = workflowRun(node, [lineageBinding(
    [firstRevision, secondRevision, firstRevision],
    undefined,
    [],
    [],
    { outputRevisionId: firstRevision.revisionId },
  )])
  const artifacts = artifactSnapshot({ documents: [first, second] })

  const pending = reviewGateApprovalReadiness(
    reviewGateDefinition(false) as never,
    node as never,
    run as never,
    artifacts as never,
  )
  assert.equal(pending.ready, false)
  assert.deepEqual(pending.revisions, [firstRevision, secondRevision])
  assert.deepEqual(pending.pendingRevisions, [secondRevision])

  Object.assign(second, { approvedRevision: { ...second.latestRevision } })
  const approved = reviewGateApprovalReadiness(
    reviewGateDefinition(false) as never,
    node as never,
    run as never,
    artifacts as never,
  )
  assert.equal(approved.ready, true)
})

test('Review Gate readiness fails closed for unavailable definitions and malformed lineage', () => {
  const target = versionedResource('brief-malformed', 'brief-malformed-r1', 'Project brief', { kind: 'projectBrief' })
  const revision = revisionRefFixture(target.latestRevision)
  Object.assign(target, { approvedRevision: { ...target.latestRevision } })
  const node = reviewGateNode()
  const artifacts = artifactSnapshot({ documents: [target] })
  const validRun = workflowRun(node, [lineageBinding(
    [revision],
    undefined,
    [],
    [],
    { materializedArtifactRevisions: [revision] },
  )])

  assert.equal(reviewGateApprovalReadiness(
    undefined,
    node as never,
    validRun as never,
    artifacts as never,
  ).ready, false)
  assert.equal(reviewGateApprovalReadiness(
    reviewGateDefinition(true) as never,
    node as never,
    workflowRun(node, []) as never,
    artifacts as never,
  ).ready, false)

  const malformed = workflowRun(node, [lineageBinding(
    [revision],
    undefined,
    [],
    [],
    { materializedArtifactRevisions: {} },
  )])
  const malformedResult = reviewGateApprovalReadiness(
    reviewGateDefinition(true) as never,
    node as never,
    malformed as never,
    artifacts as never,
  )
  assert.equal(malformedResult.ready, false)
  assert.deepEqual(malformedResult.revisions, [])
  assert.match(malformedResult.error ?? '', /lineage is malformed/i)
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
  Object.assign(revision, { proposalId: id })
  const baseRevision = {
    ...revisionRefFixture(revision),
    revisionId: `${revision.id}-base`,
    contentHash: `sha256:${revision.id}-base`,
  }
  return {
    id,
    projectId: 'project-1',
    artifactId,
    manifest: { id: `${id}-manifest`, hash: `${id}-manifest-hash` },
    baseRevision,
    payloadHash,
    status: 'applied',
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
  proposalPins: readonly unknown[] = [],
  sourceOverrides: Readonly<Record<string, unknown>> = {},
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
      proposalPins,
      ...(outputProposal ? { outputProposal } : {}),
      ...sourceOverrides,
    },
    output: {},
    outputHash: 'output-hash',
    value: {},
    valueHash: 'value-hash',
  }
}

function reviewGateNode() {
  return {
    id: 'review-node-run',
    runId: 'run-1',
    key: 'project-brief-review',
    definitionNodeId: 'project-brief-review',
    type: 'review_gate',
    status: 'waiting_review',
    attempt: 1,
  }
}

function reviewGateDefinition(governed: boolean) {
  return {
    id: 'definition-1',
    version: 1,
    name: 'Review test',
    schemaVersion: 'workflow/v2',
    nodes: [{
      id: 'project-brief-review',
      name: 'Review Project Brief',
      type: 'review_gate',
      reviewGate: {
        requiredRole: 'owner',
        minimumApprovals: 1,
        prohibitSelfReview: true,
        allowWaiver: false,
      },
    }],
    edges: [],
    ...(governed ? {
      inputContract: { capability: 'project_brief' },
      outputContract: { capability: 'application' },
    } : {}),
    hash: 'sha256:definition-1',
    createdBy: 'user-1',
    createdAt: '2026-07-10T00:00:00Z',
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
