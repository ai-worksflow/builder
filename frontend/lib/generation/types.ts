export const GENERATION_MODES = ['plan', 'build', 'iterate', 'fix'] as const

export type GenerationMode = (typeof GENERATION_MODES)[number]
export type GenerationProvider = 'openai' | 'local'
export type GenerationLogLevel = 'info' | 'warning' | 'error'
export type GenerationTaskStatus = 'started' | 'completed' | 'failed'
export type GenerationLifecycleStatus = 'started' | 'completed' | 'cancelled' | 'failed'
export type GenerationErrorCategory =
  | 'rate_limit'
  | 'quota'
  | 'context_length'
  | 'authentication'
  | 'configuration'
  | 'output_validation'
  | 'provider'
  | 'cancelled'
  | 'unknown'

export type JsonPrimitive = string | number | boolean | null

export type JsonValue =
  | JsonPrimitive
  | JsonValue[]
  | { [key: string]: JsonValue }

export interface GenerationFileInput {
  path: string
  content: string
  language?: string
}

export type GenerationAttachmentKind = 'text' | 'image' | 'url'

export interface GenerationAttachmentInput {
  kind: GenerationAttachmentKind
  name: string
  content: string
  mimeType?: string
}

export interface GenerationRequest {
  projectId?: string
  prompt: string
  mode: GenerationMode
  model?: string
  currentFiles: GenerationFileInput[]
  attachments?: GenerationAttachmentInput[]
  context?: JsonValue
}

export interface GenerationPlanTask {
  id: string
  title: string
  description: string
}

export interface GenerationPlan {
  title: string
  summary: string
  tasks: GenerationPlanTask[]
}

export interface GeneratedFile {
  path: string
  content: string
  language: string
}

export interface GeneratedWorkspace {
  plan: GenerationPlan
  files: GeneratedFile[]
  summary: string
}

export interface GenerationUsage {
  inputTokens: number
  outputTokens: number
  totalTokens: number
  estimated: boolean
}

export interface GenerationCost {
  currency: 'USD'
  amount: number
  estimated: boolean
  configured: true
}

export interface GenerationLimits {
  maxTotalTokens?: number
  maxOutputTokens?: number
}

export interface GenerationErrorDetail {
  code: string
  message: string
  retryable: boolean
  category?: GenerationErrorCategory
  status?: number
  retryAfterSeconds?: number
  action?: string
}

export interface GenerationResult extends GeneratedWorkspace {
  runId: string
  provider: GenerationProvider
  model: string
  usage?: GenerationUsage
  cost?: GenerationCost
  limits?: GenerationLimits
}

interface GenerationEventBase {
  runId: string
  sequence: number
  timestamp: string
}

export interface GenerationPlanEvent extends GenerationEventBase {
  type: 'plan'
  provider: GenerationProvider
  plan: GenerationPlan
}

export interface GenerationTaskEvent extends GenerationEventBase {
  type: 'task'
  provider: GenerationProvider
  task: GenerationPlanTask
  status: GenerationTaskStatus
}

export interface GenerationFileEvent extends GenerationEventBase {
  type: 'file'
  provider: GenerationProvider
  file: GeneratedFile
}

export interface GenerationLogEvent extends GenerationEventBase {
  type: 'log'
  level: GenerationLogLevel
  message: string
  provider?: GenerationProvider
}

export interface GenerationResultEvent extends GenerationEventBase {
  type: 'result'
  result: GenerationResult
}

export interface GenerationErrorEvent extends GenerationEventBase {
  type: 'error'
  error: GenerationErrorDetail
}

export interface GenerationLifecycleEvent extends GenerationEventBase {
  type: 'lifecycle'
  status: GenerationLifecycleStatus
  provider?: GenerationProvider
  model?: string
  error?: GenerationErrorDetail
}

export type GenerationEvent =
  | GenerationPlanEvent
  | GenerationTaskEvent
  | GenerationFileEvent
  | GenerationLogEvent
  | GenerationResultEvent
  | GenerationErrorEvent

export type GenerationStreamEvent = GenerationEvent | GenerationLifecycleEvent

export type GenerationEventPayload = GenerationEvent extends infer Event
  ? Event extends GenerationEvent
    ? Omit<Event, 'runId' | 'sequence' | 'timestamp'>
    : never
  : never

export type GenerationStreamEventPayload = GenerationStreamEvent extends infer Event
  ? Event extends GenerationStreamEvent
    ? Omit<Event, 'runId' | 'sequence' | 'timestamp'>
    : never
  : never
