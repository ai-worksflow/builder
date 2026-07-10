import { expect, test, type Page, type Route } from '@playwright/test'

const now = '2026-07-10T08:00:00Z'
const hash = (character: string) => character.repeat(64)

const user = {
  id: '11111111-1111-4111-8111-111111111111',
  displayName: 'Flow Owner',
  email: 'owner@example.test',
  createdAt: now,
}

const reviewer = {
  id: '22222222-2222-4222-8222-222222222222',
  displayName: 'Flow Reviewer',
  email: 'reviewer@example.test',
  createdAt: now,
}

const project = {
  id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
  name: 'Server Application',
  lifecycle: 'active',
  currentUserRole: 'owner',
  createdBy: user.id,
  createdAt: now,
  updatedAt: now,
  etag: '"project:1"',
}

const briefRevision = {
  id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb3',
  artifactId: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
  revisionNumber: 3,
  contentHash: hash('b'),
  status: 'approved',
  content: {
    kind: 'requirement',
    summary: 'Build a server-backed application.',
    blocks: [],
    acceptanceCriteria: [],
    openQuestions: [],
    assumptions: [],
  },
  createdBy: user.id,
  createdAt: now,
}

const projectBrief = versionedArtifact(
  'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
  'project_brief',
  'Project Brief',
  briefRevision,
)

const blueprintRevision = exactRevision(
  'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
  'cccccccc-cccc-4ccc-8ccc-ccccccccccc2',
  2,
  hash('c'),
)

const pageSpecRevision = {
  id: 'dddddddd-dddd-4ddd-8ddd-ddddddddddd2',
  artifactId: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
  revisionNumber: 2,
  contentHash: hash('d'),
  status: 'approved',
  content: {
    blueprintPageNodeId: 'page-dashboard',
    title: 'Dashboard',
    route: '/dashboard',
    userGoal: 'Inspect work.',
    entryPoints: [],
    exitPoints: [],
    requiredRoles: [],
    states: [{
      id: 'state-ready',
      key: 'ready',
      title: 'Ready',
      required: true,
      fixtureIds: [],
      acceptanceCriterionIds: [],
    }],
    dataBindings: [],
    interactions: [],
    acceptanceCriterionIds: [],
    nonFunctionalConstraints: [],
  },
  createdBy: user.id,
  createdAt: now,
}

const pageSpec = versionedArtifact(
  'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
  'page_spec',
  'Dashboard PageSpec',
  pageSpecRevision,
)

const approvedPrototypeRevision = {
  id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee2',
  artifactId: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
  revisionNumber: 2,
  contentHash: hash('e'),
  status: 'approved',
  content: prototypeContent(),
  createdBy: user.id,
  createdAt: now,
}

const approvedPrototype = {
  ...versionedArtifact(
    'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
    'prototype',
    'Dashboard Prototype',
    approvedPrototypeRevision,
  ),
  draft: {
    id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeed',
    artifactId: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
    baseRevisionId: approvedPrototypeRevision.id,
    sourceVersions: [],
    revision: 3,
    content: prototypeContent(),
    contentHash: hash('0'),
    updatedBy: user.id,
    updatedAt: now,
    etag: '"prototype-draft:3"',
  },
}

const workflowDefinition = {
  id: 'ffffffff-ffff-4fff-8fff-ffffffffffff',
  versionId: 'ffffffff-ffff-4fff-8fff-fffffffffff2',
  projectId: project.id,
  key: 'minimum-application-loop',
  title: 'Minimum application loop',
  description: 'Brief to application.',
  published: true,
  version: 2,
  contentHash: hash('f'),
  definition: {
    id: 'ffffffff-ffff-4fff-8fff-ffffffffffff',
    version: 2,
    name: 'Minimum application loop',
    schemaVersion: 'workflow/v2',
    nodes: [{
      id: 'brief-input',
      name: 'Pinned Project Brief',
      type: 'artifact_input',
      artifactInput: { allowedTypes: ['document'], requireApproved: true, minimumArtifacts: 1 },
    }],
    edges: [],
    hash: hash('f'),
    createdBy: user.id,
    createdAt: now,
  },
}

interface MockPlatformOptions {
  readonly authenticated?: boolean
  readonly prototypes?: 'approved' | 'none'
  readonly historicalRun?: boolean
}

interface MockPlatformState {
  requests: Array<{ method: string; path: string; body: unknown }>
  prototypes: unknown[]
  run: ReturnType<typeof workflowRun> | null
  proposal: ReturnType<typeof implementationProposal> | null
  workspaceRevision: ReturnType<typeof applicationRevision> | null
}

async function installPlatformMock(page: Page, options: MockPlatformOptions = {}) {
  const state: MockPlatformState = {
    requests: [],
    prototypes: options.prototypes === 'none' ? [] : [approvedPrototype],
    run: options.historicalRun ? workflowRun('run-history', 'waiting_input') : null,
    proposal: null,
    workspaceRevision: null,
  }
  await page.route('**/api/platform/v1/**', async (route) => {
    await handlePlatformRoute(route, state, options)
  })
  await page.route('**/v1/**', async (route) => {
    await handlePlatformRoute(route, state, options)
  })
  return state
}

async function handlePlatformRoute(
  route: Route,
  state: MockPlatformState,
  options: MockPlatformOptions,
) {
  const request = route.request()
  const url = new URL(request.url())
  const path = url.pathname.replace(/^\/api\/platform/, '')
  const method = request.method()
  const body = request.postDataJSON?.() ?? undefined
  state.requests.push({ method, path, body })
  if (method === 'OPTIONS') {
    await route.fulfill({ status: 204, headers: corsHeaders() })
    return
  }
  const respond = (data: unknown, status = 200, headers: Record<string, string> = {}) =>
    route.fulfill({
      status,
      contentType: 'application/json',
      headers: { ...corsHeaders(), ...headers },
      body: JSON.stringify(data),
    })

  if (path === '/v1/session') {
    await respond(options.authenticated === false
      ? { state: 'anonymous' }
      : { state: 'authenticated', user, sessionId: 'session-1', expiresAt: '2026-07-11T08:00:00Z', csrfToken: 'csrf-e2e' })
    return
  }
  if (path === '/v1/projects') {
    await respond({ items: options.authenticated === false ? [] : [project] })
    return
  }
  if (path === `/v1/projects/${project.id}`) {
    await respond(project)
    return
  }
  if (path.endsWith('/authorize')) {
    await respond({ projectId: project.id, action: url.searchParams.get('action'), allowed: true, role: 'owner' })
    return
  }
  if (path.endsWith('/members')) {
    await respond({ items: [
      { projectId: project.id, user, role: 'owner', joinedAt: now, etag: '"member:owner"' },
      { projectId: project.id, user: reviewer, role: 'admin', joinedAt: now, etag: '"member:reviewer"' },
    ] })
    return
  }
  if (path.endsWith('/comments') || path.endsWith('/reviews') ||
    path.endsWith('/notifications') || path.endsWith('/presence') ||
    path.endsWith('/audit') || path.endsWith('/traces') ||
    path.endsWith('/proposals')) {
    if (method === 'POST' && path.endsWith('/reviews')) {
      await respond({ id: 'review-1', decision: 'pending' }, 201)
    } else if (method === 'POST' && path.endsWith('/presence')) {
      await respond({ projectId: project.id, user, state: 'active', updatedAt: now })
    } else {
      await respond({ items: [] })
    }
    return
  }
  if (path === `/v1/projects/${project.id}/artifacts`) {
    await respond({ items: [projectBrief.artifact, pageSpec.artifact, ...state.prototypes.map((value) => (value as typeof approvedPrototype).artifact)] })
    return
  }
  if (path === `/v1/projects/${project.id}/documents`) {
    await respond({ items: [projectBrief] })
    return
  }
  if (path === `/v1/projects/${project.id}/blueprints`) {
    await respond({ items: [] })
    return
  }
  if (path === `/v1/projects/${project.id}/page-specs`) {
    await respond({ items: [pageSpec] })
    return
  }
  if (path === `/v1/projects/${project.id}/prototypes` && method === 'GET') {
    await respond({ items: state.prototypes })
    return
  }
  if (path === `/v1/projects/${project.id}/prototypes` && method === 'POST') {
    const created = draftPrototype('prototype-created')
    state.prototypes = [created]
    await respond(created, 201, { etag: '"prototype-created:1"' })
    return
  }
  if (/\/v1\/prototypes\/[^/]+\/draft$/.test(path) && method === 'PATCH') {
    const input = body as { content: ReturnType<typeof prototypeContent> }
    const current = (state.prototypes[0] as ReturnType<typeof draftPrototype>) ?? draftPrototype('prototype-created')
    const next = {
      ...current,
      draft: {
        ...current.draft,
        revision: current.draft.revision + 1,
        content: input.content,
        contentHash: hash('9'),
        etag: `"prototype-draft:${current.draft.revision + 1}"`,
      },
    }
    state.prototypes = [next]
    await respond(next, 200, { etag: next.draft.etag })
    return
  }
  if (/\/v1\/prototypes\/[^/]+\/revisions$/.test(path) && method === 'POST') {
    const current = state.prototypes[0] as ReturnType<typeof draftPrototype>
    const revision = {
      id: 'prototype-created-r1',
      artifactId: current.artifact.id,
      revisionNumber: 1,
      contentHash: hash('8'),
      status: 'in_review',
      content: current.draft.content,
      createdBy: user.id,
      createdAt: now,
    }
    state.prototypes = [{ ...current, latestRevision: revision }]
    await respond(revision, 201)
    return
  }
  if (/\/v1\/artifacts\/[^/]+\/(revisions|dependencies)$/.test(path)) {
    await respond({ items: [] })
    return
  }
  if (/\/v1\/artifacts\/[^/]+\/review-gate$/.test(path)) {
    await respond({ passed: false, checks: [], unresolvedBlockingCommentIds: [], traceCoverage: 1 })
    return
  }
  if (path === `/v1/projects/${project.id}/workflow-definitions`) {
    await respond({ items: [workflowDefinition], total: 1 })
    return
  }
  if (path.endsWith(`/workflow-definitions/${workflowDefinition.id}/versions`)) {
    await respond({ items: [workflowDefinition], total: 1 })
    return
  }
  if (path === `/v1/projects/${project.id}/workflow-runs` && method === 'GET') {
    await respond({ items: state.run ? [state.run] : [], total: state.run ? 1 : 0 })
    return
  }
  if (path === `/v1/projects/${project.id}/input-manifests` && method === 'POST') {
    await respond({
      id: 'manifest-1',
      projectId: project.id,
      jobType: 'workflow_start',
      sources: (body as { sources: unknown[] }).sources,
      constraints: {},
      outputSchemaVersion: 'workflow-input/v1',
      createdBy: user.id,
      createdAt: now,
      hash: hash('1'),
    }, 201)
    return
  }
  if (path === `/v1/projects/${project.id}/workflow-runs` && method === 'POST') {
    state.run = workflowRun('run-started', 'waiting_input')
    await respond(state.run, 201)
    return
  }
  if (/\/workflow-runs\/[^/]+\/events$/.test(path)) {
    await respond({ items: state.run ? [{ id: 'event-1', runId: state.run.id, sequence: 1, type: 'run.created', payload: {}, createdAt: now }] : [] })
    return
  }
  if (/\/workflow-runs\/[^/]+$/.test(path) && method === 'GET') {
    await respond(state.run ?? workflowRun('run-started', 'waiting_input'))
    return
  }
  if (path === `/v1/projects/${project.id}/build-manifests` && method === 'POST') {
    await respond(buildManifest(), 201)
    return
  }
  if (path === '/v1/build-manifests/build-1' && method === 'GET') {
    await respond(buildManifest())
    return
  }
  if (path === '/v1/build-manifests/build-1/generate' && method === 'POST') {
    state.proposal = implementationProposal('open')
    await respond({ proposal: state.proposal, provider: 'openai', model: 'gpt-5' }, 201)
    return
  }
  if (path === '/v1/implementation-proposals/implementation-1/decisions' && method === 'POST') {
    state.proposal = implementationProposal('ready', 'accepted', 2)
    await respond(state.proposal, 200, { etag: '"implementation-proposal:implementation-1:2"' })
    return
  }
  if (path === '/v1/implementation-proposals/implementation-1/apply' && method === 'POST') {
    state.workspaceRevision = applicationRevision()
    state.proposal = implementationProposal('applied', 'applied', 3)
    await respond(state.workspaceRevision)
    return
  }
  if (path === '/v1/implementation-proposals/implementation-1' && method === 'GET') {
    await respond(state.proposal ?? implementationProposal('open'))
    return
  }
  if (path === '/v1/revisions/workspace-r1' && method === 'GET') {
    await respond(state.workspaceRevision ?? applicationRevision())
    return
  }

  await respond({ type: 'about:blank', title: 'Not found', status: 404, detail: path }, 404)
}

test('anonymous Workbench fails closed without browser generation fallback', async ({ page }) => {
  await installPlatformMock(page, { authenticated: false, prototypes: 'none' })
  await page.goto('/workbench/planning?view=code')

  await expect(page.getByText('Sign in to use the application Workbench')).toBeVisible()
  await expect(page.getByText('Workbench does not generate from browser mock data.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Freeze build input' })).toBeDisabled()
})

test('workflow run history and exact Project Brief start are server-backed', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, historicalRun: true })
  await page.goto('/workbench/planning?view=code')

  await expect(page.getByText('Server run history')).toBeVisible()
  await expect(page.getByText('run-history', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Start from approved Project Brief' }).click()
  await expect(page.getByText('run-started', { exact: true }).first()).toBeVisible()

  const manifestRequest = state.requests.find((item) => item.method === 'POST' && item.path.endsWith('/input-manifests'))
  expect(manifestRequest?.body).toMatchObject({
    jobType: 'workflow_start',
    sources: [{
      ref: {
        artifactId: projectBrief.artifact.id,
        revisionId: briefRevision.id,
        contentHash: briefRevision.contentHash,
      },
      purpose: 'project_brief',
    }],
  })
  const runRequest = state.requests.find((item) => item.method === 'POST' && item.path.endsWith('/workflow-runs'))
  expect(runRequest?.body).toMatchObject({
    definitionVersionId: workflowDefinition.versionId,
    inputManifest: { id: 'manifest-1', hash: hash('1') },
  })
})

test('frozen build input produces a reviewed proposal and applied preview', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto('/workbench/complete?view=code')

  await page.getByRole('button', { name: 'Freeze build input' }).click()
  await expect(page.getByText('Frozen application build manifest')).toBeVisible()
  await page.getByRole('button', { name: 'Generate proposal' }).click()
  await expect(page.getByText('src/index.html', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Accept pending' }).click()
  await expect(page.getByText('ready', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Apply accepted operations' }).click()

  await page.getByRole('button', { name: 'Preview', exact: true }).click()
  const preview = page.frameLocator('iframe[title="Canonical application preview"]')
  await expect(preview.getByText('SERVER_APPLIED_APPLICATION', { exact: true })).toBeVisible()
  expect(state.requests.some((item) => item.path.endsWith('/build-manifests/build-1/generate'))).toBe(true)
  expect(state.requests.some((item) => item.path.endsWith('/implementation-proposals/implementation-1/apply'))).toBe(true)
  expect(state.requests.some((item) => item.path.includes('/api/generate'))).toBe(false)
})

test('Prototype Studio creates from an approved PageSpec and persists an ETag draft', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, prototypes: 'none' })
  await page.goto(`/team/acme/project/${project.id}/prototype`)

  await expect(page.getByText('No prototype artifact yet.')).toBeVisible()
  await page.getByLabel('PageSpec source').selectOption(pageSpec.artifact.id)
  await page.getByPlaceholder('Prototype title').fill('Server-created Prototype')
  await page.getByRole('button', { name: 'Create server prototype' }).click()
  await expect(page.getByText('Server-created Prototype')).toBeVisible()

  await page.getByTitle('Add Heading').click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'PATCH' && item.path.endsWith('/prototype-created/draft'),
  )).toBe(true)
  const createRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/prototypes`,
  )
  expect(createRequest?.body).toMatchObject({
    pageSpecRevision: {
      artifactId: pageSpec.artifact.id,
      revisionId: pageSpecRevision.id,
      contentHash: pageSpecRevision.contentHash,
    },
    exploratory: false,
  })
})

test('Prototype revision requests canonical review from another project member', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/prototype`)

  await page.getByRole('button', { name: 'Revision + review' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST' && item.path.endsWith(`/projects/${project.id}/reviews`),
  ), { message: JSON.stringify(state.requests) }).toBe(true)
  const reviewRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path.endsWith(`/projects/${project.id}/reviews`),
  )
  expect(reviewRequest?.body).toMatchObject({
    requiredReviewerIds: [reviewer.id],
    target: {
      artifactId: approvedPrototype.artifact.id,
    },
  })
})

function corsHeaders() {
  return {
    'access-control-allow-origin': 'http://127.0.0.1:3000',
    'access-control-allow-credentials': 'true',
    'access-control-allow-headers': 'Content-Type, X-Request-ID, X-CSRF-Token, Idempotency-Key, If-Match',
    'access-control-allow-methods': 'GET, POST, PATCH, DELETE, OPTIONS',
    'access-control-expose-headers': 'ETag, X-Request-ID, X-CSRF-Token',
  }
}

function versionedArtifact(
  id: string,
  kind: string,
  title: string,
  revision: Record<string, unknown>,
) {
  return {
    artifact: {
      id,
      projectId: project.id,
      kind,
      title,
      status: 'approved',
      latestRevisionId: revision.id,
      approvedRevisionId: revision.id,
      createdBy: user.id,
      createdAt: now,
      updatedAt: now,
      etag: `"artifact:${id}:1"`,
    },
    latestRevision: revision,
    approvedRevision: revision,
  }
}

function exactRevision(artifactId: string, revisionId: string, revisionNumber: number, contentHash: string) {
  return { artifactId, revisionId, revisionNumber, contentHash }
}

function prototypeContent() {
  return {
    pageSpecRevision: exactRevision(pageSpecRevision.artifactId, pageSpecRevision.id, 2, pageSpecRevision.contentHash),
    exploratory: false,
    states: [{ id: 'prototype-state-ready', key: 'ready', title: 'Ready', required: true, fixtureIds: [] }],
    breakpoints: [{ id: 'breakpoint-desktop', name: 'Desktop', minWidth: 1024, viewportWidth: 900, viewportHeight: 600 }],
    layers: {
      'layer-root': {
        id: 'layer-root',
        childIds: [],
        kind: 'frame',
        name: 'Page',
        layout: { x: 0, y: 0, width: 900, height: 600 },
        style: { fill: '#171719' },
        properties: {},
        requirementIds: [],
        acceptanceCriterionIds: [],
        fieldMetadata: {},
      },
    },
    frames: [{ id: 'frame-ready', stateId: 'prototype-state-ready', breakpointId: 'breakpoint-desktop', rootLayerId: 'layer-root', title: 'Ready' }],
    overrides: [],
    interactions: [],
    fixtures: [],
    tokenBindings: [],
    componentBindings: [],
    assets: [],
    traceLinks: [],
  }
}

function draftPrototype(id: string) {
  return {
    artifact: {
      id,
      projectId: project.id,
      kind: 'prototype',
      title: 'Server-created Prototype',
      status: 'draft',
      activeDraftId: `${id}-draft`,
      createdBy: user.id,
      createdAt: now,
      updatedAt: now,
      etag: `"artifact:${id}:1"`,
    },
    draft: {
      id: `${id}-draft`,
      artifactId: id,
      sourceVersions: [],
      revision: 1,
      content: prototypeContent(),
      contentHash: hash('7'),
      updatedBy: user.id,
      updatedAt: now,
      etag: `"prototype-draft:1"`,
    },
  }
}

function workflowRun(id: string, status: 'waiting_input' | 'running') {
  return {
    id,
    projectId: project.id,
    definitionVersionId: workflowDefinition.versionId,
    definition: { id: workflowDefinition.id, version: 2, hash: workflowDefinition.contentHash },
    inputManifest: { id: 'manifest-1', hash: hash('1') },
    status,
    scope: {},
    context: { values: {}, nodes: {}, slices: {} },
    eventCursor: 1,
    startedBy: user.id,
    createdAt: now,
    updatedAt: now,
    nodes: [{
      id: `${id}-input`,
      runId: id,
      key: 'brief-input',
      definitionNodeId: 'brief-input',
      type: 'artifact_input',
      status: 'waiting_input',
      attempt: 0,
      availableAt: now,
      createdAt: now,
      updatedAt: now,
    }],
  }
}

function buildManifest() {
  return {
    id: 'build-1',
    projectId: project.id,
    pageSpecRevision: exactRevision(pageSpecRevision.artifactId, pageSpecRevision.id, 2, pageSpecRevision.contentHash),
    prototypeRevision: exactRevision(approvedPrototypeRevision.artifactId, approvedPrototypeRevision.id, 2, approvedPrototypeRevision.contentHash),
    requirementRevisions: [exactRevision(briefRevision.artifactId, briefRevision.id, 3, briefRevision.contentHash)],
    blueprintRevision,
    contractRevisions: [],
    designSystemRevisions: [],
    sceneGraph: asset('scene'),
    renderedFrames: [],
    interactionManifest: asset('interactions'),
    fixtureBundle: asset('fixtures'),
    tokenManifest: asset('tokens'),
    componentMapping: asset('components'),
    traceMatrix: asset('trace'),
    acceptanceManifest: asset('acceptance'),
    assumptions: [],
    waivers: [],
    createdBy: user.id,
    createdAt: now,
    contentHash: hash('6'),
  }
}

function asset(id: string) {
  return { assetId: id, contentHash: hash('5'), mediaType: 'application/json', byteSize: 20 }
}

function implementationProposal(
  status: 'open' | 'ready' | 'applied',
  decision: 'pending' | 'accepted' | 'applied' = 'pending',
  version = 1,
) {
  return {
    id: 'implementation-1',
    projectId: project.id,
    buildManifestId: 'build-1',
    operations: [{
      id: 'operation-1',
      kind: 'file.upsert',
      path: 'src/index.html',
      content: '<!doctype html><html><body><h1>SERVER_APPLIED_APPLICATION</h1></body></html>',
      language: 'html',
      dependsOn: [],
      rationale: 'Render the approved flow.',
      traceSource: [approvedPrototypeRevision.id],
      decision,
    }],
    routes: [],
    apis: [],
    migrations: [],
    tests: [],
    previews: [],
    traceLinks: [],
    diagnostics: [],
    assumptions: [],
    unimplementedItems: [],
    status,
    version,
    payloadHash: hash('4'),
    createdBy: user.id,
    createdAt: now,
  }
}

function applicationRevision() {
  return {
    id: 'workspace-r1',
    artifactId: 'workspace-1',
    revisionNumber: 1,
    contentHash: hash('3'),
    status: 'approved',
    content: {
      schemaVersion: 1,
      id: 'workspace-1',
      name: 'Application Workspace',
      revision: 1,
      createdAt: now,
      updatedAt: now,
      files: [{
        path: 'src/index.html',
        content: '<!doctype html><html><body><h1>SERVER_APPLIED_APPLICATION</h1></body></html>',
        language: 'html',
        contentHash: hash('2'),
      }],
    },
    createdBy: user.id,
    createdAt: now,
  }
}
