export interface CandidateFileOpenSessionHead {
  readonly id: string
  readonly projectId: string
  readonly sessionEpoch: number
  readonly candidate: {
    readonly id: string
    readonly version: number
    readonly journalSequence: number
    readonly sessionEpoch: number
    readonly writerLeaseEpoch: number
    readonly treeHash: string
  }
}

export interface CandidateFileOpenCandidateHead {
  readonly id: string
  readonly projectId: string
  readonly version: number
  readonly journalSequence: number
  readonly sessionEpoch: number
  readonly writerLeaseEpoch: number
  readonly treeHash: string
  readonly currentTree: {
    readonly treeHash: string
    readonly files: readonly CandidateFileOpenTreeFile[]
  }
}

export interface CandidateFileOpenTreeFile {
  readonly path: string
  readonly mode: string
  readonly contentHash: string
  readonly byteSize: number
}

export interface CandidateFileOpenSandboxFences {
  readonly sessionEpoch: number
  readonly candidateVersion: number
  readonly writerLeaseEpoch: number
  readonly treeHash: string
}

export interface ExactCandidateFileOpenFence {
  readonly projectId: string
  readonly sessionId: string
  readonly sessionEpoch: number
  readonly candidateId: string
  readonly candidateVersion: number
  readonly journalSequence: number
  readonly writerLeaseEpoch: number
  readonly treeHash: string
  readonly path: string
  readonly contentHash: string
}

export interface CandidateFileReadEvidence {
  readonly sessionEpoch: number
  readonly candidateId: string
  readonly candidateVersion: number
  readonly journalSequence: number
  readonly writerLeaseEpoch: number
  readonly treeHash: string
  readonly contentHash: string
}

export type CandidateFileReadCommitDecision =
  | 'commit'
  | 'superseded'
  | 'head_changed'
  | 'response_mismatch'

function sameTreeFile(left: CandidateFileOpenTreeFile, right: CandidateFileOpenTreeFile) {
  return left.path === right.path
    && left.mode === right.mode
    && left.contentHash === right.contentHash
    && left.byteSize === right.byteSize
}

export function createExactCandidateFileOpenFence(input: {
  readonly projectId: string
  readonly session: CandidateFileOpenSessionHead | null
  readonly candidate: CandidateFileOpenCandidateHead | null
  readonly fences: CandidateFileOpenSandboxFences | null
  readonly path: string
  readonly observedFile?: CandidateFileOpenTreeFile
  readonly expectedContentHash?: string
}): ExactCandidateFileOpenFence | undefined {
  const { projectId, session, candidate, fences, path, observedFile, expectedContentHash } = input
  if (!projectId || !session || !candidate || !fences || !path) return undefined
  const treeFile = candidate.currentTree.files.find((file) => file.path === path)
  if (
    !treeFile
    || candidate.projectId !== projectId
    || session.projectId !== projectId
    || session.candidate.id !== candidate.id
    || session.candidate.version !== candidate.version
    || session.candidate.journalSequence !== candidate.journalSequence
    || session.candidate.sessionEpoch !== candidate.sessionEpoch
    || session.sessionEpoch !== candidate.sessionEpoch
    || session.candidate.writerLeaseEpoch !== candidate.writerLeaseEpoch
    || session.candidate.treeHash !== candidate.treeHash
    || candidate.currentTree.treeHash !== candidate.treeHash
    || fences.sessionEpoch !== session.sessionEpoch
    || fences.candidateVersion !== candidate.version
    || fences.writerLeaseEpoch !== candidate.writerLeaseEpoch
    || fences.treeHash !== candidate.treeHash
    || (observedFile !== undefined && !sameTreeFile(observedFile, treeFile))
    || (expectedContentHash !== undefined && expectedContentHash !== treeFile.contentHash)
  ) return undefined

  return {
    projectId,
    sessionId: session.id,
    sessionEpoch: session.sessionEpoch,
    candidateId: candidate.id,
    candidateVersion: candidate.version,
    journalSequence: candidate.journalSequence,
    writerLeaseEpoch: candidate.writerLeaseEpoch,
    treeHash: candidate.treeHash,
    path,
    contentHash: treeFile.contentHash,
  }
}

export function exactCandidateFileOpenFenceEquals(
  left: ExactCandidateFileOpenFence,
  right: ExactCandidateFileOpenFence,
) {
  return left.projectId === right.projectId
    && left.sessionId === right.sessionId
    && left.sessionEpoch === right.sessionEpoch
    && left.candidateId === right.candidateId
    && left.candidateVersion === right.candidateVersion
    && left.journalSequence === right.journalSequence
    && left.writerLeaseEpoch === right.writerLeaseEpoch
    && left.treeHash === right.treeHash
    && left.path === right.path
    && left.contentHash === right.contentHash
}

export function candidateFileReadCommitDecision(input: {
  readonly requestGeneration: number
  readonly currentGeneration: number
  readonly requestFence: ExactCandidateFileOpenFence
  readonly currentFence?: ExactCandidateFileOpenFence
  readonly evidence: CandidateFileReadEvidence
}): CandidateFileReadCommitDecision {
  if (input.requestGeneration !== input.currentGeneration) return 'superseded'
  if (!input.currentFence || !exactCandidateFileOpenFenceEquals(input.requestFence, input.currentFence)) {
    return 'head_changed'
  }
  const { requestFence, evidence } = input
  if (
    evidence.sessionEpoch !== requestFence.sessionEpoch
    || evidence.candidateId !== requestFence.candidateId
    || evidence.candidateVersion !== requestFence.candidateVersion
    || evidence.journalSequence !== requestFence.journalSequence
    || evidence.writerLeaseEpoch !== requestFence.writerLeaseEpoch
    || evidence.treeHash !== requestFence.treeHash
    || evidence.contentHash !== requestFence.contentHash
  ) return 'response_mismatch'
  return 'commit'
}

export type OpenFileHeadRefreshDisposition = 'rebind' | 'preserve_stale' | 'clear'

export function openFileHeadRefreshDisposition(input: {
  readonly path: string
  readonly contentHash: string | 'absent'
  readonly dirty: boolean
  readonly nextFiles: readonly CandidateFileOpenTreeFile[]
}): OpenFileHeadRefreshDisposition {
  const next = input.nextFiles.find((file) => file.path === input.path)
  if (input.contentHash !== 'absent' && next?.contentHash === input.contentHash) return 'rebind'
  return input.dirty ? 'preserve_stale' : 'clear'
}
