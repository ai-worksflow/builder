import assert from 'node:assert/strict'
import { locales, normalizeLocale } from '../lib/i18n/config'
import { formatMessage, messages } from '../lib/i18n/messages'

type MessageKey = keyof typeof messages['zh-CN']

const referenceKeys = (Object.keys(messages['zh-CN']) as MessageKey[]).sort()
const placeholderNames = (message: string) =>
  [...message.matchAll(/\{(\w+)\}/g)].map((match) => match[1]).sort()

for (const locale of locales) {
  const localeKeys = Object.keys(messages[locale]).sort()
  assert.deepEqual(localeKeys, referenceKeys, `${locale} must contain the complete message catalog`)
  assert.equal(
    Object.values(messages[locale]).every((message) => message.trim().length > 0),
    true,
    `${locale} messages must not be empty`,
  )
}

for (const key of referenceKeys) {
  assert.deepEqual(
    placeholderNames(messages['en-US'][key]),
    placeholderNames(messages['zh-CN'][key]),
    `${key} must use the same placeholders in every locale`,
  )
}

assert.equal(normalizeLocale('zh-Hans'), 'zh-CN')
assert.equal(normalizeLocale('en-GB'), 'en-US')
assert.equal(normalizeLocale('fr-FR'), null)
assert.equal(formatMessage('Hello, {name}. You have {count} items.', { name: 'Ada', count: 3 }), 'Hello, Ada. You have 3 items.')
assert.equal(formatMessage('Keep {missing}', {}), 'Keep {missing}')

console.log(`${locales.length + 6} i18n test groups passed.`)
