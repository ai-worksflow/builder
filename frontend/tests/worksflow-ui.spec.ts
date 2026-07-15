import { expect, test, type Page, type Route } from '@playwright/test'

const now = '2026-07-10T08:00:00Z'
const hash = (character: string) => character.repeat(64)
const workflowExecutionProfile = { version: 'workflow-engine/v2', hash: 'dd247a77ce3cfa1095a575a238b93c4bd41dd991eac07e8b62ec170864470da1' }

interface Deferred {
  readonly promise: Promise<void>
  readonly release: () => void
}

function deferred(): Deferred {
  let release = () => {}
  const promise = new Promise<void>((resolve) => {
    release = resolve
  })
  return { promise, release }
}

async function openConversationPanel(page: Page) {
  await page.getByRole('button', { name: 'Conversation', exact: true }).click()
  const panel = page.getByRole('complementary', { name: 'Conversation control plane' })
  await expect(panel).toBeVisible()
  return panel
}

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
  governanceMode: 'team',
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
  version: 4,
  contentHash: hash('f'),
  executionProfile: workflowExecutionProfile,
  definition: {
    id: 'ffffffff-ffff-4fff-8fff-ffffffffffff',
    version: 4,
    name: 'Minimum application loop',
    schemaVersion: '4',
    executionProfile: workflowExecutionProfile,
    inputContract: {
      capability: 'project_brief',
      manifestJobTypes: ['conversation.workflow_intent', 'workflow_start'],
      artifactKinds: ['project_brief'], minimumArtifacts: 1, maximumArtifacts: 1, requireApproved: true,
      requiredSourcePurposes: ['project_brief'],
      manifestSchemaContracts: {
        'conversation.workflow_intent': 'workflow-intent-input/v1', workflow_start: 'workflow-input/v1',
      },
    },
    outputContract: {
      capability: 'application', producedArtifactKinds: ['workspace'], terminalOutcome: 'deployment', terminalNodeType: 'publish',
    },
    nodes: [{
      id: 'brief-input',
      name: 'Pinned Project Brief',
      type: 'artifact_input',
      artifactInput: { allowedTypes: ['document'], allowedKinds: ['project_brief'], requireApproved: true, minimumArtifacts: 1, maximumArtifacts: 1 },
    }, {
      id: 'solo-review',
      name: 'Owner review',
      type: 'review_gate',
      reviewGate: { requiredRole: 'owner', prohibitSelfReview: true },
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
  version: 3,
  contentHash: hash('a'),
  executionProfile: workflowExecutionProfile,
  definition: {
    id: 'abababab-abab-4bab-8bab-abababababab',
    version: 3,
    name: 'Build application from Blueprint selection',
    schemaVersion: '3',
    executionProfile: workflowExecutionProfile,
    inputContract: {
      capability: 'blueprint_selection', manifestJobTypes: ['blueprint.selection'],
      artifactKinds: ['blueprint'], minimumArtifacts: 2, maximumArtifacts: 101, requireApproved: true,
      requiredSourcePurposes: ['blueprint_selection_node', 'blueprint_selection_root'],
      manifestSchemaContracts: { 'blueprint.selection': 'blueprint-selection/v1' },
    },
    outputContract: {
      capability: 'application', producedArtifactKinds: ['workspace'], terminalOutcome: 'deployment', terminalNodeType: 'publish',
    },
    nodes: [{
      id: 'selection', name: 'Frozen Blueprint selection', type: 'artifact_input',
      artifactInput: { allowedTypes: ['blueprint'], allowedKinds: ['blueprint'], requireApproved: true, minimumArtifacts: 2, maximumArtifacts: 101 },
    }, {
      id: 'pages', name: 'Selected approved pages', type: 'fan_out',
      fanOut: {
        itemsPath: '/blueprintPages', sliceKeyPath: '/key', mergeNodeId: 'pages-merged',
        maxParallel: 4, maxItems: 100, itemKind: 'blueprint_selection_page',
      },
    }],
    edges: [],
    hash: hash('a'), createdBy: user.id, createdAt: now,
  },
}

const workflowCapabilities = {
  version: 4,
  nodeTypes: ['artifact_input', 'ai_transform', 'human_edit', 'review_gate', 'condition', 'fan_out', 'merge', 'manifest_compiler', 'workbench_build', 'quality_gate', 'publish', 'transform'],
  inputContracts: [{ ...workflowDefinition.definition.inputContract, requireApproved: false }, selectionWorkflowDefinition.definition.inputContract],
  outputContracts: [workflowDefinition.definition.outputContract],
  aiTransforms: [
    { jobType: 'refine_project_brief', outputSchemaVersion: 'project-brief-proposal/v1', modelPolicies: ['project-default'], requiredArtifactKinds: ['project_brief'], requiredApprovedKinds: [], producedArtifactKinds: ['project_brief'] },
    { jobType: 'derive_requirements', outputSchemaVersion: 'requirements-proposal/v1', modelPolicies: ['project-default'], requiredArtifactKinds: ['project_brief'], requiredApprovedKinds: ['project_brief'], producedArtifactKinds: ['product_requirements'] },
    { jobType: 'decompose_pages', outputSchemaVersion: 'blueprint-proposal/v1', modelPolicies: ['project-default'], requiredArtifactKinds: ['product_requirements'], requiredApprovedKinds: ['product_requirements'], producedArtifactKinds: ['blueprint'] },
    { jobType: 'generate_page_spec', outputSchemaVersion: 'page-spec-proposal/v1', modelPolicies: ['project-default'], requiredArtifactKinds: ['blueprint'], requiredApprovedKinds: ['blueprint'], producedArtifactKinds: ['page_spec'] },
    { jobType: 'generate_prototype', outputSchemaVersion: 'prototype-proposal/v1', modelPolicies: ['project-default'], requiredArtifactKinds: ['page_spec'], requiredApprovedKinds: ['page_spec'], producedArtifactKinds: ['prototype'] },
  ],
  manifestCompilers: [{
    manifestKind: 'application_build', schemaVersion: 1, hook: 'application-build-manifest/v1',
    requiredArtifactKinds: ['blueprint', 'page_spec', 'prototype'], requiredApprovedKinds: ['blueprint', 'page_spec', 'prototype'],
    requiresMergedSlices: true, producedSemanticKinds: ['application_build_manifest'],
    allowedContextArtifactKinds: ['project_brief', 'product_requirements', 'requirement_baseline', 'blueprint', 'page_spec', 'prototype', 'api_contract', 'data_contract', 'permission_contract', 'design_system', 'token_set', 'component_registry'],
  }],
  transforms: ['selection_passthrough'],
  fanOutItemKinds: ['blueprint_page', 'blueprint_selection_page'],
  fanOutMaximumItems: { blueprint_page: 100, blueprint_selection_page: 100 },
  qualityGates: ['release'], publishEnvironments: ['preview', 'production'], workbenchSchemaVersions: [1],
  analysisLimits: {
    maximumDefinitionNodes: 200,
    maximumDefinitionEdges: 1000,
    maxSemanticPathStates: 256,
    maximumConditionExpressionBytes: 8192,
  },
}

type MockBlueprintRevision = typeof fullBlueprintRevision & {
  readonly proposalId?: string
  readonly sourceManifestId?: string
  readonly changeSource?: string
}

type MockBlueprint = Omit<typeof blueprint, 'latestRevision' | 'approvedRevision'> & {
  latestRevision: MockBlueprintRevision
  approvedRevision: typeof fullBlueprintRevision
  draft?: {
    readonly id: string
    readonly artifactId: string
    readonly baseRevisionId: string
    readonly sourceVersions: unknown[]
    readonly revision: number
    readonly content: typeof blueprintContent
    readonly contentHash: string
    readonly updatedBy: string
    readonly updatedAt: string
    readonly etag: string
  }
}

interface MockPlatformOptions {
  readonly authenticated?: boolean
  readonly showOnboarding?: boolean
  readonly prototypes?: 'approved' | 'none'
  readonly historicalRun?: boolean
  readonly role?: 'owner' | 'commenter'
  readonly staleBriefDraft?: boolean
  readonly workflowRequireApproved?: boolean
  readonly multiBundleWorkbench?: boolean
  readonly multiWorkbenchGroups?: boolean
  readonly blueprintProposal?: boolean
  readonly blueprintHumanEditRun?: boolean
  readonly secondBlueprint?: boolean
  readonly pauseFirstBlueprintDraftSave?: boolean
  readonly pauseBlueprintProposalApply?: boolean
  readonly pauseBlueprintNodeResume?: boolean
  readonly rejectFirstBlueprintDraftSave?: boolean
  readonly prototypeProposal?: boolean
  readonly designImportDecisionFails?: boolean
  readonly designImportCreateProcessingOnce?: boolean
  readonly conversationSelectionTarget?: boolean
  readonly conversationSelectedWorkbenchTarget?: boolean
  readonly conversationSummaryConflict?: boolean
  readonly conversationCheckpointSourcePageSize?: number
  readonly soloReviewRun?: boolean
  readonly soloProject?: boolean
  readonly failedRun?: boolean
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
  governanceMode: 'solo' | 'team'
  prototypes: unknown[]
  brief: ReturnType<typeof staleProjectBrief>
  blueprint: MockBlueprint
  secondaryBlueprint: MockBlueprint | null
  pageSpec: typeof pageSpec
  run: ReturnType<typeof workflowRun> | ReturnType<typeof blueprintHumanEditWorkflowRun> | ReturnType<typeof selectionWorkflowRun> | ReturnType<typeof multiBundleWorkflowRun> | ReturnType<typeof multiGroupWorkflowRun> | null
  proposal: ReturnType<typeof implementationProposal> | null
  workspaceRevision: ReturnType<typeof applicationRevision> | null
  workbenchBundle: ReturnType<typeof buildManifest>
  multiWorkbench: ReturnType<typeof multiWorkbenchState> | null
  blueprintProposal: ReturnType<typeof blueprintArtifactProposal> | null
  blueprintCreatedRevision: MockBlueprintRevision | null
  blueprintDraftSaveGate: Deferred | null
  blueprintListGate: Deferred | null
  blueprintProposalApplyGate: Deferred | null
  blueprintNodeResumeGate: Deferred | null
  rejectNextBlueprintDraftSave: boolean
  prototypeProposal: ReturnType<typeof artifactProposal> | null
  prototypeCreatedRevision: Record<string, unknown> | null
  designImports: MockDesignImport[]
  designImportCreateAttempts: number
  conversations: Array<ReturnType<typeof conversationRecord>>
  conversationMessages: Array<ReturnType<typeof conversationMessage>>
  conversationSummaryCheckpoints: Array<ReturnType<typeof conversationSummaryCheckpoint>>
  intentProposal: MockWorkflowIntentProposal | null
  conversationCommand: ReturnType<typeof conversationCommand> | null
  graphBriefTitle: string
  bindingVersion: number
  bindingReviewerRole: 'reviewer' | 'assignee' | 'watcher'
}

function createSecondaryBlueprint(): MockBlueprint {
  const artifactId = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'
  const revisionId = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee2'
  const content = structuredClone(blueprintContent)
  content.nodes = content.nodes.map((node) => node.id === 'feature-orders'
    ? { ...node, title: 'Billing management' }
    : node)
  content.semantic.nodes = content.semantic.nodes.map((node) => node.id === 'feature-orders'
    ? { ...node, title: 'Billing management' }
    : node)
  const revision: MockBlueprintRevision = {
    ...fullBlueprintRevision,
    id: revisionId,
    artifactId,
    contentHash: hash('e'),
    content,
  }
  return {
    artifact: {
      ...blueprint.artifact,
      id: artifactId,
      artifactKey: 'blueprint-' + artifactId,
      title: 'Billing Blueprint',
      latestRevisionId: revisionId,
      approvedRevisionId: revisionId,
      etag: '"artifact:' + artifactId + ':1"',
    },
    latestRevision: revision,
    approvedRevision: revision,
  }
}

function mockBlueprints(state: MockPlatformState): MockBlueprint[] {
  return state.secondaryBlueprint ? [state.blueprint, state.secondaryBlueprint] : [state.blueprint]
}

async function installPlatformMock(page: Page, options: MockPlatformOptions = {}) {
  if (!options.showOnboarding) {
    await page.addInitScript(({ userId }) => {
      const preference = JSON.stringify({
        schema: 'worksflow.persistence',
        version: 1,
        savedAt: Date.now(),
        data: { dismissedVersion: 2, completedVersion: 0 },
      })
      window.localStorage.setItem('worksflow.onboarding.guest', preference)
      window.localStorage.setItem(`worksflow.onboarding.${userId}`, preference)
    }, { userId: user.id })
  }
  const state: MockPlatformState = {
    requests: [],
    governanceMode: options.soloReviewRun || options.soloProject ? 'solo' : 'team',
    prototypes: options.prototypes === 'none' ? [] : [approvedPrototype],
    brief: staleProjectBrief(options.staleBriefDraft ?? false),
    blueprint: structuredClone(blueprint) as MockBlueprint,
    secondaryBlueprint: options.secondBlueprint ? createSecondaryBlueprint() : null,
    pageSpec: structuredClone(pageSpec),
    run: options.blueprintHumanEditRun
      ? blueprintHumanEditWorkflowRun()
      : options.failedRun
      ? workflowRun('run-failed', 'failed')
      : options.soloReviewRun
      ? workflowRun('run-solo-review', 'waiting_review', 'solo')
      : options.multiWorkbenchGroups
        ? multiGroupWorkflowRun()
      : options.multiBundleWorkbench ? multiBundleWorkflowRun()
      : options.conversationSelectedWorkbenchTarget ? selectionWorkflowRun('run-selection-active', 'build-root-1')
      : options.conversationSelectionTarget ? workflowRun('run-current-other', 'waiting_input')
      : options.historicalRun ? workflowRun('run-history', 'waiting_input') : null,
    proposal: null,
    workspaceRevision: null,
    workbenchBundle: buildManifest(
      options.conversationSelectionTarget || options.conversationSelectedWorkbenchTarget
        ? 'run-selection-active'
        : undefined,
      options.conversationSelectedWorkbenchTarget ? 'build-root-1' : undefined,
    ),
    multiWorkbench: options.multiWorkbenchGroups
      ? multiGroupWorkbenchState()
      : options.multiBundleWorkbench ? multiWorkbenchState() : null,
    blueprintProposal: options.blueprintProposal ? blueprintArtifactProposal() : null,
    blueprintCreatedRevision: null,
    blueprintDraftSaveGate: options.pauseFirstBlueprintDraftSave ? deferred() : null,
    blueprintListGate: null,
    blueprintProposalApplyGate: options.pauseBlueprintProposalApply ? deferred() : null,
    blueprintNodeResumeGate: options.pauseBlueprintNodeResume ? deferred() : null,
    rejectNextBlueprintDraftSave: options.rejectFirstBlueprintDraftSave ?? false,
    prototypeProposal: options.prototypeProposal ? artifactProposal() : null,
    prototypeCreatedRevision: null,
    designImports: [],
    designImportCreateAttempts: 0,
    conversations: [conversationRecord('33333333-3333-4333-8333-333333333333', 'Project discovery')],
    conversationMessages: [],
    conversationSummaryCheckpoints: [],
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
      governanceMode: state.governanceMode,
      currentUserRole: options.role ?? 'owner',
    }] })
    return
  }
  if (path === `/v1/projects/${project.id}` && method === 'PATCH') {
    const input = body as { governanceMode?: 'solo' | 'team' }
    if (input.governanceMode) state.governanceMode = input.governanceMode
    await respond({
      ...project,
      governanceMode: state.governanceMode,
      currentUserRole: options.role ?? 'owner',
      etag: '"project:2"',
    })
    return
  }
  if (path === `/v1/projects/${project.id}`) {
    await respond({
      ...project,
      governanceMode: state.governanceMode,
      currentUserRole: options.role ?? 'owner',
      etag: state.governanceMode === 'solo' ? '"project:2"' : project.etag,
    })
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
    state.designImportCreateAttempts += 1
    if (options.designImportCreateProcessingOnce && state.designImportCreateAttempts === 1) {
      await respond({
        type: 'urn:worksflow:problem:design_import_processing', title: 'Design import is processing', status: 503,
        detail: 'Another worker holds the durable creation lease. Retry with the same idempotency key.', code: 'design_import_processing',
      }, 503, { 'retry-after': '1' })
      return
    }
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
    const inputMessages = state.conversationMessages
      .filter((item) => item.conversationId === intentGeneratePath[1])
      .sort((left, right) => left.sequence - right.sequence)
    const trigger = inputMessages.find((item) => item.id === input.triggerMessageId)
      ?? inputMessages.at(-1)
    const recommended = inputMessages.filter((item) => (
      trigger ? item.sequence < trigger.sequence : true
    )).at(-1) ?? trigger
    const approvedCheckpoint = recommended
      ? [...state.conversationSummaryCheckpoints]
        .reverse()
        .find((item) => (
          item.conversationId === intentGeneratePath[1]
          && item.status === 'approved'
          && item.throughSequence >= recommended.sequence
        ))
      : undefined
    if (options.conversationSummaryConflict && trigger && recommended && !approvedCheckpoint) {
      await respond({
        type: 'urn:worksflow:problem:conversation_summary_checkpoint_required',
        title: 'Controlled summary checkpoint required',
        status: 409,
        code: 'conversation_summary_checkpoint_required',
        detail: 'Create and review a controlled summary checkpoint; no messages were silently omitted.',
        extensions: {
          triggerMessageId: trigger.id,
          triggerSequence: trigger.sequence,
          messageCount: inputMessages.length,
          messageContentBytes: inputMessages.reduce((total, item) => total + item.content.length, 0),
          contextBytes: 131072,
          recommendedThroughMessageId: recommended.id,
          recommendedThroughSequence: recommended.sequence,
          createHref: `${conversationBase}/${intentGeneratePath[1]}/summary-checkpoints`,
        },
      }, 409)
      return
    }
    const proposal = options.conversationSelectionTarget || options.conversationSelectedWorkbenchTarget
      ? {
          ...workbenchIntentProposal(intentGeneratePath[1], input.triggerMessageId, {
            expectedRunId: 'run-selection-active',
            expectedBundleId: options.conversationSelectedWorkbenchTarget ? 'build-root-1' : 'build-1',
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
      options.conversationSelectionTarget || options.conversationSelectedWorkbenchTarget
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
  const summaryCheckpointListPath = path.match(new RegExp(`^${conversationBase}/([^/]+)/summary-checkpoints$`))
  if (summaryCheckpointListPath && method === 'GET') {
    await respond({
      items: state.conversationSummaryCheckpoints.filter(
        (item) => item.conversationId === summaryCheckpointListPath[1],
      ),
    })
    return
  }
  if (summaryCheckpointListPath && method === 'POST') {
    const input = body as { throughMessageId: string; summary: string }
    const throughMessage = state.conversationMessages.find((item) => (
      item.conversationId === summaryCheckpointListPath[1]
      && item.id === input.throughMessageId
    ))
    const conversation = state.conversations.find(
      (item) => item.id === summaryCheckpointListPath[1],
    )
    if (!throughMessage || !conversation) {
      await respond({ title: 'Not found', status: 404 }, 404)
      return
    }
    const checkpoint = conversationSummaryCheckpoint(
      `88888888-8888-4888-8888-${String(state.conversationSummaryCheckpoints.length + 1).padStart(12, '0')}`,
      conversation.id,
      throughMessage,
      input.summary,
      {
        createdBy: user.id,
        previousCheckpointId: conversation.summaryCheckpointHeadId,
      },
    )
    state.conversationSummaryCheckpoints = [
      ...state.conversationSummaryCheckpoints,
      checkpoint,
    ]
    await respond(checkpoint, 201, { etag: checkpoint.etag })
    return
  }
  const summaryCheckpointSourcePath = path.match(new RegExp(
    `^${conversationBase}/([^/]+)/summary-checkpoints/([^/]+)/source-messages$`,
  ))
  if (summaryCheckpointSourcePath && method === 'GET') {
    const checkpoint = state.conversationSummaryCheckpoints.find((item) => (
      item.conversationId === summaryCheckpointSourcePath[1]
      && item.id === summaryCheckpointSourcePath[2]
    ))
    if (!checkpoint) {
      await respond({ title: 'Not found', status: 404 }, 404)
      return
    }
    const previous = checkpoint.previousCheckpointId
      ? state.conversationSummaryCheckpoints.find((item) => item.id === checkpoint.previousCheckpointId)
      : undefined
    const startSequence = (previous?.throughSequence ?? 0) + 1
    const sourceItems = state.conversationMessages.filter((item) => (
        item.conversationId === checkpoint.conversationId
        && item.sequence >= startSequence
        && item.sequence <= checkpoint.throughSequence
      )).sort((left, right) => left.sequence - right.sequence)
    const requestedLimit = Number(url.searchParams.get('limit') ?? '200')
    const pageSize = Math.min(
      Number.isFinite(requestedLimit) && requestedLimit > 0 ? requestedLimit : 200,
      options.conversationCheckpointSourcePageSize ?? 200,
    )
    const offset = Number(url.searchParams.get('cursor') ?? '0')
    const pageItems = sourceItems.slice(offset, offset + pageSize)
    const nextOffset = offset + pageItems.length
    await respond({
      items: pageItems,
      ...(nextOffset < sourceItems.length ? { nextCursor: String(nextOffset) } : {}),
    })
    return
  }
  const summaryCheckpointDecisionPath = path.match(new RegExp(
    `^${conversationBase}/([^/]+)/summary-checkpoints/([^/]+)/decision$`,
  ))
  if (summaryCheckpointDecisionPath && method === 'POST') {
    const input = body as {
      decision: 'approve' | 'reject'
      reason?: string
      soloReviewConfirmed?: boolean
    }
    const current = state.conversationSummaryCheckpoints.find((item) => (
      item.conversationId === summaryCheckpointDecisionPath[1]
      && item.id === summaryCheckpointDecisionPath[2]
    ))
    if (!current) {
      await respond({ title: 'Not found', status: 404 }, 404)
      return
    }
    if (current.createdBy === user.id && state.governanceMode !== 'solo') {
      await respond({ title: 'Self approval is forbidden', status: 403 }, 403)
      return
    }
    if (
      current.createdBy === user.id
      && (!input.soloReviewConfirmed || !input.reason?.trim())
    ) {
      await respond({ title: 'Solo self-review confirmation is required', status: 422 }, 422)
      return
    }
    const updated = {
      ...current,
      status: input.decision === 'approve' ? 'approved' as const : 'rejected' as const,
      version: current.version + 1,
      etag: `"conversation-summary-checkpoint:${current.id}:${current.version + 1}"`,
      reviewedBy: user.id,
      reviewedAt: now,
      ...(input.reason ? { reviewReason: input.reason } : {}),
    }
    state.conversationSummaryCheckpoints = state.conversationSummaryCheckpoints.map((item) => (
      item.id === updated.id ? updated : item
    ))
    if (input.decision === 'approve') {
      state.conversations = state.conversations.map((item) => item.id === current.conversationId
        ? {
            ...item,
            summaryCheckpointHeadId: updated.id,
            version: item.version + 1,
            etag: `"conversation:${item.id}:${item.version + 1}"`,
            updatedAt: now,
          }
        : item)
    }
    await respond(updated, 200, { etag: updated.etag })
    return
  }
  const summaryCheckpointItemPath = path.match(new RegExp(
    `^${conversationBase}/([^/]+)/summary-checkpoints/([^/]+)$`,
  ))
  if (summaryCheckpointItemPath && method === 'GET') {
    const checkpoint = state.conversationSummaryCheckpoints.find((item) => (
      item.conversationId === summaryCheckpointItemPath[1]
      && item.id === summaryCheckpointItemPath[2]
    ))
    await respond(
      checkpoint ?? { title: 'Not found', status: 404 },
      checkpoint ? 200 : 404,
      checkpoint ? { etag: checkpoint.etag } : {},
    )
    return
  }
  const commandExecutePath = path.match(new RegExp(`^${conversationBase}/([^/]+)/commands/([^/]+)/execute$`))
  if (commandExecutePath && method === 'POST' && state.conversationCommand) {
    if (!body || typeof body !== 'object' || Array.isArray(body) || Object.keys(body).length !== 0) {
      await respond({ code: 'unknown_json_field', detail: 'command execution body must be empty' }, 400)
      return
    }
    let serverWorkbenchReceipt: Record<string, unknown> | undefined
    if (state.conversationCommand.kind === 'start_workflow') {
      state.run = workflowRun(state.conversationCommand.id, 'waiting_input')
    } else {
      const workbench = state.conversationCommand.payload.workbench as {
        expectedRunId: string
        expectedBundleId: string
      }
      if (options.conversationSelectionTarget || options.conversationSelectedWorkbenchTarget) {
        // The command targets a different active run than the one the user had
        // open when the conversation action was accepted.
        state.run = selectionWorkflowRun(workbench.expectedRunId, workbench.expectedBundleId)
        state.workbenchBundle = buildManifest(
          workbench.expectedRunId,
          state.workbenchBundle.rootBuildManifestId,
        )
      }
      const rootBundleId = workbench.expectedBundleId
      const activeBundleId = state.multiWorkbench?.activeBundleIds[rootBundleId]
        ?? state.workbenchBundle.id
      if (state.multiWorkbench) {
        const generated = multiImplementationProposal(
          state.conversationCommand.id,
          activeBundleId,
          'open',
          'pending',
          1,
          'Server-authoritative conversation generation.',
        )
        state.multiWorkbench.proposals[generated.id] = {
          ...generated,
          executionSource: 'conversation_command',
          conversationCommandId: state.conversationCommand.id,
          instructionHash: hash('7'),
        }
        state.multiWorkbench.currentProposalIds[rootBundleId] = generated.id
      } else {
        state.proposal = {
          ...implementationProposal('open'),
          id: state.conversationCommand.id,
          buildManifestId: activeBundleId,
          executionSource: 'conversation_command',
          conversationCommandId: state.conversationCommand.id,
          instructionHash: hash('7'),
        }
      }
      serverWorkbenchReceipt = {
        runId: workbench.expectedRunId,
        rootBundleId,
        bundleId: activeBundleId,
        implementationProposalId: state.conversationCommand.id,
        instructionHash: hash('7'),
        desiredOutputCapability: 'application',
      }
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
        } : serverWorkbenchReceipt),
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
    await respond({ items: [projectBrief.artifact, ...mockBlueprints(state).map((value) => value.artifact), state.pageSpec.artifact, ...state.prototypes.map((value) => (value as typeof approvedPrototype).artifact)] })
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
    const items = structuredClone(mockBlueprints(state))
    const gate = state.blueprintListGate
    state.blueprintListGate = null
    if (gate) await gate.promise
    await respond({ items })
    return
  }
  const blueprintDraftPath = path.match(/^\/v1\/blueprints\/([^/]+)\/draft$/)
  if (blueprintDraftPath && method === 'PATCH') {
    const target = mockBlueprints(state).find((item) => item.artifact.id === blueprintDraftPath[1])
    if (!target) {
      await respond({ title: 'Blueprint not found' }, 404)
      return
    }
    const expectedEtag = target.draft?.etag ?? target.artifact.etag
    const forcedConflict = state.rejectNextBlueprintDraftSave && target.artifact.id === state.blueprint.artifact.id
    if (headers['if-match'] !== expectedEtag || forcedConflict) {
      state.rejectNextBlueprintDraftSave = false
      await respond({
        type: 'urn:worksflow:problem:etag_mismatch',
        title: 'Precondition failed',
        status: 412,
        detail: 'The Blueprint draft changed since it was loaded.',
        code: 'etag_mismatch',
      }, 412)
      return
    }
    const input = body as { content: typeof blueprintContent }
    const gate = target.artifact.id === state.blueprint.artifact.id
      ? state.blueprintDraftSaveGate
      : null
    if (gate) state.blueprintDraftSaveGate = null
    if (gate) await gate.promise
    const currentDraft = target.draft
    const nextDraftRevision = (currentDraft?.revision ?? 1) + 1
    const draft = {
      id: currentDraft?.id ?? `blueprint-draft-${target.artifact.id}`,
      artifactId: target.artifact.id,
      baseRevisionId: currentDraft?.baseRevisionId ?? target.latestRevision.id,
      sourceVersions: currentDraft?.sourceVersions ?? [],
      revision: nextDraftRevision,
      content: input.content,
      contentHash: hash(String(nextDraftRevision)),
      updatedBy: user.id,
      updatedAt: now,
      etag: `"blueprint-draft:${nextDraftRevision}"`,
    }
    const updated = { ...target, draft }
    if (target.artifact.id === state.blueprint.artifact.id) state.blueprint = updated
    else state.secondaryBlueprint = updated
    await respond(updated, 200, { etag: draft.etag })
    return
  }
  if (path === `/v1/blueprints/${blueprint.artifact.id}/revisions` && method === 'POST') {
    const input = body as { changeSource?: string }
    const draft = state.blueprint.draft
    if (!draft) {
      await respond({ title: 'Blueprint draft not found' }, 404)
      return
    }
    const revision: MockBlueprintRevision = {
      ...fullBlueprintRevision,
      id: 'cccccccc-cccc-4ccc-8ccc-ccccccccccc3',
      revisionNumber: state.blueprint.latestRevision.revisionNumber + 1,
      contentHash: draft.contentHash,
      status: 'draft',
      content: draft.content,
      ...(state.blueprintProposal?.status === 'applied' ? {
        proposalId: state.blueprintProposal.id,
        sourceManifestId: state.blueprintProposal.manifest.id,
      } : {}),
      changeSource: input.changeSource ?? 'human',
    }
    state.blueprint = {
      ...state.blueprint,
      artifact: { ...state.blueprint.artifact, latestRevisionId: revision.id },
      latestRevision: revision,
    }
    state.blueprintCreatedRevision = revision
    await respond(revision, 201)
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
    await respond({
      items: state.blueprint.latestRevision.id === fullBlueprintRevision.id
        ? [fullBlueprintRevision]
        : [state.blueprint.latestRevision, fullBlueprintRevision],
    })
    return
  }
  if (path === `/v1/projects/${project.id}/prototypes` && method === 'GET') {
    await respond({ items: state.prototypes })
    return
  }
  if (path === `/v1/projects/${project.id}/output-proposals` && method === 'GET') {
    await respond({ items: [state.blueprintProposal, state.prototypeProposal].filter(Boolean) })
    return
  }
  const artifactProposalItem = path.match(/^\/v1\/output-proposals\/([^/]+)$/)
  const blueprintProposal = state.blueprintProposal
  if (blueprintProposal && artifactProposalItem?.[1] === blueprintProposal.id && method === 'GET') {
    await respond(blueprintProposal)
    return
  }
  const artifactProposalApply = path.match(/^\/v1\/output-proposals\/([^/]+)\/apply$/)
  if (blueprintProposal && artifactProposalApply?.[1] === blueprintProposal.id && method === 'POST') {
    const gate = state.blueprintProposalApplyGate
    state.blueprintProposalApplyGate = null
    if (gate) await gate.promise
    const currentContent = state.blueprint.draft?.content ?? state.blueprint.latestRevision.content
    const appliedTitle = 'AI-reviewed order management'
    const appliedContent = {
      ...currentContent,
      nodes: currentContent.nodes.map((node) => node.id === 'feature-orders'
        ? { ...node, title: appliedTitle }
        : node),
      semantic: {
        ...currentContent.semantic,
        nodes: currentContent.semantic.nodes.map((node) => node.id === 'feature-orders'
          ? { ...node, title: appliedTitle }
          : node),
      },
    }
    const currentDraft = state.blueprint.draft
    const nextDraftRevision = (currentDraft?.revision ?? 1) + 1
    const draft = {
      id: currentDraft?.id ?? 'blueprint-draft-1',
      artifactId: blueprint.artifact.id,
      baseRevisionId: currentDraft?.baseRevisionId ?? blueprintRevision.revisionId,
      sourceVersions: currentDraft?.sourceVersions ?? [],
      revision: nextDraftRevision,
      content: appliedContent,
      contentHash: hash('p'),
      updatedBy: user.id,
      updatedAt: now,
      etag: `"blueprint-draft:${nextDraftRevision}:proposal"`,
    }
    state.blueprint = { ...state.blueprint, draft }
    state.blueprintProposal = {
      ...blueprintProposal,
      status: 'applied',
      operations: blueprintProposal.operations.map((operation) => ({
        ...operation,
        decision: operation.decision === 'accepted' ? 'applied' as const : operation.decision,
      })),
      version: blueprintProposal.version + 1,
      appliedAt: now,
    }
    await respond(draft, 200, { etag: draft.etag })
    return
  }
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
  if (path === `/v1/projects/${project.id}/workflow-capabilities`) {
    await respond(workflowCapabilities)
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
  if (path.endsWith(`/workflow-definitions/${selectionWorkflowDefinition.id}/versions`) && method === 'GET') {
    await respond({ items: [selectionWorkflowDefinition], total: 1 })
    return
  }
  if (path.endsWith(`/workflow-definitions/${selectionWorkflowDefinition.id}/versions`) && method === 'POST') {
    await respond({
      ...selectionWorkflowDefinition,
      versionId: 'abababab-abab-4bab-8bab-ababababab02',
      version: selectionWorkflowDefinition.version + 1,
      contentHash: hash('2'),
      definition: {
        ...selectionWorkflowDefinition.definition,
        ...(body as Record<string, unknown>),
        version: selectionWorkflowDefinition.version + 1,
        hash: hash('2'),
      },
    }, 201)
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
    if (state.run.definitionVersionId === selectionWorkflowDefinition.versionId) {
      state.workbenchBundle = buildManifest(state.run.id)
    }
    await respond(state.run, 201)
    return
  }
  if (
    options.blueprintHumanEditRun
    && path === '/v1/projects/' + project.id + '/workflow-runs/run-blueprint-human-edit/resume'
    && method === 'POST'
  ) {
    const gate = state.blueprintNodeResumeGate
    state.blueprintNodeResumeGate = null
    if (gate) await gate.promise
    const current = state.run as ReturnType<typeof blueprintHumanEditWorkflowRun>
    state.run = {
      ...current,
      status: 'running' as const,
      nodes: current.nodes.map((node) => node.key === 'blueprint-edit'
        ? { ...node, status: 'completed' as const, completedAt: now }
        : node),
    } as MockPlatformState['run']
    await respond(undefined, 204)
    return
  }
  if (/\/workflow-runs\/[^/]+\/approve$/.test(path) && method === 'POST') {
    await route.fulfill({ status: 204, headers: corsHeaders() })
    return
  }
  if (/\/workflow-runs\/[^/]+\/retry$/.test(path) && method === 'POST') {
    await route.fulfill({ status: 204, headers: corsHeaders() })
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
  if (lineagePath && method === 'GET') {
    if (multi) {
      await respond(multiLineageState(multi, lineagePath[1]))
      return
    }
    const rootBundleId = state.workbenchBundle.rootBuildManifestId ?? state.workbenchBundle.id
    if (lineagePath[1] !== rootBundleId) {
      await respond({ title: 'Not found' }, 404)
      return
    }
    await respond({
      rootBundleId,
      activeBundle: state.workbenchBundle,
      ...(state.proposal ? { currentProposal: state.proposal } : {}),
      lineage: [{
        bundleId: state.workbenchBundle.id,
        status: 'active',
        createdAt: state.workbenchBundle.createdAt,
        ...(state.proposal ? {
          latestProposal: {
            id: state.proposal.id,
            status: state.proposal.status,
            version: state.proposal.version,
            createdAt: state.proposal.createdAt,
          },
        } : {}),
      }],
    })
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
    } as unknown as MockPlatformState['run']
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
  const implementationProposalPath = path.match(/^\/v1\/implementation-proposals\/([^/]+)$/)
  if (implementationProposalPath && method === 'GET' && state.proposal?.id === implementationProposalPath[1]) {
    await respond(state.proposal)
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

test('first-time users receive a server-state getting started guide', async ({ page }) => {
  await installPlatformMock(page, {
    authenticated: false,
    prototypes: 'none',
    showOnboarding: true,
  })
  await page.goto('/workbench/planning?view=code')

  await expect(page.getByRole('dialog', { name: 'Start here' })).toBeVisible()
  await expect(page.getByText('Sign in to the platform')).toBeVisible()
  await page.getByRole('button', { name: 'Open sign in or registration' }).click()
  await expect(page).toHaveURL(/\/settings$/)
})

test('project Owner switches to Solo mode and completes onboarding governance', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto('/settings')

  await expect(page.getByTestId('project-governance-solo')).toBeEnabled()
  await page.getByTestId('project-governance-solo').click()
  await page.getByTestId('save-project-governance').click()

  await expect(page.getByText('Review mode saved.')).toBeVisible()
  const update = state.requests.find((request) =>
    request.method === 'PATCH' && request.path === `/v1/projects/${project.id}`,
  )
  expect(update?.body).toEqual({ governanceMode: 'solo' })
  expect(update?.headers['if-match']).toBe(project.etag)

  await page.getByRole('button', { name: 'Open getting started guide' }).click()
  const governanceStep = page
    .getByRole('heading', { name: 'Choose a review mode' })
    .locator('xpath=ancestor::section')
  await expect(governanceStep).toContainText('Completed')
  await expect(governanceStep).toContainText('Solo mode is enabled')
})

test('Solo workflow approval requires confirmation and a non-empty reason', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    soloReviewRun: true,
  })
  await page.goto('/workbench/planning?view=preview')

  await page.getByRole('button', { name: /run-solo-review/ }).click()
  await expect(page.getByTestId('solo-review-warning-solo-review')).toBeVisible()
  const approve = page.getByTestId('workflow-review-approve-solo-review')
  await expect(approve).toBeDisabled()

  await page.getByTestId('solo-review-confirm-solo-review').check()
  await expect(approve).toBeDisabled()
  await page.getByPlaceholder('Review reason / requested change').fill('Reviewed the exact result locally.')
  await expect(approve).toBeEnabled()
  await approve.click()

  await expect.poll(() => state.requests.find((request) =>
    request.method === 'POST' && request.path.endsWith('/workflow-runs/run-solo-review/approve'),
  )?.body).toEqual({
    nodeKey: 'solo-review',
    resolution: 'approve',
    reason: 'Reviewed the exact result locally.',
    soloReviewConfirmed: true,
  })
})

test('failed workflow retry supplies a non-empty default reason', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    failedRun: true,
  })
  await page.goto('/workbench/planning?view=preview')

  await page.getByRole('button', { name: /run-failed/ }).click()
  await page.getByRole('button', { name: 'Retry node' }).click()

  await expect.poll(() => state.requests.find((request) =>
    request.method === 'POST' && request.path.endsWith('/workflow-runs/run-failed/retry'),
  )?.body).toEqual({
    nodeKey: 'brief-input',
    reason: 'Retry from Workbench',
  })
})

test('Workflow approval remains blocked until the exact upstream revision has canonical approval', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    soloReviewRun: true,
  })
  const unapprovedRevision = {
    ...briefRevision,
    id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb4',
    revisionNumber: 4,
    contentHash: hash('4'),
    status: 'draft',
  }
  state.brief = {
    ...state.brief,
    artifact: {
      ...state.brief.artifact,
      latestRevisionId: unapprovedRevision.id,
      approvedRevisionId: briefRevision.id,
    },
    latestRevision: unapprovedRevision,
    approvedRevision: briefRevision,
  }
  state.run = workflowRun('run-solo-review', 'waiting_review', 'solo', unapprovedRevision)

  await page.goto('/workbench/planning?view=preview')
  await page.getByRole('button', { name: /run-solo-review/ }).click()

  const approve = page.getByTestId('workflow-review-approve-solo-review')
  await page.getByTestId('solo-review-confirm-solo-review').check()
  await page.getByPlaceholder('Review reason / requested change').fill('Reviewed the exact result locally.')
  await expect(page.getByTestId('workflow-review-canonical-blocker-solo-review')).toContainText(
    'Approve the exact upstream revision in Review Center',
  )
  await expect(approve).toBeDisabled()
  expect(state.requests.some((request) =>
    request.method === 'POST' && request.path.endsWith('/workflow-runs/run-solo-review/approve'),
  )).toBe(false)
})

test('Workbench deep links reject bundles and proposals from another selected project', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  let foreignBundleLoads = 0
  let foreignWorkspaceArtifactLoads = 0
  const foreignWorkspaceRevision = {
    ...applicationRevision(),
    id: 'foreign-workspace-r1',
    artifactId: 'foreign-workspace',
  }
  const historicalWorkspaceRevision = {
    ...applicationRevision(),
    id: 'historical-workspace-r1',
    artifactId: 'historical-workspace',
  }
  await page.route('**/v1/build-manifests/foreign-build', (route) => {
    foreignBundleLoads += 1
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: corsHeaders(),
      body: JSON.stringify({
        ...state.workbenchBundle,
        id: 'foreign-build',
        rootBuildManifestId: 'foreign-build',
        projectId: 'foreign-project',
      }),
    })
  })
  await page.route('**/v1/implementation-proposals/foreign-proposal', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify({
      ...implementationProposal('ready', 'accepted', 2),
      id: 'foreign-proposal',
      projectId: 'foreign-project',
      buildManifestId: 'foreign-build',
    }),
  }))
  await page.route('**/v1/revisions/foreign-workspace-r1', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify(foreignWorkspaceRevision),
  }))
  await page.route('**/v1/artifacts/foreign-workspace', (route) => {
    foreignWorkspaceArtifactLoads += 1
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: corsHeaders(),
      body: JSON.stringify({
        artifact: {
          id: 'foreign-workspace',
          projectId: 'foreign-project',
          kind: 'workspace',
        },
        latestRevision: foreignWorkspaceRevision,
        approvedRevision: foreignWorkspaceRevision,
      }),
    })
  })
  await page.route('**/v1/revisions/historical-workspace-r1', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify(historicalWorkspaceRevision),
  }))
  await page.route('**/v1/artifacts/historical-workspace', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify({
      artifact: {
        id: 'historical-workspace',
        projectId: project.id,
        kind: 'workspace',
      },
      // The deep link may intentionally target an older immutable revision.
      latestRevision: applicationRevision(),
      approvedRevision: applicationRevision(),
    }),
  }))

  await page.goto('/workbench/planning?view=code&bundleId=foreign-build')
  await expect(page.getByRole('alert').filter({ hasText: 'Workbench bundle belongs to another project.' }))
    .toBeVisible()
  expect(foreignBundleLoads).toBe(1)

  foreignBundleLoads = 0
  await page.goto('/workbench/planning?view=code&proposalId=foreign-proposal')
  await expect(page.getByRole('alert').filter({ hasText: 'Implementation proposal belongs to another project.' }))
    .toBeVisible()
  expect(foreignBundleLoads).toBe(0)

  await page.goto('/workbench/planning?view=code&workspaceRevisionId=foreign-workspace-r1')
  await expect(page.getByRole('alert').filter({
    hasText: 'Workspace revision belongs to another project',
  })).toBeVisible()
  expect(foreignWorkspaceArtifactLoads).toBe(1)

  await page.goto('/workbench/planning?view=preview&workspaceRevisionId=historical-workspace-r1')
  await expect(page.getByText(/historical-workspace-r1/)).toBeVisible()
  await expect(page.getByRole('alert').filter({ hasText: 'another project' })).toHaveCount(0)
})

test('workflow run history and exact Project Brief start are server-backed', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, historicalRun: true })
  await page.goto('/workbench/planning?view=code')

  await expect(page.getByText('Server run history')).toBeVisible()
  await expect(page.getByText('run-history', { exact: true })).toBeVisible()
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  await page.getByRole('combobox', { name: 'Workflow definition' }).selectOption(workflowDefinition.id)
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

test('workflow authoring exposes the pinned execution profile and semantic analysis budget', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  await page.goto('/workbench/planning?view=code')

  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()

  await expect(page.getByText(/execution workflow-engine\/v2/)).toBeVisible()
  await expect(page.getByText(/registry v4 · semantic max 256/)).toBeVisible()
  await page.getByRole('button', { name: 'New definition' }).click()
  await expect(page.getByRole('dialog', { name: 'Workflow definition editor' })).toBeVisible()
  await expect(page.getByText('semantic states 1/256', { exact: true })).toBeVisible()
})

test('workflow authoring keeps a hydrated run definition separate from the selected version chain', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, historicalRun: true })
  await page.goto('/workbench/planning?view=code&runId=run-history')

  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  await expect(page.getByText('run-history', { exact: true }).first()).toBeVisible()

  const definitionSelect = page.getByRole('combobox', { name: 'Workflow definition' })
  await definitionSelect.selectOption(selectionWorkflowDefinition.id)
  await expect(definitionSelect).toHaveValue(selectionWorkflowDefinition.id)
  await page.getByRole('button', { name: 'New version' }).click()
  await page.getByRole('button', { name: 'JSON' }).click()
  await page.getByRole('textbox', { name: 'Workflow definition JSON' }).fill(JSON.stringify({
    name: 'Selected Blueprint application flow',
    schemaVersion: '4',
    inputContract: selectionWorkflowDefinition.definition.inputContract,
    outputContract: selectionWorkflowDefinition.definition.outputContract,
    nodes: [{
      id: 'selection',
      name: 'Frozen Blueprint selection',
      type: 'artifact_input',
      inputSchema: { type: 'object', additionalProperties: true },
      outputSchema: { type: 'object', additionalProperties: true },
      artifactInput: {
        allowedTypes: ['blueprint'], allowedKinds: ['blueprint'], requireApproved: true,
        minimumArtifacts: 2, maximumArtifacts: 101,
      },
    }, {
      id: 'publish',
      name: 'Publish selected application',
      type: 'publish',
      inputSchema: { type: 'object', additionalProperties: true },
      outputSchema: { type: 'object', additionalProperties: true },
      publish: { environment: 'preview', requiredRole: 'admin', allowRollback: true },
    }],
    edges: [{ id: 'selection-publish', from: 'selection', to: 'publish' }],
  }, null, 2))
  await page.getByRole('button', { name: 'Save immutable draft version' }).click()
  await expect(page.getByRole('dialog', { name: 'Workflow definition editor' })).toBeHidden()

  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path.endsWith(`/workflow-definitions/${selectionWorkflowDefinition.id}/versions`),
  )).toBe(true)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path.endsWith(`/workflow-definitions/${workflowDefinition.id}/versions`),
  )).toBe(false)
})

test('loading a run never replaces the definition selected for authoring', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true, historicalRun: true })
  await page.goto('/workbench/planning?view=code')

  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  const definitionSelect = page.getByRole('combobox', { name: 'Workflow definition' })
  await definitionSelect.selectOption(selectionWorkflowDefinition.id)
  await expect(definitionSelect).toHaveValue(selectionWorkflowDefinition.id)

  await page.getByRole('button', { name: /run-history/ }).click()
  await expect(page.getByText('run-history', { exact: true }).first()).toBeVisible()
  await expect(definitionSelect).toHaveValue(selectionWorkflowDefinition.id)
  await expect(page.getByRole('combobox', { name: 'Workflow version' }))
    .toHaveValue(selectionWorkflowDefinition.versionId)
})

test('workflow authoring ignores an older definition-version response after a newer selection', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  let delayPrimary = false
  let resolvePrimaryStarted!: () => void
  let releasePrimary!: () => void
  let resolvePrimaryFinished!: () => void
  const primaryStarted = new Promise<void>((resolve) => { resolvePrimaryStarted = resolve })
  const primaryRelease = new Promise<void>((resolve) => { releasePrimary = resolve })
  const primaryFinished = new Promise<void>((resolve) => { resolvePrimaryFinished = resolve })
  const respond = (route: Route, data: unknown) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify(data),
  })
  await page.route(`**/v1/projects/${project.id}/workflow-definitions/${workflowDefinition.id}/versions`, async (route) => {
    if (delayPrimary) {
      resolvePrimaryStarted()
      await primaryRelease
    }
    await respond(route, { items: [workflowDefinition], total: 1 })
    if (delayPrimary) resolvePrimaryFinished()
  })
  await page.route(`**/v1/projects/${project.id}/workflow-definitions/${selectionWorkflowDefinition.id}/versions`, (route) =>
    respond(route, { items: [selectionWorkflowDefinition], total: 1 }))

  await page.goto('/workbench/planning?view=code')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  const definitionSelect = page.getByRole('combobox', { name: 'Workflow definition' })
  await expect(definitionSelect).toHaveValue(selectionWorkflowDefinition.id)
  await expect(page.getByRole('combobox', { name: 'Workflow version' })).toHaveValue(selectionWorkflowDefinition.versionId)

  delayPrimary = true
  await definitionSelect.selectOption(workflowDefinition.id)
  await primaryStarted
  await definitionSelect.selectOption(selectionWorkflowDefinition.id)
  await expect(definitionSelect).toHaveValue(selectionWorkflowDefinition.id)
  await expect(page.getByRole('combobox', { name: 'Workflow version' })).toHaveValue(selectionWorkflowDefinition.versionId)
  releasePrimary()
  await primaryFinished

  await expect(definitionSelect).toHaveValue(selectionWorkflowDefinition.id)
  await expect(page.getByRole('combobox', { name: 'Workflow version' })).toHaveValue(selectionWorkflowDefinition.versionId)
})

test('an older run lineage response cannot replace the current run Workbench bundle', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  const runA = selectionWorkflowRun('run-race-a', 'bundle-race-a')
  const runB = selectionWorkflowRun('run-race-b', 'bundle-race-b')
  const bundleA = {
    ...buildManifest(runA.id), id: 'bundle-race-a', contentHash: hash('a'),
  }
  const bundleB = {
    ...buildManifest(runB.id), id: 'bundle-race-b', contentHash: hash('b'),
  }
  let resolveLineageAStarted!: () => void
  let releaseLineageA!: () => void
  let resolveLineageAFinished!: () => void
  const lineageAStarted = new Promise<void>((resolve) => { resolveLineageAStarted = resolve })
  const lineageARelease = new Promise<void>((resolve) => { releaseLineageA = resolve })
  const lineageAFinished = new Promise<void>((resolve) => { resolveLineageAFinished = resolve })
  const respond = (route: Route, data: unknown) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify(data),
  })
  await page.route(new RegExp(`/v1/projects/${project.id}/workflow-runs(?:\\?.*)?$`), (route) =>
    respond(route, { items: [runA, runB], total: 2 }))
  await page.route(`**/v1/projects/${project.id}/workflow-runs/${runA.id}`, (route) => respond(route, runA))
  await page.route(`**/v1/projects/${project.id}/workflow-runs/${runB.id}`, (route) => respond(route, runB))
  await page.route(`**/v1/projects/${project.id}/workflow-runs/${runA.id}/events`, (route) =>
    respond(route, { items: [{ id: 'event-race-a', runId: runA.id, sequence: 1, type: 'run.created', createdAt: now }] }))
  await page.route(`**/v1/projects/${project.id}/workflow-runs/${runB.id}/events`, (route) =>
    respond(route, { items: [{ id: 'event-race-b', runId: runB.id, sequence: 1, type: 'run.created', createdAt: now }] }))
  await page.route('**/v1/build-manifests/bundle-race-a/lineage-state', async (route) => {
    resolveLineageAStarted()
    await lineageARelease
    await respond(route, { rootBundleId: bundleA.id, activeBundle: bundleA, lineage: [] })
    resolveLineageAFinished()
  })
  await page.route('**/v1/build-manifests/bundle-race-b/lineage-state', (route) =>
    respond(route, { rootBundleId: bundleB.id, activeBundle: bundleB, lineage: [] }))

  await page.goto(`/workbench/planning?view=code&runId=${runA.id}`)
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  await lineageAStarted
  await page.getByRole('button', { name: new RegExp(runB.id) }).click()
  await expect(page.getByText(new RegExp(`${bundleB.id} ·`))).toBeVisible()
  releaseLineageA()
  await lineageAFinished

  await expect(page.getByText(new RegExp(`${bundleB.id} ·`))).toBeVisible()
  await expect(page.getByText(new RegExp(`${bundleA.id} ·`))).toHaveCount(0)
})

test('direct Bundle and Proposal URL hydration is latest-wins across delayed responses', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    multiBundleWorkbench: true,
  })
  const multi = state.multiWorkbench!
  const checkoutProposal = multiImplementationProposal(
    'proposal-checkout-latest',
    'bundle-checkout',
    'open',
    'pending',
    1,
    'The latest direct proposal selection must remain active.',
  )
  multi.proposals[checkoutProposal.id] = checkoutProposal
  let resolveHomeStarted!: () => void
  let releaseHome!: () => void
  let resolveHomeFinished!: () => void
  const homeStarted = new Promise<void>((resolve) => { resolveHomeStarted = resolve })
  const homeRelease = new Promise<void>((resolve) => { releaseHome = resolve })
  const homeFinished = new Promise<void>((resolve) => { resolveHomeFinished = resolve })
  await page.route('**/v1/build-manifests/bundle-home', async (route) => {
    resolveHomeStarted()
    await homeRelease
    await route.fallback()
    resolveHomeFinished()
  })

  await page.goto(
    `/workbench/planning?view=code&bundleId=bundle-home&proposalId=${checkoutProposal.id}`,
  )
  await homeStarted
  await expect(page).toHaveURL(/bundleId=bundle-checkout/)
  await expect(page).toHaveURL(new RegExp(`proposalId=${checkoutProposal.id}`))
  await expect(page.getByText(new RegExp(`bundle-checkout ·`))).toBeVisible()
  releaseHome()
  await homeFinished

  await expect(page).toHaveURL(/bundleId=bundle-checkout/)
  await expect(page).toHaveURL(new RegExp(`proposalId=${checkoutProposal.id}`))
  await expect(page.getByText(new RegExp(`bundle-home ·`))).toHaveCount(0)
})

test('run hydration rejects a Workbench bundle whose canonical delivery slice drifts', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectedWorkbenchTarget: true,
  })
  const rootBundleId = state.workbenchBundle.rootBuildManifestId ?? state.workbenchBundle.id
  const mismatchedBundle = {
    ...state.workbenchBundle,
    deliverySliceId: 'page-wrong',
  }
  await page.route(`**/v1/build-manifests/${rootBundleId}/lineage-state`, (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify({
      rootBundleId,
      activeBundle: mismatchedBundle,
      lineage: [],
    }),
  }))

  await page.goto('/workbench/planning?view=code&runId=run-selection-active')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()

  await expect(page.getByRole('alert').filter({ hasText: 'Workbench lineage state for' })).toContainText(
    'does not match its exact run, manifest group, ordinal, or delivery slice',
  )
  await expect(page.getByRole('heading', { name: 'Frozen application build manifest' })).toHaveCount(0)
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
  await page.getByRole('combobox', { name: 'Workflow definition' }).selectOption(workflowDefinition.id)
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
  await page.getByRole('combobox', { name: 'Workflow definition' }).selectOption(workflowDefinition.id)
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
  const panel = await openConversationPanel(page)

  const composer = panel.getByPlaceholder('Describe requirements or a controlled next action…')
  await expect(composer).toBeVisible()
  await composer.fill('Build an order operations application from the approved brief.')
  await panel.getByRole('button', { name: 'Send immutable message' }).click()
  await expect(panel.getByText('Build an order operations application from the approved brief.')).toBeVisible()

  await panel.getByRole('button', { name: 'Generate governed intent' }).click()
  await expect(panel.getByText('Start workflow', { exact: true })).toBeVisible()
  await panel.getByRole('button', { name: 'Accept', exact: true }).click()
  await expect(panel.getByRole('button', { name: 'Execute and open run' })).toBeVisible()
  await panel.getByRole('button', { name: 'Execute and open run' }).click()

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
    desiredOutputCapability: 'application',
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
  expect(generateRequest?.body).not.toHaveProperty('workbenchTargetHint')
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

test('Project Brief conversation switches from the open run to its authoritative selection Workbench target', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectionTarget: true,
  })
  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)

  const composer = panel.getByPlaceholder('Describe requirements or a controlled next action…')
  await composer.fill('Continue the active Blueprint selection application.')
  await panel.getByRole('button', { name: 'Send immutable message' }).click()
  await panel.getByRole('button', { name: 'Generate governed intent' }).click()

  await expect(panel.getByText('Workbench instruction', { exact: true })).toBeVisible()
  await expect(panel.getByText(selectionWorkflowDefinition.versionId, { exact: false })).toBeVisible()

  const generateRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path.endsWith('/intent-proposals/generate'))
  expect(generateRequest?.body).toMatchObject({
    desiredOutputCapability: 'application',
  })
  expect(generateRequest?.body).not.toHaveProperty('candidateDefinitionVersionIds')

  await panel.getByRole('button', { name: 'Accept', exact: true }).click()
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'),
  )).toBe(true)

  const commandRequest = state.requests.find((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'))
  expect(commandRequest?.body).toEqual({})
  expect(state.conversationCommand?.result).toMatchObject({
    runId: 'run-selection-active',
    rootBundleId: 'build-1',
    bundleId: 'build-1',
    implementationProposalId: '77777777-7777-4777-8777-777777777777',
  })
  expect(state.conversationCommand?.payload.definitionVersionId)
    .toBe(selectionWorkflowDefinition.versionId)
  expect(state.requests.some((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/workflow-runs`)).toBe(false)
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'GET'
      && item.path.endsWith('/workflow-runs/run-selection-active'))).toBe(true)
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'GET'
      && item.path.endsWith('/implementation-proposals/77777777-7777-4777-8777-777777777777'))).toBe(true)
  const executeIndex = state.requests.findIndex((item) => item === commandRequest)
  const targetRunLoadIndex = state.requests.findIndex((item, index) =>
    index > executeIndex
      && item.method === 'GET'
      && item.path.endsWith('/workflow-runs/run-selection-active'))
  const proposalLoadIndex = state.requests.findIndex((item, index) =>
    index > targetRunLoadIndex
      && item.method === 'GET'
      && item.path.endsWith('/implementation-proposals/77777777-7777-4777-8777-777777777777'))
  expect(targetRunLoadIndex).toBeGreaterThan(executeIndex)
  expect(proposalLoadIndex).toBeGreaterThan(targetRunLoadIndex)
})

test('conversation sends the selected Workbench root as a hint and renders server page semantics', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectedWorkbenchTarget: true,
  })
  await page.goto('/workbench/planning?view=code&runId=run-selection-active')
  const panel = await openConversationPanel(page)
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'GET' && item.path === '/v1/build-manifests/build-root-1/lineage-state'),
  ).toBe(true)

  await panel.getByPlaceholder('Describe requirements or a controlled next action…')
    .fill('Continue the Orders page from the selected Workbench target.')
  await panel.getByRole('button', { name: 'Send immutable message' }).click()
  await panel.getByRole('button', { name: 'Generate governed intent' }).click()

  await expect(panel.getByText('Page: Orders', { exact: true })).toBeVisible()
  await expect(panel.getByText('ORDERS', { exact: true })).toBeVisible()
  const generateRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path.endsWith('/intent-proposals/generate'))
  expect(generateRequest?.body).toMatchObject({
    workbenchTargetHint: {
      runId: 'run-selection-active',
      rootBundleId: 'build-root-1',
    },
  })
})

test('conversation displays the RFC summary-checkpoint conflict without hiding its detail', async ({ page }) => {
  await installPlatformMock(page, {
    authenticated: true,
    conversationSummaryConflict: true,
  })
  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  await panel.getByPlaceholder('Describe requirements or a controlled next action…')
    .fill('Generate the next governed intent.')
  await panel.getByRole('button', { name: 'Send immutable message' }).click()
  await panel.getByRole('button', { name: 'Generate governed intent' }).click()

  await expect(panel.getByRole('alert')).toContainText(
    'Create and review a controlled summary checkpoint; no messages were silently omitted.',
  )
})

test('conversation creates an immutable summary checkpoint at the server-recommended prefix', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSummaryConflict: true,
  })
  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  const composer = panel.getByPlaceholder('Describe requirements or a controlled next action…')

  await composer.fill('Preserve the approved product requirements and constraints.')
  await panel.getByRole('button', { name: 'Send immutable message' }).click()
  await composer.fill('Generate a governed intent from the complete conversation.')
  await panel.getByRole('button', { name: 'Send immutable message' }).click()
  await panel.getByRole('button', { name: 'Generate governed intent' }).last().click()

  const checkpointSection = panel.getByRole('region', {
    name: 'Conversation summary checkpoint required',
  })
  await expect(checkpointSection).toBeVisible()
  await expect(checkpointSection).toContainText('through message #1')
  await panel.getByRole('textbox', { name: 'Conversation checkpoint summary' }).fill(
    'The immutable prefix requires preserving all approved product constraints.',
  )
  await panel.getByRole('button', {
    name: 'Submit immutable summary for governed review',
  }).click()

  const checkpointCard = panel.getByRole('region', { name: 'Summary checkpoint 1' })
  await expect(checkpointCard).toContainText(/pending review/i)
  await checkpointCard.getByRole('button', { name: 'Inspect exact bound source delta' }).click()
  await expect(checkpointCard).toContainText('Team mode requires another member to review')
  await expect(panel.getByRole('button', { name: 'Approve summary checkpoint 1' })).toBeDisabled()
  expect(state.conversationSummaryCheckpoints).toHaveLength(1)
  const createRequest = state.requests.find((item) => (
    item.method === 'POST'
    && item.path.endsWith('/summary-checkpoints')
  ))
  expect(createRequest?.body).toEqual({
    throughMessageId: state.conversationMessages[0].id,
    summary: 'The immutable prefix requires preserving all approved product constraints.',
  })
  expect(createRequest?.headers['if-match']).toBe(state.conversations[0].etag)
  expect(createRequest?.headers['idempotency-key']).toBeTruthy()
})

test('independent checkpoint review unlocks an exact governed-intent retry', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSummaryConflict: true,
    conversationCheckpointSourcePageSize: 1,
  })
  const conversationId = state.conversations[0].id
  const prefix = conversationMessage(
    '91919191-9191-4191-8191-919191919191',
    conversationId,
    1,
    'user',
    'The approved product constraint remains immutable.',
  )
  const secondPrefix = conversationMessage(
    '92929292-9292-4292-8292-929292929292',
    conversationId,
    2,
    'user',
    'The reviewed accessibility constraint remains immutable.',
  )
  const trigger = conversationMessage(
    '94949494-9494-4494-8494-949494949494',
    conversationId,
    3,
    'user',
    'Generate the next governed intent.',
  )
  state.conversationMessages = [prefix, secondPrefix, trigger]
  state.conversationSummaryCheckpoints = [conversationSummaryCheckpoint(
    '93939393-9393-4393-8393-939393939393',
    conversationId,
    secondPrefix,
    'The approved product and accessibility constraints must be preserved in every generated application.',
    { createdBy: reviewer.id },
  )]

  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  await panel.getByRole('button', { name: 'Generate governed intent' }).last().click()
  await expect(panel.getByRole('region', {
    name: 'Conversation summary checkpoint required',
  })).toBeVisible()

  await panel.getByRole('button', { name: 'Inspect exact bound source delta' }).click()
  await expect(panel.getByText('Exact immutable source delta · 2 messages')).toBeVisible()
  const approve = panel.getByRole('button', { name: 'Approve summary checkpoint 2' })
  await expect(approve).toBeEnabled()
  await approve.click()
  const retry = panel.getByRole('button', {
    name: 'Retry governed intent with approved checkpoint',
  })
  await expect(retry).toBeVisible()
  await retry.click()

  await expect(panel.getByText('Start workflow', { exact: true })).toBeVisible()
  expect(state.conversationSummaryCheckpoints[0].status).toBe('approved')
  expect(state.requests.filter((item) => (
    item.method === 'POST'
    && item.path.endsWith('/intent-proposals/generate')
  ))).toHaveLength(2)
  expect(state.requests.some((item) => (
    item.method === 'POST'
    && item.path.endsWith(`/summary-checkpoints/${state.conversationSummaryCheckpoints[0].id}/decision`)
  ))).toBe(true)
  const sourceRequests = state.requests.filter((item) => item.path.endsWith('/source-messages'))
  expect(sourceRequests).toHaveLength(2)
  const decisionRequest = state.requests.find((item) => (
    item.method === 'POST'
    && item.path.endsWith(`/summary-checkpoints/${state.conversationSummaryCheckpoints[0].id}/decision`)
  ))
  expect(decisionRequest?.headers['if-match']).toBe('"conversation-summary-checkpoint:93939393-9393-4393-8393-939393939393:1"')
  expect(decisionRequest?.headers['idempotency-key']).toBeTruthy()
})

test('sole Owner explicitly confirms a Solo conversation checkpoint self-review', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    soloProject: true,
  })
  const conversationId = state.conversations[0].id
  const through = conversationMessage(
    'a3939393-9393-4393-8393-939393939393',
    conversationId,
    1,
    'user',
    'Preserve the complete product decision and its accessibility constraint.',
  )
  const checkpoint = conversationSummaryCheckpoint(
    'a4949494-9494-4494-8494-949494949494',
    conversationId,
    through,
    'The product decision and accessibility constraint remain required.',
    { createdBy: user.id },
  )
  state.conversationMessages = [through]
  state.conversationSummaryCheckpoints = [checkpoint]

  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  const checkpointCard = panel.getByRole('region', { name: 'Summary checkpoint 1' })
  await expect(checkpointCard).toContainText('Solo mode permits this only for the sole Owner')

  const approve = checkpointCard.getByRole('button', { name: 'Approve summary checkpoint 1' })
  await expect(approve).toBeDisabled()
  await checkpointCard.getByRole('button', { name: 'Inspect exact bound source delta' }).click()
  await expect(approve).toBeDisabled()

  await checkpointCard.getByRole('textbox', {
    name: 'Solo summary checkpoint review reason',
  }).fill('Verified the exact source, decisions, constraints, and open questions.')
  await expect(approve).toBeDisabled()
  await checkpointCard.getByRole('checkbox', {
    name: 'I inspected the exact complete source and accept responsibility for this self-review as the sole Owner.',
  }).check()
  await expect(approve).toBeEnabled()
  await approve.click()

  await expect(checkpointCard).toContainText(/approved/i)
  const decisionRequest = state.requests.find((item) => (
    item.method === 'POST'
    && item.path.endsWith(`/summary-checkpoints/${checkpoint.id}/decision`)
  ))
  expect(decisionRequest?.body).toEqual({
    decision: 'approve',
    reason: 'Verified the exact source, decisions, constraints, and open questions.',
    soloReviewConfirmed: true,
  })
  expect(decisionRequest?.headers['if-match']).toBe(checkpoint.etag)
  expect(decisionRequest?.headers['idempotency-key']).toBeTruthy()
})

test('checkpoint review rejects a paginated source whose final message identity drifts', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const conversationId = state.conversations[0].id
  const first = conversationMessage(
    'a1919191-9191-4191-8191-919191919191',
    conversationId,
    1,
    'user',
    'First exact immutable source message.',
  )
  const through = conversationMessage(
    'a2929292-9292-4292-8292-929292929292',
    conversationId,
    2,
    'user',
    'Second exact immutable source message.',
  )
  const checkpoint = conversationSummaryCheckpoint(
    'a3939393-9393-4393-8393-939393939393',
    conversationId,
    through,
    'Both immutable source messages must remain bound.',
    { createdBy: reviewer.id },
  )
  state.conversationMessages = [first, through]
  state.conversationSummaryCheckpoints = [checkpoint]
  await page.route(
    `**/v1/projects/${project.id}/conversations/${conversationId}/summary-checkpoints/${checkpoint.id}/source-messages**`,
    (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: corsHeaders(),
      body: JSON.stringify({
        items: [first, { ...through, id: 'a4949494-9494-4494-8494-949494949494' }],
      }),
    }),
  )

  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  await panel.getByRole('button', { name: 'Inspect exact bound source delta' }).click()

  await expect(panel.getByRole('alert')).toContainText(
    'The checkpoint source response is not the exact continuous bound delta.',
  )
  await expect(panel.getByRole('button', { name: 'Approve summary checkpoint 2' })).toBeDisabled()
  expect(state.requests.some((item) => (
    item.method === 'POST'
    && item.path.endsWith(`/summary-checkpoints/${checkpoint.id}/decision`)
  ))).toBe(false)
})

test('conversation checkpoints the current Project Brief draft before generating intent', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, staleBriefDraft: true })
  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)

  await expect.poll(() => state.requests.some((item) =>
    item.path === `/v1/projects/${project.id}/documents`,
  )).toBe(true)
  await panel.getByPlaceholder('Describe requirements or a controlled next action…').fill('Generate from this brief.')
  await panel.getByRole('button', { name: 'Send immutable message' }).click()
  await panel.getByRole('button', { name: 'Checkpoint Brief and generate intent' }).click()

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

test('conversation hydration ignores a delayed response from the previously selected conversation', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  const conversationA = conversationRecord('10101010-1010-4010-8010-101010101010', 'Conversation A')
  const conversationB = conversationRecord('20202020-2020-4020-8020-202020202020', 'Conversation B')
  const messageA = conversationMessage('30303030-3030-4030-8030-303030303030', conversationA.id, 1, 'user', 'stale conversation A content')
  const messageB = conversationMessage('40404040-4040-4040-8040-404040404040', conversationB.id, 1, 'user', 'current conversation B content')
  let delayedRequests = 0
  let delayedResponses = 0
  let resolveDelayedStarted!: () => void
  let releaseDelayed!: () => void
  let resolveDelayedFinished!: () => void
  const delayedStarted = new Promise<void>((resolve) => { resolveDelayedStarted = resolve })
  const delayedRelease = new Promise<void>((resolve) => { releaseDelayed = resolve })
  const delayedFinished = new Promise<void>((resolve) => { resolveDelayedFinished = resolve })
  const respond = (route: Route, data: unknown) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify(data),
  })
  await page.route(new RegExp(`/v1/projects/${project.id}/conversations(?:\\?.*)?$`), (route) =>
    respond(route, { items: [conversationA, conversationB] }))
  for (const resource of ['messages', 'intent-proposals', 'commands']) {
    await page.route(new RegExp(`/v1/projects/${project.id}/conversations/${conversationA.id}/${resource}(?:\\?.*)?$`), async (route) => {
      delayedRequests += 1
      if (delayedRequests === 3) resolveDelayedStarted()
      await delayedRelease
      await respond(route, { items: resource === 'messages' ? [messageA] : [] })
      delayedResponses += 1
      if (delayedResponses === 3) resolveDelayedFinished()
    })
    await page.route(new RegExp(`/v1/projects/${project.id}/conversations/${conversationB.id}/${resource}(?:\\?.*)?$`), (route) =>
      respond(route, { items: resource === 'messages' ? [messageB] : [] }))
  }

  await page.goto(`/workbench/planning?view=code&conversationId=${conversationA.id}`)
  await delayedStarted
  const panel = await openConversationPanel(page)
  const conversationSelect = panel.getByRole('combobox')
  await conversationSelect.selectOption(conversationB.id)
  await expect(page.getByText(messageB.content, { exact: true })).toBeVisible()
  releaseDelayed()
  await delayedFinished

  await expect(conversationSelect).toHaveValue(conversationB.id)
  await expect(page.getByText(messageB.content, { exact: true })).toBeVisible()
  await expect(page.getByText(messageA.content, { exact: true })).toHaveCount(0)
})

test('conversation polling does not cancel an in-flight controlled Workbench execution', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectionTarget: true,
  })
  const conversationId = state.conversations[0].id
  const trigger = conversationMessage(
    '50505050-5050-4050-8050-505050505050',
    conversationId,
    1,
    'user',
    'Generate the governed Workbench proposal after polling.',
  )
  const proposal = workbenchIntentProposal(conversationId, trigger.id, {
    expectedRunId: '88888888-8888-4888-8888-888888888888',
    expectedBundleId: 'build-1',
    definitionVersionId: selectionWorkflowDefinition.versionId,
  })
  state.conversationMessages = [trigger, conversationMessage(
    proposal.assistantMessageId,
    conversationId,
    2,
    'assistant',
    'Execute this accepted Workbench command.',
    proposal.id,
  )]
  state.intentProposal = proposal
  state.conversationCommand = conversationCommand(proposal)
  let resolveExecuteStarted!: () => void
  let releaseExecute!: () => void
  let resolvePollDuringExecute!: () => void
  const executeStarted = new Promise<void>((resolve) => { resolveExecuteStarted = resolve })
  const executeRelease = new Promise<void>((resolve) => { releaseExecute = resolve })
  const pollDuringExecute = new Promise<void>((resolve) => { resolvePollDuringExecute = resolve })
  let executePending = false
  const messagePath = `/v1/projects/${project.id}/conversations/${conversationId}/messages`
  await page.route(new RegExp(`${messagePath}(?:\\?.*)?$`), async (route) => {
    if (executePending) resolvePollDuringExecute()
    await route.fallback()
  })
  await page.route(`**/v1/projects/${project.id}/conversations/${conversationId}/commands/${state.conversationCommand.id}/execute`, async (route) => {
    executePending = true
    resolveExecuteStarted()
    await executeRelease
    await route.fallback()
    executePending = false
  })

  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()
  await executeStarted
  await pollDuringExecute
  releaseExecute()

  await expect.poll(() => state.conversationCommand?.status).toBe('executed')
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'GET'
      && item.path.endsWith(`/workflow-runs/${proposal.workbenchInstruction.expectedRunId}`))).toBe(true)
  await expect(page.getByText(proposal.workbenchInstruction.expectedRunId, { exact: true }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Finish current review' })).toBeVisible()
})

test('same-run polling coalesces with delayed receipt lineage hydration', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectionTarget: true,
  })
  const conversationId = state.conversations[0].id
  const trigger = conversationMessage(
    '51515151-5151-4151-8151-515151515151',
    conversationId,
    1,
    'user',
    'Keep the receipt hydration alive across the same-run poll.',
  )
  const proposal = workbenchIntentProposal(conversationId, trigger.id, {
    expectedRunId: '89898989-8989-4989-8989-898989898989',
    expectedBundleId: 'build-1',
    definitionVersionId: selectionWorkflowDefinition.versionId,
  })
  state.conversationMessages = [trigger, conversationMessage(
    proposal.assistantMessageId,
    conversationId,
    2,
    'assistant',
    'Hydrate this exact accepted Workbench receipt.',
    proposal.id,
  )]
  state.intentProposal = proposal
  state.conversationCommand = conversationCommand(proposal)
  let resolveLineageStarted!: () => void
  let releaseLineage!: () => void
  const lineageStarted = new Promise<void>((resolve) => { resolveLineageStarted = resolve })
  const lineageRelease = new Promise<void>((resolve) => { releaseLineage = resolve })
  await page.route('**/v1/build-manifests/build-1/lineage-state', async (route) => {
    resolveLineageStarted()
    await lineageRelease
    await route.fallback()
  })

  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()
  await lineageStarted
  await page.waitForTimeout(3_600)
  releaseLineage()

  await expect(page.getByRole('button', { name: 'Finish current review' })).toBeVisible()
  await expect(panel.getByRole('alert')).toHaveCount(0)
  expect(state.requests.filter((item) => (
    item.method === 'GET'
    && item.path === '/v1/build-manifests/build-1/lineage-state'
  ))).toHaveLength(1)
})

test('conversation receipt hydration rejects a bundle from the wrong reviewed slice', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectionTarget: true,
  })
  const conversationId = state.conversations[0].id
  const trigger = conversationMessage(
    '60606060-6060-4060-8060-606060606060',
    conversationId,
    1,
    'user',
    'Generate only the reviewed Orders slice.',
  )
  const proposal = workbenchIntentProposal(conversationId, trigger.id, {
    expectedRunId: '88888888-8888-4888-8888-888888888888',
    expectedBundleId: 'build-1',
    definitionVersionId: selectionWorkflowDefinition.versionId,
  })
  state.conversationMessages = [trigger, conversationMessage(
    proposal.assistantMessageId,
    conversationId,
    2,
    'assistant',
    'Execute the exact reviewed Orders command.',
    proposal.id,
  )]
  state.intentProposal = proposal
  state.conversationCommand = conversationCommand(proposal)
  const wrongSliceBundle = {
    ...state.workbenchBundle,
    deliverySliceId: 'page-wrong',
    workflowContext: state.workbenchBundle.workflowContext
      ? { ...state.workbenchBundle.workflowContext, deliverySliceId: 'page-wrong' }
      : undefined,
  }
  await page.route('**/v1/build-manifests/build-1', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify(wrongSliceBundle),
  }))

  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()

  await expect(panel.getByRole('alert')).toContainText(
    'does not match the reviewed run, group, root, and delivery slice receipt',
  )
})

test('conversation receipt rejects a proposal manifest mismatch before hydrating another bundle', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectionTarget: true,
  })
  const conversationId = state.conversations[0].id
  const trigger = conversationMessage(
    '61616161-6161-4161-8161-616161616161',
    conversationId,
    1,
    'user',
    'Generate only from the exact reviewed Workbench receipt.',
  )
  const proposal = workbenchIntentProposal(conversationId, trigger.id, {
    expectedRunId: '88888888-8888-4888-8888-888888888888',
    expectedBundleId: 'build-1',
    definitionVersionId: selectionWorkflowDefinition.versionId,
  })
  state.conversationMessages = [trigger, conversationMessage(
    proposal.assistantMessageId,
    conversationId,
    2,
    'assistant',
    'Execute the exact reviewed Workbench command.',
    proposal.id,
  )]
  state.intentProposal = proposal
  state.conversationCommand = conversationCommand(proposal)
  const corruptBundleId = 'build-unreviewed'
  await page.route(
    `**/v1/implementation-proposals/${state.conversationCommand.id}`,
    (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: corsHeaders(),
      body: JSON.stringify({
        ...implementationProposal('open'),
        id: state.conversationCommand!.id,
        buildManifestId: corruptBundleId,
      }),
    }),
  )
  await page.route(`**/v1/build-manifests/${corruptBundleId}`, (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify({
      ...state.workbenchBundle,
      id: corruptBundleId,
      rootBuildManifestId: corruptBundleId,
    }),
  }))

  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()

  await expect(panel.getByRole('alert')).toContainText(
    'The implementation proposal does not belong to the receipt bundle.',
  )
  expect(state.requests.some((item) => (
    item.method === 'GET'
    && item.path === `/v1/build-manifests/${corruptBundleId}`
  ))).toBe(false)
})

test('conversation receipt rejects a proposal not bound to the executed command', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    conversationSelectionTarget: true,
  })
  const conversationId = state.conversations[0].id
  const trigger = conversationMessage(
    'a1616161-6161-4161-8161-616161616161',
    conversationId,
    1,
    'user',
    'Generate only the proposal bound to this accepted command.',
  )
  const proposal = workbenchIntentProposal(conversationId, trigger.id, {
    expectedRunId: '88888888-8888-4888-8888-888888888888',
    expectedBundleId: 'build-1',
    definitionVersionId: selectionWorkflowDefinition.versionId,
  })
  state.conversationMessages = [trigger, conversationMessage(
    proposal.assistantMessageId,
    conversationId,
    2,
    'assistant',
    'Execute the exact command-bound proposal.',
    proposal.id,
  )]
  state.intentProposal = proposal
  state.conversationCommand = conversationCommand(proposal)
  await page.route(
    `**/v1/implementation-proposals/${state.conversationCommand.id}`,
    (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: corsHeaders(),
      body: JSON.stringify({
        ...implementationProposal('open'),
        id: state.conversationCommand!.id,
        buildManifestId: 'build-1',
        executionSource: 'conversation_command',
        conversationCommandId: 'unreviewed-command',
        instructionHash: hash('7'),
      }),
    }),
  )

  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()

  await expect(panel.getByRole('alert')).toContainText(
    'The implementation proposal does not belong to the receipt bundle.',
  )
  await expect(page.getByRole('button', { name: 'Finish current review' })).toHaveCount(0)
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
  const panel = await openConversationPanel(page)
  const execute = panel.getByRole('button', { name: 'Generate Workbench proposal' })
  await expect(execute).toBeVisible()
  await execute.click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'),
  )).toBe(true)
  const commandRequest = state.requests.find((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'))
  expect(commandRequest?.body).toEqual({})
  expect(state.requests.some((item) =>
    item.method === 'POST' && item.path.endsWith('/build-manifests/build-1/generate'),
  )).toBe(false)
  expect(state.conversationCommand?.result).toMatchObject({
    runId: proposal.workbenchInstruction.expectedRunId,
    rootBundleId: proposal.workbenchInstruction.expectedBundleId,
    implementationProposalId: '77777777-7777-4777-8777-777777777777',
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
  const panel = await openConversationPanel(page)
  await panel.getByRole('button', { name: 'Generate Workbench proposal' }).click()

  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path.startsWith('/v1/build-manifests/')
      && item.path.endsWith('/generate'),
  )).toBe(false)
  const commandRequest = state.requests.find((item) =>
    item.method === 'POST'
      && item.path.endsWith('/commands/77777777-7777-4777-8777-777777777777/execute'))
  expect(commandRequest?.body).toEqual({})
  expect(state.conversationCommand?.result).toMatchObject({
    runId: 'run-groups',
    rootBundleId: 'bundle-group-b',
    bundleId: 'bundle-group-b',
    implementationProposalId: '77777777-7777-4777-8777-777777777777',
  })
})

test('commenters can append immutable conversation messages but cannot generate intents', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, role: 'commenter' })
  await page.goto('/workbench/planning?view=code')
  const panel = await openConversationPanel(page)

  const composer = panel.getByPlaceholder('Describe requirements or a controlled next action…')
  await composer.fill('A commenter can add requirement context.')
  const send = panel.getByRole('button', { name: 'Send immutable message' })
  await expect(send).toBeEnabled()
  await send.click()
  await expect(panel.getByText('A commenter can add requirement context.')).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Generate governed intent' })).toBeDisabled()
  await expect(panel.getByRole('button', { name: 'New conversation' })).toBeDisabled()

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
  const panel = await openConversationPanel(page)

  await expect(panel.getByRole('option', { name: 'Project discovery · active' })).toHaveCount(1)
  await panel.getByRole('button', { name: 'New conversation' }).click()
  await panel.getByPlaceholder('Conversation title').fill('Second planning thread')
  await panel.getByRole('button', { name: 'Create', exact: true }).click()

  await expect(panel.getByRole('option', { name: 'Second planning thread · active' })).toHaveCount(1)
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
  await expect(page.getByRole('button', {
    name: 'src/index.html Create or update file · Accepted',
  })).toBeVisible()
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
  await expect(page.getByText('blueprint.selection · 2 sources')).toBeVisible()
  await page.getByText('Inspect frozen context evidence').click()
  await expect(page.getByText('blueprint_selection_node', { exact: false })).toBeVisible()
  await expect(page.getByText('reviewed-start-proposal', { exact: false })).toBeVisible()
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
  await expect(page.getByRole('button', {
    name: 'src/shared.ts Create or update file · Accepted',
  })).toBeVisible()

  await page.reload()
  await expect(page.getByText(/bundle-checkout → bundle-checkout-w1/)).toBeVisible()
  await expect(page.getByRole('button', {
    name: 'src/shared.ts Create or update file · Accepted',
  })).toBeVisible()
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
    name: 'Workbench group workbench-a, status waiting input',
  })).toBeVisible()
  await expect(page.getByText(/bundle-group-a ·/)).toBeVisible()
  expect(state.requests.some((item) =>
    item.path === '/v1/build-manifests/bundle-group-a/lineage-state',
  )).toBe(true)
  expect(state.requests.some((item) =>
    item.path === '/v1/build-manifests/bundle-group-b/lineage-state',
  )).toBe(false)

  await page.getByRole('button', {
    name: 'Workbench group workbench-b, status waiting input',
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
    name: 'Workbench group workbench-a, status waiting input',
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

test('Design Import Center freezes an upload, requires an independent reviewer, and applies a Prototype revision', async ({ page }) => {
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
  await expect(page.getByTestId('design-import-list').getByText('Open', { exact: true })).toBeVisible()
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

  const approveButton = page.getByTestId(`design-import-approve-${state.designImports[0].id}`)
  await expect(approveButton).toBeDisabled()
  await expect(page.getByTestId(`design-import-independent-review-${state.designImports[0].id}`)).toContainText('Creators cannot review')
  state.designImports = [{
    ...state.designImports[0],
    createdBy: reviewer.id,
    proposal: { ...state.designImports[0].proposal, createdBy: reviewer.id },
  }]
  await page.reload()
  await expect(page.getByTestId(`design-import-approve-${state.designImports[0].id}`)).toBeEnabled()
  await page.getByTestId(`design-import-approve-${state.designImports[0].id}`).click()
  await expect(page.getByTestId('design-import-list').getByText('Applied', { exact: true })).toBeVisible()
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
  }, reviewer.id)]
  await page.goto(`/team/acme/project/${project.id}/imports`)
  await page.getByTestId(`design-import-approve-${state.designImports[0].id}`).click()
  await expect(page.getByTestId('design-import-center').getByRole('alert')).toContainText('changed since it was loaded')
  await expect(page.getByTestId('design-import-list').getByText('Open', { exact: true })).toBeVisible()
  expect(state.designImports[0].status).toBe('open')
})

test('Design Import Center retries a processing lease with the same command key', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, designImportCreateProcessingOnce: true })
  await page.goto(`/team/acme/project/${project.id}/imports`)
  await page.getByTestId('design-import-file').setInputFiles({
    name: 'retry-design.json',
    mimeType: 'application/json',
    buffer: Buffer.from('{"pages":[{"id":"home","name":"Home"}]}'),
  })
  await page.getByTestId('design-import-submit').click()
  await expect(page.getByTestId('design-import-center').getByRole('alert')).toContainText('durable creation lease')
  await page.getByTestId('design-import-submit').click()
  await expect(page.getByTestId('design-import-list')).toContainText('retry-design')
  const requests = state.requests.filter((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/design-imports`)
  expect(requests).toHaveLength(2)
  expect(requests[0].headers['idempotency-key']).toBeTruthy()
  expect(requests[1].headers['idempotency-key']).toBe(requests[0].headers['idempotency-key'])
})

test('document dependency graph opens the exact document, PageSpec, and prototype workspaces', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  const graphUrl = `/team/acme/project/${project.id}/graph`

  await page.goto(graphUrl)
  await page.getByRole('button', { name: 'Open in workspace' }).click()
  await expect(page).toHaveURL(new RegExp(`/editor\\?artifactId=${projectBrief.artifact.id}`))
  await expect(page.getByText('Project Brief', { exact: true }).first()).toBeVisible()

  await page.goto(graphUrl)
  await page.locator('button').filter({ hasText: 'Dashboard PageSpec' }).first().click()
  await page.getByRole('button', { name: 'Open in workspace' }).click()
  await expect(page).toHaveURL(new RegExp(`/blueprint\\?artifactId=${pageSpec.artifact.id}`))
  await expect(page.getByText('Dashboard PageSpec', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'States (4)' })).toBeVisible()

  await page.goto(graphUrl)
  await page.locator('button').filter({ hasText: 'Dashboard Prototype' }).first().click()
  await page.getByRole('button', { name: 'Open in workspace' }).click()
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
  await page.getByRole('button', { name: 'Reload bindings' }).click()
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

  await page.getByRole('button', { name: 'Trace', exact: true }).click()
  await expect(page.getByText('prototype-proposal-1', { exact: true })).toBeVisible()
  await page.getByRole('button', {
    name: 'Accept proposal operation prototype-operation-name',
  }).click()
  await expect(page.getByText('Accepted', { exact: true })).toBeVisible()
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
  await page.getByRole('button', { name: 'PageSpecs (1)' }).click()
  await page.getByRole('button', { name: 'Open editor' }).click()
  await expect(page.getByText('Dashboard PageSpec', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'States (4)' })).toBeVisible()

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
  const createPageSpecRevision = page.getByRole('button', { name: 'Create immutable revision' })
  await createPageSpecRevision.click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST' && item.path === `/v1/page-specs/${pageSpec.artifact.id}/revisions`,
  )).toBe(true)
  await expect(createPageSpecRevision).toBeDisabled()

  await page.getByRole('button', { name: 'Review 0' }).last().click()
  await page.getByLabel('PageSpec reviewer', { exact: true }).selectOption(reviewer.id)
  await page.getByRole('button', { name: 'Request Review' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/reviews`
      && (item.body as { target?: { revisionId?: string } })?.target?.revisionId === 'dddddddd-dddd-4ddd-8ddd-ddddddddddd4',
  )).toBe(true)
})

test('Blueprint autosave preserves newer input and background refresh keeps the editor mounted', async ({ page }) => {
  const realtime = await installProjectWebSocketMock(page)
  const state = await installPlatformMock(page, {
    authenticated: true,
    pauseFirstBlueprintDraftSave: true,
  })
  const firstSaveGate = state.blueprintDraftSaveGate
  expect(firstSaveGate).not.toBeNull()
  await page.goto(`/team/acme/project/${project.id}/blueprint`)

  const editor = page.getByRole('main')
  const title = page.getByLabel('Node title')
  await expect(editor).toBeVisible()
  await expect(title).toHaveValue('Order management')

  await title.fill('Order operations')
  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'PATCH'
      && item.path === `/v1/blueprints/${blueprint.artifact.id}/draft`).length,
  ).toBe(1)

  await title.fill('Order operations and exceptions')
  firstSaveGate!.release()
  await expect(page.getByText(/blueprint-draft:2/)).toBeVisible()
  await expect(title).toHaveValue('Order operations and exceptions')
  await expect.poll(() => state.requests.some((item) => {
    if (item.method !== 'PATCH' || item.path !== `/v1/blueprints/${blueprint.artifact.id}/draft`) return false
    const content = (item.body as { content?: typeof blueprintContent })?.content
    return content?.semantic.nodes.find((node) => node.id === 'feature-orders')?.title
      === 'Order operations and exceptions'
  })).toBe(true)
  await expect(page.getByText(/blueprint-draft:3/)).toBeVisible()

  const listRequestsBeforeRefresh = state.requests.filter((item) =>
    item.method === 'GET' && item.path === `/v1/projects/${project.id}/blueprints`).length
  const refreshGate = deferred()
  state.blueprintListGate = refreshGate
  await expect.poll(() => realtime.ready(project.id)).toBe(true)
  realtime.emit('artifact.draft_updated', project.id, {
    projectId: project.id,
    artifactId: blueprint.artifact.id,
    draftId: state.blueprint.draft?.id ?? 'blueprint-draft-1',
    sequence: state.blueprint.draft?.revision ?? 3,
  })
  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'GET' && item.path === `/v1/projects/${project.id}/blueprints`).length,
  ).toBeGreaterThan(listRequestsBeforeRefresh)

  const freshGate = deferred()
  state.blueprintListGate = freshGate
  await title.fill('Order operations, exceptions, and refunds')
  await expect.poll(() => state.requests.some((item) => {
    if (item.method !== 'PATCH' || item.path !== '/v1/blueprints/' + blueprint.artifact.id + '/draft') return false
    const content = (item.body as { content?: typeof blueprintContent })?.content
    return content?.semantic.nodes.find((node) => node.id === 'feature-orders')?.title
      === 'Order operations, exceptions, and refunds'
  })).toBe(true)
  await expect(page.getByText(/blueprint-draft:4/)).toBeVisible()

  refreshGate.release()
  await expect.poll(() => state.requests.filter((item) => (
    item.method === 'GET' && item.path === '/v1/projects/' + project.id + '/blueprints'
  )).length).toBeGreaterThan(listRequestsBeforeRefresh + 1)

  try {
    await expect(editor).toBeVisible()
    await expect(title).toBeVisible()
    await expect(title).toHaveValue('Order operations, exceptions, and refunds')
    await expect(page.getByText(/blueprint-draft:4/)).toBeVisible()
    await expect(page.getByRole('heading', { name: /Loading (?:platform )?Blueprints?/i })).toHaveCount(0)
  } finally {
    freshGate.release()
  }

  await expect(title).toHaveValue('Order operations, exceptions, and refunds')
})

test('Blueprint autosave cannot write one artifact content into another while switching artifacts', async ({ page }) => {
  const realtime = await installProjectWebSocketMock(page)
  const state = await installPlatformMock(page, {
    authenticated: true,
    secondBlueprint: true,
    pauseFirstBlueprintDraftSave: true,
  })
  const firstSaveGate = state.blueprintDraftSaveGate
  expect(firstSaveGate).not.toBeNull()
  await page.goto('/team/acme/project/' + project.id + '/blueprint')

  const title = page.getByLabel('Node title')
  const secondary = page.getByRole('button', { name: /Billing Blueprint/ })
  const primaryDraftPath = '/v1/blueprints/' + blueprint.artifact.id + '/draft'
  const secondaryDraftPath = '/v1/blueprints/' + state.secondaryBlueprint!.artifact.id + '/draft'

  await expect.poll(() => realtime.ready(project.id)).toBe(true)
  await expect(title).toHaveValue('Order management')
  await title.fill('Order operations')
  await expect.poll(() => state.requests.filter((item) => (
    item.method === 'PATCH' && item.path === primaryDraftPath
  )).length).toBe(1)
  await title.fill('Order operations and exceptions')
  await expect(secondary).toBeDisabled()

  firstSaveGate!.release()
  await expect.poll(() => state.requests.some((item) => {
    if (item.method !== 'PATCH' || item.path !== primaryDraftPath) return false
    const content = (item.body as { content?: typeof blueprintContent })?.content
    return content?.semantic.nodes.find((node) => node.id === 'feature-orders')?.title
      === 'Order operations and exceptions'
  })).toBe(true)
  expect(state.secondaryBlueprint?.draft).toBeUndefined()
  await expect(secondary).toBeEnabled()
  await secondary.click()
  await expect(title).toHaveValue('Billing management')

  await title.fill('Billing operations')
  await expect.poll(() => state.requests.some((item) => {
    if (item.method !== 'PATCH' || item.path !== secondaryDraftPath) return false
    const content = (item.body as { content?: typeof blueprintContent })?.content
    return content?.semantic.nodes.find((node) => node.id === 'feature-orders')?.title
      === 'Billing operations'
  })).toBe(true)

  const primarySaves = state.requests.filter((item) => item.method === 'PATCH' && item.path === primaryDraftPath)
  expect(primarySaves[0]?.headers['if-match']).toBe(blueprint.artifact.etag)
  expect(primarySaves[1]?.headers['if-match']).toBe('"blueprint-draft:2"')
  expect(primarySaves.every((item) => {
    const content = (item.body as { content?: typeof blueprintContent })?.content
    return content?.semantic.nodes.find((node) => node.id === 'feature-orders')?.title !== 'Billing operations'
  })).toBe(true)
  expect(state.blueprint.draft?.content.semantic.nodes.find((node) => node.id === 'feature-orders')?.title)
    .toBe('Order operations and exceptions')
})

test('Blueprint actions lock editing and artifact switching until the server responds', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    blueprintProposal: true,
    secondBlueprint: true,
    pauseBlueprintProposalApply: true,
  })
  const applyGate = state.blueprintProposalApplyGate
  expect(applyGate).not.toBeNull()
  await page.goto('/team/acme/project/' + project.id + '/blueprint')

  await page.getByRole('button', { name: 'Proposals 1' }).click()
  await page.getByRole('button', { name: 'Apply accepted operations' }).click()
  await expect.poll(() => state.requests.some((item) => (
    item.method === 'POST' && item.path === '/v1/output-proposals/blueprint-proposal-1/apply'
  ))).toBe(true)

  await page.getByRole('button', { name: 'Blueprint graph (2)', exact: true }).evaluate((button) => (button as HTMLButtonElement).click())
  const title = page.getByLabel('Node title')
  const secondary = page.getByRole('button', { name: /Billing Blueprint/ })
  await expect(title).not.toBeEditable()
  await expect(secondary).toBeDisabled()

  applyGate!.release()
  await expect(title).toBeEditable()
  await expect(secondary).toBeEnabled()
  await expect(title).toHaveValue('AI-reviewed order management')
})

test('Blueprint 412 conflict remains blocking while newer local input is preserved', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    rejectFirstBlueprintDraftSave: true,
  })
  await page.goto('/team/acme/project/' + project.id + '/blueprint')

  const title = page.getByLabel('Node title')
  const draftPath = '/v1/blueprints/' + blueprint.artifact.id + '/draft'
  await title.fill('Order conflict')
  await expect.poll(() => state.requests.filter((item) => (
    item.method === 'PATCH' && item.path === draftPath
  )).length).toBe(1)
  await expect(page.getByRole('alert').filter({ hasText: /artifact draft changed/i })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Reload server draft' })).toBeVisible()

  const firstSave = state.requests.find((item) => item.method === 'PATCH' && item.path === draftPath)
  expect(firstSave?.headers['if-match']).toBe(blueprint.artifact.etag)
  await title.fill('Order conflict kept locally')
  await expect(title).toHaveValue('Order conflict kept locally')
  await page.waitForTimeout(1_200)

  expect(state.requests.filter((item) => item.method === 'PATCH' && item.path === draftPath)).toHaveLength(1)
  await expect(page.getByRole('alert').filter({ hasText: /artifact draft changed/i })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Reload server draft' })).toBeVisible()
  await expect(title).toHaveValue('Order conflict kept locally')
})

test('applied Blueprint Proposal submits its exact immutable revision to the waiting Human Edit node', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    blueprintProposal: true,
    blueprintHumanEditRun: true,
    pauseBlueprintNodeResume: true,
  })
  const resumeGate = state.blueprintNodeResumeGate
  expect(resumeGate).not.toBeNull()
  const currentFlowLocation = () => {
    const url = new URL(page.url())
    return {
      surface: url.pathname.startsWith('/workbench/') ? 'workbench' : 'team',
      runId: url.searchParams.get('runId'),
      workbenchNodeKey: url.searchParams.get('workbenchNodeKey'),
    }
  }
  await page.goto(
    '/team/acme/project/' + project.id
      + '/blueprint?runId=run-blueprint-human-edit&workbenchNodeKey=workbench-blueprint',
  )
  await expect.poll(currentFlowLocation).toEqual({
    surface: 'team',
    runId: 'run-blueprint-human-edit',
    workbenchNodeKey: 'workbench-blueprint',
  })

  await page.getByRole('button', { name: 'Proposals 1' }).click()
  await page.getByRole('button', { name: 'Apply accepted operations' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/output-proposals/${state.blueprintProposal?.id ?? 'blueprint-proposal-1'}/apply`,
  )).toBe(true)

  const proposalNextStep = page.getByRole('status').filter({ hasText: /Proposal/i })
  await expect(proposalNextStep).toBeVisible()
  await expect(proposalNextStep).toContainText(/applied/i)
  await expect(proposalNextStep).toContainText(/immutable revision/i)
  const createRevision = proposalNextStep.getByRole('button', {
    name: 'Create immutable revision',
    exact: true,
  })
  await expect(createRevision).toBeEnabled()
  await createRevision.click()

  await expect.poll(() => state.blueprintCreatedRevision?.id).toBe('cccccccc-cccc-4ccc-8ccc-ccccccccccc3')
  expect(state.blueprintCreatedRevision).toMatchObject({
    proposalId: 'blueprint-proposal-1',
    sourceManifestId: 'blueprint-manifest-1',
  })
  const applyIndex = state.requests.findIndex((item) =>
    item.method === 'POST' && item.path === '/v1/output-proposals/blueprint-proposal-1/apply')
  const revisionIndex = state.requests.findIndex((item) =>
    item.method === 'POST' && item.path === `/v1/blueprints/${blueprint.artifact.id}/revisions`)
  expect(revisionIndex).toBeGreaterThan(applyIndex)
  expect(state.requests[revisionIndex]?.headers['if-match']).toBe(state.blueprint.draft?.etag)

  const revisionNextStep = page.getByRole('status').filter({ hasText: /return to Workbench/i })
  await expect(revisionNextStep).toBeVisible()
  await expect(revisionNextStep).toContainText(/return to Workbench/i)
  await expect(revisionNextStep).toContainText(/submit/i)
  const submitRevision = revisionNextStep.getByRole('button', {
    name: 'Return to Workbench and submit pinned revision',
    exact: true,
  })
  await expect(submitRevision).toBeEnabled()
  await submitRevision.click()

  const resumePath = '/v1/projects/' + project.id
    + '/workflow-runs/run-blueprint-human-edit/resume'
  await expect.poll(() => state.requests.some((item) => (
    item.method === 'POST' && item.path === resumePath
  ))).toBe(true)
  const completion = state.requests.find((item) => (
    item.method === 'POST' && item.path === resumePath
  ))
  const createdRevision = state.blueprintCreatedRevision
  if (!createdRevision) throw new Error('Expected the Blueprint revision to be created.')
  expect(completion?.body).toEqual({
    nodeKey: 'blueprint-edit',
    output: {
      artifactRevision: {
        artifactId: createdRevision.artifactId,
        revisionId: createdRevision.id,
        contentHash: createdRevision.contentHash,
      },
    },
  })
  await expect.poll(currentFlowLocation).toEqual({
    surface: 'team',
    runId: 'run-blueprint-human-edit',
    workbenchNodeKey: 'workbench-blueprint',
  })
  await expect(submitRevision).toBeDisabled()
  expect(state.requests.some((item) => (
    item.method === 'POST' && item.path === '/v1/projects/' + project.id + '/reviews'
  ))).toBe(false)

  resumeGate!.release()
  await expect.poll(currentFlowLocation).toEqual({
    surface: 'workbench',
    runId: 'run-blueprint-human-edit',
    workbenchNodeKey: 'workbench-blueprint',
  })
})

test('Blueprint Composer inserts module packs, multi-selects them, and persists a capability group', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/blueprint`)

  await page.getByRole('button', { name: /feature\s+Order management/i }).click()
  await page.getByRole('button', { name: /page\s+Dashboard/i }).click({ modifiers: ['Meta'] })
  await page.getByRole('button', { name: 'Group 2 nodes' }).click()
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
  await page.getByLabel('Name for capability “Capability 1”').fill('Checkout capability')
  await page.getByRole('button', { name: 'Select capability “Checkout capability”', exact: true }).click()
  await page.getByRole('button', { name: 'Generate documents for selection' }).click()
  await expect(page.getByText(/Created a document proposal for 2 selected nodes/)).toBeVisible()
  const compile = state.requests.findLast((item) => item.path.endsWith('/blueprint-selections/compile'))
  expect((compile?.body as { nodeIds?: string[] })?.nodeIds).toEqual([...groupedNodeIds].sort())
})

test('Generate docs from selection creates an AI proposal without prototype or workflow side effects', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/acme/project/${project.id}/blueprint`)

  await page.getByRole('button', { name: 'Generate documents for selection' }).click()
  await expect(page.getByText(/Created a document proposal for 1 selected nodes/)).toBeVisible()
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
  await page.getByRole('button', { name: 'Create prototypes for selection' }).click()
  await expect(page.getByText(/Created 1 prototypes/)).toBeVisible()
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
  await page.getByRole('button', { name: 'Use selection in Workbench' }).click()
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
  readonly desiredOutputCapability: 'application' | string
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
  readonly workbenchTargetHint?: {
    readonly runId: string
    readonly rootBundleId: string
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
    summaryCheckpointHeadId: undefined as string | undefined,
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

function conversationSummaryCheckpoint(
  id: string,
  conversationId: string,
  throughMessage: ReturnType<typeof conversationMessage>,
  summary: string,
  options: {
    readonly createdBy?: string
    readonly previousCheckpointId?: string
    readonly status?: 'pending_review' | 'approved' | 'rejected' | 'superseded'
  } = {},
) {
  const status = options.status ?? 'pending_review'
  const version = status === 'pending_review' ? 1 : 2
  return {
    id,
    projectId: project.id,
    conversationId,
    previousCheckpointId: options.previousCheckpointId,
    throughMessageId: throughMessage.id,
    throughSequence: throughMessage.sequence,
    messageCount: throughMessage.sequence,
    contentBytes: throughMessage.content.length,
    prefixHash: hash('e'),
    hashAlgorithm: 'conversation-prefix-chain/v1' as const,
    summary,
    summaryHash: hash('f'),
    status,
    version,
    etag: `"conversation-summary-checkpoint:${id}:${version}"`,
    createdBy: options.createdBy ?? user.id,
    createdAt: now,
    reviewedBy: undefined as string | undefined,
    reviewedAt: undefined as string | undefined,
    reviewReason: undefined as string | undefined,
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
    suggestedDefinitionVersionId: workflowDefinition.versionId,
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
    desiredOutputCapability: 'application',
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
    sliceId: 'page-orders',
    sliceKey: 'ORDERS',
    sliceTitle: 'Orders',
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
      desiredOutputCapability: 'application' as const,
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
  const nodes = workflowDefinition.definition.nodes.map((node) => node.type !== 'artifact_input'
    ? node
    : {
        ...node,
        artifactInput: {
          ...node.artifactInput,
          requireApproved: options.workflowRequireApproved ?? node.artifactInput?.requireApproved ?? true,
        },
      })
  return {
    ...workflowDefinition,
    definition: {
      ...workflowDefinition.definition,
      inputContract: {
        ...workflowDefinition.definition.inputContract,
        requireApproved: options.workflowRequireApproved ?? workflowDefinition.definition.inputContract.requireApproved,
      },
      nodes: options.blueprintHumanEditRun
        ? [...nodes, {
            id: 'blueprint-edit',
            name: 'Edit generated Blueprint',
            type: 'human_edit' as const,
            humanEdit: {
              artifactType: 'blueprint' as const,
              artifactKind: 'blueprint',
              requiredRole: 'editor',
              instructions: 'Apply the linked Proposal and submit its exact immutable revision.',
            },
          }, {
            id: 'workbench-blueprint',
            name: 'Build from submitted Blueprint',
            type: 'workbench_build' as const,
            workbenchBuild: {
              buildManifestSchemaVersion: 1,
              maxAttempts: 3,
              timeout: 60,
            },
          }]
        : nodes,
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
}, createdBy = user.id) {
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
    createdBy,
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
    pipelineStage: 'proposal_ready' as const,
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
    createdBy,
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

function workflowRun(
  id: string,
  status: 'waiting_input' | 'waiting_review' | 'running' | 'failed',
  governanceMode: 'solo' | 'team' = 'team',
  reviewRevision: {
    readonly id: string
    readonly artifactId: string
    readonly revisionNumber: number
    readonly contentHash: string
  } = briefRevision,
) {
  const waitingReview = status === 'waiting_review'
  const failed = status === 'failed'
  const reviewRef = exactRevision(
    reviewRevision.artifactId,
    reviewRevision.id,
    reviewRevision.revisionNumber,
    reviewRevision.contentHash,
  )
  return {
    id,
    projectId: project.id,
    definitionVersionId: workflowDefinition.versionId,
    definition: { id: workflowDefinition.id, version: 2, hash: workflowDefinition.contentHash },
    inputManifest: { id: 'manifest-1', hash: hash('1') },
    governanceMode,
    status,
    scope: {},
    context: {
      values: {},
      nodes: waitingReview
        ? {
            'solo-review': {
              definitionNodeId: 'solo-review',
              maxAttempts: 1,
              timeoutNanos: 60_000_000_000,
              input: {
                hash: hash('9'),
                bindings: [{
                  source: {
                    nodeKey: 'brief-input',
                    definitionNodeId: 'brief-input',
                    outputRevisionId: reviewRef.revisionId,
                    artifactRevisions: [reviewRef],
                    materializedArtifactRevisions: [reviewRef],
                  },
                }],
              },
            },
          }
        : {},
      slices: {},
    },
    eventCursor: 1,
    startedBy: user.id,
    createdAt: now,
    updatedAt: now,
    nodes: [{
      id: waitingReview ? `${id}-review` : `${id}-input`,
      runId: id,
      key: waitingReview ? 'solo-review' : 'brief-input',
      definitionNodeId: waitingReview ? 'solo-review' : 'brief-input',
      type: waitingReview ? 'review_gate' : 'artifact_input',
      status: waitingReview ? 'waiting_review' : failed ? 'failed' : 'waiting_input',
      attempt: waitingReview || failed ? 1 : 0,
      ...(failed ? { failure: { code: 'baseline.invalid_requirement_fact' } } : {}),
      availableAt: now,
      createdAt: now,
      updatedAt: now,
    }],
  }
}

function blueprintHumanEditWorkflowRun() {
  const proposal = blueprintArtifactProposal()
  const proposalPin = {
    proposal: { id: proposal.id, payloadHash: proposal.payloadHash },
    manifest: proposal.manifest,
    producerNodeKey: 'blueprint-generate',
    producerDefinitionNodeId: 'blueprint-generate',
  }
  const workbenchInput = {
    bundleIds: ['build-1'],
    sliceIds: ['page-orders'],
    manifestGroupKey: 'run-blueprint-human-edit-workbench',
    hash: hash('w'),
  }
  return {
    id: 'run-blueprint-human-edit',
    projectId: project.id,
    definitionVersionId: workflowDefinition.versionId,
    definition: {
      id: workflowDefinition.id,
      version: workflowDefinition.version,
      hash: workflowDefinition.contentHash,
      executionProfile: workflowExecutionProfile,
    },
    executionProfile: workflowExecutionProfile,
    inputManifest: { id: 'manifest-1', hash: hash('1') },
    governanceMode: 'team' as const,
    status: 'waiting_input' as const,
    scope: {},
    context: {
      values: { buildManifest: workbenchInput },
      nodes: {
        'blueprint-edit': {
          definitionNodeId: 'blueprint-edit',
          maxAttempts: 3,
          timeoutNanos: 60_000_000_000,
          input: {
            hash: hash('i'),
            bindings: [{
              source: {
                nodeKey: 'blueprint-generate',
                definitionNodeId: 'blueprint-generate',
                artifactRevisions: [proposal.baseRevision],
                materializedArtifactRevisions: [],
                deliverySliceRefs: [],
                proposalPins: [proposalPin],
              },
            }],
          },
        },
        'workbench-blueprint': {
          definitionNodeId: 'workbench-blueprint',
          maxAttempts: 3,
          timeoutNanos: 60_000_000_000,
          input: { bindings: [{ value: workbenchInput, output: workbenchInput }] },
          output: { implementationProposals: [] },
        },
      },
      slices: {},
    },
    eventCursor: 2,
    startedBy: user.id,
    createdAt: now,
    updatedAt: now,
    nodes: [{
      id: 'run-blueprint-human-edit-node',
      runId: 'run-blueprint-human-edit',
      key: 'blueprint-edit',
      definitionNodeId: 'blueprint-edit',
      type: 'human_edit' as const,
      status: 'waiting_input' as const,
      attempt: 1,
      availableAt: now,
      createdAt: now,
      updatedAt: now,
    }, {
      id: 'run-blueprint-workbench-node',
      runId: 'run-blueprint-human-edit',
      key: 'workbench-blueprint',
      definitionNodeId: 'workbench-blueprint',
      type: 'workbench_build' as const,
      status: 'pending' as const,
      attempt: 0,
      availableAt: now,
      createdAt: now,
      updatedAt: now,
    }],
  }
}

function selectionWorkflowRun(id: string, rootBundleId = 'build-1') {
  const applicationBuild = {
    bundleIds: [rootBundleId],
    sliceIds: ['page-orders'],
    manifestGroupKey: `${id}-manifest-group`,
    hash: hash('6'),
  }
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
    context: {
      values: { buildManifest: applicationBuild },
      nodes: {
        selection: { definitionNodeId: 'selection' },
        workbench: {
          definitionNodeId: 'workbench',
          input: { bindings: [{ value: applicationBuild, output: applicationBuild }] },
          output: { implementationProposals: [] },
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
        id: `${id}-selection`, runId: id, key: 'selection', definitionNodeId: 'selection',
        type: 'artifact_input', status: 'completed', attempt: 1,
        availableAt: now, createdAt: now, updatedAt: now, completedAt: now,
      },
      {
        id: `${id}-workbench`, runId: id, key: 'workbench', definitionNodeId: 'workbench',
        type: 'workbench_build', status: 'waiting_input', attempt: 1,
        availableAt: now, createdAt: now, updatedAt: now,
      },
    ],
  }
}

function buildManifest(workflowRunId?: string, rootBuildManifestId?: string) {
  const anchoredBlueprint = { ...blueprintRevision, anchorId: 'page-orders' }
  return {
    id: 'build-1',
    projectId: project.id,
    ...(workflowRunId ? {
      workflowRunId,
      manifestGroupKey: `${workflowRunId}-manifest-group`,
      deliverySliceId: 'page-orders',
    } : {}),
    ...(rootBuildManifestId ? { rootBuildManifestId } : {}),
    pageSpecRevision: exactRevision(pageSpecRevision.artifactId, pageSpecRevision.id, 2, pageSpecRevision.contentHash),
    prototypeRevision: exactRevision(approvedPrototypeRevision.artifactId, approvedPrototypeRevision.id, 2, approvedPrototypeRevision.contentHash),
    requirementRevisions: [exactRevision(briefRevision.artifactId, briefRevision.id, 3, briefRevision.contentHash)],
    blueprintRevision,
    contractRevisions: [],
    designSystemRevisions: [],
    contextRevisions: [],
    ...(workflowRunId ? {
      workflowContext: {
        definition: { id: workflowDefinition.id, version: 3, hash: workflowDefinition.contentHash },
        inputManifest: {
          id: 'selection-manifest-page-orders',
          projectId: project.id,
          jobType: 'blueprint.selection',
          deliverySliceId: 'selection-orders',
          sources: [
            { ref: blueprintRevision, purpose: 'blueprint_selection_root' },
            { ref: anchoredBlueprint, purpose: 'blueprint_selection_node' },
          ],
          constraints: { blueprintSelection: { selectionId: 'selection-orders', nodeIds: ['page-orders'] } },
          outputSchemaVersion: 'blueprint-selection/v1',
          createdBy: user.id,
          createdAt: now,
          hash: hash('8'),
        },
        deliverySliceId: 'page-orders',
        runScope: {
          conversationIntent: {
            kind: 'start_workflow',
            proposalId: 'reviewed-start-proposal',
          },
        },
        outputContract: {
          capability: 'application',
          producedArtifactKinds: ['workspace'],
          terminalOutcome: 'deployment',
          terminalNodeType: 'publish',
        },
      },
    } : {}),
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
    executionSource: 'manual_submission' as 'manual_submission' | 'manual_generation' | 'workflow_runner' | 'conversation_command',
    conversationCommandId: undefined as string | undefined,
    instructionHash: undefined as string | undefined,
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

function blueprintArtifactProposal(
  status: 'ready' | 'applied' = 'ready',
  decision: 'accepted' | 'applied' = status === 'applied' ? 'applied' : 'accepted',
) {
  return {
    id: 'blueprint-proposal-1',
    projectId: project.id,
    artifactId: blueprint.artifact.id,
    manifest: { id: 'blueprint-manifest-1', hash: hash('f') },
    baseRevision: blueprintRevision,
    payloadHash: hash('g'),
    status,
    operations: [{
      id: 'blueprint-operation-feature-title',
      kind: 'replace' as const,
      path: '/semantic/nodes/0/title',
      value: 'AI-reviewed order management',
      rationale: 'Clarify the primary capability title.',
      decision,
    }],
    assumptions: [],
    questions: [],
    version: status === 'applied' ? 2 : 1,
    createdBy: user.id,
    createdAt: now,
    appliedAt: status === 'applied' ? now : undefined,
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
    manifestGroupKey: 'manifest-group-a',
  }
  const bundleB = {
    ...multiBuildManifest('bundle-group-b', 'slice-group-b'),
    workflowRunId: 'run-groups',
    manifestGroupKey: 'manifest-group-b',
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
  const base = buildManifest('run-multi')
  return {
    ...base,
    id,
    deliverySliceId,
    workflowContext: base.workflowContext
      ? { ...base.workflowContext, deliverySliceId }
      : undefined,
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
