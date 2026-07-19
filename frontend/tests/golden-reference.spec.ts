import { createHash } from 'node:crypto'

import { canonicalJSON } from '../scripts/qualification-core.mjs'
import { expect, goldenQualificationEnvironment, test, type APIRequestContext } from './qualification-runtime'
import {
  assertDigest,
  assertGoldenFaultTarget,
  assertTimestamp,
  assertUUID,
  browserStorageState,
  consumeGoldenFault,
  goldenPrincipal,
  goldenSubject,
  qualificationKey,
  referenceAPIContext,
  waitForValue,
} from './golden-qualification-support'

type JSONRecord = Record<string, unknown>

type ConversationRecord = Readonly<{
  id: string
  projectId: string
  title: string
  createdAt: string
  updatedAt: string
}>

type MessageRecord = Readonly<{
  id: string
  projectId: string
  conversationId: string
  role: string
  content: string
  sequence: number
  createdAt: string
}>

type RunRecord = Readonly<{
  id: string
  projectId: string
  conversationId: string
  attempt: number
  status: 'queued' | 'running' | 'completed' | 'failed' | 'cancelled'
  modelProfileId: string
  retryOfRunId?: string | null
  errorCode?: string | null
  createdAt: string
  updatedAt: string
}>

type ReferenceCommand = Readonly<{
  argv: readonly string[]
  identity: string
  workingDirectory: string
}>

type ReferenceRuntimeAuthority = Readonly<{
  commands: Readonly<{
    api: ReferenceCommand
    migration: ReferenceCommand
    retention: ReferenceCommand
    web: ReferenceCommand
  }>
  gateway: Readonly<{
    attestationDigest: string
    capabilityDigest: string
    identity: string
    modelProfile: Readonly<{
      contentHash: string
      id: string
      maxAttempts: number
      modelId: string
      modelRevision: string
      providerId: string
      timeoutMilliseconds: number
    }>
    providerPolicy: Readonly<{
      contentHash: string
      fallbackAllowed: false
      id: 'reference-project-default'
      profilePinned: true
    }>
    routeId: string
    secretInjectionReceipt: Readonly<{ id: string; contentHash: string }>
  }>
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
  rateLimit: Readonly<{
    burst: number
    contentHash: string
    id: 'reference-rate-limit-v1'
    requests: number
    scopes: readonly ['project', 'tenant-actor']
    windowSeconds: number
  }>
  deploymentAdmission: Readonly<{
    migrationCompletedAt: string
    trafficEnabledAt: string
  }>
  retentionPolicy: Readonly<{
    auditDays: number
    contentHash: string
    eventDays: number
    id: string
    messageDays: number
    redactionRequired: true
    runDays: number
  }>
}>

type RunEvent = Readonly<{
  data: JSONRecord
  eventId: string
  sequence: number
  type:
    | 'heartbeat'
    | 'output.delta'
    | 'run.cancelled'
    | 'run.completed'
    | 'run.failed'
    | 'run.queued'
    | 'run.started'
    | 'tool.call'
    | 'tool.result'
}>

type ReferenceQualificationOperation = ReferenceRuntimeAuthority['qualificationOperationSet']['operations'][number]

const terminalEventTypes = ['run.cancelled', 'run.completed', 'run.failed'] as const

function strictObject(
  value: unknown,
  required: readonly string[],
  optional: readonly string[] = [],
  label = 'response',
) {
  expect(value && typeof value === 'object' && !Array.isArray(value), `${label} object`).toBeTruthy()
  const source = value as JSONRecord
  const keys = Object.keys(source).sort()
  expect(keys, `${label} required fields`).toEqual(expect.arrayContaining([...required]))
  expect(keys.every((key) => required.includes(key) || optional.includes(key)), `${label} unknown field`).toBe(true)
  return source
}

function nonEmptyString(value: unknown, label: string) {
  expect(typeof value, `${label} type`).toBe('string')
  expect((value as string).length, `${label} non-empty`).toBeGreaterThan(0)
  return value as string
}

function nonNegativeInteger(value: unknown, label: string) {
  expect(Number.isSafeInteger(value) && Number(value) >= 0, label).toBe(true)
  return Number(value)
}

function positiveInteger(value: unknown, label: string) {
  const result = nonNegativeInteger(value, label)
  expect(result, label).toBeGreaterThan(0)
  return result
}

function conversation(value: unknown, projectId: string) {
  const source = strictObject(value, ['id', 'projectId', 'title', 'createdAt', 'updatedAt'], [], 'Conversation')
  assertUUID(source.id as string, 'Conversation.id')
  expect(source.projectId).toBe(projectId)
  nonEmptyString(source.title, 'Conversation.title')
  assertTimestamp(source.createdAt as string, 'Conversation.createdAt')
  assertTimestamp(source.updatedAt as string, 'Conversation.updatedAt')
  return source as unknown as ConversationRecord
}

function message(value: unknown, projectId: string, conversationId: string) {
  const source = strictObject(
    value,
    ['id', 'projectId', 'conversationId', 'role', 'content', 'sequence', 'createdAt'],
    [],
    'Message',
  )
  assertUUID(source.id as string, 'Message.id')
  expect(source.projectId).toBe(projectId)
  expect(source.conversationId).toBe(conversationId)
  expect(['system', 'user', 'assistant', 'tool']).toContain(source.role)
  expect(typeof source.content).toBe('string')
  positiveInteger(source.sequence, 'Message.sequence')
  assertTimestamp(source.createdAt as string, 'Message.createdAt')
  return source as unknown as MessageRecord
}

function run(value: unknown, projectId: string, conversationId: string, modelProfileId: string) {
  const source = strictObject(
    value,
    ['id', 'projectId', 'conversationId', 'attempt', 'status', 'modelProfileId', 'createdAt', 'updatedAt'],
    ['retryOfRunId', 'errorCode'],
    'Run',
  )
  assertUUID(source.id as string, 'Run.id')
  expect(source.projectId).toBe(projectId)
  expect(source.conversationId).toBe(conversationId)
  positiveInteger(source.attempt, 'Run.attempt')
  expect(['queued', 'running', 'completed', 'failed', 'cancelled']).toContain(source.status)
  expect(source.modelProfileId).toBe(modelProfileId)
  if ('retryOfRunId' in source && source.retryOfRunId !== null) {
    assertUUID(source.retryOfRunId as string, 'Run.retryOfRunId')
  }
  if ('errorCode' in source && source.errorCode !== null) nonEmptyString(source.errorCode, 'Run.errorCode')
  assertTimestamp(source.createdAt as string, 'Run.createdAt')
  assertTimestamp(source.updatedAt as string, 'Run.updatedAt')
  return source as unknown as RunRecord
}

function exactIdentity(value: unknown, expected: { id: string; contentHash: string }, label: string) {
  const identity = strictObject(value, ['contentHash', 'id'], [], label)
  expect(identity).toEqual(expected)
  assertDigest(identity.contentHash as string, `${label}.contentHash`)
  return identity as unknown as { id: string; contentHash: string }
}

function exactCommand(value: unknown, label: string): ReferenceCommand {
  const command = strictObject(value, ['argv', 'identity', 'workingDirectory'], [], label)
  nonEmptyString(command.identity, `${label}.identity`)
  nonEmptyString(command.workingDirectory, `${label}.workingDirectory`)
  expect(Array.isArray(command.argv), `${label}.argv array`).toBe(true)
  expect((command.argv as unknown[]).length, `${label}.argv non-empty`).toBeGreaterThan(0)
  for (const [index, argument] of (command.argv as unknown[]).entries()) {
    nonEmptyString(argument, `${label}.argv[${index}]`)
  }
  return command as unknown as ReferenceCommand
}

async function exactQualificationEvidence(
  api: APIRequestContext,
  evidenceDigest: string,
  expectedDocument: JSONRecord,
  label: string,
) {
  assertDigest(evidenceDigest, `${label}.evidenceDigest`)
  const subject = goldenSubject()
  const response = await api.get(
    `/api/v1/qualification/evidence/${encodeURIComponent(evidenceDigest)}`,
    { params: { fixtureId: subject.fixtureId, runId: subject.runId } },
  )
  const raw = await response.body()
  expect(
    response.status(),
    `${label} exact evidence bytes are required; response=${response.status()} ${raw.toString('utf8')}`,
  ).toBe(200)
  expect(response.headers()['content-type']).toMatch(/^application\/json(?:;|$)/iu)
  expect(response.headers()['cache-control']).toBe('public, max-age=31536000, immutable')
  expect(response.headers().etag).toBe(`"${evidenceDigest}"`)
  expect(`sha256:${createHash('sha256').update(raw).digest('hex')}`).toBe(evidenceDigest)
  const text = new TextDecoder('utf-8', { fatal: true }).decode(raw)
  expect(text.startsWith('\uFEFF')).toBe(false)
  const document = JSON.parse(text) as unknown
  expect(text).toBe(canonicalJSON(document))
  expect(document).toEqual(expectedDocument)
  return document
}

function evidenceDocument(receipt: JSONRecord) {
  const result = { ...receipt }
  delete result.evidenceDigest
  return result
}

function assertQualificationOperation(
  authority: ReferenceRuntimeAuthority,
  operation: ReferenceQualificationOperation,
) {
  const matches = authority.qualificationOperationSet.operations.filter((entry) => entry === operation)
  expect(matches, `${operation} must occur exactly once in the root-bound qualification operation set`).toHaveLength(1)
  return operation
}

async function runtimeAuthority(api: APIRequestContext): Promise<ReferenceRuntimeAuthority> {
  const subject = goldenSubject()
  const response = await api.get(
    `/api/v1/qualification/deployment-receipts/${subject.reference.deploymentReceipt.id}`,
    {
      params: { contentHash: subject.reference.deploymentReceipt.contentHash },
    },
  )
  const raw = await response.body()
  expect(
    response.status(),
    `exact immutable Reference deployment receipt is required; response=${response.status()} ${raw.toString('utf8')}`,
  ).toBe(200)
  expect(response.headers()['content-type']).toMatch(/^application\/json(?:;|$)/iu)
  expect(response.headers()['cache-control']).toBe('public, max-age=31536000, immutable')
  expect(response.headers().etag).toBe(`"${subject.reference.deploymentReceipt.contentHash}"`)
  const observedHash = `sha256:${createHash('sha256').update(raw).digest('hex')}`
  expect(observedHash).toBe(subject.reference.deploymentReceipt.contentHash)
  const text = new TextDecoder('utf-8', { fatal: true }).decode(raw)
  expect(text.startsWith('\uFEFF')).toBe(false)
  const document = JSON.parse(text) as unknown
  expect(text).toBe(canonicalJSON(document))
  const source = strictObject(document, [
    'applicationId', 'commands', 'contractBundle', 'deploymentAdmission', 'gateway',
    'images', 'issuedAt', 'migration', 'qualificationOperationSet', 'rateLimit',
    'receiptId', 'retentionPolicy', 'runEventSchemaDigest', 'schemaVersion',
  ], [], 'ReferenceDeploymentRuntimeReceipt')
  expect(source.schemaVersion).toBe(subject.reference.deploymentReceipt.schemaVersion)
  expect(source.receiptId).toBe(subject.reference.deploymentReceipt.id)
  assertTimestamp(source.issuedAt as string, 'ReferenceDeploymentRuntimeReceipt.issuedAt')
  expect(source.applicationId).toBe(subject.reference.applicationId)
  exactIdentity(source.contractBundle, subject.reference.contractBundle, 'ReferenceDeploymentRuntimeReceipt.contractBundle')
  expect(source.migration).toEqual(subject.reference.migration)
  expect(source.runEventSchemaDigest).toBe(subject.reference.runEventSchemaDigest)

  const admission = strictObject(
    source.deploymentAdmission,
    ['migrationCompletedAt', 'trafficEnabledAt'],
    [],
    'ReferenceDeploymentRuntimeReceipt.deploymentAdmission',
  )
  assertTimestamp(admission.migrationCompletedAt as string, 'deployment migrationCompletedAt')
  assertTimestamp(admission.trafficEnabledAt as string, 'deployment trafficEnabledAt')
  expect(Date.parse(admission.trafficEnabledAt as string)).toBeGreaterThanOrEqual(
    Date.parse(admission.migrationCompletedAt as string),
  )

  const images = strictObject(source.images, ['api', 'web'], [], 'ReferenceDeploymentRuntimeReceipt.images')
  expect(images.api).toBe(subject.reference.apiImageDigest)
  expect(images.web).toBe(subject.reference.webImageDigest)
  assertDigest(images.api as string, 'ReferenceDeploymentRuntimeReceipt.images.api')
  assertDigest(images.web as string, 'ReferenceDeploymentRuntimeReceipt.images.web')

  const commandsSource = strictObject(
    source.commands,
    ['api', 'migration', 'retention', 'web'],
    [],
    'ReferenceDeploymentRuntimeReceipt.commands',
  )
  const commands = {
    api: exactCommand(commandsSource.api, 'ReferenceDeploymentRuntimeReceipt.commands.api'),
    migration: exactCommand(commandsSource.migration, 'ReferenceDeploymentRuntimeReceipt.commands.migration'),
    retention: exactCommand(commandsSource.retention, 'ReferenceDeploymentRuntimeReceipt.commands.retention'),
    web: exactCommand(commandsSource.web, 'ReferenceDeploymentRuntimeReceipt.commands.web'),
  }
  expect(commands).toEqual(subject.reference.commands)

  const gateway = strictObject(source.gateway, [
    'attestationDigest', 'capabilityDigest', 'identity', 'modelProfile',
    'providerPolicy', 'routeId', 'secretInjectionReceipt',
  ], [], 'ReferenceDeploymentRuntimeReceipt.gateway')
  assertDigest(gateway.attestationDigest as string, 'ReferenceDeploymentRuntimeReceipt.gateway.attestationDigest')
  assertDigest(gateway.capabilityDigest as string, 'ReferenceDeploymentRuntimeReceipt.gateway.capabilityDigest')
  nonEmptyString(gateway.identity, 'ReferenceDeploymentRuntimeReceipt.gateway.identity')
  nonEmptyString(gateway.routeId, 'ReferenceDeploymentRuntimeReceipt.gateway.routeId')
  const secretInjectionReceipt = strictObject(
    gateway.secretInjectionReceipt,
    ['contentHash', 'id'],
    [],
    'ReferenceDeploymentRuntimeReceipt.gateway.secretInjectionReceipt',
  )
  assertUUID(secretInjectionReceipt.id as string, 'ReferenceDeploymentRuntimeReceipt.gateway.secretInjectionReceipt.id')
  assertDigest(
    secretInjectionReceipt.contentHash as string,
    'ReferenceDeploymentRuntimeReceipt.gateway.secretInjectionReceipt.contentHash',
  )
  const providerPolicy = strictObject(
    gateway.providerPolicy,
    ['contentHash', 'fallbackAllowed', 'id', 'profilePinned'],
    [],
    'ReferenceDeploymentRuntimeReceipt.gateway.providerPolicy',
  )
  expect(providerPolicy.id).toBe('reference-project-default')
  expect(providerPolicy.fallbackAllowed).toBe(false)
  expect(providerPolicy.profilePinned).toBe(true)
  assertDigest(providerPolicy.contentHash as string, 'ReferenceDeploymentRuntimeReceipt.gateway.providerPolicy.contentHash')
  const modelProfile = strictObject(gateway.modelProfile, [
    'contentHash', 'id', 'maxAttempts', 'modelId', 'modelRevision',
    'providerId', 'timeoutMilliseconds',
  ], [], 'ReferenceDeploymentRuntimeReceipt.gateway.modelProfile')
  nonEmptyString(modelProfile.id, 'ReferenceDeploymentRuntimeReceipt.gateway.modelProfile.id')
  nonEmptyString(modelProfile.modelId, 'ReferenceDeploymentRuntimeReceipt.gateway.modelProfile.modelId')
  nonEmptyString(modelProfile.modelRevision, 'ReferenceDeploymentRuntimeReceipt.gateway.modelProfile.modelRevision')
  nonEmptyString(modelProfile.providerId, 'ReferenceDeploymentRuntimeReceipt.gateway.modelProfile.providerId')
  assertDigest(modelProfile.contentHash as string, 'ReferenceDeploymentRuntimeReceipt.gateway.modelProfile.contentHash')
  expect(modelProfile).toEqual(subject.reference.gateway.modelProfile)
  expect(providerPolicy).toEqual(subject.reference.gateway.providerPolicy)
  expect(secretInjectionReceipt).toEqual(subject.reference.gateway.secretInjectionReceipt)
  expect(gateway).toEqual(subject.reference.gateway)

  const qualificationOperationSet = strictObject(source.qualificationOperationSet, [
    'contentHash', 'operations', 'schemaVersion',
  ], [], 'ReferenceDeploymentRuntimeReceipt.qualificationOperationSet')
  assertDigest(
    qualificationOperationSet.contentHash as string,
    'ReferenceDeploymentRuntimeReceipt.qualificationOperationSet.contentHash',
  )
  expect(qualificationOperationSet).toEqual(subject.reference.qualificationOperationSet)

  const rateLimit = strictObject(
    source.rateLimit,
    ['burst', 'contentHash', 'id', 'requests', 'scopes', 'windowSeconds'],
    [],
    'ReferenceDeploymentRuntimeReceipt.rateLimit',
  )
  assertDigest(rateLimit.contentHash as string, 'ReferenceDeploymentRuntimeReceipt.rateLimit.contentHash')
  expect(rateLimit).toEqual(subject.reference.rateLimitPolicy)

  const retention = strictObject(source.retentionPolicy, [
    'auditDays', 'contentHash', 'eventDays', 'id', 'messageDays',
    'redactionRequired', 'runDays',
  ], [], 'ReferenceDeploymentRuntimeReceipt.retentionPolicy')
  assertDigest(retention.contentHash as string, 'ReferenceDeploymentRuntimeReceipt.retentionPolicy.contentHash')
  expect(retention).toEqual(subject.reference.retentionPolicy)

  return {
    commands,
    deploymentAdmission: admission as unknown as ReferenceRuntimeAuthority['deploymentAdmission'],
    gateway: {
      attestationDigest: gateway.attestationDigest as string,
      capabilityDigest: gateway.capabilityDigest as string,
      identity: gateway.identity as string,
      modelProfile: modelProfile as unknown as ReferenceRuntimeAuthority['gateway']['modelProfile'],
      providerPolicy: providerPolicy as unknown as ReferenceRuntimeAuthority['gateway']['providerPolicy'],
      routeId: gateway.routeId as string,
      secretInjectionReceipt: secretInjectionReceipt as unknown as { id: string; contentHash: string },
    },
    qualificationOperationSet: qualificationOperationSet as unknown as ReferenceRuntimeAuthority['qualificationOperationSet'],
    rateLimit: rateLimit as unknown as ReferenceRuntimeAuthority['rateLimit'],
    retentionPolicy: retention as unknown as ReferenceRuntimeAuthority['retentionPolicy'],
  }
}

async function createConversationAndMessage(
  api: APIRequestContext,
  caseId: string,
  vector: string,
) {
  const principal = goldenPrincipal('reference-user-a')
  const conversationInput = { title: `${caseId} ${vector} ${goldenSubject().runId}` }
  const conversationKey = qualificationKey(caseId, `${vector}-conversation`)
  const created = await api.post('/api/v1/conversations', {
    headers: { 'Idempotency-Key': conversationKey },
    data: conversationInput,
  })
  expect(created.status()).toBe(201)
  const createdConversation = conversation(await created.json(), principal.projectId)
  const messageInput = {
    clientMessageId: qualificationKey(caseId, `${vector}-client-message`),
    content: `Golden qualification ${caseId} ${vector} ${goldenSubject().runId}`,
  }
  const messageKey = qualificationKey(caseId, `${vector}-message`)
  const sent = await api.post(`/api/v1/conversations/${createdConversation.id}/messages`, {
    headers: { 'Idempotency-Key': messageKey },
    data: messageInput,
  })
  expect(sent.status()).toBe(201)
  const createdMessage = message(await sent.json(), principal.projectId, createdConversation.id)
  return {
    conversation: createdConversation,
    conversationInput,
    conversationKey,
    message: createdMessage,
    messageInput,
    messageKey,
    principal,
  }
}

async function createRun(
  api: APIRequestContext,
  caseId: string,
  vector: string,
  conversationId: string,
  messageId: string,
  authority: ReferenceRuntimeAuthority,
) {
  const principal = goldenPrincipal('reference-user-a')
  const input = {
    triggerMessageId: messageId,
    modelProfileId: authority.gateway.modelProfile.id,
  }
  const key = qualificationKey(caseId, `${vector}-run`)
  const response = await api.post(`/api/v1/conversations/${conversationId}/runs`, {
    headers: { 'Idempotency-Key': key },
    data: input,
  })
  expect(response.status()).toBe(202)
  return {
    input,
    key,
    value: run(await response.json(), principal.projectId, conversationId, authority.gateway.modelProfile.id),
  }
}

async function readRun(
  api: APIRequestContext,
  conversationId: string,
  runId: string,
  projectId: string,
  authority: ReferenceRuntimeAuthority,
) {
  const response = await api.get(`/api/v1/conversations/${conversationId}/runs/${runId}`)
  expect(response.status()).toBe(200)
  return run(await response.json(), projectId, conversationId, authority.gateway.modelProfile.id)
}

async function waitRun(
  api: APIRequestContext,
  record: RunRecord,
  authority: ReferenceRuntimeAuthority,
  accept: (value: RunRecord) => boolean,
  label: string,
) {
  return waitForValue(
    () => readRun(api, record.conversationId, record.id, record.projectId, authority),
    accept,
    label,
    150_000,
  )
}

function validateEventData(type: RunEvent['type'], value: unknown, modelProfileId: string) {
  switch (type) {
    case 'run.queued': {
      const data = strictObject(value, ['queuePosition'], [], 'RunEvent.data(run.queued)')
      nonNegativeInteger(data.queuePosition, 'RunEvent.data.queuePosition')
      return data
    }
    case 'run.started': {
      const data = strictObject(value, ['modelProfileId'], [], 'RunEvent.data(run.started)')
      expect(data.modelProfileId).toBe(modelProfileId)
      return data
    }
    case 'output.delta': {
      const data = strictObject(value, ['text'], [], 'RunEvent.data(output.delta)')
      nonEmptyString(data.text, 'RunEvent.data.text')
      return data
    }
    case 'tool.call': {
      const data = strictObject(value, ['arguments', 'callId', 'name'], [], 'RunEvent.data(tool.call)')
      nonEmptyString(data.callId, 'RunEvent.data.callId')
      nonEmptyString(data.name, 'RunEvent.data.name')
      strictObject(data.arguments, [], Object.keys(data.arguments as JSONRecord), 'RunEvent.data.arguments')
      return data
    }
    case 'tool.result': {
      const data = strictObject(value, ['callId', 'output'], [], 'RunEvent.data(tool.result)')
      nonEmptyString(data.callId, 'RunEvent.data.callId')
      return data
    }
    case 'run.completed': {
      const data = strictObject(value, ['finishReason', 'usage'], [], 'RunEvent.data(run.completed)')
      nonEmptyString(data.finishReason, 'RunEvent.data.finishReason')
      const usage = strictObject(data.usage, ['inputTokens', 'outputTokens'], [], 'RunEvent.data.usage')
      nonNegativeInteger(usage.inputTokens, 'RunEvent.data.usage.inputTokens')
      nonNegativeInteger(usage.outputTokens, 'RunEvent.data.usage.outputTokens')
      return data
    }
    case 'run.failed': {
      const data = strictObject(
        value,
        ['errorCode', 'errorMessage', 'retryable'],
        [],
        'RunEvent.data(run.failed)',
      )
      nonEmptyString(data.errorCode, 'RunEvent.data.errorCode')
      nonEmptyString(data.errorMessage, 'RunEvent.data.errorMessage')
      expect(typeof data.retryable).toBe('boolean')
      return data
    }
    case 'run.cancelled': {
      const data = strictObject(value, ['reason'], [], 'RunEvent.data(run.cancelled)')
      nonEmptyString(data.reason, 'RunEvent.data.reason')
      return data
    }
    case 'heartbeat': {
      const data = strictObject(value, ['cursor'], [], 'RunEvent.data(heartbeat)')
      positiveInteger(data.cursor, 'RunEvent.data.cursor')
      return data
    }
  }
}

function parseSSE(
  source: string,
  projectId: string,
  conversationId: string,
  runId: string,
  modelProfileId: string,
  expectedFirstSequence: number,
) {
  expect(source.trim().length, 'SSE stream must contain events').toBeGreaterThan(0)
  const events = source.trim().split(/\r?\n\r?\n+/u).map((block, index) => {
    const lines = block.split(/\r?\n/u)
    expect(lines.every((line) => /^(?:id|event|data):/u.test(line)), 'SSE field whitelist').toBe(true)
    const ids = lines.filter((line) => line.startsWith('id:'))
    const types = lines.filter((line) => line.startsWith('event:'))
    const dataLines = lines.filter((line) => line.startsWith('data:'))
    expect(ids).toHaveLength(1)
    expect(types).toHaveLength(1)
    expect(dataLines.length).toBeGreaterThan(0)
    const id = ids[0]!.slice(3).trim()
    const eventType = types[0]!.slice(6).trim() as RunEvent['type']
    expect(id).toMatch(/^[1-9]\d*$/u)
    expect([
      'run.queued', 'run.started', 'output.delta', 'tool.call', 'tool.result',
      'run.completed', 'run.failed', 'run.cancelled', 'heartbeat',
    ]).toContain(eventType)
    const encoded = dataLines.map((line) => line.slice(5).trimStart()).join('\n')
    const payload = strictObject(JSON.parse(encoded), [
      'schemaVersion', 'eventId', 'projectId', 'conversationId', 'runId',
      'sequence', 'type', 'createdAt', 'data',
    ], [], 'RunEvent')
    expect(payload.schemaVersion).toBe('run-event/v1')
    assertUUID(payload.eventId as string, 'RunEvent.eventId')
    expect(payload.projectId).toBe(projectId)
    expect(payload.conversationId).toBe(conversationId)
    expect(payload.runId).toBe(runId)
    expect(payload.sequence).toBe(expectedFirstSequence + index)
    expect(payload.sequence).toBe(Number(id))
    expect(payload.type).toBe(eventType)
    assertTimestamp(payload.createdAt as string, 'RunEvent.createdAt')
    const data = validateEventData(eventType, payload.data, modelProfileId)
    return {
      data,
      eventId: payload.eventId as string,
      sequence: payload.sequence as number,
      type: eventType,
    } satisfies RunEvent
  })
  expect(new Set(events.map((event) => event.eventId)).size).toBe(events.length)
  return events
}

async function readEvents(
  api: APIRequestContext,
  record: RunRecord,
  authority: ReferenceRuntimeAuthority,
  cursor = 0,
) {
  const response = await api.get(
    `/api/v1/conversations/${record.conversationId}/runs/${record.id}/events`,
    cursor > 0 ? { headers: { 'Last-Event-ID': String(cursor) } } : undefined,
  )
  expect(response.status()).toBe(200)
  expect(response.headers()['content-type']).toMatch(/^text\/event-stream(?:;|$)/iu)
  expect(response.headers()['cache-control']).toBe('no-cache')
  return parseSSE(
    await response.text(),
    record.projectId,
    record.conversationId,
    record.id,
    authority.gateway.modelProfile.id,
    cursor + 1,
  )
}

async function executionObservation(
  api: APIRequestContext,
  record: RunRecord,
  authority: ReferenceRuntimeAuthority,
) {
  const subject = goldenSubject()
  assertQualificationOperation(authority, 'run-execution-observation')
  const response = await api.get(`/api/v1/qualification/run-executions/${record.id}`, {
    params: { fixtureId: subject.fixtureId, runId: subject.runId },
  })
  expect(response.status()).toBe(200)
  const observation = strictObject(await response.json(), [
    'applicationId', 'evidenceDigest', 'executionCount', 'faultAdapterResultDigest', 'fixtureId',
    'gatewayIdentity', 'modelProfileId', 'observedAt', 'providerPolicyId',
    'providerRequestIds', 'referenceRunId', 'runId', 'schemaVersion', 'terminalStatus',
  ], [], 'ReferenceRunExecutionObservation')
  expect(observation.schemaVersion).toBe('reference-run-execution-observation/v1')
  expect(observation.fixtureId).toBe(subject.fixtureId)
  expect(observation.runId).toBe(subject.runId)
  expect(observation.applicationId).toBe(subject.reference.applicationId)
  expect(observation.referenceRunId).toBe(record.id)
  expect(observation.gatewayIdentity).toBe(authority.gateway.identity)
  expect(observation.modelProfileId).toBe(authority.gateway.modelProfile.id)
  expect(observation.providerPolicyId).toBe('reference-project-default')
  expect(observation.executionCount).toBe(1)
  expect(Array.isArray(observation.providerRequestIds)).toBe(true)
  expect(observation.providerRequestIds).toHaveLength(1)
  assertUUID((observation.providerRequestIds as string[])[0]!, 'Reference providerRequestId')
  expect(['completed', 'failed', 'cancelled']).toContain(observation.terminalStatus)
  expect(
    observation.faultAdapterResultDigest === null
      || /^sha256:[0-9a-f]{64}$/u.test(String(observation.faultAdapterResultDigest)),
  ).toBe(true)
  assertTimestamp(observation.observedAt as string, 'ReferenceRunExecutionObservation.observedAt')
  await exactQualificationEvidence(
    api,
    observation.evidenceDigest as string,
    evidenceDocument(observation),
    'ReferenceRunExecutionObservation',
  )
  return observation
}

async function migrationRerun(api: APIRequestContext, authority: ReferenceRuntimeAuthority) {
  const subject = goldenSubject()
  assertQualificationOperation(authority, 'migration-rerun')
  const response = await api.post('/api/v1/qualification/operations/migration-rerun', {
    headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-001', 'migration-rerun') },
    data: {
      fixtureId: subject.fixtureId,
      runId: subject.runId,
      schemaVersion: 'reference-migration-rerun-request/v1',
    },
  })
  expect(
    response.status(),
    `real migration rerun operation is required; response=${response.status()} ${await response.text()}`,
  ).toBe(200)
  const receipt = strictObject(await response.json(), [
    'applicationId', 'command', 'deploymentAdmission', 'evidenceDigest', 'executions',
    'deploymentReceipt', 'fixtureId', 'migration', 'operationId', 'runId', 'schemaVersion',
  ], [], 'ReferenceMigrationRerunReceipt')
  expect(receipt.schemaVersion).toBe('reference-migration-rerun-receipt/v1')
  expect(receipt.fixtureId).toBe(subject.fixtureId)
  expect(receipt.runId).toBe(subject.runId)
  expect(receipt.applicationId).toBe(subject.reference.applicationId)
  expect(receipt.deploymentReceipt).toEqual(subject.reference.deploymentReceipt)
  assertUUID(receipt.operationId as string, 'ReferenceMigrationRerunReceipt.operationId')
  expect(receipt.command).toEqual(authority.commands.migration)
  expect(receipt.migration).toEqual(subject.reference.migration)
  assertDigest(receipt.evidenceDigest as string, 'ReferenceMigrationRerunReceipt.evidenceDigest')
  expect(Array.isArray(receipt.executions)).toBe(true)
  expect(receipt.executions).toHaveLength(2)
  const executions = (receipt.executions as unknown[]).map((entry, index) => {
    const execution = strictObject(
      entry,
      ['changed', 'completedAt', 'ordinal', 'schemaDigest'],
      [],
      `ReferenceMigrationRerunReceipt.executions[${index}]`,
    )
    expect(execution.ordinal).toBe(index + 1)
    expect(typeof execution.changed).toBe('boolean')
    assertTimestamp(execution.completedAt as string, `migration execution ${index + 1} completedAt`)
    assertDigest(execution.schemaDigest as string, `migration execution ${index + 1} schemaDigest`)
    return execution
  })
  expect(executions[1]!.changed).toBe(false)
  expect(executions[1]!.schemaDigest).toBe(executions[0]!.schemaDigest)
  const admission = strictObject(
    receipt.deploymentAdmission,
    ['migrationCompletedAt', 'trafficEnabledAt'],
    [],
    'ReferenceMigrationRerunReceipt.deploymentAdmission',
  )
  assertTimestamp(admission.migrationCompletedAt as string, 'deployment migrationCompletedAt')
  assertTimestamp(admission.trafficEnabledAt as string, 'deployment trafficEnabledAt')
  expect(Date.parse(admission.trafficEnabledAt as string)).toBeGreaterThanOrEqual(
    Date.parse(admission.migrationCompletedAt as string),
  )
  expect(admission).toEqual(authority.deploymentAdmission)
  await exactQualificationEvidence(
    api,
    receipt.evidenceDigest as string,
    evidenceDocument(receipt),
    'ReferenceMigrationRerunReceipt',
  )
  return receipt
}

async function armTimeoutVector(
  api: APIRequestContext,
  record: RunRecord,
  authority: ReferenceRuntimeAuthority,
) {
  const subject = goldenSubject()
  assertQualificationOperation(authority, 'timeout-vector')
  const response = await api.post('/api/v1/qualification/runtime-controls/timeout-vector', {
    headers: {
      'Idempotency-Key': qualificationKey(
        'QG-REFERENCE-004',
        `timeout-vector-attempt-${record.attempt}`,
      ),
    },
    data: {
      fixtureId: subject.fixtureId,
      modelProfileId: authority.gateway.modelProfile.id,
      referenceRunId: record.id,
      runId: subject.runId,
      schemaVersion: 'reference-timeout-vector-request/v1',
      timeoutMilliseconds: authority.gateway.modelProfile.timeoutMilliseconds,
    },
  })
  expect(
    response.status(),
    `real gateway timeout vector is required; response=${response.status()} ${await response.text()}`,
  ).toBe(200)
  const receipt = strictObject(await response.json(), [
    'controlId', 'evidenceDigest', 'executionMode', 'expiresAt', 'fixtureId', 'modelProfileId',
    'providerPolicyId', 'referenceRunId', 'runId', 'schemaVersion', 'timeoutMilliseconds',
  ], [], 'ReferenceTimeoutVectorReceipt')
  expect(receipt.schemaVersion).toBe('reference-timeout-vector-receipt/v1')
  expect(receipt.fixtureId).toBe(subject.fixtureId)
  expect(receipt.runId).toBe(subject.runId)
  expect(receipt.referenceRunId).toBe(record.id)
  expect(receipt.modelProfileId).toBe(authority.gateway.modelProfile.id)
  expect(receipt.providerPolicyId).toBe('reference-project-default')
  expect(receipt.timeoutMilliseconds).toBe(authority.gateway.modelProfile.timeoutMilliseconds)
  expect(receipt.executionMode).toBe('live-gateway-deadline')
  assertUUID(receipt.controlId as string, 'ReferenceTimeoutVectorReceipt.controlId')
  assertTimestamp(receipt.expiresAt as string, 'ReferenceTimeoutVectorReceipt.expiresAt')
  await exactQualificationEvidence(
    api,
    receipt.evidenceDigest as string,
    evidenceDocument(receipt),
    'ReferenceTimeoutVectorReceipt',
  )
  return receipt
}

async function forceTimeout(
  api: APIRequestContext,
  record: RunRecord,
  authority: ReferenceRuntimeAuthority,
) {
  const running = await waitRun(
    api,
    record,
    authority,
    (value) => value.status === 'running',
    `Reference Run ${record.id} running before timeout vector`,
  )
  await armTimeoutVector(api, running, authority)
  const failed = await waitRun(
    api,
    record,
    authority,
    (value) => value.status === 'failed',
    `Reference Run ${record.id} model timeout`,
  )
  expect(failed.errorCode).toBe('model_timeout')
  const events = await readEvents(api, failed, authority)
  const terminal = events.filter((event) => terminalEventTypes.includes(
    event.type as typeof terminalEventTypes[number],
  ))
  expect(terminal).toHaveLength(1)
  expect(terminal[0]!.type).toBe('run.failed')
  expect(terminal[0]!.data.errorCode).toBe('model_timeout')
  const observation = await executionObservation(api, failed, authority)
  expect(observation.terminalStatus).toBe('failed')
  return failed
}

async function expectUnauthenticated(response: Awaited<ReturnType<APIRequestContext['get']>>, label: string) {
  expect(response.status(), label).toBe(401)
  expect(response.headers()['content-type']).toMatch(/^application\/json(?:;|$)/iu)
  const error = strictObject(await response.json(), ['code', 'message'], [], `${label} error`)
  expect(error.code).toBe('unauthenticated')
  nonEmptyString(error.message, `${label}.message`)
}

test.describe('Golden Reference AI application external qualification', () => {
  test('QG-REFERENCE-001 verifies exact service images, commands, migration admission, liveness, and readiness', async ({ request }) => {
    const environment = goldenQualificationEnvironment()
    const subject = goldenSubject()
    const apiContext = await referenceAPIContext(environment.credentials.reference.apiA)
    try {
      const authority = await runtimeAuthority(apiContext)
      const web = await request.get(`${subject.reference.webOrigin}/healthz`)
      expect(web.status()).toBe(200)
      expect(web.headers()['x-worksflow-image-digest']).toBe(subject.reference.webImageDigest)
      expect(web.headers()['x-worksflow-deployment-receipt-id']).toBe(subject.reference.deploymentReceipt.id)
      expect(web.headers()['x-worksflow-deployment-receipt-hash']).toBe(subject.reference.deploymentReceipt.contentHash)
      const api = await request.get(`${subject.reference.apiOrigin}/readyz`)
      expect(api.status()).toBe(200)
      expect(api.headers()['x-worksflow-image-digest']).toBe(subject.reference.apiImageDigest)
      expect(api.headers()['x-worksflow-migration-identity']).toBe(subject.reference.migration.identity)
      expect(api.headers()['x-worksflow-migration-content-hash']).toBe(subject.reference.migration.contentHash)
      expect(api.headers()['x-worksflow-contract-bundle-id']).toBe(subject.reference.contractBundle.id)
      expect(api.headers()['x-worksflow-contract-bundle-hash']).toBe(subject.reference.contractBundle.contentHash)
      await migrationRerun(apiContext, authority)
    } finally {
      await apiContext.dispose()
    }
  })

  test('QG-REFERENCE-002 proves persistent idempotent conversation and message create, replay, restart, and read', async ({ browser, request }) => {
    const environment = goldenQualificationEnvironment()
    const subject = goldenSubject()
    const api = await referenceAPIContext(environment.credentials.reference.apiA)
    try {
      const authority = await runtimeAuthority(api)
      const created = await createConversationAndMessage(api, 'QG-REFERENCE-002', 'persistent')
      const replayConversation = await api.post('/api/v1/conversations', {
        headers: { 'Idempotency-Key': created.conversationKey },
        data: created.conversationInput,
      })
      expect(replayConversation.status()).toBe(200)
      expect(replayConversation.headers()['x-idempotent-replay']).toBe('true')
      expect(await replayConversation.json()).toEqual(created.conversation)
      const replayMessage = await api.post(`/api/v1/conversations/${created.conversation.id}/messages`, {
        headers: { 'Idempotency-Key': created.messageKey },
        data: created.messageInput,
      })
      expect(replayMessage.status()).toBe(200)
      expect(replayMessage.headers()['x-idempotent-replay']).toBe('true')
      expect(await replayMessage.json()).toEqual(created.message)

      const queued = await createRun(
        api,
        'QG-REFERENCE-002',
        'persistent',
        created.conversation.id,
        created.message.id,
        authority,
      )
      const replayRun = await api.post(`/api/v1/conversations/${created.conversation.id}/runs`, {
        headers: { 'Idempotency-Key': queued.key },
        data: queued.input,
      })
      expect(replayRun.status()).toBe(200)
      expect(replayRun.headers()['x-idempotent-replay']).toBe('true')
      expect(await replayRun.json()).toEqual(queued.value)
      const terminal = await waitRun(
        api,
        queued.value,
        authority,
        (value) => ['completed', 'failed', 'cancelled'].includes(value.status),
        'Reference idempotent Run terminal state',
      )
      const execution = await executionObservation(api, terminal, authority)
      expect(execution.executionCount).toBe(1)
      expect(execution.faultAdapterResultDigest).toBeNull()

      const restartReceipt = await consumeGoldenFault(request, 'reference-process-restart')
      assertGoldenFaultTarget(restartReceipt, {
        id: subject.reference.applicationId,
        kind: 'reference-application',
        projectId: created.principal.projectId,
      })
      await api.dispose()
      await waitForValue(
        async () => request.get(`${subject.reference.apiOrigin}/readyz`)
          .then((response) => response.status())
          .catch(() => 0),
        (status) => status === 200,
        'Reference API readiness after real process restart',
      )
      const afterRestart = await referenceAPIContext(environment.credentials.reference.apiA)
      try {
        const conversations = await afterRestart.get('/api/v1/conversations')
        expect(conversations.status()).toBe(200)
        expect((await conversations.json() as unknown[]).map((entry) => (
          conversation(entry, created.principal.projectId)
        ))).toContainEqual(created.conversation)
        const messages = await afterRestart.get(`/api/v1/conversations/${created.conversation.id}/messages`)
        expect(messages.status()).toBe(200)
        expect((await messages.json() as unknown[]).map((entry) => (
          message(entry, created.principal.projectId, created.conversation.id)
        ))).toContainEqual(created.message)
        const persistedRun = await readRun(
          afterRestart,
          created.conversation.id,
          terminal.id,
          created.principal.projectId,
          authority,
        )
        expect(persistedRun).toEqual(terminal)
      } finally {
        await afterRestart.dispose()
      }

      const context = await browser.newContext({
        storageState: browserStorageState(environment.credentials.reference.browserA),
      })
      try {
        const page = await context.newPage()
        await page.goto(`${subject.reference.webOrigin}/conversations`)
        await expect(page.getByText(created.conversation.title, { exact: true })).toBeVisible()
        await expect(page.getByText(created.message.content, { exact: true })).toBeVisible()
      } finally {
        await context.close()
      }
    } finally {
      await api.dispose().catch(() => undefined)
    }
  })

  test('QG-REFERENCE-003 verifies typed monotonic SSE cursor reconnect and durable terminal recovery', async () => {
    const api = await referenceAPIContext(goldenQualificationEnvironment().credentials.reference.apiA)
    try {
      const authority = await runtimeAuthority(api)
      const created = await createConversationAndMessage(api, 'QG-REFERENCE-003', 'stream')
      const queued = await createRun(
        api,
        'QG-REFERENCE-003',
        'stream',
        created.conversation.id,
        created.message.id,
        authority,
      )
      const terminalRun = await waitRun(
        api,
        queued.value,
        authority,
        (value) => ['completed', 'failed', 'cancelled'].includes(value.status),
        'Reference streamed Run terminal state',
      )
      const events = await readEvents(api, terminalRun, authority)
      expect(events.length).toBeGreaterThanOrEqual(3)
      expect(events[0]!.type).toBe('run.queued')
      expect(events.some((event) => event.type === 'run.started')).toBe(true)
      const terminal = events.filter((event) => terminalEventTypes.includes(
        event.type as typeof terminalEventTypes[number],
      ))
      expect(terminal).toHaveLength(1)
      const statusByTerminalType: Partial<Record<RunEvent['type'], RunRecord['status']>> = {
        'run.cancelled': 'cancelled',
        'run.completed': 'completed',
        'run.failed': 'failed',
      }
      const expectedStatus = statusByTerminalType[terminal[0]!.type]
      expect(expectedStatus).toBeTruthy()
      expect(terminalRun.status).toBe(expectedStatus)

      const cursor = events[1]!.sequence
      const resumed = await readEvents(api, terminalRun, authority, cursor)
      expect(resumed[0]!.sequence).toBe(cursor + 1)
      expect(resumed).toEqual(events.filter((event) => event.sequence > cursor))
      const durable = await readRun(
        api,
        terminalRun.conversationId,
        terminalRun.id,
        terminalRun.projectId,
        authority,
      )
      expect(durable).toEqual(terminalRun)
    } finally {
      await api.dispose()
    }
  })

  test('QG-REFERENCE-004 proves cancel, bounded reasoned retry, timeout, and exactly one terminal state', async () => {
    const api = await referenceAPIContext(goldenQualificationEnvironment().credentials.reference.apiA)
    try {
      const authority = await runtimeAuthority(api)
      const cancellation = await createConversationAndMessage(api, 'QG-REFERENCE-004', 'cancel')
      const cancellable = await createRun(
        api,
        'QG-REFERENCE-004',
        'cancel',
        cancellation.conversation.id,
        cancellation.message.id,
        authority,
      )
      const nonTerminal = await waitRun(
        api,
        cancellable.value,
        authority,
        (value) => value.status === 'queued' || value.status === 'running',
        'Reference cancellable Run non-terminal state',
      )
      const cancellationKey = qualificationKey('QG-REFERENCE-004', 'cancel-request')
      const cancelledResponse = await api.post(
        `/api/v1/conversations/${nonTerminal.conversationId}/runs/${nonTerminal.id}/cancel`,
        { headers: { 'Idempotency-Key': cancellationKey } },
      )
      expect(cancelledResponse.status()).toBe(202)
      run(
        await cancelledResponse.json(),
        nonTerminal.projectId,
        nonTerminal.conversationId,
        authority.gateway.modelProfile.id,
      )
      const replayedCancellation = await api.post(
        `/api/v1/conversations/${nonTerminal.conversationId}/runs/${nonTerminal.id}/cancel`,
        { headers: { 'Idempotency-Key': cancellationKey } },
      )
      expect(replayedCancellation.status()).toBe(200)
      expect(replayedCancellation.headers()['x-idempotent-replay']).toBe('true')
      const cancelled = await waitRun(
        api,
        nonTerminal,
        authority,
        (value) => value.status === 'cancelled',
        'Reference cancellation durable state',
      )
      expect(
        run(
          await replayedCancellation.json(),
          cancelled.projectId,
          cancelled.conversationId,
          authority.gateway.modelProfile.id,
        ).id,
      ).toBe(cancelled.id)
      const cancellationEvents = await readEvents(api, cancelled, authority)
      expect(cancellationEvents.filter((event) => event.type === 'run.cancelled')).toHaveLength(1)
      expect(cancellationEvents.filter((event) => terminalEventTypes.includes(
        event.type as typeof terminalEventTypes[number],
      ))).toHaveLength(1)
      const outputAtCancel = cancellationEvents
        .filter((event) => event.type === 'output.delta')
        .map((event) => event.data.text)
      await expect.poll(async () => (await readEvents(api, cancelled, authority))
        .filter((event) => event.type === 'output.delta')
        .map((event) => event.data.text), { timeout: 5_000 }).toEqual(outputAtCancel)
      expect(await readRun(
        api,
        cancelled.conversationId,
        cancelled.id,
        cancelled.projectId,
        authority,
      )).toEqual(cancelled)

      const timeoutVector = await createConversationAndMessage(api, 'QG-REFERENCE-004', 'timeout')
      let attempt = (await createRun(
        api,
        'QG-REFERENCE-004',
        'timeout',
        timeoutVector.conversation.id,
        timeoutVector.message.id,
        authority,
      )).value
      expect(attempt.attempt).toBe(1)
      for (let expectedAttempt = 1; expectedAttempt <= authority.gateway.modelProfile.maxAttempts; expectedAttempt += 1) {
        expect(attempt.attempt).toBe(expectedAttempt)
        const failed = await forceTimeout(api, attempt, authority)
        if (expectedAttempt === 1) {
          const emptyReason = await api.post(
            `/api/v1/conversations/${failed.conversationId}/runs/${failed.id}/retry`,
            {
              headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-004', 'empty-reason') },
              data: { reason: '' },
            },
          )
          expect(emptyReason.status()).toBe(400)
          const error = strictObject(await emptyReason.json(), ['code', 'message'], [], 'empty retry reason error')
          expect(error.code).toBe('retry_reason_required')
        }
        if (expectedAttempt < authority.gateway.modelProfile.maxAttempts) {
          const reason = `Golden explicit timeout recovery attempt ${expectedAttempt + 1}`
          const retried = await api.post(
            `/api/v1/conversations/${failed.conversationId}/runs/${failed.id}/retry`,
            {
              headers: {
                'Idempotency-Key': qualificationKey(
                  'QG-REFERENCE-004',
                  `reasoned-retry-${expectedAttempt + 1}`,
                ),
              },
              data: { reason },
            },
          )
          expect(retried.status()).toBe(202)
          attempt = run(
            await retried.json(),
            failed.projectId,
            failed.conversationId,
            authority.gateway.modelProfile.id,
          )
          expect(attempt.retryOfRunId).toBe(failed.id)
          expect(attempt.attempt).toBe(expectedAttempt + 1)
        }
      }
      const runsBeforeLimit = await api.get(`/api/v1/conversations/${attempt.conversationId}/runs`)
      expect(runsBeforeLimit.status()).toBe(200)
      const before = (await runsBeforeLimit.json() as unknown[]).map((entry) => (
        run(entry, attempt.projectId, attempt.conversationId, authority.gateway.modelProfile.id)
      ))
      const beyondLimit = await api.post(
        `/api/v1/conversations/${attempt.conversationId}/runs/${attempt.id}/retry`,
        {
          headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-004', 'retry-beyond-limit') },
          data: { reason: 'This attempt must be rejected by the exact configured maximum' },
        },
      )
      expect(beyondLimit.status()).toBe(409)
      const limitError = strictObject(await beyondLimit.json(), ['code', 'message'], [], 'max attempts error')
      expect(limitError.code).toBe('max_attempts_exceeded')
      const runsAfterLimit = await api.get(`/api/v1/conversations/${attempt.conversationId}/runs`)
      expect(runsAfterLimit.status()).toBe(200)
      const after = (await runsAfterLimit.json() as unknown[]).map((entry) => (
        run(entry, attempt.projectId, attempt.conversationId, authority.gateway.modelProfile.id)
      ))
      expect(after).toEqual(before)
      expect(after).toHaveLength(authority.gateway.modelProfile.maxAttempts)
    } finally {
      await api.dispose()
    }
  })

  test('QG-REFERENCE-005 proves independent A and B tenant isolation with redacted actor-bound audit', async ({ request }) => {
    const environment = goldenQualificationEnvironment()
    const subject = goldenSubject()
    const apiA = await referenceAPIContext(environment.credentials.reference.apiA)
    const apiB = await referenceAPIContext(environment.credentials.reference.apiB)
    try {
      const authority = await runtimeAuthority(apiA)
      const created = await createConversationAndMessage(apiA, 'QG-REFERENCE-005', 'tenant')
      const createdRun = await createRun(
        apiA,
        'QG-REFERENCE-005',
        'tenant',
        created.conversation.id,
        created.message.id,
        authority,
      )
      for (const path of [
        `/api/v1/conversations/${created.conversation.id}`,
        `/api/v1/conversations/${created.conversation.id}/messages`,
        `/api/v1/conversations/${created.conversation.id}/runs`,
        `/api/v1/conversations/${created.conversation.id}/runs/${createdRun.value.id}`,
        `/api/v1/conversations/${created.conversation.id}/runs/${createdRun.value.id}/events`,
      ]) expect((await apiB.get(path)).status()).toBe(404)
      expect((await apiB.post(`/api/v1/conversations/${created.conversation.id}/messages`, {
        headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-005', 'tenant-b-message') },
        data: { clientMessageId: qualificationKey('QG-REFERENCE-005', 'tenant-b-client-message'), content: 'hidden' },
      })).status()).toBe(404)
      expect((await apiB.post(`/api/v1/conversations/${created.conversation.id}/runs`, {
        headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-005', 'tenant-b-run') },
        data: createdRun.input,
      })).status()).toBe(404)
      expect((await apiB.post(
        `/api/v1/conversations/${created.conversation.id}/runs/${createdRun.value.id}/cancel`,
        { headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-005', 'tenant-b-cancel') } },
      )).status()).toBe(404)
      expect((await apiB.post(
        `/api/v1/conversations/${created.conversation.id}/runs/${createdRun.value.id}/retry`,
        {
          headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-005', 'tenant-b-retry') },
          data: { reason: 'Cross-tenant operation must remain undisclosed' },
        },
      )).status()).toBe(404)

      const unauthenticatedPaths = [
        ['get', '/api/v1/conversations', undefined],
        ['post', '/api/v1/conversations', { title: 'unauthenticated' }],
        ['get', `/api/v1/conversations/${created.conversation.id}`, undefined],
        ['get', `/api/v1/conversations/${created.conversation.id}/messages`, undefined],
        ['post', `/api/v1/conversations/${created.conversation.id}/messages`, created.messageInput],
        ['get', `/api/v1/conversations/${created.conversation.id}/runs`, undefined],
        ['post', `/api/v1/conversations/${created.conversation.id}/runs`, createdRun.input],
        ['get', `/api/v1/conversations/${created.conversation.id}/runs/${createdRun.value.id}`, undefined],
        ['get', `/api/v1/conversations/${created.conversation.id}/runs/${createdRun.value.id}/events`, undefined],
        ['post', `/api/v1/conversations/${created.conversation.id}/runs/${createdRun.value.id}/cancel`, undefined],
        ['post', `/api/v1/conversations/${created.conversation.id}/runs/${createdRun.value.id}/retry`, { reason: 'required' }],
      ] as const
      for (const [method, path, data] of unauthenticatedPaths) {
        const response = method === 'get'
          ? await request.get(`${subject.reference.apiOrigin}${path}`)
          : await request.post(`${subject.reference.apiOrigin}${path}`, {
              headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-005', `unauth-${path}`) },
              ...(data === undefined ? {} : { data }),
            })
        await expectUnauthenticated(response, `unauthenticated ${method.toUpperCase()} ${path}`)
      }

      assertQualificationOperation(authority, 'reference-audit-observation')
      const audit = await apiA.get('/api/v1/qualification/audit', {
        params: { conversationId: created.conversation.id, fixtureId: subject.fixtureId, runId: subject.runId },
      })
      expect(audit.status()).toBe(200)
      const entries = await audit.json() as unknown[]
      expect(entries.length).toBeGreaterThanOrEqual(3)
      for (const [index, entry] of entries.entries()) {
        const value = strictObject(entry, [
          'action', 'actorId', 'conversationId', 'createdAt', 'details', 'errorCode',
          'id', 'outcome', 'projectId', 'providerPolicyId', 'requestId', 'runId',
          'tenantId', 'usage',
        ], [], `AuditEvent[${index}]`)
        assertUUID(value.id as string, `AuditEvent[${index}].id`)
        assertUUID(value.requestId as string, `AuditEvent[${index}].requestId`)
        expect(value.actorId).toBe(created.principal.actorId)
        expect(value.projectId).toBe(created.principal.projectId)
        expect(value.tenantId).toBe(created.principal.tenantId)
        expect(value.conversationId).toBe(created.conversation.id)
        expect(value.providerPolicyId === null || value.providerPolicyId === 'reference-project-default').toBe(true)
        assertTimestamp(value.createdAt as string, `AuditEvent[${index}].createdAt`)
        expect(JSON.stringify(value)).not.toMatch(/authorization|cookie|prompt|provider[_-]?key|response|secret|token/iu)
      }
    } finally {
      await Promise.all([apiA.dispose(), apiB.dispose()])
    }
  })

  test('QG-REFERENCE-006 closes real gateway outage, rate limiting, retention binding, and diagnostic redaction', async ({ browser, request }) => {
    const environment = goldenQualificationEnvironment()
    const subject = goldenSubject()
    const api = await referenceAPIContext(environment.credentials.reference.apiA)
    try {
      const authority = await runtimeAuthority(api)
      const created = await createConversationAndMessage(api, 'QG-REFERENCE-006', 'gateway')
      const gatewayRun = await createRun(
        api,
        'QG-REFERENCE-006',
        'gateway',
        created.conversation.id,
        created.message.id,
        authority,
      )
      const running = await waitRun(
        api,
        gatewayRun.value,
        authority,
        (value) => value.status === 'running',
        'Reference Run running before gateway outage',
      )
      const outageReceipt = await consumeGoldenFault(request, 'reference-gateway-outage')
      assertGoldenFaultTarget(outageReceipt, {
        id: running.id,
        kind: 'reference-run',
        projectId: running.projectId,
      })
      const failed = await waitRun(
        api,
        running,
        authority,
        (value) => value.status === 'failed',
        'Reference gateway-outage failed Run',
      )
      expect(failed.errorCode).toMatch(/gateway|provider|timeout/iu)
      const failedEvents = await readEvents(api, failed, authority)
      expect(failedEvents.filter((event) => event.type === 'run.failed')).toHaveLength(1)
      const gatewayExecution = await executionObservation(api, failed, authority)
      expect(gatewayExecution.faultAdapterResultDigest).toBe(outageReceipt.adapterResultDigest)

      assertQualificationOperation(authority, 'retention-job')
      const retentionResponse = await api.post('/api/v1/qualification/operations/retention-job', {
        headers: { 'Idempotency-Key': qualificationKey('QG-REFERENCE-006', 'retention-job') },
        data: {
          fixtureId: subject.fixtureId,
          runId: subject.runId,
          schemaVersion: 'reference-retention-job-request/v1',
        },
      })
      expect(
        retentionResponse.status(),
        `real retention job operation is required; response=${retentionResponse.status()} ${await retentionResponse.text()}`,
      ).toBe(200)
      const retention = strictObject(await retentionResponse.json(), [
        'applicationId', 'command', 'evidenceDigest', 'fixtureId', 'job', 'operationId',
        'policy', 'readback', 'runId', 'schemaVersion', 'seeded',
      ], [], 'ReferenceRetentionJobReceipt')
      expect(retention.schemaVersion).toBe('reference-retention-job-receipt/v1')
      expect(retention.fixtureId).toBe(subject.fixtureId)
      expect(retention.runId).toBe(subject.runId)
      expect(retention.applicationId).toBe(subject.reference.applicationId)
      expect(retention.command).toEqual(authority.commands.retention)
      expect(retention.policy).toEqual(authority.retentionPolicy)
      assertUUID(retention.operationId as string, 'ReferenceRetentionJobReceipt.operationId')
      assertDigest(retention.evidenceDigest as string, 'ReferenceRetentionJobReceipt.evidenceDigest')
      const seeded = strictObject(retention.seeded, ['expired', 'live'], [], 'ReferenceRetentionJobReceipt.seeded')
      for (const kind of ['expired', 'live'] as const) {
        const values = strictObject(
          seeded[kind],
          ['auditId', 'eventId', 'messageId', 'runId'],
          [],
          `ReferenceRetentionJobReceipt.seeded.${kind}`,
        )
        for (const [key, value] of Object.entries(values)) assertUUID(value as string, `seeded.${kind}.${key}`)
      }
      const job = strictObject(
        retention.job,
        ['completedAt', 'deleted', 'redacted', 'startedAt'],
        [],
        'ReferenceRetentionJobReceipt.job',
      )
      assertTimestamp(job.startedAt as string, 'retention job startedAt')
      assertTimestamp(job.completedAt as string, 'retention job completedAt')
      expect(Date.parse(job.completedAt as string)).toBeGreaterThanOrEqual(Date.parse(job.startedAt as string))
      expect(strictObject(job.deleted, ['events', 'messages', 'runs'], [], 'retention deleted')).toEqual({
        events: 1,
        messages: 1,
        runs: 1,
      })
      expect(strictObject(job.redacted, ['audits', 'diagnostics'], [], 'retention redacted')).toEqual({
        audits: 1,
        diagnostics: 1,
      })
      expect(strictObject(retention.readback, [
        'expiredEventStatus', 'expiredMessageStatus', 'expiredRunStatus',
        'liveEventStatus', 'liveMessageStatus', 'liveRunStatus', 'secretMatches',
      ], [], 'ReferenceRetentionJobReceipt.readback')).toEqual({
        expiredEventStatus: 410,
        expiredMessageStatus: 404,
        expiredRunStatus: 404,
        liveEventStatus: 200,
        liveMessageStatus: 200,
        liveRunStatus: 200,
        secretMatches: 0,
      })
      await exactQualificationEvidence(
        api,
        retention.evidenceDigest as string,
        evidenceDocument(retention),
        'ReferenceRetentionJobReceipt',
      )

      let limitedResponse: Awaited<ReturnType<APIRequestContext['get']>> | undefined
      let rejectedAt = 0
      const declaredMaximum = authority.rateLimit.requests + authority.rateLimit.burst + 1
      for (let index = 1; index <= declaredMaximum; index += 1) {
        const response = await api.get('/api/v1/conversations')
        if (response.status() === 429) {
          limitedResponse = response
          rejectedAt = index
          break
        }
        expect(response.status()).toBe(200)
      }
      expect(limitedResponse, 'real per-actor limiter must reject within its declared bound').toBeTruthy()
      expect(rejectedAt).toBeGreaterThan(0)
      expect(rejectedAt).toBeLessThanOrEqual(declaredMaximum)
      expect(limitedResponse!.headers()['retry-after']).toMatch(/^[1-9]\d*$/u)
      const error = strictObject(
        await limitedResponse!.json(),
        ['code', 'message', 'retryAfterSeconds'],
        [],
        'RateLimitError',
      )
      expect(error.code).toBe('rate_limited')
      expect(error.retryAfterSeconds).toBe(Number(limitedResponse!.headers()['retry-after']))
      assertQualificationOperation(authority, 'rate-limit-observation')
      const limiterObservation = await api.get('/api/v1/qualification/rate-limit-observations', {
        params: {
          actorId: created.principal.actorId,
          fixtureId: subject.fixtureId,
          projectId: created.principal.projectId,
          runId: subject.runId,
        },
      })
      expect(limiterObservation.status()).toBe(200)
      const limiter = strictObject(await limiterObservation.json(), [
        'actorId', 'actorRejected', 'evidenceDigest', 'fixtureId', 'projectId',
        'projectRejected', 'runId', 'schemaVersion', 'window',
      ], [], 'ReferenceRateLimitObservation')
      expect(limiter.schemaVersion).toBe('reference-rate-limit-observation/v1')
      expect(limiter.fixtureId).toBe(subject.fixtureId)
      expect(limiter.runId).toBe(subject.runId)
      expect(limiter.actorId).toBe(created.principal.actorId)
      expect(limiter.projectId).toBe(created.principal.projectId)
      expect(limiter.actorRejected).toBe(true)
      expect(limiter.projectRejected).toBe(true)
      expect(limiter.window).toEqual(authority.rateLimit)
      assertDigest(limiter.evidenceDigest as string, 'ReferenceRateLimitObservation.evidenceDigest')
      await exactQualificationEvidence(
        api,
        limiter.evidenceDigest as string,
        evidenceDocument(limiter),
        'ReferenceRateLimitObservation',
      )

      const context = await browser.newContext({
        storageState: browserStorageState(environment.credentials.reference.browserA),
      })
      try {
        const page = await context.newPage()
        await page.goto(`${subject.reference.webOrigin}/conversations/${created.conversation.id}`)
        await expect(page.locator('body')).not.toContainText(/authorization|cookie|provider[_-]?key|secret|token/iu)
      } finally {
        await context.close()
      }
      expect(JSON.stringify({ failed, failedEvents, gatewayExecution, retention, limiter }))
        .not.toMatch(/authorization|cookie|provider[_-]?key|secret[_-]?(?:key|value)|token/iu)
    } finally {
      await api.dispose()
    }
  })
})
