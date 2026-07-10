package logging

import (
	"bytes"
	"strings"
	"testing"

	"github.com/worksflow/builder/backend/internal/config"
)

func TestJSONLoggerHonorsLevel(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(config.LogConfig{Level: "warn", Format: "json"}, &output)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	logger.Info("hidden")
	logger.Warn("visible", "component", "test")
	value := output.String()
	if strings.Contains(value, "hidden") || !strings.Contains(value, `"msg":"visible"`) || !strings.Contains(value, `"component":"test"`) {
		t.Fatalf("logger output = %q", value)
	}
}
