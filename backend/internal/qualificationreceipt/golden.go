package qualificationreceipt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	GoldenAuthoritySchemaV2                  = "worksflow-golden-authority/v2"
	GoldenFixtureSchemaV2                    = "worksflow-golden-fixture/v2"
	GoldenFaultPayloadType                   = "application/vnd.worksflow.golden-fault-authority+json;version=1"
	GoldenFaultOperationSetSchemaV1          = "worksflow-golden-fault-operation-set/v1"
	GoldenFaultOperationSetDigestV1          = "sha256:50add6d13b4b28587f5ceab1385d85e457cc35489a031ac9d2f3ff217bd1fa9d"
	GoldenReferenceDeploymentReceiptSchemaV1 = "reference-deployment-runtime-receipt/v1"
	GoldenReferenceOperationSetSchemaV1      = "reference-qualification-operation-set/v1"
	GoldenReferenceOperationSetDigestV1      = "sha256:936f995189a3e6c89c740b6d693c4ba7e8b73b67db61bbf907f9c7fe8be0a2f8"

	maximumGoldenDocumentBytes  = 512 << 10
	goldenCredentialMemberCount = 11
)

func goldenReferenceOperationKindsV1() []string {
	return []string{
		"migration-rerun",
		"rate-limit-observation",
		"reference-audit-observation",
		"retention-job",
		"run-execution-observation",
		"timeout-vector",
	}
}

func goldenFaultOperationKindsV1() []string {
	return []string{
		"agent-runner-crash",
		"agent-runner-timeout",
		"agent-security-canary",
		"controller-conflict",
		"controller-maintenance",
		"controller-not-found",
		"controller-timeout",
		"lsp-resource-pressure",
		"lsp-runtime-crash",
		"lsp-runtime-drift",
		"reference-gateway-outage",
		"reference-process-restart",
		"sandbox-dependency-crash",
	}
}

func validateExpectedGoldenFaultOperationSet(faults []goldenFaultAuthority, expectedDigest string) error {
	expectedOperations := goldenFaultOperationKindsV1()
	commitmentDigest, err := goldenCanonicalDigest(map[string]any{
		"operations": expectedOperations, "schemaVersion": GoldenFaultOperationSetSchemaV1,
	})
	if err != nil || commitmentDigest != GoldenFaultOperationSetDigestV1 ||
		expectedDigest != commitmentDigest || len(faults) != len(expectedOperations) {
		return errors.New("Golden fixture does not contain the root-bound exact v1 fault-operation set")
	}
	actual := make(map[string]struct{}, len(faults))
	for _, fault := range faults {
		if _, duplicate := actual[fault.OperationKind]; duplicate {
			return errors.New("Golden fixture contains a duplicate fault operation")
		}
		actual[fault.OperationKind] = struct{}{}
	}
	for _, operation := range expectedOperations {
		if _, exists := actual[operation]; !exists {
			return fmt.Errorf("Golden fixture is missing root-required fault operation %q", operation)
		}
	}
	return nil
}

var (
	goldenVersionPattern         = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._+-]*[A-Za-z0-9])?$`)
	goldenServiceIdentityPattern = regexp.MustCompile(
		`^(?:spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+(?:[._/-][a-z0-9]+)*)$`,
	)
	goldenAudiencePattern  = regexp.MustCompile(`^(?:urn:[a-z0-9][a-z0-9:._/-]+|[a-z0-9]+(?:[._/-][a-z0-9]+)*)$`)
	goldenUUIDSearch       = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	goldenOriginHost       = regexp.MustCompile(`^(?:[a-z0-9.-]+|\[[0-9a-f:.]+\])(?::[1-9][0-9]{0,4})?$`)
	goldenCommandArgument  = regexp.MustCompile(`^[A-Za-z0-9./_+-][A-Za-z0-9._+/:=@%-]*$`)
	goldenWorkingDirectory = regexp.MustCompile(`^/(?:[a-z0-9][a-z0-9._-]*)(?:/[a-z0-9][a-z0-9._-]*)*$`)

	goldenPrincipalDefinitions = []struct {
		slot  string
		realm string
		role  string
	}{
		{slot: "fault-operator", realm: "control", role: "fault-operator"},
		{slot: "platform-admin", realm: "platform", role: "admin"},
		{slot: "platform-owner", realm: "platform", role: "owner"},
		{slot: "platform-user-a", realm: "platform", role: "user"},
		{slot: "platform-user-b", realm: "platform", role: "user"},
		{slot: "reference-user-a", realm: "reference", role: "user"},
		{slot: "reference-user-b", realm: "reference", role: "user"},
	}
	goldenCredentialDefinitions = []struct {
		slot          string
		principalSlot string
		kind          string
	}{
		{slot: "platform-admin", principalSlot: "platform-admin", kind: "token"},
		{slot: "platform-api-a", principalSlot: "platform-user-a", kind: "token"},
		{slot: "platform-api-b", principalSlot: "platform-user-b", kind: "token"},
		{slot: "platform-browser-a", principalSlot: "platform-user-a", kind: "storage-state"},
		{slot: "platform-browser-b", principalSlot: "platform-user-b", kind: "storage-state"},
		{slot: "platform-fault-operator", principalSlot: "fault-operator", kind: "token"},
		{slot: "platform-owner", principalSlot: "platform-owner", kind: "token"},
		{slot: "reference-api-a", principalSlot: "reference-user-a", kind: "storage-state"},
		{slot: "reference-api-b", principalSlot: "reference-user-b", kind: "storage-state"},
		{slot: "reference-browser-a", principalSlot: "reference-user-a", kind: "storage-state"},
		{slot: "reference-browser-b", principalSlot: "reference-user-b", kind: "storage-state"},
	}
	goldenRuntimeImageRoles = []string{
		"agent-runner", "language-server", "qualification-runner", "qualification-verifier", "release-controller", "sandbox-runner",
	}
)

type goldenAuthorityDocument struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Subject       goldenAuthoritySubject `json:"subject"`
}

type goldenAuthoritySubject struct {
	AuthorityID string `json:"authorityId"`
	ExpiresAt   string `json:"expiresAt"`
	FixtureHash string `json:"fixtureHash"`
	Issuance    string `json:"issuance"`
	IssuedAt    string `json:"issuedAt"`
	PlanDigest  string `json:"planDigest"`
	RunID       string `json:"runId"`
}

type goldenFixtureDocument struct {
	AuthorityHash string               `json:"authorityHash"`
	SchemaVersion string               `json:"schemaVersion"`
	Subject       goldenFixtureSubject `json:"subject"`
}

type goldenFixtureSubject struct {
	Agent            json.RawMessage            `json:"agent"`
	CredentialSet    goldenFixtureCredentialSet `json:"credentialSet"`
	ExpiresAt        string                     `json:"expiresAt"`
	FaultAuthorities []goldenFaultAuthority     `json:"faultAuthorities"`
	FixtureID        string                     `json:"fixtureId"`
	IssuedAt         string                     `json:"issuedAt"`
	LSP              json.RawMessage            `json:"lsp"`
	PlanDigest       string                     `json:"planDigest"`
	Platform         json.RawMessage            `json:"platform"`
	Principals       []goldenPrincipal          `json:"principals"`
	Reference        json.RawMessage            `json:"reference"`
	Release          json.RawMessage            `json:"release"`
	RunID            string                     `json:"runId"`
	Sandbox          json.RawMessage            `json:"sandbox"`
	SharedArtifacts  json.RawMessage            `json:"sharedArtifacts"`
}

type goldenFixtureCredentialSet struct {
	Audience                string                `json:"audience"`
	CredentialSetHandleHash string                `json:"credentialSetHandleHash"`
	ExpiresAt               string                `json:"expiresAt"`
	IssuedAt                string                `json:"issuedAt"`
	Issuer                  string                `json:"issuer"`
	IssuerAttestationDigest string                `json:"issuerAttestationDigest"`
	MemberBindings          []CredentialSetMember `json:"memberBindings"`
	MemberBindingsDigest    string                `json:"memberBindingsDigest"`
	MemberCount             int                   `json:"memberCount"`
	SetID                   string                `json:"setId"`
}

type goldenPrincipal struct {
	ActorID   string `json:"actorId"`
	ProjectID string `json:"projectId"`
	Realm     string `json:"realm"`
	Role      string `json:"role"`
	Slot      string `json:"slot"`
	TenantID  string `json:"tenantId"`
}

type goldenFaultAuthority struct {
	AuthorityID         string          `json:"authorityId"`
	DSSE                goldenFaultDSSE `json:"dsse"`
	ExpectedFenceDigest string          `json:"expectedFenceDigest"`
	MaxUses             int             `json:"maxUses"`
	OperationKind       string          `json:"operationKind"`
	ResourceSelector    string          `json:"resourceSelector"`
}

type goldenFaultDSSE struct {
	ArtifactID     string `json:"artifactId"`
	EnvelopeDigest string `json:"envelopeDigest"`
	PayloadDigest  string `json:"payloadDigest"`
	PayloadType    string `json:"payloadType"`
}

type parsedGoldenAuthority struct {
	document    goldenAuthorityDocument
	subjectHash string
	issuedAt    time.Time
	expiresAt   time.Time
}

type parsedGoldenFixture struct {
	document    goldenFixtureDocument
	subject     map[string]any
	subjectHash string
	issuedAt    time.Time
	expiresAt   time.Time
	faults      []goldenFaultAuthority
}

func verifyGoldenRuntimeDocuments(
	root string,
	receipt QualificationReceipt,
	expected ExpectedPromotion,
	artifacts verifiedArtifactSet,
) (parsedGoldenFixture, error) {
	authorityDescriptor, authorityExists := artifacts.byID[expected.GoldenRuntime.AuthorityDocumentArtifactID]
	fixtureDescriptor, fixtureExists := artifacts.byID[expected.GoldenRuntime.FixtureDocumentArtifactID]
	if !authorityExists || !fixtureExists {
		return parsedGoldenFixture{}, errors.New("root-pinned Golden authority or fixture document is missing")
	}
	authorityBytes, err := readVerifiedArtifact(root, authorityDescriptor, maximumGoldenDocumentBytes)
	if err != nil {
		return parsedGoldenFixture{}, fmt.Errorf("read Golden authority document: %w", err)
	}
	fixtureBytes, err := readVerifiedArtifact(root, fixtureDescriptor, maximumGoldenDocumentBytes)
	if err != nil {
		return parsedGoldenFixture{}, fmt.Errorf("read Golden fixture document: %w", err)
	}
	authority, err := parseGoldenAuthorityDocument(authorityBytes)
	if err != nil {
		return parsedGoldenFixture{}, err
	}
	fixture, err := parseGoldenFixtureDocument(fixtureBytes)
	if err != nil {
		return parsedGoldenFixture{}, err
	}
	if fixture.document.AuthorityHash != authority.subjectHash || authority.document.Subject.FixtureHash != fixture.subjectHash {
		return parsedGoldenFixture{}, errors.New("Golden authority and fixture bidirectional subject hashes do not close")
	}
	if authority.document.Subject.RunID != expected.RunID || fixture.document.Subject.RunID != expected.RunID ||
		authority.document.Subject.PlanDigest != expected.PlanDigest || fixture.document.Subject.PlanDigest != expected.PlanDigest ||
		fixture.document.Subject.FixtureID != expected.GoldenRuntime.FixtureID {
		return parsedGoldenFixture{}, errors.New("Golden authority or fixture does not bind the expected run, plan, and fixture identity")
	}
	if authority.document.Subject.IssuedAt != fixture.document.Subject.IssuedAt ||
		authority.document.Subject.ExpiresAt != fixture.document.Subject.ExpiresAt {
		return parsedGoldenFixture{}, errors.New("Golden authority and fixture time windows differ")
	}
	startedAt, _ := parseCanonicalTime(receipt.StartedAt, "receipt.startedAt")
	completedAt, _ := parseCanonicalTime(receipt.CompletedAt, "receipt.completedAt")
	if authority.issuedAt.After(startedAt) || !authority.expiresAt.After(completedAt) {
		return parsedGoldenFixture{}, errors.New("Golden authority does not cover the complete qualification run")
	}
	credential := fixture.document.Subject.CredentialSet
	if credential.Issuer != expected.CredentialSet.Issuer || credential.Audience != expected.CredentialSet.Audience ||
		credential.CredentialSetHandleHash != expected.CredentialSet.SetHandleHash ||
		credential.MemberBindingsDigest != expected.CredentialSet.MemberBindingsDigest ||
		credential.MemberCount != expected.CredentialSet.MemberCount ||
		credential.Issuer != receipt.CredentialSet.Issuer || credential.Audience != receipt.CredentialSet.Audience ||
		credential.CredentialSetHandleHash != receipt.CredentialSet.SetHandleHash ||
		credential.MemberBindingsDigest != receipt.CredentialSet.MemberBindingsDigest ||
		credential.MemberCount != receipt.CredentialSet.MemberCount ||
		credential.IssuedAt != receipt.CredentialSet.IssuedAt || credential.ExpiresAt != receipt.CredentialSet.ExpiresAt ||
		credential.IssuerAttestationDigest != receipt.CredentialSet.Issuance.PayloadDigest {
		return parsedGoldenFixture{}, errors.New("Golden fixture credential set does not close the authority, receipt, and issuance payload")
	}
	shared := goldenObject(fixture.subject, "sharedArtifacts")
	source := goldenObject(shared, "sourceRepository")
	templateRelease := goldenObject(shared, "templateRelease")
	buildContract := goldenObject(shared, "buildContract")
	workspaceRevision := goldenObject(shared, "workspaceRevision")
	if goldenString(source, "commitOid") != expected.Source.Commit ||
		goldenString(source, "contentTreeDigest") != expected.Source.TreeDigest ||
		goldenString(templateRelease, "id") != expected.TemplateRelease.ID ||
		goldenString(templateRelease, "contentHash") != expected.TemplateRelease.ContentHash ||
		goldenString(templateRelease, "approvalReceiptDigest") != expected.TemplateRelease.ApprovalReceiptDigest ||
		goldenString(buildContract, "contentHash") != expected.BuildContractHash ||
		goldenString(workspaceRevision, "id") != expected.PromotionTarget.TargetRevision.ID ||
		goldenString(workspaceRevision, "contentHash") != expected.PromotionTarget.TargetRevision.ContentHash {
		return parsedGoldenFixture{}, errors.New("Golden fixture source, TemplateRelease, BuildContract, or WorkspaceRevision authority drift")
	}
	return fixture, nil
}

func parseGoldenAuthorityDocument(encoded []byte) (parsedGoldenAuthority, error) {
	value, err := parseCanonicalGoldenDocument(encoded, goldenAuthorityShape(), "Golden authority")
	if err != nil {
		return parsedGoldenAuthority{}, err
	}
	var document goldenAuthorityDocument
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return parsedGoldenAuthority{}, fmt.Errorf("decode Golden authority: %w", err)
	}
	if document.SchemaVersion != GoldenAuthoritySchemaV2 || !validUUID(document.Subject.AuthorityID) ||
		document.Subject.Issuance != "root-issued-hash-bound" || !validUUID(document.Subject.RunID) ||
		!validDigest(document.Subject.PlanDigest) || !validDigest(document.Subject.FixtureHash) {
		return parsedGoldenAuthority{}, errors.New("Golden authority identity or schema is invalid")
	}
	issuedAt, err := parseCanonicalTime(document.Subject.IssuedAt, "Golden authority.subject.issuedAt")
	if err != nil {
		return parsedGoldenAuthority{}, err
	}
	expiresAt, err := parseCanonicalTime(document.Subject.ExpiresAt, "Golden authority.subject.expiresAt")
	if err != nil || expiresAt.Sub(issuedAt) < 2*time.Minute || expiresAt.Sub(issuedAt) > 30*time.Minute {
		return parsedGoldenAuthority{}, errors.New("Golden authority lifetime must be between 2 and 30 minutes")
	}
	subjectHash, err := goldenCanonicalDigest(value["subject"])
	if err != nil {
		return parsedGoldenAuthority{}, err
	}
	return parsedGoldenAuthority{document: document, subjectHash: subjectHash, issuedAt: issuedAt, expiresAt: expiresAt}, nil
}

func parseGoldenFixtureDocument(encoded []byte) (parsedGoldenFixture, error) {
	value, err := parseCanonicalGoldenDocument(encoded, goldenFixtureShape(), "Golden fixture")
	if err != nil {
		return parsedGoldenFixture{}, err
	}
	var document goldenFixtureDocument
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return parsedGoldenFixture{}, fmt.Errorf("decode Golden fixture: %w", err)
	}
	if document.SchemaVersion != GoldenFixtureSchemaV2 || !validDigest(document.AuthorityHash) ||
		!validUUID(document.Subject.FixtureID) || !validUUID(document.Subject.RunID) || !validDigest(document.Subject.PlanDigest) {
		return parsedGoldenFixture{}, errors.New("Golden fixture identity or schema is invalid")
	}
	issuedAt, err := parseCanonicalTime(document.Subject.IssuedAt, "Golden fixture.subject.issuedAt")
	if err != nil {
		return parsedGoldenFixture{}, err
	}
	expiresAt, err := parseCanonicalTime(document.Subject.ExpiresAt, "Golden fixture.subject.expiresAt")
	if err != nil || expiresAt.Sub(issuedAt) < 2*time.Minute || expiresAt.Sub(issuedAt) > 30*time.Minute {
		return parsedGoldenFixture{}, errors.New("Golden fixture lifetime must be between 2 and 30 minutes")
	}
	credential := document.Subject.CredentialSet
	credentialIssuedAt, err := parseCanonicalTime(credential.IssuedAt, "Golden fixture.subject.credentialSet.issuedAt")
	if err != nil {
		return parsedGoldenFixture{}, err
	}
	credentialExpiresAt, err := parseCanonicalTime(credential.ExpiresAt, "Golden fixture.subject.credentialSet.expiresAt")
	if err != nil || credentialIssuedAt.After(issuedAt) || credentialExpiresAt.Before(expiresAt) ||
		credentialExpiresAt.Sub(credentialIssuedAt) < 2*time.Minute || credentialExpiresAt.Sub(credentialIssuedAt) > 30*time.Minute {
		return parsedGoldenFixture{}, errors.New("Golden fixture credential set does not cover the fixture lifetime")
	}
	if !validUUID(credential.SetID) || !goldenServiceIdentityPattern.MatchString(credential.Issuer) ||
		!goldenAudiencePattern.MatchString(credential.Audience) || !validDigest(credential.CredentialSetHandleHash) ||
		!validDigest(credential.IssuerAttestationDigest) || credential.MemberCount != goldenCredentialMemberCount ||
		credential.MemberBindingsDigest == credential.CredentialSetHandleHash {
		return parsedGoldenFixture{}, errors.New("Golden fixture credential set authority is invalid")
	}
	if err := validateCredentialSetMembers(
		credential.MemberBindings,
		credential.MemberBindingsDigest,
		credential.MemberCount,
		credential.CredentialSetHandleHash,
	); err != nil {
		return parsedGoldenFixture{}, fmt.Errorf("validate Golden fixture credential members: %w", err)
	}
	if err := validateGoldenPrincipalsAndMembers(document.Subject.Principals, credential.MemberBindings); err != nil {
		return parsedGoldenFixture{}, err
	}
	if err := validateGoldenFixtureTopology(value["subject"].(map[string]any)); err != nil {
		return parsedGoldenFixture{}, err
	}
	subjectHash, err := goldenCanonicalDigest(value["subject"])
	if err != nil {
		return parsedGoldenFixture{}, err
	}
	return parsedGoldenFixture{
		document: document, subject: value["subject"].(map[string]any), subjectHash: subjectHash,
		issuedAt: issuedAt, expiresAt: expiresAt,
		faults: append([]goldenFaultAuthority(nil), document.Subject.FaultAuthorities...),
	}, nil
}

func parseCanonicalGoldenDocument(encoded []byte, shape jsonObjectShape, label string) (map[string]any, error) {
	if len(encoded) == 0 || len(encoded) > maximumGoldenDocumentBytes {
		return nil, fmt.Errorf("%s exceeds its size limit", label)
	}
	if err := requireExactShape(encoded, shape); err != nil {
		return nil, fmt.Errorf("validate %s exact shape: %w", label, err)
	}
	value, err := decodeJSONValue(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	canonical, err := canonicalJSONBytes(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize %s: %w", label, err)
	}
	if !bytes.Equal(encoded, canonical) && !bytes.Equal(encoded, append(append([]byte(nil), canonical...), '\n')) {
		return nil, fmt.Errorf("%s must use canonical JSON bytes", label)
	}
	return value.(map[string]any), nil
}

func goldenCanonicalDigest(value any) (string, error) {
	canonical, err := canonicalJSONBytes(value)
	if err != nil {
		return "", err
	}
	return sha256Digest(canonical), nil
}

func validateGoldenPrincipalsAndMembers(principals []goldenPrincipal, members []CredentialSetMember) error {
	if len(principals) != len(goldenPrincipalDefinitions) || len(members) != len(goldenCredentialDefinitions) {
		return errors.New("Golden fixture principal or credential slot closure is incomplete")
	}
	principalBySlot := make(map[string]goldenPrincipal, len(principals))
	actors := make(map[string]struct{}, len(principals))
	for index, principal := range principals {
		expected := goldenPrincipalDefinitions[index]
		if principal.Slot != expected.slot || principal.Realm != expected.realm || principal.Role != expected.role ||
			!validUUID(principal.ActorID) || !validUUID(principal.ProjectID) || !validUUID(principal.TenantID) {
			return fmt.Errorf("Golden fixture principal %d is non-canonical", index)
		}
		if _, duplicate := actors[principal.ActorID]; duplicate {
			return errors.New("Golden fixture principal actors must be unique")
		}
		actors[principal.ActorID] = struct{}{}
		principalBySlot[principal.Slot] = principal
	}
	for _, realm := range []string{"platform", "reference"} {
		left := principalBySlot[realm+"-user-a"]
		right := principalBySlot[realm+"-user-b"]
		if left.ProjectID == right.ProjectID || left.TenantID == right.TenantID {
			return fmt.Errorf("Golden fixture %s user A/B tenant and project boundaries must be distinct", realm)
		}
	}
	for index, member := range members {
		expected := goldenCredentialDefinitions[index]
		principal := principalBySlot[expected.principalSlot]
		if member.Slot != expected.slot || member.Kind != expected.kind || member.ActorID != principal.ActorID {
			return fmt.Errorf("Golden fixture credential member %d does not match its fixed slot, kind, and actor", index)
		}
	}
	return nil
}

func validateGoldenFixtureTopology(subject map[string]any) error {
	agent := goldenObject(subject, "agent")
	modelGateway := goldenObject(agent, "modelGateway")
	if !goldenDigest(modelGateway, "attestationDigest") || !goldenServiceIdentity(modelGateway, "identity") ||
		!goldenStable(modelGateway, "modelId") || !goldenStable(modelGateway, "modelRevision") ||
		!goldenStable(modelGateway, "profileId") || !goldenStable(modelGateway, "providerId") ||
		validateGoldenRunner(goldenObject(agent, "runner")) != nil {
		return errors.New("Golden fixture Agent topology is invalid")
	}
	platform := goldenObject(subject, "platform")
	platformAPI := goldenString(platform, "apiOrigin")
	platformWeb := goldenString(platform, "webOrigin")
	serverBuild := goldenObject(platform, "serverBuild")
	if !validGoldenHTTPSOrigin(platformAPI) || !validGoldenHTTPSOrigin(platformWeb) || platformAPI == platformWeb ||
		!goldenDigest(platform, "apiSchemaDigest") || !goldenDigest(platform, "wssProtocolDigest") ||
		validateGoldenIdentity(goldenObject(platform, "deploymentReceipt")) != nil ||
		!goldenStable(serverBuild, "buildId") || !goldenDigest(serverBuild, "imageDigest") ||
		!goldenVersionPattern.MatchString(goldenString(serverBuild, "version")) {
		return errors.New("Golden fixture platform topology is invalid")
	}
	reference := goldenObject(subject, "reference")
	referenceAPI := goldenString(reference, "apiOrigin")
	referenceWeb := goldenString(reference, "webOrigin")
	if err := validateGoldenReferenceAuthority(reference, modelGateway); err != nil {
		return err
	}
	origins := []string{platformAPI, platformWeb, referenceAPI, referenceWeb}
	seenOrigins := map[string]struct{}{}
	for _, origin := range origins {
		if _, duplicate := seenOrigins[origin]; duplicate {
			return errors.New("Golden fixture public origins must be distinct")
		}
		seenOrigins[origin] = struct{}{}
	}
	lsp := goldenObject(subject, "lsp")
	lspGateway := goldenObject(lsp, "gateway")
	lspRuntime := goldenObject(lsp, "runtime")
	if goldenString(lspGateway, "apiOrigin") != platformAPI || goldenString(lspGateway, "path") != "/v1/sandbox-lsp" ||
		!goldenDigest(lspGateway, "ticketProtocolDigest") || !goldenDigest(lspGateway, "wssProtocolDigest") ||
		!goldenDigest(lspRuntime, "capabilityDigest") || !goldenServiceIdentity(lspRuntime, "identity") ||
		!goldenDigest(lspRuntime, "imageDigest") || !goldenStable(lspRuntime, "profileId") ||
		!goldenSortedStrings(goldenStringArray(lspRuntime, "languages")) {
		return errors.New("Golden fixture LSP topology is invalid")
	}
	sandbox := goldenObject(subject, "sandbox")
	if goldenString(sandbox, "apiOrigin") != platformAPI || !goldenStable(sandbox, "runtimeProfileId") ||
		validateGoldenRunner(goldenObject(sandbox, "runner")) != nil || validateGoldenServiceProfiles(goldenArray(sandbox, "serviceProfiles")) != nil {
		return errors.New("Golden fixture Sandbox topology is invalid")
	}
	releaseController := goldenObject(goldenObject(subject, "release"), "controller")
	if !goldenServiceIdentity(releaseController, "identity") || !goldenDigest(releaseController, "imageDigest") ||
		!goldenStable(releaseController, "profileId") || !goldenStable(releaseController, "protocol") ||
		!goldenDigest(releaseController, "trustKeyDigest") {
		return errors.New("Golden fixture release controller topology is invalid")
	}
	internalIdentities := []string{
		goldenString(goldenObject(subject, "credentialSet"), "issuer"),
		goldenString(modelGateway, "identity"),
		goldenString(goldenObject(agent, "runner"), "identity"),
		goldenString(goldenObject(reference, "gateway"), "identity"),
		goldenString(goldenObject(sandbox, "runner"), "identity"),
		goldenString(releaseController, "identity"),
		goldenString(lspRuntime, "identity"),
	}
	seenIdentities := map[string]struct{}{}
	for _, identity := range internalIdentities {
		if _, duplicate := seenIdentities[identity]; duplicate {
			return errors.New("Golden fixture credential issuer and internal runtime identities must be role-distinct")
		}
		seenIdentities[identity] = struct{}{}
	}
	if err := validateGoldenFaultAuthorities(goldenArray(subject, "faultAuthorities")); err != nil {
		return err
	}
	shared := goldenObject(subject, "sharedArtifacts")
	for _, field := range []string{"buildContract", "buildManifest", "referenceContractBundle"} {
		if err := validateGoldenIdentity(goldenObject(shared, field)); err != nil {
			return fmt.Errorf("Golden fixture shared artifact %s is invalid", field)
		}
	}
	if goldenString(goldenObject(reference, "contractBundle"), "id") != goldenString(goldenObject(shared, "referenceContractBundle"), "id") ||
		goldenString(goldenObject(reference, "contractBundle"), "contentHash") != goldenString(goldenObject(shared, "referenceContractBundle"), "contentHash") {
		return errors.New("Golden fixture reference contract bundle binding drift")
	}
	if err := validateGoldenSharedAuthority(shared, agent, lspRuntime, releaseController, sandbox); err != nil {
		return err
	}
	return nil
}

func validateGoldenReferenceAuthority(reference, agentModelGateway map[string]any) error {
	referenceAPI := goldenString(reference, "apiOrigin")
	referenceWeb := goldenString(reference, "webOrigin")
	if !validGoldenHTTPSOrigin(referenceAPI) || !validGoldenHTTPSOrigin(referenceWeb) || referenceAPI == referenceWeb ||
		!goldenDigest(reference, "apiImageDigest") || !goldenDigest(reference, "webImageDigest") ||
		!goldenDigest(reference, "runEventSchemaDigest") || !validUUID(goldenString(reference, "applicationId")) ||
		validateGoldenIdentity(goldenObject(reference, "contractBundle")) != nil {
		return errors.New("Golden fixture Reference application topology is invalid")
	}

	deployment := goldenObject(reference, "deploymentReceipt")
	if !validUUID(goldenString(deployment, "id")) || !goldenDigest(deployment, "contentHash") ||
		goldenString(deployment, "schemaVersion") != GoldenReferenceDeploymentReceiptSchemaV1 {
		return errors.New("Golden fixture Reference deployment receipt kind or identity is invalid")
	}

	commands := goldenObject(reference, "commands")
	commandIdentities := make(map[string]struct{}, 4)
	for _, role := range []string{"api", "migration", "retention", "web"} {
		command := goldenObject(commands, role)
		identity := goldenString(command, "identity")
		if !validStableID(identity) || !goldenReferenceWorkingDirectory(goldenString(command, "workingDirectory")) ||
			!validGoldenReferenceArgv(goldenStringArray(command, "argv")) {
			return fmt.Errorf("Golden fixture Reference %s command is invalid or shell-ambiguous", role)
		}
		if _, duplicate := commandIdentities[identity]; duplicate {
			return errors.New("Golden fixture Reference command identities must be role-distinct")
		}
		commandIdentities[identity] = struct{}{}
	}

	gateway := goldenObject(reference, "gateway")
	modelProfile := goldenObject(gateway, "modelProfile")
	providerPolicy := goldenObject(gateway, "providerPolicy")
	secretInjection := goldenObject(gateway, "secretInjectionReceipt")
	maxAttempts, attemptsOK := goldenInteger(modelProfile, "maxAttempts")
	timeoutMilliseconds, timeoutOK := goldenInteger(modelProfile, "timeoutMilliseconds")
	if !validGoldenSPIFFEIdentity(goldenString(gateway, "identity")) || !goldenDigest(gateway, "attestationDigest") ||
		!goldenDigest(gateway, "capabilityDigest") || !goldenStable(gateway, "routeId") ||
		validateGoldenIdentity(secretInjection) != nil || !goldenDigest(modelProfile, "contentHash") ||
		!goldenStable(modelProfile, "id") || !goldenStable(modelProfile, "modelId") ||
		!goldenStable(modelProfile, "modelRevision") || !goldenStable(modelProfile, "providerId") ||
		!attemptsOK || maxAttempts != 3 || !timeoutOK || timeoutMilliseconds != 120000 ||
		!goldenDigest(providerPolicy, "contentHash") || goldenString(providerPolicy, "id") != "reference-project-default" ||
		!providerPolicy["profilePinned"].(bool) || providerPolicy["fallbackAllowed"].(bool) {
		return errors.New("Golden fixture independent Reference Gateway authority is invalid")
	}
	if goldenString(modelProfile, "id") == goldenString(agentModelGateway, "profileId") ||
		goldenString(modelProfile, "providerId") == goldenString(agentModelGateway, "providerId") {
		return errors.New("Golden fixture Reference profile and provider must not reuse Agent Model Gateway authority")
	}

	migration := goldenObject(reference, "migration")
	if !goldenDigest(migration, "contentHash") || !goldenStable(migration, "identity") {
		return errors.New("Golden fixture Reference migration is invalid")
	}
	if _, reused := commandIdentities[goldenString(migration, "identity")]; reused {
		return errors.New("Golden fixture Reference command and migration identities must not be reused")
	}
	if _, reused := commandIdentities[goldenString(gateway, "routeId")]; reused {
		return errors.New("Golden fixture Reference command and route identities must not be reused")
	}

	operationSet := goldenObject(reference, "qualificationOperationSet")
	expectedOperations := goldenReferenceOperationKindsV1()
	actualOperations := goldenStringArray(operationSet, "operations")
	if goldenString(operationSet, "schemaVersion") != GoldenReferenceOperationSetSchemaV1 ||
		len(actualOperations) != len(expectedOperations) {
		return errors.New("Golden fixture Reference qualification operation set is incomplete")
	}
	for index, operation := range actualOperations {
		if operation != expectedOperations[index] {
			return errors.New("Golden fixture Reference qualification operation set is not the closed v1 set")
		}
	}
	operationDigest, err := goldenCanonicalDigest(map[string]any{
		"operations": expectedOperations, "schemaVersion": GoldenReferenceOperationSetSchemaV1,
	})
	if err != nil || operationDigest != GoldenReferenceOperationSetDigestV1 ||
		goldenString(operationSet, "contentHash") != GoldenReferenceOperationSetDigestV1 {
		return errors.New("Golden fixture Reference qualification operation-set canonical hash drift")
	}

	rateLimit := goldenObject(reference, "rateLimitPolicy")
	burst, burstOK := goldenInteger(rateLimit, "burst")
	requests, requestsOK := goldenInteger(rateLimit, "requests")
	window, windowOK := goldenInteger(rateLimit, "windowSeconds")
	scopes := goldenStringArray(rateLimit, "scopes")
	if !goldenDigest(rateLimit, "contentHash") || goldenString(rateLimit, "id") != "reference-rate-limit-v1" ||
		!burstOK || burst != 10 || !requestsOK || requests != 60 || !windowOK || window != 60 ||
		len(scopes) != 2 || scopes[0] != "project" || scopes[1] != "tenant-actor" {
		return errors.New("Golden fixture Reference rate-limit policy is invalid")
	}

	retention := goldenObject(reference, "retentionPolicy")
	auditDays, auditOK := goldenInteger(retention, "auditDays")
	eventDays, eventOK := goldenInteger(retention, "eventDays")
	messageDays, messageOK := goldenInteger(retention, "messageDays")
	runDays, runOK := goldenInteger(retention, "runDays")
	if !validUUID(goldenString(retention, "id")) || !goldenDigest(retention, "contentHash") ||
		!auditOK || auditDays != 90 || !eventOK || eventDays != 30 || !messageOK || messageDays != 30 ||
		!runOK || runDays != 90 || !retention["redactionRequired"].(bool) {
		return errors.New("Golden fixture Reference retention policy is invalid")
	}

	commitments := []string{
		goldenString(deployment, "contentHash"),
		goldenString(gateway, "attestationDigest"),
		goldenString(gateway, "capabilityDigest"),
		goldenString(modelProfile, "contentHash"),
		goldenString(providerPolicy, "contentHash"),
		goldenString(secretInjection, "contentHash"),
		goldenString(migration, "contentHash"),
		goldenString(operationSet, "contentHash"),
		goldenString(rateLimit, "contentHash"),
		goldenString(retention, "contentHash"),
		goldenString(reference, "runEventSchemaDigest"),
	}
	seenCommitments := make(map[string]struct{}, len(commitments))
	for _, commitment := range commitments {
		if _, reused := seenCommitments[commitment]; reused {
			return errors.New("Golden fixture Reference receipt and policy commitments must be distinct")
		}
		seenCommitments[commitment] = struct{}{}
	}
	artifactIDs := []string{
		goldenString(goldenObject(reference, "contractBundle"), "id"),
		goldenString(deployment, "id"),
		goldenString(secretInjection, "id"),
		goldenString(retention, "id"),
	}
	seenArtifacts := make(map[string]struct{}, len(artifactIDs))
	for _, artifactID := range artifactIDs {
		if _, reused := seenArtifacts[artifactID]; reused {
			return errors.New("Golden fixture Reference artifact IDs must be distinct")
		}
		seenArtifacts[artifactID] = struct{}{}
	}
	return nil
}

func validGoldenReferenceArgv(argv []string) bool {
	if len(argv) < 1 || len(argv) > 16 {
		return false
	}
	for _, argument := range argv {
		if len(argument) == 0 || len(argument) > 256 || !goldenCommandArgument.MatchString(argument) ||
			strings.Contains(argument, "//") || argument == ".." || strings.HasPrefix(argument, "../") ||
			strings.HasSuffix(argument, "/..") || strings.Contains(argument, "/../") {
			return false
		}
	}
	segments := strings.Split(argv[0], "/")
	executable := strings.ToLower(segments[len(segments)-1])
	switch executable {
	case "sh", "bash", "dash", "zsh", "cmd", "cmd.exe", "env", "env.exe", "fish", "fish.exe",
		"powershell", "powershell.exe", "pwsh", "pwsh.exe", "sudo", "sudo.exe", "xargs", "xargs.exe":
		return false
	default:
		return true
	}
}

func goldenReferenceWorkingDirectory(value string) bool {
	return len(value) <= 512 && goldenWorkingDirectory.MatchString(value)
}

func validGoldenSPIFFEIdentity(value string) bool {
	if len(value) > 512 || strings.Contains(value, "%") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "spiffe" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" ||
		strings.ToLower(parsed.Host) != parsed.Host || parsed.String() != value || !strings.HasPrefix(parsed.Path, "/") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(segments) == 0 {
		return false
	}
	segmentPattern := regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	for _, segment := range segments {
		if !segmentPattern.MatchString(segment) {
			return false
		}
	}
	return true
}

func validateGoldenSharedAuthority(shared, agent, lspRuntime, releaseController, sandbox map[string]any) error {
	source := goldenObject(shared, "sourceRepository")
	if !commitPattern.MatchString(goldenString(source, "commitOid")) || !goldenDigest(source, "contentTreeDigest") {
		return errors.New("Golden fixture source repository binding is invalid")
	}
	templateRelease := goldenObject(shared, "templateRelease")
	if !validUUID(goldenString(templateRelease, "id")) || !goldenDigest(templateRelease, "contentHash") ||
		!goldenDigest(templateRelease, "approvalReceiptDigest") {
		return errors.New("Golden fixture TemplateRelease binding is invalid")
	}
	workspace := goldenObject(shared, "workspaceRevision")
	if !validUUID(goldenString(workspace, "id")) || !goldenDigest(workspace, "contentHash") ||
		!goldenDigest(workspace, "canonicalQualityReceiptDigest") {
		return errors.New("Golden fixture WorkspaceRevision binding is invalid")
	}
	images := goldenArray(shared, "runtimeImages")
	if len(images) != len(goldenRuntimeImageRoles) {
		return errors.New("Golden fixture runtime image closure is incomplete")
	}
	byRole := map[string]string{}
	for index, raw := range images {
		image := raw.(map[string]any)
		role := goldenString(image, "role")
		if role != goldenRuntimeImageRoles[index] || !goldenDigest(image, "imageDigest") ||
			validateGoldenIdentity(goldenObject(image, "provenance")) != nil ||
			validateGoldenIdentity(goldenObject(image, "sbom")) != nil ||
			validateGoldenIdentity(goldenObject(image, "signature")) != nil {
			return fmt.Errorf("Golden fixture runtime image %d is invalid", index)
		}
		byRole[role] = goldenString(image, "imageDigest")
	}
	expected := map[string]string{
		"agent-runner":       goldenString(goldenObject(agent, "runner"), "imageDigest"),
		"language-server":    goldenString(lspRuntime, "imageDigest"),
		"release-controller": goldenString(releaseController, "imageDigest"),
		"sandbox-runner":     goldenString(goldenObject(sandbox, "runner"), "imageDigest"),
	}
	for role, digest := range expected {
		if byRole[role] != digest {
			return fmt.Errorf("Golden fixture approved image binding drift for %s", role)
		}
	}
	return nil
}

func validateGoldenFaultAuthorities(values []any) error {
	if len(values) < 1 || len(values) > 64 {
		return errors.New("Golden fixture must declare 1..64 fault authorities")
	}
	operations := map[string]struct{}{}
	artifactIDs := map[string]struct{}{}
	envelopeDigests := map[string]struct{}{}
	payloadDigests := map[string]struct{}{}
	priorID := ""
	for index, raw := range values {
		authority := raw.(map[string]any)
		authorityID := goldenString(authority, "authorityId")
		operation := goldenString(authority, "operationKind")
		expectedSelector, _, supported := goldenFaultContract(operation)
		selector := goldenString(authority, "resourceSelector")
		dsse := goldenObject(authority, "dsse")
		artifactID := goldenString(dsse, "artifactId")
		envelopeDigest := goldenString(dsse, "envelopeDigest")
		payloadDigest := goldenString(dsse, "payloadDigest")
		maxUses, ok := goldenInteger(authority, "maxUses")
		if !validUUID(authorityID) || (index > 0 && priorID >= authorityID) || !supported || !ok || maxUses != 1 ||
			!validUUID(artifactID) || !validDigest(envelopeDigest) || !validDigest(payloadDigest) || envelopeDigest == payloadDigest ||
			goldenString(dsse, "payloadType") != GoldenFaultPayloadType || !goldenDigest(authority, "expectedFenceDigest") ||
			selector != expectedSelector ||
			goldenUUIDSearch.MatchString(selector) {
			return fmt.Errorf("Golden fixture fault authority %d is invalid", index)
		}
		if _, duplicate := operations[operation]; duplicate {
			return errors.New("Golden fixture fault operation kinds must be unique")
		}
		if _, duplicate := artifactIDs[artifactID]; duplicate {
			return errors.New("Golden fixture fault DSSE artifact IDs must be unique")
		}
		if _, duplicate := envelopeDigests[envelopeDigest]; duplicate {
			return errors.New("Golden fixture fault DSSE envelope digests must be unique")
		}
		if _, duplicate := payloadDigests[payloadDigest]; duplicate {
			return errors.New("Golden fixture fault DSSE payload digests must be unique")
		}
		operations[operation] = struct{}{}
		artifactIDs[artifactID] = struct{}{}
		envelopeDigests[envelopeDigest] = struct{}{}
		payloadDigests[payloadDigest] = struct{}{}
		priorID = authorityID
	}
	return nil
}

func goldenFaultContract(operation string) (selector, expectedOutcome string, ok bool) {
	switch operation {
	case "agent-runner-crash", "agent-runner-timeout":
		return "agent.runner", "applied", true
	case "agent-security-canary":
		return "agent.patch-policy", "refused", true
	case "controller-conflict", "controller-maintenance", "controller-not-found", "controller-timeout":
		return "release.controller", "applied", true
	case "lsp-resource-pressure", "lsp-runtime-crash", "lsp-runtime-drift":
		return "lsp.runtime", "applied", true
	case "reference-gateway-outage":
		return "reference.gateway", "applied", true
	case "reference-process-restart":
		return "reference.process", "applied", true
	case "sandbox-dependency-crash":
		return "sandbox.dependency", "applied", true
	default:
		return "", "", false
	}
}

func validateGoldenServiceProfiles(values []any) error {
	if len(values) < 1 || len(values) > 32 {
		return errors.New("Golden Sandbox must declare 1..32 service profiles")
	}
	priorID := ""
	for index, raw := range values {
		profile := raw.(map[string]any)
		id := goldenString(profile, "id")
		protocol := goldenString(profile, "protocol")
		if !validStableID(id) || (index > 0 && priorID >= id) || !goldenDigest(profile, "imageDigest") ||
			(protocol != "http" && protocol != "websocket") || !goldenStable(profile, "service") {
			return fmt.Errorf("Golden Sandbox service profile %d is invalid", index)
		}
		priorID = id
	}
	return nil
}

func validateGoldenRunner(value map[string]any) error {
	if !goldenServiceIdentity(value, "identity") || !goldenDigest(value, "imageDigest") || !goldenStable(value, "profileId") {
		return errors.New("Golden runtime runner is invalid")
	}
	return nil
}

func validateGoldenIdentity(value map[string]any) error {
	if !validUUID(goldenString(value, "id")) || !goldenDigest(value, "contentHash") {
		return errors.New("Golden artifact identity is invalid")
	}
	return nil
}

func validGoldenHTTPSOrigin(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Path == "" &&
		parsed.RawPath == "" && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Opaque == "" &&
		parsed.Port() != "443" && strings.ToLower(parsed.Host) == parsed.Host && goldenOriginHost.MatchString(parsed.Host) &&
		"https://"+parsed.Host == value
}

func goldenObject(object map[string]any, key string) map[string]any {
	return object[key].(map[string]any)
}
func goldenArray(object map[string]any, key string) []any   { return object[key].([]any) }
func goldenString(object map[string]any, key string) string { return object[key].(string) }

func goldenStringArray(object map[string]any, key string) []string {
	raw := goldenArray(object, key)
	result := make([]string, len(raw))
	for index, value := range raw {
		result[index] = value.(string)
	}
	return result
}

func goldenInteger(object map[string]any, key string) (int64, bool) {
	value, ok := object[key].(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := value.Int64()
	return integer, err == nil
}

func goldenDigest(object map[string]any, key string) bool {
	return validDigest(goldenString(object, key))
}
func goldenStable(object map[string]any, key string) bool {
	return validStableID(goldenString(object, key))
}
func goldenServiceIdentity(object map[string]any, key string) bool {
	return goldenServiceIdentityPattern.MatchString(goldenString(object, key))
}

func goldenSortedStrings(values []string) bool {
	if len(values) == 0 || len(values) > 32 {
		return false
	}
	for index, value := range values {
		if !validCanonicalString(value, 256) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func goldenAuthorityShape() jsonObjectShape {
	subjectFields := []string{"authorityId", "expiresAt", "fixtureHash", "issuance", "issuedAt", "planDigest", "runId"}
	return jsonObjectShape{
		required: []string{"schemaVersion", "subject"}, strings: []string{"schemaVersion"},
		objects: map[string]jsonObjectShape{"subject": {required: subjectFields, strings: subjectFields}},
	}
}

func goldenFixtureShape() jsonObjectShape {
	identity := jsonObjectShape{required: []string{"contentHash", "id"}, strings: []string{"contentHash", "id"}}
	runner := jsonObjectShape{required: []string{"identity", "imageDigest", "profileId"}, strings: []string{"identity", "imageDigest", "profileId"}}
	credentialMember := jsonObjectShape{
		required: []string{"actorId", "credentialHandleHash", "kind", "slot"},
		strings:  []string{"actorId", "credentialHandleHash", "kind", "slot"},
	}
	credentialSet := jsonObjectShape{
		required: []string{
			"audience", "credentialSetHandleHash", "expiresAt", "issuedAt", "issuer", "issuerAttestationDigest",
			"memberBindings", "memberBindingsDigest", "memberCount", "setId",
		},
		strings: []string{
			"audience", "credentialSetHandleHash", "expiresAt", "issuedAt", "issuer", "issuerAttestationDigest", "memberBindingsDigest", "setId",
		},
		numbers:      []string{"memberCount"},
		objectArrays: map[string]jsonObjectShape{"memberBindings": credentialMember},
	}
	principal := jsonObjectShape{
		required: []string{"actorId", "projectId", "realm", "role", "slot", "tenantId"},
		strings:  []string{"actorId", "projectId", "realm", "role", "slot", "tenantId"},
	}
	faultDSSE := jsonObjectShape{
		required: []string{"artifactId", "envelopeDigest", "payloadDigest", "payloadType"},
		strings:  []string{"artifactId", "envelopeDigest", "payloadDigest", "payloadType"},
	}
	fault := jsonObjectShape{
		required: []string{"authorityId", "dsse", "expectedFenceDigest", "maxUses", "operationKind", "resourceSelector"},
		strings:  []string{"authorityId", "expectedFenceDigest", "operationKind", "resourceSelector"}, numbers: []string{"maxUses"},
		objects: map[string]jsonObjectShape{"dsse": faultDSSE},
	}
	modelGateway := jsonObjectShape{
		required: []string{"attestationDigest", "identity", "modelId", "modelRevision", "profileId", "providerId"},
		strings:  []string{"attestationDigest", "identity", "modelId", "modelRevision", "profileId", "providerId"},
	}
	agent := jsonObjectShape{
		required: []string{"modelGateway", "runner"},
		objects:  map[string]jsonObjectShape{"modelGateway": modelGateway, "runner": runner},
	}
	platformBuild := jsonObjectShape{required: []string{"buildId", "imageDigest", "version"}, strings: []string{"buildId", "imageDigest", "version"}}
	platform := jsonObjectShape{
		required: []string{"apiOrigin", "apiSchemaDigest", "deploymentReceipt", "serverBuild", "webOrigin", "wssProtocolDigest"},
		strings:  []string{"apiOrigin", "apiSchemaDigest", "webOrigin", "wssProtocolDigest"},
		objects:  map[string]jsonObjectShape{"deploymentReceipt": identity, "serverBuild": platformBuild},
	}
	lspGateway := jsonObjectShape{
		required: []string{"apiOrigin", "path", "ticketProtocolDigest", "wssProtocolDigest"},
		strings:  []string{"apiOrigin", "path", "ticketProtocolDigest", "wssProtocolDigest"},
	}
	lspRuntime := jsonObjectShape{
		required: []string{"capabilityDigest", "identity", "imageDigest", "languages", "profileId"},
		strings:  []string{"capabilityDigest", "identity", "imageDigest", "profileId"}, stringArrays: []string{"languages"},
	}
	lsp := jsonObjectShape{required: []string{"gateway", "runtime"}, objects: map[string]jsonObjectShape{"gateway": lspGateway, "runtime": lspRuntime}}
	referenceCommand := jsonObjectShape{
		required: []string{"argv", "identity", "workingDirectory"},
		strings:  []string{"identity", "workingDirectory"}, stringArrays: []string{"argv"},
	}
	referenceCommands := jsonObjectShape{
		required: []string{"api", "migration", "retention", "web"},
		objects: map[string]jsonObjectShape{
			"api": referenceCommand, "migration": referenceCommand, "retention": referenceCommand, "web": referenceCommand,
		},
	}
	referenceDeploymentReceipt := jsonObjectShape{
		required: []string{"contentHash", "id", "schemaVersion"}, strings: []string{"contentHash", "id", "schemaVersion"},
	}
	referenceProviderPolicy := jsonObjectShape{
		required: []string{"contentHash", "fallbackAllowed", "id", "profilePinned"},
		strings:  []string{"contentHash", "id"}, booleans: []string{"fallbackAllowed", "profilePinned"},
	}
	referenceModelProfile := jsonObjectShape{
		required: []string{"contentHash", "id", "maxAttempts", "modelId", "modelRevision", "providerId", "timeoutMilliseconds"},
		strings:  []string{"contentHash", "id", "modelId", "modelRevision", "providerId"},
		numbers:  []string{"maxAttempts", "timeoutMilliseconds"},
	}
	referenceGateway := jsonObjectShape{
		required: []string{
			"attestationDigest", "capabilityDigest", "identity", "modelProfile", "providerPolicy", "routeId", "secretInjectionReceipt",
		},
		strings: []string{"attestationDigest", "capabilityDigest", "identity", "routeId"},
		objects: map[string]jsonObjectShape{
			"modelProfile": referenceModelProfile, "providerPolicy": referenceProviderPolicy, "secretInjectionReceipt": identity,
		},
	}
	referenceMigration := jsonObjectShape{required: []string{"contentHash", "identity"}, strings: []string{"contentHash", "identity"}}
	referenceOperationSet := jsonObjectShape{
		required: []string{"contentHash", "operations", "schemaVersion"}, strings: []string{"contentHash", "schemaVersion"},
		stringArrays: []string{"operations"},
	}
	referenceRateLimitPolicy := jsonObjectShape{
		required: []string{"burst", "contentHash", "id", "requests", "scopes", "windowSeconds"},
		strings:  []string{"contentHash", "id"}, stringArrays: []string{"scopes"}, numbers: []string{"burst", "requests", "windowSeconds"},
	}
	referenceRetentionPolicy := jsonObjectShape{
		required: []string{"auditDays", "contentHash", "eventDays", "id", "messageDays", "redactionRequired", "runDays"},
		strings:  []string{"contentHash", "id"}, booleans: []string{"redactionRequired"},
		numbers: []string{"auditDays", "eventDays", "messageDays", "runDays"},
	}
	reference := jsonObjectShape{
		required: []string{
			"apiImageDigest", "apiOrigin", "applicationId", "commands", "contractBundle", "deploymentReceipt", "gateway", "migration",
			"qualificationOperationSet", "rateLimitPolicy", "retentionPolicy", "runEventSchemaDigest", "webImageDigest", "webOrigin",
		},
		strings: []string{"apiImageDigest", "apiOrigin", "applicationId", "runEventSchemaDigest", "webImageDigest", "webOrigin"},
		objects: map[string]jsonObjectShape{
			"commands": referenceCommands, "contractBundle": identity, "deploymentReceipt": referenceDeploymentReceipt,
			"gateway": referenceGateway, "migration": referenceMigration, "qualificationOperationSet": referenceOperationSet,
			"rateLimitPolicy": referenceRateLimitPolicy, "retentionPolicy": referenceRetentionPolicy,
		},
	}
	releaseController := jsonObjectShape{
		required: []string{"identity", "imageDigest", "profileId", "protocol", "trustKeyDigest"},
		strings:  []string{"identity", "imageDigest", "profileId", "protocol", "trustKeyDigest"},
	}
	release := jsonObjectShape{required: []string{"controller"}, objects: map[string]jsonObjectShape{"controller": releaseController}}
	serviceProfile := jsonObjectShape{
		required: []string{"id", "imageDigest", "protocol", "service"}, strings: []string{"id", "imageDigest", "protocol", "service"},
	}
	sandbox := jsonObjectShape{
		required: []string{"apiOrigin", "runner", "runtimeProfileId", "serviceProfiles"}, strings: []string{"apiOrigin", "runtimeProfileId"},
		objects: map[string]jsonObjectShape{"runner": runner}, objectArrays: map[string]jsonObjectShape{"serviceProfiles": serviceProfile},
	}
	runtimeImage := jsonObjectShape{
		required: []string{"imageDigest", "provenance", "role", "sbom", "signature"}, strings: []string{"imageDigest", "role"},
		objects: map[string]jsonObjectShape{"provenance": identity, "sbom": identity, "signature": identity},
	}
	sourceRepository := jsonObjectShape{required: []string{"commitOid", "contentTreeDigest"}, strings: []string{"commitOid", "contentTreeDigest"}}
	templateRelease := jsonObjectShape{
		required: []string{"approvalReceiptDigest", "contentHash", "id"}, strings: []string{"approvalReceiptDigest", "contentHash", "id"},
	}
	workspaceRevision := jsonObjectShape{
		required: []string{"canonicalQualityReceiptDigest", "contentHash", "id"}, strings: []string{"canonicalQualityReceiptDigest", "contentHash", "id"},
	}
	shared := jsonObjectShape{
		required: []string{
			"buildContract", "buildManifest", "referenceContractBundle", "runtimeImages", "sourceRepository", "templateRelease", "workspaceRevision",
		},
		objects: map[string]jsonObjectShape{
			"buildContract": identity, "buildManifest": identity, "referenceContractBundle": identity,
			"sourceRepository": sourceRepository, "templateRelease": templateRelease, "workspaceRevision": workspaceRevision,
		},
		objectArrays: map[string]jsonObjectShape{"runtimeImages": runtimeImage},
	}
	subject := jsonObjectShape{
		required: []string{
			"agent", "credentialSet", "expiresAt", "faultAuthorities", "fixtureId", "issuedAt", "lsp", "planDigest",
			"platform", "principals", "reference", "release", "runId", "sandbox", "sharedArtifacts",
		},
		strings: []string{"expiresAt", "fixtureId", "issuedAt", "planDigest", "runId"},
		objects: map[string]jsonObjectShape{
			"agent": agent, "credentialSet": credentialSet, "lsp": lsp, "platform": platform,
			"reference": reference, "release": release, "sandbox": sandbox, "sharedArtifacts": shared,
		},
		objectArrays: map[string]jsonObjectShape{"faultAuthorities": fault, "principals": principal},
	}
	return jsonObjectShape{
		required: []string{"authorityHash", "schemaVersion", "subject"}, strings: []string{"authorityHash", "schemaVersion"},
		objects: map[string]jsonObjectShape{"subject": subject},
	}
}
