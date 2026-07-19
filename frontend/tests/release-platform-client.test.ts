import assert from 'node:assert/strict'
import { HttpClient, PlatformProtocolError, type CsrfTokenStore, type FetchLike } from '../lib/platform/http'
import { ReleaseClient } from '../lib/platform/release-client'
import {
  hasReleaseDeliveryMutationLock,
  isReleaseDeliveryMutationLocked,
  isReleaseDeliveryRunTerminal,
  normalizeReleaseBundle,
  normalizeReleaseBundleView,
  normalizeReleaseDeliveryReconciliationBlock,
  normalizeReleaseDeliveryReconciliationCase,
  normalizeReleaseDeliveryReconciliationCaseList,
  normalizeReleaseDeploymentRevision,
  normalizeReleasePreviewReceipt,
  normalizeReleasePreviewRun,
  normalizeReleasePreviewRunList,
  normalizeReleaseProductionReceipt,
  normalizeReleasePromotionApproval,
  normalizeReleasePromotionApprovalView,
  selectReleaseDeliveryReconciliationCaseForRun,
  selectReleaseDeliveryRunForDisplay,
  type ReleaseDeliveryRunState,
} from '../lib/platform/release-contract'

const projectId = '11111111-1111-4111-8111-111111111111'
const bundle = {
  id: '22222222-2222-4222-8222-222222222222',
  contentHash: `sha256:${'a'.repeat(64)}`,
}
const previewReceipt = {
  id: '33333333-3333-4333-8333-333333333333',
  contentHash: `sha256:${'b'.repeat(64)}`,
}
const approval = {
  id: '44444444-4444-4444-8444-444444444444',
  contentHash: `sha256:${'c'.repeat(64)}`,
}
const sourceRevision = {
  id: '55555555-5555-4555-8555-555555555555',
  contentHash: `sha256:${'d'.repeat(64)}`,
}
const productionReceipt = {
  id: '88888888-8888-4888-8888-888888888888',
  contentHash: `sha256:${'e'.repeat(64)}`,
}
const deploymentRevision = {
  id: '12121212-1212-4212-8212-121212121212',
  contentHash: `sha256:${'f'.repeat(64)}`,
}
const previewControllerOperation = {
  operationId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01',
  resultHash: `sha256:${'7'.repeat(64)}`,
}
const productionControllerOperation = {
  operationId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa02',
  resultHash: `sha256:${'8'.repeat(64)}`,
}
const workspace = {
  workspaceArtifactId: '13131313-1313-4313-8313-131313131313',
  workspaceRevisionId: '14141414-1414-4414-8414-141414141414',
  workspaceContentHash: `sha256:${'0'.repeat(64)}`,
} as const
const canonicalReceipt = {
  id: '15151515-1515-4515-8515-151515151515',
  contentHash: `sha256:${'1'.repeat(64)}`,
} as const
const buildManifest = {
  id: '16161616-1616-4616-8616-161616161616',
  contentHash: `sha256:${'2'.repeat(64)}`,
} as const
const buildContract = {
  id: '17171717-1717-4717-8717-171717171717',
  contentHash: `sha256:${'3'.repeat(64)}`,
} as const
const fullStackTemplate = {
  id: '18181818-1818-4818-8818-181818181818',
  contentHash: `sha256:${'4'.repeat(64)}`,
} as const
const releaseArtifacts = [
  { id: '01-health', kind: 'health-readiness-contract', store: 'blob', ref: 'health', contentHash: `sha256:${'1'.repeat(64)}`, mediaType: 'application/json', byteSize: 1 },
  { id: '02-migration', kind: 'migration', store: 'blob', ref: 'migration', contentHash: `sha256:${'2'.repeat(64)}`, mediaType: 'application/json', byteSize: 2 },
  { id: '03-provenance', kind: 'provenance', store: 'blob', ref: 'provenance', contentHash: `sha256:${'3'.repeat(64)}`, mediaType: 'application/json', byteSize: 3 },
  { id: '04-runtime', kind: 'runtime-config-schema', store: 'blob', ref: 'runtime', contentHash: `sha256:${'4'.repeat(64)}`, mediaType: 'application/json', byteSize: 4 },
  { id: '05-sbom', kind: 'sbom', store: 'blob', ref: 'sbom', contentHash: `sha256:${'5'.repeat(64)}`, mediaType: 'application/json', byteSize: 5 },
  { id: '06-signature', kind: 'signature', store: 'blob', ref: 'signature', contentHash: `sha256:${'6'.repeat(64)}`, mediaType: 'application/json', byteSize: 6 },
  { id: '07-vulnerability', kind: 'vulnerability-report', store: 'blob', ref: 'vulnerability', contentHash: `sha256:${'7'.repeat(64)}`, mediaType: 'application/json', byteSize: 7 },
  { id: '08-web', kind: 'web-static', store: 'blob', ref: 'web', contentHash: `sha256:${'8'.repeat(64)}`, mediaType: 'application/gzip', byteSize: 8 },
] as const
const releaseBundleDocument = {
  schemaVersion: 'release-bundle/v1',
  id: bundle.id,
  projectId,
  workspace,
  canonicalReceipt,
  buildManifest,
  buildContract,
  fullStackTemplate,
  verificationProfile: {
    id: 'canonical-release',
    version: 1,
    contentHash: `sha256:${'9'.repeat(64)}`,
  },
  releaseArtifacts,
  bundleHash: bundle.contentHash,
  createdBy: projectId,
  createdAt: '2026-07-18T10:55:00Z',
} as const
const previewReceiptV1 = {
  schemaVersion: 'release-preview-receipt/v1',
  id: previewReceipt.id,
  runId: '66666666-6666-4666-8666-666666666666',
  projectId,
  releaseBundle: bundle,
  canonicalReceipt,
  workspace,
  releaseArtifacts,
  namespace: 'preview-canary',
  provider: 'canary',
  providerRef: 'preview/ref',
  checks: [
    { id: 'contract', kind: 'contract', status: 'passed' },
    { id: 'health', kind: 'health', status: 'passed' },
    { id: 'migration', kind: 'migration', status: 'passed' },
    { id: 'playwright', kind: 'e2e', status: 'passed' },
    { id: 'smoke', kind: 'smoke', status: 'passed' },
  ],
  decision: 'passed',
  payloadHash: previewReceipt.contentHash,
  createdBy: projectId,
  createdAt: '2026-07-18T11:00:00Z',
} as const
const previewReceiptV2 = {
  ...previewReceiptV1,
  schemaVersion: 'release-preview-receipt/v2',
  controllerOperation: previewControllerOperation,
} as const
const productionReceiptV1 = {
  schemaVersion: 'release-production-receipt/v1',
  id: productionReceipt.id,
  runId: '77777777-7777-4777-8777-777777777777',
  projectId,
  operation: 'promote',
  releaseBundle: bundle,
  previewReceipt,
  promotionApproval: approval,
  provider: 'canary',
  providerRef: 'production/ref',
  publicUrl: '',
  checks: [{ id: 'health', kind: 'health', status: 'failed', detail: '503' }],
  decision: 'failed',
  payloadHash: productionReceipt.contentHash,
  createdBy: projectId,
  createdAt: '2026-07-18T11:05:00Z',
} as const
const productionReceiptV2 = {
  ...productionReceiptV1,
  schemaVersion: 'release-production-receipt/v2',
  controllerOperation: productionControllerOperation,
} as const
const deploymentRevisionV2 = {
  schemaVersion: 'release-deployment-revision/v2',
  id: deploymentRevision.id,
  runId: productionReceiptV1.runId,
  projectId,
  releaseBundle: bundle,
  previewReceipt,
  promotionApproval: approval,
  productionReceipt,
  operation: 'promote',
  provider: 'canary',
  providerRef: 'production/ref',
  publicUrl: 'https://application.example.test',
  checks: [
    { id: 'health', kind: 'health', status: 'passed' },
    { id: 'rollout', kind: 'rollout', status: 'passed' },
  ],
  controllerOperation: productionControllerOperation,
  payloadHash: deploymentRevision.contentHash,
  createdBy: projectId,
  createdAt: '2026-07-18T11:10:00Z',
} as const
const promotionApproval = {
  schemaVersion: 'release-promotion-approval/v1',
  id: approval.id,
  projectId,
  releaseBundle: bundle,
  previewReceipt,
  reason: 'approve',
  payloadHash: approval.contentHash,
  createdBy: projectId,
  createdAt: '2026-07-18T11:02:00Z',
} as const
const reconciliationCase = {
  schemaVersion: 'release-delivery-reconciliation-case/v1',
  id: '99999999-9999-4999-8999-999999999999',
  projectId,
  runKind: 'preview',
  runId: '66666666-6666-4666-8666-666666666666',
  runSchemaVersion: 'release-preview-run/v2',
  expectedRunVersion: 7,
  operationId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
  operationRequestHash: `sha256:${'1'.repeat(64)}`,
  controller: {
    schemaVersion: 'release-delivery-controller-identity/v1',
    id: 'release-controller',
    version: '2026.07.18+build.42',
    protocol: 'worksflow.release-delivery/v3',
    trustKeyDigest: `sha256:${'2'.repeat(64)}`,
  },
  previousRemoteState: 'quarantined',
  resumeRemoteState: 'submit_unknown',
  submitAttemptCount: 1,
  reconcileAttemptCount: 3,
  lastAttempt: {
    ordinal: 4,
    kind: 'reconcile',
    workerId: 'release-worker-1',
    fenceEpoch: 4,
    startedAt: '2026-07-18T10:00:00Z',
    completedAt: '2026-07-18T10:00:01Z',
    outcome: 'quarantined',
    errorCode: 'controller-protocol-error',
    errorDetail: 'The controller returned non-canonical evidence.',
  },
  lastObservation: null,
  quarantineError: {
    code: 'controller-protocol-error',
    detail: 'The controller returned non-canonical evidence.',
  },
  actorId: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
  reason: 'Controller repair was independently verified.',
  idempotencyKey: 'resume-case-1',
  requestHash: `sha256:${'3'.repeat(64)}`,
  caseHash: `sha256:${'4'.repeat(64)}`,
  createdAt: '2026-07-18T10:05:00Z',
} as const
const reconciliationBlock = {
  schemaVersion: 'release-delivery-reconciliation-block/v1',
  projectId,
  runKind: reconciliationCase.runKind,
  runId: reconciliationCase.runId,
  runSchemaVersion: reconciliationCase.runSchemaVersion,
  expectedRunVersion: reconciliationCase.expectedRunVersion,
  operationId: reconciliationCase.operationId,
  operationRequestHash: reconciliationCase.operationRequestHash,
  controller: reconciliationCase.controller,
  lastError: reconciliationCase.quarantineError,
} as const

function tokenStore(): CsrfTokenStore {
  return { get: () => 'csrf-release', set: () => {}, clear: () => {} }
}

function releaseClientReturning(value: unknown) {
  return new ReleaseClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async () => Response.json(value)) as FetchLike,
  }))
}

function withoutField(value: Record<string, unknown>, field: string): Record<string, unknown> {
  return Object.fromEntries(Object.entries(value).filter(([key]) => key !== field))
}

async function main() {
  assert.deepEqual(normalizeReleaseBundle(releaseBundleDocument), releaseBundleDocument)
  assert.deepEqual(normalizeReleaseBundleView({ bundle: releaseBundleDocument, replayed: false }), {
    bundle: releaseBundleDocument,
    replayed: false,
  })
  assert.deepEqual(normalizeReleasePromotionApproval(promotionApproval), promotionApproval)
  assert.deepEqual(normalizeReleasePromotionApprovalView({ approval: promotionApproval, replayed: true }), {
    approval: promotionApproval,
    replayed: true,
  })
  for (const malformed of [
    withoutField(releaseBundleDocument, 'createdAt'),
    { ...releaseBundleDocument, workspace: null },
    { ...releaseBundleDocument, releaseArtifacts: releaseArtifacts.map((artifact, index) => (
      index === 0 ? { ...artifact, byteSize: '1' } : artifact
    )) },
    { ...releaseBundleDocument, unexpected: true },
    { ...releaseBundleDocument, schemaVersion: 'release-bundle/v2' },
  ]) {
    assert.throws(() => normalizeReleaseBundle(malformed), /malformed immutable release Bundle/)
  }
  assert.throws(
    () => normalizeReleaseBundleView({ bundle: releaseBundleDocument }),
    /malformed immutable release Bundle response/,
  )
  assert.throws(
    () => normalizeReleasePromotionApproval({ ...promotionApproval, payloadHash: null }),
    /malformed immutable release PromotionApproval/,
  )
  assert.throws(
    () => normalizeReleasePromotionApproval({ ...promotionApproval, unexpected: true }),
    /malformed immutable release PromotionApproval/,
  )
  assert.throws(
    () => normalizeReleasePromotionApprovalView({ approval: promotionApproval, replayed: 1 }),
    /malformed immutable release PromotionApproval response/,
  )

  assert.equal(
    (await releaseClientReturning(releaseBundleDocument).getBundle(
      projectId,
      bundle.id,
      bundle.contentHash,
    )).data.bundleHash,
    bundle.contentHash,
  )
  assert.equal(
    (await releaseClientReturning({ bundle: releaseBundleDocument, replayed: false }).createBundle(
      projectId,
      canonicalReceipt,
    )).data.bundle.canonicalReceipt.contentHash,
    canonicalReceipt.contentHash,
  )
  assert.equal(
    (await releaseClientReturning(releaseBundleDocument).getBundleByReceipt(
      projectId,
      canonicalReceipt,
    )).data.id,
    bundle.id,
  )
  assert.equal(
    (await releaseClientReturning({ approval: promotionApproval, replayed: false }).approvePromotion(
      projectId,
      previewReceipt,
      'approve',
    )).data.approval.payloadHash,
    approval.contentHash,
  )
  assert.equal(
    (await releaseClientReturning(promotionApproval).getPromotionApprovalByPreview(
      projectId,
      previewReceipt,
    )).data.id,
    approval.id,
  )
  await assert.rejects(
    releaseClientReturning({ ...releaseBundleDocument, id: sourceRevision.id }).getBundle(
      projectId,
      bundle.id,
      bundle.contentHash,
    ),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({ ...releaseBundleDocument, bundleHash: sourceRevision.contentHash }).getBundle(
      projectId,
      bundle.id,
      bundle.contentHash,
    ),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({ ...releaseBundleDocument, unexpected: true }).getBundle(
      projectId,
      bundle.id,
      bundle.contentHash,
    ),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      bundle: {
        ...releaseBundleDocument,
        canonicalReceipt: { ...canonicalReceipt, contentHash: sourceRevision.contentHash },
      },
      replayed: false,
    }).createBundle(projectId, canonicalReceipt),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      ...releaseBundleDocument,
      canonicalReceipt: { ...canonicalReceipt, id: sourceRevision.id },
    }).getBundleByReceipt(projectId, canonicalReceipt),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      approval: { ...promotionApproval, reason: 'different reason' },
      replayed: false,
    }).approvePromotion(projectId, previewReceipt, 'approve'),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      ...promotionApproval,
      previewReceipt: { ...previewReceipt, contentHash: sourceRevision.contentHash },
    }).getPromotionApprovalByPreview(projectId, previewReceipt),
    PlatformProtocolError,
  )

  const legacyPreviewReceipt = normalizeReleasePreviewReceipt(previewReceiptV1)
  assert.equal(legacyPreviewReceipt.schemaVersion, 'release-preview-receipt/v1')
  assert.equal(legacyPreviewReceipt.controllerOperation, null)
  assert.deepEqual(legacyPreviewReceipt.canonicalReceipt, canonicalReceipt)
  assert.deepEqual(legacyPreviewReceipt.workspace, workspace)
  assert.deepEqual(legacyPreviewReceipt.releaseArtifacts, releaseArtifacts)
  const legacyProductionReceipt = normalizeReleaseProductionReceipt(productionReceiptV1)
  assert.equal(legacyProductionReceipt.schemaVersion, 'release-production-receipt/v1')
  assert.equal(legacyProductionReceipt.controllerOperation, null)
  const legacyDeploymentRevision = normalizeReleaseDeploymentRevision(withoutField({
    ...deploymentRevisionV2,
    schemaVersion: 'release-deployment-revision/v1',
  }, 'controllerOperation'))
  assert.equal(legacyDeploymentRevision.schemaVersion, 'release-deployment-revision/v1')
  assert.equal(legacyDeploymentRevision.controllerOperation, null)
  for (const malformed of [
    withoutField(previewReceiptV2, 'canonicalReceipt'),
    { ...previewReceiptV2, workspace: null },
    { ...previewReceiptV2, releaseArtifacts: null },
    { ...previewReceiptV2, unexpected: true },
    { ...previewReceiptV2, checks: [{ ...previewReceiptV2.checks[0], status: true }] },
  ]) {
    assert.throws(() => normalizeReleasePreviewReceipt(malformed), /malformed release preview receipt/)
  }
  assert.throws(
    () => normalizeReleasePreviewReceipt({ ...previewReceiptV1, controllerOperation: null }),
    /malformed release preview receipt/,
  )
  assert.throws(
    () => normalizeReleaseProductionReceipt({ ...productionReceiptV2, sourceRevision: null }),
    /malformed release production receipt/,
  )
  assert.throws(
    () => normalizeReleaseProductionReceipt({ ...productionReceiptV2, sourceRevision: undefined }),
    /malformed release production receipt/,
  )
  assert.throws(
    () => normalizeReleaseProductionReceipt({ ...productionReceiptV2, unexpected: true }),
    /malformed release production receipt/,
  )
  assert.throws(
    () => normalizeReleaseDeploymentRevision({ ...deploymentRevisionV2, sourceRevision: null }),
    /malformed release deployment/,
  )
  assert.throws(
    () => normalizeReleaseDeploymentRevision({ ...deploymentRevisionV2, sourceRevision: undefined }),
    /malformed release deployment/,
  )

  const deliveryStateExpectations: ReadonlyArray<readonly [ReleaseDeliveryRunState, boolean, boolean]> = [
    ['queued', true, false],
    ['claimed', true, false],
    ['submitting', true, false],
    ['reconcile_wait', true, false],
    ['reconciling', true, false],
    ['deploying', true, false],
    ['verifying', true, false],
    ['reconcile_blocked', true, true],
    ['passed', false, true],
    ['healthy', false, true],
    ['failed', false, true],
    ['error', false, true],
    ['cancelled', false, true],
  ]
  for (const [state, mutationLocked, terminal] of deliveryStateExpectations) {
    const run = normalizeReleasePreviewRun({
      id: '66666666-6666-4666-8666-666666666666',
      projectId,
      releaseBundle: bundle,
      reason: 'state projection',
      state,
      version: 1,
      createdBy: projectId,
      createdAt: '',
      updatedAt: '',
    })
    assert.equal(run.state, state)
    assert.equal(isReleaseDeliveryMutationLocked(run.state), mutationLocked, `${state} mutation lock`)
    assert.equal(isReleaseDeliveryRunTerminal(run.state), terminal, `${state} terminal projection`)
  }
  const unknownState = normalizeReleasePreviewRun({ state: 'future_controller_state' })
  assert.equal(unknownState.state, 'reconcile_blocked')
  assert.equal(isReleaseDeliveryMutationLocked(unknownState.state), true)

  const multiplePreviewRuns = normalizeReleasePreviewRunList({
    runs: [
      { id: 'passed-newest', state: 'passed' },
      { id: 'queued-hidden', state: 'queued' },
      { id: 'unknown-hidden', state: 'future_controller_state' },
      { id: 'reconciling-hidden', state: 'reconciling' },
    ],
  })
  assert.equal(hasReleaseDeliveryMutationLock(multiplePreviewRuns), true)
  assert.equal(selectReleaseDeliveryRunForDisplay(multiplePreviewRuns)?.id, 'unknown-hidden')
  assert.equal(selectReleaseDeliveryRunForDisplay(multiplePreviewRuns)?.state, 'reconcile_blocked')
  assert.equal(
    selectReleaseDeliveryRunForDisplay(multiplePreviewRuns.filter((run) => run.state !== 'reconcile_blocked'))?.id,
    'reconciling-hidden',
  )
  assert.equal(
    selectReleaseDeliveryRunForDisplay(multiplePreviewRuns.filter((run) => !isReleaseDeliveryMutationLocked(run.state)))?.id,
    'passed-newest',
  )

  const normalizedCase = normalizeReleaseDeliveryReconciliationCase(reconciliationCase)
  assert.equal(normalizedCase.quarantineError.code, 'controller-protocol-error')
  assert.equal(normalizedCase.lastObservation, undefined)
  assert.deepEqual(normalizeReleaseDeliveryReconciliationCaseList({ cases: [reconciliationCase] }), [normalizedCase])
  assert.throws(
    () => normalizeReleaseDeliveryReconciliationCase({
      ...reconciliationCase,
      quarantineError: { ...reconciliationCase.quarantineError, code: 'different-error' },
    }),
    /malformed immutable delivery reconciliation case/,
  )
  assert.throws(
    () => normalizeReleaseDeliveryReconciliationCaseList({ cases: null }),
    /malformed delivery reconciliation case list/,
  )
  assert.throws(
    () => normalizeReleaseDeliveryReconciliationCase({ ...reconciliationCase, reason: ' padded reason ' }),
    /malformed immutable delivery reconciliation case/,
  )
  assert.deepEqual(normalizeReleaseDeliveryReconciliationBlock(reconciliationBlock), reconciliationBlock)

  const previousBlockedVersion = normalizeReleaseDeliveryReconciliationCase({
    ...reconciliationCase,
    id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
    expectedRunVersion: 5,
    caseHash: `sha256:${'5'.repeat(64)}`,
  })
  assert.equal(
    selectReleaseDeliveryReconciliationCaseForRun(
      [previousBlockedVersion],
      'preview',
      reconciliationCase.runId,
      8,
    ),
    undefined,
    'a historical Case must not hide a later blocked Run version',
  )
  const currentBlockedVersion = normalizeReleaseDeliveryReconciliationCase({
    ...reconciliationCase,
    id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
    expectedRunVersion: 8,
    caseHash: `sha256:${'6'.repeat(64)}`,
  })
  assert.equal(
    selectReleaseDeliveryReconciliationCaseForRun(
      [previousBlockedVersion, currentBlockedVersion],
      'preview',
      reconciliationCase.runId,
      8,
    )?.id,
    currentBlockedVersion.id,
    'the exact blocked Run version suppresses duplicate authorization',
  )

  const calls: Array<{ readonly url: string; readonly init?: RequestInit }> = []
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async (input, init) => {
      const url = input.toString()
      calls.push({ url, init })
      if (url.endsWith('/release-capabilities')) {
        return Response.json({ schemaVersion: 'release-capabilities/v1', deliveryEnabled: false })
      }
      if (url.endsWith('/release-delivery-reconciliation-cases')) {
        if (init?.method === 'POST') {
          return Response.json({ case: reconciliationCase, replayed: false }, { status: 202 })
        }
        return Response.json({ cases: [reconciliationCase] })
      }
      if (url.endsWith(`/release-delivery-reconciliation-blocks/preview/${reconciliationCase.runId}`)) {
        return Response.json(reconciliationBlock)
      }
      if (url.endsWith(`/release-delivery-reconciliation-cases/${reconciliationCase.id}`)) {
        return Response.json(reconciliationCase)
      }
      if (url.includes('/release-preview-runs?')) {
        return Response.json({ runs: null })
      }
      if (url.endsWith('/release-preview-runs')) {
        return Response.json({
          run: {
            id: '66666666-6666-4666-8666-666666666666', projectId,
            releaseBundle: bundle, reason: 'preview', state: 'queued', version: 1,
            createdBy: projectId, createdAt: '', updatedAt: '', receipt: null,
          },
          replayed: false,
        }, { status: 202 })
      }
      if (url.includes('/release-preview-receipts/')) {
        return Response.json(previewReceiptV2)
      }
      if (url.endsWith('/release-promotion-approvals')) {
        return Response.json({
          approval: promotionApproval,
          replayed: false,
        }, { status: 201 })
      }
      if (url.endsWith('/release-deployment-runs/promote') || url.endsWith('/release-deployment-runs/rollback')) {
        return Response.json({
          run: {
            id: '77777777-7777-4777-8777-777777777777', projectId,
            operation: url.endsWith('/rollback') ? 'rollback' : 'promote',
            releaseBundle: bundle, previewReceipt, promotionApproval: approval,
            ...(url.endsWith('/rollback') ? { sourceRevision } : {}),
            reason: 'delivery', state: 'queued', version: 1,
            createdBy: projectId, createdAt: '', updatedAt: '', receipt: null, revision: null,
          },
          replayed: false,
        }, { status: 202 })
      }
      if (url.includes('/release-production-receipts/')) {
        return Response.json(productionReceiptV2)
      }
      if (url.includes('/release-deployment-revisions/')) {
        return Response.json(deploymentRevisionV2)
      }
      throw new Error(`Unexpected URL ${url}`)
    }) as FetchLike,
  })
  const client = new ReleaseClient(http)

  const capabilities = await client.getCapabilities(projectId)
  assert.equal(capabilities.data.deliveryEnabled, false)

  // Immutable reconciliation evidence remains readable while delivery
  // mutations are disabled for controller maintenance.
  const reconciliationCases = await client.listDeliveryReconciliationCases(projectId)
  assert.deepEqual(reconciliationCases.data, [normalizedCase])
  const exactCase = await client.getDeliveryReconciliationCase(projectId, reconciliationCase.id)
  assert.equal(exactCase.data.caseHash, reconciliationCase.caseHash)
  const exactBlock = await client.getBlockedDeliveryReconciliation(
    projectId,
    'preview',
    reconciliationCase.runId,
  )
  assert.equal(exactBlock.data.lastError.code, reconciliationCase.quarantineError.code)

  const resumed = await client.resumeBlockedDeliveryReconciliation(projectId, {
    runKind: 'preview',
    runId: reconciliationCase.runId,
    expectedVersion: reconciliationCase.expectedRunVersion,
    expectedErrorCode: reconciliationCase.quarantineError.code,
    reason: 'Controller repair was independently verified.',
  })
  assert.equal(resumed.data.case.id, reconciliationCase.id)
  assert.deepEqual(JSON.parse(String(calls.at(-1)?.init?.body)), {
    runKind: 'preview',
    runId: reconciliationCase.runId,
    expectedVersion: reconciliationCase.expectedRunVersion,
    expectedErrorCode: reconciliationCase.quarantineError.code,
    reason: 'Controller repair was independently verified.',
  })
  const reconciliationIdempotencyKey = new Headers(calls.at(-1)?.init?.headers).get('idempotency-key')
  assert.ok(reconciliationIdempotencyKey)

  const preview = await client.startPreview(projectId, bundle, 'preview')
  assert.equal(preview.data.run.receipt, undefined)
  assert.deepEqual(JSON.parse(String(calls.at(-1)?.init?.body)), {
    releaseBundle: bundle,
    reason: 'preview',
  })
  const previewIdempotencyKey = new Headers(calls.at(-1)?.init?.headers).get('idempotency-key')
  assert.ok(previewIdempotencyKey)
  assert.notEqual(previewIdempotencyKey, reconciliationIdempotencyKey)

  const recovered = await client.listPreviewRuns(projectId, bundle)
  assert.deepEqual(recovered.data, [])

  const previewEvidence = await client.getPreviewReceipt(projectId, previewReceipt)
  assert.equal(previewEvidence.data.decision, 'passed')
  assert.equal(previewEvidence.data.schemaVersion, 'release-preview-receipt/v2')
  assert.deepEqual(previewEvidence.data.controllerOperation, previewControllerOperation)
  assert.deepEqual(previewEvidence.data.checks.map((check) => check.kind), ['contract', 'health', 'migration', 'e2e', 'smoke'])

  const approved = await client.approvePromotion(projectId, previewReceipt, 'approve')
  assert.equal(approved.data.approval.payloadHash, approval.contentHash)

  const promotion = await client.startPromotion(projectId, approval, 'promote')
  assert.equal(promotion.data.run.receipt, undefined)
  assert.deepEqual(JSON.parse(String(calls.at(-1)?.init?.body)), {
    promotionApproval: approval,
    reason: 'promote',
  })

  const failedEvidence = await client.getProductionReceipt(projectId, productionReceipt)
  assert.equal(failedEvidence.data.decision, 'failed')
  assert.equal(failedEvidence.data.schemaVersion, 'release-production-receipt/v2')
  assert.deepEqual(failedEvidence.data.controllerOperation, productionControllerOperation)
  assert.deepEqual(failedEvidence.data.checks, [{ id: 'health', kind: 'health', status: 'failed', detail: '503' }])
  assert.ok(calls.at(-1)?.url.includes(`receiptHash=${encodeURIComponent(productionReceipt.contentHash)}`))

  const exactRevision = await client.getDeploymentRevision(projectId, deploymentRevision)
  assert.equal(exactRevision.data.schemaVersion, 'release-deployment-revision/v2')
  assert.deepEqual(exactRevision.data.controllerOperation, productionControllerOperation)
  assert.deepEqual(exactRevision.data.releaseBundle, bundle)
  assert.deepEqual(exactRevision.data.previewReceipt, previewReceipt)
  assert.deepEqual(exactRevision.data.promotionApproval, approval)
  assert.deepEqual(exactRevision.data.productionReceipt, productionReceipt)
  assert.ok(calls.at(-1)?.url.includes(`revisionHash=${encodeURIComponent(deploymentRevision.contentHash)}`))

  const rollback = await client.startRollback(projectId, sourceRevision, 'rollback')
  assert.equal(rollback.data.run.operation, 'rollback')
  assert.deepEqual(rollback.data.run.sourceRevision, sourceRevision)
  assert.deepEqual(JSON.parse(String(calls.at(-1)?.init?.body)), {
    sourceRevision,
    reason: 'rollback',
  })

  await assert.rejects(
    releaseClientReturning({
      ...previewReceiptV2,
      controllerOperation: undefined,
    }).getPreviewReceipt(projectId, previewReceipt),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      ...previewReceiptV1,
      controllerOperation: previewControllerOperation,
    }).getPreviewReceipt(projectId, previewReceipt),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      ...productionReceiptV2,
      controllerOperation: {
        ...productionControllerOperation,
        resultHash: 'not-a-hash',
      },
    }).getProductionReceipt(projectId, productionReceipt),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      ...deploymentRevisionV2,
      productionReceipt: { ...productionReceipt, unexpected: true },
    }).getDeploymentRevision(projectId, deploymentRevision),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      ...deploymentRevisionV2,
      controllerOperation: null,
    }).getDeploymentRevision(projectId, deploymentRevision),
    PlatformProtocolError,
  )
  await assert.rejects(
    releaseClientReturning({
      ...deploymentRevisionV2,
      id: '34343434-3434-4434-8434-343434343434',
    }).getDeploymentRevision(projectId, deploymentRevision),
    PlatformProtocolError,
  )

  const malformedClient = new ReleaseClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async () => Response.json({ cases: [{ ...reconciliationCase, caseHash: 'not-a-hash' }] })) as FetchLike,
  }))
  await assert.rejects(
    malformedClient.listDeliveryReconciliationCases(projectId),
    PlatformProtocolError,
  )

  console.log('Release platform client tests passed.')
}

void main()
