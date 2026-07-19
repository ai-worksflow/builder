import type {
  ApplicationBuildContractDto,
  ExactApplicationBuildContractRefDto,
} from './constructor-contract'

export type BuildContractGateReason =
  | 'ready'
  | 'missing'
  | 'not_ready'
  | 'must_missing'
  | 'must_incomplete'
  | 'blocking_gaps'
  | 'blocking_conflicts'
  | 'identity_missing'
  | 'manifest_mismatch'

export interface BuildContractReadiness {
  readonly ready: boolean
  readonly reason: BuildContractGateReason
}

export type BuildContractGatePhase =
  | 'loading'
  | 'compiling'
  | 'missing'
  | 'blocked'
  | 'ready'
  | 'error'

export interface BuildContractGateSnapshot extends BuildContractReadiness {
  readonly bundleId: string
  readonly phase: BuildContractGatePhase
  readonly contract: ApplicationBuildContractDto | null
}

/**
 * A proposal may only use the exact contract selected for the manifest that is
 * still active when the mutation is dispatched. Callers must not normalize a
 * stale/partial reference or replace it with a server-side "latest" lookup.
 */
export function exactBuildContractRefForActiveManifest(
  buildContract: ExactApplicationBuildContractRefDto | null | undefined,
  expectedBuildManifestId: string,
  activeBuildManifestId: string,
): ExactApplicationBuildContractRefDto | null {
  if (
    !expectedBuildManifestId
    || expectedBuildManifestId !== expectedBuildManifestId.trim()
    || expectedBuildManifestId !== activeBuildManifestId
    || !buildContract
    || !buildContract.id
    || buildContract.id !== buildContract.id.trim()
    || !buildContract.contractHash
    || buildContract.contractHash !== buildContract.contractHash.trim()
    || !/^(?:sha256:)?[0-9a-f]{64}$/.test(buildContract.contractHash)
  ) return null

  return buildContract
}

/**
 * Mirrors the server's fail-closed ready projection. This is intentionally
 * based on the typed server contract and its persisted counters; workflow
 * labels, button text, and locally inferred stage names are never authority.
 */
export function evaluateBuildContractReadiness(
  contract: ApplicationBuildContractDto | null | undefined,
  expectedBuildManifestId?: string,
): BuildContractReadiness {
  if (!contract) return { ready: false, reason: 'missing' }
  const canonicalHash = /^(?:sha256:)?[0-9a-f]{64}$/
  if (!canonicalHash.test(contract.contractHash) || !canonicalHash.test(contract.contentHash)) {
    return { ready: false, reason: 'identity_missing' }
  }
  if (
    expectedBuildManifestId
    && (
      contract.buildManifestId !== expectedBuildManifestId
      || contract.contract.buildManifest.id !== expectedBuildManifestId
    )
  ) return { ready: false, reason: 'manifest_mismatch' }
  if (contract.mustCount <= 0) return { ready: false, reason: 'must_missing' }
  if (contract.mustReadyCount !== contract.mustCount) {
    return { ready: false, reason: 'must_incomplete' }
  }
  if (contract.blockingCount > 0) return { ready: false, reason: 'blocking_gaps' }
  if (contract.conflictCount > 0) return { ready: false, reason: 'blocking_conflicts' }
  if (contract.status !== 'ready' || contract.contract.status !== 'ready') {
    return { ready: false, reason: 'not_ready' }
  }
  return { ready: true, reason: 'ready' }
}
