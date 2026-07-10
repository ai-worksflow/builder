import assert from 'node:assert/strict'
import {
  WorkspacePathError,
  compareCheckpoints,
  computeLineDiff,
  createCheckpoint,
  createWorkspace,
  deleteFile,
  derivePreviewDocument,
  getWorkspaceFile,
  isSafeWorkspacePath,
  normalizeWorkspacePath,
  renameFile,
  restoreCheckpoint,
  searchFiles,
  upsertFile,
} from '../lib/worksflow/workspace-model'

type TestCase = {
  name: string
  run: () => void
}

const tests: TestCase[] = []

function test(name: string, run: () => void) {
  tests.push({ name, run })
}

test('normalizes safe relative paths and rejects traversal or absolute paths', () => {
  assert.equal(normalizeWorkspacePath('./src//components\\app.tsx'), 'src/components/app.tsx')
  assert.equal(isSafeWorkspacePath('public/index.html'), true)
  assert.equal(isSafeWorkspacePath('../secrets.env'), false)

  const unsafePaths = [
    '../secrets.env',
    'src/../secrets.env',
    'src/%2e%2e/secrets.env',
    '/etc/passwd',
    'C:\\Users\\secret.txt',
    '\\\\server\\share.txt',
    '.env.local',
    'node_modules/pkg/index.js',
    '.git/config',
    'credentials.json',
    '.',
  ]
  unsafePaths.forEach((path) => {
    assert.throws(() => normalizeWorkspacePath(path), WorkspacePathError)
  })
  assert.throws(
    () => createWorkspace({ files: [{ path: '../outside.ts', content: '' }] }),
    WorkspacePathError,
  )
})

test('upserts, renames and deletes files without mutating prior workspaces', () => {
  const original = createWorkspace({
    files: [
      { path: 'src/app.ts', content: 'export const value = 1', revision: 3 },
      { path: 'src/keep.ts', content: 'export {}' },
    ],
    diagnostics: [
      { id: 'd1', path: 'src/app.ts', severity: 'warning', message: 'Example warning' },
    ],
  })
  const edited = upsertFile(original, {
    path: './src/app.ts',
    content: 'export const value = 2',
  })
  const renamed = renameFile(edited, 'src/app.ts', 'src/value.ts')
  const deleted = deleteFile(renamed, 'src/value.ts')

  assert.equal(getWorkspaceFile(original, 'src/app.ts')?.content, 'export const value = 1')
  assert.equal(getWorkspaceFile(original, 'src/app.ts')?.revision, 3)
  assert.equal(getWorkspaceFile(edited, 'src/app.ts')?.content, 'export const value = 2')
  assert.equal(getWorkspaceFile(edited, 'src/app.ts')?.revision, 4)
  assert.equal(getWorkspaceFile(edited, 'src/app.ts')?.dirty, true)
  assert.equal(getWorkspaceFile(renamed, 'src/app.ts'), undefined)
  assert.equal(getWorkspaceFile(renamed, 'src/value.ts')?.revision, 5)
  assert.equal(renamed.diagnostics[0]?.path, 'src/value.ts')
  assert.equal(getWorkspaceFile(deleted, 'src/value.ts'), undefined)
  assert.ok(getWorkspaceFile(deleted, 'src/keep.ts'))
  assert.equal(deleted.diagnostics.length, 0)
})

test('computes a stable LCS line diff with line numbers', () => {
  const diff = computeLineDiff('alpha\nbeta\ngamma', 'alpha\nbeta changed\ngamma\ndelta')

  assert.deepEqual(
    diff.lines.map((line) => line.kind),
    ['equal', 'remove', 'add', 'equal', 'add'],
  )
  assert.equal(diff.additions, 2)
  assert.equal(diff.deletions, 1)
  assert.equal(diff.unchanged, 2)
  assert.deepEqual(diff.lines[2], {
    kind: 'add',
    content: 'beta changed',
    newLineNumber: 2,
  })
})

test('checkpoints are immutable and restore rolls the working tree back', () => {
  const initial = createWorkspace({
    files: [{ path: 'src/app.js', content: 'const version = 1', dirty: true }],
  })
  const firstCheckpointState = createCheckpoint(initial, {
    id: 'cp-1',
    label: 'Version one',
    createdAt: '2026-07-10T00:00:00.000Z',
  })
  const checkpoint = firstCheckpointState.checkpoints[0]

  assert.equal(initial.files[0].dirty, true)
  assert.equal(firstCheckpointState.files[0].dirty, false)
  assert.equal(Object.isFrozen(checkpoint), true)
  assert.equal(Object.isFrozen(checkpoint.files), true)
  assert.equal(Object.isFrozen(checkpoint.files[0]), true)

  const edited = upsertFile(firstCheckpointState, {
    path: 'src/app.js',
    content: 'const version = 2',
  })
  const expanded = upsertFile(edited, { path: 'src/new.js', content: 'export const added = true' })
  const secondCheckpointState = createCheckpoint(expanded, {
    id: 'cp-2',
    label: 'Version two',
    createdAt: '2026-07-10T00:01:00.000Z',
  })
  const comparison = compareCheckpoints(secondCheckpointState, 'cp-1', 'cp-2')
  const restored = restoreCheckpoint(secondCheckpointState, 'cp-1')

  assert.deepEqual(comparison.summary, { added: 1, deleted: 0, modified: 1, unchanged: 0 })
  assert.equal(
    comparison.files.find((file) => file.path === 'src/app.js')?.diff.additions,
    1,
  )
  assert.equal(getWorkspaceFile(restored, 'src/app.js')?.content, 'const version = 1')
  assert.equal(getWorkspaceFile(restored, 'src/app.js')?.dirty, false)
  assert.equal(getWorkspaceFile(restored, 'src/new.js'), undefined)
  assert.equal(getWorkspaceFile(secondCheckpointState, 'src/app.js')?.content, 'const version = 2')
})

test('searches file paths and content with stable line and column locations', () => {
  const workspace = createWorkspace({
    files: [
      {
        path: 'src/TodoList.ts',
        content: 'export function render() {\n  return "TODO item"\n}',
      },
      { path: 'docs/readme.md', content: 'Nothing here' },
    ],
  })
  const results = searchFiles(workspace, 'todo')
  const contentResult = results.find((result) => result.kind === 'content')

  assert.equal(results.length, 2)
  assert.equal(results[0].kind, 'path')
  assert.equal(results[0].path, 'src/TodoList.ts')
  assert.equal(contentResult?.match, 'TODO')
  assert.equal(contentResult?.line, 2)
  assert.equal(contentResult?.column, 11)
  assert.deepEqual(searchFiles(workspace, 'TODO', { caseSensitive: true, includePaths: false }), [
    {
      kind: 'content',
      path: 'src/TodoList.ts',
      match: 'TODO',
      preview: '  return "TODO item"',
      line: 2,
      column: 11,
    },
  ])
})

test('preview prefers index.html and otherwise synthesizes HTML, CSS and JavaScript', () => {
  const indexed = createWorkspace({
    files: [
      { path: 'index.html', content: '<!doctype html><p>Preferred entry</p>' },
      { path: 'src/view.html', content: '<p>Ignored fragment</p>' },
      { path: 'src/app.js', content: 'console.log("ignored")' },
    ],
  })
  const directPreview = derivePreviewDocument(indexed)
  assert.equal(directPreview.synthesized, false)
  assert.equal(directPreview.entryPath, 'index.html')
  assert.equal(directPreview.html, '<!doctype html><p>Preferred entry</p>')
  assert.deepEqual(directPreview.sourcePaths, ['index.html'])

  const composable = createWorkspace({
    files: [
      { path: 'src/view.html', content: '<main id="root">Generated preview</main>' },
      { path: 'src/theme.css', content: '#root { color: rebeccapurple; }' },
      { path: 'src/app.js', content: 'document.querySelector("#root")?.setAttribute("data-ready", "true")' },
    ],
  })
  const synthesized = derivePreviewDocument(composable)

  assert.equal(synthesized.synthesized, true)
  assert.equal(synthesized.entryPath, 'src/view.html')
  assert.ok(synthesized.html.startsWith('<!doctype html>'))
  assert.ok(synthesized.html.includes('<main id="root">Generated preview</main>'))
  assert.ok(synthesized.html.includes('#root { color: rebeccapurple; }'))
  assert.ok(synthesized.html.includes('document.querySelector("#root")'))
  assert.deepEqual(synthesized.sourcePaths, ['src/view.html', 'src/theme.css', 'src/app.js'])
})

test('preview inlines local stylesheet and script references for srcDoc execution', () => {
  const workspace = createWorkspace({
    files: [
      {
        path: 'index.html',
        content: '<!doctype html><html><head><link rel="stylesheet" href="styles.css"></head><body><main>Ready</main><script type="module" src="app.js"></script></body></html>',
      },
      { path: 'styles.css', content: 'main { color: blue; }' },
      { path: 'app.js', content: 'document.body.dataset.ready = "true"' },
    ],
  })

  const preview = derivePreviewDocument(workspace)
  assert.match(preview.html, /data-workspace-path="styles\.css"/)
  assert.match(preview.html, /main \{ color: blue; \}/)
  assert.match(preview.html, /data-workspace-path="app\.js"/)
  assert.match(preview.html, /document\.body\.dataset\.ready/)
  assert.deepEqual([...preview.sourcePaths].sort(), ['app.js', 'index.html', 'styles.css'])
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
  console.error(`${failed} workspace model test(s) failed.`)
  process.exit(1)
}

console.log(`${tests.length} workspace model test(s) passed.`)
