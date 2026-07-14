import type { ProjectGovernanceMode } from '@/lib/platform/dto'
import type { WorkflowRunStatus } from '@/lib/platform/flow-contract'
import type { ProjectMember } from '@/lib/collaboration/types'

const TERMINAL_RUN_STATUSES: ReadonlySet<WorkflowRunStatus> = new Set([
  'completed',
  'cancelled',
  'stale',
])

export function hasActiveGovernanceRun(statuses: readonly WorkflowRunStatus[]) {
  return statuses.some((status) => !TERMINAL_RUN_STATUSES.has(status))
}

export function requiresSoloReviewConfirmation(
  governanceMode: ProjectGovernanceMode,
  resolution: 'approve' | 'changes_requested' | 'waive',
) {
  return governanceMode === 'solo' && resolution === 'approve'
}

export function allowsSoloSelfApprovalRequest(
  governanceMode: ProjectGovernanceMode,
  members: readonly ProjectMember[],
  reviewerIds: readonly string[],
) {
  if (governanceMode !== 'solo') return false
  const owners = members.filter((member) => member.role === 'owner')
  return owners.length === 1 && reviewerIds.includes(owners[0].user.id)
}

export function reviewCandidatesForGovernance(
  members: readonly ProjectMember[],
  currentUserId: string | null | undefined,
  governanceMode: ProjectGovernanceMode,
) {
  if (governanceMode === 'solo') {
    const owners = members.filter((member) => member.role === 'owner')
    return owners.length === 1 ? owners : []
  }
  const eligible = members.filter((member) =>
    ['owner', 'admin', 'editor'].includes(member.role),
  )
  return eligible.filter((member) => member.user.id !== currentUserId)
}

export function canSoloSelfReview(
  governanceMode: ProjectGovernanceMode,
  members: readonly ProjectMember[],
  currentUserId: string | null | undefined,
  authorId: string,
) {
  if (governanceMode !== 'solo' || !currentUserId || authorId !== currentUserId) return false
  const owners = members.filter((member) => member.role === 'owner')
  return owners.length === 1 && owners[0].user.id === currentUserId
}
