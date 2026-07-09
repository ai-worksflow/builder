import type {
  ActivityItem,
  Blueprint,
  BlueprintEdge,
  BlueprintNode,
  BuildTask,
  DocumentDependency,
  ImportAsset,
  Member,
  NodeBinding,
  PlanGroup,
  ProjectRecord,
  ProjectVersion,
  TeamDocument,
  TodoTask,
} from './types'

export const USER_PROMPT =
  'Create a simple todo app with a top navigation, task list, filters, and empty states. Use placeholder data only.'

export const PLAN_TITLE = 'Plan: Simple Todo App with Placeholder Data'

export const PLAN_GROUPS: PlanGroup[] = [
  {
    title: 'App Structure and State Foundation',
    items: [
      '定义 Task 类型与优先级枚举',
      '创建 placeholder 任务数据文件',
      '初始化 App 级别 state 管理',
    ],
  },
  {
    title: 'Top Navigation Bar',
    items: ['构建 Taskflow 顶部导航', '展示 Active / Done / Total 统计'],
  },
  {
    title: 'Task Input and Task List',
    items: ['任务输入框与优先级选择', '任务列表与完成勾选交互'],
  },
  {
    title: 'Filters and Empty States',
    items: ['All / Active / Completed 过滤器', '各筛选下的空态展示'],
  },
  {
    title: 'Polish and Responsiveness',
    items: ['响应式布局打磨', 'Made in Worksflow 标记'],
  },
]

export const PLAN_SUMMARY =
  'This plan builds a self-contained todo experience using placeholder data only — no backend calls. It covers the navigation shell, task input with priorities, a filterable task list, and empty states, finishing with responsive polish. To proceed, switch back to "build" mode and I will implement this plan.'

export const BUILD_TASKS: BuildTask[] = [
  { id: 't1', title: 'Create types and placeholder data files', status: 'pending' },
  { id: 't2', title: 'Build TopNavBar component', status: 'pending' },
  { id: 't3', title: 'Build TaskInput component', status: 'pending' },
  { id: 't4', title: 'Build TaskItem and TaskList components', status: 'pending' },
  { id: 't5', title: 'Build Filters component', status: 'pending' },
  { id: 't6', title: 'Build EmptyState component', status: 'pending' },
  { id: 't7', title: 'Wire everything together in App.tsx', status: 'pending' },
  { id: 't8', title: 'Verify build passes', status: 'pending' },
]

// Sub-status shown while a task is active, keyed by task id.
export const BUILD_SUBSTATUS: Record<string, string> = {
  t1: 'Writing src/data/tasks.ts',
  t2: 'Writing src/components/TopNavBar.tsx',
  t3: 'Writing src/components/TaskInput.tsx',
  t4: 'Writing src/components/TaskList.tsx',
  t5: 'Writing src/components/Filters.tsx',
  t6: 'Writing src/components/EmptyState.tsx',
  t7: 'Writing src/App.tsx',
  t8: 'Running build',
}

export const BUILD_SUMMARY =
  'Done. The todo app is built with placeholder data only and the production build passes cleanly.'

export const WHAT_WAS_BUILT = [
  'Top navigation',
  'Task input',
  'Task list',
  'Filters',
  'Empty states',
]

export const VERSIONS: ProjectVersion[] = [
  {
    id: 'v1',
    title: 'Create Todo App UI with Placeholder Data',
    subtitle: 'Version 1 at Jul 09 11:15 AM',
    starred: false,
  },
  {
    id: 'v2',
    title: 'Build Todo App with React State',
    subtitle: 'Version 2 at Jul 09 11:17 AM',
    starred: true,
  },
]

export const RECENT_PROJECTS: ProjectRecord[] = [
  {
    id: 'p1',
    name: 'Simple Todo App',
    teamName: 'Acme',
    phase: 'Generated preview',
    updatedAt: 'Just now',
    starred: true,
    linkedDocs: 4,
    latestVersion: 'Version 2',
  },
  {
    id: 'p2',
    name: 'CRM Rewrite',
    teamName: 'Acme',
    phase: 'Design & Contract',
    updatedAt: '1h ago',
    starred: false,
    linkedDocs: 7,
    latestVersion: 'Blueprint v4',
  },
  {
    id: 'p3',
    name: 'Billing Console',
    teamName: 'Acme',
    phase: 'Needs API contract',
    updatedAt: 'Yesterday',
    starred: false,
    linkedDocs: 5,
    latestVersion: 'Draft v3',
  },
]

export const TODO_TASKS: TodoTask[] = [
  {
    id: 'k1',
    title: 'Review the quarterly product roadmap',
    priority: 'High',
    when: '2 days ago',
    done: false,
  },
  {
    id: 'k2',
    title: 'Prepare slides for the design review meeting',
    priority: 'Med',
    when: 'Yesterday',
    done: false,
  },
  {
    id: 'k3',
    title: 'Send the onboarding welcome email to new hires',
    priority: 'Low',
    when: '4 days ago',
    done: true,
  },
  {
    id: 'k4',
    title: 'Fix the navigation overflow on mobile viewports',
    priority: 'High',
    when: 'Today',
    done: false,
  },
  {
    id: 'k5',
    title: 'Archive the completed sprint backlog items',
    priority: 'Med',
    when: '5 days ago',
    done: true,
  },
]

// ---------- Code view mock file tree ----------

export interface CodeFile {
  path: string
  name: string
  language: string
  content: string
}

export const CODE_FILES: CodeFile[] = [
  {
    path: 'src/data/tasks.ts',
    name: 'tasks.ts',
    language: 'ts',
    content: `import type { Task } from "../types"

export const placeholderTasks: Task[] = [
  { id: "1", title: "Review the quarterly product roadmap", priority: "high", done: false },
  { id: "2", title: "Prepare slides for the design review", priority: "med", done: false },
  { id: "3", title: "Send onboarding welcome email", priority: "low", done: true },
]`,
  },
  {
    path: 'src/components/TopNavBar.tsx',
    name: 'TopNavBar.tsx',
    language: 'tsx',
    content: `export function TopNavBar({ active, done, total }: Stats) {
  return (
    <header className="nav">
      <span className="brand">Taskflow</span>
      <div className="stats">
        <span>{active} Active</span>
        <span>{done} Done</span>
        <span>{total} Total</span>
      </div>
    </header>
  )
}`,
  },
  {
    path: 'src/components/TaskInput.tsx',
    name: 'TaskInput.tsx',
    language: 'tsx',
    content: `export function TaskInput({ onAdd }: { onAdd: (t: string) => void }) {
  const [value, setValue] = useState("")
  return (
    <form onSubmit={() => onAdd(value)}>
      <input placeholder="Add a new task..." value={value}
        onChange={(e) => setValue(e.target.value)} />
    </form>
  )
}`,
  },
  {
    path: 'src/components/TaskList.tsx',
    name: 'TaskList.tsx',
    language: 'tsx',
    content: `export function TaskList({ tasks, onToggle }: TaskListProps) {
  if (tasks.length === 0) return <EmptyState />
  return (
    <ul>
      {tasks.map((t) => (
        <TaskItem key={t.id} task={t} onToggle={onToggle} />
      ))}
    </ul>
  )
}`,
  },
  {
    path: 'src/App.tsx',
    name: 'App.tsx',
    language: 'tsx',
    content: `export default function App() {
  const [tasks, setTasks] = useState(placeholderTasks)
  const [filter, setFilter] = useState<Filter>("all")
  return (
    <div className="app">
      <TopNavBar {...stats} />
      <TaskInput onAdd={addTask} />
      <Filters value={filter} onChange={setFilter} />
      <TaskList tasks={visible} onToggle={toggleTask} />
    </div>
  )
}`,
  },
  {
    path: 'package.json',
    name: 'package.json',
    language: 'json',
    content: `{
  "name": "taskflow",
  "private": true,
  "scripts": { "dev": "vite", "build": "vite build" },
  "dependencies": { "react": "^19", "react-dom": "^19" }
}`,
  },
]

export const TERMINAL_LINES = [
  '[vite] connecting...',
  '[vite] connected.',
  '[vite] page reload src/components/TaskInput.tsx',
  '[vite] hmr update /src/App.tsx',
  'VITE v5.4.0  ready in 312 ms',
  '➜  Local:   http://localhost:5173/',
]

export const DATABASE_CAPABILITIES = [
  {
    icon: 'Table2',
    title: 'Tables',
    description: 'Create tables, manage rows, and query your data.',
  },
  {
    icon: 'KeyRound',
    title: 'Authentication',
    description: 'Add email, password, and social login to your app.',
  },
  {
    icon: 'FunctionSquare',
    title: 'Server functions',
    description: 'Run trusted server-side logic close to your data.',
  },
  {
    icon: 'Lock',
    title: 'Secrets',
    description: 'Store API keys and secrets securely for your project.',
  },
  {
    icon: 'Users',
    title: 'User management',
    description: 'Invite, manage, and control access for your users.',
  },
  {
    icon: 'HardDrive',
    title: 'File storage',
    description: 'Upload and serve images, documents, and media.',
  },
]

// ---------- Team members ----------

export const MEMBERS: Member[] = [
  { id: 'm1', name: 'Mia Chen', initials: 'MC', color: '#1488fc', title: 'Product Manager' },
  { id: 'm2', name: 'Leo Park', initials: 'LP', color: '#4ade80', title: 'Tech Lead' },
  { id: 'm3', name: 'Ava Ross', initials: 'AR', color: '#f59e0b', title: 'Product Designer' },
  { id: 'm4', name: 'Noah Kim', initials: 'NK', color: '#2ba6ff', title: 'Frontend Engineer' },
  { id: 'm5', name: 'Emma Diaz', initials: 'ED', color: '#ef4444', title: 'Backend Engineer' },
  { id: 'm6', name: 'Owen Wu', initials: 'OW', color: '#a78bfa', title: 'QA / Reviewer' },
]

// ---------- Team documents (default chain template) ----------

export const TEAM_DOCUMENTS: TeamDocument[] = [
  {
    id: 'd1',
    type: 'requirement',
    title: '需求文档',
    status: 'approved',
    ownerId: 'm1',
    members: [
      { userId: 'm1', role: 'owner' },
      { userId: 'm6', role: 'reviewer' },
    ],
    updatedAt: '2h ago',
    blocking: 0,
    bindings: 3,
    externalSync: null,
    position: { x: 40, y: 60 },
    summary: '定义 CRM Rewrite 的业务目标、用户角色、范围和验收口径。',
    sections: [
      { title: '业务目标', body: '把分散的客户数据整合到统一工作流，缩短销售响应时间。' },
      { title: '用户角色', body: '销售代表、销售主管、客户成功、管理员。' },
      { title: '范围', body: '客户列表、任务流转、权限、报表；不含计费。' },
      { title: '验收口径', body: '核心页面可用、权限可控、关键指标可导出。' },
    ],
  },
  {
    id: 'd2',
    type: 'pageSplit',
    title: '需求页面拆分文档',
    status: 'approved',
    ownerId: 'm1',
    members: [
      { userId: 'm1', role: 'owner' },
      { userId: 'm3', role: 'assignee' },
    ],
    updatedAt: '3h ago',
    blocking: 0,
    bindings: 2,
    externalSync: null,
    position: { x: 340, y: 60 },
    summary: '列出页面、路由、页面职责与入口关系。',
    sections: [
      { title: '页面列表', body: '/dashboard、/tasks、/tasks/:id、/members、/settings。' },
      { title: '入口关系', body: 'Dashboard 汇总入口，Tasks 为主要工作区。' },
    ],
  },
  {
    id: 'd3',
    type: 'featureList',
    title: '功能清单',
    status: 'readyForReview',
    ownerId: 'm2',
    members: [
      { userId: 'm2', role: 'owner' },
      { userId: 'm4', role: 'downstreamOwner' },
      { userId: 'm5', role: 'downstreamOwner' },
      { userId: 'm6', role: 'reviewer' },
    ],
    updatedAt: '1h ago',
    blocking: 2,
    bindings: 5,
    externalSync: null,
    position: { x: 640, y: 60 },
    summary: '功能点、优先级、状态与边界条件。阻塞 2 个下游文档。',
    sections: [
      { title: '任务流转', body: '创建、指派、状态流转、优先级、筛选。' },
      { title: '权限', body: 'Admin / Editor / Viewer 三级权限。' },
      { title: '边界条件', body: '离线态、批量操作上限、空态处理。' },
    ],
  },
  {
    id: 'd4',
    type: 'apiContract',
    title: 'API 契约',
    status: 'draft',
    ownerId: 'm5',
    members: [
      { userId: 'm5', role: 'owner' },
      { userId: 'm4', role: 'downstreamOwner' },
    ],
    updatedAt: '25m ago',
    blocking: 0,
    bindings: 4,
    externalSync: null,
    position: { x: 940, y: -40 },
    summary: 'endpoint、request、response、错误码与鉴权。',
    sections: [
      { title: 'GET /tasks', body: '返回任务列表，支持 status、priority 过滤。' },
      { title: 'PATCH /tasks/:id/status', body: '更新任务状态，需 Editor 权限。' },
      { title: '鉴权', body: 'Bearer token，401 / 403 错误码约定。' },
    ],
  },
  {
    id: 'd5',
    type: 'backendDev',
    title: '后端开发文档',
    status: 'draft',
    ownerId: 'm5',
    members: [{ userId: 'm5', role: 'owner' }],
    updatedAt: '20m ago',
    blocking: 0,
    bindings: 2,
    externalSync: null,
    position: { x: 1240, y: -40 },
    summary: '数据模型、服务拆分、任务清单与测试策略。',
    sections: [
      { title: '数据模型', body: 'Task、User、Role、Assignment。' },
      { title: '服务拆分', body: 'task-service、auth-service、report-service。' },
    ],
  },
  {
    id: 'd6',
    type: 'uiPrototype',
    title: '页面原型 UI 文档',
    status: 'needsSync',
    ownerId: 'm3',
    members: [
      { userId: 'm3', role: 'owner' },
      { userId: 'm4', role: 'downstreamOwner' },
    ],
    updatedAt: '40m ago',
    blocking: 1,
    bindings: 6,
    externalSync: 'figma',
    position: { x: 940, y: 160 },
    summary: '页面结构、组件状态、交互、空态与异常态。上游变更后需同步。',
    sections: [
      { title: '页面结构', body: 'Tasks 列表 + 筛选栏 + 详情侧栏。' },
      { title: '组件状态', body: 'empty / loading / error / success / disabled。' },
    ],
  },
  {
    id: 'd7',
    type: 'frontendDev',
    title: '前端开发文档',
    status: 'draft',
    ownerId: 'm4',
    members: [{ userId: 'm4', role: 'owner' }],
    updatedAt: '15m ago',
    blocking: 0,
    bindings: 3,
    externalSync: null,
    position: { x: 1240, y: 160 },
    summary: '组件拆分、状态管理、接口对接与测试点。',
    sections: [
      { title: '组件拆分', body: 'TaskList、FilterBar、TaskCard、SidePanel。' },
      { title: '状态管理', body: '本地 state + SWR 拉取任务数据。' },
    ],
  },
]

export const DOC_DEPENDENCIES: DocumentDependency[] = [
  { id: 'e1', sourceDocId: 'd1', targetDocId: 'd2', type: 'depends_on', isBlocking: false },
  { id: 'e2', sourceDocId: 'd2', targetDocId: 'd3', type: 'depends_on', isBlocking: false },
  { id: 'e3', sourceDocId: 'd3', targetDocId: 'd4', type: 'generates', isBlocking: true },
  { id: 'e4', sourceDocId: 'd3', targetDocId: 'd6', type: 'generates', isBlocking: true },
  { id: 'e5', sourceDocId: 'd4', targetDocId: 'd5', type: 'implements', isBlocking: false },
  { id: 'e6', sourceDocId: 'd6', targetDocId: 'd7', type: 'generates', isBlocking: false },
  { id: 'e7', sourceDocId: 'd1', targetDocId: 'd3', type: 'references', isBlocking: false },
]

export const NODE_BINDINGS: NodeBinding[] = [
  {
    id: 'nb1',
    sourceKind: 'document',
    sourceId: 'd3',
    targetKind: 'feature',
    targetId: 'b1',
    label: 'Task Workflow',
    relation: 'composes',
    isBlocking: false,
    requiredForReview: true,
    notifyOnChange: true,
    createdAt: '1h ago',
  },
  {
    id: 'nb2',
    sourceKind: 'document',
    sourceId: 'd6',
    targetKind: 'externalAsset',
    targetId: 'i1',
    label: 'CRM Rewrite — Task List',
    relation: 'syncs_with',
    isBlocking: false,
    requiredForReview: true,
    notifyOnChange: true,
    createdAt: '40m ago',
  },
  {
    id: 'nb3',
    sourceKind: 'document',
    sourceId: 'd7',
    targetKind: 'workbenchVersion',
    targetId: 'v2',
    label: 'Build Todo App with React State',
    relation: 'implements',
    isBlocking: false,
    requiredForReview: false,
    notifyOnChange: true,
    createdAt: '15m ago',
  },
]

// ---------- Blueprint ----------

export const BLUEPRINT_NODES: BlueprintNode[] = [
  {
    id: 'b1',
    type: 'feature',
    title: 'Task Workflow',
    description: '任务创建、指派、状态流转的核心能力。',
    position: { x: 60, y: 140 },
    boundDocumentIds: ['d3'],
    boundMemberIds: ['m2', 'm4'],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  },
  {
    id: 'b2',
    type: 'page',
    title: '/tasks',
    position: { x: 360, y: 40 },
    boundDocumentIds: ['d6'],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  },
  {
    id: 'b3',
    type: 'page',
    title: '/tasks/:id',
    position: { x: 360, y: 150 },
    boundDocumentIds: [],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  },
  {
    id: 'b4',
    type: 'component',
    title: 'FilterBar',
    position: { x: 360, y: 260 },
    boundDocumentIds: [],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  },
  {
    id: 'b5',
    type: 'component',
    title: 'TaskCard',
    position: { x: 360, y: 360 },
    boundDocumentIds: [],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  },
  {
    id: 'b6',
    type: 'api',
    title: 'GET /tasks',
    position: { x: 660, y: 40 },
    boundDocumentIds: ['d4'],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  },
  {
    id: 'b7',
    type: 'api',
    title: 'PATCH /tasks/:id/status',
    position: { x: 660, y: 150 },
    boundDocumentIds: [],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
    missing: ['无负责人'],
  },
  {
    id: 'b8',
    type: 'dataModel',
    title: 'Task',
    position: { x: 940, y: 90 },
    boundDocumentIds: [],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  },
  {
    id: 'b9',
    type: 'permission',
    title: 'Editor',
    position: { x: 660, y: 270 },
    boundDocumentIds: [],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
    missing: ['鉴权规则未定义'],
  },
  {
    id: 'b10',
    type: 'prototype',
    title: 'Figma Task List Frame',
    position: { x: 60, y: 340 },
    boundDocumentIds: [],
    boundMemberIds: ['m3'],
    boundPrototypeArtifactIds: ['asset-figma-task-list'],
    generatedDocIds: [],
  },
  {
    id: 'b11',
    type: 'workbenchTarget',
    title: 'Build v3',
    position: { x: 940, y: 300 },
    boundDocumentIds: [],
    boundMemberIds: [],
    boundPrototypeArtifactIds: [],
    generatedDocIds: [],
  },
]

export const BLUEPRINT_EDGES: BlueprintEdge[] = [
  { id: 'be1', sourceNodeId: 'b1', targetNodeId: 'b2', type: 'contains', isRequired: true },
  { id: 'be2', sourceNodeId: 'b1', targetNodeId: 'b3', type: 'contains', isRequired: true },
  { id: 'be3', sourceNodeId: 'b1', targetNodeId: 'b4', type: 'uses', isRequired: false },
  { id: 'be4', sourceNodeId: 'b1', targetNodeId: 'b5', type: 'uses', isRequired: false },
  { id: 'be5', sourceNodeId: 'b2', targetNodeId: 'b6', type: 'calls', isRequired: true },
  { id: 'be6', sourceNodeId: 'b3', targetNodeId: 'b7', type: 'calls', isRequired: true },
  { id: 'be7', sourceNodeId: 'b6', targetNodeId: 'b8', type: 'reads', isRequired: true },
  { id: 'be8', sourceNodeId: 'b7', targetNodeId: 'b8', type: 'writes', isRequired: true },
  { id: 'be9', sourceNodeId: 'b1', targetNodeId: 'b9', type: 'requires', isRequired: true },
  { id: 'be10', sourceNodeId: 'b1', targetNodeId: 'b10', type: 'syncs_with', isRequired: false },
  { id: 'be11', sourceNodeId: 'b1', targetNodeId: 'b11', type: 'implemented_by', isRequired: false },
]

export const DEFAULT_BLUEPRINT: Blueprint = {
  id: 'bp1',
  title: 'Task Workflow Blueprint',
  status: 'draft',
  ownerId: 'm1',
  nodes: BLUEPRINT_NODES,
  edges: BLUEPRINT_EDGES,
  generatedDocIds: [],
  version: 4,
  updatedAt: 'Just now',
}

export const MODULE_LIBRARY: { group: string; items: string[] }[] = [
  {
    group: 'Feature packs',
    items: [
      'Auth',
      'Team management',
      'Task workflow',
      'Search',
      'Notification',
      'Payment',
      'Reporting',
      'File upload',
    ],
  },
  {
    group: 'Page patterns',
    items: [
      'List page',
      'Detail page',
      'Form page',
      'Settings page',
      'Dashboard page',
      'Empty state page',
    ],
  },
  {
    group: 'API patterns',
    items: ['CRUD resource', 'Search endpoint', 'Auth endpoint', 'Webhook', 'Upload endpoint'],
  },
  {
    group: 'Data models',
    items: ['Task', 'User', 'Role', 'Invoice', 'Audit Log'],
  },
  {
    group: 'Permissions',
    items: ['Admin', 'Editor', 'Viewer', 'Billing Owner'],
  },
  {
    group: 'Prototype assets',
    items: ['Figma frame', 'Penpot board', 'tldraw canvas', 'Storybook story'],
  },
  {
    group: 'Workbench targets',
    items: ['Build v3', 'Refactor auth', 'Add billing', 'Preview sync'],
  },
  {
    group: 'UI patterns',
    items: ['Table', 'Filter bar', 'Kanban', 'Timeline', 'Modal form', 'Side panel'],
  },
]

// ---------- Imports ----------

export const IMPORT_ASSETS: ImportAsset[] = [
  {
    id: 'i1',
    source: 'figma',
    name: 'CRM Rewrite — Task List',
    syncStatus: 'connected',
    sourceUrl: 'https://figma.example/file/crm-rewrite',
    externalId: 'frame-task-list',
    linkedDocId: 'd6',
    lastSyncedAt: '10m ago',
    linkedDocTitle: '页面原型 UI 文档',
    snapshotUrl: '/placeholder.jpg',
    ownerId: 'm3',
  },
  {
    id: 'i2',
    source: 'penpot',
    name: 'Members & Permissions',
    syncStatus: 'outdated',
    sourceUrl: 'https://penpot.example/project/members-permissions',
    externalId: 'board-members-permissions',
    linkedDocId: 'd6',
    lastSyncedAt: '2d ago',
    linkedDocTitle: '页面原型 UI 文档',
    ownerId: 'm3',
  },
  {
    id: 'i3',
    source: 'excalidraw',
    name: 'Onboarding Flow Wireframe',
    syncStatus: 'connected',
    sourceUrl: 'https://excalidraw.example/onboarding-flow',
    externalId: 'onboarding-flow',
    lastSyncedAt: '1h ago',
    ownerId: 'm1',
  },
  {
    id: 'i4',
    source: 'tldraw',
    name: 'Product Flow Canvas',
    syncStatus: 'syncing',
    sourceUrl: 'https://tldraw.example/product-flow',
    externalId: 'product-flow',
    ownerId: 'm1',
  },
  {
    id: 'i5',
    source: 'storybook',
    name: 'Design System Stories',
    syncStatus: 'needsPermission',
    ownerId: 'm4',
  },
  {
    id: 'i6',
    source: 'upload',
    name: 'legacy-crm-screens.pdf',
    syncStatus: 'notConnected',
    ownerId: 'm1',
  },
]

export const ACTIVITY: ActivityItem[] = [
  { id: 'a1', memberId: 'm2', action: '更新了', target: '功能清单', time: '1h ago' },
  { id: 'a2', memberId: 'm3', action: '同步了原型', target: '页面原型 UI 文档', time: '40m ago' },
  { id: 'a3', memberId: 'm1', action: '把', target: '需求文档 设为 Approved', time: '2h ago' },
  { id: 'a4', memberId: 'm6', action: '开始评审', target: '功能清单', time: '30m ago' },
  { id: 'a5', memberId: 'm4', action: '创建了', target: '前端开发文档 草稿', time: '15m ago' },
]
