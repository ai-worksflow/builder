import type { TeamProject } from './types'

const DOC_TYPES = new Set([
  'requirement',
  'pageSplit',
  'featureList',
  'apiContract',
  'backendDev',
  'uiPrototype',
  'frontendDev',
])
const DOC_STATUSES = new Set([
  'draft',
  'readyForReview',
  'changesRequested',
  'approved',
  'needsSync',
  'archived',
])
const MEMBER_ROLES = new Set(['owner', 'assignee', 'downstreamOwner', 'reviewer', 'watcher'])
const PROJECT_SOURCES = new Set(['blank', 'template', 'blueprint', 'import'])
const BLUEPRINT_STATUSES = new Set([
  'draft',
  'validated',
  'readyForDocs',
  'docsGenerated',
  'inImplementation',
  'implemented',
  'outdated',
])

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function string(value: unknown, maximum = 100_000) {
  return typeof value === 'string' && value.length <= maximum
}

function optionalString(value: unknown, maximum = 100_000) {
  return value === undefined || string(value, maximum)
}

function position(value: unknown) {
  return (
    isRecord(value) &&
    typeof value.x === 'number' &&
    Number.isFinite(value.x) &&
    typeof value.y === 'number' &&
    Number.isFinite(value.y)
  )
}

function stringArray(value: unknown, maximum = 2_000) {
  return Array.isArray(value) && value.length <= maximum && value.every((item) => string(item, 500))
}

function document(value: unknown) {
  if (!isRecord(value)) return false
  return (
    string(value.id, 200) &&
    DOC_TYPES.has(String(value.type)) &&
    string(value.title, 500) &&
    DOC_STATUSES.has(String(value.status)) &&
    string(value.ownerId, 200) &&
    string(value.updatedAt, 200) &&
    typeof value.blocking === 'number' &&
    typeof value.bindings === 'number' &&
    position(value.position) &&
    string(value.summary) &&
    Array.isArray(value.sections) &&
    value.sections.length <= 500 &&
    value.sections.every(
      (section) => isRecord(section) && string(section.title, 500) && string(section.body),
    ) &&
    Array.isArray(value.members) &&
    value.members.length <= 500 &&
    value.members.every(
      (member) =>
        isRecord(member) &&
        string(member.userId, 200) &&
        MEMBER_ROLES.has(String(member.role)) &&
        optionalString(member.boundReason, 1_000),
    ) &&
    optionalString(value.teamId, 200) &&
    optionalString(value.projectId, 200) &&
    (value.version === undefined || typeof value.version === 'number') &&
    (value.lastApprovedVersion === undefined || typeof value.lastApprovedVersion === 'number') &&
    (value.prototypeArtifactIds === undefined || stringArray(value.prototypeArtifactIds))
  )
}

function dependency(value: unknown) {
  return (
    isRecord(value) &&
    string(value.id, 200) &&
    string(value.sourceDocId, 200) &&
    string(value.targetDocId, 200) &&
    string(value.type, 100) &&
    typeof value.isBlocking === 'boolean'
  )
}

function binding(value: unknown) {
  return (
    isRecord(value) &&
    string(value.id, 200) &&
    string(value.sourceKind, 100) &&
    string(value.sourceId, 200) &&
    string(value.targetKind, 100) &&
    string(value.targetId, 200) &&
    string(value.label, 500) &&
    string(value.relation, 100) &&
    typeof value.isBlocking === 'boolean' &&
    typeof value.requiredForReview === 'boolean' &&
    typeof value.notifyOnChange === 'boolean' &&
    string(value.createdAt, 200)
  )
}

function importAsset(value: unknown) {
  return (
    isRecord(value) &&
    string(value.id, 200) &&
    string(value.source, 100) &&
    string(value.name, 500) &&
    string(value.syncStatus, 100) &&
    string(value.ownerId, 200) &&
    optionalString(value.sourceUrl, 2_000) &&
    optionalString(value.externalId, 500) &&
    optionalString(value.linkedDocId, 200) &&
    optionalString(value.lastSyncedAt, 200) &&
    optionalString(value.linkedDocTitle, 500) &&
    optionalString(value.snapshotUrl, 2_000)
  )
}

function blueprintNode(value: unknown) {
  return (
    isRecord(value) &&
    string(value.id, 200) &&
    string(value.type, 100) &&
    string(value.title, 500) &&
    optionalString(value.description) &&
    position(value.position) &&
    stringArray(value.boundDocumentIds) &&
    stringArray(value.boundMemberIds) &&
    stringArray(value.boundPrototypeArtifactIds) &&
    stringArray(value.generatedDocIds) &&
    (value.missing === undefined || stringArray(value.missing))
  )
}

function blueprintEdge(value: unknown) {
  return (
    isRecord(value) &&
    string(value.id, 200) &&
    string(value.sourceNodeId, 200) &&
    string(value.targetNodeId, 200) &&
    string(value.type, 100) &&
    typeof value.isRequired === 'boolean'
  )
}

function blueprint(value: unknown) {
  return (
    isRecord(value) &&
    string(value.id, 200) &&
    string(value.title, 500) &&
    BLUEPRINT_STATUSES.has(String(value.status)) &&
    string(value.ownerId, 200) &&
    Array.isArray(value.nodes) &&
    value.nodes.length <= 2_000 &&
    value.nodes.every(blueprintNode) &&
    Array.isArray(value.edges) &&
    value.edges.length <= 5_000 &&
    value.edges.every(blueprintEdge) &&
    stringArray(value.generatedDocIds) &&
    typeof value.version === 'number' &&
    string(value.updatedAt, 200)
  )
}

function teamProject(value: unknown) {
  return (
    isRecord(value) &&
    string(value.id, 200) &&
    string(value.teamId, 200) &&
    string(value.teamName, 500) &&
    string(value.name, 500) &&
    string(value.phase, 500) &&
    string(value.updatedAt, 200) &&
    PROJECT_SOURCES.has(String(value.source)) &&
    string(value.graphId, 200) &&
    string(value.blueprintId, 200) &&
    Array.isArray(value.documents) &&
    value.documents.length <= 2_000 &&
    value.documents.every(document) &&
    Array.isArray(value.dependencies) &&
    value.dependencies.length <= 5_000 &&
    value.dependencies.every(dependency) &&
    Array.isArray(value.nodeBindings) &&
    value.nodeBindings.length <= 5_000 &&
    value.nodeBindings.every(binding) &&
    Array.isArray(value.importAssets) &&
    value.importAssets.length <= 2_000 &&
    value.importAssets.every(importAsset) &&
    stringArray(value.linkedDocIds) &&
    blueprint(value.blueprint)
  )
}

export function isTeamProjectList(value: unknown): value is TeamProject[] {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.length <= 200 &&
    value.every(teamProject) &&
    new Set(value.map((project) => project.id)).size === value.length
  )
}
