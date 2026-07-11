import type {
  BlueprintContentDto,
  BlueprintEdgeDto,
  BlueprintLayoutDto,
  BlueprintNodeDto,
} from './dto'

export type SemanticBlueprintNode = Omit<BlueprintNodeDto, 'position'>

export function emptyBlueprintLayout(): BlueprintLayoutDto {
  return { nodePositions: {}, groups: [], viewport: { x: 0, y: 0, zoom: 1 } }
}

const SUPPORTED_HTTP_METHODS = new Set([
  'GET',
  'POST',
  'PUT',
  'PATCH',
  'DELETE',
  'HEAD',
  'OPTIONS',
])

export function blueprintGate(content: BlueprintContentDto) {
  const nodes = content.semantic?.nodes ?? content.nodes
  const edges = content.semantic?.edges ?? content.edges
  const issues: string[] = []
  if (nodes.length === 0) issues.push('At least one semantic node is required.')
  if (nodes.some((node) => !node.id || !node.key.trim() || !node.title.trim())) issues.push('Every node needs a stable ID, business key, and title.')
  if (new Set(nodes.map((node) => node.key)).size !== nodes.length) issues.push('Every node business key must be unique.')
  if (nodes.some((node) => !content.layout?.nodePositions[node.id])) issues.push('Every semantic node needs a separate layout position.')
  const ids = new Set(nodes.map((node) => node.id))
  if (edges.some((edge) => !ids.has(edge.sourceNodeId) || !ids.has(edge.targetNodeId))) issues.push('Every edge must reference existing semantic nodes.')
  const pages = nodes.filter((node) => node.kind === 'page')
  if (pages.some((node) => node.requirementIds.length === 0)) issues.push('Every page node must trace to at least one stable requirement ID.')
  if (pages.some((node) => !node.route?.trim() || !node.userGoal?.trim())) issues.push('Every page node needs a route and user goal before review.')
  if (pages.some((page) => !edges.some((edge) => edge.kind === 'contains' && edge.targetNodeId === page.id && nodes.find((node) => node.id === edge.sourceNodeId)?.kind === 'feature'))) issues.push('Every page node must belong to a feature through a contains edge.')

  const apiOperations = nodes.filter((node) => node.kind === 'apiOperation' || node.kind === 'api')
  const operationKeys = new Set<string>()
  let hasInvalidOperation = false
  let hasDuplicateOperation = false
  for (const operation of apiOperations) {
    const method = operation.method?.trim().toUpperCase() ?? ''
    const path = operation.path?.trim() ?? ''
    if (!SUPPORTED_HTTP_METHODS.has(method) || !path.startsWith('/')) {
      hasInvalidOperation = true
      continue
    }
    const key = `${method} ${path}`
    if (operationKeys.has(key)) hasDuplicateOperation = true
    operationKeys.add(key)
  }
  if (hasInvalidOperation) issues.push('Every API operation needs a supported HTTP method and absolute path.')
  if (hasDuplicateOperation) issues.push('API method/path pairs must be unique.')
  if (apiOperations.some((operation) => !edges.some((edge) =>
    edge.kind === 'requires'
    && edge.sourceNodeId === operation.id
    && nodes.find((node) => node.id === edge.targetNodeId)?.kind === 'permission',
  ))) issues.push('Every API operation must require a Permission node.')
  return issues
}

export function normalizeBlueprintContent(content: BlueprintContentDto): BlueprintContentDto {
  const semanticNodes = (content.semantic?.nodes ?? content.nodes).map(semanticNode)
  const semanticEdges = (content.semantic?.edges ?? content.edges).map(semanticEdge)
  const positions = {
    ...Object.fromEntries(content.nodes.map((node) => [node.id, node.position])),
    ...(content.layout?.nodePositions ?? {}),
  }
  const layout: BlueprintLayoutDto = {
    nodePositions: positions,
    groups: content.layout?.groups ?? [],
    viewport: content.layout?.viewport ?? { x: 0, y: 0, zoom: 1 },
  }
  return materializeBlueprintContent(content, semanticNodes, semanticEdges, layout)
}

export function materializeBlueprintContent(
  content: BlueprintContentDto,
  nodes: readonly SemanticBlueprintNode[],
  edges: readonly BlueprintEdgeDto[],
  layout: BlueprintLayoutDto,
): BlueprintContentDto {
  return {
    ...content,
    nodes: nodes.map((node) => ({
      ...node,
      position: layout.nodePositions[node.id] ?? { x: 0, y: 0 },
    })),
    edges: [...edges],
    semantic: { nodes: [...nodes], edges: [...edges] },
    layout,
    pageSpecRefs: content.pageSpecRefs ?? [],
  }
}

function semanticNode(node: Omit<BlueprintNodeDto, 'position'> | BlueprintNodeDto): SemanticBlueprintNode {
  const kind = canonicalNodeKind(node.kind)
  const roles = uniqueStrings([
    ...(node.roles ?? []),
    ...(node.requiredRoles ?? []),
    ...(node.role ? [node.role] : []),
  ])
  return {
    id: node.id,
    key: node.key?.trim() || node.id,
    kind,
    title: node.title,
    description: node.description,
    route: node.route,
    userGoal: node.userGoal,
    method: node.method,
    path: node.path,
    ...(kind === 'permission' || roles.length > 0 ? { roles } : {}),
    requirementIds: node.requirementIds,
    pageSpecArtifactId: node.pageSpecArtifactId,
    assignedMemberIds: node.assignedMemberIds,
    metadata: node.kind === 'workbenchTarget'
      ? { ...(node.metadata ?? {}), legacyKind: 'workbenchTarget' }
      : node.metadata,
  }
}

function uniqueStrings(values: readonly string[]) {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

function semanticEdge(edge: BlueprintEdgeDto): BlueprintEdgeDto {
  return {
    ...edge,
    kind: edge.kind === 'renders'
      ? 'realized_by'
      : edge.kind === 'implements'
        ? 'implemented_by'
        : edge.kind,
  }
}

function canonicalNodeKind(kind: BlueprintNodeDto['kind']): BlueprintNodeDto['kind'] {
  if (kind === 'api') return 'apiOperation'
  if (kind === 'dataModel') return 'dataEntity'
  return kind
}
