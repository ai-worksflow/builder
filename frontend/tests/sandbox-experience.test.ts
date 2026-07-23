import assert from 'node:assert/strict'
import {
  preferredPreviewPort,
  sandboxPreviewStage,
} from '../lib/platform/sandbox-experience'
import type {
  SandboxPortListDto,
  SandboxPreviewLinkDto,
  SandboxProcessDto,
} from '../lib/platform/sandbox-contract'

const process = (state: SandboxProcessDto['state']) => ({ state }) as SandboxProcessDto
const port = (
  name: string,
  healthy: boolean,
  previewable = true,
) => ({ name, healthy, previewable }) as SandboxPortListDto['ports'][number]

assert.equal(sandboxPreviewStage(null, [], null), 'not-started')
assert.equal(sandboxPreviewStage(process('starting'), [], null), 'starting')
assert.equal(sandboxPreviewStage(process('running'), [port('web', false)], null), 'waiting-for-port')
assert.equal(sandboxPreviewStage(process('failed'), [], null), 'stopped')

const healthyWeb = port('web', true)
const healthyApi = port('api', true)
assert.equal(sandboxPreviewStage(process('running'), [healthyWeb], null), 'ready')
assert.equal(preferredPreviewPort([port('metrics', true, false), healthyWeb, healthyApi], null)?.name, 'web')

const preview = {
  port: healthyWeb,
} as SandboxPreviewLinkDto
assert.equal(sandboxPreviewStage(process('running'), [healthyWeb], preview), 'ready')
assert.equal(sandboxPreviewStage(process('failed'), [healthyWeb], preview), 'stopped')
assert.equal(preferredPreviewPort([healthyWeb, healthyApi], preview), undefined)
assert.equal(
  preferredPreviewPort([port('web', false), healthyApi], preview)?.name,
  'api',
  'an unhealthy selected port must fall forward to the next healthy declared port',
)
