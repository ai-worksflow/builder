import type { ClientMutationOptions, ClientRequestOptions } from './clients'
import {
  HttpClient,
  PlatformProtocolError,
  verifyResponseBodySha256,
  type HttpResult,
} from './http'
import type { ExactCandidateFileOpenFence } from './sandbox-file-open'
import { SandboxStreamConnection, type SandboxStreamOptions } from './sandbox-stream'
import {
  parseSandboxConnectionTicket,
  normalizeSandboxRepositoryView,
  normalizeSandboxCheckpointResult,
  normalizeSandboxCandidateFreezeResult,
  normalizeSandboxFileMutationResult,
  normalizeSandboxProcessList,
  normalizeSandboxProcessLogResult,
  normalizeSandboxProcessResult,
  normalizeSandboxPortList,
  normalizeSandboxPreviewLink,
  normalizeSandboxSession,
  normalizeSandboxSessionResult,
  normalizeSandboxTerminalList,
  normalizeSandboxTerminalResult,
  sandboxFences,
  SANDBOX_STREAM_CHANNELS,
  SandboxConnectionTicketContractError,
  SandboxContractError,
  type SandboxCheckpointResultDto,
  type SandboxCandidateFreezeResultDto,
  type SandboxConnectionCursorDto,
  type SandboxConnectionTicketDto,
  type SandboxFences,
  type SandboxFileMutationResultDto,
  type SandboxProcessListDto,
  type SandboxProcessLogResultDto,
  type SandboxProcessResultDto,
  type SandboxPortListDto,
  type SandboxPreviewLinkDto,
  type SandboxRepositoryViewDto,
  type SandboxSessionDto,
  type SandboxSessionResultDto,
  type SandboxStreamChannel,
  type SandboxTerminalListDto,
  type SandboxTerminalResultDto,
} from './sandbox-contract'

export interface SandboxMutationOptions extends ClientMutationOptions {
  readonly fences: SandboxFences
}

export interface SandboxControlOptions extends ClientMutationOptions {
  readonly fences: Pick<SandboxFences, 'etag' | 'sessionEpoch'>
}

export interface SandboxProcessControlOptions extends ClientMutationOptions {
  readonly sessionFences: Pick<SandboxFences, 'etag' | 'sessionEpoch'>
  readonly processEtag: string
}

export interface SandboxFileReadOptions extends ClientRequestOptions {
  readonly fence: ExactCandidateFileOpenFence
}

export interface SandboxFileResult {
  readonly value: ArrayBuffer
  readonly candidateId: string
  readonly journalSequence: number
  readonly contentHash: string
  readonly mode: string
  readonly etag?: string
  readonly fences: SandboxFences
}

function segment(value: string) {
  return encodeURIComponent(value)
}

function repositoryPath(value: string) {
  return value.split('/').map(segment).join('/')
}

function requestOptions(options?: ClientRequestOptions) {
  return { signal: options?.signal, requestId: options?.requestId }
}

function mutationOptions(options: SandboxMutationOptions, headers: HeadersInit = {}) {
  const fences = options.fences
  return {
    signal: options.signal,
    requestId: options.requestId,
    ifMatch: fences.etag,
    idempotencyKey: options.idempotencyKey ?? true,
    headers: {
      ...Object.fromEntries(new Headers(headers).entries()),
      'X-Sandbox-Session-Epoch': String(fences.sessionEpoch),
      'X-Candidate-Version': String(fences.candidateVersion),
      'X-Writer-Lease-Epoch': String(fences.writerLeaseEpoch),
    },
  }
}

function controlOptions(options: SandboxControlOptions) {
  return {
    signal: options.signal,
    requestId: options.requestId,
    ifMatch: options.fences.etag,
    idempotencyKey: options.idempotencyKey ?? true,
    headers: {
      'X-Sandbox-Session-Epoch': String(options.fences.sessionEpoch),
    },
  }
}

function connectionTicketScope(input: {
  readonly channels: readonly SandboxStreamChannel[]
  readonly cursors?: readonly SandboxConnectionCursorDto[]
}) {
  const requested = new Set<SandboxStreamChannel>()
  for (const channel of input.channels) {
    if (!SANDBOX_STREAM_CHANNELS.includes(channel) || requested.has(channel)) {
      throw new PlatformProtocolError('The sandbox connection ticket request has an invalid channel scope.')
    }
    requested.add(channel)
  }
  if (requested.size === 0) {
    throw new PlatformProtocolError('The sandbox connection ticket request requires at least one channel.')
  }
  const cursorByChannel = new Map<SandboxStreamChannel, number>()
  for (const cursor of input.cursors ?? []) {
    if (!requested.has(cursor.channel) || cursorByChannel.has(cursor.channel) ||
      !Number.isSafeInteger(cursor.lastAckedSeq) || cursor.lastAckedSeq < 0) {
      throw new PlatformProtocolError('The sandbox connection ticket request has an invalid replay cursor.')
    }
    cursorByChannel.set(cursor.channel, cursor.lastAckedSeq)
  }
  const channels = SANDBOX_STREAM_CHANNELS.filter((channel) => requested.has(channel))
  return {
    channels,
    cursors: channels.map((channel) => ({
      channel,
      lastAckedSeq: cursorByChannel.get(channel) ?? 0,
    })),
  }
}

function sameConnectionTicketScope(
  requested: ReturnType<typeof connectionTicketScope>,
  ticket: SandboxConnectionTicketDto,
) {
  return ticket.channels.length === requested.channels.length &&
    ticket.channels.every((channel, index) => channel === requested.channels[index]) &&
    ticket.cursors.length === requested.cursors.length &&
    ticket.cursors.every((cursor, index) => (
      cursor.channel === requested.cursors[index]?.channel &&
      cursor.lastAckedSeq === requested.cursors[index]?.lastAckedSeq
    ))
}

function normalizedSessionResult(result: HttpResult<unknown>): HttpResult<SandboxSessionDto> {
  return normalizeSandboxResult(result, normalizeSandboxSession)
}

function normalizeSandboxResult<T>(result: HttpResult<unknown>, normalize: (value: unknown) => T): HttpResult<T> {
  try {
    return { ...result, data: normalize(result.data) }
  } catch (cause) {
    if (!(cause instanceof SandboxContractError)) throw cause
    throw new PlatformProtocolError(cause.message, result.requestId, result.status)
  }
}

export class SandboxClient {
  constructor(
    private readonly http: HttpClient,
    private readonly streamOptions: SandboxStreamOptions = {},
  ) {}

  stream(
    sessionId: string,
    channels: readonly SandboxStreamChannel[],
    options: SandboxStreamOptions = {},
  ) {
    return new SandboxStreamConnection(
      sessionId,
      channels,
      this.http.baseUrl,
      (id, input, mutation) => this.createConnectionTicket(id, input, mutation),
      { ...this.streamOptions, ...options },
    )
  }

  async createSession(
    projectId: string,
    candidateId: string,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<SandboxSessionDto>> {
    const result = await this.http.post<unknown, { candidateId: string }>(
      `/v1/projects/${segment(projectId)}/sandbox-sessions`,
      { candidateId },
      {
        signal: options?.signal,
        requestId: options?.requestId,
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    return normalizedSessionResult(result)
  }

  async getSession(sessionId: string, options?: ClientRequestOptions) {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}`,
      requestOptions(options),
    )
    return normalizedSessionResult(result)
  }

  async suspendSession(
    sessionId: string,
    options: SandboxControlOptions,
  ): Promise<HttpResult<SandboxSessionDto>> {
    const result = await this.http.post<unknown, Record<string, never>>(
      `/v1/sandbox-sessions/${segment(sessionId)}:suspend`,
      {},
      controlOptions(options),
    )
    return normalizedSessionResult(result)
  }

  async resumeSession(
    sessionId: string,
    options: SandboxControlOptions,
  ): Promise<HttpResult<SandboxSessionDto>> {
    const result = await this.http.post<unknown, Record<string, never>>(
      `/v1/sandbox-sessions/${segment(sessionId)}:resume`,
      {},
      controlOptions(options),
    )
    return normalizedSessionResult(result)
  }

  async terminateSession(
    sessionId: string,
    reason: string,
    options: SandboxControlOptions,
  ): Promise<HttpResult<SandboxSessionDto>> {
    const result = await this.http.post<unknown, { reason: string }>(
      `/v1/sandbox-sessions/${segment(sessionId)}:terminate`,
      { reason },
      controlOptions(options),
    )
    return normalizedSessionResult(result)
  }

  async startProcess(
    sessionId: string,
    input: { readonly serviceId: string; readonly commandId: string },
    options: SandboxControlOptions,
  ): Promise<HttpResult<SandboxProcessResultDto>> {
    const result = await this.http.post<unknown, typeof input>(
      `/v1/sandbox-sessions/${segment(sessionId)}/processes`,
      input,
      controlOptions(options),
    )
    return { ...result, data: normalizeSandboxProcessResult(result.data) }
  }

  async listProcesses(
    sessionId: string,
    options?: ClientRequestOptions & { readonly limit?: number },
  ): Promise<HttpResult<SandboxProcessListDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/processes`,
      { ...requestOptions(options), query: options?.limit ? { limit: options.limit } : undefined },
    )
    return { ...result, data: normalizeSandboxProcessList(result.data) }
  }

  async getProcess(
    sessionId: string,
    processId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<SandboxProcessResultDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/processes/${segment(processId)}`,
      requestOptions(options),
    )
    return { ...result, data: normalizeSandboxProcessResult(result.data) }
  }

  async readProcessLogs(
    sessionId: string,
    processId: string,
    offset = 0,
    limit = 64 << 10,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<SandboxProcessLogResultDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/processes/${segment(processId)}/logs`,
      { ...requestOptions(options), query: { offset, limit } },
    )
    return { ...result, data: normalizeSandboxProcessLogResult(result.data) }
  }

  async signalProcess(
    sessionId: string,
    processId: string,
    signal: 'INT' | 'TERM' | 'KILL' | 'HUP',
    options: SandboxProcessControlOptions,
  ): Promise<HttpResult<SandboxProcessResultDto>> {
    const result = await this.http.post<unknown, { signal: typeof signal }>(
      `/v1/sandbox-sessions/${segment(sessionId)}/processes/${segment(processId)}:signal`,
      { signal },
      {
        signal: options.signal,
        requestId: options.requestId,
        ifMatch: options.processEtag,
        idempotencyKey: options.idempotencyKey ?? true,
        headers: {
          'X-Sandbox-Session-ETag': options.sessionFences.etag,
          'X-Sandbox-Session-Epoch': String(options.sessionFences.sessionEpoch),
        },
      },
    )
    return { ...result, data: normalizeSandboxProcessResult(result.data) }
  }

  async createTerminal(
    sessionId: string,
    input: { readonly workingDirectory: string; readonly rows: number; readonly columns: number },
    options: SandboxControlOptions,
  ): Promise<HttpResult<SandboxTerminalResultDto>> {
    const result = await this.http.post<unknown, typeof input>(
      `/v1/sandbox-sessions/${segment(sessionId)}/ptys`,
      input,
      controlOptions(options),
    )
    return { ...result, data: normalizeSandboxTerminalResult(result.data) }
  }

  async listTerminals(
    sessionId: string,
    options?: ClientRequestOptions & { readonly limit?: number },
  ): Promise<HttpResult<SandboxTerminalListDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/ptys`,
      { ...requestOptions(options), query: options?.limit ? { limit: options.limit } : undefined },
    )
    return { ...result, data: normalizeSandboxTerminalList(result.data) }
  }

  async getTerminal(
    sessionId: string,
    terminalId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<SandboxTerminalResultDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/ptys/${segment(terminalId)}`,
      requestOptions(options),
    )
    return { ...result, data: normalizeSandboxTerminalResult(result.data) }
  }

  async listPorts(
    sessionId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<SandboxPortListDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/ports`,
      requestOptions(options),
    )
    return { ...result, data: normalizeSandboxPortList(result.data) }
  }

  async createPreviewLink(
    sessionId: string,
    portName: string,
    options: SandboxControlOptions,
  ): Promise<HttpResult<SandboxPreviewLinkDto>> {
    const result = await this.http.post<unknown, Record<string, never>>(
      `/v1/sandbox-sessions/${segment(sessionId)}/ports/${segment(portName)}/preview-links`,
      {},
      controlOptions(options),
    )
    return { ...result, data: normalizeSandboxPreviewLink(result.data) }
  }

  async createConnectionTicket(
    sessionId: string,
    input: {
      readonly channels: readonly SandboxStreamChannel[]
      readonly cursors?: readonly SandboxConnectionCursorDto[]
    },
    options?: ClientMutationOptions,
  ): Promise<HttpResult<SandboxConnectionTicketDto>> {
    const scope = connectionTicketScope(input)
    const result = await this.http.post<unknown, typeof scope>(
      `/v1/sandbox-sessions/${segment(sessionId)}/connection-tickets`,
      scope,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    let data: SandboxConnectionTicketDto
    try {
      data = parseSandboxConnectionTicket(result.data)
    } catch (cause) {
      if (!(cause instanceof SandboxConnectionTicketContractError)) throw cause
      throw new PlatformProtocolError(cause.message, result.requestId, result.status)
    }
    if (data.sessionId !== sessionId || !sameConnectionTicketScope(scope, data)) {
      throw new PlatformProtocolError(
        'The sandbox service returned a connection ticket for a different session or replay scope.',
        result.requestId,
        result.status,
      )
    }
    return { ...result, data }
  }

  async getTree(sessionId: string, options?: ClientRequestOptions): Promise<HttpResult<SandboxRepositoryViewDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/tree`,
      requestOptions(options),
    )
    const normalized = normalizeSandboxResult(result, normalizeSandboxRepositoryView)
    if (normalized.data.session.id !== sessionId) {
      throw new PlatformProtocolError(
        'The Sandbox tree response belongs to a different Session.', result.requestId, result.status,
      )
    }
    try {
      sandboxFences(result.headers, normalized.data.session)
    } catch (cause) {
      if (!(cause instanceof SandboxContractError)) throw cause
      throw new PlatformProtocolError(cause.message, result.requestId, result.status)
    }
    const expectedTreeEtag = `"candidate-tree:${normalized.data.candidate.id}:${normalized.data.tree.treeHash}"`
    if (result.headers.get('x-candidate-tree-etag') !== expectedTreeEtag) {
      throw new PlatformProtocolError(
        'The Sandbox tree response omitted the exact X-Candidate-Tree-ETag identity.',
        result.requestId,
        result.status,
      )
    }
    return normalized
  }

  async readFile(
    sessionId: string,
    path: string,
    options: SandboxFileReadOptions,
  ): Promise<HttpResult<SandboxFileResult>> {
    const fence = options?.fence
    if (
      !fence
      || fence.sessionId !== sessionId
      || fence.path !== path
      || !fence.projectId
      || !fence.candidateId
      || !Number.isSafeInteger(fence.sessionEpoch)
      || fence.sessionEpoch < 1
      || !Number.isSafeInteger(fence.candidateVersion)
      || fence.candidateVersion < 1
      || !Number.isSafeInteger(fence.journalSequence)
      || fence.journalSequence < 0
      || !Number.isSafeInteger(fence.writerLeaseEpoch)
      || fence.writerLeaseEpoch < 0
      || !/^sha256:[0-9a-f]{64}$/.test(fence.treeHash)
      || !/^sha256:[0-9a-f]{64}$/.test(fence.contentHash)
    ) {
      throw new PlatformProtocolError('An exact Sandbox/Candidate file-read fence is required.')
    }
    const result = await this.http.get<ArrayBuffer>(
      `/v1/sandbox-sessions/${segment(sessionId)}/files/${repositoryPath(path)}`,
      {
        ...requestOptions(options),
        headers: {
          'X-Sandbox-Session-Epoch': String(fence.sessionEpoch),
          'X-Expected-Candidate-ID': fence.candidateId,
          'X-Candidate-Version': String(fence.candidateVersion),
          'X-Candidate-Journal-Sequence': String(fence.journalSequence),
          'X-Writer-Lease-Epoch': String(fence.writerLeaseEpoch),
          'X-Candidate-Tree-Hash': fence.treeHash,
          'X-Expected-File-Hash': fence.contentHash,
        },
        responseType: 'arrayBuffer',
      },
    )
    const exactIntegerHeader = (name: string) => {
      const raw = result.headers.get(name)
      if (raw === null || !/^\d+$/.test(raw)) return Number.NaN
      const value = Number(raw)
      return Number.isSafeInteger(value) ? value : Number.NaN
    }
    const candidateId = result.headers.get('x-candidate-id') ?? ''
    const journalSequence = exactIntegerHeader('x-candidate-journal-sequence')
    const responseFences = {
      etag: result.headers.get('x-sandbox-session-etag') ?? '',
      sessionEpoch: exactIntegerHeader('x-sandbox-session-epoch'),
      candidateVersion: exactIntegerHeader('x-candidate-version'),
      writerLeaseEpoch: exactIntegerHeader('x-writer-lease-epoch'),
      treeHash: result.headers.get('x-candidate-tree-hash') ?? '',
    }
    const responseContentHash = result.headers.get('x-content-hash') ?? ''
    const mode = result.headers.get('x-file-mode') ?? ''
    if (
      !new RegExp(`^"sandbox:${sessionId.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}:\\d+"$`).test(responseFences.etag)
      || candidateId !== fence.candidateId
      || !Number.isSafeInteger(journalSequence)
      || journalSequence !== fence.journalSequence
      || responseFences.sessionEpoch !== fence.sessionEpoch
      || responseFences.candidateVersion !== fence.candidateVersion
      || responseFences.writerLeaseEpoch !== fence.writerLeaseEpoch
      || responseFences.treeHash !== fence.treeHash
      || responseContentHash !== fence.contentHash
      || (mode !== '100644' && mode !== '100755')
      || !(result.data instanceof ArrayBuffer)
    ) {
      throw new PlatformProtocolError(
        'The file response did not prove the exact Sandbox/Candidate head and tree-file hash.',
        result.requestId,
        result.status,
      )
    }
    await verifyResponseBodySha256(
      result.data,
      responseContentHash,
      'The Sandbox file bytes do not match the declared X-Content-Hash.',
      result.requestId,
      result.status,
    )
    return {
      ...result,
      data: {
        value: result.data,
        candidateId,
        journalSequence,
        contentHash: responseContentHash,
        mode,
        etag: result.etag,
        fences: responseFences,
      },
    }
  }

  async acquireWriterLease(
    sessionId: string,
    ttlSeconds: number,
    options: SandboxMutationOptions,
  ): Promise<HttpResult<SandboxSessionResultDto>> {
    const result = await this.http.post<unknown, { ttlSeconds: number }>(
      `/v1/sandbox-sessions/${segment(sessionId)}/writer-lease`,
      { ttlSeconds },
      mutationOptions(options),
    )
    return { ...result, data: normalizeSandboxSessionResult(result.data) }
  }

  async putFile(
    sessionId: string,
    path: string,
    value: Uint8Array | ArrayBuffer | Blob | string,
    expectedFileHash: string | 'absent',
    options: SandboxMutationOptions & { readonly mode?: '100644' | '100755' },
  ): Promise<HttpResult<SandboxFileMutationResultDto>> {
    const result = await this.http.put<unknown, typeof value>(
      `/v1/sandbox-sessions/${segment(sessionId)}/files/${repositoryPath(path)}`,
      value,
      mutationOptions(options, {
        'Content-Type': 'application/octet-stream',
        'X-Expected-File-Hash': expectedFileHash,
        'X-File-Mode': options.mode ?? '100644',
      }),
    )
    return { ...result, data: normalizeSandboxFileMutationResult(result.data) }
  }

  async deleteFile(
    sessionId: string,
    path: string,
    expectedFileHash: string,
    options: SandboxMutationOptions,
  ): Promise<HttpResult<SandboxFileMutationResultDto>> {
    return this.fileOperation(sessionId, {
      kind: 'file.delete', path, expectedFileHash,
    }, options)
  }

  async renameFile(
    sessionId: string,
    fromPath: string,
    path: string,
    expectedFileHash: string,
    options: SandboxMutationOptions,
  ): Promise<HttpResult<SandboxFileMutationResultDto>> {
    return this.fileOperation(sessionId, {
      kind: 'file.rename', path, fromPath, expectedFileHash,
    }, options)
  }

  async checkpoint(
    sessionId: string,
    input: { readonly checkpointId: string; readonly reason: string },
    options: SandboxMutationOptions,
  ): Promise<HttpResult<SandboxCheckpointResultDto>> {
    const result = await this.http.post<unknown, typeof input>(
      `/v1/sandbox-sessions/${segment(sessionId)}/checkpoints`,
      input,
      mutationOptions(options),
    )
    return { ...result, data: normalizeSandboxCheckpointResult(result.data) }
  }

  async freezeCandidate(
    sessionId: string,
    input: {
      readonly checkpointId: string
      readonly verificationReceiptId: string
      readonly verificationReceiptHash: string
      readonly reason: string
    },
    options: SandboxMutationOptions,
  ): Promise<HttpResult<SandboxCandidateFreezeResultDto>> {
    const result = await this.http.post<unknown, typeof input>(
      `/v1/sandbox-sessions/${segment(sessionId)}:freeze`,
      input,
      mutationOptions(options),
    )
    return { ...result, data: normalizeSandboxCandidateFreezeResult(result.data) }
  }

  async abandonCandidate(
    sessionId: string,
    input: {
      readonly candidateId: string
      readonly checkpointId?: string
      readonly reason: string
    },
    options: SandboxMutationOptions,
  ): Promise<HttpResult<SandboxSessionResultDto>> {
    const result = await this.http.post<unknown, typeof input>(
      `/v1/sandbox-sessions/${segment(sessionId)}:abandon`,
      input,
      mutationOptions(options),
    )
    return { ...result, data: normalizeSandboxSessionResult(result.data) }
  }

  fences(result: HttpResult<SandboxSessionDto>) {
    return sandboxFences(result.headers, result.data)
  }

  private fileOperation(
    sessionId: string,
    input: {
      readonly kind: 'file.delete' | 'file.rename'
      readonly path: string
      readonly fromPath?: string
      readonly expectedFileHash: string
    },
    options: SandboxMutationOptions,
  ): Promise<HttpResult<SandboxFileMutationResultDto>> {
    return this.http.post<unknown, typeof input>(
      `/v1/sandbox-sessions/${segment(sessionId)}/file-operations`,
      input,
      mutationOptions(options),
    ).then((result) => ({ ...result, data: normalizeSandboxFileMutationResult(result.data) }))
  }
}
