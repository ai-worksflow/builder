import assert from 'node:assert/strict'
import {
  candidateAbandonEntryAllowed,
} from '../lib/platform/sandbox-abandon'
const candidate = {
  id: 'candidate-1',
  projectId: 'project-1',
  status: 'active',
  dirty: true,
} as const

const session = {
  candidate: { id: candidate.id },
  allowedActions: ['view', 'edit', 'checkpoint'],
} as const

assert.equal(
  candidateAbandonEntryAllowed(session, candidate),
  true,
  'dirty Candidate must be able to enter abandon flow while exact checkpoint remains available',
)
assert.equal(
  candidateAbandonEntryAllowed(
    { ...session, allowedActions: ['view', 'edit'] },
    candidate,
  ),
  false,
  'dirty Candidate cannot enter abandon when neither checkpoint nor abandon is authoritative',
)
assert.equal(
  candidateAbandonEntryAllowed(
    { ...session, allowedActions: ['view', 'abandon'] },
    { ...candidate, dirty: false },
  ),
  true,
  'clean exact Candidate uses the direct server abandon action',
)
assert.equal(
  candidateAbandonEntryAllowed(session, { ...candidate, id: 'candidate-other' }),
  false,
  'session and Candidate identities must be exact',
)
