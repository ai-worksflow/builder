package constructor

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/contracts"
	"github.com/worksflow/builder/backend/internal/domain"
)

var obligationIDCharacters = regexp.MustCompile(`[^A-Za-z0-9]+`)

var requiredSourceKinds = []string{
	"requirement_baseline", "blueprint", "page_spec", "prototype",
	contracts.KindAPI, contracts.KindData, contracts.KindPermission,
	contracts.KindAIRuntime, contracts.KindDeployment, contracts.KindVerification,
}

type Compiler struct{}

func (Compiler) Compile(input CompileInput) (CompiledContract, error) {
	identity, err := compilerIdentity()
	if err != nil {
		return CompiledContract{}, err
	}
	content := ContractContent{
		SchemaVersion: BuildContractSchemaVersion, Compiler: identity,
		ProjectID: strings.TrimSpace(input.ProjectID), DeliverySliceID: strings.TrimSpace(input.DeliverySliceID),
		BuildManifest: normalizeBuildManifest(input.BuildManifest), BaseWorkspace: normalizeWorkspace(input.BaseWorkspace),
		SourceRevisions: []ExactRevisionRef{}, FullStackTemplate: normalizeFullStackTemplate(input.FullStackTemplate),
		TemplateReleaseRefs: normalizeTemplateReleases(input.TemplateReleaseRefs),
		Routes:              []RouteConstraint{}, States: []StateConstraint{}, ContractBindings: []ContractBinding{},
		AcceptanceCriteria: []AcceptanceCriterion{}, Oracles: []Oracle{}, Obligations: []Obligation{},
		Waivers: []Waiver{}, Gaps: []BuildGap{}, Conflicts: []BuildConflict{},
		ForbiddenClaims: normalizeStrings(input.ForbiddenClaims), Status: StatusBlocked,
	}
	if len(content.ForbiddenClaims) == 0 {
		content.ForbiddenClaims = []string{
			"Do not claim persistence without an exact Data Contract and passing persistence oracle.",
			"Do not claim AI integration without the pinned AI Runtime Contract and runtime verification.",
			"Do not claim deployment readiness without a passing canonical release verification receipt.",
		}
	}

	gapBuilder := newDiagnosticBuilder()
	validateEnvelope(input, &content, gapBuilder)
	sourcesByKind := validateAndIndexSources(input.Sources, &content, gapBuilder)
	validateRequiredSources(sourcesByKind, gapBuilder)
	validateTemplates(content.FullStackTemplate, content.TemplateReleaseRefs, gapBuilder)

	contractFacts := make(map[string]contracts.Facts)
	contractSourceRefs := make(map[string]ExactRevisionRef)
	for _, kind := range []string{contracts.KindAPI, contracts.KindData, contracts.KindPermission, contracts.KindAIRuntime, contracts.KindDeployment, contracts.KindVerification} {
		sources := sourcesByKind[kind]
		if len(sources) != 1 {
			continue
		}
		facts, findings := contracts.Inspect(kind, sources[0].Content)
		if len(findings) != 0 {
			for _, finding := range findings {
				gapBuilder.gap("contract_invalid", finding.Code, finding.Path, finding.Message, sources[0].Ref.RevisionID, nil)
			}
			continue
		}
		if facts.SchemaVersion != contracts.ExpectedSchemaVersion(kind) {
			gapBuilder.gap(
				"contract_invalid", "contract_schema_version_obsolete", "$.schemaVersion",
				"BuildContract requires current machine contract schema "+contracts.ExpectedSchemaVersion(kind)+"; received "+facts.SchemaVersion+".",
				sources[0].Ref.RevisionID, nil,
			)
			continue
		}
		contractFacts[kind] = facts
		contractSourceRefs[kind] = sources[0].Ref
	}
	if _, deploymentValid := contractFacts[contracts.KindDeployment]; deploymentValid && len(sourcesByKind[contracts.KindDeployment]) == 1 {
		validateTemplateDeploymentClosure(
			content.FullStackTemplate,
			content.TemplateReleaseRefs,
			input.TemplateRuntime,
			sourcesByKind[contracts.KindDeployment][0],
			gapBuilder,
		)
	}
	if aiRuntime := contractFacts[contracts.KindAIRuntime].AIRuntimeProfile; len(contractFacts) == 6 && aiRuntime != nil {
		documents := make(map[string]json.RawMessage, len(sourcesByKind))
		for kind, sources := range sourcesByKind {
			if len(sources) == 1 {
				documents[kind] = sources[0].Content
			}
		}
		_, profileFindings := contracts.InspectApplicationProfile(aiRuntime.ApplicationProfile, documents)
		for _, finding := range profileFindings {
			sourceKind := finding.SourceKind
			if sourceKind == "" || len(sourcesByKind[sourceKind]) != 1 {
				sourceKind = contracts.KindAIRuntime
			}
			gapBuilder.gap(
				"application_profile", finding.Code, finding.Path, finding.Message,
				singleSourceRevisionID(sourcesByKind[sourceKind]), nil,
			)
		}
	}
	semanticSources := validateSemanticSources(sourcesByKind, strings.TrimSpace(input.DeliverySlicePageNodeID), contractFacts, gapBuilder)

	baselineCriteria, mustRequirements := compileBaseline(sourcesByKind["requirement_baseline"], gapBuilder)
	compilePageSpecs(sourcesByKind["page_spec"], semanticSources.validPageSpecRevisions, baselineCriteria, contractFacts, contractSourceRefs, &content, gapBuilder)
	compileOracles(sourcesByKind[contracts.KindVerification], contractFacts[contracts.KindVerification], baselineCriteria, &content, gapBuilder)
	compileObligations(mustRequirements, baselineCriteria, &content, gapBuilder)

	content.Gaps = gapBuilder.sortedGaps()
	content.Conflicts = gapBuilder.sortedConflicts()
	if len(content.Gaps) == 0 && len(content.Conflicts) == 0 && obligationsReady(content.Obligations) {
		content.Status = StatusReady
	}
	normalizeContent(&content)
	contentHash, err := domain.CanonicalHash(content)
	if err != nil {
		return CompiledContract{}, fmt.Errorf("hash Application Build Contract: %w", err)
	}
	return CompiledContract{Content: content, ContentHash: contentHash, ContractHash: contentHash}, nil
}

func compilerIdentity() (CompilerIdentity, error) {
	descriptor := map[string]any{
		"version":             CompilerVersion,
		"buildContractSchema": BuildContractSchemaVersion,
		"requiredSourceKinds": append([]string{}, requiredSourceKinds...),
		"semanticValidationProfile": []string{
			"blueprint-semantic-page/v1",
			"page-spec-blueprint-page/v1",
			"prototype-page-spec-state-frame/v1",
			"exact-semantic-authority/v1",
			"delivery-slice-api-closure/v1",
			"application-profile-closure/v2",
			"openapi-local-response-closure/v1",
			"tenant-composite-foreign-key/v1",
			"typed-run-event-shape/v1",
			"template-deployment-runtime-closure/v1",
		},
		"machineContractSchemas": map[string]string{
			contracts.KindAPI:          contracts.ExpectedSchemaVersion(contracts.KindAPI),
			contracts.KindData:         contracts.ExpectedSchemaVersion(contracts.KindData),
			contracts.KindPermission:   contracts.ExpectedSchemaVersion(contracts.KindPermission),
			contracts.KindAIRuntime:    contracts.ExpectedSchemaVersion(contracts.KindAIRuntime),
			contracts.KindDeployment:   contracts.ExpectedSchemaVersion(contracts.KindDeployment),
			contracts.KindVerification: contracts.ExpectedSchemaVersion(contracts.KindVerification),
		},
	}
	hash, err := domain.CanonicalHash(descriptor)
	return CompilerIdentity{Version: CompilerVersion, Hash: hash}, err
}

func validateEnvelope(input CompileInput, content *ContractContent, diagnostics *diagnosticBuilder) {
	if content.ProjectID == "" {
		diagnostics.gap("input", "project_required", "$.projectId", "Project ID is required.", "", nil)
	}
	if content.DeliverySliceID == "" {
		diagnostics.gap("input", "delivery_slice_required", "$.deliverySliceId", "Delivery slice ID is required.", "", nil)
	}
	if content.BuildManifest.ID == "" || !domain.IsCanonicalHash(content.BuildManifest.ContentHash) {
		diagnostics.gap("input", "build_manifest_invalid", "$.buildManifest", "An exact Build Manifest ID and canonical hash are required.", content.BuildManifest.ID, nil)
	}
	if content.BaseWorkspace != nil && (content.BaseWorkspace.ArtifactID == "" || content.BaseWorkspace.RevisionID == "" || !domain.IsCanonicalHash(content.BaseWorkspace.ContentHash)) {
		diagnostics.gap("input", "base_workspace_invalid", "$.baseWorkspaceRevision", "Base Workspace revision must be exact when present.", content.BaseWorkspace.RevisionID, nil)
	}
}

func validateAndIndexSources(sources []PinnedBuildSource, content *ContractContent, diagnostics *diagnosticBuilder) map[string][]PinnedBuildSource {
	copySources := append([]PinnedBuildSource{}, sources...)
	sort.Slice(copySources, func(i, j int) bool { return sourceKey(copySources[i].Ref) < sourceKey(copySources[j].Ref) })
	result := make(map[string][]PinnedBuildSource)
	identityHashes := map[string]string{}
	for index, source := range copySources {
		source.Ref = normalizeRevisionRef(source.Ref)
		path := fmt.Sprintf("$.sources[%d]", index)
		identity := source.Ref.ArtifactID + ":" + source.Ref.RevisionID
		if source.Ref.Kind == "" || source.Ref.ArtifactID == "" || source.Ref.RevisionID == "" || !domain.IsCanonicalHash(source.Ref.ContentHash) {
			diagnostics.gap("source", "source_revision_invalid", path, "Every source must pin kind, artifact, revision, and canonical content hash.", source.Ref.RevisionID, nil)
		}
		actualHash, hashErr := domain.CanonicalHash(source.Content)
		expectedHash := strings.TrimPrefix(source.Ref.ContentHash, "sha256:")
		if hashErr != nil || actualHash != expectedHash {
			diagnostics.gap("source", "source_content_hash_mismatch", path+".content", "Frozen source content does not match its exact revision hash.", source.Ref.RevisionID, nil)
		}
		if source.Ref.ApprovalStatus != "approved" {
			diagnostics.gap("source", "source_not_approved", path+".approvalStatus", "BuildContract sources must be canonically approved.", source.Ref.RevisionID, nil)
		}
		if previous, exists := identityHashes[identity]; exists {
			if previous != source.Ref.ContentHash {
				diagnostics.conflict("source_identity_hash_conflict", "One source revision identity has multiple content hashes.", []string{source.Ref.RevisionID})
			} else {
				diagnostics.conflict("source_identity_duplicate", "One exact source revision appears more than once.", []string{source.Ref.RevisionID})
			}
		} else {
			identityHashes[identity] = source.Ref.ContentHash
		}
		result[source.Ref.Kind] = append(result[source.Ref.Kind], source)
		content.SourceRevisions = append(content.SourceRevisions, source.Ref)
	}
	for kind, values := range result {
		if contains(requiredSourceKinds, kind) && len(values) > 1 {
			ids := make([]string, 0, len(values))
			for _, value := range values {
				ids = append(ids, value.Ref.RevisionID)
			}
			diagnostics.conflict("multiple_"+kind, "BuildContract requires one exact "+kind+" source for this delivery slice.", ids)
		}
	}
	return result
}

func validateRequiredSources(sources map[string][]PinnedBuildSource, diagnostics *diagnosticBuilder) {
	for _, kind := range requiredSourceKinds {
		if len(sources[kind]) == 0 {
			diagnostics.gap("required-source", "required_contract_missing", "$.sourceRevisions", "Required approved source "+kind+" is missing.", kind, nil)
		}
	}
}

func validateTemplates(template FullStackTemplateRef, releases []TemplateReleaseRef, diagnostics *diagnosticBuilder) {
	if template.ID == "" || !domain.IsCanonicalHash(template.ContentHash) || template.Certification != "approved" || template.PolicyStatus != "active" {
		diagnostics.gap("template", "full_stack_template_unavailable", "$.fullStackTemplate", "An exact active approved FullStackTemplate is required.", template.ID, nil)
	}
	roles := map[string]bool{}
	for index, release := range releases {
		if release.ID == "" || !domain.IsCanonicalHash(release.ReleaseHash) {
			diagnostics.gap("template", "template_release_invalid", fmt.Sprintf("$.templateReleaseRefs[%d]", index), "Template Release identity and canonical hash are required.", release.ID, nil)
		}
		if release.Certification != "approved" || release.PolicyStatus != "active" {
			diagnostics.gap("template", "template_release_unavailable", fmt.Sprintf("$.templateReleaseRefs[%d]", index), "Template Release must be approved and active.", release.ID, nil)
		}
		if roles[release.Role] {
			diagnostics.conflict("template_role_duplicate", "FullStackTemplate component role is duplicated.", []string{release.Role})
		}
		roles[release.Role] = true
	}
	for _, role := range []string{"web", "api"} {
		if !roles[role] {
			diagnostics.gap("template", "template_role_missing", "$.templateReleaseRefs", "FullStackTemplate requires a "+role+" component.", role, nil)
		}
	}
}

type baselineCriterion struct {
	ID             string
	Statement      string
	RequirementIDs []string
	Source         ExactRevisionRef
}

type mustRequirement struct {
	ID                     string
	AcceptanceCriterionIDs []string
	Source                 ExactRevisionRef
}

func compileBaseline(sources []PinnedBuildSource, diagnostics *diagnosticBuilder) (map[string]baselineCriterion, []mustRequirement) {
	criteria := map[string]baselineCriterion{}
	must := []mustRequirement{}
	if len(sources) != 1 {
		return criteria, must
	}
	var value map[string]any
	if err := json.Unmarshal(sources[0].Content, &value); err != nil {
		diagnostics.gap("baseline", "requirement_baseline_invalid", "$", "Requirement Baseline is not valid JSON.", sources[0].Ref.RevisionID, nil)
		return criteria, must
	}
	facts := objectSlice(value["requirements"])
	for _, item := range facts {
		if strings.EqualFold(stringField(item, "type"), "acceptanceCriterion") {
			id := firstStringField(item, "acceptanceCriterionId", "id", "key")
			if _, duplicate := criteria[id]; duplicate {
				diagnostics.conflict("acceptance_criterion_duplicate", "Acceptance criterion anchor is duplicated.", []string{id})
			}
			criteria[id] = baselineCriterion{ID: id, Statement: stringField(item, "statement"), RequirementIDs: []string{}, Source: sources[0].Ref}
		}
	}
	for _, item := range facts {
		if !strings.EqualFold(stringField(item, "type"), "requirement") || !strings.EqualFold(stringField(item, "priority"), "must") {
			continue
		}
		requirementID := firstStringField(item, "requirementId", "id", "key")
		links := uniqueSortedStrings(anyStringSlice(item["acceptanceCriterionIds"]))
		if requirementID == "" || len(links) == 0 {
			diagnostics.gap("baseline", "must_acceptance_missing", "$.requirements", "Every Must requirement needs stable acceptance criteria.", requirementID, nil)
		}
		for _, acceptanceID := range links {
			criterion, exists := criteria[acceptanceID]
			if !exists {
				diagnostics.gap("baseline", "acceptance_criterion_unknown", "$.requirements", "Must requirement references unknown acceptance criterion "+acceptanceID+".", requirementID, nil)
				continue
			}
			criterion.RequirementIDs = append(criterion.RequirementIDs, requirementID)
			criteria[acceptanceID] = criterion
		}
		must = append(must, mustRequirement{ID: requirementID, AcceptanceCriterionIDs: links, Source: sources[0].Ref})
	}
	if len(must) == 0 {
		diagnostics.gap("baseline", "must_requirement_missing", "$.requirements", "Requirement Baseline must contain at least one Must requirement.", sources[0].Ref.RevisionID, nil)
	}
	sort.Slice(must, func(i, j int) bool { return must[i].ID < must[j].ID })
	return criteria, must
}

func compilePageSpecs(sources []PinnedBuildSource, validRevisions map[string]bool, criteria map[string]baselineCriterion, facts map[string]contracts.Facts, contractSourceRefs map[string]ExactRevisionRef, content *ContractContent, diagnostics *diagnosticBuilder) {
	apiOperations := map[string]bool{}
	for _, operation := range facts[contracts.KindAPI].Operations {
		apiOperations[operation.ID] = true
	}
	dataEntities := map[string]bool{}
	for _, entity := range facts[contracts.KindData].Entities {
		dataEntities[entity.ID] = true
	}
	permissionRoles := stringSet(facts[contracts.KindPermission].Roles)
	for _, source := range sources {
		if !validRevisions[source.Ref.RevisionID] {
			continue
		}
		var page map[string]any
		if err := json.Unmarshal(source.Content, &page); err != nil {
			diagnostics.gap("page", "page_spec_invalid", "$", "PageSpec is not valid JSON.", source.Ref.RevisionID, nil)
			continue
		}
		pageID := firstStringField(page, "blueprintPageNodeId", "id")
		route := stringField(page, "route")
		acceptanceIDs := uniqueSortedStrings(append(anyStringSlice(page["acceptanceCriterionIds"]), acceptanceRefIDs(page["acceptanceRefs"])...))
		roles := uniqueSortedStrings(anyStringSlice(page["requiredRoles"]))
		content.Routes = append(content.Routes, RouteConstraint{PageNodeID: pageID, Route: route, RequiredRoles: roles, AcceptanceCriterionIDs: acceptanceIDs})
		for _, acceptanceID := range acceptanceIDs {
			if _, exists := criteria[acceptanceID]; !exists {
				diagnostics.gap("page", "page_acceptance_unknown", "$.acceptanceCriterionIds", "PageSpec references an acceptance criterion outside the exact Requirement Baseline.", source.Ref.RevisionID, nil)
			}
		}
		for _, role := range roles {
			if !permissionRoles[role] {
				diagnostics.gap("binding", "permission_role_missing", "$.requiredRoles", "Required PageSpec role "+role+" is absent from the Permission Contract.", source.Ref.RevisionID, nil)
				continue
			}
			content.ContractBindings = append(content.ContractBindings, ContractBinding{
				ID: "role:" + pageID + ":" + role, Kind: "permission", TargetID: role,
				SourceRevision: contractSourceRefs[contracts.KindPermission],
			})
		}
		for _, state := range objectSlice(page["states"]) {
			required, _ := state["required"].(bool)
			content.States = append(content.States, StateConstraint{PageNodeID: pageID, ID: stringField(state, "id"), Key: firstStringField(state, "key", "name"), Required: required})
		}
		for index, binding := range objectSlice(page["dataBindings"]) {
			bindingID := firstStringField(binding, "id", "name")
			switch stringField(binding, "source") {
			case "api":
				operationID := stringField(binding, "operationId")
				if !apiOperations[operationID] {
					diagnostics.gap("binding", "api_operation_missing", fmt.Sprintf("$.dataBindings[%d].operationId", index), "PageSpec API operation is absent from the exact API Contract.", source.Ref.RevisionID, nil)
					continue
				}
				content.ContractBindings = append(content.ContractBindings, ContractBinding{
					ID: bindingID, Kind: "api", TargetID: operationID,
					SourceRevision: contractSourceRefs[contracts.KindAPI],
				})
			case "database":
				entityID := firstStringField(binding, "entityId", "targetId")
				if entityID == "" || !dataEntities[entityID] {
					diagnostics.gap("binding", "data_entity_missing", fmt.Sprintf("$.dataBindings[%d].entityId", index), "Database binding must name an entity in the exact Data Contract.", source.Ref.RevisionID, nil)
					continue
				}
				content.ContractBindings = append(content.ContractBindings, ContractBinding{
					ID: bindingID, Kind: "data", TargetID: entityID,
					SourceRevision: contractSourceRefs[contracts.KindData],
				})
			}
		}
	}
}

func compileOracles(sources []PinnedBuildSource, facts contracts.Facts, criteria map[string]baselineCriterion, content *ContractContent, diagnostics *diagnosticBuilder) {
	if len(sources) != 1 {
		return
	}
	for _, oracle := range facts.Oracles {
		for _, acceptanceID := range oracle.AcceptanceCriterionIDs {
			if _, exists := criteria[acceptanceID]; !exists {
				diagnostics.gap("oracle", "oracle_acceptance_unknown", "$.oracles", "Verification oracle references unknown acceptance criterion "+acceptanceID+".", sources[0].Ref.RevisionID, nil)
			}
		}
		content.Oracles = append(content.Oracles, Oracle{
			ID: oracle.ID, AcceptanceCriterionIDs: append([]string{}, oracle.AcceptanceCriterionIDs...), Kind: oracle.Kind,
			Target: oracle.Target, CommandID: oracle.CommandID, SourceRevision: sources[0].Ref,
		})
	}
}

func compileObligations(requirements []mustRequirement, criteria map[string]baselineCriterion, content *ContractContent, diagnostics *diagnosticBuilder) {
	oraclesByAcceptance := map[string][]string{}
	for _, oracle := range content.Oracles {
		for _, acceptanceID := range oracle.AcceptanceCriterionIDs {
			oraclesByAcceptance[acceptanceID] = append(oraclesByAcceptance[acceptanceID], oracle.ID)
		}
	}
	requirementSetByCriterion := map[string]map[string]bool{}
	for _, requirement := range requirements {
		for _, acceptanceID := range requirement.AcceptanceCriterionIDs {
			if requirementSetByCriterion[acceptanceID] == nil {
				requirementSetByCriterion[acceptanceID] = map[string]bool{}
			}
			requirementSetByCriterion[acceptanceID][requirement.ID] = true
		}
	}
	criterionIDs := make([]string, 0, len(requirementSetByCriterion))
	for acceptanceID := range requirementSetByCriterion {
		criterionIDs = append(criterionIDs, acceptanceID)
	}
	sort.Strings(criterionIDs)
	for _, acceptanceID := range criterionIDs {
		criterion, exists := criteria[acceptanceID]
		if !exists {
			continue
		}
		requirementIDs := sortedBoolKeys(requirementSetByCriterion[acceptanceID])
		criterion.RequirementIDs = requirementIDs
		content.AcceptanceCriteria = append(content.AcceptanceCriteria, AcceptanceCriterion{
			ID: criterion.ID, Statement: criterion.Statement, RequirementIDs: requirementIDs, SourceRevision: criterion.Source,
		})
		oracleIDs := uniqueSortedStrings(oraclesByAcceptance[acceptanceID])
		obligationID := obligationID(acceptanceID)
		obligation := Obligation{
			ID: obligationID, Level: "must", Kind: "acceptance", SourceRevision: criterion.Source,
			SourceAnchorID: acceptanceID, OracleIDs: oracleIDs, DependsOn: []string{}, Waivable: false, Status: StatusReady,
		}
		if criterion.Statement == "" {
			gapID := diagnostics.gap("obligation", "acceptance_target_missing", "$.acceptanceCriteria", "Must acceptance criterion has no implementable statement.", acceptanceID, []string{obligationID})
			obligation.Status = StatusBlocked
			obligation.BlockingReasonID = gapID
		} else if len(oracleIDs) == 0 {
			gapID := diagnostics.gap("obligation", "must_oracle_missing", "$.oracles", "Must acceptance criterion has no executable Oracle.", acceptanceID, []string{obligationID})
			obligation.Status = StatusBlocked
			obligation.BlockingReasonID = gapID
		}
		content.Obligations = append(content.Obligations, obligation)
	}
}

type diagnosticBuilder struct {
	gaps      map[string]BuildGap
	conflicts map[string]BuildConflict
}

func newDiagnosticBuilder() *diagnosticBuilder {
	return &diagnosticBuilder{gaps: map[string]BuildGap{}, conflicts: map[string]BuildConflict{}}
}

func (builder *diagnosticBuilder) gap(namespace, code, path, message, sourceID string, obligationIDs []string) string {
	key := strings.Join([]string{namespace, code, path, sourceID, strings.Join(uniqueSortedStrings(obligationIDs), ",")}, "|")
	if existing, exists := builder.gaps[key]; exists {
		return existing.ID
	}
	id := diagnosticID("GAP", key)
	builder.gaps[key] = BuildGap{ID: id, Code: code, Path: path, Message: message, SourceID: sourceID, ObligationIDs: uniqueSortedStrings(obligationIDs), Blocking: true}
	return id
}

func (builder *diagnosticBuilder) conflict(code, message string, sourceIDs []string) {
	sources := uniqueSortedStrings(sourceIDs)
	key := code + "|" + strings.Join(sources, ",")
	if _, exists := builder.conflicts[key]; exists {
		return
	}
	builder.conflicts[key] = BuildConflict{ID: diagnosticID("CONFLICT", key), Code: code, Message: message, SourceIDs: sources, Blocking: true}
}

func (builder *diagnosticBuilder) sortedGaps() []BuildGap {
	keys := make([]string, 0, len(builder.gaps))
	for key := range builder.gaps {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]BuildGap, 0, len(keys))
	for _, key := range keys {
		result = append(result, builder.gaps[key])
	}
	return result
}

func (builder *diagnosticBuilder) sortedConflicts() []BuildConflict {
	keys := make([]string, 0, len(builder.conflicts))
	for key := range builder.conflicts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]BuildConflict, 0, len(keys))
	for _, key := range keys {
		result = append(result, builder.conflicts[key])
	}
	return result
}

func diagnosticID(prefix, key string) string {
	digest := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%s-%X", prefix, digest[:8])
}

func normalizeContent(content *ContractContent) {
	sort.Slice(content.SourceRevisions, func(i, j int) bool {
		return sourceKey(content.SourceRevisions[i]) < sourceKey(content.SourceRevisions[j])
	})
	sort.Slice(content.Routes, func(i, j int) bool {
		return content.Routes[i].PageNodeID+"|"+content.Routes[i].Route < content.Routes[j].PageNodeID+"|"+content.Routes[j].Route
	})
	sort.Slice(content.States, func(i, j int) bool {
		return content.States[i].PageNodeID+"|"+content.States[i].Key < content.States[j].PageNodeID+"|"+content.States[j].Key
	})
	sort.Slice(content.ContractBindings, func(i, j int) bool {
		return content.ContractBindings[i].Kind+"|"+content.ContractBindings[i].ID < content.ContractBindings[j].Kind+"|"+content.ContractBindings[j].ID
	})
	sort.Slice(content.AcceptanceCriteria, func(i, j int) bool { return content.AcceptanceCriteria[i].ID < content.AcceptanceCriteria[j].ID })
	sort.Slice(content.Oracles, func(i, j int) bool { return content.Oracles[i].ID < content.Oracles[j].ID })
	sort.Slice(content.Obligations, func(i, j int) bool { return content.Obligations[i].ID < content.Obligations[j].ID })
}

func obligationsReady(obligations []Obligation) bool {
	if len(obligations) == 0 {
		return false
	}
	for _, obligation := range obligations {
		if obligation.Level == "must" && obligation.Status != StatusReady {
			return false
		}
	}
	return true
}

func normalizeBuildManifest(value BuildManifestRef) BuildManifestRef {
	return BuildManifestRef{ID: strings.TrimSpace(value.ID), ContentHash: strings.TrimSpace(value.ContentHash)}
}

func normalizeWorkspace(value *WorkspaceRevisionRef) *WorkspaceRevisionRef {
	if value == nil {
		return nil
	}
	return &WorkspaceRevisionRef{ArtifactID: strings.TrimSpace(value.ArtifactID), RevisionID: strings.TrimSpace(value.RevisionID), ContentHash: strings.TrimSpace(value.ContentHash)}
}

func normalizeRevisionRef(value ExactRevisionRef) ExactRevisionRef {
	return ExactRevisionRef{
		Kind: strings.TrimSpace(value.Kind), Purpose: strings.TrimSpace(value.Purpose), Required: value.Required,
		ArtifactID: strings.TrimSpace(value.ArtifactID), RevisionID: strings.TrimSpace(value.RevisionID),
		ContentHash: strings.TrimSpace(value.ContentHash), ApprovalStatus: strings.TrimSpace(value.ApprovalStatus),
	}
}

func normalizeFullStackTemplate(value FullStackTemplateRef) FullStackTemplateRef {
	return FullStackTemplateRef{ID: strings.TrimSpace(value.ID), ContentHash: strings.TrimSpace(value.ContentHash), Certification: strings.TrimSpace(value.Certification), PolicyStatus: strings.TrimSpace(value.PolicyStatus)}
}

func normalizeTemplateReleases(values []TemplateReleaseRef) []TemplateReleaseRef {
	result := make([]TemplateReleaseRef, 0, len(values))
	for _, value := range values {
		result = append(result, TemplateReleaseRef{
			ID: strings.TrimSpace(value.ID), ReleaseHash: strings.TrimSpace(value.ReleaseHash), Role: strings.TrimSpace(value.Role),
			Certification: strings.TrimSpace(value.Certification), PolicyStatus: strings.TrimSpace(value.PolicyStatus),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Role+"|"+result[i].ID < result[j].Role+"|"+result[j].ID })
	return result
}

func sourceKey(value ExactRevisionRef) string {
	return strings.Join([]string{value.Kind, value.Purpose, value.ArtifactID, value.RevisionID, value.ContentHash}, "|")
}

func normalizeStrings(values []string) []string { return uniqueSortedStrings(values) }

func uniqueSortedStrings(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedBoolKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value, included := range values {
		if included {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func objectSlice(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func anyStringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func acceptanceRefIDs(value any) []string {
	result := []string{}
	for _, reference := range objectSlice(value) {
		if id := firstStringField(reference, "acceptanceCriterionId", "id"); id != "" {
			result = append(result, id)
		}
	}
	return result
}

func stringField(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return strings.TrimSpace(text)
}

func firstStringField(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := stringField(value, key); text != "" {
			return text
		}
	}
	return ""
}

func stringSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}

func obligationID(acceptanceID string) string {
	acceptanceID = strings.TrimSpace(acceptanceID)
	normalized := strings.Trim(obligationIDCharacters.ReplaceAllString(strings.ToUpper(acceptanceID), "-"), "-")
	if normalized == "" {
		normalized = "UNKNOWN"
	}
	if len(normalized) > 48 {
		normalized = normalized[:48]
	}
	digest := sha256.Sum256([]byte(acceptanceID))
	return fmt.Sprintf("OBL-%s-%X", normalized, digest[:])
}
