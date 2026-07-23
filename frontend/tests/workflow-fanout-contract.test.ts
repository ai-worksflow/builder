import assert from 'node:assert/strict'
import {
  appendPairedFanOutSubgraph,
  parseEditableDefinition,
  resolveWorkflowProposalReference,
  starterWorkflowDefinition,
} from '../lib/platform/workflow-ui-contract'

const starter = starterWorkflowDefinition()
assert.equal(starter.schemaVersion, '4')
const pageFanOut = starter.nodes.find((node) => node.id === 'pages')?.fanOut
assert.deepEqual(pageFanOut, {
  itemsPath: '/blueprintPages',
  sliceKeyPath: '/key',
  mergeNodeId: 'pages-merged',
  maxParallel: 4,
  maxItems: 100,
  itemKind: 'blueprint_page',
})

const generic = {
  ...starter,
  nodes: starter.nodes.map((node) => node.id === 'pages' && node.fanOut
    ? {
        ...node,
        fanOut: {
          ...node.fanOut,
          itemsPath: '/payload/jobs',
          sliceKeyPath: '/identity/key',
          itemKind: 'generic' as const,
        },
      }
    : node),
}
assert.equal(parseEditableDefinition(JSON.stringify(generic), true).error, undefined)

const invalid = {
  ...generic,
  nodes: generic.nodes.map((node) => node.id === 'pages' && node.fanOut
    ? { ...node, fanOut: { ...node.fanOut, itemKind: 'browser_fixture' } }
    : node),
}
assert.match(
  parseEditableDefinition(JSON.stringify(invalid), true).error ?? '',
  /fanOut is malformed/,
)

const firstPair = appendPairedFanOutSubgraph(starter, 'fan_out')
const secondPair = appendPairedFanOutSubgraph(firstPair.definition, 'merge')
const addedFanOuts = secondPair.definition.nodes.filter((node) =>
  node.id.startsWith('fan-out') && node.fanOut)
const addedMerges = secondPair.definition.nodes.filter((node) =>
  node.id.startsWith('merge') && node.merge)
assert.equal(addedFanOuts.length, 2)
assert.equal(addedMerges.length, 2)
for (const fanOut of addedFanOuts) {
  const merge = secondPair.definition.nodes.find((node) => node.id === fanOut.fanOut?.mergeNodeId)
  assert.equal(merge?.merge?.fanOutNodeId, fanOut.id)
  assert.equal(secondPair.definition.edges.some((edge) => edge.from === fanOut.id && edge.to === merge?.id), false)
  const branchEdge = secondPair.definition.edges.find((edge) => edge.from === fanOut.id)
  assert.ok(branchEdge)
  const branch = secondPair.definition.nodes.find((node) => node.id === branchEdge.to)
  assert.ok(branch?.aiTransform || branch?.transform)
  assert.equal(secondPair.definition.edges.filter((edge) => edge.from === branch?.id && edge.to === merge?.id).length, 1)
}
assert.notEqual(firstPair.selectedNodeId, secondPair.selectedNodeId)

assert.equal(
  resolveWorkflowProposalReference('run-1', 'proposal-1', ''),
  'proposal-1',
  'completed workflow nodes retain their exact proposal deep link for canonical review',
)
assert.equal(
  resolveWorkflowProposalReference('run-1', 'stale-proposal', 'active-proposal'),
  '',
  'an active server-derived proposal rejects a conflicting query pin',
)
assert.equal(
  resolveWorkflowProposalReference('run-1', '', 'active-proposal'),
  'active-proposal',
)

console.log('✓ workflow fan-out uses exact resolvers and atomically creates unique reciprocal merge pairs')
