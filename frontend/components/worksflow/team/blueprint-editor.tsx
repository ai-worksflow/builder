'use client'

import { useRef, useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import { MEMBERS, MODULE_LIBRARY } from '@/lib/worksflow/mock-data'
import { BLUEPRINT_NODE_COLOR } from '@/lib/worksflow/labels'
import type { BlueprintEdgeType, BlueprintNode, BlueprintNodeType } from '@/lib/worksflow/types'
import { useLocalizedLabels } from '../use-localized-labels'
import { Avatar, StatusPill, memberById } from '../shared'
import {
  ArrowRight,
  Boxes,
  ClipboardCheck,
  Download,
  FileText,
  Link2,
  Move,
  Plus,
  Save,
  Search,
  Sparkles,
  Trash2,
  TriangleAlert,
  Workflow,
  Zap,
} from 'lucide-react'

const NODE_W = 168
const NODE_H = 62
const PAD_X = 24
const PAD_Y = 24
const CANVAS_W = 1220
const CANVAS_H = 560

const EDGE_TYPES: BlueprintEdgeType[] = [
  'contains',
  'uses',
  'calls',
  'reads',
  'writes',
  'requires',
  'renders',
  'syncs_with',
  'generates',
  'implemented_by',
]

const NODE_TYPES: BlueprintNodeType[] = [
  'feature',
  'page',
  'component',
  'api',
  'dataModel',
  'permission',
  'prototype',
  'workbenchTarget',
]

function uniqueIds(ids: string[]) {
  return Array.from(new Set(ids))
}

export function BlueprintEditor() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const {
    activeTeamProject,
    blueprint,
    blueprintOperations,
    activeBlueprintContext,
    selectedBlueprintNodeId,
    setSelectedBlueprintNodeId,
    documents,
    importAssets,
    createBlueprintNode,
    updateBlueprintNode,
    moveBlueprintNode,
    deleteBlueprintNode,
    createBlueprintEdge,
    updateBlueprintEdge,
    deleteBlueprintEdge,
    saveBlueprint,
    validateBlueprint,
    completeBlueprintNode,
    startBlankBlueprint,
    generateBlueprintFromProjectBrief,
    generateBlueprintFromExistingDocs,
    generateDocsFromBlueprintSelection,
    createWorkbenchContextFromBlueprint,
    openDoc,
  } = useWorksflow()
  const [query, setQuery] = useState('')
  const [brief, setBrief] = useState('')
  const [edgeType, setEdgeType] = useState<BlueprintEdgeType>('contains')
  const [connectFrom, setConnectFrom] = useState<string | null>(null)
  const connectFromRef = useRef<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [docToBind, setDocToBind] = useState('d3')
  const [memberToBind, setMemberToBind] = useState('m1')
  const [prototypeToBind, setPrototypeToBind] = useState(importAssets[0]?.id ?? '')
  const [edgeTargetId, setEdgeTargetId] = useState('b2')
  const [draggingNodeId, setDraggingNodeId] = useState<string | null>(null)
  const dragSessionRef = useRef<{
    id: string
    pointerId: number
    offsetX: number
    offsetY: number
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
  const canvasRef = useRef<HTMLDivElement | null>(null)

  const selected = blueprint.nodes.find((node) => node.id === selectedBlueprintNodeId) ?? null
  const missingCount = blueprint.nodes.reduce((sum, node) => sum + (node.missing?.length ?? 0), 0)
  const generatedDocs = documents.filter((doc) => blueprint.generatedDocIds.includes(doc.id))

  const center = (node: BlueprintNode) => ({
    x: node.position.x + PAD_X + NODE_W / 2,
    y: node.position.y + PAD_Y + NODE_H / 2,
  })

  function addModuleToCanvas(title: string, group: string, x = 120, y = 120) {
    const type = nodeTypeForModule(group, title)
    const id = createBlueprintNode(type, title, { x, y })
    setSelectedBlueprintNodeId(id)
    setNotice(t('blueprint.nodeTypedAdded', { title, type: labels.blueprintNode(type) }))
  }

  function setPendingConnection(id: string | null) {
    connectFromRef.current = id
    setConnectFrom(id)
  }

  function beginNodeDrag(event: React.PointerEvent<HTMLDivElement>, node: BlueprintNode) {
    if ((event.target as HTMLElement).closest('[data-edge-handle]')) return
    if (event.button !== 0) return
    event.preventDefault()
    event.currentTarget.setPointerCapture(event.pointerId)
    lockDragScroll()
    const rect = event.currentTarget.getBoundingClientRect()
    dragSessionRef.current = {
      id: node.id,
      pointerId: event.pointerId,
      offsetX: event.clientX - rect.left,
      offsetY: event.clientY - rect.top,
      lastX: node.position.x,
      lastY: node.position.y,
      moved: false,
    }
    setDraggingNodeId(node.id)
    setSelectedBlueprintNodeId(node.id)
  }

  function moveNodeWithPointer(event: React.PointerEvent<HTMLDivElement>, nodeId: string) {
    const session = dragSessionRef.current
    const canvas = canvasRef.current
    if (!session || session.id !== nodeId || !canvas) return
    event.preventDefault()
    const canvasRect = canvas.getBoundingClientRect()
    const x = Math.max(
      0,
      Math.min(
        CANVAS_W - NODE_W - PAD_X * 2,
        event.clientX - canvasRect.left + canvas.scrollLeft - session.offsetX - PAD_X,
      ),
    )
    const y = Math.max(
      0,
      Math.min(
        CANVAS_H - NODE_H - PAD_Y * 2,
        event.clientY - canvasRect.top + canvas.scrollTop - session.offsetY - PAD_Y,
      ),
    )
    if (Math.abs(session.lastX - x) < 0.5 && Math.abs(session.lastY - y) < 0.5) return
    session.lastX = x
    session.lastY = y
    session.moved = true
    moveBlueprintNode(session.id, { x, y })
  }

  function finishNodeDrag(event: React.PointerEvent<HTMLDivElement>, nodeId: string) {
    const session = dragSessionRef.current
    if (!session || session.id !== nodeId) return
    if (event.currentTarget.hasPointerCapture(session.pointerId)) {
      event.currentTarget.releasePointerCapture(session.pointerId)
    }
    if (session.moved) {
      suppressNodeClickRef.current = true
      setNotice(t('blueprint.nodePositionUpdated'))
    }
    dragSessionRef.current = null
    setDraggingNodeId(null)
    unlockDragScroll()
  }

  function lockDragScroll() {
    unlockDragScroll()
    const locks: typeof scrollLockRef.current = []
    let element = canvasRef.current?.parentElement ?? null
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

  function bindDocument() {
    if (!selected || !docToBind) return
    updateBlueprintNode(selected.id, {
      boundDocumentIds: uniqueIds([...selected.boundDocumentIds, docToBind]),
    })
    setNotice(t('blueprint.documentBound', { title: selected.title }))
  }

  function bindMember() {
    if (!selected || !memberToBind) return
    updateBlueprintNode(selected.id, {
      boundMemberIds: uniqueIds([...selected.boundMemberIds, memberToBind]),
      missing: (selected.missing ?? []).filter((item) => item !== 'No owner assigned'),
    })
    setNotice(t('blueprint.memberBound', { title: selected.title }))
  }

  function bindPrototype() {
    if (!selected || !prototypeToBind) return
    updateBlueprintNode(selected.id, {
      boundPrototypeArtifactIds: uniqueIds([
        ...selected.boundPrototypeArtifactIds,
        prototypeToBind,
      ]),
      missing: (selected.missing ?? []).filter((item) => item !== 'No imported prototype source'),
    })
    setNotice(t('blueprint.prototypeBound', { title: selected.title }))
  }

  function connectSelectedToTarget() {
    if (!selected) return
    const fallbackTarget = blueprint.nodes.find((node) => node.id !== selected.id)
    const targetId = edgeTargetId !== selected.id ? edgeTargetId : fallbackTarget?.id
    if (!targetId) return
    createBlueprintEdge(selected.id, targetId, edgeType)
    setNotice(t('blueprint.edgeCreated', { type: labels.blueprintEdge(edgeType) }))
  }

  function generateDocs() {
    const ids = generateDocsFromBlueprintSelection(selected?.id)
    setNotice(t('blueprint.generatedOutputs', { count: ids.length }))
  }

  return (
    <div className="flex h-full max-lg:flex-col max-lg:overflow-y-auto">
      <aside className="flex w-60 shrink-0 flex-col border-r border-border bg-surface max-lg:h-56 max-lg:w-full max-lg:border-b max-lg:border-r-0">
        <div className="border-b border-border p-3">
          <div className="flex items-center gap-1.5 text-xs font-semibold text-foreground">
            <Boxes className="size-4 text-primary-bright" /> {t('blueprint.library')}
          </div>
          <div className="mt-2 flex items-center gap-1.5 rounded-lg border border-border bg-surface-2 px-2 py-1.5">
            <Search className="size-3.5 text-faint-foreground" />
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t('blueprint.searchModules')}
              className="w-full bg-transparent text-xs text-foreground outline-none placeholder:text-faint-foreground"
            />
          </div>
        </div>
        <div className="flex-1 overflow-y-auto scrollbar-thin p-3">
          {MODULE_LIBRARY.map((group) => {
            const items = group.items.filter((item) =>
              item.toLowerCase().includes(query.toLowerCase()),
            )
            if (items.length === 0) return null
            return (
              <div key={group.group} className="mb-4">
                <div className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-faint-foreground">
                  {group.group}
                </div>
                <div className="space-y-1">
                  {items.map((item) => (
                    <div
                      key={item}
                      draggable
                      onDragStart={(event) => {
                        event.dataTransfer.setData(
                          'application/worksflow-module',
                          `${group.group}::${item}`,
                        )
                      }}
                      onClick={() => addModuleToCanvas(item, group.group)}
                      className="group flex cursor-grab items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-muted-foreground hover:border-primary/40 hover:text-foreground"
                    >
                      <Plus className="size-3 text-faint-foreground group-hover:text-primary-bright" />
                      {item}
                    </div>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
      </aside>

      <div className="relative flex min-w-0 flex-1 flex-col bg-canvas max-lg:flex-none">
        <div className="flex items-center justify-between gap-3 border-b border-border bg-surface/80 px-5 py-3 backdrop-blur max-md:flex-wrap max-md:px-4">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <Workflow className="size-4 text-primary-bright" />
            <span className="truncate text-sm font-semibold text-foreground">{blueprint.title}</span>
            <span className="text-xs text-faint-foreground">{activeTeamProject.name}</span>
            <StatusPill
              label={blueprint.status}
              className="border-primary/30 bg-primary/10 text-primary-bright"
            />
            <span className="text-xs text-faint-foreground">
              v{blueprint.version} · {blueprint.updatedAt}
            </span>
            {missingCount > 0 && (
              <span className="inline-flex items-center gap-1 rounded-md bg-amber-400/10 px-2 py-1 text-[11px] font-medium text-warning">
                <TriangleAlert className="size-3" /> {t('blueprint.pendingIssues', { count: missingCount })}
              </span>
            )}
          </div>
          <div className="flex max-w-full items-center gap-2 overflow-x-auto scrollbar-thin">
            <select
              value={edgeType}
              onChange={(event) => setEdgeType(event.target.value as BlueprintEdgeType)}
              aria-label={t('blueprint.edgeType')}
              className="rounded-lg border border-border bg-surface-2 px-2 py-1.5 text-xs text-muted-foreground outline-none focus:border-primary/60"
            >
              {EDGE_TYPES.map((type) => (
                <option key={type} value={type}>
                  {labels.blueprintEdge(type)}
                </option>
              ))}
            </select>
            <ActionButton
              icon={Link2}
              onClick={() => {
                if (!selected) return
                setPendingConnection(selected.id)
                setNotice(t('blueprint.chooseTargetNode', { source: selected.title }))
              }}
            >
              {t('blueprint.connectSelected')}
            </ActionButton>
            <ActionButton
              icon={Boxes}
              onClick={() => {
                if (!selected) return
                const id = createBlueprintNode('feature', `Capability: ${selected.title}`, {
                  x: Math.max(20, selected.position.x - 180),
                  y: selected.position.y + 100,
                })
                createBlueprintEdge(id, selected.id, 'contains', true)
                setNotice(t('blueprint.capabilityCreated', { title: selected.title, source: selected.title }))
              }}
            >
              {t('blueprint.groupAsCapability')}
            </ActionButton>
            <ActionButton
              icon={Save}
              onClick={() => {
                saveBlueprint()
                setNotice(t('blueprint.saveSnapshot'))
              }}
            >
              {t('blueprint.save')}
            </ActionButton>
            <ActionButton
              icon={ClipboardCheck}
              onClick={() => {
                validateBlueprint()
                setNotice(t('blueprint.validationCompleted'))
              }}
            >
              {t('blueprint.validate')}
            </ActionButton>
            <ActionButton icon={FileText} onClick={generateDocs}>
              {t('blueprint.generateDocs')}
            </ActionButton>
            <ActionButton
              icon={Sparkles}
              onClick={() => {
                generateDocs()
                setNotice(t('blueprint.prototypeBriefGenerated'))
              }}
            >
              {t('blueprint.generatePrototypeBrief')}
            </ActionButton>
            <button
              type="button"
              onClick={() => createWorkbenchContextFromBlueprint(selected?.id)}
              className="inline-flex shrink-0 items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-white hover:bg-primary/90"
            >
              <Zap className="size-3.5" /> {t('blueprint.useInWorkbench')}
            </button>
            <ActionButton
              icon={Download}
              onClick={() =>
                setNotice(
                  t('blueprint.exportReady', {
                    nodes: blueprint.nodes.length,
                    edges: blueprint.edges.length,
                    docs: generatedDocs.length,
                  }),
                )
              }
            >
              {t('blueprint.export')}
            </ActionButton>
          </div>
        </div>

        {notice && (
          <div className="border-b border-border bg-primary/10 px-5 py-2 text-xs text-primary-bright">
            {notice}
          </div>
        )}

        <div
          ref={canvasRef}
          className="flex-1 overflow-auto scrollbar-thin max-lg:min-h-[560px] max-lg:flex-none"
          onDragOver={(event) => event.preventDefault()}
          onDrop={(event) => {
            event.preventDefault()
            const modulePayload = event.dataTransfer.getData('application/worksflow-module')
            if (modulePayload) {
              const [group, item] = modulePayload.split('::')
              const rect = event.currentTarget.getBoundingClientRect()
              addModuleToCanvas(
                item,
                group,
                event.clientX - rect.left - PAD_X,
                event.clientY - rect.top - PAD_Y,
              )
            }
          }}
        >
          <div className="relative" style={{ width: CANVAS_W, height: CANVAS_H }}>
            {blueprint.nodes.length === 0 && (
              <div className="absolute inset-0 flex items-center justify-center p-6">
                <div className="w-full max-w-xl rounded-lg border border-dashed border-border bg-panel p-5 text-center">
                  <Workflow className="mx-auto size-8 text-primary-bright" />
                  <h2 className="mt-3 text-base font-semibold text-foreground">
                    {t('blueprint.emptyTitle')}
                  </h2>
                  <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                    {t('blueprint.emptyBody')}
                  </p>
                  <textarea
                    value={brief}
                    onChange={(event) => setBrief(event.target.value)}
                    placeholder={t('blueprint.briefPlaceholder')}
                    rows={3}
                    className="mt-4 w-full resize-none rounded-lg border border-border bg-background px-3 py-2 text-left text-xs text-muted-foreground outline-none placeholder:text-faint-foreground focus:border-primary/60"
                  />
                  <div className="mt-4 flex flex-wrap justify-center gap-2">
                    <button
                      type="button"
                      onClick={() => generateBlueprintFromProjectBrief(brief)}
                      className="rounded-lg bg-primary px-3 py-2 text-xs font-semibold text-primary-foreground hover:bg-primary-bright"
                    >
                      {t('blueprint.generateFromBrief')}
                    </button>
                    <button
                      type="button"
                      onClick={generateBlueprintFromExistingDocs}
                      className="rounded-lg border border-border px-3 py-2 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
                    >
                      {t('blueprint.generateFromDocs')}
                    </button>
                    <button
                      type="button"
                      onClick={startBlankBlueprint}
                      className="rounded-lg border border-border px-3 py-2 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
                    >
                      {t('blueprint.startBlank')}
                    </button>
                  </div>
                  <p className="mt-3 text-[11px] leading-relaxed text-faint-foreground">
                    {t('blueprint.manualHint')}
                  </p>
                </div>
              </div>
            )}
            <svg className="pointer-events-none absolute inset-0" width={CANVAS_W} height={CANVAS_H}>
              <defs>
                <marker id="bp-arrow" markerWidth="7" markerHeight="7" refX="6" refY="3.5" orient="auto">
                  <path d="M0,0 L7,3.5 L0,7 z" fill="#5a5a66" />
                </marker>
              </defs>
              {blueprint.edges.map((edge) => {
                const source = blueprint.nodes.find((node) => node.id === edge.sourceNodeId)
                const target = blueprint.nodes.find((node) => node.id === edge.targetNodeId)
                if (!source || !target) return null
                const a = center(source)
                const b = center(target)
                const active =
                  edge.sourceNodeId === selectedBlueprintNodeId ||
                  edge.targetNodeId === selectedBlueprintNodeId
                const mx = (a.x + b.x) / 2
                return (
                  <g key={edge.id} opacity={active || !selectedBlueprintNodeId ? 0.9 : 0.2}>
                    <path
                      d={`M ${a.x} ${a.y} C ${mx} ${a.y}, ${mx} ${b.y}, ${b.x} ${b.y}`}
                      fill="none"
                      stroke={active ? '#1488fc' : '#5a5a66'}
                      strokeDasharray={edge.isRequired ? undefined : '5 4'}
                      strokeWidth={active ? 2 : 1.4}
                      markerEnd="url(#bp-arrow)"
                    />
                    {active && (
                      <text
                        x={mx}
                        y={(a.y + b.y) / 2 - 5}
                        fill="#2ba6ff"
                        fontSize="9.5"
                        textAnchor="middle"
                        className="font-medium"
                      >
                        {labels.blueprintEdge(edge.type)}
                      </text>
                    )}
                  </g>
                )
              })}
            </svg>

            {blueprint.nodes.map((node) => {
              const color = BLUEPRINT_NODE_COLOR[node.type]
              const isSelected = node.id === selectedBlueprintNodeId
              const hasMissing = !!node.missing?.length
              const hasDocs = node.boundDocumentIds.length > 0 || node.generatedDocIds.length > 0
              return (
                <div
                  key={node.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => {
                    if (suppressNodeClickRef.current) {
                      suppressNodeClickRef.current = false
                      return
                    }
                    const pendingConnection = connectFromRef.current
                    if (pendingConnection && pendingConnection !== node.id) {
                      createBlueprintEdge(pendingConnection, node.id, edgeType)
                      setPendingConnection(null)
                      setNotice(t('blueprint.edgeCreated', { type: labels.blueprintEdge(edgeType) }))
                    } else {
                      setSelectedBlueprintNodeId(node.id)
                    }
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter' || event.key === ' ') setSelectedBlueprintNodeId(node.id)
                  }}
                  onPointerDown={(event) => beginNodeDrag(event, node)}
                  onPointerMove={(event) => moveNodeWithPointer(event, node.id)}
                  onPointerUp={(event) => finishNodeDrag(event, node.id)}
                  onPointerCancel={(event) => finishNodeDrag(event, node.id)}
                  onLostPointerCapture={(event) => finishNodeDrag(event, node.id)}
                  onDragOver={(event) => {
                    if (connectFrom && connectFrom !== node.id) event.preventDefault()
                  }}
                  onDrop={(event) => {
                    event.preventDefault()
                    const sourceNodeId =
                      event.dataTransfer.getData('application/worksflow-blueprint-node') ||
                      connectFromRef.current
                    if (sourceNodeId && sourceNodeId !== node.id) {
                      createBlueprintEdge(sourceNodeId, node.id, edgeType)
                      setPendingConnection(null)
                      setNotice(t('blueprint.edgeCreated', { type: labels.blueprintEdge(edgeType) }))
                    }
                  }}
                  className={cn(
                    'absolute flex touch-none select-none flex-col justify-center rounded-lg border bg-surface-2 px-3 text-left transition-colors outline-none',
                    draggingNodeId === node.id ? 'cursor-grabbing' : 'cursor-grab',
                    isSelected
                      ? 'border-primary shadow-[0_0_0_1px_var(--color-primary)]'
                      : connectFrom
                        ? 'border-border hover:border-primary/60'
                        : 'border-border hover:border-white/20',
                  )}
                  style={{
                    left: node.position.x + PAD_X,
                    top: node.position.y + PAD_Y,
                    width: NODE_W,
                    height: NODE_H,
                    borderLeftColor: color,
                    borderLeftWidth: 3,
                  }}
                >
                  <div className="flex items-center gap-1.5">
                    <span
                      className="text-[9px] font-semibold uppercase tracking-wide"
                      style={{ color }}
                    >
                      {labels.blueprintNode(node.type)}
                    </span>
                    {hasMissing && <TriangleAlert className="size-3 text-warning" />}
                    {hasDocs && <FileText className="ml-auto size-3 text-faint-foreground" />}
                    <button
                      type="button"
                      data-edge-handle
                      draggable
                      onPointerDown={(event) => {
                        event.stopPropagation()
                        setPendingConnection(node.id)
                        setNotice(t('blueprint.dragConnectionFrom', { source: node.title }))
                      }}
                      onClick={(event) => {
                        event.stopPropagation()
                        setPendingConnection(node.id)
                        setNotice(t('blueprint.dragConnectionFrom', { source: node.title }))
                      }}
                      onDragStart={(event) => {
                        event.stopPropagation()
                        event.dataTransfer.setData('application/worksflow-blueprint-node', node.id)
                        setPendingConnection(node.id)
                        setNotice(t('blueprint.dragConnectionFrom', { source: node.title }))
                      }}
                      onDragEnd={() => setPendingConnection(null)}
                      className="flex size-5 items-center justify-center rounded text-faint-foreground hover:bg-white/5 hover:text-primary-bright"
                      aria-label={t('blueprint.connectFrom', { source: node.title })}
                      title={t('blueprint.dragToConnect')}
                    >
                      <Link2 className="size-3" />
                    </button>
                  </div>
                  <div className="truncate text-xs font-semibold text-foreground">{node.title}</div>
                  <div className="mt-0.5 flex items-center gap-1 text-[9px] text-faint-foreground">
                    <Move className="size-2.5" /> {t('blueprint.dragToMove')}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      <aside className="flex w-80 shrink-0 flex-col border-l border-border bg-surface max-lg:w-full max-lg:max-h-[560px] max-lg:border-l-0 max-lg:border-t">
        {selected ? (
          <>
            <div className="border-b border-border p-4">
              <span
                className="text-[10px] font-semibold uppercase tracking-wide"
                style={{ color: BLUEPRINT_NODE_COLOR[selected.type] }}
              >
                {labels.blueprintNode(selected.type)}
              </span>
              <input
                value={selected.title}
                onChange={(event) => updateBlueprintNode(selected.id, { title: event.target.value })}
                className="mt-1 w-full rounded-md border border-border bg-background px-2 py-1.5 text-sm font-semibold text-foreground outline-none focus:border-primary/60"
              />
              <textarea
                value={selected.description ?? ''}
                onChange={(event) =>
                  updateBlueprintNode(selected.id, { description: event.target.value })
                }
                placeholder={t('blueprint.describePlaceholder')}
                rows={2}
                className="mt-2 w-full resize-none rounded-md border border-border bg-background px-2 py-1.5 text-xs text-muted-foreground outline-none placeholder:text-faint-foreground focus:border-primary/60"
              />
            </div>

            <div className="flex-1 overflow-y-auto scrollbar-thin p-4">
              <InspLabel>{t('blueprint.nodeType')}</InspLabel>
              <select
                value={selected.type}
                onChange={(event) =>
                  updateBlueprintNode(selected.id, {
                    type: event.target.value as BlueprintNodeType,
                  })
                }
                className="mt-2 w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-foreground outline-none focus:border-primary/60"
              >
                {NODE_TYPES.map((type) => (
                  <option key={type} value={type}>
                    {labels.blueprintNode(type)}
                  </option>
                ))}
              </select>

              {selected.missing?.length ? (
                <div className="mt-4 rounded-lg border border-amber-400/30 bg-amber-400/5 p-3">
                  <div className="flex items-center gap-1.5 text-xs font-medium text-warning">
                    <TriangleAlert className="size-3.5" /> {t('blueprint.missingInputs')}
                  </div>
                  <ul className="mt-1.5 space-y-1">
                    {selected.missing.map((item) => (
                      <li key={item} className="text-[11px] text-muted-foreground">
                        {item}
                      </li>
                    ))}
                  </ul>
                  <button
                    type="button"
                    onClick={() => {
                      completeBlueprintNode(selected.id)
                      setNotice(t('blueprint.aiCompletionApplied', { title: selected.title }))
                    }}
                    className="mt-2 inline-flex w-full items-center justify-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-[11px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
                  >
                    <Sparkles className="size-3.5 text-primary-bright" /> {t('blueprint.completeNode')}
                  </button>
                </div>
              ) : null}

              <InspLabel className="mt-5">{t('blueprint.inputsOutputs')}</InspLabel>
              <div className="mt-2 grid grid-cols-[1fr_auto] gap-1.5">
                <select
                  value={edgeTargetId}
                  onChange={(event) => setEdgeTargetId(event.target.value)}
                  className="min-w-0 rounded-md border border-border bg-background px-2 py-1.5 text-[11px] text-foreground outline-none focus:border-primary/60"
                >
                  {blueprint.nodes
                    .filter((node) => node.id !== selected.id)
                    .map((node) => (
                      <option key={node.id} value={node.id}>
                        {node.title}
                      </option>
                    ))}
                </select>
                <button
                  type="button"
                  onClick={connectSelectedToTarget}
                  className="rounded-md bg-primary px-2 py-1.5 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright"
                >
                  Connect target
                </button>
              </div>
              <div className="mt-2 space-y-1.5">
                {blueprint.edges
                  .filter(
                    (edge) =>
                      edge.sourceNodeId === selected.id || edge.targetNodeId === selected.id,
                  )
                  .map((edge) => {
                    const outgoing = edge.sourceNodeId === selected.id
                    const other = blueprint.nodes.find(
                      (node) => node.id === (outgoing ? edge.targetNodeId : edge.sourceNodeId),
                    )
                    return (
                      <div key={edge.id} className="rounded-lg border border-border bg-surface-2 p-2">
                        <button
                          type="button"
                          onClick={() => other && setSelectedBlueprintNodeId(other.id)}
                          className="flex w-full items-center gap-2 text-left"
                        >
                          <span className="rounded bg-white/5 px-1.5 py-0.5 text-[10px] text-faint-foreground">
                            {outgoing ? t('blueprint.output') : t('blueprint.input')}
                          </span>
                          <span className="min-w-0 flex-1 truncate text-xs font-medium text-foreground">
                            {other?.title}
                          </span>
                          <ArrowRight className="size-3 text-faint-foreground" />
                        </button>
                        <div className="mt-2 grid grid-cols-[1fr_auto_auto] gap-1.5">
                          <select
                            value={edge.type}
                            onChange={(event) =>
                              updateBlueprintEdge(edge.id, {
                                type: event.target.value as BlueprintEdgeType,
                              })
                            }
                            className="min-w-0 rounded-md border border-border bg-background px-2 py-1 text-[11px] text-foreground outline-none focus:border-primary/60"
                          >
                            {EDGE_TYPES.map((type) => (
                              <option key={type} value={type}>
                                {labels.blueprintEdge(type)}
                              </option>
                            ))}
                          </select>
                          <button
                            type="button"
                            onClick={() =>
                              updateBlueprintEdge(edge.id, { isRequired: !edge.isRequired })
                            }
                            className={cn(
                              'rounded-md border px-2 py-1 text-[10px] font-medium',
                              edge.isRequired
                                ? 'border-warning/40 bg-warning/10 text-warning'
                                : 'border-border text-faint-foreground hover:bg-white/5',
                            )}
                          >
                            {t('blueprint.required')}
                          </button>
                          <button
                            type="button"
                            onClick={() => deleteBlueprintEdge(edge.id)}
                            className="rounded-md border border-border px-2 py-1 text-[10px] text-faint-foreground hover:bg-white/5 hover:text-destructive"
                            aria-label={t('blueprint.deleteEdge', { id: edge.id })}
                          >
                            <Trash2 className="size-3" />
                          </button>
                        </div>
                      </div>
                    )
                  })}
              </div>

              <InspLabel className="mt-5">{t('blueprint.boundDocs')}</InspLabel>
              <div className="mt-2 space-y-1.5">
                {uniqueIds([...selected.boundDocumentIds, ...selected.generatedDocIds]).map((docId) => {
                  const doc = documents.find((item) => item.id === docId)
                  return doc ? (
                    <button
                      key={docId}
                      type="button"
                      onClick={() => openDoc(doc.id)}
                      className="flex w-full items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-left hover:border-white/20"
                    >
                      <FileText className="size-3.5 text-primary-bright" />
                      <span className="min-w-0 flex-1 truncate text-xs font-medium text-foreground">
                        {doc.title}
                      </span>
                    </button>
                  ) : null
                })}
                <div className="grid grid-cols-[1fr_auto] gap-1.5">
                  <select
                    value={docToBind}
                    onChange={(event) => setDocToBind(event.target.value)}
                    className="min-w-0 rounded-md border border-border bg-background px-2 py-1.5 text-[11px] text-foreground outline-none focus:border-primary/60"
                  >
                    {documents.map((doc) => (
                      <option key={doc.id} value={doc.id}>
                        {doc.title}
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    onClick={bindDocument}
                    className="rounded-md bg-primary px-2 py-1.5 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright"
                  >
                    {t('blueprint.bind')}
                  </button>
                </div>
              </div>

              <InspLabel className="mt-5">{t('blueprint.members')}</InspLabel>
              <div className="mt-2 space-y-1.5">
                {selected.boundMemberIds.map((memberId) => {
                  const member = memberById(memberId)
                  return member ? (
                    <div key={memberId} className="flex items-center gap-2 rounded-md px-1 py-1">
                      <Avatar member={member} size={22} />
                      <span className="min-w-0 flex-1 truncate text-xs text-foreground">
                        {member.name}
                      </span>
                      <button
                        type="button"
                        onClick={() =>
                          updateBlueprintNode(selected.id, {
                            boundMemberIds: selected.boundMemberIds.filter((id) => id !== memberId),
                          })
                        }
                        className="rounded px-1 text-[10px] text-faint-foreground hover:bg-white/5"
                      >
                        {t('common.remove')}
                      </button>
                    </div>
                  ) : null
                })}
                <div className="grid grid-cols-[1fr_auto] gap-1.5">
                  <select
                    value={memberToBind}
                    onChange={(event) => setMemberToBind(event.target.value)}
                    className="min-w-0 rounded-md border border-border bg-background px-2 py-1.5 text-[11px] text-foreground outline-none focus:border-primary/60"
                  >
                    {MEMBERS.map((member) => (
                      <option key={member.id} value={member.id}>
                        {member.name}
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    onClick={bindMember}
                    className="rounded-md bg-primary px-2 py-1.5 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright"
                  >
                    {t('blueprint.bind')}
                  </button>
                </div>
              </div>

              <InspLabel className="mt-5">{t('blueprint.prototypeAssets')}</InspLabel>
              <div className="mt-2 space-y-1.5">
                {selected.boundPrototypeArtifactIds.map((assetId) => {
                  const asset = importAssets.find((item) => item.id === assetId)
                  return (
                    <div key={assetId} className="rounded-lg border border-border bg-surface-2 px-2.5 py-2">
                      <div className="truncate text-xs font-medium text-foreground">
                        {asset?.name ?? assetId}
                      </div>
                      <div className="mt-0.5 text-[10px] text-faint-foreground">
                        {asset?.source ?? t('blueprint.node.prototype')} · {labels.blueprintEdge('syncs_with')}
                      </div>
                    </div>
                  )
                })}
                <div className="grid grid-cols-[1fr_auto] gap-1.5">
                  <select
                    value={prototypeToBind}
                    onChange={(event) => setPrototypeToBind(event.target.value)}
                    className="min-w-0 rounded-md border border-border bg-background px-2 py-1.5 text-[11px] text-foreground outline-none focus:border-primary/60"
                  >
                    {importAssets.map((asset) => (
                      <option key={asset.id} value={asset.id}>
                        {asset.name}
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    onClick={bindPrototype}
                    className="rounded-md bg-primary px-2 py-1.5 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright"
                  >
                    {t('blueprint.bind')}
                  </button>
                </div>
              </div>

              {activeBlueprintContext?.selectedNodeId === selected.id && (
                <>
                  <InspLabel className="mt-5">{t('blueprint.workbenchTarget')}</InspLabel>
                  <div className="mt-2 rounded-lg border border-primary/30 bg-primary/10 p-3 text-[11px] leading-relaxed text-primary-bright">
                    {t('blueprint.contextSummary', {
                      status: activeBlueprintContext.status,
                      nodes: activeBlueprintContext.nodeIds.length,
                      docs: activeBlueprintContext.linkedDocIds.length,
                    })}
                  </div>
                </>
              )}

              <div className="mt-5 grid grid-cols-2 gap-2">
                <button
                  type="button"
                  onClick={generateDocs}
                  className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-border px-2.5 py-2 text-xs font-medium text-muted-foreground hover:bg-white/5"
                >
                  <FileText className="size-3.5" /> {t('blueprint.docs')}
                </button>
                <button
                  type="button"
                  onClick={() => createWorkbenchContextFromBlueprint(selected.id)}
                  className="inline-flex items-center justify-center gap-1.5 rounded-lg bg-primary px-2.5 py-2 text-xs font-medium text-primary-foreground hover:bg-primary-bright"
                >
                  <Zap className="size-3.5" /> Workbench
                </button>
                <button
                  type="button"
                  onClick={() => {
                    completeBlueprintNode(selected.id)
                    setNotice(t('blueprint.aiCompletionApplied', { title: selected.title }))
                  }}
                  className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-dashed border-border px-2.5 py-2 text-xs text-muted-foreground hover:border-primary/40 hover:text-foreground"
                >
                  <Sparkles className="size-3.5 text-primary-bright" /> {t('blueprint.complete')}
                </button>
                <button
                  type="button"
                  onClick={() => deleteBlueprintNode(selected.id)}
                  className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-border px-2.5 py-2 text-xs text-faint-foreground hover:bg-white/5 hover:text-destructive"
                >
                  <Trash2 className="size-3.5" /> {t('common.delete')}
                </button>
              </div>

              <InspLabel className="mt-5">{t('blueprint.operationLog')}</InspLabel>
              <div className="mt-2 space-y-1.5">
                {blueprintOperations.slice(0, 4).map((operation) => (
                  <div key={operation.id} className="rounded-md border border-border bg-surface-2 px-2 py-1.5">
                    <div className="text-[10px] font-medium text-primary-bright">
                      {operation.type}
                    </div>
                    <div className="mt-0.5 text-[11px] text-muted-foreground">
                      {operation.summary}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </>
        ) : (
          <div className="flex flex-1 items-center justify-center p-6 text-center text-sm text-faint-foreground">
            {t('blueprint.emptyEditor')}
          </div>
        )}
      </aside>
    </div>
  )
}

function nodeTypeForModule(group: string, title: string): BlueprintNodeType {
  if (group === 'Feature packs') return 'feature'
  if (group === 'Page patterns') return 'page'
  if (group === 'API patterns') return 'api'
  if (group === 'Data models') return 'dataModel'
  if (group === 'Permissions') return 'permission'
  if (group === 'Prototype assets') return 'prototype'
  if (group === 'Workbench targets') return 'workbenchTarget'
  if (title.toLowerCase().includes('permission')) return 'permission'
  return 'component'
}

function ActionButton({
  children,
  icon: Icon,
  onClick,
}: {
  children: React.ReactNode
  icon: typeof Save
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex shrink-0 items-center gap-1.5 rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
    >
      <Icon className="size-3.5" />
      {children}
    </button>
  )
}

function InspLabel({
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
