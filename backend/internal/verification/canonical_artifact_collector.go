package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/worksflow/builder/backend/internal/storage/content"
)

const CanonicalReleaseArtifactManifestSchemaVersion = "release-artifacts/v1"

var ErrCanonicalArtifactCollection = errors.New("canonical release artifact collection failed")

type ContentCanonicalArtifactCollector struct {
	contents PlanningContentReader
}

func NewContentCanonicalArtifactCollector(contents PlanningContentReader) (*ContentCanonicalArtifactCollector, error) {
	if contents == nil {
		return nil, fmt.Errorf("%w: content reader is required", ErrCanonicalArtifactCollection)
	}
	return &ContentCanonicalArtifactCollector{contents: contents}, nil
}

func (collector *ContentCanonicalArtifactCollector) CollectReleaseArtifacts(
	ctx context.Context,
	spec CanonicalExecutionSpec,
	checks []CheckResult,
) ([]CanonicalReleaseArtifact, error) {
	if err := validateCanonicalExecutionSpec(spec); err != nil {
		return nil, err
	}
	var manifest *CheckResult
	for index := range checks {
		check := &checks[index]
		if check.ID == "release-artifacts" {
			if manifest != nil {
				return nil, fmt.Errorf("%w: duplicate release-artifacts result", ErrCanonicalArtifactCollection)
			}
			manifest = check
		}
	}
	if manifest == nil || manifest.Kind != "release-manifest" || !manifest.Required ||
		manifest.Status != CheckPassed || manifest.AttemptID != spec.AttemptID || manifest.Stdout == nil ||
		manifest.Truncated || manifest.RedactionCount != 0 {
		return nil, fmt.Errorf("%w: exact passing release-artifacts result is unavailable", ErrCanonicalArtifactCollection)
	}
	reference := manifest.Stdout
	if reference.Store != "content" || reference.OwnerID != spec.AttemptID ||
		strings.TrimSpace(reference.Ref) == "" || !exactSHA256(reference.ContentHash) || reference.ByteSize < 0 {
		return nil, fmt.Errorf("%w: release-artifacts log reference is invalid", ErrCanonicalArtifactCollection)
	}
	stored, err := collector.contents.Get(ctx, reference.Ref, reference.ContentHash)
	if err != nil {
		return nil, fmt.Errorf("%w: read release-artifacts output: %v", ErrCanonicalArtifactCollection, err)
	}
	if stored.ID != reference.Ref || stored.ProjectID != spec.Content.ProjectID ||
		(stored.AggregateType != verificationCheckLogAggregate && stored.AggregateType != "candidate_verification_check_log") ||
		stored.AggregateID != spec.AttemptID || stored.SchemaVersion != 1 || stored.State != content.StateFinalized ||
		stored.ContentHash != reference.ContentHash || stored.ByteSize != reference.ByteSize {
		return nil, fmt.Errorf("%w: release-artifacts output metadata drifted", ErrCanonicalArtifactCollection)
	}
	var log struct {
		SchemaVersion string `json:"schemaVersion"`
		Stream        string `json:"stream"`
		CheckID       string `json:"checkId"`
		Value         string `json:"value"`
	}
	if err := json.Unmarshal(stored.Payload, &log); err != nil || log.SchemaVersion != "verification-check-log/v1" ||
		log.Stream != "stdout" || log.CheckID != manifest.ID || len(log.Value) > 1<<20 {
		return nil, fmt.Errorf("%w: release-artifacts output envelope is invalid", ErrCanonicalArtifactCollection)
	}
	var document struct {
		SchemaVersion string                     `json:"schemaVersion"`
		Artifacts     []CanonicalReleaseArtifact `json:"artifacts"`
	}
	decoder := json.NewDecoder(strings.NewReader(log.Value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.SchemaVersion != CanonicalReleaseArtifactManifestSchemaVersion {
		return nil, fmt.Errorf("%w: release artifact manifest is invalid", ErrCanonicalArtifactCollection)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: release artifact manifest has trailing content", ErrCanonicalArtifactCollection)
	}
	artifacts, err := normalizeCanonicalReleaseArtifacts(document.Artifacts)
	if err != nil || len(artifacts) == 0 {
		return nil, fmt.Errorf("%w: release artifact manifest has no valid artifacts", ErrCanonicalArtifactCollection)
	}
	return artifacts, nil
}
