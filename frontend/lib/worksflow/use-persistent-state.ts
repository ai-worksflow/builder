'use client'

import { useCallback, useEffect, useRef, useState } from 'react'
import type { Dispatch, SetStateAction } from 'react'
import {
  loadPersistentValue,
  parsePersistentValue,
  removePersistentValue,
  resolvePersistenceStorage,
  savePersistentValue,
} from './persistence'
import type {
  PersistenceError,
  PersistenceGuard,
  PersistenceMigration,
  PersistenceRemoveResult,
  PersistenceWriteResult,
  StorageLike,
} from './persistence'

export type PersistenceHydrationStatus = 'hydrating' | 'hydrated' | 'error' | 'unavailable'

export interface UsePersistentStateOptions<T> {
  key: string
  version: number
  initialValue: T | (() => T)
  validate: PersistenceGuard<T>
  migrate?: PersistenceMigration<T>
  storage?: StorageLike | null
  debounceMs?: number
  syncTabs?: boolean
  onError?: (error: PersistenceError) => void
}

export interface PersistentStateController<T> {
  value: T
  setValue: Dispatch<SetStateAction<T>>
  hydrationStatus: PersistenceHydrationStatus
  isHydrated: boolean
  error?: PersistenceError
  lastSavedAt?: number
  isSaving: boolean
  externalConflict?: PersistenceExternalConflict<T>
  resolveExternalConflict: (strategy: 'keep-local' | 'use-external') => void
  flush: () => PersistenceWriteResult<T>
  remove: () => PersistenceRemoveResult
}

export interface PersistenceExternalConflict<T> {
  value: T
  savedAt?: number
  deleted: boolean
}

interface LiveConfiguration<T> {
  validate: PersistenceGuard<T>
  migrate?: PersistenceMigration<T>
  storage?: StorageLike | null
  onError?: (error: PersistenceError) => void
}

function initialStateValue<T>(initialValue: T | (() => T)) {
  return typeof initialValue === 'function' ? (initialValue as () => T)() : initialValue
}

function normalizedDelay(delay: number | undefined) {
  return typeof delay === 'number' && Number.isFinite(delay) ? Math.max(0, delay) : 400
}

export function usePersistentState<T>(
  options: UsePersistentStateOptions<T>,
): PersistentStateController<T> {
  const {
    key,
    version,
    initialValue,
    validate,
    migrate,
    storage,
    debounceMs,
    syncTabs = true,
    onError,
  } = options

  const initialValueRef = useRef<{ value: T } | null>(null)
  if (initialValueRef.current === null) {
    initialValueRef.current = { value: initialStateValue(initialValue) }
  }

  const [value, setInternalValue] = useState<T>(initialValueRef.current.value)
  const [hydrationStatus, setHydrationStatus] =
    useState<PersistenceHydrationStatus>('hydrating')
  const [error, setError] = useState<PersistenceError>()
  const [lastSavedAt, setLastSavedAt] = useState<number>()
  const [isSaving, setIsSaving] = useState(false)
  const [externalConflict, setExternalConflict] =
    useState<PersistenceExternalConflict<T> | undefined>(undefined)

  const valueRef = useRef(value)
  const hydratedRef = useRef(false)
  const dirtyRef = useRef(false)
  const changedBeforeHydrationRef = useRef(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const resolvedStorageRef = useRef<StorageLike | undefined>(undefined)
  const externalConflictRef = useRef<PersistenceExternalConflict<T> | undefined>(undefined)
  const configurationRef = useRef<LiveConfiguration<T>>({
    validate,
    migrate,
    storage,
    onError,
  })

  valueRef.current = value
  configurationRef.current = { validate, migrate, storage, onError }

  const clearPendingSave = useCallback(() => {
    if (timerRef.current === null) return
    clearTimeout(timerRef.current)
    timerRef.current = null
  }, [])

  const reportError = useCallback((nextError: PersistenceError) => {
    setError(nextError)
    try {
      configurationRef.current.onError?.(nextError)
    } catch {
      // Persistence failures must stay recoverable even if an observer throws.
    }
  }, [])

  const persistCurrentValue = useCallback(
    (updateStatus = true) => {
      clearPendingSave()
      if (updateStatus) setIsSaving(true)
      const currentConfiguration = configurationRef.current
      const result = savePersistentValue(key, valueRef.current, {
        version,
        validate: currentConfiguration.validate,
        storage: currentConfiguration.storage,
      })

      if (result.ok) {
        dirtyRef.current = false
        externalConflictRef.current = undefined
        setExternalConflict(undefined)
        if (updateStatus) {
          setError(undefined)
          setHydrationStatus('hydrated')
          setLastSavedAt(result.envelope?.savedAt)
        }
      } else if (updateStatus && result.error) {
        reportError(result.error)
      }
      if (updateStatus) setIsSaving(false)

      return result
    },
    [clearPendingSave, key, reportError, version],
  )

  const setValue = useCallback<Dispatch<SetStateAction<T>>>((nextValue) => {
    const resolvedValue =
      typeof nextValue === 'function'
        ? (nextValue as (currentValue: T) => T)(valueRef.current)
        : nextValue

    valueRef.current = resolvedValue
    dirtyRef.current = true
    setIsSaving(true)
    if (!hydratedRef.current) changedBeforeHydrationRef.current = true
    setInternalValue(resolvedValue)
  }, [])

  useEffect(() => {
    let active = true
    clearPendingSave()
    hydratedRef.current = false
    changedBeforeHydrationRef.current = false
    dirtyRef.current = false
    setHydrationStatus('hydrating')
    setError(undefined)
    setIsSaving(false)
    externalConflictRef.current = undefined
    setExternalConflict(undefined)

    const currentConfiguration = configurationRef.current
    resolvedStorageRef.current = resolvePersistenceStorage(currentConfiguration.storage)
    const result = loadPersistentValue(key, {
      version,
      fallback: initialValueRef.current!.value,
      validate: currentConfiguration.validate,
      migrate: currentConfiguration.migrate,
      storage: currentConfiguration.storage,
    })

    if (!active) return

    const keepLocallyChangedValue = changedBeforeHydrationRef.current
    if (!keepLocallyChangedValue) {
      valueRef.current = result.value
      setInternalValue(result.value)
      dirtyRef.current = false
    }

    hydratedRef.current = true

    if (!result.ok) {
      setHydrationStatus(result.status === 'unavailable' ? 'unavailable' : 'error')
      if (result.error) reportError(result.error)
      return () => {
        active = false
      }
    }

    setHydrationStatus('hydrated')
    setLastSavedAt(result.savedAt)

    if (result.status === 'migrated' && !keepLocallyChangedValue) {
      const migrationWrite = savePersistentValue(key, result.value, {
        version,
        validate: currentConfiguration.validate,
        storage: currentConfiguration.storage,
      })

      if (migrationWrite.ok) {
        setLastSavedAt(migrationWrite.envelope?.savedAt)
      } else if (migrationWrite.error) {
        setHydrationStatus('error')
        reportError(migrationWrite.error)
      }
    }

    return () => {
      active = false
    }
  }, [clearPendingSave, key, reportError, storage, version])

  useEffect(() => {
    if (!hydratedRef.current || hydrationStatus === 'hydrating') return
    if (
      hydrationStatus === 'unavailable' ||
      externalConflictRef.current ||
      !dirtyRef.current
    ) return

    clearPendingSave()
    timerRef.current = setTimeout(() => {
      timerRef.current = null
      persistCurrentValue()
    }, normalizedDelay(debounceMs))

    return clearPendingSave
  }, [clearPendingSave, debounceMs, hydrationStatus, persistCurrentValue, value])

  useEffect(() => {
    if (typeof window === 'undefined') return

    const flushBeforeUnload = () => {
      if (!hydratedRef.current || !dirtyRef.current) return
      persistCurrentValue(false)
    }

    window.addEventListener('beforeunload', flushBeforeUnload)
    return () => window.removeEventListener('beforeunload', flushBeforeUnload)
  }, [persistCurrentValue])

  useEffect(() => {
    if (!syncTabs || typeof window === 'undefined') return

    const synchronizeFromStorage = (event: StorageEvent) => {
      if (event.key !== key && event.key !== null) return

      const activeStorage = resolvedStorageRef.current
      if (event.storageArea && activeStorage && event.storageArea !== activeStorage) return

      if (event.newValue === null) {
        const resetValue = initialValueRef.current!.value
        if (dirtyRef.current) {
          clearPendingSave()
          const conflict = { value: resetValue, deleted: true }
          externalConflictRef.current = conflict
          setExternalConflict(conflict)
          setHydrationStatus('error')
          setIsSaving(false)
          reportError({
            code: 'external-conflict',
            message:
              'Another tab removed this saved project while local changes are pending. Choose which version to keep.',
          })
          return
        }
        clearPendingSave()
        valueRef.current = resetValue
        dirtyRef.current = false
        hydratedRef.current = true
        setInternalValue(resetValue)
        setError(undefined)
        setHydrationStatus('hydrated')
        setIsSaving(false)
        return
      }

      const currentConfiguration = configurationRef.current
      const result = parsePersistentValue(event.newValue, {
        version,
        fallback: valueRef.current,
        validate: currentConfiguration.validate,
        migrate: currentConfiguration.migrate,
      })

      if (!result.ok) {
        setHydrationStatus('error')
        setIsSaving(false)
        if (result.error) reportError(result.error)
        return
      }

      if (dirtyRef.current) {
        clearPendingSave()
        const conflict = {
          value: result.value,
          savedAt: result.savedAt,
          deleted: false,
        }
        externalConflictRef.current = conflict
        setExternalConflict(conflict)
        setHydrationStatus('error')
        setIsSaving(false)
        reportError({
          code: 'external-conflict',
          message:
            'Another tab saved a different project version while local changes are pending. Choose which version to keep.',
        })
        return
      }

      clearPendingSave()
      valueRef.current = result.value
      dirtyRef.current = false
      setIsSaving(false)
      hydratedRef.current = true
      setInternalValue(result.value)
      setError(undefined)
      setHydrationStatus('hydrated')
      setLastSavedAt(result.savedAt)

      if (result.status === 'migrated') {
        const migrationWrite = savePersistentValue(key, result.value, {
          version,
          validate: currentConfiguration.validate,
          storage: currentConfiguration.storage,
        })

        if (migrationWrite.ok) {
          setLastSavedAt(migrationWrite.envelope?.savedAt)
        } else if (migrationWrite.error) {
          setHydrationStatus('error')
          reportError(migrationWrite.error)
        }
      }
    }

    window.addEventListener('storage', synchronizeFromStorage)
    return () => window.removeEventListener('storage', synchronizeFromStorage)
  }, [clearPendingSave, key, reportError, storage, syncTabs, version])

  useEffect(() => clearPendingSave, [clearPendingSave])

  const remove = useCallback(() => {
    clearPendingSave()
    const result = removePersistentValue(key, configurationRef.current.storage)

    if (result.ok) {
      const resetValue = initialValueRef.current!.value
      valueRef.current = resetValue
      dirtyRef.current = false
      setIsSaving(false)
      externalConflictRef.current = undefined
      setExternalConflict(undefined)
      setInternalValue(resetValue)
      setError(undefined)
      setHydrationStatus('hydrated')
    } else if (result.error) {
      reportError(result.error)
    }

    return result
  }, [clearPendingSave, key, reportError])

  const resolveExternalConflict = useCallback(
    (strategy: 'keep-local' | 'use-external') => {
      const conflict = externalConflictRef.current
      if (!conflict) return
      clearPendingSave()
      externalConflictRef.current = undefined
      setExternalConflict(undefined)
      setError(undefined)
      setHydrationStatus('hydrated')

      if (strategy === 'use-external') {
        valueRef.current = conflict.value
        dirtyRef.current = false
        setInternalValue(conflict.value)
        setLastSavedAt(conflict.savedAt)
        setIsSaving(false)
        return
      }

      dirtyRef.current = true
      persistCurrentValue()
    },
    [clearPendingSave, persistCurrentValue],
  )

  return {
    value,
    setValue,
    hydrationStatus,
    isHydrated: hydrationStatus !== 'hydrating',
    error,
    lastSavedAt,
    isSaving,
    externalConflict,
    resolveExternalConflict,
    flush: persistCurrentValue,
    remove,
  }
}
