import assert from 'node:assert/strict'
import {
  BLUEPRINT_MODULE_TEMPLATES,
  groupBlueprintNodes,
  insertBlueprintModule,
  readBlueprintSelectionScope,
} from '../lib/platform/blueprint-selection'
import type { BlueprintContentDto } from '../lib/platform/dto'
import type { InputManifestDto } from '../lib/platform/flow-contract'

let sequence = 0
const page = BLUEPRINT_MODULE_TEMPLATES.find((template) => template.id === 'page')!
const emptyBlueprint: BlueprintContentDto = {
  nodes: [], edges: [], semantic: { nodes: [], edges: [] },
  layout: { nodePositions: {}, groups: [], viewport: { x: 0, y: 0, zoom: 1 } },
  pageSpecRefs: [], validation: [],
}
const inserted = insertBlueprintModule(
  emptyBlueprint,
  page,
  { x: 80, y: 120 },
  (prefix) => `${prefix}-${++sequence}`,
)
assert.equal(inserted.content.semantic?.nodes.length, 2)
assert.equal(inserted.content.semantic?.edges[0]?.kind, 'contains')
assert.deepEqual(inserted.nodeIds, ['node-page-1', 'node-page-2'])

const grouped = groupBlueprintNodes(inserted.content, [...inserted.nodeIds].reverse(), 'Checkout', 'group-1')
assert.deepEqual(grouped.layout?.groups, [{
  id: 'group-1',
  title: 'Checkout',
  nodeIds: ['node-page-1', 'node-page-2'],
}])

const manifest = {
  id: 'selection-manifest', projectId: 'project', jobType: 'blueprint.selection',
  deliverySliceId: 'sha256:selection', sources: [], outputSchemaVersion: 'blueprint-selection/v1',
  createdBy: 'user', createdAt: new Date(0).toISOString(), hash: `sha256:${'a'.repeat(64)}`,
  constraints: { blueprintSelection: { schemaVersion: 1, selectionId: 'sha256:selection', blueprint: {}, nodeIds: ['page-a'], nodes: [], edges: [], pageBindings: [] } },
} as unknown as InputManifestDto
assert.equal(readBlueprintSelectionScope(manifest).nodeIds[0], 'page-a')

console.log('✓ Blueprint modules, capability groups, and frozen selection manifests stay deterministic')
