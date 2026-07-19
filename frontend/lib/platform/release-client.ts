import type { ClientMutationOptions, ClientRequestOptions } from './clients'
import { HttpClient, PlatformProtocolError, type HttpResult } from './http'
import {
  normalizeReleaseBundle,
  normalizeReleaseBundleView,
  normalizeReleaseCapabilities,
  normalizeReleaseDeploymentRevision,
  normalizeReleaseDeliveryReconciliationBlock,
  normalizeReleaseDeliveryReconciliationCase,
  normalizeReleaseDeliveryReconciliationCaseList,
  normalizeReleaseDeliveryReconciliationCaseView,
  normalizeReleasePreviewRun,
  normalizeReleasePreviewReceipt,
  normalizeReleasePreviewRunList,
  normalizeReleasePreviewRunView,
  normalizeReleaseProductionRun,
  normalizeReleaseProductionReceipt,
  normalizeReleaseProductionRunList,
  normalizeReleaseProductionRunView,
  normalizeReleasePromotionApproval,
  normalizeReleasePromotionApprovalView,
  ReleaseContractError,
  type ReleaseBundleDto,
  type ReleaseBundleViewDto,
  type ReleaseCapabilitiesDto,
  type ReleaseDeploymentRevisionDto,
  type ReleaseDeliveryOperationKind,
  type ReleaseDeliveryReconciliationBlockDto,
  type ReleaseDeliveryReconciliationCaseDto,
  type ReleaseDeliveryReconciliationCaseViewDto,
  type ReleasePreviewRunDto,
  type ReleasePreviewReceiptDto,
  type ReleasePreviewRunViewDto,
  type ReleaseProductionRunDto,
  type ReleaseProductionReceiptDto,
  type ReleaseProductionRunViewDto,
  type ReleasePromotionApprovalDto,
  type ReleasePromotionApprovalViewDto,
  type ResumeBlockedReleaseDeliveryInput,
} from './release-contract'

function segment(value: string) {
  return encodeURIComponent(value)
}

function strictReleaseResult<T>(
  result: HttpResult<unknown>,
  normalize: (value: unknown) => T,
): HttpResult<T> {
  try {
    return { ...result, data: normalize(result.data) }
  } catch (cause) {
    if (!(cause instanceof ReleaseContractError)) throw cause
    throw new PlatformProtocolError(cause.message, result.requestId, result.status)
  }
}

function invalidImmutableReleaseIdentity(result: HttpResult<unknown>, label: string): never {
  throw new PlatformProtocolError(
    `The release service returned ${label} for a different exact identity.`,
    result.requestId,
    result.status,
  )
}

function sameExactReleaseReference(
  left: { readonly id: string; readonly contentHash: string },
  right: { readonly id: string; readonly contentHash: string },
): boolean {
  return left.id === right.id && left.contentHash === right.contentHash
}

function invalidReconciliationIdentity(result: HttpResult<unknown>): never {
  throw new PlatformProtocolError(
    'The release service returned reconciliation evidence for a different exact identity.',
    result.requestId,
    result.status,
  )
}

export class ReleaseClient {
  constructor(private readonly http: HttpClient) {}

  async createBundle(
    projectId: string,
    canonicalReceipt: { readonly id: string; readonly contentHash: string },
    options?: ClientMutationOptions,
  ): Promise<HttpResult<ReleaseBundleViewDto>> {
    const result = await this.http.post<unknown, { readonly canonicalReceipt: typeof canonicalReceipt }>(
      `/v1/projects/${segment(projectId)}/release-bundles`,
      { canonicalReceipt },
      {
        signal: options?.signal,
        requestId: options?.requestId,
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseBundleView)
    if (normalized.data.bundle.projectId !== projectId ||
      !sameExactReleaseReference(normalized.data.bundle.canonicalReceipt, canonicalReceipt)) {
      invalidImmutableReleaseIdentity(result, 'ReleaseBundle')
    }
    return normalized
  }

  async getBundle(
    projectId: string,
    bundleId: string,
    bundleHash: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleaseBundleDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-bundles/${segment(bundleId)}`,
      { signal: options?.signal, requestId: options?.requestId, query: { bundleHash } },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseBundle)
    if (normalized.data.projectId !== projectId || normalized.data.id !== bundleId ||
      normalized.data.bundleHash !== bundleHash) {
      invalidImmutableReleaseIdentity(result, 'ReleaseBundle')
    }
    return normalized
  }

  async getBundleByReceipt(
    projectId: string,
    receipt: { readonly id: string; readonly contentHash: string },
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleaseBundleDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-bundles/by-receipt`,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        query: { receiptId: receipt.id, receiptHash: receipt.contentHash },
      },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseBundle)
    if (normalized.data.projectId !== projectId ||
      !sameExactReleaseReference(normalized.data.canonicalReceipt, receipt)) {
      invalidImmutableReleaseIdentity(result, 'ReleaseBundle')
    }
    return normalized
  }

  async getCapabilities(projectId: string, options?: ClientRequestOptions): Promise<HttpResult<ReleaseCapabilitiesDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-capabilities`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    return { ...result, data: normalizeReleaseCapabilities(result.data) }
  }

  async startPreview(
    projectId: string,
    releaseBundle: { readonly id: string; readonly contentHash: string },
    reason: string,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<ReleasePreviewRunViewDto>> {
    const result = await this.http.post<unknown, { readonly releaseBundle: typeof releaseBundle; readonly reason: string }>(
      `/v1/projects/${segment(projectId)}/release-preview-runs`,
      { releaseBundle, reason },
      { signal: options?.signal, requestId: options?.requestId, idempotencyKey: options?.idempotencyKey ?? true },
    )
    return { ...result, data: normalizeReleasePreviewRunView(result.data) }
  }

  async listPreviewRuns(
    projectId: string,
    releaseBundle: { readonly id: string; readonly contentHash: string },
    options?: ClientRequestOptions,
  ): Promise<HttpResult<readonly ReleasePreviewRunDto[]>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-preview-runs`,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        query: { bundleId: releaseBundle.id, bundleHash: releaseBundle.contentHash },
      },
    )
    return { ...result, data: normalizeReleasePreviewRunList(result.data) }
  }

  async getPreviewRun(
    projectId: string,
    runId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleasePreviewRunDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-preview-runs/${segment(runId)}`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    return { ...result, data: normalizeReleasePreviewRun(result.data) }
  }

  async getPreviewReceipt(
    projectId: string,
    receipt: { readonly id: string; readonly contentHash: string },
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleasePreviewReceiptDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-preview-receipts/${segment(receipt.id)}`,
      { signal: options?.signal, requestId: options?.requestId, query: { receiptHash: receipt.contentHash } },
    )
    const normalized = strictReleaseResult(result, normalizeReleasePreviewReceipt)
    if (normalized.data.projectId !== projectId || normalized.data.id !== receipt.id ||
      normalized.data.payloadHash !== receipt.contentHash) {
      invalidImmutableReleaseIdentity(result, 'PreviewReceipt')
    }
    return normalized
  }

  async approvePromotion(
    projectId: string,
    previewReceipt: { readonly id: string; readonly contentHash: string },
    reason: string,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<ReleasePromotionApprovalViewDto>> {
    const result = await this.http.post<unknown, { readonly previewReceipt: typeof previewReceipt; readonly reason: string }>(
      `/v1/projects/${segment(projectId)}/release-promotion-approvals`,
      { previewReceipt, reason },
      { signal: options?.signal, requestId: options?.requestId, idempotencyKey: options?.idempotencyKey ?? true },
    )
    const normalized = strictReleaseResult(result, normalizeReleasePromotionApprovalView)
    if (normalized.data.approval.projectId !== projectId ||
      !sameExactReleaseReference(normalized.data.approval.previewReceipt, previewReceipt) ||
      normalized.data.approval.reason !== reason.trim()) {
      invalidImmutableReleaseIdentity(result, 'PromotionApproval')
    }
    return normalized
  }

  async getPromotionApprovalByPreview(
    projectId: string,
    previewReceipt: { readonly id: string; readonly contentHash: string },
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleasePromotionApprovalDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-promotion-approvals/by-preview`,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        query: { previewId: previewReceipt.id, previewHash: previewReceipt.contentHash },
      },
    )
    const normalized = strictReleaseResult(result, normalizeReleasePromotionApproval)
    if (normalized.data.projectId !== projectId ||
      !sameExactReleaseReference(normalized.data.previewReceipt, previewReceipt)) {
      invalidImmutableReleaseIdentity(result, 'PromotionApproval')
    }
    return normalized
  }

  async startPromotion(
    projectId: string,
    promotionApproval: { readonly id: string; readonly contentHash: string },
    reason: string,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<ReleaseProductionRunViewDto>> {
    const result = await this.http.post<unknown, { readonly promotionApproval: typeof promotionApproval; readonly reason: string }>(
      `/v1/projects/${segment(projectId)}/release-deployment-runs/promote`,
      { promotionApproval, reason },
      { signal: options?.signal, requestId: options?.requestId, idempotencyKey: options?.idempotencyKey ?? true },
    )
    return { ...result, data: normalizeReleaseProductionRunView(result.data) }
  }

  async startRollback(
    projectId: string,
    sourceRevision: { readonly id: string; readonly contentHash: string },
    reason: string,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<ReleaseProductionRunViewDto>> {
    const result = await this.http.post<unknown, { readonly sourceRevision: typeof sourceRevision; readonly reason: string }>(
      `/v1/projects/${segment(projectId)}/release-deployment-runs/rollback`,
      { sourceRevision, reason },
      { signal: options?.signal, requestId: options?.requestId, idempotencyKey: options?.idempotencyKey ?? true },
    )
    return { ...result, data: normalizeReleaseProductionRunView(result.data) }
  }

  async listProductionRuns(
    projectId: string,
    releaseBundle: { readonly id: string; readonly contentHash: string },
    options?: ClientRequestOptions,
  ): Promise<HttpResult<readonly ReleaseProductionRunDto[]>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-deployment-runs`,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        query: { bundleId: releaseBundle.id, bundleHash: releaseBundle.contentHash },
      },
    )
    return { ...result, data: normalizeReleaseProductionRunList(result.data) }
  }

  async listProductionHistory(
    projectId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<readonly ReleaseProductionRunDto[]>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-deployment-runs`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    return { ...result, data: normalizeReleaseProductionRunList(result.data) }
  }

  async getProductionRun(
    projectId: string,
    runId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleaseProductionRunDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-deployment-runs/${segment(runId)}`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    return { ...result, data: normalizeReleaseProductionRun(result.data) }
  }

  async getProductionReceipt(
    projectId: string,
    receipt: { readonly id: string; readonly contentHash: string },
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleaseProductionReceiptDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-production-receipts/${segment(receipt.id)}`,
      { signal: options?.signal, requestId: options?.requestId, query: { receiptHash: receipt.contentHash } },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseProductionReceipt)
    if (normalized.data.projectId !== projectId || normalized.data.id !== receipt.id ||
      normalized.data.payloadHash !== receipt.contentHash) {
      invalidImmutableReleaseIdentity(result, 'ProductionReceipt')
    }
    return normalized
  }

  async getDeploymentRevision(
    projectId: string,
    revision: { readonly id: string; readonly contentHash: string },
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleaseDeploymentRevisionDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-deployment-revisions/${segment(revision.id)}`,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        query: { revisionHash: revision.contentHash },
      },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseDeploymentRevision)
    if (normalized.data.projectId !== projectId || normalized.data.id !== revision.id ||
      normalized.data.payloadHash !== revision.contentHash) {
      invalidImmutableReleaseIdentity(result, 'DeploymentRevision')
    }
    return normalized
  }

  async listDeliveryReconciliationCases(
    projectId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<readonly ReleaseDeliveryReconciliationCaseDto[]>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-delivery-reconciliation-cases`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseDeliveryReconciliationCaseList)
    if (normalized.data.some((item) => item.projectId !== projectId)) {
      invalidReconciliationIdentity(result)
    }
    return normalized
  }

  async getDeliveryReconciliationCase(
    projectId: string,
    caseId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleaseDeliveryReconciliationCaseDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-delivery-reconciliation-cases/${segment(caseId)}`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseDeliveryReconciliationCase)
    if (normalized.data.projectId !== projectId || normalized.data.id !== caseId) {
      invalidReconciliationIdentity(result)
    }
    return normalized
  }

  async getBlockedDeliveryReconciliation(
    projectId: string,
    runKind: ReleaseDeliveryOperationKind,
    runId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ReleaseDeliveryReconciliationBlockDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/release-delivery-reconciliation-blocks/${segment(runKind)}/${segment(runId)}`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseDeliveryReconciliationBlock)
    if (
      normalized.data.projectId !== projectId
      || normalized.data.runKind !== runKind
      || normalized.data.runId !== runId
    ) {
      invalidReconciliationIdentity(result)
    }
    return normalized
  }

  async resumeBlockedDeliveryReconciliation(
    projectId: string,
    input: ResumeBlockedReleaseDeliveryInput,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<ReleaseDeliveryReconciliationCaseViewDto>> {
    const result = await this.http.post<unknown, ResumeBlockedReleaseDeliveryInput>(
      `/v1/projects/${segment(projectId)}/release-delivery-reconciliation-cases`,
      input,
      {
        signal: options?.signal,
        requestId: options?.requestId,
        // Every explicit operator authorization is a distinct immutable Case.
        // A caller may provide a stable key only to replay that same request.
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    const normalized = strictReleaseResult(result, normalizeReleaseDeliveryReconciliationCaseView)
    const resolution = normalized.data.case
    if (
      resolution.projectId !== projectId
      || resolution.runKind !== input.runKind
      || resolution.runId !== input.runId
      || resolution.expectedRunVersion !== input.expectedVersion
      || resolution.quarantineError.code !== input.expectedErrorCode
      || resolution.reason !== input.reason.trim()
    ) {
      invalidReconciliationIdentity(result)
    }
    return normalized
  }
}
