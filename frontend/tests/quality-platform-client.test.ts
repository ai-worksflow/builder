import assert from 'node:assert/strict'
import {
  QualityClient,
  QualityClientError,
  qualityResultAsPromptContext,
  requestQualityRun,
} from '../lib/quality/client'
import { HttpClient, type CsrfTokenStore, type FetchLike } from '../lib/platform/http'

type TestCase = {
  readonly name: string
  readonly run: () => void | Promise<void>
}

const tests: TestCase[] = []

function test(name: string, run: TestCase['run']) {
  tests.push({ name, run })
}

function tokenStore(value = 'csrf-quality'): CsrfTokenStore {
  let token: string | undefined = value
  return {
    get: () => token,
    set: (next) => { token = next },
    clear: () => { token = undefined },
  }
}

const workspaceRevision = {
  artifactId: '11111111-1111-4111-8111-111111111111',
  revisionId: '22222222-2222-4222-8222-222222222222',
  revisionNumber: 7,
  contentHash: `sha256:${'a'.repeat(64)}`,
}

function report(overrides: Record<string, unknown> = {}) {
  return {
    id: '33333333-3333-4333-8333-333333333333',
    projectId: 'project alpha',
    workspaceRevision: {
      artifactId: workspaceRevision.artifactId,
      revisionId: workspaceRevision.revisionId,
      contentHash: workspaceRevision.contentHash,
    },
    status: 'failed',
    passed: false,
    score: 85,
    runnerVersion: '1.0.0',
    sandboxKind: 'container',
    checks: [
      {
        id: 'build',
        status: 'passed',
        durationMs: 12,
        diagnostics: [],
      },
      {
        id: 'secret',
        status: 'failed',
        durationMs: 2,
        diagnostics: [
          {
            id: 'finding-1',
            checkId: 'secret',
            code: 'credential_detected',
            severity: 'error',
            message: 'A credential-like value was detected.',
            path: 'src/config.ts',
          },
        ],
      },
    ],
    diagnostics: [
      {
        id: 'finding-1',
        checkId: 'secret',
        code: 'credential_detected',
        severity: 'error',
        message: 'A credential-like value was detected.',
        path: 'src/config.ts',
      },
    ],
    reportArtifactId: '44444444-4444-4444-8444-444444444444',
    reportRevisionId: '55555555-5555-4555-8555-555555555555',
    createdBy: '66666666-6666-4666-8666-666666666666',
    startedAt: '2026-07-10T08:00:00.000Z',
    completedAt: '2026-07-10T08:00:00.250Z',
    version: 1,
    etag: '"quality-run:test:1"',
    ...overrides,
  }
}

test('quality runs use the shared platform transport and exact immutable revision', async () => {
  let url = ''
  let init: RequestInit | undefined
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async (input, nextInit) => {
      url = input.toString()
      init = nextInit
      return Response.json(
        { qualityRun: report() },
        { status: 201, headers: { etag: '"quality-run:test:1"' } },
      )
    }) as FetchLike,
  })

  const result = await requestQualityRun(
    http,
    'project alpha',
    workspaceRevision,
    { idempotencyKey: 'quality-idempotency' },
  )

  assert.equal(url, 'https://platform.example.test/v1/projects/project%20alpha/quality-runs')
  assert.equal(init?.method, 'POST')
  const headers = new Headers(init?.headers)
  assert.equal(headers.get('x-csrf-token'), 'csrf-quality')
  assert.equal(headers.get('idempotency-key'), 'quality-idempotency')
  assert.deepEqual(JSON.parse(String(init?.body)), {
    workspaceRevision: {
      artifactId: workspaceRevision.artifactId,
      revisionId: workspaceRevision.revisionId,
      contentHash: workspaceRevision.contentHash,
    },
  })
  assert.equal(result.metadata.runId, '33333333-3333-4333-8333-333333333333')
  assert.equal(result.metadata.executionMode, 'sandbox')
  assert.equal(result.score.percentage, 85)
  assert.equal(result.durationMs, 250)
  assert.deepEqual(
    result.checks.map((check) => [check.title, check.score.percentage]),
    [['Build', 100], ['Secret scan', 0]],
  )
  assert.match(qualityResultAsPromptContext(result), /credential_detected/)
  assert.match(qualityResultAsPromptContext(result), new RegExp(workspaceRevision.revisionId))
})

test('quality history uses the project-scoped Go route and revision filter', async () => {
  const urls: string[] = []
  const client = new QualityClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async (input) => {
      urls.push(input.toString())
      return Response.json({ qualityRuns: [report({ status: 'passed', passed: true, score: 100 })] })
    }) as FetchLike,
  }))

  const results = await client.list('project/alpha', {
    workspaceRevisionId: workspaceRevision.revisionId,
  })
  assert.equal(
    urls[0],
    `https://platform.example.test/v1/projects/project%2Falpha/quality-runs?workspaceRevisionId=${workspaceRevision.revisionId}`,
  )
  assert.equal(results[0].passed, true)
})

test('RFC problem responses retain status, code and retry metadata', async () => {
  const client = new QualityClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async () => Response.json({
      type: 'urn:worksflow:problem:conflict',
      title: 'Conflict',
      status: 409,
      detail: 'The workspace revision is not approved.',
      code: 'conflict',
    }, { status: 409, headers: { 'retry-after': '3' } })) as FetchLike,
  }))

  await assert.rejects(
    client.run('project', workspaceRevision),
    (error: unknown) => (
      error instanceof QualityClientError &&
      error.code === 'conflict' &&
      error.status === 409 &&
      error.retryAfterSeconds === 3
    ),
  )
})

test('legacy raw-file input fails closed without contacting a removed Next route', async () => {
  await assert.rejects(
    requestQualityRun({ projectId: 'project', files: [], entryPath: 'index.html' }),
    (error: unknown) => (
      error instanceof QualityClientError &&
      error.code === 'frozen_workspace_revision_required'
    ),
  )
})

async function main() {
  let failed = 0
  for (const { name, run } of tests) {
    try {
      await run()
      console.log(`✓ ${name}`)
    } catch (error) {
      failed += 1
      console.error(`✗ ${name}`)
      console.error(error)
    }
  }
  if (failed > 0) {
    console.error(`${failed} quality platform client test(s) failed.`)
    process.exitCode = 1
    return
  }
  console.log(`${tests.length} quality platform client test(s) passed.`)
}

void main()

