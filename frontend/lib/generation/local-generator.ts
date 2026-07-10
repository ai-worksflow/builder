import { parseGeneratedWorkspace } from './schema'
import type {
  GeneratedWorkspace,
  GenerationMode,
  GenerationPlanTask,
  GenerationRequest,
  GenerationResult,
} from './types'

export const LOCAL_GENERATOR_MODEL = 'local-deterministic-v1'

interface LocalProfile {
  id: string
  keywords: RegExp
  eyebrow: string
  fallbackTitle: string
  description: string
  itemLabel: string
  itemPlural: string
  actionLabel: string
  placeholder: string
  starterItems: string[]
  metrics: [string, string, string]
}

const LOCAL_PROFILES: LocalProfile[] = [
  {
    id: 'tasks',
    keywords: /todo|task|project|kanban|待办|任务|项目|看板/i,
    eyebrow: 'Focused execution',
    fallbackTitle: 'Momentum Board',
    description: 'A calm task workspace that turns ideas into visible progress.',
    itemLabel: 'task',
    itemPlural: 'tasks',
    actionLabel: 'Add task',
    placeholder: 'What needs to happen next?',
    starterItems: ['Shape the first milestone', 'Review the experience', 'Share the working draft'],
    metrics: ['Open', 'Completed', 'Momentum'],
  },
  {
    id: 'analytics',
    keywords: /dashboard|analytic|metric|report|insight|数据|仪表盘|分析|报表/i,
    eyebrow: 'Live intelligence',
    fallbackTitle: 'Signal Dashboard',
    description: 'A lightweight command center for signals, decisions and follow-up.',
    itemLabel: 'signal',
    itemPlural: 'signals',
    actionLabel: 'Track signal',
    placeholder: 'Add a metric or insight to watch',
    starterItems: ['Weekly activation is trending up', 'Review the conversion drop-off', 'Publish the team snapshot'],
    metrics: ['Active', 'Resolved', 'Signal score'],
  },
  {
    id: 'commerce',
    keywords: /shop|store|commerce|product|cart|catalog|商城|商店|商品|购物/i,
    eyebrow: 'Curated commerce',
    fallbackTitle: 'Field Supply',
    description: 'A polished product workspace for curating a small, memorable catalog.',
    itemLabel: 'product',
    itemPlural: 'products',
    actionLabel: 'Add product',
    placeholder: 'Name a product for the collection',
    starterItems: ['Canvas day pack', 'Modular desk tray', 'Everyday field notes'],
    metrics: ['Available', 'Curated', 'Collection fit'],
  },
  {
    id: 'portfolio',
    keywords: /portfolio|resume|case study|creative|作品集|简历|案例|设计师/i,
    eyebrow: 'Selected work',
    fallbackTitle: 'Independent Practice',
    description: 'A personal showcase built around clear stories and thoughtful craft.',
    itemLabel: 'project',
    itemPlural: 'projects',
    actionLabel: 'Add project',
    placeholder: 'Name a case study to feature',
    starterItems: ['Identity system', 'Editorial product', 'Digital service redesign'],
    metrics: ['Published', 'In progress', 'Story strength'],
  },
  {
    id: 'events',
    keywords: /event|conference|meetup|schedule|booking|活动|会议|日程|预约/i,
    eyebrow: 'Designed gatherings',
    fallbackTitle: 'Common Ground',
    description: 'A welcoming event planner for the moments people remember.',
    itemLabel: 'session',
    itemPlural: 'sessions',
    actionLabel: 'Add session',
    placeholder: 'Add a session or moment',
    starterItems: ['Opening conversation', 'Hands-on studio', 'Community supper'],
    metrics: ['Upcoming', 'Confirmed', 'Energy'],
  },
  {
    id: 'content',
    keywords: /blog|writing|content|note|journal|knowledge|博客|写作|内容|笔记|知识/i,
    eyebrow: 'Thoughtful publishing',
    fallbackTitle: 'Margin Notes',
    description: 'A focused editorial space for shaping ideas worth returning to.',
    itemLabel: 'note',
    itemPlural: 'notes',
    actionLabel: 'Add note',
    placeholder: 'Capture the next idea',
    starterItems: ['A question worth exploring', 'Notes from the field', 'The shape of the next essay'],
    metrics: ['Drafts', 'Published', 'Clarity'],
  },
  {
    id: 'workspace',
    keywords: /.*/,
    eyebrow: 'Purpose-built workspace',
    fallbackTitle: 'Northstar Studio',
    description: 'A responsive, local-first workspace shaped directly from your brief.',
    itemLabel: 'item',
    itemPlural: 'items',
    actionLabel: 'Add item',
    placeholder: 'Add the next important item',
    starterItems: ['Define the primary outcome', 'Shape the core experience', 'Validate with real feedback'],
    metrics: ['Active', 'Completed', 'Progress'],
  },
]

const PALETTES = [
  { accent: '#ff6b4a', accentSoft: '#ffe1d8', ink: '#17211d', canvas: '#f4f0e8' },
  { accent: '#4e6fff', accentSoft: '#dfe5ff', ink: '#162033', canvas: '#eef1f7' },
  { accent: '#078b75', accentSoft: '#d6f1e8', ink: '#14251f', canvas: '#edf3ef' },
  { accent: '#a655e7', accentSoft: '#efdefb', ink: '#27182f', canvas: '#f4eff6' },
  { accent: '#d48b12', accentSoft: '#f8e7c4', ink: '#2a2112', canvas: '#f5f0e6' },
]

function hashString(value: string) {
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return hash >>> 0
}

function escapeHtml(value: string) {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;')
}

function jsonForInlineScript(value: unknown) {
  return JSON.stringify(value)
    .replace(/</g, '\\u003c')
    .replace(/>/g, '\\u003e')
    .replace(/&/g, '\\u0026')
}

function replaceControlCharacters(value: string) {
  return Array.from(value)
    .map((character) => character.charCodeAt(0) < 32 ? ' ' : character)
    .join('')
}

function profileForPrompt(prompt: string) {
  return LOCAL_PROFILES.find((profile) => profile.keywords.test(prompt)) ?? LOCAL_PROFILES.at(-1) as LocalProfile
}

function titleFromPrompt(prompt: string, fallback: string) {
  const firstLine = replaceControlCharacters(prompt)
    .replace(/\s+/g, ' ')
    .trim()
    .split(/[.!?。！？]/)[0]
    .trim()
  if (!firstLine) return fallback

  if (/[\u3400-\u9fff]/.test(firstLine)) {
    return firstLine.replace(/^(请|帮我|创建|生成|制作|实现)/, '').trim().slice(0, 18) || fallback
  }

  const title = firstLine
    .replace(/^(please\s+)?(build|create|make|design|generate|implement)\s+(me\s+)?(an?\s+|the\s+)?/i, '')
    .split(' ')
    .slice(0, 7)
    .join(' ')
  if (title.length < 3) return fallback
  return title.charAt(0).toUpperCase() + title.slice(1)
}

function modeTasks(mode: GenerationMode, profile: LocalProfile): GenerationPlanTask[] {
  const leadByMode: Record<GenerationMode, GenerationPlanTask> = {
    plan: {
      id: 'shape-scope',
      title: 'Shape the product scope',
      description: `Translate the brief into the primary ${profile.itemPlural}, user actions and success signals.`,
    },
    build: {
      id: 'build-shell',
      title: 'Build the responsive shell',
      description: 'Create the complete semantic page structure and a distinctive responsive visual system.',
    },
    iterate: {
      id: 'preserve-intent',
      title: 'Preserve and improve the current intent',
      description: 'Use the existing workspace as context while tightening hierarchy, language and interactions.',
    },
    fix: {
      id: 'stabilize-flow',
      title: 'Stabilize the primary flow',
      description: 'Rebuild the essential experience with defensive browser behavior and no external dependencies.',
    },
  }

  return [
    leadByMode[mode],
    {
      id: `model-${profile.id}`,
      title: `Model the ${profile.itemLabel} experience`,
      description: `Provide meaningful starter ${profile.itemPlural}, filtering, completion and lightweight persistence.`,
    },
    {
      id: 'wire-interactions',
      title: 'Wire real interactions',
      description: 'Implement creation, status changes, deletion, filtering, theme controls and local persistence.',
    },
    {
      id: 'verify-runnable',
      title: 'Deliver a runnable workspace',
      description: 'Package the experience as one dependency-free index.html file with embedded CSS and JavaScript.',
    },
  ]
}

function buildHtml(
  request: GenerationRequest,
  profile: LocalProfile,
  title: string,
  seed: number,
) {
  const palette = PALETTES[seed % PALETTES.length]
  const layoutVariant = seed % 3
  const promptExcerpt = request.prompt.replace(/\s+/g, ' ').trim().slice(0, 180)
  const initialItems = profile.starterItems.map((item, index) => ({
    id: `${profile.id}-${seed}-${index}`,
    title: item,
    done: index === 1 && seed % 2 === 0,
  }))
  const config = jsonForInlineScript({
    storageKey: `worksflow-local-${profile.id}-${seed}`,
    itemLabel: profile.itemLabel,
    itemPlural: profile.itemPlural,
    metrics: profile.metrics,
    initialItems,
  })

  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="description" content="${escapeHtml(profile.description)}">
  <title>${escapeHtml(title)}</title>
  <style>
    :root {
      color-scheme: light;
      --accent: ${palette.accent};
      --accent-soft: ${palette.accentSoft};
      --ink: ${palette.ink};
      --canvas: ${palette.canvas};
      --panel: rgba(255, 255, 255, 0.78);
      --line: rgba(23, 33, 29, 0.13);
      --muted: rgba(23, 33, 29, 0.62);
      --shadow: 0 24px 70px rgba(24, 31, 28, 0.11);
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }

    * { box-sizing: border-box; }

    body {
      margin: 0;
      min-height: 100vh;
      color: var(--ink);
      background:
        radial-gradient(circle at 12% 5%, var(--accent-soft), transparent 28rem),
        linear-gradient(145deg, var(--canvas), #faf9f5 62%, var(--accent-soft));
      transition: color 180ms ease, background 180ms ease;
    }

    body.dark {
      color-scheme: dark;
      --ink: #f5f3ed;
      --canvas: #111714;
      --panel: rgba(25, 34, 30, 0.82);
      --line: rgba(255, 255, 255, 0.12);
      --muted: rgba(245, 243, 237, 0.64);
      --shadow: 0 26px 80px rgba(0, 0, 0, 0.32);
      background: radial-gradient(circle at 12% 5%, color-mix(in srgb, var(--accent) 30%, transparent), transparent 30rem), #111714;
    }

    button, input { font: inherit; }
    button { color: inherit; }

    .shell {
      width: min(1180px, calc(100% - 32px));
      margin: 0 auto;
      padding: 24px 0 56px;
    }

    .topbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 20px;
      margin-bottom: 64px;
    }

    .brand { display: flex; align-items: center; gap: 11px; font-weight: 760; letter-spacing: -0.025em; }
    .brand-mark { width: 13px; height: 13px; border-radius: 4px 9px 4px 9px; background: var(--accent); transform: rotate(8deg); }

    .icon-button {
      width: 42px;
      height: 42px;
      border: 1px solid var(--line);
      border-radius: 999px;
      background: var(--panel);
      cursor: pointer;
      backdrop-filter: blur(16px);
    }

    .hero { display: grid; grid-template-columns: minmax(0, 1.45fr) minmax(230px, .55fr); gap: 42px; align-items: end; margin-bottom: 36px; }
    .eyebrow { margin: 0 0 14px; color: var(--accent); text-transform: uppercase; letter-spacing: .16em; font-size: 12px; font-weight: 800; }
    h1 { max-width: 780px; margin: 0; font-family: Georgia, "Times New Roman", serif; font-size: clamp(48px, 8vw, 92px); font-weight: 500; line-height: .92; letter-spacing: -.065em; }
    .hero-copy { margin: 0 0 4px; color: var(--muted); font-size: 16px; line-height: 1.65; }

    .workspace {
      display: grid;
      grid-template-columns: minmax(0, 1.42fr) minmax(280px, .58fr);
      gap: 20px;
    }

    .panel {
      border: 1px solid var(--line);
      border-radius: 28px;
      background: var(--panel);
      box-shadow: var(--shadow);
      backdrop-filter: blur(22px);
    }

    .primary { padding: 26px; }
    .aside { padding: 26px; display: flex; flex-direction: column; justify-content: space-between; min-height: 430px; }
    .variant-1 .workspace { grid-template-columns: minmax(280px, .58fr) minmax(0, 1.42fr); }
    .variant-1 .primary { order: 2; }
    .variant-2 .workspace { grid-template-columns: 1fr; }
    .variant-2 .aside { min-height: auto; display: grid; grid-template-columns: 1fr 1fr; gap: 28px; }

    .panel-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 22px; }
    h2 { margin: 0; font-size: 20px; letter-spacing: -.025em; }
    .count { color: var(--muted); font-size: 13px; }

    form { display: flex; gap: 10px; margin-bottom: 20px; }
    input {
      width: 100%;
      min-width: 0;
      border: 1px solid var(--line);
      border-radius: 15px;
      padding: 13px 15px;
      color: var(--ink);
      background: rgba(255, 255, 255, .42);
      outline: none;
    }
    body.dark input { background: rgba(255, 255, 255, .05); }
    input:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }

    .action {
      flex: 0 0 auto;
      border: 0;
      border-radius: 15px;
      padding: 0 18px;
      background: var(--accent);
      color: white;
      font-weight: 760;
      cursor: pointer;
      box-shadow: 0 9px 24px color-mix(in srgb, var(--accent) 28%, transparent);
    }

    .filters { display: flex; gap: 8px; margin-bottom: 16px; }
    .filter { border: 1px solid var(--line); border-radius: 999px; padding: 7px 12px; background: transparent; color: var(--muted); cursor: pointer; font-size: 12px; font-weight: 700; }
    .filter.active { border-color: var(--accent); color: var(--ink); background: var(--accent-soft); }

    .items { display: grid; gap: 10px; }
    .item { display: grid; grid-template-columns: auto 1fr auto; gap: 13px; align-items: center; padding: 15px; border: 1px solid var(--line); border-radius: 17px; background: rgba(255,255,255,.3); }
    body.dark .item { background: rgba(255,255,255,.025); }
    .check { width: 22px; height: 22px; display: grid; place-items: center; border: 1px solid var(--line); border-radius: 8px; background: transparent; cursor: pointer; font-size: 12px; }
    .item.done .check { border-color: var(--accent); background: var(--accent); color: white; }
    .item.done .item-title { color: var(--muted); text-decoration: line-through; }
    .delete { border: 0; background: transparent; color: var(--muted); cursor: pointer; font-size: 18px; }
    .empty { padding: 32px; border: 1px dashed var(--line); border-radius: 17px; text-align: center; color: var(--muted); }

    .metrics { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
    .metric { padding: 13px; border: 1px solid var(--line); border-radius: 16px; }
    .metric strong { display: block; margin-bottom: 5px; font-size: 24px; letter-spacing: -.04em; }
    .metric span { color: var(--muted); font-size: 11px; }
    .brief { margin-top: 34px; }
    .brief-label { margin: 0 0 9px; color: var(--muted); font-size: 11px; font-weight: 800; text-transform: uppercase; letter-spacing: .12em; }
    .brief p:last-child { margin: 0; font-family: Georgia, "Times New Roman", serif; font-size: 20px; line-height: 1.35; }
    .status { display: inline-flex; align-items: center; gap: 8px; margin-top: 24px; color: var(--muted); font-size: 12px; }
    .status::before { content: ""; width: 8px; height: 8px; border-radius: 50%; background: var(--accent); box-shadow: 0 0 0 5px var(--accent-soft); }

    @media (max-width: 760px) {
      .shell { width: min(100% - 22px, 1180px); padding-top: 15px; }
      .topbar { margin-bottom: 44px; }
      .hero, .workspace, .variant-1 .workspace { grid-template-columns: 1fr; }
      .variant-1 .primary { order: initial; }
      .hero { gap: 22px; }
      .hero-copy { max-width: 38rem; }
      .variant-2 .aside { grid-template-columns: 1fr; }
      .metrics { grid-template-columns: 1fr 1fr 1fr; }
    }

    @media (max-width: 470px) {
      form { flex-direction: column; }
      .action { min-height: 46px; }
      .metrics { grid-template-columns: 1fr; }
      h1 { font-size: 48px; }
    }
  </style>
</head>
<body>
  <main class="shell variant-${layoutVariant}">
    <nav class="topbar" aria-label="Primary navigation">
      <div class="brand"><span class="brand-mark" aria-hidden="true"></span>${escapeHtml(title)}</div>
      <button class="icon-button" id="theme-toggle" type="button" aria-label="Toggle color theme">◐</button>
    </nav>

    <section class="hero">
      <div>
        <p class="eyebrow">${escapeHtml(profile.eyebrow)}</p>
        <h1>${escapeHtml(title)}</h1>
      </div>
      <p class="hero-copy">${escapeHtml(profile.description)}</p>
    </section>

    <section class="workspace" aria-label="${escapeHtml(profile.itemPlural)} workspace">
      <div class="panel primary">
        <div class="panel-heading">
          <h2>Your ${escapeHtml(profile.itemPlural)}</h2>
          <span class="count" id="item-count" aria-live="polite"></span>
        </div>
        <form id="item-form">
          <input id="item-input" maxlength="100" autocomplete="off" placeholder="${escapeHtml(profile.placeholder)}" aria-label="New ${escapeHtml(profile.itemLabel)}">
          <button class="action" type="submit">${escapeHtml(profile.actionLabel)}</button>
        </form>
        <div class="filters" role="group" aria-label="Filter ${escapeHtml(profile.itemPlural)}">
          <button class="filter active" type="button" data-filter="all">All</button>
          <button class="filter" type="button" data-filter="open">Open</button>
          <button class="filter" type="button" data-filter="done">Done</button>
        </div>
        <div class="items" id="items"></div>
      </div>

      <aside class="panel aside">
        <div>
          <div class="metrics">
            <div class="metric"><strong id="metric-open">0</strong><span>${escapeHtml(profile.metrics[0])}</span></div>
            <div class="metric"><strong id="metric-done">0</strong><span>${escapeHtml(profile.metrics[1])}</span></div>
            <div class="metric"><strong id="metric-score">0%</strong><span>${escapeHtml(profile.metrics[2])}</span></div>
          </div>
          <div class="brief">
            <p class="brief-label">Generated from your brief</p>
            <p>${escapeHtml(promptExcerpt)}</p>
          </div>
        </div>
        <span class="status">Saved locally in this browser</span>
      </aside>
    </section>
  </main>

  <script>
    const config = ${config}
    const itemForm = document.querySelector('#item-form')
    const itemInput = document.querySelector('#item-input')
    const itemsNode = document.querySelector('#items')
    const filters = Array.from(document.querySelectorAll('[data-filter]'))
    let activeFilter = 'all'
    let items

    try {
      const storedItems = JSON.parse(localStorage.getItem(config.storageKey) || 'null')
      items = Array.isArray(storedItems) ? storedItems : config.initialItems
    } catch {
      items = config.initialItems
    }

    function save() {
      try {
        localStorage.setItem(config.storageKey, JSON.stringify(items))
      } catch {
        // Sandboxed previews can intentionally disable origin storage.
      }
    }

    function visibleItems() {
      if (activeFilter === 'open') return items.filter((item) => !item.done)
      if (activeFilter === 'done') return items.filter((item) => item.done)
      return items
    }

    function render() {
      const visible = visibleItems()
      itemsNode.replaceChildren()

      if (visible.length === 0) {
        const empty = document.createElement('div')
        empty.className = 'empty'
        empty.textContent = 'No ' + config.itemPlural + ' in this view yet.'
        itemsNode.append(empty)
      }

      visible.forEach((item) => {
        const row = document.createElement('article')
        row.className = 'item' + (item.done ? ' done' : '')

        const check = document.createElement('button')
        check.type = 'button'
        check.className = 'check'
        check.setAttribute('aria-label', (item.done ? 'Reopen ' : 'Complete ') + item.title)
        check.textContent = item.done ? '✓' : ''
        check.addEventListener('click', () => {
          item.done = !item.done
          save()
          render()
        })

        const title = document.createElement('span')
        title.className = 'item-title'
        title.textContent = item.title

        const remove = document.createElement('button')
        remove.type = 'button'
        remove.className = 'delete'
        remove.setAttribute('aria-label', 'Delete ' + item.title)
        remove.textContent = '×'
        remove.addEventListener('click', () => {
          items = items.filter((candidate) => candidate.id !== item.id)
          save()
          render()
        })

        row.append(check, title, remove)
        itemsNode.append(row)
      })

      const done = items.filter((item) => item.done).length
      const open = items.length - done
      document.querySelector('#item-count').textContent = items.length + ' total'
      document.querySelector('#metric-open').textContent = String(open)
      document.querySelector('#metric-done').textContent = String(done)
      document.querySelector('#metric-score').textContent = (items.length ? Math.round(done / items.length * 100) : 0) + '%'
    }

    itemForm.addEventListener('submit', (event) => {
      event.preventDefault()
      const title = itemInput.value.trim()
      if (!title) return
      items.unshift({ id: String(Date.now()), title, done: false })
      itemInput.value = ''
      save()
      render()
    })

    filters.forEach((button) => {
      button.addEventListener('click', () => {
        activeFilter = button.dataset.filter
        filters.forEach((candidate) => candidate.classList.toggle('active', candidate === button))
        render()
      })
    })

    document.querySelector('#theme-toggle').addEventListener('click', () => {
      document.body.classList.toggle('dark')
    })

    render()
  </script>
</body>
</html>`
}

export function generateLocalWorkspace(request: GenerationRequest): GenerationResult {
  const profile = profileForPrompt(request.prompt)
  const seed = hashString(`${request.mode}:${request.prompt}:${request.currentFiles.map((file) => file.path).join('|')}`)
  const title = titleFromPrompt(request.prompt, profile.fallbackTitle)
  const planTasks = modeTasks(request.mode, profile)
  const currentWorkspaceNote = request.currentFiles.length > 0
    ? ` It incorporates the intent of ${request.currentFiles.length} current workspace file${request.currentFiles.length === 1 ? '' : 's'}.`
    : ''
  const workspace: GeneratedWorkspace = {
    plan: {
      title: `${title} implementation plan`,
      summary: `Create a dependency-free ${profile.id} experience that is responsive, persistent and immediately runnable.${currentWorkspaceNote}`,
      tasks: planTasks,
    },
    files: [
      {
        path: 'index.html',
        language: 'html',
        content: buildHtml(request, profile, title, seed),
      },
    ],
    summary: `Generated a functional single-file ${profile.id} workspace with responsive styling, accessible controls, filtering, state changes and browser persistence.`,
  }

  const validatedWorkspace = parseGeneratedWorkspace(workspace)
  const inputCharacters =
    request.prompt.length +
    request.currentFiles.reduce((total, file) => total + file.path.length + file.content.length, 0) +
    (request.attachments ?? []).reduce(
      (total, attachment) =>
        total + attachment.name.length + (attachment.kind === 'image' ? 0 : attachment.content.length),
      0,
    )
  const outputCharacters = validatedWorkspace.files.reduce(
    (total, file) => total + file.path.length + file.content.length,
    validatedWorkspace.summary.length + validatedWorkspace.plan.summary.length,
  )
  const inputTokens = Math.max(1, Math.ceil(inputCharacters / 4))
  const outputTokens = Math.max(1, Math.ceil(outputCharacters / 4))
  return {
    ...validatedWorkspace,
    runId: `local-${seed.toString(16)}`,
    provider: 'local',
    model: LOCAL_GENERATOR_MODEL,
    usage: {
      inputTokens,
      outputTokens,
      totalTokens: inputTokens + outputTokens,
      estimated: true,
    },
  }
}
