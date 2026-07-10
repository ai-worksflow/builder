package health

import (
	"context"
	"errors"
	"sort"
	"time"
)

type Check func(context.Context) error

type Result struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

type Report struct {
	Healthy bool     `json:"healthy"`
	Checks  []Result `json:"checks"`
}

type Readiness struct {
	timeout time.Duration
	checks  map[string]Check
}

func NewReadiness(timeout time.Duration, checks map[string]Check) *Readiness {
	cloned := make(map[string]Check, len(checks))
	for name, check := range checks {
		cloned[name] = check
	}
	return &Readiness{timeout: timeout, checks: cloned}
}

func (r *Readiness) Check(ctx context.Context) Report {
	if len(r.checks) == 0 {
		return Report{Healthy: true, Checks: []Result{}}
	}

	type outcome struct {
		name string
		err  error
	}
	checkCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	outcomes := make(chan outcome, len(r.checks))
	for name, check := range r.checks {
		go func() {
			outcomes <- outcome{name: name, err: check(checkCtx)}
		}()
	}

	report := Report{Healthy: true, Checks: make([]Result, 0, len(r.checks))}
	pending := make(map[string]struct{}, len(r.checks))
	for name := range r.checks {
		pending[name] = struct{}{}
	}
	for len(pending) > 0 {
		select {
		case item := <-outcomes:
			if _, expected := pending[item.name]; !expected {
				continue
			}
			delete(pending, item.name)
			result := Result{Name: item.name, Healthy: item.err == nil}
			if item.err != nil {
				report.Healthy = false
				if errors.Is(item.err, context.DeadlineExceeded) {
					result.Error = "check timed out"
				} else {
					result.Error = "check failed"
				}
			}
			report.Checks = append(report.Checks, result)
		case <-checkCtx.Done():
			report.Healthy = false
			for name := range pending {
				report.Checks = append(report.Checks, Result{
					Name:    name,
					Healthy: false,
					Error:   "check timed out",
				})
				delete(pending, name)
			}
		}
	}
	sort.Slice(report.Checks, func(i, j int) bool {
		return report.Checks[i].Name < report.Checks[j].Name
	})
	return report
}
