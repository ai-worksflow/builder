'use client'

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type SetStateAction,
} from 'react'
import {
  BUILD_SUBSTATUS,
  BUILD_TASKS,
  DEFAULT_BLUEPRINT,
  DOC_DEPENDENCIES,
  IMPORT_ASSETS,
  NODE_BINDINGS,
  RECENT_PROJECTS,
  TODO_TASKS,
  TEAM_DOCUMENTS,
  USER_PROMPT,
  VERSIONS,
} from './mock-data'
import {
  BLUEPRINT_DOC_OUTPUTS,
  INITIAL_TEAM_PROJECTS,
  bindingKindForBlueprintNode,
  blankBlueprint,
  blueprintFromDocuments,
  buildTeamProject,
  cloneBlueprint,
  cloneDependencies,
  cloneDocuments,
  cloneImportAssets,
  cloneNodeBindings,
  collectBlueprintSelection,
  computeBlueprintNodeMissing,
  createBlueprintWorkbenchContextDraft,
  docsFromBlueprint,
  generatedBlueprint,
  syncWorkbenchResultToBlueprint,
  syncWorkbenchResultToDocuments,
  unique,
} from './project-model'
import { createInitialWorkspace } from './initial-workspace'
import {
  PROJECT_CATALOG_VERSION,
  addProjectAttachment as addCatalogAttachment,
  addProjectToCatalog,
  cloneProject as cloneCatalogProject,
  createBlankProject,
  createProjectFromImport,
  createProjectFromTemplate,
  createProjectCatalog,
  createProjectCatalogMigration,
  deleteProject as deleteCatalogProject,
  isProjectCatalog,
  isStrictVirtualWorkspace,
  migrateProjectCatalog,
  recordDeployment as recordCatalogDeployment,
  recordGeneration as recordCatalogGeneration,
  recordProjectVersion as recordCatalogVersion,
  removeProjectAttachment as removeCatalogAttachment,
  renameProject as renameCatalogProject,
  selectProject as selectCatalogProject,
  toggleProjectStar as toggleCatalogProjectStar,
  updateDatabaseSettings as updateCatalogDatabaseSettings,
  updateGithubSettings as updateCatalogGithubSettings,
  updateProjectWorkspace,
  type ProjectCatalog,
  type ProductProject,
} from './project-catalog'
import { usePersistentState, type PersistenceHydrationStatus } from './use-persistent-state'
import {
  createCheckpoint,
  createBranch as createWorkspaceBranchInState,
  deleteFile as deleteWorkspaceFileFromState,
  derivePreviewDocument,
  renameFile as renameWorkspaceFileInState,
  restoreCheckpoint,
  setWorkspaceDiagnostics,
  upsertFile,
  type PreviewDocument,
  type VirtualWorkspace,
} from './workspace-model'
import { attachmentSetIssue, type ComposerAttachment } from './composer-context'
import { isTeamProjectList } from './team-persistence'
import {
  DEFAULT_WORKSFLOW_PREFERENCES,
  isWorksflowPreferences,
  updatePreferences,
  type WorksflowPreferences,
} from './preferences'
import {
  BUILT_IN_PROMPT_TEMPLATES,
  BUILT_IN_PROMPT_WORKFLOWS,
  addPromptHistoryEntry,
  isPromptHistory,
  isPromptTemplateList,
  isPromptWorkflowList,
  redactSensitivePrompt,
  type PromptHistoryEntry,
  type PromptTemplate,
  type PromptWorkflow,
} from './prompt-library'
import { GenerationClientError, streamGeneration } from '@/lib/generation/client'
import { useI18n } from '@/lib/i18n'
import { generateLocalWorkspace } from '@/lib/generation/local-generator'
import { parseGenerationRequest } from '@/lib/generation/schema'
import type {
  GenerationEvent,
  GenerationCost,
  GenerationErrorCategory,
  GenerationLimits,
  GenerationLifecycleEvent,
  GenerationMode,
  GenerationPlan,
  GenerationProvider,
  GenerationUsage,
} from '@/lib/generation/types'
import { qualityResultAsPromptContext, requestQualityRun } from '@/lib/quality/client'
import type { QualityRunResult } from '@/lib/quality/types'
import {
  downloadBlob,
  exportWorkspaceArchive,
  listDeployments,
  publishWorkspace,
  rollbackDeployment as requestDeploymentRollback,
  type DeploymentMetadata,
} from '@/lib/delivery/client'
import { parseWorksflowRoute } from './route-state'
import type {
  BindingTargetKind,
  Blueprint,
  BlueprintEdge,
  BlueprintEdgeType,
  BlueprintNode,
  BlueprintNodeType,
  BlueprintOperation,
  BlueprintWorkbenchContext,
  BuildTask,
  DependencyType,
  DocMemberRole,
  DocType,
  DocStatus,
  DocumentDependency,
  FollowUpRequest,
  ImportAsset,
  NodeBinding,
  Phase,
  ProjectRecord,
  ProjectVersion,
  Surface,
  TeamView,
  TeamDocument,
  TeamProject,
  TeamProjectSource,
  TodoTask,
  WorkbenchView,
} from './types'

export { teamPathFor, workbenchPathFor } from './route-state'
export type { TeamView } from './types'

const INITIAL_GENERATION_RESULT = generateLocalWorkspace(
  parseGenerationRequest({
    prompt: USER_PROMPT,
    mode: 'plan',
    currentFiles: [],
  }),
)

function createInitialProjectCatalog(): ProjectCatalog {
  const initialWorkspace = createInitialWorkspace()
  const projects = RECENT_PROJECTS.map((project) =>
    createBlankProject({
      id: project.id,
      workspaceId: `workspace-${project.id}`,
      name: project.name,
      teamId: 'acme',
      teamName: project.teamName,
      starred: project.starred,
      lifecycleStatus: 'active',
      files: initialWorkspace.files,
    }),
  )
  return createProjectCatalog({ projects, selectedProjectId: projects[0].id })
}

interface WorksflowState {
  // global
  routeReady: boolean
  surface: Surface
  setSurface: (s: Surface) => void
  projectName: string
  setProjectName: (name: string) => void
  projects: ProjectRecord[]
  openProject: (id: string) => void
  toggleProjectStar: (id: string) => void
  duplicateProject: () => void
  deleteProductProject: (id?: string) => void
  createProductProject: (name?: string, source?: 'blank' | 'template') => string
  cloneProductProject: (id: string) => string
  renameProductProject: (id: string, name: string) => void
  importProductProject: (value: unknown, name?: string) => string
  selectedProductProjectId: string
  selectPlatformProject: (project: { readonly id: string; readonly name: string }) => void
  productProject: ProductProject
  updateGithubProjectSettings: (settings: unknown) => void
  updateDatabaseProjectSettings: (settings: unknown) => void

  // workbench
  phase: Phase
  view: WorkbenchView
  setView: (v: WorkbenchView) => void
  tasks: BuildTask[]
  versions: ProjectVersion[]
  followUps: FollowUpRequest[]
  toggleVersionStar: (id: string) => void
  startBuild: () => void
  stopBuild: () => void
  resetWorkbench: () => void
  submitPrompt: (text: string) => void
  retryGeneration: () => void
  isGenerating: boolean
  planMode: boolean
  setPlanMode: (v: boolean) => void
  linkedDocsOpen: boolean
  composerDraft: string
  setComposerDraft: (text: string) => void
  requestDatabaseSetup: () => void
  workspace: VirtualWorkspace
  previewDocument: PreviewDocument
  selectedWorkspaceFile: string
  setSelectedWorkspaceFile: (path: string) => void
  updateWorkspaceFile: (path: string, content: string, dirty?: boolean) => void
  createWorkspaceFile: (path: string, content?: string) => void
  deleteWorkspaceFile: (path: string) => void
  renameWorkspaceFile: (fromPath: string, toPath: string) => void
  createWorkspaceCheckpoint: (label?: string, message?: string) => void
  createWorkspaceBranch: (name: string, checkpointId?: string) => void
  restoreWorkspaceCheckpoint: (id: string) => void
  undoWorkspaceRestore: () => void
  canUndoWorkspaceRestore: boolean
  workspaceHydrationStatus: PersistenceHydrationStatus
  workspacePersistenceError?: string
  workspaceIsSaving: boolean
  workspaceLastSavedAt?: number
  resetWorkspacePersistence: () => void
  workspaceHasExternalConflict: boolean
  resolveWorkspaceExternalConflict: (strategy: 'keep-local' | 'use-external') => void
  attachments: ComposerAttachment[]
  addAttachment: (attachment: ComposerAttachment) => void
  removeAttachment: (id: string) => void
  toggleAttachmentIncluded: (id: string) => void
  clearAttachments: () => void
  generationPlan: GenerationPlan | null
  generationEvents: GenerationEvent[]
  generationLifecycleEvents: GenerationLifecycleEvent[]
  generationSummary: string
  generationError: string | null
  generationErrorCode: string | null
  generationErrorStatus?: number
  generationErrorRetryable: boolean
  generationErrorCategory?: GenerationErrorCategory
  generationErrorRetryAfterSeconds?: number
  generationErrorAction?: string
  generationProvider: GenerationProvider | null
  generationModel: string
  setGenerationModel: (model: string) => void
  generationMode: Exclude<GenerationMode, 'plan'>
  setGenerationMode: (mode: Exclude<GenerationMode, 'plan'>) => void
  generationUsage: GenerationUsage | null
  generationDurationMs: number
  generationCost: GenerationCost | null
  generationLimits: GenerationLimits | null
  preferences: WorksflowPreferences
  updateUserPreferences: (patch: Partial<WorksflowPreferences>) => void
  promptHistory: PromptHistoryEntry[]
  promptTemplates: PromptTemplate[]
  promptWorkflows: PromptWorkflow[]
  savePromptTemplate: (template: PromptTemplate) => void
  deletePromptTemplate: (id: string) => void
  savePromptWorkflow: (workflow: PromptWorkflow) => void
  deletePromptWorkflow: (id: string) => void
  qualityRun: QualityRunResult | null
  qualityRunning: boolean
  qualityError: string | null
  runWorkspaceQuality: () => Promise<QualityRunResult | null>
  attachQualityDiagnostics: () => void
  deliveryStatus: 'idle' | 'exporting' | 'publishing' | 'rollingBack' | 'error'
  deliveryError: string | null
  deliveryLogs: string[]
  deployments: DeploymentMetadata[]
  publishedUrl: string | null
  exportWorkspace: () => Promise<boolean>
  publishCurrentWorkspace: (
    message?: string,
    environment?: 'preview' | 'production',
  ) => Promise<DeploymentMetadata | null>
  refreshDeployments: () => Promise<DeploymentMetadata[]>
  rollbackDeployment: (deploymentId: string, versionId: string) => Promise<boolean>

  // preview todo app
  todos: TodoTask[]
  toggleTodo: (id: string) => void
  addTodo: (title: string, priority: TodoTask['priority']) => void
  todoFilter: 'all' | 'active' | 'completed'
  setTodoFilter: (f: 'all' | 'active' | 'completed') => void

  // team
  teamView: TeamView
  setTeamView: (v: TeamView) => void
  teamProjects: TeamProject[]
  activeTeamProjectId: string
  activeTeamProject: TeamProject
  platformTeamFactsStatus: 'idle' | 'loading' | 'ready' | 'error'
  platformTeamFactsError: string | null
  beginPlatformTeamFacts: (projectId: string) => void
  applyPlatformTeamFacts: (input: {
    readonly projectId: string
    readonly documents: TeamDocument[]
    readonly dependencies: DocumentDependency[]
    readonly blueprint: Blueprint
  }) => void
  failPlatformTeamFacts: (projectId: string, message: string) => void
  openTeamProject: (id: string) => void
  createTeamProject: (name?: string, source?: TeamProjectSource) => string
  selectedDocId: string | null
  setSelectedDocId: (id: string | null) => void
  openDoc: (id: string) => void
  createDocument: (type: DocType, title?: string, status?: DocStatus) => string
  generateDocumentChain: () => void
  createBlankDocumentGraph: () => void
  createDocumentGraphFromTemplate: () => void
  createDocumentGraphFromBlueprint: () => void
  documents: TeamDocument[]
  moveDocumentNode: (id: string, position: { x: number; y: number }) => void
  dependencies: DocumentDependency[]
  nodeBindings: NodeBinding[]
  importAssets: ImportAsset[]
  linkedDocIds: string[]
  blueprint: Blueprint
  blueprintOperations: BlueprintOperation[]
  activeBlueprintContext: BlueprintWorkbenchContext | null
  useDocInWorkbench: (id: string) => void
  toggleLinkedDoc: (id: string) => void
  addDocumentDependency: (
    sourceDocId: string,
    targetDocId: string,
    type: DependencyType,
    isBlocking: boolean,
  ) => void
  addNodeBinding: (
    binding: Omit<NodeBinding, 'id' | 'sourceKind' | 'createdAt'> & {
      sourceKind?: BindingTargetKind
    },
  ) => void
  createBlueprintNode: (
    type: BlueprintNodeType,
    title: string,
    position?: { x: number; y: number },
  ) => string
  updateBlueprintNode: (
    id: string,
    updates: Partial<
      Pick<
        BlueprintNode,
        | 'title'
        | 'description'
        | 'type'
        | 'boundDocumentIds'
        | 'boundMemberIds'
        | 'boundPrototypeArtifactIds'
        | 'generatedDocIds'
        | 'missing'
      >
    >,
  ) => void
  moveBlueprintNode: (id: string, position: { x: number; y: number }) => void
  deleteBlueprintNode: (id: string) => void
  createBlueprintEdge: (
    sourceNodeId: string,
    targetNodeId: string,
    type: BlueprintEdgeType,
    isRequired?: boolean,
  ) => void
  updateBlueprintEdge: (
    id: string,
    updates: Partial<Pick<BlueprintEdge, 'type' | 'isRequired'>>,
  ) => void
  deleteBlueprintEdge: (id: string) => void
  saveBlueprint: () => void
  validateBlueprint: () => void
  completeBlueprintNode: (id: string) => void
  startBlankBlueprint: () => void
  generateBlueprintFromProjectBrief: (brief?: string) => void
  generateBlueprintFromExistingDocs: () => void
  generateDocsFromBlueprintSelection: (selectedNodeId?: string) => string[]
  createWorkbenchContextFromBlueprint: (selectedNodeId?: string) => void
  updateDocumentStatus: (id: string, status: DocStatus) => void
  addDocumentMember: (docId: string, userId: string, role: DocMemberRole) => void
  removeDocumentMember: (docId: string, userId: string, role: DocMemberRole) => void
  saveDocumentDraft: (id: string) => void
  syncWorkbenchBackToDocs: () => void
  syncImportAsset: (id: string) => void
  detachImportAsset: (id: string) => void
  selectedBlueprintNodeId: string | null
  setSelectedBlueprintNodeId: (id: string | null) => void

  // cross surface
  goToWorkbenchFromDoc: () => void
}

const WorksflowContext = createContext<WorksflowState | null>(null)

export function WorksflowProvider({ children }: { children: ReactNode }) {
  const { locale, t } = useI18n()
  const [surface, setSurface] = useState<Surface>('workbench')
  const [routeReady, setRouteReady] = useState(false)
  const projectCatalogPersistence = usePersistentState({
    key: 'worksflow.projectCatalog',
    version: PROJECT_CATALOG_VERSION,
    initialValue: createInitialProjectCatalog,
    validate: isProjectCatalog,
    migrate: createProjectCatalogMigration(),
    debounceMs: 350,
  })
  const projectCatalog = projectCatalogPersistence.value
  const setProjectCatalog = projectCatalogPersistence.setValue
  const selectedProductProjectId = projectCatalog.selectedProjectId
  const activeProductProject =
    projectCatalog.projects.find((project) => project.id === projectCatalog.selectedProjectId) ??
    projectCatalog.projects[0]
  const projectName = activeProductProject.name
  useEffect(() => {
    if (
      projectCatalogPersistence.hydrationStatus === 'hydrated' &&
      !Object.isFrozen(projectCatalog)
    ) {
      setProjectCatalog(migrateProjectCatalog(projectCatalog))
    }
  }, [projectCatalog, projectCatalogPersistence.hydrationStatus, setProjectCatalog])
  const projects = useMemo<ProjectRecord[]>(
    () =>
      projectCatalog.projects.map((project) => {
        const latestRun = project.generationRuns.at(-1)
        const latestVersion = project.latestVersionId
          ? project.versions.find((version) => version.id === project.latestVersionId)
          : project.versions.at(-1)
        return {
          id: project.id,
          name: project.name,
          teamName: project.teamName,
          phase: latestRun?.status ?? project.lifecycleStatus,
          updatedAt: project.updatedAt,
          starred: project.starred,
          linkedDocs: project.teamReferences.documents.length,
          latestVersion: latestVersion?.label ?? t('core.store.workspace'),
        }
      }),
    [projectCatalog.projects, t],
  )
  const setProjectName = useCallback(
    (name: string) => {
      const normalized = name.trim()
      if (!normalized) return
      setProjectCatalog((catalog) =>
        renameCatalogProject(catalog, catalog.selectedProjectId, normalized),
      )
    },
    [setProjectCatalog],
  )

  // Workbench begins in planning, then auto advances to planReady.
  const [phase, setPhase] = useState<Phase>('planning')
  const [view, setView] = useState<WorkbenchView>('preview')
  const [tasks, setTasks] = useState<BuildTask[]>(BUILD_TASKS)
  const [versions, setVersions] = useState<ProjectVersion[]>(VERSIONS.slice(0, 1))
  const [followUps, setFollowUps] = useState<FollowUpRequest[]>([])
  const [planMode, setPlanMode] = useState(true)
  const [composerDraft, setComposerDraft] = useState('')
  const workspace = activeProductProject.workspace
  const setWorkspace = useCallback(
    (nextWorkspace: SetStateAction<VirtualWorkspace>) => {
      setProjectCatalog((catalog) =>
        updateProjectWorkspace(catalog, catalog.selectedProjectId, (current) =>
          typeof nextWorkspace === 'function'
            ? nextWorkspace(current)
            : nextWorkspace,
        ),
      )
    },
    [setProjectCatalog],
  )
  const [selectedWorkspaceFile, setSelectedWorkspaceFile] = useState('src/App.tsx')
  const [restoreRecoveryCheckpointId, setRestoreRecoveryCheckpointId] = useState<string | null>(null)
  const [attachments, setAttachments] = useState<ComposerAttachment[]>([])
  const [generationPlan, setGenerationPlan] = useState<GenerationPlan | null>(
    INITIAL_GENERATION_RESULT.plan,
  )
  const [generationEvents, setGenerationEvents] = useState<GenerationEvent[]>([])
  const [generationLifecycleEvents, setGenerationLifecycleEvents] =
    useState<GenerationLifecycleEvent[]>([])
  const [generationSummary, setGenerationSummary] = useState(INITIAL_GENERATION_RESULT.summary)
  const [generationError, setGenerationError] = useState<string | null>(null)
  const [generationErrorCode, setGenerationErrorCode] = useState<string | null>(null)
  const [generationErrorStatus, setGenerationErrorStatus] = useState<number>()
  const [generationErrorRetryable, setGenerationErrorRetryable] = useState(false)
  const [generationErrorCategory, setGenerationErrorCategory] = useState<GenerationErrorCategory>()
  const [generationErrorRetryAfterSeconds, setGenerationErrorRetryAfterSeconds] = useState<number>()
  const [generationErrorAction, setGenerationErrorAction] = useState<string>()
  const [generationProvider, setGenerationProvider] = useState<GenerationProvider | null>('local')
  const preferencesPersistence = usePersistentState({
    key: 'worksflow.preferences',
    version: 1,
    initialValue: DEFAULT_WORKSFLOW_PREFERENCES,
    validate: isWorksflowPreferences,
    debounceMs: 250,
  })
  const preferences = preferencesPersistence.value
  const setPreferences = preferencesPersistence.setValue
  const generationModel = preferences.generationModel
  const generationMode = preferences.generationMode
  const updateUserPreferences = useCallback(
    (patch: Partial<WorksflowPreferences>) => {
      setPreferences((current) => updatePreferences(current, patch))
    },
    [setPreferences],
  )
  const setGenerationModel = useCallback(
    (model: string) => updateUserPreferences({ generationModel: model }),
    [updateUserPreferences],
  )
  const setGenerationMode = useCallback(
    (mode: Exclude<GenerationMode, 'plan'>) => updateUserPreferences({ generationMode: mode }),
    [updateUserPreferences],
  )
  const [generationUsage, setGenerationUsage] = useState<GenerationUsage | null>(
    INITIAL_GENERATION_RESULT.usage ?? null,
  )
  const [generationDurationMs, setGenerationDurationMs] = useState(0)
  const [generationCost, setGenerationCost] = useState<GenerationCost | null>(null)
  const [generationLimits, setGenerationLimits] = useState<GenerationLimits | null>(null)
  const [qualityRun, setQualityRun] = useState<QualityRunResult | null>(null)
  const [qualityRunning, setQualityRunning] = useState(false)
  const [qualityError, setQualityError] = useState<string | null>(null)
  const [deliveryStatus, setDeliveryStatus] = useState<WorksflowState['deliveryStatus']>('idle')
  const [deliveryError, setDeliveryError] = useState<string | null>(null)
  const [deliveryLogs, setDeliveryLogs] = useState<string[]>([])
  const [deployments, setDeployments] = useState<DeploymentMetadata[]>([])
  const [publishedUrl, setPublishedUrl] = useState<string | null>(null)
  const promptHistoryPersistence = usePersistentState({
    key: 'worksflow.promptHistory',
    version: 2,
    initialValue: [] as PromptHistoryEntry[],
    validate: isPromptHistory,
    migrate: (value) =>
      Array.isArray(value)
        ? value.map((entry) =>
            entry && typeof entry === 'object' && typeof entry.prompt === 'string'
              ? { ...entry, prompt: redactSensitivePrompt(entry.prompt) }
              : entry,
          )
        : value,
    debounceMs: 250,
  })
  const customTemplatePersistence = usePersistentState({
    key: 'worksflow.promptTemplates',
    version: 2,
    initialValue: [] as PromptTemplate[],
    validate: isPromptTemplateList,
    migrate: (value) =>
      Array.isArray(value)
        ? value.map((template) =>
            template && typeof template === 'object' && typeof template.prompt === 'string'
              ? { ...template, prompt: redactSensitivePrompt(template.prompt) }
              : template,
          )
        : value,
    debounceMs: 250,
  })
  const customWorkflowPersistence = usePersistentState({
    key: 'worksflow.promptWorkflows',
    version: 1,
    initialValue: [] as PromptWorkflow[],
    validate: isPromptWorkflowList,
    debounceMs: 250,
  })
  const promptHistory = promptHistoryPersistence.value
  const setPromptHistory = promptHistoryPersistence.setValue
  const promptTemplates = useMemo(
    () => [...BUILT_IN_PROMPT_TEMPLATES, ...customTemplatePersistence.value],
    [customTemplatePersistence.value],
  )
  const promptWorkflows = useMemo(
    () => [...BUILT_IN_PROMPT_WORKFLOWS, ...customWorkflowPersistence.value],
    [customWorkflowPersistence.value],
  )

  const [todos, setTodos] = useState<TodoTask[]>(TODO_TASKS)
  const [todoFilter, setTodoFilter] = useState<'all' | 'active' | 'completed'>('all')

  const [teamView, setTeamView] = useState<TeamView>('dashboard')
  const teamProjectsPersistence = usePersistentState({
    key: 'worksflow.teamProjects',
    version: 1,
    initialValue: INITIAL_TEAM_PROJECTS,
    validate: isTeamProjectList,
    debounceMs: 450,
  })
  const teamProjects = teamProjectsPersistence.value
  const setTeamProjects = teamProjectsPersistence.setValue
  const [activeTeamProjectId, setActiveTeamProjectId] = useState(INITIAL_TEAM_PROJECTS[0].id)
  const [selectedDocId, setSelectedDocId] = useState<string | null>('d3')
  const [selectedBlueprintNodeId, setSelectedBlueprintNodeId] = useState<string | null>('b1')
  const [documents, setDocuments] = useState<TeamDocument[]>(INITIAL_TEAM_PROJECTS[0].documents)
  const [dependencies, setDependencies] = useState<DocumentDependency[]>(INITIAL_TEAM_PROJECTS[0].dependencies)
  const [nodeBindings, setNodeBindings] = useState<NodeBinding[]>(INITIAL_TEAM_PROJECTS[0].nodeBindings)
  const [importAssets, setImportAssets] = useState<ImportAsset[]>(INITIAL_TEAM_PROJECTS[0].importAssets)
  const [linkedDocIds, setLinkedDocIds] = useState<string[]>(INITIAL_TEAM_PROJECTS[0].linkedDocIds)
  const [blueprint, setBlueprint] = useState<Blueprint>(INITIAL_TEAM_PROJECTS[0].blueprint)
  const [platformTeamFactsStatus, setPlatformTeamFactsStatus] =
    useState<'idle' | 'loading' | 'ready' | 'error'>('idle')
  const [platformTeamFactsError, setPlatformTeamFactsError] = useState<string | null>(null)
  const platformTeamFactsProjectId = useRef<string | null>(null)
  const [blueprintOperations, setBlueprintOperations] = useState<BlueprintOperation[]>([])
  const [activeBlueprintContext, setActiveBlueprintContext] =
    useState<BlueprintWorkbenchContext | null>(null)

  const generationAbort = useRef<AbortController | null>(null)
  const activeGenerationRunId = useRef<string | null>(null)
  const lastGenerationRequest = useRef<{ prompt: string; mode: GenerationMode } | null>(null)
  const importTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const skipTeamProjectSync = useRef(false)
  const teamCatalogLoaded = useRef(false)
  const versionSerial = useRef(2)
  const selectedProductProjectIdRef = useRef(selectedProductProjectId)
  selectedProductProjectIdRef.current = selectedProductProjectId

  const isGenerating = phase === 'planning' || phase === 'building'
  const previewDocument = useMemo(() => derivePreviewDocument(workspace), [workspace])
  const activeTeamProject =
    teamProjects.find((project) => project.id === activeTeamProjectId) ?? teamProjects[0]

  useEffect(() => {
    const persistedVersions = activeProductProject.versions.map((version) => ({
      id: version.id,
      title: version.label,
      subtitle: `${version.description ?? t('core.store.workspaceCheckpoint')} · ${new Date(version.createdAt).toLocaleString(locale)}`,
      starred: false,
    }))
    setVersions(persistedVersions.length > 0 ? persistedVersions : VERSIONS.slice(0, 1))
    versionSerial.current = Math.max(2, activeProductProject.versions.length + 2)
  }, [activeProductProject.id, activeProductProject.versions, locale, t])

  useEffect(() => {
    const latestRun = activeProductProject.generationRuns.at(-1)
    setRestoreRecoveryCheckpointId(null)
    setAttachments([])
    setQualityRun(null)
    setQualityError(null)
    setDeliveryError(null)
    setPublishedUrl(null)
    setDeployments([])
    setGenerationProvider(latestRun?.provider ?? null)
    setGenerationUsage(
      latestRun?.totalTokens === undefined
        ? null
        : {
            inputTokens: latestRun.inputTokens ?? 0,
            outputTokens: latestRun.outputTokens ?? 0,
            totalTokens: latestRun.totalTokens,
            estimated: true,
          },
    )
    setGenerationDurationMs(latestRun?.durationMs ?? 0)
    setGenerationCost(
      latestRun?.costUsd === undefined
        ? null
        : { currency: 'USD', amount: latestRun.costUsd, estimated: true, configured: true },
    )
    setGenerationLimits(
      latestRun?.maxTokens === undefined
        ? null
        : { maxTotalTokens: latestRun.maxTokens, maxOutputTokens: latestRun.maxTokens },
    )
  }, [activeProductProject.id])

  useEffect(() => {
    if (workspace.files.some((file) => file.path === selectedWorkspaceFile)) return
    setSelectedWorkspaceFile(workspace.files[0]?.path ?? '')
  }, [selectedWorkspaceFile, workspace.files])

  const recordBlueprintOperation = (type: BlueprintOperation['type'], summary: string) => {
    setBlueprintOperations((prev) => [
      {
        id: `op${Date.now()}`,
        type,
        blueprintId: blueprint.id,
        summary,
        createdAt: new Date().toISOString(),
      },
      ...prev,
    ].slice(0, 8))
  }

  useEffect(() => {
    if (
      teamProjectsPersistence.hydrationStatus === 'hydrating' ||
      !teamCatalogLoaded.current
    ) return
    // Platform artifacts are collaborative server facts. Their in-memory legacy
    // projection may feed older dashboard/graph views, but must never become a
    // localStorage source of truth.
    if (platformTeamFactsProjectId.current === activeTeamProjectId) return
    if (skipTeamProjectSync.current) {
      skipTeamProjectSync.current = false
      return
    }
    setTeamProjects((prev) =>
      prev.map((project) =>
        project.id === activeTeamProjectId
          ? {
              ...project,
              documents,
              dependencies,
              nodeBindings,
              importAssets,
              linkedDocIds,
              blueprint,
              blueprintId: blueprint.id,
              phase:
                documents.length === 0
                  ? blueprint.nodes.length === 0
                    ? 'Empty project setup'
                    : 'Blueprint draft'
                  : blueprint.status === 'docsGenerated'
                    ? 'Docs generated'
                    : project.phase,
              updatedAt: 'Just now',
            }
          : project,
      ),
    )
  }, [
    activeTeamProjectId,
    documents,
    dependencies,
    nodeBindings,
    importAssets,
    linkedDocIds,
    blueprint,
    platformTeamFactsStatus,
    teamProjectsPersistence.hydrationStatus,
  ])

  const cancelActiveGeneration = useCallback(() => {
    generationAbort.current?.abort('cancelled')
    generationAbort.current = null
  }, [])

  const runGeneration = useCallback(
    async (prompt: string, mode: GenerationMode) => {
      cancelActiveGeneration()
      const runProjectId = selectedProductProjectId
      const abortController = new AbortController()
      generationAbort.current = abortController
      activeGenerationRunId.current = null
      lastGenerationRequest.current = { prompt, mode }
      const startedAt = Date.now()
      const collectedEvents: GenerationEvent[] = []
      const collectedLifecycleEvents: GenerationLifecycleEvent[] = []
      if (mode !== 'plan') {
        setProjectCatalog((catalog) =>
          updateProjectWorkspace(catalog, runProjectId, (current) =>
            createCheckpoint(current, {
              id: `before-run-${startedAt}`,
              label: t('core.store.beforeRunLabel', {
                mode: t(
                  mode === 'build'
                    ? 'core.generationMode.build'
                    : mode === 'iterate'
                      ? 'core.generationMode.iterate'
                      : 'core.generationMode.fix',
                ),
              }),
              message: t('core.store.beforeRunMessage'),
            }),
          ),
        )
      }

      setGenerationError(null)
      setGenerationErrorCode(null)
      setGenerationErrorStatus(undefined)
      setGenerationErrorRetryable(false)
      setGenerationErrorCategory(undefined)
      setGenerationErrorRetryAfterSeconds(undefined)
      setGenerationErrorAction(undefined)
      setGenerationEvents([])
      setGenerationLifecycleEvents([])
      setGenerationSummary('')
      setGenerationProvider(null)
      setGenerationUsage(null)
      setGenerationDurationMs(0)
      setGenerationCost(null)
      setGenerationLimits(null)
      setGenerationPlan(null)
      setTasks([])
      setPhase(mode === 'plan' ? 'planning' : 'building')
      setView('preview')

      const approvedDocumentIds = new Set(
        documents.filter((document) => document.status === 'approved').map((document) => document.id),
      )
      const includedAttachments = attachments
        .filter((attachment) => attachment.included !== false)
        .filter(
          (attachment) =>
            !preferences.requireApprovedContext ||
            attachment.kind !== 'document' ||
            (attachment.sourceId ? approvedDocumentIds.has(attachment.sourceId) : false),
        )
      const requestAttachments = includedAttachments
        .map((attachment) => ({
        kind:
          attachment.kind === 'image'
            ? ('image' as const)
            : attachment.kind === 'url'
              ? ('url' as const)
              : ('text' as const),
        name: redactSensitivePrompt(attachment.name),
        mimeType: attachment.mimeType,
        content: attachment.content,
      }))
      const linkedDocuments = linkedDocIds
        .map((id) => documents.find((document) => document.id === id))
        .filter((document): document is TeamDocument => Boolean(document))
        .filter(
          (document) => !preferences.requireApprovedContext || document.status === 'approved',
        )
        .map((document) => ({
          id: document.id,
          title: document.title,
          status: document.status,
          summary: document.summary,
          sections: document.sections.map((section) => ({
            title: section.title,
            body: section.body.slice(0, 4_000),
          })),
        }))

      try {
        const result = await streamGeneration(
          {
            projectId: runProjectId,
            prompt,
            mode,
            model: generationModel,
            currentFiles: workspace.files.map((file) => ({
              path: file.path,
              content: file.content,
              language: file.language,
            })),
            attachments: requestAttachments,
            context: {
              attachmentSources: includedAttachments.map((attachment) => ({
                kind: attachment.kind,
                name: redactSensitivePrompt(attachment.name),
                sourceId: attachment.sourceId ?? null,
              })),
              linkedDocuments,
              blueprint: activeBlueprintContext
                ? {
                    title: activeBlueprintContext.title,
                    nodeIds: activeBlueprintContext.nodeIds,
                    missingItems: activeBlueprintContext.missingItems,
                  }
                : null,
            },
          },
          {
            signal: abortController.signal,
            onLifecycle: (event) => {
              activeGenerationRunId.current = event.runId
              collectedLifecycleEvents.push(event)
              setGenerationLifecycleEvents((current) => [...current, event].slice(-40))
            },
            onEvent: (event) => {
              activeGenerationRunId.current = event.runId
              collectedEvents.push(event)
              setGenerationEvents((current) => [...current, event].slice(-300))
              if (event.type === 'plan') {
                setGenerationPlan(event.plan)
                setGenerationProvider(event.provider)
                setTasks(
                  event.plan.tasks.map((task) => ({
                    id: task.id,
                    title: task.title,
                    status: 'pending',
                    subStatus: task.description,
                  })),
                )
              } else if (event.type === 'task') {
                setTasks((current) =>
                  current.map((task) =>
                    task.id === event.task.id
                      ? {
                          ...task,
                          status:
                            event.status === 'completed'
                              ? 'done'
                              : event.status === 'failed'
                                ? 'error'
                                : 'active',
                          subStatus: event.task.description,
                        }
                      : task,
                  ),
                )
              } else if (event.type === 'result') {
                setGenerationSummary(event.result.summary)
                setGenerationProvider(event.result.provider)
                if (event.result.provider === 'openai') setGenerationModel(event.result.model)
                setGenerationUsage(event.result.usage ?? null)
                setGenerationCost(event.result.cost ?? null)
                setGenerationLimits(event.result.limits ?? null)
              }
            },
          },
        )

        const remainingDisplayTime = 420 - (Date.now() - startedAt)
        if (remainingDisplayTime > 0) {
          await new Promise((resolve) => setTimeout(resolve, remainingDisplayTime))
        }
        if (abortController.signal.aborted) return
        setGenerationDurationMs(Date.now() - startedAt)
        setPromptHistory((current) =>
          addPromptHistoryEntry(current, {
            id: result.runId,
            prompt,
            mode,
            model: result.model,
            status: 'completed',
            createdAt: new Date().toISOString(),
          }),
        )

        const completedAt = new Date().toISOString()
        const generationRun = {
          id: result.runId,
          prompt,
          mode,
          model: result.model,
          provider: result.provider,
          status: 'completed' as const,
          startedAt: new Date(startedAt).toISOString(),
          updatedAt: completedAt,
          completedAt,
          events: [
            ...collectedEvents.map((event) => ({
            id: `${event.runId}-${event.sequence}`,
            type:
              event.type === 'file'
                ? ('file' as const)
                : event.type === 'log'
                  ? ('message' as const)
                  : event.type === 'error'
                    ? ('diagnostic' as const)
                    : ('status' as const),
            summary:
              event.type === 'log'
                ? event.message
                : event.type === 'file'
                  ? `Generated ${event.file.path}`
                  : event.type === 'task'
                    ? `${event.task.title}: ${event.status}`
                    : event.type === 'plan'
                      ? event.plan.title
                      : event.type === 'error'
                        ? event.error.message
                        : event.result.summary,
            createdAt: event.timestamp,
            ...(event.type === 'file' ? { path: event.file.path } : {}),
            })),
            ...collectedLifecycleEvents.map((event) => ({
              id: `${event.runId}-${event.sequence}`,
              type: 'status' as const,
              summary: `Lifecycle ${event.status}`,
              createdAt: event.timestamp,
            })),
          ].sort((left, right) => left.createdAt.localeCompare(right.createdAt)),
          createdFileCount: result.files.length,
          updatedFileCount: workspace.files.length > 0 ? result.files.length : 0,
          inputTokens: result.usage?.inputTokens,
          outputTokens: result.usage?.outputTokens,
          totalTokens: result.usage?.totalTokens,
          durationMs: Date.now() - startedAt,
          costUsd: result.cost?.amount,
          maxTokens: result.limits?.maxOutputTokens ?? result.limits?.maxTotalTokens,
        }

        if (mode === 'plan') {
          setProjectCatalog((catalog) =>
            recordCatalogGeneration(catalog, runProjectId, generationRun),
          )
          setPhase('planReady')
          setPlanMode(true)
        } else {
          const versionNo = versionSerial.current + 1
          versionSerial.current = versionNo
          const version: ProjectVersion = {
            id: `v${versionNo}`,
            title: result.plan.title,
            subtitle: `Version ${versionNo} at ${new Date().toLocaleTimeString([], {
              hour: '2-digit',
              minute: '2-digit',
            })}`,
            starred: false,
          }
          setProjectCatalog((catalog) => {
            const projectId = runProjectId
            let next = updateProjectWorkspace(catalog, projectId, (current) => {
              const patched = result.files.reduce(
                (nextWorkspace, file) =>
                  upsertFile(nextWorkspace, {
                    path: file.path,
                    content: file.content,
                    language: file.language,
                    dirty: false,
                  }),
                current,
              )
              return createCheckpoint(patched, {
                id: version.id,
                label: version.title,
                message: result.summary,
              })
            })
            next = recordCatalogGeneration(next, projectId, generationRun)
            return recordCatalogVersion(next, projectId, {
              id: version.id,
              label: version.title,
              description: result.summary,
              workspaceCheckpointId: version.id,
              branchId: next.projects.find((project) => project.id === projectId)?.workspace.activeBranchId,
              generationRunId: result.runId,
              fileCount: result.files.length,
            })
          })
          setVersions((current) => [...current.filter((item) => item.id !== version.id), version])
          if (selectedProductProjectIdRef.current === runProjectId) {
            setSelectedWorkspaceFile((current) => result.files.at(-1)?.path ?? current)
          }
          setTasks((current) => current.map((task) => ({ ...task, status: 'done' })))
          setPhase('complete')
          setPlanMode(false)
          setAttachments([])
        }
      } catch (error) {
        if (abortController.signal.aborted) return
        const message =
          error instanceof GenerationClientError
            ? error.message
            : error instanceof Error
              ? error.message
              : 'Generation failed.'
        setGenerationError(message)
        setGenerationErrorCode(
          error instanceof GenerationClientError ? error.code : 'generation_failed',
        )
        setGenerationErrorStatus(
          error instanceof GenerationClientError ? error.status : undefined,
        )
        setGenerationErrorRetryable(
          error instanceof GenerationClientError ? error.retryable : false,
        )
        setGenerationErrorCategory(
          error instanceof GenerationClientError ? error.category : undefined,
        )
        setGenerationErrorRetryAfterSeconds(
          error instanceof GenerationClientError ? error.retryAfterSeconds : undefined,
        )
        setGenerationErrorAction(
          error instanceof GenerationClientError ? error.action : undefined,
        )
        setGenerationDurationMs(Date.now() - startedAt)
        setPromptHistory((current) =>
          addPromptHistoryEntry(current, {
            id: `failed-${Date.now()}`,
            prompt,
            mode,
            model: generationModel,
            status: 'failed',
            createdAt: new Date().toISOString(),
          }),
        )
        setProjectCatalog((catalog) =>
          recordCatalogGeneration(catalog, runProjectId, {
            id: collectedEvents[0]?.runId ?? `failed-${Date.now()}`,
            prompt,
            mode,
            model: generationModel,
            status: 'failed',
            startedAt: new Date(startedAt).toISOString(),
            updatedAt: new Date().toISOString(),
            completedAt: new Date().toISOString(),
            errorMessage: message,
            events: [
              ...collectedEvents.map((event) => ({
                id: `${event.runId}-${event.sequence}`,
                type:
                  event.type === 'log'
                    ? ('message' as const)
                    : event.type === 'file'
                      ? ('file' as const)
                      : ('status' as const),
                summary: event.type === 'log' ? event.message : event.type,
                createdAt: event.timestamp,
                ...(event.type === 'file' ? { path: event.file.path } : {}),
              })),
              ...collectedLifecycleEvents.map((event) => ({
                id: `${event.runId}-${event.sequence}`,
                type: 'status' as const,
                summary: `Lifecycle ${event.status}`,
                createdAt: event.timestamp,
              })),
            ].sort((left, right) => left.createdAt.localeCompare(right.createdAt)),
          }),
        )
        setTasks((current) =>
          current.map((task) =>
            task.status === 'active' || task.status === 'pending'
              ? { ...task, status: 'error', subStatus: message }
              : task,
          ),
        )
        setPhase('error')
      } finally {
        if (generationAbort.current === abortController) generationAbort.current = null
        activeGenerationRunId.current = null
      }
    },
    [
      activeBlueprintContext,
      attachments,
      cancelActiveGeneration,
      documents,
      generationModel,
      linkedDocIds,
      preferences.requireApprovedContext,
      selectedProductProjectId,
      setWorkspace,
      setPromptHistory,
      setProjectCatalog,
      t,
      workspace.files,
    ],
  )

  const startBuild = useCallback(() => {
    const prompt = lastGenerationRequest.current?.prompt ?? USER_PROMPT
    setPlanMode(false)
    void runGeneration(prompt, generationMode === 'fix' ? 'fix' : workspace.files.length > 0 ? 'iterate' : 'build')
  }, [generationMode, runGeneration, workspace.files.length])

  const submitPrompt = useCallback(
    (text: string) => {
      const prompt = text.trim()
      if (!prompt || phase === 'building' || phase === 'planning') return

      setFollowUps((current) => [
        ...current,
        {
          id: `f${Date.now()}`,
          text: prompt,
          mode: planMode ? 'plan' : 'build',
          createdAt: new Date().toISOString(),
        },
      ])
      setComposerDraft('')
      void runGeneration(prompt, planMode ? 'plan' : generationMode)
    },
    [generationMode, phase, planMode, runGeneration],
  )

  const retryGeneration = useCallback(() => {
    const last = lastGenerationRequest.current
    if (last) void runGeneration(last.prompt, last.mode)
  }, [runGeneration])

  const stopBuild = useCallback(() => {
    const cancelledRequest = lastGenerationRequest.current
    const runId = activeGenerationRunId.current ?? `cancelled-${Date.now()}`
    cancelActiveGeneration()
    setTasks((current) =>
      current.map((task) =>
        task.status === 'active' || task.status === 'pending'
          ? { ...task, status: 'error', subStatus: t('core.store.stopped') }
          : task,
      ),
    )
    setGenerationError(t('core.store.generationStopped'))
    setGenerationErrorCode('cancelled')
    setGenerationErrorStatus(undefined)
    setGenerationErrorRetryable(true)
    setGenerationErrorCategory('cancelled')
    setGenerationErrorRetryAfterSeconds(undefined)
    setGenerationErrorAction(t('core.store.adjustOrRetry'))
    if (cancelledRequest) {
      const completedAt = new Date().toISOString()
      setPromptHistory((current) =>
        addPromptHistoryEntry(current, {
          id: runId,
          prompt: cancelledRequest.prompt,
          mode: cancelledRequest.mode,
          model: generationModel,
          status: 'cancelled',
          createdAt: completedAt,
        }),
      )
      setProjectCatalog((catalog) =>
        recordCatalogGeneration(catalog, catalog.selectedProjectId, {
          id: runId,
          prompt: cancelledRequest.prompt,
          mode: cancelledRequest.mode,
          model: generationModel,
          status: 'cancelled',
          startedAt: completedAt,
          updatedAt: completedAt,
          completedAt,
          errorMessage: t('core.store.generationStopped'),
        }),
      )
    }
    setPhase('error')
  }, [cancelActiveGeneration, generationModel, setProjectCatalog, setPromptHistory, t])

  const resetWorkbench = useCallback(() => {
    cancelActiveGeneration()
    lastGenerationRequest.current = null
    setWorkspace(createInitialWorkspace())
    setSelectedWorkspaceFile('src/App.tsx')
    setTasks([])
    setVersions(VERSIONS.slice(0, 1))
    setFollowUps([])
    setGenerationPlan(INITIAL_GENERATION_RESULT.plan)
    setGenerationEvents([])
    setGenerationLifecycleEvents([])
    setGenerationSummary(INITIAL_GENERATION_RESULT.summary)
    setGenerationError(null)
    setGenerationErrorCode(null)
    setGenerationErrorStatus(undefined)
    setGenerationErrorRetryable(false)
    setGenerationErrorCategory(undefined)
    setGenerationErrorRetryAfterSeconds(undefined)
    setGenerationErrorAction(undefined)
    setGenerationProvider('local')
    setGenerationUsage(INITIAL_GENERATION_RESULT.usage ?? null)
    setGenerationDurationMs(0)
    setGenerationCost(null)
    setGenerationLimits(null)
    setAttachments([])
    versionSerial.current = 2
    setPlanMode(true)
    setView('preview')
    setPhase('planning')
  }, [cancelActiveGeneration, setWorkspace])

  useEffect(() => {
    if (!routeReady || phase !== 'planning') return
    if (lastGenerationRequest.current || followUps.length > 0) return
    const timer = setTimeout(() => setPhase('planReady'), 420)
    return () => clearTimeout(timer)
  }, [followUps.length, phase, routeReady])

  useEffect(
    () => () => {
      generationAbort.current?.abort('unmounted')
      if (importTimer.current) clearTimeout(importTimer.current)
    },
    [],
  )

  const toggleVersionStar = (id: string) =>
    setVersions((prev) => prev.map((v) => (v.id === id ? { ...v, starred: !v.starred } : v)))

  const toggleTodo = (id: string) =>
    setTodos((prev) => prev.map((t) => (t.id === id ? { ...t, done: !t.done } : t)))

  const addTodo = (title: string, priority: TodoTask['priority']) => {
    if (!title.trim()) return
    setTodos((prev) => [
      { id: `k${Date.now()}`, title: title.trim(), priority, when: new Date().toISOString(), done: false },
      ...prev,
    ])
  }

  const updateWorkspaceFile = useCallback(
    (path: string, content: string, dirty = false) => {
      setWorkspace((current) => upsertFile(current, { path, content, dirty }))
    },
    [setWorkspace],
  )

  const createWorkspaceFile = useCallback(
    (path: string, content = '') => {
      setWorkspace((current) => upsertFile(current, { path, content }))
      setSelectedWorkspaceFile(path)
    },
    [setWorkspace],
  )

  const deleteWorkspaceFile = useCallback(
    (path: string) => {
      setWorkspace((current) => {
        const next = deleteWorkspaceFileFromState(current, path)
        if (path === selectedWorkspaceFile) setSelectedWorkspaceFile(next.files[0]?.path ?? '')
        return next
      })
    },
    [selectedWorkspaceFile, setWorkspace],
  )

  const renameWorkspaceFile = useCallback(
    (fromPath: string, toPath: string) => {
      setWorkspace((current) => renameWorkspaceFileInState(current, fromPath, toPath))
      if (selectedWorkspaceFile === fromPath) setSelectedWorkspaceFile(toPath)
    },
    [selectedWorkspaceFile, setWorkspace],
  )

  const createWorkspaceCheckpoint = useCallback(
    (label?: string, message?: string) => {
      setWorkspace((current) => createCheckpoint(current, { label, message }))
    },
    [setWorkspace],
  )

  const restoreWorkspaceCheckpoint = useCallback(
    (id: string) => {
      let recoveryCheckpointId: string | undefined
      setWorkspace((current) => {
        const safetyCheckpoint = createCheckpoint(current, {
          label: t('core.store.beforeRestoreLabel', { id }),
          message: t('core.store.restoreRecoveryMessage'),
        })
        recoveryCheckpointId = safetyCheckpoint.checkpoints.at(-1)?.id
        return restoreCheckpoint(safetyCheckpoint, id)
      })
      setRestoreRecoveryCheckpointId(recoveryCheckpointId ?? null)
    },
    [setWorkspace, t],
  )

  const undoWorkspaceRestore = useCallback(() => {
    if (!restoreRecoveryCheckpointId) return
    setWorkspace((current) => restoreCheckpoint(current, restoreRecoveryCheckpointId))
    setRestoreRecoveryCheckpointId(null)
  }, [restoreRecoveryCheckpointId, setWorkspace])

  const createWorkspaceBranch = useCallback(
    (name: string, checkpointId?: string) => {
      setWorkspace((current) =>
        createWorkspaceBranchInState(current, {
          name,
          fromCheckpointId: checkpointId,
          checkout: true,
        }),
      )
    },
    [setWorkspace],
  )

  const addAttachment = useCallback((attachment: ComposerAttachment) => {
    if (attachments.some((item) => item.id === attachment.id)) return
    const normalized = { ...attachment, included: attachment.included !== false }
    if (attachmentSetIssue(attachments, normalized)) return
    setAttachments((current) => [...current, normalized])
    setProjectCatalog((catalog) =>
      addCatalogAttachment(catalog, catalog.selectedProjectId, {
        id: attachment.id,
        name: redactSensitivePrompt(attachment.name),
        kind:
          attachment.kind === 'image'
            ? 'image'
            : attachment.kind === 'document'
              ? 'document'
              : attachment.kind === 'url'
                ? 'url'
                : 'other',
        mimeType: attachment.mimeType,
        sizeBytes: attachment.size,
        sourceUrl: attachment.kind === 'url' ? attachment.content : undefined,
        workspacePath: attachment.kind === 'workspace' ? attachment.sourceId : undefined,
      }),
    )
  }, [attachments, setProjectCatalog])

  const removeAttachment = useCallback((id: string) => {
    setAttachments((current) => current.filter((item) => item.id !== id))
    setProjectCatalog((catalog) => {
      const project = catalog.projects.find((item) => item.id === catalog.selectedProjectId)
      return project?.attachments.some((attachment) => attachment.id === id)
        ? removeCatalogAttachment(catalog, catalog.selectedProjectId, id)
        : catalog
    })
  }, [setProjectCatalog])

  const toggleAttachmentIncluded = useCallback((id: string) => {
    setAttachments((current) =>
      current.map((attachment) =>
        attachment.id === id
          ? { ...attachment, included: attachment.included === false }
          : attachment,
      ),
    )
  }, [])

  const clearAttachments = useCallback(() => setAttachments([]), [])

  const savePromptTemplate = useCallback(
    (template: PromptTemplate) => {
      if (template.builtIn || !isPromptTemplateList([template])) return
      customTemplatePersistence.setValue((current) => [
        { ...template, prompt: redactSensitivePrompt(template.prompt) },
        ...current.filter((item) => item.id !== template.id),
      ].slice(0, 50))
    },
    [customTemplatePersistence],
  )

  const deletePromptTemplate = useCallback(
    (id: string) => {
      customTemplatePersistence.setValue((current) => current.filter((item) => item.id !== id))
    },
    [customTemplatePersistence],
  )

  const savePromptWorkflow = useCallback(
    (workflow: PromptWorkflow) => {
      if (workflow.builtIn || !isPromptWorkflowList([workflow])) return
      customWorkflowPersistence.setValue((current) => [
        {
          ...workflow,
          steps: workflow.steps.map((step) => ({
            ...step,
            prompt: redactSensitivePrompt(step.prompt),
          })),
        },
        ...current.filter((item) => item.id !== workflow.id),
      ].slice(0, 30))
    },
    [customWorkflowPersistence],
  )

  const deletePromptWorkflow = useCallback(
    (id: string) => {
      customWorkflowPersistence.setValue((current) =>
        current.filter((workflow) => workflow.id !== id),
      )
    },
    [customWorkflowPersistence],
  )

  const runWorkspaceQuality = useCallback(async () => {
    setQualityRunning(true)
    setQualityError(null)
    try {
      const result = await requestQualityRun({
        projectId: selectedProductProjectId,
        files: workspace.files.map((file) => ({
          path: file.path,
          content: file.content,
          language: file.language,
        })),
        entryPath: previewDocument.entryPath,
      })
      setQualityRun(result)
      setWorkspace((current) =>
        setWorkspaceDiagnostics(
          current,
          result.diagnostics.map((diagnostic, index) => ({
            id: `${result.metadata.runId}:${diagnostic.code}:${index}`,
            severity: diagnostic.severity,
            message: diagnostic.message,
            path: diagnostic.path,
            source: diagnostic.checkId,
            line: diagnostic.line,
            column: diagnostic.column,
          })),
        ),
      )
      return result
    } catch (error) {
      setQualityError(error instanceof Error ? error.message : t('core.store.qualityFailed'))
      return null
    } finally {
      setQualityRunning(false)
    }
  }, [previewDocument.entryPath, selectedProductProjectId, setWorkspace, t, workspace.files])

  const attachQualityDiagnostics = useCallback(() => {
    if (!qualityRun) return
    addAttachment({
      id: `quality-${qualityRun.metadata.runId}`,
      kind: 'file',
      name: `quality-${qualityRun.metadata.runId}.json`,
      mimeType: 'application/json',
      content: qualityResultAsPromptContext(qualityRun),
      size: qualityRun.diagnostics.length,
    })
    setGenerationMode('fix')
    setPlanMode(false)
    setComposerDraft(
      t('core.store.repairQualityPrompt'),
    )
  }, [addAttachment, qualityRun, t])

  const exportWorkspace = useCallback(async () => {
    setDeliveryStatus('exporting')
    setDeliveryError(null)
    setDeliveryLogs((current) => [...current, t('core.store.delivery.preparingArchive')])
    try {
      const result = await exportWorkspaceArchive({ ...workspace, id: selectedProductProjectId, name: projectName })
      downloadBlob(result.blob, result.filename)
      setDeliveryLogs((current) => [
        ...current,
        t('core.store.delivery.downloaded', {
          filename: result.filename,
          count: result.fileCount.toLocaleString(locale),
        }),
      ])
      setDeliveryStatus('idle')
      return true
    } catch (error) {
      const message = error instanceof Error ? error.message : t('core.store.delivery.exportFailed')
      setDeliveryError(message)
      setDeliveryLogs((current) => [...current, t('core.store.delivery.exportFailedDetail', { message })])
      setDeliveryStatus('error')
      return false
    }
  }, [locale, projectName, selectedProductProjectId, t, workspace])

  const refreshDeployments = useCallback(async () => {
    try {
      const history = await listDeployments(selectedProductProjectId)
      setDeployments(history)
      return history
    } catch (error) {
      const message = error instanceof Error ? error.message : t('core.store.delivery.loadHistoryFailed')
      setDeliveryError(message)
      return []
    }
  }, [selectedProductProjectId, t])

  const publishCurrentWorkspace = useCallback(
    async (message?: string, environment: 'preview' | 'production' = 'preview') => {
      setDeliveryStatus('publishing')
      setDeliveryError(null)
      setDeliveryLogs((current) => [
        ...current,
        t('core.store.delivery.validating'),
      ])
      try {
        const result = await publishWorkspace(
          { ...workspace, id: selectedProductProjectId, name: projectName },
          previewDocument.html,
          previewDocument.entryPath,
          {
            message,
            environment,
            onLog: (line) => setDeliveryLogs((current) => [...current, line]),
          },
        )
        setPublishedUrl(result.absoluteUrl)
        setDeployments((current) => [
          result.deployment,
          ...current.filter((item) => item.deploymentId !== result.deployment.deploymentId),
        ])
        setProjectCatalog((catalog) =>
          recordCatalogDeployment(catalog, catalog.selectedProjectId, {
            id: result.deployment.activeVersionId,
            provider: 'custom',
            status: 'ready',
            environment,
            completedAt: new Date().toISOString(),
            url: result.absoluteUrl,
            summary: message,
          }),
        )
        setDeliveryLogs((current) => [
          ...current,
          t('core.store.delivery.published', {
            version: (result.deployment.versions.at(-1)?.number ?? 1).toLocaleString(locale),
            url: result.absoluteUrl,
          }),
        ])
        setDeliveryStatus('idle')
        return result.deployment
      } catch (error) {
        const messageText = error instanceof Error ? error.message : t('core.store.delivery.publishFailed')
        setDeliveryError(messageText)
        setDeliveryLogs((current) => [...current, t('core.store.delivery.publishFailedDetail', { message: messageText })])
        setDeliveryStatus('error')
        return null
      }
    },
    [locale, previewDocument.entryPath, previewDocument.html, projectName, selectedProductProjectId, setProjectCatalog, t, workspace],
  )

  const rollbackDeployment = useCallback(
    async (deploymentId: string, versionId: string) => {
      setDeliveryStatus('rollingBack')
      setDeliveryError(null)
      setDeliveryLogs((current) => [
        ...current,
        t('core.store.delivery.creatingRollback', { version: versionId }),
      ])
      try {
        const deployment = await requestDeploymentRollback(deploymentId, versionId)
        setDeployments((current) => [
          deployment,
          ...current.filter((item) => item.deploymentId !== deployment.deploymentId),
        ])
        setProjectCatalog((catalog) =>
          recordCatalogDeployment(catalog, catalog.selectedProjectId, {
            id: deployment.activeVersionId,
            provider: 'custom',
            status: 'ready',
            environment: 'preview',
            completedAt: new Date().toISOString(),
            url: publishedUrl ?? deployment.publicPath,
            summary: t('core.store.delivery.rollbackSummary', { version: versionId }),
          }),
        )
        setDeliveryLogs((current) => [...current, t('core.store.delivery.rollbackCompleted', { version: deployment.activeVersionId })])
        setDeliveryStatus('idle')
        return true
      } catch (error) {
        const message = error instanceof Error ? error.message : t('core.store.delivery.rollbackFailed')
        setDeliveryError(message)
        setDeliveryLogs((current) => [...current, t('core.store.delivery.rollbackFailedDetail', { message })])
        setDeliveryStatus('error')
        return false
      }
    },
    [publishedUrl, setProjectCatalog, t],
  )

  const loadTeamProject = useCallback((project: TeamProject) => {
    skipTeamProjectSync.current = true
    setActiveTeamProjectId(project.id)
    setDocuments(project.documents)
    setDependencies(project.dependencies)
    setNodeBindings(project.nodeBindings)
    setImportAssets(project.importAssets)
    setLinkedDocIds(project.linkedDocIds)
    setBlueprint(project.blueprint)
    setSelectedDocId(project.documents[0]?.id ?? null)
    setSelectedBlueprintNodeId(project.blueprint.nodes[0]?.id ?? null)
    setActiveBlueprintContext(null)
    setBlueprintOperations([])
  }, [])

  useEffect(() => {
    if (teamProjectsPersistence.hydrationStatus === 'hydrating' || teamCatalogLoaded.current) return
    const project =
      teamProjects.find((item) => item.id === activeTeamProjectId) ?? teamProjects[0]
    if (!project) return
    teamCatalogLoaded.current = true
    loadTeamProject(project)
  }, [activeTeamProjectId, loadTeamProject, teamProjects, teamProjectsPersistence.hydrationStatus])

  const beginPlatformTeamFacts = useCallback((projectId: string) => {
    platformTeamFactsProjectId.current = projectId
    setPlatformTeamFactsStatus('loading')
    setPlatformTeamFactsError(null)
    setDocuments([])
    setDependencies([])
    setNodeBindings([])
    setImportAssets([])
    setLinkedDocIds([])
    setBlueprint(blankBlueprint(`bp-${projectId}`, 'Loading platform blueprint'))
    setSelectedDocId(null)
    setSelectedBlueprintNodeId(null)
  }, [])

  const applyPlatformTeamFacts = useCallback((input: {
    readonly projectId: string
    readonly documents: TeamDocument[]
    readonly dependencies: DocumentDependency[]
    readonly blueprint: Blueprint
  }) => {
    if (selectedProductProjectIdRef.current !== input.projectId) return
    platformTeamFactsProjectId.current = input.projectId
    setDocuments(input.documents)
    setDependencies(input.dependencies)
    setNodeBindings([])
    setImportAssets([])
    setLinkedDocIds(input.documents.map((document) => document.id))
    setBlueprint(input.blueprint)
    setSelectedDocId((current) =>
      input.documents.some((document) => document.id === current)
        ? current
        : input.documents[0]?.id ?? null,
    )
    setSelectedBlueprintNodeId((current) =>
      input.blueprint.nodes.some((node) => node.id === current)
        ? current
        : input.blueprint.nodes[0]?.id ?? null,
    )
    setPlatformTeamFactsStatus('ready')
    setPlatformTeamFactsError(null)
  }, [])

  const failPlatformTeamFacts = useCallback((projectId: string, message: string) => {
    if (selectedProductProjectIdRef.current !== projectId) return
    platformTeamFactsProjectId.current = projectId
    setDocuments([])
    setDependencies([])
    setNodeBindings([])
    setImportAssets([])
    setLinkedDocIds([])
    setBlueprint(blankBlueprint(`bp-${projectId}`, 'Platform blueprint unavailable'))
    setSelectedDocId(null)
    setSelectedBlueprintNodeId(null)
    setPlatformTeamFactsStatus('error')
    setPlatformTeamFactsError(message)
  }, [])

  const openTeamProject = (id: string) => {
    const project = teamProjects.find((item) => item.id === id)
    if (!project) return
    loadTeamProject(project)
    setSurface('team')
    setTeamView(project.documents.length === 0 ? 'dashboard' : 'graph')
  }

  const selectPlatformProject = useCallback((project: { readonly id: string; readonly name: string }) => {
    cancelActiveGeneration()
    setProjectCatalog((catalog) => {
      let next = catalog
      if (!next.projects.some((item) => item.id === project.id)) {
        next = addProjectToCatalog(
          next,
          createBlankProject({
            id: project.id,
            workspaceId: `workspace-${project.id}`,
            name: project.name,
            teamId: project.id,
            teamName: project.name,
            lifecycleStatus: 'active',
          }),
          { select: false },
        )
      } else if (next.projects.find((item) => item.id === project.id)?.name !== project.name) {
        next = renameCatalogProject(next, project.id, project.name)
      }
      return selectCatalogProject(next, project.id)
    })

    // A server project gets a local navigation shell only. Never hydrate its
    // documents/blueprint from a previously persisted team-project snapshot.
    const unifiedTeamProject = {
      ...buildTeamProject(project.id, project.name, 'blank'),
      teamId: project.id,
      teamName: project.name,
    }
    setTeamProjects((current) => {
      const exists = current.some((item) => item.id === project.id)
      return exists
        ? current.map((item) => item.id === project.id ? unifiedTeamProject : item)
        : [unifiedTeamProject, ...current]
    })
    if (activeTeamProjectId !== project.id) loadTeamProject(unifiedTeamProject)
    if (platformTeamFactsProjectId.current !== project.id) beginPlatformTeamFacts(project.id)
  }, [
    activeTeamProjectId,
    beginPlatformTeamFacts,
    cancelActiveGeneration,
    loadTeamProject,
    setProjectCatalog,
    setTeamProjects,
    teamProjects,
  ])

  const createTeamProject = (name = `New Project ${teamProjects.length + 1}`, source: TeamProjectSource = 'blank') => {
    const id = `tp-${Date.now()}`
    const baseBlueprint =
      source === 'blueprint'
        ? generatedBlueprint(`bp-${id}`, name, `Create a product capability map for ${name}.`)
        : source === 'template'
          ? cloneBlueprint(DEFAULT_BLUEPRINT, { id: `bp-${id}`, title: `${name} Blueprint` })
          : blankBlueprint(`bp-${id}`, name)
    const graph =
      source === 'template'
        ? {
            documents: TEAM_DOCUMENTS,
            dependencies: DOC_DEPENDENCIES,
            nodeBindings: NODE_BINDINGS,
            linkedDocIds: ['d3', 'd4', 'd6', 'd7'],
            blueprint: baseBlueprint,
          }
        : source === 'blueprint'
          ? (() => {
              const generated = docsFromBlueprint(baseBlueprint)
              return {
                ...generated,
                linkedDocIds: generated.documents.map((doc) => doc.id),
              }
            })()
          : {
              documents: [],
              dependencies: [],
              nodeBindings: [],
              linkedDocIds: [],
              blueprint: baseBlueprint,
            }
    const project = buildTeamProject(id, name, source, {
      phase:
        source === 'blank'
          ? 'Empty project setup'
          : source === 'blueprint'
            ? 'Docs generated'
            : 'Design & Contract',
      documents: graph.documents,
      dependencies: graph.dependencies,
      nodeBindings: graph.nodeBindings,
      importAssets: source === 'template' ? IMPORT_ASSETS : [],
      linkedDocIds: graph.linkedDocIds,
      blueprint: graph.blueprint,
    })
    setTeamProjects((prev) => [project, ...prev])
    setProjectCatalog((catalog) =>
      addProjectToCatalog(
        catalog,
        createBlankProject({
          id,
          workspaceId: `workspace-${id}`,
          name,
          teamId: project.teamId,
          teamName: project.teamName,
          lifecycleStatus: 'active',
        }),
        { select: false },
      ),
    )
    loadTeamProject(project)
    setSurface('team')
    setTeamView('dashboard')
    return id
  }

  const openProject = (id: string) => {
    if (id.startsWith('tp-')) {
      openTeamProject(id)
      return
    }
    if (id === 'p2') {
      openTeamProject('tp-crm')
      return
    }
    if (id === 'p3') {
      openTeamProject('tp-billing')
      return
    }
    const target = projectCatalog.projects.find((project) => project.id === id)
    if (!target) return
    cancelActiveGeneration()
    setProjectCatalog((catalog) => selectCatalogProject(catalog, id))
    setPhase(target.generationRuns.at(-1)?.status === 'completed' ? 'complete' : 'planReady')
    setSurface('workbench')
  }

  useEffect(() => {
    if (routeReady || typeof window === 'undefined') return

    const route = parseWorksflowRoute(window.location.pathname, window.location.search)

    if (route.surface === 'workbench') {
      generationAbort.current?.abort('route changed')
      generationAbort.current = null
      setSurface('workbench')
      setView(route.view)
      setPlanMode(route.phase === 'planning' || route.phase === 'planReady')

      if (route.phase === 'building') {
        setTasks(BUILD_TASKS.map((task, index) => ({
          ...task,
          status: index === 0 ? 'active' : 'pending',
          subStatus: index === 0 ? BUILD_SUBSTATUS[task.id] : undefined,
        })))
        setPhase('building')
      } else if (route.phase === 'complete') {
        setTasks(BUILD_TASKS.map((task) => ({ ...task, status: 'done', subStatus: undefined })))
        setVersions((prev) => (prev.some((version) => version.id === 'v2') ? prev : VERSIONS))
        setPhase('complete')
      } else if (route.phase === 'error') {
        setTasks(BUILD_TASKS.map((task, index) => ({
          ...task,
          status: index === 0 ? 'error' : 'pending',
          subStatus: index === 0 ? t('core.store.stopped') : undefined,
        })))
        setPhase('error')
      } else {
        setTasks(BUILD_TASKS.map((task) => ({ ...task, status: 'pending', subStatus: undefined })))
        setPhase(route.phase)
      }
    } else if (route.surface === 'team') {
      const project = teamProjects.find((item) => item.id === route.projectId)

      if (project) loadTeamProject(project)
      setSurface('team')
      setTeamView(route.teamView)
    } else if (route.surface === 'recent') {
      setSurface('recent')
    } else if (route.surface === 'settings') {
      setSurface('settings')
    }

    setRouteReady(true)
  }, [routeReady, t, teamProjects])

  const toggleProjectStar = (id: string) => {
    setProjectCatalog((catalog) => toggleCatalogProjectStar(catalog, id))
  }

  const duplicateProject = () => {
    cancelActiveGeneration()
    setProjectCatalog((catalog) =>
      cloneCatalogProject(catalog, catalog.selectedProjectId, {
        name: `${projectName} Copy`,
        select: true,
      }),
    )
    setPhase('complete')
  }

  const createProductProject = (
    name = `Untitled project ${projectCatalog.projects.length + 1}`,
    source: 'blank' | 'template' = 'blank',
  ) => {
    cancelActiveGeneration()
    let projectId = ''
    setProjectCatalog((catalog) => {
      const active = catalog.projects.find((project) => project.id === catalog.selectedProjectId)!
      const project = source === 'template'
        ? createProjectFromTemplate(active, { name, teamId: active.teamId, teamName: active.teamName })
        : createBlankProject({ name, teamId: active.teamId, teamName: active.teamName })
      projectId = project.id
      return addProjectToCatalog(catalog, project, { select: true })
    })
    setSelectedWorkspaceFile('')
    setSurface('workbench')
    setPhase('planReady')
    setPlanMode(true)
    return projectId
  }

  const cloneProductProject = (id: string) => {
    cancelActiveGeneration()
    let clonedId = ''
    setProjectCatalog((catalog) => {
      const next = cloneCatalogProject(catalog, id, { select: true })
      clonedId = next.selectedProjectId
      return next
    })
    setPhase('complete')
    setSurface('workbench')
    return clonedId
  }

  const renameProductProject = (id: string, name: string) => {
    const normalized = name.trim()
    if (!normalized) return
    setProjectCatalog((catalog) => renameCatalogProject(catalog, id, normalized))
  }

  const importProductProject = (value: unknown, name?: string) => {
    cancelActiveGeneration()
    const candidate =
      isStrictVirtualWorkspace(value)
        ? value
        : value && typeof value === 'object' && 'workspace' in value &&
            isStrictVirtualWorkspace((value as { workspace?: unknown }).workspace)
          ? (value as { workspace: VirtualWorkspace }).workspace
          : null
    if (!candidate) throw new Error(t('runtime.store.invalidWorkspace'))
    let projectId = ''
    setProjectCatalog((catalog) => {
      const active = catalog.projects.find((project) => project.id === catalog.selectedProjectId)!
      const project = createProjectFromImport({
        name: name?.trim() || candidate.name,
        teamId: active.teamId,
        teamName: active.teamName,
        workspace: candidate,
        importProvider: 'workspace-json',
      })
      projectId = project.id
      return addProjectToCatalog(catalog, project, { select: true })
    })
    setPhase('complete')
    setSurface('workbench')
    return projectId
  }

  const updateGithubProjectSettings = useCallback(
    (settings: unknown) => {
      setProjectCatalog((catalog) =>
        updateCatalogGithubSettings(catalog, catalog.selectedProjectId, settings),
      )
    },
    [setProjectCatalog],
  )

  const updateDatabaseProjectSettings = useCallback(
    (settings: unknown) => {
      setProjectCatalog((catalog) =>
        updateCatalogDatabaseSettings(catalog, catalog.selectedProjectId, settings),
      )
    },
    [setProjectCatalog],
  )

  const deleteProductProject = (id = projectCatalog.selectedProjectId) => {
    cancelActiveGeneration()
    setProjectCatalog((catalog) =>
      deleteCatalogProject(catalog, id, {
        fallbackProject: {
          name: 'Untitled project',
          teamId: 'personal',
          teamName: 'Personal',
        },
      }),
    )
    setSurface('recent')
  }

  const openDoc = (id: string) => {
    setSelectedDocId(id)
    setTeamView('editor')
  }

  const createDocument = (type: DocType, title?: string, status: DocStatus = 'draft') => {
    const id = `d${Date.now()}`
    const nextDoc: TeamDocument = {
      id,
      type,
      title: title ?? '新建交付文档',
      status,
      ownerId: 'm1',
      members: [{ userId: 'm1', role: 'owner' }],
      updatedAt: 'Just now',
      blocking: 0,
      bindings: 0,
      externalSync: null,
      position: { x: 160 + documents.length * 80, y: 260 },
      summary: '从 Team Dashboard 创建的结构化文档草稿，可继续绑定成员、上游和下游交付物。',
      sections: [
        { title: '目标', body: '描述该交付文档需要解决的问题、范围和验收标准。' },
        { title: '绑定关系', body: '从右侧或图谱中添加成员、文档、原型资产和 Workbench 版本。' },
      ],
    }
    setDocuments((prev) => [...prev, nextDoc])
    setSelectedDocId(id)
    setTeamView('editor')
    return id
  }

  const generateDocumentChain = () => {
    setDocuments((prev) =>
      prev.map((doc) => ({
        ...doc,
        status: doc.status === 'archived' ? doc.status : 'draft',
        updatedAt: 'Just now',
      })),
    )
    setDependencies(DOC_DEPENDENCIES)
    setSelectedDocId('d1')
    setTeamView('graph')
  }

  const createBlankDocumentGraph = () => {
    const id = `d-blank-${Date.now()}`
    const doc: TeamDocument = {
      id,
      type: 'requirement',
      title: `${activeTeamProject.name} project brief`,
      status: 'draft',
      ownerId: 'm1',
      members: [{ userId: 'm1', role: 'owner' }],
      updatedAt: 'Just now',
      blocking: 0,
      bindings: 0,
      externalSync: null,
      position: { x: 80, y: 90 },
      summary: 'Blank graph starting point. Define scope, owners and downstream document flow.',
      sections: [
        { title: 'Scope', body: 'Define the business goal and acceptance criteria for this project.' },
        { title: 'Next documents', body: 'Add feature, page, API, prototype and implementation documents as the graph evolves.' },
      ],
    }
    setDocuments([doc])
    setDependencies([])
    setNodeBindings([])
    setLinkedDocIds([id])
    setSelectedDocId(id)
    setTeamView('graph')
  }

  const createDocumentGraphFromTemplate = () => {
    setDocuments(cloneDocuments(TEAM_DOCUMENTS))
    setDependencies(cloneDependencies(DOC_DEPENDENCIES))
    setNodeBindings(cloneNodeBindings(NODE_BINDINGS))
    setImportAssets(cloneImportAssets(IMPORT_ASSETS))
    setLinkedDocIds(['d3', 'd4', 'd6', 'd7'])
    setSelectedDocId('d1')
    setTeamView('graph')
  }

  const createDocumentGraphFromBlueprint = () => {
    const sourceBlueprint =
      blueprint.nodes.length > 0
        ? blueprint
        : generatedBlueprint(`bp-${activeTeamProjectId}-${Date.now()}`, activeTeamProject.name)
    const generated = docsFromBlueprint(sourceBlueprint)
    setBlueprint(generated.blueprint)
    setDocuments(generated.documents)
    setDependencies(generated.dependencies)
    setNodeBindings(generated.nodeBindings)
    setLinkedDocIds(generated.documents.map((doc) => doc.id))
    setSelectedDocId(generated.documents[0]?.id ?? null)
    setSelectedBlueprintNodeId(generated.blueprint.nodes[0]?.id ?? null)
    setTeamView('graph')
    recordBlueprintOperation(
      'generateDocsFromSelection',
      `Generated project Document Graph from ${sourceBlueprint.title}`,
    )
  }

  const moveDocumentNode = (id: string, position: { x: number; y: number }) => {
    setDocuments((prev) => prev.map((doc) => (doc.id === id ? { ...doc, position } : doc)))
  }

  const goToWorkbenchFromDoc = () => {
    setSurface('workbench')
  }

  const useDocInWorkbench = (id: string) => {
    setLinkedDocIds((prev) => (prev.includes(id) ? prev : [...prev, id]))
    setSelectedDocId(id)
    setSurface('workbench')
  }

  const toggleLinkedDoc = (id: string) => {
    setLinkedDocIds((prev) =>
      prev.includes(id) ? prev.filter((docId) => docId !== id) : [...prev, id],
    )
  }

  const addDocumentDependency = (
    sourceDocId: string,
    targetDocId: string,
    type: DependencyType,
    isBlocking: boolean,
  ) => {
    if (sourceDocId === targetDocId) return
    setDependencies((prev) => {
      const exists = prev.some(
        (e) =>
          e.sourceDocId === sourceDocId && e.targetDocId === targetDocId && e.type === type,
      )
      if (exists) return prev
      return [
        ...prev,
        {
          id: `e${Date.now()}`,
          sourceDocId,
          targetDocId,
          type,
          isBlocking,
        },
      ]
    })
    setDocuments((prev) =>
      prev.map((doc) =>
        doc.id === sourceDocId || doc.id === targetDocId
          ? { ...doc, bindings: doc.bindings + 1 }
          : doc,
      ),
    )
  }

  const addNodeBinding = (
    binding: Omit<NodeBinding, 'id' | 'sourceKind' | 'createdAt'> & {
      sourceKind?: BindingTargetKind
    },
  ) => {
    const exists = nodeBindings.some(
      (item) =>
        item.sourceId === binding.sourceId &&
        item.targetKind === binding.targetKind &&
        item.targetId === binding.targetId &&
        item.relation === binding.relation,
    )
    if (exists) return
    setNodeBindings((prev) => [
      ...prev,
      {
        ...binding,
        id: `nb${Date.now()}`,
        sourceKind: binding.sourceKind ?? 'document',
        createdAt: new Date().toISOString(),
      },
    ])
    if (binding.targetKind === 'document') {
      addDocumentDependency(
        binding.sourceId,
        binding.targetId,
        binding.relation as DependencyType,
        binding.isBlocking,
      )
    } else {
      setDocuments((prev) =>
        prev.map((doc) =>
          doc.id === binding.sourceId ? { ...doc, bindings: doc.bindings + 1 } : doc,
        ),
      )
    }
  }

  const createBlueprintNode = (
    type: BlueprintNodeType,
    title: string,
    position: { x: number; y: number } = { x: 140, y: 140 },
  ) => {
    const id = `b${Date.now()}`
    const node: BlueprintNode = {
      id,
      type,
      title,
      description: type === 'workbenchTarget' ? 'Target for generated implementation work.' : undefined,
      position,
      boundDocumentIds: [],
      boundMemberIds: type === 'feature' ? ['m1'] : [],
      boundPrototypeArtifactIds: type === 'prototype' ? [`asset-${Date.now()}`] : [],
      generatedDocIds: [],
      missing: type === 'api' ? ['No owner assigned'] : undefined,
    }
    setBlueprint((prev) => ({
      ...prev,
      status: prev.status === 'implemented' ? 'outdated' : 'draft',
      nodes: [...prev.nodes, node],
      version: prev.version + 1,
      updatedAt: 'Just now',
    }))
    setSelectedBlueprintNodeId(id)
    recordBlueprintOperation('createNode', `Created ${type} node "${title}"`)
    return id
  }

  const updateBlueprintNode = (
    id: string,
    updates: Partial<
      Pick<
        BlueprintNode,
        | 'title'
        | 'description'
        | 'type'
        | 'boundDocumentIds'
        | 'boundMemberIds'
        | 'boundPrototypeArtifactIds'
        | 'generatedDocIds'
        | 'missing'
      >
    >,
  ) => {
    setBlueprint((prev) => ({
      ...prev,
      status: prev.status === 'implemented' ? 'outdated' : prev.status,
      nodes: prev.nodes.map((node) => (node.id === id ? { ...node, ...updates } : node)),
      updatedAt: 'Just now',
    }))
    recordBlueprintOperation('updateNode', `Updated Blueprint node ${id}`)
  }

  const moveBlueprintNode = (id: string, position: { x: number; y: number }) => {
    setBlueprint((prev) => ({
      ...prev,
      status: prev.status === 'implemented' ? 'outdated' : prev.status,
      nodes: prev.nodes.map((node) => (node.id === id ? { ...node, position } : node)),
      updatedAt: 'Just now',
    }))
  }

  const deleteBlueprintNode = (id: string) => {
    setBlueprint((prev) => ({
      ...prev,
      status: prev.status === 'implemented' ? 'outdated' : 'draft',
      nodes: prev.nodes.filter((node) => node.id !== id),
      edges: prev.edges.filter((edge) => edge.sourceNodeId !== id && edge.targetNodeId !== id),
      updatedAt: 'Just now',
    }))
    if (selectedBlueprintNodeId === id) setSelectedBlueprintNodeId(null)
    recordBlueprintOperation('deleteNode', `Deleted Blueprint node ${id}`)
  }

  const createBlueprintEdge = (
    sourceNodeId: string,
    targetNodeId: string,
    type: BlueprintEdgeType,
    isRequired = type !== 'uses' && type !== 'syncs_with' && type !== 'implemented_by',
  ) => {
    if (sourceNodeId === targetNodeId) return
    setBlueprint((prev) => {
      const exists = prev.edges.some(
        (edge) =>
          edge.sourceNodeId === sourceNodeId &&
          edge.targetNodeId === targetNodeId &&
          edge.type === type,
      )
      if (exists) return prev
      return {
        ...prev,
        status: prev.status === 'implemented' ? 'outdated' : 'draft',
        edges: [
          ...prev.edges,
          {
            id: `be${Date.now()}`,
            sourceNodeId,
            targetNodeId,
            type,
            isRequired,
          },
        ],
        updatedAt: 'Just now',
      }
    })
    recordBlueprintOperation('createEdge', `Created ${type} edge`)
  }

  const updateBlueprintEdge = (
    id: string,
    updates: Partial<Pick<BlueprintEdge, 'type' | 'isRequired'>>,
  ) => {
    setBlueprint((prev) => ({
      ...prev,
      status: prev.status === 'implemented' ? 'outdated' : prev.status,
      edges: prev.edges.map((edge) => (edge.id === id ? { ...edge, ...updates } : edge)),
      updatedAt: 'Just now',
    }))
    recordBlueprintOperation('updateEdge', `Updated Blueprint edge ${id}`)
  }

  const deleteBlueprintEdge = (id: string) => {
    setBlueprint((prev) => ({
      ...prev,
      status: prev.status === 'implemented' ? 'outdated' : 'draft',
      edges: prev.edges.filter((edge) => edge.id !== id),
      updatedAt: 'Just now',
    }))
    recordBlueprintOperation('deleteEdge', `Deleted Blueprint edge ${id}`)
  }

  const saveBlueprint = () => {
    setBlueprint((prev) => ({
      ...prev,
      version: prev.version + 1,
      updatedAt: 'Just now',
    }))
    recordBlueprintOperation('updateNode', 'Saved Blueprint graph snapshot')
  }

  const validateBlueprint = () => {
    setBlueprint((prev) => {
      const nodes = prev.nodes.map((node) => ({
        ...node,
        missing: computeBlueprintNodeMissing(node, prev),
      }))
      const issueCount = nodes.reduce((sum, node) => sum + (node.missing?.length ?? 0), 0)
      return {
        ...prev,
        nodes,
        status: issueCount === 0 ? 'readyForDocs' : 'validated',
        updatedAt: 'Just now',
      }
    })
    recordBlueprintOperation('validateBlueprint', 'Validated Blueprint graph')
  }

  const completeBlueprintNode = (id: string) => {
    setBlueprint((prev) => ({
      ...prev,
      nodes: prev.nodes.map((node) =>
        node.id === id
          ? {
              ...node,
              description:
                node.description ??
                `AI completed acceptance notes, ownership and implementation hints for ${node.title}.`,
              boundMemberIds: node.boundMemberIds.length ? node.boundMemberIds : ['m1'],
              boundPrototypeArtifactIds:
                node.type === 'prototype' && node.boundPrototypeArtifactIds.length === 0
                  ? [`asset-${Date.now()}`]
                  : node.boundPrototypeArtifactIds,
              missing: [],
            }
          : node,
      ),
      status: prev.status === 'implemented' ? 'outdated' : prev.status,
      updatedAt: 'Just now',
    }))
    recordBlueprintOperation('updateNode', `AI completed Blueprint node ${id}`)
  }

  const startBlankBlueprint = () => {
    const nextBlueprint = blankBlueprint(`bp-${activeTeamProjectId}-${Date.now()}`, activeTeamProject.name)
    setBlueprint(nextBlueprint)
    setSelectedBlueprintNodeId(null)
    setTeamView('blueprint')
    recordBlueprintOperation('updateNode', `Started blank Blueprint for ${activeTeamProject.name}`)
  }

  const generateBlueprintFromProjectBrief = (brief = '') => {
    const nextBlueprint = generatedBlueprint(
      `bp-${activeTeamProjectId}-${Date.now()}`,
      activeTeamProject.name,
      brief,
    )
    setBlueprint(nextBlueprint)
    setSelectedBlueprintNodeId(nextBlueprint.nodes[0]?.id ?? null)
    setTeamView('blueprint')
    recordBlueprintOperation('createNode', `Generated Blueprint draft for ${activeTeamProject.name}`)
  }

  const generateBlueprintFromExistingDocs = () => {
    if (documents.length === 0) {
      generateBlueprintFromProjectBrief()
      return
    }
    const nextBlueprint = blueprintFromDocuments(
      `bp-${activeTeamProjectId}-${Date.now()}`,
      activeTeamProject.name,
      documents,
      dependencies,
    )
    setBlueprint(nextBlueprint)
    setSelectedBlueprintNodeId(nextBlueprint.nodes[0]?.id ?? null)
    setTeamView('blueprint')
    recordBlueprintOperation('createNode', `Generated Blueprint from ${documents.length} document(s)`)
  }

  const generateDocsFromBlueprintSelection = (selectedNodeId = selectedBlueprintNodeId ?? 'b1') => {
    const selected = blueprint.nodes.find((node) => node.id === selectedNodeId)
    if (!selected) return []
    const selection = collectBlueprintSelection(blueprint, selected.id)
    const createdDocs: TeamDocument[] = []
    const updatedDocIds: string[] = []
    const nodeDocIds = new Map<string, string[]>()
    const now = Date.now()

    selection.nodes.forEach((node, nodeIndex) => {
      const specs = BLUEPRINT_DOC_OUTPUTS[node.type]
      const docIds: string[] = []
      specs.forEach((spec, specIndex) => {
        const existingId = node.generatedDocIds[specIndex]
        const id = existingId ?? `d-bp-${now}-${nodeIndex}-${specIndex}`
        docIds.push(id)
        if (!existingId) {
          const inheritedMembers: TeamDocument['members'] =
            node.boundMemberIds.length > 0
              ? node.boundMemberIds.map((userId, index) => ({
                  userId,
                  role: index === 0 ? 'owner' : 'downstreamOwner',
                  boundReason: `Inherited from Blueprint node ${node.title}`,
                }))
              : [{ userId: 'm1', role: 'owner', boundReason: 'Default Blueprint owner' }]
          createdDocs.push({
            id,
            type: spec.type,
            title: `${node.title} ${spec.suffix}`,
            status: 'draft',
            ownerId: inheritedMembers[0]?.userId ?? 'm1',
            members: inheritedMembers,
            updatedAt: 'Just now',
            blocking: 0,
            bindings: 1,
            externalSync: node.type === 'prototype' ? 'figma' : null,
            position: {
              x: 180 + ((documents.length + createdDocs.length) % 4) * 180,
              y: 260 + Math.floor((documents.length + createdDocs.length) / 4) * 110,
            },
            summary: `Generated from Blueprint node "${node.title}" (${node.type}).`,
            sections: [
              {
                title: spec.sectionTitle,
                body: `This draft was generated from Blueprint "${blueprint.title}". It inherits node type ${node.type}, selected graph relationships and responsible members.`,
              },
              {
                title: 'Blueprint source',
                body: `Node ${node.title} participates in ${selection.edges.length} selected graph relationship(s).`,
              },
            ],
          })
        } else {
          updatedDocIds.push(existingId)
        }
      })
      nodeDocIds.set(node.id, docIds)
    })

    const generatedDocIds = unique([...Array.from(nodeDocIds.values()).flat()])
    setDocuments((prev) => [
      ...prev.map((doc) =>
        updatedDocIds.includes(doc.id)
          ? {
              ...doc,
              updatedAt: 'Just now',
              status: doc.status === 'approved' ? 'needsSync' : doc.status,
              sections: doc.sections.some((section) => section.title === 'Blueprint refresh')
                ? doc.sections.map((section) =>
                    section.title === 'Blueprint refresh'
                      ? {
                          ...section,
                          body: `Blueprint ${blueprint.title} refreshed this generated document from ${selected.title}.`,
                        }
                      : section,
                  )
                : [
                    ...doc.sections,
                    {
                      title: 'Blueprint refresh',
                      body: `Blueprint ${blueprint.title} refreshed this generated document from ${selected.title}.`,
                    },
                  ],
            }
          : doc,
      ),
      ...createdDocs,
    ])
    setNodeBindings((prev) => {
      const next = [...prev]
      selection.nodes.forEach((node) => {
        nodeDocIds.get(node.id)?.forEach((docId) => {
          const exists = next.some(
            (binding) =>
              binding.sourceId === node.id &&
              binding.targetKind === 'document' &&
              binding.targetId === docId,
          )
          if (!exists) {
            next.push({
              id: `nb-bp-${Date.now()}-${node.id}-${docId}`,
              sourceKind: bindingKindForBlueprintNode(node.type),
              sourceId: node.id,
              targetKind: 'document',
              targetId: docId,
              label: `${node.title} generates document`,
              relation: 'generates',
              isBlocking: false,
              requiredForReview: true,
              notifyOnChange: true,
              createdAt: new Date().toISOString(),
            })
          }
        })
      })
      return next
    })
    setDependencies((prev) => {
      const next = [...prev]
      selection.edges.forEach((edge) => {
        const sourceDocId = nodeDocIds.get(edge.sourceNodeId)?.[0]
        const targetDocId = nodeDocIds.get(edge.targetNodeId)?.[0]
        if (!sourceDocId || !targetDocId || sourceDocId === targetDocId) return
        const exists = next.some(
          (dependency) =>
            dependency.sourceDocId === sourceDocId && dependency.targetDocId === targetDocId,
        )
        if (!exists) {
          next.push({
            id: `e-bp-${Date.now()}-${edge.id}`,
            sourceDocId,
            targetDocId,
            type: edge.type === 'implemented_by' ? 'implements' : 'generates',
            isBlocking: edge.isRequired,
          })
        }
      })
      return next
    })
    setBlueprint((prev) => ({
      ...prev,
      status: 'docsGenerated',
      generatedDocIds: unique([...prev.generatedDocIds, ...generatedDocIds]),
      nodes: prev.nodes.map((node) =>
        nodeDocIds.has(node.id)
          ? {
              ...node,
              generatedDocIds: unique([...(node.generatedDocIds ?? []), ...(nodeDocIds.get(node.id) ?? [])]),
              boundDocumentIds: unique([
                ...node.boundDocumentIds,
                ...(nodeDocIds.get(node.id) ?? []),
              ]),
              missing: [],
            }
          : node,
      ),
      updatedAt: 'Just now',
    }))
    recordBlueprintOperation(
      'generateDocsFromSelection',
      `Generated ${generatedDocIds.length} document output(s) from ${selected.title}`,
    )
    return generatedDocIds
  }

  const createWorkbenchContextFromBlueprint = (selectedNodeId = selectedBlueprintNodeId ?? 'b1') => {
    const draft = createBlueprintWorkbenchContextDraft({
      blueprint,
      documents,
      selectedNodeId,
    })
    if (!draft) return
    const { context, prompt, linkedDocIds } = draft

    setBlueprint(draft.blueprint)
    setActiveBlueprintContext(context)
    setLinkedDocIds((prev) => unique([...prev, ...linkedDocIds]))
    setComposerDraft(prompt)
    setPlanMode(true)
    setTasks(BUILD_TASKS.map((task) => ({ ...task, status: 'pending', subStatus: undefined })))
    setPhase('planning')
    setView('preview')
    setSurface('workbench')
    recordBlueprintOperation(
      'createWorkbenchContext',
      `Sent "${context.title}" selection to Workbench`,
    )
  }

  const updateDocumentStatus = (id: string, status: DocStatus) => {
    setDocuments((prev) => prev.map((doc) => (doc.id === id ? { ...doc, status } : doc)))
  }

  const addDocumentMember = (docId: string, userId: string, role: DocMemberRole) => {
    setDocuments((prev) =>
      prev.map((doc) => {
        if (doc.id !== docId) return doc
        const exists = doc.members.some((member) => member.userId === userId && member.role === role)
        if (exists) return doc
        return {
          ...doc,
          updatedAt: 'Just now',
          members: [...doc.members, { userId, role }],
        }
      }),
    )
  }

  const removeDocumentMember = (docId: string, userId: string, role: DocMemberRole) => {
    setDocuments((prev) =>
      prev.map((doc) =>
        doc.id === docId
          ? {
              ...doc,
              updatedAt: 'Just now',
              members: doc.members.filter(
                (member) => member.userId !== userId || member.role !== role,
              ),
            }
          : doc,
      ),
    )
  }

  const saveDocumentDraft = (id: string) => {
    setDocuments((prev) =>
      prev.map((doc) =>
        doc.id === id
          ? {
              ...doc,
              status: doc.status === 'approved' ? doc.status : 'draft',
              updatedAt: 'Just now',
            }
          : doc,
      ),
    )
  }

  const syncWorkbenchBackToDocs = () => {
    const latestVersion = versions[versions.length - 1] ?? VERSIONS[1]
    const targetDocIds =
      activeBlueprintContext && activeBlueprintContext.linkedDocIds.length > 0
        ? activeBlueprintContext.linkedDocIds
        : ['d7']
    setDocuments((prev) => syncWorkbenchResultToDocuments(prev, targetDocIds, latestVersion))
    if (activeBlueprintContext) {
      setBlueprint((prev) =>
        syncWorkbenchResultToBlueprint({
          blueprint: prev,
          context: activeBlueprintContext,
          latestVersion,
          targetDocIds,
        }),
      )
      setActiveBlueprintContext((prev) =>
        prev
          ? {
              ...prev,
              status: 'implemented',
            }
          : prev,
      )
      recordBlueprintOperation(
        'syncWorkbenchResult',
        `Synced ${latestVersion.title} back to Blueprint target`,
      )
    }
  }

  const syncImportAsset = (id: string) => {
    if (importTimer.current) clearTimeout(importTimer.current)
    setImportAssets((prev) =>
      prev.map((asset) => (asset.id === id ? { ...asset, syncStatus: 'syncing' } : asset)),
    )
    importTimer.current = setTimeout(() => {
      setImportAssets((prev) =>
        prev.map((asset) =>
          asset.id === id
            ? { ...asset, syncStatus: 'connected', lastSyncedAt: 'Just now' }
            : asset,
        ),
      )
    }, 1200)
  }

  const detachImportAsset = (id: string) => {
    if (importTimer.current) {
      clearTimeout(importTimer.current)
      importTimer.current = null
    }
    setImportAssets((prev) =>
      prev.map((asset) =>
        asset.id === id
          ? {
              ...asset,
              syncStatus: 'notConnected',
              lastSyncedAt: undefined,
              linkedDocTitle: undefined,
            }
          : asset,
      ),
    )
  }

  const requestDatabaseSetup = () => {
    setView('database')
    setComposerDraft(
      'Create a Worksflow Database for this todo app with tables, authentication, server functions, secrets, user management, and file storage.',
    )
  }

  const value = useMemo<WorksflowState>(
    () => ({
      routeReady,
      surface,
      setSurface,
      projectName,
      setProjectName,
      projects,
      openProject,
      toggleProjectStar,
      duplicateProject,
      deleteProductProject,
      createProductProject,
      cloneProductProject,
      renameProductProject,
      importProductProject,
      selectedProductProjectId,
      selectPlatformProject,
      productProject: activeProductProject,
      updateGithubProjectSettings,
      updateDatabaseProjectSettings,
      phase,
      view,
      setView,
      tasks,
      versions,
      followUps,
      toggleVersionStar,
      startBuild,
      stopBuild,
      resetWorkbench,
      submitPrompt,
      retryGeneration,
      isGenerating,
      planMode,
      setPlanMode,
      linkedDocsOpen: true,
      composerDraft,
      setComposerDraft,
      requestDatabaseSetup,
      workspace,
      previewDocument,
      selectedWorkspaceFile,
      setSelectedWorkspaceFile,
      updateWorkspaceFile,
      createWorkspaceFile,
      deleteWorkspaceFile,
      renameWorkspaceFile,
      createWorkspaceCheckpoint,
      createWorkspaceBranch,
      restoreWorkspaceCheckpoint,
      undoWorkspaceRestore,
      canUndoWorkspaceRestore: Boolean(restoreRecoveryCheckpointId),
      workspaceHydrationStatus: projectCatalogPersistence.hydrationStatus,
      workspacePersistenceError: projectCatalogPersistence.error?.message,
      workspaceIsSaving: projectCatalogPersistence.isSaving,
      workspaceLastSavedAt: projectCatalogPersistence.lastSavedAt,
      resetWorkspacePersistence: () => {
        if (window.confirm(t('core.store.confirmReset'))) {
          projectCatalogPersistence.remove()
        }
      },
      workspaceHasExternalConflict: Boolean(projectCatalogPersistence.externalConflict),
      resolveWorkspaceExternalConflict: projectCatalogPersistence.resolveExternalConflict,
      attachments,
      addAttachment,
      removeAttachment,
      toggleAttachmentIncluded,
      clearAttachments,
      generationPlan,
      generationEvents,
      generationLifecycleEvents,
      generationSummary,
      generationError,
      generationErrorCode,
      generationErrorStatus,
      generationErrorRetryable,
      generationErrorCategory,
      generationErrorRetryAfterSeconds,
      generationErrorAction,
      generationProvider,
      generationModel,
      setGenerationModel,
      generationMode,
      setGenerationMode,
      generationUsage,
      generationDurationMs,
      generationCost,
      generationLimits,
      preferences,
      updateUserPreferences,
      promptHistory,
      promptTemplates,
      promptWorkflows,
      savePromptTemplate,
      deletePromptTemplate,
      savePromptWorkflow,
      deletePromptWorkflow,
      qualityRun,
      qualityRunning,
      qualityError,
      runWorkspaceQuality,
      attachQualityDiagnostics,
      deliveryStatus,
      deliveryError,
      deliveryLogs,
      deployments,
      publishedUrl,
      exportWorkspace,
      publishCurrentWorkspace,
      refreshDeployments,
      rollbackDeployment,
      todos,
      toggleTodo,
      addTodo,
      todoFilter,
      setTodoFilter,
      teamView,
      setTeamView,
      teamProjects,
      activeTeamProjectId,
      activeTeamProject,
      platformTeamFactsStatus,
      platformTeamFactsError,
      beginPlatformTeamFacts,
      applyPlatformTeamFacts,
      failPlatformTeamFacts,
      openTeamProject,
      createTeamProject,
      selectedDocId,
      setSelectedDocId,
      openDoc,
      createDocument,
      generateDocumentChain,
      createBlankDocumentGraph,
      createDocumentGraphFromTemplate,
      createDocumentGraphFromBlueprint,
      documents,
      moveDocumentNode,
      dependencies,
      nodeBindings,
      importAssets,
      linkedDocIds,
      blueprint,
      blueprintOperations,
      activeBlueprintContext,
      useDocInWorkbench,
      toggleLinkedDoc,
      addDocumentDependency,
      addNodeBinding,
      createBlueprintNode,
      updateBlueprintNode,
      moveBlueprintNode,
      deleteBlueprintNode,
      createBlueprintEdge,
      updateBlueprintEdge,
      deleteBlueprintEdge,
      saveBlueprint,
      validateBlueprint,
      completeBlueprintNode,
      startBlankBlueprint,
      generateBlueprintFromProjectBrief,
      generateBlueprintFromExistingDocs,
      generateDocsFromBlueprintSelection,
      createWorkbenchContextFromBlueprint,
      updateDocumentStatus,
      addDocumentMember,
      removeDocumentMember,
      saveDocumentDraft,
      syncWorkbenchBackToDocs,
      syncImportAsset,
      detachImportAsset,
      selectedBlueprintNodeId,
      setSelectedBlueprintNodeId,
      goToWorkbenchFromDoc,
    }),
    [
      surface,
      routeReady,
      projectName,
      projects,
      selectedProductProjectId,
      selectPlatformProject,
      phase,
      view,
      tasks,
      versions,
      followUps,
      isGenerating,
      planMode,
      composerDraft,
      workspace,
      previewDocument,
      selectedWorkspaceFile,
      restoreRecoveryCheckpointId,
      projectCatalogPersistence.hydrationStatus,
      projectCatalogPersistence.error,
      projectCatalogPersistence.isSaving,
      projectCatalogPersistence.lastSavedAt,
      projectCatalogPersistence.externalConflict,
      projectCatalogPersistence.resolveExternalConflict,
      attachments,
      generationPlan,
      generationEvents,
      generationLifecycleEvents,
      generationSummary,
      generationError,
      generationErrorCode,
      generationErrorStatus,
      generationErrorRetryable,
      generationErrorCategory,
      generationErrorRetryAfterSeconds,
      generationErrorAction,
      generationProvider,
      generationModel,
      generationMode,
      generationUsage,
      generationDurationMs,
      generationCost,
      generationLimits,
      preferences,
      promptHistory,
      promptTemplates,
      promptWorkflows,
      qualityRun,
      qualityRunning,
      qualityError,
      deliveryStatus,
      deliveryError,
      deliveryLogs,
      deployments,
      publishedUrl,
      todos,
      todoFilter,
      teamView,
      teamProjects,
      activeTeamProjectId,
      activeTeamProject,
      platformTeamFactsStatus,
      platformTeamFactsError,
      beginPlatformTeamFacts,
      applyPlatformTeamFacts,
      failPlatformTeamFacts,
      selectedDocId,
      documents,
      dependencies,
      nodeBindings,
      importAssets,
      linkedDocIds,
      blueprint,
      blueprintOperations,
      activeBlueprintContext,
      selectedBlueprintNodeId,
      startBuild,
      stopBuild,
      resetWorkbench,
      submitPrompt,
      retryGeneration,
      updateWorkspaceFile,
      createWorkspaceFile,
      deleteWorkspaceFile,
      renameWorkspaceFile,
      createWorkspaceCheckpoint,
      createWorkspaceBranch,
      restoreWorkspaceCheckpoint,
      undoWorkspaceRestore,
      addAttachment,
      removeAttachment,
      toggleAttachmentIncluded,
      clearAttachments,
      savePromptTemplate,
      deletePromptTemplate,
      savePromptWorkflow,
      deletePromptWorkflow,
      runWorkspaceQuality,
      attachQualityDiagnostics,
      updateUserPreferences,
      exportWorkspace,
      publishCurrentWorkspace,
      refreshDeployments,
      rollbackDeployment,
      t,
    ],
  )

  return <WorksflowContext.Provider value={value}>{children}</WorksflowContext.Provider>
}

export function useWorksflow() {
  const ctx = useContext(WorksflowContext)
  if (!ctx) throw new Error('useWorksflow must be used within WorksflowProvider')
  return ctx
}
