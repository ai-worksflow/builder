import {
  GENERATION_MODES,
  type GeneratedFile,
  type GeneratedWorkspace,
  type GenerationStreamEvent,
  type GenerationAttachmentInput,
  type GenerationFileInput,
  type GenerationMode,
  type GenerationPlan,
  type GenerationPlanTask,
  type GenerationRequest,
  type JsonValue,
} from './types'
import {
  containsSensitiveGenerationText,
  redactSensitiveGenerationText,
  sanitizeGenerationUrl,
} from './redaction'

export const MAX_REQUEST_BODY_BYTES = 10_000_000
export const MAX_PROMPT_LENGTH = 24_000
export const MAX_CONTEXT_BYTES = 96_000
export const MAX_FILE_COUNT = 80
export const MAX_FILE_CONTENT_LENGTH = 400_000
export const MAX_TOTAL_FILE_CONTENT_LENGTH = 1_200_000
export const MAX_GENERATED_FILE_CONTENT_LENGTH = 800_000
export const MAX_TOTAL_GENERATED_CONTENT_LENGTH = 3_000_000
export const MAX_ATTACHMENT_COUNT = 12
export const MAX_ATTACHMENT_TEXT_LENGTH = 32_000
export const MAX_TOTAL_ATTACHMENT_TEXT_LENGTH = 96_000
export const MAX_IMAGE_DATA_URL_LENGTH = 7_500_000

const MAX_PATH_LENGTH = 240
const MAX_MODEL_LENGTH = 80
const MAX_TASK_COUNT = 24
const MAX_JSON_DEPTH = 10
const FORBIDDEN_PATH_SEGMENTS = new Set(['.git', '.next', 'node_modules'])
const FORBIDDEN_CONTEXT_KEYS = new Set(['__proto__', 'constructor', 'prototype'])

export class GenerationValidationError extends Error {
  readonly issues: string[]

  constructor(issues: string | string[]) {
    const normalizedIssues = Array.isArray(issues) ? issues : [issues]
    super(normalizedIssues.join('; '))
    this.name = 'GenerationValidationError'
    this.issues = normalizedIssues
  }
}

function invalid(message: string): never {
  throw new GenerationValidationError(message)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function containsControlCharacter(value: string) {
  return Array.from(value).some((character) => character.charCodeAt(0) < 32)
}

function requiredString(
  value: unknown,
  field: string,
  options: { max: number; trim?: boolean; allowEmpty?: boolean },
) {
  if (typeof value !== 'string') invalid(`${field} must be a string`)
  const normalized = options.trim === false ? value : value.trim()
  if (!options.allowEmpty && normalized.length === 0) invalid(`${field} cannot be empty`)
  if (normalized.length > options.max) invalid(`${field} exceeds ${options.max} characters`)
  if (/\0/.test(normalized)) invalid(`${field} contains a null byte`)
  return normalized
}

export function sanitizeFilePath(value: unknown, field = 'path') {
  const rawPath = requiredString(value, field, { max: MAX_PATH_LENGTH })
  if (/^[a-zA-Z]:[\\/]/.test(rawPath) || rawPath.startsWith('/') || rawPath.startsWith('~')) {
    invalid(`${field} must be a relative workspace path`)
  }

  const normalized = rawPath
    .replace(/\\/g, '/')
    .replace(/^\.\//, '')
    .replace(/\/{2,}/g, '/')
  const segments = normalized.split('/')

  if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
    invalid(`${field} contains an unsafe path segment`)
  }
  if (segments.some((segment) => FORBIDDEN_PATH_SEGMENTS.has(segment.toLowerCase()))) {
    invalid(`${field} points to a generated or dependency directory`)
  }
  if (segments.some((segment) => /^\.env(?:\.|$)/i.test(segment))) {
    invalid(`${field} may not include environment secret files`)
  }
  if (segments.some((segment) => /[<>:"|?*]/.test(segment) || containsControlCharacter(segment))) {
    invalid(`${field} contains unsupported filename characters`)
  }
  if (normalized.length > MAX_PATH_LENGTH) invalid(`${field} exceeds ${MAX_PATH_LENGTH} characters`)

  return normalized
}

function parseLanguage(value: unknown, field: string, optional: boolean) {
  if (value === undefined && optional) return undefined
  const language = requiredString(value, field, { max: 40 })
  if (!/^[a-zA-Z0-9_+#.-]+$/.test(language)) invalid(`${field} is not a valid language identifier`)
  return language.toLowerCase()
}

function parseCurrentFile(value: unknown, index: number): GenerationFileInput {
  if (!isRecord(value)) invalid(`currentFiles[${index}] must be an object`)
  return {
    path: sanitizeFilePath(value.path, `currentFiles[${index}].path`),
    content: redactSensitiveGenerationText(requiredString(value.content, `currentFiles[${index}].content`, {
      max: MAX_FILE_CONTENT_LENGTH,
      trim: false,
      allowEmpty: true,
    })),
    language: parseLanguage(value.language, `currentFiles[${index}].language`, true),
  }
}

function parseAttachment(value: unknown, index: number): GenerationAttachmentInput {
  if (!isRecord(value)) invalid(`attachments[${index}] must be an object`)
  const kind = value.kind
  if (kind !== 'text' && kind !== 'image' && kind !== 'url') {
    invalid(`attachments[${index}].kind must be text, image or url`)
  }
  const name = requiredString(value.name, `attachments[${index}].name`, { max: 180 })
  const mimeType =
    value.mimeType === undefined
      ? undefined
      : requiredString(value.mimeType, `attachments[${index}].mimeType`, { max: 120 })

  if (kind === 'image') {
    const content = requiredString(value.content, `attachments[${index}].content`, {
      max: MAX_IMAGE_DATA_URL_LENGTH,
      trim: false,
    })
    if (!/^data:image\/(?:png|jpe?g|webp|gif);base64,[a-zA-Z0-9+/=\s]+$/.test(content)) {
      invalid(`attachments[${index}].content must be a supported base64 image data URL`)
    }
    return { kind, name, content, mimeType }
  }

  let content = requiredString(value.content, `attachments[${index}].content`, {
    max: kind === 'url' ? 2_048 : MAX_ATTACHMENT_TEXT_LENGTH,
    trim: kind === 'url',
  })
  if (kind === 'url') {
    let url: URL
    try {
      url = new URL(content)
    } catch {
      invalid(`attachments[${index}].content must be a valid URL`)
    }
    if (url.protocol !== 'https:' && url.protocol !== 'http:') {
      invalid(`attachments[${index}].content must use http or https`)
    }
    try {
      content = sanitizeGenerationUrl(content)
    } catch {
      invalid(`attachments[${index}].content may not contain URL credentials`)
    }
  } else {
    content = redactSensitiveGenerationText(content)
  }
  return { kind, name, content, mimeType }
}

function sanitizeJson(value: unknown, field: string, depth = 0): JsonValue {
  if (depth > MAX_JSON_DEPTH) invalid(`${field} exceeds the maximum nesting depth`)
  if (value === null || typeof value === 'boolean') return value
  if (typeof value === 'string') return redactSensitiveGenerationText(value)
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) invalid(`${field} contains a non-finite number`)
    return value
  }
  if (Array.isArray(value)) {
    return value.map((item, index) => sanitizeJson(item, `${field}[${index}]`, depth + 1))
  }
  if (!isRecord(value)) invalid(`${field} must contain JSON-compatible values only`)

  const sanitized: { [key: string]: JsonValue } = {}
  Object.entries(value).forEach(([key, item]) => {
    if (FORBIDDEN_CONTEXT_KEYS.has(key)) invalid(`${field} contains a forbidden key`)
    if (key.length === 0 || key.length > 120) invalid(`${field} contains an invalid key`)
    sanitized[key] = sanitizeJson(item, `${field}.${key}`, depth + 1)
  })
  return sanitized
}

function parseMode(value: unknown): GenerationMode {
  if (typeof value !== 'string' || !GENERATION_MODES.includes(value as GenerationMode)) {
    invalid(`mode must be one of: ${GENERATION_MODES.join(', ')}`)
  }
  return value as GenerationMode
}

function parseModel(value: unknown) {
  if (value === undefined || value === null || value === '') return undefined
  const model = requiredString(value, 'model', { max: MAX_MODEL_LENGTH })
  if (!/^[a-zA-Z0-9][a-zA-Z0-9._:-]*$/.test(model)) invalid('model contains unsupported characters')
  return model
}

function parseProjectId(value: unknown) {
  if (value === undefined || value === null || value === '') return undefined
  const projectId = requiredString(value, 'projectId', { max: 64 })
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(projectId)) {
    invalid('projectId contains unsupported characters')
  }
  return projectId
}

export function parseGenerationRequest(value: unknown): GenerationRequest {
  if (!isRecord(value)) invalid('request body must be a JSON object')

  const filesValue = value.currentFiles ?? value.files ?? []
  if (!Array.isArray(filesValue)) invalid('currentFiles must be an array')
  if (filesValue.length > MAX_FILE_COUNT) invalid(`currentFiles may contain at most ${MAX_FILE_COUNT} files`)

  const currentFiles = filesValue.map(parseCurrentFile)
  const uniquePaths = new Set(currentFiles.map((file) => file.path.toLowerCase()))
  if (uniquePaths.size !== currentFiles.length) invalid('currentFiles contains duplicate paths')

  const totalFileLength = currentFiles.reduce((total, file) => total + file.content.length, 0)
  if (totalFileLength > MAX_TOTAL_FILE_CONTENT_LENGTH) {
    invalid(`currentFiles exceeds ${MAX_TOTAL_FILE_CONTENT_LENGTH} total characters`)
  }

  const context = value.context === undefined ? undefined : sanitizeJson(value.context, 'context')
  if (context !== undefined) {
    const contextBytes = new TextEncoder().encode(JSON.stringify(context)).byteLength
    if (contextBytes > MAX_CONTEXT_BYTES) invalid(`context exceeds ${MAX_CONTEXT_BYTES} bytes`)
  }

  const attachmentValues = value.attachments ?? []
  if (!Array.isArray(attachmentValues)) invalid('attachments must be an array')
  if (attachmentValues.length > MAX_ATTACHMENT_COUNT) {
    invalid(`attachments may contain at most ${MAX_ATTACHMENT_COUNT} items`)
  }
  const attachments = attachmentValues.map(parseAttachment)
  const totalAttachmentText = attachments
    .filter((attachment) => attachment.kind !== 'image')
    .reduce((total, attachment) => total + attachment.content.length, 0)
  if (totalAttachmentText > MAX_TOTAL_ATTACHMENT_TEXT_LENGTH) {
    invalid(`attachments exceeds ${MAX_TOTAL_ATTACHMENT_TEXT_LENGTH} text characters`)
  }

  return {
    projectId: parseProjectId(value.projectId),
    prompt: redactSensitiveGenerationText(requiredString(value.prompt, 'prompt', { max: MAX_PROMPT_LENGTH })),
    mode: parseMode(value.mode),
    model: parseModel(value.model),
    currentFiles,
    attachments,
    context,
  }
}

function parsePlanTask(value: unknown, index: number): GenerationPlanTask {
  if (!isRecord(value)) invalid(`plan.tasks[${index}] must be an object`)
  const id = requiredString(value.id, `plan.tasks[${index}].id`, { max: 64 })
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/.test(id)) invalid(`plan.tasks[${index}].id is invalid`)
  return {
    id,
    title: requiredString(value.title, `plan.tasks[${index}].title`, { max: 180 }),
    description: requiredString(value.description, `plan.tasks[${index}].description`, { max: 800 }),
  }
}

function parsePlan(value: unknown): GenerationPlan {
  if (!isRecord(value)) invalid('plan must be an object')
  if (!Array.isArray(value.tasks) || value.tasks.length === 0) invalid('plan.tasks must be a non-empty array')
  if (value.tasks.length > MAX_TASK_COUNT) invalid(`plan.tasks may contain at most ${MAX_TASK_COUNT} tasks`)

  const tasks = value.tasks.map(parsePlanTask)
  if (new Set(tasks.map((task) => task.id)).size !== tasks.length) invalid('plan.tasks contains duplicate ids')

  return {
    title: requiredString(value.title, 'plan.title', { max: 180 }),
    summary: requiredString(value.summary, 'plan.summary', { max: 1200 }),
    tasks,
  }
}

function parseGeneratedFile(value: unknown, index: number): GeneratedFile {
  if (!isRecord(value)) invalid(`files[${index}] must be an object`)
  const content = requiredString(value.content, `files[${index}].content`, {
    max: MAX_GENERATED_FILE_CONTENT_LENGTH,
    trim: false,
    allowEmpty: true,
  })
  if (containsSensitiveGenerationText(content)) {
    invalid(`files[${index}].content appears to contain a credential`)
  }
  return {
    path: sanitizeFilePath(value.path, `files[${index}].path`),
    content,
    language: parseLanguage(value.language, `files[${index}].language`, false) as string,
  }
}

export function parseGeneratedWorkspace(value: unknown): GeneratedWorkspace {
  if (!isRecord(value)) invalid('generated workspace must be an object')
  if (!Array.isArray(value.files) || value.files.length === 0) invalid('files must be a non-empty array')
  if (value.files.length > MAX_FILE_COUNT) invalid(`files may contain at most ${MAX_FILE_COUNT} files`)

  const files = value.files.map(parseGeneratedFile)
  if (new Set(files.map((file) => file.path.toLowerCase())).size !== files.length) {
    invalid('files contains duplicate paths')
  }
  const totalContentLength = files.reduce((total, file) => total + file.content.length, 0)
  if (totalContentLength > MAX_TOTAL_GENERATED_CONTENT_LENGTH) {
    invalid(`files exceeds ${MAX_TOTAL_GENERATED_CONTENT_LENGTH} total characters`)
  }

  return {
    plan: parsePlan(value.plan),
    files,
    summary: requiredString(value.summary, 'summary', { max: 1600 }),
  }
}

export const GENERATED_WORKSPACE_JSON_SCHEMA: Record<string, unknown> = {
  type: 'object',
  additionalProperties: false,
  required: ['plan', 'files', 'summary'],
  properties: {
    plan: {
      type: 'object',
      additionalProperties: false,
      required: ['title', 'summary', 'tasks'],
      properties: {
        title: { type: 'string', minLength: 1, maxLength: 180 },
        summary: { type: 'string', minLength: 1, maxLength: 1200 },
        tasks: {
          type: 'array',
          minItems: 1,
          maxItems: MAX_TASK_COUNT,
          items: {
            type: 'object',
            additionalProperties: false,
            required: ['id', 'title', 'description'],
            properties: {
              id: { type: 'string', minLength: 1, maxLength: 64, pattern: '^[a-zA-Z0-9][a-zA-Z0-9_-]*$' },
              title: { type: 'string', minLength: 1, maxLength: 180 },
              description: { type: 'string', minLength: 1, maxLength: 800 },
            },
          },
        },
      },
    },
    files: {
      type: 'array',
      minItems: 1,
      maxItems: MAX_FILE_COUNT,
      items: {
        type: 'object',
        additionalProperties: false,
        required: ['path', 'content', 'language'],
        properties: {
          path: { type: 'string', minLength: 1, maxLength: MAX_PATH_LENGTH },
          content: { type: 'string', maxLength: MAX_GENERATED_FILE_CONTENT_LENGTH },
          language: { type: 'string', minLength: 1, maxLength: 40 },
        },
      },
    },
    summary: { type: 'string', minLength: 1, maxLength: 1600 },
  },
}

const ndjsonEncoder = new TextEncoder()

export function encodeNdjsonEvent(event: GenerationStreamEvent) {
  return ndjsonEncoder.encode(`${JSON.stringify(event)}\n`)
}
