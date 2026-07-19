import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  parseCanonicalJSON,
  parseStrictJSON,
  qualificationFail,
  requireExactKeys,
  requireRelativePath,
  requireSortedUniqueStrings,
  requireString,
  resolveRegularFile,
} from './qualification-core.mjs'

const caseIdPattern = /^QG-[A-Z][A-Z0-9]*-[0-9]{3}$/
const requirementIdPattern = /^(?:AIC-(?:E2E|FAIL)-[0-9]{3}|FQP-E2E-[0-9]{3}|LSP-QA-[0-9]{3})$/
const contractCriterionIdPattern = /^AC-[A-Z][A-Z0-9]*-[0-9]{3}$/

const exactGoldenCaseIdsBySuite = new Map([
  ['agent-golden-external', ['QG-AGENT-001', 'QG-AGENT-002', 'QG-AGENT-003', 'QG-AGENT-004']],
  ['lsp-golden-external', ['QG-LSP-001', 'QG-LSP-002', 'QG-LSP-003']],
  ['reference-ai-golden-external', [
    'QG-REFERENCE-001', 'QG-REFERENCE-002', 'QG-REFERENCE-003',
    'QG-REFERENCE-004', 'QG-REFERENCE-005', 'QG-REFERENCE-006',
  ]],
  ['release-golden-external', [
    'QG-RELEASE-001', 'QG-RELEASE-002', 'QG-RELEASE-003',
    'QG-RELEASE-004', 'QG-RELEASE-005',
  ]],
  ['sandbox-golden-external', [
    'QG-SANDBOX-001', 'QG-SANDBOX-002', 'QG-SANDBOX-003', 'QG-SANDBOX-004',
  ]],
])
const exactGoldenCaseCount = [...exactGoldenCaseIdsBySuite.values()]
  .reduce((count, caseIds) => count + caseIds.length, 0)

function readCriterionSource(repositoryRoot, source, supportPaths, label) {
  requireExactKeys(source, ['suiteId', 'path', 'schemaVersion', 'applicationId'], [], label)
  requireString(source.suiteId, `${label}.suiteId`, { pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/ })
  requireRelativePath(source.path, `${label}.path`)
  requireString(source.schemaVersion, `${label}.schemaVersion`, { exact: 'reference-acceptance-criteria/v1' })
  requireString(source.applicationId, `${label}.applicationId`, { pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/ })
  if (!supportPaths.has(source.path)) qualificationFail(`${label}.path must be qualification-plan support material`)
  const file = resolveRegularFile(repositoryRoot, source.path, `${label}.path`, { maximumBytes: 1 << 20 })
  const document = parseStrictJSON(readFileSync(file.absolute), `${label}.path`, 1 << 20)
  requireExactKeys(document, ['schemaVersion', 'applicationId', 'criteria'], [], `${label} document`)
  requireString(document.schemaVersion, `${label} document.schemaVersion`, { exact: source.schemaVersion })
  requireString(document.applicationId, `${label} document.applicationId`, { exact: source.applicationId })
  if (!Array.isArray(document.criteria) || document.criteria.length === 0 || document.criteria.length > 512) {
    qualificationFail(`${label} document.criteria must contain 1..512 criteria`)
  }
  const ids = new Set()
  let prior = ''
  for (const [index, criterion] of document.criteria.entries()) {
    const criterionLabel = `${label} document.criteria[${index}]`
    requireExactKeys(criterion, ['id', 'requirementIds', 'statement'], [], criterionLabel)
    requireString(criterion.id, `${criterionLabel}.id`, { pattern: contractCriterionIdPattern })
    if (criterion.id <= prior) qualificationFail(`${label} criterion IDs must be strictly sorted and unique`)
    prior = criterion.id
    requireSortedUniqueStrings(criterion.requirementIds, `${criterionLabel}.requirementIds`, {
      pattern: /^REQ-[A-Z0-9]+(?:-[A-Z0-9]+)*$/,
    })
    requireString(criterion.statement, `${criterionLabel}.statement`, { maximumBytes: 4096 })
    ids.add(criterion.id)
  }
  return ids
}

export function validateQualificationInventory(repositoryRoot, manifest) {
  const inventoryPath = resolve(repositoryRoot, 'qualification/test-inventory.json')
  const inventory = parseCanonicalJSON(readFileSync(inventoryPath), 'qualification test inventory')
  requireExactKeys(inventory, ['schemaVersion', 'criterionSources', 'cases'], [], 'qualification test inventory')
  requireString(inventory.schemaVersion, 'qualification test inventory schemaVersion', {
    exact: 'worksflow-qualification-test-inventory/v2',
  })
  if (!Array.isArray(inventory.criterionSources) || inventory.criterionSources.length > 32) {
    qualificationFail('qualification test inventory criterionSources must contain 0..32 values')
  }
  if (!Array.isArray(inventory.cases) || inventory.cases.length === 0 || inventory.cases.length > 512) {
    qualificationFail('qualification test inventory must contain 1..512 cases')
  }
  if (inventory.cases.length !== exactGoldenCaseCount) {
    qualificationFail(`qualification test inventory must contain the exact reviewed ${exactGoldenCaseCount} cases`)
  }

  const suiteById = new Map(manifest.suites.map((suite) => [suite.id, suite]))
  const supportPaths = new Set(manifest.qualificationSupportPaths)
  if (!supportPaths.has('qualification/test-inventory.json')) {
    qualificationFail('qualificationSupportPaths must include qualification/test-inventory.json')
  }
  const requiredCriterionSources = new Map(
    manifest.suites
      .filter((suite) => suite.criterionSource)
      .map((suite) => [suite.id, { suiteId: suite.id, ...suite.criterionSource }]),
  )
  const criteriaBySuite = new Map()
  let priorSourceSuite = ''
  for (const [index, source] of inventory.criterionSources.entries()) {
    const label = `qualification test inventory criterionSources[${index}]`
    const ids = readCriterionSource(repositoryRoot, source, supportPaths, label)
    const suite = suiteById.get(source.suiteId)
    if (!suite || suite.executionKind !== 'playwright') {
      qualificationFail(`${label} references a non-Playwright qualification suite`)
    }
    const requiredSource = requiredCriterionSources.get(source.suiteId)
    if (!requiredSource || source.path !== requiredSource.path || source.schemaVersion !== requiredSource.schemaVersion ||
        source.applicationId !== requiredSource.applicationId) {
      qualificationFail(`${label} does not exactly match a manifest-bound criterion source`)
    }
    if (source.suiteId <= priorSourceSuite || criteriaBySuite.has(source.suiteId)) {
      qualificationFail('qualification criterionSources must be strictly sorted and unique by suiteId')
    }
    priorSourceSuite = source.suiteId
    criteriaBySuite.set(source.suiteId, ids)
  }
  for (const suiteId of requiredCriterionSources.keys()) {
    if (!criteriaBySuite.has(suiteId)) qualificationFail(`${suiteId} is missing its manifest-bound criterion source`)
  }
  const casesBySuite = new Map()
  let priorCaseId = ''
  for (const [index, entry] of inventory.cases.entries()) {
    const label = `qualification test inventory cases[${index}]`
    requireExactKeys(entry, ['caseId', 'suiteId', 'mode', 'file', 'title', 'requirementIds', 'contractCriterionIds'], [], label)
    requireString(entry.caseId, `${label}.caseId`, { pattern: caseIdPattern })
    if (entry.caseId <= priorCaseId) qualificationFail('qualification test inventory cases must be strictly sorted by caseId')
    priorCaseId = entry.caseId
    requireString(entry.suiteId, `${label}.suiteId`, { pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/ })
    const suite = suiteById.get(entry.suiteId)
    if (!suite || suite.executionKind !== 'playwright') {
      qualificationFail(`${entry.caseId} references a non-Playwright qualification suite`)
    }
    requireString(entry.mode, `${label}.mode`, { pattern: /^(?:partial-smoke|qualification)$/ })
    requireRelativePath(entry.file, `${label}.file`, 'frontend/tests')
    if (!/^frontend\/tests\/golden-[a-z0-9-]+\.spec\.ts$/.test(entry.file)) {
      qualificationFail(`${entry.caseId} must reference a Golden spec`)
    }
    const declaredPaths = suite.testPaths ?? suite.plannedTestPaths ?? (suite.smokeTestPath ? [suite.smokeTestPath] : [])
    if (!declaredPaths.includes(entry.file)) {
      qualificationFail(`${entry.caseId} names a file outside suite ${entry.suiteId}`)
    }
    const sourceFile = resolveRegularFile(repositoryRoot, entry.file, `${label}.file`, { maximumBytes: 1 << 20 })
    requireString(entry.title, `${label}.title`, { maximumBytes: 512 })
    if (!entry.title.startsWith(`${entry.caseId} `)) qualificationFail(`${entry.caseId} title must begin with its stable case ID`)
    if (!readFileSync(sourceFile.absolute, 'utf8').includes(entry.title)) {
      qualificationFail(`${entry.caseId} exact title is absent from ${entry.file}`)
    }
    requireSortedUniqueStrings(entry.requirementIds, `${label}.requirementIds`, { pattern: requirementIdPattern })
    const suiteRequirements = new Set(suite.requirementIds)
    for (const requirementId of entry.requirementIds) {
      if (!suiteRequirements.has(requirementId)) qualificationFail(`${entry.caseId} maps foreign requirement ${requirementId}`)
    }
    requireSortedUniqueStrings(entry.contractCriterionIds, `${label}.contractCriterionIds`, {
      minimum: 0,
      pattern: contractCriterionIdPattern,
    })
    const sourceCriteria = criteriaBySuite.get(entry.suiteId)
    if (!sourceCriteria && entry.contractCriterionIds.length !== 0) {
      qualificationFail(`${entry.caseId} maps contract criteria without a suite criterion source`)
    }
    for (const criterionId of entry.contractCriterionIds) {
      if (!sourceCriteria?.has(criterionId)) qualificationFail(`${entry.caseId} maps foreign contract criterion ${criterionId}`)
    }
    const suiteCases = casesBySuite.get(entry.suiteId) ?? []
    suiteCases.push(entry)
    casesBySuite.set(entry.suiteId, suiteCases)
  }

  for (const suite of manifest.suites) {
    if (suite.coverage !== 'external-complete' || suite.executionKind !== 'playwright') continue
    const cases = (casesBySuite.get(suite.id) ?? []).filter((entry) => entry.mode === 'qualification')
    if (cases.length === 0) qualificationFail(`${suite.id} has no executable qualification inventory cases`)
    const files = new Set(cases.map((entry) => entry.file))
    const declaredFiles = new Set(suite.testPaths)
    for (const file of files) {
      if (!declaredFiles.has(file)) qualificationFail(`${suite.id} inventory contains undeclared qualification path: ${file}`)
    }
    for (const path of suite.testPaths) {
      if (!files.has(path)) qualificationFail(`${suite.id} test path has no qualification case: ${path}`)
    }
    if (files.size !== declaredFiles.size) {
      qualificationFail(`${suite.id} inventory and exact testPaths do not form a closed set`)
    }
    const covered = new Set(cases.flatMap((entry) => entry.requirementIds))
    for (const requirementId of suite.requirementIds) {
      if (!covered.has(requirementId)) qualificationFail(`${suite.id} inventory does not cover ${requirementId}`)
    }
    const sourceCriteria = criteriaBySuite.get(suite.id)
    if (sourceCriteria) {
      const coveredCriteria = new Set(cases.flatMap((entry) => entry.contractCriterionIds))
      for (const criterionId of sourceCriteria) {
        if (!coveredCriteria.has(criterionId)) qualificationFail(`${suite.id} inventory does not cover contract criterion ${criterionId}`)
      }
    }
  }
  for (const [suiteId, expectedCaseIds] of exactGoldenCaseIdsBySuite) {
    const actualCaseIds = (casesBySuite.get(suiteId) ?? []).map((entry) => entry.caseId)
    if (JSON.stringify(actualCaseIds) !== JSON.stringify(expectedCaseIds)) {
      qualificationFail(`${suiteId} must contain its exact reviewed Golden case ID set`)
    }
  }
  return inventory
}
