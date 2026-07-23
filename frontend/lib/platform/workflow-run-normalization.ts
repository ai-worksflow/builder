import type {
  ExactArtifactRefDto,
  WorkflowActionBlockingReasonDto,
  WorkflowNodeAction,
  WorkflowNodeRunDto,
  WorkflowNodeStatus,
  WorkflowNodeType,
  WorkflowRunDto,
  WorkflowRunStatus,
} from './flow-contract'

const workflowNodeActions = new Set<WorkflowNodeAction>([
  'submit_input',
  'record_proposal',
  'authorize_execution',
  'approve_review',
  'request_review_changes',
  'waive_review',
  'retry',
])

const workflowNodeTypes = new Set<WorkflowNodeType>([
  'artifact_input',
  'ai_transform',
  'human_edit',
  'review_gate',
  'condition',
  'fan_out',
  'merge',
  'quality_gate',
  'external_qualification_gate',
  'manifest_compiler',
  'workbench_build',
  'publish',
  'transform',
])

const workflowNodeStatuses = new Set<WorkflowNodeStatus>([
  'pending',
  'ready',
  'running',
  'waiting_input',
  'waiting_review',
  'waiting_qualification',
  'completed',
  'failed',
  'cancelled',
  'stale',
])

const workflowRunStatuses = new Set<WorkflowRunStatus>([
  'pending',
  'running',
  'waiting_input',
  'waiting_review',
  'waiting_qualification',
  'completed',
  'failed',
  'cancelled',
  'stale',
])

const invalidProjectionReason: WorkflowActionBlockingReasonDto = {
  code: 'workflow_action_projection_invalid',
  message: 'The server-authoritative Workflow action projection is unavailable or malformed.',
  sourceRef: null,
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function executionProfileRef(value: unknown) {
  const source = objectValue(value)
  return Boolean(source && nonEmptyString(source.version) && nonEmptyString(source.hash))
}

function exactArtifactRef(value: unknown): value is ExactArtifactRefDto {
  const source = objectValue(value)
  if (!source) return false
  if (
    !nonEmptyString(source.artifactId)
    || !nonEmptyString(source.revisionId)
    || !nonEmptyString(source.contentHash)
  ) return false
  if (
    source.revisionNumber !== undefined
    && (
      typeof source.revisionNumber !== 'number'
      || !Number.isInteger(source.revisionNumber)
      || source.revisionNumber < 1
    )
  ) return false
  return source.anchorId === undefined || typeof source.anchorId === 'string'
}

function workflowAction(value: unknown): value is WorkflowNodeAction {
  return typeof value === 'string' && workflowNodeActions.has(value as WorkflowNodeAction)
}

function blockingReason(value: unknown): value is WorkflowActionBlockingReasonDto {
  const source = objectValue(value)
  return Boolean(
    source
    && typeof source.code === 'string'
    && source.code.trim()
    && typeof source.message === 'string'
    && source.message.trim()
    && (source.sourceRef === null || exactArtifactRef(source.sourceRef)),
  )
}

function validRunActionEnvelope(source: Record<string, unknown>) {
  const definition = objectValue(source.definition)
  const executionProfile = objectValue(source.executionProfile)
  const definitionProfile = objectValue(definition?.executionProfile)
  return Boolean(
    nonEmptyString(source.id)
    && nonEmptyString(source.projectId)
    && nonEmptyString(source.definitionVersionId)
    && nonEmptyString(source.startedBy)
    && workflowRunStatuses.has(source.status as WorkflowRunStatus)
    && typeof source.eventCursor === 'number'
    && Number.isInteger(source.eventCursor)
    && source.eventCursor >= 0
    && definition
    && nonEmptyString(definition.id)
    && typeof definition.version === 'number'
    && Number.isInteger(definition.version)
    && definition.version >= 1
    && nonEmptyString(definition.hash)
    && executionProfileRef(executionProfile)
    && executionProfileRef(definitionProfile)
    && executionProfile?.version === definitionProfile?.version
    && executionProfile?.hash === definitionProfile?.hash
  )
}

function validNodeActionIdentity(source: Record<string, unknown>, runId: string) {
  return Boolean(
    nonEmptyString(source.id)
    && source.runId === runId
    && nonEmptyString(source.key)
    && nonEmptyString(source.definitionNodeId)
    && workflowNodeTypes.has(source.type as WorkflowNodeType)
    && workflowNodeStatuses.has(source.status as WorkflowNodeStatus)
    && typeof source.attempt === 'number'
    && Number.isInteger(source.attempt)
    && source.attempt >= 0
    && nonEmptyString(source.availableAt)
    && nonEmptyString(source.createdAt)
    && nonEmptyString(source.updatedAt)
  )
}

function normalizeNode(
  source: Record<string, unknown>,
  identityBound: boolean,
): WorkflowNodeRunDto {
  const actions = source.allowedActions
  const reasons = source.blockingReasons
  const validActions = Array.isArray(actions)
    && actions.every(workflowAction)
    && new Set(actions).size === actions.length
  const validReasons = Array.isArray(reasons)
    && reasons.every(blockingReason)
    && new Set(reasons.map((reason) => `${reason.code}:${reason.message}`)).size === reasons.length

  if (!identityBound || !validActions || !validReasons) {
    return {
      ...source,
      allowedActions: [],
      blockingReasons: [invalidProjectionReason],
    } as unknown as WorkflowNodeRunDto
  }
  return {
    ...source,
    allowedActions: [...actions],
    blockingReasons: [...reasons],
  } as unknown as WorkflowNodeRunDto
}

/**
 * Keeps historical/malformed wire data from becoming UI authority. All
 * Workflow mutations remain closed unless the server provides a complete,
 * recognized, duplicate-free action projection for that exact node.
 */
export function normalizeWorkflowRun(value: WorkflowRunDto): WorkflowRunDto {
  const source = objectValue(value)
  if (!source) throw new TypeError('Workflow run response must be an object.')
  const rawNodes = Array.isArray(source.nodes) ? source.nodes : []
  const nodeSources = rawNodes.map(objectValue)
  const completeNodeEnvelope = Array.isArray(source.nodes) && nodeSources.every(Boolean)
  const runId = nonEmptyString(source.id) ? source.id : ''
  const idCounts = new Map<string, number>()
  const keyCounts = new Map<string, number>()
  for (const node of nodeSources) {
    if (!node) continue
    if (nonEmptyString(node.id)) idCounts.set(node.id, (idCounts.get(node.id) ?? 0) + 1)
    if (nonEmptyString(node.key)) keyCounts.set(node.key, (keyCounts.get(node.key) ?? 0) + 1)
  }
  const runAuthorityValid = completeNodeEnvelope && validRunActionEnvelope(source)
  const nodes = nodeSources.flatMap((node) => {
    if (!node) return []
    const identityBound = runAuthorityValid
      && validNodeActionIdentity(node, runId)
      && idCounts.get(node.id as string) === 1
      && keyCounts.get(node.key as string) === 1
    return [normalizeNode(node, identityBound)]
  })
  return { ...source, nodes } as unknown as WorkflowRunDto
}
