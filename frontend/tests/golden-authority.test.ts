import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import {
  chmodSync,
  existsSync,
  linkSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import {
  computeGoldenSourceContentTreeDigest,
  goldenExecutionTestPaths,
  goldenAuthoritySchema,
  goldenCredentialMembersSchema,
  goldenCredentialSlots,
  goldenFaultOperationKinds,
  goldenFaultOperationSetDigest,
  goldenFaultOperationSetSchema,
  goldenFixtureSchema,
  goldenReferenceDeploymentReceiptSchema,
  goldenReferenceOperationKinds,
  goldenReferenceOperationSetDigest,
  goldenReferenceOperationSetSchema,
  hashGoldenCanonicalValue,
  hashGoldenCredentialMemberBindings,
  loadGoldenDocuments,
  loadGoldenQualificationInputs,
  parseGoldenAuthority,
  parseGoldenFixture,
} from '../scripts/golden-authority.mjs'
import { canonicalJSON } from '../scripts/qualification-core.mjs'

function expectFailure(action: () => unknown, pattern: RegExp) {
  assert.throws(action, pattern)
}

function digest(value: string | Buffer) {
  return `sha256:${createHash('sha256').update(value).digest('hex')}`
}

function uuid(value: number) {
  return `10000000-0000-4000-8000-${String(value).padStart(12, '0')}`
}

function identity(value: number) {
  return { contentHash: digest(`artifact-${value}`), id: uuid(value) }
}

const credentialEnvironment: Readonly<Record<string, string>> = Object.freeze({
  'platform-admin': 'WORKSFLOW_GOLDEN_PLATFORM_ADMIN_TOKEN_FILE',
  'platform-api-a': 'WORKSFLOW_GOLDEN_PLATFORM_API_A_TOKEN_FILE',
  'platform-api-b': 'WORKSFLOW_GOLDEN_PLATFORM_API_B_TOKEN_FILE',
  'platform-browser-a': 'WORKSFLOW_GOLDEN_PLATFORM_BROWSER_A_STORAGE_STATE_FILE',
  'platform-browser-b': 'WORKSFLOW_GOLDEN_PLATFORM_BROWSER_B_STORAGE_STATE_FILE',
  'platform-fault-operator': 'WORKSFLOW_GOLDEN_PLATFORM_FAULT_OPERATOR_TOKEN_FILE',
  'platform-owner': 'WORKSFLOW_GOLDEN_PLATFORM_OWNER_TOKEN_FILE',
  'reference-api-a': 'WORKSFLOW_GOLDEN_REFERENCE_API_A_STORAGE_STATE_FILE',
  'reference-api-b': 'WORKSFLOW_GOLDEN_REFERENCE_API_B_STORAGE_STATE_FILE',
  'reference-browser-a': 'WORKSFLOW_GOLDEN_REFERENCE_BROWSER_A_STORAGE_STATE_FILE',
  'reference-browser-b': 'WORKSFLOW_GOLDEN_REFERENCE_BROWSER_B_STORAGE_STATE_FILE',
})

const memberDefinition = Object.freeze({
  'platform-admin': { kind: 'token', principal: 'platform-admin' },
  'platform-api-a': { kind: 'token', principal: 'platform-user-a' },
  'platform-api-b': { kind: 'token', principal: 'platform-user-b' },
  'platform-browser-a': { kind: 'storage-state', principal: 'platform-user-a' },
  'platform-browser-b': { kind: 'storage-state', principal: 'platform-user-b' },
  'platform-fault-operator': { kind: 'token', principal: 'fault-operator' },
  'platform-owner': { kind: 'token', principal: 'platform-owner' },
  'reference-api-a': { kind: 'storage-state', principal: 'reference-user-a' },
  'reference-api-b': { kind: 'storage-state', principal: 'reference-user-b' },
  'reference-browser-a': { kind: 'storage-state', principal: 'reference-user-a' },
  'reference-browser-b': { kind: 'storage-state', principal: 'reference-user-b' },
} as const)

const faultResourceSelector: Readonly<Record<string, string>> = Object.freeze({
  'agent-runner-crash': 'agent.runner',
  'agent-runner-timeout': 'agent.runner',
  'agent-security-canary': 'agent.patch-policy',
  'controller-conflict': 'release.controller',
  'controller-maintenance': 'release.controller',
  'controller-not-found': 'release.controller',
  'controller-timeout': 'release.controller',
  'lsp-resource-pressure': 'lsp.runtime',
  'lsp-runtime-crash': 'lsp.runtime',
  'lsp-runtime-drift': 'lsp.runtime',
  'reference-gateway-outage': 'reference.gateway',
  'reference-process-restart': 'reference.process',
  'sandbox-dependency-crash': 'sandbox.dependency',
} as const)

function makeFixture(now: number) {
  const issuedAt = new Date(now - 60_000).toISOString()
  const expiresAt = new Date(now + 5 * 60_000).toISOString()
  const principals = [
    { actorId: uuid(101), projectId: uuid(201), realm: 'control', role: 'fault-operator', slot: 'fault-operator', tenantId: uuid(301) },
    { actorId: uuid(102), projectId: uuid(202), realm: 'platform', role: 'admin', slot: 'platform-admin', tenantId: uuid(302) },
    { actorId: uuid(103), projectId: uuid(203), realm: 'platform', role: 'owner', slot: 'platform-owner', tenantId: uuid(303) },
    { actorId: uuid(104), projectId: uuid(204), realm: 'platform', role: 'user', slot: 'platform-user-a', tenantId: uuid(304) },
    { actorId: uuid(105), projectId: uuid(205), realm: 'platform', role: 'user', slot: 'platform-user-b', tenantId: uuid(305) },
    { actorId: uuid(106), projectId: uuid(206), realm: 'reference', role: 'user', slot: 'reference-user-a', tenantId: uuid(306) },
    { actorId: uuid(107), projectId: uuid(207), realm: 'reference', role: 'user', slot: 'reference-user-b', tenantId: uuid(307) },
  ]
  const principalBySlot = new Map(principals.map((principal) => [principal.slot, principal]))
  const credentialHandles = Object.fromEntries(
    goldenCredentialSlots.map((slot) => [slot, `${slot}-opaque-issuer-handle-${'h'.repeat(48)}`]),
  ) as Record<string, string>
  const memberBindings = goldenCredentialSlots.map((slot) => ({
    actorId: principalBySlot.get(memberDefinition[slot as keyof typeof memberDefinition].principal)!.actorId,
    credentialHandleHash: digest(credentialHandles[slot]!),
    kind: memberDefinition[slot as keyof typeof memberDefinition].kind,
    slot,
  }))
  const imageDigest = {
    agent: digest('agent-runner-image'),
    lsp: digest('language-server-image'),
    qualificationRunner: digest('qualification-runner-image'),
    qualificationVerifier: digest('qualification-verifier-image'),
    release: digest('release-controller-image'),
    sandbox: digest('sandbox-runner-image'),
  }
  const runtimeImages = [
    ['agent-runner', imageDigest.agent],
    ['language-server', imageDigest.lsp],
    ['qualification-runner', imageDigest.qualificationRunner],
    ['qualification-verifier', imageDigest.qualificationVerifier],
    ['release-controller', imageDigest.release],
    ['sandbox-runner', imageDigest.sandbox],
  ].map(([role, image], index) => ({
    imageDigest: image,
    provenance: identity(500 + index * 3),
    role,
    sbom: identity(501 + index * 3),
    signature: identity(502 + index * 3),
  }))
  const contractBundle = identity(50)
  const fixture = {
    authorityHash: digest('pending-authority'),
    schemaVersion: goldenFixtureSchema,
    subject: {
      agent: {
        modelGateway: {
          attestationDigest: digest('model-gateway-attestation'),
          identity: 'spiffe://golden.example.test/model-gateway',
          modelId: 'approved-model',
          modelRevision: 'model-v1',
          profileId: 'model-profile-v1',
          providerId: 'approved-provider',
        },
        runner: {
          identity: 'spiffe://golden.example.test/agent-runner',
          imageDigest: imageDigest.agent,
          profileId: 'agent-runner-v1',
        },
      },
      credentialSet: {
        audience: 'urn:worksflow:golden-stack',
        credentialSetHandleHash: digest(`set-handle-${'s'.repeat(48)}`),
        expiresAt,
        issuedAt,
        issuer: 'spiffe://golden.example.test/credential-issuer',
        issuerAttestationDigest: digest('credential-issuance-dsse-payload'),
        memberBindings,
        memberBindingsDigest: hashGoldenCredentialMemberBindings(memberBindings),
        memberCount: memberBindings.length,
        setId: uuid(10),
      },
      expiresAt,
      faultAuthorities: goldenFaultOperationKinds.map((operationKind, index) => ({
        authorityId: uuid(700 + index * 2),
        dsse: {
          artifactId: uuid(701 + index * 2),
          envelopeDigest: digest(`${operationKind}-fault-envelope`),
          payloadDigest: digest(`${operationKind}-fault-payload`),
          payloadType: 'application/vnd.worksflow.golden-fault-authority+json;version=1',
        },
        expectedFenceDigest: digest(`${operationKind}-precondition`),
        maxUses: 1,
        operationKind,
        resourceSelector: faultResourceSelector[operationKind],
      })),
      fixtureId: uuid(11),
      issuedAt,
      lsp: {
        gateway: {
          apiOrigin: 'https://platform-api.golden.example.test',
          path: '/v1/sandbox-lsp',
          ticketProtocolDigest: digest('lsp-ticket-protocol'),
          wssProtocolDigest: digest('lsp-wss-protocol'),
        },
        runtime: {
          capabilityDigest: digest('lsp-capabilities'),
          identity: 'spiffe://golden.example.test/language-server',
          imageDigest: imageDigest.lsp,
          languages: ['typescript'],
          profileId: 'lsp-runtime-v1',
        },
      },
      planDigest: digest('qualification-plan'),
      platform: {
        apiOrigin: 'https://platform-api.golden.example.test',
        apiSchemaDigest: digest('platform-api-schema'),
        deploymentReceipt: identity(30),
        serverBuild: {
          buildId: 'platform-build-v1',
          imageDigest: digest('platform-server-image'),
          version: '1.0.0',
        },
        webOrigin: 'https://platform-web.golden.example.test',
        wssProtocolDigest: digest('platform-wss-protocol'),
      },
      principals,
      reference: {
        apiImageDigest: digest('reference-api-image'),
        apiOrigin: 'https://reference-api.golden.example.test',
        applicationId: uuid(40),
        commands: {
          api: {
            argv: ['./bin/reference-api', 'serve'],
            identity: 'reference-api-command-v1',
            workingDirectory: '/workspace',
          },
          migration: {
            argv: ['./bin/reference-api', 'migrate', 'up'],
            identity: 'reference-migration-command-v1',
            workingDirectory: '/workspace',
          },
          retention: {
            argv: ['./bin/reference-api', 'retention', 'run'],
            identity: 'reference-retention-command-v1',
            workingDirectory: '/workspace',
          },
          web: {
            argv: ['node', 'frontend/server.js'],
            identity: 'reference-web-command-v1',
            workingDirectory: '/workspace',
          },
        },
        contractBundle,
        deploymentReceipt: {
          ...identity(41),
          schemaVersion: goldenReferenceDeploymentReceiptSchema,
        },
        gateway: {
          attestationDigest: digest('reference-gateway-attestation'),
          capabilityDigest: digest('reference-gateway-capability'),
          identity: 'spiffe://golden.example.test/reference-model-gateway',
          modelProfile: {
            contentHash: digest('reference-model-profile'),
            id: 'reference-model-profile-v1',
            maxAttempts: 3,
            modelId: 'reference-model',
            modelRevision: 'reference-model-v1',
            providerId: 'reference-provider',
            timeoutMilliseconds: 120_000,
          },
          providerPolicy: {
            contentHash: digest('reference-provider-policy'),
            fallbackAllowed: false,
            id: 'reference-project-default',
            profilePinned: true,
          },
          routeId: 'reference-generated-app-route-v1',
          secretInjectionReceipt: identity(43),
        },
        migration: { contentHash: digest('reference-migration'), identity: 'reference-migration-v1' },
        qualificationOperationSet: {
          contentHash: goldenReferenceOperationSetDigest,
          operations: goldenReferenceOperationKinds,
          schemaVersion: goldenReferenceOperationSetSchema,
        },
        rateLimitPolicy: {
          burst: 10,
          contentHash: digest('reference-rate-limit-policy'),
          id: 'reference-rate-limit-v1',
          requests: 60,
          scopes: ['project', 'tenant-actor'],
          windowSeconds: 60,
        },
        retentionPolicy: {
          auditDays: 90,
          ...identity(42),
          eventDays: 30,
          messageDays: 30,
          redactionRequired: true,
          runDays: 90,
        },
        runEventSchemaDigest: digest('reference-run-event-schema'),
        webImageDigest: digest('reference-web-image'),
        webOrigin: 'https://reference-web.golden.example.test',
      },
      release: {
        controller: {
          identity: 'spiffe://golden.example.test/release-controller',
          imageDigest: imageDigest.release,
          profileId: 'release-controller-v1',
          protocol: 'worksflow-release-controller.v1',
          trustKeyDigest: digest('release-controller-trust-key'),
        },
      },
      runId: uuid(12),
      sandbox: {
        apiOrigin: 'https://platform-api.golden.example.test',
        runner: {
          identity: 'spiffe://golden.example.test/sandbox-runner',
          imageDigest: imageDigest.sandbox,
          profileId: 'sandbox-runner-v1',
        },
        runtimeProfileId: 'sandbox-runtime-v1',
        serviceProfiles: [{
          id: 'sandbox-api-v1',
          imageDigest: digest('sandbox-api-image'),
          protocol: 'http',
          service: 'sandbox-api',
        }],
      },
      sharedArtifacts: {
        buildContract: identity(51),
        buildManifest: identity(52),
        referenceContractBundle: contractBundle,
        runtimeImages,
        sourceRepository: {
          commitOid: 'a'.repeat(40),
          contentTreeDigest: digest('source-content-tree'),
        },
        templateRelease: {
          approvalReceiptDigest: digest('template-approval-receipt'),
          contentHash: digest('template-release-content'),
          id: uuid(53),
        },
        workspaceRevision: {
          canonicalQualityReceiptDigest: digest('canonical-quality-receipt'),
          contentHash: digest('workspace-revision-content'),
          id: uuid(54),
        },
      },
    },
  }
  return { credentialHandles, fixture }
}

function makeDocuments(now: number) {
  const { credentialHandles, fixture } = makeFixture(now)
  const authority = {
    schemaVersion: goldenAuthoritySchema,
    subject: {
      authorityId: uuid(1),
      expiresAt: fixture.subject.expiresAt,
      fixtureHash: hashGoldenCanonicalValue(fixture.subject),
      issuance: 'root-issued-hash-bound',
      issuedAt: fixture.subject.issuedAt,
      planDigest: fixture.subject.planDigest,
      runId: fixture.subject.runId,
    },
  }
  fixture.authorityHash = hashGoldenCanonicalValue(authority.subject)
  return { authority, credentialHandles, fixture }
}

function storageState(slot: string, origin: string, expiresAt: string, csrf: boolean, includeOrigin: boolean) {
  const host = new URL(origin).hostname
  const cookies = [
    ...(csrf ? [{
      domain: host,
      expires: Date.parse(expiresAt) / 1000,
      httpOnly: false,
      name: 'csrf-token',
      path: '/',
      sameSite: 'Strict',
      secure: true,
      value: `${slot}-csrf-value-${'c'.repeat(32)}`,
    }] : []),
    {
      domain: host,
      expires: Date.parse(expiresAt) / 1000,
      httpOnly: true,
      name: 'session',
      path: '/',
      sameSite: 'Lax',
      secure: true,
      value: `${slot}-session-value-${'s'.repeat(32)}`,
    },
  ]
  return {
    cookies,
    origins: includeOrigin
      ? [{ localStorage: [], origin }]
      : [],
  }
}

function credentialDocument(slot: string, fixture: ReturnType<typeof makeFixture>['fixture'], handles: Record<string, string>) {
  const definition = memberDefinition[slot as keyof typeof memberDefinition]
  const principal = fixture.subject.principals.find((entry) => entry.slot === definition.principal)!
  if (definition.kind === 'token') {
    return {
      actorId: principal.actorId,
      audience: fixture.subject.credentialSet.audience,
      credentialHandle: handles[slot],
      credentialSetId: fixture.subject.credentialSet.setId,
      expiresAt: fixture.subject.credentialSet.expiresAt,
      issuedAt: fixture.subject.credentialSet.issuedAt,
      issuer: fixture.subject.credentialSet.issuer,
      runId: fixture.subject.runId,
      schemaVersion: 'worksflow-golden-bearer-credential/v1',
      slot,
      token: `${slot}-bearer-token-${'t'.repeat(48)}`,
      tokenType: 'Bearer',
    }
  }
  const platform = slot.startsWith('platform-browser-')
  const referenceAPI = slot.startsWith('reference-api-')
  const audience = platform
    ? fixture.subject.platform.webOrigin
    : referenceAPI
      ? fixture.subject.reference.apiOrigin
      : fixture.subject.reference.webOrigin
  return {
    actorId: principal.actorId,
    audience,
    credentialHandle: handles[slot],
    credentialSetAudience: fixture.subject.credentialSet.audience,
    credentialSetId: fixture.subject.credentialSet.setId,
    ...(platform ? { csrf: { cookieName: 'csrf-token', headerName: 'X-CSRF-Token' } } : {}),
    expiresAt: fixture.subject.credentialSet.expiresAt,
    issuedAt: fixture.subject.credentialSet.issuedAt,
    issuer: fixture.subject.credentialSet.issuer,
    runId: fixture.subject.runId,
    schemaVersion: 'worksflow-golden-storage-credential/v1',
    sessionCookieName: 'session',
    slot,
    storageState: storageState(slot, audience, fixture.subject.credentialSet.expiresAt, platform, !referenceAPI),
  }
}

function writeCanonical(path: string, value: unknown, mode?: number) {
  writeFileSync(path, canonicalJSON(value))
  if (mode !== undefined) chmodSync(path, mode)
}

function prepareEnvironment(root: string, now: number) {
  const { authority, credentialHandles, fixture } = makeDocuments(now)
  const authorityPath = join(root, 'authority.json')
  const fixturePath = join(root, 'fixture.json')
  const authorityBytes = canonicalJSON(authority)
  const fixtureBytes = canonicalJSON(fixture)
  writeFileSync(authorityPath, authorityBytes)
  writeFileSync(fixturePath, fixtureBytes)
  const paths: Record<string, string> = {}
  const environment: NodeJS.ProcessEnv = {
    NODE_ENV: 'test',
    WORKSFLOW_GOLDEN_AUTHORITY_DIGEST: digest(authorityBytes),
    WORKSFLOW_GOLDEN_AUTHORITY_FILE: authorityPath,
    WORKSFLOW_GOLDEN_FIXTURE_DIGEST: digest(fixtureBytes),
    WORKSFLOW_GOLDEN_FIXTURE_FILE: fixturePath,
    WORKSFLOW_QUALIFICATION_PLAN_DIGEST: fixture.subject.planDigest,
    WORKSFLOW_QUALIFICATION_RUN_ID: fixture.subject.runId,
  }
  for (const slot of goldenCredentialSlots) {
    const path = join(root, `${slot}.json`)
    writeCanonical(path, credentialDocument(slot, fixture, credentialHandles), 0o600)
    paths[slot] = path
    environment[credentialEnvironment[slot]!] = path
  }
  return { authority, authorityPath, credentialHandles, environment, fixture, fixturePath, paths }
}

function main() {
  assert.equal(goldenAuthoritySchema, 'worksflow-golden-authority/v2')
  assert.equal(goldenFixtureSchema, 'worksflow-golden-fixture/v2')
  assert.equal(goldenCredentialMembersSchema, 'worksflow-credential-set-member-bindings/v1')
  assert.equal(goldenFaultOperationSetSchema, 'worksflow-golden-fault-operation-set/v1')
  assert.equal(hashGoldenCanonicalValue({
    operations: goldenFaultOperationKinds,
    schemaVersion: goldenFaultOperationSetSchema,
  }), goldenFaultOperationSetDigest)
  assert.equal(goldenReferenceDeploymentReceiptSchema, 'reference-deployment-runtime-receipt/v1')
  assert.equal(goldenReferenceOperationSetSchema, 'reference-qualification-operation-set/v1')
  assert.equal(hashGoldenCanonicalValue({
    operations: goldenReferenceOperationKinds,
    schemaVersion: goldenReferenceOperationSetSchema,
  }), goldenReferenceOperationSetDigest)
  const operationSetPath = [
    join(process.cwd(), 'qualification', 'golden-fault-operation-set.json'),
    join(process.cwd(), '..', 'qualification', 'golden-fault-operation-set.json'),
  ].find(existsSync)
  assert.ok(operationSetPath, 'root Golden fault-operation contract is missing')
  assert.equal(
    readFileSync(operationSetPath, 'utf8'),
    `${canonicalJSON({ operations: goldenFaultOperationKinds, schemaVersion: goldenFaultOperationSetSchema })}\n`,
  )
  const referenceSpecPath = [
    join(process.cwd(), 'frontend', 'tests', 'golden-reference.spec.ts'),
    join(process.cwd(), 'tests', 'golden-reference.spec.ts'),
  ].find(existsSync)
  assert.ok(referenceSpecPath, 'strict Golden Reference spec is missing')
  const referenceSpec = readFileSync(referenceSpecPath, 'utf8')
  for (const requiredRootClosure of [
    'expect(commands).toEqual(subject.reference.commands)',
    'expect(gateway).toEqual(subject.reference.gateway)',
    'expect(modelProfile).toEqual(subject.reference.gateway.modelProfile)',
    'expect(providerPolicy).toEqual(subject.reference.gateway.providerPolicy)',
    'expect(qualificationOperationSet).toEqual(subject.reference.qualificationOperationSet)',
    'expect(rateLimit).toEqual(subject.reference.rateLimitPolicy)',
    'expect(retention).toEqual(subject.reference.retentionPolicy)',
  ]) {
    assert.ok(referenceSpec.includes(requiredRootClosure), `Reference receipt lost root closure: ${requiredRootClosure}`)
  }
  for (const operation of goldenReferenceOperationKinds) {
    assert.ok(
      referenceSpec.includes(`assertQualificationOperation(authority, '${operation}')`),
      `Reference spec does not exercise root-admitted operation ${operation}`,
    )
  }
  assert.equal(hashGoldenCredentialMemberBindings([
    {
      actorId: '2ada99cd-d941-4e4f-96c0-ad21b0ddcb57',
      credentialHandleHash: digest('api-a-credential'),
      kind: 'token',
      slot: 'api-a',
    },
    {
      actorId: '0d87efc5-006e-454c-8d1d-e32d459d0808',
      credentialHandleHash: digest('browser-a-credential'),
      kind: 'storage-state',
      slot: 'browser-a',
    },
  ]), 'sha256:d9f0a3dbf9240ac7010c65eff8fa43bad8614135ad954a73402121b23a61475f')
  assert.equal(computeGoldenSourceContentTreeDigest([
    {
      mode: '100644',
      path: 'z-last.txt',
      sha256: digest('second\n'),
      sizeBytes: Buffer.byteLength('second\n'),
    },
    {
      mode: '100755',
      path: 'frontend/app/[projectId]/page.tsx',
      sha256: digest('first\n'),
      sizeBytes: Buffer.byteLength('first\n'),
    },
  ]), 'sha256:89d5ad8180f746bf13b572be96a1fe7722ac0af930aaa2cd27dbcf8d4ba3dd28')
  const executionManifest = {
    qualificationSupportPaths: [
      'frontend/tests/golden-agent.spec.ts',
      'frontend/tests/golden-lsp.spec.ts',
      'frontend/tests/golden-reference.spec.ts',
      'frontend/tests/golden-release.spec.ts',
      'frontend/tests/golden-sandbox.spec.ts',
    ],
    suites: ['agent', 'lsp', 'reference', 'release', 'sandbox'].map((name) => ({
      coverage: 'external-complete',
      executionKind: 'playwright',
      id: `${name}-golden-external`,
      mode: 'external-qualification',
      qualificationGroup: 'golden',
      status: 'not-qualified',
      testPaths: [`frontend/tests/golden-${name}.spec.ts`],
    })),
  }
  assert.deepEqual([...goldenExecutionTestPaths(executionManifest)], executionManifest.qualificationSupportPaths)
  const unreviewedCoverage = structuredClone(executionManifest)
  unreviewedCoverage.suites[0]!.coverage = 'planned'
  expectFailure(() => goldenExecutionTestPaths(unreviewedCoverage), /not a reviewed, not-yet-qualified executable suite/)
  const unhashedSpec = structuredClone(executionManifest)
  unhashedSpec.qualificationSupportPaths.shift()
  expectFailure(() => goldenExecutionTestPaths(unhashedSpec), /must be hash-bound qualification support material/)
  assert.deepEqual([...goldenCredentialSlots], [
    'platform-admin',
    'platform-api-a',
    'platform-api-b',
    'platform-browser-a',
    'platform-browser-b',
    'platform-fault-operator',
    'platform-owner',
    'reference-api-a',
    'reference-api-b',
    'reference-browser-a',
    'reference-browser-b',
  ])

  const root = mkdtempSync(join(tmpdir(), 'worksflow-golden-v2-'))
  const now = Date.parse('2026-07-19T12:00:00.000Z')
  try {
    const prepared = prepareEnvironment(root, now)
    const authorityBytes = Buffer.from(canonicalJSON(prepared.authority))
    const fixtureBytes = Buffer.from(canonicalJSON(prepared.fixture))
    const parsedAuthority = parseGoldenAuthority(authorityBytes)
    const parsedFixture = parseGoldenFixture(fixtureBytes)
    assert.equal(parsedAuthority.authorityHash, prepared.fixture.authorityHash)
    assert.equal(parsedFixture.fixtureHash, prepared.authority.subject.fixtureHash)

    const loaded = loadGoldenQualificationInputs(prepared.environment, now)
    assert.equal(Object.keys(loaded.credentials).length, 11)
    assert.ok(Object.isFrozen(loaded.credentials['reference-api-a'].storageState))
    assert.equal(loaded.credentials['platform-api-a'].audience, prepared.fixture.subject.credentialSet.audience)
    assert.equal(loaded.credentials['reference-api-a'].audience, prepared.fixture.subject.reference.apiOrigin)
    assert.equal(loaded.credentials['platform-browser-a'].csrf.headerName, 'X-CSRF-Token')
    const publicFixture = JSON.stringify(loaded.fixture.document)
    assert.doesNotMatch(publicFixture, /bearer-token-|session-value-|csrf-value-|_FILE/)

    const v1Authority = structuredClone(prepared.authority)
    v1Authority.schemaVersion = 'worksflow-golden-authority/v1'
    expectFailure(() => parseGoldenAuthority(Buffer.from(canonicalJSON(v1Authority))), /must be worksflow-golden-authority\/v2/)
    const v1Fixture = structuredClone(prepared.fixture)
    v1Fixture.schemaVersion = 'worksflow-golden-fixture/v1'
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(v1Fixture))), /must be worksflow-golden-fixture\/v2/)

    expectFailure(
      () => parseGoldenAuthority(Buffer.from(`{"schemaVersion":"${goldenAuthoritySchema}","schemaVersion":"${goldenAuthoritySchema}","subject":{}}`)),
      /duplicate JSON name/,
    )
    expectFailure(() => parseGoldenAuthority(Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), authorityBytes])), /BOM-free UTF-8/)
    expectFailure(() => parseGoldenAuthority(Buffer.from([0x7b, 0x22, 0x78, 0x22, 0x3a, 0x22, 0xc0, 0xaf, 0x22, 0x7d])), /valid UTF-8/)
    expectFailure(() => parseGoldenAuthority(Buffer.from(` ${authorityBytes.toString()}`)), /must be canonical JSON/)

    const unknown = structuredClone(prepared.fixture) as unknown as Record<string, unknown>
    unknown.secretBrokerResponse = 'must-not-be-public'
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(unknown))), /secretBrokerResponse is not supported/)
    const nullAgent = structuredClone(prepared.fixture)
    ;(nullAgent.subject as unknown as Record<string, unknown>).agent = null
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(nullAgent))), /agent must be an object/)
    const dynamicID = structuredClone(prepared.fixture)
    ;(dynamicID.subject.sandbox as unknown as Record<string, unknown>).sessionId = uuid(900)
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(dynamicID))), /sessionId is not supported/)

    const unknownReference = structuredClone(prepared.fixture)
    ;(unknownReference.subject.reference.gateway as unknown as Record<string, unknown>).baseURL = 'https://secret.invalid'
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(unknownReference))), /baseURL is not supported/)
    const missingReference = structuredClone(prepared.fixture)
    delete (missingReference.subject.reference as unknown as Record<string, unknown>).gateway
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(missingReference))), /gateway is required/)
    const nullReferenceProfile = structuredClone(prepared.fixture)
    ;(nullReferenceProfile.subject.reference.gateway as unknown as Record<string, unknown>).modelProfile = null
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(nullReferenceProfile))), /modelProfile must be an object/)
    const deploymentKindDrift = structuredClone(prepared.fixture)
    deploymentKindDrift.subject.reference.deploymentReceipt.schemaVersion = 'reference-deployment-runtime-receipt/v2'
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(deploymentKindDrift))), /schemaVersion must be reference-deployment-runtime-receipt\/v1/)
    const operationSetTamper = structuredClone(prepared.fixture)
    ;(operationSetTamper.subject.reference.qualificationOperationSet.operations as unknown as string[])[0] = 'arbitrary-operation'
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(operationSetTamper))), /strictly sorted and unique|closed v1 set/)
    const operationDigestTamper = structuredClone(prepared.fixture)
    operationDigestTamper.subject.reference.qualificationOperationSet.contentHash = digest('tampered-reference-operation-set')
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(operationDigestTamper))), /canonical content hash drift/)
    const referenceGatewayReuse = structuredClone(prepared.fixture)
    referenceGatewayReuse.subject.reference.gateway.identity = referenceGatewayReuse.subject.agent.modelGateway.identity
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(referenceGatewayReuse))), /internal runtime identities must be role-distinct/)
    const referenceProfileReuse = structuredClone(prepared.fixture)
    referenceProfileReuse.subject.reference.gateway.modelProfile.id = referenceProfileReuse.subject.agent.modelGateway.profileId
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(referenceProfileReuse))), /independent from Agent Model Gateway/)
    const referenceProviderReuse = structuredClone(prepared.fixture)
    referenceProviderReuse.subject.reference.gateway.modelProfile.providerId = referenceProviderReuse.subject.agent.modelGateway.providerId
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(referenceProviderReuse))), /independent from Agent Model Gateway/)
    const commandIdentityReuse = structuredClone(prepared.fixture)
    commandIdentityReuse.subject.reference.commands.web.identity = commandIdentityReuse.subject.reference.commands.api.identity
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(commandIdentityReuse))), /commands identities must be role-distinct/)
    const launcherCommand = structuredClone(prepared.fixture)
    launcherCommand.subject.reference.commands.api.argv = ['sh', '-c']
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(launcherCommand))), /approved binary directly/)
    const commitmentReuse = structuredClone(prepared.fixture)
    commitmentReuse.subject.reference.gateway.capabilityDigest = commitmentReuse.subject.reference.gateway.attestationDigest
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(commitmentReuse))), /commitments must be distinct/)
    const referenceArtifactReuse = structuredClone(prepared.fixture)
    referenceArtifactReuse.subject.reference.gateway.secretInjectionReceipt.id =
      referenceArtifactReuse.subject.reference.deploymentReceipt.id
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(referenceArtifactReuse))), /artifact IDs must be distinct/)

    const memberDrift = structuredClone(prepared.fixture)
    memberDrift.subject.credentialSet.memberBindings[0]!.credentialHandleHash = digest('replacement-handle')
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(memberDrift))), /memberBindingsDigest drift/)
    const reorderedMembers = structuredClone(prepared.fixture)
    ;[
      reorderedMembers.subject.credentialSet.memberBindings[0],
      reorderedMembers.subject.credentialSet.memberBindings[1],
    ] = [
      reorderedMembers.subject.credentialSet.memberBindings[1]!,
      reorderedMembers.subject.credentialSet.memberBindings[0]!,
    ]
    reorderedMembers.subject.credentialSet.memberBindingsDigest =
      hashGoldenCredentialMemberBindings(reorderedMembers.subject.credentialSet.memberBindings)
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(reorderedMembers))), /slot must be platform-admin/)
    const partialMembers = structuredClone(prepared.fixture)
    partialMembers.subject.credentialSet.memberBindings.pop()
    partialMembers.subject.credentialSet.memberCount = 10
    partialMembers.subject.credentialSet.memberBindingsDigest =
      hashGoldenCredentialMemberBindings(partialMembers.subject.credentialSet.memberBindings)
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(partialMembers))), /memberCount must be an integer between 11 and 11/)

    const insecure = structuredClone(prepared.fixture)
    insecure.subject.platform.apiOrigin = 'http://platform-api.golden.example.test'
    insecure.subject.sandbox.apiOrigin = insecure.subject.platform.apiOrigin
    insecure.subject.lsp.gateway.apiOrigin = insecure.subject.platform.apiOrigin
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(insecure))), /canonical HTTPS origin/)
    const originCollision = structuredClone(prepared.fixture)
    originCollision.subject.reference.webOrigin = originCollision.subject.platform.webOrigin
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(originCollision))), /public platform\/reference origins must all be distinct/)
    const internalEndpoint = structuredClone(prepared.fixture)
    internalEndpoint.subject.agent.modelGateway.identity = 'https://model.example.test/v1'
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(internalEndpoint))), /canonical SPIFFE identity/)

    const unknownFault = structuredClone(prepared.fixture)
    unknownFault.subject.faultAuthorities[0]!.operationKind = 'arbitrary-exec'
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(unknownFault))), /operationKind has an invalid format/)
    const partialFaultSet = structuredClone(prepared.fixture)
    partialFaultSet.subject.faultAuthorities.pop()
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(partialFaultSet))), /must contain 13\.\.13 values/)
    const reusedFault = structuredClone(prepared.fixture) as typeof prepared.fixture & { subject: { faultAuthorities: Array<Record<string, unknown>> } }
    reusedFault.subject.faultAuthorities[0]!.maxUses = 2
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(reusedFault))), /maxUses must be an integer between 1 and 1/)
    const consumedFault = structuredClone(prepared.fixture) as typeof prepared.fixture & { subject: { faultAuthorities: Array<Record<string, unknown>> } }
    consumedFault.subject.faultAuthorities[0]!.consumedAt = prepared.fixture.subject.issuedAt
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(consumedFault))), /consumedAt is not supported/)
    const dynamicFault = structuredClone(prepared.fixture)
    dynamicFault.subject.faultAuthorities[0]!.resourceSelector = `agent.${uuid(901)}`
    expectFailure(() => parseGoldenFixture(Buffer.from(canonicalJSON(dynamicFault))), /resourceSelector must be agent.runner/)

    const reverseAuthority = structuredClone(prepared.authority)
    reverseAuthority.subject.fixtureHash = digest('foreign-fixture')
    const reverseAuthorityPath = join(root, 'reverse-authority.json')
    const reverseAuthorityBytes = canonicalJSON(reverseAuthority)
    writeFileSync(reverseAuthorityPath, reverseAuthorityBytes)
    expectFailure(() => loadGoldenDocuments({
      ...prepared.environment,
      WORKSFLOW_GOLDEN_AUTHORITY_DIGEST: digest(reverseAuthorityBytes),
      WORKSFLOW_GOLDEN_AUTHORITY_FILE: reverseAuthorityPath,
    }, now), /fixture authorityHash drift/)

    chmodSync(prepared.paths['platform-api-a']!, 0o640)
    expectFailure(() => loadGoldenQualificationInputs(prepared.environment, now), /PLATFORM_API_A_TOKEN_FILE must have exact mode 0600/)
    chmodSync(prepared.paths['platform-api-a']!, 0o600)

    const linkedPath = join(root, 'linked-platform-api-a.json')
    symlinkSync(prepared.paths['platform-api-a']!, linkedPath)
    expectFailure(() => loadGoldenQualificationInputs({
      ...prepared.environment,
      WORKSFLOW_GOLDEN_PLATFORM_API_A_TOKEN_FILE: linkedPath,
    }, now), /single-link regular non-symlink/)

    const hardLink = join(root, 'hard-linked-platform-api-b.json')
    linkSync(prepared.paths['platform-api-b']!, hardLink)
    expectFailure(() => loadGoldenQualificationInputs(prepared.environment, now), /single-link regular non-symlink/)
    unlinkSync(hardLink)

    expectFailure(() => loadGoldenQualificationInputs({
      ...prepared.environment,
      WORKSFLOW_GOLDEN_PLATFORM_API_B_TOKEN_FILE: prepared.paths['platform-api-a'],
    }, now), /credential slots must use distinct files/)

    const swappedPath = join(root, 'copied-platform-api-a.json')
    writeCanonical(
      swappedPath,
      credentialDocument('platform-api-a', prepared.fixture, prepared.credentialHandles),
      0o600,
    )
    expectFailure(() => loadGoldenQualificationInputs({
      ...prepared.environment,
      WORKSFLOW_GOLDEN_PLATFORM_API_B_TOKEN_FILE: swappedPath,
    }, now), /actorId binding drift|slot must be platform-api-b/)

    const handleMismatch = credentialDocument('platform-api-b', prepared.fixture, prepared.credentialHandles)
    handleMismatch.credentialHandle = `${handleMismatch.credentialHandle}-drift`
    writeCanonical(prepared.paths['platform-api-b']!, handleMismatch, 0o600)
    expectFailure(() => loadGoldenQualificationInputs(prepared.environment, now), /credentialHandle binding drift/)
    writeCanonical(
      prepared.paths['platform-api-b']!,
      credentialDocument('platform-api-b', prepared.fixture, prepared.credentialHandles),
      0o600,
    )

    const foreignSet = credentialDocument('platform-owner', prepared.fixture, prepared.credentialHandles)
    foreignSet.credentialSetId = uuid(999)
    writeCanonical(prepared.paths['platform-owner']!, foreignSet, 0o600)
    expectFailure(() => loadGoldenQualificationInputs(prepared.environment, now), /credentialSetId binding drift/)
    writeCanonical(
      prepared.paths['platform-owner']!,
      credentialDocument('platform-owner', prepared.fixture, prepared.credentialHandles),
      0o600,
    )

    const reusedMaterial = credentialDocument('platform-api-b', prepared.fixture, prepared.credentialHandles)
    const apiAToken = credentialDocument('platform-api-a', prepared.fixture, prepared.credentialHandles)
    assert.ok('token' in reusedMaterial && 'token' in apiAToken)
    reusedMaterial.token = apiAToken.token
    writeCanonical(prepared.paths['platform-api-b']!, reusedMaterial, 0o600)
    expectFailure(() => loadGoldenQualificationInputs(prepared.environment, now), /must not reuse credential material/)
    writeCanonical(
      prepared.paths['platform-api-b']!,
      credentialDocument('platform-api-b', prepared.fixture, prepared.credentialHandles),
      0o600,
    )

    const wrongReferenceAudience = credentialDocument('reference-api-a', prepared.fixture, prepared.credentialHandles)
    wrongReferenceAudience.audience = prepared.fixture.subject.platform.apiOrigin
    writeCanonical(prepared.paths['reference-api-a']!, wrongReferenceAudience, 0o600)
    expectFailure(() => loadGoldenQualificationInputs(prepared.environment, now), /audience must be https:\/\/reference-api/)
    writeCanonical(
      prepared.paths['reference-api-a']!,
      credentialDocument('reference-api-a', prepared.fixture, prepared.credentialHandles),
      0o600,
    )

    const browserBearer = credentialDocument('reference-browser-a', prepared.fixture, prepared.credentialHandles)
    assert.ok('storageState' in browserBearer)
    ;(browserBearer.storageState.origins[0]!.localStorage as Array<{ name: string; value: string }>).push({
      name: 'authorization',
      value: `Bearer ${'x'.repeat(48)}`,
    })
    writeCanonical(prepared.paths['reference-browser-a']!, browserBearer, 0o600)
    expectFailure(() => loadGoldenQualificationInputs(prepared.environment, now), /localStorage must contain 0\.\.0 values/)
    writeCanonical(
      prepared.paths['reference-browser-a']!,
      credentialDocument('reference-browser-a', prepared.fixture, prepared.credentialHandles),
      0o600,
    )

    const bearerAsReferenceAPI = credentialDocument('platform-api-a', prepared.fixture, prepared.credentialHandles)
    bearerAsReferenceAPI.slot = 'reference-api-a'
    bearerAsReferenceAPI.actorId = prepared.fixture.subject.principals.find((entry) => entry.slot === 'reference-user-a')!.actorId
    bearerAsReferenceAPI.credentialHandle = prepared.credentialHandles['reference-api-a']!
    const bearerAsReferencePath = join(root, 'bearer-as-reference-api.json')
    writeCanonical(bearerAsReferencePath, bearerAsReferenceAPI, 0o600)
    expectFailure(() => loadGoldenQualificationInputs({
      ...prepared.environment,
      WORKSFLOW_GOLDEN_REFERENCE_API_A_STORAGE_STATE_FILE: bearerAsReferencePath,
    }, now), /credentialSetAudience is required|storageState is required/)

    const duplicateCredentialPath = join(root, 'duplicate-credential.json')
    writeFileSync(duplicateCredentialPath, `{"slot":"platform-api-a","slot":"platform-api-a"}`)
    chmodSync(duplicateCredentialPath, 0o600)
    expectFailure(() => loadGoldenQualificationInputs({
      ...prepared.environment,
      WORKSFLOW_GOLDEN_PLATFORM_API_A_TOKEN_FILE: duplicateCredentialPath,
    }, now), /duplicate JSON name/)

    console.log('golden authority v2 tests passed')
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
}

main()
