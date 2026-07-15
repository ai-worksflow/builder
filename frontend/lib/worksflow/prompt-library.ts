import type { GenerationMode } from '@/lib/generation/types'
import type { MessageKey, MessageValues } from '@/lib/i18n'

type PromptTranslator = (key: MessageKey, values?: MessageValues) => string

export type PromptRunStatus = 'completed' | 'failed' | 'cancelled'

export interface PromptHistoryEntry {
  id: string
  prompt: string
  mode: GenerationMode
  model: string
  status: PromptRunStatus
  createdAt: string
}

export interface PromptTemplate {
  id: string
  title: string
  description: string
  prompt: string
  tags: string[]
  mode: GenerationMode
  builtIn?: boolean
}

export interface PromptWorkflowStep {
  id: string
  title: string
  prompt: string
  mode: GenerationMode
}

export interface PromptWorkflow {
  id: string
  title: string
  description: string
  tags: string[]
  steps: PromptWorkflowStep[]
  builtIn?: boolean
}

export interface SlashCommand {
  command: string
  title: string
  description: string
  prompt: string
  mode: GenerationMode
}

export const BUILT_IN_PROMPT_TEMPLATES: PromptTemplate[] = [
  {
    id: 'template-landing-page',
    title: 'Launch landing page',
    description: 'Build a responsive landing page with clear conversion paths.',
    prompt:
      'Build a polished responsive product landing page with a hero, proof points, feature grid, pricing, FAQ, accessible navigation, and a focused call to action.',
    tags: ['marketing', 'landing', 'responsive'],
    mode: 'build',
    builtIn: true,
  },
  {
    id: 'template-dashboard',
    title: 'Operations dashboard',
    description: 'Create a dense but accessible dashboard with filters and states.',
    prompt:
      'Build an operations dashboard with summary metrics, searchable records, filters, an empty state, loading state, error state, and responsive detail panels.',
    tags: ['dashboard', 'data', 'responsive'],
    mode: 'build',
    builtIn: true,
  },
  {
    id: 'template-accessibility',
    title: 'Accessibility repair',
    description: 'Audit and repair keyboard, semantics, contrast, and focus behavior.',
    prompt:
      'Audit the current workspace for accessibility problems. Repair semantic structure, labels, keyboard navigation, focus visibility, contrast, reduced-motion support, and responsive touch targets.',
    tags: ['a11y', 'quality', 'repair'],
    mode: 'fix',
    builtIn: true,
  },
  {
    id: 'template-test-repair',
    title: 'Quality gate repair',
    description: 'Fix attached build, lint, type, test, dependency, and secret diagnostics.',
    prompt:
      'Review the attached quality diagnostics, identify root causes, and repair every actionable build, type, lint, test, accessibility, dependency, and secret finding without regressing existing behavior.',
    tags: ['quality', 'tests', 'repair'],
    mode: 'fix',
    builtIn: true,
  },
]

export const BUILT_IN_PROMPT_WORKFLOWS: PromptWorkflow[] = [
  {
    id: 'workflow-production-feature',
    title: 'Production feature loop',
    description: 'Plan, implement, verify, and repair a complete feature in one reusable brief.',
    tags: ['plan', 'build', 'quality'],
    builtIn: true,
    steps: [
      { id: 'plan', title: 'Plan', mode: 'plan', prompt: 'Analyze requirements, risks, affected files, and acceptance criteria.' },
      { id: 'build', title: 'Implement', mode: 'build', prompt: 'Implement every accepted requirement as a complete runnable change.' },
      { id: 'verify', title: 'Verify', mode: 'fix', prompt: 'Run the supported quality gates, repair findings, and summarize remaining risks.' },
    ],
  },
  {
    id: 'workflow-accessible-release',
    title: 'Accessible release',
    description: 'Build a responsive experience, audit accessibility, then prepare it for release.',
    tags: ['a11y', 'responsive', 'release'],
    builtIn: true,
    steps: [
      { id: 'experience', title: 'Build experience', mode: 'build', prompt: 'Build the requested responsive user experience with loading, empty, error, and success states.' },
      { id: 'a11y', title: 'Accessibility pass', mode: 'fix', prompt: 'Repair semantics, keyboard flow, focus, contrast, reduced motion, and touch targets.' },
      { id: 'release', title: 'Release pass', mode: 'fix', prompt: 'Remove secrets, validate exports, and make the workspace pass every supported release check.' },
    ],
  },
]

export const SLASH_COMMANDS: SlashCommand[] = [
  {
    command: '/plan',
    title: 'Plan a change',
    description: 'Analyze the workspace and return an implementation plan.',
    prompt: 'Plan this change before editing files: ',
    mode: 'plan',
  },
  {
    command: '/build',
    title: 'Build from a request',
    description: 'Create a complete runnable implementation.',
    prompt: 'Build a complete runnable implementation for: ',
    mode: 'build',
  },
  {
    command: '/iterate',
    title: 'Iterate on the preview',
    description: 'Improve the current workspace while preserving working behavior.',
    prompt: 'Iterate on the current workspace and preserve working behavior while you: ',
    mode: 'iterate',
  },
  {
    command: '/fix',
    title: 'Repair a problem',
    description: 'Use attached errors and diagnostics to make a focused repair.',
    prompt: 'Diagnose and fix this problem using the attached runtime and quality context: ',
    mode: 'fix',
  },
  {
    command: '/a11y',
    title: 'Accessibility audit',
    description: 'Review and repair keyboard and semantic accessibility.',
    prompt: BUILT_IN_PROMPT_TEMPLATES[2].prompt,
    mode: 'fix',
  },
  {
    command: '/quality',
    title: 'Repair quality findings',
    description: 'Apply the latest structured quality diagnostics.',
    prompt: BUILT_IN_PROMPT_TEMPLATES[3].prompt,
    mode: 'fix',
  },
]

export function localizePromptTemplates(
  templates: PromptTemplate[],
  t: PromptTranslator,
): PromptTemplate[] {
  return templates.map((template) => {
    if (!template.builtIn) return template

    const prefix =
      template.id === 'template-landing-page'
        ? 'composer.template.landing'
        : template.id === 'template-dashboard'
          ? 'composer.template.dashboard'
          : template.id === 'template-accessibility'
            ? 'composer.template.accessibility'
            : template.id === 'template-test-repair'
              ? 'composer.template.quality'
              : null
    if (!prefix) return template

    return {
      ...template,
      title: t(`${prefix}.title` as MessageKey),
      description: t(`${prefix}.description` as MessageKey),
      prompt: t(`${prefix}.prompt` as MessageKey),
    }
  })
}

export function localizePromptWorkflows(
  workflows: PromptWorkflow[],
  t: PromptTranslator,
): PromptWorkflow[] {
  return workflows.map((workflow) => {
    if (!workflow.builtIn) return workflow
    if (workflow.id === 'workflow-production-feature') {
      const stepKeys = ['plan', 'build', 'verify'] as const
      return {
        ...workflow,
        title: t('composer.workflow.production.title'),
        description: t('composer.workflow.production.description'),
        steps: workflow.steps.map((step, index) => ({
          ...step,
          title: t(`composer.workflow.production.${stepKeys[index]}.title` as MessageKey),
          prompt: t(`composer.workflow.production.${stepKeys[index]}.prompt` as MessageKey),
        })),
      }
    }
    if (workflow.id === 'workflow-accessible-release') {
      const stepKeys = ['build', 'a11y', 'release'] as const
      return {
        ...workflow,
        title: t('composer.workflow.accessible.title'),
        description: t('composer.workflow.accessible.description'),
        steps: workflow.steps.map((step, index) => ({
          ...step,
          title: t(`composer.workflow.accessible.${stepKeys[index]}.title` as MessageKey),
          prompt: t(`composer.workflow.accessible.${stepKeys[index]}.prompt` as MessageKey),
        })),
      }
    }
    return workflow
  })
}

export function localizeSlashCommands(t: PromptTranslator): SlashCommand[] {
  return SLASH_COMMANDS.map((item) => {
    const command = item.command.slice(1)
    const promptKey =
      command === 'a11y'
        ? 'composer.template.accessibility.prompt'
        : command === 'quality'
          ? 'composer.template.quality.prompt'
          : `composer.command.${command}.prompt`
    return {
      ...item,
      title: t(`composer.command.${command}.title` as MessageKey),
      description: t(`composer.command.${command}.description` as MessageKey),
      prompt: `${t(promptKey as MessageKey)} `,
    }
  })
}

export function redactSensitivePrompt(value: string) {
  return value
    .replace(/\b(?:sk-[a-z0-9_-]{12,}|gh[opusr]_[a-z0-9]{12,})\b/gi, '[REDACTED]')
    .replace(/(Bearer\s+)[A-Za-z0-9._~+/-]{12,}/gi, '$1[REDACTED]')
    .replace(
      /((?:secret|token|password|credential|private.?key|api.?key|connection.?string|authorization|cookie|signature)\s*[=:]\s*)[^\s,;&]+/gi,
      '$1[REDACTED]',
    )
}

export function searchPromptTemplates(
  templates: PromptTemplate[],
  query: string,
): PromptTemplate[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return templates
  return templates.filter((template) =>
    [template.title, template.description, template.prompt, ...template.tags]
      .join(' ')
      .toLowerCase()
      .includes(normalized),
  )
}

export function searchPromptWorkflows(workflows: PromptWorkflow[], query: string) {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return workflows
  return workflows.filter((workflow) =>
    [
      workflow.title,
      workflow.description,
      ...workflow.tags,
      ...workflow.steps.flatMap((step) => [step.title, step.prompt, step.mode]),
    ].join(' ').toLowerCase().includes(normalized),
  )
}

export function workflowPrompt(workflow: PromptWorkflow, t?: PromptTranslator) {
  return [
    t
      ? t('composer.workflow.executeInOrder', { title: workflow.title })
      : `Execute the reusable workflow "${workflow.title}" in order.`,
    ...workflow.steps.map(
      (step, index) =>
        `${index + 1}. ${step.title} [${step.mode}]: ${redactSensitivePrompt(step.prompt)}`,
    ),
    t
      ? t('composer.workflow.completeEveryStep')
      : 'Complete every step in this run and report verification evidence.',
  ].join('\n')
}

export function searchPromptHistory(
  history: PromptHistoryEntry[],
  query: string,
): PromptHistoryEntry[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return history
  return history.filter((entry) =>
    [entry.prompt, entry.mode, entry.model, entry.status]
      .join(' ')
      .toLowerCase()
      .includes(normalized),
  )
}

export function suggestSlashCommands(
  value: string,
  commands: SlashCommand[] = SLASH_COMMANDS,
): SlashCommand[] {
  const trimmed = value.trimStart().toLowerCase()
  if (!trimmed.startsWith('/') || trimmed.includes(' ')) return []
  return commands.filter((item) => item.command.startsWith(trimmed))
}

export function applySlashCommand(
  value: string,
  command: SlashCommand,
): { prompt: string; mode: GenerationMode } {
  const suffix = value.trimStart().slice(command.command.length).trimStart()
  return {
    prompt: `${command.prompt}${suffix}`.trimEnd(),
    mode: command.mode,
  }
}

export function addPromptHistoryEntry(
  history: PromptHistoryEntry[],
  entry: PromptHistoryEntry,
  limit = 100,
): PromptHistoryEntry[] {
  const withoutDuplicate = history.filter((item) => item.id !== entry.id)
  return [{ ...entry, prompt: redactSensitivePrompt(entry.prompt) }, ...withoutDuplicate]
    .slice(0, Math.max(1, limit))
}

export function isPromptHistory(value: unknown): value is PromptHistoryEntry[] {
  if (!Array.isArray(value)) return false
  return value.every((entry) => {
    if (!entry || typeof entry !== 'object') return false
    const item = entry as Record<string, unknown>
    return (
      typeof item.id === 'string' &&
      typeof item.prompt === 'string' &&
      item.prompt.length <= 100_000 &&
      ['plan', 'build', 'iterate', 'fix'].includes(String(item.mode)) &&
      typeof item.model === 'string' &&
      ['completed', 'failed', 'cancelled'].includes(String(item.status)) &&
      typeof item.createdAt === 'string'
    )
  })
}

export function isPromptTemplateList(value: unknown): value is PromptTemplate[] {
  if (!Array.isArray(value)) return false
  return value.every((template) => {
    if (!template || typeof template !== 'object') return false
    const item = template as Record<string, unknown>
    return (
      typeof item.id === 'string' &&
      typeof item.title === 'string' &&
      typeof item.description === 'string' &&
      typeof item.prompt === 'string' &&
      item.prompt.length <= 100_000 &&
      Array.isArray(item.tags) &&
      item.tags.every((tag) => typeof tag === 'string') &&
      ['plan', 'build', 'iterate', 'fix'].includes(String(item.mode))
    )
  })
}

export function isPromptWorkflowList(value: unknown): value is PromptWorkflow[] {
  if (!Array.isArray(value)) return false
  return value.every((workflow) => {
    if (!workflow || typeof workflow !== 'object') return false
    const item = workflow as Record<string, unknown>
    return (
      typeof item.id === 'string' &&
      typeof item.title === 'string' &&
      typeof item.description === 'string' &&
      Array.isArray(item.tags) &&
      item.tags.every((tag) => typeof tag === 'string') &&
      Array.isArray(item.steps) &&
      item.steps.length > 0 &&
      item.steps.length <= 12 &&
      item.steps.every((step) => {
        if (!step || typeof step !== 'object') return false
        const candidate = step as Record<string, unknown>
        return (
          typeof candidate.id === 'string' &&
          typeof candidate.title === 'string' &&
          typeof candidate.prompt === 'string' &&
          candidate.prompt.length <= 24_000 &&
          ['plan', 'build', 'iterate', 'fix'].includes(String(candidate.mode))
        )
      })
    )
  })
}
