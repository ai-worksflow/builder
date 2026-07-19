package qualificationreceipt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	QualificationManifestSchemaV1 = "worksflow-qualification-manifest/v1"
	TestInventorySchemaV2         = "worksflow-qualification-test-inventory/v2"
	ReferenceCriteriaSchemaV1     = "reference-acceptance-criteria/v1"
	testInventoryRepositoryPath   = "qualification/test-inventory.json"
)

type Plan struct {
	Digest                   string
	ManifestDigest           string
	Subject                  string
	CanonicalProjection      []byte
	ExternalSuites           []ExpectedSuite
	IncompleteExternalSuites []string
	TestInventoryDigest      string
	TestCases                []ExpectedTestCase
}

type qualificationManifestDocument struct {
	SchemaVersion             string            `json:"schemaVersion"`
	Subject                   string            `json:"subject"`
	SourceDocuments           []string          `json:"sourceDocuments"`
	QualificationSupportPaths []string          `json:"qualificationSupportPaths"`
	Policy                    json.RawMessage   `json:"policy"`
	Suites                    []json.RawMessage `json:"suites"`
	Trust                     json.RawMessage   `json:"trust,omitempty"`
	TrustPolicy               json.RawMessage   `json:"trustPolicy,omitempty"`
	TrustPolicyDigest         string            `json:"trustPolicyDigest,omitempty"`
}

type planFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type testInventory struct {
	SchemaVersion    string                         `json:"schemaVersion"`
	CriterionSources []testInventoryCriterionSource `json:"criterionSources"`
	Cases            []testInventoryCase            `json:"cases"`
}

type testInventoryCriterionSource struct {
	SuiteID       string `json:"suiteId"`
	Path          string `json:"path"`
	SchemaVersion string `json:"schemaVersion"`
	ApplicationID string `json:"applicationId"`
}

type testInventoryCase struct {
	CaseID               string   `json:"caseId"`
	SuiteID              string   `json:"suiteId"`
	RequirementIDs       []string `json:"requirementIds"`
	ContractCriterionIDs []string `json:"contractCriterionIds"`
	File                 string   `json:"file"`
	Title                string   `json:"title"`
	Mode                 string   `json:"mode"`
}

type referenceAcceptanceCriteria struct {
	SchemaVersion string                         `json:"schemaVersion"`
	ApplicationID string                         `json:"applicationId"`
	Criteria      []referenceAcceptanceCriterion `json:"criteria"`
}

type referenceAcceptanceCriterion struct {
	ID             string   `json:"id"`
	RequirementIDs []string `json:"requirementIds"`
	Statement      string   `json:"statement"`
}

type qualificationPolicy struct {
	StageExitRequiresExternalQualification    bool   `json:"stageExitRequiresExternalQualification"`
	AllowSkippedTests                         bool   `json:"allowSkippedTests"`
	AllowMocks                                bool   `json:"allowMocks"`
	AllowMutableRuntimeImages                 bool   `json:"allowMutableRuntimeImages"`
	CredentialBearingArtifacts                string `json:"credentialBearingArtifacts"`
	PassingInternalSuitesAreStageExitEvidence bool   `json:"passingInternalSuitesAreStageExitEvidence"`
}

// ComputePlanDigest builds the immutable plan projection used on both sides of
// promotion. Operational status, coverage, blocker/limitation prose, receipt
// pointers, and trust selection are deliberately excluded; all executable and
// policy inputs remain hash-bound.
func ComputePlanDigest(repositoryRoot, manifestPath string) (Plan, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil || filepath.Clean(root) != root {
		return Plan{}, errors.New("repository root must be an absolute normalized path")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return Plan{}, errors.New("repository root must be a real directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || resolvedRoot != root {
		return Plan{}, errors.New("repository root path must not contain symlink components")
	}
	manifestAbsolute, _, err := resolveRepositoryFile(root, manifestPath)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve qualification manifest: %w", err)
	}
	manifestBytes, err := readBoundedRegularFile(manifestAbsolute, maxIndexBytes, false)
	if err != nil {
		return Plan{}, fmt.Errorf("read qualification manifest: %w", err)
	}
	if err := requireExactShape(manifestBytes, qualificationManifestShape()); err != nil {
		return Plan{}, fmt.Errorf("validate qualification manifest shape: %w", err)
	}
	var manifest qualificationManifestDocument
	if err := decodeStrictJSON(manifestBytes, &manifest); err != nil {
		return Plan{}, fmt.Errorf("decode qualification manifest: %w", err)
	}
	if manifest.SchemaVersion != QualificationManifestSchemaV1 || !validCanonicalString(manifest.Subject, 256) {
		return Plan{}, errors.New("qualification manifest schemaVersion or subject is invalid")
	}
	if len(manifest.SourceDocuments) == 0 || len(manifest.QualificationSupportPaths) == 0 || len(manifest.Suites) == 0 {
		return Plan{}, errors.New("qualification manifest must declare sourceDocuments, qualificationSupportPaths, and suites")
	}
	policy, err := decodeJSONValue(manifest.Policy)
	if err != nil {
		return Plan{}, fmt.Errorf("decode qualification policy: %w", err)
	}
	if _, ok := policy.(map[string]any); !ok {
		return Plan{}, errors.New("qualification policy must be a JSON object")
	}
	var enforcedPolicy qualificationPolicy
	if err := requireExactShape(manifest.Policy, qualificationPolicyShape()); err != nil {
		return Plan{}, fmt.Errorf("validate qualification policy shape: %w", err)
	}
	if err := decodeStrictJSON(manifest.Policy, &enforcedPolicy); err != nil ||
		!enforcedPolicy.StageExitRequiresExternalQualification || enforcedPolicy.AllowSkippedTests ||
		enforcedPolicy.AllowMocks || enforcedPolicy.AllowMutableRuntimeImages ||
		enforcedPolicy.CredentialBearingArtifacts != "restricted-encrypted-until-revocation" ||
		enforcedPolicy.PassingInternalSuitesAreStageExitEvidence {
		return Plan{}, errors.New("qualification stage-exit policy is not fail closed")
	}

	sourceFiles, err := projectFiles(root, manifest.SourceDocuments, "sourceDocuments")
	if err != nil {
		return Plan{}, err
	}
	supportFiles, err := projectFiles(root, manifest.QualificationSupportPaths, "qualificationSupportPaths")
	if err != nil {
		return Plan{}, err
	}
	testInventoryDigest := ""
	for _, file := range supportFiles {
		if file.Path == testInventoryRepositoryPath {
			testInventoryDigest = file.SHA256
			break
		}
	}
	if testInventoryDigest == "" {
		return Plan{}, fmt.Errorf("qualificationSupportPaths must include %s", testInventoryRepositoryPath)
	}
	supportPathSet := make(map[string]struct{}, len(supportFiles))
	for _, file := range supportFiles {
		supportPathSet[file.Path] = struct{}{}
	}

	projectedSuites := make([]map[string]any, 0, len(manifest.Suites))
	externalSuites := make([]ExpectedSuite, 0)
	incompleteExternalSuites := make([]string, 0)
	suiteTestPaths := map[string]map[string]struct{}{}
	suiteRequirements := map[string][]string{}
	suiteModes := map[string]string{}
	suiteExecutionKinds := map[string]string{}
	suiteCriterionSources := map[string]testInventoryCriterionSource{}
	suiteIDs := map[string]struct{}{}
	mappedRequirements := map[string]string{}
	documentedRequirements, err := extractDocumentedRequirementIDs(root, manifest.SourceDocuments)
	if err != nil {
		return Plan{}, err
	}
	playwrightExternalSuites := make([]ExpectedSuite, 0)
	for index, rawSuite := range manifest.Suites {
		suite, err := projectSuite(rawSuite)
		if err != nil {
			return Plan{}, fmt.Errorf("project suites[%d]: %w", index, err)
		}
		projected, expectedSuite := suite.Projected, suite.Expected
		if _, duplicate := suiteIDs[expectedSuite.ID]; duplicate {
			return Plan{}, fmt.Errorf("duplicate qualification suite %q", expectedSuite.ID)
		}
		suiteIDs[expectedSuite.ID] = struct{}{}
		for _, requirementID := range expectedSuite.RequirementIDs {
			if previousSuite, duplicate := mappedRequirements[requirementID]; duplicate {
				return Plan{}, fmt.Errorf("requirement %s is mapped by both %s and %s", requirementID, previousSuite, expectedSuite.ID)
			}
			mappedRequirements[requirementID] = expectedSuite.ID
		}
		projectedSuites = append(projectedSuites, projected)
		if rawCriterionSource, exists := projected["criterionSource"]; exists {
			criterionSource := rawCriterionSource.(map[string]any)
			source := testInventoryCriterionSource{
				SuiteID: expectedSuite.ID, Path: criterionSource["path"].(string),
				SchemaVersion: criterionSource["schemaVersion"].(string), ApplicationID: criterionSource["applicationId"].(string),
			}
			if _, supported := supportPathSet[source.Path]; !supported {
				return Plan{}, fmt.Errorf("suite %s criterionSource is not qualification-plan support material", expectedSuite.ID)
			}
			suiteCriterionSources[expectedSuite.ID] = source
		}
		suiteRequirements[expectedSuite.ID] = expectedSuite.RequirementIDs
		suiteModes[expectedSuite.ID] = suite.Mode
		suiteExecutionKinds[expectedSuite.ID] = suite.ExecutionKind
		pathSet := map[string]struct{}{}
		for _, testPath := range suite.TestPaths {
			pathSet[testPath] = struct{}{}
			if suite.ExecutionKind == "playwright" && suite.Coverage == "external-complete" {
				if _, supported := supportPathSet[testPath]; !supported {
					return Plan{}, fmt.Errorf("suite %s external-complete test path is not hash-bound support material", expectedSuite.ID)
				}
			}
		}
		suiteTestPaths[expectedSuite.ID] = pathSet
		if suite.VerificationContractPath != "" {
			if _, supported := supportPathSet[suite.VerificationContractPath]; !supported {
				return Plan{}, fmt.Errorf("suite %s verificationContractPath is not qualification-plan support material", expectedSuite.ID)
			}
		}
		if suite.Mode == "external-qualification" && suite.Coverage == "external-complete" {
			externalSuites = append(externalSuites, expectedSuite)
			if suite.ExecutionKind == "playwright" {
				playwrightExternalSuites = append(playwrightExternalSuites, expectedSuite)
			}
		} else if suite.Mode == "external-qualification" {
			incompleteExternalSuites = append(incompleteExternalSuites, expectedSuite.ID)
		}
	}
	if len(mappedRequirements) != len(documentedRequirements) {
		return Plan{}, errors.New("qualification suites do not exactly map the documented acceptance IDs")
	}
	for requirementID := range documentedRequirements {
		if _, exists := mappedRequirements[requirementID]; !exists {
			return Plan{}, fmt.Errorf("documented acceptance ID %s is not mapped by a qualification suite", requirementID)
		}
	}
	for requirementID := range mappedRequirements {
		if _, exists := documentedRequirements[requirementID]; !exists {
			return Plan{}, fmt.Errorf("qualification suite maps undocumented acceptance ID %s", requirementID)
		}
	}
	sort.Slice(projectedSuites, func(left, right int) bool {
		return projectedSuites[left]["id"].(string) < projectedSuites[right]["id"].(string)
	})
	sort.Slice(externalSuites, func(left, right int) bool { return externalSuites[left].ID < externalSuites[right].ID })
	sort.Slice(playwrightExternalSuites, func(left, right int) bool {
		return playwrightExternalSuites[left].ID < playwrightExternalSuites[right].ID
	})
	sort.Strings(incompleteExternalSuites)

	inventoryAbsolute, _, err := resolveRepositoryFile(root, testInventoryRepositoryPath)
	if err != nil {
		return Plan{}, err
	}
	inventoryBytes, err := readBoundedRegularFile(inventoryAbsolute, maxIndexBytes, false)
	if err != nil || sha256Digest(inventoryBytes) != testInventoryDigest {
		return Plan{}, errors.New("test inventory does not match its support-file digest")
	}
	testCases, err := validateTestInventory(
		root, inventoryBytes, playwrightExternalSuites, suiteTestPaths, suiteRequirements, suiteModes, suiteExecutionKinds,
		supportPathSet, suiteCriterionSources,
	)
	if err != nil {
		return Plan{}, err
	}

	projection := map[string]any{
		"schemaVersion":         "worksflow-qualification-plan/v1",
		"manifestSchemaVersion": manifest.SchemaVersion,
		"subject":               manifest.Subject,
		"policy":                policy,
		"sourceDocuments":       sourceFiles,
		"supportFiles":          supportFiles,
		"suites":                projectedSuites,
	}
	canonical, err := canonicalJSONBytes(projection)
	if err != nil {
		return Plan{}, fmt.Errorf("encode qualification plan projection: %w", err)
	}
	return Plan{
		Digest: sha256Digest(canonical), ManifestDigest: sha256Digest(manifestBytes),
		Subject:             manifest.Subject,
		CanonicalProjection: canonical, ExternalSuites: externalSuites,
		IncompleteExternalSuites: incompleteExternalSuites,
		TestInventoryDigest:      testInventoryDigest, TestCases: testCases,
	}, nil
}

type suiteProjection struct {
	Projected                map[string]any
	Expected                 ExpectedSuite
	Mode                     string
	Coverage                 string
	ExecutionKind            string
	TestPaths                []string
	VerificationContractPath string
}

func projectSuite(raw json.RawMessage) (suiteProjection, error) {
	value, err := decodeJSONValue(raw)
	if err != nil {
		return suiteProjection{}, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return suiteProjection{}, errors.New("suite must be an object")
	}
	id, err := objectString(object, "id")
	if err != nil || !validStableID(id) {
		return suiteProjection{}, errors.New("suite id is invalid")
	}
	mode, err := objectString(object, "mode")
	if err != nil || (mode != "internal-regression" && mode != "external-qualification" && mode != "governance-qualification") {
		return suiteProjection{}, errors.New("suite mode is invalid")
	}
	executionKind, err := objectString(object, "executionKind")
	if err != nil || (executionKind != "internal-test" && executionKind != "playwright" && executionKind != "post-run-verifier") {
		return suiteProjection{}, errors.New("suite executionKind is invalid")
	}
	coverage, err := objectString(object, "coverage")
	if err != nil || (coverage != "internal-complete" && coverage != "partial" && coverage != "planned" && coverage != "external-complete") {
		return suiteProjection{}, errors.New("suite coverage is invalid")
	}
	status, err := objectString(object, "status")
	if err != nil || (status != "implemented-internal" && status != "not-qualified" && status != "qualified") {
		return suiteProjection{}, errors.New("suite status is invalid")
	}
	requirements, err := objectStringArray(object, "requirementIds", requirementPattern.MatchString)
	if err != nil {
		return suiteProjection{}, err
	}
	requiredArtifacts, err := objectStringArray(object, "requiredArtifacts", validStableID)
	if err != nil {
		return suiteProjection{}, err
	}
	commands, hasCommands, err := optionalStringArray(object, "commands")
	if err != nil {
		return suiteProjection{}, err
	}
	qualificationCommand, hasQualificationCommand, err := optionalString(object, "qualificationCommand")
	if err != nil {
		return suiteProjection{}, err
	}
	qualificationGroup, hasQualificationGroup, err := optionalString(object, "qualificationGroup")
	if err != nil {
		return suiteProjection{}, err
	}
	criterionSource, hasCriterionSource, err := optionalCriterionSource(object)
	if err != nil {
		return suiteProjection{}, err
	}
	limitations, hasLimitations, err := optionalStringArray(object, "limitations")
	if err != nil {
		return suiteProjection{}, err
	}
	blockers, hasBlockers, err := optionalStringArray(object, "blockers")
	if err != nil {
		return suiteProjection{}, err
	}
	if hasLimitations == hasBlockers || len(limitations)+len(blockers) == 0 {
		return suiteProjection{}, errors.New("suite must declare exactly one non-empty limitations or blockers list")
	}

	testPaths, hasTestPaths, err := optionalStringArray(object, "testPaths")
	if err != nil {
		return suiteProjection{}, err
	}
	plannedTestPaths, hasPlannedTestPaths, err := optionalStringArray(object, "plannedTestPaths")
	if err != nil {
		return suiteProjection{}, err
	}
	smokePath, hasSmokePath, err := optionalString(object, "smokeTestPath")
	if err != nil {
		return suiteProjection{}, err
	}
	verificationContractPath, hasVerificationContract, err := optionalString(object, "verificationContractPath")
	if err != nil || (hasVerificationContract && !validArtifactPath(verificationContractPath)) {
		return suiteProjection{}, errors.New("suite verificationContractPath is invalid")
	}
	pathForms := 0
	for _, present := range []bool{hasTestPaths, hasPlannedTestPaths, hasSmokePath} {
		if present {
			pathForms++
		}
	}
	if executionKind == "post-run-verifier" {
		if pathForms != 0 || !hasVerificationContract {
			return suiteProjection{}, errors.New("post-run verifier suite must declare only verificationContractPath")
		}
	} else {
		if pathForms != 1 || hasVerificationContract {
			return suiteProjection{}, errors.New("internal-test/playwright suite must declare exactly one test path form and no verificationContractPath")
		}
		if hasPlannedTestPaths {
			testPaths = plannedTestPaths
		}
		if hasSmokePath {
			testPaths = []string{smokePath}
		}
		for index, testPath := range testPaths {
			if !validArtifactPath(testPath) {
				return suiteProjection{}, fmt.Errorf("suite test path %d is not a normalized repository-relative path", index)
			}
		}
		if executionKind == "playwright" && mode == "external-qualification" && coverage == "external-complete" && !hasTestPaths {
			return suiteProjection{}, errors.New("external-complete Playwright suite must declare only explicit testPaths")
		}
	}
	if mode == "internal-regression" {
		if executionKind != "internal-test" || status != "implemented-internal" || coverage != "internal-complete" || !hasCommands || hasQualificationCommand ||
			!hasTestPaths || hasQualificationGroup || hasCriterionSource {
			return suiteProjection{}, errors.New("internal suite must use internal-test and be implemented-internal/internal-complete with commands and explicit testPaths")
		}
	} else {
		if executionKind == "internal-test" || status != "not-qualified" || !hasQualificationGroup || hasCommands || coverage == "internal-complete" {
			return suiteProjection{}, errors.New("pre-promotion external/governance suite execution, status, group, commands, or coverage is invalid")
		}
		if mode == "external-qualification" && !hasQualificationCommand {
			return suiteProjection{}, errors.New("external qualification suite must declare qualificationCommand")
		}
		if mode == "governance-qualification" && (executionKind != "post-run-verifier" || hasQualificationCommand || coverage == "external-complete") {
			return suiteProjection{}, errors.New("governance qualification must use a post-run verifier and cannot claim an external-complete command")
		}
		if executionKind == "post-run-verifier" && hasCriterionSource {
			return suiteProjection{}, errors.New("post-run verifier cannot declare Playwright contract criteria")
		}
	}
	projected := map[string]any{
		"id": id, "mode": mode, "executionKind": executionKind,
		"requirementIds": requirements, "requiredArtifacts": requiredArtifacts,
	}
	if executionKind == "post-run-verifier" {
		projected["verificationContractPath"] = verificationContractPath
	} else {
		projected["testPaths"] = testPaths
	}
	if hasQualificationGroup {
		projected["qualificationGroup"] = qualificationGroup
	}
	if hasCommands {
		projected["commands"] = commands
	}
	if hasQualificationCommand {
		projected["qualificationCommand"] = qualificationCommand
	}
	if hasCriterionSource {
		projected["criterionSource"] = criterionSource
	}
	expectedRequirements := append([]string(nil), requirements...)
	expectedArtifacts := append([]string(nil), requiredArtifacts...)
	sort.Strings(expectedRequirements)
	sort.Strings(expectedArtifacts)
	return suiteProjection{
		Projected: projected,
		Expected: ExpectedSuite{
			ID: id, RequirementIDs: expectedRequirements, RequiredArtifacts: expectedArtifacts,
		},
		Mode: mode, Coverage: coverage, ExecutionKind: executionKind,
		TestPaths: testPaths, VerificationContractPath: verificationContractPath,
	}, nil
}

func optionalCriterionSource(object map[string]any) (map[string]any, bool, error) {
	raw, exists := object["criterionSource"]
	if !exists {
		return nil, false, nil
	}
	source, ok := raw.(map[string]any)
	if !ok || len(source) != 3 {
		return nil, false, errors.New("criterionSource must be an exact object")
	}
	path, err := objectString(source, "path")
	if err != nil || !validArtifactPath(path) {
		return nil, false, errors.New("criterionSource path is invalid")
	}
	schemaVersion, err := objectString(source, "schemaVersion")
	if err != nil || schemaVersion != ReferenceCriteriaSchemaV1 {
		return nil, false, errors.New("criterionSource schemaVersion is invalid")
	}
	applicationID, err := objectString(source, "applicationId")
	if err != nil || !validStableID(applicationID) {
		return nil, false, errors.New("criterionSource applicationId is invalid")
	}
	return map[string]any{
		"path": path, "schemaVersion": schemaVersion, "applicationId": applicationID,
	}, true, nil
}

func projectFiles(root string, paths []string, label string) ([]planFile, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s must not be empty", label)
	}
	result := make([]planFile, 0, len(paths))
	seen := map[string]struct{}{}
	for index, repositoryPath := range paths {
		absolute, relative, err := resolveRepositoryFile(root, repositoryPath)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", label, index, err)
		}
		if _, duplicate := seen[relative]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate path %q", label, relative)
		}
		seen[relative] = struct{}{}
		encoded, err := readBoundedRegularFile(absolute, maxIndexBytes, false)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relative, err)
		}
		result = append(result, planFile{Path: relative, SHA256: sha256Digest(encoded)})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result, nil
}

func extractDocumentedRequirementIDs(root string, paths []string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	for index, repositoryPath := range paths {
		absolute, _, err := resolveRepositoryFile(root, repositoryPath)
		if err != nil {
			return nil, fmt.Errorf("sourceDocuments[%d]: %w", index, err)
		}
		encoded, err := readBoundedRegularFile(absolute, maxIndexBytes, false)
		if err != nil {
			return nil, fmt.Errorf("read documented acceptance IDs from %s: %w", repositoryPath, err)
		}
		for _, match := range documentedRequirementPattern.FindAll(encoded, -1) {
			result[string(match)] = struct{}{}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("qualification source documents contain no acceptance IDs")
	}
	return result, nil
}

func resolveRepositoryFile(root, candidate string) (string, string, error) {
	if !validArtifactPath(candidate) {
		return "", "", errors.New("repository path is not normalized and relative")
	}
	absolute := filepath.Join(root, filepath.FromSlash(candidate))
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", errors.New("repository path escapes the root")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return "", "", errors.New("repository path must not contain symlink components")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("repository path must name an existing non-symlink regular file")
	}
	return absolute, candidate, nil
}

func validateTestInventory(
	root string,
	encoded []byte,
	externalSuites []ExpectedSuite,
	suitePaths map[string]map[string]struct{},
	suiteRequirements map[string][]string,
	suiteModes map[string]string,
	suiteExecutionKinds map[string]string,
	supportPaths map[string]struct{},
	requiredCriterionSources map[string]testInventoryCriterionSource,
) ([]ExpectedTestCase, error) {
	if err := requireExactShape(encoded, testInventoryShape()); err != nil {
		return nil, fmt.Errorf("validate qualification test inventory shape: %w", err)
	}
	var inventory testInventory
	if err := decodeStrictJSON(encoded, &inventory); err != nil {
		return nil, fmt.Errorf("decode qualification test inventory: %w", err)
	}
	if inventory.SchemaVersion != TestInventorySchemaV2 || len(inventory.CriterionSources) > maxCriterionSources ||
		len(inventory.Cases) == 0 || len(inventory.Cases) > maxTestInventoryCases {
		return nil, errors.New("qualification test inventory schema or cases are invalid")
	}
	if len(inventory.CriterionSources) != len(requiredCriterionSources) {
		return nil, errors.New("test inventory criterionSources do not exactly match the manifest-bound sources")
	}
	criteriaBySuite := make(map[string]map[string]struct{}, len(inventory.CriterionSources))
	for index, source := range inventory.CriterionSources {
		if !validStableID(source.SuiteID) || (index > 0 && inventory.CriterionSources[index-1].SuiteID >= source.SuiteID) {
			return nil, errors.New("test inventory criterionSources must be valid, unique, and sorted by suiteId")
		}
		mode, suiteExists := suiteModes[source.SuiteID]
		if !suiteExists || mode == "internal-regression" || suiteExecutionKinds[source.SuiteID] != "playwright" {
			return nil, fmt.Errorf("criterion source for %q references a non-Playwright qualification suite", source.SuiteID)
		}
		if source.SchemaVersion != ReferenceCriteriaSchemaV1 || !validStableID(source.ApplicationID) || !validArtifactPath(source.Path) {
			return nil, fmt.Errorf("criterion source for %q is non-canonical", source.SuiteID)
		}
		if _, supported := supportPaths[source.Path]; !supported {
			return nil, fmt.Errorf("criterion source for %q is not qualification-plan support material", source.SuiteID)
		}
		required, exists := requiredCriterionSources[source.SuiteID]
		if !exists || required != source {
			return nil, fmt.Errorf("criterion source for %q does not exactly match the manifest-bound source", source.SuiteID)
		}
		criteria, err := readReferenceAcceptanceCriteria(root, source)
		if err != nil {
			return nil, fmt.Errorf("criterion source for %q: %w", source.SuiteID, err)
		}
		criteriaBySuite[source.SuiteID] = criteria
	}
	for suiteID := range requiredCriterionSources {
		if _, exists := criteriaBySuite[suiteID]; !exists {
			return nil, fmt.Errorf("suite %q is missing its manifest-bound criterion source", suiteID)
		}
	}
	external := map[string]ExpectedSuite{}
	covered := map[string]map[string]struct{}{}
	coveredFiles := map[string]map[string]struct{}{}
	coveredCriteria := map[string]map[string]struct{}{}
	for _, suite := range externalSuites {
		external[suite.ID] = suite
		covered[suite.ID] = map[string]struct{}{}
		coveredFiles[suite.ID] = map[string]struct{}{}
		coveredCriteria[suite.ID] = map[string]struct{}{}
	}
	result := make([]ExpectedTestCase, 0, len(inventory.Cases))
	for index, testCase := range inventory.Cases {
		if !testCaseIDPattern.MatchString(testCase.CaseID) || (index > 0 && inventory.Cases[index-1].CaseID >= testCase.CaseID) {
			return nil, errors.New("test inventory caseId values must be valid, unique, and sorted")
		}
		mode, suiteExists := suiteModes[testCase.SuiteID]
		if !suiteExists || mode == "internal-regression" || suiteExecutionKinds[testCase.SuiteID] != "playwright" {
			return nil, fmt.Errorf("test inventory case %q references a non-Playwright qualification suite", testCase.CaseID)
		}
		if !validArtifactPath(testCase.File) || !goldenTestPathPattern.MatchString(testCase.File) ||
			!validCanonicalString(testCase.Title, 512) || !strings.HasPrefix(testCase.Title, testCase.CaseID+" ") ||
			(testCase.Mode != "partial-smoke" && testCase.Mode != "qualification") {
			return nil, fmt.Errorf("test inventory case %q has invalid file, title, or mode", testCase.CaseID)
		}
		declaredPaths, hasDeclaredPaths := suitePaths[testCase.SuiteID]
		if _, allowed := declaredPaths[testCase.File]; !hasDeclaredPaths || !allowed {
			return nil, fmt.Errorf("test inventory case %q names a file outside suite %q", testCase.CaseID, testCase.SuiteID)
		}
		absolute, _, err := resolveRepositoryFile(root, testCase.File)
		if err != nil {
			return nil, err
		}
		source, err := readBoundedRegularFile(absolute, 1<<20, false)
		if err != nil || !strings.Contains(string(source), testCase.Title) {
			return nil, fmt.Errorf("test inventory case %q exact title is absent from its source file", testCase.CaseID)
		}
		if !sortedUniqueStrings(testCase.RequirementIDs, requirementPattern.MatchString) {
			return nil, fmt.Errorf("test inventory case %q requirementIds are invalid", testCase.CaseID)
		}
		for _, requirementID := range testCase.RequirementIDs {
			if !containsString(suiteRequirements[testCase.SuiteID], requirementID) {
				return nil, fmt.Errorf("test inventory case %q covers a requirement outside its suite", testCase.CaseID)
			}
			if _, qualifies := external[testCase.SuiteID]; qualifies && testCase.Mode == "qualification" {
				covered[testCase.SuiteID][requirementID] = struct{}{}
			}
		}
		if len(testCase.ContractCriterionIDs) > 0 && !sortedUniqueStrings(testCase.ContractCriterionIDs, contractCriterionIDPattern.MatchString) {
			return nil, fmt.Errorf("test inventory case %q contractCriterionIds are invalid", testCase.CaseID)
		}
		sourceCriteria, hasCriterionSource := criteriaBySuite[testCase.SuiteID]
		if !hasCriterionSource && len(testCase.ContractCriterionIDs) != 0 {
			return nil, fmt.Errorf("test inventory case %q maps contract criteria without a suite criterion source", testCase.CaseID)
		}
		for _, criterionID := range testCase.ContractCriterionIDs {
			if _, exists := sourceCriteria[criterionID]; !exists {
				return nil, fmt.Errorf("test inventory case %q covers a criterion outside its suite", testCase.CaseID)
			}
			if _, qualifies := external[testCase.SuiteID]; qualifies && testCase.Mode == "qualification" {
				coveredCriteria[testCase.SuiteID][criterionID] = struct{}{}
			}
		}
		if _, qualifies := external[testCase.SuiteID]; qualifies && testCase.Mode == "qualification" {
			coveredFiles[testCase.SuiteID][testCase.File] = struct{}{}
			result = append(result, ExpectedTestCase{
				CaseID: testCase.CaseID, SuiteID: testCase.SuiteID,
				RequirementIDs:       append([]string(nil), testCase.RequirementIDs...),
				ContractCriterionIDs: append([]string(nil), testCase.ContractCriterionIDs...), File: testCase.File,
				Title: testCase.Title, Mode: testCase.Mode,
			})
		}
	}
	for _, suite := range externalSuites {
		for _, requirementID := range suiteRequirements[suite.ID] {
			if _, exists := covered[suite.ID][requirementID]; !exists {
				return nil, fmt.Errorf("test inventory does not cover %s in suite %s", requirementID, suite.ID)
			}
		}
		if len(coveredFiles[suite.ID]) != len(suitePaths[suite.ID]) {
			return nil, fmt.Errorf("test inventory does not close every declared test path in suite %s", suite.ID)
		}
		for testPath := range suitePaths[suite.ID] {
			if _, exists := coveredFiles[suite.ID][testPath]; !exists {
				return nil, fmt.Errorf("test inventory has no qualification case for %s in suite %s", testPath, suite.ID)
			}
		}
		for criterionID := range criteriaBySuite[suite.ID] {
			if _, exists := coveredCriteria[suite.ID][criterionID]; !exists {
				return nil, fmt.Errorf("test inventory does not cover contract criterion %s in suite %s", criterionID, suite.ID)
			}
		}
	}
	return result, nil
}

func readReferenceAcceptanceCriteria(root string, source testInventoryCriterionSource) (map[string]struct{}, error) {
	absolute, _, err := resolveRepositoryFile(root, source.Path)
	if err != nil {
		return nil, err
	}
	encoded, err := readBoundedRegularFile(absolute, 1<<20, false)
	if err != nil {
		return nil, err
	}
	if err := requireExactShape(encoded, referenceAcceptanceCriteriaShape()); err != nil {
		return nil, fmt.Errorf("validate reference acceptance criteria shape: %w", err)
	}
	var document referenceAcceptanceCriteria
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return nil, fmt.Errorf("decode reference acceptance criteria: %w", err)
	}
	if document.SchemaVersion != source.SchemaVersion || document.ApplicationID != source.ApplicationID ||
		len(document.Criteria) == 0 || len(document.Criteria) > maxContractCriteria {
		return nil, errors.New("reference acceptance criteria identity or cardinality is invalid")
	}
	result := make(map[string]struct{}, len(document.Criteria))
	for index, criterion := range document.Criteria {
		if !contractCriterionIDPattern.MatchString(criterion.ID) ||
			(index > 0 && document.Criteria[index-1].ID >= criterion.ID) ||
			!sortedUniqueStrings(criterion.RequirementIDs, contractRequirementIDPattern.MatchString) ||
			!validCanonicalString(criterion.Statement, 4096) {
			return nil, fmt.Errorf("reference acceptance criterion %d is non-canonical", index)
		}
		result[criterion.ID] = struct{}{}
	}
	return result, nil
}

func objectString(object map[string]any, key string) (string, error) {
	value, exists := object[key]
	text, ok := value.(string)
	if !exists || !ok || !validCanonicalString(text, 4096) {
		return "", fmt.Errorf("%s must be a canonical string", key)
	}
	return text, nil
}

func objectStringArray(object map[string]any, key string, validate func(string) bool) ([]string, error) {
	values, exists, err := optionalStringArray(object, key)
	if err != nil || !exists || len(values) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty string array", key)
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !validate(value) {
			return nil, fmt.Errorf("%s contains invalid value %q", key, value)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate value %q", key, value)
		}
		seen[value] = struct{}{}
	}
	return values, nil
}

func optionalString(object map[string]any, key string) (string, bool, error) {
	value, exists := object[key]
	if !exists {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok || !validCanonicalString(text, 4096) {
		return "", false, fmt.Errorf("%s must be a canonical string", key)
	}
	return text, true, nil
}

func optionalStringArray(object map[string]any, key string) ([]string, bool, error) {
	value, exists := object[key]
	if !exists {
		return nil, false, nil
	}
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		return nil, false, fmt.Errorf("%s must be a non-empty string array", key)
	}
	result := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		text, ok := item.(string)
		if !ok || !validCanonicalString(text, 4096) {
			return nil, false, fmt.Errorf("%s contains an invalid string", key)
		}
		if _, duplicate := seen[text]; duplicate {
			return nil, false, fmt.Errorf("%s contains duplicate %q", key, text)
		}
		seen[text] = struct{}{}
		result = append(result, text)
	}
	return result, true, nil
}

func canonicalJSONBytes(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
