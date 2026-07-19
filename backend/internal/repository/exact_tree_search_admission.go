package repository

import (
	"context"
	"errors"
	"time"
)

var (
	ErrExactTreeSearchAdmissionInvalid     = errors.New("invalid exact-tree repository search admission request or configuration")
	ErrExactTreeSearchAdmissionDenied      = errors.New("exact-tree repository search admission denied")
	ErrExactTreeSearchAdmissionUnavailable = errors.New("exact-tree repository search admission infrastructure is unavailable")
)

type ExactTreeSearchAdmissionOperation string

const (
	ExactTreeSearchAdmissionQuery        ExactTreeSearchAdmissionOperation = "query"
	ExactTreeSearchAdmissionFirstBuilder ExactTreeSearchAdmissionOperation = "first-builder"
	maximumExactTreeSearchAdmissionRetry                                   = time.Hour
)

type ExactTreeSearchAdmissionRequest struct {
	ProjectID string
	ActorID   string
	Operation ExactTreeSearchAdmissionOperation
}

// ExactTreeSearchAdmissionDeniedError is a typed, payload-free denial. It
// carries only the fixed operation classification and the bounded delay before
// another admission attempt can be made; project and actor identities never
// enter the error.
type ExactTreeSearchAdmissionDeniedError struct {
	Operation  ExactTreeSearchAdmissionOperation
	RetryAfter time.Duration
}

func (denial *ExactTreeSearchAdmissionDeniedError) Error() string {
	return ErrExactTreeSearchAdmissionDenied.Error()
}

func (denial *ExactTreeSearchAdmissionDeniedError) Unwrap() error {
	return ErrExactTreeSearchAdmissionDenied
}

type ExactTreeSearchAdmission interface {
	Admit(context.Context, ExactTreeSearchAdmissionRequest) error
}
