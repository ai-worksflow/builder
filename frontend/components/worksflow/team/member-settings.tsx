'use client'

import { useState, type FormEvent } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import type { ProjectRole } from '@/lib/collaboration/types'
import { useWorksflow } from '@/lib/worksflow/store'
import { cn } from '@/lib/utils'
import { Loader2, RefreshCw, ShieldCheck, UserPlus, Users2 } from 'lucide-react'

const ROLES: ProjectRole[] = ['admin', 'editor', 'commenter', 'viewer']

export function MemberSettings() {
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
        title="Sign in to manage project members"
        detail="Membership and roles are loaded from the platform backend, never from local team fixtures."
        action="Open sign in"
        onAction={() => setSurface('settings')}
      />
    )
  }

  if (!project) {
    return (
      <EmptyPanel
        title="Select or create a shared project"
        detail={error ?? 'A project is required before members and roles can be managed.'}
        action="Retry"
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
            <h1 className="text-lg font-semibold text-foreground">Project members</h1>
            <p className="mt-1 text-sm text-muted-foreground">{project.name} · {members.length} members · your role is {project.role}</p>
          </span>
          <span className={cn('rounded-full border px-2 py-1 text-[10px]', backendStatus === 'online' ? 'border-success/30 text-success' : 'border-destructive/30 text-destructive')}>{backendStatus === 'online' ? 'Platform online' : 'Platform unavailable'}</span>
          <button type="button" onClick={() => void refresh()} disabled={loading} className="rounded-md border border-border p-2 text-muted-foreground disabled:opacity-50" aria-label="Refresh members"><RefreshCw className={cn('size-4', loading && 'animate-spin')} /></button>
        </header>

        {error && <p role="alert" className="mt-4 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-[11px] text-destructive">{error}</p>}

        {can('admin') && (
          <form onSubmit={invite} className="mt-5 grid gap-3 rounded-lg border border-border bg-panel p-4 md:grid-cols-[1fr_1fr_150px_auto]">
            <label className="text-[11px] text-muted-foreground">Display name<input value={name} onChange={(event) => setName(event.target.value)} className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground" /></label>
            <label className="text-[11px] text-muted-foreground">Email<input value={email} onChange={(event) => setEmail(event.target.value)} type="email" className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground" /></label>
            <label className="text-[11px] text-muted-foreground">Project role<select value={role} onChange={(event) => setRole(event.target.value as ProjectRole)} className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground">{ROLES.map((item) => <option key={item}>{item}</option>)}</select></label>
            <button type="submit" disabled={loading || !name.trim() || !validEmail(email)} className="mt-auto inline-flex h-9 items-center justify-center gap-1.5 rounded-md bg-primary px-3 text-[11px] font-semibold text-primary-foreground disabled:opacity-50">{loading ? <Loader2 className="size-3.5 animate-spin" /> : <UserPlus className="size-3.5" />}Invite</button>
          </form>
        )}

        <div className="mt-5 overflow-hidden rounded-lg border border-border bg-panel">
          {members.map((member) => {
            const memberPresence = presence.find((item) => item.user.id === member.user.id)
            return (
              <div key={member.user.id} className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
                <span className="flex size-8 items-center justify-center rounded-full bg-primary/15 text-[10px] font-semibold text-primary-bright">{initials(member.user.name)}</span>
                <span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium text-foreground">{member.user.name}</span><span className="block truncate text-[11px] text-faint-foreground">{member.user.email}</span></span>
                <span className={cn('size-2 rounded-full', memberPresence?.status === 'active' ? 'bg-success' : memberPresence?.status === 'idle' ? 'bg-warning' : 'bg-faint-foreground')} title={memberPresence?.status ?? 'offline'} />
                {can('admin') && member.role !== 'owner' ? (
                  <select value={member.role} onChange={(event) => void updateMemberRole(member.user.id, event.target.value as ProjectRole)} className="h-8 rounded-md border border-border bg-background px-2 text-[11px] text-foreground">{ROLES.map((item) => <option key={item}>{item}</option>)}</select>
                ) : <span className="rounded-md bg-primary/10 px-2 py-1 text-[10px] text-primary-bright">{member.role}</span>}
                {can('admin') && member.role !== 'owner' && <button type="button" onClick={() => window.confirm(`Remove ${member.user.name} from ${project.name}?`) && void removeMember(member.user.id)} className="text-[10px] text-destructive">Remove</button>}
              </div>
            )
          })}
        </div>

        {!can('admin') && <div className="mt-4 flex items-center gap-2 rounded-md border border-border bg-panel p-3 text-[11px] text-muted-foreground"><ShieldCheck className="size-4 text-primary-bright" />Only owners and admins can change project membership.</div>}
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
