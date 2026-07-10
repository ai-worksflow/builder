import type {
  ImplementationProposalDto,
  WorkflowNodeStatus,
  WorkflowRunDto,
  WorkbenchBundleDto,
  WorkspaceRevisionDto,
} from './flow-contract'

export interface WorkbenchQueueReference {
  readonly bundleId: string
  readonly sliceId?: string
  readonly proposalId?: string
}

export interface WorkbenchQueueItem extends WorkbenchQueueReference {
  /** Active manifest. Its id may be derived; bundleId remains the root order identity. */
  readonly bundle: WorkbenchBundleDto | null
  readonly proposal: ImplementationProposalDto | null
}

export interface WorkbenchQueueGroup {
  readonly nodeKey: string
  readonly definitionNodeId: string
  readonly status: WorkflowNodeStatus
  readonly sliceId?: string
  readonly manifestGroupKey?: string
  readonly references: readonly WorkbenchQueueReference[]
}

export function workbenchRootBundleId(bundle: WorkbenchBundleDto) {
  return bundle.rootBuildManifestId ?? bundle.derivedFromBuildManifestId ?? bundle.id
}

export function workbenchBundleNeedsRebase(
  bundle: WorkbenchBundleDto | null | undefined,
  workspace: WorkspaceRevisionDto | null | undefined,
) {
  if (!bundle || !workspace) return false
  return !exactWorkspaceRefEqual(bundle.currentWorkspaceRevision, workspace)
}

export function workbenchQueueItemIndexForProposal(
  queue: readonly WorkbenchQueueItem[],
  proposal: Pick<ImplementationProposalDto, 'id' | 'buildManifestId'>,
) {
  return queue.findIndex((item) =>
    item.proposal?.id === proposal.id
    || item.bundle?.id === proposal.buildManifestId
    || item.bundleId === proposal.buildManifestId,
  )
}

/**
 * Restores the ordered Workbench contract from both forms of node output:
 * the generated bundle/proposal records while the node is waiting, and the
 * ordered proposal id list submitted when the node is completed.
 */
export function workflowWorkbenchQueueGroups(
  run: WorkflowRunDto,
): readonly WorkbenchQueueGroup[] {
  const nodes = run.nodes.filter((node) => node.type === 'workbench_build')
  const legacyBuildManifest = nodes.length === 1
    ? buildManifestValue(run.context.values?.buildManifest)
    : undefined

  return nodes.flatMap((node) => {
    const metadata = run.context.nodes[node.key]
    if (!metadata) return []
    const manifests = buildManifestsFromInput(metadata.input)
    const buildManifest = manifests.length === 1
      ? manifests[0]
      : manifests.length === 0 ? legacyBuildManifest : undefined
    if (!buildManifest) return []
    return [{
      nodeKey: node.key,
      definitionNodeId: node.definitionNodeId,
      status: node.status,
      ...(node.sliceId ?? metadata.sliceId ? { sliceId: node.sliceId ?? metadata.sliceId } : {}),
      ...(buildManifest.manifestGroupKey
        ? { manifestGroupKey: buildManifest.manifestGroupKey }
        : {}),
      references: workbenchReferencesForOutput(buildManifest, metadata.output),
    }]
  })
}

/** Compatibility accessor. Ambiguous multi-group runs deliberately return no global queue. */
export function workflowWorkbenchQueueReferences(
  run: WorkflowRunDto,
): readonly WorkbenchQueueReference[] {
  const groups = workflowWorkbenchQueueGroups(run)
  return groups.length === 1 ? groups[0].references : []
}

interface WorkbenchBuildManifestValue {
  readonly bundleIds: readonly string[]
  readonly sliceIds: readonly string[]
  readonly manifestGroupKey?: string
  readonly hash?: string
}

function workbenchReferencesForOutput(
  buildManifest: WorkbenchBuildManifestValue,
  nodeOutput: unknown,
) {
  const bundleIds = buildManifest.bundleIds
  const sliceIds = buildManifest.sliceIds
  const proposalByBundle = new Map<string, string>()
  const orderedCompletionProposalIds: string[] = []

  const output = objectValue(nodeOutput)
  for (const value of arrayValue(output?.implementationProposals)) {
    const record = objectValue(value)
    if (typeof record?.bundleId !== 'string' || typeof record.proposalId !== 'string') continue
    if (!proposalByBundle.has(record.bundleId)) proposalByBundle.set(record.bundleId, record.proposalId)
  }
  for (const proposalId of stringArray(output?.implementationProposalIds)) {
    if (!orderedCompletionProposalIds.includes(proposalId)) {
      orderedCompletionProposalIds.push(proposalId)
    }
  }

  return bundleIds.map((bundleId, index) => ({
    bundleId,
    ...(sliceIds[index] ? { sliceId: sliceIds[index] } : {}),
    ...(
      proposalByBundle.get(bundleId) ?? orderedCompletionProposalIds[index]
        ? { proposalId: proposalByBundle.get(bundleId) ?? orderedCompletionProposalIds[index] }
        : {}
    ),
  }))
}

function buildManifestsFromInput(input: unknown) {
  const envelope = objectValue(input)
  const manifests = new Map<string, WorkbenchBuildManifestValue>()
  for (const bindingValue of arrayValue(envelope?.bindings)) {
    const binding = objectValue(bindingValue)
    for (const value of [binding?.value, binding?.output]) {
      const manifest = buildManifestValue(value)
      if (!manifest) continue
      const identity = manifest.hash
        ?? `${manifest.manifestGroupKey ?? ''}\u0000${manifest.bundleIds.join('\u0000')}\u0000${manifest.sliceIds.join('\u0000')}`
      manifests.set(identity, manifest)
    }
  }
  return [...manifests.values()]
}

function buildManifestValue(value: unknown): WorkbenchBuildManifestValue | undefined {
  const record = objectValue(value)
  const rawBundleIds = stringArray(record?.bundleIds)
  const bundleIds = uniqueStrings(rawBundleIds)
  if (bundleIds.length === 0 || bundleIds.length !== rawBundleIds.length) return undefined
  const sliceIds = stringArray(record?.sliceIds)
  if (sliceIds.length > 0 && sliceIds.length !== bundleIds.length) return undefined
  const manifestGroupKey = typeof record?.manifestGroupKey === 'string'
    ? record.manifestGroupKey.trim()
    : ''
  const hash = typeof record?.hash === 'string' ? record.hash.trim() : ''
  return {
    bundleIds,
    sliceIds,
    ...(manifestGroupKey ? { manifestGroupKey } : {}),
    ...(hash ? { hash } : {}),
  }
}

export function hydrateWorkbenchQueue(
  references: readonly WorkbenchQueueReference[],
  bundles: readonly WorkbenchBundleDto[],
  proposals: readonly ImplementationProposalDto[],
  previous: readonly WorkbenchQueueItem[] = [],
): readonly WorkbenchQueueItem[] {
  const bundlesById = new Map(bundles.map((bundle) => [bundle.id, bundle]))
  const activeBundleByRoot = new Map<string, WorkbenchBundleDto>()
  for (const bundle of bundles) {
    const rootBundleId = workbenchRootBundleId(bundle)
    const current = activeBundleByRoot.get(rootBundleId)
    if (!current || bundle.id !== rootBundleId) activeBundleByRoot.set(rootBundleId, bundle)
  }
  const proposalsById = new Map(proposals.map((proposal) => [proposal.id, proposal]))
  const previousByBundle = new Map(previous.map((item) => [item.bundleId, item]))

  return references.map((reference) => {
    const prior = previousByBundle.get(reference.bundleId)
    const fetchedProposal = reference.proposalId
      ? proposalsById.get(reference.proposalId) ?? null
      : null
    // A user may regenerate a stale proposal without changing the immutable
    // workflow output. Keep that local replacement until the completion output
    // records its id, while still refreshing server state for matching ids.
    const proposal = prior?.proposal
      && prior.proposal.id !== reference.proposalId
      && !proposalIsApplied(fetchedProposal)
      ? prior.proposal
      : fetchedProposal ?? prior?.proposal ?? null
    const priorBundle = prior?.bundle
      && workbenchRootBundleId(prior.bundle) === reference.bundleId
      ? prior.bundle
      : null
    const proposalBundleCandidate = proposal
      ? bundlesById.get(proposal.buildManifestId)
        ?? (priorBundle?.id === proposal.buildManifestId ? priorBundle : null)
      : null
    const proposalBundle = proposalBundleCandidate
      && workbenchRootBundleId(proposalBundleCandidate) === reference.bundleId
      ? proposalBundleCandidate
      : null
    const bundle = proposal
      ? proposalBundle
      : priorBundle ?? activeBundleByRoot.get(reference.bundleId) ?? null

    return {
      bundleId: reference.bundleId,
      ...(reference.sliceId ?? bundle?.deliverySliceId
        ? { sliceId: reference.sliceId ?? bundle?.deliverySliceId }
        : {}),
      ...(proposal?.id ?? reference.proposalId
        ? { proposalId: proposal?.id ?? reference.proposalId }
        : {}),
      bundle,
      proposal,
    }
  })
}

export function upsertWorkbenchBundle(
  queue: readonly WorkbenchQueueItem[],
  bundle: WorkbenchBundleDto,
): readonly WorkbenchQueueItem[] {
  const rootBundleId = workbenchRootBundleId(bundle)
  const index = queue.findIndex((item) =>
    item.bundleId === rootBundleId || item.bundle?.id === bundle.id,
  )
  const existing = index >= 0 ? queue[index] : undefined
  // Hydration fetches roots and derived manifests together. Never downgrade a
  // queue item from its exact-workspace derivative when the root response lands.
  const activeBundle = bundle.id !== rootBundleId
    ? bundle
    : existing?.bundle && existing.bundle.id !== existing.bundleId
      ? existing.bundle
      : bundle
  // A rebase creates a new immutable input. Decisions from a proposal bound to
  // an older manifest must not migrate to that derivative.
  const matchingProposal = existing?.proposal?.buildManifestId === activeBundle.id
    ? existing.proposal
    : null
  const item: WorkbenchQueueItem = {
    bundleId: rootBundleId,
    ...(existing?.sliceId ?? activeBundle.deliverySliceId
      ? { sliceId: existing?.sliceId ?? activeBundle.deliverySliceId }
      : {}),
    bundle: activeBundle,
    proposal: matchingProposal,
    proposalId: matchingProposal?.id,
  }
  if (index < 0) return [...queue, item]
  return queue.map((current, itemIndex) => itemIndex === index ? { ...current, ...item } : current)
}

export function replaceWorkbenchQueueProposal(
  queue: readonly WorkbenchQueueItem[],
  proposal: ImplementationProposalDto,
  bundle?: WorkbenchBundleDto | null,
): readonly WorkbenchQueueItem[] {
  const rootBundleId = bundle
    ? workbenchRootBundleId(bundle)
    : queue.find((item) => item.bundle?.id === proposal.buildManifestId)?.bundleId
      ?? proposal.buildManifestId
  const index = queue.findIndex((item) =>
    item.bundleId === rootBundleId || item.bundle?.id === proposal.buildManifestId,
  )
  if (index < 0) {
    return [...queue, {
      bundleId: rootBundleId,
      ...(bundle?.deliverySliceId ? { sliceId: bundle.deliverySliceId } : {}),
      proposalId: proposal.id,
      bundle: bundle ?? null,
      proposal,
    }]
  }
  return queue.map((item, itemIndex) => itemIndex === index
    ? {
        ...item,
        proposalId: proposal.id,
        bundle: bundle ?? item.bundle,
        proposal,
      }
    : item)
}

export function proposalIsApplied(proposal: ImplementationProposalDto | null | undefined) {
  return proposal?.status === 'applied' || proposal?.status === 'partially_applied'
}

export function nextPendingWorkbenchQueueIndex(queue: readonly WorkbenchQueueItem[]) {
  return queue.findIndex((item) => !proposalIsApplied(item.proposal))
}

export function workbenchQueueItemHasAppliedPredecessors(
  queue: readonly WorkbenchQueueItem[],
  index: number,
) {
  return index >= 0
    && index < queue.length
    && queue.slice(0, index).every((item) => proposalIsApplied(item.proposal))
}

export function canApplyWorkbenchQueueItem(
  queue: readonly WorkbenchQueueItem[],
  index: number,
) {
  if (index < 0 || index >= queue.length || queue[index].proposal?.status !== 'ready') return false
  return workbenchQueueItemHasAppliedPredecessors(queue, index)
}

/** Returns manifest-ordered ids only when every bundle has one applied proposal. */
export function appliedWorkbenchProposalIds(
  queue: readonly WorkbenchQueueItem[],
): readonly string[] | null {
  if (queue.length === 0 || queue.some((item) => !item.proposal || !proposalIsApplied(item.proposal))) {
    return null
  }
  const ids = queue.map((item) => item.proposal!.id)
  return new Set(ids).size === ids.length ? ids : null
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function arrayValue(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : []
}

function stringArray(value: unknown) {
  return arrayValue(value).filter((item): item is string => typeof item === 'string')
}

function uniqueStrings(values: readonly string[]) {
  return [...new Set(values)]
}

function exactWorkspaceRefEqual(
  bundleRevision: WorkbenchBundleDto['currentWorkspaceRevision'],
  workspace: WorkspaceRevisionDto,
) {
  return Boolean(
    bundleRevision
    && bundleRevision.artifactId === workspace.artifactId
    && bundleRevision.revisionId === workspace.id
    && bundleRevision.contentHash === workspace.contentHash,
  )
}
