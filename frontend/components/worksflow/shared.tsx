'use client'

import { cn } from '@/lib/utils'
import { MEMBERS } from '@/lib/worksflow/mock-data'
import type { Member } from '@/lib/worksflow/types'

export function Avatar({
  member,
  size = 24,
  className,
  ring,
}: {
  member: Member
  size?: number
  className?: string
  ring?: boolean
}) {
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-full font-medium text-white',
        ring && 'ring-2 ring-background',
        className,
      )}
      style={{
        width: size,
        height: size,
        backgroundColor: member.color,
        fontSize: Math.round(size * 0.42),
      }}
      title={`${member.name} · ${member.title}`}
      aria-label={`${member.name}, ${member.title}`}
    >
      {member.initials}
    </span>
  )
}

export function AvatarStack({ ids, size = 22 }: { ids: string[]; size?: number }) {
  const members = ids
    .map((id) => MEMBERS.find((m) => m.id === id))
    .filter(Boolean) as Member[]
  return (
    <div className="flex items-center">
      {members.map((m, i) => (
        <div key={m.id} style={{ marginLeft: i === 0 ? 0 : -8 }}>
          <Avatar member={m} size={size} ring />
        </div>
      ))}
    </div>
  )
}

export function memberById(id: string) {
  return MEMBERS.find((m) => m.id === id)
}

export function StatusPill({
  label,
  className,
}: {
  label: string
  className?: string
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium leading-none',
        className,
      )}
    >
      {label}
    </span>
  )
}

export function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-[11px] font-semibold uppercase tracking-wider text-faint-foreground">
      {children}
    </div>
  )
}
