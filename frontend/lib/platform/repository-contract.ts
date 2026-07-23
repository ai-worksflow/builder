import {
  normalizeCandidateWorkspace,
  type CandidateWorkspaceDto,
  type ExactRepositoryRefDto,
  type ExactWorkspaceRevisionRefDto,
  type RepositoryTreeFileDto,
} from './sandbox-contract'
import { sha256Bytes, sha256DigestString } from './sha256'

export const REPOSITORY_SNAPSHOT_RECEIPT_SCHEMA_VERSION = 'repository-snapshot-receipt/v1'
export const REPOSITORY_SNAPSHOT_RECEIPT_SUBJECT_SCHEMA_VERSION = 'repository-snapshot-receipt-subject/v1'
export const REPOSITORY_SNAPSHOT_TREE_COMMITMENT_SCHEMA_VERSION = 'repository-snapshot-tree-commitment/v1'

export interface RepositorySnapshotTreeCommitmentDto {
  readonly schemaVersion: typeof REPOSITORY_SNAPSHOT_TREE_COMMITMENT_SCHEMA_VERSION
  readonly treeHash: string
  readonly contentObjectHash: string
  readonly fileCount: number
  readonly byteSize: number
}

export interface RepositorySnapshotTemplateReleaseDto {
  readonly role: string
  readonly mountPath: string
  readonly release: {
    readonly id: string
    readonly contentHash: string
    readonly subjectHash: string
  }
  readonly source: {
    readonly repository: string
    readonly branch: string
    readonly commit: string
    readonly treeHash: string
  }
  readonly sbomDigest: string
  readonly signatureBundleDigest: string
  readonly authorityReceipt: {
    readonly id: string
    readonly contentHash: string
    readonly policyHash: string
  }
}

export interface RepositorySnapshotReceiptSubjectDto {
  readonly schemaVersion: typeof REPOSITORY_SNAPSHOT_RECEIPT_SUBJECT_SCHEMA_VERSION
  readonly id: string
  readonly projectId: string
  readonly buildManifest: ExactRepositoryRefDto
  readonly buildContract: ExactRepositoryRefDto
  readonly fullStackTemplate: ExactRepositoryRefDto
  readonly baseWorkspaceRevision?: ExactWorkspaceRevisionRefDto
  readonly tree: RepositorySnapshotTreeCommitmentDto
  readonly templateReleases: readonly RepositorySnapshotTemplateReleaseDto[]
  readonly createdBy: string
  readonly createdAt: string
}

export type RepositorySnapshotDto = RepositorySnapshotReceiptSubjectDto

export interface RepositorySnapshotReceiptDto {
  readonly schemaVersion: typeof REPOSITORY_SNAPSHOT_RECEIPT_SCHEMA_VERSION
  readonly contentHash: string
  readonly snapshot: RepositorySnapshotReceiptSubjectDto
}

export interface RepositoryCandidateBootstrapDto {
  readonly candidate: CandidateWorkspaceDto
  readonly repositorySnapshotReceipt: RepositorySnapshotReceiptDto
  readonly created: boolean
  readonly recovered: boolean
  readonly finalizationPending: boolean
}

export interface RepositoryCandidateHeadDto {
  readonly candidate: CandidateWorkspaceDto
  readonly rebaseId?: string
}

export interface RepositoryCandidateHeadListDto {
  readonly schemaVersion: string
  readonly candidates: readonly RepositoryCandidateHeadDto[]
}

export type CandidateRebaseState = 'applying' | 'conflicted' | 'ready'
export type CandidateRebaseConflictState = 'open' | 'resolved'
export type CandidateRebaseResolutionStrategy = 'predecessor' | 'target' | 'current'

export interface CandidateRebaseOperationDto {
  readonly ordinal: number
  readonly operation: {
    readonly id: string
    readonly kind: 'file.upsert' | 'file.delete'
    readonly path: string
    readonly expectedHash?: string
    readonly contentHash?: string
    readonly byteSize?: number
    readonly mode?: string
  }
}

export interface CandidateRebaseConflictDto {
  readonly schemaVersion: string
  readonly id: string
  readonly ordinal: number
  readonly path: string
  readonly ancestorFile?: RepositoryTreeFileDto
  readonly predecessorFile?: RepositoryTreeFileDto
  readonly targetFile?: RepositoryTreeFileDto
  readonly state: CandidateRebaseConflictState
  readonly version: number
  readonly resolutionStrategy?: CandidateRebaseResolutionStrategy
  readonly resolutionFile?: RepositoryTreeFileDto
  readonly resolutionDeleted?: boolean
  readonly resolvedBy?: string
  readonly resolvedAt?: string
  readonly createdAt: string
}

export interface CandidateRebaseDto {
  readonly schemaVersion: string
  readonly id: string
  readonly projectId: string
  readonly operationId: string
  readonly predecessorCandidateId: string
  readonly successorCandidateId: string
  readonly targetBuildManifestId: string
  readonly ancestorTreeHash: string
  readonly predecessorTreeHash: string
  readonly targetTreeHash: string
  readonly plannedTreeHash: string
  readonly planHash: string
  readonly state: CandidateRebaseState
  readonly version: number
  readonly operations: readonly CandidateRebaseOperationDto[]
  readonly conflicts: readonly CandidateRebaseConflictDto[]
  readonly createdBy: string
  readonly createdAt: string
  readonly updatedAt: string
}

export interface CandidateRebaseResultDto {
  readonly rebase: CandidateRebaseDto
  readonly candidate: CandidateWorkspaceDto
  readonly created: boolean
  readonly recovered: boolean
  readonly finalizationPending: boolean
}

export interface CandidateRebaseFileContentDto {
  readonly contentHash: string
  readonly byteSize: number
  readonly encoding: 'base64'
  readonly data: string
}

export interface CandidateRebaseConflictContentDto {
  readonly schemaVersion: string
  readonly rebaseId: string
  readonly conflictId: string
  readonly path: string
  readonly ancestor?: CandidateRebaseFileContentDto
  readonly predecessor?: CandidateRebaseFileContentDto
  readonly target?: CandidateRebaseFileContentDto
}

export const REPOSITORY_CANDIDATE_SEARCH_SCHEMA_VERSION = 'repository-candidate-search/v1'

export interface RepositoryCandidateSearchInputDto {
  readonly expectedHeadGeneration: number
  readonly expectedRootHash: string
  readonly query: string
  readonly caseSensitive: boolean
  readonly includeGlobs?: readonly string[]
  readonly maxMatches?: number
}

export interface RepositoryCandidateSearchHeadDto {
  readonly candidateId: string
  readonly generation: number
  readonly rootHash: string
}

export interface RepositoryCandidateSearchLimitsDto {
  readonly maxQueryBytes: number
  readonly maxIncludeGlobs: number
  readonly maxGlobBytes: number
  readonly maxFiles: number
  readonly maxBytes: number
  readonly maxMatches: number
  readonly maxPreviewBytes: number
}

export interface RepositoryCandidateSearchStatsDto {
  readonly filesScanned: number
  readonly bytesScanned: number
  readonly binaryFilesSkipped: number
}

export interface RepositoryCandidateSearchMatchDto {
  readonly path: string
  readonly line: number
  readonly column: number
  readonly preview: string
  readonly previewTruncated: boolean
  readonly contentHash: string
}

export interface RepositoryCandidateSearchResultDto {
  readonly schemaVersion: typeof REPOSITORY_CANDIDATE_SEARCH_SCHEMA_VERSION
  readonly projectId: string
  readonly head: RepositoryCandidateSearchHeadDto
  readonly query: string
  readonly caseSensitive: boolean
  readonly includeGlobs: readonly string[]
  readonly truncated: boolean
  readonly limits: RepositoryCandidateSearchLimitsDto
  readonly stats: RepositoryCandidateSearchStatsDto
  readonly matches: readonly RepositoryCandidateSearchMatchDto[]
}

export class RepositoryContractError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'RepositoryContractError'
  }
}

function record(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null
    ? value as Record<string, unknown>
    : {}
}

function text(value: unknown) {
  return typeof value === 'string' ? value : ''
}

function number(value: unknown) {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0
}

function list(value: unknown) {
  return Array.isArray(value) ? value : []
}

function treeFile(value: unknown): RepositoryTreeFileDto | undefined {
  if (typeof value !== 'object' || value === null) return undefined
  const source = record(value)
  if (!text(source.path)) return undefined
  return {
    path: text(source.path), mode: text(source.mode) || '100644',
    contentHash: text(source.contentHash), byteSize: number(source.byteSize),
  }
}

function operation(value: unknown): CandidateRebaseOperationDto {
  const source = record(value)
  const item = record(source.operation)
  return {
    ordinal: number(source.ordinal),
    operation: {
      id: text(item.id), kind: text(item.kind) as 'file.upsert' | 'file.delete', path: text(item.path),
      expectedHash: text(item.expectedHash) || undefined,
      contentHash: text(item.contentHash) || undefined,
      byteSize: item.byteSize === undefined ? undefined : number(item.byteSize),
      mode: text(item.mode) || undefined,
    },
  }
}

function conflict(value: unknown): CandidateRebaseConflictDto {
  const source = record(value)
  return {
    schemaVersion: text(source.schemaVersion), id: text(source.id),
    ordinal: number(source.ordinal), path: text(source.path),
    ancestorFile: treeFile(source.ancestorFile), predecessorFile: treeFile(source.predecessorFile),
    targetFile: treeFile(source.targetFile),
    state: text(source.state) as CandidateRebaseConflictState, version: number(source.version),
    resolutionStrategy: (text(source.resolutionStrategy) || undefined) as CandidateRebaseResolutionStrategy | undefined,
    resolutionFile: treeFile(source.resolutionFile),
    resolutionDeleted: typeof source.resolutionDeleted === 'boolean' ? source.resolutionDeleted : undefined,
    resolvedBy: text(source.resolvedBy) || undefined, resolvedAt: text(source.resolvedAt) || undefined,
    createdAt: text(source.createdAt),
  }
}

function fileContent(value: unknown): CandidateRebaseFileContentDto | undefined {
  if (typeof value !== 'object' || value === null) return undefined
  const source = record(value)
  return {
    contentHash: text(source.contentHash), byteSize: number(source.byteSize),
    encoding: 'base64', data: text(source.data),
  }
}

const repositorySnapshotUUIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const repositorySnapshotHashPattern = /^sha256:[0-9a-f]{64}$/
const repositorySnapshotLineageHashPattern = /^(?:sha256:)?[0-9a-f]{64}$/
const repositorySnapshotTimestampPattern = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/
const repositorySnapshotGitCommitPattern = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/
const repositorySnapshotRolePattern = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/
const repositorySnapshotRoles = new Set(['api', 'web', 'worker'])
const repositorySnapshotEncoder = new TextEncoder()

function malformedRepositorySnapshot(detail: string): never {
  throw new RepositoryContractError(`The repository service returned a malformed RepositorySnapshot receipt: ${detail}.`)
}

function exactRepositorySnapshotRecord(
  value: unknown,
  required: readonly string[],
  optional: readonly string[] = [],
  detail = 'object',
): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return malformedRepositorySnapshot(`${detail} is not an object`)
  }
  const source = value as Record<string, unknown>
  const allowed = new Set([...required, ...optional])
  const keys = Object.keys(source)
  if (required.some((key) => !Object.hasOwn(source, key)) || keys.some((key) => !allowed.has(key))) {
    return malformedRepositorySnapshot(`${detail} has missing or additional fields`)
  }
  return source
}

function snapshotUUID(value: unknown, detail: string): string {
  if (typeof value !== 'string' || !repositorySnapshotUUIDPattern.test(value)) {
    return malformedRepositorySnapshot(`${detail} is not a canonical UUID`)
  }
  return value
}

function snapshotHash(value: unknown, detail: string, external = false): string {
  const pattern = external ? repositorySnapshotLineageHashPattern : repositorySnapshotHashPattern
  if (typeof value !== 'string' || !pattern.test(value)) {
    return malformedRepositorySnapshot(`${detail} is not a canonical SHA-256 hash`)
  }
  return value
}

function snapshotTimestamp(value: unknown, detail: string): string {
  if (typeof value !== 'string') {
    return malformedRepositorySnapshot(`${detail} is not a canonical UTC timestamp`)
  }
  const match = repositorySnapshotTimestampPattern.exec(value)
  if (!match || (match[7]?.endsWith('0') ?? false)) {
    return malformedRepositorySnapshot(`${detail} is not a canonical UTC timestamp`)
  }
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number)
  const calendar = new Date(0)
  calendar.setUTCFullYear(year!, month! - 1, day!)
  calendar.setUTCHours(hour!, minute!, second!, 0)
  if (
    calendar.getUTCFullYear() !== year
    || calendar.getUTCMonth() !== month! - 1
    || calendar.getUTCDate() !== day
    || calendar.getUTCHours() !== hour
    || calendar.getUTCMinutes() !== minute
    || calendar.getUTCSeconds() !== second
  ) {
    return malformedRepositorySnapshot(`${detail} is not a canonical UTC timestamp`)
  }
  return value
}

function snapshotText(value: unknown, detail: string, maximumBytes: number): string {
  if (
    typeof value !== 'string'
    || value.length === 0
    || value !== value.trim()
    || repositorySnapshotEncoder.encode(value).byteLength > maximumBytes
    || [...value].some((character) => {
      const code = character.codePointAt(0) ?? 0
      return code === 0 || code < 0x20 || (code >= 0x7f && code <= 0x9f)
    })
  ) {
    return malformedRepositorySnapshot(`${detail} is not canonical text`)
  }
  return value
}

function snapshotRepositoryURL(value: unknown, detail: string): string {
  const raw = snapshotText(value, detail, 2_048)
  try {
    const parsed = new URL(raw)
    if (
      !raw.startsWith('https://')
      || parsed.protocol !== 'https:'
      || parsed.username !== ''
      || parsed.password !== ''
      || parsed.search !== ''
      || parsed.hash !== ''
      || parsed.port !== ''
      || !parsed.pathname.endsWith('.git')
    ) {
      return malformedRepositorySnapshot(`${detail} is not a canonical HTTPS Git URL`)
    }
  } catch {
    return malformedRepositorySnapshot(`${detail} is not a canonical HTTPS Git URL`)
  }
  return raw
}

function parseSnapshotExactRef(
  value: unknown,
  detail: string,
  external = true,
): ExactRepositoryRefDto {
  const source = exactRepositorySnapshotRecord(value, ['id', 'contentHash'], [], detail)
  return {
    id: snapshotUUID(source.id, `${detail}.id`),
    contentHash: snapshotHash(source.contentHash, `${detail}.contentHash`, external),
  }
}

function repositorySnapshotBytes(value: string) {
  return repositorySnapshotEncoder.encode(value)
}

function compareRepositorySnapshotUTF8(left: string, right: string) {
  const leftBytes = repositorySnapshotBytes(left)
  const rightBytes = repositorySnapshotBytes(right)
  const length = Math.min(leftBytes.length, rightBytes.length)
  for (let index = 0; index < length; index += 1) {
    const difference = leftBytes[index]! - rightBytes[index]!
    if (difference !== 0) return difference
  }
  return leftBytes.length - rightBytes.length
}

function validRepositorySnapshotPath(value: unknown): value is string {
  if (
    typeof value !== 'string'
    || value.length === 0
    || value !== value.trim()
    || repositorySnapshotBytes(value).byteLength > 512
    || value.startsWith('/')
    || value.includes('\\')
    || [...value].some((character) => {
      const code = character.codePointAt(0) ?? 0
      return code === 0 || code < 0x20 || (code >= 0x7f && code <= 0x9f)
    })
  ) {
    return false
  }
  return !value.split('/').some((part) => {
    const lower = part.toLowerCase()
    return !part || part === '.' || part === '..'
      || lower === '.git' || lower === '.env' || lower.startsWith('.env.')
      || lower === 'node_modules' || lower === '.next' || lower === 'dist'
      || lower === 'build' || lower === '__pycache__'
  })
}

function goRepositorySnapshotJSONString(value: string) {
  return JSON.stringify(value).replace(/[<>&\u2028\u2029]/gu, (character) => {
    const code = character.codePointAt(0)!
    return `\\u${code.toString(16).padStart(4, '0')}`
  })
}

function canonicalRepositorySnapshotJSON(value: unknown): string {
  if (value === null) return 'null'
  if (typeof value === 'string') return goRepositorySnapshotJSONString(value)
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) return malformedRepositorySnapshot('canonical content contains a non-integer number')
    return String(value)
  }
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (Array.isArray(value)) return `[${value.map(canonicalRepositorySnapshotJSON).join(',')}]`
  if (typeof value === 'object' && value !== null) {
    const source = value as Record<string, unknown>
    const keys = Object.keys(source).sort(compareRepositorySnapshotUTF8)
    return `{${keys.map((key) => `${goRepositorySnapshotJSONString(key)}:${canonicalRepositorySnapshotJSON(source[key])}`).join(',')}}`
  }
  return malformedRepositorySnapshot('canonical content contains an unsupported value')
}

async function hashCanonicalRepositorySnapshot(value: unknown) {
  try {
    const digest = await sha256Bytes(repositorySnapshotEncoder.encode(canonicalRepositorySnapshotJSON(value)))
    return sha256DigestString(digest)
  } catch {
    return malformedRepositorySnapshot('SHA-256 verification failed')
  }
}

export function computeRepositorySnapshotContentHash(snapshot: RepositorySnapshotDto) {
  return hashCanonicalRepositorySnapshot(snapshot)
}

function parseRepositorySnapshotTree(value: unknown): RepositorySnapshotTreeCommitmentDto {
  const source = exactRepositorySnapshotRecord(
    value,
    ['schemaVersion', 'treeHash', 'contentObjectHash', 'fileCount', 'byteSize'],
    [],
    'snapshot.tree',
  )
  if (source.schemaVersion !== REPOSITORY_SNAPSHOT_TREE_COMMITMENT_SCHEMA_VERSION) {
    return malformedRepositorySnapshot('snapshot.tree.schemaVersion is unsupported')
  }
  if (
    typeof source.fileCount !== 'number'
    || !Number.isSafeInteger(source.fileCount)
    || source.fileCount < 0
    || source.fileCount > 20_000
    || typeof source.byteSize !== 'number'
    || !Number.isSafeInteger(source.byteSize)
    || source.byteSize < 0
    || source.byteSize > 64 * 1024 * 1024
  ) {
    return malformedRepositorySnapshot('snapshot.tree counters are invalid')
  }
  return {
    schemaVersion: REPOSITORY_SNAPSHOT_TREE_COMMITMENT_SCHEMA_VERSION,
    treeHash: snapshotHash(source.treeHash, 'snapshot.tree.treeHash'),
    contentObjectHash: snapshotHash(source.contentObjectHash, 'snapshot.tree.contentObjectHash'),
    fileCount: source.fileCount,
    byteSize: source.byteSize,
  }
}

function parseRepositorySnapshotTemplateRelease(
  value: unknown,
  index: number,
): RepositorySnapshotTemplateReleaseDto {
  const detail = `snapshot.templateReleases[${index}]`
  const source = exactRepositorySnapshotRecord(
    value,
    [
      'role', 'mountPath', 'release', 'source', 'sbomDigest',
      'signatureBundleDigest', 'authorityReceipt',
    ],
    [],
    detail,
  )
  const role = snapshotText(source.role, `${detail}.role`, 64)
  if (!repositorySnapshotRolePattern.test(role) || !repositorySnapshotRoles.has(role)) {
    return malformedRepositorySnapshot(`${detail}.role is not canonical`)
  }
  if (!validRepositorySnapshotPath(source.mountPath)) {
    return malformedRepositorySnapshot(`${detail}.mountPath is not a canonical repository path`)
  }
  const release = exactRepositorySnapshotRecord(
    source.release,
    ['id', 'contentHash', 'subjectHash'],
    [],
    `${detail}.release`,
  )
  const releaseSource = exactRepositorySnapshotRecord(
    source.source,
    ['repository', 'branch', 'commit', 'treeHash'],
    [],
    `${detail}.source`,
  )
  const commit = snapshotText(releaseSource.commit, `${detail}.source.commit`, 64)
  if (!repositorySnapshotGitCommitPattern.test(commit)) {
    return malformedRepositorySnapshot(`${detail}.source.commit is not a pinned Git object ID`)
  }
  const authorityReceipt = exactRepositorySnapshotRecord(
    source.authorityReceipt,
    ['id', 'contentHash', 'policyHash'],
    [],
    `${detail}.authorityReceipt`,
  )
  return {
    role,
    mountPath: source.mountPath,
    release: {
      id: snapshotUUID(release.id, `${detail}.release.id`),
      contentHash: snapshotHash(release.contentHash, `${detail}.release.contentHash`),
      subjectHash: snapshotHash(release.subjectHash, `${detail}.release.subjectHash`),
    },
    source: {
      repository: snapshotRepositoryURL(releaseSource.repository, `${detail}.source.repository`),
      branch: snapshotText(releaseSource.branch, `${detail}.source.branch`, 255),
      commit,
      treeHash: snapshotHash(releaseSource.treeHash, `${detail}.source.treeHash`),
    },
    sbomDigest: snapshotHash(source.sbomDigest, `${detail}.sbomDigest`),
    signatureBundleDigest: snapshotHash(
      source.signatureBundleDigest,
      `${detail}.signatureBundleDigest`,
    ),
    authorityReceipt: {
      id: snapshotUUID(authorityReceipt.id, `${detail}.authorityReceipt.id`),
      contentHash: snapshotHash(
        authorityReceipt.contentHash,
        `${detail}.authorityReceipt.contentHash`,
      ),
      policyHash: snapshotHash(authorityReceipt.policyHash, `${detail}.authorityReceipt.policyHash`),
    },
  }
}

export async function parseRepositorySnapshotReceipt(
  value: unknown,
): Promise<RepositorySnapshotReceiptDto> {
  const source = exactRepositorySnapshotRecord(
    value, ['schemaVersion', 'contentHash', 'snapshot'], [], 'receipt',
  )
  if (source.schemaVersion !== REPOSITORY_SNAPSHOT_RECEIPT_SCHEMA_VERSION) {
    return malformedRepositorySnapshot('receipt.schemaVersion is unsupported')
  }
  const rawSnapshot = exactRepositorySnapshotRecord(
    source.snapshot,
    [
      'schemaVersion', 'id', 'projectId', 'buildManifest', 'buildContract',
      'fullStackTemplate', 'tree', 'templateReleases', 'createdBy', 'createdAt',
    ],
    ['baseWorkspaceRevision'],
    'snapshot',
  )
  if (rawSnapshot.schemaVersion !== REPOSITORY_SNAPSHOT_RECEIPT_SUBJECT_SCHEMA_VERSION) {
    return malformedRepositorySnapshot('snapshot.schemaVersion is unsupported')
  }
  const base = rawSnapshot.baseWorkspaceRevision === undefined
    ? undefined
    : exactRepositorySnapshotRecord(
        rawSnapshot.baseWorkspaceRevision,
        ['artifactId', 'revisionId', 'contentHash'],
        [],
        'snapshot.baseWorkspaceRevision',
      )
  if (!Array.isArray(rawSnapshot.templateReleases)) {
    return malformedRepositorySnapshot('snapshot.templateReleases is not an array')
  }
  if (rawSnapshot.templateReleases.length < 2 || rawSnapshot.templateReleases.length > 8) {
    return malformedRepositorySnapshot('snapshot.templateReleases has an invalid size')
  }
  const templateReleases = rawSnapshot.templateReleases.map(parseRepositorySnapshotTemplateRelease)
  for (let index = 1; index < templateReleases.length; index += 1) {
    if (compareRepositorySnapshotUTF8(templateReleases[index - 1]!.role, templateReleases[index]!.role) >= 0) {
      return malformedRepositorySnapshot('snapshot.templateReleases roles are duplicated or not canonically sorted')
    }
  }
  if (!templateReleases.some((release) => release.role === 'api')
    || !templateReleases.some((release) => release.role === 'web')) {
    return malformedRepositorySnapshot('snapshot.templateReleases must contain exact api and web roles')
  }
  const snapshot: RepositorySnapshotDto = {
    schemaVersion: REPOSITORY_SNAPSHOT_RECEIPT_SUBJECT_SCHEMA_VERSION,
    id: snapshotUUID(rawSnapshot.id, 'snapshot.id'),
    projectId: snapshotUUID(rawSnapshot.projectId, 'snapshot.projectId'),
    buildManifest: parseSnapshotExactRef(rawSnapshot.buildManifest, 'snapshot.buildManifest'),
    buildContract: parseSnapshotExactRef(rawSnapshot.buildContract, 'snapshot.buildContract'),
    fullStackTemplate: parseSnapshotExactRef(rawSnapshot.fullStackTemplate, 'snapshot.fullStackTemplate'),
    ...(base ? { baseWorkspaceRevision: {
      artifactId: snapshotUUID(base.artifactId, 'snapshot.baseWorkspaceRevision.artifactId'),
      revisionId: snapshotUUID(base.revisionId, 'snapshot.baseWorkspaceRevision.revisionId'),
      contentHash: snapshotHash(base.contentHash, 'snapshot.baseWorkspaceRevision.contentHash'),
    } } : {}),
    tree: parseRepositorySnapshotTree(rawSnapshot.tree),
    templateReleases,
    createdBy: snapshotUUID(rawSnapshot.createdBy, 'snapshot.createdBy'),
    createdAt: snapshotTimestamp(rawSnapshot.createdAt, 'snapshot.createdAt'),
  }
  const contentHash = snapshotHash(source.contentHash, 'receipt.contentHash')
  if (await computeRepositorySnapshotContentHash(snapshot) !== contentHash) {
    return malformedRepositorySnapshot('receipt.contentHash does not match its snapshot')
  }
  return {
    schemaVersion: REPOSITORY_SNAPSHOT_RECEIPT_SCHEMA_VERSION,
    contentHash,
    snapshot,
  }
}

function sameSnapshotRef(left: ExactRepositoryRefDto, right: ExactRepositoryRefDto) {
  return left.id === right.id && left.contentHash === right.contentHash
}

function repositorySnapshotMatchesBootstrapCandidate(
  receipt: RepositorySnapshotReceiptDto,
  candidate: CandidateWorkspaceDto,
) {
  const snapshot = receipt.snapshot
  const snapshotBase = snapshot.baseWorkspaceRevision
  const candidateBase = candidate.baseWorkspaceRevision
  return candidate.schemaVersion === 'candidate-workspace/v1'
    && candidate.repositorySnapshotId === snapshot.id
    && candidate.projectId === snapshot.projectId
    && candidate.baseTreeHash === snapshot.tree.treeHash
    && sameSnapshotRef(candidate.buildManifest, snapshot.buildManifest)
    && sameSnapshotRef(candidate.buildContract, snapshot.buildContract)
    && sameSnapshotRef(candidate.fullStackTemplate, snapshot.fullStackTemplate)
    && candidate.createdBy === snapshot.createdBy
    && candidate.createdAt === snapshot.createdAt
    && ((!snapshotBase && !candidateBase) || Boolean(
      snapshotBase && candidateBase
      && snapshotBase.artifactId === candidateBase.artifactId
      && snapshotBase.revisionId === candidateBase.revisionId
      && snapshotBase.contentHash === candidateBase.contentHash,
    ))
}

function validateBootstrapCandidateIdentity(value: unknown) {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return malformedRepositorySnapshot('bootstrap Candidate is not an object')
  }
  const source = value as Record<string, unknown>
  if (source.schemaVersion !== 'candidate-workspace/v1') {
    return malformedRepositorySnapshot('bootstrap Candidate schema is unsupported')
  }
  snapshotUUID(source.id, 'bootstrap Candidate.id')
  snapshotUUID(source.projectId, 'bootstrap Candidate.projectId')
  snapshotUUID(source.repositorySnapshotId, 'bootstrap Candidate.repositorySnapshotId')
  parseSnapshotExactRef(source.buildManifest, 'bootstrap Candidate.buildManifest')
  parseSnapshotExactRef(source.buildContract, 'bootstrap Candidate.buildContract')
  parseSnapshotExactRef(source.fullStackTemplate, 'bootstrap Candidate.fullStackTemplate')
  snapshotHash(source.baseTreeHash, 'bootstrap Candidate.baseTreeHash')
  snapshotUUID(source.createdBy, 'bootstrap Candidate.createdBy')
  snapshotTimestamp(source.createdAt, 'bootstrap Candidate.createdAt')
  snapshotTimestamp(source.updatedAt, 'bootstrap Candidate.updatedAt')
  if (Object.hasOwn(source, 'baseWorkspaceRevision')) {
    const base = exactRepositorySnapshotRecord(
      source.baseWorkspaceRevision,
      ['artifactId', 'revisionId', 'contentHash'],
      [],
      'bootstrap Candidate.baseWorkspaceRevision',
    )
    snapshotUUID(base.artifactId, 'bootstrap Candidate.baseWorkspaceRevision.artifactId')
    snapshotUUID(base.revisionId, 'bootstrap Candidate.baseWorkspaceRevision.revisionId')
    snapshotHash(base.contentHash, 'bootstrap Candidate.baseWorkspaceRevision.contentHash', true)
  }
}

export async function normalizeRepositoryCandidateBootstrap(
  value: unknown,
): Promise<RepositoryCandidateBootstrapDto> {
  const source = exactRepositorySnapshotRecord(
    value,
    ['candidate', 'repositorySnapshotReceipt', 'created', 'recovered', 'finalizationPending'],
    [],
    'bootstrap response',
  )
  if (
    typeof source.created !== 'boolean'
    || typeof source.recovered !== 'boolean'
    || typeof source.finalizationPending !== 'boolean'
    || (source.created && source.recovered)
  ) {
    return malformedRepositorySnapshot('bootstrap state flags are invalid')
  }
  validateBootstrapCandidateIdentity(source.candidate)
  const candidate = normalizeCandidateWorkspace(source.candidate)
  const repositorySnapshotReceipt = await parseRepositorySnapshotReceipt(source.repositorySnapshotReceipt)
  if (!repositorySnapshotMatchesBootstrapCandidate(repositorySnapshotReceipt, candidate)) {
    return malformedRepositorySnapshot('bootstrap Candidate differs from its exact RepositorySnapshot')
  }
  return {
    candidate,
    repositorySnapshotReceipt,
    created: source.created,
    recovered: source.recovered,
    finalizationPending: source.finalizationPending,
  }
}

export function normalizeRepositoryCandidateHeadList(
  value: unknown,
): RepositoryCandidateHeadListDto {
  const source = record(value)
  return {
    schemaVersion: text(source.schemaVersion),
    candidates: list(source.candidates).map((entry) => {
      const head = record(entry)
      return {
        candidate: normalizeCandidateWorkspace(head.candidate),
        rebaseId: text(head.rebaseId) || undefined,
      }
    }),
  }
}

export function normalizeCandidateRebase(value: unknown): CandidateRebaseDto {
  const source = record(value)
  return {
    schemaVersion: text(source.schemaVersion), id: text(source.id), projectId: text(source.projectId),
    operationId: text(source.operationId), predecessorCandidateId: text(source.predecessorCandidateId),
    successorCandidateId: text(source.successorCandidateId),
    targetBuildManifestId: text(source.targetBuildManifestId),
    ancestorTreeHash: text(source.ancestorTreeHash), predecessorTreeHash: text(source.predecessorTreeHash),
    targetTreeHash: text(source.targetTreeHash), plannedTreeHash: text(source.plannedTreeHash),
    planHash: text(source.planHash), state: text(source.state) as CandidateRebaseState,
    version: number(source.version), operations: list(source.operations).map(operation),
    conflicts: list(source.conflicts).map(conflict), createdBy: text(source.createdBy),
    createdAt: text(source.createdAt), updatedAt: text(source.updatedAt),
  }
}

export function normalizeCandidateRebaseResult(value: unknown): CandidateRebaseResultDto {
  const source = record(value)
  return {
    rebase: normalizeCandidateRebase(source.rebase),
    candidate: normalizeCandidateWorkspace(source.candidate),
    created: source.created === true, recovered: source.recovered === true,
    finalizationPending: source.finalizationPending === true,
  }
}

export function normalizeCandidateRebaseConflictContent(
  value: unknown,
): CandidateRebaseConflictContentDto {
  const source = record(value)
  return {
    schemaVersion: text(source.schemaVersion), rebaseId: text(source.rebaseId),
    conflictId: text(source.conflictId), path: text(source.path),
    ancestor: fileContent(source.ancestor), predecessor: fileContent(source.predecessor),
    target: fileContent(source.target),
  }
}

const candidateSearchHashPattern = /^sha256:[0-9a-f]{64}$/
const candidateSearchUUIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const candidateSearchEncoder = new TextEncoder()

function malformedCandidateSearch(detail: string): never {
  throw new RepositoryContractError(`The repository service returned malformed Candidate search ${detail}.`)
}

function candidateSearchRecord(value: unknown, detail: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return malformedCandidateSearch(detail)
  }
  return value as Record<string, unknown>
}

function candidateSearchInteger(value: unknown, minimum = 0): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum
}

function candidateSearchBytes(value: string) {
  return candidateSearchEncoder.encode(value).byteLength
}

function candidateSearchCanonicalText(value: unknown, maximumBytes: number): value is string {
  return typeof value === 'string'
    && value.length > 0
    && value === value.trim()
    && candidateSearchBytes(value) <= maximumBytes
    && ![...value].some((character) => {
      const code = character.codePointAt(0) ?? 0
      return code === 0 || code < 0x20 || code === 0x7f
    })
}

function candidateSearchQuery(value: unknown): value is string {
  return typeof value === 'string'
    && value.length > 0
    && candidateSearchBytes(value) <= 256
    && ![...value].some((character) => {
      const code = character.codePointAt(0) ?? 0
      return code < 0x20 || code === 0x7f
    })
}

function candidateSearchPath(value: unknown): value is string {
  if (!candidateSearchCanonicalText(value, 512) || value.startsWith('/') || value.includes('\\')) {
    return false
  }
  return !value.split('/').some((part) => !part || part === '.' || part === '..')
}

function candidateSearchGlob(value: unknown): value is string {
  return candidateSearchCanonicalText(value, 256)
    && !value.startsWith('/')
    && !value.includes('\\')
}

/**
 * Parses the v1 exact-head search response without manufacturing defaults.
 * Search results are code-navigation evidence, so any missing, widened, or
 * internally inconsistent field makes the whole response unusable.
 */
export function parseRepositoryCandidateSearchResult(
  value: unknown,
): RepositoryCandidateSearchResultDto {
  const source = candidateSearchRecord(value, 'response')
  const head = candidateSearchRecord(source.head, 'head')
  const limits = candidateSearchRecord(source.limits, 'limits')
  const stats = candidateSearchRecord(source.stats, 'statistics')
  // Go's canonical zero-value slice is encoded as null here. It has one
  // unambiguous wire meaning: no include filter. Non-empty filters must still
  // be an explicit string array and are identity-checked by the client.
  const includeGlobs = source.includeGlobs === null ? [] : source.includeGlobs

  if (
    source.schemaVersion !== REPOSITORY_CANDIDATE_SEARCH_SCHEMA_VERSION
    || typeof source.projectId !== 'string'
    || !candidateSearchUUIDPattern.test(source.projectId)
    || typeof head.candidateId !== 'string'
    || !candidateSearchUUIDPattern.test(head.candidateId)
    || !candidateSearchInteger(head.generation, 1)
    || typeof head.rootHash !== 'string'
    || !candidateSearchHashPattern.test(head.rootHash)
    || !candidateSearchQuery(source.query)
    || typeof source.caseSensitive !== 'boolean'
    || !Array.isArray(includeGlobs)
    || includeGlobs.length > 16
    || !includeGlobs.every(candidateSearchGlob)
    || typeof source.truncated !== 'boolean'
    || limits.maxQueryBytes !== 256
    || limits.maxIncludeGlobs !== 16
    || limits.maxGlobBytes !== 256
    || limits.maxFiles !== 2_000
    || limits.maxBytes !== 8 * 1024 * 1024
    || !candidateSearchInteger(limits.maxMatches, 1)
    || limits.maxMatches > 500
    || limits.maxPreviewBytes !== 320
    || !candidateSearchInteger(stats.filesScanned)
    || stats.filesScanned > limits.maxFiles
    || !candidateSearchInteger(stats.bytesScanned)
    || stats.bytesScanned > limits.maxBytes
    || !candidateSearchInteger(stats.binaryFilesSkipped)
    || stats.binaryFilesSkipped > stats.filesScanned
    || !Array.isArray(source.matches)
    || source.matches.length > limits.maxMatches
  ) {
    return malformedCandidateSearch('response')
  }

  const matches = source.matches.map((value, index): RepositoryCandidateSearchMatchDto => {
    const match = candidateSearchRecord(value, `match ${index + 1}`)
    if (
      !candidateSearchPath(match.path)
      || !candidateSearchInteger(match.line, 1)
      || !candidateSearchInteger(match.column, 1)
      || typeof match.preview !== 'string'
      || candidateSearchBytes(match.preview) > 320
      || match.preview.includes('\0')
      || typeof match.previewTruncated !== 'boolean'
      || typeof match.contentHash !== 'string'
      || !candidateSearchHashPattern.test(match.contentHash)
    ) {
      return malformedCandidateSearch(`match ${index + 1}`)
    }
    return {
      path: match.path,
      line: match.line,
      column: match.column,
      preview: match.preview,
      previewTruncated: match.previewTruncated,
      contentHash: match.contentHash,
    }
  })

  return {
    schemaVersion: REPOSITORY_CANDIDATE_SEARCH_SCHEMA_VERSION,
    projectId: source.projectId,
    head: {
      candidateId: head.candidateId,
      generation: head.generation,
      rootHash: head.rootHash,
    },
    query: source.query,
    caseSensitive: source.caseSensitive,
    includeGlobs: [...includeGlobs] as string[],
    truncated: source.truncated,
    limits: {
      maxQueryBytes: 256,
      maxIncludeGlobs: 16,
      maxGlobBytes: 256,
      maxFiles: 2_000,
      maxBytes: 8 * 1024 * 1024,
      maxMatches: limits.maxMatches,
      maxPreviewBytes: 320,
    },
    stats: {
      filesScanned: stats.filesScanned,
      bytesScanned: stats.bytesScanned,
      binaryFilesSkipped: stats.binaryFilesSkipped,
    },
    matches,
  }
}

/** The single predicate used by search rendering and match-open fencing. */
export function candidateSearchResultMatchesCandidate(
  result: RepositoryCandidateSearchResultDto,
  candidate: Pick<CandidateWorkspaceDto, 'id' | 'projectId' | 'version' | 'treeHash' | 'currentTree'> | null,
) {
  return Boolean(
    candidate
    && result.projectId === candidate.projectId
    && result.head.candidateId === candidate.id
    && result.head.generation === candidate.version
    && result.head.rootHash === candidate.treeHash
    && result.head.rootHash === candidate.currentTree.treeHash,
  )
}
