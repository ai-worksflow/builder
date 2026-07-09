'use client'

import { useMemo, useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { MEMBERS } from '@/lib/worksflow/mock-data'
import { DOC_STATUS_CLASS } from '@/lib/worksflow/labels'
import { useWorksflow } from '@/lib/worksflow/store'
import type { DocMemberRole } from '@/lib/worksflow/types'
import { useLocalizedLabels } from '../use-localized-labels'
import { Avatar, StatusPill } from '../shared'
import { Link2, ShieldCheck, UserPlus, Users2 } from 'lucide-react'

const ROLES: DocMemberRole[] = ['owner', 'assignee', 'downstreamOwner', 'reviewer', 'watcher']

export function MemberSettings() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const {
    documents,
    selectedDocId,
    setSelectedDocId,
    openDoc,
    addDocumentMember,
    removeDocumentMember,
  } = useWorksflow()
  const [memberId, setMemberId] = useState(MEMBERS[0].id)
  const [role, setRole] = useState<DocMemberRole>('assignee')
  const selectedDoc = documents.find((doc) => doc.id === selectedDocId) ?? documents[0]

  const memberStats = useMemo(
    () =>
      MEMBERS.map((member) => {
        const bindings = documents.flatMap((doc) =>
          doc.members
            .filter((item) => item.userId === member.id)
            .map((item) => ({ doc, role: item.role })),
        )
        return { member, bindings }
      }),
    [documents],
  )

  if (!selectedDoc) {
    return (
      <div className="flex h-full items-center justify-center bg-canvas p-6 text-center">
        <div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-5">
          <Users2 className="mx-auto size-8 text-primary-bright" />
          <h1 className="mt-3 text-base font-semibold text-foreground">{t('graph.emptyTitle')}</h1>
          <p className="mt-2 text-sm text-muted-foreground">{t('graph.emptyBody')}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full">
      <main className="min-w-0 flex-1 overflow-y-auto scrollbar-thin bg-canvas">
        <div className="mx-auto max-w-6xl px-6 py-6 max-sm:px-4">
          <div className="mb-5 flex items-start justify-between gap-4 max-sm:flex-wrap">
            <div>
              <h1 className="text-lg font-semibold text-foreground">{t('members.title')}</h1>
              <p className="mt-1 text-sm text-muted-foreground">
                {t('members.description')}
              </p>
            </div>
            <button
              type="button"
              onClick={() => addDocumentMember(selectedDoc.id, memberId, role)}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary-bright"
            >
              <UserPlus className="h-4 w-4" />
              {t('members.addBinding')}
            </button>
          </div>

          <section className="mb-5 grid grid-cols-1 gap-3 lg:grid-cols-[1fr_360px]">
            <div className="rounded-lg border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-foreground">
                <Users2 className="h-4 w-4 text-primary-bright" />
                {t('members.teamWorkload')}
              </div>
              <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
                {memberStats.map(({ member, bindings }) => (
                  <div key={member.id} className="rounded-lg border border-border bg-card p-3">
                    <div className="flex items-center gap-2">
                      <Avatar member={member} size={28} />
                      <div className="min-w-0">
                        <div className="truncate text-sm font-medium text-foreground">{member.name}</div>
                        <div className="truncate text-[11px] text-faint-foreground">{member.title}</div>
                      </div>
                    </div>
                    <div className="mt-3 flex items-center justify-between text-[12px]">
                      <span className="text-muted-foreground">{t('members.bindings')}</span>
                      <span className="font-medium text-foreground">{bindings.length}</span>
                    </div>
                    <div className="mt-2 flex flex-wrap gap-1">
                      {bindings.slice(0, 3).map((binding) => (
                        <span
                          key={`${binding.doc.id}-${binding.role}`}
                          className="rounded border border-border px-1.5 py-0.5 text-[10px] text-faint-foreground"
                        >
                          {labels.role(binding.role)}
                        </span>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            </div>

            <div className="rounded-lg border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-foreground">
                <ShieldCheck className="h-4 w-4 text-primary-bright" />
                {t('members.bindMember')}
              </div>
              <label className="block text-[12px] text-muted-foreground">
                {t('common.document')}
                <select
                  value={selectedDoc.id}
                  onChange={(event) => setSelectedDocId(event.target.value)}
                  className="mt-1.5 w-full rounded-md border border-border bg-background px-2 py-2 text-sm text-foreground outline-none focus:border-primary/60"
                >
                  {documents.map((doc) => (
                    <option key={doc.id} value={doc.id}>
                      {doc.title}
                    </option>
                  ))}
                </select>
              </label>
              <label className="mt-3 block text-[12px] text-muted-foreground">
                {t('common.member')}
                <select
                  value={memberId}
                  onChange={(event) => setMemberId(event.target.value)}
                  className="mt-1.5 w-full rounded-md border border-border bg-background px-2 py-2 text-sm text-foreground outline-none focus:border-primary/60"
                >
                  {MEMBERS.map((member) => (
                    <option key={member.id} value={member.id}>
                      {member.name} · {member.title}
                    </option>
                  ))}
                </select>
              </label>
              <label className="mt-3 block text-[12px] text-muted-foreground">
                {t('common.role')}
                <select
                  value={role}
                  onChange={(event) => setRole(event.target.value as DocMemberRole)}
                  className="mt-1.5 w-full rounded-md border border-border bg-background px-2 py-2 text-sm text-foreground outline-none focus:border-primary/60"
                >
                  {ROLES.map((item) => (
                    <option key={item} value={item}>
                      {labels.role(item)}
                    </option>
                  ))}
                </select>
              </label>
            </div>
          </section>

          <section className="overflow-x-auto rounded-lg border border-border bg-panel scrollbar-thin">
            <div className="min-w-[720px]">
              <div className="grid grid-cols-[1fr_150px_180px_120px] border-b border-border px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wide text-faint-foreground">
                <span>{t('common.document')}</span>
                <span>{t('common.status')}</span>
                <span>{t('members.membersColumn')}</span>
                <span className="text-right">{t('common.actions')}</span>
              </div>
              {documents.map((doc) => (
                <div
                  key={doc.id}
                  className="grid grid-cols-[1fr_150px_180px_120px] items-center gap-3 border-b border-border px-4 py-3 last:border-b-0"
                >
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium text-foreground">{doc.title}</div>
                    <div className="text-[11px] text-faint-foreground">{labels.docType(doc.type)}</div>
                  </div>
                  <StatusPill label={labels.docStatus(doc.status)} className={DOC_STATUS_CLASS[doc.status]} />
                  <div className="flex flex-wrap gap-1">
                    {doc.members.map((binding) => {
                      const member = MEMBERS.find((item) => item.id === binding.userId)
                      if (!member) return null
                      return (
                        <button
                          key={`${binding.userId}-${binding.role}`}
                          type="button"
                          onClick={() => removeDocumentMember(doc.id, binding.userId, binding.role)}
                          className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground hover:bg-white/5"
                          title={t('members.removeBinding')}
                        >
                          {member.initials}
                          <span className="text-faint-foreground">{labels.role(binding.role)}</span>
                        </button>
                      )
                    })}
                  </div>
                  <div className="flex justify-end">
                    <button
                      type="button"
                      onClick={() => openDoc(doc.id)}
                      className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-[11px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
                    >
                      <Link2 className="h-3 w-3" />
                      {t('members.openDoc')}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </section>
        </div>
      </main>
    </div>
  )
}
