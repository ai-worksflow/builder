import assert from 'node:assert/strict'
import { HttpClient } from '../lib/platform/http'
import {
  candidateDocumentURI,
  computeLanguageServerCapabilityHash,
  computeLanguageServerProfileContentHash,
  decodeLSPServerBound,
  decodeLSPServerEnvelope,
  LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH,
  LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH,
  SandboxLSPError,
  type LSPClientBindDto,
  type LSPProfileIdentityDto,
  type LSPServerEnvelopeExpectation,
  type LSPTicketDto,
} from '../lib/platform/lsp-contract'
import { createLSPTicketRequest, PlatformLSPClient } from '../lib/platform/lsp-client'
import {
  ProductionLSPSession,
  type LSPWebSocketLike,
  type ProductionLSPHandshakePhase,
} from '../lib/platform/lsp-session'

const projectId = '11111111-1111-4111-8111-111111111111'
const sessionId = '22222222-2222-4222-8222-222222222222'
const candidateId = '33333333-3333-4333-8333-333333333333'
const releaseId = '44444444-4444-4444-8444-444444444444'
const ticketId = '55555555-5555-4555-8555-555555555555'
const connectionId = '66666666-6666-4666-8666-666666666666'
const bindingId = '77777777-7777-4777-8777-777777777777'
const openId = '88888888-8888-4888-8888-888888888888'
const ticketSecret = `${'A'.repeat(42)}E`
const digest = (character: string) => `sha256:${character.repeat(64)}`

const head = {
  projectId,
  sessionId,
  sessionEpoch: 3,
  candidateId,
  version: 7,
  journalSequence: 12,
  writerLeaseEpoch: 2,
  treeHash: digest('1'),
} as const

const nextHead = {
  ...head,
  version: 8,
  journalSequence: 13,
  treeHash: digest('9'),
} as const

const templateRelease = { id: releaseId, contentHash: digest('2') } as const

const limits = {
  startupTimeoutMillis: 10_000,
  requestTimeoutMillis: 5_000,
  shutdownTimeoutMillis: 2_000,
  cpuMillis: 1_000,
  memoryBytes: 512 * 2 ** 20,
  pidLimit: 64,
  tempBytes: 256 * 2 ** 20,
  cacheBytes: 256 * 2 ** 20,
  maxOpenDocuments: 16,
  maxDocumentBytes: 512 * 2 ** 10,
  maxTotalSyncBytes: 4 * 2 ** 20,
  maxFrameBytes: 256 * 2 ** 10,
  maxResultBytes: 512 * 2 ** 10,
  maxConcurrentRequests: 16,
  requestsPerSecond: 15,
  requestBurst: 30,
  maxDiagnosticsPerDocument: 1_000,
  maxCompletionItems: 250,
  maxNavigationLocations: 2_500,
} as const

async function makeProfile() {
  const value = {
    schemaVersion: 'language-server-profile/v1',
    id: 'typescript-lsp',
    contentHash: digest('0'),
    serviceId: 'web',
    languageIds: ['typescript'],
    fileGlobs: ['**/*.ts'],
    protocolVersion: '3.17',
    runtime: {
      image: `ghcr.io/worksflow/typescript-lsp@${digest('6')}`,
      executablePath: '/opt/lsp/bin/typescript-language-server',
      executableDigest: digest('7'),
      argv: ['/opt/lsp/bin/typescript-language-server', '--stdio'],
      workingDirectoryPolicy: 'service-root',
    },
    serverInfo: { name: 'typescript-language-server', version: '4.3.0' },
    initializationParametersHash: LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH,
    workspaceConfigurationHash: LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH,
    requireVersionedDiagnostics: true,
    methods: ['textDocument/hover', 'textDocument/publishDiagnostics'],
    capabilityHash: digest('0'),
    limits,
    isolation: {
      networkPolicy: 'none',
      workspaceMountPolicy: 'read-only',
      tempPolicy: 'isolated-bounded',
      cachePolicy: 'isolated-bounded',
      workspacePluginPolicy: 'forbidden',
      dynamicSdkPolicy: 'forbidden',
      dynamicRegistrationPolicy: 'forbidden',
      configurationCommandPolicy: 'forbidden',
      packageManagerHookPolicy: 'forbidden',
    },
    templateRelease,
    effectiveLimits: limits,
  }
  value.capabilityHash = await computeLanguageServerCapabilityHash(value.methods)
  value.contentHash = await computeLanguageServerProfileContentHash(value as LSPProfileIdentityDto)
  return value as LSPProfileIdentityDto
}

class FakeSocket implements LSPWebSocketLike {
  readyState = 1
  binaryType: BinaryType = 'blob'
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: { readonly data: unknown }) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onclose: ((event: { readonly code: number; readonly reason: string }) => void) | null = null
  readonly sent: string[] = []
  readonly closes: Array<{ code?: number; reason?: string }> = []

  send(data: string) {
    this.sent.push(data)
  }

  close(code?: number, reason?: string) {
    this.closes.push({ code, reason })
  }

  emit(value: unknown) {
    this.onmessage?.({ data: value })
  }

  emitClose(code: number, reason = '') {
    this.readyState = 3
    this.onclose?.({ code, reason })
  }
}

async function waitFor(predicate: () => boolean) {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return
    await new Promise((resolve) => setTimeout(resolve, 0))
  }
  assert.fail('timed out waiting for LSP state')
}

function envelope(overrides: Record<string, unknown>) {
  return {
    schemaVersion: 'sandbox-lsp-envelope/v1',
    connectionId,
    bindingId,
    sequence: 2,
    messageId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1',
    replyTo: null,
    kind: 'server.error',
    method: 'worksflow/error',
    sandboxHeadFence: head,
    documentFence: null,
    payload: { code: 'test', message: 'test error' },
    ...overrides,
  }
}

function expectMalformed(action: () => unknown) {
  assert.throws(action, (error: unknown) => error instanceof SandboxLSPError)
}

async function main() {
  const serverProfile = await makeProfile()
  assert.throws(
    () => createLSPTicketRequest({
      mode: 'editor',
      sandboxHeadFence: head,
      templateRelease,
      profileIds: ['typescript-lsp', 'python-lsp'],
    }),
    (error: unknown) => error instanceof SandboxLSPError && error.code === 'lsp_message_malformed',
  )
  const ticket = {
    schemaVersion: 'sandbox-lsp-ticket/v1',
    id: ticketId,
    ticket: ticketSecret,
    webSocketPath: '/v1/sandbox-lsp',
    subprotocol: 'worksflow.sandbox-lsp.v1',
    mode: 'editor',
    sandboxHeadFence: head,
    templateRelease,
    profiles: [structuredClone(serverProfile)],
    expiresAt: new Date(Date.now() + 20_000).toISOString(),
  } as LSPTicketDto
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test/api/platform',
    fetch: async () => assert.fail('LSP session must not issue an implicit HTTP request'),
  })
  const client = new PlatformLSPClient(http)
  const socket = new FakeSocket()
  let claimedURL = ''
  const phases: ProductionLSPHandshakePhase[] = []
  const receivedDiagnostics: unknown[] = []
  let nextClientID = 1
  const sourceDocument = {
    modelUri: candidateDocumentURI(projectId, candidateId, 'src/page.ts'),
    openId,
    modelVersion: 1,
    savedContentHash: digest('8'),
  }
  const session = ProductionLSPSession.connect({
    client,
    ticket,
    profileId: serverProfile.id,
    documents: [sourceDocument],
    socketFactory: (url, protocol) => {
      claimedURL = url
      assert.equal(protocol, 'worksflow.sandbox-lsp.v1')
      return socket
    },
    messageIdFactory: () => `90000000-0000-4000-8000-${String(nextClientID++).padStart(12, '0')}`,
    callbacks: {
      onStateChange: (snapshot) => phases.push(snapshot.handshakePhase),
      onDiagnostics: (value) => receivedDiagnostics.push(value),
    },
  })
  assert.ok(claimedURL.endsWith(`?ticket=${ticketSecret}`))
  assert.ok(!JSON.stringify(session.snapshot()).includes(ticketSecret))

  // Inputs can be released or mutated after connect without aliasing session authority.
  sourceDocument.modelVersion = 99
  ;(ticket.profiles[0] as unknown as { methods: string[] }).methods = []
  ;(ticket as { ticket: string }).ticket = `${'B'.repeat(42)}E`

  const hello = {
    schemaVersion: 'sandbox-lsp-connection/v1',
    kind: 'server.hello',
    connectionId,
    ticketId,
    sequence: 0,
    sandboxHeadFence: head,
    templateRelease,
    profiles: [serverProfile],
    limits,
    bindDeadlineAt: new Date(Date.now() + 5_000).toISOString(),
  }
  socket.emit(JSON.stringify(hello))
  await waitFor(() => session.snapshot().handshakePhase === 'bind-sent')
  const bind = JSON.parse(socket.sent[0]!) as LSPClientBindDto
  assert.equal(bind.kind, 'client.bind')
  assert.equal(bind.bindingId, null)
  assert.equal(bind.sequence, 1)
  assert.equal(bind.documents[0]!.modelVersion, 1)

  const effectiveCapabilities = [...serverProfile.methods]
  const bound = {
    schemaVersion: 'sandbox-lsp-binding/v1',
    kind: 'server.bound',
    connectionId,
    bindingId,
    sequence: 1,
    sandboxHeadFence: head,
    languageServer: {
      profileId: serverProfile.id,
      profileContentHash: serverProfile.contentHash,
      runtimeImageDigest: serverProfile.runtime.image,
      executableDigest: serverProfile.runtime.executableDigest,
      serverName: serverProfile.serverInfo.name,
      serverVersion: serverProfile.serverInfo.version,
      capabilityAllowlistHash: await computeLanguageServerCapabilityHash(effectiveCapabilities),
    },
    documents: bind.documents,
    effectiveCapabilities,
    limits,
  }

  const acceptedHello = client.acceptHello(JSON.stringify(hello), {
    id: ticketId,
    mode: 'editor',
    sandboxHeadFence: head,
    templateRelease,
    profiles: [serverProfile],
  })
  const acceptedBound = await decodeLSPServerBound(JSON.stringify(bound), acceptedHello, bind)
  assert.equal(acceptedBound.bindingId, bindingId)
  for (const mutation of [
    (value: Record<string, unknown>) => { value.binding = value.bindingId; delete value.bindingId },
    (value: Record<string, unknown>) => { value.bindingId = null },
    (value: Record<string, unknown>) => { value.unknown = true },
    (value: Record<string, unknown>) => {
      ;(value.languageServer as Record<string, unknown>).capabilityAllowlistHash = digest('f')
    },
  ]) {
    const drift = structuredClone(bound) as Record<string, unknown>
    mutation(drift)
    await assert.rejects(() => decodeLSPServerBound(JSON.stringify(drift), acceptedHello, bind))
  }
  const duplicateBound = JSON.stringify(bound).replace(
    `"bindingId":"${bindingId}"`,
    `"bindingId":"${bindingId}","bindingId":"${bindingId}"`,
  )
  await assert.rejects(() => decodeLSPServerBound(duplicateBound, acceptedHello, bind))

  socket.emit(JSON.stringify(bound))
  await waitFor(() => session.snapshot().status === 'ready')
  assert.deepEqual(phases, ['ticket-claimed', 'hello-accepted', 'bind-sent', 'bound'])

  const document = bind.documents[0]!
  session.openDocument(document, 'typescript', 'export const page = 1\n')
  const open = JSON.parse(socket.sent.at(-1)!) as Record<string, unknown>
  assert.equal(open.kind, 'client.document.open')
  assert.equal(open.sequence, 2)
  assert.equal(open.messageId, '90000000-0000-4000-8000-000000000001')

  const request = session.request('textDocument/hover', document, {
    textDocument: { uri: document.modelUri },
    position: { line: 0, character: 7 },
  })
  const requestFrame = JSON.parse(socket.sent.at(-1)!) as Record<string, unknown>
  assert.equal(requestFrame.sequence, 3)
  assert.equal(requestFrame.messageId, request.messageId)
  assert.ok(!Object.hasOwn(requestFrame.payload as object, 'requestId'))

  const responseFrame = envelope({
    sequence: 2,
    messageId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1',
    replyTo: request.messageId,
    kind: 'server.response',
    method: 'textDocument/hover',
    documentFence: document,
    payload: { status: 'ok', result: { contents: 'page' }, error: null },
  })
  const expectation: LSPServerEnvelopeExpectation = {
    connectionId,
    bindingId,
    sequence: 2,
    sandboxHeadFence: head,
    documents: [document],
    pendingRequests: [{
      messageId: request.messageId,
      method: 'textDocument/hover',
      sandboxHeadFence: head,
      documentFence: document,
    }],
    staleRequests: [],
    pendingPings: [],
    seenMessageIds: new Set([request.messageId]),
    limits,
  }
  assert.equal(decodeLSPServerEnvelope(JSON.stringify(responseFrame), expectation).kind, 'server.response')
  const negativeFrames: string[] = []
  negativeFrames.push(JSON.stringify(responseFrame).replace(
    '"messageId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"',
    '"messageId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1","messageId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2"',
  ))
  for (const mutate of [
    (value: Record<string, unknown>) => { value.unknown = true },
    (value: Record<string, unknown>) => { value.replyTo = null },
    (value: Record<string, unknown>) => { value.method = null },
    (value: Record<string, unknown>) => { value.sequence = 3 },
    (value: Record<string, unknown>) => {
      value.connectionId = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
    },
    (value: Record<string, unknown>) => {
      value.sandboxHeadFence = { ...head, treeHash: digest('f') }
    },
    (value: Record<string, unknown>) => {
      value.documentFence = { ...document, modelVersion: 2 }
    },
    (value: Record<string, unknown>) => {
      value.requestId = value.messageId
      delete value.messageId
    },
    (value: Record<string, unknown>) => {
      ;(value.payload as Record<string, unknown>).unknown = true
    },
  ]) {
    const drift = structuredClone(responseFrame) as Record<string, unknown>
    mutate(drift)
    negativeFrames.push(JSON.stringify(drift))
  }
  for (const frame of negativeFrames) {
    expectMalformed(() => decodeLSPServerEnvelope(frame, expectation))
  }

  socket.emit(JSON.stringify(responseFrame))
  assert.equal(
    JSON.stringify(await request.response),
    JSON.stringify({ status: 'ok', result: { contents: 'page' }, error: null }),
  )

  const diagnostics = {
    uri: document.modelUri,
    version: document.modelVersion,
    diagnostics: [{
      range: { start: { line: 0, character: 0 }, end: { line: 0, character: 6 } },
      severity: 2,
      code: 'demo',
      source: 'typescript',
      message: 'Demo diagnostic',
      tags: [1],
    }],
  }
  socket.emit(JSON.stringify(envelope({
    sequence: 3,
    messageId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2',
    kind: 'server.diagnostics',
    method: 'textDocument/publishDiagnostics',
    documentFence: document,
    payload: { diagnostics },
  })))
  await waitFor(() => receivedDiagnostics.length === 1)
  assert.deepEqual(receivedDiagnostics[0], diagnostics)

  const heartbeat = session.ping('context-free-editor-lease-heartbeat')
  const ping = JSON.parse(socket.sent.at(-1)!) as Record<string, unknown>
  assert.equal(ping.kind, 'client.ping')
  assert.equal(ping.method, 'worksflow/ping')
  assert.equal(ping.documentFence, null)
  assert.deepEqual(ping.payload, { nonce: 'context-free-editor-lease-heartbeat' })
  socket.emit(JSON.stringify(envelope({
    sequence: 4,
    messageId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3',
    replyTo: heartbeat.messageId,
    kind: 'server.pong',
    method: 'worksflow/pong',
    payload: { nonce: 'context-free-editor-lease-heartbeat' },
  })))
  await heartbeat.response

  const staleRequest = session.request('textDocument/hover', document, {
    textDocument: { uri: document.modelUri },
    position: { line: 0, character: 1 },
  })
  const staleOutcome = staleRequest.response.catch((error: unknown) => error)
  const nextDocument = { ...document, savedContentHash: digest('a') }
  session.headRebind(nextHead, [nextDocument])
  const rebind = JSON.parse(socket.sent.at(-1)!) as Record<string, unknown>
  assert.equal(rebind.kind, 'client.headRebind')
  assert.equal(rebind.method, 'worksflow/headRebind')
  assert.equal(rebind.documentFence, null)
  assert.ok(await staleOutcome instanceof SandboxLSPError)

  socket.emit(JSON.stringify(envelope({
    sequence: 5,
    messageId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4',
    replyTo: staleRequest.messageId,
    kind: 'server.stale',
    method: 'textDocument/hover',
    sandboxHeadFence: head,
    documentFence: document,
    payload: { code: 'stale-request' },
  })))
  await waitFor(() => session.snapshot().serverSequence === 5)
  assert.equal(session.snapshot().status, 'ready')

  const cancelled = session.request('textDocument/hover', nextDocument, {
    textDocument: { uri: nextDocument.modelUri },
    position: { line: 0, character: 1 },
  })
  const cancelledOutcome = cancelled.response.catch((error: unknown) => error)
  cancelled.cancel()
  assert.equal((JSON.parse(socket.sent.at(-1)!) as Record<string, unknown>).kind, 'client.cancel')
  const cancellationError = await cancelledOutcome
  assert.ok(cancellationError instanceof SandboxLSPError)
  assert.equal(cancellationError.code, 'lsp_request_cancelled')
  assert.ok(!String(cancellationError).includes(ticketSecret))

  socket.emit(JSON.stringify(envelope({
    sequence: 6,
    messageId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa5',
    replyTo: cancelled.messageId,
    kind: 'server.stale',
    method: 'textDocument/hover',
    sandboxHeadFence: nextHead,
    documentFence: nextDocument,
    payload: { code: 'cancelled' },
  })))
  await waitFor(() => session.snapshot().serverSequence === 6)

  // Old-head diagnostics are never projected: the session closes with the stable stale code.
  socket.emit(JSON.stringify(envelope({
    sequence: 7,
    messageId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa6',
    kind: 'server.diagnostics',
    method: 'textDocument/publishDiagnostics',
    sandboxHeadFence: head,
    documentFence: document,
    payload: { diagnostics },
  })))
  await waitFor(() => session.snapshot().status === 'stale')
  assert.deepEqual(socket.closes.at(-1), { code: 4409, reason: 'lsp_binding_stale' })
  assert.equal(session.snapshot().closeCode, 4409)
  assert.equal(receivedDiagnostics.length, 1)
  assert.ok(!JSON.stringify(socket.closes).includes(ticketSecret))
  assert.ok(!JSON.stringify(session.snapshot()).includes(ticketSecret))

  const malformedSocket = new FakeSocket()
  const malformedSecret = `${'C'.repeat(42)}E`
  const malformedSession = ProductionLSPSession.connect({
    client: new PlatformLSPClient(http),
    ticket: {
      ...ticket,
      id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
      ticket: malformedSecret,
      profiles: [serverProfile],
    },
    profileId: serverProfile.id,
    documents: [{ ...sourceDocument, modelVersion: 1 }],
    socketFactory: () => malformedSocket,
  })
  malformedSocket.emit(new Uint8Array([1, 2, 3]))
  await waitFor(() => malformedSession.snapshot().status === 'failed')
  assert.deepEqual(malformedSocket.closes.at(-1), {
    code: 4400,
    reason: 'lsp_message_malformed',
  })
  assert.equal(malformedSession.snapshot().closeCode, 4400)
  assert.ok(!JSON.stringify(malformedSocket.closes).includes(malformedSecret))

  const runtimeSocket = new FakeSocket()
  const runtimeCloseSnapshots: Array<number | null> = []
  const runtimeSession = ProductionLSPSession.connect({
    client: new PlatformLSPClient(http),
    ticket: {
      ...ticket,
      id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
      ticket: `${'D'.repeat(42)}E`,
      profiles: [serverProfile],
    },
    profileId: serverProfile.id,
    documents: [{ ...sourceDocument, modelVersion: 1 }],
    socketFactory: () => runtimeSocket,
    callbacks: {
      onStateChange: (snapshot) => runtimeCloseSnapshots.push(snapshot.closeCode),
    },
  })
  runtimeSocket.emitClose(4500, 'runtime unavailable')
  assert.equal(runtimeSession.snapshot().status, 'failed')
  assert.equal(runtimeSession.snapshot().closeCode, 4500)
  assert.equal(runtimeCloseSnapshots.at(-1), 4500, 'close code must be visible in the terminal callback')

  console.log('Production LSP session contract tests passed')
}

void main()
