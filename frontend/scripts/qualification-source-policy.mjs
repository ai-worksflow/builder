import { readFileSync } from 'node:fs'
import ts from 'typescript'

import {
  qualificationFail,
  requireRelativePath,
  resolveRegularFile,
} from './qualification-core.mjs'

const forbiddenPlaywrightMethods = new Set([
  'abort',
  'fail',
  'fixme',
  'fulfill',
  'only',
  'route',
  'routeFromHAR',
  'setContent',
  'skip',
  'unroute',
])

const forbiddenImportSegment = /(?:^|[/_.-])(fake|fixture|mock|stub)s?(?:$|[/_.-])/i

function propertyName(expression) {
  if (ts.isPropertyAccessExpression(expression)) return expression.name.text
  if (ts.isElementAccessExpression(expression) && ts.isStringLiteral(expression.argumentExpression)) {
    return expression.argumentExpression.text
  }
  return ''
}

function lineAndColumn(source, node) {
  const location = source.getLineAndCharacterOfPosition(node.getStart(source))
  return `${source.fileName}:${location.line + 1}:${location.character + 1}`
}

export function validateGoldenSource(root, relativePath) {
  requireRelativePath(relativePath, 'Golden source path', 'frontend/tests')
  if (!/^frontend\/tests\/golden-[a-z0-9-]+\.spec\.ts$/.test(relativePath)) {
    qualificationFail(`Golden source must use frontend/tests/golden-*.spec.ts: ${relativePath}`)
  }
  const file = resolveRegularFile(root, relativePath, 'Golden source', { maximumBytes: 1 << 20 })
  const sourceText = readFileSync(file.absolute, 'utf8')
  const source = ts.createSourceFile(relativePath, sourceText, ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS)
  if (source.parseDiagnostics.length > 0) {
    qualificationFail(`Golden source has TypeScript parse errors: ${relativePath}`)
  }

  let importsQualificationRuntime = false
  function visit(node) {
    if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
      const moduleName = node.moduleSpecifier.text
      if (moduleName === '@playwright/test') {
        qualificationFail(`Golden source must import the reviewed qualification runtime, not @playwright/test, at ${lineAndColumn(source, node)}`)
      }
      if (moduleName === './qualification-runtime') importsQualificationRuntime = true
      if (forbiddenImportSegment.test(moduleName)) {
        qualificationFail(`Golden source imports a mock/fake/fixture/stub module at ${lineAndColumn(source, node)}`)
      }
    }
    if (ts.isCallExpression(node)) {
      const method = propertyName(node.expression)
      if (forbiddenPlaywrightMethods.has(method)) {
        qualificationFail(`Golden source uses forbidden Playwright method ${method} at ${lineAndColumn(source, node)}`)
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(source)
  if (!importsQualificationRuntime) {
    qualificationFail(`Golden source must import ./qualification-runtime: ${relativePath}`)
  }
}

export function validateGoldenSources(root, paths) {
  for (const path of paths) validateGoldenSource(root, path)
}
