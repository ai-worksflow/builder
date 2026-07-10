export const PERSISTENCE_ENVELOPE_SCHEMA = 'worksflow.persistence'

export interface StorageLike {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export interface PersistenceEnvelope<T> {
  schema: typeof PERSISTENCE_ENVELOPE_SCHEMA
  version: number
  savedAt: number
  data: T
}

export type PersistenceGuard<T> = (value: unknown) => value is T

export interface PersistenceMigrationContext {
  fromVersion: number
  toVersion: number
}

export type PersistenceMigration<T> = (
  value: unknown,
  context: PersistenceMigrationContext,
) => T | unknown

export type PersistenceErrorCode =
  | 'storage-unavailable'
  | 'invalid-version'
  | 'read-failed'
  | 'corrupt-data'
  | 'version-mismatch'
  | 'migration-failed'
  | 'invalid-data'
  | 'serialization-failed'
  | 'quota-exceeded'
  | 'write-failed'
  | 'external-conflict'
  | 'remove-failed'

export interface PersistenceError {
  code: PersistenceErrorCode
  message: string
  cause?: unknown
}

export type PersistenceLoadStatus =
  | 'loaded'
  | 'migrated'
  | 'missing'
  | 'unavailable'
  | 'read-error'
  | 'corrupt'
  | 'version-mismatch'
  | 'migration-error'
  | 'invalid'

export interface PersistenceLoadResult<T> {
  ok: boolean
  status: PersistenceLoadStatus
  value: T
  sourceVersion?: number
  savedAt?: number
  error?: PersistenceError
}

export interface PersistenceWriteResult<T> {
  ok: boolean
  envelope?: PersistenceEnvelope<T>
  error?: PersistenceError
}

export interface PersistenceRemoveResult {
  ok: boolean
  error?: PersistenceError
}

interface PersistenceReadOptions<T> {
  version: number
  fallback: T
  validate: PersistenceGuard<T>
  migrate?: PersistenceMigration<T>
}

export interface LoadPersistentValueOptions<T> extends PersistenceReadOptions<T> {
  storage?: StorageLike | null
}

export interface SavePersistentValueOptions<T> {
  version: number
  validate: PersistenceGuard<T>
  storage?: StorageLike | null
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function hasOwn(object: object, property: PropertyKey) {
  return Object.prototype.hasOwnProperty.call(object, property)
}

function isValidVersion(version: number) {
  return Number.isSafeInteger(version) && version > 0
}

function messageFromCause(cause: unknown, fallback: string) {
  return cause instanceof Error && cause.message ? `${fallback}: ${cause.message}` : fallback
}

function issue(code: PersistenceErrorCode, message: string, cause?: unknown): PersistenceError {
  return cause === undefined ? { code, message } : { code, message, cause }
}

function fallbackResult<T>(
  status: Exclude<PersistenceLoadStatus, 'loaded' | 'migrated' | 'missing'>,
  fallback: T,
  error: PersistenceError,
  sourceVersion?: number,
): PersistenceLoadResult<T> {
  return {
    ok: false,
    status,
    value: fallback,
    sourceVersion,
    error,
  }
}

function safelyValidate<T>(validate: PersistenceGuard<T>, value: unknown): value is T {
  try {
    return validate(value)
  } catch {
    return false
  }
}

export function isPersistenceEnvelope(value: unknown): value is PersistenceEnvelope<unknown> {
  return (
    isRecord(value) &&
    value.schema === PERSISTENCE_ENVELOPE_SCHEMA &&
    typeof value.version === 'number' &&
    isValidVersion(value.version) &&
    typeof value.savedAt === 'number' &&
    Number.isFinite(value.savedAt) &&
    value.savedAt >= 0 &&
    hasOwn(value, 'data')
  )
}

export function getBrowserStorage(): StorageLike | undefined {
  if (typeof window === 'undefined') return undefined

  try {
    return window.localStorage
  } catch {
    return undefined
  }
}

export function resolvePersistenceStorage(storage?: StorageLike | null) {
  if (storage === null) return undefined
  return storage ?? getBrowserStorage()
}

export function isQuotaExceededError(error: unknown) {
  if (!isRecord(error)) return false

  const name = typeof error.name === 'string' ? error.name : ''
  const code = typeof error.code === 'number' ? error.code : undefined

  return (
    name === 'QuotaExceededError' ||
    name === 'NS_ERROR_DOM_QUOTA_REACHED' ||
    code === 22 ||
    code === 1014
  )
}

export function parsePersistentValue<T>(
  rawValue: string,
  options: PersistenceReadOptions<T>,
): PersistenceLoadResult<T> {
  if (!isValidVersion(options.version)) {
    return fallbackResult(
      'invalid',
      options.fallback,
      issue('invalid-version', 'Persistence versions must be positive safe integers'),
    )
  }

  let parsed: unknown
  try {
    parsed = JSON.parse(rawValue)
  } catch (error) {
    return fallbackResult(
      'corrupt',
      options.fallback,
      issue('corrupt-data', messageFromCause(error, 'Stored data is not valid JSON'), error),
    )
  }

  if (!isPersistenceEnvelope(parsed)) {
    return fallbackResult(
      'corrupt',
      options.fallback,
      issue('corrupt-data', 'Stored data does not use a valid persistence envelope'),
    )
  }

  if (parsed.version === options.version) {
    if (!safelyValidate(options.validate, parsed.data)) {
      return fallbackResult(
        'invalid',
        options.fallback,
        issue('invalid-data', 'Stored data does not match the expected schema'),
        parsed.version,
      )
    }

    return {
      ok: true,
      status: 'loaded',
      value: parsed.data,
      sourceVersion: parsed.version,
      savedAt: parsed.savedAt,
    }
  }

  if (!options.migrate) {
    return fallbackResult(
      'version-mismatch',
      options.fallback,
      issue(
        'version-mismatch',
        `Stored version ${parsed.version} cannot be read as version ${options.version}`,
      ),
      parsed.version,
    )
  }

  let migrated: unknown
  try {
    migrated = options.migrate(parsed.data, {
      fromVersion: parsed.version,
      toVersion: options.version,
    })
  } catch (error) {
    return fallbackResult(
      'migration-error',
      options.fallback,
      issue('migration-failed', messageFromCause(error, 'Stored data migration failed'), error),
      parsed.version,
    )
  }

  if (!safelyValidate(options.validate, migrated)) {
    return fallbackResult(
      'invalid',
      options.fallback,
      issue('invalid-data', 'Migrated data does not match the expected schema'),
      parsed.version,
    )
  }

  return {
    ok: true,
    status: 'migrated',
    value: migrated,
    sourceVersion: parsed.version,
    savedAt: parsed.savedAt,
  }
}

export function loadPersistentValue<T>(
  key: string,
  options: LoadPersistentValueOptions<T>,
): PersistenceLoadResult<T> {
  const storage = resolvePersistenceStorage(options.storage)
  if (!storage) {
    return fallbackResult(
      'unavailable',
      options.fallback,
      issue('storage-unavailable', 'Persistent storage is unavailable in this environment'),
    )
  }

  let rawValue: string | null
  try {
    rawValue = storage.getItem(key)
  } catch (error) {
    return fallbackResult(
      'read-error',
      options.fallback,
      issue('read-failed', messageFromCause(error, `Unable to read storage key "${key}"`), error),
    )
  }

  if (rawValue === null) {
    return {
      ok: true,
      status: 'missing',
      value: options.fallback,
    }
  }

  return parsePersistentValue(rawValue, options)
}

export function savePersistentValue<T>(
  key: string,
  value: T,
  options: SavePersistentValueOptions<T>,
): PersistenceWriteResult<T> {
  if (!isValidVersion(options.version)) {
    return {
      ok: false,
      error: issue('invalid-version', 'Persistence versions must be positive safe integers'),
    }
  }

  if (!safelyValidate(options.validate, value)) {
    return {
      ok: false,
      error: issue('invalid-data', 'The value to persist does not match the expected schema'),
    }
  }

  const storage = resolvePersistenceStorage(options.storage)
  if (!storage) {
    return {
      ok: false,
      error: issue('storage-unavailable', 'Persistent storage is unavailable in this environment'),
    }
  }

  const envelope: PersistenceEnvelope<T> = {
    schema: PERSISTENCE_ENVELOPE_SCHEMA,
    version: options.version,
    savedAt: Date.now(),
    data: value,
  }

  let serialized: string
  try {
    serialized = JSON.stringify(envelope)
  } catch (error) {
    return {
      ok: false,
      error: issue(
        'serialization-failed',
        messageFromCause(error, 'Unable to serialize the value for persistence'),
        error,
      ),
    }
  }

  try {
    storage.setItem(key, serialized)
  } catch (error) {
    const quotaExceeded = isQuotaExceededError(error)
    return {
      ok: false,
      error: issue(
        quotaExceeded ? 'quota-exceeded' : 'write-failed',
        messageFromCause(
          error,
          quotaExceeded
            ? 'Persistent storage quota was exceeded'
            : `Unable to write storage key "${key}"`,
        ),
        error,
      ),
    }
  }

  return { ok: true, envelope }
}

export function removePersistentValue(
  key: string,
  storage?: StorageLike | null,
): PersistenceRemoveResult {
  const resolvedStorage = resolvePersistenceStorage(storage)
  if (!resolvedStorage) {
    return {
      ok: false,
      error: issue('storage-unavailable', 'Persistent storage is unavailable in this environment'),
    }
  }

  try {
    resolvedStorage.removeItem(key)
    return { ok: true }
  } catch (error) {
    return {
      ok: false,
      error: issue(
        'remove-failed',
        messageFromCause(error, `Unable to remove storage key "${key}"`),
        error,
      ),
    }
  }
}
