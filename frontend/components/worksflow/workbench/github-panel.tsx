'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n } from '@/lib/i18n'
import {
  connectGitHub,
  createGitHubPullRequest,
  disconnectGitHub,
  getGitHubStatus,
  listGitHubBranches,
  listGitHubRepositories,
  previewGitHubChanges,
  pushGitHubWorkspace,
} from '@/lib/github/browser-client'
import type {
  GitHubBranch,
  GitHubChangesPreview,
  GitHubConnectionStatus,
  GitHubPullRequestResult,
  GitHubPushResult,
  GitHubRepository,
} from '@/lib/github/types'
import {
  CheckCircle2,
  ExternalLink,
  FileDiff,
  GitBranch,
  GitCommitHorizontal,
  GitPullRequest,
  Loader2,
  LogOut,
  RefreshCw,
  ShieldCheck,
} from 'lucide-react'

type PendingMutation = 'push' | 'pull-request' | null

interface GitHubPanelProps {
  readonly projectId?: string
  readonly files?: readonly { readonly path: string; readonly content: string }[]
}

export function GitHubPanel({ projectId, files }: GitHubPanelProps = {}) {
  const { locale, t } = useI18n()
  const { platformClient } = useCollaboration()
  const {
    workspace,
    projectName,
    productProject,
    updateGithubProjectSettings,
    selectedProductProjectId,
  } = useWorksflow()
  const effectiveProjectId = projectId ?? selectedProductProjectId
  const effectiveFiles = files ?? workspace.files
  const [connection, setConnection] = useState<GitHubConnectionStatus>({ connected: false })
  const [token, setToken] = useState('')
  const [repositories, setRepositories] = useState<GitHubRepository[]>([])
  const [branches, setBranches] = useState<GitHubBranch[]>([])
  const [repositoryName, setRepositoryName] = useState(
    productProject.githubSettings.owner && productProject.githubSettings.repository
      ? `${productProject.githubSettings.owner}/${productProject.githubSettings.repository}`
      : '',
  )
  const [baseBranch, setBaseBranch] = useState(productProject.githubSettings.defaultBranch ?? '')
  const [createBranch, setCreateBranch] = useState(false)
  const [newBranch, setNewBranch] = useState(`worksflow/${slug(projectName)}`)
  const [commitMessage, setCommitMessage] = useState(() => t('github.default.commitMessage', { project: projectName }))
  const [preview, setPreview] = useState<GitHubChangesPreview | null>(null)
  const [pushResult, setPushResult] = useState<GitHubPushResult | null>(null)
  const [pullRequest, setPullRequest] = useState<GitHubPullRequestResult | null>(null)
  const [prTitle, setPrTitle] = useState(() => t('github.default.prTitle', { project: projectName }))
  const [prBody, setPrBody] = useState(() => t('github.default.prBody'))
  const [prDraft, setPrDraft] = useState(true)
  const [pendingMutation, setPendingMutation] = useState<PendingMutation>(null)
  const [loading, setLoading] = useState<'status' | 'connect' | 'repos' | 'branches' | 'preview' | 'push' | 'pr' | null>('status')
  const [error, setError] = useState<string | null>(null)

  const selectedRepository = useMemo(
    () => repositories.find((repository) => repository.fullName === repositoryName),
    [repositories, repositoryName],
  )
  const targetBranch = createBranch ? newBranch.trim() : baseBranch

  const loadRepositories = useCallback(async () => {
    if (!effectiveProjectId) {
      setRepositories([])
      setError(t('github.error.selectProject'))
      return
    }
    setLoading('repos')
    setError(null)
    try {
      const next = await listGitHubRepositories(platformClient.http, effectiveProjectId)
      setRepositories(next)
      setRepositoryName((current) =>
        next.some((repository) => repository.fullName === current)
          ? current
          : next[0]?.fullName ?? '',
      )
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('github.error.loadRepositories'))
    } finally {
      setLoading(null)
    }
  }, [effectiveProjectId, platformClient.http, t])

  useEffect(() => {
    let active = true
    void (async () => {
      if (!effectiveProjectId) {
        setConnection({ connected: false })
        setLoading(null)
        return
      }
      try {
        const status = await getGitHubStatus(platformClient.http, effectiveProjectId)
        if (!active) return
        setConnection(status)
        if (status.connected) await loadRepositories()
      } catch (cause) {
        if (active) setError(cause instanceof Error ? cause.message : t('github.error.restoreSession'))
      } finally {
        if (active) setLoading(null)
      }
    })()
    return () => {
      active = false
    }
  }, [effectiveProjectId, loadRepositories, platformClient.http, t])

  useEffect(() => {
    if (!selectedRepository) {
      setBranches([])
      return
    }
    let active = true
    setLoading('branches')
    setError(null)
    if (!effectiveProjectId) return
    void listGitHubBranches(platformClient.http, effectiveProjectId, selectedRepository.owner, selectedRepository.name)
      .then((next) => {
        if (!active) return
        setBranches(next)
        setBaseBranch((current) =>
          next.some((branch) => branch.name === current)
            ? current
            : selectedRepository.defaultBranch,
        )
        updateGithubProjectSettings({
          status: 'connected',
          host: 'github.com',
          owner: selectedRepository.owner,
          repository: selectedRepository.name,
          defaultBranch: selectedRepository.defaultBranch,
          connectedAt: new Date().toISOString(),
          permissionScopes: selectedRepository.permissions.push ? ['contents:write'] : ['contents:read'],
        })
      })
      .catch((cause) => {
        if (active) setError(cause instanceof Error ? cause.message : t('github.error.loadBranches'))
      })
      .finally(() => {
        if (active) setLoading(null)
      })
    return () => {
      active = false
    }
  }, [effectiveProjectId, platformClient.http, selectedRepository, t, updateGithubProjectSettings])

  async function connect() {
    if (!effectiveProjectId || !token.trim()) return
    setLoading('connect')
    setError(null)
    try {
      const status = await connectGitHub(platformClient.http, effectiveProjectId, token)
      setConnection(status)
      setToken('')
      await loadRepositories()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('github.error.connect'))
    } finally {
      setLoading(null)
    }
  }

  async function disconnect() {
    if (!effectiveProjectId) return
    setError(null)
    try {
      setConnection(await disconnectGitHub(platformClient.http, effectiveProjectId))
      setRepositories([])
      setBranches([])
      setPreview(null)
      updateGithubProjectSettings({
        status: 'disconnected',
        host: 'github.com',
        permissionScopes: [],
      })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('github.error.disconnect'))
    }
  }

  async function createPreview() {
    if (!effectiveProjectId || !selectedRepository || !baseBranch) return
    setLoading('preview')
    setError(null)
    setPendingMutation(null)
    setPushResult(null)
    try {
      setPreview(await previewGitHubChanges(platformClient.http, effectiveProjectId, {
        owner: selectedRepository.owner,
        repo: selectedRepository.name,
        branch: baseBranch,
        files: effectiveFiles.map((file) => ({ path: file.path, content: file.content })),
      }))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('github.error.preview'))
    } finally {
      setLoading(null)
    }
  }

  async function confirmPush() {
    if (!effectiveProjectId || !selectedRepository || !baseBranch || !targetBranch || !commitMessage.trim()) return
    setLoading('push')
    setError(null)
    try {
      const result = await pushGitHubWorkspace(platformClient.http, effectiveProjectId, {
        owner: selectedRepository.owner,
        repo: selectedRepository.name,
        branch: targetBranch,
        message: commitMessage,
        files: effectiveFiles.map((file) => ({ path: file.path, content: file.content })),
        confirm: true,
        ...(createBranch ? { createBranch: true, baseBranch } : {}),
      })
      setPushResult(result)
      setPreview(result.preview)
      setPendingMutation(null)
      updateGithubProjectSettings({
        status: 'connected',
        host: 'github.com',
        owner: selectedRepository.owner,
        repository: selectedRepository.name,
        defaultBranch: targetBranch,
        lastCommitSha: result.commitSha,
        connectedAt: new Date().toISOString(),
        permissionScopes: ['contents:write'],
      })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('github.error.push'))
    } finally {
      setLoading(null)
    }
  }

  async function confirmPullRequest() {
    if (!effectiveProjectId || !selectedRepository || !targetBranch || !baseBranch || !prTitle.trim()) return
    setLoading('pr')
    setError(null)
    try {
      setPullRequest(await createGitHubPullRequest(platformClient.http, effectiveProjectId, {
        owner: selectedRepository.owner,
        repo: selectedRepository.name,
        head: targetBranch,
        base: selectedRepository.defaultBranch,
        title: prTitle,
        body: prBody,
        draft: prDraft,
        maintainerCanModify: true,
        confirm: true,
      }))
      setPendingMutation(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('github.error.pullRequest'))
    } finally {
      setLoading(null)
    }
  }

  if (loading === 'status') {
    return <div className="flex h-36 items-center justify-center"><Loader2 className="h-5 w-5 animate-spin text-primary-bright" /></div>
  }

  if (!connection.connected) {
    return (
      <div className="space-y-3">
        <div className="rounded-md border border-primary/25 bg-primary/10 p-3">
          <div className="flex items-center gap-2 text-[12px] font-medium text-primary-bright"><ShieldCheck className="h-4 w-4" />{t('github.session.title')}</div>
          <p className="mt-1 text-[10px] leading-relaxed text-muted-foreground">{t('github.session.description')}</p>
        </div>
        <label className="block text-[10px] text-faint-foreground">
          {t('github.token.label')}
          <input type="password" value={token} onChange={(event) => setToken(event.target.value)} autoComplete="off" className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3 text-[11px] text-foreground outline-none focus:border-primary/60" />
        </label>
        <button type="button" onClick={() => void connect()} disabled={!effectiveProjectId || !token.trim() || loading === 'connect'} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:opacity-50">
          {loading === 'connect' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <GitBranch className="h-3.5 w-3.5" />}{t('github.connectVerify')}
        </button>
        {error && <p role="alert" className="text-[11px] text-destructive">{error}</p>}
      </div>
    )
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 rounded-md border border-success/30 bg-success/10 px-3 py-2">
        <CheckCircle2 className="h-4 w-4 text-success" />
        <span className="min-w-0 flex-1 text-[11px] text-success">
          {t('github.connectedAs', { user: connection.user?.login ?? t('github.environmentToken'), source: connection.source ?? 'environment' })}
        </span>
        <button type="button" onClick={() => void disconnect()} className="rounded p-1.5 text-success hover:bg-white/5" aria-label={t('github.disconnectAria')}><LogOut className="h-3.5 w-3.5" /></button>
      </div>

      <div className="grid gap-2 sm:grid-cols-2">
        <label className="text-[10px] text-faint-foreground">{t('github.repository')}<select value={repositoryName} onChange={(event) => setRepositoryName(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground"><option value="">{t('github.selectRepository')}</option>{repositories.map((repository) => <option key={repository.id} value={repository.fullName}>{repository.fullName}{repository.private ? ` · ${t('github.private')}` : ''}</option>)}</select></label>
        <label className="text-[10px] text-faint-foreground">{t('github.baseBranch')}<select value={baseBranch} onChange={(event) => setBaseBranch(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground"><option value="">{t('github.selectBranch')}</option>{branches.map((branch) => <option key={branch.name} value={branch.name}>{branch.name}{branch.protected ? ` · ${t('github.protected')}` : ''}</option>)}</select></label>
      </div>

      <label className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2 text-[10px] text-muted-foreground"><input type="checkbox" checked={createBranch} onChange={(event) => setCreateBranch(event.target.checked)} />{t('github.createBranch')}</label>
      {createBranch && <label className="block text-[10px] text-faint-foreground">{t('github.newBranch')}<input value={newBranch} onChange={(event) => setNewBranch(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3 font-mono text-[11px] text-foreground outline-none" /></label>}

      <div className="flex flex-wrap gap-2">
        <button type="button" onClick={() => void loadRepositories()} className="inline-flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-[10px] text-muted-foreground hover:bg-white/5"><RefreshCw className={cn('h-3.5 w-3.5', loading === 'repos' && 'animate-spin')} />{t('github.refresh')}</button>
        <button type="button" onClick={() => void createPreview()} disabled={!selectedRepository || !baseBranch || effectiveFiles.length === 0 || loading === 'preview'} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-2.5 py-1.5 text-[10px] font-semibold text-primary-foreground disabled:opacity-50">{loading === 'preview' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <FileDiff className="h-3.5 w-3.5" />}{t('github.previewRemote')}</button>
      </div>

      {preview && (
        <div className="rounded-md border border-border bg-card p-3">
          <div className="flex items-center gap-2 text-[11px] font-medium text-foreground"><FileDiff className="h-3.5 w-3.5 text-primary-bright" />{t('github.previewSummary', { changed: formatNumber(preview.summary.changed, locale), added: formatNumber(preview.summary.added, locale), modified: formatNumber(preview.summary.modified, locale), deleted: formatNumber(preview.summary.deleted, locale) })}</div>
          <div className="mt-2 max-h-32 overflow-y-auto space-y-1 scrollbar-thin">
            {preview.changes.filter((change) => change.status !== 'unchanged').map((change) => <div key={change.path} className="flex items-center gap-2 rounded bg-background px-2 py-1 font-mono text-[9px]"><span className={change.status === 'added' ? 'text-success' : change.status === 'deleted' ? 'text-destructive' : 'text-warning'}>{changeStatusLabel(change.status, t)}</span><span className="min-w-0 flex-1 truncate text-muted-foreground">{change.path}</span>{change.lines && <span className="text-faint-foreground">+{formatNumber(change.lines.additions, locale)}/−{formatNumber(change.lines.deletions, locale)}</span>}</div>)}
          </div>
        </div>
      )}

      {preview && (
        <div className="space-y-2 rounded-md border border-border bg-card p-3">
          <label className="block text-[10px] text-faint-foreground">{t('github.commitMessage')}<input value={commitMessage} onChange={(event) => setCommitMessage(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3 text-[11px] text-foreground outline-none" /></label>
          {pendingMutation === 'push' ? (
            <Confirmation copy={t('github.pushConfirmation', { count: formatNumber(preview.summary.changed, locale), repository: selectedRepository?.fullName ?? '', branch: targetBranch })} loading={loading === 'push'} confirm={t('github.confirmCommitPush')} onCancel={() => setPendingMutation(null)} onConfirm={() => void confirmPush()} />
          ) : (
            <button type="button" onClick={() => setPendingMutation('push')} disabled={!selectedRepository?.permissions.push || preview.summary.changed === 0} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><GitCommitHorizontal className="h-3.5 w-3.5" />{t('github.reviewCommitPush')}</button>
          )}
        </div>
      )}

      {pushResult && !pushResult.noOp && (
        <div className="space-y-2 rounded-md border border-border bg-card p-3">
          <a href={pushResult.commitUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-[10px] text-primary-bright hover:underline">{t('github.commitLink', { sha: pushResult.commitSha.slice(0, 12) })}<ExternalLink className="h-3 w-3" /></a>
          <label className="block text-[10px] text-faint-foreground">{t('github.prTitle')}<input value={prTitle} onChange={(event) => setPrTitle(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3 text-[11px] text-foreground outline-none" /></label>
          <label className="block text-[10px] text-faint-foreground">{t('github.prBody')}<textarea value={prBody} onChange={(event) => setPrBody(event.target.value)} rows={3} className="mt-1 w-full resize-y rounded-md border border-border bg-background p-2 text-[10px] text-foreground outline-none" /></label>
          <label className="flex items-center gap-2 text-[10px] text-muted-foreground"><input type="checkbox" checked={prDraft} onChange={(event) => setPrDraft(event.target.checked)} />{t('github.createDraft')}</label>
          {pendingMutation === 'pull-request' ? (
            <Confirmation copy={t(prDraft ? 'github.prConfirmationDraft' : 'github.prConfirmationOpen', { head: targetBranch, base: selectedRepository?.defaultBranch ?? '' })} loading={loading === 'pr'} confirm={t('github.confirmPullRequest')} onCancel={() => setPendingMutation(null)} onConfirm={() => void confirmPullRequest()} />
          ) : (
            <button type="button" onClick={() => setPendingMutation('pull-request')} className="inline-flex items-center gap-1.5 rounded-md border border-primary/30 bg-primary/10 px-3 py-2 text-[10px] font-medium text-primary-bright"><GitPullRequest className="h-3.5 w-3.5" />{t('github.reviewPullRequest')}</button>
          )}
        </div>
      )}

      {pullRequest && <a href={pullRequest.url} target="_blank" rel="noopener noreferrer" className="flex items-center gap-2 rounded-md border border-success/30 bg-success/10 px-3 py-2 text-[11px] text-success hover:underline"><GitPullRequest className="h-3.5 w-3.5" />{t('github.pullRequestLink', { number: formatNumber(pullRequest.number, locale), title: pullRequest.title })}<ExternalLink className="ml-auto h-3 w-3" /></a>}
      {error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">{error}</p>}
    </div>
  )
}

function Confirmation({ copy, confirm, loading, onCancel, onConfirm }: { copy: string; confirm: string; loading: boolean; onCancel: () => void; onConfirm: () => void }) {
  const { t } = useI18n()
  return <div className="rounded-md border border-warning/30 bg-warning/10 p-2.5"><div className="flex items-start gap-2 text-[10px] leading-relaxed text-warning"><GitBranch className="mt-0.5 h-3.5 w-3.5 shrink-0" />{copy}</div><div className="mt-2 flex gap-2"><button type="button" onClick={onCancel} className="rounded-md border border-border px-2.5 py-1.5 text-[10px] text-muted-foreground">{t('common.cancel')}</button><button type="button" onClick={onConfirm} disabled={loading} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-2.5 py-1.5 text-[10px] font-semibold text-primary-foreground disabled:opacity-50">{loading && <Loader2 className="h-3 w-3 animate-spin" />}{confirm}</button></div></div>
}

function changeStatusLabel(status: GitHubChangesPreview['changes'][number]['status'], t: ReturnType<typeof useI18n>['t']) {
  const labels = {
    added: t('workbenchPlatform.status.added'),
    modified: t('workbenchPlatform.status.modified'),
    deleted: t('workbenchPlatform.status.deleted'),
    unchanged: t('workbenchPlatform.status.unchanged'),
  }
  return labels[status]
}

function formatNumber(value: number, locale: string) {
  return new Intl.NumberFormat(locale).format(value)
}

function slug(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 48) || 'update'
}
