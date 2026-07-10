import assert from 'node:assert/strict'
import { INITIAL_TEAM_PROJECTS } from '../lib/worksflow/project-model'
import { isTeamProjectList } from '../lib/worksflow/team-persistence'

assert.equal(isTeamProjectList(INITIAL_TEAM_PROJECTS), true)
assert.equal(isTeamProjectList([]), false)
assert.equal(isTeamProjectList([{ ...INITIAL_TEAM_PROJECTS[0], documents: [{}] }]), false)
assert.equal(
  isTeamProjectList([INITIAL_TEAM_PROJECTS[0], { ...INITIAL_TEAM_PROJECTS[0] }]),
  false,
)
console.log('4 team persistence guard test(s) passed.')
