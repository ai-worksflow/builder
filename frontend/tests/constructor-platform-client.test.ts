import assert from 'node:assert/strict'
import { PlatformClient } from '../lib/platform/client'
import { evaluateBuildContractReadiness } from '../lib/platform/build-contract-gate'
import {
  normalizeApplicationBuildContract,
  normalizeFullStackTemplatePage,
  normalizeTemplateReleasePage,
  type CreateApplicationBuildContractInputDto,
} from '../lib/platform/constructor-contract'
import type { CsrfTokenStore, FetchLike } from '../lib/platform/http'

type Call = {
  readonly path: string
  readonly method: string
  readonly headers: Headers
  readonly body?: unknown
}

const releaseId = 'release/one'
const stackId = 'stack/one'
const manifestId = 'manifest/one'
const contractId = 'contract/one'
const contentHash = `sha256:${'a'.repeat(64)}`
const subjectHash = `sha256:${'b'.repeat(64)}`

function tokenStore(initial = 'csrf-constructor'): CsrfTokenStore {
  let token: string | undefined = initial
  return {
    get: () => token,
    set: (next) => { token = next },
    clear: () => { token = undefined },
  }
}

const releaseRegistration = {
  release: {
    id: releaseId,
    schemaVersion: 'template-release/v1',
    source: null,
    manifest: {
      templateId: 'react-web',
      displayName: null,
      services: null,
      toolchains: null,
      commands: { build: { argv: null } },
      ports: null,
      healthChecks: null,
      buildOutputs: null,
      extensionPaths: null,
      protectedPaths: null,
      environmentSchema: null,
      lockfiles: null,
    },
    evidenceRefs: null,
    signature: null,
    subjectHash,
    contentHash,
  },
  policy: {
    state: 'approved',
    reason: null,
  },
}

const stackRegistration = {
  template: {
    id: stackId,
    schemaVersion: 'full-stack-template/v1',
    templateId: 'react-fastapi',
    components: null,
    layout: null,
    contentHash,
  },
  components: null,
}

const buildContract = {
  id: contractId,
  projectId: 'project-1',
  buildManifestId: manifestId,
  status: 'blocked',
  version: 1,
  contentHash: 'sha256:stored-content-address',
  contractHash: 'canonical-contract-identity',
  contract: {
    schemaVersion: 'application-build-contract/v2',
    compiler: null,
    buildManifest: null,
    sourceRevisions: null,
    fullStackTemplate: null,
    templateReleaseRefs: null,
    routes: null,
    states: null,
    contractBindings: null,
    acceptanceCriteria: null,
    oracles: null,
    obligations: [{
      sourceRevision: null,
      oracleIds: null,
      dependsOn: null,
      status: null,
    }],
    waivers: null,
    gaps: [{
      code: 'required_contract_missing',
      message: null,
      obligationIds: null,
      blocking: true,
    }],
    conflicts: null,
    forbiddenClaims: null,
    status: null,
  },
  createdAt: null,
}

async function main() {
  const calls: Call[] = []
  const fetch: FetchLike = async (input, init) => {
    const url = new URL(input.toString())
    const method = init?.method ?? 'GET'
    calls.push({
      path: `${url.pathname}${url.search}`,
      method,
      headers: new Headers(init?.headers),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) as unknown : undefined,
    })

    if (url.pathname === '/v1/template-releases') {
      return Response.json({ items: [releaseRegistration] })
    }
    if (url.pathname.startsWith('/v1/template-releases/')) {
      return Response.json(releaseRegistration)
    }
    if (url.pathname === '/v1/full-stack-templates') {
      return Response.json({ items: [stackRegistration] })
    }
    if (url.pathname.startsWith('/v1/full-stack-templates/')) {
      return Response.json(stackRegistration)
    }
    return Response.json(buildContract, {
      status: method === 'POST' ? 201 : 200,
      headers: { etag: '"application-build-contract:contract:1"' },
    })
  }

  const platform = new PlatformClient({
    http: {
      baseUrl: 'https://platform.example.test',
      fetch,
      csrfTokenStore: tokenStore(),
      requestIdFactory: () => 'generated-constructor-key',
    },
  })
  const client = platform.constructorApi

  const releases = await client.listTemplateReleases(
    { templateId: 'react-web', states: ['approved', 'revoked'] },
    { limit: 25 },
  )
  const release = await client.getTemplateRelease(releaseId, { contentHash, subjectHash })
  const stacks = await client.listFullStackTemplates(
    { templateId: 'react-fastapi' },
    { limit: 10 },
  )
  const stack = await client.getFullStackTemplate(stackId, { contentHash })

  const untrustedInput = {
    fullStackTemplate: {
      id: stackId,
      contentHash,
      branch: 'main',
    },
    sources: [{ revisionId: 'client-claimed-source' }],
  } as unknown as CreateApplicationBuildContractInputDto
  const created = await client.createBuildContract(manifestId, untrustedInput)
  const forManifest = await client.getBuildContractForManifest(manifestId)
  const byId = await client.getBuildContract(contractId)

  assert.deepEqual(calls.map((call) => [call.method, call.path]), [
    ['GET', '/v1/template-releases?templateId=react-web&limit=25&state=approved&state=revoked'],
    ['GET', `/v1/template-releases/release%2Fone?contentHash=${encodeURIComponent(contentHash)}&subjectHash=${encodeURIComponent(subjectHash)}`],
    ['GET', '/v1/full-stack-templates?templateId=react-fastapi&limit=10'],
    ['GET', `/v1/full-stack-templates/stack%2Fone?contentHash=${encodeURIComponent(contentHash)}`],
    ['POST', '/v1/build-manifests/manifest%2Fone/build-contracts'],
    ['GET', '/v1/build-manifests/manifest%2Fone/build-contract'],
    ['GET', '/v1/application-build-contracts/contract%2Fone'],
  ])
  assert.deepEqual(calls[4]?.body, {
    fullStackTemplate: { id: stackId, contentHash },
  })
  assert.equal(calls[4]?.headers.get('x-csrf-token'), 'csrf-constructor')
  assert.equal(calls[4]?.headers.get('idempotency-key'), 'generated-constructor-key')

  assert.equal(releases.data.items[0].release.manifest.services.length, 0)
  assert.equal(releases.data.items[0].release.manifest.commands.build.argv.length, 0)
  assert.equal(releases.data.items[0].release.evidenceRefs.length, 0)
  assert.equal(releases.data.items[0].policy.reason.trim(), '')
  assert.equal(release.data.release.manifest.displayName.trim(), '')
  assert.equal(stacks.data.items[0].template.components.length, 0)
  assert.equal(stacks.data.items[0].components.length, 0)
  assert.equal(stack.data.template.layout.openapiPath.trim(), '')

  for (const result of [created, forManifest, byId]) {
    assert.equal(result.data.contentHash, 'sha256:stored-content-address')
    assert.equal(result.data.contractHash, 'canonical-contract-identity')
    assert.equal(result.data.status, 'blocked')
    assert.equal(result.data.contract.sourceRevisions.length, 0)
    assert.equal(result.data.contract.templateReleaseRefs.length, 0)
    assert.equal(result.data.contract.conflicts.length, 0)
    assert.equal(result.data.contract.obligations[0].oracleIds.length, 0)
    assert.equal(result.data.contract.obligations[0].status.trim(), '')
    assert.equal(result.data.contract.gaps[0].message.trim(), '')
    assert.equal(result.data.contract.gaps[0].obligationIds.length, 0)
    assert.equal(result.data.createdAt.trim(), '')
  }

  assert.deepEqual(normalizeTemplateReleasePage({ items: null }).items, [])
  assert.deepEqual(normalizeFullStackTemplatePage({ items: null }).items, [])
  const emptyContract = normalizeApplicationBuildContract(null)
  assert.equal(emptyContract.status, 'blocked')
  assert.deepEqual(emptyContract.contract.gaps, [])
  assert.deepEqual(emptyContract.contract.obligations, [])
  assert.equal(emptyContract.contract.deliverySliceId.trim(), '')

  const readyContract = normalizeApplicationBuildContract({
    ...buildContract,
    status: 'ready',
    contentHash: `sha256:${'c'.repeat(64)}`,
    contractHash: 'd'.repeat(64),
    mustCount: 3,
    mustReadyCount: 3,
    blockingCount: 0,
    conflictCount: 0,
    contract: {
      ...buildContract.contract,
      status: 'ready',
      buildManifest: { id: manifestId, contentHash: `sha256:${'e'.repeat(64)}` },
      obligations: [],
      gaps: [],
      conflicts: [],
    },
  })
  assert.deepEqual(evaluateBuildContractReadiness(readyContract, manifestId), {
    ready: true,
    reason: 'ready',
  })
  assert.equal(evaluateBuildContractReadiness(null).reason, 'missing')
  assert.equal(evaluateBuildContractReadiness({
    ...readyContract,
    contractHash: '',
  }).reason, 'identity_missing')
  assert.equal(evaluateBuildContractReadiness({
    ...readyContract,
    mustReadyCount: 2,
  }).reason, 'must_incomplete')
  assert.equal(evaluateBuildContractReadiness({
    ...readyContract,
    blockingCount: 1,
  }).reason, 'blocking_gaps')
  assert.equal(evaluateBuildContractReadiness({
    ...readyContract,
    conflictCount: 1,
  }).reason, 'blocking_conflicts')
  assert.equal(evaluateBuildContractReadiness({
    ...readyContract,
    status: 'blocked',
  }).reason, 'not_ready')
  assert.equal(
    evaluateBuildContractReadiness(readyContract, 'another-manifest').reason,
    'manifest_mismatch',
  )

  console.log('constructor platform client tests passed')
}

void main()
