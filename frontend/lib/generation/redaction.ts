const REDACTED = '[REDACTED]'

const secretPatterns: readonly RegExp[] = [
  /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/gi,
  /\bsk-[a-z0-9_-]{16,}\b/gi,
  /\bgh[opusr]_[a-z0-9]{16,}\b/gi,
  /\bAKIA[0-9A-Z]{16}\b/g,
]

const credentialAssignment = /(\b(?:api[_-]?key|client[_-]?secret|access[_-]?token|auth[_-]?token|password|authorization)\b\s*[:=]\s*)['"]?[^\s'";,]{8,}['"]?/gi

const sensitiveQueryKeys = /^(?:api[_-]?key|access[_-]?token|auth[_-]?token|token|password|secret|signature)$/i

export function containsSensitiveGenerationText(value: string) {
  credentialAssignment.lastIndex = 0
  if (credentialAssignment.test(value)) return true
  return secretPatterns.some((pattern) => {
    pattern.lastIndex = 0
    return pattern.test(value)
  })
}

export function redactSensitiveGenerationText(value: string) {
  const redacted = secretPatterns.reduce((current, pattern) => {
    pattern.lastIndex = 0
    return current.replace(pattern, REDACTED)
  }, value)
  credentialAssignment.lastIndex = 0
  return redacted.replace(credentialAssignment, `$1${REDACTED}`)
}

export function sanitizeGenerationUrl(value: string) {
  const url = new URL(value)
  if (url.username || url.password) throw new Error('URL credentials are not allowed')
  Array.from(url.searchParams.keys()).forEach((key) => {
    if (sensitiveQueryKeys.test(key)) url.searchParams.set(key, REDACTED)
  })
  return url.toString()
}
