import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import * as React from 'react'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { SandboxLSPStatus } from '../components/worksflow/workbench/sandbox-lsp-status'
import {
  resolveSandboxLSPAdmission,
  type SandboxLSPAdmissionDecision,
} from '../components/worksflow/workbench/sandbox-lsp-admission'
import {
  projectSandboxLSPDiagnostics,
  useSandboxLSP,
} from '../components/worksflow/workbench/use-sandbox-lsp'
import {
  SandboxLSPError,
  type LSPServerEnvelopeDto,
  type LSPTemplateProfileDto,
} from '../lib/platform/lsp-contract'
import type {
  CandidateWorkspaceDto,
  SandboxFences,
  SandboxSessionDto,
} from '../lib/platform/sandbox-contract'

// Jiti's opt-in JSX transform is classic; production Next.js uses the
// automatic runtime. Supply the equivalent test-only global before rendering.
;(globalThis as typeof globalThis & { React: typeof React }).React = React

const digest = (character: string) => `sha256:${character.repeat(64)}`
const projectId = '11111111-1111-4111-8111-111111111111'
const sessionId = '22222222-2222-4222-8222-222222222222'
const candidateId = '33333333-3333-4333-8333-333333333333'
const release = {
  id: '44444444-4444-4444-8444-444444444444',
  contentHash: digest('4'),
} as const
const treeHash = digest('1')
const fileHash = digest('2')
const now = Date.now()

const candidateState = {
  id: candidateId,
  repositorySnapshotId: '55555555-5555-4555-8555-555555555555',
  status: 'active',
  baseTreeHash: digest('0'),
  treeHash,
  version: 7,
  journalSequence: 12,
  sessionEpoch: 3,
  writerLeaseEpoch: 2,
  dirty: true,
  conflicted: false,
  stale: false,
  rebaseRequired: false,
  updatedAt: new Date(now).toISOString(),
} as const

const candidate: CandidateWorkspaceDto = {
  ...candidateState,
  schemaVersion: 'repository-candidate/v1',
  projectId,
  buildManifest: { id: 'manifest', contentHash: digest('5') },
  buildContract: { id: 'contract', contentHash: digest('6') },
  fullStackTemplate: { id: 'template', contentHash: digest('7') },
  currentTree: {
    schemaVersion: 'repository-tree/v1',
    treeHash,
    files: [{ path: 'src/page.ts', mode: '100644', contentHash: fileHash, byteSize: 20 }],
  },
  lease: {
    ownerId: 'actor-1',
    epoch: 2,
    expiresAt: new Date(now + 60_000).toISOString(),
  },
  createdBy: 'actor-1',
  createdAt: new Date(now - 60_000).toISOString(),
}

const session: SandboxSessionDto = {
  schemaVersion: 'sandbox-session/v1',
  id: sessionId,
  projectId,
  actorId: 'actor-1',
  buildManifest: candidate.buildManifest,
  buildContract: candidate.buildContract,
  fullStackTemplate: candidate.fullStackTemplate,
  templateReleases: [release],
  runnerImageDigest: digest('8'),
  candidate: candidateState,
  sessionEpoch: 3,
  state: 'ready',
  version: 9,
  ttl: {
    policy: { idleHibernateAfter: 1_800, maxRuntime: 14_400 },
    idleDeadline: new Date(now + 60_000).toISOString(),
    expiresAt: new Date(now + 120_000).toISOString(),
  },
  quota: {
    cpuMillis: 2_000,
    memoryBytes: 2 ** 30,
    workspaceBytes: 2 ** 30,
    pidLimit: 128,
    previewPortLimit: 4,
  },
  allowedServices: [{ id: 'web', kind: 'frontend', profiles: ['dev'], templateRelease: release }],
  allowedPorts: [],
  allowedActions: ['view', 'edit'],
  blockingReasons: [],
  lastTransition: {
    to: 'ready',
    reason: 'Sandbox runtime is ready.',
    at: new Date(now - 30_000).toISOString(),
  },
  createdAt: new Date(now - 60_000).toISOString(),
  updatedAt: new Date(now).toISOString(),
}

const fences: SandboxFences = {
  etag: '"sandbox-session:9"',
  sessionEpoch: 3,
  candidateVersion: 7,
  writerLeaseEpoch: 2,
  treeHash,
}

function decision(overrides: Partial<Parameters<typeof resolveSandboxLSPAdmission>[0]> = {}) {
  return resolveSandboxLSPAdmission({
    projectId,
    canEdit: true,
    session,
    candidate,
    fences,
    selectedServiceId: 'web',
    document: {
      path: 'src/page.ts',
      contentHash: fileHash,
      binary: false,
      stale: false,
    },
    now,
    ...overrides,
  })
}

const admitted = decision()
assert.equal(admitted.eligible, true)
if (!admitted.eligible) assert.fail('expected exact Candidate LSP admission')
assert.equal(
  admitted.admission.modelUri,
  `worksflow-candidate://${projectId}/${candidateId}/src/page.ts`,
)
assert.deepEqual(admitted.admission.templateRelease, release)
assert.equal(decision({ canEdit: false }).eligible, false)
assert.equal(decision({ candidate: { ...candidate, stale: true } }).eligible, false)
assert.equal(decision({ fences: { ...fences, treeHash: digest('9') } }).eligible, false)
assert.equal(decision({
  candidate: {
    ...candidate,
    lease: { ...candidate.lease!, expiresAt: new Date(now - 1).toISOString() },
  },
}).eligible, false)
assert.equal(decision({
  document: { path: 'src/page.ts', contentHash: digest('3'), binary: false, stale: false },
}).eligible, false)
assert.equal(decision({ selectedServiceId: 'api' }).eligible, false)

const successorTreeHash = digest('a')
const successorFileHash = digest('b')
const successorCandidate: CandidateWorkspaceDto = {
  ...candidate,
  version: 8,
  journalSequence: 13,
  treeHash: successorTreeHash,
  currentTree: {
    ...candidate.currentTree,
    treeHash: successorTreeHash,
    files: [{
      path: 'src/page.ts',
      mode: '100644',
      contentHash: successorFileHash,
      byteSize: 20,
    }],
  },
}
const successor = decision({
  candidate: successorCandidate,
  session: {
    ...session,
    candidate: {
      ...session.candidate,
      version: 8,
      journalSequence: 13,
      treeHash: successorTreeHash,
    },
  },
  fences: {
    ...fences,
    candidateVersion: 8,
    treeHash: successorTreeHash,
  },
  document: {
    path: 'src/page.ts',
    contentHash: successorFileHash,
    binary: false,
    stale: false,
  },
})
assert.equal(successor.eligible, true)
if (!successor.eligible) assert.fail('expected successor Candidate LSP admission')
assert.equal(
  successor.admission.modelUri,
  admitted.admission.modelUri,
  'Candidate CAS/head refresh must retain the same Monaco model URI',
)

const profile = {
  id: 'typescript-lsp',
  serverInfo: { name: 'typescript-language-server', version: '4.3.0' },
} as unknown as LSPTemplateProfileDto
const statusHTML = renderToStaticMarkup(
  createElement(SandboxLSPStatus, {
    view: {
      status: 'stale',
      detail: 'The exact Candidate binding is stale.',
      closeCode: 4409,
    },
    profiles: [profile],
    selectedProfileId: profile.id,
    enabled: true,
    onProfile: () => undefined,
    onEnable: () => undefined,
    onDisable: () => undefined,
    onRetry: () => undefined,
  }),
)
assert.match(statusHTML, /typescript-language-server 4\.3\.0/)
assert.match(statusHTML, /WSS 4409/)
assert.match(statusHTML, /Refresh exact head and reconnect/)
assert.match(statusHTML, /Disable LSP/)

function HookInitialState({ admission }: { readonly admission: SandboxLSPAdmissionDecision }) {
  const lsp = useSandboxLSP({
    admission,
    languageId: 'typescript',
    onRefreshExactHead: async () => undefined,
    onMarkers: () => undefined,
  })
  return createElement('span', null, `${lsp.enabled ? 'enabled' : 'disabled'}:${lsp.view.status}`)
}

assert.equal(
  renderToStaticMarkup(createElement(HookInitialState, { admission: admitted })),
  '<span>disabled:disabled</span>',
  'an exact Candidate head still requires explicit user enablement',
)
const blocked = decision({ canEdit: false })
assert.equal(
  renderToStaticMarkup(createElement(HookInitialState, { admission: blocked })),
  '<span>disabled:blocked</span>',
  'the hook fails closed before profile discovery or ticket issuance',
)

let unsafeDiagnosticQuarantines = 0
let unsafeDiagnosticSummaries = 0
const unsafeDiagnosticHandled = projectSandboxLSPDiagnostics({
  adapter: {
    projectDiagnostics: () => {
      throw new SandboxLSPError('lsp_message_malformed')
    },
  },
  monaco: {
    MarkerSeverity: { Hint: 1, Info: 2, Warning: 4, Error: 8 },
    editor: { setModelMarkers: () => assert.fail('unsafe diagnostics must not set markers') },
  },
} as unknown as Parameters<typeof projectSandboxLSPDiagnostics>[0], {
  kind: 'server.diagnostics',
} as LSPServerEnvelopeDto, () => {
  unsafeDiagnosticSummaries += 1
}, () => {
  unsafeDiagnosticQuarantines += 1
})
assert.equal(unsafeDiagnosticHandled, true)
assert.equal(unsafeDiagnosticQuarantines, 1)
assert.equal(unsafeDiagnosticSummaries, 0)

const workspaceSource = readFileSync(
  new URL('../components/worksflow/workbench/sandbox-workspace.tsx', import.meta.url),
  'utf8',
)
const hookSource = readFileSync(
  new URL('../components/worksflow/workbench/use-sandbox-lsp.ts', import.meta.url),
  'utf8',
)
const monacoAdapterSource = readFileSync(
  new URL('../lib/platform/lsp-monaco.ts', import.meta.url),
  'utf8',
)
assert.match(workspaceSource, /path=\{editorModelURI\}[\s\S]*keepCurrentModel/)
assert.match(workspaceSource, /value=\{selectedFile\.draft\}/)
assert.match(hookSource, /active\.session\.headRebind\(/)
assert.match(hookSource, /active\.adapter\.rebindHead\(/)
assert.match(hookSource, /active\.heartbeatPending/)
assert.match(hookSource, /window\.setInterval\(pulse, 10_000\)/)
assert.match(hookSource, /}, 8_000\)/)
assert.match(hookSource, /token\.onCancellationRequested\(\(\) =>/)
assert.match(hookSource, /request\.cancel\(\)/)
assert.match(hookSource, /Math\.min\(profile\.limits\.requestTimeoutMillis, 10_000\)/)
assert.match(hookSource, /target as unknown as ProductionLSPMonacoModel\) !== model/)
assert.match(hookSource, /requireExactMonacoRange\(requested, target\.validateRange\(requested\)\)/)
assert.match(monacoAdapterSource, /model\.validateRange\(requested\)/)
assert.match(hookSource, /projectSandboxLSPDiagnostics\(active, envelope, onMarkers,[\s\S]*?quarantineBinding\(active, true\)/)
for (const provider of [
  'registerCompletionItemProvider',
  'registerHoverProvider',
  'registerSignatureHelpProvider',
  'registerDocumentHighlightProvider',
  'registerDocumentSymbolProvider',
  'registerDefinitionProvider',
  'registerDeclarationProvider',
  'registerImplementationProvider',
  'registerTypeDefinitionProvider',
  'registerReferenceProvider',
]) assert.match(hookSource, new RegExp(provider))
assert.doesNotMatch(hookSource, /\b(?:model|active\.model)\.(?:setValue|dispose)\s*\(/)

console.log('Sandbox LSP admission, hook, and status UI tests passed')
