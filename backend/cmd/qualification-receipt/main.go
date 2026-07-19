package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

type commandOptions struct {
	repositoryRoot         string
	manifestPath           string
	receiptPath            string
	artifactIndexPath      string
	artifactRoot           string
	promotionAuthorityPath string
}

type promotionVerifier interface {
	Verify(string, string, string, qualificationreceipt.ExpectedPromotion) (qualificationreceipt.VerifiedPromotion, error)
}

type commandDependencies struct {
	now                func() time.Time
	loadAuthority      func(string) (qualificationreceipt.PromotionAuthority, error)
	verifyRepository   func(string, qualificationreceipt.SourceBinding, string) error
	verifySourcePolicy func(qualificationreceipt.PromotionAuthority) error
	verifyExecutable   func(qualificationreceipt.PromotionAuthority) error
	computePlan        func(string, string) (qualificationreceipt.Plan, error)
	loadTrustPolicy    func(qualificationreceipt.PromotionAuthority) (qualificationreceipt.TrustPolicy, error)
	newVerifier        func(qualificationreceipt.TrustPolicy) (promotionVerifier, error)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "external qualification promotion evidence denied:", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer, now func() time.Time) error {
	if output == nil || now == nil {
		return errors.New("command dependencies are required")
	}
	return runWithDependencies(arguments, output, commandDependencies{
		now: now, loadAuthority: qualificationreceipt.LoadPromotionAuthority,
		verifyRepository:   qualificationreceipt.VerifyRepositorySource,
		verifySourcePolicy: qualificationreceipt.VerifySourcePolicyAuthority,
		verifyExecutable:   qualificationreceipt.VerifyCurrentExecutable,
		computePlan:        qualificationreceipt.ComputePlanDigest,
		loadTrustPolicy:    qualificationreceipt.LoadAuthorityTrustPolicy,
		newVerifier: func(policy qualificationreceipt.TrustPolicy) (promotionVerifier, error) {
			return qualificationreceipt.NewVerifier(policy)
		},
	})
}

func runWithDependencies(arguments []string, output io.Writer, dependencies commandDependencies) error {
	if output == nil || dependencies.now == nil || dependencies.loadAuthority == nil || dependencies.verifyRepository == nil ||
		dependencies.verifySourcePolicy == nil || dependencies.verifyExecutable == nil ||
		dependencies.computePlan == nil || dependencies.loadTrustPolicy == nil || dependencies.newVerifier == nil {
		return errors.New("command dependencies are required")
	}
	options, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	authority, err := dependencies.loadAuthority(options.promotionAuthorityPath)
	if err != nil {
		return err
	}
	if options.repositoryRoot != authority.RepositoryRoot || options.manifestPath != authority.QualificationManifestPath {
		return errors.New("repository root or qualification manifest does not match the server-owned snapshot authority")
	}
	if options.receiptPath != authority.ReceiptPath || options.artifactIndexPath != authority.ArtifactIndexPath || options.artifactRoot != authority.ArtifactRoot {
		return errors.New("receipt, artifact index, or artifact root does not match the server-owned evidence snapshot authority")
	}
	verificationTime := dependencies.now().UTC()
	if err := qualificationreceipt.ValidatePromotionAuthorityAt(authority, verificationTime); err != nil {
		return err
	}
	if err := dependencies.verifyExecutable(authority); err != nil {
		return err
	}
	if err := dependencies.verifyRepository(authority.RepositoryRoot, authority.Source, authority.GitExecutableDigest); err != nil {
		return err
	}
	if err := dependencies.verifySourcePolicy(authority); err != nil {
		return err
	}
	plan, err := dependencies.computePlan(options.repositoryRoot, options.manifestPath)
	if err != nil {
		return err
	}
	if len(plan.IncompleteExternalSuites) != 0 {
		return fmt.Errorf("external qualification coverage is incomplete for %v", plan.IncompleteExternalSuites)
	}
	if plan.Digest != authority.PlanDigest {
		return errors.New("repository qualification plan does not match the server-owned promotion authority")
	}
	if plan.ManifestDigest != authority.PrePromotionManifestDigest {
		return errors.New("qualification manifest does not match the server-owned pre-promotion manifest digest")
	}
	if plan.Subject != authority.PromotionTarget.Subject {
		return errors.New("qualification manifest subject does not match the server-owned promotion target")
	}
	policy, err := dependencies.loadTrustPolicy(authority)
	if err != nil {
		return err
	}
	verifier, err := dependencies.newVerifier(policy)
	if err != nil {
		return err
	}
	expected := qualificationreceipt.ExpectedPromotion{
		PromotionTarget:               authority.PromotionTarget,
		AuthorityNonce:                authority.AuthorityNonce,
		AuthorityIssuedAt:             authority.AuthorityIssuedAt,
		AuthorityExpiresAt:            authority.AuthorityExpiresAt,
		PromotionAuthorityDigest:      authority.Digest,
		RunID:                         authority.RunID,
		PlanDigest:                    authority.PlanDigest,
		PrePromotionManifestDigest:    authority.PrePromotionManifestDigest,
		Source:                        authority.Source,
		TemplateRelease:               authority.TemplateRelease,
		GoldenRuntime:                 authority.GoldenRuntime,
		BuildContractHash:             authority.BuildContractHash,
		WriterDrainEvidenceArtifactID: authority.WriterDrainEvidenceArtifactID,
		CredentialSet:                 authority.CredentialSet,
		SourcePolicyAttestationDigest: authority.SourcePolicyAttestationDigest,
		ArtifactRoot:                  authority.ArtifactRoot,
		EvidenceSnapshotRoot:          authority.EvidenceSnapshotRoot,
		ArtifactIndexDigest:           authority.ArtifactIndexDigest,
		ReceiptBundleDigest:           authority.ReceiptBundleDigest,
		TrustedReceiptIssuedAt:        authority.TrustedReceiptIssuedAt,
		ArtifactSnapshotID:            authority.ArtifactSnapshotID,
		ArtifactSnapshotMode:          authority.ArtifactSnapshotMode,
		Suites:                        plan.ExternalSuites,
		TestInventoryDigest:           plan.TestInventoryDigest,
		TestCases:                     plan.TestCases,
		VerifiedAt:                    verificationTime,
	}
	verified, err := verifier.Verify(options.receiptPath, options.artifactIndexPath, options.artifactRoot, expected)
	if err != nil {
		return err
	}
	afterPlan, err := dependencies.computePlan(options.repositoryRoot, options.manifestPath)
	if err != nil || afterPlan.Digest != plan.Digest || afterPlan.ManifestDigest != plan.ManifestDigest ||
		afterPlan.TestInventoryDigest != plan.TestInventoryDigest ||
		!reflect.DeepEqual(afterPlan.ExternalSuites, plan.ExternalSuites) || !reflect.DeepEqual(afterPlan.TestCases, plan.TestCases) {
		return errors.New("qualification plan or manifest changed while promotion evidence was verified")
	}
	afterAuthority, err := dependencies.loadAuthority(options.promotionAuthorityPath)
	if err != nil || afterAuthority != authority {
		return errors.New("server-owned promotion authority changed while promotion evidence was verified")
	}
	if _, err := dependencies.loadTrustPolicy(afterAuthority); err != nil {
		return errors.New("server-owned trust policy changed while promotion evidence was verified")
	}
	if err := qualificationreceipt.ValidatePromotionAuthorityAt(afterAuthority, dependencies.now().UTC()); err != nil {
		return errors.New("server-owned promotion authority expired while promotion evidence was verified")
	}
	if err := dependencies.verifyExecutable(afterAuthority); err != nil {
		return errors.New("qualification verifier executable changed while promotion evidence was verified")
	}
	if err := dependencies.verifyRepository(afterAuthority.RepositoryRoot, afterAuthority.Source, afterAuthority.GitExecutableDigest); err != nil {
		return errors.New("repository source snapshot changed while promotion evidence was verified")
	}
	if err := dependencies.verifySourcePolicy(afterAuthority); err != nil {
		return errors.New("source-policy authority changed while promotion evidence was verified")
	}
	encoded, err := json.Marshal(verified)
	if err != nil {
		return fmt.Errorf("encode verified promotion: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := output.Write(encoded); err != nil {
		return fmt.Errorf("write verified promotion: %w", err)
	}
	return nil
}

func parseOptions(arguments []string) (commandOptions, error) {
	var options commandOptions
	flags := flag.NewFlagSet("qualification-receipt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.repositoryRoot, "repository-root", "", "absolute repository root")
	flags.StringVar(&options.manifestPath, "qualification-manifest", "qualification/manifest.json", "repository-relative qualification manifest")
	flags.StringVar(&options.receiptPath, "receipt", "", "absolute DSSE qualification receipt path")
	flags.StringVar(&options.artifactIndexPath, "artifact-index", "", "absolute artifact-index v1 path")
	flags.StringVar(&options.artifactRoot, "artifact-root", "", "absolute sealed artifact directory")
	flags.StringVar(&options.promotionAuthorityPath, "promotion-authority", "", "absolute root-owned promotion authority path")
	if err := flags.Parse(arguments); err != nil {
		return commandOptions{}, err
	}
	if flags.NArg() != 0 {
		return commandOptions{}, errors.New("positional arguments are not accepted")
	}
	for label, candidate := range map[string]string{
		"repository-root": options.repositoryRoot, "receipt": options.receiptPath,
		"artifact-index": options.artifactIndexPath, "artifact-root": options.artifactRoot,
		"promotion-authority": options.promotionAuthorityPath,
	} {
		if candidate == "" || !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate {
			return commandOptions{}, fmt.Errorf("%s must be an absolute normalized path", label)
		}
	}
	if options.manifestPath == "" || path.IsAbs(options.manifestPath) || path.Clean(options.manifestPath) != options.manifestPath ||
		strings.Contains(options.manifestPath, "\\") || options.manifestPath == "." || strings.HasPrefix(options.manifestPath, "../") {
		return commandOptions{}, errors.New("qualification-manifest must be a normalized repository-relative path")
	}
	return options, nil
}
