# Workflow I/O contracts and executable capabilities

Every newly authored workflow version freezes an `inputContract` and an
`outputContract` into its immutable definition hash. The input contract binds a
stable capability to exact manifest job/schema pairs, source purposes, artifact
kinds, minimum/maximum input cardinality, and approval policy. Project Brief
starts are exactly one artifact; Blueprint-selection starts are one Blueprint
root plus 1-100 selected node anchors (2-101 exact input refs). The output contract binds the
desired capability to produced artifact kinds and the executable terminal
outcome/node type.

The Go service is authoritative for authoring capabilities. Clients load
`GET /v1/projects/:projectId/workflow-capabilities`; they must use its registered
AI job/schema/model-policy signatures, fan-out resolvers, manifest compiler
kind/schema/hook signatures, Workbench schema, blocking release gate, and
publish environments. Unknown values are rejected again on draft creation,
publication, discovery, and before a run is persisted.

Current-profile edge validation proves schema implication rather than merely
comparing top-level types: every value admitted by an output port, after the
declared additive field mapping, must be admitted by the target input port.
Nested required/type constraints, finite values, homogeneous array items,
bounds, and additional-property policy are checked recursively. Unsupported
or non-provable target assertions fail closed. Runtime input validation remains
the second boundary; legacy definitions keep their frozen pre-pin authoring
policy for replay only.

Current-profile Conditions evaluate only immutable `/inputs`, `/scope`, `/run`
and current `/slice` data. Paths rooted at global `/nodes`, `/values` or
`/slices` are rejected during authoring and again at execution, so parallel
completion order cannot change a branch. The legacy profile retains its
separate evaluator for already-pinned runs.

The current authoring registry is `version: 4` and exposes immutable
`analysisLimits`, including `maxSemanticPathStates: 256`. Registry version
alone is not an execution identity. Every new definition also freezes a
`WorkflowExecutionProfileRef {version, hash}` into its definition hash; the
profile descriptor hash covers the complete capability/analysis-limit snapshot
and explicit IDs for the core interpreter, input builder, result validator and
apply logic, reconciliation, runner/manifest-compiler dispatch, and
condition/proposal analysis.

The exact current ref is `workflow-engine/v2` with descriptor hash
`dd247a77ce3cfa1095a575a238b93c4bd41dd991eac07e8b62ec170864470da1`.
It differs from frozen `workflow-engine/v1` only at the reconciliation component:
`typed-dag-reconcile/v2` cancels the paired zero-slice Merge of a
Condition-disabled FanOut after proving the FanOut has no effective predecessor.
That permits the selected alternative to enter a shared tail. Any valid but
unfinished FanOut predecessor keeps the Merge pending. The v1 ref
`648034d2edc8f82ac2b2959b89e181b8b67db80dadbfcd354672f386d81cbdc1`
continues to dispatch its frozen `typed-dag-reconcile/v1` entry point.

Built-in template versions make this boundary observable: `minimum-product-loop`
v4 and `blueprint-selection-app` v3 remain pinned to v1, while current
`minimum-product-loop` v5 and `blueprint-selection-app` v4 are pinned to v2.

Migration 016 binds all pre-pin definition rows and runs to the frozen
`legacy-pre-pin/v0` descriptor without rewriting their JSON or content hashes.
New publication, discovery, and Start accept only the current exact profile.
Existing runs remain executable only while their exact legacy/current bundle is
registered; there is no same-version or "use latest" fallback. Workers filter
unsupported profiles before acquiring a lease, and readiness fails with an
observable diagnostic if any active nonterminal run has no bundle in the local
process. This permits legacy/current workers to coexist for already-pinned
runs while preventing permanently ready, unclaimable work. Migration 016 also
keeps the database write protocol in an expand-compatible state: a pre-016
writer that omits the new columns can create only a profile-less legacy
definition and a run of that legacy definition; the composite foreign key
rejects it if it attempts to start a current-profile definition. Operationally,
current-profile publication/provisioning therefore follows the contract phase,
after pre-016 HTTP writers have drained; it is never made backward-readable by
silently stripping the new execution identity.

Every newly governed fan-out pins `maxItems`; the registered Blueprint page
and Blueprint-selection page resolvers both cap it at 100. Historical
definitions that omitted the field replay with the same 100-item safety
default. Canonical Blueprint approval, resolver output, and the engine's final
instantiation boundary enforce the limit independently, so `maxParallel`
cannot be mistaken for a total expansion bound.

Application definitions are semantically checked on every DAG path. Project
Brief workflows must establish approved requirements, Blueprint, PageSpec, and
Prototype lineage. Blueprint-selection workflows must use the frozen selection
fan-out and `selection_passthrough`. Complete approved slices must then pass
through the exact sequence `manifest_compiler -> workbench_build -> blocking
release quality_gate -> publish`; bypasses, ambiguous compiler inputs, and
unregistered context kinds fail closed. Generic and `delivery_slice` fan-out
remain replayable in historical definitions but are not offered for new
contracted application workflows.

Application merge and semantic review are non-waivable: merge requires
`policy=all`, approval gates prohibit self-review, and the blocking release
quality gate cannot be waived. A fan-out/merge pair must contain a real branch
region. Each `blueprint_page` fan-out opens a new slice epoch; PageSpec and
Prototype must both be produced, edited, and approved inside that same epoch.
Global artifacts from an earlier merge cannot be rebound by a later empty or
control-only fan-out.

Semantic state is version-sensitive: generating or editing Project Brief,
requirements, Blueprint, PageSpec, or Prototype deterministically invalidates
every dependent downstream kind and any merged slice snapshot. Prototype is a
leaf artifact kind, but changing it still invalidates the merge epoch because
the exact Prototype revision is part of each slice. Compilation is blocked
while a review is pending or until all invalidated downstream stages have been
regenerated and approved from the current upstream lineage. Selection input is
the explicit exception because its server-minted manifest already freezes and
validates the complete approved PageSpec/Prototype bindings.

Blueprint-selection Start/discovery re-resolves the pinned Blueprint content
and dependency closure. It requires at least one semantic Page, exact current
same-project latest-approved PageSpec and Prototype bindings, exact
Blueprint->PageSpec->Prototype sources, an exact source multiset, and a
recomputed canonical selection identity. Blueprint approval and runtime
fan-out share one Page decoder, including the supported nested `node.spec`
shape, so an approved Blueprint cannot later fail because those two stages
interpreted Page fields differently.

Conversation discovery must call
`Facade.CompatibleDefinitionVersions(..., manifestRef, "application")` and exact
proposal/command revalidation must call
`Facade.ValidateCompatibleDefinitionVersion(...)`. Both resolve the pinned
manifest and trusted persisted artifact kind/count/approval metadata; neither
trusts client candidate lists. Contractless historical definitions remain
available to already pinned runs but are excluded from new-run discovery.

`ApplicationBuildContext` freezes the same exact profile at both the context
level and inside its `WorkflowDefinitionRef`, and validates both against the
definition-version and run rows. A Workbench bundle frozen before migration 016
is a read-only exception only when both fields are absent and both database
rows carry the legacy profile. Its payload is never rewritten, so its existing
ManifestHash remains stable; every newly created bundle requires both refs.
