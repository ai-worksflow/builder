import type { ProblemDetailsDto } from './dto'
import { sha256Bytes, sha256DigestString } from './sha256'

export type FetchLike = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>
export type QueryValue = string | number | boolean | null | undefined

export interface CsrfTokenStore {
  get(): string | undefined
  set(token: string): void
  clear(): void
}

export interface HttpClientOptions {
  readonly baseUrl?: string
  readonly fetch?: FetchLike
  readonly credentials?: RequestCredentials
  readonly defaultHeaders?: HeadersInit
  readonly defaultTimeoutMs?: number
  readonly requestIdFactory?: () => string
  readonly csrfTokenStore?: CsrfTokenStore
}

export interface HttpRequestOptions<TBody = unknown> {
  readonly method?: string
  readonly query?: Readonly<Record<string, QueryValue | readonly QueryValue[]>>
  readonly headers?: HeadersInit
  readonly body?: TBody
  readonly signal?: AbortSignal
  readonly timeoutMs?: number
  readonly requestId?: string
  readonly idempotencyKey?: string | true
  readonly ifMatch?: string
  readonly responseType?: 'json' | 'text' | 'blob' | 'arrayBuffer' | 'void'
  readonly acceptedStatuses?: readonly number[]
  readonly clearCsrfOnSuccess?: boolean
}

export interface HttpResult<T> {
  readonly data: T
  readonly status: number
  readonly headers: Headers
  readonly requestId: string
  readonly etag?: string
}

export class PlatformClientError extends Error {
  readonly code: string
  readonly requestId?: string

  constructor(message: string, code: string, requestId?: string) {
    super(message)
    this.name = 'PlatformClientError'
    this.code = code
    this.requestId = requestId
  }
}

export class PlatformHttpError extends PlatformClientError {
  readonly status: number
  readonly problem: ProblemDetailsDto
  readonly retryAfterSeconds?: number

  constructor(problem: ProblemDetailsDto, retryAfterSeconds?: number) {
    super(
      problem.detail ?? problem.title,
      problem.code ?? 'platform_http_error',
      problem.requestId,
    )
    this.name = 'PlatformHttpError'
    this.status = problem.status
    this.problem = problem
    this.retryAfterSeconds = retryAfterSeconds
  }
}

export class PlatformNetworkError extends PlatformClientError {
  readonly causeValue?: unknown

  constructor(message: string, requestId: string, causeValue?: unknown) {
    super(message, 'platform_network_error', requestId)
    this.name = 'PlatformNetworkError'
    this.causeValue = causeValue
  }
}

export class PlatformAbortError extends PlatformClientError {
  readonly timedOut: boolean

  constructor(requestId: string, timedOut: boolean) {
    super(
      timedOut ? 'The platform request timed out.' : 'The platform request was aborted.',
      timedOut ? 'platform_timeout' : 'platform_aborted',
      requestId,
    )
    this.name = 'PlatformAbortError'
    this.timedOut = timedOut
  }
}

export class PlatformProtocolError extends PlatformClientError {
  readonly status?: number

  constructor(message: string, requestId?: string, status?: number) {
    super(message, 'platform_protocol_error', requestId)
    this.name = 'PlatformProtocolError'
    this.status = status
  }
}

const SHA256_DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/

/**
 * Recomputes a response body's byte-level SHA-256 before a higher-level
 * decoder is allowed to interpret it. Web Crypto is preferred, with a
 * byte-identical portable implementation for insecure browser contexts.
 */
export async function verifyResponseBodySha256(
  value: ArrayBuffer,
  expectedDigest: string,
  detail: string,
  requestId?: string,
  status?: number,
) {
  if (!(value instanceof ArrayBuffer) || !SHA256_DIGEST_PATTERN.test(expectedDigest)) {
    throw new PlatformProtocolError(detail, requestId, status)
  }
  let digest: Uint8Array
  try {
    digest = await sha256Bytes(value)
  } catch {
    throw new PlatformProtocolError(
      'The platform response body SHA-256 could not be verified.',
      requestId,
      status,
    )
  }
  const actualDigest = sha256DigestString(digest)
  if (actualDigest !== expectedDigest) {
    throw new PlatformProtocolError(detail, requestId, status)
  }
}

export function resolvePlatformBaseUrl(
  configuredValue?: string,
  location?: { readonly hostname: string },
) {
  const value = configuredValue?.trim()
  if (value) return value
  if (location?.hostname === 'localhost' || location?.hostname === '127.0.0.1') {
    return `http://${location.hostname}:8080`
  }
  if (location) return '/api/platform'
  return 'http://127.0.0.1:8080'
}

function configuredBaseUrl() {
  const value = typeof process !== 'undefined'
    ? process.env.NEXT_PUBLIC_PLATFORM_API_URL
    : undefined
  const location = typeof window !== 'undefined' ? window.location : undefined
  return resolvePlatformBaseUrl(value, location)
}

class MemoryCsrfTokenStore implements CsrfTokenStore {
  private token?: string

  get() {
    return this.token
  }

  set(token: string) {
    this.token = token
  }

  clear() {
    this.token = undefined
  }
}

function defaultRequestId() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `req_${Date.now().toString(36)}_${Math.random().toString(36).slice(2)}`
}

function joinUrl(baseUrl: string, path: string) {
  const normalizedBase = baseUrl.replace(/\/+$/, '')
  const normalizedPath = path.replace(/^\/+/, '')
  return normalizedPath ? `${normalizedBase}/${normalizedPath}` : normalizedBase
}

function appendQuery(
  url: string,
  query?: Readonly<Record<string, QueryValue | readonly QueryValue[]>>,
) {
  if (!query) return url

  const pairs: string[] = []
  for (const [key, rawValue] of Object.entries(query)) {
    const values = Array.isArray(rawValue) ? rawValue : [rawValue]
    for (const value of values) {
      if (value === undefined || value === null) continue
      pairs.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`)
    }
  }

  if (pairs.length === 0) return url
  return `${url}${url.includes('?') ? '&' : '?'}${pairs.join('&')}`
}

function isBodyInit(value: unknown): value is BodyInit {
  if (typeof value === 'string') return true
  if (typeof Blob !== 'undefined' && value instanceof Blob) return true
  if (typeof FormData !== 'undefined' && value instanceof FormData) return true
  if (typeof URLSearchParams !== 'undefined' && value instanceof URLSearchParams) return true
  if (typeof ArrayBuffer !== 'undefined' && value instanceof ArrayBuffer) return true
  if (typeof ArrayBuffer !== 'undefined' && ArrayBuffer.isView(value)) return true
  return false
}

function abortError(error: unknown) {
  return error instanceof Error && error.name === 'AbortError'
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function stringValue(value: unknown) {
  return typeof value === 'string' ? value : undefined
}

function normalizeProblem(
  body: unknown,
  response: Response,
  requestId: string,
): ProblemDetailsDto {
  const value = record(body) ? body : {}
  const status = typeof value.status === 'number' ? value.status : response.status
  const responseRequestId = response.headers.get('x-request-id') ?? requestId

  return {
    type: stringValue(value.type) ?? 'about:blank',
    title: stringValue(value.title) ?? `Platform request failed (${response.status}).`,
    status,
    detail: stringValue(value.detail),
    instance: stringValue(value.instance),
    code: stringValue(value.code),
    requestId: stringValue(value.requestId) ?? responseRequestId,
    errors: record(value.errors)
      ? Object.fromEntries(
          Object.entries(value.errors).flatMap(([key, entry]) => {
            if (!Array.isArray(entry) || !entry.every((item) => typeof item === 'string')) return []
            return [[key, entry as string[]]]
          }),
        )
      : undefined,
    extensions: record(value.extensions) ? value.extensions as ProblemDetailsDto['extensions'] : undefined,
  }
}

function retryAfterSeconds(response: Response) {
  const value = response.headers.get('retry-after')?.trim()
  if (!value) return undefined

  const seconds = Number(value)
  if (Number.isFinite(seconds) && seconds >= 0) return seconds

  const timestamp = Date.parse(value)
  if (!Number.isFinite(timestamp)) return undefined
  return Math.max(0, Math.ceil((timestamp - Date.now()) / 1000))
}

async function parseJsonText(response: Response, requestId: string) {
  const text = await response.text()
  if (!text.trim()) return undefined
  try {
    return JSON.parse(text) as unknown
  } catch {
    throw new PlatformProtocolError(
      'The platform returned malformed JSON.',
      response.headers.get('x-request-id') ?? requestId,
      response.status,
    )
  }
}

export class HttpClient {
  readonly baseUrl: string
  private readonly fetchImplementation: FetchLike
  private readonly credentials: RequestCredentials
  private readonly defaultHeaders: Headers
  private readonly defaultTimeoutMs?: number
  private readonly requestIdFactory: () => string
  private readonly csrfTokenStore: CsrfTokenStore

  constructor(options: HttpClientOptions = {}) {
    this.baseUrl = options.baseUrl?.trim() || configuredBaseUrl()
    const availableFetch = options.fetch ?? globalThis.fetch
    if (!availableFetch) {
      throw new PlatformClientError(
        'A fetch implementation is required.',
        'platform_fetch_unavailable',
      )
    }
    this.fetchImplementation = availableFetch.bind(globalThis)
    this.credentials = options.credentials ?? 'include'
    this.defaultHeaders = new Headers(options.defaultHeaders)
    this.defaultTimeoutMs = options.defaultTimeoutMs
    this.requestIdFactory = options.requestIdFactory ?? defaultRequestId
    this.csrfTokenStore = options.csrfTokenStore ?? new MemoryCsrfTokenStore()
  }

  async request<TResponse, TBody = unknown>(
    path: string,
    options: HttpRequestOptions<TBody> = {},
  ): Promise<HttpResult<TResponse>> {
    const requestId = options.requestId ?? this.requestIdFactory()
    const method = (options.method ?? 'GET').toUpperCase()
    const headers = new Headers(this.defaultHeaders)
    new Headers(options.headers).forEach((value, key) => headers.set(key, value))

    if (!headers.has('accept')) {
      headers.set('Accept', 'application/json, application/problem+json')
    }
    headers.set('X-Request-ID', requestId)
    if (method !== 'GET' && method !== 'HEAD' && !headers.has('x-csrf-token')) {
      const csrfToken = this.csrfTokenStore.get()
      if (csrfToken) headers.set('X-CSRF-Token', csrfToken)
    }
    if (options.ifMatch) headers.set('If-Match', options.ifMatch)
    if (options.idempotencyKey) {
      headers.set(
        'Idempotency-Key',
        options.idempotencyKey === true ? this.requestIdFactory() : options.idempotencyKey,
      )
    }

    let body: BodyInit | undefined
    if (options.body !== undefined) {
      if (isBodyInit(options.body)) {
        body = options.body
      } else {
        body = JSON.stringify(options.body)
        if (!headers.has('content-type')) headers.set('Content-Type', 'application/json')
      }
    }

    const controller = new AbortController()
    let timedOut = false
    const externalAbort = () => controller.abort(options.signal?.reason)
    if (options.signal?.aborted) externalAbort()
    else options.signal?.addEventListener('abort', externalAbort, { once: true })

    const timeoutMs = options.timeoutMs ?? this.defaultTimeoutMs
    const timeout = timeoutMs && timeoutMs > 0
      ? setTimeout(() => {
          timedOut = true
          controller.abort()
        }, timeoutMs)
      : undefined

    let response: Response
    try {
      response = await this.fetchImplementation(
        appendQuery(joinUrl(this.baseUrl, path), options.query),
        {
          method,
          credentials: this.credentials,
          headers,
          body,
          signal: controller.signal,
        },
      )
    } catch (error) {
      if (controller.signal.aborted || abortError(error)) {
        throw new PlatformAbortError(requestId, timedOut)
      }
      throw new PlatformNetworkError(
        error instanceof Error ? error.message : 'Unable to reach the platform service.',
        requestId,
        error,
      )
    } finally {
      if (timeout !== undefined) clearTimeout(timeout)
      options.signal?.removeEventListener('abort', externalAbort)
    }

    const responseRequestId = response.headers.get('x-request-id') ?? requestId
    const contentType = response.headers.get('content-type')?.toLowerCase() ?? ''
    const acceptedStatus = options.acceptedStatuses?.includes(response.status) === true
      && !contentType.includes('application/problem+json')
    if (!response.ok && !acceptedStatus) {
      let bodyValue: unknown
      try {
        bodyValue = await parseJsonText(response, responseRequestId)
      } catch (error) {
        if (!(error instanceof PlatformProtocolError)) throw error
        bodyValue = undefined
      }
      const problem = normalizeProblem(bodyValue, response, responseRequestId)
      if (
        response.status === 401 ||
        response.status === 419 ||
        problem.code === 'csrf_failed'
      ) this.csrfTokenStore.clear()
      throw new PlatformHttpError(problem, retryAfterSeconds(response))
    }

    let data: unknown
    const responseType = options.responseType ?? 'json'
    if (responseType === 'arrayBuffer') {
      // A 204 patch-file response still carries an authoritative empty byte
      // representation. Preserve those actual transport bytes instead of
      // fabricating an empty buffer in a higher-level client.
      data = await response.arrayBuffer()
    } else if (responseType === 'void' || response.status === 204 || response.status === 205) {
      data = undefined
    } else if (responseType === 'text') {
      data = await response.text()
    } else if (responseType === 'blob') {
      data = await response.blob()
    } else {
      data = await parseJsonText(response, responseRequestId)
    }

    const responseCsrfToken = response.headers.get('x-csrf-token') ?? (
      record(data) && typeof data.csrfToken === 'string' ? data.csrfToken : undefined
    )
    if (responseCsrfToken) this.csrfTokenStore.set(responseCsrfToken)
    if (options.clearCsrfOnSuccess || (record(data) && data.state === 'anonymous')) {
      this.csrfTokenStore.clear()
    }

    return {
      data: data as TResponse,
      status: response.status,
      headers: response.headers,
      requestId: responseRequestId,
      etag: response.headers.get('etag') ?? undefined,
    }
  }

  clearCsrfToken() {
    this.csrfTokenStore.clear()
  }

  getCsrfToken() {
    return this.csrfTokenStore.get()
  }

  get<TResponse>(path: string, options: Omit<HttpRequestOptions<never>, 'method' | 'body'> = {}) {
    return this.request<TResponse, never>(path, { ...options, method: 'GET' })
  }

  post<TResponse, TBody = unknown>(
    path: string,
    body?: TBody,
    options: Omit<HttpRequestOptions<TBody>, 'method' | 'body'> = {},
  ) {
    return this.request<TResponse, TBody>(path, { ...options, method: 'POST', body })
  }

  patch<TResponse, TBody = unknown>(
    path: string,
    body: TBody,
    options: Omit<HttpRequestOptions<TBody>, 'method' | 'body'> = {},
  ) {
    return this.request<TResponse, TBody>(path, { ...options, method: 'PATCH', body })
  }

  put<TResponse, TBody = unknown>(
    path: string,
    body: TBody,
    options: Omit<HttpRequestOptions<TBody>, 'method' | 'body'> = {},
  ) {
    return this.request<TResponse, TBody>(path, { ...options, method: 'PUT', body })
  }

  delete<TResponse = void>(
    path: string,
    options: Omit<HttpRequestOptions<never>, 'method' | 'body'> = {},
  ) {
    return this.request<TResponse, never>(path, { ...options, method: 'DELETE' })
  }
}
