import assert from 'node:assert/strict'
import {
  ImplementationProposalContractError,
  normalizeImplementationProposal,
} from '../lib/platform/implementation-proposal'

const proposalId = '11111111-1111-4111-8111-111111111111'
const projectId = '22222222-2222-4222-8222-222222222222'
const manifestId = '33333333-3333-4333-8333-333333333333'
const contractId = '44444444-4444-4444-8444-444444444444'
const actorId = '55555555-5555-4555-8555-555555555555'

function hash(character: string) {
  return `sha256:${character.repeat(64)}`
}

function manualProposal() {
  return {
    id: proposalId,
    projectId,
    buildManifestId: manifestId,
    applicationBuildContract: {
      id: contractId,
      contractHash: 'a'.repeat(64),
    },
    executionSource: 'manual_submission',
    operations: [{
      id: 'operation-1',
      kind: 'file.upsert',
      path: 'frontend/app/page.tsx',
      content: 'export default function Page() { return null }',
      language: 'typescript',
      mode: '100644',
      rationale: 'Implement AC-1.',
      traceSource: ['AC-1'],
      decision: 'pending',
    }],
    routes: [{ method: 'GET', path: '/' }],
    apis: [],
    migrations: [],
    tests: [],
    previews: [],
    traceLinks: [],
    diagnostics: [{
      code: 'review-note',
      path: '$.operations[0]',
      message: 'Review this operation.',
      severity: 'info',
    }],
    assumptions: ['The template is already installed.'],
    unimplementedItems: [],
    status: 'open',
    version: 1,
    payloadHash: 'b'.repeat(64),
    createdBy: actorId,
    createdAt: '2026-07-18T10:11:12.123456789Z',
  }
}

function candidateProposal() {
  const candidateId = '66666666-6666-4666-8666-666666666666'
  const snapshotId = '77777777-7777-4777-8777-777777777777'
  const verificationId = '88888888-8888-4888-8888-888888888888'
  return {
    ...manualProposal(),
    baseWorkspaceRevision: {
      artifactId: '99999999-9999-4999-8999-999999999999',
      revisionId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
      contentHash: hash('1'),
    },
    executionSource: 'candidate_freeze',
    candidateSource: {
      freezeReceiptId: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
      repositorySnapshotId: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
      sessionId: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
      candidateId,
      candidateSnapshotId: snapshotId,
      candidateVersion: 3,
      journalSequence: 7,
      sessionEpoch: 2,
      writerLeaseEpoch: 4,
      baseTreeHash: hash('2'),
      treeHash: hash('3'),
      fullStackTemplate: {
        id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
        contentHash: hash('4'),
      },
      verificationReceipt: {
        id: verificationId,
        contentHash: hash('5'),
      },
    },
    operations: [{
      id: 'candidate-00001-abcdefabcdef',
      kind: 'file.upsert',
      path: 'frontend/app/page.tsx',
      content: 'export default function Page() { return null }',
      language: 'typescript',
      mode: '100644',
      rationale: `Freeze exact CandidateSnapshot ${snapshotId}`,
      traceSource: [`candidate-snapshot:${snapshotId}`],
      decision: 'pending',
    }],
    routes: [],
    diagnostics: [],
    assumptions: [],
    traceLinks: [{
      kind: 'candidate_snapshot',
      candidateId,
      candidateSnapshotId: snapshotId,
      baseTreeHash: hash('2'),
      treeHash: hash('3'),
    }, {
      kind: 'candidate_verification_receipt',
      id: verificationId,
      contentHash: hash('5'),
    }],
  }
}

function clone<T>(value: T): T {
  return structuredClone(value)
}

function rejects(value: unknown, message: string) {
  assert.throws(
    () => normalizeImplementationProposal(value),
    (error: unknown) => error instanceof ImplementationProposalContractError,
    message,
  )
}

const original = manualProposal()
const parsed = normalizeImplementationProposal(original)
assert.notEqual(parsed, original)
assert.notEqual(parsed.operations, original.operations)
assert.notEqual(parsed.routes[0], original.routes[0])
assert.equal(parsed.operations[0]?.path, 'frontend/app/page.tsx')
assert.ok(Object.isFrozen(parsed))
assert.ok(Object.isFrozen(parsed.operations))
assert.ok(Object.isFrozen(parsed.operations[0]))
assert.ok(Object.isFrozen(parsed.routes[0] as object))
original.operations[0]!.path = 'changed-after-parse.ts'
assert.equal(parsed.operations[0]?.path, 'frontend/app/page.tsx')
assert.throws(() => Object.assign(parsed.operations[0]!, { path: 'mutated.ts' }), TypeError)

const candidate = normalizeImplementationProposal(candidateProposal())
assert.equal(candidate.executionSource, 'candidate_freeze')
assert.equal(candidate.candidateSource?.candidateVersion, 3)

const conversation = manualProposal()
conversation.executionSource = 'conversation_command'
Object.assign(conversation, {
  id: 'ffffffff-ffff-4fff-8fff-ffffffffffff',
  conversationCommandId: 'ffffffff-ffff-4fff-8fff-ffffffffffff',
  instructionHash: hash('6'),
  aiProvider: 'provider',
  aiModel: 'model',
})
assert.equal(normalizeImplementationProposal(conversation).conversationCommandId, conversation.id)

rejects(null, 'null is not a Proposal')
rejects({ ...manualProposal(), extra: true }, 'unknown top-level fields fail closed')

const missingRequired = clone(manualProposal()) as Record<string, unknown>
delete missingRequired.operations
rejects(missingRequired, 'missing required fields fail closed')

rejects({ ...manualProposal(), applicationBuildContract: undefined }, 'undefined is not a missing-field default')
rejects({ ...manualProposal(), routes: null }, 'null arrays fail closed')
rejects({ ...manualProposal(), status: 'unknown' }, 'unknown status fails closed')
rejects({ ...manualProposal(), version: 1.5 }, 'non-integer versions fail closed')
rejects({ ...manualProposal(), id: 'proposal-1' }, 'non-UUID identities fail closed')
rejects({ ...manualProposal(), payloadHash: hash('a') }, 'the Proposal payload hash has its exact raw form')
rejects({ ...manualProposal(), createdAt: '2026-02-31T10:11:12Z' }, 'invalid calendar timestamps fail closed')
rejects({ ...manualProposal(), createdAt: '2026-07-18T10:11:12+00:00' }, 'non-canonical timestamp offsets fail closed')

const historical = clone(manualProposal()) as Record<string, unknown>
delete historical.applicationBuildContract
rejects(historical, 'unversioned pre-BuildContract history is not silently accepted')

const unknownOperation = clone(manualProposal())
Object.assign(unknownOperation.operations[0]!, { unknown: true })
rejects(unknownOperation, 'unknown operation fields fail closed')

const deleteWithContent = clone(manualProposal())
deleteWithContent.operations[0]!.kind = 'file.delete'
rejects(deleteWithContent, 'operation kind and content must agree')

const pendingWithActor = clone(manualProposal())
Object.assign(pendingWithActor.operations[0]!, { decidedBy: actorId })
rejects(pendingWithActor, 'pending operations cannot have decision identity')

const rejectedWithoutReason = clone(manualProposal())
Object.assign(rejectedWithoutReason.operations[0]!, { decision: 'rejected', decidedBy: actorId })
rejectedWithoutReason.status = 'rejected'
rejects(rejectedWithoutReason, 'rejections require a reason')

const duplicateOperations = clone(manualProposal())
duplicateOperations.operations.push(clone(duplicateOperations.operations[0]!))
rejects(duplicateOperations, 'operation IDs are unique')

const duplicateDependencies = clone(manualProposal())
const repeatedDependency = {
  ...clone(duplicateDependencies.operations[0]!),
  id: 'operation-2',
  path: 'frontend/app/other.tsx',
}
Object.assign(repeatedDependency, { dependsOn: ['operation-1', 'operation-1'] })
duplicateDependencies.operations.push(repeatedDependency)
rejects(duplicateDependencies, 'dependency identities are unique')

const dependencyCycle = clone(manualProposal())
Object.assign(dependencyCycle.operations[0]!, { dependsOn: ['operation-2'] })
const cyclicDependency = {
  ...clone(dependencyCycle.operations[0]!),
  id: 'operation-2',
  path: 'frontend/app/other.tsx',
}
Object.assign(cyclicDependency, { dependsOn: ['operation-1'] })
dependencyCycle.operations.push(cyclicDependency)
rejects(dependencyCycle, 'dependency cycles fail closed')

rejects({ ...manualProposal(), status: 'ready' }, 'status must agree with operation decisions')

const inconsistentSource = candidateProposal()
inconsistentSource.candidateSource.candidateSnapshotId = '12121212-1212-4212-8212-121212121212'
rejects(inconsistentSource, 'Candidate operations and source identity must agree')

const missingVerification = candidateProposal()
missingVerification.candidateSource.verificationReceipt.contentHash = ''
rejects(missingVerification, 'Candidate authority cannot be fabricated from an empty receipt')

const generatedWithoutProvenance = manualProposal()
generatedWithoutProvenance.executionSource = 'workflow_runner'
rejects(generatedWithoutProvenance, 'generated sources require complete provenance')

const appliedBeforeCreation = clone(manualProposal())
Object.assign(appliedBeforeCreation.operations[0]!, { decision: 'applied', decidedBy: actorId })
Object.assign(appliedBeforeCreation, {
  status: 'applied',
  appliedAt: '2026-07-17T10:11:12Z',
})
rejects(appliedBeforeCreation, 'appliedAt cannot precede createdAt')

console.log('implementation Proposal responses are exact, fail-closed, identity-consistent, and immutable')
