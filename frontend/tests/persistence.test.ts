import assert from 'node:assert/strict'
import {
  PERSISTENCE_ENVELOPE_SCHEMA,
  isPersistenceEnvelope,
  loadPersistentValue,
  removePersistentValue,
  savePersistentValue,
} from '../lib/worksflow/persistence'
import type { StorageLike } from '../lib/worksflow/persistence'

type TestCase = {
  name: string
  run: () => void
}

type Preferences = {
  theme: 'light' | 'dark'
  density: number
}

class MemoryStorage implements StorageLike {
  private readonly values = new Map<string, string>()

  getItem(key: string) {
    return this.values.get(key) ?? null
  }

  setItem(key: string, value: string) {
    this.values.set(key, value)
  }

  removeItem(key: string) {
    this.values.delete(key)
  }

  raw(key: string) {
    return this.values.get(key)
  }
}

const tests: TestCase[] = []

function test(name: string, run: () => void) {
  tests.push({ name, run })
}

function isPreferences(value: unknown): value is Preferences {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Partial<Preferences>
  return (
    (candidate.theme === 'light' || candidate.theme === 'dark') &&
    typeof candidate.density === 'number' &&
    Number.isFinite(candidate.density)
  )
}

test('roundtrips a guarded value in a versioned envelope', () => {
  const storage = new MemoryStorage()
  const preferences: Preferences = { theme: 'dark', density: 2 }
  const saved = savePersistentValue('preferences', preferences, {
    version: 3,
    validate: isPreferences,
    storage,
  })

  assert.equal(saved.ok, true)
  assert.equal(saved.envelope?.schema, PERSISTENCE_ENVELOPE_SCHEMA)
  assert.equal(saved.envelope?.version, 3)
  assert.equal(typeof saved.envelope?.savedAt, 'number')

  const serialized = storage.raw('preferences')
  assert.ok(serialized)
  assert.equal(isPersistenceEnvelope(JSON.parse(serialized)), true)

  const loaded = loadPersistentValue('preferences', {
    version: 3,
    fallback: { theme: 'light', density: 1 },
    validate: isPreferences,
    storage,
  })

  assert.equal(loaded.ok, true)
  assert.equal(loaded.status, 'loaded')
  assert.equal(loaded.sourceVersion, 3)
  assert.deepEqual(loaded.value, preferences)
})

test('reports a version mismatch when no migration hook is available', () => {
  const storage = new MemoryStorage()
  savePersistentValue('preferences', { theme: 'dark', density: 1 }, {
    version: 1,
    validate: isPreferences,
    storage,
  })

  const fallback: Preferences = { theme: 'light', density: 3 }
  const loaded = loadPersistentValue('preferences', {
    version: 2,
    fallback,
    validate: isPreferences,
    storage,
  })

  assert.equal(loaded.ok, false)
  assert.equal(loaded.status, 'version-mismatch')
  assert.equal(loaded.error?.code, 'version-mismatch')
  assert.equal(loaded.sourceVersion, 1)
  assert.deepEqual(loaded.value, fallback)
})

test('migrates old data and validates the migration result', () => {
  const storage = new MemoryStorage()
  const isLegacyTheme = (value: unknown): value is string => typeof value === 'string'
  savePersistentValue('preferences', 'dark', {
    version: 1,
    validate: isLegacyTheme,
    storage,
  })

  const loaded = loadPersistentValue('preferences', {
    version: 2,
    fallback: { theme: 'light', density: 1 },
    validate: isPreferences,
    storage,
    migrate: (value, context) => {
      assert.deepEqual(context, { fromVersion: 1, toVersion: 2 })
      return { theme: value, density: 2 }
    },
  })

  assert.equal(loaded.ok, true)
  assert.equal(loaded.status, 'migrated')
  assert.equal(loaded.sourceVersion, 1)
  assert.deepEqual(loaded.value, { theme: 'dark', density: 2 })
})

test('falls back safely when stored JSON or its schema is corrupt', () => {
  const storage = new MemoryStorage()
  const fallback: Preferences = { theme: 'light', density: 1 }
  storage.setItem('preferences', '{not-json')

  const malformedJson = loadPersistentValue('preferences', {
    version: 1,
    fallback,
    validate: isPreferences,
    storage,
  })

  assert.equal(malformedJson.ok, false)
  assert.equal(malformedJson.status, 'corrupt')
  assert.equal(malformedJson.error?.code, 'corrupt-data')
  assert.deepEqual(malformedJson.value, fallback)

  storage.setItem('preferences', JSON.stringify({ version: 1, data: fallback }))
  const malformedEnvelope = loadPersistentValue('preferences', {
    version: 1,
    fallback,
    validate: isPreferences,
    storage,
  })

  assert.equal(malformedEnvelope.ok, false)
  assert.equal(malformedEnvelope.status, 'corrupt')
  assert.deepEqual(malformedEnvelope.value, fallback)
})

test('returns a quota error instead of throwing on autosave-sized writes', () => {
  const storage: StorageLike = {
    getItem: () => null,
    setItem: () => {
      throw { name: 'QuotaExceededError', code: 22 }
    },
    removeItem: () => undefined,
  }

  const result = savePersistentValue('preferences', { theme: 'dark', density: 2 }, {
    version: 1,
    validate: isPreferences,
    storage,
  })

  assert.equal(result.ok, false)
  assert.equal(result.error?.code, 'quota-exceeded')
})

test('removes stored state and contains removal failures', () => {
  const storage = new MemoryStorage()
  savePersistentValue('preferences', { theme: 'dark', density: 2 }, {
    version: 1,
    validate: isPreferences,
    storage,
  })

  const removed = removePersistentValue('preferences', storage)
  assert.equal(removed.ok, true)
  assert.equal(storage.getItem('preferences'), null)

  const failingStorage: StorageLike = {
    getItem: () => null,
    setItem: () => undefined,
    removeItem: () => {
      throw new Error('blocked')
    },
  }
  const failedRemoval = removePersistentValue('preferences', failingStorage)
  assert.equal(failedRemoval.ok, false)
  assert.equal(failedRemoval.error?.code, 'remove-failed')
})

let failures = 0

tests.forEach(({ name, run }) => {
  try {
    run()
    console.log(`✓ ${name}`)
  } catch (error) {
    failures += 1
    console.error(`✗ ${name}`)
    console.error(error)
  }
})

if (failures > 0) {
  console.error(`${failures} persistence test(s) failed.`)
  process.exit(1)
}

console.log(`${tests.length} persistence test(s) passed.`)
