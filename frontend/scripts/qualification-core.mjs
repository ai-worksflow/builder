import { createHash } from 'node:crypto'
import {
  closeSync,
  lstatSync,
  openSync,
  readFileSync,
  readSync,
  realpathSync,
  statSync,
} from 'node:fs'
import { isAbsolute, relative, resolve, sep } from 'node:path'
import { TextDecoder } from 'node:util'

export const canonicalUUIDv4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
export const sha256Hex = /^[0-9a-f]{64}$/
export const sha256Identity = /^sha256:[0-9a-f]{64}$/
export const stableId = /^[a-z0-9]+(?:[._/-][a-z0-9]+)*$/

export function qualificationFail(message) {
  throw new Error(`qualification receipt: ${message}`)
}

export function compareCanonicalUTF8(left, right) {
  return Buffer.compare(Buffer.from(left, 'utf8'), Buffer.from(right, 'utf8'))
}

function canonicalJSONString(value, location) {
  // Go replaces lone UTF-16 surrogates while JavaScript preserves them in
  // JSON.stringify. Reject them so both qualification implementations hash
  // the same Unicode scalar sequence instead of silently diverging.
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (!(next >= 0xdc00 && next <= 0xdfff)) {
        qualificationFail(`${location} contains an unpaired UTF-16 surrogate`)
      }
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      qualificationFail(`${location} contains an unpaired UTF-16 surrogate`)
    }
  }
  // encoding/json with SetEscapeHTML(false) still escapes the two JavaScript
  // line separators. Preserve that exact cross-language wire form.
  return JSON.stringify(value)
    .replaceAll('\u2028', '\\u2028')
    .replaceAll('\u2029', '\\u2029')
}

export function canonicalJSON(value, location = '$') {
  if (value === null) return 'null'
  if (typeof value === 'string') return canonicalJSONString(value, location)
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || Object.is(value, -0)) {
      qualificationFail(`${location} must use a safe canonical integer`)
    }
    return String(value)
  }
  if (Array.isArray(value)) {
    return `[${value.map((entry, index) => canonicalJSON(entry, `${location}[${index}]`)).join(',')}]`
  }
  if (typeof value !== 'object' || Object.getPrototypeOf(value) !== Object.prototype) {
    qualificationFail(`${location} contains a non-JSON value`)
  }
  const keys = Object.keys(value).sort(compareCanonicalUTF8)
  return `{${keys.map((key) => `${canonicalJSONString(key, `${location} key`)}:${canonicalJSON(value[key], `${location}.${key}`)}`).join(',')}}`
}

export function parseCanonicalJSON(bytes, label, maximumBytes = 2 << 20) {
  if (!Buffer.isBuffer(bytes)) bytes = Buffer.from(bytes)
  if (bytes.length === 0 || bytes.length > maximumBytes || bytes.includes(0)) {
    qualificationFail(`${label} must contain 1..${maximumBytes} non-NUL bytes`)
  }
  const raw = bytes.toString('utf8')
  if (Buffer.byteLength(raw, 'utf8') !== bytes.length || raw.startsWith('\ufeff')) {
    qualificationFail(`${label} must be canonical UTF-8 without a BOM`)
  }
  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch (error) {
    qualificationFail(`${label} is not valid JSON: ${error instanceof Error ? error.message : String(error)}`)
  }
  const canonical = canonicalJSON(parsed)
  if (raw !== canonical && raw !== `${canonical}\n`) {
    qualificationFail(`${label} must be canonical JSON; duplicate names and formatting variants are rejected`)
  }
  return parsed
}

function rejectDuplicateJSONNames(raw, label) {
  let index = 0
  let nodes = 0
  const maximumDepth = 128
  const maximumNodes = 100_000
  const whitespace = /[\t\n\r ]/

  function skipWhitespace() {
    while (index < raw.length && whitespace.test(raw[index])) index += 1
  }

  function scanString() {
    const start = index
    if (raw[index] !== '"') qualificationFail(`${label} contains a malformed JSON string`)
    index += 1
    while (index < raw.length) {
      const character = raw[index]
      if (character === '"') {
        index += 1
        try {
          return JSON.parse(raw.slice(start, index))
        } catch {
          qualificationFail(`${label} contains a malformed JSON string`)
        }
      }
      if (character === '\\') index += 1
      index += 1
    }
    qualificationFail(`${label} contains an unterminated JSON string`)
  }

  function scanPrimitive() {
    const start = index
    while (index < raw.length && !/[\t\n\r ,\]}]/.test(raw[index])) index += 1
    if (index === start) qualificationFail(`${label} contains a malformed JSON value`)
  }

  function scanValue(depth) {
    nodes += 1
    if (nodes > maximumNodes || depth > maximumDepth) qualificationFail(`${label} exceeds strict JSON complexity limits`)
    skipWhitespace()
    if (raw[index] === '{') {
      index += 1
      skipWhitespace()
      const names = new Set()
      if (raw[index] === '}') {
        index += 1
        return
      }
      for (;;) {
        skipWhitespace()
        const name = scanString()
        if (names.has(name)) qualificationFail(`${label} contains duplicate JSON name ${JSON.stringify(name)}`)
        names.add(name)
        skipWhitespace()
        if (raw[index] !== ':') qualificationFail(`${label} contains a malformed JSON object`)
        index += 1
        scanValue(depth + 1)
        skipWhitespace()
        if (raw[index] === '}') {
          index += 1
          return
        }
        if (raw[index] !== ',') qualificationFail(`${label} contains a malformed JSON object`)
        index += 1
      }
    }
    if (raw[index] === '[') {
      index += 1
      skipWhitespace()
      if (raw[index] === ']') {
        index += 1
        return
      }
      for (;;) {
        scanValue(depth + 1)
        skipWhitespace()
        if (raw[index] === ']') {
          index += 1
          return
        }
        if (raw[index] !== ',') qualificationFail(`${label} contains a malformed JSON array`)
        index += 1
      }
    }
    if (raw[index] === '"') {
      scanString()
      return
    }
    scanPrimitive()
  }

  scanValue(0)
  skipWhitespace()
  if (index !== raw.length) qualificationFail(`${label} contains trailing JSON content`)
}

export function parseStrictJSON(bytes, label, maximumBytes = 2 << 20) {
  if (!Buffer.isBuffer(bytes)) bytes = Buffer.from(bytes)
  if (bytes.length === 0 || bytes.length > maximumBytes || bytes.includes(0)) {
    qualificationFail(`${label} must contain 1..${maximumBytes} non-NUL bytes`)
  }
  let raw
  try {
    raw = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    qualificationFail(`${label} must use valid UTF-8`)
  }
  if (raw.startsWith('\ufeff') || raw.includes('\ufffd')) {
    qualificationFail(`${label} must use BOM-free UTF-8 without replacement characters`)
  }
  rejectDuplicateJSONNames(raw, label)
  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch (error) {
    qualificationFail(`${label} is not valid JSON: ${error instanceof Error ? error.message : String(error)}`)
  }
  canonicalJSON(parsed, label)
  return parsed
}

export function readCanonicalJSONFile(path, label, maximumBytes) {
  return parseCanonicalJSON(readFileSync(path), label, maximumBytes)
}

export function requireObject(value, label) {
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.getPrototypeOf(value) !== Object.prototype) {
    qualificationFail(`${label} must be an object`)
  }
  return value
}

export function requireExactKeys(value, required, optional, label) {
  requireObject(value, label)
  const requiredSet = new Set(required)
  const optionalSet = new Set(optional)
  for (const key of required) {
    if (!Object.hasOwn(value, key)) qualificationFail(`${label}.${key} is required`)
  }
  for (const key of Object.keys(value)) {
    if (!requiredSet.has(key) && !optionalSet.has(key)) qualificationFail(`${label}.${key} is not supported`)
  }
  return value
}

export function requireString(value, label, options = {}) {
  const { maximumBytes = 4096, pattern, exact } = options
  if (typeof value !== 'string' || value.length === 0 || value.trim() !== value || /[\r\n\0]/.test(value) || Buffer.byteLength(value) > maximumBytes) {
    qualificationFail(`${label} must be a bounded canonical string`)
  }
  if (exact !== undefined && value !== exact) qualificationFail(`${label} must be ${exact}`)
  if (pattern && !pattern.test(value)) qualificationFail(`${label} has an invalid format`)
  return value
}

export function requireBoolean(value, label, exact) {
  if (typeof value !== 'boolean' || (exact !== undefined && value !== exact)) {
    qualificationFail(`${label} must be ${exact === undefined ? 'boolean' : String(exact)}`)
  }
  return value
}

export function requireInteger(value, label, minimum = 0, maximum = Number.MAX_SAFE_INTEGER) {
  if (!Number.isSafeInteger(value) || value < minimum || value > maximum) {
    qualificationFail(`${label} must be an integer between ${minimum} and ${maximum}`)
  }
  return value
}

export function requireSortedUniqueStrings(value, label, options = {}) {
  const { minimum = 1, maximum = 256, pattern = stableId } = options
  if (!Array.isArray(value) || value.length < minimum || value.length > maximum) {
    qualificationFail(`${label} must contain ${minimum}..${maximum} values`)
  }
  let prior = ''
  for (const [index, entry] of value.entries()) {
    requireString(entry, `${label}[${index}]`, { pattern })
    if (index > 0 && compareCanonicalUTF8(entry, prior) <= 0) qualificationFail(`${label} must be strictly sorted and unique`)
    prior = entry
  }
  return value
}

export function requireTimestamp(value, label) {
  requireString(value, label, { maximumBytes: 64 })
  const parsed = Date.parse(value)
  if (!Number.isFinite(parsed) || new Date(parsed).toISOString() !== value) {
    qualificationFail(`${label} must be an exact UTC ISO-8601 timestamp`)
  }
  return parsed
}

export function requireRelativePath(value, label, prefix) {
  requireString(value, label, { maximumBytes: 1024 })
  if (!/^[A-Za-z0-9._/-]+$/.test(value) || isAbsolute(value) || value.includes('\\') || value.startsWith('./') || value.endsWith('/') || value.split('/').some((part) => part === '' || part === '.' || part === '..')) {
    qualificationFail(`${label} must be a normalized repository-relative file path`)
  }
  if (prefix && value !== prefix && !value.startsWith(`${prefix}/`)) {
    qualificationFail(`${label} must remain under ${prefix}`)
  }
  return value
}

export function resolveRegularFile(root, relativePath, label, options = {}) {
  const { prefix, maximumBytes } = options
  requireRelativePath(relativePath, label, prefix)
  const absoluteRoot = realpathSync(root)
  const absolute = resolve(absoluteRoot, relativePath)
  if (absolute !== absoluteRoot && !absolute.startsWith(`${absoluteRoot}${sep}`)) {
    qualificationFail(`${label} escapes the repository root`)
  }
  let linkStat
  try {
    linkStat = lstatSync(absolute)
  } catch {
    qualificationFail(`${label} does not exist: ${relativePath}`)
  }
  if (linkStat.isSymbolicLink() || !linkStat.isFile()) {
    qualificationFail(`${label} must be a regular non-symlink file`)
  }
  const actual = realpathSync(absolute)
  const escaped = relative(absoluteRoot, actual)
  if (escaped.startsWith('..') || isAbsolute(escaped)) qualificationFail(`${label} resolves outside the repository root`)
  const fileStat = statSync(actual)
  if (maximumBytes !== undefined && (fileStat.size < 1 || fileStat.size > maximumBytes)) {
    qualificationFail(`${label} must contain 1..${maximumBytes} bytes`)
  }
  return { absolute: actual, stat: fileStat }
}

export function hashFileSHA256(path) {
  const hash = createHash('sha256')
  const descriptor = openSync(path, 'r')
  const chunk = Buffer.allocUnsafe(1 << 20)
  try {
    for (;;) {
      const count = readSync(descriptor, chunk, 0, chunk.length, null)
      if (count === 0) break
      hash.update(chunk.subarray(0, count))
    }
  } finally {
    closeSync(descriptor)
  }
  return hash.digest('hex')
}

export function hashBytesSHA256(bytes) {
  return createHash('sha256').update(bytes).digest('hex')
}

export function decodeCanonicalBase64(value, label, maximumBytes = 8 << 20) {
  requireString(value, label, { maximumBytes: Math.ceil(maximumBytes * 4 / 3) + 8, pattern: /^[A-Za-z0-9+/]+={0,2}$/ })
  const decoded = Buffer.from(value, 'base64')
  if (decoded.length === 0 || decoded.length > maximumBytes || decoded.toString('base64') !== value) {
    qualificationFail(`${label} must be canonical standard base64 for 1..${maximumBytes} bytes`)
  }
  return decoded
}

export function dssePAE(payloadType, payload) {
  return Buffer.concat([
    Buffer.from(`DSSEv1 ${Buffer.byteLength(payloadType)} ${payloadType} ${payload.length} `),
    payload,
  ])
}
