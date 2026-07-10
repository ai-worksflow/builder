export interface WireVersionRef {
  readonly artifactId: string
  readonly revisionId: string
  readonly contentHash: string
  readonly anchorId?: string
}

/**
 * The Go API treats a version reference as an exact immutable identity.
 * `revisionNumber` is deliberately excluded because it is display metadata and
 * strict JSON decoders reject it on command payloads.
 */
export function wireVersionRef(reference: WireVersionRef): WireVersionRef {
  return {
    artifactId: reference.artifactId,
    revisionId: reference.revisionId,
    contentHash: reference.contentHash,
    ...(reference.anchorId ? { anchorId: reference.anchorId } : {}),
  }
}

export function wireVersionRefs(references: readonly WireVersionRef[]) {
  return references.map(wireVersionRef)
}
