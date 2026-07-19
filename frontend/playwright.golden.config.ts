import { defineConfig, devices } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  goldenExecutionTestPaths,
  loadGoldenCredentialFiles,
  loadGoldenDocuments,
  verifyGoldenSourceRepository,
} from './scripts/golden-authority.mjs'
import { parseCanonicalJSON } from './scripts/qualification-core.mjs'
import { qualificationPlanDigest } from './scripts/qualification-plan.mjs'

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

const artifactDirectory = process.env.WORKSFLOW_QUALIFICATION_ARTIFACT_DIR?.trim() ?? ''
if (!artifactDirectory || !isAbsolute(artifactDirectory) || resolve(artifactDirectory) !== artifactDirectory) {
  throw new Error('WORKSFLOW_QUALIFICATION_ARTIFACT_DIR must be an absolute normalized path')
}

const qualificationMode = Boolean(process.env.WORKSFLOW_GOLDEN_AUTHORITY_FILE?.trim())
let exactTestMatch: RegExp[]
if (qualificationMode) {
  // Loading the hash-bound inputs here as well as in preflight makes a direct
  // `playwright test --config ...` invocation fail closed. No global Bearer
  // header is installed.
  const manifest = parseCanonicalJSON(
    readFileSync(resolve(repositoryRoot, 'qualification/manifest.json')),
    'qualification manifest',
  )
  const expectedPlanDigest = process.env.WORKSFLOW_QUALIFICATION_PLAN_DIGEST?.trim() ?? ''
  if (qualificationPlanDigest(manifest, repositoryRoot) !== expectedPlanDigest) {
    throw new Error('Golden Playwright config qualification plan digest drift')
  }
  const testPaths = goldenExecutionTestPaths(manifest)
  const documents = loadGoldenDocuments(process.env)
  verifyGoldenSourceRepository(
    repositoryRoot,
    documents.fixture.document.subject.sharedArtifacts.sourceRepository,
  )
  loadGoldenCredentialFiles(process.env, documents.fixture)
  exactTestMatch = testPaths.map((path) => new RegExp(
    `(?:^|/)${path.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`,
  ))
} else if (!process.env.WORKSFLOW_GOLDEN_E2E_TOKEN_FILE?.trim()) {
  throw new Error('Golden Playwright config requires explicit smoke or qualification inputs')
} else {
  const rawOrigin = process.env.WORKSFLOW_GOLDEN_STACK_URL?.trim() ?? ''
  let origin
  try {
    origin = new URL(rawOrigin)
  } catch {
    throw new Error('Golden smoke requires a canonical stack origin')
  }
  const loopback = origin.hostname === 'localhost' || origin.hostname === '127.0.0.1' || origin.hostname === '::1'
  if (
    rawOrigin !== origin.origin ||
    (origin.protocol !== 'https:' && !(
      origin.protocol === 'http:' && loopback && process.env.WORKSFLOW_GOLDEN_ALLOW_HTTP === 'true'
    ))
  ) {
    throw new Error('Golden smoke permits HTTP only for an explicit canonical loopback origin')
  }
  exactTestMatch = [/(?:^|\/)golden-stack\.spec\.ts$/]
}

export default defineConfig({
  testDir: './tests',
  testMatch: exactTestMatch,
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  workers: 1,
  reporter: [
    ['line'],
    ['json', { outputFile: `${artifactDirectory}/playwright-results.json` }],
  ],
  outputDir: `${artifactDirectory}/playwright`,
  use: {
    locale: 'en-US',
    screenshot: 'only-on-failure',
    video: 'on',
    // The test uses a real Bearer credential. A Playwright trace may contain
    // request headers, so trace evidence remains fail closed until a reviewed
    // redaction/encryption + credential-revocation pipeline exists.
    trace: 'off',
  },
  projects: [
    {
      name: 'golden-chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
