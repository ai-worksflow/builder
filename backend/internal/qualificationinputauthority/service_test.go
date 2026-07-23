package qualificationinputauthority

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type leakingIssueStore struct {
	*MemoryStore
}

func (store *leakingIssueStore) Issue(context.Context, Record) (Record, error) {
	return Record{}, errors.New("password=store-super-secret /root/private")
}

func TestServiceIssueBindsExactAuthoritiesReceiptsAndPromotionProjection(t *testing.T) {
	harness := newServiceHarness(t)
	record, err := harness.service.Issue(context.Background(), harness.command)
	if err != nil {
		t.Fatal(err)
	}
	if record.Idempotent || harness.sourceCalls.Load() != 1 || harness.credentialCalls.Load() != 1 {
		t.Fatalf("unexpected first issue result: idempotent=%v source=%d credential=%d", record.Idempotent, harness.sourceCalls.Load(), harness.credentialCalls.Load())
	}
	if err := ValidateRecord(record); err != nil {
		t.Fatal(err)
	}
	for name, item := range map[string]struct {
		kind  string
		proof VerificationProof
	}{
		"source":     {kind: ReceiptKindSource, proof: record.Document.SourceProof},
		"credential": {kind: ReceiptKindCredential, proof: record.Document.CredentialProof},
	} {
		proof := item.proof
		admission, err := harness.store.resolveReceiptAdmission(context.Background(), item.kind, proof.AdmissionHash)
		if err != nil {
			t.Fatalf("resolve %s admission: %v", name, err)
		}
		if admission.Document.RequestHash != proof.RequestHash || admission.Document.ReceiptHash != proof.ReceiptHash ||
			admission.Document.AuthorityID != proof.AuthorityID || admission.Document.ExecutableDigest != proof.ExecutableDigest {
			t.Fatalf("%s proof is not backed by its exact local admission", name)
		}
	}
	binding, err := PromotionBindingFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if binding.AuthorityID != record.Document.AuthorityID || binding.AuthorityHash != record.AuthorityHash ||
		binding.WorkflowInputAuthorityID != harness.resolved.WorkflowInput.AuthorityID ||
		binding.QualificationPolicyAuthorityID != harness.resolved.Policy.AuthorityID ||
		binding.QualificationPlanAuthorityID != harness.resolved.Plan.AuthorityID ||
		binding.SourceAdmissionHash != record.Document.SourceProof.AdmissionHash ||
		binding.CredentialAdmissionHash != record.Document.CredentialProof.AdmissionHash {
		t.Fatalf("Promotion binding omitted an exact authority or proof edge: %+v", binding)
	}
}

func TestServiceExactReplayPrecedesResolverAndVerifiers(t *testing.T) {
	harness := newServiceHarness(t)
	first, err := harness.service.Issue(context.Background(), harness.command)
	if err != nil {
		t.Fatal(err)
	}
	resolverCalls := atomic.Int64{}
	resolver := authorityResolverFunc(func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (ResolvedAuthorities, error) {
		resolverCalls.Add(1)
		return ResolvedAuthorities{}, errors.New("retired")
	})
	source, err := NewSourceVerifier(
		ExecutableBinding{AuthorityID: "retired-source", ExecutableDigest: testDigest("retired-source-executable")},
		func(context.Context, SourceVerificationRequest, []byte, string) (VerificationObservation, error) {
			t.Fatal("source verifier must not run on exact replay")
			return VerificationObservation{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := NewCredentialResolver(
		ExecutableBinding{AuthorityID: "retired-credential", ExecutableDigest: testDigest("retired-credential-executable")},
		func(context.Context, CredentialResolutionRequest, []byte, string) (VerificationObservation, error) {
			t.Fatal("credential resolver must not run on exact replay")
			return VerificationObservation{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		resolver, source, credential, harness.store,
		DatabaseClockFunc(func(context.Context) (time.Time, error) {
			t.Fatal("clock must not run on exact replay")
			return time.Time{}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Issue(context.Background(), harness.command)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Idempotent || replayed.AuthorityHash != first.AuthorityHash || resolverCalls.Load() != 0 {
		t.Fatalf("exact replay did not return immutable result before resolution")
	}

	changed := harness.command
	changed.AuthorityID = uuid.New()
	if _, err := service.Issue(context.Background(), changed); !errors.Is(err, ErrConflict) || resolverCalls.Load() != 0 {
		t.Fatalf("changed operation replay should conflict before resolution, got %v", err)
	}
}

func TestServiceRecoversAdmissionAndIssueCommitUnknown(t *testing.T) {
	harness := newServiceHarness(t)
	harness.store.InjectAdmissionCommitUnknownOnce()
	harness.store.InjectIssueCommitUnknownOnce()
	record, err := harness.service.Issue(context.Background(), harness.command)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Idempotent {
		t.Fatal("post-commit issue recovery must be marked idempotent")
	}
	if _, err := harness.store.resolveReceiptAdmission(
		context.Background(), ReceiptKindSource, record.Document.SourceProof.AdmissionHash,
	); err != nil {
		t.Fatalf("source admission was not recovered: %v", err)
	}
}

func TestNewServiceRejectsVerifierIdentityAndDigestAliasBeforeCallbacks(t *testing.T) {
	for name, credentialBinding := range map[string]ExecutableBinding{
		"identity":   {AuthorityID: "source-authority", ExecutableDigest: testDigest("credential-executable")},
		"executable": {AuthorityID: "credential-authority", ExecutableDigest: testDigest("source-executable")},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			source, err := NewSourceVerifier(
				ExecutableBinding{AuthorityID: "source-authority", ExecutableDigest: testDigest("source-executable")},
				func(context.Context, SourceVerificationRequest, []byte, string) (VerificationObservation, error) {
					calls.Add(1)
					return VerificationObservation{}, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			credential, err := NewCredentialResolver(
				credentialBinding,
				func(context.Context, CredentialResolutionRequest, []byte, string) (VerificationObservation, error) {
					calls.Add(1)
					return VerificationObservation{}, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			store := NewMemoryStore()
			if _, err := NewService(
				store, source, credential, store,
				DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
			); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected constructor alias rejection, got %v", err)
			}
			if calls.Load() != 0 {
				t.Fatal("binding alias triggered an external callback")
			}
		})
	}
}

func TestServiceRejectsVerifierReceiptAliasing(t *testing.T) {
	harness := newServiceHarnessWithBindings(
		t,
		ExecutableBinding{AuthorityID: "source-authority", ExecutableDigest: testDigest("source-executable")},
		ExecutableBinding{AuthorityID: "credential-authority", ExecutableDigest: testDigest("credential-executable")},
		testDigest("shared-receipt"),
		testDigest("shared-receipt"),
	)
	if _, err := harness.service.Issue(context.Background(), harness.command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected receipt-alias rejection, got %v", err)
	}
}

func TestServiceRejectsCrossDomainReceiptBeforeCredentialAdmission(t *testing.T) {
	resolved := testResolvedAuthorities()
	store := NewMemoryStore()
	if err := store.InstallAuthorities(resolved); err != nil {
		t.Fatal(err)
	}
	_, sourceRequestHash, err := EncodeSourceRequest(sourceRequestFromAuthoritySet(resolved))
	if err != nil {
		t.Fatal(err)
	}
	_, credentialRequestHash, err := EncodeCredentialRequest(credentialRequestFromAuthoritySet(resolved))
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewSourceVerifier(
		resolved.SourceVerifier,
		func(_ context.Context, _ SourceVerificationRequest, _ []byte, requestHash string) (VerificationObservation, error) {
			return VerificationObservation{ReceiptHash: testDigest("cross-domain-source-receipt"), RequestHash: requestHash}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := NewCredentialResolver(
		resolved.CredentialResolver,
		func(_ context.Context, _ CredentialResolutionRequest, _ []byte, requestHash string) (VerificationObservation, error) {
			return VerificationObservation{ReceiptHash: sourceRequestHash, RequestHash: requestHash}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		store, source, credential, store,
		DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), commandForResolved(resolved)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-domain credential receipt error=%v, want ErrInvalid", err)
	}
	if _, err := store.resolveReceiptAdmissionForRequest(
		context.Background(), ReceiptKindCredential, credentialRequestHash,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-domain credential admission persisted before rejection: %v", err)
	}
}

func TestStoreRechecksCurrentPolicyAfterExternalVerification(t *testing.T) {
	resolved := testResolvedAuthorities()
	store := NewMemoryStore()
	if err := store.InstallAuthorities(resolved); err != nil {
		t.Fatal(err)
	}
	command := IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		WorkflowInputAuthorityID:       uuid.MustParse(resolved.WorkflowInput.AuthorityID),
		QualificationPolicyAuthorityID: uuid.MustParse(resolved.Policy.AuthorityID),
		QualificationPlanAuthorityID:   uuid.MustParse(resolved.Plan.AuthorityID),
	}
	source, _ := NewSourceVerifier(
		resolved.SourceVerifier,
		func(_ context.Context, _ SourceVerificationRequest, _ []byte, requestHash string) (VerificationObservation, error) {
			return VerificationObservation{ReceiptHash: testDigest("source-receipt"), RequestHash: requestHash}, nil
		},
	)
	credential, _ := NewCredentialResolver(
		resolved.CredentialResolver,
		func(_ context.Context, _ CredentialResolutionRequest, _ []byte, requestHash string) (VerificationObservation, error) {
			if err := store.SetPolicyState(uuid.MustParse(resolved.Policy.AuthorityID), false, "suspended"); err != nil {
				t.Fatal(err)
			}
			return VerificationObservation{ReceiptHash: testDigest("credential-receipt"), RequestHash: requestHash}, nil
		},
	)
	service, err := NewService(
		store, source, credential, store,
		DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), command); !errors.Is(err, ErrStale) {
		t.Fatalf("expected transaction-current Policy rejection, got %v", err)
	}
}

func TestReceiptAdmissionPartialCrashReusesFirstReceipt(t *testing.T) {
	harness := newServiceHarness(t)
	sourceRequest := sourceRequestFromAuthoritySet(harness.resolved)
	sourceBytes, sourceHash, err := EncodeSourceRequest(sourceRequest)
	if err != nil {
		t.Fatal(err)
	}
	firstVerifier, err := NewSourceVerifier(
		ExecutableBinding{AuthorityID: "source-verifier-v1", ExecutableDigest: testDigest("source-executable")},
		func(_ context.Context, _ SourceVerificationRequest, _ []byte, requestHash string) (VerificationObservation, error) {
			return VerificationObservation{ReceiptHash: testDigest("first-source-receipt"), RequestHash: requestHash}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := firstVerifier.verifySource(context.Background(), sourceRequest, sourceBytes, sourceHash)
	if err != nil {
		t.Fatal(err)
	}
	firstAdmission, err := harness.store.admitSourceReceipt(context.Background(), grant)
	if err != nil {
		t.Fatal(err)
	}

	var retrySourceCalls atomic.Int64
	retryVerifier, err := NewSourceVerifier(
		ExecutableBinding{AuthorityID: "source-verifier-v1", ExecutableDigest: testDigest("source-executable")},
		func(_ context.Context, _ SourceVerificationRequest, _ []byte, requestHash string) (VerificationObservation, error) {
			retrySourceCalls.Add(1)
			return VerificationObservation{ReceiptHash: testDigest("different-source-receipt"), RequestHash: requestHash}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := NewCredentialResolver(
		ExecutableBinding{AuthorityID: "credential-resolver-v1", ExecutableDigest: testDigest("credential-executable")},
		func(_ context.Context, _ CredentialResolutionRequest, _ []byte, requestHash string) (VerificationObservation, error) {
			return VerificationObservation{ReceiptHash: testDigest("credential-receipt"), RequestHash: requestHash}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		harness.store, retryVerifier, credential, harness.store,
		DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Issue(context.Background(), harness.command)
	if err != nil {
		t.Fatal(err)
	}
	if retrySourceCalls.Load() != 0 || record.Document.SourceProof.AdmissionHash != firstAdmission.AdmissionHash ||
		record.Document.SourceProof.ReceiptHash != testDigest("first-source-receipt") {
		t.Fatal("retry did not reuse the first durable source admission")
	}
}

func TestReceiptAdmissionRejectsDifferentReviewedExecutableWinner(t *testing.T) {
	for name, concurrent := range map[string]bool{"existing": false, "concurrent": true} {
		t.Run(name, func(t *testing.T) {
			resolved := testResolvedAuthorities()
			store := NewMemoryStore()
			if err := store.InstallAuthorities(resolved); err != nil {
				t.Fatal(err)
			}
			request := sourceRequestFromAuthoritySet(resolved)
			requestBytes, requestHash, err := EncodeSourceRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			injectOld := func() {
				document := ReceiptAdmission{
					AuthorityID: "old-source-verifier", ExecutableDigest: testDigest("old-source-executable"),
					Kind: ReceiptKindSource, ReceiptHash: testDigest("old-source-receipt"), RequestHash: requestHash,
					SchemaVersion: ReceiptAdmissionSchemaV1,
				}
				encoded, admissionHash, encodeErr := EncodeReceiptAdmission(document)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				record := ReceiptAdmissionRecord{
					Document: document, DocumentBytes: encoded, RequestBytes: append([]byte(nil), requestBytes...), AdmissionHash: admissionHash,
				}
				store.mu.Lock()
				store.receiptAdmissions[admissionHash] = record
				store.receiptByRequest[receiptRequestKey(ReceiptKindSource, requestHash)] = admissionHash
				store.mu.Unlock()
			}
			if !concurrent {
				injectOld()
			}
			var reviewedCalls atomic.Int64
			reviewedVerifier, err := NewSourceVerifier(
				resolved.SourceVerifier,
				func(_ context.Context, _ SourceVerificationRequest, _ []byte, exactHash string) (VerificationObservation, error) {
					reviewedCalls.Add(1)
					if concurrent {
						injectOld()
					}
					return VerificationObservation{ReceiptHash: testDigest("reviewed-source-receipt"), RequestHash: exactHash}, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			credential, err := NewCredentialResolver(
				resolved.CredentialResolver,
				func(_ context.Context, _ CredentialResolutionRequest, _ []byte, exactHash string) (VerificationObservation, error) {
					return VerificationObservation{ReceiptHash: testDigest("credential-receipt"), RequestHash: exactHash}, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewService(
				store, reviewedVerifier, credential, store,
				DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
			)
			if err != nil {
				t.Fatal(err)
			}
			command := IssueCommand{
				OperationID: uuid.New(), AuthorityID: uuid.New(),
				WorkflowInputAuthorityID:       uuid.MustParse(resolved.WorkflowInput.AuthorityID),
				QualificationPolicyAuthorityID: uuid.MustParse(resolved.Policy.AuthorityID),
				QualificationPlanAuthorityID:   uuid.MustParse(resolved.Plan.AuthorityID),
			}
			if _, err := service.Issue(context.Background(), command); !errors.Is(err, ErrConflict) {
				t.Fatalf("expected reviewed-binding winner rejection, got %v", err)
			}
			if !concurrent && reviewedCalls.Load() != 0 {
				t.Fatal("existing mismatched admission should fail before external callback")
			}
		})
	}
}

func TestConcurrentDifferentReceiptObservationsUseFirstAdmission(t *testing.T) {
	resolved := testResolvedAuthorities()
	store := NewMemoryStore()
	if err := store.InstallAuthorities(resolved); err != nil {
		t.Fatal(err)
	}
	command := IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		WorkflowInputAuthorityID:       uuid.MustParse(resolved.WorkflowInput.AuthorityID),
		QualificationPolicyAuthorityID: uuid.MustParse(resolved.Policy.AuthorityID),
		QualificationPlanAuthorityID:   uuid.MustParse(resolved.Plan.AuthorityID),
	}
	ready := sync.WaitGroup{}
	ready.Add(2)
	release := make(chan struct{})
	services := make([]*Service, 2)
	for index := range services {
		receiptHash := testDigest("concurrent-source-receipt-" + string(rune('a'+index)))
		source, err := NewSourceVerifier(
			ExecutableBinding{AuthorityID: "source-verifier-v1", ExecutableDigest: testDigest("source-executable")},
			func(_ context.Context, _ SourceVerificationRequest, _ []byte, requestHash string) (VerificationObservation, error) {
				ready.Done()
				<-release
				return VerificationObservation{ReceiptHash: receiptHash, RequestHash: requestHash}, nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		credential, err := NewCredentialResolver(
			ExecutableBinding{AuthorityID: "credential-resolver-v1", ExecutableDigest: testDigest("credential-executable")},
			func(_ context.Context, _ CredentialResolutionRequest, _ []byte, requestHash string) (VerificationObservation, error) {
				return VerificationObservation{ReceiptHash: testDigest("credential-receipt"), RequestHash: requestHash}, nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		services[index], err = NewService(
			store, source, credential, store,
			DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		record Record
		err    error
	}
	results := make(chan result, 2)
	for _, service := range services {
		go func(service *Service) {
			record, err := service.Issue(context.Background(), command)
			results <- result{record: record, err: err}
		}(service)
	}
	ready.Wait()
	close(release)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent issues failed: first=%v second=%v", first.err, second.err)
	}
	if first.record.AuthorityHash != second.record.AuthorityHash ||
		first.record.Document.SourceProof.AdmissionHash != second.record.Document.SourceProof.AdmissionHash ||
		first.record.Document.SourceProof.ReceiptHash != second.record.Document.SourceProof.ReceiptHash {
		t.Fatal("concurrent different observations did not converge on the first immutable admission")
	}
}

func TestStoreWillNotTrustUnresolvedCandidateReceiptHashes(t *testing.T) {
	harness := newServiceHarness(t)
	record, err := harness.service.Issue(context.Background(), harness.command)
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewMemoryStore()
	if err := fresh.InstallAuthorities(harness.resolved); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Issue(context.Background(), record); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Store trusted candidate receipt hashes without local admissions: %v", err)
	}
}

func TestWIAAndPlanAreSingleUseAndReceiptAdmissionsAreReused(t *testing.T) {
	harness := newServiceHarness(t)
	if _, err := harness.service.Issue(context.Background(), harness.command); err != nil {
		t.Fatal(err)
	}
	second := harness.command
	second.OperationID = uuid.New()
	second.AuthorityID = uuid.New()
	if _, err := harness.service.Issue(context.Background(), second); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected WIA/Plan single-use conflict, got %v", err)
	}
	if harness.sourceCalls.Load() != 1 || harness.credentialCalls.Load() != 1 {
		t.Fatalf("same exact component requests should reuse admissions, calls source=%d credential=%d", harness.sourceCalls.Load(), harness.credentialCalls.Load())
	}
}

func TestServerIdentityAndDatabaseTimeFailClosed(t *testing.T) {
	harness := newServiceHarness(t)
	nonRFCVariant := harness.command
	nonRFCVariant.OperationID = uuid.MustParse("11111111-1111-4111-c111-111111111111")
	if _, err := harness.service.Issue(context.Background(), nonRFCVariant); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected non-RFC UUIDv4 rejection, got %v", err)
	}

	harness.service.clock = DatabaseClockFunc(func(context.Context) (time.Time, error) {
		return testIssuedAt.Add(time.Nanosecond), nil
	})
	if _, err := harness.service.Issue(context.Background(), harness.command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected non-millisecond database time rejection, got %v", err)
	}
}

func TestExternalErrorsAreSanitized(t *testing.T) {
	const leaked = "password=super-secret /root/private"
	assertSanitized := func(t *testing.T, err error, class error) {
		t.Helper()
		if !errors.Is(err, class) || strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "/root/private") {
			t.Fatalf("error was not safely classified: %v", err)
		}
	}

	t.Run("source verifier", func(t *testing.T) {
		resolved := testResolvedAuthorities()
		store := NewMemoryStore()
		if err := store.InstallAuthorities(resolved); err != nil {
			t.Fatal(err)
		}
		source, _ := NewSourceVerifier(
			resolved.SourceVerifier,
			func(context.Context, SourceVerificationRequest, []byte, string) (VerificationObservation, error) {
				return VerificationObservation{}, errors.New(leaked)
			},
		)
		credential, _ := NewCredentialResolver(
			resolved.CredentialResolver,
			func(_ context.Context, _ CredentialResolutionRequest, _ []byte, requestHash string) (VerificationObservation, error) {
				return VerificationObservation{ReceiptHash: testDigest("credential-receipt"), RequestHash: requestHash}, nil
			},
		)
		service, err := NewService(
			store, source, credential, store,
			DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Issue(context.Background(), commandForResolved(resolved))
		assertSanitized(t, err, ErrNotReady)
	})

	t.Run("authority resolver", func(t *testing.T) {
		harness := newServiceHarness(t)
		harness.service.resolver = authorityResolverFunc(func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (ResolvedAuthorities, error) {
			return ResolvedAuthorities{}, errors.New(leaked)
		})
		_, err := harness.service.Issue(context.Background(), harness.command)
		assertSanitized(t, err, ErrNotReady)
	})

	t.Run("database clock", func(t *testing.T) {
		harness := newServiceHarness(t)
		harness.service.clock = DatabaseClockFunc(func(context.Context) (time.Time, error) {
			return time.Time{}, errors.New(leaked)
		})
		_, err := harness.service.Issue(context.Background(), harness.command)
		assertSanitized(t, err, ErrNotReady)
	})

	t.Run("store mutation", func(t *testing.T) {
		resolved := testResolvedAuthorities()
		memory := NewMemoryStore()
		if err := memory.InstallAuthorities(resolved); err != nil {
			t.Fatal(err)
		}
		store := &leakingIssueStore{MemoryStore: memory}
		source, _ := NewSourceVerifier(
			resolved.SourceVerifier,
			func(_ context.Context, _ SourceVerificationRequest, _ []byte, requestHash string) (VerificationObservation, error) {
				return VerificationObservation{ReceiptHash: testDigest("source-receipt"), RequestHash: requestHash}, nil
			},
		)
		credential, _ := NewCredentialResolver(
			resolved.CredentialResolver,
			func(_ context.Context, _ CredentialResolutionRequest, _ []byte, requestHash string) (VerificationObservation, error) {
				return VerificationObservation{ReceiptHash: testDigest("credential-receipt"), RequestHash: requestHash}, nil
			},
		)
		service, err := NewService(
			memory, source, credential, store,
			DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Issue(context.Background(), commandForResolved(resolved))
		assertSanitized(t, err, ErrOutcomeUnknown)
	})
}

func commandForResolved(resolved ResolvedAuthorities) IssueCommand {
	return IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		WorkflowInputAuthorityID:       uuid.MustParse(resolved.WorkflowInput.AuthorityID),
		QualificationPolicyAuthorityID: uuid.MustParse(resolved.Policy.AuthorityID),
		QualificationPlanAuthorityID:   uuid.MustParse(resolved.Plan.AuthorityID),
	}
}

func TestResolvedAuthorityRejectsUnrepresentableEdgeDrift(t *testing.T) {
	for name, mutate := range map[string]func(*ResolvedAuthorities){
		"source policy aliased to tree": func(value *ResolvedAuthorities) {
			value.Policy.SourcePolicyDigest = value.Plan.Source.TreeDigest
		},
		"member request aliased to bindings": func(value *ResolvedAuthorities) {
			value.Policy.CredentialProfile.MemberRequestSetDigest = value.Plan.CredentialSet.MemberBindingsDigest
		},
		"credential audience drift": func(value *ResolvedAuthorities) {
			value.Plan.CredentialSet.Audience = "urn:worksflow:other"
		},
		"credential issuer drift": func(value *ResolvedAuthorities) {
			value.Plan.CredentialSet.Issuer = "other-credential-authority"
		},
		"dirty source": func(value *ResolvedAuthorities) {
			value.Plan.Source.Dirty = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			resolved := testResolvedAuthorities()
			mutate(&resolved)
			if err := ValidateResolvedAuthorities(resolved); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid edge, got %v", err)
			}
		})
	}
}
