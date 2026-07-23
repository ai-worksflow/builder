import { createHash, randomUUID } from 'node:crypto'

import {
  candidateDocumentURI,
  decodeLSPServerBound,
  decodeLSPServerEnvelope,
  LSP_ENVELOPE_SCHEMA_VERSION,
  lspTemplateProfileSupportsPath,
  normalizeLSPBrowserRequestParams,
  type LSPClientBindDto,
  type LSPConnectionHelloDto,
  type LSPEnvelopeRequestExpectation,
  type LSPServerBoundDto,
  type LSPServerEnvelopeDto,
  type LSPTemplateProfileDto,
  type LSPTicketDto,
  type SandboxHeadFenceDto,
  type SandboxLSPMode,
} from '@/lib/platform/lsp-contract'
import { createExactCandidateFileOpenFence } from '@/lib/platform/sandbox-file-open'
import { sandboxFences } from '@/lib/platform/sandbox-contract'

import { canonicalJSON } from '../scripts/qualification-core.mjs'

import {
  expect,
  goldenQualificationEnvironment,
  test,
  type APIRequestContext,
  type Page,
} from './qualification-runtime'
import {
  bootstrapGoldenSandbox,
  browserStorageStateWithSandbox,
  consumeGoldenFault,
  goldenPrincipal,
  goldenSubject,
  qualificationKey,
  waitForValue,
  type GoldenFaultOperation,
  type GoldenSandbox,
} from './golden-qualification-support'

const LSP_CLOSE_TICKET_REJECTED = 4401
const LSP_CLOSE_RESOURCE_EXHAUSTED = 4429
const LSP_CLOSE_RUNTIME_UNAVAILABLE = 4500
const LSP_CLOSE_CAPABILITY_VIOLATION = 4503

const ORACLE_VALUE = 'qualificationOracleValue'
const ORACLE_TARGET = 'qualificationOracleTarget'
const ORACLE_PARTIAL = 'qualificationOracle'
const ORACLE_SOURCE = [
  `export const ${ORACLE_VALUE} = 42`,
  `export function ${ORACLE_TARGET}() {`,
  `  return ${ORACLE_VALUE}`,
  '}',
  ORACLE_PARTIAL,
  '',
].join('\n')

const MALICIOUS_SERVER_VECTOR = Object.freeze([
  { vectorId: 'apply-edit', method: 'workspace/applyEdit' },
  { vectorId: 'execute-command', method: 'workspace/executeCommand' },
  { vectorId: 'rename', method: 'textDocument/rename' },
  { vectorId: 'formatting', method: 'textDocument/formatting' },
  { vectorId: 'code-action', method: 'textDocument/codeAction' },
  { vectorId: 'dynamic-registration', method: 'client/registerCapability' },
  { vectorId: 'cross-file-edit', method: 'textDocument/completion' },
] as const)

type PreparedLSP = Readonly<{
  document: Readonly<{
    content: string
    contentHash: string
    languageId: 'typescript' | 'typescriptreact'
    path: string
  }>
  head: SandboxHeadFenceDto
  profile: LSPTemplateProfileDto
  sandbox: GoldenSandbox
  templateRelease: Readonly<{ id: string; contentHash: string }>
}>

type BoundSocket = Readonly<{
  bind: LSPClientBindDto
  bound: LSPServerBoundDto
  hello: LSPConnectionHelloDto
  key: string
  ticket: LSPTicketDto
}>

type MonacoSnapshot = Readonly<{
  alternativeVersion: number
  modelVersion: number
  uri: string
  value: string
}>

type GatewayAudit = Readonly<{
  actorId: string
  action: string
  createdAt: string
  id: string
  metadata: Record<string, unknown>
  targetId: string
}>

function exactRecord(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label} must be an object`)
  }
  return value as Record<string, unknown>
}

function exactKeys(value: unknown, keys: readonly string[], label: string) {
  const record = exactRecord(value, label)
  expect(Object.keys(record).sort(), `${label} shape drift`).toEqual([...keys].sort())
  return record
}

function headFence(sandbox: GoldenSandbox): SandboxHeadFenceDto {
  return {
    projectId: sandbox.projectId,
    sessionId: sandbox.session.id,
    sessionEpoch: sandbox.session.sessionEpoch,
    candidateId: sandbox.candidate.id,
    version: sandbox.candidate.version,
    journalSequence: sandbox.candidate.journalSequence,
    writerLeaseEpoch: sandbox.candidate.writerLeaseEpoch,
    treeHash: sandbox.candidate.treeHash,
  }
}

function languageForOracle(path: string): PreparedLSP['document']['languageId'] | null {
  if (path.endsWith('.ts')) return 'typescript'
  if (path.endsWith('.tsx')) return 'typescriptreact'
  return null
}

async function refreshPrepared(prepared: PreparedLSP): Promise<PreparedLSP> {
  const tree = await prepared.sandbox.client.sandbox.getTree(prepared.sandbox.session.id)
  const document = tree.data.tree.files.find((entry) => entry.path === prepared.document.path)
  expect(document, 'The fixed LSP oracle file must remain in the exact Candidate tree').toBeTruthy()
  const sandbox: GoldenSandbox = {
    ...prepared.sandbox,
    candidate: tree.data.candidate,
    fences: sandboxFences(tree.headers, tree.data.session),
    session: tree.data.session,
    tree: tree.data,
  }
  return {
    ...prepared,
    document: { ...prepared.document, contentHash: document!.contentHash },
    head: headFence(sandbox),
    sandbox,
  }
}

async function prepareLSP(caseId: string): Promise<PreparedLSP> {
  const initial = await bootstrapGoldenSandbox(caseId)
  await initial.client.sandbox.acquireWriterLease(
    initial.session.id,
    120,
    { fences: initial.fences, idempotencyKey: qualificationKey(caseId, 'lsp-writer-lease') },
  )
  const currentTree = await initial.client.sandbox.getTree(initial.session.id)
  const sandbox: GoldenSandbox = {
    ...initial,
    candidate: currentTree.data.candidate,
    fences: sandboxFences(currentTree.headers, currentTree.data.session),
    session: currentTree.data.session,
    tree: currentTree.data,
  }
  const subject = goldenSubject()
  const templateRelease = {
    id: subject.sharedArtifacts.templateRelease.id,
    contentHash: subject.sharedArtifacts.templateRelease.contentHash,
  }
  const approved = await sandbox.client.constructorApi.listTemplateReleases(
    { states: ['approved'] },
    { limit: 100 },
  )
  const registrations = approved.data.items.filter((entry) => (
    entry.release.id === templateRelease.id
    && entry.release.contentHash === templateRelease.contentHash
    && entry.policy.state === 'approved'
  ))
  expect(registrations, 'The exact TemplateRelease must be canonically approved').toHaveLength(1)
  const registration = await sandbox.client.constructorApi.getTemplateRelease(
    registrations[0]!.release.id,
    {
      contentHash: registrations[0]!.release.contentHash,
      subjectHash: registrations[0]!.release.subjectHash,
    },
  )
  expect(registration.data).toEqual(registrations[0])

  const discovery = await sandbox.client.lsp.discoverProfiles(templateRelease)
  expect(discovery.data.templateRelease).toEqual(templateRelease)
  const profile = discovery.data.profiles.find((entry) => (
    entry.id === subject.lsp.runtime.profileId
  ))
  expect(profile, 'The approved TemplateRelease must declare the fixture LSP profile').toBeTruthy()
  expect(profile!.contentHash).toMatch(/^sha256:[0-9a-f]{64}$/u)
  expect(profile!.capabilityHash).toBe(subject.lsp.runtime.capabilityDigest)
  expect(profile!.runtime.image.endsWith(`@${subject.lsp.runtime.imageDigest}`)).toBe(true)
  expect(profile!.languageIds).toEqual(subject.lsp.runtime.languages)
  const service = sandbox.session.allowedServices.find((entry) => (
    entry.id === profile!.serviceId
    && entry.templateRelease.id === templateRelease.id
    && entry.templateRelease.contentHash === templateRelease.contentHash
  ))
  expect(service, 'The exact SandboxSession must authorize the LSP service').toBeTruthy()
  const sourceFile = sandbox.tree.tree.files.find((entry) => {
    const languageId = languageForOracle(entry.path)
    return languageId !== null
      && profile!.languageIds.includes(languageId)
      && lspTemplateProfileSupportsPath(profile!, entry.path)
  })
  expect(sourceFile, 'The approved profile must admit a saved TypeScript Golden oracle file').toBeTruthy()
  const languageId = languageForOracle(sourceFile!.path)!
  const written = await sandbox.client.sandbox.putFile(
    sandbox.session.id,
    sourceFile!.path,
    ORACLE_SOURCE,
    sourceFile!.contentHash,
    {
      fences: sandbox.fences,
      idempotencyKey: qualificationKey(caseId, 'lsp-oracle-source'),
      mode: sourceFile!.mode === '100755' ? '100755' : '100644',
    },
  )
  const oracleHash = written.data.mutation.entry.operation.contentHash
  expect(oracleHash).toMatch(/^sha256:[0-9a-f]{64}$/u)
  const oracleTree = await sandbox.client.sandbox.getTree(sandbox.session.id)
  const oracleFile = oracleTree.data.tree.files.find((entry) => entry.path === sourceFile!.path)
  expect(oracleFile?.contentHash).toBe(oracleHash)
  const preparedSandbox: GoldenSandbox = {
    ...sandbox,
    candidate: oracleTree.data.candidate,
    fences: sandboxFences(oracleTree.headers, oracleTree.data.session),
    session: oracleTree.data.session,
    tree: oracleTree.data,
  }
  return {
    document: {
      content: ORACLE_SOURCE,
      contentHash: oracleHash!,
      languageId,
      path: sourceFile!.path,
    },
    head: headFence(preparedSandbox),
    profile: profile!,
    sandbox: preparedSandbox,
    templateRelease,
  }
}

function workbenchURL() {
  const subject = goldenSubject()
  return `${subject.platform.webOrigin}/workbench/complete?view=code`
    + `&bundleId=${subject.sharedArtifacts.buildManifest.id}`
    + `&workspaceRevisionId=${subject.sharedArtifacts.workspaceRevision.id}`
}

async function openExactBrowserWorkspace(page: Page, prepared: PreparedLSP) {
  const subject = goldenSubject()
  await page.goto(
    `${subject.platform.webOrigin}/team/${prepared.sandbox.projectId}/project/${prepared.sandbox.projectId}/dashboard`,
  )
  await page.goto(workbenchURL())
  await expect(page).toHaveURL(new RegExp(
    `^${subject.platform.webOrigin.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}/`,
  ))
  await expect(page.getByText(prepared.sandbox.session.id.slice(0, 8), { exact: true })).toBeVisible()
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: prepared.document.path, exact: true }).click()
  await expect(page.locator('.monaco-editor textarea').first()).toBeVisible()
}

async function enableBrowserLSP(page: Page, prepared: PreparedLSP) {
  const status = page.getByRole('status', { name: 'Language intelligence status' })
  const profile = page.getByLabel('Approved language-server profile')
  await expect(profile).toBeEnabled({ timeout: 30_000 })
  await profile.selectOption(prepared.profile.id)
  await page.getByRole('button', { name: 'Enable selected profile', exact: true }).click()
  await expect(status).toContainText(
    `${prepared.profile.serverInfo.name} ${prepared.profile.serverInfo.version} is bound to the exact Candidate head.`,
    { timeout: 45_000 },
  )
  return status
}

async function monacoSnapshot(page: Page, expectedURI: string): Promise<MonacoSnapshot> {
  return page.evaluate((uri) => {
    type Model = {
      uri: { toString(): string }
      getAlternativeVersionId(): number
      getValue(): string
      getVersionId(): number
    }
    const runtime = (globalThis as unknown as {
      monaco?: { editor?: { getModels?(): readonly Model[] } }
    }).monaco
    const models = runtime?.editor?.getModels?.().filter((model) => model.uri.toString() === uri) ?? []
    if (models.length !== 1) throw new Error(`expected one exact Monaco model for ${uri}`)
    const model = models[0]!
    return {
      alternativeVersion: model.getAlternativeVersionId(),
      modelVersion: model.getVersionId(),
      uri: model.uri.toString(),
      value: model.getValue(),
    }
  }, expectedURI)
}

async function focusOracleIdentifier(page: Page) {
  const editor = page.locator('.monaco-editor textarea').first()
  await editor.click()
  await page.keyboard.press('Control+f')
  const input = page.locator('.find-widget input').first()
  await expect(input).toBeVisible()
  await input.fill(ORACLE_VALUE)
  await page.keyboard.press('Enter')
  await page.keyboard.press('Escape')
  return editor
}

async function triggerUIHover(page: Page) {
  await focusOracleIdentifier(page)
  await page.keyboard.press('Control+k')
  await page.keyboard.press('Control+i')
  await expect(page.locator('.monaco-hover:visible').last()).toContainText(ORACLE_VALUE, {
    timeout: 30_000,
  })
  await page.keyboard.press('Escape')
}

async function exerciseMonacoOracle(page: Page) {
  await page.getByRole('button', { name: /^Problems\s+[1-9][0-9]*$/u }).click()
  await expect(page.getByText(new RegExp(ORACLE_PARTIAL, 'u')).last()).toBeVisible({ timeout: 30_000 })
  const editor = await focusOracleIdentifier(page)
  await page.keyboard.press('Control+k')
  await page.keyboard.press('Control+i')
  await expect(page.locator('.monaco-hover:visible').last()).toContainText(ORACLE_VALUE, {
    timeout: 30_000,
  })
  await page.keyboard.press('Escape')
  await page.keyboard.press('F12')
  await expect(editor).toBeFocused()
  await page.keyboard.press('Control+End')
  await page.keyboard.press('Control+Space')
  await expect(page.locator('.suggest-widget.visible .monaco-list-row').filter({
    hasText: ORACLE_VALUE,
  }).first()).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('Escape')
}

async function acceptOracleCompletion(page: Page) {
  const editor = page.locator('.monaco-editor textarea').first()
  await editor.click()
  await page.keyboard.press('Control+End')
  await page.keyboard.press('Control+Space')
  const row = page.locator('.suggest-widget.visible .monaco-list-row').filter({
    hasText: ORACLE_VALUE,
  }).first()
  await expect(row).toBeVisible({ timeout: 30_000 })
  await row.click()
}

async function bindSocket(
  page: Page,
  prepared: PreparedLSP,
  suffix: string,
  mode: SandboxLSPMode = 'snapshot',
): Promise<BoundSocket> {
  const ticketResult = await prepared.sandbox.client.lsp.issueTicket(
    prepared.sandbox.session.id,
    {
      mode,
      sandboxHeadFence: prepared.head,
      templateRelease: prepared.templateRelease,
      profileIds: [prepared.profile.id],
    },
  )
  const ticket = ticketResult.data
  const descriptor = prepared.sandbox.client.lsp.claimWebSocket(
    ticket,
    goldenSubject().platform.webOrigin,
  )
  const key = `${suffix}:${ticket.id}`
  const helloEncoded = await page.evaluate(
    ({ key: socketKey, protocol, url }) => new Promise<string>((resolve, reject) => {
      const socket = new WebSocket(url, protocol)
      const timer = globalThis.setTimeout(() => reject(new Error('LSP hello deadline exceeded')), 30_000)
      socket.addEventListener('message', (event) => {
        globalThis.clearTimeout(timer)
        const store = globalThis as unknown as {
          __goldenLSPSockets?: Record<string, { socket: WebSocket }>
        }
        store.__goldenLSPSockets ??= {}
        store.__goldenLSPSockets[socketKey] = { socket }
        resolve(String(event.data))
      }, { once: true })
      socket.addEventListener('error', () => {
        globalThis.clearTimeout(timer)
        reject(new Error('LSP socket connection failed'))
      }, { once: true })
    }),
    { key, protocol: descriptor.subprotocol, url: descriptor.url },
  )
  const hello = prepared.sandbox.client.lsp.acceptHello(helloEncoded, ticket)
  const bind = prepared.sandbox.client.lsp.createBind(
    ticket,
    hello,
    prepared.profile.id,
    [{
      modelUri: candidateDocumentURI(
        prepared.sandbox.projectId,
        prepared.sandbox.candidate.id,
        prepared.document.path,
      ),
      openId: randomUUID(),
      modelVersion: 1,
      savedContentHash: prepared.document.contentHash,
    }],
  )
  const boundEncoded = await page.evaluate(
    ({ encoded, socketKey }) => new Promise<string>((resolve, reject) => {
      const store = globalThis as unknown as {
        __goldenLSPSockets?: Record<string, { socket: WebSocket }>
      }
      const socket = store.__goldenLSPSockets?.[socketKey]?.socket
      if (!socket || socket.readyState !== WebSocket.OPEN) {
        reject(new Error('LSP socket is not open for binding'))
        return
      }
      const timer = globalThis.setTimeout(() => reject(new Error('LSP bind deadline exceeded')), 30_000)
      socket.addEventListener('message', (event) => {
        globalThis.clearTimeout(timer)
        resolve(String(event.data))
      }, { once: true })
      socket.send(encoded)
    }),
    { encoded: JSON.stringify(bind), socketKey: key },
  )
  const bound = await decodeLSPServerBound(boundEncoded, hello, bind)
  return { bind, bound, hello, key, ticket }
}

async function closeSocket(page: Page, key: string) {
  await page.evaluate((socketKey) => {
    const store = globalThis as unknown as {
      __goldenLSPSockets?: Record<string, { socket: WebSocket }>
    }
    const target = store.__goldenLSPSockets?.[socketKey]
    if (target?.socket.readyState === WebSocket.OPEN) target.socket.close(1000, 'qualification_complete')
    if (store.__goldenLSPSockets) delete store.__goldenLSPSockets[socketKey]
  }, key)
}

async function exchangeOracleFrames(
  page: Page,
  socketKey: string,
  frames: readonly string[],
  replyTo: string,
  requireDiagnostics: boolean,
) {
  return page.evaluate(({ diagnosticRequired, outgoing, requestId, key }) => (
    new Promise<string[]>((resolve, reject) => {
      const store = globalThis as unknown as {
        __goldenLSPSockets?: Record<string, { socket: WebSocket }>
      }
      const socket = store.__goldenLSPSockets?.[key]?.socket
      if (!socket || socket.readyState !== WebSocket.OPEN) {
        reject(new Error('LSP oracle socket is not open'))
        return
      }
      const messages: string[] = []
      let diagnostics = false
      let terminal = false
      const timer = globalThis.setTimeout(() => {
        socket.removeEventListener('message', onMessage)
        reject(new Error('LSP fixed-oracle response deadline exceeded'))
      }, 30_000)
      const finish = () => {
        if (!terminal || (diagnosticRequired && !diagnostics)) return
        globalThis.clearTimeout(timer)
        socket.removeEventListener('message', onMessage)
        resolve(messages)
      }
      const onMessage = (event: MessageEvent) => {
        const encoded = String(event.data)
        messages.push(encoded)
        try {
          const parsed = JSON.parse(encoded) as Record<string, unknown>
          if (parsed.kind === 'server.diagnostics') diagnostics = true
          if ((parsed.kind === 'server.response' || parsed.kind === 'server.stale')
            && parsed.replyTo === requestId) terminal = true
        } catch {
          terminal = true
        }
        finish()
      }
      socket.addEventListener('message', onMessage)
      for (const frame of outgoing) socket.send(frame)
    })
  ), {
    diagnosticRequired: requireDiagnostics,
    key: socketKey,
    outgoing: [...frames],
    requestId: replyTo,
  })
}

async function runRealServerOracle(page: Page, prepared: PreparedLSP) {
  const binding = await bindSocket(page, prepared, 'fixed-real-server-oracle')
  const document = binding.bind.documents[0]!
  const requiredMethods = [
    'textDocument/completion',
    'textDocument/definition',
    'textDocument/hover',
    'textDocument/publishDiagnostics',
  ] as const
  for (const method of requiredMethods) {
    expect(binding.bound.effectiveCapabilities).toContain(method)
  }
  let clientSequence = 1
  let serverSequence = 1
  const seen = new Set<string>()
  const responses = new Map<string, unknown>()
  const diagnostics: LSPServerEnvelopeDto[] = []
  const requests = [
    {
      method: 'textDocument/hover',
      params: {
        textDocument: { uri: document.modelUri },
        position: { line: 0, character: 20 },
      },
    },
    {
      method: 'textDocument/definition',
      params: {
        textDocument: { uri: document.modelUri },
        position: { line: 2, character: 15 },
      },
    },
    {
      method: 'textDocument/completion',
      params: {
        textDocument: { uri: document.modelUri },
        position: { line: 4, character: ORACLE_PARTIAL.length },
        context: { triggerKind: 1 },
      },
    },
  ] as const

  try {
    for (const [index, request] of requests.entries()) {
      const requestId = randomUUID()
      const outgoing: string[] = []
      if (index === 0) {
        const openId = randomUUID()
        clientSequence += 1
        seen.add(openId)
        outgoing.push(JSON.stringify({
          schemaVersion: LSP_ENVELOPE_SCHEMA_VERSION,
          connectionId: binding.bound.connectionId,
          bindingId: binding.bound.bindingId,
          sequence: clientSequence,
          messageId: openId,
          replyTo: null,
          kind: 'client.document.open',
          method: 'textDocument/didOpen',
          sandboxHeadFence: prepared.head,
          documentFence: document,
          payload: { languageId: prepared.document.languageId, text: prepared.document.content },
        }))
      }
      const normalized = normalizeLSPBrowserRequestParams(
        request.method,
        request.params,
        prepared.head,
        document,
      )
      clientSequence += 1
      seen.add(requestId)
      outgoing.push(JSON.stringify({
        schemaVersion: LSP_ENVELOPE_SCHEMA_VERSION,
        connectionId: binding.bound.connectionId,
        bindingId: binding.bound.bindingId,
        sequence: clientSequence,
        messageId: requestId,
        replyTo: null,
        kind: 'client.request',
        method: request.method,
        sandboxHeadFence: prepared.head,
        documentFence: document,
        payload: { params: normalized },
      }))
      const encodedFrames = await exchangeOracleFrames(
        page,
        binding.key,
        outgoing,
        requestId,
        index === 0,
      )
      const expectation: LSPEnvelopeRequestExpectation = {
        documentFence: document,
        messageId: requestId,
        method: request.method,
        sandboxHeadFence: prepared.head,
      }
      for (const encoded of encodedFrames) {
        const envelope = decodeLSPServerEnvelope(encoded, {
          connectionId: binding.bound.connectionId,
          bindingId: binding.bound.bindingId,
          sequence: serverSequence + 1,
          sandboxHeadFence: prepared.head,
          documents: [document],
          pendingRequests: [expectation],
          staleRequests: [],
          pendingPings: [],
          seenMessageIds: seen,
          limits: binding.bound.limits,
        })
        serverSequence = envelope.sequence
        seen.add(envelope.messageId)
        if (envelope.kind === 'server.diagnostics') diagnostics.push(envelope)
        if (envelope.kind === 'server.response' && envelope.replyTo === requestId) {
          const payload = envelope.payload as {
            readonly error: null | { readonly code: number; readonly message: string }
            readonly result: unknown
            readonly status: 'error' | 'ok'
          }
          expect(payload.status).toBe('ok')
          expect(payload.error).toBeNull()
          responses.set(request.method, payload.result)
        }
      }
      expect(responses.has(request.method), `${request.method} must return a real server response`).toBe(true)
    }
  } catch (cause) {
    await closeSocket(page, binding.key)
    throw cause
  }

  expect(JSON.stringify(responses.get('textDocument/hover'))).toContain(ORACLE_VALUE)
  const navigationValue = responses.get('textDocument/definition')
  const navigation = navigationValue === null
    ? []
    : Array.isArray(navigationValue) ? navigationValue : [navigationValue]
  expect(navigation.length).toBeGreaterThan(0)
  const location = exactRecord(navigation[0], 'definition location')
  expect(location.uri).toBe(document.modelUri)
  const range = exactRecord(location.range, 'definition range')
  const start = exactRecord(range.start, 'definition range start')
  expect(start.line).toBe(0)
  expect(start.character).toBe(13)

  const completionValue = responses.get('textDocument/completion')
  const completionItems = Array.isArray(completionValue)
    ? completionValue
    : exactRecord(completionValue, 'completion list').items
  expect(Array.isArray(completionItems)).toBe(true)
  const items = completionItems as unknown[]
  expect(items.some((entry) => exactRecord(entry, 'completion item').label === ORACLE_VALUE)).toBe(true)
  expect(items.some((entry) => exactRecord(entry, 'completion item').label === ORACLE_TARGET)).toBe(true)
  for (const entry of items) {
    const item = exactRecord(entry, 'safe completion item')
    expect(item.command).toBeUndefined()
    expect(item.additionalTextEdits).toBeUndefined()
    expect(item.data).toBeUndefined()
    expect(item.insertTextFormat).not.toBe(2)
    expect(Boolean(item.insertText) !== Boolean(item.textEdit)).toBe(true)
  }

  const diagnosticValues = diagnostics.flatMap((envelope) => {
    const payload = envelope.payload as {
      readonly diagnostics: { readonly diagnostics: readonly unknown[] }
    }
    return payload.diagnostics.diagnostics
  })
  expect(diagnosticValues.some((entry) => {
    const diagnostic = exactRecord(entry, 'diagnostic')
    const diagnosticRange = exactRecord(diagnostic.range, 'diagnostic range')
    return exactRecord(diagnosticRange.start, 'diagnostic start').line === 4
      && String(diagnostic.message).includes(ORACLE_PARTIAL)
  })).toBe(true)
  return binding
}

async function observeRealRuntimeBinding(
  request: APIRequestContext,
  prepared: PreparedLSP,
  binding: BoundSocket,
) {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const ticketProfile = binding.ticket.profiles[0]!
  const pointerResponse = await request.get(
    `${subject.platform.apiOrigin}/v1/qualification/lsp-runtime-bindings/${binding.bound.bindingId}`,
    {
      headers: environment.credentials.platform.apiA.headers,
      params: { fixtureId: subject.fixtureId, runId: subject.runId },
    },
  )
  const pointerBytes = await pointerResponse.body()
  expect(
    pointerResponse.status(),
    `A Golden-run-bound immutable LSP runtime receipt pointer is required; response=${pointerResponse.status()} ${pointerBytes.toString('utf8')}`,
  ).toBe(200)
  expect(pointerResponse.headers()['content-type'] ?? '').toMatch(/^application\/json(?:;|$)/iu)
  expect(pointerResponse.headers()['cache-control'] ?? '').toContain('no-store')
  expect(Array.from(pointerBytes.subarray(0, 3))).not.toEqual([0xef, 0xbb, 0xbf])
  const pointerText = new TextDecoder('utf-8', { fatal: true }).decode(pointerBytes)
  const pointerDocument = JSON.parse(pointerText) as unknown
  expect(pointerText).toBe(canonicalJSON(pointerDocument))
  // The external runner signs this exact authority/fixture/plan/run-rooted pointer in
  // the Playwright qualification evidence; only its content-addressed receipt is data.
  const pointer = exactKeys(pointerDocument, [
    'authorityHash', 'bindingId', 'fixtureHash', 'fixtureId', 'planDigest',
    'receipt', 'runId', 'schemaVersion',
  ], 'Golden run LSP runtime-binding receipt pointer')
  expect(pointer.schemaVersion).toBe('worksflow-golden-run-lsp-runtime-binding-pointer/v1')
  expect(pointer.authorityHash).toBe(environment.authorityHash)
  expect(pointer.fixtureHash).toBe(environment.fixtureHash)
  expect(pointer.planDigest).toBe(subject.planDigest)
  expect(pointer.fixtureId).toBe(subject.fixtureId)
  expect(pointer.runId).toBe(subject.runId)
  expect(pointer.bindingId).toBe(binding.bound.bindingId)
  const receiptPointer = exactKeys(pointer.receipt, ['contentHash', 'id'], 'LSP runtime receipt ref')
  expect(receiptPointer.id).toMatch(/^[0-9a-f-]{36}$/u)
  expect(receiptPointer.contentHash).toMatch(/^sha256:[0-9a-f]{64}$/u)

  const receiptResponse = await request.get(
    `${subject.platform.apiOrigin}/v1/qualification/lsp-runtime-binding-receipts/${receiptPointer.id}`,
    {
      headers: environment.credentials.platform.apiA.headers,
      params: {
        contentHash: String(receiptPointer.contentHash),
        fixtureId: subject.fixtureId,
        runId: subject.runId,
      },
    },
  )
  const receiptBytes = await receiptResponse.body()
  expect(
    receiptResponse.status(),
    `Exact immutable LSP runtime-binding receipt bytes are required; response=${receiptResponse.status()} ${receiptBytes.toString('utf8')}`,
  ).toBe(200)
  expect(receiptResponse.headers()['content-type'] ?? '').toMatch(/^application\/json(?:;|$)/iu)
  expect(receiptResponse.headers()['cache-control']).toBe('public, max-age=31536000, immutable')
  expect(receiptResponse.headers().etag).toBe(`"${receiptPointer.contentHash}"`)
  expect(`sha256:${createHash('sha256').update(receiptBytes).digest('hex')}`).toBe(
    receiptPointer.contentHash,
  )
  expect(Array.from(receiptBytes.subarray(0, 3))).not.toEqual([0xef, 0xbb, 0xbf])
  const receiptText = new TextDecoder('utf-8', { fatal: true }).decode(receiptBytes)
  const receiptDocument = JSON.parse(receiptText) as unknown
  expect(receiptText).toBe(canonicalJSON(receiptDocument))
  const receipt = exactKeys(receiptDocument, [
    'argv', 'authorityHash', 'bindingId', 'candidateId', 'capabilityHash',
    'connectionId', 'documents', 'effectiveCapabilities', 'effectiveLimits',
    'executableDigest', 'executablePath', 'fixtureHash', 'fixtureId', 'isolation',
    'observedAt', 'planDigest', 'profileContentHash', 'profileId', 'projectId',
    'receiptId', 'runId', 'runtimeIdentity', 'runtimeImage', 'runtimeProcessId',
    'runtimeStartedAt', 'sandboxHeadFence', 'schemaVersion', 'serverInfo', 'sessionId',
    'templateRelease', 'templateReleaseApprovalReceiptDigest', 'workingDirectoryPolicy',
  ], 'immutable real LSP runtime-binding receipt')
  expect(receipt.schemaVersion).toBe('lsp-runtime-binding-receipt/v1')
  expect(receipt.receiptId).toBe(receiptPointer.id)
  expect(receipt.authorityHash).toBe(environment.authorityHash)
  expect(receipt.fixtureHash).toBe(environment.fixtureHash)
  expect(receipt.planDigest).toBe(subject.planDigest)
  expect(receipt.fixtureId).toBe(subject.fixtureId)
  expect(receipt.runId).toBe(subject.runId)
  expect(receipt.projectId).toBe(prepared.sandbox.projectId)
  expect(receipt.sessionId).toBe(prepared.sandbox.session.id)
  expect(receipt.candidateId).toBe(prepared.sandbox.candidate.id)
  expect(receipt.connectionId).toBe(binding.bound.connectionId)
  expect(receipt.bindingId).toBe(binding.bound.bindingId)
  expect(receipt.runtimeProcessId).toMatch(/^[0-9a-f-]{36}$/u)
  expect(Number.isFinite(Date.parse(String(receipt.runtimeStartedAt)))).toBe(true)
  expect(Number.isFinite(Date.parse(String(receipt.observedAt)))).toBe(true)
  expect(Date.parse(String(receipt.observedAt))).toBeGreaterThanOrEqual(
    Date.parse(String(receipt.runtimeStartedAt)),
  )
  expect(receipt.sandboxHeadFence).toEqual(prepared.head)
  expect(receipt.documents).toEqual(binding.bound.documents)
  expect(receipt.templateRelease).toEqual(prepared.templateRelease)
  expect(receipt.templateReleaseApprovalReceiptDigest).toBe(
    subject.sharedArtifacts.templateRelease.approvalReceiptDigest,
  )
  expect(receipt.profileId).toBe(ticketProfile.id)
  expect(receipt.profileContentHash).toBe(ticketProfile.contentHash)
  expect(receipt.runtimeIdentity).toBe(subject.lsp.runtime.identity)
  expect(receipt.runtimeImage).toBe(ticketProfile.runtime.image)
  expect(receipt.executablePath).toBe(ticketProfile.runtime.executablePath)
  expect(receipt.executableDigest).toBe(ticketProfile.runtime.executableDigest)
  expect(receipt.argv).toEqual(ticketProfile.runtime.argv)
  expect(receipt.workingDirectoryPolicy).toBe(ticketProfile.runtime.workingDirectoryPolicy)
  expect(receipt.serverInfo).toEqual(ticketProfile.serverInfo)
  expect(receipt.capabilityHash).toBe(ticketProfile.capabilityHash)
  expect(receipt.effectiveCapabilities).toEqual(binding.bound.effectiveCapabilities)
  expect(receipt.effectiveLimits).toEqual(ticketProfile.effectiveLimits)
  expect(receipt.isolation).toEqual(ticketProfile.isolation)
}

async function verifyTicketSingleUse(page: Page, prepared: PreparedLSP) {
  const issued = await prepared.sandbox.client.lsp.issueTicket(prepared.sandbox.session.id, {
    mode: 'snapshot',
    sandboxHeadFence: prepared.head,
    templateRelease: prepared.templateRelease,
    profileIds: [prepared.profile.id],
  })
  const descriptor = prepared.sandbox.client.lsp.claimWebSocket(
    issued.data,
    goldenSubject().platform.webOrigin,
  )
  const raced = await page.evaluate(({ protocol, url }) => Promise.all([0, 1].map((slot) => (
    new Promise<{ closeCode: number | null; hello: string | null }>((resolve) => {
      const socket = new WebSocket(url, protocol)
      let settled = false
      const finish = (value: { closeCode: number | null; hello: string | null }) => {
        if (settled) return
        settled = true
        resolve(value)
      }
      const timer = globalThis.setTimeout(() => {
        socket.close()
        finish({ closeCode: null, hello: null })
      }, 30_000)
      socket.addEventListener('message', (event) => {
        globalThis.clearTimeout(timer)
        const store = globalThis as unknown as { __goldenTicketRace?: Record<string, WebSocket> }
        store.__goldenTicketRace ??= {}
        store.__goldenTicketRace[String(slot)] = socket
        finish({ closeCode: null, hello: String(event.data) })
      }, { once: true })
      socket.addEventListener('close', (event) => {
        globalThis.clearTimeout(timer)
        finish({ closeCode: event.code, hello: null })
      }, { once: true })
    })
  ))), { protocol: descriptor.subprotocol, url: descriptor.url })
  const winners = raced.filter((entry) => entry.hello !== null)
  const rejected = raced.filter((entry) => entry.closeCode === LSP_CLOSE_TICKET_REJECTED)
  expect(winners).toHaveLength(1)
  expect(rejected).toHaveLength(1)
  prepared.sandbox.client.lsp.acceptHello(winners[0]!.hello!, issued.data)
  await page.evaluate(() => {
    const store = globalThis as unknown as { __goldenTicketRace?: Record<string, WebSocket> }
    for (const socket of Object.values(store.__goldenTicketRace ?? {})) socket.close(1000, 'race_complete')
    delete store.__goldenTicketRace
  })
  const replay = await page.evaluate(({ protocol, url }) => (
    new Promise<{ closeCode: number | null; receivedFrame: boolean }>((resolve) => {
      const socket = new WebSocket(url, protocol)
      const timer = globalThis.setTimeout(() => {
        socket.close()
        resolve({ closeCode: null, receivedFrame: false })
      }, 30_000)
      socket.addEventListener('message', () => {
        globalThis.clearTimeout(timer)
        socket.close()
        resolve({ closeCode: null, receivedFrame: true })
      }, { once: true })
      socket.addEventListener('close', (event) => {
        globalThis.clearTimeout(timer)
        resolve({ closeCode: event.code, receivedFrame: false })
      }, { once: true })
    })
  ), { protocol: descriptor.subprotocol, url: descriptor.url })
  expect(replay).toEqual({ closeCode: LSP_CLOSE_TICKET_REJECTED, receivedFrame: false })
}

function candidateFileURL(prepared: PreparedLSP) {
  const path = prepared.document.path.split('/').map(encodeURIComponent).join('/')
  return `/v1/sandbox-sessions/${prepared.sandbox.session.id}/files/${path}`
}

function isCandidatePut(url: string, method: string, prepared: PreparedLSP) {
  return method === 'PUT' && new URL(url).pathname.endsWith(candidateFileURL(prepared))
}

async function readPreparedDocument(prepared: PreparedLSP) {
  const observed = prepared.sandbox.tree.tree.files.find((entry) => entry.path === prepared.document.path)
  expect(observed, 'The exact oracle file must remain readable').toBeTruthy()
  const fence = createExactCandidateFileOpenFence({
    projectId: prepared.sandbox.projectId,
    session: prepared.sandbox.session,
    candidate: prepared.sandbox.candidate,
    fences: prepared.sandbox.fences,
    path: prepared.document.path,
    observedFile: observed,
    expectedContentHash: observed!.contentHash,
  })
  expect(fence).toBeTruthy()
  const result = await prepared.sandbox.client.sandbox.readFile(
    prepared.sandbox.session.id,
    prepared.document.path,
    { fence: fence! },
  )
  return new TextDecoder('utf-8', { fatal: true }).decode(result.data.value)
}

function gatewayMetadata(value: unknown, sessionId: string) {
  const metadata = exactRecord(value, 'LSP Gateway audit metadata')
  return metadata.schemaVersion === 'sandbox-lsp-gateway-audit/v1'
    && metadata.sessionId === sessionId
    ? metadata
    : null
}

async function gatewayRequestAudits(
  prepared: PreparedLSP,
  method: string,
  minimum: number,
): Promise<readonly GatewayAudit[]> {
  return waitForValue(
    async () => {
      const result = await prepared.sandbox.client.audit.list(prepared.sandbox.projectId, { limit: 200 })
      return result.data.items.flatMap((entry) => {
        const metadata = gatewayMetadata(entry.metadata, prepared.sandbox.session.id)
        if (!metadata || entry.action !== 'lsp.gateway.request.completed'
          || metadata.method !== method || metadata.outcome !== 'completed') return []
        return [{
          actorId: entry.actorId,
          action: entry.action,
          createdAt: entry.createdAt,
          id: entry.id,
          metadata,
          targetId: entry.targetId,
        }]
      }).sort((left, right) => left.createdAt.localeCompare(right.createdAt))
    },
    (events) => events.length >= minimum,
    `${method} durable Gateway audit`,
  )
}

function auditDocument(event: GatewayAudit) {
  return exactRecord(event.metadata.documentFence, 'Gateway audit DocumentFence')
}

const STALE_HOLD_KEYS = [
  'bindingId', 'candidateId', 'documentFence', 'fixtureId', 'holdId', 'method',
  'openingHead', 'profileId', 'projectId', 'requestId', 'runId', 'schemaVersion',
  'serverResultDigest', 'sessionId', 'status',
] as const

function staleHoldIdentity(value: unknown, prepared: PreparedLSP, bindingId: string) {
  const subject = goldenSubject()
  const observation = exactKeys(value, STALE_HOLD_KEYS, 'LSP stale-response hold')
  expect(observation.schemaVersion).toBe('worksflow-lsp-stale-response-hold/v1')
  expect(observation.fixtureId).toBe(subject.fixtureId)
  expect(observation.runId).toBe(subject.runId)
  expect(observation.projectId).toBe(prepared.sandbox.projectId)
  expect(observation.sessionId).toBe(prepared.sandbox.session.id)
  expect(observation.candidateId).toBe(prepared.sandbox.candidate.id)
  expect(observation.bindingId).toBe(bindingId)
  expect(observation.profileId).toBe(prepared.profile.id)
  expect(observation.method).toBe('textDocument/completion')
  return observation
}

async function armStaleResponseHold(
  request: APIRequestContext,
  prepared: PreparedLSP,
  bindingId: string,
  openingHead: unknown,
  documentFence: unknown,
) {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const response = await request.post(
    `${subject.platform.apiOrigin}/v1/qualification/lsp-bindings/${bindingId}/stale-response-holds`,
    {
      headers: {
        ...environment.credentials.platform.faultOperator.headers,
        'Idempotency-Key': qualificationKey('QG-LSP-002', 'arm-stale-response-hold'),
      },
      data: {
        fixtureId: subject.fixtureId,
        runId: subject.runId,
        schemaVersion: 'worksflow-lsp-stale-response-hold-request/v1',
      },
    },
  )
  expect(
    response.status(),
    `A real binding-scoped LSP stale-response hold route is required; response=${response.status()} ${await response.text()}`,
  ).toBe(200)
  expect(response.headers()['content-type'] ?? '').toMatch(/^application\/json(?:;|$)/iu)
  const armed = staleHoldIdentity(await response.json(), prepared, bindingId)
  expect(armed.holdId).toMatch(/^[0-9a-f-]{36}$/u)
  expect(armed.openingHead).toEqual(openingHead)
  expect(armed.documentFence).toEqual(documentFence)
  expect(armed.requestId).toBeNull()
  expect(armed.serverResultDigest).toBeNull()
  expect(armed.status).toBe('armed')
  return armed
}

async function waitForHeldStaleResponse(
  request: APIRequestContext,
  prepared: PreparedLSP,
  bindingId: string,
  holdId: string,
) {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  return waitForValue(
    async () => {
      const response = await request.get(
        `${subject.platform.apiOrigin}/v1/qualification/lsp-stale-response-holds/${holdId}`,
        {
          headers: environment.credentials.platform.faultOperator.headers,
          params: { fixtureId: subject.fixtureId, runId: subject.runId },
        },
      )
      expect(
        response.status(),
        `The real LSP stale-response hold must remain observable; response=${response.status()} ${await response.text()}`,
      ).toBe(200)
      return staleHoldIdentity(await response.json(), prepared, bindingId)
    },
    (observation) => observation.status === 'response-held',
    'real language-server response held in flight',
  )
}

async function releaseStaleResponseHold(
  request: APIRequestContext,
  prepared: PreparedLSP,
  bindingId: string,
  holdId: string,
) {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const response = await request.post(
    `${subject.platform.apiOrigin}/v1/qualification/lsp-stale-response-holds/${holdId}:release`,
    {
      headers: {
        ...environment.credentials.platform.faultOperator.headers,
        'Idempotency-Key': qualificationKey('QG-LSP-002', 'release-stale-response-hold'),
      },
      data: {
        fixtureId: subject.fixtureId,
        runId: subject.runId,
        schemaVersion: 'worksflow-lsp-stale-response-release-request/v1',
      },
    },
  )
  expect(
    response.status(),
    `The real held LSP response must be released through the Gateway; response=${response.status()} ${await response.text()}`,
  ).toBe(200)
  expect(response.headers()['content-type'] ?? '').toMatch(/^application\/json(?:;|$)/iu)
  const result = exactKeys(await response.json(), [
    'auditEventId', 'bindingId', 'browserForwardedCount', 'candidateId',
    'documentFence', 'fixtureId', 'holdId', 'method', 'oldServerResponseObserved',
    'openingHead', 'outcome', 'profileId', 'projectId', 'requestId', 'runId',
    'schemaVersion', 'serverResultDigest', 'serverStaleSent', 'sessionId',
    'staleDiagnosticForwardedCount', 'status', 'successorHead',
  ], 'released LSP stale-response result')
  expect(result.schemaVersion).toBe('worksflow-lsp-stale-response-result/v1')
  expect(result.fixtureId).toBe(subject.fixtureId)
  expect(result.runId).toBe(subject.runId)
  expect(result.projectId).toBe(prepared.sandbox.projectId)
  expect(result.sessionId).toBe(prepared.sandbox.session.id)
  expect(result.candidateId).toBe(prepared.sandbox.candidate.id)
  expect(result.bindingId).toBe(bindingId)
  expect(result.profileId).toBe(prepared.profile.id)
  expect(result.method).toBe('textDocument/completion')
  expect(result.holdId).toBe(holdId)
  expect(result.oldServerResponseObserved).toBe(true)
  expect(result.serverStaleSent).toBe(true)
  expect(result.browserForwardedCount).toBe(0)
  expect(result.staleDiagnosticForwardedCount).toBe(0)
  expect(result.outcome).toBe('stale-dropped')
  expect(result.status).toBe('completed')
  expect(result.auditEventId).toMatch(/^[0-9a-f-]{36}$/u)
  expect(result.serverResultDigest).toMatch(/^sha256:[0-9a-f]{64}$/u)
  return result
}

async function executeMaliciousServerExercise(
  request: APIRequestContext,
  prepared: PreparedLSP,
) {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const response = await request.post(
    `${subject.platform.apiOrigin}/v1/qualification/lsp-security-exercises`,
    {
      headers: {
        ...environment.credentials.platform.faultOperator.headers,
        'Idempotency-Key': qualificationKey('QG-LSP-003', 'malicious-server-exercise'),
      },
      data: {
        fixtureId: subject.fixtureId,
        runId: subject.runId,
        schemaVersion: 'worksflow-lsp-security-exercise-request/v1',
        sessionId: prepared.sandbox.session.id,
      },
    },
  )
  expect(
    response.status(),
    `A real controlled malicious language-server qualification route is required; response=${response.status()} ${await response.text()}`,
  ).toBe(200)
  expect(response.headers()['content-type'] ?? '').toMatch(/^application\/json(?:;|$)/iu)
  const result = exactKeys(await response.json(), [
    'attempts', 'candidateId', 'fixtureId', 'profileId', 'projectId', 'runId',
    'schemaVersion', 'sessionId', 'treeHashAfter', 'treeHashBefore',
  ], 'LSP security exercise result')
  expect(result.schemaVersion).toBe('worksflow-lsp-security-exercise-result/v1')
  expect(result.fixtureId).toBe(subject.fixtureId)
  expect(result.runId).toBe(subject.runId)
  expect(result.projectId).toBe(prepared.sandbox.projectId)
  expect(result.sessionId).toBe(prepared.sandbox.session.id)
  expect(result.candidateId).toBe(prepared.sandbox.candidate.id)
  expect(result.profileId).toBe(prepared.profile.id)
  expect(result.treeHashBefore).toBe(prepared.sandbox.candidate.treeHash)
  expect(result.treeHashAfter).toBe(prepared.sandbox.candidate.treeHash)
  expect(Array.isArray(result.attempts)).toBe(true)
  const attempts = (result.attempts as unknown[]).map((entry, index) => {
    const attempt = exactKeys(entry, [
      'auditEventId', 'gatewayForwarded', 'method', 'mutationCount', 'outcome',
      'terminated', 'vectorId',
    ], `LSP malicious attempt ${index}`)
    expect(attempt.vectorId).toBe(MALICIOUS_SERVER_VECTOR[index]?.vectorId)
    expect(attempt.method).toBe(MALICIOUS_SERVER_VECTOR[index]?.method)
    expect(attempt.outcome).toBe('rejected')
    expect(attempt.gatewayForwarded).toBe(false)
    expect(attempt.mutationCount).toBe(0)
    expect(attempt.terminated).toBe(true)
    expect(attempt.auditEventId).toMatch(/^[0-9a-f-]{36}$/u)
    return attempt
  })
  expect(attempts).toHaveLength(MALICIOUS_SERVER_VECTOR.length)
  return attempts
}

async function allSessionGatewayAudits(prepared: PreparedLSP) {
  const result = await prepared.sandbox.client.audit.list(prepared.sandbox.projectId, { limit: 200 })
  return result.data.items.flatMap((entry) => {
    const metadata = gatewayMetadata(entry.metadata, prepared.sandbox.session.id)
    if (!metadata) return []
    return [{
      actorId: entry.actorId,
      action: entry.action,
      createdAt: entry.createdAt,
      id: entry.id,
      metadata,
      targetId: entry.targetId,
    } satisfies GatewayAudit]
  })
}

test.describe('Golden LSP external qualification', () => {
  test('QG-LSP-001 binds an approved real language server and verifies its exact capabilities', async ({ browser, request }) => {
    const environment = goldenQualificationEnvironment()
    const subject = goldenSubject()
    const prepared = await prepareLSP('QG-LSP-001')
    const context = await browser.newContext({
      storageState: browserStorageStateWithSandbox(
        environment.credentials.platform.browserA,
        prepared.sandbox,
        'QG-LSP-001',
      ),
    })
    try {
      const page = await context.newPage()
      await openExactBrowserWorkspace(page, prepared)
      const binding = await runRealServerOracle(page, prepared)
      expect(binding.ticket.profiles).toHaveLength(1)
      const ticketProfile = binding.ticket.profiles[0]!
      const {
        effectiveLimits,
        templateRelease,
        ...discoveredProfile
      } = ticketProfile
      expect(binding.ticket.templateRelease).toEqual(prepared.templateRelease)
      expect(templateRelease).toEqual(prepared.templateRelease)
      expect(discoveredProfile).toEqual(prepared.profile)
      expect(binding.bound.languageServer).toEqual({
        profileId: ticketProfile.id,
        profileContentHash: ticketProfile.contentHash,
        runtimeImageDigest: ticketProfile.runtime.image,
        executableDigest: ticketProfile.runtime.executableDigest,
        serverName: ticketProfile.serverInfo.name,
        serverVersion: ticketProfile.serverInfo.version,
        capabilityAllowlistHash: ticketProfile.capabilityHash,
      })
      expect(binding.bound.languageServer.runtimeImageDigest.endsWith(
        `@${subject.lsp.runtime.imageDigest}`,
      )).toBe(true)
      expect(binding.bound.effectiveCapabilities).toEqual(ticketProfile.methods)
      expect(binding.bound.limits).toEqual(effectiveLimits)
      expect(binding.hello.limits).toEqual(effectiveLimits)
      expect(prepared.profile.isolation).toEqual({
        networkPolicy: 'none',
        workspaceMountPolicy: 'read-only',
        tempPolicy: 'isolated-bounded',
        cachePolicy: 'isolated-bounded',
        workspacePluginPolicy: 'forbidden',
        dynamicSdkPolicy: 'forbidden',
        dynamicRegistrationPolicy: 'forbidden',
        configurationCommandPolicy: 'forbidden',
        packageManagerHookPolicy: 'forbidden',
      })
      await observeRealRuntimeBinding(request, prepared, binding)
      await closeSocket(page, binding.key)
      await verifyTicketSingleUse(page, prepared)
      await enableBrowserLSP(page, prepared)
      const modelURI = candidateDocumentURI(
        prepared.sandbox.projectId,
        prepared.sandbox.candidate.id,
        prepared.document.path,
      )
      expect(await monacoSnapshot(page, modelURI)).toMatchObject({
        uri: modelURI,
        value: ORACLE_SOURCE,
      })
      await exerciseMonacoOracle(page)
    } finally {
      await context.close()
    }
  })

  test('QG-LSP-002 drops a stale binding, rebinds after head change, and enforces Candidate-CAS-only save', async ({ browser, request }) => {
    const environment = goldenQualificationEnvironment()
    let prepared = await prepareLSP('QG-LSP-002')
    const storageStateA = browserStorageStateWithSandbox(
      environment.credentials.platform.browserA,
      prepared.sandbox,
      'QG-LSP-002',
    )
    const storageStateB = browserStorageStateWithSandbox(
      environment.credentials.platform.browserB,
      prepared.sandbox,
      'QG-LSP-002',
    )
    const contextA = await browser.newContext({ storageState: storageStateA })
    const contextB = await browser.newContext({ storageState: storageStateB })
    try {
      const pageA = await contextA.newPage()
      const connectionTickets: string[] = []
      const staleReplies: string[] = []
      pageA.on('websocket', (socket) => {
        const url = new URL(socket.url())
        if (url.pathname !== goldenSubject().lsp.gateway.path) return
        const ticket = url.searchParams.get('ticket')
        if (ticket) connectionTickets.push(ticket)
        socket.on('framereceived', (event) => {
          try {
            const frame = JSON.parse(String(event.payload)) as Record<string, unknown>
            if (frame.kind === 'server.stale' && typeof frame.replyTo === 'string') {
              staleReplies.push(frame.replyTo)
            }
          } catch {
            // A non-JSON frame is not evidence for the strict stale envelope.
          }
        })
      })
      await openExactBrowserWorkspace(pageA, prepared)
      const status = await enableBrowserLSP(pageA, prepared)
      const modelURI = candidateDocumentURI(
        prepared.sandbox.projectId,
        prepared.sandbox.candidate.id,
        prepared.document.path,
      )
      const completionSave = pageA.waitForResponse((response) => (
        isCandidatePut(response.url(), response.request().method(), prepared)
      ))
      await acceptOracleCompletion(pageA)
      expect((await completionSave).status()).toBe(200)
      await expect(status).toContainText('rebound after Candidate CAS without reloading Monaco.', {
        timeout: 30_000,
      })
      prepared = await refreshPrepared(prepared)
      expect(await readPreparedDocument(prepared)).toContain(ORACLE_VALUE)

      const editorA = pageA.locator('.monaco-editor textarea').first()
      const undoProbe = `LSP_UNDO_${goldenSubject().runId.replaceAll('-', '_')}`
      const probeSave = pageA.waitForResponse((response) => (
        isCandidatePut(response.url(), response.request().method(), prepared)
      ))
      await editorA.click()
      await pageA.keyboard.press('Control+End')
      await pageA.keyboard.insertText(`\n// ${undoProbe}`)
      expect((await probeSave).status()).toBe(200)
      await expect(status).toContainText('rebound after Candidate CAS without reloading Monaco.', {
        timeout: 30_000,
      })
      prepared = await refreshPrepared(prepared)
      await triggerUIHover(pageA)
      const beforeAudits = await gatewayRequestAudits(prepared, 'textDocument/hover', 1)
      const beforeAudit = beforeAudits.at(-1)!
      const beforeDocument = auditDocument(beforeAudit)
      const beforeDisconnect = await monacoSnapshot(pageA, modelURI)
      expect(beforeDocument.modelUri).toBe(modelURI)
      expect(beforeDocument.modelVersion).toBe(beforeDisconnect.modelVersion)

      await contextA.setOffline(true)
      await expect(status).toContainText(/(?:disconnected|unavailable|WebSocket closed)/iu, {
        timeout: 30_000,
      })
      await contextA.setOffline(false)
      await pageA.getByRole('button', { name: 'Refresh exact head and reconnect', exact: true }).click()
      await expect(status).toContainText('is bound to the exact Candidate head.', { timeout: 45_000 })
      await expect.poll(() => connectionTickets.length).toBeGreaterThanOrEqual(2)
      expect(new Set(connectionTickets).size).toBe(connectionTickets.length)
      const afterReconnect = await monacoSnapshot(pageA, modelURI)
      expect(afterReconnect).toEqual(beforeDisconnect)
      await triggerUIHover(pageA)
      const afterAudits = await gatewayRequestAudits(
        prepared,
        'textDocument/hover',
        beforeAudits.length + 1,
      )
      const afterAudit = afterAudits.at(-1)!
      const afterDocument = auditDocument(afterAudit)
      expect(afterAudit.targetId).not.toBe(beforeAudit.targetId)
      expect(afterDocument).toEqual(beforeDocument)

      await editorA.click()
      await pageA.keyboard.press('Control+z')
      await expect.poll(async () => (await monacoSnapshot(pageA, modelURI)).value).not.toContain(undoProbe)
      await pageA.keyboard.press('Control+Shift+z')
      await expect.poll(async () => (await monacoSnapshot(pageA, modelURI)).value).toContain(undoProbe)

      await triggerUIHover(pageA)
      const liveAudits = await gatewayRequestAudits(
        prepared,
        'textDocument/hover',
        afterAudits.length + 1,
      )
      const liveAudit = liveAudits.at(-1)!
      const liveDocument = auditDocument(liveAudit)
      const liveOpeningHead = exactRecord(
        liveAudit.metadata.sandboxHeadFence,
        'in-flight LSP opening head',
      )
      const beforeHeldRequest = await monacoSnapshot(pageA, modelURI)
      expect(liveAudit.targetId).toBe(afterAudit.targetId)
      expect(liveOpeningHead).toEqual(prepared.head)
      expect(liveDocument.modelUri).toBe(modelURI)
      expect(liveDocument.openId).toBe(beforeDocument.openId)
      expect(liveDocument.modelVersion).toBe(beforeHeldRequest.modelVersion)
      const armed = await armStaleResponseHold(
        request,
        prepared,
        liveAudit.targetId,
        liveOpeningHead,
        liveDocument,
      )
      await editorA.click()
      await pageA.keyboard.press('Control+End')
      await pageA.keyboard.press('Control+Space')
      const held = await waitForHeldStaleResponse(
        request,
        prepared,
        liveAudit.targetId,
        String(armed.holdId),
      )
      expect(held.openingHead).toEqual(liveOpeningHead)
      expect(held.documentFence).toEqual(liveDocument)
      expect(held.requestId).toMatch(/^[0-9a-f-]{36}$/u)
      expect(held.serverResultDigest).toMatch(/^sha256:[0-9a-f]{64}$/u)

      const pageB = await contextB.newPage()
      await openExactBrowserWorkspace(pageB, prepared)
      const editorB = pageB.locator('.monaco-editor textarea').first()
      const remoteMarker = `LSP_REMOTE_${goldenSubject().runId.replaceAll('-', '_')}`
      const localMarker = `LSP_DIRTY_${goldenSubject().runId.replaceAll('-', '_')}`
      const remoteResponse = pageB.waitForResponse((response) => (
        isCandidatePut(response.url(), response.request().method(), prepared)
      ))
      await editorB.click()
      await pageB.keyboard.press('Control+End')
      await pageB.keyboard.insertText(`\n// ${remoteMarker}`)
      expect((await remoteResponse).status()).toBe(200)
      prepared = await refreshPrepared(prepared)
      const released = await releaseStaleResponseHold(
        request,
        prepared,
        liveAudit.targetId,
        String(armed.holdId),
      )
      expect(released.requestId).toBe(held.requestId)
      expect(released.serverResultDigest).toBe(held.serverResultDigest)
      expect(released.openingHead).toEqual(liveOpeningHead)
      expect(released.successorHead).toEqual(prepared.head)
      expect(released.documentFence).toEqual(liveDocument)
      await expect.poll(() => staleReplies).toContain(String(held.requestId))
      await expect(pageA.locator('.suggest-widget.visible')).toHaveCount(0)
      await expect(pageA.getByRole('button', { name: /^Problems\s+0$/u })).toBeVisible()
      expect(await monacoSnapshot(pageA, modelURI)).toEqual(beforeHeldRequest)

      const staleAudits = await waitForValue(
        () => allSessionGatewayAudits(prepared),
        (events) => events.some((entry) => entry.id === released.auditEventId),
        'durable exact in-flight stale-drop audit',
      )
      const staleAudit = staleAudits.find((entry) => entry.id === released.auditEventId)!
      expect(staleAudit.action).toBe('lsp.gateway.request.stale')
      expect(staleAudit.targetId).toBe(liveAudit.targetId)
      expect(staleAudit.metadata.method).toBe('textDocument/completion')
      expect(staleAudit.metadata.outcome).toBe('stale')
      expect(staleAudit.metadata.sandboxHeadFence).toEqual(liveOpeningHead)
      expect(staleAudit.metadata.documentFence).toEqual(liveDocument)

      const staleResponse = pageA.waitForResponse((response) => (
        isCandidatePut(response.url(), response.request().method(), prepared)
        && response.status() === 409
      ))
      await editorA.click()
      await pageA.keyboard.press('Control+End')
      await pageA.keyboard.insertText(`\n// ${localMarker}`)
      expect((await staleResponse).status()).toBe(409)
      await expect(pageA.getByRole('alert')).toContainText('Autosave needs reconciliation')
      const dirtyModel = await monacoSnapshot(pageA, modelURI)
      expect(dirtyModel.value).toContain(localMarker)
      expect(dirtyModel.value).not.toContain(remoteMarker)
      prepared = await refreshPrepared(prepared)
      const authoritative = await readPreparedDocument(prepared)
      expect(authoritative).toContain(remoteMarker)
      expect(authoritative).not.toContain(localMarker)
      expect(prepared.head.treeHash).not.toBe(liveOpeningHead.treeHash)
    } finally {
      await contextA.close()
      await contextB.close()
    }
  })

  test('QG-LSP-003 rejects malicious protocol behavior and closes crash, drift, resource, and audit privacy faults', async ({ browser, request }) => {
    const environment = goldenQualificationEnvironment()
    let prepared = await prepareLSP('QG-LSP-003')
    const context = await browser.newContext({
      storageState: browserStorageStateWithSandbox(
        environment.credentials.platform.browserA,
        prepared.sandbox,
        'QG-LSP-003',
      ),
    })
    const ticketSecrets: string[] = []
    const privacyNonces: string[] = []
    const resultIds = new Set<string>()
    try {
      const page = await context.newPage()
      let streamTickets = 0
      page.on('request', (observed) => {
        if (/\/connection-tickets$/u.test(observed.url())) streamTickets += 1
      })
      page.on('websocket', (socket) => {
        const ticket = new URL(socket.url()).searchParams.get('ticket')
        if (ticket) ticketSecrets.push(ticket)
      })
      await openExactBrowserWorkspace(page, prepared)
      const status = await enableBrowserLSP(page, prepared)
      await triggerUIHover(page)
      const uiAudits = await gatewayRequestAudits(prepared, 'textDocument/hover', 1)
      const maliciousAttempts = await executeMaliciousServerExercise(request, prepared)
      const treeAfterExercise = await prepared.sandbox.client.sandbox.getTree(prepared.sandbox.session.id)
      expect(treeAfterExercise.data.candidate.treeHash).toBe(prepared.sandbox.candidate.treeHash)
      expect(treeAfterExercise.data.tree.treeHash).toBe(prepared.sandbox.candidate.treeHash)

      const faultClose = new Map<GoldenFaultOperation, number>([
        ['lsp-runtime-crash', LSP_CLOSE_RUNTIME_UNAVAILABLE],
        ['lsp-runtime-drift', LSP_CLOSE_CAPABILITY_VIOLATION],
        ['lsp-resource-pressure', LSP_CLOSE_RESOURCE_EXHAUSTED],
      ])
      for (const fault of [
        'lsp-runtime-crash',
        'lsp-runtime-drift',
        'lsp-resource-pressure',
      ] as const) {
        const receipt = await consumeGoldenFault(request, fault)
        expect(receipt.operationKind).toBe(fault)
        expect(receipt.outcome).toBe('applied')
        expect(receipt.resolvedResourceId).toBe(prepared.sandbox.session.id)
        expect(resultIds.has(String(receipt.resultId))).toBe(false)
        resultIds.add(String(receipt.resultId))
        await expect(status).toContainText(`WSS ${faultClose.get(fault)}`, { timeout: 45_000 })
        if (fault === 'lsp-resource-pressure') {
          const autosaveNonce = `LSP_RESOURCE_AUTOSAVE_${goldenSubject().runId.replaceAll('-', '_')}`
          const save = page.waitForResponse((response) => (
            isCandidatePut(response.url(), response.request().method(), prepared)
          ))
          const editor = page.locator('.monaco-editor textarea').first()
          await editor.click()
          await page.keyboard.press('Control+End')
          await page.keyboard.insertText(`\n// ${autosaveNonce}`)
          expect((await save).status()).toBe(200)
          prepared = await refreshPrepared(prepared)
          expect(await readPreparedDocument(prepared)).toContain(autosaveNonce)
          privacyNonces.push(autosaveNonce)
          const ticketsBeforeTerminal = streamTickets
          await page.getByRole('button', { name: 'Terminal', exact: true }).click()
          const terminal = page.locator('.xterm-helper-textarea')
          await expect(terminal).toBeVisible()
          await expect.poll(() => streamTickets).toBeGreaterThan(ticketsBeforeTerminal)
          const ptyNonce = `LSP_RESOURCE_PTY_${goldenSubject().runId.replaceAll('-', '')}`
          await terminal.fill(`printf '%s\\n' '${ptyNonce}'`)
          await page.keyboard.press('Enter')
          await expect(page.locator('.xterm-rows')).toContainText(ptyNonce, { timeout: 30_000 })
          await expect(page.getByRole('button', { name: 'Close terminal' }).locator('..')).toContainText('open')
          privacyNonces.push(ptyNonce)
        }
        await page.getByRole('button', { name: 'Refresh exact head and reconnect', exact: true }).click()
        await expect(status).toContainText('is bound to the exact Candidate head.', { timeout: 45_000 })
      }

      const exerciseAuditIds = new Set(maliciousAttempts.map((entry) => String(entry.auditEventId)))
      const audits = await waitForValue(
        () => allSessionGatewayAudits(prepared),
        (events) => [...exerciseAuditIds].every((id) => events.some((entry) => entry.id === id)),
        'controlled malicious-server Gateway audits',
      )
      expect(uiAudits.some((entry) => entry.actorId === goldenPrincipal('platform-user-a').actorId)).toBe(true)
      for (const event of audits) {
        expect(event.actorId).toBe(goldenPrincipal('platform-user-a').actorId)
        expect(event.targetId).toMatch(/^[0-9a-f-]{36}$/u)
        const head = exactRecord(event.metadata.sandboxHeadFence, 'audited LSP head')
        expect(head.projectId).toBe(prepared.sandbox.projectId)
        expect(head.sessionId).toBe(prepared.sandbox.session.id)
        expect(head.candidateId).toBe(prepared.sandbox.candidate.id)
        expect(head.treeHash).toMatch(/^sha256:[0-9a-f]{64}$/u)
        expect(event.metadata.profile).toEqual({
          id: prepared.profile.id,
          contentHash: prepared.profile.contentHash,
          image: prepared.profile.runtime.image,
          executableDigest: prepared.profile.runtime.executableDigest,
          capabilityHash: prepared.profile.capabilityHash,
        })
        expect(event.metadata.outcome).toEqual(expect.any(String))
        if (event.action.startsWith('lsp.gateway.request.')) {
          expect(event.metadata.method).toEqual(expect.any(String))
        }
      }
      const encodedAudit = JSON.stringify(audits)
      const lowerAudit = encodedAudit.toLowerCase()
      expect(ticketSecrets.length).toBeGreaterThan(0)
      expect(new Set(ticketSecrets).size).toBe(ticketSecrets.length)
      for (const secret of ticketSecrets) {
        expect(secret).toMatch(/^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/u)
        expect(lowerAudit).not.toContain(secret.toLowerCase())
      }
      expect(privacyNonces).toHaveLength(2)
      for (const privateValue of [
        ...privacyNonces,
        ORACLE_SOURCE,
        ORACLE_VALUE,
        ORACLE_PARTIAL,
      ]) expect(lowerAudit).not.toContain(privateValue.toLowerCase())
      expect(encodedAudit).not.toMatch(
        /"(?:authorization|cookie|secret|ticketSecret|bearerSecret|source|sourceText|unsavedText|diagnostic|diagnostics|completion|completions|completionItems|completionBody|completionText)"\s*:/iu,
      )
    } finally {
      await context.close()
    }
  })
})
