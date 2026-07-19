'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Archive,
  CheckCircle2,
  CircleAlert,
  ExternalLink,
  GitBranch,
  LoaderCircle,
  RefreshCw,
  Rocket,
  RotateCcw,
  ShieldCheck,
  X,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  DeliveryClient,
  downloadBlob,
  type DeliveryEnvironment,
  type DeploymentMetadata,
} from '@/lib/delivery/client'
import {
  selectLatestPassingQualityRun,
  selectReleaseBuildManifestId,
} from '@/lib/delivery/release-provenance'
import { QualityClient } from '@/lib/quality/client'
import type { QualityRunResult } from '@/lib/quality/types'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import { PlatformHttpError } from '@/lib/platform/http'
import { ReleaseClient } from '@/lib/platform/release-client'
import {
  hasReleaseDeliveryMutationLock,
  isReleaseDeliveryRunTerminal,
  selectReleaseDeliveryReconciliationCaseForRun,
  selectReleaseDeliveryRunForDisplay,
} from '@/lib/platform/release-contract'
import type {
  ReleaseBundleDto,
  ReleaseDeliveryOperationKind,
  ReleaseDeliveryReconciliationBlockDto,
  ReleaseDeliveryReconciliationCaseDto,
  ReleaseDeliveryRunState,
  ReleasePreviewReceiptDto,
  ReleasePreviewRunDto,
  ReleaseProductionReceiptDto,
  ReleaseProductionRunDto,
  ReleasePromotionApprovalDto,
} from '@/lib/platform/release-contract'
import { VerificationClient } from '@/lib/platform/verification-client'
import type {
  CanonicalVerificationRunViewDto,
  VerificationProfileReferenceDto,
  VerificationProfileSummaryDto,
} from '@/lib/platform/verification-contract'
import { useI18n } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import { GitHubPanel } from './github-panel'

export function ReleasePanel({ onClose }: { readonly onClose: () => void }) {
  const { locale, t } = useI18n()
  const { platformClient, project, can } = useCollaboration()
  const flow = usePlatformFlow()
  const qualityClient = useMemo(() => new QualityClient(platformClient.http), [platformClient.http])
  const deliveryClient = useMemo(() => new DeliveryClient(platformClient.http), [platformClient.http])
  const verificationClient = useMemo(() => new VerificationClient(platformClient.http), [platformClient.http])
  const releaseClient = useMemo(() => new ReleaseClient(platformClient.http), [platformClient.http])
  const [qualityRuns, setQualityRuns] = useState<QualityRunResult[]>([])
  const [deployments, setDeployments] = useState<DeploymentMetadata[]>([])
  const environment: DeliveryEnvironment = 'preview'
  const [canonicalProfiles, setCanonicalProfiles] = useState<readonly VerificationProfileSummaryDto[]>([])
  const [selectedCanonicalProfile, setSelectedCanonicalProfile] = useState<VerificationProfileReferenceDto | null>(null)
  const [canonicalRun, setCanonicalRun] = useState<CanonicalVerificationRunViewDto | null>(null)
  const [releaseBundle, setReleaseBundle] = useState<ReleaseBundleDto | null>(null)
  const [deliveryEnabled, setDeliveryEnabled] = useState(false)
  const [previewRuns, setPreviewRuns] = useState<readonly ReleasePreviewRunDto[]>([])
  const [previewReceipt, setPreviewReceipt] = useState<ReleasePreviewReceiptDto | null>(null)
  const [promotionApproval, setPromotionApproval] = useState<ReleasePromotionApprovalDto | null>(null)
  const [productionRuns, setProductionRuns] = useState<readonly ReleaseProductionRunDto[]>([])
  const [productionReceipt, setProductionReceipt] = useState<ReleaseProductionReceiptDto | null>(null)
  const [productionHistory, setProductionHistory] = useState<readonly ReleaseProductionRunDto[]>([])
  const [reconciliationCases, setReconciliationCases] = useState<readonly ReleaseDeliveryReconciliationCaseDto[]>([])
  const [reconciliationBlocks, setReconciliationBlocks] = useState<Readonly<Record<string, ReleaseDeliveryReconciliationBlockDto>>>({})
  const [reconciliationBlockErrors, setReconciliationBlockErrors] = useState<Readonly<Record<string, string>>>({})
  const [reconciliationReasons, setReconciliationReasons] = useState<Readonly<Record<string, string>>>({})
  const [reconciliationConfirmations, setReconciliationConfirmations] = useState<Readonly<Record<string, boolean>>>({})
  const [busy, setBusy] = useState<'refresh' | 'quality' | 'canonical' | 'bundle' | 'delivery_preview' | 'delivery_approval' | 'delivery_promotion' | 'delivery_rollback' | 'delivery_reconciliation' | 'export' | 'publish' | 'rollback' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const requestId = useRef(0)
  const workspace = flow.workspaceRevision
  const exactWorkspace = workspace
    ? {
        artifactId: workspace.artifactId,
        revisionId: workspace.id,
        revisionNumber: workspace.revisionNumber,
        contentHash: workspace.contentHash,
      }
    : null
  const canonicalSubject = useMemo(() => workspace
    ? ({
        workspaceArtifactId: workspace.artifactId,
        workspaceRevisionId: workspace.id,
        workspaceContentHash: workspace.contentHash,
      })
    : null, [workspace])
  const previewRun = useMemo(
    () => selectReleaseDeliveryRunForDisplay(previewRuns) ?? null,
    [previewRuns],
  )
  const productionRun = useMemo(
    () => selectReleaseDeliveryRunForDisplay(productionRuns) ?? null,
    [productionRuns],
  )
  const displayedBlockedRuns = useMemo(() => {
    const result: Array<{
      readonly runKind: ReleaseDeliveryOperationKind
      readonly run: ReleasePreviewRunDto | ReleaseProductionRunDto
    }> = []
    if (previewRun?.state === 'reconcile_blocked') result.push({ runKind: 'preview', run: previewRun })
    if (productionRun?.state === 'reconcile_blocked') result.push({ runKind: 'production', run: productionRun })
    return result
  }, [previewRun, productionRun])

  const refresh = useCallback(async () => {
    if (!project) {
      setQualityRuns([])
      setDeployments([])
      setCanonicalProfiles([])
      setCanonicalRun(null)
      setReleaseBundle(null)
      setDeliveryEnabled(false)
      setPreviewRuns([])
      setPreviewReceipt(null)
      setPromotionApproval(null)
      setProductionRuns([])
      setProductionReceipt(null)
      setProductionHistory([])
      setReconciliationCases([])
      setReconciliationBlocks({})
      setReconciliationBlockErrors({})
      return
    }
    const current = ++requestId.current
    setBusy('refresh')
    setError(null)
    setReconciliationCases([])
    if (!workspace) {
      setQualityRuns([])
      setDeployments([])
      setCanonicalProfiles([])
      setCanonicalRun(null)
      setReleaseBundle(null)
      setDeliveryEnabled(false)
      setPreviewRuns([])
      setPreviewReceipt(null)
      setPromotionApproval(null)
      setProductionRuns([])
      setProductionReceipt(null)
      setProductionHistory([])
      try {
        const cases = await releaseClient.listDeliveryReconciliationCases(project.id)
        if (current === requestId.current) setReconciliationCases(cases.data)
      } catch (cause) {
        if (current === requestId.current) {
          setError(cause instanceof Error ? cause.message : 'Immutable reconciliation audit could not be loaded.')
        }
      } finally {
        if (current === requestId.current) setBusy(null)
      }
      return
    }
    try {
      const [nextQuality, nextDeployments, profileResult, runResult, capabilities, cases] = await Promise.all([
        qualityClient.list(project.id, { workspaceRevisionId: workspace.id }),
        deliveryClient.list(project.id),
        verificationClient.listCanonicalProfiles(project.id, canonicalSubject!),
        verificationClient.listCanonicalRuns(project.id, canonicalSubject!),
        releaseClient.getCapabilities(project.id),
        releaseClient.listDeliveryReconciliationCases(project.id),
      ])
      if (current !== requestId.current) return
      setQualityRuns(nextQuality)
      setDeployments(nextDeployments)
      setCanonicalProfiles(profileResult.data.profiles)
      setSelectedCanonicalProfile((selected) => selected
        && profileResult.data.profiles.some((item) => sameProfile(item.verificationProfile, selected))
        ? selected
        : profileResult.data.profiles[0]?.verificationProfile ?? null)
      const nextRun = runResult.data.runs[0] ?? null
      setCanonicalRun(nextRun)
      setDeliveryEnabled(capabilities.data.deliveryEnabled)
      setReconciliationCases(cases.data)
      let recoveredBundle: ReleaseBundleDto | null = null
      if (nextRun?.receipt) {
        try {
          const bundle = await releaseClient.getBundleByReceipt(project.id, nextRun.receipt)
          recoveredBundle = bundle.data
          if (current === requestId.current) setReleaseBundle(recoveredBundle)
        } catch (cause) {
          if (!(cause instanceof PlatformHttpError) || cause.status !== 404) throw cause
          if (current === requestId.current) setReleaseBundle(null)
        }
      } else {
        setReleaseBundle(null)
      }
      // Delivery execution may be disabled during controller maintenance, but
      // immutable Runs/Receipts/Revisions remain readable for audit and
      // operator reconciliation. Capability gates mutations, not history.
      if (recoveredBundle) {
        const exactBundle = { id: recoveredBundle.id, contentHash: recoveredBundle.bundleHash }
        const [previews, productions, history] = await Promise.all([
          releaseClient.listPreviewRuns(project.id, exactBundle),
          releaseClient.listProductionRuns(project.id, exactBundle),
          releaseClient.listProductionHistory(project.id),
        ])
        if (current !== requestId.current) return
        const recoveredPreview = selectReleaseDeliveryRunForDisplay(previews.data) ?? null
        setPreviewRuns(previews.data)
        setProductionRuns(mergeDeliveryRuns(history.data, productions.data))
        setProductionHistory(history.data)
        if (recoveredPreview?.receipt) {
          try {
            const approval = await releaseClient.getPromotionApprovalByPreview(project.id, recoveredPreview.receipt)
            if (current === requestId.current) setPromotionApproval(approval.data)
          } catch (cause) {
            if (!(cause instanceof PlatformHttpError) || cause.status !== 404) throw cause
            if (current === requestId.current) setPromotionApproval(null)
          }
        } else {
          setPromotionApproval(null)
        }
      } else {
        setPreviewRuns([])
        setPreviewReceipt(null)
        setPromotionApproval(null)
        setProductionRuns([])
        setProductionReceipt(null)
        setProductionHistory([])
      }
    } catch (cause) {
      if (current === requestId.current) {
        setReconciliationCases([])
        setError(cause instanceof Error ? cause.message : t('release.error.load'))
      }
    } finally {
      if (current === requestId.current) setBusy(null)
    }
  }, [canonicalSubject, deliveryClient, project, qualityClient, releaseClient, t, verificationClient, workspace])

  useEffect(() => {
    void refresh()
    return () => {
      requestId.current += 1
    }
  }, [refresh])

  useEffect(() => {
    if (!project || !canonicalRun || isTerminalVerificationState(canonicalRun.run.state)) return
    const timer = window.setTimeout(() => {
      void verificationClient.getCanonicalRun(project.id, canonicalRun.run.id)
        .then((result) => setCanonicalRun(result.data))
        .catch((cause) => setError(cause instanceof Error ? cause.message : 'Canonical verification refresh failed.'))
    }, 2000)
    return () => window.clearTimeout(timer)
  }, [canonicalRun, project, verificationClient])

  useEffect(() => {
    if (!project) return
    const pendingRuns = previewRuns.filter((run) => !isReleaseDeliveryRunTerminal(run.state))
    if (pendingRuns.length === 0) return
    const timer = window.setTimeout(() => {
      void Promise.all(pendingRuns.map((run) => releaseClient.getPreviewRun(project.id, run.id)))
        .then((results) => setPreviewRuns((current) => mergeDeliveryRuns(results.map((result) => result.data), current)))
        .catch((cause) => setError(cause instanceof Error ? cause.message : 'Preview refresh failed.'))
    }, 2000)
    return () => window.clearTimeout(timer)
  }, [previewRuns, project, releaseClient])

  useEffect(() => {
    if (!project || !previewRun?.receipt) {
      setPreviewReceipt(null)
      return
    }
    let active = true
    void releaseClient.getPreviewReceipt(project.id, previewRun.receipt)
      .then((result) => {
        if (active) setPreviewReceipt(result.data)
      })
      .catch((cause) => {
        if (active) {
          setPreviewReceipt(null)
          setError(cause instanceof Error ? cause.message : 'Preview evidence could not be loaded.')
        }
      })
    return () => {
      active = false
    }
  }, [previewRun?.receipt, project, releaseClient])

  useEffect(() => {
    if (!project) return
    const pendingRuns = productionRuns.filter((run) => !isReleaseDeliveryRunTerminal(run.state))
    if (pendingRuns.length === 0) return
    const timer = window.setTimeout(() => {
      void Promise.all(pendingRuns.map((run) => releaseClient.getProductionRun(project.id, run.id)))
        .then((results) => {
          const refreshedRuns = results.map((result) => result.data)
          setProductionRuns((current) => mergeDeliveryRuns(refreshedRuns, current))
          setProductionHistory((current) => mergeDeliveryRuns(refreshedRuns, current))
        })
        .catch((cause) => setError(cause instanceof Error ? cause.message : 'Production refresh failed.'))
    }, 2000)
    return () => window.clearTimeout(timer)
  }, [productionRuns, project, releaseClient])

  useEffect(() => {
    if (!project || !productionRun?.receipt) {
      setProductionReceipt(null)
      return
    }
    let active = true
    void releaseClient.getProductionReceipt(project.id, productionRun.receipt)
      .then((result) => {
        if (active) setProductionReceipt(result.data)
      })
      .catch((cause) => {
        if (active) {
          setProductionReceipt(null)
          setError(cause instanceof Error ? cause.message : 'Production evidence could not be loaded.')
        }
      })
    return () => {
      active = false
    }
  }, [productionRun?.receipt, project, releaseClient])

  useEffect(() => {
    const unresolved = displayedBlockedRuns.filter(({ runKind, run }) => (
      !selectReleaseDeliveryReconciliationCaseForRun(reconciliationCases, runKind, run.id, run.version)
    ))
    const activeKeys = new Set(unresolved.map(({ runKind, run }) => reconciliationRunKey(runKind, run.id)))
    setReconciliationBlocks((current) => retainRecordKeys(current, activeKeys))
    setReconciliationBlockErrors((current) => retainRecordKeys(current, activeKeys))
    if (!project || !can('admin') || unresolved.length === 0) return

    let active = true
    void Promise.all(unresolved.map(async ({ runKind, run }) => {
      const key = reconciliationRunKey(runKind, run.id)
      try {
        const result = await releaseClient.getBlockedDeliveryReconciliation(project.id, runKind, run.id)
        if (!active) return
        setReconciliationBlocks((current) => ({ ...current, [key]: result.data }))
        setReconciliationBlockErrors((current) => omitRecordKey(current, key))
      } catch (cause) {
        if (!active) return
        setReconciliationBlocks((current) => omitRecordKey(current, key))
        const message = cause instanceof PlatformHttpError && cause.status === 409
          ? 'The exact blocked state changed. Refresh the Run before authorizing reconciliation.'
          : cause instanceof Error
            ? cause.message
            : 'The exact blocked reconciliation snapshot could not be loaded.'
        setReconciliationBlockErrors((current) => ({ ...current, [key]: message }))
      }
    }))
    return () => {
      active = false
    }
  }, [can, displayedBlockedRuns, project, reconciliationCases, releaseClient])

  const latestQuality = qualityRuns[0]
  const latestPassingQuality = selectLatestPassingQualityRun(qualityRuns, exactWorkspace)
  const releaseManifestId = selectReleaseBuildManifestId(
    flow.workbenchQueue,
    flow.bundle,
    flow.proposal,
  )
  const selectedDeployment = deployments.find((item) => item.environment === environment)
  const hasExactStaticReleaseArtifact = Boolean(
    latestPassingQuality?.buildArtifact
    && releaseBundle?.releaseArtifacts.some((artifact) => (
      artifact.kind === 'web-static'
      && artifact.contentHash === latestPassingQuality.buildArtifact?.buildHash
    )),
  )
  const healthyDeploymentRevisions = productionHistory.filter((run) => run.state === 'healthy' && run.revision)
  const allDeliveryRuns = [...previewRuns, ...productionRuns]
  const deliveryMutationLocked = hasReleaseDeliveryMutationLock(allDeliveryRuns)
  const confirmingControllerResult = allDeliveryRuns.some((run) => (
    run.state === 'reconcile_wait' || run.state === 'reconciling'
  ))
  const controllerReconciliationBlocked = allDeliveryRuns.some((run) => run.state === 'reconcile_blocked')

  async function runCanonicalQuality() {
    if (!project || !exactWorkspace || !selectedCanonicalProfile || deliveryMutationLocked) return
    setBusy('canonical')
    setError(null)
    setReleaseBundle(null)
    setPromotionApproval(null)
    try {
      const result = await verificationClient.createCanonicalRun(project.id, {
        workspaceRevision: {
          artifactId: exactWorkspace.artifactId,
          revisionId: exactWorkspace.revisionId,
          contentHash: exactWorkspace.contentHash,
        },
        verificationProfile: selectedCanonicalProfile,
        reason: `Build release authority for WorkspaceRevision ${exactWorkspace.revisionId}.`,
      })
      setCanonicalRun(result.data)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Canonical verification could not be started.')
    } finally {
      setBusy(null)
    }
  }

  async function createReleaseBundle() {
    if (!project || !canonicalRun?.receipt || !canonicalRun.allowedActions.includes('create_release_bundle')) return
    setBusy('bundle')
    setError(null)
    try {
      const result = await releaseClient.createBundle(project.id, canonicalRun.receipt)
      setReleaseBundle(result.data.bundle)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'ReleaseBundle could not be created.')
    } finally {
      setBusy(null)
    }
  }

  async function startReleasePreview() {
    if (!project || !releaseBundle || !deliveryEnabled || deliveryMutationLocked) return
    setBusy('delivery_preview')
    setError(null)
    setPromotionApproval(null)
    setProductionReceipt(null)
    try {
      const result = await releaseClient.startPreview(
        project.id,
        { id: releaseBundle.id, contentHash: releaseBundle.bundleHash },
        `Deploy ReleaseBundle ${releaseBundle.id} to its isolated preview trust domain.`,
      )
      setPreviewRuns((current) => mergeDeliveryRuns([result.data.run], current))
      setPreviewReceipt(null)
    } catch (cause) {
      if (cause instanceof PlatformHttpError && cause.code === 'release_preview_run_conflict') {
        await refresh()
      }
      setError(cause instanceof Error ? cause.message : 'Release preview could not be started.')
    } finally {
      setBusy(null)
    }
  }

  async function approveReleasePromotion() {
    if (!project || !previewRun?.receipt || previewRun.state !== 'passed' || deliveryMutationLocked) return
    setBusy('delivery_approval')
    setError(null)
    try {
      const result = await releaseClient.approvePromotion(
        project.id,
        previewRun.receipt,
        `Approve the exact passed PreviewReceipt ${previewRun.receipt.id}.`,
      )
      setPromotionApproval(result.data.approval)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Production promotion could not be approved.')
    } finally {
      setBusy(null)
    }
  }

  async function startReleasePromotion() {
    if (!project || !promotionApproval || deliveryMutationLocked) return
    setBusy('delivery_promotion')
    setError(null)
    try {
      const result = await releaseClient.startPromotion(
        project.id,
        { id: promotionApproval.id, contentHash: promotionApproval.payloadHash },
        `Promote ReleaseBundle ${promotionApproval.releaseBundle.id} without rebuilding.`,
      )
      setProductionRuns((current) => mergeDeliveryRuns([result.data.run], current))
      setProductionReceipt(null)
      setProductionHistory((current) => mergeDeliveryRuns([result.data.run], current))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Production promotion could not be started.')
    } finally {
      setBusy(null)
    }
  }

  async function startImmutableRollback(sourceRevision: { readonly id: string; readonly contentHash: string }) {
    if (!project || deliveryMutationLocked) return
    setBusy('delivery_rollback')
    setError(null)
    try {
      const result = await releaseClient.startRollback(
        project.id,
        sourceRevision,
        `Rollback by creating a new DeploymentRevision from ${sourceRevision.id}; do not rebuild.`,
      )
      setProductionRuns((current) => mergeDeliveryRuns([result.data.run], current))
      setProductionReceipt(null)
      setProductionHistory((current) => mergeDeliveryRuns([result.data.run], current))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Immutable production rollback could not be started.')
    } finally {
      setBusy(null)
    }
  }

  async function authorizeExactDeliveryReconciliation(
    runKind: ReleaseDeliveryOperationKind,
    run: ReleasePreviewRunDto | ReleaseProductionRunDto,
  ) {
    const key = reconciliationRunKey(runKind, run.id)
    const snapshot = reconciliationBlocks[key]
    const reason = reconciliationReasons[key]?.trim() ?? ''
    if (
      !project
      || !deliveryEnabled
      || !can('admin')
      || run.state !== 'reconcile_blocked'
      || !snapshot
      || snapshot.expectedRunVersion !== run.version
      || !reason
      || reason.length > 1000
      || reconciliationConfirmations[key] !== true
      || selectReleaseDeliveryReconciliationCaseForRun(reconciliationCases, runKind, run.id, run.version) !== undefined
    ) return

    setBusy('delivery_reconciliation')
    setError(null)
    try {
      const result = await releaseClient.resumeBlockedDeliveryReconciliation(project.id, {
        runKind,
        runId: run.id,
        expectedVersion: snapshot.expectedRunVersion,
        expectedErrorCode: snapshot.lastError.code,
        reason,
      })
      setReconciliationCases((current) => mergeReconciliationCases([result.data.case], current))
      setReconciliationBlocks((current) => omitRecordKey(current, key))
      setReconciliationReasons((current) => omitRecordKey(current, key))
      setReconciliationConfirmations((current) => omitRecordKey(current, key))
      await refresh()
    } catch (cause) {
      if (cause instanceof PlatformHttpError && cause.status === 409) {
        setReconciliationBlocks((current) => omitRecordKey(current, key))
        await refresh()
        setError('The exact blocked state changed. Run and audit evidence were refreshed; release controls remain blocked until a fresh server snapshot is authorized.')
      } else {
        setError(cause instanceof Error ? cause.message : 'Exact GET-only reconciliation could not be authorized.')
      }
    } finally {
      setBusy(null)
    }
  }

  async function runQuality() {
    if (!project || !exactWorkspace) return
    setBusy('quality')
    setError(null)
    try {
      const result = await qualityClient.run(project.id, exactWorkspace)
      setQualityRuns((current) => [result, ...current.filter((item) => item.metadata.runId !== result.metadata.runId)])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('release.error.quality'))
    } finally {
      setBusy(null)
    }
  }

  async function exportSource() {
    if (!project || !exactWorkspace) return
    setBusy('export')
    setError(null)
    try {
      const result = await deliveryClient.exportArchive(project.id, {
        kind: 'source',
        revision: exactWorkspace,
        redactSensitive: true,
      })
      downloadBlob(result.blob, result.filename)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('release.error.export'))
    } finally {
      setBusy(null)
    }
  }

  async function publish() {
    if (!project || !exactWorkspace || !latestPassingQuality || !releaseManifestId || !releaseBundle || !hasExactStaticReleaseArtifact || deliveryMutationLocked) return
    setBusy('publish')
    setError(null)
    try {
      const result = await deliveryClient.publish(project.id, {
        deploymentId: selectedDeployment?.deploymentId,
        environment,
        workspaceRevision: exactWorkspace,
        buildManifestId: releaseManifestId,
        qualityRunId: latestPassingQuality.metadata.runId,
        canonicalReceiptId: releaseBundle.canonicalReceipt.id,
        canonicalReceiptHash: releaseBundle.canonicalReceipt.contentHash,
        releaseBundleId: releaseBundle.id,
        releaseBundleHash: releaseBundle.bundleHash,
        environmentRef: `data-runtime:${environment}`,
        message: t('release.publishMessage', { revision: workspace!.revisionNumber }),
      }, { ifMatch: selectedDeployment?.etag })
      setDeployments((current) => [
        result.deployment,
        ...current.filter((item) => item.deploymentId !== result.deployment.deploymentId),
      ])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('release.error.publish'))
    } finally {
      setBusy(null)
    }
  }

  async function rollback(deployment: DeploymentMetadata, versionId: string) {
    if (deliveryMutationLocked) return
    setBusy('rollback')
    setError(null)
    try {
      const next = await deliveryClient.rollback(deployment.deploymentId, versionId, {
        ifMatch: deployment.etag,
        message: t('release.rollbackMessage', { version: versionId }),
      })
      setDeployments((current) => [next, ...current.filter((item) => item.deploymentId !== next.deploymentId)])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('release.error.rollback'))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-black/55 backdrop-blur-sm" role="dialog" aria-modal="true" aria-label={t('release.dialog')}>
      <div className="h-full w-full max-w-2xl overflow-y-auto border-l border-border bg-panel shadow-2xl scrollbar-thin">
        <header className="sticky top-0 z-10 flex h-12 items-center gap-2 border-b border-border bg-panel/95 px-4 backdrop-blur">
          <Rocket className="size-4 text-primary-bright" />
          <div className="min-w-0 flex-1">
            <h2 className="text-xs font-semibold text-foreground">{t('release.center')}</h2>
            <p className="truncate font-mono text-[8px] text-faint-foreground">
              {workspace ? `${workspace.artifactId} · r${formatNumber(workspace.revisionNumber, locale)} · ${workspace.contentHash}` : t('release.noWorkspace')}
            </p>
          </div>
          <button type="button" onClick={() => void refresh()} disabled={busy !== null} className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-35" aria-label={t('release.refresh')}>
            <RefreshCw className={cn('size-3.5', busy === 'refresh' && 'animate-spin')} />
          </button>
          <button type="button" onClick={onClose} className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground" aria-label={t('release.close')}><X className="size-4" /></button>
        </header>

        <div className="space-y-4 p-4">
          {!workspace && <Notice text={t('release.workspaceRequired')} />}
          {error && <div role="alert" className="flex gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-[10px] text-destructive"><CircleAlert className="mt-0.5 size-3.5 shrink-0" /><span className="min-w-0 flex-1">{error}</span><button type="button" onClick={() => setError(null)}><X className="size-3" /></button></div>}

          <section className="rounded-lg border border-primary/30 bg-primary/5 p-3">
            <div className="flex flex-wrap items-center gap-2">
              <ShieldCheck className="size-4 text-primary-bright" />
              <h3 className="text-[11px] font-semibold text-foreground">Canonical release authority</h3>
              {canonicalRun && <span className={cn('rounded px-2 py-0.5 text-[9px] font-medium', canonicalRun.run.state === 'passed' ? 'bg-success/15 text-success' : canonicalRun.run.state === 'failed' || canonicalRun.run.state === 'error' ? 'bg-destructive/15 text-destructive' : 'bg-warning/15 text-warning')}>{canonicalRun.run.state.replaceAll('_', ' ')}</span>}
            </div>
            <p className="mt-1 text-[9px] leading-relaxed text-faint-foreground">Re-runs the approved immutable WorkspaceRevision. Candidate and legacy quality results cannot create a ReleaseBundle.</p>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <select
                value={selectedCanonicalProfile ? profileKey(selectedCanonicalProfile) : ''}
                onChange={(event) => setSelectedCanonicalProfile(canonicalProfiles.find((item) => profileKey(item.verificationProfile) === event.target.value)?.verificationProfile ?? null)}
                disabled={busy !== null || canonicalProfiles.length === 0}
                className="h-8 min-w-52 rounded border border-border bg-panel px-2 text-[10px] text-foreground disabled:opacity-35"
              >
                {canonicalProfiles.length === 0 && <option value="">No qualified release profile</option>}
                {canonicalProfiles.map((item) => <option key={profileKey(item.verificationProfile)} value={profileKey(item.verificationProfile)}>{item.verificationProfile.id} · v{item.verificationProfile.version}</option>)}
              </select>
              <button type="button" onClick={() => void runCanonicalQuality()} disabled={!workspace || !selectedCanonicalProfile || !can('edit') || busy !== null || deliveryMutationLocked} className="inline-flex h-8 items-center gap-1.5 rounded bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-35">
                {busy === 'canonical' || (canonicalRun && !isTerminalVerificationState(canonicalRun.run.state)) ? <LoaderCircle className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />} Run Canonical quality
              </button>
              <button type="button" onClick={() => void createReleaseBundle()} disabled={!canonicalRun?.allowedActions.includes('create_release_bundle') || !canonicalRun.receipt || busy !== null || releaseBundle !== null} className="inline-flex h-8 items-center gap-1.5 rounded border border-primary/40 px-3 text-[10px] font-semibold text-primary-bright disabled:opacity-35">
                {busy === 'bundle' ? <LoaderCircle className="size-3 animate-spin" /> : <Archive className="size-3" />} {releaseBundle ? 'ReleaseBundle ready' : 'Create ReleaseBundle'}
              </button>
            </div>
            {canonicalRun?.blockingReasons.map((reason) => <p key={`${reason.code}-${reason.detail}`} className="mt-2 text-[9px] text-warning">{reason.detail}</p>)}
            {releaseBundle && <div className="mt-3 rounded border border-success/30 bg-success/5 p-2 font-mono text-[9px] text-success"><div>{releaseBundle.id}</div><div className="mt-1 break-all text-faint-foreground">{releaseBundle.bundleHash}</div><div className="mt-1">{releaseBundle.releaseArtifacts.length} immutable artifact(s)</div></div>}
          </section>

          <section className="rounded-lg border border-success/30 bg-success/5 p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Rocket className="size-4 text-success" />
              <h3 className="text-[11px] font-semibold text-foreground">Full-stack Preview and Production</h3>
              {previewRun && <span className="rounded bg-white/5 px-2 py-0.5 text-[9px] text-muted-foreground">preview · {deliveryRunStateLabel(previewRun.state, t)}</span>}
              {productionRun && <span className="rounded bg-white/5 px-2 py-0.5 text-[9px] text-muted-foreground">production · {deliveryRunStateLabel(productionRun.state, t)}</span>}
            </div>
            <p className="mt-1 text-[9px] leading-relaxed text-faint-foreground">
              {deliveryEnabled
                ? 'Deploys the exact ReleaseBundle to an isolated preview, then promotes the same artifact digests after an exact PreviewReceipt approval.'
                : 'A governed Release Controller is not enabled. Preview, approval, promotion, and immutable rollback remain server-blocked.'}
            </p>
            {confirmingControllerResult && (
              <div role="status" aria-live="polite" className="mt-2 flex items-center gap-2 rounded border border-warning/30 bg-warning/10 p-2 text-[10px] font-medium text-warning">
                <LoaderCircle className="size-3.5 shrink-0 animate-spin" />
                {t('release.delivery.reconcilingNotice')}
              </div>
            )}
            {controllerReconciliationBlocked && (
              <div role="alert" className="mt-2 flex items-start gap-2 rounded border border-destructive/30 bg-destructive/10 p-2 text-[10px] font-medium leading-relaxed text-destructive">
                <CircleAlert className="mt-0.5 size-3.5 shrink-0" />
                {t('release.delivery.reconcileBlockedNotice')}
              </div>
            )}
            {displayedBlockedRuns.map(({ runKind, run }) => {
              const key = reconciliationRunKey(runKind, run.id)
              const resolution = selectReleaseDeliveryReconciliationCaseForRun(
                reconciliationCases,
                runKind,
                run.id,
                run.version,
              )
              return (
                <DeliveryReconciliationBlockCard
                  key={key}
                  runKind={runKind}
                  run={run}
                  resolution={resolution}
                  snapshot={reconciliationBlocks[key]}
                  snapshotError={reconciliationBlockErrors[key]}
                  reason={reconciliationReasons[key] ?? ''}
                  confirmed={reconciliationConfirmations[key] === true}
                  canAdmin={can('admin')}
                  deliveryEnabled={deliveryEnabled}
                  busy={busy !== null}
                  authorizing={busy === 'delivery_reconciliation'}
                  onReasonChange={(value) => setReconciliationReasons((current) => ({ ...current, [key]: value }))}
                  onConfirmationChange={(value) => setReconciliationConfirmations((current) => ({ ...current, [key]: value }))}
                  onAuthorize={() => void authorizeExactDeliveryReconciliation(runKind, run)}
                />
              )
            })}
            <div className="mt-2 flex flex-wrap gap-2">
              <button type="button" onClick={() => void startReleasePreview()} disabled={!deliveryEnabled || !releaseBundle || !can('edit') || busy !== null || deliveryMutationLocked} className="inline-flex h-8 items-center gap-1.5 rounded border border-success/40 px-3 text-[10px] font-semibold text-success disabled:opacity-35">
                {busy === 'delivery_preview' || (previewRun && !isReleaseDeliveryRunTerminal(previewRun.state)) ? <LoaderCircle className="size-3 animate-spin" /> : <Rocket className="size-3" />} Start isolated Preview
              </button>
              <button type="button" onClick={() => void approveReleasePromotion()} disabled={!deliveryEnabled || previewRun?.state !== 'passed' || !previewRun.receipt || !can('publish') || busy !== null || promotionApproval !== null || deliveryMutationLocked} className="inline-flex h-8 items-center gap-1.5 rounded border border-border px-3 text-[10px] font-semibold text-foreground disabled:opacity-35">
                {busy === 'delivery_approval' ? <LoaderCircle className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />} {promotionApproval ? 'Promotion approved' : 'Approve exact Preview'}
              </button>
              <button type="button" onClick={() => void startReleasePromotion()} disabled={!deliveryEnabled || !promotionApproval || !can('publish') || busy !== null || deliveryMutationLocked} className="inline-flex h-8 items-center gap-1.5 rounded bg-success px-3 text-[10px] font-semibold text-white disabled:opacity-35">
                {busy === 'delivery_promotion' || (productionRun && !isReleaseDeliveryRunTerminal(productionRun.state)) ? <LoaderCircle className="size-3 animate-spin" /> : <Rocket className="size-3" />} Promote same Bundle
              </button>
            </div>
            {previewRun?.receipt && <p className="mt-2 break-all font-mono text-[9px] text-faint-foreground">PreviewReceipt · {previewRun.receipt.id} · {previewRun.receipt.contentHash}</p>}
            {previewReceipt && (
              <div className="mt-2 space-y-1 rounded border border-border bg-panel p-2">
                <p className="text-[9px] font-medium text-muted-foreground">Preview verification evidence · {previewReceipt.decision}</p>
                {previewReceipt.checks.map((check) => (
                  <div key={check.id} className="flex items-start gap-2 text-[9px]">
                    {check.status === 'passed'
                      ? <CheckCircle2 className="mt-0.5 size-3 shrink-0 text-success" />
                      : <CircleAlert className="mt-0.5 size-3 shrink-0 text-destructive" />}
                    <span className="font-mono text-faint-foreground">{check.id} · {check.kind}</span>
                    {check.detail && <span className="min-w-0 flex-1 break-words text-muted-foreground">{check.detail}</span>}
                  </div>
                ))}
              </div>
            )}
            {productionRun?.receipt && <p className={cn('mt-2 break-all font-mono text-[9px]', productionRun.state === 'healthy' ? 'text-success' : 'text-warning')}>ProductionReceipt · {productionRun.receipt.id} · {productionRun.receipt.contentHash}</p>}
            {productionReceipt && (
              <div className="mt-2 space-y-1 rounded border border-border bg-panel p-2">
                <p className="text-[9px] font-medium text-muted-foreground">Production health evidence · {productionReceipt.decision}</p>
                {productionReceipt.checks.map((check) => (
                  <div key={check.id} className="flex items-start gap-2 text-[9px]">
                    {check.status === 'passed'
                      ? <CheckCircle2 className="mt-0.5 size-3 shrink-0 text-success" />
                      : <CircleAlert className="mt-0.5 size-3 shrink-0 text-destructive" />}
                    <span className="font-mono text-faint-foreground">{check.id} · {check.kind}</span>
                    {check.detail && <span className="min-w-0 flex-1 break-words text-muted-foreground">{check.detail}</span>}
                  </div>
                ))}
              </div>
            )}
            {productionRun?.revision && <p className="mt-2 break-all font-mono text-[9px] text-success">DeploymentRevision · {productionRun.revision.id} · {productionRun.revision.contentHash}</p>}
            {healthyDeploymentRevisions.length > 0 && (
              <div className="mt-3 space-y-1">
                <p className="text-[9px] font-medium text-muted-foreground">Immutable production history</p>
                {healthyDeploymentRevisions.map((run, index) => (
                  <div key={run.id} className="flex items-center gap-2 rounded border border-border bg-panel px-2 py-1.5 text-[9px]">
                    <span className="min-w-0 flex-1 truncate font-mono text-faint-foreground">{run.operation} · {run.revision?.id}</span>
                    {index === 0
                      ? <span className="text-success">active</span>
                      : <button type="button" onClick={() => run.revision && void startImmutableRollback(run.revision)} disabled={!can('publish') || busy !== null || deliveryMutationLocked} className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-1 text-[8px] text-muted-foreground disabled:opacity-35"><RotateCcw className="size-2.5" /> Roll back to this Bundle</button>}
                  </div>
                ))}
              </div>
            )}
            {reconciliationCases.length > 0 && (
              <div className="mt-3 space-y-1.5 rounded border border-border bg-panel p-2">
                <p className="text-[9px] font-medium text-muted-foreground">Immutable delivery reconciliation audit</p>
                {reconciliationCases.map((item) => (
                  <div key={item.id} className="rounded border border-border bg-background/60 p-2 text-[9px]">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="font-medium text-foreground">{item.runKind} · exact GET-only authorization</span>
                      <span className="rounded bg-warning/10 px-1.5 py-0.5 text-warning">{item.resumeRemoteState}</span>
                      <span className="ml-auto text-faint-foreground">Run v{item.expectedRunVersion}</span>
                    </div>
                    <p className="mt-1 break-all font-mono text-faint-foreground">Case {item.id} · {item.caseHash}</p>
                    <p className="mt-1 break-all font-mono text-faint-foreground">Operation {item.operationId} · {item.operationRequestHash}</p>
                    <p className="mt-1 text-destructive">{item.quarantineError.code} · {item.quarantineError.detail}</p>
                    <p className="mt-1 text-muted-foreground">{item.reason} · actor {item.actorId} · {item.createdAt}</p>
                  </div>
                ))}
              </div>
            )}
          </section>

          <section className="rounded-lg border border-border bg-background/45 p-3">
            <div className="flex items-center gap-2">
              <ShieldCheck className="size-4 text-primary-bright" />
              <h3 className="text-[11px] font-semibold text-foreground">{t('release.qualityGate')}</h3>
              {latestQuality && <span className={cn('ml-auto rounded px-2 py-0.5 text-[9px] font-medium', latestQuality.passed ? 'bg-success/15 text-success' : 'bg-destructive/15 text-destructive')}>{latestQuality.passed ? t('workbenchPlatform.status.passed') : t('workbenchPlatform.status.blocked')} · {formatPercentage(latestQuality.score.percentage, locale)}</span>}
            </div>
            <p className="mt-1 text-[9px] leading-relaxed text-faint-foreground">{t('release.qualityDescription')}</p>
            <button type="button" onClick={() => void runQuality()} disabled={!workspace || !can('edit') || busy !== null} className="mt-2 inline-flex h-8 items-center gap-1.5 rounded bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-35">
              {busy === 'quality' ? <LoaderCircle className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />} {t('release.runQuality')}
            </button>
            {latestQuality && (
              <div className="mt-3 grid gap-1.5 sm:grid-cols-2">
                {latestQuality.checks.map((check) => (
                  <div key={check.id} className="flex items-center gap-2 rounded border border-border bg-panel px-2 py-1.5 text-[9px]">
                    {check.status === 'passed' || check.status === 'skipped' ? <CheckCircle2 className="size-3 text-success" /> : <CircleAlert className="size-3 text-destructive" />}
                    <span className="min-w-0 flex-1 truncate text-muted-foreground">{check.title}</span>
                    <span className="text-faint-foreground">{qualityStatusLabel(check.status, t)}</span>
                  </div>
                ))}
              </div>
            )}
          </section>

          <section className="rounded-lg border border-border bg-background/45 p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Archive className="size-4 text-primary-bright" />
              <h3 className="text-[11px] font-semibold text-foreground">{t('release.exportDeploy')}</h3>
              <span className="ml-auto rounded border border-border bg-panel px-2 py-1 text-[9px] text-muted-foreground">Legacy static preview only</span>
            </div>
            <div className="mt-2 flex flex-wrap gap-2">
              <button type="button" onClick={() => void exportSource()} disabled={!workspace || busy !== null} className="inline-flex h-8 items-center gap-1.5 rounded border border-border px-3 text-[10px] text-muted-foreground hover:text-foreground disabled:opacity-35">
                {busy === 'export' ? <LoaderCircle className="size-3 animate-spin" /> : <Archive className="size-3" />} {t('release.exportRedacted')}
              </button>
              <button type="button" onClick={() => void publish()} disabled={!workspace || !latestPassingQuality || !releaseManifestId || !releaseBundle || !hasExactStaticReleaseArtifact || busy !== null || deliveryMutationLocked || !can('edit')} className="inline-flex h-8 items-center gap-1.5 rounded bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-35" title={!releaseBundle ? 'Create the exact ReleaseBundle before publishing.' : !latestPassingQuality ? t('release.passingRequired') : !releaseManifestId ? t('release.manifestRequired') : !hasExactStaticReleaseArtifact ? 'The ReleaseBundle does not contain the exact web-static artifact produced by quality verification.' : deliveryMutationLocked ? t('release.delivery.reconcilingNotice') : undefined}>
                {busy === 'publish' ? <LoaderCircle className="size-3 animate-spin" /> : <Rocket className="size-3" />} {t('release.publishEnvironment', { environment: environmentLabel(environment, t) })}
              </button>
            </div>
            {selectedDeployment && <DeploymentCard deployment={selectedDeployment} busy={busy !== null || deliveryMutationLocked} onRollback={rollback} />}
          </section>

          <section className="rounded-lg border border-border bg-background/45 p-3">
            <div className="mb-3 flex items-center gap-2"><GitBranch className="size-4 text-primary-bright" /><h3 className="text-[11px] font-semibold text-foreground">{t('release.githubDelivery')}</h3></div>
            <GitHubPanel
              projectId={project?.id}
              files={workspace?.content.files ?? []}
            />
          </section>
        </div>
      </div>
    </div>
  )
}

function DeliveryReconciliationBlockCard({
  runKind,
  run,
  resolution,
  snapshot,
  snapshotError,
  reason,
  confirmed,
  canAdmin,
  deliveryEnabled,
  busy,
  authorizing,
  onReasonChange,
  onConfirmationChange,
  onAuthorize,
}: {
  readonly runKind: ReleaseDeliveryOperationKind
  readonly run: ReleasePreviewRunDto | ReleaseProductionRunDto
  readonly resolution?: ReleaseDeliveryReconciliationCaseDto
  readonly snapshot?: ReleaseDeliveryReconciliationBlockDto
  readonly snapshotError?: string
  readonly reason: string
  readonly confirmed: boolean
  readonly canAdmin: boolean
  readonly deliveryEnabled: boolean
  readonly busy: boolean
  readonly authorizing: boolean
  readonly onReasonChange: (value: string) => void
  readonly onConfirmationChange: (value: boolean) => void
  readonly onAuthorize: () => void
}) {
  const snapshotMatchesRun = snapshot?.runKind === runKind
    && snapshot.runId === run.id
    && snapshot.expectedRunVersion === run.version
  const validReason = reason.trim().length > 0 && reason.trim().length <= 1000

  return (
    <div className="mt-2 rounded border border-destructive/35 bg-background/70 p-2.5 text-[9px]">
      <div className="flex flex-wrap items-center gap-1.5">
        <CircleAlert className="size-3.5 text-destructive" />
        <span className="font-semibold text-foreground">{runKind} Run is quarantined</span>
        <span className="rounded bg-destructive/10 px-1.5 py-0.5 text-destructive">reconcile_blocked</span>
        <span className="ml-auto font-mono text-faint-foreground">v{run.version} · {run.id}</span>
      </div>

      {resolution ? (
        <div className="mt-2 space-y-1 rounded border border-warning/30 bg-warning/5 p-2">
          <p className="font-medium text-warning">Immutable exact GET-only authorization already recorded</p>
          <p className="break-all font-mono text-faint-foreground">Case {resolution.id} · {resolution.caseHash}</p>
          <p className="break-all font-mono text-faint-foreground">Operation {resolution.operationId} · {resolution.operationRequestHash}</p>
          <p className="text-destructive">{resolution.quarantineError.code} · {resolution.quarantineError.detail}</p>
          <p className="text-muted-foreground">Pinned controller {resolution.controller.id} · {resolution.controller.version} · {resolution.controller.trustKeyDigest}</p>
          <p className="text-muted-foreground">{resolution.reason} · actor {resolution.actorId} · {resolution.createdAt}</p>
          <p className="font-medium text-warning">This authorization never asserts a controller result or production head. The same blocked Run version cannot be authorized twice; a later block at a new version requires a fresh snapshot and a new immutable Case.</p>
        </div>
      ) : (
        <div className="mt-2 space-y-2">
          {!canAdmin && (
            <p className="text-warning">Administrator access is required to read the exact quarantine snapshot and authorize reconciliation.</p>
          )}
          {canAdmin && snapshotError && <p role="alert" className="text-destructive">{snapshotError}</p>}
          {canAdmin && !snapshot && !snapshotError && (
            <p className="inline-flex items-center gap-1.5 text-warning"><LoaderCircle className="size-3 animate-spin" />Loading exact server quarantine evidence…</p>
          )}
          {canAdmin && snapshot && (
            <>
              <div className="space-y-1 rounded border border-border bg-panel p-2">
                <p className="font-medium text-foreground">Exact blocked snapshot · Run v{snapshot.expectedRunVersion}</p>
                <p className="break-all font-mono text-faint-foreground">Operation {snapshot.operationId} · {snapshot.operationRequestHash}</p>
                <p className="text-destructive">{snapshot.lastError.code} · {snapshot.lastError.detail}</p>
                <p className="break-all text-muted-foreground">Pinned controller {snapshot.controller.id} · {snapshot.controller.version} · {snapshot.controller.trustKeyDigest}</p>
              </div>
              {!snapshotMatchesRun && (
                <p role="alert" className="font-medium text-destructive">The snapshot no longer matches the displayed Run version. Refresh before authorizing.</p>
              )}
              <label className="block text-muted-foreground">
                Required operator reason
                <textarea
                  value={reason}
                  onChange={(event) => onReasonChange(event.target.value)}
                  maxLength={1000}
                  disabled={busy}
                  placeholder="Record how the pinned controller repair was independently verified."
                  className="mt-1 min-h-16 w-full resize-y rounded border border-border bg-panel p-2 text-[10px] text-foreground outline-none focus:border-primary disabled:opacity-40"
                />
              </label>
              <label className="flex items-start gap-2 text-muted-foreground">
                <input
                  type="checkbox"
                  checked={confirmed}
                  onChange={(event) => onConfirmationChange(event.target.checked)}
                  disabled={busy}
                  className="mt-0.5"
                />
                <span>I confirm the pinned controller was repaired and authorize GET-only reconciliation for this exact Operation. This does not submit work, fabricate a result, or change the production head.</span>
              </label>
              {!deliveryEnabled && (
                <p className="text-warning">Controller mutation authority remains disabled. Historical evidence stays readable, but authorization is unavailable until the repaired controller is enabled.</p>
              )}
              <button
                type="button"
                onClick={onAuthorize}
                disabled={!deliveryEnabled || busy || !snapshotMatchesRun || !validReason || !confirmed}
                className="inline-flex h-8 items-center gap-1.5 rounded border border-warning/40 px-3 text-[10px] font-semibold text-warning disabled:opacity-35"
              >
                {authorizing ? <LoaderCircle className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />}
                Authorize exact GET-only reconciliation
              </button>
            </>
          )}
        </div>
      )}
    </div>
  )
}

function DeploymentCard({ deployment, busy, onRollback }: { readonly deployment: DeploymentMetadata; readonly busy: boolean; readonly onRollback: (deployment: DeploymentMetadata, versionId: string) => Promise<void> }) {
  const { locale, t } = useI18n()
  return (
    <div className="mt-3 rounded border border-border bg-panel p-2.5">
      <div className="flex items-center gap-2 text-[10px]">
        <span className="font-medium text-foreground">{environmentLabel(deployment.environment, t)}</span>
        <span className="rounded bg-white/5 px-1.5 py-0.5 text-[8px] text-muted-foreground">{deliveryStatusLabel(deployment.status, t)}</span>
        {deployment.publicPath && <a href={deployment.publicPath} target="_blank" rel="noopener noreferrer" className="ml-auto inline-flex items-center gap-1 text-primary-bright hover:underline">{t('release.openDeployment')} <ExternalLink className="size-3" /></a>}
      </div>
      <div className="mt-2 max-h-36 space-y-1 overflow-y-auto scrollbar-thin">
        {deployment.versions.map((version) => (
          <div key={version.id} className="flex items-center gap-2 rounded bg-background px-2 py-1.5 text-[9px]">
            <span className="font-mono text-faint-foreground">v{formatNumber(version.number, locale)}</span>
            <span className="min-w-0 flex-1 truncate text-muted-foreground">{deliveryActionLabel(version.action, t)} · {deliveryStatusLabel(version.status, t)} · {version.checksum || t('release.checksumPending')}</span>
            {version.status === 'ready' && version.id !== deployment.activeVersionId && (
              <button type="button" onClick={() => void onRollback(deployment, version.id)} disabled={busy} className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-1 text-[8px] text-muted-foreground hover:text-foreground disabled:opacity-35"><RotateCcw className="size-2.5" /> {t('release.rollback')}</button>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

function Notice({ text }: { readonly text: string }) {
  return <div className="flex gap-2 rounded-lg border border-warning/30 bg-warning/10 p-3 text-[10px] leading-relaxed text-warning"><CircleAlert className="mt-0.5 size-3.5 shrink-0" />{text}</div>
}

function profileKey(profile: VerificationProfileReferenceDto) {
  return `${profile.id}:${profile.version}:${profile.contentHash}`
}

function sameProfile(left: VerificationProfileReferenceDto, right: VerificationProfileReferenceDto) {
  return profileKey(left) === profileKey(right)
}

function mergeDeliveryRuns<T extends { readonly id: string }>(
  preferred: readonly T[],
  existing: readonly T[],
): readonly T[] {
  const preferredIds = new Set(preferred.map((run) => run.id))
  return [...preferred, ...existing.filter((run) => !preferredIds.has(run.id))]
}

function reconciliationRunKey(runKind: ReleaseDeliveryOperationKind, runId: string) {
  return `${runKind}:${runId}`
}

function omitRecordKey<T>(source: Readonly<Record<string, T>>, key: string): Readonly<Record<string, T>> {
  if (!Object.hasOwn(source, key)) return source
  const next = { ...source }
  delete next[key]
  return next
}

function retainRecordKeys<T>(
  source: Readonly<Record<string, T>>,
  keys: ReadonlySet<string>,
): Readonly<Record<string, T>> {
  const entries = Object.entries(source).filter(([key]) => keys.has(key))
  if (entries.length === Object.keys(source).length) return source
  return Object.fromEntries(entries) as Readonly<Record<string, T>>
}

function mergeReconciliationCases(
  preferred: readonly ReleaseDeliveryReconciliationCaseDto[],
  existing: readonly ReleaseDeliveryReconciliationCaseDto[],
) {
  const preferredIds = new Set(preferred.map((item) => item.id))
  return [...preferred, ...existing.filter((item) => !preferredIds.has(item.id))]
}

function isTerminalVerificationState(state: CanonicalVerificationRunViewDto['run']['state']) {
  return state === 'passed'
    || state === 'failed'
    || state === 'error'
    || state === 'cancelled'
    || state === 'timed_out'
}

function deliveryRunStateLabel(state: ReleaseDeliveryRunState, t: ReturnType<typeof useI18n>['t']) {
  switch (state) {
    case 'queued': return t('release.delivery.state.queued')
    case 'claimed': return t('release.delivery.state.claimed')
    case 'submitting': return t('release.delivery.state.submitting')
    case 'reconcile_wait': return t('release.delivery.state.reconcileWait')
    case 'reconciling': return t('release.delivery.state.reconciling')
    case 'deploying': return t('release.delivery.state.deploying')
    case 'verifying': return t('release.delivery.state.verifying')
    case 'reconcile_blocked': return t('release.delivery.state.reconcileBlocked')
    case 'passed': return t('release.delivery.state.passed')
    case 'healthy': return t('release.delivery.state.healthy')
    case 'failed': return t('release.delivery.state.failed')
    case 'error': return t('release.delivery.state.error')
    case 'cancelled': return t('release.delivery.state.cancelled')
  }
}

function environmentLabel(environment: DeliveryEnvironment, t: ReturnType<typeof useI18n>['t']) {
  return environment === 'production'
    ? t('workbenchPlatform.environment.production')
    : t('workbenchPlatform.environment.preview')
}

function qualityStatusLabel(status: QualityRunResult['checks'][number]['status'], t: ReturnType<typeof useI18n>['t']) {
  if (status === 'passed') return t('workbenchPlatform.status.passed')
  if (status === 'skipped') return t('workbenchPlatform.status.skipped')
  if (status === 'warning') return t('workbenchPlatform.status.warning')
  return t('workbenchPlatform.status.failed')
}

function deliveryActionLabel(action: 'publish' | 'rollback', t: ReturnType<typeof useI18n>['t']) {
  return action === 'publish'
    ? t('workbenchPlatform.status.publish')
    : t('workbenchPlatform.status.rollback')
}

function deliveryStatusLabel(status: string, t: ReturnType<typeof useI18n>['t']) {
  if (status === 'ready') return t('workbenchPlatform.status.ready')
  if (status === 'pending') return t('workbenchPlatform.status.pending')
  if (status === 'failed') return t('workbenchPlatform.status.failed')
  return status.replaceAll('_', ' ')
}

function formatNumber(value: number, locale: string) {
  return new Intl.NumberFormat(locale).format(value)
}

function formatPercentage(value: number, locale: string) {
  return new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 2,
  }).format(value / 100)
}
