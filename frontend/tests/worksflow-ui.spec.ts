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

const selectionWorkflowDefinition = {
  id: 'abababab-abab-4bab-8bab-abababababab',
  versionId: 'abababab-abab-4bab-8bab-ababababab01',
  projectId: project.id,
  key: 'blueprint-selection-app',
  title: 'Build application from Blueprint selection',
  description: 'Selection to Workbench.',
  published: true,
  version: 1,
  contentHash: hash('a'),
  definition: {
    id: 'abababab-abab-4bab-8bab-abababababab',
    version: 1,
    name: 'Build application from Blueprint selection',
    schemaVersion: '1',
    nodes: [{
      id: 'selection', name: 'Frozen Blueprint selection', type: 'artifact_input',
      artifactInput: { allowedTypes: ['blueprint'], requireApproved: true, minimumArtifacts: 1 },
    }, {
      id: 'pages', name: 'Selected approved pages', type: 'fan_out',
      fanOut: {
        itemsPath: '/blueprintPages', sliceKeyPath: '/key', mergeNodeId: 'pages-merged',
        maxParallel: 4, itemKind: 'blueprint_selection_page',
      },
    }],
    edges: [],
    hash: hash('a'), createdBy: user.id, createdAt: now,
  },
}

interface MockPlatformOptions {
  readonly authenticated?: boolean
  readonly prototypes?: 'approved' | 'none'
  readonly historicalRun?: boolean
  readonly role?: 'owner' | 'commenter'
  readonly staleBriefDraft?: boolean
  readonly workflowRequireApproved?: boolean
  readonly multiBundleWorkbench?: boolean
  readonly multiWorkbenchGroups?: boolean
  readonly prototypeProposal?: boolean
  readonly designImportDecisionFails?: boolean
  readonly conversationSelectionTarget?: boolean
}

type DesignImportRecord = ReturnType<typeof designImportRecord>
type MockDesignImport = Omit<DesignImportRecord, 'status' | 'proposal'> & {
  readonly status: 'open' | 'applied' | 'rejected'
  readonly proposal: Omit<DesignImportRecord['proposal'], 'status'> & {
    readonly status: 'open' | 'applied' | 'rejected'
  }
}

interface MockPlatformState {
  requests: Array<{
    method: string
    path: string
    body: unknown
    headers: Record<string, string>
  }>
  prototypes: unknown[]
  brief: ReturnType<typeof staleProjectBrief>
  pageSpec: typeof pageSpec
  run: ReturnType<typeof workflowRun> | ReturnType<typeof selectionWorkflowRun> | ReturnType<typeof multiBundleWorkflowRun> | ReturnType<typeof multiGroupWorkflowRun> | null
  proposal: ReturnType<typeof implementationProposal> | null
  workspaceRevision: ReturnType<typeof applicationRevision> | null
  workbenchBundle: ReturnType<typeof buildManifest>
  multiWorkbench: ReturnType<typeof multiWorkbenchState> | null
  prototypeProposal: ReturnType<typeof artifactProposal> | null
  prototypeCreatedRevision: Record<string, unknown> | null
  designImports: MockDesignImport[]
  conversations: Array<ReturnType<typeof conversationRecord>>
  conversationMessages: Array<ReturnType<typeof conversationMessage>>
  intentProposal: MockWorkflowIntentProposal | null
  conversationCommand: ReturnType<typeof conversationCommand> | null
  graphBriefTitle: string
  bindingVersion: number
  bindingReviewerRole: 'reviewer' | 'assignee' | 'watcher'
}

async function installPlatformMock(page: Page, options: MockPlatformOptions = {}) {
  const state: MockPlatformState = {
    requests: [],
    prototypes: options.prototypes === 'none' ? [] : [approvedPrototype],
    brief: staleProjectBrief(options.staleBriefDraft ?? false),
    pageSpec: structuredClone(pageSpec),
    run: options.multiWorkbenchGroups
      ? multiGroupWorkflowRun()
      : options.multiBundleWorkbench ? multiBundleWorkflowRun()
      : options.conversationSelectionTarget ? selectionWorkflowRun('run-selection-active')
      : options.historicalRun ? workflowRun('run-history', 'waiting_input') : null,
    proposal: null,
    workspaceRevision: null,
    workbenchBundle: buildManifest(options.conversationSelectionTarget ? 'run-selection-active' : undefined),
    multiWorkbench: options.multiWorkbenchGroups
      ? multiGroupWorkbenchState()
      : options.multiBundleWorkbench ? multiWorkbenchState() : null,
    prototypeProposal: options.prototypeProposal ? artifactProposal() : null,
    prototypeCreatedRevision: null,
    designImports: [],
    conversations: [conversationRecord('33333333-3333-4333-8333-333333333333', 'Project discovery')],
    conversationMessages: [],
    intentProposal: null,
    conversationCommand: null,
    graphBriefTitle: projectBrief.artifact.title,
    bindingVersion: 1,
    bindingReviewerRole: 'reviewer',
  }
  await page.route('**/api/platform/v1/**', async (route) => {
    await handlePlatformRoute(route, state, options)
  })
  await page.route('**/v1/**', async (route) => {
    await handlePlatformRoute(route, state, options)
  })
  return state
}

async function installProjectWebSocketMock(page: Page) {
  let active: {
    send(message: string): void
    subscriptionId: string
    projectId: string
  } | null = null
  await page.routeWebSocket('**/v1/ws', (socket) => {
    const state = { send: (message: string) => socket.send(message), subscriptionId: '', projectId: '' }
    active = state
    socket.onMessage((raw) => {
      const message = JSON.parse(raw.toString()) as {
        type?: string
        subscriptionId?: string
        projectId?: string
      }
      if (message.type === 'auth') {
        socket.send(JSON.stringify({ type: 'auth.ack', connectionId: 'e2e-connection' }))
      } else if (message.type === 'subscribe' && message.subscriptionId && message.projectId) {
        state.subscriptionId = message.subscriptionId
        state.projectId = message.projectId
        socket.send(JSON.stringify({ type: 'subscription.ack', subscriptionId: message.subscriptionId }))
      }
    })
  })
  return {
    ready(projectId: string) {
      return Boolean(active && active.projectId === projectId && active.subscriptionId)
    },
    emit(type: string, projectId: string, payload: Record<string, unknown>) {
      if (!active || active.projectId !== projectId || !active.subscriptionId) {
        throw new Error('project WebSocket subscription is not ready')
      }
      active.send(JSON.stringify({
        type: 'event',
        event: {
          id: `e2e-event-${Date.now()}-${Math.random()}`,
          type,
          cursor: String(Date.now()),
          subscriptionId: active.subscriptionId,
          projectId,
          occurredAt: new Date().toISOString(),
          payload,
        },
      }))
    },
  }
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

  if (path === `/v1/projects/${project.id}/document-graph` && method === 'GET') {
    await respond({
      projectId: project.id,
      nodes: [
        graphArtifactNode(projectBrief, 'document', state.graphBriefTitle),
        graphArtifactNode(pageSpec, 'page'),
        graphArtifactNode(approvedPrototype, 'page'),
        {
          id: 'input_manifest:manifest-graph-1', entityId: 'manifest-graph-1', entityType: 'inputManifest',
          title: 'Frozen document input', status: 'frozen', metadata: { sourceCount: 1 }, updatedAt: now,
        },
        {
          id: 'output_proposal:proposal-graph-1', entityId: 'proposal-graph-1', entityType: 'outputProposal',
          title: 'Reviewable document output', status: 'open', metadata: { operationCount: 1, pendingCount: 1 }, updatedAt: now,
        },
      ],
      edges: [
        { id: 'graph-frozen-input', sourceId: `artifact:${projectBrief.artifact.id}`, targetId: 'input_manifest:manifest-graph-1', relation: 'frozen_input', required: true, metadata: {} },
        { id: 'graph-generated-output', sourceId: 'input_manifest:manifest-graph-1', targetId: 'output_proposal:proposal-graph-1', relation: 'generated_output', required: true, metadata: {} },
      ],
    })
    return
  }
  const memberBindings = path.match(/^\/v1\/artifacts\/([^/]+)\/member-bindings$/)
  if (memberBindings && method === 'GET') {
    const artifactId = memberBindings[1]
    const etag = `"artifact-bindings:${artifactId}:v${state.bindingVersion}"`
    await respond({
      artifactId, projectId: project.id, version: state.bindingVersion, etag,
      items: [
        { artifactId, projectId: project.id, userId: user.id, role: 'owner', reason: '', assignedBy: user.id, assignedAt: now },
        { artifactId, projectId: project.id, userId: reviewer.id, role: state.bindingReviewerRole, reason: '', assignedBy: user.id, assignedAt: now },
      ],
    }, 200, { etag })
    return
  }
  if (memberBindings && method === 'PUT') {
    const artifactId = memberBindings[1]
    state.bindingVersion += 1
    const etag = `"artifact-bindings:${artifactId}:v${state.bindingVersion}"`
    await respond({
      artifactId, projectId: project.id, version: state.bindingVersion, etag,
      items: (body as { items: unknown[] }).items,
    }, 200, { etag })
    return
  }

  if (path === `/v1/projects/${project.id}/design-import-capabilities` && method === 'GET') {
    await respond({
      snapshotPolicy: 'Every upload is frozen before proposal creation.',
      trustPolicy: 'External sources are not project facts.',
      sources: ['figma', 'penpot', 'excalidraw', 'tldraw', 'storybook', 'ladle', 'upload'].map((sourceKind) => ({
        sourceKind,
        label: sourceKind === 'tldraw' ? 'tldraw' : sourceKind[0].toUpperCase() + sourceKind.slice(1),
        uploadEnabled: true,
        remoteEnabled: false,
        remoteReason: 'No remote connector credential is configured.',
        acceptedMediaTypes: ['application/json'],
        acceptedFileExtensions: ['.json'],
        maxUploadBytes: 8 * 1024 * 1024,
      })),
    })
    return
  }
  if (path === `/v1/projects/${project.id}/design-imports` && method === 'GET') {
    await respond({ items: state.designImports, total: state.designImports.length })
    return
  }
  if (path === `/v1/projects/${project.id}/design-imports` && method === 'POST') {
    const input = body as {
      sourceKind: 'figma'
      title?: string
      file: { name: string; mediaType: string; contentBase64: string }
      selectedFrameIds?: string[]
      pageSpecRevision: { artifactId: string; revisionId: string; contentHash: string }
      targetPrototypeArtifactId?: string
    }
    const created = designImportRecord(input)
    state.designImports = [created, ...state.designImports]
    await respond(created, 201, { etag: created.etag })
    return
  }
  const designImportDecision = path.match(/^\/v1\/design-imports\/([^/]+)\/decision$/)
  if (designImportDecision && method === 'POST') {
    if (options.designImportDecisionFails) {
      await respond({
        type: 'urn:worksflow:problem:etag_mismatch', title: 'Precondition failed', status: 412,
        detail: 'The design import changed since it was loaded.', code: 'etag_mismatch',
      }, 412)
      return
    }
    const input = body as { decision: 'approve' | 'reject'; version: number }
    const current = state.designImports.find((item) => item.id === designImportDecision[1])
    if (!current) {
      await respond({ title: 'Not found', status: 404 }, 404)
      return
    }
    const nextVersion = current.version + 2
    const updated = {
      ...current,
      status: input.decision === 'approve' ? 'applied' as const : 'rejected' as const,
      version: nextVersion,
      etag: `"design-import:${current.id}:${nextVersion}"`,
      decidedBy: user.id,
      decidedAt: now,
      updatedAt: now,
      ...(input.decision === 'approve' ? {
        appliedRevisionId: '99999999-9999-4999-8999-999999999992',
        proposal: { ...current.proposal, status: 'applied' as const, version: current.proposal.version + 2 },
      } : {
        proposal: { ...current.proposal, status: 'rejected' as const, version: current.proposal.version + 1 },
      }),
    }
    state.designImports = state.designImports.map((item) => item.id === current.id ? updated : item)
    await respond(updated, 200, { etag: updated.etag })
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
    const proposal = options.conversationSelectionTarget
      ? {
          ...workbenchIntentProposal(intentGeneratePath[1], input.triggerMessageId, {
            expectedRunId: 'run-selection-active',
            expectedBundleId: 'build-1',
            definitionVersionId: selectionWorkflowDefinition.versionId,
          }),
          status: 'pending' as 'pending' | 'accepted' | 'rejected',
          version: 1,
          etag: '"intent-proposal:66666666-6666-4666-8666-666666666666:1"',
          sourceRefs: input.sourceRefs,
          manifestIntent: input.manifestIntent,
          decidedBy: undefined,
          decidedAt: undefined,
        }
      : workflowIntentProposal(intentGeneratePath[1], input)
    const assistant = conversationMessage(
      proposal.assistantMessageId,
      intentGeneratePath[1],
      state.conversationMessages.filter((item) => item.conversationId === intentGeneratePath[1]).length + 1,
      'assistant',
      options.conversationSelectionTarget
        ? 'Continue the active Blueprint-selection Workbench target.'
        : 'Start the published minimum application workflow from the approved Project Brief.',
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
      workbenchResult?: { runId: string; bundleId: string; implementationProposalId: string }
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
    if (method === 'POST') {
      const input = body as { title: string; kind: string; content: unknown; sourceVersions: unknown[] }
      await respond({
        artifact: { ...projectBrief.artifact, id: 'selection-document-1', kind: 'reference_source', title: input.title, status: 'draft', activeDraftId: 'selection-document-draft-1', etag: '"artifact:selection-document-1:1"' },
        draft: { id: 'selection-document-draft-1', artifactId: 'selection-document-1', sourceVersions: input.sourceVersions, revision: 1, content: input.content, contentHash: hash('6'), updatedBy: user.id, updatedAt: now, etag: '"selection-document-draft:1"' },
      }, 201, { etag: '"selection-document-draft:1"' })
      return
    }
    await respond({ items: [state.brief] })
    return
  }
  if (path === '/v1/documents/selection-document-1/revisions' && method === 'POST') {
    await respond({
      id: 'selection-document-revision-1', artifactId: 'selection-document-1', revisionNumber: 1,
      contentHash: hash('6'), status: 'draft', content: {}, createdBy: user.id, createdAt: now,
    }, 201)
    return
  }
  if (path === `/v1/documents/${projectBrief.artifact.id}/draft` && method === 'PATCH') {
    const input = body as { content: typeof briefRevision.content }
    const currentDraft = state.brief.draft
    state.brief = {
      ...state.brief,
      draft: {
        ...currentDraft,
        revision: currentDraft.revision + 1,
        content: input.content,
        contentHash: hash('a'),
        updatedAt: now,
        etag: `"project-brief-draft:${currentDraft.revision + 1}"`,
      },
    }
    await respond(state.brief, 200, { etag: state.brief.draft.etag })
    return
  }
  if (path === `/v1/documents/${projectBrief.artifact.id}/revisions` && method === 'POST') {
    const revision = {
      id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb4',
      artifactId: projectBrief.artifact.id,
      revisionNumber: 4,
      contentHash: state.brief.draft.contentHash,
      status: 'draft',
      content: state.brief.draft.content,
      sourceVersions: state.brief.draft.sourceVersions,
      createdBy: user.id,
      createdAt: now,
    }
    state.brief = {
      ...state.brief,
      artifact: { ...state.brief.artifact, latestRevisionId: revision.id },
      latestRevision: revision,
    }
    await respond(revision, 201)
    return
  }
  if (path === `/v1/projects/${project.id}/blueprints`) {
    await respond({ items: [blueprint] })
    return
  }
  if (path === `/v1/blueprints/${blueprint.artifact.id}/draft` && method === 'PATCH') {
    const input = body as { content: typeof blueprintContent }
    await respond({
      ...blueprint,
      draft: {
        id: 'blueprint-draft-1', artifactId: blueprint.artifact.id,
        baseRevisionId: blueprintRevision.revisionId, sourceVersions: [], revision: 2,
        content: input.content, contentHash: hash('4'), updatedBy: user.id, updatedAt: now,
        etag: '"blueprint-draft:2"',
      },
    }, 200, { etag: '"blueprint-draft:2"' })
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
    await respond({ items: state.prototypeProposal ? [state.prototypeProposal] : [] })
    return
  }
  const artifactProposalItem = path.match(/^\/v1\/output-proposals\/([^/]+)$/)
  if (artifactProposalItem && method === 'GET' && state.prototypeProposal) {
    await respond(state.prototypeProposal)
    return
  }
  const artifactProposalDecision = path.match(/^\/v1\/output-proposals\/([^/]+)\/decisions$/)
  if (artifactProposalDecision && method === 'POST' && state.prototypeProposal) {
    const input = body as {
      operationId: string
      decision: 'accepted' | 'rejected'
      reason?: string
      version: number
    }
    const operations = state.prototypeProposal.operations.map((operation) =>
      operation.id === input.operationId
        ? {
            ...operation,
            decision: input.decision,
            decidedBy: user.id,
            ...(input.reason ? { reason: input.reason } : {}),
          }
        : operation)
    state.prototypeProposal = {
      ...state.prototypeProposal,
      operations,
      status: operations.every((operation) => operation.decision !== 'pending')
        && operations.some((operation) => operation.decision === 'accepted')
        ? 'ready'
        : 'reviewing',
      version: state.prototypeProposal.version + 1,
    }
    await respond(state.prototypeProposal, 200, {
      etag: `"output-proposal:${state.prototypeProposal.id}:${state.prototypeProposal.version}"`,
    })
    return
  }
  const artifactProposalApply = path.match(/^\/v1\/output-proposals\/([^/]+)\/apply$/)
  if (artifactProposalApply && method === 'POST' && state.prototypeProposal) {
    const current = state.prototypes[0] as typeof approvedPrototype
    const appliedContent = {
      ...current.draft.content,
      layers: {
        ...current.draft.content.layers,
        'layer-root': {
          ...current.draft.content.layers['layer-root'],
          name: 'AI-reviewed Page',
          properties: { aiReviewed: true },
        },
      },
    }
    const draft = {
      ...current.draft,
      revision: current.draft.revision + 1,
      content: appliedContent,
      contentHash: hash('p'),
      etag: `"prototype-draft:${current.draft.revision + 1}:proposal"`,
    }
    state.prototypes = [{ ...current, draft }]
    state.prototypeProposal = {
      ...state.prototypeProposal,
      status: 'applied',
      operations: state.prototypeProposal.operations.map((operation) => ({
        ...operation,
        decision: operation.decision === 'accepted' ? 'applied' : operation.decision,
      })),
      version: state.prototypeProposal.version + 1,
      appliedAt: now,
    }
    await respond(draft)
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
    const attributedRevision = state.prototypeProposal?.status === 'applied'
      ? {
          ...revision,
          changeSource: 'human',
          sourceManifestId: state.prototypeProposal.manifest.id,
          proposalId: state.prototypeProposal.id,
        }
      : revision
    state.prototypes = [{ ...current, latestRevision: attributedRevision }]
    state.prototypeCreatedRevision = attributedRevision
    await respond(attributedRevision, 201)
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
    await respond({ items: [workflowDefinitionFor(options), selectionWorkflowDefinition], total: 2 })
    return
  }
  if (path.endsWith(`/workflow-definitions/${workflowDefinition.id}/versions`)) {
    await respond({ items: [workflowDefinitionFor(options)], total: 1 })
    return
  }
  if (path.endsWith(`/workflow-definitions/${selectionWorkflowDefinition.id}/versions`)) {
    await respond({ items: [selectionWorkflowDefinition], total: 1 })
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
  if (path === `/v1/projects/${project.id}/blueprint-selections/compile` && method === 'POST') {
    const input = body as { blueprintRevision: typeof blueprintRevision; nodeIds: string[] }
    const selectionId = `sha256:${hash(input.nodeIds.includes('page-dashboard') ? 's' : 't')}`
    const nodes = blueprintContent.semantic.nodes.filter((node) => input.nodeIds.includes(node.id))
    const anchoredSources = input.nodeIds.map((nodeId) => ({ ref: { ...input.blueprintRevision, anchorId: nodeId }, purpose: 'blueprint_selection_node' }))
    const pageSelected = input.nodeIds.includes('page-dashboard')
    const prototypeAvailable = state.prototypes.length > 0
    const pageSpecRef = exactRevision(pageSpecRevision.artifactId, pageSpecRevision.id, pageSpecRevision.revisionNumber, pageSpecRevision.contentHash)
    const prototypeRef = exactRevision(approvedPrototypeRevision.artifactId, approvedPrototypeRevision.id, approvedPrototypeRevision.revisionNumber, approvedPrototypeRevision.contentHash)
    await respond({
      id: `selection-manifest-${input.nodeIds.join('-')}`, projectId: project.id,
      jobType: 'blueprint.selection', deliverySliceId: selectionId,
      sources: [
        { ref: input.blueprintRevision, purpose: 'blueprint_selection_root' },
        ...anchoredSources,
        ...(pageSelected ? [{ ref: pageSpecRef, purpose: 'selected_page_spec' }] : []),
        ...(pageSelected && prototypeAvailable ? [{ ref: prototypeRef, purpose: 'selected_prototype' }] : []),
      ],
      constraints: { blueprintSelection: {
        schemaVersion: 1, selectionId, blueprint: input.blueprintRevision,
        nodeIds: input.nodeIds, nodes, edges: [],
        pageBindings: pageSelected ? [{ nodeId: 'page-dashboard', pageSpec: pageSpecRef, ...(prototypeAvailable ? { prototype: prototypeRef } : {}) }] : [],
      } },
      outputSchemaVersion: 'blueprint-selection/v1', createdBy: user.id, createdAt: now,
      hash: hash(input.nodeIds.includes('page-dashboard') ? '8' : '9'),
    }, 201)
    return
  }
  if (/^\/v1\/input-manifests\/[^/]+\/generate$/.test(path) && method === 'POST') {
    await respond({ proposal: artifactProposal(), provider: 'mock', model: 'gpt-5' }, 201)
    return
  }
  if (path === `/v1/projects/${project.id}/workflow-runs` && method === 'POST') {
    const input = body as { definitionVersionId?: string }
    state.run = input.definitionVersionId === selectionWorkflowDefinition.versionId
      ? selectionWorkflowRun('run-selection')
      : workflowRun('run-started', 'waiting_input')
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
  const multi = state.multiWorkbench
  const lineagePath = path.match(/^\/v1\/build-manifests\/([^/]+)\/lineage-state$/)
  if (multi && lineagePath && method === 'GET') {
    await respond(multiLineageState(multi, lineagePath[1]))
    return
  }
  const multiBundlePath = path.match(/^\/v1\/build-manifests\/([^/]+)$/)
  if (multi && multiBundlePath && method === 'GET') {
    const bundle = multi.bundles[multiBundlePath[1]]
    await respond(bundle ?? { title: 'Not found' }, bundle ? 200 : 404)
    return
  }
  const rebasePath = path.match(/^\/v1\/build-manifests\/([^/]+)\/rebase$/)
  if (multi && rebasePath && method === 'POST') {
    const activeBundle = multi.bundles[rebasePath[1]]
    const rootId = activeBundle.rootBuildManifestId ?? activeBundle.id
    const input = body as {
      workspaceRevision: {
        artifactId: string
        revisionId: string
        contentHash: string
      }
    }
    const revisionNumber = Number(input.workspaceRevision.revisionId.match(/\d+$/)?.[0] ?? 0)
    const derived = multiBuildManifest(
      `${rootId}-w${revisionNumber}`,
      activeBundle.deliverySliceId ?? 'page-checkout',
      rootId,
      activeBundle.id,
      exactRevision(
        input.workspaceRevision.artifactId,
        input.workspaceRevision.revisionId,
        revisionNumber,
        input.workspaceRevision.contentHash,
      ),
    )
    multi.bundles[derived.id] = derived
    multi.activeBundleIds[rootId] = derived.id
    delete multi.currentProposalIds[rootId]
    await respond(derived, 201)
    return
  }
  const multiGeneratePath = path.match(/^\/v1\/build-manifests\/([^/]+)\/generate$/)
  if (multi && multiGeneratePath && method === 'POST') {
    const bundleId = multiGeneratePath[1]
    const proposal = multiImplementationProposal(
      'proposal-checkout',
      bundleId,
      'open',
      'pending',
      1,
      'Checkout updates the exact shared file produced by Home.',
      hash('a'),
    )
    multi.proposals[proposal.id] = proposal
    multi.currentProposalIds['bundle-checkout'] = proposal.id
    await respond({ proposal, provider: 'openai', model: 'gpt-5' }, 201)
    return
  }
  const multiDecisionPath = path.match(/^\/v1\/implementation-proposals\/([^/]+)\/decisions$/)
  if (multi && multiDecisionPath && method === 'POST') {
    const proposal = multi.proposals[multiDecisionPath[1]]
    const input = body as {
      operationId: string
      decision: 'accepted' | 'rejected'
      version: number
    }
    const updated = {
      ...proposal,
      operations: proposal.operations.map((operation) => operation.id === input.operationId
        ? { ...operation, decision: input.decision }
        : operation),
      status: 'ready' as const,
      version: proposal.version + 1,
    }
    multi.proposals[updated.id] = updated
    await respond(updated, 200, {
      etag: `"implementation-proposal:${updated.id}:${updated.version}"`,
    })
    return
  }
  const multiApplyPath = path.match(/^\/v1\/implementation-proposals\/([^/]+)\/apply$/)
  if (multi && multiApplyPath && method === 'POST') {
    const proposal = multi.proposals[multiApplyPath[1]]
    const isHome = proposal.id === 'proposal-home'
    const workspace = multiApplicationRevision(isHome ? 1 : 2)
    const applied = {
      ...proposal,
      operations: proposal.operations.map((operation) => ({
        ...operation,
        decision: operation.decision === 'accepted' ? 'applied' as const : operation.decision,
      })),
      status: 'applied' as const,
      version: proposal.version + 1,
    }
    multi.proposals[applied.id] = applied
    multi.currentWorkspaceRevision = workspace
    await respond(workspace)
    return
  }
  const multiProposalPath = path.match(/^\/v1\/implementation-proposals\/([^/]+)$/)
  if (multi && multiProposalPath && method === 'GET' && multi.proposals[multiProposalPath[1]]) {
    await respond(multi.proposals[multiProposalPath[1]])
    return
  }
  const multiWorkspacePath = path.match(/^\/v1\/revisions\/(workspace-r[12])$/)
  if (multi && multiWorkspacePath && method === 'GET') {
    await respond(multiApplicationRevision(Number(multiWorkspacePath[1].slice(-1))))
    return
  }
  if (
    multi
    && path === `/v1/projects/${project.id}/workflow-runs/run-multi/resume`
    && method === 'POST'
  ) {
    state.run = {
      ...multiBundleWorkflowRun(),
      status: 'completed',
      nodes: multiBundleWorkflowRun().nodes.map((node) => node.type === 'workbench_build'
        ? { ...node, status: 'completed' }
        : node),
    }
    await respond(undefined, 204)
    return
  }
  if (
    multi
    && path === `/v1/projects/${project.id}/workflow-runs/run-groups/resume`
    && method === 'POST'
  ) {
    const input = body as { nodeKey: string }
    const current = state.run as ReturnType<typeof multiGroupWorkflowRun>
    state.run = {
      ...current,
      nodes: current.nodes.map((node) => node.key === input.nodeKey
        ? { ...node, status: 'completed' as const }
        : node),
    }
    await respond(undefined, 204)
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

test('direct workflow start checkpoints a newer Brief draft when approval is not required', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    staleBriefDraft: true,
    workflowRequireApproved: false,
  })
  await page.goto('/workbench/planning?view=code')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  await page.getByRole('button', { name: 'Start from exact Project Brief' }).click()
  await expect(page.getByText('run-started', { exact: true }).first()).toBeVisible()

  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/documents/${projectBrief.artifact.id}/revisions`,
  )).toBe(true)
  const manifestRequest = state.requests.find((item) =>
    item.method === 'POST'
      && item.path.endsWith('/input-manifests')
      && (item.body as { jobType?: string })?.jobType === 'workflow_start')
  expect(manifestRequest?.body).toMatchObject({
    baseRevision: {
      artifactId: projectBrief.artifact.id,
      revisionId: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb4',
      contentHash: hash('a'),
    },
    sources: [{
      ref: {
        artifactId: projectBrief.artifact.id,
        revisionId: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb4',
        contentHash: hash('a'),
      },
      purpose: 'project_brief',
    }],
  })
})

test('direct workflow start fails closed when a required approved Brief has newer changes', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, staleBriefDraft: true })
  await page.goto('/workbench/planning?view=code')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  await page.getByRole('button', { name: 'Start from exact Project Brief' }).click()

  await expect(page.getByText('Project Brief has changes newer than its approved revision.')).toBeVisible()
  expect(state.requests.some((item) =>
    item.method === 'POST' && item.path.endsWith('/input-manifests'),
  )).toBe(false)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/documents/${projectBrief.artifact.id}/revisions`,
  )).toBe(false)
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

test('Project Brief conversation continues an active selection Workbench target without making it startable', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectionTarget: true,
  })
  await page.goto('/workbench/planning?view=code')

  const composer = page.getByPlaceholder('Describe requirements or a controlled next action…')
  await composer.fill('Continue the active Blueprint selection application.')
  await page.getByRole('button', { name: 'Send immutable message' }).click()
  await page.getByRole('button', { name: 'Generate governed intent' }).click()

  const panel = page.getByRole('complementary', { name: 'Conversation control plane' })
  await expect(panel.getByText('Workbench instruction', { exact: true })).toBeVisible()
  await expect(panel.getByText(selectionWorkflowDefinition.versionId, { exact: false })).toBeVisible()

  const generateRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path.endsWith('/intent-proposals/generate'))
  expect(generateRequest?.body).toMatchObject({
    candidateDefinitionVersionIds: [workflowDefinition.versionId],
  })
  expect((generateRequest?.body as GenerateIntentBody).candidateDefinitionVersionIds)
    .not.toContain(selectionWorkflowDefinition.versionId)

  await panel.getByRole('button', { name: 'Accept', exact: true }).click()
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'),
  )).toBe(true)

  const commandRequest = state.requests.find((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'))
  expect(commandRequest?.body).toEqual({
    workbenchResult: {
      runId: 'run-selection-active',
      bundleId: 'build-1',
      implementationProposalId: 'implementation-1',
    },
  })
  expect(state.conversationCommand?.payload.definitionVersionId)
    .toBe(selectionWorkflowDefinition.versionId)
  expect(state.requests.some((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/workflow-runs`)).toBe(false)
})

test('conversation checkpoints the current Project Brief draft before generating intent', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, staleBriefDraft: true })
  await page.goto('/workbench/planning?view=code')

  await expect.poll(() => state.requests.some((item) =>
    item.path === `/v1/projects/${project.id}/documents`,
  )).toBe(true)
  await page.getByPlaceholder('Describe requirements or a controlled next action…').fill('Generate from this brief.')
  await page.getByRole('button', { name: 'Send immutable message' }).click()
  await page.getByRole('button', { name: 'Checkpoint Brief & generate intent' }).click()

  const panel = page.getByRole('complementary', { name: 'Conversation control plane' })
  await expect(panel.getByRole('status')).toContainText('Created immutable Project Brief checkpoint r4')
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST' && item.path.endsWith('/intent-proposals/generate'),
  )).toBe(true)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path.includes(`/documents/${projectBrief.artifact.id}/revisions`),
  )).toBe(true)
  const manifestRequest = state.requests.find((item) =>
    item.method === 'POST'
      && item.path.endsWith('/input-manifests')
      && (item.body as { jobType?: string })?.jobType === 'conversation.workflow_intent')
  expect(manifestRequest?.body).toMatchObject({
    baseRevision: {
      artifactId: projectBrief.artifact.id,
      revisionId: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb4',
      contentHash: hash('a'),
    },
    sources: [{
      ref: {
        artifactId: projectBrief.artifact.id,
        revisionId: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb4',
        contentHash: hash('a'),
      },
      purpose: 'project_brief',
    }],
  })
  expect((manifestRequest?.body as { constraints?: Record<string, unknown> })?.constraints)
    .not.toHaveProperty('messages')
})

test('Workbench conversation commands decouple governance and run manifests while enforcing exact runtime inputs', async ({ page }) => {
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
    'frozen Workbench bundle is not linked to the expected workflow run',
  )
  expect(state.requests.some((item) => item.path === '/v1/build-manifests/build-1')).toBe(true)
  expect(state.requests.some((item) => item.path.endsWith('/build-manifests/build-1/generate'))).toBe(false)

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
      implementationProposalId: 'implementation-1',
    },
  })
  expect(commandRequest?.headers['if-match']).toBe(
    '"conversation-command:77777777-7777-4777-8777-777777777777:1"',
  )
  expect(commandRequest?.headers['idempotency-key']).toBeTruthy()
})

test('Workbench conversation commands select their exact DAG group before generation', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    multiWorkbenchGroups: true,
  })
  const conversationId = state.conversations[0].id
  const trigger = conversationMessage(
    '99999999-9999-4999-8999-999999999992',
    conversationId,
    1,
    'user',
    'Generate only the second Workbench group.',
  )
  const proposal = workbenchIntentProposal(conversationId, trigger.id, {
    expectedRunId: 'run-groups',
    expectedBundleId: 'bundle-group-b',
  })
  state.conversationMessages = [trigger, conversationMessage(
    proposal.assistantMessageId,
    conversationId,
    2,
    'assistant',
    'Generate from the exact second DAG group.',
    proposal.id,
  )]
  state.intentProposal = proposal
  state.conversationCommand = conversationCommand(proposal)
  if (!state.multiWorkbench) throw new Error('multi Workbench fixture is unavailable')
  delete state.multiWorkbench.proposals['proposal-group-b']
  state.multiWorkbench.currentProposalIds['bundle-group-b'] = undefined

  await page.goto('/workbench/complete?view=code&runId=run-groups')
  const panel = page.getByRole('complementary', { name: 'Conversation control plane' })
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()

  await expect(page).toHaveURL(/workbenchNodeKey=workbench-b/)
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path === '/v1/build-manifests/bundle-group-b/generate',
  )).toBe(true)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path === '/v1/build-manifests/bundle-group-a/generate',
  )).toBe(false)
  const commandRequest = state.requests.find((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'))
  expect(commandRequest?.body).toEqual({
    workbenchResult: {
      runId: 'run-groups',
      bundleId: 'bundle-group-b',
      implementationProposalId: 'proposal-checkout',
    },
  })
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
  await expect(page.getByText('ready', { exact: true }).last()).toBeVisible()
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

test('multi-bundle Workbench rebases exact shared-file inputs and survives both reload boundaries', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    multiBundleWorkbench: true,
  })
  await page.goto('/workbench/complete?view=code&runId=run-multi')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()

  await expect(page.getByText('Frozen application build manifest')).toBeVisible()
  await expect(page.getByText('page-home', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '2 page-checkout blocked' }).click()
  await expect(page.getByRole('button', { name: 'Blocked by order' })).toBeDisabled()
  await expect(page.getByRole('status')).toContainText(
    'Apply page-home before generating or proposing file changes for page-checkout',
  )
  await page.getByPlaceholder('src/new-file.ts').fill('src/blocked.ts')
  await page.getByRole('button', { name: 'Prepare new file proposal' }).click()
  await expect(page.getByRole('button', { name: 'Propose change' })).toBeDisabled()
  expect(state.requests.some((item) =>
    item.method === 'POST' && item.path.includes('/implementation-proposals'),
  )).toBe(false)
  await page.getByRole('button', { name: '1 page-home review' }).click()
  await page.getByRole('button', { name: 'Accept pending' }).click()
  await page.getByRole('button', { name: 'Apply and continue' }).click()
  await expect(page.getByRole('button', { name: 'Rebase next bundle' })).toBeVisible()

  const firstReloadRequestIndex = state.requests.length
  await page.reload()
  await expect(page.getByText('page-checkout', { exact: true })).toBeVisible()
  await expect(page.getByRole('status')).toContainText('workspace r1 (workspace-r1)')
  await expect(page.getByRole('button', { name: 'Generate proposal' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Rebase next bundle' })).toBeVisible()
  expect(state.requests.slice(firstReloadRequestIndex).some((item) =>
    item.method === 'GET' && item.path === '/v1/revisions/workspace-r1',
  )).toBe(true)

  await page.getByRole('button', { name: 'Rebase next bundle' }).click()
  await expect(page.getByText(/bundle-checkout → bundle-checkout-w1/)).toBeVisible()
  await page.getByRole('button', { name: 'Generate proposal' }).click()
  await expect(page.getByText('src/shared.ts', { exact: true }).first()).toBeVisible()
  await page.getByRole('button', { name: 'Accept pending' }).click()
  await expect(page.getByText('ready', { exact: true }).last()).toBeVisible()

  await page.reload()
  await expect(page.getByText(/bundle-checkout → bundle-checkout-w1/)).toBeVisible()
  await expect(page.getByText('ready', { exact: true }).last()).toBeVisible()
  await expect(page.getByRole('button', { name: 'src/shared.ts file.upsert · accepted' })).toBeVisible()
  await page.getByRole('button', { name: 'Apply and complete Workbench' }).click()

  const rebaseRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path === '/v1/build-manifests/bundle-checkout/rebase')
  expect(rebaseRequest?.body).toEqual({
    workspaceRevision: {
      artifactId: 'workspace-application',
      revisionId: 'workspace-r1',
      contentHash: hash('1'),
    },
  })
  const rebaseIndex = state.requests.findIndex((item) => item === rebaseRequest)
  const generateIndex = state.requests.findIndex((item) =>
    item.method === 'POST'
      && item.path === '/v1/build-manifests/bundle-checkout-w1/generate')
  expect(generateIndex).toBeGreaterThan(rebaseIndex)
  expect(state.multiWorkbench?.proposals['proposal-checkout'].operations[0].expectedHash)
    .toBe(hash('a'))

  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/workflow-runs/run-multi/resume`,
  )).toBe(true)
  const completion = state.requests.find((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/workflow-runs/run-multi/resume`)
  expect(completion?.body).toEqual({
    nodeKey: 'workbench',
    output: {
      implementationProposalIds: ['proposal-home', 'proposal-checkout'],
      workspaceRevision: {
        artifactId: 'workspace-application',
        revisionId: 'workspace-r2',
        contentHash: hash('2'),
      },
    },
  })
})

test('Workbench rebases from the active derived leaf when the project workspace advances again', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    multiBundleWorkbench: true,
  })
  await page.goto('/workbench/complete?view=code&runId=run-multi')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()

  await page.getByRole('button', { name: 'Accept pending' }).click()
  await page.getByRole('button', { name: 'Apply and continue' }).click()
  await page.getByRole('button', { name: 'Rebase next bundle' }).click()
  await expect(page.getByText(/bundle-checkout → bundle-checkout-w1/)).toBeVisible()

  if (!state.multiWorkbench) throw new Error('Expected multi-bundle mock state.')
  state.multiWorkbench.currentWorkspaceRevision = multiApplicationRevision(2)
  await page.reload()
  await expect(page.getByText('Frozen application build manifest')).toBeVisible()
  await expect(page.getByRole('status')).toContainText(
    'active page bundle bundle-checkout-w1 (order root bundle-checkout)',
  )
  await expect(page.getByRole('status')).toContainText('workspace r2 (workspace-r2)')
  await page.getByRole('button', { name: 'Rebase next bundle' }).click()
  await expect(page.getByText(/bundle-checkout → bundle-checkout-w2/)).toBeVisible()

  const rebases = state.requests.filter((item) =>
    item.method === 'POST' && item.path.endsWith('/rebase'))
  expect(rebases.map((item) => item.path)).toEqual([
    '/v1/build-manifests/bundle-checkout/rebase',
    '/v1/build-manifests/bundle-checkout-w1/rebase',
  ])
  expect(rebases[1].body).toEqual({
    workspaceRevision: {
      artifactId: 'workspace-application',
      revisionId: 'workspace-r2',
      contentHash: hash('2'),
    },
  })
})

test('node-scoped Workbench groups hydrate independently and complete only the selected DAG node', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    multiWorkbenchGroups: true,
  })
  await page.goto('/workbench/complete?view=code&runId=run-groups')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()

  await expect(page.getByRole('button', {
    name: 'Workbench group workbench-a waiting input',
  })).toBeVisible()
  await expect(page.getByText(/bundle-group-a ·/)).toBeVisible()
  expect(state.requests.some((item) =>
    item.path === '/v1/build-manifests/bundle-group-a/lineage-state',
  )).toBe(true)
  expect(state.requests.some((item) =>
    item.path === '/v1/build-manifests/bundle-group-b/lineage-state',
  )).toBe(false)

  await page.getByRole('button', {
    name: 'Workbench group workbench-b waiting input',
  }).click()
  await expect(page).toHaveURL(/workbenchNodeKey=workbench-b/)
  await expect(page.getByText(/bundle-group-b ·/)).toBeVisible()
  expect(state.requests.some((item) =>
    item.path === '/v1/build-manifests/bundle-group-b/lineage-state',
  )).toBe(true)

  const reloadRequestIndex = state.requests.length
  await page.reload()
  await expect(page).toHaveURL(/workbenchNodeKey=workbench-b/)
  await expect.poll(() => state.requests.slice(reloadRequestIndex)
    .filter((item) => item.path.endsWith('/lineage-state'))
    .map((item) => item.path)).toContain('/v1/build-manifests/bundle-group-b/lineage-state')
  await expect(page.getByText(/bundle-group-b ·/)).toBeVisible()
  const reloadRequests = state.requests.slice(reloadRequestIndex)
  expect(reloadRequests.some((item) =>
    item.path === '/v1/build-manifests/bundle-group-b/lineage-state',
  )).toBe(true)
  expect(reloadRequests.some((item) =>
    item.path === '/v1/build-manifests/bundle-group-a/lineage-state',
  )).toBe(false)

  await page.getByRole('button', {
    name: 'Workbench group workbench-a waiting input',
  }).click()
  await expect(page.getByText(/bundle-group-a ·/)).toBeVisible()
  await page.getByRole('button', { name: 'Complete Workbench' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/workflow-runs/run-groups/resume`,
  )).toBe(true)
  const completion = state.requests.find((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/workflow-runs/run-groups/resume`)
  expect(completion?.body).toEqual({
    nodeKey: 'workbench-a',
    output: {
      implementationProposalIds: ['proposal-group-a'],
      workspaceRevision: {
        artifactId: 'workspace-application',
        revisionId: 'workspace-r1',
        contentHash: hash('1'),
      },
    },
  })
})

test('Design Import Center freezes an upload, reviews its proposal, and applies a Prototype revision', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/imports`)

  await expect(page.getByTestId('design-import-center')).toBeVisible()
  await expect(page.getByTestId('design-source-figma')).toContainText('Remote not configured')
  await expect(page.getByTestId('design-source-ladle')).toBeVisible()
  await page.getByTestId('design-source-figma').click()
  await page.getByTestId('design-import-file').setInputFiles({
    name: 'dashboard.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify({
      document: {
        id: 'document', name: 'Dashboard export', type: 'DOCUMENT',
        children: [
          { id: 'dashboard-frame', name: 'Dashboard frame', type: 'FRAME' },
          { id: 'task-card', name: 'Task card', type: 'COMPONENT' },
        ],
      },
    })),
  })
  await page.getByLabel('Frame / story IDs (optional)').fill('dashboard-frame, task-card')
  await page.getByTestId('design-import-submit').click()

  await expect(page.getByTestId('design-import-list')).toContainText('dashboard')
  await expect(page.getByText('open', { exact: true })).toBeVisible()
  const createRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/design-imports`)
  expect(createRequest?.headers['idempotency-key']).toBeTruthy()
  expect(createRequest?.body).toMatchObject({
    sourceKind: 'figma',
    mode: 'upload',
    selectedFrameIds: ['dashboard-frame', 'task-card'],
    pageSpecRevision: {
      artifactId: pageSpec.artifact.id,
      revisionId: pageSpecRevision.id,
      contentHash: pageSpecRevision.contentHash,
    },
    file: { name: 'dashboard.json', mediaType: 'application/json' },
  })

  await page.getByTestId(`design-import-approve-${state.designImports[0].id}`).click()
  await expect(page.getByText('applied', { exact: true })).toBeVisible()
  await expect(page.getByTestId('design-import-list')).toContainText('99999999…999992')
  const decisionRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path === `/v1/design-imports/${state.designImports[0].id}/decision`)
  expect(decisionRequest?.headers['if-match']).toBe('"design-import:99999999-9999-4999-8999-999999999999:4"')
  expect(decisionRequest?.headers['idempotency-key']).toBeTruthy()
  expect(decisionRequest?.body).toEqual({ decision: 'approve', version: 4 })
})

test('Design Import Center keeps server-open state when approval CAS fails', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, designImportDecisionFails: true })
  state.designImports = [designImportRecord({
    sourceKind: 'figma',
    title: 'Failed approval stays open',
    file: { name: 'dashboard.json', mediaType: 'application/json', contentBase64: 'e30=' },
    pageSpecRevision: {
      artifactId: pageSpec.artifact.id,
      revisionId: pageSpecRevision.id,
      contentHash: pageSpecRevision.contentHash,
    },
  })]
  await page.goto(`/team/acme/project/${project.id}/imports`)
  await page.getByTestId(`design-import-approve-${state.designImports[0].id}`).click()
  await expect(page.getByTestId('design-import-center').getByRole('alert')).toContainText('changed since it was loaded')
  await expect(page.getByText('open', { exact: true })).toBeVisible()
  expect(state.designImports[0].status).toBe('open')
})

test('document dependency graph opens the exact document, PageSpec, and prototype workspaces', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  const graphUrl = `/team/acme/project/${project.id}/graph`

  await page.goto(graphUrl)
  await page.getByRole('button', { name: 'Open artifact workspace' }).click()
  await expect(page).toHaveURL(new RegExp(`/editor\\?artifactId=${projectBrief.artifact.id}`))
  await expect(page.getByText('Project Brief', { exact: true }).first()).toBeVisible()

  await page.goto(graphUrl)
  await page.locator('button').filter({ hasText: 'Dashboard PageSpec' }).first().click()
  await page.getByRole('button', { name: 'Open artifact workspace' }).click()
  await expect(page).toHaveURL(new RegExp(`/blueprint\\?artifactId=${pageSpec.artifact.id}`))
  await expect(page.getByText('Dashboard PageSpec', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'States 4' })).toBeVisible()

  await page.goto(graphUrl)
  await page.locator('button').filter({ hasText: 'Dashboard Prototype' }).first().click()
  await page.getByRole('button', { name: 'Open artifact workspace' }).click()
  await expect(page).toHaveURL(new RegExp(`/prototype\\?artifactId=${approvedPrototype.artifact.id}`))
  await expect(page.getByText('Dashboard Prototype', { exact: true }).first()).toBeVisible()
})

test('document graph reloads its server projection after a project WebSocket event', async ({ page }) => {
  const realtime = await installProjectWebSocketMock(page)
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/graph`)
  await expect(page.getByTestId('server-document-graph')).toContainText('Project Brief')
  const before = state.requests.filter((item) =>
    item.method === 'GET' && item.path === `/v1/projects/${project.id}/document-graph`).length

  state.graphBriefTitle = 'Project Brief · realtime refresh'
  await expect.poll(() => realtime.ready(project.id)).toBe(true)
  realtime.emit('document.downstream_generated', project.id, {
    projectId: project.id, artifactId: projectBrief.artifact.id,
  })

  await expect(page.getByTestId('server-document-graph')).toContainText('Project Brief · realtime refresh')
  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'GET' && item.path === `/v1/projects/${project.id}/document-graph`).length).toBeGreaterThan(before)
})

test('document bindings preserve local edits when a remote replacement event arrives', async ({ page }) => {
  const realtime = await installProjectWebSocketMock(page)
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/editor?artifactId=${projectBrief.artifact.id}`)
  await page.getByRole('main').getByRole('button', { name: 'Collaboration 2', exact: true }).click()
  const panel = page.getByTestId('document-collaboration-panel')
  await expect(panel).toContainText('Flow Reviewer')
  const reviewerRole = panel.locator('select').nth(1)
  await reviewerRole.selectOption('watcher')
  await expect(reviewerRole).toHaveValue('watcher')

  state.bindingVersion = 2
  state.bindingReviewerRole = 'assignee'
  await expect.poll(() => realtime.ready(project.id)).toBe(true)
  realtime.emit('artifact.member_bindings_replaced', project.id, {
    projectId: project.id, artifactId: projectBrief.artifact.id, version: 2,
  })

  await expect(page.getByTestId('document-bindings-conflict')).toBeVisible()
  await expect(reviewerRole).toHaveValue('watcher')
  await page.getByRole('button', { name: 'Reload server bindings' }).click()
  await expect(page.getByTestId('document-bindings-conflict')).toBeHidden()
  await expect(panel.locator('select').nth(1)).toHaveValue('assignee')
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

test('Prototype Studio decides and applies an AI proposal before freezing its attributed revision', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    prototypeProposal: true,
  })
  await page.goto(`/team/acme/project/${project.id}/prototype`)

  await page.getByRole('button', { name: 'trace', exact: true }).click()
  await expect(page.getByText('prototype-proposal-1', { exact: true })).toBeVisible()
  await page.getByRole('button', {
    name: 'Accept proposal operation prototype-operation-name',
  }).click()
  await expect(page.getByText('accepted', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Accept all pending proposal operations' }).click()
  const apply = page.getByRole('button', { name: 'Apply reviewed prototype proposal' })
  await expect(apply).toBeEnabled()
  await apply.click()
  await expect(page.getByText('Applied to the server draft.')).toBeVisible()
  await expect(page.getByText('AI-reviewed Page', { exact: true }).first()).toBeVisible()

  await page.getByRole('button', { name: 'Revision + review' }).click()
  await expect.poll(() => state.prototypeCreatedRevision?.proposalId).toBe('prototype-proposal-1')
  expect(state.prototypeCreatedRevision).toMatchObject({
    proposalId: 'prototype-proposal-1',
    sourceManifestId: 'prototype-manifest-1',
  })

  const decisions = state.requests.filter((item) =>
    item.method === 'POST'
      && item.path === '/v1/output-proposals/prototype-proposal-1/decisions')
  expect(decisions.map((item) => item.body)).toEqual([
    {
      operationId: 'prototype-operation-name',
      decision: 'accepted',
      version: 1,
    },
    {
      operationId: 'prototype-operation-policy',
      decision: 'accepted',
      version: 2,
    },
  ])
  expect(decisions.map((item) => item.headers['if-match'])).toEqual([
    '"output-proposal:prototype-proposal-1:1"',
    '"output-proposal:prototype-proposal-1:2"',
  ])
  const applyIndex = state.requests.findIndex((item) =>
    item.method === 'POST'
      && item.path === '/v1/output-proposals/prototype-proposal-1/apply')
  const revisionIndex = state.requests.findIndex((item) =>
    item.method === 'POST'
      && item.path === `/v1/prototypes/${approvedPrototype.artifact.id}/revisions`)
  expect(revisionIndex).toBeGreaterThan(applyIndex)
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

test('Blueprint Composer inserts module packs, multi-selects them, and persists a capability group', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/blueprint`)

  await page.getByRole('button', { name: /feature\s+Order management/i }).click()
  await page.getByRole('button', { name: /page\s+Dashboard/i }).click({ modifiers: ['Meta'] })
  await page.getByRole('button', { name: /Group as capability \(2\)/ }).click()
  await expect(page.getByText('Capability 1', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'UI', exact: true }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'PATCH'
      && item.path === `/v1/blueprints/${blueprint.artifact.id}/draft`
      && ((item.body as { content?: { layout?: { groups?: unknown[] } } })?.content?.layout?.groups?.length ?? 0) === 1,
  )).toBe(true)
  const groupedDraft = state.requests.findLast((item) =>
    item.method === 'PATCH' && item.path === `/v1/blueprints/${blueprint.artifact.id}/draft`)
  const groupedNodeIds = (groupedDraft?.body as { content?: { layout?: { groups?: Array<{ nodeIds: string[] }> } } })?.content?.layout?.groups?.[0]?.nodeIds ?? []
  expect(groupedNodeIds).toEqual(['feature-orders', 'page-dashboard'])
  await page.getByLabel('Capability name Capability 1').fill('Checkout capability')
  await page.getByRole('button', { name: 'Select capability Checkout capability', exact: true }).click()
  await page.getByRole('button', { name: 'Generate docs from selection' }).click()
  await expect(page.getByText(/Documentation proposal created from 2 frozen nodes/)).toBeVisible()
  const compile = state.requests.findLast((item) => item.path.endsWith('/blueprint-selections/compile'))
  expect((compile?.body as { nodeIds?: string[] })?.nodeIds).toEqual([...groupedNodeIds].sort())
})

test('Generate docs from selection creates an AI proposal without prototype or workflow side effects', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/blueprint`)

  await page.getByRole('button', { name: 'Generate docs from selection' }).click()
  await expect(page.getByText(/Documentation proposal created from 1 frozen nodes/)).toBeVisible()
  const selection = state.requests.find((item) => item.path.endsWith('/blueprint-selections/compile'))
  expect(selection?.body).toMatchObject({ nodeIds: ['feature-orders'] })
  expect(selection?.headers['if-match']).toBe(blueprint.artifact.etag)
  const derivedManifest = state.requests.findLast((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/input-manifests`)
  expect(derivedManifest?.body).toMatchObject({
    constraints: {
      parentSelectionManifest: {
        id: 'selection-manifest-feature-orders',
        hash: hash('9'),
      },
      frozenSelectionScope: {
        nodeIds: ['feature-orders'],
      },
    },
  })
  expect(state.requests.some((item) => item.method === 'POST' && item.path === `/v1/projects/${project.id}/documents`)).toBe(true)
  expect(state.requests.some((item) => item.method === 'POST' && item.path === `/v1/projects/${project.id}/prototypes`)).toBe(false)
  expect(state.requests.some((item) => item.method === 'POST' && item.path === `/v1/projects/${project.id}/workflow-runs`)).toBe(false)
})

test('Create prototypes from selection uses the exact approved PageSpec without starting Workbench', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, prototypes: 'none' })
  await page.goto(`/team/acme/project/${project.id}/blueprint`)

  await page.getByRole('button', { name: /page\s+Dashboard/i }).click()
  await page.getByRole('button', { name: 'Create prototypes from selection' }).click()
  await expect(page.getByText(/1 formal Prototype draft created from exact PageSpec revisions/)).toBeVisible()
  const prototypeRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/prototypes`)
  expect(prototypeRequest?.body).toMatchObject({
    pageSpecRevision: {
      artifactId: pageSpecRevision.artifactId,
      revisionId: pageSpecRevision.id,
      contentHash: pageSpecRevision.contentHash,
    },
  })
  expect(state.requests.some((item) => item.method === 'POST' && item.path === `/v1/projects/${project.id}/workflow-runs`)).toBe(false)
})

test('Use selection in workflow starts the selection DAG and does not create documents or prototypes', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/blueprint`)

  await page.getByRole('button', { name: /page\s+Dashboard/i }).click()
  await page.getByRole('button', { name: 'Use selection in workflow / Workbench' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/workflow-runs`
      && (item.body as { definitionVersionId?: string })?.definitionVersionId === selectionWorkflowDefinition.versionId,
  )).toBe(true)
  const compile = state.requests.findLast((item) => item.path.endsWith('/blueprint-selections/compile'))
  expect(compile?.body).toMatchObject({ nodeIds: ['page-dashboard'] })
  expect(state.requests.some((item) => item.method === 'POST' && item.path === `/v1/projects/${project.id}/documents`)).toBe(false)
  expect(state.requests.some((item) => item.method === 'POST' && item.path === `/v1/projects/${project.id}/prototypes`)).toBe(false)
  await expect(page.getByRole('heading', { name: 'No applied application revision' })).toBeVisible()
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

function workbenchIntentProposal(
  conversationId: string,
  triggerMessageId: string,
  target: {
    readonly expectedRunId: string
    readonly expectedBundleId: string
    readonly definitionVersionId?: string
  } = {
    expectedRunId: '88888888-8888-4888-8888-888888888888',
    expectedBundleId: 'build-1',
  },
) {
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
    expectedRunId: target.expectedRunId,
    expectedBundleId: target.expectedBundleId,
  }
  return {
    ...generated,
    kind: 'workbench_instruction' as const,
    suggestedDefinitionVersionId: target.definitionVersionId ?? generated.suggestedDefinitionVersionId,
    status: 'accepted' as 'pending' | 'accepted' | 'rejected',
    version: 2,
    etag: `"intent-proposal:${generated.id}:2"`,
    scope: { conversationIntent: { workbenchInstruction } },
    workbenchInstruction,
    decidedBy: user.id as string | undefined,
    decidedAt: now as string | undefined,
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

function staleProjectBrief(stale = true) {
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
        summary: stale
          ? 'This current draft must be checkpointed before becoming an AI source.'
          : briefRevision.content.summary,
      },
      contentHash: stale ? hash('a') : briefRevision.contentHash,
      updatedBy: user.id,
      updatedAt: now,
      etag: '"project-brief-draft:4"',
    },
  }
}

function workflowDefinitionFor(options: MockPlatformOptions) {
  return {
    ...workflowDefinition,
    definition: {
      ...workflowDefinition.definition,
      nodes: workflowDefinition.definition.nodes.map((node) => ({
        ...node,
        artifactInput: {
          ...node.artifactInput,
          requireApproved: options.workflowRequireApproved ?? node.artifactInput.requireApproved,
        },
      })),
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
      artifactKey: `${kind}-${id}`,
      title,
      lifecycle: 'active',
      status: 'approved',
      syncStatus: 'current',
      deliveryStatus: 'incomplete',
      latestRevisionId: revision.id,
      approvedRevisionId: revision.id,
      version: 1,
      createdBy: user.id,
      createdAt: now,
      updatedAt: now,
      etag: `"artifact:${id}:1"`,
    },
    latestRevision: revision,
    approvedRevision: revision,
  }
}

function graphArtifactNode(
  resource: ReturnType<typeof versionedArtifact>,
  entityType: 'document' | 'page',
  title = resource.artifact.title,
) {
  const revision = resource.approvedRevision as {
    id: string
    artifactId: string
    revisionNumber: number
    contentHash: string
  }
  return {
    id: `artifact:${resource.artifact.id}`,
    entityId: resource.artifact.id,
    entityType,
    artifactKind: resource.artifact.kind,
    title,
    status: 'approved',
    revision: {
      artifactId: revision.artifactId,
      revisionId: revision.id,
      revisionNumber: revision.revisionNumber,
      contentHash: revision.contentHash,
    },
    memberBindings: [],
    metadata: {},
    updatedAt: resource.artifact.updatedAt,
  }
}

function designImportRecord(input: {
  sourceKind: 'figma'
  title?: string
  file: { name: string; mediaType: string; contentBase64: string }
  selectedFrameIds?: string[]
  pageSpecRevision: { artifactId: string; revisionId: string; contentHash: string }
  targetPrototypeArtifactId?: string
}) {
  const id = '99999999-9999-4999-8999-999999999999'
  const prototypeArtifactId = input.targetPrototypeArtifactId ?? '99999999-9999-4999-8999-999999999991'
  const baseRevision = {
    artifactId: prototypeArtifactId,
    revisionId: '99999999-9999-4999-8999-999999999990',
    contentHash: hash('6'),
  }
  const manifest = {
    id: '99999999-9999-4999-8999-999999999993',
    projectId: project.id,
    jobType: 'design_import_to_prototype',
    baseRevision,
    sources: [{ ref: input.pageSpecRevision, purpose: 'page_spec' }],
    constraints: {
      snapshot: { contentHash: hash('7'), rawContentHash: hash('8') },
      trust: { externalSourceIsFact: false, reviewRequired: true },
    },
    outputSchemaVersion: 'prototype@1',
    createdBy: user.id,
    createdAt: now,
    hash: hash('9'),
  }
  const proposal = {
    id: '99999999-9999-4999-8999-999999999994',
    projectId: project.id,
    artifactId: prototypeArtifactId,
    manifest: { id: manifest.id, hash: manifest.hash },
    baseRevision,
    payloadHash: hash('a'),
    status: 'open' as const,
    operations: [{
      id: `design-import-${id}`,
      kind: 'replace' as const,
      path: '',
      value: prototypeContent(),
      decision: 'pending' as const,
      rationale: 'Convert the frozen design snapshot.',
    }],
    assumptions: ['External source is not a project fact.'],
    questions: ['Review the mapping.'],
    version: 1,
    createdBy: user.id,
    createdAt: now,
  }
  return {
    id,
    projectId: project.id,
    status: 'open' as 'open' | 'applied' | 'rejected',
    version: 4,
    etag: `"design-import:${id}:4"`,
    snapshot: {
      contentHash: hash('7'),
      rawContentHash: hash('8'),
      sourceKind: input.sourceKind,
      sourceName: input.title ?? input.file.name,
      mode: 'upload' as const,
      fileName: input.file.name,
      mediaType: input.file.mediaType,
      byteSize: Math.floor(input.file.contentBase64.length * 0.75),
      capturedAt: now,
      selectedFrameIds: input.selectedFrameIds ?? [],
    },
    pageSpecRevision: input.pageSpecRevision,
    prototypeArtifactId,
    baseRevisionId: baseRevision.revisionId,
    inputManifestId: manifest.id,
    outputProposalId: proposal.id,
    operationId: proposal.operations[0].id,
    createsPrototype: !input.targetPrototypeArtifactId,
    createdBy: user.id,
    createdAt: now,
    updatedAt: now,
    manifest,
    proposal,
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

function selectionWorkflowRun(id: string) {
  return {
    id,
    projectId: project.id,
    definitionVersionId: selectionWorkflowDefinition.versionId,
    definition: {
      id: selectionWorkflowDefinition.id,
      version: selectionWorkflowDefinition.version,
      hash: selectionWorkflowDefinition.contentHash,
    },
    inputManifest: { id: 'selection-manifest-page-dashboard', hash: hash('8') },
    status: 'waiting_input' as const,
    scope: { blueprintSelection: { selectionId: `sha256:${hash('s')}` } },
    context: { values: {}, nodes: {}, slices: {} },
    eventCursor: 1,
    startedBy: user.id,
    createdAt: now,
    updatedAt: now,
    nodes: [{
      id: `${id}-selection`, runId: id, key: 'selection', definitionNodeId: 'selection',
      type: 'artifact_input', status: 'waiting_input', attempt: 0,
      availableAt: now, createdAt: now, updatedAt: now,
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

function artifactProposal(
  status: 'open' | 'reviewing' | 'ready' | 'applied' = 'open',
  decisions: readonly ('pending' | 'accepted' | 'rejected' | 'applied')[] = ['pending', 'pending'],
  version = 1,
) {
  const operations = [
    {
      id: 'prototype-operation-name',
      kind: 'replace' as const,
      path: '/layers/layer-root/name',
      value: 'AI-reviewed Page',
      rationale: 'Give the root frame a reviewed semantic name.',
      decision: decisions[0] ?? 'pending',
    },
    {
      id: 'prototype-operation-policy',
      kind: 'add' as const,
      path: '/layers/layer-root/properties/aiReviewed',
      value: true,
      rationale: 'Record the approved AI design operation.',
      decision: decisions[1] ?? 'pending',
    },
  ]
  return {
    id: 'prototype-proposal-1',
    projectId: project.id,
    artifactId: approvedPrototype.artifact.id,
    manifest: { id: 'prototype-manifest-1', hash: hash('c') },
    baseRevision: exactRevision(
      approvedPrototypeRevision.artifactId,
      approvedPrototypeRevision.id,
      approvedPrototypeRevision.revisionNumber,
      approvedPrototypeRevision.contentHash,
    ),
    payloadHash: hash('d'),
    status,
    operations,
    assumptions: [],
    questions: [],
    version,
    createdBy: user.id,
    createdAt: now,
    appliedAt: undefined as string | undefined,
  }
}

interface MockMultiWorkbenchState {
  currentWorkspaceRevision: ReturnType<typeof multiApplicationRevision> | undefined
  bundles: Record<string, ReturnType<typeof multiBuildManifest>>
  proposals: Record<string, ReturnType<typeof multiImplementationProposal>>
  activeBundleIds: Record<string, string>
  currentProposalIds: Record<string, string | undefined>
}

function multiWorkbenchState(): MockMultiWorkbenchState {
  const home = multiBuildManifest('bundle-home', 'page-home')
  const checkout = multiBuildManifest(
    'bundle-checkout',
    'page-checkout',
    undefined,
    undefined,
    exactRevision('workspace-application', 'workspace-r0', 0, hash('0')),
  )
  const proposal = multiImplementationProposal(
    'proposal-home',
    home.id,
    'open',
    'pending',
    1,
    'Home establishes the shared application shell.',
  )
  return {
    currentWorkspaceRevision: undefined,
    bundles: { [home.id]: home, [checkout.id]: checkout },
    proposals: { [proposal.id]: proposal },
    activeBundleIds: {
      'bundle-home': home.id,
      'bundle-checkout': checkout.id,
    },
    currentProposalIds: {
      'bundle-home': proposal.id,
      'bundle-checkout': undefined,
    },
  }
}

function multiGroupWorkbenchState(): MockMultiWorkbenchState {
  const bundleA = {
    ...multiBuildManifest('bundle-group-a', 'slice-group-a'),
    workflowRunId: 'run-groups',
  }
  const bundleB = {
    ...multiBuildManifest('bundle-group-b', 'slice-group-b'),
    workflowRunId: 'run-groups',
    currentWorkspaceRevision: exactRevision(
      'workspace-application',
      'workspace-r1',
      1,
      hash('1'),
    ),
  }
  const proposalA = multiImplementationProposal(
    'proposal-group-a',
    bundleA.id,
    'applied',
    'applied',
    3,
    'Group A applied output.',
  )
  const proposalB = multiImplementationProposal(
    'proposal-group-b',
    bundleB.id,
    'ready',
    'accepted',
    2,
    'Group B reviewed output.',
  )
  return {
    currentWorkspaceRevision: multiApplicationRevision(1),
    bundles: { [bundleA.id]: bundleA, [bundleB.id]: bundleB },
    proposals: { [proposalA.id]: proposalA, [proposalB.id]: proposalB },
    activeBundleIds: {
      [bundleA.id]: bundleA.id,
      [bundleB.id]: bundleB.id,
    },
    currentProposalIds: {
      [bundleA.id]: proposalA.id,
      [bundleB.id]: proposalB.id,
    },
  }
}

function multiLineageState(state: MockMultiWorkbenchState, rootBundleId: string) {
  const activeBundleId = state.activeBundleIds[rootBundleId]
  const activeBundle = state.bundles[activeBundleId]
  const currentProposalId = state.currentProposalIds[rootBundleId]
  const currentProposal = currentProposalId ? state.proposals[currentProposalId] : undefined
  const currentWorkspaceRevision = state.currentWorkspaceRevision
    ? exactRevision(
        state.currentWorkspaceRevision.artifactId,
        state.currentWorkspaceRevision.id,
        state.currentWorkspaceRevision.revisionNumber,
        state.currentWorkspaceRevision.contentHash,
      )
    : undefined
  const lineage = Object.values(state.bundles)
    .filter((bundle) => (bundle.rootBuildManifestId ?? bundle.id) === rootBundleId)
    .map((bundle) => {
      const latestProposal = Object.values(state.proposals)
        .filter((proposal) => proposal.buildManifestId === bundle.id)
        .sort((left, right) => right.version - left.version)[0]
      return {
        bundleId: bundle.id,
        ...(bundle.derivedFromBuildManifestId
          ? { derivedFromBuildManifestId: bundle.derivedFromBuildManifestId }
          : {}),
        ...(bundle.currentWorkspaceRevision
          ? { workspaceRevision: bundle.currentWorkspaceRevision }
          : {}),
        status: bundle.id === activeBundleId ? 'active' : 'superseded',
        createdAt: bundle.createdAt,
        ...(latestProposal ? {
          latestProposal: {
            id: latestProposal.id,
            status: latestProposal.status,
            version: latestProposal.version,
            createdAt: latestProposal.createdAt,
          },
        } : {}),
      }
    })
  return {
    rootBundleId,
    activeBundle,
    ...(currentProposal ? { currentProposal } : {}),
    ...(currentWorkspaceRevision ? { currentWorkspaceRevision } : {}),
    lineage,
  }
}

function multiBuildManifest(
  id: string,
  deliverySliceId: string,
  rootBuildManifestId?: string,
  derivedFromBuildManifestId?: string,
  currentWorkspaceRevision?: ReturnType<typeof exactRevision>,
) {
  return {
    ...buildManifest('run-multi'),
    id,
    deliverySliceId,
    ...(rootBuildManifestId ? { rootBuildManifestId } : {}),
    ...(derivedFromBuildManifestId ? { derivedFromBuildManifestId } : {}),
    ...(currentWorkspaceRevision ? { currentWorkspaceRevision } : {}),
    contentHash: id === 'bundle-checkout-w1' ? hash('7') : hash('6'),
  }
}

function multiImplementationProposal(
  id: string,
  buildManifestId: string,
  status: 'open' | 'ready' | 'applied',
  decision: 'pending' | 'accepted' | 'rejected' | 'applied',
  version: number,
  rationale: string,
  expectedHash?: string,
) {
  return {
    ...implementationProposal(status, decision === 'rejected' ? 'pending' : decision, version),
    id,
    buildManifestId,
    operations: [{
      id: `${id}-operation`,
      kind: 'file.upsert' as const,
      path: 'src/shared.ts',
      content: id === 'proposal-home'
        ? "export const currentPage = 'home'\n"
        : "export const currentPage = 'checkout'\n",
      language: 'typescript',
      ...(expectedHash ? { expectedHash } : {}),
      dependsOn: [],
      rationale,
      traceSource: [approvedPrototypeRevision.id],
      decision,
    }],
    payloadHash: id === 'proposal-home' ? hash('4') : hash('5'),
  }
}

function multiApplicationRevision(revisionNumber: number) {
  const checkout = revisionNumber === 2
  const sharedContent = checkout
    ? "export const currentPage = 'checkout'\n"
    : "export const currentPage = 'home'\n"
  return {
    id: `workspace-r${revisionNumber}`,
    artifactId: 'workspace-application',
    revisionNumber,
    contentHash: hash(String(revisionNumber)),
    status: 'approved',
    content: {
      schemaVersion: 1,
      id: 'workspace-application',
      name: 'Multi-page Application Workspace',
      revision: revisionNumber,
      createdAt: now,
      updatedAt: now,
      files: [{
        path: 'src/shared.ts',
        content: sharedContent,
        language: 'typescript',
        contentHash: checkout ? hash('b') : hash('a'),
      }],
    },
    createdBy: user.id,
    createdAt: now,
  }
}

function multiBundleWorkflowRun(status: 'waiting_input' | 'completed' = 'waiting_input') {
  return {
    id: 'run-multi',
    projectId: project.id,
    definitionVersionId: workflowDefinition.versionId,
    definition: { id: workflowDefinition.id, version: 2, hash: workflowDefinition.contentHash },
    inputManifest: { id: 'manifest-1', hash: hash('1') },
    status,
    scope: {},
    context: {
      values: {
        buildManifest: {
          bundleIds: ['bundle-home', 'bundle-checkout'],
          sliceIds: ['page-home', 'page-checkout'],
        },
      },
      nodes: {
        workbench: {
          output: {
            implementationProposals: [{
              bundleId: 'bundle-home',
              proposalId: 'proposal-home',
              payloadHash: hash('4'),
            }],
          },
        },
      },
      slices: {},
    },
    eventCursor: 1,
    startedBy: user.id,
    createdAt: now,
    updatedAt: now,
    nodes: [{
      id: 'run-multi-workbench',
      runId: 'run-multi',
      key: 'workbench',
      definitionNodeId: 'workbench',
      type: 'workbench_build',
      status,
      attempt: 0,
      availableAt: now,
      createdAt: now,
      updatedAt: now,
    }],
  }
}

function multiGroupWorkflowRun() {
  const manifestA = {
    bundleIds: ['bundle-group-a'],
    sliceIds: ['slice-group-a'],
    manifestGroupKey: 'manifest-group-a',
    hash: hash('a'),
  }
  const manifestB = {
    bundleIds: ['bundle-group-b'],
    sliceIds: ['slice-group-b'],
    manifestGroupKey: 'manifest-group-b',
    hash: hash('b'),
  }
  return {
    id: 'run-groups',
    projectId: project.id,
    definitionVersionId: workflowDefinition.versionId,
    definition: { id: workflowDefinition.id, version: 2, hash: workflowDefinition.contentHash },
    inputManifest: { id: 'manifest-1', hash: hash('1') },
    status: 'waiting_input' as const,
    scope: {},
    context: {
      values: { buildManifest: { bundleIds: ['global-must-not-be-used'] } },
      nodes: {
        'workbench-a': {
          definitionNodeId: 'workbench-a',
          maxAttempts: 3,
          timeoutNanos: 1,
          input: { bindings: [{ value: manifestA, output: manifestA }] },
          output: {
            implementationProposals: [{
              bundleId: 'bundle-group-a',
              proposalId: 'proposal-group-a',
            }],
          },
        },
        'workbench-b': {
          definitionNodeId: 'workbench-b',
          maxAttempts: 3,
          timeoutNanos: 1,
          input: { bindings: [{ value: manifestB, output: manifestB }] },
          output: {
            implementationProposals: [{
              bundleId: 'bundle-group-b',
              proposalId: 'proposal-group-b',
            }],
          },
        },
      },
      slices: {},
    },
    eventCursor: 1,
    startedBy: user.id,
    createdAt: now,
    updatedAt: now,
    nodes: [
      {
        id: 'run-groups-workbench-a',
        runId: 'run-groups',
        key: 'workbench-a',
        definitionNodeId: 'workbench-a',
        type: 'workbench_build' as const,
        status: 'waiting_input' as const,
        attempt: 0,
        availableAt: now,
        createdAt: now,
        updatedAt: now,
      },
      {
        id: 'run-groups-workbench-b',
        runId: 'run-groups',
        key: 'workbench-b',
        definitionNodeId: 'workbench-b',
        type: 'workbench_build' as const,
        status: 'waiting_input' as const,
        attempt: 0,
        availableAt: now,
        createdAt: now,
        updatedAt: now,
      },
    ],
  }
}
