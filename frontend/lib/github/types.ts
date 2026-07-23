export type GitHubConnectionSource = 'platform' | 'user' | 'session' | 'environment'

export interface GitHubUser {
  readonly id: number
  readonly login: string
  readonly name?: string
  readonly avatarUrl: string
  readonly htmlUrl: string
}

export interface GitHubConnectionStatus {
  readonly connected: boolean
  readonly source?: GitHubConnectionSource
  readonly organization?: string
  readonly user?: GitHubUser
  readonly expiresAt?: string
}

export interface GitHubRepository {
  readonly id: number
  readonly name: string
  readonly fullName: string
  readonly owner: string
  readonly private: boolean
  readonly archived: boolean
  readonly htmlUrl: string
  readonly defaultBranch: string
  readonly updatedAt?: string
  readonly permissions: {
    readonly pull: boolean
    readonly push: boolean
    readonly admin: boolean
  }
}

export interface GitHubCreateRepositoryInput {
  readonly owner?: string
  readonly name: string
  readonly description?: string
  readonly private: boolean
  readonly files: readonly GitHubWorkspaceFile[]
  readonly commitMessage: string
  readonly confirm: true
}

export interface GitHubCreateRepositoryResult {
  readonly repository: GitHubRepository
  readonly commitSha: string
  readonly commitUrl: string
}

export interface GitHubBranch {
  readonly name: string
  readonly commitSha: string
  readonly protected: boolean
}

export interface GitHubWorkspaceFile {
  readonly path: string
  readonly content: string
}

export interface GitHubRepositoryTarget {
  readonly owner: string
  readonly repo: string
  readonly branch: string
}

export interface GitHubPreviewInput extends GitHubRepositoryTarget {
  readonly files: readonly GitHubWorkspaceFile[]
}

export type GitHubChangeStatus = 'added' | 'modified' | 'deleted' | 'unchanged'

export interface GitHubLineChanges {
  readonly additions: number
  readonly deletions: number
}

export interface GitHubChange {
  readonly path: string
  readonly status: GitHubChangeStatus
  readonly beforeSha?: string
  readonly afterSha?: string
  readonly beforeBytes: number
  readonly afterBytes: number
  readonly lines?: GitHubLineChanges
}

export interface GitHubChangeSummary {
  readonly added: number
  readonly modified: number
  readonly deleted: number
  readonly unchanged: number
  readonly changed: number
}

export interface GitHubChangesPreview {
  readonly repository: string
  readonly branch: string
  readonly baseCommitSha: string
  readonly baseTreeSha: string
  readonly changes: readonly GitHubChange[]
  readonly summary: GitHubChangeSummary
}

export interface GitHubPushInput extends GitHubPreviewInput {
  readonly message: string
  readonly confirm: true
  readonly createBranch?: boolean
  readonly baseBranch?: string
}

export interface GitHubPushResult {
  readonly repository: string
  readonly branch: string
  readonly createdBranch: boolean
  readonly noOp: boolean
  readonly commitSha: string
  readonly commitUrl: string
  readonly preview: GitHubChangesPreview
}

export interface GitHubPullRequestInput {
  readonly owner: string
  readonly repo: string
  readonly head: string
  readonly base: string
  readonly title: string
  readonly body?: string
  readonly draft?: boolean
  readonly maintainerCanModify?: boolean
  readonly confirm: true
}

export interface GitHubPullRequestResult {
  readonly repository: string
  readonly number: number
  readonly title: string
  readonly state: string
  readonly draft: boolean
  readonly head: string
  readonly base: string
  readonly url: string
}
