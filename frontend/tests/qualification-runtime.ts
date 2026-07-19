import { expect, request, test } from '@playwright/test'
import type {
  APIRequestContext,
  Browser,
  BrowserContext,
  BrowserContextOptions,
  Page,
} from '@playwright/test'
import { lstatSync, readFileSync, realpathSync } from 'node:fs'
import { isAbsolute, resolve } from 'node:path'

import { loadGoldenQualificationInputs } from '../scripts/golden-authority.mjs'

export type GoldenAuthority = Readonly<{
  schemaVersion: 'worksflow-golden-authority/v2'
  subject: Readonly<{
    authorityId: string
    expiresAt: string
    fixtureHash: string
    issuance: 'root-issued-hash-bound'
    issuedAt: string
    planDigest: string
    runId: string
  }>
}>

type GoldenPrincipal = Readonly<{
  actorId: string
  projectId: string
  realm: 'control' | 'platform' | 'reference'
  role: 'admin' | 'fault-operator' | 'owner' | 'user'
  slot: string
  tenantId: string
}>

export type GoldenFixture = Readonly<{
  authorityHash: string
  schemaVersion: 'worksflow-golden-fixture/v2'
  subject: Readonly<{
    credentialSet: Readonly<{
      audience: string
      credentialSetHandleHash: string
      memberBindingsDigest: string
      memberCount: 11
      setId: string
    }>
    expiresAt: string
    fixtureId: string
    issuedAt: string
    planDigest: string
    platform: Readonly<{ apiOrigin: string; webOrigin: string }>
    principals: readonly GoldenPrincipal[]
    reference: Readonly<{
      apiImageDigest: string
      apiOrigin: string
      applicationId: string
      commands: Readonly<Record<'api' | 'migration' | 'retention' | 'web', Readonly<{
        argv: readonly string[]
        identity: string
        workingDirectory: string
      }>>>
      contractBundle: Readonly<{ contentHash: string; id: string }>
      deploymentReceipt: Readonly<{
        contentHash: string
        id: string
        schemaVersion: 'reference-deployment-runtime-receipt/v1'
      }>
      gateway: Readonly<{
        attestationDigest: string
        capabilityDigest: string
        identity: string
        modelProfile: Readonly<{
          contentHash: string
          id: string
          maxAttempts: 3
          modelId: string
          modelRevision: string
          providerId: string
          timeoutMilliseconds: 120000
        }>
        providerPolicy: Readonly<{
          contentHash: string
          fallbackAllowed: false
          id: 'reference-project-default'
          profilePinned: true
        }>
        routeId: string
        secretInjectionReceipt: Readonly<{ contentHash: string; id: string }>
      }>
      migration: Readonly<{ contentHash: string; identity: string }>
      qualificationOperationSet: Readonly<{
        contentHash: string
        operations: readonly [
          'migration-rerun',
          'rate-limit-observation',
          'reference-audit-observation',
          'retention-job',
          'run-execution-observation',
          'timeout-vector',
        ]
        schemaVersion: 'reference-qualification-operation-set/v1'
      }>
      rateLimitPolicy: Readonly<{
        burst: 10
        contentHash: string
        id: 'reference-rate-limit-v1'
        requests: 60
        scopes: readonly ['project', 'tenant-actor']
        windowSeconds: 60
      }>
      retentionPolicy: Readonly<{
        auditDays: 90
        contentHash: string
        eventDays: 30
        id: string
        messageDays: 30
        redactionRequired: true
        runDays: 90
      }>
      runEventSchemaDigest: string
      webImageDigest: string
      webOrigin: string
    }>
    runId: string
  }>
}>

type BearerCredential = Readonly<{
  actorId: string
  audience: string
  headers: Readonly<{ Authorization: string }>
  role: string
}>

type StorageCredential = Readonly<{
  actorId: string
  audience: string
  csrf?: Readonly<{ cookieName: string; headerName: string }>
  role: string
  storageState: Readonly<{
    cookies: readonly Readonly<Record<string, unknown>>[]
    origins: readonly Readonly<Record<string, unknown>>[]
  }>
}>

export type GoldenQualificationEnvironment = Readonly<{
  authority: GoldenAuthority
  authorityHash: string
  credentials: Readonly<{
    platform: Readonly<{
      admin: BearerCredential
      apiA: BearerCredential
      apiB: BearerCredential
      browserA: StorageCredential
      browserB: StorageCredential
      faultOperator: BearerCredential
      owner: BearerCredential
    }>
    reference: Readonly<{
      apiA: StorageCredential
      apiB: StorageCredential
      browserA: StorageCredential
      browserB: StorageCredential
    }>
  }>
  fixture: GoldenFixture
  fixtureHash: string
  mode: 'qualification'
}>

type SmokeEnvironment = Readonly<{
  authorization: Readonly<{ Authorization: string }>
  goldenOrigin: URL
  mode: 'partial-smoke'
  templateReleaseHash: string
  templateReleaseId: string
}>

let cachedQualificationEnvironment: GoldenQualificationEnvironment | undefined
let cachedSmokeEnvironment: SmokeEnvironment | undefined

function required(name: string) {
  const value = process.env[name]?.trim() ?? ''
  if (!value || /[\r\n\0]/.test(value)) throw new Error(`qualification runtime: ${name} is required`)
  return value
}

function readSmokeToken(path: string) {
  if (!isAbsolute(path) || resolve(path) !== path) {
    throw new Error('qualification runtime: smoke token path must be absolute and normalized')
  }
  const fileStat = lstatSync(path)
  if (
    fileStat.isSymbolicLink() || !fileStat.isFile() || fileStat.nlink !== 1 ||
    realpathSync(path) !== path || (fileStat.mode & 0o777) !== 0o600
  ) {
    throw new Error('qualification runtime: smoke token must be a single-link mode-0600 regular non-symlink file')
  }
  const currentUID = typeof process.getuid === 'function' ? process.getuid() : fileStat.uid
  if (fileStat.uid !== 0 && fileStat.uid !== currentUID) {
    throw new Error('qualification runtime: smoke token must be owned by root or the current user')
  }
  const token = readFileSync(path, 'utf8')
  if (token.length < 32 || token.length > 8192 || token.trim() !== token || /[\r\n\0]/.test(token)) {
    throw new Error('qualification runtime: token file contains an invalid bounded credential')
  }
  return token
}

// The legacy helper is intentionally smoke-only. In qualification mode it is
// forbidden because its single Bearer header could be attached to a browser
// and escape to another origin. Qualification callers must use the typed,
// per-principal API and browser credential slots below.
export function qualificationEnvironment(): SmokeEnvironment {
  if (process.env.WORKSFLOW_GOLDEN_AUTHORITY_FILE?.trim()) {
    throw new Error('qualification runtime: the partial-smoke helper is forbidden in qualification mode')
  }
  if (cachedSmokeEnvironment) return cachedSmokeEnvironment
  const token = readSmokeToken(required('WORKSFLOW_GOLDEN_E2E_TOKEN_FILE'))
  cachedSmokeEnvironment = Object.freeze({
    authorization: Object.freeze({ Authorization: `Bearer ${token}` }),
    goldenOrigin: new URL(required('WORKSFLOW_GOLDEN_STACK_URL')),
    mode: 'partial-smoke' as const,
    templateReleaseHash: required('WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_HASH'),
    templateReleaseId: required('WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_ID'),
  })
  return cachedSmokeEnvironment
}

function bearerCredential(
  principal: GoldenPrincipal,
  credential: Readonly<{ audience: string; token: string }>,
): BearerCredential {
  return Object.freeze({
    actorId: principal.actorId,
    audience: credential.audience,
    headers: Object.freeze({ Authorization: `Bearer ${credential.token}` }),
    role: principal.role,
  })
}

function storageCredential(
  principal: GoldenPrincipal,
  credential: Readonly<{
    audience: string
    csrf?: Readonly<{ cookieName: string; headerName: string }>
    storageState: StorageCredential['storageState']
  }>,
): StorageCredential {
  // Browser and Reference API cookie identities expose parsed storage state
  // only. They deliberately have no
  // Authorization header, preventing a global extraHTTPHeaders Bearer from
  // being replayed when a page follows a cross-origin request.
  return Object.freeze({
    actorId: principal.actorId,
    audience: credential.audience,
    ...(credential.csrf ? { csrf: credential.csrf } : {}),
    role: principal.role,
    storageState: credential.storageState,
  })
}

export function goldenQualificationEnvironment(): GoldenQualificationEnvironment {
  if (cachedQualificationEnvironment) return cachedQualificationEnvironment
  const loaded = loadGoldenQualificationInputs(process.env)
  const authority = loaded.authority.document as GoldenAuthority
  const fixture = loaded.fixture.document as GoldenFixture
  const principal = new Map(fixture.subject.principals.map((entry) => [entry.slot, entry]))
  const credentials = loaded.credentials as Readonly<Record<string, Readonly<{
    audience: string
    csrf?: Readonly<{ cookieName: string; headerName: string }>
    storageState: StorageCredential['storageState']
    token: string
  }>>>
  cachedQualificationEnvironment = Object.freeze({
    authority,
    authorityHash: loaded.authority.authorityHash,
    credentials: Object.freeze({
      platform: Object.freeze({
        admin: bearerCredential(principal.get('platform-admin')!, credentials['platform-admin']!),
        apiA: bearerCredential(principal.get('platform-user-a')!, credentials['platform-api-a']!),
        apiB: bearerCredential(principal.get('platform-user-b')!, credentials['platform-api-b']!),
        browserA: storageCredential(principal.get('platform-user-a')!, credentials['platform-browser-a']!),
        browserB: storageCredential(principal.get('platform-user-b')!, credentials['platform-browser-b']!),
        faultOperator: bearerCredential(principal.get('fault-operator')!, credentials['platform-fault-operator']!),
        owner: bearerCredential(principal.get('platform-owner')!, credentials['platform-owner']!),
      }),
      reference: Object.freeze({
        apiA: storageCredential(principal.get('reference-user-a')!, credentials['reference-api-a']!),
        apiB: storageCredential(principal.get('reference-user-b')!, credentials['reference-api-b']!),
        browserA: storageCredential(principal.get('reference-user-a')!, credentials['reference-browser-a']!),
        browserB: storageCredential(principal.get('reference-user-b')!, credentials['reference-browser-b']!),
      }),
    }),
    fixture,
    fixtureHash: loaded.fixture.fixtureHash,
    mode: 'qualification' as const,
  })
  return cachedQualificationEnvironment
}

// Golden sources import the reviewed runtime instead of reaching through to
// Playwright directly. Keeping both values and their public types behind this
// module makes that boundary usable without an unreviewed type-only bypass.
export { expect, request, test }
export type { APIRequestContext, Browser, BrowserContext, BrowserContextOptions, Page }
