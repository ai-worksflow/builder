'use client'

import { useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import type { WorkbenchView } from '@/lib/worksflow/types'
import { useDropdown } from '../use-dropdown'
import { LanguageToggle } from '../language-toggle'
import { GitHubPanel } from './github-panel'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import {
  BarChart3,
  Blocks,
  ChevronDown,
  Code2,
  Database,
  Download,
  FileClock,
  GitBranch,
  Home,
  Link2,
  Monitor,
  MoreHorizontal,
  Pencil,
  Plug,
  Rocket,
  Share2,
  Trash2,
  Users2,
  X,
} from 'lucide-react'

const VIEWS: { id: WorkbenchView; labelKey: MessageKey; icon: typeof Monitor }[] = [
  { id: 'preview', labelKey: 'workbench.view.preview', icon: Monitor },
  { id: 'code', labelKey: 'workbench.view.code', icon: Code2 },
  { id: 'database', labelKey: 'workbench.view.database', icon: Database },
]

export function TopBar() {
  const {
    session: collaborationSession,
    project: collaborationProject,
    can: canCollaborate,
    authorize: authorizeCollaboration,
    error: collaborationError,
    renameProject,
    archiveProject,
  } = useCollaboration()
  const {
    view,
    setView,
    setSurface,
    setComposerDraft,
    setTeamView,
    deliveryStatus,
    deliveryError,
    deliveryLogs,
    deployments,
    publishedUrl,
    exportWorkspace,
    publishCurrentWorkspace,
    refreshDeployments,
    rollbackDeployment,
  } = useWorksflow()
  const artifactWorkspace = useArtifactWorkspace()
  const projectName = collaborationProject?.name ?? 'Select a server project'
  const { t } = useI18n()
  const projectMenu = useDropdown()
  const moreMenu = useDropdown()
  const [notice, setNotice] = useState<string | null>(null)
  const [panel, setPanel] = useState<
    | null
    | 'versions'
    | 'rename'
    | 'export'
    | 'delete'
    | 'connect'
    | 'publish'
    | 'analytics'
    | 'knowledge'
    | 'connectors'
    | 'stripe'
  >(null)
  const [renameDraft, setRenameDraft] = useState(projectName)
  const [publishMessage, setPublishMessage] = useState('Publish from Worksflow')
  const [publishEnvironment, setPublishEnvironment] = useState<'preview' | 'production'>('preview')
  const [rollbackCandidate, setRollbackCandidate] = useState<{
    deploymentId: string
    versionId: string
  } | null>(null)

  function showNotice(message: string) {
    setNotice(message)
    window.setTimeout(() => setNotice(null), 2400)
  }

  async function handleExport(key: string) {
    if (!(await authorizeCollaboration('view'))) return
    if (key !== 'sourceZip') return
    const ok = await exportWorkspace()
    if (!ok) return
    const option = EXPORT_OPTIONS.find((item) => item.key === key)
    if (option) showNotice(t('workbench.notice.exportPrepared', { item: t(option.labelKey) }))
  }

  return (
    <header className="relative flex min-h-[50px] shrink-0 items-center gap-2 border-b border-border bg-panel px-3 py-2 max-md:flex-wrap">
      <IconButton label={t('workbench.home')} onClick={() => setSurface('recent')}>
        <Home className="h-4 w-4" />
      </IconButton>

      {/* Workspace / project path */}
      <div className="flex min-w-0 flex-1 items-center gap-1.5 text-sm md:flex-none">
        <span className="shrink-0 rounded-md bg-white/5 px-2 py-1 text-xs font-medium text-muted-foreground">
          Projects
        </span>
        <span className="shrink-0 text-faint-foreground">/</span>

        {/* Project title menu (Frame 07) */}
        <div className="relative min-w-0" ref={projectMenu.ref}>
          <button
            type="button"
            onClick={() => projectMenu.setOpen((v) => !v)}
            className="flex min-w-0 items-center gap-1 rounded-md px-2 py-1 font-medium hover:bg-white/5"
            aria-haspopup="menu"
            aria-expanded={projectMenu.open}
          >
            <span className="truncate">{projectName}</span>
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          </button>
          {projectMenu.open && (
            <Menu>
              <MenuItem icon={FileClock} onClick={() => setSurface('recent')}>
                {t('workbench.menu.recentProjects')}
              </MenuItem>
              <MenuItem icon={FileClock} onClick={() => setPanel('versions')}>
                {t('workbench.menu.versionHistory')}
              </MenuItem>
              <MenuDivider />
              <MenuItem
                icon={Pencil}
                onClick={() => {
                  void authorizeCollaboration('admin').then((allowed) => {
                    if (!allowed) return
                    setRenameDraft(projectName)
                    setPanel('rename')
                  })
                }}
              >
                {t('workbench.menu.rename')}
              </MenuItem>
              <MenuItem icon={Download} onClick={() => setPanel('export')}>
                {t('workbench.menu.export')}
              </MenuItem>
              <MenuDivider />
              <MenuItem icon={Trash2} danger onClick={() => {
                void authorizeCollaboration('admin').then((allowed) => {
                  if (allowed) setPanel('delete')
                })
              }}>
                {t('workbench.menu.delete')}
              </MenuItem>
            </Menu>
          )}
        </div>
      </div>

      {/* View switcher */}
      <div className="mx-auto flex max-w-full items-center gap-1 overflow-x-auto rounded-lg bg-white/5 p-0.5 scrollbar-thin max-md:order-3 max-md:mx-0 max-md:w-full">
        {VIEWS.map((v) => {
          const Icon = v.icon
          const active = view === v.id
          return (
            <button
              key={v.id}
              type="button"
              onClick={() => setView(v.id)}
              className={cn(
                'flex shrink-0 items-center gap-1.5 rounded-md px-3 py-1.5 text-[13px] font-medium transition-colors',
                active
                  ? 'bg-secondary text-foreground shadow-sm'
                  : 'text-muted-foreground hover:text-foreground',
              )}
              aria-pressed={active}
            >
              <Icon className="h-4 w-4" />
              {t(v.labelKey)}
            </button>
          )
        })}

        {/* More menu (Frame 08) */}
        <div className="relative" ref={moreMenu.ref}>
          <button
            type="button"
            onClick={() => moreMenu.setOpen((v) => !v)}
            className="flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:text-foreground"
            aria-label={t('workbench.more')}
            aria-haspopup="menu"
            aria-expanded={moreMenu.open}
          >
            <MoreHorizontal className="h-4 w-4" />
          </button>
          {moreMenu.open && (
            <Menu align="right">
              <MenuItem icon={BarChart3} onClick={() => setPanel('analytics')}>{t('workbench.menu.analytics')}</MenuItem>
              <MenuItem icon={Blocks} onClick={() => setPanel('knowledge')}>{t('workbench.menu.knowledge')}</MenuItem>
              <MenuItem icon={Plug} onClick={() => setPanel('connectors')}>{t('workbench.menu.connectors')}</MenuItem>
              <MenuItem icon={Users2} onClick={() => setSurface('settings')}>{t('workbench.menu.allSettings')}</MenuItem>
              <MenuDivider />
              <div className="px-3 pb-1 pt-1.5 text-[11px] font-semibold uppercase tracking-wider text-faint-foreground">
                {t('workbench.menu.integrations')}
              </div>
              <MenuItem icon={Plug} onClick={() => setPanel('stripe')}>{t('workbench.menu.stripe')}</MenuItem>
              <MenuItem icon={Database} onClick={() => setView('database')}>Worksflow Database</MenuItem>
            </Menu>
          )}
        </div>
        <LanguageToggle className="sm:hidden" />
      </div>

      {/* Right actions */}
      <div className="flex shrink-0 items-center gap-1.5">
        <LanguageToggle className="max-sm:hidden" />
        <button
          type="button"
          onClick={() => {
            setTeamView('graph')
            setSurface('team')
          }}
          className="hidden items-center gap-1 rounded-md border border-primary/30 bg-primary/10 px-2 py-1 text-[11px] font-medium text-primary-bright hover:bg-primary/15 md:inline-flex"
        >
          <Link2 className="h-3 w-3" />
          Server artifacts · {artifactWorkspace.documents.length + artifactWorkspace.blueprints.length + artifactWorkspace.pageSpecs.length + artifactWorkspace.prototypes.length}
        </button>
        <button
          type="button"
          onClick={() => setPanel('connect')}
          className="flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-[13px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
        >
          <GitBranch className="h-4 w-4" />
          <span className="hidden lg:inline">{t('workbench.connect')}</span>
        </button>
        <button
          type="button"
          onClick={() => {
            setTeamView('members')
            setSurface('team')
          }}
          className="flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-[13px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
        >
          <Share2 className="h-4 w-4" />
          <span className="hidden lg:inline">{t('workbench.share')}</span>
        </button>
        <button
          type="button"
          onClick={() => {
            setRollbackCandidate(null)
            setPanel('publish')
            void refreshDeployments()
          }}
          disabled={deliveryStatus === 'publishing' || deliveryStatus === 'rollingBack'}
          className="flex h-8 items-center gap-1.5 rounded-md bg-primary px-3 text-[13px] font-semibold text-primary-foreground hover:bg-primary-bright max-sm:w-8 max-sm:px-0 max-sm:justify-center"
        >
          <Rocket className="h-4 w-4 shrink-0" />
          <span className="max-sm:hidden">
            {deliveryStatus === 'publishing'
              ? t('workbench.publishing')
              : publishedUrl
                ? t('workbench.published')
                : t('workbench.publish')}
          </span>
        </button>
      </div>
      {notice && (
        <div className="absolute right-3 top-[calc(100%+4px)] z-50 rounded-lg border border-border bg-popover px-3 py-2 text-xs text-muted-foreground shadow-2xl">
          {notice}
        </div>
      )}
      {panel && (
        <TopBarPanel
          title={panelTitle(panel, t)}
          wide={panel === 'connect'}
          onClose={() => setPanel(null)}
          footer={
            panel === 'rename' ? (
              <button
                type="button"
                disabled={!collaborationProject || !renameDraft.trim() || renameDraft.trim() === collaborationProject.name}
                onClick={async () => {
                  if (!collaborationProject || !(await authorizeCollaboration('admin'))) return
                  if (await renameProject(collaborationProject.id, renameDraft.trim())) setPanel(null)
                }}
                className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:opacity-50"
              >
                {t('workbench.panel.saveName')}
              </button>
            ) : panel === 'delete' ? (
              <button
                type="button"
                disabled={!collaborationProject}
                onClick={async () => {
                  if (!collaborationProject || !(await authorizeCollaboration('admin'))) return
                  if (await archiveProject(collaborationProject.id)) setPanel(null)
                }}
                className="rounded-md bg-destructive px-3 py-1.5 text-[12px] font-semibold text-destructive-foreground hover:opacity-90"
              >
                {t('workbench.panel.confirmDelete')}
              </button>
            ) : panel === 'publish' ? (
              !collaborationSession.signedIn ? (
                <button
                  type="button"
                  onClick={() => {
                    setPanel(null)
                    setSurface('settings')
                  }}
                  className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground"
                >
                  {t('workbench.publish.signIn')}
                </button>
              ) : rollbackCandidate ? (
                <button
                  type="button"
                  disabled={deliveryStatus === 'rollingBack'}
                  onClick={async () => {
                    if (!(await authorizeCollaboration('publish'))) return
                    const completed = await rollbackDeployment(
                      rollbackCandidate.deploymentId,
                      rollbackCandidate.versionId,
                    )
                    if (completed) setRollbackCandidate(null)
                  }}
                  className="rounded-md bg-destructive px-3 py-1.5 text-[12px] font-semibold text-destructive-foreground hover:opacity-90 disabled:opacity-60"
                >
                  {deliveryStatus === 'rollingBack'
                    ? t('workbench.publish.rollingBack')
                    : t('workbench.publish.confirmRollback')}
                </button>
              ) : (
                <button
                  type="button"
                  onClick={async () => {
                    if (await authorizeCollaboration('publish')) {
                      await publishCurrentWorkspace(publishMessage, publishEnvironment)
                    }
                  }}
                  disabled={deliveryStatus === 'publishing' || !canCollaborate('publish')}
                  className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:opacity-60"
                >
                  {deliveryStatus === 'publishing'
                    ? t('workbench.publishing')
                    : t('workbench.publish.confirmPublish')}
                </button>
              )
            ) : (
              <button
                type="button"
                onClick={() => setPanel(null)}
                className="rounded-md border border-border px-3 py-1.5 text-[12px] font-medium text-muted-foreground hover:bg-white/5"
              >
                {t('common.done')}
              </button>
            )
          }
        >
          {panel === 'versions' && (
            <div className="space-y-2">
              {deployments.flatMap((deployment) => deployment.versions.map((version) => (
                <div key={`${deployment.deploymentId}:${version.id}`} className="rounded-md border border-border bg-card px-3 py-2">
                  <div className="text-sm font-medium text-foreground">v{version.number} · {version.action}</div>
                  <div className="mt-0.5 text-[11px] text-faint-foreground">{new Date(version.createdAt).toLocaleString()} · {version.environment ?? 'preview'} · {version.checksum.slice(0, 12)}</div>
                </div>
              )))}
              {deployments.every((deployment) => deployment.versions.length === 0) && (
                <p className="rounded-md border border-dashed border-border px-3 py-4 text-[11px] text-faint-foreground">No server release versions yet.</p>
              )}
            </div>
          )}
          {panel === 'rename' && (
            <input
              value={renameDraft}
              onChange={(event) => setRenameDraft(event.target.value)}
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground outline-none focus:border-primary/60 focus:ring-1 focus:ring-primary/40"
            />
          )}
          {panel === 'export' && (
            <div className="space-y-3">
              <div className="grid grid-cols-2 gap-2">
              {EXPORT_OPTIONS.map((item) => (
                <button
                  key={item.key}
                  type="button"
                  onClick={() => void handleExport(item.key)}
                  disabled={deliveryStatus === 'exporting'}
                  className="rounded-md border border-border bg-card px-3 py-2 text-left text-[12px] text-muted-foreground hover:border-primary/40 hover:text-foreground"
                >
                  {t(item.labelKey)}
                </button>
              ))}
              </div>
              {deliveryError && <p role="alert" className="text-[11px] text-destructive">{deliveryError}</p>}
            </div>
          )}
          {panel === 'delete' && (
            <p className="text-[12px] leading-relaxed text-muted-foreground">
              {t('workbench.panel.deleteCopy')}
            </p>
          )}
          {panel === 'connect' && (
            collaborationSession.signedIn ? (
              <GitHubPanel />
            ) : (
              <div className="rounded-md border border-warning/30 bg-warning/10 px-3 py-3 text-[11px] leading-relaxed text-warning">
                {t('workbench.github.signInRequired')}
              </div>
            )
          )}
          {panel === 'publish' && (
            <div className="space-y-3">
              <div className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-[11px] leading-relaxed text-warning">
                {t('workbench.publish.confirmationCopy')}
              </div>
              {collaborationSession.signedIn && !canCollaborate('publish') && (
                <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[11px] text-destructive">
                  {t('workbench.publish.permissionDenied')}
                </div>
              )}
              <label className="block text-[11px] text-muted-foreground">
                {t('workbench.publish.message')}
                <input
                  value={publishMessage}
                  onChange={(event) => setPublishMessage(event.target.value)}
                  maxLength={500}
                  className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2.5 text-[12px] text-foreground outline-none focus:border-primary/60"
                />
              </label>
              <label className="block text-[11px] text-muted-foreground">
                Environment
                <select
                  value={publishEnvironment}
                  onChange={(event) => setPublishEnvironment(event.target.value as 'preview' | 'production')}
                  className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2.5 text-[12px] text-foreground outline-none focus:border-primary/60"
                >
                  <option value="preview">Preview</option>
                  <option value="production">Production</option>
                </select>
                <span className="mt-1 block text-[10px] text-faint-foreground">
                  Plain variables in this scope are embedded as window.__WORKSFLOW_ENV__. Secrets remain server-only.
                </span>
              </label>
              {publishedUrl && (
                <a
                  href={publishedUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="block truncate rounded-md border border-success/30 bg-success/10 px-3 py-2 font-mono text-[11px] text-success hover:underline"
                >
                  {publishedUrl}
                </a>
              )}
              {rollbackCandidate && (
                <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[11px] text-destructive">
                  {t('workbench.publish.rollbackCopy', { version: rollbackCandidate.versionId })}
                </div>
              )}
              {deployments.flatMap((deployment) =>
                deployment.versions
                  .slice()
                  .reverse()
                  .map((version) => (
                    <div key={version.id} className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2">
                      <span className="min-w-0 flex-1">
                        <span className="block text-[11px] font-medium text-foreground">
                          v{version.number} · {version.action}
                        </span>
                        <span className="block truncate text-[10px] text-faint-foreground">
                          {new Date(version.createdAt).toLocaleString()} · {version.environment ?? 'preview'} · {version.environmentVariableNames?.length ?? 0} public vars · {version.checksum.slice(0, 12)}
                        </span>
                      </span>
                      {version.id !== deployment.activeVersionId && (
                        <button
                          type="button"
                          onClick={() => setRollbackCandidate({ deploymentId: deployment.deploymentId, versionId: version.id })}
                          className="rounded-md border border-border px-2 py-1 text-[10px] text-muted-foreground hover:bg-white/5 hover:text-foreground"
                        >
                          {t('workbench.publish.rollback')}
                        </button>
                      )}
                    </div>
                  )),
              )}
              {deliveryLogs.length > 0 && (
                <div className="max-h-24 overflow-y-auto rounded-md border border-border bg-background px-3 py-2 font-mono text-[10px] leading-relaxed text-faint-foreground scrollbar-thin">
                  {deliveryLogs.slice(-6).map((line, index) => <div key={`${index}-${line}`}>{line}</div>)}
                </div>
              )}
              {deliveryError && <p role="alert" className="text-[11px] text-destructive">{deliveryError}</p>}
              {collaborationError && <p role="alert" className="text-[11px] text-destructive">{collaborationError}</p>}
            </div>
          )}
          {panel === 'analytics' && (
            <div className="grid grid-cols-3 gap-2">
              {[
                ['Deployments', String(deployments.length)],
                ['Release log entries', String(deliveryLogs.length)],
                ['Server artifacts', String(artifactWorkspace.documents.length + artifactWorkspace.blueprints.length + artifactWorkspace.pageSpecs.length + artifactWorkspace.prototypes.length)],
              ].map(([label, value]) => (
                <div key={label} className="rounded-md border border-border bg-card p-3">
                  <div className="text-lg font-semibold text-foreground">{value}</div>
                  <div className="mt-1 text-[11px] text-faint-foreground">{label}</div>
                </div>
              ))}
            </div>
          )}
          {panel === 'knowledge' && (
            <div className="space-y-2 text-[12px] leading-relaxed text-muted-foreground">
              <p className="rounded-md border border-border bg-card px-3 py-2">
                Build knowledge is frozen by the server InputManifest and BuildManifest. Context selection is reviewed in the workflow; this panel does not mutate it locally.
              </p>
              <p className="rounded-md border border-border bg-card px-3 py-2">
                {artifactWorkspace.documents.length} server document artifact{artifactWorkspace.documents.length === 1 ? '' : 's'} are visible. Only exact approved revisions included by the workflow reach AI generation.
              </p>
            </div>
          )}
          {panel === 'connectors' && (
            <div className="space-y-2">
              {['Figma', 'Penpot', 'Storybook / Ladle', 'Supabase'].map((item) => (
                <div
                  key={item}
                  className="flex items-center justify-between rounded-md border border-border bg-card px-3 py-2 text-[12px]"
                >
                  <span className="text-muted-foreground">{item}</span>
                  <span className="text-faint-foreground">Not configured</span>
                </div>
              ))}
            </div>
          )}
          {panel === 'stripe' && (
            <div className="space-y-3">
              <p className="text-[12px] leading-relaxed text-muted-foreground">
                {t('workbench.panel.stripeCopy')}
              </p>
              <button
                type="button"
                onClick={() => {
                  setComposerDraft(
                    'Add Stripe billing with subscription plans, webhook handling, customer portal access, and billing state in the dashboard.',
                  )
                  showNotice(t('workbench.notice.stripeContextAdded'))
                }}
                className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright"
              >
                {t('workbench.panel.addBillingContext')}
              </button>
            </div>
          )}
        </TopBarPanel>
      )}
    </header>
  )
}

function panelTitle(panel: string, t: (key: MessageKey) => string) {
  const map: Record<string, MessageKey> = {
    versions: 'workbench.panel.versionHistory',
    rename: 'workbench.panel.renameProject',
    export: 'workbench.panel.exportProject',
    delete: 'workbench.panel.deleteProject',
    connect: 'workbench.panel.githubConnection',
    publish: 'workbench.panel.publishResult',
    analytics: 'workbench.panel.analytics',
    knowledge: 'workbench.panel.knowledge',
    connectors: 'workbench.panel.connectors',
    stripe: 'workbench.panel.stripeIntegration',
  }
  return t(map[String(panel)] ?? 'workbench.panel.projectAction')
}

function TopBarPanel({
  title,
  children,
  footer,
  onClose,
  wide = false,
}: {
  title: string
  children: React.ReactNode
  footer: React.ReactNode
  onClose: () => void
  wide?: boolean
}) {
  const { t } = useI18n()

  return (
    <div className={cn('absolute right-3 top-[calc(100%+4px)] z-50 rounded-lg border border-border bg-popover shadow-2xl shadow-black/50 max-md:left-3 max-md:w-auto', wide ? 'w-[620px]' : 'w-[360px]')}>
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <h3 className="text-sm font-semibold text-foreground">{title}</h3>
        <button
          type="button"
          onClick={onClose}
          className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-white/5 hover:text-foreground"
          aria-label={t('common.close')}
        >
          <X className="h-4 w-4" />
        </button>
      </div>
      <div className="max-h-[560px] overflow-y-auto scrollbar-thin p-4">{children}</div>
      <div className="flex justify-end border-t border-border px-4 py-3">{footer}</div>
    </div>
  )
}

const EXPORT_OPTIONS: { key: string; labelKey: MessageKey }[] = [
  { key: 'sourceZip', labelKey: 'workbench.export.sourceZip' },
]

function IconButton({
  children,
  label,
  onClick,
}: {
  children: React.ReactNode
  label: string
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-white/5 hover:text-foreground"
      aria-label={label}
      title={label}
    >
      {children}
    </button>
  )
}

function Menu({
  children,
  align = 'left',
}: {
  children: React.ReactNode
  align?: 'left' | 'right'
}) {
  return (
    <div
      role="menu"
      className={cn(
        'absolute top-[calc(100%+6px)] z-50 w-56 rounded-lg border border-border-strong bg-popover p-1 shadow-2xl shadow-black/50',
        align === 'right' ? 'right-0' : 'left-0',
      )}
    >
      {children}
    </div>
  )
}

function MenuItem({
  children,
  icon: Icon,
  danger,
  onClick,
}: {
  children: React.ReactNode
  icon: typeof Home
  danger?: boolean
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      className={cn(
        'flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[13px] transition-colors',
        danger
          ? 'text-destructive hover:bg-destructive/10'
          : 'text-foreground hover:bg-white/5',
      )}
    >
      <Icon className={cn('h-4 w-4', !danger && 'text-muted-foreground')} />
      {children}
    </button>
  )
}

function MenuDivider() {
  return <div className="my-1 h-px bg-border" />
}
