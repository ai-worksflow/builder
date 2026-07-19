import assert from 'node:assert/strict'
import { HttpClient, type FetchLike } from '../lib/platform/http'
import {
  candidateDocumentURI,
  candidateWorkspaceURI,
  computeLanguageServerCapabilityHash,
  computeLanguageServerProfileContentHash,
  LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH,
  LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH,
  lspTemplateProfileSupportsPath,
  parseCandidateDocumentURI,
  parseCandidateWorkspaceURI,
  SandboxLSPError,
  type LSPProfileIdentityDto,
  type LSPTemplateProfileDto,
  type LSPTicketDto,
  type LSPTicketRequestDto,
} from '../lib/platform/lsp-contract'
import { PlatformLSPClient } from '../lib/platform/lsp-client'

const projectId = '11111111-1111-4111-8111-111111111111'
const sessionId = '22222222-2222-4222-8222-222222222222'
const candidateId = '33333333-3333-4333-8333-333333333333'
const releaseId = '44444444-4444-4444-8444-444444444444'
const ticketId = '55555555-5555-4555-8555-555555555555'
const connectionId = '66666666-6666-4666-8666-666666666666'
const openId = '77777777-7777-4777-8777-777777777777'
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

async function profile() {
  const value = {
    schemaVersion: 'language-server-profile/v1',
    id: 'python-lsp',
    contentHash: digest('0'),
    serviceId: 'api',
    languageIds: ['python', 'python-django'],
    fileGlobs: ['**/*.py', 'src/**/*.py'],
    protocolVersion: '3.17',
    runtime: {
      image: `ghcr.io/worksflow/python-lsp@${digest('6')}`,
      executablePath: '/opt/lsp/bin/pyright-langserver',
      executableDigest: digest('7'),
      argv: ['/opt/lsp/bin/pyright-langserver', '--stdio'],
      workingDirectoryPolicy: 'service-root',
    },
    serverInfo: { name: 'pyright', version: '1.2.3' },
    initializationParametersHash: LSP_EMPTY_INITIALIZATION_PARAMETERS_HASH,
    workspaceConfigurationHash: LSP_EMPTY_WORKSPACE_CONFIGURATION_HASH,
    requireVersionedDiagnostics: true,
    methods: [
      'textDocument/completion',
      'textDocument/hover',
      'textDocument/publishDiagnostics',
      'textDocument/references',
    ],
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
  value.contentHash = await computeLanguageServerProfileContentHash(
    value as LSPProfileIdentityDto,
  )
  return value as LSPProfileIdentityDto
}

function requestInput() {
  return {
    mode: 'editor' as const,
    sandboxHeadFence: head,
    templateRelease,
    profileIds: ['python-lsp'],
  }
}

async function ticketBody(serverProfile: LSPProfileIdentityDto) {
  return {
    schemaVersion: 'sandbox-lsp-ticket/v1',
    id: ticketId,
    ticket: ticketSecret,
    webSocketPath: '/v1/sandbox-lsp',
    subprotocol: 'worksflow.sandbox-lsp.v1',
    mode: 'editor',
    sandboxHeadFence: head,
    templateRelease,
    profiles: [serverProfile],
    expiresAt: new Date(Date.now() + 25_000).toISOString(),
  }
}

function templateProfile(serverProfile: LSPProfileIdentityDto) {
  const value = structuredClone(serverProfile) as unknown as Record<string, unknown>
  delete value.templateRelease
  delete value.effectiveLimits
  return value as unknown as LSPTemplateProfileDto
}

function registryBody(serverProfile: LSPProfileIdentityDto) {
  return {
    release: {
      id: releaseId,
      schemaVersion: 'template-release/v2',
      admissionAttemptId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
      source: { repository: 'https://example.test/template.git' },
      manifest: {
        schemaVersion: 'template-manifest/v1',
        templateId: 'full-stack',
        displayName: 'Full stack',
        version: '1.0.0',
        services: [{ id: 'api', kind: 'api', rootPath: 'services/api' }],
        toolchains: [],
        commands: {},
        ports: [],
        healthChecks: [],
        buildOutputs: [],
        extensionPaths: [],
        protectedPaths: [],
        environmentSchema: [],
        lockfiles: [],
        profileDigest: digest('3'),
        languageServers: [templateProfile(serverProfile)],
      },
      sbomDigest: digest('4'),
      licenseExpression: 'MIT',
      licenseDigest: digest('5'),
      evidenceRefs: [],
      signature: {},
      subjectHash: digest('6'),
      contentHash: templateRelease.contentHash,
      approvedBy: 'template-authority',
      approvedAt: '2026-07-18T00:00:00Z',
      authorityReceipt: { id: 'release-receipt' },
    },
    policy: {
      schemaVersion: 'template-release-policy/v2',
      templateReleaseId: releaseId,
      releaseContentHash: templateRelease.contentHash,
      authorityReceipt: { id: 'policy-receipt' },
      state: 'approved',
      version: 1,
      reason: 'approved',
      updatedBy: 'template-authority',
      createdAt: '2026-07-18T00:00:00Z',
      updatedAt: '2026-07-18T00:00:00Z',
    },
    authorityReceipt: { id: 'registration-receipt' },
  }
}

function http(fetch: FetchLike) {
  return new HttpClient({
    baseUrl: 'https://platform.example.test/api/platform',
    fetch,
    csrfTokenStore: { get: () => 'csrf-lsp', set: () => undefined, clear: () => undefined },
    requestIdFactory: () => 'lsp-request-id',
  })
}

async function rejectedCode(action: Promise<unknown>, code: string, secret = ticketSecret) {
  try {
    await action
    assert.fail(`expected ${code}`)
  } catch (error) {
    assert.ok(error instanceof SandboxLSPError)
    assert.equal(error.code, code)
    assert.ok(!String(error).includes(secret))
    assert.ok(!JSON.stringify(error).includes(secret))
  }
}

async function main() {
  const serverProfile = await profile()
  const profileCalls: Array<{ url: string; headers: Headers; method: string | undefined }> = []
  const profileClient = new PlatformLSPClient(http(async (input, init) => {
    profileCalls.push({
      url: String(input),
      headers: new Headers(init?.headers),
      method: init?.method,
    })
    return new Response(JSON.stringify(registryBody(serverProfile)), {
      status: 200,
      headers: { 'Content-Type': 'application/json', 'Cache-Control': 'private, no-store' },
    })
  }))
  const discovery = await profileClient.discoverProfiles(templateRelease)
  assert.deepEqual(discovery.data.templateRelease, templateRelease)
  assert.deepEqual(discovery.data.profiles, [templateProfile(serverProfile)])
  assert.ok(Object.isFrozen(discovery.data))
  assert.ok(Object.isFrozen(discovery.data.profiles[0]))
  assert.equal(profileCalls[0]?.url,
    `https://platform.example.test/api/platform/v1/template-releases/${releaseId}`)
  assert.equal(profileCalls[0]?.method, 'GET')
  assert.equal(profileCalls[0]?.headers.get('accept'), 'application/json')
  assert.equal(profileCalls[0]?.headers.get('cache-control'), 'no-store')
  assert.equal(profileCalls[0]?.headers.get('pragma'), 'no-cache')
  assert.equal(lspTemplateProfileSupportsPath(discovery.data.profiles[0]!, 'src/api/view.py'), true)
  assert.equal(lspTemplateProfileSupportsPath(discovery.data.profiles[0]!, 'src/api/view.ts'), false)
  assert.equal(lspTemplateProfileSupportsPath(discovery.data.profiles[0]!, '../view.py'), false)

  async function discoverEncoded(encoded: string, cacheControl = 'no-store') {
    return new PlatformLSPClient(http(async () => new Response(encoded, {
      status: 200,
      headers: { 'Content-Type': 'application/json', 'Cache-Control': cacheControl },
    }))).discoverProfiles(templateRelease)
  }
  const revoked = registryBody(serverProfile)
  revoked.policy.state = 'revoked'
  await rejectedCode(discoverEncoded(JSON.stringify(revoked)), 'lsp_connection_identity_mismatch')
  const unknownProfile = registryBody(serverProfile) as unknown as {
    release: { manifest: { languageServers: Array<Record<string, unknown>> } }
  }
  unknownProfile.release.manifest.languageServers[0]!.shell = true
  await rejectedCode(discoverEncoded(JSON.stringify(unknownProfile)), 'lsp_message_malformed')
  const unknownService = registryBody(serverProfile) as unknown as {
    release: { manifest: { services: Array<Record<string, unknown>> } }
  }
  unknownService.release.manifest.services[0]!.id = 'web'
  await rejectedCode(discoverEncoded(JSON.stringify(unknownService)), 'lsp_message_malformed')
  await rejectedCode(
    discoverEncoded(JSON.stringify(registryBody(serverProfile)), 'private, max-age=60'),
    'lsp_message_malformed',
  )

  const calls: Array<{ url: string; headers: Headers; body: LSPTicketRequestDto }> = []
  const fetch: FetchLike = async (input, init) => {
    calls.push({
      url: String(input),
      headers: new Headers(init?.headers),
      body: JSON.parse(String(init?.body)) as LSPTicketRequestDto,
    })
    return new Response(JSON.stringify(await ticketBody(serverProfile)), {
      status: 201,
      headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' },
    })
  }
  const client = new PlatformLSPClient(http(fetch))
  const result = await client.issueTicket(sessionId, requestInput())
  assert.equal(result.data.id, ticketId)
  assert.ok(Object.isFrozen(result.data))
  assert.ok(Object.isFrozen(result.data.profiles[0]))
  assert.equal(calls.length, 1)
  assert.equal(
    calls[0]?.url,
    `https://platform.example.test/api/platform/v1/sandbox-sessions/${sessionId}/lsp-tickets`,
  )
  assert.equal(calls[0]?.headers.get('x-csrf-token'), 'csrf-lsp')
  assert.equal(calls[0]?.headers.get('cache-control'), 'no-store')
  assert.equal(calls[0]?.headers.get('pragma'), 'no-cache')
  assert.equal(calls[0]?.headers.get('idempotency-key'), null)
  assert.deepEqual(calls[0]?.body, {
    schemaVersion: 'sandbox-lsp-ticket-request/v1',
    mode: 'editor',
    sandboxHeadFence: head,
    templateRelease,
    profileIds: ['python-lsp'],
  })

  const socket = client.claimWebSocket(result.data)
  assert.deepEqual(socket, {
    url: `wss://platform.example.test/api/platform/v1/sandbox-lsp?ticket=${ticketSecret}`,
    subprotocol: 'worksflow.sandbox-lsp.v1',
  })
  await rejectedCode(
    Promise.resolve().then(() => client.claimWebSocket(result.data)),
    'lsp_ticket_scope_mismatch',
  )

  assert.equal(candidateWorkspaceURI(projectId, candidateId),
    `worksflow-candidate://${projectId}/${candidateId}`)
  assert.deepEqual(parseCandidateWorkspaceURI(candidateWorkspaceURI(projectId, candidateId)), {
    projectId, candidateId,
  })
  const modelUri = candidateDocumentURI(projectId, candidateId, 'src/a+b &c.ts')
  assert.equal(modelUri, `worksflow-candidate://${projectId}/${candidateId}/src/a+b%20&c.ts`)
  assert.deepEqual(parseCandidateDocumentURI(modelUri), {
    projectId, candidateId, path: 'src/a+b &c.ts',
  })
  assert.throws(
    () => parseCandidateDocumentURI(candidateWorkspaceURI(projectId, candidateId)),
    (error: unknown) => error instanceof SandboxLSPError && error.code === 'lsp_message_malformed',
  )
  assert.throws(
    () => candidateDocumentURI(projectId, candidateId, '../secret'),
    (error: unknown) => error instanceof SandboxLSPError && error.code === 'lsp_message_malformed',
  )

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
  const acceptedHello = client.acceptHello(JSON.stringify(hello), result.data)
  const bind = client.createBind(result.data, acceptedHello, 'python-lsp', [{
    modelUri,
    openId,
    modelVersion: 1,
    savedContentHash: digest('8'),
  }])
  assert.deepEqual(bind, {
    schemaVersion: 'sandbox-lsp-binding/v1',
    kind: 'client.bind',
    connectionId,
    bindingId: null,
    sequence: 1,
    sandboxHeadFence: head,
    languageServerProfile: serverProfile,
    documents: [{ modelUri, openId, modelVersion: 1, savedContentHash: digest('8') }],
  })

  const driftedHello = structuredClone(hello) as unknown as {
    profiles: Array<{ contentHash: string }>
  }
  driftedHello.profiles[0]!.contentHash = digest('9')
  assert.throws(
    () => client.acceptHello(JSON.stringify(driftedHello), result.data),
    (error: unknown) => error instanceof SandboxLSPError &&
      error.code === 'lsp_connection_identity_mismatch' && !String(error).includes(ticketSecret),
  )

  async function issueEncoded(encoded: string) {
    const driftClient = new PlatformLSPClient(http(async () => new Response(encoded, {
      status: 201,
      headers: { 'Content-Type': 'application/json' },
    })))
    return driftClient.issueTicket(sessionId, requestInput())
  }

  const validBody = await ticketBody(serverProfile)
  const stale = structuredClone(validBody) as unknown as {
    sandboxHeadFence: { version: number }
  }
  stale.sandboxHeadFence.version += 1
  await rejectedCode(issueEncoded(JSON.stringify(stale)), 'lsp_ticket_scope_mismatch')

  const hashDrift = structuredClone(validBody) as unknown as {
    profiles: Array<{ contentHash: string }>
  }
  hashDrift.profiles[0]!.contentHash = digest('9')
  await rejectedCode(issueEncoded(JSON.stringify(hashDrift)), 'lsp_message_malformed')

  const policyDrift = structuredClone(validBody) as unknown as {
    profiles: Array<{ workspaceConfigurationHash: string }>
  }
  policyDrift.profiles[0]!.workspaceConfigurationHash = digest('9')
  await rejectedCode(issueEncoded(JSON.stringify(policyDrift)), 'lsp_message_malformed')

  const unknownNested = structuredClone(validBody) as Record<string, unknown>
  const profiles = unknownNested.profiles as Array<Record<string, unknown>>
  ;(profiles[0]!.runtime as Record<string, unknown>).shell = true
  await rejectedCode(issueEncoded(JSON.stringify(unknownNested)), 'lsp_message_malformed')

  const validEncoded = JSON.stringify(validBody)
  await rejectedCode(
    issueEncoded(`${validEncoded.slice(0, -1)},"id":"${ticketId}"}`),
    'lsp_message_malformed',
  )
  await rejectedCode(
    issueEncoded(validEncoded.replace('"version":7', '"version":7.5')),
    'lsp_message_malformed',
  )
  await rejectedCode(
    issueEncoded(validEncoded.replace('"profiles":', '"profileIdentities":')),
    'lsp_message_malformed',
  )

  const forged = { ...result.data, webSocketPath: '/v1/sandbox-stream' } as unknown as LSPTicketDto
  assert.throws(
    () => new PlatformLSPClient(http(fetch)).claimWebSocket(forged),
    (error: unknown) => error instanceof SandboxLSPError &&
      error.code === 'lsp_websocket_url_required' && !String(error).includes(ticketSecret),
  )

  console.log('LSP Platform client contract tests passed')
}

void main()
