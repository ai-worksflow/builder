import assert from 'node:assert/strict'
import {
  DOC_DEPENDENCIES,
  IMPORT_ASSETS,
  TEAM_DOCUMENTS,
  VERSIONS,
} from '../lib/worksflow/mock-data'
import {
  INITIAL_TEAM_PROJECTS,
  blueprintFromDocuments,
  buildTeamProject,
  collectBlueprintSelection,
  computeBlueprintNodeMissing,
  createBlueprintWorkbenchContextDraft,
  docsFromBlueprint,
  generatedBlueprint,
  syncWorkbenchResultToBlueprint,
  syncWorkbenchResultToDocuments,
} from '../lib/worksflow/project-model'

type TestCase = {
  name: string
  run: () => void
}

const tests: TestCase[] = []

function test(name: string, run: () => void) {
  tests.push({ name, run })
}

function assertHasAll<T>(actual: T[], expected: T[]) {
  expected.forEach((item) => assert.ok(actual.includes(item), `missing ${String(item)}`))
}

test('initial team projects keep CRM and blank Billing data isolated', () => {
  const crm = INITIAL_TEAM_PROJECTS.find((project) => project.id === 'tp-crm')
  const billing = INITIAL_TEAM_PROJECTS.find((project) => project.id === 'tp-billing')

  assert.ok(crm)
  assert.ok(billing)
  assert.equal(crm.documents.length, 7)
  assert.equal(crm.importAssets.length, IMPORT_ASSETS.length)
  assert.equal(crm.blueprint.title, 'CRM Rewrite Blueprint')
  assert.equal(billing.documents.length, 0)
  assert.equal(billing.dependencies.length, 0)
  assert.equal(billing.importAssets.length, 0)
  assert.equal(billing.blueprint.nodes.length, 0)
  assert.equal(billing.phase, 'Empty graph')
})

test('buildTeamProject does not inherit template assets unless explicitly provided', () => {
  const blank = buildTeamProject('tp-test', 'Test Project', 'blank')
  assert.equal(blank.documents.length, 0)
  assert.equal(blank.importAssets.length, 0)

  const templated = buildTeamProject('tp-template', 'Template Project', 'template', {
    documents: TEAM_DOCUMENTS,
    dependencies: DOC_DEPENDENCIES,
    importAssets: IMPORT_ASSETS,
  })
  assert.equal(templated.documents.length, TEAM_DOCUMENTS.length)
  assert.equal(templated.importAssets.length, IMPORT_ASSETS.length)

  templated.documents[0].title = 'Mutated title'
  assert.notEqual(TEAM_DOCUMENTS[0].title, 'Mutated title')
})

test('generatedBlueprint creates the full product capability structure', () => {
  const blueprint = generatedBlueprint(
    'bp-vendor',
    'Vendor Onboarding',
    'Create vendor onboarding with approvals, API, data model, permissions and prototype handoff.',
  )
  const nodeTypes = blueprint.nodes.map((node) => node.type)
  const edgeTypes = blueprint.edges.map((edge) => edge.type)

  assert.equal(blueprint.status, 'draft')
  assertHasAll(nodeTypes, [
    'feature',
    'page',
    'component',
    'api',
    'dataModel',
    'permission',
    'prototype',
  ])
  assertHasAll(edgeTypes, ['contains', 'uses', 'calls', 'reads', 'requires', 'syncs_with'])
  assert.equal(blueprint.nodes.find((node) => node.type === 'page')?.title, '/vendor-onboarding')

  const prototype = blueprint.nodes.find((node) => node.type === 'prototype')
  assert.ok(prototype)
  assert.deepEqual(computeBlueprintNodeMissing(prototype, blueprint), ['No imported prototype source'])
})

test('docsFromBlueprint generates documents, dependencies and node bindings', () => {
  const blueprint = generatedBlueprint('bp-billing', 'Billing Console')
  const graph = docsFromBlueprint(blueprint)

  assert.equal(graph.blueprint.status, 'docsGenerated')
  assert.equal(graph.documents.length, blueprint.nodes.length)
  assert.equal(graph.dependencies.length, blueprint.edges.length)
  assert.equal(graph.nodeBindings.length, blueprint.nodes.length)
  assert.deepEqual(
    graph.blueprint.generatedDocIds,
    graph.documents.map((doc) => doc.id),
  )

  const apiDoc = graph.documents.find((doc) => doc.title.includes('GET /api/billing-console'))
  const prototypeDoc = graph.documents.find((doc) => doc.type === 'uiPrototype')
  assert.ok(apiDoc)
  assert.equal(apiDoc.type, 'apiContract')
  assert.ok(prototypeDoc)
  assert.equal(prototypeDoc.externalSync, 'figma')

  graph.blueprint.nodes.forEach((node) => {
    assert.equal(node.boundDocumentIds.length, 1)
    assert.equal(node.generatedDocIds.length, 1)
    assert.deepEqual(node.missing, [])
  })
})

test('blueprintFromDocuments infers a blueprint from existing mock documents', () => {
  const inferred = blueprintFromDocuments('bp-inferred', 'CRM Rewrite', TEAM_DOCUMENTS, DOC_DEPENDENCIES)

  assert.equal(inferred.nodes.length, TEAM_DOCUMENTS.length)
  assert.ok(inferred.edges.length > 0)
  assertHasAll(
    inferred.nodes.map((node) => node.type),
    ['feature', 'page', 'api', 'prototype', 'workbenchTarget'],
  )
  TEAM_DOCUMENTS.forEach((doc) => {
    assert.ok(
      inferred.nodes.some((node) => node.boundDocumentIds.includes(doc.id)),
      `document ${doc.id} was not bound back to a blueprint node`,
    )
  })
})

test('Workbench context uses selected one-hop blueprint subgraph and linked docs', () => {
  const generated = docsFromBlueprint(generatedBlueprint('bp-workbench', 'Workbench Scope'))
  const selected = generated.blueprint.nodes.find((node) => node.type === 'feature')
  assert.ok(selected)

  const selection = collectBlueprintSelection(generated.blueprint, selected.id)
  assertHasAll(
    selection.nodes.map((node) => node.type),
    ['feature', 'page', 'component', 'permission', 'prototype'],
  )
  assert.equal(selection.nodes.some((node) => node.type === 'api'), false)

  const draft = createBlueprintWorkbenchContextDraft({
    blueprint: generated.blueprint,
    documents: generated.documents,
    selectedNodeId: selected.id,
    idSeed: 'mock',
  })
  assert.ok(draft)
  assert.equal(draft.blueprint.status, 'inImplementation')
  assert.equal(draft.context.status, 'inImplementation')
  assert.equal(draft.context.workbenchTargetId, 'b-target-mock')
  assert.equal(draft.context.prototypeNodeIds.length, 1)
  assert.ok(draft.context.linkedDocIds.length > 0)
  assert.ok(draft.prompt.includes('Use Blueprint selection "Workbench Scope Core Flow"'))
  assert.ok(draft.prompt.includes('Team documents to read:'))
  assert.ok(
    draft.blueprint.edges.some(
      (edge) =>
        edge.sourceNodeId === selected.id &&
        edge.targetNodeId === draft.context.workbenchTargetId &&
        edge.type === 'implemented_by',
    ),
  )
})

test('Sync back updates linked docs and Blueprint Workbench Target', () => {
  const generated = docsFromBlueprint(generatedBlueprint('bp-sync', 'Sync Back'))
  const selected = generated.blueprint.nodes.find((node) => node.type === 'feature')
  assert.ok(selected)
  const draft = createBlueprintWorkbenchContextDraft({
    blueprint: generated.blueprint,
    documents: generated.documents,
    selectedNodeId: selected.id,
    idSeed: 'sync',
  })
  assert.ok(draft)

  const latestVersion = VERSIONS[1]
  const syncedDocs = syncWorkbenchResultToDocuments(
    generated.documents,
    draft.context.linkedDocIds,
    latestVersion,
  )
  const syncedBlueprint = syncWorkbenchResultToBlueprint({
    blueprint: draft.blueprint,
    context: draft.context,
    latestVersion,
    targetDocIds: draft.context.linkedDocIds,
  })

  const linkedDocs = syncedDocs.filter((doc) => draft.context.linkedDocIds.includes(doc.id))
  assert.ok(linkedDocs.length > 0)
  linkedDocs.forEach((doc) => {
    assert.equal(doc.status, 'readyForReview')
    assert.ok(doc.sections.some((section) => section.title === 'Workbench 回写'))
  })

  assert.equal(syncedBlueprint.status, 'implemented')
  const target = syncedBlueprint.nodes.find((node) => node.id === draft.context.workbenchTargetId)
  assert.ok(target)
  assert.equal(target.title, latestVersion.title)
  assert.deepEqual(target.boundDocumentIds, draft.context.linkedDocIds)
  assert.ok(
    syncedBlueprint.edges.some(
      (edge) =>
        edge.sourceNodeId === draft.context.selectedNodeId &&
        edge.targetNodeId === draft.context.workbenchTargetId &&
        edge.type === 'implemented_by',
    ),
  )
})

let failed = 0

tests.forEach(({ name, run }) => {
  try {
    run()
    console.log(`✓ ${name}`)
  } catch (error) {
    failed += 1
    console.error(`✗ ${name}`)
    console.error(error)
  }
})

if (failed > 0) {
  console.error(`${failed} mock test(s) failed.`)
  process.exit(1)
}

console.log(`${tests.length} mock test(s) passed.`)
