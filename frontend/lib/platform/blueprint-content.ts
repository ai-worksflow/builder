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

export function normalizeBlueprintContent(content: BlueprintContentDto): BlueprintContentDto {
  const semanticNodes = content.semantic?.nodes ?? content.nodes.map(semanticNode)
  const semanticEdges = content.semantic?.edges ?? content.edges
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

function semanticNode(node: BlueprintNodeDto): SemanticBlueprintNode {
  return {
    id: node.id,
    kind: node.kind,
    title: node.title,
    description: node.description,
    requirementIds: node.requirementIds,
    pageSpecArtifactId: node.pageSpecArtifactId,
    assignedMemberIds: node.assignedMemberIds,
    metadata: node.metadata,
  }
}
