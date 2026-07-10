import assert from 'node:assert/strict'
import {
  parseEditableDefinition,
  starterWorkflowDefinition,
} from '../lib/platform/workflow-ui-contract'

const starter = starterWorkflowDefinition()
const pageFanOut = starter.nodes.find((node) => node.id === 'pages')?.fanOut
assert.deepEqual(pageFanOut, {
  itemsPath: '/workflowContext/deliverySlices',
  sliceKeyPath: '/key',
  mergeNodeId: 'pages-merged',
  maxParallel: 4,
  itemKind: 'delivery_slice',
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

console.log('✓ workflow fan-out distinguishes generic input items from exact delivery slices')
