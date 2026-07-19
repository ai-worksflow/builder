import {
  createPlatformDomainClients,
  type PlatformDomainClients,
} from './clients'
import { DataRuntimeClient } from './data-client'
import { AgentClient } from './agent-client'
import { PlatformConversationClient } from './conversation-client'
import { ConstructorClient } from './constructor-client'
import { SandboxClient } from './sandbox-client'
import { RepositoryClient } from './repository-client'
import { VerificationClient } from './verification-client'
import { ReleaseClient } from './release-client'
import type { SandboxStreamOptions } from './sandbox-stream'
import { DesignImportsClient } from './design-import-client'
import { PlatformFlowClient } from './flow-client'
import { PlatformLSPClient } from './lsp-client'
import { HttpClient, type HttpClientOptions } from './http'
import {
  PlatformWebSocketClient,
  type PlatformWebSocketOptions,
} from './websocket'

export interface PlatformClientOptions {
  readonly http?: HttpClientOptions
  readonly httpClient?: HttpClient
  readonly websocket?: PlatformWebSocketOptions
  readonly sandboxStream?: SandboxStreamOptions
}

export class PlatformClient implements PlatformDomainClients {
  readonly http: HttpClient
  readonly websocket: PlatformWebSocketClient
  readonly session: PlatformDomainClients['session']
  readonly projects: PlatformDomainClients['projects']
  readonly members: PlatformDomainClients['members']
  readonly artifacts: PlatformDomainClients['artifacts']
  readonly documents: PlatformDomainClients['documents']
  readonly blueprints: PlatformDomainClients['blueprints']
  readonly pageSpecs: PlatformDomainClients['pageSpecs']
  readonly prototypes: PlatformDomainClients['prototypes']
  readonly reviews: PlatformDomainClients['reviews']
  readonly comments: PlatformDomainClients['comments']
  readonly notifications: PlatformDomainClients['notifications']
  readonly audit: PlatformDomainClients['audit']
  readonly presence: PlatformDomainClients['presence']
  readonly proposals: PlatformDomainClients['proposals']
  readonly workflows: PlatformDomainClients['workflows']
  readonly manifests: PlatformDomainClients['manifests']
  readonly runs: PlatformDomainClients['runs']
  readonly workbench: PlatformDomainClients['workbench']
  readonly traces: PlatformDomainClients['traces']
  readonly data: DataRuntimeClient
  readonly agent: AgentClient
  readonly flow: PlatformFlowClient
  readonly conversation: PlatformConversationClient
  readonly designImports: DesignImportsClient
  readonly constructorApi: ConstructorClient
  readonly sandbox: SandboxClient
  readonly repository: RepositoryClient
  readonly verification: VerificationClient
  readonly release: ReleaseClient
  readonly lsp: PlatformLSPClient

  constructor(options: PlatformClientOptions = {}) {
    this.http = options.httpClient ?? new HttpClient(options.http)
    this.websocket = new PlatformWebSocketClient({
      ...options.websocket,
      getAuth: options.websocket?.getAuth ?? (() => ({ csrfToken: this.http.getCsrfToken() })),
    })
    const clients = createPlatformDomainClients(this.http)
    this.session = clients.session
    this.projects = clients.projects
    this.members = clients.members
    this.artifacts = clients.artifacts
    this.documents = clients.documents
    this.blueprints = clients.blueprints
    this.pageSpecs = clients.pageSpecs
    this.prototypes = clients.prototypes
    this.reviews = clients.reviews
    this.comments = clients.comments
    this.notifications = clients.notifications
    this.audit = clients.audit
    this.presence = clients.presence
    this.proposals = clients.proposals
    this.workflows = clients.workflows
    this.manifests = clients.manifests
    this.runs = clients.runs
    this.workbench = clients.workbench
    this.traces = clients.traces
    this.data = new DataRuntimeClient(this.http)
    this.agent = new AgentClient(this.http)
    this.flow = new PlatformFlowClient(this.http)
    this.conversation = new PlatformConversationClient(this.http)
    this.designImports = new DesignImportsClient(this.http)
    this.constructorApi = new ConstructorClient(this.http)
    this.sandbox = new SandboxClient(this.http, options.sandboxStream)
    this.repository = new RepositoryClient(this.http)
    this.verification = new VerificationClient(this.http)
    this.release = new ReleaseClient(this.http)
    this.lsp = new PlatformLSPClient(this.http)
  }
}

export function createPlatformClient(options?: PlatformClientOptions) {
  return new PlatformClient(options)
}
