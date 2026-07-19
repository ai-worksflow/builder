export interface CandidateHeadSelectionItem {
  readonly candidate: {
    readonly id: string
    readonly buildManifest: { readonly id: string }
  }
}

export type CandidateHeadSelection<T extends CandidateHeadSelectionItem> =
  | { readonly kind: 'none' }
  | { readonly kind: 'selected'; readonly head: T }
  | { readonly kind: 'ambiguous'; readonly heads: readonly T[] }

export function resolveCandidateHeadSelection<T extends CandidateHeadSelectionItem>(
  heads: readonly T[],
  targetBuildManifestId: string,
): CandidateHeadSelection<T> {
  const exact = heads.filter((head) => head.candidate.buildManifest.id === targetBuildManifestId)
  const eligible = exact.length > 0 ? exact : heads
  if (eligible.length === 0) return { kind: 'none' }
  if (eligible.length === 1) return { kind: 'selected', head: eligible[0]! }
  return { kind: 'ambiguous', heads: eligible }
}
