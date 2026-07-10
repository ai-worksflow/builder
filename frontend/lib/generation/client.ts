import type {
  GenerationErrorCategory,
  GenerationEvent,
  GenerationLifecycleEvent,
  GenerationRequest,
  GenerationResult,
  GenerationStreamEvent,
} from './types'

export class GenerationClientError extends Error {
  readonly code: string
  readonly status?: number
  readonly retryable: boolean
  readonly category?: GenerationErrorCategory
  readonly retryAfterSeconds?: number
  readonly action?: string

  constructor(
    message: string,
    options: {
      code?: string
      status?: number
      retryable?: boolean
      category?: GenerationErrorCategory
      retryAfterSeconds?: number
      action?: string
    } = {},
  ) {
    super(message)
    this.name = 'GenerationClientError'
    this.code = options.code ?? 'generation_client_error'
    this.status = options.status
    this.retryable = options.retryable ?? false
    this.category = options.category
    this.retryAfterSeconds = options.retryAfterSeconds
    this.action = options.action
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

export function isGenerationEvent(value: unknown): value is GenerationEvent {
  if (
    !isRecord(value) ||
    typeof value.type !== 'string' ||
    typeof value.runId !== 'string' ||
    value.runId.length === 0 ||
    typeof value.sequence !== 'number' ||
    typeof value.timestamp !== 'string'
  ) {
    return false
  }

  if (value.type === 'plan') return isRecord(value.plan) && typeof value.provider === 'string'
  if (value.type === 'task') return isRecord(value.task) && typeof value.status === 'string'
  if (value.type === 'file') return isRecord(value.file) && typeof value.provider === 'string'
  if (value.type === 'log') return typeof value.level === 'string' && typeof value.message === 'string'
  if (value.type === 'result') return isRecord(value.result)
  if (value.type === 'error') return isRecord(value.error) && typeof value.error.message === 'string'
  return false
}

export function isGenerationLifecycleEvent(value: unknown): value is GenerationLifecycleEvent {
  if (
    !isRecord(value) ||
    value.type !== 'lifecycle' ||
    typeof value.runId !== 'string' ||
    value.runId.length === 0 ||
    typeof value.sequence !== 'number' ||
    typeof value.timestamp !== 'string'
  ) {
    return false
  }
  return (
    value.status === 'started' ||
    value.status === 'completed' ||
    value.status === 'cancelled' ||
    value.status === 'failed'
  )
}

export function isGenerationStreamEvent(value: unknown): value is GenerationStreamEvent {
  return isGenerationEvent(value) || isGenerationLifecycleEvent(value)
}

export class NdjsonGenerationParser {
  private buffer = ''
  private readonly decoder = new TextDecoder()

  push(chunk: Uint8Array) {
    this.buffer += this.decoder.decode(chunk, { stream: true })
    return this.readCompleteLines()
  }

  finish() {
    this.buffer += this.decoder.decode()
    const events = this.readCompleteLines()
    const remainder = this.buffer.trim()
    this.buffer = ''
    if (!remainder) return events
    return [...events, parseEventLine(remainder)]
  }

  private readCompleteLines() {
    const lines = this.buffer.split('\n')
    this.buffer = lines.pop() ?? ''
    return lines.filter((line) => line.trim()).map(parseEventLine)
  }
}

function parseEventLine(line: string) {
  let value: unknown
  try {
    value = JSON.parse(line)
  } catch {
    throw new GenerationClientError('The generation stream returned invalid JSON.', {
      code: 'invalid_stream_json',
      retryable: true,
    })
  }
  if (!isGenerationStreamEvent(value)) {
    throw new GenerationClientError('The generation stream returned an unknown event.', {
      code: 'invalid_stream_event',
      retryable: true,
    })
  }
  return value
}

export async function streamGeneration(
  request: GenerationRequest,
  options: {
    signal?: AbortSignal
    onEvent?: (event: GenerationEvent) => void
    onLifecycle?: (event: GenerationLifecycleEvent) => void
  } = {},
) {
  let response: Response
  try {
    response = await fetch('/api/generate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(request),
      signal: options.signal,
    })
  } catch (error) {
    if (options.signal?.aborted) throw error
    throw new GenerationClientError(
      error instanceof Error ? error.message : 'Unable to reach the generation service.',
      { code: 'generation_unreachable', retryable: true },
    )
  }

  if (!response.ok) {
    const detail = await readErrorResponse(response)
    const headerRetryAfter = parseRetryAfter(response.headers.get('retry-after'))
    throw new GenerationClientError(detail.message, {
      code: detail.code,
      status: response.status,
      retryable:
        detail.retryable ??
        (response.status === 408 || response.status === 409 || response.status === 429 || response.status >= 500),
      category: detail.category,
      retryAfterSeconds: detail.retryAfterSeconds ?? headerRetryAfter,
      action: detail.action,
    })
  }
  if (!response.body) {
    throw new GenerationClientError('The generation service returned an empty stream.', {
      code: 'empty_generation_stream',
      retryable: true,
    })
  }

  const reader = response.body.getReader()
  const parser = new NdjsonGenerationParser()
  let result: GenerationResult | undefined

  const handleEvent = (event: GenerationStreamEvent) => {
    if (event.type === 'lifecycle') {
      options.onLifecycle?.(event)
      return
    }
    options.onEvent?.(event)
    if (event.type === 'result') result = event.result
    if (event.type === 'error') {
      throw new GenerationClientError(event.error.message, {
        code: event.error.code,
        status: event.error.status,
        retryable: event.error.retryable,
        category: event.error.category,
        retryAfterSeconds: event.error.retryAfterSeconds,
        action: event.error.action,
      })
    }
  }

  try {
    while (true) {
      const next = await reader.read()
      if (next.done) break
      parser.push(next.value).forEach(handleEvent)
    }
    parser.finish().forEach(handleEvent)
  } finally {
    reader.releaseLock()
  }

  if (!result) {
    throw new GenerationClientError('The generation stream ended before producing a result.', {
      code: 'incomplete_generation',
      retryable: true,
    })
  }
  return result
}

export async function listGenerationModels(projectId: string) {
  const response = await fetch(`/api/generate?projectId=${encodeURIComponent(projectId)}`, {
    headers: { Accept: 'application/json' },
    credentials: 'same-origin',
  })
  if (!response.ok) {
    const detail = await readErrorResponse(response)
    throw new GenerationClientError(detail.message, {
      code: detail.code,
      status: response.status,
      retryable: detail.retryable,
      category: detail.category,
      retryAfterSeconds: detail.retryAfterSeconds,
      action: detail.action,
    })
  }
  const body: unknown = await response.json()
  if (
    !isRecord(body) ||
    !Array.isArray(body.models) ||
    body.models.length === 0 ||
    !body.models.every((model) => typeof model === 'string')
  ) {
    throw new GenerationClientError('The generation model list is invalid.', {
      code: 'invalid_model_list',
    })
  }
  return {
    models: body.models as string[],
    defaultModel: typeof body.defaultModel === 'string' ? body.defaultModel : body.models[0] as string,
    providerConfigured: body.providerConfigured === true,
  }
}

async function readErrorResponse(response: Response) {
  try {
    const body = (await response.json()) as unknown
    if (isRecord(body) && isRecord(body.error)) {
      return {
        code: typeof body.error.code === 'string' ? body.error.code : 'generation_request_failed',
        message:
          typeof body.error.message === 'string'
            ? body.error.message
            : `Generation failed with status ${response.status}.`,
        retryable:
          typeof body.error.retryable === 'boolean' ? body.error.retryable : undefined,
        category:
          typeof body.error.category === 'string'
            ? body.error.category as GenerationErrorCategory
            : undefined,
        retryAfterSeconds:
          typeof body.error.retryAfterSeconds === 'number'
            ? body.error.retryAfterSeconds
            : undefined,
        action: typeof body.error.action === 'string' ? body.error.action : undefined,
      }
    }
  } catch {
    // Fall back to the HTTP status when the server did not return JSON.
  }
  return {
    code: 'generation_request_failed',
    message: `Generation failed with status ${response.status}.`,
    retryable: undefined,
    category: undefined,
    retryAfterSeconds: undefined,
    action: undefined,
  }
}

function parseRetryAfter(value: string | null) {
  if (!value) return undefined
  const seconds = Number(value)
  if (Number.isFinite(seconds) && seconds >= 0) return Math.ceil(seconds)
  const retryAt = Date.parse(value)
  return Number.isFinite(retryAt) ? Math.max(0, Math.ceil((retryAt - Date.now()) / 1_000)) : undefined
}
