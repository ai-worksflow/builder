package collaboration

import (
	"testing"

	"github.com/worksflow/builder/backend/internal/core"
)

func TestDownstreamMachineContractScaffoldsAreStructurallyValid(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"api_contract", "data_contract", "permission_contract"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			contentKind, ok := downstreamDocumentContentKind(kind)
			if !ok {
				t.Fatalf("missing downstream kind %s", kind)
			}
			payload, err := downstreamDocumentScaffold(kind, contentKind, core.VersionRef{})
			if err != nil {
				t.Fatal(err)
			}
			if report := core.ValidateArtifactContent(kind, payload); !report.Valid {
				t.Fatalf("invalid %s scaffold: %#v", kind, report.Findings)
			}
		})
	}
}
