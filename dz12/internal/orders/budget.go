package orders

import (
	"context"
	"time"
)

type operationBudget struct {
	parent              context.Context
	predecisionDeadline time.Time
	completionDeadline  time.Time
	releaseDeadline     time.Time
}

func newOperationBudget(parent context.Context) operationBudget {
	now := time.Now()
	b := operationBudget{parent: parent, predecisionDeadline: now.Add(20 * time.Second)}
	if outer, ok := parent.Deadline(); ok {
		remaining := max(time.Duration(0), outer.Sub(now))
		preReserve := min(5*time.Second, remaining/3)
		replyReserve := min(time.Second, remaining/6)
		b.predecisionDeadline = minTime(b.predecisionDeadline, outer.Add(-preReserve))
		b.completionDeadline = outer.Add(-replyReserve)
		b.releaseDeadline = outer.Add(-replyReserve / 2)
	}
	return b
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (b operationBudget) predecisionContext() (context.Context, context.CancelFunc) {
	return context.WithDeadline(b.parent, b.predecisionDeadline)
}
func (b operationBudget) completionContext() (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(8 * time.Second)
	if !b.completionDeadline.IsZero() {
		deadline = minTime(deadline, b.completionDeadline)
	}
	return context.WithDeadline(context.WithoutCancel(b.parent), deadline)
}
func (b operationBudget) releaseContext() (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(500 * time.Millisecond)
	if !b.releaseDeadline.IsZero() {
		deadline = minTime(deadline, b.releaseDeadline)
	}
	return context.WithDeadline(context.WithoutCancel(b.parent), deadline)
}
