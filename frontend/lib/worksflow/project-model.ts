import {
  DEFAULT_BLUEPRINT,
  DOC_DEPENDENCIES,
  IMPORT_ASSETS,
  NODE_BINDINGS,
  TEAM_DOCUMENTS,
} from './mock-data'
import type {
  BindingTargetKind,
  Blueprint,
  BlueprintEdge,
  BlueprintNode,
  BlueprintNodeType,
  BlueprintWorkbenchContext,
  DocType,
  DocumentDependency,
  ImportAsset,
  NodeBinding,
  ProjectVersion,
  TeamDocument,
  TeamProject,
  TeamProjectSource,
} from './types'

export const BLUEPRINT_DOC_OUTPUTS: Record<
  BlueprintNodeType,
  Array<{ type: DocType; suffix: string; sectionTitle: string }>
> = {
  feature: [{ type: 'featureList', suffix: 'feature list', sectionTitle: '功能范围' }],
  page: [
    { type: 'pageSplit', suffix: 'page split', sectionTitle: '页面职责' },
    { type: 'uiPrototype', suffix: 'UI prototype', sectionTitle: '页面状态' },
    { type: 'frontendDev', suffix: 'frontend implementation', sectionTitle: '前端实现' },
  ],
  component: [{ type: 'frontendDev', suffix: 'component implementation', sectionTitle: '组件拆分' }],
  api: [
    { type: 'apiContract', suffix: 'API contract', sectionTitle: '接口契约' },
    { type: 'backendDev', suffix: 'backend implementation', sectionTitle: '后端实现' },
  ],
  dataModel: [{ type: 'backendDev', suffix: 'data model', sectionTitle: '数据模型' }],
  permission: [{ type: 'apiContract', suffix: 'permission matrix', sectionTitle: '权限矩阵' }],
  prototype: [{ type: 'uiPrototype', suffix: 'prototype brief', sectionTitle: '原型来源' }],
  workbenchTarget: [{ type: 'frontendDev', suffix: 'workbench target', sectionTitle: '实现目标' }],
}

export function unique<T>(items: T[]) {
  return Array.from(new Set(items))
}

export function bindingKindForBlueprintNode(type: BlueprintNodeType): BindingTargetKind {
  return type === 'workbenchTarget' ? 'workbenchVersion' : type
}

export function collectBlueprintSelection(blueprint: Blueprint, selectedNodeId: string) {
  const connectedEdges = blueprint.edges.filter(
    (edge) => edge.sourceNodeId === selectedNodeId || edge.targetNodeId === selectedNodeId,
  )
  const nodeIds = unique([
    selectedNodeId,
    ...connectedEdges.flatMap((edge) => [edge.sourceNodeId, edge.targetNodeId]),
  ])
  const nodes = blueprint.nodes.filter((node) => nodeIds.includes(node.id))
  const edgeIds = connectedEdges.map((edge) => edge.id)
  return { nodes, edges: connectedEdges, nodeIds, edgeIds }
}

export function computeBlueprintNodeMissing(node: BlueprintNode, blueprint: Blueprint) {
  const outgoing = blueprint.edges.filter((edge) => edge.sourceNodeId === node.id)
  const incoming = blueprint.edges.filter((edge) => edge.targetNodeId === node.id)
  const missing: string[] = []

  if (
    ['feature', 'page', 'api', 'permission', 'prototype'].includes(node.type) &&
    node.boundMemberIds.length === 0
  ) {
    missing.push('No owner assigned')
  }
  if (node.type === 'feature') {
    if (!outgoing.some((edge) => edge.type === 'contains')) missing.push('No page or child feature')
    if (!outgoing.some((edge) => edge.type === 'requires')) missing.push('No permission rule')
    if (!outgoing.some((edge) => edge.type === 'syncs_with')) missing.push('No prototype asset')
  }
  if (node.type === 'page') {
    if (!outgoing.some((edge) => edge.type === 'calls')) missing.push('No API contract')
    if (
      !outgoing.some((edge) => edge.type === 'renders' || edge.type === 'uses') &&
      !incoming.some((edge) => edge.type === 'renders' || edge.type === 'uses')
    ) {
      missing.push('No component state')
    }
  }
  if (node.type === 'api') {
    if (!outgoing.some((edge) => edge.type === 'reads' || edge.type === 'writes')) {
      missing.push('No data model')
    }
  }
  if (node.type === 'permission' && !node.description) {
    missing.push('Auth rules undefined')
  }
  if (
    node.type === 'prototype' &&
    node.boundPrototypeArtifactIds.length === 0 &&
    node.boundDocumentIds.length === 0
  ) {
    missing.push('No imported prototype source')
  }

  return unique(missing)
}

export function blueprintPrompt(
  blueprint: Blueprint,
  selected: BlueprintNode,
  nodes: BlueprintNode[],
  edges: BlueprintEdge[],
  linkedDocs: TeamDocument[],
) {
  const nodesLine = nodes.map((node) => `${node.type}: ${node.title}`).join(', ')
  const edgesLine = edges
    .map((edge) => {
      const source = blueprint.nodes.find((node) => node.id === edge.sourceNodeId)
      const target = blueprint.nodes.find((node) => node.id === edge.targetNodeId)
      return `${source?.title ?? edge.sourceNodeId} ${edge.type} ${target?.title ?? edge.targetNodeId}`
    })
    .join('; ')
  const docsLine = linkedDocs.map((doc) => `${doc.title} (${doc.status})`).join(', ')

  return [
    `Use Blueprint selection "${selected.title}" as the implementation scope.`,
    `Capability graph nodes: ${nodesLine || 'none'}.`,
    `Relationships: ${edgesLine || 'none'}.`,
    `Team documents to read: ${docsLine || 'none'}.`,
    'Generate the implementation plan from this selected capability graph only, then preserve a sync-back summary for the Blueprint Workbench Target.',
  ].join('\n')
}

export function cloneDocuments(docs: TeamDocument[]) {
  return docs.map((doc) => ({
    ...doc,
    position: { ...doc.position },
    members: doc.members.map((member) => ({ ...member })),
    sections: doc.sections.map((section) => ({ ...section })),
  }))
}

export function cloneDependencies(dependencies: DocumentDependency[]) {
  return dependencies.map((dependency) => ({ ...dependency }))
}

export function cloneNodeBindings(bindings: NodeBinding[]) {
  return bindings.map((binding) => ({ ...binding }))
}

export function cloneImportAssets(assets: ImportAsset[]) {
  return assets.map((asset) => ({ ...asset }))
}

export function cloneBlueprint(source: Blueprint, overrides: Partial<Pick<Blueprint, 'id' | 'title'>> = {}) {
  return {
    ...source,
    ...overrides,
    nodes: source.nodes.map((node) => ({
      ...node,
      position: { ...node.position },
      boundDocumentIds: [...node.boundDocumentIds],
      boundMemberIds: [...node.boundMemberIds],
      boundPrototypeArtifactIds: [...node.boundPrototypeArtifactIds],
      generatedDocIds: [...node.generatedDocIds],
      missing: node.missing ? [...node.missing] : undefined,
    })),
    edges: source.edges.map((edge) => ({ ...edge })),
    generatedDocIds: [...source.generatedDocIds],
  }
}

export function blankBlueprint(id: string, projectName: string): Blueprint {
  return {
    id,
    title: `${projectName} Blueprint`,
    status: 'draft',
    ownerId: 'm1',
    nodes: [],
    edges: [],
    generatedDocIds: [],
    version: 1,
    updatedAt: 'Just now',
  }
}

export function generatedBlueprint(id: string, projectName: string, brief = ''): Blueprint {
  const baseTitle = projectName.trim() || 'New capability'
  const summary = brief.trim() || `Initial capability map for ${baseTitle}.`
  const slug = baseTitle.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
  const nodes: BlueprintNode[] = [
    {
      id: `${id}-feature`,
      type: 'feature',
      title: `${baseTitle} Core Flow`,
      description: summary,
      position: { x: 60, y: 140 },
      boundDocumentIds: [],
      boundMemberIds: ['m1'],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
    },
    {
      id: `${id}-page`,
      type: 'page',
      title: `/${slug || 'app'}`,
      description: 'Primary user-facing page for the generated capability.',
      position: { x: 360, y: 60 },
      boundDocumentIds: [],
      boundMemberIds: ['m3'],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
    },
    {
      id: `${id}-component`,
      type: 'component',
      title: `${baseTitle} Panel`,
      description: 'Reusable component surface for filters, states and repeated actions.',
      position: { x: 360, y: 230 },
      boundDocumentIds: [],
      boundMemberIds: ['m4'],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
    },
    {
      id: `${id}-api`,
      type: 'api',
      title: `GET /api/${slug || 'resource'}`,
      description: 'Initial API contract generated from the project brief.',
      position: { x: 660, y: 60 },
      boundDocumentIds: [],
      boundMemberIds: ['m5'],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
    },
    {
      id: `${id}-data`,
      type: 'dataModel',
      title: `${baseTitle}Record`,
      description: 'Data model backing the generated API contract.',
      position: { x: 940, y: 90 },
      boundDocumentIds: [],
      boundMemberIds: ['m5'],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
    },
    {
      id: `${id}-permission`,
      type: 'permission',
      title: 'Editor access',
      description: 'Owners can manage configuration; editors can operate the workflow; viewers can read.',
      position: { x: 660, y: 250 },
      boundDocumentIds: [],
      boundMemberIds: ['m1'],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
    },
    {
      id: `${id}-prototype`,
      type: 'prototype',
      title: `${baseTitle} Prototype`,
      description: 'Placeholder prototype node ready to bind imported design assets.',
      position: { x: 60, y: 330 },
      boundDocumentIds: [],
      boundMemberIds: ['m3'],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
      missing: ['No imported prototype source'],
    },
  ]
  const edges: BlueprintEdge[] = [
    { id: `${id}-e1`, sourceNodeId: nodes[0].id, targetNodeId: nodes[1].id, type: 'contains', isRequired: true },
    { id: `${id}-e2`, sourceNodeId: nodes[0].id, targetNodeId: nodes[2].id, type: 'uses', isRequired: false },
    { id: `${id}-e3`, sourceNodeId: nodes[1].id, targetNodeId: nodes[3].id, type: 'calls', isRequired: true },
    { id: `${id}-e4`, sourceNodeId: nodes[3].id, targetNodeId: nodes[4].id, type: 'reads', isRequired: true },
    { id: `${id}-e5`, sourceNodeId: nodes[0].id, targetNodeId: nodes[5].id, type: 'requires', isRequired: true },
    { id: `${id}-e6`, sourceNodeId: nodes[0].id, targetNodeId: nodes[6].id, type: 'syncs_with', isRequired: false },
  ]
  return {
    id,
    title: `${baseTitle} Blueprint`,
    status: 'draft',
    ownerId: 'm1',
    nodes,
    edges,
    generatedDocIds: [],
    version: 1,
    updatedAt: 'Just now',
  }
}

export function blueprintFromDocuments(
  id: string,
  projectName: string,
  docs: TeamDocument[],
  deps: DocumentDependency[],
): Blueprint {
  const nodes = docs.map<BlueprintNode>((doc, index) => ({
    id: `${id}-doc-${doc.id}`,
    type:
      doc.type === 'requirement' || doc.type === 'featureList'
        ? 'feature'
        : doc.type === 'pageSplit'
          ? 'page'
          : doc.type === 'apiContract'
            ? 'api'
            : doc.type === 'uiPrototype'
              ? 'prototype'
              : 'workbenchTarget',
    title: doc.title,
    description: doc.summary,
    position: {
      x: 80 + (index % 4) * 270,
      y: 80 + Math.floor(index / 4) * 120,
    },
    boundDocumentIds: [doc.id],
    boundMemberIds: unique([doc.ownerId, ...doc.members.map((member) => member.userId)]),
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  }))
  const edges = deps
    .map<BlueprintEdge | null>((dep) => {
      const source = nodes.find((node) => node.boundDocumentIds.includes(dep.sourceDocId))
      const target = nodes.find((node) => node.boundDocumentIds.includes(dep.targetDocId))
      if (!source || !target) return null
      return {
        id: `${id}-edge-${dep.id}`,
        sourceNodeId: source.id,
        targetNodeId: target.id,
        type:
          dep.type === 'implements'
            ? 'implemented_by'
            : dep.type === 'syncs_with'
              ? 'syncs_with'
              : dep.type === 'depends_on'
                ? 'contains'
                : 'generates',
        isRequired: dep.isBlocking || dep.type === 'depends_on',
      }
    })
    .filter((edge): edge is BlueprintEdge => Boolean(edge))
  return {
    id,
    title: `${projectName} Blueprint`,
    status: 'draft',
    ownerId: 'm1',
    nodes,
    edges,
    generatedDocIds: [],
    version: 1,
    updatedAt: 'Just now',
  }
}

export function docsFromBlueprint(source: Blueprint) {
  const createdDocs: TeamDocument[] = []
  const nodeToDoc = new Map<string, string>()
  source.nodes.forEach((node, index) => {
    const spec = BLUEPRINT_DOC_OUTPUTS[node.type][0]
    const id = `d-graph-${source.id}-${node.id}`
    nodeToDoc.set(node.id, id)
    createdDocs.push({
      id,
      type: spec.type,
      title: `${node.title} ${spec.suffix}`,
      status: 'draft',
      ownerId: node.boundMemberIds[0] ?? 'm1',
      members:
        node.boundMemberIds.length > 0
          ? node.boundMemberIds.map((userId, memberIndex) => ({
              userId,
              role: memberIndex === 0 ? 'owner' : 'downstreamOwner',
              boundReason: `Inherited from Blueprint node ${node.title}`,
            }))
          : [{ userId: 'm1', role: 'owner', boundReason: 'Default Blueprint owner' }],
      updatedAt: 'Just now',
      blocking: 0,
      bindings: 1,
      externalSync: node.type === 'prototype' ? 'figma' : null,
      position: {
        x: 40 + (index % 4) * 300,
        y: 60 + Math.floor(index / 4) * 130,
      },
      summary: `Generated from Blueprint node "${node.title}" (${node.type}).`,
      sections: [
        {
          title: spec.sectionTitle,
          body: `Generated from project Blueprint "${source.title}".`,
        },
        {
          title: 'Blueprint source',
          body: node.description ?? `Node type: ${node.type}.`,
        },
      ],
    })
  })

  const dependencies = source.edges
    .map<DocumentDependency | null>((edge) => {
      const sourceDocId = nodeToDoc.get(edge.sourceNodeId)
      const targetDocId = nodeToDoc.get(edge.targetNodeId)
      if (!sourceDocId || !targetDocId || sourceDocId === targetDocId) return null
      return {
        id: `e-graph-${edge.id}`,
        sourceDocId,
        targetDocId,
        type: edge.type === 'implemented_by' ? 'implements' : edge.type === 'syncs_with' ? 'syncs_with' : 'generates',
        isBlocking: edge.isRequired,
      }
    })
    .filter((dependency): dependency is DocumentDependency => Boolean(dependency))

  const bindings: NodeBinding[] = source.nodes.flatMap((node) => {
    const targetId = nodeToDoc.get(node.id)
    if (!targetId) return []
    return [{
      id: `nb-graph-${source.id}-${node.id}`,
      sourceKind: bindingKindForBlueprintNode(node.type),
      sourceId: node.id,
      targetKind: 'document',
      targetId,
      label: `${node.title} generates document`,
      relation: 'generates',
      isBlocking: false,
      requiredForReview: true,
      notifyOnChange: true,
      createdAt: 'Just now',
    }]
  })

  const blueprint = {
    ...source,
    status: 'docsGenerated' as const,
    generatedDocIds: createdDocs.map((doc) => doc.id),
    nodes: source.nodes.map((node) => {
      const docId = nodeToDoc.get(node.id)
      return docId
        ? {
            ...node,
            boundDocumentIds: unique([...node.boundDocumentIds, docId]),
            generatedDocIds: unique([...node.generatedDocIds, docId]),
            missing: [],
          }
        : node
    }),
    updatedAt: 'Just now',
  }

  return { documents: createdDocs, dependencies, nodeBindings: bindings, blueprint }
}

export function buildTeamProject(
  id: string,
  name: string,
  source: TeamProjectSource,
  options: {
    phase?: string
    documents?: TeamDocument[]
    dependencies?: DocumentDependency[]
    nodeBindings?: NodeBinding[]
    importAssets?: ImportAsset[]
    linkedDocIds?: string[]
    blueprint?: Blueprint
  } = {},
): TeamProject {
  return {
    id,
    teamId: 'team-acme',
    teamName: 'Acme',
    name,
    phase: options.phase ?? (source === 'blank' ? 'Empty project setup' : 'Design & Contract'),
    updatedAt: 'Just now',
    source,
    graphId: `graph-${id}`,
    blueprintId: options.blueprint?.id ?? `bp-${id}`,
    documents: cloneDocuments(options.documents ?? []),
    dependencies: cloneDependencies(options.dependencies ?? []),
    nodeBindings: cloneNodeBindings(options.nodeBindings ?? []),
    importAssets: cloneImportAssets(options.importAssets ?? []),
    linkedDocIds: [...(options.linkedDocIds ?? [])],
    blueprint: cloneBlueprint(options.blueprint ?? blankBlueprint(`bp-${id}`, name), {
      id: options.blueprint?.id ?? `bp-${id}`,
      title: options.blueprint?.title ?? `${name} Blueprint`,
    }),
  }
}

export function createBlueprintWorkbenchContextDraft(options: {
  blueprint: Blueprint
  documents: TeamDocument[]
  selectedNodeId: string
  idSeed?: string | number
}) {
  const selected = options.blueprint.nodes.find((node) => node.id === options.selectedNodeId)
  if (!selected) return null

  const idSeed = options.idSeed ?? Date.now()
  let workingBlueprint = options.blueprint
  let selection = collectBlueprintSelection(workingBlueprint, selected.id)
  let workbenchTarget = selection.nodes.find((node) => node.type === 'workbenchTarget')

  if (!workbenchTarget) {
    workbenchTarget = {
      id: `b-target-${idSeed}`,
      type: 'workbenchTarget',
      title: `Implement ${selected.title}`,
      description: 'Created when this Blueprint selection was sent to Workbench.',
      position: { x: selected.position.x + 260, y: selected.position.y + 160 },
      boundDocumentIds: [],
      boundMemberIds: [],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
    }
    const implementationEdge: BlueprintEdge = {
      id: `be-target-${idSeed}`,
      sourceNodeId: selected.id,
      targetNodeId: workbenchTarget.id,
      type: 'implemented_by',
      isRequired: false,
    }
    workingBlueprint = {
      ...workingBlueprint,
      nodes: [...workingBlueprint.nodes, workbenchTarget],
      edges: [...workingBlueprint.edges, implementationEdge],
    }
    selection = collectBlueprintSelection(workingBlueprint, selected.id)
  }

  const linkedDocIds = unique(
    selection.nodes.flatMap((node) => [...node.boundDocumentIds, ...node.generatedDocIds]),
  )
  const linkedDocs = options.documents.filter((doc) => linkedDocIds.includes(doc.id))
  const memberIds = unique(selection.nodes.flatMap((node) => node.boundMemberIds))
  const missingItems = selection.nodes.flatMap((node) =>
    (node.missing ?? []).map((item) => `${node.title}: ${item}`),
  )
  const prompt = blueprintPrompt(workingBlueprint, selected, selection.nodes, selection.edges, linkedDocs)
  const context: BlueprintWorkbenchContext = {
    id: `bwc-${idSeed}`,
    blueprintId: workingBlueprint.id,
    selectedNodeId: selected.id,
    title: selected.title,
    prompt,
    nodeIds: selection.nodeIds,
    edgeIds: selection.edgeIds,
    linkedDocIds,
    memberIds,
    prototypeNodeIds: selection.nodes
      .filter((node) => node.type === 'prototype')
      .map((node) => node.id),
    missingItems,
    workbenchTargetId: workbenchTarget.id,
    status: 'inImplementation',
    createdAt: 'Just now',
  }

  return {
    blueprint: {
      ...workingBlueprint,
      status: 'inImplementation' as const,
      updatedAt: 'Just now',
    },
    context,
    linkedDocIds,
    prompt,
  }
}

export function syncWorkbenchResultToDocuments(
  documents: TeamDocument[],
  targetDocIds: string[],
  latestVersion: ProjectVersion,
) {
  return documents.map((doc) => {
    if (!targetDocIds.includes(doc.id)) return doc
    const alreadySynced = doc.sections.some((section) => section.title === 'Workbench 回写')
    const syncSection = {
      title: 'Workbench 回写',
      body: `${latestVersion.title} 已回写：预览路径 /，实现摘要包含任务输入、筛选、空态、构建通过记录和 Blueprint Target 状态。`,
    }
    return {
      ...doc,
      status: 'readyForReview' as const,
      updatedAt: 'Just now',
      sections: alreadySynced
        ? doc.sections.map((section) => (section.title === syncSection.title ? syncSection : section))
        : [...doc.sections, syncSection],
    }
  })
}

export function syncWorkbenchResultToBlueprint(options: {
  blueprint: Blueprint
  context: BlueprintWorkbenchContext
  latestVersion: ProjectVersion
  targetDocIds: string[]
}) {
  const hasImplementationEdge = options.blueprint.edges.some(
    (edge) =>
      edge.sourceNodeId === options.context.selectedNodeId &&
      edge.targetNodeId === options.context.workbenchTargetId &&
      edge.type === 'implemented_by',
  )

  return {
    ...options.blueprint,
    status: 'implemented' as const,
    nodes: options.blueprint.nodes.map((node) =>
      node.id === options.context.workbenchTargetId
        ? {
            ...node,
            title: options.latestVersion.title,
            description: `${options.latestVersion.subtitle}. Preview URL: /`,
            boundDocumentIds: unique([...node.boundDocumentIds, ...options.targetDocIds]),
            missing: [],
          }
        : node,
    ),
    edges: hasImplementationEdge
      ? options.blueprint.edges
      : [
          ...options.blueprint.edges,
          {
            id: `be-implemented-${Date.now()}`,
            sourceNodeId: options.context.selectedNodeId,
            targetNodeId: options.context.workbenchTargetId,
            type: 'implemented_by' as const,
            isRequired: false,
          },
        ],
    updatedAt: 'Just now',
  }
}

export const INITIAL_TEAM_PROJECTS: TeamProject[] = [
  buildTeamProject('tp-crm', 'CRM Rewrite', 'template', {
    phase: 'Design & Contract',
    documents: TEAM_DOCUMENTS,
    dependencies: DOC_DEPENDENCIES,
    nodeBindings: NODE_BINDINGS,
    importAssets: IMPORT_ASSETS,
    linkedDocIds: ['d3', 'd4', 'd6', 'd7'],
    blueprint: cloneBlueprint(DEFAULT_BLUEPRINT, {
      id: 'bp-tp-crm',
      title: 'CRM Rewrite Blueprint',
    }),
  }),
  buildTeamProject('tp-billing', 'Billing Console', 'blank', {
    phase: 'Empty graph',
    documents: [],
    dependencies: [],
    nodeBindings: [],
    importAssets: [],
    linkedDocIds: [],
    blueprint: blankBlueprint('bp-tp-billing', 'Billing Console'),
  }),
]
