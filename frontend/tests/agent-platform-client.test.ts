import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { PlatformClient } from '../lib/platform/client'
import { PlatformHttpError, PlatformProtocolError, type FetchLike } from '../lib/platform/http'

const digestA = `sha256:${'a'.repeat(64)}`
const digestB = `sha256:${'b'.repeat(64)}`
const digestC = `sha256:${'c'.repeat(64)}`
const attemptId = 'attempt/one'
const proposedFileBytes = new TextEncoder().encode('new')
const proposedFileHash = `sha256:${createHash('sha256').update(proposedFileBytes).digest('hex')}`

function rawHash(value: string | Uint8Array) {
  return `sha256:${createHash('sha256').update(value).digest('hex')}`
}

const attempt = {
  schemaVersion: 'agent-attempt/v1',
  id: attemptId,
  projectId: 'project-1',
  sandboxSessionId: 'session/one',
  candidateId: 'candidate-1',
  taskCapsule: { id: 'capsule-1', contentHash: digestC },
  contextPack: { id: 'context-1', contentHash: digestB },
  baseCandidateTreeHash: digestA,
  buildContractHash: digestB,
  templateReleaseHashes: [],
  executor: {
    adapter: 'codex', provider: 'openai', model: 'qualified-model',
    runnerImageDigest: digestA, modelPolicyHash: digestA,
    parametersHash: digestA, promptHash: digestA,
    outputSchemaHash: digestA, toolchainHash: digestA,
  },
  requestKeyHash: digestA,
  configurationHash: digestB,
  state: 'review_ready',
  version: 7,
  fenceEpoch: 4,
  evidence: {
    patch: { store: 'agent', ownerId: attemptId, ref: 'patch.json', contentHash: digestA, byteSize: 123 },
  },
  createdBy: 'actor-1',
  createdAt: '2026-07-17T00:00:00Z',
  updatedAt: '2026-07-17T00:01:00Z',
}

const taskResult = {
  contextPack: {
    schemaVersion: 'agent-context-pack/v1', id: 'context-1', projectId: 'project-1',
    candidateId: 'candidate-1', baseCandidateTreeHash: digestA,
    buildContract: { id: 'contract-1', contentHash: digestB }, items: [],
    contentHash: digestB, createdBy: 'actor-1', createdAt: '2026-07-17T00:00:00Z',
  },
  taskCapsule: {
    schemaVersion: 'agent-task-capsule/v1', taskId: 'capsule-1', taskKey: 'implement-page',
    projectId: 'project-1', sandboxSessionId: 'session/one', candidateId: 'candidate-1',
    candidateVersion: 8, candidateSessionEpoch: 2, candidateWriterLeaseEpoch: 4,
    baseCandidateTreeHash: digestA, objective: 'Implement the approved page.',
    buildContract: { id: 'contract-1', contentHash: digestB },
    templateReleases: [], contextPack: { id: 'context-1', contentHash: digestB },
    obligationIds: [], acceptanceCriterionIds: [], readSet: [], writeSet: [],
    protectedPaths: [], preconditions: [], postconditions: [],
    verificationCommandIds: [], allowedTools: [],
    networkPolicy: { mode: 'deny_all', allowedHosts: [] },
    budgets: {
      wallTimeSeconds: 300, maxInputTokens: 1000, maxOutputTokens: 1000,
      maxCommands: 20, maxLogBytes: 1048576, maxPatchBytes: 1048576,
    },
    outputSchemaHash: digestA,
    contentHash: digestC, createdAt: '2026-07-17T00:00:00Z',
    createdBy: 'actor-1',
  },
  attempt,
  replayed: false,
}

const mergePlan = {
  schemaVersion: 'agent-patch-merge-plan/v1', id: 'merge-1', operationId: 'merge-operation',
  projectId: 'project-1', sandboxSessionId: 'session/one', candidateId: 'candidate-1',
  attemptId, attemptVersion: 7, baseTreeHash: digestA, currentTreeHash: digestB,
  proposedTreeHash: digestC, plannedTreeHash: digestC, disposition: 'conflicted',
  patchReference: { store: 'agent', ownerId: attemptId, ref: 'patch.json', contentHash: digestA, byteSize: 123 },
  patchRawHash: digestA, patchContentHash: digestC,
  expectedSessionVersion: 3, expectedSessionEpoch: 2, expectedCandidateVersion: 8,
  expectedCandidateJournalSequence: 9, expectedWriterLeaseEpoch: 4,
  operations: [],
  conflicts: [{
    path: 'src/app.ts', reason: 'both_changed',
    base: { exists: true, contentHash: digestA, byteSize: 1, mode: '100644' },
    current: { exists: true, contentHash: digestB, byteSize: 2, mode: '100644' },
    proposed: { exists: true, contentHash: digestC, byteSize: 3, mode: '100644' },
  }],
  contentHash: digestC, createdBy: 'actor-1', createdAt: '2026-07-17T00:02:00Z',
}

const patchEvidence = {
  schemaVersion: 'agent-platform-patch/v1', attemptId, projectId: 'project-1',
  candidateId: 'candidate-1', taskCapsule: { id: 'capsule-1', contentHash: digestC },
  configurationHash: digestB, baseTreeHash: digestA, proposedTreeHash: digestC,
  operations: [{
    id: 'operation-1', kind: 'file.upsert', path: 'src/app.ts',
    contentHash: proposedFileHash, byteSize: 3, mode: '100644',
  }],
  changedBytes: 3, contentHash: digestC,
}

const structuredEvidence = {
  summary: 'Implemented the requested file.',
  changedPaths: ['src/app.ts'],
  verification: [{ commandId: 'typecheck', status: 'passed', note: 'Passed.' }],
  blockers: [],
}

const validationEvidence = {
  schemaVersion: 'agent-patch-validation/v1', scope: 'candidate', attemptId,
  projectId: 'project-1', taskCapsule: { id: 'capsule-1', contentHash: digestC },
  patch: { store: 'agent', ownerId: attemptId, ref: 'patch.json', contentHash: digestA, byteSize: 123 },
  patchContentHash: digestC, baseTreeHash: digestA, proposedTreeHash: digestC,
  checks: [{ id: 'typecheck', status: 'passed', detail: 'Passed.' }],
  decision: 'passed', independentQualityRequired: true, contentHash: digestB,
}

type Call = {
  readonly method: string
  readonly path: string
  readonly headers: Headers
  readonly body?: unknown
}

function json(value: unknown, status = 200, headers?: HeadersInit) {
  return Response.json(value, { status, headers })
}

function evidenceResponse(
  kind: 'patch' | 'structured_result' | 'stdout' | 'stderr' | 'validation',
  value: unknown,
  options: {
    readonly encoded?: string | Uint8Array
    readonly mediaType?: string
    readonly headers?: HeadersInit
  } = {},
) {
  const encoded = options.encoded ?? JSON.stringify(value)
  const objectHash = digestB
  const headers = new Headers({
    'content-type': options.mediaType ?? 'application/json',
    etag: `"agent-evidence:${attemptId}:${kind}:${objectHash}"`,
    'x-content-hash': rawHash(encoded),
    'x-content-object-hash': objectHash,
  })
  new Headers(options.headers).forEach((entry, name) => headers.set(name, entry))
  return new Response(encoded, { status: 200, headers })
}

async function main() {
  const calls: Call[] = []
  const fetch: FetchLike = async (input, init) => {
    const url = new URL(input.toString())
    const method = init?.method ?? 'GET'
    calls.push({
      method,
      path: `${url.pathname}${url.search}`,
      headers: new Headers(init?.headers),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : init?.body,
    })
    if (url.pathname.endsWith('/evidence/patch')) {
      return evidenceResponse('patch', patchEvidence)
    }
    if (url.pathname.endsWith('/evidence/structured_result')) {
      return evidenceResponse('structured_result', structuredEvidence)
    }
    if (url.pathname.endsWith('/evidence/validation')) {
      return evidenceResponse('validation', validationEvidence)
    }
    if (url.pathname.endsWith('/patch-file')) {
      if (url.searchParams.get('side') === 'base') {
        return new Response(null, {
          status: 204,
          headers: {
            etag: `"agent-patch-file:${attemptId}:base:${digestA}"`,
            'x-file-exists': 'false', 'x-byte-size': '0',
            'x-patch-content-hash': digestC,
          },
        })
      }
      return new Response(proposedFileBytes, {
        status: 200,
        headers: {
          etag: `"agent-patch-file:${attemptId}:proposed:${digestA}"`,
          'x-file-exists': 'true',
          'x-content-hash': proposedFileHash,
          'x-file-mode': '100644',
          'x-byte-size': '3',
          'x-patch-content-hash': digestC,
        },
      })
    }
    if (url.pathname.endsWith('/evidence/stdout')) {
      return evidenceResponse('stdout', undefined, {
        encoded: '{"message":"done"}\n', mediaType: 'application/x-ndjson',
      })
    }
    if (url.pathname.endsWith('/merges')) {
      return json({
        merges: [{
          plan: { ...mergePlan, disposition: 'planned', conflicts: [], operations: [{
            id: 'operation-1', kind: 'file.upsert', path: 'src/app.ts',
            expectedHash: digestA, contentHash: digestC, byteSize: 3, mode: '100644',
          }] },
          application: {
            schemaVersion: 'agent-patch-merge-application/v1', contentHash: digestA,
            mergeId: 'merge-1', planContentHash: digestC, projectId: 'project-1',
            candidateId: 'candidate-1',
            journalSequenceFrom: 9, journalSequenceTo: 9,
            candidateVersionFrom: 8, candidateVersionTo: 9,
            beforeTree: {
              store: 'content', ref: 'tree-before', ownerId: 'candidate-1', treeHash: digestB,
              fileCount: 1, byteSize: 3, contentObjectHash: digestA,
            },
            afterTree: {
              store: 'content', ref: 'tree-after', ownerId: 'candidate-1', treeHash: digestC,
              fileCount: 1, byteSize: 3, contentObjectHash: digestB,
            },
            appliedBy: 'actor-1',
            appliedAt: '2026-07-17T00:03:00Z',
          },
        }],
      })
    }
    if (url.pathname.endsWith('/merge')) {
      return json({ plan: mergePlan, replayed: false }, 409, {
        'content-type': 'application/json',
        etag: `"agent-merge:merge-1:${digestC}"`,
      })
    }
    if (url.pathname.endsWith(':cancel')) return json({ ...attempt, state: 'cancelled', version: 8 })
    if (url.pathname.endsWith(':retry')) return json({
      ...taskResult,
      attempt: {
        ...attempt, id: 'attempt-2', parentAttemptId: attemptId,
        retryReason: 'Retry after correcting constraints.',
      },
    }, 201)
    if (url.pathname.includes('/events')) return json({ events: [], afterSequence: 0, lastSequence: 0 })
    if (url.pathname.endsWith('/agent-attempts') && method === 'GET') return json({ attempts: [] })
    return json(taskResult, method === 'POST' ? 201 : 200, {
      etag: `"agent-attempt:${attemptId}:7"`,
    })
  }

  const client = new PlatformClient({
    http: { baseUrl: 'https://platform.example.test', fetch },
  })
  const created = await client.agent.createAttempt('session/one', {
    taskKey: 'implement-page', instruction: 'Implement the approved page.',
    executorProfile: 'codex-qualified',
  }, { idempotencyKey: 'create-attempt-1' })
  assert.equal(created.data.attempt.state, 'review_ready')
  assert.deepEqual(created.data.attempt.templateReleaseHashes, [])
  assert.deepEqual(created.data.taskCapsule.acceptanceCriterionIds, [])
  assert.equal(created.data.contextPack.itemCount, 0)

  assert.deepEqual((await client.agent.listAttempts('session/one')).data, [])
  assert.deepEqual((await client.agent.listEvents(attemptId)).data.events, [])
  const history = (await client.agent.listMerges(attemptId)).data
  assert.equal(history[0]?.plan.id, 'merge-1')
  assert.equal(history[0]?.application?.afterTreeHash, digestC)
  assert.equal((await client.agent.readPatch(attemptId)).data.operations[0]?.path, 'src/app.ts')
  const proposedFile = await client.agent.readPatchFile(attemptId, 'src/app.ts', 'proposed')
  assert.equal(new TextDecoder().decode(proposedFile.data.value), 'new')
  assert.equal(proposedFile.data.contentHash, proposedFileHash)
  assert.equal(proposedFile.data.patchContentHash, digestC)
  assert.equal(proposedFile.data.mode, '100644')
  const absentBase = await client.agent.readPatchFile(attemptId, 'src/app.ts', 'base')
  assert.equal(absentBase.data.exists, false)
  assert.equal(absentBase.data.value.byteLength, 0)
  assert.equal((await client.agent.readStructuredResult(attemptId)).data.summary, structuredEvidence.summary)
  assert.equal((await client.agent.readValidation(attemptId)).data.attemptId, attemptId)
  assert.equal((await client.agent.readStdout(attemptId)).data, '{"message":"done"}\n')

  const merge = await client.agent.mergePatch(attemptId, {
    expectedSessionVersion: 3,
    expectedSessionEpoch: 2,
    expectedCandidateVersion: 8,
    expectedWriterLeaseEpoch: 4,
  }, { ifMatch: `"agent-attempt:${attemptId}:7"`, idempotencyKey: 'merge-1' })
  assert.equal(merge.status, 409)
  assert.equal(merge.data.plan.disposition, 'conflicted')
  assert.equal(merge.data.plan.conflicts[0]?.path, 'src/app.ts')

  await client.agent.cancelAttempt(attemptId, 'User cancelled execution.', {
    ifMatch: `"agent-attempt:${attemptId}:7"`, idempotencyKey: 'cancel-1',
  })
  await client.agent.retryAttempt(attemptId, 'Retry after correcting constraints.', {
    ifMatch: `"agent-attempt:${attemptId}:7"`, idempotencyKey: 'retry-1',
  })

  assert.equal(calls[0]?.path, '/v1/sandbox-sessions/session%2Fone/agent-attempts')
  assert.equal(calls[0]?.headers.get('idempotency-key'), 'create-attempt-1')
  assert.deepEqual(calls[0]?.body, {
    taskKey: 'implement-page', instruction: 'Implement the approved page.',
    executorProfile: 'codex-qualified',
  })
  const mergeCall = calls.find((call) => call.path.endsWith('/merge'))
  assert.equal(mergeCall?.headers.get('if-match'), `"agent-attempt:${attemptId}:7"`)
  assert.equal(mergeCall?.headers.get('idempotency-key'), 'merge-1')
  assert.deepEqual(mergeCall?.body, {
    expectedSessionVersion: 3,
    expectedSessionEpoch: 2,
    expectedCandidateVersion: 8,
    expectedWriterLeaseEpoch: 4,
  })
  assert.ok(calls.some((call) => call.path === `/v1/agent-attempts/${encodeURIComponent(attemptId)}/patch-file?path=src%2Fapp.ts&side=proposed`))

  const problemClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => json({
        title: 'Merge fenced', status: 409, code: 'agent_patch_merge_fenced',
      }, 409, { 'content-type': 'application/problem+json' })) as FetchLike,
    },
  })
  await assert.rejects(
    problemClient.agent.mergePatch(attemptId, {
      expectedSessionVersion: 3,
      expectedSessionEpoch: 2,
      expectedCandidateVersion: 8,
      expectedWriterLeaseEpoch: 4,
    }, { ifMatch: `"agent-attempt:${attemptId}:7"` }),
    PlatformHttpError,
  )

  const malformedPatchFileClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => new Response(new Uint8Array([1]), {
        headers: { 'x-byte-size': '1', 'x-content-hash': digestA, 'x-file-mode': '100644' },
      })) as FetchLike,
    },
  })
  await assert.rejects(
    malformedPatchFileClient.agent.readPatchFile(attemptId, 'src/app.ts', 'proposed'),
    PlatformProtocolError,
  )

  const mismatchedPatchFileClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => new Response(new TextEncoder().encode('old'), {
        headers: {
          etag: `"agent-patch-file:${attemptId}:proposed:${digestA}"`,
          'x-file-exists': 'true', 'x-byte-size': '3',
          'x-content-hash': proposedFileHash, 'x-file-mode': '100644',
          'x-patch-content-hash': digestC,
        },
      })) as FetchLike,
    },
  })
  await assert.rejects(
    mismatchedPatchFileClient.agent.readPatchFile(attemptId, 'src/app.ts', 'proposed'),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('bytes do not match the declared X-Content-Hash'),
  )

  const evidenceMismatchClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => evidenceResponse('patch', patchEvidence, {
        headers: { 'x-content-hash': digestA },
      })) as FetchLike,
    },
  })
  await assert.rejects(
    evidenceMismatchClient.agent.readPatch(attemptId),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('bytes do not match the declared X-Content-Hash'),
  )

  const malformedEvidenceHashClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => evidenceResponse('patch', patchEvidence, {
        headers: { 'x-content-hash': 'sha256:not-a-digest' },
      })) as FetchLike,
    },
  })
  await assert.rejects(
    malformedEvidenceHashClient.agent.readPatch(attemptId),
    PlatformProtocolError,
  )

  const missingObjectHashClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => {
        const response = evidenceResponse('patch', patchEvidence)
        const headers = new Headers(response.headers)
        headers.delete('x-content-object-hash')
        return new Response(await response.arrayBuffer(), { headers })
      }) as FetchLike,
    },
  })
  await assert.rejects(missingObjectHashClient.agent.readPatch(attemptId), PlatformProtocolError)

  const duplicateJSON = JSON.stringify(patchEvidence).replace(
    '"attemptId":"attempt/one"',
    '"attemptId":"attempt/one","attemptId":"attempt/one"',
  )
  const duplicateEvidenceClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => evidenceResponse('patch', undefined, { encoded: duplicateJSON })) as FetchLike,
    },
  })
  await assert.rejects(
    duplicateEvidenceClient.agent.readPatch(attemptId),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('strict UTF-8 JSON'),
  )

  const invalidUtf8 = new Uint8Array([0x7b, 0x22, 0x78, 0x22, 0x3a, 0xc3, 0x28, 0x7d])
  const invalidUtf8Client = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => evidenceResponse('patch', undefined, { encoded: invalidUtf8 })) as FetchLike,
    },
  })
  await assert.rejects(invalidUtf8Client.agent.readPatch(attemptId), PlatformProtocolError)

  const unknownStructuredClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => evidenceResponse('structured_result', {
        ...structuredEvidence, unexpected: null,
      })) as FetchLike,
    },
  })
  await assert.rejects(
    unknownStructuredClient.agent.readStructuredResult(attemptId),
    PlatformProtocolError,
  )

  const nullStructuredClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: (async () => evidenceResponse('structured_result', {
        ...structuredEvidence, summary: null,
      })) as FetchLike,
    },
  })
  await assert.rejects(
    nullStructuredClient.agent.readStructuredResult(attemptId),
    PlatformProtocolError,
  )

  console.log('Agent platform client tests passed.')
}

void main()
