'use client'

import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import {
  PREVIEW_DEVICE_DIMENSIONS,
  PREVIEW_DIMENSION_LIMITS,
  buildPreviewErrorComposerContext,
  buildPreviewSelectionComposerContext,
  calculatePreviewFitScale,
  createSandboxPreviewDocument,
  inlineSafePreviewAssets,
  normalizePreviewRoute,
  normalizePreviewInspectionRect,
  parsePreviewDimension,
  type PreviewDimensionAxis,
  type PreviewDimensions,
  type PreviewInspectionMode,
  type PreviewInspectionRect,
} from '@/lib/worksflow/preview-controls'
import { useWorksflow } from '@/lib/worksflow/store'
import {
  ArrowRight,
  Copy,
  ExternalLink,
  Loader2,
  Maximize2,
  Monitor,
  MousePointer2,
  PanelBottom,
  RotateCw,
  Scan,
  Smartphone,
  Tablet,
  TerminalSquare,
  TriangleAlert,
  X,
} from 'lucide-react'

type DevicePresetName = keyof typeof PREVIEW_DEVICE_DIMENSIONS
type DevicePreset = DevicePresetName | 'custom'
type PreviewLogLevel = 'log' | 'info' | 'warn' | 'error'
type PreviewZoom = 'fit' | 50 | 75 | 100 | 125 | 150

interface PreviewLog {
  id: number
  level: PreviewLogLevel
  message: string
  kind: 'console' | 'runtime' | 'unhandled-rejection'
  route: string
  stack?: string
  source?: string
  line?: number
  column?: number
}

const ZOOM_OPTIONS: PreviewZoom[] = ['fit', 50, 75, 100, 125, 150]

export function PreviewPanel() {
  const { t } = useI18n()
  const {
    phase,
    previewDocument,
    workspace,
    retryGeneration,
    generationError,
    setComposerDraft,
    setGenerationMode,
    setPlanMode,
    setView,
  } = useWorksflow()
  const [device, setDevice] = useState<DevicePreset>('desktop')
  const [dimensions, setDimensions] = useState<PreviewDimensions>({
    ...PREVIEW_DEVICE_DIMENSIONS.desktop,
  })
  const [widthInput, setWidthInput] = useState(String(PREVIEW_DEVICE_DIMENSIONS.desktop.width))
  const [heightInput, setHeightInput] = useState(String(PREVIEW_DEVICE_DIMENSIONS.desktop.height))
  const [dimensionError, setDimensionError] = useState<PreviewDimensionAxis | null>(null)
  const [zoom, setZoom] = useState<PreviewZoom>('fit')
  const [fitScale, setFitScale] = useState(1)
  const [route, setRoute] = useState('/')
  const [routeInput, setRouteInput] = useState('/')
  const [runtimeLocation, setRuntimeLocation] = useState('about:srcdoc#/')
  const [fullscreen, setFullscreen] = useState(false)
  const [refreshKey, setRefreshKey] = useState(0)
  const [readyKey, setReadyKey] = useState<string | null>(null)
  const [inspectionMode, setInspectionMode] = useState<PreviewInspectionMode | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [logs, setLogs] = useState<PreviewLog[]>([])
  const [consoleOpen, setConsoleOpen] = useState(false)
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const previewAreaRef = useRef<HTMLDivElement>(null)
  const noticeTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const announceRefresh = useRef(false)
  const channelId = `preview-${useId().replace(/:/g, '')}`
  const previewHtml = useMemo(
    () => inlineSafePreviewAssets(
      previewDocument.html,
      previewDocument.entryPath ?? 'index.html',
      workspace.files,
    ),
    [previewDocument.entryPath, previewDocument.html, workspace.files],
  )
  const srcDoc = useMemo(
    () => createSandboxPreviewDocument(previewHtml, channelId, inspectionMode),
    [channelId, inspectionMode, previewHtml],
  )
  const iframeKey = `${workspace.revision}-${refreshKey}-${inspectionMode ?? 'browse'}`
  const loading = readyKey !== iframeKey
  const previewScale = zoom === 'fit' ? fitScale : zoom / 100

  const showNotice = useCallback((message: string) => {
    if (noticeTimer.current) clearTimeout(noticeTimer.current)
    setNotice(message)
    noticeTimer.current = setTimeout(() => setNotice(null), 1800)
  }, [])

  const refreshPreview = useCallback(() => {
    announceRefresh.current = true
    setLogs([])
    setRefreshKey((value) => value + 1)
  }, [])

  const finishLoad = useCallback(() => {
    setReadyKey(iframeKey)
    if (announceRefresh.current) {
      announceRefresh.current = false
      showNotice(t('preview.refreshed'))
    }
  }, [iframeKey, showNotice, t])

  const sendRouteToPreview = useCallback((nextRoute: string, replace = false) => {
    iframeRef.current?.contentWindow?.postMessage(
      {
        source: 'worksflow-preview-host',
        channelId,
        type: 'navigate',
        route: nextRoute,
        replace,
      },
      '*',
    )
  }, [channelId])

  const sendContextToComposer = useCallback((draft: string, mode: 'iterate' | 'fix') => {
    setComposerDraft(draft)
    setPlanMode(false)
    setGenerationMode(mode)
    setInspectionMode(null)
    setView('preview')
    requestAnimationFrame(() => {
      const composer = document.querySelector<HTMLTextAreaElement>('textarea[aria-autocomplete="list"]')
      composer?.focus()
      composer?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
    })
  }, [setComposerDraft, setGenerationMode, setPlanMode, setView])

  useEffect(() => {
    function receivePreviewEvent(event: MessageEvent) {
      const data = event.data as unknown
      if (
        event.source !== iframeRef.current?.contentWindow ||
        !isPreviewMessage(data) ||
        data.channelId !== channelId
      ) return
      if (data.type === 'log') {
        setLogs((current) => [
          ...current,
          {
            id: Date.now() + current.length,
            level: data.level,
            message: data.message,
            kind: data.kind,
            route: normalizePreviewRoute(data.route, route),
            stack: data.stack,
            source: data.filename,
            line: data.line,
            column: data.column,
          },
        ].slice(-100))
      }
      if (data.type === 'selected' || data.type === 'region-selected') {
        const rect = normalizePreviewInspectionRect(
          data.rect.x,
          data.rect.y,
          data.rect.x + data.rect.width,
          data.rect.y + data.rect.height,
          dimensions,
        )
        sendContextToComposer(buildPreviewSelectionComposerContext({
          kind: data.type === 'region-selected' ? 'region' : 'element',
          selector: data.selector,
          text: data.text,
          route: data.route,
          ...(rect ? { rect } : {}),
        }, route), 'iterate')
        showNotice(t('preview.selectionAdded'))
      }
      if (data.type === 'route' || data.type === 'ready') {
        const nextRoute = normalizePreviewRoute(data.route)
        setRoute(nextRoute)
        setRouteInput(nextRoute)
        setRuntimeLocation(data.location)
      }
      if (data.type === 'ready') finishLoad()
      if (data.type === 'reload') refreshPreview()
      if (data.type === 'escape') {
        setInspectionMode(null)
        setFullscreen(false)
      }
    }
    window.addEventListener('message', receivePreviewEvent)
    return () => window.removeEventListener('message', receivePreviewEvent)
  }, [channelId, dimensions, finishLoad, refreshPreview, route, sendContextToComposer, showNotice, t])

  useEffect(() => {
    const previewArea = previewAreaRef.current
    if (!previewArea || typeof ResizeObserver === 'undefined') return

    const updateFitScale = () => {
      const bounds = previewArea.getBoundingClientRect()
      setFitScale(calculatePreviewFitScale(bounds.width, bounds.height, dimensions))
    }
    updateFitScale()
    const observer = new ResizeObserver(updateFitScale)
    observer.observe(previewArea)
    return () => observer.disconnect()
  }, [dimensions])

  useEffect(
    () => () => {
      if (noticeTimer.current) clearTimeout(noticeTimer.current)
    },
    [],
  )

  function selectDevice(nextDevice: DevicePresetName) {
    const nextDimensions = PREVIEW_DEVICE_DIMENSIONS[nextDevice]
    setDevice(nextDevice)
    setDimensions({ ...nextDimensions })
    setWidthInput(String(nextDimensions.width))
    setHeightInput(String(nextDimensions.height))
    setDimensionError(null)
    setZoom('fit')
  }

  function commitDimension(axis: PreviewDimensionAxis) {
    const input = axis === 'width' ? widthInput : heightInput
    const value = parsePreviewDimension(input, axis)
    if (value === null) {
      setDimensionError(axis)
      if (axis === 'width') setWidthInput(String(dimensions.width))
      else setHeightInput(String(dimensions.height))
      return
    }

    setDimensionError(null)
    setDevice('custom')
    setDimensions((current) => ({ ...current, [axis]: value }))
    if (axis === 'width') setWidthInput(String(value))
    else setHeightInput(String(value))
  }

  function navigatePreview(replace = false) {
    const nextRoute = normalizePreviewRoute(routeInput, route)
    setRoute(nextRoute)
    setRouteInput(nextRoute)
    sendRouteToPreview(nextRoute, replace)
  }

  function openPreview() {
    const blob = new Blob([srcDoc], { type: 'text/html' })
    const url = URL.createObjectURL(blob)
    const opened = window.open(`${url}#${route}`, '_blank', 'noopener,noreferrer')
    setTimeout(() => URL.revokeObjectURL(url), 60_000)
    showNotice(opened ? t('preview.opened') : t('preview.openBlocked'))
  }

  async function copyPreview() {
    const blob = new Blob([srcDoc], { type: 'text/html' })
    const url = URL.createObjectURL(blob)
    try {
      await navigator.clipboard?.writeText(`${url}#${route}`)
      showNotice(t('preview.urlCopied'))
    } catch {
      showNotice(t('preview.copyFailed'))
    }
    setTimeout(() => URL.revokeObjectURL(url), 60_000)
  }

  return (
    <div
      className={cn(
        'relative flex h-full flex-col bg-panel',
        fullscreen &&
          'fixed inset-0 z-[70] overflow-hidden border border-border shadow-2xl shadow-black/60 sm:inset-4 sm:rounded-lg',
      )}
      onKeyDown={(event) => {
        if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'r') {
          event.preventDefault()
          refreshPreview()
        }
        if (event.key === 'Escape') {
          if (inspectionMode) setInspectionMode(null)
          if (fullscreen) setFullscreen(false)
        }
      }}
    >
      <div className="shrink-0 border-b border-border bg-panel">
        <div className="flex min-h-10 items-center gap-1.5 px-2 sm:gap-2 sm:px-2.5">
          <ToolbarButton label={`${t('preview.refresh')} (Ctrl/⌘ R)`} onClick={refreshPreview}>
            <RotateCw className={cn('h-3.5 w-3.5', loading && 'animate-spin')} />
          </ToolbarButton>
          <form
            className="flex min-w-0 flex-1 items-center rounded-md bg-white/5 px-2 font-mono text-[10px] text-muted-foreground focus-within:ring-1 focus-within:ring-primary/50 sm:text-[11px]"
            onSubmit={(event) => {
              event.preventDefault()
              navigatePreview()
            }}
            aria-label={t('preview.route')}
            title={runtimeLocation}
          >
            <span className="hidden max-w-40 shrink-0 truncate text-faint-foreground md:inline">
              preview://{previewDocument.entryPath ?? 'index.html'}
            </span>
            <input
              value={routeInput}
              onChange={(event) => setRouteInput(event.target.value)}
              onBlur={() => setRouteInput(normalizePreviewRoute(routeInput, route))}
              className="min-h-6 min-w-16 flex-1 bg-transparent px-1.5 py-1 text-foreground outline-none"
              aria-label={t('preview.virtualRoute')}
              autoCapitalize="off"
              autoComplete="off"
              spellCheck={false}
            />
            <button
              type="submit"
              className="min-h-6 min-w-6 rounded p-1 text-faint-foreground hover:bg-white/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              aria-label={t('preview.navigateRoute')}
              title={t('preview.navigate')}
            >
              <ArrowRight className="h-3 w-3" />
            </button>
          </form>
          <ToolbarButton label={t('preview.copyLink')} onClick={() => void copyPreview()}>
            <Copy className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton label={t('preview.openNewWindow')} onClick={openPreview}>
            <ExternalLink className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton label={t('preview.fullscreen')} active={fullscreen} onClick={() => setFullscreen((value) => !value)}>
            <Maximize2 className="h-3.5 w-3.5" />
          </ToolbarButton>
        </div>
        <div className="flex min-h-9 flex-wrap items-center gap-1 border-t border-white/[0.04] px-2 py-1 sm:gap-1.5 sm:px-2.5">
          <div className="flex items-center" role="group" aria-label={t('preview.devicePresets')}>
            <ToolbarButton label={t('preview.mobileView')} active={device === 'mobile'} onClick={() => selectDevice('mobile')}>
              <Smartphone className="h-3.5 w-3.5" />
            </ToolbarButton>
            <ToolbarButton label={t('preview.tabletView')} active={device === 'tablet'} onClick={() => selectDevice('tablet')}>
              <Tablet className="h-3.5 w-3.5" />
            </ToolbarButton>
            <ToolbarButton label={t('preview.desktopView')} active={device === 'desktop'} onClick={() => selectDevice('desktop')}>
              <Monitor className="h-3.5 w-3.5" />
            </ToolbarButton>
          </div>
          <span className="mx-0.5 h-4 w-px bg-border" aria-hidden="true" />
          <DimensionInput
            axis="width"
            value={widthInput}
            invalid={dimensionError === 'width'}
            onChange={(value) => {
              setWidthInput(value)
              if (dimensionError === 'width') setDimensionError(null)
            }}
            onCommit={() => commitDimension('width')}
          />
          <span className="text-[9px] text-faint-foreground" aria-hidden="true">×</span>
          <DimensionInput
            axis="height"
            value={heightInput}
            invalid={dimensionError === 'height'}
            onChange={(value) => {
              setHeightInput(value)
              if (dimensionError === 'height') setDimensionError(null)
            }}
            onCommit={() => commitDimension('height')}
          />
          <select
            value={zoom}
            onChange={(event) => setZoom(event.target.value === 'fit' ? 'fit' : Number(event.target.value) as PreviewZoom)}
            className="h-7 rounded-md border border-border bg-background px-1.5 text-[10px] text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-primary"
            aria-label={t('preview.zoom')}
          >
            {ZOOM_OPTIONS.map((option) => (
              <option key={option} value={option}>{option === 'fit' ? t('preview.fit') : `${option}%`}</option>
            ))}
          </select>
          <div
            className="ml-auto flex items-center gap-1 whitespace-nowrap font-mono text-[9px] text-faint-foreground"
            role="status"
            aria-live="polite"
            aria-label={t('preview.status', { status: loading ? t('common.loading') : t('workbench.panel.ready'), revision: workspace.revision, reload: refreshKey })}
          >
            <span className={cn('h-1.5 w-1.5 rounded-full', loading ? 'animate-pulse bg-warning' : 'bg-success')} />
            {loading ? t('common.loading') : t('workbench.panel.ready')} · r{workspace.revision}.{refreshKey}
          </div>
          <div className="flex items-center" role="group" aria-label={t('preview.inspectionTools')}>
            <ToolbarButton
              label={t('preview.inspect')}
              active={inspectionMode === 'element'}
              onClick={() => setInspectionMode((current) => current === 'element' ? null : 'element')}
            >
              <MousePointer2 className="h-3.5 w-3.5" />
            </ToolbarButton>
            <ToolbarButton
              label={t('preview.selectRegion')}
              active={inspectionMode === 'region'}
              onClick={() => setInspectionMode((current) => current === 'region' ? null : 'region')}
            >
              <Scan className="h-3.5 w-3.5" />
            </ToolbarButton>
          </div>
          <ToolbarButton label={t('preview.console')} active={consoleOpen} onClick={() => setConsoleOpen((value) => !value)}>
            <TerminalSquare className="h-3.5 w-3.5" />
            {logs.some((log) => log.level === 'error') && <span className="absolute right-0.5 top-0.5 h-1.5 w-1.5 rounded-full bg-destructive" />}
          </ToolbarButton>
        </div>
      </div>

      <div ref={previewAreaRef} className="relative min-h-0 flex-1 overflow-auto bg-[#0b0b0d] scrollbar-thin">
        {inspectionMode && phase === 'complete' && (
          <div
            className="pointer-events-none sticky left-1/2 top-2 z-20 flex w-max max-w-[calc(100%_-_16px)] -translate-x-1/2 items-center gap-2 rounded-md border border-primary/40 bg-[#111114]/95 px-2.5 py-1.5 text-[10px] text-foreground shadow-xl backdrop-blur"
            role="status"
            aria-live="polite"
          >
            <span>
              {inspectionMode === 'region'
                ? t('preview.regionSelectionHint')
                : t('preview.elementSelectionHint')}
            </span>
            <button
              type="button"
              onClick={() => setInspectionMode(null)}
              className="pointer-events-auto rounded border border-border px-1.5 py-0.5 text-faint-foreground hover:bg-white/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            >
              {t('preview.cancelEsc')}
            </button>
          </div>
        )}
        {phase === 'planning' || phase === 'building' ? (
          <LoadingState />
        ) : phase === 'error' ? (
          <ErrorState message={generationError} onRetry={retryGeneration} />
        ) : phase === 'complete' ? (
          <div className="flex min-h-full min-w-full items-start justify-center p-3">
            <div
              className="relative shrink-0 transition-[width,height] duration-200"
              style={{
                width: `${Math.round(dimensions.width * previewScale)}px`,
                height: `${Math.round(dimensions.height * previewScale)}px`,
              }}
            >
              <div
                className="relative origin-top-left overflow-hidden rounded-md border border-white/10 bg-white shadow-2xl"
                style={{
                  width: `${dimensions.width}px`,
                  height: `${dimensions.height}px`,
                  transform: `scale(${previewScale})`,
                }}
              >
                <iframe
                  ref={iframeRef}
                  key={iframeKey}
                  srcDoc={srcDoc}
                  sandbox="allow-scripts allow-modals"
                  onLoad={() => {
                    sendRouteToPreview(route, true)
                    finishLoad()
                  }}
                  className="h-full w-full border-0 bg-white"
                  title={t('preview.generatedTitle')}
                />
                <span className="sr-only">Taskflow</span>
                <footer className="sr-only">{t('preview.madeIn')}</footer>
                {loading && (
                  <div className="absolute inset-0 grid place-items-center bg-background/70 backdrop-blur-sm" role="status" aria-label={t('preview.loading')}>
                    <Loader2 className="h-6 w-6 animate-spin text-primary-bright" />
                  </div>
                )}
              </div>
            </div>
          </div>
        ) : (
          <EmptyState />
        )}
      </div>

      {consoleOpen && (
        <div
          className="absolute inset-x-2 bottom-2 z-20 flex max-h-44 flex-col overflow-hidden rounded-lg border border-border bg-[#111114]/95 shadow-2xl backdrop-blur"
          role="region"
          aria-label={t('preview.runtimeConsole')}
        >
          <div className="flex items-center border-b border-border px-3 py-2">
            <PanelBottom className="mr-2 h-3.5 w-3.5 text-primary-bright" />
            <span className="text-[11px] font-semibold text-foreground">{t('preview.runtimeConsole')}</span>
            <button type="button" onClick={() => setLogs([])} className="ml-auto rounded text-[10px] text-faint-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary">{t('preview.clearConsole')}</button>
            <button type="button" onClick={() => setConsoleOpen(false)} className="ml-2 rounded p-0.5 text-faint-foreground hover:bg-white/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary" aria-label={t('common.close')}><X className="h-3.5 w-3.5" /></button>
          </div>
          <div
            className="min-h-16 overflow-y-auto p-2 font-mono text-[10px] scrollbar-thin"
            role="log"
            aria-live="polite"
            aria-relevant="additions"
          >
            {logs.length === 0 ? (
              <p className="px-1 py-2 text-faint-foreground">{t('preview.noConsoleMessages')}</p>
            ) : (
              logs.map((log) => (
                <div
                  key={log.id}
                  className={cn(
                    'flex items-start gap-2 rounded px-1.5 py-1',
                    log.level === 'error'
                      ? 'text-destructive'
                      : log.level === 'warn'
                        ? 'text-warning'
                        : 'text-muted-foreground',
                  )}
                >
                  <span className="min-w-0 flex-1 break-words">
                    [{log.level}] {log.message}
                    <span className="ml-2 text-[9px] text-faint-foreground">{log.route}</span>
                  </span>
                  {log.level === 'error' && (
                    <button
                      type="button"
                      onClick={() => {
                        sendContextToComposer(buildPreviewErrorComposerContext(log, route), 'fix')
                        setConsoleOpen(false)
                        showNotice(t('preview.runtimeErrorAdded'))
                      }}
                      className="shrink-0 rounded border border-destructive/40 px-1.5 py-0.5 text-[9px] font-sans font-medium text-destructive hover:bg-destructive/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                      aria-label={t('preview.sendErrorAria', { message: log.message.slice(0, 120) })}
                    >
                      {t('preview.sendToComposer')}
                    </button>
                  )}
                </div>
              ))
            )}
          </div>
        </div>
      )}

      {notice && (
        <div className="absolute bottom-4 left-1/2 z-30 -translate-x-1/2 rounded-md border border-border bg-popover px-3 py-2 text-xs text-muted-foreground shadow-2xl">
          {notice}
        </div>
      )}
    </div>
  )
}

function isPreviewMessage(value: unknown): value is
  | {
      source: 'worksflow-preview'
      channelId: string
      type: 'log'
      level: PreviewLogLevel
      message: string
      kind: 'console' | 'runtime' | 'unhandled-rejection'
      route: string
      stack?: string
      filename?: string
      line?: number
      column?: number
    }
  | {
      source: 'worksflow-preview'
      channelId: string
      type: 'selected' | 'region-selected'
      selector: string
      text: string
      route: string
      rect: PreviewInspectionRect
    }
  | { source: 'worksflow-preview'; channelId: string; type: 'route' | 'ready'; route: string; location: string }
  | { source: 'worksflow-preview'; channelId: string; type: 'reload' | 'escape' } {
  if (!value || typeof value !== 'object') return false
  const data = value as Record<string, unknown>
  if (data.source !== 'worksflow-preview' || typeof data.channelId !== 'string') return false
  if (data.type === 'log') {
    return isPreviewLogLevel(data.level) &&
      typeof data.message === 'string' &&
      data.message.length <= 4_000 &&
      isPreviewLogKind(data.kind) &&
      isBoundedRoute(data.route) &&
      optionalBoundedString(data.stack, 4_000) &&
      optionalBoundedString(data.filename, 1_024) &&
      optionalFiniteNumber(data.line) &&
      optionalFiniteNumber(data.column)
  }
  if (data.type === 'selected' || data.type === 'region-selected') {
    return typeof data.selector === 'string' &&
      data.selector.length <= 1_024 &&
      typeof data.text === 'string' &&
      data.text.length <= 1_000 &&
      isBoundedRoute(data.route) &&
      isPreviewInspectionRect(data.rect)
  }
  if (data.type === 'route' || data.type === 'ready') {
    return isBoundedRoute(data.route) &&
      typeof data.location === 'string' &&
      data.location.length <= 2_048
  }
  if (data.type === 'reload' || data.type === 'escape') return true
  return false
}

function isPreviewLogLevel(value: unknown): value is PreviewLogLevel {
  return value === 'log' || value === 'info' || value === 'warn' || value === 'error'
}

function isPreviewLogKind(value: unknown): value is PreviewLog['kind'] {
  return value === 'console' || value === 'runtime' || value === 'unhandled-rejection'
}

function optionalBoundedString(value: unknown, maximumLength: number) {
  return value === undefined || (typeof value === 'string' && value.length <= maximumLength)
}

function optionalFiniteNumber(value: unknown) {
  return value === undefined || (typeof value === 'number' && Number.isFinite(value))
}

function isBoundedRoute(value: unknown) {
  return typeof value === 'string' && value.length <= 512
}

function isPreviewInspectionRect(value: unknown): value is PreviewInspectionRect {
  if (!value || typeof value !== 'object') return false
  const rect = value as Record<string, unknown>
  return ['x', 'y', 'width', 'height'].every(
    (key) => typeof rect[key] === 'number' &&
      Number.isFinite(rect[key]) &&
      rect[key] >= 0 &&
      rect[key] <= 100_000,
  )
}

function DimensionInput({
  axis,
  value,
  invalid,
  onChange,
  onCommit,
}: {
  axis: PreviewDimensionAxis
  value: string
  invalid: boolean
  onChange: (value: string) => void
  onCommit: () => void
}) {
  const { t } = useI18n()
  const limits = PREVIEW_DIMENSION_LIMITS[axis]
  const hintId = `preview-${axis}-range`

  return (
    <label
      className="flex h-7 items-center rounded-md border border-border bg-background px-1.5 text-[9px] text-faint-foreground focus-within:ring-2 focus-within:ring-primary"
      title={`${limits.min}–${limits.max}px`}
    >
      <span aria-hidden="true">{t(axis === 'width' ? 'preview.widthShort' : 'preview.heightShort')}</span>
      <input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onBlur={onCommit}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault()
            onCommit()
          }
        }}
        className={cn(
          'h-6 w-10 bg-transparent pl-1 text-right font-mono text-[10px] text-foreground outline-none',
          invalid && 'text-destructive',
        )}
        inputMode="numeric"
        maxLength={6}
        aria-label={t(axis === 'width' ? 'preview.widthPixels' : 'preview.heightPixels')}
        aria-describedby={hintId}
        aria-invalid={invalid}
      />
      <span aria-hidden="true">px</span>
      <span id={hintId} className="sr-only">
        {t('preview.allowedRange', { min: limits.min, max: limits.max })}
      </span>
    </label>
  )
}

function ToolbarButton({
  children,
  label,
  active,
  onClick,
}: {
  children: React.ReactNode
  label: string
  active?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'relative flex h-7 w-7 shrink-0 items-center justify-center rounded-md hover:bg-white/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
        active ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground',
      )}
      aria-label={label}
      aria-pressed={active}
      title={label}
    >
      {children}
    </button>
  )
}

function EmptyState() {
  const { t } = useI18n()
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
      <Monitor className="h-8 w-8 text-faint-foreground" />
      <p className="text-[13px] text-muted-foreground">{t('preview.emptyTitle')}</p>
      <p className="max-w-xs text-[12px] text-faint-foreground">{t('preview.emptyHint')}</p>
    </div>
  )
}

function LoadingState() {
  const { t } = useI18n()
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3">
      <Loader2 className="h-6 w-6 animate-spin text-primary-bright" />
      <p className="text-[13px] text-muted-foreground">{t('preview.loading')}</p>
    </div>
  )
}

function ErrorState({ message, onRetry }: { message: string | null; onRetry: () => void }) {
  const { t } = useI18n()
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
      <TriangleAlert className="h-8 w-8 text-destructive" />
      <div>
        <p className="text-[13px] font-medium text-foreground">{t('preview.errorTitle')}</p>
        <p className="mt-1 max-w-sm text-[12px] leading-relaxed text-muted-foreground">{message ?? t('preview.errorCopy')}</p>
      </div>
      <button type="button" onClick={onRetry} className="rounded-md bg-primary px-3 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright">{t('preview.retryBuild')}</button>
    </div>
  )
}
