package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type HTTPDeliveryProviderConfig struct {
	BaseURL          string
	BearerToken      string
	RequestTimeout   time.Duration
	MaxResponseBytes int64
}

// HTTPDeliveryProvider delegates cluster mutation to a separately governed
// controller. The controller receives immutable artifact references only; it
// is never given source, model credentials, or interactive sandbox state.
type HTTPDeliveryProvider struct {
	baseURL     *url.URL
	bearerToken string
	client      *http.Client
	maxResponse int64
}

func (provider *HTTPDeliveryProvider) Readiness(ctx context.Context) error {
	endpoint := *provider.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/health/ready"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+provider.bearerToken)
	request.Header.Set("Accept", "application/json")
	response, err := provider.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, provider.maxResponse))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("release delivery controller readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}

func NewHTTPDeliveryProvider(
	config HTTPDeliveryProviderConfig,
	client *http.Client,
) (*HTTPDeliveryProvider, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("release delivery controller URL must be an absolute HTTP(S) origin without credentials, query, or fragment")
	}
	token := strings.TrimSpace(config.BearerToken)
	if len(token) < 32 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("release delivery controller bearer token is invalid")
	}
	if config.RequestTimeout <= 0 || config.RequestTimeout > time.Hour ||
		config.MaxResponseBytes < 1024 || config.MaxResponseBytes > 8<<20 {
		return nil, errors.New("release delivery controller timeout or response limit is invalid")
	}
	if client == nil {
		client = &http.Client{Timeout: config.RequestTimeout}
	} else if client.Timeout == 0 || client.Timeout > config.RequestTimeout {
		copy := *client
		copy.Timeout = config.RequestTimeout
		client = &copy
	}
	return &HTTPDeliveryProvider{
		baseURL: parsed, bearerToken: token, client: client, maxResponse: config.MaxResponseBytes,
	}, nil
}

func (provider *HTTPDeliveryProvider) Preview(
	ctx context.Context,
	request PreviewProviderRequest,
) (PreviewProviderResult, error) {
	payload := struct {
		SchemaVersion string `json:"schemaVersion"`
		RunID         string `json:"runId"`
		ProjectID     string `json:"projectId"`
		Namespace     string `json:"namespace"`
		Bundle        Bundle `json:"releaseBundle"`
	}{
		SchemaVersion: "release-preview-controller-request/v1", RunID: request.RunID,
		ProjectID: request.ProjectID, Namespace: request.Namespace, Bundle: request.Bundle,
	}
	var result PreviewProviderResult
	if err := provider.mutate(ctx, "/v1/previews", request.RunID, payload, &result); err != nil {
		return PreviewProviderResult{}, err
	}
	if !boundedIdentifier(result.Provider, 128) || !boundedIdentifier(result.ProviderRef, 1000) {
		return PreviewProviderResult{}, errors.New("release preview controller returned invalid provider identity")
	}
	checks, err := normalizePreviewChecks(result.Checks)
	if err != nil {
		return PreviewProviderResult{}, fmt.Errorf("release preview controller checks: %w", err)
	}
	if previewChecksPassed(checks) && !checksCoverKinds(checks, "migration", "health", "smoke", "contract", "e2e") {
		return PreviewProviderResult{}, errors.New("passing release preview controller result is missing required checks")
	}
	return result, nil
}

func (provider *HTTPDeliveryProvider) DeployProduction(
	ctx context.Context,
	request ProductionProviderRequest,
) (ProductionProviderResult, error) {
	payload := struct {
		SchemaVersion    string                     `json:"schemaVersion"`
		RunID            string                     `json:"runId"`
		ProjectID        string                     `json:"projectId"`
		Environment      string                     `json:"environment"`
		Operation        DeploymentOperation        `json:"operation"`
		Bundle           Bundle                     `json:"releaseBundle"`
		PreviewReceipt   repository.ExactReference  `json:"previewReceipt"`
		Approval         repository.ExactReference  `json:"promotionApproval"`
		SourceRevision   *repository.ExactReference `json:"sourceRevision,omitempty"`
		ExpectedRevision *repository.ExactReference `json:"expectedRevision,omitempty"`
		ExpectedReceipt  *repository.ExactReference `json:"expectedProductionReceipt,omitempty"`
	}{
		SchemaVersion: "release-production-controller-request/v2", RunID: request.RunID,
		ProjectID: request.ProjectID, Environment: request.Environment, Operation: request.Operation, Bundle: request.Bundle,
		PreviewReceipt:   repository.ExactReference{ID: request.Preview.ID, ContentHash: request.Preview.PayloadHash},
		Approval:         repository.ExactReference{ID: request.Approval.ID, ContentHash: request.Approval.PayloadHash},
		ExpectedRevision: request.ExpectedHead, ExpectedReceipt: request.ExpectedReceipt,
	}
	if payload.Environment == "" {
		payload.Environment = "production"
	}
	if request.Source != nil {
		reference, err := request.Source.ExactReference()
		if err != nil {
			return ProductionProviderResult{}, err
		}
		payload.SourceRevision = &reference
	}
	var result ProductionProviderResult
	if err := provider.mutate(ctx, "/v1/production-deployments", request.RunID, payload, &result); err != nil {
		return ProductionProviderResult{}, err
	}
	if !boundedIdentifier(result.Provider, 128) || !boundedIdentifier(result.ProviderRef, 1000) ||
		len(strings.TrimSpace(result.PublicURL)) > 2000 || strings.ContainsRune(result.PublicURL, '\x00') {
		return ProductionProviderResult{}, errors.New("release production controller returned invalid deployment identity")
	}
	checks, err := normalizePreviewChecks(result.Checks)
	if err != nil {
		return ProductionProviderResult{}, fmt.Errorf("release production controller checks: %w", err)
	}
	if previewChecksPassed(checks) && !boundedIdentifier(result.PublicURL, 2000) {
		return ProductionProviderResult{}, errors.New("healthy release production controller result requires a public URL")
	}
	if previewChecksPassed(checks) && !checksCoverKinds(checks, "health", "rollout") {
		return ProductionProviderResult{}, errors.New("healthy release production controller result is missing rollout checks")
	}
	return result, nil
}

func (provider *HTTPDeliveryProvider) mutate(
	ctx context.Context,
	path, runID string,
	payload any,
	result any,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := *provider.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+provider.bearerToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", runID)
	response, err := provider.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, provider.maxResponse+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(body)) > provider.maxResponse {
		return errors.New("release delivery controller response exceeded its bound")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("release delivery controller returned HTTP %d", response.StatusCode)
	}
	if err := decodeReleaseStrictJSON(body, result); err != nil {
		return fmt.Errorf("decode release delivery controller response: %w", err)
	}
	return nil
}
