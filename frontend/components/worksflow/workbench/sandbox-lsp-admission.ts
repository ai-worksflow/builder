import { candidateDocumentURI, type SandboxHeadFenceDto } from '@/lib/platform/lsp-contract'
import type {
  CandidateWorkspaceDto,
  ExactRepositoryRefDto,
  SandboxFences,
  SandboxSessionDto,
} from '@/lib/platform/sandbox-contract'

export interface SandboxLSPDocumentAdmissionInput {
  readonly path: string
  readonly contentHash: string | 'absent'
  readonly binary: boolean
  readonly stale: boolean
}

export interface SandboxLSPAdmissionInput {
  readonly projectId: string
  readonly canEdit: boolean
  readonly session: SandboxSessionDto | null
  readonly candidate: CandidateWorkspaceDto | null
  readonly fences: SandboxFences | null
  readonly selectedServiceId: string
  readonly document: SandboxLSPDocumentAdmissionInput | null
  readonly now?: number
}

export interface SandboxLSPAdmission {
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly templateRelease: ExactRepositoryRefDto
  readonly modelUri: string
  readonly path: string
  readonly contentHash: string
  readonly serviceId: string
}

export type SandboxLSPAdmissionDecision =
  | { readonly eligible: true; readonly admission: SandboxLSPAdmission }
  | { readonly eligible: false; readonly reason: string }

function sameRef(left: ExactRepositoryRefDto, right: ExactRepositoryRefDto) {
  return left.id === right.id && left.contentHash === right.contentHash
}

function blocked(reason: string): SandboxLSPAdmissionDecision {
  return { eligible: false, reason }
}

/** Fail-closed UI admission; the server independently repeats every check. */
export function resolveSandboxLSPAdmission(
  input: SandboxLSPAdmissionInput,
): SandboxLSPAdmissionDecision {
  const { session, candidate, fences, document } = input
  if (!input.canEdit) return blocked('LSP requires explicit Candidate edit permission.')
  if (!session || !candidate || !fences) return blocked('Open the exact Sandbox Candidate first.')
  if (session.state !== 'ready' || !session.allowedActions.includes('edit') ||
    candidate.status !== 'active' || candidate.conflicted || candidate.stale ||
    candidate.rebaseRequired) {
    return blocked('LSP is available only for an active, editable, conflict-free Candidate.')
  }
  if (session.projectId !== input.projectId || candidate.projectId !== input.projectId ||
    session.candidate.id !== candidate.id || session.candidate.version !== candidate.version ||
    session.candidate.journalSequence !== candidate.journalSequence ||
    session.candidate.sessionEpoch !== candidate.sessionEpoch ||
    session.candidate.writerLeaseEpoch !== candidate.writerLeaseEpoch ||
    session.candidate.treeHash !== candidate.treeHash ||
    session.sessionEpoch !== candidate.sessionEpoch || fences.sessionEpoch !== session.sessionEpoch ||
    fences.candidateVersion !== candidate.version ||
    fences.writerLeaseEpoch !== candidate.writerLeaseEpoch || fences.treeHash !== candidate.treeHash ||
    candidate.currentTree.treeHash !== candidate.treeHash) {
    return blocked('Refresh the exact Sandbox/Candidate head before enabling LSP.')
  }
  const now = input.now ?? Date.now()
  if (!candidate.lease || candidate.lease.ownerId !== session.actorId ||
    candidate.lease.epoch !== candidate.writerLeaseEpoch ||
    !Number.isFinite(Date.parse(candidate.lease.expiresAt)) || Date.parse(candidate.lease.expiresAt) <= now) {
    return blocked('Acquire the exact active writer lease before enabling LSP.')
  }
  if (!document || document.binary || document.stale || document.contentHash === 'absent' ||
    !/^sha256:[0-9a-f]{64}$/u.test(document.contentHash)) {
    return blocked('Select a saved, non-binary, non-stale Candidate file.')
  }
  const treeFile = candidate.currentTree.files.find((entry) => entry.path === document.path)
  if (!treeFile || treeFile.contentHash !== document.contentHash) {
    return blocked('The open file is not bound to the exact Candidate tree hash.')
  }
  const service = session.allowedServices.find((entry) => entry.id === input.selectedServiceId)
  if (!service || !session.templateReleases.some((release) => sameRef(release, service.templateRelease))) {
    return blocked('Select the exact TemplateRelease service that declares the LSP profile.')
  }
  return {
    eligible: true,
    admission: {
      sandboxHeadFence: {
        projectId: input.projectId,
        sessionId: session.id,
        sessionEpoch: session.sessionEpoch,
        candidateId: candidate.id,
        version: candidate.version,
        journalSequence: candidate.journalSequence,
        writerLeaseEpoch: candidate.writerLeaseEpoch,
        treeHash: candidate.treeHash,
      },
      templateRelease: service.templateRelease,
      modelUri: candidateDocumentURI(input.projectId, candidate.id, document.path),
      path: document.path,
      contentHash: document.contentHash,
      serviceId: service.id,
    },
  }
}
