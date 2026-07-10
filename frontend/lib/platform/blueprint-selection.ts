import {
  emptyBlueprintLayout,
  materializeBlueprintContent,
  normalizeBlueprintContent,
  type SemanticBlueprintNode,
} from './blueprint-content'
import type {
  BlueprintContentDto,
  BlueprintEdgeDto,
  BlueprintLayoutDto,
  BlueprintNodeKind,
} from './dto'
import type { BlueprintSelectionScopeDto, InputManifestDto } from './flow-contract'

interface ModuleNodeSeed {
  readonly localId: string
  readonly kind: BlueprintNodeKind
  readonly title: string
}

interface ModuleEdgeSeed {
  readonly source: string
  readonly target: string
  readonly kind: BlueprintEdgeDto['kind']
}

export interface BlueprintModuleTemplate {
  readonly id: 'feature' | 'page' | 'api' | 'data' | 'permission' | 'ui'
  readonly label: string
  readonly description: string
  readonly nodes: readonly ModuleNodeSeed[]
  readonly edges: readonly ModuleEdgeSeed[]
}

export const BLUEPRINT_MODULE_TEMPLATES: readonly BlueprintModuleTemplate[] = [
  { id: 'feature', label: 'Feature', description: 'Business capability boundary', nodes: [{ localId: 'feature', kind: 'feature', title: 'New feature' }], edges: [] },
  { id: 'page', label: 'Page', description: 'Feature with a contained route', nodes: [{ localId: 'feature', kind: 'feature', title: 'Page capability' }, { localId: 'page', kind: 'page', title: 'New page' }], edges: [{ source: 'feature', target: 'page', kind: 'contains' }] },
  { id: 'api', label: 'API', description: 'Operation with required permission', nodes: [{ localId: 'api', kind: 'apiOperation', title: 'New API operation' }, { localId: 'permission', kind: 'permission', title: 'API permission' }], edges: [{ source: 'api', target: 'permission', kind: 'requires' }] },
  { id: 'data', label: 'Data', description: 'Persistent domain entity', nodes: [{ localId: 'data', kind: 'dataEntity', title: 'New data entity' }], edges: [] },
  { id: 'permission', label: 'Permission', description: 'Access-control contract', nodes: [{ localId: 'permission', kind: 'permission', title: 'New permission' }], edges: [] },
  { id: 'ui', label: 'UI', description: 'Reusable interface component', nodes: [{ localId: 'component', kind: 'component', title: 'New UI component' }], edges: [] },
]

export function insertBlueprintModule(
  content: BlueprintContentDto,
  template: BlueprintModuleTemplate,
  origin: { readonly x: number; readonly y: number },
  createId: (prefix: string) => string,
) {
  const normalized = normalizeBlueprintContent(content)
  const nodes = normalized.semantic?.nodes ?? []
  const edges = normalized.semantic?.edges ?? []
  const layout = normalized.layout ?? emptyBlueprintLayout()
  const ids = new Map<string, string>()
	const insertedNodes: SemanticBlueprintNode[] = template.nodes.map((seed) => {
    const id = createId(`node-${template.id}`)
    ids.set(seed.localId, id)
    const key = `${seed.kind.replace(/([a-z])([A-Z])/g, '$1_$2').toUpperCase()}-${id.slice(-8).toUpperCase()}`
    return {
      id,
      key,
      kind: seed.kind,
      title: seed.title,
      description: '',
      ...(seed.kind === 'page' ? { route: `/${template.id}-${id.slice(-6)}`, userGoal: '' } : {}),
      ...(seed.kind === 'apiOperation' ? { method: 'GET', path: '/resource' } : {}),
      requirementIds: [],
      assignedMemberIds: [],
    }
  })
  const insertedEdges: BlueprintEdgeDto[] = template.edges.map((seed) => ({
    id: createId(`edge-${template.id}`),
    sourceNodeId: ids.get(seed.source)!,
    targetNodeId: ids.get(seed.target)!,
    kind: seed.kind,
    required: true,
  }))
  const nodePositions = { ...layout.nodePositions }
  insertedNodes.forEach((node, index) => {
    nodePositions[node.id] = {
      x: Math.max(0, Math.round(origin.x + (index % 2) * 190)),
      y: Math.max(0, Math.round(origin.y + Math.floor(index / 2) * 90)),
    }
  })
  return {
    content: materializeBlueprintContent(
      normalized,
      [...nodes, ...insertedNodes],
      [...edges, ...insertedEdges],
      { ...layout, nodePositions },
    ),
    nodeIds: insertedNodes.map((node) => node.id),
  }
}

export function groupBlueprintNodes(
  content: BlueprintContentDto,
  nodeIds: readonly string[],
  title: string,
  groupId: string,
) {
  const normalized = normalizeBlueprintContent(content)
  const nodes = normalized.semantic?.nodes ?? []
  const layout: BlueprintLayoutDto = normalized.layout ?? emptyBlueprintLayout()
  const existing = new Set(nodes.map((node) => node.id))
  const selected = Array.from(new Set(nodeIds.filter((nodeId) => existing.has(nodeId)))).sort()
  if (selected.length === 0) return normalized
  const groups = [
    ...layout.groups.filter((group) => group.id !== groupId),
    { id: groupId, title: title.trim() || 'Capability', nodeIds: selected },
  ]
  return materializeBlueprintContent(
    normalized,
    nodes,
    normalized.semantic?.edges ?? [],
    { ...layout, groups },
  )
}

export function readBlueprintSelectionScope(manifest: InputManifestDto): BlueprintSelectionScopeDto {
  const scope = (manifest.constraints as Record<string, unknown>).blueprintSelection as BlueprintSelectionScopeDto | undefined
  if (
    manifest.jobType !== 'blueprint.selection'
    || manifest.outputSchemaVersion !== 'blueprint-selection/v1'
    || !scope
    || scope.schemaVersion !== 1
    || scope.selectionId !== manifest.deliverySliceId
    || !Array.isArray(scope.nodeIds)
    || !Array.isArray(scope.pageBindings)
  ) {
    throw new Error('The server response is not a valid frozen Blueprint selection manifest.')
  }
  return scope
}
