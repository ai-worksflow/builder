// Shared domain types for the Worksflow prototype.

export type Surface = 'workbench' | 'team' | 'recent' | 'settings'

export type TeamView =
  | 'dashboard'
  | 'graph'
  | 'editor'
  | 'blueprint'
  | 'prototype'
  | 'imports'
  | 'reviews'
  | 'members'

// ---------- Workbench ----------

export type Phase = 'planning' | 'planReady' | 'building' | 'complete' | 'error'
export type WorkbenchView = 'preview' | 'code' | 'database'
export type TaskStatus = 'pending' | 'active' | 'done' | 'error'

export interface BuildTask {
  id: string
  title: string
  status: TaskStatus
  subStatus?: string
}

export interface PlanGroup {
  title: string
  items: string[]
}

export interface ProjectVersion {
  id: string
  title: string
  subtitle: string
  starred: boolean
}

export interface ProjectRecord {
  id: string
  name: string
  teamName: string
  phase: string
  updatedAt: string
  starred: boolean
  linkedDocs: number
  latestVersion: string
}

export interface FollowUpRequest {
  id: string
  text: string
  mode: 'plan' | 'build'
  createdAt: string
}

export interface TodoTask {
  id: string
  title: string
  priority: 'Low' | 'Med' | 'High'
  when: string
  done: boolean
}

// ---------- Team collaboration ----------

export type DocType =
  | 'requirement'
  | 'pageSplit'
  | 'featureList'
  | 'apiContract'
  | 'backendDev'
  | 'uiPrototype'
  | 'frontendDev'

export type DocStatus =
  | 'draft'
  | 'readyForReview'
  | 'changesRequested'
  | 'approved'
  | 'needsSync'
  | 'archived'

export type DependencyType =
  | 'depends_on'
  | 'generates'
  | 'blocks'
  | 'implements'
  | 'reviews'
  | 'references'
  | 'composes'
  | 'derives_from'
  | 'syncs_with'

export type DocMemberRole =
  | 'owner'
  | 'assignee'
  | 'downstreamOwner'
  | 'reviewer'
  | 'watcher'

export type BindingTargetKind =
  | 'document'
  | 'member'
  | 'blueprint'
  | 'feature'
  | 'page'
  | 'component'
  | 'api'
  | 'dataModel'
  | 'permission'
  | 'prototype'
  | 'workbenchVersion'
  | 'externalAsset'

export interface Member {
  id: string
  name: string
  initials: string
  color: string
  title: string
}

export interface DocumentMember {
  userId: string
  role: DocMemberRole
  boundReason?: string
}

export interface TeamDocument {
  id: string
  teamId?: string
  projectId?: string
  type: DocType
  title: string
  status: DocStatus
  ownerId: string
  members: DocumentMember[]
  updatedAt: string
  blocking: number
  bindings: number
  externalSync?: 'figma' | 'penpot' | 'tldraw' | null
  position: { x: number; y: number }
  summary: string
  sections: { title: string; body: string }[]
  version?: number
  lastApprovedVersion?: number
  prototypeArtifactIds?: string[]
}

export interface DocumentDependency {
  id: string
  sourceDocId: string
  targetDocId: string
  type: DependencyType
  isBlocking: boolean
}

export interface NodeBinding {
  id: string
  sourceKind: BindingTargetKind
  sourceId: string
  targetKind: BindingTargetKind
  targetId: string
  label: string
  relation: DependencyType | BlueprintEdgeType
  isBlocking: boolean
  requiredForReview: boolean
  notifyOnChange: boolean
  inheritedFromBindingId?: string
  createdBy?: string
  createdAt: string
}

export type TeamProjectSource = 'blank' | 'template' | 'blueprint' | 'import'

export interface TeamProject {
  id: string
  teamId: string
  teamName: string
  name: string
  phase: string
  updatedAt: string
  source: TeamProjectSource
  graphId: string
  blueprintId: string
  documents: TeamDocument[]
  dependencies: DocumentDependency[]
  nodeBindings: NodeBinding[]
  importAssets: ImportAsset[]
  linkedDocIds: string[]
  blueprint: Blueprint
}

// ---------- Blueprint ----------

export type BlueprintNodeType =
  | 'feature'
  | 'page'
  | 'component'
  | 'api'
  | 'dataModel'
  | 'permission'
  | 'prototype'
  | 'workbenchTarget'

export type BlueprintEdgeType =
  | 'contains'
  | 'uses'
  | 'calls'
  | 'reads'
  | 'writes'
  | 'requires'
  | 'renders'
  | 'syncs_with'
  | 'generates'
  | 'implemented_by'

export type BlueprintStatus =
  | 'draft'
  | 'validated'
  | 'readyForDocs'
  | 'docsGenerated'
  | 'inImplementation'
  | 'implemented'
  | 'outdated'

export interface BlueprintNode {
  id: string
  type: BlueprintNodeType
  title: string
  description?: string
  position: { x: number; y: number }
  boundDocumentIds: string[]
  boundMemberIds: string[]
  boundPrototypeArtifactIds: string[]
  generatedDocIds: string[]
  missing?: string[]
}

export interface BlueprintEdge {
  id: string
  sourceNodeId: string
  targetNodeId: string
  type: BlueprintEdgeType
  isRequired: boolean
}

export interface Blueprint {
  id: string
  title: string
  status: BlueprintStatus
  ownerId: string
  nodes: BlueprintNode[]
  edges: BlueprintEdge[]
  generatedDocIds: string[]
  version: number
  updatedAt: string
}

export interface BlueprintWorkbenchContext {
  id: string
  blueprintId: string
  selectedNodeId: string
  title: string
  prompt: string
  nodeIds: string[]
  edgeIds: string[]
  linkedDocIds: string[]
  memberIds: string[]
  prototypeNodeIds: string[]
  missingItems: string[]
  workbenchTargetId: string
  status: 'draft' | 'inImplementation' | 'implemented'
  createdAt: string
}

export type BlueprintOperationType =
  | 'createNode'
  | 'updateNode'
  | 'moveNode'
  | 'deleteNode'
  | 'createEdge'
  | 'updateEdge'
  | 'deleteEdge'
  | 'bindTarget'
  | 'unbindTarget'
  | 'validateBlueprint'
  | 'generateDocsFromSelection'
  | 'createWorkbenchContext'
  | 'syncWorkbenchResult'

export interface BlueprintOperation {
  id: string
  type: BlueprintOperationType
  blueprintId: string
  summary: string
  createdAt: string
}

// ---------- Prototype / imports ----------

export type ImportSource =
  | 'figma'
  | 'penpot'
  | 'excalidraw'
  | 'tldraw'
  | 'storybook'
  | 'upload'

export type SyncStatus =
  | 'notConnected'
  | 'connected'
  | 'syncing'
  | 'needsPermission'
  | 'syncFailed'
  | 'outdated'

export interface ImportAsset {
  id: string
  source: ImportSource
  name: string
  syncStatus: SyncStatus
  sourceUrl?: string
  externalId?: string
  linkedDocId?: string
  lastSyncedAt?: string
  linkedDocTitle?: string
  snapshotUrl?: string
  ownerId: string
}

export interface ActivityItem {
  id: string
  memberId: string
  action: string
  target: string
  time: string
}
