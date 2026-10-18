package holder

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// endedLoads hands Ensure one gated component per prepare call and records,
// at each Start, whether every earlier component had already stopped.
type endedLoads struct {
	mu      sync.Mutex
	owned   []*gatedOwned
	overlap []string
}

func (l *endedLoads) prepare() (Component, error) {
	return Component{Amount: 4096, Start: func() (Owned, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		for i, earlier := range l.owned {
			if earlier.Alive() {
				l.overlap = append(l.overlap, fmt.Sprintf("engine %d started while engine %d ran", len(l.owned)+1, i+1))
			}
		}
		o := &gatedOwned{gate: make(chan struct{})}
		l.owned = append(l.owned, o)
		return o, nil
	}}, nil
}

// waitStarted returns the n-th started component once Start has returned it
// to the holder.
func (l *endedLoads) waitStarted(t *testing.T, h *Holder, n int) *gatedOwned {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
		l.mu.Lock()
		var o *gatedOwned
		if len(l.owned) >= n {
			o = l.owned[n-1]
		}
		l.mu.Unlock()
		if o != nil && h.get("video").startedOwned() == Owned(o) {
			return o
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine %d did not start", n)
		}
	}
}

// refuseDuringReadiness has the service refuse the lease of the component
// now readying, lets the holder's renewal see it, and then opens readiness.
func refuseDuringReadiness(t *testing.T, h *Holder, leases *lifetimeLeases, o *gatedOwned, lease string) {
	t.Helper()
	leases.mu.Lock()
	leases.refuse[lease] = true
	leases.mu.Unlock()
	within(t, "renew", time.Second, h.renew)
	if !h.leaseEnded(lease) {
		t.Fatalf("the refused renewal of %s was not recorded", lease)
	}
	close(o.gate)
}

// A lease the service ends while its component readies: Ensure stops that
// engine, releases its lease, and loads once more under a new lease before
// it returns.
func TestEnsureReloadsOnceWhenTheLeaseEndsDuringReadiness(t *testing.T) {
	leases := newLifetimeLeases(time.Hour)
	h := newHolderWith(t, leases)
	loads := &endedLoads{}
	ensured := make(chan error, 1)
	go func() { ensured <- h.Ensure(context.Background(), "video", loads.prepare) }()
	first := loads.waitStarted(t, h, 1)
	refuseDuringReadiness(t, h, leases, first, "lease-1")
	second := loads.waitStarted(t, h, 2)
	close(second.gate)
	select {
	case err := <-ensured:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ensure did not return")
	}
	if lease, loaded := h.Snapshot("video"); !loaded || lease != "lease-2" {
		t.Fatalf("resident %v under %q", loaded, lease)
	}
	if first.Alive() || !second.Alive() {
		t.Fatalf("first alive %v, second alive %v", first.Alive(), second.Alive())
	}
	if _, releases := leases.count("lease-1"); releases != 1 {
		t.Fatalf("the ended lease was released %d times", releases)
	}
	loads.mu.Lock()
	defer loads.mu.Unlock()
	if len(loads.overlap) != 0 || len(loads.owned) != 2 {
		t.Fatalf("starts %d, overlap %v", len(loads.owned), loads.overlap)
	}
}

// A second refusal during the reload's readiness ends Ensure with an acquire
// failure wrapping ErrLeaseEnded, with both engines stopped and both leases
// released.
func TestEnsureFailsWhenTheReloadedLeaseEndsToo(t *testing.T) {
	leases := newLifetimeLeases(time.Hour)
	h := newHolderWith(t, leases)
	loads := &endedLoads{}
	ensured := make(chan error, 1)
	go func() { ensured <- h.Ensure(context.Background(), "video", loads.prepare) }()
	refuseDuringReadiness(t, h, leases, loads.waitStarted(t, h, 1), "lease-1")
	refuseDuringReadiness(t, h, leases, loads.waitStarted(t, h, 2), "lease-2")
	var err error
	select {
	case err = <-ensured:
	case <-time.After(2 * time.Second):
		t.Fatal("Ensure did not return")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Stage != StageAcquire || !errors.Is(err, ErrLeaseEnded) {
		t.Fatalf("Ensure gave %v", err)
	}
	if _, loaded := h.Snapshot("video"); loaded {
		t.Fatal("a component stayed resident after its lease ended twice")
	}
	for _, id := range []string{"lease-1", "lease-2"} {
		if _, releases := leases.count(id); releases != 1 {
			t.Fatalf("%s released %d times", id, releases)
		}
	}
	loads.mu.Lock()
	defer loads.mu.Unlock()
	if len(loads.owned) != 2 || loads.owned[0].Alive() || loads.owned[1].Alive() || len(loads.overlap) != 0 {
		t.Fatalf("starts %d, overlap %v", len(loads.owned), loads.overlap)
	}
}
