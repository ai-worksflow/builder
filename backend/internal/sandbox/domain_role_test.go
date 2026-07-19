package sandbox

import (
	"errors"
	"testing"
)

func TestSandboxTemplateReleaseRoleInvariant(t *testing.T) {
	t.Run("multiple services in one role share its exact release", func(t *testing.T) {
		input := testSessionInput(cleanCandidate(t))
		input.Services = append(input.Services, AllowedService{
			ID:              "web-admin",
			Kind:            "web",
			Profiles:        []string{"dev"},
			TemplateRelease: input.Services[0].TemplateRelease,
		})

		session, err := NewSession(input, sandboxBaseTime)
		if err != nil {
			t.Fatalf("shared role release was rejected: %v", err)
		}
		view := session.Snapshot()
		if len(view.AllowedServices) != 3 || len(view.TemplateReleases) != 2 {
			t.Fatalf("shared role release was not projected once: services=%#v releases=%#v", view.AllowedServices, view.TemplateReleases)
		}
	})

	tests := []struct {
		name   string
		mutate func(*NewSessionInput)
	}{
		{
			name: "one role cannot select different releases",
			mutate: func(input *NewSessionInput) {
				input.Services[1].Kind = input.Services[0].Kind
			},
		},
		{
			name: "one release cannot span roles",
			mutate: func(input *NewSessionInput) {
				input.Services[1].TemplateRelease = input.Services[0].TemplateRelease
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := testSessionInput(cleanCandidate(t))
			test.mutate(&input)
			if _, err := NewSession(input, sandboxBaseTime); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("invalid role/release mapping was accepted: %v", err)
			}
		})
	}
}
