import assert from 'node:assert/strict'
import { parseWorksflowRoute, teamPathFor } from '../lib/worksflow/route-state'

const path = teamPathFor('team / north', 'project / alpha', 'blueprint')
assert.equal(path, '/team/team%20%2F%20north/project/project%20%2F%20alpha/blueprint')
assert.deepEqual(parseWorksflowRoute(path), {
  surface: 'team',
  teamId: 'team / north',
  projectId: 'project / alpha',
  teamView: 'blueprint',
})

assert.deepEqual(parseWorksflowRoute('/team/acme/not-project/p1/editor'), {
  surface: 'team',
  teamId: undefined,
  projectId: undefined,
  teamView: 'dashboard',
})

console.log('route-state tests passed')
