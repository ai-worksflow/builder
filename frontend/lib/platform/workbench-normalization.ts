function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function arrayValue(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : []
}

/**
 * Presents persisted Workbench bundles through the non-null collection
 * contract used by the UI. Historical immutable manifests may contain JSON
 * null for Go nil slices; this compatibility view must never be persisted back
 * over the frozen payload.
 */
export function normalizeWorkbenchBundle<T>(bundle: T): T {
  const raw = objectValue(bundle)
  if (!raw) return bundle

  const workflowContext = objectValue(raw.workflowContext)
  const inputManifest = objectValue(workflowContext?.inputManifest)
  const normalizedWorkflowContext = workflowContext && inputManifest
    ? {
        ...workflowContext,
        inputManifest: {
          ...inputManifest,
          sources: arrayValue(inputManifest.sources),
        },
      }
    : undefined

  return {
    ...raw,
    requirementRevisions: arrayValue(raw.requirementRevisions),
    contractRevisions: arrayValue(raw.contractRevisions),
    designSystemRevisions: arrayValue(raw.designSystemRevisions),
    contextRevisions: arrayValue(raw.contextRevisions),
    renderedFrames: arrayValue(raw.renderedFrames),
    assumptions: arrayValue(raw.assumptions),
    waivers: arrayValue(raw.waivers),
    ...(raw.workflowContext === undefined
      ? {}
      : { workflowContext: normalizedWorkflowContext }),
  } as T
}

/** Normalizes both the active bundle and lineage collection from one response. */
export function normalizeWorkbenchLineageState<T>(state: T): T {
  const raw = objectValue(state)
  if (!raw) return state
  return {
    ...raw,
    activeBundle: normalizeWorkbenchBundle(raw.activeBundle),
    lineage: arrayValue(raw.lineage),
  } as T
}
