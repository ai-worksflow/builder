'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useCollaboration } from '@/lib/collaboration/provider'
import type {
  DataColumnType,
  DataMetadataKind,
  DataMigrationOperation,
  DataMigrationPreview,
  DataProjectSnapshot,
  DataConnectionMetadata,
  DataRecord,
  EnvironmentScope,
  EnvironmentVariableKind,
  JsonValue,
  SupabaseConnectionResult,
} from '@/lib/data-runtime/types'
import {
  PlatformHttpError,
  PlatformNetworkError,
} from '@/lib/platform/http'
import {
  Braces,
  CheckCircle2,
  Cloud,
  Database,
  FileKey2,
  FunctionSquare,
  HardDrive,
  KeyRound,
  Loader2,
  Play,
  Plus,
  RefreshCw,
  ScrollText,
  ShieldCheck,
  Table2,
  Trash2,
  Users,
} from 'lucide-react'

type DataTab =
  | 'overview'
  | 'tables'
  | 'records'
  | 'auth'
  | 'storage'
  | 'functions'
  | 'migrations'
  | 'variables'
  | 'connect'
  | 'audit'

const TABS: Array<{ id: DataTab; label: string; icon: typeof Database }> = [
  { id: 'overview', label: 'Overview', icon: Database },
  { id: 'tables', label: 'Tables', icon: Table2 },
  { id: 'records', label: 'Records', icon: Braces },
  { id: 'auth', label: 'Auth', icon: Users },
  { id: 'storage', label: 'Storage', icon: HardDrive },
  { id: 'functions', label: 'Functions', icon: FunctionSquare },
  { id: 'migrations', label: 'Migrations', icon: Play },
  { id: 'variables', label: 'Secrets', icon: FileKey2 },
  { id: 'connect', label: 'Supabase', icon: Cloud },
  { id: 'audit', label: 'Audit', icon: ScrollText },
]

export function DatabasePanel() {
  const { t } = useI18n()
  const { session, project, platformClient, can } = useCollaboration()
  const projectId = project?.id ?? ''
  const data = platformClient.data
  const canView = session.signedIn && Boolean(project) && can('view')
  const canEdit = session.signedIn && can('edit')
  const canAdmin = session.signedIn && can('admin')
  const [tab, setTab] = useState<DataTab>('overview')
  const [snapshot, setSnapshot] = useState<DataProjectSnapshot | null>(null)
  const [loading, setLoading] = useState(true)
  const [mutating, setMutating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [selectedTableId, setSelectedTableId] = useState('')
  const [records, setRecords] = useState<readonly DataRecord[]>([])
  const [recordsTotal, setRecordsTotal] = useState(0)
  const [recordsOffset, setRecordsOffset] = useState(0)
  const [recordsLoading, setRecordsLoading] = useState(false)
  const [tableName, setTableName] = useState('tasks')
  const [columnName, setColumnName] = useState('title')
  const [columnType, setColumnType] = useState<DataColumnType>('text')
  const [recordJson, setRecordJson] = useState('{\n  "title": "First task"\n}')
  const [metadataName, setMetadataName] = useState('')
  const [metadataSecondary, setMetadataSecondary] = useState('')
  const [variableName, setVariableName] = useState('API_BASE_URL')
  const [variableValue, setVariableValue] = useState('https://api.example.com')
  const [variableScope, setVariableScope] = useState<EnvironmentScope>('development')
  const [variableKind, setVariableKind] = useState<EnvironmentVariableKind>('plain')
  const [migrationKind, setMigrationKind] = useState<'create-table' | 'drop-table'>('create-table')
  const [migrationTableName, setMigrationTableName] = useState('new_table')
  const [migrationPreview, setMigrationPreview] = useState<DataMigrationPreview | null>(null)
  const [supabaseEndpoint, setSupabaseEndpoint] = useState('')
  const [supabaseKey, setSupabaseKey] = useState('')
  const [connection, setConnection] = useState<SupabaseConnectionResult | null>(null)
  const refreshSequence = useRef(0)
  const recordsSequence = useRef(0)

  const selectedTable = useMemo(
    () => snapshot?.tables.find((table) => table.id === selectedTableId),
    [selectedTableId, snapshot?.tables],
  )
  const refresh = useCallback(async () => {
    if (!canView) {
      refreshSequence.current += 1
      setLoading(false)
      setSnapshot(null)
      setRecords([])
      setRecordsTotal(0)
      return
    }
    const sequence = ++refreshSequence.current
    setLoading(true)
    setError(null)
    try {
      const next = (await data.snapshot(projectId)).data.project
      if (sequence !== refreshSequence.current) return
      setSnapshot(next)
      setSelectedTableId((current) =>
        next.tables.some((table) => table.id === current) ? current : next.tables[0]?.id ?? '',
      )
    } catch (cause) {
      if (sequence !== refreshSequence.current) return
      setSnapshot(null)
      setRecords([])
      setRecordsTotal(0)
      setError(dataErrorMessage(cause, 'Unable to load the data project.'))
    } finally {
      if (sequence === refreshSequence.current) setLoading(false)
    }
  }, [canView, data, projectId])

  const refreshRecords = useCallback(async (offset: number) => {
    if (!selectedTableId) {
      recordsSequence.current += 1
      setRecords([])
      setRecordsTotal(0)
      setRecordsOffset(0)
      return
    }
    const sequence = ++recordsSequence.current
    setRecordsLoading(true)
    setError(null)
    try {
      const result = (await data.listRecords(projectId, selectedTableId, {
        limit: 100,
        offset,
      })).data
      if (sequence !== recordsSequence.current) return
      setRecords(result.records)
      setRecordsTotal(result.total)
      setRecordsOffset(result.offset)
    } catch (cause) {
      if (sequence !== recordsSequence.current) return
      setRecords([])
      setRecordsTotal(0)
      setError(dataErrorMessage(cause, 'Unable to load records.'))
    } finally {
      if (sequence === recordsSequence.current) setRecordsLoading(false)
    }
  }, [data, projectId, selectedTableId])

  useEffect(() => {
    refreshSequence.current += 1
    recordsSequence.current += 1
    setSnapshot(null)
    setSelectedTableId('')
    setRecords([])
    setRecordsTotal(0)
    setRecordsOffset(0)
    setMigrationPreview(null)
    setConnection(null)
    setNotice(null)
  }, [projectId])

  useEffect(() => {
    void refresh()
  }, [refresh])

  useEffect(() => {
    if (tab === 'records' && canView) void refreshRecords(0)
  }, [canView, refreshRecords, tab])

  async function perform(action: () => Promise<unknown>, success: string) {
    if (mutating) return false
    setError(null)
    setNotice(null)
    setMutating(true)
    try {
      await action()
      setNotice(success)
      await refresh()
      return true
    } catch (cause) {
      setError(dataErrorMessage(cause, 'The data operation failed.'))
      return false
    } finally {
      setMutating(false)
    }
  }

  async function addMetadata(kind: DataMetadataKind) {
    if (!metadataName.trim()) return
    if (kind === 'auth-users') {
      await perform(
        () => data.createMetadata(projectId, kind, {
          email: metadataName,
          displayName: metadataSecondary || undefined,
          status: 'active',
        }),
        'Auth user metadata added.',
      )
    } else if (kind === 'storage-objects') {
      await perform(
        () => data.createMetadata(projectId, kind, {
          bucket: metadataSecondary || 'public',
          path: metadataName,
          sizeBytes: 0,
        }),
        'Storage object metadata added.',
      )
    } else {
      await perform(
        () => data.createMetadata(projectId, kind, {
          name: metadataName,
          entryPath: metadataSecondary || undefined,
          runtime: 'edge',
          status: 'draft',
        }),
        'Server function metadata added.',
      )
    }
    setMetadataName('')
    setMetadataSecondary('')
  }

  async function previewMigration() {
    let operations: DataMigrationOperation[]
    if (migrationKind === 'create-table') {
      operations = [{ type: 'create-table', table: { name: migrationTableName, columns: [] } }]
    } else if (selectedTableId) {
      operations = [{ type: 'drop-table', tableId: selectedTableId }]
    } else {
      setError('Select a table before previewing a destructive migration.')
      return
    }
    setError(null)
    try {
      setMigrationPreview((await data.previewMigration(projectId, operations)).data.preview)
    } catch (cause) {
      setError(dataErrorMessage(cause, 'Migration preview failed.'))
    }
  }

  return (
    <div className="flex h-full min-h-0 bg-background max-lg:flex-col">
      <aside className="w-48 shrink-0 overflow-y-auto border-r border-border bg-panel p-2 scrollbar-thin max-lg:flex max-lg:w-full max-lg:overflow-x-auto max-lg:border-b max-lg:border-r-0">
        {TABS.map((item) => {
          const Icon = item.icon
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => setTab(item.id)}
              className={cn(
                'flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-[12px] font-medium max-lg:w-auto max-lg:shrink-0',
                tab === item.id
                  ? 'bg-primary/15 text-primary-bright'
                  : 'text-muted-foreground hover:bg-white/5 hover:text-foreground',
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              {item.label}
            </button>
          )
        })}
      </aside>

      <main className="min-h-0 min-w-0 flex-1 overflow-y-auto p-4 scrollbar-thin">
        <div className="mx-auto max-w-5xl">
          <header className="mb-4 flex items-start gap-3">
            <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary-bright">
              <Database className="h-4 w-4" />
            </span>
            <div className="min-w-0 flex-1">
              <h2 className="text-sm font-semibold text-foreground">{t('database.title')}</h2>
              <p className="text-[11px] text-faint-foreground">
                Project {projectId || 'not selected'} · Go data runtime · secret values are never returned
              </p>
            </div>
            <button
              type="button"
              onClick={() => void refresh()}
              disabled={loading || !canView}
              className="rounded-md border border-border p-2 text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-50"
              aria-label="Refresh database"
            >
              <RefreshCw className={cn('h-3.5 w-3.5', loading && 'animate-spin')} />
            </button>
          </header>

          {error && <div role="alert" className="mb-3 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[11px] text-destructive">{error}</div>}
          {notice && <div className="mb-3 rounded-md border border-success/30 bg-success/10 px-3 py-2 text-[11px] text-success">{notice}</div>}
          {!session.signedIn ? (
            <div className="rounded-lg border border-warning/30 bg-warning/10 p-6 text-center text-[12px] text-warning">
              Sign in from Settings to inspect or change this project&apos;s data.
            </div>
          ) : !canView ? (
            <div className="rounded-lg border border-border bg-card p-6 text-center text-[12px] text-muted-foreground">
              Waiting for project access, or your account is not a project member.
            </div>
          ) : loading && !snapshot ? (
            <div className="flex h-48 items-center justify-center"><Loader2 className="h-5 w-5 animate-spin text-primary-bright" /></div>
          ) : !snapshot ? (
            <div className="rounded-lg border border-destructive/30 bg-card p-6 text-center">
              <Database className="mx-auto h-5 w-5 text-destructive" />
              <p className="mt-2 text-[12px] font-medium text-foreground">Data runtime unavailable</p>
              <p className="mt-1 text-[10px] text-faint-foreground">
                No browser fixture or local database has been substituted. Restore the Go service and retry.
              </p>
              <button
                type="button"
                onClick={() => void refresh()}
                className="mt-3 rounded-md border border-border px-3 py-2 text-[11px] text-foreground hover:bg-white/5"
              >
                Retry server connection
              </button>
            </div>
          ) : (
            <fieldset disabled={mutating} className={cn('min-w-0', mutating && 'opacity-60')}>
              {tab !== 'overview' && (
                <p className="mb-3 text-[10px] text-faint-foreground">
                  {tab === 'audit'
                    ? 'Read-only audit events are loaded from the Go data runtime.'
                    : tab === 'tables' || tab === 'records' || tab === 'migrations'
                    ? 'Editing this area requires an editor role. Applying a migration still requires an administrator.'
                    : 'Changing this area requires an administrator role.'}
                </p>
              )}
              {tab === 'overview' && <Overview snapshot={snapshot} />}
              {tab === 'tables' && (
                <TablesView
                  snapshot={snapshot}
                  tableName={tableName}
                  columnName={columnName}
                  columnType={columnType}
                  onTableName={setTableName}
                  onColumnName={setColumnName}
                  onColumnType={setColumnType}
                  canMutate={canEdit}
                  onCreate={() => void perform(
                    () => data.createTable(projectId, {
                      name: tableName,
                      columns: columnName ? [{ name: columnName, type: columnType }] : [],
                    }),
                    'Table created.',
                  )}
                  onDelete={(tableId) => void perform(
                    () => data.deleteTable(projectId, tableId),
                    'Table deleted.',
                  )}
                />
              )}
              {tab === 'records' && (
                <RecordsView
                  tables={snapshot.tables}
                  selectedTableId={selectedTableId}
                  onSelectTable={setSelectedTableId}
                  records={records}
                  total={recordsTotal}
                  offset={recordsOffset}
                  loading={recordsLoading}
                  recordJson={recordJson}
                  onRecordJson={setRecordJson}
                  canMutate={canEdit}
                  onCreate={() => void (async () => {
                    try {
                      const values = jsonObject(JSON.parse(recordJson) as unknown)
                      const ok = await perform(
                        () => data.createRecord(projectId, selectedTableId, { values }),
                        'Record created.',
                      )
                      if (ok) await refreshRecords(recordsOffset)
                    } catch {
                      setError('Record values must be a valid JSON object.')
                    }
                  })()}
                  onDelete={(recordId) => void (async () => {
                    const ok = await perform(
                      () => data.deleteRecord(projectId, selectedTableId, recordId),
                      'Record deleted.',
                    )
                    if (ok) await refreshRecords(recordsOffset)
                  })()}
                  onPrevious={() => void refreshRecords(Math.max(0, recordsOffset - 100))}
                  onNext={() => void refreshRecords(recordsOffset + 100)}
                />
              )}
              {tab === 'auth' && (
                <MetadataView
                  kind="auth-users"
                  items={snapshot.authUsers}
                  primaryLabel="Email"
                  secondaryLabel="Display name"
                  primary={metadataName}
                  secondary={metadataSecondary}
                  onPrimary={setMetadataName}
                  onSecondary={setMetadataSecondary}
                  canMutate={canAdmin}
                  onAdd={() => void addMetadata('auth-users')}
                  onDelete={(id) => void perform(
                    () => data.deleteMetadata(projectId, 'auth-users', id),
                    'Auth user metadata deleted.',
                  )}
                />
              )}
              {tab === 'storage' && (
                <MetadataView
                  kind="storage-objects"
                  items={snapshot.storageObjects}
                  primaryLabel="Object path"
                  secondaryLabel="Bucket"
                  primary={metadataName}
                  secondary={metadataSecondary}
                  onPrimary={setMetadataName}
                  onSecondary={setMetadataSecondary}
                  canMutate={canAdmin}
                  onAdd={() => void addMetadata('storage-objects')}
                  onDelete={(id) => void perform(
                    () => data.deleteMetadata(projectId, 'storage-objects', id),
                    'Storage object metadata deleted.',
                  )}
                />
              )}
              {tab === 'functions' && (
                <MetadataView
                  kind="server-functions"
                  items={snapshot.serverFunctions}
                  primaryLabel="Function name"
                  secondaryLabel="Entry path"
                  primary={metadataName}
                  secondary={metadataSecondary}
                  onPrimary={setMetadataName}
                  onSecondary={setMetadataSecondary}
                  canMutate={canAdmin}
                  onAdd={() => void addMetadata('server-functions')}
                  onDelete={(id) => void perform(
                    () => data.deleteMetadata(projectId, 'server-functions', id),
                    'Function metadata deleted.',
                  )}
                />
              )}
              {tab === 'migrations' && (
                <MigrationsView
                  kind={migrationKind}
                  onKind={setMigrationKind}
                  tableName={migrationTableName}
                  onTableName={setMigrationTableName}
                  selectedTable={selectedTable?.name}
                  preview={migrationPreview}
                  canPreview={canEdit}
                  onPreview={() => void previewMigration()}
                  onApply={() => void (async () => {
                    if (!migrationPreview) return
                    const ok = await perform(
                      () => data.applyMigration(projectId, migrationPreview.confirmationToken),
                      'Migration applied.',
                    )
                    if (ok) setMigrationPreview(null)
                  })()}
                  canApply={canAdmin}
                />
              )}
              {tab === 'variables' && (
                <VariablesView
                  variables={snapshot.variables}
                  name={variableName}
                  value={variableValue}
                  scope={variableScope}
                  kind={variableKind}
                  onName={setVariableName}
                  onValue={setVariableValue}
                  onScope={setVariableScope}
                  onKind={setVariableKind}
                  canMutate={canAdmin}
                  onSave={() => void perform(
                    () => data.setVariable(projectId, {
                      name: variableName,
                      value: variableValue,
                      scope: variableScope,
                      kind: variableKind,
                    }),
                    'Environment variable saved with a masked value.',
                  )}
                  onDelete={(id) => void perform(
                    () => data.deleteVariable(projectId, id),
                    'Environment variable deleted.',
                  )}
                />
              )}
              {tab === 'connect' && (
                <ConnectionView
                  endpoint={supabaseEndpoint}
                  apiKey={supabaseKey}
                  result={connection}
                  storedConnection={snapshot.connection}
                  canMutate={canAdmin}
                  onEndpoint={setSupabaseEndpoint}
                  onApiKey={setSupabaseKey}
                  onConnect={() => void (async () => {
                    if (mutating) return
                    setError(null)
                    setNotice(null)
                    setMutating(true)
                    try {
                      const result = (await data.connectSupabase(projectId, {
                        endpoint: supabaseEndpoint,
                        key: supabaseKey,
                      })).data.connection
                      setConnection(result)
                      if (!result.ok) {
                        setError(result.message)
                        return
                      }
                      setSupabaseKey('')
                      setNotice('Supabase connection verified and stored by the Go data runtime.')
                      await refresh()
                    } catch (cause) {
                      setError(dataErrorMessage(cause, 'Supabase connection failed.'))
                    } finally {
                      setMutating(false)
                    }
                  })()}
                />
              )}
              {tab === 'audit' && <AuditView events={snapshot.audit} />}
            </fieldset>
          )}
        </div>
      </main>
    </div>
  )
}

function Overview({ snapshot }: { snapshot: DataProjectSnapshot }) {
  const cards = [
    ['Tables', snapshot.tables.length, Table2],
    ['Records', snapshot.tables.reduce((sum, table) => sum + table.recordCount, 0), Braces],
    ['Auth users', snapshot.authUsers.length, Users],
    ['Storage objects', snapshot.storageObjects.length, HardDrive],
    ['Functions', snapshot.serverFunctions.length, FunctionSquare],
    ['Variables', snapshot.variables.length, FileKey2],
  ] as const
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
      {cards.map(([label, value, Icon]) => (
        <div key={label} className="rounded-lg border border-border bg-card p-4">
          <Icon className="h-4 w-4 text-primary-bright" />
          <div className="mt-3 text-2xl font-semibold text-foreground">{value}</div>
          <div className="text-[11px] text-faint-foreground">{label}</div>
        </div>
      ))}
      <div className="col-span-2 rounded-lg border border-border bg-card p-4 md:col-span-3">
        <div className="flex items-center gap-2 text-[12px] font-medium text-foreground">
          <ShieldCheck className="h-4 w-4 text-success" />
          Secret-safe by default
        </div>
        <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">
          Variable values stay on the server. Browser responses, project snapshots, audit events, logs, and exports contain masked metadata only.
        </p>
      </div>
    </div>
  )
}

function TablesView({
  snapshot,
  tableName,
  columnName,
  columnType,
  onTableName,
  onColumnName,
  onColumnType,
  canMutate,
  onCreate,
  onDelete,
}: {
  snapshot: DataProjectSnapshot
  tableName: string
  columnName: string
  columnType: DataColumnType
  onTableName: (value: string) => void
  onColumnName: (value: string) => void
  onColumnType: (value: DataColumnType) => void
  canMutate: boolean
  onCreate: () => void
  onDelete: (id: string) => void
}) {
  return (
    <div className="grid gap-3 lg:grid-cols-[1fr_300px]">
      <div className="space-y-2">
        {snapshot.tables.map((table) => (
          <div key={table.id} className="rounded-lg border border-border bg-card p-3">
            <div className="flex items-center gap-2">
              <Table2 className="h-4 w-4 text-primary-bright" />
              <span className="font-mono text-[12px] font-medium text-foreground">{table.name}</span>
              <span className="text-[10px] text-faint-foreground">{table.recordCount} records</span>
              <button type="button" disabled={!canMutate} onClick={() => window.confirm(`Delete table ${table.name} and all of its records?`) && onDelete(table.id)} className="ml-auto rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40" aria-label={`Delete ${table.name}`}>
                <Trash2 className="h-3.5 w-3.5" />
              </button>
            </div>
            <div className="mt-2 flex flex-wrap gap-1.5">
              {table.columns.map((column) => (
                <span key={column.id} className="rounded bg-white/5 px-2 py-1 font-mono text-[10px] text-muted-foreground">
                  {column.name}: {column.type}{column.required ? '!' : ''}
                </span>
              ))}
              {table.columns.length === 0 && <span className="text-[10px] text-faint-foreground">No columns yet</span>}
            </div>
          </div>
        ))}
        {snapshot.tables.length === 0 && <EmptyState title="No tables" copy="Create the first typed table using the form." />}
      </div>
      <FormCard title="Create table">
        <Field label="Table name" value={tableName} onChange={onTableName} />
        <Field label="First column" value={columnName} onChange={onColumnName} />
        <label className="block text-[10px] text-faint-foreground">
          Column type
          <select value={columnType} onChange={(event) => onColumnType(event.target.value as DataColumnType)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground">
            {(['text', 'number', 'boolean', 'date', 'json'] as const).map((type) => <option key={type}>{type}</option>)}
          </select>
        </label>
        <PrimaryButton onClick={onCreate} disabled={!canMutate}><Plus className="h-3.5 w-3.5" />Create table</PrimaryButton>
      </FormCard>
    </div>
  )
}

function RecordsView({
  tables,
  selectedTableId,
  onSelectTable,
  records,
  total,
  offset,
  loading,
  recordJson,
  onRecordJson,
  canMutate,
  onCreate,
  onDelete,
  onPrevious,
  onNext,
}: {
  tables: DataProjectSnapshot['tables']
  selectedTableId: string
  onSelectTable: (id: string) => void
  records: readonly DataRecord[]
  total: number
  offset: number
  loading: boolean
  recordJson: string
  onRecordJson: (value: string) => void
  canMutate: boolean
  onCreate: () => void
  onDelete: (id: string) => void
  onPrevious: () => void
  onNext: () => void
}) {
  return (
    <div className="space-y-3">
      <select value={selectedTableId} onChange={(event) => onSelectTable(event.target.value)} className="h-9 rounded-md border border-border bg-card px-3 text-[11px] text-foreground">
        <option value="">Select a table</option>
        {tables.map((table) => <option key={table.id} value={table.id}>{table.name}</option>)}
      </select>
      {selectedTableId && (
        <div className="grid gap-3 lg:grid-cols-[1fr_320px]">
          <div className="space-y-2">
            {loading ? <Loader2 className="h-4 w-4 animate-spin text-primary-bright" /> : records.map((record) => (
              <div key={record.id} className="flex items-start gap-2 rounded-md border border-border bg-card p-3">
                <pre className="min-w-0 flex-1 overflow-x-auto text-[10px] leading-relaxed text-muted-foreground">{JSON.stringify(record.values, null, 2)}</pre>
                <button type="button" disabled={!canMutate} onClick={() => window.confirm('Delete this record?') && onDelete(record.id)} className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40"><Trash2 className="h-3.5 w-3.5" /></button>
              </div>
            ))}
            {!loading && records.length === 0 && <EmptyState title="No records" copy="Add a JSON record from the form." />}
            {!loading && total > 0 && (
              <div className="flex items-center justify-between rounded-md border border-border bg-card px-3 py-2 text-[10px] text-faint-foreground">
                <span>
                  {offset + 1}–{Math.min(offset + records.length, total)} of {total}
                </span>
                <span className="flex gap-2">
                  <button
                    type="button"
                    onClick={onPrevious}
                    disabled={offset === 0}
                    className="rounded border border-border px-2 py-1 text-foreground disabled:opacity-40"
                  >
                    Previous
                  </button>
                  <button
                    type="button"
                    onClick={onNext}
                    disabled={offset + records.length >= total}
                    className="rounded border border-border px-2 py-1 text-foreground disabled:opacity-40"
                  >
                    Next
                  </button>
                </span>
              </div>
            )}
          </div>
          <FormCard title="Create record">
            <label className="block text-[10px] text-faint-foreground">
              JSON values
              <textarea value={recordJson} onChange={(event) => onRecordJson(event.target.value)} rows={8} className="mt-1 w-full resize-y rounded-md border border-border bg-background p-2 font-mono text-[10px] text-foreground outline-none" />
            </label>
            <PrimaryButton onClick={onCreate} disabled={!canMutate}><Plus className="h-3.5 w-3.5" />Create record</PrimaryButton>
          </FormCard>
        </div>
      )}
    </div>
  )
}

function MetadataView({
  kind,
  items,
  primaryLabel,
  secondaryLabel,
  primary,
  secondary,
  onPrimary,
  onSecondary,
  canMutate,
  onAdd,
  onDelete,
}: {
  kind: DataMetadataKind
  items: ReadonlyArray<{ id: string }>
  primaryLabel: string
  secondaryLabel: string
  primary: string
  secondary: string
  onPrimary: (value: string) => void
  onSecondary: (value: string) => void
  canMutate: boolean
  onAdd: () => void
  onDelete: (id: string) => void
}) {
  return (
    <div className="grid gap-3 lg:grid-cols-[1fr_300px]">
      <div className="space-y-2">
        {items.map((item) => (
          <div key={item.id} className="flex items-start gap-2 rounded-md border border-border bg-card p-3">
            <pre className="min-w-0 flex-1 overflow-x-auto text-[10px] leading-relaxed text-muted-foreground">{JSON.stringify(item, null, 2)}</pre>
            <button type="button" disabled={!canMutate} onClick={() => window.confirm('Delete this metadata item?') && onDelete(item.id)} className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40"><Trash2 className="h-3.5 w-3.5" /></button>
          </div>
        ))}
        {items.length === 0 && <EmptyState title={`No ${kind}`} copy="Add metadata using the form. Credentials are not accepted here." />}
      </div>
      <FormCard title={`Add ${kind}`}>
        <Field label={primaryLabel} value={primary} onChange={onPrimary} />
        <Field label={secondaryLabel} value={secondary} onChange={onSecondary} />
        <PrimaryButton onClick={onAdd} disabled={!canMutate}><Plus className="h-3.5 w-3.5" />Add metadata</PrimaryButton>
      </FormCard>
    </div>
  )
}

function VariablesView({
  variables,
  name,
  value,
  scope,
  kind,
  onName,
  onValue,
  onScope,
  onKind,
  canMutate,
  onSave,
  onDelete,
}: {
  variables: DataProjectSnapshot['variables']
  name: string
  value: string
  scope: EnvironmentScope
  kind: EnvironmentVariableKind
  onName: (value: string) => void
  onValue: (value: string) => void
  onScope: (value: EnvironmentScope) => void
  onKind: (value: EnvironmentVariableKind) => void
  canMutate: boolean
  onSave: () => void
  onDelete: (id: string) => void
}) {
  return (
    <div className="grid gap-3 lg:grid-cols-[1fr_320px]">
      <div className="space-y-2">
        {variables.map((variable) => (
          <div key={variable.id} className="flex items-center gap-3 rounded-md border border-border bg-card px-3 py-2">
            <KeyRound className="h-3.5 w-3.5 text-primary-bright" />
            <span className="min-w-0 flex-1">
              <span className="block font-mono text-[11px] text-foreground">{variable.name}</span>
              <span className="block text-[10px] text-faint-foreground">{variable.scope} · {variable.kind} · {variable.maskedValue}</span>
            </span>
            <button type="button" disabled={!canMutate} onClick={() => window.confirm(`Delete ${variable.name}?`) && onDelete(variable.id)} className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40"><Trash2 className="h-3.5 w-3.5" /></button>
          </div>
        ))}
      </div>
      <FormCard title="Set environment variable">
        <Field label="Name" value={name} onChange={onName} />
        <label className="block text-[10px] text-faint-foreground">
          Value (never returned)
          <input type={kind === 'secret' ? 'password' : 'text'} value={value} onChange={(event) => onValue(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none" />
        </label>
        <div className="grid grid-cols-2 gap-2">
          <select value={scope} onChange={(event) => onScope(event.target.value as EnvironmentScope)} className="h-9 rounded-md border border-border bg-background px-2 text-[11px] text-foreground">
            {(['development', 'preview', 'production'] as const).map((item) => <option key={item}>{item}</option>)}
          </select>
          <select value={kind} onChange={(event) => onKind(event.target.value as EnvironmentVariableKind)} className="h-9 rounded-md border border-border bg-background px-2 text-[11px] text-foreground">
            <option value="plain">plain</option><option value="secret">secret</option>
          </select>
        </div>
        <PrimaryButton onClick={onSave} disabled={!canMutate}><FileKey2 className="h-3.5 w-3.5" />Save masked variable</PrimaryButton>
      </FormCard>
    </div>
  )
}

function MigrationsView({
  kind,
  onKind,
  tableName,
  onTableName,
  selectedTable,
  preview,
  canPreview,
  onPreview,
  onApply,
  canApply,
}: {
  kind: 'create-table' | 'drop-table'
  onKind: (value: 'create-table' | 'drop-table') => void
  tableName: string
  onTableName: (value: string) => void
  selectedTable?: string
  preview: DataMigrationPreview | null
  canPreview: boolean
  onPreview: () => void
  onApply: () => void
  canApply: boolean
}) {
  return (
    <div className="grid gap-3 lg:grid-cols-[320px_1fr]">
      <FormCard title="Migration operation">
        <select value={kind} onChange={(event) => onKind(event.target.value as typeof kind)} className="h-9 rounded-md border border-border bg-background px-2 text-[11px] text-foreground">
          <option value="create-table">Create table</option>
          <option value="drop-table">Drop selected table</option>
        </select>
        {kind === 'create-table' ? <Field label="Table name" value={tableName} onChange={onTableName} /> : <p className="rounded-md border border-destructive/30 bg-destructive/10 px-2 py-2 text-[10px] text-destructive">Destructive target: {selectedTable ?? 'none selected'}</p>}
        <PrimaryButton onClick={onPreview} disabled={!canPreview}><Play className="h-3.5 w-3.5" />Preview migration</PrimaryButton>
      </FormCard>
      <div className="rounded-lg border border-border bg-card p-3">
        <h3 className="text-[12px] font-medium text-foreground">Preview and confirmation</h3>
        {!preview ? <p className="mt-2 text-[11px] text-faint-foreground">No migration preview yet. Applying is impossible until a server-issued one-time token exists.</p> : (
          <div className="mt-3 space-y-2">
            {preview.changes.map((change, index) => (
              <div key={`${change.operation}-${index}`} className={cn('rounded-md border px-3 py-2 text-[11px]', change.destructive ? 'border-destructive/30 bg-destructive/10 text-destructive' : 'border-border bg-background text-muted-foreground')}>
                <div className="font-medium">{change.operation}{change.destructive ? ' · destructive' : ''}</div>
                <div className="mt-0.5">{change.summary}</div>
              </div>
            ))}
            <p className="font-mono text-[9px] text-faint-foreground">Expires {new Date(preview.expiresAt).toLocaleString()}</p>
            <button type="button" onClick={onApply} disabled={!canApply} className="inline-flex items-center gap-1.5 rounded-md bg-destructive px-3 py-2 text-[11px] font-semibold text-destructive-foreground hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40">
              <CheckCircle2 className="h-3.5 w-3.5" />Confirm and apply migration
            </button>
          </div>
        )}
      </div>
    </div>
  )
}

function ConnectionView({
  endpoint,
  apiKey,
  result,
  storedConnection,
  canMutate,
  onEndpoint,
  onApiKey,
  onConnect,
}: {
  endpoint: string
  apiKey: string
  result: SupabaseConnectionResult | null
  storedConnection?: DataConnectionMetadata
  canMutate: boolean
  onEndpoint: (value: string) => void
  onApiKey: (value: string) => void
  onConnect: () => void
}) {
  return (
    <div className="mx-auto max-w-lg rounded-lg border border-border bg-card p-4">
      <div className="flex items-center gap-2 text-sm font-semibold text-foreground"><Cloud className="h-4 w-4 text-primary-bright" />Test Supabase REST connection</div>
      <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">The key is used only by the server for this connection test, is never echoed, and is cleared from this form after success. Private-network and redirect targets are blocked.</p>
      {storedConnection && (
        <div className="mt-3 rounded-md border border-success/30 bg-success/10 px-3 py-2 text-[10px] text-success">
          Stored server-side · {storedConnection.endpoint} · {storedConnection.schemaTables.length} schema tables · updated {new Date(storedConnection.updatedAt).toLocaleString()}
        </div>
      )}
      <div className="mt-4 space-y-3">
        <Field label="Project endpoint" value={endpoint} onChange={onEndpoint} placeholder="https://project.supabase.co" />
        <label className="block text-[10px] text-faint-foreground">API key<input type="password" value={apiKey} onChange={(event) => onApiKey(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none" /></label>
        <PrimaryButton onClick={onConnect} disabled={!canMutate}><Cloud className="h-3.5 w-3.5" />Test connection</PrimaryButton>
      </div>
      {result && <div role={result.ok ? 'status' : 'alert'} className={cn('mt-3 rounded-md border px-3 py-2 text-[11px]', result.ok ? 'border-success/30 bg-success/10 text-success' : 'border-destructive/30 bg-destructive/10 text-destructive')}>{result.message} · HTTP {result.status} · {result.latencyMs} ms</div>}
      {result?.schemaTables && result.schemaTables.length > 0 && (
        <p className="mt-2 text-[10px] text-faint-foreground">Schema: {result.schemaTables.join(', ')}</p>
      )}
    </div>
  )
}

function AuditView({ events }: { events: DataProjectSnapshot['audit'] }) {
  if (events.length === 0) {
    return <EmptyState title="No audit events" copy="Data mutations will appear here after the server commits them." />
  }
  return (
    <div className="space-y-2">
      {events.map((event) => (
        <div key={event.id} className="rounded-md border border-border bg-card px-3 py-2">
          <div className="flex items-center gap-2 text-[11px]">
            <ScrollText className="h-3.5 w-3.5 text-primary-bright" />
            <span className="font-medium text-foreground">{event.action}</span>
            <span className="font-mono text-faint-foreground">
              {event.resource}{event.resourceId ? `/${event.resourceId}` : ''}
            </span>
            <time className="ml-auto text-[9px] text-faint-foreground" dateTime={event.createdAt}>
              {new Date(event.createdAt).toLocaleString()}
            </time>
          </div>
          {event.details && Object.keys(event.details).length > 0 && (
            <pre className="mt-2 overflow-x-auto rounded bg-background p-2 text-[9px] text-muted-foreground">
              {JSON.stringify(event.details, null, 2)}
            </pre>
          )}
        </div>
      ))}
    </div>
  )
}

function FormCard({ title, children }: { title: string; children: React.ReactNode }) {
  return <div className="space-y-3 rounded-lg border border-border bg-card p-3"><h3 className="text-[12px] font-medium text-foreground">{title}</h3>{children}</div>
}

function Field({ label, value, onChange, placeholder }: { label: string; value: string; onChange: (value: string) => void; placeholder?: string }) {
  return <label className="block text-[10px] text-faint-foreground">{label}<input value={value} onChange={(event) => onChange(event.target.value)} placeholder={placeholder} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none focus:border-primary/60" /></label>
}

function PrimaryButton({ children, onClick, disabled = false }: { children: React.ReactNode; onClick: () => void; disabled?: boolean }) {
  return <button type="button" onClick={onClick} disabled={disabled} className="inline-flex items-center justify-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:cursor-not-allowed disabled:opacity-40">{children}</button>
}

function EmptyState({ title, copy }: { title: string; copy: string }) {
  return <div className="rounded-lg border border-dashed border-border p-8 text-center"><div className="text-[12px] font-medium text-foreground">{title}</div><div className="mt-1 text-[10px] text-faint-foreground">{copy}</div></div>
}

function jsonObject(value: unknown): Readonly<Record<string, JsonValue>> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('Record values must be a JSON object.')
  }
  return value as Readonly<Record<string, JsonValue>>
}

function dataErrorMessage(error: unknown, fallback: string) {
  if (error instanceof PlatformNetworkError) {
    return 'The Go data runtime is unavailable. No local or mock data was substituted.'
  }
  if (error instanceof PlatformHttpError) {
    if (error.status === 401) return 'Your session expired. Sign in again before using project data.'
    if (error.status === 403) return 'Your project role does not allow this data operation.'
    if (error.problem.errors) {
      const details = Object.values(error.problem.errors).flat()
      if (details.length > 0) return details.join(' ')
    }
    return error.problem.detail ?? error.problem.title
  }
  return error instanceof Error && error.message ? error.message : fallback
}
