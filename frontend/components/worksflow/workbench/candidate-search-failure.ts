import { PlatformHttpError } from '../../../lib/platform/http'

const minimumRetryAfterSeconds = 1
const maximumRetryAfterSeconds = 3_600

export interface CandidateSearchRetryIdentityInput {
  readonly projectId: string
  readonly candidateId: string
  readonly generation: number
  readonly rootHash: string
  readonly query: string
  readonly caseSensitive: boolean
  readonly include: string
}

export type CandidateSearchFailureDecision =
  | { readonly kind: 'refresh-head' }
  | {
    readonly kind: 'retry-once'
    readonly retryAfterSeconds: number
    readonly message: string
  }
  | { readonly kind: 'blocked'; readonly message: string }
  | { readonly kind: 'unhandled' }

export function candidateSearchRetryIdentity(input: CandidateSearchRetryIdentityInput) {
  return JSON.stringify([
    input.projectId,
    input.candidateId,
    input.generation,
    input.rootHash,
    input.query,
    input.caseSensitive,
    input.include,
  ])
}

function boundedRetryAfterSeconds(value: number | undefined) {
  return typeof value === 'number'
    && Number.isFinite(value)
    && value >= minimumRetryAfterSeconds
    && value <= maximumRetryAfterSeconds
    ? value
    : null
}

function rateLimitSubject(code: string) {
  return code === 'repository_search_index_busy'
    ? 'The Candidate search index is busy.'
    : 'The Candidate search rate limit was reached.'
}

export function resolveCandidateSearchFailure(
  cause: unknown,
  automaticRetryUsed: boolean,
): CandidateSearchFailureDecision {
  if (!(cause instanceof PlatformHttpError)) return { kind: 'unhandled' }

  if (cause.status === 409 && cause.code === 'repository_search_head_changed') {
    return { kind: 'refresh-head' }
  }

  if (cause.status === 409 && cause.code === 'repository_search_index_quota_exceeded') {
    return {
      kind: 'blocked',
      message: 'The project Candidate search index quota is exceeded. The exact Candidate head, Blueprint, and editor draft were left unchanged.',
    }
  }

  if (
    cause.status === 429
    && (cause.code === 'repository_search_rate_limited' || cause.code === 'repository_search_index_busy')
  ) {
    const subject = rateLimitSubject(cause.code)
    if (automaticRetryUsed) {
      return {
        kind: 'blocked',
        message: `${subject} The single automatic retry was already used; search remains blocked without refreshing the Candidate head or editor.`,
      }
    }
    const retryAfterSeconds = boundedRetryAfterSeconds(cause.retryAfterSeconds)
    if (retryAfterSeconds === null) {
      return {
        kind: 'blocked',
        message: `${subject} The server did not provide a usable Retry-After delay from 1 to 3600 seconds, so no automatic retry or Candidate head refresh was started.`,
      }
    }
    return {
      kind: 'retry-once',
      retryAfterSeconds,
      message: `${subject} Retrying once in ${retryAfterSeconds} ${retryAfterSeconds === 1 ? 'second' : 'seconds'}; the exact Candidate head, Blueprint, and editor draft will stay unchanged.`,
    }
  }

  if (cause.status === 503) {
    const detail = cause.problem.detail || cause.problem.title
    return {
      kind: 'blocked',
      message: `Exact Candidate search is temporarily unavailable: ${detail} No automatic retry or Candidate head refresh was started; the editor draft was preserved.`,
    }
  }

  return { kind: 'unhandled' }
}
