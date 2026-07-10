import assert from 'node:assert/strict'
import {
  DeliveryClient,
  DeliveryClientError,
  exportWorkspaceArchive,
  listDeployments,
  publishWorkspace,
  rollbackDeployment,
} from '../lib/delivery/client'
import { createWorkspace } from '../lib/worksflow/workspace-model'
import { HttpClient, type CsrfTokenStore, type FetchLike } from '../lib/platform/http'

type TestCase = {
  readonly name: string
  readonly run: () => void | Promise<void>
}

const tests: TestCase[] = []

function test(name: string, run: TestCase['run']) {
  tests.push({ name, run })
}

function tokenStore(value = 'csrf-delivery'): CsrfTokenStore {
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
  revisionNumber: 9,
  contentHash: `sha256:${'b'.repeat(64)}`,
}

const deploymentId = '33333333-3333-4333-8333-333333333333'
const deploymentVersionId = '44444444-4444-4444-8444-444444444444'
const buildManifestId = '66666666-6666-4666-8666-666666666666'
const qualityRunId = '77777777-7777-4777-8777-777777777777'

function deployment(includeVersions = true) {
  return {
    id: deploymentId,
    projectId: 'project delivery',
    environment: 'preview',
    environmentRef: 'preview/default',
    provider: 'local-static',
    status: 'ready',
    activeVersionId: deploymentVersionId,
    publicUrl: `https://published.example.test/${deploymentId}/${deploymentVersionId}/`,
    ...(includeVersions ? {
      versions: [{
        id: deploymentVersionId,
        number: 1,
        action: 'publish',
        workspaceRevision: {
          artifactId: workspaceRevision.artifactId,
          revisionId: workspaceRevision.revisionId,
          contentHash: workspaceRevision.contentHash,
        },
        status: 'ready',
        publicUrl: `https://published.example.test/${deploymentId}/${deploymentVersionId}/`,
        entryPath: 'index.html',
        checksum: `sha256:${'c'.repeat(64)}`,
        fileCount: 3,
        totalBytes: 512,
        environmentRef: 'preview/default',
        environmentVariableNames: ['PUBLIC_API_ORIGIN'],
        message: 'Preview release',
        createdBy: '55555555-5555-4555-8555-555555555555',
        createdAt: '2026-07-10T08:00:00.000Z',
      }],
    } : {}),
    version: 2,
    etag: `"deployment:${deploymentId}:2"`,
    createdBy: '55555555-5555-4555-8555-555555555555',
    createdAt: '2026-07-10T08:00:00.000Z',
    updatedAt: '2026-07-10T08:00:01.000Z',
  }
}

test('source export downloads the exact Go archive and defaults to server redaction', async () => {
  let url = ''
  let init: RequestInit | undefined
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async (input, nextInit) => {
      url = input.toString()
      init = nextInit
      return new Response(new Blob(['exact zip bytes']), {
        headers: {
          'content-type': 'application/zip',
          'content-disposition': 'attachment; filename="release-source.zip"',
          'x-archive-file-count': '4',
          'x-archive-redaction-count': '2',
          digest: 'sha-256=exact',
          etag: '"sha256:archive"',
        },
      })
    }) as FetchLike,
  })

  const result = await exportWorkspaceArchive(http, 'project delivery', {
    kind: 'source',
    revision: workspaceRevision,
  })

  assert.equal(url, 'https://platform.example.test/v1/projects/project%20delivery/exports')
  assert.equal(init?.method, 'POST')
  const headers = new Headers(init?.headers)
  assert.equal(headers.get('idempotency-key'), null)
  assert.equal(headers.get('x-csrf-token'), 'csrf-delivery')
  assert.deepEqual(JSON.parse(String(init?.body)), {
    kind: 'source',
    revision: {
      artifactId: workspaceRevision.artifactId,
      revisionId: workspaceRevision.revisionId,
      contentHash: workspaceRevision.contentHash,
    },
  })
  assert.equal(await result.blob.text(), 'exact zip bytes')
  assert.equal(result.filename, 'release-source.zip')
  assert.equal(result.fileCount, 4)
  assert.equal(result.redactionCount, 2)
})

test('publish uses CSRF, idempotency and strong conditional writes', async () => {
  let init: RequestInit | undefined
  const logs: string[] = []
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async (_input, nextInit) => {
      init = nextInit
      const value = deployment()
      return Response.json({ deployment: value, absoluteUrl: value.publicUrl }, { status: 201 })
    }) as FetchLike,
  })

  const result = await publishWorkspace(http, 'project delivery', {
    deploymentId,
    environment: 'preview',
    environmentRef: 'preview/default',
    workspaceRevision,
    buildManifestId,
    qualityRunId,
    message: 'Preview release',
  }, {
    ifMatch: `"deployment:${deploymentId}:1"`,
    idempotencyKey: 'publish-idempotency',
    onLog: (message) => logs.push(message),
  })

  const headers = new Headers(init?.headers)
  assert.equal(headers.get('x-csrf-token'), 'csrf-delivery')
  assert.equal(headers.get('idempotency-key'), 'publish-idempotency')
  assert.equal(headers.get('if-match'), `"deployment:${deploymentId}:1"`)
  assert.deepEqual(JSON.parse(String(init?.body)), {
    deploymentId,
    environment: 'preview',
    environmentRef: 'preview/default',
    workspaceRevision: {
      artifactId: workspaceRevision.artifactId,
      revisionId: workspaceRevision.revisionId,
      contentHash: workspaceRevision.contentHash,
    },
    buildManifestId,
    qualityRunId,
    message: 'Preview release',
  })
  assert.equal(result.deployment.deploymentId, deploymentId)
  assert.equal(result.deployment.publicPath, result.absoluteUrl)
  assert.equal(result.deployment.versions[0].environment, 'preview')
  assert.match(logs[0], new RegExp(deploymentVersionId))
})

test('deployment history hydrates exact versions and rollback carries current ETag', async () => {
  const calls: Array<{ readonly url: string; readonly init?: RequestInit }> = []
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async (input, init) => {
      const url = input.toString()
      calls.push({ url, init })
      if (url.endsWith('/rollback')) {
        return Response.json({ deployment: deployment(), absoluteUrl: deployment().publicUrl })
      }
      if (url.endsWith(`/v1/deployments/${deploymentId}`)) {
        return Response.json({ deployment: deployment() })
      }
      return Response.json({ deployments: [deployment(false)] })
    }) as FetchLike,
  })

  const history = await listDeployments(http, 'project delivery')
  assert.equal(history.length, 1)
  assert.equal(history[0].versions[0].id, deploymentVersionId)
  assert.deepEqual(calls.slice(0, 2).map((call) => new URL(call.url).pathname), [
    '/v1/projects/project%20delivery/deployments',
    `/v1/deployments/${deploymentId}`,
  ])

  const rolledBack = await rollbackDeployment(
    http,
    deploymentId,
    deploymentVersionId,
    {
      ifMatch: history[0].etag,
      message: 'Restore the reviewed preview',
      idempotencyKey: 'rollback-idempotency',
    },
  )
  assert.equal(rolledBack.deploymentId, deploymentId)
  const rollback = calls.at(-1)
  assert.equal(new URL(rollback!.url).pathname, `/v1/deployments/${deploymentId}/rollback`)
  const headers = new Headers(rollback?.init?.headers)
  assert.equal(headers.get('if-match'), history[0].etag)
  assert.equal(headers.get('idempotency-key'), 'rollback-idempotency')
  assert.deepEqual(JSON.parse(String(rollback?.init?.body)), {
    targetVersionId: deploymentVersionId,
    message: 'Restore the reviewed preview',
  })
})

test('delivery RFC problems preserve actionable conflict metadata', async () => {
  const client = new DeliveryClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    csrfTokenStore: tokenStore(),
    fetch: (async () => Response.json({
      type: 'urn:worksflow:problem:etag_mismatch',
      title: 'Precondition failed',
      status: 412,
      detail: 'Reload the current deployment before publishing.',
      code: 'etag_mismatch',
    }, { status: 412 })) as FetchLike,
  }))

  await assert.rejects(
    client.publish('project', {
      environment: 'preview',
      workspaceRevision,
      buildManifestId,
      qualityRunId,
    }),
    (error: unknown) => (
      error instanceof DeliveryClientError &&
      error.code === 'etag_mismatch' &&
      error.status === 412
    ),
  )
})

test('legacy mutable workspace delivery calls fail closed', async () => {
  const workspace = createWorkspace({ id: 'legacy', name: 'Legacy', files: [] })
  await assert.rejects(
    exportWorkspaceArchive(workspace),
    (error: unknown) => (
      error instanceof DeliveryClientError &&
      error.code === 'frozen_delivery_source_required'
    ),
  )
  await assert.rejects(
    publishWorkspace(workspace, '<main>Legacy</main>', 'index.html'),
    (error: unknown) => (
      error instanceof DeliveryClientError &&
      error.code === 'frozen_delivery_source_required'
    ),
  )
  await assert.rejects(
    listDeployments('legacy'),
    (error: unknown) => (
      error instanceof DeliveryClientError &&
      error.code === 'platform_http_client_required'
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
    console.error(`${failed} delivery platform client test(s) failed.`)
    process.exitCode = 1
    return
  }
  console.log(`${tests.length} delivery platform client test(s) passed.`)
}

void main()
