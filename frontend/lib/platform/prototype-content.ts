import type {
  PrototypeActionDto,
  PrototypeBreakpointDto,
  PrototypeContentDto,
  PrototypeFrameDto,
  PrototypeLayerDto,
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

export function prototypeReviewIssues(content: PrototypeContentDto) {
  const issues: string[] = []
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
  const seenLayerIds = new Set<string>()
  for (const [recordId, layer] of layerEntries) {
    const id = trimmed(recordId)
    const embeddedId = trimmed(layer.id)
    if (!id || seenLayerIds.has(id) || (embeddedId && embeddedId !== id)) {
      issues.push(`Layer ${recordId || '(missing ID)'} does not have one unique stable record ID.`)
    }
    seenLayerIds.add(id)
  }
  for (const [id, layer] of layerEntries) {
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
  }

  const frames = arrayValue(content.frames)
  if (frames.length === 0) {
    issues.push('Prototype must define a frame for each required state and breakpoint.')
  } else {
    const coverage = new Set<string>()
    frames.forEach((frame, index) => {
      const stateId = trimmed(frame.stateId)
      const breakpointId = trimmed(frame.breakpointId)
      const rootLayerId = trimmed(frame.rootLayerId)
      const pair = framePair(stateId, breakpointId)
      if (!nonEmpty(frame.id)
        || !stateIds.has(stateId)
        || !breakpointIds.has(breakpointId)
        || !layers[rootLayerId]) {
        issues.push(`Frame ${index + 1} must reference an existing state, breakpoint, and root layer.`)
      }
      if (coverage.has(pair)) {
        issues.push(`Frame ${index + 1} duplicates a state and breakpoint pair.`)
      }
      coverage.add(pair)
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

  arrayValue(content.fixtures).forEach((fixture, index) => {
    if (fixture.sanitized !== true) issues.push(`Fixture ${index + 1} must be marked sanitized.`)
    if (!stateIds.has(trimmed(fixture.stateId))) {
      issues.push(`Fixture ${index + 1} must reference an existing state.`)
    }
  })

  arrayValue(content.interactions).forEach((interaction, index) => {
    const trigger = trimmed(interaction.trigger)
    if (!nonEmpty(interaction.id)
      || !layers[trimmed(interaction.sourceLayerId)]
      || !ALLOWED_PROTOTYPE_TRIGGERS.has(trigger)) {
      issues.push(`Interaction ${index + 1} needs a stable ID, existing source layer, and declarative trigger.`)
    }
    arrayValue(interaction.actions).forEach((action, actionIndex) => {
      if (!ALLOWED_PROTOTYPE_ACTIONS.has(actionType(action))) {
        issues.push(`Interaction ${index + 1} action ${actionIndex + 1} is not on the declarative action whitelist.`)
      }
    })
  })
  return issues
}

export function prototypeFrameCoverageGaps(content: PrototypeContentDto) {
  const coverage = new Set(arrayValue(content.frames).map((frame) =>
    framePair(trimmed(frame.stateId), trimmed(frame.breakpointId)),
  ))
  return arrayValue(content.states).flatMap((state) =>
    arrayValue(content.breakpoints).flatMap((breakpoint) =>
      coverage.has(framePair(trimmed(state.id), trimmed(breakpoint.id)))
        ? []
        : [{ stateId: state.id, breakpointId: breakpoint.id }],
    ),
  )
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
    viewportWidth: Math.max(1, finiteNonNegative(breakpoint.viewportWidth)),
    viewportHeight: Math.max(1, finiteNonNegative(breakpoint.viewportHeight)),
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
