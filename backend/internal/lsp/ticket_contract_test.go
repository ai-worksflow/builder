package lsp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validTicketRequest() TicketRequest {
	return TicketRequest{
		SchemaVersion: TicketRequestSchemaVersion,
		Mode:          TicketModeEditor,
		Head:          validHead(),
		TemplateRelease: ExactTemplateRelease{
			ID: testRelease, ContentHash: lspDigest("2"),
		},
		ProfileIDs: []string{"typescript"},
	}
}

func TestStrictTicketRequestDecoderBindsExactHeadReleaseAndProfiles(t *testing.T) {
	want := validTicketRequest()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTicketRequest(encoded)
	if err != nil || got.SchemaVersion != want.SchemaVersion || got.Mode != want.Mode ||
		!got.Head.Equal(want.Head) || got.TemplateRelease != want.TemplateRelease ||
		len(got.ProfileIDs) != 1 || got.ProfileIDs[0] != "typescript" {
		t.Fatalf("ticket request = %#v, %v", got, err)
	}

	valid := string(encoded)
	for _, invalid := range []string{
		"null",
		strings.Replace(valid, `"schemaVersion":"`+TicketRequestSchemaVersion+`",`, "", 1),
		strings.Replace(valid, `"schemaVersion":"`+TicketRequestSchemaVersion+`"`, `"schemaVersion":null`, 1),
		strings.Replace(valid, `"schemaVersion":"`+TicketRequestSchemaVersion+`"`, `"schemaVersion":"sandbox-lsp-ticket-request/v2"`, 1),
		strings.Replace(valid, `"mode":"editor"`, `"mode":"write"`, 1),
		strings.Replace(valid, `"mode":"editor"`, `"mode":"editor","mode":"snapshot"`, 1),
		strings.Replace(valid, `"sandboxHeadFence":{`, `"sandboxHeadFence":{"candidateVersion":18,`, 1),
		strings.Replace(valid, `"templateRelease":{"id":`, `"templateRelease":{"unknown":true,"id":`, 1),
		strings.Replace(valid, `"profileIds":["typescript"]`, `"profileIds":null`, 1),
		strings.Replace(valid, `"profileIds":["typescript"]`, `"profileIds":["typescript","typescript"]`, 1),
		strings.Replace(valid, `"profileIds":["typescript"]`, `"profileIds":["typescript","go"]`, 1),
		strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
		valid + ` {}`,
	} {
		if _, err := DecodeTicketRequest([]byte(invalid)); !errors.Is(err, ErrTicketInvalid) &&
			!errors.Is(err, ErrInvalidSandboxHead) {
			t.Fatalf("ticket schema drift accepted: %s (%v)", invalid, err)
		}
	}
}
