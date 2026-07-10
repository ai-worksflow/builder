import assert from 'node:assert/strict'
import {
  PROJECT_CATALOG_SCHEMA,
  PROJECT_CATALOG_VERSION,
  addProjectAttachment,
  addProjectToCatalog,
  cloneProductProject,
  cloneProject,
  createBlankProject,
  createProjectCatalog,
  createProjectFromImport,
  createProjectFromTemplate,
  deleteProject,
  isProductProject,
  isProjectCatalog,
  listProjectSummaries,
  migrateProjectCatalog,
  recordDeployment,
  recordGeneration,
  recordProjectVersion,
  renameProject,
  selectProject,
  setProjectStarred,
  updateDatabaseSettings,
  updateDeploymentSettings,
  updateGithubSettings,
  updateProjectWorkspace,
} from '../lib/worksflow/project-catalog'
import type {
  ProjectCatalogIdKind,
  ProjectCatalogRuntime,
} from '../lib/worksflow/project-catalog'
import {
  createCheckpoint,
  createWorkspace,
  getWorkspaceFile,
  upsertFile,
} from '../lib/worksflow/workspace-model'

type TestCase = {
  name: string
  run: () => void
}

const NOW = '2026-07-10T08:00:00.000Z'
const LATER = '2026-07-10T09:00:00.000Z'

function deterministicRuntime(now = NOW): ProjectCatalogRuntime {
  const counts = new Map<ProjectCatalogIdKind, number>()
  return {
    now: () => now,
    createId: (kind) => {
      const count = (counts.get(kind) ?? 0) + 1
      counts.set(kind, count)
      return `${kind}-${count}`
    },
  }
}

const tests: TestCase[] = []

function test(name: string, run: () => void) {
  tests.push({ name, run })
}

test('creates a JSON-serializable catalog and guards every nested layer strictly', () => {
  const runtime = deterministicRuntime()
  const project = createBlankProject(
    {
      id: 'project-alpha',
      name: 'Alpha',
      teamId: 'team-1',
      teamName: 'Core team',
      files: [{ path: 'src/index.ts', content: 'export const alpha = true' }],
      teamReferences: {
        documents: [
          {
            id: 'doc-1',
            type: 'requirement',
            title: 'Requirements',
            status: 'approved',
            updatedAt: NOW,
          },
        ],
        dependencies: [],
        blueprints: [
          {
            id: 'blueprint-1',
            title: 'Core flow',
            status: 'validated',
            version: 1,
            updatedAt: NOW,
          },
        ],
      },
    },
    runtime,
  )
  const catalog = createProjectCatalog(
    { projects: [project], selectedProjectId: project.id, createdAt: NOW },
    runtime,
  )
  const roundtrip: unknown = JSON.parse(JSON.stringify(catalog))

  assert.equal(isProductProject(project), true)
  assert.equal(isProjectCatalog(roundtrip), true)
  assert.equal(Object.isFrozen(catalog), true)
  assert.equal(Object.isFrozen(catalog.projects), true)
  assert.equal(Object.isFrozen(catalog.projects[0].workspace.files), true)

  const unknownField = JSON.parse(JSON.stringify(catalog)) as Record<string, unknown>
  unknownField.unexpected = true
  assert.equal(isProjectCatalog(unknownField), false)

  const brokenBranch = JSON.parse(JSON.stringify(catalog)) as {
    projects: Array<{ workspace: { activeBranchId: string } }>
  }
  brokenBranch.projects[0].workspace.activeBranchId = 'missing'
  assert.equal(isProjectCatalog(brokenBranch), false)

  const poisonedSettings = JSON.parse(JSON.stringify(catalog)) as {
    projects: Array<{ githubSettings: Record<string, unknown> }>
  }
  poisonedSettings.projects[0].githubSettings.token = 'must-not-persist'
  assert.equal(isProjectCatalog(poisonedSettings), false)
})

test('creates isolated blank, template and imported projects', () => {
  const runtime = deterministicRuntime()
  const template = createBlankProject(
    {
      id: 'template-1',
      name: 'Dashboard template',
      description: 'Reusable dashboard',
      files: [
        { path: 'index.html', content: '<main>Template</main>' },
        { path: 'styles.css', content: 'main { color: navy; }' },
      ],
      attachments: [{ id: 'asset-1', name: 'wireframe.png', kind: 'image' }],
    },
    runtime,
  )
  const fromTemplate = createProjectFromTemplate(
    template,
    { id: 'project-from-template', name: 'Customer dashboard' },
    runtime,
  )
  const importedWorkspace = createWorkspace({
    id: 'external-workspace',
    name: 'Imported repository',
    createdAt: NOW,
    files: [{ path: 'src/main.ts', content: 'console.log("imported")' }],
  })
  const imported = createProjectFromImport(
    {
      id: 'project-imported',
      name: 'Imported app',
      workspace: importedWorkspace,
      importProvider: 'github',
      importReference: 'owner/repository',
    },
    runtime,
  )

  assert.equal(fromTemplate.source.kind, 'template')
  assert.equal(fromTemplate.source.templateId, template.id)
  assert.equal(fromTemplate.generationRuns.length, 0)
  assert.equal(getWorkspaceFile(fromTemplate.workspace, 'index.html')?.content, '<main>Template</main>')
  assert.notEqual(fromTemplate.workspace, template.workspace)
  assert.notEqual(fromTemplate.workspace.files, template.workspace.files)
  assert.notEqual(fromTemplate.attachments, template.attachments)

  assert.equal(imported.source.kind, 'import')
  assert.equal(imported.source.importProvider, 'github')
  assert.equal(imported.source.importReference, 'owner/repository')
  assert.notEqual(imported.workspace, importedWorkspace)
  assert.notEqual(imported.workspace.files[0], importedWorkspace.files[0])
})

test('clones deeply and catalog edits never mutate earlier snapshots', () => {
  const runtime = deterministicRuntime()
  const original = createBlankProject(
    {
      id: 'original',
      name: 'Original',
      files: [{ path: 'src/app.ts', content: 'export const version = 1' }],
      attachments: [{ id: 'attachment-original', name: 'brief.md', kind: 'document' }],
    },
    runtime,
  )
  const standaloneClone = cloneProductProject(
    original,
    { id: 'standalone-clone', name: 'Standalone clone' },
    runtime,
  )
  const initialCatalog = createProjectCatalog({ projects: [original], createdAt: NOW }, runtime)
  const withClone = cloneProject(
    initialCatalog,
    original.id,
    { id: 'catalog-clone', name: 'Catalog clone' },
    runtime,
  )
  const renamed = renameProject(withClone, 'catalog-clone', 'Renamed clone', {
    now: () => LATER,
  })
  const starred = setProjectStarred(renamed, 'catalog-clone', true, { now: () => LATER })

  assert.notEqual(standaloneClone.workspace, original.workspace)
  assert.notEqual(standaloneClone.workspace.files[0], original.workspace.files[0])
  assert.notEqual(standaloneClone.attachments[0], original.attachments[0])
  assert.equal(standaloneClone.source.kind, 'clone')
  assert.equal(standaloneClone.source.clonedFromProjectId, original.id)

  assert.equal(initialCatalog.projects.length, 1)
  assert.equal(withClone.projects.length, 2)
  assert.equal(withClone.selectedProjectId, 'catalog-clone')
  assert.equal(withClone.projects[1].name, 'Catalog clone')
  assert.equal(renamed.projects[1].name, 'Renamed clone')
  assert.equal(renamed.projects[1].starred, false)
  assert.equal(starred.projects[1].starred, true)
  assert.equal(starred.projects[1].updatedAt, LATER)
  assert.equal(original.name, 'Original')
})

test('selects, updates and deletes projects while preserving a nonempty fallback', () => {
  const runtime = deterministicRuntime()
  const first = createBlankProject({ id: 'first', name: 'First' }, runtime)
  const second = createBlankProject({ id: 'second', name: 'Second' }, runtime)
  const initial = createProjectCatalog({ projects: [first], createdAt: NOW }, runtime)
  const twoProjects = addProjectToCatalog(initial, second, { select: false }, runtime)
  const selected = selectProject(twoProjects, second.id, runtime)
  const updated = updateProjectWorkspace(
    selected,
    second.id,
    (workspace) => upsertFile(workspace, { path: 'src/new.ts', content: 'export {}' }),
    runtime,
  )
  const oneProject = deleteProject(updated, second.id, {}, runtime)
  const fallback = deleteProject(
    oneProject,
    first.id,
    { fallbackProject: { id: 'fallback', name: 'Fresh start' } },
    runtime,
  )

  assert.equal(twoProjects.selectedProjectId, first.id)
  assert.equal(selected.selectedProjectId, second.id)
  assert.equal(getWorkspaceFile(selected.projects[1].workspace, 'src/new.ts'), undefined)
  assert.equal(getWorkspaceFile(updated.projects[1].workspace, 'src/new.ts')?.content, 'export {}')
  assert.equal(oneProject.projects.length, 1)
  assert.equal(oneProject.selectedProjectId, first.id)
  assert.equal(fallback.projects.length, 1)
  assert.equal(fallback.projects[0].id, 'fallback')
  assert.equal(fallback.projects[0].name, 'Fresh start')
  assert.equal(fallback.selectedProjectId, 'fallback')
  assert.equal(isProjectCatalog(fallback), true)
  assert.throws(() => selectProject(fallback, 'missing', runtime), /Unknown project/)
})

test('records generation, checkpoints, versions and deployments as compact summaries', () => {
  const runtime = deterministicRuntime()
  const project = createBlankProject(
    {
      id: 'activity-project',
      name: 'Activity project',
      files: [{ path: 'src/app.ts', content: 'export const ready = true', dirty: true }],
    },
    runtime,
  )
  let catalog = createProjectCatalog({ projects: [project], createdAt: NOW }, runtime)
  catalog = updateProjectWorkspace(
    catalog,
    project.id,
    (workspace) => createCheckpoint(workspace, { id: 'checkpoint-1', createdAt: NOW }),
    runtime,
  )
  catalog = recordGeneration(
    catalog,
    project.id,
    {
      id: 'run-1',
      prompt: 'Build the application',
      mode: 'build',
      model: 'test-model',
      provider: 'openai',
      status: 'completed',
      startedAt: NOW,
      completedAt: LATER,
      createdFileCount: 1,
      inputTokens: 120,
      outputTokens: 80,
      totalTokens: 200,
      durationMs: 1500,
      costUsd: 0.0042,
      maxTokens: 24000,
      events: [
        {
          id: 'event-1',
          type: 'file',
          summary: 'Created application entry',
          path: 'src/app.ts',
          createdAt: LATER,
        },
      ],
    },
    runtime,
  )
  catalog = recordProjectVersion(
    catalog,
    project.id,
    {
      id: 'version-1',
      label: 'Initial build',
      workspaceCheckpointId: 'checkpoint-1',
      branchId: 'main',
      generationRunId: 'run-1',
    },
    runtime,
  )
  catalog = recordDeployment(
    catalog,
    project.id,
    {
      id: 'deployment-1',
      provider: 'vercel',
      status: 'ready',
      environment: 'production',
      createdAt: NOW,
      completedAt: LATER,
      url: 'https://example.test/app',
      commitSha: 'abc123',
    },
    runtime,
  )

  const recorded = catalog.projects[0]
  const summaries = listProjectSummaries(catalog)
  assert.equal(recorded.generationRuns[0].eventCount, 1)
  assert.equal(recorded.generationRuns[0].events[0].path, 'src/app.ts')
  assert.equal(recorded.generationRuns[0].provider, 'openai')
  assert.equal(recorded.generationRuns[0].totalTokens, 200)
  assert.equal(recorded.generationRuns[0].durationMs, 1500)
  assert.equal(recorded.generationRuns[0].costUsd, 0.0042)
  assert.equal(recorded.generationRuns[0].maxTokens, 24000)
  assert.equal(recorded.latestVersionId, 'version-1')
  assert.equal(recorded.versions[0].workspaceCheckpointId, 'checkpoint-1')
  assert.equal(recorded.deployments[0].status, 'ready')
  assert.equal(recorded.deploymentSettings.provider, 'vercel')
  assert.equal(recorded.deploymentSettings.productionUrl, 'https://example.test/app')
  assert.equal(summaries[0].latestGenerationStatus, 'completed')
  assert.equal(summaries[0].latestVersionLabel, 'Initial build')
  assert.equal(summaries[0].latestDeploymentStatus, 'ready')
  assert.equal(isProjectCatalog(catalog), true)
})

test('retains setting names but redacts all supplied secret values and URL credentials', () => {
  const runtime = deterministicRuntime()
  const project = createBlankProject(
    {
      id: 'safe-project',
      name: 'Safe project',
      deploymentSettings: {
        provider: 'vercel',
        status: 'ready',
        token: 'deploy-token-value',
        productionUrl: 'https://user:password@example.test/app?token=url-token&ref=public',
        environmentVariables: {
          OPENAI_API_KEY: 'openai-secret-value',
          PUBLIC_ORIGIN: 'https://public.test',
        },
      },
      databaseSettings: {
        provider: 'neon',
        status: 'ready',
        connectionString: 'postgres://database-secret',
        secrets: { DATABASE_URL: 'database-secret-value' },
      },
      githubSettings: {
        status: 'connected',
        owner: 'octo',
        repository: 'builder',
        token: 'github-token-value',
      },
    },
    runtime,
  )
  let catalog = createProjectCatalog({ projects: [project], createdAt: NOW }, runtime)
  catalog = updateDeploymentSettings(
    catalog,
    project.id,
    {
      provider: 'vercel',
      status: 'ready',
      apiKey: 'replacement-deploy-secret',
      environmentVariables: { RELEASE_KEY: 'release-secret-value' },
    },
    runtime,
  )
  catalog = updateDatabaseSettings(
    catalog,
    project.id,
    {
      provider: 'neon',
      status: 'ready',
      password: 'replacement-database-secret',
      secrets: { DATABASE_URL: 'replacement-database-url' },
    },
    runtime,
  )
  catalog = updateGithubSettings(
    catalog,
    project.id,
    {
      status: 'connected',
      owner: 'octo',
      repository: 'builder',
      accessToken: 'replacement-github-secret',
    },
    runtime,
  )
  catalog = addProjectAttachment(
    catalog,
    project.id,
    {
      id: 'safe-link',
      name: 'Design source',
      kind: 'url',
      sourceUrl: 'https://design.test/file?access_token=attachment-secret&node=42',
    },
    runtime,
  )
  catalog = recordDeployment(
    catalog,
    project.id,
    {
      id: 'safe-deployment',
      provider: 'vercel',
      status: 'failed',
      token: 'record-token-secret',
      url: 'https://example.test/preview?signature=record-url-secret&view=public',
      errorMessage: 'authorization=record-error-secret failed',
    },
    runtime,
  )

  const serialized = JSON.stringify(catalog)
  ;[
    'deploy-token-value',
    'url-token',
    'openai-secret-value',
    'database-secret',
    'database-secret-value',
    'github-token-value',
    'replacement-deploy-secret',
    'release-secret-value',
    'replacement-database-secret',
    'replacement-database-url',
    'replacement-github-secret',
    'attachment-secret',
    'record-token-secret',
    'record-url-secret',
    'record-error-secret',
  ].forEach((secret) => assert.equal(serialized.includes(secret), false, secret))

  const safe = catalog.projects[0]
  assert.deepEqual(safe.deploymentSettings.environmentVariableNames, ['RELEASE_KEY'])
  assert.deepEqual(safe.databaseSettings.secretNames, ['DATABASE_URL'])
  assert.equal(safe.githubSettings.owner, 'octo')
  assert.equal(safe.attachments[0].sourceUrl, 'https://design.test/file?node=42')
  assert.equal(safe.deployments[0].url, 'https://example.test/preview?view=public')
  assert.match(safe.deployments[0].errorMessage ?? '', /\[REDACTED\]/)
  assert.equal(isProjectCatalog(JSON.parse(serialized)), true)
})

test('migrates legacy recent-project records into a selected versioned catalog', () => {
  const runtime = deterministicRuntime()
  const legacy = {
    version: 1,
    selectedProjectId: 'legacy-2',
    projects: [
      {
        id: 'legacy-1',
        name: 'Legacy one',
        teamName: 'Legacy team',
        updatedAt: NOW,
        starred: false,
      },
      {
        id: 'legacy-2',
        name: 'Legacy two',
        teamName: 'Legacy team',
        updatedAt: NOW,
        starred: true,
      },
    ],
  }
  const migrated = migrateProjectCatalog(
    legacy,
    { fromVersion: 1, toVersion: PROJECT_CATALOG_VERSION },
    runtime,
  )

  assert.equal(migrated.schema, PROJECT_CATALOG_SCHEMA)
  assert.equal(migrated.version, PROJECT_CATALOG_VERSION)
  assert.equal(migrated.projects.length, 2)
  assert.equal(migrated.selectedProjectId, 'legacy-2')
  assert.equal(migrated.projects[1].starred, true)
  assert.equal(migrated.projects[1].teamName, 'Legacy team')
  assert.equal(isProjectCatalog(migrated), true)
  assert.equal(Object.isFrozen(migrated), true)
  assert.equal(Object.isFrozen(migrated.projects[0].workspace.checkpoints), true)

  const emptyMigration = migrateProjectCatalog(
    { version: 1, projects: [] },
    { fromVersion: 1, toVersion: PROJECT_CATALOG_VERSION },
    deterministicRuntime(),
  )
  assert.equal(emptyMigration.projects.length, 1)
  assert.equal(emptyMigration.selectedProjectId, emptyMigration.projects[0].id)
})

let failures = 0

tests.forEach(({ name, run }) => {
  try {
    run()
    console.log(`✓ ${name}`)
  } catch (error) {
    failures += 1
    console.error(`✗ ${name}`)
    console.error(error)
  }
})

if (failures > 0) {
  console.error(`${failures} project catalog test(s) failed.`)
  process.exit(1)
}

console.log(`${tests.length} project catalog test(s) passed.`)
