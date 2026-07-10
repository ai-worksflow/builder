package domain

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrConflict          = errors.New("optimistic concurrency conflict")
	ErrNotFound          = errors.New("not found")
	ErrImmutable         = errors.New("immutable value")
	ErrSelfApproval      = errors.New("review author cannot self-approve")
	ErrStaleProposal     = errors.New("proposal base revision is stale")
	ErrValidation        = errors.New("domain validation failed")
	ErrManifestUnpinned  = errors.New("input manifest contains an unpinned reference")
)

// DomainError adds stable, machine-readable context while preserving errors.Is.
type DomainError struct {
	Kind    error
	Field   string
	Message string
}

func (e *DomainError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("%s (%s): %s", e.Kind, e.Field, e.Message)
}

func (e *DomainError) Unwrap() error { return e.Kind }

func invalid(field, message string) error {
	return &DomainError{Kind: ErrInvalidArgument, Field: field, Message: message}
}

func transition(entity, from, to string) error {
	return &DomainError{
		Kind:    ErrInvalidTransition,
		Field:   entity,
		Message: fmt.Sprintf("cannot transition from %q to %q", from, to),
	}
}

func conflict(entity string, expected, actual uint64) error {
	return &DomainError{
		Kind:    ErrConflict,
		Field:   entity,
		Message: fmt.Sprintf("expected version %d, found %d", expected, actual),
	}
}

// ValidationIssue is suitable for displaying several graph/schema failures at once.
type ValidationIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type ValidationError struct {
	Issues []ValidationIssue
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %d issue(s)", ErrValidation, len(e.Issues))
}

func (e *ValidationError) Unwrap() error { return ErrValidation }

func validationError(issues []ValidationIssue) error {
	if len(issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: append([]ValidationIssue(nil), issues...)}
}
