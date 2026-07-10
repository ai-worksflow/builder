import assert from 'node:assert/strict'
import { PlatformClient } from '../lib/platform/client'
import type {
  ImplementationProposalDto,
  WorkflowRunDto,
  WorkbenchBundleDto,
} from '../lib/platform/flow-contract'
import type { FetchLike } from '../lib/platform/http'
import {
  appliedWorkbenchProposalIds,
  canApplyWorkbenchQueueItem,
  hydrateWorkbenchQueue,
  replaceWorkbenchQueueProposal,
  workflowWorkbenchQueueReferences,
} from '../lib/platform/flow-queue'

type TestCase = {
  readonly name: string
  readonly run: () => void | Promise<void>
}

const tests: TestCase[] = []

function test(name: string, run: TestCase['run']) {
  tests.push({ name, run })
}

test('two-page workflow hydration preserves manifest order and bundle proposal mapping', () => {
  const run = workflowRun({
    implementationProposals: [
      { bundleId: 'bundle-checkout', proposalId: 'proposal-checkout', payloadHash: 'b' },
      { bundleId: 'bundle-home', proposalId: 'proposal-home', payloadHash: 'a' },
    ],
  })

  const references = workflowWorkbenchQueueReferences(run)
  assert.deepEqual(references, [
    { bundleId: 'bundle-home', sliceId: 'page-home', proposalId: 'proposal-home' },
    { bundleId: 'bundle-checkout', sliceId: 'page-checkout', proposalId: 'proposal-checkout' },
  ])

  const queue = hydrateWorkbenchQueue(
    references,
    [bundle('bundle-checkout', 'page-checkout'), bundle('bundle-home', 'page-home')],
    [
      proposal('proposal-checkout', 'bundle-checkout', 'ready'),
      proposal('proposal-home', 'bundle-home', 'reviewing'),
    ],
  )
  assert.deepEqual(queue.map((item) => [item.bundleId, item.sliceId, item.proposal?.id]), [
    ['bundle-home', 'page-home', 'proposal-home'],
    ['bundle-checkout', 'page-checkout', 'proposal-checkout'],
  ])
})

test('two-page proposals apply sequentially and complete with every ordered applied id', () => {
  let queue = hydrateWorkbenchQueue(
    [
      { bundleId: 'bundle-home', sliceId: 'page-home', proposalId: 'proposal-home' },
      { bundleId: 'bundle-checkout', sliceId: 'page-checkout', proposalId: 'proposal-checkout' },
    ],
    [bundle('bundle-home', 'page-home'), bundle('bundle-checkout', 'page-checkout')],
    [
      proposal('proposal-home', 'bundle-home', 'ready'),
      proposal('proposal-checkout', 'bundle-checkout', 'ready'),
    ],
  )

  assert.equal(canApplyWorkbenchQueueItem(queue, 0), true)
  assert.equal(canApplyWorkbenchQueueItem(queue, 1), false)
  assert.equal(appliedWorkbenchProposalIds(queue), null)

  queue = replaceWorkbenchQueueProposal(
    queue,
    proposal('proposal-home', 'bundle-home', 'applied'),
  )
  assert.equal(canApplyWorkbenchQueueItem(queue, 1), true)
  assert.equal(appliedWorkbenchProposalIds(queue), null)

  queue = replaceWorkbenchQueueProposal(
    queue,
    proposal('proposal-checkout', 'bundle-checkout', 'partially_applied'),
  )
  assert.deepEqual(appliedWorkbenchProposalIds(queue), [
    'proposal-home',
    'proposal-checkout',
  ])
})

test('completed Workbench output restores all proposal ids by frozen bundle order', () => {
  const references = workflowWorkbenchQueueReferences(workflowRun({
    implementationProposalIds: ['replacement-home', 'replacement-checkout'],
  }))
  assert.deepEqual(references, [
    { bundleId: 'bundle-home', sliceId: 'page-home', proposalId: 'replacement-home' },
    { bundleId: 'bundle-checkout', sliceId: 'page-checkout', proposalId: 'replacement-checkout' },
  ])
})

test('Workbench completion submits both applied proposal ids and the exact final workspace', async () => {
  let requestBody: unknown
  const client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async (_input, init) => {
        requestBody = typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined
        return new Response(null, { status: 204 })
      }) as FetchLike,
    },
  }).flow

  await client.completeWorkbenchNode(
    'project-1',
    'run-1',
    'workbench',
    ['proposal-home', 'proposal-checkout'],
    {
      artifactId: 'workspace-1',
      revisionId: 'workspace-r2',
      revisionNumber: 2,
      contentHash: 'a'.repeat(64),
    },
  )

  assert.deepEqual(requestBody, {
    nodeKey: 'workbench',
    output: {
      implementationProposalIds: ['proposal-home', 'proposal-checkout'],
      workspaceRevision: {
        artifactId: 'workspace-1',
        revisionId: 'workspace-r2',
        contentHash: 'a'.repeat(64),
      },
    },
  })
})

function workflowRun(workbenchOutput: Record<string, unknown>) {
  return {
    context: {
      values: {
        buildManifest: {
          bundleIds: ['bundle-home', 'bundle-checkout'],
          sliceIds: ['page-home', 'page-checkout'],
        },
      },
      nodes: {
        workbench: {
          definitionNodeId: 'workbench',
          maxAttempts: 3,
          timeoutNanos: 1,
          output: workbenchOutput,
        },
      },
    },
  } as unknown as WorkflowRunDto
}

function bundle(id: string, deliverySliceId: string) {
  return { id, deliverySliceId } as WorkbenchBundleDto
}

function proposal(
  id: string,
  buildManifestId: string,
  status: ImplementationProposalDto['status'],
) {
  return { id, buildManifestId, status } as ImplementationProposalDto
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
}

void main()
