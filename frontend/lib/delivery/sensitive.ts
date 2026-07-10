export interface SensitiveArtifactIssue {
  code: 'private-key' | 'provider-token' | 'embedded-credential'
  message: string
}

const CHECKS: ReadonlyArray<{
  code: SensitiveArtifactIssue['code']
  pattern: RegExp
  message: string
}> = [
  {
    code: 'private-key',
    pattern: /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/i,
    message: 'A private key block is embedded in this artifact.',
  },
  {
    code: 'provider-token',
    pattern: /\b(?:sk-[a-z0-9_-]{16,}|gh[opusr]_[a-z0-9]{16,}|AKIA[0-9A-Z]{16})\b/i,
    message: 'A provider access token appears to be embedded in this artifact.',
  },
  {
    code: 'embedded-credential',
    pattern: /\b(?:api[_-]?key|client[_-]?secret|auth[_-]?token|password|authorization)\b\s*[:=]\s*['"][^'"\n]{12,}['"]/i,
    message: 'A likely credential assignment appears in this artifact.',
  },
]

export function findSensitiveArtifactIssue(content: string): SensitiveArtifactIssue | undefined {
  const check = CHECKS.find((candidate) => candidate.pattern.test(content))
  return check ? { code: check.code, message: check.message } : undefined
}
