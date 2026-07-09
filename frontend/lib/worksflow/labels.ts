import type {
  BlueprintEdgeType,
  BlueprintNodeType,
  DependencyType,
  DocMemberRole,
  DocStatus,
  DocType,
  ImportSource,
  SyncStatus,
} from './types'
import type { MessageKey } from '@/lib/i18n'

export const DOC_TYPE_LABEL_KEY: Record<DocType, MessageKey> = {
  requirement: 'doc.type.requirement',
  pageSplit: 'doc.type.pageSplit',
  featureList: 'doc.type.featureList',
  apiContract: 'doc.type.apiContract',
  backendDev: 'doc.type.backendDev',
  uiPrototype: 'doc.type.uiPrototype',
  frontendDev: 'doc.type.frontendDev',
}

export const DOC_TYPE_LABEL: Record<DocType, string> = {
  requirement: '需求文档',
  pageSplit: '需求页面拆分',
  featureList: '功能清单',
  apiContract: 'API 契约',
  backendDev: '后端开发文档',
  uiPrototype: '页面原型 UI',
  frontendDev: '前端开发文档',
}

export const DOC_STATUS_LABEL_KEY: Record<DocStatus, MessageKey> = {
  draft: 'doc.status.draft',
  readyForReview: 'doc.status.readyForReview',
  changesRequested: 'doc.status.changesRequested',
  approved: 'doc.status.approved',
  needsSync: 'doc.status.needsSync',
  archived: 'doc.status.archived',
}

export const DOC_STATUS_LABEL: Record<DocStatus, string> = {
  draft: 'Draft',
  readyForReview: 'Ready for Review',
  changesRequested: 'Changes Requested',
  approved: 'Approved',
  needsSync: 'Needs Sync',
  archived: 'Archived',
}

// Tailwind classes for status pills (text + bg).
export const DOC_STATUS_CLASS: Record<DocStatus, string> = {
  draft: 'text-muted-foreground bg-white/5 border border-border',
  readyForReview: 'text-primary-bright bg-primary/10 border border-primary/30',
  changesRequested: 'text-warning bg-amber-400/10 border border-amber-400/30',
  approved: 'text-success bg-emerald-400/10 border border-emerald-400/30',
  needsSync: 'text-warning bg-amber-400/10 border border-amber-400/30',
  archived: 'text-faint-foreground bg-white/5 border border-border',
}

export const ROLE_LABEL_KEY: Record<DocMemberRole, MessageKey> = {
  owner: 'role.owner',
  assignee: 'role.assignee',
  downstreamOwner: 'role.downstreamOwner',
  reviewer: 'role.reviewer',
  watcher: 'role.watcher',
}

export const ROLE_LABEL: Record<DocMemberRole, string> = {
  owner: 'Owner',
  assignee: 'Assignee',
  downstreamOwner: 'Downstream Owner',
  reviewer: 'Reviewer',
  watcher: 'Watcher',
}

export const DEP_TYPE_LABEL_KEY: Record<DependencyType, MessageKey> = {
  depends_on: 'dep.depends_on',
  generates: 'dep.generates',
  blocks: 'dep.blocks',
  implements: 'dep.implements',
  reviews: 'dep.reviews',
  references: 'dep.references',
  composes: 'dep.composes',
  derives_from: 'dep.derives_from',
  syncs_with: 'dep.syncs_with',
}

export const DEP_TYPE_LABEL: Record<DependencyType, string> = {
  depends_on: 'depends on',
  generates: 'generates',
  blocks: 'blocks',
  implements: 'implements',
  reviews: 'reviews',
  references: 'references',
  composes: 'composes',
  derives_from: 'derives from',
  syncs_with: 'syncs with',
}

export const BLUEPRINT_NODE_LABEL_KEY: Record<BlueprintNodeType, MessageKey> = {
  feature: 'blueprint.node.feature',
  page: 'blueprint.node.page',
  component: 'blueprint.node.component',
  api: 'blueprint.node.api',
  dataModel: 'blueprint.node.dataModel',
  permission: 'blueprint.node.permission',
  prototype: 'blueprint.node.prototype',
  workbenchTarget: 'blueprint.node.workbenchTarget',
}

export const BLUEPRINT_NODE_LABEL: Record<BlueprintNodeType, string> = {
  feature: 'Feature',
  page: 'Page',
  component: 'Component',
  api: 'API',
  dataModel: 'Data Model',
  permission: 'Permission',
  prototype: 'Prototype',
  workbenchTarget: 'Workbench Target',
}

export const BLUEPRINT_NODE_COLOR: Record<BlueprintNodeType, string> = {
  feature: '#1488fc',
  page: '#2ba6ff',
  component: '#a78bfa',
  api: '#4ade80',
  dataModel: '#f59e0b',
  permission: '#ef4444',
  prototype: '#ec4899',
  workbenchTarget: '#22d3ee',
}

export const BLUEPRINT_EDGE_LABEL: Record<BlueprintEdgeType, string> = {
  contains: 'contains',
  uses: 'uses',
  calls: 'calls',
  reads: 'reads',
  writes: 'writes',
  requires: 'requires',
  renders: 'renders',
  syncs_with: 'syncs with',
  generates: 'generates',
  implemented_by: 'implemented by',
}

export const BLUEPRINT_EDGE_LABEL_KEY: Record<BlueprintEdgeType, MessageKey> = {
  contains: 'blueprint.edge.contains',
  uses: 'blueprint.edge.uses',
  calls: 'blueprint.edge.calls',
  reads: 'blueprint.edge.reads',
  writes: 'blueprint.edge.writes',
  requires: 'blueprint.edge.requires',
  renders: 'blueprint.edge.renders',
  syncs_with: 'blueprint.edge.syncs_with',
  generates: 'blueprint.edge.generates',
  implemented_by: 'blueprint.edge.implemented_by',
}

export const IMPORT_SOURCE_LABEL_KEY: Record<ImportSource, MessageKey> = {
  figma: 'import.source.figma',
  penpot: 'import.source.penpot',
  excalidraw: 'import.source.excalidraw',
  tldraw: 'import.source.tldraw',
  storybook: 'import.source.storybook',
  upload: 'import.source.upload',
}

export const IMPORT_SOURCE_LABEL: Record<ImportSource, string> = {
  figma: 'Figma',
  penpot: 'Penpot',
  excalidraw: 'Excalidraw',
  tldraw: 'tldraw',
  storybook: 'Storybook / Ladle',
  upload: 'Image / SVG / PDF',
}

export const SYNC_STATUS_LABEL_KEY: Record<SyncStatus, MessageKey> = {
  notConnected: 'sync.status.notConnected',
  connected: 'sync.status.connected',
  syncing: 'sync.status.syncing',
  needsPermission: 'sync.status.needsPermission',
  syncFailed: 'sync.status.syncFailed',
  outdated: 'sync.status.outdated',
}

export const SYNC_STATUS_LABEL: Record<SyncStatus, string> = {
  notConnected: 'Not connected',
  connected: 'Connected',
  syncing: 'Syncing',
  needsPermission: 'Needs permission',
  syncFailed: 'Sync failed',
  outdated: 'Outdated',
}

export const SYNC_STATUS_CLASS: Record<SyncStatus, string> = {
  notConnected: 'text-faint-foreground bg-white/5 border border-border',
  connected: 'text-success bg-emerald-400/10 border border-emerald-400/30',
  syncing: 'text-primary-bright bg-primary/10 border border-primary/30',
  needsPermission: 'text-warning bg-amber-400/10 border border-amber-400/30',
  syncFailed: 'text-destructive bg-red-500/10 border border-red-500/30',
  outdated: 'text-warning bg-amber-400/10 border border-amber-400/30',
}
