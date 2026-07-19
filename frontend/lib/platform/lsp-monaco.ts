import {
  isMonotonicLSPHeadSuccessor,
  normalizeLSPDocumentFence,
  normalizeLSPHeadFence,
  parseCandidateDocumentURI,
  sameLSPDocumentFence,
  sameLSPHeadFence,
  SandboxLSPError,
  type LSPDiagnosticDto,
  type LSPDocumentFenceDto,
  type LSPPublishDiagnosticsDto,
  type LSPServerEnvelopeDto,
  type LSPServerResponsePayloadDto,
  type SandboxHeadFenceDto,
} from './lsp-contract'

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

/** Deliberately excludes setValue and dispose from the LSP integration surface. */
export interface ProductionLSPMonacoModel {
  readonly uri: { toString(): string }
  getValue(): string
  getVersionId(): number
  validateRange(range: ProductionLSPMonacoRange): ProductionLSPMonacoRange
}

export interface ProductionLSPMonacoRange {
  readonly startLineNumber: number
  readonly startColumn: number
  readonly endLineNumber: number
  readonly endColumn: number
}

export interface ProductionLSPMonacoMarker {
  readonly startLineNumber: number
  readonly startColumn: number
  readonly endLineNumber: number
  readonly endColumn: number
  readonly message: string
  readonly severity: number
  readonly code?: string | { readonly value: string; readonly target: string }
  readonly source?: string
  readonly tags?: readonly number[]
}

export interface ProductionLSPMonacoNamespace {
  readonly MarkerSeverity: {
    readonly Hint: number
    readonly Info: number
    readonly Warning: number
    readonly Error: number
  }
  readonly editor: {
    setModelMarkers(
      model: ProductionLSPMonacoModel,
      owner: string,
      markers: readonly ProductionLSPMonacoMarker[],
    ): void
  }
}

export interface ProductionLSPMonacoDocument {
  readonly fence: LSPDocumentFenceDto
  readonly languageId: string
  readonly text: string
}

export interface ProductionLSPNavigationLocation {
  readonly modelUri: string
  readonly range: {
    readonly startLineNumber: number
    readonly startColumn: number
    readonly endLineNumber: number
    readonly endColumn: number
  }
}

interface ModelBinding {
  readonly model: ProductionLSPMonacoModel
  readonly openId: string
  readonly languageId: string
  savedContentHash: string
}

function defaultOpenIdFactory() {
  const value = globalThis.crypto?.randomUUID?.()
  if (!value) throw new SandboxLSPError('lsp_session_closed')
  return value
}

function modelVersion(model: ProductionLSPMonacoModel) {
  const version = model.getVersionId()
  if (!Number.isSafeInteger(version) || version < 1) {
    throw new SandboxLSPError('lsp_binding_stale')
  }
  return version
}

function markerSeverity(
  monaco: ProductionLSPMonacoNamespace,
  diagnostic: LSPDiagnosticDto,
) {
  switch (diagnostic.severity) {
    case 1: return monaco.MarkerSeverity.Error
    case 2: return monaco.MarkerSeverity.Warning
    case 3: return monaco.MarkerSeverity.Info
    default: return monaco.MarkerSeverity.Hint
  }
}

function marker(
  monaco: ProductionLSPMonacoNamespace,
  model: ProductionLSPMonacoModel,
  diagnostic: LSPDiagnosticDto,
): ProductionLSPMonacoMarker {
  const requested = {
    startLineNumber: diagnostic.range.start.line + 1,
    startColumn: diagnostic.range.start.character + 1,
    endLineNumber: diagnostic.range.end.line + 1,
    endColumn: diagnostic.range.end.character + 1,
  }
  const validated = model.validateRange(requested)
  if (requested.startLineNumber !== validated.startLineNumber ||
    requested.startColumn !== validated.startColumn ||
    requested.endLineNumber !== validated.endLineNumber ||
    requested.endColumn !== validated.endColumn) {
    throw new SandboxLSPError('lsp_message_malformed')
  }
  return {
    ...requested,
    message: diagnostic.message,
    severity: markerSeverity(monaco, diagnostic),
    ...(diagnostic.code === undefined ? {} : { code: String(diagnostic.code) }),
    ...(diagnostic.source === undefined ? {} : { source: diagnostic.source }),
    ...(diagnostic.tags === undefined ? {} : { tags: diagnostic.tags }),
  }
}

function navigationInteger(value: unknown) {
  if (!Number.isSafeInteger(value) || (value as number) < 0 ||
    (value as number) > 2_147_483_647) throw new SandboxLSPError('lsp_message_malformed')
  return value as number
}

function navigationPosition(value: unknown) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new SandboxLSPError('lsp_message_malformed')
  }
  const source = value as Record<string, unknown>
  if (Object.keys(source).length !== 2 || !Object.hasOwn(source, 'line') ||
    !Object.hasOwn(source, 'character')) throw new SandboxLSPError('lsp_message_malformed')
  return { line: navigationInteger(source.line), character: navigationInteger(source.character) }
}

function navigationLocations(
  value: unknown,
  maximum: number,
  head: SandboxHeadFenceDto,
): readonly ProductionLSPNavigationLocation[] {
  const values = value === null ? [] : Array.isArray(value) ? value : [value]
  if (!Number.isSafeInteger(maximum) || maximum < 1 || values.length > maximum) {
    throw new SandboxLSPError('lsp_message_malformed')
  }
  return Object.freeze(values.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    const source = entry as Record<string, unknown>
    if (Object.keys(source).length !== 2 || typeof source.uri !== 'string' ||
      !Object.hasOwn(source, 'range') || !source.range || typeof source.range !== 'object' ||
      Array.isArray(source.range)) throw new SandboxLSPError('lsp_message_malformed')
    const range = source.range as Record<string, unknown>
    if (Object.keys(range).length !== 2 || !Object.hasOwn(range, 'start') ||
      !Object.hasOwn(range, 'end')) throw new SandboxLSPError('lsp_message_malformed')
    const start = navigationPosition(range.start)
    const end = navigationPosition(range.end)
    if (start.line > end.line || (start.line === end.line && start.character > end.character)) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    const target = parseCandidateDocumentURI(source.uri)
    if (target.projectId !== head.projectId || target.candidateId !== head.candidateId) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    return Object.freeze({
      modelUri: source.uri,
      range: Object.freeze({
        startLineNumber: start.line + 1,
        startColumn: start.character + 1,
        endLineNumber: end.line + 1,
        endColumn: end.character + 1,
      }),
    })
  }))
}

/**
 * Keeps Candidate/Monaco identity stable across WSS reconnect and head refresh.
 * It never creates, disposes, replaces, or setValue()s a Monaco model, so the
 * editor's in-memory value, version and undo stack remain owned by Monaco.
 */
export class ProductionLSPMonacoAdapter {
  readonly markerOwner: string
  private head: SandboxHeadFenceDto
  private readonly models = new Map<string, ModelBinding>()

  constructor(
    head: SandboxHeadFenceDto,
    profileId: string,
    private readonly openIdFactory: () => string = defaultOpenIdFactory,
  ) {
    this.head = normalizeLSPHeadFence(head)
    if (!/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/u.test(profileId)) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    // Stable across reconnects; markers from an obsolete binding are replaced
    // only by another exact projection for the same immutable profile.
    this.markerOwner = `worksflow-lsp:${profileId}`
  }

  attachModel(
    model: ProductionLSPMonacoModel,
    languageId: string,
    savedContentHash: string,
    openId = this.openIdFactory(),
  ): LSPDocumentFenceDto {
    if (!UUID_PATTERN.test(openId) || !languageId || languageId !== languageId.trim()) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    const modelUri = model.uri.toString()
    const candidate = normalizeLSPDocumentFence({
      modelUri,
      openId,
      modelVersion: modelVersion(model),
      savedContentHash,
    }, this.head)
    const existing = this.models.get(modelUri)
    if (existing) {
      if (existing.model !== model || existing.openId !== openId ||
        existing.languageId !== languageId || existing.savedContentHash !== savedContentHash) {
        throw new SandboxLSPError('lsp_binding_stale')
      }
      return this.fence(existing)
    }
    this.models.set(modelUri, { model, openId, languageId, savedContentHash })
    return candidate
  }

  fenceFor(model: ProductionLSPMonacoModel): LSPDocumentFenceDto {
    const binding = this.models.get(model.uri.toString())
    if (!binding || binding.model !== model) throw new SandboxLSPError('lsp_binding_stale')
    return this.fence(binding)
  }

  reconnectDocuments(): readonly ProductionLSPMonacoDocument[] {
    return Object.freeze(Array.from(this.models.values(), (binding) => Object.freeze({
      fence: this.fence(binding),
      languageId: binding.languageId,
      text: binding.model.getValue(),
    })).sort((left, right) => left.fence.modelUri < right.fence.modelUri
      ? -1
      : left.fence.modelUri === right.fence.modelUri ? 0 : 1))
  }

  /** Applies only CAS metadata; it never changes a model value or lifecycle. */
  rebindHead(
    nextHead: SandboxHeadFenceDto,
    nextDocuments: readonly LSPDocumentFenceDto[],
  ) {
    const head = normalizeLSPHeadFence(nextHead)
    if (!isMonotonicLSPHeadSuccessor(this.head, head) || nextDocuments.length !== this.models.size) {
      throw new SandboxLSPError('lsp_binding_stale')
    }
    const admitted = new Map<string, LSPDocumentFenceDto>()
    for (const value of nextDocuments) {
      const document = normalizeLSPDocumentFence(value, head)
      const binding = this.models.get(document.modelUri)
      if (!binding || admitted.has(document.modelUri) || binding.openId !== document.openId ||
        modelVersion(binding.model) !== document.modelVersion) {
        throw new SandboxLSPError('lsp_binding_stale')
      }
      admitted.set(document.modelUri, document)
    }
    for (const [uri, document] of admitted) {
      this.models.get(uri)!.savedContentHash = document.savedContentHash
    }
    this.head = head
  }

  projectDiagnostics(
    monaco: ProductionLSPMonacoNamespace,
    envelope: LSPServerEnvelopeDto,
  ) {
    if (envelope.kind !== 'server.diagnostics' || !envelope.documentFence ||
      !sameLSPHeadFence(envelope.sandboxHeadFence, this.head)) return false
    const binding = this.models.get(envelope.documentFence.modelUri)
    if (!binding || !sameLSPDocumentFence(envelope.documentFence, this.fence(binding))) return false
    const payload = envelope.payload as { readonly diagnostics: LSPPublishDiagnosticsDto }
    if (payload.diagnostics.uri !== envelope.documentFence.modelUri ||
      payload.diagnostics.version !== envelope.documentFence.modelVersion) return false
    monaco.editor.setModelMarkers(
      binding.model,
      this.markerOwner,
      payload.diagnostics.diagnostics.map((entry) => marker(monaco, binding.model, entry)),
    )
    return true
  }

  projectResult(
    envelope: LSPServerEnvelopeDto,
    apply: (result: LSPServerResponsePayloadDto, model: ProductionLSPMonacoModel) => void,
  ) {
    if (envelope.kind !== 'server.response' || !envelope.documentFence ||
      !sameLSPHeadFence(envelope.sandboxHeadFence, this.head)) return false
    const binding = this.models.get(envelope.documentFence.modelUri)
    if (!binding || !sameLSPDocumentFence(envelope.documentFence, this.fence(binding))) return false
    apply(envelope.payload as LSPServerResponsePayloadDto, binding.model)
    return true
  }

  projectNavigation(
    envelope: LSPServerEnvelopeDto,
    maximumLocations: number,
    apply: (
      locations: readonly ProductionLSPNavigationLocation[],
      model: ProductionLSPMonacoModel,
    ) => void,
  ) {
    if (![
      'textDocument/declaration',
      'textDocument/definition',
      'textDocument/implementation',
      'textDocument/references',
      'textDocument/typeDefinition',
    ].includes(envelope.method)) return false
    let admitted = false
    const exact = this.projectResult(envelope, (response, model) => {
      try {
        const locations = response.status === 'ok'
          ? navigationLocations(response.result, maximumLocations, this.head)
          : []
        apply(locations, model)
        admitted = true
      } catch {
        admitted = false
      }
    })
    return exact && admitted
  }

  clearDiagnostics(
    monaco: ProductionLSPMonacoNamespace,
    model: ProductionLSPMonacoModel,
  ) {
    const binding = this.models.get(model.uri.toString())
    if (!binding || binding.model !== model) return false
    monaco.editor.setModelMarkers(model, this.markerOwner, [])
    return true
  }

  private fence(binding: ModelBinding) {
    return normalizeLSPDocumentFence({
      modelUri: binding.model.uri.toString(),
      openId: binding.openId,
      modelVersion: modelVersion(binding.model),
      savedContentHash: binding.savedContentHash,
    }, this.head)
  }
}
