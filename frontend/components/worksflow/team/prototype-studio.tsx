'use client'

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import {
  Box,
  Braces,
  CheckCircle2,
  CircleAlert,
  CircleDashed,
  Component,
  Database,
  Eye,
  EyeOff,
  FileClock,
  FormInput,
  Frame,
  ImageIcon,
  Layers,
  LoaderCircle,
  Lock,
  MonitorSmartphone,
  PackageCheck,
  PanelRight,
  Plus,
  RefreshCw,
  Save,
  Send,
  ShieldCheck,
  Sparkles,
  Trash2,
  Type,
  Unlock,
  Wand2,
  X,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import {
  ArtifactWorkspaceConflictError,
  reviewGateReadyForRequest,
} from '@/lib/platform/artifact-workspace'
import type {
  ArtifactReviewGateDto,
  JsonObject,
  JsonValue,
  PrototypeContentDto,
  PrototypeFixtureDto,
  PrototypeLayerDto,
  PrototypeLayerKind,
  VersionedArtifactDto,
} from '@/lib/platform/dto'
import {
  PrototypeContentMutationError,
  addPrototypeBreakpoint,
  addPrototypeState,
  isRequiredPrototypeBreakpoint,
  prototypeFrameCoverageGaps,
  prototypeReviewIssues,
  removePrototypeBreakpoint,
  removePrototypeState,
  repairPrototypeFrameCoverage,
  updatePrototypeBreakpoint,
  updatePrototypeState,
} from '@/lib/platform/prototype-content'
import { useWorksflow } from '@/lib/worksflow/store'
import { cn } from '@/lib/utils'

type PrototypeMode = 'wireframe' | 'design' | 'component' | 'handoff'
type Panel = 'properties' | 'variants' | 'data' | 'trace'
type SaveState = 'idle' | 'dirty' | 'saving' | 'saved' | 'conflict' | 'error'

const MODES: readonly { id: PrototypeMode; label: string; icon: typeof Frame }[] = [
  { id: 'wireframe', label: 'Wireframe', icon: Frame },
  { id: 'design', label: 'Visual design', icon: ImageIcon },
  { id: 'component', label: 'Components', icon: Component },
  { id: 'handoff', label: 'Handoff', icon: PanelRight },
]

const LAYER_TEMPLATES: readonly {
  kind: PrototypeLayerKind
  name: string
  width: number
  height: number
  icon: typeof Frame
  style?: JsonObject
  properties?: JsonObject
}[] = [
  { kind: 'frame', name: 'Section', width: 360, height: 160, icon: Frame, style: { fill: '#171719', borderRadius: 12 } },
  { kind: 'text', name: 'Heading', width: 240, height: 36, icon: Type, style: { color: '#ffffff', fontSize: 24 }, properties: { text: 'Heading' } },
  { kind: 'button', name: 'Primary button', width: 144, height: 44, icon: Sparkles, style: { fill: '#1488fc', borderRadius: 10 }, properties: { text: 'Continue' } },
  { kind: 'input', name: 'Text input', width: 260, height: 44, icon: FormInput, style: { fill: '#26262a', borderRadius: 8 }, properties: { placeholder: 'Enter value' } },
  { kind: 'componentInstance', name: 'Card', width: 300, height: 104, icon: Box, style: { fill: '#1e1e21', borderRadius: 12 } },
  { kind: 'image', name: 'Image', width: 300, height: 160, icon: ImageIcon, style: { fill: '#20252b', borderRadius: 12 } },
  { kind: 'list', name: 'List', width: 320, height: 180, icon: Layers, style: { fill: '#1e1e21', borderRadius: 10 } },
]

const LAYER_ICONS: Record<PrototypeLayerKind, typeof Frame> = {
  frame: Frame,
  group: Layers,
  text: Type,
  image: ImageIcon,
  componentInstance: Component,
  input: FormInput,
  button: Sparkles,
  list: Layers,
  overlay: PanelRight,
  slot: Box,
}

export function PrototypeStudio() {
  const workspace = useArtifactWorkspace()
  const collaboration = useCollaboration()
  const { setSurface } = useWorksflow()
  const [activeArtifactId, setActiveArtifactId] = useState('')
  const [content, setContent] = useState<PrototypeContentDto | null>(null)
  const [draftEtag, setDraftEtag] = useState('')
  const [saveState, setSaveState] = useState<SaveState>('idle')
  const [error, setError] = useState<string | null>(null)
  const [mode, setMode] = useState<PrototypeMode>('wireframe')
  const [panel, setPanel] = useState<Panel>('properties')
  const [selectedLayerId, setSelectedLayerId] = useState('')
  const [selectedStateId, setSelectedStateId] = useState('')
  const [selectedBreakpointId, setSelectedBreakpointId] = useState('')
  const [zoom, setZoom] = useState(82)
  const [showGrid, setShowGrid] = useState(true)
  const [details, setDetails] = useState<Awaited<ReturnType<typeof workspace.loadDetails<PrototypeContentDto>>> | null>(null)
  const [selectedPageSpecId, setSelectedPageSpecId] = useState('')
  const [newPrototypeTitle, setNewPrototypeTitle] = useState('')
  const [drag, setDrag] = useState<{
    id: string
    pointerX: number
    pointerY: number
    originX: number
    originY: number
  } | null>(null)
  const saveSequence = useRef(0)
  const activeResource = workspace.prototypes.find((item) => item.artifact.id === activeArtifactId)
    ?? workspace.prototypes[0]
  const activeId = activeResource?.artifact.id ?? ''
  const canEdit = collaboration.session.signedIn && collaboration.can('edit')
  const canReview = collaboration.session.signedIn && collaboration.can('publish')

  useEffect(() => {
    const referenced = artifactReference()
    const next = workspace.prototypes.find((item) => item.artifact.id === referenced)?.artifact.id
      ?? activeId
    if (!activeArtifactId && next) setActiveArtifactId(next)
  }, [activeArtifactId, activeId, workspace.prototypes])

  const serverContent = activeResource?.draft?.content
    ?? activeResource?.latestRevision?.content
    ?? activeResource?.approvedRevision?.content

  useEffect(() => {
    if (!activeResource || !serverContent) {
      setContent(null)
      setDraftEtag('')
      setSelectedLayerId('')
      setSelectedStateId('')
      setSelectedBreakpointId('')
      setDetails(null)
      return
    }
    if (saveState === 'dirty' || saveState === 'saving' || saveState === 'conflict') return
    setContent(cloneContent(serverContent))
    setDraftEtag(activeResource.draft?.etag ?? activeResource.artifact.etag)
    setSelectedLayerId((current) => current && serverContent.layers[current]
      ? current
      : serverContent.frames[0]?.rootLayerId ?? Object.keys(serverContent.layers)[0] ?? '')
    setSelectedStateId((current) => serverContent.states.some((item) => item.id === current)
      ? current
      : serverContent.states[0]?.id ?? '')
    setSelectedBreakpointId((current) => serverContent.breakpoints.some((item) => item.id === current)
      ? current
      : serverContent.breakpoints[0]?.id ?? '')
    setSaveState('idle')
    void workspace.loadDetails<PrototypeContentDto>(activeResource.artifact.id)
      .then(setDetails)
      .catch((cause) => setError(message(cause)))
  }, [activeResource, saveState, serverContent, workspace])

  const saveDraft = useCallback(async (nextContent = content) => {
    if (!activeResource || !nextContent || !draftEtag || !canEdit) return null
    const sequence = ++saveSequence.current
    setSaveState('saving')
    setError(null)
    try {
      const result = await workspace.savePrototypeDraft(activeResource.artifact.id, nextContent, draftEtag)
      if (sequence !== saveSequence.current) return result
      const nextEtag = result.data.draft?.etag ?? result.etag
      if (nextEtag) setDraftEtag(nextEtag)
      setSaveState('saved')
      try {
        const nextDetails = await workspace.loadDetails<PrototypeContentDto>(activeResource.artifact.id)
        if (sequence !== saveSequence.current) return result
        setDetails(nextDetails)
      } catch (cause) {
        if (sequence === saveSequence.current) {
          setDetails(null)
          setError(`Draft saved, but the review gate could not be refreshed: ${message(cause)}`)
        }
      }
      return result
    } catch (cause) {
      if (sequence !== saveSequence.current) return null
      if (cause instanceof ArtifactWorkspaceConflictError) {
        setSaveState('conflict')
        setError('The server draft changed. Your local prototype is preserved; reload or retry after reviewing the latest revision.')
      } else {
        setSaveState('error')
        setError(message(cause))
      }
      return null
    }
  }, [activeResource, canEdit, content, draftEtag, workspace])

  useEffect(() => {
    if (saveState !== 'dirty' || !content || !canEdit) return
    const timer = window.setTimeout(() => void saveDraft(content), 750)
    return () => window.clearTimeout(timer)
  }, [canEdit, content, saveDraft, saveState])

  const updateContent = useCallback((updater: (current: PrototypeContentDto) => PrototypeContentDto) => {
    if (!canEdit) return
    setContent((current) => current ? updater(current) : current)
    setSaveState('dirty')
    setError(null)
  }, [canEdit])

  const selectedLayer = content?.layers[selectedLayerId]
  const breakpoint = content?.breakpoints.find((item) => item.id === selectedBreakpointId)
    ?? content?.breakpoints[0]
  const state = content?.states.find((item) => item.id === selectedStateId)
    ?? content?.states[0]
  const frame = content?.frames.find((item) =>
    item.stateId === state?.id && item.breakpointId === breakpoint?.id,
  )
  const visibleLayers = useMemo(
    () => content ? layerTree(content.layers, frame?.rootLayerId) : [],
    [content, frame?.rootLayerId],
  )
  const proposals = workspace.proposals.filter((item) => item.artifactId === activeId)
  const review = collaboration.reviews.find((item) => item.target?.artifactId === activeId)
  const clientIssues = useMemo(
    () => content ? prototypeReviewIssues(content) : [],
    [content],
  )
  const serverGateIssues = useMemo(
    () => reviewGateIssues(details?.reviewGate),
    [details?.reviewGate],
  )
  const revisionReady = clientIssues.length === 0
  const requestReady = reviewGateReadyForRequest(details?.reviewGate)

  function updateLayer(updates: Partial<PrototypeLayerDto>) {
    if (!selectedLayer) return
    updateContent((current) => ({
      ...current,
      layers: {
        ...current.layers,
        [selectedLayer.id]: {
          ...selectedLayer,
          ...updates,
          fieldMetadata: {
            ...selectedLayer.fieldMetadata,
            ...fieldMetadataFor(updates, collaboration.session.signedIn ? collaboration.session.user.id : ''),
          },
        },
      },
    }))
  }

  function updateLayerLayout(updates: Record<string, JsonValue>) {
    if (!selectedLayer) return
    updateLayer({ layout: { ...selectedLayer.layout, ...updates } })
  }

  function updateLayerStyle(updates: Record<string, JsonValue>) {
    if (!selectedLayer) return
    updateLayer({ style: { ...selectedLayer.style, ...updates } })
  }

  function addLayer(template: (typeof LAYER_TEMPLATES)[number]) {
    if (!content) return
    const id = stableId('layer')
    const root = frame?.rootLayerId ? content.layers[frame.rootLayerId] : undefined
    const next: PrototypeLayerDto = {
      id,
      parentId: root?.id,
      childIds: [],
      kind: template.kind,
      name: template.name,
      semanticRole: template.kind === 'button' ? 'button' : undefined,
      layout: { x: 40, y: 40 + Object.keys(content.layers).length * 8, width: template.width, height: template.height },
      style: template.style ?? {},
      properties: template.properties ?? {},
      requirementIds: [],
      acceptanceCriterionIds: [],
      fieldMetadata: fieldMetadataFor({ layout: {}, style: {}, properties: {} }, collaboration.session.signedIn ? collaboration.session.user.id : ''),
    }
    updateContent((current) => ({
      ...current,
      layers: {
        ...current.layers,
        ...(root ? { [root.id]: { ...root, childIds: [...root.childIds, id] } } : {}),
        [id]: next,
      },
    }))
    setSelectedLayerId(id)
  }

  function duplicateLayer() {
    if (!content || !selectedLayer) return
    const id = stableId('layer')
    const cloned = cloneLayer(selectedLayer, id)
    const next = {
      ...cloned,
      layout: {
        ...cloned.layout,
        x: numberValue(cloned.layout.x, 0) + 16,
        y: numberValue(cloned.layout.y, 0) + 16,
      },
    }
    updateContent((current) => {
      const parent = selectedLayer.parentId ? current.layers[selectedLayer.parentId] : undefined
      return {
        ...current,
        layers: {
          ...current.layers,
          ...(parent ? { [parent.id]: { ...parent, childIds: [...parent.childIds, id] } } : {}),
          [id]: next,
        },
      }
    })
    setSelectedLayerId(id)
  }

  function deleteLayer() {
    if (!content || !selectedLayer || selectedLayer.id === frame?.rootLayerId) return
    updateContent((current) => {
      const layers = { ...current.layers }
      const remove = new Set(descendantIds(layers, selectedLayer.id))
      remove.add(selectedLayer.id)
      for (const id of remove) delete layers[id]
      for (const [id, item] of Object.entries(layers)) {
        layers[id] = { ...item, childIds: item.childIds.filter((childId) => !remove.has(childId)) }
      }
      return {
        ...current,
        layers,
        interactions: current.interactions.filter((item) => !remove.has(item.sourceLayerId)),
        overrides: current.overrides.filter((item) => !remove.has(item.layerId)),
        tokenBindings: current.tokenBindings.filter((item) => !remove.has(item.layerId)),
        componentBindings: current.componentBindings.filter((item) => !remove.has(item.layerId)),
      }
    })
    setSelectedLayerId(frame?.rootLayerId ?? '')
  }

  function startDrag(event: ReactPointerEvent<HTMLButtonElement>, item: PrototypeLayerDto) {
    if (!canEdit || booleanValue(item.properties.locked)) return
    event.currentTarget.setPointerCapture(event.pointerId)
    setSelectedLayerId(item.id)
    setDrag({
      id: item.id,
      pointerX: event.clientX,
      pointerY: event.clientY,
      originX: numberValue(item.layout.x, 0),
      originY: numberValue(item.layout.y, 0),
    })
  }

  function moveDrag(event: ReactPointerEvent<HTMLDivElement>) {
    if (!drag || !content) return
    const item = content.layers[drag.id]
    if (!item) return
    const scale = zoom / 100
    const x = Math.round((drag.originX + (event.clientX - drag.pointerX) / scale) / 8) * 8
    const y = Math.round((drag.originY + (event.clientY - drag.pointerY) / scale) / 8) * 8
    updateContent((current) => ({
      ...current,
      layers: {
        ...current.layers,
        [item.id]: { ...item, layout: { ...item.layout, x, y } },
      },
    }))
  }

  async function createPrototype() {
    const pageSpec = workspace.pageSpecs.find((item) => item.artifact.id === selectedPageSpecId)
      ?? workspace.pageSpecs.find((item) => item.approvedRevision)
      ?? workspace.pageSpecs[0]
    if (!pageSpec) return
    if (!pageSpec.approvedRevision) {
      setError('Approve the PageSpec revision before creating a formal prototype.')
      return
    }
    setError(null)
    try {
      const id = await workspace.createPrototype(
        pageSpec.artifact.id,
        newPrototypeTitle.trim() || `${pageSpec.artifact.title} Prototype`,
        false,
      )
      if (id) {
        setActiveArtifactId(id)
        setNewPrototypeTitle('')
      }
    } catch (cause) {
      setError(message(cause))
    }
  }

  async function createRevisionAndRequestReview() {
    if (!activeResource || !content || !canEdit) return
    const issues = prototypeReviewIssues(content)
    if (issues.length > 0) {
      setError(`Prototype revision gate is blocked: ${issues.join(' ')}`)
      return
    }
    if (!draftEtag) {
      setError('The exact server draft ETag is missing. Reload the prototype before creating a revision.')
      return
    }
    if (saveState === 'conflict') {
      setError('Resolve the draft ETag conflict before creating a revision.')
      return
    }
    if (saveState === 'dirty' || saveState === 'saving') {
      setError('Wait for the current draft autosave before creating a revision.')
      return
    }
    setSaveState('saving')
    setError(null)
    let createdRevisionNumber: number | null = null
    try {
      const saved = await workspace.savePrototypeDraft(activeResource.artifact.id, content, draftEtag)
      const etag = saved.data.draft?.etag ?? saved.etag
      if (!etag) throw new Error('The Go service did not return a draft ETag.')
      setDraftEtag(etag)
      const revisionResult = await collaboration.platformClient.prototypes.createRevision(
        activeResource.artifact.id,
        { changeSummary: 'Prototype checkpoint for review', changeSource: 'human' },
        { ifMatch: etag, idempotencyKey: true },
      )
      const revision = revisionResult.data
      createdRevisionNumber = revision.revisionNumber
      await workspace.refresh()
      const currentDetails = await workspace.loadDetails<PrototypeContentDto>(activeResource.artifact.id)
      setDetails(currentDetails)
      if (!reviewGateReadyForRequest(currentDetails.reviewGate)) {
        const blockers = reviewGateIssues(currentDetails.reviewGate)
        setSaveState('saved')
        setError(`Revision ${revision.revisionNumber} was created, but review was not requested: ${blockers.join(' ') || 'refresh the server gate and resolve its blocking checks.'}`)
        return
      }
      const currentUserId = collaboration.session.signedIn ? collaboration.session.user.id : ''
      const reviewerIds = collaboration.members
        .filter((member) => member.user.id !== currentUserId && ['owner', 'admin', 'editor'].includes(member.role))
        .map((member) => member.user.id)
      if (reviewerIds.length === 0) {
        setError('Revision created. Add another owner, admin, or editor before requesting review.')
      } else {
        await collaboration.requestReview(
          'Review prototype states, responsive frames, fixtures, interactions, tokens, and trace coverage.',
          {
            artifactId: revision.artifactId,
            revisionId: revision.id,
            revisionNumber: revision.revisionNumber,
            contentHash: revision.contentHash,
            title: activeResource.artifact.title,
          },
          reviewerIds,
        )
      }
      await workspace.refresh()
      setSaveState('saved')
    } catch (cause) {
      setSaveState(createdRevisionNumber === null ? 'error' : 'saved')
      setError(createdRevisionNumber === null
        ? message(cause)
        : `Revision ${createdRevisionNumber} was created, but review was not requested: ${message(cause)}`)
    }
  }

  if (!collaboration.session.signedIn || !collaboration.project) {
    return <StudioGate title="Sign in and select a server project" description="Prototype Studio never opens browser fixtures as team facts." />
  }
  if (collaboration.backendStatus === 'error' || workspace.status === 'error') {
    return <StudioGate title="Artifact service unavailable" description={workspace.error ?? 'Prototype editing is disabled until the Go service recovers.'} onRetry={workspace.refresh} />
  }
  if (workspace.status === 'loading') {
    return <StudioGate loading title="Loading prototypes" description="Reading exact server drafts, revisions, sources, and review state…" />
  }

  return (
    <div className="flex h-full min-h-0 bg-canvas max-lg:flex-col max-lg:overflow-y-auto">
      <aside className="flex w-64 shrink-0 flex-col border-r border-border bg-panel max-lg:max-h-[380px] max-lg:w-full max-lg:border-b max-lg:border-r-0">
        <div className="border-b border-border p-3">
          <div className="flex items-center gap-2">
            <MonitorSmartphone className="size-4 text-primary-bright" />
            <div className="min-w-0 flex-1">
              <h2 className="text-xs font-semibold text-foreground">Prototype artifacts</h2>
              <p className="mt-0.5 text-[9px] text-faint-foreground">Server drafts and immutable revisions</p>
            </div>
            <span className="rounded bg-white/5 px-1.5 py-0.5 text-[9px] text-faint-foreground">{workspace.prototypes.length}</span>
          </div>
        </div>
        <div className="border-b border-border p-2">
          {workspace.prototypes.length === 0 && <p className="rounded border border-dashed border-border p-3 text-center text-[9px] text-faint-foreground">No prototype artifact yet.</p>}
          {workspace.prototypes.map((prototype) => (
            <button
              key={prototype.artifact.id}
              type="button"
              onClick={() => {
                setSaveState('idle')
                setActiveArtifactId(prototype.artifact.id)
                setError(null)
              }}
              className={cn('mb-1 block w-full rounded-md border px-2.5 py-2 text-left', activeId === prototype.artifact.id ? 'border-primary/40 bg-primary/10' : 'border-transparent hover:border-border hover:bg-white/5')}
            >
              <span className="block truncate text-[11px] font-medium text-foreground">{prototype.artifact.title}</span>
              <span className="mt-1 flex items-center gap-1.5 text-[8px] text-faint-foreground">
                <span>{prototype.artifact.status}</span>
                <span>draft {prototype.draft?.revision ?? '—'}</span>
                <span>revision {prototype.latestRevision?.revisionNumber ?? '—'}</span>
              </span>
            </button>
          ))}
        </div>
        {canEdit && (
          <div className="border-b border-border p-2">
            <select value={selectedPageSpecId} onChange={(event) => setSelectedPageSpecId(event.target.value)} className="h-7 w-full rounded border border-border bg-background px-1.5 text-[9px] text-foreground outline-none" aria-label="PageSpec source">
              <option value="">Select PageSpec source</option>
              {workspace.pageSpecs.map((pageSpec) => <option key={pageSpec.artifact.id} value={pageSpec.artifact.id}>{pageSpec.artifact.title} · {pageSpec.approvedRevision ? `approved r${pageSpec.approvedRevision.revisionNumber}` : 'latest revision'}</option>)}
            </select>
            <div className="mt-1.5 flex gap-1">
              <input value={newPrototypeTitle} onChange={(event) => setNewPrototypeTitle(event.target.value)} placeholder="Prototype title" className="h-7 min-w-0 flex-1 rounded border border-border bg-background px-1.5 text-[9px] text-foreground outline-none" />
              <button type="button" onClick={() => void createPrototype()} disabled={workspace.pageSpecs.length === 0} className="flex size-7 items-center justify-center rounded bg-primary text-primary-foreground disabled:opacity-35" aria-label="Create server prototype"><Plus className="size-3.5" /></button>
            </div>
          </div>
        )}
        {content && (
          <>
            <div className="flex items-center gap-2 border-b border-border px-3 py-2 text-[9px] font-semibold uppercase tracking-wider text-faint-foreground"><Layers className="size-3" />Layers<span className="ml-auto font-mono">{Object.keys(content.layers).length}</span></div>
            <div className="min-h-0 flex-1 overflow-y-auto p-2 scrollbar-thin">
              {visibleLayers.toReversed().map((item) => {
                const Icon = LAYER_ICONS[item.kind]
                return <button key={item.id} type="button" onClick={() => setSelectedLayerId(item.id)} className={cn('mb-0.5 flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[10px]', selectedLayerId === item.id ? 'bg-primary/10 text-primary-bright' : 'text-muted-foreground hover:bg-white/5 hover:text-foreground')}><Icon className="size-3 shrink-0" /><span className="min-w-0 flex-1 truncate">{item.name}</span>{booleanValue(item.properties.locked) && <Lock className="size-2.5" />}{booleanValue(item.properties.hidden) && <EyeOff className="size-2.5" />}</button>
              })}
            </div>
            {canEdit && (
              <div className="border-t border-border p-2">
                <div className="grid grid-cols-4 gap-1">
                  {LAYER_TEMPLATES.map((template) => { const Icon = template.icon; return <button key={`${template.kind}-${template.name}`} type="button" onClick={() => addLayer(template)} className="flex h-12 flex-col items-center justify-center gap-1 rounded border border-border text-[8px] text-faint-foreground hover:border-primary/40 hover:text-foreground" title={`Add ${template.name}`}><Icon className="size-3.5" /><span className="max-w-full truncate px-1">{template.name}</span></button> })}
                </div>
              </div>
            )}
          </>
        )}
      </aside>

      {!content || !activeResource ? (
        <div className="min-w-0 flex-1"><StudioGate title="Create a prototype from a PageSpec" description="A formal prototype starts from an exact PageSpec revision. Local sample pages are not used." /></div>
      ) : (
        <>
          <main className="flex min-w-0 flex-1 flex-col">
            <header className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-b border-border bg-panel px-3 py-1.5">
              <div className="flex items-center gap-1 rounded border border-border bg-background p-0.5">
                {MODES.map((item) => { const Icon = item.icon; return <button key={item.id} type="button" onClick={() => setMode(item.id)} className={cn('inline-flex h-7 items-center gap-1 rounded px-2 text-[9px]', mode === item.id ? 'bg-primary/15 text-primary-bright' : 'text-faint-foreground hover:text-foreground')}><Icon className="size-3" />{item.label}</button> })}
              </div>
              <select value={selectedStateId} onChange={(event) => setSelectedStateId(event.target.value)} className="h-7 rounded border border-border bg-background px-2 text-[9px] text-foreground outline-none" aria-label="Prototype state">{content.states.map((item) => <option key={item.id} value={item.id}>{item.title}{item.required ? ' · required' : ''}</option>)}</select>
              <select value={selectedBreakpointId} onChange={(event) => setSelectedBreakpointId(event.target.value)} className="h-7 rounded border border-border bg-background px-2 text-[9px] text-foreground outline-none" aria-label="Prototype breakpoint">{content.breakpoints.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.viewportWidth}×{item.viewportHeight}</option>)}</select>
              <button type="button" onClick={() => setPanel('variants')} className="inline-flex h-7 items-center gap-1 rounded border border-border px-2 text-[9px] text-faint-foreground hover:text-foreground"><MonitorSmartphone className="size-3" />Manage states &amp; breakpoints</button>
              <div className="ml-auto flex items-center gap-1">
                <button type="button" onClick={() => setShowGrid((value) => !value)} className={cn('rounded p-1.5 text-faint-foreground hover:text-foreground', showGrid && 'bg-primary/10 text-primary-bright')} aria-label="Toggle grid"><Braces className="size-3.5" /></button>
                <button type="button" onClick={() => setZoom((value) => Math.max(25, value - 10))} className="rounded p-1.5 text-faint-foreground hover:text-foreground" aria-label="Zoom out"><ZoomOut className="size-3.5" /></button>
                <span className="w-9 text-center font-mono text-[9px] text-faint-foreground">{zoom}%</span>
                <button type="button" onClick={() => setZoom((value) => Math.min(160, value + 10))} className="rounded p-1.5 text-faint-foreground hover:text-foreground" aria-label="Zoom in"><ZoomIn className="size-3.5" /></button>
                <SaveIndicator state={saveState} />
              </div>
            </header>

            {error && <div role="alert" className="flex items-center gap-2 border-b border-destructive/30 bg-destructive/10 px-3 py-2 text-[9px] text-destructive"><CircleAlert className="size-3 shrink-0" /><span className="min-w-0 flex-1">{error}</span>{saveState === 'conflict' && <button type="button" onClick={() => { setSaveState('idle'); void workspace.refresh() }} className="rounded border border-destructive/30 px-2 py-1">Load server draft</button>}<button type="button" onClick={() => setError(null)} aria-label="Dismiss"><X className="size-3" /></button></div>}

            <div className="relative min-h-0 flex-1 overflow-auto bg-[#0b0b0d] p-8 scrollbar-thin" onPointerMove={moveDrag} onPointerUp={() => setDrag(null)} onPointerCancel={() => setDrag(null)}>
              {mode === 'design' && <div className="absolute left-3 top-3 z-20 rounded border border-primary/30 bg-primary/10 px-2 py-1 text-[8px] text-primary-bright">Design mode · token bindings {content.tokenBindings.length}</div>}
              {mode === 'component' && <div className="absolute left-3 top-3 z-20 rounded border border-primary/30 bg-primary/10 px-2 py-1 text-[8px] text-primary-bright">Component mappings {content.componentBindings.length}</div>}
              {mode === 'handoff' && <div className="absolute left-3 top-3 z-20 rounded border border-success/30 bg-success/10 px-2 py-1 text-[8px] text-success">Exact source trace · {content.traceLinks.length} links</div>}
              {breakpoint && frame && (
                <div className="relative mx-auto origin-top-left overflow-hidden rounded-xl border border-white/15 bg-[#171719] shadow-2xl" style={{ width: breakpoint.viewportWidth, height: breakpoint.viewportHeight, transform: `scale(${zoom / 100})`, marginBottom: `${breakpoint.viewportHeight * (zoom / 100 - 1)}px`, backgroundImage: showGrid ? 'linear-gradient(rgba(255,255,255,.035) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,.035) 1px, transparent 1px)' : undefined, backgroundSize: showGrid ? '8px 8px' : undefined }}>
                  {visibleLayers.map((item) => <CanvasLayer key={item.id} layer={item} selected={item.id === selectedLayerId} onSelect={() => setSelectedLayerId(item.id)} onPointerDown={(event) => startDrag(event, item)} />)}
                  {state && state.key !== 'ready' && <div className="pointer-events-none absolute inset-0 z-50 flex items-center justify-center bg-black/35"><div className="rounded-lg border border-border bg-panel/95 px-5 py-3 text-center shadow-xl"><p className="text-xs font-semibold text-foreground">{state.title}</p><p className="mt-1 text-[9px] text-faint-foreground">Fixture state · {state.fixtureIds.length} pinned fixture(s)</p></div></div>}
                </div>
              )}
              {state && breakpoint && !frame && (
                <div className="mx-auto flex max-w-sm flex-col items-center rounded-xl border border-warning/30 bg-warning/10 p-6 text-center text-warning">
                  <CircleAlert className="size-6" />
                  <p className="mt-2 text-xs font-semibold">Missing {state.title} · {breakpoint.name} frame</p>
                  <p className="mt-1 text-[9px] leading-relaxed opacity-80">Complete frame coverage before creating an immutable revision.</p>
                  {canEdit && <button type="button" onClick={() => updateContent((current) => repairPrototypeFrameCoverage(current, stableId))} className="mt-3 rounded bg-warning px-3 py-1.5 text-[9px] font-semibold text-black">Repair all frame coverage</button>}
                </div>
              )}
            </div>

            {(clientIssues.length > 0 || !requestReady) && (
              <div className="border-t border-warning/30 bg-warning/10 px-3 py-2 text-[8px] text-warning">
                <span className="font-semibold">{clientIssues.length > 0 ? 'Revision gate blocked.' : 'Current review-request gate is blocked.'}</span>{' '}
                {clientIssues[0] ?? serverGateIssues[0] ?? 'A revision can still be created; the server gate will be refreshed before review is requested.'}
                {(clientIssues.length > 1 || (clientIssues.length === 0 && serverGateIssues.length > 1)) && ` +${(clientIssues.length || serverGateIssues.length) - 1} more`}
              </div>
            )}
            <footer className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-t border-border bg-panel px-3 py-2">
              <div className="flex items-center gap-2 text-[9px] text-faint-foreground"><ShieldCheck className="size-3 text-success" />PageSpec {shortRef(content.pageSpecRevision)} · {content.exploratory ? 'exploratory' : 'formal'}</div>
              <div className="ml-auto flex items-center gap-1.5">
                <button type="button" onClick={() => void saveDraft()} disabled={!canEdit || saveState === 'saving'} className="inline-flex h-7 items-center gap-1 rounded border border-border px-2 text-[9px] text-muted-foreground hover:text-foreground disabled:opacity-35"><Save className="size-3" />Save draft</button>
                <button type="button" onClick={() => void createRevisionAndRequestReview()} disabled={!canEdit || !draftEtag || (saveState !== 'saved' && saveState !== 'idle') || !revisionReady} title={revisionReady ? 'Create an immutable revision, then refresh the server gate before requesting review' : clientIssues[0]} className="inline-flex h-7 items-center gap-1 rounded border border-primary/35 bg-primary/10 px-2 text-[9px] text-primary-bright disabled:opacity-35"><Send className="size-3" />Revision + review</button>
                <button type="button" onClick={() => setSurface('workbench')} disabled={!activeResource.approvedRevision} className="inline-flex h-7 items-center gap-1 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground disabled:opacity-35" title="Only approved prototype revisions can become build input"><PackageCheck className="size-3" />Open Workbench</button>
              </div>
            </footer>
          </main>

          <aside className="flex w-72 shrink-0 flex-col border-l border-border bg-panel max-xl:w-64 max-lg:max-h-[440px] max-lg:w-full max-lg:border-l-0 max-lg:border-t">
            <div className="grid grid-cols-4 border-b border-border p-1">
              {(['properties', 'variants', 'data', 'trace'] as Panel[]).map((item) => <button key={item} type="button" onClick={() => setPanel(item)} className={cn('rounded px-1 py-1.5 text-[8px] capitalize', panel === item ? 'bg-primary/10 text-primary-bright' : 'text-faint-foreground hover:text-foreground')}>{item}</button>)}
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto p-3 scrollbar-thin">
              {panel === 'properties' && <PropertiesPanel layer={selectedLayer} rootLayerId={frame?.rootLayerId} canEdit={canEdit} onUpdate={updateLayer} onLayout={updateLayerLayout} onStyle={updateLayerStyle} onDuplicate={duplicateLayer} onDelete={deleteLayer} />}
              {panel === 'variants' && <VariantsPanel content={content} selectedStateId={state?.id} selectedBreakpointId={breakpoint?.id} canEdit={canEdit} onChange={updateContent} onSelectState={setSelectedStateId} onSelectBreakpoint={setSelectedBreakpointId} onError={setError} />}
              {panel === 'data' && <DataPanel content={content} stateId={state?.id} canEdit={canEdit} onChange={updateContent} />}
              {panel === 'trace' && <TracePanel resource={activeResource} content={content} details={details} proposals={proposals} review={review} clientIssues={clientIssues} canReview={canReview} onRefresh={() => void workspace.refresh()} />}
            </div>
          </aside>
        </>
      )}
    </div>
  )
}

function VariantsPanel({
  content,
  selectedStateId,
  selectedBreakpointId,
  canEdit,
  onChange,
  onSelectState,
  onSelectBreakpoint,
  onError,
}: {
  content: PrototypeContentDto
  selectedStateId?: string
  selectedBreakpointId?: string
  canEdit: boolean
  onChange: (updater: (content: PrototypeContentDto) => PrototypeContentDto) => void
  onSelectState: (id: string) => void
  onSelectBreakpoint: (id: string) => void
  onError: (error: string | null) => void
}) {
  const [newStateKey, setNewStateKey] = useState('alternate')
  const [newStateTitle, setNewStateTitle] = useState('Alternate')
  const [newBreakpointName, setNewBreakpointName] = useState('Wide')
  const [newViewportWidth, setNewViewportWidth] = useState(1920)
  const [newViewportHeight, setNewViewportHeight] = useState(1080)
  const gaps = prototypeFrameCoverageGaps(content)

  function mutate(operation: (current: PrototypeContentDto) => PrototypeContentDto) {
    try {
      const next = operation(content)
      onChange(() => next)
      onError(null)
      return true
    } catch (cause) {
      onError(cause instanceof PrototypeContentMutationError ? cause.message : message(cause))
      return false
    }
  }

  function createState() {
    const id = stableId('state')
    if (mutate((current) => addPrototypeState(current, {
      id,
      key: newStateKey.trim(),
      title: newStateTitle.trim(),
      required: true,
      fixtureIds: [],
    }, stableId))) {
      onSelectState(id)
      setNewStateKey('alternate')
      setNewStateTitle('Alternate')
    }
  }

  function deleteState(stateId: string) {
    const nextSelection = content.states.find((state) => state.id !== stateId)?.id ?? ''
    if (mutate((current) => removePrototypeState(current, stateId))) onSelectState(nextSelection)
  }

  function createBreakpoint() {
    const id = stableId('breakpoint')
    if (mutate((current) => addPrototypeBreakpoint(current, {
      id,
      name: newBreakpointName.trim(),
      minWidth: Math.max(0, newViewportWidth),
      viewportWidth: Math.max(1, newViewportWidth),
      viewportHeight: Math.max(1, newViewportHeight),
    }, stableId))) {
      onSelectBreakpoint(id)
      setNewBreakpointName('Wide')
    }
  }

  function deleteBreakpoint(breakpointId: string) {
    const nextSelection = content.breakpoints.find((breakpoint) => breakpoint.id !== breakpointId)?.id ?? ''
    if (mutate((current) => removePrototypeBreakpoint(current, breakpointId))) onSelectBreakpoint(nextSelection)
  }

  return (
    <div className="space-y-5">
      <section>
        <div className="flex items-center justify-between"><PanelLabel>States</PanelLabel><span className="font-mono text-[8px] text-faint-foreground">{content.states.length}</span></div>
        <div className="mt-2 space-y-2">
          {content.states.map((state) => (
            <div key={state.id} className={cn('rounded border p-2', selectedStateId === state.id ? 'border-primary/40 bg-primary/5' : 'border-border bg-background')}>
              <div className="flex items-center gap-1"><button type="button" onClick={() => onSelectState(state.id)} className="min-w-0 flex-1 truncate text-left font-mono text-[8px] text-faint-foreground">{state.id}</button><button type="button" onClick={() => deleteState(state.id)} disabled={!canEdit || content.states.length <= 1} className="rounded p-1 text-faint-foreground hover:text-destructive disabled:opacity-25" aria-label={`Delete state ${state.title}`} title={content.states.length <= 1 ? 'A prototype must keep at least one state.' : 'Delete state and dependent frames, overrides, fixtures, and actions'}><Trash2 className="size-3" /></button></div>
              <div className="mt-1 grid grid-cols-2 gap-1"><input value={state.key} onFocus={() => onSelectState(state.id)} onChange={(event) => mutate((current) => updatePrototypeState(current, state.id, { key: event.target.value }))} disabled={!canEdit} className="h-7 rounded border border-border bg-panel px-1.5 font-mono text-[8px] text-foreground outline-none disabled:opacity-50" aria-label={`State key ${state.title}`} /><input value={state.title} onFocus={() => onSelectState(state.id)} onChange={(event) => mutate((current) => updatePrototypeState(current, state.id, { title: event.target.value }))} disabled={!canEdit} className="h-7 rounded border border-border bg-panel px-1.5 text-[8px] text-foreground outline-none disabled:opacity-50" aria-label={`State title ${state.key}`} /></div>
              <label className="mt-1.5 flex items-center gap-1.5 text-[8px] text-faint-foreground"><input type="checkbox" checked={state.required} onChange={(event) => mutate((current) => updatePrototypeState(current, state.id, { required: event.target.checked }))} disabled={!canEdit} />Required coverage · {state.fixtureIds.length} fixture(s)</label>
            </div>
          ))}
        </div>
        {canEdit && <div className="mt-2 rounded border border-dashed border-border p-2"><div className="grid grid-cols-2 gap-1"><input value={newStateKey} onChange={(event) => setNewStateKey(event.target.value)} className="h-7 rounded border border-border bg-background px-1.5 font-mono text-[8px] text-foreground outline-none" placeholder="stable-key" aria-label="New state key" /><input value={newStateTitle} onChange={(event) => setNewStateTitle(event.target.value)} className="h-7 rounded border border-border bg-background px-1.5 text-[8px] text-foreground outline-none" placeholder="State title" aria-label="New state title" /></div><button type="button" onClick={createState} disabled={!newStateKey.trim() || !newStateTitle.trim()} className="mt-1.5 inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[8px] font-semibold text-primary-foreground disabled:opacity-35"><Plus className="size-3" />Add state with all frames</button></div>}
      </section>

      <section>
        <div className="flex items-center justify-between"><PanelLabel>Breakpoints</PanelLabel><span className="font-mono text-[8px] text-faint-foreground">{content.breakpoints.length}</span></div>
        <div className="mt-2 space-y-2">
          {content.breakpoints.map((breakpoint) => {
            const required = isRequiredPrototypeBreakpoint(breakpoint)
            return (
              <div key={breakpoint.id} className={cn('rounded border p-2', selectedBreakpointId === breakpoint.id ? 'border-primary/40 bg-primary/5' : 'border-border bg-background')}>
                <div className="flex items-center gap-1"><button type="button" onClick={() => onSelectBreakpoint(breakpoint.id)} className="min-w-0 flex-1 truncate text-left font-mono text-[8px] text-faint-foreground">{breakpoint.id}</button>{required && <span className="rounded bg-success/10 px-1 text-[7px] text-success">required</span>}<button type="button" onClick={() => deleteBreakpoint(breakpoint.id)} disabled={!canEdit || required} className="rounded p-1 text-faint-foreground hover:text-destructive disabled:opacity-25" aria-label={`Delete breakpoint ${breakpoint.name}`} title={required ? 'Desktop, Tablet, and Mobile cannot be deleted.' : 'Delete breakpoint and dependent frames and overrides'}><Trash2 className="size-3" /></button></div>
                <input value={breakpoint.name} onFocus={() => onSelectBreakpoint(breakpoint.id)} onChange={(event) => mutate((current) => updatePrototypeBreakpoint(current, breakpoint.id, { name: event.target.value }))} disabled={!canEdit || required} className="mt-1 h-7 w-full rounded border border-border bg-panel px-1.5 text-[8px] text-foreground outline-none disabled:opacity-50" aria-label={`Breakpoint name ${breakpoint.id}`} />
                <div className="mt-1 grid grid-cols-2 gap-1"><SmallNumber label="Viewport width" value={breakpoint.viewportWidth} disabled={!canEdit} onChange={(value) => mutate((current) => updatePrototypeBreakpoint(current, breakpoint.id, { viewportWidth: value }))} /><SmallNumber label="Viewport height" value={breakpoint.viewportHeight} disabled={!canEdit} onChange={(value) => mutate((current) => updatePrototypeBreakpoint(current, breakpoint.id, { viewportHeight: value }))} /><SmallNumber label="Min width" value={breakpoint.minWidth} disabled={!canEdit} onChange={(value) => mutate((current) => updatePrototypeBreakpoint(current, breakpoint.id, { minWidth: value }))} /><label className="text-[7px] text-faint-foreground">Max width<input type="number" value={breakpoint.maxWidth ?? ''} onChange={(event) => mutate((current) => updatePrototypeBreakpoint(current, breakpoint.id, { maxWidth: event.target.value === '' ? undefined : Number(event.target.value) }))} disabled={!canEdit} className="mt-0.5 h-7 w-full rounded border border-border bg-panel px-1 font-mono text-[8px] text-foreground outline-none disabled:opacity-50" /></label></div>
              </div>
            )
          })}
        </div>
        {canEdit && <div className="mt-2 rounded border border-dashed border-border p-2"><input value={newBreakpointName} onChange={(event) => setNewBreakpointName(event.target.value)} className="h-7 w-full rounded border border-border bg-background px-1.5 text-[8px] text-foreground outline-none" placeholder="Custom breakpoint name" aria-label="New breakpoint name" /><div className="mt-1 grid grid-cols-2 gap-1"><SmallNumber label="Viewport width" value={newViewportWidth} onChange={setNewViewportWidth} /><SmallNumber label="Viewport height" value={newViewportHeight} onChange={setNewViewportHeight} /></div><button type="button" onClick={createBreakpoint} disabled={!newBreakpointName.trim()} className="mt-1.5 inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[8px] font-semibold text-primary-foreground disabled:opacity-35"><Plus className="size-3" />Add breakpoint with all frames</button></div>}
      </section>

      <section>
        <PanelLabel>Frame coverage</PanelLabel>
        <div className={cn('mt-2 rounded border p-2 text-[8px]', gaps.length === 0 ? 'border-success/30 bg-success/10 text-success' : 'border-warning/30 bg-warning/10 text-warning')}>{gaps.length === 0 ? `${content.states.length} × ${content.breakpoints.length} selectable frames are complete.` : `${gaps.length} state × breakpoint frame(s) are missing.`}</div>
        {canEdit && gaps.length > 0 && <button type="button" onClick={() => mutate((current) => repairPrototypeFrameCoverage(current, stableId))} className="mt-2 inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-warning text-[8px] font-semibold text-black"><RefreshCw className="size-3" />Repair frame coverage</button>}
      </section>
    </div>
  )
}

function SmallNumber({ label, value, disabled, onChange }: { label: string; value: number; disabled?: boolean; onChange: (value: number) => void }) {
  return <label className="text-[7px] text-faint-foreground">{label}<input type="number" value={value} onChange={(event) => onChange(Number(event.target.value) || 0)} disabled={disabled} className="mt-0.5 h-7 w-full rounded border border-border bg-panel px-1 font-mono text-[8px] text-foreground outline-none disabled:opacity-50" /></label>
}

function CanvasLayer({ layer, selected, onSelect, onPointerDown }: { layer: PrototypeLayerDto; selected: boolean; onSelect: () => void; onPointerDown: (event: ReactPointerEvent<HTMLButtonElement>) => void }) {
  const hidden = booleanValue(layer.properties.hidden)
  const locked = booleanValue(layer.properties.locked)
  if (hidden) return null
  const x = numberValue(layer.layout.x, 0)
  const y = numberValue(layer.layout.y, 0)
  const width = numberValue(layer.layout.width, 120)
  const height = numberValue(layer.layout.height, 44)
  const fill = stringValue(layer.style.fill, layer.kind === 'text' ? 'transparent' : '#1e1e21')
  const color = stringValue(layer.style.color, '#ffffff')
  const radius = numberValue(layer.style.borderRadius, 8)
  const opacity = numberValue(layer.style.opacity, 1)
  const text = stringValue(layer.properties.text, layer.name)
  return (
    <button type="button" onClick={(event) => { event.stopPropagation(); onSelect() }} onPointerDown={onPointerDown} className={cn('absolute overflow-hidden border text-left', selected ? 'z-20 border-primary shadow-[0_0_0_1px_rgba(20,136,252,.5)]' : 'border-white/10', locked ? 'cursor-default' : 'cursor-move')} style={{ left: x, top: y, width, height, background: fill, color, borderRadius: radius, opacity }} title={layer.name}>
      {layer.kind === 'text' || layer.kind === 'button' ? <span className={cn('flex h-full items-center', layer.kind === 'button' ? 'justify-center px-3 text-xs font-semibold' : 'px-1 font-semibold')} style={{ fontSize: numberValue(layer.style.fontSize, 16) }}>{text}</span> : layer.kind === 'input' ? <span className="flex h-full items-center px-3 text-xs text-white/45">{stringValue(layer.properties.placeholder, 'Input')}</span> : layer.kind === 'image' ? <span className="flex h-full items-center justify-center text-white/30"><ImageIcon className="size-8" /></span> : <span className="flex h-full items-center gap-3 px-4"><span className="size-8 rounded-full border border-white/10 bg-white/5" /><span className="flex-1 space-y-2"><span className="block h-2 w-2/3 rounded bg-white/15" /><span className="block h-2 w-1/2 rounded bg-white/8" /></span></span>}
      {selected && <span className="pointer-events-none absolute left-1 top-1 rounded bg-primary px-1 py-0.5 text-[7px] text-white">{layer.name}</span>}
    </button>
  )
}

function PropertiesPanel({ layer, rootLayerId, canEdit, onUpdate, onLayout, onStyle, onDuplicate, onDelete }: { layer?: PrototypeLayerDto; rootLayerId?: string; canEdit: boolean; onUpdate: (value: Partial<PrototypeLayerDto>) => void; onLayout: (value: Record<string, JsonValue>) => void; onStyle: (value: Record<string, JsonValue>) => void; onDuplicate: () => void; onDelete: () => void }) {
  if (!layer) return <PanelEmpty text="Select a layer on the canvas." />
  const locked = booleanValue(layer.properties.locked)
  const hidden = booleanValue(layer.properties.hidden)
  return <div className="space-y-4">
    <section><PanelLabel>Layer</PanelLabel><input value={layer.name} onChange={(event) => onUpdate({ name: event.target.value })} disabled={!canEdit} className="mt-2 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground outline-none disabled:opacity-50" /><div className="mt-2 grid grid-cols-4 gap-1"><IconButton icon={hidden ? EyeOff : Eye} label={hidden ? 'Show' : 'Hide'} onClick={() => onUpdate({ properties: { ...layer.properties, hidden: !hidden } })} disabled={!canEdit} /><IconButton icon={locked ? Unlock : Lock} label={locked ? 'Unlock' : 'Lock'} onClick={() => onUpdate({ properties: { ...layer.properties, locked: !locked } })} disabled={!canEdit} /><IconButton icon={FileClock} label="Duplicate" onClick={onDuplicate} disabled={!canEdit} /><IconButton icon={Trash2} label="Delete" onClick={onDelete} disabled={!canEdit || layer.id === rootLayerId} /></div></section>
    <section><PanelLabel>Layout</PanelLabel><div className="mt-2 grid grid-cols-2 gap-2"><NumberInput label="X" value={numberValue(layer.layout.x, 0)} onChange={(value) => onLayout({ x: value })} disabled={!canEdit || locked} /><NumberInput label="Y" value={numberValue(layer.layout.y, 0)} onChange={(value) => onLayout({ y: value })} disabled={!canEdit || locked} /><NumberInput label="Width" value={numberValue(layer.layout.width, 120)} onChange={(value) => onLayout({ width: Math.max(1, value) })} disabled={!canEdit || locked} /><NumberInput label="Height" value={numberValue(layer.layout.height, 44)} onChange={(value) => onLayout({ height: Math.max(1, value) })} disabled={!canEdit || locked} /></div></section>
    <section><PanelLabel>Style</PanelLabel><div className="mt-2 grid grid-cols-2 gap-2"><label className="text-[8px] text-faint-foreground">Fill<input type="color" value={normalizeColor(stringValue(layer.style.fill, '#1e1e21'))} onChange={(event) => onStyle({ fill: event.target.value })} disabled={!canEdit} className="mt-1 h-8 w-full rounded border border-border bg-background p-1" /></label><NumberInput label="Radius" value={numberValue(layer.style.borderRadius, 8)} onChange={(value) => onStyle({ borderRadius: Math.max(0, value) })} disabled={!canEdit} /></div></section>
    {(layer.kind === 'text' || layer.kind === 'button') && <section><PanelLabel>Content</PanelLabel><textarea value={stringValue(layer.properties.text, '')} onChange={(event) => onUpdate({ properties: { ...layer.properties, text: event.target.value } })} disabled={!canEdit} className="mt-2 h-20 w-full resize-none rounded border border-border bg-background p-2 text-[10px] text-foreground outline-none" /></section>}
    <section><PanelLabel>Stable identity</PanelLabel><div className="mt-2 rounded border border-border bg-background p-2 font-mono text-[8px] leading-relaxed text-faint-foreground">{layer.id}<br />role: {layer.semanticRole ?? 'unassigned'}<br />AI policy fields: {Object.keys(layer.fieldMetadata).length}</div></section>
  </div>
}

function DataPanel({ content, stateId, canEdit, onChange }: { content: PrototypeContentDto; stateId?: string; canEdit: boolean; onChange: (updater: (content: PrototypeContentDto) => PrototypeContentDto) => void }) {
  const [fixtureName, setFixtureName] = useState('Ready response')
  const [endpoint, setEndpoint] = useState('/api/resource')
  const [response, setResponse] = useState('{"items":[]}')
  const [fixtureError, setFixtureError] = useState<string | null>(null)
  function addFixture() {
    if (!stateId) return
    try {
      const parsed = JSON.parse(response) as JsonValue
      const id = stableId('fixture')
      const fixture: PrototypeFixtureDto = { id, name: fixtureName.trim() || 'Fixture', stateId, operationId: endpoint.trim(), response: parsed, statusCode: 200, latencyMs: 120, sanitized: true, contentHash: 'pending-server-hash' }
      onChange((current) => ({ ...current, fixtures: [...current.fixtures, fixture], states: current.states.map((item) => item.id === stateId ? { ...item, fixtureIds: [...item.fixtureIds, id] } : item) }))
      setFixtureError(null)
    } catch { setFixtureError('Fixture response must be valid JSON.') }
  }
  return <div className="space-y-4">
    <section><PanelLabel>State fixtures</PanelLabel><div className="mt-2 space-y-1.5">{content.fixtures.map((fixture) => <div key={fixture.id} className="rounded border border-border bg-background p-2"><div className="flex items-center gap-2"><Database className="size-3 text-primary-bright" /><span className="min-w-0 flex-1 truncate text-[9px] text-foreground">{fixture.name}</span><span className="font-mono text-[8px] text-faint-foreground">{fixture.statusCode}</span></div><div className="mt-1 truncate font-mono text-[8px] text-faint-foreground">{fixture.operationId ?? 'local fixture'} · {fixture.latencyMs}ms · {fixture.sanitized ? 'sanitized' : 'unsafe'}</div></div>)}{content.fixtures.length === 0 && <PanelEmpty text="No server fixture in this draft." />}</div></section>
    {canEdit && <section><PanelLabel>Add sanitized fixture</PanelLabel><div className="mt-2 space-y-1.5"><input value={fixtureName} onChange={(event) => setFixtureName(event.target.value)} className="h-8 w-full rounded border border-border bg-background px-2 text-[9px] text-foreground outline-none" placeholder="Fixture name" /><input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} className="h-8 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground outline-none" placeholder="operationId or endpoint" /><textarea value={response} onChange={(event) => setResponse(event.target.value)} className="h-24 w-full resize-none rounded border border-border bg-background p-2 font-mono text-[9px] text-foreground outline-none" />{fixtureError && <p className="text-[8px] text-destructive">{fixtureError}</p>}<button type="button" onClick={addFixture} className="inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[9px] font-semibold text-primary-foreground"><Plus className="size-3" />Add fixture to draft</button></div></section>}
    <section><PanelLabel>Interaction manifest</PanelLabel><div className="mt-2 grid grid-cols-2 gap-2"><Info label="Interactions" value={content.interactions.length} /><Info label="Overrides" value={content.overrides.length} /><Info label="Token bindings" value={content.tokenBindings.length} /><Info label="Components" value={content.componentBindings.length} /></div></section>
  </div>
}

function TracePanel({ resource, content, details, proposals, review, clientIssues, canReview, onRefresh }: { resource: VersionedArtifactDto<PrototypeContentDto>; content: PrototypeContentDto; details: Awaited<ReturnType<ReturnType<typeof useArtifactWorkspace>['loadDetails']>> | null; proposals: ReturnType<typeof useArtifactWorkspace>['proposals']; review?: ReturnType<typeof useCollaboration>['reviews'][number]; clientIssues: readonly string[]; canReview: boolean; onRefresh: () => void }) {
  return <div className="space-y-4">
    <section><div className="flex items-center justify-between"><PanelLabel>Exact source</PanelLabel><button type="button" onClick={onRefresh} className="rounded p-1 text-faint-foreground hover:text-foreground" aria-label="Refresh trace"><RefreshCw className="size-3" /></button></div><div className="mt-2 rounded border border-border bg-background p-2 font-mono text-[8px] leading-relaxed text-faint-foreground">PageSpec<br />{content.pageSpecRevision.artifactId}<br />{content.pageSpecRevision.revisionId}<br />{content.pageSpecRevision.contentHash}</div></section>
    <section><PanelLabel>Revision and dependency evidence</PanelLabel><div className="mt-2 grid grid-cols-2 gap-2"><Info label="Revisions" value={details?.versions.length ?? 0} /><Info label="Dependencies" value={details?.dependencies.length ?? 0} /><Info label="Trace links" value={content.traceLinks.length} /><Info label="Coverage" value={`${Math.round((details?.reviewGate.traceCoverage ?? 0) * 100)}%`} /></div></section>
    <PrototypeReviewGatePanel clientIssues={clientIssues} gate={details?.reviewGate} />
    <section><PanelLabel>Review gate</PanelLabel><div className={cn('mt-2 rounded border p-2 text-[9px]', review?.decision === 'approve' ? 'border-success/30 bg-success/10 text-success' : review?.decision === 'request_changes' ? 'border-destructive/30 bg-destructive/10 text-destructive' : 'border-warning/30 bg-warning/10 text-warning')}><div className="flex items-center gap-2"><CheckCircle2 className="size-3" /><span className="flex-1">{review?.decision ?? resource.artifact.status}</span></div><p className="mt-1 text-[8px] leading-relaxed opacity-80">{review?.summary ?? 'Create an immutable revision and request another project member to review it.'}</p></div>{canReview && <p className="mt-1 text-[8px] text-faint-foreground">Use Review Center for canonical reviewer decisions and blocking comment resolution.</p>}</section>
    <section><PanelLabel>AI output proposals</PanelLabel><div className="mt-2 space-y-1.5">{proposals.map((proposal) => <div key={proposal.id} className="rounded border border-border bg-background p-2"><div className="flex items-center gap-2"><Wand2 className="size-3 text-primary-bright" /><span className="min-w-0 flex-1 truncate font-mono text-[8px] text-foreground">{proposal.id}</span><span className="text-[8px] text-faint-foreground">{proposal.status}</span></div><p className="mt-1 text-[8px] text-faint-foreground">{proposal.operations.length} reviewable operation(s); apply through the proposal decision flow.</p></div>)}{proposals.length === 0 && <PanelEmpty text="No AI proposal targets this prototype." />}</div></section>
    <section><PanelLabel>Formal delivery readiness</PanelLabel><div className="mt-2 space-y-1"><Readiness passed={Boolean(resource.approvedRevision)} label="Approved immutable revision" /><Readiness passed={!content.exploratory} label="Formal, non-exploratory prototype" /><Readiness passed={content.states.some((item) => item.required)} label="Required state coverage" /><Readiness passed={content.breakpoints.length > 0} label="Responsive breakpoint" /><Readiness passed={content.fixtures.every((item) => item.sanitized)} label="Sanitized fixtures" /></div></section>
  </div>
}

function PrototypeReviewGatePanel({ clientIssues, gate }: { clientIssues: readonly string[]; gate?: ArtifactReviewGateDto }) {
  const serverIssues = reviewGateIssues(gate)
  const issues = [...clientIssues, ...serverIssues]
  const ready = clientIssues.length === 0 && reviewGateReadyForRequest(gate)
  return (
    <section>
      <PanelLabel>Revision request checks</PanelLabel>
      <div className={cn('mt-2 rounded border p-2', ready ? 'border-success/30 bg-success/10' : 'border-warning/30 bg-warning/10')}>
        <div className={cn('flex items-center gap-2 text-[9px] font-semibold', ready ? 'text-success' : 'text-warning')}>
          {ready ? <CheckCircle2 className="size-3" /> : <CircleAlert className="size-3" />}
          <span>{ready ? 'Current exact revision is ready for review request' : 'Current review request is blocked'}</span>
        </div>
        <p className="mt-1.5 text-[8px] leading-relaxed text-muted-foreground">A structurally valid draft can create its next immutable revision. These server checks are refreshed after creation and must pass before the review request is sent.</p>
        {!gate && <p className="mt-1.5 text-[8px] leading-relaxed text-warning">Waiting for the server review gate. Refresh the trace before requesting review.</p>}
        {issues.length > 0 && (
          <ol className="mt-2 space-y-1 text-[8px] leading-relaxed text-muted-foreground">
            {issues.map((issue, index) => <li key={`${issue}-${index}`} className="rounded border border-border/70 bg-background/70 px-2 py-1.5">{issue}</li>)}
          </ol>
        )}
        {gate && (
          <div className="mt-2 grid grid-cols-2 gap-1.5">
            <Info label="Trace coverage" value={`${Math.round(gate.traceCoverage * 100)}%`} />
            <Info label="Blocking comments" value={gate.unresolvedBlockingCommentIds.length} />
          </div>
        )}
      </div>
    </section>
  )
}

function StudioGate({ title, description, loading, onRetry }: { title: string; description: string; loading?: boolean; onRetry?: () => Promise<void> }) { return <div className="flex h-full items-center justify-center bg-canvas p-6"><div className="max-w-md rounded-xl border border-dashed border-border bg-panel p-7 text-center">{loading ? <LoaderCircle className="mx-auto mb-3 size-6 animate-spin text-primary-bright" /> : <MonitorSmartphone className="mx-auto mb-3 size-6 text-faint-foreground" />}<h2 className="text-sm font-semibold text-foreground">{title}</h2><p className="mt-2 text-[10px] leading-relaxed text-faint-foreground">{description}</p>{onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 inline-flex items-center gap-1 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground"><RefreshCw className="size-3" />Retry</button>}</div></div> }
function SaveIndicator({ state }: { state: SaveState }) { const config = state === 'saving' ? [LoaderCircle, 'Saving exact draft…', 'animate-spin text-primary-bright'] as const : state === 'dirty' ? [CircleDashed, 'Unsaved changes', 'text-warning'] as const : state === 'conflict' || state === 'error' ? [CircleAlert, state === 'conflict' ? 'Conflict' : 'Save failed', 'text-destructive'] as const : [CheckCircle2, state === 'saved' ? 'Saved' : 'Server draft', 'text-success'] as const; const Icon = config[0]; return <span className={cn('ml-1 inline-flex items-center gap-1 text-[8px]', config[2])}><Icon className={cn('size-3', state === 'saving' && 'animate-spin')} />{config[1]}</span> }
function PanelLabel({ children }: { children: React.ReactNode }) { return <h3 className="text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">{children}</h3> }
function PanelEmpty({ text }: { text: string }) { return <p className="rounded border border-dashed border-border p-3 text-center text-[8px] leading-relaxed text-faint-foreground">{text}</p> }
function Info({ label, value }: { label: string; value: string | number }) { return <div className="rounded border border-border bg-background p-2"><div className="text-[7px] uppercase tracking-wider text-faint-foreground">{label}</div><div className="mt-1 truncate text-[9px] font-medium text-muted-foreground">{value}</div></div> }
function Readiness({ passed, label }: { passed: boolean; label: string }) { return <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5 text-[8px] text-muted-foreground">{passed ? <CheckCircle2 className="size-3 text-success" /> : <CircleAlert className="size-3 text-warning" />}<span>{label}</span></div> }
function IconButton({ icon: Icon, label, onClick, disabled }: { icon: typeof Frame; label: string; onClick: () => void; disabled?: boolean }) { return <button type="button" onClick={onClick} disabled={disabled} className="flex h-12 flex-col items-center justify-center gap-1 rounded border border-border text-[7px] text-faint-foreground hover:text-foreground disabled:opacity-35"><Icon className="size-3" />{label}</button> }
function NumberInput({ label, value, onChange, disabled }: { label: string; value: number; onChange: (value: number) => void; disabled?: boolean }) { return <label className="text-[8px] text-faint-foreground">{label}<input type="number" value={value} onChange={(event) => onChange(Number(event.target.value) || 0)} disabled={disabled} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground outline-none disabled:opacity-40" /></label> }

function cloneContent(content: PrototypeContentDto): PrototypeContentDto { return typeof structuredClone === 'function' ? structuredClone(content) : JSON.parse(JSON.stringify(content)) as PrototypeContentDto }
function cloneLayer(layer: PrototypeLayerDto, id: string): PrototypeLayerDto { return { ...layer, id, childIds: [], layout: { ...layer.layout }, style: { ...layer.style }, properties: { ...layer.properties }, requirementIds: [...layer.requirementIds], acceptanceCriterionIds: [...layer.acceptanceCriterionIds], fieldMetadata: { ...layer.fieldMetadata } } }
function layerTree(layers: Readonly<Record<string, PrototypeLayerDto>>, rootId?: string) { if (!rootId || !layers[rootId]) return Object.values(layers); const result: PrototypeLayerDto[] = []; const visited = new Set<string>(); const visit = (id: string) => { if (visited.has(id) || !layers[id]) return; visited.add(id); result.push(layers[id]); layers[id].childIds.forEach(visit) }; visit(rootId); Object.keys(layers).forEach(visit); return result }
function descendantIds(layers: Readonly<Record<string, PrototypeLayerDto>>, id: string) { const result: string[] = []; const visit = (current: string) => { for (const child of layers[current]?.childIds ?? []) { result.push(child); visit(child) } }; visit(id); return result }
function fieldMetadataFor(updates: object, userId: string): PrototypeLayerDto['fieldMetadata'] { const now = new Date().toISOString(); const operationId = stableId('edit'); return Object.fromEntries(Object.keys(updates).map((field) => [field, { source: 'human' as const, changedBy: userId || 'anonymous', changedAt: now, operationId, aiPolicy: 'suggestOnly' as const }])) }
function numberValue(value: JsonValue | undefined, fallback: number) { return typeof value === 'number' && Number.isFinite(value) ? value : fallback }
function stringValue(value: JsonValue | undefined, fallback: string) { return typeof value === 'string' ? value : fallback }
function booleanValue(value: JsonValue | undefined) { return value === true }
function normalizeColor(value: string) { return /^#[0-9a-f]{6}$/i.test(value) ? value : '#1e1e21' }
function shortRef(ref: { artifactId: string; revisionId: string }) { return `${ref.artifactId.slice(0, 8)}:${ref.revisionId.slice(0, 8)}` }
function reviewGateIssues(gate?: ArtifactReviewGateDto) { return gate?.checks.filter((check) => check.severity === 'error' && check.code !== 'canonical_review_approved').map((check) => check.message) ?? [] }
function stableId(prefix: string) { const id = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`; return `${prefix}-${id}` }
function message(cause: unknown) { return cause instanceof Error ? cause.message : 'Prototype service request failed.' }
function artifactReference() { if (typeof window === 'undefined') return ''; return new URLSearchParams(window.location.search).get('artifactId') ?? '' }
