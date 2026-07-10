'use client'

import { useState } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n } from '@/lib/i18n'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import type { ArtifactStatus } from '@/lib/platform/dto'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import {
  Boxes,
  CircleSlash2,
  Component,
  ExternalLink,
  FileUp,
  Frame,
  GitFork,
  MonitorPlay,
  PackageCheck,
  PenTool,
  RefreshCw,
  Server,
  Shapes,
  ShieldCheck,
} from 'lucide-react'

const CONNECTORS = [
  { id: 'figma', label: 'Figma', icon: Frame },
  { id: 'penpot', label: 'Penpot', icon: PenTool },
  { id: 'excalidraw', label: 'Excalidraw', icon: Shapes },
  { id: 'tldraw', label: 'tldraw', icon: Frame },
  { id: 'storybook', label: 'Storybook', icon: Component },
  { id: 'upload', label: 'File upload', icon: FileUp },
] as const

const COPY = {
  'zh-CN': {
    description: '查看当前项目的真实原型上下文，以及此部署可用的外部导入能力。',
    unavailableTitle: '外部导入连接器尚未配置',
    unavailableBody: '当前前端与 Go 服务没有注册外部导入端点、OAuth 连接器或文件接收流水线，因此不会接受 URL、凭证或上传文件。',
    unavailableSafety: '此页面采用 fail-closed：不会在浏览器生成“已连接”“已同步”或“已解除关联”的本地状态。',
    notConfigured: '未配置',
    connectorRequirement: '启用连接器前，需要服务端连接器注册、密钥托管、来源校验、可审查导入提案以及审计事件。',
    serverContext: '真实服务器上下文',
    project: '项目',
    role: '当前角色',
    backend: '后端状态',
    prototypes: '原型',
    pageSpecs: '页面规格',
    documents: '文档',
    approved: '已批准原型',
    noProject: '尚未选择服务器项目',
    signedOut: '需要登录后才能读取项目产物。',
    refresh: '刷新服务器数据',
    refreshing: '正在刷新',
    serverPrototypes: '服务器原型产物',
    serverPrototypesBody: '以下内容来自当前项目的 /prototypes 集合，不代表外部导入或同步状态。',
    noPrototypes: '当前项目还没有服务器原型。请先从 Blueprint 拆分 PageSpec，再在原型工作室创建原型。',
    artifact: '原型产物',
    status: '状态',
    pageSpecSource: '固定 PageSpec 来源',
    revision: '最新不可变版本',
    updated: '更新时间',
    action: '操作',
    draftOnly: '仅草稿',
    openStudio: '打开原型工作室',
    nativeArtifact: '平台原生产物',
    navigationTitle: '继续使用已实现的流程',
    navigationBody: '这些入口只做真实页面导航，不会创建或修改导入记录。',
    graph: '文档依赖图',
    blueprint: '蓝图编辑器',
    prototype: '原型工作室',
    workbench: '应用工作台',
  },
  'en-US': {
    description: 'Inspect the active project’s real prototype context and the external import capabilities available in this deployment.',
    unavailableTitle: 'External import connectors are not configured',
    unavailableBody: 'The current frontend and Go service register no external import endpoint, OAuth connector, or file-ingestion pipeline, so URLs, credentials, and uploads are not accepted.',
    unavailableSafety: 'This page fails closed: it never creates browser-only connected, synced, or detached states.',
    notConfigured: 'Not configured',
    connectorRequirement: 'Enabling a connector requires a server connector registry, managed secrets, source verification, reviewable import proposals, and audit events.',
    serverContext: 'Real server context',
    project: 'Project',
    role: 'Current role',
    backend: 'Backend',
    prototypes: 'Prototypes',
    pageSpecs: 'PageSpecs',
    documents: 'Documents',
    approved: 'Approved prototypes',
    noProject: 'No server project selected',
    signedOut: 'Sign in before reading project artifacts.',
    refresh: 'Refresh server data',
    refreshing: 'Refreshing',
    serverPrototypes: 'Server prototype artifacts',
    serverPrototypesBody: 'These records come from the active project’s /prototypes collection. They do not imply an external import or sync state.',
    noPrototypes: 'This project has no server prototype yet. Split a PageSpec from the Blueprint, then create a prototype in Prototype Studio.',
    artifact: 'Prototype artifact',
    status: 'Status',
    pageSpecSource: 'Pinned PageSpec source',
    revision: 'Latest immutable revision',
    updated: 'Updated',
    action: 'Action',
    draftOnly: 'Draft only',
    openStudio: 'Open Prototype Studio',
    nativeArtifact: 'Platform-native artifact',
    navigationTitle: 'Continue with implemented workflow surfaces',
    navigationBody: 'These controls only navigate to real application surfaces; they do not create or mutate import records.',
    graph: 'Document Graph',
    blueprint: 'Blueprint Editor',
    prototype: 'Prototype Studio',
    workbench: 'Application Workbench',
  },
} as const

export function ImportCenter() {
  const { locale, t } = useI18n()
  const copy = COPY[locale]
  const { setTeamView, setSurface } = useWorksflow()
  const collaboration = useCollaboration()
  const workspace = useArtifactWorkspace()
  const [refreshing, setRefreshing] = useState(false)
  const approvedPrototypeCount = workspace.prototypes.filter(
    (prototype) => Boolean(prototype.approvedRevision),
  ).length

  async function refreshServerData() {
    setRefreshing(true)
    try {
      await Promise.allSettled([collaboration.refresh(), workspace.refresh()])
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <div className="h-full overflow-y-auto bg-canvas scrollbar-thin">
      <div className="mx-auto max-w-6xl px-6 py-6 max-sm:px-4">
        <div className="flex items-start justify-between gap-4 max-sm:flex-wrap">
          <div>
            <h1 className="text-lg font-semibold text-foreground">{t('imports.title')}</h1>
            <p className="mt-1 max-w-3xl text-sm leading-relaxed text-muted-foreground">
              {copy.description}
            </p>
          </div>
          <button
            type="button"
            onClick={() => void refreshServerData()}
            disabled={refreshing || !collaboration.session.signedIn}
            className="inline-flex shrink-0 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 py-2 text-sm font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
          >
            <RefreshCw className={cn('size-4', refreshing && 'animate-spin')} />
            {refreshing ? copy.refreshing : copy.refresh}
          </button>
        </div>

        <section className="mt-6 overflow-hidden rounded-xl border border-warning/30 bg-warning/[0.04]">
          <div className="flex items-start gap-3 border-b border-warning/20 p-4">
            <span className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-warning/10">
              <CircleSlash2 className="size-5 text-warning" />
            </span>
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="text-sm font-semibold text-foreground">{copy.unavailableTitle}</h2>
                <span className="rounded border border-warning/30 bg-warning/10 px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wide text-warning">
                  fail-closed
                </span>
              </div>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{copy.unavailableBody}</p>
              <p className="mt-2 flex items-start gap-1.5 text-[11px] leading-relaxed text-warning">
                <ShieldCheck className="mt-0.5 size-3.5 shrink-0" />
                {copy.unavailableSafety}
              </p>
            </div>
          </div>
          <div className="grid grid-cols-2 gap-px bg-border/50 sm:grid-cols-3 lg:grid-cols-6">
            {CONNECTORS.map((connector) => {
              const Icon = connector.icon
              return (
                <div key={connector.id} className="bg-surface/80 p-3 text-center" aria-disabled="true">
                  <span className="mx-auto flex size-9 items-center justify-center rounded-lg bg-surface-2">
                    <Icon className="size-4 text-faint-foreground" />
                  </span>
                  <div className="mt-2 text-xs font-medium text-muted-foreground">{connector.label}</div>
                  <div className="mt-1 text-[9px] font-medium uppercase tracking-wide text-warning">
                    {copy.notConfigured}
                  </div>
                </div>
              )
            })}
          </div>
          <p className="border-t border-warning/20 px-4 py-3 text-[10px] leading-relaxed text-faint-foreground">
            {copy.connectorRequirement}
          </p>
        </section>

        <section className="mt-6 rounded-xl border border-border bg-surface p-4">
          <div className="flex items-center gap-2">
            <Server className="size-4 text-primary-bright" />
            <h2 className="text-sm font-semibold text-foreground">{copy.serverContext}</h2>
          </div>
          {!collaboration.session.signedIn ? (
            <p className="mt-3 rounded-lg border border-border bg-background p-3 text-xs text-muted-foreground">
              {copy.signedOut}
            </p>
          ) : (
            <div className="mt-3 grid grid-cols-2 gap-2 md:grid-cols-4 lg:grid-cols-7">
              <ContextMetric label={copy.project} value={collaboration.project?.name ?? copy.noProject} wide />
              <ContextMetric label={copy.role} value={collaboration.project?.role ?? '—'} />
              <ContextMetric label={copy.backend} value={collaboration.backendStatus} tone={collaboration.backendStatus === 'online' ? 'success' : 'warning'} />
              <ContextMetric label={copy.documents} value={workspace.documents.length} />
              <ContextMetric label={copy.pageSpecs} value={workspace.pageSpecs.length} />
              <ContextMetric label={copy.prototypes} value={workspace.prototypes.length} />
              <ContextMetric label={copy.approved} value={approvedPrototypeCount} />
            </div>
          )}
          {(collaboration.error || workspace.error) && (
            <div className="mt-3 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-[11px] text-destructive">
              {workspace.error ?? collaboration.error}
            </div>
          )}
        </section>

        <section className="mt-6">
          <div className="flex items-end justify-between gap-3">
            <div>
              <h2 className="text-sm font-semibold text-foreground">{copy.serverPrototypes}</h2>
              <p className="mt-1 text-[11px] text-faint-foreground">{copy.serverPrototypesBody}</p>
            </div>
            <span className="shrink-0 text-xs text-faint-foreground">
              {t('imports.count', { count: workspace.prototypes.length })}
            </span>
          </div>

          {workspace.status === 'loading' && workspace.prototypes.length === 0 ? (
            <div className="mt-3 flex items-center justify-center gap-2 rounded-xl border border-border bg-surface p-8 text-xs text-muted-foreground">
              <RefreshCw className="size-4 animate-spin text-primary-bright" />
              {copy.refreshing}
            </div>
          ) : workspace.prototypes.length === 0 ? (
            <div className="mt-3 rounded-xl border border-dashed border-border bg-surface p-6 text-center">
              <MonitorPlay className="mx-auto size-7 text-primary-bright" />
              <p className="mx-auto mt-3 max-w-xl text-xs leading-relaxed text-muted-foreground">
                {copy.noPrototypes}
              </p>
              <button
                type="button"
                onClick={() => setTeamView('prototype')}
                className="mt-4 inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-2 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
              >
                <MonitorPlay className="size-3.5" /> {copy.openStudio}
              </button>
            </div>
          ) : (
            <div className="mt-3 overflow-x-auto rounded-xl border border-border scrollbar-thin">
              <table className="w-full min-w-[980px] text-left">
                <thead className="bg-surface-2 text-[10px] uppercase tracking-wide text-faint-foreground">
                  <tr>
                    <th className="px-4 py-2.5 font-medium">{copy.artifact}</th>
                    <th className="px-4 py-2.5 font-medium">{copy.status}</th>
                    <th className="px-4 py-2.5 font-medium">{copy.pageSpecSource}</th>
                    <th className="px-4 py-2.5 font-medium">{copy.revision}</th>
                    <th className="px-4 py-2.5 font-medium">{copy.updated}</th>
                    <th className="px-4 py-2.5 font-medium">{copy.action}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {workspace.prototypes.map((prototype) => {
                    const content = prototype.draft?.content ?? prototype.latestRevision?.content
                    const pageSpec = content?.pageSpecRevision
                    return (
                      <tr key={prototype.artifact.id} className="bg-surface hover:bg-white/[0.02]">
                        <td className="px-4 py-3">
                          <div className="flex items-center gap-2">
                            <MonitorPlay className="size-4 shrink-0 text-primary-bright" />
                            <span className="min-w-0">
                              <span className="block truncate text-xs font-medium text-foreground">{prototype.artifact.title}</span>
                              <span className="block truncate text-[9px] text-faint-foreground">{prototype.artifact.id}</span>
                            </span>
                          </div>
                        </td>
                        <td className="px-4 py-3"><ArtifactStatusBadge status={prototype.artifact.status} /></td>
                        <td className="px-4 py-3">
                          {pageSpec ? (
                            <span className="block max-w-[260px] truncate font-mono text-[10px] text-muted-foreground" title={`${pageSpec.artifactId}:${pageSpec.revisionId}`}>
                              {pageSpec.artifactId}:{pageSpec.revisionId}
                            </span>
                          ) : (
                            <span className="text-[10px] text-faint-foreground">—</span>
                          )}
                          <span className="mt-0.5 block text-[9px] text-faint-foreground">
                            {content?.exploratory ? 'Exploratory · ' : ''}{copy.nativeArtifact}
                          </span>
                        </td>
                        <td className="px-4 py-3">
                          {prototype.latestRevision ? (
                            <span className="font-mono text-[10px] text-muted-foreground">
                              r{prototype.latestRevision.revisionNumber} · {shortId(prototype.latestRevision.id)}
                            </span>
                          ) : (
                            <span className="text-[10px] text-warning">{copy.draftOnly}</span>
                          )}
                        </td>
                        <td className="px-4 py-3 text-[10px] text-faint-foreground">
                          {formatDate(prototype.artifact.updatedAt, locale)}
                        </td>
                        <td className="px-4 py-3">
                          <button
                            type="button"
                            onClick={() => setTeamView('prototype')}
                            className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-[10px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
                          >
                            <ExternalLink className="size-3" /> {copy.openStudio}
                          </button>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>

        <section className="mt-6 rounded-xl border border-border bg-surface p-4">
          <h2 className="text-sm font-semibold text-foreground">{copy.navigationTitle}</h2>
          <p className="mt-1 text-[11px] text-faint-foreground">{copy.navigationBody}</p>
          <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
            <NavigationButton icon={GitFork} label={copy.graph} onClick={() => setTeamView('graph')} />
            <NavigationButton icon={Boxes} label={copy.blueprint} onClick={() => setTeamView('blueprint')} />
            <NavigationButton icon={MonitorPlay} label={copy.prototype} onClick={() => setTeamView('prototype')} />
            <NavigationButton icon={PackageCheck} label={copy.workbench} onClick={() => setSurface('workbench')} />
          </div>
        </section>
      </div>
    </div>
  )
}

function ContextMetric({
  label,
  value,
  wide,
  tone = 'default',
}: {
  label: string
  value: string | number
  wide?: boolean
  tone?: 'default' | 'success' | 'warning'
}) {
  return (
    <div className={cn('min-w-0 rounded-lg border border-border bg-background p-2.5', wide && 'col-span-2')}>
      <div className="text-[9px] font-medium uppercase tracking-wide text-faint-foreground">{label}</div>
      <div className={cn(
        'mt-1 truncate text-xs font-semibold text-foreground',
        tone === 'success' && 'text-success',
        tone === 'warning' && 'text-warning',
      )}>
        {value}
      </div>
    </div>
  )
}

function ArtifactStatusBadge({ status }: { status: ArtifactStatus }) {
  return (
    <span className={cn(
      'rounded border px-1.5 py-0.5 text-[9px] font-medium',
      status === 'approved'
        ? 'border-success/30 bg-success/10 text-success'
        : status === 'inReview'
          ? 'border-primary/30 bg-primary/10 text-primary-bright'
          : status === 'changesRequested' || status === 'needsSync'
            ? 'border-warning/30 bg-warning/10 text-warning'
            : 'border-border bg-white/5 text-muted-foreground',
    )}>
      {status.replaceAll(/([A-Z])/g, ' $1').toLowerCase()}
    </span>
  )
}

function NavigationButton({
  icon: Icon,
  label,
  onClick,
}: {
  icon: typeof GitFork
  label: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex items-center gap-2 rounded-lg border border-border bg-background px-3 py-2.5 text-left text-xs font-medium text-muted-foreground hover:border-primary/30 hover:bg-white/5 hover:text-foreground"
    >
      <Icon className="size-4 text-primary-bright" />
      {label}
    </button>
  )
}

function shortId(value: string) {
  return value.length > 14 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value
}

function formatDate(value: string, locale: string) {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString(locale)
}
