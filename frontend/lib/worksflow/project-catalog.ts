import {
  createWorkspace,
  isSafeWorkspacePath,
} from './workspace-model'
import type {
  VirtualWorkspace,
  WorkspaceBranch,
  WorkspaceCheckpoint,
  WorkspaceDiagnostic,
  WorkspaceFile,
  WorkspaceFileInput,
} from './workspace-model'
import type {
  BlueprintStatus,
  DependencyType,
  DocStatus,
  DocType,
} from './types'

export const PROJECT_CATALOG_SCHEMA = 'worksflow.project-catalog'
export const PROJECT_CATALOG_VERSION = 2 as const

export type ProjectSourceKind = 'blank' | 'template' | 'import' | 'clone'
export type ProjectLifecycleStatus = 'draft' | 'active' | 'archived'
export type ProjectCatalogIdKind =
  | 'project'
  | 'workspace'
  | 'generation-run'
  | 'generation-event'
  | 'attachment'
  | 'version'
  | 'deployment'

export interface ProjectCatalogRuntime {
  readonly now?: () => string
  readonly createId?: (kind: ProjectCatalogIdKind) => string
}

export interface ProjectSourceMetadata {
  readonly kind: ProjectSourceKind
  readonly templateId?: string
  readonly importProvider?: string
  readonly importReference?: string
  readonly clonedFromProjectId?: string
}

export type GenerationMode = 'plan' | 'build' | 'iterate' | 'fix'
export type GenerationRunStatus =
  | 'queued'
  | 'planning'
  | 'generating'
  | 'validating'
  | 'completed'
  | 'failed'
  | 'cancelled'
export type GenerationEventType =
  | 'status'
  | 'message'
  | 'file'
  | 'tool'
  | 'diagnostic'
  | 'usage'

export interface GenerationEventSummary {
  readonly id: string
  readonly type: GenerationEventType
  readonly summary: string
  readonly createdAt: string
  readonly path?: string
  readonly count?: number
  readonly durationMs?: number
}

export interface GenerationRunSummary {
  readonly id: string
  readonly prompt: string
  readonly mode: GenerationMode
  readonly model?: string
  readonly provider?: 'openai' | 'local'
  readonly status: GenerationRunStatus
  readonly startedAt: string
  readonly updatedAt: string
  readonly completedAt?: string
  readonly eventCount: number
  readonly events: readonly GenerationEventSummary[]
  readonly createdFileCount: number
  readonly updatedFileCount: number
  readonly inputTokens?: number
  readonly outputTokens?: number
  readonly totalTokens?: number
  readonly durationMs?: number
  readonly costUsd?: number
  readonly maxTokens?: number
  readonly errorMessage?: string
}

export type AttachmentKind = 'image' | 'document' | 'url' | 'repository' | 'other'

export interface AttachmentMetadata {
  readonly id: string
  readonly name: string
  readonly kind: AttachmentKind
  readonly mimeType?: string
  readonly sizeBytes?: number
  readonly createdAt: string
  readonly sourceUrl?: string
  readonly workspacePath?: string
  readonly checksum?: string
}

export interface ProjectVersionMetadata {
  readonly id: string
  readonly label: string
  readonly description?: string
  readonly createdAt: string
  readonly workspaceCheckpointId?: string
  readonly branchId?: string
  readonly generationRunId?: string
  readonly fileCount: number
}

export type DeploymentProvider =
  | 'none'
  | 'vercel'
  | 'netlify'
  | 'cloudflare'
  | 'custom'
export type DeploymentConnectionStatus = 'notConfigured' | 'ready' | 'error'
export type DeploymentStatus =
  | 'queued'
  | 'building'
  | 'ready'
  | 'failed'
  | 'cancelled'
export type DeploymentEnvironment = 'preview' | 'production'

export interface DeploymentSettingsMetadata {
  readonly provider: DeploymentProvider
  readonly status: DeploymentConnectionStatus
  readonly projectRef?: string
  readonly siteName?: string
  readonly region?: string
  readonly productionUrl?: string
  readonly environmentVariableNames: readonly string[]
  readonly lastDeploymentAt?: string
}

export interface DeploymentRecordSummary {
  readonly id: string
  readonly provider: Exclude<DeploymentProvider, 'none'>
  readonly status: DeploymentStatus
  readonly environment: DeploymentEnvironment
  readonly createdAt: string
  readonly completedAt?: string
  readonly url?: string
  readonly commitSha?: string
  readonly summary?: string
  readonly errorMessage?: string
}

export type DatabaseProvider =
  | 'none'
  | 'worksflow'
  | 'supabase'
  | 'neon'
  | 'planetscale'
  | 'custom'
export type DatabaseConnectionStatus = 'notConfigured' | 'provisioning' | 'ready' | 'error'

export interface DatabaseSettingsMetadata {
  readonly provider: DatabaseProvider
  readonly status: DatabaseConnectionStatus
  readonly projectRef?: string
  readonly databaseName?: string
  readonly region?: string
  readonly schemaNames: readonly string[]
  readonly tableNames: readonly string[]
  readonly authEnabled: boolean
  readonly storageBucketNames: readonly string[]
  readonly secretNames: readonly string[]
}

export type GithubConnectionStatus = 'disconnected' | 'connected' | 'error'

export interface GithubSettingsMetadata {
  readonly status: GithubConnectionStatus
  readonly host: string
  readonly owner?: string
  readonly repository?: string
  readonly defaultBranch?: string
  readonly installationRef?: string
  readonly lastCommitSha?: string
  readonly connectedAt?: string
  readonly permissionScopes: readonly string[]
}

export interface TeamDocumentReference {
  readonly id: string
  readonly type: DocType
  readonly title: string
  readonly status: DocStatus
  readonly updatedAt: string
}

export interface DocumentDependencyReference {
  readonly id: string
  readonly sourceDocumentId: string
  readonly targetDocumentId: string
  readonly type: DependencyType
  readonly isBlocking: boolean
}

export interface BlueprintReference {
  readonly id: string
  readonly title: string
  readonly status: BlueprintStatus
  readonly version: number
  readonly updatedAt: string
}

export interface TeamKnowledgeReferences {
  readonly documents: readonly TeamDocumentReference[]
  readonly dependencies: readonly DocumentDependencyReference[]
  readonly blueprints: readonly BlueprintReference[]
}

export interface ProductProject {
  readonly id: string
  readonly name: string
  readonly description?: string
  readonly teamId: string
  readonly teamName: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly starred: boolean
  readonly lifecycleStatus: ProjectLifecycleStatus
  readonly source: ProjectSourceMetadata
  readonly workspace: VirtualWorkspace
  readonly generationRuns: readonly GenerationRunSummary[]
  readonly attachments: readonly AttachmentMetadata[]
  readonly versions: readonly ProjectVersionMetadata[]
  readonly latestVersionId?: string
  readonly deployments: readonly DeploymentRecordSummary[]
  readonly deploymentSettings: DeploymentSettingsMetadata
  readonly databaseSettings: DatabaseSettingsMetadata
  readonly githubSettings: GithubSettingsMetadata
  readonly teamReferences: TeamKnowledgeReferences
}

export interface ProjectCatalog {
  readonly schema: typeof PROJECT_CATALOG_SCHEMA
  readonly version: typeof PROJECT_CATALOG_VERSION
  readonly createdAt: string
  readonly updatedAt: string
  readonly selectedProjectId: string
  readonly projects: readonly ProductProject[]
}

export interface ProjectCreationInput {
  readonly id?: string
  readonly name?: string
  readonly description?: string
  readonly teamId?: string
  readonly teamName?: string
  readonly starred?: boolean
  readonly lifecycleStatus?: ProjectLifecycleStatus
  readonly createdAt?: string
  readonly workspaceId?: string
  readonly files?: readonly WorkspaceFileInput[]
  readonly attachments?: readonly AttachmentMetadataInput[]
  readonly deploymentSettings?: unknown
  readonly databaseSettings?: unknown
  readonly githubSettings?: unknown
  readonly teamReferences?: TeamKnowledgeReferences
}

export interface AttachmentMetadataInput {
  readonly id?: string
  readonly name: string
  readonly kind?: AttachmentKind
  readonly mimeType?: string
  readonly sizeBytes?: number
  readonly createdAt?: string
  readonly sourceUrl?: string
  readonly workspacePath?: string
  readonly checksum?: string
}

export interface ProjectTemplate {
  readonly id: string
  readonly name: string
  readonly description?: string
  readonly workspace: VirtualWorkspace
  readonly attachments?: readonly AttachmentMetadata[]
  readonly deploymentSettings?: unknown
  readonly databaseSettings?: unknown
  readonly githubSettings?: unknown
  readonly teamReferences?: TeamKnowledgeReferences
}

export interface ImportedProjectInput extends Omit<ProjectCreationInput, 'files'> {
  readonly workspace: VirtualWorkspace
  readonly importProvider: string
  readonly importReference?: string
}

export interface CreateProjectCatalogOptions {
  readonly projects?: readonly ProductProject[]
  readonly selectedProjectId?: string
  readonly createdAt?: string
  readonly fallbackProject?: ProjectCreationInput
}

export interface CloneProjectOptions {
  readonly id?: string
  readonly name?: string
  readonly teamId?: string
  readonly teamName?: string
  readonly select?: boolean
}

export interface DeleteProjectOptions {
  readonly fallbackProject?: ProjectCreationInput
}

export interface GenerationEventInput {
  readonly id?: string
  readonly type: GenerationEventType
  readonly summary: string
  readonly createdAt?: string
  readonly path?: string
  readonly count?: number
  readonly durationMs?: number
}

export interface GenerationRunInput {
  readonly id?: string
  readonly prompt: string
  readonly mode?: GenerationMode
  readonly model?: string
  readonly provider?: 'openai' | 'local'
  readonly status: GenerationRunStatus
  readonly startedAt?: string
  readonly updatedAt?: string
  readonly completedAt?: string
  readonly events?: readonly GenerationEventInput[]
  readonly createdFileCount?: number
  readonly updatedFileCount?: number
  readonly inputTokens?: number
  readonly outputTokens?: number
  readonly totalTokens?: number
  readonly durationMs?: number
  readonly costUsd?: number
  readonly maxTokens?: number
  readonly errorMessage?: string
}

export interface DeploymentRecordInput extends Record<string, unknown> {
  readonly id?: string
  readonly provider?: Exclude<DeploymentProvider, 'none'>
  readonly status?: DeploymentStatus
  readonly environment?: DeploymentEnvironment
  readonly createdAt?: string
  readonly completedAt?: string
  readonly url?: string
  readonly commitSha?: string
  readonly summary?: string
  readonly errorMessage?: string
}

export interface ProjectVersionInput {
  readonly id?: string
  readonly label: string
  readonly description?: string
  readonly createdAt?: string
  readonly workspaceCheckpointId?: string
  readonly branchId?: string
  readonly generationRunId?: string
  readonly fileCount?: number
}

export interface ProjectSummaryRecord {
  readonly id: string
  readonly name: string
  readonly teamId: string
  readonly teamName: string
  readonly updatedAt: string
  readonly starred: boolean
  readonly lifecycleStatus: ProjectLifecycleStatus
  readonly source: ProjectSourceKind
  readonly fileCount: number
  readonly linkedDocumentCount: number
  readonly generationRunCount: number
  readonly latestGenerationStatus?: GenerationRunStatus
  readonly latestVersionLabel?: string
  readonly latestDeploymentStatus?: DeploymentStatus
}

export interface ProjectCatalogMigrationContext {
  readonly fromVersion: number
  readonly toVersion: number
}

const DOC_TYPES: readonly DocType[] = [
  'requirement',
  'pageSplit',
  'featureList',
  'apiContract',
  'backendDev',
  'uiPrototype',
  'frontendDev',
]
const DOC_STATUSES: readonly DocStatus[] = [
  'draft',
  'readyForReview',
  'changesRequested',
  'approved',
  'needsSync',
  'archived',
]
const DEPENDENCY_TYPES: readonly DependencyType[] = [
  'depends_on',
  'generates',
  'blocks',
  'implements',
  'reviews',
  'references',
  'composes',
  'derives_from',
  'syncs_with',
]
const BLUEPRINT_STATUSES: readonly BlueprintStatus[] = [
  'draft',
  'validated',
  'readyForDocs',
  'docsGenerated',
  'inImplementation',
  'implemented',
  'outdated',
]
const PROJECT_SOURCE_KINDS: readonly ProjectSourceKind[] = [
  'blank',
  'template',
  'import',
  'clone',
]
const PROJECT_LIFECYCLE_STATUSES: readonly ProjectLifecycleStatus[] = [
  'draft',
  'active',
  'archived',
]
const GENERATION_MODES: readonly GenerationMode[] = ['plan', 'build', 'iterate', 'fix']
const GENERATION_RUN_STATUSES: readonly GenerationRunStatus[] = [
  'queued',
  'planning',
  'generating',
  'validating',
  'completed',
  'failed',
  'cancelled',
]
const GENERATION_EVENT_TYPES: readonly GenerationEventType[] = [
  'status',
  'message',
  'file',
  'tool',
  'diagnostic',
  'usage',
]
const ATTACHMENT_KINDS: readonly AttachmentKind[] = [
  'image',
  'document',
  'url',
  'repository',
  'other',
]
const DEPLOYMENT_PROVIDERS: readonly DeploymentProvider[] = [
  'none',
  'vercel',
  'netlify',
  'cloudflare',
  'custom',
]
const DEPLOYMENT_CONNECTION_STATUSES: readonly DeploymentConnectionStatus[] = [
  'notConfigured',
  'ready',
  'error',
]
const DEPLOYMENT_STATUSES: readonly DeploymentStatus[] = [
  'queued',
  'building',
  'ready',
  'failed',
  'cancelled',
]
const DEPLOYMENT_ENVIRONMENTS: readonly DeploymentEnvironment[] = ['preview', 'production']
const DATABASE_PROVIDERS: readonly DatabaseProvider[] = [
  'none',
  'worksflow',
  'supabase',
  'neon',
  'planetscale',
  'custom',
]
const DATABASE_CONNECTION_STATUSES: readonly DatabaseConnectionStatus[] = [
  'notConfigured',
  'provisioning',
  'ready',
  'error',
]
const GITHUB_CONNECTION_STATUSES: readonly GithubConnectionStatus[] = [
  'disconnected',
  'connected',
  'error',
]

const SENSITIVE_KEY = /(?:secret|token|password|credential|private.?key|api.?key|connection.?string|authorization|cookie|signature)/i

function isRecord(value: unknown): value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false
  const prototype = Object.getPrototypeOf(value)
  return (
    (prototype === Object.prototype || prototype === null) &&
    Object.getOwnPropertySymbols(value).length === 0
  )
}

function hasOwn(value: object, key: string) {
  return Object.prototype.hasOwnProperty.call(value, key)
}

function hasOnlyKeys(value: Record<string, unknown>, allowed: readonly string[]) {
  const allowedKeys = new Set(allowed)
  return Object.keys(value).every((key) => allowedKeys.has(key))
}

function isNonemptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function isOptionalString(value: Record<string, unknown>, key: string) {
  return !hasOwn(value, key) || isNonemptyString(value[key])
}

function isTimestamp(value: unknown): value is string {
  return isNonemptyString(value) && Number.isFinite(Date.parse(value))
}

function isOptionalTimestamp(value: Record<string, unknown>, key: string) {
  return !hasOwn(value, key) || isTimestamp(value[key])
}

function isNonNegativeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0
}

function isPositiveInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) > 0
}

function isOptionalNonNegativeInteger(value: Record<string, unknown>, key: string) {
  return !hasOwn(value, key) || isNonNegativeInteger(value[key])
}

function isEnumValue<T extends string>(value: unknown, values: readonly T[]): value is T {
  return typeof value === 'string' && values.includes(value as T)
}

function hasUniqueStrings(values: readonly string[]) {
  return new Set(values).size === values.length
}

function isStringArray(value: unknown): value is string[] {
  return (
    Array.isArray(value) &&
    value.every(isNonemptyString) &&
    hasUniqueStrings(value)
  )
}

function deepFreeze<T>(value: T, seen = new WeakSet<object>()): T {
  if (typeof value !== 'object' || value === null || seen.has(value)) return value
  seen.add(value)
  Object.values(value).forEach((item) => deepFreeze(item, seen))
  return Object.freeze(value)
}

function defaultId(kind: ProjectCatalogIdKind) {
  const randomUuid = globalThis.crypto?.randomUUID?.()
  return randomUuid ? `${kind}-${randomUuid}` : `${kind}-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

function resolveId(runtime: ProjectCatalogRuntime, kind: ProjectCatalogIdKind) {
  const id = runtime.createId?.(kind) ?? defaultId(kind)
  if (!isNonemptyString(id)) throw new Error(`The ${kind} id factory returned an empty id`)
  return id
}

function resolveTime(runtime: ProjectCatalogRuntime, explicit?: string) {
  const value = explicit ?? runtime.now?.() ?? new Date().toISOString()
  if (!isTimestamp(value)) throw new Error(`Invalid project catalog timestamp "${value}"`)
  return value
}

function optionalTrimmed(value: unknown) {
  return isNonemptyString(value) ? value.trim() : undefined
}

function nonemptyOr(value: unknown, fallback: string) {
  return optionalTrimmed(value) ?? fallback
}

function nonNegativeOr(value: unknown, fallback = 0) {
  return isNonNegativeInteger(value) ? value : fallback
}

function uniqueStringValues(...values: unknown[]) {
  const result: string[] = []
  values.forEach((value) => {
    if (!Array.isArray(value)) return
    value.forEach((item) => {
      const normalized = optionalTrimmed(item)
      if (normalized && !result.includes(normalized)) result.push(normalized)
    })
  })
  return result
}

function objectKeys(value: unknown) {
  return isRecord(value) ? Object.keys(value).filter(isNonemptyString) : []
}

function redactSensitiveText(value: string) {
  return value
    .replace(/(Bearer\s+)[A-Za-z0-9._~+/-]+/gi, '$1[REDACTED]')
    .replace(
      /((?:secret|token|password|credential|private.?key|api.?key|connection.?string|authorization|cookie|signature)\s*[=:]\s*)[^\s,;&]+/gi,
      '$1[REDACTED]',
    )
}

function sanitizeMetadataUrl(value: unknown) {
  const candidate = optionalTrimmed(value)
  if (!candidate) return undefined

  try {
    const url = new URL(candidate)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return undefined
    url.username = ''
    url.password = ''
    Array.from(url.searchParams.keys()).forEach((key) => {
      if (SENSITIVE_KEY.test(key)) url.searchParams.delete(key)
    })
    return redactSensitiveText(url.toString())
  } catch {
    return undefined
  }
}

function isSafeMetadataUrl(value: unknown) {
  return isNonemptyString(value) && sanitizeMetadataUrl(value) === value
}

function enumOr<T extends string>(value: unknown, values: readonly T[], fallback: T) {
  return isEnumValue(value, values) ? value : fallback
}

function assertNonemptyName(value: unknown, label: string) {
  const name = optionalTrimmed(value)
  if (!name) throw new Error(`${label} must not be empty`)
  return name
}

function cloneWorkspaceFile(file: WorkspaceFile): WorkspaceFile {
  return {
    path: file.path,
    content: file.content,
    language: file.language,
    revision: file.revision,
    dirty: file.dirty,
  }
}

function cloneWorkspaceCheckpoint(checkpoint: WorkspaceCheckpoint): WorkspaceCheckpoint {
  return {
    id: checkpoint.id,
    label: checkpoint.label,
    ...(checkpoint.message === undefined ? {} : { message: checkpoint.message }),
    branchId: checkpoint.branchId,
    ...(checkpoint.parentCheckpointId === undefined
      ? {}
      : { parentCheckpointId: checkpoint.parentCheckpointId }),
    createdAt: checkpoint.createdAt,
    files: checkpoint.files.map(cloneWorkspaceFile),
  }
}

function cloneWorkspaceBranch(branch: WorkspaceBranch): WorkspaceBranch {
  return {
    id: branch.id,
    name: branch.name,
    createdAt: branch.createdAt,
    updatedAt: branch.updatedAt,
    ...(branch.baseCheckpointId === undefined
      ? {}
      : { baseCheckpointId: branch.baseCheckpointId }),
    ...(branch.headCheckpointId === undefined
      ? {}
      : { headCheckpointId: branch.headCheckpointId }),
  }
}

function cloneWorkspaceDiagnostic(diagnostic: WorkspaceDiagnostic): WorkspaceDiagnostic {
  return {
    id: diagnostic.id,
    severity: diagnostic.severity,
    message: diagnostic.message,
    ...(diagnostic.path === undefined ? {} : { path: diagnostic.path }),
    ...(diagnostic.source === undefined ? {} : { source: diagnostic.source }),
    ...(diagnostic.line === undefined ? {} : { line: diagnostic.line }),
    ...(diagnostic.column === undefined ? {} : { column: diagnostic.column }),
    ...(diagnostic.endLine === undefined ? {} : { endLine: diagnostic.endLine }),
    ...(diagnostic.endColumn === undefined ? {} : { endColumn: diagnostic.endColumn }),
  }
}

export function cloneVirtualWorkspace(
  workspace: VirtualWorkspace,
  overrides: Partial<Pick<VirtualWorkspace, 'id' | 'name' | 'createdAt' | 'updatedAt'>> = {},
): VirtualWorkspace {
  const cloned: VirtualWorkspace = {
    id: overrides.id ?? workspace.id,
    name: overrides.name ?? workspace.name,
    revision: workspace.revision,
    createdAt: overrides.createdAt ?? workspace.createdAt,
    updatedAt: overrides.updatedAt ?? workspace.updatedAt,
    files: workspace.files.map(cloneWorkspaceFile),
    checkpoints: workspace.checkpoints.map(cloneWorkspaceCheckpoint),
    branches: workspace.branches.map(cloneWorkspaceBranch),
    activeBranchId: workspace.activeBranchId,
    diagnostics: workspace.diagnostics.map(cloneWorkspaceDiagnostic),
  }
  if (!isStrictVirtualWorkspace(cloned)) throw new Error('Cannot clone an invalid virtual workspace')
  return cloned
}

function isWorkspaceFile(value: unknown): value is WorkspaceFile {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, ['path', 'content', 'language', 'revision', 'dirty']) &&
    isNonemptyString(value.path) &&
    isSafeWorkspacePath(value.path) &&
    typeof value.content === 'string' &&
    isNonemptyString(value.language) &&
    isNonNegativeInteger(value.revision) &&
    typeof value.dirty === 'boolean'
  )
}

function isWorkspaceCheckpoint(value: unknown): value is WorkspaceCheckpoint {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'id',
      'label',
      'message',
      'branchId',
      'parentCheckpointId',
      'createdAt',
      'files',
    ]) &&
    isNonemptyString(value.id) &&
    isNonemptyString(value.label) &&
    isOptionalString(value, 'message') &&
    isNonemptyString(value.branchId) &&
    isOptionalString(value, 'parentCheckpointId') &&
    isTimestamp(value.createdAt) &&
    Array.isArray(value.files) &&
    value.files.every(isWorkspaceFile) &&
    hasUniqueStrings(value.files.map((file) => file.path))
  )
}

function isWorkspaceBranch(value: unknown): value is WorkspaceBranch {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'id',
      'name',
      'createdAt',
      'updatedAt',
      'baseCheckpointId',
      'headCheckpointId',
    ]) &&
    isNonemptyString(value.id) &&
    isNonemptyString(value.name) &&
    isTimestamp(value.createdAt) &&
    isTimestamp(value.updatedAt) &&
    isOptionalString(value, 'baseCheckpointId') &&
    isOptionalString(value, 'headCheckpointId')
  )
}

function isWorkspaceDiagnostic(value: unknown): value is WorkspaceDiagnostic {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'id',
      'severity',
      'message',
      'path',
      'source',
      'line',
      'column',
      'endLine',
      'endColumn',
    ]) &&
    isNonemptyString(value.id) &&
    isEnumValue(value.severity, ['error', 'warning', 'info', 'hint']) &&
    isNonemptyString(value.message) &&
    isOptionalString(value, 'path') &&
    (!hasOwn(value, 'path') || isSafeWorkspacePath(value.path as string)) &&
    isOptionalString(value, 'source') &&
    ['line', 'column', 'endLine', 'endColumn'].every(
      (key) => !hasOwn(value, key) || isPositiveInteger(value[key]),
    )
  )
}

export function isStrictVirtualWorkspace(value: unknown): value is VirtualWorkspace {
  if (
    !isRecord(value) ||
    !hasOnlyKeys(value, [
      'id',
      'name',
      'revision',
      'createdAt',
      'updatedAt',
      'files',
      'checkpoints',
      'branches',
      'activeBranchId',
      'diagnostics',
    ]) ||
    !isNonemptyString(value.id) ||
    !isNonemptyString(value.name) ||
    !isNonNegativeInteger(value.revision) ||
    !isTimestamp(value.createdAt) ||
    !isTimestamp(value.updatedAt) ||
    !Array.isArray(value.files) ||
    !value.files.every(isWorkspaceFile) ||
    !Array.isArray(value.checkpoints) ||
    !value.checkpoints.every(isWorkspaceCheckpoint) ||
    !Array.isArray(value.branches) ||
    value.branches.length === 0 ||
    !value.branches.every(isWorkspaceBranch) ||
    !isNonemptyString(value.activeBranchId) ||
    !Array.isArray(value.diagnostics) ||
    !value.diagnostics.every(isWorkspaceDiagnostic)
  ) {
    return false
  }

  const filePaths = value.files.map((file) => file.path)
  const checkpointIds = value.checkpoints.map((checkpoint) => checkpoint.id)
  const branchIds = value.branches.map((branch) => branch.id)
  if (
    !hasUniqueStrings(filePaths) ||
    !hasUniqueStrings(checkpointIds) ||
    !hasUniqueStrings(branchIds) ||
    !branchIds.includes(value.activeBranchId)
  ) {
    return false
  }

  return (
    value.checkpoints.every(
      (checkpoint) =>
        branchIds.includes(checkpoint.branchId) &&
        (!checkpoint.parentCheckpointId || checkpointIds.includes(checkpoint.parentCheckpointId)),
    ) &&
    value.branches.every(
      (branch) =>
        (!branch.baseCheckpointId || checkpointIds.includes(branch.baseCheckpointId)) &&
        (!branch.headCheckpointId || checkpointIds.includes(branch.headCheckpointId)),
    )
  )
}

function isProjectSource(value: unknown): value is ProjectSourceMetadata {
  if (
    !isRecord(value) ||
    !isEnumValue(value.kind, PROJECT_SOURCE_KINDS)
  ) {
    return false
  }
  if (value.kind === 'template') {
    return hasOnlyKeys(value, ['kind', 'templateId']) && isNonemptyString(value.templateId)
  }
  if (value.kind === 'import') {
    return (
      hasOnlyKeys(value, ['kind', 'importProvider', 'importReference']) &&
      isNonemptyString(value.importProvider) &&
      isOptionalString(value, 'importReference')
    )
  }
  if (value.kind === 'clone') {
    return (
      hasOnlyKeys(value, ['kind', 'clonedFromProjectId']) &&
      isNonemptyString(value.clonedFromProjectId)
    )
  }
  return hasOnlyKeys(value, ['kind'])
}

function isGenerationEventSummary(value: unknown): value is GenerationEventSummary {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, ['id', 'type', 'summary', 'createdAt', 'path', 'count', 'durationMs']) &&
    isNonemptyString(value.id) &&
    isEnumValue(value.type, GENERATION_EVENT_TYPES) &&
    isNonemptyString(value.summary) &&
    isTimestamp(value.createdAt) &&
    isOptionalString(value, 'path') &&
    (!hasOwn(value, 'path') || isSafeWorkspacePath(value.path as string)) &&
    isOptionalNonNegativeInteger(value, 'count') &&
    isOptionalNonNegativeInteger(value, 'durationMs')
  )
}

function isGenerationRunSummary(value: unknown): value is GenerationRunSummary {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'id',
      'prompt',
      'mode',
      'model',
      'provider',
      'status',
      'startedAt',
      'updatedAt',
      'completedAt',
      'eventCount',
      'events',
      'createdFileCount',
      'updatedFileCount',
      'inputTokens',
      'outputTokens',
      'totalTokens',
      'durationMs',
      'costUsd',
      'maxTokens',
      'errorMessage',
    ]) &&
    isNonemptyString(value.id) &&
    typeof value.prompt === 'string' &&
    isEnumValue(value.mode, GENERATION_MODES) &&
    isOptionalString(value, 'model') &&
    (!hasOwn(value, 'provider') || value.provider === 'openai' || value.provider === 'local') &&
    isEnumValue(value.status, GENERATION_RUN_STATUSES) &&
    isTimestamp(value.startedAt) &&
    isTimestamp(value.updatedAt) &&
    isOptionalTimestamp(value, 'completedAt') &&
    isNonNegativeInteger(value.eventCount) &&
    Array.isArray(value.events) &&
    value.events.every(isGenerationEventSummary) &&
    value.eventCount === value.events.length &&
    hasUniqueStrings(value.events.map((event) => event.id)) &&
    isNonNegativeInteger(value.createdFileCount) &&
    isNonNegativeInteger(value.updatedFileCount) &&
    isOptionalNonNegativeInteger(value, 'inputTokens') &&
    isOptionalNonNegativeInteger(value, 'outputTokens') &&
    isOptionalNonNegativeInteger(value, 'totalTokens') &&
    isOptionalNonNegativeInteger(value, 'durationMs') &&
    (!hasOwn(value, 'costUsd') ||
      (typeof value.costUsd === 'number' && Number.isFinite(value.costUsd) && value.costUsd >= 0)) &&
    isOptionalNonNegativeInteger(value, 'maxTokens') &&
    isOptionalString(value, 'errorMessage')
  )
}

function isAttachmentMetadata(value: unknown): value is AttachmentMetadata {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'id',
      'name',
      'kind',
      'mimeType',
      'sizeBytes',
      'createdAt',
      'sourceUrl',
      'workspacePath',
      'checksum',
    ]) &&
    isNonemptyString(value.id) &&
    isNonemptyString(value.name) &&
    isEnumValue(value.kind, ATTACHMENT_KINDS) &&
    isOptionalString(value, 'mimeType') &&
    isOptionalNonNegativeInteger(value, 'sizeBytes') &&
    isTimestamp(value.createdAt) &&
    (!hasOwn(value, 'sourceUrl') || isSafeMetadataUrl(value.sourceUrl)) &&
    isOptionalString(value, 'workspacePath') &&
    (!hasOwn(value, 'workspacePath') || isSafeWorkspacePath(value.workspacePath as string)) &&
    isOptionalString(value, 'checksum')
  )
}

function isProjectVersionMetadata(value: unknown): value is ProjectVersionMetadata {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'id',
      'label',
      'description',
      'createdAt',
      'workspaceCheckpointId',
      'branchId',
      'generationRunId',
      'fileCount',
    ]) &&
    isNonemptyString(value.id) &&
    isNonemptyString(value.label) &&
    isOptionalString(value, 'description') &&
    isTimestamp(value.createdAt) &&
    isOptionalString(value, 'workspaceCheckpointId') &&
    isOptionalString(value, 'branchId') &&
    isOptionalString(value, 'generationRunId') &&
    isNonNegativeInteger(value.fileCount)
  )
}

function isDeploymentSettings(value: unknown): value is DeploymentSettingsMetadata {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'provider',
      'status',
      'projectRef',
      'siteName',
      'region',
      'productionUrl',
      'environmentVariableNames',
      'lastDeploymentAt',
    ]) &&
    isEnumValue(value.provider, DEPLOYMENT_PROVIDERS) &&
    isEnumValue(value.status, DEPLOYMENT_CONNECTION_STATUSES) &&
    isOptionalString(value, 'projectRef') &&
    isOptionalString(value, 'siteName') &&
    isOptionalString(value, 'region') &&
    (!hasOwn(value, 'productionUrl') || isSafeMetadataUrl(value.productionUrl)) &&
    isStringArray(value.environmentVariableNames) &&
    isOptionalTimestamp(value, 'lastDeploymentAt')
  )
}

function isDeploymentRecord(value: unknown): value is DeploymentRecordSummary {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'id',
      'provider',
      'status',
      'environment',
      'createdAt',
      'completedAt',
      'url',
      'commitSha',
      'summary',
      'errorMessage',
    ]) &&
    isNonemptyString(value.id) &&
    isEnumValue(value.provider, DEPLOYMENT_PROVIDERS.filter((provider) => provider !== 'none')) &&
    isEnumValue(value.status, DEPLOYMENT_STATUSES) &&
    isEnumValue(value.environment, DEPLOYMENT_ENVIRONMENTS) &&
    isTimestamp(value.createdAt) &&
    isOptionalTimestamp(value, 'completedAt') &&
    (!hasOwn(value, 'url') || isSafeMetadataUrl(value.url)) &&
    isOptionalString(value, 'commitSha') &&
    isOptionalString(value, 'summary') &&
    isOptionalString(value, 'errorMessage')
  )
}

function isDatabaseSettings(value: unknown): value is DatabaseSettingsMetadata {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'provider',
      'status',
      'projectRef',
      'databaseName',
      'region',
      'schemaNames',
      'tableNames',
      'authEnabled',
      'storageBucketNames',
      'secretNames',
    ]) &&
    isEnumValue(value.provider, DATABASE_PROVIDERS) &&
    isEnumValue(value.status, DATABASE_CONNECTION_STATUSES) &&
    isOptionalString(value, 'projectRef') &&
    isOptionalString(value, 'databaseName') &&
    isOptionalString(value, 'region') &&
    isStringArray(value.schemaNames) &&
    isStringArray(value.tableNames) &&
    typeof value.authEnabled === 'boolean' &&
    isStringArray(value.storageBucketNames) &&
    isStringArray(value.secretNames)
  )
}

function isGithubSettings(value: unknown): value is GithubSettingsMetadata {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'status',
      'host',
      'owner',
      'repository',
      'defaultBranch',
      'installationRef',
      'lastCommitSha',
      'connectedAt',
      'permissionScopes',
    ]) &&
    isEnumValue(value.status, GITHUB_CONNECTION_STATUSES) &&
    isNonemptyString(value.host) &&
    isOptionalString(value, 'owner') &&
    isOptionalString(value, 'repository') &&
    isOptionalString(value, 'defaultBranch') &&
    isOptionalString(value, 'installationRef') &&
    isOptionalString(value, 'lastCommitSha') &&
    isOptionalTimestamp(value, 'connectedAt') &&
    isStringArray(value.permissionScopes)
  )
}

function isTeamDocumentReference(value: unknown): value is TeamDocumentReference {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, ['id', 'type', 'title', 'status', 'updatedAt']) &&
    isNonemptyString(value.id) &&
    isEnumValue(value.type, DOC_TYPES) &&
    isNonemptyString(value.title) &&
    isEnumValue(value.status, DOC_STATUSES) &&
    isNonemptyString(value.updatedAt)
  )
}

function isDocumentDependencyReference(value: unknown): value is DocumentDependencyReference {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'id',
      'sourceDocumentId',
      'targetDocumentId',
      'type',
      'isBlocking',
    ]) &&
    isNonemptyString(value.id) &&
    isNonemptyString(value.sourceDocumentId) &&
    isNonemptyString(value.targetDocumentId) &&
    isEnumValue(value.type, DEPENDENCY_TYPES) &&
    typeof value.isBlocking === 'boolean'
  )
}

function isBlueprintReference(value: unknown): value is BlueprintReference {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, ['id', 'title', 'status', 'version', 'updatedAt']) &&
    isNonemptyString(value.id) &&
    isNonemptyString(value.title) &&
    isEnumValue(value.status, BLUEPRINT_STATUSES) &&
    isPositiveInteger(value.version) &&
    isNonemptyString(value.updatedAt)
  )
}

function isTeamReferences(value: unknown): value is TeamKnowledgeReferences {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, ['documents', 'dependencies', 'blueprints']) &&
    Array.isArray(value.documents) &&
    value.documents.every(isTeamDocumentReference) &&
    hasUniqueStrings(value.documents.map((document) => document.id)) &&
    Array.isArray(value.dependencies) &&
    value.dependencies.every(isDocumentDependencyReference) &&
    hasUniqueStrings(value.dependencies.map((dependency) => dependency.id)) &&
    Array.isArray(value.blueprints) &&
    value.blueprints.every(isBlueprintReference) &&
    hasUniqueStrings(value.blueprints.map((blueprint) => blueprint.id))
  )
}

export function isProductProject(value: unknown): value is ProductProject {
  if (
    !isRecord(value) ||
    !hasOnlyKeys(value, [
      'id',
      'name',
      'description',
      'teamId',
      'teamName',
      'createdAt',
      'updatedAt',
      'starred',
      'lifecycleStatus',
      'source',
      'workspace',
      'generationRuns',
      'attachments',
      'versions',
      'latestVersionId',
      'deployments',
      'deploymentSettings',
      'databaseSettings',
      'githubSettings',
      'teamReferences',
    ]) ||
    !isNonemptyString(value.id) ||
    !isNonemptyString(value.name) ||
    !isOptionalString(value, 'description') ||
    !isNonemptyString(value.teamId) ||
    !isNonemptyString(value.teamName) ||
    !isTimestamp(value.createdAt) ||
    !isTimestamp(value.updatedAt) ||
    typeof value.starred !== 'boolean' ||
    !isEnumValue(value.lifecycleStatus, PROJECT_LIFECYCLE_STATUSES) ||
    !isProjectSource(value.source) ||
    !isStrictVirtualWorkspace(value.workspace) ||
    !Array.isArray(value.generationRuns) ||
    !value.generationRuns.every(isGenerationRunSummary) ||
    !Array.isArray(value.attachments) ||
    !value.attachments.every(isAttachmentMetadata) ||
    !Array.isArray(value.versions) ||
    !value.versions.every(isProjectVersionMetadata) ||
    !isOptionalString(value, 'latestVersionId') ||
    !Array.isArray(value.deployments) ||
    !value.deployments.every(isDeploymentRecord) ||
    !isDeploymentSettings(value.deploymentSettings) ||
    !isDatabaseSettings(value.databaseSettings) ||
    !isGithubSettings(value.githubSettings) ||
    !isTeamReferences(value.teamReferences)
  ) {
    return false
  }

  const generationRunIds = value.generationRuns.map((run) => run.id)
  const attachmentIds = value.attachments.map((attachment) => attachment.id)
  const versionIds = value.versions.map((version) => version.id)
  const deploymentIds = value.deployments.map((deployment) => deployment.id)
  const checkpointIds = value.workspace.checkpoints.map((checkpoint) => checkpoint.id)
  const branchIds = value.workspace.branches.map((branch) => branch.id)
  const latestVersionId = hasOwn(value, 'latestVersionId')
    ? value.latestVersionId as string
    : undefined
  if (
    !hasUniqueStrings(generationRunIds) ||
    !hasUniqueStrings(attachmentIds) ||
    !hasUniqueStrings(versionIds) ||
    !hasUniqueStrings(deploymentIds) ||
    (latestVersionId !== undefined && !versionIds.includes(latestVersionId))
  ) {
    return false
  }

  return value.versions.every(
    (version) =>
      (!version.workspaceCheckpointId || checkpointIds.includes(version.workspaceCheckpointId)) &&
      (!version.branchId || branchIds.includes(version.branchId)) &&
      (!version.generationRunId || generationRunIds.includes(version.generationRunId)),
  )
}

export function isProjectCatalog(value: unknown): value is ProjectCatalog {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      'schema',
      'version',
      'createdAt',
      'updatedAt',
      'selectedProjectId',
      'projects',
    ]) &&
    value.schema === PROJECT_CATALOG_SCHEMA &&
    value.version === PROJECT_CATALOG_VERSION &&
    isTimestamp(value.createdAt) &&
    isTimestamp(value.updatedAt) &&
    isNonemptyString(value.selectedProjectId) &&
    Array.isArray(value.projects) &&
    value.projects.length > 0 &&
    value.projects.every(isProductProject) &&
    hasUniqueStrings(value.projects.map((project) => project.id)) &&
    value.projects.some((project) => project.id === value.selectedProjectId)
  )
}

function sanitizeProjectSource(value: ProjectSourceMetadata): ProjectSourceMetadata {
  if (value.kind === 'template') {
    return { kind: 'template', templateId: assertNonemptyName(value.templateId, 'Template id') }
  }
  if (value.kind === 'import') {
    return {
      kind: 'import',
      importProvider: assertNonemptyName(value.importProvider, 'Import provider'),
      ...(optionalTrimmed(value.importReference)
        ? { importReference: redactSensitiveText(value.importReference as string) }
        : {}),
    }
  }
  if (value.kind === 'clone') {
    return {
      kind: 'clone',
      clonedFromProjectId: assertNonemptyName(value.clonedFromProjectId, 'Source project id'),
    }
  }
  return { kind: 'blank' }
}

function sanitizeDeploymentSettings(value: unknown): DeploymentSettingsMetadata {
  const input = isRecord(value) ? value : {}
  const provider = enumOr(input.provider, DEPLOYMENT_PROVIDERS, 'none')
  const environmentVariableNames = uniqueStringValues(
    input.environmentVariableNames,
    input.secretNames,
    objectKeys(input.environmentVariables),
    objectKeys(input.secrets),
  )
  const productionUrl = sanitizeMetadataUrl(input.productionUrl)
  const lastDeploymentAt = isTimestamp(input.lastDeploymentAt) ? input.lastDeploymentAt : undefined
  return {
    provider,
    status: enumOr(
      input.status,
      DEPLOYMENT_CONNECTION_STATUSES,
      provider === 'none' ? 'notConfigured' : 'ready',
    ),
    ...(optionalTrimmed(input.projectRef) ? { projectRef: optionalTrimmed(input.projectRef) } : {}),
    ...(optionalTrimmed(input.siteName) ? { siteName: optionalTrimmed(input.siteName) } : {}),
    ...(optionalTrimmed(input.region) ? { region: optionalTrimmed(input.region) } : {}),
    ...(productionUrl ? { productionUrl } : {}),
    environmentVariableNames,
    ...(lastDeploymentAt ? { lastDeploymentAt } : {}),
  }
}

function sanitizeDatabaseSettings(value: unknown): DatabaseSettingsMetadata {
  const input = isRecord(value) ? value : {}
  const provider = enumOr(input.provider, DATABASE_PROVIDERS, 'none')
  return {
    provider,
    status: enumOr(
      input.status,
      DATABASE_CONNECTION_STATUSES,
      provider === 'none' ? 'notConfigured' : 'ready',
    ),
    ...(optionalTrimmed(input.projectRef) ? { projectRef: optionalTrimmed(input.projectRef) } : {}),
    ...(optionalTrimmed(input.databaseName)
      ? { databaseName: optionalTrimmed(input.databaseName) }
      : {}),
    ...(optionalTrimmed(input.region) ? { region: optionalTrimmed(input.region) } : {}),
    schemaNames: uniqueStringValues(input.schemaNames),
    tableNames: uniqueStringValues(input.tableNames),
    authEnabled: typeof input.authEnabled === 'boolean' ? input.authEnabled : false,
    storageBucketNames: uniqueStringValues(input.storageBucketNames),
    secretNames: uniqueStringValues(
      input.secretNames,
      objectKeys(input.secrets),
      objectKeys(input.environmentVariables),
    ),
  }
}

function sanitizeGithubSettings(value: unknown): GithubSettingsMetadata {
  const input = isRecord(value) ? value : {}
  const connectedAt = isTimestamp(input.connectedAt) ? input.connectedAt : undefined
  return {
    status: enumOr(input.status, GITHUB_CONNECTION_STATUSES, 'disconnected'),
    host: nonemptyOr(input.host, 'github.com'),
    ...(optionalTrimmed(input.owner) ? { owner: optionalTrimmed(input.owner) } : {}),
    ...(optionalTrimmed(input.repository) ? { repository: optionalTrimmed(input.repository) } : {}),
    ...(optionalTrimmed(input.defaultBranch)
      ? { defaultBranch: optionalTrimmed(input.defaultBranch) }
      : {}),
    ...(optionalTrimmed(input.installationRef)
      ? { installationRef: optionalTrimmed(input.installationRef) }
      : {}),
    ...(optionalTrimmed(input.lastCommitSha)
      ? { lastCommitSha: optionalTrimmed(input.lastCommitSha) }
      : {}),
    ...(connectedAt ? { connectedAt } : {}),
    permissionScopes: uniqueStringValues(input.permissionScopes),
  }
}

function cloneTeamReferences(value?: TeamKnowledgeReferences): TeamKnowledgeReferences {
  const references = value ?? { documents: [], dependencies: [], blueprints: [] }
  const cloned: TeamKnowledgeReferences = {
    documents: references.documents.map((document) => ({ ...document })),
    dependencies: references.dependencies.map((dependency) => ({ ...dependency })),
    blueprints: references.blueprints.map((blueprint) => ({ ...blueprint })),
  }
  if (!isTeamReferences(cloned)) throw new Error('Invalid team knowledge references')
  return cloned
}

function normalizeAttachment(
  input: AttachmentMetadataInput,
  runtime: ProjectCatalogRuntime,
  fallbackTime: string,
): AttachmentMetadata {
  const sourceUrl = sanitizeMetadataUrl(input.sourceUrl)
  const workspacePath = optionalTrimmed(input.workspacePath)
  if (workspacePath && !isSafeWorkspacePath(workspacePath)) {
    throw new Error(`Unsafe attachment workspace path "${workspacePath}"`)
  }
  return {
    id: input.id ?? resolveId(runtime, 'attachment'),
    name: redactSensitiveText(assertNonemptyName(input.name, 'Attachment name')),
    kind: enumOr(input.kind, ATTACHMENT_KINDS, 'other'),
    ...(optionalTrimmed(input.mimeType) ? { mimeType: optionalTrimmed(input.mimeType) } : {}),
    ...(input.sizeBytes === undefined ? {} : { sizeBytes: nonNegativeOr(input.sizeBytes) }),
    createdAt: resolveTime(runtime, input.createdAt ?? fallbackTime),
    ...(sourceUrl ? { sourceUrl } : {}),
    ...(workspacePath ? { workspacePath } : {}),
    ...(optionalTrimmed(input.checksum) ? { checksum: optionalTrimmed(input.checksum) } : {}),
  }
}

function copyGenerationEvent(event: GenerationEventSummary): GenerationEventSummary {
  return { ...event }
}

function copyGenerationRun(run: GenerationRunSummary): GenerationRunSummary {
  return { ...run, events: run.events.map(copyGenerationEvent) }
}

function copyAttachment(attachment: AttachmentMetadata): AttachmentMetadata {
  return { ...attachment }
}

function copyVersion(version: ProjectVersionMetadata): ProjectVersionMetadata {
  return { ...version }
}

function copyDeployment(deployment: DeploymentRecordSummary): DeploymentRecordSummary {
  return { ...deployment }
}

function cloneProjectSnapshot(project: ProductProject): ProductProject {
  const cloned: ProductProject = {
    id: project.id,
    name: project.name,
    ...(project.description === undefined ? {} : { description: project.description }),
    teamId: project.teamId,
    teamName: project.teamName,
    createdAt: project.createdAt,
    updatedAt: project.updatedAt,
    starred: project.starred,
    lifecycleStatus: project.lifecycleStatus,
    source: sanitizeProjectSource(project.source),
    workspace: cloneVirtualWorkspace(project.workspace),
    generationRuns: project.generationRuns.map(copyGenerationRun),
    attachments: project.attachments.map(copyAttachment),
    versions: project.versions.map(copyVersion),
    ...(project.latestVersionId === undefined
      ? {}
      : { latestVersionId: project.latestVersionId }),
    deployments: project.deployments.map(copyDeployment),
    deploymentSettings: sanitizeDeploymentSettings(project.deploymentSettings),
    databaseSettings: sanitizeDatabaseSettings(project.databaseSettings),
    githubSettings: sanitizeGithubSettings(project.githubSettings),
    teamReferences: cloneTeamReferences(project.teamReferences),
  }
  if (!isProductProject(cloned)) throw new Error(`Invalid project "${project.id}"`)
  return cloned
}

function freezeProject(project: ProductProject) {
  if (!isProductProject(project)) throw new Error('Invalid project')
  return deepFreeze(project)
}

function buildProject(
  input: ProjectCreationInput,
  workspace: VirtualWorkspace,
  source: ProjectSourceMetadata,
  runtime: ProjectCatalogRuntime,
): ProductProject {
  const createdAt = resolveTime(runtime, input.createdAt)
  const project: ProductProject = {
    id: input.id ?? resolveId(runtime, 'project'),
    name: nonemptyOr(input.name, 'Untitled project'),
    ...(optionalTrimmed(input.description)
      ? { description: optionalTrimmed(input.description) }
      : {}),
    teamId: nonemptyOr(input.teamId, 'personal'),
    teamName: nonemptyOr(input.teamName, 'Personal'),
    createdAt,
    updatedAt: createdAt,
    starred: input.starred ?? false,
    lifecycleStatus: enumOr(input.lifecycleStatus, PROJECT_LIFECYCLE_STATUSES, 'draft'),
    source: sanitizeProjectSource(source),
    workspace: cloneVirtualWorkspace(workspace),
    generationRuns: [],
    attachments: (input.attachments ?? []).map((attachment) =>
      normalizeAttachment(attachment, runtime, createdAt),
    ),
    versions: [],
    deployments: [],
    deploymentSettings: sanitizeDeploymentSettings(input.deploymentSettings),
    databaseSettings: sanitizeDatabaseSettings(input.databaseSettings),
    githubSettings: sanitizeGithubSettings(input.githubSettings),
    teamReferences: cloneTeamReferences(input.teamReferences),
  }
  return freezeProject(project)
}

export function createBlankProject(
  input: ProjectCreationInput = {},
  runtime: ProjectCatalogRuntime = {},
): ProductProject {
  const createdAt = resolveTime(runtime, input.createdAt)
  const projectId = input.id ?? resolveId(runtime, 'project')
  const projectName = nonemptyOr(input.name, 'Untitled project')
  const workspace = createWorkspace({
    id: input.workspaceId ?? resolveId(runtime, 'workspace'),
    name: projectName,
    files: input.files,
    createdAt,
  })
  return buildProject(
    { ...input, id: projectId, name: projectName, createdAt },
    workspace,
    { kind: 'blank' },
    runtime,
  )
}

export function createProjectFromTemplate(
  template: ProjectTemplate | ProductProject,
  input: ProjectCreationInput = {},
  runtime: ProjectCatalogRuntime = {},
): ProductProject {
  if (!isStrictVirtualWorkspace(template.workspace)) throw new Error('Template workspace is invalid')
  const createdAt = resolveTime(runtime, input.createdAt)
  const name = nonemptyOr(input.name, template.name)
  const workspace = createWorkspace({
    id: input.workspaceId ?? resolveId(runtime, 'workspace'),
    name,
    files: template.workspace.files.map(cloneWorkspaceFile),
    createdAt,
  })
  const templateAttachments = template.attachments?.map((attachment) => ({ ...attachment })) ?? []
  return buildProject(
    {
      ...input,
      name,
      description: input.description ?? template.description,
      createdAt,
      attachments: input.attachments ?? templateAttachments,
      deploymentSettings: input.deploymentSettings ?? template.deploymentSettings,
      databaseSettings: input.databaseSettings ?? template.databaseSettings,
      githubSettings: input.githubSettings ?? template.githubSettings,
      teamReferences: input.teamReferences ?? template.teamReferences,
    },
    workspace,
    { kind: 'template', templateId: template.id },
    runtime,
  )
}

export function createProjectFromImport(
  input: ImportedProjectInput,
  runtime: ProjectCatalogRuntime = {},
): ProductProject {
  if (!isStrictVirtualWorkspace(input.workspace)) throw new Error('Imported workspace is invalid')
  const createdAt = resolveTime(runtime, input.createdAt)
  const name = nonemptyOr(input.name, input.workspace.name)
  const workspace = cloneVirtualWorkspace(input.workspace, {
    id: input.workspaceId ?? resolveId(runtime, 'workspace'),
    name,
    createdAt,
    updatedAt: createdAt,
  })
  return buildProject(
    { ...input, name, createdAt },
    workspace,
    {
      kind: 'import',
      importProvider: input.importProvider,
      ...(optionalTrimmed(input.importReference)
        ? { importReference: redactSensitiveText(input.importReference as string) }
        : {}),
    },
    runtime,
  )
}

export function cloneProductProject(
  project: ProductProject,
  options: CloneProjectOptions = {},
  runtime: ProjectCatalogRuntime = {},
): ProductProject {
  if (!isProductProject(project)) throw new Error('Cannot clone an invalid project')
  const createdAt = resolveTime(runtime)
  const name = nonemptyOr(options.name, `${project.name} copy`)
  const cloned: ProductProject = {
    ...cloneProjectSnapshot(project),
    id: options.id ?? resolveId(runtime, 'project'),
    name,
    teamId: nonemptyOr(options.teamId, project.teamId),
    teamName: nonemptyOr(options.teamName, project.teamName),
    createdAt,
    updatedAt: createdAt,
    starred: false,
    source: { kind: 'clone', clonedFromProjectId: project.id },
    workspace: cloneVirtualWorkspace(project.workspace, {
      id: resolveId(runtime, 'workspace'),
      name,
      createdAt,
      updatedAt: createdAt,
    }),
  }
  return freezeProject(cloned)
}

function freezeCatalog(catalog: ProjectCatalog) {
  if (!isProjectCatalog(catalog)) throw new Error('Invalid project catalog')
  return deepFreeze(catalog)
}

export function createProjectCatalog(
  options: CreateProjectCatalogOptions = {},
  runtime: ProjectCatalogRuntime = {},
): ProjectCatalog {
  const suppliedProjects = options.projects ?? []
  const projects = suppliedProjects.length > 0
    ? suppliedProjects.map((project) => freezeProject(cloneProjectSnapshot(project)))
    : [createBlankProject(options.fallbackProject, runtime)]
  if (!hasUniqueStrings(projects.map((project) => project.id))) {
    throw new Error('Project ids must be unique within a catalog')
  }

  const selectedProjectId = options.selectedProjectId ?? projects[0].id
  if (!projects.some((project) => project.id === selectedProjectId)) {
    throw new Error(`Cannot select unknown project "${selectedProjectId}"`)
  }
  const createdAt = resolveTime(runtime, options.createdAt)
  return freezeCatalog({
    schema: PROJECT_CATALOG_SCHEMA,
    version: PROJECT_CATALOG_VERSION,
    createdAt,
    updatedAt: createdAt,
    selectedProjectId,
    projects,
  })
}

function projectIndex(catalog: ProjectCatalog, projectId: string) {
  const index = catalog.projects.findIndex((project) => project.id === projectId)
  if (index < 0) throw new Error(`Unknown project "${projectId}"`)
  return index
}

function updateCatalogProject(
  catalog: ProjectCatalog,
  projectId: string,
  runtime: ProjectCatalogRuntime,
  update: (project: ProductProject, updatedAt: string) => ProductProject,
) {
  const index = projectIndex(catalog, projectId)
  const updatedAt = resolveTime(runtime)
  const updatedProject = freezeProject(update(catalog.projects[index], updatedAt))
  const projects = catalog.projects.map((project, projectPosition) =>
    projectPosition === index ? updatedProject : project,
  )
  return freezeCatalog({ ...catalog, projects, updatedAt })
}

export function addProjectToCatalog(
  catalog: ProjectCatalog,
  project: ProductProject,
  options: { readonly select?: boolean } = {},
  runtime: ProjectCatalogRuntime = {},
) {
  if (!isProjectCatalog(catalog)) throw new Error('Cannot update an invalid project catalog')
  if (!isProductProject(project)) throw new Error('Cannot add an invalid project')
  if (catalog.projects.some((existing) => existing.id === project.id)) {
    throw new Error(`Duplicate project "${project.id}"`)
  }
  const updatedAt = resolveTime(runtime)
  const clonedProject = freezeProject(cloneProjectSnapshot(project))
  return freezeCatalog({
    ...catalog,
    projects: [...catalog.projects, clonedProject],
    selectedProjectId: options.select === false ? catalog.selectedProjectId : clonedProject.id,
    updatedAt,
  })
}

export function cloneProject(
  catalog: ProjectCatalog,
  projectId: string,
  options: CloneProjectOptions = {},
  runtime: ProjectCatalogRuntime = {},
) {
  const source = catalog.projects[projectIndex(catalog, projectId)]
  const cloned = cloneProductProject(source, options, runtime)
  return addProjectToCatalog(catalog, cloned, { select: options.select }, runtime)
}

export function renameProject(
  catalog: ProjectCatalog,
  projectId: string,
  name: string,
  runtime: ProjectCatalogRuntime = {},
) {
  const normalizedName = assertNonemptyName(name, 'Project name')
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => ({
    ...project,
    name: normalizedName,
    updatedAt,
    workspace: cloneVirtualWorkspace(project.workspace, { name: normalizedName, updatedAt }),
  }))
}

export function setProjectStarred(
  catalog: ProjectCatalog,
  projectId: string,
  starred: boolean,
  runtime: ProjectCatalogRuntime = {},
) {
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => ({
    ...project,
    starred,
    updatedAt,
  }))
}

export function toggleProjectStar(
  catalog: ProjectCatalog,
  projectId: string,
  runtime: ProjectCatalogRuntime = {},
) {
  const project = catalog.projects[projectIndex(catalog, projectId)]
  return setProjectStarred(catalog, projectId, !project.starred, runtime)
}

export function selectProject(
  catalog: ProjectCatalog,
  projectId: string,
  runtime: ProjectCatalogRuntime = {},
) {
  projectIndex(catalog, projectId)
  if (catalog.selectedProjectId === projectId) return catalog
  return freezeCatalog({ ...catalog, selectedProjectId: projectId, updatedAt: resolveTime(runtime) })
}

export function deleteProject(
  catalog: ProjectCatalog,
  projectId: string,
  options: DeleteProjectOptions = {},
  runtime: ProjectCatalogRuntime = {},
) {
  const deletedIndex = projectIndex(catalog, projectId)
  const remaining = catalog.projects.filter((project) => project.id !== projectId)
  const projects = remaining.length > 0
    ? remaining
    : [createBlankProject(options.fallbackProject, runtime)]
  const selectedProjectId = catalog.selectedProjectId === projectId
    ? projects[Math.min(deletedIndex, projects.length - 1)].id
    : catalog.selectedProjectId
  return freezeCatalog({
    ...catalog,
    projects,
    selectedProjectId,
    updatedAt: resolveTime(runtime),
  })
}

export function updateProjectWorkspace(
  catalog: ProjectCatalog,
  projectId: string,
  update: VirtualWorkspace | ((workspace: VirtualWorkspace) => VirtualWorkspace),
  runtime: ProjectCatalogRuntime = {},
) {
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => {
    const isolatedCurrentWorkspace = cloneVirtualWorkspace(project.workspace)
    const candidate = typeof update === 'function' ? update(isolatedCurrentWorkspace) : update
    const workspace = cloneVirtualWorkspace(candidate, { updatedAt })
    return { ...project, workspace, updatedAt }
  })
}

function normalizeGenerationEvent(
  input: GenerationEventInput,
  runtime: ProjectCatalogRuntime,
  fallbackTime: string,
): GenerationEventSummary {
  const path = optionalTrimmed(input.path)
  if (path && !isSafeWorkspacePath(path)) throw new Error(`Unsafe generation event path "${path}"`)
  return {
    id: input.id ?? resolveId(runtime, 'generation-event'),
    type: input.type,
    summary: assertNonemptyName(input.summary, 'Generation event summary'),
    createdAt: resolveTime(runtime, input.createdAt ?? fallbackTime),
    ...(path ? { path } : {}),
    ...(input.count === undefined ? {} : { count: nonNegativeOr(input.count) }),
    ...(input.durationMs === undefined
      ? {}
      : { durationMs: nonNegativeOr(input.durationMs) }),
  }
}

function normalizeGenerationRun(
  input: GenerationRunInput,
  runtime: ProjectCatalogRuntime,
): GenerationRunSummary {
  const startedAt = resolveTime(runtime, input.startedAt)
  const updatedAt = resolveTime(runtime, input.updatedAt ?? input.completedAt ?? startedAt)
  const events = (input.events ?? []).map((event) =>
    normalizeGenerationEvent(event, runtime, updatedAt),
  )
  if (!hasUniqueStrings(events.map((event) => event.id))) {
    throw new Error('Generation event ids must be unique within a run')
  }
  return {
    id: input.id ?? resolveId(runtime, 'generation-run'),
    prompt: redactSensitiveText(input.prompt),
    mode: enumOr(input.mode, GENERATION_MODES, 'build'),
    ...(optionalTrimmed(input.model) ? { model: optionalTrimmed(input.model) } : {}),
    ...(input.provider === 'openai' || input.provider === 'local'
      ? { provider: input.provider }
      : {}),
    status: input.status,
    startedAt,
    updatedAt,
    ...(input.completedAt ? { completedAt: resolveTime(runtime, input.completedAt) } : {}),
    eventCount: events.length,
    events,
    createdFileCount: nonNegativeOr(input.createdFileCount),
    updatedFileCount: nonNegativeOr(input.updatedFileCount),
    ...(input.inputTokens === undefined
      ? {}
      : { inputTokens: nonNegativeOr(input.inputTokens) }),
    ...(input.outputTokens === undefined
      ? {}
      : { outputTokens: nonNegativeOr(input.outputTokens) }),
    ...(input.totalTokens === undefined
      ? {}
      : { totalTokens: nonNegativeOr(input.totalTokens) }),
    ...(input.durationMs === undefined
      ? {}
      : { durationMs: nonNegativeOr(input.durationMs) }),
    ...(typeof input.costUsd === 'number' && Number.isFinite(input.costUsd) && input.costUsd >= 0
      ? { costUsd: input.costUsd }
      : {}),
    ...(input.maxTokens === undefined
      ? {}
      : { maxTokens: nonNegativeOr(input.maxTokens) }),
    ...(optionalTrimmed(input.errorMessage)
      ? { errorMessage: redactSensitiveText(input.errorMessage as string) }
      : {}),
  }
}

export function recordGeneration(
  catalog: ProjectCatalog,
  projectId: string,
  input: GenerationRunInput,
  runtime: ProjectCatalogRuntime = {},
) {
  const run = normalizeGenerationRun(input, runtime)
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => {
    const existingIndex = project.generationRuns.findIndex((existing) => existing.id === run.id)
    const generationRuns = existingIndex < 0
      ? [...project.generationRuns, run]
      : project.generationRuns.map((existing, index) => (index === existingIndex ? run : existing))
    return {
      ...project,
      generationRuns,
      lifecycleStatus: project.lifecycleStatus === 'draft' ? 'active' : project.lifecycleStatus,
      updatedAt,
    }
  })
}

export const recordGenerationRun = recordGeneration

function normalizeDeployment(
  input: DeploymentRecordInput,
  project: ProductProject,
  runtime: ProjectCatalogRuntime,
): DeploymentRecordSummary {
  const provider = enumOr(
    input.provider,
    DEPLOYMENT_PROVIDERS.filter(
      (candidate): candidate is Exclude<DeploymentProvider, 'none'> => candidate !== 'none',
    ),
    project.deploymentSettings.provider === 'none'
      ? 'custom'
      : project.deploymentSettings.provider,
  )
  const createdAt = resolveTime(runtime, input.createdAt as string | undefined)
  const url = sanitizeMetadataUrl(input.url)
  const completedAt = isTimestamp(input.completedAt) ? input.completedAt : undefined
  return {
    id: optionalTrimmed(input.id) ?? resolveId(runtime, 'deployment'),
    provider,
    status: enumOr(input.status, DEPLOYMENT_STATUSES, 'queued'),
    environment: enumOr(input.environment, DEPLOYMENT_ENVIRONMENTS, 'preview'),
    createdAt,
    ...(completedAt ? { completedAt } : {}),
    ...(url ? { url } : {}),
    ...(optionalTrimmed(input.commitSha) ? { commitSha: optionalTrimmed(input.commitSha) } : {}),
    ...(optionalTrimmed(input.summary)
      ? { summary: redactSensitiveText(input.summary as string) }
      : {}),
    ...(optionalTrimmed(input.errorMessage)
      ? { errorMessage: redactSensitiveText(input.errorMessage as string) }
      : {}),
  }
}

export function recordDeployment(
  catalog: ProjectCatalog,
  projectId: string,
  input: DeploymentRecordInput,
  runtime: ProjectCatalogRuntime = {},
) {
  const project = catalog.projects[projectIndex(catalog, projectId)]
  const deployment = normalizeDeployment(input, project, runtime)
  return updateCatalogProject(catalog, projectId, runtime, (current, updatedAt) => {
    const existingIndex = current.deployments.findIndex((existing) => existing.id === deployment.id)
    const deployments = existingIndex < 0
      ? [...current.deployments, deployment]
      : current.deployments.map((existing, index) =>
          index === existingIndex ? deployment : existing,
        )
    return {
      ...current,
      deployments,
      deploymentSettings: sanitizeDeploymentSettings({
        ...current.deploymentSettings,
        provider: deployment.provider,
        status: deployment.status === 'failed' ? 'error' : 'ready',
        productionUrl:
          deployment.environment === 'production'
            ? deployment.url ?? current.deploymentSettings.productionUrl
            : current.deploymentSettings.productionUrl,
        lastDeploymentAt: deployment.completedAt ?? deployment.createdAt,
      }),
      updatedAt,
    }
  })
}

export function recordProjectVersion(
  catalog: ProjectCatalog,
  projectId: string,
  input: ProjectVersionInput,
  runtime: ProjectCatalogRuntime = {},
) {
  const project = catalog.projects[projectIndex(catalog, projectId)]
  if (
    input.workspaceCheckpointId &&
    !project.workspace.checkpoints.some((checkpoint) => checkpoint.id === input.workspaceCheckpointId)
  ) {
    throw new Error(`Unknown workspace checkpoint "${input.workspaceCheckpointId}"`)
  }
  if (input.branchId && !project.workspace.branches.some((branch) => branch.id === input.branchId)) {
    throw new Error(`Unknown workspace branch "${input.branchId}"`)
  }
  if (
    input.generationRunId &&
    !project.generationRuns.some((run) => run.id === input.generationRunId)
  ) {
    throw new Error(`Unknown generation run "${input.generationRunId}"`)
  }
  const version: ProjectVersionMetadata = {
    id: input.id ?? resolveId(runtime, 'version'),
    label: assertNonemptyName(input.label, 'Version label'),
    ...(optionalTrimmed(input.description)
      ? { description: optionalTrimmed(input.description) }
      : {}),
    createdAt: resolveTime(runtime, input.createdAt),
    ...(input.workspaceCheckpointId
      ? { workspaceCheckpointId: input.workspaceCheckpointId }
      : {}),
    ...(input.branchId ? { branchId: input.branchId } : {}),
    ...(input.generationRunId ? { generationRunId: input.generationRunId } : {}),
    fileCount: input.fileCount ?? project.workspace.files.length,
  }
  return updateCatalogProject(catalog, projectId, runtime, (current, updatedAt) => {
    const existingIndex = current.versions.findIndex((existing) => existing.id === version.id)
    const versions = existingIndex < 0
      ? [...current.versions, version]
      : current.versions.map((existing, index) => (index === existingIndex ? version : existing))
    return { ...current, versions, latestVersionId: version.id, updatedAt }
  })
}

export function updateDeploymentSettings(
  catalog: ProjectCatalog,
  projectId: string,
  settings: unknown,
  runtime: ProjectCatalogRuntime = {},
) {
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => ({
    ...project,
    deploymentSettings: sanitizeDeploymentSettings(settings),
    updatedAt,
  }))
}

export function updateDatabaseSettings(
  catalog: ProjectCatalog,
  projectId: string,
  settings: unknown,
  runtime: ProjectCatalogRuntime = {},
) {
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => ({
    ...project,
    databaseSettings: sanitizeDatabaseSettings(settings),
    updatedAt,
  }))
}

export function updateGithubSettings(
  catalog: ProjectCatalog,
  projectId: string,
  settings: unknown,
  runtime: ProjectCatalogRuntime = {},
) {
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => ({
    ...project,
    githubSettings: sanitizeGithubSettings(settings),
    updatedAt,
  }))
}

export function updateTeamReferences(
  catalog: ProjectCatalog,
  projectId: string,
  references: TeamKnowledgeReferences,
  runtime: ProjectCatalogRuntime = {},
) {
  const clonedReferences = cloneTeamReferences(references)
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => ({
    ...project,
    teamReferences: clonedReferences,
    updatedAt,
  }))
}

export function addProjectAttachment(
  catalog: ProjectCatalog,
  projectId: string,
  input: AttachmentMetadataInput,
  runtime: ProjectCatalogRuntime = {},
) {
  const attachment = normalizeAttachment(input, runtime, resolveTime(runtime))
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => {
    if (project.attachments.some((existing) => existing.id === attachment.id)) {
      throw new Error(`Duplicate attachment "${attachment.id}"`)
    }
    return { ...project, attachments: [...project.attachments, attachment], updatedAt }
  })
}

export function removeProjectAttachment(
  catalog: ProjectCatalog,
  projectId: string,
  attachmentId: string,
  runtime: ProjectCatalogRuntime = {},
) {
  return updateCatalogProject(catalog, projectId, runtime, (project, updatedAt) => {
    if (!project.attachments.some((attachment) => attachment.id === attachmentId)) {
      throw new Error(`Unknown attachment "${attachmentId}"`)
    }
    return {
      ...project,
      attachments: project.attachments.filter((attachment) => attachment.id !== attachmentId),
      updatedAt,
    }
  })
}

export function summarizeProject(project: ProductProject): ProjectSummaryRecord {
  if (!isProductProject(project)) throw new Error('Cannot summarize an invalid project')
  const latestRun = project.generationRuns.at(-1)
  const latestVersion = project.latestVersionId
    ? project.versions.find((version) => version.id === project.latestVersionId)
    : project.versions.at(-1)
  const latestDeployment = project.deployments.at(-1)
  return {
    id: project.id,
    name: project.name,
    teamId: project.teamId,
    teamName: project.teamName,
    updatedAt: project.updatedAt,
    starred: project.starred,
    lifecycleStatus: project.lifecycleStatus,
    source: project.source.kind,
    fileCount: project.workspace.files.length,
    linkedDocumentCount: project.teamReferences.documents.length,
    generationRunCount: project.generationRuns.length,
    ...(latestRun ? { latestGenerationStatus: latestRun.status } : {}),
    ...(latestVersion ? { latestVersionLabel: latestVersion.label } : {}),
    ...(latestDeployment ? { latestDeploymentStatus: latestDeployment.status } : {}),
  }
}

export function listProjectSummaries(catalog: ProjectCatalog) {
  if (!isProjectCatalog(catalog)) throw new Error('Cannot summarize an invalid project catalog')
  return catalog.projects.map(summarizeProject)
}

export const projectSummaryRecords = listProjectSummaries

function legacyProject(
  value: unknown,
  runtime: ProjectCatalogRuntime,
): ProductProject | undefined {
  if (isProductProject(value)) return freezeProject(cloneProjectSnapshot(value))
  if (!isRecord(value)) return undefined

  const id = optionalTrimmed(value.id)
  const name = optionalTrimmed(value.name)
  if (!id || !name) return undefined
  const updatedAt = isTimestamp(value.updatedAt) ? value.updatedAt : resolveTime(runtime)
  const workspace = isStrictVirtualWorkspace(value.workspace)
    ? cloneVirtualWorkspace(value.workspace)
    : undefined
  if (workspace) {
    return buildProject(
      {
        id,
        name,
        teamId: optionalTrimmed(value.teamId),
        teamName: optionalTrimmed(value.teamName),
        starred: typeof value.starred === 'boolean' ? value.starred : false,
        lifecycleStatus: enumOr(value.lifecycleStatus, PROJECT_LIFECYCLE_STATUSES, 'active'),
        createdAt: isTimestamp(value.createdAt) ? value.createdAt : updatedAt,
        deploymentSettings: value.deploymentSettings,
        databaseSettings: value.databaseSettings,
        githubSettings: value.githubSettings,
      },
      workspace,
      { kind: 'import', importProvider: 'catalog-migration', importReference: id },
      runtime,
    )
  }
  return createBlankProject(
    {
      id,
      name,
      teamId: optionalTrimmed(value.teamId),
      teamName: optionalTrimmed(value.teamName),
      starred: typeof value.starred === 'boolean' ? value.starred : false,
      lifecycleStatus: 'active',
      createdAt: isTimestamp(value.createdAt) ? value.createdAt : updatedAt,
    },
    runtime,
  )
}

export function migrateProjectCatalog(
  value: unknown,
  context: ProjectCatalogMigrationContext = {
    fromVersion: 0,
    toVersion: PROJECT_CATALOG_VERSION,
  },
  runtime: ProjectCatalogRuntime = {},
): ProjectCatalog {
  if (context.toVersion !== PROJECT_CATALOG_VERSION) {
    throw new Error(`Cannot migrate a project catalog to version ${context.toVersion}`)
  }
  if (context.fromVersion > PROJECT_CATALOG_VERSION || context.fromVersion < 0) {
    throw new Error(`Unsupported project catalog version ${context.fromVersion}`)
  }
  if (isProjectCatalog(value)) return freezeCatalog({
    ...value,
    projects: value.projects.map((project) => freezeProject(cloneProjectSnapshot(project))),
  })
  if (context.fromVersion === PROJECT_CATALOG_VERSION) {
    throw new Error('Current project catalog data is invalid and cannot be migrated')
  }

  const container = isRecord(value) ? value : undefined
  const sourceProjects = Array.isArray(value)
    ? value
    : container && Array.isArray(container.projects)
      ? container.projects
      : []
  const migratedProjects = sourceProjects
    .map((project) => legacyProject(project, runtime))
    .filter((project): project is ProductProject => project !== undefined)
  const projects = migratedProjects.length > 0
    ? migratedProjects
    : [createBlankProject({}, runtime)]
  const requestedSelection = container ? optionalTrimmed(container.selectedProjectId) : undefined
  const selectedProjectId = requestedSelection && projects.some((project) => project.id === requestedSelection)
    ? requestedSelection
    : projects[0].id
  const createdAt = container && isTimestamp(container.createdAt)
    ? container.createdAt
    : projects[0].createdAt
  const updatedAt = container && isTimestamp(container.updatedAt)
    ? container.updatedAt
    : resolveTime(runtime)

  return freezeCatalog({
    schema: PROJECT_CATALOG_SCHEMA,
    version: PROJECT_CATALOG_VERSION,
    createdAt,
    updatedAt,
    selectedProjectId,
    projects,
  })
}

export function createProjectCatalogMigration(runtime: ProjectCatalogRuntime = {}) {
  return (value: unknown, context: ProjectCatalogMigrationContext) =>
    migrateProjectCatalog(value, context, runtime)
}
