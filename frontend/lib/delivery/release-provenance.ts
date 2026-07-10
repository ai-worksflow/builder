import type { WorkbenchQueueItem } from '../platform/flow-queue'
import { proposalIsApplied } from '../platform/flow-queue'
import type {
  ImplementationProposalDto,
  WorkbenchBundleDto,
} from '../platform/flow-contract'
import type { QualityRunResult, QualityVersionRef } from '../quality/types'

export function selectLatestPassingQualityRun(
  qualityRuns: readonly QualityRunResult[],
  workspaceRevision: QualityVersionRef | null,
) {
  if (!workspaceRevision) return undefined
  return qualityRuns.find((candidate) => (
    candidate.passed &&
    Boolean(candidate.buildArtifact) &&
    candidate.metadata.workspaceRevision.artifactId === workspaceRevision.artifactId &&
    candidate.metadata.workspaceRevision.revisionId === workspaceRevision.revisionId &&
    candidate.metadata.workspaceRevision.contentHash === workspaceRevision.contentHash
  ))
}

export function selectReleaseBuildManifestId(
  queue: readonly WorkbenchQueueItem[],
  activeBundle: WorkbenchBundleDto | null,
  activeProposal: ImplementationProposalDto | null,
) {
  for (let index = queue.length - 1; index >= 0; index -= 1) {
    const item = queue[index]
    if (proposalIsApplied(item.proposal) && item.proposal?.buildManifestId === item.bundleId) {
      return item.bundleId
    }
  }
  if (
    activeBundle &&
    proposalIsApplied(activeProposal) &&
    activeBundle.id === activeProposal?.buildManifestId
  ) {
    return activeBundle.id
  }
  return null
}
