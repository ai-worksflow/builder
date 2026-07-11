import type {
  CreateInputManifestDto,
  ExactArtifactRefDto,
} from './flow-contract'

export type ProjectBriefEntryAction =
  | 'use_existing_revision'
  | 'checkpoint_draft'
  | 'blocked_unapproved_changes'
  | 'missing_revision'

export function projectBriefEntryAction(input: {
  readonly requireApproved: boolean
  readonly approvedRevision?: { readonly id: string; readonly contentHash: string }
  readonly latestRevision?: { readonly id: string; readonly contentHash: string }
  readonly draft?: { readonly contentHash: string }
}): ProjectBriefEntryAction {
  if (input.requireApproved) {
    if (!input.approvedRevision) return 'missing_revision'
    const draftIsNewer = Boolean(
      input.draft && input.draft.contentHash !== input.approvedRevision.contentHash,
    )
    const latestIsNewer = Boolean(
      input.latestRevision
      && (
        input.latestRevision.id !== input.approvedRevision.id
        || input.latestRevision.contentHash !== input.approvedRevision.contentHash
      ),
    )
    return draftIsNewer || latestIsNewer
      ? 'blocked_unapproved_changes'
      : 'use_existing_revision'
  }

  const latest = input.latestRevision ?? input.approvedRevision
  if (input.draft && (!latest || input.draft.contentHash !== latest.contentHash)) {
    return 'checkpoint_draft'
  }
  return latest ? 'use_existing_revision' : 'missing_revision'
}

export function projectBriefWorkflowManifestInput(
  source: ExactArtifactRefDto,
): CreateInputManifestDto {
  const exactSource = {
    artifactId: source.artifactId,
    revisionId: source.revisionId,
    contentHash: source.contentHash,
    ...(source.anchorId ? { anchorId: source.anchorId } : {}),
  }
  return {
    jobType: 'workflow_start',
    baseRevision: exactSource,
    sources: [{ ref: exactSource, purpose: 'project_brief' }],
    constraints: {
      entryArtifactId: exactSource.artifactId,
      entryRevisionId: exactSource.revisionId,
      entryContentHash: exactSource.contentHash,
    },
    outputSchemaVersion: 'workflow-input/v1',
  }
}
