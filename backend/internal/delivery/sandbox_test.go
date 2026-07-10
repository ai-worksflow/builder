package delivery

import "testing"

func TestValidateContainerDaemonHost(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "unix:///var/run/docker.sock", "tcp://sandbox:2375", "tcp://[::1]:2375"} {
		if actual, err := validateContainerDaemonHost(value); err != nil || actual != value {
			t.Fatalf("validateContainerDaemonHost(%q) = %q, %v", value, actual, err)
		}
	}
	for _, value := range []string{"http://sandbox:2375", "tcp://sandbox", "tcp://user@sandbox:2375", "unix://relative.sock", "unix:///tmp/../docker.sock"} {
		if _, err := validateContainerDaemonHost(value); err == nil {
			t.Fatalf("validateContainerDaemonHost(%q) unexpectedly succeeded", value)
		}
	}
}

func TestSandboxClientEnvironmentIsAllowlisted(t *testing.T) {
	t.Parallel()
	environment := sandboxClientEnvironment("/tmp/config", "tcp://sandbox:2375")
	want := map[string]bool{
		"DOCKER_CONFIG=/tmp/config":      true,
		"HOME=/tmp/config":               true,
		"DOCKER_HOST=tcp://sandbox:2375": true,
	}
	for _, entry := range environment {
		delete(want, entry)
	}
	if len(want) != 0 {
		t.Fatalf("sandbox environment is missing values: %#v; got %#v", want, environment)
	}
}
