'use client'

import { Languages } from 'lucide-react'
import { cn } from '@/lib/utils'
import { localeLabels, useI18n, type Locale } from '@/lib/i18n'

const nextLocale: Record<Locale, Locale> = {
  'zh-CN': 'en-US',
  'en-US': 'zh-CN',
}

export function LanguageToggle({ className }: { className?: string }) {
  const { locale, setLocale, t } = useI18n()
  const next = nextLocale[locale]

  return (
    <button
      type="button"
      onClick={() => setLocale(next)}
      className={cn(
        'inline-flex h-8 shrink-0 items-center justify-center gap-1.5 rounded-md border border-border px-2 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground',
        className,
      )}
      aria-label={t('i18n.switchTo', { locale: localeLabels[next] })}
      title={t('i18n.switchTo', { locale: localeLabels[next] })}
    >
      <Languages className="h-3.5 w-3.5" />
      <span>{locale === 'zh-CN' ? '中' : 'EN'}</span>
    </button>
  )
}
