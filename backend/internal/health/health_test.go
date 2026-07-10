package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReadinessReportsSortedResults(t *testing.T) {
	readiness := NewReadiness(time.Second, map[string]Check{
		"redis": func(context.Context) error { return errors.New("unavailable") },
		"mongo": func(context.Context) error { return nil },
	})
	report := readiness.Check(context.Background())
	if report.Healthy {
		t.Fatal("report.Healthy = true, want false")
	}
	if len(report.Checks) != 2 || report.Checks[0].Name != "mongo" || report.Checks[1].Name != "redis" {
		t.Fatalf("checks = %#v, want sorted results", report.Checks)
	}
	if report.Checks[1].Error != "check failed" {
		t.Fatalf("failure detail = %q, want safe generic detail", report.Checks[1].Error)
	}
}

func TestReadinessBoundsEachCheck(t *testing.T) {
	readiness := NewReadiness(10*time.Millisecond, map[string]Check{
		"slow": func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	report := readiness.Check(context.Background())
	if report.Healthy || report.Checks[0].Error != "check timed out" {
		t.Fatalf("report = %#v, want timed out failure", report)
	}
}

func TestReadinessDoesNotWaitForCheckThatIgnoresContext(t *testing.T) {
	readiness := NewReadiness(5*time.Millisecond, map[string]Check{
		"stuck": func(context.Context) error {
			time.Sleep(100 * time.Millisecond)
			return nil
		},
	})
	startedAt := time.Now()
	report := readiness.Check(context.Background())
	if elapsed := time.Since(startedAt); elapsed > 50*time.Millisecond {
		t.Fatalf("Check() elapsed = %s, want bounded readiness", elapsed)
	}
	if report.Healthy || report.Checks[0].Error != "check timed out" {
		t.Fatalf("report = %#v, want timed out failure", report)
	}
}
