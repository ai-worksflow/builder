import { expect, test, type Page, type Route } from '@playwright/test'
import { createHash } from 'node:crypto'
import type { PrototypeContentDto } from '../lib/platform/dto'
import {
  REPOSITORY_SNAPSHOT_RECEIPT_SCHEMA_VERSION,
  REPOSITORY_SNAPSHOT_RECEIPT_SUBJECT_SCHEMA_VERSION,
  REPOSITORY_SNAPSHOT_TREE_COMMITMENT_SCHEMA_VERSION,
  computeRepositorySnapshotContentHash,
  type RepositorySnapshotDto,
} from '../lib/platform/repository-contract'

const now = '2026-07-10T08:00:00Z'
const hash = (character: string) => character.repeat(64)
const contentHash = (value: string) => `sha256:${createHash('sha256').update(value).digest('hex')}`
const fixtureUUIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

function fixtureId(value: string) {
  if (fixtureUUIDPattern.test(value)) return value
  let state = 0x811c9dc5
  let hexadecimal = ''
  for (let index = 0; index < 32; index += 1) {
    const code = value.charCodeAt(index % value.length)
    state = Math.imul(state ^ code ^ index, 0x01000193) >>> 0
    hexadecimal += (state & 0xf).toString(16)
  }
  hexadecimal = `${hexadecimal.slice(0, 12)}4${hexadecimal.slice(13, 16)}8${hexadecimal.slice(17)}`
  return `${hexadecimal.slice(0, 8)}-${hexadecimal.slice(8, 12)}-${hexadecimal.slice(12, 16)}-${hexadecimal.slice(16, 20)}-${hexadecimal.slice(20)}`
}

const primaryBuildManifestId = fixtureId('build-1')
const primaryProposalId = fixtureId('implementation-1')
const workflowExecutionProfile = { version: 'workflow-engine/v2', hash: 'dd247a77ce3cfa1095a575a238b93c4bd41dd991eac07e8b62ec170864470da1' }

const fullStackTemplateComponents = [
  {
    role: 'web',
    mountPath: 'frontend',
    release: { id: fixtureId('template-release-web-e2e'), contentHash: hash('d'), subjectHash: hash('e') },
  },
  {
    role: 'api',
    mountPath: 'backend',
    release: { id: fixtureId('template-release-api-e2e'), contentHash: hash('7'), subjectHash: hash('8') },
  },
]

const fullStackTemplateRegistration = {
  template: {
    id: fixtureId('full-stack-template-e2e'),
    schemaVersion: 'full-stack-template/v1',
    templateId: 'react-fastapi-postgres',
    version: '1.0.0',
    components: fullStackTemplateComponents,
    layout: {
      contractTruthSource: 'openapi',
      openapiPath: 'backend/openapi.yaml',
      generatedClientPath: 'frontend/lib/generated',
      deploymentPath: 'deploy',
      testPath: 'tests',
      databaseEngine: 'postgres',
    },
    contentHash: hash('f'),
    createdBy: '11111111-1111-4111-8111-111111111111',
    createdAt: now,
  },
  components: fullStackTemplateComponents,
}

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

function readyApplicationBuildContract(buildManifestId: string) {
  return {
    id: fixtureId(`build-contract-${buildManifestId}`),
    projectId: project.id,
    buildManifestId,
    status: 'ready',
    version: 1,
    etag: `"application-build-contract:${buildManifestId}:1"`,
    contentHash: hash('a'),
    contractHash: hash('b'),
    contract: {
      schemaVersion: 'application-build-contract/v2',
      compiler: { version: 'e2e-v1', hash: hash('c') },
      projectId: project.id,
      deliverySliceId: 'page-orders',
      buildManifest: { id: buildManifestId, contentHash: hash('6') },
      sourceRevisions: [],
      fullStackTemplate: {
        id: fullStackTemplateRegistration.template.id,
        contentHash: fullStackTemplateRegistration.template.contentHash,
        certification: 'qualified',
        policyStatus: 'approved',
      },
      templateReleaseRefs: [],
      routes: [],
      states: [],
      contractBindings: [],
      acceptanceCriteria: [],
      oracles: [],
      obligations: [{
        id: 'must-build-exact-manifest',
        level: 'must',
        kind: 'build_manifest',
        sourceRevision: {},
        sourceAnchorId: buildManifestId,
        oracleIds: [],
        dependsOn: [],
        waivable: false,
        status: 'ready',
      }],
      waivers: [],
      gaps: [],
      conflicts: [],
      forbiddenClaims: [],
      status: 'ready',
    },
    mustCount: 1,
    mustReadyCount: 1,
    blockingCount: 0,
    conflictCount: 0,
    createdBy: user.id,
    createdAt: now,
  }
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
    contentHash: approvedPrototypeRevision.contentHash,
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
  readonly pageSpecHumanEditRun?: boolean
  readonly secondBlueprint?: boolean
  readonly pauseFirstBlueprintDraftSave?: boolean
  readonly pauseBlueprintProposalApply?: boolean
  readonly pauseBlueprintNodeResume?: boolean
  readonly rejectFirstBlueprintDraftSave?: boolean
  readonly prototypeProposal?: boolean
  readonly pauseFirstPrototypeDraftSave?: boolean
  readonly pausePrototypeCreate?: boolean
  readonly pausePrototypeProposalApply?: boolean
  readonly pausePrototypeRevisionCreate?: boolean
  readonly prototypeReviewFirstFailure?: 'before-create' | 'after-create'
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

interface MockReview {
  id: string
  projectId: string
  artifactId: string
  revisionId: string
  contentHash: string
  status: 'open'
  policy: {
    reviewerIds: string[]
    minimumApprovals: number
    prohibitSelfReview: boolean
    governanceMode: 'solo' | 'team'
  }
  requestedBy: string
  requestedAt: string
  decisions: []
  etag: string
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
  run: ReturnType<typeof workflowRun> | ReturnType<typeof blueprintHumanEditWorkflowRun> | ReturnType<typeof pageSpecHumanEditWorkflowRun> | ReturnType<typeof selectionWorkflowRun> | ReturnType<typeof multiBundleWorkflowRun> | ReturnType<typeof multiGroupWorkflowRun> | null
  proposal: ReturnType<typeof implementationProposal> | null
  workspaceRevision: ReturnType<typeof applicationRevision> | null
  workbenchBundle: ReturnType<typeof buildManifest>
  multiWorkbench: ReturnType<typeof multiWorkbenchState> | null
  sandboxCandidate: ReturnType<typeof freshCandidateWorkspace> | null
  sandboxSession: ReturnType<typeof readySandboxSession> | null
  blueprintProposal: ReturnType<typeof blueprintArtifactProposal> | null
  pageSpecProposals: Array<ReturnType<typeof pageSpecArtifactProposal>>
  blueprintCreatedRevision: MockBlueprintRevision | null
  blueprintDraftSaveGate: Deferred | null
  blueprintListGate: Deferred | null
  blueprintProposalApplyGate: Deferred | null
  blueprintNodeResumeGate: Deferred | null
  rejectNextBlueprintDraftSave: boolean
  prototypeProposal: ReturnType<typeof artifactProposal> | null
  prototypeCreatedRevision: Record<string, unknown> | null
  prototypeDraftSaveGate: Deferred | null
  prototypeCreateGate: Deferred | null
  prototypeProposalApplyGate: Deferred | null
  prototypeRevisionCreateGate: Deferred | null
  prototypeReviews: MockReview[]
  prototypeReviewRequestAttempts: number
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
    run: options.pageSpecHumanEditRun
      ? pageSpecHumanEditWorkflowRun()
      : options.blueprintHumanEditRun
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
    sandboxCandidate: null,
    sandboxSession: null,
    blueprintProposal: options.blueprintProposal ? blueprintArtifactProposal() : null,
    pageSpecProposals: [],
    blueprintCreatedRevision: null,
    blueprintDraftSaveGate: options.pauseFirstBlueprintDraftSave ? deferred() : null,
    blueprintListGate: null,
    blueprintProposalApplyGate: options.pauseBlueprintProposalApply ? deferred() : null,
    blueprintNodeResumeGate: options.pauseBlueprintNodeResume ? deferred() : null,
    rejectNextBlueprintDraftSave: options.rejectFirstBlueprintDraftSave ?? false,
    prototypeProposal: options.prototypeProposal ? artifactProposal() : null,
    prototypeCreatedRevision: null,
    prototypeDraftSaveGate: options.pauseFirstPrototypeDraftSave ? deferred() : null,
    prototypeCreateGate: options.pausePrototypeCreate ? deferred() : null,
    prototypeProposalApplyGate: options.pausePrototypeProposalApply ? deferred() : null,
    prototypeRevisionCreateGate: options.pausePrototypeRevisionCreate ? deferred() : null,
    prototypeReviews: [],
    prototypeReviewRequestAttempts: 0,
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
  let body: unknown
  try {
    body = request.postDataJSON() ?? undefined
  } catch {
    body = request.postData() ?? undefined
  }
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
            expectedBundleId: options.conversationSelectedWorkbenchTarget
              ? fixtureId('build-root-1')
              : primaryBuildManifestId,
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
      const input = body as {
        target?: { artifactId?: string; revisionId?: string; contentHash?: string }
        summary?: string
        requiredReviewerIds?: string[]
      }
      const target = input.target
      const isPrototypeReview = Boolean(target?.artifactId && state.prototypes.some((item) =>
        (item as ReturnType<typeof draftPrototype>).artifact.id === target.artifactId))
      if (isPrototypeReview) state.prototypeReviewRequestAttempts += 1
      const failFirst = isPrototypeReview
        && state.prototypeReviewRequestAttempts === 1
        ? options.prototypeReviewFirstFailure
        : undefined
      if (failFirst === 'before-create') {
        await respond({
          title: 'Review request failed',
          status: 503,
          detail: 'The review was not created.',
        }, 503)
        return
      }
      const review: MockReview = {
        id: `review-${state.prototypeReviews.length + 1}`,
        projectId: project.id,
        artifactId: target?.artifactId ?? '',
        revisionId: target?.revisionId ?? '',
        contentHash: target?.contentHash ?? '',
        status: 'open',
        policy: {
          reviewerIds: input.requiredReviewerIds ?? [],
          minimumApprovals: 1,
          prohibitSelfReview: true,
          governanceMode: state.governanceMode,
        },
        requestedBy: user.id,
        requestedAt: now,
        decisions: [],
        etag: `"review:${state.prototypeReviews.length + 1}"`,
      }
      state.prototypeReviews.push(review)
      if (failFirst === 'after-create') {
        await respond({
          title: 'Snapshot refresh failed',
          status: 503,
          detail: 'The review was created but the client did not receive confirmation.',
        }, 503)
        return
      }
      await respond(review, 201)
    } else if (method === 'POST' && path.endsWith('/presence')) {
      await respond({ projectId: project.id, user, state: 'active', updatedAt: now })
    } else if (method === 'GET' && path.endsWith('/reviews')) {
      const artifactId = url.searchParams.get('artifactId')
      await respond({
        items: artifactId
          ? state.prototypeReviews.filter((review) => review.artifactId === artifactId)
          : state.prototypeReviews,
      })
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
  if (path === `/v1/drafts/${state.pageSpec.draft?.id}` && method === 'PATCH') {
    const input = body as {
      content: typeof pageSpecRevision.content
      sourceVersions?: Array<{
        version: typeof blueprintRevision
        purpose: string
        required: boolean
      }>
    }
    const currentDraft = state.pageSpec.draft!
    const nextDraftRevision = currentDraft.revision + 1
    const draft = {
      ...currentDraft,
      revision: nextDraftRevision,
      content: input.content,
      sourceVersions: input.sourceVersions?.map((source) => source.version) ?? currentDraft.sourceVersions,
      contentHash: state.pageSpec.latestRevision.contentHash as string,
      updatedAt: now,
      etag: `"page-spec-draft:${nextDraftRevision}:restored"`,
    }
    state.pageSpec = { ...state.pageSpec, draft }
    await respond(draft, 200, { etag: draft.etag })
    return
  }
  if (path === `/v1/page-specs/${pageSpec.artifact.id}/revisions` && method === 'POST') {
    const appliedProposal = state.pageSpecProposals.find((proposal) => proposal.status === 'applied')
    const revision = {
      id: 'dddddddd-dddd-4ddd-8ddd-ddddddddddd4',
      artifactId: pageSpec.artifact.id,
      revisionNumber: 4,
      contentHash: state.pageSpec.draft!.contentHash,
      status: 'draft',
      content: state.pageSpec.draft!.content,
      sourceVersions: state.pageSpec.draft!.sourceVersions,
      ...(appliedProposal ? {
        proposalId: appliedProposal.id,
        sourceManifestId: appliedProposal.manifest.id,
      } : {}),
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
    await respond({ items: [state.blueprintProposal, ...state.pageSpecProposals, state.prototypeProposal].filter(Boolean) })
    return
  }
  const artifactProposalItem = path.match(/^\/v1\/output-proposals\/([^/]+)$/)
  const pageSpecProposal = state.pageSpecProposals.find((proposal) =>
    proposal.id === artifactProposalItem?.[1])
  if (pageSpecProposal && method === 'GET') {
    await respond(pageSpecProposal)
    return
  }
  const blueprintProposal = state.blueprintProposal
  if (blueprintProposal && artifactProposalItem?.[1] === blueprintProposal.id && method === 'GET') {
    await respond(blueprintProposal)
    return
  }
  const artifactProposalApply = path.match(/^\/v1\/output-proposals\/([^/]+)\/apply$/)
  const appliedPageSpecProposal = state.pageSpecProposals.find((proposal) =>
    proposal.id === artifactProposalApply?.[1])
  if (appliedPageSpecProposal && method === 'POST') {
    const baseContent = structuredClone(
      state.pageSpec.latestRevision.content as typeof pageSpecRevision.content,
    )
    for (const operation of appliedPageSpecProposal.operations) {
      if (operation.decision !== 'accepted' && operation.decision !== 'applied') continue
      if (operation.path === '/userGoal' && typeof operation.value === 'string') {
        baseContent.userGoal = operation.value
      }
      if (operation.path === '/acceptanceCriterionIds' && Array.isArray(operation.value)) {
        baseContent.acceptanceCriterionIds = operation.value
      }
    }
    const currentDraft = state.pageSpec.draft!
    const nextDraftRevision = currentDraft.revision + 1
    const draft = {
      ...currentDraft,
      revision: nextDraftRevision,
      content: baseContent,
      contentHash: hash('q'),
      updatedAt: now,
      etag: `"page-spec-draft:${nextDraftRevision}:proposal"`,
    }
    state.pageSpec = { ...state.pageSpec, draft }
    state.pageSpecProposals = state.pageSpecProposals.map((proposal) =>
      proposal.id !== appliedPageSpecProposal.id
        ? proposal
        : {
            ...proposal,
            status: 'applied' as const,
            operations: proposal.operations.map((operation) => ({
              ...operation,
              decision: operation.decision === 'accepted' ? 'applied' as const : operation.decision,
            })),
            version: proposal.version + 1,
            appliedAt: now,
          })
    await respond(draft, 200, { etag: draft.etag })
    return
  }
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
    const proposal = state.prototypeProposal
    const currentIndex = state.prototypes.findIndex((item) =>
      (item as typeof approvedPrototype).artifact.id === proposal.artifactId)
    const current = state.prototypes[currentIndex] as typeof approvedPrototype | undefined
    if (!current) {
      await respond({ title: 'Not found', status: 404 }, 404)
      return
    }
    const gate = state.prototypeProposalApplyGate
    state.prototypeProposalApplyGate = null
    if (gate) await gate.promise
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
    state.prototypes = state.prototypes.map((item, index) => index === currentIndex
      ? { ...current, draft }
      : item)
    state.prototypeProposal = {
      ...proposal,
      status: 'applied',
      operations: proposal.operations.map((operation) => ({
        ...operation,
        decision: operation.decision === 'accepted' ? 'applied' : operation.decision,
      })),
      version: proposal.version + 1,
      appliedAt: now,
    }
    await respond(draft)
    return
  }
  if (path === `/v1/projects/${project.id}/prototypes` && method === 'POST') {
    const gate = state.prototypeCreateGate
    state.prototypeCreateGate = null
    if (gate) await gate.promise
    const created = draftPrototype('prototype-created')
    state.prototypes = [created]
    await respond(created, 201, { etag: '"prototype-created:1"' })
    return
  }
  const prototypeDraftPath = path.match(/^\/v1\/prototypes\/([^/]+)\/draft$/)
  if (prototypeDraftPath && method === 'PATCH') {
    const input = body as { content: ReturnType<typeof prototypeContent> }
    const currentIndex = state.prototypes.findIndex((item) =>
      (item as ReturnType<typeof draftPrototype>).artifact.id === prototypeDraftPath[1])
    const current = currentIndex >= 0
      ? state.prototypes[currentIndex] as ReturnType<typeof draftPrototype>
      : draftPrototype(prototypeDraftPath[1])
    if (route.request().headers()['if-match'] !== current.draft.etag) {
      await respond({
        type: 'about:blank',
        title: 'Precondition Failed',
        status: 412,
        detail: 'The prototype draft ETag is stale.',
      }, 412)
      return
    }
    const gate = state.prototypeDraftSaveGate
    state.prototypeDraftSaveGate = null
    if (gate) await gate.promise
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
    state.prototypes = currentIndex >= 0
      ? state.prototypes.map((item, index) => index === currentIndex ? next : item)
      : [...state.prototypes, next]
    await respond(next, 200, { etag: next.draft.etag })
    return
  }
  const prototypeRevisionPath = path.match(/^\/v1\/prototypes\/([^/]+)\/revisions$/)
  if (prototypeRevisionPath && method === 'POST') {
    const currentIndex = state.prototypes.findIndex((item) =>
      (item as ReturnType<typeof draftPrototype>).artifact.id === prototypeRevisionPath[1])
    const current = state.prototypes[currentIndex] as (ReturnType<typeof draftPrototype> & {
      latestRevision?: { revisionNumber: number }
    }) | undefined
    if (!current) {
      await respond({ title: 'Not found', status: 404 }, 404)
      return
    }
    const gate = state.prototypeRevisionCreateGate
    state.prototypeRevisionCreateGate = null
    if (gate) await gate.promise
    const revisionNumber = (current.latestRevision?.revisionNumber ?? 0) + 1
    const revision = {
      id: `prototype-created-r${revisionNumber}`,
      artifactId: current.artifact.id,
      revisionNumber,
      contentHash: current.draft.contentHash,
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
    state.prototypes = state.prototypes.map((item, index) => index === currentIndex
      ? { ...current, latestRevision: attributedRevision }
      : item)
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
      eventCursor: current.eventCursor + 1,
      nodes: current.nodes.map((node) => node.key === 'blueprint-edit'
        ? {
            ...node,
            status: 'completed' as const,
            completedAt: now,
            allowedActions: [],
            blockingReasons: [],
          }
        : node),
    } as MockPlatformState['run']
    await respond(undefined, 204)
    return
  }
  if (/\/workflow-runs\/[^/]+\/approve$/.test(path) && method === 'POST') {
    if (state.run) {
      const input = body as { nodeKey: string }
      state.run = {
        ...state.run,
        status: 'running',
        eventCursor: state.run.eventCursor + 1,
        nodes: state.run.nodes.map((node) => node.key === input.nodeKey
          ? {
              ...node,
              status: 'completed',
              completedAt: now,
              allowedActions: [],
              blockingReasons: [],
            }
          : node),
      } as MockPlatformState['run']
    }
    await route.fulfill({ status: 204, headers: corsHeaders() })
    return
  }
  if (/\/workflow-runs\/[^/]+\/retry$/.test(path) && method === 'POST') {
    if (state.run) {
      const input = body as { nodeKey: string }
      state.run = {
        ...state.run,
        status: 'running',
        eventCursor: state.run.eventCursor + 1,
        nodes: state.run.nodes.map((node) => node.key === input.nodeKey
          ? {
              ...node,
              status: 'ready',
              attempt: 0,
              allowedActions: [],
              blockingReasons: [],
            }
          : node),
      } as MockPlatformState['run']
    }
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
  if (path === '/v1/full-stack-templates' && method === 'GET') {
    await respond({ items: [fullStackTemplateRegistration] })
    return
  }
  const fullStackTemplatePath = path.match(/^\/v1\/full-stack-templates\/([^/]+)$/)
  if (fullStackTemplatePath && method === 'GET') {
    const matches = fullStackTemplatePath[1] === fullStackTemplateRegistration.template.id
      && (!url.searchParams.get('contentHash')
        || url.searchParams.get('contentHash') === fullStackTemplateRegistration.template.contentHash)
    await respond(matches ? fullStackTemplateRegistration : { title: 'Not found' }, matches ? 200 : 404)
    return
  }
  const buildContractPath = path.match(/^\/v1\/build-manifests\/([^/]+)\/build-contract$/)
  if (buildContractPath && method === 'GET') {
    await respond(readyApplicationBuildContract(buildContractPath[1]))
    return
  }
  const buildContractCreatePath = path.match(/^\/v1\/build-manifests\/([^/]+)\/build-contracts$/)
  if (buildContractCreatePath && method === 'POST') {
    await respond(readyApplicationBuildContract(buildContractCreatePath[1]), 201)
    return
  }
  if (path === `/v1/projects/${project.id}/repository-candidates` && method === 'GET') {
    await respond({
      schemaVersion: 'repository-candidate-head-list/v1',
      candidates: state.sandboxCandidate ? [{ candidate: state.sandboxCandidate }] : [],
    })
    return
  }
  if (path === `/v1/projects/${project.id}/repository-candidates` && method === 'POST') {
    const input = body as { buildManifestId: string }
    const existing = state.sandboxCandidate?.buildManifest.id === input.buildManifestId
      ? state.sandboxCandidate
      : undefined
    state.sandboxCandidate = existing ?? freshCandidateWorkspace(input.buildManifestId)
    await respond({
      candidate: state.sandboxCandidate,
      repositorySnapshotReceipt: await repositorySnapshotReceiptFor(state.sandboxCandidate),
      created: !existing,
      recovered: Boolean(existing),
      finalizationPending: false,
    }, existing ? 200 : 201)
    return
  }
  const repositoryCandidatePath = path.match(
    new RegExp(`^/v1/projects/${project.id}/repository-candidates/([^/]+)$`),
  )
  if (repositoryCandidatePath && method === 'GET') {
    const candidate = state.sandboxCandidate
    await respond(
      candidate?.id === repositoryCandidatePath[1]
        ? candidate
        : { title: 'Not found', status: 404 },
      candidate?.id === repositoryCandidatePath[1] ? 200 : 404,
    )
    return
  }
  if (path === `/v1/projects/${project.id}/sandbox-sessions` && method === 'POST') {
    const input = body as { candidateId: string }
    const candidate = state.sandboxCandidate
    if (!candidate || candidate.id !== input.candidateId) {
      await respond({ title: 'Candidate not found', status: 404 }, 404)
      return
    }
    state.sandboxSession = readySandboxSession(candidate)
    await respond(
      state.sandboxSession,
      201,
      sandboxFenceHeaders(state.sandboxSession, candidate),
    )
    return
  }
  const sandboxWriterLeasePath = path.match(/^\/v1\/sandbox-sessions\/([^/]+)\/writer-lease$/)
  if (sandboxWriterLeasePath && method === 'POST') {
    const session = state.sandboxSession
    const candidate = state.sandboxCandidate
    if (!session || !candidate || session.id !== sandboxWriterLeasePath[1]) {
      await respond({ title: 'Sandbox not found', status: 404 }, 404)
      return
    }
    state.sandboxCandidate = {
      ...candidate,
      version: candidate.version + 1,
      writerLeaseEpoch: candidate.writerLeaseEpoch + 1,
      lease: {
        ownerId: user.id,
        epoch: candidate.writerLeaseEpoch + 1,
        expiresAt: '2026-07-10T08:15:00Z',
      },
      updatedAt: now,
    }
    state.sandboxSession = {
      ...session,
      version: session.version + 1,
      candidate: sandboxCandidateState(state.sandboxCandidate),
      updatedAt: now,
    }
    await respond(
      { session: state.sandboxSession, candidate: state.sandboxCandidate },
      200,
      sandboxFenceHeaders(state.sandboxSession, state.sandboxCandidate),
    )
    return
  }
  const sandboxTreePath = path.match(/^\/v1\/sandbox-sessions\/([^/]+)\/tree$/)
  if (sandboxTreePath && method === 'GET') {
    const session = state.sandboxSession
    const candidate = state.sandboxCandidate
    if (!session || !candidate || session.id !== sandboxTreePath[1]) {
      await respond({ title: 'Sandbox not found', status: 404 }, 404)
      return
    }
    await respond(
      { session, candidate, tree: candidate.currentTree },
      200,
      {
        ...sandboxFenceHeaders(session, candidate),
        'x-candidate-tree-etag': `"candidate-tree:${candidate.id}:${candidate.currentTree.treeHash}"`,
      },
    )
    return
  }
  const sandboxFilePath = path.match(/^\/v1\/sandbox-sessions\/([^/]+)\/files\/(.+)$/)
  if (sandboxFilePath && method === 'GET') {
    const session = state.sandboxSession
    const candidate = state.sandboxCandidate
    const requestedPath = sandboxFilePath[2].split('/').map(decodeURIComponent).join('/')
    const file = candidate?.currentTree.files.find((item) => item.path === requestedPath)
    if (!session || !candidate || session.id !== sandboxFilePath[1] || !file) {
      await respond({ title: 'File not found', status: 404 }, 404)
      return
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/octet-stream',
      headers: {
        ...corsHeaders(),
        ...sandboxFenceHeaders(session, candidate),
        'x-content-hash': file.contentHash,
        'x-file-mode': file.mode,
      },
      body: freshCandidateFile,
    })
    return
  }
  if (sandboxFilePath && method === 'PUT') {
    const session = state.sandboxSession
    const candidate = state.sandboxCandidate
    const requestedPath = sandboxFilePath[2].split('/').map(decodeURIComponent).join('/')
    const currentFile = candidate?.currentTree.files.find((item) => item.path === requestedPath)
    if (!session || !candidate || session.id !== sandboxFilePath[1] || !currentFile) {
      await respond({ title: 'File not found', status: 404 }, 404)
      return
    }
    const value = request.postData() ?? ''
    const byteSize = new TextEncoder().encode(value).byteLength
    const savedContentHash = contentHash(value)
    const nextTreeHash = `sha256:${hash('b')}`
    const nextVersion = candidate.version + 1
    const nextSequence = candidate.journalSequence + 1
    const nextFiles = candidate.currentTree.files.map((item) => item.path === requestedPath
      ? { ...item, contentHash: savedContentHash, byteSize }
      : item)
    const nextCandidate = {
      ...candidate,
      currentTree: { ...candidate.currentTree, treeHash: nextTreeHash, files: nextFiles },
      version: nextVersion,
      journalSequence: nextSequence,
      dirty: true,
      updatedAt: now,
    }
    const nextSession = {
      ...session,
      version: session.version + 1,
      candidate: sandboxCandidateState(nextCandidate),
      updatedAt: now,
    }
    state.sandboxCandidate = nextCandidate
    state.sandboxSession = nextSession
    const pointer = (treeHash: string, contentObjectHash: string) => ({
      store: 'content',
      ref: `tree-${treeHash.slice(-12)}`,
      ownerId: candidate.id,
      treeHash,
      fileCount: candidate.currentTree.files.length,
      byteSize: candidate.currentTree.files.reduce((total, item) => total + item.byteSize, 0),
      contentObjectHash,
    })
    await respond({
      session: nextSession,
      mutation: {
        recovered: false,
        finalizationPending: false,
        beforeTree: pointer(candidate.currentTree.treeHash, `sha256:${hash('d')}`),
        afterTree: {
          ...pointer(nextTreeHash, `sha256:${hash('e')}`),
          byteSize: nextFiles.reduce((total, item) => total + item.byteSize, 0),
        },
        entry: {
          candidateId: candidate.id,
          sequence: nextSequence,
          candidateVersionFrom: candidate.version,
          candidateVersionTo: nextVersion,
          sessionEpoch: candidate.sessionEpoch,
          leaseEpoch: candidate.writerLeaseEpoch,
          actorId: user.id,
          attribution: 'user',
          operation: {
            id: headers['idempotency-key'] ?? fixtureId('sandbox-autosave-operation'),
            kind: 'file.upsert',
            path: requestedPath,
            expectedHash: currentFile.contentHash,
            contentHash: savedContentHash,
            byteSize,
            mode: headers['x-file-mode'] ?? currentFile.mode,
          },
          beforeTreeHash: candidate.currentTree.treeHash,
          afterTreeHash: nextTreeHash,
          createdAt: now,
        },
      },
    }, 200, sandboxFenceHeaders(nextSession, nextCandidate))
    return
  }
  const sandboxProcessListPath = path.match(/^\/v1\/sandbox-sessions\/([^/]+)\/processes$/)
  if (sandboxProcessListPath && method === 'GET') {
    const session = state.sandboxSession
    if (!session || session.id !== sandboxProcessListPath[1]) {
      await respond({ title: 'Sandbox not found', status: 404 }, 404)
      return
    }
    await respond(
      { session, processes: [] },
      200,
      sandboxFenceHeaders(session, state.sandboxCandidate),
    )
    return
  }
  const sandboxTerminalListPath = path.match(/^\/v1\/sandbox-sessions\/([^/]+)\/ptys$/)
  if (sandboxTerminalListPath && method === 'GET') {
    const session = state.sandboxSession
    if (!session || session.id !== sandboxTerminalListPath[1]) {
      await respond({ title: 'Sandbox not found', status: 404 }, 404)
      return
    }
    await respond(
      { session, terminals: [] },
      200,
      sandboxFenceHeaders(session, state.sandboxCandidate),
    )
    return
  }
  const sandboxPortListPath = path.match(/^\/v1\/sandbox-sessions\/([^/]+)\/ports$/)
  if (sandboxPortListPath && method === 'GET') {
    const session = state.sandboxSession
    if (!session || session.id !== sandboxPortListPath[1]) {
      await respond({ title: 'Sandbox not found', status: 404 }, 404)
      return
    }
    await respond(
      { session, ports: [] },
      200,
      sandboxFenceHeaders(session, state.sandboxCandidate),
    )
    return
  }
  const verificationProfilePath = path.match(
    /^\/v1\/sandbox-sessions\/([^/]+)\/verification-profiles$/,
  )
  if (verificationProfilePath && method === 'GET') {
    await respond({ profiles: [] })
    return
  }
  const verificationRunListPath = path.match(
    /^\/v1\/sandbox-sessions\/([^/]+)\/verification-runs$/,
  )
  if (verificationRunListPath && method === 'GET') {
    await respond({ runs: [] })
    return
  }
  const sandboxSessionPath = path.match(/^\/v1\/sandbox-sessions\/([^/]+)$/)
  if (sandboxSessionPath && method === 'GET') {
    const session = state.sandboxSession
    await respond(
      session?.id === sandboxSessionPath[1]
        ? session
        : { title: 'Not found', status: 404 },
      session?.id === sandboxSessionPath[1] ? 200 : 404,
      session?.id === sandboxSessionPath[1]
        ? sandboxFenceHeaders(session, state.sandboxCandidate)
        : {},
    )
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
        ? { ...operation, decision: input.decision, decidedBy: user.id }
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
    const isHome = proposal.id === fixtureId('proposal-home')
    const workspace = multiApplicationRevision(isHome ? 1 : 2)
    const applied = {
      ...proposal,
      operations: proposal.operations.map((operation) => ({
        ...operation,
        decision: operation.decision === 'accepted' ? 'applied' as const : operation.decision,
      })),
      status: 'applied' as const,
      version: proposal.version + 1,
      appliedAt: now,
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
      eventCursor: multiBundleWorkflowRun().eventCursor + 1,
      nodes: multiBundleWorkflowRun().nodes.map((node) => node.type === 'workbench_build'
        ? { ...node, status: 'completed', allowedActions: [], blockingReasons: [] }
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
      eventCursor: current.eventCursor + 1,
      nodes: current.nodes.map((node) => node.key === input.nodeKey
        ? {
            ...node,
            status: 'completed' as const,
            allowedActions: [],
            blockingReasons: [],
          }
        : node),
    } as unknown as MockPlatformState['run']
    await respond(undefined, 204)
    return
  }
  if (path === `/v1/projects/${project.id}/build-manifests` && method === 'POST') {
    await respond(state.workbenchBundle, 201)
    return
  }
  if (path === `/v1/build-manifests/${primaryBuildManifestId}` && method === 'GET') {
    await respond(state.workbenchBundle)
    return
  }
  if (path === `/v1/implementation-proposals/${primaryProposalId}/decisions` && method === 'POST') {
    state.proposal = implementationProposal('ready', 'accepted', 2)
    await respond(state.proposal, 200, { etag: '"implementation-proposal:implementation-1:2"' })
    return
  }
  if (path === `/v1/implementation-proposals/${primaryProposalId}/apply` && method === 'POST') {
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
  if (path === `/v1/implementation-proposals/${primaryProposalId}` && method === 'GET') {
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
  await expect(page.getByRole('button', { name: 'Lock development input' })).toBeDisabled()
})

test('incomplete manual Proposal cannot expose review or Apply controls', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  state.proposal = {
    ...implementationProposal('open'),
    diagnostics: [{
      code: 'missing_contract',
      path: 'backend/openapi.yaml',
      severity: 'blocker',
      message: 'API contract is absent.',
    }],
    unimplementedItems: ['Persistence is not implemented.'],
  }

  await page.goto(`/workbench/planning?view=code&proposalId=${primaryProposalId}`)

  const blocker = page.getByRole('alert').filter({
    hasText: 'There are 2 required issue(s) left.',
  })
  await expect(blocker).toBeVisible()
  await expect(page.getByRole('button', { name: 'Accept pending' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Reject pending' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Discard and return to development' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Apply accepted operations' })).toBeDisabled()
})

test('pre-verification Candidate history can only be quarantined', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const candidateId = fixtureId('historical-candidate')
  const candidateSnapshotId = fixtureId('historical-checkpoint')
  const baseTreeHash = `sha256:${hash('1')}`
  const treeHash = `sha256:${hash('2')}`
  state.proposal = {
    ...implementationProposal('open'),
    executionSource: 'candidate_freeze',
    baseWorkspaceRevision: {
      artifactId: fixtureId('historical-workspace'),
      revisionId: fixtureId('historical-workspace-revision'),
      contentHash: `sha256:${hash('0')}`,
    },
    candidateSource: {
      freezeReceiptId: fixtureId('historical-freeze'),
      repositorySnapshotId: fixtureId('historical-repository-snapshot'),
      sessionId: fixtureId('historical-session'),
      candidateId,
      candidateSnapshotId,
      candidateVersion: 2,
      journalSequence: 3,
      sessionEpoch: 1,
      writerLeaseEpoch: 1,
      baseTreeHash,
      treeHash,
      fullStackTemplate: {
        id: fixtureId('historical-template'),
        contentHash: `sha256:${hash('3')}`,
      },
      verificationReceipt: { id: '', contentHash: '' },
    },
    operations: [{
      id: 'candidate-00001-aaaaaaaaaaaa',
      kind: 'file.upsert',
      path: 'src/index.html',
      content: '<!doctype html><html><body><h1>Historical Candidate</h1></body></html>',
      language: 'html',
      mode: '100644',
      rationale: `Freeze exact CandidateSnapshot ${candidateSnapshotId}`,
      traceSource: [`candidate-snapshot:${candidateSnapshotId}`],
      decision: 'pending',
    }],
    traceLinks: [{
      kind: 'candidate_snapshot',
      candidateId,
      candidateSnapshotId,
      baseTreeHash,
      treeHash,
    }],
  }

  await page.goto(`/workbench/planning?view=code&proposalId=${primaryProposalId}`)

  const blocker = page.getByRole('alert').filter({
    hasText: 'This historical change has not passed the current automated checks.',
  })
  await expect(blocker).toBeVisible()
  await expect(page.getByText('Automated checks passed')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Accept pending' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Reject pending' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Discard and return to development' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Apply accepted operations' })).toBeDisabled()
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

test('a mutation refresh supersedes an older in-flight Workflow snapshot', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    soloReviewRun: true,
  })
  await page.goto('/workbench/planning?view=preview')
  await page.getByRole('button', { name: /run-solo-review/ }).click()
  await expect(page.getByTestId('workflow-review-approve-solo-review')).toBeVisible()

  let delayNextLoad = true
  let staleLoadStarted = false
  let releaseStaleLoad: (() => void) | undefined
  const staleLoadGate = new Promise<void>((resolve) => {
    releaseStaleLoad = resolve
  })
  await page.route(
    `**/v1/projects/${project.id}/workflow-runs/run-solo-review`,
    async (route) => {
      if (route.request().method() !== 'GET') {
        await route.fallback()
        return
      }
      const snapshot = structuredClone(state.run)
      if (delayNextLoad) {
        delayNextLoad = false
        staleLoadStarted = true
        await staleLoadGate
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: corsHeaders(),
        body: JSON.stringify(snapshot),
      })
    },
  )

  await expect.poll(() => staleLoadStarted, { timeout: 6_000 }).toBe(true)
  await page.getByTestId('solo-review-confirm-solo-review').check()
  await page.getByPlaceholder('Review reason / requested change').fill('Reviewed the exact result locally.')
  await page.getByTestId('workflow-review-approve-solo-review').click()
  await expect(page.getByTestId('workflow-review-approve-solo-review')).toHaveCount(0)

  releaseStaleLoad?.()
  await expect.poll(
    () => page.getByTestId('workflow-review-approve-solo-review').count(),
  ).toBe(0)
})

test('Workbench deep links reject bundles and proposals from another selected project', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const foreignProjectId = fixtureId('foreign-project')
  const foreignBuildId = fixtureId('foreign-build')
  const foreignProposalId = fixtureId('foreign-proposal')
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
  await page.route(`**/v1/build-manifests/${foreignBuildId}`, (route) => {
    foreignBundleLoads += 1
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: corsHeaders(),
      body: JSON.stringify({
        ...state.workbenchBundle,
        id: foreignBuildId,
        rootBuildManifestId: foreignBuildId,
        projectId: foreignProjectId,
      }),
    })
  })
  await page.route(`**/v1/implementation-proposals/${foreignProposalId}`, (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    headers: corsHeaders(),
    body: JSON.stringify({
      ...implementationProposal('ready', 'accepted', 2),
      id: foreignProposalId,
      projectId: foreignProjectId,
      buildManifestId: foreignBuildId,
      applicationBuildContract: {
        id: fixtureId(`build-contract-${foreignBuildId}`),
        contractHash: hash('b'),
      },
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

  await page.goto(`/workbench/planning?view=code&bundleId=${foreignBuildId}`)
  await expect(page.getByRole('alert').filter({ hasText: 'Workbench bundle belongs to another project.' }))
    .toBeVisible()
  expect(foreignBundleLoads).toBe(1)

  foreignBundleLoads = 0
  await page.goto(`/workbench/planning?view=code&proposalId=${foreignProposalId}`)
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
  const state = await installPlatformMock(page, { authenticated: true })
  const bundleAId = fixtureId('bundle-race-a')
  const bundleBId = fixtureId('bundle-race-b')
  const runA = selectionWorkflowRun('run-race-a', bundleAId)
  const runB = selectionWorkflowRun('run-race-b', bundleBId)
  const bundleA = {
    ...buildManifest(runA.id), id: bundleAId, contentHash: hash('a'),
  }
  const bundleB = {
    ...buildManifest(runB.id), id: bundleBId, contentHash: hash('b'),
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
  await page.route(`**/v1/build-manifests/${bundleAId}/lineage-state`, async (route) => {
    resolveLineageAStarted()
    await lineageARelease
    await respond(route, { rootBundleId: bundleA.id, activeBundle: bundleA, lineage: [] })
    resolveLineageAFinished()
  })
  await page.route(`**/v1/build-manifests/${bundleBId}/lineage-state`, (route) =>
    respond(route, { rootBundleId: bundleB.id, activeBundle: bundleB, lineage: [] }))

  await page.goto(`/workbench/planning?view=code&runId=${runA.id}`)
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()
  await lineageAStarted
  await page.getByRole('button', { name: new RegExp(runB.id) }).click()
  const openSandbox = page.getByRole('button', { name: 'Open development sandbox' })
  await expect(openSandbox).toBeVisible()
  await openSandbox.click()
  await expect(page.getByText('frontend/app/page.tsx', { exact: true }).first()).toBeVisible()
  const bundleBBootstrap = state.requests.find((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/repository-candidates`
      && (item.body as { buildManifestId?: string })?.buildManifestId === bundleB.id)
  expect(bundleBBootstrap).toBeTruthy()
  releaseLineageA()
  await lineageAFinished

  await expect(page.getByText('frontend/app/page.tsx', { exact: true }).first()).toBeVisible()
  expect(state.sandboxCandidate?.buildManifest.id).toBe(bundleB.id)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/repository-candidates`
      && (item.body as { buildManifestId?: string })?.buildManifestId === bundleA.id)).toBe(false)
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
  const homeBundleId = fixtureId('bundle-home')
  const checkoutBundleId = fixtureId('bundle-checkout')
  await page.route(`**/v1/build-manifests/${homeBundleId}`, async (route) => {
    resolveHomeStarted()
    await homeRelease
    await route.fallback()
    resolveHomeFinished()
  })

  await page.goto(
    `/workbench/planning?view=code&bundleId=${homeBundleId}&proposalId=${checkoutProposal.id}`,
  )
  await homeStarted
  await expect(page).toHaveURL(new RegExp(`bundleId=${checkoutBundleId}`))
  await expect(page).toHaveURL(new RegExp(`proposalId=${checkoutProposal.id}`))
  await expect(page.getByText('Development input locked')).toBeVisible()
  releaseHome()
  await homeFinished

  await expect(page).toHaveURL(new RegExp(`bundleId=${checkoutBundleId}`))
  await expect(page).toHaveURL(new RegExp(`proposalId=${checkoutProposal.id}`))
  await expect(page.getByText('Development input locked')).toBeVisible()
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
  await expect(page.getByRole('heading', { name: 'Development input locked' })).toHaveCount(0)
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
    rootBundleId: primaryBuildManifestId,
    bundleId: primaryBuildManifestId,
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
    item.method === 'GET'
      && item.path === `/v1/build-manifests/${fixtureId('build-root-1')}/lineage-state`),
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
      rootBundleId: fixtureId('build-root-1'),
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

test('conversation polling does not cancel execution before the legacy Proposal quarantine gate', async ({ page }) => {
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
    expectedBundleId: primaryBuildManifestId,
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
  await expect(page.getByRole('button', { name: 'Discard and return to development' })).toBeVisible()
  await expect(page.getByRole('alert').filter({
    hasText: 'This change came from an older generation path',
  })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Finish current review' })).toHaveCount(0)
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
    expectedBundleId: primaryBuildManifestId,
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
  await page.route(`**/v1/build-manifests/${primaryBuildManifestId}/lineage-state`, async (route) => {
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

  await expect(page.getByRole('button', { name: 'Discard and return to development' })).toBeVisible()
  await expect(page.getByRole('alert').filter({
    hasText: 'This change came from an older generation path',
  })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Finish current review' })).toHaveCount(0)
  await expect(panel.getByRole('alert')).toHaveCount(0)
  expect(state.requests.filter((item) => (
    item.method === 'GET'
    && item.path === `/v1/build-manifests/${primaryBuildManifestId}/lineage-state`
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
    expectedBundleId: primaryBuildManifestId,
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
  await page.route(`**/v1/build-manifests/${primaryBuildManifestId}`, (route) => route.fulfill({
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
    expectedBundleId: primaryBuildManifestId,
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
    expectedBundleId: primaryBuildManifestId,
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
        buildManifestId: primaryBuildManifestId,
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
    item.method === 'POST' && item.path.endsWith(`/build-manifests/${primaryBuildManifestId}/generate`),
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
    expectedBundleId: fixtureId('bundle-group-b'),
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
  delete state.multiWorkbench.proposals[fixtureId('proposal-group-b')]
  state.multiWorkbench.currentProposalIds[fixtureId('bundle-group-b')] = undefined

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
    rootBundleId: fixtureId('bundle-group-b'),
    bundleId: fixtureId('bundle-group-b'),
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

test('fresh frozen build input opens a template-backed Candidate without direct generation', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto('/workbench/complete?view=code')

  await page.getByRole('button', { name: 'Lock development input' }).click()
  const openSandbox = page.getByRole('button', { name: 'Open development sandbox' })
  await expect(openSandbox).toBeVisible()
  await expect(page.getByText(
    'Edits are saved to a durable Candidate. Blueprint and document state are not reloaded by autosave.',
    { exact: true },
  )).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Generation model' })).toHaveCount(0)
  await expect(page.getByRole('textbox', { name: 'Implementation instruction' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Generate proposal' })).toHaveCount(0)

  await openSandbox.click()
  await expect(page.getByText('frontend/app/page.tsx', { exact: true }).first()).toBeVisible()
  await expect(page.getByRole('region', { name: 'Candidate verification' })).toBeVisible()
  await expect(page.getByRole('combobox', { name: 'Verification profile' })).toHaveValue('')

  const bootstrap = state.requests.find((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/repository-candidates`)
  expect(bootstrap?.body).toEqual({ buildManifestId: primaryBuildManifestId })
  expect(state.sandboxCandidate).not.toHaveProperty('baseWorkspaceRevision')
  expect(state.requests.some((item) => item.path.endsWith(`/build-manifests/${primaryBuildManifestId}/generate`))).toBe(false)
  expect(state.requests.some((item) => item.path.endsWith(`/implementation-proposals/${primaryProposalId}/apply`))).toBe(false)
  expect(state.requests.some((item) => item.path.includes('/api/generate'))).toBe(false)
})

test('development Preview keeps the primary path focused and separates production qualification', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  await page.goto('/workbench/complete?view=preview')

  await page.getByRole('button', { name: 'Lock development input' }).click()
  await page.getByRole('button', { name: 'Open development sandbox' }).click()

  await expect(page.getByText('Development Preview', { exact: true })).toBeVisible()
  await expect(page.getByText('Not a production release', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Start preview' })).toBeVisible()
  await expect(page.getByText('Start the development preview', { exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Candidate verification' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Agent', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Create Proposal' })).toHaveCount(0)
})

test('Candidate autosave preserves the mounted editor and does not reload governed Blueprint state', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  let blueprintLoads = 0
  let mainNavigations = 0
  page.on('request', (request) => {
    if (/\/blueprints(?:[/?]|$)/u.test(request.url())) blueprintLoads += 1
  })
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame()) mainNavigations += 1
  })

  await page.goto('/workbench/complete?view=code')
  await page.getByRole('button', { name: 'Lock development input' }).click()
  await page.getByRole('button', { name: 'Open development sandbox' }).click()
  await expect(page.getByText('frontend/app/page.tsx', { exact: true }).first()).toBeVisible()
  await page.getByRole('button', { name: 'frontend/app/page.tsx', exact: true }).click()

  const editor = page.locator('.monaco-editor').first()
  await expect(editor).toBeVisible()
  const blueprintLoadsBeforeEdit = blueprintLoads
  const navigationsBeforeEdit = mainNavigations
  const candidateVersionBeforeEdit = state.sandboxCandidate?.version
  const journalSequenceBeforeEdit = state.sandboxCandidate?.journalSequence
  const workbenchURL = page.url()
  const marker = 'CI_AUTOSAVE_PRESERVES_GOVERNED_CONTEXT'
  await editor.locator('.view-lines').click({ position: { x: 24, y: 18 } })
  await page.keyboard.press('Control+End')
  await page.keyboard.insertText(`\n// ${marker}\n`)

  await expect(page.locator('.monaco-editor .view-lines').first()).toContainText(marker)
  await expect.poll(() => state.requests.filter((item) => (
    item.method === 'PUT' && /\/sandbox-sessions\/[^/]+\/files\/frontend\/app\/page\.tsx$/u.test(item.path)
  )).length).toBe(1)
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  const save = state.requests.findLast((item) => item.method === 'PUT')
  expect(save?.body).toContain(marker)
  expect(state.sandboxCandidate?.version).toBe((candidateVersionBeforeEdit ?? 0) + 1)
  expect(state.sandboxCandidate?.journalSequence).toBe((journalSequenceBeforeEdit ?? -1) + 1)
  expect(page.url()).toBe(workbenchURL)
  expect(mainNavigations).toBe(navigationsBeforeEdit)
  expect(blueprintLoads).toBe(blueprintLoadsBeforeEdit)
})

test('Workbench renders production-shaped nullable build manifest collections', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const rawBundle = state.workbenchBundle as unknown as Record<string, unknown>

  // This intentionally mirrors the persisted production wire shape instead
  // of hiding Go nil slices behind the stricter WorkbenchBundleDto.
  Object.assign(rawBundle, {
    contractRevisions: null,
    designSystemRevisions: null,
    assumptions: null,
  })

  await page.goto('/workbench/complete?view=code')
  await page.getByRole('button', { name: 'Lock development input' }).click()

  await expect(page.getByRole('button', { name: 'Open development sandbox' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Generate proposal' })).toHaveCount(0)
  expect(state.requests.some((item) => item.path.endsWith(`/build-manifests/${primaryBuildManifestId}/generate`))).toBe(false)
})

test('multi-bundle Workbench applies the existing proposal then hands the rebased bundle to Candidate', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    multiBundleWorkbench: true,
  })
  await page.goto('/workbench/complete?view=code&runId=run-multi')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()

  await expect(page.getByText('Development input locked')).toBeVisible()
  await page.getByRole('button', { name: 'Toggle manifest details' }).click()
  await expect(page.getByText('blueprint.selection · 2 sources')).toBeVisible()
  await page.getByText('Inspect frozen context evidence').click()
  await expect(page.getByText('blueprint_selection_node', { exact: false })).toBeVisible()
  await expect(page.getByText('reviewed-start-proposal', { exact: false })).toBeVisible()
  await expect(page.getByText('page-home', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Accept pending' }).click()
  await page.getByRole('button', { name: 'Apply and continue' }).click()
  const checkoutRootId = fixtureId('bundle-checkout')
  await expect(page).toHaveURL(new RegExp(`bundleId=${checkoutRootId}`))
  const rebaseNext = page.getByRole('button', { name: 'Update to latest version' })
  await expect(rebaseNext).toBeEnabled()
  await rebaseNext.click()
  await expect(page.getByRole('button', { name: 'Open development sandbox' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Generation model' })).toHaveCount(0)
  await expect(page.getByRole('textbox', { name: 'Implementation instruction' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Generate proposal' })).toHaveCount(0)

  const firstDerivedId = fixtureId(`${checkoutRootId}-w1`)
  await expect.poll(() => state.requests
    .filter((item) => item.method === 'POST' && item.path.endsWith('/rebase'))
    .map((item) => item.path))
    .toContain(`/v1/build-manifests/${checkoutRootId}/rebase`)
  await expect.poll(() => state.multiWorkbench?.activeBundleIds[checkoutRootId])
    .toBe(firstDerivedId)
  await page.reload()
  await expect(page.getByRole('button', { name: 'Open development sandbox' })).toBeVisible()
  expect(state.multiWorkbench?.activeBundleIds[checkoutRootId]).toBe(firstDerivedId)

  const rebaseRequest = state.requests.find((item) =>
    item.method === 'POST' && item.path === `/v1/build-manifests/${checkoutRootId}/rebase`)
  expect(rebaseRequest?.body).toEqual({
    workspaceRevision: {
      artifactId: 'workspace-application',
      revisionId: 'workspace-r1',
      contentHash: hash('1'),
    },
  })
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/build-manifests/${firstDerivedId}/generate`)).toBe(false)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/implementation-proposals/${fixtureId('proposal-home')}/apply`)).toBe(true)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/workflow-runs/run-multi/resume`)).toBe(false)
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
  const checkoutRootId = fixtureId('bundle-checkout')
  await expect(page).toHaveURL(new RegExp(`bundleId=${checkoutRootId}`))
  await page.getByRole('button', { name: 'Update to latest version' }).click()
  await expect(page.getByRole('button', { name: 'Open development sandbox' })).toBeVisible()
  const firstDerivedId = fixtureId(`${checkoutRootId}-w1`)
  await expect.poll(() => state.requests
    .filter((item) => item.method === 'POST' && item.path.endsWith('/rebase'))
    .map((item) => item.path))
    .toContain(`/v1/build-manifests/${checkoutRootId}/rebase`)
  await expect.poll(() => state.multiWorkbench?.activeBundleIds[checkoutRootId])
    .toBe(firstDerivedId)

  if (!state.multiWorkbench) throw new Error('Expected multi-bundle mock state.')
  state.multiWorkbench.currentWorkspaceRevision = multiApplicationRevision(2)
  await page.reload()
  await expect(page.getByText('Development input locked')).toBeVisible()
  const rebaseStatus = page.getByRole('status').filter({ hasText: 'The project has changed.' })
  await expect(rebaseStatus).toContainText('Update this page to workspace r2 before development')
  await page.getByRole('button', { name: 'Update to latest version' }).click()
  await expect(page.getByRole('button', { name: 'Open development sandbox' })).toBeVisible()
  const secondDerivedId = fixtureId(`${checkoutRootId}-w2`)
  await expect.poll(() => state.multiWorkbench?.activeBundleIds[checkoutRootId])
    .toBe(secondDerivedId)

  const rebases = state.requests.filter((item) =>
    item.method === 'POST' && item.path.endsWith('/rebase'))
  expect(rebases.map((item) => item.path)).toEqual([
    `/v1/build-manifests/${checkoutRootId}/rebase`,
    `/v1/build-manifests/${firstDerivedId}/rebase`,
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
  await expect(page.getByRole('region', {
    name: 'Application delivery progress',
  })).toHaveCount(0)
  await expect(page.getByText(
    'The current version is ready to enter the release flow.',
    { exact: true },
  )).toHaveCount(0)
  expect(state.requests.some((item) =>
    item.path === `/v1/build-manifests/${fixtureId('bundle-group-a')}/lineage-state`,
  )).toBe(true)
  expect(state.requests.some((item) =>
    item.path === `/v1/build-manifests/${fixtureId('bundle-group-b')}/lineage-state`,
  )).toBe(false)

  await page.getByRole('button', {
    name: 'Workbench group workbench-b, status waiting input',
  }).click()
  await expect(page).toHaveURL(/workbenchNodeKey=workbench-b/)
  await expect(page.getByText(
    'Review the file changes and decide whether to apply them.',
    { exact: true },
  )).toHaveCount(0)
  expect(state.requests.some((item) =>
    item.path === `/v1/build-manifests/${fixtureId('bundle-group-b')}/lineage-state`,
  )).toBe(true)

  const reloadRequestIndex = state.requests.length
  await page.reload()
  await expect(page).toHaveURL(/workbenchNodeKey=workbench-b/)
  await expect.poll(() => state.requests.slice(reloadRequestIndex)
    .filter((item) => item.path.endsWith('/lineage-state'))
    .map((item) => item.path)).toContain(`/v1/build-manifests/${fixtureId('bundle-group-b')}/lineage-state`)
  await expect(page.getByText(
    'Review the file changes and decide whether to apply them.',
    { exact: true },
  )).toHaveCount(0)
  const reloadRequests = state.requests.slice(reloadRequestIndex)
  expect(reloadRequests.some((item) =>
    item.path === `/v1/build-manifests/${fixtureId('bundle-group-b')}/lineage-state`,
  )).toBe(true)
  expect(reloadRequests.some((item) =>
    item.path === `/v1/build-manifests/${fixtureId('bundle-group-a')}/lineage-state`,
  )).toBe(false)

  await page.getByRole('button', {
    name: 'Workbench group workbench-a, status waiting input',
  }).click()
  await expect(page.getByText(
    'The current version is ready to enter the release flow.',
    { exact: true },
  )).toHaveCount(0)
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
      implementationProposalIds: [fixtureId('proposal-group-a')],
      workspaceRevision: {
        artifactId: 'workspace-application',
        revisionId: 'workspace-r1',
        contentHash: hash('1'),
      },
    },
  })
})

test('Workbench apply and completion fail closed without a server action projection', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    multiWorkbenchGroups: true,
  })
  const current = state.run as ReturnType<typeof multiGroupWorkflowRun>
  state.run = {
    ...current,
    nodes: current.nodes.map((node) => ({
      ...node,
      allowedActions: undefined,
      blockingReasons: undefined,
    })),
  } as unknown as MockPlatformState['run']

  await page.goto('/workbench/complete?view=code&runId=run-groups')
  const closeConversation = page.getByRole('button', { name: 'Close conversation panel' })
  if (await closeConversation.isVisible()) await closeConversation.click()

  const complete = page.getByRole('button', { name: 'Complete Workbench' })
  await expect(complete).toHaveCount(0)
  expect(state.requests.some((item) =>
    item.method === 'POST'
      && item.path === `/v1/projects/${project.id}/workflow-runs/run-groups/resume`,
  )).toBe(false)
})

test('Design Import Center freezes an upload, requires an independent reviewer, and applies a Prototype revision', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/${project.id}/project/${project.id}/imports`)

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
  await page.goto(`/team/${project.id}/project/${project.id}/imports`)
  await page.getByTestId(`design-import-approve-${state.designImports[0].id}`).click()
  await expect(page.getByTestId('design-import-center').getByRole('alert')).toContainText('changed since it was loaded')
  await expect(page.getByTestId('design-import-list').getByText('Open', { exact: true })).toBeVisible()
  expect(state.designImports[0].status).toBe('open')
})

test('Design Import Center retries a processing lease with the same command key', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true, designImportCreateProcessingOnce: true })
  await page.goto(`/team/${project.id}/project/${project.id}/imports`)
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
  const graphUrl = `/team/${project.id}/project/${project.id}/graph`

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
  await page.goto(`/team/${project.id}/project/${project.id}/graph`)
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
  await page.goto(`/team/${project.id}/project/${project.id}/editor?artifactId=${projectBrief.artifact.id}`)
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
  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

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

test('Prototype Studio safely reads an exact historical PageSpec without dataBindings', async ({ page }) => {
  const errors: Error[] = []
  page.on('pageerror', (error) => errors.push(error))
  const state = await installPlatformMock(page, { authenticated: true })
  const historical = structuredClone(state.pageSpec)
  for (const revision of [historical.approvedRevision, historical.latestRevision]) {
    delete (revision.content as unknown as Record<string, unknown>).dataBindings
  }
  state.pageSpec = historical

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

  await expect(page.getByTitle('Page')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Revision + review' })).toBeEnabled()
  expect(errors).toEqual([])
})

test('formal Prototype state structure stays locked to its exact PageSpec authority', async ({ page }) => {
  await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()

  await expect(page.getByText('Formal Prototype state IDs, keys, required flags, and membership')).toBeVisible()
  await expect(page.getByLabel('State key Ready')).toBeDisabled()
  await expect(page.getByLabel('Delete state Ready')).toBeDisabled()
  await expect(page.getByText('Required coverage · 0 fixtures').first().locator('input')).toBeDisabled()
  await expect(page.getByLabel('State title ready')).toBeEnabled()
  await expect(page.getByLabel('New state key')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Revision + review' })).toBeEnabled()
})

test('Prototype Studio blocks cross-artifact switching and creation until the local save is safe', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    pauseFirstPrototypeDraftSave: true,
  })
  const gate = state.prototypeDraftSaveGate
  if (!gate) throw new Error('Expected a prototype draft save gate')
  const current = state.prototypes[0] as typeof approvedPrototype
  const secondContent = structuredClone(current.draft.content)
  secondContent.states[0] = { ...secondContent.states[0], title: 'Second Ready' }
  const secondId = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeff'
  const secondBase = draftPrototype(secondId)
  const second = {
    ...secondBase,
    artifact: {
      ...secondBase.artifact,
      title: 'Second Prototype',
    },
    draft: {
      ...secondBase.draft,
      content: secondContent,
      etag: '"prototype-second-draft:1"',
    },
  }
  state.prototypes = [current, second]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  const titleInput = page.getByLabel('State title ready')
  await titleInput.fill('Newest local Ready')

  const secondButton = page.getByRole('button', { name: /Second Prototype/ })
  const createButton = page.getByRole('button', { name: 'Create server prototype' })
  await expect(secondButton).toBeDisabled()
  await expect(createButton).toBeDisabled()
  await expect(titleInput).toHaveValue('Newest local Ready')

  await expect.poll(() => state.requests.some((item) =>
    item.method === 'PATCH' && item.path === `/v1/prototypes/${current.artifact.id}/draft`,
  )).toBe(true)
  await expect(page.getByText('Saving exact draft…', { exact: true })).toBeVisible()
  await expect(secondButton).toBeDisabled()
  gate.release()

  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  await expect(secondButton).toBeEnabled()
  await expect(createButton).toBeEnabled()
  await secondButton.click()
  await expect(page.getByLabel('State title ready')).toHaveValue('Second Ready')
})

test('Prototype Studio safely opens an incomplete workflow target and directs the user to its Proposal', async ({ page }) => {
  const errors: Error[] = []
  page.on('pageerror', (error) => errors.push(error))
  const state = await installPlatformMock(page, {
    authenticated: true,
    prototypeProposal: true,
  })
  const current = state.prototypes[0] as typeof approvedPrototype
  const incompleteContent = structuredClone(current.draft.content) as Record<string, unknown>
  delete incompleteContent.overrides
  delete incompleteContent.tokenBindings
  delete incompleteContent.componentBindings
  delete incompleteContent.assets
  delete incompleteContent.traceLinks
  state.prototypes = [{
    ...current,
    draft: {
      ...current.draft,
      content: {
        ...incompleteContent,
        states: [],
        breakpoints: [],
        frames: [],
      },
    },
  }]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

  await expect(page.getByText('AI prototype proposal is waiting')).toBeVisible()
  await expect(page.getByText('prototype-proposal-1', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Visual design', exact: true }).click()
  await expect(page.getByText('Design mode · 0 token bindings')).toBeVisible()
  await page.getByRole('button', { name: 'Components', exact: true }).click()
  await expect(page.getByText('0 component mappings')).toBeVisible()
  await page.getByRole('button', { name: 'Handoff', exact: true }).click()
  await expect(page.getByText('Exact source trace · 0 links')).toBeVisible()
  await page.getByRole('button', { name: 'Data', exact: true }).click()
  await expect(page.getByText('Token bindings')).toBeVisible()
  expect(errors).toEqual([])
})

test('Prototype Studio gives legacy semantic layers a visible fallback canvas layout', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const current = state.prototypes[0] as typeof approvedPrototype
  const baseContent = structuredClone(current.draft.content) as unknown as PrototypeContentDto
  const content: PrototypeContentDto = {
    ...baseContent,
    layers: {
      ...baseContent.layers,
      'layer-root': {
        ...baseContent.layers['layer-root'],
        childIds: ['legacy-title', 'legacy-input'],
        layout: {},
      },
      'legacy-title': {
        id: 'legacy-title',
        parentId: 'layer-root',
        childIds: [],
        kind: 'text',
        name: 'Legacy title',
        layout: {},
        style: {},
        properties: { text: 'Visible legacy title' },
        requirementIds: [],
        acceptanceCriterionIds: [],
        fieldMetadata: {},
      },
      'legacy-input': {
        id: 'legacy-input',
        parentId: 'layer-root',
        childIds: [],
        kind: 'input',
        name: 'Legacy input',
        layout: {},
        style: {},
        properties: { placeholder: 'Visible legacy input' },
        requirementIds: [],
        acceptanceCriterionIds: [],
        fieldMetadata: {},
      },
    },
  }
  state.prototypes = [{ ...current, draft: { ...current.draft, content } }]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

  const titleLayer = page.getByTitle('Legacy title')
  const inputLayer = page.getByTitle('Legacy input')
  await expect(titleLayer).toBeVisible()
  await expect(inputLayer).toBeVisible()
  const titleBox = await titleLayer.boundingBox()
  const inputBox = await inputLayer.boundingBox()
  expect(titleBox?.y).not.toBe(inputBox?.y)
  if (!titleBox || !inputBox) throw new Error('Expected legacy layers to have canvas bounds')
  for (const [layer, box] of [[titleLayer, titleBox], [inputLayer, inputBox]] as const) {
    const hitTitle = await page.evaluate(({ x, y }) =>
      (document.elementFromPoint(x, y)?.closest('button') as HTMLButtonElement | null)?.title,
    { x: box.x + box.width / 2, y: box.y + box.height / 2 })
    expect(hitTitle).toBe(await layer.getAttribute('title'))
  }
})

test('Prototype Studio uses visible viewport defaults, scene fallback layers, and an unknown-kind icon fallback', async ({ page }) => {
  const errors: Error[] = []
  page.on('pageerror', (error) => errors.push(error))
  const state = await installPlatformMock(page, { authenticated: true })
  const current = state.prototypes[0] as typeof approvedPrototype
  const baseContent = structuredClone(current.draft.content) as unknown as PrototypeContentDto
  const root = baseContent.layers['layer-root']
  const content = {
    ...baseContent,
    breakpoints: baseContent.breakpoints.map((breakpoint) => ({
      id: breakpoint.id,
      name: breakpoint.name,
      minWidth: breakpoint.minWidth,
      ...(breakpoint.maxWidth === undefined ? {} : { maxWidth: breakpoint.maxWidth }),
    })),
    layers: [],
    scene: {
      layers: [{
        ...root,
        childIds: ['layer-carousel'],
        layout: {},
      }, {
        id: 'layer-carousel',
        parentId: root.id,
        childIds: [],
        kind: 'carousel',
        name: 'Custom carousel',
        layout: {},
        style: {},
        properties: {},
        requirementIds: [],
        acceptanceCriterionIds: [],
        fieldMetadata: {},
      }],
    },
  } as unknown as PrototypeContentDto
  state.prototypes = [{ ...current, draft: { ...current.draft, content } }]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

  await expect(page.getByTitle('Custom carousel')).toBeVisible()
  await expect(page.getByLabel('Prototype breakpoint').locator('option')).toHaveText([
    'Desktop · 1,440×900',
    'Tablet · 768×1,024',
    'Mobile · 390×844',
  ])
  expect(errors).toEqual([])
})

test('Prototype Studio preserves a tiny viewport while rendering a visible review-blocked canvas', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const current = state.prototypes[0] as typeof approvedPrototype
  const baseContent = structuredClone(current.draft.content) as unknown as PrototypeContentDto
  const content: PrototypeContentDto = {
    ...baseContent,
    breakpoints: baseContent.breakpoints.map((breakpoint, index) => index === 0
      ? { ...breakpoint, viewportWidth: 0, viewportHeight: 1 }
      : breakpoint),
  }
  state.prototypes = [{ ...current, draft: { ...current.draft, content } }]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

  await expect(page.getByLabel('Prototype breakpoint').locator('option').first()).toHaveText('Desktop · 0×1')
  const canvas = page.getByTestId('prototype-canvas')
  await expect(canvas).toBeVisible()
  const canvasBox = await canvas.boundingBox()
  expect(canvasBox?.width).toBeGreaterThan(1000)
  expect(canvasBox?.height).toBeGreaterThan(700)
  await expect(page.getByText('Breakpoint 1 viewport width and height must each be integers of at least 240 pixels.'))
    .toBeVisible()
  await expect(page.getByRole('button', { name: 'Revision + review' })).toBeDisabled()

  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect.poll(() => state.requests.some((item) =>
    item.method === 'PATCH' && item.path === `/v1/prototypes/${current.artifact.id}/draft`,
  )).toBe(true)
  const saveRequest = state.requests.findLast((item) =>
    item.method === 'PATCH' && item.path === `/v1/prototypes/${current.artifact.id}/draft`)
  const savedContent = (saveRequest?.body as { content?: PrototypeContentDto } | undefined)?.content
  expect(savedContent?.breakpoints[0]).toMatchObject({ viewportWidth: 0, viewportHeight: 1 })
})

test('Prototype Studio retains malformed collection diagnostics and blocks autosave after ordinary edits', async ({ page }) => {
  const errors: Error[] = []
  page.on('pageerror', (error) => errors.push(error))
  const state = await installPlatformMock(page, { authenticated: true })
  const current = state.prototypes[0] as typeof approvedPrototype
  const baseContent = structuredClone(current.draft.content) as unknown as PrototypeContentDto
  const root = baseContent.layers['layer-root']
  const content = {
    ...baseContent,
    states: [...baseContent.states, null],
    tokenBindings: [...baseContent.tokenBindings, null],
    layers: [
      root,
      null,
      { name: 'Missing layer identity' },
    ],
    scene: {
      layers: [{
        ...root,
        id: 'scene-lure',
        name: 'Scene lure must not render',
      }],
    },
  } as unknown as PrototypeContentDto
  state.prototypes = [{ ...current, draft: { ...current.draft, content } }]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

  await expect(page.getByText('Invalid layer 2', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('Missing layer identity', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('Scene lure must not render', { exact: true })).toBeHidden()
  await expect(page.getByText(`Prototype data at states[${baseContent.states.length + 1}] must be an object.`))
    .toBeVisible()
  await expect(page.getByRole('button', { name: 'Revision + review' })).toBeDisabled()

  const patchCount = state.requests.filter((item) =>
    item.method === 'PATCH' && item.path === `/v1/prototypes/${current.artifact.id}/draft`).length
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  await page.getByLabel('State title ready').fill('Ready edited')
  await expect(page.getByRole('alert').filter({ hasText: 'Cannot save: the server prototype' }))
    .toContainText('cannot be normalized safely')
  expect(state.requests.filter((item) =>
    item.method === 'PATCH' && item.path === `/v1/prototypes/${current.artifact.id}/draft`)).toHaveLength(patchCount)
  expect(errors).toEqual([])
})

test('Prototype Studio blocks every Proposal mutation after a failed local save until explicit server reload', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    prototypeProposal: true,
  })
  const current = state.prototypes[0] as typeof approvedPrototype
  const serverTitle = current.draft.content.states[0].title
  const malformed = {
    ...current,
    draft: {
      ...current.draft,
      content: {
        ...current.draft.content,
        exploratory: null,
      } as unknown as ReturnType<typeof prototypeContent>,
    },
  }
  const secondId = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeaa'
  const secondBase = draftPrototype(secondId)
  const second = {
    ...secondBase,
    artifact: { ...secondBase.artifact, title: 'Error-safe second Prototype' },
  }
  state.prototypes = [malformed, second]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Trace', exact: true }).click()

  const acceptOperation = page.getByRole('button', {
    name: 'Accept proposal operation prototype-operation-name',
  })
  const rejectOperation = page.getByRole('button', {
    name: 'Reject proposal operation prototype-operation-name',
  })
  await expect(acceptOperation).toBeEnabled()
  await expect(rejectOperation).toBeEnabled()
  await expect(page.getByRole('button', { name: 'Accept all pending proposal operations' })).toBeEnabled()
  await expect(page.getByRole('button', { name: 'Reject all pending proposal operations' })).toBeEnabled()

  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  const titleInput = page.getByLabel('State title ready')
  await titleInput.fill('Ready local unsaved change')
  await expect(page.getByRole('alert').filter({ hasText: 'Cannot save: the server prototype' }))
    .toContainText('cannot be normalized safely')
  await expect(titleInput).toHaveValue('Ready local unsaved change')

  await page.getByRole('button', { name: 'Trace', exact: true }).click()
  await expect(page.getByText('The local draft has not been saved safely.')).toContainText(
    'reload the server draft before deciding or applying an AI proposal',
  )
  await expect(acceptOperation).toBeDisabled()
  await expect(rejectOperation).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Accept all pending proposal operations' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Reject all pending proposal operations' })).toBeDisabled()
  const secondPrototype = page.getByRole('button', { name: /Error-safe second Prototype/ })
  await expect(secondPrototype).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Create server prototype' })).toBeDisabled()

  state.prototypeProposal = artifactProposal('ready', ['accepted', 'accepted'], 3)
  await page.getByRole('button', { name: 'Refresh trace' }).click()
  const apply = page.getByRole('button', { name: 'Apply reviewed prototype proposal' })
  await expect(apply).toBeDisabled()
  expect(state.requests.filter((item) =>
    item.path.includes('/output-proposals/prototype-proposal-1/')
      && item.method === 'POST')).toHaveLength(0)

  await page.getByRole('button', { name: 'Discard local changes and reload server draft' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Cannot save: the server prototype' })).toBeHidden()
  await expect(apply).toBeEnabled()
  await expect(secondPrototype).toBeEnabled()
  await expect(page.getByRole('button', { name: 'Create server prototype' })).toBeEnabled()
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  await expect(page.getByLabel('State title ready')).toHaveValue(serverTitle)
})

test('Prototype Studio serializes overlapping autosaves and keeps the newest local edit', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    pauseFirstPrototypeDraftSave: true,
  })
  const gate = state.prototypeDraftSaveGate
  if (!gate) throw new Error('Expected a prototype draft save gate')
  const current = state.prototypes[0] as typeof approvedPrototype
  const draftPath = `/v1/prototypes/${current.artifact.id}/draft`

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  const titleInput = page.getByLabel('State title ready')
  await titleInput.fill('Ready edit A')
  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'PATCH' && item.path === draftPath).length).toBe(1)

  await titleInput.fill('Ready edit B')
  await expect(titleInput).toHaveValue('Ready edit B')
  gate.release()

  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'PATCH' && item.path === draftPath).length).toBe(2)
  const saves = state.requests.filter((item) =>
    item.method === 'PATCH' && item.path === draftPath)
  const firstContent = (saves[0].body as { content: PrototypeContentDto }).content
  const secondContent = (saves[1].body as { content: PrototypeContentDto }).content
  expect(firstContent.states[0].title).toBe('Ready edit A')
  expect(secondContent.states[0].title).toBe('Ready edit B')
  expect(saves[0].headers['if-match']).toBe(current.draft.etag)
  expect(saves[1].headers['if-match']).toBe('"prototype-draft:4"')
  await expect(titleInput).toHaveValue('Ready edit B')
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  await expect(page.getByText('Draft conflict')).toBeHidden()
})

test('Prototype Studio locks every editor transition while prototype creation is pending', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    pausePrototypeCreate: true,
  })
  const gate = state.prototypeCreateGate
  if (!gate) throw new Error('Expected a prototype create gate')
  const current = state.prototypes[0] as typeof approvedPrototype
  const second = titledDraftPrototype(
    'eeeeeeee-eeee-4eee-8eee-eeeeeeeeee01',
    'Creation-lock second Prototype',
  )
  state.prototypes = [current, second]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  const stateTitle = page.getByLabel('State title ready')
  const secondPrototype = page.getByRole('button', { name: /Creation-lock second Prototype/ })
  const createButton = page.getByRole('button', { name: 'Create server prototype' })
  await page.getByLabel('PageSpec source').selectOption(pageSpec.artifact.id)
  await page.getByPlaceholder('Prototype title').fill('Delayed Prototype')
  await createButton.click()

  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/prototypes`).length).toBe(1)
  await expect(createButton).toBeDisabled()
  await expect(page.getByLabel('PageSpec source')).toBeDisabled()
  await expect(page.getByPlaceholder('Prototype title')).toBeDisabled()
  await expect(secondPrototype).toBeDisabled()
  await expect(stateTitle).toBeDisabled()
  await expect(page.getByTitle('Add Heading')).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Save draft' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Revision + review' })).toBeDisabled()

  gate.release()
  await expect(page.getByText('Server-created Prototype', { exact: true }).first()).toBeVisible()
  await expect(page.getByTitle('Add Heading')).toBeEnabled()
  await expect(stateTitle).toBeEnabled()
  expect(state.requests.filter((item) =>
    item.method === 'POST' && item.path === `/v1/projects/${project.id}/prototypes`)).toHaveLength(1)
})

test('Prototype Studio keeps a delayed Proposal apply exclusive at a nonzero edit generation', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    prototypeProposal: true,
    pausePrototypeProposalApply: true,
  })
  const gate = state.prototypeProposalApplyGate
  if (!gate) throw new Error('Expected a prototype Proposal apply gate')
  const current = state.prototypes[0] as typeof approvedPrototype
  const second = titledDraftPrototype(
    'eeeeeeee-eeee-4eee-8eee-eeeeeeeeee02',
    'Apply-lock second Prototype',
  )
  state.prototypes = [current, second]
  state.prototypeProposal = artifactProposal('ready', ['accepted', 'accepted'], 3)

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  const stateTitle = page.getByLabel('State title ready')
  await stateTitle.fill('Ready before delayed apply')
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Trace', exact: true }).click()
  page.once('dialog', (dialog) => void dialog.accept())
  const apply = page.getByRole('button', { name: 'Apply reviewed prototype proposal' })
  await apply.click()
  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'POST'
      && item.path === '/v1/output-proposals/prototype-proposal-1/apply').length).toBe(1)

  await expect(apply).toBeDisabled()
  await expect(page.getByRole('button', { name: /Apply-lock second Prototype/ })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Create server prototype' })).toBeDisabled()
  await expect(page.getByTitle('Add Heading')).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Revision + review' })).toBeDisabled()
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  await expect(stateTitle).toBeDisabled()
  await expect(stateTitle).toHaveValue('Ready before delayed apply')

  gate.release()
  await expect(page.getByText('AI-reviewed Page', { exact: true }).first()).toBeVisible()
  await expect(stateTitle).toBeEnabled()
  await expect(stateTitle).toHaveValue('Ready before delayed apply')
  expect(state.requests.filter((item) =>
    item.method === 'POST'
      && item.path === '/v1/output-proposals/prototype-proposal-1/apply')).toHaveLength(1)
})

test('Prototype Studio freezes the captured revision snapshot until revision and review complete', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    pausePrototypeRevisionCreate: true,
  })
  const gate = state.prototypeRevisionCreateGate
  if (!gate) throw new Error('Expected a prototype revision gate')
  const current = state.prototypes[0] as typeof approvedPrototype
  const second = titledDraftPrototype(
    'eeeeeeee-eeee-4eee-8eee-eeeeeeeeee03',
    'Revision-lock second Prototype',
  )
  state.prototypes = [current, second]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Manage states & breakpoints' }).click()
  const stateTitle = page.getByLabel('State title ready')
  const capturedTitle = await stateTitle.inputValue()
  const revision = page.getByRole('button', { name: 'Revision + review' })
  await revision.click()

  const revisionPath = `/v1/prototypes/${current.artifact.id}/revisions`
  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'POST' && item.path === revisionPath).length).toBe(1)
  await expect(revision).toBeDisabled()
  await expect(page.getByRole('button', { name: /Revision-lock second Prototype/ })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Create server prototype' })).toBeDisabled()
  await expect(page.getByTitle('Add Heading')).toBeDisabled()
  await expect(stateTitle).toBeDisabled()
  await expect(stateTitle).toHaveValue(capturedTitle)

  gate.release()
  await expect.poll(() => state.prototypeCreatedRevision).not.toBeNull()
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  await expect(stateTitle).toBeEnabled()
  await expect(stateTitle).toHaveValue(capturedTitle)
  expect((state.prototypeCreatedRevision as { content?: PrototypeContentDto } | null)?.content?.states[0].title)
    .toBe(capturedTitle)
  expect(state.requests.filter((item) =>
    item.method === 'POST' && item.path === revisionPath)).toHaveLength(1)
})

test('Prototype Studio retries review for the same immutable revision after a 503', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    prototypeReviewFirstFailure: 'before-create',
  })
  const revisionPath = `/v1/prototypes/${approvedPrototype.artifact.id}/revisions`
  const reviewPath = `/v1/projects/${project.id}/reviews`

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Revision + review' }).click()

  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'POST' && item.path === reviewPath).length).toBe(1)
  await expect(page.getByRole('heading', { name: 'Artifact service unavailable' })).toBeVisible()
  expect(state.prototypeReviews).toHaveLength(0)
  expect(state.requests.filter((item) =>
    item.method === 'POST' && item.path === revisionPath)).toHaveLength(1)

  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByRole('button', { name: 'Revision + review' })).toBeVisible()
  await expect(page.getByRole('alert').filter({ hasText: 'without creating another revision' }))
    .toBeVisible()
  await page.getByRole('button', { name: 'Revision + review' }).click()

  await expect.poll(() => state.requests.filter((item) =>
    item.method === 'POST' && item.path === reviewPath).length).toBe(2)
  await expect.poll(() => state.prototypeReviews.length).toBe(1)
  const revisionRequests = state.requests.filter((item) =>
    item.method === 'POST' && item.path === revisionPath)
  const reviewRequests = state.requests.filter((item) =>
    item.method === 'POST' && item.path === reviewPath)
  expect(revisionRequests).toHaveLength(1)
  expect(state.prototypeCreatedRevision).not.toBeNull()
  const exactTarget = {
    artifactId: state.prototypeCreatedRevision?.artifactId,
    revisionId: state.prototypeCreatedRevision?.id,
    contentHash: state.prototypeCreatedRevision?.contentHash,
  }
  expect(reviewRequests.map((request) => (request.body as { target?: unknown }).target))
    .toEqual([exactTarget, exactTarget])
})

test('Prototype Studio does not duplicate a review when requestReview returns false after creation', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    prototypeReviewFirstFailure: 'after-create',
  })
  const revisionPath = `/v1/prototypes/${approvedPrototype.artifact.id}/revisions`
  const reviewPath = `/v1/projects/${project.id}/reviews`

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  const revision = page.getByRole('button', { name: 'Revision + review' })
  await revision.click()

  await expect.poll(() => state.prototypeReviews.length).toBe(1)
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  expect(state.requests.filter((item) =>
    item.method === 'POST' && item.path === revisionPath)).toHaveLength(1)
  expect(state.requests.filter((item) =>
    item.method === 'POST' && item.path === reviewPath)).toHaveLength(1)

  await revision.click()
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  expect(state.requests.filter((item) =>
    item.method === 'POST' && item.path === revisionPath)).toHaveLength(1)
  expect(state.requests.filter((item) =>
    item.method === 'POST' && item.path === reviewPath)).toHaveLength(1)
})

test('Prototype Studio keeps cyclic layer deletion bounded and exposes only frozen fixture governance', async ({ page }) => {
  const errors: Error[] = []
  page.on('pageerror', (error) => errors.push(error))
  const state = await installPlatformMock(page, { authenticated: true })
  const current = state.prototypes[0] as typeof approvedPrototype
  const baseContent = structuredClone(current.draft.content) as unknown as PrototypeContentDto
  const root = baseContent.layers['layer-root']
  const childId = 'layer-cycle-child'
  const content: PrototypeContentDto = {
    ...baseContent,
    layers: {
      ...baseContent.layers,
      [root.id]: { ...root, childIds: [childId] },
      [childId]: {
        id: childId,
        parentId: root.id,
        childIds: [root.id],
        kind: 'group',
        name: 'Cycle child',
        layout: { x: 20, y: 20, width: 200, height: 120 },
        style: {},
        properties: {},
        requirementIds: [],
        acceptanceCriterionIds: [],
        fieldMetadata: {},
      },
    },
  }
  state.prototypes = [{ ...current, draft: { ...current.draft, content } }]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByTitle('Cycle child').click()
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.getByTitle('Cycle child')).toBeHidden()

  await page.getByRole('button', { name: 'Data', exact: true }).click()
  await expect(page.getByText('Fixture governance')).toBeVisible()
  await expect(page.getByText('This panel only displays frozen fixtures')).toContainText(
    'cannot invent ad-hoc fixture IDs',
  )
  await expect(page.getByRole('button', { name: 'Add fixture to draft' })).toHaveCount(0)
  await expect(page.getByPlaceholder('operationId or endpoint')).toHaveCount(0)
  expect(errors).toEqual([])
})

test('Prototype Studio keeps duplicate array layers visible but blocks saving them', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const current = state.prototypes[0] as typeof approvedPrototype
  const baseContent = structuredClone(current.draft.content) as unknown as PrototypeContentDto
  const root = baseContent.layers['layer-root']
  const duplicate = {
    id: 'layer-duplicate',
    parentId: root.id,
    childIds: [],
    kind: 'text',
    name: 'Duplicate layer',
    layout: {},
    style: {},
    properties: { text: 'Duplicate layer' },
    requirementIds: [],
    acceptanceCriterionIds: [],
    fieldMetadata: {},
  }
  const content = {
    ...baseContent,
    layers: [
      { ...root, childIds: [duplicate.id] },
      duplicate,
      { ...duplicate, name: 'Duplicate layer copy' },
    ],
  } as unknown as PrototypeContentDto
  state.prototypes = [{ ...current, draft: { ...current.draft, content } }]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

  await expect(page.getByTitle('Duplicate layer', { exact: true })).toBeVisible()
  await expect(page.getByTitle('Duplicate layer copy', { exact: true })).toBeVisible()
  const patchCount = state.requests.filter((item) =>
    item.method === 'PATCH' && item.path === `/v1/prototypes/${current.artifact.id}/draft`).length
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Cannot save: the prototype' }))
    .toContainText('duplicate or inconsistent layer IDs')
  expect(state.requests.filter((item) =>
    item.method === 'PATCH' && item.path === `/v1/prototypes/${current.artifact.id}/draft`)).toHaveLength(patchCount)
})

test('Prototype Studio treats a frame with a missing root as repairable missing coverage', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const current = state.prototypes[0] as typeof approvedPrototype
  const baseContent = structuredClone(current.draft.content) as unknown as PrototypeContentDto
  const content: PrototypeContentDto = {
    ...baseContent,
    frames: baseContent.frames.map((frame, index) => index === 0
      ? { ...frame, rootLayerId: 'missing-root' }
      : frame),
  }
  state.prototypes = [{ ...current, draft: { ...current.draft, content } }]

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

  await expect(page.getByText('Missing Ready · Desktop frame')).toBeVisible()
  await page.getByRole('button', { name: 'Repair all frame coverage' }).click()
  await expect(page.getByText('Missing Ready · Desktop frame')).toBeHidden()
  await expect(page.getByTitle('Page')).toBeVisible()
})

test('Prototype Studio decides and applies an AI proposal before freezing its attributed revision', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    prototypeProposal: true,
  })
  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

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
  expect(state.requests[applyIndex]?.body).toEqual({ version: 3 })
  const revisionIndex = state.requests.findIndex((item) =>
    item.method === 'POST'
      && item.path === `/v1/prototypes/${approvedPrototype.artifact.id}/revisions`)
  expect(revisionIndex).toBeGreaterThan(applyIndex)
})

test('Prototype Studio explicitly confirms before replacing unrevisioned draft changes with a Proposal', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    prototypeProposal: true,
  })
  const current = state.prototypes[0] as typeof approvedPrototype
  state.prototypes = [{
    ...current,
    draft: { ...current.draft, contentHash: hash('0') },
  }]
  state.prototypeProposal = artifactProposal('ready', ['accepted', 'accepted'], 3)

  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)
  await page.getByRole('button', { name: 'Trace', exact: true }).click()
  page.once('dialog', async (dialog) => {
    expect(dialog.message()).toContain('working draft contains changes that have not been revisioned')
    await dialog.accept()
  })
  await page.getByRole('button', { name: 'Apply reviewed prototype proposal' }).click()

  await expect(page.getByText('Applied to the server draft.')).toBeVisible()
  const request = state.requests.find((item) =>
    item.method === 'POST'
      && item.path === '/v1/output-proposals/prototype-proposal-1/apply')
  expect(request?.body).toEqual({
    version: 3,
    discardUnrevisionedChanges: true,
    expectedDraftId: current.draft.id,
    expectedDraftEtag: current.draft.etag,
    expectedDraftContentHash: hash('0'),
  })
})

test('Blueprint PageSpec editor autosaves, versions, and requests exact revision review', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/${project.id}/project/${project.id}/blueprint`)

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

test('PageSpec workflow deep link prioritizes the exact Proposal and locks historical apply and revision creation', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const linkedProposal = pageSpecArtifactProposal('page-spec-workflow-proposal')
  const historicalProposal = pageSpecArtifactProposal('page-spec-historical-proposal')
  state.pageSpecProposals = [historicalProposal, linkedProposal]

  await page.goto(
    `/team/${project.id}/project/${project.id}/blueprint?artifactId=${pageSpec.artifact.id}&proposalId=${linkedProposal.id}`,
  )

  const guide = page.getByTestId('page-spec-workflow-proposal-guide')
  await expect(guide).toContainText('Exact workflow-linked PageSpec proposal')
  await expect(guide).toContainText(linkedProposal.id)
  await expect(guide).toContainText('Revision creation stays locked until apply succeeds.')

  const proposalCards = page.locator('article').filter({ hasText: 'page-spec-' })
  await expect(proposalCards).toHaveCount(2)
  await expect(proposalCards.nth(0)).toContainText(linkedProposal.id)
  await expect(proposalCards.nth(0).getByText('Workflow linked', { exact: true })).toBeVisible()

  const linkedApply = proposalCards.nth(0).getByRole('button', {
    name: 'Apply selected operations',
  })
  await expect(linkedApply).toBeEnabled()

  const historicalCard = proposalCards.filter({ hasText: historicalProposal.id })
  await expect(historicalCard).toContainText(
    'This historical proposal is not linked to the current workflow node and cannot be applied from this entry point.',
  )
  await expect(historicalCard.getByRole('button', {
    name: 'Apply selected operations',
  })).toBeDisabled()

  await page.getByRole('button', { name: 'Versions 1' }).last().click()
  await expect(page.getByRole('button', { name: 'Create immutable revision' })).toBeDisabled()
  await expect(page.getByText(
    'Review and apply the exact workflow-linked proposal in the Proposal tab first.',
    { exact: true },
  )).toBeVisible()
})

test('PageSpec run link infers the exact typed-lineage Proposal and locks Review until Revision', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    pageSpecHumanEditRun: true,
  })
  const linkedProposal = pageSpecArtifactProposal('page-spec-human-edit-proposal')
  const historicalProposal = pageSpecArtifactProposal('page-spec-historical-proposal')
  state.pageSpecProposals = [historicalProposal, linkedProposal]

  await page.goto(
    `/team/${project.id}/project/${project.id}/blueprint?runId=run-page-spec-human-edit&artifactId=${pageSpec.artifact.id}`,
  )

  await expect(page).toHaveURL(new RegExp(`[?&]proposalId=${linkedProposal.id}(?:&|$)`))
  const guide = page.getByTestId('page-spec-workflow-proposal-guide')
  await expect(guide).toContainText('Exact workflow-linked PageSpec proposal')
  await expect(guide).toContainText(linkedProposal.id)

  const linkedCard = page.locator('article').filter({ hasText: linkedProposal.id })
  await expect(linkedCard.getByText('Workflow linked', { exact: true })).toBeVisible()
  const review = page.getByRole('button', { name: 'Review 0' }).last()
  await expect(review).toBeDisabled()

  await linkedCard.getByRole('button', { name: 'Apply selected operations' }).click()
  await expect(guide).toContainText('The proposal is applied.')
  await expect(review).toBeDisabled()

  await page.getByRole('button', { name: 'Versions 1' }).last().click()
  const createRevision = page.getByRole('button', { name: 'Create immutable revision' })
  await expect(createRevision).toBeEnabled()
  await createRevision.click()
  await expect(guide).toContainText('The immutable Revision is ready.')
  await expect(review).toBeEnabled()
})

test('PageSpec run link does not trust a Proposal ID outside the typed lineage', async ({ page }) => {
  const state = await installPlatformMock(page, {
    authenticated: true,
    pageSpecHumanEditRun: true,
  })
  const linkedProposal = pageSpecArtifactProposal('page-spec-human-edit-proposal')
  const historicalProposal = pageSpecArtifactProposal('page-spec-historical-proposal')
  state.pageSpecProposals = [historicalProposal, linkedProposal]

  await page.goto(
    `/team/${project.id}/project/${project.id}/blueprint?runId=run-page-spec-human-edit&artifactId=${pageSpec.artifact.id}&proposalId=${historicalProposal.id}`,
  )

  await expect(page).toHaveURL(new RegExp(`[?&]proposalId=${linkedProposal.id}(?:&|$)`))
  const guide = page.getByTestId('page-spec-workflow-proposal-guide')
  await expect(guide).toContainText(linkedProposal.id)
  await expect(page.getByRole('button', { name: 'Review 0' }).last()).toBeDisabled()

  const historicalCard = page.locator('article').filter({ hasText: historicalProposal.id })
  await expect(historicalCard.getByRole('button', { name: 'Apply selected operations' })).toBeDisabled()
  expect(state.requests.some((request) =>
    request.method === 'POST'
      && (
        request.path.includes('/output-proposals/')
        || request.path === `/v1/page-specs/${pageSpec.artifact.id}/revisions`
      ))).toBe(false)
})

test('PageSpec workflow can explicitly restore an exact Proposal base without normalized autosave', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const rawBaseContent = {
    blueprintPageNodeId: 'page-dashboard',
    title: 'Dashboard',
    route: '/dashboard',
    userGoal: 'Inspect work.',
    states: pageSpecRevision.content.states,
    dataBindings: [],
    interactions: [],
    schemaVersion: 1,
  } as unknown as typeof pageSpecRevision.content
  const baseRevision = {
    ...pageSpecRevision,
    content: rawBaseContent,
    contentHash: hash('b'),
  }
  state.pageSpec = {
    ...state.pageSpec,
    latestRevision: baseRevision,
    draft: {
      ...state.pageSpec.draft!,
      baseRevisionId: baseRevision.id,
      content: {
        ...rawBaseContent,
        entryPoints: [],
        exitPoints: [],
        requiredRoles: [],
        acceptanceCriterionIds: [],
        nonFunctionalConstraints: [],
      },
      contentHash: hash('c'),
      revision: 7,
      etag: '"page-spec-draft:7"',
    },
  }
  const linkedProposal = {
    ...pageSpecArtifactProposal('page-spec-workflow-recovery'),
    baseRevision: exactRevision(
      baseRevision.artifactId,
      baseRevision.id,
      baseRevision.revisionNumber,
      baseRevision.contentHash,
    ),
  }
  state.pageSpecProposals = [linkedProposal]

  await page.goto(
    `/team/${project.id}/project/${project.id}/blueprint?artifactId=${pageSpec.artifact.id}&proposalId=${linkedProposal.id}`,
  )

  const guide = page.getByTestId('page-spec-workflow-proposal-guide')
  await expect(guide).toContainText('Restore the exact base before applying the proposal.')
  await page.getByRole('button', { name: 'Restore Proposal base' }).click()
  await expect(guide).toContainText('This discards all current unversioned draft changes')
  await page.getByRole('button', { name: 'Confirm restore' }).click()

  await expect(guide).toContainText('Revision creation stays locked until apply succeeds.')
  const recoveryPatch = state.requests.find((request) =>
    request.method === 'PATCH'
      && request.path === `/v1/drafts/${pageSpec.draft.id}`,
  )
  expect(recoveryPatch?.headers['if-match']).toBe('"page-spec-draft:7"')
  const recoveryContent = (recoveryPatch?.body as { content?: Record<string, unknown> })?.content
  expect(recoveryContent).toEqual(rawBaseContent)
  expect(recoveryContent).not.toHaveProperty('entryPoints')
  expect(recoveryContent).not.toHaveProperty('acceptanceCriterionIds')

  await page.waitForTimeout(850)
  expect(state.requests.filter((request) =>
    request.method === 'PATCH'
      && request.path === `/v1/page-specs/${pageSpec.artifact.id}/draft`,
  )).toHaveLength(0)
  const apply = page.getByRole('button', { name: 'Apply selected operations' })
  await expect(apply).toBeEnabled()
  await apply.click()

  await expect(guide).toContainText('The proposal is applied.')
  const createRevision = page.getByRole('button', { name: 'Create immutable revision' })
  await expect(createRevision).toBeEnabled()
  await createRevision.click()
  await expect(guide).toContainText('The immutable Revision is ready.')

  const applyIndex = state.requests.findIndex((request) =>
    request.method === 'POST'
      && request.path === `/v1/output-proposals/${linkedProposal.id}/apply`)
  const revisionIndex = state.requests.findIndex((request) =>
    request.method === 'POST'
      && request.path === `/v1/page-specs/${pageSpec.artifact.id}/revisions`)
  expect(applyIndex).toBeGreaterThan(-1)
  expect(revisionIndex).toBeGreaterThan(applyIndex)
  expect(state.requests.filter((request) =>
    request.method === 'PATCH'
      && request.path === `/v1/page-specs/${pageSpec.artifact.id}/draft`,
  )).toHaveLength(0)
})

test('Blueprint type selects persist canonical values instead of translated labels', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/${project.id}/project/${project.id}/blueprint`)

  const nodeKind = page.getByLabel('Kind')
  const edgeKind = page.getByLabel('Edge type')
  await expect(nodeKind.locator('option').filter({ hasText: 'Data Model' })).toHaveAttribute('value', 'dataEntity')
  await expect(edgeKind.locator('option').filter({ hasText: 'navigates to' })).toHaveAttribute('value', 'navigates_to')

  await page.getByLabel('Source').selectOption('feature-orders')
  await edgeKind.selectOption('navigates_to')
  await page.getByLabel('Target').selectOption('page-dashboard')
  await page.getByRole('button', { name: 'Connect' }).click()

  await expect.poll(() => state.requests.some((request) => {
    if (request.method !== 'PATCH' || request.path !== `/v1/blueprints/${blueprint.artifact.id}/draft`) return false
    const content = (request.body as { content?: typeof blueprintContent })?.content
    return content?.semantic.edges.some((edge) => edge.kind === 'navigates_to')
  })).toBe(true)
})

test('Blueprint revision creation repairs a localized legacy edge before freezing the draft', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  const localizedContent = structuredClone(blueprintContent)
  localizedContent.edges[0].kind = '包含'
  localizedContent.semantic.edges[0].kind = '包含'
  state.blueprintProposal = blueprintArtifactProposal('applied')
  state.blueprint = {
    ...state.blueprint,
    draft: {
      id: 'blueprint-draft-legacy-edge',
      artifactId: blueprint.artifact.id,
      baseRevisionId: blueprintRevision.revisionId,
      sourceVersions: [],
      revision: 37,
      content: localizedContent,
      contentHash: hash('7'),
      updatedBy: user.id,
      updatedAt: now,
      etag: '"blueprint-draft:37"',
    },
  }
  await page.goto(`/team/${project.id}/project/${project.id}/blueprint`)

  const nextStep = page.getByRole('status').filter({ hasText: /Proposal applied/i })
  const createRevision = nextStep.getByRole('button', { name: 'Create immutable revision', exact: true })
  await expect(createRevision).toBeEnabled()
  await createRevision.click()
  await expect.poll(() => state.blueprintCreatedRevision?.id).toBe('cccccccc-cccc-4ccc-8ccc-ccccccccccc3')

  const draftPath = `/v1/blueprints/${blueprint.artifact.id}/draft`
  const revisionPath = `/v1/blueprints/${blueprint.artifact.id}/revisions`
  const patchIndex = state.requests.findIndex((request) => request.method === 'PATCH' && request.path === draftPath)
  const revisionIndex = state.requests.findIndex((request) => request.method === 'POST' && request.path === revisionPath)
  expect(patchIndex).toBeGreaterThanOrEqual(0)
  expect(revisionIndex).toBeGreaterThan(patchIndex)
  const savedContent = (state.requests[patchIndex].body as { content: typeof blueprintContent }).content
  expect(savedContent.edges[0].kind).toBe('contains')
  expect(savedContent.semantic.edges[0].kind).toBe('contains')
  expect(state.requests[revisionIndex].headers['if-match']).toBe('"blueprint-draft:38"')
  expect(state.blueprintCreatedRevision?.content.semantic.edges[0].kind).toBe('contains')
})

test('Blueprint autosave preserves newer input and background refresh keeps the editor mounted', async ({ page }) => {
  const realtime = await installProjectWebSocketMock(page)
  const state = await installPlatformMock(page, {
    authenticated: true,
    pauseFirstBlueprintDraftSave: true,
  })
  const firstSaveGate = state.blueprintDraftSaveGate
  expect(firstSaveGate).not.toBeNull()
  await page.goto(`/team/${project.id}/project/${project.id}/blueprint`)

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
  await page.goto('/team/' + project.id + '/project/' + project.id + '/blueprint')

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
  await page.goto('/team/' + project.id + '/project/' + project.id + '/blueprint')

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
  await page.goto('/team/' + project.id + '/project/' + project.id + '/blueprint')

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
    '/team/' + project.id + '/project/' + project.id
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
  await page.goto(`/team/${project.id}/project/${project.id}/blueprint`)

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
  await page.goto(`/team/${project.id}/project/${project.id}/blueprint`)

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
  await page.goto(`/team/${project.id}/project/${project.id}/blueprint`)

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
  await page.goto(`/team/${project.id}/project/${project.id}/blueprint`)

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
  await expect(page.getByRole('button', { name: 'Open development sandbox' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Generate proposal' })).toHaveCount(0)
})

test('Prototype revision requests canonical review from another project member', async ({ page }) => {
  const state = await installPlatformMock(page, { authenticated: true })
  await page.goto(`/team/${project.id}/project/${project.id}/prototype`)

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
    'access-control-expose-headers': [
      'ETag',
      'X-Request-ID',
      'X-CSRF-Token',
      'X-Command-ETag',
      'X-Command-Location',
      'X-Sandbox-Session-ETag',
      'X-Sandbox-Session-Epoch',
      'X-Candidate-Version',
      'X-Candidate-ID',
      'X-Candidate-Journal-Sequence',
      'X-Writer-Lease-Epoch',
      'X-Candidate-Tree-Hash',
      'X-Candidate-Tree-ETag',
      'X-Content-Hash',
      'X-File-Mode',
    ].join(', '),
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
    expectedBundleId: primaryBuildManifestId,
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
      nodes: [
        ...nodes,
        ...(options.blueprintHumanEditRun ? [{
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
        }] : []),
        ...(options.pageSpecHumanEditRun ? [{
          id: 'page-spec-edit',
          name: 'Edit generated PageSpec',
          type: 'human_edit' as const,
          humanEdit: {
            artifactType: 'blueprint' as const,
            artifactKind: 'page_spec',
            requiredRole: 'editor',
            instructions: 'Apply the linked Proposal and submit its exact immutable revision.',
          },
        }] : []),
      ],
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
  const states = pageSpecRevision.content.states.map((state) => ({
    id: state.id,
    key: state.key,
    title: state.title,
    required: state.required,
    fixtureIds: [...state.fixtureIds],
    pageStateId: state.id,
  }))
  const breakpoints = [
    { id: 'breakpoint-desktop', name: 'Desktop', minWidth: 1024, viewportWidth: 1280, viewportHeight: 800 },
    { id: 'breakpoint-tablet', name: 'Tablet', minWidth: 768, maxWidth: 1023, viewportWidth: 834, viewportHeight: 1112 },
    { id: 'breakpoint-mobile', name: 'Mobile', minWidth: 0, maxWidth: 767, viewportWidth: 390, viewportHeight: 844 },
  ]
  return {
    pageSpecRevision: exactRevision(pageSpecRevision.artifactId, pageSpecRevision.id, 2, pageSpecRevision.contentHash),
    exploratory: false,
    states,
    breakpoints,
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
    frames: states.flatMap((state) => breakpoints.map((breakpoint) => ({
      id: `frame-${state.id}-${breakpoint.id}`,
      stateId: state.id,
      breakpointId: breakpoint.id,
      rootLayerId: 'layer-root',
      title: `${state.title} · ${breakpoint.name}`,
    }))),
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

function titledDraftPrototype(id: string, title: string) {
  const prototype = draftPrototype(id)
  return {
    ...prototype,
    artifact: {
      ...prototype.artifact,
      title,
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
  const canonicalReviewApproved = reviewRevision.id === briefRevision.id
    && reviewRevision.contentHash === briefRevision.contentHash
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
    definition: {
      id: workflowDefinition.id,
      version: 2,
      hash: workflowDefinition.contentHash,
      executionProfile: workflowExecutionProfile,
    },
    executionProfile: workflowExecutionProfile,
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
      allowedActions: waitingReview
        ? canonicalReviewApproved
          ? ['approve_review', 'request_review_changes']
          : ['request_review_changes']
        : failed ? ['retry'] : [],
      blockingReasons: waitingReview && !canonicalReviewApproved
        ? [{
            code: 'canonical_review_gate_blocked',
            message: 'Approve the exact upstream revision in Review Center before approving this Workflow node.',
            sourceRef: null,
          }]
        : [],
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
    bundleIds: [primaryBuildManifestId],
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
      allowedActions: ['submit_input'] as const,
      blockingReasons: [],
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
      allowedActions: [],
      blockingReasons: [],
      availableAt: now,
      createdAt: now,
      updatedAt: now,
    }],
  }
}

function pageSpecHumanEditWorkflowRun() {
  const proposal = pageSpecArtifactProposal('page-spec-human-edit-proposal')
  const nodeKey = 'page-spec-edit:slice-dashboard'
  const sliceId = 'slice-dashboard'
  const producerNodeKey = 'page-spec-generate:slice-dashboard'
  return {
    id: 'run-page-spec-human-edit',
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
      values: {},
      nodes: {
        [nodeKey]: {
          definitionNodeId: 'page-spec-edit',
          maxAttempts: 3,
          timeoutNanos: 60_000_000_000,
          input: {
            hash: hash('i'),
            bindings: [{
              source: {
                nodeKey: producerNodeKey,
                definitionNodeId: 'page-spec-generate',
                artifactRevisions: [proposal.baseRevision],
                materializedArtifactRevisions: [],
                deliverySliceRefs: [{
                  id: sliceId,
                  key: 'PAGE-DASHBOARD',
                  fanOutNodeId: 'pages',
                  blueprint: blueprintRevision,
                  pageSpec: proposal.baseRevision,
                }],
                proposalPins: [{
                  proposal: { id: proposal.id, payloadHash: proposal.payloadHash },
                  manifest: proposal.manifest,
                  producerNodeKey,
                  producerDefinitionNodeId: 'page-spec-generate',
                }],
              },
            }],
          },
        },
      },
      slices: {},
    },
    eventCursor: 2,
    startedBy: user.id,
    createdAt: now,
    updatedAt: now,
    nodes: [{
      id: 'run-page-spec-human-edit-node',
      runId: 'run-page-spec-human-edit',
      key: nodeKey,
      definitionNodeId: 'page-spec-edit',
      sliceId,
      type: 'human_edit' as const,
      status: 'waiting_input' as const,
      attempt: 1,
      allowedActions: ['submit_input'] as const,
      blockingReasons: [],
      availableAt: now,
      createdAt: now,
      updatedAt: now,
    }],
  }
}

function selectionWorkflowRun(id: string, rootBundleId = primaryBuildManifestId) {
  const exactRootBundleId = fixtureId(rootBundleId)
  const applicationBuild = {
    bundleIds: [exactRootBundleId],
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
      executionProfile: workflowExecutionProfile,
    },
    executionProfile: workflowExecutionProfile,
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
        allowedActions: [], blockingReasons: [],
        availableAt: now, createdAt: now, updatedAt: now, completedAt: now,
      },
      {
        id: `${id}-workbench`, runId: id, key: 'workbench', definitionNodeId: 'workbench',
        type: 'workbench_build', status: 'waiting_input', attempt: 1,
        allowedActions: ['submit_input'], blockingReasons: [],
        availableAt: now, createdAt: now, updatedAt: now,
      },
    ],
  }
}

function buildManifest(workflowRunId?: string, rootBuildManifestId?: string) {
  const anchoredBlueprint = { ...blueprintRevision, anchorId: 'page-orders' }
  return {
    id: primaryBuildManifestId,
    projectId: project.id,
    ...(workflowRunId ? {
      workflowRunId,
      manifestGroupKey: `${workflowRunId}-manifest-group`,
      deliverySliceId: 'page-orders',
    } : {}),
    ...(rootBuildManifestId ? { rootBuildManifestId: fixtureId(rootBuildManifestId) } : {}),
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

const freshCandidateFile = [
  "export default function Page() {",
  "  return <main>Template-backed Candidate</main>",
  "}",
  '',
].join('\n')

function freshCandidateWorkspace(buildManifestId: string) {
  const treeHash = `sha256:${hash('8')}`
  return {
    schemaVersion: 'candidate-workspace/v1',
    id: fixtureId(`candidate-${buildManifestId}`),
    projectId: project.id,
    repositorySnapshotId: fixtureId(`repository-snapshot-${buildManifestId}`),
    status: 'active' as const,
    buildManifest: { id: buildManifestId, contentHash: hash('6') },
    buildContract: {
      id: fixtureId(`build-contract-${buildManifestId}`),
      contentHash: hash('a'),
    },
    fullStackTemplate: {
      id: fullStackTemplateRegistration.template.id,
      contentHash: fullStackTemplateRegistration.template.contentHash,
    },
    // A fresh project is materialized directly from the qualified template.
    // It intentionally has no base WorkspaceRevision.
    baseTreeHash: treeHash,
    currentTree: {
      schemaVersion: 'repository-tree/v1',
      treeHash,
      files: [{
        path: 'frontend/app/page.tsx',
        mode: '100644',
        contentHash: contentHash(freshCandidateFile),
        byteSize: freshCandidateFile.length,
      }],
    },
    version: 1,
    journalSequence: 0,
    sessionEpoch: 1,
    writerLeaseEpoch: 0,
    dirty: false,
    conflicted: false,
    stale: false,
    rebaseRequired: false,
    lease: undefined as {
      ownerId: string
      epoch: number
      expiresAt: string
    } | undefined,
    createdBy: user.id,
    createdAt: now,
    updatedAt: now,
  }
}

async function repositorySnapshotReceiptFor(
  candidate: ReturnType<typeof freshCandidateWorkspace>,
) {
  const snapshot: RepositorySnapshotDto = {
    schemaVersion: REPOSITORY_SNAPSHOT_RECEIPT_SUBJECT_SCHEMA_VERSION,
    id: candidate.repositorySnapshotId,
    projectId: candidate.projectId,
    buildManifest: candidate.buildManifest,
    buildContract: candidate.buildContract,
    fullStackTemplate: candidate.fullStackTemplate,
    tree: {
      schemaVersion: REPOSITORY_SNAPSHOT_TREE_COMMITMENT_SCHEMA_VERSION,
      treeHash: candidate.baseTreeHash,
      contentObjectHash: `sha256:${hash('5')}`,
      fileCount: candidate.currentTree.files.length,
      byteSize: candidate.currentTree.files.reduce((total, file) => total + file.byteSize, 0),
    },
    templateReleases: ['api', 'web'].map((role, index) => ({
      role,
      mountPath: role === 'api' ? 'backend' : 'frontend',
      release: {
        id: fixtureId(`snapshot-${role}-release`),
        contentHash: `sha256:${hash(index === 0 ? '6' : '7')}`,
        subjectHash: `sha256:${hash(index === 0 ? '8' : '9')}`,
      },
      source: {
        repository: 'https://github.com/ai-worksflow/templates.git',
        branch: 'main',
        commit: String(index + 1).repeat(40),
        treeHash: `sha256:${hash(index === 0 ? 'a' : 'b')}`,
      },
      sbomDigest: `sha256:${hash(index === 0 ? 'c' : 'd')}`,
      signatureBundleDigest: `sha256:${hash(index === 0 ? 'e' : 'f')}`,
      authorityReceipt: {
        id: fixtureId(`snapshot-${role}-authority`),
        contentHash: `sha256:${hash(index === 0 ? '1' : '2')}`,
        policyHash: `sha256:${hash(index === 0 ? '3' : '4')}`,
      },
    })),
    createdBy: candidate.createdBy,
    createdAt: candidate.createdAt,
  }
  return {
    schemaVersion: REPOSITORY_SNAPSHOT_RECEIPT_SCHEMA_VERSION,
    contentHash: await computeRepositorySnapshotContentHash(snapshot),
    snapshot,
  }
}

function sandboxCandidateState(candidate: ReturnType<typeof freshCandidateWorkspace>) {
  return {
    id: candidate.id,
    repositorySnapshotId: candidate.repositorySnapshotId,
    status: candidate.status,
    baseTreeHash: candidate.baseTreeHash,
    treeHash: candidate.currentTree.treeHash,
    version: candidate.version,
    journalSequence: candidate.journalSequence,
    sessionEpoch: candidate.sessionEpoch,
    writerLeaseEpoch: candidate.writerLeaseEpoch,
    dirty: candidate.dirty,
    conflicted: candidate.conflicted,
    stale: candidate.stale,
    rebaseRequired: candidate.rebaseRequired,
    updatedAt: candidate.updatedAt,
  }
}

function readySandboxSession(candidate: ReturnType<typeof freshCandidateWorkspace>) {
  return {
    schemaVersion: 'sandbox-session/v1',
    id: fixtureId(`sandbox-${candidate.buildManifest.id}`),
    projectId: project.id,
    actorId: user.id,
    buildManifest: candidate.buildManifest,
    buildContract: candidate.buildContract,
    fullStackTemplate: candidate.fullStackTemplate,
    templateReleases: fullStackTemplateComponents.map((component) => ({
      id: component.release.id,
      contentHash: component.release.contentHash,
    })),
    runnerImageDigest: `sha256:${hash('1')}`,
    candidate: sandboxCandidateState(candidate),
    sessionEpoch: candidate.sessionEpoch,
    state: 'ready' as const,
    version: 1,
    ttl: {
      policy: { idleHibernateAfter: 900, maxRuntime: 7200 },
      idleDeadline: '2026-07-10T08:15:00Z',
      expiresAt: '2026-07-10T10:00:00Z',
    },
    quota: {
      cpuMillis: 1000,
      memoryBytes: 1_073_741_824,
      workspaceBytes: 536_870_912,
      pidLimit: 256,
      previewPortLimit: 4,
    },
    allowedServices: [{
      id: 'web',
      kind: 'web',
      profiles: ['dev'],
      templateRelease: {
        id: fullStackTemplateComponents[0].release.id,
        contentHash: fullStackTemplateComponents[0].release.contentHash,
      },
    }],
    allowedPorts: [{ name: 'web', serviceId: 'web', number: 3000, protocol: 'http' }],
    allowedActions: [
      'view',
      'edit',
      'pty',
      'process',
      'agent',
      'checkpoint',
      'verify',
      'suspend',
      'terminate',
    ],
    blockingReasons: [],
    lastTransition: { to: 'ready' as const, reason: 'Bootstrap complete', at: now },
    createdAt: now,
    updatedAt: now,
  }
}

function sandboxFenceHeaders(
  session: ReturnType<typeof readySandboxSession>,
  candidate: ReturnType<typeof freshCandidateWorkspace> | null,
) {
  const current = candidate ?? session.candidate
  return {
    etag: `"sandbox:${session.id}:${session.version}"`,
    'x-sandbox-session-etag': `"sandbox:${session.id}:${session.version}"`,
    'x-sandbox-session-epoch': String(session.sessionEpoch),
    'x-candidate-version': String(current.version),
    'x-candidate-id': current.id,
    'x-candidate-journal-sequence': String(current.journalSequence),
    'x-writer-lease-epoch': String(current.writerLeaseEpoch),
    'x-candidate-tree-hash': candidate
      ? candidate.currentTree.treeHash
      : session.candidate.treeHash,
  }
}

function implementationProposal(
  status: 'open' | 'ready' | 'applied',
  decision: 'pending' | 'accepted' | 'applied' = 'pending',
  version = 1,
) {
  return {
    id: primaryProposalId,
    projectId: project.id,
    buildManifestId: primaryBuildManifestId,
    applicationBuildContract: {
      id: fixtureId(`build-contract-${primaryBuildManifestId}`),
      contractHash: hash('b'),
    },
    baseWorkspaceRevision: undefined as {
      artifactId: string
      revisionId: string
      contentHash: string
      anchorId?: string
    } | undefined,
    executionSource: 'manual_submission' as 'manual_submission' | 'manual_generation' | 'workflow_runner' | 'conversation_command' | 'candidate_freeze',
    conversationCommandId: undefined as string | undefined,
    instructionHash: undefined as string | undefined,
    candidateSource: undefined as {
      freezeReceiptId: string
      repositorySnapshotId: string
      sessionId: string
      candidateId: string
      candidateSnapshotId: string
      candidateVersion: number
      journalSequence: number
      sessionEpoch: number
      writerLeaseEpoch: number
      baseTreeHash: string
      treeHash: string
      fullStackTemplate: { id: string; contentHash: string }
      verificationReceipt: { id: string; contentHash: string }
    } | undefined,
    operations: [{
      id: 'operation-1',
      kind: 'file.upsert',
      path: 'src/index.html',
      content: '<!doctype html><html><body><h1>SERVER_APPLIED_APPLICATION</h1></body></html>',
      language: 'html',
      mode: '100644',
      rationale: 'Render the approved flow.',
      traceSource: [approvedPrototypeRevision.id],
      decision,
      ...(decision !== 'pending' ? { decidedBy: user.id } : {}),
    }],
    routes: [],
    apis: [],
    migrations: [],
    tests: [],
    previews: [],
    traceLinks: [] as Array<Record<string, string>>,
    diagnostics: [] as Array<{
      code: string
      severity: 'info' | 'warning' | 'error' | 'blocker'
      message: string
      path?: string
      sourceId?: string
    }>,
    assumptions: [],
    unimplementedItems: [] as string[],
    status,
    version,
    payloadHash: hash('4'),
    createdBy: user.id,
    createdAt: now,
    ...(status === 'applied' ? { appliedAt: now } : {}),
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

function pageSpecArtifactProposal(id: string, status: 'ready' | 'applied' = 'ready') {
  const decision: 'accepted' | 'applied' = status === 'applied' ? 'applied' : 'accepted'
  return {
    id,
    projectId: project.id,
    artifactId: pageSpec.artifact.id,
    manifest: { id: `${id}-manifest`, hash: hash('8') },
    baseRevision: exactRevision(
      pageSpecRevision.artifactId,
      pageSpecRevision.id,
      pageSpecRevision.revisionNumber,
      pageSpec.draft.contentHash,
    ),
    payloadHash: hash('9'),
    status,
    operations: [
      {
        id: `${id}-operation-goal`,
        kind: 'replace' as const,
        path: '/userGoal',
        value: 'Inspect order health and resolve exceptions.',
        rationale: 'Make the reviewed workflow goal explicit.',
        decision,
      },
      {
        id: `${id}-operation-acceptance`,
        kind: 'add' as const,
        path: '/acceptanceCriterionIds',
        value: ['AC-DASHBOARD-001'],
        rationale: 'Preserve exact acceptance traceability.',
        decision,
      },
    ],
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
      [home.id]: home.id,
      [checkout.id]: checkout.id,
    },
    currentProposalIds: {
      [home.id]: proposal.id,
      [checkout.id]: undefined,
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
    id: fixtureId(id),
    deliverySliceId,
    workflowContext: base.workflowContext
      ? { ...base.workflowContext, deliverySliceId }
      : undefined,
    ...(rootBuildManifestId ? { rootBuildManifestId: fixtureId(rootBuildManifestId) } : {}),
    ...(derivedFromBuildManifestId ? { derivedFromBuildManifestId: fixtureId(derivedFromBuildManifestId) } : {}),
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
    id: fixtureId(id),
    buildManifestId: fixtureId(buildManifestId),
    applicationBuildContract: {
      id: fixtureId(`build-contract-${fixtureId(buildManifestId)}`),
      contractHash: hash('b'),
    },
    operations: [{
      id: `${id}-operation`,
      kind: 'file.upsert' as const,
      path: 'src/shared.ts',
      content: id === 'proposal-home'
        ? "export const currentPage = 'home'\n"
        : "export const currentPage = 'checkout'\n",
      language: 'typescript',
      ...(expectedHash ? { expectedHash } : {}),
      rationale,
      traceSource: [approvedPrototypeRevision.id],
      decision,
      ...(decision !== 'pending' ? { decidedBy: user.id } : {}),
    }],
    payloadHash: id === 'proposal-home' ? hash('4') : hash('5'),
    ...(status === 'applied' ? { appliedAt: now } : {}),
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
    definition: {
      id: workflowDefinition.id,
      version: 2,
      hash: workflowDefinition.contentHash,
      executionProfile: workflowExecutionProfile,
    },
    executionProfile: workflowExecutionProfile,
    inputManifest: { id: 'manifest-1', hash: hash('1') },
    status,
    scope: {},
    context: {
      values: {
        buildManifest: {
          bundleIds: [fixtureId('bundle-home'), fixtureId('bundle-checkout')],
          sliceIds: ['page-home', 'page-checkout'],
        },
      },
      nodes: {
        workbench: {
          output: {
            implementationProposals: [{
              bundleId: fixtureId('bundle-home'),
              proposalId: fixtureId('proposal-home'),
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
      allowedActions: status === 'waiting_input' ? ['submit_input'] : [],
      blockingReasons: [],
      availableAt: now,
      createdAt: now,
      updatedAt: now,
    }],
  }
}

function multiGroupWorkflowRun() {
  const manifestA = {
    bundleIds: [fixtureId('bundle-group-a')],
    sliceIds: ['slice-group-a'],
    manifestGroupKey: 'manifest-group-a',
    hash: hash('a'),
  }
  const manifestB = {
    bundleIds: [fixtureId('bundle-group-b')],
    sliceIds: ['slice-group-b'],
    manifestGroupKey: 'manifest-group-b',
    hash: hash('b'),
  }
  return {
    id: 'run-groups',
    projectId: project.id,
    definitionVersionId: workflowDefinition.versionId,
    definition: {
      id: workflowDefinition.id,
      version: 2,
      hash: workflowDefinition.contentHash,
      executionProfile: workflowExecutionProfile,
    },
    executionProfile: workflowExecutionProfile,
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
              bundleId: fixtureId('bundle-group-a'),
              proposalId: fixtureId('proposal-group-a'),
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
              bundleId: fixtureId('bundle-group-b'),
              proposalId: fixtureId('proposal-group-b'),
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
        allowedActions: ['submit_input'] as const,
        blockingReasons: [],
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
        allowedActions: ['submit_input'] as const,
        blockingReasons: [],
        availableAt: now,
        createdAt: now,
        updatedAt: now,
      },
    ],
  }
}
