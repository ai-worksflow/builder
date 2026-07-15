'use client'

import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import {
  AlertTriangle,
  CheckCircle2,
  CircleAlert,
  Loader2,
  Paperclip,
  Play,
  ShieldCheck,
} from 'lucide-react'

export function QualityPanel() {
  const { locale, t } = useI18n()
  const {
    qualityRun,
    qualityRunning,
    qualityError,
    runWorkspaceQuality,
    attachQualityDiagnostics,
    setSelectedWorkspaceFile,
  } = useWorksflow()

  return (
    <div className="space-y-3 p-2 text-[11px]">
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-3">
        <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary-bright">
          <ShieldCheck className="h-4 w-4" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="font-semibold text-foreground">{t('quality.title')}</div>
          <div className="text-faint-foreground">
            {qualityRun
              ? t('quality.summary', {
                  score: qualityRun.score.percentage.toLocaleString(locale),
                  count: qualityRun.diagnostics.length.toLocaleString(locale),
                })
              : t('quality.description')}
          </div>
        </div>
        {qualityRun && qualityRun.diagnostics.length > 0 && (
          <button
            type="button"
            onClick={attachQualityDiagnostics}
            className="inline-flex items-center gap-1.5 rounded-md border border-primary/30 bg-primary/10 px-2.5 py-1.5 font-medium text-primary-bright hover:bg-primary/15"
          >
            <Paperclip className="h-3.5 w-3.5" />
            {t('quality.attachRepair')}
          </button>
        )}
        <button
          type="button"
          onClick={() => void runWorkspaceQuality()}
          disabled={qualityRunning}
          className="inline-flex items-center gap-1.5 rounded-md bg-primary px-2.5 py-1.5 font-semibold text-primary-foreground hover:bg-primary-bright disabled:opacity-60"
        >
          {qualityRunning ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Play className="h-3.5 w-3.5" />}
          {qualityRunning ? t('quality.running') : t('quality.run')}
        </button>
      </div>

      {qualityError && (
        <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-destructive">
          {qualityError}
        </div>
      )}

      {qualityRun && (
        <>
          <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
            {qualityRun.checks.map((check) => (
              <div key={check.id} className="rounded-md border border-border bg-card p-2.5">
                <div className="flex items-center gap-1.5">
                  {check.status === 'passed' ? (
                    <CheckCircle2 className="h-3.5 w-3.5 text-success" />
                  ) : check.status === 'failed' ? (
                    <CircleAlert className="h-3.5 w-3.5 text-destructive" />
                  ) : (
                    <AlertTriangle className="h-3.5 w-3.5 text-warning" />
                  )}
                  <span className="truncate font-medium text-foreground">
                    {qualityCheckTitle(check.id, t)}
                  </span>
                </div>
                <div className="mt-1 flex items-center justify-between text-[10px] text-faint-foreground">
                  <span>{qualityCheckStatus(check.status, t)}</span>
                  <span>{new Intl.NumberFormat(locale, { style: 'percent' }).format(check.score.percentage / 100)}</span>
                </div>
              </div>
            ))}
          </div>

          <div className="space-y-1.5">
            {qualityRun.diagnostics.length === 0 ? (
              <div className="rounded-md border border-success/25 bg-success/10 px-3 py-3 text-success">
                {t('quality.clean')}
              </div>
            ) : (
              qualityRun.diagnostics.map((diagnostic, index) => (
                <button
                  key={`${diagnostic.code}-${diagnostic.path ?? 'workspace'}-${index}`}
                  type="button"
                  onClick={() => diagnostic.path && setSelectedWorkspaceFile(diagnostic.path)}
                  className="flex w-full items-start gap-2 rounded-md border border-border bg-card px-3 py-2 text-left hover:border-border-strong"
                >
                  <span
                    className={cn(
                      'mt-0.5 rounded px-1.5 py-0.5 text-[9px] font-semibold uppercase',
                      diagnostic.severity === 'error'
                        ? 'bg-destructive/10 text-destructive'
                        : diagnostic.severity === 'warning'
                          ? 'bg-warning/10 text-warning'
                          : 'bg-primary/10 text-primary-bright',
                    )}
                  >
                    {qualitySeverity(diagnostic.severity, t)}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block text-foreground">{diagnostic.message}</span>
                    <span className="mt-0.5 block truncate font-mono text-[10px] text-faint-foreground">
                      {diagnostic.path ?? t('quality.workspace')}
                      {diagnostic.line ? `:${diagnostic.line}${diagnostic.column ? `:${diagnostic.column}` : ''}` : ''}
                      {' · '}{diagnostic.checkId}/{diagnostic.code}
                    </span>
                    {diagnostic.suggestion && (
                      <span className="mt-1 block text-[10px] text-muted-foreground">{diagnostic.suggestion}</span>
                    )}
                  </span>
                </button>
              ))
            )}
          </div>
        </>
      )}
    </div>
  )
}

type Translate = ReturnType<typeof useI18n>['t']

function qualityCheckTitle(checkId: string, t: Translate) {
  switch (checkId) {
    case 'build': return t('quality.check.build')
    case 'type': return t('quality.check.type')
    case 'lint': return t('quality.check.lint')
    case 'test': return t('quality.check.test')
    case 'accessibility': return t('quality.check.accessibility')
    case 'dependency': return t('quality.check.dependency')
    case 'secret': return t('quality.check.secret')
    default: return checkId
  }
}

function qualityCheckStatus(status: string, t: Translate) {
  switch (status) {
    case 'passed': return t('quality.status.passed')
    case 'warning': return t('quality.status.warning')
    case 'failed': return t('quality.status.failed')
    case 'skipped': return t('quality.status.skipped')
    default: return status
  }
}

function qualitySeverity(severity: string, t: Translate) {
  switch (severity) {
    case 'error': return t('quality.severity.error')
    case 'warning': return t('quality.severity.warning')
    case 'info': return t('quality.severity.info')
    default: return severity
  }
}
