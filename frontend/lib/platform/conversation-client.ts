import type { ClientMutationOptions, ClientRequestOptions, ListOptions } from './clients'
import type {
  ConversationCommandDto,
  ConversationDto,
  ConversationMessageDto,
  ConversationPageDto,
  ConversationSummaryCheckpointDto,
  CreateConversationSummaryCheckpointDto,
  CreatedWorkflowIntentProposalDto,
  CreateWorkflowIntentProposalDto,
  DecideWorkflowIntentProposalResultDto,
  GenerateWorkflowIntentProposalDto,
  GeneratedWorkflowIntentProposalDto,
  WorkflowIntentProposalDto,
} from './conversation-contract'
import { HttpClient } from './http'

function segment(value: string) {
  return encodeURIComponent(value)
}

function requestOptions(options?: ClientRequestOptions) {
  return { signal: options?.signal, requestId: options?.requestId }
}

function listOptions(options?: ListOptions) {
  return {
    ...requestOptions(options),
    query: { cursor: options?.cursor, limit: options?.limit },
  }
}

function mutationOptions(options?: ClientMutationOptions, ifMatch?: string) {
  return {
    ...requestOptions(options),
    ifMatch: options?.ifMatch ?? ifMatch,
    idempotencyKey: options?.idempotencyKey ?? true,
  }
}

export async function collectConversationPages<T>(
  load: (cursor?: string) => Promise<{ readonly data: ConversationPageDto<T> }>,
): Promise<T[]> {
  const items: T[] = []
  const cursors = new Set<string>()
  let cursor: string | undefined
  let hasNextPage = true
  while (hasNextPage) {
    const page = await load(cursor)
    items.push(...page.data.items)
    const next = page.data.nextCursor?.trim()
    if (!next) {
      hasNextPage = false
      continue
    }
    if (cursors.has(next)) throw new Error('Conversation pagination returned a repeated cursor.')
    cursors.add(next)
    cursor = next
  }
  return items
}

export class PlatformConversationClient {
  readonly http: HttpClient

  constructor(http: HttpClient) {
    this.http = http
  }

  private base(projectId: string) {
    return `/v1/projects/${segment(projectId)}/conversations`
  }

  list(projectId: string, options?: ListOptions) {
    return this.http.get<ConversationPageDto<ConversationDto>>(
      this.base(projectId),
      listOptions(options),
    )
  }

  create(projectId: string, title: string, options?: ClientMutationOptions) {
    return this.http.post<ConversationDto, { readonly title: string }>(
      this.base(projectId),
      { title },
      mutationOptions(options),
    )
  }

  get(projectId: string, conversationId: string, options?: ClientRequestOptions) {
    return this.http.get<ConversationDto>(
      `${this.base(projectId)}/${segment(conversationId)}`,
      requestOptions(options),
    )
  }

  update(
    projectId: string,
    conversation: Pick<ConversationDto, 'id' | 'etag'>,
    input: { readonly title?: string; readonly status?: ConversationDto['status'] },
    options?: ClientMutationOptions,
  ) {
    return this.http.patch<ConversationDto, typeof input>(
      `${this.base(projectId)}/${segment(conversation.id)}`,
      input,
      mutationOptions(options, conversation.etag),
    )
  }

  listMessages(projectId: string, conversationId: string, options?: ListOptions) {
    return this.http.get<ConversationPageDto<ConversationMessageDto>>(
      `${this.base(projectId)}/${segment(conversationId)}/messages`,
      listOptions(options),
    )
  }

  addMessage(
    projectId: string,
    conversationId: string,
    content: string,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ConversationMessageDto, { readonly content: string }>(
      `${this.base(projectId)}/${segment(conversationId)}/messages`,
      { content },
      mutationOptions(options),
    )
  }

  listSummaryCheckpoints(projectId: string, conversationId: string, options?: ListOptions) {
    return this.http.get<ConversationPageDto<ConversationSummaryCheckpointDto>>(
      `${this.base(projectId)}/${segment(conversationId)}/summary-checkpoints`,
      listOptions(options),
    )
  }

  createSummaryCheckpoint(
    projectId: string,
    conversation: Pick<ConversationDto, 'id' | 'etag'>,
    input: CreateConversationSummaryCheckpointDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ConversationSummaryCheckpointDto, CreateConversationSummaryCheckpointDto>(
      `${this.base(projectId)}/${segment(conversation.id)}/summary-checkpoints`,
      input,
      mutationOptions(options, conversation.etag),
    )
  }

  getSummaryCheckpoint(
    projectId: string,
    conversationId: string,
    checkpointId: string,
    options?: ClientRequestOptions,
  ) {
    return this.http.get<ConversationSummaryCheckpointDto>(
      `${this.base(projectId)}/${segment(conversationId)}/summary-checkpoints/${segment(checkpointId)}`,
      requestOptions(options),
    )
  }

  listSummaryCheckpointSourceMessages(
    projectId: string,
    conversationId: string,
    checkpointId: string,
    options?: ListOptions,
  ) {
    return this.http.get<ConversationPageDto<ConversationMessageDto>>(
      `${this.base(projectId)}/${segment(conversationId)}/summary-checkpoints/${segment(checkpointId)}/source-messages`,
      listOptions(options),
    )
  }

  decideSummaryCheckpoint(
    projectId: string,
    conversationId: string,
    checkpoint: Pick<ConversationSummaryCheckpointDto, 'id' | 'etag'>,
    decision: 'approve' | 'reject',
    reason = '',
    soloReviewConfirmed = false,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ConversationSummaryCheckpointDto, {
      readonly decision: 'approve' | 'reject'
      readonly reason?: string
      readonly soloReviewConfirmed?: boolean
    }>(
      `${this.base(projectId)}/${segment(conversationId)}/summary-checkpoints/${segment(checkpoint.id)}/decision`,
      {
        decision,
        ...(reason.trim() ? { reason: reason.trim() } : {}),
        ...(soloReviewConfirmed ? { soloReviewConfirmed: true } : {}),
      },
      mutationOptions(options, checkpoint.etag),
    )
  }

  listIntentProposals(projectId: string, conversationId: string, options?: ListOptions) {
    return this.http.get<ConversationPageDto<WorkflowIntentProposalDto>>(
      `${this.base(projectId)}/${segment(conversationId)}/intent-proposals`,
      listOptions(options),
    )
  }

  createIntentProposal(
    projectId: string,
    conversationId: string,
    input: CreateWorkflowIntentProposalDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<CreatedWorkflowIntentProposalDto, CreateWorkflowIntentProposalDto>(
      `${this.base(projectId)}/${segment(conversationId)}/intent-proposals`,
      input,
      mutationOptions(options),
    )
  }

  generateIntentProposal(
    projectId: string,
    conversationId: string,
    input: GenerateWorkflowIntentProposalDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<GeneratedWorkflowIntentProposalDto, GenerateWorkflowIntentProposalDto>(
      `${this.base(projectId)}/${segment(conversationId)}/intent-proposals/generate`,
      input,
      mutationOptions(options),
    )
  }

  getIntentProposal(
    projectId: string,
    conversationId: string,
    proposalId: string,
    options?: ClientRequestOptions,
  ) {
    return this.http.get<WorkflowIntentProposalDto>(
      `${this.base(projectId)}/${segment(conversationId)}/intent-proposals/${segment(proposalId)}`,
      requestOptions(options),
    )
  }

  decideIntentProposal(
    projectId: string,
    conversationId: string,
    proposal: Pick<WorkflowIntentProposalDto, 'id' | 'etag'>,
    decision: 'accept' | 'reject',
    reason = '',
    options?: ClientMutationOptions,
  ) {
    return this.http.post<DecideWorkflowIntentProposalResultDto, {
      readonly decision: 'accept' | 'reject'
      readonly reason?: string
    }>(
      `${this.base(projectId)}/${segment(conversationId)}/intent-proposals/${segment(proposal.id)}/decision`,
      { decision, ...(reason.trim() ? { reason: reason.trim() } : {}) },
      mutationOptions(options, proposal.etag),
    )
  }

  listCommands(projectId: string, conversationId: string, options?: ListOptions) {
    return this.http.get<ConversationPageDto<ConversationCommandDto>>(
      `${this.base(projectId)}/${segment(conversationId)}/commands`,
      listOptions(options),
    )
  }

  getCommand(
    projectId: string,
    conversationId: string,
    commandId: string,
    options?: ClientRequestOptions,
  ) {
    return this.http.get<ConversationCommandDto>(
      `${this.base(projectId)}/${segment(conversationId)}/commands/${segment(commandId)}`,
      requestOptions(options),
    )
  }

  executeCommand(
    projectId: string,
    conversationId: string,
    command: Pick<ConversationCommandDto, 'id' | 'etag' | 'kind'>,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ConversationCommandDto, Record<string, never>>(
      `${this.base(projectId)}/${segment(conversationId)}/commands/${segment(command.id)}/execute`,
      {},
      mutationOptions(options, command.etag),
    )
  }

  rejectCommand(
    projectId: string,
    conversationId: string,
    command: Pick<ConversationCommandDto, 'id' | 'etag'>,
    reason: string,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ConversationCommandDto, { readonly reason: string }>(
      `${this.base(projectId)}/${segment(conversationId)}/commands/${segment(command.id)}/reject`,
      { reason },
      mutationOptions(options, command.etag),
    )
  }
}
