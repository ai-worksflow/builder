import type {
  CandidateWorkspaceDto,
  SandboxSessionDto,
} from './sandbox-contract'

export function candidateAbandonEntryAllowed(
  session: Pick<SandboxSessionDto, 'allowedActions'> & {
    readonly candidate: Pick<SandboxSessionDto['candidate'], 'id'>
  },
  candidate: Pick<CandidateWorkspaceDto, 'id' | 'status' | 'dirty'>,
) {
  if (candidate.status !== 'active' || session.candidate.id !== candidate.id) return false
  if (session.allowedActions.includes('abandon')) return true
  // A dirty Candidate intentionally lacks `abandon` until its current exact
  // checkpoint is attached. The UI may enter the confirmation flow while the
  // server still allows `checkpoint`, then must re-check `abandon` afterwards.
  return candidate.dirty && session.allowedActions.includes('checkpoint')
}
