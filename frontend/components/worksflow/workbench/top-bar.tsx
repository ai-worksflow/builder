'use client'

import { useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { DOC_STATUS_CLASS } from '@/lib/worksflow/labels'
import type { WorkbenchView } from '@/lib/worksflow/types'
import { useDropdown } from '../use-dropdown'
import { LanguageToggle } from '../language-toggle'
import { useLocalizedLabels } from '../use-localized-labels'
import { StatusPill } from '../shared'
import {
  BarChart3,
  Blocks,
  ChevronDown,
  Code2,
  Copy,
  Database,
  Download,
  FileClock,
  FileText,
  GitBranch,
  Home,
  Link2,
  Monitor,
  MoreHorizontal,
  Pencil,
  Plug,
  Rocket,
  Share2,
  Star,
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
    view,
    setView,
    projectName,
    setProjectName,
    setSurface,
    setComposerDraft,
    duplicateProject,
    versions,
    linkedDocIds,
    documents,
    toggleLinkedDoc,
    setTeamView,
  } = useWorksflow()
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const projectMenu = useDropdown()
  const moreMenu = useDropdown()
  const [notice, setNotice] = useState<string | null>(null)
  const [publishState, setPublishState] = useState<'idle' | 'publishing' | 'published'>('idle')
  const [panel, setPanel] = useState<
    | null
    | 'versions'
    | 'transfer'
    | 'rename'
    | 'export'
    | 'delete'
    | 'connect'
    | 'share'
    | 'publish'
    | 'analytics'
    | 'knowledge'
    | 'connectors'
    | 'stripe'
    | 'linkedDocs'
  >(null)
  const [renameDraft, setRenameDraft] = useState(projectName)
  const [githubConnected, setGithubConnected] = useState(false)
  const linkedDocs = linkedDocIds
    .map((id) => documents.find((doc) => doc.id === id))
    .filter((doc): doc is (typeof documents)[number] => Boolean(doc))
  const availableDocs = documents.filter((doc) => !linkedDocIds.includes(doc.id))

  function showNotice(message: string) {
    setNotice(message)
    window.setTimeout(() => setNotice(null), 2400)
  }

  return (
    <header className="relative flex min-h-[50px] shrink-0 items-center gap-2 border-b border-border bg-panel px-3 py-2 max-md:flex-wrap">
      <IconButton label={t('workbench.home')} onClick={() => setSurface('recent')}>
        <Home className="h-4 w-4" />
      </IconButton>

      {/* Workspace / project path */}
      <div className="flex min-w-0 flex-1 items-center gap-1.5 text-sm md:flex-none">
        <span className="shrink-0 rounded-md bg-white/5 px-2 py-1 text-xs font-medium text-muted-foreground">
          Acme
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
              <MenuItem icon={Share2} onClick={() => setPanel('transfer')}>
                {t('workbench.menu.transferTo')}
              </MenuItem>
              <MenuDivider />
              <MenuItem
                icon={Pencil}
                onClick={() => {
                  setRenameDraft(projectName)
                  setPanel('rename')
                }}
              >
                {t('workbench.menu.rename')}
              </MenuItem>
              <MenuItem
                icon={Copy}
                onClick={() => {
                  duplicateProject()
                  showNotice(t('workbench.notice.projectDuplicated'))
                }}
              >
                {t('workbench.menu.duplicate')}
              </MenuItem>
              <MenuItem icon={Star} onClick={() => showNotice(t('workbench.notice.projectStarred'))}>
                {t('workbench.menu.starProject')}
              </MenuItem>
              <MenuItem icon={Download} onClick={() => setPanel('export')}>
                {t('workbench.menu.export')}
              </MenuItem>
              <MenuDivider />
              <MenuItem icon={Trash2} danger onClick={() => setPanel('delete')}>
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
          onClick={() => setPanel('linkedDocs')}
          className="hidden items-center gap-1 rounded-md border border-primary/30 bg-primary/10 px-2 py-1 text-[11px] font-medium text-primary-bright hover:bg-primary/15 md:inline-flex"
        >
          <Link2 className="h-3 w-3" />
          {t('workbench.linkedDocs', { count: linkedDocIds.length })}
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
          onClick={() => setPanel('share')}
          className="flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-[13px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
        >
          <Share2 className="h-4 w-4" />
          <span className="hidden lg:inline">{t('workbench.share')}</span>
        </button>
        <button
          type="button"
          onClick={() => {
            setPublishState('publishing')
            window.setTimeout(() => {
              setPublishState('published')
              setPanel('publish')
            }, 900)
          }}
          className="flex h-8 items-center gap-1.5 rounded-md bg-primary px-3 text-[13px] font-semibold text-primary-foreground hover:bg-primary-bright max-sm:w-8 max-sm:px-0 max-sm:justify-center"
        >
          <Rocket className="h-4 w-4 shrink-0" />
          <span className="max-sm:hidden">
            {publishState === 'publishing'
              ? t('workbench.publishing')
              : publishState === 'published'
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
          onClose={() => setPanel(null)}
          footer={
            panel === 'rename' ? (
              <button
                type="button"
                onClick={() => {
                  setProjectName(renameDraft.trim() || projectName)
                  setPanel(null)
                }}
                className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright"
              >
                {t('workbench.panel.saveName')}
              </button>
            ) : panel === 'delete' ? (
              <button
                type="button"
                onClick={() => {
                  showNotice(t('workbench.notice.deleteConfirmed'))
                  setPanel(null)
                }}
                className="rounded-md bg-destructive px-3 py-1.5 text-[12px] font-semibold text-destructive-foreground hover:opacity-90"
              >
                {t('workbench.panel.confirmDelete')}
              </button>
            ) : panel === 'connect' ? (
              <button
                type="button"
                onClick={() => {
                  setGithubConnected(true)
                  setPanel(null)
                }}
                className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright"
              >
                {t('workbench.panel.connectGithub')}
              </button>
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
              {versions.map((version) => (
                <div key={version.id} className="rounded-md border border-border bg-card px-3 py-2">
                  <div className="text-sm font-medium text-foreground">{version.title}</div>
                  <div className="mt-0.5 text-[11px] text-faint-foreground">{version.subtitle}</div>
                </div>
              ))}
            </div>
          )}
          {panel === 'linkedDocs' && (
            <div className="space-y-3">
              <div className="rounded-md border border-primary/25 bg-primary/10 px-3 py-2 text-[12px] leading-relaxed text-primary-bright">
                {t('chat.linkedDocs')} · {t('chat.contextLocked')}
              </div>
              <div className="space-y-1.5">
                {linkedDocs.map((doc) => (
                  <div
                    key={doc.id}
                    className="flex items-center justify-between gap-2 rounded-md border border-border bg-card px-3 py-2"
                  >
                    <span className="flex min-w-0 items-start gap-2">
                      <FileText className="mt-0.5 h-3.5 w-3.5 shrink-0 text-faint-foreground" />
                      <span className="min-w-0">
                        <span className="block truncate text-[12px] font-medium text-foreground">
                          {doc.title}
                        </span>
                        <span className="text-[10px] text-faint-foreground">
                          {labels.docType(doc.type)}
                        </span>
                      </span>
                    </span>
                    <span className="flex shrink-0 items-center gap-1.5">
                      <StatusPill
                        label={labels.docStatus(doc.status)}
                        className={DOC_STATUS_CLASS[doc.status]}
                      />
                      <button
                        type="button"
                        onClick={() => toggleLinkedDoc(doc.id)}
                        className="rounded px-1.5 py-0.5 text-[10px] text-faint-foreground hover:bg-white/5 hover:text-foreground"
                      >
                        {t('common.remove')}
                      </button>
                    </span>
                  </div>
                ))}
              </div>
              {availableDocs.length > 0 && (
                <details>
                  <summary className="cursor-pointer rounded-md px-2 py-1 text-[12px] font-medium text-muted-foreground hover:bg-white/5">
                    {t('chat.addLinkedContext')}
                  </summary>
                  <div className="mt-1 space-y-1">
                    {availableDocs.slice(0, 5).map((doc) => (
                      <button
                        key={doc.id}
                        type="button"
                        onClick={() => toggleLinkedDoc(doc.id)}
                        className="flex w-full items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left hover:bg-white/5"
                      >
                        <span className="min-w-0 truncate text-[12px] text-muted-foreground">
                          {doc.title}
                        </span>
                        <span className="shrink-0 text-[10px] text-faint-foreground">
                          {labels.docType(doc.type)}
                        </span>
                      </button>
                    ))}
                  </div>
                </details>
              )}
              <button
                type="button"
                onClick={() => {
                  setPanel(null)
                  setTeamView('graph')
                  setSurface('team')
                }}
                className="inline-flex w-full items-center justify-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
              >
                <GitBranch className="h-3.5 w-3.5" />
                {t('team.nav.graph')}
              </button>
            </div>
          )}
          {panel === 'transfer' && (
            <div className="space-y-2">
              {['Acme / CRM Rewrite', 'Acme / Design Systems', 'Personal workspace'].map((target) => (
                <button
                  key={target}
                  type="button"
                  onClick={() => showNotice(t('workbench.notice.transferTarget', { target }))}
                  className="flex w-full items-center justify-between rounded-md border border-border bg-card px-3 py-2 text-left text-sm text-muted-foreground hover:border-primary/40 hover:text-foreground"
                >
                  {target}
                  <Users2 className="h-4 w-4 text-faint-foreground" />
                </button>
              ))}
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
            <div className="grid grid-cols-2 gap-2">
              {EXPORT_OPTIONS.map((item) => (
                <button
                  key={item.key}
                  type="button"
                  onClick={() =>
                    showNotice(t('workbench.notice.exportPrepared', { item: t(item.labelKey) }))
                  }
                  className="rounded-md border border-border bg-card px-3 py-2 text-left text-[12px] text-muted-foreground hover:border-primary/40 hover:text-foreground"
                >
                  {t(item.labelKey)}
                </button>
              ))}
            </div>
          )}
          {panel === 'delete' && (
            <p className="text-[12px] leading-relaxed text-muted-foreground">
              {t('workbench.panel.deleteCopy')}
            </p>
          )}
          {panel === 'connect' && (
            <div className="rounded-md border border-border bg-card px-3 py-2 text-[12px] text-muted-foreground">
              {githubConnected
                ? t('workbench.panel.githubConnected')
                : t('workbench.panel.githubConnectCopy')}
            </div>
          )}
          {panel === 'share' && (
            <div className="space-y-3">
              <div className="rounded-md border border-border bg-card px-3 py-2 font-mono text-[12px] text-muted-foreground">
                https://worksflow.local/acme/{projectName.toLowerCase().replace(/\s+/g, '-')}
              </div>
              <div className="grid grid-cols-3 gap-2 text-[11px]">
                {[
                  t('common.viewer'),
                  t('common.commenter'),
                  t('common.editor'),
                ].map((role) => (
                  <button
                    key={role}
                    type="button"
                    className="rounded-md border border-border px-2 py-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground"
                  >
                    {role}
                  </button>
                ))}
              </div>
            </div>
          )}
          {panel === 'publish' && (
            <div className="space-y-2">
              <div className="rounded-md border border-emerald-400/30 bg-emerald-400/10 px-3 py-2 text-[12px] text-success">
                {t('workbench.panel.publishedUrlReady')}
              </div>
              <div className="rounded-md border border-border bg-card px-3 py-2 text-[12px] text-muted-foreground">
                {t('workbench.panel.contextLocked', { count: linkedDocIds.length })}
              </div>
            </div>
          )}
          {panel === 'analytics' && (
            <div className="grid grid-cols-3 gap-2">
              {[
                [t('workbench.panel.previewLoads'), '42'],
                [t('workbench.panel.promptRuns'), '8'],
                [t('workbench.panel.syncBacks'), '3'],
              ].map(([label, value]) => (
                <div key={label} className="rounded-md border border-border bg-card p-3">
                  <div className="text-lg font-semibold text-foreground">{value}</div>
                  <div className="mt-1 text-[11px] text-faint-foreground">{label}</div>
                </div>
              ))}
            </div>
          )}
          {panel === 'knowledge' && (
            <div className="space-y-2">
              {[
                t('workbench.panel.approvedDocsOnly'),
                t('workbench.panel.includeApiContracts'),
                t('workbench.panel.includeUiStates'),
              ].map((item) => (
                <label
                  key={item}
                  className="flex items-center justify-between rounded-md border border-border bg-card px-3 py-2 text-[12px] text-muted-foreground"
                >
                  {item}
                  <input type="checkbox" defaultChecked />
                </label>
              ))}
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
                  <span className="text-primary-bright">
                    {item === 'Supabase' ? t('workbench.panel.ready') : t('common.connected')}
                  </span>
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
    transfer: 'workbench.panel.transferProject',
    rename: 'workbench.panel.renameProject',
    export: 'workbench.panel.exportProject',
    delete: 'workbench.panel.deleteProject',
    connect: 'workbench.panel.githubConnection',
    share: 'workbench.panel.shareProject',
    publish: 'workbench.panel.publishResult',
    analytics: 'workbench.panel.analytics',
    knowledge: 'workbench.panel.knowledge',
    connectors: 'workbench.panel.connectors',
    stripe: 'workbench.panel.stripeIntegration',
    linkedDocs: 'chat.linkedDocs',
  }
  return t(map[String(panel)] ?? 'workbench.panel.projectAction')
}

function TopBarPanel({
  title,
  children,
  footer,
  onClose,
}: {
  title: string
  children: React.ReactNode
  footer: React.ReactNode
  onClose: () => void
}) {
  const { t } = useI18n()

  return (
    <div className="absolute right-3 top-[calc(100%+4px)] z-50 w-[360px] rounded-lg border border-border bg-popover shadow-2xl shadow-black/50 max-md:left-3 max-md:w-auto">
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
      <div className="max-h-[360px] overflow-y-auto scrollbar-thin p-4">{children}</div>
      <div className="flex justify-end border-t border-border px-4 py-3">{footer}</div>
    </div>
  )
}

const EXPORT_OPTIONS: { key: string; labelKey: MessageKey }[] = [
  { key: 'sourceZip', labelKey: 'workbench.export.sourceZip' },
  { key: 'documentBundle', labelKey: 'workbench.export.documentBundle' },
  { key: 'previewSnapshot', labelKey: 'workbench.export.previewSnapshot' },
  { key: 'blueprintJson', labelKey: 'workbench.export.blueprintJson' },
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
