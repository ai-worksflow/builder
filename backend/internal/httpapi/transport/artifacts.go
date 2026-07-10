package transport

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

const artifactCollectionKey = "artifact_collection"

type createCollectionArtifactInput struct {
	ArtifactKey         string            `json:"artifactKey,omitempty"`
	Title               string            `json:"title"`
	Kind                string            `json:"kind,omitempty"`
	SchemaVersion       int               `json:"schemaVersion,omitempty"`
	Content             json.RawMessage   `json:"content,omitempty"`
	SourceVersions      []core.VersionRef `json:"sourceVersions,omitempty"`
	RequirementVersions []core.VersionRef `json:"requirementVersions,omitempty"`
	BlueprintRevision   *core.VersionRef  `json:"blueprintRevision,omitempty"`
	PageSpecRevision    *core.VersionRef  `json:"pageSpecRevision,omitempty"`
	BlueprintPageNodeID string            `json:"blueprintPageNodeId,omitempty"`
	Exploratory         bool              `json:"exploratory,omitempty"`
}

type updateCollectionDraftInput struct {
	SchemaVersion  int               `json:"schemaVersion,omitempty"`
	Content        json.RawMessage   `json:"content"`
	SourceVersions []core.VersionRef `json:"sourceVersions,omitempty"`
}

func withArtifactCollection(collection string) gin.HandlerFunc {
	return func(context *gin.Context) {
		context.Set(artifactCollectionKey, collection)
		context.Next()
	}
}

func (s *Server) ListArtifacts(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Artifacts.List(
		context.Request.Context(), context.Param("projectId"), actor,
		strings.TrimSpace(context.Query("kind")), strings.TrimSpace(context.Query("status")),
	)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, items)
}

func (s *Server) GetArtifact(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	includeContent, ok := optionalBoolQuery(context, "includeContent")
	if !ok {
		return
	}
	artifact, err := s.services.Artifacts.Get(context.Request.Context(), context.Param("artifactId"), actor, includeContent)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", versionedArtifactETag(artifact))
	context.JSON(http.StatusOK, artifact)
}

func (s *Server) GetArtifactDraft(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	artifact, err := s.services.Artifacts.Get(context.Request.Context(), context.Param("artifactId"), actor, true)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if artifact.Draft == nil {
		problem.WriteError(context, core.ErrNotFound)
		return
	}
	context.Header("ETag", artifact.Draft.ETag)
	context.JSON(http.StatusOK, artifact.Draft)
}

func (s *Server) CreateArtifact(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	var input core.CreateArtifactInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	created, err := s.services.Artifacts.Create(context.Request.Context(), context.Param("projectId"), actor, input)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", versionedArtifactETag(created))
	context.Header("Location", "/v1/artifacts/"+created.Artifact.ID)
	context.JSON(http.StatusCreated, created)
}

func (s *Server) UpdateArtifactDraft(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	var input core.UpdateDraftInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	draft, err := s.services.Artifacts.UpdateDraft(
		context.Request.Context(), context.Param("draftId"), actor, worksmiddleware.IfMatch(context), input,
	)
	if err != nil {
		conditionalServiceError(s, context, "artifact draft", err)
		return
	}
	context.Header("ETag", draft.ETag)
	context.JSON(http.StatusOK, draft)
}

func (s *Server) CreateArtifactRevision(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	var input core.CreateRevisionInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	revision, err := s.services.Artifacts.CreateRevision(
		context.Request.Context(), context.Param("artifactId"), actor, worksmiddleware.IfMatch(context), input,
	)
	if err != nil {
		conditionalServiceError(s, context, "artifact draft", err)
		return
	}
	context.Header("ETag", revisionETag(revision))
	context.Header("Location", "/v1/revisions/"+revision.ID)
	context.JSON(http.StatusCreated, revision)
}

func (s *Server) ListArtifactRevisions(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Artifacts.ListRevisions(context.Request.Context(), context.Param("artifactId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, items)
}

func (s *Server) ListCollectionArtifactRevisions(context *gin.Context) {
	if !s.verifyCollectionArtifact(context) {
		return
	}
	s.ListArtifactRevisions(context)
}

func (s *Server) GetArtifactRevision(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	revision, err := s.services.Artifacts.GetRevision(context.Request.Context(), context.Param("revisionId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", revisionETag(revision))
	context.JSON(http.StatusOK, revision)
}

func (s *Server) CreateCollectionArtifactRevision(context *gin.Context) {
	if !s.verifyCollectionArtifact(context) {
		return
	}
	s.CreateArtifactRevision(context)
}

func (s *Server) ListCollectionArtifacts(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	collection := context.GetString(artifactCollectionKey)
	all, err := s.services.Artifacts.List(context.Request.Context(), context.Param("projectId"), actor, "", strings.TrimSpace(context.Query("status")))
	if err != nil {
		s.businessError(context, err)
		return
	}
	items := make([]core.VersionedArtifact, 0, len(all))
	for _, artifact := range all {
		if !collectionContainsArtifact(collection, artifact.Kind) {
			continue
		}
		versioned, getErr := s.services.Artifacts.Get(context.Request.Context(), artifact.ID, actor, true)
		if getErr != nil {
			s.businessError(context, getErr)
			return
		}
		items = append(items, versioned)
	}
	writePage(context, items)
}

func (s *Server) GetCollectionArtifact(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	artifact, err := s.services.Artifacts.Get(context.Request.Context(), context.Param("artifactId"), actor, true)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if !collectionContainsArtifact(context.GetString(artifactCollectionKey), artifact.Artifact.Kind) {
		problem.WriteError(context, core.ErrNotFound)
		return
	}
	context.Header("ETag", versionedArtifactETag(artifact))
	context.JSON(http.StatusOK, artifact)
}

func (s *Server) CreateCollectionArtifact(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	var input createCollectionArtifactInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	collection := context.GetString(artifactCollectionKey)
	artifactInput, err := collectionArtifactInput(collection, input)
	if err != nil {
		problem.WriteError(context, err)
		return
	}
	created, err := s.services.Artifacts.Create(context.Request.Context(), context.Param("projectId"), actor, artifactInput)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", versionedArtifactETag(created))
	context.Header("Location", "/v1/"+collection+"/"+created.Artifact.ID)
	context.JSON(http.StatusCreated, created)
}

func (s *Server) UpdateCollectionDraft(context *gin.Context) {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return
	}
	var input updateCollectionDraftInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	artifact, err := s.services.Artifacts.Get(context.Request.Context(), context.Param("artifactId"), actor, false)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if !collectionContainsArtifact(context.GetString(artifactCollectionKey), artifact.Artifact.Kind) || artifact.Draft == nil {
		problem.WriteError(context, core.ErrNotFound)
		return
	}
	draft, err := s.services.Artifacts.UpdateDraft(context.Request.Context(), artifact.Draft.ID, actor, worksmiddleware.IfMatch(context), core.UpdateDraftInput{
		SchemaVersion:  input.SchemaVersion,
		Content:        input.Content,
		SourceVersions: sourceInputs(input.SourceVersions, "source"),
	})
	if err != nil {
		conditionalServiceError(s, context, "artifact draft", err)
		return
	}
	updated := artifact
	updated.Draft = &draft
	context.Header("ETag", draft.ETag)
	context.JSON(http.StatusOK, updated)
}

func collectionArtifactInput(collection string, input createCollectionArtifactInput) (core.CreateArtifactInput, error) {
	kind := collectionKind(collection, input.Kind)
	if kind == "" {
		return core.CreateArtifactInput{}, core.ErrInvalidInput
	}
	content := input.Content
	var err error
	switch collection {
	case "documents":
		if input.Kind != "" {
			content, err = setContentField(content, "kind", input.Kind)
		}
	case "page-specs":
		if input.BlueprintPageNodeID != "" {
			content, err = setContentField(content, "blueprintPageNodeId", input.BlueprintPageNodeID)
		}
	case "prototypes":
		if input.PageSpecRevision != nil {
			content, err = setContentField(content, "pageSpecRevision", *input.PageSpecRevision)
		}
		if err == nil {
			content, err = setContentField(content, "exploratory", input.Exploratory)
		}
	}
	if err != nil {
		return core.CreateArtifactInput{}, err
	}
	sources := sourceInputs(input.SourceVersions, "source")
	sources = append(sources, sourceInputs(input.RequirementVersions, "requirements")...)
	if input.BlueprintRevision != nil {
		sources = append(sources, core.ArtifactSourceInput{Ref: *input.BlueprintRevision, Purpose: "blueprint", Required: true})
	}
	if input.PageSpecRevision != nil {
		sources = append(sources, core.ArtifactSourceInput{Ref: *input.PageSpecRevision, Purpose: "page_spec", Required: true})
	}
	return core.CreateArtifactInput{
		Kind: kind, ArtifactKey: input.ArtifactKey, Title: input.Title,
		SchemaVersion: input.SchemaVersion, Content: content, SourceVersions: sources,
	}, nil
}

func setContentField(content json.RawMessage, key string, value any) (json.RawMessage, error) {
	object := map[string]json.RawMessage{}
	if len(content) != 0 {
		if err := json.Unmarshal(content, &object); err != nil {
			return nil, core.ErrInvalidInput
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if existing, exists := object[key]; exists {
		var existingValue, requestedValue any
		if json.Unmarshal(existing, &existingValue) != nil || json.Unmarshal(encoded, &requestedValue) != nil || !reflect.DeepEqual(existingValue, requestedValue) {
			return nil, core.ErrInvalidInput
		}
		return content, nil
	}
	object[key] = encoded
	return json.Marshal(object)
}

func (s *Server) verifyCollectionArtifact(context *gin.Context) bool {
	if s.services.Artifacts == nil {
		serviceUnavailable(context, "artifact")
		return false
	}
	actor, ok := actorID(context)
	if !ok {
		return false
	}
	artifact, err := s.services.Artifacts.Get(context.Request.Context(), context.Param("artifactId"), actor, false)
	if err != nil {
		s.businessError(context, err)
		return false
	}
	if !collectionContainsArtifact(context.GetString(artifactCollectionKey), artifact.Artifact.Kind) {
		problem.WriteError(context, core.ErrNotFound)
		return false
	}
	return true
}

func sourceInputs(refs []core.VersionRef, purpose string) []core.ArtifactSourceInput {
	result := make([]core.ArtifactSourceInput, 0, len(refs))
	for _, reference := range refs {
		result = append(result, core.ArtifactSourceInput{Ref: reference, Purpose: purpose, Required: true})
	}
	return result
}

func collectionKind(collection, documentKind string) string {
	switch collection {
	case "blueprints":
		return "blueprint"
	case "page-specs":
		return "page_spec"
	case "prototypes":
		return "prototype"
	case "documents":
		switch documentKind {
		case "requirement", "featureList", "pageSplit", "":
			return "product_requirements"
		case "apiContract":
			return "api_contract"
		case "decisionLog":
			return "decision_record"
		case "backendDevelopment", "frontendDevelopment", "uiPrototype":
			return "reference_source"
		}
	}
	return ""
}

func collectionContainsArtifact(collection, kind string) bool {
	switch collection {
	case "blueprints":
		return kind == "blueprint"
	case "page-specs":
		return kind == "page_spec"
	case "prototypes":
		return kind == "prototype" || kind == "prototype_flow"
	case "documents":
		switch kind {
		case "project_brief", "product_requirements", "decision_record", "glossary_policy", "reference_source", "change_request", "api_contract", "data_contract", "permission_contract":
			return true
		}
	}
	return false
}

func versionedArtifactETag(artifact core.VersionedArtifact) string {
	return representationETag("versioned-artifact", artifact.Artifact.ID, artifact)
}
