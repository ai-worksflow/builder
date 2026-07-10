import assert from 'node:assert/strict'
import { PlatformConversationClient } from '../lib/platform/conversation-client'
import type {
  ConversationCommandDto,
  WorkflowIntentProposalDto,
} from '../lib/platform/conversation-contract'
import { HttpClient, type FetchLike } from '../lib/platform/http'

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
    candidateDefinitionVersionIds: ['definition-version-1'],
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
  await client.executeCommand('project/1', 'conversation/1', command, {
    workbenchResult: { runId: 'run-1', bundleId: 'bundle-1' },
  })

  assert.deepEqual(calls.map((call) => [call.method, call.path]), [
    ['POST', '/v1/projects/project%2F1/conversations'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/messages'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/intent-proposals/generate'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/intent-proposals/proposal%2F1/decision'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/commands/start-command%2F1/execute'],
    ['POST', '/v1/projects/project%2F1/conversations/conversation%2F1/commands/command%2F1/execute'],
  ])
  for (const call of calls) {
    assert.ok(call.headers.get('idempotency-key'), `${call.path} omitted Idempotency-Key`)
  }
  assert.deepEqual(calls[1].body, { content: 'Build an order console.' })
  assert.equal(Object.hasOwn(calls[1].body as object, 'role'), false)
  const generated = calls[2].body as { sourceRefs: readonly Record<string, unknown>[] }
  assert.equal(Object.hasOwn(generated.sourceRefs[0], 'revisionNumber'), false)
  assert.equal(calls[3].headers.get('if-match'), '"intent-proposal:1"')
  assert.equal(calls[4].headers.get('if-match'), '"conversation-command:start:1"')
  assert.deepEqual(calls[4].body, {})
  assert.equal(calls[5].headers.get('if-match'), '"conversation-command:1"')
  assert.deepEqual(calls[5].body, {
    workbenchResult: { runId: 'run-1', bundleId: 'bundle-1' },
  })

  const callCount = calls.length
  assert.throws(() => client.executeCommand('project/1', 'conversation/1', startCommand, {
    workbenchResult: { runId: 'run-1', bundleId: 'bundle-1' },
  }), /does not accept/)
  assert.throws(() => client.executeCommand('project/1', 'conversation/1', command), /requires an exact run/)
  assert.equal(calls.length, callCount, 'invalid command shapes reached the server')
  console.log('✓ governed conversation client preserves immutable intent and command boundaries')
}

void main()
