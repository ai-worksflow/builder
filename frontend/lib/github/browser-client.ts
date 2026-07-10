import {
  PlatformHttpError,
  PlatformNetworkError,
  type HttpClient,
  type QueryValue,
} from '@/lib/platform/http'
import type {
  GitHubBranch,
  GitHubChangesPreview,
  GitHubConnectionStatus,
  GitHubPullRequestInput,
  GitHubPullRequestResult,
  GitHubPushInput,
  GitHubPushResult,
  GitHubRepository,
} from './types'

export class GitHubBrowserError extends Error {
  readonly code: string
  readonly status?: number
  readonly retryAfterSeconds?: number

  constructor(
    message: string,
    options: { code?: string; status?: number; retryAfterSeconds?: number } = {},
  ) {
    super(message)
    this.name = 'GitHubBrowserError'
    this.code = options.code ?? 'github_browser_error'
    this.status = options.status
    this.retryAfterSeconds = options.retryAfterSeconds
  }
}

function githubPath(projectId: string, suffix: string) {
  return `/v1/projects/${encodeURIComponent(projectId)}/github/${suffix}`
}

async function githubRequest<T>(
  http: HttpClient,
  path: string,
  options: {
    method?: 'GET' | 'POST'
    body?: unknown
    query?: Readonly<Record<string, QueryValue>>
  } = {},
): Promise<T> {
  try {
    const result = await http.request<T>(path, {
      method: options.method ?? 'GET',
      body: options.body,
      query: options.query,
      idempotencyKey: options.method === 'POST' ? true : undefined,
      timeoutMs: 30_000,
    })
    return result.data
  } catch (error) {
    if (error instanceof PlatformHttpError) {
      throw new GitHubBrowserError(error.message, {
        code: error.code,
        status: error.status,
        retryAfterSeconds: error.retryAfterSeconds,
      })
    }
    if (error instanceof PlatformNetworkError) {
      throw new GitHubBrowserError(error.message, { code: 'github_unreachable' })
    }
    throw new GitHubBrowserError(
      error instanceof Error ? error.message : 'Unable to reach the GitHub service.',
      { code: 'github_request_failed' },
    )
  }
}

export async function getGitHubStatus(http: HttpClient, projectId: string) {
  return (
    await githubRequest<{ connection: GitHubConnectionStatus }>(
      http,
      githubPath(projectId, 'status'),
    )
  ).connection
}

export async function connectGitHub(http: HttpClient, projectId: string, token: string) {
  return (
    await githubRequest<{ connection: GitHubConnectionStatus }>(
      http,
      githubPath(projectId, 'connect'),
      { method: 'POST', body: { token } },
    )
  ).connection
}

export async function disconnectGitHub(http: HttpClient, projectId: string) {
  return (
    await githubRequest<{ connection: GitHubConnectionStatus }>(
      http,
      githubPath(projectId, 'disconnect'),
      { method: 'POST' },
    )
  ).connection
}

export async function listGitHubRepositories(http: HttpClient, projectId: string) {
  return (
    await githubRequest<{ repositories: GitHubRepository[] }>(
      http,
      githubPath(projectId, 'repositories'),
    )
  ).repositories
}

export async function listGitHubBranches(
  http: HttpClient,
  projectId: string,
  owner: string,
  repo: string,
) {
  return (
    await githubRequest<{ branches: GitHubBranch[] }>(
      http,
      githubPath(projectId, 'branches'),
      { query: { owner, repo } },
    )
  ).branches
}

export async function previewGitHubChanges(
  http: HttpClient,
  projectId: string,
  input: {
    owner: string
    repo: string
    branch: string
    files: Array<{ path: string; content: string }>
  },
) {
  return (
    await githubRequest<{ preview: GitHubChangesPreview }>(
      http,
      githubPath(projectId, 'preview'),
      { method: 'POST', body: input },
    )
  ).preview
}

export async function pushGitHubWorkspace(
  http: HttpClient,
  projectId: string,
  input: GitHubPushInput,
) {
  return (
    await githubRequest<{ result: GitHubPushResult }>(
      http,
      githubPath(projectId, 'push'),
      { method: 'POST', body: input },
    )
  ).result
}

export async function createGitHubPullRequest(
  http: HttpClient,
  projectId: string,
  input: GitHubPullRequestInput,
) {
  return (
    await githubRequest<{ pullRequest: GitHubPullRequestResult }>(
      http,
      githubPath(projectId, 'pull-requests'),
      { method: 'POST', body: input },
    )
  ).pullRequest
}
