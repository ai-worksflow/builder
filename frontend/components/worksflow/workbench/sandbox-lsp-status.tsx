import type { LSPTemplateProfileDto } from '@/lib/platform/lsp-contract'

export type SandboxLSPUIStatus =
  | 'disabled'
  | 'discovering'
  | 'blocked'
  | 'connecting'
  | 'ready'
  | 'stale'
  | 'unavailable'
  | 'disconnected'

export interface SandboxLSPUIView {
  readonly status: SandboxLSPUIStatus
  readonly detail: string
  readonly closeCode: number | null
}

export function SandboxLSPStatus({
  view,
  profiles,
  selectedProfileId,
  enabled,
  onProfile,
  onEnable,
  onDisable,
  onRetry,
}: {
  readonly view: SandboxLSPUIView
  readonly profiles: readonly LSPTemplateProfileDto[]
  readonly selectedProfileId: string
  readonly enabled: boolean
  readonly onProfile: (profileId: string) => void
  readonly onEnable: () => void
  readonly onDisable: () => void
  readonly onRetry: () => void
}) {
  const canEnable = !enabled && profiles.length > 0 && Boolean(selectedProfileId) &&
    view.status !== 'blocked' && view.status !== 'discovering'
  const retryable = enabled && (
    view.status === 'stale' || view.status === 'unavailable' || view.status === 'disconnected'
  )
  return (
    <div
      role="status"
      aria-label="Language intelligence status"
      className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border bg-card/40 px-3 py-1.5 text-[9px]"
    >
      <span
        aria-hidden="true"
        className={`size-2 rounded-full ${view.status === 'ready' ? 'bg-success' : view.status === 'connecting' || view.status === 'discovering' ? 'bg-warning' : 'bg-muted-foreground'}`}
      />
      <span className="font-semibold text-foreground">Language intelligence</span>
      <span className="min-w-48 flex-1 text-muted-foreground">
        {view.detail}{view.closeCode ? ` (WSS ${view.closeCode})` : ''}
      </span>
      <label htmlFor="sandbox-lsp-profile" className="sr-only">Approved language-server profile</label>
      <select
        id="sandbox-lsp-profile"
        value={selectedProfileId}
        onChange={(event) => onProfile(event.target.value)}
        disabled={enabled || profiles.length === 0}
        className="h-7 max-w-72 rounded border border-border bg-background px-2 text-[9px] text-foreground disabled:opacity-50"
      >
        {profiles.length === 0 && <option value="">No matching approved profile</option>}
        {profiles.map((profile) => (
          <option key={profile.id} value={profile.id}>
            {profile.serverInfo.name} {profile.serverInfo.version} · {profile.id}
          </option>
        ))}
      </select>
      {retryable && (
        <button
          type="button"
          onClick={onRetry}
          className="h-7 rounded border border-border px-2 font-semibold text-foreground hover:bg-muted"
        >
          Refresh exact head and reconnect
        </button>
      )}
      {enabled ? (
        <button
          type="button"
          onClick={onDisable}
          className="h-7 rounded border border-border px-2 font-semibold text-muted-foreground hover:bg-muted"
        >
          Disable LSP
        </button>
      ) : (
        <button
          type="button"
          onClick={onEnable}
          disabled={!canEnable}
          className="h-7 rounded bg-primary px-2 font-semibold text-primary-foreground disabled:opacity-40"
        >
          Enable selected profile
        </button>
      )}
    </div>
  )
}
