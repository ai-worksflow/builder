import { existsSync, lstatSync, readFileSync, realpathSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  goldenExecutionTestPaths,
  loadGoldenCredentialFiles,
  loadGoldenDocuments,
  verifyGoldenSourceRepository,
} from './golden-authority.mjs'
import { parseCanonicalJSON } from './qualification-core.mjs'
import { qualificationPlanDigest } from './qualification-plan.mjs'

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

const mode = process.argv[2]
if (mode !== '--smoke' && mode !== '--qualification') {
  throw new Error('golden preflight: expected --smoke or --qualification')
}

function required(name) {
  const value = process.env[name]?.trim() ?? ''
  if (!value || /[\r\n\0]/.test(value)) throw new Error(`golden preflight: ${name} is required`)
  return value
}

function requireUUID(value, name) {
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value)) {
    throw new Error(`golden preflight: ${name} must be a canonical lowercase UUIDv4`)
  }
}

function requireDigest(value, name) {
  if (!/^sha256:[0-9a-f]{64}$/.test(value)) {
    throw new Error(`golden preflight: ${name} must be sha256:<64 lowercase hex>`)
  }
}

function requireFreshArtifactDirectory() {
  const artifactDirectory = required('WORKSFLOW_QUALIFICATION_ARTIFACT_DIR')
  if (!isAbsolute(artifactDirectory) || resolve(artifactDirectory) !== artifactDirectory) {
    throw new Error('golden preflight: WORKSFLOW_QUALIFICATION_ARTIFACT_DIR must be an absolute normalized path')
  }
  if (existsSync(artifactDirectory)) {
    throw new Error('golden preflight: artifact directory already exists; evidence runs are append-only and must use a fresh path')
  }
}

function validateSmokeCredential(path) {
  if (!isAbsolute(path) || resolve(path) !== path || !existsSync(path)) {
    throw new Error('golden preflight: WORKSFLOW_GOLDEN_E2E_TOKEN_FILE must be an existing absolute normalized file')
  }
  const fileStat = lstatSync(path)
  if (fileStat.isSymbolicLink() || !fileStat.isFile() || fileStat.nlink !== 1 || realpathSync(path) !== path) {
    throw new Error('golden preflight: WORKSFLOW_GOLDEN_E2E_TOKEN_FILE must be a single-link regular non-symlink file')
  }
  if ((fileStat.mode & 0o777) !== 0o600) {
    throw new Error('golden preflight: smoke token file must have exact mode 0600')
  }
  const currentUID = typeof process.getuid === 'function' ? process.getuid() : fileStat.uid
  if (fileStat.uid !== 0 && fileStat.uid !== currentUID) {
    throw new Error('golden preflight: smoke token file must be owned by root or the current user')
  }
  const token = readFileSync(path, 'utf8')
  if (token.length < 32 || token.length > 8192 || token.trim() !== token || /[\r\n\0]/.test(token)) {
    throw new Error('golden preflight: token file contains an invalid bounded credential')
  }
}

function runSmokePreflight() {
  const goldenURL = new URL(required('WORKSFLOW_GOLDEN_STACK_URL'))
  const allowHTTP = process.env.WORKSFLOW_GOLDEN_ALLOW_HTTP === 'true'
  if (goldenURL.protocol !== 'https:') {
    const loopback = goldenURL.hostname === 'localhost' || goldenURL.hostname === '127.0.0.1' || goldenURL.hostname === '::1'
    if (!allowHTTP || !loopback || goldenURL.protocol !== 'http:') {
      throw new Error('golden preflight: HTTP is allowed only for an explicit loopback smoke')
    }
  }
  const templateReleaseId = required('WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_ID')
  requireUUID(templateReleaseId, 'WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_ID')
  requireDigest(required('WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_HASH'), 'WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_HASH')
  validateSmokeCredential(required('WORKSFLOW_GOLDEN_E2E_TOKEN_FILE'))
  const expiresAt = Date.parse(required('WORKSFLOW_GOLDEN_TOKEN_EXPIRES_AT'))
  const lifetime = expiresAt - Date.now()
  if (!Number.isFinite(expiresAt) || lifetime < 2 * 60_000 || lifetime > 30 * 60_000) {
    throw new Error('golden preflight: token must expire between 2 and 30 minutes from preflight time')
  }
}

function runQualificationPreflight(runId) {
  const legacyBusinessFacts = [
    'WORKSFLOW_GOLDEN_ALLOW_HTTP',
    'WORKSFLOW_GOLDEN_E2E_TOKEN_FILE',
    'WORKSFLOW_GOLDEN_STACK_URL',
    'WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_HASH',
    'WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_ID',
    'WORKSFLOW_GOLDEN_TOKEN_EXPIRES_AT',
    'WORKSFLOW_GOLDEN_ADMIN_TOKEN_FILE',
    'WORKSFLOW_GOLDEN_API_A_TOKEN_FILE',
    'WORKSFLOW_GOLDEN_API_B_TOKEN_FILE',
    'WORKSFLOW_GOLDEN_BROWSER_A_STORAGE_STATE_FILE',
    'WORKSFLOW_GOLDEN_BROWSER_B_STORAGE_STATE_FILE',
    'WORKSFLOW_GOLDEN_FAULT_OPERATOR_TOKEN_FILE',
    'WORKSFLOW_GOLDEN_OWNER_TOKEN_FILE',
  ]
  const legacy = legacyBusinessFacts.filter((name) => (process.env[name]?.trim() ?? '') !== '')
  if (legacy.length > 0) {
    throw new Error(`golden preflight: qualification rejects smoke-only inputs: ${legacy.join(', ')}`)
  }
  const planDigest = required('WORKSFLOW_QUALIFICATION_PLAN_DIGEST')
  requireDigest(planDigest, 'WORKSFLOW_QUALIFICATION_PLAN_DIGEST')
  const manifest = parseCanonicalJSON(
    readFileSync(resolve(repositoryRoot, 'qualification/manifest.json')),
    'qualification manifest',
  )
  const actualPlanDigest = qualificationPlanDigest(manifest, repositoryRoot)
  if (actualPlanDigest !== planDigest) throw new Error('golden preflight: qualification plan digest drift')
  goldenExecutionTestPaths(manifest)
  const documents = loadGoldenDocuments(process.env)
  if (documents.authority.document.subject.runId !== runId) {
    throw new Error('golden preflight: qualification run identity drift')
  }
  verifyGoldenSourceRepository(
    repositoryRoot,
    documents.fixture.document.subject.sharedArtifacts.sourceRepository,
  )
  loadGoldenCredentialFiles(process.env, documents.fixture)
  // These files are deliberately called root-issued/hash-bound, not signed.
  // The promotion authority and immutable qualification receipt are the final
  // trust boundary that binds their exact document digests.
}

const runId = required('WORKSFLOW_QUALIFICATION_RUN_ID')
requireUUID(runId, 'WORKSFLOW_QUALIFICATION_RUN_ID')
requireFreshArtifactDirectory()

if (process.env.WORKSFLOW_GOLDEN_CAPTURE_TRACE === 'true') {
  throw new Error('golden preflight: trace capture remains disabled until reviewed credential-safe artifact handling is active')
}

if (mode === '--smoke') {
  runSmokePreflight()
  console.log(`golden preflight: partial smoke inputs accepted for run ${runId}; no stage-exit receipt will be issued`)
} else {
  runQualificationPreflight(runId)
  console.log(`golden preflight: root-issued/hash-bound qualification inputs accepted for run ${runId}`)
}
