export type TemplateReleasePolicyState = 'approved' | 'deprecated' | 'revoked'
export type ApplicationBuildContractStatus = 'ready' | 'blocked' | 'superseded'

export interface ExactApplicationBuildContractRefDto {
  readonly id: string
  readonly contractHash: string
}

export interface ExactTemplateReleaseRefDto {
  readonly id: string
  readonly contentHash: string
  readonly subjectHash: string
}

export interface ExactFullStackTemplateRefDto {
  readonly id: string
  readonly contentHash: string
}

export interface TemplateSourceDto {
  readonly repository: string
  readonly branch: string
  readonly commit: string
  readonly treeHash: string
}

export interface TemplateServiceDto {
  readonly id: string
  readonly kind: string
  readonly rootPath: string
}

export interface TemplateToolchainDto {
  readonly name: string
  readonly version: string
  readonly image: string
}

export interface TemplateCommandDto {
  readonly workingDirectory: string
  readonly argv: readonly string[]
}

export interface TemplatePortDto {
  readonly name: string
  readonly serviceId: string
  readonly number: number
  readonly protocol: string
  readonly exposure: string
}

export interface TemplateHealthCheckDto {
  readonly id: string
  readonly serviceId: string
  readonly portName: string
  readonly path: string
}

export interface TemplateMigrationCommandDto {
  readonly serviceId: string
  readonly commandName: string
}

export interface TemplateBuildOutputDto {
  readonly serviceId: string
  readonly path: string
}

export interface TemplateEnvironmentVariableDto {
  readonly name: string
  readonly required: boolean
  readonly secret: boolean
  readonly description: string
  readonly default?: string
}

export interface TemplateLockfileDto {
  readonly path: string
  readonly digest: string
  readonly registry: string
}

export interface TemplateManifestDto {
  readonly schemaVersion: string
  readonly templateId: string
  readonly displayName: string
  readonly version: string
  readonly services: readonly TemplateServiceDto[]
  readonly toolchains: readonly TemplateToolchainDto[]
  readonly commands: Readonly<Record<string, TemplateCommandDto>>
  readonly ports: readonly TemplatePortDto[]
  readonly healthChecks: readonly TemplateHealthCheckDto[]
  readonly migration?: TemplateMigrationCommandDto
  readonly buildOutputs: readonly TemplateBuildOutputDto[]
  readonly extensionPaths: readonly string[]
  readonly protectedPaths: readonly string[]
  readonly environmentSchema: readonly TemplateEnvironmentVariableDto[]
  readonly lockfiles: readonly TemplateLockfileDto[]
  readonly profileDigest: string
}

export interface TemplateGateEvidenceDto {
  readonly gate: string
  readonly outcome: string
  readonly subjectHash: string
  readonly digest: string
  readonly reference: string
  readonly producer: string
  readonly invocationId: string
  readonly observedAt: string
}

export interface TemplateSignatureDto {
  readonly format: string
  readonly subjectHash: string
  readonly bundleDigest: string
  readonly signer: string
  readonly transparencyLogRef: string
  readonly signedAt: string
}

export interface TemplateReleaseDto {
  readonly id: string
  readonly schemaVersion: string
  readonly admissionAttemptId: string
  readonly source: TemplateSourceDto
  readonly manifest: TemplateManifestDto
  readonly sbomDigest: string
  readonly licenseExpression: string
  readonly licenseDigest: string
  readonly evidenceRefs: readonly TemplateGateEvidenceDto[]
  readonly signature: TemplateSignatureDto
  readonly subjectHash: string
  readonly contentHash: string
  readonly approvedBy: string
  readonly approvedAt: string
}

export interface TemplateReleasePolicyDto {
  readonly templateReleaseId: string
  readonly releaseContentHash: string
  readonly state: TemplateReleasePolicyState
  readonly version: number
  readonly reason: string
  readonly updatedBy: string
  readonly createdAt: string
  readonly updatedAt: string
}

export interface TemplateReleaseRegistrationDto {
  readonly release: TemplateReleaseDto
  readonly policy: TemplateReleasePolicyDto
}

export interface TemplateReleasePageDto {
  readonly items: readonly TemplateReleaseRegistrationDto[]
}

export interface FullStackTemplateComponentDto {
  readonly role: string
  readonly mountPath: string
  readonly release: ExactTemplateReleaseRefDto
}

export interface FullStackTemplateLayoutDto {
  readonly contractTruthSource: string
  readonly openapiPath: string
  readonly generatedClientPath: string
  readonly deploymentPath: string
  readonly testPath: string
  readonly databaseEngine: string
}

export interface FullStackTemplateDto {
  readonly id: string
  readonly schemaVersion: string
  readonly templateId: string
  readonly version: string
  readonly components: readonly FullStackTemplateComponentDto[]
  readonly layout: FullStackTemplateLayoutDto
  readonly contentHash: string
  readonly createdBy: string
  readonly createdAt: string
}

export interface FullStackTemplateRegistrationDto {
  readonly template: FullStackTemplateDto
  readonly components: readonly FullStackTemplateComponentDto[]
}

export interface FullStackTemplatePageDto {
  readonly items: readonly FullStackTemplateRegistrationDto[]
}

export interface CreateApplicationBuildContractInputDto {
  readonly fullStackTemplate: ExactFullStackTemplateRefDto
}

export interface BuildContractCompilerDto {
  readonly version: string
  readonly hash: string
}

export interface BuildContractRevisionRefDto {
  readonly kind: string
  readonly purpose: string
  readonly required: boolean
  readonly artifactId: string
  readonly revisionId: string
  readonly contentHash: string
  readonly approvalStatus: string
}

export interface BuildManifestRefDto {
  readonly id: string
  readonly contentHash: string
}

export interface BuildWorkspaceRevisionRefDto {
  readonly artifactId: string
  readonly revisionId: string
  readonly contentHash: string
}

export interface BuildContractTemplateReleaseRefDto {
  readonly id: string
  readonly releaseHash: string
  readonly role: string
  readonly certification: string
  readonly policyStatus: string
}

export interface BuildContractFullStackTemplateRefDto {
  readonly id: string
  readonly contentHash: string
  readonly certification: string
  readonly policyStatus: string
}

export interface BuildRouteConstraintDto {
  readonly pageNodeId: string
  readonly route: string
  readonly requiredRoles: readonly string[]
  readonly acceptanceCriterionIds: readonly string[]
}

export interface BuildStateConstraintDto {
  readonly pageNodeId: string
  readonly id: string
  readonly key: string
  readonly required: boolean
}

export interface BuildContractBindingDto {
  readonly id: string
  readonly kind: string
  readonly targetId: string
  readonly sourceRevision: BuildContractRevisionRefDto
}

export interface BuildAcceptanceCriterionDto {
  readonly id: string
  readonly statement: string
  readonly requirementIds: readonly string[]
  readonly sourceRevision: BuildContractRevisionRefDto
}

export interface BuildOracleDto {
  readonly id: string
  readonly acceptanceCriterionIds: readonly string[]
  readonly kind: string
  readonly target: string
  readonly commandId?: string
  readonly sourceRevision: BuildContractRevisionRefDto
}

export interface BuildObligationDto {
  readonly id: string
  readonly level: string
  readonly kind: string
  readonly sourceRevision: BuildContractRevisionRefDto
  readonly sourceAnchorId: string
  readonly oracleIds: readonly string[]
  readonly dependsOn: readonly string[]
  readonly waivable: boolean
  readonly status: string
  readonly blockingReasonId?: string
}

export interface BuildGapDto {
  readonly id: string
  readonly code: string
  readonly path: string
  readonly message: string
  readonly sourceId?: string
  readonly obligationIds: readonly string[]
  readonly blocking: boolean
}

export interface BuildConflictDto {
  readonly id: string
  readonly code: string
  readonly message: string
  readonly sourceIds: readonly string[]
  readonly blocking: boolean
}

export interface BuildWaiverDto {
  readonly id: string
  readonly obligationIds: readonly string[]
  readonly reason: string
  readonly approvedBy: string
  readonly expiresAt: string
  readonly alternativeOracleId: string
}

export interface ApplicationBuildContractContentDto {
  readonly schemaVersion: string
  readonly compiler: BuildContractCompilerDto
  readonly projectId: string
  readonly deliverySliceId: string
  readonly buildManifest: BuildManifestRefDto
  readonly baseWorkspaceRevision?: BuildWorkspaceRevisionRefDto
  readonly sourceRevisions: readonly BuildContractRevisionRefDto[]
  readonly fullStackTemplate: BuildContractFullStackTemplateRefDto
  readonly templateReleaseRefs: readonly BuildContractTemplateReleaseRefDto[]
  readonly routes: readonly BuildRouteConstraintDto[]
  readonly states: readonly BuildStateConstraintDto[]
  readonly contractBindings: readonly BuildContractBindingDto[]
  readonly acceptanceCriteria: readonly BuildAcceptanceCriterionDto[]
  readonly oracles: readonly BuildOracleDto[]
  readonly obligations: readonly BuildObligationDto[]
  readonly waivers: readonly BuildWaiverDto[]
  readonly gaps: readonly BuildGapDto[]
  readonly conflicts: readonly BuildConflictDto[]
  readonly forbiddenClaims: readonly string[]
  readonly status: ApplicationBuildContractStatus
}

export interface ApplicationBuildContractDto {
  readonly id: string
  readonly projectId: string
  readonly buildManifestId: string
  readonly status: ApplicationBuildContractStatus
  readonly version: number
  readonly etag: string
  /** Address of the stored immutable response content object. */
  readonly contentHash: string
  /** Canonical identity of `contract`; use this for exact generation gates. */
  readonly contractHash: string
  readonly contract: ApplicationBuildContractContentDto
  readonly mustCount: number
  readonly mustReadyCount: number
  readonly blockingCount: number
  readonly conflictCount: number
  readonly createdBy: string
  readonly createdAt: string
  readonly supersededAt?: string
}

function recordValue(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {}
}

function records(value: unknown) {
  return Array.isArray(value) ? value.map(recordValue) : []
}

function text(value: unknown) {
  return typeof value === 'string' ? value : ''
}

function optionalText(value: unknown) {
  return typeof value === 'string' ? value : undefined
}

function texts(value: unknown) {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}

function nonNegativeInteger(value: unknown) {
  return Number.isSafeInteger(value) && Number(value) >= 0 ? Number(value) : 0
}

function booleanValue(value: unknown) {
  return value === true
}

function buildStatus(value: unknown): ApplicationBuildContractStatus {
  return value === 'ready' || value === 'superseded' ? value : 'blocked'
}

function policyState(value: unknown): TemplateReleasePolicyState {
  return value === 'approved' || value === 'deprecated' ? value : 'revoked'
}

function normalizeExactReleaseRef(value: unknown): ExactTemplateReleaseRefDto {
  const raw = recordValue(value)
  return {
    id: text(raw.id),
    contentHash: text(raw.contentHash),
    subjectHash: text(raw.subjectHash),
  }
}

function normalizeTemplateSource(value: unknown): TemplateSourceDto {
  const raw = recordValue(value)
  return {
    repository: text(raw.repository),
    branch: text(raw.branch),
    commit: text(raw.commit),
    treeHash: text(raw.treeHash),
  }
}

function normalizeTemplateManifest(value: unknown): TemplateManifestDto {
  const raw = recordValue(value)
  const rawCommands = recordValue(raw.commands)
  const commands = Object.fromEntries(Object.entries(rawCommands).map(([name, command]) => {
    const entry = recordValue(command)
    return [name, {
      workingDirectory: text(entry.workingDirectory),
      argv: texts(entry.argv),
    }]
  }))
  const migration = recordValue(raw.migration)
  return {
    schemaVersion: text(raw.schemaVersion),
    templateId: text(raw.templateId),
    displayName: text(raw.displayName),
    version: text(raw.version),
    services: records(raw.services).map((entry) => ({
      id: text(entry.id),
      kind: text(entry.kind),
      rootPath: text(entry.rootPath),
    })),
    toolchains: records(raw.toolchains).map((entry) => ({
      name: text(entry.name),
      version: text(entry.version),
      image: text(entry.image),
    })),
    commands,
    ports: records(raw.ports).map((entry) => ({
      name: text(entry.name),
      serviceId: text(entry.serviceId),
      number: nonNegativeInteger(entry.number),
      protocol: text(entry.protocol),
      exposure: text(entry.exposure),
    })),
    healthChecks: records(raw.healthChecks).map((entry) => ({
      id: text(entry.id),
      serviceId: text(entry.serviceId),
      portName: text(entry.portName),
      path: text(entry.path),
    })),
    ...(Object.keys(migration).length > 0
      ? { migration: { serviceId: text(migration.serviceId), commandName: text(migration.commandName) } }
      : {}),
    buildOutputs: records(raw.buildOutputs).map((entry) => ({
      serviceId: text(entry.serviceId),
      path: text(entry.path),
    })),
    extensionPaths: texts(raw.extensionPaths),
    protectedPaths: texts(raw.protectedPaths),
    environmentSchema: records(raw.environmentSchema).map((entry) => ({
      name: text(entry.name),
      required: booleanValue(entry.required),
      secret: booleanValue(entry.secret),
      description: text(entry.description),
      ...(typeof entry.default === 'string' ? { default: entry.default } : {}),
    })),
    lockfiles: records(raw.lockfiles).map((entry) => ({
      path: text(entry.path),
      digest: text(entry.digest),
      registry: text(entry.registry),
    })),
    profileDigest: text(raw.profileDigest),
  }
}

function normalizeTemplateRelease(value: unknown): TemplateReleaseDto {
  const raw = recordValue(value)
  const signature = recordValue(raw.signature)
  return {
    id: text(raw.id),
    schemaVersion: text(raw.schemaVersion),
    admissionAttemptId: text(raw.admissionAttemptId),
    source: normalizeTemplateSource(raw.source),
    manifest: normalizeTemplateManifest(raw.manifest),
    sbomDigest: text(raw.sbomDigest),
    licenseExpression: text(raw.licenseExpression),
    licenseDigest: text(raw.licenseDigest),
    evidenceRefs: records(raw.evidenceRefs).map((entry) => ({
      gate: text(entry.gate),
      outcome: text(entry.outcome),
      subjectHash: text(entry.subjectHash),
      digest: text(entry.digest),
      reference: text(entry.reference),
      producer: text(entry.producer),
      invocationId: text(entry.invocationId),
      observedAt: text(entry.observedAt),
    })),
    signature: {
      format: text(signature.format),
      subjectHash: text(signature.subjectHash),
      bundleDigest: text(signature.bundleDigest),
      signer: text(signature.signer),
      transparencyLogRef: text(signature.transparencyLogRef),
      signedAt: text(signature.signedAt),
    },
    subjectHash: text(raw.subjectHash),
    contentHash: text(raw.contentHash),
    approvedBy: text(raw.approvedBy),
    approvedAt: text(raw.approvedAt),
  }
}

export function normalizeTemplateReleaseRegistration(value: unknown): TemplateReleaseRegistrationDto {
  const raw = recordValue(value)
  const policy = recordValue(raw.policy)
  return {
    release: normalizeTemplateRelease(raw.release),
    policy: {
      templateReleaseId: text(policy.templateReleaseId),
      releaseContentHash: text(policy.releaseContentHash),
      state: policyState(policy.state),
      version: nonNegativeInteger(policy.version),
      reason: text(policy.reason),
      updatedBy: text(policy.updatedBy),
      createdAt: text(policy.createdAt),
      updatedAt: text(policy.updatedAt),
    },
  }
}

export function normalizeTemplateReleasePage(value: unknown): TemplateReleasePageDto {
  const raw = recordValue(value)
  return { items: records(raw.items).map(normalizeTemplateReleaseRegistration) }
}

function normalizeFullStackComponent(value: unknown): FullStackTemplateComponentDto {
  const raw = recordValue(value)
  return {
    role: text(raw.role),
    mountPath: text(raw.mountPath),
    release: normalizeExactReleaseRef(raw.release),
  }
}

export function normalizeFullStackTemplateRegistration(value: unknown): FullStackTemplateRegistrationDto {
  const raw = recordValue(value)
  const template = recordValue(raw.template)
  const layout = recordValue(template.layout)
  return {
    template: {
      id: text(template.id),
      schemaVersion: text(template.schemaVersion),
      templateId: text(template.templateId),
      version: text(template.version),
      components: Array.isArray(template.components)
        ? template.components.map(normalizeFullStackComponent)
        : [],
      layout: {
        contractTruthSource: text(layout.contractTruthSource),
        openapiPath: text(layout.openapiPath),
        generatedClientPath: text(layout.generatedClientPath),
        deploymentPath: text(layout.deploymentPath),
        testPath: text(layout.testPath),
        databaseEngine: text(layout.databaseEngine),
      },
      contentHash: text(template.contentHash),
      createdBy: text(template.createdBy),
      createdAt: text(template.createdAt),
    },
    components: Array.isArray(raw.components)
      ? raw.components.map(normalizeFullStackComponent)
      : [],
  }
}

export function normalizeFullStackTemplatePage(value: unknown): FullStackTemplatePageDto {
  const raw = recordValue(value)
  return { items: records(raw.items).map(normalizeFullStackTemplateRegistration) }
}

function normalizeRevisionRef(value: unknown): BuildContractRevisionRefDto {
  const raw = recordValue(value)
  return {
    kind: text(raw.kind),
    purpose: text(raw.purpose),
    required: booleanValue(raw.required),
    artifactId: text(raw.artifactId),
    revisionId: text(raw.revisionId),
    contentHash: text(raw.contentHash),
    approvalStatus: text(raw.approvalStatus),
  }
}

function normalizeBuildContractContent(value: unknown): ApplicationBuildContractContentDto {
  const raw = recordValue(value)
  const compiler = recordValue(raw.compiler)
  const manifest = recordValue(raw.buildManifest)
  const workspace = recordValue(raw.baseWorkspaceRevision)
  const fullStack = recordValue(raw.fullStackTemplate)
  return {
    schemaVersion: text(raw.schemaVersion),
    compiler: { version: text(compiler.version), hash: text(compiler.hash) },
    projectId: text(raw.projectId),
    deliverySliceId: text(raw.deliverySliceId),
    buildManifest: { id: text(manifest.id), contentHash: text(manifest.contentHash) },
    ...(Object.keys(workspace).length > 0
      ? {
          baseWorkspaceRevision: {
            artifactId: text(workspace.artifactId),
            revisionId: text(workspace.revisionId),
            contentHash: text(workspace.contentHash),
          },
        }
      : {}),
    sourceRevisions: records(raw.sourceRevisions).map(normalizeRevisionRef),
    fullStackTemplate: {
      id: text(fullStack.id),
      contentHash: text(fullStack.contentHash),
      certification: text(fullStack.certification),
      policyStatus: text(fullStack.policyStatus),
    },
    templateReleaseRefs: records(raw.templateReleaseRefs).map((entry) => ({
      id: text(entry.id),
      releaseHash: text(entry.releaseHash),
      role: text(entry.role),
      certification: text(entry.certification),
      policyStatus: text(entry.policyStatus),
    })),
    routes: records(raw.routes).map((entry) => ({
      pageNodeId: text(entry.pageNodeId),
      route: text(entry.route),
      requiredRoles: texts(entry.requiredRoles),
      acceptanceCriterionIds: texts(entry.acceptanceCriterionIds),
    })),
    states: records(raw.states).map((entry) => ({
      pageNodeId: text(entry.pageNodeId),
      id: text(entry.id),
      key: text(entry.key),
      required: booleanValue(entry.required),
    })),
    contractBindings: records(raw.contractBindings).map((entry) => ({
      id: text(entry.id),
      kind: text(entry.kind),
      targetId: text(entry.targetId),
      sourceRevision: normalizeRevisionRef(entry.sourceRevision),
    })),
    acceptanceCriteria: records(raw.acceptanceCriteria).map((entry) => ({
      id: text(entry.id),
      statement: text(entry.statement),
      requirementIds: texts(entry.requirementIds),
      sourceRevision: normalizeRevisionRef(entry.sourceRevision),
    })),
    oracles: records(raw.oracles).map((entry) => ({
      id: text(entry.id),
      acceptanceCriterionIds: texts(entry.acceptanceCriterionIds),
      kind: text(entry.kind),
      target: text(entry.target),
      ...(typeof entry.commandId === 'string' ? { commandId: entry.commandId } : {}),
      sourceRevision: normalizeRevisionRef(entry.sourceRevision),
    })),
    obligations: records(raw.obligations).map((entry) => ({
      id: text(entry.id),
      level: text(entry.level),
      kind: text(entry.kind),
      sourceRevision: normalizeRevisionRef(entry.sourceRevision),
      sourceAnchorId: text(entry.sourceAnchorId),
      oracleIds: texts(entry.oracleIds),
      dependsOn: texts(entry.dependsOn),
      waivable: booleanValue(entry.waivable),
      status: text(entry.status),
      ...(typeof entry.blockingReasonId === 'string'
        ? { blockingReasonId: entry.blockingReasonId }
        : {}),
    })),
    waivers: records(raw.waivers).map((entry) => ({
      id: text(entry.id),
      obligationIds: texts(entry.obligationIds),
      reason: text(entry.reason),
      approvedBy: text(entry.approvedBy),
      expiresAt: text(entry.expiresAt),
      alternativeOracleId: text(entry.alternativeOracleId),
    })),
    gaps: records(raw.gaps).map((entry) => ({
      id: text(entry.id),
      code: text(entry.code),
      path: text(entry.path),
      message: text(entry.message),
      ...(typeof entry.sourceId === 'string' ? { sourceId: entry.sourceId } : {}),
      obligationIds: texts(entry.obligationIds),
      blocking: booleanValue(entry.blocking),
    })),
    conflicts: records(raw.conflicts).map((entry) => ({
      id: text(entry.id),
      code: text(entry.code),
      message: text(entry.message),
      sourceIds: texts(entry.sourceIds),
      blocking: booleanValue(entry.blocking),
    })),
    forbiddenClaims: texts(raw.forbiddenClaims),
    status: buildStatus(raw.status),
  }
}

/**
 * Converts historical nullable Go slices and partial pre-v2 payloads into the
 * total collection/text contract consumed by the UI. It is a compatibility
 * view only; callers must never persist the normalized value over an immutable
 * server document.
 */
export function normalizeApplicationBuildContract(value: unknown): ApplicationBuildContractDto {
  const raw = recordValue(value)
  const supersededAt = optionalText(raw.supersededAt)
  return {
    id: text(raw.id),
    projectId: text(raw.projectId),
    buildManifestId: text(raw.buildManifestId),
    status: buildStatus(raw.status),
    version: nonNegativeInteger(raw.version),
    etag: text(raw.etag),
    contentHash: text(raw.contentHash),
    contractHash: text(raw.contractHash),
    contract: normalizeBuildContractContent(raw.contract),
    mustCount: nonNegativeInteger(raw.mustCount),
    mustReadyCount: nonNegativeInteger(raw.mustReadyCount),
    blockingCount: nonNegativeInteger(raw.blockingCount),
    conflictCount: nonNegativeInteger(raw.conflictCount),
    createdBy: text(raw.createdBy),
    createdAt: text(raw.createdAt),
    ...(supersededAt ? { supersededAt } : {}),
  }
}
