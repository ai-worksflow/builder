import assert from 'node:assert/strict'
import {
  selectLatestPassingQualityRun,
  selectReleaseBuildManifestId,
} from '../lib/delivery/release-provenance'
import type { WorkbenchQueueItem } from '../lib/platform/flow-queue'
import type {
  ImplementationProposalDto,
  WorkbenchBundleDto,
} from '../lib/platform/flow-contract'
import type { QualityRunResult, QualityVersionRef } from '../lib/quality/types'

const workspace: QualityVersionRef = {
  artifactId: 'workspace-artifact',
  revisionId: 'workspace-r2',
  contentHash: `sha256:${'a'.repeat(64)}`,
}

function qualityRun(
  runId: string,
  options: {
    readonly passed?: boolean
    readonly revision?: QualityVersionRef
    readonly buildArtifact?: boolean
  } = {},
) {
  const passed = options.passed ?? true
  return {
    status: passed ? 'passed' : 'failed',
    passed,
    score: { earned: passed ? 100 : 0, possible: 100, percentage: passed ? 100 : 0 },
    metadata: {
      runId,
      projectId: 'project',
      runnerVersion: 'test',
      executionMode: 'sandbox',
      sandboxKind: 'test',
      startedAt: '2026-07-10T00:00:00.000Z',
      completedAt: '2026-07-10T00:00:01.000Z',
      workspaceRevision: options.revision ?? workspace,
      createdBy: 'actor',
      version: 1,
      etag: '"quality:1"',
    },
    checks: [],
    diagnostics: [],
    durationMs: 1,
    ...(options.buildArtifact === false ? {} : {
      buildArtifact: {
        id: `build-${runId}`,
        contentRef: `content-${runId}`,
        contentHash: `sha256:${'b'.repeat(64)}`,
        buildHash: `sha256:${'c'.repeat(64)}`,
        entryPath: 'index.html',
        fileCount: 1,
        totalBytes: 10,
      },
    }),
  } satisfies QualityRunResult
}

function proposal(
  id: string,
  manifestId: string,
  status: ImplementationProposalDto['status'],
) {
  return { id, buildManifestId: manifestId, status } as ImplementationProposalDto
}

function bundle(id: string) {
  return { id } as WorkbenchBundleDto
}

function queueItem(
  bundleId: string,
  implementationProposal: ImplementationProposalDto | null,
  activeBundleId = bundleId,
): WorkbenchQueueItem {
  return {
    bundleId,
    bundle: bundle(activeBundleId),
    proposal: implementationProposal,
  }
}

const wrongRevision = { ...workspace, contentHash: `sha256:${'d'.repeat(64)}` }
const selectedQuality = selectLatestPassingQualityRun([
  qualityRun('failed-newest', { passed: false }),
  qualityRun('wrong-revision', { revision: wrongRevision }),
  qualityRun('no-build', { buildArtifact: false }),
  qualityRun('selected'),
  qualityRun('older'),
], workspace)
assert.equal(selectedQuality?.metadata.runId, 'selected')
assert.equal(selectLatestPassingQualityRun([qualityRun('wrong', { revision: wrongRevision })], workspace), undefined)

const selectedManifest = selectReleaseBuildManifestId([
  queueItem('manifest-1', proposal('proposal-1', 'manifest-1', 'applied')),
  queueItem('manifest-forged', proposal('proposal-forged', 'different-manifest', 'applied')),
  queueItem('manifest-2', proposal('proposal-2', 'manifest-2', 'partially_applied')),
  queueItem('manifest-3', proposal('proposal-3', 'manifest-3', 'ready')),
], null, null)
assert.equal(selectedManifest, 'manifest-2')
assert.equal(
  selectReleaseBuildManifestId([], bundle('active-manifest'), proposal('active', 'active-manifest', 'applied')),
  'active-manifest',
)
assert.equal(
  selectReleaseBuildManifestId([], bundle('forged'), proposal('active', 'different', 'applied')),
  null,
)
assert.equal(
  selectReleaseBuildManifestId([
    queueItem('root-page-1', proposal('proposal-page-1', 'root-page-1', 'applied')),
    queueItem(
      'root-page-2',
      proposal('proposal-page-2-w1', 'derived-page-2-w1', 'applied'),
      'derived-page-2-w1',
    ),
  ], null, null),
  'derived-page-2-w1',
)

console.log('2 release provenance test groups passed.')
