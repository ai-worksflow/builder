package productionpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func safeFacts(kind RoleKind, roleName string) sessionFacts {
	facts := sessionFacts{
		roleCount:                     1,
		roleName:                      roleName,
		sessionRoleName:               roleName,
		canLogin:                      true,
		reachableRoleCount:            2,
		stableRoleCount:               5,
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
		facts.promotionOperatorReachable = true
		facts.reachableHasSchemaUsage = true
		facts.reachableTablePrivilegeCount = 2
		facts.promotionExactTableSelectCount = 2
		facts.reachableColumnPrivilegeCount = 20
		facts.reachableRoutineExecuteCount = 1
		facts.promotionExactRoutineExecuteCount = 1
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
		{"cluster authority", RoleApplication, func(f *sessionFacts) { f.hasClusterAuthority = true }},
		{"reachable authority", RoleApplication, func(f *sessionFacts) { f.reachableHasClusterAuthority = true }},
		{"role admin", RoleApplication, func(f *sessionFacts) { f.hasAdminOption = true }},
		{"missing stable role", RoleApplication, func(f *sessionFacts) { f.stableRoleCount = 4 }},
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
		{"application schema create", RoleApplication, func(f *sessionFacts) { f.reachableCanCreateInSchema = true }},
		{"application relation owner", RoleApplication, func(f *sessionFacts) { f.reachableOwnedRelationCount = 1 }},
		{"migrator app reach", RoleMigrator, func(f *sessionFacts) { f.applicationReachable = true }},
		{"migrator no create", RoleMigrator, func(f *sessionFacts) { f.reachableCanCreateInSchema = false }},
		{"migrator relation owner drift", RoleMigrator, func(f *sessionFacts) { f.migrationOwnedBoundaryCount-- }},
		{"migrator routine owner drift", RoleMigrator, func(f *sessionFacts) { f.migrationOwnedRoutineCount-- }},
		{"auditor app reach", RoleQualification, func(f *sessionFacts) { f.applicationReachable = true; f.reachableRoleCount = 2 }},
		{"auditor fault operator reach", RoleQualification, func(f *sessionFacts) { f.goldenFaultOperatorReachable = true; f.reachableRoleCount = 2 }},
		{"auditor table read", RoleQualification, func(f *sessionFacts) { f.reachableTablePrivilegeCount = 1 }},
		{"auditor column read", RoleQualification, func(f *sessionFacts) { f.reachableColumnPrivilegeCount = 1 }},
		{"auditor sequence use", RoleQualification, func(f *sessionFacts) { f.reachableSequencePrivilegeCount = 1 }},
		{"auditor function execute", RoleQualification, func(f *sessionFacts) { f.reachableRoutineExecuteCount = 1 }},
		{"auditor function owner", RoleQualification, func(f *sessionFacts) { f.reachableOwnedRoutineCount = 1 }},
		{"promotion app reach", RolePromotion, func(f *sessionFacts) { f.applicationReachable = true }},
		{"promotion missing group", RolePromotion, func(f *sessionFacts) { f.promotionOperatorReachable = false }},
		{"promotion extra table", RolePromotion, func(f *sessionFacts) { f.reachableTablePrivilegeCount = 3 }},
		{"promotion missing table", RolePromotion, func(f *sessionFacts) { f.reachableTablePrivilegeCount = 1 }},
		{"promotion substituted table", RolePromotion, func(f *sessionFacts) {
			f.promotionExactTableSelectCount = 1
			f.promotionUnexpectedTablePrivilegeCount = 1
		}},
		{"promotion non-select table privilege", RolePromotion, func(f *sessionFacts) { f.promotionUnexpectedTablePrivilegeCount = 1 }},
		{"promotion column-only third table", RolePromotion, func(f *sessionFacts) { f.promotionUnexpectedColumnPrivilegeCount = 1 }},
		{"promotion sequence", RolePromotion, func(f *sessionFacts) { f.reachableSequencePrivilegeCount = 1 }},
		{"promotion missing routine", RolePromotion, func(f *sessionFacts) { f.reachableRoutineExecuteCount = 0 }},
		{"promotion extra routine", RolePromotion, func(f *sessionFacts) { f.reachableRoutineExecuteCount = 2 }},
		{"promotion substituted routine", RolePromotion, func(f *sessionFacts) {
			f.promotionExactRoutineExecuteCount = 0
			f.promotionUnexpectedRoutineExecuteCount = 1
		}},
		{"promotion owner", RolePromotion, func(f *sessionFacts) { f.reachableOwnedRelationCount = 1 }},
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
		ApplicationDSN:   secureTestDSN("app_login", "app-secret", "db.internal", "5432", "worksflow"),
		MigratorDSN:      secureTestDSN("migrator_login", "migrator-secret", "db.internal", "5432", "worksflow"),
		QualificationDSN: secureTestDSN("qualification_login", "qualification-secret", "db.internal", "5432", "worksflow"),
		PromotionDSN:     secureTestDSN("promotion_login", "promotion-secret", "db.internal", "5432", "worksflow"),
		Schema:           "worksflow",
	}
}

func TestVerifyWithDependenciesReturnsSafeStructuredResult(t *testing.T) {
	configuration := testConfig()
	opened := make([]RoleKind, 0, 4)
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
	if result.Status != StatusPassed || len(result.Roles) != 4 || len(opened) != 4 || closed != 4 || !trustChecked {
		t.Fatalf("unexpected result/open/close state: %#v, %v, %d", result, opened, closed)
	}
	if len(result.ExcludedClaims) != 3 || result.ExcludedClaims[1] != "gc-scheduler-qualification" {
		t.Fatalf("excluded claims are missing: %#v", result.ExcludedClaims)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"app-secret", "migrator-secret", "qualification-secret", "promotion-secret", "db.internal", "postgres://"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("structured result exposed credential material %q: %s", secret, encoded)
		}
	}
}

func TestVerifyWithDependenciesClassifiesFailuresWithoutDriverDetails(t *testing.T) {
	configuration := testConfig()
	driverSecret := errors.New("dial postgres://app_login:do-not-expose@db.internal/worksflow")
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
		{"application posture", func(dependencies *verificationDependencies) {
			dependencies.verifyApplication = func(context.Context, *sql.DB, string) error { return driverSecret }
		}, ErrUnsafePosture, FailureApplicationPostureUnsafe},
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
					}{{RoleApplication, "app_login"}, {RoleMigrator, "migrator_login"}, {RoleQualification, "qualification_login"}, {RolePromotion, "promotion_login"}}
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
		"relation.relnamespace = schema_state.schema_oid",
		"routine.pronamespace = schema_state.schema_oid",
		"pg_catalog.has_column_privilege",
		"pg_catalog.has_function_privilege",
		"promotion_exact_table_select_count",
		"promotion_unexpected_table_privilege_count",
		"promotion_unexpected_column_privilege_count",
		"promotion_exact_routine_execute_count",
		"promotion_unexpected_routine_execute_count",
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
	if _, err := inspectSession(context.Background(), unusedQueryer{err: errors.New("scan failed")}, "worksflow"); err == nil {
		t.Fatal("catalog scan error was accepted")
	}
}
