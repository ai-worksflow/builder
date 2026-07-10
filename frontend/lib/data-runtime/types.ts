export type JsonPrimitive = string | number | boolean | null
export type JsonValue = JsonPrimitive | JsonValue[] | { [key: string]: JsonValue }

export type DataColumnType = 'text' | 'number' | 'boolean' | 'date' | 'json'

export interface DataColumnInput {
  readonly name: string
  readonly type: DataColumnType
  readonly required?: boolean
  readonly defaultValue?: JsonValue
}

export interface DataColumn extends DataColumnInput {
  readonly id: string
  readonly required: boolean
  readonly createdAt: string
}

export interface DataTableInput {
  readonly name: string
  readonly columns?: readonly DataColumnInput[]
}

export interface DataTable {
  readonly id: string
  readonly name: string
  readonly columns: readonly DataColumn[]
  readonly recordCount: number
  readonly createdAt: string
  readonly updatedAt: string
}

export interface DataRecord {
  readonly id: string
  readonly values: Readonly<Record<string, JsonValue>>
  readonly createdAt: string
  readonly updatedAt: string
}

export interface DataRecordInput {
  readonly values: Readonly<Record<string, JsonValue>>
}

export interface DataRecordPage {
  readonly records: readonly DataRecord[]
  readonly total: number
  readonly limit: number
  readonly offset: number
}

export type AuthUserStatus = 'invited' | 'active' | 'disabled'

export interface AuthUserMetadataInput {
  readonly email?: string
  readonly displayName?: string
  readonly role?: string
  readonly status?: AuthUserStatus
  readonly lastSignInAt?: string
  readonly metadata?: Readonly<Record<string, JsonValue>>
}

export interface AuthUserMetadata extends AuthUserMetadataInput {
  readonly id: string
  readonly status: AuthUserStatus
  readonly createdAt: string
  readonly updatedAt: string
}

export interface StorageObjectMetadataInput {
  readonly bucket: string
  readonly path: string
  readonly contentType?: string
  readonly sizeBytes?: number
  readonly checksum?: string
  readonly metadata?: Readonly<Record<string, JsonValue>>
}

export interface StorageObjectMetadata extends StorageObjectMetadataInput {
  readonly id: string
  readonly sizeBytes: number
  readonly createdAt: string
  readonly updatedAt: string
}

export type ServerFunctionRuntime = 'edge' | 'node'
export type ServerFunctionStatus = 'draft' | 'active' | 'disabled'

export interface ServerFunctionMetadataInput {
  readonly name: string
  readonly description?: string
  readonly runtime?: ServerFunctionRuntime
  readonly entryPath?: string
  readonly status?: ServerFunctionStatus
  readonly metadata?: Readonly<Record<string, JsonValue>>
}

export interface ServerFunctionMetadata extends ServerFunctionMetadataInput {
  readonly id: string
  readonly runtime: ServerFunctionRuntime
  readonly status: ServerFunctionStatus
  readonly createdAt: string
  readonly updatedAt: string
}

export type DataMetadataKind = 'auth-users' | 'storage-objects' | 'server-functions'

export interface DataMetadataMap {
  readonly 'auth-users': AuthUserMetadata
  readonly 'storage-objects': StorageObjectMetadata
  readonly 'server-functions': ServerFunctionMetadata
}

export type EnvironmentScope = 'development' | 'preview' | 'production'
export type EnvironmentVariableKind = 'plain' | 'secret'

export interface EnvironmentVariableInput {
  readonly name: string
  readonly scope: EnvironmentScope
  readonly kind?: EnvironmentVariableKind
  readonly value: string
}

export interface EnvironmentVariableMetadata {
  readonly id: string
  readonly name: string
  readonly scope: EnvironmentScope
  readonly kind: EnvironmentVariableKind
  readonly maskedValue: string
  readonly valueBytes: number
  readonly createdAt: string
  readonly updatedAt: string
}

export type DataMigrationOperation =
  | {
      readonly type: 'create-table'
      readonly table: DataTableInput
    }
  | {
      readonly type: 'rename-table'
      readonly tableId: string
      readonly name: string
    }
  | {
      readonly type: 'drop-table'
      readonly tableId: string
    }
  | {
      readonly type: 'add-column'
      readonly tableId: string
      readonly column: DataColumnInput
    }
  | {
      readonly type: 'rename-column'
      readonly tableId: string
      readonly columnId: string
      readonly name: string
    }
  | {
      readonly type: 'drop-column'
      readonly tableId: string
      readonly columnId: string
    }

export interface DataMigrationChange {
  readonly operation: DataMigrationOperation['type']
  readonly summary: string
  readonly destructive: boolean
}

export interface DataMigrationPreview {
  readonly id: string
  readonly projectId: string
  readonly confirmationToken: string
  readonly expiresAt: string
  readonly changes: readonly DataMigrationChange[]
  readonly resultingTables: readonly DataTable[]
}

export interface AppliedDataMigration {
  readonly id: string
  readonly previewId: string
  readonly appliedAt: string
  readonly changes: readonly DataMigrationChange[]
}

export interface DataAuditEvent {
  readonly id: string
  readonly action: string
  readonly resource: string
  readonly resourceId?: string
  readonly createdAt: string
  readonly details?: Readonly<Record<string, JsonValue>>
}

export interface DataConnectionMetadata {
  readonly provider: 'supabase'
  readonly endpoint: string
  readonly status: 'connected'
  readonly connectedAt: string
  readonly updatedAt: string
  readonly httpStatus: number
  readonly latencyMs: number
  readonly schemaTables: readonly string[]
}

export interface DataProjectSnapshot {
  readonly projectId: string
  readonly tables: readonly DataTable[]
  readonly authUsers: readonly AuthUserMetadata[]
  readonly storageObjects: readonly StorageObjectMetadata[]
  readonly serverFunctions: readonly ServerFunctionMetadata[]
  readonly variables: readonly EnvironmentVariableMetadata[]
  readonly migrations: readonly AppliedDataMigration[]
  readonly audit: readonly DataAuditEvent[]
  readonly connection?: DataConnectionMetadata
  readonly updatedAt: string
}

export interface SupabaseConnectionInput {
  readonly endpoint: string
  readonly key: string
}

export interface SupabaseConnectionResult {
  readonly ok: boolean
  readonly endpoint: string
  readonly latencyMs: number
  readonly status: number
  readonly message: string
  readonly schemaTables?: readonly string[]
}

export type DataRuntimeErrorCode =
  | 'invalid_request'
  | 'request_too_large'
  | 'not_found'
  | 'conflict'
  | 'confirmation_required'
  | 'confirmation_expired'
  | 'connection_failed'
  | 'unsafe_endpoint'
  | 'internal_error'
