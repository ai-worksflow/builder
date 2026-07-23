package core

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/canonicalreviewreceipt"
	"gorm.io/gorm"
)

type canonicalReviewReceiptResult struct {
	Hash    string `gorm:"column:receipt_hash"`
	Bytes   []byte `gorm:"column:receipt_bytes"`
	Created bool   `gorm:"column:created"`
}

func issueCanonicalReviewReceipt(
	ctx context.Context,
	transaction *gorm.DB,
	requestID uuid.UUID,
) (canonicalReviewReceiptResult, error) {
	var result canonicalReviewReceiptResult
	err := transaction.WithContext(ctx).Raw(`
SELECT
  (issued.receipt_record).receipt_hash AS receipt_hash,
  (issued.receipt_record).receipt_bytes AS receipt_bytes,
  issued.created AS created
FROM issue_canonical_review_approval_receipt(?) AS issued
`, requestID).Scan(&result).Error
	if err != nil {
		return canonicalReviewReceiptResult{}, fmt.Errorf("issue canonical review approval receipt: %w", err)
	}
	if result.Hash == "" || len(result.Bytes) == 0 {
		return canonicalReviewReceiptResult{}, fmt.Errorf("%w: canonical review receipt was not returned", ErrConflict)
	}
	if _, err := canonicalreviewreceipt.Decode(result.Bytes, result.Hash); err != nil {
		return canonicalReviewReceiptResult{}, fmt.Errorf("verify issued canonical review approval receipt: %w", err)
	}
	return result, nil
}

func canonicalReviewReceiptExists(
	transaction *gorm.DB,
	projectID, revisionID, requestID uuid.UUID,
) (bool, error) {
	var exact bool
	if err := transaction.Raw(
		`SELECT canonical_review_approval_receipt_is_exact(?, ?, ?)`,
		projectID, revisionID, requestID,
	).Scan(&exact).Error; err != nil {
		return false, fmt.Errorf("verify canonical review approval receipt: %w", err)
	}
	return exact, nil
}

func enforceCanonicalReviewReceiptConstraint(
	transaction *gorm.DB,
	projectID, revisionID, requestID uuid.UUID,
) error {
	if err := transaction.Exec(
		`SET CONSTRAINTS canonical_review_approved_requires_receipt IMMEDIATE`,
	).Error; err != nil {
		return fmt.Errorf("enforce canonical review approval receipt constraint: %w", err)
	}
	exact, err := canonicalReviewReceiptExists(transaction, projectID, revisionID, requestID)
	if err != nil {
		return err
	}
	if !exact {
		return fmt.Errorf("%w: canonical review approval receipt is not exact after issuance", ErrConflict)
	}
	return nil
}
