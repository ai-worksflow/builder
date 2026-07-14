import assert from 'node:assert/strict'
import {
  allowsSoloSelfApprovalRequest,
  canSoloSelfReview,
  hasActiveGovernanceRun,
  reviewCandidatesForGovernance,
  requiresSoloReviewConfirmation,
} from '../lib/worksflow/project-governance'

assert.equal(hasActiveGovernanceRun(['running']), true)
assert.equal(hasActiveGovernanceRun(['waiting_input']), true)
assert.equal(hasActiveGovernanceRun(['failed']), true)
assert.equal(hasActiveGovernanceRun(['completed', 'cancelled', 'stale']), false)

assert.equal(requiresSoloReviewConfirmation('solo', 'approve'), true)
assert.equal(requiresSoloReviewConfirmation('solo', 'changes_requested'), false)
assert.equal(requiresSoloReviewConfirmation('solo', 'waive'), false)
assert.equal(requiresSoloReviewConfirmation('team', 'approve'), false)

const members = [
  {
    user: { id: 'owner-1', name: 'Owner', email: 'owner@example.com', createdAt: '' },
    role: 'owner' as const,
    joinedAt: '',
  },
  {
    user: { id: 'editor-1', name: 'Editor', email: 'editor@example.com', createdAt: '' },
    role: 'editor' as const,
    joinedAt: '',
  },
]
assert.deepEqual(
  reviewCandidatesForGovernance(members, 'owner-1', 'solo').map((member) => member.user.id),
  ['owner-1'],
)
assert.deepEqual(
  reviewCandidatesForGovernance(members, 'editor-1', 'solo').map((member) => member.user.id),
  ['owner-1'],
)
const reorderedSoloMembers = [
  {
    user: { id: 'editor-2', name: 'Second editor', email: 'editor-2@example.com', createdAt: '' },
    role: 'editor' as const,
    joinedAt: '',
  },
  {
    user: { id: 'admin-1', name: 'Admin', email: 'admin@example.com', createdAt: '' },
    role: 'admin' as const,
    joinedAt: '',
  },
  ...members.toReversed(),
]
assert.deepEqual(
  reviewCandidatesForGovernance(reorderedSoloMembers, 'admin-1', 'solo').map((member) => member.user.id),
  ['owner-1'],
)
assert.deepEqual(
  reviewCandidatesForGovernance(reorderedSoloMembers, 'editor-2', 'solo').map((member) => member.user.id),
  ['owner-1'],
)
assert.deepEqual(
  reviewCandidatesForGovernance([
    ...reorderedSoloMembers,
    {
      user: { id: 'owner-2', name: 'Second owner', email: 'owner-2@example.com', createdAt: '' },
      role: 'owner' as const,
      joinedAt: '',
    },
  ], 'admin-1', 'solo'),
  [],
)
assert.deepEqual(
  reviewCandidatesForGovernance(
    reorderedSoloMembers.filter((member) => member.role !== 'owner'),
    'admin-1',
    'solo',
  ),
  [],
)
assert.deepEqual(
  reviewCandidatesForGovernance(members, 'owner-1', 'team').map((member) => member.user.id),
  ['editor-1'],
)

assert.equal(allowsSoloSelfApprovalRequest('solo', members, ['owner-1']), true)
assert.equal(allowsSoloSelfApprovalRequest('solo', members, ['editor-1']), false)
assert.equal(allowsSoloSelfApprovalRequest('team', members, ['owner-1']), false)
assert.equal(allowsSoloSelfApprovalRequest('solo', [
  ...members,
  {
    user: { id: 'owner-2', name: 'Second owner', email: 'owner-2@example.com', createdAt: '' },
    role: 'owner' as const,
    joinedAt: '',
  },
], ['owner-1']), false)

assert.equal(canSoloSelfReview('solo', members, 'owner-1', 'owner-1'), true)
assert.equal(canSoloSelfReview('team', members, 'owner-1', 'owner-1'), false)
assert.equal(canSoloSelfReview('solo', members, 'editor-1', 'editor-1'), false)
assert.equal(canSoloSelfReview('solo', members, 'owner-1', 'editor-1'), false)
assert.equal(canSoloSelfReview('solo', [
  ...members,
  {
    user: { id: 'owner-2', name: 'Second owner', email: 'owner-2@example.com', createdAt: '' },
    role: 'owner' as const,
    joinedAt: '',
  },
], 'owner-1', 'owner-1'), false)

console.log('project governance tests passed')
