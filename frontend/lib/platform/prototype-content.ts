import type {
  JsonObject,
  PrototypeActionDto,
  PrototypeBreakpointDto,
  PrototypeContentDto,
  PrototypeFrameDto,
  PrototypeLayerDto,
  PrototypeLayerKind,
  PrototypeStateDto,
} from './dto'

export const REQUIRED_PROTOTYPE_BREAKPOINT_NAMES = ['desktop', 'tablet', 'mobile'] as const

const ALLOWED_PROTOTYPE_TRIGGERS = new Set(['click', 'submit', 'change', 'hover', 'load'])
const ALLOWED_PROTOTYPE_ACTIONS = new Set([
  'navigate',
  'setState',
  'openOverlay',
  'closeOverlay',
  'updateBinding',
  'submitFixture',
])

export type PrototypeIdFactory = (prefix: 'frame' | 'layer') => string

export class PrototypeContentMutationError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'PrototypeContentMutationError'
  }
}

const PROTOTYPE_LAYER_KINDS = new Set<PrototypeLayerKind>([
  'frame',
  'group',
  'text',
  'image',
  'componentInstance',
  'input',
  'button',
  'list',
  'overlay',
  'slot',
])

const INVALID_DUPLICATE_LAYER_ID_PREFIX = '__invalid_duplicate_layer__'
const INVALID_LAYER_ENTRY_ID_PREFIX = '__invalid_layer_entry__'
const PROTOTYPE_COMPATIBILITY_ISSUES_FIELD = '__prototypeCompatibilityIssues'

type PrototypeContentWithCompatibilityIssues = PrototypeContentDto & {
  readonly __prototypeCompatibilityIssues?: readonly string[]
}

export interface PrototypePageSpecStateAuthority {
  readonly id: string
  readonly key: string
  readonly required: boolean
}

export interface PrototypePageSpecAuthority {
  readonly states: readonly PrototypePageSpecStateAuthority[]
  readonly allowedFixtureOperationIds: ReadonlySet<string>
  readonly issues: readonly string[]
}

export function prototypePageSpecAuthority(value: unknown): PrototypePageSpecAuthority {
  const issues: string[] = []
  if (!isObjectValue(value)) {
    return {
      states: [],
      allowedFixtureOperationIds: new Set(),
      issues: ['Exact PageSpec revision content must be an object.'],
    }
  }

  const states: PrototypePageSpecStateAuthority[] = []
  const rawStates = value.states
  if (Array.isArray(rawStates)) {
    for (const rawState of rawStates) {
      if (!isObjectValue(rawState)) continue
      const id = textValue(rawState.id)
      const key = textValue(rawState.key) || textValue(rawState.name)
      if (!id || !key) continue
      states.push({ id, key, required: rawState.required === true })
    }
  }

  const allowedFixtureOperationIds = new Set<string>()
  const rawBindings = value.dataBindings
  if (Array.isArray(rawBindings)) {
    for (const rawBinding of rawBindings) {
      if (!isObjectValue(rawBinding)) continue
      const source = textValue(rawBinding.source)
      const operationId = textValue(rawBinding.operationId)
      if (source === 'api' && operationId) allowedFixtureOperationIds.add(operationId)
    }
  }

  return {
    states,
    allowedFixtureOperationIds,
    issues: uniqueStrings(issues),
  }
}

// Platform payloads are runtime data even when the HTTP client has a generic
// TypeScript return type. Keep the editor usable for incomplete historical
// drafts while the canonical server gate continues to reject missing semantic
// content such as states, layers, and frames.
export function normalizePrototypeContent(content: PrototypeContentDto): PrototypeContentDto {
  const raw = objectValue(content)
  const compatibilityIssues = compatibilityIssueValues(raw[PROTOTYPE_COMPATIBILITY_ISSUES_FIELD])
  const objectArray = (value: unknown, path: string) =>
    objectArrayValue(value, path, compatibilityIssues)
  const pageSpecRevisionPresent = hasOwn(raw, 'pageSpecRevision')
  const pageSpecRevisionValid = isObjectValue(raw.pageSpecRevision)
  if (pageSpecRevisionPresent && !pageSpecRevisionValid) {
    compatibilityIssues.push('Prototype data at pageSpecRevision must be an object.')
  }
  const pageSpecRevision: Record<string, unknown> = pageSpecRevisionValid
    ? raw.pageSpecRevision as Record<string, unknown>
    : {}
  const states = objectArray(raw.states, 'states').map(({ value: state, path }) => {
    const id = requiredStringFieldValue(state, 'id', '', `${path}.id`, compatibilityIssues)
    const key = requiredStringFieldValue(state, 'key', id, `${path}.key`, compatibilityIssues)
    const title = requiredStringFieldValue(
      state,
      'title',
      key || id,
      `${path}.title`,
      compatibilityIssues,
    )
    const pageStateId = optionalStringFieldValue(
      state,
      'pageStateId',
      `${path}.pageStateId`,
      compatibilityIssues,
    )
    const normalizedState: Record<string, unknown> = {
      ...state,
      id,
      key,
      title,
      required: booleanFieldValue(state, 'required', false, `${path}.required`, compatibilityIssues),
      fixtureIds: stringArrayValue(state.fixtureIds, `${path}.fixtureIds`, compatibilityIssues),
      ...(pageStateId ? { pageStateId } : {}),
    }
    if (!pageStateId) delete normalizedState.pageStateId
    return normalizedState as unknown as PrototypeStateDto
  })
  const breakpoints = objectArray(raw.breakpoints, 'breakpoints').map(({ value: breakpoint, path }) => {
    const id = hasOwn(breakpoint, 'id')
      ? requiredStringFieldValue(breakpoint, 'id', '', `${path}.id`, compatibilityIssues)
      : requiredStringFieldValue(breakpoint, 'key', '', `${path}.key`, compatibilityIssues)
    const name = hasOwn(breakpoint, 'name')
      ? requiredStringFieldValue(breakpoint, 'name', '', `${path}.name`, compatibilityIssues)
      : hasOwn(breakpoint, 'title')
        ? requiredStringFieldValue(breakpoint, 'title', '', `${path}.title`, compatibilityIssues)
        : hasOwn(breakpoint, 'key')
          ? requiredStringFieldValue(breakpoint, 'key', '', `${path}.key`, compatibilityIssues)
          : id
    const defaultViewport = defaultBreakpointViewport(name)
    const maxWidth = optionalNonNegativeIntegerField(
      breakpoint,
      'maxWidth',
      `${path}.maxWidth`,
      compatibilityIssues,
    )
    const normalizedBreakpoint: Record<string, unknown> = {
      ...breakpoint,
      id,
      name,
      minWidth: nonNegativeIntegerField(
        breakpoint,
        'minWidth',
        defaultBreakpointMinWidth(name),
        `${path}.minWidth`,
        compatibilityIssues,
      ),
      ...(maxWidth === undefined ? {} : { maxWidth }),
      viewportWidth: normalizedViewportDimension(
        breakpoint,
        'viewportWidth',
        'width',
        breakpoint.viewportWidth,
        breakpoint.width,
        defaultViewport.width,
        `${path}.viewportWidth`,
        compatibilityIssues,
      ),
      viewportHeight: normalizedViewportDimension(
        breakpoint,
        'viewportHeight',
        'height',
        breakpoint.viewportHeight,
        breakpoint.height,
        defaultViewport.height,
        `${path}.viewportHeight`,
        compatibilityIssues,
      ),
    }
    if (maxWidth === undefined) delete normalizedBreakpoint.maxWidth
    return normalizedBreakpoint as unknown as PrototypeBreakpointDto
  })
  const primaryLayers = layerCollectionValue(raw.layers, 'layers', compatibilityIssues)
  let rawLayers = primaryLayers.entries
  if (primaryLayers.useFallback) {
    if (raw.scene !== undefined && !isObjectValue(raw.scene)) {
      compatibilityIssues.push('Prototype data at scene must be an object.')
      rawLayers = []
    } else {
      rawLayers = layerCollectionValue(
        objectValue(raw.scene).layers,
        'scene.layers',
        compatibilityIssues,
      ).entries
    }
  }
  const layers: Record<string, PrototypeLayerDto> = {}
  const layerIds = new Set<string>()
  rawLayers.forEach(([recordId, layer, path], index) => {
    const sourceId = recordId
    if (!sourceId) return
    const textCompatibilityIssues = sourceId.startsWith(INVALID_LAYER_ENTRY_ID_PREFIX)
      ? []
      : compatibilityIssues
    const id = layerIds.has(sourceId)
      ? duplicateLayerId(sourceId, index, layerIds)
      : sourceId
    layerIds.add(id)
    const kindText = hasOwn(layer, 'kind')
      ? requiredStringFieldValue(layer, 'kind', '', `${path}.kind`, textCompatibilityIssues)
      : requiredStringFieldValue(layer, 'type', '', `${path}.type`, textCompatibilityIssues)
    const kind = prototypeLayerKind(kindText)
    const properties: Record<string, JsonObject[string]> = {
      ...(hasOwn(layer, 'properties')
        ? jsonObjectValue(layer.properties, `${path}.properties`, compatibilityIssues)
        : jsonObjectValue(layer.props, `${path}.props`, compatibilityIssues)),
    }
    if (kind === 'button' && !hasOwn(properties, 'text') && hasOwn(properties, 'label')) {
      properties.text = requiredStringFieldValue(
        properties,
        'label',
        '',
        `${path}.properties.label`,
        textCompatibilityIssues,
      )
    }
    const parentId = optionalStringFieldValue(
      layer,
      'parentId',
      `${path}.parentId`,
      compatibilityIssues,
    )
    const semanticRole = hasOwn(layer, 'semanticRole')
      ? requiredStringFieldValue(
          layer,
          'semanticRole',
          '',
          `${path}.semanticRole`,
          textCompatibilityIssues,
        )
      : hasOwn(properties, 'role')
        ? requiredStringFieldValue(
            properties,
            'role',
            '',
            `${path}.properties.role`,
            textCompatibilityIssues,
          )
        : ''
    const dataBindingId = optionalStringFieldValue(
      layer,
      'dataBindingId',
      `${path}.dataBindingId`,
      textCompatibilityIssues,
    )
    const componentRef = layer.componentRef
    if (hasOwn(layer, 'componentRef')
      && componentRef !== undefined
      && componentRef !== null
      && !isObjectValue(componentRef)) {
      compatibilityIssues.push(`Prototype data at ${path}.componentRef must be an object.`)
    }
    const normalizedLayer: Record<string, unknown> = {
      ...layer,
      id,
      ...(parentId ? { parentId } : {}),
      childIds: stringArrayValue(layer.childIds, `${path}.childIds`, compatibilityIssues),
      kind,
      name: requiredStringFieldValue(
        layer,
        'name',
        `Layer ${index + 1}`,
        `${path}.name`,
        textCompatibilityIssues,
      ),
      ...(semanticRole ? { semanticRole } : {}),
      layout: jsonObjectValue(layer.layout, `${path}.layout`, compatibilityIssues),
      style: jsonObjectValue(layer.style, `${path}.style`, compatibilityIssues),
      properties: properties as JsonObject,
      ...(dataBindingId ? { dataBindingId } : {}),
      ...(isObjectValue(componentRef) ? { componentRef } : {}),
      requirementIds: stringArrayValue(layer.requirementIds, `${path}.requirementIds`, compatibilityIssues),
      acceptanceCriterionIds: stringArrayValue(layer.acceptanceCriterionIds, `${path}.acceptanceCriterionIds`, compatibilityIssues),
      fieldMetadata: objectFieldValue(layer.fieldMetadata, `${path}.fieldMetadata`, compatibilityIssues),
    }
    if (!parentId) delete normalizedLayer.parentId
    if (!semanticRole) delete normalizedLayer.semanticRole
    if (!dataBindingId) delete normalizedLayer.dataBindingId
    if (!isObjectValue(componentRef)) delete normalizedLayer.componentRef
    layers[id] = normalizedLayer as unknown as PrototypeLayerDto
  })
  const stateTitles = new Map(states.map((state) => [state.id, state.title]))
  const breakpointNames = new Map(breakpoints.map((breakpoint) => [breakpoint.id, breakpoint.name]))

  const normalized: Record<string, unknown> = {
    ...raw,
    pageSpecRevision: {
      artifactId: canonicalStringWithLegacyFallback(
        pageSpecRevision,
        'artifactId',
        raw,
        'sourcePageSpecArtifactId',
        'pageSpecRevision.artifactId',
        'sourcePageSpecArtifactId',
        compatibilityIssues,
        !pageSpecRevisionPresent || pageSpecRevisionValid,
      ),
      revisionId: canonicalStringWithLegacyFallback(
        pageSpecRevision,
        'revisionId',
        raw,
        'sourcePageSpecRevisionId',
        'pageSpecRevision.revisionId',
        'sourcePageSpecRevisionId',
        compatibilityIssues,
        !pageSpecRevisionPresent || pageSpecRevisionValid,
      ),
      contentHash: canonicalStringWithLegacyFallback(
        pageSpecRevision,
        'contentHash',
        raw,
        'sourcePageSpecHash',
        'pageSpecRevision.contentHash',
        'sourcePageSpecHash',
        compatibilityIssues,
        !pageSpecRevisionPresent || pageSpecRevisionValid,
      ),
    },
    exploratory: booleanFieldValue(raw, 'exploratory', false, 'exploratory', compatibilityIssues),
    states,
    breakpoints,
    layers,
    frames: objectArray(raw.frames, 'frames').map(({ value: frame, path }) => {
      const id = requiredStringFieldValue(frame, 'id', '', `${path}.id`, compatibilityIssues)
      const stateId = requiredStringFieldValue(
        frame,
        'stateId',
        '',
        `${path}.stateId`,
        compatibilityIssues,
      )
      const breakpointId = requiredStringFieldValue(
        frame,
        'breakpointId',
        '',
        `${path}.breakpointId`,
        compatibilityIssues,
      )
      const rootLayerId = requiredStringFieldValue(
        frame,
        'rootLayerId',
        '',
        `${path}.rootLayerId`,
        compatibilityIssues,
      )
      return {
        ...frame,
        id,
        stateId,
        breakpointId,
        rootLayerId,
        title: requiredStringFieldValue(
          frame,
          'title',
          `${stateTitles.get(stateId) || 'State'} · ${breakpointNames.get(breakpointId) || 'Breakpoint'}`,
          `${path}.title`,
          compatibilityIssues,
        ),
      }
    }),
    overrides: objectArray(raw.overrides, 'overrides').map(({ value }) => value),
    interactions: objectArray(raw.interactions, 'interactions').map(({ value: interaction, path }) => ({
      ...interaction,
      guards: objectArray(interaction.guards, `${path}.guards`).map(({ value }) => value),
      actions: objectArray(interaction.actions, `${path}.actions`).map(({ value }) => value),
    })),
    fixtures: objectArray(raw.fixtures, 'fixtures').map(({ value }) => value),
    tokenBindings: objectArray(raw.tokenBindings, 'tokenBindings').map(({ value }) => value),
    componentBindings: objectArray(raw.componentBindings, 'componentBindings').map(({ value: binding, path }) => ({
      ...binding,
      propertyMapping: jsonObjectValue(binding.propertyMapping, `${path}.propertyMapping`, compatibilityIssues),
    })),
    assets: objectArray(raw.assets, 'assets').map(({ value }) => value),
    traceLinks: objectArray(raw.traceLinks, 'traceLinks').map(({ value }) => value),
  }
  delete normalized[PROTOTYPE_COMPATIBILITY_ISSUES_FIELD]
  const uniqueCompatibilityIssues = uniqueStrings(compatibilityIssues)
  if (uniqueCompatibilityIssues.length > 0) {
    normalized[PROTOTYPE_COMPATIBILITY_ISSUES_FIELD] = uniqueCompatibilityIssues
  }
  return normalized as unknown as PrototypeContentDto
}

export function prototypeReviewIssues(
  content: PrototypeContentDto,
  options: {
    readonly allowedFixtureOperationIds?: ReadonlySet<string>
    readonly pageSpecAuthority?: PrototypePageSpecAuthority
  } = {},
) {
  content = normalizePrototypeContent(content)
  const issues: string[] = [...prototypePayloadIntegrityIssues(content)]
  const pageSpecRevision = content.pageSpecRevision
  if (!pageSpecRevision
    || !nonEmpty(pageSpecRevision.artifactId)
    || !nonEmpty(pageSpecRevision.revisionId)
    || !nonEmpty(pageSpecRevision.contentHash)) {
    issues.push('Prototype must pin an exact PageSpec artifact, revision, and content hash.')
  }

  const states = arrayValue(content.states)
  if (states.length === 0) issues.push('Prototype must contain at least one PageSpec state.')
  const stateIds = new Set<string>()
  const stateKeys = new Set<string>()
  states.forEach((state, index) => {
    const id = trimmed(state.id)
    const key = trimmed(state.key)
    if (!id || !key || !nonEmpty(state.title)) {
      issues.push(`State ${index + 1} needs a stable ID, key, and title.`)
    }
    if (stateIds.has(id) || stateKeys.has(key)) {
      issues.push(`State ${index + 1} duplicates an existing state ID or key.`)
    }
    stateIds.add(id)
    stateKeys.add(key)
  })
  if (!content.exploratory && options.pageSpecAuthority) {
    issues.push(...options.pageSpecAuthority.issues)
    const expectedStates = new Map(options.pageSpecAuthority.states.map((state) => [state.id, state]))
    const exactStateSet = states.length === expectedStates.size
      && states.every((state) => {
        const expected = expectedStates.get(trimmed(state.id))
        return expected
          && trimmed(state.key) === expected.key
          && (!expected.required || state.required === true)
      })
    if (!exactStateSet) {
      issues.push('Formal Prototype states must preserve the exact PageSpec state ID and key set without downgrading required states.')
    }
  }

  const breakpoints = arrayValue(content.breakpoints)
  if (breakpoints.length < 3) {
    issues.push('Prototype must provide Desktop, Tablet, and Mobile breakpoints.')
  }
  const breakpointIds = new Set<string>()
  const breakpointNames = new Set<string>()
  breakpoints.forEach((breakpoint, index) => {
    const id = trimmed(breakpoint.id)
    const name = normalizedBreakpointName(breakpoint.name)
    if (!id || !name) issues.push(`Breakpoint ${index + 1} needs a stable ID and name.`)
    const validMinWidth = Number.isInteger(breakpoint.minWidth) && breakpoint.minWidth >= 0
    const validMaxWidth = breakpoint.maxWidth === undefined
      || (Number.isInteger(breakpoint.maxWidth)
        && breakpoint.maxWidth >= 0
        && validMinWidth
        && breakpoint.maxWidth >= breakpoint.minWidth)
    if (!validMinWidth || !validMaxWidth) {
      issues.push(`Breakpoint ${index + 1} must use a nonnegative integer minWidth and an optional integer maxWidth not below minWidth.`)
    }
    if (!Number.isInteger(breakpoint.viewportWidth)
      || !Number.isInteger(breakpoint.viewportHeight)
      || breakpoint.viewportWidth < 240
      || breakpoint.viewportHeight < 240) {
      issues.push(`Breakpoint ${index + 1} viewport width and height must each be integers of at least 240 pixels.`)
    }
    if (breakpointIds.has(id) || breakpointNames.has(name)) {
      issues.push(`Breakpoint ${index + 1} duplicates an existing breakpoint ID or name.`)
    }
    breakpointIds.add(id)
    breakpointNames.add(name)
  })
  for (const required of REQUIRED_PROTOTYPE_BREAKPOINT_NAMES) {
    if (!breakpointNames.has(required)) {
      issues.push(`Prototype must declare the ${titleCase(required)} breakpoint.`)
    }
  }

  const layers = recordValue(content.layers)
  const layerEntries = Object.entries(layers)
  if (layerEntries.length === 0) issues.push('Prototype must contain a semantic layer tree.')
  issues.push(...prototypeLayerIdentityIssues(content))
  for (const [id, layer] of layerEntries) {
    const kind = trimmed(layer.kind)
    if (!PROTOTYPE_LAYER_KINDS.has(kind as PrototypeLayerKind)) {
      issues.push(`Layer ${id} has unsupported kind ${kind || '(missing kind)'}.`)
    }
    const parentId = trimmed(layer.parentId)
    if (parentId && !layers[parentId]) {
      issues.push(`Layer ${id} parent ${parentId} does not exist.`)
    }
    arrayValue(layer.childIds).forEach((childId, childIndex) => {
      const normalizedChildId = trimmed(childId)
      if (!layers[normalizedChildId] || normalizedChildId === id) {
        issues.push(`Layer ${id} child ${childIndex + 1} must reference another existing layer.`)
      }
    })
    const layout = layer.layout
    if (!nonNegativeIntegerValue(layout.x)
      || !nonNegativeIntegerValue(layout.y)
      || !positiveIntegerValue(layout.width)
      || !positiveIntegerValue(layout.height)) {
      issues.push(`Layer ${id} layout must declare nonnegative integer x and y values plus positive integer width and height values.`)
    }
  }
  if (hasPrototypeLayerCycle(layers)) {
    issues.push('Prototype layer child IDs must form an acyclic semantic tree.')
  }

  const frames = arrayValue(content.frames)
  if (frames.length === 0) {
    issues.push('Prototype must define a frame for each required state and breakpoint.')
  } else {
    const coverage = new Set<string>()
    const seenPairs = new Set<string>()
    frames.forEach((frame, index) => {
      const stateId = trimmed(frame.stateId)
      const breakpointId = trimmed(frame.breakpointId)
      const rootLayerId = trimmed(frame.rootLayerId)
      const pair = framePair(stateId, breakpointId)
      const valid = nonEmpty(frame.id)
        && stateIds.has(stateId)
        && breakpointIds.has(breakpointId)
        && Boolean(layers[rootLayerId])
      if (!valid) {
        issues.push(`Frame ${index + 1} must reference an existing state, breakpoint, and root layer.`)
      }
      if (seenPairs.has(pair)) {
        issues.push(`Frame ${index + 1} duplicates a state and breakpoint pair.`)
      }
      seenPairs.add(pair)
      if (valid) coverage.add(pair)
    })
    for (const state of states) {
      if (state.required === false) continue
      for (const breakpoint of breakpoints) {
        const stateId = trimmed(state.id)
        const breakpointId = trimmed(breakpoint.id)
        if (stateId && breakpointId && !coverage.has(framePair(stateId, breakpointId))) {
          issues.push(`Required state ${state.id} has no frame at breakpoint ${breakpoint.id}.`)
        }
      }
    }
  }

  const fixtureIds = new Set<string>()
  const fixtureStateIds = new Map<string, string>()
  arrayValue(content.fixtures).forEach((fixture, index) => {
    const fixtureId = trimmed(fixture.id)
    if (!fixtureId || fixtureIds.has(fixtureId)) {
      issues.push(`Fixture ${index + 1} needs one unique stable ID.`)
    }
    if (fixtureId) {
      fixtureIds.add(fixtureId)
      fixtureStateIds.set(fixtureId, trimmed(fixture.stateId))
    }
    if (fixture.sanitized !== true) issues.push(`Fixture ${index + 1} must be marked sanitized.`)
    if (!stateIds.has(trimmed(fixture.stateId))) {
      issues.push(`Fixture ${index + 1} must reference an existing state.`)
    }
    if (!nonEmpty(fixture.name)
      || !hasOwn(fixture, 'response')
      || !Number.isInteger(fixture.statusCode)
      || fixture.statusCode < 100
      || fixture.statusCode > 599
      || !Number.isInteger(fixture.latencyMs)
      || fixture.latencyMs < 0
      || !canonicalSha256Hash(fixture.contentHash)) {
      issues.push(`Fixture ${index + 1} must declare a name, response, HTTP status, nonnegative integer latency, and canonical SHA-256 content hash.`)
    }
    if (hasOwn(fixture, 'operationId')) {
      const operationId = trimmed(fixture.operationId)
      if (!operationId) {
        issues.push(`Fixture ${index + 1} operation ID must be non-empty when provided.`)
      } else if ((options.pageSpecAuthority?.allowedFixtureOperationIds
        ?? options.allowedFixtureOperationIds)?.has(operationId) === false) {
        issues.push(`Fixture ${index + 1} operation ID ${operationId} is not declared by the exact PageSpec revision.`)
      }
    }
  })

  const declaredFixtureStates = new Map<string, string>()
  states.forEach((state, stateIndex) => {
    arrayValue(state.fixtureIds).forEach((rawFixtureId, fixtureIndex) => {
      const fixtureId = trimmed(rawFixtureId)
      const stateId = trimmed(state.id)
      const alreadyDeclared = declaredFixtureStates.has(fixtureId)
      if (!fixtureId
        || !fixtureIds.has(fixtureId)
        || fixtureStateIds.get(fixtureId) !== stateId
        || alreadyDeclared) {
        issues.push(`State ${stateIndex + 1} fixture ${fixtureIndex + 1} must be a duplicate-free exact reference to a fixture owned by that state.`)
      }
      if (fixtureId) declaredFixtureStates.set(fixtureId, stateId)
    })
  })
  for (const fixtureId of fixtureIds) {
    if (!declaredFixtureStates.has(fixtureId)) {
      issues.push(`Fixture ${fixtureId} must be declared by exactly one state fixtureIds set.`)
    }
  }

  const interactionIds = new Set<string>()
  arrayValue(content.interactions).forEach((interaction, index) => {
    const interactionId = trimmed(interaction.id)
    const trigger = trimmed(interaction.trigger)
    if (!interactionId
      || interactionIds.has(interactionId)
      || !layers[trimmed(interaction.sourceLayerId)]
      || !ALLOWED_PROTOTYPE_TRIGGERS.has(trigger)) {
      issues.push(`Interaction ${index + 1} needs a unique stable ID, existing source layer, and declarative trigger.`)
    }
    if (interactionId) interactionIds.add(interactionId)
    const actions = arrayValue(interaction.actions)
    if (actions.length === 0) {
      issues.push(`Interaction ${index + 1} must declare at least one action.`)
    }
    actions.forEach((action, actionIndex) => {
      const type = actionType(action)
      if (!ALLOWED_PROTOTYPE_ACTIONS.has(type)) {
        issues.push(`Interaction ${index + 1} action ${actionIndex + 1} is not on the declarative action whitelist.`)
        return
      }
      const value = objectValue(action)
      const validReference = type === 'navigate'
        ? Boolean(trimmed(value.targetPageNodeId) || trimmed(value.targetPageSpecId))
        : type === 'setState'
          ? stateIds.has(trimmed(value.stateId))
          : type === 'openOverlay'
            ? normalizedBreakpointName(layers[trimmed(value.layerId)]?.kind) === 'overlay'
            : type === 'updateBinding'
              ? Boolean(trimmed(value.bindingId)) && hasOwn(value, 'value')
              : type === 'submitFixture'
                ? fixtureIds.has(trimmed(value.fixtureId))
                : true
      if (!validReference) {
        issues.push(`Interaction ${index + 1} action ${actionIndex + 1} must reference the exact declared state, overlay, binding, fixture, or navigation target.`)
      }
    })
  })
  return issues
}

export function prototypeFrameCoverageGaps(content: PrototypeContentDto) {
  const states = arrayValue(content.states)
  const breakpoints = arrayValue(content.breakpoints)
  const stateIds = new Set(states.map((state) => trimmed(state.id)).filter(Boolean))
  const breakpointIds = new Set(breakpoints.map((breakpoint) => trimmed(breakpoint.id)).filter(Boolean))
  const layers = recordValue(content.layers)
  const coverage = new Set(arrayValue(content.frames).flatMap((frame) => {
    const stateId = trimmed(frame.stateId)
    const breakpointId = trimmed(frame.breakpointId)
    return nonEmpty(frame.id)
      && stateIds.has(stateId)
      && breakpointIds.has(breakpointId)
      && Boolean(layers[trimmed(frame.rootLayerId)])
      ? [framePair(stateId, breakpointId)]
      : []
  }))
  return states.flatMap((state) =>
    breakpoints.flatMap((breakpoint) =>
      coverage.has(framePair(trimmed(state.id), trimmed(breakpoint.id)))
        ? []
        : [{ stateId: state.id, breakpointId: breakpoint.id }],
    ),
  )
}

export function prototypeLayerIdentityIssues(content: PrototypeContentDto) {
  const issues: string[] = []
  const seenEmbeddedIds = new Set<string>()
  for (const [recordId, layer] of Object.entries(recordValue(content.layers))) {
    const id = trimmed(recordId)
    const embeddedId = trimmed(layer.id)
    const duplicate = id.startsWith(INVALID_DUPLICATE_LAYER_ID_PREFIX)
      || embeddedId.startsWith(INVALID_DUPLICATE_LAYER_ID_PREFIX)
    if (duplicate) {
      issues.push(`Layer ${duplicateLayerSourceId(embeddedId || id)} duplicates an existing layer ID.`)
      continue
    }
    if (id.startsWith(INVALID_LAYER_ENTRY_ID_PREFIX)
      || embeddedId.startsWith(INVALID_LAYER_ENTRY_ID_PREFIX)) {
      issues.push(`Layer ${recordId || '(missing ID)'} is an invalid source placeholder.`)
      continue
    }
    if (!id || !embeddedId || embeddedId !== id || seenEmbeddedIds.has(embeddedId)) {
      issues.push(`Layer ${recordId || '(missing ID)'} does not have one unique stable record ID.`)
    }
    if (embeddedId) seenEmbeddedIds.add(embeddedId)
  }
  return issues
}

export function prototypePayloadIntegrityIssues(content: PrototypeContentDto) {
  const stored = storedCompatibilityIssues(content)
  if (stored.length > 0) return stored
  return storedCompatibilityIssues(normalizePrototypeContent(content))
}

export function prototypeVisibleViewport(
  breakpoint: Pick<PrototypeBreakpointDto, 'name' | 'viewportWidth' | 'viewportHeight'>,
) {
  const fallback = defaultBreakpointViewport(breakpoint.name)
  return {
    width: visibleViewportDimension(breakpoint.viewportWidth, fallback.width),
    height: visibleViewportDimension(breakpoint.viewportHeight, fallback.height),
  }
}

export function addPrototypeState(
  content: PrototypeContentDto,
  state: PrototypeStateDto,
  createId: PrototypeIdFactory = defaultId,
) {
  const id = trimmed(state.id)
  const key = trimmed(state.key)
  if (!id || !key || !nonEmpty(state.title)) {
    throw new PrototypeContentMutationError('A new state needs a stable ID, key, and title.')
  }
  if (content.states.some((item) => trimmed(item.id) === id || trimmed(item.key) === key)) {
    throw new PrototypeContentMutationError('State IDs and keys must be unique.')
  }
  const withRoot = ensurePrototypeRootLayer(content, createId)
  const nextState: PrototypeStateDto = {
    ...state,
    id,
    key,
    title: state.title.trim(),
    fixtureIds: uniqueStrings(state.fixtureIds),
  }
  const next = { ...withRoot, states: [...withRoot.states, nextState] }
  return repairPrototypeFrameCoverage(next, createId)
}

export function updatePrototypeState(
  content: PrototypeContentDto,
  stateId: string,
  patch: Partial<Omit<PrototypeStateDto, 'id' | 'fixtureIds'>>,
) {
  const current = content.states.find((state) => state.id === stateId)
  if (!current) throw new PrototypeContentMutationError('The selected state no longer exists.')
  const updated: PrototypeStateDto = {
    ...current,
    ...patch,
    id: current.id,
    key: patch.key === undefined ? current.key : patch.key.trim(),
    title: patch.title === undefined ? current.title : patch.title.trim(),
    fixtureIds: current.fixtureIds,
  }
  return {
    ...content,
    states: content.states.map((state) => state.id === stateId ? updated : state),
    frames: content.frames.map((frame) => frame.stateId === stateId
      ? { ...frame, title: frameTitle(updated, breakpointById(content, frame.breakpointId)) }
      : frame),
  }
}

export function removePrototypeState(content: PrototypeContentDto, stateId: string) {
  if (!content.states.some((state) => state.id === stateId)) return content
  if (content.states.length <= 1) {
    throw new PrototypeContentMutationError('A prototype must keep at least one state.')
  }
  const removedFixtureIds = new Set(content.fixtures
    .filter((fixture) => fixture.stateId === stateId)
    .map((fixture) => fixture.id))
  return {
    ...content,
    states: content.states
      .filter((state) => state.id !== stateId)
      .map((state) => ({
        ...state,
        fixtureIds: state.fixtureIds.filter((fixtureId) => !removedFixtureIds.has(fixtureId)),
      })),
    frames: content.frames.filter((frame) => frame.stateId !== stateId),
    overrides: content.overrides.filter((override) => override.stateId !== stateId),
    fixtures: content.fixtures.filter((fixture) => fixture.stateId !== stateId),
    interactions: content.interactions.map((interaction) => ({
      ...interaction,
      actions: interaction.actions.filter((action) => !actionReferencesStateOrFixture(action, stateId, removedFixtureIds)),
    })),
  }
}

export function addPrototypeBreakpoint(
  content: PrototypeContentDto,
  breakpoint: PrototypeBreakpointDto,
  createId: PrototypeIdFactory = defaultId,
) {
  const id = trimmed(breakpoint.id)
  const name = breakpoint.name.trim()
  if (!id || !name) {
    throw new PrototypeContentMutationError('A new breakpoint needs a stable ID and name.')
  }
  if (content.breakpoints.some((item) =>
    trimmed(item.id) === id || normalizedBreakpointName(item.name) === normalizedBreakpointName(name),
  )) {
    throw new PrototypeContentMutationError('Breakpoint IDs and names must be unique.')
  }
  const withRoot = ensurePrototypeRootLayer(content, createId)
  const next = {
    ...withRoot,
    breakpoints: [...withRoot.breakpoints, normalizedBreakpoint({ ...breakpoint, id, name })],
  }
  return repairPrototypeFrameCoverage(next, createId)
}

export function updatePrototypeBreakpoint(
  content: PrototypeContentDto,
  breakpointId: string,
  patch: Partial<Omit<PrototypeBreakpointDto, 'id'>>,
) {
  const current = content.breakpoints.find((breakpoint) => breakpoint.id === breakpointId)
  if (!current) throw new PrototypeContentMutationError('The selected breakpoint no longer exists.')
  const protectedName = isRequiredPrototypeBreakpoint(current)
  const updated = normalizedBreakpoint({
    ...current,
    ...patch,
    id: current.id,
    name: protectedName ? current.name : (patch.name ?? current.name).trim(),
  })
  return {
    ...content,
    breakpoints: content.breakpoints.map((breakpoint) => breakpoint.id === breakpointId ? updated : breakpoint),
    frames: content.frames.map((frame) => frame.breakpointId === breakpointId
      ? { ...frame, title: frameTitle(stateById(content, frame.stateId), updated) }
      : frame),
  }
}

export function removePrototypeBreakpoint(content: PrototypeContentDto, breakpointId: string) {
  const breakpoint = content.breakpoints.find((item) => item.id === breakpointId)
  if (!breakpoint) return content
  if (isRequiredPrototypeBreakpoint(breakpoint)) {
    throw new PrototypeContentMutationError('Desktop, Tablet, and Mobile breakpoints cannot be deleted.')
  }
  return {
    ...content,
    breakpoints: content.breakpoints.filter((item) => item.id !== breakpointId),
    frames: content.frames.filter((frame) => frame.breakpointId !== breakpointId),
    overrides: content.overrides.filter((override) => override.breakpointId !== breakpointId),
  }
}

export function repairPrototypeFrameCoverage(
  content: PrototypeContentDto,
  createId: PrototypeIdFactory = defaultId,
) {
  const withRoot = ensurePrototypeRootLayer(content, createId)
  const stateIds = new Set(withRoot.states.map((state) => state.id))
  const breakpointIds = new Set(withRoot.breakpoints.map((breakpoint) => breakpoint.id))
  const coverage = new Set<string>()
  const frames: PrototypeFrameDto[] = []
  for (const frame of withRoot.frames) {
    const pair = framePair(frame.stateId, frame.breakpointId)
    if (!nonEmpty(frame.id)
      || !stateIds.has(frame.stateId)
      || !breakpointIds.has(frame.breakpointId)
      || !withRoot.layers[frame.rootLayerId]
      || coverage.has(pair)) continue
    coverage.add(pair)
    frames.push(frame)
  }
  for (const state of withRoot.states) {
    for (const breakpoint of withRoot.breakpoints) {
      const pair = framePair(state.id, breakpoint.id)
      if (coverage.has(pair)) continue
      const rootLayerId = rootLayerForBreakpoint(withRoot, breakpoint.id)
      frames.push({
        id: createId('frame'),
        stateId: state.id,
        breakpointId: breakpoint.id,
        rootLayerId,
        title: frameTitle(state, breakpoint),
      })
      coverage.add(pair)
    }
  }
  return { ...withRoot, frames }
}

export function isRequiredPrototypeBreakpoint(breakpoint: Pick<PrototypeBreakpointDto, 'name'>) {
  return REQUIRED_PROTOTYPE_BREAKPOINT_NAMES.includes(
    normalizedBreakpointName(breakpoint.name) as (typeof REQUIRED_PROTOTYPE_BREAKPOINT_NAMES)[number],
  )
}

function ensurePrototypeRootLayer(content: PrototypeContentDto, createId: PrototypeIdFactory) {
  if (Object.keys(content.layers).length > 0) return content
  const id = createId('layer')
  const layer: PrototypeLayerDto = {
    id,
    childIds: [],
    kind: 'frame',
    name: 'Page',
    semanticRole: 'main',
    layout: { x: 0, y: 0, width: 1440, height: 900 },
    style: { fill: '#171719' },
    properties: {},
    requirementIds: [],
    acceptanceCriterionIds: [],
    fieldMetadata: {},
  }
  return { ...content, layers: { [id]: layer } }
}

function rootLayerForBreakpoint(content: PrototypeContentDto, breakpointId: string) {
  return content.frames.find((frame) =>
    frame.breakpointId === breakpointId && Boolean(content.layers[frame.rootLayerId]),
  )?.rootLayerId
    ?? content.frames.find((frame) => Boolean(content.layers[frame.rootLayerId]))?.rootLayerId
    ?? Object.keys(content.layers)[0]
}

function actionReferencesStateOrFixture(
  action: PrototypeActionDto,
  stateId: string,
  fixtureIds: ReadonlySet<string>,
) {
  if (action.type === 'setState') return action.stateId === stateId
  if (action.type === 'navigate') return action.targetStateId === stateId
  if (action.type === 'submitFixture') return fixtureIds.has(action.fixtureId)
  return false
}

function normalizedBreakpoint(breakpoint: PrototypeBreakpointDto): PrototypeBreakpointDto {
  const minWidth = finiteNonNegative(breakpoint.minWidth)
  const maxWidth = breakpoint.maxWidth === undefined
    ? undefined
    : Math.max(minWidth, finiteNonNegative(breakpoint.maxWidth))
  return {
    ...breakpoint,
    name: breakpoint.name.trim(),
    minWidth,
    ...(maxWidth === undefined ? { maxWidth: undefined } : { maxWidth }),
    viewportWidth: finiteNonNegative(breakpoint.viewportWidth),
    viewportHeight: finiteNonNegative(breakpoint.viewportHeight),
  }
}

function frameTitle(state?: Pick<PrototypeStateDto, 'title'>, breakpoint?: Pick<PrototypeBreakpointDto, 'name'>) {
  return `${state?.title || 'State'} · ${breakpoint?.name || 'Breakpoint'}`
}

function stateById(content: PrototypeContentDto, stateId: string) {
  return content.states.find((state) => state.id === stateId)
}

function breakpointById(content: PrototypeContentDto, breakpointId: string) {
  return content.breakpoints.find((breakpoint) => breakpoint.id === breakpointId)
}

function normalizedBreakpointName(value: unknown) {
  return trimmed(value).toLowerCase()
}

function titleCase(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

function actionType(action: unknown) {
  if (!action || typeof action !== 'object' || !('type' in action)) return ''
  return trimmed((action as { type?: unknown }).type)
}

function isObjectValue(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function objectValue(value: unknown): Record<string, unknown> {
  return isObjectValue(value) ? value : {}
}

function objectArrayValue(
  value: unknown,
  path: string,
  issues: string[],
): { value: Record<string, unknown>; path: string }[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) {
    issues.push(`Prototype data at ${path} must be an array of objects.`)
    return []
  }
  return value.flatMap((item, index) => {
    const itemPath = `${path}[${index + 1}]`
    if (isObjectValue(item)) return [{ value: item, path: itemPath }]
    issues.push(`Prototype data at ${itemPath} must be an object.`)
    return []
  })
}

function jsonObjectValue(value: unknown, path: string, issues: string[]): JsonObject {
  return objectFieldValue(value, path, issues) as JsonObject
}

function objectFieldValue(value: unknown, path: string, issues: string[]) {
  if (value === undefined) return {}
  if (isObjectValue(value)) return value
  issues.push(`Prototype data at ${path} must be an object.`)
  return {}
}

function stringArrayValue(value: unknown, path: string, issues: string[]): string[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) {
    issues.push(`Prototype data at ${path} must be an array of non-empty strings.`)
    return []
  }
  return value.flatMap((item, index) => {
    const normalized = textValue(item)
    if (normalized) return [normalized]
    issues.push(`Prototype data at ${path}[${index + 1}] must be a non-empty string.`)
    return []
  })
}

function textValue(value: unknown) {
  return typeof value === 'string' ? value.trim() : ''
}

function hasFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function booleanFieldValue(
  source: Record<string, unknown>,
  field: string,
  fallback: boolean,
  path: string,
  issues: string[],
) {
  const value = source[field]
  if (!hasOwn(source, field) || value === undefined) return fallback
  if (typeof value === 'boolean') return value
  issues.push(`Prototype data at ${path} must be a boolean.`)
  return fallback
}

function requiredStringFieldValue(
  source: Record<string, unknown>,
  field: string,
  fallback: string,
  path: string,
  issues: string[],
) {
  if (!hasOwn(source, field) || source[field] === undefined) return fallback
  const normalized = textValue(source[field])
  if (normalized) return normalized
  issues.push(`Prototype data at ${path} must be a non-empty string.`)
  return ''
}

function canonicalStringWithLegacyFallback(
  canonicalSource: Record<string, unknown>,
  canonicalField: string,
  legacySource: Record<string, unknown>,
  legacyField: string,
  canonicalPath: string,
  legacyPath: string,
  issues: string[],
  allowLegacy: boolean,
) {
  if (hasOwn(canonicalSource, canonicalField)) {
    return requiredStringFieldValue(
      canonicalSource,
      canonicalField,
      '',
      canonicalPath,
      issues,
    )
  }
  return allowLegacy
    ? requiredStringFieldValue(legacySource, legacyField, '', legacyPath, issues)
    : ''
}

function nonNegativeIntegerField(
  source: Record<string, unknown>,
  field: string,
  fallback: number,
  path: string,
  issues: string[],
) {
  const value = source[field]
  if (!hasOwn(source, field) || value === undefined) return fallback
  if (hasFiniteNumber(value) && Number.isInteger(value) && value >= 0) return value
  issues.push(`Prototype data at ${path} must be a nonnegative integer.`)
  return fallback
}

function optionalNonNegativeIntegerField(
  source: Record<string, unknown>,
  field: string,
  path: string,
  issues: string[],
) {
  const value = source[field]
  if (!hasOwn(source, field) || value === undefined) return undefined
  if (hasFiniteNumber(value) && Number.isInteger(value) && value >= 0) return value
  issues.push(`Prototype data at ${path} must be a nonnegative integer.`)
  return undefined
}

function optionalStringFieldValue(
  source: Record<string, unknown>,
  field: string,
  path: string,
  issues: string[],
) {
  const value = source[field]
  if (!hasOwn(source, field) || value === undefined || value === null) return ''
  const normalized = textValue(value)
  if (normalized) return normalized
  issues.push(`Prototype data at ${path} must be null or a non-empty string.`)
  return ''
}

function normalizedViewportDimension(
  source: Record<string, unknown>,
  field: string,
  legacyField: string,
  value: unknown,
  legacyValue: unknown,
  fallback: number,
  path: string,
  issues: string[],
) {
  if (hasFiniteNumber(value)) return value
  if (hasOwn(source, field) && value !== undefined) {
    issues.push(`Prototype data at ${path} must be a finite number.`)
  }
  if (hasFiniteNumber(legacyValue)) return legacyValue
  if (hasOwn(source, legacyField) && legacyValue !== undefined) {
    issues.push(`Prototype data at ${path.replace(field, legacyField)} must be a finite number.`)
  }
  return fallback
}

function visibleViewportDimension(value: number, fallback: number) {
  return Number.isFinite(value) && value >= 240 ? Math.round(value) : fallback
}

function defaultBreakpointMinWidth(name: string) {
  switch (name.trim().toLowerCase()) {
  case 'desktop': return 1024
  case 'tablet': return 768
  default: return 0
  }
}

function defaultBreakpointViewport(name: string) {
  switch (name.trim().toLowerCase()) {
  case 'desktop': return { width: 1440, height: 900 }
  case 'tablet': return { width: 768, height: 1024 }
  case 'mobile': return { width: 390, height: 844 }
  default: return { width: 1440, height: 900 }
  }
}

function prototypeLayerKind(value: unknown): PrototypeLayerKind {
  const kind = textValue(value)
  if (PROTOTYPE_LAYER_KINDS.has(kind as PrototypeLayerKind)) return kind as PrototypeLayerKind
  switch (kind.toLowerCase()) {
  case 'screen': return 'frame'
  case 'section':
  case 'container':
  case 'card': return 'group'
  case 'component': return 'componentInstance'
  case 'heading':
  case 'label':
  case 'paragraph': return 'text'
  default: return kind as PrototypeLayerKind
  }
}

function layerCollectionValue(
  value: unknown,
  path: string,
  issues: string[],
): { entries: [string, Record<string, unknown>, string][]; useFallback: boolean } {
  if (value === undefined || value === null) return { entries: [], useFallback: true }
  if (Array.isArray(value)) {
    if (value.length === 0) return { entries: [], useFallback: true }
    return { entries: value.map((item, index) => {
      if (!isObjectValue(item)) {
        const id = invalidLayerEntryId(index)
        const itemPath = `${path}[${index + 1}]`
        issues.push(`Prototype data at ${itemPath} must be an object.`)
        return [id, invalidLayerPlaceholder(id, index), itemPath]
      }
      const layer = item
      const itemPath = `${path}[${index + 1}]`
      const canonicalPresent = hasOwn(layer, 'id')
      const legacyPresent = hasOwn(layer, 'layerId')
      const id = canonicalPresent
        ? requiredStringFieldValue(layer, 'id', '', `${itemPath}.id`, issues)
        : requiredStringFieldValue(layer, 'layerId', '', `${itemPath}.layerId`, issues)
      if (id) return [id, layer, itemPath]
      const placeholderId = invalidLayerEntryId(index)
      if (!canonicalPresent && !legacyPresent) {
        issues.push(`Prototype layer at ${itemPath} must have a stable ID.`)
      }
      return [placeholderId, { ...layer, id: placeholderId }, itemPath]
    }), useFallback: false }
  }
  if (isObjectValue(value)) {
    const records = Object.entries(value)
    if (records.length === 0) return { entries: [], useFallback: true }
    return { entries: records.map(([recordId, item], index) => {
      const normalizedRecordId = textValue(recordId)
      if (!isObjectValue(item)) {
        const id = invalidLayerEntryId(index, normalizedRecordId)
        const itemPath = `${path}.${recordId || '(empty key)'}`
        issues.push(`Prototype data at ${itemPath} must be an object.`)
        return [id, invalidLayerPlaceholder(id, index), itemPath]
      }
      const itemPath = `${path}.${recordId || '(empty key)'}`
      const canonicalId = hasOwn(item, 'id')
        ? requiredStringFieldValue(item, 'id', '', `${itemPath}.id`, issues)
        : ''
      const legacyId = hasOwn(item, 'layerId')
        ? requiredStringFieldValue(item, 'layerId', '', `${itemPath}.layerId`, issues)
        : ''
      const embeddedId = hasOwn(item, 'id') ? canonicalId : legacyId
      const id = embeddedId || normalizedRecordId
      if (!id) {
        const placeholderId = invalidLayerEntryId(index)
        issues.push(`Prototype layer at ${itemPath} must have a stable ID.`)
        return [placeholderId, { ...item, id: placeholderId }, itemPath]
      }
      for (const explicitId of [canonicalId, legacyId]) {
        if (explicitId && normalizedRecordId && explicitId !== normalizedRecordId) {
          issues.push(`Prototype layer record ${path}.${recordId} does not match embedded ID ${explicitId}.`)
        }
      }
      return [recordId, item, itemPath]
    }), useFallback: false }
  }
  const id = invalidLayerEntryId(0)
  issues.push(`Prototype data at ${path} must be an array or object layer collection.`)
  return { entries: [[id, invalidLayerPlaceholder(id, 0), path]], useFallback: false }
}

function invalidLayerEntryId(index: number, sourceId = '') {
  return `${INVALID_LAYER_ENTRY_ID_PREFIX}${index + 1}${sourceId ? `__${sourceId}` : ''}`
}

function invalidLayerPlaceholder(id: string, index: number): Record<string, unknown> {
  return {
    id,
    childIds: [],
    name: `Invalid layer ${index + 1}`,
    layout: {},
    style: {},
    properties: {},
  }
}

function compatibilityIssueValues(value: unknown) {
  return Array.isArray(value)
    ? value.filter((issue): issue is string => typeof issue === 'string' && issue.trim() !== '')
    : []
}

function storedCompatibilityIssues(content: PrototypeContentDto) {
  const raw = content as PrototypeContentWithCompatibilityIssues
  return uniqueStrings(compatibilityIssueValues(raw.__prototypeCompatibilityIssues))
}

function nonNegativeIntegerValue(value: unknown) {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0
}

function positiveIntegerValue(value: unknown) {
  return typeof value === 'number' && Number.isInteger(value) && value > 0
}

function canonicalSha256Hash(value: unknown) {
  return typeof value === 'string' && /^(?:sha256:)?[a-f\d]{64}$/i.test(value.trim())
}

function hasPrototypeLayerCycle(layers: Readonly<Record<string, PrototypeLayerDto>>) {
  const visiting = new Set<string>()
  const visited = new Set<string>()
  const visit = (id: string): boolean => {
    if (visiting.has(id)) return true
    if (visited.has(id) || !layers[id]) return false
    visiting.add(id)
    for (const childId of arrayValue(layers[id].childIds)) {
      if (layers[childId] && visit(childId)) return true
    }
    visiting.delete(id)
    visited.add(id)
    return false
  }
  return Object.keys(layers).some(visit)
}

function hasOwn(value: object, key: string) {
  return Object.prototype.hasOwnProperty.call(value, key)
}

function duplicateLayerId(sourceId: string, index: number, existing: ReadonlySet<string>) {
  const base = `${INVALID_DUPLICATE_LAYER_ID_PREFIX}${index + 1}__${sourceId}`
  let id = base
  let suffix = 1
  while (existing.has(id)) id = `${base}__${++suffix}`
  return id
}

function duplicateLayerSourceId(id: string) {
  if (!id.startsWith(INVALID_DUPLICATE_LAYER_ID_PREFIX)) return id
  const marker = id.indexOf('__', INVALID_DUPLICATE_LAYER_ID_PREFIX.length)
  return marker < 0 ? id : id.slice(marker + 2)
}

function recordValue<T>(value: Readonly<Record<string, T>> | null | undefined) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {} as Readonly<Record<string, T>>
}

function arrayValue<T>(value: readonly T[] | null | undefined): readonly T[] {
  return Array.isArray(value) ? value : []
}

function framePair(stateId: string, breakpointId: string) {
  return `${stateId}\u0000${breakpointId}`
}

function trimmed(value: unknown) {
  return typeof value === 'string' ? value.trim() : ''
}

function nonEmpty(value: unknown) {
  return trimmed(value) !== ''
}

function finiteNonNegative(value: number) {
  return Number.isFinite(value) ? Math.max(0, Math.round(value)) : 0
}

function uniqueStrings(values: readonly string[]) {
  return [...new Set(arrayValue(values).map(trimmed).filter(Boolean))]
}

function defaultId(prefix: 'frame' | 'layer') {
  const suffix = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${suffix}`
}
