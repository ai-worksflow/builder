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
  upsertWorkbenchBundle,
  workbenchBundleNeedsRebase,
  workbenchQueueItemHasAppliedPredecessors,
  workbenchRootBundleId,
  workflowWorkbenchQueueGroups,
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

test('multi-group Workbench queues read only each node frozen input and output', () => {
  const groupA = {
    bundleIds: ['bundle-a1', 'bundle-a2'],
    sliceIds: ['slice-a1', 'slice-a2'],
    manifestGroupKey: 'group-a',
    hash: 'manifest-a',
  }
  const groupB = {
    bundleIds: ['bundle-b1'],
    sliceIds: ['slice-b1'],
    manifestGroupKey: 'group-b',
    hash: 'manifest-b',
  }
  const run = {
    context: {
      values: { buildManifest: { bundleIds: ['global-wrong'] } },
      nodes: {
        'workbench-a': {
          definitionNodeId: 'workbench-a',
          maxAttempts: 3,
          timeoutNanos: 1,
          input: { bindings: [{ value: groupA, output: groupA }] },
          output: {
            implementationProposals: [{ bundleId: 'bundle-a1', proposalId: 'proposal-a1' }],
          },
        },
        'workbench-b': {
          definitionNodeId: 'workbench-b',
          maxAttempts: 3,
          timeoutNanos: 1,
          input: { bindings: [{ value: groupB, output: groupB }] },
          output: { implementationProposalIds: ['proposal-b1'] },
        },
        unrelated: {
          definitionNodeId: 'unrelated',
          maxAttempts: 1,
          timeoutNanos: 1,
          output: {
            implementationProposals: [{ bundleId: 'bundle-a2', proposalId: 'leaked' }],
          },
        },
      },
    },
    nodes: [
      { key: 'workbench-a', definitionNodeId: 'workbench-a', type: 'workbench_build', status: 'waiting_input' },
      { key: 'workbench-b', definitionNodeId: 'workbench-b', type: 'workbench_build', status: 'completed' },
      { key: 'unrelated', definitionNodeId: 'unrelated', type: 'ai_transform', status: 'completed' },
    ],
  } as unknown as WorkflowRunDto

  const groups = workflowWorkbenchQueueGroups(run)
  assert.deepEqual(groups.map((group) => ({
    nodeKey: group.nodeKey,
    manifestGroupKey: group.manifestGroupKey,
    references: group.references,
  })), [
    {
      nodeKey: 'workbench-a',
      manifestGroupKey: 'group-a',
      references: [
        { bundleId: 'bundle-a1', sliceId: 'slice-a1', proposalId: 'proposal-a1' },
        { bundleId: 'bundle-a2', sliceId: 'slice-a2' },
      ],
    },
    {
      nodeKey: 'workbench-b',
      manifestGroupKey: 'group-b',
      references: [{ bundleId: 'bundle-b1', sliceId: 'slice-b1', proposalId: 'proposal-b1' }],
    },
  ])
  assert.deepEqual(workflowWorkbenchQueueReferences(run), [])
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
  assert.equal(workbenchQueueItemHasAppliedPredecessors(queue, 0), true)
  assert.equal(workbenchQueueItemHasAppliedPredecessors(queue, 1), false)
  assert.equal(appliedWorkbenchProposalIds(queue), null)

  queue = replaceWorkbenchQueueProposal(
    queue,
    proposal('proposal-home', 'bundle-home', 'applied'),
  )
  assert.equal(canApplyWorkbenchQueueItem(queue, 1), true)
  assert.equal(workbenchQueueItemHasAppliedPredecessors(queue, 1), true)
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

test('derived bundle stays under its root order identity and owns its exact proposal', () => {
  const root = bundle('bundle-checkout', 'page-checkout')
  const derived = {
    ...root,
    id: 'bundle-checkout-w1',
    rootBuildManifestId: root.id,
    derivedFromBuildManifestId: root.id,
    currentWorkspaceRevision: workspaceRef('workspace-r1', '1'),
  }
  const current = proposal('proposal-checkout-w1', derived.id, 'reviewing')
  const queue = hydrateWorkbenchQueue(
    [{ bundleId: root.id, sliceId: 'page-checkout', proposalId: current.id }],
    [root, derived],
    [current],
  )

  assert.equal(workbenchRootBundleId(derived), root.id)
  assert.equal(queue[0].bundleId, root.id)
  assert.equal(queue[0].bundle?.id, derived.id)
  assert.equal(queue[0].proposal?.buildManifestId, derived.id)
})

test('lineage hydration keeps an active derived leaf even before it has a proposal', () => {
  const root = bundle('bundle-checkout', 'page-checkout')
  const derived = {
    ...root,
    id: 'bundle-checkout-w1',
    rootBuildManifestId: root.id,
    derivedFromBuildManifestId: root.id,
    currentWorkspaceRevision: workspaceRef('workspace-r1', '1'),
  }
  const queue = hydrateWorkbenchQueue(
    [{ bundleId: root.id, sliceId: 'page-checkout' }],
    [derived],
    [],
  )

  assert.equal(queue[0].bundleId, root.id)
  assert.equal(queue[0].bundle?.id, derived.id)
  assert.equal(queue[0].proposal, null)
})

test('rebasing replaces stale active proposal without migrating its decisions', () => {
  const root = bundle('bundle-checkout', 'page-checkout')
  const staleProposal = proposal('proposal-before-rebase', root.id, 'ready')
  const originalQueue = hydrateWorkbenchQueue(
    [{ bundleId: root.id, proposalId: staleProposal.id }],
    [root],
    [staleProposal],
  )
  const derived = {
    ...root,
    id: 'bundle-checkout-w1',
    rootBuildManifestId: root.id,
    derivedFromBuildManifestId: root.id,
    currentWorkspaceRevision: workspaceRef('workspace-r1', '1'),
  }
  const rebased = upsertWorkbenchBundle(originalQueue, derived)

  assert.equal(rebased[0].bundleId, root.id)
  assert.equal(rebased[0].bundle?.id, derived.id)
  assert.equal(rebased[0].proposal, null)
  assert.equal(rebased[0].proposalId, undefined)
  assert.equal(workbenchBundleNeedsRebase(derived, workspace('workspace-r1', '1')), false)
  assert.equal(workbenchBundleNeedsRebase(derived, workspace('workspace-r2', '2')), true)
})

test('a second rebase advances the active leaf without forking the root queue identity', () => {
  const root = bundle('bundle-checkout', 'page-checkout')
  const firstDerived = {
    ...root,
    id: 'bundle-checkout-w1',
    rootBuildManifestId: root.id,
    derivedFromBuildManifestId: root.id,
    currentWorkspaceRevision: workspaceRef('workspace-r1', '1'),
  }
  const firstProposal = proposal('proposal-checkout-w1', firstDerived.id, 'ready')
  let queue = hydrateWorkbenchQueue(
    [{ bundleId: root.id, proposalId: firstProposal.id }],
    [firstDerived],
    [firstProposal],
  )
  const secondDerived = {
    ...root,
    id: 'bundle-checkout-w2',
    rootBuildManifestId: root.id,
    derivedFromBuildManifestId: firstDerived.id,
    currentWorkspaceRevision: workspaceRef('workspace-r2', '2'),
  }
  queue = upsertWorkbenchBundle(queue, secondDerived)

  assert.equal(queue.length, 1)
  assert.equal(queue[0].bundleId, root.id)
  assert.equal(queue[0].bundle?.id, secondDerived.id)
  assert.equal(queue[0].bundle?.derivedFromBuildManifestId, firstDerived.id)
  assert.equal(queue[0].proposal, null)
  assert.equal(queue[0].proposalId, undefined)
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
    nodes: [{
      key: 'workbench',
      definitionNodeId: 'workbench',
      type: 'workbench_build',
      status: 'waiting_input',
    }],
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

function workspaceRef(revisionId: string, hashCharacter: string) {
  return {
    artifactId: 'workspace-1',
    revisionId,
    revisionNumber: Number(hashCharacter),
    contentHash: hashCharacter.repeat(64),
  }
}

function workspace(revisionId: string, hashCharacter: string) {
  return {
    id: revisionId,
    artifactId: 'workspace-1',
    revisionNumber: Number(hashCharacter),
    contentHash: hashCharacter.repeat(64),
  } as import('../lib/platform/flow-contract').WorkspaceRevisionDto
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
