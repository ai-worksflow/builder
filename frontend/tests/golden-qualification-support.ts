import assert from 'node:assert/strict'

import {
  request as playwrightRequest,
  type APIRequestContext,
  type BrowserContextOptions,
} from './qualification-runtime'

import { PlatformClient } from '@/lib/platform/client'
import { PlatformHttpError } from '@/lib/platform/http'
import {
  sandboxFences,
  type CandidateWorkspaceDto,
  type SandboxFences,
  type SandboxRepositoryViewDto,
  type SandboxSessionDto,
} from '@/lib/platform/sandbox-contract'
import type { ReleaseBundleDto } from '@/lib/platform/release-contract'

import {
  goldenQualificationEnvironment,
  type GoldenQualificationEnvironment,
} from './qualification-runtime'

const digestPattern = /^sha256:[0-9a-f]{64}$/
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i

type ExactIdentity = Readonly<{ id: string; contentHash: string }>
type ApprovalIdentity = ExactIdentity & Readonly<{ approvalReceiptDigest: string }>
type ServiceIdentity = string
type GoldenBearer = GoldenQualificationEnvironment['credentials']['platform']['apiA']
type GoldenStorage = GoldenQualificationEnvironment['credentials']['platform']['browserA']
type PlaywrightStorageState = Exclude<
  NonNullable<BrowserContextOptions['storageState']>,
  string
>

export type GoldenFaultOperation =
  | 'agent-runner-crash'
  | 'agent-runner-timeout'
  | 'agent-security-canary'
  | 'controller-conflict'
  | 'controller-maintenance'
  | 'controller-not-found'
  | 'controller-timeout'
  | 'lsp-resource-pressure'
  | 'lsp-runtime-crash'
  | 'lsp-runtime-drift'
  | 'reference-gateway-outage'
  | 'reference-process-restart'
  | 'sandbox-dependency-crash'

export type GoldenFaultReceipt = Readonly<{
  adapterInvocationId: string
  adapterResultDigest: string
  authorityId: string
  completedAt: string
  envelopeDigest: string
  expectedFenceDigest: string
  fixtureId: string
  observedFenceDigest: string
  observedHeadDigest: string
  operationKind: GoldenFaultOperation
  outcome: 'applied' | 'refused'
  payloadDigest: string
  predicateDigest: string
  reservedAt: string
  resolutionDigest: string
  resolvedFenceDigest: string
  resolvedHeadDigest: string
  resolvedResourceId: string
  resourceSelector: string
  resultId: string
  runId: string
  schemaVersion: 'worksflow-golden-fault-consume-receipt/v1'
}>

export type GoldenFaultTarget = Readonly<{
  id: string
  kind:
    | 'agent-attempt'
    | 'reference-application'
    | 'reference-run'
    | 'release-delivery-operation'
    | 'sandbox-session'
  projectId: string
}>

export type GoldenFixtureSubject = GoldenQualificationEnvironment['fixture']['subject'] & Readonly<{
  agent: Readonly<{
    modelGateway: Readonly<{
      attestationDigest: string
      identity: ServiceIdentity
      modelId: string
      modelRevision: string
      profileId: string
      providerId: string
    }>
    runner: Readonly<{ identity: ServiceIdentity; imageDigest: string; profileId: string }>
  }>
  faultAuthorities: readonly Readonly<{
    authorityId: string
    dsse: Readonly<{
      artifactId: string
      envelopeDigest: string
      payloadDigest: string
      payloadType: string
    }>
    expectedFenceDigest: string
    maxUses: 1
    operationKind: GoldenFaultOperation
    resourceSelector: string
  }>[]
  lsp: Readonly<{
    gateway: Readonly<{
      apiOrigin: string
      path: '/v1/sandbox-lsp'
      ticketProtocolDigest: string
      wssProtocolDigest: string
    }>
    runtime: Readonly<{
      capabilityDigest: string
      identity: ServiceIdentity
      imageDigest: string
      languages: readonly string[]
      profileId: string
    }>
  }>
  platform: Readonly<{
    apiOrigin: string
    apiSchemaDigest: string
    deploymentReceipt: ExactIdentity
    serverBuild: Readonly<{ buildId: string; imageDigest: string; version: string }>
    webOrigin: string
    wssProtocolDigest: string
  }>
  reference: Readonly<{
    apiImageDigest: string
    apiOrigin: string
    applicationId: string
    contractBundle: ExactIdentity
    deploymentReceipt: ExactIdentity
    migration: Readonly<{ contentHash: string; identity: string }>
    retentionPolicy: ExactIdentity
    runEventSchemaDigest: string
    webImageDigest: string
    webOrigin: string
  }>
  release: Readonly<{
    controller: Readonly<{
      identity: ServiceIdentity
      imageDigest: string
      profileId: string
      protocol: string
      trustKeyDigest: string
    }>
  }>
  sandbox: Readonly<{
    apiOrigin: string
    runner: Readonly<{ identity: ServiceIdentity; imageDigest: string; profileId: string }>
    runtimeProfileId: string
    serviceProfiles: readonly Readonly<{
      id: string
      imageDigest: string
      protocol: 'http' | 'websocket'
      service: string
    }>[]
  }>
  sharedArtifacts: Readonly<{
    buildContract: ExactIdentity
    buildManifest: ExactIdentity
    referenceContractBundle: ExactIdentity
    runtimeImages: readonly Readonly<{
      imageDigest: string
      provenance: ExactIdentity
      role: string
      sbom: ExactIdentity
      signature: ExactIdentity
    }>[]
    sourceRepository: Readonly<{ commitOid: string; contentTreeDigest: string }>
    templateRelease: ApprovalIdentity
    workspaceRevision: Readonly<{
      canonicalQualityReceiptDigest: string
      contentHash: string
      id: string
    }>
  }>
}>

export type GoldenSandbox = Readonly<{
  client: PlatformClient
  projectId: string
  candidate: CandidateWorkspaceDto
  fences: SandboxFences
  session: SandboxSessionDto
  tree: SandboxRepositoryViewDto
}>

export type GoldenRelease = Readonly<{
  bundle: ReleaseBundleDto
  canonicalReceipt: ExactIdentity
  client: PlatformClient
  projectId: string
}>

function exactRecord(value: unknown, keys: readonly string[], label: string): Record<string, unknown> {
  assert.ok(value && typeof value === 'object' && !Array.isArray(value), `${label} must be an object`)
  const source = value as Record<string, unknown>
  assert.deepEqual(Object.keys(source).sort(), [...keys].sort(), `${label} shape drift`)
  return source
}

function exactString(value: unknown, label: string) {
  assert.equal(typeof value, 'string', `${label} must be a string`)
  assert.ok((value as string).length > 0, `${label} must not be empty`)
  return value as string
}

function exactUUID(value: unknown, label: string) {
  const result = exactString(value, label)
  assert.match(result, uuidPattern, `${label} must be a UUID v4`)
  return result
}

function exactDigest(value: unknown, label: string) {
  const result = exactString(value, label)
  assert.match(result, digestPattern, `${label} must be a sha256 digest`)
  return result
}

function exactTimestamp(value: unknown, label: string) {
  const result = exactString(value, label)
  assert.ok(Number.isFinite(Date.parse(result)), `${label} must be an RFC3339 timestamp`)
  return result
}

export function goldenSubject() {
  return goldenQualificationEnvironment().fixture.subject as GoldenFixtureSubject
}

export function goldenPrincipal(slot: string) {
  const principal = goldenSubject().principals.find((entry) => entry.slot === slot)
  assert.ok(principal, `Golden fixture principal ${slot} is required`)
  return principal
}

export function qualificationKey(caseId: string, operation: string) {
  const runId = goldenSubject().runId
  const value = `golden:${runId}:${caseId}:${operation}`
  assert.ok(value.length <= 200)
  return value
}

export function platformClient(
  credential: GoldenBearer = goldenQualificationEnvironment().credentials.platform.apiA,
) {
  const subject = goldenSubject()
  assert.equal(credential.audience, subject.credentialSet.audience)
  return new PlatformClient({
    http: {
      baseUrl: subject.platform.apiOrigin,
      defaultHeaders: {
        ...credential.headers,
        Origin: subject.platform.webOrigin,
      },
      defaultTimeoutMs: 30_000,
    },
  })
}

export function browserStorageState(credential: GoldenStorage) {
  // The strict Golden loader has already validated every cookie and origin;
  // clone the readonly authority projection into Playwright's mutable option shape.
  return structuredClone(credential.storageState) as unknown as PlaywrightStorageState
}

export function browserMutationHeaders(credential: GoldenStorage) {
  const subject = goldenSubject()
  assert.equal(credential.audience, subject.platform.webOrigin)
  assert.ok(credential.csrf, 'Platform browser mutation credential must declare CSRF binding')
  const cookie = credential.storageState.cookies.find((entry) => (
    entry.name === credential.csrf!.cookieName
  ))
  assert.ok(cookie && typeof cookie.value === 'string' && cookie.value.length >= 16,
    'Platform browser mutation CSRF cookie is required')
  return {
    Origin: subject.platform.webOrigin,
    [credential.csrf.headerName]: cookie.value as string,
  }
}

/**
 * Projects the server-created exact Candidate and SandboxSession into the two
 * durable browser pointers consumed by SandboxWorkspace. This is deliberately
 * not a second bootstrap path: opening the UI must prove that it reuses the
 * already-authorized identities instead of silently creating successors.
 */
export function browserStorageStateWithSandbox(
  credential: GoldenStorage,
  sandbox: Pick<GoldenSandbox, 'candidate' | 'projectId' | 'session'>,
  caseId: string,
) {
  const state = browserStorageState(credential)
  const subject = goldenSubject()
  const origin = state.origins.find((entry) => entry.origin === subject.platform.webOrigin)
  const target = origin ?? { origin: subject.platform.webOrigin, localStorage: [] }
  if (!origin) state.origins.push(target)
  const values = new Map(target.localStorage.map((entry) => [entry.name, entry.value]))
  values.set(
    `worksflow:sandbox:${sandbox.projectId}:${subject.sharedArtifacts.buildManifest.id}`,
    JSON.stringify({
      candidateId: sandbox.candidate.id,
      sessionId: sandbox.session.id,
      sessionKey: qualificationKey(caseId, 'sandbox-open'),
    }),
  )
  values.set(
    `worksflow:sandbox-active-candidate:${sandbox.projectId}`,
    JSON.stringify({
      buildManifestId: subject.sharedArtifacts.buildManifest.id,
      candidateId: sandbox.candidate.id,
    }),
  )
  target.localStorage.splice(
    0,
    target.localStorage.length,
    ...[...values].sort(([left], [right]) => left.localeCompare(right)).map(([name, value]) => ({ name, value })),
  )
  return state
}

export async function referenceAPIContext(
  credential: GoldenQualificationEnvironment['credentials']['reference']['apiA'],
) {
  const subject = goldenSubject()
  assert.equal(credential.audience, 'reference-api')
  const extraHTTPHeaders: Record<string, string> = {
    Accept: 'application/json, text/event-stream',
    Origin: subject.reference.webOrigin,
  }
  if (credential.csrf) {
    const cookie = credential.storageState.cookies.find((entry) => (
      entry.name === credential.csrf!.cookieName
    ))
    assert.ok(cookie && typeof cookie.value === 'string' && cookie.value.length >= 16,
      'Reference API CSRF cookie is required')
    extraHTTPHeaders[credential.csrf.headerName] = cookie.value as string
  }
  return playwrightRequest.newContext({
    baseURL: subject.reference.apiOrigin,
    extraHTTPHeaders,
    storageState: browserStorageState(credential),
  })
}

export async function waitForValue<T>(
  read: () => Promise<T>,
  accept: (value: T) => boolean,
  label: string,
  timeoutMilliseconds = 90_000,
) {
  const deadline = Date.now() + timeoutMilliseconds
  let last: T | undefined
  while (Date.now() < deadline) {
    last = await read()
    if (accept(last)) return last
    await new Promise((resolve) => setTimeout(resolve, 400))
  }
  throw new Error(`${label} did not reach its required state; last=${JSON.stringify(last)}`)
}

export async function bootstrapGoldenSandbox(caseId: string): Promise<GoldenSandbox> {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const principal = goldenPrincipal('platform-user-a')
  const client = platformClient(environment.credentials.platform.apiA)
  const bootstrapped = await client.repository.bootstrapCandidate(
    principal.projectId,
    subject.sharedArtifacts.buildManifest.id,
    { idempotencyKey: qualificationKey(caseId, 'candidate-bootstrap') },
  )
  assert.equal(bootstrapped.data.candidate.projectId, principal.projectId)
  assert.deepEqual(bootstrapped.data.candidate.buildManifest, subject.sharedArtifacts.buildManifest)
  assert.deepEqual(bootstrapped.data.candidate.buildContract, subject.sharedArtifacts.buildContract)
  assert.equal(
    bootstrapped.data.candidate.baseWorkspaceRevision?.revisionId,
    subject.sharedArtifacts.workspaceRevision.id,
  )
  assert.equal(
    bootstrapped.data.candidate.baseWorkspaceRevision?.contentHash,
    subject.sharedArtifacts.workspaceRevision.contentHash,
  )
  const snapshot = bootstrapped.data.repositorySnapshotReceipt.snapshot
  assert.equal(snapshot.projectId, principal.projectId)
  assert.deepEqual(snapshot.buildManifest, subject.sharedArtifacts.buildManifest)
  assert.deepEqual(snapshot.buildContract, subject.sharedArtifacts.buildContract)
  assert.equal(snapshot.baseWorkspaceRevision?.revisionId, subject.sharedArtifacts.workspaceRevision.id)
  assert.equal(snapshot.baseWorkspaceRevision?.contentHash, subject.sharedArtifacts.workspaceRevision.contentHash)
  assert.equal(snapshot.tree.treeHash, bootstrapped.data.candidate.treeHash)
  const templateRelease = snapshot.templateReleases.find((entry) => (
    entry.release.id === subject.sharedArtifacts.templateRelease.id
  ))
  assert.ok(templateRelease, 'RepositorySnapshot must bind the approved TemplateRelease')
  assert.equal(templateRelease.release.contentHash, subject.sharedArtifacts.templateRelease.contentHash)
  assert.equal(
    templateRelease.authorityReceipt.contentHash,
    subject.sharedArtifacts.templateRelease.approvalReceiptDigest,
  )
  const opened = await client.sandbox.createSession(
    principal.projectId,
    bootstrapped.data.candidate.id,
    { idempotencyKey: qualificationKey(caseId, 'sandbox-open') },
  )
  assert.equal(opened.data.projectId, principal.projectId)
  assert.equal(opened.data.candidate.id, bootstrapped.data.candidate.id)
  assert.deepEqual(opened.data.buildManifest, subject.sharedArtifacts.buildManifest)
  assert.deepEqual(opened.data.buildContract, subject.sharedArtifacts.buildContract)
  assert.equal(opened.data.baseWorkspaceRevision?.revisionId, subject.sharedArtifacts.workspaceRevision.id)
  assert.equal(opened.data.baseWorkspaceRevision?.contentHash, subject.sharedArtifacts.workspaceRevision.contentHash)
  assert.equal(opened.data.runnerImageDigest, subject.sandbox.runner.imageDigest)
  assert.ok(opened.data.templateReleases.some((entry) => (
    entry.id === subject.sharedArtifacts.templateRelease.id
    && entry.contentHash === subject.sharedArtifacts.templateRelease.contentHash
  )))
  const expectedServiceIds = [...new Set(subject.sandbox.serviceProfiles.map((entry) => entry.service))].sort()
  assert.deepEqual(opened.data.allowedServices.map((entry) => entry.id).sort(), expectedServiceIds)
  assert.deepEqual(
    opened.data.allowedPorts.map((entry) => ({
      name: entry.name,
      protocol: entry.protocol,
      service: entry.serviceId,
    })).sort((left, right) => left.name.localeCompare(right.name)),
    subject.sandbox.serviceProfiles.map((entry) => ({
      name: entry.id,
      protocol: entry.protocol,
      service: entry.service,
    })),
  )
  const ready = opened.data.state === 'ready'
    ? opened
    : await waitForValue(
        () => client.sandbox.getSession(opened.data.id),
        (result) => result.data.state === 'ready',
        `${caseId} SandboxSession`,
      )
  const treeResult = await client.sandbox.getTree(ready.data.id)
  assert.equal(treeResult.data.session.id, ready.data.id)
  assert.equal(treeResult.data.candidate.projectId, principal.projectId)
  return {
    client,
    projectId: principal.projectId,
    candidate: treeResult.data.candidate,
    fences: sandboxFences(treeResult.headers, treeResult.data.session),
    session: treeResult.data.session,
    tree: treeResult.data,
  }
}

export async function resolveGoldenRelease(caseId: string): Promise<GoldenRelease> {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const owner = goldenPrincipal('platform-owner')
  const client = platformClient(environment.credentials.platform.owner)
  const revision = await client.artifacts.getRevision(subject.sharedArtifacts.workspaceRevision.id)
  assert.equal(revision.data.id, subject.sharedArtifacts.workspaceRevision.id)
  assert.equal(revision.data.contentHash, subject.sharedArtifacts.workspaceRevision.contentHash)
  const artifact = await client.artifacts.get(revision.data.artifactId)
  assert.equal(artifact.data.artifact.projectId, owner.projectId)
  const workspace = {
    workspaceArtifactId: revision.data.artifactId,
    workspaceContentHash: revision.data.contentHash,
    workspaceRevisionId: revision.data.id,
  }
  const canonicalRuns = await client.verification.listCanonicalRuns(owner.projectId, workspace, 50)
  const matchingRuns = canonicalRuns.data.runs.filter((entry) => (
    entry.subject.workspaceArtifactId === workspace.workspaceArtifactId
    && entry.subject.workspaceRevisionId === workspace.workspaceRevisionId
    && entry.subject.workspaceContentHash === workspace.workspaceContentHash
    && entry.receipt?.contentHash === subject.sharedArtifacts.workspaceRevision.canonicalQualityReceiptDigest
  ))
  assert.equal(matchingRuns.length, 1, 'exact approved WorkspaceRevision must expose one canonical Receipt')
  const canonicalRun = matchingRuns[0]!
  assert.equal(canonicalRun.run.state, 'passed')
  assert.ok(canonicalRun.receipt)
  assert.deepEqual(canonicalRun.buildManifest, subject.sharedArtifacts.buildManifest)
  assert.deepEqual(canonicalRun.buildContract, subject.sharedArtifacts.buildContract)
  const bundle = await client.release.createBundle(
    owner.projectId,
    canonicalRun.receipt,
    { idempotencyKey: qualificationKey(caseId, 'release-bundle') },
  )
  assert.equal(bundle.data.bundle.projectId, owner.projectId)
  assert.deepEqual(bundle.data.bundle.canonicalReceipt, canonicalRun.receipt)
  assert.deepEqual(bundle.data.bundle.buildManifest, subject.sharedArtifacts.buildManifest)
  assert.deepEqual(bundle.data.bundle.buildContract, subject.sharedArtifacts.buildContract)
  return {
    bundle: bundle.data.bundle,
    canonicalReceipt: canonicalRun.receipt,
    client,
    projectId: owner.projectId,
  }
}

export function faultAuthority(operationKind: GoldenFaultOperation) {
  const matches = goldenSubject().faultAuthorities.filter((entry) => entry.operationKind === operationKind)
  assert.equal(matches.length, 1, `fixture must bind exactly one ${operationKind} authority`)
  return matches[0]!
}

export async function consumeGoldenFault(
  request: APIRequestContext,
  operationKind: GoldenFaultOperation,
) {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const authority = faultAuthority(operationKind)
  const response = await request.post(
    `${subject.platform.apiOrigin}/v1/qualification/golden-fault-authorities/${authority.authorityId}/consume`,
    {
      headers: {
        ...environment.credentials.platform.faultOperator.headers,
        'Idempotency-Key': qualificationKey(`fault:${authority.authorityId}`, 'consume'),
      },
      data: {
        fixtureId: subject.fixtureId,
        runId: subject.runId,
        schemaVersion: 'worksflow-golden-fault-consume-request/v1',
      },
    },
  )
  assert.equal(
    response.status(),
    200,
    `real ${operationKind} adapter is required; response=${response.status()} ${await response.text()}`,
  )
  assert.match(response.headers()['content-type'] ?? '', /^application\/json(?:;|$)/i)
  const receipt = exactRecord(await response.json(), [
    'adapterInvocationId', 'adapterResultDigest', 'authorityId', 'completedAt',
    'envelopeDigest', 'expectedFenceDigest', 'fixtureId', 'observedFenceDigest',
    'observedHeadDigest', 'operationKind', 'outcome', 'payloadDigest', 'predicateDigest',
    'reservedAt', 'resolutionDigest', 'resolvedFenceDigest', 'resolvedHeadDigest',
    'resolvedResourceId', 'resourceSelector', 'resultId', 'runId', 'schemaVersion',
  ], `${operationKind} consume receipt`)
  assert.equal(receipt.schemaVersion, 'worksflow-golden-fault-consume-receipt/v1')
  assert.equal(exactUUID(receipt.authorityId, 'receipt.authorityId'), authority.authorityId)
  assert.equal(exactUUID(receipt.fixtureId, 'receipt.fixtureId'), subject.fixtureId)
  assert.equal(exactUUID(receipt.runId, 'receipt.runId'), subject.runId)
  exactUUID(receipt.adapterInvocationId, 'receipt.adapterInvocationId')
  exactUUID(receipt.resultId, 'receipt.resultId')
  assert.equal(receipt.operationKind, operationKind)
  assert.equal(receipt.resourceSelector, authority.resourceSelector)
  assert.equal(receipt.expectedFenceDigest, authority.expectedFenceDigest)
  assert.equal(receipt.envelopeDigest, authority.dsse.envelopeDigest)
  assert.equal(receipt.payloadDigest, authority.dsse.payloadDigest)
  for (const field of [
    'adapterResultDigest', 'observedFenceDigest', 'observedHeadDigest', 'predicateDigest',
    'resolutionDigest', 'resolvedFenceDigest', 'resolvedHeadDigest',
  ]) exactDigest(receipt[field], `receipt.${field}`)
  const reservedAt = exactTimestamp(receipt.reservedAt, 'receipt.reservedAt')
  const completedAt = exactTimestamp(receipt.completedAt, 'receipt.completedAt')
  assert.ok(Date.parse(completedAt) >= Date.parse(reservedAt))
  assert.equal(receipt.outcome, operationKind === 'agent-security-canary' ? 'refused' : 'applied')
  exactString(receipt.resolvedResourceId, 'receipt.resolvedResourceId')
  return receipt as unknown as GoldenFaultReceipt
}

/**
 * Golden v1 requires each closed adapter to use the exact business resource ID
 * as resolvedResourceId. The ID is therefore inside resolutionDigest and the
 * append-before-side-effect ledger commitment; an auxiliary read projection
 * is intentionally not accepted as a substitute for that commitment.
 */
export function assertGoldenFaultTarget(
  receipt: GoldenFaultReceipt,
  target: GoldenFaultTarget,
) {
  const expectedKind: Partial<Record<GoldenFaultOperation, GoldenFaultTarget['kind']>> = {
    'agent-runner-crash': 'agent-attempt',
    'agent-runner-timeout': 'agent-attempt',
    'controller-conflict': 'release-delivery-operation',
    'controller-maintenance': 'release-delivery-operation',
    'controller-not-found': 'release-delivery-operation',
    'controller-timeout': 'release-delivery-operation',
    'reference-gateway-outage': 'reference-run',
    'reference-process-restart': 'reference-application',
    'sandbox-dependency-crash': 'sandbox-session',
  }
  assert.equal(expectedKind[receipt.operationKind], target.kind)
  assert.equal(receipt.resolvedResourceId, target.id)
  assert.ok(
    goldenSubject().principals.some((principal) => principal.projectId === target.projectId),
    'fault target project must be root-bound by the Golden fixture',
  )
}

export function assertPlatformError(error: unknown, status: number, code?: string) {
  assert.ok(error instanceof PlatformHttpError, `expected PlatformHttpError, got ${String(error)}`)
  assert.equal(error.status, status)
  if (code) assert.equal(error.code, code)
}

export async function expectPlatformFailure(
  action: () => Promise<unknown>,
  status: number,
  code?: string,
) {
  let failure: unknown
  try {
    await action()
  } catch (error) {
    failure = error
  }
  assertPlatformError(failure, status, code)
}

export function assertExactReference(actual: ExactIdentity, expected: ExactIdentity, label: string) {
  assert.deepEqual(actual, expected, `${label} exact identity drift`)
}

export function assertDigest(value: string, label: string) {
  exactDigest(value, label)
}

export function assertUUID(value: string, label: string) {
  exactUUID(value, label)
}

export function assertTimestamp(value: string, label: string) {
  exactTimestamp(value, label)
}

export function assertExactObject(value: unknown, keys: readonly string[], label: string) {
  return exactRecord(value, keys, label)
}
