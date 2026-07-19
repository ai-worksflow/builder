import assert from 'node:assert/strict'
import {
  candidateDocumentURI,
  SandboxLSPError,
  type LSPServerEnvelopeDto,
} from '../lib/platform/lsp-contract'
import {
  ProductionLSPMonacoAdapter,
  type ProductionLSPMonacoMarker,
  type ProductionLSPMonacoModel,
  type ProductionLSPMonacoNamespace,
  type ProductionLSPMonacoRange,
} from '../lib/platform/lsp-monaco'

const projectId = '11111111-1111-4111-8111-111111111111'
const sessionId = '22222222-2222-4222-8222-222222222222'
const candidateId = '33333333-3333-4333-8333-333333333333'
const connectionId = '44444444-4444-4444-8444-444444444444'
const bindingId = '55555555-5555-4555-8555-555555555555'
const openId = '66666666-6666-4666-8666-666666666666'
const digest = (character: string) => `sha256:${character.repeat(64)}`

const head = {
  projectId,
  sessionId,
  sessionEpoch: 3,
  candidateId,
  version: 7,
  journalSequence: 12,
  writerLeaseEpoch: 2,
  treeHash: digest('1'),
} as const

const nextHead = {
  ...head,
  version: 8,
  journalSequence: 13,
  treeHash: digest('2'),
} as const

class FakeModel implements ProductionLSPMonacoModel {
  readonly uri: { toString: () => string }
  private value: string
  private version = 1
  readonly undoStack = ['initial']
  setValueCalls = 0
  disposeCalls = 0

  constructor(uri: string, value: string) {
    this.uri = { toString: () => uri }
    this.value = value
  }

  getValue() {
    return this.value
  }

  getVersionId() {
    return this.version
  }

  validateRange(range: ProductionLSPMonacoRange): ProductionLSPMonacoRange {
    const lines = this.value.split('\n')
    const line = (value: number) => Math.min(Math.max(value, 1), lines.length)
    const column = (lineNumber: number, value: number) =>
      Math.min(Math.max(value, 1), lines[lineNumber - 1]!.length + 1)
    const startLineNumber = line(range.startLineNumber)
    const endLineNumber = line(range.endLineNumber)
    return {
      startLineNumber,
      startColumn: column(startLineNumber, range.startColumn),
      endLineNumber,
      endColumn: column(endLineNumber, range.endColumn),
    }
  }

  localEdit(value: string) {
    this.value = value
    this.version += 1
    this.undoStack.push(value)
  }

  setValue(value: string) {
    this.setValueCalls += 1
    this.value = value
  }

  dispose() {
    this.disposeCalls += 1
  }
}

function responseEnvelope(
  headFence: typeof nextHead,
  documentFence: ReturnType<ProductionLSPMonacoAdapter['fenceFor']>,
): LSPServerEnvelopeDto {
  return {
    schemaVersion: 'sandbox-lsp-envelope/v1',
    connectionId,
    bindingId,
    sequence: 2,
    messageId: '77777777-7777-4777-8777-777777777777',
    replyTo: '88888888-8888-4888-8888-888888888888',
    kind: 'server.response',
    method: 'textDocument/hover',
    sandboxHeadFence: headFence,
    documentFence,
    payload: { status: 'ok', result: { contents: 'page' }, error: null },
  }
}

function main() {
  const uri = candidateDocumentURI(projectId, candidateId, 'src/page.ts')
  const model = new FakeModel(uri, 'export const page = 1\n')
  const adapter = new ProductionLSPMonacoAdapter(head, 'typescript-lsp', () => openId)
  const initial = adapter.attachModel(model, 'typescript', digest('8'))
  assert.deepEqual(initial, {
    modelUri: uri,
    openId,
    modelVersion: 1,
    savedContentHash: digest('8'),
  })
  assert.equal(adapter.markerOwner, 'worksflow-lsp:typescript-lsp')
  assert.equal(adapter.reconnectDocuments()[0]!.text, model.getValue())

  model.localEdit('export const page = 2\n')
  const edited = adapter.fenceFor(model)
  assert.equal(edited.modelVersion, 2)
  assert.equal(edited.openId, openId)
  assert.equal(edited.savedContentHash, digest('8'))
  const modelIdentity = model
  const undoBeforeRebind = [...model.undoStack]
  const saved = { ...edited, savedContentHash: digest('9') }
  adapter.rebindHead(nextHead, [saved])
  assert.equal(adapter.fenceFor(model).savedContentHash, digest('9'))
  assert.equal(adapter.reconnectDocuments()[0]!.fence.openId, openId)
  assert.equal(adapter.reconnectDocuments()[0]!.fence.modelVersion, 2)
  assert.equal(adapter.reconnectDocuments()[0]!.text, 'export const page = 2\n')
  assert.equal(model, modelIdentity)
  assert.deepEqual(model.undoStack, undoBeforeRebind)
  assert.equal(model.setValueCalls, 0)
  assert.equal(model.disposeCalls, 0)

  const markerCalls: Array<{
    model: ProductionLSPMonacoModel
    owner: string
    markers: readonly ProductionLSPMonacoMarker[]
  }> = []
  const monaco: ProductionLSPMonacoNamespace = {
    MarkerSeverity: { Hint: 1, Info: 2, Warning: 4, Error: 8 },
    editor: {
      setModelMarkers: (target, owner, markers) => markerCalls.push({
        model: target,
        owner,
        markers,
      }),
    },
  }
  const exactDiagnostics = {
    uri,
    version: 2,
    diagnostics: [{
      range: { start: { line: 0, character: 1 }, end: { line: 0, character: 5 } },
      severity: 1,
      code: 'TS1000',
      source: 'typescript',
      message: 'Example error',
      tags: [1],
    }],
  } as const
  const diagnosticEnvelope: LSPServerEnvelopeDto = {
    schemaVersion: 'sandbox-lsp-envelope/v1',
    connectionId,
    bindingId,
    sequence: 2,
    messageId: '99999999-9999-4999-8999-999999999999',
    replyTo: null,
    kind: 'server.diagnostics',
    method: 'textDocument/publishDiagnostics',
    sandboxHeadFence: nextHead,
    documentFence: saved,
    payload: {
      diagnostics: exactDiagnostics,
    },
  }
  assert.equal(adapter.projectDiagnostics(monaco, diagnosticEnvelope), true)
  assert.equal(markerCalls.length, 1)
  assert.equal(markerCalls[0]!.model, model)
  assert.equal(markerCalls[0]!.owner, adapter.markerOwner)
  assert.deepEqual(markerCalls[0]!.markers[0], {
    startLineNumber: 1,
    startColumn: 2,
    endLineNumber: 1,
    endColumn: 6,
    message: 'Example error',
    severity: 8,
    code: 'TS1000',
    source: 'typescript',
    tags: [1],
  })
  const invalidDiagnosticEnvelope: LSPServerEnvelopeDto = {
    ...diagnosticEnvelope,
    payload: {
      diagnostics: {
        ...exactDiagnostics,
        diagnostics: [
          ...exactDiagnostics.diagnostics,
          {
            range: { start: { line: 99, character: 0 }, end: { line: 99, character: 1 } },
            severity: 1,
            message: 'Out-of-model diagnostic',
          },
        ],
      },
    },
  }
  assert.throws(
    () => adapter.projectDiagnostics(monaco, invalidDiagnosticEnvelope),
    (error: unknown) => error instanceof SandboxLSPError && error.code === 'lsp_message_malformed',
  )
  assert.equal(markerCalls.length, 1, 'a clamped range must reject the complete marker batch atomically')

  let projectedModel: ProductionLSPMonacoModel | null = null
  assert.equal(adapter.projectResult(responseEnvelope(nextHead, saved), (_result, target) => {
    projectedModel = target
  }), true)
  assert.equal(projectedModel, model)

  const navigationEnvelope: LSPServerEnvelopeDto = {
    ...responseEnvelope(nextHead, saved),
    method: 'textDocument/definition',
    payload: {
      status: 'ok',
      result: [{
        uri,
        range: {
          start: { line: 0, character: 7 },
          end: { line: 0, character: 11 },
        },
      }],
      error: null,
    },
  }
  for (const method of [
    'textDocument/declaration',
    'textDocument/definition',
    'textDocument/implementation',
    'textDocument/references',
    'textDocument/typeDefinition',
  ]) {
    let navigation: unknown
    assert.equal(adapter.projectNavigation({ ...navigationEnvelope, method }, 10, (locations, target) => {
      assert.equal(target, model)
      navigation = locations
    }), true, `${method} must project only through the exact model fence`)
    assert.deepEqual(navigation, [{
      modelUri: uri,
      range: {
        startLineNumber: 1,
        startColumn: 8,
        endLineNumber: 1,
        endColumn: 12,
      },
    }])
  }
  const foreignNavigation: LSPServerEnvelopeDto = {
    ...navigationEnvelope,
    payload: {
      status: 'ok',
      result: {
        uri: candidateDocumentURI(projectId, connectionId, 'src/foreign.ts'),
        range: {
          start: { line: 0, character: 0 },
          end: { line: 0, character: 1 },
        },
      },
      error: null,
    },
  }
  assert.equal(adapter.projectNavigation(foreignNavigation, 10, () => {
    assert.fail('navigation outside the exact Candidate must not be projected')
  }), false)
  const widenedNavigation: LSPServerEnvelopeDto = {
    ...navigationEnvelope,
    payload: {
      status: 'ok',
      result: [{
        uri,
        range: {
          start: { line: 0, character: 7 },
          end: { line: 0, character: 11 },
        },
        data: 'unexpected',
      }],
      error: null,
    },
  }
  assert.equal(adapter.projectNavigation(widenedNavigation, 10, () => {
    assert.fail('navigation with additional properties must not be projected')
  }), false)

  // A local edit makes every old model-version projection stale without
  // replacing content or undo history.
  model.localEdit('export const page = 3\n')
  assert.equal(adapter.projectDiagnostics(monaco, diagnosticEnvelope), false)
  assert.equal(adapter.projectResult(responseEnvelope(nextHead, saved), () => {
    assert.fail('stale result must not be projected')
  }), false)
  assert.equal(adapter.projectNavigation(navigationEnvelope, 10, () => {
    assert.fail('stale navigation must not be projected')
  }), false)
  assert.equal(markerCalls.length, 1)
  assert.equal(model.setValueCalls, 0)
  assert.equal(model.disposeCalls, 0)
  assert.deepEqual(model.undoStack, [
    'initial',
    'export const page = 2\n',
    'export const page = 3\n',
  ])

  assert.throws(
    () => adapter.rebindHead({
      ...nextHead,
      version: 9,
      journalSequence: 14,
      treeHash: digest('3'),
    }, [{ ...adapter.fenceFor(model), openId: connectionId }]),
    (error: unknown) => error instanceof SandboxLSPError && error.code === 'lsp_binding_stale',
  )
  assert.equal(model.setValueCalls, 0)
  assert.equal(model.disposeCalls, 0)

  console.log('Production LSP Monaco adapter tests passed')
}

main()
