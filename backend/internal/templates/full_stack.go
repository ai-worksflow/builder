package templates

import (
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

type fullStackHashPayload struct {
	ID            string               `json:"id"`
	SchemaVersion string               `json:"schemaVersion"`
	TemplateID    string               `json:"templateId"`
	Version       string               `json:"version"`
	Components    []FullStackComponent `json:"components"`
	Layout        FullStackLayout      `json:"layout"`
	CreatedBy     string               `json:"createdBy"`
	CreatedAt     time.Time            `json:"createdAt"`
}

func NewFullStackTemplate(
	id, templateID, version string,
	components []FullStackComponentInput,
	layout FullStackLayout,
	createdBy string,
	now time.Time,
) (FullStackTemplate, error) {
	if err := validateUUID(id, "id"); err != nil {
		return FullStackTemplate{}, err
	}
	if err := validateUUID(createdBy, "createdBy"); err != nil {
		return FullStackTemplate{}, err
	}
	if now.IsZero() {
		return FullStackTemplate{}, invalid("invalid_time", "createdAt", "must not be zero")
	}
	if len(components) < 2 || len(components) > 8 {
		return FullStackTemplate{}, invalid("invalid_components", "components", "must contain the exact web/api releases and at most six auxiliary releases")
	}
	resolved := make([]FullStackComponent, 0, len(components))
	seenReleases := map[string]bool{}
	for index, component := range components {
		if err := validateReleaseDocument(component.Release.document); err != nil {
			return FullStackTemplate{}, invalid("invalid_component_release", "components", err.Error())
		}
		role := strings.TrimSpace(component.Role)
		if role != "web" && role != "api" && role != "worker" {
			return FullStackTemplate{}, invalid("invalid_component_role", "components", "roles must be web, api, or worker")
		}
		if !releaseContainsServiceKind(component.Release, role) {
			return FullStackTemplate{}, invalid("component_role_mismatch", "components", "component release does not declare a "+role+" service")
		}
		if seenReleases[component.Release.ID()] {
			return FullStackTemplate{}, invalid("duplicate_component_release", "components", "a release can be mounted only once")
		}
		seenReleases[component.Release.ID()] = true
		resolved = append(resolved, FullStackComponent{
			Role: role, MountPath: component.MountPath,
			Release: TemplateReleaseRef{ID: component.Release.ID(), ContentHash: component.Release.ContentHash(), SubjectHash: component.Release.SubjectHash()},
		})
		_ = index
	}
	document := fullStackTemplateDocument{
		ID: strings.TrimSpace(id), SchemaVersion: FullStackTemplateSchemaVersion,
		TemplateID: strings.TrimSpace(templateID), Version: strings.TrimSpace(version),
		Components: resolved, Layout: layout, CreatedBy: strings.TrimSpace(createdBy), CreatedAt: now.UTC(),
	}
	normalized, err := normalizeFullStackDocument(document)
	if err != nil {
		return FullStackTemplate{}, err
	}
	hash, err := fullStackContentHash(normalized)
	if err != nil {
		return FullStackTemplate{}, err
	}
	normalized.ContentHash = hash
	return FullStackTemplate{document: normalized}, nil
}

func normalizeFullStackDocument(document fullStackTemplateDocument) (fullStackTemplateDocument, error) {
	if document.SchemaVersion != FullStackTemplateSchemaVersion {
		return fullStackTemplateDocument{}, &Error{Kind: ErrUnsupportedSchema, Code: "unsupported_full_stack_schema", Field: "schemaVersion", Detail: "must be full-stack-template/v1"}
	}
	if err := validateUUID(document.ID, "id"); err != nil {
		return fullStackTemplateDocument{}, err
	}
	if err := validateUUID(document.CreatedBy, "createdBy"); err != nil {
		return fullStackTemplateDocument{}, err
	}
	document.TemplateID = strings.TrimSpace(document.TemplateID)
	document.Version = strings.TrimSpace(document.Version)
	if !slugPattern.MatchString(document.TemplateID) || len(document.TemplateID) > 120 {
		return fullStackTemplateDocument{}, invalid("invalid_template_id", "templateId", "must be a lowercase kebab-case identifier")
	}
	if !versionPattern.MatchString(document.Version) || len(document.Version) > 80 {
		return fullStackTemplateDocument{}, invalid("invalid_version", "version", "must be an explicit semantic version")
	}
	if document.CreatedAt.IsZero() || document.CreatedAt.Location() != document.CreatedAt.UTC().Location() {
		return fullStackTemplateDocument{}, invalid("invalid_time", "createdAt", "must be a non-zero UTC timestamp")
	}
	if len(document.Components) < 2 || len(document.Components) > 8 {
		return fullStackTemplateDocument{}, invalid("invalid_components", "components", "must contain between 2 and 8 exact release references")
	}
	roles, mounts, releases := map[string]bool{}, []string{}, map[string]bool{}
	for index, component := range document.Components {
		component.Role = strings.TrimSpace(component.Role)
		if component.Role != "web" && component.Role != "api" && component.Role != "worker" {
			return fullStackTemplateDocument{}, invalid("invalid_component_role", "components", "roles must be web, api, or worker")
		}
		if roles[component.Role] {
			return fullStackTemplateDocument{}, invalid("duplicate_component_role", "components", "full-stack-template/v1 permits one release per role")
		}
		roles[component.Role] = true
		mount, err := normalizeRelativePath(component.MountPath, false)
		if err != nil {
			return fullStackTemplateDocument{}, invalid("invalid_component_mount", "components", err.Error())
		}
		component.MountPath = mount
		for _, prior := range mounts {
			if pathsOverlap(prior, mount) {
				return fullStackTemplateDocument{}, invalid("overlapping_component_mount", "components", mount+" overlaps "+prior)
			}
		}
		mounts = append(mounts, mount)
		if err := validateUUID(component.Release.ID, "components.release.id"); err != nil {
			return fullStackTemplateDocument{}, err
		}
		if err := validateDigest(component.Release.ContentHash, "components.release.contentHash"); err != nil {
			return fullStackTemplateDocument{}, err
		}
		if err := validateDigest(component.Release.SubjectHash, "components.release.subjectHash"); err != nil {
			return fullStackTemplateDocument{}, err
		}
		if releases[component.Release.ID] {
			return fullStackTemplateDocument{}, invalid("duplicate_component_release", "components", "a release can be mounted only once")
		}
		releases[component.Release.ID] = true
		document.Components[index] = component
	}
	if !roles["web"] || !roles["api"] {
		return fullStackTemplateDocument{}, invalid("missing_component_role", "components", "both web and api releases are required")
	}
	sort.Slice(document.Components, func(i, j int) bool {
		return document.Components[i].Role+":"+document.Components[i].MountPath < document.Components[j].Role+":"+document.Components[j].MountPath
	})

	document.Layout.ContractTruthSource = strings.TrimSpace(document.Layout.ContractTruthSource)
	document.Layout.DatabaseEngine = strings.TrimSpace(document.Layout.DatabaseEngine)
	if document.Layout.ContractTruthSource != "openapi" {
		return fullStackTemplateDocument{}, invalid("invalid_contract_truth_source", "layout.contractTruthSource", "full-stack-template/v1 requires OpenAPI as the API truth source")
	}
	if document.Layout.DatabaseEngine != "postgresql" {
		return fullStackTemplateDocument{}, invalid("invalid_database_engine", "layout.databaseEngine", "full-stack-template/v1 requires PostgreSQL")
	}
	layoutPaths := []*string{
		&document.Layout.OpenAPIPath,
		&document.Layout.GeneratedClientPath,
		&document.Layout.DeploymentPath,
		&document.Layout.TestPath,
	}
	allPaths := append([]string(nil), mounts...)
	for index, pointer := range layoutPaths {
		normalized, err := normalizeRelativePath(*pointer, false)
		if err != nil {
			return fullStackTemplateDocument{}, invalid("invalid_layout_path", "layout", err.Error())
		}
		for _, prior := range allPaths {
			if pathsOverlap(prior, normalized) {
				return fullStackTemplateDocument{}, invalid("overlapping_layout_path", "layout", normalized+" overlaps "+prior)
			}
		}
		*pointer = normalized
		allPaths = append(allPaths, normalized)
		_ = index
	}
	return document, nil
}

func fullStackContentHash(document fullStackTemplateDocument) (string, error) {
	return canonicalHash(fullStackHashPayload{
		ID: document.ID, SchemaVersion: document.SchemaVersion, TemplateID: document.TemplateID,
		Version: document.Version, Components: document.Components, Layout: document.Layout,
		CreatedBy: document.CreatedBy, CreatedAt: document.CreatedAt,
	})
}

func ParseFullStackTemplate(encoded []byte) (FullStackTemplate, error) {
	var document fullStackTemplateDocument
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return FullStackTemplate{}, invalid("invalid_full_stack_json", "fullStackTemplate", err.Error())
	}
	normalized, err := normalizeFullStackDocument(document)
	if err != nil {
		return FullStackTemplate{}, err
	}
	if !reflect.DeepEqual(document, normalized) {
		return FullStackTemplate{}, invalid("noncanonical_full_stack_template", "fullStackTemplate", "must use canonical component ordering and normalized paths")
	}
	if err := validateDigest(document.ContentHash, "contentHash"); err != nil {
		return FullStackTemplate{}, err
	}
	expected, err := fullStackContentHash(document)
	if err != nil {
		return FullStackTemplate{}, err
	}
	if document.ContentHash != expected {
		return FullStackTemplate{}, invalid("full_stack_content_mismatch", "contentHash", "does not commit to the exact immutable combination")
	}
	return FullStackTemplate{document: cloneFullStackDocument(document)}, nil
}

func (t FullStackTemplate) CanonicalJSON() ([]byte, error) {
	return domain.CanonicalJSON(t.document)
}

func (t *FullStackTemplate) UnmarshalJSON([]byte) error {
	return ErrImmutableRelease
}
