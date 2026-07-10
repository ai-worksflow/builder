export type WorkspaceDiagnosticSeverity = 'error' | 'warning' | 'info' | 'hint'

export interface WorkspaceFile {
  readonly path: string
  readonly content: string
  readonly language: string
  readonly revision: number
  readonly dirty: boolean
}

export interface WorkspaceFileInput {
  path: string
  content: string
  language?: string
  revision?: number
  dirty?: boolean
}

export interface WorkspaceDiagnostic {
  readonly id: string
  readonly severity: WorkspaceDiagnosticSeverity
  readonly message: string
  readonly path?: string
  readonly source?: string
  readonly line?: number
  readonly column?: number
  readonly endLine?: number
  readonly endColumn?: number
}

export interface WorkspaceCheckpoint {
  readonly id: string
  readonly label: string
  readonly message?: string
  readonly branchId: string
  readonly parentCheckpointId?: string
  readonly createdAt: string
  readonly files: readonly WorkspaceFile[]
}

export interface WorkspaceBranch {
  readonly id: string
  readonly name: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly baseCheckpointId?: string
  readonly headCheckpointId?: string
}

export interface VirtualWorkspace {
  readonly id: string
  readonly name: string
  readonly revision: number
  readonly createdAt: string
  readonly updatedAt: string
  readonly files: readonly WorkspaceFile[]
  readonly checkpoints: readonly WorkspaceCheckpoint[]
  readonly branches: readonly WorkspaceBranch[]
  readonly activeBranchId: string
  readonly diagnostics: readonly WorkspaceDiagnostic[]
}

export interface CreateWorkspaceOptions {
  id?: string
  name?: string
  files?: readonly WorkspaceFileInput[]
  diagnostics?: readonly WorkspaceDiagnostic[]
  branchId?: string
  branchName?: string
  createdAt?: string
}

export interface CreateCheckpointOptions {
  id?: string
  label?: string
  message?: string
  createdAt?: string
}

export interface CreateBranchOptions {
  id?: string
  name: string
  fromCheckpointId?: string
  checkout?: boolean
  createdAt?: string
}

export type LineDiffKind = 'equal' | 'add' | 'remove'

export interface LineDiffEntry {
  readonly kind: LineDiffKind
  readonly content: string
  readonly oldLineNumber?: number
  readonly newLineNumber?: number
}

export interface LineDiff {
  readonly lines: readonly LineDiffEntry[]
  readonly additions: number
  readonly deletions: number
  readonly unchanged: number
}

export type CheckpointFileStatus = 'added' | 'deleted' | 'modified' | 'unchanged'

export interface CheckpointFileComparison {
  readonly path: string
  readonly status: CheckpointFileStatus
  readonly before?: WorkspaceFile
  readonly after?: WorkspaceFile
  readonly diff: LineDiff
}

export interface CheckpointComparison {
  readonly fromCheckpointId: string
  readonly toCheckpointId: string
  readonly files: readonly CheckpointFileComparison[]
  readonly summary: Readonly<Record<CheckpointFileStatus, number>>
}

export interface SearchFilesOptions {
  caseSensitive?: boolean
  includePaths?: boolean
  includeContent?: boolean
  maxResults?: number
}

export interface WorkspaceSearchResult {
  readonly kind: 'path' | 'content'
  readonly path: string
  readonly match: string
  readonly preview: string
  readonly line?: number
  readonly column?: number
}

export interface PreviewDocument {
  readonly html: string
  readonly synthesized: boolean
  readonly entryPath?: string
  readonly sourcePaths: readonly string[]
}

export class WorkspacePathError extends Error {
  constructor(path: string, reason: string) {
    super(`Unsafe workspace path "${path}": ${reason}`)
    this.name = 'WorkspacePathError'
  }
}

const FORBIDDEN_WORKSPACE_SEGMENTS = new Set(['.git', '.next', 'node_modules'])
const SECRET_WORKSPACE_FILENAMES = new Set([
  '.npmrc',
  '.pypirc',
  'credentials.json',
  'id_dsa',
  'id_ecdsa',
  'id_ed25519',
  'id_rsa',
])

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

export function isVirtualWorkspace(value: unknown): value is VirtualWorkspace {
  if (!isRecord(value)) return false
  if (
    typeof value.id !== 'string' ||
    typeof value.name !== 'string' ||
    typeof value.revision !== 'number' ||
    typeof value.createdAt !== 'string' ||
    typeof value.updatedAt !== 'string' ||
    typeof value.activeBranchId !== 'string' ||
    !Array.isArray(value.files) ||
    !Array.isArray(value.checkpoints) ||
    !Array.isArray(value.branches) ||
    !Array.isArray(value.diagnostics)
  ) {
    return false
  }

  return value.files.every(
    (file) =>
      isRecord(file) &&
      typeof file.path === 'string' &&
      isSafeWorkspacePath(file.path) &&
      typeof file.content === 'string' &&
      typeof file.language === 'string' &&
      typeof file.revision === 'number' &&
      Number.isInteger(file.revision) &&
      file.revision >= 0 &&
      typeof file.dirty === 'boolean',
  )
}

function isoNow() {
  return new Date().toISOString()
}

function assertRevision(revision: number, path: string) {
  if (!Number.isInteger(revision) || revision < 0) {
    throw new RangeError(`File revision for "${path}" must be a non-negative integer`)
  }
}

function sortFiles(files: readonly WorkspaceFile[]) {
  return [...files].sort((left, right) => left.path.localeCompare(right.path))
}

function nextWorkspace(
  workspace: VirtualWorkspace,
  patch: Partial<Omit<VirtualWorkspace, 'id' | 'name' | 'createdAt' | 'revision' | 'updatedAt'>>,
): VirtualWorkspace {
  return {
    ...workspace,
    ...patch,
    revision: workspace.revision + 1,
    updatedAt: isoNow(),
  }
}

function checkpointById(workspace: VirtualWorkspace, checkpointId: string) {
  const checkpoint = workspace.checkpoints.find((item) => item.id === checkpointId)
  if (!checkpoint) throw new Error(`Unknown workspace checkpoint "${checkpointId}"`)
  return checkpoint
}

function activeBranch(workspace: VirtualWorkspace) {
  const branch = workspace.branches.find((item) => item.id === workspace.activeBranchId)
  if (!branch) throw new Error(`Unknown active workspace branch "${workspace.activeBranchId}"`)
  return branch
}

function cloneDiagnostic(diagnostic: WorkspaceDiagnostic): WorkspaceDiagnostic {
  return {
    ...diagnostic,
    path: diagnostic.path ? normalizeWorkspacePath(diagnostic.path) : undefined,
  }
}

function immutableCheckpointFile(file: WorkspaceFile): WorkspaceFile {
  return Object.freeze({
    ...file,
    dirty: false,
  })
}

function immutableCheckpoint(checkpoint: WorkspaceCheckpoint): WorkspaceCheckpoint {
  const files = Object.freeze(checkpoint.files.map(immutableCheckpointFile))
  return Object.freeze({ ...checkpoint, files })
}

function uniqueId(prefix: string, existingIds: readonly string[]) {
  let suffix = existingIds.length + 1
  let candidate = `${prefix}-${suffix}`
  while (existingIds.includes(candidate)) {
    suffix += 1
    candidate = `${prefix}-${suffix}`
  }
  return candidate
}

function splitLines(content: string) {
  if (content.length === 0) return []
  return content.replace(/\r\n?/g, '\n').split('\n')
}

function escapeHtmlAttribute(value: string) {
  return value
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}

function injectBeforeClosingTag(document: string, tag: 'head' | 'body', content: string) {
  if (!content) return document
  const closingTag = new RegExp(`</${tag}\\s*>`, 'i')
  if (closingTag.test(document)) return document.replace(closingTag, `${content}\n</${tag}>`)
  return `${document}\n${content}`
}

function resolvePreviewAssetPath(entryPath: string, reference: string) {
  const cleanReference = reference.split(/[?#]/, 1)[0]
  if (
    !cleanReference ||
    cleanReference.startsWith('/') ||
    cleanReference.startsWith('#') ||
    /^(?:[a-z]+:|\/\/)/i.test(cleanReference)
  ) {
    return undefined
  }

  const segments = [...entryPath.split('/').slice(0, -1), ...cleanReference.replace(/\\/g, '/').split('/')]
  const resolved: string[] = []
  for (const segment of segments) {
    if (!segment || segment === '.') continue
    if (segment === '..') {
      if (resolved.length === 0) return undefined
      resolved.pop()
      continue
    }
    resolved.push(segment)
  }
  return resolved.length > 0 ? resolved.join('/') : undefined
}

function inlinePreviewAssets(workspace: VirtualWorkspace, entryPath: string, document: string) {
  const sourcePaths = new Set([entryPath])
  const filesByPath = new Map(workspace.files.map((file) => [file.path, file]))
  let html = document.replace(
    /<link\b([^>]*?)href=["']([^"']+)["']([^>]*)>/gi,
    (tag, before: string, reference: string, after: string) => {
      if (!/\brel=["']?stylesheet\b/i.test(`${before} ${after}`)) return tag
      const path = resolvePreviewAssetPath(entryPath, reference)
      const file = path ? filesByPath.get(path) : undefined
      if (!file || !/\.css$/i.test(file.path)) return tag
      sourcePaths.add(file.path)
      return `<style data-workspace-path="${escapeHtmlAttribute(file.path)}">\n${file.content.replace(/<\/style/gi, '<\\/style')}\n</style>`
    },
  )
  html = html.replace(
    /<script\b([^>]*?)src=["']([^"']+)["']([^>]*)>\s*<\/script>/gi,
    (tag, before: string, reference: string, after: string) => {
      const path = resolvePreviewAssetPath(entryPath, reference)
      const file = path ? filesByPath.get(path) : undefined
      if (!file || !/\.(?:js|mjs)$/i.test(file.path)) return tag
      sourcePaths.add(file.path)
      const attributes = `${before} ${after}`.replace(/\s+/g, ' ').trim()
      return `<script ${attributes} data-workspace-path="${escapeHtmlAttribute(file.path)}">\n${file.content.replace(/<\/script/gi, '<\\/script')}\n</script>`
    },
  )
  return { html, sourcePaths: Array.from(sourcePaths) }
}

export function normalizeWorkspacePath(path: string) {
  if (typeof path !== 'string' || path.length === 0) {
    throw new WorkspacePathError(String(path), 'path must not be empty')
  }
  if (path.includes('\0')) throw new WorkspacePathError(path, 'null bytes are not allowed')

  const slashPath = path.replace(/\\/g, '/')
  if (slashPath.startsWith('/') || /^[a-zA-Z]:/.test(slashPath)) {
    throw new WorkspacePathError(path, 'absolute paths are not allowed')
  }

  const normalizedSegments: string[] = []
  for (const segment of slashPath.split('/')) {
    if (segment === '' || segment === '.') continue

    let decodedSegment = segment
    try {
      decodedSegment = decodeURIComponent(segment)
    } catch {
      // Malformed URL escapes stay literal and cannot become traversal segments.
    }
    if (
      segment === '..' ||
      decodedSegment === '..' ||
      decodedSegment.includes('/') ||
      decodedSegment.includes('\\')
    ) {
      throw new WorkspacePathError(path, 'parent traversal is not allowed')
    }
    const lowered = decodedSegment.toLowerCase()
    if (FORBIDDEN_WORKSPACE_SEGMENTS.has(lowered)) {
      throw new WorkspacePathError(
        path,
        'generated, dependency, and source-control directories are not allowed',
      )
    }
    if (lowered.startsWith('.env') || SECRET_WORKSPACE_FILENAMES.has(lowered)) {
      throw new WorkspacePathError(path, 'secret-bearing files are not allowed')
    }
    if (
      /[<>:"|?*]/.test(decodedSegment) ||
      Array.from(decodedSegment).some((character) => {
        const code = character.charCodeAt(0)
        return code < 32 || code === 127
      })
    ) {
      throw new WorkspacePathError(path, 'unsupported filename characters are not allowed')
    }
    normalizedSegments.push(segment)
  }

  if (normalizedSegments.length === 0) {
    throw new WorkspacePathError(path, 'path must identify a file')
  }
  return normalizedSegments.join('/')
}

export function isSafeWorkspacePath(path: string) {
  try {
    normalizeWorkspacePath(path)
    return true
  } catch {
    return false
  }
}

export function inferWorkspaceLanguage(path: string) {
  const normalizedPath = normalizeWorkspacePath(path)
  const extension = normalizedPath.split('.').pop()?.toLowerCase()
  const languages: Record<string, string> = {
    css: 'css',
    html: 'html',
    htm: 'html',
    js: 'javascript',
    jsx: 'javascriptreact',
    json: 'json',
    md: 'markdown',
    mjs: 'javascript',
    scss: 'scss',
    ts: 'typescript',
    tsx: 'typescriptreact',
    txt: 'plaintext',
    yaml: 'yaml',
    yml: 'yaml',
  }
  return extension ? languages[extension] ?? 'plaintext' : 'plaintext'
}

export function createWorkspace(options: CreateWorkspaceOptions = {}): VirtualWorkspace {
  const createdAt = options.createdAt ?? isoNow()
  const branchId = options.branchId ?? 'main'
  const seenPaths = new Set<string>()
  const files = (options.files ?? []).map((input) => {
    const path = normalizeWorkspacePath(input.path)
    if (seenPaths.has(path)) throw new Error(`Duplicate workspace file "${path}"`)
    seenPaths.add(path)
    const revision = input.revision ?? 1
    assertRevision(revision, path)
    return {
      path,
      content: input.content,
      language: input.language ?? inferWorkspaceLanguage(path),
      revision,
      dirty: input.dirty ?? false,
    }
  })

  return {
    id: options.id ?? 'workspace',
    name: options.name ?? 'Untitled workspace',
    revision: 0,
    createdAt,
    updatedAt: createdAt,
    files: sortFiles(files),
    checkpoints: [],
    branches: [
      {
        id: branchId,
        name: options.branchName ?? 'main',
        createdAt,
        updatedAt: createdAt,
      },
    ],
    activeBranchId: branchId,
    diagnostics: (options.diagnostics ?? []).map(cloneDiagnostic),
  }
}

export function getWorkspaceFile(workspace: VirtualWorkspace, path: string) {
  const normalizedPath = normalizeWorkspacePath(path)
  return workspace.files.find((file) => file.path === normalizedPath)
}

export function upsertFile(workspace: VirtualWorkspace, input: WorkspaceFileInput): VirtualWorkspace {
  const path = normalizeWorkspacePath(input.path)
  const existing = workspace.files.find((file) => file.path === path)
  const language = input.language ?? existing?.language ?? inferWorkspaceLanguage(path)
  const dirty = input.dirty ?? true
  const revision = existing ? existing.revision + 1 : input.revision ?? 1
  assertRevision(revision, path)

  if (
    existing &&
    existing.content === input.content &&
    existing.language === language &&
    existing.dirty === dirty
  ) {
    return workspace
  }

  const updatedFile: WorkspaceFile = {
    path,
    content: input.content,
    language,
    revision,
    dirty,
  }
  const files = existing
    ? workspace.files.map((file) => (file.path === path ? updatedFile : file))
    : [...workspace.files, updatedFile]

  return nextWorkspace(workspace, { files: sortFiles(files) })
}

export function deleteFile(workspace: VirtualWorkspace, path: string): VirtualWorkspace {
  const normalizedPath = normalizeWorkspacePath(path)
  if (!workspace.files.some((file) => file.path === normalizedPath)) return workspace

  return nextWorkspace(workspace, {
    files: workspace.files.filter((file) => file.path !== normalizedPath),
    diagnostics: workspace.diagnostics.filter((diagnostic) => diagnostic.path !== normalizedPath),
  })
}

export function renameFile(workspace: VirtualWorkspace, fromPath: string, toPath: string) {
  const normalizedFromPath = normalizeWorkspacePath(fromPath)
  const normalizedToPath = normalizeWorkspacePath(toPath)
  if (normalizedFromPath === normalizedToPath) return workspace

  const source = workspace.files.find((file) => file.path === normalizedFromPath)
  if (!source) throw new Error(`Cannot rename missing workspace file "${normalizedFromPath}"`)
  if (workspace.files.some((file) => file.path === normalizedToPath)) {
    throw new Error(`Cannot overwrite workspace file "${normalizedToPath}" during rename`)
  }

  const files = workspace.files.map((file) =>
    file.path === normalizedFromPath
      ? {
          ...file,
          path: normalizedToPath,
          revision: file.revision + 1,
          dirty: true,
        }
      : file,
  )
  const diagnostics = workspace.diagnostics.map((diagnostic) =>
    diagnostic.path === normalizedFromPath
      ? { ...diagnostic, path: normalizedToPath }
      : diagnostic,
  )

  return nextWorkspace(workspace, { files: sortFiles(files), diagnostics })
}

export function setWorkspaceDiagnostics(
  workspace: VirtualWorkspace,
  diagnostics: readonly WorkspaceDiagnostic[],
) {
  return nextWorkspace(workspace, { diagnostics: diagnostics.map(cloneDiagnostic) })
}

export function computeLineDiff(before: string, after: string): LineDiff {
  const oldLines = splitLines(before)
  const newLines = splitLines(after)
  const table = Array.from(
    { length: oldLines.length + 1 },
    () => new Uint32Array(newLines.length + 1),
  )

  for (let oldIndex = oldLines.length - 1; oldIndex >= 0; oldIndex -= 1) {
    for (let newIndex = newLines.length - 1; newIndex >= 0; newIndex -= 1) {
      table[oldIndex][newIndex] =
        oldLines[oldIndex] === newLines[newIndex]
          ? table[oldIndex + 1][newIndex + 1] + 1
          : Math.max(table[oldIndex + 1][newIndex], table[oldIndex][newIndex + 1])
    }
  }

  const lines: LineDiffEntry[] = []
  let oldIndex = 0
  let newIndex = 0
  let additions = 0
  let deletions = 0
  let unchanged = 0

  while (oldIndex < oldLines.length || newIndex < newLines.length) {
    if (
      oldIndex < oldLines.length &&
      newIndex < newLines.length &&
      oldLines[oldIndex] === newLines[newIndex]
    ) {
      lines.push({
        kind: 'equal',
        content: oldLines[oldIndex],
        oldLineNumber: oldIndex + 1,
        newLineNumber: newIndex + 1,
      })
      oldIndex += 1
      newIndex += 1
      unchanged += 1
      continue
    }

    if (
      oldIndex < oldLines.length &&
      (newIndex >= newLines.length || table[oldIndex + 1][newIndex] >= table[oldIndex][newIndex + 1])
    ) {
      lines.push({
        kind: 'remove',
        content: oldLines[oldIndex],
        oldLineNumber: oldIndex + 1,
      })
      oldIndex += 1
      deletions += 1
      continue
    }

    lines.push({
      kind: 'add',
      content: newLines[newIndex],
      newLineNumber: newIndex + 1,
    })
    newIndex += 1
    additions += 1
  }

  return { lines, additions, deletions, unchanged }
}

export function createCheckpoint(
  workspace: VirtualWorkspace,
  options: CreateCheckpointOptions = {},
): VirtualWorkspace {
  const branch = activeBranch(workspace)
  const checkpointId =
    options.id ?? uniqueId('checkpoint', workspace.checkpoints.map((checkpoint) => checkpoint.id))
  if (workspace.checkpoints.some((checkpoint) => checkpoint.id === checkpointId)) {
    throw new Error(`Duplicate workspace checkpoint "${checkpointId}"`)
  }

  const createdAt = options.createdAt ?? isoNow()
  const checkpoint = immutableCheckpoint({
    id: checkpointId,
    label: options.label ?? `Checkpoint ${workspace.checkpoints.length + 1}`,
    message: options.message,
    branchId: branch.id,
    parentCheckpointId: branch.headCheckpointId,
    createdAt,
    files: workspace.files,
  })
  const files = workspace.files.map((file) => ({ ...file, dirty: false }))
  const branches = workspace.branches.map((item) =>
    item.id === branch.id
      ? {
          ...item,
          baseCheckpointId: item.baseCheckpointId ?? checkpoint.id,
          headCheckpointId: checkpoint.id,
          updatedAt: createdAt,
        }
      : item,
  )

  return nextWorkspace(workspace, {
    files,
    checkpoints: [...workspace.checkpoints, checkpoint],
    branches,
  })
}

export function restoreCheckpoint(workspace: VirtualWorkspace, checkpointId: string) {
  const checkpoint = checkpointById(workspace, checkpointId)
  const branch = activeBranch(workspace)
  const files = checkpoint.files.map((file) => ({ ...file, dirty: false }))
  const branches = workspace.branches.map((item) =>
    item.id === branch.id
      ? { ...item, headCheckpointId: checkpoint.id, updatedAt: isoNow() }
      : item,
  )

  return nextWorkspace(workspace, {
    files,
    branches,
    diagnostics: [],
  })
}

export function compareCheckpoints(
  workspace: VirtualWorkspace,
  fromCheckpointId: string,
  toCheckpointId: string,
): CheckpointComparison {
  const fromCheckpoint = checkpointById(workspace, fromCheckpointId)
  const toCheckpoint = checkpointById(workspace, toCheckpointId)
  const beforeByPath = new Map(fromCheckpoint.files.map((file) => [file.path, file]))
  const afterByPath = new Map(toCheckpoint.files.map((file) => [file.path, file]))
  const paths = Array.from(new Set([...beforeByPath.keys(), ...afterByPath.keys()])).sort()
  const summary: Record<CheckpointFileStatus, number> = {
    added: 0,
    deleted: 0,
    modified: 0,
    unchanged: 0,
  }

  const files = paths.map((path): CheckpointFileComparison => {
    const before = beforeByPath.get(path)
    const after = afterByPath.get(path)
    let status: CheckpointFileStatus
    if (!before) status = 'added'
    else if (!after) status = 'deleted'
    else if (before.content !== after.content || before.language !== after.language) status = 'modified'
    else status = 'unchanged'
    summary[status] += 1

    return {
      path,
      status,
      before,
      after,
      diff: computeLineDiff(before?.content ?? '', after?.content ?? ''),
    }
  })

  return {
    fromCheckpointId,
    toCheckpointId,
    files,
    summary,
  }
}

export function createBranch(
  workspace: VirtualWorkspace,
  options: CreateBranchOptions,
): VirtualWorkspace {
  const sourceBranch = activeBranch(workspace)
  const branchId = options.id ?? uniqueId('branch', workspace.branches.map((branch) => branch.id))
  if (workspace.branches.some((branch) => branch.id === branchId)) {
    throw new Error(`Duplicate workspace branch "${branchId}"`)
  }
  if (workspace.branches.some((branch) => branch.name === options.name)) {
    throw new Error(`Duplicate workspace branch name "${options.name}"`)
  }

  const fromCheckpointId = options.fromCheckpointId ?? sourceBranch.headCheckpointId
  if (fromCheckpointId) checkpointById(workspace, fromCheckpointId)
  const createdAt = options.createdAt ?? isoNow()
  const branch: WorkspaceBranch = {
    id: branchId,
    name: options.name,
    createdAt,
    updatedAt: createdAt,
    baseCheckpointId: fromCheckpointId,
    headCheckpointId: fromCheckpointId,
  }
  const next = nextWorkspace(workspace, {
    branches: [...workspace.branches, branch],
    activeBranchId: options.checkout ? branchId : workspace.activeBranchId,
  })
  return options.checkout && fromCheckpointId
    ? restoreCheckpoint(next, fromCheckpointId)
    : next
}

export function switchBranch(workspace: VirtualWorkspace, branchId: string) {
  const branch = workspace.branches.find((item) => item.id === branchId)
  if (!branch) throw new Error(`Unknown workspace branch "${branchId}"`)
  if (branch.id === workspace.activeBranchId) return workspace

  const files = branch.headCheckpointId
    ? checkpointById(workspace, branch.headCheckpointId).files.map((file) => ({ ...file, dirty: false }))
    : []
  return nextWorkspace(workspace, {
    activeBranchId: branch.id,
    files,
    diagnostics: [],
  })
}

export function searchFiles(
  workspace: VirtualWorkspace,
  query: string,
  options: SearchFilesOptions = {},
): WorkspaceSearchResult[] {
  if (!query) return []
  const includePaths = options.includePaths ?? true
  const includeContent = options.includeContent ?? true
  const maxResults = Math.max(0, options.maxResults ?? 100)
  if (maxResults === 0) return []

  const normalizedQuery = options.caseSensitive ? query : query.toLocaleLowerCase()
  const results: WorkspaceSearchResult[] = []
  const addResult = (result: WorkspaceSearchResult) => {
    if (results.length < maxResults) results.push(result)
  }

  for (const file of workspace.files) {
    if (includePaths) {
      const searchablePath = options.caseSensitive ? file.path : file.path.toLocaleLowerCase()
      const matchIndex = searchablePath.indexOf(normalizedQuery)
      if (matchIndex >= 0) {
        addResult({
          kind: 'path',
          path: file.path,
          match: file.path.slice(matchIndex, matchIndex + query.length),
          preview: file.path,
        })
      }
    }
    if (!includeContent || results.length >= maxResults) continue

    const lines = splitLines(file.content)
    for (let lineIndex = 0; lineIndex < lines.length && results.length < maxResults; lineIndex += 1) {
      const line = lines[lineIndex]
      const searchableLine = options.caseSensitive ? line : line.toLocaleLowerCase()
      let matchIndex = searchableLine.indexOf(normalizedQuery)
      while (matchIndex >= 0 && results.length < maxResults) {
        addResult({
          kind: 'content',
          path: file.path,
          match: line.slice(matchIndex, matchIndex + query.length),
          preview: line,
          line: lineIndex + 1,
          column: matchIndex + 1,
        })
        matchIndex = searchableLine.indexOf(normalizedQuery, matchIndex + Math.max(query.length, 1))
      }
    }
  }

  return results
}

export function derivePreviewDocument(workspace: VirtualWorkspace): PreviewDocument {
  const htmlFiles = workspace.files.filter((file) => /\.html?$/i.test(file.path))
  const indexFile = htmlFiles
    .filter((file) => file.path.toLowerCase() === 'index.html' || /\/index\.html$/i.test(file.path))
    .sort((left, right) => {
      const depthDifference = left.path.split('/').length - right.path.split('/').length
      return depthDifference || left.path.localeCompare(right.path)
    })[0]

  if (indexFile) {
    const inlined = inlinePreviewAssets(workspace, indexFile.path, indexFile.content)
    return {
      html: inlined.html,
      synthesized: false,
      entryPath: indexFile.path,
      sourcePaths: inlined.sourcePaths,
    }
  }

  const htmlFile = [...htmlFiles].sort((left, right) => left.path.localeCompare(right.path))[0]
  const cssFiles = workspace.files.filter((file) => /\.css$/i.test(file.path))
  const javascriptFiles = workspace.files.filter((file) => /\.(?:js|mjs)$/i.test(file.path))
  const styles = cssFiles
    .map(
      (file) =>
        `<style data-workspace-path="${escapeHtmlAttribute(file.path)}">\n${file.content.replace(/<\/style/gi, '<\\/style')}\n</style>`,
    )
    .join('\n')
  const scripts = javascriptFiles
    .map(
      (file) =>
        `<script type="module" data-workspace-path="${escapeHtmlAttribute(file.path)}">\n${file.content.replace(/<\/script/gi, '<\\/script')}\n</script>`,
    )
    .join('\n')

  let html: string
  if (htmlFile && /<(?:!doctype|html|head|body)\b/i.test(htmlFile.content)) {
    html = htmlFile.content
    html = injectBeforeClosingTag(html, 'head', styles)
    html = injectBeforeClosingTag(html, 'body', scripts)
  } else {
    const body = htmlFile?.content ?? '<div id="app"></div>'
    html = [
      '<!doctype html>',
      '<html lang="en">',
      '<head>',
      '<meta charset="utf-8">',
      '<meta name="viewport" content="width=device-width, initial-scale=1">',
      styles,
      '</head>',
      '<body>',
      body,
      scripts,
      '</body>',
      '</html>',
    ]
      .filter(Boolean)
      .join('\n')
  }

  return {
    html,
    synthesized: true,
    entryPath: htmlFile?.path,
    sourcePaths: [
      ...(htmlFile ? [htmlFile.path] : []),
      ...cssFiles.map((file) => file.path),
      ...javascriptFiles.map((file) => file.path),
    ],
  }
}
