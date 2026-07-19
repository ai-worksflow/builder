package reference

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/contracts"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	AIConversationProfileID = contracts.ProfileReferenceAIConversation
	aiConversationDirectory = "ai-conversation"
)

//go:embed ai-conversation/*.json
var files embed.FS

type ComponentDescriptor struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	ArtifactID string `json:"artifactId"`
	RevisionID string `json:"revisionId"`
	SHA256     string `json:"sha256"`
}

type Expectations struct {
	Entities               []string `json:"entities"`
	Operations             []string `json:"operations"`
	StateKeys              []string `json:"stateKeys"`
	EventTypes             []string `json:"eventTypes"`
	AcceptanceCriterionIDs []string `json:"acceptanceCriterionIds"`
}

type Manifest struct {
	SchemaVersion    string                `json:"schemaVersion"`
	ProfileID        string                `json:"profileId"`
	DeliverySliceID  string                `json:"deliverySliceId"`
	Components       []ComponentDescriptor `json:"components"`
	Expectations     Expectations          `json:"expectations"`
	ContentSetSHA256 string                `json:"contentSetSha256"`
	BundleHash       string                `json:"bundleHash"`
}

type Bundle struct {
	manifest    Manifest
	manifestRaw json.RawMessage
	components  map[string]json.RawMessage
	descriptors map[string]ComponentDescriptor
}

func LoadAIConversation() (Bundle, error) {
	manifestRaw, err := files.ReadFile(aiConversationDirectory + "/bundle.json")
	if err != nil {
		return Bundle{}, fmt.Errorf("read Reference bundle manifest: %w", err)
	}
	var manifest Manifest
	if err := strictDecode(manifestRaw, &manifest); err != nil {
		return Bundle{}, fmt.Errorf("decode Reference bundle manifest: %w", err)
	}
	if manifest.SchemaVersion != "reference-ai-conversation-bundle/v1" ||
		manifest.ProfileID != AIConversationProfileID || manifest.DeliverySliceID != "page-conversations" {
		return Bundle{}, errors.New("Reference bundle identity is invalid")
	}
	if err := verifyBundleHash(manifestRaw, manifest.BundleHash); err != nil {
		return Bundle{}, err
	}
	if len(manifest.Components) != 12 {
		return Bundle{}, fmt.Errorf("Reference bundle has %d components; want 12", len(manifest.Components))
	}

	bundle := Bundle{
		manifest: manifest, manifestRaw: append(json.RawMessage(nil), manifestRaw...),
		components: map[string]json.RawMessage{}, descriptors: map[string]ComponentDescriptor{},
	}
	paths := map[string]bool{}
	artifacts := map[string]bool{}
	revisions := map[string]bool{}
	for _, descriptor := range manifest.Components {
		if strings.TrimSpace(descriptor.Kind) == "" || strings.TrimSpace(descriptor.ArtifactID) == "" || strings.TrimSpace(descriptor.RevisionID) == "" ||
			path.Base(descriptor.Path) != descriptor.Path || descriptor.Path == "." || descriptor.Path == ".." || !domain.IsCanonicalHash(descriptor.SHA256) {
			return Bundle{}, fmt.Errorf("Reference component descriptor is invalid: %#v", descriptor)
		}
		if _, duplicate := bundle.components[descriptor.Kind]; duplicate || paths[descriptor.Path] || artifacts[descriptor.ArtifactID] || revisions[descriptor.RevisionID] {
			return Bundle{}, fmt.Errorf("Reference component identity is duplicated: %#v", descriptor)
		}
		payload, readErr := files.ReadFile(aiConversationDirectory + "/" + descriptor.Path)
		if readErr != nil {
			return Bundle{}, fmt.Errorf("read Reference component %s: %w", descriptor.Path, readErr)
		}
		if err := rejectDuplicateJSONKeys(payload); err != nil {
			return Bundle{}, fmt.Errorf("Reference component %s is not strict JSON: %w", descriptor.Path, err)
		}
		digest := sha256.Sum256(payload)
		if hex.EncodeToString(digest[:]) != descriptor.SHA256 {
			return Bundle{}, fmt.Errorf("Reference component %s hash does not match manifest", descriptor.Path)
		}
		bundle.components[descriptor.Kind] = append(json.RawMessage(nil), payload...)
		bundle.descriptors[descriptor.Kind] = descriptor
		paths[descriptor.Path], artifacts[descriptor.ArtifactID], revisions[descriptor.RevisionID] = true, true, true
	}
	if actual := contentSetHash(manifest.Components); actual != manifest.ContentSetSHA256 {
		return Bundle{}, fmt.Errorf("Reference content-set hash = %s, want %s", actual, manifest.ContentSetSHA256)
	}
	if err := verifyDirectoryMembership(manifest.Components); err != nil {
		return Bundle{}, err
	}
	if err := bundle.validate(); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (bundle Bundle) Manifest() Manifest {
	result := bundle.manifest
	result.Components = append([]ComponentDescriptor{}, bundle.manifest.Components...)
	result.Expectations = cloneExpectations(bundle.manifest.Expectations)
	return result
}

func (bundle Bundle) ManifestPayload() json.RawMessage {
	return append(json.RawMessage(nil), bundle.manifestRaw...)
}

func (bundle Bundle) Component(kind string) (json.RawMessage, bool) {
	payload, exists := bundle.components[strings.TrimSpace(kind)]
	return append(json.RawMessage(nil), payload...), exists
}

func (bundle Bundle) Descriptor(kind string) (ComponentDescriptor, bool) {
	descriptor, exists := bundle.descriptors[strings.TrimSpace(kind)]
	return descriptor, exists
}

func (bundle Bundle) validate() error {
	for _, kind := range []string{contracts.KindRequirementBaseline, contracts.KindBlueprint, contracts.KindPageSpec, contracts.KindPrototype} {
		report := core.ValidateArtifactContent(kind, bundle.components[kind])
		if !report.Valid {
			return fmt.Errorf("Reference %s is invalid: %#v", kind, report.Findings)
		}
	}
	profileFacts, findings := contracts.InspectApplicationProfile(bundle.manifest.ProfileID, bundle.components)
	if len(findings) != 0 {
		return fmt.Errorf("Reference application profile is invalid: %#v", findings)
	}
	if !sameStrings(profileFacts.Entities, bundle.manifest.Expectations.Entities) ||
		!sameStrings(profileFacts.Operations, bundle.manifest.Expectations.Operations) ||
		!sameStrings(profileFacts.StateKeys, bundle.manifest.Expectations.StateKeys) ||
		!sameStrings(profileFacts.EventTypes, bundle.manifest.Expectations.EventTypes) ||
		!sameStrings(profileFacts.AcceptanceCriterionIDs, bundle.manifest.Expectations.AcceptanceCriterionIDs) {
		return errors.New("Reference application profile facts do not match bundle expectations")
	}
	if err := bundle.validateAcceptanceIndex(); err != nil {
		return err
	}
	return bundle.validatePrototypePageSpecPin()
}

func (bundle Bundle) validateAcceptanceIndex() error {
	type criterion struct {
		ID             string   `json:"id"`
		RequirementIDs []string `json:"requirementIds"`
		Statement      string   `json:"statement"`
	}
	var index struct {
		SchemaVersion string      `json:"schemaVersion"`
		ApplicationID string      `json:"applicationId"`
		Criteria      []criterion `json:"criteria"`
	}
	var baseline struct {
		SchemaVersion  string `json:"schemaVersion"`
		SourceVersions []struct {
			ArtifactID  string `json:"artifactId"`
			RevisionID  string `json:"revisionId"`
			ContentHash string `json:"contentHash"`
		} `json:"sourceVersions"`
		Requirements []struct {
			Type                   string   `json:"type"`
			RequirementID          string   `json:"requirementId"`
			AcceptanceCriterionID  string   `json:"acceptanceCriterionId"`
			AcceptanceCriterionIDs []string `json:"acceptanceCriterionIds"`
			Statement              string   `json:"statement"`
			Priority               string   `json:"priority"`
		} `json:"requirements"`
		BaselineHash string `json:"baselineHash"`
	}
	if err := strictDecode(bundle.components["acceptance_index"], &index); err != nil {
		return fmt.Errorf("decode Reference acceptance index: %w", err)
	}
	if err := strictDecode(bundle.components[contracts.KindRequirementBaseline], &baseline); err != nil {
		return fmt.Errorf("decode Reference acceptance baseline: %w", err)
	}
	if index.SchemaVersion != "reference-acceptance-criteria/v1" || index.ApplicationID != "reference-ai-conversation-v1" {
		return errors.New("Reference acceptance index identity is invalid")
	}
	ids := make([]string, 0, len(index.Criteria))
	seen := map[string]bool{}
	baselineStatements := map[string]string{}
	baselineRequirements := map[string][]string{}
	for _, item := range baseline.Requirements {
		switch item.Type {
		case "requirement":
			for _, criterionID := range item.AcceptanceCriterionIDs {
				baselineRequirements[criterionID] = append(baselineRequirements[criterionID], item.RequirementID)
			}
		case "acceptanceCriterion":
			baselineStatements[item.AcceptanceCriterionID] = item.Statement
		}
	}
	for _, item := range index.Criteria {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Statement) == "" || len(item.RequirementIDs) == 0 || seen[item.ID] {
			return errors.New("Reference acceptance index contains an invalid or duplicate criterion")
		}
		if item.Statement != baselineStatements[item.ID] || !sameStrings(item.RequirementIDs, baselineRequirements[item.ID]) {
			return errors.New("Reference acceptance index drifts from the authoritative Requirement Baseline")
		}
		seen[item.ID] = true
		ids = append(ids, item.ID)
	}
	if !sameStrings(ids, bundle.manifest.Expectations.AcceptanceCriterionIDs) {
		return errors.New("Reference acceptance index does not match expected criterion IDs")
	}
	return nil
}

func (bundle Bundle) validatePrototypePageSpecPin() error {
	pageDescriptor := bundle.descriptors[contracts.KindPageSpec]
	pageHash, err := domain.CanonicalHash(bundle.components[contracts.KindPageSpec])
	if err != nil {
		return fmt.Errorf("hash Reference PageSpec: %w", err)
	}
	var prototype struct {
		PageSpecRevision struct {
			ArtifactID  string `json:"artifactId"`
			RevisionID  string `json:"revisionId"`
			ContentHash string `json:"contentHash"`
		} `json:"pageSpecRevision"`
	}
	if err := json.Unmarshal(bundle.components[contracts.KindPrototype], &prototype); err != nil {
		return fmt.Errorf("decode Reference Prototype PageSpec pin: %w", err)
	}
	if prototype.PageSpecRevision.ArtifactID != pageDescriptor.ArtifactID ||
		prototype.PageSpecRevision.RevisionID != pageDescriptor.RevisionID || prototype.PageSpecRevision.ContentHash != pageHash {
		return errors.New("Reference Prototype does not pin the exact bundled PageSpec revision")
	}
	return nil
}

func verifyBundleHash(payload []byte, expected string) error {
	if !domain.IsCanonicalHash(expected) {
		return errors.New("Reference bundle hash is invalid")
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	value["bundleHash"] = ""
	actual, err := domain.CanonicalHash(value)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("Reference bundle hash = %s, want %s", actual, expected)
	}
	return nil
}

func contentSetHash(components []ComponentDescriptor) string {
	ordered := append([]ComponentDescriptor{}, components...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	digest := sha256.New()
	for _, component := range ordered {
		_, _ = io.WriteString(digest, component.Path+"\n"+component.SHA256+"\n")
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func strictDecode(payload []byte, target any) error {
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func verifyDirectoryMembership(components []ComponentDescriptor) error {
	expected := map[string]bool{"bundle.json": true}
	for _, component := range components {
		expected[component.Path] = true
	}
	entries, err := files.ReadDir(aiConversationDirectory)
	if err != nil {
		return fmt.Errorf("list Reference bundle directory: %w", err)
	}
	if len(entries) != len(expected) {
		return errors.New("Reference bundle directory contains files outside the hash-closed manifest")
	}
	for _, entry := range entries {
		if entry.IsDir() || !expected[entry.Name()] {
			return errors.New("Reference bundle directory contains files outside the hash-closed manifest")
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var consume func() error
	consume = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = true
				if err := consume(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := consume(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := consume(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func cloneExpectations(value Expectations) Expectations {
	return Expectations{
		Entities: append([]string{}, value.Entities...), Operations: append([]string{}, value.Operations...),
		StateKeys: append([]string{}, value.StateKeys...), EventTypes: append([]string{}, value.EventTypes...),
		AcceptanceCriterionIDs: append([]string{}, value.AcceptanceCriterionIDs...),
	}
}

func sameStrings(left, right []string) bool {
	leftCopy, rightCopy := append([]string{}, left...), append([]string{}, right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	if len(leftCopy) != len(rightCopy) {
		return false
	}
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}
