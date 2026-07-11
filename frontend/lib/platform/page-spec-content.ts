import type { PageSpecContentDto } from './dto'

export const REQUIRED_PAGE_STATE_KEYS = ['ready', 'loading', 'empty', 'error'] as const

export function normalizePageSpecContent(content: PageSpecContentDto): PageSpecContentDto {
  return {
    blueprintPageNodeId: content.blueprintPageNodeId ?? '',
    title: content.title ?? '',
    route: content.route ?? '',
    userGoal: content.userGoal ?? '',
    entryPoints: uniqueStrings(content.entryPoints ?? []),
    exitPoints: uniqueStrings(content.exitPoints ?? []),
    requiredRoles: uniqueStrings(content.requiredRoles ?? []),
    states: (content.states ?? []).map((state) => ({
      id: state.id,
      key: state.key ?? state.id,
      title: state.title ?? state.key ?? state.id,
      ...(state.description ? { description: state.description } : {}),
      ...(state.entryCondition ? { entryCondition: state.entryCondition } : {}),
      required: Boolean(state.required),
      fixtureIds: uniqueStrings(state.fixtureIds ?? []),
      acceptanceCriterionIds: uniqueStrings(state.acceptanceCriterionIds ?? []),
    })),
    dataBindings: (content.dataBindings ?? []).map((binding) => ({
      id: binding.id,
      name: binding.name ?? '',
      source: binding.source,
      ...(binding.operationId ? { operationId: binding.operationId } : {}),
      ...(binding.schema ? { schema: binding.schema } : {}),
      required: Boolean(binding.required),
    })),
    interactions: (content.interactions ?? []).map((interaction) => ({
      id: interaction.id,
      trigger: interaction.trigger ?? '',
      outcome: interaction.outcome ?? '',
      ...(interaction.targetPageNodeId || interaction.targetPageSpecId
        ? { targetPageNodeId: interaction.targetPageNodeId ?? interaction.targetPageSpecId }
        : {}),
      acceptanceCriterionIds: uniqueStrings(interaction.acceptanceCriterionIds ?? []),
    })),
    acceptanceCriterionIds: uniqueStrings(content.acceptanceCriterionIds ?? []),
    nonFunctionalConstraints: uniqueStrings(content.nonFunctionalConstraints ?? []),
  }
}

export function pageSpecReviewIssues(content: PageSpecContentDto) {
  const value = normalizePageSpecContent(content)
  const issues: string[] = []
  if (!value.blueprintPageNodeId.trim()) issues.push('A stable Blueprint page node ID is required.')
  if (!value.title.trim()) issues.push('Page title is required.')
  if (!value.route.trim() || !value.route.startsWith('/')) {
    issues.push('Route is required and must start with /.')
  }
  if (!value.userGoal.trim()) issues.push('User goal is required.')

  const stateIds = value.states.map((state) => state.id.trim())
  const stateKeys = value.states.map((state) => state.key.trim())
  if (stateIds.some((id) => !id) || new Set(stateIds).size !== stateIds.length) {
    issues.push('Every state needs a unique stable ID.')
  }
  if (stateKeys.some((key) => !key) || new Set(stateKeys).size !== stateKeys.length) {
    issues.push('Every state needs a unique stable key.')
  }
  if (value.states.some((state) => !state.title.trim())) {
    issues.push('Every state needs a title.')
  }
  for (const requiredKey of REQUIRED_PAGE_STATE_KEYS) {
    const state = value.states.find((item) => item.key === requiredKey)
    if (!state) issues.push(`PageSpec must declare the canonical ${requiredKey} state key.`)
    else if (!state.required) issues.push(`Required state ${requiredKey} must be marked required.`)
  }

  const bindingIds = value.dataBindings.map((binding) => binding.id.trim())
  if (bindingIds.some((id) => !id) || new Set(bindingIds).size !== bindingIds.length) {
    issues.push('Every data binding needs a unique stable ID.')
  }
  if (value.dataBindings.some((binding) => !binding.name.trim())) {
    issues.push('Every data binding needs a name.')
  }
  if (value.dataBindings.some((binding) => binding.source === 'api' && !binding.operationId?.trim())) {
    issues.push('API data bindings must name a stable operation ID.')
  }

  const interactionIds = value.interactions.map((interaction) => interaction.id.trim())
  if (interactionIds.some((id) => !id) || new Set(interactionIds).size !== interactionIds.length) {
    issues.push('Every interaction needs a unique stable ID.')
  }
  if (value.interactions.some((interaction) =>
    !interaction.trigger.trim() || !interaction.outcome.trim(),
  )) {
    issues.push('Every interaction needs both a trigger and an outcome.')
  }
  if (value.acceptanceCriterionIds.length === 0) {
    issues.push('Trace the PageSpec to at least one stable acceptance criterion ID.')
  }
  return issues
}

function uniqueStrings(values: readonly string[]) {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}
