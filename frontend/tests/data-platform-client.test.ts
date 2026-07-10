import assert from 'node:assert/strict'
import { PlatformClient } from '../lib/platform/client'
import { DataRuntimeClient } from '../lib/platform/data-client'
import {
  HttpClient,
  PlatformHttpError,
  PlatformNetworkError,
  PlatformProtocolError,
  type FetchLike,
} from '../lib/platform/http'

type TestCase = {
  readonly name: string
  readonly run: () => void | Promise<void>
}

const tests: TestCase[] = []

function test(name: string, run: TestCase['run']) {
  tests.push({ name, run })
}

function json(data: unknown, status = 200, headers?: HeadersInit) {
  return Response.json(data, { status, headers })
}

test('data client uses the canonical Go snapshot and paginated record routes', async () => {
  const requests: Array<{ readonly url: string; readonly method: string }> = []
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async (input, init) => {
      requests.push({ url: input.toString(), method: init?.method ?? 'GET' })
      if (input.toString().includes('/records')) {
        return json({ records: [], total: 0, limit: 25, offset: 50 })
      }
      return json({ project: { projectId: 'project/a', tables: [] } })
    }) as FetchLike,
  })
  const data = new DataRuntimeClient(http)

  await data.snapshot('project/a')
  await data.audit('project/a')
  await data.listTables('project/a')
  await data.getTable('project/a', 'table/b')
  await data.listRecords('project/a', 'table/b', { limit: 25, offset: 50 })
  await data.getRecord('project/a', 'table/b', 'record/c')
  await data.listMetadata('project/a', 'auth-users')
  await data.getMetadata('project/a', 'auth-users', 'user/d')
  await data.listVariables('project/a')

  assert.deepEqual(requests, [
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa',
      method: 'GET',
    },
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa/audit',
      method: 'GET',
    },
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa/tables',
      method: 'GET',
    },
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa/tables/table%2Fb',
      method: 'GET',
    },
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa/tables/table%2Fb/records?limit=25&offset=50',
      method: 'GET',
    },
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa/tables/table%2Fb/records/record%2Fc',
      method: 'GET',
    },
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa/metadata/auth-users',
      method: 'GET',
    },
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa/metadata/auth-users/user%2Fd',
      method: 'GET',
    },
    {
      url: 'https://platform.example.test/v1/data/projects/project%2Fa/variables',
      method: 'GET',
    },
  ])
})

test('every data mutation uses the Go contract and carries an idempotency key', async () => {
  const requests: Array<{
    readonly url: string
    readonly method: string
    readonly headers: Headers
    readonly body?: string
  }> = []
  let id = 0
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    requestIdFactory: () => `generated-${++id}`,
    fetch: (async (input, init) => {
      requests.push({
        url: input.toString(),
        method: init?.method ?? 'GET',
        headers: new Headers(init?.headers),
        body: typeof init?.body === 'string' ? init.body : undefined,
      })
      return json({
        table: {},
        record: {},
        item: {},
        variable: {},
        preview: {},
        migration: {},
        tables: [],
        project: {},
        connection: {},
        deleted: true,
        id: 'deleted-id',
      })
    }) as FetchLike,
  })
  const data = new DataRuntimeClient(http)

  await data.createTable('project-1', { name: 'tasks', columns: [] })
  await data.renameTable('project-1', 'table-1', 'work_items')
  await data.deleteTable('project-1', 'table-1')
  await data.createRecord('project-1', 'table-1', { values: { title: 'First' } })
  await data.updateRecord('project-1', 'table-1', 'record-1', { values: { done: true } })
  await data.deleteRecord('project-1', 'table-1', 'record-1')
  await data.createMetadata('project-1', 'auth-users', {
    email: 'user@example.test',
    status: 'active',
  })
  await data.updateMetadata('project-1', 'auth-users', 'user-1', { status: 'disabled' })
  await data.deleteMetadata('project-1', 'auth-users', 'user-1')
  await data.setVariable('project-1', {
    name: 'API_URL',
    scope: 'preview',
    kind: 'plain',
    value: 'https://api.example.test',
  })
  await data.deleteVariable('project-1', 'variable-1')
  await data.previewMigration('project-1', [{
    type: 'create-table',
    table: { name: 'events', columns: [] },
  }])
  await data.applyMigration('project-1', 'confirm_abcdefghijklmnopqrstuvwxyz')
  await data.connectSupabase('project-1', {
    endpoint: 'https://demo.supabase.co',
    key: 'server-key',
  })

  assert.deepEqual(
    requests.map((entry) => [entry.method, new URL(entry.url).pathname]),
    [
      ['POST', '/v1/data/projects/project-1/tables'],
      ['PATCH', '/v1/data/projects/project-1/tables/table-1'],
      ['DELETE', '/v1/data/projects/project-1/tables/table-1'],
      ['POST', '/v1/data/projects/project-1/tables/table-1/records'],
      ['PATCH', '/v1/data/projects/project-1/tables/table-1/records/record-1'],
      ['DELETE', '/v1/data/projects/project-1/tables/table-1/records/record-1'],
      ['POST', '/v1/data/projects/project-1/metadata/auth-users'],
      ['PATCH', '/v1/data/projects/project-1/metadata/auth-users/user-1'],
      ['DELETE', '/v1/data/projects/project-1/metadata/auth-users/user-1'],
      ['POST', '/v1/data/projects/project-1/variables'],
      ['DELETE', '/v1/data/projects/project-1/variables/variable-1'],
      ['POST', '/v1/data/projects/project-1/migrations/preview'],
      ['POST', '/v1/data/projects/project-1/migrations/apply'],
      ['POST', '/v1/data/connect/supabase'],
    ],
  )
  for (const request of requests) {
    assert.ok(request.headers.get('idempotency-key'), `${request.method} ${request.url}`)
  }
  assert.equal(
    requests.at(-1)?.headers.get('x-worksflow-project-id'),
    'project-1',
  )
  assert.deepEqual(JSON.parse(requests[3].body ?? '{}'), {
    values: { title: 'First' },
  })
  assert.deepEqual(JSON.parse(requests[11].body ?? '{}'), {
    operations: [{ type: 'create-table', table: { name: 'events', columns: [] } }],
  })
})

test('data mutations share the authenticated PlatformClient CSRF store', async () => {
  const mutationHeaders: Headers[] = []
  let call = 0
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async (_, init) => {
      call += 1
      if (call === 1) {
        return json({
          state: 'authenticated',
          csrfToken: 'csrf-from-session',
          user: { id: 'user-1' },
          expiresAt: '2026-07-11T00:00:00Z',
        })
      }
      mutationHeaders.push(new Headers(init?.headers))
      return json({ table: { id: 'table-1' } }, 201)
    }) as FetchLike,
  })
  const platform = new PlatformClient({ httpClient: http })

  await platform.session.get()
  await platform.data.createTable('project-1', { name: 'tasks' })

  assert.equal(mutationHeaders[0].get('x-csrf-token'), 'csrf-from-session')
  assert.ok(mutationHeaders[0].get('idempotency-key'))
})

test('public data management uses canonical routes, exact policy bodies and idempotency', async () => {
  const requests: Array<{
    readonly path: string
    readonly method: string
    readonly headers: Headers
    readonly body?: string
  }> = []
  const now = '2026-07-10T08:00:00.000Z'
  const policy = {
    projectId: 'project-1',
    tableId: 'table-1',
    tableName: 'tasks',
    allowRead: true,
    allowCreate: true,
    allowUpdate: false,
    allowDelete: false,
    readableFields: ['done', 'title'],
    writableFields: ['title'],
    version: 3,
    etag: '"public-data-policy:project-1:table-1:3"',
    createdAt: now,
    updatedAt: now,
  }
  const runtime = {
    apiBasePath: '/v1/public/data/deployments',
    projectId: 'project-1',
    deploymentId: 'deployment-1',
    deploymentVersionId: 'deployment-version-1',
    capabilityId: 'capability-1',
    allowedOrigins: ['https://app.example.test'],
    expiresAt: '2026-07-17T08:00:00.000Z',
    activatedAt: now,
  }
  const http = new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async (input, init) => {
      const url = new URL(input.toString())
      const method = init?.method ?? 'GET'
      requests.push({
        path: url.pathname,
        method,
        headers: new Headers(init?.headers),
        body: typeof init?.body === 'string' ? init.body : undefined,
      })
      if (url.pathname.endsWith('/policies') && method === 'GET') {
        return json({ policies: [policy] })
      }
      if (url.pathname.endsWith('/policies/table-1') && method === 'PUT') {
        return json({ policy })
      }
      if (url.pathname.endsWith('/policies/table-1')) {
        return json({ deleted: true, tableId: 'table-1' })
      }
      if (method === 'GET') return json({ runtime })
      return json({ revoked: true, deploymentId: 'deployment-1' })
    }) as FetchLike,
  })
  const publicData = new DataRuntimeClient(http).publicRuntime

  assert.deepEqual(await publicData.listPolicies('project-1'), [policy])
  await assert.rejects(
    publicData.putPolicy('project-1', 'table-1', {
      allowRead: false,
      allowCreate: false,
      allowUpdate: false,
      allowDelete: false,
      readableFields: [],
      writableFields: [],
    }),
    PlatformProtocolError,
  )
  await assert.rejects(
    publicData.deletePolicy('project-1', 'table-1'),
    PlatformProtocolError,
  )
  assert.deepEqual(await publicData.putPolicy('project-1', 'table-1', {
    allowRead: true,
    allowCreate: true,
    allowUpdate: false,
    allowDelete: false,
    readableFields: ['done', 'title'],
    writableFields: ['title'],
  }, { ifMatch: policy.etag }), policy)
  assert.deepEqual(await publicData.deletePolicy(
    'project-1',
    'table-1',
    { ifMatch: policy.etag },
  ), {
    deleted: true,
    tableId: 'table-1',
  })
  assert.deepEqual(
    await publicData.activeDeploymentRuntime('project-1', 'deployment-1'),
    runtime,
  )
  assert.deepEqual(
    await publicData.revokeDeploymentRuntime('project-1', 'deployment-1'),
    { revoked: true, deploymentId: 'deployment-1' },
  )

  assert.deepEqual(requests.map(({ method, path }) => [method, path]), [
    ['GET', '/v1/data/projects/project-1/public-runtime/policies'],
    ['PUT', '/v1/data/projects/project-1/public-runtime/policies/table-1'],
    ['DELETE', '/v1/data/projects/project-1/public-runtime/policies/table-1'],
    ['GET', '/v1/data/projects/project-1/public-runtime/deployments/deployment-1'],
    ['DELETE', '/v1/data/projects/project-1/public-runtime/deployments/deployment-1'],
  ])
  assert.deepEqual(JSON.parse(requests[1].body ?? '{}'), {
    allowRead: true,
    allowCreate: true,
    allowUpdate: false,
    allowDelete: false,
    readableFields: ['done', 'title'],
    writableFields: ['title'],
  })
  for (const request of requests.filter(({ method }) => method !== 'GET')) {
    assert.ok(request.headers.get('idempotency-key'), `${request.method} ${request.path}`)
  }
  assert.equal(requests[1].headers.get('if-match'), policy.etag)
  assert.equal(requests[2].headers.get('if-match'), policy.etag)
})

test('public data management rejects cross-project DTOs and leaked capability tokens', async () => {
  const now = '2026-07-10T08:00:00.000Z'
  let call = 0
  const publicData = new DataRuntimeClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async () => {
      call += 1
      if (call === 1) {
        return json({
          policies: [{
            projectId: 'another-project',
            tableId: 'table-1',
            tableName: 'tasks',
            allowRead: false,
            allowCreate: false,
            allowUpdate: false,
            allowDelete: false,
            readableFields: [],
            writableFields: [],
            version: 1,
            etag: '"public-data-policy:another-project:table-1:1"',
            createdAt: now,
            updatedAt: now,
          }],
        })
      }
      return json({
        runtime: {
          apiBasePath: '/v1/public/data/deployments',
          projectId: 'project-1',
          deploymentId: 'deployment-1',
          deploymentVersionId: 'deployment-version-1',
          capabilityId: 'capability-1',
          capabilityToken: 'wfpub_must-never-reach-builder-clients',
          allowedOrigins: ['https://app.example.test'],
          expiresAt: '2026-07-17T08:00:00.000Z',
          activatedAt: now,
        },
      })
    }) as FetchLike,
  })).publicRuntime

  await assert.rejects(
    publicData.listPolicies('project-1'),
    PlatformProtocolError,
  )
  await assert.rejects(
    publicData.activeDeploymentRuntime('project-1', 'deployment-1'),
    PlatformProtocolError,
  )
})

test('RFC 9457 data failures stay structured and network failure has no fallback request', async () => {
  const conflict = new DataRuntimeClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async () => json({
      type: 'urn:worksflow:problem:conflict',
      title: 'Conflict',
      status: 409,
      detail: 'Table tasks already exists',
      code: 'conflict',
      errors: { name: ['name already exists'] },
    }, 409, { 'content-type': 'application/problem+json' })) as FetchLike,
  }))
  await assert.rejects(
    conflict.createTable('project-1', { name: 'tasks' }),
    (error: unknown) => {
      assert.ok(error instanceof PlatformHttpError)
      assert.equal(error.status, 409)
      assert.equal(error.problem.detail, 'Table tasks already exists')
      assert.deepEqual(error.problem.errors, { name: ['name already exists'] })
      return true
    },
  )

  let calls = 0
  const offline = new DataRuntimeClient(new HttpClient({
    baseUrl: 'https://platform.example.test',
    fetch: (async () => {
      calls += 1
      throw new TypeError('connection refused')
    }) as FetchLike,
  }))
  await assert.rejects(offline.snapshot('project-1'), PlatformNetworkError)
  assert.equal(calls, 1)
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
    console.error(`${failed} data platform client test(s) failed.`)
    process.exitCode = 1
    return
  }
  console.log(`${tests.length} data platform client test(s) passed.`)
}

void main()
