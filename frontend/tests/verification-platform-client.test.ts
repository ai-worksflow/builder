import assert from 'node:assert/strict'
import { PlatformClient } from '../lib/platform/client'
import type { FetchLike } from '../lib/platform/http'

const digest = `sha256:${'a'.repeat(64)}`
const sessionId = 'session/one'
const runId = 'run/one'
const receiptId = 'receipt/one'
const profile = {
  id: 'react-fastapi-postgres-v1',
  version: 1,
  contentHash: digest,
}

const runView = {
  run: {
    schemaVersion: 'candidate-verification-run/v1',
    id: runId,
    projectId: 'project-1',
    plan: { id: 'plan-1', contentHash: digest },
    requestKey: 'verify-1',
    requestHash: digest,
    reason: 'Verify exact Candidate checkpoint',
    parentRunId: null,
    retryReason: null,
    state: 'queued',
    version: 1,
    fenceEpoch: 0,
    terminalReason: null,
    executionError: null,
    startedAt: null,
    finishedAt: null,
    createdBy: 'actor-1',
    updatedBy: 'actor-1',
    createdAt: '2026-07-17T00:00:00Z',
    updatedAt: '2026-07-17T00:00:00Z',
    replayed: null,
  },
  subject: {
    sessionId,
    sessionVersion: 3,
    candidateId: 'candidate-1',
    candidateSnapshotId: 'checkpoint-1',
    candidateVersion: 7,
    journalSequence: 4,
    sessionEpoch: 2,
    writerLeaseEpoch: 3,
    treeStore: 'blob',
    treeOwnerId: 'owner-1',
    treeRef: 'candidate/tree',
    treeContentHash: digest,
    treeHash: digest,
  },
  buildManifest: { id: 'manifest-1', contentHash: digest },
  buildContract: { id: 'contract-1', contentHash: digest },
  fullStackTemplate: { id: 'template-1', contentHash: digest },
  verificationProfile: profile,
  receipt: null,
  receiptDecision: null,
  checkCount: 2,
  requiredCheckCount: 1,
  completedCheckCount: 0,
  attemptCount: 0,
  latestAttempt: null,
  mustCount: 0,
  mustPassedCount: 0,
  blockerCount: 0,
  warningCount: 0,
  stale: null,
  allowedActions: ['cancel'],
  blockingReasons: null,
}

type Call = {
  readonly method: string
  readonly path: string
  readonly headers: Headers
  readonly body?: unknown
}

async function main() {
  const calls: Call[] = []
  const fetch: FetchLike = async (input, init) => {
    const url = new URL(input.toString())
    const method = init?.method ?? 'GET'
    calls.push({
      method,
      path: url.pathname,
      headers: new Headers(init?.headers),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    })
    if (url.pathname.endsWith('/verification-profiles')) {
      return Response.json({
        profiles: [{
          verificationProfile: profile,
          supportedTemplateRoles: null,
        }],
      })
    }
    if (url.pathname.endsWith('/verification-runs') && method === 'GET') {
      return Response.json({ runs: [runView] })
    }
    if (url.pathname.endsWith('/verification-runs') && method === 'POST') {
      return Response.json(runView, { status: 201, headers: {
        etag: `"candidate-verification-run:${runId}:1:0"`,
      } })
    }

    if (url.pathname.endsWith('/checks')) {
      return Response.json({
        runId,
        receipt: { id: receiptId, contentHash: digest },
        offset: 0,
        limit: 10,
        totalCount: 1,
        checks: [{
          id: 'typecheck',
          kind: 'command',
          required: true,
          status: 'failed',
          attemptId: 'attempt-1',
          verifierImageDigest: `registry.example/quality@${digest}`,
          argv: null,
          workingDirectory: '.',
          exitCode: 1,
          startedAt: '2026-07-17T00:00:00Z',
          completedAt: '2026-07-17T00:00:01Z',
          durationMs: 1000,
          attemptCount: 1,
          diagnostics: null,
        }],
      })
    }
    if (url.pathname.endsWith(':cancel')) {
      return Response.json({
        ...runView,
        run: { ...runView.run, state: 'cancelled', version: 2 },
        allowedActions: ['retry'],
      })
    }
    if (url.pathname.endsWith(':retry')) {
      return Response.json({
        ...runView,
        run: { ...runView.run, id: 'run-2', parentRunId: runId },
      }, { status: 201 })
    }
    if (url.pathname.includes('/verification-receipts/')) {
      return Response.json({
        schemaVersion: 'verification-receipt/v1',
        id: receiptId,
        runId,
        scope: 'candidate',
        projectId: 'project-1',
        subject: {
          sessionId,
          candidateId: 'candidate-1',
          candidateSnapshotId: 'checkpoint-1',
          candidateVersion: 7,
          journalSequence: 4,
          sessionEpoch: 2,
          writerLeaseEpoch: 3,
          treeHash: digest,
        },
        buildManifest: { id: 'manifest-1', contentHash: digest },
        buildContract: { id: 'contract-1', contentHash: digest },
        fullStackTemplate: { id: 'template-1', contentHash: digest },
        verificationProfile: profile,
        plan: { id: 'plan-1', contentHash: digest },
        attemptIds: null,
        checks: [{
          id: 'typecheck',
          kind: 'command',
          required: true,
          status: 'passed',
          attemptId: 'attempt-1',
          verifierImageDigest: `registry.example/quality@${digest}`,
          argv: null,
          workingDirectory: '.',
          exitCode: 0,
          startedAt: '2026-07-17T00:00:00Z',
          completedAt: '2026-07-17T00:00:01Z',
          durationMs: 1000,
          attemptCount: 1,
          stdout: null,
          stderr: null,
          truncated: null,
          redactionCount: 0,
          oracleIds: null,
          acceptanceCriterionIds: null,
          obligationIds: null,
          diagnostics: null,
        }],
        obligationCoverage: null,
        mustCount: 1,
        mustPassedCount: 1,
        blockerCount: 0,
        warningCount: 0,
        decision: 'passed',
        executionError: null,
        payloadHash: digest,
        createdBy: 'actor-1',
        createdAt: '2026-07-17T00:00:02Z',
      })
    }
    return Response.json(runView)
  }

  const client = new PlatformClient({
    http: { baseUrl: 'https://platform.example.test', fetch },
  })

  const profiles = await client.verification.listProfiles(sessionId)
  assert.equal(profiles.data.profiles[0]?.verificationProfile.contentHash, digest)
  assert.deepEqual(profiles.data.profiles[0]?.supportedTemplateRoles, [])

  const history = await client.verification.listRuns(sessionId, 10)
  assert.equal(history.data.runs[0]?.run.id, runId)
  assert.deepEqual(history.data.runs[0]?.blockingReasons, [])

  const input = {
    candidateId: 'candidate-1',
    checkpointId: 'checkpoint-1',
    expectedSessionVersion: 3,
    expectedSessionEpoch: 2,
    expectedCandidateVersion: 7,
    expectedWriterLeaseEpoch: 3,
    verificationProfile: profile,
    reason: 'Verify exact Candidate checkpoint',
  }
  const created = await client.verification.createRun(
    sessionId,
    input,
    { idempotencyKey: 'verify-1' },
  )
  assert.equal(created.data.run.state, 'queued')
  assert.deepEqual(created.data.blockingReasons, [])
  assert.equal(created.data.latestAttempt, undefined)
  assert.equal(created.data.stale, false)


  const checks = await client.verification.listChecks(runId, 0, 10)
  assert.equal(checks.data.totalCount, 1)
  assert.equal(checks.data.checks[0]?.status, 'failed')
  assert.deepEqual(checks.data.checks[0]?.argv, [])
  assert.deepEqual(checks.data.checks[0]?.diagnostics, [])
  const cancelled = await client.verification.cancelRun(runId, {
    expectedVersion: 1,
    expectedFenceEpoch: 0,
    reason: 'User cancelled verification.',
  }, { idempotencyKey: 'cancel-1' })
  assert.equal(cancelled.data.run.state, 'cancelled')
  assert.deepEqual(cancelled.data.allowedActions, ['retry'])

  const retried = await client.verification.retryRun(
    runId,
    'Retry after fixing exact source.',
    { idempotencyKey: 'retry-1' },
  )
  assert.equal(retried.data.run.parentRunId, runId)

  const receipt = await client.verification.getReceipt(receiptId)
  assert.equal(receipt.data.decision, 'passed')
  assert.deepEqual(receipt.data.attemptIds, [])
  assert.deepEqual(receipt.data.obligationCoverage, [])
  assert.deepEqual(receipt.data.checks[0]?.argv, [])
  assert.deepEqual(receipt.data.checks[0]?.diagnostics, [])

  assert.equal(calls[0]?.path, '/v1/sandbox-sessions/session%2Fone/verification-profiles')
  const createCall = calls.find((call) => call.path.endsWith('/verification-runs') && call.method === 'POST')
  assert.equal(createCall?.headers.get('idempotency-key'), 'verify-1')
  assert.deepEqual(createCall?.body, input)
  const cancelCall = calls.find((call) => call.path.endsWith(':cancel'))
  assert.equal(cancelCall?.headers.get('idempotency-key'), 'cancel-1')
  assert.deepEqual(cancelCall?.body, {
    expectedVersion: 1,
    expectedFenceEpoch: 0,
    reason: 'User cancelled verification.',
  })
  const retryCall = calls.find((call) => call.path.endsWith(':retry'))
  assert.equal(retryCall?.headers.get('idempotency-key'), 'retry-1')
  assert.deepEqual(retryCall?.body, { reason: 'Retry after fixing exact source.' })

  console.log('Verification platform client tests passed.')
}

void main()
