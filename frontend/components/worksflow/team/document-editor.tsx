'use client'

import { useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import { MEMBERS } from '@/lib/worksflow/mock-data'
import { DOC_STATUS_CLASS } from '@/lib/worksflow/labels'
import type { DocMemberRole } from '@/lib/worksflow/types'
import { useLocalizedLabels } from '../use-localized-labels'
import { Avatar, StatusPill, memberById } from '../shared'
import {
  ArrowUpRight,
  Check,
  ChevronLeft,
  Clock,
  Download,
  FileText,
  GitBranch,
  Link2,
  Save,
  Send,
  Sparkles,
  TriangleAlert,
  Workflow,
} from 'lucide-react'

const COMMENTS = [
  { id: 'c1', memberId: 'm6', body: '边界条件里补一下批量操作上限的具体数值。', time: '25m ago', resolved: false },
  { id: 'c2', memberId: 'm4', body: '权限三级需要和 API 契约里的角色对齐。', time: '18m ago', resolved: false },
  { id: 'c3', memberId: 'm2', body: '已按需求文档更新了任务流转部分。', time: '1h ago', resolved: true },
]

const ROLES: DocMemberRole[] = ['owner', 'assignee', 'downstreamOwner', 'reviewer', 'watcher']

export function DocumentEditor() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const {
    selectedDocId,
    setTeamView,
    openDoc,
    useDocInWorkbench,
    documents,
    createDocument,
    dependencies,
    updateDocumentStatus,
    addDocumentDependency,
    addDocumentMember,
    removeDocumentMember,
    saveDocumentDraft,
  } = useWorksflow()
  const [tab, setTab] = useState<'content' | 'comments' | 'history'>('content')
  const [draft, setDraft] = useState('')
  const [notice, setNotice] = useState<string | null>(null)
  const [exported, setExported] = useState(false)
  const [memberToAdd, setMemberToAdd] = useState('m4')
  const [roleToAdd, setRoleToAdd] = useState<DocMemberRole>('assignee')

  const doc = documents.find((d) => d.id === selectedDocId) ?? documents[0]

  if (!doc) {
    return (
      <div className="flex h-full items-center justify-center bg-canvas p-6">
        <div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-5 text-center">
          <FileText className="mx-auto size-8 text-primary-bright" />
          <h2 className="mt-3 text-base font-semibold text-foreground">{t('graph.emptyTitle')}</h2>
          <p className="mt-2 text-sm text-muted-foreground">{t('graph.emptyBody')}</p>
          <div className="mt-4 flex justify-center gap-2">
            <button
              type="button"
              onClick={() => setTeamView('graph')}
              className="rounded-lg border border-border px-3 py-2 text-xs font-medium text-muted-foreground hover:bg-white/5"
            >
              {t('team.dashboard.openGraph')}
            </button>
            <button
              type="button"
              onClick={() => createDocument('requirement', 'Project brief')}
              className="rounded-lg bg-primary px-3 py-2 text-xs font-semibold text-primary-foreground hover:bg-primary-bright"
            >
              {t('team.dashboard.createDocument')}
            </button>
          </div>
        </div>
      </div>
    )
  }

  const downstream = dependencies.filter((e) => e.sourceDocId === doc.id)
  const upstream = dependencies.filter((e) => e.targetDocId === doc.id)

  return (
    <div className="flex h-full max-lg:flex-col max-lg:overflow-y-auto">
      {/* Main editor column */}
      <div className="flex min-w-0 flex-1 flex-col max-lg:flex-none">
        {/* Header */}
        <div className="border-b border-border bg-surface px-6 py-3 max-sm:px-4">
          <div className="flex items-center gap-2 text-xs text-faint-foreground">
            <button
              onClick={() => setTeamView('graph')}
              className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 hover:bg-white/5 hover:text-foreground"
            >
              <ChevronLeft className="size-3.5" /> {t('editor.breadcrumbGraph')}
            </button>
            <span>/</span>
            <span>{labels.docType(doc.type)}</span>
          </div>
          <div className="mt-2 flex items-center justify-between gap-4 max-md:flex-wrap">
            <div className="flex min-w-0 items-center gap-3 max-sm:flex-wrap">
              <h1 className="min-w-0 text-lg font-semibold text-foreground max-sm:w-full">{doc.title}</h1>
              <StatusPill
                label={labels.docStatus(doc.status)}
                className={DOC_STATUS_CLASS[doc.status]}
              />
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <div className="mr-1 flex items-center">
                {doc.members.slice(0, 4).map((m, i) => {
                  const mem = memberById(m.userId)
                  return mem ? (
                    <div key={m.userId} style={{ marginLeft: i === 0 ? 0 : -8 }}>
                      <Avatar member={mem} size={26} ring />
                    </div>
                  ) : null
                })}
              </div>
              <button
                onClick={() => {
                  saveDocumentDraft(doc.id)
                  setNotice(
                    doc.status === 'approved'
                      ? t('editor.approvedCheckpointSaved')
                      : t('editor.draftSaved'),
                  )
                }}
                className="inline-flex items-center gap-1.5 rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5"
              >
                <Save className="size-3.5" /> {t('editor.saveDraft')}
              </button>
              <button
                onClick={() => {
                  setExported(true)
                  setNotice(t('editor.exportPrepared', { title: doc.title }))
                }}
                className="inline-flex items-center gap-1.5 rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5"
              >
                <Download className="size-3.5" /> {exported ? t('editor.exported') : t('common.export')}
              </button>
              <button
                onClick={() => {
                  updateDocumentStatus(doc.id, 'readyForReview')
                  setNotice(t('editor.reviewRequestedNotice'))
                }}
                className="inline-flex items-center gap-1.5 rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5"
              >
                <GitBranch className="size-3.5" /> {t('editor.requestReview')}
              </button>
              <button
                onClick={() => {
                  updateDocumentStatus(doc.id, 'approved')
                  setNotice(t('editor.approvedNotice'))
                }}
                className="inline-flex items-center gap-1.5 rounded-lg bg-success/90 px-3 py-1.5 text-xs font-medium text-white hover:bg-success"
              >
                <Check className="size-3.5" /> {t('editor.approve')}
              </button>
            </div>
          </div>

          {/* Tabs */}
          <div className="mt-3 flex items-center gap-1">
            {(['content', 'comments', 'history'] as const).map((tabKey) => (
              <button
                key={tabKey}
                onClick={() => setTab(tabKey)}
                className={cn(
                  'rounded-md px-3 py-1.5 text-xs font-medium capitalize transition-colors',
                  tab === tabKey
                    ? 'bg-white/10 text-foreground'
                    : 'text-faint-foreground hover:text-muted-foreground',
                )}
              >
                {tabKey === 'content'
                  ? t('editor.tab.content')
                  : tabKey === 'comments'
                    ? t('editor.tab.comments', {
                        count: COMMENTS.filter((c) => !c.resolved).length,
                      })
                    : t('editor.tab.history')}
              </button>
            ))}
          </div>
        </div>

        {/* Body */}
        <div className="flex-1 overflow-y-auto scrollbar-thin bg-canvas">
          {tab === 'content' && (
            <div className="mx-auto max-w-3xl px-8 py-8 max-sm:px-4">
              {doc.status === 'needsSync' && (
                <div className="mb-6 flex items-start gap-3 rounded-lg border border-amber-400/30 bg-amber-400/5 p-4">
                  <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
                  <div>
                    <div className="text-sm font-medium text-warning">
                      {t('editor.needsSyncTitle')}
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t('editor.needsSyncCopy')}
                    </p>
                    <button className="mt-2 inline-flex items-center gap-1.5 rounded-md bg-warning/20 px-2.5 py-1 text-xs font-medium text-warning hover:bg-warning/30">
                      <Sparkles className="size-3" /> {t('editor.syncWithAi')}
                    </button>
                  </div>
                </div>
              )}

              {notice && (
                <div className="mb-6 rounded-lg border border-primary/30 bg-primary/10 px-3 py-2 text-xs text-primary-bright">
                  {notice}
                </div>
              )}

              <p className="text-sm leading-relaxed text-muted-foreground">{doc.summary}</p>

              <div className="mt-6 space-y-6">
                {doc.sections.map((s, i) => (
                  <section key={i}>
                    <h2 className="text-sm font-semibold text-foreground">{s.title}</h2>
                    <p className="mt-2 rounded-lg border border-border bg-surface-2 p-4 text-sm leading-relaxed text-muted-foreground">
                      {s.body}
                    </p>
                  </section>
                ))}
              </div>

              <div className="mt-8 rounded-lg border border-dashed border-border p-4 text-center">
                <Sparkles className="mx-auto size-4 text-primary-bright" />
                <p className="mt-1.5 text-xs text-muted-foreground">
                  {t('editor.expandWithAi')}
                </p>
              </div>
            </div>
          )}

          {tab === 'comments' && (
            <div className="mx-auto max-w-2xl px-8 py-6 max-sm:px-4">
              <div className="space-y-3">
                {COMMENTS.map((c) => {
                  const mem = memberById(c.memberId)
                  return (
                    <div
                      key={c.id}
                      className={cn(
                        'rounded-lg border p-3',
                        c.resolved
                          ? 'border-border bg-surface/50 opacity-60'
                          : 'border-border bg-surface-2',
                      )}
                    >
                      <div className="flex items-center gap-2">
                        {mem && <Avatar member={mem} size={22} />}
                        <span className="text-xs font-medium text-foreground">{mem?.name}</span>
                        <span className="text-[10px] text-faint-foreground">{c.time}</span>
                        {c.resolved && (
                          <span className="ml-auto inline-flex items-center gap-1 text-[10px] text-success">
                            <Check className="size-3" /> {t('editor.resolved')}
                          </span>
                        )}
                      </div>
                      <p className="mt-2 text-sm text-muted-foreground">{c.body}</p>
                    </div>
                  )
                })}
              </div>
              <div className="mt-4 flex items-center gap-2 rounded-lg border border-border bg-surface-2 p-2 max-sm:flex-wrap">
                <input
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  placeholder={t('editor.addComment')}
                  className="flex-1 bg-transparent px-2 text-sm text-foreground outline-none placeholder:text-faint-foreground"
                />
                <button
                  onClick={() => setDraft('')}
                  className="inline-flex items-center gap-1 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-white hover:bg-primary/90"
                >
                  <Send className="size-3" /> {t('editor.send')}
                </button>
              </div>
            </div>
          )}

          {tab === 'history' && (
            <div className="mx-auto max-w-2xl px-8 py-6 max-sm:px-4">
              <ol className="relative space-y-5 border-l border-border pl-5">
                {[
                  { who: 'm2', what: '更新了「边界条件」部分', when: '1h ago' },
                  { who: 'm6', what: '请求评审，指派给 QA', when: '30m ago' },
                  { who: 'm1', what: '把状态设为 Ready for Review', when: '2h ago' },
                  { who: 'm2', what: '创建了文档', when: 'Yesterday' },
                ].map((h, i) => {
                  const mem = memberById(h.who)
                  return (
                    <li key={i} className="relative">
                      <span className="absolute -left-[26px] top-1 flex size-3 items-center justify-center rounded-full bg-primary ring-4 ring-canvas" />
                      <div className="flex items-center gap-2">
                        {mem && <Avatar member={mem} size={20} />}
                        <span className="text-xs font-medium text-foreground">{mem?.name}</span>
                        <span className="text-xs text-muted-foreground">{h.what}</span>
                      </div>
                      <span className="mt-0.5 flex items-center gap-1 text-[10px] text-faint-foreground">
                        <Clock className="size-2.5" /> {h.when}
                      </span>
                    </li>
                  )
                })}
              </ol>
            </div>
          )}
        </div>
      </div>

      {/* Right rail: bindings & relations */}
      <aside className="flex w-72 shrink-0 flex-col border-l border-border bg-surface max-lg:w-full max-lg:max-h-[420px] max-lg:border-l-0 max-lg:border-t">
        <div className="border-b border-border p-4">
          <div className="text-[11px] font-semibold uppercase tracking-wider text-faint-foreground">
            {t('editor.relations')}
          </div>
          <button
            onClick={() => {
              useDocInWorkbench(doc.id)
            }}
            className="mt-3 inline-flex w-full items-center justify-center gap-1.5 rounded-lg border border-primary/40 bg-primary/10 px-3 py-2 text-xs font-medium text-primary-bright hover:bg-primary/20"
          >
            <Workflow className="size-3.5" /> {t('editor.generateInWorkbench')}
            <ArrowUpRight className="size-3" />
          </button>
        </div>

        <div className="flex-1 overflow-y-auto scrollbar-thin p-4">
          <SideLabel>{t('common.member')}</SideLabel>
          <div className="mt-2 grid grid-cols-[1fr_1fr_auto] gap-1.5">
            <select
              value={memberToAdd}
              onChange={(event) => setMemberToAdd(event.target.value)}
              className="min-w-0 rounded-md border border-border bg-background px-2 py-1.5 text-[11px] text-foreground outline-none focus:border-primary/60"
            >
              {MEMBERS.map((member) => (
                <option key={member.id} value={member.id}>
                  {member.name}
                </option>
              ))}
            </select>
            <select
              value={roleToAdd}
              onChange={(event) => setRoleToAdd(event.target.value as DocMemberRole)}
              className="min-w-0 rounded-md border border-border bg-background px-2 py-1.5 text-[11px] text-foreground outline-none focus:border-primary/60"
            >
              {ROLES.map((role) => (
                <option key={role} value={role}>
                  {labels.role(role)}
                </option>
              ))}
            </select>
            <button
              type="button"
              onClick={() => addDocumentMember(doc.id, memberToAdd, roleToAdd)}
              className="rounded-md bg-primary px-2 py-1.5 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright"
            >
              {t('common.add')}
            </button>
          </div>
          <div className="mt-2 space-y-2">
            {doc.members.map((m) => {
              const mem = memberById(m.userId)
              if (!mem) return null
              return (
                <div key={`${m.userId}-${m.role}`} className="flex items-center gap-2">
                  <Avatar member={mem} size={24} />
                  <span className="min-w-0 flex-1 truncate text-xs text-foreground">
                    {mem.name}
                  </span>
                  <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground">
                    {labels.role(m.role)}
                  </span>
                  <button
                    type="button"
                    onClick={() => removeDocumentMember(doc.id, m.userId, m.role)}
                    className="rounded px-1 text-[10px] text-faint-foreground hover:bg-white/5 hover:text-foreground"
                    aria-label={`${t('common.remove')} ${mem.name} ${labels.role(m.role)}`}
                  >
                    ×
                  </button>
                </div>
              )
            })}
          </div>

          {upstream.length > 0 && (
            <>
              <SideLabel className="mt-5">{t('editor.upstreamDocs')}</SideLabel>
              <div className="mt-2 space-y-1.5">
                {upstream.map((e) => {
                  const d = documents.find((x) => x.id === e.sourceDocId)
                  return (
                    <RelationRow
                      key={e.id}
                      title={d?.title ?? ''}
                      onClick={() => d && openDoc(d.id)}
                    />
                  )
                })}
              </div>
            </>
          )}

          {downstream.length > 0 && (
            <>
              <SideLabel className="mt-5">{t('editor.downstreamDocs')}</SideLabel>
              <div className="mt-2 space-y-1.5">
                {downstream.map((e) => {
                  const d = documents.find((x) => x.id === e.targetDocId)
                  return (
                    <RelationRow
                      key={e.id}
                      title={d?.title ?? ''}
                      blocking={e.isBlocking}
                      onClick={() => d && openDoc(d.id)}
                    />
                  )
                })}
              </div>
            </>
          )}

          <SideLabel className="mt-5">{t('editor.blueprintBinding')}</SideLabel>
          <div className="mt-2 flex items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2">
            <Link2 className="size-3.5 text-primary-bright" />
            <span className="text-xs text-muted-foreground">
              {t('editor.blueprintBindingsCount', { count: doc.bindings })}
            </span>
          </div>
          <button
            onClick={() => {
              const targets = documents.filter((candidate) => candidate.id !== doc.id)
              const nextTarget = targets.find(
                (candidate) =>
                  !dependencies.some(
                    (e) => e.sourceDocId === doc.id && e.targetDocId === candidate.id,
                  ),
              )
              if (!nextTarget) return
              addDocumentDependency(doc.id, nextTarget.id, 'generates', false)
              setNotice(t('editor.downstreamGeneratedNotice', { target: nextTarget.title }))
            }}
            className="mt-4 inline-flex w-full items-center justify-center gap-1.5 rounded-lg border border-dashed border-border px-3 py-2 text-xs font-medium text-muted-foreground hover:border-primary/40 hover:text-foreground"
          >
            <Sparkles className="size-3.5 text-primary-bright" /> {t('editor.generateDownstream')}
          </button>
        </div>
      </aside>
    </div>
  )
}

function RelationRow({
  title,
  blocking,
  onClick,
}: {
  title: string
  blocking?: boolean
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      className="flex w-full items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-left hover:border-white/20"
    >
      {blocking ? (
        <TriangleAlert className="size-3.5 shrink-0 text-destructive" />
      ) : (
        <GitBranch className="size-3.5 shrink-0 text-faint-foreground" />
      )}
      <span className="min-w-0 flex-1 truncate text-xs font-medium text-foreground">{title}</span>
      <ArrowUpRight className="size-3 text-faint-foreground" />
    </button>
  )
}

function SideLabel({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        'text-[11px] font-semibold uppercase tracking-wider text-faint-foreground',
        className,
      )}
    >
      {children}
    </div>
  )
}
