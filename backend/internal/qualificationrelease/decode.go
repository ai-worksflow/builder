package qualificationrelease

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maximumCapabilityBundleBytes = 2 << 20

type exactMaterial struct {
	Hash     string          `json:"hash"`
	BytesHex string          `json:"bytesHex"`
	Document json.RawMessage `json:"document"`
}

func (material exactMaterial) validate(maximumBytes int) error {
	if !exactHashPattern.MatchString(material.Hash) || material.BytesHex == "" ||
		len(material.BytesHex) > maximumBytes*2 || len(material.BytesHex)%2 != 0 ||
		len(material.Document) == 0 || bytes.Equal(bytes.TrimSpace(material.Document), []byte("null")) {
		return ErrConflict
	}
	decoded, err := hex.DecodeString(material.BytesHex)
	if err != nil || len(decoded) == 0 || len(decoded) > maximumBytes {
		return ErrConflict
	}
	return nil
}

type authorizationBundleWire struct {
	SchemaVersion   string        `json:"schemaVersion"`
	OperationID     string        `json:"operationId"`
	AuthorizationID string        `json:"authorizationId"`
	Request         exactMaterial `json:"request"`
	Equivalence     exactMaterial `json:"equivalence"`
	Authorization   exactMaterial `json:"authorization"`
	Idempotent      *bool         `json:"idempotent,omitempty"`
}

type authorizationDocumentWire struct {
	AuthorizationID string `json:"authorizationId"`
	AuthorizedAt    string `json:"authorizedAt"`
	EquivalenceHash string `json:"equivalenceHash"`
	OperationID     string `json:"operationId"`
	Release         struct {
		BuildContract           exactReferenceWire `json:"buildContract"`
		BuildManifest           exactReferenceWire `json:"buildManifest"`
		CanonicalReceipt        exactReferenceWire `json:"canonicalReceipt"`
		ExpectedProductionRunID string             `json:"expectedProductionRunId"`
		PreviewReceipt          exactReferenceWire `json:"previewReceipt"`
		PromotionApproval       exactReferenceWire `json:"promotionApproval"`
		ReleaseBundle           exactReferenceWire `json:"releaseBundle"`
		RequestKey              string             `json:"requestKey"`
	} `json:"release"`
	SchemaVersion string `json:"schemaVersion"`
	Workflow      struct {
		ActionEvent      json.RawMessage `json:"actionEvent"`
		Actor            json.RawMessage `json:"actor"`
		ExecutionProfile struct {
			Hash    string `json:"hash"`
			Version string `json:"version"`
		} `json:"executionProfile"`
		ProjectID        string `json:"projectId"`
		PublishNodeKey   string `json:"publishNodeKey"`
		PublishNodeRunID string `json:"publishNodeRunId"`
		WorkflowRunID    string `json:"workflowRunId"`
	} `json:"workflow"`
}

type exactReferenceWire struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

func decodeAuthorizationBundle(encoded []byte, target Target) (Authorization, error) {
	var bundle authorizationBundleWire
	if err := decodeExactJSON(encoded, &bundle); err != nil ||
		bundle.SchemaVersion != "worksflow-qualification-release-authorization-bundle/v1" ||
		bundle.Request.validate(64<<10) != nil || bundle.Equivalence.validate(1<<20) != nil ||
		bundle.Authorization.validate(1<<20) != nil {
		return Authorization{}, ErrConflict
	}
	var document authorizationDocumentWire
	if err := decodeExactJSON(bundle.Authorization.Document, &document); err != nil ||
		document.SchemaVersion != "worksflow-qualification-release-authorization/v1" ||
		document.Workflow.ExecutionProfile.Version != WorkflowExecutionProfileVersion ||
		document.Workflow.ExecutionProfile.Hash != WorkflowExecutionProfileHash ||
		document.Workflow.PublishNodeKey == "" ||
		document.EquivalenceHash != bundle.Equivalence.Hash ||
		document.AuthorizationID != bundle.AuthorizationID || document.OperationID != bundle.OperationID {
		return Authorization{}, ErrConflict
	}
	authorizationID, err := parseUUID(document.AuthorizationID)
	if err != nil || !validUUIDv4(authorizationID) {
		return Authorization{}, ErrConflict
	}
	operationID, err := parseUUID(document.OperationID)
	if err != nil || !validUUIDv4(operationID) {
		return Authorization{}, ErrConflict
	}
	projectID, err := parseUUID(document.Workflow.ProjectID)
	if err != nil {
		return Authorization{}, ErrConflict
	}
	workflowRunID, err := parseUUID(document.Workflow.WorkflowRunID)
	if err != nil {
		return Authorization{}, ErrConflict
	}
	publishNodeRunID, err := parseUUID(document.Workflow.PublishNodeRunID)
	if err != nil {
		return Authorization{}, ErrConflict
	}
	productionRunID, err := parseUUID(document.Release.ExpectedProductionRunID)
	if err != nil || !validUUIDv4(productionRunID) {
		return Authorization{}, ErrConflict
	}
	if _, err := parseMillisecondTimestamp(document.AuthorizedAt); err != nil ||
		!boundedText(document.Release.RequestKey, 128) ||
		validateExactReferences(document.Release.BuildContract, document.Release.BuildManifest,
			document.Release.CanonicalReceipt, document.Release.PreviewReceipt,
			document.Release.PromotionApproval, document.Release.ReleaseBundle) != nil {
		return Authorization{}, ErrConflict
	}
	authorization := Authorization{
		AuthorizationID: authorizationID, OperationID: operationID, ProjectID: projectID,
		WorkflowRunID: workflowRunID, PublishNodeRunID: publishNodeRunID,
		ExpectedProductionRunID: productionRunID, AuthorizationHash: bundle.Authorization.Hash,
		AuthorizationDocument: append(json.RawMessage(nil), bundle.Authorization.Document...),
	}
	if err := authorization.ValidateFor(target); err != nil {
		return Authorization{}, err
	}
	return authorization, nil
}

func validateExactReferences(references ...exactReferenceWire) error {
	for _, reference := range references {
		if !validUUIDString(reference.ID) || !exactHashPattern.MatchString(reference.Hash) {
			return ErrConflict
		}
	}
	return nil
}

type claimBundleWire struct {
	SchemaVersion       string        `json:"schemaVersion"`
	ClaimEventID        string        `json:"claimEventId"`
	Active              bool          `json:"active"`
	CurrentLeaseExpires *string       `json:"currentLeaseExpiresAt"`
	Claim               exactMaterial `json:"claim"`
	Idempotent          *bool         `json:"idempotent,omitempty"`
}

type claimDocumentWire struct {
	AuthorizationID string `json:"authorizationId"`
	ClaimEvent      struct {
		ID       string `json:"id"`
		Sequence int64  `json:"sequence"`
		Type     string `json:"type"`
	} `json:"claimEvent"`
	ClaimedAt string `json:"claimedAt"`
	Lease     struct {
		Attempt              int    `json:"attempt"`
		DurationMilliseconds int    `json:"durationMilliseconds"`
		InitialExpiresAt     string `json:"initialExpiresAt"`
		Owner                string `json:"owner"`
	} `json:"lease"`
	SchemaVersion string          `json:"schemaVersion"`
	Workflow      json.RawMessage `json:"workflow"`
}

func decodeClaimBundle(encoded []byte, authorization Authorization, expectedClaimID uuid.UUID) (Claim, error) {
	var bundle claimBundleWire
	if err := decodeExactJSON(encoded, &bundle); err != nil ||
		bundle.SchemaVersion != "worksflow-qualification-release-lease-claim-bundle/v1" ||
		bundle.Claim.validate(64<<10) != nil {
		return Claim{}, ErrConflict
	}
	claimID, err := parseUUID(bundle.ClaimEventID)
	if err != nil || !validUUIDv4(claimID) || claimID != expectedClaimID {
		return Claim{}, ErrConflict
	}
	var document claimDocumentWire
	if err := decodeExactJSON(bundle.Claim.Document, &document); err != nil ||
		document.SchemaVersion != "worksflow-qualification-release-lease-claim/v1" ||
		document.AuthorizationID != authorization.AuthorizationID.String() ||
		document.ClaimEvent.ID != bundle.ClaimEventID || document.ClaimEvent.Type != "node.claimed" ||
		document.ClaimEvent.Sequence < 1 || document.Lease.DurationMilliseconds < 1000 ||
		document.Lease.DurationMilliseconds > 300000 {
		return Claim{}, ErrConflict
	}
	initialExpiry, err := parseMillisecondTimestamp(document.Lease.InitialExpiresAt)
	if err != nil {
		return Claim{}, ErrConflict
	}
	if _, err := parseMillisecondTimestamp(document.ClaimedAt); err != nil {
		return Claim{}, ErrConflict
	}
	currentExpiry := initialExpiry
	if bundle.Active {
		if bundle.CurrentLeaseExpires == nil {
			return Claim{}, ErrConflict
		}
		currentExpiry, err = parseMillisecondTimestamp(*bundle.CurrentLeaseExpires)
		if err != nil || currentExpiry.Before(initialExpiry) {
			return Claim{}, ErrConflict
		}
	} else if bundle.CurrentLeaseExpires != nil {
		return Claim{}, ErrConflict
	}
	claim := Claim{
		ClaimEventID: claimID, AuthorizationID: authorization.AuthorizationID,
		Attempt: document.Lease.Attempt, Owner: document.Lease.Owner,
		LeaseExpiresAt: currentExpiry, Active: bundle.Active, Hash: bundle.Claim.Hash,
	}
	if err := claim.ValidateFor(authorization); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

type controllerBundleWire struct {
	SchemaVersion   string        `json:"schemaVersion"`
	AuthorizationID string        `json:"authorizationId"`
	Binding         exactMaterial `json:"binding"`
	Idempotent      *bool         `json:"idempotent,omitempty"`
}

type controllerBindingDocumentWire struct {
	SchemaVersion      string          `json:"schemaVersion"`
	AuthorizationID    string          `json:"authorizationId"`
	WorkflowLeaseClaim json.RawMessage `json:"workflowLeaseClaim"`
	ProductionRun      struct {
		ID          string `json:"id"`
		ProjectID   string `json:"projectId"`
		Environment string `json:"environment"`
		Operation   string `json:"operation"`
		StateAtBind string `json:"stateAtBind"`
	} `json:"productionRun"`
	ControllerOperation struct {
		ID          string `json:"id"`
		RequestHash string `json:"requestHash"`
		Controller  struct {
			SchemaVersion  string `json:"schemaVersion"`
			ID             string `json:"id"`
			Version        string `json:"version"`
			Protocol       string `json:"protocol"`
			TrustKeyDigest string `json:"trustKeyDigest"`
		} `json:"controller"`
	} `json:"controllerOperation"`
	Release json.RawMessage `json:"release"`
	BoundAt string          `json:"boundAt"`
}

func decodeControllerBundle(encoded []byte, authorization Authorization) (ControllerBinding, error) {
	var bundle controllerBundleWire
	if err := decodeExactJSON(encoded, &bundle); err != nil ||
		bundle.SchemaVersion != "worksflow-qualification-release-controller-bundle/v1" ||
		bundle.AuthorizationID != authorization.AuthorizationID.String() ||
		bundle.Binding.validate(1<<20) != nil {
		return ControllerBinding{}, ErrConflict
	}
	var document controllerBindingDocumentWire
	if err := decodeExactJSON(bundle.Binding.Document, &document); err != nil ||
		document.SchemaVersion != "worksflow-qualification-release-controller-binding/v1" ||
		document.AuthorizationID != bundle.AuthorizationID ||
		document.ProductionRun.Environment != "production" ||
		document.ProductionRun.Operation != "promote" || document.ProductionRun.StateAtBind != "queued" {
		return ControllerBinding{}, ErrConflict
	}
	productionRunID, err := parseUUID(document.ProductionRun.ID)
	if err != nil {
		return ControllerBinding{}, ErrConflict
	}
	operationID, err := parseUUID(document.ControllerOperation.ID)
	if err != nil {
		return ControllerBinding{}, ErrConflict
	}
	projectID, err := parseUUID(document.ProductionRun.ProjectID)
	if err != nil {
		return ControllerBinding{}, ErrConflict
	}
	if _, err := parseMillisecondTimestamp(document.BoundAt); err != nil {
		return ControllerBinding{}, ErrConflict
	}
	controller := ControllerIdentity{
		SchemaVersion:  document.ControllerOperation.Controller.SchemaVersion,
		ID:             document.ControllerOperation.Controller.ID,
		Version:        document.ControllerOperation.Controller.Version,
		Protocol:       document.ControllerOperation.Controller.Protocol,
		TrustKeyDigest: document.ControllerOperation.Controller.TrustKeyDigest,
	}
	binding := ControllerBinding{
		AuthorizationID: authorization.AuthorizationID, ProductionRunID: productionRunID,
		ControllerOperationID: operationID, ProjectID: projectID,
		RequestHash: document.ControllerOperation.RequestHash,
		Controller:  controller, Hash: bundle.Binding.Hash,
	}
	if err := binding.ValidateFor(authorization); err != nil {
		return ControllerBinding{}, err
	}
	return binding, nil
}

type terminalBundleWire struct {
	SchemaVersion   string         `json:"schemaVersion"`
	AuthorizationID string         `json:"authorizationId"`
	Outcome         string         `json:"outcome,omitempty"`
	Result          exactMaterial  `json:"result"`
	PublishResult   *PublishResult `json:"publishResult,omitempty"`
	Failure         *Failure       `json:"failure,omitempty"`
	Idempotent      *bool          `json:"idempotent,omitempty"`
}

type terminalDocumentWire struct {
	SchemaVersion       string          `json:"schemaVersion"`
	AuthorizationID     string          `json:"authorizationId"`
	Outcome             string          `json:"outcome,omitempty"`
	ProductionRun       json.RawMessage `json:"productionRun"`
	ControllerOperation json.RawMessage `json:"controllerOperation"`
	ProductionReceipt   json.RawMessage `json:"productionReceipt,omitempty"`
	DeploymentRevision  json.RawMessage `json:"deploymentRevision,omitempty"`
	PublishResult       *PublishResult  `json:"publishResult,omitempty"`
	Failure             *Failure        `json:"failure,omitempty"`
	CompletedAt         string          `json:"completedAt"`
}

func decodeTerminalBundle(encoded []byte, authorization Authorization, healthy bool) (TerminalRecord, error) {
	var bundle terminalBundleWire
	if err := decodeExactJSON(encoded, &bundle); err != nil || bundle.AuthorizationID != authorization.AuthorizationID.String() ||
		bundle.Result.validate(1<<20) != nil {
		return TerminalRecord{}, ErrConflict
	}
	var document terminalDocumentWire
	if err := decodeExactJSON(bundle.Result.Document, &document); err != nil ||
		document.AuthorizationID != bundle.AuthorizationID {
		return TerminalRecord{}, ErrConflict
	}
	if _, err := parseMillisecondTimestamp(document.CompletedAt); err != nil {
		return TerminalRecord{}, ErrConflict
	}
	record := TerminalRecord{
		AuthorizationID: authorization.AuthorizationID, ResultHash: bundle.Result.Hash,
		Idempotent: bundle.Idempotent != nil && *bundle.Idempotent,
	}
	if healthy {
		if bundle.SchemaVersion != "worksflow-qualification-release-result-bundle/v1" ||
			document.SchemaVersion != "worksflow-qualification-release-result/v1" ||
			bundle.PublishResult == nil || document.PublishResult == nil ||
			*bundle.PublishResult != *document.PublishResult || bundle.Failure != nil || document.Failure != nil {
			return TerminalRecord{}, ErrConflict
		}
		record.Outcome = "healthy"
		result := *bundle.PublishResult
		record.PublishResult = &result
	} else {
		if bundle.SchemaVersion != "worksflow-qualification-release-failure-bundle/v1" ||
			document.SchemaVersion != "worksflow-qualification-release-failure/v1" ||
			bundle.Outcome == "" || document.Outcome != bundle.Outcome ||
			bundle.Failure == nil || document.Failure == nil ||
			*bundle.Failure != *document.Failure || bundle.PublishResult != nil || document.PublishResult != nil {
			return TerminalRecord{}, ErrConflict
		}
		record.Outcome = bundle.Outcome
		failure := *bundle.Failure
		record.Failure = &failure
	}
	if err := record.ValidateFor(authorization); err != nil {
		return TerminalRecord{}, err
	}
	return record, nil
}

type bootstrapBundleWire struct {
	SchemaVersion string        `json:"schemaVersion"`
	BootstrapID   string        `json:"bootstrapId"`
	Bootstrap     exactMaterial `json:"bootstrap"`
}

type bootstrapDocumentWire struct {
	BootstrapID string `json:"bootstrapId"`
	Controller  struct {
		ID             string `json:"id"`
		Protocol       string `json:"protocol"`
		SchemaVersion  string `json:"schemaVersion"`
		TrustKeyDigest string `json:"trustKeyDigest"`
		Version        string `json:"version"`
	} `json:"controller"`
	SchemaVersion string `json:"schemaVersion"`
}

func decodeBootstrapBundle(encoded []byte) (ControllerIdentity, error) {
	var bundle bootstrapBundleWire
	if err := decodeExactJSON(encoded, &bundle); err != nil ||
		bundle.SchemaVersion != "worksflow-qualification-release-controller-bootstrap-bundle/v1" ||
		bundle.Bootstrap.validate(64<<10) != nil || !validUUIDString(bundle.BootstrapID) {
		return ControllerIdentity{}, ErrConflict
	}
	var document bootstrapDocumentWire
	if err := decodeExactJSON(bundle.Bootstrap.Document, &document); err != nil ||
		document.SchemaVersion != "worksflow-qualification-release-controller-bootstrap/v1" ||
		document.BootstrapID != bundle.BootstrapID {
		return ControllerIdentity{}, ErrConflict
	}
	identity := ControllerIdentity{
		SchemaVersion: document.Controller.SchemaVersion, ID: document.Controller.ID,
		Version: document.Controller.Version, Protocol: document.Controller.Protocol,
		TrustKeyDigest: document.Controller.TrustKeyDigest,
	}
	if err := identity.Validate(); err != nil {
		return ControllerIdentity{}, ErrConflict
	}
	return identity, nil
}

func decodeExactJSON(encoded []byte, destination any) error {
	if len(encoded) == 0 || len(encoded) > maximumCapabilityBundleBytes || destination == nil {
		return ErrConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("qualification release JSON contains trailing data")
		}
		return err
	}
	return nil
}

func parseUUID(value string) (uuid.UUID, error) {
	if value == "" || value != strings.ToLower(value) {
		return uuid.Nil, ErrConflict
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || !validUUID(parsed) {
		return uuid.Nil, ErrConflict
	}
	return parsed, nil
}

func parseMillisecondTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format("2006-01-02T15:04:05.000Z") != value {
		return time.Time{}, ErrConflict
	}
	return parsed, nil
}
