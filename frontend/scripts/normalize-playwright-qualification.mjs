import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  canonicalJSON,
  compareCanonicalUTF8,
  hashFileSHA256,
  parseCanonicalJSON,
  qualificationFail,
  requireObject,
  requireString,
} from './qualification-core.mjs'

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

function collectSpecs(suite, result) {
  requireObject(suite, 'Playwright suite')
  if (!Array.isArray(suite.specs)) qualificationFail('Playwright suite specs must be an array')
  result.push(...suite.specs)
  if (suite.suites !== undefined) {
    if (!Array.isArray(suite.suites)) qualificationFail('Playwright nested suites must be an array')
    for (const nested of suite.suites) collectSpecs(nested, result)
  }
}

function normalizedInventoryFile(file) {
  const canonical = file.replaceAll('\\', '/')
  return canonical.startsWith('frontend/') ? canonical : `frontend/${canonical}`
}

export function normalizePlaywrightQualification(rawReport, inventory, options) {
  const { mode, runId, testInventoryDigest } = options
  if (mode !== 'smoke' && mode !== 'qualification') qualificationFail('Playwright normalization mode is invalid')
  requireString(runId, 'Playwright qualification runId', {
    pattern: /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
  })
  requireString(testInventoryDigest, 'Playwright test inventory digest', { pattern: /^sha256:[0-9a-f]{64}$/ })
  requireObject(rawReport, 'Playwright report')
  requireObject(rawReport.config, 'Playwright report config')
  if (rawReport.config.forbidOnly !== true || rawReport.config.workers !== 1 || !Array.isArray(rawReport.config.projects) || rawReport.config.projects.length !== 1) {
    qualificationFail('Playwright report must use forbidOnly, one worker, and one exact project')
  }
  const project = rawReport.config.projects[0]
  if (!project || project.retries !== 0 || project.repeatEach !== 1 || project.name !== 'golden-chromium') {
    qualificationFail('Playwright qualification project must use golden-chromium, zero retries, and repeatEach=1')
  }
  if (!Array.isArray(rawReport.errors) || rawReport.errors.length !== 0) qualificationFail('Playwright report contains top-level errors')
  requireObject(rawReport.stats, 'Playwright report stats')
  for (const field of ['expected', 'unexpected', 'flaky', 'skipped']) {
    if (!Number.isSafeInteger(rawReport.stats[field]) || rawReport.stats[field] < 0) qualificationFail(`Playwright stats.${field} is invalid`)
  }
  if (rawReport.stats.expected < 1 || rawReport.stats.unexpected !== 0 || rawReport.stats.flaky !== 0 || rawReport.stats.skipped !== 0) {
    qualificationFail('Playwright qualification must be non-empty with zero unexpected, flaky, or skipped tests')
  }
  if (!Array.isArray(rawReport.suites)) qualificationFail('Playwright report suites must be an array')

  const expectedMode = mode === 'qualification' ? 'qualification' : 'partial-smoke'
  const expectedCases = new Map(
    inventory.cases.filter((entry) => entry.mode === expectedMode).map((entry) => [entry.caseId, entry]),
  )
  if (expectedCases.size === 0) qualificationFail(`test inventory contains no ${expectedMode} cases`)

  const specs = []
  for (const suite of rawReport.suites) collectSpecs(suite, specs)
  const tests = []
  const seen = new Set()
  for (const spec of specs) {
    requireObject(spec, 'Playwright spec')
    const caseId = typeof spec.title === 'string' ? spec.title.split(' ', 1)[0] : ''
    const expected = expectedCases.get(caseId)
    if (!expected || spec.title !== expected.title || normalizedInventoryFile(spec.file) !== expected.file) {
      qualificationFail(`Playwright discovered an unreviewed or drifted test case ${caseId || '<missing>'}`)
    }
    if (seen.has(caseId)) qualificationFail(`Playwright test case ${caseId} was discovered more than once`)
    seen.add(caseId)
    if (spec.ok !== true || !Array.isArray(spec.tests) || spec.tests.length !== 1) {
      qualificationFail(`Playwright test case ${caseId} did not have one passing project result`)
    }
    const test = spec.tests[0]
    if (test.projectName !== 'golden-chromium' || test.expectedStatus !== 'passed' || test.status !== 'expected' || !Array.isArray(test.results) || test.results.length !== 1) {
      qualificationFail(`Playwright test case ${caseId} has a skipped, expected-failure, flaky, retry, or project drift outcome`)
    }
    const result = test.results[0]
    if (result.status !== 'passed' || result.retry !== 0 || !Array.isArray(result.errors) || result.errors.length !== 0 || result.error) {
      qualificationFail(`Playwright test case ${caseId} did not pass exactly once without errors`)
    }
    const annotations = [...(test.annotations ?? []), ...(result.annotations ?? [])]
    if (annotations.some((annotation) => ['skip', 'fixme', 'fail'].includes(annotation?.type))) {
      qualificationFail(`Playwright test case ${caseId} contains a forbidden outcome annotation`)
    }
    tests.push({
      caseId,
      suiteId: expected.suiteId,
      requirementIds: [...expected.requirementIds],
      contractCriterionIds: [...expected.contractCriterionIds],
      status: 'passed',
      retry: 0,
      flaky: false,
      mocked: false,
    })
  }
  tests.sort((left, right) => compareCanonicalUTF8(left.caseId, right.caseId))
  if (tests.length !== expectedCases.size || tests.length !== rawReport.stats.expected) {
    qualificationFail('Playwright discovery does not exactly match the reviewed test inventory')
  }
  for (const caseId of expectedCases.keys()) {
    if (!seen.has(caseId)) qualificationFail(`Playwright report is missing reviewed test case ${caseId}`)
  }
  return {
    schemaVersion: 'worksflow-playwright-qualification-result/v1',
    runId,
    testInventoryDigest,
    config: { forbidOnly: true, retries: 0, workers: 1 },
    tests,
    totals: {
      discovered: tests.length,
      passed: tests.length,
      failed: 0,
      skipped: 0,
      flaky: 0,
      retried: 0,
      mocked: 0,
    },
  }
}

function main() {
  const [modeArgument, rawPath, outputPath] = process.argv.slice(2)
  const mode = modeArgument === '--smoke' ? 'smoke' : modeArgument === '--qualification' ? 'qualification' : ''
  if (!mode || !rawPath || !outputPath) {
    throw new Error('usage: normalize-playwright-qualification.mjs (--smoke|--qualification) <raw-report.json> <normalized-result.json>')
  }
  const inventoryPath = resolve(repositoryRoot, 'qualification/test-inventory.json')
  const inventory = parseCanonicalJSON(readFileSync(inventoryPath), 'qualification test inventory')
  const rawReport = JSON.parse(readFileSync(resolve(rawPath), 'utf8'))
  const normalized = normalizePlaywrightQualification(rawReport, inventory, {
    mode,
    runId: process.env.WORKSFLOW_QUALIFICATION_RUN_ID?.trim() ?? '',
    testInventoryDigest: `sha256:${hashFileSHA256(inventoryPath)}`,
  })
  writeFileSync(resolve(outputPath), `${canonicalJSON(normalized)}\n`, { flag: 'wx', mode: 0o600 })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
