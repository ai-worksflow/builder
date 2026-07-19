import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const suiteId = process.argv[2]?.trim() ?? ''
const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const manifest = JSON.parse(readFileSync(resolve(repositoryRoot, 'qualification/manifest.json'), 'utf8'))
const suite = manifest.suites.find((candidate) => candidate.id === suiteId)
if (!suite) throw new Error(`unknown qualification suite ${suiteId || '<empty>'}`)

const blockers = Array.isArray(suite.blockers) ? suite.blockers : ['Qualification is not implemented.']
throw new Error(`${suiteId} is ${suite.status}: ${blockers.join(' ')}`)
