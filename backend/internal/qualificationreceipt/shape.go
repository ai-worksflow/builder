package qualificationreceipt

import (
	"encoding/json"
	"errors"
	"fmt"
)

// jsonObjectShape makes field presence part of the signed contract. Go's JSON
// decoder otherwise maps an omitted boolean or integer to its zero value, which
// is unsafe for fail-closed evidence such as retries=0 or mocked=false.
type jsonObjectShape struct {
	required     []string
	optional     []string
	strings      []string
	booleans     []string
	numbers      []string
	stringArrays []string
	anyFields    []string
	objects      map[string]jsonObjectShape
	objectArrays map[string]jsonObjectShape
}

func requireExactShape(encoded []byte, shape jsonObjectShape) error {
	value, err := decodeJSONValue(encoded)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("JSON document must be an object")
	}
	return validateObjectShape(object, shape, "$")
}

func validateObjectShape(object map[string]any, shape jsonObjectShape, location string) error {
	allowed := make(map[string]struct{}, len(shape.required)+len(shape.optional))
	for _, name := range shape.required {
		allowed[name] = struct{}{}
		if _, exists := object[name]; !exists {
			return fmt.Errorf("%s is missing required field %q", location, name)
		}
	}
	for _, name := range shape.optional {
		allowed[name] = struct{}{}
	}
	for name := range object {
		if _, exists := allowed[name]; !exists {
			return fmt.Errorf("%s contains unexpected field %q", location, name)
		}
	}
	typed := make(map[string]struct{}, len(object))
	for _, name := range shape.strings {
		if value, exists := object[name]; exists {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s.%s must be a string", location, name)
			}
			typed[name] = struct{}{}
		}
	}
	for _, name := range shape.booleans {
		if value, exists := object[name]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s.%s must be a boolean", location, name)
			}
			typed[name] = struct{}{}
		}
	}
	for _, name := range shape.numbers {
		if value, exists := object[name]; exists {
			if _, ok := value.(json.Number); !ok {
				return fmt.Errorf("%s.%s must be a number", location, name)
			}
			typed[name] = struct{}{}
		}
	}
	for _, name := range shape.stringArrays {
		if value, exists := object[name]; exists {
			items, ok := value.([]any)
			if !ok {
				return fmt.Errorf("%s.%s must be an array of strings", location, name)
			}
			for index, item := range items {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("%s.%s[%d] must be a string", location, name, index)
				}
			}
			typed[name] = struct{}{}
		}
	}
	for _, name := range shape.anyFields {
		if _, exists := object[name]; exists {
			typed[name] = struct{}{}
		}
	}
	for name, childShape := range shape.objects {
		value, exists := object[name]
		if !exists {
			continue
		}
		child, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.%s must be an object", location, name)
		}
		if err := validateObjectShape(child, childShape, location+"."+name); err != nil {
			return err
		}
		typed[name] = struct{}{}
	}
	for name, childShape := range shape.objectArrays {
		value, exists := object[name]
		if !exists {
			continue
		}
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s.%s must be an array", location, name)
		}
		for index, item := range items {
			child, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s[%d] must be an object", location, name, index)
			}
			if err := validateObjectShape(child, childShape, fmt.Sprintf("%s.%s[%d]", location, name, index)); err != nil {
				return err
			}
		}
		typed[name] = struct{}{}
	}
	for name := range object {
		if _, exists := typed[name]; !exists {
			return fmt.Errorf("%s field %q has no strict type declaration", location, name)
		}
	}
	return nil
}

func sourceShape() jsonObjectShape {
	return jsonObjectShape{
		required: []string{"commit", "treeDigestSchema", "treeDigest", "dirty"},
		strings:  []string{"commit", "treeDigestSchema", "treeDigest"}, booleans: []string{"dirty"},
	}
}

func templateReleaseShape() jsonObjectShape {
	return jsonObjectShape{
		required: []string{"id", "contentHash", "approvalReceiptDigest"},
		strings:  []string{"id", "contentHash", "approvalReceiptDigest"},
	}
}

func promotionTargetShape() jsonObjectShape {
	fields := []string{"projectId", "workflowRunId", "nodeKey", "subject", "stageGate"}
	revision := jsonObjectShape{required: []string{"id", "contentHash"}, strings: []string{"id", "contentHash"}}
	return jsonObjectShape{
		required: append(append([]string(nil), fields...), "targetRevision"),
		strings:  fields,
		objects:  map[string]jsonObjectShape{"targetRevision": revision},
	}
}

func totalsShape() jsonObjectShape {
	fields := []string{"discovered", "passed", "failed", "skipped", "flaky", "retried", "mocked"}
	return jsonObjectShape{required: fields, numbers: fields}
}

func encryptionShape() jsonObjectShape {
	fields := []string{"scheme", "keyResource", "keyVersion", "wrappedKey", "nonce", "tag", "additionalData", "additionalDataHash"}
	return jsonObjectShape{required: fields, strings: fields}
}

func artifactIndexShape() jsonObjectShape {
	artifact := jsonObjectShape{
		required:     []string{"id", "path", "type", "mediaType", "sha256", "sizeBytes", "classification", "suiteIds", "requirementIds"},
		optional:     []string{"encryption"},
		strings:      []string{"id", "path", "type", "mediaType", "sha256", "classification"},
		numbers:      []string{"sizeBytes"},
		stringArrays: []string{"suiteIds", "requirementIds"},
		objects:      map[string]jsonObjectShape{"encryption": encryptionShape()},
	}
	return jsonObjectShape{
		required:     []string{"schemaVersion", "runId", "planDigest", "source", "templateRelease", "artifacts"},
		strings:      []string{"schemaVersion", "runId", "planDigest"},
		objects:      map[string]jsonObjectShape{"source": sourceShape(), "templateRelease": templateReleaseShape()},
		objectArrays: map[string]jsonObjectShape{"artifacts": artifact},
	}
}

func receiptShape() jsonObjectShape {
	writerDrain := jsonObjectShape{required: []string{
		"fromVersion", "toVersion", "status", "activeWriters", "inFlightMutations", "completedAt", "evidenceArtifactId",
	}, strings: []string{"fromVersion", "toVersion", "status", "completedAt", "evidenceArtifactId"}, numbers: []string{"activeWriters", "inFlightMutations"}}
	constructor := jsonObjectShape{
		required: []string{"compilerVersion", "buildContractHash", "writerDrain"},
		strings:  []string{"compilerVersion", "buildContractHash"},
		objects:  map[string]jsonObjectShape{"writerDrain": writerDrain},
	}
	suite := jsonObjectShape{
		required: []string{"id", "result", "requirementIds", "testInventoryDigest", "artifactIds"},
		strings:  []string{"id", "result", "testInventoryDigest"}, stringArrays: []string{"requirementIds", "artifactIds"},
	}
	credentialArtifact := jsonObjectShape{
		required: []string{"artifactId", "payloadDigest"}, strings: []string{"artifactId", "payloadDigest"},
	}
	credentialSet := jsonObjectShape{
		required: []string{
			"issuer", "audience", "setHandleHash", "memberBindingsDigest", "memberCount", "issuedAt", "expiresAt", "revokedAt", "issuance", "revocation",
		},
		strings: []string{"issuer", "audience", "setHandleHash", "memberBindingsDigest", "issuedAt", "expiresAt", "revokedAt"},
		numbers: []string{"memberCount"},
		objects: map[string]jsonObjectShape{"issuance": credentialArtifact, "revocation": credentialArtifact},
	}
	index := jsonObjectShape{
		required: []string{"digest", "count", "restrictedEncryptedCount"}, strings: []string{"digest"}, numbers: []string{"count", "restrictedEncryptedCount"},
	}
	return jsonObjectShape{
		required: []string{
			"schemaVersion", "scope", "promotionTarget", "authorityNonce", "authorityExpiresAt", "runId", "planDigest", "prePromotionManifestDigest", "trustPolicyDigest", "sourcePolicyAttestationDigest", "source", "templateRelease",
			"goldenRuntime", "constructor", "suites", "totals", "credentialSet", "artifactIndex", "decision", "startedAt", "completedAt", "issuedAt",
		},
		objects: map[string]jsonObjectShape{
			"promotionTarget": promotionTargetShape(), "source": sourceShape(), "templateRelease": templateReleaseShape(), "constructor": constructor,
			"goldenRuntime": goldenRuntimeShape(), "totals": totalsShape(), "credentialSet": credentialSet, "artifactIndex": index,
		},
		strings: []string{
			"schemaVersion", "scope", "authorityNonce", "authorityExpiresAt", "runId", "planDigest", "prePromotionManifestDigest", "trustPolicyDigest", "sourcePolicyAttestationDigest", "decision", "startedAt", "completedAt", "issuedAt",
		},
		objectArrays: map[string]jsonObjectShape{"suites": suite},
	}
}

func playwrightResultShape() jsonObjectShape {
	config := jsonObjectShape{
		required: []string{"forbidOnly", "retries", "workers"}, booleans: []string{"forbidOnly"}, numbers: []string{"retries", "workers"},
	}
	test := jsonObjectShape{
		required: []string{"caseId", "suiteId", "requirementIds", "contractCriterionIds", "status", "retry", "flaky", "mocked"},
		strings:  []string{"caseId", "suiteId", "status"}, stringArrays: []string{"requirementIds", "contractCriterionIds"}, numbers: []string{"retry"}, booleans: []string{"flaky", "mocked"},
	}
	return jsonObjectShape{
		required:     []string{"schemaVersion", "runId", "testInventoryDigest", "config", "tests", "totals"},
		strings:      []string{"schemaVersion", "runId", "testInventoryDigest"},
		objects:      map[string]jsonObjectShape{"config": config, "totals": totalsShape()},
		objectArrays: map[string]jsonObjectShape{"tests": test},
	}
}

func writerDrainProofShape() jsonObjectShape {
	return jsonObjectShape{
		required: []string{
			"schemaVersion", "planDigest", "templateRelease", "fromVersion", "toVersion", "status", "activeWriters", "inFlightMutations", "completedAt",
		},
		strings: []string{"schemaVersion", "planDigest", "fromVersion", "toVersion", "status", "completedAt"},
		numbers: []string{"activeWriters", "inFlightMutations"},
		objects: map[string]jsonObjectShape{"templateRelease": templateReleaseShape()},
	}
}

func credentialSetMemberShape() jsonObjectShape {
	fields := []string{"slot", "actorId", "kind", "credentialHandleHash"}
	return jsonObjectShape{required: fields, strings: fields}
}

func credentialSetIssuanceShape() jsonObjectShape {
	return jsonObjectShape{
		required: []string{
			"schemaVersion", "runId", "fixtureId", "issuer", "audience", "setHandleHash", "memberBindingsDigest", "memberCount", "members", "status", "issuedAt", "expiresAt",
		},
		strings: []string{"schemaVersion", "runId", "fixtureId", "issuer", "audience", "setHandleHash", "memberBindingsDigest", "status", "issuedAt", "expiresAt"},
		numbers: []string{"memberCount"}, objectArrays: map[string]jsonObjectShape{"members": credentialSetMemberShape()},
	}
}

func credentialSetRevocationShape() jsonObjectShape {
	shape := credentialSetIssuanceShape()
	shape.required = append(shape.required, "revokedAt")
	shape.strings = append(shape.strings, "revokedAt")
	return shape
}

func encryptionAttestationShape() jsonObjectShape {
	artifact := jsonObjectShape{required: []string{
		"artifactId", "path", "ciphertextDigest", "sizeBytes", "keyResource", "keyVersion", "wrappedKeyDigest",
		"additionalDataHash", "encryptionDescriptorDigest", "encryptedAt", "plaintextDisposition", "plaintextDispositionAt", "status",
	}, strings: []string{
		"artifactId", "path", "ciphertextDigest", "keyResource", "keyVersion", "wrappedKeyDigest", "additionalDataHash",
		"encryptionDescriptorDigest", "encryptedAt", "plaintextDisposition", "plaintextDispositionAt", "status",
	}, numbers: []string{"sizeBytes"}}
	return jsonObjectShape{
		required: []string{
			"schemaVersion", "runId", "planDigest", "templateRelease", "manifestDigest", "artifacts", "issuedAt",
		},
		strings:      []string{"schemaVersion", "runId", "planDigest", "manifestDigest", "issuedAt"},
		objects:      map[string]jsonObjectShape{"templateRelease": templateReleaseShape()},
		objectArrays: map[string]jsonObjectShape{"artifacts": artifact},
	}
}

func goldenFaultLedgerAttestationShape() jsonObjectShape {
	entryFields := []string{
		"authorityDigest", "authorityId", "completedAt", "envelopeDigest", "operationKind", "outcome",
		"payloadDigest", "receiptArtifactId", "receiptDigest", "reservationDigest", "reservedAt",
		"resultDigest", "resultId", "state",
	}
	entry := jsonObjectShape{required: entryFields, strings: entryFields}
	return jsonObjectShape{
		required:     []string{"entries", "fixtureId", "issuedAt", "runId", "schemaVersion", "status"},
		strings:      []string{"fixtureId", "issuedAt", "runId", "schemaVersion", "status"},
		objectArrays: map[string]jsonObjectShape{"entries": entry},
	}
}

func trustPolicyShape() jsonObjectShape {
	signer := jsonObjectShape{
		required: []string{"keyId", "algorithm", "identity", "role", "notBefore", "notAfter", "publicKeyPem"},
		optional: []string{"revokedAt"},
		strings:  []string{"keyId", "algorithm", "identity", "role", "notBefore", "notAfter", "revokedAt", "publicKeyPem"},
	}
	key := jsonObjectShape{
		required: []string{"keyId", "algorithm", "identity", "notBefore", "notAfter", "publicKeyPem"}, optional: []string{"revokedAt"},
		strings: []string{"keyId", "algorithm", "identity", "notBefore", "notAfter", "revokedAt", "publicKeyPem"},
	}
	issuer := jsonObjectShape{
		required:     []string{"issuer", "minimumSignatures", "allowedIdentities", "keys"},
		strings:      []string{"issuer"},
		numbers:      []string{"minimumSignatures"},
		stringArrays: []string{"allowedIdentities"},
		objectArrays: map[string]jsonObjectShape{"keys": key},
	}
	recipient := jsonObjectShape{
		required: []string{"keyResource", "keyVersion"}, strings: []string{"keyResource", "keyVersion"},
	}
	encryptionAuthority := jsonObjectShape{
		required:     []string{"minimumSignatures", "allowedIdentities", "keys"},
		numbers:      []string{"minimumSignatures"},
		stringArrays: []string{"allowedIdentities"},
		objectArrays: map[string]jsonObjectShape{"keys": key},
	}
	faultAuthority := jsonObjectShape{
		required:     []string{"minimumSignatures", "allowedIdentities", "keys"},
		numbers:      []string{"minimumSignatures"},
		stringArrays: []string{"allowedIdentities"},
		objectArrays: map[string]jsonObjectShape{"keys": key},
	}
	return jsonObjectShape{
		required: []string{
			"schemaVersion", "minimumSignatures", "maxReceiptAgeSeconds", "maxFutureSkewSeconds", "signers", "credentialIssuers",
			"encryptionRecipients", "encryptionAuthority", "faultAuthority", "faultLedgerAttestor",
		},
		strings: []string{"schemaVersion"},
		numbers: []string{"minimumSignatures", "maxReceiptAgeSeconds", "maxFutureSkewSeconds"},
		objects: map[string]jsonObjectShape{
			"encryptionAuthority": encryptionAuthority,
			"faultAuthority":      faultAuthority,
			"faultLedgerAttestor": faultAuthority,
		},
		objectArrays: map[string]jsonObjectShape{"signers": signer, "credentialIssuers": issuer, "encryptionRecipients": recipient},
	}
}

func promotionAuthorityShape() jsonObjectShape {
	return jsonObjectShape{
		required: []string{
			"schemaVersion", "promotionTarget", "authorityNonce", "authorityIssuedAt", "authorityExpiresAt", "runId", "planDigest", "prePromotionManifestDigest", "source", "templateRelease", "goldenRuntime", "buildContractHash",
			"writerDrainEvidenceArtifactId", "credentialSet", "trustPolicyPath", "trustPolicyDigest",
			"artifactRoot", "artifactIndexDigest", "receiptBundleDigest", "trustedReceiptIssuedAt", "artifactSnapshotId", "artifactSnapshotMode",
			"evidenceSnapshotRoot", "receiptPath", "artifactIndexPath",
			"repositoryRoot", "qualificationManifestPath", "repositorySnapshotId", "repositorySnapshotMode",
			"sourcePolicyStatus", "sourcePolicyToolDigest", "sourcePolicyVerifiedAt", "sourcePolicyAttestationDigest",
			"verifierExecutableDigest", "gitExecutableDigest",
		},
		strings: []string{
			"schemaVersion", "authorityNonce", "authorityIssuedAt", "authorityExpiresAt", "runId", "planDigest", "prePromotionManifestDigest", "buildContractHash", "writerDrainEvidenceArtifactId",
			"trustPolicyPath", "trustPolicyDigest", "artifactRoot", "artifactIndexDigest",
			"evidenceSnapshotRoot", "receiptPath", "artifactIndexPath",
			"receiptBundleDigest", "trustedReceiptIssuedAt", "artifactSnapshotId", "artifactSnapshotMode",
			"repositoryRoot", "qualificationManifestPath", "repositorySnapshotId", "repositorySnapshotMode",
			"sourcePolicyStatus", "sourcePolicyToolDigest", "sourcePolicyVerifiedAt", "sourcePolicyAttestationDigest",
			"verifierExecutableDigest", "gitExecutableDigest",
		},
		objects: map[string]jsonObjectShape{
			"promotionTarget": promotionTargetShape(), "source": sourceShape(), "templateRelease": templateReleaseShape(),
			"goldenRuntime": goldenRuntimeShape(), "credentialSet": credentialSetAuthorityShape(),
		},
	}
}

func goldenRuntimeShape() jsonObjectShape {
	fields := []string{
		"authorityDocumentArtifactId", "authorityDocumentDigest", "faultOperationSetDigest", "fixtureDocumentArtifactId", "fixtureDocumentDigest", "fixtureId",
	}
	return jsonObjectShape{required: fields, strings: fields}
}

func credentialSetAuthorityShape() jsonObjectShape {
	return jsonObjectShape{
		required: []string{"issuer", "audience", "setHandleHash", "memberBindingsDigest", "memberCount"},
		strings:  []string{"issuer", "audience", "setHandleHash", "memberBindingsDigest"}, numbers: []string{"memberCount"},
	}
}

func testInventoryShape() jsonObjectShape {
	criterionSource := jsonObjectShape{
		required: []string{"suiteId", "path", "schemaVersion", "applicationId"},
		strings:  []string{"suiteId", "path", "schemaVersion", "applicationId"},
	}
	testCase := jsonObjectShape{
		required: []string{"caseId", "suiteId", "requirementIds", "contractCriterionIds", "file", "title", "mode"},
		strings:  []string{"caseId", "suiteId", "file", "title", "mode"}, stringArrays: []string{"requirementIds", "contractCriterionIds"},
	}
	return jsonObjectShape{
		required:     []string{"schemaVersion", "criterionSources", "cases"},
		strings:      []string{"schemaVersion"},
		objectArrays: map[string]jsonObjectShape{"criterionSources": criterionSource, "cases": testCase},
	}
}

func referenceAcceptanceCriteriaShape() jsonObjectShape {
	criterion := jsonObjectShape{
		required:     []string{"id", "requirementIds", "statement"},
		strings:      []string{"id", "statement"},
		stringArrays: []string{"requirementIds"},
	}
	return jsonObjectShape{
		required:     []string{"schemaVersion", "applicationId", "criteria"},
		strings:      []string{"schemaVersion", "applicationId"},
		objectArrays: map[string]jsonObjectShape{"criteria": criterion},
	}
}

func qualificationManifestShape() jsonObjectShape {
	criterionSource := jsonObjectShape{
		required: []string{"path", "schemaVersion", "applicationId"},
		strings:  []string{"path", "schemaVersion", "applicationId"},
	}
	suite := jsonObjectShape{
		required: []string{"id", "mode", "executionKind", "status", "coverage", "requirementIds", "requiredArtifacts"},
		optional: []string{
			"commands", "qualificationCommand", "testPaths", "plannedTestPaths", "smokeTestPath", "qualificationGroup", "criterionSource",
			"verificationContractPath", "blockers", "limitations", "receiptPath", "trustPolicyDigest",
		},
		strings:      []string{"id", "mode", "executionKind", "status", "coverage", "qualificationCommand", "smokeTestPath", "verificationContractPath", "qualificationGroup", "receiptPath", "trustPolicyDigest"},
		stringArrays: []string{"requirementIds", "requiredArtifacts", "commands", "testPaths", "plannedTestPaths", "blockers", "limitations"},
		objects:      map[string]jsonObjectShape{"criterionSource": criterionSource},
	}
	return jsonObjectShape{
		required:     []string{"schemaVersion", "subject", "sourceDocuments", "qualificationSupportPaths", "policy", "suites"},
		optional:     []string{"trust", "trustPolicy", "trustPolicyDigest"},
		strings:      []string{"schemaVersion", "subject", "trustPolicyDigest"},
		stringArrays: []string{"sourceDocuments", "qualificationSupportPaths"},
		anyFields:    []string{"trust", "trustPolicy"},
		objects:      map[string]jsonObjectShape{"policy": qualificationPolicyShape()},
		objectArrays: map[string]jsonObjectShape{"suites": suite},
	}
}

func qualificationPolicyShape() jsonObjectShape {
	return jsonObjectShape{required: []string{
		"stageExitRequiresExternalQualification", "allowSkippedTests", "allowMocks", "allowMutableRuntimeImages",
		"credentialBearingArtifacts", "passingInternalSuitesAreStageExitEvidence",
	}, booleans: []string{
		"stageExitRequiresExternalQualification", "allowSkippedTests", "allowMocks", "allowMutableRuntimeImages", "passingInternalSuitesAreStageExitEvidence",
	}, strings: []string{"credentialBearingArtifacts"}}
}
