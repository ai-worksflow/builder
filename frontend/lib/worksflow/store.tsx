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
  isGenerating: boolean
  planMode: boolean
  setPlanMode: (v: boolean) => void
  linkedDocsOpen: boolean
  composerDraft: string
  setComposerDraft: (text: string) => void
  requestDatabaseSetup: () => void

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
  const [surface, setSurface] = useState<Surface>('workbench')
  const [routeReady, setRouteReady] = useState(false)
  const [projectName, setProjectName] = useState('Simple Todo App')
  const [projects, setProjects] = useState<ProjectRecord[]>(RECENT_PROJECTS)

  // Workbench begins in planning, then auto advances to planReady.
  const [phase, setPhase] = useState<Phase>('planning')
  const [view, setView] = useState<WorkbenchView>('preview')
  const [tasks, setTasks] = useState<BuildTask[]>(BUILD_TASKS)
  const [versions, setVersions] = useState<ProjectVersion[]>(VERSIONS.slice(0, 1))
  const [followUps, setFollowUps] = useState<FollowUpRequest[]>([])
  const [planMode, setPlanMode] = useState(true)
  const [composerDraft, setComposerDraft] = useState('')

  const [todos, setTodos] = useState<TodoTask[]>(TODO_TASKS)
  const [todoFilter, setTodoFilter] = useState<'all' | 'active' | 'completed'>('all')

  const [teamView, setTeamView] = useState<TeamView>('dashboard')
  const [teamProjects, setTeamProjects] = useState<TeamProject[]>(INITIAL_TEAM_PROJECTS)
  const [activeTeamProjectId, setActiveTeamProjectId] = useState(INITIAL_TEAM_PROJECTS[0].id)
  const [selectedDocId, setSelectedDocId] = useState<string | null>('d3')
  const [selectedBlueprintNodeId, setSelectedBlueprintNodeId] = useState<string | null>('b1')
  const [documents, setDocuments] = useState<TeamDocument[]>(INITIAL_TEAM_PROJECTS[0].documents)
  const [dependencies, setDependencies] = useState<DocumentDependency[]>(INITIAL_TEAM_PROJECTS[0].dependencies)
  const [nodeBindings, setNodeBindings] = useState<NodeBinding[]>(INITIAL_TEAM_PROJECTS[0].nodeBindings)
  const [importAssets, setImportAssets] = useState<ImportAsset[]>(INITIAL_TEAM_PROJECTS[0].importAssets)
  const [linkedDocIds, setLinkedDocIds] = useState<string[]>(INITIAL_TEAM_PROJECTS[0].linkedDocIds)
  const [blueprint, setBlueprint] = useState<Blueprint>(INITIAL_TEAM_PROJECTS[0].blueprint)
  const [blueprintOperations, setBlueprintOperations] = useState<BlueprintOperation[]>([])
  const [activeBlueprintContext, setActiveBlueprintContext] =
    useState<BlueprintWorkbenchContext | null>(null)

  const buildTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const importTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const skipTeamProjectSync = useRef(false)
  const nextVersion = useRef<ProjectVersion>(VERSIONS[1])
  const versionSerial = useRef(2)

  const isGenerating = phase === 'planning' || phase === 'building'
  const activeTeamProject =
    teamProjects.find((project) => project.id === activeTeamProjectId) ?? teamProjects[0]

  const recordBlueprintOperation = (type: BlueprintOperation['type'], summary: string) => {
    setBlueprintOperations((prev) => [
      {
        id: `op${Date.now()}`,
        type,
        blueprintId: blueprint.id,
        summary,
        createdAt: 'Just now',
      },
      ...prev,
    ].slice(0, 8))
  }

  useEffect(() => {
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
  ])

  // Auto-advance planning -> planReady (Flow A)
  useEffect(() => {
    if (phase !== 'planning') return
    const t = setTimeout(() => setPhase('planReady'), 2600)
    return () => clearTimeout(t)
  }, [phase])

  const clearBuildTimer = () => {
    if (buildTimer.current) {
      clearTimeout(buildTimer.current)
      buildTimer.current = null
    }
  }

  // Flow B: step through build tasks one at a time.
  const runBuildStep = useCallback((index: number) => {
    setTasks((prev) =>
      prev.map((t, i) => {
        if (i < index) return { ...t, status: 'done', subStatus: undefined }
        if (i === index) return { ...t, status: 'active', subStatus: BUILD_SUBSTATUS[t.id] }
        return { ...t, status: 'pending', subStatus: undefined }
      }),
    )

    buildTimer.current = setTimeout(() => {
      const next = index + 1
      if (next >= BUILD_TASKS.length) {
        setTasks((prev) => prev.map((t) => ({ ...t, status: 'done', subStatus: undefined })))
        setPhase('complete')
        setVersions((prev) => {
          const nextItem = nextVersion.current
          return prev.some((v) => v.id === nextItem.id) ? prev : [...prev, nextItem]
        })
        setView('preview')
      } else {
        runBuildStep(next)
      }
    }, 1100)
  }, [])

  const startBuild = useCallback(() => {
    clearBuildTimer()
    nextVersion.current = VERSIONS[1]
    versionSerial.current = Math.max(versionSerial.current, 2)
    setTasks(BUILD_TASKS.map((t) => ({ ...t, status: 'pending', subStatus: undefined })))
    setPlanMode(false)
    setPhase('building')
    setView('preview')
    runBuildStep(0)
  }, [runBuildStep])

  const submitPrompt = useCallback(
    (text: string) => {
      const prompt = text.trim()
      if (!prompt || phase === 'building' || phase === 'planning') return

      const versionNo = versionSerial.current + 1
      versionSerial.current = versionNo
      nextVersion.current = {
        id: `v${versionNo}`,
        title: `Iterate: ${prompt.length > 46 ? `${prompt.slice(0, 46)}...` : prompt}`,
        subtitle: `Version ${versionNo} at Jul 09 11:${13 + versionNo * 2} AM`,
        starred: false,
      }
      setFollowUps((prev) => [
        ...prev,
        {
          id: `f${Date.now()}`,
          text: prompt,
          mode: planMode ? 'plan' : 'build',
          createdAt: 'Just now',
        },
      ])
      setComposerDraft('')
      setTasks(BUILD_TASKS.map((t) => ({ ...t, status: 'pending', subStatus: undefined })))

      if (planMode) {
        setPhase('planning')
        setView('preview')
      } else {
        clearBuildTimer()
        setPhase('building')
        setView('preview')
        runBuildStep(0)
      }
    },
    [phase, planMode, runBuildStep],
  )

  const stopBuild = useCallback(() => {
    clearBuildTimer()
    if (phase === 'building') {
      setTasks((prev) =>
        prev.map((t) =>
          t.status === 'active' ? { ...t, status: 'error', subStatus: 'Stopped' } : t,
        ),
      )
      setPhase('error')
    } else if (phase === 'planning') {
      setPhase('planReady')
    }
  }, [phase])

  const resetWorkbench = useCallback(() => {
    clearBuildTimer()
    setTasks(BUILD_TASKS.map((t) => ({ ...t, status: 'pending', subStatus: undefined })))
    setVersions(VERSIONS.slice(0, 1))
    setFollowUps([])
    nextVersion.current = VERSIONS[1]
    versionSerial.current = 2
    setPlanMode(true)
    setView('preview')
    setPhase('planning')
  }, [])

  useEffect(
    () => () => {
      clearBuildTimer()
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
      { id: `k${Date.now()}`, title: title.trim(), priority, when: 'Just now', done: false },
      ...prev,
    ])
  }

  const loadTeamProject = (project: TeamProject) => {
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
  }

  const openTeamProject = (id: string) => {
    const project = teamProjects.find((item) => item.id === id)
    if (!project) return
    loadTeamProject(project)
    setSurface('team')
    setTeamView(project.documents.length === 0 ? 'dashboard' : 'graph')
  }

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
    setProjects((prev) => [
      {
        id,
        name,
        teamName: project.teamName,
        phase: project.phase,
        updatedAt: 'Just now',
        starred: false,
        linkedDocs: project.documents.length,
        latestVersion: project.blueprint.nodes.length ? `Blueprint v${project.blueprint.version}` : 'Blank',
      },
      ...prev,
    ])
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
    const project = projects.find((item) => item.id === id)
    if (!project) return
    setProjectName(project.name)
    setSurface(id === 'p2' ? 'team' : 'workbench')
  }

  useEffect(() => {
    if (routeReady || typeof window === 'undefined') return

    const route = parseWorksflowRoute(window.location.pathname, window.location.search)

    if (route.surface === 'workbench') {
      clearBuildTimer()
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
          subStatus: index === 0 ? 'Stopped' : undefined,
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
  }, [routeReady, teamProjects])

  const toggleProjectStar = (id: string) => {
    setProjects((prev) =>
      prev.map((project) =>
        project.id === id ? { ...project, starred: !project.starred } : project,
      ),
    )
  }

  const duplicateProject = () => {
    const id = `p${Date.now()}`
    const name = `${projectName} Copy`
    setProjects((prev) => [
      {
        id,
        name,
        teamName: 'Acme',
        phase: 'Draft duplicate',
        updatedAt: 'Just now',
        starred: false,
        linkedDocs: linkedDocIds.length,
        latestVersion: versions[versions.length - 1]?.subtitle.split(' at ')[0] ?? 'Version 1',
      },
      ...prev,
    ])
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
        createdAt: 'Just now',
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
              createdAt: 'Just now',
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
      isGenerating,
      planMode,
      setPlanMode,
      linkedDocsOpen: true,
      composerDraft,
      setComposerDraft,
      requestDatabaseSetup,
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
      phase,
      view,
      tasks,
      versions,
      followUps,
      isGenerating,
      planMode,
      composerDraft,
      todos,
      todoFilter,
      teamView,
      teamProjects,
      activeTeamProjectId,
      activeTeamProject,
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
    ],
  )

  return <WorksflowContext.Provider value={value}>{children}</WorksflowContext.Provider>
}

export function useWorksflow() {
  const ctx = useContext(WorksflowContext)
  if (!ctx) throw new Error('useWorksflow must be used within WorksflowProvider')
  return ctx
}
