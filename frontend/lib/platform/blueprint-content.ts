import type {
  BlueprintContentDto,
  BlueprintEdgeDto,
  BlueprintLayoutDto,
  BlueprintNodeDto,
} from './dto'

export type SemanticBlueprintNode = Omit<BlueprintNodeDto, 'position'>

type BlueprintNodeWire = (Omit<BlueprintNodeDto, 'position'> | BlueprintNodeDto) & {
  readonly businessKey?: string
  readonly type?: BlueprintNodeDto['kind'] | string
  readonly goal?: string
  readonly spec?: {
    readonly title?: string
    readonly description?: string
    readonly route?: string
    readonly goal?: string
    readonly userGoal?: string
    readonly roles?: readonly string[]
    readonly requiredRoles?: readonly string[]
  }
}

type BlueprintEdgeWire = BlueprintEdgeDto & {
  readonly from?: string
  readonly to?: string
  readonly source?: string
  readonly target?: string
  readonly type?: BlueprintEdgeDto['kind'] | string
  readonly relation?: string
  readonly isRequired?: boolean
}

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
  const nodes = Array.isArray(content.semantic?.nodes)
    ? content.semantic.nodes
    : Array.isArray(content.nodes) ? content.nodes : []
  const edges = Array.isArray(content.semantic?.edges)
    ? content.semantic.edges
    : Array.isArray(content.edges) ? content.edges : []
  const issues: string[] = []
  if (nodes.length === 0) issues.push('At least one semantic node is required.')
  if (nodes.some((node) => !node.id || !node.key?.trim() || !node.title?.trim())) issues.push('Every node needs a stable ID, business key, and title.')
  if (new Set(nodes.map((node) => node.key)).size !== nodes.length) issues.push('Every node business key must be unique.')
  if (nodes.some((node) => !content.layout?.nodePositions[node.id])) issues.push('Every semantic node needs a separate layout position.')
  const ids = new Set(nodes.map((node) => node.id))
  if (edges.some((edge) => !ids.has(edge.sourceNodeId) || !ids.has(edge.targetNodeId))) issues.push('Every edge must reference existing semantic nodes.')
  const pages = nodes.filter((node) => node.kind === 'page')
  if (pages.some((node) => (node.requirementIds?.length ?? 0) === 0)) issues.push('Every page node must trace to at least one stable requirement ID.')
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
  const rootNodes = Array.isArray(content.nodes) ? content.nodes : []
  const rootEdges = Array.isArray(content.edges) ? content.edges : []
  const semanticNodes = (
    Array.isArray(content.semantic?.nodes) ? content.semantic.nodes : rootNodes
  ).map(semanticNode)
  const semanticEdges = (
    Array.isArray(content.semantic?.edges) ? content.semantic.edges : rootEdges
  ).map(semanticEdge)
  const positions: Record<string, { readonly x: number; readonly y: number }> = {
    ...Object.fromEntries(rootNodes.flatMap((node) => node.position ? [[node.id, node.position]] : [])),
    ...(content.layout?.nodePositions ?? {}),
  }
  semanticNodes.forEach((node, index) => {
    if (positions[node.id]) return
    positions[node.id] = {
      x: 48 + (index % 4) * 210,
      y: 48 + Math.floor(index / 4) * 110,
    }
  })
  const layout: BlueprintLayoutDto = {
    nodePositions: positions,
    groups: (content.layout?.groups ?? []).map((group) => ({
      ...group,
      nodeIds: Array.isArray(group.nodeIds) ? group.nodeIds : [],
    })),
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
    validation: content.validation ?? [],
  }
}

function semanticNode(node: BlueprintNodeWire): SemanticBlueprintNode {
  const kind = canonicalNodeKind(firstNonEmptyString(node.kind, node.type))
  const key = firstNonEmptyString(node.key, node.businessKey) || node.id
  const roles = uniqueStrings([
    ...(Array.isArray(node.roles) ? node.roles : []),
    ...(Array.isArray(node.requiredRoles) ? node.requiredRoles : []),
    ...(node.role ? [node.role] : []),
    ...(Array.isArray(node.spec?.roles) ? node.spec.roles : []),
    ...(Array.isArray(node.spec?.requiredRoles) ? node.spec.requiredRoles : []),
  ])
  return {
    id: node.id,
    key,
    kind,
    title: firstNonEmptyString(node.title, node.spec?.title) || (kind === 'page' ? '' : key),
    description: firstNonEmptyString(node.description, node.spec?.description),
    route: firstNonEmptyString(node.route, node.spec?.route) || undefined,
    userGoal: firstNonEmptyString(
      node.userGoal,
      node.goal,
      node.spec?.userGoal,
      node.spec?.goal,
    ) || undefined,
    method: kind === 'apiOperation' ? node.method : undefined,
    path: kind === 'apiOperation'
      ? firstNonEmptyString(node.path, node.route) || undefined
      : undefined,
    ...(kind === 'permission' || roles.length > 0 ? { roles } : {}),
    requirementIds: Array.isArray(node.requirementIds) ? node.requirementIds : [],
    pageSpecArtifactId: node.pageSpecArtifactId,
    assignedMemberIds: Array.isArray(node.assignedMemberIds) ? node.assignedMemberIds : [],
    metadata: kind === 'workbenchTarget'
      ? { ...(node.metadata ?? {}), legacyKind: 'workbenchTarget' }
      : node.metadata,
  }
}

function uniqueStrings(values: readonly string[]) {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

function semanticEdge(edge: BlueprintEdgeWire): BlueprintEdgeDto {
  return {
    id: edge.id,
    sourceNodeId: firstNonEmptyString(edge.sourceNodeId, edge.from, edge.source),
    targetNodeId: firstNonEmptyString(edge.targetNodeId, edge.to, edge.target),
    kind: canonicalEdgeKind(firstNonEmptyString(edge.kind, edge.type, edge.relation)),
    required: Boolean(edge.required || edge.isRequired),
  }
}

function canonicalNodeKind(kind: BlueprintNodeDto['kind'] | string): BlueprintNodeDto['kind'] {
  switch (kind.trim().toLowerCase()) {
    case 'api':
    case 'apioperation':
      return 'apiOperation'
    case 'dataentity':
    case 'datamodel':
      return 'dataEntity'
    case 'workbenchtarget':
      return 'workbenchTarget'
    default:
      return kind.trim().toLowerCase() as BlueprintNodeDto['kind']
  }
}

function canonicalEdgeKind(kind: BlueprintEdgeDto['kind'] | string): BlueprintEdgeDto['kind'] {
  const normalized = kind.trim().toLowerCase()
  if (normalized === 'renders') return 'realized_by'
  if (normalized === 'implements') return 'implemented_by'
  return normalized as BlueprintEdgeDto['kind']
}

function firstNonEmptyString(...values: readonly unknown[]) {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return ''
}
