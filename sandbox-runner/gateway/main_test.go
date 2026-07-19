package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePortsCanonicalizesBoundedAllowlist(t *testing.T) {
	ports, err := parsePorts("8080,3000")
	if err != nil || !reflect.DeepEqual(ports, []int{3000, 8080}) {
		t.Fatalf("parsePorts = %v, %v", ports, err)
	}
	for _, value := range []string{"", "80", "3000,3000", "3000,not-a-port", strings.Repeat("3000,", maxPorts) + "3000"} {
		if _, err := parsePorts(value); err == nil {
			t.Fatalf("invalid port allowlist %q was accepted", value)
		}
	}
}
