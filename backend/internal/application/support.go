package application

import (
	"context"
	"time"
)

func serviceNow(clock Clock) time.Time {
	if clock == nil {
		return time.Now().UTC()
	}
	return clock.Now().UTC()
}

func inTransaction(ctx context.Context, manager TransactionManager, operation func(context.Context) error) error {
	if manager == nil {
		return operation(ctx)
	}
	return manager.WithinTransaction(ctx, operation)
}
