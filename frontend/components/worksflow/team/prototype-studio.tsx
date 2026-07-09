'use client'

import { useMemo, useState, type CSSProperties } from 'react'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import type { DocType } from '@/lib/worksflow/types'
import { useLocalizedLabels } from '../use-localized-labels'
import { clamp, cloneItems, parsedNumber } from './prototype-studio-helpers'
import {
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  ArrowUpRight,
  Badge,
  Box,
  Braces,
  CheckCircle2,
  Component,
  Copy,
  Database,
  Download,
  Eye,
  EyeOff,
  FileCode2,
  FormInput,
  Frame,
  Grid3X3,
  History,
  ImageIcon,
  Layers,
  LayoutTemplate,
  Link2,
  ListTree,
  Lock,
  Maximize2,
  Menu,
  MonitorSmartphone,
  MousePointer2,
  Move,
  PanelRight,
  Play,
  Plus,
  RotateCcw,
  Save,
  Search,
  Settings2,
  Shapes,
  SlidersHorizontal,
  Sparkles,
  Table2,
  TabletSmartphone,
  ToggleLeft,
  Type,
  Unlock,
  Wand2,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'

type StateKey = 'empty' | 'loading' | 'ready' | 'error'
type PrototypeMode = 'wireframe' | 'design' | 'component' | 'handoff'
type StudioPanel = 'properties' | 'data' | 'handoff'
type DevicePreset = 'desktop' | 'tablet' | 'mobile'
type LayerKind = 'frame' | 'text' | 'card' | 'button' | 'input' | 'image' | 'badge'
type UiLibraryId = 'shadcn' | 'mui' | 'ant' | 'chakra' | 'headless' | 'custom'

interface PrototypeLayer {
  id: string
  name: string
  kind: LayerKind
  x: number
  y: number
  w: number
  h: number
  radius: number
  opacity: number
  rotation: number
  fill: string
  stroke: string
  text?: string
  textSize?: number
  cropX?: number
  cropY?: number
  imageScale?: number
  brightness?: number
  contrast?: number
  saturation?: number
  blur?: number
  visible: boolean
  locked: boolean
}

interface PrototypePage {
  id: string
  name: string
  docType: DocType
  states: StateKey[]
  owner: string
  updatedAt: string
  viewport: { width: number; height: number }
  layers: PrototypeLayer[]
  componentStories: string[]
  sourceDocs: string[]
  apiContract: string
}

interface MockFixture {
  id: string
  state: StateKey
  endpoint: string
  fixture: string
  method: 'GET' | 'POST'
  status: number
  latency: number
  rows: number
  schema: string
  sample: string
}

interface UiLibrary {
  id: UiLibraryId
  name: string
  descriptionKey: MessageKey
  token: string
}

interface ComponentTemplate {
  id: string
  libraryId: UiLibraryId
  name: string
  category: string
  kind: LayerKind
  width: number
  height: number
  fill?: string
  stroke?: string
  text?: string
  icon: typeof Frame
}

const STATE_ORDER: StateKey[] = ['empty', 'loading', 'ready', 'error']

const STATE_LABEL_KEY: Record<StateKey, MessageKey> = {
  empty: 'prototype.emptyState',
  loading: 'prototype.loadingState',
  ready: 'prototype.readyState',
  error: 'prototype.errorState',
}

const MODE_LABEL_KEY: Record<PrototypeMode, MessageKey> = {
  wireframe: 'prototype.mode.wireframe',
  design: 'prototype.mode.design',
  component: 'prototype.mode.component',
  handoff: 'prototype.mode.handoff',
}

const PANEL_LABEL_KEY: Record<StudioPanel, MessageKey> = {
  properties: 'prototype.panel.properties',
  data: 'prototype.panel.data',
  handoff: 'prototype.panel.handoff',
}

const DEVICE_PRESETS: Record<
  DevicePreset,
  { labelKey: MessageKey; width: number; height: number; icon: typeof MonitorSmartphone }
> = {
  desktop: { labelKey: 'prototype.device.desktop', width: 430, height: 560, icon: MonitorSmartphone },
  tablet: { labelKey: 'prototype.device.tablet', width: 390, height: 540, icon: TabletSmartphone },
  mobile: { labelKey: 'prototype.device.mobile', width: 320, height: 560, icon: MonitorSmartphone },
}

const MODE_ICON: Record<PrototypeMode, typeof Frame> = {
  wireframe: Frame,
  design: ImageIcon,
  component: Component,
  handoff: PanelRight,
}

const PANEL_ICON: Record<StudioPanel, typeof SlidersHorizontal> = {
  properties: SlidersHorizontal,
  data: Database,
  handoff: FileCode2,
}

const LAYER_ICON: Record<LayerKind, typeof Frame> = {
  frame: Frame,
  text: MousePointer2,
  card: Layers,
  button: Sparkles,
  input: Maximize2,
  image: ImageIcon,
  badge: Component,
}

const COLOR_SWATCHES = [
  '#1488fc',
  '#2ba6ff',
  '#4ade80',
  '#fbbf24',
  '#ef4444',
  '#1e1e21',
  '#26262a',
  '#ffffff',
]

const UI_LIBRARIES: UiLibrary[] = [
  { id: 'shadcn', name: 'shadcn/ui', descriptionKey: 'prototype.library.shadcn', token: 'Radix + Tailwind' },
  { id: 'mui', name: 'Material UI', descriptionKey: 'prototype.library.mui', token: 'Material Design' },
  { id: 'ant', name: 'Ant Design', descriptionKey: 'prototype.library.ant', token: 'Enterprise' },
  { id: 'chakra', name: 'Chakra UI', descriptionKey: 'prototype.library.chakra', token: 'Composable' },
  { id: 'headless', name: 'Headless UI', descriptionKey: 'prototype.library.headless', token: 'Unstyled' },
  { id: 'custom', name: 'Custom', descriptionKey: 'prototype.library.custom', token: 'Team system' },
]

const COMPONENT_TEMPLATES: ComponentTemplate[] = [
  {
    id: 'shadcn-card',
    libraryId: 'shadcn',
    name: 'Card',
    category: 'Layout',
    kind: 'card',
    width: 280,
    height: 92,
    fill: '#1e1e21',
    icon: LayoutTemplate,
  },
  {
    id: 'shadcn-button',
    libraryId: 'shadcn',
    name: 'Button',
    category: 'Actions',
    kind: 'button',
    width: 128,
    height: 42,
    fill: '#1488fc',
    text: 'Submit',
    icon: Sparkles,
  },
  {
    id: 'shadcn-input',
    libraryId: 'shadcn',
    name: 'Input',
    category: 'Forms',
    kind: 'input',
    width: 240,
    height: 44,
    fill: '#26262a',
    icon: FormInput,
  },
  {
    id: 'shadcn-tabs',
    libraryId: 'shadcn',
    name: 'Tabs',
    category: 'Navigation',
    kind: 'input',
    width: 220,
    height: 40,
    fill: '#26262a',
    icon: ListTree,
  },
  {
    id: 'mui-appbar',
    libraryId: 'mui',
    name: 'AppBar',
    category: 'Navigation',
    kind: 'frame',
    width: 340,
    height: 54,
    fill: '#1976d2',
    icon: Menu,
  },
  {
    id: 'mui-card',
    libraryId: 'mui',
    name: 'Paper Card',
    category: 'Surface',
    kind: 'card',
    width: 300,
    height: 96,
    fill: '#202124',
    icon: Box,
  },
  {
    id: 'mui-switch',
    libraryId: 'mui',
    name: 'Switch Row',
    category: 'Forms',
    kind: 'input',
    width: 240,
    height: 48,
    fill: '#26262a',
    icon: ToggleLeft,
  },
  {
    id: 'ant-table',
    libraryId: 'ant',
    name: 'Table',
    category: 'Data Display',
    kind: 'card',
    width: 360,
    height: 150,
    fill: '#1e1e21',
    icon: Table2,
  },
  {
    id: 'ant-form',
    libraryId: 'ant',
    name: 'Form Item',
    category: 'Data Entry',
    kind: 'input',
    width: 300,
    height: 56,
    fill: '#26262a',
    icon: FormInput,
  },
  {
    id: 'ant-tag',
    libraryId: 'ant',
    name: 'Tag',
    category: 'Feedback',
    kind: 'badge',
    width: 110,
    height: 30,
    fill: 'rgba(20,136,252,0.12)',
    text: 'Active',
    icon: Badge,
  },
  {
    id: 'chakra-stack',
    libraryId: 'chakra',
    name: 'Stack',
    category: 'Layout',
    kind: 'frame',
    width: 260,
    height: 120,
    fill: '#171719',
    icon: Layers,
  },
  {
    id: 'chakra-alert',
    libraryId: 'chakra',
    name: 'Alert',
    category: 'Feedback',
    kind: 'card',
    width: 300,
    height: 64,
    fill: 'rgba(74,222,128,0.12)',
    stroke: 'rgba(74,222,128,0.35)',
    icon: Component,
  },
  {
    id: 'chakra-badge',
    libraryId: 'chakra',
    name: 'Badge',
    category: 'Data Display',
    kind: 'badge',
    width: 104,
    height: 30,
    fill: 'rgba(251,191,36,0.12)',
    text: 'New',
    icon: Badge,
  },
  {
    id: 'headless-combobox',
    libraryId: 'headless',
    name: 'Combobox',
    category: 'Forms',
    kind: 'input',
    width: 280,
    height: 44,
    fill: '#26262a',
    icon: Search,
  },
  {
    id: 'headless-dialog',
    libraryId: 'headless',
    name: 'Dialog',
    category: 'Overlay',
    kind: 'card',
    width: 320,
    height: 180,
    fill: '#1e1e21',
    icon: PanelRight,
  },
  {
    id: 'headless-menu',
    libraryId: 'headless',
    name: 'Menu',
    category: 'Navigation',
    kind: 'card',
    width: 180,
    height: 132,
    fill: '#1e1e21',
    icon: Menu,
  },
]

function layer(input: Partial<PrototypeLayer> & Pick<PrototypeLayer, 'id' | 'name' | 'kind' | 'x' | 'y' | 'w' | 'h'>): PrototypeLayer {
  return {
    radius: 8,
    opacity: 100,
    rotation: 0,
    fill: '#1e1e21',
    stroke: 'rgba(255,255,255,0.12)',
    textSize: 14,
    cropX: 50,
    cropY: 50,
    imageScale: 118,
    brightness: 100,
    contrast: 100,
    saturation: 100,
    blur: 0,
    visible: true,
    locked: false,
    ...input,
  }
}

const PAGES: PrototypePage[] = [
  {
    id: 'p1',
    name: '/tasks',
    docType: 'uiPrototype',
    states: ['empty', 'loading', 'ready', 'error'],
    owner: 'Mia Chen',
    updatedAt: '40m ago',
    viewport: { width: 430, height: 560 },
    sourceDocs: ['CRM requirement v3', 'Feature list draft', 'UI state matrix'],
    apiContract: 'GET /api/tasks',
    componentStories: ['TaskCard / ready', 'TaskCard / completed', 'FilterBar / active', 'EmptyState'],
    layers: [
      layer({
        id: 'p1-shell',
        name: 'App frame',
        kind: 'frame',
        x: 0,
        y: 0,
        w: 430,
        h: 560,
        radius: 18,
        fill: '#171719',
        locked: true,
      }),
      layer({
        id: 'p1-hero',
        name: 'Header image',
        kind: 'image',
        x: 24,
        y: 24,
        w: 382,
        h: 132,
        radius: 14,
        stroke: 'rgba(43,166,255,0.35)',
        brightness: 92,
        contrast: 112,
        saturation: 118,
      }),
      layer({
        id: 'p1-title',
        name: 'Page title',
        kind: 'text',
        x: 32,
        y: 178,
        w: 190,
        h: 30,
        fill: 'transparent',
        stroke: 'transparent',
        text: 'Tasks',
        textSize: 24,
      }),
      layer({
        id: 'p1-filter',
        name: 'Filter segmented control',
        kind: 'input',
        x: 248,
        y: 176,
        w: 148,
        h: 36,
        radius: 10,
        fill: '#26262a',
      }),
      layer({
        id: 'p1-card-1',
        name: 'High priority task',
        kind: 'card',
        x: 24,
        y: 236,
        w: 382,
        h: 76,
        fill: '#1e1e21',
      }),
      layer({
        id: 'p1-card-2',
        name: 'Design review task',
        kind: 'card',
        x: 24,
        y: 326,
        w: 382,
        h: 76,
        fill: '#1e1e21',
      }),
      layer({
        id: 'p1-cta',
        name: 'Add task button',
        kind: 'button',
        x: 270,
        y: 488,
        w: 136,
        h: 44,
        radius: 12,
        fill: '#1488fc',
        stroke: 'rgba(20,136,252,0.65)',
        text: 'New task',
      }),
      layer({
        id: 'p1-badge',
        name: 'Sync badge',
        kind: 'badge',
        x: 32,
        y: 492,
        w: 112,
        h: 34,
        radius: 999,
        fill: 'rgba(74,222,128,0.12)',
        stroke: 'rgba(74,222,128,0.35)',
        text: '4 states',
      }),
    ],
  },
  {
    id: 'p2',
    name: '/tasks/:id',
    docType: 'uiPrototype',
    states: ['loading', 'ready', 'error'],
    owner: 'Noah Kim',
    updatedAt: '1h ago',
    viewport: { width: 430, height: 560 },
    sourceDocs: ['Task detail requirement', 'Activity feed contract'],
    apiContract: 'GET /api/tasks/:id',
    componentStories: ['TaskDetail / ready', 'AssigneeMenu / open', 'ActivityFeed / loading'],
    layers: [
      layer({
        id: 'p2-shell',
        name: 'Detail frame',
        kind: 'frame',
        x: 0,
        y: 0,
        w: 430,
        h: 560,
        radius: 18,
        fill: '#171719',
        locked: true,
      }),
      layer({
        id: 'p2-title',
        name: 'Detail header',
        kind: 'text',
        x: 28,
        y: 34,
        w: 260,
        h: 32,
        fill: 'transparent',
        stroke: 'transparent',
        text: 'Review roadmap',
        textSize: 22,
      }),
      layer({
        id: 'p2-status',
        name: 'Status badge',
        kind: 'badge',
        x: 292,
        y: 34,
        w: 104,
        h: 30,
        fill: 'rgba(251,191,36,0.12)',
        stroke: 'rgba(251,191,36,0.35)',
        text: 'High',
      }),
      layer({
        id: 'p2-cover',
        name: 'Activity snapshot',
        kind: 'image',
        x: 24,
        y: 96,
        w: 382,
        h: 164,
        radius: 14,
        brightness: 98,
        contrast: 106,
        saturation: 92,
      }),
      layer({
        id: 'p2-form',
        name: 'Edit form',
        kind: 'input',
        x: 24,
        y: 286,
        w: 382,
        h: 132,
        radius: 14,
        fill: '#1e1e21',
      }),
      layer({
        id: 'p2-save',
        name: 'Save change button',
        kind: 'button',
        x: 250,
        y: 486,
        w: 156,
        h: 44,
        radius: 12,
        fill: '#1488fc',
        text: 'Save changes',
      }),
    ],
  },
  {
    id: 'p3',
    name: '/members',
    docType: 'pageSplit',
    states: ['empty', 'ready'],
    owner: 'Ava Patel',
    updatedAt: 'Yesterday',
    viewport: { width: 430, height: 560 },
    sourceDocs: ['Member roles doc', 'Permission matrix'],
    apiContract: 'GET /api/members',
    componentStories: ['MemberCard / owner', 'RolePicker / editor', 'InvitePanel / empty'],
    layers: [
      layer({
        id: 'p3-shell',
        name: 'Members frame',
        kind: 'frame',
        x: 0,
        y: 0,
        w: 430,
        h: 560,
        radius: 18,
        fill: '#171719',
        locked: true,
      }),
      layer({
        id: 'p3-title',
        name: 'Team title',
        kind: 'text',
        x: 28,
        y: 34,
        w: 220,
        h: 32,
        fill: 'transparent',
        stroke: 'transparent',
        text: 'Members',
        textSize: 22,
      }),
      layer({
        id: 'p3-search',
        name: 'Member search',
        kind: 'input',
        x: 28,
        y: 88,
        w: 374,
        h: 42,
        radius: 12,
        fill: '#26262a',
      }),
      layer({
        id: 'p3-member-1',
        name: 'Owner member card',
        kind: 'card',
        x: 24,
        y: 158,
        w: 382,
        h: 86,
        fill: '#1e1e21',
      }),
      layer({
        id: 'p3-member-2',
        name: 'Reviewer member card',
        kind: 'card',
        x: 24,
        y: 260,
        w: 382,
        h: 86,
        fill: '#1e1e21',
      }),
      layer({
        id: 'p3-invite',
        name: 'Invite button',
        kind: 'button',
        x: 268,
        y: 488,
        w: 138,
        h: 44,
        radius: 12,
        fill: '#1488fc',
        text: 'Invite',
      }),
    ],
  },
]

const HANDOFF_PINS = [
  { id: 'c1', x: 366, y: 92, label: '1', text: 'Confirm crop ratio before approval' },
  { id: 'c2', x: 54, y: 312, label: '2', text: 'Empty state needs reviewer sign-off' },
  { id: 'c3', x: 348, y: 508, label: '3', text: 'Button copy synced to frontend doc' },
]

const DESIGN_DIFFS = [
  { id: 'color', labelKey: 'prototype.diff.color', status: '+2' },
  { id: 'spacing', labelKey: 'prototype.diff.spacing', status: '-4px' },
  { id: 'asset', labelKey: 'prototype.diff.asset', status: '1' },
] as const

const MOCK_FIXTURES: MockFixture[] = [
  {
    id: 'tasks-ready',
    endpoint: '/api/tasks',
    fixture: 'tasks.ready.json',
    method: 'GET',
    status: 200,
    latency: 140,
    rows: 4,
    state: 'ready',
    schema: 'Task[]',
    sample: '{ "tasks": [{ "id": "t1", "title": "Review roadmap", "priority": "High" }] }',
  },
  {
    id: 'tasks-empty',
    endpoint: '/api/tasks?filter=done',
    fixture: 'tasks.empty.json',
    method: 'GET',
    status: 200,
    latency: 90,
    rows: 0,
    state: 'empty',
    schema: 'Task[]',
    sample: '{ "tasks": [] }',
  },
  {
    id: 'tasks-loading',
    endpoint: '/api/tasks',
    fixture: 'tasks.loading.json',
    method: 'GET',
    status: 202,
    latency: 1200,
    rows: 0,
    state: 'loading',
    schema: 'PendingResponse',
    sample: '{ "status": "pending", "retryAfter": 1 }',
  },
  {
    id: 'tasks-error',
    endpoint: '/api/tasks',
    fixture: 'tasks.error.json',
    method: 'POST',
    status: 422,
    latency: 220,
    rows: 1,
    state: 'error',
    schema: 'ApiError',
    sample: '{ "error": { "code": "TASK_LIMIT", "message": "Task limit reached" } }',
  },
]

const ACCEPTANCE_CHECKS = [
  { id: 'states', labelKey: 'prototype.acceptance.states', passed: true },
  { id: 'responsive', labelKey: 'prototype.acceptance.responsive', passed: true },
  { id: 'fixtures', labelKey: 'prototype.acceptance.fixtures', passed: true },
  { id: 'tokens', labelKey: 'prototype.acceptance.tokens', passed: true },
] as const satisfies readonly {
  id: string
  labelKey: MessageKey
  passed: boolean
}[]

const EXPORT_TARGETS = [
  { id: 'context', labelKey: 'prototype.export.context', icon: Braces },
  { id: 'fixtures', labelKey: 'prototype.export.fixtures', icon: Database },
  { id: 'spec', labelKey: 'prototype.export.spec', icon: FileCode2 },
] as const satisfies readonly {
  id: string
  labelKey: MessageKey
  icon: typeof Frame
}[]

function createInitialLayers() {
  return Object.fromEntries(PAGES.map((page) => [page.id, cloneItems(page.layers)])) as Record<string, PrototypeLayer[]>
}

export function PrototypeStudio() {
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const { setSurface, openDoc, createDocument } = useWorksflow()
  const [activePage, setActivePage] = useState('p1')
  const [state, setState] = useState<StateKey>('ready')
  const [mode, setMode] = useState<PrototypeMode>('wireframe')
  const [activePanel, setActivePanel] = useState<StudioPanel>('properties')
  const [device, setDevice] = useState<DevicePreset>('desktop')
  const [zoom, setZoom] = useState(92)
  const [showGrid, setShowGrid] = useState(true)
  const [snapToGrid, setSnapToGrid] = useState(true)
  const [activeFixtureId, setActiveFixtureId] = useState('tasks-ready')
  const [activeLibraryId, setActiveLibraryId] = useState<UiLibraryId>('shadcn')
  const [componentSearch, setComponentSearch] = useState('')
  const [customComponentName, setCustomComponentName] = useState('Team metric card')
  const [customComponentKind, setCustomComponentKind] = useState<LayerKind>('card')
  const [customTemplates, setCustomTemplates] = useState<ComponentTemplate[]>([
    {
      id: 'custom-metric-card',
      libraryId: 'custom',
      name: 'Metric card',
      category: 'Team',
      kind: 'card',
      width: 260,
      height: 96,
      fill: '#1e1e21',
      icon: Settings2,
    },
    {
      id: 'custom-empty-state',
      libraryId: 'custom',
      name: 'Empty state',
      category: 'Team',
      kind: 'frame',
      width: 300,
      height: 150,
      fill: '#171719',
      icon: Shapes,
    },
  ])
  const [layersByPage, setLayersByPage] = useState(createInitialLayers)
  const [selectedLayerId, setSelectedLayerId] = useState('p1-hero')
  const [dragging, setDragging] = useState<{
    id: string
    startX: number
    startY: number
    originX: number
    originY: number
  } | null>(null)
  const [layerCounter, setLayerCounter] = useState(1)
  const [snapshotCount, setSnapshotCount] = useState(3)
  const [notice, setNotice] = useState<string | null>(null)

  const page = PAGES.find((item) => item.id === activePage) ?? PAGES[0]
  const layers = layersByPage[page.id] ?? []
  const selectedLayer = layers.find((item) => item.id === selectedLayerId) ?? layers.find((item) => !item.locked) ?? layers[0]
  const activeViewport = DEVICE_PRESETS[device]
  const activeFixture = MOCK_FIXTURES.find((item) => item.id === activeFixtureId) ?? MOCK_FIXTURES[0]
  const allComponentTemplates = useMemo(
    () => [...COMPONENT_TEMPLATES, ...customTemplates],
    [customTemplates],
  )
  const activeLibrary = UI_LIBRARIES.find((item) => item.id === activeLibraryId) ?? UI_LIBRARIES[0]
  const visibleComponentTemplates = useMemo(() => {
    const normalizedSearch = componentSearch.trim().toLowerCase()
    return allComponentTemplates.filter((item) => {
      const libraryMatched = item.libraryId === activeLibraryId
      const searchMatched =
        normalizedSearch.length === 0 ||
        item.name.toLowerCase().includes(normalizedSearch) ||
        item.category.toLowerCase().includes(normalizedSearch)
      return libraryMatched && searchMatched
    })
  }, [activeLibraryId, allComponentTemplates, componentSearch])
  const scale = zoom / 100
  const availableStates = page.states

  const coveredStates = useMemo(
    () => STATE_ORDER.filter((item) => availableStates.includes(item)).length,
    [availableStates],
  )

  function selectPage(pageId: string) {
    const nextPage = PAGES.find((item) => item.id === pageId)
    if (!nextPage) return

    setActivePage(nextPage.id)
    setState(nextPage.states.includes('ready') ? 'ready' : nextPage.states[0])
    setActiveFixtureId(nextPage.states.includes('ready') ? 'tasks-ready' : MOCK_FIXTURES.find((item) => item.state === nextPage.states[0])?.id ?? 'tasks-ready')
    setSelectedLayerId(nextPage.layers.find((item) => !item.locked)?.id ?? nextPage.layers[0].id)
    setNotice(null)
  }

  function updateLayer(layerId: string, updates: Partial<PrototypeLayer>) {
    setLayersByPage((current) => ({
      ...current,
      [page.id]: (current[page.id] ?? []).map((item) =>
        item.id === layerId ? { ...item, ...updates } : item,
      ),
    }))
  }

  function updateSelectedLayer(updates: Partial<PrototypeLayer>) {
    if (!selectedLayer || selectedLayer.locked) return
    updateLayer(selectedLayer.id, updates)
  }

  function addComponentTemplate(template: ComponentTemplate) {
    const id = `${template.id}-${layerCounter}`
    const nextLayer = layer({
      id,
      name: template.name,
      kind: template.kind,
      x: 52 + layerCounter * 8,
      y: 92 + layerCounter * 8,
      w: template.width,
      h: template.height,
      radius: template.kind === 'badge' ? 999 : 10,
      fill: template.fill ?? (template.kind === 'button' ? '#1488fc' : '#1e1e21'),
      stroke: template.stroke ?? (template.kind === 'text' ? 'transparent' : 'rgba(255,255,255,0.12)'),
      text:
        template.text ??
        (template.kind === 'text' ? template.name : template.kind === 'button' ? template.name : undefined),
    })

    setLayersByPage((current) => ({
      ...current,
      [page.id]: [...(current[page.id] ?? []), nextLayer],
    }))
    setSelectedLayerId(id)
    setLayerCounter((value) => value + 1)
    setNotice(t('prototype.componentInserted', { component: template.name, library: activeLibrary.name }))
  }

  function insertActiveComponent() {
    const template = visibleComponentTemplates[0] ?? allComponentTemplates.find((item) => item.libraryId === activeLibraryId)
    if (template) addComponentTemplate(template)
  }

  function addCustomComponent() {
    const name = customComponentName.trim()
    if (!name) return

    const icon = customComponentKind === 'text'
      ? Type
      : customComponentKind === 'input'
        ? FormInput
        : customComponentKind === 'badge'
          ? Badge
          : customComponentKind === 'image'
            ? ImageIcon
            : customComponentKind === 'button'
              ? Sparkles
              : customComponentKind === 'frame'
                ? Frame
                : Settings2
    const template: ComponentTemplate = {
      id: `custom-${name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}-${layerCounter}`,
      libraryId: 'custom',
      name,
      category: 'Custom',
      kind: customComponentKind,
      width: customComponentKind === 'badge' || customComponentKind === 'button' ? 128 : 260,
      height: customComponentKind === 'text' ? 34 : customComponentKind === 'badge' || customComponentKind === 'button' ? 42 : 96,
      fill: customComponentKind === 'button' ? '#1488fc' : customComponentKind === 'text' ? 'transparent' : '#1e1e21',
      text: customComponentKind === 'text' || customComponentKind === 'button' || customComponentKind === 'badge' ? name : undefined,
      icon,
    }

    setCustomTemplates((current) => [...current, template])
    setActiveLibraryId('custom')
    setComponentSearch('')
    setCustomComponentName('')
    setNotice(t('prototype.customComponentAdded', { component: template.name }))
  }

  function duplicateSelectedLayer() {
    if (!selectedLayer) return

    const id = `${selectedLayer.kind}-${layerCounter}`
    const duplicate = {
      ...selectedLayer,
      id,
      name: `${selectedLayer.name} copy`,
      x: selectedLayer.x + 16,
      y: selectedLayer.y + 16,
      locked: false,
    }

    setLayersByPage((current) => ({
      ...current,
      [page.id]: [...(current[page.id] ?? []), duplicate],
    }))
    setSelectedLayerId(id)
    setLayerCounter((value) => value + 1)
    setNotice(t('prototype.layerDuplicated'))
  }

  function deleteSelectedLayer() {
    if (!selectedLayer || selectedLayer.locked) return

    const nextLayers = layers.filter((item) => item.id !== selectedLayer.id)
    setLayersByPage((current) => ({
      ...current,
      [page.id]: nextLayers,
    }))
    setSelectedLayerId(nextLayers.find((item) => !item.locked)?.id ?? nextLayers[0]?.id ?? '')
    setNotice(t('prototype.layerDeleted'))
  }

  function resetSelectedLayer() {
    if (!selectedLayer || selectedLayer.locked) return

    const original = page.layers.find((item) => item.id === selectedLayer.id)
    if (!original) return

    updateLayer(selectedLayer.id, { ...original })
    setNotice(t('prototype.layerReset'))
  }

  function nudgeSelectedLayer(dx: number, dy: number) {
    if (!selectedLayer || selectedLayer.locked) return

    updateLayer(selectedLayer.id, {
      x: clamp(selectedLayer.x + dx, -80, activeViewport.width + 80),
      y: clamp(selectedLayer.y + dy, -80, activeViewport.height + 80),
    })
  }

  function beginLayerDrag(event: React.PointerEvent<HTMLButtonElement>, item: PrototypeLayer) {
    setSelectedLayerId(item.id)
    if (item.locked) return

    event.preventDefault()
    event.currentTarget.setPointerCapture(event.pointerId)
    setDragging({
      id: item.id,
      startX: event.clientX,
      startY: event.clientY,
      originX: item.x,
      originY: item.y,
    })
  }

  function handleCanvasPointerMove(event: React.PointerEvent<HTMLDivElement>) {
    if (!dragging) return

    const deltaX = (event.clientX - dragging.startX) / scale
    const deltaY = (event.clientY - dragging.startY) / scale
    const nextX = dragging.originX + deltaX
    const nextY = dragging.originY + deltaY
    const snappedX = snapToGrid ? Math.round(nextX / 8) * 8 : nextX
    const snappedY = snapToGrid ? Math.round(nextY / 8) * 8 : nextY

    updateLayer(dragging.id, {
      x: clamp(Math.round(snappedX), -80, activeViewport.width + 80),
      y: clamp(Math.round(snappedY), -80, activeViewport.height + 80),
    })
  }

  function saveSnapshot() {
    setSnapshotCount((value) => value + 1)
    setNotice(t('prototype.snapshotSaved'))
  }

  function createFrontendDoc() {
    const docId = createDocument(
      'frontendDev',
      t('prototype.frontendDocTitle', { page: page.name }),
      'readyForReview',
    )
    openDoc(docId)
  }

  return (
    <div className="flex h-full max-lg:flex-col max-lg:overflow-y-auto">
      <aside className="flex w-64 shrink-0 flex-col border-r border-border bg-surface max-lg:max-h-[360px] max-lg:w-full max-lg:border-b max-lg:border-r-0">
        <div className="border-b border-border p-3">
          <div className="flex items-center justify-between gap-2">
            <span className="flex items-center gap-1.5 text-xs font-semibold text-foreground">
              <MonitorSmartphone className="size-4 text-primary-bright" />
              {t('prototype.pages')}
            </span>
            <span className="rounded bg-white/5 px-1.5 py-0.5 text-[10px] text-faint-foreground">
              {PAGES.length}
            </span>
          </div>
          <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">
            {t('prototype.studioSubtitle')}
          </p>
        </div>

        <div className="border-b border-border p-2">
          {PAGES.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => selectPage(item.id)}
              className={cn(
                'flex w-full flex-col gap-1 rounded-lg px-2.5 py-2 text-left transition-colors',
                activePage === item.id ? 'bg-white/10' : 'hover:bg-white/5',
              )}
            >
              <span className="flex items-center gap-1.5">
                <span className="font-mono text-xs font-medium text-foreground">{item.name}</span>
                <span className="ml-auto text-[10px] text-faint-foreground">
                  {t('prototype.statesCount', { count: item.states.length })}
                </span>
              </span>
              <span className="text-[10px] text-faint-foreground">
                {labels.docType(item.docType)} · {item.updatedAt}
              </span>
            </button>
          ))}
        </div>

        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2">
            <span className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-faint-foreground">
              <Layers className="size-3.5" />
              {t('prototype.layers')}
            </span>
            <span className="text-[10px] text-faint-foreground">
              {t('prototype.layerCount', { count: layers.length })}
            </span>
          </div>

          <div className="flex-1 overflow-y-auto scrollbar-thin p-2">
            {layers.toReversed().map((item) => {
              const Icon = LAYER_ICON[item.kind]
              const selected = selectedLayerId === item.id

              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => setSelectedLayerId(item.id)}
                  className={cn(
                    'mb-1 flex w-full items-center gap-2 rounded-lg border px-2 py-2 text-left text-xs transition-colors',
                    selected
                      ? 'border-primary/45 bg-primary/10 text-foreground'
                      : 'border-transparent text-muted-foreground hover:border-border hover:bg-white/5',
                  )}
                >
                  <Icon className="size-3.5 shrink-0 text-faint-foreground" />
                  <span className="min-w-0 flex-1 truncate">{item.name}</span>
                  {item.locked ? <Lock className="size-3 text-faint-foreground" /> : null}
                  {item.visible ? (
                    <Eye className="size-3 text-faint-foreground" />
                  ) : (
                    <EyeOff className="size-3 text-faint-foreground" />
                  )}
                </button>
              )
            })}
          </div>

          <div className="border-t border-border p-2">
            <div className="mb-2 flex items-center justify-between gap-2 px-1">
              <span className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-faint-foreground">
                <Component className="size-3.5" />
                {t('prototype.componentLibrary')}
              </span>
              <span className="text-[10px] text-faint-foreground">{visibleComponentTemplates.length}</span>
            </div>

            <div className="mb-2 flex gap-1 overflow-x-auto pb-1 scrollbar-thin">
              {UI_LIBRARIES.map((library) => (
                <button
                  key={library.id}
                  type="button"
                  onClick={() => {
                    setActiveLibraryId(library.id)
                    setComponentSearch('')
                  }}
                  className={cn(
                    'shrink-0 rounded-md border px-2 py-1 text-[10px] font-medium transition-colors',
                    activeLibraryId === library.id
                      ? 'border-primary/45 bg-primary/10 text-primary-bright'
                      : 'border-border bg-surface-2 text-faint-foreground hover:text-foreground',
                  )}
                  title={t(library.descriptionKey)}
                >
                  {library.name}
                </button>
              ))}
            </div>

            <label className="mb-2 flex h-8 items-center gap-2 rounded-md border border-border bg-surface-2 px-2 text-[11px] text-faint-foreground">
              <Search className="size-3.5" />
              <input
                value={componentSearch}
                onChange={(event) => setComponentSearch(event.target.value)}
                placeholder={t('prototype.componentSearchPlaceholder')}
                className="min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-faint-foreground"
                aria-label={t('prototype.componentSearch')}
              />
            </label>

            <div className="mb-2 rounded-md border border-border bg-surface-2 px-2 py-1.5">
              <div className="flex items-center justify-between gap-2">
                <span className="truncate text-[11px] font-medium text-foreground">{activeLibrary.name}</span>
                <span className="shrink-0 rounded bg-white/5 px-1.5 py-0.5 text-[10px] text-faint-foreground">
                  {activeLibrary.token}
                </span>
              </div>
              <p className="mt-1 text-[10px] leading-relaxed text-faint-foreground">
                {t(activeLibrary.descriptionKey)}
              </p>
            </div>

            {activeLibraryId === 'custom' && (
              <div className="mb-2 rounded-md border border-border bg-surface-2 p-2">
                <div className="text-[11px] font-medium text-foreground">{t('prototype.customLibrary')}</div>
                <div className="mt-2 flex gap-1.5">
                  <input
                    value={customComponentName}
                    onChange={(event) => setCustomComponentName(event.target.value)}
                    placeholder={t('prototype.customComponentName')}
                    className="min-w-0 flex-1 rounded-md border border-border bg-canvas px-2 py-1.5 text-xs text-foreground outline-none placeholder:text-faint-foreground"
                    aria-label={t('prototype.customComponentName')}
                  />
                  <button
                    type="button"
                    onClick={addCustomComponent}
                    className="flex size-8 shrink-0 items-center justify-center rounded-md bg-primary text-white hover:bg-primary/90"
                    aria-label={t('prototype.addCustomComponent')}
                    title={t('prototype.addCustomComponent')}
                  >
                    <Plus className="size-4" />
                  </button>
                </div>
                <div className="mt-2 flex flex-wrap gap-1">
                  {(['card', 'button', 'input', 'badge', 'frame', 'text'] as LayerKind[]).map((kind) => (
                    <button
                      key={kind}
                      type="button"
                      onClick={() => setCustomComponentKind(kind)}
                      className={cn(
                        'rounded border px-1.5 py-1 text-[10px] font-medium',
                        customComponentKind === kind
                          ? 'border-primary/45 bg-primary/10 text-primary-bright'
                          : 'border-border text-faint-foreground hover:text-foreground',
                      )}
                    >
                      {kind}
                    </button>
                  ))}
                </div>
              </div>
            )}

            <div className="max-h-52 space-y-1.5 overflow-y-auto pr-1 scrollbar-thin">
              {visibleComponentTemplates.map((template) => (
                <ComponentTemplateButton
                  key={template.id}
                  template={template}
                  onClick={() => addComponentTemplate(template)}
                />
              ))}
              {visibleComponentTemplates.length === 0 && (
                <div className="rounded-md border border-dashed border-border bg-surface-2 p-3 text-center text-[11px] text-faint-foreground">
                  {t('prototype.noComponents')}
                </div>
              )}
            </div>
          </div>
        </div>
      </aside>

      <main className="flex min-w-0 flex-1 flex-col bg-canvas max-lg:min-h-[680px]">
        <div className="flex min-h-[58px] items-center justify-between gap-3 border-b border-border bg-surface/85 px-4 py-2 backdrop-blur max-xl:flex-wrap">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h1 className="truncate text-sm font-semibold text-foreground">{t('prototype.studioTitle')}</h1>
              <span className="font-mono text-xs text-muted-foreground">{page.name}</span>
              <span className="inline-flex items-center gap-1 rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary-bright">
                <Frame className="size-3" />
                {t('prototype.figmaSynced')}
              </span>
            </div>
          <div className="mt-1 text-[11px] text-faint-foreground">
              {page.owner} · {coveredStates}/{STATE_ORDER.length} {t('prototype.componentStates')} · {activeFixture.fixture}
          </div>
          </div>

          <div className="flex max-w-full items-center gap-1.5 overflow-x-auto rounded-lg border border-border bg-surface-2 p-0.5 scrollbar-thin">
            {(['wireframe', 'design', 'component', 'handoff'] as PrototypeMode[]).map((item) => {
              const Icon = MODE_ICON[item]
              return (
                <button
                  key={item}
                  type="button"
                  onClick={() => setMode(item)}
                  className={cn(
                    'inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md px-2 text-[11px] font-medium transition-colors',
                    mode === item
                      ? 'bg-white/10 text-foreground'
                      : 'text-faint-foreground hover:text-muted-foreground',
                  )}
                >
                  <Icon className="size-3.5" />
                  {t(MODE_LABEL_KEY[item])}
                </button>
              )
            })}
          </div>

          <div className="flex max-w-full items-center gap-1.5 overflow-x-auto rounded-lg border border-border bg-surface-2 p-0.5 scrollbar-thin">
            {page.states.map((item) => (
              <button
                key={item}
                type="button"
                onClick={() => setState(item)}
                className={cn(
                  'h-7 shrink-0 rounded-md px-2.5 text-[11px] font-medium transition-colors',
                  state === item
                    ? 'bg-white/10 text-foreground'
                    : 'text-faint-foreground hover:text-muted-foreground',
                )}
              >
                {t(STATE_LABEL_KEY[item])}
              </button>
            ))}
          </div>
        </div>

        <div className="flex min-h-[46px] items-center justify-between gap-3 border-b border-border bg-panel px-4 py-2 max-xl:flex-wrap">
          <div className="flex items-center gap-1.5">
            <ToolbarButton
              active
              icon={MousePointer2}
              label={t('prototype.selectTool')}
            />
            <ToolbarButton
              icon={Move}
              label={t('prototype.moveTool')}
              onClick={() => nudgeSelectedLayer(8, 0)}
            />
            <ToolbarButton
              icon={Grid3X3}
              label={t('prototype.showGrid')}
              active={showGrid}
              onClick={() => setShowGrid((value) => !value)}
            />
            <ToolbarButton
              icon={Wand2}
              label={t('prototype.snap')}
              active={snapToGrid}
              onClick={() => setSnapToGrid((value) => !value)}
            />
            <ToolbarButton
              icon={Plus}
              label={t('prototype.insertComponent')}
              onClick={insertActiveComponent}
            />
          </div>

          <div className="flex items-center gap-1.5">
            {(['desktop', 'tablet', 'mobile'] as DevicePreset[]).map((item) => {
              const Icon = DEVICE_PRESETS[item].icon
              return (
                <button
                  key={item}
                  type="button"
                  onClick={() => setDevice(item)}
                  className={cn(
                    'inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-[11px] font-medium transition-colors',
                    device === item
                      ? 'bg-primary/15 text-primary-bright'
                      : 'text-faint-foreground hover:bg-white/5 hover:text-foreground',
                  )}
                >
                  <Icon className="size-3.5" />
                  {t(DEVICE_PRESETS[item].labelKey)}
                </button>
              )
            })}
          </div>

          <div className="flex items-center gap-1.5">
            <IconOnlyButton
              icon={ZoomOut}
              label={t('prototype.zoomOut')}
              onClick={() => setZoom((value) => clamp(value - 8, 48, 140))}
            />
            <span className="w-12 text-center text-[11px] text-muted-foreground">{zoom}%</span>
            <IconOnlyButton
              icon={ZoomIn}
              label={t('prototype.zoomIn')}
              onClick={() => setZoom((value) => clamp(value + 8, 48, 140))}
            />
          </div>
        </div>

        {notice && (
          <div className="border-b border-primary/25 bg-primary/10 px-4 py-2 text-xs text-primary-bright">
            {notice}
          </div>
        )}

        <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
          <div
            className="flex min-h-0 flex-1 items-center justify-center overflow-auto p-8 scrollbar-thin max-sm:p-4"
            onPointerMove={handleCanvasPointerMove}
            onPointerUp={() => setDragging(null)}
            onPointerCancel={() => setDragging(null)}
          >
            <div
              className="relative shrink-0"
              style={{
                width: activeViewport.width * scale,
                height: activeViewport.height * scale,
              }}
            >
              <div
                className="absolute left-0 top-0 origin-top-left overflow-hidden rounded-[18px] border border-border bg-surface shadow-2xl"
                style={{
                  width: activeViewport.width,
                  height: activeViewport.height,
                  transform: `scale(${scale})`,
                  backgroundImage: showGrid
                    ? 'linear-gradient(rgba(255,255,255,0.05) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,0.05) 1px, transparent 1px)'
                    : undefined,
                  backgroundSize: showGrid ? '16px 16px' : undefined,
                }}
              >
                <CanvasStateOverlay state={state} />

                {layers.map((item, index) => (
                  <PrototypeLayerView
                    key={item.id}
                    layer={item}
                    selected={selectedLayerId === item.id}
                    zIndex={index + 2}
                    onPointerDown={(event) => beginLayerDrag(event, item)}
                  />
                ))}

                {mode === 'design' && <DesignDiffOverlay />}
                {mode === 'handoff' && <HandoffPins />}
              </div>
            </div>
          </div>

          <ModeShelf
            mode={mode}
            page={page}
            selectedLayer={selectedLayer}
            activeFixture={activeFixture}
            onSelectFixture={(fixture) => {
              setActiveFixtureId(fixture.id)
              setState(fixture.state)
            }}
            snapshotCount={snapshotCount}
            onSaveSnapshot={saveSnapshot}
          />
        </div>
      </main>

      <aside className="flex w-80 shrink-0 flex-col border-l border-border bg-surface max-lg:w-full max-lg:max-h-[620px] max-lg:border-l-0 max-lg:border-t">
        <div className="border-b border-border p-3">
          <div className="grid grid-cols-3 gap-1 rounded-lg border border-border bg-surface-2 p-0.5">
            {(['properties', 'data', 'handoff'] as StudioPanel[]).map((item) => {
              const Icon = PANEL_ICON[item]
              return (
                <button
                  key={item}
                  type="button"
                  onClick={() => setActivePanel(item)}
                  className={cn(
                    'inline-flex h-8 items-center justify-center gap-1.5 rounded-md text-[11px] font-medium transition-colors',
                    activePanel === item
                      ? 'bg-white/10 text-foreground'
                      : 'text-faint-foreground hover:text-muted-foreground',
                  )}
                >
                  <Icon className="size-3.5" />
                  {t(PANEL_LABEL_KEY[item])}
                </button>
              )
            })}
          </div>
        </div>

        <div className="flex-1 overflow-y-auto scrollbar-thin p-4">
          {activePanel === 'properties' && selectedLayer && (
            <PropertiesPanel
              layer={selectedLayer}
              page={page}
              mode={mode}
              snapshotCount={snapshotCount}
              disabled={selectedLayer.locked}
              onChange={updateSelectedLayer}
              onToggleVisible={() => updateLayer(selectedLayer.id, { visible: !selectedLayer.visible })}
              onToggleLocked={() => updateLayer(selectedLayer.id, { locked: !selectedLayer.locked })}
              onDuplicate={duplicateSelectedLayer}
              onDelete={deleteSelectedLayer}
              onReset={resetSelectedLayer}
              onNudge={nudgeSelectedLayer}
            />
          )}
          {activePanel === 'properties' && !selectedLayer && <EmptyInspector />}
          {activePanel === 'data' && (
            <DataPanel
              activeFixture={activeFixture}
              fixtures={MOCK_FIXTURES}
              onSelectFixture={(fixture) => {
                setActiveFixtureId(fixture.id)
                setState(fixture.state)
              }}
            />
          )}
          {activePanel === 'handoff' && (
            <HandoffPanel
              page={page}
              mode={mode}
              snapshotCount={snapshotCount}
              onOpenDoc={() => openDoc('d6')}
              onGenerate={() => setSurface('workbench')}
              onCreateFrontendDoc={createFrontendDoc}
              onSaveSnapshot={saveSnapshot}
            />
          )}
        </div>
      </aside>
    </div>
  )
}

function ComponentTemplateButton({
  template,
  onClick,
}: {
  template: ComponentTemplate
  onClick: () => void
}) {
  const Icon = template.icon

  return (
    <button
      type="button"
      onClick={onClick}
      className="flex w-full items-center gap-2 rounded-md border border-border bg-surface-2 px-2.5 py-2 text-left hover:border-primary/35 hover:text-foreground"
    >
      <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-white/5">
        <Icon className="size-3.5 text-primary-bright" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-xs font-medium text-foreground">{template.name}</span>
        <span className="block truncate text-[10px] text-faint-foreground">
          {template.category} · {template.width}x{template.height}
        </span>
      </span>
      <Plus className="size-3.5 shrink-0 text-faint-foreground" />
    </button>
  )
}

function ToolbarButton({
  icon: Icon,
  label,
  active,
  onClick,
}: {
  icon: typeof Frame
  label: string
  active?: boolean
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-[11px] font-medium transition-colors',
        active
          ? 'bg-white/10 text-foreground'
          : 'text-faint-foreground hover:bg-white/5 hover:text-foreground',
      )}
      title={label}
    >
      <Icon className="size-3.5" />
      {label}
    </button>
  )
}

function IconOnlyButton({
  icon: Icon,
  label,
  onClick,
}: {
  icon: typeof Frame
  label: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex size-8 items-center justify-center rounded-md text-faint-foreground hover:bg-white/5 hover:text-foreground"
      aria-label={label}
      title={label}
    >
      <Icon className="size-4" />
    </button>
  )
}

function PrototypeLayerView({
  layer,
  selected,
  zIndex,
  onPointerDown,
}: {
  layer: PrototypeLayer
  selected: boolean
  zIndex: number
  onPointerDown: (event: React.PointerEvent<HTMLButtonElement>) => void
}) {
  if (!layer.visible) return null

  const style: CSSProperties = {
    left: layer.x,
    top: layer.y,
    width: layer.w,
    height: layer.h,
    borderRadius: layer.radius,
    opacity: layer.opacity / 100,
    transform: `rotate(${layer.rotation}deg)`,
    zIndex,
    backgroundColor: layer.kind === 'image' ? undefined : layer.fill,
    borderColor: layer.stroke,
  }

  if (layer.kind === 'image') {
    style.backgroundImage = "url('/placeholder.jpg')"
    style.backgroundSize = `${layer.imageScale ?? 118}%`
    style.backgroundPosition = `${layer.cropX ?? 50}% ${layer.cropY ?? 50}%`
    style.filter = `brightness(${layer.brightness ?? 100}%) contrast(${layer.contrast ?? 100}%) saturate(${layer.saturation ?? 100}%) blur(${layer.blur ?? 0}px)`
  }

  return (
    <button
      type="button"
      onPointerDown={onPointerDown}
      className={cn(
        'absolute overflow-hidden border text-left transition-shadow',
        layer.locked ? 'cursor-not-allowed' : 'cursor-move',
        selected && 'ring-2 ring-primary ring-offset-2 ring-offset-background',
      )}
      style={style}
      aria-pressed={selected}
    >
      <LayerContent layer={layer} />
    </button>
  )
}

function LayerContent({ layer }: { layer: PrototypeLayer }) {
  if (layer.kind === 'frame') {
    return (
      <div className="pointer-events-none flex h-full flex-col">
        <div className="flex h-8 items-center gap-1.5 border-b border-white/10 bg-white/[0.03] px-3">
          <span className="size-2.5 rounded-full bg-white/15" />
          <span className="size-2.5 rounded-full bg-white/15" />
          <span className="size-2.5 rounded-full bg-white/15" />
        </div>
      </div>
    )
  }

  if (layer.kind === 'text') {
    return (
      <div
        className="pointer-events-none flex h-full items-center font-semibold leading-none text-foreground"
        style={{ fontSize: layer.textSize }}
      >
        {layer.text}
      </div>
    )
  }

  if (layer.kind === 'button') {
    return (
      <div className="pointer-events-none flex h-full items-center justify-center text-xs font-semibold text-white">
        {layer.text}
      </div>
    )
  }

  if (layer.kind === 'badge') {
    return (
      <div className="pointer-events-none flex h-full items-center justify-center gap-1.5 px-3 text-[11px] font-medium text-success">
        <CheckCircle2 className="size-3.5" />
        <span className="truncate">{layer.text}</span>
      </div>
    )
  }

  if (layer.kind === 'input') {
    return (
      <div className="pointer-events-none flex h-full flex-col justify-center gap-2 px-4">
        <div className="h-2 w-2/3 rounded bg-white/18" />
        <div className="h-2 w-1/2 rounded bg-white/8" />
      </div>
    )
  }

  if (layer.kind === 'card') {
    return (
      <div className="pointer-events-none flex h-full items-center gap-3 px-4">
        <span className="size-8 rounded-full border border-white/15 bg-white/5" />
        <span className="min-w-0 flex-1 space-y-2">
          <span className="block h-2.5 w-2/3 rounded bg-white/20" />
          <span className="block h-2 w-1/2 rounded bg-white/10" />
        </span>
        <span className="h-5 w-12 rounded bg-primary/15" />
      </div>
    )
  }

  return (
    <div className="pointer-events-none flex h-full items-end bg-black/20 p-3">
      <span className="rounded bg-black/40 px-2 py-1 text-[10px] font-medium text-white">
        {layer.name}
      </span>
    </div>
  )
}

function CanvasStateOverlay({ state }: { state: StateKey }) {
  const { t } = useI18n()

  if (state === 'ready') return null

  return (
    <div className="pointer-events-none absolute inset-0 z-[1] flex items-center justify-center bg-black/20">
      <div className="rounded-lg border border-border bg-surface/95 px-4 py-3 text-center shadow-xl">
        <div className="text-xs font-semibold text-foreground">{t(STATE_LABEL_KEY[state])}</div>
        <div className="mt-1 text-[11px] text-faint-foreground">
          {state === 'empty' && t('prototype.noTasksCopy')}
          {state === 'loading' && t('prototype.loadingState')}
          {state === 'error' && t('prototype.loadFailedCopy')}
        </div>
      </div>
    </div>
  )
}

function DesignDiffOverlay() {
  const { t } = useI18n()

  return (
    <div className="pointer-events-none absolute left-4 top-12 z-30 flex flex-wrap gap-1.5">
      {DESIGN_DIFFS.map((item) => (
        <span
          key={item.id}
          className="rounded-md border border-primary/30 bg-primary/15 px-2 py-1 text-[10px] font-medium text-primary-bright"
        >
          {t(item.labelKey)} {item.status}
        </span>
      ))}
    </div>
  )
}

function HandoffPins() {
  return (
    <>
      {HANDOFF_PINS.map((pin) => (
        <button
          key={pin.id}
          type="button"
          className="absolute z-40 flex size-6 items-center justify-center rounded-full border border-white/40 bg-primary text-[11px] font-semibold text-white shadow-lg"
          style={{ left: pin.x, top: pin.y }}
          title={pin.text}
        >
          {pin.label}
        </button>
      ))}
    </>
  )
}

function ModeShelf({
  mode,
  page,
  selectedLayer,
  activeFixture,
  onSelectFixture,
  snapshotCount,
  onSaveSnapshot,
}: {
  mode: PrototypeMode
  page: PrototypePage
  selectedLayer?: PrototypeLayer
  activeFixture: MockFixture
  onSelectFixture: (fixture: MockFixture) => void
  snapshotCount: number
  onSaveSnapshot: () => void
}) {
  const { t } = useI18n()

  if (mode === 'design') {
    return (
      <div className="border-t border-border bg-surface px-4 py-3">
        <div className="flex items-center justify-between gap-3 max-lg:flex-col max-lg:items-stretch">
          <div className="min-w-0">
            <div className="text-xs font-semibold text-foreground">{t('prototype.importedFrames')}</div>
            <div className="mt-1 text-[11px] text-faint-foreground">{t('prototype.importedFramesCopy')}</div>
          </div>
          <div className="flex items-center gap-3 overflow-x-auto scrollbar-thin">
          {['Task List Frame', 'Task Detail Frame', 'Empty State Frame', 'Error State Frame'].map((frameName, index) => (
            <button
              key={frameName}
              type="button"
              className="flex min-w-44 items-center gap-3 rounded-lg border border-border bg-surface-2 px-3 py-2 text-left hover:border-primary/35"
            >
              <span className="flex size-9 items-center justify-center rounded-md border border-primary/30 bg-primary/10 text-primary-bright">
                {index + 1}
              </span>
              <span className="min-w-0">
                <span className="block truncate text-xs font-medium text-foreground">{frameName}</span>
                <span className="block truncate text-[11px] text-faint-foreground">
                  {t('prototype.mapFramesCopy')}
                </span>
              </span>
            </button>
          ))}
          </div>
        </div>
      </div>
    )
  }

  if (mode === 'component') {
    return (
      <div className="border-t border-border bg-surface px-4 py-3">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="text-xs font-semibold text-foreground">{t('prototype.componentStories')}</div>
            <div className="mt-1 text-[11px] text-faint-foreground">{t('prototype.componentStoriesCopy')}</div>
          </div>
          <div className="flex max-w-full gap-1.5 overflow-x-auto scrollbar-thin">
            {page.componentStories.map((story) => (
              <button
                key={story}
                type="button"
                className={cn(
                  'shrink-0 rounded-md border px-2.5 py-1.5 text-[11px] font-medium',
                  selectedLayer?.name.includes(story.split(' ')[0])
                    ? 'border-primary/45 bg-primary/10 text-primary-bright'
                    : 'border-border bg-surface-2 text-muted-foreground hover:text-foreground',
                )}
              >
                {story}
              </button>
            ))}
          </div>
        </div>
      </div>
    )
  }

  if (mode === 'handoff') {
    return (
      <div className="border-t border-border bg-surface px-4 py-3">
        <div className="flex items-center justify-between gap-3 max-lg:flex-col max-lg:items-stretch">
          <div>
            <div className="text-xs font-semibold text-foreground">{t('prototype.deliveryPackage')}</div>
            <div className="mt-1 text-[11px] text-faint-foreground">
              {t('prototype.deliveryPackageCopy', { page: page.name })}
            </div>
          </div>
          <div className="flex gap-1.5">
            {EXPORT_TARGETS.map((target) => {
              const Icon = target.icon
              return (
                <button
                  key={target.id}
                  type="button"
                  className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs font-medium text-muted-foreground hover:border-primary/40 hover:text-foreground"
                >
                  <Icon className="size-3.5 text-primary-bright" />
                  {t(target.labelKey)}
                </button>
              )
            })}
            <button
              type="button"
              onClick={onSaveSnapshot}
              className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs font-medium text-muted-foreground hover:border-primary/40 hover:text-foreground"
            >
              <Save className="size-3.5 text-primary-bright" />
              v{snapshotCount + 1}
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="border-t border-border bg-surface px-4 py-3">
      <div className="flex items-center justify-between gap-4 max-xl:flex-col max-xl:items-stretch">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Database className="size-3.5 text-primary-bright" />
            <span className="text-xs font-semibold text-foreground">{t('prototype.fixtureStrip')}</span>
          </div>
          <div className="mt-1 text-[11px] text-faint-foreground">
            {activeFixture.method} {activeFixture.endpoint} · {activeFixture.status} · {activeFixture.latency}ms
          </div>
        </div>
        <div className="flex max-w-full items-center gap-1.5 overflow-x-auto scrollbar-thin">
          {MOCK_FIXTURES.map((fixture) => (
            <button
              key={fixture.id}
              type="button"
              onClick={() => onSelectFixture(fixture)}
              className={cn(
                'inline-flex shrink-0 items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-[11px] font-medium',
                activeFixture.id === fixture.id
                  ? 'border-primary/45 bg-primary/10 text-primary-bright'
                  : 'border-border bg-surface-2 text-muted-foreground hover:text-foreground',
              )}
            >
              <Play className="size-3" />
              {t(STATE_LABEL_KEY[fixture.state])}
              <span className="font-mono text-[10px] text-faint-foreground">{fixture.fixture}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

function DataPanel({
  activeFixture,
  fixtures,
  onSelectFixture,
}: {
  activeFixture: MockFixture
  fixtures: MockFixture[]
  onSelectFixture: (fixture: MockFixture) => void
}) {
  const { t } = useI18n()

  return (
    <div className="space-y-5">
      <section>
        <SectionLabel>{t('prototype.fixtureLibrary')}</SectionLabel>
        <div className="mt-2 space-y-1.5">
          {fixtures.map((fixture) => (
            <button
              key={fixture.id}
              type="button"
              onClick={() => onSelectFixture(fixture)}
              className={cn(
                'w-full rounded-lg border p-2.5 text-left transition-colors',
                activeFixture.id === fixture.id
                  ? 'border-primary/45 bg-primary/10'
                  : 'border-border bg-surface-2 hover:border-white/20',
              )}
            >
              <div className="flex items-center gap-2">
                <Database className="size-3.5 text-primary-bright" />
                <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
                  {fixture.method} {fixture.endpoint}
                </span>
                <span className="rounded bg-white/5 px-1.5 py-0.5 text-[10px] text-faint-foreground">
                  {fixture.status}
                </span>
              </div>
              <div className="mt-1 flex items-center gap-2 text-[11px] text-faint-foreground">
                <span className="truncate">{fixture.fixture}</span>
                <span className="ml-auto">{t(STATE_LABEL_KEY[fixture.state])}</span>
              </div>
            </button>
          ))}
        </div>
      </section>

      <section>
        <SectionLabel>{t('prototype.activeFixture')}</SectionLabel>
        <div className="mt-2 grid grid-cols-2 gap-2">
          <InfoTile label={t('prototype.method')} value={activeFixture.method} />
          <InfoTile label={t('prototype.statusCode')} value={String(activeFixture.status)} />
          <InfoTile label={t('prototype.latency')} value={`${activeFixture.latency}ms`} />
          <InfoTile label={t('prototype.schema')} value={activeFixture.schema} />
        </div>
        <div className="mt-2 rounded-lg border border-border bg-surface-2 p-3">
          <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold text-foreground">
            <Braces className="size-3.5 text-primary-bright" />
            {activeFixture.fixture}
          </div>
          <pre className="max-h-32 overflow-auto whitespace-pre-wrap rounded-md bg-black/20 p-2 font-mono text-[10px] leading-relaxed text-muted-foreground scrollbar-thin">
            {activeFixture.sample}
          </pre>
        </div>
      </section>

      <section className="grid grid-cols-2 gap-1.5">
        <button
          type="button"
          className="inline-flex items-center justify-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-xs font-medium text-white hover:bg-primary/90"
        >
          <Play className="size-3.5" />
          {t('prototype.runFixture')}
        </button>
        <button
          type="button"
          className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs font-medium text-muted-foreground hover:border-primary/40 hover:text-foreground"
        >
          <Save className="size-3.5 text-primary-bright" />
          {t('prototype.saveFixture')}
        </button>
      </section>
    </div>
  )
}

function HandoffPanel({
  page,
  mode,
  snapshotCount,
  onOpenDoc,
  onGenerate,
  onCreateFrontendDoc,
  onSaveSnapshot,
}: {
  page: PrototypePage
  mode: PrototypeMode
  snapshotCount: number
  onOpenDoc: () => void
  onGenerate: () => void
  onCreateFrontendDoc: () => void
  onSaveSnapshot: () => void
}) {
  const { t } = useI18n()
  const labels = useLocalizedLabels()

  return (
    <div className="space-y-5">
      <section>
        <SectionLabel>{t('prototype.deliveryReadiness')}</SectionLabel>
        <div className="mt-2 grid grid-cols-2 gap-2">
          <InfoTile label={t('common.type')} value={labels.docType(page.docType)} />
          <InfoTile label={t('prototype.versionHistory')} value={`v${snapshotCount}`} />
          <InfoTile label={t('prototype.viewport')} value={`${page.viewport.width}x${page.viewport.height}`} />
          <InfoTile label={t('prototype.mode')} value={t(MODE_LABEL_KEY[mode])} />
        </div>
      </section>

      <section>
        <SectionLabel>{t('prototype.sourceDocuments')}</SectionLabel>
        <div className="mt-2 space-y-1.5">
          {page.sourceDocs.map((doc) => (
            <div key={doc} className="flex items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-xs">
              <Link2 className="size-3.5 text-primary-bright" />
              <span className="min-w-0 flex-1 truncate text-muted-foreground">{doc}</span>
            </div>
          ))}
        </div>
      </section>

      <section>
        <SectionLabel>{t('prototype.acceptanceChecks')}</SectionLabel>
        <div className="mt-2 space-y-1.5">
          {ACCEPTANCE_CHECKS.map((item) => (
            <div
              key={item.id}
              className="flex items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-xs text-muted-foreground"
            >
              <CheckCircle2 className={cn('size-3.5', item.passed ? 'text-success' : 'text-warning')} />
              <span className="min-w-0 flex-1">{t(item.labelKey)}</span>
            </div>
          ))}
        </div>
      </section>

      <section>
        <SectionLabel>{t('prototype.exportBundle')}</SectionLabel>
        <div className="mt-2 grid grid-cols-1 gap-1.5">
          {EXPORT_TARGETS.map((target) => {
            const Icon = target.icon
            return (
              <button
                key={target.id}
                type="button"
                className="inline-flex items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-xs text-muted-foreground hover:border-primary/40 hover:text-foreground"
              >
                <Icon className="size-3.5 text-primary-bright" />
                <span className="min-w-0 flex-1 truncate text-left">{t(target.labelKey)}</span>
                <Download className="size-3 text-faint-foreground" />
              </button>
            )
          })}
        </div>
      </section>

      <section className="space-y-2">
        <button
          type="button"
          onClick={onOpenDoc}
          className="inline-flex w-full items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-xs text-muted-foreground hover:border-white/20"
        >
          <Link2 className="size-3.5 text-primary-bright" />
          <span className="flex-1 truncate text-left">{labels.docType(page.docType)}</span>
          <ArrowUpRight className="size-3 text-faint-foreground" />
        </button>
        <button
          type="button"
          onClick={onGenerate}
          className="inline-flex w-full items-center justify-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-xs font-medium text-white hover:bg-primary/90"
        >
          <Sparkles className="size-3.5" />
          {t('prototype.generateImplementation')}
        </button>
        <button
          type="button"
          onClick={onCreateFrontendDoc}
          className="inline-flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs font-medium text-muted-foreground hover:border-primary/40 hover:text-foreground"
        >
          <Sparkles className="size-3.5 text-primary-bright" />
          {t('prototype.createFrontendDoc')}
        </button>
        <button
          type="button"
          onClick={onSaveSnapshot}
          className="inline-flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs font-medium text-muted-foreground hover:border-primary/40 hover:text-foreground"
        >
          <History className="size-3.5 text-primary-bright" />
          {t('prototype.saveSnapshot')}
        </button>
      </section>
    </div>
  )
}

function PropertiesPanel({
  layer,
  page,
  mode,
  snapshotCount,
  disabled,
  onChange,
  onToggleVisible,
  onToggleLocked,
  onDuplicate,
  onDelete,
  onReset,
  onNudge,
}: {
  layer: PrototypeLayer
  page: PrototypePage
  mode: PrototypeMode
  snapshotCount: number
  disabled: boolean
  onChange: (updates: Partial<PrototypeLayer>) => void
  onToggleVisible: () => void
  onToggleLocked: () => void
  onDuplicate: () => void
  onDelete: () => void
  onReset: () => void
  onNudge: (dx: number, dy: number) => void
}) {
  const { t } = useI18n()

  return (
    <div className="space-y-5">
      <section>
        <SectionLabel>{t('prototype.pageSettings')}</SectionLabel>
        <div className="mt-2 grid grid-cols-2 gap-2">
          <InfoTile label={t('prototype.route')} value={page.name} />
          <InfoTile label={t('prototype.mode')} value={t(MODE_LABEL_KEY[mode])} />
          <InfoTile label={t('prototype.apiContract')} value={page.apiContract} />
          <InfoTile label={t('prototype.versionHistory')} value={`v${snapshotCount}`} />
        </div>
      </section>

      <section>
        <SectionLabel>{t('prototype.activeLayer')}</SectionLabel>
        <div className="mt-2 flex items-center gap-2 rounded-lg border border-border bg-surface-2 p-2">
          <Layers className="size-4 text-primary-bright" />
          <input
            value={layer.name}
            disabled={disabled}
            onChange={(event) => onChange({ name: event.target.value })}
            className="min-w-0 flex-1 bg-transparent text-xs font-semibold text-foreground outline-none disabled:text-faint-foreground"
            aria-label={t('prototype.activeLayer')}
          />
        </div>
        <div className="mt-2 grid grid-cols-4 gap-1.5">
          <IconAction icon={layer.visible ? Eye : EyeOff} label={layer.visible ? t('prototype.visible') : t('prototype.hidden')} onClick={onToggleVisible} />
          <IconAction icon={layer.locked ? Lock : Unlock} label={layer.locked ? t('prototype.locked') : t('prototype.unlocked')} onClick={onToggleLocked} />
          <IconAction icon={Copy} label={t('prototype.duplicateLayer')} onClick={onDuplicate} />
          <IconAction icon={RotateCcw} label={t('prototype.resetLayer')} onClick={onReset} disabled={disabled} />
        </div>
      </section>

      <section>
        <SectionLabel>{t('prototype.layout')}</SectionLabel>
        <div className="mt-2 grid grid-cols-2 gap-2">
          <NumberField label="X" value={layer.x} disabled={disabled} onChange={(value) => onChange({ x: value })} />
          <NumberField label="Y" value={layer.y} disabled={disabled} onChange={(value) => onChange({ y: value })} />
          <NumberField label="W" value={layer.w} disabled={disabled} min={12} onChange={(value) => onChange({ w: value })} />
          <NumberField label="H" value={layer.h} disabled={disabled} min={12} onChange={(value) => onChange({ h: value })} />
        </div>
        <div className="mt-3 grid grid-cols-[1fr_34px_1fr] items-center gap-1.5">
          <span />
          <IconAction icon={ArrowUp} label={t('prototype.nudgeUp')} onClick={() => onNudge(0, -8)} disabled={disabled} />
          <span />
          <IconAction icon={ArrowLeft} label={t('prototype.nudgeLeft')} onClick={() => onNudge(-8, 0)} disabled={disabled} />
          <IconAction icon={Move} label={t('prototype.nudge')} onClick={() => onNudge(0, 0)} disabled={disabled} />
          <IconAction icon={ArrowRight} label={t('prototype.nudgeRight')} onClick={() => onNudge(8, 0)} disabled={disabled} />
          <span />
          <IconAction icon={ArrowDown} label={t('prototype.nudgeDown')} onClick={() => onNudge(0, 8)} disabled={disabled} />
          <span />
        </div>
      </section>

      <section>
        <SectionLabel>{t('prototype.appearance')}</SectionLabel>
        <div className="mt-2 space-y-3">
          <RangeField label={t('prototype.radius')} value={layer.radius} min={0} max={64} disabled={disabled} onChange={(value) => onChange({ radius: value })} />
          <RangeField label={t('prototype.opacity')} value={layer.opacity} min={12} max={100} disabled={disabled} onChange={(value) => onChange({ opacity: value })} suffix="%" />
          <RangeField label={t('prototype.rotation')} value={layer.rotation} min={-30} max={30} disabled={disabled} onChange={(value) => onChange({ rotation: value })} suffix="deg" />
          <RangeField label={t('prototype.textSize')} value={layer.textSize ?? 14} min={10} max={32} disabled={disabled || layer.kind !== 'text'} onChange={(value) => onChange({ textSize: value })} suffix="px" />
        </div>
        <div className="mt-3">
          <div className="mb-2 text-[11px] font-medium text-muted-foreground">{t('prototype.fill')}</div>
          <div className="grid grid-cols-8 gap-1.5">
            {COLOR_SWATCHES.map((color) => (
              <button
                key={color}
                type="button"
                disabled={disabled}
                onClick={() => onChange({ fill: color })}
                className={cn(
                  'size-6 rounded-md border border-border disabled:opacity-40',
                  layer.fill === color && 'ring-2 ring-primary ring-offset-1 ring-offset-surface',
                )}
                style={{ backgroundColor: color }}
                aria-label={`${t('prototype.fill')} ${color}`}
              />
            ))}
          </div>
        </div>
      </section>

      <section>
        <SectionLabel>{t('prototype.imageAdjustments')}</SectionLabel>
        {layer.kind === 'image' ? (
          <div className="mt-2 space-y-3">
            <RangeField label={t('prototype.cropX')} value={layer.cropX ?? 50} min={0} max={100} disabled={disabled} onChange={(value) => onChange({ cropX: value })} suffix="%" />
            <RangeField label={t('prototype.cropY')} value={layer.cropY ?? 50} min={0} max={100} disabled={disabled} onChange={(value) => onChange({ cropY: value })} suffix="%" />
            <RangeField label={t('prototype.imageScale')} value={layer.imageScale ?? 118} min={80} max={180} disabled={disabled} onChange={(value) => onChange({ imageScale: value })} suffix="%" />
            <RangeField label={t('prototype.brightness')} value={layer.brightness ?? 100} min={50} max={150} disabled={disabled} onChange={(value) => onChange({ brightness: value })} suffix="%" />
            <RangeField label={t('prototype.contrast')} value={layer.contrast ?? 100} min={50} max={160} disabled={disabled} onChange={(value) => onChange({ contrast: value })} suffix="%" />
            <RangeField label={t('prototype.saturation')} value={layer.saturation ?? 100} min={0} max={180} disabled={disabled} onChange={(value) => onChange({ saturation: value })} suffix="%" />
            <RangeField label={t('prototype.blur')} value={layer.blur ?? 0} min={0} max={8} disabled={disabled} onChange={(value) => onChange({ blur: value })} suffix="px" />
          </div>
        ) : (
          <div className="mt-2 rounded-lg border border-dashed border-border bg-surface-2 p-3 text-[11px] leading-relaxed text-faint-foreground">
            {t('prototype.selectImageLayer')}
          </div>
        )}
      </section>

      <button
        type="button"
        onClick={onDelete}
        disabled={disabled}
        className="inline-flex w-full items-center justify-center gap-1.5 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs font-medium text-destructive hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-45"
      >
        {t('prototype.deleteLayer')}
      </button>
    </div>
  )
}

function EmptyInspector() {
  const { t } = useI18n()

  return (
    <div className="rounded-lg border border-dashed border-border bg-surface-2 p-4 text-center">
      <MousePointer2 className="mx-auto size-5 text-faint-foreground" />
      <div className="mt-2 text-xs font-semibold text-foreground">{t('prototype.noLayerSelected')}</div>
      <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">
        {t('prototype.selectLayerCopy')}
      </p>
    </div>
  )
}

function InfoTile({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-border bg-surface-2 p-2">
      <div className="text-[10px] uppercase tracking-wide text-faint-foreground">{label}</div>
      <div className="mt-1 truncate text-xs font-medium text-foreground">{value}</div>
    </div>
  )
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-[11px] font-semibold uppercase tracking-wider text-faint-foreground">
      {children}
    </div>
  )
}

function NumberField({
  label,
  value,
  min = -999,
  max = 999,
  disabled,
  onChange,
}: {
  label: string
  value: number
  min?: number
  max?: number
  disabled?: boolean
  onChange: (value: number) => void
}) {
  return (
    <label className="block rounded-lg border border-border bg-surface-2 px-2 py-1.5">
      <span className="text-[10px] font-medium text-faint-foreground">{label}</span>
      <input
        type="number"
        value={value}
        min={min}
        max={max}
        disabled={disabled}
        onChange={(event) => onChange(clamp(parsedNumber(event.target.value, value), min, max))}
        className="mt-0.5 w-full bg-transparent text-xs font-medium text-foreground outline-none disabled:text-faint-foreground"
      />
    </label>
  )
}

function RangeField({
  label,
  value,
  min,
  max,
  suffix,
  disabled,
  onChange,
}: {
  label: string
  value: number
  min: number
  max: number
  suffix?: string
  disabled?: boolean
  onChange: (value: number) => void
}) {
  return (
    <label className="block">
      <span className="mb-1 flex items-center justify-between gap-2 text-[11px] font-medium text-muted-foreground">
        <span>{label}</span>
        <span className="font-mono text-faint-foreground">
          {value}
          {suffix}
        </span>
      </span>
      <input
        type="range"
        value={value}
        min={min}
        max={max}
        disabled={disabled}
        onChange={(event) => onChange(parsedNumber(event.target.value, value))}
        className="h-1.5 w-full accent-primary disabled:opacity-45"
      />
    </label>
  )
}

function IconAction({
  icon: Icon,
  label,
  onClick,
  disabled,
}: {
  icon: typeof Frame
  label: string
  onClick: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="inline-flex h-8 items-center justify-center rounded-md border border-border bg-surface-2 text-faint-foreground hover:border-primary/35 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-45"
      aria-label={label}
      title={label}
    >
      <Icon className="size-3.5" />
    </button>
  )
}
