'use client'

import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type ReactNode } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n } from '@/lib/i18n'
import type {
  DesignImportCapabilitiesDto,
  DesignImportDto,
  DesignImportSourceKind,
} from '@/lib/platform/design-import-contract'
import { PlatformHttpError } from '@/lib/platform/http'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import {
  AlertTriangle,
  Boxes,
  Check,
  CheckCircle2,
  CircleSlash2,
  Component,
  FileArchive,
  FileUp,
  Frame,
  GitFork,
  LoaderCircle,
  MonitorPlay,
  PenTool,
  RefreshCw,
  Server,
  Shapes,
  ShieldCheck,
  Sparkles,
  X,
} from 'lucide-react'

const SOURCE_ICONS = {
  figma: Frame,
  penpot: PenTool,
  excalidraw: Shapes,
  tldraw: Frame,
  storybook: Component,
  ladle: Component,
  upload: FileUp,
} satisfies Record<DesignImportSourceKind, typeof Frame>

const SOURCE_ORDER: readonly DesignImportSourceKind[] = [
  'figma', 'penpot', 'excalidraw', 'tldraw', 'storybook', 'ladle', 'upload',
]

const COPY = {
  'zh-CN': {
    description: '冻结外部设计导出文件，审阅转换提案，再生成或更新内部页面原型。',
    refresh: '刷新', refreshing: '刷新中', signedOut: '登录后才能读取和创建设计导入。',
    noProject: '请先选择一个服务器项目。', capabilityTitle: '导入能力',
    capabilityBody: '上传已启用；远端连接器未配置时不会请求 URL、OAuth 或凭证。',
    uploadReady: '可上传', uploadUnavailable: '存储容量不足', remoteUnavailable: '远端未配置', source: '来源',
    formTitle: '冻结新快照', formBody: '浏览器只读取所选文件并发送一次命令；成功状态完全以后端响应为准。',
    name: '导入名称', file: '导出文件', pageSpec: '已批准 PageSpec', target: '目标 Prototype',
    createNew: '新建 Prototype', frames: 'Frame / story ID（可选）', framesHint: '逗号分隔；留空表示导入快照中全部可识别项。',
    submit: '冻结并生成提案', submitting: '正在冻结快照', noPageSpec: '没有可用的 Approved + Current PageSpec。请先在蓝图流程中完成页面拆分和评审。',
    maxFile: '最大文件', accepted: '格式', serverContext: '真实服务器上下文', project: '项目',
    role: '角色', backend: '后端', pageSpecs: 'PageSpecs', prototypes: 'Prototypes', imports: '导入记录',
    queueTitle: '快照与审阅提案', queueBody: '每条记录都来自服务器；失败不会生成浏览器本地“已连接”状态。',
    empty: '尚无设计导入。', snapshot: '不可变快照', proposal: '审阅提案', manifest: '输入清单',
    prototype: '目标原型', pageSpecPin: 'PageSpec 固定版本', selected: '选择项', allFrames: '全部识别项',
    approve: '批准并应用', retryApprove: '重试应用', reject: '拒绝', openStudio: '打开原型工作室',
    mapping: '提案映射', layers: '图层', components: '组件', states: '状态', interactions: '交互',
    createMode: '新建', updateMode: '更新', failure: '导入失败', trust: '外部来源不是事实源；只有批准后形成的内部 Prototype revision 才能进入下游。',
    navigationTitle: '继续工作流', graph: '文档依赖图', blueprint: '蓝图编辑器', prototypeStudio: '原型工作室', workbench: '应用工作台',
  },
  'en-US': {
    description: 'Freeze an external design export, review its conversion proposal, then create or update an internal Prototype.',
    refresh: 'Refresh', refreshing: 'Refreshing', signedOut: 'Sign in before reading or creating design imports.',
    noProject: 'Select a server project first.', capabilityTitle: 'Import capabilities',
    capabilityBody: 'Uploads are enabled. When remote connectors are not configured, no URL, OAuth, or credential is requested.',
    uploadReady: 'Upload ready', uploadUnavailable: 'Storage capacity unavailable', remoteUnavailable: 'Remote not configured', source: 'Source',
    formTitle: 'Freeze a new snapshot', formBody: 'The browser reads the selected file for one command only; durable state comes exclusively from the server response.',
    name: 'Import name', file: 'Export file', pageSpec: 'Approved PageSpec', target: 'Target Prototype',
    createNew: 'Create new Prototype', frames: 'Frame / story IDs (optional)', framesHint: 'Comma-separated. Leave blank to import every recognized item in the snapshot.',
    submit: 'Freeze and create proposal', submitting: 'Freezing snapshot', noPageSpec: 'No Approved + Current PageSpec is available. Complete page splitting and review in the Blueprint flow first.',
    maxFile: 'Maximum file', accepted: 'Formats', serverContext: 'Real server context', project: 'Project',
    role: 'Role', backend: 'Backend', pageSpecs: 'PageSpecs', prototypes: 'Prototypes', imports: 'Imports',
    queueTitle: 'Snapshots and review proposals', queueBody: 'Every record comes from the server; failures never create a browser-only “connected” state.',
    empty: 'No design imports yet.', snapshot: 'Immutable snapshot', proposal: 'Review proposal', manifest: 'Input manifest',
    prototype: 'Target Prototype', pageSpecPin: 'Pinned PageSpec', selected: 'Selection', allFrames: 'All recognized items',
    approve: 'Approve and apply', retryApprove: 'Retry apply', reject: 'Reject', openStudio: 'Open Prototype Studio',
    mapping: 'Proposal mapping', layers: 'Layers', components: 'Components', states: 'States', interactions: 'Interactions',
    createMode: 'Create', updateMode: 'Update', failure: 'Import failed', trust: 'External sources are not facts. Only the internal Prototype revision created after approval can flow downstream.',
    navigationTitle: 'Continue the workflow', graph: 'Document Graph', blueprint: 'Blueprint Editor', prototypeStudio: 'Prototype Studio', workbench: 'Application Workbench',
  },
} as const

type LoadState = 'idle' | 'loading' | 'ready' | 'error'

export function ImportCenter() {
  const { locale, t } = useI18n()
  const copy = COPY[locale]
  const collaboration = useCollaboration()
  const workspace = useArtifactWorkspace()
  const { setTeamView, setSurface } = useWorksflow()
  const client = collaboration.platformClient.designImports
  const projectId = collaboration.project?.id
  const [capabilities, setCapabilities] = useState<DesignImportCapabilitiesDto | null>(null)
  const [imports, setImports] = useState<readonly DesignImportDto[]>([])
  const [loadState, setLoadState] = useState<LoadState>('idle')
  const [error, setError] = useState<string | null>(null)
  const [selectedSource, setSelectedSource] = useState<DesignImportSourceKind>('figma')
  const [title, setTitle] = useState('')
  const [pageSpecId, setPageSpecId] = useState('')
  const [targetPrototypeId, setTargetPrototypeId] = useState('')
  const [selectedFrameText, setSelectedFrameText] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [decisionBusy, setDecisionBusy] = useState('')
  const fileInputRef = useRef<HTMLInputElement>(null)

  const pageSpecs = useMemo(() => workspace.pageSpecs.filter((resource) =>
    resource.approvedRevision
      && resource.artifact.syncStatus === 'current'
      && resource.artifact.lifecycle === 'active',
  ), [workspace.pageSpecs])
  const selectedPageSpec = pageSpecs.find((resource) => resource.artifact.id === pageSpecId)
  const selectedPageSpecRevision = selectedPageSpec?.approvedRevision
  const targetPrototypes = useMemo(() => workspace.prototypes.filter((resource) => {
    if (!resource.latestRevision || resource.artifact.lifecycle !== 'active' || !selectedPageSpecRevision) return false
    return (resource.latestRevision.sourceVersions ?? []).some((source) =>
      source.purpose === 'page_spec'
        && source.required
        && source.artifactId === selectedPageSpecRevision.artifactId
        && source.revisionId === selectedPageSpecRevision.id
        && source.contentHash === selectedPageSpecRevision.contentHash,
    )
  }), [selectedPageSpecRevision, workspace.prototypes])
  const selectedCapability = capabilities?.sources.find((item) => item.sourceKind === selectedSource)
  const canEdit = collaboration.can('edit')

  useEffect(() => {
    if (pageSpecId && pageSpecs.some((resource) => resource.artifact.id === pageSpecId)) return
    setPageSpecId(pageSpecs[0]?.artifact.id ?? '')
  }, [pageSpecId, pageSpecs])

  useEffect(() => {
    if (targetPrototypeId && !targetPrototypes.some((resource) => resource.artifact.id === targetPrototypeId)) {
      setTargetPrototypeId('')
    }
  }, [targetPrototypeId, targetPrototypes])

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!projectId || !collaboration.session.signedIn) {
      setCapabilities(null)
      setImports([])
      setLoadState('idle')
      return
    }
    setLoadState('loading')
    setError(null)
    try {
      const [capabilityResult, importResult] = await Promise.all([
        client.capabilities(projectId, { signal }),
        client.list(projectId, {}, { limit: 200, signal }),
      ])
      if (signal?.aborted) return
      setCapabilities(capabilityResult.data)
      setImports(importResult.data.items)
      setLoadState('ready')
    } catch (cause) {
      if (signal?.aborted) return
      setError(describeError(cause))
      setLoadState('error')
    }
  }, [client, collaboration.session.signedIn, projectId])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  async function refresh() {
    await Promise.allSettled([load(), workspace.refresh(), collaboration.refresh()])
  }

  function selectSource(kind: DesignImportSourceKind) {
    setSelectedSource(kind)
    setFile(null)
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const next = event.target.files?.[0] ?? null
    setError(null)
    if (next && selectedCapability && next.size > selectedCapability.maxUploadBytes) {
      setFile(null)
      setError(`${copy.maxFile}: ${formatBytes(selectedCapability.maxUploadBytes)}`)
      event.target.value = ''
      return
    }
    setFile(next)
    if (next && !title.trim()) setTitle(next.name.replace(/\.[^.]+$/, ''))
  }

  async function submitImport() {
    if (!projectId || !file || !pageSpecId || !selectedCapability || !canEdit) return
    const pageSpec = pageSpecs.find((resource) => resource.artifact.id === pageSpecId)
    const revision = pageSpec?.approvedRevision
    if (!revision) return
    setSubmitting(true)
    setError(null)
    try {
      const mediaType = normalizedMediaType(file)
      const contentBase64 = await fileBase64(file)
      const selectedFrameIds = selectedFrameText.split(',').map((item) => item.trim()).filter(Boolean)
      const result = await client.create(projectId, {
        sourceKind: selectedSource,
        mode: 'upload',
        title: title.trim() || undefined,
        file: { name: file.name, mediaType, contentBase64 },
        selectedFrameIds,
        pageSpecRevision: {
          artifactId: revision.artifactId,
          revisionId: revision.id,
          contentHash: revision.contentHash,
        },
        targetPrototypeArtifactId: targetPrototypeId || undefined,
      }, { idempotencyKey: commandKey('design-import-create') })
      setImports((current) => [result.data, ...current.filter((item) => item.id !== result.data.id)])
      setFile(null)
      setTitle('')
      setSelectedFrameText('')
      if (fileInputRef.current) fileInputRef.current.value = ''
      await workspace.refresh()
    } catch (cause) {
      setError(describeError(cause))
    } finally {
      setSubmitting(false)
    }
  }

  async function decide(item: DesignImportDto, decision: 'approve' | 'reject') {
    if (!canEdit) return
    setDecisionBusy(item.id)
    setError(null)
    try {
      const result = await client.decide(item.id, {
        decision,
        version: item.version,
      }, {
        ifMatch: item.etag,
        idempotencyKey: commandKey(`design-import-${decision}`),
      })
      setImports((current) => current.map((entry) => entry.id === item.id ? result.data : entry))
      await Promise.allSettled([workspace.refresh(), collaboration.refresh()])
    } catch (cause) {
      const message = describeError(cause)
      await load()
      setError(message)
    } finally {
      setDecisionBusy('')
    }
  }

  return (
    <div className="h-full overflow-y-auto bg-canvas scrollbar-thin" data-testid="design-import-center">
      <div className="mx-auto max-w-7xl px-6 py-6 max-sm:px-4">
        <header className="flex items-start justify-between gap-4 max-sm:flex-wrap">
          <div>
            <h1 className="text-lg font-semibold text-foreground">{t('imports.title')}</h1>
            <p className="mt-1 max-w-3xl text-sm leading-relaxed text-muted-foreground">{copy.description}</p>
          </div>
          <button
            type="button"
            onClick={() => void refresh()}
            disabled={loadState === 'loading' || !collaboration.session.signedIn}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-surface px-3 py-2 text-sm text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-40"
          >
            <RefreshCw className={cn('size-4', loadState === 'loading' && 'animate-spin')} />
            {loadState === 'loading' ? copy.refreshing : copy.refresh}
          </button>
        </header>

        {!collaboration.session.signedIn || !projectId ? (
          <div className="mt-6 rounded-xl border border-border bg-surface p-5 text-sm text-muted-foreground">
            {collaboration.session.signedIn ? copy.noProject : copy.signedOut}
          </div>
        ) : (
          <>
            <section className="mt-6 rounded-xl border border-border bg-surface p-4">
              <div className="flex items-center gap-2">
                <Server className="size-4 text-primary-bright" />
                <h2 className="text-sm font-semibold text-foreground">{copy.serverContext}</h2>
              </div>
              <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
                <Metric label={copy.project} value={collaboration.project?.name ?? '—'} wide />
                <Metric label={copy.role} value={collaboration.project?.role ?? '—'} />
                <Metric label={copy.backend} value={collaboration.backendStatus} tone={collaboration.backendStatus === 'online' ? 'success' : 'warning'} />
                <Metric label={copy.pageSpecs} value={pageSpecs.length} />
                <Metric label={copy.prototypes} value={workspace.prototypes.length} />
                <Metric label={copy.imports} value={imports.length} />
              </div>
            </section>

            <section className="mt-6 overflow-hidden rounded-xl border border-border bg-surface">
              <div className="flex items-start gap-3 border-b border-border p-4">
                <ShieldCheck className="mt-0.5 size-5 shrink-0 text-success" />
                <div>
                  <h2 className="text-sm font-semibold text-foreground">{copy.capabilityTitle}</h2>
                  <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{capabilities?.snapshotPolicy ?? copy.capabilityBody}</p>
                  <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">{capabilities?.trustPolicy ?? copy.trust}</p>
                </div>
              </div>
              <div className="grid grid-cols-2 gap-px bg-border/50 sm:grid-cols-4 lg:grid-cols-7">
                {SOURCE_ORDER.map((sourceKind) => {
                  const capability = capabilities?.sources.find((item) => item.sourceKind === sourceKind)
                  const Icon = SOURCE_ICONS[sourceKind]
                  const selected = sourceKind === selectedSource
                  return (
                    <button
                      key={sourceKind}
                      type="button"
                      data-testid={`design-source-${sourceKind}`}
                      onClick={() => selectSource(sourceKind)}
                      aria-pressed={selected}
                      className={cn('bg-surface p-3 text-left transition-colors hover:bg-surface-2', selected && 'bg-primary/10 ring-1 ring-inset ring-primary/50')}
                    >
                      <span className="flex size-9 items-center justify-center rounded-lg bg-surface-2"><Icon className="size-4 text-primary-bright" /></span>
                      <div className="mt-2 text-xs font-semibold text-foreground">{capability?.label ?? sourceKind}</div>
                      <div className={cn('mt-1 flex items-center gap-1 text-[9px] font-medium', capability?.uploadEnabled === false ? 'text-destructive' : 'text-success')}>
                        {capability?.uploadEnabled === false ? <X className="size-2.5" /> : <Check className="size-2.5" />}
                        {capability?.uploadEnabled === false ? copy.uploadUnavailable : copy.uploadReady}
                      </div>
                      <div className="mt-0.5 flex items-center gap-1 text-[9px] text-warning"><CircleSlash2 className="size-2.5" />{copy.remoteUnavailable}</div>
                    </button>
                  )
                })}
              </div>
            </section>

            <div className="mt-6 grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
              <section className="h-fit rounded-xl border border-border bg-surface p-4">
                <div className="flex items-start gap-2">
                  <FileArchive className="mt-0.5 size-4 text-primary-bright" />
                  <div>
                    <h2 className="text-sm font-semibold text-foreground">{copy.formTitle}</h2>
                    <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">{copy.formBody}</p>
                  </div>
                </div>
                <div className="mt-4 space-y-3">
                  <Field label={copy.source}>
                    <div className="rounded-lg border border-border bg-background px-3 py-2 text-xs font-medium text-foreground">{selectedCapability?.label ?? selectedSource}</div>
                  </Field>
                  <Field label={copy.name}>
                    <input value={title} onChange={(event) => setTitle(event.target.value)} maxLength={240} className="w-full rounded-lg border border-border bg-background px-3 py-2 text-xs text-foreground outline-none focus:border-primary focus:ring-2 focus:ring-primary/20" placeholder={file?.name ?? ''} />
                  </Field>
                  <Field label={copy.file}>
                    <input
                      ref={fileInputRef}
                      data-testid="design-import-file"
                      type="file"
                      onChange={chooseFile}
                      accept={selectedCapability?.acceptedFileExtensions.join(',')}
                      className="block w-full rounded-lg border border-border bg-background px-2 py-2 text-[11px] text-muted-foreground file:mr-2 file:rounded file:border-0 file:bg-primary/15 file:px-2 file:py-1 file:text-[10px] file:font-semibold file:text-primary-bright"
                    />
                    {selectedCapability && <p className="mt-1 text-[9px] leading-relaxed text-faint-foreground">{selectedCapability.uploadEnabled ? `${copy.maxFile}: ${formatBytes(selectedCapability.maxUploadBytes)} · ${copy.accepted}: ${selectedCapability.acceptedFileExtensions.join(', ')}` : selectedCapability.uploadReason}</p>}
                  </Field>
                  <Field label={copy.pageSpec}>
                    <select data-testid="design-import-page-spec" value={pageSpecId} onChange={(event) => setPageSpecId(event.target.value)} className="w-full rounded-lg border border-border bg-background px-3 py-2 text-xs text-foreground outline-none focus:border-primary focus:ring-2 focus:ring-primary/20">
                      {pageSpecs.map((resource) => <option key={resource.artifact.id} value={resource.artifact.id}>{resource.artifact.title} · r{resource.approvedRevision?.revisionNumber}</option>)}
                    </select>
                    {pageSpecs.length === 0 && <p className="mt-1 text-[10px] leading-relaxed text-warning">{copy.noPageSpec}</p>}
                  </Field>
                  <Field label={copy.target}>
                    <select value={targetPrototypeId} onChange={(event) => setTargetPrototypeId(event.target.value)} className="w-full rounded-lg border border-border bg-background px-3 py-2 text-xs text-foreground outline-none focus:border-primary focus:ring-2 focus:ring-primary/20">
                      <option value="">{copy.createNew}</option>
                      {targetPrototypes.map((resource) => <option key={resource.artifact.id} value={resource.artifact.id}>{resource.artifact.title} · r{resource.latestRevision?.revisionNumber}</option>)}
                    </select>
                  </Field>
                  <Field label={copy.frames}>
                    <input value={selectedFrameText} onChange={(event) => setSelectedFrameText(event.target.value)} className="w-full rounded-lg border border-border bg-background px-3 py-2 text-xs text-foreground outline-none focus:border-primary focus:ring-2 focus:ring-primary/20" placeholder="frame-home, story-button-primary" />
                    <p className="mt-1 text-[9px] leading-relaxed text-faint-foreground">{copy.framesHint}</p>
                  </Field>
                  <button
                    type="button"
                    data-testid="design-import-submit"
                    onClick={() => void submitImport()}
                    disabled={!file || !pageSpecId || !canEdit || submitting || !selectedCapability?.uploadEnabled}
                    className="flex w-full items-center justify-center gap-1.5 rounded-lg bg-primary px-3 py-2.5 text-xs font-semibold text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-40"
                  >
                    {submitting ? <LoaderCircle className="size-4 animate-spin" /> : <Sparkles className="size-4" />}
                    {submitting ? copy.submitting : copy.submit}
                  </button>
                </div>
              </section>

              <section className="min-w-0 rounded-xl border border-border bg-surface" data-testid="design-import-list">
                <div className="border-b border-border p-4">
                  <h2 className="text-sm font-semibold text-foreground">{copy.queueTitle}</h2>
                  <p className="mt-1 text-[11px] text-muted-foreground">{copy.queueBody}</p>
                </div>
                {imports.length === 0 ? (
                  <div className="p-8 text-center text-xs text-muted-foreground">{copy.empty}</div>
                ) : (
                  <div className="divide-y divide-border">
                    {imports.map((item) => (
                      <ImportRecord
                        key={item.id}
                        item={item}
                        locale={locale}
                        busy={decisionBusy === item.id}
                        canEdit={canEdit}
                        copy={copy}
                        onApprove={() => void decide(item, 'approve')}
                        onReject={() => void decide(item, 'reject')}
                        onOpenPrototype={() => { setSurface('team'); setTeamView('prototype') }}
                      />
                    ))}
                  </div>
                )}
              </section>
            </div>
          </>
        )}

        {error && (
          <div role="alert" className="mt-4 flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            <AlertTriangle className="mt-0.5 size-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        <section className="mt-6 rounded-xl border border-border bg-surface p-4">
          <h2 className="text-sm font-semibold text-foreground">{copy.navigationTitle}</h2>
          <div className="mt-3 grid grid-cols-2 gap-2 md:grid-cols-4">
            <NavigationButton icon={GitFork} label={copy.graph} onClick={() => { setSurface('team'); setTeamView('graph') }} />
            <NavigationButton icon={Boxes} label={copy.blueprint} onClick={() => { setSurface('team'); setTeamView('blueprint') }} />
            <NavigationButton icon={MonitorPlay} label={copy.prototypeStudio} onClick={() => { setSurface('team'); setTeamView('prototype') }} />
            <NavigationButton icon={Sparkles} label={copy.workbench} onClick={() => setSurface('workbench')} />
          </div>
        </section>
      </div>
    </div>
  )
}

function ImportRecord({ item, locale, busy, canEdit, copy, onApprove, onReject, onOpenPrototype }: {
  item: DesignImportDto
  locale: 'zh-CN' | 'en-US'
  busy: boolean
  canEdit: boolean
  copy: typeof COPY['zh-CN'] | typeof COPY['en-US']
  onApprove: () => void
  onReject: () => void
  onOpenPrototype: () => void
}) {
  const sourceIcon = SOURCE_ICONS[item.snapshot.sourceKind]
  const Icon = sourceIcon
  const reviewable = item.status === 'open' || (item.status === 'failed' && Boolean(item.proposal))
  const statusTone = item.status === 'applied' ? 'success' : item.status === 'failed' || item.status === 'rejected' ? 'danger' : item.status === 'applying' ? 'warning' : 'primary'
  const mapping = proposalMapping(item)
  return (
    <article className="p-4" data-testid={`design-import-record-${item.id}`}>
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-surface-2"><Icon className="size-4 text-primary-bright" /></span>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="truncate text-sm font-semibold text-foreground">{item.snapshot.sourceName}</h3>
              <StatusBadge status={item.status} tone={statusTone} />
              <span className="rounded border border-border px-1.5 py-0.5 text-[9px] text-muted-foreground">{item.createsPrototype ? copy.createMode : copy.updateMode}</span>
            </div>
            <p className="mt-1 text-[10px] text-faint-foreground">{item.snapshot.fileName} · {formatBytes(item.snapshot.byteSize)} · {new Date(item.snapshot.capturedAt).toLocaleString(locale)}</p>
          </div>
        </div>
        {busy && <LoaderCircle className="size-4 animate-spin text-primary-bright" />}
      </div>

      <div className="mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
        <Info label={copy.snapshot} value={shortHash(item.snapshot.contentHash)} mono />
        <Info label={copy.pageSpecPin} value={shortId(item.pageSpecRevision.revisionId)} mono />
        <Info label={copy.manifest} value={item.manifest ? shortHash(item.manifest.hash) : '—'} mono />
        <Info label={copy.proposal} value={item.proposal ? `${item.proposal.operations.length} op · ${item.proposal.status}` : '—'} />
        <Info label={copy.prototype} value={item.prototypeArtifactId ? shortId(item.prototypeArtifactId) : '—'} mono />
        <Info label={copy.selected} value={item.snapshot.selectedFrameIds.length ? item.snapshot.selectedFrameIds.join(', ') : copy.allFrames} />
        <Info label="Raw SHA-256" value={shortHash(item.snapshot.rawContentHash)} mono />
        <Info label="Revision" value={item.appliedRevisionId ? shortId(item.appliedRevisionId) : item.baseRevisionId ? `base ${shortId(item.baseRevisionId)}` : '—'} mono />
      </div>

      {item.failureDetail && (
        <div className="mt-3 flex items-start gap-1.5 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">
          <AlertTriangle className="mt-0.5 size-3 shrink-0" />
          <span><strong>{copy.failure}:</strong> {item.failureDetail}</span>
        </div>
      )}
      {mapping && (
        <div className="mt-3 rounded-lg border border-primary/20 bg-primary/[0.04] p-3">
          <div className="flex flex-wrap items-center gap-2 text-[9px] font-semibold uppercase tracking-wide text-primary-bright">
            <span>{copy.mapping}</span>
            <span>{copy.layers} {mapping.layers}</span>
            <span>{copy.components} {mapping.components}</span>
            <span>{copy.states} {mapping.states}</span>
            <span>{copy.interactions} {mapping.interactions}</span>
          </div>
          {mapping.names.length > 0 && <p className="mt-1.5 line-clamp-2 text-[10px] leading-relaxed text-muted-foreground">{mapping.names.join(' · ')}</p>}
          {item.proposal?.operations[0]?.rationale && <p className="mt-1 text-[9px] leading-relaxed text-faint-foreground">{item.proposal.operations[0].rationale}</p>}
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3">
        <p className="flex max-w-2xl items-start gap-1.5 text-[10px] leading-relaxed text-faint-foreground"><ShieldCheck className="mt-0.5 size-3 shrink-0 text-success" />{copy.trust}</p>
        <div className="flex gap-2">
          {item.status === 'applied' && (
            <button type="button" onClick={onOpenPrototype} className="inline-flex items-center gap-1 rounded border border-border px-2 py-1.5 text-[10px] text-muted-foreground hover:bg-white/5 hover:text-foreground"><MonitorPlay className="size-3" />{copy.openStudio}</button>
          )}
          {reviewable && (
            <>
              {item.status === 'open' && <button type="button" onClick={onReject} disabled={!canEdit || busy} className="inline-flex items-center gap-1 rounded border border-destructive/40 px-2 py-1.5 text-[10px] text-destructive hover:bg-destructive/10 disabled:opacity-40"><X className="size-3" />{copy.reject}</button>}
              <button type="button" data-testid={`design-import-approve-${item.id}`} onClick={onApprove} disabled={!canEdit || busy} className="inline-flex items-center gap-1 rounded bg-primary px-2.5 py-1.5 text-[10px] font-semibold text-primary-foreground hover:bg-primary/90 disabled:opacity-40"><CheckCircle2 className="size-3" />{item.status === 'failed' ? copy.retryApprove : copy.approve}</button>
            </>
          )}
        </div>
      </div>
    </article>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block"><span className="mb-1 block text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">{label}</span>{children}</label>
}

function Metric({ label, value, tone = 'default', wide = false }: { label: string; value: string | number; tone?: 'default' | 'success' | 'warning'; wide?: boolean }) {
  return <div className={cn('rounded-lg border border-border bg-background p-2', wide && 'col-span-2')}><div className="text-[9px] uppercase tracking-wide text-faint-foreground">{label}</div><div className={cn('mt-1 truncate text-xs font-semibold', tone === 'success' ? 'text-success' : tone === 'warning' ? 'text-warning' : 'text-foreground')}>{value}</div></div>
}

function Info({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="min-w-0 rounded-lg border border-border/70 bg-background px-2.5 py-2"><div className="text-[8px] uppercase tracking-wide text-faint-foreground">{label}</div><div className={cn('mt-1 truncate text-[10px] text-muted-foreground', mono && 'font-mono')}>{value}</div></div>
}

function StatusBadge({ status, tone }: { status: string; tone: 'success' | 'danger' | 'warning' | 'primary' }) {
  const Icon = tone === 'success' ? CheckCircle2 : tone === 'danger' ? AlertTriangle : tone === 'warning' ? LoaderCircle : Sparkles
  return <span className={cn('inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wide', tone === 'success' && 'border-success/30 bg-success/10 text-success', tone === 'danger' && 'border-destructive/30 bg-destructive/10 text-destructive', tone === 'warning' && 'border-warning/30 bg-warning/10 text-warning', tone === 'primary' && 'border-primary/30 bg-primary/10 text-primary-bright')}><Icon className={cn('size-2.5', status === 'applying' && 'animate-spin')} />{status}</span>
}

function NavigationButton({ icon: Icon, label, onClick }: { icon: typeof Boxes; label: string; onClick: () => void }) {
  return <button type="button" onClick={onClick} className="flex items-center gap-2 rounded-lg border border-border bg-background px-3 py-2 text-left text-xs text-muted-foreground hover:bg-white/5 hover:text-foreground"><Icon className="size-4 text-primary-bright" />{label}</button>
}

function describeError(cause: unknown) {
  if (cause instanceof PlatformHttpError) {
    const fields = cause.problem.errors ? Object.values(cause.problem.errors).flat().join(' ') : ''
    return [cause.problem.detail ?? cause.message, fields].filter(Boolean).join(' ')
  }
  return cause instanceof Error ? cause.message : 'Design import request failed.'
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`
}

function shortHash(value: string) {
  return value.length > 24 ? `${value.slice(0, 15)}…${value.slice(-6)}` : value
}

function shortId(value: string) {
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value
}

function normalizedMediaType(file: File) {
  if (file.type && file.type !== 'application/octet-stream') return file.type
  const suffix = file.name.toLowerCase().split('.').pop()
  if (suffix === 'svg') return 'image/svg+xml'
  if (suffix === 'png') return 'image/png'
  if (suffix === 'jpg' || suffix === 'jpeg') return 'image/jpeg'
  if (suffix === 'webp') return 'image/webp'
  if (suffix === 'pdf') return 'application/pdf'
  return 'application/json'
}

async function fileBase64(file: File) {
  const url = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error ?? new Error('Unable to read the selected file.'))
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.readAsDataURL(file)
  })
  const comma = url.indexOf(',')
  if (comma < 0) throw new Error('Unable to encode the selected file.')
  return url.slice(comma + 1)
}

function commandKey(prefix: string) {
  const suffix = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${suffix}`
}

function proposalMapping(item: DesignImportDto) {
  const value = item.proposal?.operations.find((operation) => operation.id === item.operationId)?.value
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const content = value as Record<string, unknown>
  const layers = content.layers && typeof content.layers === 'object' && !Array.isArray(content.layers)
    ? Object.values(content.layers as Record<string, unknown>)
    : []
  const names = layers.flatMap((layer) => {
    if (!layer || typeof layer !== 'object' || Array.isArray(layer)) return []
    const name = (layer as Record<string, unknown>).name
    return typeof name === 'string' && name !== 'Imported design snapshot' ? [name] : []
  })
  const components = Array.isArray(content.componentBindings) ? content.componentBindings.length : 0
  const states = Array.isArray(content.states) ? content.states.length : 0
  const interactions = Array.isArray(content.interactions) ? content.interactions.length : 0
  return { layers: layers.length, components, states, interactions, names }
}
