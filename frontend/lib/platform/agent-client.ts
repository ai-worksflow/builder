import type { ClientMutationOptions, ClientRequestOptions } from './clients'
import {
  HttpClient,
  PlatformProtocolError,
  verifyResponseBodySha256,
  type HttpResult,
} from './http'
import {
  normalizeAgentAttempt,
  normalizeAgentAttemptList,
  normalizeAgentEventList,
  normalizeAgentPatchMergeResult,
  normalizeAgentPatchMergeHistory,
  normalizeAgentPatchUndoResult,
  normalizeAgentPatchValidation,
  normalizeAgentPlatformPatch,
  normalizeAgentStructuredResult,
  normalizeAgentTaskAttemptResult,
  normalizeAgentTaskGraph,
  normalizeAgentTaskGraphAdvanceResult,
  AgentContractError,
  type AgentAttemptDto,
  type AgentAttemptEventDto,
  type AgentEvidenceKind,
  type AgentEventPageDto,
  type AgentPatchMergeResultDto,
  type AgentPatchMergeHistoryItemDto,
  type AgentPatchUndoResultDto,
  type AgentPatchValidationDto,
  type AgentPlatformPatchDto,
  type AgentStructuredResultDto,
  type AgentTaskAttemptResultDto,
  type AgentTaskGraphAdvanceResultDto,
  type AgentTaskGraphDto,
} from './agent-contract'

export interface AgentCreateAttemptInput {
  readonly taskKey: string
  readonly instruction: string
  readonly executorProfile: string
}

export interface AgentAdvanceTaskGraphInput {
  readonly instruction: string
  readonly executorProfile: string
}

export interface AgentPatchFencesInput {
  readonly expectedSessionVersion: number
  readonly expectedSessionEpoch: number
  readonly expectedCandidateVersion: number
  readonly expectedWriterLeaseEpoch: number
}

export interface AgentMutationOptions extends ClientMutationOptions {
  readonly ifMatch: string
}

export type AgentPatchFileSide = 'base' | 'proposed'

export interface AgentPatchFileResult {
  readonly path: string
  readonly side: AgentPatchFileSide
  readonly exists: boolean
  readonly value: ArrayBuffer
  readonly patchContentHash: string
  readonly contentHash: string
  readonly byteSize: number
  readonly mode: string
}

export interface AgentEventRecoveryDto {
  readonly events: readonly AgentAttemptEventDto[]
  readonly afterSequence: number
  readonly lastSequence: number
}

function segment(value: string) {
  return encodeURIComponent(value)
}

function requestOptions(options?: ClientRequestOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
  }
}

function mutationOptions(options: AgentMutationOptions) {
  return {
    signal: options.signal,
    requestId: options.requestId,
    ifMatch: options.ifMatch,
    idempotencyKey: options.idempotencyKey ?? true,
  }
}

function normalizedResult<T>(result: HttpResult<unknown>, normalize: (value: unknown) => T) {
  try {
    return { ...result, data: normalize(result.data) }
  } catch (cause) {
    if (!(cause instanceof AgentContractError)) throw cause
    throw new PlatformProtocolError(cause.message, result.requestId, result.status)
  }
}

function exactIdentity(
  condition: boolean,
  result: Pick<HttpResult<unknown>, 'requestId' | 'status'>,
  detail: string,
) {
  if (!condition) throw new PlatformProtocolError(detail, result.requestId, result.status)
}

const SHA256_DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/
const MAX_AGENT_EVIDENCE_BYTES = 4 << 20
const MAX_AGENT_EVIDENCE_JSON_DEPTH = 32
const MAX_AGENT_EVIDENCE_JSON_NODES = 100_000

function malformedStrictJSON(): never {
  throw new Error('malformed strict JSON')
}

function validUnicodeScalarString(value: string) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (next < 0xdc00 || next > 0xdfff) return false
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false
    }
  }
  return true
}

/**
 * JSON.parse silently accepts duplicate object names by retaining the last
 * value. Evidence must remain inspectable before that lossy transformation,
 * so this bounded parser rejects duplicate names and excessive structure.
 */
class StrictAgentEvidenceJSONParser {
  private index = 0
  private nodes = 0

  constructor(private readonly source: string) {}

  parse(): unknown {
    this.whitespace()
    const value = this.value(0)
    this.whitespace()
    if (this.index !== this.source.length) malformedStrictJSON()
    return value
  }

  private value(depth: number): unknown {
    this.nodes += 1
    if (depth > MAX_AGENT_EVIDENCE_JSON_DEPTH || this.nodes > MAX_AGENT_EVIDENCE_JSON_NODES) {
      malformedStrictJSON()
    }
    const character = this.source[this.index]
    if (character === '{') return this.object(depth + 1)
    if (character === '[') return this.array(depth + 1)
    if (character === '"') return this.string()
    return this.primitive()
  }

  private object(depth: number) {
    this.index += 1
    this.whitespace()
    const result = Object.create(null) as Record<string, unknown>
    const names = new Set<string>()
    if (this.source[this.index] === '}') {
      this.index += 1
      return result
    }
    while (this.index < this.source.length) {
      if (this.source[this.index] !== '"') malformedStrictJSON()
      const name = this.string()
      if (names.has(name)) malformedStrictJSON()
      names.add(name)
      this.whitespace()
      if (this.source[this.index] !== ':') malformedStrictJSON()
      this.index += 1
      this.whitespace()
      result[name] = this.value(depth)
      this.whitespace()
      const separator = this.source[this.index]
      this.index += 1
      if (separator === '}') return result
      if (separator !== ',') malformedStrictJSON()
      this.whitespace()
    }
    return malformedStrictJSON()
  }

  private array(depth: number) {
    this.index += 1
    this.whitespace()
    const result: unknown[] = []
    if (this.source[this.index] === ']') {
      this.index += 1
      return result
    }
    while (this.index < this.source.length) {
      result.push(this.value(depth))
      this.whitespace()
      const separator = this.source[this.index]
      this.index += 1
      if (separator === ']') return result
      if (separator !== ',') malformedStrictJSON()
      this.whitespace()
    }
    return malformedStrictJSON()
  }

  private string() {
    const start = this.index
    this.index += 1
    let escaped = false
    while (this.index < this.source.length) {
      const code = this.source.charCodeAt(this.index)
      if (!escaped && code === 0x22) {
        this.index += 1
        const encoded = this.source.slice(start, this.index)
        try {
          const value = JSON.parse(encoded) as unknown
          if (typeof value !== 'string' || !validUnicodeScalarString(value)) malformedStrictJSON()
          return value
        } catch {
          return malformedStrictJSON()
        }
      }
      if (!escaped && code < 0x20) malformedStrictJSON()
      if (!escaped && code === 0x5c) escaped = true
      else escaped = false
      this.index += 1
    }
    return malformedStrictJSON()
  }

  private primitive() {
    const remaining = this.source.slice(this.index)
    const match = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(remaining)
    if (!match) malformedStrictJSON()
    this.index += match[0].length
    try {
      const value = JSON.parse(match[0]) as unknown
      if (typeof value === 'number' && !Number.isFinite(value)) malformedStrictJSON()
      return value
    } catch {
      return malformedStrictJSON()
    }
  }

  private whitespace() {
    while (/^[\t\n\r ]$/u.test(this.source[this.index] ?? '')) this.index += 1
  }
}

function decodeStrictUTF8(
  value: ArrayBuffer,
  result: Pick<HttpResult<unknown>, 'requestId' | 'status'>,
  detail: string,
) {
  try {
    // ignoreBOM=true preserves a leading BOM as U+FEFF, which the strict JSON
    // parser then rejects instead of silently stripping it.
    const decoded = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(value)
    if (decoded.includes('\uFFFD') || !validUnicodeScalarString(decoded)) throw new Error('invalid Unicode scalar')
    return decoded
  } catch {
    throw new PlatformProtocolError(detail, result.requestId, result.status)
  }
}

function parseStrictAgentEvidenceJSON(
  value: ArrayBuffer,
  result: Pick<HttpResult<unknown>, 'requestId' | 'status'>,
) {
  if (value.byteLength < 1 || value.byteLength > MAX_AGENT_EVIDENCE_BYTES) {
    throw new PlatformProtocolError(
      'The Agent evidence JSON is empty or exceeds its byte bound.',
      result.requestId,
      result.status,
    )
  }
  const source = decodeStrictUTF8(
    value,
    result,
    'The Agent evidence is not strict UTF-8 JSON.',
  )
  try {
    return new StrictAgentEvidenceJSONParser(source).parse()
  } catch {
    throw new PlatformProtocolError(
      'The Agent evidence is not strict UTF-8 JSON.',
      result.requestId,
      result.status,
    )
  }
}

function exactEvidenceHeaders(
  result: HttpResult<ArrayBuffer>,
  attemptId: string,
  kind: AgentEvidenceKind,
  mediaType: string,
) {
  const rawHash = result.headers.get('x-content-hash') ?? ''
  const objectHash = result.headers.get('x-content-object-hash') ?? ''
  const responseMediaType = (result.headers.get('content-type') ?? '')
    .split(';', 1)[0]
    ?.trim()
    .toLowerCase()
  const expectedEtag = `"agent-evidence:${attemptId}:${kind}:${objectHash}"`
  if (
    !SHA256_DIGEST_PATTERN.test(rawHash)
    || !SHA256_DIGEST_PATTERN.test(objectHash)
    || responseMediaType !== mediaType
    || result.etag !== expectedEtag
  ) {
    throw new PlatformProtocolError(
      'The platform omitted the exact Agent evidence hash, object identity, or media type.',
      result.requestId,
      result.status,
    )
  }
  return rawHash
}

export function agentAttemptEtag(attempt: Pick<AgentAttemptDto, 'id' | 'version'>) {
  return `"agent-attempt:${attempt.id}:${attempt.version}"`
}

export function agentMergeEtag(result: Pick<AgentPatchMergeResultDto, 'plan'>) {
  return `"agent-merge:${result.plan.id}:${result.plan.contentHash}"`
}

export class AgentClient {
  constructor(private readonly http: HttpClient) {}

  async createAttempt(
    sessionId: string,
    input: AgentCreateAttemptInput,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<AgentTaskAttemptResultDto>> {
    const result = await this.http.post<unknown, AgentCreateAttemptInput>(
      `/v1/sandbox-sessions/${segment(sessionId)}/agent-attempts`,
      input,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    const normalized = normalizedResult(result, normalizeAgentTaskAttemptResult)
    exactIdentity(
      normalized.data.attempt.sandboxSessionId === sessionId,
      result,
      'The Agent attempt response does not belong to the requested SandboxSession.',
    )
    return normalized
  }

  async getTaskGraph(
    sessionId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<AgentTaskGraphDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/agent-task-graph`,
      requestOptions(options),
    )
    const normalized = normalizedResult(result, normalizeAgentTaskGraph)
    exactIdentity(
      normalized.data.sandboxSessionId === sessionId,
      result,
      'The Agent task graph does not belong to the requested SandboxSession.',
    )
    return normalized
  }

  async advanceTaskGraph(
    sessionId: string,
    input: AgentAdvanceTaskGraphInput,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<AgentTaskGraphAdvanceResultDto>> {
    const result = await this.http.post<unknown, AgentAdvanceTaskGraphInput>(
      `/v1/sandbox-sessions/${segment(sessionId)}/agent-task-graph/advance`,
      input,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    const normalized = normalizedResult(result, normalizeAgentTaskGraphAdvanceResult)
    exactIdentity(
      normalized.data.graph.sandboxSessionId === sessionId,
      result,
      'The advanced Agent task graph does not belong to the requested SandboxSession.',
    )
    return normalized
  }

  async listAttempts(
    sessionId: string,
    limit = 50,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<readonly AgentAttemptDto[]>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/agent-attempts`,
      { ...requestOptions(options), query: { limit } },
    )
    const normalized = normalizedResult(result, normalizeAgentAttemptList)
    exactIdentity(
      normalized.data.every((attempt) => attempt.sandboxSessionId === sessionId),
      result,
      'The Agent attempt list widened beyond the requested SandboxSession.',
    )
    return normalized
  }

  async getAttempt(
    attemptId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<AgentTaskAttemptResultDto>> {
    const result = await this.http.get<unknown>(
      `/v1/agent-attempts/${segment(attemptId)}`,
      requestOptions(options),
    )
    const normalized = normalizedResult(result, normalizeAgentTaskAttemptResult)
    exactIdentity(
      normalized.data.attempt.id === attemptId,
      result,
      'The Agent attempt response has a different exact identity.',
    )
    return normalized
  }

  async listEvents(
    attemptId: string,
    afterSequence = 0,
    limit = 200,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<AgentEventPageDto>> {
    if (!Number.isSafeInteger(afterSequence) || afterSequence < 0 ||
      !Number.isSafeInteger(limit) || limit < 1 || limit > 1000) {
      throw new PlatformProtocolError('A bounded non-negative Agent event cursor and limit are required.')
    }
    const result = await this.http.get<unknown>(
      `/v1/agent-attempts/${segment(attemptId)}/events`,
      { ...requestOptions(options), query: { afterSequence, limit } },
    )
    const normalized = normalizedResult(result, normalizeAgentEventList)
    exactIdentity(
      normalized.data.afterSequence === afterSequence &&
      normalized.data.events.every((event) => event.attemptId === attemptId),
      result,
      'The Agent event page does not match the exact requested Attempt cursor.',
    )
    return normalized
  }

  async recoverEvents(
    attemptId: string,
    afterSequence: number,
    options?: ClientRequestOptions & { readonly limit?: number; readonly maxPages?: number },
  ): Promise<AgentEventRecoveryDto> {
    const limit = options?.limit ?? 200
    const maxPages = options?.maxPages ?? 4
    if (!Number.isSafeInteger(maxPages) || maxPages < 1 || maxPages > 8) {
      throw new PlatformProtocolError('Agent event recovery must use between one and eight bounded pages.')
    }
    let cursor = afterSequence
    const events: AgentAttemptEventDto[] = []
    for (let pageIndex = 0; pageIndex < maxPages; pageIndex += 1) {
      const page = await this.listEvents(attemptId, cursor, limit, options)
      events.push(...page.data.events)
      cursor = page.data.lastSequence
      if (page.data.events.length < limit) {
        return { events, afterSequence, lastSequence: cursor }
      }
    }
    throw new PlatformProtocolError(
      'Agent event recovery exceeded its bounded page window; an exact snapshot reset is required.',
    )
  }

  async listMerges(
    attemptId: string,
    limit = 50,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<readonly AgentPatchMergeHistoryItemDto[]>> {
    const result = await this.http.get<unknown>(
      `/v1/agent-attempts/${segment(attemptId)}/merges`,
      { ...requestOptions(options), query: { limit } },
    )
    const normalized = normalizedResult(result, normalizeAgentPatchMergeHistory)
    exactIdentity(
      normalized.data.every((item) => item.plan.attemptId === attemptId),
      result,
      'The Agent merge history widened beyond the requested Attempt.',
    )
    return normalized
  }

  async cancelAttempt(
    attemptId: string,
    reason: string,
    options: AgentMutationOptions,
  ): Promise<HttpResult<AgentAttemptDto>> {
    const result = await this.http.post<unknown, { readonly reason: string }>(
      `/v1/agent-attempts/${segment(attemptId)}:cancel`,
      { reason },
      mutationOptions(options),
    )
    const normalized = normalizedResult(result, normalizeAgentAttempt)
    exactIdentity(normalized.data.id === attemptId, result, 'The cancelled AgentAttempt identity changed.')
    return normalized
  }

  async retryAttempt(
    attemptId: string,
    reason: string,
    options: AgentMutationOptions,
  ): Promise<HttpResult<AgentTaskAttemptResultDto>> {
    const result = await this.http.post<unknown, { readonly reason: string }>(
      `/v1/agent-attempts/${segment(attemptId)}:retry`,
      { reason },
      mutationOptions(options),
    )
    const normalized = normalizedResult(result, normalizeAgentTaskAttemptResult)
    exactIdentity(
      normalized.data.attempt.parentAttemptId === attemptId,
      result,
      'The retried AgentAttempt does not bind the requested parent Attempt.',
    )
    return normalized
  }

  async readPatch(
    attemptId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<AgentPlatformPatchDto>> {
    const normalized = await this.readJSONEvidence(
      attemptId,
      'patch',
      normalizeAgentPlatformPatch,
      options,
    )
    exactIdentity(normalized.data.attemptId === attemptId, normalized, 'The Agent patch identity changed.')
    return normalized
  }

  async readPatchFile(
    attemptId: string,
    path: string,
    side: AgentPatchFileSide,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<AgentPatchFileResult>> {
    const result = await this.http.get<ArrayBuffer>(
      `/v1/agent-attempts/${segment(attemptId)}/patch-file`,
      {
        ...requestOptions(options),
        query: { path, side },
        responseType: 'arrayBuffer',
      },
    )
    const existsHeader = result.headers.get('x-file-exists')
    if (existsHeader !== 'true' && existsHeader !== 'false') {
      throw new PlatformProtocolError(
        'The platform omitted the exact patch file existence state.',
        result.requestId,
        result.status,
      )
    }
    const exists = existsHeader === 'true'
    if (!(result.data instanceof ArrayBuffer)) {
      throw new PlatformProtocolError(
        'The platform omitted the authoritative patch file byte representation.',
        result.requestId,
        result.status,
      )
    }
    const value = result.data
    const byteSizeHeader = result.headers.get('x-byte-size') ?? ''
    const byteSize = /^(?:0|[1-9]\d*)$/.test(byteSizeHeader) ? Number(byteSizeHeader) : Number.NaN
    if (!Number.isSafeInteger(byteSize) || byteSize < 0 || value.byteLength !== byteSize) {
      throw new PlatformProtocolError(
        'The platform returned inconsistent patch file bytes.',
        result.requestId,
        result.status,
      )
    }
    if ((!exists && (result.status !== 204 || byteSize !== 0)) || (exists && result.status !== 200)) {
      throw new PlatformProtocolError(
        'The patch file status does not match its exact existence state.',
        result.requestId,
        result.status,
      )
    }
    const contentHash = result.headers.get('x-content-hash') ?? ''
    const mode = result.headers.get('x-file-mode') ?? ''
    const patchContentHash = result.headers.get('x-patch-content-hash') ?? ''
    const representationEtagPrefix = `"agent-patch-file:${attemptId}:${side}:`
    const representationHash = result.etag?.startsWith(representationEtagPrefix)
      ? result.etag.slice(representationEtagPrefix.length, -1)
      : ''
    if (
      !SHA256_DIGEST_PATTERN.test(patchContentHash)
      || !result.etag?.endsWith('"')
      || !SHA256_DIGEST_PATTERN.test(representationHash)
    ) {
      throw new PlatformProtocolError(
        'The platform omitted the finalized patch or representation identity.',
        result.requestId,
        result.status,
      )
    }
    if (exists && (!SHA256_DIGEST_PATTERN.test(contentHash) || (mode !== '100644' && mode !== '100755'))) {
      throw new PlatformProtocolError(
        'The platform omitted exact patch file identity metadata.',
        result.requestId,
        result.status,
      )
    }
    if (!exists && (contentHash !== '' || mode !== '')) {
      throw new PlatformProtocolError(
        'An absent patch file cannot claim content identity metadata.',
        result.requestId,
        result.status,
      )
    }
    if (exists) {
      await verifyResponseBodySha256(
        value,
        contentHash,
        'The Agent patch file bytes do not match the declared X-Content-Hash.',
        result.requestId,
        result.status,
      )
    }
    return {
      ...result,
      data: { path, side, exists, value, patchContentHash, contentHash, byteSize, mode },
    }
  }

  async readStructuredResult(
    attemptId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<AgentStructuredResultDto>> {
    return this.readJSONEvidence(
      attemptId,
      'structured_result',
      normalizeAgentStructuredResult,
      options,
    )
  }

  async readValidation(
    attemptId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<AgentPatchValidationDto>> {
    const normalized = await this.readJSONEvidence(
      attemptId,
      'validation',
      normalizeAgentPatchValidation,
      options,
    )
    exactIdentity(normalized.data.attemptId === attemptId, normalized, 'The Agent validation identity changed.')
    return normalized
  }

  readStdout(attemptId: string, options?: ClientRequestOptions) {
    return this.readLog(attemptId, 'stdout', options)
  }

  readStderr(attemptId: string, options?: ClientRequestOptions) {
    return this.readLog(attemptId, 'stderr', options)
  }

  async mergePatch(
    attemptId: string,
    fences: AgentPatchFencesInput,
    options: AgentMutationOptions,
  ): Promise<HttpResult<AgentPatchMergeResultDto>> {
    const result = await this.http.post<unknown, AgentPatchFencesInput>(
      `/v1/agent-attempts/${segment(attemptId)}/merge`,
      fences,
      { ...mutationOptions(options), acceptedStatuses: [409] },
    )
    const normalized = normalizedResult(result, normalizeAgentPatchMergeResult)
    exactIdentity(normalized.data.plan.attemptId === attemptId, result, 'The Agent merge plan identity changed.')
    return normalized
  }

  async undoPatch(
    mergeId: string,
    fences: AgentPatchFencesInput,
    options: AgentMutationOptions,
  ): Promise<HttpResult<AgentPatchUndoResultDto>> {
    const result = await this.http.post<unknown, AgentPatchFencesInput>(
      `/v1/agent-merges/${segment(mergeId)}/undo`,
      fences,
      { ...mutationOptions(options), acceptedStatuses: [409] },
    )
    return normalizedResult(result, normalizeAgentPatchUndoResult)
  }

  private async readJSONEvidence<T>(
    attemptId: string,
    kind: 'patch' | 'structured_result' | 'validation',
    normalize: (value: unknown) => T,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<T>> {
    const result = await this.http.get<ArrayBuffer>(
      `/v1/agent-attempts/${segment(attemptId)}/evidence/${kind}`,
      { ...requestOptions(options), responseType: 'arrayBuffer' },
    )
    if (!(result.data instanceof ArrayBuffer)) {
      throw new PlatformProtocolError(
        'The platform omitted the authoritative Agent evidence bytes.',
        result.requestId,
        result.status,
      )
    }
    const rawHash = exactEvidenceHeaders(result, attemptId, kind, 'application/json')
    await verifyResponseBodySha256(
      result.data,
      rawHash,
      'The Agent evidence bytes do not match the declared X-Content-Hash.',
      result.requestId,
      result.status,
    )
    return normalizedResult(
      { ...result, data: parseStrictAgentEvidenceJSON(result.data, result) },
      normalize,
    )
  }

  private async readLog(
    attemptId: string,
    kind: 'stdout' | 'stderr',
    options?: ClientRequestOptions,
  ) {
    const result = await this.http.get<ArrayBuffer>(
      `/v1/agent-attempts/${segment(attemptId)}/evidence/${kind}`,
      { ...requestOptions(options), responseType: 'arrayBuffer' },
    )
    if (!(result.data instanceof ArrayBuffer)) {
      throw new PlatformProtocolError(
        'The platform omitted the authoritative Agent log bytes.',
        result.requestId,
        result.status,
      )
    }
    const rawHash = exactEvidenceHeaders(
      result,
      attemptId,
      kind,
      kind === 'stdout' ? 'application/x-ndjson' : 'text/plain',
    )
    await verifyResponseBodySha256(
      result.data,
      rawHash,
      'The Agent log bytes do not match the declared X-Content-Hash.',
      result.requestId,
      result.status,
    )
    return {
      ...result,
      data: decodeStrictUTF8(
        result.data,
        result,
        'The Agent log evidence is not strict UTF-8.',
      ),
    }
  }
}
