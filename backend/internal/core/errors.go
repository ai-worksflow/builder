package core

import (
	"errors"
	"strings"
)

var (
	ErrNotFound        = errors.New("resource not found")
	ErrForbidden       = errors.New("operation is not permitted")
	ErrConflict        = errors.New("resource changed since it was loaded")
	ErrInvalidInput    = errors.New("input is invalid")
	ErrLastOwner       = errors.New("a project must retain at least one owner")
	ErrSelfApproval    = errors.New("authors cannot approve their own revision")
	ErrBlockingGate    = errors.New("a blocking review gate is not satisfied")
	ErrProposalStale   = errors.New("proposal base is stale")
	ErrContentNotReady = errors.New("content metadata was committed but content finalization is pending")
)

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint")
}
