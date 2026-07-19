import { createPublicKey, verify as verifySignature } from 'node:crypto'

import { verifyResponseBodySha256 } from '@/lib/platform/http'
import {
  isReleaseDeliveryRunTerminal,
  type ReleaseDeliveryOperationKind,
  type ReleasePreviewReceiptDto,
  type ReleasePreviewRunDto,
  type ReleaseProductionReceiptDto,
  type ReleaseProductionRunDto,
} from '@/lib/platform/release-contract'

import {
  expect,
  goldenQualificationEnvironment,
  test,
  type APIRequestContext,
} from './qualification-runtime'
import {
  assertDigest,
  assertExactObject,
  assertGoldenFaultTarget,
  assertTimestamp,
  assertUUID,
  bootstrapGoldenSandbox,
  consumeGoldenFault,
  expectPlatformFailure,
  goldenPrincipal,
  goldenSubject,
  platformClient,
  qualificationKey,
  resolveGoldenRelease,
  waitForValue,
  type GoldenRelease,
} from './golden-qualification-support'
import {
  decodeCanonicalBase64,
  dssePAE,
  parseCanonicalJSON,
} from '../scripts/qualification-core.mjs'

type ExactIdentity = Readonly<{ id: string; contentHash: string }>

const terminalVerificationStates = new Set([
  'cancelled', 'error', 'failed', 'passed', 'timed_out',
])

function requiredString(value: unknown, label: string) {
  if (typeof value !== 'string' || value.length === 0) {
    throw new Error(`${label} must be a non-empty string`)
  }
  return value
}

function requiredInteger(value: unknown, label: string) {
  if (typeof value !== 'number' || !Number.isSafeInteger(value)) {
    throw new Error(`${label} must be a safe integer`)
  }
  return value
}

function requiredBoolean(value: unknown, label: string) {
  if (typeof value !== 'boolean') throw new Error(`${label} must be a boolean`)
  return value
}

function requiredArray(value: unknown, label: string) {
  if (!Array.isArray(value)) throw new Error(`${label} must be an array`)
  return value
}

function exactIdentity(value: unknown, label: string): ExactIdentity {
  const identity = assertExactObject(value, ['contentHash', 'id'], label)
  const id = requiredString(identity.id, `${label}.id`)
  const contentHash = requiredString(identity.contentHash, `${label}.contentHash`)
  assertUUID(id, `${label}.id`)
  assertDigest(contentHash, `${label}.contentHash`)
  return { id, contentHash }
}

async function immutableQualificationJSON(
  request: APIRequestContext,
  path: string,
  identity: ExactIdentity,
  label: string,
  contentType = 'application/json',
) {
  const subject = goldenSubject()
  const environment = goldenQualificationEnvironment()
  const response = await request.get(
    `${subject.platform.apiOrigin}${path}/${encodeURIComponent(identity.id)}`,
    {
      headers: environment.credentials.platform.admin.headers,
      params: { contentHash: identity.contentHash },
    },
  )
  expect(
    response.status(),
    `${label} immutable bytes are required; response=${response.status()} ${await response.text()}`,
  ).toBe(200)
  expect(response.headers()['content-type']).toBe(contentType)
  expect(response.headers()['x-content-hash']).toBe(identity.contentHash)
  expect(response.headers().etag).toBe(`"${identity.contentHash}"`)
  expect(response.headers()['cache-control']).toBe('public, max-age=31536000, immutable')
  const bytes = await response.body()
  await verifyResponseBodySha256(
    Uint8Array.from(bytes).buffer,
    identity.contentHash,
    `${label} actual bytes do not match the immutable reference`,
  )
  // The shared qualification wire contract deliberately permits either exact
  // canonical JSON or that same byte sequence followed by one LF. The LF is
  // still covered by X-Content-Hash, ETag, and the immutable reference.
  return parseCanonicalJSON(bytes, label)
}

async function controllerTrustDocument(request: APIRequestContext, completedAt: string) {
  const subject = goldenSubject()
  const environment = goldenQualificationEnvironment()
  const digest = subject.release.controller.trustKeyDigest
  const response = await request.get(
    `${subject.platform.apiOrigin}/v1/qualification/release-controller-trust/${encodeURIComponent(digest)}`,
    { headers: environment.credentials.platform.admin.headers },
  )
  expect(
    response.status(),
    `root-bound Release Controller trust is required; response=${response.status()} ${await response.text()}`,
  ).toBe(200)
  expect(response.headers()['content-type']).toBe('application/json')
  expect(response.headers()['x-content-hash']).toBe(digest)
  expect(response.headers().etag).toBe(`"${digest}"`)
  expect(response.headers()['cache-control']).toBe('public, max-age=31536000, immutable')
  const bytes = await response.body()
  await verifyResponseBodySha256(
    Uint8Array.from(bytes).buffer,
    digest,
    'Release Controller trust bytes do not match the Fixture trustKeyDigest',
  )
  const trust = assertExactObject(parseCanonicalJSON(bytes, 'Release Controller trust document'), [
    'algorithm', 'identity', 'keyId', 'notAfter', 'notBefore', 'publicKeyPem',
    'revokedAt', 'role', 'schemaVersion',
  ], 'Release Controller trust document')
  expect(trust.schemaVersion).toBe('release-controller-trust/v1')
  expect(trust.identity).toBe(subject.release.controller.identity)
  expect(trust.role).toBe('release-controller-qualification-attestor')
  expect(trust.revokedAt).toBeNull()
  const keyId = requiredString(trust.keyId, 'controller trust keyId')
  const algorithm = requiredString(trust.algorithm, 'controller trust algorithm')
  expect(['ecdsa-p256-sha256', 'ed25519']).toContain(algorithm)
  const notBefore = requiredString(trust.notBefore, 'controller trust notBefore')
  const notAfter = requiredString(trust.notAfter, 'controller trust notAfter')
  assertTimestamp(notBefore, 'controller trust notBefore')
  assertTimestamp(notAfter, 'controller trust notAfter')
  expect(Date.parse(completedAt)).toBeGreaterThanOrEqual(Date.parse(notBefore))
  expect(Date.parse(completedAt)).toBeLessThanOrEqual(Date.parse(notAfter))
  const publicKey = createPublicKey(requiredString(trust.publicKeyPem, 'controller trust publicKeyPem'))
  if (algorithm === 'ed25519') {
    expect(publicKey.asymmetricKeyType).toBe('ed25519')
  } else {
    expect(publicKey.asymmetricKeyType).toBe('ec')
    expect(publicKey.asymmetricKeyDetails?.namedCurve).toBe('prime256v1')
  }
  return { algorithm, keyId, publicKey }
}

async function runServerQualification(
  request: APIRequestContext,
  release: GoldenRelease,
  caseId: string,
  vector: string,
  subjects: Readonly<Record<string, unknown>>,
) {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const response = await request.post(
    `${subject.platform.apiOrigin}/v1/qualification/release-delivery-operations`,
    {
      headers: {
        ...environment.credentials.platform.admin.headers,
        'Idempotency-Key': qualificationKey(caseId, `server-${vector}`),
      },
      data: {
        schemaVersion: 'worksflow-release-delivery-qualification-request/v1',
        root: {
          authorityHash: environment.authorityHash,
          fixtureHash: environment.fixtureHash,
          fixtureId: subject.fixtureId,
          planDigest: subject.planDigest,
          qualificationRunId: subject.runId,
        },
        caseId,
        vector,
        projectId: release.projectId,
        releaseBundle: { id: release.bundle.id, contentHash: release.bundle.bundleHash },
        subjects,
      },
    },
  )
  expect(
    response.status(),
    `real server qualification vector ${vector} is required; response=${response.status()} ${await response.text()}`,
  ).toBe(201)
  const accepted = assertExactObject(await response.json(), [
    'attestation', 'operation', 'receipt', 'replayed',
  ], `${vector} qualification acceptance`)
  expect(requiredBoolean(accepted.replayed, `${vector}.replayed`)).toBe(false)
  const operationReference = exactIdentity(accepted.operation, `${vector}.operation`)
  const receiptReference = exactIdentity(accepted.receipt, `${vector}.receipt`)
  const attestationReference = exactIdentity(accepted.attestation, `${vector}.attestation`)

  const operation = assertExactObject(
    await immutableQualificationJSON(
      request,
      '/v1/qualification/release-delivery-operations',
      operationReference,
      `${vector} qualification operation`,
    ),
    [
      'caseId', 'createdAt', 'id', 'projectId', 'releaseBundle', 'requestHash',
      'root', 'schemaVersion', 'subjects', 'vector',
    ],
    `${vector} qualification operation`,
  )
  expect(operation.schemaVersion).toBe('worksflow-release-delivery-qualification-operation/v1')
  expect(operation.id).toBe(operationReference.id)
  expect(operation.caseId).toBe(caseId)
  expect(operation.vector).toBe(vector)
  expect(operation.projectId).toBe(release.projectId)
  expect(operation.releaseBundle).toEqual({ id: release.bundle.id, contentHash: release.bundle.bundleHash })
  expect(operation.subjects).toEqual(subjects)
  assertDigest(requiredString(operation.requestHash, `${vector}.requestHash`), `${vector}.requestHash`)
  assertTimestamp(requiredString(operation.createdAt, `${vector}.createdAt`), `${vector}.createdAt`)
  expect(operation.root).toEqual({
    authorityHash: environment.authorityHash,
    fixtureHash: environment.fixtureHash,
    fixtureId: subject.fixtureId,
    planDigest: subject.planDigest,
    qualificationRunId: subject.runId,
  })

  const receipt = assertExactObject(
    await immutableQualificationJSON(
      request,
      '/v1/qualification/release-delivery-receipts',
      receiptReference,
      `${vector} qualification receipt`,
    ),
    [
      'caseId', 'completedAt', 'evidence', 'id', 'operation', 'platformDeployment',
      'projectId', 'releaseBundle', 'root', 'schemaVersion', 'serverBuild', 'vector',
    ],
    `${vector} qualification receipt`,
  )
  expect(receipt.schemaVersion).toBe('worksflow-release-delivery-qualification-receipt/v1')
  expect(receipt.id).toBe(receiptReference.id)
  expect(receipt.operation).toEqual(operationReference)
  expect(receipt.root).toEqual(operation.root)
  expect(receipt.caseId).toBe(caseId)
  expect(receipt.vector).toBe(vector)
  expect(receipt.projectId).toBe(release.projectId)
  expect(receipt.releaseBundle).toEqual(operation.releaseBundle)
  expect(receipt.platformDeployment).toEqual(subject.platform.deploymentReceipt)
  expect(receipt.serverBuild).toEqual(subject.platform.serverBuild)
  const completedAt = requiredString(receipt.completedAt, `${vector}.completedAt`)
  assertTimestamp(completedAt, `${vector}.completedAt`)

  const envelope = assertExactObject(
    await immutableQualificationJSON(
      request,
      '/v1/qualification/release-delivery-attestations',
      attestationReference,
      `${vector} qualification attestation`,
      'application/vnd.dsse.envelope.v1+json',
    ),
    ['payload', 'payloadType', 'signatures'],
    `${vector} DSSE envelope`,
  )
  expect(envelope.payloadType).toBe('application/vnd.in-toto+json')
  const payload = requiredString(envelope.payload, `${vector}.payload`)
  const payloadBytes = decodeCanonicalBase64(payload, `${vector}.payload`)
  const signatures = requiredArray(envelope.signatures, `${vector}.signatures`)
  expect(signatures).toHaveLength(1)
  const signature = assertExactObject(signatures[0], ['keyid', 'sig'], `${vector}.signature`)
  const signatureKeyId = requiredString(signature.keyid, `${vector}.signature.keyid`)
  const signatureBytes = decodeCanonicalBase64(
    requiredString(signature.sig, `${vector}.signature.sig`),
    `${vector}.signature.sig`,
    4096,
  )
  const statement = parseCanonicalJSON(payloadBytes, `${vector} attestation payload`)
  const attestation = assertExactObject(
    statement,
    ['_type', 'predicate', 'predicateType', 'subject'],
    `${vector} in-toto statement`,
  )
  expect(attestation._type).toBe('https://in-toto.io/Statement/v1')
  expect(attestation.predicateType).toBe('https://worksflow.dev/qualification/release-delivery/v1')
  expect(attestation.predicate).toEqual({ operation: operationReference, root: operation.root })
  expect(attestation.subject).toEqual([{
    digest: { sha256: receiptReference.contentHash.slice('sha256:'.length) },
    name: `release-delivery-receipt/${receiptReference.id}`,
  }])
  const trust = await controllerTrustDocument(request, completedAt)
  expect(signatureKeyId).toBe(trust.keyId)
  expect(verifySignature(
    trust.algorithm === 'ed25519' ? null : 'sha256',
    dssePAE(requiredString(envelope.payloadType, `${vector}.payloadType`), payloadBytes),
    trust.publicKey,
    signatureBytes,
  ), `${vector} DSSE signature must verify under the Fixture-bound Controller key`).toBe(true)
  if (receipt.evidence === null || typeof receipt.evidence !== 'object' || Array.isArray(receipt.evidence)) {
    throw new Error(`${vector}.evidence must be an object`)
  }
  return { evidence: receipt.evidence, operationReference, receiptReference }
}

async function waitPreview(release: GoldenRelease, runId: string) {
  return waitForValue(
    async () => (await release.client.release.getPreviewRun(release.projectId, runId)).data,
    (run) => isReleaseDeliveryRunTerminal(run.state),
    `Preview Run ${runId}`,
    180_000,
  )
}

async function waitProduction(release: GoldenRelease, runId: string) {
  return waitForValue(
    async () => (await release.client.release.getProductionRun(release.projectId, runId)).data,
    (run) => isReleaseDeliveryRunTerminal(run.state),
    `Production Run ${runId}`,
    180_000,
  )
}

async function readPreviewReceipt(release: GoldenRelease, run: ReleasePreviewRunDto) {
  expect(run.receipt, `Preview Run ${run.id} must close an immutable Receipt`).toBeTruthy()
  return release.client.release.getPreviewReceipt(release.projectId, run.receipt!)
    .then((result) => result.data)
}

async function readProductionReceipt(release: GoldenRelease, run: ReleaseProductionRunDto) {
  expect(run.receipt, `Production Run ${run.id} must close an immutable Receipt`).toBeTruthy()
  return release.client.release.getProductionReceipt(release.projectId, run.receipt!)
    .then((result) => result.data)
}

function assertControllerResult(
  run: ReleasePreviewRunDto | ReleaseProductionRunDto,
  receipt: ReleasePreviewReceiptDto | ReleaseProductionReceiptDto,
) {
  expect(receipt.schemaVersion).toMatch(/^release-(?:preview|production)-receipt\/v2$/u)
  expect(receipt.runId).toBe(run.id)
  expect(receipt.releaseBundle).toEqual(run.releaseBundle)
  expect(receipt.controllerOperation).toBeTruthy()
  expect(receipt.controllerOperation!.operationId).toBe(run.id)
  assertDigest(receipt.controllerOperation!.resultHash, `${run.id} controller Result hash`)
  return receipt.controllerOperation!
}

async function passingPreview(caseId: string) {
  const release = await resolveGoldenRelease(caseId)
  const started = await release.client.release.startPreview(
    release.projectId,
    { id: release.bundle.id, contentHash: release.bundle.bundleHash },
    `${caseId} exact Golden Preview`,
    { idempotencyKey: qualificationKey(caseId, 'preview') },
  )
  const run = await waitPreview(release, started.data.run.id)
  expect(run.state).toBe('passed')
  const receipt = await readPreviewReceipt(release, run)
  const controllerResult = assertControllerResult(run, receipt)
  expect(receipt.decision).toBe('passed')
  expect(receipt.canonicalReceipt).toEqual(release.canonicalReceipt)
  expect(receipt.releaseBundle).toEqual({ id: release.bundle.id, contentHash: release.bundle.bundleHash })
  return { controllerResult, receipt, release, run }
}

async function observeBusinessOperation(
  request: APIRequestContext,
  release: GoldenRelease,
  caseId: string,
  runKind: ReleaseDeliveryOperationKind,
  businessRunId: string,
) {
  const qualified = await runServerQualification(
    request,
    release,
    caseId,
    'observe-business-operation',
    { businessRunId, runKind },
  )
  const evidence = assertExactObject(qualified.evidence, [
    'attempts', 'businessRun', 'expectedHead', 'operation', 'result', 'schemaVersion',
  ], `${caseId} business operation evidence`)
  expect(evidence.schemaVersion).toBe('release-delivery-business-operation-evidence/v1')
  const businessRun = assertExactObject(evidence.businessRun, [
    'id', 'kind', 'releaseBundle', 'state', 'version',
  ], `${caseId} business Run`)
  expect(businessRun.id).toBe(businessRunId)
  expect(businessRun.kind).toBe(runKind)
  expect(businessRun.releaseBundle).toEqual({ id: release.bundle.id, contentHash: release.bundle.bundleHash })
  expect(requiredInteger(businessRun.version, `${caseId}.businessRun.version`)).toBeGreaterThan(0)
  const operation = assertExactObject(evidence.operation, [
    'controller', 'id', 'kind', 'requestHash', 'schemaVersion',
  ], `${caseId} business Operation`)
  expect(operation.schemaVersion).toBe('release-delivery-operation/v1')
  expect(operation.id).toBe(businessRunId)
  expect(operation.kind).toBe(runKind)
  assertDigest(requiredString(operation.requestHash, `${caseId}.operation.requestHash`), `${caseId}.operation.requestHash`)
  expect(operation.controller).toEqual({
    schemaVersion: 'release-delivery-controller-identity/v1',
    id: goldenSubject().release.controller.identity,
    version: requiredString(
      assertExactObject(operation.controller, [
        'id', 'protocol', 'schemaVersion', 'trustKeyDigest', 'version',
      ], `${caseId} controller`).version,
      `${caseId}.controller.version`,
    ),
    protocol: goldenSubject().release.controller.protocol,
    trustKeyDigest: goldenSubject().release.controller.trustKeyDigest,
  })
  const result = assertExactObject(evidence.result, [
    'id', 'requestHash', 'resultHash', 'status',
  ], `${caseId} immutable Result`)
  expect(result.id).toBe(businessRunId)
  expect(result.requestHash).toBe(operation.requestHash)
  assertDigest(requiredString(result.resultHash, `${caseId}.result.resultHash`), `${caseId}.result.resultHash`)
  expect(['completed', 'rejected']).toContain(result.status)
  return {
    attempts: requiredArray(evidence.attempts, `${caseId}.attempts`),
    businessRun,
    expectedHead: evidence.expectedHead,
    operation,
    result,
  }
}

async function assertBundleClosure(release: GoldenRelease) {
  const subject = goldenSubject()
  expect(release.bundle.schemaVersion).toBe('release-bundle/v1')
  expect(release.bundle.canonicalReceipt).toEqual(release.canonicalReceipt)
  expect(release.bundle.buildManifest).toEqual(subject.sharedArtifacts.buildManifest)
  expect(release.bundle.buildContract).toEqual(subject.sharedArtifacts.buildContract)
  for (const runtime of subject.sharedArtifacts.runtimeImages) {
    for (const authority of [runtime.provenance, runtime.sbom, runtime.signature]) {
      expect(release.bundle.releaseArtifacts.filter((artifact) => (
        artifact.id === authority.id && artifact.contentHash === authority.contentHash
      )), `${runtime.role} must close ${authority.id}`).toHaveLength(1)
    }
    expect(release.bundle.releaseArtifacts.some((artifact) => artifact.contentHash === runtime.imageDigest),
      `${runtime.role} image digest must be in the Canonical artifact set`).toBe(true)
  }
  const stack = await release.client.constructorApi.getFullStackTemplate(
    release.bundle.fullStackTemplate.id,
    { contentHash: release.bundle.fullStackTemplate.contentHash },
  )
  expect(stack.data.template.components).toEqual(stack.data.components)
  expect(stack.data.components.length).toBeGreaterThan(0)
  let migrations = 0
  for (const component of stack.data.components) {
    const registration = await release.client.constructorApi.getTemplateRelease(
      component.release.id,
      {
        contentHash: component.release.contentHash,
        subjectHash: component.release.subjectHash,
      },
    )
    expect(registration.data.policy.state).toBe('approved')
    expect(registration.data.release.manifest.services.length).toBeGreaterThan(0)
    expect(Object.keys(registration.data.release.manifest.commands).length).toBeGreaterThan(0)
    assertDigest(registration.data.release.sbomDigest, `${component.role} SBOM digest`)
    assertDigest(registration.data.release.signature.bundleDigest, `${component.role} signature digest`)
    expect(registration.data.release.signature.subjectHash).toBe(registration.data.release.subjectHash)
    if (registration.data.release.manifest.migration) {
      migrations += 1
      const migration = registration.data.release.manifest.migration
      expect(registration.data.release.manifest.services.some((service) => service.id === migration.serviceId)).toBe(true)
      expect(registration.data.release.manifest.commands[migration.commandName]).toBeTruthy()
    }
  }
  expect(migrations, 'the exact full-stack Bundle must declare its database migration command').toBeGreaterThan(0)
}

test.describe('Golden Release external qualification', () => {
  test('QG-RELEASE-001 proves canonical handoff and preview happy, migration-fail, health-fail, and single-flight outcomes', async ({ request }) => {
    const release = await resolveGoldenRelease('QG-RELEASE-001')
    await assertBundleClosure(release)

    const sandbox = await bootstrapGoldenSandbox('QG-RELEASE-001-CANDIDATE')
    const checkpointId = crypto.randomUUID()
    const checkpoint = await sandbox.client.sandbox.checkpoint(
      sandbox.session.id,
      { checkpointId, reason: 'QG-RELEASE-001 Candidate-only verification authority' },
      { fences: sandbox.fences, idempotencyKey: qualificationKey('QG-RELEASE-001', 'candidate-checkpoint') },
    )
    const profiles = await sandbox.client.verification.listProfiles(sandbox.session.id)
    const matchingProfiles = profiles.data.profiles.filter((entry) => (
      entry.verificationProfile.id === release.bundle.verificationProfile.id
      && entry.verificationProfile.version === release.bundle.verificationProfile.version
      && entry.verificationProfile.contentHash === release.bundle.verificationProfile.contentHash
    ))
    expect(matchingProfiles).toHaveLength(1)
    const candidateStarted = await sandbox.client.verification.createRun(
      sandbox.session.id,
      {
        candidateId: checkpoint.data.session.candidate.id,
        checkpointId: checkpoint.data.checkpoint.id,
        expectedSessionVersion: checkpoint.data.session.version,
        expectedSessionEpoch: checkpoint.data.session.sessionEpoch,
        expectedCandidateVersion: checkpoint.data.session.candidate.version,
        expectedWriterLeaseEpoch: checkpoint.data.session.candidate.writerLeaseEpoch,
        verificationProfile: matchingProfiles[0]!.verificationProfile,
        reason: 'QG-RELEASE-001 produce exact Candidate Receipt that Release must reject',
      },
      { idempotencyKey: qualificationKey('QG-RELEASE-001', 'candidate-verification') },
    )
    const candidateTerminal = await waitForValue(
      () => sandbox.client.verification.getRun(candidateStarted.data.run.id),
      (view) => terminalVerificationStates.has(view.data.run.state),
      'QG-RELEASE-001 Candidate VerificationRun',
      180_000,
    )
    expect(candidateTerminal.data.run.state).toBe('passed')
    expect(candidateTerminal.data.receipt).toBeTruthy()
    expect(candidateTerminal.data.receiptDecision).toBe('passed')
    const candidateReceipt = await sandbox.client.verification.getReceipt(candidateTerminal.data.receipt!.id)
    expect(candidateReceipt.data.scope).toBe('candidate')
    expect(candidateReceipt.data.payloadHash).toBe(candidateTerminal.data.receipt!.contentHash)
    await expectPlatformFailure(
      () => release.client.release.createBundle(
        release.projectId,
        candidateTerminal.data.receipt!,
        { idempotencyKey: qualificationKey('QG-RELEASE-001', 'candidate-bundle-rejected') },
      ),
      404,
    )
    const exactBundle = await release.client.release.getBundleByReceipt(release.projectId, release.canonicalReceipt)
    expect(exactBundle.data.id).toBe(release.bundle.id)
    expect(exactBundle.data.bundleHash).toBe(release.bundle.bundleHash)

    const happyStarted = await release.client.release.startPreview(
      release.projectId,
      { id: release.bundle.id, contentHash: release.bundle.bundleHash },
      'QG-RELEASE-001 real happy Preview',
      { idempotencyKey: qualificationKey('QG-RELEASE-001', 'happy-preview') },
    )
    const happy = await waitPreview(release, happyStarted.data.run.id)
    expect(happy.state).toBe('passed')
    const happyReceipt = await readPreviewReceipt(release, happy)
    assertControllerResult(happy, happyReceipt)
    expect(happyReceipt.checks.some((check) => check.kind === 'migration' && check.status === 'passed')).toBe(true)
    expect(happyReceipt.checks.some((check) => check.kind === 'health' && check.status === 'passed')).toBe(true)

    for (const vector of ['preview-migration-failure', 'preview-health-failure']) {
      const qualified = await runServerQualification(request, release, 'QG-RELEASE-001', vector, {})
      const evidence = assertExactObject(qualified.evidence, [
        'businessRunId', 'checkKind', 'productionMutationCount', 'schemaVersion',
      ], `${vector} evidence`)
      expect(evidence.schemaVersion).toBe('release-preview-failure-evidence/v1')
      const businessRunId = requiredString(evidence.businessRunId, `${vector}.businessRunId`)
      assertUUID(businessRunId, `${vector}.businessRunId`)
      expect(evidence.checkKind).toBe(vector === 'preview-migration-failure' ? 'migration' : 'health')
      expect(evidence.productionMutationCount).toBe(0)
      const failed = await waitPreview(release, businessRunId)
      expect(failed.state).toBe('failed')
      const failedReceipt = await readPreviewReceipt(release, failed)
      assertControllerResult(failed, failedReceipt)
      expect(failedReceipt.decision).toBe('failed')
      expect(failedReceipt.checks.filter((check) => (
        check.kind === evidence.checkKind && check.status === 'failed'
      ))).toHaveLength(1)
    }

    const beforeConcurrent = await release.client.release.listPreviewRuns(
      release.projectId,
      { id: release.bundle.id, contentHash: release.bundle.bundleHash },
    )
    const concurrent = await Promise.allSettled([
      release.client.release.startPreview(
        release.projectId,
        { id: release.bundle.id, contentHash: release.bundle.bundleHash },
        'QG-RELEASE-001 distinct single-flight request A',
        { idempotencyKey: qualificationKey('QG-RELEASE-001', 'single-flight-a') },
      ),
      release.client.release.startPreview(
        release.projectId,
        { id: release.bundle.id, contentHash: release.bundle.bundleHash },
        'QG-RELEASE-001 distinct single-flight request B',
        { idempotencyKey: qualificationKey('QG-RELEASE-001', 'single-flight-b') },
      ),
    ])
    const winners = concurrent.filter((entry) => entry.status === 'fulfilled')
    const losers = concurrent.filter((entry) => entry.status === 'rejected')
    expect(winners).toHaveLength(1)
    expect(losers).toHaveLength(1)
    if (losers[0]!.status === 'rejected') {
      await expectPlatformFailure(() => Promise.reject(losers[0]!.reason), 409, 'release_preview_run_conflict')
    }
    if (winners[0]!.status !== 'fulfilled') throw new Error('single-flight winner is required')
    const winner = await waitPreview(release, winners[0]!.value.data.run.id)
    expect(winner.state).toBe('passed')
    const winnerReceipt = await readPreviewReceipt(release, winner)
    const winnerResult = assertControllerResult(winner, winnerReceipt)
    const afterConcurrent = await release.client.release.listPreviewRuns(
      release.projectId,
      { id: release.bundle.id, contentHash: release.bundle.bundleHash },
    )
    expect(afterConcurrent.data).toHaveLength(beforeConcurrent.data.length + 1)
    expect(afterConcurrent.data.filter((entry) => entry.id === winner.id)).toHaveLength(1)
    const winnerEvidence = await observeBusinessOperation(
      request, release, 'QG-RELEASE-001-WINNER', 'preview', winner.id,
    )
    expect(winnerEvidence.result.resultHash).toBe(winnerResult.resultHash)

    const unresolved = await runServerQualification(
      request,
      release,
      'QG-RELEASE-001',
      'preview-single-flight-unresolved-matrix',
      {},
    )
    const matrix = assertExactObject(unresolved.evidence, [
      'blocked', 'schemaVersion', 'terminalSuccessor', 'unknown',
    ], 'single-flight unresolved matrix')
    expect(matrix.schemaVersion).toBe('release-preview-single-flight-evidence/v1')
    for (const state of ['unknown', 'blocked'] as const) {
      const branch = assertExactObject(matrix[state], [
        'activeOperationCount', 'businessRunId', 'conflictingCreateCode', 'successorCount',
      ], `single-flight ${state}`)
      assertUUID(requiredString(branch.businessRunId, `${state}.businessRunId`), `${state}.businessRunId`)
      expect(branch.activeOperationCount).toBe(1)
      expect(branch.conflictingCreateCode).toBe('release_preview_run_conflict')
      expect(branch.successorCount).toBe(0)
    }
    const terminalSuccessor = assertExactObject(matrix.terminalSuccessor, [
      'operationCount', 'predecessorRunId', 'resultHash', 'successorRunId',
    ], 'terminal successor')
    expect(terminalSuccessor.predecessorRunId).not.toBe(terminalSuccessor.successorRunId)
    expect(terminalSuccessor.operationCount).toBe(1)
    assertDigest(requiredString(terminalSuccessor.resultHash, 'terminalSuccessor.resultHash'), 'terminalSuccessor.resultHash')
  })

  test('QG-RELEASE-002 proves same-digest production promotion and exact revision rollback', async ({ request }) => {
    const { receipt: previewReceipt, release } = await passingPreview('QG-RELEASE-002')
    const previewReference = { id: previewReceipt.id, contentHash: previewReceipt.payloadHash }
    const approval = await release.client.release.approvePromotion(
      release.projectId,
      previewReference,
      'QG-RELEASE-002 approve exact Preview Receipt',
      { idempotencyKey: qualificationKey('QG-RELEASE-002', 'approval') },
    )
    expect(approval.data.approval.previewReceipt).toEqual(previewReference)
    expect(approval.data.approval.releaseBundle).toEqual({
      id: release.bundle.id,
      contentHash: release.bundle.bundleHash,
    })
    const approvalReference = {
      id: approval.data.approval.id,
      contentHash: approval.data.approval.payloadHash,
    }

    const firstStarted = await release.client.release.startPromotion(
      release.projectId,
      approvalReference,
      'QG-RELEASE-002 establish exact old production revision',
      { idempotencyKey: qualificationKey('QG-RELEASE-002', 'promotion-old') },
    )
    const first = await waitProduction(release, firstStarted.data.run.id)
    expect(first.state).toBe('healthy')
    expect(first.revision).toBeTruthy()
    const firstReceipt = await readProductionReceipt(release, first)
    const firstControllerResult = assertControllerResult(first, firstReceipt)
    const firstRevision = await release.client.release.getDeploymentRevision(release.projectId, first.revision!)
    expect(firstRevision.data.runId).toBe(first.id)
    expect(firstRevision.data.productionReceipt).toEqual({ id: firstReceipt.id, contentHash: firstReceipt.payloadHash })
    expect(firstRevision.data.releaseBundle).toEqual({ id: release.bundle.id, contentHash: release.bundle.bundleHash })
    const firstEvidence = await observeBusinessOperation(
      request, release, 'QG-RELEASE-002-PROMOTION-OLD', 'production', first.id,
    )
    expect(firstEvidence.result.resultHash).toBe(firstControllerResult.resultHash)

    const secondStarted = await release.client.release.startPromotion(
      release.projectId,
      approvalReference,
      'QG-RELEASE-002 create a successor under expected-head CAS',
      { idempotencyKey: qualificationKey('QG-RELEASE-002', 'promotion-new') },
    )
    const second = await waitProduction(release, secondStarted.data.run.id)
    expect(second.state).toBe('healthy')
    expect(second.id).not.toBe(first.id)
    expect(second.revision).toBeTruthy()
    const secondReceipt = await readProductionReceipt(release, second)
    assertControllerResult(second, secondReceipt)
    const secondRevision = await release.client.release.getDeploymentRevision(release.projectId, second.revision!)
    expect(secondRevision.data.id).not.toBe(firstRevision.data.id)
    expect(secondRevision.data.releaseBundle).toEqual(firstRevision.data.releaseBundle)
    const secondEvidence = await observeBusinessOperation(
      request, release, 'QG-RELEASE-002-PROMOTION-NEW', 'production', second.id,
    )
    expect(secondEvidence.expectedHead).toEqual({
      productionReceipt: { id: firstReceipt.id, contentHash: firstReceipt.payloadHash },
      revision: { id: firstRevision.data.id, contentHash: firstRevision.data.payloadHash },
    })

    const rollbackStarted = await release.client.release.startRollback(
      release.projectId,
      { id: firstRevision.data.id, contentHash: firstRevision.data.payloadHash },
      'QG-RELEASE-002 restore exact old revision under current-head CAS',
      { idempotencyKey: qualificationKey('QG-RELEASE-002', 'rollback') },
    )
    const rollback = await waitProduction(release, rollbackStarted.data.run.id)
    expect(rollback.state).toBe('healthy')
    expect(rollback.operation).toBe('rollback')
    expect(rollback.sourceRevision).toEqual({ id: firstRevision.data.id, contentHash: firstRevision.data.payloadHash })
    expect(rollback.revision).toBeTruthy()
    const rollbackReceipt = await readProductionReceipt(release, rollback)
    const rollbackControllerResult = assertControllerResult(rollback, rollbackReceipt)
    const rollbackRevision = await release.client.release.getDeploymentRevision(release.projectId, rollback.revision!)
    expect(rollbackRevision.data.id).not.toBe(firstRevision.data.id)
    expect(rollbackRevision.data.id).not.toBe(secondRevision.data.id)
    expect(rollbackRevision.data.operation).toBe('rollback')
    expect(rollbackRevision.data.sourceRevision).toEqual({
      id: firstRevision.data.id,
      contentHash: firstRevision.data.payloadHash,
    })
    expect(rollbackRevision.data.releaseBundle).toEqual(firstRevision.data.releaseBundle)
    const rollbackEvidence = await observeBusinessOperation(
      request, release, 'QG-RELEASE-002-ROLLBACK', 'production', rollback.id,
    )
    expect(rollbackEvidence.expectedHead).toEqual({
      productionReceipt: { id: secondReceipt.id, contentHash: secondReceipt.payloadHash },
      revision: { id: secondRevision.data.id, contentHash: secondRevision.data.payloadHash },
    })
    expect(rollbackEvidence.result.resultHash).toBe(rollbackControllerResult.resultHash)

    const history = await release.client.release.listProductionHistory(release.projectId)
    expect(history.data.filter((entry) => [first.id, second.id, rollback.id].includes(entry.id))).toHaveLength(3)
    const immutableFirst = await release.client.release.getDeploymentRevision(
      release.projectId,
      { id: firstRevision.data.id, contentHash: firstRevision.data.payloadHash },
    )
    expect(immutableFirst.data).toEqual(firstRevision.data)

    const otherTenant = platformClient(goldenQualificationEnvironment().credentials.platform.apiB)
    await expectPlatformFailure(
      () => otherTenant.release.getDeploymentRevision(
        goldenPrincipal('platform-user-b').projectId,
        { id: rollbackRevision.data.id, contentHash: rollbackRevision.data.payloadHash },
      ),
      404,
    )
  })

  test('QG-RELEASE-003 reconciles timeout-after-commit, not-found, acknowledgement drift, conflict, and operator CAS', async ({ request }) => {
    const release = await resolveGoldenRelease('QG-RELEASE-003')

    const startFaultedPreview = async (fault: 'controller-conflict' | 'controller-not-found' | 'controller-timeout') => {
      const started = await release.client.release.startPreview(
        release.projectId,
        { id: release.bundle.id, contentHash: release.bundle.bundleHash },
        `QG-RELEASE-003 exact ${fault} operation`,
        { idempotencyKey: qualificationKey('QG-RELEASE-003', fault) },
      )
      const faultReceipt = await consumeGoldenFault(request, fault)
      assertGoldenFaultTarget(faultReceipt, {
        id: started.data.run.id,
        kind: 'release-delivery-operation',
        projectId: release.projectId,
      })
      return { faultReceipt, run: await waitPreview(release, started.data.run.id) }
    }

    const timeout = await startFaultedPreview('controller-timeout')
    expect(timeout.run.state).toBe('passed')
    const timeoutReceipt = await readPreviewReceipt(release, timeout.run)
    const timeoutResult = assertControllerResult(timeout.run, timeoutReceipt)
    const timeoutEvidence = await observeBusinessOperation(
      request, release, 'QG-RELEASE-003-TIMEOUT', 'preview', timeout.run.id,
    )
    expect(timeoutEvidence.operation.id).toBe(timeout.run.id)
    expect(timeoutEvidence.result.resultHash).toBe(timeoutResult.resultHash)
    expect(timeoutEvidence.attempts.map((attempt, index) => (
      assertExactObject(attempt, [
        'kind', 'ordinal', 'outcome',
      ], `timeout attempt ${index}`)
    ))).toEqual([
      { kind: 'submit', ordinal: 1, outcome: 'response_lost_after_commit' },
      { kind: 'reconcile', ordinal: 2, outcome: 'same_operation_completed' },
    ])

    const notFound = await startFaultedPreview('controller-not-found')
    expect(notFound.run.state).toBe('passed')
    const notFoundEvidence = await observeBusinessOperation(
      request, release, 'QG-RELEASE-003-NOT-FOUND', 'preview', notFound.run.id,
    )
    expect(notFoundEvidence.attempts.map((attempt, index) => (
      assertExactObject(attempt, [
        'caseExisted', 'kind', 'ordinal', 'outcome', 'putCount',
      ], `not-found attempt ${index}`)
    ))).toEqual([
      { caseExisted: false, kind: 'submit', ordinal: 1, outcome: 'response_lost', putCount: 1 },
      { caseExisted: false, kind: 'reconcile', ordinal: 2, outcome: 'not_found', putCount: 1 },
      { caseExisted: false, kind: 'resubmit', ordinal: 3, outcome: 'same_operation_completed', putCount: 2 },
    ])

    const conflict = await startFaultedPreview('controller-conflict')
    expect(conflict.run.state).toBe('reconcile_blocked')
    const block = await release.client.release.getBlockedDeliveryReconciliation(
      release.projectId, 'preview', conflict.run.id,
    )
    expect(block.data.operationId).toBe(conflict.run.id)
    expect(block.data.controller.id).toBe(goldenSubject().release.controller.identity)
    expect(block.data.controller.trustKeyDigest).toBe(goldenSubject().release.controller.trustKeyDigest)

    const beforeCases = await release.client.release.listDeliveryReconciliationCases(release.projectId)
    const user = platformClient(goldenQualificationEnvironment().credentials.platform.apiA)
    const correctInput = {
      expectedErrorCode: block.data.lastError.code,
      expectedVersion: block.data.expectedRunVersion,
      reason: 'QG-RELEASE-003 exact operator reconciliation Case',
      runId: conflict.run.id,
      runKind: 'preview' as const,
    }
    await expectPlatformFailure(
      () => user.release.resumeBlockedDeliveryReconciliation(release.projectId, correctInput, {
        idempotencyKey: qualificationKey('QG-RELEASE-003', 'wrong-role'),
      }),
      403,
    )
    await expectPlatformFailure(
      () => release.client.release.resumeBlockedDeliveryReconciliation(
        release.projectId,
        { ...correctInput, expectedVersion: correctInput.expectedVersion + 1 },
        { idempotencyKey: qualificationKey('QG-RELEASE-003', 'wrong-version') },
      ),
      409,
      'release_delivery_reconciliation_conflict',
    )
    await expectPlatformFailure(
      () => release.client.release.resumeBlockedDeliveryReconciliation(
        release.projectId,
        { ...correctInput, expectedErrorCode: `${correctInput.expectedErrorCode}-drift` },
        { idempotencyKey: qualificationKey('QG-RELEASE-003', 'wrong-code') },
      ),
      409,
      'release_delivery_reconciliation_conflict',
    )
    const afterRejectedAuthorizations = await release.client.release.listDeliveryReconciliationCases(
      release.projectId,
    )
    const unchangedBlockedRun = await release.client.release.getPreviewRun(
      release.projectId,
      conflict.run.id,
    )
    expect(afterRejectedAuthorizations.data).toEqual(beforeCases.data)
    expect(unchangedBlockedRun.data).toEqual(conflict.run)
    const createdCase = await release.client.release.resumeBlockedDeliveryReconciliation(
      release.projectId,
      correctInput,
      { idempotencyKey: qualificationKey('QG-RELEASE-003', 'correct-case') },
    )
    expect(createdCase.data.replayed).toBe(false)
    expect(createdCase.data.case.operationId).toBe(conflict.run.id)
    expect(createdCase.data.case.operationRequestHash).toBe(block.data.operationRequestHash)
    const replayedCase = await release.client.release.resumeBlockedDeliveryReconciliation(
      release.projectId,
      correctInput,
      { idempotencyKey: qualificationKey('QG-RELEASE-003', 'correct-case') },
    )
    expect(replayedCase.data.replayed).toBe(true)
    expect(replayedCase.data.case).toEqual(createdCase.data.case)
    const afterCases = await release.client.release.listDeliveryReconciliationCases(release.projectId)
    expect(afterCases.data).toHaveLength(beforeCases.data.length + 1)
    expect(afterCases.data.filter((entry) => entry.id === createdCase.data.case.id)).toHaveLength(1)
    const resumedVersion = await waitForValue(
      () => release.client.release.getPreviewRun(release.projectId, conflict.run.id),
      (result) => result.data.version > block.data.expectedRunVersion,
      'QG-RELEASE-003 Case-authorized Run version',
    )
    expect(resumedVersion.data.id).toBe(conflict.run.id)

    const matrix = await runServerQualification(
      request,
      release,
      'QG-RELEASE-003',
      'controller-reconciliation-branch-matrix',
      { caseId: createdCase.data.case.id, operationId: conflict.run.id },
    )
    const branches = assertExactObject(matrix.evidence, [
      'acknowledgementDrift', 'afterCaseNotFound', 'schemaVersion', 'wrongController',
    ], 'controller branch matrix')
    expect(branches.schemaVersion).toBe('release-controller-branch-evidence/v1')
    for (const name of ['acknowledgementDrift', 'wrongController'] as const) {
      const branch = assertExactObject(branches[name], [
        'caseCountDelta', 'operationCountDelta', 'outcome', 'runVersionDelta',
      ], name)
      expect(branch.outcome).toBe('fail_closed')
      expect(branch.caseCountDelta).toBe(0)
      expect(branch.operationCountDelta).toBe(0)
      expect(branch.runVersionDelta).toBe(0)
    }
    const permanentNotFound = assertExactObject(branches.afterCaseNotFound, [
      'getCount', 'outcome', 'putCount', 'sameOperationId',
    ], 'after-Case not-found')
    expect(permanentNotFound).toEqual({
      getCount: 1,
      outcome: 'permanent_reconcile_blocked',
      putCount: 0,
      sameOperationId: conflict.run.id,
    })
  })

  test('QG-RELEASE-004 enforces mutation maintenance and converges legacy v1 with legacy-v3 writer races', async ({ request }) => {
    const { receipt: previewReceipt, release } = await passingPreview('QG-RELEASE-004')
    const approval = await release.client.release.approvePromotion(
      release.projectId,
      { id: previewReceipt.id, contentHash: previewReceipt.payloadHash },
      'QG-RELEASE-004 establish readable immutable production history',
      { idempotencyKey: qualificationKey('QG-RELEASE-004', 'approval-before-maintenance') },
    )
    const productionStarted = await release.client.release.startPromotion(
      release.projectId,
      { id: approval.data.approval.id, contentHash: approval.data.approval.payloadHash },
      'QG-RELEASE-004 establish rollback authority before maintenance',
      { idempotencyKey: qualificationKey('QG-RELEASE-004', 'production-before-maintenance') },
    )
    const production = await waitProduction(release, productionStarted.data.run.id)
    expect(production.state).toBe('healthy')
    expect(production.revision).toBeTruthy()
    const maintenanceStarted = await release.client.release.startPreview(
      release.projectId,
      { id: release.bundle.id, contentHash: release.bundle.bundleHash },
      'QG-RELEASE-004 live operation entering Controller maintenance',
      { idempotencyKey: qualificationKey('QG-RELEASE-004', 'maintenance-operation') },
    )
    const faultReceipt = await consumeGoldenFault(request, 'controller-maintenance')
    assertGoldenFaultTarget(faultReceipt, {
      id: maintenanceStarted.data.run.id,
      kind: 'release-delivery-operation',
      projectId: release.projectId,
    })
    const blocked = await waitPreview(release, maintenanceStarted.data.run.id)
    expect(blocked.state).toBe('reconcile_blocked')
    const block = await release.client.release.getBlockedDeliveryReconciliation(
      release.projectId, 'preview', blocked.id,
    )
    expect(block.data.lastError.code).toMatch(/maintenance/iu)
    const capabilities = await release.client.release.getCapabilities(release.projectId)
    expect(capabilities.data.deliveryEnabled).toBe(false)

    await expectPlatformFailure(
      () => release.client.release.startPreview(
        release.projectId,
        { id: release.bundle.id, contentHash: release.bundle.bundleHash },
        'must remain closed',
        { idempotencyKey: qualificationKey('QG-RELEASE-004', 'maintenance-preview') },
      ),
      503,
    )
    await expectPlatformFailure(
      () => release.client.release.startPromotion(
        release.projectId,
        { id: approval.data.approval.id, contentHash: approval.data.approval.payloadHash },
        'must remain closed',
        { idempotencyKey: qualificationKey('QG-RELEASE-004', 'maintenance-promotion') },
      ),
      503,
    )
    await expectPlatformFailure(
      () => release.client.release.startRollback(
        release.projectId,
        production.revision!,
        'must remain closed',
        { idempotencyKey: qualificationKey('QG-RELEASE-004', 'maintenance-rollback') },
      ),
      503,
    )
    await expectPlatformFailure(
      () => release.client.release.resumeBlockedDeliveryReconciliation(
        release.projectId,
        {
          expectedErrorCode: block.data.lastError.code,
          expectedVersion: block.data.expectedRunVersion,
          reason: 'must remain closed',
          runId: blocked.id,
          runKind: 'preview',
        },
        { idempotencyKey: qualificationKey('QG-RELEASE-004', 'maintenance-reconcile') },
      ),
      503,
    )

    expect((await release.client.release.getPreviewRun(release.projectId, blocked.id)).data).toEqual(blocked)
    expect((await release.client.release.getPreviewReceipt(
      release.projectId,
      { id: previewReceipt.id, contentHash: previewReceipt.payloadHash },
    )).data).toEqual(previewReceipt)
    expect((await release.client.release.getProductionRun(release.projectId, production.id)).data).toEqual(production)
    expect((await release.client.release.getDeploymentRevision(release.projectId, production.revision!)).data.runId)
      .toBe(production.id)
    expect((await release.client.release.listDeliveryReconciliationCases(release.projectId)).data)
      .toEqual(expect.any(Array))

    const migration = await runServerQualification(
      request,
      release,
      'QG-RELEASE-004',
      'legacy-v1-migration-v3-writer-race',
      {},
    )
    const evidence = assertExactObject(migration.evidence, [
      'legacy', 'race', 'readiness', 'schemaVersion',
    ], 'legacy migration evidence')
    expect(evidence.schemaVersion).toBe('release-delivery-legacy-convergence-evidence/v1')
    const legacy = assertExactObject(evidence.legacy, [
      'historicalRunId', 'immutable', 'migrationCount', 'sourceSchemaVersion', 'targetState',
    ], 'legacy v1 migration')
    expect(legacy.sourceSchemaVersion).toBe('release-preview-run/v1')
    expect(legacy.targetState).toBe('reconcile_blocked')
    expect(legacy.migrationCount).toBe(1)
    expect(legacy.immutable).toBe(true)
    assertUUID(requiredString(legacy.historicalRunId, 'legacy.historicalRunId'), 'legacy.historicalRunId')
    const race = assertExactObject(evidence.race, [
      'legacyWriterOutcome', 'operationCount', 'v3WriterOutcome', 'winningRunId',
    ], 'legacy/v3 writer race')
    expect([race.legacyWriterOutcome, race.v3WriterOutcome].sort()).toEqual(['conflict', 'created'])
    expect(race.operationCount).toBe(1)
    assertUUID(requiredString(race.winningRunId, 'race.winningRunId'), 'race.winningRunId')
    const readiness = assertExactObject(evidence.readiness, [
      'checkedAt', 'legacyWritableCount', 'ready', 'violations',
    ], 'post-migration readiness')
    expect(readiness.ready).toBe(true)
    expect(readiness.legacyWritableCount).toBe(0)
    expect(readiness.violations).toEqual([])
    assertTimestamp(requiredString(readiness.checkedAt, 'readiness.checkedAt'), 'readiness.checkedAt')
  })

  test('QG-RELEASE-005 blocks nested authority drift, database clock skew, and orphan Run or Operation state', async ({ request }) => {
    const release = await resolveGoldenRelease('QG-RELEASE-005')
    const qualified = await runServerQualification(
      request,
      release,
      'QG-RELEASE-005',
      'release-authority-postgresql-readiness',
      {},
    )
    const evidence = assertExactObject(qualified.evidence, [
      'lease', 'lineage', 'nestedAuthority', 'readiness', 'schemaVersion',
    ], 'Release server posture evidence')
    expect(evidence.schemaVersion).toBe('release-delivery-postgresql-readiness-evidence/v1')

    const nested = assertExactObject(evidence.nestedAuthority, [
      'hashDriftRejected', 'nullRejected', 'operationCountDelta', 'resultCountDelta',
    ], 'nested authority evidence')
    expect(nested).toEqual({
      hashDriftRejected: true,
      nullRejected: true,
      operationCountDelta: 0,
      resultCountDelta: 0,
    })

    const lease = assertExactObject(evidence.lease, [
      'claimedAt', 'clientClockOffsetMs', 'clockSource', 'expiresAt', 'staleFenceRejected',
    ], 'database lease evidence')
    expect(lease.clockSource).toBe('postgresql.statement_timestamp')
    expect(Math.abs(requiredInteger(lease.clientClockOffsetMs, 'lease.clientClockOffsetMs')))
      .toBeGreaterThan(300_000)
    expect(lease.staleFenceRejected).toBe(true)
    const claimedAt = requiredString(lease.claimedAt, 'lease.claimedAt')
    const expiresAt = requiredString(lease.expiresAt, 'lease.expiresAt')
    assertTimestamp(claimedAt, 'lease.claimedAt')
    assertTimestamp(expiresAt, 'lease.expiresAt')
    expect(Date.parse(expiresAt)).toBeGreaterThan(Date.parse(claimedAt))

    const lineage = assertExactObject(evidence.lineage, [
      'deferredResultRecovered', 'operationWithoutRunRejected', 'orphanCount',
      'runWithoutOperationRejected',
    ], 'Run/Operation lineage evidence')
    expect(lineage.runWithoutOperationRejected).toBe(true)
    expect(lineage.operationWithoutRunRejected).toBe(true)
    expect(lineage.deferredResultRecovered).toBe(true)
    expect(lineage.orphanCount).toBe(0)

    const readiness = assertExactObject(evidence.readiness, [
      'checkedAt', 'ready', 'violations',
    ], 'Release readiness evidence')
    expect(readiness.ready).toBe(true)
    expect(readiness.violations).toEqual([])
    assertTimestamp(requiredString(readiness.checkedAt, 'readiness.checkedAt'), 'readiness.checkedAt')

    const owner = goldenPrincipal('platform-owner')
    const otherTenant = platformClient(goldenQualificationEnvironment().credentials.platform.apiB)
    await expectPlatformFailure(
      () => otherTenant.release.getBundle(
        goldenPrincipal('platform-user-b').projectId,
        release.bundle.id,
        release.bundle.bundleHash,
      ),
      404,
    )
    expect(owner.projectId).toBe(release.projectId)
  })
})
