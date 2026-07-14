import assert from 'node:assert/strict'
import { collectConversationPages, PlatformConversationClient } from '../lib/platform/conversation-client'
import type {
  ConversationCommandDto,
  ConversationSummaryCheckpointDto,
  WorkflowIntentProposalDto,
} from '../lib/platform/conversation-contract'
import { HttpClient, PlatformHttpError, type FetchLike } from '../lib/platform/http'

type Call = {
  readonly method: string
  readonly path: string
  readonly headers: Headers
  readonly body?: unknown
}

const calls: Call[] = []
const client = new PlatformConversationClient(new HttpClient({
  baseUrl: 'https://platform.example.test',
  csrfTokenStore: { get: () => 'csrf-test', set: () => {}, clear: () => {} },
  fetch: (async (input, init) => {
    calls.push({
      method: init?.method ?? 'GET',
      path: new URL(input.toString()).pathname,
      headers: new Headers(init?.headers),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    })
    return Response.json({ id: 'response' })
  }) as FetchLike,
}))

async function main() {
  await client.create('project/1', 'Application planning')
  await client.addMessage('project/1', 'conversation/1', 'Build an order console.')
  await client.generateIntentProposal('project/1', 'conversation/1', {
    triggerMessageId: 'message-1',
    desiredOutputCapability: 'application',
    sourceRefs: [{
      artifactId: 'brief-1',
      revisionId: 'brief-revision-2',
      contentHash: 'sha256:brief',
    }],
    manifestIntent: {
      mode: 'use_existing',
      inputManifest: { id: 'manifest-1', hash: 'sha256:manifest' },
      purpose: 'start_application_workflow',
    },
    workbenchTargetHint: {
      runId: 'run-1',
      rootBundleId: 'bundle-root-1',
    },
    model: 'gpt-5',
  })

  const proposal = {
    id: 'proposal/1',
    conversationId: 'conversation/1',
    etag: '"intent-proposal:1"',
  } as WorkflowIntentProposalDto
  await client.decideIntentProposal('project/1', 'conversation/1', proposal, 'accept')

  const startCommand = {
    id: 'start-command/1',
    conversationId: 'conversation/1',
    kind: 'start_workflow',
    etag: '"conversation-command:start:1"',
  } as ConversationCommandDto
  await client.executeCommand('project/1', 'conversation/1', startCommand)

  const command = {
    id: 'command/1',
    conversationId: 'conversation/1',
    kind: 'workbench_instruction',
    etag: '"conversation-command:1"',
  } as ConversationCommandDto
  await client.executeCommand('project/1', 'conversation/1', command)
  const refreshedRetryCommand = {
    ...command,
    etag: '"conversation-command:2"',
    failure: { code: 'ai_unavailable', message: 'Retry safely.' },
  } as ConversationCommandDto
  await client.executeCommand('project/1', 'conversation/1', refreshedRetryCommand)
  const summaryCheckpoint = {
    id: 'summary-checkpoint/1',
    etag: '"conversation-summary-checkpoint:1"',
  } as ConversationSummaryCheckpointDto
  await client.decideSummaryCheckpoint(
    'project/1',
    'conversation/1',
    summaryCheckpoint,
    'approve',
    'Verified the exact immutable source prefix.',
    true,
  )

  assert.deepEqual(calls.map((call) => [call.method, call.path]), [
    ['POST', '/v1/projects/project%2F1/conversations'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/messages'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/intent-proposals/generate'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/intent-proposals/proposal%2F1/decision'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/commands/start-command%2F1/execute'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/commands/command%2F1/execute'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/commands/command%2F1/execute'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/summary-checkpoints/summary-checkpoint%2F1/decision'],
  ])
  for (const call of calls) {
    assert.ok(call.headers.get('idempotency-key'), `${call.path} omitted Idempotency-Key`)
  }
  assert.deepEqual(calls[1].body, { content: 'Build an order console.' })
  assert.equal(Object.hasOwn(calls[1].body as object, 'role'), false)
  const generated = calls[2].body as {
    desiredOutputCapability: string
    sourceRefs: readonly Record<string, unknown>[]
    workbenchTargetHint: { readonly runId: string; readonly rootBundleId: string }
  }
  assert.equal(generated.desiredOutputCapability, 'application')
  assert.equal(Object.hasOwn(generated, 'candidateDefinitionVersionIds'), false)
  assert.equal(Object.hasOwn(generated.sourceRefs[0], 'revisionNumber'), false)
  assert.deepEqual(generated.workbenchTargetHint, { runId: 'run-1', rootBundleId: 'bundle-root-1' })
  assert.equal(calls[3].headers.get('if-match'), '"intent-proposal:1"')
  assert.equal(calls[4].headers.get('if-match'), '"conversation-command:start:1"')
  assert.deepEqual(calls[4].body, {})
  assert.equal(calls[5].headers.get('if-match'), '"conversation-command:1"')
  assert.deepEqual(calls[5].body, {})
  assert.equal(calls[6].headers.get('if-match'), '"conversation-command:2"')
  assert.deepEqual(calls[6].body, {})
  assert.equal(calls[7].headers.get('if-match'), '"conversation-summary-checkpoint:1"')
  assert.deepEqual(calls[7].body, {
    decision: 'approve',
    reason: 'Verified the exact immutable source prefix.',
    soloReviewConfirmed: true,
  })

  const conflictClient = new PlatformConversationClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async () => Response.json({
      type: 'urn:worksflow:problem:conversation_summary_checkpoint_required',
      title: 'Controlled summary checkpoint required',
      status: 409,
      code: 'conversation_summary_checkpoint_required',
      detail: 'Create and review a controlled summary checkpoint; no messages were silently omitted.',
    }, { status: 409, headers: { 'content-type': 'application/problem+json' } })) as FetchLike,
  }))
  await assert.rejects(
    conflictClient.generateIntentProposal('project-1', 'conversation-1', {
      triggerMessageId: 'message-51',
      desiredOutputCapability: 'application',
      sourceRefs: [{ artifactId: 'brief-1', revisionId: 'revision-1', contentHash: 'sha256:brief' }],
      manifestIntent: {
        mode: 'use_existing',
        inputManifest: { id: 'manifest-1', hash: 'sha256:manifest' },
        purpose: 'start_or_continue_application_workflow',
      },
    }),
    (error: unknown) => error instanceof PlatformHttpError
      && error.status === 409
      && error.code === 'conversation_summary_checkpoint_required'
      && error.message.includes('no messages were silently omitted'),
  )

  const requestedCursors: Array<string | undefined> = []
  const completeHistory = await collectConversationPages(async (cursor) => {
    requestedCursors.push(cursor)
    return {
      data: cursor
        ? { items: [{ id: 'message-201' }], nextCursor: undefined }
        : { items: Array.from({ length: 200 }, (_, index) => ({ id: `message-${index + 1}` })), nextCursor: 'after-200' },
    }
  })
  assert.equal(completeHistory.length, 201)
  assert.deepEqual(requestedCursors, [undefined, 'after-200'])
  await assert.rejects(
    collectConversationPages(async () => ({ data: { items: [], nextCursor: 'repeat' } })),
    /repeated cursor/,
  )
  console.log('✓ governed conversation client preserves immutable intent and command boundaries')
}

void main()
