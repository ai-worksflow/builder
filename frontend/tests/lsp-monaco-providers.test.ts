import assert from 'node:assert/strict'
import {
  escapeMarkdown,
  parseSafeCompletionResult,
  parseSafeDocumentHighlights,
  parseSafeDocumentSymbols,
  parseSafeHoverResult,
  parseSafeSignatureHelpResult,
  requireExactMonacoRange,
} from '../lib/platform/lsp-monaco-providers'
import { SandboxLSPError } from '../lib/platform/lsp-contract'

const currentRange = {
  start: { line: 0, character: 7 },
  end: { line: 0, character: 11 },
} as const
const editRange = {
  start: { line: 1, character: 0 },
  end: { line: 1, character: 4 },
} as const

function malformed(action: () => unknown) {
  assert.throws(action, (error: unknown) =>
    error instanceof SandboxLSPError && error.code === 'lsp_message_malformed')
}

assert.equal(
  escapeMarkdown('**page** [x](y) <b> #1'),
  '\\*\\*page\\*\\* \\[x\\]\\(y\\) \\<b\\> \\#1',
)
const exactModelRange = {
  startLineNumber: 2,
  startColumn: 1,
  endLineNumber: 2,
  endColumn: 5,
} as const
assert.equal(requireExactMonacoRange(exactModelRange, { ...exactModelRange }), exactModelRange)
malformed(() => requireExactMonacoRange(exactModelRange, {
  ...exactModelRange,
  endColumn: 4,
}))

const completion = parseSafeCompletionResult({
  isIncomplete: false,
  items: [{
    label: 'page',
    kind: 6,
    detail: 'const page',
    documentation: { kind: 'plaintext', value: '**page**' },
    sortText: '01',
    filterText: 'page',
    insertTextFormat: 1,
    textEdit: { range: editRange, newText: 'pageValue' },
    preselect: true,
    commitCharacters: ['.', ';'],
  }],
}, currentRange, 20)
assert.deepEqual(completion, {
  incomplete: false,
  suggestions: [{
    label: 'page',
    insertText: 'pageValue',
    range: { startLineNumber: 2, startColumn: 1, endLineNumber: 2, endColumn: 5 },
    kind: 4,
    detail: 'const page',
    documentation: { value: '\\*\\*page\\*\\*' },
    sortText: '01',
    filterText: 'page',
    preselect: true,
    commitCharacters: ['.', ';'],
  }],
})
assert.deepEqual(parseSafeCompletionResult([{
  label: 'replace word',
  insertText: '',
}], currentRange, 20).suggestions[0], {
  label: 'replace word',
  insertText: '',
  range: { startLineNumber: 1, startColumn: 8, endLineNumber: 1, endColumn: 12 },
  kind: 18,
})
assert.deepEqual(parseSafeCompletionResult(null, currentRange, 20), {
  incomplete: false,
  suggestions: [],
})

for (const forbidden of [
  'labelDetails',
  'tags',
  'deprecated',
  'insertTextMode',
  'textEditText',
  'additionalTextEdits',
  'command',
  'data',
]) malformed(() => parseSafeCompletionResult([{
  label: 'unsafe',
  insertText: 'unsafe',
  [forbidden]: {},
}], currentRange, 20))
malformed(() => parseSafeCompletionResult([{ label: 'missing edit' }], currentRange, 20))
malformed(() => parseSafeCompletionResult([{
  label: 'ambiguous',
  insertText: 'one',
  textEdit: { range: editRange, newText: 'two' },
}], currentRange, 20))
malformed(() => parseSafeCompletionResult([{
  label: 'snippet',
  insertText: '${1:unsafe}',
  insertTextFormat: 2,
}], currentRange, 20))
malformed(() => parseSafeCompletionResult([{
  label: 'markdown',
  insertText: 'markdown',
  documentation: { kind: 'markdown', value: '**unsafe**' },
}], currentRange, 20))
malformed(() => parseSafeCompletionResult([{
  label: 'duplicate commits',
  insertText: 'duplicate',
  commitCharacters: ['.', '.'],
}], currentRange, 20))
malformed(() => parseSafeCompletionResult([{
  label: 'edit extras',
  textEdit: { range: editRange, newText: 'safe', annotationId: 'unsafe' },
}], currentRange, 20))

assert.deepEqual(parseSafeHoverResult({
  contents: { kind: 'plaintext', value: '**type** <script>' },
  range: currentRange,
}), {
  contents: [{ value: '\\*\\*type\\*\\* \\<script\\>' }],
  range: { startLineNumber: 1, startColumn: 8, endLineNumber: 1, endColumn: 12 },
})
assert.deepEqual(parseSafeHoverResult({ contents: '# raw plaintext' }), {
  contents: [{ value: '\\# raw plaintext' }],
})
malformed(() => parseSafeHoverResult({ contents: ['array is forbidden'] }))
malformed(() => parseSafeHoverResult({ contents: { kind: 'markdown', value: '**unsafe**' } }))
malformed(() => parseSafeHoverResult({ contents: { language: 'ts', value: 'const page = 1' } }))
malformed(() => parseSafeHoverResult({ contents: 'hover', command: 'unsafe' }))

assert.deepEqual(parseSafeSignatureHelpResult({
  signatures: [{
    label: 'page(value: string)',
    documentation: 'Signature *docs*',
    parameters: [{
      label: [5, 18],
      documentation: { kind: 'plaintext', value: 'value [docs]' },
    }],
  }],
  activeSignature: 0,
  activeParameter: 0,
}), {
  signatures: [{
    label: 'page(value: string)',
    documentation: { value: 'Signature \\*docs\\*' },
    parameters: [{ label: [5, 18], documentation: { value: 'value \\[docs\\]' } }],
  }],
  activeSignature: 0,
  activeParameter: 0,
})
malformed(() => parseSafeSignatureHelpResult({
  signatures: [{ label: 'page()', activeParameter: 0 }],
}))
malformed(() => parseSafeSignatureHelpResult({
  signatures: [{ label: 'page()', documentation: { kind: 'markdown', value: 'unsafe' } }],
}))
malformed(() => parseSafeSignatureHelpResult({
  signatures: [{ label: 'page(value)', parameters: [{ label: 'value', extra: true }] }],
}))
malformed(() => parseSafeSignatureHelpResult({
  signatures: [{ label: 'page(value)', parameters: [{ label: 'value' }] }],
  activeParameter: 1,
}))
malformed(() => parseSafeSignatureHelpResult({
  signatures: [],
  activeParameter: 0,
}))

assert.deepEqual(parseSafeDocumentHighlights([{
  range: currentRange,
  kind: 2,
}], 10), [{
  range: { startLineNumber: 1, startColumn: 8, endLineNumber: 1, endColumn: 12 },
  kind: 1,
}])
malformed(() => parseSafeDocumentHighlights([{ range: currentRange, kind: 2, data: true }], 10))

const parentRange = {
  start: { line: 0, character: 0 },
  end: { line: 2, character: 0 },
} as const
assert.deepEqual(parseSafeDocumentSymbols([{
  name: 'page',
  detail: 'const',
  kind: 13,
  tags: [1],
  range: parentRange,
  selectionRange: currentRange,
  children: [{
    name: 'value',
    kind: 14,
    range: currentRange,
    selectionRange: currentRange,
  }],
}], 10), [{
  name: 'page',
  detail: 'const',
  kind: 12,
  tags: [1],
  range: { startLineNumber: 1, startColumn: 1, endLineNumber: 3, endColumn: 1 },
  selectionRange: { startLineNumber: 1, startColumn: 8, endLineNumber: 1, endColumn: 12 },
  children: [{
    name: 'value',
    detail: '',
    kind: 13,
    tags: [],
    range: { startLineNumber: 1, startColumn: 8, endLineNumber: 1, endColumn: 12 },
    selectionRange: { startLineNumber: 1, startColumn: 8, endLineNumber: 1, endColumn: 12 },
  }],
}])
malformed(() => parseSafeDocumentSymbols([{
  name: 'legacy location',
  kind: 13,
  location: { uri: 'worksflow-candidate://foreign', range: currentRange },
}], 10))
malformed(() => parseSafeDocumentSymbols([{
  name: 'deprecated key',
  kind: 13,
  range: currentRange,
  selectionRange: currentRange,
  deprecated: true,
}], 10))
malformed(() => parseSafeDocumentSymbols([{
  name: 'outside selection',
  kind: 13,
  range: currentRange,
  selectionRange: editRange,
}], 10))

let tooDeep: unknown = {
  name: 'depth-9',
  kind: 13,
  range: currentRange,
  selectionRange: currentRange,
}
for (let depth = 8; depth >= 0; depth -= 1) {
  tooDeep = {
    name: `depth-${depth}`,
    kind: 13,
    range: currentRange,
    selectionRange: currentRange,
    children: [tooDeep],
  }
}
malformed(() => parseSafeDocumentSymbols([tooDeep], 20))

console.log('Safe Monaco LSP provider exact DTO projection tests passed')
