import type { ClientRequestOptions } from './clients'
import { HttpClient, type HttpResult } from './http'
import {
  createLSPClientBind,
  decodeLSPTemplateProfileDiscovery,
  decodeLSPConnectionHello,
  decodeLSPTicketResponse,
  LSP_TICKET_REQUEST_SCHEMA_VERSION,
  LSP_WEB_SOCKET_PATH,
  LSP_WEB_SOCKET_SUBPROTOCOL,
  normalizeLSPTicketRequest,
  SandboxLSPError,
  type ExactTemplateReleaseDto,
  type LSPClientBindDto,
  type LSPConnectionHelloDto,
  type LSPDocumentFenceDto,
  type LSPTicketDto,
  type LSPTicketHandshakeScopeDto,
  type LSPTicketRequestDto,
  type LSPTemplateProfileDiscoveryDto,
  type SandboxHeadFenceDto,
  type SandboxLSPMode,
} from './lsp-contract'

export interface IssueLSPTicketInput {
  readonly mode: SandboxLSPMode
  readonly sandboxHeadFence: SandboxHeadFenceDto
  readonly templateRelease: ExactTemplateReleaseDto
  readonly profileIds: readonly string[]
}

export interface LSPWebSocketDescriptor {
  readonly url: string
  readonly subprotocol: 'worksflow.sandbox-lsp.v1'
}

function segment(value: string) {
  return encodeURIComponent(value)
}

export function createLSPTicketRequest(input: IssueLSPTicketInput): LSPTicketRequestDto {
  return normalizeLSPTicketRequest({
    schemaVersion: LSP_TICKET_REQUEST_SCHEMA_VERSION,
    mode: input.mode,
    sandboxHeadFence: input.sandboxHeadFence,
    templateRelease: input.templateRelease,
    profileIds: input.profileIds,
  })
}

/**
 * Resolves the dedicated LSP endpoint. It intentionally cannot accept the
 * ordinary collaboration `/ws` or sandbox stream endpoints.
 */
export function resolveLSPWebSocketDescriptor(
  platformBaseUrl: string,
  ticket: LSPTicketDto,
  browserOrigin?: string,
): LSPWebSocketDescriptor {
  if (ticket.webSocketPath !== LSP_WEB_SOCKET_PATH ||
    ticket.subprotocol !== LSP_WEB_SOCKET_SUBPROTOCOL ||
    !/^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/.test(ticket.ticket)) {
    throw new SandboxLSPError('lsp_websocket_url_required')
  }
  const normalizedBase = platformBaseUrl.trim().replace(/\/+$/, '')
  const joined = `${normalizedBase}${LSP_WEB_SOCKET_PATH}`
  let url: URL
  try {
    const origin = browserOrigin ?? (
      typeof window !== 'undefined' ? window.location.origin : undefined
    )
    if (/^https?:\/\//u.test(joined)) url = new URL(joined)
    else if (origin) url = new URL(joined, origin)
    else throw new Error('relative URL outside a browser')
    if ((url.protocol !== 'http:' && url.protocol !== 'https:') ||
      url.username !== '' || url.password !== '' || url.hash !== '' || url.search !== '') {
      throw new Error('non-canonical platform URL')
    }
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    // A base64url ticket is already canonical. Assigning the complete query
    // guarantees exactly one key and prevents inherited or duplicate params.
    url.search = `?ticket=${ticket.ticket}`
  } catch {
    throw new SandboxLSPError('lsp_websocket_url_required')
  }
  return Object.freeze({ url: url.toString(), subprotocol: LSP_WEB_SOCKET_SUBPROTOCOL })
}

export class PlatformLSPClient {
  private readonly claimedTicketIds = new Set<string>()

  constructor(private readonly http: HttpClient) {}

  async discoverProfiles(
    templateRelease: ExactTemplateReleaseDto,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<LSPTemplateProfileDiscoveryDto>> {
    const result = await this.http.get<string>(
      `/v1/template-releases/${segment(templateRelease.id)}`,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        responseType: 'text',
        headers: {
          Accept: 'application/json',
          'Cache-Control': 'no-store',
          Pragma: 'no-cache',
        },
      },
    )
    const contentType = result.headers.get('content-type')?.toLowerCase() ?? ''
    const cacheControl = result.headers.get('cache-control')?.toLowerCase() ?? ''
    if (!contentType.startsWith('application/json') ||
      !cacheControl.split(',').some((value) => value.trim() === 'no-store')) {
      throw new SandboxLSPError('lsp_message_malformed')
    }
    return {
      ...result,
      data: await decodeLSPTemplateProfileDiscovery(result.data, templateRelease),
    }
  }

  async issueTicket(
    sessionId: string,
    input: IssueLSPTicketInput,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<LSPTicketDto>> {
    const request = createLSPTicketRequest(input)
    if (request.sandboxHeadFence.sessionId !== sessionId) {
      throw new SandboxLSPError('lsp_ticket_scope_mismatch')
    }
    // Request text so the strict decoder can observe duplicate fields before
    // JSON.parse could collapse them. Omitting idempotencyKey is intentional:
    // a replay cache must never retain the one-time bearer response.
    const result = await this.http.post<string, LSPTicketRequestDto>(
      `/v1/sandbox-sessions/${segment(sessionId)}/lsp-tickets`,
      request,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        responseType: 'text',
        headers: {
          'Cache-Control': 'no-store',
          Pragma: 'no-cache',
        },
      },
    )
    const data = await decodeLSPTicketResponse(result.data, request)
    return { ...result, data }
  }

  /**
   * Claims a single-use ticket for one socket attempt. Only the non-secret ID
   * is retained, and a failed construction is never reported with the bearer.
   */
  claimWebSocket(ticket: LSPTicketDto, browserOrigin?: string): LSPWebSocketDescriptor {
    if (this.claimedTicketIds.has(ticket.id)) {
      throw new SandboxLSPError('lsp_ticket_scope_mismatch')
    }
    const descriptor = resolveLSPWebSocketDescriptor(this.http.baseUrl, ticket, browserOrigin)
    this.claimedTicketIds.add(ticket.id)
    return descriptor
  }

  acceptHello(encoded: string, ticket: LSPTicketHandshakeScopeDto): LSPConnectionHelloDto {
    return decodeLSPConnectionHello(encoded, ticket)
  }

  createBind(
    ticket: LSPTicketHandshakeScopeDto,
    hello: LSPConnectionHelloDto,
    profileId: string,
    documents: readonly LSPDocumentFenceDto[],
  ): LSPClientBindDto {
    return createLSPClientBind(ticket, hello, profileId, documents)
  }
}
