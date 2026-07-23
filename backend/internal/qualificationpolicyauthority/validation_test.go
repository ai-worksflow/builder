package qualificationpolicyauthority

import (
	"errors"
	"testing"

	"github.com/worksflow/builder/backend/internal/qualificationevidence"
)

func TestRevisionPolicyRejectsEveryWideningAndUnsafeCurrencyRule(t *testing.T) {
	valid := validResolvedPolicy().RevisionPolicy
	tests := map[string]func(*RevisionPolicy){
		"nil exact array": func(value *RevisionPolicy) {
			value.ExactApprovedSources = nil
		},
		"nil review array": func(value *RevisionPolicy) {
			value.ReviewByChangeSource = nil
		},
		"missing change source": func(value *RevisionPolicy) {
			value.ReviewByChangeSource = value.ReviewByChangeSource[:5]
		},
		"reordered change source": func(value *RevisionPolicy) {
			value.ReviewByChangeSource[0], value.ReviewByChangeSource[1] = value.ReviewByChangeSource[1], value.ReviewByChangeSource[0]
		},
		"human review disabled": func(value *RevisionPolicy) {
			value.ReviewByChangeSource[1].CanonicalReviewRequired = false
		},
		"workspace review enabled": func(value *RevisionPolicy) {
			value.WorkspaceTarget.CanonicalReviewRequired = true
		},
		"workspace exact currency": func(value *RevisionPolicy) {
			value.WorkspaceTarget.CurrencyPolicy = CurrencyExactApproved
		},
		"default exact currency": func(value *RevisionPolicy) {
			value.SourceCurrencyPolicy = CurrencyExactApproved
		},
		"workspace override": func(value *RevisionPolicy) {
			value.ExactApprovedSources[0].SourceKind = WorkspaceSourceKind
		},
		"duplicate exact tuple": func(value *RevisionPolicy) {
			value.ExactApprovedSources = append(value.ExactApprovedSources, value.ExactApprovedSources[0])
		},
		"invalid exact hash": func(value *RevisionPolicy) {
			value.ExactApprovedSources[0].ContentHash = "SHA256:ABC"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneRevisionPolicy(valid)
			mutate(&candidate)
			if err := ValidateRevisionPolicy(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateRevisionPolicy() error = %v", err)
			}
		})
	}
}

func TestPlanInputProfileRejectsUnsafeOrIncompleteAuthorityInputs(t *testing.T) {
	valid := validResolvedPolicy().PlanInputProfile
	tests := map[string]func(*PlanInputProfile){
		"nil artifacts": func(value *PlanInputProfile) {
			value.Artifacts = nil
		},
		"artifact policy weakened": func(value *PlanInputProfile) {
			value.ArtifactPolicy.RequireRestrictedEncryption = false
		},
		"artifact order": func(value *PlanInputProfile) {
			value.Artifacts[0], value.Artifacts[1] = value.Artifacts[1], value.Artifacts[0]
		},
		"trace classification": func(value *PlanInputProfile) {
			value.Artifacts[1].Classification = qualificationevidence.ClassificationDistributable
		},
		"missing video": func(value *PlanInputProfile) {
			value.Artifacts[0] = ArtifactExpectation{
				ID: "browser-result", Kind: qualificationevidence.ArtifactKindRunResult,
				Classification: qualificationevidence.ClassificationDistributable,
			}
		},
		"trust role alias": func(value *PlanInputProfile) {
			value.TrustBindings.VerifierAuthorityID = value.TrustBindings.ReceiptAuthorityID
		},
		"credential resolver mismatch": func(value *PlanInputProfile) {
			value.CredentialProfile.AuthorityID = "other-credential-authority"
		},
		"output alias": func(value *PlanInputProfile) {
			value.Outputs.ReceiptID = value.Outputs.SnapshotID
		},
		"source trust domain alias": func(value *PlanInputProfile) {
			value.SourcePolicyDigest = value.TrustPolicyDigest
		},
		"template approval alias": func(value *PlanInputProfile) {
			value.TemplateRelease.ApprovalReceiptDigest = value.TemplateRelease.ContentHash
		},
		"manifest plan alias": func(value *PlanInputProfile) {
			value.QualificationManifest.PlanDigest = value.QualificationManifest.ContentHash
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := clonePlanInputProfile(valid)
			mutate(&candidate)
			if err := ValidatePlanInputProfile(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidatePlanInputProfile() error = %v", err)
			}
		})
	}
}

func TestPromotionPolicyRejectsUnknownAliasedAndSecretBindings(t *testing.T) {
	valid := validResolvedPolicy().PromotionPolicy
	tests := map[string]func(*PromotionPolicy){
		"nil requirements": func(value *PromotionPolicy) {
			value.IndependentRequirements = nil
		},
		"unknown kind": func(value *PromotionPolicy) {
			value.IndependentRequirements[0].Kind = "browser-supplied-gate"
		},
		"reordered": func(value *PromotionPolicy) {
			value.IndependentRequirements[0], value.IndependentRequirements[1] =
				value.IndependentRequirements[1], value.IndependentRequirements[0]
		},
		"authority alias": func(value *PromotionPolicy) {
			value.IndependentRequirements[1].AuthorityID = value.IndependentRequirements[0].AuthorityID
		},
		"hash alias": func(value *PromotionPolicy) {
			value.IndependentRequirements[1].AuthorityHash = value.IndependentRequirements[0].AuthorityHash
		},
		"provider token": func(value *PromotionPolicy) {
			value.IndependentRequirements[0].AuthorityID = "sk-abcdefghijklmnop"
		},
		"database URL": func(value *PromotionPolicy) {
			value.IndependentRequirements[0].AuthorityID = "postgresql://production-db"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := clonePromotionPolicy(valid)
			mutate(&candidate)
			if err := ValidatePromotionPolicy(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidatePromotionPolicy() error = %v", err)
			}
		})
	}

	empty := clonePromotionPolicy(valid)
	empty.IndependentRequirements = []IndependentAuthorityBinding{}
	if err := ValidatePromotionPolicy(empty); err != nil {
		t.Fatalf("explicit reviewed empty independent requirement set was reinterpreted: %v", err)
	}
}

func TestAuthorityDocumentRejectsLifecycleChainAndIdentityDrift(t *testing.T) {
	command := validIssueCommand()
	resolved := validResolvedPolicy()
	record, err := compileRecord(command, resolved, 1, nil, fixedDatabaseTime)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*AuthorityDocument){
		"component hash": func(value *AuthorityDocument) {
			value.ComponentDigests.RevisionPolicy = testDigest("tampered-component")
		},
		"generation one previous": func(value *AuthorityDocument) {
			previous := testDigest("previous")
			value.PreviousAuthorityHash = &previous
		},
		"generation two no previous": func(value *AuthorityDocument) {
			value.Generation = 2
		},
		"unknown status": func(value *AuthorityDocument) {
			value.Status = "revoked-in-place"
		},
		"source provenance missing": func(value *AuthorityDocument) {
			value.PolicySourceID = ""
		},
		"local identity aliases exact artifact": func(value *AuthorityDocument) {
			value.AuthorityID = value.RevisionPolicy.ExactApprovedSources[0].ArtifactID
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneAuthorityDocument(record.Document)
			mutate(&candidate)
			if err := ValidateAuthorityDocument(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateAuthorityDocument() error = %v", err)
			}
		})
	}
}
