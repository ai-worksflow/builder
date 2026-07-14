package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type QualityService struct {
	database *gorm.DB
	contents content.Store
	access   AccessControl
	loader   *RevisionLoader
	sandbox  Sandbox
	tempRoot string
	now      func() time.Time
}

func NewQualityService(database *gorm.DB, contents content.Store, access AccessControl, sandbox Sandbox, tempRoot string) (*QualityService, error) {
	loader, err := NewRevisionLoader(database, contents, access)
	if err != nil {
		return nil, err
	}
	if sandbox == nil {
		return nil, errors.New("quality sandbox is required")
	}
	return &QualityService{database: database, contents: contents, access: access, loader: loader, sandbox: sandbox, tempRoot: tempRoot, now: time.Now}, nil
}

func (s *QualityService) Evaluate(ctx context.Context, projectID, actorID string, input QualityRunInput) (QualityReport, error) {
	if err := ValidateVersionRef(input.WorkspaceRevision); err != nil {
		return QualityReport{}, err
	}
	if input.WorkflowRunID != nil {
		if _, err := uuid.Parse(*input.WorkflowRunID); err != nil {
			return QualityReport{}, Invalid("workflowRunId", "workflowRunId must be a UUID")
		}
	}
	workspace, err := s.loader.LoadFrozenWorkspace(ctx, projectID, actorID, input.WorkspaceRevision, core.ActionEdit)
	if err != nil {
		return QualityReport{}, err
	}
	if input.WorkflowRunID != nil {
		var count int64
		if err := s.database.WithContext(ctx).Model(&storage.WorkflowRunModel{}).
			Where("id = ? AND project_id = ?", *input.WorkflowRunID, projectID).Count(&count).Error; err != nil {
			return QualityReport{}, wrapInternal("validate workflow quality scope", err)
		}
		if count != 1 {
			return QualityReport{}, Invalid("workflowRunId", "workflowRunId must identify a workflow run in the same project")
		}
	}
	startedAt := s.now().UTC()
	runID := uuid.NewString()
	checks, buildArtifact := s.runChecksAndCapture(ctx, workspace)
	diagnostics := make([]Diagnostic, 0)
	for _, check := range checks {
		diagnostics = append(diagnostics, check.Diagnostics...)
	}
	passed := true
	errorCount, warningCount := 0, 0
	for _, finding := range diagnostics {
		switch finding.Severity {
		case SeverityError:
			passed = false
			errorCount++
		case SeverityWarning:
			warningCount++
		}
	}
	score := 100 - errorCount*15 - warningCount*5
	if score < 0 {
		score = 0
	}
	completedAt := s.now().UTC()
	status := "failed"
	if passed {
		status = "passed"
	}
	reportArtifactID, reportRevisionID := uuid.NewString(), uuid.NewString()
	for index := range diagnostics {
		diagnostics[index].ID = uuid.NewString()
	}
	report := QualityReport{
		ID: runID, ProjectID: projectID, WorkflowRunID: input.WorkflowRunID,
		WorkspaceRevision: input.WorkspaceRevision, Status: status, Passed: passed, Score: score,
		RunnerVersion: RunnerVersion, SandboxKind: s.sandbox.Kind(), Checks: checks, Diagnostics: diagnostics,
		ReportArtifactID: reportArtifactID, ReportRevisionID: reportRevisionID,
		CreatedBy: actorID, StartedAt: startedAt, CompletedAt: &completedAt, Version: 1,
	}
	report.ETag = qualityETag(report.ID, report.Version)
	if !report.Passed {
		buildArtifact = nil
	}
	if err := s.persistReport(ctx, &report, buildArtifact); err != nil {
		return QualityReport{}, err
	}
	return report, nil
}

func (s *QualityService) runChecks(ctx context.Context, workspace WorkspaceSnapshot) []CheckResult {
	checks, _ := s.runChecksAndCapture(ctx, workspace)
	return checks
}

func (s *QualityService) runChecksAndCapture(ctx context.Context, workspace WorkspaceSnapshot) ([]CheckResult, *BuildArtifact) {
	safeFiles := make([]WorkspaceFile, 0, len(workspace.Files))
	for _, file := range workspace.Files {
		if SensitivePath(file.Path) {
			continue
		}
		redacted, _ := RedactSensitive(file.Content)
		safeFiles = append(safeFiles, WorkspaceFile{Path: file.Path, Content: redacted, Language: file.Language})
	}
	directory, cleanup, materializeErr := materializeWorkspace(s.tempRoot, safeFiles)
	if cleanup != nil {
		defer cleanup()
	}
	ecosystem, applicable := detectCommandChecks(workspace.Files)
	if materializeErr == nil && applicable[CheckBuild] {
		materializeErr = clearStaticBuildOutputs(directory)
	}
	policy := secureDependencyPolicy(s.sandbox)
	plan, dependencyDiagnostics := validateDependencyWorkspace(workspace.Files, policy)
	dependencyResult := staticCheckResult(CheckDependency, dependencyDiagnostics)
	dependencyDirectory := ""
	if plan.ecosystem == "" {
		dependencyResult = CheckResult{ID: CheckDependency, Status: CheckSkipped, Diagnostics: []Diagnostic{{CheckID: CheckDependency, Code: "not_applicable", Severity: SeverityInfo, Message: "No supported dependency manifest was present."}}}
	} else if !hasErrorDiagnostic(dependencyDiagnostics) && materializeErr == nil {
		preparedDirectory, preparedCleanup, err := materializeDependencyPlan(s.tempRoot, plan)
		if preparedCleanup != nil {
			defer preparedCleanup()
		}
		if err != nil {
			dependencyResult = failedCheck(CheckDependency, "dependency_workspace_failed", err.Error())
		} else if preparer, ok := s.sandbox.(DependencyPreparer); !ok {
			dependencyResult = failedCheck(CheckDependency, "dependency_resolver_unavailable", "The configured sandbox cannot perform isolated dependency preparation.")
		} else {
			prepared, prepareErr := preparer.PrepareDependencies(ctx, preparedDirectory, DependencyPreparationRequest{Ecosystem: plan.ecosystem})
			output, _ := RedactSensitive(prepared.Output)
			dependencyResult.Output = output
			dependencyResult.Truncated = prepared.Truncated
			dependencyResult.DurationMS = prepared.Duration.Milliseconds()
			dependencyResult.ExitCode = &prepared.ExitCode
			if prepareErr != nil || prepared.ExitCode != 0 {
				message := "Fixed dependency preparation failed before source code was mounted."
				code := "dependency_prepare_failed"
				if prepareErr != nil {
					message = prepareErr.Error()
					if typed, ok := AsError(prepareErr); ok {
						code = string(typed.Code)
					}
				}
				dependencyResult.Status = CheckFailed
				dependencyResult.Diagnostics = append(dependencyResult.Diagnostics, Diagnostic{CheckID: CheckDependency, Code: code, Severity: SeverityError, Message: message})
			} else if layoutErr := ensurePreparedDependencyLayout(preparedDirectory, plan.ecosystem); layoutErr != nil {
				dependencyResult.Status = CheckFailed
				dependencyResult.Diagnostics = append(dependencyResult.Diagnostics, Diagnostic{CheckID: CheckDependency, Code: "dependency_layout_invalid", Severity: SeverityError, Message: layoutErr.Error()})
			} else {
				dependencyDirectory = preparedDirectory
			}
			if prepared.Truncated {
				dependencyResult.Diagnostics = append(dependencyResult.Diagnostics, Diagnostic{CheckID: CheckDependency, Code: "output_truncated", Severity: SeverityWarning, Message: "Dependency resolver output exceeded the configured capture limit."})
				if dependencyResult.Status == CheckPassed {
					dependencyResult.Status = CheckWarning
				}
			}
		}
	}
	results := make([]CheckResult, 0, len(RequiredChecks))
	var buildArtifact *BuildArtifact
	for _, checkID := range RequiredChecks {
		started := time.Now()
		var result CheckResult
		switch checkID {
		case CheckBuild, CheckType, CheckLint, CheckTest:
			if checkID != CheckBuild && materializeErr == nil && applicable[checkID] && dependencyResult.Status != CheckFailed {
				materializeErr = resetMaterializedWorkspace(directory, safeFiles)
			}
			if materializeErr != nil {
				result = failedCheck(checkID, "sandbox_workspace_failed", materializeErr.Error())
			} else if !applicable[checkID] {
				result = CheckResult{ID: checkID, Status: CheckSkipped, Diagnostics: []Diagnostic{{CheckID: checkID, Code: "not_applicable", Severity: SeverityInfo, Message: "No fixed quality profile applies to this workspace."}}}
			} else if dependencyResult.Status == CheckFailed {
				result = failedCheck(checkID, "dependency_prepare_blocked", "The fixed command was not run because isolated dependency preparation failed.")
			} else {
				result = s.runSandboxCheck(ctx, directory, dependencyDirectory, ecosystem, checkID)
			}
		case CheckAccessibility:
			result = accessibilityCheck(workspace.Files)
		case CheckDependency:
			result = dependencyResult
		case CheckSecret:
			result = secretCheck(workspace.Files)
		}
		if checkID == CheckBuild && materializeErr == nil && dependencyResult.Status != CheckFailed && result.Status != CheckFailed {
			artifact, err := captureBuildArtifact(directory, workspace.Revision, !applicable[CheckBuild])
			if err != nil {
				result.Status = CheckFailed
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					CheckID: CheckBuild, Code: "static_build_artifact_missing", Severity: SeverityError,
					Message: err.Error(), Suggestion: "Produce a static index.html under dist, out, or build, or provide a root static index.html.",
				})
			} else {
				buildArtifact = &artifact
			}
		}
		if result.Diagnostics == nil {
			result.Diagnostics = []Diagnostic{}
		}
		if result.DurationMS == 0 {
			result.DurationMS = time.Since(started).Milliseconds()
		}
		results = append(results, result)
	}
	return results, buildArtifact
}

func (s *QualityService) runSandboxCheck(ctx context.Context, directory, dependencyDirectory, ecosystem string, checkID CheckID) CheckResult {
	result, err := s.sandbox.Run(ctx, directory, SandboxRequest{Ecosystem: ecosystem, Check: checkID, DependencyDirectory: dependencyDirectory})
	output, _ := RedactSensitive(result.Output)
	check := CheckResult{ID: checkID, ExitCode: &result.ExitCode, DurationMS: result.Duration.Milliseconds(), Output: output, Truncated: result.Truncated, Diagnostics: []Diagnostic{}}
	if err != nil {
		code := "sandbox_failed"
		if deliveryError, ok := AsError(err); ok {
			code = string(deliveryError.Code)
		}
		check.Status = CheckFailed
		check.Diagnostics = append(check.Diagnostics, Diagnostic{CheckID: checkID, Code: code, Severity: SeverityError, Message: err.Error()})
		return check
	}
	if result.ExitCode != 0 || checkID == CheckLint && ecosystem == "go" && strings.TrimSpace(output) != "" {
		check.Status = CheckFailed
		check.Diagnostics = append(check.Diagnostics, Diagnostic{CheckID: checkID, Code: "command_failed", Severity: SeverityError, Message: "The fixed sandbox quality command failed.", Suggestion: "Review the bounded command output and fix the frozen workspace before publishing."})
	} else {
		check.Status = CheckPassed
	}
	if result.Truncated {
		check.Diagnostics = append(check.Diagnostics, Diagnostic{CheckID: checkID, Code: "output_truncated", Severity: SeverityWarning, Message: "Quality command output exceeded the configured capture limit."})
		if check.Status == CheckPassed {
			check.Status = CheckWarning
		}
	}
	return check
}

func detectCommandChecks(files []WorkspaceFile) (string, map[CheckID]bool) {
	byPath := map[string]WorkspaceFile{}
	for _, file := range files {
		byPath[file.Path] = file
	}
	result := map[CheckID]bool{}
	if manifest, exists := byPath["package.json"]; exists {
		var value struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal([]byte(manifest.Content), &value) == nil {
			result[CheckBuild] = strings.TrimSpace(value.Scripts["build"]) != ""
			result[CheckLint] = strings.TrimSpace(value.Scripts["lint"]) != ""
			testScript := strings.TrimSpace(value.Scripts["test"])
			result[CheckTest] = testScript != "" && !strings.Contains(testScript, "no test specified")
		}
		_, result[CheckType] = byPath["tsconfig.json"]
		return "node", result
	}
	if _, exists := byPath["go.mod"]; exists {
		for _, check := range []CheckID{CheckBuild, CheckType, CheckLint, CheckTest} {
			result[check] = true
		}
		return "go", result
	}
	return "", result
}

var (
	htmlTagPattern = regexp.MustCompile(`(?i)<html\b[^>]*>`)
	langPattern    = regexp.MustCompile(`(?i)\blang\s*=`)
	imgPattern     = regexp.MustCompile(`(?i)<img\b[^>]*>`)
	altPattern     = regexp.MustCompile(`(?i)\balt\s*=`)
	buttonPattern  = regexp.MustCompile(`(?is)<button\b([^>]*)>(.*?)</button>`)
	ariaPattern    = regexp.MustCompile(`(?i)\baria-label(?:ledby)?\s*=`)
)

func accessibilityCheck(files []WorkspaceFile) CheckResult {
	diagnostics := []Diagnostic{}
	applicable := false
	for _, file := range files {
		if !strings.HasSuffix(strings.ToLower(file.Path), ".html") && !strings.HasSuffix(strings.ToLower(file.Path), ".htm") {
			continue
		}
		applicable = true
		html := file.Content
		if tag := htmlTagPattern.FindString(html); tag != "" && !langPattern.MatchString(tag) {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckAccessibility, Code: "html_lang_missing", Severity: SeverityError, Message: "The HTML root element must declare a language.", Path: file.Path})
		}
		for _, tag := range imgPattern.FindAllString(html, -1) {
			if !altPattern.MatchString(tag) {
				diagnostics = append(diagnostics, Diagnostic{CheckID: CheckAccessibility, Code: "image_alt_missing", Severity: SeverityError, Message: "Images must provide alt text.", Path: file.Path})
			}
		}
		for _, match := range buttonPattern.FindAllStringSubmatch(html, -1) {
			if strings.TrimSpace(stripTags(match[2])) == "" && !ariaPattern.MatchString(match[1]) {
				diagnostics = append(diagnostics, Diagnostic{CheckID: CheckAccessibility, Code: "button_name_missing", Severity: SeverityError, Message: "Buttons must have an accessible name.", Path: file.Path})
			}
		}
	}
	if !applicable {
		return CheckResult{ID: CheckAccessibility, Status: CheckSkipped, Diagnostics: []Diagnostic{{CheckID: CheckAccessibility, Code: "not_applicable", Severity: SeverityInfo, Message: "No HTML document was present in the frozen workspace."}}}
	}
	return staticCheckResult(CheckAccessibility, diagnostics)
}

func dependencyCheck(files []WorkspaceFile) CheckResult {
	plan, diagnostics := validateDependencyWorkspace(files, DependencyPolicy{NPMRegistry: defaultNPMRegistry, GoProxy: defaultGoProxy, GoSumDB: defaultGoSumDB})
	if plan.ecosystem == "" {
		return CheckResult{ID: CheckDependency, Status: CheckSkipped, Diagnostics: []Diagnostic{{CheckID: CheckDependency, Code: "not_applicable", Severity: SeverityInfo, Message: "No supported dependency manifest was present."}}}
	}
	return staticCheckResult(CheckDependency, diagnostics)
}

func secretCheck(files []WorkspaceFile) CheckResult {
	diagnostics := []Diagnostic{}
	for _, file := range files {
		if SensitivePath(file.Path) {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckSecret, Code: "secret_file", Severity: SeverityError, Message: "Secret-bearing files may not be committed to a publishable workspace.", Path: file.Path})
		}
		if kind, found := SensitiveFinding(file.Content); found {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckSecret, Code: kind, Severity: SeverityError, Message: "A likely secret is embedded in the workspace file.", Path: file.Path, Suggestion: "Move secrets to a server-side environment reference and rotate the exposed value."})
		}
	}
	return staticCheckResult(CheckSecret, diagnostics)
}

func staticCheckResult(id CheckID, diagnostics []Diagnostic) CheckResult {
	status := CheckPassed
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			status = CheckFailed
			break
		}
		if diagnostic.Severity == SeverityWarning {
			status = CheckWarning
		}
	}
	return CheckResult{ID: id, Status: status, Diagnostics: diagnostics}
}

func failedCheck(id CheckID, code, message string) CheckResult {
	return CheckResult{ID: id, Status: CheckFailed, Diagnostics: []Diagnostic{{CheckID: id, Code: code, Severity: SeverityError, Message: message}}}
}

func stripTags(value string) string {
	return regexp.MustCompile(`<[^>]+>`).ReplaceAllString(value, "")
}

func (s *QualityService) persistReport(ctx context.Context, report *QualityReport, buildArtifact *BuildArtifact) error {
	if report == nil {
		return errors.New("quality report is required")
	}
	var buildContentRef content.Reference
	if buildArtifact != nil {
		if !report.Passed {
			return conflict("failed quality reports cannot reference a publishable build artifact")
		}
		if !exactVersionRefEqual(buildArtifact.WorkspaceRevision, report.WorkspaceRevision) {
			return conflict("build artifact does not match the quality workspace revision")
		}
		if err := validateBuildArtifact(*buildArtifact); err != nil {
			return err
		}
		buildPayload, err := json.Marshal(buildArtifact)
		if err != nil {
			return wrapInternal("encode immutable build artifact", err)
		}
		buildContentRef, err = s.contents.PutPending(ctx, report.ProjectID, "quality_build_artifact", buildArtifact.ID, 1, buildPayload)
		if err != nil {
			return wrapInternal("store immutable build artifact", err)
		}
		reference := referenceForBuild(*buildArtifact, buildContentRef.ID, buildContentRef.ContentHash)
		report.BuildArtifact = &reference
	}
	payload, err := json.Marshal(report)
	if err != nil {
		if buildContentRef.ID != "" {
			_ = s.contents.Abort(context.Background(), buildContentRef.ID)
		}
		return wrapInternal("encode quality report", err)
	}
	contentRef, err := s.contents.PutPending(ctx, report.ProjectID, "quality_report_revision", report.ReportRevisionID, 1, payload)
	if err != nil {
		if buildContentRef.ID != "" {
			_ = s.contents.Abort(context.Background(), buildContentRef.ID)
		}
		return wrapInternal("store quality report content", err)
	}
	abort := true
	defer func() {
		if abort {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
			if buildContentRef.ID != "" {
				_ = s.contents.Abort(context.Background(), buildContentRef.ID)
			}
		}
	}()
	projectID, _ := uuid.Parse(report.ProjectID)
	actorID, _ := uuid.Parse(report.CreatedBy)
	runID, _ := uuid.Parse(report.ID)
	workspaceArtifactID, _ := uuid.Parse(report.WorkspaceRevision.ArtifactID)
	workspaceRevisionID, _ := uuid.Parse(report.WorkspaceRevision.RevisionID)
	reportArtifactID, _ := uuid.Parse(report.ReportArtifactID)
	reportRevisionID, _ := uuid.Parse(report.ReportRevisionID)
	var workflowRunID *uuid.UUID
	if report.WorkflowRunID != nil {
		parsed, _ := uuid.Parse(*report.WorkflowRunID)
		workflowRunID = &parsed
	}
	now := report.StartedAt
	artifact := storage.ArtifactModel{
		ID: reportArtifactID, ProjectID: projectID, Kind: "quality_report",
		ArtifactKey: "QUALITY-" + strings.ToUpper(strings.ReplaceAll(report.ID, "-", "")[:20]),
		Title:       "Quality report for workspace revision " + report.WorkspaceRevision.RevisionID,
		Lifecycle:   "active", Version: 1, CreatedBy: actorID, CreatedAt: now, UpdatedAt: *report.CompletedAt,
	}
	revision := storage.ArtifactRevisionModel{
		ID: reportRevisionID, ArtifactID: reportArtifactID, RevisionNumber: 1, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		ByteSize: contentRef.ByteSize, WorkflowStatus: "approved", ChangeSource: "system",
		ChangeSummary: "Evaluate frozen workspace revision " + report.WorkspaceRevision.RevisionID,
		CreatedBy:     actorID, CreatedAt: *report.CompletedAt, ApprovedAt: report.CompletedAt,
	}
	var buildArtifactID *uuid.UUID
	if report.BuildArtifact != nil {
		parsed, err := uuid.Parse(report.BuildArtifact.ID)
		if err != nil {
			return conflict("quality build artifact id is invalid")
		}
		buildArtifactID = &parsed
	}
	draftID := uuid.New()
	draft := storage.ArtifactDraftModel{
		ID: draftID, ArtifactID: reportArtifactID, BaseRevisionID: &reportRevisionID,
		Sequence: 1, ETag: fmt.Sprintf(`"draft:%s:1:%s"`, draftID, contentRef.ContentHash),
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		ByteSize: contentRef.ByteSize, Status: "draft", CreatedBy: actorID, UpdatedBy: actorID,
		CreatedAt: *report.CompletedAt, UpdatedAt: *report.CompletedAt,
	}
	run := qualityRunModel{
		ID: runID, ProjectID: projectID, WorkflowRunID: workflowRunID,
		WorkspaceArtifactID: workspaceArtifactID, WorkspaceRevisionID: workspaceRevisionID,
		WorkspaceContentHash: report.WorkspaceRevision.ContentHash,
		ReportArtifactID:     &reportArtifactID, ReportRevisionID: &reportRevisionID,
		Status: report.Status, Score: report.Score, RunnerVersion: report.RunnerVersion,
		SandboxKind: report.SandboxKind, Version: report.Version, CreatedBy: actorID,
		StartedAt: report.StartedAt, CompletedAt: report.CompletedAt, CreatedAt: report.StartedAt,
	}
	if report.BuildArtifact != nil {
		run.BuildArtifactID = buildArtifactID
		run.BuildContentRef = &report.BuildArtifact.ContentRef
		run.BuildContentHash = &report.BuildArtifact.ContentHash
		run.BuildHash = &report.BuildArtifact.BuildHash
		run.BuildEntryPath = &report.BuildArtifact.EntryPath
		run.BuildFileCount = &report.BuildArtifact.FileCount
		run.BuildTotalBytes = &report.BuildArtifact.TotalBytes
	}
	diagnosticModels := make([]qualityDiagnosticModel, 0, len(report.Diagnostics))
	for _, diagnostic := range report.Diagnostics {
		model := qualityDiagnosticModel{
			ID: uuid.MustParse(diagnostic.ID), QualityRunID: runID, CheckID: string(diagnostic.CheckID),
			Code: diagnostic.Code, Severity: string(diagnostic.Severity), Message: diagnostic.Message,
			CreatedAt: *report.CompletedAt,
		}
		if diagnostic.Path != "" {
			model.Path = &diagnostic.Path
		}
		if diagnostic.Line > 0 {
			model.Line = &diagnostic.Line
		}
		if diagnostic.Column > 0 {
			model.ColumnNumber = &diagnostic.Column
		}
		if diagnostic.Suggestion != "" {
			model.Suggestion = &diagnostic.Suggestion
		}
		diagnosticModels = append(diagnosticModels, model)
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&artifact).Error; err != nil {
			return err
		}
		if err := transaction.Create(&revision).Error; err != nil {
			return err
		}
		if err := transaction.Create(&draft).Error; err != nil {
			return err
		}
		if _, err := core.PersistSystemRevisionLineage(
			transaction,
			projectID,
			reportArtifactID,
			reportRevisionID,
			draftID,
			actorID,
			*report.CompletedAt,
			[]core.SystemRevisionSource{{
				Ref: report.WorkspaceRevision, Purpose: "quality_workspace",
				Required: true, Relation: "verified_by",
			}},
		); err != nil {
			return err
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", reportArtifactID).Updates(map[string]any{
			"latest_draft_id": draftID, "latest_revision_id": reportRevisionID, "latest_approved_revision_id": reportRevisionID,
		}).Error; err != nil {
			return err
		}
		if err := transaction.Create(&run).Error; err != nil {
			return err
		}
		if len(diagnosticModels) > 0 {
			if err := transaction.Create(&diagnosticModels).Error; err != nil {
				return err
			}
		}
		deliveryStatus := "complete"
		blockingCount := 0
		if !report.Passed {
			deliveryStatus = "blocked"
			for _, diagnostic := range report.Diagnostics {
				if diagnostic.Severity == SeverityError {
					blockingCount++
				}
			}
		}
		reportHealth := storage.ArtifactHealthModel{
			ArtifactID: reportArtifactID, SyncStatus: "current", DeliveryStatus: deliveryStatus,
			FindingCount: len(report.Diagnostics), BlockingCount: blockingCount,
			Report: payload, ComputedAt: *report.CompletedAt,
		}
		if err := transaction.Create(&reportHealth).Error; err != nil {
			return err
		}
		health := storage.ArtifactHealthModel{ArtifactID: workspaceArtifactID, SyncStatus: "current", DeliveryStatus: deliveryStatus, FindingCount: len(report.Diagnostics), BlockingCount: blockingCount, Report: payload, ComputedAt: *report.CompletedAt}
		if err := transaction.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "artifact_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"sync_status", "delivery_status", "finding_count", "blocking_count", "report", "computed_at"}),
		}).Create(&health).Error; err != nil {
			return err
		}
		return recordAuditAndOutbox(ctx, transaction, projectID, actorID,
			"quality.completed", "quality_run", report.ID, "quality.completed", "worksflow.quality.completed",
			map[string]any{"projectId": report.ProjectID, "qualityRunId": report.ID, "workspaceRevisionId": report.WorkspaceRevision.RevisionID, "passed": report.Passed, "score": report.Score},
		)
	})
	if err != nil {
		return wrapInternal("persist quality report", err)
	}
	abort = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return fmt.Errorf("%w: %v", core.ErrContentNotReady, err)
	}
	if buildContentRef.ID != "" {
		if err := s.contents.Finalize(ctx, buildContentRef.ID); err != nil {
			return fmt.Errorf("%w: %v", core.ErrContentNotReady, err)
		}
	}
	return nil
}

func (s *QualityService) Get(ctx context.Context, runID, actorID string) (QualityReport, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return QualityReport{}, Invalid("qualityRunId", "qualityRunId must be a UUID")
	}
	var model qualityRunModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return QualityReport{}, notFound("quality run was not found")
		}
		return QualityReport{}, wrapInternal("load quality run", err)
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, core.ActionView); err != nil {
		return QualityReport{}, err
	}
	return s.reportFromModel(ctx, model)
}

func (s *QualityService) List(ctx context.Context, projectID, actorID, workspaceRevisionID string) ([]QualityReport, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, Invalid("projectId", "projectId must be a UUID")
	}
	query := s.database.WithContext(ctx).Where("project_id = ?", projectUUID)
	if workspaceRevisionID != "" {
		parsed, err := uuid.Parse(workspaceRevisionID)
		if err != nil {
			return nil, Invalid("workspaceRevisionId", "workspaceRevisionId must be a UUID")
		}
		query = query.Where("workspace_revision_id = ?", parsed)
	}
	var models []qualityRunModel
	if err := query.Order("created_at DESC").Limit(200).Find(&models).Error; err != nil {
		return nil, wrapInternal("list quality runs", err)
	}
	result := make([]QualityReport, 0, len(models))
	for _, model := range models {
		report, err := s.reportFromModel(ctx, model)
		if err != nil {
			return nil, err
		}
		result = append(result, report)
	}
	return result, nil
}

func (s *QualityService) LatestPassingForWorkflow(ctx context.Context, projectID, workflowRunID, actorID string) (QualityReport, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return QualityReport{}, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return QualityReport{}, Invalid("projectId", "projectId must be a UUID")
	}
	runUUID, err := uuid.Parse(workflowRunID)
	if err != nil {
		return QualityReport{}, Invalid("workflowRunId", "workflowRunId must be a UUID")
	}
	var model qualityRunModel
	if err := s.database.WithContext(ctx).Where("project_id = ? AND workflow_run_id = ? AND status = 'passed' AND build_artifact_id IS NOT NULL", projectUUID, runUUID).Order("created_at DESC").Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return QualityReport{}, notFound("a passing quality report was not found for the workflow run")
		}
		return QualityReport{}, wrapInternal("load workflow quality result", err)
	}
	return s.reportFromModel(ctx, model)
}

func (s *QualityService) LatestPassingForRevision(ctx context.Context, projectID, workspaceRevisionID, actorID string) (QualityReport, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return QualityReport{}, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return QualityReport{}, Invalid("projectId", "projectId must be a UUID")
	}
	revisionUUID, err := uuid.Parse(workspaceRevisionID)
	if err != nil {
		return QualityReport{}, Invalid("workspaceRevisionId", "workspaceRevisionId must be a UUID")
	}
	var model qualityRunModel
	if err := s.database.WithContext(ctx).Where("project_id = ? AND workspace_revision_id = ? AND status = 'passed' AND build_artifact_id IS NOT NULL", projectUUID, revisionUUID).Order("created_at DESC").Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return QualityReport{}, notFound("a passing quality report was not found for the workspace revision")
		}
		return QualityReport{}, wrapInternal("load workspace quality result", err)
	}
	return s.reportFromModel(ctx, model)
}

func (s *QualityService) reportFromModel(ctx context.Context, model qualityRunModel) (QualityReport, error) {
	if model.ReportRevisionID == nil {
		return QualityReport{}, conflict("quality run has no immutable report revision")
	}
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ?", *model.ReportRevisionID).Take(&revision).Error; err != nil {
		return QualityReport{}, wrapInternal("load quality report revision", err)
	}
	stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
	if err != nil {
		return QualityReport{}, wrapInternal("load quality report content", err)
	}
	var report QualityReport
	if err := json.Unmarshal(stored.Payload, &report); err != nil {
		return QualityReport{}, wrapInternal("decode quality report", err)
	}
	if report.ID != model.ID.String() || report.Version != model.Version || report.Status != model.Status || report.ReportRevisionID != model.ReportRevisionID.String() {
		return QualityReport{}, conflict("quality report content does not match its metadata")
	}
	if model.BuildArtifactID != nil {
		if report.BuildArtifact == nil || model.BuildContentRef == nil || model.BuildContentHash == nil || model.BuildHash == nil || model.BuildEntryPath == nil || model.BuildFileCount == nil || model.BuildTotalBytes == nil {
			return QualityReport{}, conflict("quality report build artifact metadata is incomplete")
		}
		expected := BuildArtifactReference{
			ID: model.BuildArtifactID.String(), ContentRef: *model.BuildContentRef,
			ContentHash: *model.BuildContentHash, BuildHash: *model.BuildHash,
			EntryPath: *model.BuildEntryPath, FileCount: *model.BuildFileCount, TotalBytes: *model.BuildTotalBytes,
		}
		if *report.BuildArtifact != expected {
			return QualityReport{}, conflict("quality report build artifact does not match its persisted relation")
		}
	} else if report.BuildArtifact != nil {
		return QualityReport{}, conflict("quality report payload contains an unpersisted build artifact")
	}
	report.ETag = qualityETag(report.ID, report.Version)
	return report, nil
}

func (s *QualityService) LoadBuildArtifact(ctx context.Context, projectID string, reference BuildArtifactReference) (BuildArtifact, error) {
	if err := validateBuildReference(reference); err != nil {
		return BuildArtifact{}, err
	}
	stored, err := s.contents.Get(ctx, reference.ContentRef, reference.ContentHash)
	if err != nil {
		return BuildArtifact{}, wrapInternal("load immutable quality build artifact", err)
	}
	if stored.ProjectID != projectID || stored.AggregateType != "quality_build_artifact" || stored.AggregateID != reference.ID {
		return BuildArtifact{}, conflict("quality build artifact content is outside its persisted project relation")
	}
	var artifact BuildArtifact
	if err := json.Unmarshal(stored.Payload, &artifact); err != nil {
		return BuildArtifact{}, wrapInternal("decode immutable quality build artifact", err)
	}
	if err := validateBuildArtifact(artifact); err != nil {
		return BuildArtifact{}, err
	}
	if !buildArtifactMatchesReference(artifact, reference) {
		return BuildArtifact{}, conflict("quality build artifact payload does not match its persisted reference")
	}
	return artifact, nil
}

func qualityETag(id string, version uint64) string {
	return fmt.Sprintf(`"quality-run:%s:%d"`, id, version)
}

func sortedStrings(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
