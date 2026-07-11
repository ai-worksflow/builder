import type {
  PageDto,
  ProposalDto,
  VersionRefDto,
} from './dto'
import type { InputManifestDto } from './flow-contract'

export type DesignImportSourceKind =
  | 'figma'
  | 'penpot'
  | 'excalidraw'
  | 'tldraw'
  | 'storybook'
  | 'ladle'
  | 'upload'

export type DesignImportStatus =
  | 'creating'
  | 'open'
  | 'applying'
  | 'applied'
  | 'rejected'
  | 'failed'

export interface DesignImportSourceCapabilityDto {
  readonly sourceKind: DesignImportSourceKind
  readonly label: string
  readonly uploadEnabled: boolean
  readonly uploadReason?: string
  readonly remoteEnabled: boolean
  readonly remoteReason?: string
  readonly acceptedMediaTypes: readonly string[]
  readonly acceptedFileExtensions: readonly string[]
  readonly maxUploadBytes: number
}

export interface DesignImportCapabilitiesDto {
  readonly snapshotPolicy: string
  readonly trustPolicy: string
  readonly sources: readonly DesignImportSourceCapabilityDto[]
}

export interface DesignImportUploadDto {
  readonly name: string
  readonly mediaType: string
  readonly contentBase64: string
}

export interface CreateDesignImportInputDto {
  readonly sourceKind: DesignImportSourceKind
  readonly mode: 'upload' | 'remote_url'
  readonly title?: string
  readonly sourceUrl?: string
  readonly file?: DesignImportUploadDto
  readonly selectedFrameIds?: readonly string[]
  readonly pageSpecRevision: VersionRefDto
  readonly targetPrototypeArtifactId?: string
}

export interface DesignImportSnapshotDto {
  readonly contentHash: string
  readonly rawContentHash: string
  readonly sourceKind: DesignImportSourceKind
  readonly sourceName: string
  readonly mode: 'upload' | 'remote_url'
  readonly sourceUrl?: string
  readonly fileName?: string
  readonly mediaType: string
  readonly byteSize: number
  readonly capturedAt: string
  readonly selectedFrameIds: readonly string[]
}

export interface DesignImportDto {
  readonly id: string
  readonly projectId: string
  readonly status: DesignImportStatus
  readonly pipelineStage: 'snapshot_frozen' | 'target_frozen' | 'manifest_frozen' | 'proposal_ready'
  readonly version: number
  readonly etag: string
  readonly snapshot: DesignImportSnapshotDto
  readonly pageSpecRevision: VersionRefDto
  readonly prototypeArtifactId?: string
  readonly baseRevisionId?: string
  readonly inputManifestId?: string
  readonly outputProposalId?: string
  readonly operationId?: string
  readonly appliedRevisionId?: string
  readonly createsPrototype: boolean
  readonly failureCode?: string
  readonly failureDetail?: string
  readonly createdBy: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly decidedBy?: string
  readonly decidedAt?: string
  readonly manifest?: InputManifestDto
  readonly proposal?: ProposalDto
}

export interface DecideDesignImportInputDto {
  readonly decision: 'approve' | 'reject'
  readonly reason?: string
  readonly version: number
}

export type DesignImportPageDto = PageDto<DesignImportDto>
