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

const blueprintContent = {
  nodes: [
    {
      id: 'feature-orders',
      key: 'FEATURE-ORDERS',
      kind: 'feature',
      title: 'Order management',
      description: 'Manage the complete order lifecycle.',
      position: { x: 60, y: 80 },
      requirementIds: ['REQ-ORDER-001'],
      assignedMemberIds: [],
    },
    {
      id: 'page-dashboard',
      key: 'PAGE-DASHBOARD',
      kind: 'page',
      title: 'Dashboard',
      description: 'Inspect order health.',
      route: '/dashboard',
      userGoal: 'Inspect work and resolve order exceptions.',
      position: { x: 300, y: 80 },
      requirementIds: ['REQ-ORDER-001'],
      pageSpecArtifactId: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
      assignedMemberIds: [],
    },
  ],
  edges: [{
    id: 'edge-feature-dashboard',
    sourceNodeId: 'feature-orders',
    targetNodeId: 'page-dashboard',
    kind: 'contains',
    required: true,
  }],
  semantic: {
    nodes: [
      {
        id: 'feature-orders',
        key: 'FEATURE-ORDERS',
        kind: 'feature',
        title: 'Order management',
        description: 'Manage the complete order lifecycle.',
        requirementIds: ['REQ-ORDER-001'],
        assignedMemberIds: [],
      },
      {
        id: 'page-dashboard',
        key: 'PAGE-DASHBOARD',
        kind: 'page',
        title: 'Dashboard',
        description: 'Inspect order health.',
        route: '/dashboard',
        userGoal: 'Inspect work and resolve order exceptions.',
        requirementIds: ['REQ-ORDER-001'],
        pageSpecArtifactId: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
        assignedMemberIds: [],
      },
    ],
    edges: [{
      id: 'edge-feature-dashboard',
      sourceNodeId: 'feature-orders',
      targetNodeId: 'page-dashboard',
      kind: 'contains',
      required: true,
    }],
  },
  layout: {
    nodePositions: {
      'feature-orders': { x: 60, y: 80 },
      'page-dashboard': { x: 300, y: 80 },
    },
    groups: [],
    viewport: { x: 0, y: 0, zoom: 1 },
  },
  pageSpecRefs: [],
  validation: [],
}

const fullBlueprintRevision = {
  id: blueprintRevision.revisionId,
  artifactId: blueprintRevision.artifactId,
  revisionNumber: blueprintRevision.revisionNumber,
  contentHash: blueprintRevision.contentHash,
  status: 'approved',
  content: blueprintContent,
  createdBy: user.id,
  createdAt: now,
}

const blueprint = versionedArtifact(
  blueprintRevision.artifactId,
  'blueprint',
  'Product Blueprint',
  fullBlueprintRevision,
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
    states: ['ready', 'loading', 'empty', 'error'].map((id) => ({
      id,
      key: id,
      title: id[0].toUpperCase() + id.slice(1),
      required: true,
      fixtureIds: [],
      acceptanceCriterionIds: ['AC-DASHBOARD-001'],
    })),
    dataBindings: [],
    interactions: [],
    acceptanceCriterionIds: ['AC-DASHBOARD-001'],
    nonFunctionalConstraints: ['Keyboard accessible'],
  },
  createdBy: user.id,
  createdAt: now,
}

const pageSpec = {
  ...versionedArtifact(
    'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
    'page_spec',
    'Dashboard PageSpec',
    pageSpecRevision,
  ),
  draft: {
    id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd-draft',
    artifactId: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
    baseRevisionId: pageSpecRevision.id,
    sourceVersions: [blueprintRevision],
    revision: 3,
    content: pageSpecRevision.content,
    contentHash: hash('7'),
    updatedBy: user.id,
    updatedAt: now,
    etag: '"page-spec-draft:3"',
  },
}

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
  readonly role?: 'owner' | 'commenter'
  readonly staleBriefDraft?: boolean
}

interface MockPlatformState {
  requests: Array<{
    method: string
    path: string
    body: unknown
    headers: Record<string, string>
  }>
  prototypes: unknown[]
  pageSpec: typeof pageSpec
  run: ReturnType<typeof workflowRun> | null
  proposal: ReturnType<typeof implementationProposal> | null
  workspaceRevision: ReturnType<typeof applicationRevision> | null
  workbenchBundle: ReturnType<typeof buildManifest>
  conversations: Array<ReturnType<typeof conversationRecord>>
  conversationMessages: Array<ReturnType<typeof conversationMessage>>
  intentProposal: MockWorkflowIntentProposal | null
  conversationCommand: ReturnType<typeof conversationCommand> | null
}

async function installPlatformMock(page: Page, options: MockPlatformOptions = {}) {
  const state: MockPlatformState = {
    requests: [],
    prototypes: options.prototypes === 'none' ? [] : [approvedPrototype],
    pageSpec: structuredClone(pageSpec),
    run: options.historicalRun ? workflowRun('run-history', 'waiting_input') : null,
    proposal: null,
    workspaceRevision: null,
    workbenchBundle: buildManifest(),
    conversations: [conversationRecord('33333333-3333-4333-8333-333333333333', 'Project discovery')],
    conversationMessages: [],
    intentProposal: null,
    conversationCommand: null,
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
  const headers = await request.allHeaders()
  state.requests.push({ method, path, body, headers })
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
    await respond({ items: options.authenticated === false ? [] : [{
      ...project,
      currentUserRole: options.role ?? 'owner',
    }] })
    return
  }
  if (path === `/v1/projects/${project.id}`) {
    await respond({ ...project, currentUserRole: options.role ?? 'owner' })
    return
  }
  if (path.endsWith('/authorization')) {
    const role = options.role ?? 'owner'
    const action = url.searchParams.get('action')
    await respond({
      projectId: project.id,
      action,
      allowed: role === 'owner' || action === 'view' || action === 'comment',
      role,
    })
    return
  }
  if (path.endsWith('/members')) {
    await respond({ items: [
      { projectId: project.id, user, role: options.role ?? 'owner', joinedAt: now, etag: '"member:owner"' },
      { projectId: project.id, user: reviewer, role: 'admin', joinedAt: now, etag: '"member:reviewer"' },
    ] })
    return
  }

  const conversationBase = `/v1/projects/${project.id}/conversations`
  if (path === conversationBase && method === 'GET') {
    await respond({ items: state.conversations })
    return
  }
  if (path === conversationBase && method === 'POST') {
    const input = body as { title: string }
    const created = conversationRecord(
      '44444444-4444-4444-8444-444444444444',
      input.title,
    )
    state.conversations = [created, ...state.conversations]
    await respond(created, 201, { etag: created.etag })
    return
  }
  const conversationItem = path.match(new RegExp(`^${conversationBase}/([^/]+)$`))
  if (conversationItem && method === 'GET') {
    const item = state.conversations.find((candidate) => candidate.id === conversationItem[1])
    await respond(item ?? { title: 'Not found' }, item ? 200 : 404, item ? { etag: item.etag } : {})
    return
  }
  if (conversationItem && method === 'PATCH') {
    const input = body as { title?: string; status?: 'active' | 'archived' }
    const current = state.conversations.find((candidate) => candidate.id === conversationItem[1])
    if (!current) {
      await respond({ title: 'Not found' }, 404)
      return
    }
    const updated = {
      ...current,
      ...(input.title ? { title: input.title } : {}),
      ...(input.status ? { status: input.status } : {}),
      version: current.version + 1,
      etag: `"conversation:${current.id}:${current.version + 1}"`,
      updatedAt: now,
      ...(input.status === 'archived' ? { archivedAt: now } : {}),
    }
    state.conversations = state.conversations.map((candidate) =>
      candidate.id === updated.id ? updated : candidate)
    await respond(updated, 200, { etag: updated.etag })
    return
  }

  const messagePath = path.match(new RegExp(`^${conversationBase}/([^/]+)/messages$`))
  if (messagePath && method === 'GET') {
    await respond({
      items: state.conversationMessages.filter((item) => item.conversationId === messagePath[1]),
    })
    return
  }
  if (messagePath && method === 'POST') {
    const input = body as { content: string }
    const sequence = state.conversationMessages.filter(
      (item) => item.conversationId === messagePath[1],
    ).length + 1
    const created = conversationMessage(
      `55555555-5555-4555-8555-55555555555${sequence}`,
      messagePath[1],
      sequence,
      'user',
      input.content,
    )
    state.conversationMessages = [...state.conversationMessages, created]
    await respond(created, 201)
    return
  }

  const intentListPath = path.match(new RegExp(`^${conversationBase}/([^/]+)/intent-proposals$`))
  if (intentListPath && method === 'GET') {
    await respond({ items: state.intentProposal ? [state.intentProposal] : [] })
    return
  }
  const intentGeneratePath = path.match(new RegExp(`^${conversationBase}/([^/]+)/intent-proposals/generate$`))
  if (intentGeneratePath && method === 'POST') {
    const input = body as GenerateIntentBody
    const proposal = workflowIntentProposal(intentGeneratePath[1], input)
    const assistant = conversationMessage(
      proposal.assistantMessageId,
      intentGeneratePath[1],
      state.conversationMessages.filter((item) => item.conversationId === intentGeneratePath[1]).length + 1,
      'assistant',
      'Start the published minimum application workflow from the approved Project Brief.',
      proposal.id,
    )
    state.intentProposal = proposal
    state.conversationMessages = [...state.conversationMessages, assistant]
    await respond({ proposal, message: assistant, provider: 'openai', model: input.model ?? 'gpt-5' }, 201, {
      etag: proposal.etag,
    })
    return
  }
  const intentDecisionPath = path.match(new RegExp(`^${conversationBase}/([^/]+)/intent-proposals/([^/]+)/decision$`))
  if (intentDecisionPath && method === 'POST' && state.intentProposal) {
    const input = body as { decision: 'accept' | 'reject'; reason?: string }
    state.intentProposal = {
      ...state.intentProposal,
      status: input.decision === 'accept' ? 'accepted' : 'rejected',
      version: 2,
      etag: `"intent-proposal:${state.intentProposal.id}:2"`,
      decisionReason: input.reason,
      decidedBy: user.id,
      decidedAt: now,
    }
    if (input.decision === 'accept') {
      state.conversationCommand = conversationCommand(state.intentProposal)
    }
    await respond({
      proposal: state.intentProposal,
      ...(state.conversationCommand ? { command: state.conversationCommand } : {}),
    }, 200, {
      etag: state.intentProposal.etag,
      ...(state.conversationCommand ? {
        'x-command-etag': state.conversationCommand.etag,
      } : {}),
    })
    return
  }

  const commandListPath = path.match(new RegExp(`^${conversationBase}/([^/]+)/commands$`))
  if (commandListPath && method === 'GET') {
    await respond({ items: state.conversationCommand ? [state.conversationCommand] : [] })
    return
  }
  const commandExecutePath = path.match(new RegExp(`^${conversationBase}/([^/]+)/commands/([^/]+)/execute$`))
  if (commandExecutePath && method === 'POST' && state.conversationCommand) {
    const workbenchResult = (body as {
      workbenchResult?: { runId: string; bundleId: string }
    })?.workbenchResult
    if (state.conversationCommand.kind === 'start_workflow') {
      state.run = workflowRun(state.conversationCommand.id, 'waiting_input')
    }
    state.conversationCommand = {
      ...state.conversationCommand,
      status: 'executed',
      version: 2,
      etag: `"conversation-command:${state.conversationCommand.id}:2"`,
      result: {
        ...(state.conversationCommand.kind === 'start_workflow' ? {
          runId: state.conversationCommand.id,
          definitionVersionId: state.conversationCommand.payload.definitionVersionId,
          inputManifest: state.conversationCommand.payload.manifestIntent.inputManifest,
        } : workbenchResult ?? {}),
      },
      executionActorId: user.id,
      executedBy: user.id,
      executedAt: now,
      updatedAt: now,
    }
    await respond(state.conversationCommand, 200, { etag: state.conversationCommand.etag })
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
    await respond({ items: [projectBrief.artifact, blueprint.artifact, state.pageSpec.artifact, ...state.prototypes.map((value) => (value as typeof approvedPrototype).artifact)] })
    return
  }
  if (path === `/v1/revisions/${briefRevision.id}`) {
    await respond(briefRevision)
    return
  }
  if (path === `/v1/revisions/${pageSpecRevision.id}`) {
    await respond(state.pageSpec.latestRevision ?? pageSpecRevision)
    return
  }
  if (path === `/v1/revisions/${blueprintRevision.revisionId}`) {
    await respond(fullBlueprintRevision)
    return
  }
  if (path === `/v1/revisions/${approvedPrototypeRevision.id}`) {
    await respond(approvedPrototypeRevision)
    return
  }
  if (path === `/v1/projects/${project.id}/documents`) {
    await respond({ items: [options.staleBriefDraft ? staleProjectBrief() : projectBrief] })
    return
  }
  if (path === `/v1/projects/${project.id}/blueprints`) {
    await respond({ items: [blueprint] })
    return
  }
  if (path === `/v1/projects/${project.id}/page-specs`) {
    await respond({ items: [state.pageSpec] })
    return
  }
  if (path === `/v1/page-specs/${pageSpec.artifact.id}/draft` && method === 'PATCH') {
    const input = body as {
      content: typeof pageSpecRevision.content
      sourceVersions?: typeof pageSpec.draft.sourceVersions
    }
    const nextDraftRevision = (state.pageSpec.draft?.revision ?? 3) + 1
    state.pageSpec = {
      ...state.pageSpec,
      draft: {
        ...state.pageSpec.draft!,
        revision: nextDraftRevision,
        content: input.content,
        sourceVersions: input.sourceVersions ?? state.pageSpec.draft!.sourceVersions,
        contentHash: hash('8'),
        updatedAt: now,
        etag: `"page-spec-draft:${nextDraftRevision}"`,
      },
    }
    await respond(state.pageSpec, 200, { etag: state.pageSpec.draft.etag })
    return
  }
  if (path === `/v1/page-specs/${pageSpec.artifact.id}/revisions` && method === 'POST') {
    const revision = {
      id: 'dddddddd-dddd-4ddd-8ddd-ddddddddddd4',
      artifactId: pageSpec.artifact.id,
      revisionNumber: 4,
      contentHash: state.pageSpec.draft!.contentHash,
      status: 'draft',
      content: state.pageSpec.draft!.content,
      sourceVersions: state.pageSpec.draft!.sourceVersions,
      createdBy: user.id,
      createdAt: now,
    }
    state.pageSpec = {
      ...state.pageSpec,
      artifact: {
        ...state.pageSpec.artifact,
        latestRevisionId: revision.id,
      },
      latestRevision: revision,
    }
    await respond(revision, 201)
    return
  }
  if (path === `/v1/artifacts/${pageSpec.artifact.id}/revisions`) {
    await respond({ items: [state.pageSpec.latestRevision] })
    return
  }
  if (path === `/v1/artifacts/${blueprint.artifact.id}/revisions`) {
    await respond({ items: [fullBlueprintRevision] })
    return
  }
  if (path === `/v1/projects/${project.id}/prototypes` && method === 'GET') {
    await respond({ items: state.prototypes })
    return
  }
  if (path === `/v1/projects/${project.id}/output-proposals` && method === 'GET') {
    await respond({ items: [] })
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
    const input = body as {
      jobType: string
      baseRevision?: unknown
      sources: unknown[]
      constraints: Record<string, unknown>
      outputSchemaVersion: string
    }
    await respond({
      id: 'manifest-1',
      projectId: project.id,
      jobType: input.jobType,
      baseRevision: input.baseRevision,
      sources: input.sources,
      constraints: input.constraints,
      outputSchemaVersion: input.outputSchemaVersion,
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
    await respond(state.workbenchBundle, 201)
    return
  }
  if (path === '/v1/build-manifests/build-1' && method === 'GET') {
    await respond(state.workbenchBundle)
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
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  await page.getByRole('button', { name: 'Start from exact Project Brief' }).click()
  await expect(page.getByText('run-started', { exact: true }).first()).toBeVisible()

  const manifestRequest = state.requests.find((item) => item.method === 'POST' && item.path.endsWith('/input-manifests'))
  expect(manifestRequest?.body).toMatchObject({
    jobType: 'workflow_start',
    baseRevision: {
      artifactId: projectBrief.artifact.id,
      revisionId: briefRevision.id,
      contentHash: briefRevision.contentHash,
    },
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

test('conversation turns an approved brief into an accepted command and opens its exact run', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto('/workbench/planning?view=code')

  const composer = page.getByPlaceholder('Describe requirements or a controlled next action…')
  await expect(composer).toBeVisible()
  await composer.fill('Build an order operations application from the approved brief.')
  await page.getByRole('button', { name: 'Send immutable message' }).click()
  await expect(page.getByText('Build an order operations application from the approved brief.')).toBeVisible()

  await expect.poll(() => state.requests.some((item) =>
    item.path.endsWith(`/workflow-definitions/${workflowDefinition.id}/versions`),
  )).toBe(true)
  await page.getByRole('button', { name: 'Generate governed intent' }).click()
  await expect(page.getByText('Start workflow', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Accept', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Execute and open run' })).toBeVisible()
  await page.getByRole('button', { name: 'Execute and open run' }).click()

  const commandId = '77777777-7777-4777-8777-777777777777'
  await expect(page).toHaveURL(new RegExp(`runId=${commandId}`))
  expect(state.run?.id).toBe(commandId)

  const messageRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path.endsWith('/messages'))
  expect(messageRequest?.headers['idempotency-key']).toBeTruthy()
  expect(messageRequest?.body).toEqual({
    content: 'Build an order operations application from the approved brief.',
  })

  const manifestRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path.endsWith('/input-manifests')
      && (item.body as { jobType?: string })?.jobType === 'conversation.workflow_intent')
  expect(manifestRequest?.body).toMatchObject({
    sources: [{
      ref: {
        artifactId: projectBrief.artifact.id,
        revisionId: briefRevision.id,
        contentHash: briefRevision.contentHash,
      },
      purpose: 'project_brief',
    }],
    outputSchemaVersion: 'workflow-intent-input/v1',
  })

  const generateRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path.endsWith('/intent-proposals/generate'))
  expect(generateRequest?.headers['idempotency-key']).toBeTruthy()
  expect(generateRequest?.body).toMatchObject({
    triggerMessageId: '55555555-5555-4555-8555-555555555551',
    candidateDefinitionVersionIds: [workflowDefinition.versionId],
    sourceRefs: [{
      artifactId: projectBrief.artifact.id,
      revisionId: briefRevision.id,
      contentHash: briefRevision.contentHash,
    }],
    manifestIntent: {
      mode: 'use_existing',
      inputManifest: { id: 'manifest-1', hash: hash('1') },
    },
  })
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path.includes(`/documents/${projectBrief.artifact.id}/revisions`),
  )).toBe(false)

  const decisionRequest = state.requests.find((item) => item.method === 'POST'
    && item.path.endsWith('/intent-proposals/66666666-6666-4666-8666-666666666666/decision'))
  expect(decisionRequest?.headers['if-match']).toBe('"intent-proposal:66666666-6666-4666-8666-666666666666:1"')
  expect(decisionRequest?.headers['idempotency-key']).toBeTruthy()

  const executeRequest = state.requests.find((item) => item.method === 'POST'
    && item.path.endsWith(`/commands/${commandId}/execute`))
  expect(executeRequest?.headers['if-match']).toBe(`"conversation-command:${commandId}:1"`)
  expect(executeRequest?.headers['idempotency-key']).toBeTruthy()
  expect(executeRequest?.body).toEqual({})
  expect(state.conversationCommand?.payload.scope).toMatchObject({
    conversationIntent: {
      workbenchInstruction: {
        objective: 'Start the minimum application workflow from the approved Project Brief.',
      },
    },
  })
  expect(state.conversationCommand?.payload.workbench.objective).toBe(
    'Start the minimum application workflow from the approved Project Brief.',
  )

  const executeIndex = state.requests.findIndex((item) => item === executeRequest)
  const exactRunLoadIndex = state.requests.findIndex((item, index) =>
    index > executeIndex && item.method === 'GET'
      && item.path.endsWith(`/workflow-runs/${commandId}`),
  )
  expect(exactRunLoadIndex).toBeGreaterThan(executeIndex)
})

test('conversation intent generation fails closed on unapproved Project Brief changes', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, staleBriefDraft: true })
  await page.goto('/workbench/planning?view=code')

  await expect.poll(() => state.requests.some((item) =>
    item.path === `/v1/projects/${project.id}/documents`,
  )).toBe(true)
  await page.getByPlaceholder('Describe requirements or a controlled next action…').fill('Generate from this brief.')
  await page.getByRole('button', { name: 'Send immutable message' }).click()
  await page.getByRole('button', { name: 'Generate governed intent' }).click()

  await expect(page.getByRole('complementary', { name: 'Conversation control plane' }).getByRole('alert'))
    .toContainText('Project Brief has unapproved changes')
  expect(state.requests.some((item) =>
    item.method === 'POST' && item.path.endsWith('/intent-proposals/generate'),
  )).toBe(false)
  expect(state.requests.some((item) =>
    item.method === 'POST' && item.path.endsWith('/input-manifests'),
  )).toBe(false)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path.includes(`/documents/${projectBrief.artifact.id}/revisions`),
  )).toBe(false)
})

test('Workbench conversation commands fail closed on drift and proceed only from exact inputs', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const conversationId = state.conversations[0].id
  const trigger = conversationMessage(
    '99999999-9999-4999-8999-999999999991',
    conversationId,
    1,
    'user',
    'Generate the next reviewed Workbench change.',
  )
  const proposal = workbenchIntentProposal(conversationId, trigger.id)
  const assistant = conversationMessage(
    proposal.assistantMessageId,
    conversationId,
    2,
    'assistant',
    'Generate an implementation proposal from the exact frozen bundle.',
    proposal.id,
  )
  state.conversationMessages = [trigger, assistant]
  state.intentProposal = proposal
  state.conversationCommand = conversationCommand(proposal)
  state.run = {
    ...workflowRun(proposal.workbenchInstruction.expectedRunId, 'waiting_input'),
    inputManifest: { id: 'manifest-drifted', hash: hash('9') },
  }

  await page.goto('/workbench/planning?view=code')
  const panel = page.getByRole('complementary', { name: 'Conversation control plane' })
  const execute = panel.getByRole('button', { name: 'Generate Workbench proposal' })
  await expect(execute).toBeVisible()
  await execute.click()
  await expect(panel.getByRole('alert')).toContainText(
    'workflow run does not use the input manifest pinned by the accepted command',
  )
  expect(state.requests.some((item) => item.path === '/v1/build-manifests/build-1')).toBe(false)
  expect(state.requests.some((item) => item.path.endsWith('/build-manifests/build-1/generate'))).toBe(false)

  await panel.getByRole('alert').getByRole('button').click()
  state.run = workflowRun(proposal.workbenchInstruction.expectedRunId, 'waiting_input')
  await execute.click()
  await expect(panel.getByRole('alert')).toContainText(
    'frozen Workbench bundle is not linked to the expected workflow run',
  )
  expect(state.requests.some((item) => item.path === '/v1/build-manifests/build-1')).toBe(true)
  expect(state.requests.some((item) => item.path.endsWith('/build-manifests/build-1/generate'))).toBe(false)
  expect(state.requests.some((item) => item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'))).toBe(false)

  await panel.getByRole('alert').getByRole('button').click()
  state.workbenchBundle = buildManifest(proposal.workbenchInstruction.expectedRunId)
  await execute.click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST' && item.path.endsWith('/build-manifests/build-1/generate'),
  )).toBe(true)
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'),
  )).toBe(true)
  const commandRequest = state.requests.find((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'))
  expect(commandRequest?.body).toEqual({
    workbenchResult: {
      runId: proposal.workbenchInstruction.expectedRunId,
      bundleId: 'build-1',
    },
  })
  expect(commandRequest?.headers['if-match']).toBe(
    '"conversation-command:77777777-7777-4777-8777-777777777777:1"',
  )
  expect(commandRequest?.headers['idempotency-key']).toBeTruthy()
})

test('commenters can append immutable conversation messages but cannot generate intents', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, role: 'commenter' })
  await page.goto('/workbench/planning?view=code')

  const composer = page.getByPlaceholder('Describe requirements or a controlled next action…')
  await composer.fill('A commenter can add requirement context.')
  const send = page.getByRole('button', { name: 'Send immutable message' })
  await expect(send).toBeEnabled()
  await send.click()
  await expect(page.getByText('A commenter can add requirement context.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Generate governed intent' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'New conversation' })).toBeDisabled()

  const messageRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path.endsWith('/messages'))
  expect(messageRequest?.headers['idempotency-key']).toBeTruthy()
  expect(state.requests.some((item) =>
    item.method === 'POST' && item.path.endsWith('/intent-proposals/generate'),
  )).toBe(false)
})

test('an existing project conversation exposes an explicit new-conversation path', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto('/workbench/planning?view=code')

  await expect(page.getByRole('option', { name: 'Project discovery · active' })).toHaveCount(1)
  await page.getByRole('button', { name: 'New conversation' }).click()
  await page.getByPlaceholder('Conversation title').fill('Second planning thread')
  await page.getByRole('button', { name: 'Create', exact: true }).click()

  await expect(page.getByRole('option', { name: 'Second planning thread · active' })).toHaveCount(1)
  await expect(page).toHaveURL(/conversationId=44444444-4444-4444-8444-444444444444/)
  const createRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/conversations`)
  expect(createRequest?.body).toEqual({ title: 'Second planning thread' })
  expect(createRequest?.headers['idempotency-key']).toBeTruthy()
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

  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  await page.getByRole('button', { name: 'Preview', exact: true }).click()
  const preview = page.frameLocator('iframe[title="Canonical application preview"]')
  await expect(preview.getByText('SERVER_APPLIED_APPLICATION', { exact: true })).toBeVisible()
  expect(state.requests.some((item) => item.path.endsWith('/build-manifests/build-1/generate'))).toBe(true)
  expect(state.requests.some((item) => item.path.endsWith('/implementation-proposals/implementation-1/apply'))).toBe(true)
  expect(state.requests.some((item) => item.path.includes('/api/generate'))).toBe(false)
})

test('document dependency graph opens the exact document, PageSpec, and prototype workspaces', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  const graphUrl = `/team/acme/project/${project.id}/graph`

  await page.goto(graphUrl)
  await page.getByRole('button', { name: 'Open document editor' }).click()
  await expect(page).toHaveURL(new RegExp(`/editor\\?artifactId=${projectBrief.artifact.id}`))
  await expect(page.getByText('Project Brief', { exact: true }).first()).toBeVisible()

  await page.goto(graphUrl)
  await page.locator('button').filter({ hasText: 'Dashboard PageSpec' }).first().click()
  await page.getByRole('button', { name: 'Open PageSpecs in Blueprint' }).click()
  await expect(page).toHaveURL(new RegExp(`/blueprint\\?artifactId=${pageSpec.artifact.id}`))
  await expect(page.getByText('Dashboard PageSpec', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'States 4' })).toBeVisible()

  await page.goto(graphUrl)
  await page.locator('button').filter({ hasText: 'Dashboard Prototype' }).first().click()
  await page.getByRole('button', { name: 'Open Prototype Studio' }).click()
  await expect(page).toHaveURL(new RegExp(`/prototype\\?artifactId=${approvedPrototype.artifact.id}`))
  await expect(page.getByText('Dashboard Prototype', { exact: true }).first()).toBeVisible()
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

test('Blueprint PageSpec editor autosaves, versions, and requests exact revision review', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/blueprint`)

  await expect(page.getByText('Product Blueprint', { exact: true }).first()).toBeVisible()
  await page.getByRole('button', { name: 'PageSpecs 1' }).click()
  await page.getByRole('button', { name: 'Open editor' }).click()
  await expect(page.getByText('Dashboard PageSpec', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'States 4' })).toBeVisible()

  await page.getByLabel('User goal').fill('Inspect order health and resolve exceptions.')
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'PATCH' && item.path === `/v1/page-specs/${pageSpec.artifact.id}/draft`,
  )).toBe(true)
  const draftRequest = state.requests.findLast((item) =>
    item.method === 'PATCH' && item.path === `/v1/page-specs/${pageSpec.artifact.id}/draft`,
  )
  expect(draftRequest?.body).toMatchObject({
    content: {
      route: '/dashboard',
      userGoal: 'Inspect order health and resolve exceptions.',
      acceptanceCriterionIds: ['AC-DASHBOARD-001'],
    },
  })
  expect((draftRequest?.body as { sourceVersions?: unknown })?.sourceVersions).toBeUndefined()

  await page.getByRole('button', { name: 'Versions 1' }).last().click()
  await page.getByRole('button', { name: 'Create immutable PageSpec revision' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST' && item.path === `/v1/page-specs/${pageSpec.artifact.id}/revisions`,
  )).toBe(true)

  await page.getByRole('button', { name: 'Review 0' }).last().click()
  await page.getByLabel('PageSpec reviewer').selectOption(reviewer.id)
  await page.getByRole('button', { name: 'Request review' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/reviews`
      && (item.body as { target?: { revisionId?: string } })?.target?.revisionId === 'dddddddd-dddd-4ddd-8ddd-ddddddddddd4',
  )).toBe(true)
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
    'access-control-expose-headers': 'ETag, X-Request-ID, X-CSRF-Token, X-Command-ETag, X-Command-Location',
  }
}

interface GenerateIntentBody {
  readonly triggerMessageId: string
  readonly candidateDefinitionVersionIds: readonly string[]
  readonly sourceRefs: ReadonlyArray<{
    readonly artifactId: string
    readonly revisionId: string
    readonly contentHash: string
  }>
  readonly manifestIntent: {
    readonly mode: 'use_existing'
    readonly inputManifest: { readonly id: string; readonly hash: string }
    readonly purpose: string
  }
  readonly model?: string
}

type MockWorkflowIntentProposal =
  | ReturnType<typeof workflowIntentProposal>
  | ReturnType<typeof workbenchIntentProposal>

function conversationRecord(id: string, title: string) {
  return {
    id,
    projectId: project.id,
    title,
    status: 'active' as 'active' | 'archived',
    version: 1,
    etag: `"conversation:${id}:1"`,
    createdBy: user.id,
    createdAt: now,
    updatedAt: now,
  }
}

function conversationMessage(
  id: string,
  conversationId: string,
  sequence: number,
  role: 'user' | 'assistant',
  content: string,
  proposalId?: string,
) {
  return {
    id,
    conversationId,
    sequence,
    role,
    content,
    ...(proposalId ? { proposalId } : {}),
    createdBy: user.id,
    createdAt: now,
  }
}

function workflowIntentProposal(conversationId: string, input: GenerateIntentBody) {
  const id = '66666666-6666-4666-8666-666666666666'
  const workbenchInstruction = {
    objective: 'Start the minimum application workflow from the approved Project Brief.',
    constraints: ['Preserve exact source and manifest identities.'],
  }
  return {
    id,
    projectId: project.id,
    conversationId,
    triggerMessageId: input.triggerMessageId,
    assistantMessageId: '66666666-6666-4666-8666-666666666667',
    kind: 'start_workflow' as const,
    status: 'pending' as 'pending' | 'accepted' | 'rejected',
    version: 1,
    etag: `"intent-proposal:${id}:1"`,
    suggestedDefinitionVersionId: input.candidateDefinitionVersionIds[0] ?? workflowDefinition.versionId,
    scope: { conversationIntent: { workbenchInstruction } },
    sourceRefs: input.sourceRefs,
    manifestIntent: input.manifestIntent,
    workbenchInstruction,
    origin: 'ai' as const,
    ai: { provider: 'openai', model: input.model ?? 'gpt-5', responseId: 'response-e2e' },
    proposedBy: user.id,
    createdAt: now,
    decisionReason: undefined as string | undefined,
    decidedBy: undefined as string | undefined,
    decidedAt: undefined as string | undefined,
  }
}

function workbenchIntentProposal(conversationId: string, triggerMessageId: string) {
  const expectedRunId = '88888888-8888-4888-8888-888888888888'
  const generated = workflowIntentProposal(conversationId, {
    triggerMessageId,
    candidateDefinitionVersionIds: [workflowDefinition.versionId],
    sourceRefs: [{
      artifactId: projectBrief.artifact.id,
      revisionId: briefRevision.id,
      contentHash: briefRevision.contentHash,
    }],
    manifestIntent: {
      mode: 'use_existing',
      inputManifest: { id: 'manifest-1', hash: hash('1') },
      purpose: 'continue_application_workflow',
    },
    model: 'gpt-5',
  })
  const workbenchInstruction = {
    objective: 'Generate the reviewed order dashboard implementation.',
    constraints: ['Use only the frozen Workbench bundle.'],
    expectedRunId,
    expectedBundleId: 'build-1',
  }
  return {
    ...generated,
    kind: 'workbench_instruction' as const,
    status: 'accepted' as 'pending' | 'accepted' | 'rejected',
    version: 2,
    etag: `"intent-proposal:${generated.id}:2"`,
    scope: { conversationIntent: { workbenchInstruction } },
    workbenchInstruction,
    decidedBy: user.id,
    decidedAt: now,
  }
}

function conversationCommand(proposal: MockWorkflowIntentProposal) {
  const id = '77777777-7777-4777-8777-777777777777'
  return {
    id,
    projectId: project.id,
    conversationId: proposal.conversationId,
    proposalId: proposal.id,
    kind: proposal.kind,
    status: 'pending' as 'pending' | 'executed' | 'rejected' | 'failed',
    version: 1,
    etag: `"conversation-command:${id}:1"`,
    payload: {
      definitionVersionId: proposal.suggestedDefinitionVersionId,
      scope: proposal.scope,
      sourceRefs: proposal.sourceRefs,
      manifestIntent: proposal.manifestIntent,
      workbench: proposal.workbenchInstruction,
    },
    result: undefined as Record<string, unknown> | undefined,
    acceptedBy: user.id,
    executionActorId: undefined as string | undefined,
    executedBy: undefined as string | undefined,
    createdAt: now,
    updatedAt: now,
    executedAt: undefined as string | undefined,
  }
}

function staleProjectBrief() {
  return {
    ...projectBrief,
    draft: {
      id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbd',
      artifactId: projectBrief.artifact.id,
      baseRevisionId: briefRevision.id,
      sourceVersions: [],
      revision: 4,
      content: {
        ...briefRevision.content,
        summary: 'This unapproved draft must never become an AI source implicitly.',
      },
      contentHash: hash('a'),
      updatedBy: user.id,
      updatedAt: now,
      etag: '"project-brief-draft:4"',
    },
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
    breakpoints: [
      { id: 'breakpoint-desktop', name: 'Desktop', minWidth: 1024, viewportWidth: 1280, viewportHeight: 800 },
      { id: 'breakpoint-tablet', name: 'Tablet', minWidth: 768, maxWidth: 1023, viewportWidth: 834, viewportHeight: 1112 },
      { id: 'breakpoint-mobile', name: 'Mobile', minWidth: 0, maxWidth: 767, viewportWidth: 390, viewportHeight: 844 },
    ],
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
    frames: [
      { id: 'frame-ready-desktop', stateId: 'prototype-state-ready', breakpointId: 'breakpoint-desktop', rootLayerId: 'layer-root', title: 'Ready · Desktop' },
      { id: 'frame-ready-tablet', stateId: 'prototype-state-ready', breakpointId: 'breakpoint-tablet', rootLayerId: 'layer-root', title: 'Ready · Tablet' },
      { id: 'frame-ready-mobile', stateId: 'prototype-state-ready', breakpointId: 'breakpoint-mobile', rootLayerId: 'layer-root', title: 'Ready · Mobile' },
    ],
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

function buildManifest(workflowRunId?: string) {
  return {
    id: 'build-1',
    projectId: project.id,
    ...(workflowRunId ? { workflowRunId } : {}),
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
