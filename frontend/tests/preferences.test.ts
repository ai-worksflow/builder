import assert from 'node:assert/strict'
import {
  DEFAULT_WORKSFLOW_PREFERENCES,
  isWorksflowPreferences,
  updatePreferences,
} from '../lib/worksflow/preferences'

assert.equal(isWorksflowPreferences(DEFAULT_WORKSFLOW_PREFERENCES), true)
assert.equal(isWorksflowPreferences({ ...DEFAULT_WORKSFLOW_PREFERENCES, generationMode: 'unknown' }), false)
assert.equal(updatePreferences(DEFAULT_WORKSFLOW_PREFERENCES, { defaultProjectRole: 'viewer' }).defaultProjectRole, 'viewer')
assert.equal(updatePreferences(DEFAULT_WORKSFLOW_PREFERENCES, { generationModel: '' }).generationModel, 'gpt-5.5')
console.log('4 preferences test(s) passed.')
