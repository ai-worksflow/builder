'use client'

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react'
import {
  defaultLocale,
  localeLabels,
  localeStorageKey,
  locales,
  normalizeLocale,
  type Locale,
} from './config'
import { formatMessage, messages, type MessageKey, type MessageValues } from './messages'

type I18nContextValue = {
  locale: Locale
  locales: readonly Locale[]
  localeLabels: Record<Locale, string>
  setLocale: (locale: Locale) => void
  t: (key: MessageKey, values?: MessageValues) => string
  formatDate: (value: Date | string | number, options?: Intl.DateTimeFormatOptions) => string
  formatNumber: (value: number, options?: Intl.NumberFormatOptions) => string
  formatList: (values: string[], options?: Intl.ListFormatOptions) => string
}

const I18nContext = createContext<I18nContextValue | null>(null)

function detectLocale(): Locale {
  if (typeof window === 'undefined') return defaultLocale

  try {
    const stored = normalizeLocale(window.localStorage.getItem(localeStorageKey))
    if (stored) return stored
  } catch {
    // Storage can be unavailable in restricted browser contexts.
  }

  for (const language of window.navigator.languages ?? []) {
    const locale = normalizeLocale(language)
    if (locale) return locale
  }

  return normalizeLocale(window.navigator.language) ?? defaultLocale
}

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(defaultLocale)

  useEffect(() => {
    setLocaleState(detectLocale())
  }, [])

  const setLocale = useCallback((nextLocale: Locale) => {
    setLocaleState(nextLocale)
  }, [])

  useEffect(() => {
    document.documentElement.lang = locale
    try {
      window.localStorage.setItem(localeStorageKey, locale)
    } catch {
      // Persisting the locale is optional; rendering should keep working.
    }
  }, [locale])

  const t = useCallback(
    (key: MessageKey, values?: MessageValues) => {
      const message = messages[locale][key] ?? messages[defaultLocale][key] ?? key
      return formatMessage(message, values)
    },
    [locale],
  )

  const formatDate = useCallback(
    (value: Date | string | number, options?: Intl.DateTimeFormatOptions) => {
      const date = value instanceof Date ? value : new Date(value)
      if (Number.isNaN(date.getTime())) return String(value)
      return new Intl.DateTimeFormat(locale, options).format(date)
    },
    [locale],
  )

  const formatNumber = useCallback(
    (value: number, options?: Intl.NumberFormatOptions) =>
      new Intl.NumberFormat(locale, options).format(value),
    [locale],
  )

  const formatList = useCallback(
    (values: string[], options?: Intl.ListFormatOptions) =>
      new Intl.ListFormat(locale, options).format(values),
    [locale],
  )

  useEffect(() => {
    document.title = t('meta.title')
    const description = document.querySelector<HTMLMetaElement>('meta[name="description"]')
    if (description) description.content = t('meta.description')
  }, [t])

  const value = useMemo<I18nContextValue>(
    () => ({
      locale,
      locales,
      localeLabels,
      setLocale,
      t,
      formatDate,
      formatNumber,
      formatList,
    }),
    [formatDate, formatList, formatNumber, locale, setLocale, t],
  )

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

export function useI18n() {
  const context = useContext(I18nContext)
  if (!context) {
    throw new Error('useI18n must be used within I18nProvider')
  }
  return context
}
