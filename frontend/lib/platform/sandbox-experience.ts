import type {
  SandboxPortListDto,
  SandboxPreviewLinkDto,
  SandboxProcessDto,
} from './sandbox-contract'

export type SandboxPreviewStage =
  | 'not-started'
  | 'starting'
  | 'waiting-for-port'
  | 'ready'
  | 'stopped'

export function sandboxPreviewStage(
  process: SandboxProcessDto | null,
  ports: SandboxPortListDto['ports'],
  preview: SandboxPreviewLinkDto | null,
): SandboxPreviewStage {
  if (!process) return 'not-started'
  if (process.state === 'exited' || process.state === 'failed' || process.state === 'orphaned') {
    return 'stopped'
  }
  if (preview) return 'ready'
  if (process.state === 'starting') return 'starting'
  if (process.state === 'running') {
    return ports.some((port) => port.previewable && port.healthy)
      ? 'ready'
      : 'waiting-for-port'
  }
  return 'stopped'
}

export function preferredPreviewPort(
  ports: SandboxPortListDto['ports'],
  preview: SandboxPreviewLinkDto | null,
) {
  if (preview && ports.some((port) => (
    port.name === preview.port.name
    && port.previewable
    && port.healthy
  ))) return undefined

  return ports.find((port) => port.previewable && port.healthy)
}
