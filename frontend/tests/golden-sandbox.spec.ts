import {
  sandboxFences,
  type SandboxRuntimePortDto,
} from '@/lib/platform/sandbox-contract'

import { expect, goldenQualificationEnvironment, test, type Page } from './qualification-runtime'
import {
  assertDigest,
  assertExactObject,
  assertUUID,
  assertGoldenFaultTarget,
  bootstrapGoldenSandbox,
  browserStorageStateWithSandbox,
  consumeGoldenFault,
  expectPlatformFailure,
  goldenPrincipal,
  goldenSubject,
  platformClient,
  qualificationKey,
  waitForValue,
  type GoldenSandbox,
} from './golden-qualification-support'

function workbenchURL() {
  const subject = goldenSubject()
  return `${subject.platform.webOrigin}/workbench/complete?view=code`
    + `&bundleId=${subject.sharedArtifacts.buildManifest.id}`
    + `&workspaceRevisionId=${subject.sharedArtifacts.workspaceRevision.id}`
}

async function openExactBrowserWorkspace(
  page: Page,
  sandbox: GoldenSandbox,
) {
  const subject = goldenSubject()
  await page.goto(`${subject.platform.webOrigin}/team/acme/project/${sandbox.projectId}/dashboard`)
  await page.goto(workbenchURL())
  await expect(page).toHaveURL(new RegExp(
    `^${subject.platform.webOrigin.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}/`,
  ))
  await expect(page.getByText(sandbox.session.id.slice(0, 8), { exact: true })).toBeVisible()
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
}

async function startApprovedHTTPService(caseId: string) {
  const subject = goldenSubject()
  const sandbox = await bootstrapGoldenSandbox(caseId)
  const page = await sandbox.client.constructorApi.listTemplateReleases(
    { states: ['approved'] },
    { limit: 100 },
  )
  const registrations = page.data.items.filter((entry) => (
    entry.release.id === subject.sharedArtifacts.templateRelease.id
    && entry.release.contentHash === subject.sharedArtifacts.templateRelease.contentHash
    && entry.policy.state === 'approved'
  ))
  expect(registrations).toHaveLength(1)
  const registration = await sandbox.client.constructorApi.getTemplateRelease(
    registrations[0]!.release.id,
    {
      contentHash: registrations[0]!.release.contentHash,
      subjectHash: registrations[0]!.release.subjectHash,
    },
  )
  expect(registration.data).toEqual(registrations[0])
  const runtimeProfiles = subject.sandbox.serviceProfiles.filter((profile) => (
    profile.protocol === 'http'
    && Boolean(registration.data.release.manifest.commands[profile.id])
    && registration.data.release.manifest.ports.some((port) => (
      port.name === profile.id
      && port.serviceId === profile.service
      && port.protocol === profile.protocol
      && port.exposure === 'preview'
    ))
  ))
  expect(runtimeProfiles, 'Fixture must bind one approved HTTP command/port profile').toHaveLength(1)
  const runtimeProfile = runtimeProfiles[0]!
  const service = sandbox.session.allowedServices.find((entry) => (
    entry.id === runtimeProfile.service
    && entry.profiles.includes(runtimeProfile.id)
    && entry.templateRelease.id === registration.data.release.id
    && entry.templateRelease.contentHash === registration.data.release.contentHash
  ))
  expect(service, 'SandboxSession must bind the exact approved service and command').toBeTruthy()
  const command = registration.data.release.manifest.commands[runtimeProfile.id]!
  const declaredPorts = registration.data.release.manifest.ports.filter((port) => (
    port.name === runtimeProfile.id
    && port.serviceId === runtimeProfile.service
    && port.protocol === runtimeProfile.protocol
    && port.exposure === 'preview'
  ))
  expect(declaredPorts).toHaveLength(1)
  const declaredPort = declaredPorts[0]!
  const healthChecks = registration.data.release.manifest.healthChecks.filter((check) => (
    check.serviceId === runtimeProfile.service && check.portName === declaredPort.name
  ))
  expect(healthChecks).toHaveLength(1)
  const started = await sandbox.client.sandbox.startProcess(
    sandbox.session.id,
    { serviceId: runtimeProfile.service, commandId: runtimeProfile.id },
    {
      fences: sandbox.fences,
      idempotencyKey: qualificationKey(caseId, 'process'),
    },
  )
  expect(started.data.process.serviceId).toBe(runtimeProfile.service)
  expect(started.data.process.commandId).toBe(runtimeProfile.id)
  expect(started.data.process.templateRelease).toEqual({
    id: registration.data.release.id,
    contentHash: registration.data.release.contentHash,
  })
  expect(started.data.process.argv).toEqual(command.argv)
  expect(started.data.process.workingDirectory).toBe(command.workingDirectory)
  const process = await waitForValue(
    () => sandbox.client.sandbox.getProcess(sandbox.session.id, started.data.process.id),
    (result) => ['running', 'exited', 'failed', 'orphaned'].includes(result.data.process.state),
    `${caseId} approved process`,
  )
  expect(process.data.process.state).toBe('running')
  const exactPort = await waitForValue(
    () => sandbox.client.sandbox.listPorts(sandbox.session.id),
    (result) => result.data.ports.some((port) => (
      port.name === declaredPort.name
      && port.serviceId === declaredPort.serviceId
      && port.number === declaredPort.number
      && port.protocol === declaredPort.protocol
      && port.state === 'listening'
      && port.healthy
      && port.previewable
    )),
    `${caseId} approved port health`,
  ).then((result) => result.data.ports.find((port) => port.name === declaredPort.name)!)
  const tree = await sandbox.client.sandbox.getTree(sandbox.session.id)
  const current: GoldenSandbox = {
    ...sandbox,
    candidate: tree.data.candidate,
    fences: sandboxFences(tree.headers, tree.data.session),
    session: tree.data.session,
    tree: tree.data,
  }
  return {
    command,
    healthCheck: healthChecks[0]!,
    port: exactPort as SandboxRuntimePortDto,
    process: process.data.process,
    registration: registration.data,
    runtimeProfile,
    sandbox: current,
  }
}

test.describe('Golden Sandbox external qualification', () => {
  test('QG-SANDBOX-001 bootstraps the approved Template and opens the real browser IDE on the exact Candidate', async ({ browser }) => {
    const environment = goldenQualificationEnvironment()
    const subject = goldenSubject()
    const sandbox = await bootstrapGoldenSandbox('QG-SANDBOX-001')
    expect(sandbox.session.runnerImageDigest).toBe(subject.sandbox.runner.imageDigest)
    expect(sandbox.tree.tree.treeHash).toBe(sandbox.candidate.treeHash)
    const runtime = await sandbox.client.http.get<unknown>(
      `/v1/qualification/sandbox-sessions/${sandbox.session.id}/runtime-binding`,
      { query: { fixtureId: subject.fixtureId, runId: subject.runId } },
    )
    const binding = assertExactObject(runtime.data, [
      'fixtureId', 'projectId', 'runId', 'runner', 'runtimeProfileId',
      'schemaVersion', 'services', 'sessionId',
    ], 'SandboxRuntimeBinding')
    expect(binding.schemaVersion).toBe('sandbox-runtime-binding-observation/v1')
    expect(binding.fixtureId).toBe(subject.fixtureId)
    expect(binding.runId).toBe(subject.runId)
    expect(binding.projectId).toBe(sandbox.projectId)
    expect(binding.sessionId).toBe(sandbox.session.id)
    expect(binding.runtimeProfileId).toBe(subject.sandbox.runtimeProfileId)
    expect(binding.runner).toEqual(subject.sandbox.runner)
    expect(binding.services).toEqual(subject.sandbox.serviceProfiles)

    let sessionCreates = 0
    const context = await browser.newContext({
      storageState: browserStorageStateWithSandbox(
        environment.credentials.platform.browserA,
        sandbox,
        'QG-SANDBOX-001',
      ),
    })
    try {
      const page = await context.newPage()
      page.on('request', (observed) => {
        if (observed.method() === 'POST' && /\/v1\/projects\/[^/]+\/sandbox-sessions$/u.test(observed.url())) {
          sessionCreates += 1
        }
      })
      await openExactBrowserWorkspace(page, sandbox)
      expect(sessionCreates).toBe(0)
      const pointers = await page.evaluate(({ buildManifestId, projectId }) => ({
        active: localStorage.getItem(`worksflow:sandbox-active-candidate:${projectId}`),
        session: localStorage.getItem(`worksflow:sandbox:${projectId}:${buildManifestId}`),
      }), {
        buildManifestId: subject.sharedArtifacts.buildManifest.id,
        projectId: sandbox.projectId,
      })
      expect(JSON.parse(pointers.session!)).toEqual({
        candidateId: sandbox.candidate.id,
        sessionId: sandbox.session.id,
        sessionKey: qualificationKey('QG-SANDBOX-001', 'sandbox-open'),
      })
      expect(JSON.parse(pointers.active!)).toEqual({
        buildManifestId: subject.sharedArtifacts.buildManifest.id,
        candidateId: sandbox.candidate.id,
      })
    } finally {
      await context.close()
    }
  })

  test('QG-SANDBOX-002 preserves dirty editor state across autosave, checkpoint, and stream reconnection without Blueprint reload', async ({ browser, request }) => {
    const environment = goldenQualificationEnvironment()
    const sandbox = await bootstrapGoldenSandbox('QG-SANDBOX-002')
    const editable = sandbox.tree.tree.files.find((entry) => (
      /\.(?:css|go|html|js|jsx|json|md|py|ts|tsx|txt|ya?ml)$/iu.test(entry.path)
    ))
    expect(editable, 'Candidate must expose a real text editor file').toBeTruthy()
    const context = await browser.newContext({
      storageState: browserStorageStateWithSandbox(
        environment.credentials.platform.browserA,
        sandbox,
        'QG-SANDBOX-002',
      ),
    })
    try {
      const page = await context.newPage()
      let blueprintLoads = 0
      let mainNavigations = 0
      let streamTickets = 0
      page.on('request', (observed) => {
        if (/\/blueprints(?:[/?]|$)/u.test(observed.url())) blueprintLoads += 1
        if (/\/connection-tickets$/u.test(observed.url())) streamTickets += 1
      })
      page.on('framenavigated', (frame) => {
        if (frame === page.mainFrame()) mainNavigations += 1
      })
      await openExactBrowserWorkspace(page, sandbox)
      await expect.poll(() => blueprintLoads).toBeGreaterThan(0)
      await page.getByRole('button', { name: editable!.path, exact: true }).click()
      const editor = page.locator('.monaco-editor textarea').first()
      await expect(editor).toBeVisible()
      const blueprintLoadsBeforeEdit = blueprintLoads
      const navigationsBeforeEdit = mainNavigations
      const marker = `GOLDEN_AUTOSAVE_${goldenSubject().runId.replaceAll('-', '_')}`
      await editor.click()
      await page.keyboard.press('Control+End')
      await page.keyboard.insertText(`\n/* ${marker} */\n`)
      await expect(page.locator('.monaco-editor .view-lines').first()).toContainText(marker)
      await expect(page.getByText(/^(?:Saving|Unsaved)$/u)).toBeVisible()
      await expect(page.getByText('Saved', { exact: true })).toBeVisible({ timeout: 30_000 })
      const checkpoint = page.getByRole('button', { name: 'Checkpoint', exact: true })
      await expect(checkpoint).toBeEnabled()
      await checkpoint.click()
      await expect(page.getByRole('button', { name: 'Checkpointed', exact: true })).toBeVisible()
      await page.getByRole('button', { name: 'Terminal', exact: true }).click()
      await expect(page.locator('.xterm-helper-textarea')).toBeVisible()
      await expect.poll(() => streamTickets).toBeGreaterThan(0)
      const ticketsBeforeFault = streamTickets
      const receipt = await consumeGoldenFault(request, 'sandbox-dependency-crash')
      assertGoldenFaultTarget(receipt, {
        id: sandbox.session.id,
        kind: 'sandbox-session',
        projectId: sandbox.projectId,
      })
      await expect.poll(() => streamTickets, { timeout: 90_000 }).toBeGreaterThan(ticketsBeforeFault)
      await expect(page.locator('.monaco-editor .view-lines').first()).toContainText(marker)
      expect(mainNavigations).toBe(navigationsBeforeEdit)
      expect(blueprintLoads).toBe(blueprintLoadsBeforeEdit)
      await expect(page.getByRole('button', { name: 'Checkpointed', exact: true })).toBeVisible()
    } finally {
      await context.close()
    }
  })

  test('QG-SANDBOX-003 runs a real process and PTY and verifies the declared port health', async ({ browser }) => {
    const environment = goldenQualificationEnvironment()
    const started = await startApprovedHTTPService('QG-SANDBOX-003')
    expect(started.process.serviceId).toBe(started.runtimeProfile.service)
    expect(started.process.commandId).toBe(started.runtimeProfile.id)
    expect(started.process.templateRelease.id).toBe(started.registration.release.id)
    expect(started.port.name).toBe(started.runtimeProfile.id)
    expect(started.port.serviceId).toBe(started.runtimeProfile.service)
    expect(started.port.protocol).toBe(started.runtimeProfile.protocol)
    const preview = await started.sandbox.client.sandbox.createPreviewLink(
      started.sandbox.session.id,
      started.port.name,
      {
        fences: started.sandbox.fences,
        idempotencyKey: qualificationKey('QG-SANDBOX-003', 'health-link'),
      },
    )
    const healthURL = new URL(preview.data.url)
    healthURL.pathname = `${healthURL.pathname.replace(/\/?$/u, '/')}${started.healthCheck.path.replace(/^\//u, '')}`
    const health = await fetch(healthURL, { redirect: 'error' })
    expect(health.ok).toBe(true)

    const context = await browser.newContext({
      storageState: browserStorageStateWithSandbox(
        environment.credentials.platform.browserA,
        started.sandbox,
        'QG-SANDBOX-003',
      ),
    })
    try {
      const page = await context.newPage()
      let streamTickets = 0
      page.on('request', (observed) => {
        if (/\/connection-tickets$/u.test(observed.url())) streamTickets += 1
      })
      await openExactBrowserWorkspace(page, started.sandbox)
      await page.getByRole('button', { name: 'Terminal', exact: true }).click()
      const terminal = page.locator('.xterm-helper-textarea')
      await expect(terminal).toBeVisible()
      await expect.poll(() => streamTickets).toBeGreaterThan(0)
      const nonce = `PTY_${goldenSubject().runId.replaceAll('-', '')}`
      await terminal.fill(`printf '%s\\n' '${nonce}'`)
      await page.keyboard.press('Enter')
      await expect(page.locator('.xterm-rows')).toContainText(nonce, { timeout: 30_000 })
    } finally {
      await context.close()
    }
  })

  test('QG-SANDBOX-004 verifies Preview reaches the real API and database with exact tenant and Candidate fences', async ({ browser }) => {
    const subject = goldenSubject()
    const started = await startApprovedHTTPService('QG-SANDBOX-004')
    const preview = await started.sandbox.client.sandbox.createPreviewLink(
      started.sandbox.session.id,
      started.port.name,
      {
        fences: started.sandbox.fences,
        idempotencyKey: qualificationKey('QG-SANDBOX-004', 'preview'),
      },
    )
    const context = await browser.newContext()
    try {
      const page = await context.newPage()
      const opened = await page.goto(preview.data.url)
      expect(opened?.status()).toBe(200)
      const result = await page.evaluate(async (input) => {
        const endpoint = new URL('api/qualification/golden-preview-integration', `${location.href.replace(/\/?$/u, '/')}`)
        const response = await fetch(endpoint, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        return { body: await response.json(), status: response.status }
      }, {
        candidate: {
          id: started.sandbox.candidate.id,
          treeHash: started.sandbox.candidate.treeHash,
          version: started.sandbox.candidate.version,
        },
        fixtureId: subject.fixtureId,
        projectId: started.sandbox.projectId,
        runId: subject.runId,
        schemaVersion: 'golden-preview-integration-request/v1',
        session: {
          epoch: started.sandbox.session.sessionEpoch,
          id: started.sandbox.session.id,
        },
      })
      expect(result.status).toBe(200)
      const receipt = assertExactObject(result.body, [
        'api', 'candidate', 'crossTenant', 'database', 'fixtureId', 'projectId',
        'runId', 'schemaVersion', 'session', 'staleHead', 'tenantId',
      ], 'GoldenPreviewIntegrationReceipt')
      expect(receipt.schemaVersion).toBe('golden-preview-integration-receipt/v1')
      expect(receipt.fixtureId).toBe(subject.fixtureId)
      expect(receipt.runId).toBe(subject.runId)
      expect(receipt.projectId).toBe(started.sandbox.projectId)
      expect(receipt.tenantId).toBe(goldenPrincipal('platform-user-a').tenantId)
      expect(receipt.candidate).toEqual({
        id: started.sandbox.candidate.id,
        treeHash: started.sandbox.candidate.treeHash,
        version: started.sandbox.candidate.version,
      })
      expect(receipt.session).toEqual({
        epoch: started.sandbox.session.sessionEpoch,
        id: started.sandbox.session.id,
      })
      const api = assertExactObject(receipt.api, [
        'imageDigest', 'origin', 'requestId',
      ], 'GoldenPreviewIntegrationReceipt.api')
      expect(api.imageDigest).toBe(subject.reference.apiImageDigest)
      expect(api.origin).toBe(subject.reference.apiOrigin)
      assertUUID(api.requestId as string, 'GoldenPreviewIntegrationReceipt.api.requestId')
      const database = assertExactObject(receipt.database, [
        'createDigest', 'engine', 'persistedAfterReload', 'readDigest',
        'recordId', 'reloadDigest',
      ], 'GoldenPreviewIntegrationReceipt.database')
      assertUUID(database.recordId as string, 'GoldenPreviewIntegrationReceipt.database.recordId')
      assertDigest(database.createDigest as string, 'GoldenPreviewIntegrationReceipt.database.createDigest')
      expect(database.readDigest).toBe(database.createDigest)
      expect(database.reloadDigest).toBe(database.createDigest)
      expect(database.persistedAfterReload).toBe(true)
      expect(database.engine).toBe('postgresql')
      expect(receipt.crossTenant).toEqual({ rowsAffected: 0, status: 404 })
      expect(receipt.staleHead).toEqual({ rowsAffected: 0, status: 409 })
    } finally {
      await context.close()
    }
    const tenantB = platformClient(goldenQualificationEnvironment().credentials.platform.apiB)
    await expectPlatformFailure(
      () => tenantB.sandbox.createPreviewLink(
        started.sandbox.session.id,
        started.port.name,
        { fences: started.sandbox.fences },
      ),
      404,
    )
    await expectPlatformFailure(
      () => started.sandbox.client.sandbox.createPreviewLink(
        started.sandbox.session.id,
        started.port.name,
        {
          fences: {
            etag: started.sandbox.fences.etag,
            sessionEpoch: started.sandbox.fences.sessionEpoch + 1,
          },
        },
      ),
      409,
    )
  })
})
