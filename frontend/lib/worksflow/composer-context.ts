export type ComposerAttachmentKind = 'file' | 'image' | 'url' | 'workspace' | 'document'

export interface ComposerAttachment {
  id: string
  kind: ComposerAttachmentKind
  name: string
  content: string
  mimeType?: string
  size?: number
  sourceId?: string
  included?: boolean
}

export interface ComposerContextSource {
  id: string
  name: string
  description?: string
  content: string
}

export const MAX_COMPOSER_ATTACHMENT_COUNT = 12
export const MAX_TEXT_ATTACHMENT_BYTES = 32 * 1024
export const MAX_TOTAL_TEXT_ATTACHMENT_BYTES = 96 * 1024
export const MAX_IMAGE_ATTACHMENT_BYTES = 5 * 1024 * 1024

export function attachmentSetIssue(
  current: readonly ComposerAttachment[],
  next: ComposerAttachment,
) {
  if (current.length >= MAX_COMPOSER_ATTACHMENT_COUNT) {
    return `You can include at most ${MAX_COMPOSER_ATTACHMENT_COUNT} context items.`
  }
  if (next.kind !== 'image' && next.kind !== 'url' && next.content.length > MAX_TEXT_ATTACHMENT_BYTES) {
    return `Text context must be ${MAX_TEXT_ATTACHMENT_BYTES / 1024} KB or smaller.`
  }
  const totalText = [...current, next]
    .filter((item) => item.kind !== 'image' && item.kind !== 'url')
    .reduce((total, item) => total + item.content.length, 0)
  if (totalText > MAX_TOTAL_TEXT_ATTACHMENT_BYTES) {
    return `Combined text context must be ${MAX_TOTAL_TEXT_ATTACHMENT_BYTES / 1024} KB or smaller.`
  }
  return undefined
}

export function attachmentId(kind: ComposerAttachmentKind) {
  return `${kind}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

export function attachmentSummary(attachment: ComposerAttachment) {
  if (attachment.kind === 'url') return attachment.content
  if (typeof attachment.size === 'number') return formatBytes(attachment.size)
  return attachment.kind
}

export function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${Math.round(value / 102.4) / 10} KB`
  return `${Math.round(value / (1024 * 102.4)) / 10} MB`
}

export function isSafeContextUrl(value: string) {
  try {
    const url = new URL(value)
    return (
      (url.protocol === 'https:' || url.protocol === 'http:') &&
      !url.username &&
      !url.password
    )
  } catch {
    return false
  }
}

export async function attachmentFromFile(file: File): Promise<ComposerAttachment> {
  const isImage = file.type.startsWith('image/')
  const supportedImage = ['image/png', 'image/jpeg', 'image/webp', 'image/gif'].includes(file.type)
  if (isImage && !supportedImage) throw new Error('unsupported-file')
  const limit = isImage ? MAX_IMAGE_ATTACHMENT_BYTES : MAX_TEXT_ATTACHMENT_BYTES

  if (file.size > limit) {
    throw new Error(isImage ? 'image-too-large' : 'file-too-large')
  }

  if (!isImage && !isReadableTextFile(file)) throw new Error('unsupported-file')

  return {
    id: attachmentId(isImage ? 'image' : 'file'),
    kind: isImage ? 'image' : 'file',
    name: file.name,
    content: isImage ? await readAsDataUrl(file) : await file.text(),
    mimeType: file.type || undefined,
    size: file.size,
    included: true,
  }
}

function isReadableTextFile(file: File) {
  if (file.type.startsWith('text/')) return true
  return /\.(?:css|csv|html?|jsx?|json|md|sql|svg|tsx?|txt|xml|ya?ml)$/i.test(file.name)
}

function readAsDataUrl(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.addEventListener('load', () => resolve(String(reader.result ?? '')))
    reader.addEventListener('error', () => reject(reader.error ?? new Error('read-failed')))
    reader.readAsDataURL(file)
  })
}
