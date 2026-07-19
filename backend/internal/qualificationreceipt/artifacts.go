package qualificationreceipt

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type verifiedArtifactSet struct {
	byID                     map[string]ArtifactDescriptor
	restrictedEncryptedCount int
	traceCount               int
	videoCount               int
}

func readArtifactIndex(indexPath string) ([]byte, ArtifactIndex, error) {
	encoded, err := readBoundedRegularFile(indexPath, maxIndexBytes, true)
	if err != nil {
		return nil, ArtifactIndex{}, fmt.Errorf("read artifact index: %w", err)
	}
	if err := requireExactShape(encoded, artifactIndexShape()); err != nil {
		return nil, ArtifactIndex{}, fmt.Errorf("validate artifact index shape: %w", err)
	}
	var index ArtifactIndex
	if err := decodeStrictJSON(encoded, &index); err != nil {
		return nil, ArtifactIndex{}, fmt.Errorf("decode artifact index: %w", err)
	}
	return encoded, index, nil
}

func verifyArtifactDirectory(root string, index ArtifactIndex, expected ExpectedPromotion) (verifiedArtifactSet, error) {
	if err := validateArtifactIndex(index, expected); err != nil {
		return verifiedArtifactSet{}, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return verifiedArtifactSet{}, fmt.Errorf("inspect artifact root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return verifiedArtifactSet{}, errors.New("artifact root must be a real directory, not a symlink")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil || filepath.Clean(absoluteRoot) != absoluteRoot {
		return verifiedArtifactSet{}, errors.New("artifact root must resolve to an absolute normalized path")
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil || resolvedRoot != absoluteRoot {
		return verifiedArtifactSet{}, errors.New("artifact root path must not contain symlink components")
	}

	byPath := make(map[string]ArtifactDescriptor, len(index.Artifacts))
	byID := make(map[string]ArtifactDescriptor, len(index.Artifacts))
	set := verifiedArtifactSet{byID: byID}
	for _, artifact := range index.Artifacts {
		byPath[artifact.Path] = artifact
		byID[artifact.ID] = artifact
		if artifact.Classification == ClassificationRestrictedEncrypted {
			set.restrictedEncryptedCount++
		}
		if artifact.Type == ArtifactTypeTrace {
			set.traceCount++
		}
		if artifact.Type == ArtifactTypeVideo {
			set.videoCount++
		}
	}

	seen := make(map[string]struct{}, len(index.Artifacts))
	err = filepath.WalkDir(absoluteRoot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == absoluteRoot {
			return nil
		}
		relative, err := filepath.Rel(absoluteRoot, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact tree contains symlink %q", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("artifact tree contains non-regular file %q", relative)
		}
		descriptor, listed := byPath[relative]
		if !listed {
			return fmt.Errorf("artifact tree contains unlisted file %q", relative)
		}
		actualSize, actualDigest, err := hashStableRegularFile(current)
		if err != nil {
			return fmt.Errorf("verify artifact %q: %w", relative, err)
		}
		if actualSize != descriptor.SizeBytes || actualDigest != descriptor.SHA256 {
			return fmt.Errorf("artifact %q hash or size does not match the sealed index", relative)
		}
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return verifiedArtifactSet{}, err
	}
	if len(seen) != len(index.Artifacts) {
		missing := make([]string, 0)
		for _, artifact := range index.Artifacts {
			if _, ok := seen[artifact.Path]; !ok {
				missing = append(missing, artifact.Path)
			}
		}
		return verifiedArtifactSet{}, fmt.Errorf("artifact index references missing files: %s", strings.Join(missing, ", "))
	}
	return set, nil
}

func validateArtifactIndex(index ArtifactIndex, expected ExpectedPromotion) error {
	if index.SchemaVersion != ArtifactIndexSchemaV1 {
		return errors.New("artifact index schemaVersion is not v1")
	}
	if index.RunID != expected.RunID || !validUUID(index.RunID) {
		return errors.New("artifact index runId does not match the expected canonical UUIDv4")
	}
	if index.PlanDigest != expected.PlanDigest || !validDigest(index.PlanDigest) {
		return errors.New("artifact index planDigest does not match the current qualification plan")
	}
	if err := validateSource(index.Source); err != nil || index.Source != expected.Source {
		return errors.New("artifact index source binding does not match the expected clean source")
	}
	if err := validateTemplateRelease(index.TemplateRelease); err != nil || index.TemplateRelease != expected.TemplateRelease {
		return errors.New("artifact index TemplateRelease binding does not match the approved release")
	}
	if len(index.Artifacts) == 0 || len(index.Artifacts) > maxArtifactCount {
		return fmt.Errorf("artifact index must contain 1..%d artifacts", maxArtifactCount)
	}
	paths := map[string]struct{}{}
	ids := map[string]ArtifactDescriptor{}
	var aggregateSize int64
	for artifactIndex, artifact := range index.Artifacts {
		if artifactIndex > 0 && index.Artifacts[artifactIndex-1].ID >= artifact.ID {
			return errors.New("artifact index entries must be uniquely sorted by id")
		}
		if !validStableID(artifact.ID) {
			return fmt.Errorf("artifact %d has an invalid id", artifactIndex)
		}
		if !validArtifactPath(artifact.Path) {
			return fmt.Errorf("artifact %q has a non-canonical path", artifact.ID)
		}
		if _, duplicate := paths[artifact.Path]; duplicate {
			return fmt.Errorf("artifact path %q is duplicated", artifact.Path)
		}
		paths[artifact.Path] = struct{}{}
		if !validArtifactType(artifact.Type) || !validCanonicalString(artifact.MediaType, 256) {
			return fmt.Errorf("artifact %q has an invalid type or mediaType", artifact.ID)
		}
		if !validDigest(artifact.SHA256) || artifact.SizeBytes <= 0 || artifact.SizeBytes > maxArtifactBytes || aggregateSize > maxArtifactBytes-artifact.SizeBytes {
			return fmt.Errorf("artifact %q has an invalid digest or size", artifact.ID)
		}
		aggregateSize += artifact.SizeBytes
		if !sortedUniqueStrings(artifact.SuiteIDs, validStableID) || !sortedUniqueStrings(artifact.RequirementIDs, requirementPattern.MatchString) {
			return fmt.Errorf("artifact %q suiteIds and requirementIds must be non-empty, valid, sorted, and unique", artifact.ID)
		}
		switch artifact.Classification {
		case ClassificationDistributable:
			if artifact.Encryption != nil {
				return fmt.Errorf("distributable artifact %q must not carry encryption metadata", artifact.ID)
			}
			switch artifact.Type {
			case ArtifactTypePlaywrightResults, ArtifactTypeCredentialSetIssuance, ArtifactTypeCredentialSetRevocation,
				ArtifactTypeGoldenAuthorityDocument, ArtifactTypeGoldenFixtureDocument,
				ArtifactTypeWriterDrain, ArtifactTypeEncryptionAttestation,
				ArtifactTypeGoldenFaultAuthority, ArtifactTypeGoldenFaultReceipt, ArtifactTypeGoldenFaultLedger:
			default:
				return fmt.Errorf("unstructured artifact %q must be restricted-encrypted", artifact.ID)
			}
		case ClassificationRestrictedEncrypted:
			if err := validateEncryption(index, artifact); err != nil {
				return err
			}
		default:
			return fmt.Errorf("artifact %q has unsupported classification", artifact.ID)
		}
		if (artifact.Type == ArtifactTypeTrace || artifact.Type == ArtifactTypeVideo) && artifact.Classification != ClassificationRestrictedEncrypted {
			return fmt.Errorf("trace/video artifact %q must be restricted and encrypted", artifact.ID)
		}
		if (artifact.Type == ArtifactTypeGoldenFaultAuthority || artifact.Type == ArtifactTypeGoldenFaultReceipt ||
			artifact.Type == ArtifactTypeGoldenFaultLedger) && artifact.Classification != ClassificationDistributable {
			return fmt.Errorf("Golden fault evidence artifact %q must be distributable", artifact.ID)
		}
		if artifact.Type == ArtifactTypeEncryptionAttestation && artifact.ID != EncryptionAttestationArtifactID {
			return fmt.Errorf("encryption attestation artifact must use id %q", EncryptionAttestationArtifactID)
		}
		if artifact.Type == ArtifactTypeGoldenFaultLedger && artifact.ID != GoldenFaultLedgerArtifactID {
			return fmt.Errorf("Golden fault ledger attestation artifact must use id %q", GoldenFaultLedgerArtifactID)
		}
		switch artifact.Type {
		case ArtifactTypeGoldenFaultAuthority, ArtifactTypeGoldenFaultLedger:
			if artifact.MediaType != DSSEEnvelopeMediaType {
				return fmt.Errorf("artifact %q must use the exact DSSE envelope media type", artifact.ID)
			}
		case ArtifactTypeGoldenFaultReceipt:
			if artifact.MediaType != CanonicalJSONMediaType {
				return fmt.Errorf("artifact %q must use the exact canonical JSON media type", artifact.ID)
			}
		}
		switch artifact.ID {
		case "credential-safe-trace":
			if artifact.Type != ArtifactTypeTrace {
				return errors.New("credential-safe-trace artifact must use the trace type")
			}
		case "browser-video":
			if artifact.Type != ArtifactTypeVideo {
				return errors.New("browser-video artifact must use the video type")
			}
		case "zero-mock-zero-skip-playwright-json":
			if artifact.Type != ArtifactTypePlaywrightResults {
				return errors.New("zero-mock-zero-skip-playwright-json must use the Playwright result type")
			}
		case EncryptionAttestationArtifactID:
			if artifact.Type != ArtifactTypeEncryptionAttestation || artifact.Classification != ClassificationDistributable {
				return errors.New("kms-encryption-attestation must be a distributable encryption attestation")
			}
		}
		ids[artifact.ID] = artifact
	}

	expectedSuites := make(map[string]ExpectedSuite, len(expected.Suites))
	for _, suite := range expected.Suites {
		expectedSuites[suite.ID] = suite
	}
	for _, artifact := range index.Artifacts {
		for _, suiteID := range artifact.SuiteIDs {
			if _, exists := expectedSuites[suiteID]; !exists {
				return fmt.Errorf("artifact %q references unexpected suite %q", artifact.ID, suiteID)
			}
		}
		allowedRequirements := map[string]struct{}{}
		for _, suiteID := range artifact.SuiteIDs {
			for _, requirementID := range expectedSuites[suiteID].RequirementIDs {
				allowedRequirements[requirementID] = struct{}{}
			}
		}
		for _, requirementID := range artifact.RequirementIDs {
			if _, allowed := allowedRequirements[requirementID]; !allowed {
				return fmt.Errorf("artifact %q references requirement %q outside its suites", artifact.ID, requirementID)
			}
		}
	}
	for _, suite := range expected.Suites {
		for _, artifactID := range suite.RequiredArtifacts {
			artifact, exists := ids[artifactID]
			if !exists || !containsString(artifact.SuiteIDs, suite.ID) {
				return fmt.Errorf("suite %q is missing required artifact %q", suite.ID, artifactID)
			}
		}
	}
	if _, exists := ids[EncryptionAttestationArtifactID]; !exists {
		return errors.New("artifact index is missing the trusted KMS encryption attestation")
	}
	if err := validateGoldenAndCredentialArtifacts(ids, expected); err != nil {
		return err
	}
	return nil
}

func validateGoldenAndCredentialArtifacts(ids map[string]ArtifactDescriptor, expected ExpectedPromotion) error {
	goldenAuthorityCount := 0
	goldenFixtureCount := 0
	faultAuthorityCount := 0
	faultReceiptCount := 0
	faultLedgerCount := 0
	for _, descriptor := range ids {
		switch descriptor.Type {
		case ArtifactTypeGoldenAuthorityDocument:
			goldenAuthorityCount++
		case ArtifactTypeGoldenFixtureDocument:
			goldenFixtureCount++
		case ArtifactTypeGoldenFaultAuthority:
			faultAuthorityCount++
		case ArtifactTypeGoldenFaultReceipt:
			faultReceiptCount++
		case ArtifactTypeGoldenFaultLedger:
			faultLedgerCount++
		}
	}
	if goldenAuthorityCount != 1 || goldenFixtureCount != 1 {
		return errors.New("artifact index must contain exactly one root-pinned Golden authority document and fixture document")
	}
	if faultAuthorityCount < 1 || faultAuthorityCount > 64 || faultReceiptCount != faultAuthorityCount || faultLedgerCount != 1 {
		return errors.New("artifact index must contain equal 1..64 Golden fault authorities/receipts and exactly one ledger attestation")
	}
	bindings := []struct {
		id           string
		digest       string
		artifactType string
		label        string
	}{
		{expected.GoldenRuntime.AuthorityDocumentArtifactID, expected.GoldenRuntime.AuthorityDocumentDigest, ArtifactTypeGoldenAuthorityDocument, "Golden authority document"},
		{expected.GoldenRuntime.FixtureDocumentArtifactID, expected.GoldenRuntime.FixtureDocumentDigest, ArtifactTypeGoldenFixtureDocument, "Golden fixture document"},
	}
	for _, binding := range bindings {
		descriptor, exists := ids[binding.id]
		if !exists || descriptor.Type != binding.artifactType || descriptor.Classification != ClassificationDistributable ||
			descriptor.MediaType != "application/json" || descriptor.SHA256 != binding.digest {
			return fmt.Errorf("%s artifact does not close the root-pinned id and document digest", binding.label)
		}
	}
	return nil
}

func validateEncryption(index ArtifactIndex, artifact ArtifactDescriptor) error {
	encryption := artifact.Encryption
	if encryption == nil || encryption.Scheme != EncryptionSchemeV1 ||
		!validCanonicalString(encryption.KeyResource, 2048) || !validCanonicalString(encryption.KeyVersion, 256) {
		return fmt.Errorf("restricted artifact %q has invalid encryption authority", artifact.ID)
	}
	if _, err := decodeCanonicalBase64(encryption.WrappedKey, 16, 16<<10); err != nil {
		return fmt.Errorf("restricted artifact %q has invalid wrapped key", artifact.ID)
	}
	if _, err := decodeCanonicalBase64(encryption.Nonce, 12, 12); err != nil {
		return fmt.Errorf("restricted artifact %q has invalid AES-GCM nonce", artifact.ID)
	}
	if _, err := decodeCanonicalBase64(encryption.Tag, 16, 16); err != nil {
		return fmt.Errorf("restricted artifact %q has invalid AES-GCM tag", artifact.ID)
	}
	expectedAAD := strings.Join([]string{
		"worksflow-qualification-encryption/v1", index.RunID, index.PlanDigest,
		artifact.ID, artifact.Path, index.TemplateRelease.ContentHash,
	}, "\n")
	if encryption.AdditionalData != expectedAAD || encryption.AdditionalDataHash != sha256Digest([]byte(expectedAAD)) {
		return fmt.Errorf("restricted artifact %q encryption AAD does not bind the run, plan, path, and TemplateRelease", artifact.ID)
	}
	return nil
}

func validArtifactPath(value string) bool {
	if value == "" || len(value) > 1024 || strings.Contains(value, "\\") || strings.ContainsAny(value, "\r\n\x00") || path.IsAbs(value) {
		return false
	}
	if !canonicalPathPattern.MatchString(value) {
		return false
	}
	if path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func validArtifactType(value string) bool {
	switch value {
	case ArtifactTypeEvidence, ArtifactTypePlaywrightResults, ArtifactTypeCredentialSetIssuance, ArtifactTypeCredentialSetRevocation,
		ArtifactTypeGoldenAuthorityDocument, ArtifactTypeGoldenFixtureDocument, ArtifactTypeTrace, ArtifactTypeVideo,
		ArtifactTypeWriterDrain, ArtifactTypeEncryptionAttestation, ArtifactTypeGoldenFaultAuthority,
		ArtifactTypeGoldenFaultReceipt, ArtifactTypeGoldenFaultLedger:
		return true
	default:
		return false
	}
}

func hashStableRegularFile(filePath string) (int64, string, error) {
	before, err := os.Lstat(filePath)
	if err != nil {
		return 0, "", err
	}
	if !before.Mode().IsRegular() || hardLinkCount(before) != 1 {
		return 0, "", errors.New("file must be regular with exactly one hard link")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || hardLinkCount(opened) != 1 {
		return 0, "", errors.New("file identity changed while opening")
	}
	hasher := sha256.New()
	read, err := io.Copy(hasher, bufio.NewReader(io.LimitReader(file, maxArtifactBytes+1)))
	if err != nil || read > maxArtifactBytes {
		return 0, "", errors.New("file exceeds the artifact size limit or could not be read")
	}
	after, err := os.Lstat(filePath)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || hardLinkCount(after) != 1 ||
		after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return 0, "", errors.New("file changed while hashing")
	}
	return read, fmt.Sprintf("sha256:%x", hasher.Sum(nil)), nil
}

func hardLinkCount(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Nlink)
}

func readBoundedRegularFile(filePath string, maximum int, requireSingleLink bool) ([]byte, error) {
	if !filepath.IsAbs(filePath) || filepath.Clean(filePath) != filePath {
		return nil, errors.New("path must be absolute and normalized")
	}
	resolvedPath, err := filepath.EvalSymlinks(filePath)
	if err != nil || resolvedPath != filePath {
		return nil, errors.New("path must not contain symlink components")
	}
	before, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > int64(maximum) || (requireSingleLink && hardLinkCount(before) != 1) {
		return nil, errors.New("path must be a bounded regular file")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || (requireSingleLink && hardLinkCount(opened) != 1) {
		return nil, errors.New("file identity changed while opening")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maximum {
		return nil, errors.New("file exceeds its size limit or could not be read")
	}
	after, err := os.Lstat(filePath)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) ||
		(requireSingleLink && hardLinkCount(after) != 1) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, errors.New("file changed while reading")
	}
	return encoded, nil
}

func readVerifiedArtifact(root string, descriptor ArtifactDescriptor, maximum int) ([]byte, error) {
	encoded, err := readBoundedRegularFile(filepath.Join(root, filepath.FromSlash(descriptor.Path)), maximum, true)
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) != descriptor.SizeBytes || sha256Digest(encoded) != descriptor.SHA256 {
		return nil, fmt.Errorf("artifact %q changed after directory closure verification", descriptor.ID)
	}
	return encoded, nil
}

func validateSource(source SourceBinding) error {
	if !commitPattern.MatchString(source.Commit) || source.TreeDigestSchema != SourceContentTreeCommitmentSchemaV1 ||
		!validDigest(source.TreeDigest) || source.Dirty {
		return errors.New("source must bind a clean canonical commit and SHA-256 actual-byte content-tree digest")
	}
	return nil
}

func validateTemplateRelease(release TemplateReleaseBinding) error {
	if !validUUID(release.ID) || !validDigest(release.ContentHash) || !validDigest(release.ApprovalReceiptDigest) {
		return errors.New("TemplateRelease must bind a canonical UUIDv4 and exact content/approval digests")
	}
	return nil
}

func containsString(values []string, expected string) bool {
	index := sort.SearchStrings(values, expected)
	return index < len(values) && values[index] == expected
}
