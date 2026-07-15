'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  DeliveryClient,
  type DeploymentMetadata,
} from '@/lib/delivery/client'
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
  PublicDeploymentRuntime,
  PublicTablePolicy,
  PublicTablePolicyInput,
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
  Globe2,
  HardDrive,
  KeyRound,
  Loader2,
  Play,
  Plus,
  RefreshCw,
  ScrollText,
  Save,
  ShieldCheck,
  ShieldOff,
  Table2,
  Trash2,
  Users,
} from 'lucide-react'

type DataTab =
  | 'overview'
  | 'tables'
  | 'records'
  | 'public'
  | 'auth'
  | 'storage'
  | 'functions'
  | 'migrations'
  | 'variables'
  | 'connect'
  | 'audit'

const TABS: Array<{ id: DataTab; icon: typeof Database }> = [
  { id: 'overview', icon: Database },
  { id: 'tables', icon: Table2 },
  { id: 'records', icon: Braces },
  { id: 'public', icon: Globe2 },
  { id: 'auth', icon: Users },
  { id: 'storage', icon: HardDrive },
  { id: 'functions', icon: FunctionSquare },
  { id: 'migrations', icon: Play },
  { id: 'variables', icon: FileKey2 },
  { id: 'connect', icon: Cloud },
  { id: 'audit', icon: ScrollText },
]

export function DatabasePanel() {
  const { locale, t } = useI18n()
  const { session, project, platformClient, can } = useCollaboration()
  const projectId = project?.id ?? ''
  const data = platformClient.data
  const publicData = data.publicRuntime
  const delivery = useMemo(
    () => new DeliveryClient(platformClient.http),
    [platformClient.http],
  )
  const canView = session.signedIn && Boolean(project) && can('view')
  const canEdit = session.signedIn && can('edit')
  const canAdmin = session.signedIn && can('admin')
  const canPublish = session.signedIn && can('publish')
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
  const [recordJson, setRecordJson] = useState('{\n  "title": ""\n}')
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
  const [publicPolicies, setPublicPolicies] = useState<readonly PublicTablePolicy[]>([])
  const [publicPolicyDrafts, setPublicPolicyDrafts] = useState<
    Readonly<Record<string, PublicTablePolicyInput>>
  >({})
  const [publicPoliciesLoading, setPublicPoliciesLoading] = useState(false)
  const [publicPolicyMutating, setPublicPolicyMutating] = useState<string | null>(null)
  const [publicError, setPublicError] = useState<string | null>(null)
  const [deployments, setDeployments] = useState<readonly DeploymentMetadata[]>([])
  const [deploymentsLoading, setDeploymentsLoading] = useState(false)
  const [deploymentError, setDeploymentError] = useState<string | null>(null)
  const [selectedDeploymentId, setSelectedDeploymentId] = useState('')
  const [publicDeploymentRuntime, setPublicDeploymentRuntime] =
    useState<PublicDeploymentRuntime | null>(null)
  const [runtimeLoading, setRuntimeLoading] = useState(false)
  const [runtimeChecked, setRuntimeChecked] = useState(false)
  const [runtimeError, setRuntimeError] = useState<string | null>(null)
  const [runtimeRevoking, setRuntimeRevoking] = useState(false)
  const [runtimeReload, setRuntimeReload] = useState(0)
  const refreshSequence = useRef(0)
  const recordsSequence = useRef(0)
  const publicPoliciesSequence = useRef(0)
  const deploymentsSequence = useRef(0)
  const runtimeSequence = useRef(0)

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
      setError(dataErrorMessage(cause, t('database.error.loadProject'), t))
    } finally {
      if (sequence === refreshSequence.current) setLoading(false)
    }
  }, [canView, data, projectId, t])

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
      setError(dataErrorMessage(cause, t('database.error.loadRecords'), t))
    } finally {
      if (sequence === recordsSequence.current) setRecordsLoading(false)
    }
  }, [data, projectId, selectedTableId, t])

  const refreshPublicPolicies = useCallback(async () => {
    if (!canView || !snapshot) {
      publicPoliciesSequence.current += 1
      setPublicPolicies([])
      setPublicPolicyDrafts({})
      setPublicPoliciesLoading(false)
      return
    }
    const sequence = ++publicPoliciesSequence.current
    setPublicPoliciesLoading(true)
    setPublicError(null)
    try {
      const policies = await publicData.listPolicies(projectId)
      if (sequence !== publicPoliciesSequence.current) return
      setPublicPolicies(policies)
      setPublicPolicyDrafts(Object.fromEntries(snapshot.tables.map((table) => [
        table.id,
        publicPolicyInput(policies.find((policy) => policy.tableId === table.id)),
      ])))
    } catch (cause) {
      if (sequence !== publicPoliciesSequence.current) return
      setPublicError(dataErrorMessage(cause, t('database.error.loadPolicies'), t))
    } finally {
      if (sequence === publicPoliciesSequence.current) setPublicPoliciesLoading(false)
    }
  }, [canView, projectId, publicData, snapshot, t])

  const refreshDeployments = useCallback(async () => {
    if (!canView) {
      deploymentsSequence.current += 1
      setDeployments([])
      setSelectedDeploymentId('')
      setDeploymentsLoading(false)
      return
    }
    const sequence = ++deploymentsSequence.current
    setDeploymentsLoading(true)
    setDeploymentError(null)
    try {
      const next = await delivery.list(projectId)
      if (sequence !== deploymentsSequence.current) return
      if (next.some((deployment) => deployment.projectId !== projectId)) {
        throw new Error(t('database.error.foreignDeployment'))
      }
      const sorted = [...next].sort((left, right) =>
        Date.parse(right.updatedAt) - Date.parse(left.updatedAt),
      )
      setDeployments(sorted)
      setSelectedDeploymentId((current) =>
        sorted.some((deployment) => deployment.deploymentId === current)
          ? current
          : sorted.find((deployment) => deployment.activeVersionId)?.deploymentId
            ?? sorted[0]?.deploymentId
            ?? '',
      )
    } catch (cause) {
      if (sequence !== deploymentsSequence.current) return
      setDeploymentError(dataErrorMessage(cause, t('database.error.loadDeployments'), t))
    } finally {
      if (sequence === deploymentsSequence.current) setDeploymentsLoading(false)
    }
  }, [canView, delivery, projectId, t])

  useEffect(() => {
    refreshSequence.current += 1
    recordsSequence.current += 1
    publicPoliciesSequence.current += 1
    deploymentsSequence.current += 1
    runtimeSequence.current += 1
    setSnapshot(null)
    setSelectedTableId('')
    setRecords([])
    setRecordsTotal(0)
    setRecordsOffset(0)
    setMigrationPreview(null)
    setConnection(null)
    setPublicPolicies([])
    setPublicPolicyDrafts({})
    setPublicPoliciesLoading(false)
    setPublicPolicyMutating(null)
    setPublicError(null)
    setDeployments([])
    setDeploymentsLoading(false)
    setDeploymentError(null)
    setSelectedDeploymentId('')
    setPublicDeploymentRuntime(null)
    setRuntimeLoading(false)
    setRuntimeChecked(false)
    setRuntimeError(null)
    setRuntimeRevoking(false)
    setRuntimeReload(0)
    setNotice(null)
  }, [projectId])

  useEffect(() => {
    void refresh()
  }, [refresh])

  useEffect(() => {
    if (tab === 'records' && canView) void refreshRecords(0)
  }, [canView, refreshRecords, tab])

  useEffect(() => {
    if (tab !== 'public' || !canView || !snapshot) return
    void refreshPublicPolicies()
    void refreshDeployments()
  }, [canView, refreshDeployments, refreshPublicPolicies, snapshot, tab])

  useEffect(() => {
    const sequence = ++runtimeSequence.current
    setPublicDeploymentRuntime(null)
    setRuntimeError(null)
    setRuntimeChecked(false)
    if (tab !== 'public' || !canView || !selectedDeploymentId) {
      setRuntimeLoading(false)
      return
    }
    setRuntimeLoading(true)
    void publicData.activeDeploymentRuntime(projectId, selectedDeploymentId)
      .then((runtime) => {
        if (sequence !== runtimeSequence.current) return
        setPublicDeploymentRuntime(runtime)
      })
      .catch((cause: unknown) => {
        if (sequence !== runtimeSequence.current) return
        if (cause instanceof PlatformHttpError && cause.status === 404) return
        setRuntimeError(dataErrorMessage(cause, t('database.error.loadRuntime'), t))
      })
      .finally(() => {
        if (sequence !== runtimeSequence.current) return
        setRuntimeLoading(false)
        setRuntimeChecked(true)
      })
  }, [canView, projectId, publicData, runtimeReload, selectedDeploymentId, t, tab])

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
      setError(dataErrorMessage(cause, t('database.error.operationFailed'), t))
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
        t('database.notice.authAdded'),
      )
    } else if (kind === 'storage-objects') {
      await perform(
        () => data.createMetadata(projectId, kind, {
          bucket: metadataSecondary || 'public',
          path: metadataName,
          sizeBytes: 0,
        }),
        t('database.notice.storageAdded'),
      )
    } else {
      await perform(
        () => data.createMetadata(projectId, kind, {
          name: metadataName,
          entryPath: metadataSecondary || undefined,
          runtime: 'edge',
          status: 'draft',
        }),
        t('database.notice.functionAdded'),
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
      setError(t('database.error.selectTableForMigration'))
      return
    }
    setError(null)
    try {
      setMigrationPreview((await data.previewMigration(projectId, operations)).data.preview)
    } catch (cause) {
      setError(dataErrorMessage(cause, t('database.error.migrationPreview'), t))
    }
  }

  function updatePublicPolicyDraft(
    tableId: string,
    update: (current: PublicTablePolicyInput) => PublicTablePolicyInput,
  ) {
    setPublicPolicyDrafts((current) => ({
      ...current,
      [tableId]: update(
        current[tableId]
          ?? publicPolicyInput(publicPolicies.find((policy) => policy.tableId === tableId)),
      ),
    }))
  }

  async function savePublicPolicy(tableId: string) {
    if (!canAdmin || publicPolicyMutating) {
      if (!canAdmin) setPublicError(t('database.error.adminChangePolicy'))
      return
    }
    const table = snapshot?.tables.find((candidate) => candidate.id === tableId)
    if (!table) {
      setPublicError(t('database.error.refreshBeforePolicy'))
      return
    }
    const currentPolicy = publicPolicies.find((policy) => policy.tableId === tableId)
    if (!currentPolicy?.etag) {
      setPublicError(t('database.error.refreshPolicyEtag'))
      return
    }
    const draft = publicPolicyDrafts[tableId] ?? publicPolicyInput()
    const knownFields = new Set(table.columns.map((column) => column.name))
    const input: PublicTablePolicyInput = {
      ...draft,
      readableFields: draft.allowRead
        ? [...new Set(draft.readableFields.filter((field) => knownFields.has(field)))].sort()
        : [],
      writableFields: draft.allowCreate || draft.allowUpdate
        ? [...new Set(draft.writableFields.filter((field) => knownFields.has(field)))].sort()
        : [],
    }
    publicPoliciesSequence.current += 1
    setPublicPolicyMutating(tableId)
    setPublicError(null)
    setNotice(null)
    try {
      const policy = await publicData.putPolicy(projectId, tableId, input, {
        ifMatch: currentPolicy.etag,
      })
      setPublicPolicies((current) => [
        ...current.filter((candidate) => candidate.tableId !== tableId),
        policy,
      ])
      setPublicPolicyDrafts((current) => ({
        ...current,
        [tableId]: publicPolicyInput(policy),
      }))
      setNotice(t('database.notice.policySaved', {
        table: table.name,
        version: policy.version.toLocaleString(locale),
      }))
    } catch (cause) {
      setPublicError(dataErrorMessage(
        cause,
        t('database.error.savePolicy'),
        t,
      ))
    } finally {
      setPublicPolicyMutating(null)
    }
  }

  async function deletePublicPolicy(tableId: string) {
    if (!canAdmin || publicPolicyMutating) {
      if (!canAdmin) setPublicError(t('database.error.adminRemovePolicy'))
      return
    }
    const table = snapshot?.tables.find((candidate) => candidate.id === tableId)
    if (!table) return
    const currentPolicy = publicPolicies.find((policy) => policy.tableId === tableId)
    if (!currentPolicy?.etag || currentPolicy.version === 0) {
      setPublicError(t('database.error.noSavedPolicy'))
      return
    }
    publicPoliciesSequence.current += 1
    setPublicPolicyMutating(tableId)
    setPublicError(null)
    setNotice(null)
    try {
      await publicData.deletePolicy(projectId, tableId, { ifMatch: currentPolicy.etag })
      await refreshPublicPolicies()
      setNotice(t('database.notice.policyRemoved', { table: table.name }))
    } catch (cause) {
      setPublicError(dataErrorMessage(
        cause,
        t('database.error.removePolicy'),
        t,
      ))
    } finally {
      setPublicPolicyMutating(null)
    }
  }

  async function revokePublicRuntime() {
    if (!canPublish || !selectedDeploymentId || runtimeRevoking) {
      if (!canPublish) setRuntimeError(t('database.error.publishRevokeRuntime'))
      return
    }
    runtimeSequence.current += 1
    setRuntimeRevoking(true)
    setRuntimeError(null)
    setNotice(null)
    try {
      await publicData.revokeDeploymentRuntime(projectId, selectedDeploymentId)
      setPublicDeploymentRuntime(null)
      setRuntimeChecked(true)
      setNotice(t('database.notice.runtimeRevoked'))
    } catch (cause) {
      setRuntimeError(dataErrorMessage(cause, t('database.error.revokeRuntime'), t))
    } finally {
      setRuntimeRevoking(false)
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
              {dataTabLabel(item.id, t)}
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
                {t('database.projectSummary', {
                  project: projectId || t('database.notSelected'),
                })}
              </p>
            </div>
            <button
              type="button"
              onClick={() => void refresh()}
              disabled={loading || !canView}
              className="rounded-md border border-border p-2 text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-50"
              aria-label={t('database.refresh')}
            >
              <RefreshCw className={cn('h-3.5 w-3.5', loading && 'animate-spin')} />
            </button>
          </header>

          {error && <div role="alert" className="mb-3 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[11px] text-destructive">{error}</div>}
          {notice && <div className="mb-3 rounded-md border border-success/30 bg-success/10 px-3 py-2 text-[11px] text-success">{notice}</div>}
          {!session.signedIn ? (
            <div className="rounded-lg border border-warning/30 bg-warning/10 p-6 text-center text-[12px] text-warning">
              {t('database.signInRequired')}
            </div>
          ) : !canView ? (
            <div className="rounded-lg border border-border bg-card p-6 text-center text-[12px] text-muted-foreground">
              {t('database.waitingForAccess')}
            </div>
          ) : loading && !snapshot ? (
            <div className="flex h-48 items-center justify-center"><Loader2 className="h-5 w-5 animate-spin text-primary-bright" /></div>
          ) : !snapshot ? (
            <div className="rounded-lg border border-destructive/30 bg-card p-6 text-center">
              <Database className="mx-auto h-5 w-5 text-destructive" />
              <p className="mt-2 text-[12px] font-medium text-foreground">{t('database.unavailable')}</p>
              <p className="mt-1 text-[10px] text-faint-foreground">
                {t('database.unavailableCopy')}
              </p>
              <button
                type="button"
                onClick={() => void refresh()}
                className="mt-3 rounded-md border border-border px-3 py-2 text-[11px] text-foreground hover:bg-white/5"
              >
                {t('database.retryConnection')}
              </button>
            </div>
          ) : (
            <fieldset disabled={mutating} className={cn('min-w-0', mutating && 'opacity-60')}>
              {tab !== 'overview' && (
                <p className="mb-3 text-[10px] text-faint-foreground">
                  {tab === 'audit'
                    ? t('database.permission.audit')
                    : tab === 'public'
                    ? t('database.permission.public')
                    : tab === 'tables' || tab === 'records' || tab === 'migrations'
                    ? t('database.permission.editor')
                    : t('database.permission.admin')}
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
                    t('database.notice.tableCreated'),
                  )}
                  onDelete={(tableId) => void perform(
                    () => data.deleteTable(projectId, tableId),
                    t('database.notice.tableDeleted'),
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
                      const values = jsonObject(
                        JSON.parse(recordJson) as unknown,
                        t('database.error.recordJson'),
                      )
                      const ok = await perform(
                        () => data.createRecord(projectId, selectedTableId, { values }),
                        t('database.notice.recordCreated'),
                      )
                      if (ok) await refreshRecords(recordsOffset)
                    } catch {
                      setError(t('database.error.recordJson'))
                    }
                  })()}
                  onDelete={(recordId) => void (async () => {
                    const ok = await perform(
                      () => data.deleteRecord(projectId, selectedTableId, recordId),
                      t('database.notice.recordDeleted'),
                    )
                    if (ok) await refreshRecords(recordsOffset)
                  })()}
                  onPrevious={() => void refreshRecords(Math.max(0, recordsOffset - 100))}
                  onNext={() => void refreshRecords(recordsOffset + 100)}
                />
              )}
              {tab === 'public' && (
                <PublicRuntimeView
                  tables={snapshot.tables}
                  policies={publicPolicies}
                  drafts={publicPolicyDrafts}
                  policiesLoading={publicPoliciesLoading}
                  mutatingTableId={publicPolicyMutating}
                  policyError={publicError}
                  canAdmin={canAdmin}
                  onChangeDraft={updatePublicPolicyDraft}
                  onSave={(tableId) => void savePublicPolicy(tableId)}
                  onDelete={(tableId) => void deletePublicPolicy(tableId)}
                  deployments={deployments}
                  deploymentsLoading={deploymentsLoading}
                  deploymentError={deploymentError}
                  selectedDeploymentId={selectedDeploymentId}
                  onSelectDeployment={setSelectedDeploymentId}
                  runtime={publicDeploymentRuntime}
                  runtimeLoading={runtimeLoading}
                  runtimeChecked={runtimeChecked}
                  runtimeError={runtimeError}
                  runtimeRevoking={runtimeRevoking}
                  canPublish={canPublish}
                  onRevoke={() => void revokePublicRuntime()}
                  onRefresh={() => {
                    void refreshPublicPolicies()
                    void refreshDeployments()
                    setRuntimeReload((current) => current + 1)
                  }}
                />
              )}
              {tab === 'auth' && (
                <MetadataView
                  kind="auth-users"
                  items={snapshot.authUsers}
                  primaryLabel={t('database.email')}
                  secondaryLabel={t('database.displayName')}
                  primary={metadataName}
                  secondary={metadataSecondary}
                  onPrimary={setMetadataName}
                  onSecondary={setMetadataSecondary}
                  canMutate={canAdmin}
                  onAdd={() => void addMetadata('auth-users')}
                  onDelete={(id) => void perform(
                    () => data.deleteMetadata(projectId, 'auth-users', id),
                    t('database.notice.authDeleted'),
                  )}
                />
              )}
              {tab === 'storage' && (
                <MetadataView
                  kind="storage-objects"
                  items={snapshot.storageObjects}
                  primaryLabel={t('database.objectPath')}
                  secondaryLabel={t('database.bucket')}
                  primary={metadataName}
                  secondary={metadataSecondary}
                  onPrimary={setMetadataName}
                  onSecondary={setMetadataSecondary}
                  canMutate={canAdmin}
                  onAdd={() => void addMetadata('storage-objects')}
                  onDelete={(id) => void perform(
                    () => data.deleteMetadata(projectId, 'storage-objects', id),
                    t('database.notice.storageDeleted'),
                  )}
                />
              )}
              {tab === 'functions' && (
                <MetadataView
                  kind="server-functions"
                  items={snapshot.serverFunctions}
                  primaryLabel={t('database.functionName')}
                  secondaryLabel={t('database.entryPath')}
                  primary={metadataName}
                  secondary={metadataSecondary}
                  onPrimary={setMetadataName}
                  onSecondary={setMetadataSecondary}
                  canMutate={canAdmin}
                  onAdd={() => void addMetadata('server-functions')}
                  onDelete={(id) => void perform(
                    () => data.deleteMetadata(projectId, 'server-functions', id),
                    t('database.notice.functionDeleted'),
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
                      t('database.notice.migrationApplied'),
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
                    t('database.notice.variableSaved'),
                  )}
                  onDelete={(id) => void perform(
                    () => data.deleteVariable(projectId, id),
                    t('database.notice.variableDeleted'),
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
                      setNotice(t('database.notice.supabaseConnected'))
                      await refresh()
                    } catch (cause) {
                      setError(dataErrorMessage(cause, t('database.error.supabaseConnection'), t))
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
  const { locale, t } = useI18n()
  const cards = [
    [t('database.overview.tables'), snapshot.tables.length, Table2],
    [t('database.overview.records'), snapshot.tables.reduce((sum, table) => sum + table.recordCount, 0), Braces],
    [t('database.overview.authUsers'), snapshot.authUsers.length, Users],
    [t('database.overview.storageObjects'), snapshot.storageObjects.length, HardDrive],
    [t('database.overview.functions'), snapshot.serverFunctions.length, FunctionSquare],
    [t('database.overview.variables'), snapshot.variables.length, FileKey2],
  ] as const
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
      {cards.map(([label, value, Icon]) => (
        <div key={label} className="rounded-lg border border-border bg-card p-4">
          <Icon className="h-4 w-4 text-primary-bright" />
          <div className="mt-3 text-2xl font-semibold text-foreground">{value.toLocaleString(locale)}</div>
          <div className="text-[11px] text-faint-foreground">{label}</div>
        </div>
      ))}
      <div className="col-span-2 rounded-lg border border-border bg-card p-4 md:col-span-3">
        <div className="flex items-center gap-2 text-[12px] font-medium text-foreground">
          <ShieldCheck className="h-4 w-4 text-success" />
          {t('database.overview.secretSafe')}
        </div>
        <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">
          {t('database.overview.secretSafeCopy')}
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
  const { locale, t } = useI18n()
  return (
    <div className="grid gap-3 lg:grid-cols-[1fr_300px]">
      <div className="space-y-2">
        {snapshot.tables.map((table) => (
          <div key={table.id} className="rounded-lg border border-border bg-card p-3">
            <div className="flex items-center gap-2">
              <Table2 className="h-4 w-4 text-primary-bright" />
              <span className="font-mono text-[12px] font-medium text-foreground">{table.name}</span>
              <span className="text-[10px] text-faint-foreground">
                {t('database.recordCount', { count: table.recordCount.toLocaleString(locale) })}
              </span>
              <button type="button" disabled={!canMutate} onClick={() => window.confirm(t('database.confirm.deleteTable', { table: table.name })) && onDelete(table.id)} className="ml-auto rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40" aria-label={t('database.deleteNamed', { name: table.name })}>
                <Trash2 className="h-3.5 w-3.5" />
              </button>
            </div>
            <div className="mt-2 flex flex-wrap gap-1.5">
              {table.columns.map((column) => (
                <span key={column.id} className="rounded bg-white/5 px-2 py-1 font-mono text-[10px] text-muted-foreground">
                  {column.name}: {dataColumnTypeLabel(column.type, t)}{column.required ? '!' : ''}
                </span>
              ))}
              {table.columns.length === 0 && <span className="text-[10px] text-faint-foreground">{t('database.tables.noColumns')}</span>}
            </div>
          </div>
        ))}
        {snapshot.tables.length === 0 && <EmptyState title={t('database.tables.empty')} copy={t('database.tables.emptyCopy')} />}
      </div>
      <FormCard title={t('database.tables.create')}>
        <Field label={t('database.tableName')} value={tableName} onChange={onTableName} />
        <Field label={t('database.tables.firstColumn')} value={columnName} onChange={onColumnName} />
        <label className="block text-[10px] text-faint-foreground">
          {t('database.tables.columnType')}
          <select value={columnType} onChange={(event) => onColumnType(event.target.value as DataColumnType)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground">
            {(['text', 'number', 'boolean', 'date', 'json'] as const).map((type) => (
              <option key={type} value={type}>{dataColumnTypeLabel(type, t)}</option>
            ))}
          </select>
        </label>
        <PrimaryButton onClick={onCreate} disabled={!canMutate}><Plus className="h-3.5 w-3.5" />{t('database.tables.create')}</PrimaryButton>
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
  const { locale, t } = useI18n()
  return (
    <div className="space-y-3">
      <select value={selectedTableId} onChange={(event) => onSelectTable(event.target.value)} className="h-9 rounded-md border border-border bg-card px-3 text-[11px] text-foreground">
        <option value="">{t('database.records.selectTable')}</option>
        {tables.map((table) => <option key={table.id} value={table.id}>{table.name}</option>)}
      </select>
      {selectedTableId && (
        <div className="grid gap-3 lg:grid-cols-[1fr_320px]">
          <div className="space-y-2">
            {loading ? <Loader2 className="h-4 w-4 animate-spin text-primary-bright" /> : records.map((record) => (
              <div key={record.id} className="flex items-start gap-2 rounded-md border border-border bg-card p-3">
                <pre className="min-w-0 flex-1 overflow-x-auto text-[10px] leading-relaxed text-muted-foreground">{JSON.stringify(record.values, null, 2)}</pre>
                <button type="button" disabled={!canMutate} onClick={() => window.confirm(t('database.confirm.deleteRecord')) && onDelete(record.id)} className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40" aria-label={t('database.records.delete')}><Trash2 className="h-3.5 w-3.5" /></button>
              </div>
            ))}
            {!loading && records.length === 0 && <EmptyState title={t('database.records.empty')} copy={t('database.records.emptyCopy')} />}
            {!loading && total > 0 && (
              <div className="flex items-center justify-between rounded-md border border-border bg-card px-3 py-2 text-[10px] text-faint-foreground">
                <span>
                  {t('database.records.range', {
                    start: (offset + 1).toLocaleString(locale),
                    end: Math.min(offset + records.length, total).toLocaleString(locale),
                    total: total.toLocaleString(locale),
                  })}
                </span>
                <span className="flex gap-2">
                  <button
                    type="button"
                    onClick={onPrevious}
                    disabled={offset === 0}
                    className="rounded border border-border px-2 py-1 text-foreground disabled:opacity-40"
                  >
                    {t('database.previous')}
                  </button>
                  <button
                    type="button"
                    onClick={onNext}
                    disabled={offset + records.length >= total}
                    className="rounded border border-border px-2 py-1 text-foreground disabled:opacity-40"
                  >
                    {t('database.next')}
                  </button>
                </span>
              </div>
            )}
          </div>
          <FormCard title={t('database.records.create')}>
            <label className="block text-[10px] text-faint-foreground">
              {t('database.records.jsonValues')}
              <textarea value={recordJson} onChange={(event) => onRecordJson(event.target.value)} rows={8} className="mt-1 w-full resize-y rounded-md border border-border bg-background p-2 font-mono text-[10px] text-foreground outline-none" />
            </label>
            <PrimaryButton onClick={onCreate} disabled={!canMutate}><Plus className="h-3.5 w-3.5" />{t('database.records.create')}</PrimaryButton>
          </FormCard>
        </div>
      )}
    </div>
  )
}

function PublicRuntimeView({
  tables,
  policies,
  drafts,
  policiesLoading,
  mutatingTableId,
  policyError,
  canAdmin,
  onChangeDraft,
  onSave,
  onDelete,
  deployments,
  deploymentsLoading,
  deploymentError,
  selectedDeploymentId,
  onSelectDeployment,
  runtime,
  runtimeLoading,
  runtimeChecked,
  runtimeError,
  runtimeRevoking,
  canPublish,
  onRevoke,
  onRefresh,
}: {
  tables: DataProjectSnapshot['tables']
  policies: readonly PublicTablePolicy[]
  drafts: Readonly<Record<string, PublicTablePolicyInput>>
  policiesLoading: boolean
  mutatingTableId: string | null
  policyError: string | null
  canAdmin: boolean
  onChangeDraft: (
    tableId: string,
    update: (current: PublicTablePolicyInput) => PublicTablePolicyInput,
  ) => void
  onSave: (tableId: string) => void
  onDelete: (tableId: string) => void
  deployments: readonly DeploymentMetadata[]
  deploymentsLoading: boolean
  deploymentError: string | null
  selectedDeploymentId: string
  onSelectDeployment: (deploymentId: string) => void
  runtime: PublicDeploymentRuntime | null
  runtimeLoading: boolean
  runtimeChecked: boolean
  runtimeError: string | null
  runtimeRevoking: boolean
  canPublish: boolean
  onRevoke: () => void
  onRefresh: () => void
}) {
  const { locale, t } = useI18n()
  const selectedDeployment = deployments.find(
    (deployment) => deployment.deploymentId === selectedDeploymentId,
  )

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-3 rounded-lg border border-primary/25 bg-primary/5 p-3">
        <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-primary-bright" />
        <div className="min-w-0 flex-1">
          <h3 className="text-[12px] font-medium text-foreground">{t('database.public.title')}</h3>
          <p className="mt-1 text-[10px] leading-relaxed text-faint-foreground">
            {t('database.public.copy')}
          </p>
        </div>
        <button
          type="button"
          onClick={onRefresh}
          disabled={policiesLoading || deploymentsLoading || runtimeLoading}
          className="rounded border border-border p-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-40"
          aria-label={t('database.public.refresh')}
        >
          <RefreshCw className={cn(
            'h-3.5 w-3.5',
            (policiesLoading || deploymentsLoading || runtimeLoading) && 'animate-spin',
          )} />
        </button>
      </div>

      <section className="rounded-lg border border-border bg-card p-3">
        <div className="flex flex-wrap items-center gap-2">
          <Globe2 className="h-4 w-4 text-primary-bright" />
          <div className="min-w-0 flex-1">
            <h3 className="text-[12px] font-medium text-foreground">{t('database.public.runtimeTitle')}</h3>
            <p className="text-[10px] text-faint-foreground">
              {t('database.public.runtimeCopy')}
            </p>
          </div>
          <select
            value={selectedDeploymentId}
            onChange={(event) => onSelectDeployment(event.target.value)}
            disabled={deploymentsLoading || deployments.length === 0}
            className="h-8 max-w-full rounded-md border border-border bg-background px-2 text-[10px] text-foreground disabled:opacity-50"
            aria-label={t('database.public.selectedDeployment')}
          >
            <option value="">{t('database.public.selectDeployment')}</option>
            {deployments.map((deployment) => (
              <option key={deployment.deploymentId} value={deployment.deploymentId}>
                {environmentLabel(deployment.environment, t)} · {deploymentStatusLabel(deployment.status, t)} · {shortId(deployment.deploymentId)}
              </option>
            ))}
          </select>
        </div>

        {deploymentError && <InlineError message={deploymentError} />}
        {runtimeError && <InlineError message={runtimeError} />}
        {deploymentsLoading && deployments.length === 0 ? (
          <div className="flex h-24 items-center justify-center">
            <Loader2 className="h-4 w-4 animate-spin text-primary-bright" />
          </div>
        ) : deployments.length === 0 ? (
          <div className="mt-3 rounded-md border border-dashed border-border p-4 text-center text-[10px] text-faint-foreground">
            {t('database.public.noDeployment')}
          </div>
        ) : !selectedDeployment ? (
          <p className="mt-3 text-[10px] text-faint-foreground">{t('database.public.selectDeploymentCopy')}</p>
        ) : runtimeLoading ? (
          <div className="flex h-24 items-center justify-center">
            <Loader2 className="h-4 w-4 animate-spin text-primary-bright" />
          </div>
        ) : runtime ? (
          <div className="mt-3 grid gap-3 lg:grid-cols-[1fr_auto]">
            <div className="min-w-0 rounded-md border border-success/25 bg-success/5 p-3">
              <div className="flex flex-wrap items-center gap-2 text-[10px]">
                <span className="rounded-full bg-success/15 px-2 py-0.5 font-medium text-success">{t('database.status.active')}</span>
                <span className="font-mono text-faint-foreground">{t('database.public.capability')} {shortId(runtime.capabilityId)}</span>
                <span className="font-mono text-faint-foreground">{t('database.public.version')} {shortId(runtime.deploymentVersionId)}</span>
              </div>
              <dl className="mt-3 grid gap-2 text-[10px] sm:grid-cols-2">
                <RuntimeDetail
                  label={t('database.public.endpoint')}
                  value={`${runtime.apiBasePath}/${runtime.deploymentId}`}
                />
                <RuntimeDetail label={t('database.public.expires')} value={formatDate(runtime.expiresAt, locale)} />
                <RuntimeDetail
                  label={t('database.public.activated')}
                  value={runtime.activatedAt ? formatDate(runtime.activatedAt, locale) : t('database.public.activationPending')}
                />
                <RuntimeDetail
                  label={t('database.public.deployment')}
                  value={`${environmentLabel(selectedDeployment.environment, t)} · ${deploymentStatusLabel(selectedDeployment.status, t)}`}
                />
              </dl>
              <div className="mt-3">
                <div className="text-[9px] uppercase tracking-wide text-faint-foreground">{t('database.public.allowedOrigins')}</div>
                <div className="mt-1 flex flex-wrap gap-1">
                  {runtime.allowedOrigins.map((origin) => (
                    <span key={origin} className="rounded bg-background px-2 py-1 font-mono text-[9px] text-muted-foreground">
                      {origin}
                    </span>
                  ))}
                </div>
              </div>
            </div>
            <button
              type="button"
              onClick={() => window.confirm(
                t('database.confirm.revokeCapability'),
              ) && onRevoke()}
              disabled={!canPublish || runtimeRevoking}
              className="inline-flex h-9 items-center justify-center gap-1.5 rounded-md border border-destructive/40 px-3 text-[10px] font-medium text-destructive hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {runtimeRevoking
                ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
                : <ShieldOff className="h-3.5 w-3.5" />}
              {t('database.public.revokeCapability')}
            </button>
          </div>
        ) : runtimeChecked && !runtimeError ? (
          <div className="mt-3 rounded-md border border-warning/25 bg-warning/5 p-3 text-[10px] text-warning">
            {t('database.public.noRuntime')}
          </div>
        ) : null}
        {!canPublish && selectedDeployment && (
          <p className="mt-2 text-[9px] text-faint-foreground">
            {t('database.public.cannotRevoke')}
          </p>
        )}
      </section>

      <section>
        <div className="mb-2 flex items-center gap-2">
          <Table2 className="h-4 w-4 text-primary-bright" />
          <div className="min-w-0 flex-1">
            <h3 className="text-[12px] font-medium text-foreground">{t('database.public.policiesTitle')}</h3>
            <p className="text-[10px] text-faint-foreground">
              {t('database.public.policiesCopy')}
            </p>
          </div>
        </div>
        {policyError && <InlineError message={policyError} />}
        {!canAdmin && (
          <div className="mb-2 rounded-md border border-border bg-card px-3 py-2 text-[10px] text-faint-foreground">
            {t('database.public.readOnly')}
          </div>
        )}
        {policiesLoading && tables.length > 0 && policies.length === 0 ? (
          <div className="flex h-28 items-center justify-center rounded-lg border border-border bg-card">
            <Loader2 className="h-4 w-4 animate-spin text-primary-bright" />
          </div>
        ) : tables.length === 0 ? (
          <EmptyState title={t('database.tables.empty')} copy={t('database.public.noTablesCopy')} />
        ) : (
          <div className="space-y-3">
            {tables.map((table) => {
              const policy = policies.find((candidate) => candidate.tableId === table.id)
              const draft = drafts[table.id] ?? publicPolicyInput(policy)
              const writeEnabled = draft.allowCreate || draft.allowUpdate
              const saving = mutatingTableId === table.id
              const dirty = !samePublicPolicyInput(draft, publicPolicyInput(policy))
              return (
                <article key={table.id} className="rounded-lg border border-border bg-card p-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-[12px] font-medium text-foreground">{table.name}</span>
                    {policy && policy.version > 0 ? (
                      <span className="rounded-full bg-primary/10 px-2 py-0.5 text-[9px] text-primary-bright">
                        {t('database.public.savedVersion', { version: policy.version.toLocaleString(locale) })}
                      </span>
                    ) : (
                      <span className="rounded-full bg-warning/10 px-2 py-0.5 text-[9px] text-warning">
                        {t('database.public.defaultDeny')}
                      </span>
                    )}
                    {dirty && <span className="text-[9px] text-warning">{t('database.public.unsaved')}</span>}
                    <span className="ml-auto font-mono text-[8px] text-faint-foreground">{shortId(table.id)}</span>
                  </div>

                  <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
                    <PolicyToggle
                      label={t('database.public.anonymousRead')}
                      checked={draft.allowRead}
                      disabled={!canAdmin || saving}
                      onChange={(checked) => onChangeDraft(table.id, (current) => ({
                        ...current,
                        allowRead: checked,
                        readableFields: checked ? current.readableFields : [],
                      }))}
                    />
                    <PolicyToggle
                      label={t('database.public.anonymousCreate')}
                      checked={draft.allowCreate}
                      disabled={!canAdmin || saving}
                      onChange={(checked) => onChangeDraft(table.id, (current) => ({
                        ...current,
                        allowCreate: checked,
                        writableFields: checked || current.allowUpdate
                          ? current.writableFields
                          : [],
                      }))}
                    />
                    <PolicyToggle
                      label={t('database.public.anonymousUpdate')}
                      checked={draft.allowUpdate}
                      disabled={!canAdmin || saving}
                      onChange={(checked) => onChangeDraft(table.id, (current) => ({
                        ...current,
                        allowUpdate: checked,
                        writableFields: checked || current.allowCreate
                          ? current.writableFields
                          : [],
                      }))}
                    />
                    <PolicyToggle
                      label={t('database.public.anonymousDelete')}
                      checked={draft.allowDelete}
                      disabled={!canAdmin || saving}
                      onChange={(checked) => onChangeDraft(table.id, (current) => ({
                        ...current,
                        allowDelete: checked,
                      }))}
                    />
                  </div>

                  <div className="mt-3 grid gap-3 md:grid-cols-2">
                    <FieldAllowlist
                      title={t('database.public.readableFields')}
                      description={t('database.public.readableFieldsCopy')}
                      columns={table.columns.map((column) => column.name)}
                      selected={draft.readableFields}
                      disabled={!canAdmin || !draft.allowRead || saving}
                      disabledReason={!draft.allowRead ? t('database.public.enableRead') : undefined}
                      onToggle={(field) => onChangeDraft(table.id, (current) => ({
                        ...current,
                        readableFields: toggleField(current.readableFields, field),
                      }))}
                    />
                    <FieldAllowlist
                      title={t('database.public.writableFields')}
                      description={t('database.public.writableFieldsCopy')}
                      columns={table.columns.map((column) => column.name)}
                      selected={draft.writableFields}
                      disabled={!canAdmin || !writeEnabled || saving}
                      disabledReason={!writeEnabled ? t('database.public.enableWrite') : undefined}
                      onToggle={(field) => onChangeDraft(table.id, (current) => ({
                        ...current,
                        writableFields: toggleField(current.writableFields, field),
                      }))}
                    />
                  </div>

                  <div className="mt-3 flex flex-wrap items-center justify-end gap-2 border-t border-border pt-3">
                    <button
                      type="button"
                      onClick={() => window.confirm(
                        t('database.confirm.removePolicy', { table: table.name }),
                      ) && onDelete(table.id)}
                      disabled={!canAdmin || !policy || policy.version === 0 || saving}
                      className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border px-2.5 text-[10px] text-muted-foreground hover:border-destructive/40 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40"
                    >
                      <Trash2 className="h-3.5 w-3.5" />{t('database.public.removePolicy')}
                    </button>
                    <button
                      type="button"
                      onClick={() => onSave(table.id)}
                      disabled={!canAdmin || !dirty || saving}
                      className="inline-flex h-8 items-center gap-1.5 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:cursor-not-allowed disabled:opacity-40"
                    >
                      {saving
                        ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        : <Save className="h-3.5 w-3.5" />}
                      {t('database.public.savePolicy')}
                    </button>
                  </div>
                </article>
              )
            })}
          </div>
        )}
      </section>
    </div>
  )
}

function PolicyToggle({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string
  checked: boolean
  disabled: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <label className={cn(
      'flex items-center gap-2 rounded-md border px-2.5 py-2 text-[10px]',
      checked ? 'border-primary/35 bg-primary/10 text-primary-bright' : 'border-border bg-background text-muted-foreground',
      disabled && 'cursor-not-allowed opacity-55',
    )}>
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
        className="accent-primary"
      />
      {label}
    </label>
  )
}

function FieldAllowlist({
  title,
  description,
  columns,
  selected,
  disabled,
  disabledReason,
  onToggle,
}: {
  title: string
  description: string
  columns: readonly string[]
  selected: readonly string[]
  disabled: boolean
  disabledReason?: string
  onToggle: (field: string) => void
}) {
  const { t } = useI18n()
  return (
    <fieldset disabled={disabled} className="rounded-md border border-border bg-background p-2.5">
      <legend className="sr-only">{title}</legend>
      <div className="text-[10px] font-medium text-foreground">{title}</div>
      <p className="text-[9px] text-faint-foreground">{disabledReason ?? description}</p>
      <div className="mt-2 flex flex-wrap gap-1.5">
        {columns.map((column) => {
          const checked = selected.includes(column)
          return (
            <label key={column} className={cn(
              'inline-flex items-center gap-1.5 rounded border px-2 py-1 font-mono text-[9px]',
              checked ? 'border-primary/35 bg-primary/10 text-primary-bright' : 'border-border text-muted-foreground',
              disabled && 'cursor-not-allowed opacity-50',
            )}>
              <input
                type="checkbox"
                checked={checked}
                onChange={() => onToggle(column)}
                className="accent-primary"
              />
              {column}
            </label>
          )
        })}
        {columns.length === 0 && (
          <span className="text-[9px] text-faint-foreground">{t('database.public.noValueFields')}</span>
        )}
      </div>
    </fieldset>
  )
}

function RuntimeDetail({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-[9px] uppercase tracking-wide text-faint-foreground">{label}</dt>
      <dd className="mt-0.5 break-all font-mono text-[9px] text-muted-foreground">{value}</dd>
    </div>
  )
}

function InlineError({ message }: { message: string }) {
  return (
    <div role="alert" className="mt-3 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">
      {message}
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
  const { t } = useI18n()
  return (
    <div className="grid gap-3 lg:grid-cols-[1fr_300px]">
      <div className="space-y-2">
        {items.map((item) => (
          <div key={item.id} className="flex items-start gap-2 rounded-md border border-border bg-card p-3">
            <pre className="min-w-0 flex-1 overflow-x-auto text-[10px] leading-relaxed text-muted-foreground">{JSON.stringify(item, null, 2)}</pre>
            <button type="button" disabled={!canMutate} onClick={() => window.confirm(t('database.confirm.deleteMetadata')) && onDelete(item.id)} className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40" aria-label={t('database.metadata.delete')}><Trash2 className="h-3.5 w-3.5" /></button>
          </div>
        ))}
        {items.length === 0 && <EmptyState title={t('database.metadata.empty', { kind: metadataKindLabel(kind, t) })} copy={t('database.metadata.emptyCopy')} />}
      </div>
      <FormCard title={t('database.metadata.addKind', { kind: metadataKindLabel(kind, t) })}>
        <Field label={primaryLabel} value={primary} onChange={onPrimary} />
        <Field label={secondaryLabel} value={secondary} onChange={onSecondary} />
        <PrimaryButton onClick={onAdd} disabled={!canMutate}><Plus className="h-3.5 w-3.5" />{t('database.metadata.add')}</PrimaryButton>
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
  const { t } = useI18n()
  return (
    <div className="grid gap-3 lg:grid-cols-[1fr_320px]">
      <div className="space-y-2">
        {variables.map((variable) => (
          <div key={variable.id} className="flex items-center gap-3 rounded-md border border-border bg-card px-3 py-2">
            <KeyRound className="h-3.5 w-3.5 text-primary-bright" />
            <span className="min-w-0 flex-1">
              <span className="block font-mono text-[11px] text-foreground">{variable.name}</span>
              <span className="block text-[10px] text-faint-foreground">{environmentLabel(variable.scope, t)} · {variableKindLabel(variable.kind, t)} · {variable.maskedValue}</span>
            </span>
            <button type="button" disabled={!canMutate} onClick={() => window.confirm(t('database.confirm.deleteVariable', { name: variable.name })) && onDelete(variable.id)} className="rounded p-1.5 text-faint-foreground hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-40" aria-label={t('database.deleteNamed', { name: variable.name })}><Trash2 className="h-3.5 w-3.5" /></button>
          </div>
        ))}
      </div>
      <FormCard title={t('database.variables.set')}>
        <Field label={t('database.variables.name')} value={name} onChange={onName} />
        <label className="block text-[10px] text-faint-foreground">
          {t('database.variables.value')}
          <input type={kind === 'secret' ? 'password' : 'text'} value={value} onChange={(event) => onValue(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none" />
        </label>
        <div className="grid grid-cols-2 gap-2">
          <select value={scope} onChange={(event) => onScope(event.target.value as EnvironmentScope)} className="h-9 rounded-md border border-border bg-background px-2 text-[11px] text-foreground">
            {(['development', 'preview', 'production'] as const).map((item) => <option key={item} value={item}>{environmentLabel(item, t)}</option>)}
          </select>
          <select value={kind} onChange={(event) => onKind(event.target.value as EnvironmentVariableKind)} className="h-9 rounded-md border border-border bg-background px-2 text-[11px] text-foreground">
            <option value="plain">{variableKindLabel('plain', t)}</option><option value="secret">{variableKindLabel('secret', t)}</option>
          </select>
        </div>
        <PrimaryButton onClick={onSave} disabled={!canMutate}><FileKey2 className="h-3.5 w-3.5" />{t('database.variables.save')}</PrimaryButton>
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
  const { locale, t } = useI18n()
  return (
    <div className="grid gap-3 lg:grid-cols-[320px_1fr]">
      <FormCard title={t('database.migrations.operation')}>
        <select value={kind} onChange={(event) => onKind(event.target.value as typeof kind)} className="h-9 rounded-md border border-border bg-background px-2 text-[11px] text-foreground">
          <option value="create-table">{t('database.migrations.createTable')}</option>
          <option value="drop-table">{t('database.migrations.dropTable')}</option>
        </select>
        {kind === 'create-table' ? <Field label={t('database.tableName')} value={tableName} onChange={onTableName} /> : <p className="rounded-md border border-destructive/30 bg-destructive/10 px-2 py-2 text-[10px] text-destructive">{t('database.migrations.destructiveTarget', { table: selectedTable ?? t('database.migrations.noneSelected') })}</p>}
        <PrimaryButton onClick={onPreview} disabled={!canPreview}><Play className="h-3.5 w-3.5" />{t('database.migrations.preview')}</PrimaryButton>
      </FormCard>
      <div className="rounded-lg border border-border bg-card p-3">
        <h3 className="text-[12px] font-medium text-foreground">{t('database.migrations.previewTitle')}</h3>
        {!preview ? <p className="mt-2 text-[11px] text-faint-foreground">{t('database.migrations.noPreview')}</p> : (
          <div className="mt-3 space-y-2">
            {preview.changes.map((change, index) => (
              <div key={`${change.operation}-${index}`} className={cn('rounded-md border px-3 py-2 text-[11px]', change.destructive ? 'border-destructive/30 bg-destructive/10 text-destructive' : 'border-border bg-background text-muted-foreground')}>
                <div className="font-medium">{migrationOperationLabel(change.operation, t)}{change.destructive ? ` · ${t('database.migrations.destructive')}` : ''}</div>
                <div className="mt-0.5">{change.summary}</div>
              </div>
            ))}
            <p className="font-mono text-[9px] text-faint-foreground">{t('database.migrations.expires', { date: new Date(preview.expiresAt).toLocaleString(locale) })}</p>
            <button type="button" onClick={onApply} disabled={!canApply} className="inline-flex items-center gap-1.5 rounded-md bg-destructive px-3 py-2 text-[11px] font-semibold text-destructive-foreground hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40">
              <CheckCircle2 className="h-3.5 w-3.5" />{t('database.migrations.apply')}
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
  const { locale, t } = useI18n()
  return (
    <div className="mx-auto max-w-lg rounded-lg border border-border bg-card p-4">
      <div className="flex items-center gap-2 text-sm font-semibold text-foreground"><Cloud className="h-4 w-4 text-primary-bright" />{t('database.connection.title')}</div>
      <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">{t('database.connection.copy')}</p>
      {storedConnection && (
        <div className="mt-3 rounded-md border border-success/30 bg-success/10 px-3 py-2 text-[10px] text-success">
          {t('database.connection.stored', {
            endpoint: storedConnection.endpoint,
            count: storedConnection.schemaTables.length.toLocaleString(locale),
            date: new Date(storedConnection.updatedAt).toLocaleString(locale),
          })}
        </div>
      )}
      <div className="mt-4 space-y-3">
        <Field label={t('database.connection.endpoint')} value={endpoint} onChange={onEndpoint} placeholder="https://project.supabase.co" />
        <label className="block text-[10px] text-faint-foreground">{t('database.connection.apiKey')}<input type="password" value={apiKey} onChange={(event) => onApiKey(event.target.value)} className="mt-1 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none" /></label>
        <PrimaryButton onClick={onConnect} disabled={!canMutate}><Cloud className="h-3.5 w-3.5" />{t('database.connection.test')}</PrimaryButton>
      </div>
      {result && <div role={result.ok ? 'status' : 'alert'} className={cn('mt-3 rounded-md border px-3 py-2 text-[11px]', result.ok ? 'border-success/30 bg-success/10 text-success' : 'border-destructive/30 bg-destructive/10 text-destructive')}>{result.message} · HTTP {result.status} · {t('database.connection.latency', { value: result.latencyMs.toLocaleString(locale) })}</div>}
      {result?.schemaTables && result.schemaTables.length > 0 && (
        <p className="mt-2 text-[10px] text-faint-foreground">{t('database.connection.schema', { tables: result.schemaTables.join(', ') })}</p>
      )}
    </div>
  )
}

function AuditView({ events }: { events: DataProjectSnapshot['audit'] }) {
  const { locale, t } = useI18n()
  if (events.length === 0) {
    return <EmptyState title={t('database.audit.empty')} copy={t('database.audit.emptyCopy')} />
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
              {new Date(event.createdAt).toLocaleString(locale)}
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

function publicPolicyInput(policy?: PublicTablePolicy): PublicTablePolicyInput {
  return {
    allowRead: policy?.allowRead ?? false,
    allowCreate: policy?.allowCreate ?? false,
    allowUpdate: policy?.allowUpdate ?? false,
    allowDelete: policy?.allowDelete ?? false,
    readableFields: [...(policy?.readableFields ?? [])],
    writableFields: [...(policy?.writableFields ?? [])],
  }
}

function samePublicPolicyInput(
  left: PublicTablePolicyInput,
  right: PublicTablePolicyInput,
) {
  return (
    left.allowRead === right.allowRead &&
    left.allowCreate === right.allowCreate &&
    left.allowUpdate === right.allowUpdate &&
    left.allowDelete === right.allowDelete &&
    [...left.readableFields].sort().join('\u0000') ===
      [...right.readableFields].sort().join('\u0000') &&
    [...left.writableFields].sort().join('\u0000') ===
      [...right.writableFields].sort().join('\u0000')
  )
}

function toggleField(fields: readonly string[], field: string) {
  return fields.includes(field)
    ? fields.filter((candidate) => candidate !== field)
    : [...fields, field].sort()
}

function shortId(value: string) {
  return value.length > 12 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value
}

type Translate = ReturnType<typeof useI18n>['t']

function dataTabLabel(tab: DataTab, t: Translate) {
  switch (tab) {
    case 'overview': return t('database.tab.overview')
    case 'tables': return t('database.tab.tables')
    case 'records': return t('database.tab.records')
    case 'public': return t('database.tab.public')
    case 'auth': return t('database.tab.auth')
    case 'storage': return t('database.tab.storage')
    case 'functions': return t('database.tab.functions')
    case 'migrations': return t('database.tab.migrations')
    case 'variables': return t('database.tab.variables')
    case 'connect': return t('database.tab.connect')
    case 'audit': return t('database.tab.audit')
  }
}

function dataColumnTypeLabel(type: DataColumnType, t: Translate) {
  switch (type) {
    case 'text': return t('database.columnType.text')
    case 'number': return t('database.columnType.number')
    case 'boolean': return t('database.columnType.boolean')
    case 'date': return t('database.columnType.date')
    case 'json': return t('database.columnType.json')
  }
}

function metadataKindLabel(kind: DataMetadataKind, t: Translate) {
  switch (kind) {
    case 'auth-users': return t('database.metadata.authUsers')
    case 'storage-objects': return t('database.metadata.storageObjects')
    case 'server-functions': return t('database.metadata.serverFunctions')
  }
}

function environmentLabel(environment: string, t: Translate) {
  switch (environment) {
    case 'development': return t('database.environment.development')
    case 'preview': return t('database.environment.preview')
    case 'production': return t('database.environment.production')
    default: return environment
  }
}

function variableKindLabel(kind: EnvironmentVariableKind, t: Translate) {
  return kind === 'secret' ? t('database.variableKind.secret') : t('database.variableKind.plain')
}

function deploymentStatusLabel(status: string, t: Translate) {
  switch (status) {
    case 'pending': return t('database.status.pending')
    case 'building': return t('database.status.building')
    case 'ready': return t('database.status.ready')
    case 'active': return t('database.status.active')
    case 'failed': return t('database.status.failed')
    case 'cancelled': return t('database.status.cancelled')
    case 'revoked': return t('database.status.revoked')
    default: return status.replaceAll('_', ' ')
  }
}

function migrationOperationLabel(operation: string, t: Translate) {
  switch (operation) {
    case 'create-table': return t('database.migrations.createTable')
    case 'drop-table': return t('database.migrations.dropTable')
    case 'add-column': return t('database.migrations.addColumn')
    case 'drop-column': return t('database.migrations.dropColumn')
    default: return operation.replaceAll('-', ' ')
  }
}

function formatDate(value: string, locale: string) {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString(locale) : value
}

function jsonObject(
  value: unknown,
  errorMessage: string,
): Readonly<Record<string, JsonValue>> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error(errorMessage)
  }
  return value as Readonly<Record<string, JsonValue>>
}

function dataErrorMessage(error: unknown, fallback: string, t: Translate) {
  if (error instanceof PlatformNetworkError) {
    return t('database.error.network')
  }
  if (error instanceof PlatformHttpError) {
    if (error.status === 401) return t('database.error.sessionExpired')
    if (error.status === 403) return t('database.error.forbidden')
    if (error.problem.errors) {
      const details = Object.values(error.problem.errors).flat()
      if (details.length > 0) return details.join(' ')
    }
    return error.problem.detail ?? error.problem.title
  }
  return error instanceof Error && error.message ? error.message : fallback
}
