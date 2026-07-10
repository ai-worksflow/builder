import type {
  ImplementationProposalDto,
  WorkflowRunDto,
  WorkbenchBundleDto,
} from './flow-contract'

export interface WorkbenchQueueReference {
  readonly bundleId: string
  readonly sliceId?: string
  readonly proposalId?: string
}

export interface WorkbenchQueueItem extends WorkbenchQueueReference {
  readonly bundle: WorkbenchBundleDto | null
  readonly proposal: ImplementationProposalDto | null
}

/**
 * Restores the ordered Workbench contract from both forms of node output:
 * the generated bundle/proposal records while the node is waiting, and the
 * ordered proposal id list submitted when the node is completed.
 */
export function workflowWorkbenchQueueReferences(
  run: WorkflowRunDto,
): readonly WorkbenchQueueReference[] {
  const buildManifest = objectValue(run.context.values?.buildManifest)
  const bundleIds = uniqueStrings(stringArray(buildManifest?.bundleIds))
  const sliceIds = stringArray(buildManifest?.sliceIds)
  const proposalByBundle = new Map<string, string>()
  const orderedCompletionProposalIds: string[] = []

  for (const metadata of Object.values(run.context.nodes)) {
    const output = objectValue(metadata.output)
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
  }

  const orderedBundleIds = bundleIds.length > 0
    ? bundleIds
    : [...proposalByBundle.keys()]

  return orderedBundleIds.map((bundleId, index) => ({
    bundleId,
    ...(sliceIds[index] ? { sliceId: sliceIds[index] } : {}),
    ...(
      proposalByBundle.get(bundleId) ?? orderedCompletionProposalIds[index]
        ? { proposalId: proposalByBundle.get(bundleId) ?? orderedCompletionProposalIds[index] }
        : {}
    ),
  }))
}

export function hydrateWorkbenchQueue(
  references: readonly WorkbenchQueueReference[],
  bundles: readonly WorkbenchBundleDto[],
  proposals: readonly ImplementationProposalDto[],
  previous: readonly WorkbenchQueueItem[] = [],
): readonly WorkbenchQueueItem[] {
  const bundlesById = new Map(bundles.map((bundle) => [bundle.id, bundle]))
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
    const bundle = bundlesById.get(reference.bundleId) ?? prior?.bundle ?? null

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
  const index = queue.findIndex((item) => item.bundleId === bundle.id)
  const item: WorkbenchQueueItem = {
    bundleId: bundle.id,
    ...(bundle.deliverySliceId ? { sliceId: bundle.deliverySliceId } : {}),
    bundle,
    proposal: index >= 0 ? queue[index].proposal : null,
    ...(index >= 0 && queue[index].proposalId ? { proposalId: queue[index].proposalId } : {}),
  }
  if (index < 0) return [...queue, item]
  return queue.map((current, itemIndex) => itemIndex === index ? { ...current, ...item } : current)
}

export function replaceWorkbenchQueueProposal(
  queue: readonly WorkbenchQueueItem[],
  proposal: ImplementationProposalDto,
  bundle?: WorkbenchBundleDto | null,
): readonly WorkbenchQueueItem[] {
  const index = queue.findIndex((item) => item.bundleId === proposal.buildManifestId)
  if (index < 0) {
    return [...queue, {
      bundleId: proposal.buildManifestId,
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

export function canApplyWorkbenchQueueItem(
  queue: readonly WorkbenchQueueItem[],
  index: number,
) {
  if (index < 0 || index >= queue.length || queue[index].proposal?.status !== 'ready') return false
  return queue.slice(0, index).every((item) => proposalIsApplied(item.proposal))
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
