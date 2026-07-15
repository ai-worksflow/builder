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
  normalizePrototypeContent,
  prototypeFrameCoverageGaps,
  prototypeLayerIdentityIssues,
  prototypePageSpecAuthority,
  prototypePayloadIntegrityIssues,
  prototypeReviewIssues,
  prototypeVisibleViewport,
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

test('prototype normalization keeps incomplete workflow targets render-safe without inventing semantic content', () => {
  const raw = {
    schemaVersion: 1,
    pageSpecRevision: {
      artifactId: 'page-spec-orders',
      revisionId: 'page-spec-revision-1',
      contentHash: 'sha256:page-spec-orders',
    },
    exploratory: false,
    states: [],
    breakpoints: [],
    layers: {
      'layer-input': {
        id: 'layer-input',
        childIds: [],
        kind: 'input',
        name: 'Answer',
      },
    },
    frames: [],
    interactions: [],
    fixtures: [],
    futureField: { retained: true },
  }

  const normalized = normalizePrototypeContent(raw as unknown as PrototypeContentDto)

  assert.deepEqual(normalized.overrides, [])
  assert.deepEqual(normalized.tokenBindings, [])
  assert.deepEqual(normalized.componentBindings, [])
  assert.deepEqual(normalized.assets, [])
  assert.deepEqual(normalized.traceLinks, [])
  assert.deepEqual(normalized.layers['layer-input'].layout, {})
  assert.deepEqual(normalized.layers['layer-input'].style, {})
  assert.deepEqual(normalized.layers['layer-input'].properties, {})
  assert.deepEqual(normalized.layers['layer-input'].requirementIds, [])
  assert.deepEqual(normalized.layers['layer-input'].acceptanceCriterionIds, [])
  assert.deepEqual(normalized.layers['layer-input'].fieldMetadata, {})
  assert.deepEqual((normalized as unknown as { futureField: unknown }).futureField, { retained: true })
  assert.equal('tokenBindings' in raw, false)

  const issues = prototypeReviewIssues(raw as unknown as PrototypeContentDto)
  assert.ok(issues.some((issue) => issue.includes('PageSpec state')))
  assert.ok(issues.some((issue) => issue.includes('Desktop, Tablet, and Mobile')))
  assert.ok(issues.some((issue) => issue.includes('frame for each required state')))
})

test('prototype normalization adapts legacy AI breakpoint, layer, and frame aliases for safe inspection', () => {
  const normalized = normalizePrototypeContent({
    pageSpecRevision: {
      artifactId: 'page-spec-orders',
      revisionId: 'page-spec-revision-1',
      contentHash: 'sha256:page-spec-orders',
    },
    states: [{ id: 'state-ready', key: 'ready', title: 'Ready', required: true, pageStateId: null }],
    breakpoints: [{ id: 'desktop', key: 'desktop', title: 'Desktop', width: 1440, height: 1024 }],
    layers: [{
      id: 'layer-root',
      parentId: null,
      dataBindingId: null,
      type: 'screen',
      name: 'Interview',
      childIds: ['layer-action'],
      props: { role: 'main' },
    }, {
      id: 'layer-action',
      parentId: 'layer-root',
      type: 'button',
      name: 'Continue',
      childIds: [],
      props: { label: 'Continue' },
    }],
    frames: [{
      id: 'frame-ready-desktop',
      stateId: 'state-ready',
      breakpointId: 'desktop',
      rootLayerId: 'layer-root',
    }],
  } as unknown as PrototypeContentDto)

  assert.deepEqual(normalized.states[0].fixtureIds, [])
  assert.equal('pageStateId' in normalized.states[0], false)
  assert.deepEqual(normalized.breakpoints[0], {
    id: 'desktop',
    key: 'desktop',
    title: 'Desktop',
    width: 1440,
    height: 1024,
    name: 'Desktop',
    minWidth: 1024,
    viewportWidth: 1440,
    viewportHeight: 1024,
  })
  assert.equal(normalized.layers['layer-root'].kind, 'frame')
  assert.equal('parentId' in normalized.layers['layer-root'], false)
  assert.equal('dataBindingId' in normalized.layers['layer-root'], false)
  assert.equal(normalized.layers['layer-root'].semanticRole, 'main')
  assert.equal(normalized.layers['layer-action'].kind, 'button')
  assert.equal(normalized.layers['layer-action'].properties.text, 'Continue')
  assert.equal(normalized.frames[0].title, 'Ready · Desktop')
})

test('prototype normalization supplies visible canonical and custom viewport defaults', () => {
  const normalized = normalizePrototypeContent({
    pageSpecRevision: {
      artifactId: 'page-spec-orders',
      revisionId: 'page-spec-revision-1',
      contentHash: 'sha256:page-spec-orders',
    },
    states: [],
    breakpoints: [
      { id: 'desktop', name: 'Desktop' },
      { id: 'tablet', key: 'tablet' },
      { id: 'mobile', title: 'Mobile' },
      { id: 'wide', name: 'Wide' },
    ],
    layers: {},
    frames: [],
  } as unknown as PrototypeContentDto)

  assert.deepEqual(
    normalized.breakpoints.map((breakpoint) => [breakpoint.id, breakpoint.viewportWidth, breakpoint.viewportHeight]),
    [
      ['desktop', 1440, 900],
      ['tablet', 768, 1024],
      ['mobile', 390, 844],
      ['wide', 1440, 900],
    ],
  )
})

test('prototype normalization preserves tiny viewport values while review blocks and canvas stays visible', () => {
  const base = canonicalPrototype()
  const content = {
    ...base,
    breakpoints: base.breakpoints.map((breakpoint, index) => index === 0
      ? { ...breakpoint, viewportWidth: 0, viewportHeight: 1 }
      : breakpoint),
  }

  const normalized = normalizePrototypeContent(content)
  const desktop = normalized.breakpoints[0]

  assert.equal(desktop.viewportWidth, 0)
  assert.equal(desktop.viewportHeight, 1)
  assert.deepEqual(prototypeVisibleViewport(desktop), { width: 1440, height: 900 })
  assert.deepEqual(prototypePayloadIntegrityIssues(normalized), [])
  assert.ok(prototypeReviewIssues(normalized).some((issue) =>
    issue === 'Breakpoint 1 viewport width and height must each be integers of at least 240 pixels.'))

  const boundary = {
    ...normalized,
    breakpoints: normalized.breakpoints.map((breakpoint, index) => index === 0
      ? { ...breakpoint, viewportWidth: 240, viewportHeight: 240 }
      : breakpoint),
  }
  assert.equal(prototypeReviewIssues(boundary).some((issue) => issue.includes('at least 240 pixels')), false)
})

test('prototype normalization records explicitly invalid viewport values instead of silently defaulting', () => {
  const base = canonicalPrototype()
  const raw = {
    ...base,
    breakpoints: base.breakpoints.map((breakpoint, index) => index === 0
      ? { ...breakpoint, viewportWidth: null, viewportHeight: '900' }
      : breakpoint),
  } as unknown as PrototypeContentDto

  const normalized = normalizePrototypeContent(raw)
  assert.deepEqual(prototypePayloadIntegrityIssues(raw), [
    'Prototype data at breakpoints[1].viewportWidth must be a finite number.',
    'Prototype data at breakpoints[1].viewportHeight must be a finite number.',
  ])
  assert.deepEqual(
    [normalized.breakpoints[0].viewportWidth, normalized.breakpoints[0].viewportHeight],
    [1440, 900],
  )
  assert.deepEqual(prototypePayloadIntegrityIssues(normalized), [
    'Prototype data at breakpoints[1].viewportWidth must be a finite number.',
    'Prototype data at breakpoints[1].viewportHeight must be a finite number.',
  ])
})

test('prototype normalization falls back to scene layers and preserves unknown kinds as blockers', () => {
  const normalized = normalizePrototypeContent({
    pageSpecRevision: {
      artifactId: 'page-spec-orders',
      revisionId: 'page-spec-revision-1',
      contentHash: 'sha256:page-spec-orders',
    },
    states: [],
    breakpoints: [],
    layers: [],
    scene: {
      layers: [{
        id: 'layer-custom',
        type: 'carousel',
        name: 'Custom carousel',
        childIds: [],
      }],
    },
    frames: [],
  } as unknown as PrototypeContentDto)

  assert.deepEqual(Object.keys(normalized.layers), ['layer-custom'])
  assert.equal(normalized.layers['layer-custom'].kind, 'carousel')
  assert.ok(prototypeReviewIssues(normalized).some((issue) =>
    issue.includes('unsupported kind carousel')))
})

test('prototype normalization never falls back from a non-empty invalid primary layer collection', () => {
  const normalized = normalizePrototypeContent({
    pageSpecRevision: {
      artifactId: 'page-spec-orders',
      revisionId: 'page-spec-revision-1',
      contentHash: 'sha256:page-spec-orders',
    },
    states: [],
    breakpoints: [],
    layers: [null, { name: 'Missing identity' }],
    scene: {
      layers: [{
        id: 'scene-lure',
        kind: 'frame',
        name: 'Must not be used',
      }],
    },
    frames: [],
  } as unknown as PrototypeContentDto)

  assert.equal(normalized.layers['scene-lure'], undefined)
  assert.equal(Object.keys(normalized.layers).length, 2)
  assert.deepEqual(Object.values(normalized.layers).map((layer) => layer.name), [
    'Invalid layer 1',
    'Missing identity',
  ])
  assert.deepEqual(prototypePayloadIntegrityIssues(normalized), [
    'Prototype data at layers[1] must be an object.',
    'Prototype layer at layers[2] must have a stable ID.',
  ])
  assert.equal(prototypeLayerIdentityIssues(normalized).length, 2)

  const normalizedAgain = normalizePrototypeContent(
    JSON.parse(JSON.stringify(normalized)) as PrototypeContentDto,
  )
  assert.deepEqual(
    prototypePayloadIntegrityIssues(normalizedAgain),
    prototypePayloadIntegrityIssues(normalized),
  )
})

test('prototype normalization keeps object-array corruption sticky across ordinary edits', () => {
  const base = canonicalPrototype()
  const interaction = {
    id: 'interaction-invalid-nested',
    sourceLayerId: Object.keys(base.layers)[0],
    trigger: 'click',
    guards: [null],
    actions: [false],
  }
  const raw = {
    ...base,
    states: [...base.states, null],
    breakpoints: [...base.breakpoints, null],
    frames: [...base.frames, null],
    overrides: [null],
    interactions: [interaction, null],
    fixtures: [null],
    tokenBindings: [null],
    componentBindings: [null],
    assets: null,
    traceLinks: [null],
  } as unknown as PrototypeContentDto

  const normalized = normalizePrototypeContent(raw)
  const issues = prototypePayloadIntegrityIssues(normalized)
  assert.deepEqual(prototypePayloadIntegrityIssues(raw), issues)
  const expectedPaths = [
    `states[${base.states.length + 1}]`,
    `breakpoints[${base.breakpoints.length + 1}]`,
    `frames[${base.frames.length + 1}]`,
    'overrides[1]',
    'interactions[2]',
    'interactions[1].guards[1]',
    'interactions[1].actions[1]',
    'fixtures[1]',
    'tokenBindings[1]',
    'componentBindings[1]',
    'assets',
    'traceLinks[1]',
  ]
  for (const path of expectedPaths) {
    assert.ok(issues.some((issue) => issue.includes(path)), `missing integrity issue for ${path}`)
  }
  assert.equal(normalized.states.length, base.states.length)
  assert.equal(normalized.breakpoints.length, base.breakpoints.length)
  assert.equal(normalized.frames.length, base.frames.length)
  assert.equal(normalized.interactions.length, 1)
  assert.ok(prototypeReviewIssues(normalized).some((issue) => issue.includes('states[')))

  const edited = { ...normalized, exploratory: true }
  assert.deepEqual(prototypePayloadIntegrityIssues(normalizePrototypeContent(edited)), issues)
  assert.equal('__prototypeCompatibilityIssues' in canonicalPrototype(), false)
  assert.deepEqual(prototypePayloadIntegrityIssues(normalizePrototypeContent(canonicalPrototype())), [])
})

test('prototype normalization records malformed present contract fields at their exact paths', () => {
  const base = canonicalPrototype()
  const rootId = Object.keys(base.layers)[0]
  const raw = {
    ...base,
    exploratory: 'false',
    states: base.states.map((state, index) => index === 0
      ? { ...state, required: 'yes', fixtureIds: ['fixture-valid', null] }
      : state),
    breakpoints: base.breakpoints.map((breakpoint, index) => index === 0
      ? { ...breakpoint, minWidth: 1.5, maxWidth: '1440' }
      : breakpoint),
    layers: {
      ...base.layers,
      [rootId]: {
        ...base.layers[rootId],
        parentId: 17,
        childIds: ['layer-valid', false],
        layout: null,
        style: [],
        properties: 'invalid canonical properties',
        props: { text: 'legacy content must not be used' },
        requirementIds: ['requirement-valid', 42],
        acceptanceCriterionIds: null,
        fieldMetadata: 'invalid metadata',
      },
    },
    componentBindings: [{
      id: 'component-binding-1',
      layerId: rootId,
      componentId: 'component-1',
      propertyMapping: null,
    }],
  } as unknown as PrototypeContentDto

  const normalized = normalizePrototypeContent(raw)
  const issues = prototypePayloadIntegrityIssues(normalized)

  assert.deepEqual(issues, [
    'Prototype data at states[1].required must be a boolean.',
    'Prototype data at states[1].fixtureIds[2] must be a non-empty string.',
    'Prototype data at breakpoints[1].maxWidth must be a nonnegative integer.',
    'Prototype data at breakpoints[1].minWidth must be a nonnegative integer.',
    `Prototype data at layers.${rootId}.properties must be an object.`,
    `Prototype data at layers.${rootId}.parentId must be null or a non-empty string.`,
    `Prototype data at layers.${rootId}.childIds[2] must be a non-empty string.`,
    `Prototype data at layers.${rootId}.layout must be an object.`,
    `Prototype data at layers.${rootId}.style must be an object.`,
    `Prototype data at layers.${rootId}.requirementIds[2] must be a non-empty string.`,
    `Prototype data at layers.${rootId}.acceptanceCriterionIds must be an array of non-empty strings.`,
    `Prototype data at layers.${rootId}.fieldMetadata must be an object.`,
    'Prototype data at exploratory must be a boolean.',
    'Prototype data at componentBindings[1].propertyMapping must be an object.',
  ])
  assert.deepEqual(normalized.layers[rootId].properties, {})
  assert.equal(normalized.layers[rootId].properties.text, undefined)
  assert.deepEqual(normalized.states[0].fixtureIds, ['fixture-valid'])
  assert.deepEqual(normalized.layers[rootId].childIds, ['layer-valid'])

  const edited = normalizePrototypeContent({
    ...normalized,
    states: normalized.states.map((state, index) => index === 0
      ? { ...state, title: 'Ready after an ordinary edit' }
      : state),
  })
  assert.deepEqual(prototypePayloadIntegrityIssues(edited), issues)
})

test('prototype normalization uses legacy props only when canonical properties are absent', () => {
  const base = canonicalPrototype()
  const rootId = Object.keys(base.layers)[0]
  const root = base.layers[rootId]
  const { properties: _properties, ...withoutProperties } = root
  const legacyOnly = normalizePrototypeContent({
    ...base,
    layers: {
      ...base.layers,
      [rootId]: { ...withoutProperties, props: { text: 'Legacy content' } },
    },
  } as unknown as PrototypeContentDto)
  assert.equal(legacyOnly.layers[rootId].properties.text, 'Legacy content')
  assert.deepEqual(prototypePayloadIntegrityIssues(legacyOnly), [])

  const canonicalMalformed = normalizePrototypeContent({
    ...base,
    layers: {
      ...base.layers,
      [rootId]: {
        ...withoutProperties,
        properties: null,
        props: { text: 'Legacy lure' },
      },
    },
  } as unknown as PrototypeContentDto)
  assert.deepEqual(canonicalMalformed.layers[rootId].properties, {})
  assert.deepEqual(prototypePayloadIntegrityIssues(canonicalMalformed), [
    `Prototype data at layers.${rootId}.properties must be an object.`,
  ])
})

test('prototype normalization keeps an explicit non-object componentRef as a sticky compatibility issue', () => {
  const base = canonicalPrototype()
  const rootId = Object.keys(base.layers)[0]
  const normalized = normalizePrototypeContent({
    ...base,
    layers: {
      ...base.layers,
      [rootId]: {
        ...base.layers[rootId],
        componentRef: 'not-an-exact-revision',
      },
    },
  } as unknown as PrototypeContentDto)

  const expected = `Prototype data at layers.${rootId}.componentRef must be an object.`
  assert.equal(normalized.layers[rootId].componentRef, undefined)
  assert.ok(prototypePayloadIntegrityIssues(normalized).includes(expected))
  assert.ok(prototypePayloadIntegrityIssues(normalizePrototypeContent({
    ...normalized,
    states: normalized.states.map((state, index) => index === 0
      ? { ...state, title: 'Edited without losing diagnostics' }
      : state),
  })).includes(expected))
})

test('exact PageSpec authority is runtime-safe and formal state membership is exact', () => {
  const pageSpecContent = createEmptyPageSpecContent(
    'page-orders',
    'Orders',
    '/orders',
    'Review customer orders.',
  )
  const authority = prototypePageSpecAuthority(pageSpecContent)
  assert.deepEqual(authority.issues, [])
  assert.equal(prototypeReviewIssues(canonicalPrototype(), { pageSpecAuthority: authority }).length, 0)

  const base = canonicalPrototype()
  const missingState = { ...base, states: base.states.slice(0, -1) }
  assert.ok(prototypeReviewIssues(missingState, { pageSpecAuthority: authority }).includes(
    'Formal Prototype states must preserve the exact PageSpec state ID and key set without downgrading required states.',
  ))
  const changedRequired = {
    ...base,
    states: base.states.map((state, index) => index === 0
      ? { ...state, required: !state.required }
      : state),
  }
  assert.ok(prototypeReviewIssues(changedRequired, { pageSpecAuthority: authority }).includes(
    'Formal Prototype states must preserve the exact PageSpec state ID and key set without downgrading required states.',
  ))
  const optionalAuthority = prototypePageSpecAuthority({
    ...pageSpecContent,
    states: pageSpecContent.states.map((state, index) => index === 0
      ? { ...state, required: undefined }
      : state),
  })
  assert.equal(prototypeReviewIssues(base, { pageSpecAuthority: optionalAuthority }).some((issue) =>
    issue.includes('without downgrading required states')), false)

  const missingBindings = prototypePageSpecAuthority({
    ...pageSpecContent,
    dataBindings: undefined,
  })
  assert.deepEqual(missingBindings.issues, [])
  assert.deepEqual([...missingBindings.allowedFixtureOperationIds], [])
  const malformedBindings = prototypePageSpecAuthority({
    ...pageSpecContent,
    dataBindings: [null],
  })
  assert.deepEqual(malformedBindings.issues, [])
  assert.deepEqual([...malformedBindings.allowedFixtureOperationIds], [])
})

test('prototype normalization never replaces explicitly invalid canonical text with aliases or generated text', () => {
  const base = canonicalPrototype()
  const rootId = Object.keys(base.layers)[0]
  const raw = {
    ...base,
    pageSpecRevision: {
      artifactId: '',
      revisionId: 17,
      contentHash: null,
    },
    sourcePageSpecArtifactId: 'legacy-page-spec',
    sourcePageSpecRevisionId: 'legacy-revision',
    sourcePageSpecHash: `sha256:${'b'.repeat(64)}`,
    states: base.states.map((state, index) => index === 0
      ? { ...state, id: '', key: '', title: 17, pageStateId: '' }
      : state),
    breakpoints: base.breakpoints.map((breakpoint, index) => index === 0
      ? {
          ...breakpoint,
          id: '',
          key: 'legacy-desktop',
          name: '',
          title: 'Legacy Desktop',
        }
      : breakpoint),
    layers: {
      ...base.layers,
      [rootId]: {
        ...base.layers[rootId],
        id: '',
        layerId: 'legacy-root',
        kind: '',
        type: 'screen',
        name: null,
        semanticRole: null,
        dataBindingId: '',
        props: { role: 'legacy-main' },
      },
    },
    frames: base.frames.map((frame, index) => index === 0
      ? {
          ...frame,
          id: '',
          stateId: null,
          breakpointId: '',
          rootLayerId: 17,
          title: '',
        }
      : frame),
  } as unknown as PrototypeContentDto

  const normalized = normalizePrototypeContent(raw)
  const expected = [
    'Prototype data at pageSpecRevision.artifactId must be a non-empty string.',
    'Prototype data at pageSpecRevision.revisionId must be a non-empty string.',
    'Prototype data at pageSpecRevision.contentHash must be a non-empty string.',
    'Prototype data at states[1].id must be a non-empty string.',
    'Prototype data at states[1].key must be a non-empty string.',
    'Prototype data at states[1].title must be a non-empty string.',
    'Prototype data at states[1].pageStateId must be null or a non-empty string.',
    'Prototype data at breakpoints[1].id must be a non-empty string.',
    'Prototype data at breakpoints[1].name must be a non-empty string.',
    `Prototype data at layers.${rootId}.id must be a non-empty string.`,
    `Prototype layer record layers.${rootId} does not match embedded ID legacy-root.`,
    `Prototype data at layers.${rootId}.kind must be a non-empty string.`,
    `Prototype data at layers.${rootId}.name must be a non-empty string.`,
    `Prototype data at layers.${rootId}.semanticRole must be a non-empty string.`,
    `Prototype data at layers.${rootId}.dataBindingId must be null or a non-empty string.`,
    'Prototype data at frames[1].id must be a non-empty string.',
    'Prototype data at frames[1].stateId must be a non-empty string.',
    'Prototype data at frames[1].breakpointId must be a non-empty string.',
    'Prototype data at frames[1].rootLayerId must be a non-empty string.',
    'Prototype data at frames[1].title must be a non-empty string.',
  ]
  assert.deepEqual(
    [...prototypePayloadIntegrityIssues(normalized)].sort(),
    [...expected].sort(),
  )
  assert.deepEqual(normalized.pageSpecRevision, {
    artifactId: '',
    revisionId: '',
    contentHash: '',
  })
  assert.deepEqual(
    [normalized.states[0].id, normalized.states[0].key, normalized.states[0].title],
    ['', '', ''],
  )
  assert.deepEqual(
    [normalized.breakpoints[0].id, normalized.breakpoints[0].name],
    ['', ''],
  )
  assert.equal(normalized.layers[rootId].kind, '')
  assert.equal(normalized.layers[rootId].name, '')
  assert.equal(normalized.layers[rootId].semanticRole, undefined)
  assert.equal(normalized.frames[0].title, '')

  const invalidRevisionObject = normalizePrototypeContent({
    ...base,
    pageSpecRevision: null,
    sourcePageSpecArtifactId: 'legacy-page-spec',
    sourcePageSpecRevisionId: 'legacy-revision',
    sourcePageSpecHash: `sha256:${'b'.repeat(64)}`,
  } as unknown as PrototypeContentDto)
  assert.deepEqual(invalidRevisionObject.pageSpecRevision, {
    artifactId: '',
    revisionId: '',
    contentHash: '',
  })
  assert.deepEqual(prototypePayloadIntegrityIssues(invalidRevisionObject), [
    'Prototype data at pageSpecRevision must be an object.',
  ])
})

test('prototype normalization retains duplicate array layers as detectable invalid records', () => {
  const normalized = normalizePrototypeContent({
    pageSpecRevision: {
      artifactId: 'page-spec-orders',
      revisionId: 'page-spec-revision-1',
      contentHash: 'sha256:page-spec-orders',
    },
    states: [],
    breakpoints: [],
    layers: [{
      id: 'layer-duplicate',
      kind: 'frame',
      name: 'First duplicate',
      childIds: [],
    }, {
      id: 'layer-duplicate',
      kind: 'text',
      name: 'Second duplicate',
      childIds: [],
    }],
    frames: [],
  } as unknown as PrototypeContentDto)

  assert.equal(Object.keys(normalized.layers).length, 2)
  assert.deepEqual(Object.values(normalized.layers).map((layer) => layer.name), [
    'First duplicate',
    'Second duplicate',
  ])
  assert.ok(prototypeLayerIdentityIssues(normalized).some((issue) =>
    issue.includes('duplicates an existing layer ID')))
  assert.ok(prototypeReviewIssues(normalized).some((issue) =>
    issue.includes('duplicates an existing layer ID')))
})

test('prototype frame coverage treats a missing root layer as a repairable gap', () => {
  const base = canonicalPrototype()
  const brokenFrame = { ...base.frames[0], rootLayerId: 'missing-root' }
  const broken = {
    ...base,
    frames: [brokenFrame, ...base.frames.slice(1)],
  }

  assert.ok(prototypeFrameCoverageGaps(broken).some((gap) =>
    gap.stateId === brokenFrame.stateId && gap.breakpointId === brokenFrame.breakpointId))
  assert.ok(prototypeReviewIssues(broken).some((issue) =>
    issue.includes(`has no frame at breakpoint ${brokenFrame.breakpointId}`)))

  const repaired = repairPrototypeFrameCoverage(broken, idFactory())
  assert.deepEqual(prototypeFrameCoverageGaps(repaired), [])
  assert.ok(repaired.frames.some((frame) =>
    frame.stateId === brokenFrame.stateId
      && frame.breakpointId === brokenFrame.breakpointId
      && Boolean(repaired.layers[frame.rootLayerId])))
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

test('prototype review gate mirrors integer geometry, fixture evidence, operation, and cycle blockers', () => {
  const base = canonicalPrototype()
  const rootId = Object.keys(base.layers)[0]
  const childId = 'layer-cycle-child'
  const stateId = base.states[0].id
  const invalid = {
    ...base,
    breakpoints: base.breakpoints.map((breakpoint, index) => index === 0
      ? {
          ...breakpoint,
          minWidth: 500,
          maxWidth: 499,
          viewportWidth: 1024.5,
          viewportHeight: 899.5,
        }
      : breakpoint),
    layers: {
      ...base.layers,
      [rootId]: {
        ...base.layers[rootId],
        childIds: [childId],
        layout: { x: -1, y: 0.5, width: 0, height: 900.25 },
      },
      [childId]: {
        id: childId,
        parentId: rootId,
        childIds: [rootId],
        kind: 'group',
        name: 'Cycle child',
        layout: { x: 0, y: 0, width: 100, height: 100 },
        style: {},
        properties: {},
        requirementIds: [],
        acceptanceCriterionIds: [],
        fieldMetadata: {},
      },
    },
    fixtures: [{
      id: 'fixture-invalid',
      name: '',
      stateId,
      statusCode: 99,
      latencyMs: -0.5,
      sanitized: true,
      contentHash: 'not-a-sha256-hash',
      operationId: 'operation-not-declared',
    }],
  } as unknown as PrototypeContentDto

  const issues = prototypeReviewIssues(invalid, {
    allowedFixtureOperationIds: new Set(['operation-declared']),
  })

  for (const expected of [
    'Breakpoint 1 must use a nonnegative integer minWidth and an optional integer maxWidth not below minWidth.',
    'Breakpoint 1 viewport width and height must each be integers of at least 240 pixels.',
    `Layer ${rootId} layout must declare nonnegative integer x and y values plus positive integer width and height values.`,
    'Prototype layer child IDs must form an acyclic semantic tree.',
    'Fixture 1 must declare a name, response, HTTP status, nonnegative integer latency, and canonical SHA-256 content hash.',
    'Fixture 1 operation ID operation-not-declared is not declared by the exact PageSpec revision.',
  ]) {
    assert.ok(issues.includes(expected), `missing exact review issue: ${expected}`)
  }

  const validFixture = {
    ...base,
    states: base.states.map((state, index) => index === 0
      ? { ...state, fixtureIds: ['fixture-valid'] }
      : state),
    fixtures: [{
      id: 'fixture-valid',
      name: 'Sanitized API response',
      stateId,
      response: {},
      statusCode: 200,
      latencyMs: 0,
      sanitized: true,
      contentHash: `sha256:${'a'.repeat(64)}`,
      operationId: 'operation-declared',
    }],
  }
  assert.equal(prototypeReviewIssues(validFixture, {
    allowedFixtureOperationIds: new Set(['operation-declared']),
  }).some((issue) => issue.startsWith('Fixture 1')), false)
})

test('prototype review gate requires exact fixture sets and complete declarative action references', () => {
  const base = canonicalPrototype()
  const states = base.states
  const rootId = Object.keys(base.layers)[0]
  const overlayId = 'layer-overlay'
  const fixture = (id: string, stateId: string) => ({
    id,
    name: `Fixture ${id}`,
    stateId,
    response: {},
    statusCode: 200,
    latencyMs: 0,
    sanitized: true,
    contentHash: `sha256:${'c'.repeat(64)}`,
  })
  const invalid = {
    ...base,
    states: states.map((state, index) => index === 0
      ? { ...state, fixtureIds: ['fixture-a', 'fixture-a', 'fixture-b'] }
      : { ...state, fixtureIds: [] }),
    fixtures: [
      fixture('fixture-a', states[0].id),
      fixture('fixture-b', states[1].id),
      fixture('fixture-c', states[0].id),
    ],
    layers: {
      ...base.layers,
      [rootId]: { ...base.layers[rootId], childIds: [overlayId] },
      [overlayId]: {
        id: overlayId,
        parentId: rootId,
        childIds: [],
        kind: 'overlay',
        name: 'Confirmation overlay',
        layout: { x: 0, y: 0, width: 320, height: 240 },
        style: {},
        properties: {},
        requirementIds: [],
        acceptanceCriterionIds: [],
        fieldMetadata: {},
      },
    },
    interactions: [{
      id: 'interaction-duplicate',
      sourceLayerId: rootId,
      trigger: 'click',
      guards: [],
      actions: [],
    }, {
      id: 'interaction-duplicate',
      sourceLayerId: rootId,
      trigger: 'click',
      guards: [],
      actions: [
        { type: 'setState', stateId: 'state-missing' },
        { type: 'openOverlay', layerId: rootId },
        { type: 'submitFixture', fixtureId: 'fixture-missing' },
        { type: 'updateBinding', bindingId: '' },
        { type: 'navigate' },
        { type: 'closeOverlay' },
      ],
    }],
  } as unknown as PrototypeContentDto

  const issues = prototypeReviewIssues(invalid)
  for (const expected of [
    'State 1 fixture 2 must be a duplicate-free exact reference to a fixture owned by that state.',
    'State 1 fixture 3 must be a duplicate-free exact reference to a fixture owned by that state.',
    'Fixture fixture-c must be declared by exactly one state fixtureIds set.',
    'Interaction 1 must declare at least one action.',
    'Interaction 2 needs a unique stable ID, existing source layer, and declarative trigger.',
    'Interaction 2 action 1 must reference the exact declared state, overlay, binding, fixture, or navigation target.',
    'Interaction 2 action 2 must reference the exact declared state, overlay, binding, fixture, or navigation target.',
    'Interaction 2 action 3 must reference the exact declared state, overlay, binding, fixture, or navigation target.',
    'Interaction 2 action 4 must reference the exact declared state, overlay, binding, fixture, or navigation target.',
    'Interaction 2 action 5 must reference the exact declared state, overlay, binding, fixture, or navigation target.',
  ]) {
    assert.ok(issues.includes(expected), `missing exact fixture/action issue: ${expected}`)
  }
  assert.equal(issues.some((issue) => issue.includes('Interaction 2 action 6')), false)
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
