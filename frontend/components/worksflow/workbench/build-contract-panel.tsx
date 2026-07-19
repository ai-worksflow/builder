'use client'

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import {
  CheckCircle2,
  CircleAlert,
  FileCheck2,
  LoaderCircle,
  RefreshCw,
  ShieldCheck,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n } from '@/lib/i18n'
import {
  evaluateBuildContractReadiness,
  type BuildContractGatePhase,
  type BuildContractGateSnapshot,
} from '@/lib/platform/build-contract-gate'
import { ConstructorClient } from '@/lib/platform/constructor-client'
import type {
  ApplicationBuildContractDto,
  FullStackTemplateRegistrationDto,
} from '@/lib/platform/constructor-contract'
import { PlatformHttpError } from '@/lib/platform/http'
import { cn } from '@/lib/utils'

export interface BuildContractPanelProps {
  readonly bundleId: string
  readonly canCompile: boolean
  readonly onGateChange: (gate: BuildContractGateSnapshot) => void
}

type LoadState = 'loading' | 'ready' | 'error'

function templateKey(template: FullStackTemplateRegistrationDto) {
  return `${template.template.id}\u001f${template.template.contentHash}`
}

function exactTemplate(
  items: readonly FullStackTemplateRegistrationDto[],
  contract: ApplicationBuildContractDto,
) {
  return items.find((item) => (
    item.template.id === contract.contract.fullStackTemplate.id
    && item.template.contentHash === contract.contract.fullStackTemplate.contentHash
  ))
}

export function BuildContractPanel({
  bundleId,
  canCompile,
  onGateChange,
}: BuildContractPanelProps) {
  const { t, formatList, formatNumber } = useI18n()
  const { platformClient } = useCollaboration()
  const client = useMemo(
    () => new ConstructorClient(platformClient.http),
    [platformClient.http],
  )
  const [loadState, setLoadState] = useState<LoadState>('loading')
  const [templates, setTemplates] = useState<readonly FullStackTemplateRegistrationDto[]>([])
  const [selectedKey, setSelectedKey] = useState('')
  const [contract, setContract] = useState<ApplicationBuildContractDto | null>(null)
  const [compiling, setCompiling] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [reloadVersion, setReloadVersion] = useState(0)
  const loadSequence = useRef(0)
  const compileController = useRef<AbortController | null>(null)
  const compilingRef = useRef(false)
  const activeBundle = useRef(bundleId)
  activeBundle.current = bundleId

  useEffect(() => {
    const sequence = ++loadSequence.current
    const controller = new AbortController()
    compileController.current?.abort()
    compileController.current = null
    compilingRef.current = false
    setLoadState('loading')
    setTemplates([])
    setSelectedKey('')
    setContract(null)
    setCompiling(false)
    setError(null)

    const loadContract = async () => {
      try {
        return (await client.getBuildContractForManifest(bundleId, {
          signal: controller.signal,
        })).data
      } catch (cause) {
        if (cause instanceof PlatformHttpError && cause.status === 404) return null
        throw cause
      }
    }

    void Promise.all([
      client.listFullStackTemplates({}, { limit: 100, signal: controller.signal }),
      loadContract(),
    ]).then(async ([registryResult, existingContract]) => {
      let nextTemplates = [...registryResult.data.items]
      if (existingContract && !exactTemplate(nextTemplates, existingContract)) {
        const exact = existingContract.contract.fullStackTemplate
        const exactResult = await client.getFullStackTemplate(
          exact.id,
          { contentHash: exact.contentHash },
          { signal: controller.signal },
        )
        nextTemplates = [...nextTemplates, exactResult.data]
      }
      if (controller.signal.aborted || sequence !== loadSequence.current) return
      setTemplates(nextTemplates)
      setContract(existingContract)
      const selected = existingContract
        ? exactTemplate(nextTemplates, existingContract)
        : nextTemplates[0]
      setSelectedKey(selected ? templateKey(selected) : '')
      setLoadState('ready')
    }).catch((cause: unknown) => {
      if (controller.signal.aborted || sequence !== loadSequence.current) return
      setError(cause)
      setLoadState('error')
    })

    return () => {
      controller.abort()
      compileController.current?.abort()
      compileController.current = null
      compilingRef.current = false
    }
  }, [bundleId, client, reloadVersion])

  const selectedTemplate = templates.find((item) => templateKey(item) === selectedKey)
  const readiness = useMemo(
    () => evaluateBuildContractReadiness(contract, bundleId),
    [bundleId, contract],
  )
  const phase: BuildContractGatePhase = loadState === 'loading'
    ? 'loading'
    : compiling
      ? 'compiling'
      : loadState === 'error'
        ? 'error'
        : readiness.ready
          ? 'ready'
          : contract ? 'blocked' : 'missing'
  const gate = useMemo<BuildContractGateSnapshot>(() => ({
    bundleId,
    phase,
    contract,
    ready: phase === 'ready' && readiness.ready,
    reason: readiness.reason,
  }), [bundleId, contract, phase, readiness.ready, readiness.reason])

  useEffect(() => {
    onGateChange(gate)
  }, [gate, onGateChange])

  const compile = useCallback(async () => {
    if (!selectedTemplate || !canCompile || compilingRef.current) return
    compilingRef.current = true
    const controller = new AbortController()
    compileController.current?.abort()
    compileController.current = controller
    const requestedBundle = bundleId
    onGateChange({
      bundleId: requestedBundle,
      phase: 'compiling',
      contract,
      ready: false,
      reason: readiness.reason,
    })
    setCompiling(true)
    setError(null)
    try {
      const result = await client.createBuildContract(
        requestedBundle,
        {
          fullStackTemplate: {
            id: selectedTemplate.template.id,
            contentHash: selectedTemplate.template.contentHash,
          },
        },
        { signal: controller.signal },
      )
      if (controller.signal.aborted || activeBundle.current !== requestedBundle) return
      setContract(result.data)
      setLoadState('ready')
    } catch (cause) {
      if (controller.signal.aborted || activeBundle.current !== requestedBundle) return
      setError(cause)
      setLoadState('error')
    } finally {
      if (compileController.current === controller) compileController.current = null
      if (activeBundle.current === requestedBundle) {
        compilingRef.current = false
        setCompiling(false)
      }
    }
  }, [bundleId, canCompile, client, contract, onGateChange, readiness.reason, selectedTemplate])

  const blockingGaps = contract?.contract.gaps.filter((gap) => gap.blocking) ?? []
  const blockingConflicts = contract?.contract.conflicts.filter((conflict) => conflict.blocking) ?? []
  const sourceKinds = contract
    ? [...new Set(contract.contract.sourceRevisions.map((source) => source.kind).filter(Boolean))]
    : []
  const statusLabel = phase === 'loading'
    ? t('platform.buildContract.loading')
    : phase === 'compiling'
      ? t('platform.buildContract.compiling')
      : phase === 'ready'
        ? t('platform.buildContract.ready')
        : phase === 'blocked'
          ? t('platform.buildContract.blocked')
          : phase === 'error'
            ? t('platform.buildContract.error')
            : t('platform.buildContract.missing')

  return (
    <section className="mt-3 rounded-md border border-border bg-panel p-2.5" aria-label={t('platform.buildContract.title')}>
      <div className="flex flex-wrap items-center gap-2">
        {phase === 'loading' || phase === 'compiling'
          ? <LoaderCircle className="size-4 animate-spin text-primary-bright" />
          : phase === 'ready'
            ? <CheckCircle2 className="size-4 text-success" />
            : phase === 'error'
              ? <CircleAlert className="size-4 text-destructive" />
              : <ShieldCheck className="size-4 text-warning" />}
        <div className="min-w-44 flex-1">
          <div className="flex items-center gap-2">
            <h3 className="text-[10px] font-semibold text-foreground">{t('platform.buildContract.title')}</h3>
            <span className={cn(
              'rounded px-1.5 py-0.5 text-[7px] font-semibold uppercase',
              phase === 'ready'
                ? 'bg-success/15 text-success'
                : phase === 'error'
                  ? 'bg-destructive/10 text-destructive'
                  : 'bg-warning/10 text-warning',
            )}>{statusLabel}</span>
          </div>
          <p className="mt-0.5 text-[8px] leading-relaxed text-faint-foreground">{t('platform.buildContract.description')}</p>
        </div>
        <select
          value={selectedKey}
          onChange={(event) => setSelectedKey(event.target.value)}
          disabled={loadState !== 'ready' || compiling || templates.length === 0}
          className="h-8 min-w-48 max-w-80 rounded-md border border-border bg-background px-2 text-[9px] text-foreground outline-none disabled:opacity-40"
          aria-label={t('platform.buildContract.template')}
        >
          {templates.length === 0 && <option value="">{t('platform.buildContract.noTemplate')}</option>}
          {templates.map((item) => {
            const roles = item.components.map((component) => component.role).filter(Boolean)
            return (
              <option key={templateKey(item)} value={templateKey(item)}>
                {item.template.templateId} @ {item.template.version}{roles.length > 0 ? ` · ${formatList(roles)}` : ''}
              </option>
            )
          })}
        </select>
        <button
          type="button"
          onClick={() => void compile()}
          disabled={!canCompile || loadState !== 'ready' || compiling || !selectedTemplate}
          className="inline-flex h-8 items-center gap-1 rounded bg-primary px-2.5 text-[9px] font-semibold text-primary-foreground disabled:cursor-not-allowed disabled:opacity-40"
        >
          {compiling ? <LoaderCircle className="size-3 animate-spin" /> : <FileCheck2 className="size-3" />}
          {contract ? t('platform.buildContract.recompile') : t('platform.buildContract.compile')}
        </button>
      </div>

      {loadState === 'error' && (
        <div role="alert" className="mt-2 flex items-center gap-2 rounded border border-destructive/30 bg-destructive/10 px-2 py-1.5 text-[8px] text-destructive">
          <span className="min-w-0 flex-1">{buildContractErrorMessage(error, t)}</span>
          <button type="button" onClick={() => setReloadVersion((value) => value + 1)} disabled={compiling} className="inline-flex shrink-0 items-center gap-1 rounded border border-destructive/30 px-1.5 py-1 font-semibold disabled:opacity-40">
            <RefreshCw className="size-2.5" /> {t('platform.buildContract.retry')}
          </button>
        </div>
      )}

      {loadState === 'ready' && templates.length === 0 && (
        <p role="status" className="mt-2 rounded border border-warning/30 bg-warning/10 px-2 py-1.5 text-[8px] leading-relaxed text-warning">
          {t('platform.buildContract.emptyRegistry')}
        </p>
      )}

      {contract && (
        <div className="mt-2 space-y-2">
          <div className="grid grid-cols-5 gap-1.5 max-xl:grid-cols-3 max-md:grid-cols-2">
            <ContractFact label={t('platform.buildContract.must')} value={`${formatNumber(contract.mustReadyCount)}/${formatNumber(contract.mustCount)}`} />
            <ContractFact label={t('platform.buildContract.gaps')} value={formatNumber(contract.blockingCount)} warning={contract.blockingCount > 0} />
            <ContractFact label={t('platform.buildContract.conflicts')} value={formatNumber(contract.conflictCount)} warning={contract.conflictCount > 0} />
            <ContractFact label={t('platform.buildContract.canonicalHash')} value={contract.contractHash} mono />
            <ContractFact label={t('platform.buildContract.storageHash')} value={contract.contentHash} mono />
          </div>
          {sourceKinds.length > 0 && (
            <div className="flex flex-wrap items-center gap-1 text-[7px] text-faint-foreground">
              <span className="mr-1 font-semibold uppercase tracking-wider">{t('platform.buildContract.sources')}</span>
              {sourceKinds.map((kind) => <code key={kind} className="rounded bg-white/5 px-1 py-0.5">{kind}</code>)}
            </div>
          )}
          {(blockingGaps.length > 0 || blockingConflicts.length > 0) && (
            <details className="rounded border border-warning/25 bg-warning/5">
              <summary className="cursor-pointer px-2 py-1 text-[8px] font-semibold text-warning">{t('platform.buildContract.inspectBlockers', { count: formatNumber(blockingGaps.length + blockingConflicts.length) })}</summary>
              <div className="max-h-28 space-y-1 overflow-y-auto border-t border-warning/20 p-1.5 scrollbar-thin">
                {blockingGaps.map((gap) => (
                  <p key={gap.id || gap.code} className="text-[8px] leading-relaxed text-warning"><code>{gap.code}</code> · {gap.message || gap.path}</p>
                ))}
                {blockingConflicts.map((conflict) => (
                  <p key={conflict.id || conflict.code} className="text-[8px] leading-relaxed text-warning"><code>{conflict.code}</code> · {conflict.message}</p>
                ))}
              </div>
            </details>
          )}
        </div>
      )}

      <p className={cn(
        'mt-2 text-[8px] font-medium leading-relaxed',
        gate.ready ? 'text-success' : 'text-warning',
      )}>
        {gate.ready
          ? t('platform.buildContract.nextReady')
          : t('platform.buildContract.nextBlocked')}
      </p>
    </section>
  )
}

function ContractFact({
  label,
  value,
  warning = false,
  mono = false,
}: {
  readonly label: string
  readonly value: string
  readonly warning?: boolean
  readonly mono?: boolean
}) {
  return (
    <div className="min-w-0 rounded border border-border bg-background px-2 py-1.5">
      <div className="text-[7px] uppercase tracking-wider text-faint-foreground">{label}</div>
      <div className={cn(
        'mt-0.5 truncate text-[8px]',
        warning ? 'text-warning' : 'text-muted-foreground',
        mono && 'font-mono',
      )} title={value}>{value || '—'}</div>
    </div>
  )
}

function buildContractErrorMessage(
  error: unknown,
  t: ReturnType<typeof useI18n>['t'],
) {
  if (error instanceof PlatformHttpError) {
    if (error.status === 403) return t('platform.buildContract.errorForbidden')
    if (error.status === 404) return t('platform.buildContract.errorNotFound')
    if (error.status === 409) return t('platform.buildContract.errorConflict')
    if (error.status === 422) return t('platform.buildContract.errorInvalid')
    if (error.status >= 500) return t('platform.buildContract.errorUnavailable')
  }
  return t('platform.buildContract.errorGeneric')
}
