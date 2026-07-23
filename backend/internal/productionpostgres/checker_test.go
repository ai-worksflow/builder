package productionpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/platform"
)

func safeFacts(kind RoleKind, roleName string) sessionFacts {
	facts := sessionFacts{
		roleCount:                     1,
		roleName:                      roleName,
		sessionRoleName:               roleName,
		canLogin:                      true,
		roleInherits:                  true,
		roleSettingIsNone:             true,
		reachableRoleCount:            2,
		stableRoleCount:               10,
		databaseCount:                 1,
		databaseName:                  "worksflow",
		transportUsesTLS:              true,
		primaryIsReadWrite:            true,
		schemaCount:                   1,
		schemaName:                    "worksflow",
		schemaOwnedByMigrationOwner:   true,
		ownedBoundaryRelationCount:    120,
		migrationOwnedBoundaryCount:   120,
		routineCount:                  80,
		migrationOwnedRoutineCount:    80,
		reachableTablePrivilegeCount:  100,
		reachableColumnPrivilegeCount: 1000,
		reachableRoutineExecuteCount:  10,
	}
	switch kind {
	case RoleApplication:
		facts.applicationReachable = true
		facts.reachableHasSchemaUsage = true
	case RoleMigrator:
		facts.migrationOwnerReachable = true
		facts.reachableOwnsSchema = true
		facts.reachableHasSchemaUsage = true
		facts.reachableCanCreateInSchema = true
		facts.reachableOwnedRelationCount = 120
		facts.reachableOwnedRoutineCount = 80
	case RoleQualification:
		facts.reachableRoleCount = 1
		facts.reachableTablePrivilegeCount = 0
		facts.reachableColumnPrivilegeCount = 0
		facts.reachableRoutineExecuteCount = 0
	case RolePromotion:
		facts.directMembershipCount = 1
		facts.exactInheritOnlyDirectMembershipCount = 1
		facts.directMembershipGroupMemberCount = 1
		facts.promotionOperatorReachable = true
		facts.reachableHasSchemaUsage = true
		facts.reachableTablePrivilegeCount = 0
		facts.reachableColumnPrivilegeCount = 0
		facts.reachableRoutineExecuteCount = 4
		facts.promotionExactRoutineExecuteCount = 4
	case RolePolicy:
		facts.policyOperatorReachable = true
		facts.reachableHasSchemaUsage = true
		facts.reachableTablePrivilegeCount = 0
		facts.reachableColumnPrivilegeCount = 0
		facts.reachableRoutineExecuteCount = 4
		facts.policyExactRoutineExecuteCount = 4
	case RoleInputPrecommit:
		facts.directMembershipCount = 1
		facts.exactInheritOnlyDirectMembershipCount = 1
		facts.directMembershipGroupMemberCount = 1
		facts.inputPrecommitOperatorReachable = true
		facts.reachableHasSchemaUsage = true
		facts.reachableTablePrivilegeCount = 0
		facts.reachableColumnPrivilegeCount = 0
		facts.reachableRoutineExecuteCount = 3
		facts.inputPrecommitExactRoutineExecuteCount = 3
	case RoleSourceVerifier:
		facts.directMembershipCount = 1
		facts.exactInheritOnlyDirectMembershipCount = 1
		facts.directMembershipGroupMemberCount = 1
		facts.sourceVerifierOperatorReachable = true
		facts.reachableHasSchemaUsage = true
		facts.reachableTablePrivilegeCount = 0
		facts.reachableColumnPrivilegeCount = 0
		facts.reachableRoutineExecuteCount = 3
		facts.sourceVerifierExactRoutineExecuteCount = 3
	case RoleCredentialResolver:
		facts.directMembershipCount = 1
		facts.exactInheritOnlyDirectMembershipCount = 1
		facts.directMembershipGroupMemberCount = 1
		facts.credentialResolverOperatorReachable = true
		facts.reachableHasSchemaUsage = true
		facts.reachableTablePrivilegeCount = 0
		facts.reachableColumnPrivilegeCount = 0
		facts.reachableRoutineExecuteCount = 3
		facts.credentialResolverExactRoutineExecuteCount = 3
	case RoleHandoff:
		facts.directMembershipCount = 1
		facts.exactInheritOnlyDirectMembershipCount = 1
		facts.directMembershipGroupMemberCount = 1
		facts.handoffOperatorReachable = true
		facts.reachableHasSchemaUsage = true
		facts.reachableTablePrivilegeCount = 0
		facts.reachableColumnPrivilegeCount = 0
		facts.reachableRoutineExecuteCount = 2
		facts.handoffExactRoutineExecuteCount = 2
	}
	return facts
}

func TestValidateSessionFactsAcceptsExactRoleBoundaries(t *testing.T) {
	for _, role := range []struct {
		kind RoleKind
		name string
	}{
		{RoleApplication, "app_login"},
		{RoleMigrator, "migrator_login"},
		{RoleQualification, "qualification_login"},
		{RolePromotion, "promotion_login"},
		{RolePolicy, "policy_login"},
		{RoleInputPrecommit, "input_precommit_login"},
		{RoleSourceVerifier, "source_verifier_login"},
		{RoleCredentialResolver, "credential_resolver_login"},
		{RoleHandoff, "handoff_login"},
	} {
		facts := safeFacts(role.kind, role.name)
		if violations := validateSessionFacts(role.kind, role.name, "worksflow", facts); len(violations) != 0 {
			t.Fatalf("%s safe facts rejected: %v", role.kind, violations)
		}
	}
}

func TestValidateSessionFactsFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		kind   RoleKind
		mutate func(*sessionFacts)
	}{
		{"switched current role", RoleApplication, func(f *sessionFacts) { f.sessionRoleName = "postgres" }},
		{"role setting is not none", RoleApplication, func(f *sessionFacts) { f.roleSettingIsNone = false }},
		{"cluster authority", RoleApplication, func(f *sessionFacts) { f.hasClusterAuthority = true }},
		{"reachable authority", RoleApplication, func(f *sessionFacts) { f.reachableHasClusterAuthority = true }},
		{"role admin", RoleApplication, func(f *sessionFacts) { f.hasAdminOption = true }},
		{"missing stable role", RoleApplication, func(f *sessionFacts) { f.stableRoleCount = 8 }},
		{"unsafe stable role", RoleApplication, func(f *sessionFacts) { f.unsafeStableRoleCount = 1 }},
		{"stable outgoing membership", RoleApplication, func(f *sessionFacts) { f.stableOutgoingMembershipCount = 1 }},
		{"administered stable role", RoleApplication, func(f *sessionFacts) { f.stableAdministeredRoleCount = 1 }},
		{"database owner", RoleApplication, func(f *sessionFacts) { f.reachableOwnsDatabase = true }},
		{"database create", RoleApplication, func(f *sessionFacts) { f.reachableCanCreateDatabaseObjects = true }},
		{"TLS absent", RoleApplication, func(f *sessionFacts) { f.transportUsesTLS = false }},
		{"read-only server", RoleApplication, func(f *sessionFacts) { f.primaryIsReadWrite = false }},
		{"wrong schema owner", RoleApplication, func(f *sessionFacts) { f.schemaOwnedByMigrationOwner = false }},
		{"direct ACL", RoleApplication, func(f *sessionFacts) { f.directColumnACLCount = 1 }},
		{"application extra role", RoleApplication, func(f *sessionFacts) { f.reachableRoleCount = 3 }},
		{"application migration reach", RoleApplication, func(f *sessionFacts) { f.migrationOwnerReachable = true }},
		{"application fault operator reach", RoleApplication, func(f *sessionFacts) { f.goldenFaultOperatorReachable = true }},
		{"application promotion operator reach", RoleApplication, func(f *sessionFacts) { f.promotionOperatorReachable = true }},
		{"application policy operator reach", RoleApplication, func(f *sessionFacts) { f.policyOperatorReachable = true }},
		{"application schema create", RoleApplication, func(f *sessionFacts) { f.reachableCanCreateInSchema = true }},
		{"application relation owner", RoleApplication, func(f *sessionFacts) { f.reachableOwnedRelationCount = 1 }},
		{"migrator app reach", RoleMigrator, func(f *sessionFacts) { f.applicationReachable = true }},
		{"migrator policy operator reach", RoleMigrator, func(f *sessionFacts) { f.policyOperatorReachable = true }},
		{"migrator no create", RoleMigrator, func(f *sessionFacts) { f.reachableCanCreateInSchema = false }},
		{"migrator relation owner drift", RoleMigrator, func(f *sessionFacts) { f.migrationOwnedBoundaryCount-- }},
		{"migrator routine owner drift", RoleMigrator, func(f *sessionFacts) { f.migrationOwnedRoutineCount-- }},
		{"auditor app reach", RoleQualification, func(f *sessionFacts) { f.applicationReachable = true; f.reachableRoleCount = 2 }},
		{"auditor fault operator reach", RoleQualification, func(f *sessionFacts) { f.goldenFaultOperatorReachable = true; f.reachableRoleCount = 2 }},
		{"auditor policy operator reach", RoleQualification, func(f *sessionFacts) { f.policyOperatorReachable = true; f.reachableRoleCount = 2 }},
		{"auditor table read", RoleQualification, func(f *sessionFacts) { f.reachableTablePrivilegeCount = 1 }},
		{"auditor column read", RoleQualification, func(f *sessionFacts) { f.reachableColumnPrivilegeCount = 1 }},
		{"auditor sequence use", RoleQualification, func(f *sessionFacts) { f.reachableSequencePrivilegeCount = 1 }},
		{"auditor function execute", RoleQualification, func(f *sessionFacts) { f.reachableRoutineExecuteCount = 1 }},
		{"auditor function owner", RoleQualification, func(f *sessionFacts) { f.reachableOwnedRoutineCount = 1 }},
		{"auditor direct membership", RoleQualification, func(f *sessionFacts) {
			f.directMembershipCount = 1
			f.exactInheritOnlyDirectMembershipCount = 1
		}},
		{"promotion app reach", RolePromotion, func(f *sessionFacts) { f.applicationReachable = true }},
		{"promotion missing group", RolePromotion, func(f *sessionFacts) { f.promotionOperatorReachable = false }},
		{"promotion login noinherit", RolePromotion, func(f *sessionFacts) { f.roleInherits = false }},
		{"promotion missing direct membership", RolePromotion, func(f *sessionFacts) {
			f.directMembershipCount = 0
			f.exactInheritOnlyDirectMembershipCount = 0
		}},
		{"promotion settable membership", RolePromotion, func(f *sessionFacts) {
			f.exactInheritOnlyDirectMembershipCount = 0
		}},
		{"promotion extra direct membership", RolePromotion, func(f *sessionFacts) {
			f.directMembershipCount = 2
			f.exactInheritOnlyDirectMembershipCount = 2
		}},
		{"promotion operator shared with another login", RolePromotion, func(f *sessionFacts) {
			f.directMembershipGroupMemberCount = 2
		}},
		{"promotion table privilege", RolePromotion, func(f *sessionFacts) { f.reachableTablePrivilegeCount = 1 }},
		{"promotion boundary table privilege", RolePromotion, func(f *sessionFacts) { f.promotionBoundaryTablePrivilegeCount = 1 }},
		{"promotion substituted table", RolePromotion, func(f *sessionFacts) { f.promotionUnexpectedTablePrivilegeCount = 1 }},
		{"promotion non-select table privilege", RolePromotion, func(f *sessionFacts) { f.promotionUnexpectedTablePrivilegeCount = 1 }},
		{"promotion column-only third table", RolePromotion, func(f *sessionFacts) { f.promotionUnexpectedColumnPrivilegeCount = 1 }},
		{"promotion sequence", RolePromotion, func(f *sessionFacts) { f.reachableSequencePrivilegeCount = 1 }},
		{"promotion missing routine", RolePromotion, func(f *sessionFacts) { f.reachableRoutineExecuteCount = 3; f.promotionExactRoutineExecuteCount = 3 }},
		{"promotion extra routine", RolePromotion, func(f *sessionFacts) {
			f.reachableRoutineExecuteCount = 5
			f.promotionUnexpectedRoutineExecuteCount = 1
		}},
		{"promotion substituted routine", RolePromotion, func(f *sessionFacts) {
			f.promotionExactRoutineExecuteCount = 0
			f.promotionUnexpectedRoutineExecuteCount = 1
		}},
		{"promotion owner", RolePromotion, func(f *sessionFacts) { f.reachableOwnedRelationCount = 1 }},
		{"promotion policy operator reach", RolePromotion, func(f *sessionFacts) { f.policyOperatorReachable = true }},
		{"policy app reach", RolePolicy, func(f *sessionFacts) { f.applicationReachable = true }},
		{"policy missing group", RolePolicy, func(f *sessionFacts) { f.policyOperatorReachable = false }},
		{"policy promotion operator reach", RolePolicy, func(f *sessionFacts) { f.promotionOperatorReachable = true }},
		{"policy table access", RolePolicy, func(f *sessionFacts) { f.reachableTablePrivilegeCount = 1 }},
		{"policy column access", RolePolicy, func(f *sessionFacts) { f.reachableColumnPrivilegeCount = 1 }},
		{"policy sequence access", RolePolicy, func(f *sessionFacts) { f.reachableSequencePrivilegeCount = 1 }},
		{"policy missing routine", RolePolicy, func(f *sessionFacts) { f.reachableRoutineExecuteCount = 3; f.policyExactRoutineExecuteCount = 3 }},
		{"policy extra routine", RolePolicy, func(f *sessionFacts) { f.reachableRoutineExecuteCount = 5; f.policyUnexpectedRoutineExecuteCount = 1 }},
		{"policy substituted routine", RolePolicy, func(f *sessionFacts) { f.policyExactRoutineExecuteCount = 3; f.policyUnexpectedRoutineExecuteCount = 1 }},
		{"policy relation owner", RolePolicy, func(f *sessionFacts) { f.reachableOwnedRelationCount = 1 }},
		{"policy routine owner", RolePolicy, func(f *sessionFacts) { f.reachableOwnedRoutineCount = 1 }},
		{"policy non-relation schema object owner", RolePolicy, func(f *sessionFacts) { f.reachableOwnedSchemaObjectCount = 1 }},
		{"application input-precommit reach", RoleApplication, func(f *sessionFacts) { f.inputPrecommitOperatorReachable = true }},
		{"migrator source-verifier reach", RoleMigrator, func(f *sessionFacts) { f.sourceVerifierOperatorReachable = true }},
		{"auditor credential-resolver reach", RoleQualification, func(f *sessionFacts) { f.credentialResolverOperatorReachable = true; f.reachableRoleCount = 2 }},
		{"promotion input-precommit operator reach", RolePromotion, func(f *sessionFacts) { f.inputPrecommitOperatorReachable = true }},
		{"policy source-verifier reach", RolePolicy, func(f *sessionFacts) { f.sourceVerifierOperatorReachable = true }},
		{"input-precommit missing group", RoleInputPrecommit, func(f *sessionFacts) { f.inputPrecommitOperatorReachable = false }},
		{"input-precommit login noinherit", RoleInputPrecommit, func(f *sessionFacts) { f.roleInherits = false }},
		{"input-precommit settable membership", RoleInputPrecommit, func(f *sessionFacts) {
			f.exactInheritOnlyDirectMembershipCount = 0
		}},
		{"input-precommit table access", RoleInputPrecommit, func(f *sessionFacts) { f.reachableTablePrivilegeCount = 1 }},
		{"input-precommit extra routine", RoleInputPrecommit, func(f *sessionFacts) {
			f.reachableRoutineExecuteCount = 4
			f.inputPrecommitUnexpectedRoutineExecuteCount = 1
		}},
		{"input-precommit source reach", RoleInputPrecommit, func(f *sessionFacts) { f.sourceVerifierOperatorReachable = true }},
		{"source-verifier missing group", RoleSourceVerifier, func(f *sessionFacts) { f.sourceVerifierOperatorReachable = false }},
		{"source-verifier extra direct membership", RoleSourceVerifier, func(f *sessionFacts) {
			f.directMembershipCount = 2
			f.exactInheritOnlyDirectMembershipCount = 2
		}},
		{"source-verifier column access", RoleSourceVerifier, func(f *sessionFacts) { f.reachableColumnPrivilegeCount = 1 }},
		{"source-verifier substituted routine", RoleSourceVerifier, func(f *sessionFacts) {
			f.sourceVerifierExactRoutineExecuteCount = 2
			f.sourceVerifierUnexpectedRoutineExecuteCount = 1
		}},
		{"source-verifier credential reach", RoleSourceVerifier, func(f *sessionFacts) { f.credentialResolverOperatorReachable = true }},
		{"credential-resolver missing group", RoleCredentialResolver, func(f *sessionFacts) { f.credentialResolverOperatorReachable = false }},
		{"credential-resolver missing direct membership", RoleCredentialResolver, func(f *sessionFacts) {
			f.directMembershipCount = 0
			f.exactInheritOnlyDirectMembershipCount = 0
		}},
		{"credential-resolver sequence access", RoleCredentialResolver, func(f *sessionFacts) { f.reachableSequencePrivilegeCount = 1 }},
		{"credential-resolver missing routine", RoleCredentialResolver, func(f *sessionFacts) {
			f.reachableRoutineExecuteCount = 2
			f.credentialResolverExactRoutineExecuteCount = 2
		}},
		{"credential-resolver input reach", RoleCredentialResolver, func(f *sessionFacts) { f.inputPrecommitOperatorReachable = true }},
		{"application handoff reach", RoleApplication, func(f *sessionFacts) { f.handoffOperatorReachable = true }},
		{"handoff missing group", RoleHandoff, func(f *sessionFacts) { f.handoffOperatorReachable = false }},
		{"handoff settable membership", RoleHandoff, func(f *sessionFacts) { f.exactInheritOnlyDirectMembershipCount = 0 }},
		{"handoff promotion reach", RoleHandoff, func(f *sessionFacts) { f.promotionOperatorReachable = true }},
		{"handoff table access", RoleHandoff, func(f *sessionFacts) { f.reachableTablePrivilegeCount = 1 }},
		{"handoff missing routine", RoleHandoff, func(f *sessionFacts) {
			f.reachableRoutineExecuteCount = 1
			f.handoffExactRoutineExecuteCount = 1
		}},
		{"handoff extra routine", RoleHandoff, func(f *sessionFacts) {
			f.reachableRoutineExecuteCount = 3
			f.handoffUnexpectedRoutineExecuteCount = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := string(test.kind) + "_login"
			facts := safeFacts(test.kind, name)
			test.mutate(&facts)
			if violations := validateSessionFacts(test.kind, name, "worksflow", facts); len(violations) == 0 {
				t.Fatal("unsafe session facts were accepted")
			}
		})
	}
}

type unusedRow struct{ err error }

func (row unusedRow) Scan(...any) error { return row.err }

type unusedQueryer struct{ err error }

func (queryer unusedQueryer) QueryRowContext(context.Context, string, ...any) rowScanner {
	return unusedRow{err: queryer.err}
}

func testConfig() Config {
	return Config{
		ApplicationDSN:                    secureTestDSN("app_login", "app-secret", "db.internal", "5432", "worksflow"),
		MigratorDSN:                       secureTestDSN("migrator_login", "migrator-secret", "db.internal", "5432", "worksflow"),
		QualificationDSN:                  secureTestDSN("qualification_login", "qualification-secret", "db.internal", "5432", "worksflow"),
		PromotionDSN:                      secureTestDSN("promotion_login", "promotion-secret", "db.internal", "5432", "worksflow"),
		PromotionSessionAffinity:          PromotionSessionAffinityDirect,
		PromotionRuntimeGate:              PromotionRuntimeGateDisabledPendingInputPrecommitAuthorityCanary,
		PolicyDSN:                         secureTestDSN("policy_login", "policy-secret", "db.internal", "5432", "worksflow"),
		InputPrecommitDSN:                 secureTestDSN("input_precommit_login", "input-precommit-secret", "db.internal", "5432", "worksflow"),
		InputPrecommitSessionAffinity:     PromotionSessionAffinityDirect,
		SourceVerifierDSN:                 secureTestDSN("source_verifier_login", "source-verifier-secret", "db.internal", "5432", "worksflow"),
		SourceVerifierSessionAffinity:     PromotionSessionAffinityDirect,
		CredentialResolverDSN:             secureTestDSN("credential_resolver_login", "credential-resolver-secret", "db.internal", "5432", "worksflow"),
		CredentialResolverSessionAffinity: PromotionSessionAffinityDirect,
		HandoffDSN:                        secureTestDSN("handoff_login", "handoff-secret", "db.internal", "5432", "worksflow"),
		HandoffSessionAffinity:            PromotionSessionAffinityDirect,
		Schema:                            "worksflow",
	}
}

func TestVerifyWithDependenciesReturnsSafeStructuredResult(t *testing.T) {
	configuration := testConfig()
	opened := make([]RoleKind, 0, 9)
	closed := 0
	inspected := 0
	trustChecked := false
	dependencies := verificationDependencies{
		now: func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) },
		verifyTrustAnchor: func(path string) error {
			trustChecked = true
			if path != "/etc/worksflow/postgres-ca.pem" {
				t.Fatalf("trust anchor = %q", path)
			}
			return nil
		},
		open: func(kind RoleKind, dsn string) (*databaseHandle, error) {
			opened = append(opened, kind)
			if !strings.Contains(dsn, "search_path=worksflow") {
				t.Fatalf("%s DSN was not safely scoped", kind)
			}
			return &databaseHandle{
				queryer: unusedQueryer{},
				ping:    func(context.Context) error { return nil },
				close:   func() error { closed++; return nil },
			}, nil
		},
		verifyApplication: func(_ context.Context, database *sql.DB, environment string) error {
			if database != nil || environment != "production" {
				t.Fatalf("application verifier received unexpected inputs: %p %q", database, environment)
			}
			return nil
		},
		inspect: func(_ context.Context, _ sessionQueryer, schema string) (sessionFacts, error) {
			if schema != "worksflow" {
				t.Fatalf("inspect schema = %q", schema)
			}
			roles := []struct {
				kind RoleKind
				name string
			}{
				{RoleApplication, "app_login"},
				{RoleMigrator, "migrator_login"},
				{RoleQualification, "qualification_login"},
				{RolePromotion, "promotion_login"},
				{RolePolicy, "policy_login"},
				{RoleInputPrecommit, "input_precommit_login"},
				{RoleSourceVerifier, "source_verifier_login"},
				{RoleCredentialResolver, "credential_resolver_login"},
				{RoleHandoff, "handoff_login"},
			}
			role := roles[inspected]
			inspected++
			return safeFacts(role.kind, role.name), nil
		},
	}
	result, err := verifyWithDependencies(context.Background(), configuration, dependencies)
	if err != nil {
		t.Fatalf("verifyWithDependencies: %v", err)
	}
	if result.Status != StatusPassed || len(result.Roles) != 9 || len(opened) != 9 || closed != 9 || !trustChecked {
		t.Fatalf("unexpected result/open/close state: %#v, %v, %d", result, opened, closed)
	}
	if len(result.ExcludedClaims) != 5 || result.ExcludedClaims[1] != "gc-scheduler-qualification" ||
		result.PromotionSessionAffinity != PromotionSessionAffinityDirect ||
		result.InputPrecommitSessionAffinity != PromotionSessionAffinityDirect ||
		result.SourceVerifierSessionAffinity != PromotionSessionAffinityDirect ||
		result.CredentialResolverSessionAffinity != PromotionSessionAffinityDirect ||
		result.HandoffSessionAffinity != PromotionSessionAffinityDirect ||
		result.PromotionRuntimeGate != PromotionRuntimeGateDisabledPendingInputPrecommitAuthorityCanary {
		t.Fatalf("excluded claims are missing: %#v", result.ExcludedClaims)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"app-secret", "migrator-secret", "qualification-secret", "promotion-secret", "policy-secret", "input-precommit-secret", "source-verifier-secret", "credential-resolver-secret", "handoff-secret", "db.internal", "postgres://"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("structured result exposed credential material %q: %s", secret, encoded)
		}
	}
}

func TestVerifyWithDependenciesClassifiesFailuresWithoutDriverDetails(t *testing.T) {
	configuration := testConfig()
	driverSecret := errors.New("dial postgres://app_login:do-not-expose@db.internal/worksflow")
	workflowAuthorityTriggerPosture := platform.ErrUnsafePostgresAPIRolePosture
	tests := []struct {
		name     string
		mutate   func(*verificationDependencies)
		sentinel error
		code     string
	}{
		{"trust anchor", func(dependencies *verificationDependencies) {
			dependencies.verifyTrustAnchor = func(string) error { return driverSecret }
		}, ErrInvalidConfiguration, FailureConfigurationInvalid},
		{"open", func(dependencies *verificationDependencies) {
			dependencies.open = func(RoleKind, string) (*databaseHandle, error) { return nil, driverSecret }
		}, ErrOperational, FailureConnectionUnavailable},
		{"application Workflow authority trigger posture", func(dependencies *verificationDependencies) {
			dependencies.verifyApplication = func(context.Context, *sql.DB, string) error {
				return workflowAuthorityTriggerPosture
			}
		}, ErrUnsafePosture, FailureApplicationPostureUnsafe},
		{"qualification-policy posture", func(dependencies *verificationDependencies) {
			index := 0
			dependencies.inspect = func(context.Context, sessionQueryer, string) (sessionFacts, error) {
				roles := []struct {
					kind RoleKind
					name string
				}{{RoleApplication, "app_login"}, {RoleMigrator, "migrator_login"}, {RoleQualification, "qualification_login"}, {RolePromotion, "promotion_login"}, {RolePolicy, "policy_login"}, {RoleInputPrecommit, "input_precommit_login"}, {RoleSourceVerifier, "source_verifier_login"}, {RoleCredentialResolver, "credential_resolver_login"}, {RoleHandoff, "handoff_login"}}
				role := roles[index]
				index++
				facts := safeFacts(role.kind, role.name)
				if role.kind == RolePolicy {
					facts.policyExactRoutineExecuteCount--
				}
				return facts, nil
			}
		}, ErrUnsafePosture, FailurePolicyPostureUnsafe},
		{"inspect", func(dependencies *verificationDependencies) {
			dependencies.inspect = func(context.Context, sessionQueryer, string) (sessionFacts, error) {
				return sessionFacts{}, driverSecret
			}
		}, ErrOperational, FailureCatalogInspectionFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := 0
			dependencies := verificationDependencies{
				now:               time.Now,
				verifyTrustAnchor: func(string) error { return nil },
				open: func(RoleKind, string) (*databaseHandle, error) {
					return &databaseHandle{
						queryer: unusedQueryer{},
						ping:    func(context.Context) error { return nil },
						close:   func() error { return nil },
					}, nil
				},
				verifyApplication: func(context.Context, *sql.DB, string) error { return nil },
				inspect: func(context.Context, sessionQueryer, string) (sessionFacts, error) {
					roles := []struct {
						kind RoleKind
						name string
					}{{RoleApplication, "app_login"}, {RoleMigrator, "migrator_login"}, {RoleQualification, "qualification_login"}, {RolePromotion, "promotion_login"}, {RolePolicy, "policy_login"}, {RoleInputPrecommit, "input_precommit_login"}, {RoleSourceVerifier, "source_verifier_login"}, {RoleCredentialResolver, "credential_resolver_login"}, {RoleHandoff, "handoff_login"}}
					role := roles[index]
					index++
					return safeFacts(role.kind, role.name), nil
				},
			}
			test.mutate(&dependencies)
			result, err := verifyWithDependencies(context.Background(), configuration, dependencies)
			if !errors.Is(err, test.sentinel) || result.Failure == nil || result.Failure.Code != test.code {
				t.Fatalf("failure = %#v, %v", result.Failure, err)
			}
			if test.code == FailureApplicationPostureUnsafe && result.Failure.Role != RoleApplication {
				t.Fatalf("application posture failure role = %q", result.Failure.Role)
			}
			if strings.Contains(err.Error(), "do-not-expose") {
				t.Fatalf("failure exposed driver detail: %v", err)
			}
		})
	}
}

func TestSessionPostureQueryUsesDynamicTrustedSchemaClosure(t *testing.T) {
	for _, required := range []string{
		"pg_catalog.pg_class",
		"pg_catalog.pg_attribute",
		"pg_catalog.pg_proc",
		"schema_object_owner_facts",
		"direct_membership_facts",
		"role.rolinherit",
		"current_setting('role') = 'none'",
		"membership.inherit_option",
		"NOT membership.set_option",
		"NOT membership.admin_option",
		"exact_inherit_only_membership_count",
		"membership_group_member_count",
		"pg_catalog.pg_type",
		"pg_catalog.pg_opclass",
		"pg_catalog.pg_extension",
		"relation.relnamespace = schema_state.schema_oid",
		"routine.pronamespace = schema_state.schema_oid",
		"pg_catalog.has_column_privilege",
		"pg_catalog.has_function_privilege",
		"promotion_boundary_table_privilege_count",
		"promotion_unexpected_table_privilege_count",
		"promotion_unexpected_column_privilege_count",
		"promotion_exact_routine_execute_count",
		"promotion_unexpected_routine_execute_count",
		"consume_qualification_promotion_v2",
		"inspect_qualification_promotion_v2_operation",
		"inspect_historical_qualification_promotion_v1_operation",
		"policy_operator_is_reachable",
		"policy_exact_routine_execute_count",
		"policy_unexpected_routine_execute_count",
		"issue_qualification_policy_authority_v1",
		"inspect_qualification_policy_operation_v1",
		"resolve_qualification_policy_authority_v1",
		"resolve_current_qualification_policy_authority_v1",
		"input_precommit_operator_is_reachable",
		"source_verifier_operator_is_reachable",
		"credential_resolver_operator_is_reachable",
		"handoff_operator_is_reachable",
		"input_precommit_exact_routine_execute_count",
		"source_verifier_exact_routine_execute_count",
		"credential_resolver_exact_routine_execute_count",
		"handoff_exact_routine_execute_count",
		"issue_qualification_input_precommit_v1",
		"resolve_qualification_input_precommit_for_promotion_v1",
		"admit_qualification_input_source_receipt_v1",
		"admit_qualification_input_credential_receipt_v1",
		"complete_qualification_promotion_v2_handoff",
		"inspect_qualification_promotion_v2_handoff_completion",
		"pg_catalog.pg_stat_ssl",
		"pg_catalog.pg_is_in_recovery",
		"membership.inherit_option OR membership.set_option",
		"namespace.nspname = $1",
	} {
		if !strings.Contains(sessionPostureQuery, required) {
			t.Fatalf("session posture query does not pin %q", required)
		}
	}
	if strings.Contains(sessionPostureQuery, "SET ROLE") || strings.Contains(sessionPostureQuery, "information_schema") {
		t.Fatal("session posture query mutates the session or uses lossy information_schema views")
	}
	for _, forbidden := range []string{
		"consume_verified_qualification_promotion",
		"assert_current_qualification_policy_authority_v1",
		"assert_current_workflow_input_authority_v1",
		"resolve_qualification_promotion_v2_handoff",
		"assert_pending_qualification_promotion_v2_handoff",
	} {
		if strings.Contains(sessionPostureQuery, forbidden) {
			t.Fatalf("Promotion login allowlist unexpectedly contains %q", forbidden)
		}
	}
	if _, err := inspectSession(context.Background(), unusedQueryer{err: errors.New("scan failed")}, "worksflow"); err == nil {
		t.Fatal("catalog scan error was accepted")
	}
}
