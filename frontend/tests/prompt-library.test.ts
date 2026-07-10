import assert from 'node:assert/strict'
import {
  BUILT_IN_PROMPT_TEMPLATES,
  BUILT_IN_PROMPT_WORKFLOWS,
  SLASH_COMMANDS,
  addPromptHistoryEntry,
  applySlashCommand,
  isPromptHistory,
  isPromptTemplateList,
  isPromptWorkflowList,
  searchPromptHistory,
  searchPromptTemplates,
  searchPromptWorkflows,
  redactSensitivePrompt,
  suggestSlashCommands,
  workflowPrompt,
} from '../lib/worksflow/prompt-library'

const tests: Array<{ name: string; run: () => void }> = [
  {
    name: 'searches and expands reusable multi-step workflows',
    run: () => {
      const matches = searchPromptWorkflows(BUILT_IN_PROMPT_WORKFLOWS, 'accessibility')
      assert.equal(matches[0]?.id, 'workflow-accessible-release')
      assert.equal(isPromptWorkflowList(BUILT_IN_PROMPT_WORKFLOWS), true)
      assert.match(workflowPrompt(BUILT_IN_PROMPT_WORKFLOWS[0]), /1\. Plan/)
    },
  },
  {
    name: 'redacts credentials before prompt history is persisted',
    run: () => {
      const prompt = redactSensitivePrompt('token=ghp_1234567890abcdefgh password=hunter2')
      assert.equal(prompt.includes('ghp_1234567890abcdefgh'), false)
      assert.equal(prompt.includes('hunter2'), false)
      const entry = { id: 'secret', prompt: 'api_key=sk-abcdefghijklmnop', mode: 'build' as const, model: 'gpt-5.5', status: 'completed' as const, createdAt: '2026-01-01' }
      assert.equal(addPromptHistoryEntry([], entry)[0].prompt.includes('sk-abcdefghijklmnop'), false)
    },
  },
  {
    name: 'searches reusable templates across titles, prompts and tags',
    run: () => {
      assert.equal(searchPromptTemplates(BUILT_IN_PROMPT_TEMPLATES, 'a11y')[0]?.id, 'template-accessibility')
      assert.equal(searchPromptTemplates(BUILT_IN_PROMPT_TEMPLATES, 'dashboard')[0]?.id, 'template-dashboard')
    },
  },
  {
    name: 'suggests slash commands only while typing the leading token',
    run: () => {
      assert.deepEqual(suggestSlashCommands('/fi').map((item) => item.command), ['/fix'])
      assert.deepEqual(suggestSlashCommands('please /fix'), [])
      assert.deepEqual(suggestSlashCommands('/fix now'), [])
    },
  },
  {
    name: 'expands slash commands without losing a supplied suffix',
    run: () => {
      const fix = SLASH_COMMANDS.find((item) => item.command === '/fix')!
      assert.deepEqual(applySlashCommand('/fix broken save button', fix), {
        prompt: `${fix.prompt}broken save button`,
        mode: 'fix',
      })
    },
  },
  {
    name: 'keeps newest prompt history first with a stable limit',
    run: () => {
      const first = { id: 'one', prompt: 'first', mode: 'build' as const, model: 'gpt-5.5', status: 'completed' as const, createdAt: '2026-01-01' }
      const second = { ...first, id: 'two', prompt: 'second' }
      assert.deepEqual(addPromptHistoryEntry([first], second, 1), [second])
      assert.deepEqual(addPromptHistoryEntry([first, second], { ...first, prompt: 'updated' }).map((item) => item.id), ['one', 'two'])
    },
  },
  {
    name: 'searches and validates persisted prompt data',
    run: () => {
      const history = [{ id: 'one', prompt: 'Repair focus ring', mode: 'fix' as const, model: 'gpt-5.5', status: 'completed' as const, createdAt: '2026-01-01' }]
      assert.equal(searchPromptHistory(history, 'focus').length, 1)
      assert.equal(isPromptHistory(history), true)
      assert.equal(isPromptHistory([{ ...history[0], status: 'unknown' }]), false)
      assert.equal(isPromptTemplateList(BUILT_IN_PROMPT_TEMPLATES), true)
    },
  },
]

let passed = 0
for (const test of tests) {
  test.run()
  passed += 1
  console.log(`✓ ${test.name}`)
}
console.log(`${passed} prompt library test(s) passed.`)
