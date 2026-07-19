import assert from 'node:assert/strict'
import { resolveCandidateHeadSelection } from '../lib/platform/repository-candidate-head'

function head(candidateId: string, buildManifestId: string) {
  return { candidate: { id: candidateId, buildManifest: { id: buildManifestId } } }
}

const oldOne = head('candidate-old-1', 'manifest-old-1')
const oldTwo = head('candidate-old-2', 'manifest-old-2')
const exactOne = head('candidate-exact-1', 'manifest-target')
const exactTwo = head('candidate-exact-2', 'manifest-target')

assert.deepEqual(resolveCandidateHeadSelection([], 'manifest-target'), { kind: 'none' })
assert.deepEqual(resolveCandidateHeadSelection([oldOne], 'manifest-target'), {
  kind: 'selected', head: oldOne,
})
assert.deepEqual(resolveCandidateHeadSelection([oldOne, exactOne, oldTwo], 'manifest-target'), {
  kind: 'selected', head: exactOne,
})
assert.deepEqual(resolveCandidateHeadSelection([oldOne, oldTwo], 'manifest-target'), {
  kind: 'ambiguous', heads: [oldOne, oldTwo],
})
assert.deepEqual(resolveCandidateHeadSelection([oldOne, exactOne, exactTwo], 'manifest-target'), {
  kind: 'ambiguous', heads: [exactOne, exactTwo],
})

console.log('repository Candidate head selection tests passed')
