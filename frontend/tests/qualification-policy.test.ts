import assert from 'node:assert/strict'
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'

import {
  canonicalJSON,
  compareCanonicalUTF8,
  parseCanonicalJSON,
  parseStrictJSON,
} from '../scripts/qualification-core.mjs'
import { validateQualificationInventory } from '../scripts/qualification-inventory.mjs'
import { qualificationPlanDigest } from '../scripts/qualification-plan.mjs'
import { normalizePlaywrightQualification } from '../scripts/normalize-playwright-qualification.mjs'
import { validateGoldenSource } from '../scripts/qualification-source-policy.mjs'

function expectFailure(action: () => unknown, pattern: RegExp) {
  assert.throws(action, pattern)
}

const reviewedGoldenSuites = [
  {
    id: 'agent-golden-external',
    file: 'frontend/tests/golden-agent.spec.ts',
    cases: [
      ['QG-AGENT-001', 'QG-AGENT-001 executes an exact task, validates its patch, merges it, and undoes the exact merge'],
      ['QG-AGENT-002', 'QG-AGENT-002 proves two browser contexts cannot silently overwrite a fenced Candidate head'],
      ['QG-AGENT-003', 'QG-AGENT-003 refuses the one-shot malicious patch and preserves Secret, Canonical, and Deployment isolation'],
      ['QG-AGENT-004', 'QG-AGENT-004 closes runner crash, timeout, cancel, retry, and bounded event recovery'],
    ],
  },
  {
    id: 'lsp-golden-external',
    file: 'frontend/tests/golden-lsp.spec.ts',
    cases: [
      ['QG-LSP-001', 'QG-LSP-001 binds an approved real language server and verifies its exact capabilities'],
      ['QG-LSP-002', 'QG-LSP-002 drops a stale binding, rebinds after head change, and enforces Candidate-CAS-only save'],
      ['QG-LSP-003', 'QG-LSP-003 rejects malicious protocol behavior and closes crash, drift, resource, and audit privacy faults'],
    ],
  },
  {
    id: 'reference-ai-golden-external',
    file: 'frontend/tests/golden-reference.spec.ts',
    cases: [
      ['QG-REFERENCE-001', 'QG-REFERENCE-001 verifies exact service images, commands, migration admission, liveness, and readiness'],
      ['QG-REFERENCE-002', 'QG-REFERENCE-002 proves persistent idempotent conversation and message create, replay, restart, and read'],
      ['QG-REFERENCE-003', 'QG-REFERENCE-003 verifies typed monotonic SSE cursor reconnect and durable terminal recovery'],
      ['QG-REFERENCE-004', 'QG-REFERENCE-004 proves cancel, bounded reasoned retry, timeout, and exactly one terminal state'],
      ['QG-REFERENCE-005', 'QG-REFERENCE-005 proves independent A and B tenant isolation with redacted actor-bound audit'],
      ['QG-REFERENCE-006', 'QG-REFERENCE-006 closes real gateway outage, rate limiting, retention binding, and diagnostic redaction'],
    ],
  },
  {
    id: 'release-golden-external',
    file: 'frontend/tests/golden-release.spec.ts',
    cases: [
      ['QG-RELEASE-001', 'QG-RELEASE-001 proves canonical handoff and preview happy, migration-fail, health-fail, and single-flight outcomes'],
      ['QG-RELEASE-002', 'QG-RELEASE-002 proves same-digest production promotion and exact revision rollback'],
      ['QG-RELEASE-003', 'QG-RELEASE-003 reconciles timeout-after-commit, not-found, acknowledgement drift, conflict, and operator CAS'],
      ['QG-RELEASE-004', 'QG-RELEASE-004 enforces mutation maintenance and converges legacy v1 with legacy-v3 writer races'],
      ['QG-RELEASE-005', 'QG-RELEASE-005 blocks nested authority drift, database clock skew, and orphan Run or Operation state'],
    ],
  },
  {
    id: 'sandbox-golden-external',
    file: 'frontend/tests/golden-sandbox.spec.ts',
    cases: [
      ['QG-SANDBOX-001', 'QG-SANDBOX-001 bootstraps the approved Template and opens the real browser IDE on the exact Candidate'],
      ['QG-SANDBOX-002', 'QG-SANDBOX-002 preserves dirty editor state across autosave, checkpoint, and stream reconnection without Blueprint reload'],
      ['QG-SANDBOX-003', 'QG-SANDBOX-003 runs a real process and PTY and verifies the declared port health'],
      ['QG-SANDBOX-004', 'QG-SANDBOX-004 verifies Preview reaches the real API and database with exact tenant and Candidate fences'],
    ],
  },
] as const

function main() {
  assert.equal(canonicalJSON({ z: [2, 1], a: true }), '{"a":true,"z":[2,1]}')
  assert.equal(canonicalJSON({ value: 'line\u2028separator' }), '{"value":"line\\u2028separator"}')
  assert.ok(compareCanonicalUTF8('\ue000', '\u{10000}') < 0)
  assert.equal(canonicalJSON({ '\u{10000}': 2, '\ue000': 1 }), '{"\ue000":1,"𐀀":2}')
  expectFailure(() => canonicalJSON('\ud800'), /unpaired UTF-16 surrogate/)
  assert.deepEqual(parseCanonicalJSON(Buffer.from('{"a":1}\n'), 'canonical fixture'), { a: 1 })
  expectFailure(
    () => parseCanonicalJSON(Buffer.from('{"a":1,"a":1}'), 'duplicate fixture'),
    /must be canonical JSON/,
  )
  expectFailure(
    () => parseCanonicalJSON(Buffer.from('{ "a": 1 }'), 'formatted fixture'),
    /must be canonical JSON/,
  )
  assert.deepEqual(parseStrictJSON(Buffer.from('{ "a": 1 }'), 'strict fixture'), { a: 1 })
  expectFailure(
    () => parseStrictJSON(Buffer.from('{"a":1,"a":2}'), 'duplicate strict fixture'),
    /duplicate JSON name/,
  )

  const crossLanguageRoot = join(
    process.cwd(), '..', 'backend', 'internal', 'qualificationreceipt', 'testdata', 'plan-vector',
  )
  const crossLanguageManifest = JSON.parse(
    readFileSync(join(crossLanguageRoot, 'qualification/manifest.json'), 'utf8'),
  )
  assert.equal(
    qualificationPlanDigest(crossLanguageManifest, crossLanguageRoot),
    'sha256:d754b92262a101668872685952bd7dd12c0a6a0216d777a722f6ea1c46a72aea',
  )

  const root = mkdtempSync(join(tmpdir(), 'worksflow-qualification-policy-'))
  try {
    const tests = join(root, 'frontend/tests')
    mkdirSync(tests, { recursive: true })
    writeFileSync(join(tests, 'golden-clean.spec.ts'), [
      "import { expect, test } from './qualification-runtime'",
      "test('QG-REFERENCE-001 closes the reference application', async ({ request }) => {",
      "  const response = await request.get('https://golden.example.test/health')",
      '  expect(response.status()).toBe(200)',
      '})',
      '',
    ].join('\n'))
    validateGoldenSource(root, 'frontend/tests/golden-clean.spec.ts')

    writeFileSync(join(tests, 'golden-route-mock.spec.ts'), [
      "import { test } from './qualification-runtime'",
      "test('mocks', async ({ page }) => {",
      "  await page.route('**/health', async (route) => route.fulfill({ status: 200 }))",
      '})',
      '',
    ].join('\n'))
    expectFailure(
      () => validateGoldenSource(root, 'frontend/tests/golden-route-mock.spec.ts'),
      /forbidden Playwright method route/,
    )

    writeFileSync(join(tests, 'golden-skipped.spec.ts'), [
      "import { test } from './qualification-runtime'",
      "test.skip('not ready', async () => {})",
      '',
    ].join('\n'))
    expectFailure(
      () => validateGoldenSource(root, 'frontend/tests/golden-skipped.spec.ts'),
      /forbidden Playwright method skip/,
    )

    const sourceDocument = 'docs/qualification.md'
    mkdirSync(join(root, 'docs'), { recursive: true })
    writeFileSync(join(root, sourceDocument), '# Qualification\n')
    const criterionPath = 'contracts/reference-acceptance-criteria.json'
    mkdirSync(join(root, 'contracts'))
    writeFileSync(join(root, criterionPath), canonicalJSON({
      applicationId: 'qualification-fixture',
      criteria: [{
        id: 'AC-REFERENCE-001',
        requirementIds: ['REQ-REFERENCE-PERSISTENCE'],
        statement: 'The reference application persists an exact user message.',
      }],
      schemaVersion: 'reference-acceptance-criteria/v1',
    }))

    for (const suite of reviewedGoldenSuites) {
      writeFileSync(join(root, suite.file), [
        "import { test } from './qualification-runtime'",
        ...suite.cases.map(([, title]) => `test('${title}', async () => {})`),
        '',
      ].join('\n'))
    }
    const reviewedInventory = {
      schemaVersion: 'worksflow-qualification-test-inventory/v2',
      criterionSources: [{
        applicationId: 'qualification-fixture',
        path: criterionPath,
        schemaVersion: 'reference-acceptance-criteria/v1',
        suiteId: 'reference-ai-golden-external',
      }],
      cases: reviewedGoldenSuites.flatMap((suite) => suite.cases.map(([caseId, title], index) => ({
        caseId,
        contractCriterionIds: suite.id === 'reference-ai-golden-external' && index === 0
          ? ['AC-REFERENCE-001']
          : [],
        suiteId: suite.id,
        mode: 'qualification',
        file: suite.file,
        title,
        requirementIds: ['AIC-E2E-003'],
      }))),
    }
    mkdirSync(join(root, 'qualification'))
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(reviewedInventory))
    const manifest = {
      schemaVersion: 'worksflow-qualification-manifest/v1',
      subject: 'constructor',
      sourceDocuments: [sourceDocument],
      qualificationSupportPaths: [
        criterionPath,
        ...reviewedGoldenSuites.map((suite) => suite.file),
        'qualification/test-inventory.json',
      ],
      policy: {
        stageExitRequiresExternalQualification: true,
        allowSkippedTests: false,
        allowMocks: false,
        allowMutableRuntimeImages: false,
        credentialBearingArtifacts: 'restricted-encrypted-until-revocation',
        passingInternalSuitesAreStageExitEvidence: false,
      },
      suites: reviewedGoldenSuites.map((suite) => ({
        id: suite.id,
        mode: 'external-qualification',
        executionKind: 'playwright',
        status: 'not-qualified',
        coverage: 'external-complete',
        requirementIds: ['AIC-E2E-003'],
        qualificationGroup: 'golden',
        qualificationCommand: 'qualification:golden',
        testPaths: [suite.file],
        ...(suite.id === 'reference-ai-golden-external'
          ? {
              criterionSource: {
                path: criterionPath,
                schemaVersion: 'reference-acceptance-criteria/v1',
                applicationId: 'qualification-fixture',
              },
            }
          : {}),
        requiredArtifacts: ['playwright-results'],
        blockers: ['external endpoint is not configured'],
      })),
    }
    validateQualificationInventory(root, manifest)

    const missingReviewedCase = structuredClone(reviewedInventory)
    missingReviewedCase.cases.pop()
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(missingReviewedCase))
    expectFailure(
      () => validateQualificationInventory(root, manifest),
      /must contain the exact reviewed 22 cases/,
    )

    const extraReviewedCase = structuredClone(reviewedInventory)
    extraReviewedCase.cases.push(structuredClone(extraReviewedCase.cases.at(-1)!))
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(extraReviewedCase))
    expectFailure(
      () => validateQualificationInventory(root, manifest),
      /must contain the exact reviewed 22 cases/,
    )

    const wrongReviewedCaseId = structuredClone(reviewedInventory)
    const wrongCaseTitle = 'QG-AGENT-005 closes runner crash, timeout, cancel, retry, and bounded event recovery'
    Object.assign(wrongReviewedCaseId.cases[3]!, {
      caseId: 'QG-AGENT-005',
      title: wrongCaseTitle,
    })
    const agentSourcePath = join(root, reviewedGoldenSuites[0].file)
    const reviewedAgentSource = readFileSync(agentSourcePath, 'utf8')
    writeFileSync(agentSourcePath, `${reviewedAgentSource}test('${wrongCaseTitle}', async () => {})\n`)
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(wrongReviewedCaseId))
    expectFailure(
      () => validateQualificationInventory(root, manifest),
      /agent-golden-external must contain its exact reviewed Golden case ID set/,
    )
    writeFileSync(agentSourcePath, reviewedAgentSource)

    const wrongReviewedSuite = structuredClone(reviewedInventory)
    Object.assign(wrongReviewedSuite.cases[3]!, { suiteId: 'lsp-golden-external' })
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(wrongReviewedSuite))
    expectFailure(
      () => validateQualificationInventory(root, manifest),
      /QG-AGENT-004 names a file outside suite lsp-golden-external/,
    )

    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(reviewedInventory))
    const removedCriterionSource = structuredClone(reviewedInventory)
    removedCriterionSource.criterionSources = []
    removedCriterionSource.cases[0]!.contractCriterionIds = []
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(removedCriterionSource))
    expectFailure(
      () => validateQualificationInventory(root, manifest),
      /missing its manifest-bound criterion source|do not exactly match/,
    )
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(reviewedInventory))
    const inventoryOutsideSupport = structuredClone(manifest)
    inventoryOutsideSupport.qualificationSupportPaths = inventoryOutsideSupport.qualificationSupportPaths
      .filter((path) => path !== 'qualification/test-inventory.json')
    expectFailure(
      () => validateQualificationInventory(root, inventoryOutsideSupport),
      /must include qualification\/test-inventory.json/,
    )
    const initialDigest = qualificationPlanDigest(manifest, root)
    const unboundExecutable = structuredClone(manifest)
    unboundExecutable.qualificationSupportPaths = unboundExecutable.qualificationSupportPaths
      .filter((path) => path !== reviewedGoldenSuites[0].file)
    expectFailure(
      () => qualificationPlanDigest(unboundExecutable, root),
      /external-complete test path must be hash-bound support material/,
    )

    const postRunContractPath = 'qualification/artifact-hygiene.json'
    writeFileSync(join(root, postRunContractPath), canonicalJSON({
      schemaVersion: 'worksflow-post-run-verification-contract/v1',
      suiteId: 'artifact-hygiene',
    }))
    const withPostRun = structuredClone(manifest)
    withPostRun.qualificationSupportPaths.push(postRunContractPath)
    withPostRun.suites.push({
      id: 'artifact-hygiene',
      mode: 'external-qualification',
      executionKind: 'post-run-verifier',
      status: 'not-qualified',
      coverage: 'external-complete',
      requirementIds: ['FQP-E2E-016'],
      qualificationGroup: 'golden',
      qualificationCommand: 'qualification:golden',
      verificationContractPath: postRunContractPath,
      requiredArtifacts: ['credential-set-revocation-receipt'],
      blockers: ['external evidence is not configured'],
    } as unknown as typeof withPostRun.suites[number])
    validateQualificationInventory(root, withPostRun)
    const postRunDigest = qualificationPlanDigest(withPostRun, root)
    assert.notEqual(postRunDigest, initialDigest)
    writeFileSync(join(root, postRunContractPath), canonicalJSON({
      schemaVersion: 'worksflow-post-run-verification-contract/v1',
      suiteId: 'artifact-hygiene',
      changed: true,
    }))
    assert.notEqual(qualificationPlanDigest(withPostRun, root), postRunDigest)
    const postRunCase = structuredClone(reviewedInventory)
    Object.assign(postRunCase.cases[0]!, {
      suiteId: 'artifact-hygiene',
      contractCriterionIds: [],
    })
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(postRunCase))
    expectFailure(
      () => validateQualificationInventory(root, withPostRun),
      /non-Playwright qualification suite/,
    )
    writeFileSync(join(root, 'qualification/test-inventory.json'), canonicalJSON(reviewedInventory))

    const promoted = structuredClone(manifest)
    promoted.suites[0]!.status = 'qualified'
    promoted.suites[0]!.coverage = 'external-complete'
    promoted.suites[0]!.blockers = []
    Object.assign(promoted.suites[0]!, { receiptPath: 'qualification/evidence/receipt.json' })
    assert.equal(qualificationPlanDigest(promoted, root), initialDigest)
    promoted.suites[0]!.qualificationCommand = 'qualification:changed'
    assert.notEqual(qualificationPlanDigest(promoted, root), initialDigest)
    const plannedAsComplete = structuredClone(manifest)
    const plannedSuite = plannedAsComplete.suites[0] as unknown as Record<string, unknown>
    plannedSuite.plannedTestPaths = plannedSuite.testPaths
    delete plannedSuite.testPaths
    expectFailure(
      () => qualificationPlanDigest(plannedAsComplete, root),
      /external-complete coverage requires exact testPaths/,
    )
    const hiddenSuiteInput = structuredClone(manifest)
    const hiddenSuite = hiddenSuiteInput.suites[0] as unknown as Record<string, unknown>
    hiddenSuite.hiddenCommand = 'unhashed-side-effect'
    expectFailure(
      () => qualificationPlanDigest(hiddenSuiteInput, root),
      /hiddenCommand is not supported/,
    )

    const tokenFile = join(root, 'golden-token')
    writeFileSync(tokenFile, 'q'.repeat(48))
    chmodSync(tokenFile, 0o600)
    const preflightEnvironment = {
      ...process.env,
      WORKSFLOW_GOLDEN_STACK_URL: 'http://127.0.0.1:43119',
      WORKSFLOW_GOLDEN_ALLOW_HTTP: 'true',
      WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_ID: '11111111-1111-4111-8111-111111111111',
      WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_HASH: `sha256:${'a'.repeat(64)}`,
      WORKSFLOW_QUALIFICATION_RUN_ID: '22222222-2222-4222-8222-222222222222',
      WORKSFLOW_QUALIFICATION_ARTIFACT_DIR: join(root, 'new-artifacts'),
      WORKSFLOW_GOLDEN_E2E_TOKEN_FILE: tokenFile,
      WORKSFLOW_GOLDEN_TOKEN_EXPIRES_AT: new Date(Date.now() + 5 * 60_000).toISOString(),
    }
    const smoke = spawnSync('node', ['scripts/check-golden-environment.mjs', '--smoke'], {
      cwd: join(process.cwd()),
      encoding: 'utf8',
      env: preflightEnvironment,
    })
    assert.equal(smoke.status, 0, smoke.stderr)
    assert.match(smoke.stdout, /partial smoke inputs accepted/)

    const qualification = spawnSync('node', ['scripts/check-golden-environment.mjs', '--qualification'], {
      cwd: join(process.cwd()),
      encoding: 'utf8',
      env: preflightEnvironment,
    })
    assert.notEqual(qualification.status, 0)
    assert.match(qualification.stderr, /qualification rejects smoke-only inputs/)

    const report = {
      config: {
        forbidOnly: true,
        workers: 1,
        projects: [{ name: 'golden-chromium', retries: 0, repeatEach: 1 }],
      },
      errors: [],
      stats: { expected: 1, unexpected: 0, flaky: 0, skipped: 0 },
      suites: [{
        specs: [{
          title: 'QG-REFERENCE-001 closes the reference application',
          file: 'tests/golden-clean.spec.ts',
          ok: true,
          tests: [{
            projectName: 'golden-chromium', expectedStatus: 'passed', status: 'expected', annotations: [],
            results: [{ status: 'passed', retry: 0, errors: [], annotations: [] }],
          }],
        }],
      }],
    }
    const normalizationInventory = {
      cases: [{
        caseId: 'QG-REFERENCE-001',
        contractCriterionIds: ['AC-REFERENCE-001'],
        suiteId: 'golden',
        mode: 'qualification',
        file: 'frontend/tests/golden-clean.spec.ts',
        title: 'QG-REFERENCE-001 closes the reference application',
        requirementIds: ['AIC-E2E-003'],
      }],
    }
    const normalized = normalizePlaywrightQualification(report, normalizationInventory, {
      mode: 'qualification',
      runId: '33333333-3333-4333-8333-333333333333',
      testInventoryDigest: `sha256:${'b'.repeat(64)}`,
    })
    assert.deepEqual(normalized.totals, {
      discovered: 1, passed: 1, failed: 0, skipped: 0, flaky: 0, retried: 0, mocked: 0,
    })
    assert.deepEqual(normalized.tests[0]?.contractCriterionIds, ['AC-REFERENCE-001'])
    const skippedReport = structuredClone(report)
    skippedReport.stats.expected = 0
    skippedReport.stats.skipped = 1
    expectFailure(
      () => normalizePlaywrightQualification(skippedReport, normalizationInventory, {
        mode: 'qualification',
        runId: '33333333-3333-4333-8333-333333333333',
        testInventoryDigest: `sha256:${'b'.repeat(64)}`,
      }),
      /zero unexpected, flaky, or skipped tests/,
    )
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
}

main()
