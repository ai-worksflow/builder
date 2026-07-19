import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { PlatformClient } from '../lib/platform/client'
import { PlatformProtocolError, type CsrfTokenStore, type FetchLike } from '../lib/platform/http'
import {
  candidateFileReadCommitDecision,
  createExactCandidateFileOpenFence,
  openFileHeadRefreshDisposition,
} from '../lib/platform/sandbox-file-open'

const sessionId = '11111111-1111-4111-8111-111111111111'
const etag = `"sandbox:${sessionId}:3"`
const processId = 'process/one'
const processEtag = '"sandbox-process:process/one:2"'
const terminalId = 'terminal/one'
const fileBytes = new Uint8Array([0, 1, 2, 255])
const contentHash = `sha256:${createHash('sha256').update(fileBytes).digest('hex')}`
const treeHash = `sha256:${'b'.repeat(64)}`

function tokenStore(): CsrfTokenStore {
  return { get: () => 'csrf-sandbox', set: () => undefined, clear: () => undefined }
}

const session = {
  schemaVersion: 'sandbox-session/v1',
  id: sessionId,
  projectId: 'project-1',
  actorId: 'actor-1',
  buildManifest: { id: 'manifest-1', contentHash },
  buildContract: { id: 'contract-1', contentHash },
  fullStackTemplate: { id: 'template-1', contentHash },
  state: 'ready',
  version: 3,
  sessionEpoch: 2,
  candidate: {
    id: 'candidate-1',
    repositorySnapshotId: 'snapshot-1',
    status: 'active',
    baseTreeHash: treeHash,
    treeHash,
    version: 7,
    journalSequence: 5,
    sessionEpoch: 2,
    writerLeaseEpoch: 4,
    dirty: false,
    conflicted: false,
    stale: false,
    rebaseRequired: false,
    updatedAt: '2026-07-16T08:00:00Z',
  },
  templateReleases: [],
  runnerImageDigest: contentHash,
  ttl: {
    policy: { idleHibernateAfter: 1_800_000_000_000, maxRuntime: 14_400_000_000_000 },
    idleDeadline: '2026-07-16T08:30:00Z', expiresAt: '2026-07-16T12:00:00Z',
  },
  quota: {
    cpuMillis: 2_000, memoryBytes: 536_870_912, workspaceBytes: 1_073_741_824,
    pidLimit: 512, previewPortLimit: 4,
  },
  allowedServices: [],
  allowedPorts: [],
  allowedActions: [],
  blockingReasons: [],
  lastTransition: { to: 'ready', reason: 'Sandbox runtime is ready.', at: '2026-07-16T08:00:00Z' },
  createdAt: '2026-07-16T07:59:00Z',
  updatedAt: '2026-07-16T08:00:00Z',
}

const { treeHash: _candidateProjectionTreeHash, ...candidateState } = session.candidate
const candidate = {
  ...candidateState,
  schemaVersion: 'candidate-workspace/v1',
  projectId: 'project-1',
  buildManifest: session.buildManifest,
  buildContract: session.buildContract,
  fullStackTemplate: session.fullStackTemplate,
  currentTree: {
    schemaVersion: 'repository-tree/v1',
    treeHash,
    files: [
      { path: 'src/app.ts', mode: '100644', contentHash, byteSize: 4 },
      { path: 'src/my file.ts', mode: '100755', contentHash, byteSize: 4 },
    ],
  },
  createdBy: 'actor-1',
  createdAt: '2026-07-16T07:59:00Z',
}

const process = {
  schemaVersion: 'sandbox-process/v1', id: processId, projectId: 'project-1', sessionId,
  sessionEpoch: 2, sessionVersionAtCreation: 3, actorId: 'actor-1',
  serviceId: 'web-ui', commandId: 'dev', templateRelease: { id: 'release-1', contentHash },
  workingDirectory: 'apps/web', argv: ['node', 'server.js'], logLimitBytes: 1048576,
  state: 'running', version: 2, pid: 42,
  logBytes: 0, logTruncated: false, runtimeStartedAt: '2026-07-16T08:00:00Z',
  createdAt: '2026-07-16T07:59:59Z', updatedAt: '2026-07-16T08:00:00Z',
}

const terminal = {
  schemaVersion: 'sandbox-terminal/v1', id: terminalId, projectId: 'project-1', sessionId,
  sessionEpoch: 2, sessionVersionAtCreation: 3, actorId: 'actor-1',
  workingDirectory: '.', shellPath: '/bin/bash', rows: 24, columns: 80,
  outputLimitBytes: 1048576, state: 'running', version: 2,
  outputBytes: 0, outputTruncated: false,
  runtimeStartedAt: '2026-07-16T08:00:00Z',
  createdAt: '2026-07-16T07:59:59Z', updatedAt: '2026-07-16T08:00:00Z',
}

type Call = {
  readonly method: string
  readonly path: string
  readonly headers: Headers
  readonly body?: BodyInit | null
}

function testExactCandidateFileOpenFencing() {
  const fileA = { path: 'src/a.ts', mode: '100644', contentHash, byteSize: 4 }
  const fileB = {
    path: 'src/b.ts', mode: '100644', contentHash: `sha256:${'c'.repeat(64)}`, byteSize: 5,
  }
  const exactSession = {
    id: 'session-1', projectId: 'project-1', version: 3, sessionEpoch: 2,
    candidate: {
      id: 'candidate-1', version: 7, journalSequence: 6, sessionEpoch: 2,
      writerLeaseEpoch: 4, treeHash,
    },
  }
  const exactCandidate = {
    ...exactSession.candidate,
    projectId: 'project-1',
    currentTree: { treeHash, files: [fileA, fileB] },
  }
  const exactFences = {
    etag, sessionEpoch: 2, candidateVersion: 7, writerLeaseEpoch: 4, treeHash,
  }
  const fenceA = createExactCandidateFileOpenFence({
    projectId: 'project-1', session: exactSession, candidate: exactCandidate,
    fences: exactFences, path: fileA.path, observedFile: fileA,
  })
  const fenceB = createExactCandidateFileOpenFence({
    projectId: 'project-1', session: exactSession, candidate: exactCandidate,
    fences: exactFences, path: fileB.path, observedFile: fileB,
  })
  assert.ok(fenceA)
  assert.ok(fenceB)
  const evidenceA = {
    sessionEpoch: 2, candidateId: 'candidate-1', candidateVersion: 7,
    journalSequence: 6, writerLeaseEpoch: 4, treeHash, contentHash: fileA.contentHash,
  }
  const evidenceB = { ...evidenceA, contentHash: fileB.contentHash }

  assert.equal(candidateFileReadCommitDecision({
    requestGeneration: 1, currentGeneration: 2, requestFence: fenceA,
    currentFence: fenceA, evidence: evidenceA,
  }), 'superseded', 'an A response cannot commit after B became the current open generation')
  assert.equal(candidateFileReadCommitDecision({
    requestGeneration: 2, currentGeneration: 2, requestFence: fenceB,
    currentFence: fenceB, evidence: evidenceB,
  }), 'commit', 'the latest B response can commit only against its exact fence')
  assert.equal(candidateFileReadCommitDecision({
    requestGeneration: 2, currentGeneration: 2, requestFence: fenceB,
    currentFence: { ...fenceB, candidateVersion: 8 }, evidence: evidenceB,
  }), 'head_changed', 'a changed Candidate head rejects an otherwise valid response')
  assert.equal(candidateFileReadCommitDecision({
    requestGeneration: 2, currentGeneration: 2, requestFence: fenceB,
    currentFence: fenceB, evidence: { ...evidenceB, sessionEpoch: 3 },
  }), 'response_mismatch', 'a response from a different session epoch is rejected')
  assert.equal(candidateFileReadCommitDecision({
    requestGeneration: 2, currentGeneration: 2, requestFence: fenceB,
    currentFence: fenceB, evidence: { ...evidenceB, contentHash: fileA.contentHash },
  }), 'response_mismatch', 'a response with the wrong tree-file content hash is rejected')
  assert.equal(createExactCandidateFileOpenFence({
    projectId: 'project-1', session: exactSession, candidate: exactCandidate,
    fences: { ...exactFences, sessionEpoch: 9 }, path: fileA.path, observedFile: fileA,
  }), undefined, 'a stale local epoch cannot start a file read')

  assert.equal(openFileHeadRefreshDisposition({
    path: fileA.path, contentHash: fileA.contentHash, dirty: true, nextFiles: [fileB],
  }), 'preserve_stale', 'a dirty draft is preserved when its exact file hash leaves the refreshed tree')
  assert.equal(openFileHeadRefreshDisposition({
    path: fileA.path, contentHash: fileA.contentHash, dirty: false, nextFiles: [fileB],
  }), 'clear', 'a clean open file can be safely cleared when its hash leaves the refreshed tree')
  assert.equal(openFileHeadRefreshDisposition({
    path: fileA.path, contentHash: fileA.contentHash, dirty: true, nextFiles: [fileA, fileB],
  }), 'rebind', 'an unchanged exact file hash can be rebound to the refreshed head')
}

async function main() {
  testExactCandidateFileOpenFencing()
  const calls: Call[] = []
  const fetch: FetchLike = async (input, init) => {
    const url = new URL(input.toString())
    const method = init?.method ?? 'GET'
    calls.push({ method, path: url.pathname, headers: new Headers(init?.headers), body: init?.body })
    const headers = {
      etag,
      'x-sandbox-session-etag': etag,
      'x-sandbox-session-epoch': '2',
      'x-candidate-id': 'candidate-1',
      'x-candidate-version': method === 'GET' ? '7' : '8',
      'x-candidate-journal-sequence': '5',
      'x-writer-lease-epoch': '4',
      'x-candidate-tree-hash': treeHash,
    }
    if (url.pathname.endsWith('/files/src/my%20file.ts') && method === 'GET') {
      return new Response(fileBytes, {
        headers: { ...headers, 'x-content-hash': contentHash, 'x-file-mode': '100755' },
      })
    }
    if (url.pathname.endsWith('/connection-tickets')) {
      return Response.json({
        schemaVersion: 'sandbox-connection-ticket/v1',
        id: '22222222-2222-4222-8222-222222222222', ticket: `${'A'.repeat(42)}E`,
        sessionId, sessionEpoch: 2, channels: ['control', 'pty'],
        cursors: [{ channel: 'control', lastAckedSeq: 0 }, { channel: 'pty', lastAckedSeq: 9 }],
        webSocketPath: '/v1/sandbox-stream', expiresAt: new Date(Date.now() + 30_000).toISOString(),
      }, { status: 201, headers })
    }
    if (url.pathname.endsWith('/tree')) {
      return Response.json({ session, candidate, tree: candidate.currentTree }, {
        headers: {
          ...headers,
          'x-candidate-tree-etag': `"candidate-tree:candidate-1:${treeHash}"`,
        },
      })
    }
    if (url.pathname.endsWith('/writer-lease')) {
      return Response.json({ session: { ...session, version: 4 }, candidate }, { headers })
    }
    if (url.pathname.endsWith('/checkpoints')) {
      return Response.json({
        session: { ...session, version: 4 },
        checkpoint: {
          schemaVersion: 'candidate-snapshot/v1', id: 'checkpoint-1', projectId: 'project-1',
          candidateId: 'candidate-1', candidateVersion: 7, journalSequence: 5,
          sessionEpoch: 2, writerLeaseEpoch: 4,
          tree: candidate.currentTree, reason: 'autosave', createdBy: 'actor-1',
          createdAt: '2026-07-16T08:01:00Z',
        },
      }, { status: 201, headers })
    }
    if (url.pathname.endsWith(':freeze')) {
      return Response.json({
        session: {
          ...session,
          version: 4,
          candidate: { ...session.candidate, status: 'frozen' },
          allowedActions: ['view', 'terminate'],
        },
        candidate: { ...candidate, status: 'frozen' },
        proposal: {
          id: '88888888-8888-4888-8888-888888888888',
          projectId: '33333333-3333-4333-8333-333333333333',
          buildManifestId: '99999999-9999-4999-8999-999999999999',
          applicationBuildContract: {
            id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
            contractHash: contentHash,
          },
          baseWorkspaceRevision: {
            artifactId: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
            revisionId: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
            contentHash,
          },
          executionSource: 'candidate_freeze',
          candidateSource: {
            freezeReceiptId: '77777777-7777-4777-8777-777777777777',
            repositorySnapshotId: '66666666-6666-4666-8666-666666666666',
            sessionId,
            candidateId: '44444444-4444-4444-8444-444444444444',
            candidateSnapshotId: '55555555-5555-4555-8555-555555555555',
            candidateVersion: 7,
            journalSequence: 5,
            sessionEpoch: 2,
            writerLeaseEpoch: 4,
            baseTreeHash: `sha256:${'c'.repeat(64)}`,
            treeHash,
            fullStackTemplate: { id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', contentHash },
            verificationReceipt: { id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', contentHash },
          },
          operations: [{
            id: 'candidate-00001-aaaaaaaaaaaa',
            kind: 'file.upsert',
            path: 'src/app.ts',
            content: 'test',
            language: 'typescript',
            mode: '100755',
            rationale: 'Freeze exact CandidateSnapshot 55555555-5555-4555-8555-555555555555',
            traceSource: ['candidate-snapshot:55555555-5555-4555-8555-555555555555'],
            decision: 'pending',
          }],
          routes: [],
          apis: [],
          migrations: [],
          tests: [],
          previews: [],
          traceLinks: [{
            kind: 'candidate_snapshot',
            candidateId: '44444444-4444-4444-8444-444444444444',
            candidateSnapshotId: '55555555-5555-4555-8555-555555555555',
            baseTreeHash: `sha256:${'c'.repeat(64)}`,
            treeHash,
          }, {
            kind: 'candidate_verification_receipt',
            id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
            contentHash,
          }],
          diagnostics: [],
          assumptions: [],
          unimplementedItems: [],
          status: 'open',
          version: 1,
          payloadHash: contentHash.slice('sha256:'.length),
          createdBy: 'ffffffff-ffff-4fff-8fff-ffffffffffff',
          createdAt: '2026-07-16T08:01:00Z',
        },
        receipt: {
          id: 'freeze-receipt-1',
          projectId: 'project-1',
          sessionId,
          candidateId: 'candidate-1',
          candidateSnapshotId: 'candidate-snapshot-1',
          verificationReceipt: { id: 'verification-receipt-1', contentHash },
          implementationProposalId: 'proposal-1',
          requestKey: 'freeze-1',
          requestHash: contentHash,
          sessionVersion: 4,
          candidateVersion: 7,
          journalSequence: 5,
          sessionEpoch: 2,
          writerLeaseEpoch: 4,
          baseTreeHash: `sha256:${'c'.repeat(64)}`,
          candidateTreeHash: treeHash,
          buildManifest: { id: 'manifest-1', contentHash },
          buildContract: { id: 'contract-1', contentHash },
          fullStackTemplate: { id: 'template-1', contentHash },
          baseWorkspaceRevision: {
            artifactId: 'workspace-1',
            revisionId: 'workspace-r1',
            contentHash,
          },
          proposalPayloadHash: contentHash,
          operationCount: 1,
          reason: 'Freeze exact Candidate into implementation Proposal',
          createdBy: 'actor-1',
          createdAt: '2026-07-16T08:01:00Z',
        },
        replayed: false,
      }, { status: 201, headers })
    }
    if (url.pathname.endsWith(':abandon')) {
      return Response.json({
        session: {
          ...session,
          state: 'terminated',
          version: 4,
          candidate: { ...session.candidate, status: 'abandoned', version: 8 },
          allowedActions: ['view'],
        },
        candidate: { ...candidate, status: 'abandoned', version: 8 },
      }, { headers })
    }
    const processHeaders = {
      ...headers,
      etag: processEtag,
      'x-sandbox-process-etag': processEtag,
      'x-sandbox-session-etag': etag,
    }
    if (url.pathname.endsWith('/processes') && method === 'POST') {
      return Response.json({ session, process }, { status: 201, headers: processHeaders })
    }
    if (url.pathname.endsWith('/processes') && method === 'GET') {
      return Response.json({ session, processes: [process] }, { headers })
    }
    if (url.pathname.endsWith('/processes/process%2Fone/logs')) {
      return Response.json({
        session, process,
        log: { schemaVersion: 'sandbox-process/v1', id: processId, offset: 0, nextOffset: 4, value: 'dGVzdA==', eof: false, truncated: false },
      }, { headers: processHeaders })
    }
    if (url.pathname.endsWith('/processes/process%2Fone:signal')) {
      return Response.json({ session, process: { ...process, version: 3 } }, { headers: { ...processHeaders, etag: '"sandbox-process:process/one:3"' } })
    }
    if (url.pathname.endsWith('/processes/process%2Fone')) {
      return Response.json({ session, process }, { headers: processHeaders })
    }
    if (url.pathname.endsWith('/ptys') && method === 'POST') {
      return Response.json({ session, terminal }, { status: 201, headers })
    }
    if (url.pathname.endsWith('/ptys') && method === 'GET') {
      return Response.json({ session, terminals: [terminal] }, { headers })
    }
    if (url.pathname.endsWith('/ptys/terminal%2Fone')) {
      return Response.json({ session, terminal }, { headers })
    }
    if (url.pathname.endsWith('/ports') && method === 'GET') {
      return Response.json({
        session,
        ports: [{
          schemaVersion: 'sandbox-port/v1', name: 'web-http', serviceId: 'web-ui',
          number: 3000, protocol: 'http', state: 'listening', healthy: true, previewable: true,
        }],
      }, { headers })
    }
    if (url.pathname.endsWith('/ports/web-http/preview-links') && method === 'POST') {
      return Response.json({
        schemaVersion: 'sandbox-preview-grant/v1', id: 'preview-1', sessionId, sessionEpoch: 2,
        port: {
          schemaVersion: 'sandbox-port/v1', name: 'web-http', serviceId: 'web-ui',
          number: 3000, protocol: 'http', state: 'listening', healthy: true, previewable: true,
        },
        url: `https://${'a'.repeat(48)}.preview.example/`, expiresAt: '2026-07-16T08:15:00Z',
      }, { status: 201, headers })
    }
    if (method === 'PUT' || url.pathname.endsWith('/file-operations')) {
      const pointer = {
        store: 'content', ref: 'tree-object-1', ownerId: 'candidate-1', treeHash,
        fileCount: 2, byteSize: 8, contentObjectHash: contentHash,
      }
      return Response.json({
        session: { ...session, version: 4, candidate: { ...session.candidate, version: 8 } },
        mutation: {
          recovered: false,
          finalizationPending: false,
          beforeTree: pointer,
          afterTree: { ...pointer, ref: 'tree-object-2' },
          entry: {
            candidateId: 'candidate-1', sequence: 1,
            candidateVersionFrom: 7, candidateVersionTo: 8,
            sessionEpoch: 2, leaseEpoch: 4, actorId: 'actor-1', attribution: 'user',
            operation: {
              id: 'save-1', kind: 'file.upsert', path: 'src/my file.ts',
              expectedHash: contentHash, contentHash, byteSize: 3, mode: '100755',
            },
            beforeTreeHash: treeHash, afterTreeHash: treeHash,
            createdAt: '2026-07-16T08:02:00Z',
          },
        },
      }, { headers })
    }
    return Response.json(session, { headers })
  }

  const platform = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test', fetch,
      csrfTokenStore: tokenStore(), requestIdFactory: () => 'sandbox-generated-key',
    },
  })
  const client = platform.sandbox

  const created = await client.createSession('project/one', 'candidate/one', {
    idempotencyKey: 'create-sandbox-1',
  })
  assert.equal(created.data.id, sessionId)
  const loaded = await client.getSession(sessionId)
  const fences = client.fences(loaded)
  assert.deepEqual(fences, {
    etag,
    sessionEpoch: 2,
    candidateVersion: 7,
    writerLeaseEpoch: 4,
    treeHash,
  })
  assert.deepEqual(loaded.data.allowedActions, [])
  assert.deepEqual(loaded.data.blockingReasons, [])
  assert.deepEqual(loaded.data.allowedServices, [])

  const ticket = await client.createConnectionTicket(sessionId, {
    channels: ['control', 'pty'], cursors: [{ channel: 'pty', lastAckedSeq: 9 }],
  }, { idempotencyKey: 'sandbox-ticket-1' })
  assert.equal(ticket.data.sessionEpoch, 2)
  assert.equal(ticket.data.cursors[1]?.lastAckedSeq, 9)

  const tree = await client.getTree(sessionId)
  assert.equal(tree.data.tree.files[0]?.path, 'src/app.ts')
  assert.equal(tree.data.candidate.treeHash, treeHash)

  const readTreeFile = tree.data.tree.files.find((entry) => entry.path === 'src/my file.ts')
  const readFence = createExactCandidateFileOpenFence({
    projectId: 'project-1',
    session: tree.data.session,
    candidate: tree.data.candidate,
    fences,
    path: 'src/my file.ts',
    observedFile: readTreeFile,
  })
  assert.ok(readFence)
  await assert.rejects(
    () => (client.readFile as unknown as (session: string, path: string) => Promise<unknown>)(
      sessionId,
      'src/my file.ts',
    ),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('exact Sandbox/Candidate file-read fence'),
    'readFile must fail closed at runtime when a caller bypasses its required fence type',
  )
  const file = await client.readFile(sessionId, 'src/my file.ts', { fence: readFence })
  assert.deepEqual([...new Uint8Array(file.data.value)], [0, 1, 2, 255])
  assert.equal(file.data.candidateId, 'candidate-1')
  assert.equal(file.data.journalSequence, 5)
  assert.equal(file.data.contentHash, contentHash)
  assert.equal(file.data.mode, '100755')
  assert.equal(file.data.fences.etag, etag)

  const mismatchedFileClient = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch: async () => new Response(new Uint8Array([0, 1, 2, 254]), {
        headers: {
          etag,
          'x-sandbox-session-etag': etag,
          'x-sandbox-session-epoch': '2',
          'x-candidate-id': 'candidate-1',
          'x-candidate-version': '7',
          'x-candidate-journal-sequence': '5',
          'x-writer-lease-epoch': '4',
          'x-candidate-tree-hash': treeHash,
          'x-content-hash': contentHash,
          'x-file-mode': '100755',
        },
      }),
      csrfTokenStore: tokenStore(),
      requestIdFactory: () => 'sandbox-mismatched-file',
    },
  }).sandbox
  await assert.rejects(
    () => mismatchedFileClient.readFile(sessionId, 'src/my file.ts', { fence: readFence }),
    (cause: unknown) => cause instanceof PlatformProtocolError
      && cause.message.includes('bytes do not match the declared X-Content-Hash'),
    'readFile must reject response bytes whose evidence does not match the requested file hash',
  )

  await client.acquireWriterLease(sessionId, 300, { fences, idempotencyKey: 'lease-1' })
  const put = await client.putFile(
    sessionId,
    'src/my file.ts',
    new Uint8Array([9, 8, 7]),
    contentHash,
    { fences, idempotencyKey: 'save-1', mode: '100755' },
  )
  await client.deleteFile(sessionId, 'src/my file.ts', contentHash, {
    fences, idempotencyKey: 'delete-1',
  })
  await client.renameFile(sessionId, 'src/old.ts', 'src/new.ts', contentHash, {
    fences, idempotencyKey: 'rename-1',
  })
  const checkpoint = await client.checkpoint(sessionId, {
    checkpointId: 'checkpoint-1', reason: 'autosave',
  }, { fences, idempotencyKey: 'checkpoint-1' })
  await client.suspendSession(sessionId, { fences, idempotencyKey: 'suspend-1' })
  await client.resumeSession(sessionId, { fences, idempotencyKey: 'resume-1' })
  await client.terminateSession(sessionId, 'user closed the sandbox', {
    fences, idempotencyKey: 'terminate-1',
  })
  const startedProcess = await client.startProcess(sessionId, {
    serviceId: 'web-ui', commandId: 'dev',
  }, { fences, idempotencyKey: 'process-start-1' })
  const processes = await client.listProcesses(sessionId, { limit: 10 })
  const loadedProcess = await client.getProcess(sessionId, processId)
  const processLogs = await client.readProcessLogs(sessionId, processId, 0, 4096)
  await client.signalProcess(sessionId, processId, 'TERM', {
    sessionFences: fences, processEtag, idempotencyKey: 'process-signal-1',
  })
  const createdTerminal = await client.createTerminal(sessionId, {
    workingDirectory: '.', rows: 24, columns: 80,
  }, { fences, idempotencyKey: 'terminal-open-1' })
  const terminals = await client.listTerminals(sessionId, { limit: 10 })
  const loadedTerminal = await client.getTerminal(sessionId, terminalId)
  const ports = await client.listPorts(sessionId)
  const previewLink = await client.createPreviewLink(sessionId, 'web-http', {
    fences, idempotencyKey: 'preview-link-1',
  })
  const frozen = await client.freezeCandidate(sessionId, {
    checkpointId: 'checkpoint-1',
    verificationReceiptId: 'verification-receipt-1',
    verificationReceiptHash: contentHash,
    reason: 'Freeze exact Candidate into implementation Proposal',
  }, { fences, idempotencyKey: 'freeze-1' })
  const abandoned = await client.abandonCandidate(sessionId, {
    candidateId: 'candidate-1',
    checkpointId: 'checkpoint-1',
    reason: 'Start this implementation again from the exact build input',
  }, { fences, idempotencyKey: 'abandon-1' })
  await client.abandonCandidate(sessionId, {
    candidateId: 'candidate-1',
    reason: 'Discard a clean Candidate without manufacturing a checkpoint',
  }, { fences, idempotencyKey: 'abandon-clean-1' })

  assert.equal(put.data.session.candidate.version, 8)
  assert.equal(put.data.mutation.recovered, false)
  assert.equal(put.data.mutation.entry.operation.path, 'src/my file.ts')
  assert.equal(checkpoint.data.checkpoint.reason, 'autosave')
  assert.equal(startedProcess.data.process.argv[1], 'server.js')
  assert.equal(processes.data.processes[0]?.state, 'running')
  assert.equal(loadedProcess.data.process.pid, 42)
  assert.equal(processLogs.data.log.valueBase64, 'dGVzdA==')
  assert.equal(processLogs.data.log.truncated, false)
  assert.equal(createdTerminal.data.terminal.shellPath, '/bin/bash')
  assert.equal(terminals.data.terminals[0]?.state, 'running')
  assert.equal(loadedTerminal.data.terminal.columns, 80)
  assert.equal(ports.data.ports[0]?.state, 'listening')
  assert.equal(ports.data.ports[0]?.previewable, true)
  assert.equal(previewLink.data.url, `https://${'a'.repeat(48)}.preview.example/`)
  assert.equal(frozen.data.candidate.status, 'frozen')
  assert.equal(
    frozen.data.proposal.candidateSource?.candidateSnapshotId,
    '55555555-5555-4555-8555-555555555555',
  )
  assert.equal(frozen.data.proposal.operations[0]?.mode, '100755')
  assert.deepEqual(frozen.data.proposal.routes, [])
  assert.equal(frozen.data.receipt.implementationProposalId, 'proposal-1')
  assert.equal(frozen.data.receipt.verificationReceipt.id, 'verification-receipt-1')
  assert.equal(frozen.data.replayed, false)
  assert.equal(abandoned.data.session.state, 'terminated')
  assert.equal(abandoned.data.session.candidate.status, 'abandoned')
  assert.equal(abandoned.data.candidate.status, 'abandoned')
  assert.deepEqual(calls.map((call) => [call.method, call.path]), [
    ['POST', '/v1/projects/project%2Fone/sandbox-sessions'],
    ['GET', `/v1/sandbox-sessions/${sessionId}`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/connection-tickets`],
    ['GET', `/v1/sandbox-sessions/${sessionId}/tree`],
    ['GET', `/v1/sandbox-sessions/${sessionId}/files/src/my%20file.ts`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/writer-lease`],
    ['PUT', `/v1/sandbox-sessions/${sessionId}/files/src/my%20file.ts`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/file-operations`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/file-operations`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/checkpoints`],
    ['POST', `/v1/sandbox-sessions/${sessionId}:suspend`],
    ['POST', `/v1/sandbox-sessions/${sessionId}:resume`],
    ['POST', `/v1/sandbox-sessions/${sessionId}:terminate`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/processes`],
    ['GET', `/v1/sandbox-sessions/${sessionId}/processes`],
    ['GET', `/v1/sandbox-sessions/${sessionId}/processes/process%2Fone`],
    ['GET', `/v1/sandbox-sessions/${sessionId}/processes/process%2Fone/logs`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/processes/process%2Fone:signal`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/ptys`],
    ['GET', `/v1/sandbox-sessions/${sessionId}/ptys`],
    ['GET', `/v1/sandbox-sessions/${sessionId}/ptys/terminal%2Fone`],
    ['GET', `/v1/sandbox-sessions/${sessionId}/ports`],
    ['POST', `/v1/sandbox-sessions/${sessionId}/ports/web-http/preview-links`],
    ['POST', `/v1/sandbox-sessions/${sessionId}:freeze`],
    ['POST', `/v1/sandbox-sessions/${sessionId}:abandon`],
    ['POST', `/v1/sandbox-sessions/${sessionId}:abandon`],
  ])

	assert.deepEqual(JSON.parse(calls[0]?.body as string), { candidateId: 'candidate/one' })
	assert.equal(calls[0]?.headers.get('if-match'), null)
	assert.equal(calls[0]?.headers.get('idempotency-key'), 'create-sandbox-1')
	assert.deepEqual(JSON.parse(calls[2]?.body as string), {
	  channels: ['control', 'pty'],
	  cursors: [{ channel: 'control', lastAckedSeq: 0 }, { channel: 'pty', lastAckedSeq: 9 }],
	})
	assert.equal(calls[2]?.headers.get('if-match'), null)
	assert.equal(calls[2]?.headers.get('idempotency-key'), 'sandbox-ticket-1')
	assert.equal(calls[4]?.headers.get('x-sandbox-session-epoch'), '2')
	assert.equal(calls[4]?.headers.get('x-expected-candidate-id'), 'candidate-1')
	assert.equal(calls[4]?.headers.get('x-candidate-version'), '7')
	assert.equal(calls[4]?.headers.get('x-candidate-journal-sequence'), '5')
	assert.equal(calls[4]?.headers.get('x-writer-lease-epoch'), '4')
	assert.equal(calls[4]?.headers.get('x-candidate-tree-hash'), treeHash)
	assert.equal(calls[4]?.headers.get('x-expected-file-hash'), contentHash)

  for (const index of [5, 6, 7, 8, 9]) {
    const call = calls[index]
    assert.equal(call?.headers.get('if-match'), etag)
    assert.equal(call?.headers.get('x-sandbox-session-epoch'), '2')
    assert.equal(call?.headers.get('x-candidate-version'), '7')
    assert.equal(call?.headers.get('x-writer-lease-epoch'), '4')
    assert.equal(call?.headers.get('x-csrf-token'), 'csrf-sandbox')
  }
  assert.equal(calls[6]?.headers.get('x-expected-file-hash'), contentHash)
  assert.equal(calls[6]?.headers.get('x-file-mode'), '100755')
  assert.deepEqual([...new Uint8Array(calls[6]?.body as ArrayBufferView as Uint8Array)], [9, 8, 7])
  assert.deepEqual(JSON.parse(calls[7]?.body as string), {
    kind: 'file.delete', path: 'src/my file.ts', expectedFileHash: contentHash,
  })
  assert.deepEqual(JSON.parse(calls[8]?.body as string), {
    kind: 'file.rename', path: 'src/new.ts', fromPath: 'src/old.ts', expectedFileHash: contentHash,
  })
  for (const index of [10, 11, 12]) {
    const call = calls[index]
    assert.equal(call?.headers.get('if-match'), etag)
    assert.equal(call?.headers.get('x-sandbox-session-epoch'), '2')
    assert.equal(call?.headers.get('x-candidate-version'), null)
    assert.equal(call?.headers.get('x-writer-lease-epoch'), null)
    assert.equal(call?.headers.get('x-csrf-token'), 'csrf-sandbox')
  }
  assert.deepEqual(JSON.parse(calls[12]?.body as string), { reason: 'user closed the sandbox' })
  assert.deepEqual(JSON.parse(calls[13]?.body as string), { serviceId: 'web-ui', commandId: 'dev' })
  assert.equal(calls[13]?.headers.get('if-match'), etag)
  assert.equal(calls[13]?.headers.get('x-sandbox-session-epoch'), '2')
  assert.equal(calls[14]?.path, `/v1/sandbox-sessions/${sessionId}/processes`)
  assert.deepEqual(JSON.parse(calls[17]?.body as string), { signal: 'TERM' })
  assert.equal(calls[17]?.headers.get('if-match'), processEtag)
  assert.equal(calls[17]?.headers.get('x-sandbox-session-etag'), etag)
  assert.equal(calls[17]?.headers.get('x-sandbox-session-epoch'), '2')
  assert.deepEqual(JSON.parse(calls[18]?.body as string), {
    workingDirectory: '.', rows: 24, columns: 80,
  })
  assert.equal(calls[18]?.headers.get('if-match'), etag)
  assert.equal(calls[18]?.headers.get('x-sandbox-session-epoch'), '2')
  assert.equal(calls[22]?.headers.get('if-match'), etag)
  assert.equal(calls[22]?.headers.get('x-sandbox-session-epoch'), '2')
  assert.equal(calls[22]?.headers.get('x-csrf-token'), 'csrf-sandbox')
  assert.equal(calls[23]?.headers.get('if-match'), etag)
  assert.equal(calls[23]?.headers.get('x-sandbox-session-epoch'), '2')
  assert.equal(calls[23]?.headers.get('x-candidate-version'), '7')
  assert.equal(calls[23]?.headers.get('x-writer-lease-epoch'), '4')
  assert.deepEqual(JSON.parse(calls[23]?.body as string), {
    checkpointId: 'checkpoint-1',
    verificationReceiptId: 'verification-receipt-1',
    verificationReceiptHash: contentHash,
    reason: 'Freeze exact Candidate into implementation Proposal',
  })
  assert.equal(calls[24]?.headers.get('if-match'), etag)
  assert.equal(calls[24]?.headers.get('x-sandbox-session-epoch'), '2')
  assert.equal(calls[24]?.headers.get('x-candidate-version'), '7')
  assert.equal(calls[24]?.headers.get('x-writer-lease-epoch'), '4')
  assert.equal(calls[24]?.headers.get('x-csrf-token'), 'csrf-sandbox')
  assert.equal(calls[24]?.headers.get('idempotency-key'), 'abandon-1')
  assert.deepEqual(JSON.parse(calls[24]?.body as string), {
    candidateId: 'candidate-1',
    checkpointId: 'checkpoint-1',
    reason: 'Start this implementation again from the exact build input',
  })
  assert.equal(calls[25]?.headers.get('if-match'), etag)
  assert.equal(calls[25]?.headers.get('x-sandbox-session-epoch'), '2')
  assert.equal(calls[25]?.headers.get('x-candidate-version'), '7')
  assert.equal(calls[25]?.headers.get('x-writer-lease-epoch'), '4')
  assert.equal(calls[25]?.headers.get('idempotency-key'), 'abandon-clean-1')
  assert.deepEqual(JSON.parse(calls[25]?.body as string), {
    candidateId: 'candidate-1',
    reason: 'Discard a clean Candidate without manufacturing a checkpoint',
  })
}

void main()
