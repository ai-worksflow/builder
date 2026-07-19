import { existsSync, readFileSync, statSync } from 'node:fs'
import { dirname, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

import { qualificationPlanDigest } from './qualification-plan.mjs'
import { validateQualificationInventory } from './qualification-inventory.mjs'
import { validateGoldenSources } from './qualification-source-policy.mjs'

const scriptDirectory = dirname(fileURLToPath(import.meta.url))
const repositoryRoot = resolve(scriptDirectory, '../..')
const manifestPath = resolve(repositoryRoot, 'qualification/manifest.json')
const stableId = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
const requirementId = /\b(?:AIC-(?:E2E|FAIL)-\d{3}|FQP-E2E-\d{3}|LSP-QA-\d{3})\b/g
const allowedModes = new Set(['internal-regression', 'external-qualification', 'governance-qualification'])
const allowedStatuses = new Set(['implemented-internal', 'not-qualified', 'qualified'])
const allowedCoverage = new Set(['internal-complete', 'partial', 'planned', 'external-complete'])
const allowedExecutionKinds = new Set(['internal-test', 'playwright', 'post-run-verifier'])

function fail(message) {
  throw new Error(`qualification manifest: ${message}`)
}

function requireString(value, label) {
  if (typeof value !== 'string' || value.length === 0 || value.trim() !== value || /[\r\n\0]/.test(value)) {
    fail(`${label} must be a non-empty canonical string`)
  }
  return value
}

function requireStringArray(value, label) {
  if (!Array.isArray(value) || value.length === 0) {
    fail(`${label} must be a non-empty array`)
  }
  const seen = new Set()
  for (const [index, item] of value.entries()) {
    requireString(item, `${label}[${index}]`)
    if (seen.has(item)) fail(`${label} contains duplicate ${item}`)
    seen.add(item)
  }
  return value
}

function resolveRepositoryPath(value, label) {
  const relative = requireString(value, label)
  if (relative.startsWith('/') || relative.includes('\\') || relative.split('/').includes('..')) {
    fail(`${label} must be a normalized repository-relative path`)
  }
  const absolute = resolve(repositoryRoot, relative)
  if (absolute !== repositoryRoot && !absolute.startsWith(`${repositoryRoot}${sep}`)) {
    fail(`${label} escapes the repository`)
  }
  return absolute
}

if (!existsSync(manifestPath)) fail('qualification/manifest.json is missing')

let manifest
try {
  manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
} catch (error) {
  fail(`manifest is not valid JSON: ${error instanceof Error ? error.message : String(error)}`)
}

if (manifest.schemaVersion !== 'worksflow-qualification-manifest/v1') {
  fail('schemaVersion must be worksflow-qualification-manifest/v1')
}
requireString(manifest.subject, 'subject')
const sourceDocuments = requireStringArray(manifest.sourceDocuments, 'sourceDocuments')
const qualificationSupportPaths = requireStringArray(manifest.qualificationSupportPaths, 'qualificationSupportPaths')
if (!manifest.policy || typeof manifest.policy !== 'object' || Array.isArray(manifest.policy)) {
  fail('policy must be an object')
}
for (const key of [
  'stageExitRequiresExternalQualification',
  'allowSkippedTests',
  'allowMocks',
  'allowMutableRuntimeImages',
  'passingInternalSuitesAreStageExitEvidence',
]) {
  if (typeof manifest.policy[key] !== 'boolean') fail(`policy.${key} must be boolean`)
}
if (!manifest.policy.stageExitRequiresExternalQualification || manifest.policy.allowSkippedTests ||
    manifest.policy.allowMocks || manifest.policy.allowMutableRuntimeImages ||
    manifest.policy.passingInternalSuitesAreStageExitEvidence) {
  fail('stage-exit policy must require external, immutable, zero-mock and zero-skip evidence')
}
if (manifest.policy.credentialBearingArtifacts !== 'restricted-encrypted-until-revocation') {
  fail('credentialBearingArtifacts policy is not fail closed')
}

const documentedIds = new Set()
for (const [index, source] of sourceDocuments.entries()) {
  const absolute = resolveRepositoryPath(source, `sourceDocuments[${index}]`)
  if (!existsSync(absolute) || !statSync(absolute).isFile()) fail(`${source} is not a file`)
  const matches = readFileSync(absolute, 'utf8').match(requirementId) ?? []
  for (const id of matches) documentedIds.add(id)
}

for (const [index, supportPath] of qualificationSupportPaths.entries()) {
  const absolute = resolveRepositoryPath(supportPath, `qualificationSupportPaths[${index}]`)
  if (!existsSync(absolute) || !statSync(absolute).isFile()) fail(`${supportPath} is not a support file`)
}
if (documentedIds.size === 0) fail('source documents contain no acceptance IDs')

if (!Array.isArray(manifest.suites) || manifest.suites.length === 0) fail('suites must be a non-empty array')
const suiteIds = new Set()
const mappedIds = new Map()
const counts = new Map()

for (const [suiteIndex, suite] of manifest.suites.entries()) {
  const label = `suites[${suiteIndex}]`
  if (!suite || typeof suite !== 'object' || Array.isArray(suite)) fail(`${label} must be an object`)
  const suiteId = requireString(suite.id, `${label}.id`)
  if (!stableId.test(suiteId)) fail(`${label}.id is not a stable kebab-case ID`)
  if (suiteIds.has(suiteId)) fail(`duplicate suite id ${suiteId}`)
  suiteIds.add(suiteId)
  if (!allowedModes.has(suite.mode)) fail(`${suiteId} has unsupported mode ${suite.mode}`)
  if (!allowedExecutionKinds.has(suite.executionKind)) {
    fail(`${suiteId} has unsupported executionKind ${suite.executionKind}`)
  }
  if (!allowedStatuses.has(suite.status)) fail(`${suiteId} has unsupported status ${suite.status}`)
  if (!allowedCoverage.has(suite.coverage)) fail(`${suiteId} has unsupported coverage ${suite.coverage}`)

  const ids = requireStringArray(suite.requirementIds, `${suiteId}.requirementIds`)
  for (const id of ids) {
    if (!new RegExp(`^(?:AIC-(?:E2E|FAIL)-\\d{3}|FQP-E2E-\\d{3}|LSP-QA-\\d{3})$`).test(id)) {
      fail(`${suiteId} contains malformed requirement ID ${id}`)
    }
    const previous = mappedIds.get(id)
    if (previous) fail(`${id} is mapped by both ${previous} and ${suiteId}`)
    mappedIds.set(id, suiteId)
  }

  requireStringArray(suite.requiredArtifacts, `${suiteId}.requiredArtifacts`)
  requireStringArray(suite.limitations ?? suite.blockers, `${suiteId}.${suite.limitations ? 'limitations' : 'blockers'}`)

  if (suite.mode === 'internal-regression') {
    if (suite.executionKind !== 'internal-test') fail(`${suiteId} internal suite must use internal-test execution`)
    if (suite.status !== 'implemented-internal' || suite.coverage !== 'internal-complete') {
      fail(`${suiteId} must remain implemented-internal/internal-complete`)
    }
    requireStringArray(suite.commands, `${suiteId}.commands`)
    const testPaths = requireStringArray(suite.testPaths, `${suiteId}.testPaths`)
    for (const [pathIndex, path] of testPaths.entries()) {
      const absolute = resolveRepositoryPath(path, `${suiteId}.testPaths[${pathIndex}]`)
      if (!existsSync(absolute)) fail(`${suiteId} internal test path does not exist: ${path}`)
    }
  } else {
    if (suite.executionKind === 'internal-test') fail(`${suiteId} qualification suite cannot use internal-test execution`)
    if (suite.mode === 'governance-qualification' && suite.executionKind !== 'post-run-verifier') {
      fail(`${suiteId} governance suite must use post-run-verifier execution`)
    }
    requireString(suite.qualificationGroup, `${suiteId}.qualificationGroup`)
    if (suite.mode === 'external-qualification') {
      requireString(suite.qualificationCommand, `${suiteId}.qualificationCommand`)
    }
    if (suite.executionKind === 'playwright') {
      const planned = suite.testPaths ?? suite.plannedTestPaths ?? (suite.smokeTestPath ? [suite.smokeTestPath] : undefined)
      const pathLabel = suite.testPaths ? 'testPaths' : (suite.plannedTestPaths ? 'plannedTestPaths' : 'smokeTestPath')
      requireStringArray(planned, `${suiteId}.${pathLabel}`)
      for (const [pathIndex, path] of planned.entries()) {
        const absolute = resolveRepositoryPath(path, `${suiteId}.${pathLabel}[${pathIndex}]`)
        if ((suite.coverage === 'external-complete' || suite.status === 'qualified') &&
            (!existsSync(absolute) || !statSync(absolute).isFile())) {
          fail(`${suiteId} executable qualification path does not exist: ${path}`)
        }
      }
      if (suite.coverage === 'external-complete' && !suite.testPaths) {
        fail(`${suiteId} must replace planned/smoke paths with exact testPaths before external-complete`)
      }
      if (suite.coverage === 'external-complete') {
        const goldenSources = planned.filter((path) => /^frontend\/tests\/golden-[a-z0-9-]+\.spec\.ts$/.test(path))
        validateGoldenSources(repositoryRoot, goldenSources)
      }
    } else {
      const contractPath = requireString(suite.verificationContractPath, `${suiteId}.verificationContractPath`)
      const absolute = resolveRepositoryPath(contractPath, `${suiteId}.verificationContractPath`)
      if (!qualificationSupportPaths.includes(contractPath) || !existsSync(absolute) || !statSync(absolute).isFile()) {
        fail(`${suiteId} post-run verification contract must be existing qualification support material`)
      }
      if (suite.testPaths || suite.plannedTestPaths || suite.smokeTestPath) {
        fail(`${suiteId} post-run verifier cannot declare a Playwright path`)
      }
    }
    if (suite.status === 'qualified' && suite.coverage !== 'external-complete') {
      fail(`${suiteId} cannot be qualified without external-complete coverage`)
    }
    if (suite.status === 'qualified') {
      fail(`${suiteId} cannot be promoted until the cryptographic qualification receipt verifier accepts its exact plan and artifact closure`)
    }
  }
  counts.set(suite.status, (counts.get(suite.status) ?? 0) + ids.length)
}

const missing = [...documentedIds].filter((id) => !mappedIds.has(id)).sort()
const extra = [...mappedIds.keys()].filter((id) => !documentedIds.has(id)).sort()
if (missing.length > 0) fail(`unmapped documented IDs: ${missing.join(', ')}`)
if (extra.length > 0) fail(`manifest IDs absent from source documents: ${extra.join(', ')}`)

const qualifiedExternal = manifest.suites.filter((suite) =>
  suite.mode === 'external-qualification' && suite.status === 'qualified')
const incompleteExternal = manifest.suites.filter((suite) =>
  suite.mode === 'external-qualification' && suite.status !== 'qualified')

validateQualificationInventory(repositoryRoot, manifest)

console.log(`qualification manifest: ${mappedIds.size} IDs mapped exactly once across ${manifest.suites.length} suites`)
console.log(`qualification manifest: ${counts.get('implemented-internal') ?? 0} implemented-internal, ${counts.get('not-qualified') ?? 0} not-qualified`)
console.log(`qualification manifest: ${qualifiedExternal.length} external suites qualified, ${incompleteExternal.length} external suites pending`)
console.log(`qualification manifest: stable plan digest ${qualificationPlanDigest(manifest, repositoryRoot)}`)
