package qualificationreceipt

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const PromotionAuthoritySchemaV2 = "worksflow-qualification-promotion-authority/v2"

const (
	maximumPromotionAuthorityTTL = 15 * time.Minute
	promotionAuthorityClockSkew  = 30 * time.Second
)

// PromotionAuthority is injected by the promotion service, not by the
// qualification runner. It is the independently controlled root for every
// expected binding accepted by the command-line verifier.
type PromotionAuthority struct {
	Digest                        string                        `json:"-"`
	SchemaVersion                 string                        `json:"schemaVersion"`
	PromotionTarget               PromotionTarget               `json:"promotionTarget"`
	AuthorityNonce                string                        `json:"authorityNonce"`
	AuthorityIssuedAt             string                        `json:"authorityIssuedAt"`
	AuthorityExpiresAt            string                        `json:"authorityExpiresAt"`
	RunID                         string                        `json:"runId"`
	PlanDigest                    string                        `json:"planDigest"`
	PrePromotionManifestDigest    string                        `json:"prePromotionManifestDigest"`
	Source                        SourceBinding                 `json:"source"`
	TemplateRelease               TemplateReleaseBinding        `json:"templateRelease"`
	GoldenRuntime                 GoldenRuntimeBinding          `json:"goldenRuntime"`
	BuildContractHash             string                        `json:"buildContractHash"`
	WriterDrainEvidenceArtifactID string                        `json:"writerDrainEvidenceArtifactId"`
	CredentialSet                 CredentialSetAuthorityBinding `json:"credentialSet"`
	TrustPolicyPath               string                        `json:"trustPolicyPath"`
	TrustPolicyDigest             string                        `json:"trustPolicyDigest"`
	ArtifactRoot                  string                        `json:"artifactRoot"`
	EvidenceSnapshotRoot          string                        `json:"evidenceSnapshotRoot"`
	ReceiptPath                   string                        `json:"receiptPath"`
	ArtifactIndexPath             string                        `json:"artifactIndexPath"`
	ArtifactIndexDigest           string                        `json:"artifactIndexDigest"`
	ReceiptBundleDigest           string                        `json:"receiptBundleDigest"`
	TrustedReceiptIssuedAt        string                        `json:"trustedReceiptIssuedAt"`
	ArtifactSnapshotID            string                        `json:"artifactSnapshotId"`
	ArtifactSnapshotMode          string                        `json:"artifactSnapshotMode"`
	RepositoryRoot                string                        `json:"repositoryRoot"`
	QualificationManifestPath     string                        `json:"qualificationManifestPath"`
	RepositorySnapshotID          string                        `json:"repositorySnapshotId"`
	RepositorySnapshotMode        string                        `json:"repositorySnapshotMode"`
	SourcePolicyStatus            string                        `json:"sourcePolicyStatus"`
	SourcePolicyToolDigest        string                        `json:"sourcePolicyToolDigest"`
	SourcePolicyVerifiedAt        string                        `json:"sourcePolicyVerifiedAt"`
	SourcePolicyAttestationDigest string                        `json:"sourcePolicyAttestationDigest"`
	VerifierExecutableDigest      string                        `json:"verifierExecutableDigest"`
	GitExecutableDigest           string                        `json:"gitExecutableDigest"`
}

func LoadPromotionAuthority(filePath string) (PromotionAuthority, error) {
	encoded, err := readRootOwnedAuthorityFile(filePath, maxIndexBytes)
	if err != nil {
		return PromotionAuthority{}, fmt.Errorf("read promotion authority: %w", err)
	}
	if err := requireExactShape(encoded, promotionAuthorityShape()); err != nil {
		return PromotionAuthority{}, fmt.Errorf("validate promotion authority shape: %w", err)
	}
	var authority PromotionAuthority
	if err := decodeStrictJSON(encoded, &authority); err != nil {
		return PromotionAuthority{}, fmt.Errorf("decode promotion authority: %w", err)
	}
	authority.Digest = sha256Digest(encoded)
	if authority.SchemaVersion != PromotionAuthoritySchemaV2 || validatePromotionTarget(authority.PromotionTarget) != nil ||
		!validUUID(authority.AuthorityNonce) || !validUUID(authority.RunID) ||
		!validDigest(authority.PlanDigest) || !validDigest(authority.PrePromotionManifestDigest) ||
		validateSource(authority.Source) != nil || validateTemplateRelease(authority.TemplateRelease) != nil ||
		validateGoldenRuntimeBinding(authority.GoldenRuntime) != nil ||
		!validDigest(authority.BuildContractHash) || !validStableID(authority.WriterDrainEvidenceArtifactID) ||
		validateCredentialSetAuthorityBinding(authority.CredentialSet) != nil ||
		!validDigest(authority.TrustPolicyDigest) || !filepath.IsAbs(authority.TrustPolicyPath) ||
		filepath.Clean(authority.TrustPolicyPath) != authority.TrustPolicyPath ||
		!filepath.IsAbs(authority.ArtifactRoot) || filepath.Clean(authority.ArtifactRoot) != authority.ArtifactRoot ||
		!filepath.IsAbs(authority.EvidenceSnapshotRoot) || filepath.Clean(authority.EvidenceSnapshotRoot) != authority.EvidenceSnapshotRoot ||
		!filepath.IsAbs(authority.ReceiptPath) || filepath.Clean(authority.ReceiptPath) != authority.ReceiptPath ||
		!filepath.IsAbs(authority.ArtifactIndexPath) || filepath.Clean(authority.ArtifactIndexPath) != authority.ArtifactIndexPath ||
		!validDigest(authority.ArtifactIndexDigest) || !validDigest(authority.ReceiptBundleDigest) ||
		!validStableID(authority.ArtifactSnapshotID) || authority.ArtifactSnapshotMode != ImmutableSnapshotMode ||
		!filepath.IsAbs(authority.RepositoryRoot) || filepath.Clean(authority.RepositoryRoot) != authority.RepositoryRoot ||
		!validArtifactPath(authority.QualificationManifestPath) || !validStableID(authority.RepositorySnapshotID) ||
		authority.RepositorySnapshotMode != ImmutableSnapshotMode || authority.SourcePolicyStatus != "passed" ||
		!validDigest(authority.SourcePolicyToolDigest) || !validDigest(authority.SourcePolicyAttestationDigest) ||
		!validDigest(authority.VerifierExecutableDigest) || !validDigest(authority.GitExecutableDigest) {
		return PromotionAuthority{}, errors.New("promotion authority contains invalid expected bindings")
	}
	for _, candidate := range []string{authority.ArtifactRoot, authority.ReceiptPath, authority.ArtifactIndexPath} {
		if !pathWithinRoot(authority.EvidenceSnapshotRoot, candidate) {
			return PromotionAuthority{}, errors.New("promotion evidence paths must be contained by the immutable evidence snapshot root")
		}
	}
	if authority.ReceiptPath == authority.ArtifactIndexPath || pathWithinRoot(authority.ArtifactRoot, authority.ReceiptPath) ||
		pathWithinRoot(authority.ArtifactRoot, authority.ArtifactIndexPath) {
		return PromotionAuthority{}, errors.New("receipt and artifact index must be distinct files outside the artifact directory")
	}
	issuedAt, err := parseCanonicalTime(authority.AuthorityIssuedAt, "promotionAuthority.authorityIssuedAt")
	if err != nil {
		return PromotionAuthority{}, err
	}
	expiresAt, err := parseCanonicalTime(authority.AuthorityExpiresAt, "promotionAuthority.authorityExpiresAt")
	if err != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maximumPromotionAuthorityTTL {
		return PromotionAuthority{}, errors.New("promotion authority validity interval must be positive and no longer than 15 minutes")
	}
	receiptAt, err := parseCanonicalTime(authority.TrustedReceiptIssuedAt, "promotionAuthority.trustedReceiptIssuedAt")
	if err != nil {
		return PromotionAuthority{}, err
	}
	if receiptAt.Before(issuedAt) || !receiptAt.Before(expiresAt) {
		return PromotionAuthority{}, errors.New("trusted receipt issuance must fall within the promotion authority interval")
	}
	if _, err := parseCanonicalTime(authority.SourcePolicyVerifiedAt, "promotionAuthority.sourcePolicyVerifiedAt"); err != nil {
		return PromotionAuthority{}, err
	}
	return authority, nil
}

func validateGoldenRuntimeBinding(binding GoldenRuntimeBinding) error {
	if !validStableID(binding.AuthorityDocumentArtifactID) || !validDigest(binding.AuthorityDocumentDigest) ||
		binding.FaultOperationSetDigest != GoldenFaultOperationSetDigestV1 ||
		!validStableID(binding.FixtureDocumentArtifactID) || !validDigest(binding.FixtureDocumentDigest) ||
		!validUUID(binding.FixtureID) || binding.AuthorityDocumentArtifactID == binding.FixtureDocumentArtifactID ||
		binding.AuthorityDocumentDigest == binding.FixtureDocumentDigest {
		return errors.New("Golden runtime binding must contain distinct digest-pinned authority and fixture documents, the closed v1 fault-operation set, and a canonical fixture id")
	}
	return nil
}

func validateCredentialSetAuthorityBinding(binding CredentialSetAuthorityBinding) error {
	if !validCanonicalString(binding.Issuer, 256) || !validCanonicalString(binding.Audience, 512) ||
		!validDigest(binding.SetHandleHash) || !validDigest(binding.MemberBindingsDigest) ||
		binding.MemberCount < 1 || binding.MemberCount > CredentialSetMaximumMembers {
		return errors.New("credential set authority binding is incomplete or non-canonical")
	}
	return nil
}

func validatePromotionTarget(target PromotionTarget) error {
	if !validUUID(target.ProjectID) || !validUUID(target.WorkflowRunID) || !validStableID(target.NodeKey) ||
		!validUUID(target.TargetRevision.ID) || !validDigest(target.TargetRevision.ContentHash) ||
		!validCanonicalString(target.Subject, 256) || target.StageGate != ExternalQualificationGate {
		return errors.New("promotion target must bind a canonical project, workflow run, node key, immutable revision, subject, and external qualification gate")
	}
	return nil
}

// ValidatePromotionAuthorityAt enforces the short-lived root authority against
// trusted service time. Replay prevention is completed by the downstream
// append-only ledger atomically consuming (target, nonce); a stateless verifier
// cannot truthfully provide that transaction guarantee.
func ValidatePromotionAuthorityAt(authority PromotionAuthority, now time.Time) error {
	if now.IsZero() {
		return errors.New("trusted promotion verification time is required")
	}
	issuedAt, err := parseCanonicalTime(authority.AuthorityIssuedAt, "promotionAuthority.authorityIssuedAt")
	if err != nil {
		return err
	}
	expiresAt, err := parseCanonicalTime(authority.AuthorityExpiresAt, "promotionAuthority.authorityExpiresAt")
	if err != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maximumPromotionAuthorityTTL {
		return errors.New("promotion authority validity interval must be positive and no longer than 15 minutes")
	}
	now = now.UTC()
	if now.Before(issuedAt.Add(-promotionAuthorityClockSkew)) || !now.Before(expiresAt) {
		return errors.New("promotion authority is not currently valid at trusted service time")
	}
	return nil
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func VerifyCurrentExecutable(authority PromotionAuthority) error {
	digest, err := runningExecutableDigest()
	if err != nil {
		return err
	}
	if digest != authority.VerifierExecutableDigest {
		return errors.New("running qualification verifier executable does not match the server-owned authority digest")
	}
	return nil
}

func runningExecutableDigest() (string, error) {
	file, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", fmt.Errorf("open running verifier executable: %w", err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maxArtifactBytes {
		return "", errors.New("running verifier executable is not a bounded regular file")
	}
	hasher := sha256.New()
	read, err := io.Copy(hasher, io.LimitReader(file, maxArtifactBytes+1))
	if err != nil || read != before.Size() {
		return "", errors.New("running verifier executable could not be hashed completely")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", errors.New("running verifier executable changed while hashing")
	}
	return fmt.Sprintf("sha256:%x", hasher.Sum(nil)), nil
}

func VerifySourcePolicyAuthority(authority PromotionAuthority) error {
	toolPath := filepath.Join(authority.RepositoryRoot, "frontend", "scripts", "qualification-source-policy.mjs")
	encoded, err := readBoundedRegularFile(toolPath, maxIndexBytes, false)
	if err != nil || sha256Digest(encoded) != authority.SourcePolicyToolDigest {
		return errors.New("source-policy tool does not match the independently attested digest")
	}
	verifiedAt, _ := parseCanonicalTime(authority.SourcePolicyVerifiedAt, "promotionAuthority.sourcePolicyVerifiedAt")
	receiptAt, _ := parseCanonicalTime(authority.TrustedReceiptIssuedAt, "promotionAuthority.trustedReceiptIssuedAt")
	if !receiptAt.After(verifiedAt) {
		return errors.New("source-policy attestation must precede trusted receipt issuance")
	}
	statement := map[string]any{
		"schemaVersion": "worksflow-qualification-source-policy-attestation/v1", "status": authority.SourcePolicyStatus,
		"planDigest": authority.PlanDigest, "source": authority.Source, "toolDigest": authority.SourcePolicyToolDigest,
		"verifiedAt": authority.SourcePolicyVerifiedAt,
	}
	canonical, err := canonicalJSONBytes(statement)
	if err != nil || sha256Digest(canonical) != authority.SourcePolicyAttestationDigest {
		return errors.New("root-owned source-policy attestation digest is invalid")
	}
	return nil
}

func LoadAuthorityTrustPolicy(authority PromotionAuthority) (TrustPolicy, error) {
	if _, err := readRootOwnedAuthorityFile(authority.TrustPolicyPath, maxIndexBytes); err != nil {
		return TrustPolicy{}, fmt.Errorf("trust policy is not a root-owned authority file: %w", err)
	}
	policy, err := LoadTrustPolicy(authority.TrustPolicyPath)
	if err != nil {
		return TrustPolicy{}, err
	}
	if policy.Digest != authority.TrustPolicyDigest {
		return TrustPolicy{}, errors.New("trust policy bytes do not match the root-owned promotion authority digest")
	}
	return policy, nil
}

func readRootOwnedAuthorityFile(filePath string, maximum int) ([]byte, error) {
	if !filepath.IsAbs(filePath) || filepath.Clean(filePath) != filePath {
		return nil, errors.New("authority path must be absolute and normalized")
	}
	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil || resolved != filePath {
		return nil, errors.New("authority path must not contain symlink components")
	}
	if err := validateRootOwnedAuthorityAncestors(filepath.Dir(filePath)); err != nil {
		return nil, err
	}
	descriptor, err := unix.Open(filePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), filePath)
	if file == nil {
		unix.Close(descriptor)
		return nil, errors.New("open authority file descriptor")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !validRootOwnedAuthorityInfo(before, maximum) {
		return nil, errors.New("authority file must be bounded, root-owned, single-linked, regular, and not writable by group or other")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maximum {
		return nil, errors.New("authority file exceeds its size limit or could not be read")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !validRootOwnedAuthorityInfo(after, maximum) ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, errors.New("authority file changed while reading")
	}
	pathInfo, err := os.Lstat(filePath)
	if err != nil || !os.SameFile(after, pathInfo) || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("authority path changed while reading")
	}
	return encoded, nil
}

func validRootOwnedAuthorityInfo(info os.FileInfo, maximum int) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 ||
		info.Size() <= 0 || info.Size() > int64(maximum) || hardLinkCount(info) != 1 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func validateRootOwnedAuthorityAncestors(directory string) error {
	for {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("authority directory hierarchy must be real and not writable by group or other")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return errors.New("authority directory hierarchy must be owned by uid 0")
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil
		}
		directory = parent
	}
}
