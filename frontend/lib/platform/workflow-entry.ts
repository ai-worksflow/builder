import type {
  CreateInputManifestDto,
  ExactArtifactRefDto,
} from './flow-contract'

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
