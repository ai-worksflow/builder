import assert from 'node:assert/strict'
import {
  createEmptyPageSpecContent,
  createEmptyPrototypeContent,
  reviewGateReadyForRequest,
} from '../lib/platform/artifact-workspace'
import type {
  ArtifactReviewGateDto,
  PrototypeContentDto,
  PrototypeStateDto,
} from '../lib/platform/dto'
import {
  PrototypeContentMutationError,
  addPrototypeBreakpoint,
  addPrototypeState,
  prototypeFrameCoverageGaps,
  prototypeReviewIssues,
  removePrototypeBreakpoint,
  removePrototypeState,
  repairPrototypeFrameCoverage,
  updatePrototypeBreakpoint,
  updatePrototypeState,
} from '../lib/platform/prototype-content'

type TestCase = {
  readonly name: string
  readonly run: () => void | Promise<void>
}

const tests: TestCase[] = []

function test(name: string, run: TestCase['run']) {
  tests.push({ name, run })
}

function canonicalPrototype(): PrototypeContentDto {
  return createEmptyPrototypeContent(
    {
      artifactId: 'page-spec-orders',
      revisionId: 'page-spec-revision-1',
      revisionNumber: 1,
      contentHash: 'sha256:page-spec-orders',
    },
    createEmptyPageSpecContent(
      'page-orders',
      'Orders',
      '/orders',
      'Review customer orders.',
    ),
  )
}

function idFactory() {
  let sequence = 0
  return (prefix: 'frame' | 'layer') => `${prefix}-test-${++sequence}`
}

test('prototype review gate accepts the canonical PageSpec-derived responsive scene', () => {
  const content = canonicalPrototype()
  assert.deepEqual(prototypeReviewIssues(content), [])
  assert.equal(content.states.length * content.breakpoints.length, content.frames.length)
})

test('first revision and valid dirty drafts use client structure before the post-revision server gate', () => {
  const firstDraft = canonicalPrototype()
  const dirtyDraft: PrototypeContentDto = {
    ...firstDraft,
    frames: firstDraft.frames.map((frame, index) => index === 0
      ? { ...frame, title: `${frame.title} updated` }
      : frame),
  }
  const preRevisionGate: ArtifactReviewGateDto = {
    passed: false,
    checks: [
      { code: 'latest_revision_present', severity: 'error', message: 'Create an immutable artifact revision before review.' },
      { code: 'artifact_content_available', severity: 'error', message: 'The latest revision content is unavailable.' },
      { code: 'required_trace_coverage', severity: 'error', message: 'No exact revision exists for trace coverage.' },
      { code: 'canonical_review_approved', severity: 'error', message: 'No canonical approval exists.' },
    ],
    unresolvedBlockingCommentIds: [],
    traceCoverage: 0,
  }
  const postRevisionGate: ArtifactReviewGateDto = {
    passed: false,
    checks: [
      { code: 'latest_revision_present', severity: 'info', message: 'Latest immutable revision is available.' },
      { code: 'draft_matches_latest_revision', severity: 'info', message: 'The working draft matches the latest revision.' },
      { code: 'artifact_content_valid', severity: 'info', message: 'Artifact content is valid.' },
      { code: 'blocking_comments_resolved', severity: 'info', message: 'No unresolved blockers.' },
      { code: 'artifact_sync_current', severity: 'info', message: 'Dependency health is current.' },
      { code: 'required_trace_coverage', severity: 'info', message: 'Required trace coverage is complete.' },
      { code: 'canonical_review_approved', severity: 'error', message: 'Canonical approval is pending.' },
    ],
    unresolvedBlockingCommentIds: [],
    traceCoverage: 1,
  }
  const dirtyPreRevisionGate: ArtifactReviewGateDto = {
    ...postRevisionGate,
    checks: postRevisionGate.checks.map((check) => check.code === 'draft_matches_latest_revision'
      ? { ...check, severity: 'error', message: 'The working draft has unrevisioned changes.' }
      : check),
  }

  assert.deepEqual(prototypeReviewIssues(firstDraft), [])
  assert.deepEqual(prototypeReviewIssues(dirtyDraft), [])
  assert.equal(reviewGateReadyForRequest(preRevisionGate), false)
  assert.equal(reviewGateReadyForRequest(dirtyPreRevisionGate), false)
  assert.equal(reviewGateReadyForRequest(postRevisionGate), true)
})

test('prototype review gate mirrors backend structural, fixture, and interaction blockers', () => {
  const base = canonicalPrototype()
  const rootId = Object.keys(base.layers)[0]
  const ready = base.states[0]
  const invalid = {
    ...base,
    pageSpecRevision: { artifactId: '', revisionId: '', contentHash: '' },
    states: [ready, { ...ready, title: '' }],
    breakpoints: [
      { ...base.breakpoints[0], name: 'Desktop' },
      { ...base.breakpoints[1], name: 'desktop' },
    ],
    layers: {
      [rootId]: {
        ...base.layers[rootId],
        parentId: 'missing-parent',
        childIds: [rootId],
      },
    },
    frames: [
      { ...base.frames[0], rootLayerId: 'missing-root' },
      { ...base.frames[0], id: 'duplicate-pair' },
    ],
    fixtures: [{
      id: 'fixture-unsafe',
      name: 'Unsafe fixture',
      stateId: 'missing-state',
      response: {},
      statusCode: 200,
      latencyMs: 1,
      sanitized: false,
      contentHash: 'sha256:fixture',
    }],
    interactions: [{
      id: '',
      sourceLayerId: 'missing-layer',
      trigger: 'execute',
      guards: [],
      actions: [{ type: 'javascript', source: 'alert(1)' }],
    }],
  } as unknown as PrototypeContentDto
  const issues = prototypeReviewIssues(invalid)

  for (const expected of [
    'exact PageSpec',
    'duplicates an existing state',
    'Tablet',
    'Mobile',
    'parent',
    'child',
    'existing state, breakpoint, and root layer',
    'duplicates a state and breakpoint pair',
    'marked sanitized',
    'existing state',
    'declarative trigger',
    'declarative action whitelist',
  ]) {
    assert.ok(issues.some((issue) => issue.includes(expected)), `missing issue containing ${expected}: ${issues.join(' | ')}`)
  }
})

test('state mutations atomically maintain frames, overrides, fixtures, and interaction references', () => {
  const createId = idFactory()
  const base = canonicalPrototype()
  const addedState: PrototypeStateDto = {
    id: 'state-filtered',
    key: 'filtered',
    title: 'Filtered',
    required: true,
    fixtureIds: [],
    pageStateId: 'page-state-filtered',
  }
  let content: PrototypeContentDto = addPrototypeState(base, addedState, createId)
  assert.equal(content.frames.filter((frame) => frame.stateId === addedState.id).length, content.breakpoints.length)
  assert.deepEqual(prototypeFrameCoverageGaps(content), [])

  content = updatePrototypeState(content, addedState.id, { key: 'filtered-results', title: 'Filtered results' })
  assert.equal(content.states.find((state) => state.id === addedState.id)?.key, 'filtered-results')
  assert.ok(content.frames.filter((frame) => frame.stateId === addedState.id).every((frame) => frame.title.startsWith('Filtered results')))

  const rootLayerId = Object.keys(content.layers)[0]
  const fixtureId = 'fixture-filtered'
  content = {
    ...content,
    states: content.states.map((state) => state.id === addedState.id
      ? { ...state, fixtureIds: [fixtureId] }
      : state),
    fixtures: [...content.fixtures, {
      id: fixtureId,
      name: 'Filtered fixture',
      stateId: addedState.id,
      response: {},
      statusCode: 200,
      latencyMs: 1,
      sanitized: true,
      contentHash: 'sha256:fixture-filtered',
    }],
    overrides: [...content.overrides, {
      id: 'override-filtered',
      stateId: addedState.id,
      layerId: rootLayerId,
      propertyPath: 'style.fill',
      value: '#ffffff',
    }],
    interactions: [...content.interactions, {
      id: 'interaction-filtered',
      sourceLayerId: rootLayerId,
      trigger: 'click',
      guards: [],
      actions: [
        { type: 'setState', stateId: addedState.id },
        { type: 'submitFixture', fixtureId },
      ],
    }],
  }
  content = removePrototypeState(content, addedState.id)
  assert.equal(content.frames.some((frame) => frame.stateId === addedState.id), false)
  assert.equal(content.overrides.some((override) => override.stateId === addedState.id), false)
  assert.equal(content.fixtures.some((fixture) => fixture.stateId === addedState.id), false)
  assert.equal(content.interactions.some((interaction) => interaction.actions.some((action) =>
    action.type === 'setState' && action.stateId === addedState.id,
  )), false)

  const oneState = { ...content, states: [content.states[0]], frames: content.frames.filter((frame) => frame.stateId === content.states[0].id) }
  assert.throws(() => removePrototypeState(oneState, oneState.states[0].id), PrototypeContentMutationError)
})

test('breakpoint mutations preserve required breakpoints and maintain complete frame coverage', () => {
  const createId = idFactory()
  let content: PrototypeContentDto = canonicalPrototype()
  content = addPrototypeBreakpoint(content, {
    id: 'breakpoint-wide',
    name: 'Wide',
    minWidth: 1600,
    viewportWidth: 1920,
    viewportHeight: 1080,
  }, createId)
  assert.equal(content.frames.filter((frame) => frame.breakpointId === 'breakpoint-wide').length, content.states.length)

  content = updatePrototypeBreakpoint(content, 'breakpoint-wide', {
    name: 'Presentation',
    minWidth: 1800,
    viewportWidth: 2048,
    viewportHeight: 1152,
  })
  assert.equal(content.breakpoints.find((breakpoint) => breakpoint.id === 'breakpoint-wide')?.name, 'Presentation')
  assert.ok(content.frames.filter((frame) => frame.breakpointId === 'breakpoint-wide').every((frame) => frame.title.endsWith('Presentation')))

  const withoutFrame = {
    ...content,
    frames: content.frames.filter((frame) => !(frame.stateId === content.states[0].id && frame.breakpointId === 'breakpoint-wide')),
  }
  assert.equal(prototypeFrameCoverageGaps(withoutFrame).length, 1)
  content = repairPrototypeFrameCoverage(withoutFrame, createId)
  assert.deepEqual(prototypeFrameCoverageGaps(content), [])

  content = removePrototypeBreakpoint(content, 'breakpoint-wide')
  assert.equal(content.frames.some((frame) => frame.breakpointId === 'breakpoint-wide'), false)
  assert.throws(() => removePrototypeBreakpoint(content, 'desktop'), PrototypeContentMutationError)
})

async function main() {
  let failed = 0
  for (const { name, run } of tests) {
    try {
      await run()
      console.log(`✓ ${name}`)
    } catch (error) {
      failed += 1
      console.error(`✗ ${name}`)
      console.error(error)
    }
  }
  if (failed > 0) process.exitCode = 1
}

void main()
