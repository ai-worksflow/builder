import type {
  CandidateImplementationSourceDto,
  FileOperationDto,
  ImplementationProposalDto,
} from './flow-contract'
import type { JsonValue, ValidationResultDto } from './dto'

type UnknownRecord = Record<string, unknown>

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const HASH_PATTERN = /^(?:sha256:)?[0-9a-f]{64}$/
const PREFIXED_HASH_PATTERN = /^sha256:[0-9a-f]{64}$/
const RAW_HASH_PATTERN = /^[0-9a-f]{64}$/
const RFC3339_UTC_PATTERN = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?Z$/
const CANDIDATE_OPERATION_PATTERN = /^candidate-(\d{5})-[0-9a-f]{12}$/
const MAX_SAFE_WIRE_INTEGER = Number.MAX_SAFE_INTEGER
const MAX_OPERATIONS = 40_000
const MAX_FILE_BYTES = 4 * 1024 * 1024
const encoder = new TextEncoder()

const executionSources = new Set<ImplementationProposalDto['executionSource']>([
  'manual_submission',
  'manual_generation',
  'workflow_runner',
  'conversation_command',
  'candidate_freeze',
])
const proposalStatuses = new Set<ImplementationProposalDto['status']>([
  'open',
  'reviewing',
  'ready',
  'rejected',
  'applied',
  'partially_applied',
  'stale',
])
const operationKinds = new Set<FileOperationDto['kind']>([
  'file.upsert',
  'file.delete',
  'file.rename',
])
const operationDecisions = new Set<FileOperationDto['decision']>([
  'pending',
  'accepted',
  'rejected',
  'applied',
])
const diagnosticSeverities = new Set<ValidationResultDto['severity']>([
  'info',
  'warning',
  'error',
  'blocker',
])

export class ImplementationProposalContractError extends Error {
  readonly code = 'implementation_proposal_malformed'

  constructor(readonly detail: string) {
    super(`Malformed implementation Proposal: ${detail}`)
    this.name = 'ImplementationProposalContractError'
  }
}

function malformed(detail: string): never {
  throw new ImplementationProposalContractError(detail)
}

function isRecord(value: unknown): value is UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false
  const prototype = Object.getPrototypeOf(value)
  return prototype === Object.prototype || prototype === null
}

function exactRecord(
  value: unknown,
  required: readonly string[],
  optional: readonly string[],
  detail: string,
): UnknownRecord {
  if (!isRecord(value)) malformed(`${detail} must be an object`)
  const allowed = new Set([...required, ...optional])
  const keys = Object.keys(value)
  if (required.some((key) => !Object.hasOwn(value, key)) || keys.some((key) => !allowed.has(key))) {
    malformed(`${detail} has missing or additional fields`)
  }
  return value
}

function has(value: UnknownRecord, key: string) {
  return Object.hasOwn(value, key)
}

function containsControl(value: string) {
  for (const character of value) {
    const code = character.codePointAt(0)!
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) return true
  }
  return false
}

function exactText(value: unknown, detail: string, maximumBytes = Number.POSITIVE_INFINITY) {
  if (typeof value !== 'string' || value.length === 0 || value !== value.trim() ||
    containsControl(value) || encoder.encode(value).byteLength > maximumBytes) {
    malformed(`${detail} must be bounded non-empty text`)
  }
  return value
}

function exactHumanText(value: unknown, detail: string) {
  if (typeof value !== 'string' || value.length === 0 || value !== value.trim() || value.includes('\0')) {
    malformed(`${detail} must be non-empty text`)
  }
  return value
}

function canonicalUUID(value: unknown, detail: string) {
  if (typeof value !== 'string' || !UUID_PATTERN.test(value)) malformed(`${detail} must be a canonical UUID`)
  return value
}

function canonicalHash(value: unknown, detail: string) {
  if (typeof value !== 'string' || !HASH_PATTERN.test(value)) malformed(`${detail} must be a canonical SHA-256 hash`)
  return value
}

function prefixedHash(value: unknown, detail: string) {
  if (typeof value !== 'string' || !PREFIXED_HASH_PATTERN.test(value)) {
    malformed(`${detail} must be a canonical prefixed SHA-256 hash`)
  }
  return value
}

function rawHash(value: unknown, detail: string) {
  if (typeof value !== 'string' || !RAW_HASH_PATTERN.test(value)) {
    malformed(`${detail} must be a canonical raw SHA-256 hash`)
  }
  return value
}

function exactInteger(value: unknown, detail: string, minimum = 0) {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > MAX_SAFE_WIRE_INTEGER) {
    malformed(`${detail} must be a safe integer greater than or equal to ${minimum}`)
  }
  return value as number
}

function exactTimestamp(value: unknown, detail: string) {
  if (typeof value !== 'string') malformed(`${detail} must be an RFC3339 UTC timestamp`)
  const match = RFC3339_UTC_PATTERN.exec(value)
  const milliseconds = Date.parse(value)
  if (!match || !Number.isFinite(milliseconds)) malformed(`${detail} must be an RFC3339 UTC timestamp`)
  const parsed = new Date(milliseconds)
  if (
    parsed.getUTCFullYear() !== Number(match[1]) ||
    parsed.getUTCMonth() + 1 !== Number(match[2]) ||
    parsed.getUTCDate() !== Number(match[3]) ||
    parsed.getUTCHours() !== Number(match[4]) ||
    parsed.getUTCMinutes() !== Number(match[5]) ||
    parsed.getUTCSeconds() !== Number(match[6])
  ) {
    malformed(`${detail} is not a real UTC calendar instant`)
  }
  return value
}

function workspacePath(value: unknown, detail: string) {
  const result = exactText(value, detail, 512)
  if (result.startsWith('/') || result.includes('\\')) malformed(`${detail} must be a safe relative workspace path`)
  const parts = result.split('/')
  if (parts.some((part) => part === '' || part === '.' || part === '..')) {
    malformed(`${detail} must be a clean workspace path`)
  }
  const first = parts[0]
  if (first === '.git' || first === '.ssh' || result === '.env' || result.startsWith('.env.')) {
    malformed(`${detail} is a protected workspace path`)
  }
  return result
}

function identityArray(value: unknown, detail: string): readonly string[] {
  if (!Array.isArray(value) || value.length === 0) malformed(`${detail} must be a non-empty identity array`)
  const result = value.map((entry, index) => exactText(entry, `${detail}[${index}]`))
  if (new Set(result).size !== result.length) malformed(`${detail} contains duplicate identities`)
  return Object.freeze(result)
}

function parseJSONValue(
  value: unknown,
  detail: string,
  depth = 0,
  seen = new WeakSet<object>(),
): JsonValue {
  if (depth > 64) malformed(`${detail} exceeds the JSON nesting limit`)
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) malformed(`${detail} contains a non-finite number`)
    return value
  }
  if (typeof value !== 'object') malformed(`${detail} is not JSON`)
  if (seen.has(value)) malformed(`${detail} contains a cycle`)
  seen.add(value)
  try {
    if (Array.isArray(value)) {
      return Object.freeze(
        value.map((entry, index) => parseJSONValue(entry, `${detail}[${index}]`, depth + 1, seen)),
      ) as unknown as JsonValue[]
    }
    if (!isRecord(value)) malformed(`${detail} contains a non-JSON object`)
    const source = value
    const output: Record<string, JsonValue> = {}
    for (const key of Object.keys(source)) {
      if (containsControl(key)) malformed(`${detail} contains an invalid object key`)
      output[key] = parseJSONValue(source[key], `${detail}.${key}`, depth + 1, seen)
    }
    return Object.freeze(output)
  } finally {
    seen.delete(value)
  }
}

function jsonArray(value: unknown, detail: string): readonly JsonValue[] {
  if (!Array.isArray(value)) malformed(`${detail} must be an array`)
  return Object.freeze(value.map((entry, index) => parseJSONValue(entry, `${detail}[${index}]`)))
}

function stringArray(value: unknown, detail: string): readonly string[] {
  if (!Array.isArray(value)) malformed(`${detail} must be an array`)
  return Object.freeze(value.map((entry, index) => exactHumanText(entry, `${detail}[${index}]`)))
}

function parseOperation(value: unknown, index: number): FileOperationDto {
  const detail = `operations[${index}]`
  const source = exactRecord(
    value,
    ['id', 'kind', 'path', 'decision'],
    [
      'fromPath', 'content', 'language', 'mode', 'expectedHash', 'dependsOn',
      'rationale', 'traceSource', 'decidedBy', 'reason',
    ],
    detail,
  )
  const id = exactText(source.id, `${detail}.id`)
  if (!operationKinds.has(source.kind as FileOperationDto['kind'])) malformed(`${detail}.kind is unsupported`)
  const kind = source.kind as FileOperationDto['kind']
  const path = workspacePath(source.path, `${detail}.path`)
  const fromPath = has(source, 'fromPath') ? workspacePath(source.fromPath, `${detail}.fromPath`) : undefined
  const content = has(source, 'content') ? source.content : undefined
  if (content !== undefined && (typeof content !== 'string' || encoder.encode(content).byteLength > MAX_FILE_BYTES)) {
    malformed(`${detail}.content must be a bounded string`)
  }
  const language = has(source, 'language') ? exactText(source.language, `${detail}.language`, 80) : undefined
  const mode = has(source, 'mode') ? source.mode : undefined
  if (mode !== undefined && mode !== '100644' && mode !== '100755') malformed(`${detail}.mode is unsupported`)
  const expectedHash = has(source, 'expectedHash')
    ? prefixedHash(source.expectedHash, `${detail}.expectedHash`)
    : undefined
  const dependsOn = has(source, 'dependsOn') ? identityArray(source.dependsOn, `${detail}.dependsOn`) : undefined
  const rationale = has(source, 'rationale') ? exactHumanText(source.rationale, `${detail}.rationale`) : undefined
  const traceSource = has(source, 'traceSource') ? identityArray(source.traceSource, `${detail}.traceSource`) : undefined
  if (!operationDecisions.has(source.decision as FileOperationDto['decision'])) {
    malformed(`${detail}.decision is unsupported`)
  }
  const decision = source.decision as FileOperationDto['decision']
  const decidedBy = has(source, 'decidedBy') ? canonicalUUID(source.decidedBy, `${detail}.decidedBy`) : undefined
  const reason = has(source, 'reason') ? exactHumanText(source.reason, `${detail}.reason`) : undefined

  if (kind === 'file.upsert' && (typeof content !== 'string' || fromPath !== undefined)) {
    malformed(`${detail} has an inconsistent upsert shape`)
  }
  if (kind === 'file.delete' && (fromPath !== undefined || content !== undefined || language !== undefined || mode !== undefined)) {
    malformed(`${detail} has an inconsistent delete shape`)
  }
  if (kind === 'file.rename' && (
    fromPath === undefined || fromPath === path || content !== undefined || language !== undefined || mode !== undefined
  )) {
    malformed(`${detail} has an inconsistent rename shape`)
  }
  if (decision === 'pending' && (decidedBy !== undefined || reason !== undefined)) {
    malformed(`${detail} has decision metadata while pending`)
  }
  if (decision !== 'pending' && decidedBy === undefined) malformed(`${detail}.decidedBy is required after a decision`)
  if (decision === 'rejected' && reason === undefined) malformed(`${detail}.reason is required when rejected`)

  return Object.freeze({
    id,
    kind,
    path,
    ...(fromPath !== undefined ? { fromPath } : {}),
    ...(content !== undefined ? { content } : {}),
    ...(language !== undefined ? { language } : {}),
    ...(mode !== undefined ? { mode } : {}),
    ...(expectedHash !== undefined ? { expectedHash } : {}),
    ...(dependsOn !== undefined ? { dependsOn } : {}),
    ...(rationale !== undefined ? { rationale } : {}),
    ...(traceSource !== undefined ? { traceSource } : {}),
    decision,
    ...(decidedBy !== undefined ? { decidedBy } : {}),
    ...(reason !== undefined ? { reason } : {}),
  })
}

function validateOperationGraph(operations: readonly FileOperationDto[]) {
  const byID = new Map<string, FileOperationDto>()
  for (const operation of operations) {
    if (byID.has(operation.id)) malformed(`operations contains duplicate id ${operation.id}`)
    byID.set(operation.id, operation)
  }
  for (const operation of operations) {
    for (const dependency of operation.dependsOn ?? []) {
      if (dependency === operation.id || !byID.has(dependency)) {
        malformed(`operation ${operation.id} has an invalid dependency`)
      }
    }
  }
  const visiting = new Set<string>()
  const visited = new Set<string>()
  const visit = (id: string) => {
    if (visiting.has(id)) malformed('operations contains a dependency cycle')
    if (visited.has(id)) return
    visiting.add(id)
    for (const dependency of byID.get(id)!.dependsOn ?? []) visit(dependency)
    visiting.delete(id)
    visited.add(id)
  }
  for (const id of byID.keys()) visit(id)
}

function parseDiagnostic(value: unknown, index: number): ValidationResultDto {
  const detail = `diagnostics[${index}]`
  const source = exactRecord(value, ['code', 'path', 'message', 'severity'], [], detail)
  if (!diagnosticSeverities.has(source.severity as ValidationResultDto['severity'])) {
    malformed(`${detail}.severity is unsupported`)
  }
  return Object.freeze({
    code: exactText(source.code, `${detail}.code`),
    path: exactText(source.path, `${detail}.path`),
    message: exactHumanText(source.message, `${detail}.message`),
    severity: source.severity as ValidationResultDto['severity'],
  })
}

function parseExactReference(value: unknown, detail: string) {
  const source = exactRecord(value, ['id', 'contentHash'], [], detail)
  return Object.freeze({
    id: canonicalUUID(source.id, `${detail}.id`),
    contentHash: canonicalHash(source.contentHash, `${detail}.contentHash`),
  })
}

function parseCandidateVerificationReference(value: unknown) {
  const detail = 'candidateSource.verificationReceipt'
  const source = exactRecord(value, ['id', 'contentHash'], [], detail)
  if (source.id === '' && source.contentHash === '') {
    return Object.freeze({ id: '', contentHash: '' })
  }
  return Object.freeze({
    id: canonicalUUID(source.id, `${detail}.id`),
    contentHash: canonicalHash(source.contentHash, `${detail}.contentHash`),
  })
}

function parseCandidateSource(value: unknown): CandidateImplementationSourceDto {
  const detail = 'candidateSource'
  const source = exactRecord(value, [
    'freezeReceiptId', 'repositorySnapshotId', 'sessionId', 'candidateId',
    'candidateSnapshotId', 'candidateVersion', 'journalSequence', 'sessionEpoch',
    'writerLeaseEpoch', 'baseTreeHash', 'treeHash', 'fullStackTemplate',
    'verificationReceipt',
  ], [], detail)
  const baseTreeHash = prefixedHash(source.baseTreeHash, `${detail}.baseTreeHash`)
  const treeHash = prefixedHash(source.treeHash, `${detail}.treeHash`)
  if (baseTreeHash === treeHash) malformed(`${detail} does not describe a changed tree`)
  const fullStackTemplate = parseExactReference(source.fullStackTemplate, `${detail}.fullStackTemplate`)
  const verificationReceipt = parseCandidateVerificationReference(source.verificationReceipt)
  const legacyUnverified = verificationReceipt.id === '' && verificationReceipt.contentHash === ''
  if (!PREFIXED_HASH_PATTERN.test(fullStackTemplate.contentHash) ||
    (!legacyUnverified && !PREFIXED_HASH_PATTERN.test(verificationReceipt.contentHash))) {
    malformed(`${detail} exact authorities require prefixed SHA-256 hashes`)
  }
  return Object.freeze({
    freezeReceiptId: canonicalUUID(source.freezeReceiptId, `${detail}.freezeReceiptId`),
    repositorySnapshotId: canonicalUUID(source.repositorySnapshotId, `${detail}.repositorySnapshotId`),
    sessionId: canonicalUUID(source.sessionId, `${detail}.sessionId`),
    candidateId: canonicalUUID(source.candidateId, `${detail}.candidateId`),
    candidateSnapshotId: canonicalUUID(source.candidateSnapshotId, `${detail}.candidateSnapshotId`),
    candidateVersion: exactInteger(source.candidateVersion, `${detail}.candidateVersion`, 1),
    journalSequence: exactInteger(source.journalSequence, `${detail}.journalSequence`),
    sessionEpoch: exactInteger(source.sessionEpoch, `${detail}.sessionEpoch`, 1),
    writerLeaseEpoch: exactInteger(source.writerLeaseEpoch, `${detail}.writerLeaseEpoch`, 1),
    baseTreeHash,
    treeHash,
    fullStackTemplate,
    verificationReceipt,
  })
}

function parseBuildContract(value: unknown) {
  const source = exactRecord(value, ['id', 'contractHash'], [], 'applicationBuildContract')
  return Object.freeze({
    id: canonicalUUID(source.id, 'applicationBuildContract.id'),
    contractHash: canonicalHash(source.contractHash, 'applicationBuildContract.contractHash'),
  })
}

function parseBaseWorkspace(value: unknown) {
  const source = exactRecord(
    value,
    ['artifactId', 'revisionId', 'contentHash'],
    ['anchorId'],
    'baseWorkspaceRevision',
  )
  const anchorId = has(source, 'anchorId')
    ? canonicalUUID(source.anchorId, 'baseWorkspaceRevision.anchorId')
    : undefined
  return Object.freeze({
    artifactId: canonicalUUID(source.artifactId, 'baseWorkspaceRevision.artifactId'),
    revisionId: canonicalUUID(source.revisionId, 'baseWorkspaceRevision.revisionId'),
    contentHash: canonicalHash(source.contentHash, 'baseWorkspaceRevision.contentHash'),
    ...(anchorId !== undefined ? { anchorId } : {}),
  })
}

function validateCandidateBinding(
  source: CandidateImplementationSourceDto,
  baseWorkspaceRevision: NonNullable<ImplementationProposalDto['baseWorkspaceRevision']>,
  operations: readonly FileOperationDto[],
  routes: readonly JsonValue[],
  apis: readonly JsonValue[],
  migrations: readonly JsonValue[],
  tests: readonly JsonValue[],
  previews: readonly JsonValue[],
  traceLinks: readonly JsonValue[],
  diagnostics: readonly ValidationResultDto[],
  assumptions: readonly string[],
  unimplementedItems: readonly string[],
) {
  if (!PREFIXED_HASH_PATTERN.test(baseWorkspaceRevision.contentHash) || baseWorkspaceRevision.anchorId !== undefined) {
    malformed('candidate_freeze requires its exact unanchored base workspace revision')
  }
  if (
    routes.length !== 0 || apis.length !== 0 || migrations.length !== 0 || tests.length !== 0 ||
    previews.length !== 0 || diagnostics.length !== 0 || assumptions.length !== 0 || unimplementedItems.length !== 0
  ) {
    malformed('candidate_freeze contains fields outside the frozen Candidate projection')
  }
  const expectedCandidateTrace = `candidate-snapshot:${source.candidateSnapshotId}`
  for (const [index, operation] of operations.entries()) {
    const match = CANDIDATE_OPERATION_PATTERN.exec(operation.id)
    if (!match || Number(match[1]) !== index + 1 || operation.kind === 'file.rename' ||
      operation.dependsOn !== undefined || operation.rationale !== `Freeze exact CandidateSnapshot ${source.candidateSnapshotId}` ||
      operation.traceSource?.length !== 1 || operation.traceSource[0] !== expectedCandidateTrace) {
      malformed(`operations[${index}] is not bound to the exact Candidate snapshot`)
    }
    if (operation.kind === 'file.upsert' && (operation.language === undefined || operation.mode === undefined)) {
      malformed(`operations[${index}] is missing its Candidate file identity`)
    }
    if (operation.kind === 'file.delete' && operation.expectedHash === undefined) {
      malformed(`operations[${index}] is missing its Candidate predecessor hash`)
    }
  }
  const legacyUnverified = source.verificationReceipt.id === ''
    && source.verificationReceipt.contentHash === ''
  const expectedTraceCount = legacyUnverified ? 1 : 2
  if (traceLinks.length !== expectedTraceCount) {
    malformed('traceLinks does not bind the exact Candidate and VerificationReceipt identities')
  }
  const candidateTrace = exactRecord(traceLinks[0], [
    'baseTreeHash', 'candidateId', 'candidateSnapshotId', 'kind', 'treeHash',
  ], [], 'traceLinks[0]')
  const verificationTrace = legacyUnverified
    ? undefined
    : exactRecord(
        traceLinks[1],
        ['contentHash', 'id', 'kind'],
        [],
        'traceLinks[1]',
      )
  if (candidateTrace.kind !== 'candidate_snapshot' ||
    candidateTrace.candidateId !== source.candidateId ||
    candidateTrace.candidateSnapshotId !== source.candidateSnapshotId ||
    candidateTrace.baseTreeHash !== source.baseTreeHash || candidateTrace.treeHash !== source.treeHash ||
    (verificationTrace !== undefined && (
      verificationTrace.kind !== 'candidate_verification_receipt' ||
      verificationTrace.id !== source.verificationReceipt.id ||
      verificationTrace.contentHash !== source.verificationReceipt.contentHash
    ))) {
    malformed('traceLinks does not bind the exact Candidate and VerificationReceipt identities')
  }
}

function validateStatus(
  status: ImplementationProposalDto['status'],
  operations: readonly FileOperationDto[],
  appliedAt: string | undefined,
) {
  const counts = { pending: 0, accepted: 0, rejected: 0, applied: 0 }
  for (const operation of operations) counts[operation.decision] += 1
  const total = operations.length
  const valid = status === 'open'
    ? counts.pending === total && appliedAt === undefined
    : status === 'reviewing'
      ? counts.pending > 0 && counts.pending < total && counts.applied === 0 && appliedAt === undefined
      : status === 'ready'
        ? counts.pending === 0 && counts.applied === 0 && counts.accepted > 0 && appliedAt === undefined
        : status === 'rejected'
          ? counts.rejected === total && appliedAt === undefined
          : status === 'applied'
            ? counts.applied === total && appliedAt !== undefined
            : status === 'partially_applied'
              ? counts.applied > 0 && counts.rejected > 0 &&
                counts.applied + counts.rejected === total && appliedAt !== undefined
              : counts.applied === 0 && appliedAt === undefined
  if (!valid) malformed(`status ${status} is inconsistent with operation decisions or appliedAt`)
}

export function normalizeImplementationProposal(value: unknown): ImplementationProposalDto {
  const source = exactRecord(value, [
    'id', 'projectId', 'buildManifestId', 'applicationBuildContract', 'executionSource',
    'operations', 'routes', 'apis', 'migrations', 'tests', 'previews', 'traceLinks',
    'diagnostics', 'assumptions', 'unimplementedItems', 'status', 'version',
    'payloadHash', 'createdBy', 'createdAt',
  ], [
    'baseWorkspaceRevision', 'conversationCommandId', 'supersedesProposalId',
    'instructionHash', 'aiProvider', 'aiModel', 'candidateSource', 'appliedAt',
  ], 'proposal')

  const id = canonicalUUID(source.id, 'id')
  const projectId = canonicalUUID(source.projectId, 'projectId')
  const buildManifestId = canonicalUUID(source.buildManifestId, 'buildManifestId')
  const applicationBuildContract = parseBuildContract(source.applicationBuildContract)
  const baseWorkspaceRevision = has(source, 'baseWorkspaceRevision')
    ? parseBaseWorkspace(source.baseWorkspaceRevision)
    : undefined
  if (!executionSources.has(source.executionSource as ImplementationProposalDto['executionSource'])) {
    malformed('executionSource is unsupported')
  }
  const executionSource = source.executionSource as ImplementationProposalDto['executionSource']
  const conversationCommandId = has(source, 'conversationCommandId')
    ? canonicalUUID(source.conversationCommandId, 'conversationCommandId')
    : undefined
  const supersedesProposalId = has(source, 'supersedesProposalId')
    ? canonicalUUID(source.supersedesProposalId, 'supersedesProposalId')
    : undefined
  if (supersedesProposalId === id) malformed('supersedesProposalId cannot equal id')
  const instructionHash = has(source, 'instructionHash')
    ? prefixedHash(source.instructionHash, 'instructionHash')
    : undefined
  const aiProvider = has(source, 'aiProvider') ? exactText(source.aiProvider, 'aiProvider') : undefined
  const aiModel = has(source, 'aiModel') ? exactText(source.aiModel, 'aiModel') : undefined
  const candidateSource = has(source, 'candidateSource') ? parseCandidateSource(source.candidateSource) : undefined

  if (!Array.isArray(source.operations) || source.operations.length < 1 || source.operations.length > MAX_OPERATIONS) {
    malformed(`operations must contain between 1 and ${MAX_OPERATIONS} entries`)
  }
  const operations = Object.freeze(source.operations.map(parseOperation))
  validateOperationGraph(operations)
  const routes = jsonArray(source.routes, 'routes')
  const apis = jsonArray(source.apis, 'apis')
  const migrations = jsonArray(source.migrations, 'migrations')
  const tests = jsonArray(source.tests, 'tests')
  const previews = jsonArray(source.previews, 'previews')
  const traceLinks = jsonArray(source.traceLinks, 'traceLinks')
  if (!Array.isArray(source.diagnostics)) malformed('diagnostics must be an array')
  const diagnostics = Object.freeze(source.diagnostics.map(parseDiagnostic))
  const assumptions = stringArray(source.assumptions, 'assumptions')
  const unimplementedItems = stringArray(source.unimplementedItems, 'unimplementedItems')
  if (!proposalStatuses.has(source.status as ImplementationProposalDto['status'])) malformed('status is unsupported')
  const status = source.status as ImplementationProposalDto['status']
  const version = exactInteger(source.version, 'version', 1)
  const payloadHash = rawHash(source.payloadHash, 'payloadHash')
  const createdBy = canonicalUUID(source.createdBy, 'createdBy')
  const createdAt = exactTimestamp(source.createdAt, 'createdAt')
  const appliedAt = has(source, 'appliedAt') ? exactTimestamp(source.appliedAt, 'appliedAt') : undefined
  if (appliedAt !== undefined && Date.parse(appliedAt) < Date.parse(createdAt)) {
    malformed('appliedAt precedes createdAt')
  }

  const hasGenerationProvenance = instructionHash !== undefined && aiProvider !== undefined && aiModel !== undefined
  switch (executionSource) {
    case 'manual_submission':
      if (conversationCommandId !== undefined || supersedesProposalId !== undefined || hasGenerationProvenance ||
        candidateSource !== undefined || instructionHash !== undefined || aiProvider !== undefined || aiModel !== undefined) {
        malformed('manual_submission has generated or Candidate provenance')
      }
      break
    case 'manual_generation':
    case 'workflow_runner':
      if (!hasGenerationProvenance || conversationCommandId !== undefined || candidateSource !== undefined) {
        malformed(`${executionSource} has incomplete or conflicting execution provenance`)
      }
      break
    case 'conversation_command':
      if (!hasGenerationProvenance || conversationCommandId !== id || candidateSource !== undefined) {
        malformed('conversation_command does not bind its exact command/proposal identity')
      }
      break
    case 'candidate_freeze':
      if (candidateSource === undefined || baseWorkspaceRevision === undefined || conversationCommandId !== undefined ||
        supersedesProposalId !== undefined || instructionHash !== undefined || aiProvider !== undefined || aiModel !== undefined) {
        malformed('candidate_freeze has incomplete or conflicting execution provenance')
      }
      validateCandidateBinding(
        candidateSource,
        baseWorkspaceRevision,
        operations,
        routes,
        apis,
        migrations,
        tests,
        previews,
        traceLinks,
        diagnostics,
        assumptions,
        unimplementedItems,
      )
      break
  }
  validateStatus(status, operations, appliedAt)

  return Object.freeze({
    id,
    projectId,
    buildManifestId,
    applicationBuildContract,
    ...(baseWorkspaceRevision !== undefined ? { baseWorkspaceRevision } : {}),
    executionSource,
    ...(conversationCommandId !== undefined ? { conversationCommandId } : {}),
    ...(supersedesProposalId !== undefined ? { supersedesProposalId } : {}),
    ...(instructionHash !== undefined ? { instructionHash } : {}),
    ...(aiProvider !== undefined ? { aiProvider } : {}),
    ...(aiModel !== undefined ? { aiModel } : {}),
    ...(candidateSource !== undefined ? { candidateSource } : {}),
    operations,
    routes,
    apis,
    migrations,
    tests,
    previews,
    traceLinks,
    diagnostics,
    assumptions,
    unimplementedItems,
    status,
    version,
    payloadHash,
    createdBy,
    createdAt,
    ...(appliedAt !== undefined ? { appliedAt } : {}),
  })
}
