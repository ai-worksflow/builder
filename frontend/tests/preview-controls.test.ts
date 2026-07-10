import assert from 'node:assert/strict'
import {
  PREVIEW_DEVICE_DIMENSIONS,
  PREVIEW_SANDBOX_CSP,
  buildPreviewErrorComposerContext,
  buildPreviewSelectionComposerContext,
  calculatePreviewFitScale,
  createSandboxPreviewDocument,
  inlineSafePreviewAssets,
  normalizePreviewInspectionRect,
  normalizePreviewRoute,
  parsePreviewDimension,
} from '../lib/worksflow/preview-controls'

type TestCase = {
  name: string
  run: () => void
}

const tests: TestCase[] = []

function test(name: string, run: () => void) {
  tests.push({ name, run })
}

test('parses whole pixel dimensions only inside the safe ranges', () => {
  assert.equal(parsePreviewDimension('390', 'width'), 390)
  assert.equal(parsePreviewDimension(' 844px ', 'height'), 844)
  assert.equal(parsePreviewDimension('279', 'width'), null)
  assert.equal(parsePreviewDimension('1601', 'height'), null)
  assert.equal(parsePreviewDimension('390.5', 'width'), null)
  assert.equal(parsePreviewDimension('auto', 'height'), null)
})

test('normalizes absolute, relative and hash-backed virtual routes', () => {
  assert.equal(normalizePreviewRoute('dashboard'), '/dashboard')
  assert.equal(normalizePreviewRoute('../settings?tab=team', '/projects/42/'), '/projects/settings?tab=team')
  assert.equal(normalizePreviewRoute('#/reports/daily'), '/reports/daily')
  assert.equal(normalizePreviewRoute('https://example.com/docs?q=1#intro'), '/docs?q=1#intro')
  assert.equal(normalizePreviewRoute(''), '/')
})

test('keeps the current route for unsupported or malformed protocols', () => {
  assert.equal(normalizePreviewRoute('javascript:alert(1)', '/safe'), '/safe')
  assert.equal(normalizePreviewRoute('data:text/html,unsafe', '/safe'), '/safe')
  assert.equal(normalizePreviewRoute('\u0000javascript:alert(1)', '/safe'), '/safe')
})

test('calculates a bounded fit scale and preserves smaller previews at 100%', () => {
  assert.equal(calculatePreviewFitScale(1400, 900, PREVIEW_DEVICE_DIMENSIONS.desktop), 1)
  assert.equal(calculatePreviewFitScale(664, 424, PREVIEW_DEVICE_DIMENSIONS.desktop), 0.5)
  assert.equal(calculatePreviewFitScale(100, 100, { width: 0, height: 0 }), 1)
})

test('normalizes drag rectangles in either direction and clamps them to the viewport', () => {
  assert.deepEqual(
    normalizePreviewInspectionRect(220.129, 160.456, -20, 20, { width: 200, height: 120 }),
    { x: 0, y: 20, width: 200, height: 100 },
  )
  assert.deepEqual(
    normalizePreviewInspectionRect(10.111, 20.222, 30.333, 50.555),
    { x: 10.11, y: 20.22, width: 20.22, height: 30.33 },
  )
  assert.equal(normalizePreviewInspectionRect(Number.NaN, 0, 10, 10), null)
})

test('builds structured error and region contexts with the active route', () => {
  const errorContext = buildPreviewErrorComposerContext({
    level: 'error',
    kind: 'runtime',
    message: 'Cannot read properties of undefined',
    route: '/tasks/42?tab=details',
    stack: 'TypeError: Cannot read properties of undefined',
    source: 'app.js',
    line: 18,
    column: 7,
  })
  assert.match(errorContext, /"contextType": "preview-runtime-error"/)
  assert.match(errorContext, /"route": "\/tasks\/42\?tab=details"/)
  assert.match(errorContext, /"line": 18/)

  const regionContext = buildPreviewSelectionComposerContext({
    kind: 'region',
    selector: 'main > section:nth-of-type(2)',
    route: '/dashboard',
    rect: { x: 12, y: 24, width: 320, height: 180 },
    text: 'Quarterly revenue',
  })
  assert.match(regionContext, /"selectionKind": "region"/)
  assert.match(regionContext, /"width": 320/)
  assert.match(regionContext, /Describe the desired change here/)
})

test('inlines safe relative workspace images, fonts and media without enabling network URLs', () => {
  const png = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB'
  const font = 'data:font/woff2;base64,d09GMgAAAAA='
  const video = 'data:video/mp4;base64,AAAAHGZ0eXA='
  const html = `<html><head>
    <style data-workspace-path="styles/site.css">
      @font-face { font-family: Demo; src: url('../assets/demo.woff2') }
      .hero { background-image: url('../assets/hero.png') }
    </style>
  </head><body>
    <img src="assets/hero.png" srcset="assets/hero.png 1x, assets/hero@2x.png 2x">
    <img src="/assets/hero.png">
    <video poster="assets/hero.png"><source src="assets/intro.mp4"></video>
    <img src="https://example.com/tracker.png">
    <img src="../../secret.png">
  </body></html>`
  const inlined = inlineSafePreviewAssets(html, 'index.html', [
    { path: 'assets/hero.png', content: png },
    { path: 'assets/hero@2x.png', content: png },
    { path: 'assets/demo.woff2', content: font },
    { path: 'assets/intro.mp4', content: video },
    { path: 'secret.png', content: png },
  ])

  assert.ok(inlined.includes(`src="${png}"`))
  assert.ok(inlined.includes(`url("${font}")`))
  assert.ok(inlined.includes(`url("${png}")`))
  assert.ok(inlined.includes(`src="${video}"`))
  assert.ok(inlined.includes(`${png} 1x, ${png} 2x`))
  assert.ok(!inlined.includes('src="/assets/hero.png"'))
  assert.ok(inlined.includes('src="https://example.com/tracker.png"'))
  assert.ok(inlined.includes('src="../../secret.png"'))
})

test('rejects mismatched or executable data payloads during asset inlining', () => {
  const html = '<img src="assets/photo.png"><img src="assets/icon.svg">'
  const inlined = inlineSafePreviewAssets(html, 'index.html', [
    { path: 'assets/photo.png', content: 'data:text/html,<script>alert(1)</script>' },
    { path: 'assets/icon.svg', content: '<svg viewBox="0 0 1 1"><path d="M0 0"/></svg>' },
  ])
  assert.ok(inlined.includes('src="assets/photo.png"'))
  assert.ok(inlined.includes('src="data:image/svg+xml;charset=utf-8,'))
  assert.ok(!inlined.includes('data:text/html'))
})

test('creates a strict sandbox bridge with structured errors, region selection and keyboard escape', () => {
  const document = createSandboxPreviewDocument(
    '<html><head><title>Preview</title></head><body></body></html>',
    'channel</script>',
    'region',
  )
  assert.ok(document.includes(`content="${PREVIEW_SANDBOX_CSP}"`))
  assert.ok(document.includes('const inspectionMode = "region"'))
  assert.ok(document.includes("type: 'region-selected'"))
  assert.ok(document.includes("kind: 'runtime'"))
  assert.ok(document.includes("route: virtualRoute"))
  assert.ok(document.includes("event.key === 'Escape'"))
  assert.ok(document.includes('pointercancel'))
  assert.ok(document.includes('channel\\u003c/script>'))
  assert.ok(!PREVIEW_SANDBOX_CSP.includes('https:'))
  assert.ok(!PREVIEW_SANDBOX_CSP.includes('allow-same-origin'))
  const bridgeSource = document.match(/<script>([\s\S]*?)<\/script>/)?.[1]
  assert.ok(bridgeSource)
  assert.doesNotThrow(() => Function(bridgeSource))
})

let failures = 0
for (const item of tests) {
  try {
    item.run()
    console.log(`✓ ${item.name}`)
  } catch (error) {
    failures += 1
    console.error(`✗ ${item.name}`)
    console.error(error)
  }
}

if (failures > 0) process.exitCode = 1
else console.log(`\n${tests.length} preview control tests passed`)
