import type {
  ArtifactRevisionDto,
  BlueprintContentDto,
  DocumentContentDto,
  JsonObject,
  PageSpecContentDto,
  ProposalDto,
  PrototypeContentDto,
  VersionRefDto,
  VersionedArtifactDto,
} from './dto'
import type {
  CreateWorkflowDefinitionInputDto,
  ExactArtifactRefDto,
  WorkflowCapabilitiesDto,
  WorkflowEdgeDto,
  WorkflowInputContractDto,
  WorkflowNodeDefinitionDto,
  WorkflowNodeRunDto,
  WorkflowNodeType,
  WorkflowOutputContractDto,
  WorkflowRunDto,
} from './flow-contract'

export interface EditableWorkflowDefinition {
  readonly name: string
  readonly schemaVersion: string
  readonly inputContract: WorkflowInputContractDto
  readonly outputContract: WorkflowOutputContractDto
  readonly nodes: readonly WorkflowNodeDefinitionDto[]
  readonly edges: readonly WorkflowEdgeDto[]
}

export interface WorkflowArtifactSnapshot {
  readonly documents: readonly VersionedArtifactDto<DocumentContentDto>[]
  readonly blueprints: readonly VersionedArtifactDto<BlueprintContentDto>[]
  readonly pageSpecs: readonly VersionedArtifactDto<PageSpecContentDto>[]
  readonly prototypes: readonly VersionedArtifactDto<PrototypeContentDto>[]
  readonly proposals: readonly ProposalDto[]
}

export interface WorkflowRevisionCandidate {
  readonly key: string
  readonly label: string
  readonly ref: ExactArtifactRefDto
  readonly artifactId: string
  readonly lineageSource: 'proposal_target' | 'delivery_slice' | 'artifact_lineage'
}

export interface WorkflowRevisionCandidateResolution {
  readonly candidates: readonly WorkflowRevisionCandidate[]
  readonly error?: string
}

interface LineageSliceRef {
  readonly id: string
  readonly key: string
  readonly fanOutNodeId: string
  readonly blueprint?: ExactArtifactRefDto
  readonly pageSpec?: ExactArtifactRefDto
  readonly prototype?: ExactArtifactRefDto
}

interface LineageBinding {
  readonly artifactRevisions: readonly ExactArtifactRefDto[]
  readonly deliverySliceRefs: readonly LineageSliceRef[]
  readonly outputProposal?: { readonly id: string; readonly payloadHash: string }
}

const WORKFLOW_NODE_TYPES: readonly WorkflowNodeType[] = [
  'artifact_input',
  'ai_transform',
  'human_edit',
  'review_gate',
  'condition',
  'fan_out',
  'merge',
  'manifest_compiler',
  'workbench_build',
  'quality_gate',
  'publish',
  'transform',
]

export function starterWorkflowDefinition(): {
  name: string
  schemaVersion: string
  inputContract: WorkflowInputContractDto
  outputContract: WorkflowOutputContractDto
  nodes: WorkflowNodeDefinitionDto[]
  edges: CreateWorkflowDefinitionInputDto['edges']
} {
  const envelope: JsonObject = { type: 'object', additionalProperties: true }
  return {
    name: 'Minimum product delivery loop',
    schemaVersion: '3',
    inputContract: {
      capability: 'project_brief',
      manifestJobTypes: ['conversation.workflow_intent', 'workflow_start'],
      artifactKinds: ['project_brief'],
      requiredSourcePurposes: ['project_brief'],
      manifestSchemaContracts: {
        'conversation.workflow_intent': 'workflow-intent-input/v1',
        workflow_start: 'workflow-input/v1',
      },
    },
    outputContract: {
      capability: 'application',
      producedArtifactKinds: ['workspace'],
      terminalOutcome: 'deployment',
      terminalNodeType: 'publish',
    },
    nodes: [
      node('source', 'Project brief input', 'artifact_input', envelope, {
        artifactInput: { allowedTypes: ['document'], allowedKinds: ['project_brief'], requireApproved: false, minimumArtifacts: 1 },
      }),
      node('project-brief-ai', 'Refine project brief proposal', 'ai_transform', envelope, {
        aiTransform: {
          jobType: 'refine_project_brief', modelPolicy: 'project-default',
          outputSchemaVersion: 'project-brief-proposal/v1', maxAttempts: 3, timeout: 300_000_000_000,
        },
      }),
      node('project-brief-edit', 'Interview and edit project brief', 'human_edit', envelope, {
        humanEdit: {
          artifactType: 'document',
          artifactKind: 'project_brief',
          requiredRole: 'editor',
          instructions: 'Review the AI proposal, resolve blocking questions, and create an exact Project Brief revision.',
        },
      }),
      node('project-brief-review', 'Approve project brief', 'review_gate', envelope, {
        reviewGate: { requiredRole: 'owner', minimumApprovals: 1, prohibitSelfReview: true, allowWaiver: false },
      }),
      node('requirements-ai', 'Generate requirements proposal', 'ai_transform', envelope, {
        aiTransform: {
          jobType: 'derive_requirements', modelPolicy: 'project-default',
          outputSchemaVersion: 'requirements-proposal/v1', maxAttempts: 3, timeout: 300_000_000_000,
        },
      }),
      node('requirements-edit', 'Edit requirements', 'human_edit', envelope, {
        humanEdit: {
          artifactType: 'document', artifactKind: 'product_requirements', requiredRole: 'editor',
          instructions: 'Resolve questions and produce stable requirement and acceptance IDs without bypassing the proposal review.',
        },
      }),
      node('requirements-review', 'Approve requirements', 'review_gate', envelope, {
        reviewGate: { requiredRole: 'owner', minimumApprovals: 1, prohibitSelfReview: true, allowWaiver: false },
      }),
      node('blueprint-ai', 'Compile baseline and generate blueprint proposal', 'ai_transform', envelope, {
        aiTransform: {
          jobType: 'decompose_pages', modelPolicy: 'project-default',
          outputSchemaVersion: 'blueprint-proposal/v1', maxAttempts: 3, timeout: 300_000_000_000,
        },
      }),
      node('blueprint-edit', 'Edit blueprint', 'human_edit', envelope, {
        humanEdit: {
          artifactType: 'blueprint', artifactKind: 'blueprint', requiredRole: 'editor',
          instructions: 'Review the proposal, close coverage gaps, and create one exact Blueprint revision. PageSpecs are created after Blueprint approval.',
        },
      }),
      node('blueprint-review', 'Approve blueprint', 'review_gate', envelope, {
        reviewGate: { requiredRole: 'owner', minimumApprovals: 1, prohibitSelfReview: true, allowWaiver: false },
      }),
      node('pages', 'Create Blueprint page branches', 'fan_out', envelope, {
        fanOut: { itemsPath: '/blueprintPages', sliceKeyPath: '/key', mergeNodeId: 'pages-merged', maxParallel: 4, itemKind: 'blueprint_page' },
      }),
      node('page-spec-ai', 'Generate page specification proposal', 'ai_transform', envelope, {
        aiTransform: {
          jobType: 'generate_page_spec', modelPolicy: 'project-default',
          outputSchemaVersion: 'page-spec-proposal/v1', maxAttempts: 3, timeout: 300_000_000_000,
        },
      }),
      node('page-spec-edit', 'Edit page specification', 'human_edit', envelope, {
        humanEdit: {
          artifactType: 'blueprint', artifactKind: 'page_spec', requiredRole: 'editor',
          instructions: "Review the proposal and create one exact PageSpec revision anchored to this branch's approved Blueprint page.",
        },
      }),
      node('page-spec-review', 'Approve page specification', 'review_gate', envelope, {
        reviewGate: { requiredRole: 'owner', minimumApprovals: 1, prohibitSelfReview: true, allowWaiver: false },
      }),
      node('prototype-ai', 'Generate page prototype proposal', 'ai_transform', envelope, {
        aiTransform: {
          jobType: 'generate_prototype', modelPolicy: 'project-default',
          outputSchemaVersion: 'prototype-proposal/v1', maxAttempts: 3, timeout: 300_000_000_000,
        },
      }),
      node('prototype-edit', 'Edit page prototype', 'human_edit', envelope, {
        humanEdit: {
          artifactType: 'prototype', artifactKind: 'prototype', requiredRole: 'editor',
          instructions: 'Adjust all required responsive states without changing the approved PageSpec.',
        },
      }),
      node('prototype-review', 'Approve page prototype', 'review_gate', envelope, {
        reviewGate: { requiredRole: 'owner', minimumApprovals: 1, prohibitSelfReview: true, allowWaiver: false },
      }),
      node('pages-merged', 'Merge approved page branches', 'merge', envelope, {
        merge: { fanOutNodeId: 'pages', policy: 'all', allowWaiver: false },
      }),
      node('compile-manifest', 'Freeze application build manifest', 'manifest_compiler', envelope, {
        manifestCompiler: { manifestKind: 'application_build', schemaVersion: 1, hook: 'application-build-manifest/v1' },
      }),
      node('workbench', 'Build in Workbench', 'workbench_build', envelope, {
        workbenchBuild: { buildManifestSchemaVersion: 1, maxAttempts: 3, timeout: 900_000_000_000 },
      }),
      node('quality', 'Quality gate', 'quality_gate', envelope, {
        qualityGate: { gateName: 'release', blocking: true, requiredRole: 'editor' },
      }),
      node('publish', 'Publish', 'publish', envelope, {
        publish: { environment: 'production', requiredRole: 'admin', allowRollback: true },
      }),
    ],
    edges: [
      edge(1, 'source', 'project-brief-ai'),
      edge(2, 'project-brief-ai', 'project-brief-edit'),
      edge(3, 'project-brief-edit', 'project-brief-review'),
      edge(4, 'project-brief-review', 'requirements-ai'),
      edge(5, 'requirements-ai', 'requirements-edit'),
      edge(6, 'requirements-edit', 'requirements-review'),
      edge(7, 'requirements-review', 'blueprint-ai'),
      edge(8, 'blueprint-ai', 'blueprint-edit'),
      edge(9, 'blueprint-edit', 'blueprint-review'),
      edge(10, 'blueprint-review', 'pages'),
      edge(11, 'pages', 'page-spec-ai'),
      edge(12, 'page-spec-ai', 'page-spec-edit'),
      edge(13, 'page-spec-edit', 'page-spec-review'),
      edge(14, 'page-spec-review', 'prototype-ai'),
      edge(15, 'prototype-ai', 'prototype-edit'),
      edge(16, 'prototype-edit', 'prototype-review'),
      edge(17, 'prototype-review', 'pages-merged'),
      edge(18, 'pages-merged', 'compile-manifest'),
      edge(19, 'compile-manifest', 'workbench'),
      edge(20, 'workbench', 'quality'),
      edge(21, 'quality', 'publish'),
    ],
  }
}

function node(
  id: string,
  name: string,
  type: WorkflowNodeType,
  schema: JsonObject,
  config: Partial<WorkflowNodeDefinitionDto>,
): WorkflowNodeDefinitionDto {
  return { id, name, type, inputSchema: schema, outputSchema: schema, ...config }
}

function edge(index: number, from: string, to: string): WorkflowEdgeDto {
  return { id: `edge-${String(index).padStart(2, '0')}`, from, to }
}

export function parseEditableDefinition(
  value: string,
  requireExecutable = false,
  capabilities?: WorkflowCapabilitiesDto | null,
): { readonly definition?: EditableWorkflowDefinition; readonly error?: string } {
  try {
    const parsed = JSON.parse(value) as unknown
    const error = validateWorkflowDefinition(parsed, requireExecutable, capabilities)
    if (error) return { error }
    return { definition: parsed as EditableWorkflowDefinition }
  } catch (cause) {
    return { error: cause instanceof Error ? cause.message : 'Definition JSON is invalid' }
  }
}

export function validateWorkflowDefinition(
  value: unknown,
  requireExecutable = true,
  capabilities?: WorkflowCapabilitiesDto | null,
): string | undefined {
  if (!object(value)) return 'Definition must be a JSON object.'
  if (!nonEmpty(value.name)) return 'Definition name is required.'
  if (!nonEmpty(value.schemaVersion)) return 'schemaVersion is required.'
  const inputContractError = validateInputContract(value.inputContract)
  if (inputContractError) return inputContractError
  const outputContractError = validateOutputContract(value.outputContract)
  if (outputContractError) return outputContractError
  if (!Array.isArray(value.nodes)) return 'nodes must be an array.'
  if (!Array.isArray(value.edges)) return 'edges must be an array.'
  if (value.nodes.length === 0) return 'nodes must contain at least one node.'
  if (value.nodes.length > 200) return 'nodes cannot contain more than 200 entries.'
  if (value.edges.length > 1_000) return 'edges cannot contain more than 1000 entries.'

  const nodes = new Map<string, WorkflowNodeDefinitionDto>()
  for (const [index, rawNode] of value.nodes.entries()) {
    const error = validateWorkflowNode(rawNode, `nodes[${index}]`)
    if (error) return error
    const typed = rawNode as WorkflowNodeDefinitionDto
    if (nodes.has(typed.id)) return `nodes[${index}].id duplicates ${typed.id}.`
    nodes.set(typed.id, typed)
  }
  const edgeIds = new Set<string>()
  const incoming = new Map([...nodes.keys()].map((id) => [id, 0]))
  const outgoing = new Map([...nodes.keys()].map((id) => [id, 0]))
  const adjacency = new Map([...nodes.keys()].map((id) => [id, [] as string[]]))
  const reverse = new Map([...nodes.keys()].map((id) => [id, [] as string[]]))
  for (const [index, rawEdge] of value.edges.entries()) {
    const path = `edges[${index}]`
    if (!object(rawEdge)) return `${path} must be a JSON object.`
    if (!nonEmpty(rawEdge.id) || !nonEmpty(rawEdge.from) || !nonEmpty(rawEdge.to)) return `${path} requires non-empty id, from, and to.`
    if (edgeIds.has(rawEdge.id)) return `${path}.id duplicates ${rawEdge.id}.`
    edgeIds.add(rawEdge.id)
    if (!nodes.has(rawEdge.from) || !nodes.has(rawEdge.to)) return `${path} references an unknown node.`
    if (rawEdge.from === rawEdge.to) return `${path} cannot connect a node to itself.`
    if (rawEdge.fromPort !== undefined && !nonEmpty(rawEdge.fromPort)) return `${path}.fromPort must be a non-empty string.`
    if (rawEdge.toPort !== undefined && !nonEmpty(rawEdge.toPort)) return `${path}.toPort must be a non-empty string.`
    if (rawEdge.mapping !== undefined && !stringRecord(rawEdge.mapping)) return `${path}.mapping must contain only string values.`
    const fromPort = nonEmpty(rawEdge.fromPort) ? rawEdge.fromPort : 'default'
    const toPort = nonEmpty(rawEdge.toPort) ? rawEdge.toPort : 'default'
    if (!resolvedPortNames(nodes.get(rawEdge.from), 'output').includes(fromPort)) return `${path}.fromPort ${fromPort} is not declared by ${rawEdge.from}.`
    if (!resolvedPortNames(nodes.get(rawEdge.to), 'input').includes(toPort)) return `${path}.toPort ${toPort} is not declared by ${rawEdge.to}.`
    incoming.set(rawEdge.to, (incoming.get(rawEdge.to) ?? 0) + 1)
    outgoing.set(rawEdge.from, (outgoing.get(rawEdge.from) ?? 0) + 1)
    adjacency.get(rawEdge.from)?.push(rawEdge.to)
    reverse.get(rawEdge.to)?.push(rawEdge.from)
  }
  if (!requireExecutable) return undefined

  const entries = [...incoming].filter(([, count]) => count === 0).map(([id]) => id)
  const terminals = [...outgoing].filter(([, count]) => count === 0).map(([id]) => id)
  if (entries.length !== 1) return `Workflow requires exactly one entry node; found ${entries.length}.`
  if (terminals.length !== 1) return `Workflow requires exactly one terminal node; found ${terminals.length}.`
  if (reachable(entries.at(0)!, adjacency).size !== nodes.size) return 'Every node must be reachable from the entry node.'
  if (reachable(terminals.at(0)!, reverse).size !== nodes.size) return 'Every node must have a path to the terminal node.'
  if (containsCycle(nodes.keys(), adjacency)) return 'Workflow graph must be acyclic.'
  const entry = nodes.get(entries[0])
  const inputContract = value.inputContract as unknown as WorkflowInputContractDto
  if (entry?.type !== 'artifact_input') return 'inputContract requires artifact_input as the single entry node.'
  if (!sameStringSet(entry.artifactInput?.allowedKinds ?? [], inputContract.artifactKinds)) return 'Entry allowedKinds must exactly match inputContract.artifactKinds.'
  const outputContract = value.outputContract as unknown as WorkflowOutputContractDto
  if (nodes.get(terminals[0])?.type !== outputContract.terminalNodeType) return 'outputContract.terminalNodeType must match the executable terminal node.'
  for (const workflowNode of nodes.values()) {
    if (workflowNode.type === 'fan_out') {
      const merge = workflowNode.fanOut && nodes.get(workflowNode.fanOut.mergeNodeId)
      if (!merge || merge.type !== 'merge' || merge.merge?.fanOutNodeId !== workflowNode.id) return `Node ${workflowNode.id} must reference a reciprocal merge node.`
    }
    if (workflowNode.type === 'merge') {
      const fanOut = workflowNode.merge && nodes.get(workflowNode.merge.fanOutNodeId)
      if (!fanOut || fanOut.type !== 'fan_out' || fanOut.fanOut?.mergeNodeId !== workflowNode.id) return `Node ${workflowNode.id} must reference a reciprocal fan-out node.`
    }
  }
  if (capabilities) {
    const capabilityError = validateRegisteredCapabilities(
      value as unknown as EditableWorkflowDefinition,
      capabilities,
    )
    if (capabilityError) return capabilityError
  }
  return undefined
}

export function validateWorkflowNode(value: unknown, path = 'node'): string | undefined {
  if (!object(value)) return `${path} must be a JSON object.`
  if (!nonEmpty(value.id)) return `${path}.id is required.`
  if (!nonEmpty(value.name)) return `${path}.name is required.`
  if (!WORKFLOW_NODE_TYPES.includes(value.type as WorkflowNodeType)) return `${path}.type is unsupported.`
  const inputError = validatePorts(value.inputPorts, `${path}.inputPorts`)
  if (inputError) return inputError
  const outputError = validatePorts(value.outputPorts, `${path}.outputPorts`)
  if (outputError) return outputError
  if (!hasPorts(value.inputPorts)) {
    const error = validateObjectSchema(value.inputSchema, `${path}.inputSchema`)
    if (error) return error
  }
  if (!hasPorts(value.outputPorts)) {
    const error = validateObjectSchema(value.outputSchema, `${path}.outputSchema`)
    if (error) return error
  }
  const configKeys = [
    'artifactInput', 'aiTransform', 'humanEdit', 'reviewGate', 'condition', 'fanOut',
    'merge', 'qualityGate', 'manifestCompiler', 'workbenchBuild', 'publish',
    'ai', 'humanTask', 'approval', 'transform', 'delivery',
  ] as const
  const configs = configKeys.filter((key) => value[key] !== undefined)
  if (configs.length !== 1) return `${path} must contain exactly one typed node config.`
  const type = value.type as WorkflowNodeType
  const expected: Record<WorkflowNodeType, (typeof configKeys)[number]> = {
    artifact_input: 'artifactInput', ai_transform: 'aiTransform', human_edit: 'humanEdit',
    review_gate: 'reviewGate', condition: 'condition', fan_out: 'fanOut', merge: 'merge',
    quality_gate: 'qualityGate', manifest_compiler: 'manifestCompiler',
    workbench_build: 'workbenchBuild', publish: 'publish',
    transform: 'transform',
  }
  if (configs.at(0) !== expected[type]) return `${path}.${expected[type]} must match type ${type}.`
  const config = value[expected[type]]
  if (!object(config)) return `${path}.${expected[type]} must be a JSON object.`
  return validateNodeConfig(type, config, path)
}

function validateInputContract(value: unknown) {
  if (!object(value)) return 'inputContract is required.'
  if (!nonEmpty(value.capability)) return 'inputContract.capability is required.'
  if (!nonEmptyStringArray(value.manifestJobTypes)) return 'inputContract.manifestJobTypes must contain supported job types.'
  if (!nonEmptyStringArray(value.artifactKinds) || !value.artifactKinds.every(validArtifactKind)) return 'inputContract.artifactKinds must contain exact artifact kinds.'
  if (!nonEmptyStringArray(value.requiredSourcePurposes)) return 'inputContract.requiredSourcePurposes must not be empty.'
  if (!stringRecord(value.manifestSchemaContracts)) return 'inputContract.manifestSchemaContracts must map job types to schema versions.'
  const schemaContracts = value.manifestSchemaContracts
  const jobs = new Set(value.manifestJobTypes)
  const schemaJobs = Object.keys(schemaContracts)
  if (schemaJobs.length !== jobs.size || schemaJobs.some((jobType) => !jobs.has(jobType) || !nonEmpty(schemaContracts[jobType]))) return 'inputContract.manifestSchemaContracts must exactly map every manifestJobType.'
  return undefined
}

function validateOutputContract(value: unknown) {
  if (!object(value)) return 'outputContract is required.'
  if (!nonEmpty(value.capability)) return 'outputContract.capability is required.'
  if (!nonEmptyStringArray(value.producedArtifactKinds) || !value.producedArtifactKinds.every(validArtifactKind)) return 'outputContract.producedArtifactKinds must contain exact artifact kinds.'
  if (!['application', 'deployment'].includes(String(value.terminalOutcome))) return 'outputContract.terminalOutcome is unsupported.'
  if (!WORKFLOW_NODE_TYPES.includes(value.terminalNodeType as WorkflowNodeType)) return 'outputContract.terminalNodeType is unsupported.'
  return undefined
}

function validateRegisteredCapabilities(
  definition: EditableWorkflowDefinition,
  capabilities: WorkflowCapabilitiesDto,
) {
  const inputRegistered = capabilities.inputContracts.some((contract) =>
    contract.capability === definition.inputContract.capability
    && sameStringSet(contract.manifestJobTypes, definition.inputContract.manifestJobTypes)
    && sameStringSet(contract.artifactKinds, definition.inputContract.artifactKinds)
    && sameStringSet(contract.requiredSourcePurposes, definition.inputContract.requiredSourcePurposes)
    && sameStringRecord(contract.manifestSchemaContracts, definition.inputContract.manifestSchemaContracts))
  if (!inputRegistered) return 'inputContract is not registered by the workflow service.'
  const outputRegistered = capabilities.outputContracts.some((contract) =>
    contract.capability === definition.outputContract.capability
    && contract.terminalOutcome === definition.outputContract.terminalOutcome
    && contract.terminalNodeType === definition.outputContract.terminalNodeType
    && sameStringSet(contract.producedArtifactKinds, definition.outputContract.producedArtifactKinds))
  if (!outputRegistered) return 'outputContract is not registered by the workflow service.'
  for (const node of definition.nodes) {
    if (!capabilities.nodeTypes.includes(node.type)) return `Node ${node.id} uses an unregistered node type.`
    if (node.aiTransform && !capabilities.aiTransforms.some((capability) =>
      capability.jobType === node.aiTransform?.jobType
      && capability.outputSchemaVersion === node.aiTransform?.outputSchemaVersion
      && capability.modelPolicies.includes(node.aiTransform?.modelPolicy ?? ''))) return `Node ${node.id} uses an unregistered AI transform.`
    if (node.manifestCompiler && !capabilities.manifestCompilers.some((capability) =>
      capability.manifestKind === node.manifestCompiler?.manifestKind
      && capability.schemaVersion === node.manifestCompiler?.schemaVersion
      && capability.hook === node.manifestCompiler?.hook)) return `Node ${node.id} uses an unregistered manifest compiler.`
    if (node.transform && !capabilities.transforms.includes(node.transform.transform)) return `Node ${node.id} uses an unregistered transform.`
    const fanOutKind = node.fanOut?.itemKind ?? (node.fanOut ? 'generic' : undefined)
    if (fanOutKind && !capabilities.fanOutItemKinds.includes(fanOutKind)) return `Node ${node.id} uses an unregistered fan-out resolver.`
    if (node.qualityGate && !capabilities.qualityGates.includes(node.qualityGate.gateName)) return `Node ${node.id} uses an unregistered quality gate.`
    if (node.publish && !capabilities.publishEnvironments.includes(node.publish.environment)) return `Node ${node.id} uses an unregistered publish environment.`
    if (node.workbenchBuild && !capabilities.workbenchSchemaVersions.includes(node.workbenchBuild.buildManifestSchemaVersion)) return `Node ${node.id} uses an unregistered Workbench schema.`
  }
  return undefined
}

function sameStringSet(left: readonly string[], right: readonly string[]) {
  return [...new Set(left)].sort().join('\u0000') === [...new Set(right)].sort().join('\u0000')
}

function sameStringRecord(left: Readonly<Record<string, string>>, right: Readonly<Record<string, string>>) {
  const leftEntries = Object.entries(left).sort(([leftKey], [rightKey]) => leftKey.localeCompare(rightKey))
  const rightEntries = Object.entries(right).sort(([leftKey], [rightKey]) => leftKey.localeCompare(rightKey))
  return JSON.stringify(leftEntries) === JSON.stringify(rightEntries)
}

function nonEmptyStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.length > 0 && value.every(nonEmpty) && new Set(value).size === value.length
}

function validateNodeConfig(type: WorkflowNodeType, config: Record<string, unknown>, path: string) {
  switch (type) {
    case 'artifact_input':
      if (!Array.isArray(config.allowedTypes) || config.allowedTypes.length === 0 || !config.allowedTypes.every(validArtifactType) || (config.allowedKinds !== undefined && (!nonEmptyStringArray(config.allowedKinds) || !config.allowedKinds.every(validArtifactKind))) || typeof config.requireApproved !== 'boolean' || !positiveInteger(config.minimumArtifacts)) return `${path}.artifactInput is malformed.`
      break
    case 'ai_transform':
      if (!nonEmpty(config.jobType) || !nonEmpty(config.modelPolicy) || !nonEmpty(config.outputSchemaVersion) || !positiveInteger(config.maxAttempts) || !positiveNumber(config.timeout)) return `${path}.aiTransform is malformed.`
      break
    case 'human_edit':
      if (!validArtifactType(config.artifactType) || (config.artifactKind !== undefined && !validArtifactKind(config.artifactKind)) || !validRole(config.requiredRole) || (config.instructions !== undefined && typeof config.instructions !== 'string')) return `${path}.humanEdit is malformed.`
      break
    case 'review_gate':
      if (!validRole(config.requiredRole) || !positiveInteger(config.minimumApprovals) || typeof config.prohibitSelfReview !== 'boolean' || typeof config.allowWaiver !== 'boolean') return `${path}.reviewGate is malformed.`
      break
    case 'condition':
      if (!Array.isArray(config.branches) || config.branches.length < 2 || config.branches.some((branch) => !object(branch) || !nonEmpty(branch.name) || typeof branch.default !== 'boolean' || (branch.expression !== undefined && typeof branch.expression !== 'string'))) return `${path}.condition.branches is malformed.`
      if (config.branches.filter((branch) => object(branch) && branch.default === true).length !== 1) return `${path}.condition requires exactly one default branch.`
      if (new Set(config.branches.map((branch) => object(branch) ? branch.name : '')).size !== config.branches.length) return `${path}.condition branch names must be unique.`
      if (config.branches.some((branch) => object(branch) && branch.default === false && !nonEmpty(branch.expression))) return `${path}.condition non-default branches require an expression.`
      break
    case 'fan_out':
      if (!jsonPointer(config.itemsPath) || !jsonPointer(config.sliceKeyPath) || !nonEmpty(config.mergeNodeId) || !positiveInteger(config.maxParallel) || (config.itemKind !== undefined && !['generic', 'delivery_slice', 'blueprint_page', 'blueprint_selection_page'].includes(String(config.itemKind)))) return `${path}.fanOut is malformed.`
      break
    case 'merge':
      if (!nonEmpty(config.fanOutNodeId) || !['all', 'any', 'quorum'].includes(String(config.policy)) || typeof config.allowWaiver !== 'boolean' || (config.policy === 'quorum' && !positiveInteger(config.quorum))) return `${path}.merge is malformed.`
      break
    case 'quality_gate':
      if (!nonEmpty(config.gateName) || typeof config.blocking !== 'boolean' || (config.requiredRole !== undefined && !validRole(config.requiredRole))) return `${path}.qualityGate is malformed.`
      break
    case 'manifest_compiler':
      if (!nonEmpty(config.manifestKind) || !positiveInteger(config.schemaVersion) || !nonEmpty(config.hook)) return `${path}.manifestCompiler is malformed.`
      break
    case 'workbench_build':
      if (!positiveInteger(config.buildManifestSchemaVersion) || !positiveInteger(config.maxAttempts) || !positiveNumber(config.timeout)) return `${path}.workbenchBuild is malformed.`
      break
    case 'publish':
      if (!nonEmpty(config.environment) || !validRole(config.requiredRole) || typeof config.allowRollback !== 'boolean') return `${path}.publish is malformed.`
      break
    case 'transform':
      if (config.transform !== 'selection_passthrough') return `${path}.transform must be selection_passthrough.`
      break
  }
  return undefined
}

export function resolvedPortNames(node: WorkflowNodeDefinitionDto | undefined, direction: 'input' | 'output') {
  if (!node) return ['default']
  const ports = direction === 'input' ? node.inputPorts : node.outputPorts
  const names = ports ? Object.keys(ports) : []
  return names.length > 0 ? names : ['default']
}

export function resolveCandidateSelection(
  candidates: readonly WorkflowRevisionCandidate[],
  explicitKey: string,
) {
  if (explicitKey) return candidates.find((candidate) => candidate.key === explicitKey)
  return candidates.length === 1 ? candidates.at(0) : undefined
}

export function workflowRoleSatisfies(actual: string, required: string) {
  const rank: Readonly<Record<string, number>> = {
    viewer: 1,
    commenter: 2,
    editor: 3,
    admin: 4,
    owner: 5,
  }
  return (rank[actual] ?? 0) >= (rank[required] ?? Number.POSITIVE_INFINITY)
}

export function revisionCandidates(
  definitionNode: WorkflowNodeDefinitionDto | undefined,
  nodeRun: WorkflowNodeRunDto,
  run: WorkflowRunDto | null,
  artifacts: WorkflowArtifactSnapshot,
): WorkflowRevisionCandidateResolution {
  const type = definitionNode?.humanEdit?.artifactType
  const kind = definitionNode?.humanEdit?.artifactKind
  if (nodeRun.type !== 'human_edit' || !type) return { candidates: [], error: 'This node is not a typed Human Edit node.' }
  const lineage = nodeInputLineage(run, nodeRun)
  if (lineage.error) return { candidates: [], error: lineage.error }
  let bindings = lineage.bindings
  if (nodeRun.sliceId) {
    bindings = bindings.filter((binding) => binding.deliverySliceRefs.some((slice) => slice.id === nodeRun.sliceId))
    if (bindings.length === 0) return { candidates: [], error: `The current node input has no delivery-slice lineage for ${nodeRun.sliceId}.` }
  }
  const allResources: readonly VersionedArtifactDto<unknown>[] = [
    ...artifacts.documents,
    ...artifacts.blueprints,
    ...artifacts.pageSpecs,
    ...artifacts.prototypes,
  ]
  const resources: readonly VersionedArtifactDto<unknown>[] = kind
    ? allResources.filter((resource) => resource.artifact.kind === kind)
    : type === 'document'
      ? artifacts.documents
      : type === 'blueprint'
        ? [...artifacts.blueprints, ...artifacts.pageSpecs]
        : type === 'prototype'
          ? artifacts.prototypes
          : []
  const proposalRefs = uniqueBy(
    bindings.flatMap((binding) => binding.outputProposal ? [binding.outputProposal] : []),
    (proposal) => `${proposal.id}:${proposal.payloadHash}`,
  )
  const proposalTargetIds = new Set<string>()
  const appliedProposalIds = new Set<string>()
  for (const proposalRef of proposalRefs) {
    const proposal = artifacts.proposals.find((item) => item.id === proposalRef.id && item.payloadHash === proposalRef.payloadHash)
    if (!proposal) return { candidates: [], error: `Proposal ${proposalRef.id} from the typed input lineage is unavailable or has a different payload hash.` }
    if (!exactRef(proposal.baseRevision) || proposal.baseRevision.artifactId !== proposal.artifactId) {
      return { candidates: [], error: `Proposal ${proposalRef.id} has an invalid target revision.` }
    }
    proposalTargetIds.add(proposal.artifactId)
    if (proposal.status !== 'applied' && proposal.status !== 'partially_applied') {
      return { candidates: [], error: 'Review and apply the linked Proposal in the corresponding editor before creating an immutable revision.' }
    }
    appliedProposalIds.add(proposal.id)
  }
  const sliceTargetIds = new Set<string>()
  for (const binding of bindings) {
    for (const slice of binding.deliverySliceRefs) {
      if (nodeRun.sliceId && slice.id !== nodeRun.sliceId) continue
      const target = kind === 'page_spec'
        ? slice.pageSpec
        : kind === 'blueprint' || (!kind && type === 'blueprint')
          ? slice.blueprint
          : type === 'prototype'
            ? slice.prototype
            : undefined
      if (target) sliceTargetIds.add(target.artifactId)
    }
  }
  const artifactLineageIds = new Set(bindings.flatMap((binding) => binding.artifactRevisions.map((ref) => ref.artifactId)))
  const allowedIds = proposalTargetIds.size > 0 ? proposalTargetIds : sliceTargetIds.size > 0 ? sliceTargetIds : artifactLineageIds
  const lineageSource: WorkflowRevisionCandidate['lineageSource'] = proposalTargetIds.size > 0
    ? 'proposal_target'
    : sliceTargetIds.size > 0
      ? 'delivery_slice'
      : 'artifact_lineage'
  if (allowedIds.size === 0) return { candidates: [], error: 'The current typed input contains no artifact, proposal target, or delivery-slice target for this Human Edit type.' }
  const candidates = resources.flatMap((resource) => {
    if (!allowedIds.has(resource.artifact.id)) return []
    const availableRevisions = uniqueBy(
      [resource.latestRevision, resource.approvedRevision].filter(
        (revision): revision is ArtifactRevisionDto<unknown> => Boolean(revision),
      ),
      (revision) => revision.id,
    )
    const revision = appliedProposalIds.size > 0
      ? [...availableRevisions]
          .sort((left, right) => right.revisionNumber - left.revisionNumber)
          .find((candidate) => revisionContainsAppliedProposal(candidate, availableRevisions, appliedProposalIds))
      : resource.latestRevision ?? resource.approvedRevision
    if (!revision) return []
    return [{
      key: `${revision.artifactId}:${revision.id}`,
      label: `${resource.artifact.title} · r${revision.revisionNumber} · ${resource.artifact.status}`,
      ref: exactRevisionRef(revision),
      artifactId: resource.artifact.id,
      lineageSource,
    }]
  }).sort((left, right) => left.label.localeCompare(right.label))
  return candidates.length > 0
    ? { candidates }
    : appliedProposalIds.size > 0
      ? { candidates, error: 'Apply the linked Proposal in the corresponding editor, then create an immutable revision before submitting this node.' }
      : { candidates, error: `No immutable ${kind ?? type} revision matches the current node ${lineageSource.replaceAll('_', ' ')}.` }
}

function revisionContainsAppliedProposal(
  candidate: ArtifactRevisionDto<unknown>,
  available: readonly ArtifactRevisionDto<unknown>[],
  proposalIds: ReadonlySet<string>,
) {
  const byId = new Map(available.map((revision) => [revision.id, revision]))
  const visited = new Set<string>()
  let current: ArtifactRevisionDto<unknown> | undefined = candidate
  while (current && !visited.has(current.id)) {
    if (current.proposalId && proposalIds.has(current.proposalId)) return true
    visited.add(current.id)
    current = current.basedOnRevisionId ? byId.get(current.basedOnRevisionId) : undefined
  }
  return false
}

export function deliverySliceContext(
  blueprintRevision: ExactArtifactRefDto,
  artifacts: WorkflowArtifactSnapshot,
): {
  readonly context?: { readonly deliverySlices: readonly DeliverySliceInput[] }
  readonly error?: string
} {
  const blueprint = artifacts.blueprints.find((resource) => resource.artifact.id === blueprintRevision.artifactId)
  const immutableBlueprint = blueprint && immutableRevisions(blueprint).find((revision) =>
    revision.id === blueprintRevision.revisionId && revision.contentHash === blueprintRevision.contentHash,
  )
  if (!immutableBlueprint) return { error: 'The selected Blueprint revision is not present in the current immutable workspace snapshot.' }
  const pinnedPageSpecs = immutableBlueprint.content.pageSpecRefs ?? []
  const blueprintPageNodeIds = new Set(
    (immutableBlueprint.content.semantic?.nodes ?? immutableBlueprint.content.nodes)
      .filter((node) => node.kind === 'page')
      .map((node) => node.id),
  )
  const slices = artifacts.pageSpecs.flatMap((pageSpec) => {
    const pageSpecRevision = immutableRevisions(pageSpec).find((revision) => {
      const ref = exactRevisionRef(revision)
      return pinnedPageSpecs.length > 0
        ? pinnedPageSpecs.some((pinned) => exactArtifactRefsEqual(pinned, ref))
        : pageSpecDerivesFromBlueprint(revision, blueprintRevision, blueprintPageNodeIds)
    })
    if (!pageSpecRevision) return []
    const pageSpecRef = exactRevisionRef(pageSpecRevision)
    const prototypeRevisions = artifacts.prototypes
      .flatMap(immutableRevisions)
      .filter((revision) => exactArtifactRefsEqual(revision.content.pageSpecRevision, pageSpecRef))
    const prototypeRevision = prototypeRevisions.length === 1 ? prototypeRevisions.at(0) : undefined
    return [{
      key: pageSpecRevision.content.blueprintPageNodeId || pageSpec.artifact.id,
      title: pageSpecRevision.content.title || pageSpec.artifact.title,
      blueprint: blueprintRevision,
      pageSpec: pageSpecRef,
      ...(prototypeRevision ? { prototype: exactRevisionRef(prototypeRevision) } : {}),
    }]
  })
  const expectedPageSpecs = uniqueBy(pinnedPageSpecs, (ref) => `${ref.artifactId}:${ref.revisionId}:${ref.contentHash}`)
  if (expectedPageSpecs.length > 0 && slices.length !== expectedPageSpecs.length) {
    return { error: 'One or more PageSpec revisions pinned by this Blueprint are unavailable; delivery slices were not submitted.' }
  }
  if (new Set(slices.map((slice) => slice.key)).size !== slices.length) {
    return { error: 'The selected Blueprint resolves duplicate PageSpec slice keys.' }
  }
  return slices.length > 0
    ? { context: { deliverySlices: slices } }
    : { error: 'This exact Blueprint revision has no immutable PageSpec lineage. Create PageSpec revisions pinned to this Blueprint before submitting.' }
}

function pageSpecDerivesFromBlueprint(
  revision: ArtifactRevisionDto<PageSpecContentDto>,
  blueprintRevision: VersionRefDto,
  blueprintPageNodeIds: ReadonlySet<string>,
) {
  const pageNodeId = revision.content.blueprintPageNodeId.trim()
  if (!pageNodeId || !blueprintPageNodeIds.has(pageNodeId)) return false
  return (revision.sourceVersions ?? []).some((source) =>
    source.artifactId === blueprintRevision.artifactId
    && source.revisionId === blueprintRevision.revisionId
    && source.contentHash === blueprintRevision.contentHash
    && source.anchorId === pageNodeId,
  )
}

interface DeliverySliceInput {
  readonly key: string
  readonly title: string
  readonly blueprint: ExactArtifactRefDto
  readonly pageSpec: ExactArtifactRefDto
  readonly prototype?: ExactArtifactRefDto
}

function nodeInputLineage(
  run: WorkflowRunDto | null,
  nodeRun: WorkflowNodeRunDto,
): { readonly bindings: readonly LineageBinding[]; readonly error?: string } {
  if (!run || run.id !== nodeRun.runId) return { bindings: [], error: 'The hydrated workflow run does not match this node.' }
  const metadata = run.context.nodes[nodeRun.key] as unknown
  if (!object(metadata)) return { bindings: [], error: 'The run has no metadata for this node.' }
  const input = metadata.input
  if (!object(input) || !Array.isArray(input.bindings) || !nonEmpty(input.hash)) return { bindings: [], error: 'The current node has no valid typed input envelope.' }
  const bindings: LineageBinding[] = []
  for (const [index, rawBinding] of input.bindings.entries()) {
    if (!object(rawBinding) || !object(rawBinding.source)) return { bindings: [], error: `Typed input binding ${index} is malformed.` }
    const rawArtifacts = rawBinding.source.artifactRevisions ?? []
    const rawSlices = rawBinding.source.deliverySliceRefs ?? []
    if (!Array.isArray(rawArtifacts) || !Array.isArray(rawSlices)) return { bindings: [], error: `Typed input binding ${index} lineage is malformed.` }
    const artifactRevisions = rawArtifacts.filter(exactRef)
    if (artifactRevisions.length !== rawArtifacts.length) return { bindings: [], error: `Typed input binding ${index} contains a malformed artifact reference.` }
    const deliverySliceRefs = rawSlices.filter(lineageSlice)
    if (deliverySliceRefs.length !== rawSlices.length) return { bindings: [], error: `Typed input binding ${index} contains a malformed delivery-slice reference.` }
    const outputProposal = rawBinding.source.outputProposal
    if (outputProposal !== undefined && (!object(outputProposal) || !nonEmpty(outputProposal.id) || !nonEmpty(outputProposal.payloadHash))) return { bindings: [], error: `Typed input binding ${index} contains a malformed proposal reference.` }
    bindings.push({
      artifactRevisions,
      deliverySliceRefs,
      ...(outputProposal ? { outputProposal: outputProposal as LineageBinding['outputProposal'] } : {}),
    })
  }
  return bindings.length > 0 ? { bindings } : { bindings, error: 'The typed input envelope has no enabled incoming bindings.' }
}

function exactRevisionRef(revision: { readonly artifactId: string; readonly id: string; readonly revisionNumber?: number; readonly contentHash: string }): ExactArtifactRefDto {
  return {
    artifactId: revision.artifactId,
    revisionId: revision.id,
    contentHash: revision.contentHash,
    ...(revision.revisionNumber ? { revisionNumber: revision.revisionNumber } : {}),
  }
}

function exactRef(value: unknown): value is ExactArtifactRefDto {
  return object(value) && nonEmpty(value.artifactId) && nonEmpty(value.revisionId) && nonEmpty(value.contentHash) && (value.anchorId === undefined || typeof value.anchorId === 'string')
}

function lineageSlice(value: unknown): value is LineageSliceRef {
  return object(value) && nonEmpty(value.id) && nonEmpty(value.key) && nonEmpty(value.fanOutNodeId)
    && (value.blueprint === undefined || exactRef(value.blueprint)) && (value.pageSpec === undefined || exactRef(value.pageSpec))
    && (value.prototype === undefined || exactRef(value.prototype))
}

export function exactArtifactRefsEqual(left: VersionRefDto, right: VersionRefDto) {
  return left.artifactId === right.artifactId && left.revisionId === right.revisionId
    && left.contentHash === right.contentHash && (left.anchorId ?? '') === (right.anchorId ?? '')
}

function immutableRevisions<TContent>(resource: VersionedArtifactDto<TContent>) {
  return uniqueBy(
    [resource.latestRevision, resource.approvedRevision].filter((revision) => revision !== undefined),
    (revision) => `${revision.artifactId}:${revision.id}:${revision.contentHash}`,
  )
}

function uniqueBy<T>(values: readonly T[], key: (value: T) => string) {
  const seen = new Set<string>()
  return values.filter((value) => {
    const candidate = key(value)
    if (seen.has(candidate)) return false
    seen.add(candidate)
    return true
  })
}

function validatePorts(value: unknown, path: string) {
  if (value === undefined) return undefined
  if (!object(value)) return `${path} must be a JSON object.`
  for (const [name, port] of Object.entries(value)) {
    if (!name.trim() || !object(port) || (port.description !== undefined && typeof port.description !== 'string')) return `${path}.${name || '<empty>'} must contain an object schema.`
    const error = validateObjectSchema(port.schema, `${path}.${name}.schema`)
    if (error) return error
  }
  return undefined
}

function hasPorts(value: unknown) {
  return object(value) && Object.keys(value).length > 0
}

function validateObjectSchema(value: unknown, path: string) {
  if (!object(value) || value.type !== 'object') return `${path} must declare a top-level object schema.`
  if (value.properties !== undefined && !object(value.properties)) return `${path}.properties must be a JSON object.`
  const properties = object(value.properties) ? value.properties : {}
  for (const [name, property] of Object.entries(properties)) {
    if (!name.trim() || !object(property) || !nonEmpty(property.type)) return `${path}.properties.${name || '<empty>'} must declare a type.`
  }
  if (value.required !== undefined) {
    if (!Array.isArray(value.required) || !value.required.every(nonEmpty)) return `${path}.required must contain property names.`
    if (new Set(value.required).size !== value.required.length) return `${path}.required cannot contain duplicates.`
    if (value.required.some((name) => !(name in properties))) return `${path}.required references an undeclared property.`
  }
  return undefined
}

function reachable(start: string, adjacency: ReadonlyMap<string, readonly string[]>) {
  const seen = new Set<string>()
  const queue = [start]
  while (queue.length > 0) {
    const current = queue.shift()
    if (!current || seen.has(current)) continue
    seen.add(current)
    queue.push(...(adjacency.get(current) ?? []))
  }
  return seen
}

function containsCycle(nodes: Iterable<string>, adjacency: ReadonlyMap<string, readonly string[]>) {
  const visiting = new Set<string>()
  const visited = new Set<string>()
  const visit = (id: string): boolean => {
    if (visiting.has(id)) return true
    if (visited.has(id)) return false
    visiting.add(id)
    if ((adjacency.get(id) ?? []).some(visit)) return true
    visiting.delete(id)
    visited.add(id)
    return false
  }
  return [...nodes].some(visit)
}

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function nonEmpty(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function positiveInteger(value: unknown) {
  return typeof value === 'number' && Number.isInteger(value) && value > 0
}

function positiveNumber(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
}

function jsonPointer(value: unknown) {
  return nonEmpty(value) && value.startsWith('/')
}

function validArtifactType(value: unknown): boolean {
  return typeof value === 'string' && ['document', 'blueprint', 'prototype', 'implementation', 'test'].includes(value)
}

function validArtifactKind(value: unknown): boolean {
  return typeof value === 'string' && [
    'project_brief', 'product_requirements', 'decision_record', 'glossary_policy',
    'reference_source', 'change_request', 'requirement_baseline', 'blueprint',
    'page_spec', 'prototype', 'prototype_flow', 'fixture_bundle', 'design_system',
    'token_set', 'component_registry', 'api_contract', 'data_contract',
    'permission_contract', 'workspace', 'test_report', 'quality_report',
  ].includes(value)
}

function validRole(value: unknown): boolean {
  return typeof value === 'string' && ['owner', 'admin', 'editor', 'commenter', 'viewer'].includes(value)
}

function stringRecord(value: unknown): value is Record<string, string> {
  return object(value) && Object.values(value).every((item) => typeof item === 'string')
}
