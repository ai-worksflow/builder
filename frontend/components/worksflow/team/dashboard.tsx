'use client'

import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { ACTIVITY } from '@/lib/worksflow/mock-data'
import { DOC_STATUS_CLASS } from '@/lib/worksflow/labels'
import { useLocalizedLabels } from '../use-localized-labels'
import { Avatar, StatusPill, memberById } from '../shared'
import {
  Boxes,
  FilePlus2,
  GitFork,
  Layers,
  Sparkles,
  TriangleAlert,
  UploadCloud,
} from 'lucide-react'

export function TeamDashboard() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const {
    activeTeamProject,
    openDoc,
    setTeamView,
    documents,
    dependencies,
    createDocument,
    generateDocumentChain,
    createBlankDocumentGraph,
    createDocumentGraphFromTemplate,
    createDocumentGraphFromBlueprint,
  } = useWorksflow()

  const myDocs = documents.filter((d) =>
    d.members.some((m) => m.userId === 'm1' || d.ownerId === 'm1'),
  ).slice(0, 4)

  const blocked = dependencies.filter((d) => d.isBlocking)
  const approved = documents.filter((d) => d.status === 'approved').length
  const completion = documents.length ? Math.round((approved / documents.length) * 100) : 0

  return (
    <div className="h-full overflow-y-auto scrollbar-thin">
      <div className="mx-auto max-w-5xl px-6 py-6 max-sm:px-4">
        {/* Header */}
        <div className="mb-6 flex flex-wrap items-start justify-between gap-4">
          <div>
            <h1 className="text-xl font-semibold text-foreground">{activeTeamProject.name}</h1>
            <p className="mt-1 text-[13px] text-muted-foreground">
              {t('team.dashboard.description')}
            </p>
          </div>
          <div className="flex flex-wrap gap-2 max-sm:w-full">
            <PrimaryCta
              icon={FilePlus2}
              onClick={() => createDocument('featureList', t('team.dashboard.newFeatureDraft'))}
            >
              {t('team.dashboard.createDocument')}
            </PrimaryCta>
            <SecondaryCta icon={Sparkles} onClick={generateDocumentChain}>
              {t('team.dashboard.generateChain')}
            </SecondaryCta>
            <SecondaryCta icon={Boxes} onClick={() => setTeamView('blueprint')}>
              {t('team.dashboard.openBlueprint')}
            </SecondaryCta>
            <SecondaryCta icon={UploadCloud} onClick={() => setTeamView('imports')}>
              {t('team.dashboard.importPrototype')}
            </SecondaryCta>
          </div>
        </div>

        {documents.length === 0 && (
          <section className="mb-6 rounded-lg border border-dashed border-border bg-panel p-5">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div className="max-w-xl">
                <div className="flex items-center gap-2 text-sm font-semibold text-foreground">
                  <Layers className="size-4 text-primary-bright" />
                  {t('team.dashboard.emptyDocsTitle')}
                </div>
                <p className="mt-2 text-[13px] leading-relaxed text-muted-foreground">
                  {t('team.dashboard.emptyDocsBody')}
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                <SecondaryCta icon={Sparkles} onClick={createDocumentGraphFromTemplate}>
                  {t('team.dashboard.createTemplateGraph')}
                </SecondaryCta>
                <SecondaryCta icon={Boxes} onClick={createDocumentGraphFromBlueprint}>
                  {t('team.dashboard.createBlueprintGraph')}
                </SecondaryCta>
                <SecondaryCta icon={GitFork} onClick={createBlankDocumentGraph}>
                  {t('graph.createBlank')}
                </SecondaryCta>
              </div>
            </div>
          </section>
        )}

        {/* Overview stats */}
        <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
          <StatCard label={t('team.dashboard.currentPhase')} value={activeTeamProject.phase} />
          <StatCard label={t('team.dashboard.completion')} value={`${completion}%`} accent />
          <StatCard label={t('team.dashboard.totalDocuments')} value={String(documents.length)} />
          <StatCard label={t('team.dashboard.blockedDependencies')} value={String(blocked.length)} danger />
        </div>

        <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
          {/* My assigned documents */}
          <section className="rounded-lg border border-border bg-panel lg:col-span-2">
            <SectionHeader
              title={t('team.dashboard.myAssignedDocs')}
              action={t('team.dashboard.openGraph')}
              onAction={() => setTeamView('graph')}
              icon={GitFork}
            />
            <ul className="divide-y divide-border">
              {myDocs.map((doc) => (
                <li key={doc.id}>
                  <button
                    type="button"
                    onClick={() => openDoc(doc.id)}
                    className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-white/5 max-sm:flex-wrap"
                  >
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-[13px] font-medium text-foreground">
                        {doc.title}
                      </p>
                      <p className="text-[11px] text-faint-foreground">
                        {labels.docType(doc.type)} · {doc.updatedAt}
                      </p>
                    </div>
                    <StatusPill
                      label={labels.docStatus(doc.status)}
                      className={DOC_STATUS_CLASS[doc.status]}
                    />
                    <Avatar member={memberById(doc.ownerId)!} size={24} />
                  </button>
                </li>
              ))}
              {myDocs.length === 0 && (
                <li className="px-4 py-6 text-sm text-faint-foreground">
                  {t('team.dashboard.emptyDocsTitle')}
                </li>
              )}
            </ul>
          </section>

          {/* Recent activity */}
          <section className="rounded-lg border border-border bg-panel">
            <SectionHeader title={t('team.dashboard.recentActivity')} />
            <ul className="space-y-3 p-4">
              {ACTIVITY.map((item) => {
                const m = memberById(item.memberId)!
                return (
                  <li key={item.id} className="flex items-start gap-2.5">
                    <Avatar member={m} size={22} />
                    <div className="min-w-0 flex-1 text-[12px] leading-relaxed">
                      <span className="font-medium text-foreground">{m.name}</span>{' '}
                      <span className="text-muted-foreground">{item.action}</span>{' '}
                      <span className="text-foreground">{item.target}</span>
                      <div className="text-[11px] text-faint-foreground">{item.time}</div>
                    </div>
                  </li>
                )
              })}
            </ul>
          </section>
        </div>

        {/* Blocked dependencies */}
        <section className="mt-4 rounded-lg border border-border bg-panel">
          <SectionHeader title={t('team.dashboard.blockedDependencies')} icon={TriangleAlert} />
          <ul className="divide-y divide-border">
            {blocked.map((dep) => {
              const source = documents.find((d) => d.id === dep.sourceDocId)!
              const target = documents.find((d) => d.id === dep.targetDocId)!
              return (
                <li key={dep.id} className="flex items-center gap-3 px-4 py-3 text-[13px] max-sm:flex-wrap">
                  <TriangleAlert className="h-4 w-4 shrink-0 text-warning" />
                  <span className="min-w-0 font-medium text-foreground max-sm:w-full">{source.title}</span>
                  <span className="text-faint-foreground">{labels.dependency('blocks')}</span>
                  <span className="min-w-0 text-muted-foreground">{target.title}</span>
                  <div className="ml-auto flex items-center gap-2 max-sm:ml-0">
                    <span className="text-[11px] text-faint-foreground">{t('common.owner')}</span>
                    <Avatar member={memberById(source.ownerId)!} size={22} />
                  </div>
                </li>
              )
            })}
            {blocked.length === 0 && (
              <li className="px-4 py-5 text-sm text-faint-foreground">
                {t('graph.noDependencies')}
              </li>
            )}
          </ul>
        </section>
      </div>
    </div>
  )
}

function StatCard({
  label,
  value,
  accent,
  danger,
}: {
  label: string
  value: string
  accent?: boolean
  danger?: boolean
}) {
  return (
    <div className="rounded-lg border border-border bg-panel p-4">
      <p className="text-[11px] uppercase tracking-wide text-faint-foreground">{label}</p>
      <p
        className={
          'mt-1 text-2xl font-semibold ' +
          (accent ? 'text-primary-bright' : danger ? 'text-warning' : 'text-foreground')
        }
      >
        {value}
      </p>
    </div>
  )
}

function SectionHeader({
  title,
  action,
  onAction,
  icon: Icon,
}: {
  title: string
  action?: string
  onAction?: () => void
  icon?: typeof GitFork
}) {
  return (
    <div className="flex items-center justify-between border-b border-border px-4 py-3">
      <div className="flex items-center gap-2 text-[13px] font-semibold text-foreground">
        {Icon && <Icon className="h-4 w-4 text-muted-foreground" />}
        {title}
      </div>
      {action && (
        <button
          type="button"
          onClick={onAction}
          className="text-[12px] font-medium text-primary-bright hover:underline"
        >
          {action}
        </button>
      )}
    </div>
  )
}

function PrimaryCta({
  children,
  icon: Icon,
  onClick,
}: {
  children: React.ReactNode
  icon: typeof GitFork
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex items-center justify-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright max-sm:flex-1"
    >
      <Icon className="h-3.5 w-3.5" />
      {children}
    </button>
  )
}

function SecondaryCta({
  children,
  icon: Icon,
  onClick,
}: {
  children: React.ReactNode
  icon: typeof GitFork
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex items-center justify-center gap-1.5 rounded-md border border-border px-3 py-2 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground max-sm:flex-1"
    >
      <Icon className="h-3.5 w-3.5" />
      {children}
    </button>
  )
}
