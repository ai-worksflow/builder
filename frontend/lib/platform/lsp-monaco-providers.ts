import { SandboxLSPError } from './lsp-contract'

const MAX_WIRE_INTEGER = 2_147_483_647
const COMPLETION_ITEM_KEYS = [
  'label', 'kind', 'detail', 'documentation', 'sortText', 'filterText', 'insertText',
  'insertTextFormat', 'textEdit', 'preselect', 'commitCharacters',
] as const
const COMPLETION_KIND_MAP = [
  18, 0, 1, 2, 3, 4, 5, 7, 8, 9, 12, 13, 15, 17, 28, 19, 20, 21, 23, 16,
  14, 6, 10, 11, 24,
] as const

type UnknownRecord = Record<string, unknown>

export interface SafeMonacoRange {
  readonly startLineNumber: number
  readonly startColumn: number
  readonly endLineNumber: number
  readonly endColumn: number
}

export function requireExactMonacoRange<T extends SafeMonacoRange>(
  requested: T,
  validated: SafeMonacoRange,
) {
  if (requested.startLineNumber !== validated.startLineNumber ||
    requested.startColumn !== validated.startColumn ||
    requested.endLineNumber !== validated.endLineNumber ||
    requested.endColumn !== validated.endColumn) malformed()
  return requested
}

export interface SafeCompletionItem {
  readonly label: string
  readonly insertText: string
  readonly range: SafeMonacoRange
  readonly kind: number
  readonly detail?: string
  readonly documentation?: { readonly value: string }
  readonly sortText?: string
  readonly filterText?: string
  readonly preselect?: boolean
  readonly commitCharacters?: readonly string[]
}

export interface SafeCompletionList {
  readonly incomplete: boolean
  readonly suggestions: readonly SafeCompletionItem[]
}

export interface SafeHover {
  readonly contents: readonly { readonly value: string }[]
  readonly range?: SafeMonacoRange
}

export interface SafeSignatureHelp {
  readonly signatures: readonly {
    readonly label: string
    readonly documentation?: { readonly value: string }
    readonly parameters: readonly {
      readonly label: string | readonly [number, number]
      readonly documentation?: { readonly value: string }
    }[]
  }[]
  readonly activeSignature: number
  readonly activeParameter: number
}

export interface SafeDocumentHighlight {
  readonly range: SafeMonacoRange
  readonly kind?: number
}

export interface SafeDocumentSymbol {
  readonly name: string
  readonly detail: string
  readonly kind: number
  readonly tags: readonly number[]
  readonly range: SafeMonacoRange
  readonly selectionRange: SafeMonacoRange
  readonly children?: readonly SafeDocumentSymbol[]
}

function malformed(): never {
  throw new SandboxLSPError('lsp_message_malformed')
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) malformed()
  return value as UnknownRecord
}

function shape(
  value: unknown,
  required: readonly string[],
  optional: readonly string[] = [],
) {
  const source = record(value)
  const allowed = new Set([...required, ...optional])
  const keys = Object.keys(source)
  if (required.some((key) => !Object.hasOwn(source, key)) ||
    keys.some((key) => !allowed.has(key))) malformed()
  return source
}

function integer(value: unknown, minimum = 0, maximum = MAX_WIRE_INTEGER) {
  if (!Number.isSafeInteger(value) || (value as number) < minimum ||
    (value as number) > maximum) malformed()
  return value as number
}

function text(value: unknown, maximum = 16 << 10) {
  const result = plainText(value, maximum)
  if (result.length === 0) malformed()
  return result
}

function plainText(value: unknown, maximum = 16 << 10) {
  if (typeof value !== 'string' || value.includes('\0') || value.includes('\uFFFD') ||
    !hasValidSurrogatePairs(value) || new TextEncoder().encode(value).byteLength > maximum) malformed()
  return value
}

function hasValidSurrogatePairs(value: string) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xD800 && code <= 0xDBFF) {
      const following = value.charCodeAt(index + 1)
      if (following < 0xDC00 || following > 0xDFFF) return false
      index += 1
    } else if (code >= 0xDC00 && code <= 0xDFFF) return false
  }
  return true
}

function rawPosition(value: unknown) {
  const source = shape(value, ['line', 'character'])
  return { line: integer(source.line), character: integer(source.character) }
}

function rawRange(value: unknown) {
  const source = shape(value, ['start', 'end'])
  const start = rawPosition(source.start)
  const end = rawPosition(source.end)
  if (start.line > end.line || (start.line === end.line && start.character > end.character)) {
    malformed()
  }
  return { start, end }
}

function range(value: unknown): SafeMonacoRange {
  const parsed = rawRange(value)
  return {
    startLineNumber: parsed.start.line + 1,
    startColumn: parsed.start.character + 1,
    endLineNumber: parsed.end.line + 1,
    endColumn: parsed.end.character + 1,
  }
}

export function escapeMarkdown(value: string) {
  return value.replace(/[\\`*_[\]{}()<>#+\-.!|>]/gu, '\\$&')
}

function markup(value: unknown): { readonly value: string } {
  if (typeof value === 'string') return { value: escapeMarkdown(plainText(value, 64 << 10)) }
  const source = shape(value, ['kind', 'value'])
  if (source.kind !== 'plaintext') malformed()
  return { value: escapeMarkdown(plainText(source.value, 64 << 10)) }
}

export function parseSafeCompletionResult(
  value: unknown,
  currentRange: {
    readonly start: { readonly line: number; readonly character: number }
    readonly end: { readonly line: number; readonly character: number }
  },
  maximumItems: number,
  maximumDocumentBytes = 64 << 10,
): SafeCompletionList {
  if (!Number.isSafeInteger(maximumItems) || maximumItems < 0 ||
    !Number.isSafeInteger(maximumDocumentBytes) || maximumDocumentBytes < 0) malformed()
  if (value === null) return { incomplete: false, suggestions: [] }
  const source = Array.isArray(value)
    ? { isIncomplete: false, items: value }
    : shape(value, ['isIncomplete', 'items'])
  if (typeof source.isIncomplete !== 'boolean' || !Array.isArray(source.items) ||
    source.items.length > maximumItems) malformed()
  rawRange(currentRange)
  const suggestions = source.items.map((entry) => {
    const item = shape(entry, ['label'], COMPLETION_ITEM_KEYS.slice(1))
    const label = text(item.label, 4 << 10)
    const hasInsertText = Object.hasOwn(item, 'insertText')
    const hasTextEdit = Object.hasOwn(item, 'textEdit')
    if (hasInsertText === hasTextEdit) malformed()
    if (item.insertTextFormat !== undefined && integer(item.insertTextFormat, 1, 2) !== 1) malformed()
    if (item.kind !== undefined) integer(item.kind, 1, 25)
    if (item.preselect !== undefined && typeof item.preselect !== 'boolean') malformed()
    let commitCharacters: string[] | undefined
    if (item.commitCharacters !== undefined) {
      if (!Array.isArray(item.commitCharacters) || item.commitCharacters.length > 32) malformed()
      commitCharacters = item.commitCharacters.map((entry) => text(entry, 16))
      if (new Set(commitCharacters).size !== commitCharacters.length) malformed()
    }
    let insertText: string
    let replacementRange: unknown = currentRange
    if (hasInsertText) {
      insertText = plainText(item.insertText, maximumDocumentBytes)
    } else {
      const edit = shape(item.textEdit, ['range', 'newText'])
      rawRange(edit.range)
      replacementRange = edit.range
      insertText = plainText(edit.newText, maximumDocumentBytes)
    }
    return {
      label,
      insertText,
      range: range(replacementRange),
      kind: item.kind === undefined ? 18 : COMPLETION_KIND_MAP[integer(item.kind, 1, 25) - 1]!,
      ...(item.detail === undefined ? {} : { detail: plainText(item.detail, 16 << 10) }),
      ...(item.documentation === undefined ? {} : { documentation: markup(item.documentation) }),
      ...(item.sortText === undefined ? {} : { sortText: plainText(item.sortText, 4 << 10) }),
      ...(item.filterText === undefined ? {} : { filterText: plainText(item.filterText, 4 << 10) }),
      ...(item.preselect === undefined ? {} : { preselect: item.preselect }),
      ...(commitCharacters === undefined ? {} : { commitCharacters }),
    }
  })
  return { incomplete: source.isIncomplete, suggestions }
}

export function parseSafeHoverResult(value: unknown): SafeHover | undefined {
  if (value === null) return undefined
  const source = shape(value, ['contents'], ['range'])
  return {
    contents: [markup(source.contents)],
    ...(source.range === undefined ? {} : { range: range(source.range) }),
  }
}

export function parseSafeSignatureHelpResult(
  value: unknown,
  maximumSignatures = 128,
): SafeSignatureHelp | undefined {
  if (!Number.isSafeInteger(maximumSignatures) || maximumSignatures < 0) malformed()
  if (value === null) return undefined
  const source = shape(value, ['signatures'], ['activeSignature', 'activeParameter'])
  if (!Array.isArray(source.signatures) ||
    source.signatures.length > Math.min(maximumSignatures, 128)) {
    malformed()
  }
  const signatures = source.signatures.map((entry) => {
    const signature = shape(entry, ['label'], ['documentation', 'parameters'])
    const label = text(signature.label, 16 << 10)
    const rawParameters = signature.parameters === undefined ? [] : signature.parameters
    if (!Array.isArray(rawParameters) || rawParameters.length > 256) malformed()
    const parameters = rawParameters.map((parameterValue) => {
      const parameter = shape(parameterValue, ['label'], ['documentation'])
      let parameterLabel: string | readonly [number, number]
      if (typeof parameter.label === 'string') parameterLabel = text(parameter.label, 4 << 10)
      else {
        if (!Array.isArray(parameter.label) || parameter.label.length !== 2) malformed()
        const start = integer(parameter.label[0], 0, label.length)
        const end = integer(parameter.label[1], start, label.length)
        parameterLabel = [start, end] as const
      }
      return {
        label: parameterLabel,
        ...(parameter.documentation === undefined
          ? {}
          : { documentation: markup(parameter.documentation) }),
      }
    })
    return {
      label,
      parameters,
      ...(signature.documentation === undefined
        ? {}
        : { documentation: markup(signature.documentation) }),
    }
  })
  const hasActiveSignature = source.activeSignature !== undefined
  const activeSignature = hasActiveSignature
    ? integer(source.activeSignature, 0, signatures.length - 1)
    : 0
  const activeParameter = source.activeParameter === undefined
    ? 0
    : integer(source.activeParameter, 0, 255)
  if (source.activeParameter !== undefined) {
    const active = signatures[activeSignature]
    if (!active || activeParameter >= active.parameters.length) malformed()
  }
  return { signatures, activeSignature, activeParameter }
}

export function parseSafeDocumentHighlights(
  value: unknown,
  maximum: number,
): readonly SafeDocumentHighlight[] {
  if (!Number.isSafeInteger(maximum) || maximum < 0) malformed()
  if (value === null) return []
  if (!Array.isArray(value) || value.length > maximum) malformed()
  return value.map((entry) => {
    const highlight = shape(entry, ['range'], ['kind'])
    const kind = highlight.kind === undefined ? undefined : integer(highlight.kind, 1, 3) - 1
    return {
      range: range(highlight.range),
      ...(kind === undefined ? {} : { kind }),
    }
  })
}

export function parseSafeDocumentSymbols(
  value: unknown,
  maximum: number,
): readonly SafeDocumentSymbol[] {
  if (!Number.isSafeInteger(maximum) || maximum < 0) malformed()
  if (value === null) return []
  if (!Array.isArray(value) || value.length > maximum) malformed()
  let count = 0
  const parse = (entry: unknown, depth: number): SafeDocumentSymbol => {
    if (count >= maximum) malformed()
    count += 1
    if (depth > 8) malformed()
    const symbol = shape(entry, ['name', 'kind', 'range', 'selectionRange'], [
      'detail', 'tags', 'children',
    ])
    const childrenValue = symbol.children === undefined ? undefined : symbol.children
    if (childrenValue !== undefined && !Array.isArray(childrenValue)) malformed()
    const symbolRange = rawRange(symbol.range)
    const selectionRange = rawRange(symbol.selectionRange)
    if (!rawRangeContains(symbolRange, selectionRange)) malformed()
    return {
      name: text(symbol.name, 4 << 10),
      detail: symbol.detail === undefined ? '' : plainText(symbol.detail, 16 << 10),
      kind: integer(symbol.kind, 1, 26) - 1,
      tags: symbol.tags === undefined ? [] : parseSymbolTags(symbol.tags),
      range: range(symbolRange),
      selectionRange: range(selectionRange),
      ...(childrenValue === undefined
        ? {}
        : { children: childrenValue.map((child) => parse(child, depth + 1)) }),
    }
  }
  const result = value.map((entry) => parse(entry, 0))
  if (count > maximum) malformed()
  return result
}

function parseSymbolTags(value: unknown) {
  if (!Array.isArray(value) || value.length > 1 ||
    value.some((entry) => integer(entry, 1, 1) !== 1)) malformed()
  return value as number[]
}

function rawRangeContains(
  outer: ReturnType<typeof rawRange>,
  inner: ReturnType<typeof rawRange>,
) {
  const startsBefore = outer.start.line < inner.start.line ||
    (outer.start.line === inner.start.line && outer.start.character <= inner.start.character)
  const endsAfter = outer.end.line > inner.end.line ||
    (outer.end.line === inner.end.line && outer.end.character >= inner.end.character)
  return startsBefore && endsAfter
}
