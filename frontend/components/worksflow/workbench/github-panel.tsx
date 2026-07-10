'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import { useCollaboration } from '@/lib/collaboration/provider'
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
  const [commitMessage, setCommitMessage] = useState(`Update ${projectName} from Worksflow`)
  const [preview, setPreview] = useState<GitHubChangesPreview | null>(null)
  const [pushResult, setPushResult] = useState<GitHubPushResult | null>(null)
  const [pullRequest, setPullRequest] = useState<GitHubPullRequestResult | null>(null)
  const [prTitle, setPrTitle] = useState(`Update ${projectName}`)
  const [prBody, setPrBody] = useState('Generated and reviewed in Worksflow.')
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
      setError('Select a server project before connecting GitHub.')
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
      setError(cause instanceof Error ? cause.message : 'Unable to load repositories.')
    } finally {
      setLoading(null)
    }
  }, [effectiveProjectId, platformClient.http])

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
        if (active) setError(cause instanceof Error ? cause.message : 'Unable to restore GitHub session.')
      } finally {
        if (active) setLoading(null)
      }
    })()
    return () => {
      active = false
    }
  }, [effectiveProjectId, loadRepositories, platformClient.http])

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
        if (active) setError(cause instanceof Error ? cause.message : 'Unable to load branches.')
      })
      .finally(() => {
        if (active) setLoading(null)
      })
    return () => {
      active = false
    }
  }, [effectiveProjectId, platformClient.http, selectedRepository, updateGithubProjectSettings])

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
      setError(cause instanceof Error ? cause.message : 'GitHub connection failed.')
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
      setError(cause instanceof Error ? cause.message : 'GitHub disconnect failed.')
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
      setError(cause instanceof Error ? cause.message : 'Unable to preview GitHub changes.')
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
      setError(cause instanceof Error ? cause.message : 'GitHub push failed.')
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
      setError(cause instanceof Error ? cause.message : 'Pull request creation failed.')
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
          <div className="flex items-center gap-2 text-[12px] font-medium text-primary-bright"><ShieldCheck className="h-4 w-4" />Server-side token session</div>
          <p className="mt-1 text-[10px] leading-relaxed text-muted-foreground">The personal access token is verified against GitHub and encrypted in an expiring server-side project credential. It is never persisted in this browser.</p>
        </div>
        <label className="block text-[10px] text-faint-foreground">
          Fine-grained personal access token
          <input type="password" value={token} onChange={(event) => setToken(event.target.value)} autoComplete="off" className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3 text-[11px] text-foreground outline-none focus:border-primary/60" />
        </label>
        <button type="button" onClick={() => void connect()} disabled={!effectiveProjectId || !token.trim() || loading === 'connect'} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:opacity-50">
          {loading === 'connect' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <GitBranch className="h-3.5 w-3.5" />}Connect and verify
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
          Connected as {connection.user?.login ?? 'environment token'} · {connection.source}
        </span>
        <button type="button" onClick={() => void disconnect()} className="rounded p-1.5 text-success hover:bg-white/5" aria-label="Disconnect GitHub"><LogOut className="h-3.5 w-3.5" /></button>
      </div>

      <div className="grid gap-2 sm:grid-cols-2">
        <label className="text-[10px] text-faint-foreground">Repository<select value={repositoryName} onChange={(event) => setRepositoryName(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground"><option value="">Select repository</option>{repositories.map((repository) => <option key={repository.id} value={repository.fullName}>{repository.fullName}{repository.private ? ' · private' : ''}</option>)}</select></label>
        <label className="text-[10px] text-faint-foreground">Base branch<select value={baseBranch} onChange={(event) => setBaseBranch(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground"><option value="">Select branch</option>{branches.map((branch) => <option key={branch.name} value={branch.name}>{branch.name}{branch.protected ? ' · protected' : ''}</option>)}</select></label>
      </div>

      <label className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2 text-[10px] text-muted-foreground"><input type="checkbox" checked={createBranch} onChange={(event) => setCreateBranch(event.target.checked)} />Create a new branch before pushing</label>
      {createBranch && <label className="block text-[10px] text-faint-foreground">New branch<input value={newBranch} onChange={(event) => setNewBranch(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3 font-mono text-[11px] text-foreground outline-none" /></label>}

      <div className="flex flex-wrap gap-2">
        <button type="button" onClick={() => void loadRepositories()} className="inline-flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-[10px] text-muted-foreground hover:bg-white/5"><RefreshCw className={cn('h-3.5 w-3.5', loading === 'repos' && 'animate-spin')} />Refresh</button>
        <button type="button" onClick={() => void createPreview()} disabled={!selectedRepository || !baseBranch || effectiveFiles.length === 0 || loading === 'preview'} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-2.5 py-1.5 text-[10px] font-semibold text-primary-foreground disabled:opacity-50">{loading === 'preview' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <FileDiff className="h-3.5 w-3.5" />}Preview remote changes</button>
      </div>

      {preview && (
        <div className="rounded-md border border-border bg-card p-3">
          <div className="flex items-center gap-2 text-[11px] font-medium text-foreground"><FileDiff className="h-3.5 w-3.5 text-primary-bright" />{preview.summary.changed} changed · +{preview.summary.added} ~{preview.summary.modified} −{preview.summary.deleted}</div>
          <div className="mt-2 max-h-32 overflow-y-auto space-y-1 scrollbar-thin">
            {preview.changes.filter((change) => change.status !== 'unchanged').map((change) => <div key={change.path} className="flex items-center gap-2 rounded bg-background px-2 py-1 font-mono text-[9px]"><span className={change.status === 'added' ? 'text-success' : change.status === 'deleted' ? 'text-destructive' : 'text-warning'}>{change.status}</span><span className="min-w-0 flex-1 truncate text-muted-foreground">{change.path}</span>{change.lines && <span className="text-faint-foreground">+{change.lines.additions}/−{change.lines.deletions}</span>}</div>)}
          </div>
        </div>
      )}

      {preview && (
        <div className="space-y-2 rounded-md border border-border bg-card p-3">
          <label className="block text-[10px] text-faint-foreground">Commit message<input value={commitMessage} onChange={(event) => setCommitMessage(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3 text-[11px] text-foreground outline-none" /></label>
          {pendingMutation === 'push' ? (
            <Confirmation copy={`This will write ${preview.summary.changed} changed file(s) to ${selectedRepository?.fullName}:${targetBranch}.`} loading={loading === 'push'} confirm="Confirm commit and push" onCancel={() => setPendingMutation(null)} onConfirm={() => void confirmPush()} />
          ) : (
            <button type="button" onClick={() => setPendingMutation('push')} disabled={!selectedRepository?.permissions.push || preview.summary.changed === 0} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><GitCommitHorizontal className="h-3.5 w-3.5" />Review commit and push</button>
          )}
        </div>
      )}

      {pushResult && !pushResult.noOp && (
        <div className="space-y-2 rounded-md border border-border bg-card p-3">
          <a href={pushResult.commitUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-[10px] text-primary-bright hover:underline">Commit {pushResult.commitSha.slice(0, 12)}<ExternalLink className="h-3 w-3" /></a>
          <label className="block text-[10px] text-faint-foreground">PR title<input value={prTitle} onChange={(event) => setPrTitle(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3 text-[11px] text-foreground outline-none" /></label>
          <label className="block text-[10px] text-faint-foreground">PR body<textarea value={prBody} onChange={(event) => setPrBody(event.target.value)} rows={3} className="mt-1 w-full resize-y rounded-md border border-border bg-background p-2 text-[10px] text-foreground outline-none" /></label>
          <label className="flex items-center gap-2 text-[10px] text-muted-foreground"><input type="checkbox" checked={prDraft} onChange={(event) => setPrDraft(event.target.checked)} />Create as draft</label>
          {pendingMutation === 'pull-request' ? (
            <Confirmation copy={`This will create ${prDraft ? 'a draft' : 'an open'} pull request from ${targetBranch} to ${selectedRepository?.defaultBranch}.`} loading={loading === 'pr'} confirm="Confirm pull request" onCancel={() => setPendingMutation(null)} onConfirm={() => void confirmPullRequest()} />
          ) : (
            <button type="button" onClick={() => setPendingMutation('pull-request')} className="inline-flex items-center gap-1.5 rounded-md border border-primary/30 bg-primary/10 px-3 py-2 text-[10px] font-medium text-primary-bright"><GitPullRequest className="h-3.5 w-3.5" />Review pull request</button>
          )}
        </div>
      )}

      {pullRequest && <a href={pullRequest.url} target="_blank" rel="noopener noreferrer" className="flex items-center gap-2 rounded-md border border-success/30 bg-success/10 px-3 py-2 text-[11px] text-success hover:underline"><GitPullRequest className="h-3.5 w-3.5" />PR #{pullRequest.number}: {pullRequest.title}<ExternalLink className="ml-auto h-3 w-3" /></a>}
      {error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">{error}</p>}
    </div>
  )
}

function Confirmation({ copy, confirm, loading, onCancel, onConfirm }: { copy: string; confirm: string; loading: boolean; onCancel: () => void; onConfirm: () => void }) {
  return <div className="rounded-md border border-warning/30 bg-warning/10 p-2.5"><div className="flex items-start gap-2 text-[10px] leading-relaxed text-warning"><GitBranch className="mt-0.5 h-3.5 w-3.5 shrink-0" />{copy}</div><div className="mt-2 flex gap-2"><button type="button" onClick={onCancel} className="rounded-md border border-border px-2.5 py-1.5 text-[10px] text-muted-foreground">Cancel</button><button type="button" onClick={onConfirm} disabled={loading} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-2.5 py-1.5 text-[10px] font-semibold text-primary-foreground disabled:opacity-50">{loading && <Loader2 className="h-3 w-3 animate-spin" />}{confirm}</button></div></div>
}

function slug(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 48) || 'update'
}
