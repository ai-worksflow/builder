import assert from 'node:assert/strict'
import {
  candidateSearchRetryIdentity,
  resolveCandidateSearchFailure,
} from '../components/worksflow/workbench/candidate-search-failure'
import { PlatformHttpError } from '../lib/platform/http'

function httpError(status: number, code: string, retryAfterSeconds?: number) {
  return new PlatformHttpError({
    type: 'about:blank',
    title: code,
    status,
    detail: `detail for ${code}.`,
    code,
  }, retryAfterSeconds)
}

const headChanged = resolveCandidateSearchFailure(
  httpError(409, 'repository_search_head_changed'),
  false,
)
assert.equal(headChanged.kind, 'refresh-head')

const quotaExceeded = resolveCandidateSearchFailure(
  httpError(409, 'repository_search_index_quota_exceeded'),
  false,
)
assert.equal(quotaExceeded.kind, 'blocked')
if (quotaExceeded.kind === 'blocked') {
  assert.match(quotaExceeded.message, /head, Blueprint, and editor draft were left unchanged/)
}
assert.equal(
  resolveCandidateSearchFailure(httpError(409, 'some_other_conflict'), false).kind,
  'unhandled',
  'only the exact head-changed code may request a Candidate head refresh',
)

for (const code of ['repository_search_rate_limited', 'repository_search_index_busy']) {
  const retry = resolveCandidateSearchFailure(httpError(429, code, 2), false)
  assert.equal(retry.kind, 'retry-once')
  if (retry.kind === 'retry-once') {
    assert.equal(retry.retryAfterSeconds, 2)
    assert.match(retry.message, /Retrying once in 2 seconds/)
    assert.match(retry.message, /Blueprint, and editor draft will stay unchanged/)
  }

  const alreadyRetried = resolveCandidateSearchFailure(httpError(429, code, 2), true)
  assert.equal(alreadyRetried.kind, 'blocked')
  if (alreadyRetried.kind === 'blocked') {
    assert.match(alreadyRetried.message, /single automatic retry was already used/)
  }
}

for (const retryAfterSeconds of [undefined, 0, Number.NaN, Number.POSITIVE_INFINITY, 3_601]) {
  const invalidDelay = resolveCandidateSearchFailure(
    httpError(429, 'repository_search_rate_limited', retryAfterSeconds),
    false,
  )
  assert.equal(invalidDelay.kind, 'blocked')
  if (invalidDelay.kind === 'blocked') {
    assert.match(invalidDelay.message, /usable Retry-After delay from 1 to 3600 seconds/)
  }
}
assert.equal(
  resolveCandidateSearchFailure(httpError(429, 'repository_search_rate_limited', 1), false).kind,
  'retry-once',
)
assert.equal(
  resolveCandidateSearchFailure(httpError(429, 'repository_search_rate_limited', 3_600), false).kind,
  'retry-once',
)

for (const code of ['repository_search_admission_unavailable', 'repository_search_index_unavailable']) {
  const outage = resolveCandidateSearchFailure(httpError(503, code), false)
  assert.equal(outage.kind, 'blocked')
  if (outage.kind === 'blocked') {
    assert.match(outage.message, /No automatic retry or Candidate head refresh was started/)
  }
}

const baseIdentityInput = {
  projectId: 'project-1',
  candidateId: 'candidate-1',
  generation: 7,
  rootHash: `sha256:${'a'.repeat(64)}`,
  query: 'literal',
  caseSensitive: true,
  include: 'src/**',
} as const
const identity = candidateSearchRetryIdentity(baseIdentityInput)
assert.equal(candidateSearchRetryIdentity(baseIdentityInput), identity)
assert.notEqual(
  candidateSearchRetryIdentity({ ...baseIdentityInput, query: 'changed' }),
  identity,
  'query changes must invalidate a pending automatic retry',
)
assert.notEqual(
  candidateSearchRetryIdentity({ ...baseIdentityInput, generation: 8 }),
  identity,
  'exact Candidate head changes must invalidate a pending automatic retry',
)
