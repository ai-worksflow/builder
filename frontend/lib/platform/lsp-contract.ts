import { sha256Bytes, sha256DigestString } from './sha256'

export const LSP_TICKET_REQUEST_SCHEMA_VERSION = 'sandbox-lsp-ticket-request/v1'
export const LSP_TICKET_SCHEMA_VERSION = 'sandbox-lsp-ticket/v1'
export const LSP_CONNECTION_SCHEMA_VERSION = 'sandbox-lsp-connection/v1'
export const LSP_BINDING_SCHEMA_VERSION = 'sandbox-lsp-binding/v1'
export const LSP_ENVELOPE_SCHEMA_VERSION = 'sandbox-lsp-envelope/v1'
export const LSP_LANGUAGE_SERVER_PROFILE_SCHEMA_VERSION = 'language-server-profile/v1'
export const LSP_LANGUAGE_SERVER_CAPABILITY_SCHEMA_VERSION = 'language-server-capabilities/v1'
export const LSP_WEB_SOCKET_PATH = '/v1/sandbox-lsp'
export const LSP_WEB_SOCKET_SUBPROTOCOL = 'worksflow.sandbox-lsp.v1'

export const LSP_CLOSE_MESSAGE_MALFORMED = 4400
export const LSP_CLOSE_BINDING_STALE = 4409
export const LSP_CLOSE_RUNTIME_UNAVAILABLE = 4500

export const LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH =
  'sha256:3a6752895f60c697f9e048f30f8c5edf9340432c3af0619d8b26f24de7a8472b'
export const LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH =
  'sha256:a2b30d9614131fc4030afa07ac01c2bf5477c7f90c720594a563aae58400bd36'

const MAX_SAFE_WIRE_INTEGER = 9_007_199_254_740_991
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/
const PROFILE_ID_PATTERN = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/
const LANGUAGE_ID_PATTERN = /^[a-z][a-z0-9.+-]{0,63}$/
const OCI_IMAGE_PATTERN = /^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?\/[a-z0-9]+(?:[._/-][a-z0-9]+)*@sha256:[0-9a-f]{64}$/
const TICKET_PATTERN = /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/
const RFC3339_UTC_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/

const BASELINE_METHODS = new Set([
  'textDocument/completion',
  'textDocument/declaration',
  'textDocument/definition',
  'textDocument/diagnostic',
  'textDocument/documentHighlight',
  'textDocument/documentSymbol',
  'textDocument/hover',
  'textDocument/implementation',
  'textDocument/inlayHint',
  'textDocument/publishDiagnostics',
  'textDocument/references',
  'textDocument/semanticTokens/full',
  'textDocument/semanticTokens/range',
  'textDocument/signatureHelp',
  'textDocument/typeDefinition',
])

export type SandboxLSPMode = 'snapshot' | 'editor'

export type SandboxLSPErrorCode =
  | 'lsp_message_malformed'
  | 'lsp_ticket_scope_mismatch'
  | 'lsp_connection_identity_mismatch'
  | 'lsp_binding_stale'
  | 'lsp_websocket_url_required'
  | 'lsp_crypto_unavailable'
  | 'lsp_session_not_ready'
  | 'lsp_session_closed'
  | 'lsp_request_cancelled'

const ERROR_MESSAGES: Readonly<Record<SandboxLSPErrorCode, string>> = {
  lsp_message_malformed: 'The LSP protocol message is malformed.',
  lsp_ticket_scope_mismatch: 'The LSP ticket does not match the requested immutable scope.',
  lsp_connection_identity_mismatch: 'The LSP connection identity does not match its one-time ticket.',
  lsp_binding_stale: 'The LSP document binding is stale.',
  lsp_websocket_url_required: 'A canonical LSP WebSocket URL is required.',
  lsp_crypto_unavailable: 'The browser cannot verify the LSP profile commitment.',
  lsp_session_not_ready: 'The LSP session is not ready.',
  lsp_session_closed: 'The LSP session is closed.',
  lsp_request_cancelled: 'The LSP request was cancelled.',
}

/** A deliberately context-free error: it never retains a frame or bearer ticket. */
export class SandboxLSPError extends Error {
  readonly code: SandboxLSPErrorCode

  constructor(code: SandboxLSPErrorCode) {
    super(ERROR_MESSAGES[code])
    this.name = 'SandboxLSPError'
    this.code = code
  }
}

export interface SandboxHeadFenceDto {
  readonly projectId: string
  readonly sessionId: string
  readonly sessionEpoch: number
  readonly candidateId: string
  readonly version: number
  readonly journalSequence: number
  readonly writerLeaseEpoch: number
  readonly treeHash: string
}

export interface ExactTemplateReleaseDto {
  readonly id: string
  readonly contentHash: string
}

export interface LSPDocumentFenceDto {
  readonly modelUri: string
  readonly openId: string
  readonly modelVersion: number
  readonly savedContentHash: string
}

export interface LanguageServerRuntimeDto {
  readonly image: string
  readonly executablePath: string
  readonly executableDigest: string
  readonly argv: readonly string[]
  readonly workingDirectoryPolicy: 'service-root'
}

export interface LanguageServerInfoDto {
  readonly name: string
  readonly version: string
}

export interface LanguageServerLimitsDto {
  readonly startupTimeoutMillis: number
  readonly requestTimeoutMillis: number
  readonly shutdownTimeoutMillis: number
  readonly cpuMillis: number
  readonly memoryBytes: number
  readonly pidLimit: number
  readonly tempBytes: number
  readonly cacheBytes: number
  readonly maxOpenDocuments: number
  readonly maxDocumentBytes: number
  readonly maxTotalSyncBytes: number
  readonly maxFrameBytes: number
  readonly maxResultBytes: number
  readonly maxConcurrentRequests: number
  readonly requestsPerSecond: number
  readonly requestBurst: number
  readonly maxDiagnosticsPerDocument: number
  readonly maxCompletionItems: number
  readonly maxNavigationLocations: number
}

export interface LanguageServerIsolationDto {
  readonly networkPolicy: 'none'
  readonly workspaceMountPolicy: 'read-only'
  readonly tempPolicy: 'isolated-bounded'
  readonly cachePolicy: 'isolated-bounded'
  readonly workspacePluginPolicy: 'forbidden'
  readonly dynamicSdkPolicy: 'forbidden'
  readonly dynamicRegistrationPolicy: 'forbidden'
  readonly configurationCommandPolicy: 'forbidden'
  readonly packageManagerHookPolicy: 'forbidden'
}

export interface LSPProfileIdentityDto {
  readonly schemaVersion: 'language-server-profile/v1'
  readonly id: string
  readonly contentHash: string
  readonly serviceId: string
  readonly languageIds: readonly string[]
  readonly fileGlobs: readonly string[]
  readonly protocolVersion: '3.17'
  readonly runtime: LanguageServerRuntimeDto
  readonly serverInfo: LanguageServerInfoDto
  readonly initializationParametersHash: string
  readonly workspaceConfigurationHash: string
  readonly requireVersionedDiagnostics: true
  readonly methods: readonly string[]
  readonly capabilityHash: string
  readonly limits: LanguageServerLimitsDto
  readonly isolation: LanguageServerIsolationDto
  readonly templateRelease: ExactTemplateReleaseDto
  readonly effectiveLimits: LanguageServerLimitsDto
}

export type LSPTemplateProfileDto = Omit<
  LSPProfileIdentityDto,
  'templateRelease' | 'effectiveLimits'
>

export interface LSPTemplateProfileDiscoveryDto {
  readonly templateRelease: ExactTemplateReleaseDto
  readonly profiles: readonly LSPTemplateProfileDto[]
}

export interface LSPTicketRequestDto {
  readonly schemaVersion: 'sandbox-lsp-ticket-request/v1'
  readonly mode: SandboxLSPMode
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly templateRelease: ExactTemplateReleaseDto
  readonly profileIds: readonly string[]
}

export interface LSPTicketDto {
  readonly schemaVersion: 'sandbox-lsp-ticket/v1'
  readonly id: string
  readonly ticket: string
  readonly webSocketPath: '/v1/sandbox-lsp'
  readonly subprotocol: 'worksflow.sandbox-lsp.v1'
  readonly mode: SandboxLSPMode
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly templateRelease: ExactTemplateReleaseDto
  readonly profiles: readonly LSPProfileIdentityDto[]
  readonly expiresAt: string
}

export type LSPTicketHandshakeScopeDto = Pick<
  LSPTicketDto,
  'id' | 'mode' | 'sandboxHeadFence' | 'templateRelease' | 'profiles'
>

export interface LSPConnectionHelloDto {
  readonly schemaVersion: 'sandbox-lsp-connection/v1'
  readonly kind: 'server.hello'
  readonly connectionId: string
  readonly ticketId: string
  readonly sequence: 0
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly templateRelease: ExactTemplateReleaseDto
  readonly profiles: readonly LSPProfileIdentityDto[]
  readonly limits: LanguageServerLimitsDto
  readonly bindDeadlineAt: string
}

export interface LSPClientBindDto {
  readonly schemaVersion: 'sandbox-lsp-binding/v1'
  readonly kind: 'client.bind'
  readonly connectionId: string
  readonly bindingId: null
  readonly sequence: 1
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly languageServerProfile: LSPProfileIdentityDto
  readonly documents: readonly LSPDocumentFenceDto[]
}

export interface LSPBoundLanguageServerDto {
  readonly profileId: string
  readonly profileContentHash: string
  readonly runtimeImageDigest: string
  readonly executableDigest: string
  readonly serverName: string
  readonly serverVersion: string
  readonly capabilityAllowlistHash: string
}

export interface LSPServerBoundDto {
  readonly schemaVersion: 'sandbox-lsp-binding/v1'
  readonly kind: 'server.bound'
  readonly connectionId: string
  readonly bindingId: string
  readonly sequence: 1
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly languageServer: LSPBoundLanguageServerDto
  readonly documents: readonly LSPDocumentFenceDto[]
  readonly effectiveCapabilities: readonly string[]
  readonly limits: LanguageServerLimitsDto
}

export type LSPClientEnvelopeKind =
  | 'client.document.open'
  | 'client.document.change'
  | 'client.document.close'
  | 'client.request'
  | 'client.cancel'
  | 'client.headRebind'
  | 'client.ping'

export type LSPServerEnvelopeKind =
  | 'server.response'
  | 'server.diagnostics'
  | 'server.stale'
  | 'server.error'
  | 'server.pong'

export interface LSPPositionDto {
  readonly line: number
  readonly character: number
}

export interface LSPRangeDto {
  readonly start: LSPPositionDto
  readonly end: LSPPositionDto
}

export interface LSPDiagnosticDto {
  readonly range: LSPRangeDto
  readonly message: string
  readonly severity?: 1 | 2 | 3 | 4
  readonly code?: string | number
  readonly source?: string
  readonly tags?: readonly (1 | 2)[]
}

export interface LSPPublishDiagnosticsDto {
  readonly uri: string
  readonly version: number
  readonly diagnostics: readonly LSPDiagnosticDto[]
}

export interface LSPServerResponsePayloadDto {
  readonly status: 'ok' | 'error'
  readonly result: unknown
  readonly error: null | { readonly code: number; readonly message: string }
}

export interface LSPServerEnvelopeDto {
  readonly schemaVersion: 'sandbox-lsp-envelope/v1'
  readonly connectionId: string
  readonly bindingId: string
  readonly sequence: number
  readonly messageId: string
  readonly replyTo: string | null
  readonly kind: LSPServerEnvelopeKind
  readonly method: string
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly documentFence: LSPDocumentFenceDto | null
  readonly payload:
    | LSPServerResponsePayloadDto
    | { readonly diagnostics: LSPPublishDiagnosticsDto }
    | { readonly code: string }
    | { readonly code: string; readonly message: string }
    | { readonly nonce: string }
}

export interface LSPEnvelopeRequestExpectation {
  readonly messageId: string
  readonly method: string
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly documentFence: LSPDocumentFenceDto
}

export interface LSPEnvelopePingExpectation {
  readonly messageId: string
  readonly nonce: string
  readonly sandboxHeadFence: SandboxHeadFenceDto
}

export interface LSPServerEnvelopeExpectation {
  readonly connectionId: string
  readonly bindingId: string
  readonly sequence: number
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly documents: readonly LSPDocumentFenceDto[]
  readonly pendingRequests: readonly LSPEnvelopeRequestExpectation[]
  readonly staleRequests: readonly LSPEnvelopeRequestExpectation[]
  readonly pendingPings: readonly LSPEnvelopePingExpectation[]
  readonly seenMessageIds: ReadonlySet<string>
  readonly limits: LanguageServerLimitsDto
}

type UnknownRecord = Record<string, unknown>

function malformed(): never {
  throw new SandboxLSPError('lsp_message_malformed')
}

function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function exactRecord(value: unknown, keys: readonly string[]): UnknownRecord {
  if (!isRecord(value)) malformed()
  const actual = Object.keys(value)
  if (actual.length !== keys.length || keys.some((key) => !Object.hasOwn(value, key))) malformed()
  return value
}

function exactOptionalRecord(
  value: unknown,
  required: readonly string[],
  optional: readonly string[],
): UnknownRecord {
  if (!isRecord(value)) malformed()
  const allowed = new Set([...required, ...optional])
  const actual = Object.keys(value)
  if (required.some((key) => !Object.hasOwn(value, key)) ||
    actual.some((key) => !allowed.has(key))) malformed()
  return value
}

function exactString(value: unknown, pattern?: RegExp, maximum = Number.POSITIVE_INFINITY): string {
  if (typeof value !== 'string' || value.length === 0 || value.length > maximum ||
    value !== value.trim() || containsControl(value) || (pattern && !pattern.test(value))) malformed()
  return value
}

function containsControl(value: string) {
  for (const character of value) {
    const code = character.codePointAt(0)!
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) return true
  }
  return false
}

function exactInteger(value: unknown, minimum = 0, maximum = MAX_SAFE_WIRE_INTEGER): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) malformed()
  return value as number
}

function canonicalUUID(value: unknown): string {
  return exactString(value, UUID_PATTERN, 36)
}

function digest(value: unknown): string {
  return exactString(value, DIGEST_PATTERN, 71)
}

function sortedStrings(
  value: unknown,
  minimum: number,
  maximum: number,
  validator: (entry: unknown) => string,
): string[] {
  if (!Array.isArray(value) || value.length < minimum || value.length > maximum) malformed()
  const result = value.map(validator)
  for (let index = 1; index < result.length; index += 1) {
    if (utf8Compare(result[index - 1]!, result[index]!) >= 0) malformed()
  }
  return result
}

function utf8Compare(left: string, right: string) {
  const encoder = new TextEncoder()
  const a = encoder.encode(left)
  const b = encoder.encode(right)
  const count = Math.min(a.length, b.length)
  for (let index = 0; index < count; index += 1) {
    if (a[index] !== b[index]) return a[index]! - b[index]!
  }
  return a.length - b.length
}

function sameValue(left: unknown, right: unknown) {
  return canonicalJSON(left) === canonicalJSON(right)
}

function frozen<T>(value: T): T {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value as UnknownRecord)) frozen(child)
    Object.freeze(value)
  }
  return value
}

class StrictJSONParser {
  private index = 0

  constructor(
    private readonly source: string,
    private readonly maximumDepth: number,
  ) {}

  parse() {
    this.whitespace()
    const value = this.value(0)
    this.whitespace()
    if (this.index !== this.source.length) malformed()
    return value
  }

  private value(depth: number): unknown {
    if (depth > this.maximumDepth) malformed()
    const character = this.source[this.index]
    if (character === '{') return this.object(depth + 1)
    if (character === '[') return this.array(depth + 1)
    if (character === '"') return this.string()
    if (character === 't' && this.literal('true')) return true
    if (character === 'f' && this.literal('false')) return false
    if (character === 'n' && this.literal('null')) return null
    return this.number()
  }

  private object(depth: number) {
    this.index += 1
    this.whitespace()
    const result: UnknownRecord = Object.create(null) as UnknownRecord
    const seen = new Set<string>()
    if (this.source[this.index] === '}') {
      this.index += 1
      return result
    }
    while (this.index < this.source.length) {
      if (this.source[this.index] !== '"') malformed()
      const key = this.string()
      if (seen.has(key)) malformed()
      seen.add(key)
      this.whitespace()
      if (this.source[this.index] !== ':') malformed()
      this.index += 1
      this.whitespace()
      result[key] = this.value(depth)
      this.whitespace()
      const separator = this.source[this.index]
      this.index += 1
      if (separator === '}') return result
      if (separator !== ',') malformed()
      this.whitespace()
    }
    return malformed()
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
      if (separator !== ',') malformed()
      this.whitespace()
    }
    return malformed()
  }

  private string() {
    const start = this.index
    this.index += 1
    let escaped = false
    while (this.index < this.source.length) {
      const code = this.source.charCodeAt(this.index)
      if (!escaped && code === 0x22) {
        this.index += 1
        try {
          const value = JSON.parse(this.source.slice(start, this.index)) as unknown
          if (typeof value !== 'string') malformed()
          return value
        } catch {
          return malformed()
        }
      }
      if (!escaped && code < 0x20) malformed()
      if (!escaped && code === 0x5c) escaped = true
      else escaped = false
      this.index += 1
    }
    return malformed()
  }

  private literal(value: string) {
    if (this.source.slice(this.index, this.index + value.length) !== value) return false
    this.index += value.length
    return true
  }

  private number() {
    const remaining = this.source.slice(this.index)
    const match = /^-?(?:0|[1-9]\d*)/.exec(remaining)
    if (!match) malformed()
    this.index += match[0].length
    const result = Number(match[0])
    // No LSP control-plane v1 field is fractional or wider than the safe wire range.
    if (!Number.isSafeInteger(result) || Object.is(result, -0)) malformed()
    return result
  }

  private whitespace() {
    while (/[\t\n\r ]/.test(this.source[this.index] ?? '')) this.index += 1
  }
}

export function parseStrictLSPJSON(source: string, maximumBytes = 512 << 10, maximumDepth = 12) {
  if (typeof source !== 'string' || source.length === 0 ||
    new TextEncoder().encode(source).byteLength > maximumBytes) malformed()
  return new StrictJSONParser(source, maximumDepth).parse()
}

function parseHead(value: unknown): SandboxHeadFenceDto {
  const source = exactRecord(value, [
    'projectId', 'sessionId', 'sessionEpoch', 'candidateId', 'version',
    'journalSequence', 'writerLeaseEpoch', 'treeHash',
  ])
  return {
    projectId: canonicalUUID(source.projectId),
    sessionId: canonicalUUID(source.sessionId),
    sessionEpoch: exactInteger(source.sessionEpoch, 1),
    candidateId: canonicalUUID(source.candidateId),
    version: exactInteger(source.version, 1),
    journalSequence: exactInteger(source.journalSequence),
    writerLeaseEpoch: exactInteger(source.writerLeaseEpoch),
    treeHash: digest(source.treeHash),
  }
}

function parseRelease(value: unknown): ExactTemplateReleaseDto {
  const source = exactRecord(value, ['id', 'contentHash'])
  return { id: canonicalUUID(source.id), contentHash: digest(source.contentHash) }
}

function parseMode(value: unknown): SandboxLSPMode {
  if (value !== 'snapshot' && value !== 'editor') malformed()
  return value
}

function canonicalProfileIDs(value: unknown): string[] {
  return sortedStrings(value, 1, 1, (entry) => exactString(entry, PROFILE_ID_PATTERN, 80))
}

export function normalizeLSPTicketRequest(value: unknown): LSPTicketRequestDto {
  const source = exactRecord(value, [
    'schemaVersion', 'mode', 'sandboxHeadFence', 'templateRelease', 'profileIds',
  ])
  if (source.schemaVersion !== LSP_TICKET_REQUEST_SCHEMA_VERSION) malformed()
  return frozen({
    schemaVersion: LSP_TICKET_REQUEST_SCHEMA_VERSION,
    mode: parseMode(source.mode),
    sandboxHeadFence: parseHead(source.sandboxHeadFence),
    templateRelease: parseRelease(source.templateRelease),
    profileIds: canonicalProfileIDs(source.profileIds),
  })
}

const LIMIT_KEYS = [
  'startupTimeoutMillis', 'requestTimeoutMillis', 'shutdownTimeoutMillis', 'cpuMillis',
  'memoryBytes', 'pidLimit', 'tempBytes', 'cacheBytes', 'maxOpenDocuments',
  'maxDocumentBytes', 'maxTotalSyncBytes', 'maxFrameBytes', 'maxResultBytes',
  'maxConcurrentRequests', 'requestsPerSecond', 'requestBurst',
  'maxDiagnosticsPerDocument', 'maxCompletionItems', 'maxNavigationLocations',
] as const

const LIMIT_CAPS: Readonly<Record<(typeof LIMIT_KEYS)[number], number>> = {
  startupTimeoutMillis: 20_000,
  requestTimeoutMillis: 10_000,
  shutdownTimeoutMillis: 5_000,
  cpuMillis: 4_000,
  memoryBytes: 4 * 2 ** 30,
  pidLimit: 256,
  tempBytes: 2 * 2 ** 30,
  cacheBytes: 2 * 2 ** 30,
  maxOpenDocuments: 32,
  maxDocumentBytes: 2 ** 20,
  maxTotalSyncBytes: 8 * 2 ** 20,
  maxFrameBytes: 512 * 2 ** 10,
  maxResultBytes: 2 ** 20,
  maxConcurrentRequests: 32,
  requestsPerSecond: 30,
  requestBurst: 60,
  maxDiagnosticsPerDocument: 2_000,
  maxCompletionItems: 500,
  maxNavigationLocations: 5_000,
}

function parseLimits(value: unknown): LanguageServerLimitsDto {
  const source = exactRecord(value, LIMIT_KEYS)
  const result = Object.fromEntries(LIMIT_KEYS.map((key) => [
    key,
    exactInteger(source[key], 1, LIMIT_CAPS[key]),
  ])) as unknown as LanguageServerLimitsDto
  if (result.requestBurst < result.requestsPerSecond ||
    result.maxTotalSyncBytes < result.maxDocumentBytes ||
    result.maxResultBytes < result.maxFrameBytes) malformed()
  return result
}

function parseRuntime(value: unknown): LanguageServerRuntimeDto {
  const source = exactRecord(value, [
    'image', 'executablePath', 'executableDigest', 'argv', 'workingDirectoryPolicy',
  ])
  const executablePath = exactString(source.executablePath, undefined, 500)
  if (!canonicalAbsolutePath(executablePath)) malformed()
  if (!Array.isArray(source.argv) || source.argv.length === 0 || source.argv.length > 64) malformed()
  const argv = source.argv.map((entry) => exactString(entry, undefined, 1_024))
  const basename = executablePath.slice(executablePath.lastIndexOf('/') + 1).toLowerCase()
  if (argv[0] !== executablePath || [
    'sh', 'bash', 'dash', 'zsh', 'fish', 'cmd', 'cmd.exe', 'powershell',
    'powershell.exe', 'pwsh', 'env', 'busybox',
  ].includes(basename) || source.workingDirectoryPolicy !== 'service-root') malformed()
  return {
    image: exactString(source.image, OCI_IMAGE_PATTERN, 500),
    executablePath,
    executableDigest: digest(source.executableDigest),
    argv,
    workingDirectoryPolicy: 'service-root',
  }
}

function canonicalAbsolutePath(value: string) {
  if (value === '/' || !value.startsWith('/') || value.includes('\\') || value.endsWith('/')) return false
  return value.slice(1).split('/').every((segment) => segment !== '' && segment !== '.' && segment !== '..')
}

function parseServerInfo(value: unknown): LanguageServerInfoDto {
  const source = exactRecord(value, ['name', 'version'])
  return {
    name: exactString(source.name, undefined, 160),
    version: exactString(source.version, undefined, 120),
  }
}

function parseIsolation(value: unknown): LanguageServerIsolationDto {
  const keys = [
    'networkPolicy', 'workspaceMountPolicy', 'tempPolicy', 'cachePolicy',
    'workspacePluginPolicy', 'dynamicSdkPolicy', 'dynamicRegistrationPolicy',
    'configurationCommandPolicy', 'packageManagerHookPolicy',
  ] as const
  const source = exactRecord(value, keys)
  if (source.networkPolicy !== 'none' || source.workspaceMountPolicy !== 'read-only' ||
    source.tempPolicy !== 'isolated-bounded' || source.cachePolicy !== 'isolated-bounded' ||
    source.workspacePluginPolicy !== 'forbidden' || source.dynamicSdkPolicy !== 'forbidden' ||
    source.dynamicRegistrationPolicy !== 'forbidden' ||
    source.configurationCommandPolicy !== 'forbidden' ||
    source.packageManagerHookPolicy !== 'forbidden') malformed()
  return {
    networkPolicy: 'none',
    workspaceMountPolicy: 'read-only',
    tempPolicy: 'isolated-bounded',
    cachePolicy: 'isolated-bounded',
    workspacePluginPolicy: 'forbidden',
    dynamicSdkPolicy: 'forbidden',
    dynamicRegistrationPolicy: 'forbidden',
    configurationCommandPolicy: 'forbidden',
    packageManagerHookPolicy: 'forbidden',
  }
}

function canonicalRelativePath(value: string) {
  if (value.length > 512 || value !== value.trim() || value.startsWith('/') || value.endsWith('/') ||
    value.includes('\\') || containsControl(value)) return false
  const segments = value.split('/')
  return segments.every((segment) => {
    const lower = segment.toLowerCase()
    return segment !== '' && segment !== '.' && segment !== '..' && lower !== '.git' &&
      lower !== '.env' && !lower.startsWith('.env.') && lower !== 'node_modules' &&
      lower !== '.next' && lower !== 'dist' && lower !== 'build' && lower !== '__pycache__'
  })
}

function validFileGlob(value: string) {
  if (value.length > 400 || !canonicalRelativePath(value) ||
    Array.from(value).some((character) => '?[]{}!'.includes(character))) return false
  return value.split('/').every((segment) => !segment.includes('**') || segment === '**')
}

function parseProfile(value: unknown): LSPProfileIdentityDto {
  const keys = [
    'schemaVersion', 'id', 'contentHash', 'serviceId', 'languageIds', 'fileGlobs',
    'protocolVersion', 'runtime', 'serverInfo', 'initializationParametersHash',
    'workspaceConfigurationHash', 'requireVersionedDiagnostics', 'methods',
    'capabilityHash', 'limits', 'isolation', 'templateRelease', 'effectiveLimits',
  ] as const
  const source = exactRecord(value, keys)
  if (source.schemaVersion !== LSP_LANGUAGE_SERVER_PROFILE_SCHEMA_VERSION ||
    source.protocolVersion !== '3.17' || source.requireVersionedDiagnostics !== true ||
    source.initializationParametersHash !== LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH ||
    source.workspaceConfigurationHash !== LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH) malformed()
  const fileGlobs = sortedStrings(source.fileGlobs, 1, 64, (entry) => {
    const result = exactString(entry, undefined, 400)
    if (!validFileGlob(result)) malformed()
    return result
  })
  if (new Set(fileGlobs.map((entry) => entry.toLowerCase())).size !== fileGlobs.length) malformed()
  const methods = sortedStrings(source.methods, 1, 32, (entry) => {
    const result = exactString(entry, undefined, 100)
    if (!BASELINE_METHODS.has(result)) malformed()
    return result
  })
  const limits = parseLimits(source.limits)
  const effectiveLimits = parseLimits(source.effectiveLimits)
  if (!sameValue(limits, effectiveLimits)) malformed()
  return {
    schemaVersion: LSP_LANGUAGE_SERVER_PROFILE_SCHEMA_VERSION,
    id: exactString(source.id, PROFILE_ID_PATTERN, 80),
    contentHash: digest(source.contentHash),
    serviceId: exactString(source.serviceId, PROFILE_ID_PATTERN, 80),
    languageIds: sortedStrings(
      source.languageIds,
      1,
      32,
      (entry) => exactString(entry, LANGUAGE_ID_PATTERN, 64),
    ),
    fileGlobs,
    protocolVersion: '3.17',
    runtime: parseRuntime(source.runtime),
    serverInfo: parseServerInfo(source.serverInfo),
    initializationParametersHash: LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH,
    workspaceConfigurationHash: LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH,
    requireVersionedDiagnostics: true,
    methods,
    capabilityHash: digest(source.capabilityHash),
    limits,
    isolation: parseIsolation(source.isolation),
    templateRelease: parseRelease(source.templateRelease),
    effectiveLimits,
  }
}

function parseTemplateProfile(value: unknown): LSPTemplateProfileDto {
  const keys = [
    'schemaVersion', 'id', 'contentHash', 'serviceId', 'languageIds', 'fileGlobs',
    'protocolVersion', 'runtime', 'serverInfo', 'initializationParametersHash',
    'workspaceConfigurationHash', 'requireVersionedDiagnostics', 'methods',
    'capabilityHash', 'limits', 'isolation',
  ] as const
  const source = exactRecord(value, keys)
  if (source.schemaVersion !== LSP_LANGUAGE_SERVER_PROFILE_SCHEMA_VERSION ||
    source.protocolVersion !== '3.17' || source.requireVersionedDiagnostics !== true ||
    source.initializationParametersHash !== LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH ||
    source.workspaceConfigurationHash !== LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH) malformed()
  const fileGlobs = sortedStrings(source.fileGlobs, 1, 64, (entry) => {
    const result = exactString(entry, undefined, 400)
    if (!validFileGlob(result)) malformed()
    return result
  })
  if (new Set(fileGlobs.map((entry) => entry.toLowerCase())).size !== fileGlobs.length) malformed()
  const methods = sortedStrings(source.methods, 1, 32, (entry) => {
    const result = exactString(entry, undefined, 100)
    if (!BASELINE_METHODS.has(result)) malformed()
    return result
  })
  return {
    schemaVersion: LSP_LANGUAGE_SERVER_PROFILE_SCHEMA_VERSION,
    id: exactString(source.id, PROFILE_ID_PATTERN, 80),
    contentHash: digest(source.contentHash),
    serviceId: exactString(source.serviceId, PROFILE_ID_PATTERN, 80),
    languageIds: sortedStrings(
      source.languageIds,
      1,
      32,
      (entry) => exactString(entry, LANGUAGE_ID_PATTERN, 64),
    ),
    fileGlobs,
    protocolVersion: '3.17',
    runtime: parseRuntime(source.runtime),
    serverInfo: parseServerInfo(source.serverInfo),
    initializationParametersHash: LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH,
    workspaceConfigurationHash: LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH,
    requireVersionedDiagnostics: true,
    methods,
    capabilityHash: digest(source.capabilityHash),
    limits: parseLimits(source.limits),
    isolation: parseIsolation(source.isolation),
  }
}

function parseProfiles(value: unknown): LSPProfileIdentityDto[] {
  if (!Array.isArray(value) || value.length !== 1) malformed()
  const result = value.map(parseProfile)
  for (let index = 1; index < result.length; index += 1) {
    if (result[index - 1]!.id >= result[index]!.id) malformed()
  }
  return result
}

function goJSONString(value: string) {
  return JSON.stringify(value).replace(/[<>&\u2028\u2029]/gu, (character) => {
    const code = character.codePointAt(0)!
    return `\\u${code.toString(16).padStart(4, '0')}`
  })
}

function canonicalJSON(value: unknown): string {
  if (value === null) return 'null'
  if (typeof value === 'string') return goJSONString(value)
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) malformed()
    return String(value)
  }
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(',')}]`
  if (isRecord(value)) {
    const keys = Object.keys(value).sort(utf8Compare)
    return `{${keys.map((key) => `${goJSONString(key)}:${canonicalJSON(value[key])}`).join(',')}}`
  }
  return malformed()
}

async function sha256(value: string) {
  try {
    return sha256DigestString(await sha256Bytes(new TextEncoder().encode(value)))
  } catch {
    throw new SandboxLSPError('lsp_crypto_unavailable')
  }
}

export function computeLanguageServerCapabilityHash(methods: readonly string[]) {
  return sha256(canonicalJSON({
    schemaVersion: LSP_LANGUAGE_SERVER_CAPABILITY_SCHEMA_VERSION,
    methods,
  }))
}

export function computeLanguageServerProfileContentHash(
  profile: LSPProfileIdentityDto | LSPTemplateProfileDto,
) {
  return sha256(canonicalJSON({
    schemaVersion: profile.schemaVersion,
    id: profile.id,
    serviceId: profile.serviceId,
    languageIds: profile.languageIds,
    fileGlobs: profile.fileGlobs,
    protocolVersion: profile.protocolVersion,
    runtime: profile.runtime,
    serverInfo: profile.serverInfo,
    initializationParametersHash: profile.initializationParametersHash,
    workspaceConfigurationHash: profile.workspaceConfigurationHash,
    requireVersionedDiagnostics: profile.requireVersionedDiagnostics,
    methods: profile.methods,
    capabilityHash: profile.capabilityHash,
    limits: profile.limits,
    isolation: profile.isolation,
  }))
}

async function verifyProfile(profile: LSPProfileIdentityDto) {
  const capabilityHash = await computeLanguageServerCapabilityHash(profile.methods)
  if (capabilityHash !== profile.capabilityHash) malformed()
  const contentHash = await computeLanguageServerProfileContentHash(profile)
  if (contentHash !== profile.contentHash) malformed()
}

/** Strict LSP-only projection of an authenticated TemplateRelease detail. */
export async function decodeLSPTemplateProfileDiscovery(
  encoded: string,
  expectedRelease: ExactTemplateReleaseDto,
): Promise<LSPTemplateProfileDiscoveryDto> {
  const registration = exactOptionalRecord(
    parseStrictLSPJSON(encoded, 2 << 20, 20),
    ['release', 'policy'],
    ['authorityReceipt'],
  )
  const release = exactOptionalRecord(registration.release, [
    'id', 'schemaVersion', 'admissionAttemptId', 'source', 'manifest', 'sbomDigest',
    'licenseExpression', 'licenseDigest', 'evidenceRefs', 'signature', 'subjectHash',
    'contentHash', 'approvedBy', 'approvedAt',
  ], ['authorityReceipt'])
  const policy = exactOptionalRecord(registration.policy, [
    'schemaVersion', 'templateReleaseId', 'releaseContentHash', 'state', 'version',
    'reason', 'updatedBy', 'createdAt', 'updatedAt',
  ], ['authorityReceipt'])
  const releaseID = canonicalUUID(release.id)
  const contentHash = digest(release.contentHash)
  if (releaseID !== expectedRelease.id || contentHash !== expectedRelease.contentHash ||
    canonicalUUID(policy.templateReleaseId) !== releaseID ||
    digest(policy.releaseContentHash) !== contentHash || policy.state !== 'approved' ||
    exactInteger(policy.version, 1) < 1) {
    throw new SandboxLSPError('lsp_connection_identity_mismatch')
  }
  const releaseSchema = exactString(release.schemaVersion, undefined, 80)
  const policySchema = exactString(policy.schemaVersion, undefined, 80)
  if ((releaseSchema !== 'template-release/v1' && releaseSchema !== 'template-release/v2') ||
    (policySchema !== 'template-release-policy/v1' &&
      policySchema !== 'template-release-policy/v2') ||
    releaseSchema.endsWith('/v1') !== policySchema.endsWith('/v1')) malformed()
  exactString(release.approvedBy, undefined, 200)
  parseTimestamp(release.approvedAt, false)
  parseTimestamp(policy.createdAt, false)
  parseTimestamp(policy.updatedAt, false)
  const manifest = exactOptionalRecord(release.manifest, [
    'schemaVersion', 'templateId', 'displayName', 'version', 'services', 'toolchains',
    'commands', 'ports', 'healthChecks', 'buildOutputs', 'extensionPaths',
    'protectedPaths', 'environmentSchema', 'lockfiles', 'profileDigest',
  ], ['migration', 'languageServers'])
  if (manifest.schemaVersion !== 'template-manifest/v1' || !Array.isArray(manifest.services) ||
    manifest.services.length === 0 || manifest.services.length > 16) malformed()
  const serviceIDs = manifest.services.map((value) => {
    const service = exactRecord(value, ['id', 'kind', 'rootPath'])
    const id = exactString(service.id, PROFILE_ID_PATTERN, 80)
    if (service.kind !== 'web' && service.kind !== 'api' && service.kind !== 'worker') malformed()
    exactString(service.rootPath, undefined, 512)
    return id
  })
  for (let index = 1; index < serviceIDs.length; index += 1) {
    if (utf8Compare(serviceIDs[index - 1]!, serviceIDs[index]!) >= 0) malformed()
  }
  const rawProfiles = Object.hasOwn(manifest, 'languageServers') ? manifest.languageServers : []
  if (!Array.isArray(rawProfiles) || rawProfiles.length > 16) malformed()
  const profiles = rawProfiles.map(parseTemplateProfile)
  for (let index = 1; index < profiles.length; index += 1) {
    if (utf8Compare(profiles[index - 1]!.id, profiles[index]!.id) >= 0) malformed()
  }
  await Promise.all(profiles.map(async (profile) => {
    if (!serviceIDs.includes(profile.serviceId) ||
      await computeLanguageServerCapabilityHash(profile.methods) !== profile.capabilityHash ||
      await computeLanguageServerProfileContentHash(profile) !== profile.contentHash) malformed()
  }))
  return frozen({
    templateRelease: { id: releaseID, contentHash },
    profiles,
  })
}

function matchLSPGlobSegment(pattern: string, value: string) {
  const escaped = pattern.replace(/[|\\{}()[\]^$+?.-]/gu, '\\$&').replace(/\*/gu, '.*')
  return new RegExp(`^${escaped}$`, 'u').test(value)
}

export function lspTemplateProfileSupportsPath(
  profile: LSPTemplateProfileDto,
  repositoryPath: string,
) {
  if (!canonicalRelativePath(repositoryPath)) return false
  const value = repositoryPath.split('/')
  return profile.fileGlobs.some((glob) => {
    const pattern = glob.split('/')
    const memo = new Map<string, boolean>()
    const visit = (patternIndex: number, valueIndex: number): boolean => {
      const key = `${patternIndex}:${valueIndex}`
      const known = memo.get(key)
      if (known !== undefined) return known
      let matched = false
      if (patternIndex === pattern.length) matched = valueIndex === value.length
      else if (pattern[patternIndex] === '**') {
        matched = visit(patternIndex + 1, valueIndex) ||
          (valueIndex < value.length && visit(patternIndex, valueIndex + 1))
      } else if (valueIndex < value.length) {
        matched = matchLSPGlobSegment(pattern[patternIndex]!, value[valueIndex]!) &&
          visit(patternIndex + 1, valueIndex + 1)
      }
      memo.set(key, matched)
      return matched
    }
    return visit(0, 0)
  })
}

function parseTimestamp(value: unknown, mustBeFuture: boolean) {
  const result = exactString(value, RFC3339_UTC_PATTERN, 40)
  const timestamp = Date.parse(result)
  if (!Number.isFinite(timestamp) || (mustBeFuture && timestamp <= Date.now())) malformed()
  return result
}

export async function decodeLSPTicketResponse(
  encoded: string,
  expectedRequest: LSPTicketRequestDto,
): Promise<LSPTicketDto> {
  const expected = normalizeLSPTicketRequest(expectedRequest)
  const source = exactRecord(parseStrictLSPJSON(encoded, 512 << 10, 12), [
    'schemaVersion', 'id', 'ticket', 'webSocketPath', 'subprotocol', 'mode',
    'sandboxHeadFence', 'templateRelease', 'profiles', 'expiresAt',
  ])
  if (source.schemaVersion !== LSP_TICKET_SCHEMA_VERSION ||
    source.webSocketPath !== LSP_WEB_SOCKET_PATH ||
    source.subprotocol !== LSP_WEB_SOCKET_SUBPROTOCOL) malformed()
  const ticket = exactString(source.ticket, TICKET_PATTERN, 43)
  const profiles = parseProfiles(source.profiles)
  const result: LSPTicketDto = {
    schemaVersion: LSP_TICKET_SCHEMA_VERSION,
    id: canonicalUUID(source.id),
    ticket,
    webSocketPath: LSP_WEB_SOCKET_PATH,
    subprotocol: LSP_WEB_SOCKET_SUBPROTOCOL,
    mode: parseMode(source.mode),
    sandboxHeadFence: parseHead(source.sandboxHeadFence),
    templateRelease: parseRelease(source.templateRelease),
    profiles,
    expiresAt: parseTimestamp(source.expiresAt, true),
  }
  if (Date.parse(result.expiresAt) - Date.now() > 60_000) malformed()
  if (result.mode !== expected.mode || !sameValue(result.sandboxHeadFence, expected.sandboxHeadFence) ||
    !sameValue(result.templateRelease, expected.templateRelease) ||
    profiles.length !== expected.profileIds.length ||
    profiles.some((profile, index) => profile.id !== expected.profileIds[index] ||
      !sameValue(profile.templateRelease, expected.templateRelease))) {
    throw new SandboxLSPError('lsp_ticket_scope_mismatch')
  }
  await Promise.all(profiles.map(verifyProfile))
  return frozen(result)
}

function minimumLimits(profiles: readonly LSPProfileIdentityDto[]): LanguageServerLimitsDto {
  const result = { ...profiles[0]!.effectiveLimits } as Record<(typeof LIMIT_KEYS)[number], number>
  for (const profile of profiles.slice(1)) {
    for (const key of LIMIT_KEYS) result[key] = Math.min(result[key], profile.effectiveLimits[key])
  }
  return result as unknown as LanguageServerLimitsDto
}

export function decodeLSPConnectionHello(
  encoded: string,
  ticket: LSPTicketHandshakeScopeDto,
): LSPConnectionHelloDto {
  const source = exactRecord(parseStrictLSPJSON(encoded, 512 << 10, 12), [
    'schemaVersion', 'kind', 'connectionId', 'ticketId', 'sequence', 'sandboxHeadFence',
    'templateRelease', 'profiles', 'limits', 'bindDeadlineAt',
  ])
  if (source.schemaVersion !== LSP_CONNECTION_SCHEMA_VERSION || source.kind !== 'server.hello' ||
    source.sequence !== 0) malformed()
  const result: LSPConnectionHelloDto = {
    schemaVersion: LSP_CONNECTION_SCHEMA_VERSION,
    kind: 'server.hello',
    connectionId: canonicalUUID(source.connectionId),
    ticketId: canonicalUUID(source.ticketId),
    sequence: 0,
    sandboxHeadFence: parseHead(source.sandboxHeadFence),
    templateRelease: parseRelease(source.templateRelease),
    profiles: parseProfiles(source.profiles),
    limits: parseLimits(source.limits),
    bindDeadlineAt: parseTimestamp(source.bindDeadlineAt, true),
  }
  if (result.ticketId !== ticket.id ||
    !sameValue(result.sandboxHeadFence, ticket.sandboxHeadFence) ||
    !sameValue(result.templateRelease, ticket.templateRelease) ||
    !sameValue(result.profiles, ticket.profiles) ||
    !sameValue(result.limits, minimumLimits(ticket.profiles))) {
    throw new SandboxLSPError('lsp_connection_identity_mismatch')
  }
  return frozen(result)
}

function goPathEscape(segment: string) {
  try {
    return encodeURIComponent(segment)
      .replace(/[!'()*]/gu, (character) => `%${character.charCodeAt(0).toString(16).toUpperCase()}`)
      .replace(/%(?:24|26|2B|3A|3D|40)/gu, (escape) => String.fromCharCode(Number.parseInt(escape.slice(1), 16)))
  } catch {
    return malformed()
  }
}

function candidateURI(projectId: string, candidateId: string, path?: string) {
  canonicalUUID(projectId)
  canonicalUUID(candidateId)
  if (path === undefined) return `worksflow-candidate://${projectId}/${candidateId}`
  if (!canonicalRelativePath(path)) malformed()
  return `worksflow-candidate://${projectId}/${candidateId}/${path.split('/').map(goPathEscape).join('/')}`
}

/** Candidate workspace root; it is never a valid Monaco document URI. */
export function candidateWorkspaceURI(projectId: string, candidateId: string) {
  return candidateURI(projectId, candidateId)
}

export function parseCandidateWorkspaceURI(value: unknown) {
  const uri = exactString(value, undefined, 1_024)
  const match = /^worksflow-candidate:\/\/([0-9a-f-]+)\/([0-9a-f-]+)$/u.exec(uri)
  if (!match) malformed()
  const projectId = canonicalUUID(match[1])
  const candidateId = canonicalUUID(match[2])
  if (candidateWorkspaceURI(projectId, candidateId) !== uri) malformed()
  return frozen({ projectId, candidateId })
}

/** Candidate file URI. A non-empty canonical repository path is mandatory. */
export function candidateDocumentURI(projectId: string, candidateId: string, path: string) {
  return candidateURI(projectId, candidateId, path)
}

export function parseCandidateDocumentURI(value: unknown) {
  const uri = exactString(value, undefined, 1_024)
  const prefix = 'worksflow-candidate://'
  if (!uri.startsWith(prefix) || uri.includes('?') || uri.includes('#')) malformed()
  const segments = uri.slice(prefix.length).split('/')
  if (segments.length < 3) malformed()
  const projectId = canonicalUUID(segments[0])
  const candidateId = canonicalUUID(segments[1])
  const decoded = segments.slice(2).map((segment) => {
    if (!segment) return malformed()
    let value: string
    try {
      value = decodeURIComponent(segment)
    } catch {
      return malformed()
    }
    if (!value || value.includes('/') || value.includes('\\') || goPathEscape(value) !== segment) malformed()
    return value
  })
  const path = decoded.join('/')
  if (!canonicalRelativePath(path) || candidateDocumentURI(projectId, candidateId, path) !== uri) malformed()
  return frozen({ projectId, candidateId, path })
}

function parseDocument(value: unknown, head: SandboxHeadFenceDto): LSPDocumentFenceDto {
  const source = exactRecord(value, ['modelUri', 'openId', 'modelVersion', 'savedContentHash'])
  const identity = parseCandidateDocumentURI(source.modelUri)
  if (identity.projectId !== head.projectId || identity.candidateId !== head.candidateId) {
    throw new SandboxLSPError('lsp_binding_stale')
  }
  return {
    modelUri: source.modelUri as string,
    openId: canonicalUUID(source.openId),
    modelVersion: exactInteger(source.modelVersion, 1),
    savedContentHash: digest(source.savedContentHash),
  }
}

export function createLSPClientBind(
  ticket: LSPTicketHandshakeScopeDto,
  hello: LSPConnectionHelloDto,
  profileId: string,
  documents: readonly LSPDocumentFenceDto[],
): LSPClientBindDto {
  if (hello.ticketId !== ticket.id || !sameValue(hello.profiles, ticket.profiles) ||
    !sameValue(hello.sandboxHeadFence, ticket.sandboxHeadFence)) {
    throw new SandboxLSPError('lsp_connection_identity_mismatch')
  }
  const profile = ticket.profiles.find((entry) => entry.id === profileId)
  if (!profile || !Array.isArray(documents) || documents.length === 0 ||
    documents.length > profile.effectiveLimits.maxOpenDocuments) {
    throw new SandboxLSPError('lsp_binding_stale')
  }
  const parsed = documents.map((document) => parseDocument(document, ticket.sandboxHeadFence))
  for (let index = 1; index < parsed.length; index += 1) {
    if (parsed[index - 1]!.modelUri >= parsed[index]!.modelUri) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
  }
  return frozen({
    schemaVersion: LSP_BINDING_SCHEMA_VERSION,
    kind: 'client.bind',
    connectionId: hello.connectionId,
    bindingId: null,
    sequence: 1,
    sandboxHeadFence: ticket.sandboxHeadFence,
    languageServerProfile: profile,
    documents: parsed,
  })
}

export function normalizeLSPHeadFence(value: unknown): SandboxHeadFenceDto {
  return frozen(parseHead(value))
}

export function normalizeLSPDocumentFence(
  value: unknown,
  head: SandboxHeadFenceDto,
): LSPDocumentFenceDto {
  return frozen(parseDocument(value, parseHead(head)))
}

export function sameLSPHeadFence(
  left: SandboxHeadFenceDto,
  right: SandboxHeadFenceDto,
) {
  return sameValue(left, right)
}

export function sameLSPDocumentFence(
  left: LSPDocumentFenceDto,
  right: LSPDocumentFenceDto,
) {
  return sameValue(left, right)
}

export function isMonotonicLSPHeadSuccessor(
  current: SandboxHeadFenceDto,
  next: SandboxHeadFenceDto,
) {
  const left = parseHead(current)
  const right = parseHead(next)
  return left.projectId === right.projectId && left.sessionId === right.sessionId &&
    left.sessionEpoch === right.sessionEpoch && left.candidateId === right.candidateId &&
    left.writerLeaseEpoch === right.writerLeaseEpoch && right.version > left.version &&
    right.journalSequence > left.journalSequence && right.treeHash !== left.treeHash
}

function parseDocumentArray(
  value: unknown,
  head: SandboxHeadFenceDto,
  maximum: number,
  requireNonEmpty: boolean,
) {
  if (!Array.isArray(value) || (requireNonEmpty && value.length === 0) || value.length > maximum) {
    malformed()
  }
  const result = value.map((entry) => parseDocument(entry, head))
  for (let index = 1; index < result.length; index += 1) {
    if (utf8Compare(result[index - 1]!.modelUri, result[index]!.modelUri) >= 0) malformed()
  }
  return result
}

function sameDocumentArray(
  left: readonly LSPDocumentFenceDto[],
  right: readonly LSPDocumentFenceDto[],
) {
  return left.length === right.length && left.every((entry, index) =>
    sameLSPDocumentFence(entry, right[index]!))
}

/** Strictly verifies the independent binding fact emitted after initialize. */
export async function decodeLSPServerBound(
  encoded: string,
  hello: LSPConnectionHelloDto,
  bind: LSPClientBindDto,
): Promise<LSPServerBoundDto> {
  const profile = bind.languageServerProfile
  const source = exactRecord(parseStrictLSPJSON(
    encoded,
    profile.effectiveLimits.maxFrameBytes,
    14,
  ), [
    'schemaVersion', 'kind', 'connectionId', 'bindingId', 'sequence',
    'sandboxHeadFence', 'languageServer', 'documents', 'effectiveCapabilities', 'limits',
  ])
  if (source.schemaVersion !== LSP_BINDING_SCHEMA_VERSION || source.kind !== 'server.bound' ||
    source.sequence !== 1) malformed()
  const connectionId = canonicalUUID(source.connectionId)
  const bindingId = canonicalUUID(source.bindingId)
  if (connectionId !== hello.connectionId || connectionId !== bind.connectionId ||
    bindingId === connectionId) {
    throw new SandboxLSPError('lsp_connection_identity_mismatch')
  }
  const head = parseHead(source.sandboxHeadFence)
  if (!sameLSPHeadFence(head, bind.sandboxHeadFence)) {
    throw new SandboxLSPError('lsp_binding_stale')
  }
  const identitySource = exactRecord(source.languageServer, [
    'profileId', 'profileContentHash', 'runtimeImageDigest', 'executableDigest',
    'serverName', 'serverVersion', 'capabilityAllowlistHash',
  ])
  const languageServer: LSPBoundLanguageServerDto = {
    profileId: exactString(identitySource.profileId, PROFILE_ID_PATTERN, 80),
    profileContentHash: digest(identitySource.profileContentHash),
    runtimeImageDigest: exactString(identitySource.runtimeImageDigest, OCI_IMAGE_PATTERN, 500),
    executableDigest: digest(identitySource.executableDigest),
    serverName: exactString(identitySource.serverName, undefined, 160),
    serverVersion: exactString(identitySource.serverVersion, undefined, 120),
    capabilityAllowlistHash: digest(identitySource.capabilityAllowlistHash),
  }
  const effectiveCapabilities = sortedStrings(source.effectiveCapabilities, 1, 32, (entry) => {
    const method = exactString(entry, undefined, 128)
    if (!BASELINE_METHODS.has(method)) malformed()
    return method
  })
  if (languageServer.profileId !== profile.id ||
    languageServer.profileContentHash !== profile.contentHash ||
    languageServer.runtimeImageDigest !== profile.runtime.image ||
    languageServer.executableDigest !== profile.runtime.executableDigest ||
    languageServer.serverName !== profile.serverInfo.name ||
    languageServer.serverVersion !== profile.serverInfo.version ||
    effectiveCapabilities.some((method) => !profile.methods.includes(method)) ||
    languageServer.capabilityAllowlistHash !==
      await computeLanguageServerCapabilityHash(effectiveCapabilities)) {
    throw new SandboxLSPError('lsp_connection_identity_mismatch')
  }
  const documents = parseDocumentArray(
    source.documents,
    head,
    profile.effectiveLimits.maxOpenDocuments,
    true,
  )
  if (!sameDocumentArray(documents, bind.documents)) {
    throw new SandboxLSPError('lsp_binding_stale')
  }
  const limits = parseLimits(source.limits)
  if (!sameValue(limits, profile.effectiveLimits)) malformed()
  return frozen({
    schemaVersion: LSP_BINDING_SCHEMA_VERSION,
    kind: 'server.bound',
    connectionId,
    bindingId,
    sequence: 1,
    sandboxHeadFence: head,
    languageServer,
    documents,
    effectiveCapabilities,
    limits,
  })
}

const BROWSER_REQUEST_METHODS = new Set([
  'textDocument/completion',
  'textDocument/declaration',
  'textDocument/definition',
  'textDocument/documentHighlight',
  'textDocument/documentSymbol',
  'textDocument/hover',
  'textDocument/implementation',
  'textDocument/references',
  'textDocument/signatureHelp',
  'textDocument/typeDefinition',
])

export function isLSPBrowserRequestMethod(value: string) {
  return BROWSER_REQUEST_METHODS.has(value)
}

function parseBrowserTextDocument(value: unknown, document: LSPDocumentFenceDto) {
  const source = exactRecord(value, ['uri'])
  if (source.uri !== document.modelUri) throw new SandboxLSPError('lsp_binding_stale')
  return { uri: document.modelUri }
}

function parseCompletionContext(value: unknown) {
  const source = exactOptionalRecord(value, ['triggerKind'], ['triggerCharacter'])
  const triggerKind = exactInteger(source.triggerKind, 1, 3)
  const requiresCharacter = triggerKind === 2
  if (Object.hasOwn(source, 'triggerCharacter') !== requiresCharacter) malformed()
  return requiresCharacter
    ? { triggerKind, triggerCharacter: exactAtom(source.triggerCharacter, 16) }
    : { triggerKind }
}

function parseSignatureContext(value: unknown) {
  const source = exactOptionalRecord(
    value,
    ['triggerKind', 'isRetrigger'],
    ['triggerCharacter'],
  )
  const triggerKind = exactInteger(source.triggerKind, 1, 3)
  const requiresCharacter = triggerKind === 2
  if (Object.hasOwn(source, 'triggerCharacter') !== requiresCharacter ||
    typeof source.isRetrigger !== 'boolean') malformed()
  return requiresCharacter
    ? {
        triggerKind,
        triggerCharacter: exactAtom(source.triggerCharacter, 16),
        isRetrigger: source.isRetrigger,
      }
    : { triggerKind, isRetrigger: source.isRetrigger }
}

/** Mirrors the Gateway's method-specific, additionalProperties=false request DTOs. */
export function normalizeLSPBrowserRequestParams(
  method: string,
  value: unknown,
  head: SandboxHeadFenceDto,
  document: LSPDocumentFenceDto,
): unknown {
  if (!BROWSER_REQUEST_METHODS.has(method)) malformed()
  const normalizedDocument = normalizeLSPDocumentFence(document, head)
  if (method === 'textDocument/documentSymbol') {
    const source = exactRecord(value, ['textDocument'])
    return frozen({ textDocument: parseBrowserTextDocument(source.textDocument, normalizedDocument) })
  }
  const required = method === 'textDocument/references' || method === 'textDocument/completion' ||
    method === 'textDocument/signatureHelp'
    ? ['textDocument', 'position', 'context']
    : ['textDocument', 'position']
  const source = exactRecord(value, required)
  const result: Record<string, unknown> = {
    textDocument: parseBrowserTextDocument(source.textDocument, normalizedDocument),
    position: parsePosition(source.position),
  }
  if (method === 'textDocument/references') {
    const context = exactRecord(source.context, ['includeDeclaration'])
    if (typeof context.includeDeclaration !== 'boolean') malformed()
    result.context = { includeDeclaration: context.includeDeclaration }
  } else if (method === 'textDocument/completion') {
    result.context = parseCompletionContext(source.context)
  } else if (method === 'textDocument/signatureHelp') {
    result.context = parseSignatureContext(source.context)
  }
  return frozen(result)
}

function exactSignedInteger(
  value: unknown,
  minimum = -MAX_SAFE_WIRE_INTEGER,
  maximum = MAX_SAFE_WIRE_INTEGER,
) {
  if (!Number.isSafeInteger(value) || Object.is(value, -0) ||
    (value as number) < minimum || (value as number) > maximum) malformed()
  return value as number
}

function optionalUUID(value: unknown): string | null {
  return value === null ? null : canonicalUUID(value)
}

function exactAtom(value: unknown, maximum: number) {
  return exactString(value, undefined, maximum)
}

function exactMessage(value: unknown, maximum: number) {
  if (typeof value !== 'string' || value.length === 0 || value.length > maximum ||
    value.includes('\0')) malformed()
  return value
}

function parsePosition(value: unknown): LSPPositionDto {
  const source = exactRecord(value, ['line', 'character'])
  return {
    line: exactInteger(source.line, 0, 2_147_483_647),
    character: exactInteger(source.character, 0, 2_147_483_647),
  }
}

function parseRange(value: unknown): LSPRangeDto {
  const source = exactRecord(value, ['start', 'end'])
  return { start: parsePosition(source.start), end: parsePosition(source.end) }
}

function parseDiagnostic(value: unknown): LSPDiagnosticDto {
  const source = exactOptionalRecord(
    value,
    ['range', 'message'],
    ['severity', 'code', 'source', 'tags'],
  )
  const result: {
    range: LSPRangeDto
    message: string
    severity?: 1 | 2 | 3 | 4
    code?: string | number
    source?: string
    tags?: (1 | 2)[]
  } = {
    range: parseRange(source.range),
    message: exactMessage(source.message, 4 << 10),
  }
  if (Object.hasOwn(source, 'severity')) {
    result.severity = exactInteger(source.severity, 1, 4) as 1 | 2 | 3 | 4
  }
  if (Object.hasOwn(source, 'code')) {
    if (typeof source.code === 'string') result.code = exactAtom(source.code, 256)
    else result.code = exactSignedInteger(source.code)
  }
  if (Object.hasOwn(source, 'source')) result.source = exactAtom(source.source, 256)
  if (Object.hasOwn(source, 'tags')) {
    if (!Array.isArray(source.tags) || source.tags.length > 2) malformed()
    const tags = source.tags.map((tag) => exactInteger(tag, 1, 2) as 1 | 2)
    if (new Set(tags).size !== tags.length) malformed()
    result.tags = tags
  }
  return result
}

function parseDiagnostics(
  value: unknown,
  document: LSPDocumentFenceDto,
  limits: LanguageServerLimitsDto,
): LSPPublishDiagnosticsDto {
  const source = exactRecord(value, ['uri', 'version', 'diagnostics'])
  if (source.uri !== document.modelUri || source.version !== document.modelVersion ||
    !Array.isArray(source.diagnostics) ||
    source.diagnostics.length > limits.maxDiagnosticsPerDocument) {
    throw new SandboxLSPError('lsp_binding_stale')
  }
  return {
    uri: document.modelUri,
    version: document.modelVersion,
    diagnostics: source.diagnostics.map(parseDiagnostic),
  }
}

function findRequestExpectation(
  value: string,
  requests: readonly LSPEnvelopeRequestExpectation[],
) {
  return requests.find((entry) => entry.messageId === value)
}

/**
 * Decodes one post-binding Gateway frame against the exact browser state.
 * The caller advances sequence/pending state only after this function returns.
 */
export function decodeLSPServerEnvelope(
  encoded: string,
  expected: LSPServerEnvelopeExpectation,
): LSPServerEnvelopeDto {
  const source = exactRecord(parseStrictLSPJSON(
    encoded,
    expected.limits.maxFrameBytes,
    16,
  ), [
    'schemaVersion', 'connectionId', 'bindingId', 'sequence', 'messageId',
    'replyTo', 'kind', 'method', 'sandboxHeadFence', 'documentFence', 'payload',
  ])
  if (source.schemaVersion !== LSP_ENVELOPE_SCHEMA_VERSION) malformed()
  const connectionId = canonicalUUID(source.connectionId)
  const bindingId = canonicalUUID(source.bindingId)
  const sequence = exactInteger(source.sequence, 2)
  const messageId = canonicalUUID(source.messageId)
  const replyTo = optionalUUID(source.replyTo)
  const kind = exactAtom(source.kind, 64) as LSPServerEnvelopeKind
  const method = exactAtom(source.method, 256)
  if (connectionId !== expected.connectionId || bindingId !== expected.bindingId ||
    bindingId === connectionId || sequence !== expected.sequence ||
    messageId === connectionId || messageId === bindingId || messageId === replyTo ||
    expected.seenMessageIds.has(messageId)) {
    throw new SandboxLSPError('lsp_binding_stale')
  }
  const head = parseHead(source.sandboxHeadFence)
  let document: LSPDocumentFenceDto | null = null
  if (source.documentFence !== null) document = parseDocument(source.documentFence, head)

  let payload: LSPServerEnvelopeDto['payload']
  switch (kind) {
    case 'server.response': {
      if (replyTo === null || !BROWSER_REQUEST_METHODS.has(method)) malformed()
      const request = findRequestExpectation(replyTo, expected.pendingRequests)
      if (!request || !sameLSPHeadFence(head, request.sandboxHeadFence) || !document ||
        !sameLSPDocumentFence(document, request.documentFence) || request.method !== method) {
        throw new SandboxLSPError('lsp_binding_stale')
      }
      const response = exactRecord(source.payload, ['status', 'result', 'error'])
      if (response.status === 'ok' && response.error === null) {
        payload = { status: 'ok', result: response.result, error: null }
      } else if (response.status === 'error' && response.result === null) {
        const error = exactRecord(response.error, ['code', 'message'])
        const code = exactSignedInteger(error.code, -2_147_483_648, 2_147_483_647)
        if (code === 0) malformed()
        payload = {
          status: 'error',
          result: null,
          error: {
            code,
            message: exactMessage(error.message, 4 << 10),
          },
        }
      } else malformed()
      break
    }
    case 'server.stale': {
      if (replyTo === null || !BROWSER_REQUEST_METHODS.has(method)) malformed()
      const request = findRequestExpectation(replyTo, [
        ...expected.pendingRequests,
        ...expected.staleRequests,
      ])
      if (!request || request.method !== method || !document ||
        !sameLSPHeadFence(head, request.sandboxHeadFence) ||
        !sameLSPDocumentFence(document, request.documentFence)) {
        throw new SandboxLSPError('lsp_binding_stale')
      }
      const stale = exactRecord(source.payload, ['code'])
      payload = { code: exactAtom(stale.code, 128) }
      break
    }
    case 'server.diagnostics': {
      if (replyTo !== null || method !== 'textDocument/publishDiagnostics' || !document ||
        !sameLSPHeadFence(head, expected.sandboxHeadFence) ||
        !expected.documents.some((entry) => sameLSPDocumentFence(entry, document!))) {
        throw new SandboxLSPError('lsp_binding_stale')
      }
      const diagnosticsPayload = exactRecord(source.payload, ['diagnostics'])
      payload = { diagnostics: parseDiagnostics(diagnosticsPayload.diagnostics, document, expected.limits) }
      break
    }
    case 'server.error': {
      if (replyTo !== null || method !== 'worksflow/error' || document !== null ||
        !sameLSPHeadFence(head, expected.sandboxHeadFence)) malformed()
      const error = exactRecord(source.payload, ['code', 'message'])
      payload = {
        code: exactAtom(error.code, 128),
        message: exactMessage(error.message, 4 << 10),
      }
      break
    }
    case 'server.pong': {
      if (replyTo === null || method !== 'worksflow/pong' || document !== null) malformed()
      const ping = expected.pendingPings.find((entry) => entry.messageId === replyTo)
      const pong = exactRecord(source.payload, ['nonce'])
      if (!ping || !sameLSPHeadFence(head, ping.sandboxHeadFence) ||
        pong.nonce !== ping.nonce) throw new SandboxLSPError('lsp_binding_stale')
      payload = { nonce: exactAtom(pong.nonce, 128) }
      break
    }
    default:
      return malformed()
  }
  return frozen({
    schemaVersion: LSP_ENVELOPE_SCHEMA_VERSION,
    connectionId,
    bindingId,
    sequence,
    messageId,
    replyTo,
    kind,
    method,
    sandboxHeadFence: head,
    documentFence: document,
    payload,
  })
}
