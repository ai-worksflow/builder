'use client'

import { useEffect, useState, type FormEvent } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import type { ProjectRole } from '@/lib/collaboration/types'
import { useWorksflow } from '@/lib/worksflow/store'
import { cn } from '@/lib/utils'
import { Loader2, RefreshCw, ShieldCheck, UserPlus, Users2 } from 'lucide-react'
import { useI18n } from '@/lib/i18n'

const PROJECT_ROLES: ProjectRole[] = ['owner', 'admin', 'editor', 'commenter', 'viewer']

export function MemberSettings() {
  const { locale, t } = useI18n()
  const {
    loading,
    backendStatus,
    session,
    project,
    members,
    presence,
    error,
    can,
    refresh,
    addMember,
    updateMemberRole,
    removeMember,
  } = useCollaboration()
  const { setSurface } = useWorksflow()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<ProjectRole>('viewer')
  const canAssignOwner = project?.role === 'owner' && project.governanceMode === 'team'
  const assignableRoles = canAssignOwner
    ? PROJECT_ROLES
    : PROJECT_ROLES.filter((item) => item !== 'owner')

  useEffect(() => {
    if (!canAssignOwner && role === 'owner') setRole('viewer')
  }, [canAssignOwner, role])

  async function invite(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!name.trim() || !validEmail(email)) return
    if (await addMember({ name: name.trim(), email: email.trim(), role })) {
      setName('')
      setEmail('')
    }
  }

  if (!session.signedIn) {
    return (
      <EmptyPanel
        title={t('teamPlatform.members.signInTitle')}
        detail={t('teamPlatform.members.signInDetail')}
        action={t('teamPlatform.common.openSignIn')}
        onAction={() => setSurface('settings')}
      />
    )
  }

  if (!project) {
    return (
      <EmptyPanel
        title={t('teamPlatform.members.selectProjectTitle')}
        detail={error ?? t('teamPlatform.members.projectRequired')}
        action={t('common.retry')}
        onAction={() => void refresh()}
      />
    )
  }

  return (
    <div className="h-full overflow-y-auto bg-canvas p-6 scrollbar-thin max-sm:p-4">
      <div className="mx-auto max-w-5xl">
        <header className="flex flex-wrap items-start gap-3">
          <span className="flex size-10 items-center justify-center rounded-lg bg-primary/15 text-primary-bright"><Users2 className="size-5" /></span>
          <span className="min-w-0 flex-1">
            <h1 className="text-lg font-semibold text-foreground">{t('teamPlatform.members.title')}</h1>
            <p className="mt-1 text-sm text-muted-foreground">{project.name} · {t('teamPlatform.members.summary', { count: members.length.toLocaleString(locale), role: projectRoleLabel(project.role, t) })}</p>
          </span>
          <span className={cn('rounded-full border px-2 py-1 text-[10px]', backendStatus === 'online' ? 'border-success/30 text-success' : 'border-destructive/30 text-destructive')}>{backendStatus === 'online' ? t('teamPlatform.members.platformOnline') : t('teamPlatform.members.platformUnavailable')}</span>
          <button type="button" onClick={() => void refresh()} disabled={loading} className="rounded-md border border-border p-2 text-muted-foreground disabled:opacity-50" aria-label={t('teamPlatform.members.refresh')} title={t('teamPlatform.members.refresh')}><RefreshCw className={cn('size-4', loading && 'animate-spin')} /></button>
        </header>

        {error && <p role="alert" className="mt-4 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-[11px] text-destructive">{error}</p>}

        {can('admin') && (
          <form onSubmit={invite} className="mt-5 grid gap-3 rounded-lg border border-border bg-panel p-4 md:grid-cols-[1fr_1fr_150px_auto]">
            <label className="text-[11px] text-muted-foreground">{t('teamPlatform.members.displayName')}<input value={name} onChange={(event) => setName(event.target.value)} placeholder={t('teamPlatform.members.displayNamePlaceholder')} className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground" /></label>
            <label className="text-[11px] text-muted-foreground">{t('teamPlatform.members.email')}<input value={email} onChange={(event) => setEmail(event.target.value)} type="email" placeholder={t('teamPlatform.members.emailPlaceholder')} className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground" /></label>
            <label className="text-[11px] text-muted-foreground">{t('teamPlatform.members.projectRole')}<select value={role} onChange={(event) => setRole(event.target.value as ProjectRole)} className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground">{assignableRoles.map((item) => <option key={item} value={item}>{projectRoleLabel(item, t)}</option>)}</select></label>
            <button type="submit" disabled={loading || !name.trim() || !validEmail(email)} className="mt-auto inline-flex h-9 items-center justify-center gap-1.5 rounded-md bg-primary px-3 text-[11px] font-semibold text-primary-foreground disabled:opacity-50">{loading ? <Loader2 className="size-3.5 animate-spin" /> : <UserPlus className="size-3.5" />}{t('teamPlatform.members.invite')}</button>
          </form>
        )}

        {project.governanceMode === 'solo' && project.role === 'owner' && (
          <p className="mt-3 rounded-md border border-warning/30 bg-warning/10 p-3 text-[11px] text-warning">
            {t('teamPlatform.members.soloOwnerRestriction')}
          </p>
        )}

        <div className="mt-5 overflow-hidden rounded-lg border border-border bg-panel">
          {members.map((member) => {
            const memberPresence = presence.find((item) => item.user.id === member.user.id)
            return (
              <div key={member.user.id} className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
                <span className="flex size-8 items-center justify-center rounded-full bg-primary/15 text-[10px] font-semibold text-primary-bright">{initials(member.user.name)}</span>
                <span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium text-foreground">{member.user.name}</span><span className="block truncate text-[11px] text-faint-foreground">{member.user.email}</span></span>
                <span className={cn('size-2 rounded-full', memberPresence?.status === 'active' ? 'bg-success' : memberPresence?.status === 'idle' ? 'bg-warning' : 'bg-faint-foreground')} title={presenceStatusLabel(memberPresence?.status ?? 'offline', t)} aria-label={presenceStatusLabel(memberPresence?.status ?? 'offline', t)} />
                {can('admin') && member.role !== 'owner' ? (
                  <select value={member.role} onChange={(event) => void updateMemberRole(member.user.id, event.target.value as ProjectRole)} aria-label={t('teamPlatform.members.roleFor', { name: member.user.name })} className="h-8 rounded-md border border-border bg-background px-2 text-[11px] text-foreground">{assignableRoles.map((item) => <option key={item} value={item}>{projectRoleLabel(item, t)}</option>)}</select>
                ) : <span className="rounded-md bg-primary/10 px-2 py-1 text-[10px] text-primary-bright">{projectRoleLabel(member.role, t)}</span>}
                {can('admin') && member.role !== 'owner' && <button type="button" onClick={() => window.confirm(t('teamPlatform.members.removeConfirm', { name: member.user.name, project: project.name })) && void removeMember(member.user.id)} className="text-[10px] text-destructive">{t('common.remove')}</button>}
              </div>
            )
          })}
        </div>

        {!can('admin') && <div className="mt-4 flex items-center gap-2 rounded-md border border-border bg-panel p-3 text-[11px] text-muted-foreground"><ShieldCheck className="size-4 text-primary-bright" />{t('teamPlatform.members.adminOnly')}</div>}
      </div>
    </div>
  )
}

function EmptyPanel({ title, detail, action, onAction }: { title: string; detail: string; action: string; onAction: () => void }) {
  return <div className="flex h-full items-center justify-center bg-canvas p-6 text-center"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6"><Users2 className="mx-auto size-8 text-primary-bright" /><h1 className="mt-3 text-base font-semibold text-foreground">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{detail}</p><button type="button" onClick={onAction} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground">{action}</button></div></div>
}

function validEmail(value: string) {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())
}

function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]?.toUpperCase()).join('') || 'U'
}

type Translate = ReturnType<typeof useI18n>['t']

function projectRoleLabel(role: string, t: Translate) {
  const labels: Record<string, string> = {
    owner: t('common.owner'),
    admin: t('team.role.admin'),
    editor: t('common.editor'),
    commenter: t('common.commenter'),
    viewer: t('common.viewer'),
  }
  return labels[role] ?? role
}

function presenceStatusLabel(status: string, t: Translate) {
  const labels: Record<string, string> = {
    active: t('teamPlatform.members.presence.active'),
    idle: t('teamPlatform.members.presence.idle'),
    offline: t('teamPlatform.members.presence.offline'),
  }
  return labels[status] ?? status
}
