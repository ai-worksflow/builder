import assert from 'node:assert/strict'
import { projectBriefWorkflowManifestInput } from '../lib/platform/workflow-entry'

const source = {
  artifactId: 'project-brief-1',
  revisionId: 'project-brief-r1',
  revisionNumber: 1,
  contentHash: 'sha256:project-brief-r1',
}

const input = projectBriefWorkflowManifestInput(source)

assert.deepEqual(input.baseRevision, {
  artifactId: source.artifactId,
  revisionId: source.revisionId,
  contentHash: source.contentHash,
})
assert.deepEqual(input.sources, [{
  ref: input.baseRevision,
  purpose: 'project_brief',
}])
assert.deepEqual(input.constraints, {
  entryArtifactId: source.artifactId,
  entryRevisionId: source.revisionId,
  entryContentHash: source.contentHash,
})
assert.equal(input.outputSchemaVersion, 'workflow-input/v1')
assert.equal(input.jobType, 'workflow_start')

console.log('✓ Project Brief workflow entry pins one exact revision as both base and input source')
