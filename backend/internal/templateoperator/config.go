package templateoperator

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const (
	ConfigSchemaVersion    = "template-artifact-authority-config/v1"
	AdmissionSchemaVersion = "template-artifact-authority-admission/v1"

	maxAuthorityConfigBytes = 1 << 20
	maxPublicKeyBytes       = 64 << 10
)

var (
	environmentNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,127}$`)
	digestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Config is an operator-owned, immutable Artifact Authority policy. It holds
// public trust material by file reference and names the environment variables
// containing registry credentials; neither private keys nor credentials are
// accepted in an admission request.
type Config struct {
	SchemaVersion string             `json:"schemaVersion"`
	Authority     AuthorityConfig    `json:"authority"`
	Source        SourceConfig       `json:"source"`
	Registry      RegistryConfig     `json:"registry"`
	DSSE          DSSEConfig         `json:"dsse"`
	Transparency  TransparencyConfig `json:"transparency"`
}

type AuthorityConfig struct {
	ID                      string `json:"id"`
	Version                 string `json:"version"`
	VerifierImageDigest     string `json:"verifierImageDigest"`
	PredicateType           string `json:"predicateType"`
	ExpectedPolicyHash      string `json:"expectedPolicyHash"`
	ExpectedTrustRootDigest string `json:"expectedTrustRootDigest"`
}

type SourceConfig struct {
	GitBinary    string   `json:"gitBinary"`
	CacheRoot    string   `json:"cacheRoot"`
	AllowedHosts []string `json:"allowedHosts"`
	FetchTimeout string   `json:"fetchTimeout"`
}

type RegistryConfig struct {
	Origins          []RegistryOriginConfig     `json:"origins"`
	Repositories     []RegistryRepositoryConfig `json:"repositories"`
	MaxManifestBytes int64                      `json:"maxManifestBytes"`
	MaxBlobBytes     int64                      `json:"maxBlobBytes"`
	MaxTotalBytes    int64                      `json:"maxTotalBytes"`
	MaxBlobs         int                        `json:"maxBlobs"`
	MaxRedirects     int                        `json:"maxRedirects"`
	Timeout          string                     `json:"timeout"`
}

type RegistryOriginConfig struct {
	Host             string   `json:"host"`
	AuthorizationEnv string   `json:"authorizationEnv,omitempty"`
	RedirectHosts    []string `json:"redirectHosts"`
}

type RegistryRepositoryConfig struct {
	Host       string `json:"host"`
	Repository string `json:"repository"`
}

type DSSEConfig struct {
	Keys                  []TrustedKeyConfig `json:"keys"`
	AllowedPayloadTypes   []string           `json:"allowedPayloadTypes"`
	AllowedPredicateTypes []string           `json:"allowedPredicateTypes"`
	MinSignatures         int                `json:"minSignatures"`
}

type TransparencyConfig struct {
	Logs          []TransparencyLogConfig `json:"logs"`
	MaxEntryAge   string                  `json:"maxEntryAge"`
	MaxFutureSkew string                  `json:"maxFutureSkew"`
}

type TransparencyLogConfig struct {
	ID   string             `json:"id"`
	Keys []TrustedKeyConfig `json:"keys"`
}

type TrustedKeyConfig struct {
	KeyID         string `json:"keyId"`
	Algorithm     string `json:"algorithm"`
	Identity      string `json:"identity"`
	PublicKeyFile string `json:"publicKeyFile"`
}

type Commitments struct {
	SchemaVersion   string `json:"schemaVersion"`
	PolicyHash      string `json:"policyHash"`
	TrustRootDigest string `json:"trustRootDigest"`
}

type compiledConfig struct {
	config             Config
	commitments        Commitments
	redirectHosts      []string
	sourceTimeout      time.Duration
	registryTimeout    time.Duration
	maxEntryAge        time.Duration
	maxFutureSkew      time.Duration
	dssePolicy         templateauthority.DSSETrustPolicy
	transparencyPolicy templateauthority.TransparencyTrustPolicy
}

type compiledKey struct {
	KeyID           string
	Algorithm       templateauthority.SignatureAlgorithm
	Identity        string
	PublicKey       any
	PublicKeyDigest string
}

type trustRootDocument struct {
	SchemaVersion string                    `json:"schemaVersion"`
	DSSE          []trustRootKeyDocument    `json:"dsse"`
	Transparency  []trustRootLogKeyDocument `json:"transparency"`
}

type trustRootKeyDocument struct {
	KeyID           string `json:"keyId"`
	Algorithm       string `json:"algorithm"`
	Identity        string `json:"identity"`
	PublicKeyDigest string `json:"publicKeyDigest"`
}

type trustRootLogKeyDocument struct {
	LogID string                 `json:"logId"`
	Keys  []trustRootKeyDocument `json:"keys"`
}

type policyDocument struct {
	SchemaVersion       string                     `json:"schemaVersion"`
	AuthorityID         string                     `json:"authorityId"`
	AuthorityVersion    string                     `json:"authorityVersion"`
	VerifierImageDigest string                     `json:"verifierImageDigest"`
	PredicateType       string                     `json:"predicateType"`
	TrustRootDigest     string                     `json:"trustRootDigest"`
	Source              policySourceDocument       `json:"source"`
	Registry            policyRegistryDocument     `json:"registry"`
	DSSE                policyDSSEDocument         `json:"dsse"`
	Transparency        policyTransparencyDocument `json:"transparency"`
}

type policySourceDocument struct {
	AllowedHosts []string `json:"allowedHosts"`
	FetchTimeout string   `json:"fetchTimeout"`
}

type policyRegistryDocument struct {
	Origins          []policyRegistryOriginDocument `json:"origins"`
	Repositories     []RegistryRepositoryConfig     `json:"repositories"`
	MaxManifestBytes int64                          `json:"maxManifestBytes"`
	MaxBlobBytes     int64                          `json:"maxBlobBytes"`
	MaxTotalBytes    int64                          `json:"maxTotalBytes"`
	MaxBlobs         int                            `json:"maxBlobs"`
	MaxRedirects     int                            `json:"maxRedirects"`
	Timeout          string                         `json:"timeout"`
}

type policyRegistryOriginDocument struct {
	Host                  string   `json:"host"`
	RequiresAuthorization bool     `json:"requiresAuthorization"`
	RedirectHosts         []string `json:"redirectHosts"`
}

type policyDSSEDocument struct {
	Keys                  []trustRootKeyDocument `json:"keys"`
	AllowedPayloadTypes   []string               `json:"allowedPayloadTypes"`
	AllowedPredicateTypes []string               `json:"allowedPredicateTypes"`
	MinSignatures         int                    `json:"minSignatures"`
}

type policyTransparencyDocument struct {
	Logs          []trustRootLogKeyDocument `json:"logs"`
	MaxEntryAge   string                    `json:"maxEntryAge"`
	MaxFutureSkew string                    `json:"maxFutureSkew"`
}

// DecodeConfig rejects unknown fields, duplicate JSON names, trailing values,
// and oversized input before any filesystem or network dependency is touched.
func DecodeConfig(encoded []byte) (Config, error) {
	if len(encoded) == 0 || len(encoded) > maxAuthorityConfigBytes {
		return Config{}, fmt.Errorf("Template Artifact Authority config must be between 1 and %d bytes", maxAuthorityConfigBytes)
	}
	var config Config
	if err := decodeStrictJSON(encoded, &config); err != nil {
		return Config{}, fmt.Errorf("decode Template Artifact Authority config: %w", err)
	}
	return config, nil
}

func LoadConfig(path string) (Config, error) {
	encoded, err := readRegularFile(path, maxAuthorityConfigBytes)
	if err != nil {
		return Config{}, fmt.Errorf("read Template Artifact Authority config: %w", err)
	}
	return DecodeConfig(encoded)
}

// DeriveCommitments computes the two values that must be reviewed and pinned
// in Config before the authority is allowed to write. Registry credentials and
// database state do not participate in this offline operation.
func DeriveCommitments(config Config) (Commitments, error) {
	compiled, err := compileConfig(config)
	if err != nil {
		return Commitments{}, err
	}
	return compiled.commitments, nil
}

func compileConfig(config Config) (compiledConfig, error) {
	if config.SchemaVersion != ConfigSchemaVersion {
		return compiledConfig{}, fmt.Errorf("schemaVersion must equal %q", ConfigSchemaVersion)
	}
	if err := requireCanonicalText(config.Authority.ID, "authority.id", 240); err != nil {
		return compiledConfig{}, err
	}
	if err := requireCanonicalText(config.Authority.Version, "authority.version", 120); err != nil {
		return compiledConfig{}, err
	}
	if err := requireCanonicalText(config.Authority.PredicateType, "authority.predicateType", 500); err != nil {
		return compiledConfig{}, err
	}
	if !digestPattern.MatchString(config.Authority.VerifierImageDigest) {
		return compiledConfig{}, errors.New("authority.verifierImageDigest must be a canonical sha256 digest")
	}

	sourceTimeout, err := parseBoundedDuration(config.Source.FetchTimeout, "source.fetchTimeout", time.Millisecond, 10*time.Minute)
	if err != nil {
		return compiledConfig{}, err
	}
	if config.Source.GitBinary == "" || strings.TrimSpace(config.Source.GitBinary) != config.Source.GitBinary || strings.ContainsAny(config.Source.GitBinary, "\r\n\x00") {
		return compiledConfig{}, errors.New("source.gitBinary must be a canonical executable name or path")
	}
	if !filepath.IsAbs(config.Source.CacheRoot) || filepath.Clean(config.Source.CacheRoot) != config.Source.CacheRoot {
		return compiledConfig{}, errors.New("source.cacheRoot must be an absolute normalized path")
	}
	allowedHosts, err := sortedUniqueCanonical(config.Source.AllowedHosts, "source.allowedHosts")
	if err != nil || len(allowedHosts) == 0 {
		if err != nil {
			return compiledConfig{}, err
		}
		return compiledConfig{}, errors.New("source.allowedHosts must not be empty")
	}
	for _, host := range allowedHosts {
		if strings.ToLower(host) != host || strings.ContainsAny(host, "/:@") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
			return compiledConfig{}, fmt.Errorf("source.allowedHosts contains invalid canonical host %q", host)
		}
	}

	registryTimeout, err := parseBoundedDuration(config.Registry.Timeout, "registry.timeout", time.Millisecond, 10*time.Minute)
	if err != nil {
		return compiledConfig{}, err
	}
	if config.Registry.MaxManifestBytes <= 0 || config.Registry.MaxBlobBytes <= 0 || config.Registry.MaxTotalBytes <= 0 ||
		config.Registry.MaxBlobs <= 0 || config.Registry.MaxRedirects <= 0 {
		return compiledConfig{}, errors.New("registry byte, blob, and redirect limits must all be positive")
	}
	if config.Registry.MaxManifestBytes > config.Registry.MaxBlobBytes || config.Registry.MaxBlobBytes > config.Registry.MaxTotalBytes {
		return compiledConfig{}, errors.New("registry limits must satisfy manifest <= blob <= total")
	}

	origins, originDocuments, redirectHosts, err := normalizeOrigins(config.Registry.Origins)
	if err != nil {
		return compiledConfig{}, err
	}
	repositories, err := normalizeRepositories(config.Registry.Repositories, origins)
	if err != nil {
		return compiledConfig{}, err
	}
	validationOrigins := make([]templateauthority.RegistryHTTPOrigin, 0, len(origins))
	for _, origin := range origins {
		validationOrigins = append(validationOrigins, templateauthority.RegistryHTTPOrigin{
			Host: origin.Host, RedirectHosts: origin.RedirectHosts,
		})
	}
	validationClient, err := templateauthority.NewHTTPSRegistryClient(templateauthority.HTTPSRegistryClientConfig{
		Origins: validationOrigins, Timeout: registryTimeout, MaxRedirects: config.Registry.MaxRedirects,
	})
	if err != nil {
		return compiledConfig{}, fmt.Errorf("invalid registry transport policy: %w", err)
	}
	validationRepositories := make([]templateauthority.RepositoryRule, 0, len(repositories))
	for _, repository := range repositories {
		validationRepositories = append(validationRepositories, templateauthority.RepositoryRule{
			Host: repository.Host, Repository: repository.Repository,
		})
	}
	if _, err := templateauthority.NewOCIVerifier(validationClient, templateauthority.RegistryPolicy{
		Repositories: validationRepositories, RedirectHosts: redirectHosts,
	}, templateauthority.Limits{
		MaxManifestBytes: config.Registry.MaxManifestBytes,
		MaxBlobBytes:     config.Registry.MaxBlobBytes,
		MaxTotalBytes:    config.Registry.MaxTotalBytes,
		MaxBlobs:         config.Registry.MaxBlobs,
		MaxRedirects:     config.Registry.MaxRedirects,
		Timeout:          registryTimeout,
	}); err != nil {
		return compiledConfig{}, fmt.Errorf("invalid registry verification policy: %w", err)
	}

	dsseKeys, err := compileKeys(config.DSSE.Keys, "dsse.keys")
	if err != nil {
		return compiledConfig{}, err
	}
	payloadTypes, err := sortedUniqueCanonical(config.DSSE.AllowedPayloadTypes, "dsse.allowedPayloadTypes")
	if err != nil || len(payloadTypes) == 0 {
		if err != nil {
			return compiledConfig{}, err
		}
		return compiledConfig{}, errors.New("dsse.allowedPayloadTypes must not be empty")
	}
	predicateTypes, err := sortedUniqueCanonical(config.DSSE.AllowedPredicateTypes, "dsse.allowedPredicateTypes")
	if err != nil || len(predicateTypes) == 0 {
		if err != nil {
			return compiledConfig{}, err
		}
		return compiledConfig{}, errors.New("dsse.allowedPredicateTypes must not be empty")
	}
	if !containsExact(predicateTypes, config.Authority.PredicateType) {
		return compiledConfig{}, errors.New("authority.predicateType must be present in dsse.allowedPredicateTypes")
	}
	dssePolicy := templateauthority.DSSETrustPolicy{
		Keys:                make(map[string]templateauthority.TrustedSigner, len(dsseKeys)),
		AllowedPayloadTypes: payloadTypes, AllowedPredicateTypes: predicateTypes,
		MinSignatures: config.DSSE.MinSignatures,
	}
	for _, key := range dsseKeys {
		dssePolicy.Keys[key.KeyID] = templateauthority.TrustedSigner{Algorithm: key.Algorithm, PublicKey: key.PublicKey, Identity: key.Identity}
	}
	if _, err := templateauthority.NewDSSEVerifier(dssePolicy); err != nil {
		return compiledConfig{}, fmt.Errorf("invalid DSSE policy: %w", err)
	}

	maxEntryAge, err := parseBoundedDuration(config.Transparency.MaxEntryAge, "transparency.maxEntryAge", time.Second, 365*24*time.Hour)
	if err != nil {
		return compiledConfig{}, err
	}
	maxFutureSkew, err := parseBoundedDuration(config.Transparency.MaxFutureSkew, "transparency.maxFutureSkew", 0, 24*time.Hour)
	if err != nil {
		return compiledConfig{}, err
	}
	if maxFutureSkew > maxEntryAge {
		return compiledConfig{}, errors.New("transparency.maxFutureSkew must not exceed maxEntryAge")
	}
	transparencyPolicy, trustLogs, err := compileTransparencyLogs(config.Transparency.Logs, maxEntryAge, maxFutureSkew)
	if err != nil {
		return compiledConfig{}, err
	}
	if _, err := templateauthority.NewTransparencyVerifier(transparencyPolicy); err != nil {
		return compiledConfig{}, fmt.Errorf("invalid transparency policy: %w", err)
	}

	dsseTrustKeys := trustKeyDocuments(dsseKeys)
	trustRoot := trustRootDocument{
		SchemaVersion: "template-artifact-authority-trust-root/v1",
		DSSE:          dsseTrustKeys, Transparency: trustLogs,
	}
	trustRootDigest, err := canonicalDigest(trustRoot)
	if err != nil {
		return compiledConfig{}, err
	}
	policy := policyDocument{
		SchemaVersion: "template-artifact-authority-policy/v1",
		AuthorityID:   config.Authority.ID, AuthorityVersion: config.Authority.Version,
		VerifierImageDigest: config.Authority.VerifierImageDigest,
		PredicateType:       config.Authority.PredicateType, TrustRootDigest: trustRootDigest,
		Source: policySourceDocument{AllowedHosts: allowedHosts, FetchTimeout: sourceTimeout.String()},
		Registry: policyRegistryDocument{
			Origins: originDocuments, Repositories: repositories,
			MaxManifestBytes: config.Registry.MaxManifestBytes, MaxBlobBytes: config.Registry.MaxBlobBytes,
			MaxTotalBytes: config.Registry.MaxTotalBytes, MaxBlobs: config.Registry.MaxBlobs,
			MaxRedirects: config.Registry.MaxRedirects, Timeout: registryTimeout.String(),
		},
		DSSE: policyDSSEDocument{
			Keys: dsseTrustKeys, AllowedPayloadTypes: payloadTypes,
			AllowedPredicateTypes: predicateTypes, MinSignatures: config.DSSE.MinSignatures,
		},
		Transparency: policyTransparencyDocument{
			Logs: trustLogs, MaxEntryAge: maxEntryAge.String(), MaxFutureSkew: maxFutureSkew.String(),
		},
	}
	policyHash, err := canonicalDigest(policy)
	if err != nil {
		return compiledConfig{}, err
	}

	normalized := config
	normalized.Source.AllowedHosts = allowedHosts
	normalized.Source.FetchTimeout = sourceTimeout.String()
	normalized.Registry.Origins = origins
	normalized.Registry.Repositories = repositories
	normalized.Registry.Timeout = registryTimeout.String()
	normalized.DSSE.Keys = normalizedKeyConfigs(config.DSSE.Keys)
	normalized.DSSE.AllowedPayloadTypes = payloadTypes
	normalized.DSSE.AllowedPredicateTypes = predicateTypes
	normalized.Transparency.Logs = normalizedTransparencyLogConfigs(config.Transparency.Logs)
	normalized.Transparency.MaxEntryAge = maxEntryAge.String()
	normalized.Transparency.MaxFutureSkew = maxFutureSkew.String()
	return compiledConfig{
		config:        normalized,
		commitments:   Commitments{SchemaVersion: "template-artifact-authority-commitments/v1", PolicyHash: policyHash, TrustRootDigest: trustRootDigest},
		redirectHosts: redirectHosts,
		sourceTimeout: sourceTimeout, registryTimeout: registryTimeout,
		maxEntryAge: maxEntryAge, maxFutureSkew: maxFutureSkew,
		dssePolicy: dssePolicy, transparencyPolicy: transparencyPolicy,
	}, nil
}

func normalizeOrigins(input []RegistryOriginConfig) ([]RegistryOriginConfig, []policyRegistryOriginDocument, []string, error) {
	if len(input) == 0 {
		return nil, nil, nil, errors.New("registry.origins must not be empty")
	}
	result := append([]RegistryOriginConfig(nil), input...)
	sort.Slice(result, func(i, j int) bool { return result[i].Host < result[j].Host })
	documents := make([]policyRegistryOriginDocument, 0, len(result))
	allRedirects := make([]string, 0)
	seen := map[string]bool{}
	for index := range result {
		origin := &result[index]
		if err := requireCanonicalText(origin.Host, fmt.Sprintf("registry.origins[%d].host", index), 253); err != nil {
			return nil, nil, nil, err
		}
		if seen[origin.Host] {
			return nil, nil, nil, fmt.Errorf("registry.origins repeats host %q", origin.Host)
		}
		seen[origin.Host] = true
		if origin.AuthorizationEnv != "" && !environmentNamePattern.MatchString(origin.AuthorizationEnv) {
			return nil, nil, nil, fmt.Errorf("registry origin %q has an invalid authorizationEnv", origin.Host)
		}
		redirects, err := sortedUniqueCanonical(origin.RedirectHosts, fmt.Sprintf("registry.origins[%d].redirectHosts", index))
		if err != nil {
			return nil, nil, nil, err
		}
		origin.RedirectHosts = redirects
		allRedirects = append(allRedirects, redirects...)
		documents = append(documents, policyRegistryOriginDocument{
			Host: origin.Host, RequiresAuthorization: origin.AuthorizationEnv != "", RedirectHosts: redirects,
		})
	}
	sort.Strings(allRedirects)
	uniqueRedirects := allRedirects[:0]
	for _, host := range allRedirects {
		if len(uniqueRedirects) == 0 || uniqueRedirects[len(uniqueRedirects)-1] != host {
			uniqueRedirects = append(uniqueRedirects, host)
		}
	}
	allRedirects = uniqueRedirects
	return result, documents, allRedirects, nil
}

func normalizeRepositories(input []RegistryRepositoryConfig, origins []RegistryOriginConfig) ([]RegistryRepositoryConfig, error) {
	if len(input) == 0 {
		return nil, errors.New("registry.repositories must not be empty")
	}
	originSet := make(map[string]bool, len(origins))
	for _, origin := range origins {
		originSet[origin.Host] = true
	}
	result := append([]RegistryRepositoryConfig(nil), input...)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Host+"/"+result[i].Repository < result[j].Host+"/"+result[j].Repository
	})
	seen := map[string]bool{}
	for index, repository := range result {
		if !originSet[repository.Host] {
			return nil, fmt.Errorf("registry.repositories[%d] uses an unconfigured origin", index)
		}
		if err := requireCanonicalText(repository.Repository, fmt.Sprintf("registry.repositories[%d].repository", index), 1024); err != nil {
			return nil, err
		}
		identity := repository.Host + "/" + repository.Repository
		if seen[identity] {
			return nil, fmt.Errorf("registry.repositories repeats %q", identity)
		}
		seen[identity] = true
	}
	return result, nil
}

func compileKeys(input []TrustedKeyConfig, field string) ([]compiledKey, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%s must not be empty", field)
	}
	configs := append([]TrustedKeyConfig(nil), input...)
	sort.Slice(configs, func(i, j int) bool { return configs[i].KeyID < configs[j].KeyID })
	result := make([]compiledKey, 0, len(configs))
	seen := map[string]bool{}
	for index, config := range configs {
		prefix := fmt.Sprintf("%s[%d]", field, index)
		if err := requireCanonicalText(config.KeyID, prefix+".keyId", 256); err != nil {
			return nil, err
		}
		if seen[config.KeyID] {
			return nil, fmt.Errorf("%s repeats keyId %q", field, config.KeyID)
		}
		seen[config.KeyID] = true
		if err := requireCanonicalText(config.Identity, prefix+".identity", 2048); err != nil {
			return nil, err
		}
		algorithm := templateauthority.SignatureAlgorithm(config.Algorithm)
		if algorithm != templateauthority.AlgorithmEd25519 && algorithm != templateauthority.AlgorithmECDSASHA256 {
			return nil, fmt.Errorf("%s.algorithm is unsupported", prefix)
		}
		publicKey, der, err := readPublicKey(config.PublicKeyFile, algorithm)
		if err != nil {
			return nil, fmt.Errorf("%s.publicKeyFile: %w", prefix, err)
		}
		digest := sha256.Sum256(der)
		result = append(result, compiledKey{
			KeyID: config.KeyID, Algorithm: algorithm, Identity: config.Identity,
			PublicKey: publicKey, PublicKeyDigest: "sha256:" + hex.EncodeToString(digest[:]),
		})
	}
	return result, nil
}

func compileTransparencyLogs(input []TransparencyLogConfig, maxEntryAge, maxFutureSkew time.Duration) (templateauthority.TransparencyTrustPolicy, []trustRootLogKeyDocument, error) {
	if len(input) == 0 {
		return templateauthority.TransparencyTrustPolicy{}, nil, errors.New("transparency.logs must not be empty")
	}
	logs := append([]TransparencyLogConfig(nil), input...)
	sort.Slice(logs, func(i, j int) bool { return logs[i].ID < logs[j].ID })
	policy := templateauthority.TransparencyTrustPolicy{
		Logs:        make(map[string]templateauthority.TrustedTransparencyLog, len(logs)),
		MaxEntryAge: maxEntryAge, MaxFutureSkew: maxFutureSkew,
	}
	documents := make([]trustRootLogKeyDocument, 0, len(logs))
	for index, log := range logs {
		if err := requireCanonicalText(log.ID, fmt.Sprintf("transparency.logs[%d].id", index), 240); err != nil {
			return templateauthority.TransparencyTrustPolicy{}, nil, err
		}
		if _, duplicate := policy.Logs[log.ID]; duplicate {
			return templateauthority.TransparencyTrustPolicy{}, nil, fmt.Errorf("transparency.logs repeats id %q", log.ID)
		}
		keys, err := compileKeys(log.Keys, fmt.Sprintf("transparency.logs[%d].keys", index))
		if err != nil {
			return templateauthority.TransparencyTrustPolicy{}, nil, err
		}
		trusted := templateauthority.TrustedTransparencyLog{Keys: make(map[string]templateauthority.TrustedSigner, len(keys))}
		for _, key := range keys {
			trusted.Keys[key.KeyID] = templateauthority.TrustedSigner{Algorithm: key.Algorithm, PublicKey: key.PublicKey, Identity: key.Identity}
		}
		policy.Logs[log.ID] = trusted
		documents = append(documents, trustRootLogKeyDocument{LogID: log.ID, Keys: trustKeyDocuments(keys)})
	}
	return policy, documents, nil
}

func readPublicKey(path string, algorithm templateauthority.SignatureAlgorithm) (any, []byte, error) {
	encoded, err := readRegularFile(path, maxPublicKeyBytes)
	if err != nil {
		return nil, nil, err
	}
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, errors.New("must contain exactly one PEM PKIX PUBLIC KEY")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse PKIX public key: %w", err)
	}
	switch algorithm {
	case templateauthority.AlgorithmEd25519:
		key, ok := parsed.(ed25519.PublicKey)
		if !ok || len(key) != ed25519.PublicKeySize {
			return nil, nil, errors.New("public key is not Ed25519")
		}
		return ed25519.PublicKey(bytes.Clone(key)), bytes.Clone(block.Bytes), nil
	case templateauthority.AlgorithmECDSASHA256:
		key, ok := parsed.(*ecdsa.PublicKey)
		if !ok || key == nil || key.Curve == nil || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
			return nil, nil, errors.New("public key is not a valid ECDSA key")
		}
		return key, bytes.Clone(block.Bytes), nil
	default:
		return nil, nil, errors.New("unsupported public key algorithm")
	}
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\r\n\x00") {
		return nil, errors.New("path must be absolute and normalized")
	}
	lstat, err := os.Lstat(path)
	if err != nil || !lstat.Mode().IsRegular() {
		return nil, errors.New("path must identify a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || !os.SameFile(lstat, stat) {
		return nil, errors.New("file identity changed while opening")
	}
	if stat.Size() <= 0 || stat.Size() > limit {
		return nil, fmt.Errorf("file size must be between 1 and %d bytes", limit)
	}
	encoded, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return encoded, nil
}

func trustKeyDocuments(keys []compiledKey) []trustRootKeyDocument {
	result := make([]trustRootKeyDocument, 0, len(keys))
	for _, key := range keys {
		result = append(result, trustRootKeyDocument{
			KeyID: key.KeyID, Algorithm: string(key.Algorithm), Identity: key.Identity,
			PublicKeyDigest: key.PublicKeyDigest,
		})
	}
	return result
}

func normalizedKeyConfigs(input []TrustedKeyConfig) []TrustedKeyConfig {
	result := append([]TrustedKeyConfig(nil), input...)
	sort.Slice(result, func(i, j int) bool { return result[i].KeyID < result[j].KeyID })
	return result
}

func normalizedTransparencyLogConfigs(input []TransparencyLogConfig) []TransparencyLogConfig {
	result := append([]TransparencyLogConfig(nil), input...)
	for index := range result {
		result[index].Keys = normalizedKeyConfigs(result[index].Keys)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func parseBoundedDuration(value, field string, minimum, maximum time.Duration) (time.Duration, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return 0, fmt.Errorf("%s must be a canonical duration", field)
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < minimum || duration > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", field, minimum, maximum)
	}
	return duration, nil
}

func sortedUniqueCanonical(input []string, field string) ([]string, error) {
	result := append([]string(nil), input...)
	for index, value := range result {
		if err := requireCanonicalText(value, fmt.Sprintf("%s[%d]", field, index), 2048); err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, fmt.Errorf("%s contains duplicate %q", field, result[index])
		}
	}
	return result, nil
}

func requireCanonicalText(value, field string, maximum int) error {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s must be canonical non-empty text no longer than %d bytes", field, maximum)
	}
	return nil
}

func containsExact(values []string, expected string) bool {
	index := sort.SearchStrings(values, expected)
	return index < len(values) && values[index] == expected
}

func canonicalDigest(value any) (string, error) {
	encoded, err := domain.CanonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize authority policy: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func decodeStrictJSON(input []byte, target any) error {
	if err := rejectDuplicateJSONNames(input); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func rejectDuplicateJSONNames(input []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON object key %q", name)
			}
			seen[name] = true
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
