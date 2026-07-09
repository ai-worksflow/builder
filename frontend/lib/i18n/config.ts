export const locales = ['zh-CN', 'en-US'] as const

export type Locale = (typeof locales)[number]

export const defaultLocale: Locale = 'zh-CN'

export const localeStorageKey = 'worksflow.locale'

export const localeLabels: Record<Locale, string> = {
  'zh-CN': '中文',
  'en-US': 'English',
}

export function isLocale(value: unknown): value is Locale {
  return typeof value === 'string' && locales.includes(value as Locale)
}

export function normalizeLocale(value: string | null | undefined): Locale | null {
  if (!value) return null
  if (isLocale(value)) return value

  const normalized = value.toLowerCase()
  if (normalized.startsWith('zh')) return 'zh-CN'
  if (normalized.startsWith('en')) return 'en-US'

  return null
}
