package gpufault

import (
	"fmt"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/holder"
)

// LeaseWake takes the wake lease through leases@1: an ordinary lease of
// resource awake, renewed every renew until released, during which the
// resource service keeps the platform's wake request (RES-A1). A provider
// uses this and never calls the platform itself. A renewal that fails or
// finds the lease ended takes a new lease at the next tick, so the work
// that is still running is covered again. A zero renew reads 8 s. report
// receives each failure; it may be nil.
func LeaseWake(leases holder.Leases, renew time.Duration, report func(error)) WakeFunc {
	if renew <= 0 {
		renew = 8 * time.Second
	}
	acquire := func(why string) (string, error) {
		result, err := leases.Acquire(res.ResourceAwake, 0, 0)
		if err != nil {
			return "", err
		}
		if result.Outcome != res.AcquireOutcomeAcquired || result.Lease == nil || result.Lease.ID == "" {
			return "", fmt.Errorf("the wake lease for %s: %s", why, result.Outcome)
		}
		return result.Lease.ID, nil
	}
	say := func(err error) {
		if err != nil && report != nil {
			report(err)
		}
	}
	return func(why string) (func(), error) {
		id, err := acquire(why)
		if err != nil {
			return nil, err
		}
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(renew)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
				}
				if id == "" {
					taken, err := acquire(why)
					if err != nil {
						say(fmt.Errorf("taking the wake lease again: %w", err))
						continue
					}
					id = taken
					continue
				}
				change, err := leases.Renew(id)
				if err == nil && !change.Applied {
					err = fmt.Errorf("the wake lease %s ended before the GPU work did", id)
				}
				if err != nil {
					say(err)
					//unchecked: the lease may already be gone; a new one is taken at the next tick
					leases.Release(id)
					id = ""
				}
			}
		}()
		return func() {
			close(stop)
			<-done
			if id == "" {
				return
			}
			if _, err := leases.Release(id); err != nil {
				say(fmt.Errorf("releasing the wake lease %s: %w", id, err))
			}
		}, nil
	}
}
