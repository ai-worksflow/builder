package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
)

const postgresQualityCompletionMaterialPlanQuery = `
SELECT resolve_workflow_v3_quality_completion_material_plan_v1(
  CAST(? AS uuid),CAST(? AS uuid),?
)`

const postgresQualityCompletionMaterialAdmissionQuery = `
SELECT admit_workflow_v3_quality_completion_materials_v1(
  ?,?,?,?,?,?,?,CAST(? AS jsonb)
)`

const (
	maximumQualityCompletionMaterialPlanBytes = (2 * maximumQualityCompletionRetainedBytes) +
		((maximumQualityCompletionManifests + maximumQualityCompletionRevisions +
			maximumQualityCompletionReviewReceipts) * 512) + 1024
	maximumQualityCompletionDefinitionBytes = 8 << 20
	maximumQualityCompletionRunScopeBytes   = 8 << 20
	maximumQualityCompletionNodeInputBytes  = 16 << 20
	maximumQualityCompletionBuildBytes      = 8 << 20
	maximumQualityCompletionManifestBytes   = 8 << 20
	maximumQualityCompletionRevisionBytes   = 16 << 20
	maximumQualityCompletionReviewBytes     = 1 << 20
	maximumQualityCompletionRetainedBytes   = 128 << 20
	maximumQualityCompletionManifests       = 1024
	maximumQualityCompletionRevisions       = 2048
	maximumQualityCompletionReviewReceipts  = 2048
)

type qualityCompletionMaterialAdmission struct {
	definitionRaw    []byte
	runScopeRaw      []byte
	nodeInputRaw     []byte
	buildManifestRaw []byte
	buildContractRaw []byte
	bundle           json.RawMessage
}

type qualityCompletionMaterialPlan struct {
	DefinitionRawBytesHex string                                  `json:"definitionRawBytesHex"`
	RunScopeRawBytesHex   string                                  `json:"runScopeRawBytesHex"`
	NodeInputRawBytesHex  string                                  `json:"nodeInputRawBytesHex"`
	BuildManifest         qualityCompletionMaterialContentPlan    `json:"buildManifest"`
	BuildContract         qualityCompletionMaterialContentPlan    `json:"buildContract"`
	InputManifests        []qualityCompletionManifestMaterialPlan `json:"inputManifests"`
	Revisions             []qualityCompletionRevisionMaterialPlan `json:"revisions"`
	ReviewReceipts        []qualityCompletionReviewReceiptPlan    `json:"reviewReceipts"`
}

type qualityCompletionMaterialContentPlan struct {
	ContentStore string `json:"contentStore"`
	ContentRef   string `json:"contentRef"`
	ContentHash  string `json:"contentHash"`
}

type qualityCompletionManifestMaterialPlan struct {
	ManifestID   string `json:"manifestId"`
	Role         string `json:"role"`
	ContentStore string `json:"contentStore"`
	ContentRef   string `json:"contentRef"`
	ContentHash  string `json:"contentHash"`
}

type qualityCompletionRevisionMaterialPlan struct {
	Purpose      string `json:"purpose"`
	RevisionID   string `json:"revisionId"`
	ContentStore string `json:"contentStore"`
	ContentRef   string `json:"contentRef"`
	ContentHash  string `json:"contentHash"`
}

type qualityCompletionReviewReceiptPlan struct {
	ReviewRequestID string `json:"reviewRequestId"`
	RawBytesHex     string `json:"rawBytesHex"`
}

type qualityCompletionManifestMaterial struct {
	ManifestID  string `json:"manifestId"`
	RawBytesHex string `json:"rawBytesHex"`
	Role        string `json:"role"`
}

type qualityCompletionRevisionMaterial struct {
	Purpose     string `json:"purpose"`
	RawBytesHex string `json:"rawBytesHex"`
	RevisionID  string `json:"revisionId"`
}

type qualityCompletionReviewReceiptMaterial struct {
	RawBytesHex     string `json:"rawBytesHex"`
	ReviewRequestID string `json:"reviewRequestId"`
}

type qualityCompletionMaterialBundle struct {
	InputManifests []qualityCompletionManifestMaterial      `json:"inputManifests"`
	Revisions      []qualityCompletionRevisionMaterial      `json:"revisions"`
	ReviewReceipts []qualityCompletionReviewReceiptMaterial `json:"reviewReceipts"`
}

func (s *GORMStore) prefetchQualityCompletionMaterials(
	ctx context.Context,
	precommit *QualityCompletionPrecommitMutation,
) (qualityCompletionMaterialAdmission, error) {
	if s == nil || s.db == nil || s.content == nil || precommit == nil {
		return qualityCompletionMaterialAdmission{}, domain.ErrInvalidArgument
	}
	var rawPlan json.RawMessage
	if err := s.db.WithContext(ctx).Raw(
		postgresQualityCompletionMaterialPlanQuery,
		precommit.WorkflowRunID, precommit.QualityNodeRunID,
		[]byte(precommit.GateInputCanonical),
	).Row().Scan(&rawPlan); err != nil {
		return qualityCompletionMaterialAdmission{}, mapQualityCompletionPostgresError("resolve material plan", err)
	}
	plan, err := decodeQualityCompletionMaterialPlan(rawPlan)
	if err != nil {
		return qualityCompletionMaterialAdmission{}, err
	}
	return s.prefetchQualityCompletionMaterialPlan(ctx, precommit, plan)
}

func (s *GORMStore) prefetchQualityCompletionMaterialPlan(
	ctx context.Context,
	precommit *QualityCompletionPrecommitMutation,
	plan qualityCompletionMaterialPlan,
) (qualityCompletionMaterialAdmission, error) {
	if s == nil || s.content == nil || precommit == nil {
		return qualityCompletionMaterialAdmission{}, domain.ErrInvalidArgument
	}
	definitionRaw, err := decodeQualityCompletionMaterialPlanHex(
		"definition", plan.DefinitionRawBytesHex, maximumQualityCompletionDefinitionBytes,
	)
	if err != nil {
		return qualityCompletionMaterialAdmission{}, err
	}
	runScopeRaw, err := decodeQualityCompletionMaterialPlanHex(
		"run scope", plan.RunScopeRawBytesHex, maximumQualityCompletionRunScopeBytes,
	)
	if err != nil {
		return qualityCompletionMaterialAdmission{}, err
	}
	nodeInputRaw, err := decodeQualityCompletionMaterialPlanHex(
		"node input", plan.NodeInputRawBytesHex, maximumQualityCompletionNodeInputBytes,
	)
	if err != nil {
		return qualityCompletionMaterialAdmission{}, err
	}
	if !bytes.Equal(nodeInputRaw, precommit.GateInputCanonical) {
		return qualityCompletionMaterialAdmission{}, qualityCompletionMaterialPlanCorrupt(
			"node input differs from the precommitted gate bytes",
		)
	}
	if !json.Valid(definitionRaw) || !json.Valid(runScopeRaw) {
		return qualityCompletionMaterialAdmission{}, qualityCompletionMaterialPlanCorrupt(
			"definition or run scope is not JSON",
		)
	}

	retainedBytes := len(definitionRaw) + len(runScopeRaw) + len(nodeInputRaw)
	buildManifestRaw, err := s.getQualityCompletionMaterial(
		ctx, "BuildManifest", plan.BuildManifest, maximumQualityCompletionBuildBytes,
	)
	if err != nil {
		return qualityCompletionMaterialAdmission{}, err
	}
	retainedBytes += len(buildManifestRaw)
	buildContractRaw, err := s.getQualityCompletionMaterial(
		ctx, "BuildContract", plan.BuildContract, maximumQualityCompletionBuildBytes,
	)
	if err != nil {
		return qualityCompletionMaterialAdmission{}, err
	}
	retainedBytes += len(buildContractRaw)
	if retainedBytes > maximumQualityCompletionRetainedBytes {
		return qualityCompletionMaterialAdmission{}, qualityCompletionMaterialPlanCorrupt(
			"retained material exceeds the aggregate bound",
		)
	}

	bundle := qualityCompletionMaterialBundle{
		InputManifests: make([]qualityCompletionManifestMaterial, 0, len(plan.InputManifests)),
		Revisions:      make([]qualityCompletionRevisionMaterial, 0, len(plan.Revisions)),
		ReviewReceipts: make([]qualityCompletionReviewReceiptMaterial, 0, len(plan.ReviewReceipts)),
	}
	for _, material := range plan.InputManifests {
		raw, err := s.getQualityCompletionMaterial(ctx, "input manifest "+material.ManifestID,
			qualityCompletionMaterialContentPlan{
				ContentStore: material.ContentStore,
				ContentRef:   material.ContentRef,
				ContentHash:  material.ContentHash,
			}, maximumQualityCompletionManifestBytes)
		if err != nil {
			return qualityCompletionMaterialAdmission{}, err
		}
		retainedBytes += len(raw)
		if retainedBytes > maximumQualityCompletionRetainedBytes {
			return qualityCompletionMaterialAdmission{}, qualityCompletionMaterialPlanCorrupt(
				"retained material exceeds the aggregate bound",
			)
		}
		bundle.InputManifests = append(bundle.InputManifests, qualityCompletionManifestMaterial{
			ManifestID: material.ManifestID, RawBytesHex: hex.EncodeToString(raw), Role: material.Role,
		})
	}
	for _, material := range plan.Revisions {
		raw, err := s.getQualityCompletionMaterial(ctx, "artifact revision "+material.RevisionID,
			qualityCompletionMaterialContentPlan{
				ContentStore: material.ContentStore,
				ContentRef:   material.ContentRef,
				ContentHash:  material.ContentHash,
			}, maximumQualityCompletionRevisionBytes)
		if err != nil {
			return qualityCompletionMaterialAdmission{}, err
		}
		retainedBytes += len(raw)
		if retainedBytes > maximumQualityCompletionRetainedBytes {
			return qualityCompletionMaterialAdmission{}, qualityCompletionMaterialPlanCorrupt(
				"retained material exceeds the aggregate bound",
			)
		}
		bundle.Revisions = append(bundle.Revisions, qualityCompletionRevisionMaterial{
			Purpose: material.Purpose, RawBytesHex: hex.EncodeToString(raw), RevisionID: material.RevisionID,
		})
	}
	for _, material := range plan.ReviewReceipts {
		raw, err := decodeQualityCompletionMaterialPlanHex(
			"Canonical Review receipt", material.RawBytesHex, maximumQualityCompletionReviewBytes,
		)
		if err != nil {
			return qualityCompletionMaterialAdmission{}, err
		}
		retainedBytes += len(raw)
		if retainedBytes > maximumQualityCompletionRetainedBytes {
			return qualityCompletionMaterialAdmission{}, qualityCompletionMaterialPlanCorrupt(
				"retained material exceeds the aggregate bound",
			)
		}
		bundle.ReviewReceipts = append(bundle.ReviewReceipts, qualityCompletionReviewReceiptMaterial{
			RawBytesHex: hex.EncodeToString(raw), ReviewRequestID: material.ReviewRequestID,
		})
	}
	if retainedBytes > maximumQualityCompletionRetainedBytes {
		return qualityCompletionMaterialAdmission{}, qualityCompletionMaterialPlanCorrupt(
			"retained material exceeds the aggregate bound",
		)
	}
	bundleRaw, err := json.Marshal(bundle)
	if err != nil {
		return qualityCompletionMaterialAdmission{}, err
	}
	return qualityCompletionMaterialAdmission{
		definitionRaw:    definitionRaw,
		runScopeRaw:      runScopeRaw,
		nodeInputRaw:     nodeInputRaw,
		buildManifestRaw: buildManifestRaw,
		buildContractRaw: buildContractRaw,
		bundle:           bundleRaw,
	}, nil
}

func decodeQualityCompletionMaterialPlan(raw json.RawMessage) (qualityCompletionMaterialPlan, error) {
	if len(raw) == 0 || len(raw) > maximumQualityCompletionMaterialPlanBytes ||
		!json.Valid(raw) || rejectDuplicateJSONNamesV3(raw) != nil {
		return qualityCompletionMaterialPlan{}, qualityCompletionMaterialPlanCorrupt(
			"resolver returned invalid, duplicate-name, or oversized JSON",
		)
	}
	if err := requireExactObjectFieldsV3(raw, []string{
		"definitionRawBytesHex", "runScopeRawBytesHex", "nodeInputRawBytesHex",
		"buildManifest", "buildContract", "inputManifests", "revisions", "reviewReceipts",
	}, nil); err != nil {
		return qualityCompletionMaterialPlan{}, qualityCompletionMaterialPlanCorrupt(
			"resolver root is not exact: %v", err,
		)
	}
	var plan qualityCompletionMaterialPlan
	if err := strictDecodeJSONV3(raw, &plan); err != nil {
		return qualityCompletionMaterialPlan{}, qualityCompletionMaterialPlanCorrupt(
			"resolver plan does not strictly decode: %v", err,
		)
	}
	if plan.InputManifests == nil || len(plan.InputManifests) < 1 ||
		len(plan.InputManifests) > maximumQualityCompletionManifests ||
		plan.Revisions == nil || len(plan.Revisions) < 1 ||
		len(plan.Revisions) > maximumQualityCompletionRevisions ||
		plan.ReviewReceipts == nil || len(plan.ReviewReceipts) > maximumQualityCompletionReviewReceipts ||
		!qualityCompletionMaterialContentPlanValid(plan.BuildManifest) ||
		!qualityCompletionMaterialContentPlanValid(plan.BuildContract) {
		return qualityCompletionMaterialPlan{}, qualityCompletionMaterialPlanCorrupt(
			"resolver collections or Build references are absent, malformed, or oversized",
		)
	}
	manifestIdentities := make(map[string]bool, len(plan.InputManifests))
	for index, material := range plan.InputManifests {
		key := material.Role + "\x00" + material.ManifestID
		if !qualityCompletionUUIDv4(material.ManifestID) ||
			(material.Role != "run" && material.Role != "predecessor") ||
			!qualityCompletionMaterialContentPlanValid(qualityCompletionMaterialContentPlan{
				ContentStore: material.ContentStore, ContentRef: material.ContentRef, ContentHash: material.ContentHash,
			}) || manifestIdentities[key] ||
			(index > 0 && !qualityCompletionManifestPlanLess(plan.InputManifests[index-1], material)) {
			return qualityCompletionMaterialPlan{}, qualityCompletionMaterialPlanCorrupt(
				"resolver manifest closure is malformed, duplicated, or unordered",
			)
		}
		manifestIdentities[key] = true
	}
	revisionIdentities := make(map[string]bool, len(plan.Revisions))
	for index, material := range plan.Revisions {
		if !qualityCompletionUUIDv4(material.RevisionID) ||
			len(material.Purpose) == 0 || len(material.Purpose) > 256 ||
			strings.TrimSpace(material.Purpose) != material.Purpose ||
			!qualityCompletionMaterialContentPlanValid(qualityCompletionMaterialContentPlan{
				ContentStore: material.ContentStore, ContentRef: material.ContentRef, ContentHash: material.ContentHash,
			}) || revisionIdentities[material.RevisionID] ||
			(index > 0 && !qualityCompletionRevisionPlanLess(plan.Revisions[index-1], material)) {
			return qualityCompletionMaterialPlan{}, qualityCompletionMaterialPlanCorrupt(
				"resolver revision closure is malformed, duplicated, or unordered",
			)
		}
		revisionIdentities[material.RevisionID] = true
	}
	receiptIdentities := make(map[string]bool, len(plan.ReviewReceipts))
	for index, material := range plan.ReviewReceipts {
		if !qualityCompletionUUIDv4(material.ReviewRequestID) ||
			receiptIdentities[material.ReviewRequestID] ||
			(index > 0 && plan.ReviewReceipts[index-1].ReviewRequestID >= material.ReviewRequestID) {
			return qualityCompletionMaterialPlan{}, qualityCompletionMaterialPlanCorrupt(
				"resolver Canonical Review receipt closure is malformed, duplicated, or unordered",
			)
		}
		receiptIdentities[material.ReviewRequestID] = true
	}
	return plan, nil
}

func qualityCompletionMaterialContentPlanValid(plan qualityCompletionMaterialContentPlan) bool {
	return len(plan.ContentStore) > 0 && len(plan.ContentStore) <= 128 &&
		len(plan.ContentRef) > 0 && len(plan.ContentRef) <= 4096 &&
		qualityCompletionMaterialContentHashValid(plan.ContentHash) &&
		strings.TrimSpace(plan.ContentStore) == plan.ContentStore &&
		strings.TrimSpace(plan.ContentRef) == plan.ContentRef &&
		strings.TrimSpace(plan.ContentHash) == plan.ContentHash
}

func qualityCompletionMaterialContentHashValid(value string) bool {
	digest := strings.TrimPrefix(value, "sha256:")
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func qualityCompletionManifestPlanLess(
	left, right qualityCompletionManifestMaterialPlan,
) bool {
	if left.Role == right.Role {
		return left.ManifestID < right.ManifestID
	}
	return left.Role < right.Role
}

func qualityCompletionRevisionPlanLess(
	left, right qualityCompletionRevisionMaterialPlan,
) bool {
	if left.Purpose == right.Purpose {
		return left.RevisionID < right.RevisionID
	}
	return left.Purpose < right.Purpose
}

func decodeQualityCompletionMaterialPlanHex(name, encoded string, maximum int) ([]byte, error) {
	if maximum < 1 || len(encoded) < 2 || len(encoded) > maximum*2 || len(encoded)%2 != 0 ||
		strings.ToLower(encoded) != encoded {
		return nil, qualityCompletionMaterialPlanCorrupt("%s raw hexadecimal is malformed or oversized", name)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) < 1 || len(raw) > maximum {
		return nil, qualityCompletionMaterialPlanCorrupt("%s raw hexadecimal is malformed or oversized", name)
	}
	return raw, nil
}

func (s *GORMStore) getQualityCompletionMaterial(
	ctx context.Context,
	kind string,
	reference qualityCompletionMaterialContentPlan,
	maximum int,
) ([]byte, error) {
	if s == nil || s.content == nil || !qualityCompletionMaterialContentPlanValid(reference) || maximum < 1 {
		return nil, domain.ErrInvalidArgument
	}
	raw, err := s.content.Get(ctx, reference.ContentStore, reference.ContentRef, reference.ContentHash)
	if err != nil {
		return nil, fmt.Errorf("prefetch Quality completion %s: %w", kind, err)
	}
	if len(raw) < 1 || len(raw) > maximum {
		return nil, qualityCompletionMaterialPlanCorrupt("%s bytes are absent or oversized", kind)
	}
	return append([]byte(nil), raw...), nil
}

func admitQualityCompletionMaterialsTx(
	tx *gorm.DB,
	precommit *QualityCompletionPrecommitMutation,
	material qualityCompletionMaterialAdmission,
) error {
	if tx == nil || precommit == nil || len(material.definitionRaw) == 0 ||
		len(material.runScopeRaw) == 0 || len(material.nodeInputRaw) == 0 ||
		len(material.buildManifestRaw) == 0 || len(material.buildContractRaw) == 0 ||
		len(material.bundle) == 0 || !json.Valid(material.bundle) {
		return domain.ErrInvalidArgument
	}
	if err := tx.Exec(
		postgresQualityCompletionMaterialAdmissionQuery,
		precommit.PrecommitID, precommit.CompletionEventID,
		material.definitionRaw, material.runScopeRaw, material.nodeInputRaw,
		material.buildManifestRaw, material.buildContractRaw, string(material.bundle),
	).Error; err != nil {
		return mapQualityCompletionPostgresError("admit materials", err)
	}
	return nil
}

func qualityCompletionSerializableOptions(precommit *QualityCompletionPrecommitMutation) []*sql.TxOptions {
	if precommit == nil {
		return nil
	}
	return []*sql.TxOptions{{Isolation: sql.LevelSerializable}}
}

func qualityCompletionMaterialPlanCorrupt(format string, arguments ...any) error {
	return errors.Join(
		ErrCASConflict, ErrQualityCompletionPrecommitCorrupt,
		fmt.Errorf("Quality completion material plan: "+format, arguments...),
	)
}
