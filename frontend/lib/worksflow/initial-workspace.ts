import { CODE_FILES } from './mock-data'
import { createWorkspace } from './workspace-model'

const previewFiles = [
  {
    path: 'index.html',
    language: 'html',
    content: `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="color-scheme" content="dark">
    <title>Taskflow</title>
    <link rel="stylesheet" href="styles.css">
  </head>
  <body>
    <main class="app-shell">
      <header class="topbar">
        <div class="brand"><span class="brand-mark">T</span><span>Taskflow</span></div>
        <dl class="stats">
          <div><dt>Active</dt><dd id="active-count">0</dd></div>
          <div><dt>Done</dt><dd id="done-count">0</dd></div>
          <div><dt>Total</dt><dd id="total-count">0</dd></div>
        </dl>
      </header>
      <form id="task-form" class="composer">
        <label class="sr-only" for="task-title">Add a new task</label>
        <input id="task-title" name="title" placeholder="Add a new task..." autocomplete="off" required>
        <select id="task-priority" name="priority" aria-label="Priority">
          <option>Low</option><option selected>Med</option><option>High</option>
        </select>
        <button type="submit">Add task</button>
      </form>
      <nav class="filters" aria-label="Task filters">
        <button type="button" data-filter="all" aria-pressed="true">All</button>
        <button type="button" data-filter="active" aria-pressed="false">Active</button>
        <button type="button" data-filter="completed" aria-pressed="false">Completed</button>
      </nav>
      <ul id="task-list" class="task-list" aria-live="polite"></ul>
      <p id="empty-state" class="empty-state" hidden>No tasks match this filter.</p>
    </main>
    <footer>Made in Worksflow</footer>
    <script type="module" src="app.js"></script>
  </body>
</html>`,
  },
  {
    path: 'styles.css',
    language: 'css',
    content: `:root { color-scheme: dark; font-family: Inter, ui-sans-serif, system-ui, sans-serif; background: #111114; color: #fff; }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100vh; background: radial-gradient(circle at top, #202838, #111114 42%); }
button, input, select { font: inherit; }
button { cursor: pointer; }
.app-shell { width: min(720px, calc(100% - 32px)); margin: 0 auto; padding: 32px 0 72px; }
.topbar { display: flex; align-items: center; justify-content: space-between; gap: 24px; margin-bottom: 28px; }
.brand { display: flex; align-items: center; gap: 10px; font-size: 20px; font-weight: 750; }
.brand-mark { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 10px; background: #1488fc; }
.stats { display: flex; gap: 18px; margin: 0; }
.stats div { display: flex; gap: 5px; align-items: baseline; }
.stats dt { color: #92929b; font-size: 12px; }
.stats dd { order: -1; margin: 0; font-weight: 700; }
.composer { display: grid; grid-template-columns: 1fr auto auto; gap: 8px; margin-bottom: 16px; }
input, select { min-width: 0; border: 1px solid #33343a; border-radius: 9px; background: #1e1e22; color: #fff; padding: 11px 12px; outline: none; }
input:focus, select:focus { border-color: #1488fc; box-shadow: 0 0 0 3px #1488fc26; }
.composer button { border: 0; border-radius: 9px; background: #1488fc; color: white; padding: 0 16px; font-weight: 700; }
.filters { display: flex; gap: 5px; margin-bottom: 16px; }
.filters button { border: 0; border-radius: 7px; background: transparent; color: #92929b; padding: 7px 11px; }
.filters button[aria-pressed="true"] { background: #1488fc26; color: #41a9ff; }
.task-list { display: grid; gap: 9px; padding: 0; list-style: none; }
.task { display: flex; align-items: center; gap: 12px; border: 1px solid #2a2a30; border-radius: 11px; background: #1b1b1f; padding: 13px 14px; }
.task input { width: 19px; height: 19px; accent-color: #4ade80; }
.task-title { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.task.done .task-title { color: #73737c; text-decoration: line-through; }
.priority { border-radius: 999px; background: #ffffff0a; color: #a2a2aa; padding: 3px 8px; font-size: 11px; }
.priority.High { color: #fca5a5; background: #ef44441a; }
.priority.Low { color: #86efac; background: #22c55e1a; }
.empty-state { border: 1px dashed #34343a; border-radius: 11px; color: #777780; padding: 44px 20px; text-align: center; }
footer { position: fixed; inset: auto 0 0; border-top: 1px solid #ffffff0d; background: #111114e6; color: #66666e; padding: 11px; text-align: center; font-size: 11px; backdrop-filter: blur(12px); }
.sr-only { position: absolute; width: 1px; height: 1px; clip: rect(0, 0, 0, 0); overflow: hidden; }
@media (max-width: 560px) { .topbar { align-items: flex-start; flex-direction: column; } .composer { grid-template-columns: 1fr auto; } .composer input { grid-column: 1 / -1; } .stats { width: 100%; justify-content: space-between; } }`,
  },
  {
    path: 'app.js',
    language: 'javascript',
    content: `const tasks = [
  { id: 1, title: 'Review the quarterly product roadmap', priority: 'High', done: false },
  { id: 2, title: 'Prepare slides for the design review meeting', priority: 'Med', done: false },
  { id: 3, title: 'Send the onboarding welcome email', priority: 'Low', done: true },
]

let filter = 'all'
const list = document.querySelector('#task-list')
const empty = document.querySelector('#empty-state')

function render() {
  const visible = tasks.filter((task) => filter === 'all' || (filter === 'active' ? !task.done : task.done))
  list.innerHTML = visible.map((task) => \`<li class="task \${task.done ? 'done' : ''}">
    <input type="checkbox" data-task-id="\${task.id}" \${task.done ? 'checked' : ''} aria-label="Toggle \${task.title}">
    <span class="task-title">\${task.title}</span>
    <span class="priority \${task.priority}">\${task.priority}</span>
  </li>\`).join('')
  empty.hidden = visible.length > 0
  document.querySelector('#active-count').textContent = String(tasks.filter((task) => !task.done).length)
  document.querySelector('#done-count').textContent = String(tasks.filter((task) => task.done).length)
  document.querySelector('#total-count').textContent = String(tasks.length)
}

document.querySelector('#task-form').addEventListener('submit', (event) => {
  event.preventDefault()
  const data = new FormData(event.currentTarget)
  tasks.unshift({ id: Date.now(), title: String(data.get('title')), priority: String(data.get('priority')), done: false })
  event.currentTarget.reset()
  render()
})

document.querySelector('.filters').addEventListener('click', (event) => {
  const button = event.target.closest('[data-filter]')
  if (!button) return
  filter = button.dataset.filter
  document.querySelectorAll('[data-filter]').forEach((item) => item.setAttribute('aria-pressed', String(item === button)))
  render()
})

list.addEventListener('change', (event) => {
  const task = tasks.find((item) => item.id === Number(event.target.dataset.taskId))
  if (task) task.done = event.target.checked
  render()
})

render()`,
  },
]

export function createInitialWorkspace(projectName = 'Simple Todo App') {
  return createWorkspace({
    id: 'workspace-p1',
    name: projectName,
    files: [
      ...previewFiles,
      ...CODE_FILES.map((file) => ({
        path: file.path,
        content: file.content,
        language: file.language,
        dirty: false,
      })),
    ],
  })
}
