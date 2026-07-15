import assert from 'node:assert/strict'
import { createEmptyPageSpecContent } from '../lib/platform/artifact-workspace'
import {
  normalizePageSpecContent,
  pageSpecReviewIssues,
} from '../lib/platform/page-spec-content'

type TestCase = {
  readonly name: string
  readonly run: () => void | Promise<void>
}

const tests: TestCase[] = []

function test(name: string, run: TestCase['run']) {
  tests.push({ name, run })
}

test('new PageSpec content carries Blueprint route and goal with four stable states', () => {
  const content = createEmptyPageSpecContent(
    'page-node-orders',
    'Orders PageSpec',
    '/orders',
    'Review and resolve order exceptions.',
  )

  assert.equal(content.blueprintPageNodeId, 'page-node-orders')
  assert.equal(content.route, '/orders')
  assert.equal(content.userGoal, 'Review and resolve order exceptions.')
  assert.deepEqual(content.states.map(({ id, key, required }) => ({ id, key, required })), [
    { id: 'ready', key: 'ready', required: true },
    { id: 'loading', key: 'loading', required: true },
    { id: 'empty', key: 'empty', required: true },
    { id: 'error', key: 'error', required: true },
  ])
})

test('PageSpec review gate accepts a complete canonical content contract', () => {
  const base = createEmptyPageSpecContent(
    'page-node-orders',
    'Orders PageSpec',
    '/orders',
    'Review and resolve order exceptions.',
  )
  const content = {
    ...base,
    states: base.states.map((state) => ({ ...state, id: `state-${state.key}` })),
    acceptanceCriterionIds: ['AC-ORDER-001'],
    dataBindings: [{
      id: 'binding-orders',
      name: 'Orders',
      source: 'api' as const,
      operationId: 'orders.list',
      schema: { type: 'array' },
      required: true,
    }],
    interactions: [{
      id: 'interaction-open-order',
      trigger: 'Select an order',
      outcome: 'Open order details',
      acceptanceCriterionIds: ['AC-ORDER-001'],
    }],
  }

  assert.deepEqual(pageSpecReviewIssues(content), [])
  assert.deepEqual(normalizePageSpecContent({
    ...content,
    requiredRoles: [' editor ', 'editor', 'admin'],
  }).requiredRoles, ['editor', 'admin'])
})

test('PageSpec normalization migrates legacy navigation targets to Blueprint page node IDs', () => {
  const content = createEmptyPageSpecContent(
    'page-node-orders',
    'Orders PageSpec',
    '/orders',
    'Review and resolve order exceptions.',
  )
  const normalized = normalizePageSpecContent({
    ...content,
    interactions: [{
      id: 'interaction-checkout',
      trigger: 'Click checkout',
      outcome: 'Open checkout',
      targetPageSpecId: 'page-node-checkout',
      acceptanceCriterionIds: [],
    }],
  })

  assert.equal(normalized.interactions[0]?.targetPageNodeId, 'page-node-checkout')
  assert.equal(Object.hasOwn(normalized.interactions[0] ?? {}, 'targetPageSpecId'), false)
})

test('PageSpec normalization preserves forward-compatible Proposal fields', () => {
  const content = createEmptyPageSpecContent(
    'page-node-orders',
    'Orders PageSpec',
    '/orders',
    'Review and resolve order exceptions.',
  )
  const forwardCompatible = {
    ...content,
    schemaVersion: 1,
    requirementIds: ['REQ-ORDER-001'],
    states: content.states.map((state) => ({
      ...state,
      transitionPolicy: 'server-authoritative',
    })),
  } as typeof content & {
    readonly schemaVersion: number
    readonly requirementIds: readonly string[]
  }

  const normalized = normalizePageSpecContent(forwardCompatible) as typeof forwardCompatible

  assert.equal(normalized.schemaVersion, 1)
  assert.deepEqual(normalized.requirementIds, ['REQ-ORDER-001'])
  assert.equal(
    (normalized.states[0] as typeof normalized.states[0] & { transitionPolicy?: string })
      .transitionPolicy,
    'server-authoritative',
  )
})

test('PageSpec review gate blocks missing stable states, traces, and binding identity', () => {
  const content = {
    ...createEmptyPageSpecContent('page-node-orders', 'Orders', 'orders', ''),
    states: [{
      id: 'ready',
      key: 'ready',
      title: '',
      required: false,
      fixtureIds: [],
      acceptanceCriterionIds: [],
    }],
    dataBindings: [{
      id: 'binding-orders',
      name: '',
      source: 'api' as const,
      required: true,
    }],
  }
  const issues = pageSpecReviewIssues(content)

  assert.ok(issues.some((issue) => issue.includes('Route')))
  assert.ok(issues.some((issue) => issue.includes('User goal')))
  assert.ok(issues.some((issue) => issue.includes('loading')))
  assert.ok(issues.some((issue) => issue.includes('marked required')))
  assert.ok(issues.some((issue) => issue.includes('operation ID')))
  assert.ok(issues.some((issue) => issue.includes('acceptance criterion')))
})

async function main() {
  let failed = 0
  for (const { name, run } of tests) {
    try {
      await run()
      console.log(`✓ ${name}`)
    } catch (error) {
      failed += 1
      console.error(`✗ ${name}`)
      console.error(error)
    }
  }
  if (failed > 0) process.exitCode = 1
}

void main()
