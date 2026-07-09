'use client'

import { useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { DATABASE_CAPABILITIES } from '@/lib/worksflow/mock-data'
import { useWorksflow } from '@/lib/worksflow/store'
import {
  Database,
  FunctionSquare,
  HardDrive,
  KeyRound,
  Lock,
  Table2,
  Users,
  type LucideIcon,
} from 'lucide-react'

const ICONS: Record<string, LucideIcon> = {
  Table2,
  KeyRound,
  FunctionSquare,
  Lock,
  Users,
  HardDrive,
}

export function DatabasePanel() {
  const { t } = useI18n()
  const { requestDatabaseSetup } = useWorksflow()
  const [dialog, setDialog] = useState<'supabase' | 'learn' | null>(null)
  const [selectedCapability, setSelectedCapability] = useState('Tables')

  return (
    <div className="h-full overflow-y-auto scrollbar-thin bg-background">
      <div className="mx-auto max-w-3xl px-6 py-12">
        <div className="mb-8 text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-lg bg-primary/15">
            <Database className="h-6 w-6 text-primary-bright" />
          </div>
          <h2 className="text-xl font-semibold text-foreground text-balance">
            {t('database.title')}
          </h2>
          <p className="mt-2 text-[13px] text-muted-foreground">
            {t('database.subtitle')}
          </p>
        </div>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {DATABASE_CAPABILITIES.map((cap) => {
            const Icon = ICONS[cap.icon] ?? Table2
            return (
              <div
                key={cap.title}
                className="flex flex-col rounded-lg border border-border bg-card p-4"
              >
                <span className="mb-3 flex h-9 w-9 items-center justify-center rounded-lg bg-white/5">
                  <Icon className="h-[18px] w-[18px] text-primary-bright" />
                </span>
                <h3 className="text-[14px] font-medium text-foreground">{cap.title}</h3>
                <p className="mt-1 flex-1 text-[12px] leading-relaxed text-muted-foreground">
                  {cap.description}
                </p>
                <button
                  type="button"
                  onClick={() => {
                    setSelectedCapability(cap.title)
                    setDialog('learn')
                  }}
                  className="mt-3 text-[12px] font-medium text-primary-bright hover:underline"
                >
                  {t('database.learnMore')}
                </button>
              </div>
            )
          })}
        </div>

        <div className="mt-8 flex flex-col items-center gap-3">
          <button
            type="button"
            onClick={requestDatabaseSetup}
            className="rounded-lg bg-primary px-4 py-2.5 text-[13px] font-semibold text-primary-foreground hover:bg-primary-bright"
          >
            {t('database.create')}
          </button>
          <button
            type="button"
            onClick={() => setDialog('supabase')}
            className="text-[12px] font-medium text-muted-foreground hover:text-foreground"
          >
            {t('database.connectSupabase')}
          </button>
        </div>
      </div>
      {dialog && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
          <div className="w-full max-w-md rounded-lg border border-border bg-popover p-4 shadow-2xl">
            <div className="flex items-center gap-2">
              <Database className="h-4 w-4 text-primary-bright" />
              <h3 className="text-sm font-semibold text-foreground">
                {dialog === 'supabase' ? t('database.modal.connectSupabase') : selectedCapability}
              </h3>
            </div>
            <p className="mt-2 text-[12px] leading-relaxed text-muted-foreground">
              {dialog === 'supabase'
                ? t('database.modal.supabaseCopy')
                : t('database.modal.learnCopy', { capability: selectedCapability })}
            </p>
            {dialog === 'supabase' && (
              <input
                placeholder={t('database.modal.supabasePlaceholder')}
                className="mt-3 w-full rounded-md border border-border bg-background px-3 py-2 text-[13px] text-foreground outline-none placeholder:text-faint-foreground focus:border-primary/60 focus:ring-1 focus:ring-primary/40"
              />
            )}
            <div className="mt-4 flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setDialog(null)}
                className="rounded-md border border-border px-3 py-1.5 text-[12px] font-medium text-muted-foreground hover:bg-white/5"
              >
                {t('common.cancel')}
              </button>
              <button
                type="button"
                onClick={() => {
                  if (dialog === 'learn') requestDatabaseSetup()
                  setDialog(null)
                }}
                className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright"
              >
                {dialog === 'supabase' ? t('common.connect') : t('database.modal.addToPrompt')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
