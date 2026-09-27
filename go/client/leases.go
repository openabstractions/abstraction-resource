package client

import (
	"context"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

// The rights rules a caller of this client needs. Hold is decided on the
// resource asked for; Yield is the service's own rule on an attached holder
// (CONTRACT.md RES-L1, RES-L3).
const (
	ActionHold  = "abstraction.resource/hold"
	ActionYield = "abstraction.resource/yield"
)

// ResourceAwake is the wake hold, a lease like any other (CONTRACT.md RES-A1).
const ResourceAwake = "awake"

// AcquireBudget is what a caller must allow beyond wait_ms for the reply
// itself. An Acquire that waits for holders to yield takes as long as they
// take, and the client's own deadline has to outlast the wait it asked for.
const AcquireBudget = 10 * time.Second

// Acquire asks for amount bytes of resource for this program, waiting up to
// wait for holders to yield. It returns the typed outcome and every holder the
// service asked, whatever the outcome.
func (c *Client) Acquire(resource string, amount int64, wait time.Duration) (AcquireResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait+AcquireBudget)
	defer cancel()
	return c.AcquireContext(ctx, resource, amount, wait)
}

// AcquireContext uses ctx only for this operation. ctx must outlast wait.
func (c *Client) AcquireContext(ctx context.Context, resource string, amount int64, wait time.Duration) (AcquireResult, error) {
	if err := ctx.Err(); err != nil {
		return AcquireResult{}, err
	}
	return c.leases(ctx, wait+AcquireBudget).Acquire(resource, amount, wait.Milliseconds())
}

// Renew extends this program's own lease.
func (c *Client) Renew(lease string) (LeaseChange, error) {
	return c.RenewContext(context.Background(), lease)
}

// RenewContext uses ctx only for this operation.
func (c *Client) RenewContext(ctx context.Context, lease string) (LeaseChange, error) {
	if err := ctx.Err(); err != nil {
		return LeaseChange{}, err
	}
	reply, err := c.leases(ctx, 0).Renew(lease)
	if err != nil {
		return LeaseChange{}, err
	}
	if err := callResultError("Renew", reply.Outcome, reply.Change != nil); err != nil {
		return LeaseChange{}, err
	}
	return *reply.Change, nil
}

// Release ends this program's own lease now.
func (c *Client) Release(lease string) (LeaseChange, error) {
	return c.ReleaseContext(context.Background(), lease)
}

// ReleaseContext uses ctx only for this operation.
func (c *Client) ReleaseContext(ctx context.Context, lease string) (LeaseChange, error) {
	if err := ctx.Err(); err != nil {
		return LeaseChange{}, err
	}
	reply, err := c.leases(ctx, 0).Release(lease)
	if err != nil {
		return LeaseChange{}, err
	}
	if err := callResultError("Release", reply.Outcome, reply.Change != nil); err != nil {
		return LeaseChange{}, err
	}
	return *reply.Change, nil
}

// BindProcess asks the lease book to credit this caller's directly spawned
// process against its own admission reservation. A non-bound outcome keeps
// conservative accounting; it does not revoke the lease.
func (c *Client) BindProcess(lease string, pid int) (ProcessBindResult, error) {
	return c.BindProcessContext(context.Background(), lease, pid)
}

func (c *Client) BindProcessContext(ctx context.Context, lease string, pid int) (ProcessBindResult, error) {
	if err := ctx.Err(); err != nil {
		return ProcessBindResult{}, err
	}
	return c.leases(ctx, 0).BindProcess(lease, int64(pid))
}

// Observe reads the yield requests addressed to this program since cursor,
// waiting up to wait for one.
func (c *Client) Observe(ctx context.Context, cursor string, wait time.Duration) (YieldPage, error) {
	if err := ctx.Err(); err != nil {
		return YieldPage{}, err
	}
	reply, err := c.leases(ctx, wait+AcquireBudget).Observe(cursor, wait.Milliseconds())
	if err != nil {
		return YieldPage{}, err
	}
	if err := callResultError("Observe", reply.Outcome, reply.Page != nil); err != nil {
		return YieldPage{}, err
	}
	return *reply.Page, nil
}

// Answer records this program's answer to one yield request. yielded is
// confirmed against the instrument before the asker is told.
func (c *Client) Answer(ctx context.Context, request string, answer YieldAnswer, reason string) (AnswerResult, error) {
	if err := ctx.Err(); err != nil {
		return AnswerResult{}, err
	}
	reply, err := c.leases(ctx, 0).Answer(request, answer, reason)
	if err != nil {
		return AnswerResult{}, err
	}
	if err := callResultError("Answer", reply.Outcome, reply.Answer != nil); err != nil {
		return AnswerResult{}, err
	}
	return *reply.Answer, nil
}

// leases binds one lease call, widening the transport's own waiting limit to
// the wait the call was given.
func (c *Client) leases(ctx context.Context, budget time.Duration) *wire.LeasesClient {
	transport := c.transport
	if budget > transport.Timeout {
		transport.Timeout = budget
	}
	return wire.NewLeasesClient(transport.WithContext(ctx))
}
