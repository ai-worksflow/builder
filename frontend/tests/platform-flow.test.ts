import assert from 'node:assert/strict'
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

test('human resume and canonical review commands target a project-scoped run', async () => {
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
