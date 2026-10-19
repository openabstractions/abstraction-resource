// Package wake ties an application's wake request to the resource service's lease.
package wake

import (
	"context"
	"fmt"
	"sync"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Leases is the generated resource client's wake operations.
type Leases interface {
	AcquireContext(context.Context, string, int64, time.Duration) (res.AcquireResult, error)
	RenewContext(context.Context, string) (res.LeaseChange, error)
	ReleaseContext(context.Context, string) (res.LeaseChange, error)
}

// Holder adapts resource leases to a job's lifetime. The service authenticates
// this process; who and why remain local until the reason-bearing wire arrives.
type Holder struct {
	Client Leases
	// Subject is supplied by a designated enforcer from its authenticated receipt.
	// Nil keeps direct caller admission. The service supplies all authority.
	Subject *rights.Subject
}

func (h Holder) Hold(who, why string) (*lease, error) {
	c := h.Client
	if c == nil {
		c = res.Discover()
	}
	ctx, cancel := context.WithTimeout(context.Background(), res.AcquireBudget)
	var result res.AcquireResult
	var err error
	if h.Subject == nil {
		result, err = c.AcquireContext(ctx, res.ResourceAwake, 0, 0)
	} else if delegated, ok := c.(interface {
		AcquireForContext(context.Context, rights.Subject, string, int64, time.Duration) (res.AcquireResult, error)
	}); ok {
		result, err = delegated.AcquireForContext(ctx, *h.Subject, res.ResourceAwake, 0, 0)
	} else {
		err = fmt.Errorf("resource client cannot acquire for the authenticated owner")
	}
	cancel()
	if err != nil {
		return nil, err
	}
	if result.Outcome != res.AcquireOutcomeAcquired || result.Lease == nil || result.Lease.ID == "" {
		return nil, fmt.Errorf("wake for %s: %s", why, result.Outcome)
	}
	ctx, cancel = context.WithCancel(context.Background())
	l := &lease{client: c, id: result.Lease.ID, cancel: cancel, done: make(chan struct{})}
	go l.follow(ctx, result.Lease.RenewBy)
	return l, nil
}

type lease struct {
	client Leases
	id     string
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	err    error
}

func (l *lease) Done() <-chan struct{} { return l.done }
func (l *lease) Release() error {
	l.once.Do(l.cancel)
	<-l.done
	return l.err
}

func (l *lease) follow(ctx context.Context, renewBy string) {
	defer close(l.done)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), res.AcquireBudget)
		defer cancel()
		_, l.err = l.client.ReleaseContext(cleanup, l.id)
	}()
	for {
		until, err := time.Parse(time.RFC3339Nano, renewBy)
		if err != nil || !until.After(time.Now()) {
			return
		}
		timer := time.NewTimer(time.Until(until) / 3)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		call, cancel := context.WithDeadline(ctx, until)
		change, err := l.client.RenewContext(call, l.id)
		cancel()
		if err != nil || !change.Applied || change.Lease == nil {
			return
		}
		renewBy = change.Lease.RenewBy
	}
}
