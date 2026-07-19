import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import {
  closeSync,
  constants,
  fstatSync,
  lstatSync,
  openSync,
  readFileSync,
  realpathSync,
} from 'node:fs'
import { isAbsolute, resolve } from 'node:path'
import { TextDecoder } from 'node:util'

import {
  canonicalJSON,
  canonicalUUIDv4,
  compareCanonicalUTF8,
  parseStrictJSON,
  qualificationFail,
  requireBoolean,
  requireExactKeys,
  requireInteger,
  requireObject,
  requireSortedUniqueStrings,
  requireString,
  requireTimestamp,
  sha256Identity,
  stableId,
} from './qualification-core.mjs'

export const goldenAuthoritySchema = 'worksflow-golden-authority/v2'
export const goldenFixtureSchema = 'worksflow-golden-fixture/v2'
export const goldenCredentialMembersSchema = 'worksflow-credential-set-member-bindings/v1'
export const goldenFaultOperationSetSchema = 'worksflow-golden-fault-operation-set/v1'
export const goldenFaultOperationSetDigest = 'sha256:50add6d13b4b28587f5ceab1385d85e457cc35489a031ac9d2f3ff217bd1fa9d'
export const goldenReferenceDeploymentReceiptSchema = 'reference-deployment-runtime-receipt/v1'
export const goldenReferenceOperationSetSchema = 'reference-qualification-operation-set/v1'
export const goldenReferenceOperationKinds = Object.freeze([
  'migration-rerun',
  'rate-limit-observation',
  'reference-audit-observation',
  'retention-job',
  'run-execution-observation',
  'timeout-vector',
])
export const goldenReferenceOperationSetDigest = 'sha256:936f995189a3e6c89c740b6d693c4ba7e8b73b67db61bbf907f9c7fe8be0a2f8'
export const goldenAuthoritySubjectFields = Object.freeze([
  'authorityId',
  'expiresAt',
  'fixtureHash',
  'issuance',
  'issuedAt',
  'planDigest',
  'runId',
])
export const goldenFixtureSubjectFields = Object.freeze([
  'agent',
  'credentialSet',
  'expiresAt',
  'faultAuthorities',
  'fixtureId',
  'issuedAt',
  'lsp',
  'planDigest',
  'platform',
  'principals',
  'reference',
  'release',
  'runId',
  'sandbox',
  'sharedArtifacts',
])
export const goldenCredentialMemberFields = Object.freeze([
  'actorId',
  'credentialHandleHash',
  'kind',
  'slot',
])
export const goldenCredentialSetFields = Object.freeze([
  'audience',
  'credentialSetHandleHash',
  'expiresAt',
  'issuedAt',
  'issuer',
  'issuerAttestationDigest',
  'memberBindings',
  'memberBindingsDigest',
  'memberCount',
  'setId',
])

export const goldenBearerCredentialSchema = 'worksflow-golden-bearer-credential/v1'
export const goldenStorageCredentialSchema = 'worksflow-golden-storage-credential/v1'
export const goldenFaultPayloadType = 'application/vnd.worksflow.golden-fault-authority+json;version=1'
const maximumDocumentBytes = 512 << 10
const minimumRemainingLifetimeMilliseconds = 2 * 60_000
const maximumAuthorityLifetimeMilliseconds = 30 * 60_000
const maximumClockSkewMilliseconds = 30_000
const maximumSourceBytes = 8 * 1024 ** 3
const maximumSourceFiles = 100_000

const principalRoles = Object.freeze({
  'fault-operator': Object.freeze({ realm: 'control', role: 'fault-operator' }),
  'platform-admin': Object.freeze({ realm: 'platform', role: 'admin' }),
  'platform-owner': Object.freeze({ realm: 'platform', role: 'owner' }),
  'platform-user-a': Object.freeze({ realm: 'platform', role: 'user' }),
  'platform-user-b': Object.freeze({ realm: 'platform', role: 'user' }),
  'reference-user-a': Object.freeze({ realm: 'reference', role: 'user' }),
  'reference-user-b': Object.freeze({ realm: 'reference', role: 'user' }),
})

const credentialDefinitions = Object.freeze({
  'platform-admin': Object.freeze({
    environment: 'WORKSFLOW_GOLDEN_PLATFORM_ADMIN_TOKEN_FILE',
    kind: 'token',
    principalSlot: 'platform-admin',
  }),
  'platform-api-a': Object.freeze({
    environment: 'WORKSFLOW_GOLDEN_PLATFORM_API_A_TOKEN_FILE',
    kind: 'token',
    principalSlot: 'platform-user-a',
  }),
  'platform-api-b': Object.freeze({
    environment: 'WORKSFLOW_GOLDEN_PLATFORM_API_B_TOKEN_FILE',
    kind: 'token',
    principalSlot: 'platform-user-b',
  }),
  'platform-browser-a': Object.freeze({
    audience: 'platform-web',
    environment: 'WORKSFLOW_GOLDEN_PLATFORM_BROWSER_A_STORAGE_STATE_FILE',
    kind: 'storage-state',
    principalSlot: 'platform-user-a',
  }),
  'platform-browser-b': Object.freeze({
    audience: 'platform-web',
    environment: 'WORKSFLOW_GOLDEN_PLATFORM_BROWSER_B_STORAGE_STATE_FILE',
    kind: 'storage-state',
    principalSlot: 'platform-user-b',
  }),
  'platform-fault-operator': Object.freeze({
    environment: 'WORKSFLOW_GOLDEN_PLATFORM_FAULT_OPERATOR_TOKEN_FILE',
    kind: 'token',
    principalSlot: 'fault-operator',
  }),
  'platform-owner': Object.freeze({
    environment: 'WORKSFLOW_GOLDEN_PLATFORM_OWNER_TOKEN_FILE',
    kind: 'token',
    principalSlot: 'platform-owner',
  }),
  'reference-api-a': Object.freeze({
    audience: 'reference-api',
    environment: 'WORKSFLOW_GOLDEN_REFERENCE_API_A_STORAGE_STATE_FILE',
    kind: 'storage-state',
    principalSlot: 'reference-user-a',
  }),
  'reference-api-b': Object.freeze({
    audience: 'reference-api',
    environment: 'WORKSFLOW_GOLDEN_REFERENCE_API_B_STORAGE_STATE_FILE',
    kind: 'storage-state',
    principalSlot: 'reference-user-b',
  }),
  'reference-browser-a': Object.freeze({
    audience: 'reference-web',
    environment: 'WORKSFLOW_GOLDEN_REFERENCE_BROWSER_A_STORAGE_STATE_FILE',
    kind: 'storage-state',
    principalSlot: 'reference-user-a',
  }),
  'reference-browser-b': Object.freeze({
    audience: 'reference-web',
    environment: 'WORKSFLOW_GOLDEN_REFERENCE_BROWSER_B_STORAGE_STATE_FILE',
    kind: 'storage-state',
    principalSlot: 'reference-user-b',
  }),
})

export const goldenCredentialSlots = Object.freeze(
  Object.keys(credentialDefinitions).sort(compareCanonicalUTF8),
)

const runtimeImageRoles = Object.freeze([
  'agent-runner',
  'language-server',
  'qualification-runner',
  'qualification-verifier',
  'release-controller',
  'sandbox-runner',
].sort(compareCanonicalUTF8))

export const goldenFaultOperationKinds = Object.freeze([
  'agent-runner-crash',
  'agent-runner-timeout',
  'agent-security-canary',
  'controller-conflict',
  'controller-maintenance',
  'controller-not-found',
  'controller-timeout',
  'lsp-resource-pressure',
  'lsp-runtime-crash',
  'lsp-runtime-drift',
  'reference-gateway-outage',
  'reference-process-restart',
  'sandbox-dependency-crash',
].sort(compareCanonicalUTF8))

const faultResourceSelectors = Object.freeze({
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
})

function hashBytes(bytes) {
  return `sha256:${createHash('sha256').update(bytes).digest('hex')}`
}

export function hashGoldenCanonicalValue(value) {
  return hashBytes(Buffer.from(canonicalJSON(value)))
}

export function hashGoldenCredentialMemberBindings(members) {
  return hashGoldenCanonicalValue({
    members,
    schemaVersion: goldenCredentialMembersSchema,
  })
}

function parseCanonicalDocument(bytes, label, maximumBytes = maximumDocumentBytes) {
  if (!Buffer.isBuffer(bytes)) bytes = Buffer.from(bytes)
  if (bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) {
    qualificationFail(`${label} must use BOM-free UTF-8`)
  }
  const value = parseStrictJSON(bytes, label, maximumBytes)
  const canonical = canonicalJSON(value)
  const raw = bytes.toString('utf8')
  if (raw !== canonical && raw !== `${canonical}\n`) {
    qualificationFail(`${label} must be canonical JSON`)
  }
  return value
}

function requireDigest(value, label) {
  return requireString(value, label, { maximumBytes: 71, pattern: sha256Identity })
}

function requireUUID(value, label) {
  return requireString(value, label, { maximumBytes: 36, pattern: canonicalUUIDv4 })
}

function requireStable(value, label, maximumBytes = 128) {
  return requireString(value, label, { maximumBytes, pattern: stableId })
}

function requireReferenceStable(value, label, maximumBytes = 128) {
  return requireString(value, label, {
    maximumBytes,
    pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/,
  })
}

function requireArray(value, label, minimum, maximum) {
  if (!Array.isArray(value) || value.length < minimum || value.length > maximum) {
    qualificationFail(`${label} must contain ${minimum}..${maximum} values`)
  }
  return value
}

function requireVersion(value, label) {
  return requireString(value, label, {
    maximumBytes: 128,
    pattern: /^[A-Za-z0-9](?:[A-Za-z0-9._+-]*[A-Za-z0-9])?$/,
  })
}

function requireServiceIdentity(value, label) {
  requireString(value, label, { maximumBytes: 512 })
  let parsed
  try {
    parsed = new URL(value)
  } catch {
    qualificationFail(`${label} must be a canonical SPIFFE identity`)
  }
  const segments = parsed.pathname.split('/').slice(1)
  if (
    parsed.protocol !== 'spiffe:' || !parsed.hostname || parsed.hostname !== parsed.hostname.toLowerCase() ||
    parsed.port || parsed.username || parsed.password || parsed.search || parsed.hash || parsed.toString() !== value ||
    value.includes('%') || segments.length < 1 || segments.some((segment) => !/^[a-z0-9]+(?:[._-][a-z0-9]+)*$/.test(segment))
  ) {
    qualificationFail(`${label} must be a canonical SPIFFE identity without dot or empty path segments`)
  }
  return value
}

function parseIdentity(value, label) {
  requireExactKeys(value, ['contentHash', 'id'], [], label)
  requireDigest(value.contentHash, `${label}.contentHash`)
  requireUUID(value.id, `${label}.id`)
  return value
}

function requireCanonicalHTTPSOrigin(value, label) {
  requireString(value, label, { maximumBytes: 2048 })
  let parsed
  try {
    parsed = new URL(value)
  } catch {
    qualificationFail(`${label} must be a canonical HTTPS origin`)
  }
  if (
    parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash ||
    parsed.pathname !== '/' || parsed.origin !== value
  ) {
    qualificationFail(`${label} must be an exact canonical HTTPS origin without credentials, path, query, or fragment`)
  }
  return value
}

function parsePlatform(value, label) {
  requireExactKeys(value, [
    'apiOrigin',
    'apiSchemaDigest',
    'deploymentReceipt',
    'serverBuild',
    'webOrigin',
    'wssProtocolDigest',
  ], [], label)
  requireCanonicalHTTPSOrigin(value.apiOrigin, `${label}.apiOrigin`)
  requireDigest(value.apiSchemaDigest, `${label}.apiSchemaDigest`)
  parseIdentity(value.deploymentReceipt, `${label}.deploymentReceipt`)
  requireExactKeys(value.serverBuild, ['buildId', 'imageDigest', 'version'], [], `${label}.serverBuild`)
  requireStable(value.serverBuild.buildId, `${label}.serverBuild.buildId`, 256)
  requireDigest(value.serverBuild.imageDigest, `${label}.serverBuild.imageDigest`)
  requireVersion(value.serverBuild.version, `${label}.serverBuild.version`)
  requireCanonicalHTTPSOrigin(value.webOrigin, `${label}.webOrigin`)
  if (value.apiOrigin === value.webOrigin) qualificationFail(`${label} webOrigin and apiOrigin must be distinct`)
  requireDigest(value.wssProtocolDigest, `${label}.wssProtocolDigest`)
}

function parseCredentialSet(value, label, principals) {
  requireExactKeys(value, goldenCredentialSetFields, [], label)
  requireString(value.audience, `${label}.audience`, {
    maximumBytes: 256,
    pattern: /^(?:urn:[a-z0-9][a-z0-9:._/-]+|[a-z0-9]+(?:[._/-][a-z0-9]+)*)$/,
  })
  requireDigest(value.credentialSetHandleHash, `${label}.credentialSetHandleHash`)
  const expiresAt = requireTimestamp(value.expiresAt, `${label}.expiresAt`)
  const issuedAt = requireTimestamp(value.issuedAt, `${label}.issuedAt`)
  if (expiresAt % 1000 !== 0) qualificationFail(`${label}.expiresAt must use whole-second precision`)
  requireServiceIdentity(value.issuer, `${label}.issuer`)
  requireDigest(value.issuerAttestationDigest, `${label}.issuerAttestationDigest`)
  requireDigest(value.memberBindingsDigest, `${label}.memberBindingsDigest`)
  requireInteger(value.memberCount, `${label}.memberCount`, goldenCredentialSlots.length, goldenCredentialSlots.length)
  requireUUID(value.setId, `${label}.setId`)
  if (expiresAt - issuedAt < minimumRemainingLifetimeMilliseconds || expiresAt - issuedAt > maximumAuthorityLifetimeMilliseconds) {
    qualificationFail(`${label} lifetime must be between 2 and 30 minutes`)
  }
  requireArray(value.memberBindings, `${label}.memberBindings`, goldenCredentialSlots.length, goldenCredentialSlots.length)
  const handles = new Set()
  for (const [index, binding] of value.memberBindings.entries()) {
    const item = `${label}.memberBindings[${index}]`
    requireExactKeys(binding, goldenCredentialMemberFields, [], item)
    const expectedSlot = goldenCredentialSlots[index]
    requireString(binding.slot, `${item}.slot`, { exact: expectedSlot })
    const definition = credentialDefinitions[expectedSlot]
    requireUUID(binding.actorId, `${item}.actorId`)
    const expectedActor = principals.get(definition.principalSlot).actorId
    if (binding.actorId !== expectedActor) qualificationFail(`${item}.actorId does not match principal ${definition.principalSlot}`)
    requireString(binding.kind, `${item}.kind`, { exact: definition.kind })
    requireDigest(binding.credentialHandleHash, `${item}.credentialHandleHash`)
    if (handles.has(binding.credentialHandleHash)) qualificationFail(`${label} member credential handles must be unique`)
    handles.add(binding.credentialHandleHash)
  }
  if (handles.has(value.credentialSetHandleHash)) {
    qualificationFail(`${label}.credentialSetHandleHash must not reuse a member handle`)
  }
  const actualDigest = hashGoldenCredentialMemberBindings(value.memberBindings)
  if (actualDigest !== value.memberBindingsDigest) qualificationFail(`${label}.memberBindingsDigest drift`)
  return { issuedAt, expiresAt }
}

function parsePrincipals(value, label) {
  const slots = Object.keys(principalRoles).sort(compareCanonicalUTF8)
  requireArray(value, label, slots.length, slots.length)
  const actors = new Set()
  const principals = new Map()
  for (const [index, principal] of value.entries()) {
    const item = `${label}[${index}]`
    requireExactKeys(principal, ['actorId', 'projectId', 'realm', 'role', 'slot', 'tenantId'], [], item)
    const expectedSlot = slots[index]
    requireString(principal.slot, `${item}.slot`, { exact: expectedSlot })
    requireUUID(principal.actorId, `${item}.actorId`)
    requireUUID(principal.projectId, `${item}.projectId`)
    requireString(principal.realm, `${item}.realm`, { exact: principalRoles[expectedSlot].realm })
    requireString(principal.role, `${item}.role`, { exact: principalRoles[expectedSlot].role })
    requireUUID(principal.tenantId, `${item}.tenantId`)
    if (actors.has(principal.actorId)) qualificationFail(`${label} actors must be unique`)
    actors.add(principal.actorId)
    principals.set(expectedSlot, principal)
  }
  for (const realm of ['platform', 'reference']) {
    const left = principals.get(`${realm}-user-a`)
    const right = principals.get(`${realm}-user-b`)
    if (left.tenantId === right.tenantId || left.projectId === right.projectId) {
      qualificationFail(`${label} ${realm} user A/B must use distinct tenant and project boundaries`)
    }
  }
  return principals
}

function parseArtifactWithApproval(value, label) {
  requireExactKeys(value, ['approvalReceiptDigest', 'contentHash', 'id'], [], label)
  requireDigest(value.approvalReceiptDigest, `${label}.approvalReceiptDigest`)
  requireDigest(value.contentHash, `${label}.contentHash`)
  requireUUID(value.id, `${label}.id`)
}

function parseRuntimeImages(value, label) {
  requireArray(value, label, runtimeImageRoles.length, runtimeImageRoles.length)
  const byRole = new Map()
  for (const [index, image] of value.entries()) {
    const item = `${label}[${index}]`
    requireExactKeys(image, ['imageDigest', 'provenance', 'role', 'sbom', 'signature'], [], item)
    requireString(image.role, `${item}.role`, { exact: runtimeImageRoles[index] })
    requireDigest(image.imageDigest, `${item}.imageDigest`)
    parseIdentity(image.provenance, `${item}.provenance`)
    parseIdentity(image.sbom, `${item}.sbom`)
    parseIdentity(image.signature, `${item}.signature`)
    byRole.set(image.role, image)
  }
  return byRole
}

function parseSharedArtifacts(value, label) {
  requireExactKeys(value, [
    'buildContract',
    'buildManifest',
    'referenceContractBundle',
    'runtimeImages',
    'sourceRepository',
    'templateRelease',
    'workspaceRevision',
  ], [], label)
  parseIdentity(value.buildContract, `${label}.buildContract`)
  parseIdentity(value.buildManifest, `${label}.buildManifest`)
  parseIdentity(value.referenceContractBundle, `${label}.referenceContractBundle`)
  const runtimeImages = parseRuntimeImages(value.runtimeImages, `${label}.runtimeImages`)
  requireExactKeys(value.sourceRepository, ['commitOid', 'contentTreeDigest'], [], `${label}.sourceRepository`)
  requireString(value.sourceRepository.commitOid, `${label}.sourceRepository.commitOid`, {
    maximumBytes: 64,
    pattern: /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/,
  })
  requireDigest(value.sourceRepository.contentTreeDigest, `${label}.sourceRepository.contentTreeDigest`)
  parseArtifactWithApproval(value.templateRelease, `${label}.templateRelease`)
  requireExactKeys(value.workspaceRevision, ['canonicalQualityReceiptDigest', 'contentHash', 'id'], [], `${label}.workspaceRevision`)
  requireDigest(value.workspaceRevision.canonicalQualityReceiptDigest, `${label}.workspaceRevision.canonicalQualityReceiptDigest`)
  requireDigest(value.workspaceRevision.contentHash, `${label}.workspaceRevision.contentHash`)
  requireUUID(value.workspaceRevision.id, `${label}.workspaceRevision.id`)
  return runtimeImages
}

function parseSandbox(value, label) {
  requireExactKeys(value, ['apiOrigin', 'runner', 'runtimeProfileId', 'serviceProfiles'], [], label)
  requireCanonicalHTTPSOrigin(value.apiOrigin, `${label}.apiOrigin`)
  requireExactKeys(value.runner, ['identity', 'imageDigest', 'profileId'], [], `${label}.runner`)
  requireServiceIdentity(value.runner.identity, `${label}.runner.identity`)
  requireDigest(value.runner.imageDigest, `${label}.runner.imageDigest`)
  requireStable(value.runner.profileId, `${label}.runner.profileId`)
  requireStable(value.runtimeProfileId, `${label}.runtimeProfileId`)
  requireArray(value.serviceProfiles, `${label}.serviceProfiles`, 1, 32)
  let prior = ''
  for (const [index, profile] of value.serviceProfiles.entries()) {
    const item = `${label}.serviceProfiles[${index}]`
    requireExactKeys(profile, ['id', 'imageDigest', 'protocol', 'service'], [], item)
    requireStable(profile.id, `${item}.id`)
    if (index > 0 && compareCanonicalUTF8(profile.id, prior) <= 0) {
      qualificationFail(`${label}.serviceProfiles must be strictly sorted and unique by id`)
    }
    prior = profile.id
    requireDigest(profile.imageDigest, `${item}.imageDigest`)
    requireString(profile.protocol, `${item}.protocol`, { pattern: /^(?:http|websocket)$/ })
    requireStable(profile.service, `${item}.service`)
  }
}

function parseAgent(value, label) {
  requireExactKeys(value, ['modelGateway', 'runner'], [], label)
  requireExactKeys(value.modelGateway, [
    'attestationDigest',
    'identity',
    'modelId',
    'modelRevision',
    'profileId',
    'providerId',
  ], [], `${label}.modelGateway`)
  requireDigest(value.modelGateway.attestationDigest, `${label}.modelGateway.attestationDigest`)
  requireServiceIdentity(value.modelGateway.identity, `${label}.modelGateway.identity`)
  requireStable(value.modelGateway.modelId, `${label}.modelGateway.modelId`, 256)
  requireStable(value.modelGateway.modelRevision, `${label}.modelGateway.modelRevision`, 256)
  requireStable(value.modelGateway.profileId, `${label}.modelGateway.profileId`)
  requireStable(value.modelGateway.providerId, `${label}.modelGateway.providerId`)
  requireExactKeys(value.runner, ['identity', 'imageDigest', 'profileId'], [], `${label}.runner`)
  requireServiceIdentity(value.runner.identity, `${label}.runner.identity`)
  requireDigest(value.runner.imageDigest, `${label}.runner.imageDigest`)
  requireStable(value.runner.profileId, `${label}.runner.profileId`)
}

function parseRelease(value, label) {
  requireExactKeys(value, ['controller'], [], label)
  requireExactKeys(value.controller, ['identity', 'imageDigest', 'profileId', 'protocol', 'trustKeyDigest'], [], `${label}.controller`)
  requireServiceIdentity(value.controller.identity, `${label}.controller.identity`)
  requireDigest(value.controller.imageDigest, `${label}.controller.imageDigest`)
  requireStable(value.controller.profileId, `${label}.controller.profileId`)
  requireStable(value.controller.protocol, `${label}.controller.protocol`, 128)
  requireDigest(value.controller.trustKeyDigest, `${label}.controller.trustKeyDigest`)
}

function parseLSP(value, label) {
  requireExactKeys(value, ['gateway', 'runtime'], [], label)
  requireExactKeys(value.gateway, ['apiOrigin', 'path', 'ticketProtocolDigest', 'wssProtocolDigest'], [], `${label}.gateway`)
  requireCanonicalHTTPSOrigin(value.gateway.apiOrigin, `${label}.gateway.apiOrigin`)
  requireString(value.gateway.path, `${label}.gateway.path`, { exact: '/v1/sandbox-lsp' })
  requireDigest(value.gateway.ticketProtocolDigest, `${label}.gateway.ticketProtocolDigest`)
  requireDigest(value.gateway.wssProtocolDigest, `${label}.gateway.wssProtocolDigest`)
  requireExactKeys(value.runtime, ['capabilityDigest', 'identity', 'imageDigest', 'languages', 'profileId'], [], `${label}.runtime`)
  requireDigest(value.runtime.capabilityDigest, `${label}.runtime.capabilityDigest`)
  requireServiceIdentity(value.runtime.identity, `${label}.runtime.identity`)
  requireDigest(value.runtime.imageDigest, `${label}.runtime.imageDigest`)
  requireSortedUniqueStrings(value.runtime.languages, `${label}.runtime.languages`, { minimum: 1, maximum: 32 })
  requireStable(value.runtime.profileId, `${label}.runtime.profileId`)
}

function parseReference(value, label) {
  requireExactKeys(value, [
    'apiImageDigest',
    'apiOrigin',
    'applicationId',
    'commands',
    'contractBundle',
    'deploymentReceipt',
    'gateway',
    'migration',
    'qualificationOperationSet',
    'rateLimitPolicy',
    'retentionPolicy',
    'runEventSchemaDigest',
    'webImageDigest',
    'webOrigin',
  ], [], label)
  requireDigest(value.apiImageDigest, `${label}.apiImageDigest`)
  requireCanonicalHTTPSOrigin(value.apiOrigin, `${label}.apiOrigin`)
  requireUUID(value.applicationId, `${label}.applicationId`)
  requireExactKeys(value.commands, ['api', 'migration', 'retention', 'web'], [], `${label}.commands`)
  const commandIdentities = new Set()
  for (const name of ['api', 'migration', 'retention', 'web']) {
    const command = value.commands[name]
    const item = `${label}.commands.${name}`
    requireExactKeys(command, ['argv', 'identity', 'workingDirectory'], [], item)
    requireReferenceStable(command.identity, `${item}.identity`)
    if (commandIdentities.has(command.identity)) qualificationFail(`${label}.commands identities must be role-distinct`)
    commandIdentities.add(command.identity)
    requireArray(command.argv, `${item}.argv`, 1, 16)
    for (const [index, argument] of command.argv.entries()) {
      requireString(argument, `${item}.argv[${index}]`, {
        maximumBytes: 256,
        pattern: /^[A-Za-z0-9./_+-][A-Za-z0-9._+/:=@%-]*$/,
      })
      if (argument.includes('//') || /(?:^|\/)\.\.(?:\/|$)/.test(argument)) {
        qualificationFail(`${item}.argv[${index}] must not contain ambiguous path traversal or empty segments`)
      }
    }
    const executable = command.argv[0].split('/').at(-1)
    if (/^(?:ba|da|z)?sh$|^(?:cmd|env|fish|powershell|pwsh|sudo|xargs)(?:\.exe)?$/i.test(executable)) {
      qualificationFail(`${item}.argv must execute the approved binary directly, not a shell or command launcher`)
    }
    requireString(command.workingDirectory, `${item}.workingDirectory`, {
      maximumBytes: 512,
      pattern: /^\/(?:[a-z0-9][a-z0-9._-]*)(?:\/[a-z0-9][a-z0-9._-]*)*$/,
    })
  }
  parseIdentity(value.contractBundle, `${label}.contractBundle`)
  requireExactKeys(value.deploymentReceipt, ['contentHash', 'id', 'schemaVersion'], [], `${label}.deploymentReceipt`)
  requireDigest(value.deploymentReceipt.contentHash, `${label}.deploymentReceipt.contentHash`)
  requireUUID(value.deploymentReceipt.id, `${label}.deploymentReceipt.id`)
  requireString(value.deploymentReceipt.schemaVersion, `${label}.deploymentReceipt.schemaVersion`, {
    exact: goldenReferenceDeploymentReceiptSchema,
  })
  requireExactKeys(value.gateway, [
    'attestationDigest',
    'capabilityDigest',
    'identity',
    'modelProfile',
    'providerPolicy',
    'routeId',
    'secretInjectionReceipt',
  ], [], `${label}.gateway`)
  requireDigest(value.gateway.attestationDigest, `${label}.gateway.attestationDigest`)
  requireDigest(value.gateway.capabilityDigest, `${label}.gateway.capabilityDigest`)
  requireServiceIdentity(value.gateway.identity, `${label}.gateway.identity`)
  requireReferenceStable(value.gateway.routeId, `${label}.gateway.routeId`)
  parseIdentity(value.gateway.secretInjectionReceipt, `${label}.gateway.secretInjectionReceipt`)
  requireExactKeys(value.gateway.providerPolicy, [
    'contentHash',
    'fallbackAllowed',
    'id',
    'profilePinned',
  ], [], `${label}.gateway.providerPolicy`)
  requireDigest(value.gateway.providerPolicy.contentHash, `${label}.gateway.providerPolicy.contentHash`)
  requireString(value.gateway.providerPolicy.id, `${label}.gateway.providerPolicy.id`, {
    exact: 'reference-project-default',
  })
  requireBoolean(value.gateway.providerPolicy.profilePinned, `${label}.gateway.providerPolicy.profilePinned`)
  requireBoolean(value.gateway.providerPolicy.fallbackAllowed, `${label}.gateway.providerPolicy.fallbackAllowed`)
  if (!value.gateway.providerPolicy.profilePinned || value.gateway.providerPolicy.fallbackAllowed) {
    qualificationFail(`${label}.gateway.providerPolicy must pin the profile and forbid fallback`)
  }
  requireExactKeys(value.gateway.modelProfile, [
    'contentHash',
    'id',
    'maxAttempts',
    'modelId',
    'modelRevision',
    'providerId',
    'timeoutMilliseconds',
  ], [], `${label}.gateway.modelProfile`)
  requireDigest(value.gateway.modelProfile.contentHash, `${label}.gateway.modelProfile.contentHash`)
  requireReferenceStable(value.gateway.modelProfile.id, `${label}.gateway.modelProfile.id`)
  requireInteger(value.gateway.modelProfile.maxAttempts, `${label}.gateway.modelProfile.maxAttempts`, 3, 3)
  requireReferenceStable(value.gateway.modelProfile.modelId, `${label}.gateway.modelProfile.modelId`)
  requireReferenceStable(value.gateway.modelProfile.modelRevision, `${label}.gateway.modelProfile.modelRevision`)
  requireReferenceStable(value.gateway.modelProfile.providerId, `${label}.gateway.modelProfile.providerId`)
  requireInteger(
    value.gateway.modelProfile.timeoutMilliseconds,
    `${label}.gateway.modelProfile.timeoutMilliseconds`,
    120_000,
    120_000,
  )
  requireExactKeys(value.migration, ['contentHash', 'identity'], [], `${label}.migration`)
  requireDigest(value.migration.contentHash, `${label}.migration.contentHash`)
  requireReferenceStable(value.migration.identity, `${label}.migration.identity`)
  requireExactKeys(value.qualificationOperationSet, [
    'contentHash',
    'operations',
    'schemaVersion',
  ], [], `${label}.qualificationOperationSet`)
  requireString(
    value.qualificationOperationSet.schemaVersion,
    `${label}.qualificationOperationSet.schemaVersion`,
    { exact: goldenReferenceOperationSetSchema },
  )
  requireSortedUniqueStrings(
    value.qualificationOperationSet.operations,
    `${label}.qualificationOperationSet.operations`,
    { minimum: goldenReferenceOperationKinds.length, maximum: goldenReferenceOperationKinds.length },
  )
  if (value.qualificationOperationSet.operations.some((operation, index) => operation !== goldenReferenceOperationKinds[index])) {
    qualificationFail(`${label}.qualificationOperationSet operations do not match the closed v1 set`)
  }
  requireDigest(value.qualificationOperationSet.contentHash, `${label}.qualificationOperationSet.contentHash`)
  const operationSetDigest = hashGoldenCanonicalValue({
    operations: goldenReferenceOperationKinds,
    schemaVersion: goldenReferenceOperationSetSchema,
  })
  if (operationSetDigest !== goldenReferenceOperationSetDigest ||
      value.qualificationOperationSet.contentHash !== goldenReferenceOperationSetDigest) {
    qualificationFail(`${label}.qualificationOperationSet canonical content hash drift`)
  }
  requireExactKeys(value.rateLimitPolicy, [
    'burst',
    'contentHash',
    'id',
    'requests',
    'scopes',
    'windowSeconds',
  ], [], `${label}.rateLimitPolicy`)
  requireInteger(value.rateLimitPolicy.burst, `${label}.rateLimitPolicy.burst`, 10, 10)
  requireDigest(value.rateLimitPolicy.contentHash, `${label}.rateLimitPolicy.contentHash`)
  requireString(value.rateLimitPolicy.id, `${label}.rateLimitPolicy.id`, { exact: 'reference-rate-limit-v1' })
  requireInteger(value.rateLimitPolicy.requests, `${label}.rateLimitPolicy.requests`, 60, 60)
  requireSortedUniqueStrings(value.rateLimitPolicy.scopes, `${label}.rateLimitPolicy.scopes`, { minimum: 2, maximum: 2 })
  if (value.rateLimitPolicy.scopes[0] !== 'project' || value.rateLimitPolicy.scopes[1] !== 'tenant-actor') {
    qualificationFail(`${label}.rateLimitPolicy.scopes must be the exact project and tenant-actor boundaries`)
  }
  requireInteger(value.rateLimitPolicy.windowSeconds, `${label}.rateLimitPolicy.windowSeconds`, 60, 60)
  requireExactKeys(value.retentionPolicy, [
    'auditDays',
    'contentHash',
    'eventDays',
    'id',
    'messageDays',
    'redactionRequired',
    'runDays',
  ], [], `${label}.retentionPolicy`)
  requireInteger(value.retentionPolicy.auditDays, `${label}.retentionPolicy.auditDays`, 90, 90)
  requireDigest(value.retentionPolicy.contentHash, `${label}.retentionPolicy.contentHash`)
  requireInteger(value.retentionPolicy.eventDays, `${label}.retentionPolicy.eventDays`, 30, 30)
  requireUUID(value.retentionPolicy.id, `${label}.retentionPolicy.id`)
  requireInteger(value.retentionPolicy.messageDays, `${label}.retentionPolicy.messageDays`, 30, 30)
  requireBoolean(value.retentionPolicy.redactionRequired, `${label}.retentionPolicy.redactionRequired`)
  if (!value.retentionPolicy.redactionRequired) qualificationFail(`${label}.retentionPolicy must require redaction`)
  requireInteger(value.retentionPolicy.runDays, `${label}.retentionPolicy.runDays`, 90, 90)
  requireDigest(value.runEventSchemaDigest, `${label}.runEventSchemaDigest`)
  requireDigest(value.webImageDigest, `${label}.webImageDigest`)
  requireCanonicalHTTPSOrigin(value.webOrigin, `${label}.webOrigin`)
  if (value.apiOrigin === value.webOrigin) qualificationFail(`${label} webOrigin and apiOrigin must be distinct`)

  const commitments = [
    value.deploymentReceipt.contentHash,
    value.gateway.attestationDigest,
    value.gateway.capabilityDigest,
    value.gateway.modelProfile.contentHash,
    value.gateway.providerPolicy.contentHash,
    value.gateway.secretInjectionReceipt.contentHash,
    value.migration.contentHash,
    value.qualificationOperationSet.contentHash,
    value.rateLimitPolicy.contentHash,
    value.retentionPolicy.contentHash,
    value.runEventSchemaDigest,
  ]
  if (new Set(commitments).size !== commitments.length) {
    qualificationFail(`${label} receipt, policy, capability, migration, schema, and operation-set commitments must be distinct`)
  }
  const artifactIds = [
    value.contractBundle.id,
    value.deploymentReceipt.id,
    value.gateway.secretInjectionReceipt.id,
    value.retentionPolicy.id,
  ]
  if (new Set(artifactIds).size !== artifactIds.length) {
    qualificationFail(`${label} contract, deployment, secret-injection, and retention artifact IDs must be distinct`)
  }
}

function parseFaultAuthorities(value, label) {
  requireArray(value, label, goldenFaultOperationKinds.length, goldenFaultOperationKinds.length)
  if (hashGoldenCanonicalValue({
    operations: goldenFaultOperationKinds,
    schemaVersion: goldenFaultOperationSetSchema,
  }) !== goldenFaultOperationSetDigest) {
    qualificationFail('compiled Golden fault-operation set does not match its canonical commitment')
  }
  let prior = ''
  const operations = new Set()
  const artifactIDs = new Set()
  const envelopeDigests = new Set()
  const payloadDigests = new Set()
  for (const [index, authority] of value.entries()) {
    const item = `${label}[${index}]`
    requireExactKeys(authority, [
      'authorityId',
      'dsse',
      'expectedFenceDigest',
      'maxUses',
      'operationKind',
      'resourceSelector',
    ], [], item)
    requireUUID(authority.authorityId, `${item}.authorityId`)
    if (index > 0 && compareCanonicalUTF8(authority.authorityId, prior) <= 0) {
      qualificationFail(`${label} must be strictly sorted and unique by authorityId`)
    }
    prior = authority.authorityId
    requireExactKeys(authority.dsse, ['artifactId', 'envelopeDigest', 'payloadDigest', 'payloadType'], [], `${item}.dsse`)
    requireUUID(authority.dsse.artifactId, `${item}.dsse.artifactId`)
    requireDigest(authority.dsse.envelopeDigest, `${item}.dsse.envelopeDigest`)
    requireDigest(authority.dsse.payloadDigest, `${item}.dsse.payloadDigest`)
    if (authority.dsse.envelopeDigest === authority.dsse.payloadDigest) {
      qualificationFail(`${item}.dsse envelope and payload digests must be distinct`)
    }
    requireString(authority.dsse.payloadType, `${item}.dsse.payloadType`, { exact: goldenFaultPayloadType })
    if (artifactIDs.has(authority.dsse.artifactId) || envelopeDigests.has(authority.dsse.envelopeDigest) ||
        payloadDigests.has(authority.dsse.payloadDigest)) {
      qualificationFail(`${label} entries must use distinct DSSE artifact, envelope, and payload references`)
    }
    artifactIDs.add(authority.dsse.artifactId)
    envelopeDigests.add(authority.dsse.envelopeDigest)
    payloadDigests.add(authority.dsse.payloadDigest)
    requireDigest(authority.expectedFenceDigest, `${item}.expectedFenceDigest`)
    requireInteger(authority.maxUses, `${item}.maxUses`, 1, 1)
    requireString(authority.operationKind, `${item}.operationKind`, {
      pattern: new RegExp(`^(?:${goldenFaultOperationKinds.join('|')})$`),
    })
    if (operations.has(authority.operationKind)) qualificationFail(`${label} operationKind values must be unique`)
    operations.add(authority.operationKind)
    requireString(authority.resourceSelector, `${item}.resourceSelector`, {
      exact: faultResourceSelectors[authority.operationKind],
    })
  }
}

export function parseGoldenAuthority(bytes) {
  const value = parseCanonicalDocument(bytes, 'Golden authority')
  requireExactKeys(value, ['schemaVersion', 'subject'], [], 'Golden authority')
  requireString(value.schemaVersion, 'Golden authority.schemaVersion', { exact: goldenAuthoritySchema })
  const subject = value.subject
  requireExactKeys(subject, goldenAuthoritySubjectFields, [], 'Golden authority.subject')
  requireUUID(subject.authorityId, 'Golden authority.subject.authorityId')
  requireString(subject.issuance, 'Golden authority.subject.issuance', { exact: 'root-issued-hash-bound' })
  const issuedAt = requireTimestamp(subject.issuedAt, 'Golden authority.subject.issuedAt')
  const expiresAt = requireTimestamp(subject.expiresAt, 'Golden authority.subject.expiresAt')
  if (expiresAt - issuedAt < minimumRemainingLifetimeMilliseconds || expiresAt - issuedAt > maximumAuthorityLifetimeMilliseconds) {
    qualificationFail('Golden authority lifetime must be between 2 and 30 minutes')
  }
  requireUUID(subject.runId, 'Golden authority.subject.runId')
  requireDigest(subject.planDigest, 'Golden authority.subject.planDigest')
  requireDigest(subject.fixtureHash, 'Golden authority.subject.fixtureHash')
  return deepFreeze({
    document: value,
    documentDigest: hashBytes(Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes)),
    authorityHash: hashGoldenCanonicalValue(subject),
    issuedAt,
    expiresAt,
  })
}

export function parseGoldenFixture(bytes) {
  const value = parseCanonicalDocument(bytes, 'Golden fixture')
  requireExactKeys(value, ['authorityHash', 'schemaVersion', 'subject'], [], 'Golden fixture')
  requireString(value.schemaVersion, 'Golden fixture.schemaVersion', { exact: goldenFixtureSchema })
  requireDigest(value.authorityHash, 'Golden fixture.authorityHash')
  const subject = value.subject
  requireExactKeys(subject, goldenFixtureSubjectFields, [], 'Golden fixture.subject')
  parseAgent(subject.agent, 'Golden fixture.subject.agent')
  const expiresAt = requireTimestamp(subject.expiresAt, 'Golden fixture.subject.expiresAt')
  const issuedAt = requireTimestamp(subject.issuedAt, 'Golden fixture.subject.issuedAt')
  if (expiresAt - issuedAt < minimumRemainingLifetimeMilliseconds || expiresAt - issuedAt > maximumAuthorityLifetimeMilliseconds) {
    qualificationFail('Golden fixture lifetime must be between 2 and 30 minutes')
  }
  parseFaultAuthorities(subject.faultAuthorities, 'Golden fixture.subject.faultAuthorities')
  requireUUID(subject.fixtureId, 'Golden fixture.subject.fixtureId')
  parseLSP(subject.lsp, 'Golden fixture.subject.lsp')
  requireDigest(subject.planDigest, 'Golden fixture.subject.planDigest')
  parsePlatform(subject.platform, 'Golden fixture.subject.platform')
  const principals = parsePrincipals(subject.principals, 'Golden fixture.subject.principals')
  const credentialTimes = parseCredentialSet(subject.credentialSet, 'Golden fixture.subject.credentialSet', principals)
  parseReference(subject.reference, 'Golden fixture.subject.reference')
  parseRelease(subject.release, 'Golden fixture.subject.release')
  requireUUID(subject.runId, 'Golden fixture.subject.runId')
  parseSandbox(subject.sandbox, 'Golden fixture.subject.sandbox')
  const runtimeImages = parseSharedArtifacts(subject.sharedArtifacts, 'Golden fixture.subject.sharedArtifacts')

  const publicOrigins = [
    subject.platform.apiOrigin,
    subject.platform.webOrigin,
    subject.reference.apiOrigin,
    subject.reference.webOrigin,
  ]
  if (new Set(publicOrigins).size !== publicOrigins.length) {
    qualificationFail('Golden fixture public platform/reference origins must all be distinct')
  }
  const internalIdentities = [
    subject.credentialSet.issuer,
    subject.agent.modelGateway.identity,
    subject.agent.runner.identity,
    subject.reference.gateway.identity,
    subject.sandbox.runner.identity,
    subject.release.controller.identity,
    subject.lsp.runtime.identity,
  ]
  if (new Set(internalIdentities).size !== internalIdentities.length) {
    qualificationFail('Golden fixture credential issuer and internal runtime identities must be role-distinct')
  }
  if (subject.reference.gateway.modelProfile.id === subject.agent.modelGateway.profileId ||
      subject.reference.gateway.modelProfile.providerId === subject.agent.modelGateway.providerId) {
    qualificationFail('Golden fixture Reference ModelProfile and provider must be independent from Agent Model Gateway')
  }
  const referenceCommandIdentities = Object.values(subject.reference.commands).map((command) => command.identity)
  if (referenceCommandIdentities.includes(subject.reference.migration.identity) ||
      referenceCommandIdentities.includes(subject.reference.gateway.routeId)) {
    qualificationFail('Golden fixture Reference command, migration, and route identities must not be reused')
  }
  if (subject.sandbox.apiOrigin !== subject.platform.apiOrigin || subject.lsp.gateway.apiOrigin !== subject.platform.apiOrigin) {
    qualificationFail('Golden fixture Sandbox and LSP gateway must bind the exact platform apiOrigin')
  }
  if (credentialTimes.issuedAt > issuedAt || credentialTimes.expiresAt < expiresAt) {
    qualificationFail('Golden fixture credentialSet lifetime must cover the complete fixture lifetime')
  }
  if (subject.reference.contractBundle.id !== subject.sharedArtifacts.referenceContractBundle.id ||
      subject.reference.contractBundle.contentHash !== subject.sharedArtifacts.referenceContractBundle.contentHash) {
    qualificationFail('Golden fixture reference contract bundle binding drift')
  }
  const imageBindings = [
    ['agent-runner', subject.agent.runner.imageDigest],
    ['language-server', subject.lsp.runtime.imageDigest],
    ['release-controller', subject.release.controller.imageDigest],
    ['sandbox-runner', subject.sandbox.runner.imageDigest],
  ]
  for (const [role, digest] of imageBindings) {
    if (runtimeImages.get(role).imageDigest !== digest) {
      qualificationFail(`Golden fixture ${role} approved image binding drift`)
    }
  }
  return deepFreeze({
    document: value,
    documentDigest: hashBytes(Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes)),
    fixtureHash: hashGoldenCanonicalValue(subject),
    credentialIssuedAt: credentialTimes.issuedAt,
    credentialExpiresAt: credentialTimes.expiresAt,
    principals: Object.fromEntries(principals),
  })
}

function requiredEnvironment(environment, name) {
  const value = environment[name]?.trim() ?? ''
  if (!value || /[\r\n\0]/.test(value)) qualificationFail(`${name} is required`)
  return value
}

function openProtectedFile(path, label, maximumBytes, exactMode) {
  if (!isAbsolute(path) || resolve(path) !== path) qualificationFail(`${label} must be an absolute normalized path`)
  let linkStat
  try {
    linkStat = lstatSync(path)
  } catch {
    qualificationFail(`${label} does not exist`)
  }
  if (linkStat.isSymbolicLink() || !linkStat.isFile() || linkStat.nlink !== 1) {
    qualificationFail(`${label} must be a single-link regular non-symlink file`)
  }
  if (realpathSync(path) !== path) qualificationFail(`${label} must not traverse symlinks`)
  const currentUID = typeof process.getuid === 'function' ? process.getuid() : linkStat.uid
  if (linkStat.uid !== 0 && linkStat.uid !== currentUID) qualificationFail(`${label} must be owned by root or the current user`)
  if (exactMode !== undefined) {
    if ((linkStat.mode & 0o777) !== exactMode) qualificationFail(`${label} must have exact mode ${exactMode.toString(8).padStart(4, '0')}`)
  } else if ((linkStat.mode & 0o022) !== 0) {
    qualificationFail(`${label} must not be group- or world-writable`)
  } else if ((linkStat.mode & 0o111) !== 0) {
    qualificationFail(`${label} must not be executable`)
  }
  let descriptor
  try {
    descriptor = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0))
    const descriptorStat = fstatSync(descriptor)
    if (
      descriptorStat.dev !== linkStat.dev || descriptorStat.ino !== linkStat.ino ||
      descriptorStat.uid !== linkStat.uid || !descriptorStat.isFile() || descriptorStat.nlink !== 1 ||
      descriptorStat.size < 1 || descriptorStat.size > maximumBytes ||
      (descriptorStat.mode & 0o777) !== (linkStat.mode & 0o777)
    ) {
      qualificationFail(`${label} changed during validation or has an invalid size`)
    }
    const beforeRead = fstatSync(descriptor, { bigint: true })
    const bytes = readFileSync(descriptor)
    const afterRead = fstatSync(descriptor, { bigint: true })
    for (const field of ['dev', 'ino', 'uid', 'mode', 'nlink', 'size', 'mtimeNs', 'ctimeNs']) {
      if (beforeRead[field] !== afterRead[field]) qualificationFail(`${label} changed while it was read`)
    }
    return Object.freeze({
      bytes,
      dev: descriptorStat.dev,
      ino: descriptorStat.ino,
    })
  } finally {
    if (descriptor !== undefined) closeSync(descriptor)
  }
}

function runTrustedGit(repositoryRoot, args, label) {
  const result = spawnSync('/usr/bin/git', [
    '-c', 'core.fsmonitor=false',
    '-c', 'core.untrackedCache=false',
    '-c', 'core.hooksPath=/dev/null',
    '-C', repositoryRoot,
    ...args,
  ], {
    encoding: 'buffer',
    env: {
      GIT_CONFIG_GLOBAL: '/dev/null',
      GIT_CONFIG_NOSYSTEM: '1',
      HOME: '/nonexistent',
      LANG: 'C',
      LC_ALL: 'C',
      PATH: '/usr/bin:/bin',
    },
    maxBuffer: 64 << 20,
  })
  if (result.error || result.status !== 0 || !Buffer.isBuffer(result.stdout)) {
    qualificationFail(`${label} could not be read with the trusted Git executable`)
  }
  return result.stdout
}

function requireSourcePath(value, label) {
  requireString(value, label, { maximumBytes: 4096 })
  if (
    value.startsWith('/') || value.includes('\\') ||
    value.split('/').some((part) => part === '' || part === '.' || part === '..' || part === '.git')
  ) {
    qualificationFail(`${label} is not a canonical tracked source path`)
  }
  return value
}

function readStableSourceFile(path, expectedExecutable) {
  let descriptor
  try {
    descriptor = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0))
    const before = fstatSync(descriptor, { bigint: true })
    if (!before.isFile() || before.nlink !== 1n || before.size < 0n || before.size > BigInt(maximumSourceBytes)) {
      qualificationFail(`tracked source ${path} is not a bounded single-link regular file`)
    }
    if (((before.mode & 0o111n) !== 0n) !== expectedExecutable) {
      qualificationFail(`tracked source ${path} executable mode drift`)
    }
    const bytes = readFileSync(descriptor)
    const after = fstatSync(descriptor, { bigint: true })
    for (const field of ['dev', 'ino', 'uid', 'mode', 'nlink', 'size', 'mtimeNs', 'ctimeNs']) {
      if (before[field] !== after[field]) qualificationFail(`tracked source ${path} changed while it was read`)
    }
    if (BigInt(bytes.length) !== before.size) qualificationFail(`tracked source ${path} size drift`)
    return bytes
  } finally {
    if (descriptor !== undefined) closeSync(descriptor)
  }
}

export function computeGoldenSourceContentTreeDigest(entries) {
  if (!Array.isArray(entries) || entries.length < 1 || entries.length > maximumSourceFiles) {
    qualificationFail(`source content tree must contain 1..${maximumSourceFiles} entries`)
  }
  const ordered = [...entries].sort((left, right) => compareCanonicalUTF8(left.path, right.path))
  const hash = createHash('sha256')
  hash.update('worksflow-source-content-tree/v1')
  hash.update(Buffer.from([0]))
  const count = Buffer.alloc(4)
  count.writeUInt32BE(ordered.length)
  hash.update(count)
  let prior = ''
  let aggregate = 0
  for (const [index, entry] of ordered.entries()) {
    requireExactKeys(entry, ['mode', 'path', 'sha256', 'sizeBytes'], [], `source content tree[${index}]`)
    requireSourcePath(entry.path, `source content tree[${index}].path`)
    if (index > 0 && compareCanonicalUTF8(entry.path, prior) === 0) qualificationFail('source content tree paths must be unique')
    prior = entry.path
    requireString(entry.mode, `source content tree[${index}].mode`, { pattern: /^(?:100644|100755)$/ })
    requireDigest(entry.sha256, `source content tree[${index}].sha256`)
    requireInteger(entry.sizeBytes, `source content tree[${index}].sizeBytes`, 0, maximumSourceBytes)
    aggregate += entry.sizeBytes
    if (!Number.isSafeInteger(aggregate) || aggregate > maximumSourceBytes) qualificationFail('source content tree is oversized')
    const pathBytes = Buffer.from(entry.path)
    const pathLength = Buffer.alloc(4)
    pathLength.writeUInt32BE(pathBytes.length)
    hash.update(pathLength)
    hash.update(pathBytes)
    hash.update(entry.mode)
    const size = Buffer.alloc(8)
    size.writeBigUInt64BE(BigInt(entry.sizeBytes))
    hash.update(size)
    hash.update(Buffer.from(entry.sha256.slice('sha256:'.length), 'hex'))
  }
  return `sha256:${hash.digest('hex')}`
}

export function verifyGoldenSourceRepository(repositoryRoot, expected) {
  if (!isAbsolute(repositoryRoot) || resolve(repositoryRoot) !== repositoryRoot || realpathSync(repositoryRoot) !== repositoryRoot) {
    qualificationFail('Golden source repository root must be an absolute normalized non-symlink path')
  }
  requireExactKeys(expected, ['commitOid', 'contentTreeDigest'], [], 'Golden source repository binding')
  requireString(expected.commitOid, 'Golden source repository binding.commitOid', {
    maximumBytes: 64,
    pattern: /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/,
  })
  requireDigest(expected.contentTreeDigest, 'Golden source repository binding.contentTreeDigest')
  const statusArguments = ['status', '--porcelain=v1', '-z', '--untracked-files=all', '--ignored=matching']
  if (runTrustedGit(repositoryRoot, statusArguments, 'Golden source repository status').length !== 0) {
    qualificationFail('Golden source repository must be an exact clean tracked-file closure')
  }
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let commit
  try {
    commit = decoder.decode(runTrustedGit(repositoryRoot, ['rev-parse', '--verify', 'HEAD'], 'Golden source commit')).trim()
  } catch {
    qualificationFail('Golden source commit must use canonical UTF-8')
  }
  if (commit !== expected.commitOid) qualificationFail('Golden source repository commit drift')
  let listing
  try {
    listing = decoder.decode(runTrustedGit(
      repositoryRoot,
      ['ls-tree', '-r', '-z', '--full-tree', 'HEAD'],
      'Golden source tree',
    ))
  } catch {
    qualificationFail('Golden source tree paths must use canonical UTF-8')
  }
  const records = listing.split('\0')
  if (records.at(-1) !== '') qualificationFail('Golden source tree listing is non-canonical')
  records.pop()
  if (records.length < 1 || records.length > maximumSourceFiles) qualificationFail('Golden source tree listing is empty or oversized')
  const entries = []
  for (const [index, record] of records.entries()) {
    const match = /^(100644|100755) blob (?:[0-9a-f]{40}|[0-9a-f]{64})\t(.+)$/.exec(record)
    if (!match) qualificationFail(`Golden source tree entry ${index} is unsupported`)
    const [, mode, path] = match
    requireSourcePath(path, `Golden source tree entry ${index}.path`)
    const absolute = resolve(repositoryRoot, path)
    if (!absolute.startsWith(`${repositoryRoot}/`)) qualificationFail(`Golden source tree entry ${index} escapes the repository`)
    const bytes = readStableSourceFile(absolute, mode === '100755')
    entries.push({ mode, path, sha256: hashBytes(bytes), sizeBytes: bytes.length })
  }
  const contentTreeDigest = computeGoldenSourceContentTreeDigest(entries)
  if (contentTreeDigest !== expected.contentTreeDigest) qualificationFail('Golden source repository content-tree drift')
  if (runTrustedGit(repositoryRoot, statusArguments, 'Golden source repository status').length !== 0) {
    qualificationFail('Golden source repository changed during source verification')
  }
  return Object.freeze({ commitOid: commit, contentTreeDigest })
}

export function goldenExecutionTestPaths(manifest) {
  requireObject(manifest, 'Golden qualification manifest')
  if (!Array.isArray(manifest.suites) || !Array.isArray(manifest.qualificationSupportPaths)) {
    qualificationFail('Golden qualification manifest suites and support paths are required')
  }
  const support = new Set(manifest.qualificationSupportPaths)
  const suites = manifest.suites.filter((suite) =>
    suite.mode === 'external-qualification' && suite.qualificationGroup === 'golden' && suite.executionKind === 'playwright')
  if (suites.length !== 5) qualificationFail('Golden qualification requires exactly five Playwright suites')
  const paths = []
  for (const suite of suites) {
    if (suite.coverage !== 'external-complete' || suite.status !== 'not-qualified' ||
        !Array.isArray(suite.testPaths) || suite.testPaths.length < 1) {
      qualificationFail(`${suite.id} is not a reviewed, not-yet-qualified executable suite`)
    }
    for (const [index, path] of suite.testPaths.entries()) {
      requireSourcePath(path, `${suite.id}.testPaths[${index}]`)
      if (!/^frontend\/tests\/golden-[a-z0-9-]+\.spec\.ts$/.test(path)) {
        qualificationFail(`${suite.id}.testPaths[${index}] is not a canonical Golden spec path`)
      }
      if (!support.has(path)) qualificationFail(`${path} must be hash-bound qualification support material`)
      paths.push(path)
    }
  }
  paths.sort(compareCanonicalUTF8)
  if (new Set(paths).size !== paths.length) qualificationFail('Golden qualification spec paths must be unique')
  return Object.freeze(paths)
}

function readGoldenDocument(environment, name) {
  const path = requiredEnvironment(environment, name)
  return { path, ...openProtectedFile(path, name, maximumDocumentBytes) }
}

export function loadGoldenDocuments(environment = process.env, now = Date.now()) {
  const authorityFile = readGoldenDocument(environment, 'WORKSFLOW_GOLDEN_AUTHORITY_FILE')
  const fixtureFile = readGoldenDocument(environment, 'WORKSFLOW_GOLDEN_FIXTURE_FILE')
  if (`${authorityFile.dev}:${authorityFile.ino}` === `${fixtureFile.dev}:${fixtureFile.ino}`) {
    qualificationFail('Golden authority and fixture must use distinct files')
  }
  const expectedAuthorityDigest = requireDigest(
    requiredEnvironment(environment, 'WORKSFLOW_GOLDEN_AUTHORITY_DIGEST'),
    'WORKSFLOW_GOLDEN_AUTHORITY_DIGEST',
  )
  const expectedFixtureDigest = requireDigest(
    requiredEnvironment(environment, 'WORKSFLOW_GOLDEN_FIXTURE_DIGEST'),
    'WORKSFLOW_GOLDEN_FIXTURE_DIGEST',
  )
  const expectedRunId = requireUUID(
    requiredEnvironment(environment, 'WORKSFLOW_QUALIFICATION_RUN_ID'),
    'WORKSFLOW_QUALIFICATION_RUN_ID',
  )
  const expectedPlanDigest = requireDigest(
    requiredEnvironment(environment, 'WORKSFLOW_QUALIFICATION_PLAN_DIGEST'),
    'WORKSFLOW_QUALIFICATION_PLAN_DIGEST',
  )
  const authority = parseGoldenAuthority(authorityFile.bytes)
  const fixture = parseGoldenFixture(fixtureFile.bytes)
  if (authority.documentDigest !== expectedAuthorityDigest) qualificationFail('Golden authority document digest drift')
  if (fixture.documentDigest !== expectedFixtureDigest) qualificationFail('Golden fixture document digest drift')
  if (fixture.document.authorityHash !== authority.authorityHash) qualificationFail('Golden fixture authorityHash drift')
  if (authority.document.subject.fixtureHash !== fixture.fixtureHash) qualificationFail('Golden authority fixtureHash drift')
  const exactBindings = [
    ['runId', expectedRunId],
    ['planDigest', expectedPlanDigest],
    ['issuedAt', authority.document.subject.issuedAt],
    ['expiresAt', authority.document.subject.expiresAt],
  ]
  for (const [field, expected] of exactBindings) {
    if (authority.document.subject[field] !== expected || fixture.document.subject[field] !== expected) {
      qualificationFail(`Golden authority/fixture ${field} binding drift`)
    }
  }
  if (!Number.isFinite(now) || authority.issuedAt > now + maximumClockSkewMilliseconds) {
    qualificationFail('Golden authority issuedAt is in the future')
  }
  const remaining = authority.expiresAt - now
  if (remaining < minimumRemainingLifetimeMilliseconds || remaining > maximumAuthorityLifetimeMilliseconds) {
    qualificationFail('Golden authority must have between 2 and 30 minutes remaining')
  }
  return deepFreeze({ authority, fixture })
}

function requireOpaqueCredential(value, label, minimumBytes = 32, maximumBytes = 8192) {
  requireString(value, label, { maximumBytes })
  if (Buffer.byteLength(value, 'utf8') < minimumBytes) qualificationFail(`${label} is too short`)
  return value
}

function validateCredentialSetMetadata(value, label, subject) {
  requireUUID(value.credentialSetId, `${label}.credentialSetId`)
  if (value.credentialSetId !== subject.credentialSet.setId) qualificationFail(`${label}.credentialSetId binding drift`)
  requireTimestamp(value.expiresAt, `${label}.expiresAt`)
  if (value.expiresAt !== subject.credentialSet.expiresAt) qualificationFail(`${label}.expiresAt binding drift`)
  requireTimestamp(value.issuedAt, `${label}.issuedAt`)
  if (value.issuedAt !== subject.credentialSet.issuedAt) qualificationFail(`${label}.issuedAt binding drift`)
  requireServiceIdentity(value.issuer, `${label}.issuer`)
  if (value.issuer !== subject.credentialSet.issuer) qualificationFail(`${label}.issuer binding drift`)
  requireUUID(value.runId, `${label}.runId`)
  if (value.runId !== subject.runId) qualificationFail(`${label}.runId binding drift`)
}

function expectedCredentialAudience(definition, fixture) {
  switch (definition.audience) {
    case 'platform-web': return fixture.subject.platform.webOrigin
    case 'reference-api': return fixture.subject.reference.apiOrigin
    case 'reference-web': return fixture.subject.reference.webOrigin
    default: return fixture.subject.credentialSet.audience
  }
}

function validateStorageState(state, label, expectedOrigin, credentialExpiresAt, sessionCookieName, requireCSRF, csrf) {
  requireExactKeys(state, ['cookies', 'origins'], [], label)
  requireArray(state.cookies, `${label}.cookies`, requireCSRF ? 2 : 1, 128)
  requireArray(state.origins, `${label}.origins`, requireCSRF ? 1 : 0, 1)
  const expected = new URL(expectedOrigin)
  let priorCookie = ''
  let hasSessionCookie = false
  let hasCSRFCookie = false
  for (const [index, cookie] of state.cookies.entries()) {
    const item = `${label}.cookies[${index}]`
    requireExactKeys(cookie, ['domain', 'expires', 'httpOnly', 'name', 'path', 'sameSite', 'secure', 'value'], [], item)
    requireString(cookie.domain, `${item}.domain`, { exact: expected.hostname })
    requireInteger(cookie.expires, `${item}.expires`, 0, 4_102_444_800)
    if (cookie.expires !== credentialExpiresAt / 1000) {
      qualificationFail(`${item}.expires must match the atomic credential-set expiry`)
    }
    requireBoolean(cookie.httpOnly, `${item}.httpOnly`)
    requireString(cookie.name, `${item}.name`, { maximumBytes: 256, pattern: /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/ })
    const cookieIdentity = `${cookie.domain}\0${cookie.path}\0${cookie.name}`
    if (index > 0 && compareCanonicalUTF8(cookieIdentity, priorCookie) <= 0) {
      qualificationFail(`${label}.cookies must be strictly sorted and unique by domain/path/name`)
    }
    priorCookie = cookieIdentity
    requireString(cookie.path, `${item}.path`, { exact: '/' })
    requireString(cookie.sameSite, `${item}.sameSite`, { pattern: /^(?:Strict|Lax|None)$/ })
    requireBoolean(cookie.secure, `${item}.secure`, true)
    requireOpaqueCredential(cookie.value, `${item}.value`, 16, 8192)
    if (cookie.name === sessionCookieName && cookie.httpOnly && cookie.sameSite !== 'None') hasSessionCookie = true
    if (csrf && cookie.name === csrf.cookieName && cookie.httpOnly === false) hasCSRFCookie = true
  }
  if (!hasSessionCookie) qualificationFail(`${label} must contain the declared Secure HttpOnly Lax/Strict session cookie`)
  if (requireCSRF && !hasCSRFCookie) qualificationFail(`${label} must contain the declared readable CSRF cookie`)
  for (const [index, origin] of state.origins.entries()) {
    const item = `${label}.origins[${index}]`
    requireExactKeys(origin, ['localStorage', 'origin'], [], item)
    requireArray(origin.localStorage, `${item}.localStorage`, 0, 0)
    requireString(origin.origin, `${item}.origin`, { exact: expectedOrigin })
    let priorName = ''
    for (const [storageIndex, entry] of origin.localStorage.entries()) {
      const storageItem = `${item}.localStorage[${storageIndex}]`
      requireExactKeys(entry, ['name', 'value'], [], storageItem)
      requireString(entry.name, `${storageItem}.name`, { maximumBytes: 256 })
      if (storageIndex > 0 && compareCanonicalUTF8(entry.name, priorName) <= 0) {
        qualificationFail(`${item}.localStorage must be strictly sorted and unique by name`)
      }
      priorName = entry.name
      requireString(entry.value, `${storageItem}.value`, { maximumBytes: 16 << 10 })
      if (/(?:authorization|bearer|access.?token|refresh.?token)/i.test(entry.name) || /^bearer\s/i.test(entry.value)) {
        qualificationFail(`${storageItem} must not expose bearer credentials in browser storage`)
      }
    }
  }
}

function parseBearerCredential(bytes, label, slot, member, principal, subject) {
  const value = parseCanonicalDocument(bytes, label, 16 << 10)
  requireExactKeys(value, [
    'actorId',
    'audience',
    'credentialHandle',
    'credentialSetId',
    'expiresAt',
    'issuedAt',
    'issuer',
    'runId',
    'schemaVersion',
    'slot',
    'token',
    'tokenType',
  ], [], label)
  requireUUID(value.actorId, `${label}.actorId`)
  if (value.actorId !== member.actorId || value.actorId !== principal.actorId) qualificationFail(`${label}.actorId binding drift`)
  requireString(value.audience, `${label}.audience`, { exact: subject.credentialSet.audience })
  const handle = requireOpaqueCredential(value.credentialHandle, `${label}.credentialHandle`, 32, 512)
  if (hashBytes(Buffer.from(handle)) !== member.credentialHandleHash) qualificationFail(`${label}.credentialHandle binding drift`)
  validateCredentialSetMetadata(value, label, subject)
  requireString(value.schemaVersion, `${label}.schemaVersion`, { exact: goldenBearerCredentialSchema })
  requireString(value.slot, `${label}.slot`, { exact: slot })
  const token = requireOpaqueCredential(value.token, `${label}.token`)
  requireString(value.tokenType, `${label}.tokenType`, { exact: 'Bearer' })
  return Object.freeze({
    actorId: value.actorId,
    audience: value.audience,
    kind: member.kind,
    role: principal.role,
    slot,
    token,
    materialFingerprint: hashBytes(Buffer.from(token)),
  })
}

function parseStorageCredential(bytes, label, slot, definition, member, principal, fixture) {
  const value = parseCanonicalDocument(bytes, label, 2 << 20)
  const requireCSRF = slot.startsWith('platform-browser-')
  requireExactKeys(value, [
    'actorId',
    'audience',
    'credentialHandle',
    'credentialSetAudience',
    'credentialSetId',
    'expiresAt',
    'issuedAt',
    'issuer',
    'runId',
    'schemaVersion',
    'sessionCookieName',
    'slot',
    'storageState',
    ...(requireCSRF ? ['csrf'] : []),
  ], [], label)
  requireUUID(value.actorId, `${label}.actorId`)
  if (value.actorId !== member.actorId || value.actorId !== principal.actorId) qualificationFail(`${label}.actorId binding drift`)
  const audience = expectedCredentialAudience(definition, fixture)
  requireString(value.audience, `${label}.audience`, { exact: audience })
  const handle = requireOpaqueCredential(value.credentialHandle, `${label}.credentialHandle`, 32, 512)
  if (hashBytes(Buffer.from(handle)) !== member.credentialHandleHash) qualificationFail(`${label}.credentialHandle binding drift`)
  requireString(value.credentialSetAudience, `${label}.credentialSetAudience`, {
    exact: fixture.subject.credentialSet.audience,
  })
  validateCredentialSetMetadata(value, label, fixture.subject)
  requireString(value.schemaVersion, `${label}.schemaVersion`, { exact: goldenStorageCredentialSchema })
  requireString(value.sessionCookieName, `${label}.sessionCookieName`, {
    maximumBytes: 256,
    pattern: /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/,
  })
  requireString(value.slot, `${label}.slot`, { exact: slot })
  let csrf
  if (requireCSRF) {
    requireExactKeys(value.csrf, ['cookieName', 'headerName'], [], `${label}.csrf`)
    requireString(value.csrf.cookieName, `${label}.csrf.cookieName`, {
      maximumBytes: 256,
      pattern: /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/,
    })
    requireString(value.csrf.headerName, `${label}.csrf.headerName`, { exact: 'X-CSRF-Token' })
    csrf = Object.freeze({ ...value.csrf })
  }
  validateStorageState(
    value.storageState,
    `${label}.storageState`,
    audience,
    Date.parse(fixture.subject.credentialSet.expiresAt),
    value.sessionCookieName,
    requireCSRF,
    csrf,
  )
  return Object.freeze({
    actorId: value.actorId,
    audience,
    csrf,
    kind: member.kind,
    role: principal.role,
    slot,
    storageState: deepFreeze(value.storageState),
    materialFingerprints: value.storageState.cookies.map((cookie) => hashBytes(Buffer.from(cookie.value))),
  })
}

export function loadGoldenCredentialFiles(environment = process.env, fixtureInput) {
  const candidate = fixtureInput?.document?.subject ? fixtureInput.document : fixtureInput
  requireObject(candidate, 'Golden fixture credential input')
  // loadGoldenQualificationInputs supplies an already actual-byte-validated
  // document. Re-parse its canonical projection here so the separately
  // exported credential loader cannot be called with a shape-invalid lookalike.
  const fixture = parseGoldenFixture(Buffer.from(canonicalJSON(candidate))).document
  const principals = new Map(fixture.subject.principals.map((principal) => [principal.slot, principal]))
  const members = new Map(fixture.subject.credentialSet.memberBindings.map((member) => [member.slot, member]))
  const result = {}
  const identities = new Set()
  const materialFingerprints = new Set()
  for (const slot of goldenCredentialSlots) {
    const definition = credentialDefinitions[slot]
    const path = requiredEnvironment(environment, definition.environment)
    const protectedFile = openProtectedFile(
      path,
      definition.environment,
      definition.kind === 'token' ? 16 << 10 : 2 << 20,
      0o600,
    )
    const identity = `${protectedFile.dev}:${protectedFile.ino}`
    if (identities.has(identity)) qualificationFail('Golden credential slots must use distinct files')
    identities.add(identity)
    const member = members.get(slot)
    const principal = principals.get(definition.principalSlot)
    const credential = definition.kind === 'token'
      ? parseBearerCredential(
          protectedFile.bytes,
          definition.environment,
          slot,
          member,
          principal,
          fixture.subject,
        )
      : parseStorageCredential(
          protectedFile.bytes,
          definition.environment,
          slot,
          definition,
          member,
          principal,
          fixture,
        )
    const fingerprints = credential.materialFingerprints ?? [credential.materialFingerprint]
    for (const fingerprint of fingerprints) {
      if (materialFingerprints.has(fingerprint)) {
        qualificationFail('Golden credential slots must not reuse credential material')
      }
      materialFingerprints.add(fingerprint)
    }
    const {
      materialFingerprint: _materialFingerprint,
      materialFingerprints: _materialFingerprints,
      ...publicCredential
    } = credential
    result[slot] = Object.freeze(publicCredential)
  }
  return deepFreeze(result)
}

export function loadGoldenQualificationInputs(environment = process.env, now = Date.now()) {
  const documents = loadGoldenDocuments(environment, now)
  const credentials = loadGoldenCredentialFiles(environment, documents.fixture)
  return deepFreeze({ ...documents, credentials })
}

export function deepFreeze(value) {
  if (!value || typeof value !== 'object' || Object.isFrozen(value)) return value
  for (const entry of Object.values(value)) deepFreeze(entry)
  return Object.freeze(value)
}
