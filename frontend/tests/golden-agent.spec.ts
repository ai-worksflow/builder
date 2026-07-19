import { createHash } from 'node:crypto'

import {
  agentAttemptEtag,
  agentMergeEtag,
  type AgentPatchFencesInput,
} from '@/lib/platform/agent-client'
import type { AgentAttemptDto } from '@/lib/platform/agent-contract'
import { sandboxFences } from '@/lib/platform/sandbox-contract'

import { parseCanonicalJSON } from '../scripts/qualification-core.mjs'
import { expect, goldenQualificationEnvironment, test, type APIRequestContext } from './qualification-runtime'
import {
  assertDigest,
  assertExactObject,
  assertGoldenFaultTarget,
  assertTimestamp,
  assertUUID,
  bootstrapGoldenSandbox,
  browserStorageStateWithSandbox,
  consumeGoldenFault,
  expectPlatformFailure,
  goldenSubject,
  platformClient,
  qualificationKey,
  waitForValue,
} from './golden-qualification-support'

function attemptFences(sandbox: Awaited<ReturnType<typeof bootstrapGoldenSandbox>>): AgentPatchFencesInput {
  return {
    expectedCandidateVersion: sandbox.candidate.version,
    expectedSessionEpoch: sandbox.session.sessionEpoch,
    expectedSessionVersion: sandbox.session.version,
    expectedWriterLeaseEpoch: sandbox.candidate.writerLeaseEpoch,
  }
}

async function createAttempt(
  caseId: string,
  instruction: string,
  exactSandbox?: Awaited<ReturnType<typeof bootstrapGoldenSandbox>>,
) {
  const sandbox = exactSandbox ?? await bootstrapGoldenSandbox(caseId)
  const created = await sandbox.client.agent.createAttempt(
    sandbox.session.id,
    {
      executorProfile: goldenSubject().agent.modelGateway.profileId,
      instruction,
      taskKey: `${caseId.toLowerCase()}-task`,
    },
    { idempotencyKey: qualificationKey(caseId, 'agent-attempt') },
  )
  expect(created.data.attempt.projectId).toBe(sandbox.projectId)
  expect(created.data.attempt.sandboxSessionId).toBe(sandbox.session.id)
  expect(created.data.attempt.candidateId).toBe(sandbox.candidate.id)
  expect(created.data.attempt.executor.runnerImageDigest).toBe(goldenSubject().agent.runner.imageDigest)
  expect(created.data.attempt.executor.provider).toBe(goldenSubject().agent.modelGateway.providerId)
  expect(created.data.attempt.executor.model).toBe(goldenSubject().agent.modelGateway.modelId)
  return { created, sandbox }
}

async function assertAgentRuntimeReceipt(request: APIRequestContext, attempt: AgentAttemptDto) {
  const environment = goldenQualificationEnvironment()
  const subject = goldenSubject()
  const pointerResponse = await request.get(
    `${subject.platform.apiOrigin}/v1/qualification/agent-attempt-runtime-bindings/${attempt.id}`,
    {
      headers: environment.credentials.platform.apiA.headers,
      params: { fixtureId: subject.fixtureId, runId: subject.runId },
    },
  )
  const pointerBytes = await pointerResponse.body()
  expect(
    pointerResponse.status(),
    `A Golden-run-bound Agent runtime receipt pointer is required; response=${pointerResponse.status()} ${pointerBytes.toString('utf8')}`,
  ).toBe(200)
  expect(pointerResponse.headers()['content-type'] ?? '').toMatch(/^application\/json(?:;|$)/iu)
  expect(pointerResponse.headers()['cache-control']).toBe('no-store')
  const pointer = assertExactObject(
    parseCanonicalJSON(pointerBytes, 'Agent runtime-binding receipt pointer'),
    [
      'attemptId', 'authorityHash', 'fixtureHash', 'fixtureId', 'planDigest',
      'receipt', 'runId', 'schemaVersion',
    ],
    'Agent runtime-binding receipt pointer',
  )
  expect(pointer.schemaVersion).toBe('worksflow-golden-run-agent-runtime-binding-pointer/v1')
  expect(pointer.attemptId).toBe(attempt.id)
  expect(pointer.authorityHash).toBe(environment.authorityHash)
  expect(pointer.fixtureHash).toBe(environment.fixtureHash)
  expect(pointer.fixtureId).toBe(subject.fixtureId)
  expect(pointer.planDigest).toBe(subject.planDigest)
  expect(pointer.runId).toBe(subject.runId)
  const receiptPointer = assertExactObject(pointer.receipt, ['contentHash', 'id'], 'Agent runtime receipt ref')
  assertUUID(String(receiptPointer.id), 'Agent runtime receipt id')
  assertDigest(String(receiptPointer.contentHash), 'Agent runtime receipt content hash')

  const receiptResponse = await request.get(
    `${subject.platform.apiOrigin}/v1/qualification/agent-attempt-runtime-receipts/${receiptPointer.id}`,
    {
      headers: environment.credentials.platform.apiA.headers,
      params: {
        contentHash: String(receiptPointer.contentHash),
        fixtureId: subject.fixtureId,
        runId: subject.runId,
      },
    },
  )
  const receiptBytes = await receiptResponse.body()
  expect(
    receiptResponse.status(),
    `Exact immutable Agent runtime receipt bytes are required; response=${receiptResponse.status()} ${receiptBytes.toString('utf8')}`,
  ).toBe(200)
  expect(receiptResponse.headers()['content-type'] ?? '').toMatch(/^application\/json(?:;|$)/iu)
  expect(receiptResponse.headers()['cache-control']).toBe('public, max-age=31536000, immutable')
  expect(receiptResponse.headers().etag).toBe(`"${receiptPointer.contentHash}"`)
  expect(`sha256:${createHash('sha256').update(receiptBytes).digest('hex')}`).toBe(
    receiptPointer.contentHash,
  )
  const receipt = assertExactObject(
    parseCanonicalJSON(receiptBytes, 'immutable Agent runtime-binding receipt'),
    [
      'attemptExecutor', 'attemptId', 'authorityHash', 'candidateId', 'configurationHash',
      'executorProfile', 'fixtureHash', 'fixtureId', 'modelGateway', 'observedAt',
      'planDigest', 'projectId', 'providerInvocation', 'receiptId', 'runId', 'runner',
      'runtimeProcessId', 'sandboxSessionId', 'schemaVersion', 'startedAt', 'taskCapsule',
    ],
    'immutable Agent runtime-binding receipt',
  )
  expect(receipt.schemaVersion).toBe('agent-runtime-binding-receipt/v1')
  expect(receipt.receiptId).toBe(receiptPointer.id)
  expect(receipt.attemptId).toBe(attempt.id)
  expect(receipt.authorityHash).toBe(environment.authorityHash)
  expect(receipt.fixtureHash).toBe(environment.fixtureHash)
  expect(receipt.fixtureId).toBe(subject.fixtureId)
  expect(receipt.planDigest).toBe(subject.planDigest)
  expect(receipt.runId).toBe(subject.runId)
  expect(receipt.projectId).toBe(attempt.projectId)
  expect(receipt.sandboxSessionId).toBe(attempt.sandboxSessionId)
  expect(receipt.candidateId).toBe(attempt.candidateId)
  expect(receipt.executorProfile).toBe(subject.agent.modelGateway.profileId)
  expect(receipt.modelGateway).toEqual(subject.agent.modelGateway)
  expect(receipt.runner).toEqual(subject.agent.runner)
  expect(receipt.attemptExecutor).toEqual(attempt.executor)
  expect(receipt.taskCapsule).toEqual(attempt.taskCapsule)
  expect(receipt.configurationHash).toBe(attempt.configurationHash)
  assertUUID(String(receipt.runtimeProcessId), 'Agent runtime process id')
  assertTimestamp(String(receipt.startedAt), 'Agent runtime startedAt')
  assertTimestamp(String(receipt.observedAt), 'Agent runtime observedAt')
  expect(Date.parse(String(receipt.observedAt))).toBeGreaterThanOrEqual(
    Date.parse(String(receipt.startedAt)),
  )
  const providerInvocation = assertExactObject(
    receipt.providerInvocation,
    ['requestId', 'state'],
    'Agent provider invocation',
  )
  expect([
    'cancelled', 'completed', 'failed', 'refused-before-provider', 'running', 'timed-out',
  ]).toContain(providerInvocation.state)
  if (providerInvocation.state === 'refused-before-provider') {
    expect(providerInvocation.requestId).toBeNull()
  } else {
    assertUUID(String(providerInvocation.requestId), 'Agent provider request id')
  }
  return receipt
}

test.describe('Golden Agent external qualification', () => {
  test('QG-AGENT-001 executes an exact task, validates its patch, merges it, and undoes the exact merge', async ({ request }) => {
    const openingSandbox = await bootstrapGoldenSandbox('QG-AGENT-001')
    const subject = goldenSubject()
    const releases = await openingSandbox.client.constructorApi.listTemplateReleases(
      { states: ['approved'] },
      { limit: 100 },
    )
    const release = releases.data.items.find((entry) => (
      entry.release.id === subject.sharedArtifacts.templateRelease.id
      && entry.release.contentHash === subject.sharedArtifacts.templateRelease.contentHash
    ))
    expect(release, 'Agent task must resolve the exact approved TemplateRelease').toBeTruthy()
    expect(release!.release.manifest.extensionPaths).toHaveLength(1)
    const writeRoot = release!.release.manifest.extensionPaths[0]!.replace(/\/$/u, '')
    const path = `${writeRoot}/.worksflow-golden-${subject.runId}.txt`
    const expectedBytes = `worksflow-golden-agent:${subject.runId}\n`
    const { created, sandbox } = await createAttempt(
      'QG-AGENT-001',
      `Create exact path ${path} with exact UTF-8 bytes ${JSON.stringify(expectedBytes)}. Change no other path.`,
      openingSandbox,
    )
    const buildContract = await sandbox.client.constructorApi.getBuildContract(
      sandbox.candidate.buildContract.id,
    )
    expect(buildContract.data.contractHash).toBe(sandbox.candidate.buildContract.contentHash)
    const expectedObligations = buildContract.data.contract.obligations
      .filter((entry) => entry.level === 'must' && entry.status === 'ready')
      .map((entry) => entry.id)
      .sort()
    const oracleById = new Map(buildContract.data.contract.oracles.map((entry) => [entry.id, entry]))
    const expectedAcceptanceCriteria = [...new Set(expectedObligations.flatMap((obligationId) => {
      const obligation = buildContract.data.contract.obligations.find((entry) => entry.id === obligationId)!
      return obligation.oracleIds.flatMap((oracleId) => oracleById.get(oracleId)?.acceptanceCriterionIds ?? [])
    }))].sort()
    expect(created.data.taskCapsule.obligationIds).toEqual(expectedObligations)
    expect(created.data.taskCapsule.acceptanceCriterionIds).toEqual(expectedAcceptanceCriteria)
    expect(created.data.taskCapsule.writeSet).toEqual([path])
    expect(created.data.taskCapsule.protectedPaths.length).toBeGreaterThan(0)
    expect(created.data.taskCapsule.allowedTools).toEqual([
      'diagnostic.read', 'file.read', 'file.search', 'file.write', 'shell.exec',
    ])
    const capsule = created.data.taskCapsule as typeof created.data.taskCapsule & {
      readonly buildContract: Readonly<{ id: string; contentHash: string }>
      readonly templateReleases: readonly Readonly<{ id: string; contentHash: string }>[]
    }
    expect(capsule.buildContract).toEqual(sandbox.candidate.buildContract)
    expect(capsule.templateReleases).toEqual([{
      id: subject.sharedArtifacts.templateRelease.id,
      contentHash: subject.sharedArtifacts.templateRelease.contentHash,
    }])
    const ready = await waitForValue(
      () => sandbox.client.agent.getAttempt(created.data.attempt.id),
      (result) => result.data.attempt.state === 'review_ready',
      'AgentAttempt review_ready',
    )
    await assertAgentRuntimeReceipt(request, ready.data.attempt)
    const patch = await sandbox.client.agent.readPatch(ready.data.attempt.id)
    const structured = await sandbox.client.agent.readStructuredResult(ready.data.attempt.id)
    const validation = await sandbox.client.agent.readValidation(ready.data.attempt.id)
    expect(validation.data.decision).toBe('reviewable')
    expect(validation.data.patchContentHash).toBe(patch.data.contentHash)
    expect(patch.data.baseTreeHash).toBe(sandbox.candidate.treeHash)
    expect(patch.data.operations).toHaveLength(1)
    const operation = patch.data.operations[0]!
    expect(operation.kind).toBe('file.upsert')
    expect(operation.path).toBe(path)
    expect(operation.fromPath).toBeUndefined()
    expect(operation.expectedHash).toBeUndefined()
    expect(operation.contentHash).toMatch(/^sha256:[0-9a-f]{64}$/u)
    expect(operation.byteSize).toBeGreaterThan(0)
    expect(operation.mode).toMatch(/^100(?:644|755)$/u)
    expect(created.data.taskCapsule.protectedPaths.some((root) => (
      operation.path === root || operation.path.startsWith(`${root}/`)
    ))).toBe(false)
    expect(structured.data.changedPaths).toEqual([operation.path])
    expect(structured.data.blockers).toEqual([])
    expect(structured.data.verification.map((entry) => entry.commandId).sort())
      .toEqual([...created.data.taskCapsule.verificationCommandIds].sort())
    expect(structured.data.verification.every((entry) => entry.status === 'passed')).toBe(true)
    expect(validation.data.taskCapsule).toEqual({
      id: created.data.taskCapsule.taskId,
      contentHash: created.data.taskCapsule.contentHash,
    })
    expect(patch.data.taskCapsule).toEqual(validation.data.taskCapsule)
    const proposed = await sandbox.client.agent.readPatchFile(
      ready.data.attempt.id,
      path,
      'proposed',
    )
    expect(proposed.data.exists).toBe(true)
    expect(proposed.data.contentHash).toBe(operation.contentHash)
    expect(proposed.data.byteSize).toBe(operation.byteSize)
    expect(new TextDecoder('utf-8', { fatal: true }).decode(proposed.data.value)).toBe(expectedBytes)
    const merge = await sandbox.client.agent.mergePatch(
      ready.data.attempt.id,
      attemptFences(sandbox),
      {
        ifMatch: agentAttemptEtag(ready.data.attempt),
        idempotencyKey: qualificationKey('QG-AGENT-001', 'merge'),
      },
    )
    expect(merge.status).toBe(200)
    expect(merge.data.application).toBeTruthy()
    expect(merge.data.plan.disposition).toBe('planned')
    expect(merge.data.plan.operations).toEqual([operation])
    expect(merge.data.application!.operationId).toBe(merge.data.plan.operationId)
    expect(merge.data.application!.planContentHash).toBe(merge.data.plan.contentHash)
    expect(merge.data.application!.beforeTreeHash).toBe(sandbox.candidate.treeHash)
    const mergedSession = merge.data.session!
    const mergedTree = await sandbox.client.sandbox.getTree(mergedSession.id)
    expect(mergedTree.data.candidate.treeHash).toBe(merge.data.application!.afterTreeHash)
    const mergedFile = mergedTree.data.tree.files.find((entry) => entry.path === path)
    expect(mergedFile?.contentHash).toBe(operation.contentHash)
    const mergedBytes = await sandbox.client.sandbox.readFile(mergedSession.id, path, {
      fence: {
        projectId: sandbox.projectId,
        sessionId: mergedTree.data.session.id,
        sessionEpoch: mergedTree.data.session.sessionEpoch,
        candidateId: mergedTree.data.candidate.id,
        candidateVersion: mergedTree.data.candidate.version,
        journalSequence: mergedTree.data.candidate.journalSequence,
        writerLeaseEpoch: mergedTree.data.candidate.writerLeaseEpoch,
        treeHash: mergedTree.data.candidate.treeHash,
        path,
        contentHash: mergedFile!.contentHash,
      },
    })
    expect(new TextDecoder('utf-8', { fatal: true }).decode(mergedBytes.data.value)).toBe(expectedBytes)
    const undo = await sandbox.client.agent.undoPatch(
      merge.data.plan.id,
      {
        expectedCandidateVersion: mergedSession.candidate.version,
        expectedSessionEpoch: mergedSession.sessionEpoch,
        expectedSessionVersion: mergedSession.version,
        expectedWriterLeaseEpoch: mergedSession.candidate.writerLeaseEpoch,
      },
      {
        ifMatch: agentMergeEtag(merge.data),
        idempotencyKey: qualificationKey('QG-AGENT-001', 'undo'),
      },
    )
    expect(undo.data.application).toBeTruthy()
    expect(undo.data.plan.disposition).toBe('planned')
    expect(undo.data.application!.operationId).toBe(undo.data.plan.operationId)
    expect(undo.data.application!.planContentHash).toBe(undo.data.plan.contentHash)
    expect(undo.data.application!.afterTreeHash).toBe(sandbox.candidate.treeHash)
    const undoneTree = await sandbox.client.sandbox.getTree(undo.data.session!.id)
    expect(undoneTree.data.candidate.treeHash).toBe(sandbox.candidate.treeHash)
    expect(undoneTree.data.tree.files.some((entry) => entry.path === path)).toBe(false)
    const history = await sandbox.client.agent.listMerges(ready.data.attempt.id)
    expect(history.data).toContainEqual(expect.objectContaining({
      plan: expect.objectContaining({ id: merge.data.plan.id }),
      undo: expect.objectContaining({ plan: expect.objectContaining({ id: undo.data.plan.id }) }),
    }))
  })

  test('QG-AGENT-002 proves two browser contexts cannot silently overwrite a fenced Candidate head', async ({ browser, request }) => {
    const environment = goldenQualificationEnvironment()
    const subject = goldenSubject()
    const sandbox = await bootstrapGoldenSandbox('QG-AGENT-002')
    const registrations = await sandbox.client.constructorApi.listTemplateReleases(
      { states: ['approved'] },
      { limit: 100 },
    )
    const release = registrations.data.items.find((entry) => (
      entry.release.id === subject.sharedArtifacts.templateRelease.id
      && entry.release.contentHash === subject.sharedArtifacts.templateRelease.contentHash
    ))
    expect(release).toBeTruthy()
    const roots = release!.release.manifest.extensionPaths.map((entry) => entry.replace(/\/$/u, ''))
    const file = sandbox.tree.tree.files.find((entry) => (
      entry.byteSize <= 4096
      && /\.(?:css|go|html|js|jsx|json|md|py|ts|tsx|txt|ya?ml)$/iu.test(entry.path)
      && roots.some((root) => entry.path === root || entry.path.startsWith(`${root}/`))
    ))
    expect(file, 'Agent conflict vector requires one bounded existing writable text file').toBeTruthy()
    const openingBytes = await sandbox.client.sandbox.readFile(sandbox.session.id, file!.path, {
      fence: {
        projectId: sandbox.projectId,
        sessionId: sandbox.session.id,
        sessionEpoch: sandbox.session.sessionEpoch,
        candidateId: sandbox.candidate.id,
        candidateVersion: sandbox.candidate.version,
        journalSequence: sandbox.candidate.journalSequence,
        writerLeaseEpoch: sandbox.candidate.writerLeaseEpoch,
        treeHash: sandbox.candidate.treeHash,
        path: file!.path,
        contentHash: file!.contentHash,
      },
    })
    const original = new TextDecoder('utf-8', { fatal: true }).decode(openingBytes.data.value)
    const agentNonce = `AGENT_CONFLICT_${subject.runId.replaceAll('-', '_')}`
    const agentBytes = `${original}\n/* ${agentNonce} */\n`
    const { created } = await createAttempt(
      'QG-AGENT-002',
      `Replace exact path ${file!.path} with exact UTF-8 bytes ${JSON.stringify(agentBytes)} and change no other path.`,
      sandbox,
    )
    const ready = await waitForValue(
      () => sandbox.client.agent.getAttempt(created.data.attempt.id),
      (result) => result.data.attempt.state === 'review_ready',
      'QG-AGENT-002 AgentAttempt review_ready',
    )
    await assertAgentRuntimeReceipt(request, ready.data.attempt)
    const patch = await sandbox.client.agent.readPatch(ready.data.attempt.id)
    expect(patch.data.operations).toHaveLength(1)
    expect(patch.data.operations[0]).toEqual(expect.objectContaining({
      kind: 'file.upsert',
      path: file!.path,
    }))
    const browserA = await browser.newContext({
      storageState: browserStorageStateWithSandbox(
        environment.credentials.platform.browserA,
        sandbox,
        'QG-AGENT-002',
      ),
    })
    const browserB = await browser.newContext({
      storageState: browserStorageStateWithSandbox(
        environment.credentials.platform.browserA,
        sandbox,
        'QG-AGENT-002',
      ),
    })
    try {
      const [pageA, pageB] = await Promise.all([browserA.newPage(), browserB.newPage()])
      const exactURL = `${subject.platform.webOrigin}/workbench/complete?view=code`
        + `&bundleId=${subject.sharedArtifacts.buildManifest.id}`
        + `&workspaceRevisionId=${subject.sharedArtifacts.workspaceRevision.id}`
      const open = async (page: typeof pageA) => {
        await page.goto(`${subject.platform.webOrigin}/team/acme/project/${sandbox.projectId}/dashboard`)
        await page.goto(exactURL)
        await expect(page.getByText(sandbox.session.id.slice(0, 8), { exact: true })).toBeVisible()
        await page.getByRole('button', { name: file!.path, exact: true }).click()
        await expect(page.locator('.monaco-editor textarea').first()).toBeVisible()
      }
      await open(pageA)
      await open(pageB)
      const browserBNonce = `BROWSER_B_${subject.runId.replaceAll('-', '_')}`
      const editorB = pageB.locator('.monaco-editor textarea').first()
      await editorB.click()
      await pageB.keyboard.press('Control+End')
      await pageB.keyboard.insertText(`\n/* ${browserBNonce} */\n`)
      await expect(pageB.getByText(/^(?:Saving|Unsaved)$/u)).toBeVisible()
      await expect(pageB.getByText('Saved', { exact: true })).toBeVisible({ timeout: 30_000 })

      const browserANonce = `BROWSER_A_DIRTY_${subject.runId.replaceAll('-', '_')}`
      const editorA = pageA.locator('.monaco-editor textarea').first()
      await editorA.click()
      await pageA.keyboard.press('Control+End')
      await pageA.keyboard.insertText(`\n/* ${browserANonce} */\n`)
      await expect(pageA.locator('.monaco-editor .view-lines').first()).toContainText(browserANonce)
      await expect(pageA.getByText('Stale draft · save blocked', { exact: true })).toBeVisible({ timeout: 30_000 })

      const current = await sandbox.client.sandbox.getTree(sandbox.session.id)
      const currentSandbox = {
        ...sandbox,
        candidate: current.data.candidate,
        fences: sandboxFences(current.headers, current.data.session),
        session: current.data.session,
        tree: current.data,
      }
      const merge = await sandbox.client.agent.mergePatch(
        ready.data.attempt.id,
        attemptFences(currentSandbox),
        {
          ifMatch: agentAttemptEtag(ready.data.attempt),
          idempotencyKey: qualificationKey('QG-AGENT-002', 'conflicted-merge'),
        },
      )
      expect(merge.data.plan.disposition).toBe('conflicted')
      expect(merge.data.application).toBeUndefined()
      expect(merge.data.session).toBeUndefined()
      expect(merge.data.plan.baseTreeHash).toBe(sandbox.candidate.treeHash)
      expect(merge.data.plan.currentTreeHash).toBe(current.data.candidate.treeHash)
      expect(merge.data.plan.proposedTreeHash).toBe(patch.data.proposedTreeHash)
      expect(merge.data.plan.conflicts.map((entry) => entry.path)).toEqual([file!.path])
      const authoritativeFile = current.data.tree.files.find((entry) => entry.path === file!.path)
      expect(authoritativeFile).toBeTruthy()
      const bytes = await sandbox.client.sandbox.readFile(sandbox.session.id, file!.path, {
        fence: {
          projectId: sandbox.projectId,
          sessionId: current.data.session.id,
          sessionEpoch: current.data.session.sessionEpoch,
          candidateId: current.data.candidate.id,
          candidateVersion: current.data.candidate.version,
          journalSequence: current.data.candidate.journalSequence,
          writerLeaseEpoch: current.data.candidate.writerLeaseEpoch,
          treeHash: current.data.candidate.treeHash,
          path: file!.path,
          contentHash: authoritativeFile!.contentHash,
        },
      })
      const content = new TextDecoder('utf-8', { fatal: true }).decode(bytes.data.value)
      expect(content).toContain(browserBNonce)
      expect(content).not.toContain(agentNonce)
      expect(content).not.toContain(browserANonce)
      await expect(pageA.locator('.monaco-editor .view-lines').first()).toContainText(browserANonce)
    } finally {
      await Promise.all([browserA.close(), browserB.close()])
    }
  })

  test('QG-AGENT-003 refuses the one-shot malicious patch and preserves Secret, Canonical, and Deployment isolation', async ({ request }) => {
    const environment = goldenQualificationEnvironment()
    const subject = goldenSubject()
    const sandbox = await bootstrapGoldenSandbox('QG-AGENT-003')
    const owner = platformClient(environment.credentials.platform.owner)
    const canonicalBefore = await owner.artifacts.getRevision(subject.sharedArtifacts.workspaceRevision.id)
    expect(canonicalBefore.data.contentHash).toBe(subject.sharedArtifacts.workspaceRevision.contentHash)
    const deploymentsBefore = await owner.release.listProductionHistory(sandbox.projectId)
    const deploymentSnapshot = JSON.stringify(deploymentsBefore.data)
    const receipt = await consumeGoldenFault(request, 'agent-security-canary')
    expect(receipt.outcome).toBe('refused')
    const findingsResult = await sandbox.client.http.get<unknown>(
      `/v1/qualification/agent-security-canaries/${receipt.resultId}/findings`,
      { query: { fixtureId: subject.fixtureId, runId: subject.runId } },
    )
    const findings = assertExactObject(findingsResult.data, [
      'attemptId', 'authorityId', 'candidate', 'evidenceDigest', 'fixtureId',
      'outcomes', 'resourceId', 'runId', 'schemaVersion',
    ], 'AgentSecurityCanaryFindings')
    expect(findings.schemaVersion).toBe('agent-security-canary-findings/v1')
    expect(findings.fixtureId).toBe(subject.fixtureId)
    expect(findings.runId).toBe(subject.runId)
    expect(findings.authorityId).toBe(receipt.authorityId)
    expect(findings.resourceId).toBe(receipt.resolvedResourceId)
    expect(findings.candidate).toEqual({
      id: sandbox.candidate.id,
      treeHash: sandbox.candidate.treeHash,
      version: sandbox.candidate.version,
    })
    assertDigest(findings.evidenceDigest as string, 'AgentSecurityCanaryFindings.evidenceDigest')
    expect(findings.evidenceDigest).toBe(receipt.adapterResultDigest)
    expect(findings.outcomes).toEqual({
      canonicalMutation: { decision: 'denied', writes: 0 },
      deploymentMutation: { decision: 'denied', writes: 0 },
      protectedPathMutation: { decision: 'denied', writes: 0 },
      publicEgress: { decision: 'denied', requests: 0 },
      secretRead: { bytesRead: 0, decision: 'denied' },
    })
    const attemptId = findings.attemptId as string
    const [attempt, patch, stdout, stderr] = await Promise.all([
      sandbox.client.agent.getAttempt(attemptId),
      sandbox.client.agent.readPatch(attemptId),
      sandbox.client.agent.readStdout(attemptId),
      sandbox.client.agent.readStderr(attemptId),
    ])
    await assertAgentRuntimeReceipt(request, attempt.data.attempt)
    expect(attempt.data.attempt.state).toBe('failed')
    expect(attempt.data.attempt.baseCandidateTreeHash).toBe(sandbox.candidate.treeHash)
    expect(patch.data.operations.length).toBeGreaterThan(0)
    const evidence = [
      JSON.stringify(attempt.data),
      JSON.stringify(patch.data),
      stdout.data,
      stderr.data,
    ].join('\n')
    expect(evidence).not.toContain(environment.credentials.platform.apiA.headers.Authorization)
    expect(evidence).not.toMatch(/(?:provider[_-]?key|session[_-]?cookie|secret[_-]?nonce)\s*[:=]/iu)
    const otherTenant = platformClient(environment.credentials.platform.apiB)
    await expectPlatformFailure(() => otherTenant.sandbox.getSession(sandbox.session.id), 404)
    await expectPlatformFailure(
      () => otherTenant.artifacts.getRevision(goldenSubject().sharedArtifacts.workspaceRevision.id),
      404,
    )
    const current = await sandbox.client.sandbox.getTree(sandbox.session.id)
    expect(current.data.candidate.treeHash).toBe(sandbox.candidate.treeHash)
    expect(current.data.candidate.version).toBe(sandbox.candidate.version)
    const canonicalAfter = await owner.artifacts.getRevision(subject.sharedArtifacts.workspaceRevision.id)
    expect(canonicalAfter.data).toEqual(canonicalBefore.data)
    const deploymentsAfter = await owner.release.listProductionHistory(sandbox.projectId)
    expect(JSON.stringify(deploymentsAfter.data)).toBe(deploymentSnapshot)
  })

  test('QG-AGENT-004 closes runner crash, timeout, cancel, retry, and bounded event recovery', async ({ browser, request }) => {
    const environment = goldenQualificationEnvironment()
    const opening = await bootstrapGoldenSandbox('QG-AGENT-004')
    const context = await browser.newContext({
      storageState: browserStorageStateWithSandbox(
        environment.credentials.platform.browserA,
        opening,
        'QG-AGENT-004',
      ),
    })
    const page = await context.newPage()
    const streamTickets: Array<{ channels?: string[]; cursors?: Array<{ channel: string; lastAckedSeq: number }> }> = []
    page.on('request', (observed) => {
      if (/\/connection-tickets$/u.test(observed.url())) {
        streamTickets.push(observed.postDataJSON() as typeof streamTickets[number])
      }
    })
    const subject = goldenSubject()
    await page.goto(`${subject.platform.webOrigin}/team/acme/project/${opening.projectId}/dashboard`)
    await page.goto(
      `${subject.platform.webOrigin}/workbench/complete?view=code`
      + `&bundleId=${subject.sharedArtifacts.buildManifest.id}`
      + `&workspaceRevisionId=${subject.sharedArtifacts.workspaceRevision.id}`,
    )
    await expect(page.getByText(opening.session.id.slice(0, 8), { exact: true })).toBeVisible()
    const refreshed = await opening.client.sandbox.getTree(opening.session.id)
    const sandbox = {
      ...opening,
      candidate: refreshed.data.candidate,
      fences: sandboxFences(refreshed.headers, refreshed.data.session),
      session: refreshed.data.session,
      tree: refreshed.data,
    }
    const { created } = await createAttempt(
      'QG-AGENT-004',
      'Inspect the exact sealed task inputs and return a bounded no-op result without changing any Candidate file.',
      sandbox,
    )
    await page.getByRole('button', { name: 'Agent', exact: true }).click()
    await expect(page.getByText(/open · seq \d+/u)).toBeVisible({ timeout: 30_000 })
    await waitForValue(
      () => sandbox.client.agent.getAttempt(created.data.attempt.id),
      (result) => result.data.attempt.state === 'running',
      'AgentAttempt running before runner crash',
    )
    await context.setOffline(true)
    await expect(page.getByText(/(?:offline|reconnecting) · seq \d+/u)).toBeVisible({ timeout: 30_000 })
    const crashReceipt = await consumeGoldenFault(request, 'agent-runner-crash')
    assertGoldenFaultTarget(crashReceipt, {
      id: created.data.attempt.id,
      kind: 'agent-attempt',
      projectId: sandbox.projectId,
    })
    const crashed = await waitForValue(
      () => sandbox.client.agent.getAttempt(created.data.attempt.id),
      (result) => result.data.attempt.state === 'failed',
      'crashed AgentAttempt terminal state',
    )
    expect(crashed.data.attempt.exitReason).toMatch(/runner.*crash/iu)
    expect(crashed.data.attempt.baseCandidateTreeHash).toBe(sandbox.candidate.treeHash)
    await assertAgentRuntimeReceipt(request, crashed.data.attempt)
    await context.setOffline(false)
    await expect(page.getByText(/open · seq \d+/u)).toBeVisible({ timeout: 90_000 })
    await expect.poll(() => streamTickets.filter((entry) => entry.channels?.includes('agent')).length)
      .toBeGreaterThan(1)
    expect(streamTickets.some((entry) => entry.cursors?.some((cursor) => (
      cursor.channel === 'agent' && cursor.lastAckedSeq > 0
    )))).toBe(true)
    const events = await sandbox.client.agent.recoverEvents(crashed.data.attempt.id, 0, { limit: 200, maxPages: 4 })
    expect(events.events.length).toBeGreaterThan(0)
    expect(events.events.every((event, index) => (
      event.sequence === index + 1
      && event.versionFrom === event.sequence
      && event.versionTo === event.versionFrom + 1
      && (index === 0 || event.stateFrom === events.events[index - 1]!.stateTo)
      && (index === 0 || event.fenceEpochFrom >= events.events[index - 1]!.fenceEpochTo)
    ))).toBe(true)
    expect(events.events.filter((event) => (
      ['failed', 'timed_out', 'cancelled', 'review_ready', 'verification_failed', 'stale']
        .includes(event.stateTo)
    ))).toHaveLength(1)
    expect(events.events.at(-1)?.stateTo).toBe('failed')
    const firstPage = await sandbox.client.agent.listEvents(crashed.data.attempt.id, 0, 1)
    expect(firstPage.data.events).toHaveLength(1)
    const secondPage = await sandbox.client.agent.listEvents(
      crashed.data.attempt.id,
      firstPage.data.lastSequence,
      1,
    )
    expect(secondPage.data.events[0]?.sequence).toBe(firstPage.data.lastSequence + 1)
    const repeatedCrash = await sandbox.client.agent.getAttempt(crashed.data.attempt.id)
    expect(repeatedCrash.data).toEqual(crashed.data)

    const retryReason = 'Golden recovery after the one-shot runner crash'
    const next = await sandbox.client.agent.retryAttempt(
      crashed.data.attempt.id,
      retryReason,
      {
        ifMatch: agentAttemptEtag(crashed.data.attempt),
        idempotencyKey: qualificationKey('QG-AGENT-004', 'reasoned-retry'),
      },
    )
    expect(next.data.attempt.id).not.toBe(crashed.data.attempt.id)
    expect(next.data.attempt.parentAttemptId).toBe(crashed.data.attempt.id)
    expect(next.data.attempt.retryReason).toBe(retryReason)
    expect(next.data.attempt.baseCandidateTreeHash).toBe(sandbox.candidate.treeHash)
    await waitForValue(
      () => sandbox.client.agent.getAttempt(next.data.attempt.id),
      (result) => result.data.attempt.state === 'running',
      'AgentAttempt running before runner timeout',
    )
    const timeoutReceipt = await consumeGoldenFault(request, 'agent-runner-timeout')
    assertGoldenFaultTarget(timeoutReceipt, {
      id: next.data.attempt.id,
      kind: 'agent-attempt',
      projectId: sandbox.projectId,
    })
    const timedOut = await waitForValue(
      () => sandbox.client.agent.getAttempt(next.data.attempt.id),
      (result) => result.data.attempt.state === 'timed_out',
      'AgentAttempt timed_out state',
    )
    expect(timedOut.data.attempt.exitReason).toMatch(/runner.*timeout|deadline/iu)
    await assertAgentRuntimeReceipt(request, timedOut.data.attempt)
    expect((await sandbox.client.agent.getAttempt(crashed.data.attempt.id)).data).toEqual(crashed.data)

    const cancellable = await sandbox.client.agent.createAttempt(
      sandbox.session.id,
      {
        executorProfile: goldenSubject().agent.modelGateway.profileId,
        instruction: 'Return a bounded cancellation acknowledgement without changing files.',
        taskKey: 'qg-agent-004-cancel',
      },
      { idempotencyKey: qualificationKey('QG-AGENT-004', 'cancel-attempt') },
    )
    const runningCancellation = await waitForValue(
      () => sandbox.client.agent.getAttempt(cancellable.data.attempt.id),
      (result) => result.data.attempt.state === 'running',
      'AgentAttempt running before explicit cancellation',
    )
    const cancellationReason = 'Golden explicit cancellation'
    const cancellationKey = qualificationKey('QG-AGENT-004', 'cancel')
    const cancelled = await sandbox.client.agent.cancelAttempt(
      cancellable.data.attempt.id,
      cancellationReason,
      {
        ifMatch: agentAttemptEtag(runningCancellation.data.attempt),
        idempotencyKey: cancellationKey,
      },
    )
    expect(cancelled.data.state).toBe('cancelled')
    const replayedCancellation = await sandbox.client.agent.cancelAttempt(
      cancellable.data.attempt.id,
      cancellationReason,
      {
        ifMatch: agentAttemptEtag(runningCancellation.data.attempt),
        idempotencyKey: cancellationKey,
      },
    )
    expect(replayedCancellation.data).toEqual(cancelled.data)
    const durableCancellation = await sandbox.client.agent.getAttempt(cancellable.data.attempt.id)
    expect(durableCancellation.data.attempt.state).toBe('cancelled')
    expect(durableCancellation.data.attempt.exitReason).toMatch(/cancel/iu)
    expect(durableCancellation.data.attempt.evidence.stdout).toBeTruthy()
    await assertAgentRuntimeReceipt(request, durableCancellation.data.attempt)
    const stdoutAtCancel = await sandbox.client.agent.readStdout(cancellable.data.attempt.id)
    const cancellationEvents = await sandbox.client.agent.recoverEvents(
      cancellable.data.attempt.id,
      0,
      { limit: 200, maxPages: 4 },
    )
    expect(cancellationEvents.events.filter((event) => event.stateTo === 'cancelled')).toHaveLength(1)
    const afterCancellation = await sandbox.client.agent.getAttempt(cancellable.data.attempt.id)
    const stdoutAfterCancel = await sandbox.client.agent.readStdout(cancellable.data.attempt.id)
    expect(afterCancellation.data).toEqual(durableCancellation.data)
    expect(stdoutAfterCancel.data).toBe(stdoutAtCancel.data)
    expect(stdoutAfterCancel.headers.get('x-content-hash')).toBe(stdoutAtCancel.headers.get('x-content-hash'))
    const finalTree = await sandbox.client.sandbox.getTree(sandbox.session.id)
    expect(finalTree.data.candidate.treeHash).toBe(sandbox.candidate.treeHash)
    expect(finalTree.data.candidate.version).toBe(sandbox.candidate.version)
    await context.close()
  })
})
