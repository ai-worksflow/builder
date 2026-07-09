'use client'

import { useRef, useState } from 'react'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import { MEMBERS, VERSIONS } from '@/lib/worksflow/mock-data'
import { DOC_STATUS_CLASS } from '@/lib/worksflow/labels'
import type {
  BindingTargetKind,
  Blueprint,
  BlueprintNodeType,
  BlueprintEdgeType,
  DependencyType,
  DocMemberRole,
  ImportAsset,
  TeamDocument,
} from '@/lib/worksflow/types'
import { useLocalizedLabels } from '../use-localized-labels'
import { Avatar, StatusPill, memberById } from '../shared'
import {
  ArrowRight,
  GitFork,
  Layers,
  Link2,
  Maximize2,
  Move,
  PenLine,
  TriangleAlert,
  Workflow,
} from 'lucide-react'

const NODE_W = 210
const NODE_H = 96
const OFFSET_X = 24
const OFFSET_Y = 64
const MIN_DOC_Y = -40
const RELATION_TYPES: DependencyType[] = [
  'references',
  'depends_on',
  'generates',
  'blocks',
  'implements',
  'reviews',
  'composes',
  'derives_from',
  'syncs_with',
]
const MEMBER_ROLES: DocMemberRole[] = ['owner', 'assignee', 'downstreamOwner', 'reviewer', 'watcher']
type RelationFilter = 'all' | 'blocking' | 'review' | 'implementation' | 'prototype'
const RELATION_FILTERS: { id: RelationFilter; labelKey: MessageKey }[] = [
  { id: 'all', labelKey: 'graph.filter.all' },
  { id: 'blocking', labelKey: 'graph.filter.blocking' },
  { id: 'review', labelKey: 'graph.filter.review' },
  { id: 'implementation', labelKey: 'graph.filter.implementation' },
  { id: 'prototype', labelKey: 'graph.filter.prototype' },
]

function bindingRelationLabel(
  relation: DependencyType | BlueprintEdgeType,
  labels: LocalizedLabels,
) {
  if (RELATION_TYPES.includes(relation as DependencyType)) {
    return labels.dependency(relation as DependencyType)
  }
  return labels.blueprintEdge(relation as BlueprintEdgeType)
}

type BindingOption = {
  kind: BindingTargetKind
  id: string
  label: string
  meta: string
}

type LocalizedLabels = ReturnType<typeof useLocalizedLabels>

function statusDot(status: TeamDocument['status']) {
  switch (status) {
    case 'approved':
      return 'bg-success'
    case 'readyForReview':
      return 'bg-primary-bright'
    case 'changesRequested':
    case 'needsSync':
      return 'bg-warning'
    case 'draft':
      return 'bg-faint-foreground'
    default:
      return 'bg-faint-foreground'
  }
}

export function DocumentGraph() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const {
    activeTeamProject,
    selectedDocId,
    setSelectedDocId,
    openDoc,
    documents,
    moveDocumentNode,
    dependencies,
    importAssets,
    blueprint,
    nodeBindings,
    addDocumentDependency,
    addNodeBinding,
    addDocumentMember,
    useDocInWorkbench,
    createBlankDocumentGraph,
    createDocumentGraphFromTemplate,
    createDocumentGraphFromBlueprint,
  } = useWorksflow()
  const [hoverEdge, setHoverEdge] = useState<string | null>(null)
  const [connectFrom, setConnectFrom] = useState<string | null>(null)
  const connectFromRef = useRef<string | null>(null)
  const [relationType, setRelationType] = useState<DependencyType>('references')
  const [blocking, setBlocking] = useState(false)
  const [requiredForReview, setRequiredForReview] = useState(false)
  const [notifyOnChange, setNotifyOnChange] = useState(true)
  const [memberRole, setMemberRole] = useState<DocMemberRole>('watcher')
  const [targetValue, setTargetValue] = useState('')
  const [relationFilter, setRelationFilter] = useState<RelationFilter>('all')
  const [notice, setNotice] = useState<string | null>(null)
  const [draggingDocId, setDraggingDocId] = useState<string | null>(null)
  const dragSessionRef = useRef<{
    id: string
    pointerId: number
    offsetX: number
    offsetY: number
    canvasLeft: number
    canvasTop: number
    lastX: number
    lastY: number
    moved: boolean
  } | null>(null)
  const scrollLockRef = useRef<
    Array<{
      element: HTMLElement
      overflow: string
      overflowX: string
      overflowY: string
      scrollLeft: number
      scrollTop: number
    }>
  >([])
  const suppressNodeClickRef = useRef(false)
  const scrollFrameRef = useRef<HTMLDivElement | null>(null)
  const canvasRef = useRef<HTMLDivElement | null>(null)

  const selected = documents.find((d) => d.id === selectedDocId) ?? null

  const canvasWidth = 1560
  const canvasHeight = 420

  const nodeCenter = (doc: TeamDocument) => ({
    x: doc.position.x + OFFSET_X + NODE_W / 2,
    y: doc.position.y + OFFSET_Y + NODE_H / 2,
  })

  const visibleDependencies = dependencies.filter((edge) =>
    relationFilter === 'all'
      ? true
      : relationFilter === 'blocking'
        ? edge.isBlocking || edge.type === 'blocks'
        : relationFilter === 'review'
          ? edge.type === 'reviews'
          : relationFilter === 'implementation'
            ? edge.type === 'implements'
            : edge.type === 'syncs_with' ||
              documents.find((doc) => doc.id === edge.targetDocId)?.type === 'uiPrototype',
  )

  const relatedEdges = visibleDependencies.filter(
    (e) => e.sourceDocId === selectedDocId || e.targetDocId === selectedDocId,
  )
  const relatedBindings = nodeBindings.filter(
    (binding) =>
      (binding.sourceKind === 'document' && binding.sourceId === selectedDocId) ||
      (binding.targetKind === 'document' && binding.targetId === selectedDocId),
  )
  const bindingOptions = buildBindingOptions(documents, blueprint, importAssets, labels, t)
  const selectedTarget = bindingOptions.find((option) => optionKey(option) === targetValue)
  const targetKind = selectedTarget?.kind

  function setPendingConnection(id: string | null) {
    connectFromRef.current = id
    setConnectFrom(id)
  }

  function createBinding(sourceDocId: string, targetDocId: string) {
    if (sourceDocId === targetDocId) return
    addDocumentDependency(
      sourceDocId,
      targetDocId,
      relationType,
      blocking || relationType === 'blocks',
    )
    setNotice(
      `${documents.find((d) => d.id === sourceDocId)?.title ?? t('common.source')} ${
        labels.dependency(relationType)
      } ${documents.find((d) => d.id === targetDocId)?.title ?? t('common.target')}`,
    )
    setPendingConnection(null)
    setSelectedDocId(targetDocId)
  }

  function selectNode(doc: TeamDocument) {
    const pendingConnection = connectFromRef.current
    if (pendingConnection && pendingConnection !== doc.id) {
      createBinding(pendingConnection, doc.id)
      return
    }
    setSelectedDocId(doc.id)
  }

  function beginDocumentDrag(event: React.PointerEvent<HTMLElement>, doc: TeamDocument) {
    if ((event.target as HTMLElement).closest('[data-doc-connect-handle]')) return
    if (event.button !== 0) return
    const canvas = canvasRef.current
    if (!canvas) return
    event.preventDefault()
    event.currentTarget.setPointerCapture(event.pointerId)
    lockDragScroll()
    const rect = event.currentTarget.getBoundingClientRect()
    const canvasRect = canvas.getBoundingClientRect()
    dragSessionRef.current = {
      id: doc.id,
      pointerId: event.pointerId,
      offsetX: event.clientX - rect.left,
      offsetY: event.clientY - rect.top,
      canvasLeft: canvasRect.left,
      canvasTop: canvasRect.top,
      lastX: doc.position.x,
      lastY: doc.position.y,
      moved: false,
    }
    setDraggingDocId(doc.id)
  }

  function moveDocumentWithPointer(event: React.PointerEvent<HTMLElement>, docId: string) {
    const session = dragSessionRef.current
    const canvas = canvasRef.current
    if (!session || session.id !== docId || !canvas) return
    event.preventDefault()
    const x = Math.max(
      0,
      Math.min(
        canvasWidth - NODE_W - OFFSET_X * 2,
        event.clientX - session.canvasLeft - session.offsetX - OFFSET_X,
      ),
    )
    const y = Math.max(
      MIN_DOC_Y,
      Math.min(
        canvasHeight - NODE_H - OFFSET_Y,
        event.clientY - session.canvasTop - session.offsetY - OFFSET_Y,
      ),
    )
    if (Math.abs(session.lastX - x) < 0.5 && Math.abs(session.lastY - y) < 0.5) return
    session.lastX = x
    session.lastY = y
    session.moved = true
    moveDocumentNode(session.id, { x, y })
  }

  function finishDocumentDrag(event: React.PointerEvent<HTMLElement>, docId: string) {
    const session = dragSessionRef.current
    if (!session || session.id !== docId) return
    const moved = session.moved
    const movedDocId = session.id
    if (event.currentTarget.hasPointerCapture(session.pointerId)) {
      event.currentTarget.releasePointerCapture(session.pointerId)
    }
    if (moved) {
      suppressNodeClickRef.current = true
      setNotice(t('graph.nodePositionUpdated'))
    }
    dragSessionRef.current = null
    setDraggingDocId(null)
    unlockDragScroll()
    if (moved) setSelectedDocId(movedDocId)
  }

  function lockDragScroll() {
    unlockDragScroll()
    const locks: typeof scrollLockRef.current = []
    let element: HTMLElement | null = scrollFrameRef.current
    while (element && element !== document.body) {
      const style = window.getComputedStyle(element)
      const scrollsY = /(auto|scroll)/.test(style.overflowY) && element.scrollHeight > element.clientHeight
      const scrollsX = /(auto|scroll)/.test(style.overflowX) && element.scrollWidth > element.clientWidth
      if (scrollsY || scrollsX) {
        locks.push({
          element,
          overflow: element.style.overflow,
          overflowX: element.style.overflowX,
          overflowY: element.style.overflowY,
          scrollLeft: element.scrollLeft,
          scrollTop: element.scrollTop,
        })
        element.style.overflow = 'hidden'
      }
      element = element.parentElement
    }
    scrollLockRef.current = locks
  }

  function unlockDragScroll() {
    scrollLockRef.current.forEach((lock) => {
      lock.element.style.overflow = lock.overflow
      lock.element.style.overflowX = lock.overflowX
      lock.element.style.overflowY = lock.overflowY
      lock.element.scrollLeft = lock.scrollLeft
      lock.element.scrollTop = lock.scrollTop
    })
    scrollLockRef.current = []
  }

  function addSelectedTargetBinding() {
    if (!selected || !selectedTarget) return
    if (selectedTarget.kind === 'document' && selectedTarget.id === selected.id) {
      setNotice(t('graph.chooseOtherTarget'))
      return
    }
    addNodeBinding({
      sourceId: selected.id,
      targetKind: selectedTarget.kind,
      targetId: selectedTarget.id,
      label: selectedTarget.label,
      relation: relationType,
      isBlocking: blocking || relationType === 'blocks',
      requiredForReview,
      notifyOnChange,
    })
    if (selectedTarget.kind === 'member') {
      addDocumentMember(selected.id, selectedTarget.id, memberRole)
    }
    setNotice(
      `${selected.title} ${labels.dependency(relationType)} ${selectedTarget.label} (${bindingKindLabel(
        selectedTarget.kind,
        t,
      )})`,
    )
  }

  return (
    <div className="flex h-full max-lg:flex-col max-lg:overflow-y-auto">
      {/* Canvas */}
      <div
        ref={scrollFrameRef}
        className="relative flex-1 overflow-auto scrollbar-thin bg-canvas max-lg:min-h-[520px] max-lg:flex-none"
      >
        {/* Toolbar */}
        <div className="sticky top-0 z-20 flex items-center justify-between gap-3 border-b border-border bg-surface/80 px-5 py-3 backdrop-blur max-md:flex-wrap max-md:px-4">
          <div className="flex items-center gap-2">
            <Workflow className="size-4 text-primary-bright" />
            <span className="text-sm font-semibold text-foreground">{t('graph.title')}</span>
            <span className="text-xs text-faint-foreground">{activeTeamProject.name}</span>
          </div>
          <div className="flex max-w-full items-center gap-3 overflow-x-auto text-[11px] text-faint-foreground scrollbar-thin">
            <Legend color="#4ade80" label={t('graph.legend.references')} />
            <Legend color="#ef4444" label={t('graph.legend.blocking')} dashed />
            <div className="flex shrink-0 items-center gap-1 rounded-md border border-border bg-surface-2 p-0.5">
              {RELATION_FILTERS.map((filter) => (
                <button
                  key={filter.id}
                  type="button"
                  onClick={() => setRelationFilter(filter.id)}
                  className={cn(
                    'rounded px-2 py-0.5 text-[11px] font-medium transition-colors',
                    relationFilter === filter.id
                      ? 'bg-primary/15 text-primary-bright'
                      : 'text-faint-foreground hover:text-foreground',
                  )}
                >
                  {t(filter.labelKey)}
                </button>
              ))}
            </div>
            <button className="ml-2 inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-muted-foreground hover:bg-white/5">
              <Maximize2 className="size-3" /> {t('graph.fit')}
            </button>
          </div>
        </div>

        {documents.length === 0 ? (
          <div className="flex min-h-[420px] items-center justify-center p-6">
            <div className="max-w-lg rounded-lg border border-dashed border-border bg-panel p-5 text-center">
              <GitFork className="mx-auto size-8 text-primary-bright" />
              <h2 className="mt-3 text-base font-semibold text-foreground">{t('graph.emptyTitle')}</h2>
              <p className="mt-2 text-sm leading-relaxed text-muted-foreground">{t('graph.emptyBody')}</p>
              <div className="mt-5 flex flex-wrap justify-center gap-2">
                <button
                  type="button"
                  onClick={createDocumentGraphFromTemplate}
                  className="rounded-lg bg-primary px-3 py-2 text-xs font-semibold text-primary-foreground hover:bg-primary-bright"
                >
                  {t('graph.createFromTemplate')}
                </button>
                <button
                  type="button"
                  onClick={createDocumentGraphFromBlueprint}
                  className="rounded-lg border border-border px-3 py-2 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
                >
                  {t('graph.createFromBlueprint')}
                </button>
                <button
                  type="button"
                  onClick={createBlankDocumentGraph}
                  className="rounded-lg border border-border px-3 py-2 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
                >
                  {t('graph.createBlank')}
                </button>
              </div>
            </div>
          </div>
        ) : (
        <div
          ref={canvasRef}
          className="relative"
          style={{ width: canvasWidth, height: canvasHeight, backgroundSize: '28px 28px' }}
        >
          {/* Edges */}
          <svg className="absolute inset-0" width={canvasWidth} height={canvasHeight}>
            <defs>
              <marker id="arrow-green" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
                <path d="M0,0 L8,4 L0,8 z" fill="#4ade80" />
              </marker>
              <marker id="arrow-red" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
                <path d="M0,0 L8,4 L0,8 z" fill="#ef4444" />
              </marker>
            </defs>
            {visibleDependencies.map((edge) => {
              const src = documents.find((d) => d.id === edge.sourceDocId)!
              const tgt = documents.find((d) => d.id === edge.targetDocId)!
              const a = nodeCenter(src)
              const b = nodeCenter(tgt)
              const isActive =
                edge.sourceDocId === selectedDocId || edge.targetDocId === selectedDocId
              const color = edge.isBlocking ? '#ef4444' : '#4ade80'
              const mx = (a.x + b.x) / 2
              const path = `M ${a.x} ${a.y} C ${mx} ${a.y}, ${mx} ${b.y}, ${b.x} ${b.y}`
              return (
                <g key={edge.id} opacity={isActive || !selectedDocId ? 1 : 0.22}>
                  <path
                    d={path}
                    fill="none"
                    stroke={color}
                    strokeWidth={hoverEdge === edge.id ? 2.4 : 1.6}
                    strokeDasharray={edge.isBlocking ? '5 4' : undefined}
                    markerEnd={edge.isBlocking ? 'url(#arrow-red)' : 'url(#arrow-green)'}
                    className="cursor-pointer"
                    pointerEvents="stroke"
                    onClick={() => {
                      setHoverEdge(edge.id)
                      setNotice(
                        `${src.title} ${labels.dependency(edge.type)} ${tgt.title}${
                          edge.isBlocking ? ` · ${t('graph.legend.blocking')}` : ''
                        }`,
                      )
                    }}
                  />
                  {isActive && (
                    <text
                      x={mx}
                      y={(a.y + b.y) / 2 - 6}
                      fill={color}
                      fontSize="10"
                      textAnchor="middle"
                      className="font-medium"
                    >
                      {labels.dependency(edge.type)}
                    </text>
                  )}
                </g>
              )
            })}
          </svg>

          {/* Nodes */}
          {documents.map((doc) => {
            const isSel = doc.id === selectedDocId
            return (
              <div
                key={doc.id}
                role="button"
                tabIndex={0}
                onClick={() => {
                  const pendingConnection = connectFromRef.current
                  if (suppressNodeClickRef.current && !pendingConnection) {
                    suppressNodeClickRef.current = false
                    return
                  }
                  suppressNodeClickRef.current = false
                  selectNode(doc)
                }}
                onDoubleClick={() => !connectFrom && openDoc(doc.id)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' || event.key === ' ') selectNode(doc)
                }}
                onPointerDown={(event) => beginDocumentDrag(event, doc)}
                onPointerMove={(event) => moveDocumentWithPointer(event, doc.id)}
                onPointerUp={(event) => finishDocumentDrag(event, doc.id)}
                onPointerCancel={(event) => finishDocumentDrag(event, doc.id)}
                onLostPointerCapture={(event) => finishDocumentDrag(event, doc.id)}
                onDragOver={(event) => {
                  const hasDocPayload = Array.from(event.dataTransfer.types).includes(
                    'application/worksflow-doc',
                  )
                  const pendingConnection = connectFromRef.current
                  if ((pendingConnection || hasDocPayload) && pendingConnection !== doc.id) {
                    event.preventDefault()
                  }
                }}
                onDrop={(event) => {
                  event.preventDefault()
                  const sourceDocId =
                    event.dataTransfer.getData('application/worksflow-doc') ||
                    connectFromRef.current
                  if (sourceDocId) createBinding(sourceDocId, doc.id)
                }}
                className={cn(
                  'group absolute touch-none select-none rounded-lg border bg-surface-2 p-3 text-left transition-colors outline-none',
                  draggingDocId === doc.id ? 'cursor-grabbing' : 'cursor-grab',
                  isSel
                    ? 'border-primary shadow-[0_0_0_1px_var(--color-primary),0_8px_30px_rgba(20,136,252,0.2)]'
                    : connectFrom
                      ? 'border-border hover:border-primary/60'
                      : 'border-border hover:border-white/20',
                )}
                style={{
                  left: doc.position.x + OFFSET_X,
                  top: doc.position.y + OFFSET_Y,
                  width: NODE_W,
                  height: NODE_H,
                }}
              >
                <div className="flex items-center justify-between">
                  <span className="flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-wide text-faint-foreground">
                    <span className={cn('size-1.5 rounded-full', statusDot(doc.status))} />
                    {labels.docType(doc.type)}
                    <Move className="size-3 opacity-0 transition-opacity group-hover:opacity-100" />
                  </span>
                  {doc.blocking > 0 && (
                    <span className="inline-flex items-center gap-0.5 rounded bg-red-500/10 px-1 text-[10px] font-medium text-destructive">
                      <TriangleAlert className="size-2.5" />
                      {doc.blocking}
                    </span>
                  )}
                </div>
                <div className="mt-1 truncate text-sm font-semibold text-foreground">
                  {doc.title}
                </div>
                <div className="mt-1 line-clamp-1 text-[11px] text-muted-foreground">
                  {doc.summary}
                </div>
                <div className="mt-2 flex items-center justify-between">
                  <StatusPill
                    label={labels.docStatus(doc.status)}
                    className={DOC_STATUS_CLASS[doc.status]}
                  />
                  <button
                    type="button"
                    data-doc-connect-handle
                    draggable
                    onPointerDown={(event) => {
                      event.stopPropagation()
                      suppressNodeClickRef.current = false
                      setPendingConnection(doc.id)
                      setNotice(t('graph.dragCreateBinding'))
                    }}
                    onClick={(event) => {
                      event.stopPropagation()
                      suppressNodeClickRef.current = false
                      setPendingConnection(doc.id)
                      setNotice(t('graph.dragCreateBinding'))
                    }}
                    onDragStart={(event) => {
                      event.stopPropagation()
                      suppressNodeClickRef.current = false
                      event.dataTransfer.setData('application/worksflow-doc', doc.id)
                      event.dataTransfer.effectAllowed = 'link'
                      setPendingConnection(doc.id)
                      setNotice(t('graph.dragCreateBinding'))
                    }}
                    onDragEnd={() => {
                      if (connectFromRef.current) setPendingConnection(null)
                    }}
                    className="flex h-7 min-w-9 items-center justify-center gap-1 rounded-md px-2 text-[10px] text-faint-foreground hover:bg-white/5 hover:text-primary-bright"
                    aria-label={`${t('graph.addBinding')}: ${doc.title}`}
                    title={t('graph.dragCreateBinding')}
                  >
                    <Link2 className="size-3" />
                    {doc.bindings}
                  </button>
                </div>
              </div>
            )
          })}
        </div>
        )}
      </div>

      {/* Inspector */}
      <aside className="flex w-80 shrink-0 flex-col border-l border-border bg-surface max-lg:w-full max-lg:max-h-[420px] max-lg:border-l-0 max-lg:border-t">
        {selected ? (
          <>
            <div className="border-b border-border p-4">
              <div className="flex items-center gap-1.5 text-[11px] uppercase tracking-wide text-faint-foreground">
                <Layers className="size-3" />
                {labels.docType(selected.type)}
              </div>
              <h3 className="mt-1 text-base font-semibold text-foreground">{selected.title}</h3>
              <div className="mt-2 flex items-center gap-2">
                <StatusPill
                  label={labels.docStatus(selected.status)}
                  className={DOC_STATUS_CLASS[selected.status]}
                />
                <span className="text-[11px] text-faint-foreground">
                  {t('graph.updatedAt', { time: selected.updatedAt })}
                </span>
              </div>
              <p className="mt-3 text-xs leading-relaxed text-muted-foreground">
                {selected.summary}
              </p>
              {notice && (
                <div className="mt-3 rounded-md border border-primary/30 bg-primary/10 px-2.5 py-2 text-[11px] text-primary-bright">
                  {notice}
                </div>
              )}
              <button
                onClick={() => openDoc(selected.id)}
                className="mt-3 inline-flex w-full items-center justify-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-white hover:bg-primary/90"
              >
                <PenLine className="size-3.5" /> {t('graph.openDoc')}
              </button>
              <div className="mt-2 grid grid-cols-2 gap-2">
                <button
                  onClick={() => useDocInWorkbench(selected.id)}
                  className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-primary/40 bg-primary/10 px-2 py-2 text-xs font-medium text-primary-bright hover:bg-primary/20"
                >
                  <Workflow className="size-3.5" /> {t('graph.useContext')}
                </button>
                <button
                  onClick={() => {
                    setPendingConnection(selected.id)
                    setNotice(t('graph.addBinding'))
                  }}
                  className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-border px-2 py-2 text-xs font-medium text-muted-foreground hover:bg-white/5"
                >
                  <Link2 className="size-3.5" /> {t('graph.addBinding')}
                </button>
              </div>
            </div>

            <div className="flex-1 overflow-y-auto scrollbar-thin p-4">
              <SectionLabel>{t('graph.freeBinding')}</SectionLabel>
              <div className="mt-2 space-y-2 rounded-lg border border-border bg-surface-2 p-2">
                <label className="block text-[11px] text-faint-foreground">
                  {t('graph.relationType')}
                  <select
                    value={relationType}
                    onChange={(e) => setRelationType(e.target.value as DependencyType)}
                    className="mt-1 w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-foreground outline-none focus:border-primary/60"
                  >
                    {RELATION_TYPES.map((type) => (
                      <option key={type} value={type}>
                        {labels.dependency(type)}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="flex items-center gap-2 text-[11px] text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={blocking}
                    onChange={(e) => setBlocking(e.target.checked)}
                  />
                  {t('graph.blockingNotify')}
                </label>
                <label className="flex items-center gap-2 text-[11px] text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={requiredForReview}
                    onChange={(e) => setRequiredForReview(e.target.checked)}
                  />
                  {t('graph.requiredForReview')}
                </label>
                <label className="flex items-center gap-2 text-[11px] text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={notifyOnChange}
                    onChange={(e) => setNotifyOnChange(e.target.checked)}
                  />
                  {t('graph.notifyOnChange')}
                </label>
                {connectFrom && (
                  <div className="rounded-md bg-primary/10 px-2 py-1.5 text-[11px] text-primary-bright">
                    {t('graph.bindingFrom', {
                      source: documents.find((d) => d.id === connectFrom)?.title ?? t('common.source'),
                    })}
                  </div>
                )}
              </div>

              <SectionLabel className="mt-5">{t('graph.bindingObjects')}</SectionLabel>
              <div className="mt-2 space-y-2 rounded-lg border border-border bg-surface-2 p-2">
                <label className="block text-[11px] text-faint-foreground">
                  {t('graph.bindingTarget')}
                  <select
                    value={targetValue}
                    onChange={(event) => setTargetValue(event.target.value)}
                    className="mt-1 w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-foreground outline-none focus:border-primary/60"
                  >
                    <option value="">{t('graph.chooseTarget')}</option>
                    {bindingOptions.map((option) => (
                      <option key={optionKey(option)} value={optionKey(option)}>
                        {bindingKindLabel(option.kind, t)} · {option.label}
                      </option>
                    ))}
                  </select>
                </label>
                {targetKind === 'member' && (
                  <label className="block text-[11px] text-faint-foreground">
                    {t('graph.memberRole')}
                    <select
                      value={memberRole}
                      onChange={(event) => setMemberRole(event.target.value as DocMemberRole)}
                      className="mt-1 w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-foreground outline-none focus:border-primary/60"
                    >
                      {MEMBER_ROLES.map((role) => (
                        <option key={role} value={role}>
                          {labels.role(role)}
                        </option>
                      ))}
                    </select>
                  </label>
                )}
                <button
                  type="button"
                  onClick={addSelectedTargetBinding}
                  disabled={!selectedTarget}
                  className="w-full rounded-md bg-primary px-2.5 py-1.5 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:cursor-not-allowed disabled:bg-white/10 disabled:text-faint-foreground"
                >
                  {t('graph.addSelectedBinding')}
                </button>
                <div className="space-y-1.5">
                  {relatedBindings.map((binding) => (
                    <BindingRow
                      key={binding.id}
                      label={bindingKindLabel(binding.targetKind, t)}
                      value={binding.label}
                      meta={[
                        bindingRelationLabel(binding.relation, labels),
                        binding.isBlocking ? t('graph.meta.blocking') : null,
                        binding.requiredForReview ? t('graph.meta.required') : null,
                        binding.notifyOnChange ? t('graph.meta.notify') : null,
                      ]
                        .filter(Boolean)
                        .join(' · ')}
                    />
                  ))}
                </div>
              </div>

              <SectionLabel className="mt-5">{t('graph.collaborators')}</SectionLabel>
              <div className="mt-2 space-y-2">
                {selected.members.map((m) => {
                  const mem = memberById(m.userId)
                  if (!mem) return null
                  return (
                    <div key={m.userId} className="flex items-center gap-2">
                      <Avatar member={mem} size={26} />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-xs font-medium text-foreground">
                          {mem.name}
                        </div>
                        <div className="text-[10px] text-faint-foreground">{mem.title}</div>
                      </div>
                      <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground">
                        {labels.role(m.role)}
                      </span>
                    </div>
                  )
                })}
              </div>

              <SectionLabel className="mt-5">{t('graph.dependencies')}</SectionLabel>
              <div className="mt-2 space-y-1.5">
                {relatedEdges.map((e) => {
                  const other =
                    e.sourceDocId === selected.id
                      ? documents.find((d) => d.id === e.targetDocId)
                      : documents.find((d) => d.id === e.sourceDocId)
                  const outgoing = e.sourceDocId === selected.id
                  return (
                    <button
                      key={e.id}
                      onClick={() => other && setSelectedDocId(other.id)}
                      onMouseEnter={() => setHoverEdge(e.id)}
                      onMouseLeave={() => setHoverEdge(null)}
                      className="flex w-full items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-left hover:border-white/20"
                    >
                      <GitFork
                        className={cn(
                          'size-3.5 shrink-0',
                          e.isBlocking ? 'text-destructive' : 'text-success',
                        )}
                      />
                      <span className="text-[11px] text-faint-foreground">
                        {outgoing ? '' : '← '}
                        {labels.dependency(e.type)}
                      </span>
                      <span className="ml-auto flex items-center gap-1 truncate text-xs font-medium text-foreground">
                        {outgoing && <ArrowRight className="size-3 text-faint-foreground" />}
                        {other?.title}
                      </span>
                    </button>
                  )
                })}
                {relatedEdges.length === 0 && (
                  <p className="text-xs text-faint-foreground">{t('graph.noDependencies')}</p>
                )}
              </div>

              {selected.blocking > 0 && (
                <div className="mt-5 rounded-lg border border-red-500/30 bg-red-500/5 p-3">
                  <div className="flex items-center gap-1.5 text-xs font-medium text-destructive">
                    <TriangleAlert className="size-3.5" />
                    {t('graph.blocksDownstream', { count: selected.blocking })}
                  </div>
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    {t('graph.blocksCopy')}
                  </p>
                </div>
              )}

              <button
                onClick={() => {
                  setPendingConnection(selected.id)
                  setRelationType('generates')
                  setNotice(t('graph.chooseDownstream'))
                }}
                className="mt-5 inline-flex w-full items-center justify-center gap-1.5 rounded-lg border border-dashed border-border px-3 py-2 text-xs font-medium text-muted-foreground hover:border-primary/40 hover:text-foreground"
              >
                <GitFork className="size-3.5 text-primary-bright" /> {t('graph.generateDownstream')}
              </button>
            </div>
          </>
        ) : (
          <div className="flex flex-1 items-center justify-center p-6 text-center text-sm text-faint-foreground">
            {t('graph.emptySelection')}
          </div>
        )}
      </aside>
    </div>
  )
}

function Legend({ color, label, dashed }: { color: string; label: string; dashed?: boolean }) {
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5">
      <span
        className="inline-block h-0 w-5 border-t-2"
        style={{ borderColor: color, borderStyle: dashed ? 'dashed' : 'solid' }}
      />
      {label}
    </span>
  )
}

function SectionLabel({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        'text-[11px] font-semibold uppercase tracking-wider text-faint-foreground',
        className,
      )}
    >
      {children}
    </div>
  )
}

function nodeTargetKind(type: BlueprintNodeType): BindingTargetKind {
  if (type === 'workbenchTarget') return 'workbenchVersion'
  return type
}

function buildBindingOptions(
  documents: TeamDocument[],
  blueprint: Blueprint,
  importAssets: ImportAsset[],
  labels: LocalizedLabels,
  t: (key: MessageKey) => string,
): BindingOption[] {
  return [
    {
      kind: 'blueprint',
      id: blueprint.id,
      label: blueprint.title,
      meta: bindingKindLabel('blueprint', t),
    },
    ...documents.map((doc) => ({
      kind: 'document' as const,
      id: doc.id,
      label: doc.title,
      meta: labels.docType(doc.type),
    })),
    ...MEMBERS.map((member) => ({
      kind: 'member' as const,
      id: member.id,
      label: member.name,
      meta: member.title,
    })),
    ...blueprint.nodes.map((node) => ({
      kind: nodeTargetKind(node.type),
      id: node.id,
      label: node.title,
      meta: bindingKindLabel(nodeTargetKind(node.type), t),
    })),
    ...importAssets.map((asset) => ({
      kind: 'externalAsset' as const,
      id: asset.id,
      label: asset.name,
      meta: bindingKindLabel('externalAsset', t),
    })),
    ...VERSIONS.map((version) => ({
      kind: 'workbenchVersion' as const,
      id: version.id,
      label: version.title,
      meta: version.subtitle,
    })),
  ]
}

function optionKey(option: BindingOption) {
  return `${option.kind}:${option.id}`
}

function bindingKindLabel(kind: BindingTargetKind, t: (key: MessageKey) => string) {
  const map: Record<BindingTargetKind, MessageKey> = {
    document: 'graph.bindingKind.document',
    member: 'graph.bindingKind.member',
    blueprint: 'graph.bindingKind.blueprint',
    feature: 'graph.bindingKind.feature',
    page: 'graph.bindingKind.page',
    component: 'graph.bindingKind.component',
    api: 'graph.bindingKind.api',
    dataModel: 'graph.bindingKind.dataModel',
    permission: 'graph.bindingKind.permission',
    prototype: 'graph.bindingKind.prototype',
    workbenchVersion: 'graph.bindingKind.workbenchVersion',
    externalAsset: 'graph.bindingKind.externalAsset',
  }
  return t(map[kind])
}

function BindingRow({ label, value, meta }: { label: string; value: string; meta?: string }) {
  return (
    <div className="rounded-lg border border-border bg-background px-2.5 py-2">
      <div className="flex items-center justify-between gap-2">
      <span className="text-[10px] font-medium uppercase tracking-wide text-faint-foreground">
        {label}
      </span>
      <span className="min-w-0 truncate text-xs text-muted-foreground">{value}</span>
      </div>
      {meta && <div className="mt-1 truncate text-[10px] text-faint-foreground">{meta}</div>}
    </div>
  )
}
