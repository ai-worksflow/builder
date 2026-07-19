package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const testConnection = "10000000-0000-4000-8000-000000000012"

func connectionGrant(t *testing.T) TicketGrant {
	t.Helper()
	service, store, _, _, input, _ := lspTicketFixture(t, TicketModeEditor)
	if _, err := service.Issue(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	return store.grant
}

func validClientBind(t *testing.T, grant TicketGrant) ClientBind {
	t.Helper()
	modelURI, err := CandidateModelURI(grant.ProjectID, grant.Head.CandidateID, "apps/web/page.tsx")
	if err != nil {
		t.Fatal(err)
	}
	return ClientBind{
		SchemaVersion: BindingSchemaVersion, Kind: "client.bind", ConnectionID: testConnection,
		BindingID: nil, Sequence: 1, Head: grant.Head, Profile: grant.Profiles[0],
		Documents: []DocumentFence{{
			ModelURI: modelURI, OpenID: testOpen, ModelVersion: 1, SavedContentHash: lspDigest("b"),
		}},
	}
}

func TestConnectionHelloAndClientBindCarryExactTicketAuthority(t *testing.T) {
	grant := connectionGrant(t)
	deadline := time.Date(2026, 7, 18, 8, 0, 5, 0, time.UTC)
	hello, err := NewConnectionHello(grant, testConnection, deadline)
	if err != nil {
		t.Fatal(err)
	}
	if hello.SchemaVersion != ConnectionSchemaVersion || hello.Kind != "server.hello" ||
		hello.ConnectionID != testConnection || hello.TicketID != grant.ID || hello.Sequence != 0 ||
		!hello.Head.Equal(grant.Head) || hello.TemplateRelease != grant.TemplateRelease ||
		len(hello.Profiles) != 1 || !equalProfiles(hello.Profiles, grant.Profiles) ||
		hello.Limits != grant.Profiles[0].EffectiveLimits || !hello.BindDeadlineAt.Equal(deadline) {
		t.Fatalf("hello authority drifted: %#v", hello)
	}

	want := validClientBind(t, grant)
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeClientBind(encoded, grant, testConnection)
	if err != nil || got.ConnectionID != testConnection || got.Sequence != 1 ||
		!got.Head.Equal(grant.Head) || !equalProfiles([]ProfileIdentity{got.Profile}, []ProfileIdentity{want.Profile}) ||
		len(got.Documents) != 1 || !got.Documents[0].Equal(want.Documents[0]) {
		t.Fatalf("bind = %#v, %v; json=%s", got, err, encoded)
	}
}

func TestClientBindRejectsRecursiveDriftBeforeRuntimeStart(t *testing.T) {
	grant := connectionGrant(t)
	bind := validClientBind(t, grant)
	encoded, err := json.Marshal(bind)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(encoded)
	otherConnection := "10000000-0000-4000-8000-000000000013"
	for _, invalid := range []string{
		strings.Replace(valid, `"bindingId":null`, `"bindingId":"`+testOpen+`"`, 1),
		strings.Replace(valid, `"sequence":1`, `"sequence":2`, 1),
		strings.Replace(valid, testConnection, otherConnection, 1),
		strings.Replace(valid, `"runtime":{`, `"runtime":{"unknown":true,`, 1),
		strings.Replace(valid, `"runtime":{`, `"runtime":{"image":"duplicate",`, 1),
		strings.Replace(valid, `"documents":[`, `"documents":null,"shadow":[`, 1),
		strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
	} {
		if _, err := DecodeClientBind([]byte(invalid), grant, testConnection); err == nil {
			t.Fatalf("recursive bind drift was accepted: %s", invalid)
		}
	}

	stale := bind
	stale.Head.Version++
	staleJSON, _ := json.Marshal(stale)
	if _, err := DecodeClientBind(staleJSON, grant, testConnection); !errors.Is(err, ErrBindingStale) {
		t.Fatalf("stale head = %v", err)
	}
	foreignProfile := bind
	foreignProfile.Profile.TemplateRelease.ContentHash = lspDigest("9")
	foreignJSON, _ := json.Marshal(foreignProfile)
	if _, err := DecodeClientBind(foreignJSON, grant, testConnection); !errors.Is(err, ErrProfileNotDeclared) {
		t.Fatalf("foreign profile = %v", err)
	}
	foreignDocument := bind
	foreignDocument.Documents[0].OpenID = otherConnection
	foreignDocument.Documents = append(foreignDocument.Documents, foreignDocument.Documents[0])
	foreignDocumentJSON, _ := json.Marshal(foreignDocument)
	if _, err := DecodeClientBind(foreignDocumentJSON, grant, testConnection); !errors.Is(err, ErrBindingStale) {
		t.Fatalf("duplicate document = %v", err)
	}
}

func TestServerBoundFreezesActualIdentityCapabilitiesLimitsAndDocuments(t *testing.T) {
	grant := connectionGrant(t)
	bind := validClientBind(t, grant)
	bindingID := "10000000-0000-4000-8000-000000000014"
	methods := []string{"textDocument/hover"}
	capabilityHash, err := ComputeProductionV1CapabilityHash(methods)
	if err != nil {
		t.Fatal(err)
	}
	expected := ServerBoundExpectation{
		ConnectionID: testConnection, BindingID: bindingID, Head: bind.Head,
		Profile: bind.Profile,
		Initialized: InitializedServer{
			ServerInfo: bind.Profile.ServerInfo, Methods: methods, CapabilityHash: capabilityHash,
		},
		Documents: bind.Documents,
	}
	bound, err := NewServerBound(expected)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(bound)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeServerBound(encoded, expected)
	if err != nil || decoded.SchemaVersion != BindingSchemaVersion || decoded.Kind != "server.bound" ||
		decoded.Sequence != 1 || decoded.ConnectionID != testConnection || decoded.BindingID != bindingID ||
		decoded.LanguageServer.ServerName != bind.Profile.ServerInfo.Name ||
		decoded.LanguageServer.CapabilityAllowlistHash != capabilityHash ||
		decoded.Limits != bind.Profile.EffectiveLimits || !equalDocumentSequences(decoded.Documents, bind.Documents) {
		t.Fatalf("server.bound = %#v, %v\n%s", decoded, err, encoded)
	}

	valid := string(encoded)
	for name, invalid := range map[string]string{
		"unknown top": strings.TrimSuffix(valid, "}") + `,"shadow":true}`,
		"alias":       strings.Replace(valid, `"profileId"`, `"profile_id"`, 1),
		"duplicate nested": strings.Replace(
			valid, `"serverName":`, `"serverName":"duplicate","serverName":`, 1,
		),
		"identity drift":   strings.Replace(valid, bind.Profile.ServerInfo.Name, "other-language-server", 1),
		"capability drift": strings.Replace(valid, capabilityHash, lspDigest("f"), 1),
		"limit drift":      strings.Replace(valid, `"maxConcurrentRequests":8`, `"maxConcurrentRequests":9`, 1),
		"document drift":   strings.Replace(valid, bind.Documents[0].SavedContentHash, lspDigest("e"), 1),
		"float sequence":   strings.Replace(valid, `"sequence":1`, `"sequence":1.0`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeServerBound([]byte(invalid), expected); err == nil {
				t.Fatalf("server.bound drift accepted: %s", invalid)
			}
		})
	}

	drifted := expected
	drifted.Initialized.Methods = []string{"textDocument/definition"}
	drifted.Initialized.CapabilityHash, _ = ComputeProductionV1CapabilityHash(drifted.Initialized.Methods)
	if _, err := NewServerBound(drifted); !errors.Is(err, ErrServerBoundMalformed) {
		t.Fatalf("profile-excluded actual capability = %v", err)
	}
}
