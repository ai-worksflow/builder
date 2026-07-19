import { readFileSync } from 'node:fs'

import {
  canonicalJSON,
  compareCanonicalUTF8,
  hashBytesSHA256,
  hashFileSHA256,
  requireBoolean,
  requireExactKeys,
  requireObject,
  requireRelativePath,
  requireString,
  resolveRegularFile,
} from './qualification-core.mjs'

function stringArray(value, label) {
  if (!Array.isArray(value) || value.length === 0) throw new Error(`qualification plan: ${label} is required`)
  const seen = new Set()
  return value.map((entry, index) => {
    const result = requireString(entry, `${label}[${index}]`)
    if (seen.has(result)) throw new Error(`qualification plan: ${label} contains duplicate ${result}`)
    seen.add(result)
    return result
  })
}

function suiteExecution(suite, label, supportPaths) {
  const executionKind = requireString(suite.executionKind, `${label}.executionKind`)
  if (!['internal-test', 'playwright', 'post-run-verifier'].includes(executionKind)) {
    throw new Error(`qualification plan: ${label}.executionKind is unsupported`)
  }
  const declared = [
    Object.hasOwn(suite, 'testPaths') ? 'testPaths' : '',
    Object.hasOwn(suite, 'plannedTestPaths') ? 'plannedTestPaths' : '',
    Object.hasOwn(suite, 'smokeTestPath') ? 'smokeTestPath' : '',
  ].filter(Boolean)
  const hasVerificationContract = Object.hasOwn(suite, 'verificationContractPath')
  if (executionKind === 'post-run-verifier') {
    if (declared.length !== 0 || !hasVerificationContract) {
      throw new Error(`qualification plan: ${label} post-run verifier requires only verificationContractPath`)
    }
    const verificationContractPath = requireRelativePath(
      suite.verificationContractPath,
      `${label}.verificationContractPath`,
    )
    if (!supportPaths.has(verificationContractPath)) {
      throw new Error(`qualification plan: ${label}.verificationContractPath must be qualification-plan support material`)
    }
    return { executionKind, verificationContractPath }
  }
  if (hasVerificationContract) {
    throw new Error(`qualification plan: ${label} ${executionKind} cannot declare verificationContractPath`)
  }
  if (declared.length !== 1) {
    throw new Error(`qualification plan: ${label} must declare exactly one executable path field`)
  }
  if (suite.coverage === 'external-complete' && declared[0] !== 'testPaths') {
    throw new Error(`qualification plan: ${label} external-complete coverage requires exact testPaths`)
  }
  const paths = declared[0] === 'smokeTestPath'
    ? [requireString(suite.smokeTestPath, `${label}.smokeTestPath`)]
    : stringArray(suite[declared[0]], `${label}.${declared[0]}`)
  if (suite.coverage === 'external-complete') {
    for (const path of paths) {
      if (!supportPaths.has(path)) {
        throw new Error(`qualification plan: ${label} external-complete test path must be hash-bound support material`)
      }
    }
  }
  return {
    executionKind,
    testPaths: paths.map((path, index) => requireRelativePath(path, `${label}.${declared[0]}[${index}]`)),
  }
}

function suiteCriterionSource(suite, label) {
  if (!Object.hasOwn(suite, 'criterionSource')) return undefined
  requireObject(suite.criterionSource, `${label}.criterionSource`)
  requireExactKeys(
    suite.criterionSource,
    ['path', 'schemaVersion', 'applicationId'],
    [],
    `${label}.criterionSource`,
  )
  return {
    path: requireRelativePath(suite.criterionSource.path, `${label}.criterionSource.path`),
    schemaVersion: requireString(suite.criterionSource.schemaVersion, `${label}.criterionSource.schemaVersion`, {
      exact: 'reference-acceptance-criteria/v1',
    }),
    applicationId: requireString(suite.criterionSource.applicationId, `${label}.criterionSource.applicationId`, {
      pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/,
    }),
  }
}

export function qualificationPlan(manifest, repositoryRoot) {
  requireObject(manifest, 'qualification manifest')
  requireString(manifest.schemaVersion, 'qualification manifest schemaVersion')
  requireString(manifest.subject, 'qualification manifest subject')
  requireObject(manifest.policy, 'qualification manifest policy')
  requireExactKeys(
    manifest,
    ['schemaVersion', 'subject', 'sourceDocuments', 'qualificationSupportPaths', 'policy', 'suites'],
    ['trust', 'trustPolicy', 'trustPolicyDigest'],
    'qualification manifest',
  )
  requireExactKeys(
    manifest.policy,
    [
      'stageExitRequiresExternalQualification', 'allowSkippedTests', 'allowMocks',
      'allowMutableRuntimeImages', 'credentialBearingArtifacts',
      'passingInternalSuitesAreStageExitEvidence',
    ],
    [],
    'qualification manifest policy',
  )
  requireBoolean(manifest.policy.stageExitRequiresExternalQualification, 'policy.stageExitRequiresExternalQualification', true)
  requireBoolean(manifest.policy.allowSkippedTests, 'policy.allowSkippedTests', false)
  requireBoolean(manifest.policy.allowMocks, 'policy.allowMocks', false)
  requireBoolean(manifest.policy.allowMutableRuntimeImages, 'policy.allowMutableRuntimeImages', false)
  requireBoolean(manifest.policy.passingInternalSuitesAreStageExitEvidence, 'policy.passingInternalSuitesAreStageExitEvidence', false)
  requireString(manifest.policy.credentialBearingArtifacts, 'policy.credentialBearingArtifacts', {
    exact: 'restricted-encrypted-until-revocation',
  })
  if (!Array.isArray(manifest.sourceDocuments) || !Array.isArray(manifest.suites)) {
    throw new Error('qualification plan: sourceDocuments and suites are required')
  }

  const sourceDocuments = manifest.sourceDocuments.map((path, index) => {
    requireRelativePath(path, `sourceDocuments[${index}]`)
    const file = resolveRegularFile(repositoryRoot, path, `sourceDocuments[${index}]`, { maximumBytes: 16 << 20 })
    return { path, sha256: `sha256:${hashFileSHA256(file.absolute)}` }
  })
  sourceDocuments.sort((left, right) => compareCanonicalUTF8(left.path, right.path))

  if (!Array.isArray(manifest.qualificationSupportPaths) || manifest.qualificationSupportPaths.length === 0) {
    throw new Error('qualification plan: qualificationSupportPaths are required')
  }
  const supportFiles = manifest.qualificationSupportPaths.map((path, index) => {
    requireRelativePath(path, `qualificationSupportPaths[${index}]`)
    const file = resolveRegularFile(repositoryRoot, path, `qualificationSupportPaths[${index}]`, { maximumBytes: 16 << 20 })
    return { path, sha256: `sha256:${hashFileSHA256(file.absolute)}` }
  })
  supportFiles.sort((left, right) => compareCanonicalUTF8(left.path, right.path))
  const supportPaths = new Set(manifest.qualificationSupportPaths)

  const suites = manifest.suites.map((suite, index) => {
    requireObject(suite, `suites[${index}]`)
    requireExactKeys(
      suite,
      ['id', 'mode', 'executionKind', 'status', 'coverage', 'requirementIds', 'requiredArtifacts'],
      [
        'commands', 'qualificationCommand', 'testPaths', 'plannedTestPaths', 'smokeTestPath',
        'verificationContractPath', 'qualificationGroup', 'criterionSource', 'blockers', 'limitations',
        'receiptPath', 'trustPolicyDigest',
      ],
      `suites[${index}]`,
    )
    const id = requireString(suite.id, `suites[${index}].id`)
    const mode = requireString(suite.mode, `suites[${index}].mode`)
    const status = requireString(suite.status, `suites[${index}].status`)
    const coverage = requireString(suite.coverage, `suites[${index}].coverage`)
    if (!['internal-regression', 'external-qualification', 'governance-qualification'].includes(mode)) {
      throw new Error(`qualification plan: suites[${index}] mode is unsupported`)
    }
    if (!['implemented-internal', 'not-qualified', 'qualified'].includes(status)) {
      throw new Error(`qualification plan: suites[${index}] status is unsupported`)
    }
    if (!['internal-complete', 'partial', 'planned', 'external-complete'].includes(coverage)) {
      throw new Error(`qualification plan: suites[${index}] coverage is unsupported`)
    }
    const requirementIds = stringArray(suite.requirementIds, `suites[${index}].requirementIds`)
    const requiredArtifacts = stringArray(suite.requiredArtifacts, `suites[${index}].requiredArtifacts`)
    const execution = suiteExecution(suite, `suites[${index}]`, supportPaths)
    const criterionSource = suiteCriterionSource(suite, `suites[${index}]`)
    if (mode === 'internal-regression' && (
      execution.executionKind !== 'internal-test' || !Array.isArray(suite.commands) || !Object.hasOwn(suite, 'testPaths')
    )) {
      throw new Error(`qualification plan: suites[${index}] internal suite lacks commands or exact testPaths`)
    }
    if (mode !== 'internal-regression' && execution.executionKind === 'internal-test') {
      throw new Error(`qualification plan: suites[${index}] qualification suite cannot use internal-test execution`)
    }
    if (mode === 'governance-qualification' && execution.executionKind === 'playwright') {
      throw new Error(`qualification plan: suites[${index}] governance suite cannot use Playwright execution`)
    }
    if (mode === 'internal-regression' && criterionSource) {
      throw new Error(`qualification plan: suites[${index}] internal suites cannot declare a qualification criterion source`)
    }
    if (mode === 'external-qualification') {
      requireString(suite.qualificationCommand, `suites[${index}].qualificationCommand`)
    }
    if (mode !== 'internal-regression') {
      requireString(suite.qualificationGroup, `suites[${index}].qualificationGroup`)
    }
    const projected = {
      id,
      mode,
      executionKind: execution.executionKind,
      ...(typeof suite.qualificationGroup === 'string' ? { qualificationGroup: suite.qualificationGroup } : {}),
      requirementIds,
      requiredArtifacts,
      ...(execution.testPaths ? { testPaths: execution.testPaths } : {}),
      ...(execution.verificationContractPath ? { verificationContractPath: execution.verificationContractPath } : {}),
      ...(criterionSource ? { criterionSource } : {}),
    }
    if (Array.isArray(suite.commands)) projected.commands = stringArray(suite.commands, `suites[${index}].commands`)
    if (typeof suite.qualificationCommand === 'string') projected.qualificationCommand = suite.qualificationCommand
    return projected
  })
  suites.sort((left, right) => compareCanonicalUTF8(left.id, right.id))

  return {
    schemaVersion: 'worksflow-qualification-plan/v1',
    manifestSchemaVersion: manifest.schemaVersion,
    subject: manifest.subject,
    policy: manifest.policy,
    sourceDocuments,
    supportFiles,
    suites,
  }
}

export function qualificationPlanDigest(manifest, repositoryRoot) {
  const plan = qualificationPlan(manifest, repositoryRoot)
  return `sha256:${hashBytesSHA256(Buffer.from(canonicalJSON(plan)))}`
}

export function qualificationPlanDigestFromFile(manifestPath, repositoryRoot) {
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
  return qualificationPlanDigest(manifest, repositoryRoot)
}
