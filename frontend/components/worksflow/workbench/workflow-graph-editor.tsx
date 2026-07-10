'use client'

import { useEffect, useMemo, useState } from 'react'
import {
  ArrowRight,
  Braces,
  CirclePlus,
  GitBranch,
  Network,
  Save,
  Trash2,
} from 'lucide-react'
import type {
  WorkflowEdgeDto,
  WorkflowNodeDefinitionDto,
  WorkflowNodeType,
} from '@/lib/platform/flow-contract'
import { cn } from '@/lib/utils'

interface EditableWorkflowDefinition {
  readonly name: string
  readonly schemaVersion: string
  readonly nodes: readonly WorkflowNodeDefinitionDto[]
  readonly edges: readonly WorkflowEdgeDto[]
}

interface WorkflowGraphEditorProps {
  readonly value: string
  readonly onChange: (value: string) => void
}

const NODE_TYPES: readonly { readonly value: WorkflowNodeType; readonly label: string }[] = [
  { value: 'artifact_input', label: 'Artifact input' },
  { value: 'ai_transform', label: 'AI transform' },
  { value: 'human_edit', label: 'Human edit' },
  { value: 'review_gate', label: 'Review gate' },
  { value: 'condition', label: 'Condition' },
  { value: 'fan_out', label: 'Fan out' },
  { value: 'merge', label: 'Merge' },
  { value: 'manifest_compiler', label: 'Manifest compiler' },
  { value: 'workbench_build', label: 'Workbench build' },
  { value: 'quality_gate', label: 'Quality gate' },
  { value: 'publish', label: 'Publish' },
]

const NODE_WIDTH = 176
const NODE_HEIGHT = 72
const COLUMN_GAP = 70
const ROW_GAP = 34
const PADDING = 30

export function WorkflowGraphEditor({ value, onChange }: WorkflowGraphEditorProps) {
  const [mode, setMode] = useState<'graph' | 'json'>('graph')
  const [selectedNodeId, setSelectedNodeId] = useState('')
  const [newNodeType, setNewNodeType] = useState<WorkflowNodeType>('ai_transform')
  const [edgeFrom, setEdgeFrom] = useState('')
  const [edgeTo, setEdgeTo] = useState('')
  const [fromPort, setFromPort] = useState('default')
  const [toPort, setToPort] = useState('default')
  const [nodeDraft, setNodeDraft] = useState('')
  const [localError, setLocalError] = useState<string | null>(null)
  const parsed = useMemo(() => parseEditableDefinition(value), [value])
  const definition = parsed.definition
  const selectedNode = definition?.nodes.find((node) => node.id === selectedNodeId)

  useEffect(() => {
    if (!definition) return
    if (!selectedNodeId || !definition.nodes.some((node) => node.id === selectedNodeId)) {
      setSelectedNodeId(definition.nodes[0]?.id ?? '')
    }
    setEdgeFrom((current) => definition.nodes.some((node) => node.id === current)
      ? current
      : definition.nodes[0]?.id ?? '')
    setEdgeTo((current) => definition.nodes.some((node) => node.id === current)
      ? current
      : definition.nodes[1]?.id ?? definition.nodes[0]?.id ?? '')
  }, [definition, selectedNodeId])

  useEffect(() => {
    setNodeDraft(selectedNode ? JSON.stringify(selectedNode, null, 2) : '')
    setLocalError(null)
  }, [selectedNode])

  const fromNode = definition?.nodes.find((node) => node.id === edgeFrom)
  const toNode = definition?.nodes.find((node) => node.id === edgeTo)
  const outputPorts = resolvedPorts(fromNode, 'output')
  const inputPorts = resolvedPorts(toNode, 'input')

  useEffect(() => {
    if (!outputPorts.includes(fromPort)) setFromPort(outputPorts[0] ?? 'default')
  }, [fromPort, outputPorts])

  useEffect(() => {
    if (!inputPorts.includes(toPort)) setToPort(inputPorts[0] ?? 'default')
  }, [inputPorts, toPort])

  function commit(next: EditableWorkflowDefinition) {
    onChange(JSON.stringify(next, null, 2))
    setLocalError(null)
  }

  function addNode() {
    if (!definition) return
    const id = uniqueNodeId(newNodeType.replaceAll('_', '-'), definition.nodes)
    const node = createNode(id, newNodeType)
    commit({ ...definition, nodes: [...definition.nodes, node] })
    setSelectedNodeId(id)
  }

  function deleteNode(nodeId: string) {
    if (!definition) return
    commit({
      ...definition,
      nodes: definition.nodes.filter((node) => node.id !== nodeId),
      edges: definition.edges.filter((edge) => edge.from !== nodeId && edge.to !== nodeId),
    })
  }

  function applyNodeDraft() {
    if (!definition || !selectedNode) return
    try {
      const candidate = JSON.parse(nodeDraft) as unknown
      if (!isRecord(candidate)) throw new Error('Node must be a JSON object.')
      if (typeof candidate.id !== 'string' || !candidate.id.trim()) throw new Error('Node id is required.')
      if (typeof candidate.name !== 'string' || !candidate.name.trim()) throw new Error('Node name is required.')
      if (!NODE_TYPES.some((item) => item.value === candidate.type)) throw new Error('Choose a supported node type.')
      const nextId = candidate.id.trim()
      if (definition.nodes.some((node) => node.id === nextId && node.id !== selectedNode.id)) {
        throw new Error(`Node id ${nextId} already exists.`)
      }
      const nextNode = candidate as unknown as WorkflowNodeDefinitionDto
      commit({
        ...definition,
        nodes: definition.nodes.map((node) => node.id === selectedNode.id ? nextNode : node),
        edges: definition.edges.map((edge) => ({
          ...edge,
          from: edge.from === selectedNode.id ? nextId : edge.from,
          to: edge.to === selectedNode.id ? nextId : edge.to,
        })),
      })
      setSelectedNodeId(nextId)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : 'Node JSON is invalid.')
    }
  }

  function addEdge() {
    if (!definition || !edgeFrom || !edgeTo) return
    if (edgeFrom === edgeTo) {
      setLocalError('A node cannot connect to itself.')
      return
    }
    const duplicate = definition.edges.some((edge) =>
      edge.from === edgeFrom && edge.to === edgeTo &&
      (edge.fromPort || 'default') === fromPort && (edge.toPort || 'default') === toPort)
    if (duplicate) {
      setLocalError('That typed connection already exists.')
      return
    }
    const baseId = `${edgeFrom}-${fromPort}-${edgeTo}-${toPort}`
    const edge: WorkflowEdgeDto = {
      id: uniqueEdgeId(baseId, definition.edges),
      from: edgeFrom,
      ...(fromPort === 'default' ? {} : { fromPort }),
      to: edgeTo,
      ...(toPort === 'default' ? {} : { toPort }),
    }
    commit({ ...definition, edges: [...definition.edges, edge] })
  }

  if (mode === 'json') {
    return (
      <div>
        <EditorTabs mode={mode} onModeChange={setMode} graphDisabled={false} />
        <textarea
          value={value}
          onChange={(event) => onChange(event.target.value)}
          spellCheck={false}
          className="mt-2 h-[52vh] w-full resize-none rounded-lg border border-border bg-background p-3 font-mono text-[11px] leading-relaxed text-muted-foreground outline-none focus:border-primary/60"
          aria-label="Workflow definition JSON"
        />
        {parsed.error && <p className="mt-2 text-[10px] text-destructive">{parsed.error}</p>}
      </div>
    )
  }

  if (!definition) {
    return (
      <div>
        <EditorTabs mode={mode} onModeChange={setMode} graphDisabled />
        <div className="mt-2 rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-[11px] text-destructive">
          {parsed.error}. Open JSON to repair the definition.
        </div>
      </div>
    )
  }

  const layout = graphLayout(definition)

  return (
    <div>
      <EditorTabs mode={mode} onModeChange={setMode} graphDisabled={false} />
      <div className="mt-2 grid min-h-[52vh] grid-cols-[minmax(0,1fr)_280px] overflow-hidden rounded-lg border border-border max-lg:grid-cols-1">
        <div className="flex min-h-0 flex-col bg-background">
          <div className="flex flex-wrap items-center gap-1.5 border-b border-border p-2">
            <select value={newNodeType} onChange={(event) => setNewNodeType(event.target.value as WorkflowNodeType)} className="h-8 min-w-40 rounded border border-border bg-panel px-2 text-[10px] text-foreground">
              {NODE_TYPES.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
            </select>
            <button type="button" onClick={addNode} className="inline-flex h-8 items-center gap-1 rounded bg-primary px-2.5 text-[10px] font-semibold text-primary-foreground"><CirclePlus className="size-3" /> Add node</button>
            <span className="ml-auto text-[9px] text-faint-foreground">{definition.nodes.length} nodes · {definition.edges.length} edges</span>
          </div>

          <div className="min-h-[300px] flex-1 overflow-auto scrollbar-thin">
            <div className="relative" style={{ width: layout.width, height: layout.height }}>
              <svg className="absolute inset-0" width={layout.width} height={layout.height} aria-label="Workflow graph connections">
                <defs>
                  <marker id="workflow-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse">
                    <path d="M 0 0 L 10 5 L 0 10 z" className="fill-primary/70" />
                  </marker>
                </defs>
                {definition.edges.map((edge) => {
                  const from = layout.positions.get(edge.from)
                  const to = layout.positions.get(edge.to)
                  if (!from || !to) return null
                  const startX = from.x + NODE_WIDTH
                  const startY = from.y + NODE_HEIGHT / 2
                  const endX = to.x
                  const endY = to.y + NODE_HEIGHT / 2
                  const bend = Math.max(32, Math.abs(endX - startX) / 2)
                  return <path key={edge.id} d={`M ${startX} ${startY} C ${startX + bend} ${startY}, ${endX - bend} ${endY}, ${endX} ${endY}`} fill="none" className="stroke-primary/55" strokeWidth="1.5" markerEnd="url(#workflow-arrow)" />
                })}
              </svg>
              {definition.nodes.map((node) => {
                const position = layout.positions.get(node.id) ?? { x: PADDING, y: PADDING }
                const incoming = definition.edges.filter((edge) => edge.to === node.id).length
                const outgoing = definition.edges.filter((edge) => edge.from === node.id).length
                return (
                  <button
                    key={node.id}
                    type="button"
                    onClick={() => setSelectedNodeId(node.id)}
                    className={cn(
                      'absolute rounded-lg border p-2 text-left shadow-md transition-colors',
                      selectedNodeId === node.id
                        ? 'border-primary bg-primary/15 ring-1 ring-primary/30'
                        : 'border-border bg-panel hover:border-primary/40',
                    )}
                    style={{ left: position.x, top: position.y, width: NODE_WIDTH, height: NODE_HEIGHT }}
                  >
                    <span className="block truncate text-[10px] font-semibold text-foreground">{node.name}</span>
                    <span className="mt-1 block truncate font-mono text-[8px] text-primary-bright">{node.type}</span>
                    <span className="mt-1 flex justify-between text-[8px] text-faint-foreground"><span>{incoming} in</span><span>{node.id}</span><span>{outgoing} out</span></span>
                  </button>
                )
              })}
            </div>
          </div>

          <div className="border-t border-border p-2">
            <div className="flex flex-wrap items-end gap-1.5">
              <NodePortSelect label="From" nodeId={edgeFrom} port={fromPort} nodes={definition.nodes} direction="output" onNodeChange={setEdgeFrom} onPortChange={setFromPort} />
              <ArrowRight className="mb-2 size-3.5 text-faint-foreground" />
              <NodePortSelect label="To" nodeId={edgeTo} port={toPort} nodes={definition.nodes} direction="input" onNodeChange={setEdgeTo} onPortChange={setToPort} />
              <button type="button" onClick={addEdge} disabled={definition.nodes.length < 2} className="mb-0 inline-flex h-8 items-center gap-1 rounded border border-primary/40 bg-primary/10 px-2 text-[9px] font-medium text-primary-bright disabled:opacity-35"><GitBranch className="size-3" /> Connect</button>
            </div>
            <div className="mt-2 flex max-h-20 flex-wrap gap-1 overflow-y-auto scrollbar-thin">
              {definition.edges.map((edge) => (
                <span key={edge.id} className="inline-flex items-center gap-1 rounded border border-border bg-panel px-1.5 py-1 font-mono text-[8px] text-muted-foreground">
                  {edge.from}:{edge.fromPort || 'default'} → {edge.to}:{edge.toPort || 'default'}
                  <button type="button" onClick={() => commit({ ...definition, edges: definition.edges.filter((item) => item.id !== edge.id) })} className="text-faint-foreground hover:text-destructive" aria-label={`Delete edge ${edge.id}`}><Trash2 className="size-2.5" /></button>
                </span>
              ))}
            </div>
          </div>
        </div>

        <aside className="min-h-0 border-l border-border bg-panel p-3 max-lg:border-l-0 max-lg:border-t">
          {selectedNode ? (
            <>
              <div className="flex items-center gap-2">
                <Network className="size-3.5 text-primary-bright" />
                <div className="min-w-0 flex-1"><div className="truncate text-[10px] font-semibold text-foreground">Node contract</div><div className="truncate font-mono text-[8px] text-faint-foreground">{selectedNode.id}</div></div>
                <button type="button" onClick={() => deleteNode(selectedNode.id)} className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive" aria-label="Delete selected node"><Trash2 className="size-3.5" /></button>
              </div>
              <p className="mt-2 text-[9px] leading-relaxed text-faint-foreground">Edit the typed config, schemas, and named ports. Renaming an id updates connected edges.</p>
              <textarea value={nodeDraft} onChange={(event) => setNodeDraft(event.target.value)} spellCheck={false} className="mt-2 h-[34vh] min-h-56 w-full resize-none rounded border border-border bg-background p-2 font-mono text-[9px] leading-relaxed text-muted-foreground outline-none focus:border-primary/60" aria-label="Selected workflow node JSON" />
              <button type="button" onClick={applyNodeDraft} className="mt-2 inline-flex h-8 w-full items-center justify-center gap-1 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground"><Save className="size-3" /> Apply node contract</button>
            </>
          ) : (
            <p className="text-[10px] text-faint-foreground">Add or select a node to edit its typed contract.</p>
          )}
          {localError && <p role="alert" className="mt-2 text-[9px] leading-relaxed text-destructive">{localError}</p>}
        </aside>
      </div>
    </div>
  )
}

function EditorTabs({ mode, graphDisabled, onModeChange }: { readonly mode: 'graph' | 'json'; readonly graphDisabled: boolean; readonly onModeChange: (mode: 'graph' | 'json') => void }) {
  return (
    <div className="inline-flex rounded-md border border-border bg-background p-0.5">
      <button type="button" onClick={() => onModeChange('graph')} disabled={graphDisabled} className={cn('inline-flex h-7 items-center gap-1 rounded px-2 text-[9px]', mode === 'graph' ? 'bg-primary/15 text-primary-bright' : 'text-faint-foreground', 'disabled:opacity-35')}><Network className="size-3" /> Graph</button>
      <button type="button" onClick={() => onModeChange('json')} className={cn('inline-flex h-7 items-center gap-1 rounded px-2 text-[9px]', mode === 'json' ? 'bg-primary/15 text-primary-bright' : 'text-faint-foreground')}><Braces className="size-3" /> JSON</button>
    </div>
  )
}

function NodePortSelect({ label, nodeId, port, nodes, direction, onNodeChange, onPortChange }: { readonly label: string; readonly nodeId: string; readonly port: string; readonly nodes: readonly WorkflowNodeDefinitionDto[]; readonly direction: 'input' | 'output'; readonly onNodeChange: (value: string) => void; readonly onPortChange: (value: string) => void }) {
  const node = nodes.find((item) => item.id === nodeId)
  const ports = resolvedPorts(node, direction)
  return (
    <div className="min-w-0 flex-1">
      <span className="mb-1 block text-[8px] uppercase tracking-wider text-faint-foreground">{label}</span>
      <div className="flex gap-1">
        <select value={nodeId} onChange={(event) => onNodeChange(event.target.value)} className="h-8 min-w-0 flex-1 rounded border border-border bg-panel px-1.5 text-[9px] text-foreground">{nodes.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
        <select value={port} onChange={(event) => onPortChange(event.target.value)} className="h-8 w-24 rounded border border-border bg-panel px-1 text-[9px] text-foreground">{ports.map((item) => <option key={item} value={item}>{item}</option>)}</select>
      </div>
    </div>
  )
}

function parseEditableDefinition(value: string): { readonly definition?: EditableWorkflowDefinition; readonly error?: string } {
  try {
    const parsed = JSON.parse(value) as unknown
    if (!isRecord(parsed) || typeof parsed.name !== 'string' || typeof parsed.schemaVersion !== 'string' || !Array.isArray(parsed.nodes) || !Array.isArray(parsed.edges)) {
      return { error: 'Definition requires name, schemaVersion, nodes, and edges' }
    }
    return { definition: parsed as unknown as EditableWorkflowDefinition }
  } catch (cause) {
    return { error: cause instanceof Error ? cause.message : 'Definition JSON is invalid' }
  }
}

function createNode(id: string, type: WorkflowNodeType): WorkflowNodeDefinitionDto {
  const schema = { type: 'object', additionalProperties: true } as const
  const base = { id, name: NODE_TYPES.find((item) => item.value === type)?.label ?? type, type, inputSchema: schema, outputSchema: schema }
  switch (type) {
    case 'artifact_input': return { ...base, artifactInput: { allowedTypes: ['document'], requireApproved: true, minimumArtifacts: 1 } }
    case 'ai_transform': return { ...base, aiTransform: { jobType: 'custom_transform', modelPolicy: 'default', outputSchemaVersion: 'artifact/v1', maxAttempts: 2, timeout: 120_000_000_000 } }
    case 'human_edit': return { ...base, humanEdit: { artifactType: 'document', requiredRole: 'editor', instructions: 'Submit an exact immutable revision.' } }
    case 'review_gate': return { ...base, reviewGate: { requiredRole: 'admin', minimumApprovals: 1, prohibitSelfReview: true, allowWaiver: false } }
    case 'condition': return { ...base, outputPorts: { yes: { schema }, otherwise: { schema } }, condition: { branches: [{ name: 'yes', expression: 'true', default: false }, { name: 'otherwise', default: true }] } }
    case 'fan_out': return { ...base, fanOut: { itemsPath: '/items', sliceKeyPath: '/id', mergeNodeId: 'merge', maxParallel: 4 } }
    case 'merge': return { ...base, merge: { fanOutNodeId: 'fan-out', policy: 'all', allowWaiver: false } }
    case 'manifest_compiler': return { ...base, manifestCompiler: { manifestKind: 'application_build', schemaVersion: 1, hook: 'v1' } }
    case 'workbench_build': return { ...base, workbenchBuild: { buildManifestSchemaVersion: 1, maxAttempts: 2, timeout: 300_000_000_000 } }
    case 'quality_gate': return { ...base, qualityGate: { gateName: 'application_quality', blocking: true } }
    case 'publish': return { ...base, publish: { environment: 'preview', requiredRole: 'admin', allowRollback: true } }
  }
}

function resolvedPorts(node: WorkflowNodeDefinitionDto | undefined, direction: 'input' | 'output') {
  if (!node) return ['default']
  const explicit = direction === 'input' ? node.inputPorts : node.outputPorts
  const names = explicit ? Object.keys(explicit) : []
  return names.length > 0 ? names : ['default']
}

function uniqueNodeId(base: string, nodes: readonly WorkflowNodeDefinitionDto[]) {
  let candidate = base
  let index = 2
  while (nodes.some((node) => node.id === candidate)) candidate = `${base}-${index++}`
  return candidate
}

function uniqueEdgeId(base: string, edges: readonly WorkflowEdgeDto[]) {
  const normalized = base.replace(/[^a-zA-Z0-9_-]+/g, '-').slice(0, 120)
  let candidate = normalized
  let index = 2
  while (edges.some((edge) => edge.id === candidate)) candidate = `${normalized}-${index++}`
  return candidate
}

function graphLayout(definition: EditableWorkflowDefinition) {
  const incoming = new Map(definition.nodes.map((node) => [node.id, 0]))
  const outgoing = new Map(definition.nodes.map((node) => [node.id, [] as string[]]))
  definition.edges.forEach((edge) => {
    if (!incoming.has(edge.to) || !outgoing.has(edge.from)) return
    incoming.set(edge.to, (incoming.get(edge.to) ?? 0) + 1)
    outgoing.get(edge.from)?.push(edge.to)
  })
  const levels = new Map<string, number>()
  const queue = definition.nodes.filter((node) => (incoming.get(node.id) ?? 0) === 0).map((node) => node.id)
  queue.forEach((id) => levels.set(id, 0))
  const remaining = new Map(incoming)
  while (queue.length > 0) {
    const current = queue.shift()!
    for (const next of outgoing.get(current) ?? []) {
      levels.set(next, Math.max(levels.get(next) ?? 0, (levels.get(current) ?? 0) + 1))
      remaining.set(next, (remaining.get(next) ?? 1) - 1)
      if (remaining.get(next) === 0) queue.push(next)
    }
  }
  definition.nodes.forEach((node, index) => {
    if (!levels.has(node.id)) levels.set(node.id, index)
  })
  const columns = new Map<number, WorkflowNodeDefinitionDto[]>()
  definition.nodes.forEach((node) => {
    const level = levels.get(node.id) ?? 0
    columns.set(level, [...(columns.get(level) ?? []), node])
  })
  const positions = new Map<string, { readonly x: number; readonly y: number }>()
  let maxRows = 1
  columns.forEach((nodes, level) => {
    maxRows = Math.max(maxRows, nodes.length)
    nodes.forEach((node, row) => positions.set(node.id, {
      x: PADDING + level * (NODE_WIDTH + COLUMN_GAP),
      y: PADDING + row * (NODE_HEIGHT + ROW_GAP),
    }))
  })
  const maxLevel = Math.max(0, ...levels.values())
  return {
    positions,
    width: Math.max(620, PADDING * 2 + (maxLevel + 1) * NODE_WIDTH + maxLevel * COLUMN_GAP),
    height: Math.max(320, PADDING * 2 + maxRows * NODE_HEIGHT + (maxRows - 1) * ROW_GAP),
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
