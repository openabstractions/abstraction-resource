package holder

import (
	"context"
	"slices"
	"testing"

	res "github.com/openabstractions/abstraction-resource/go/client"
)

// unconfirmedOwned is a tree whose exit was seen but whose descendants were
// not confirmed gone: its own Stop reports true.
type unconfirmedOwned struct{ testOwned }

func (o *unconfirmedOwned) ExitConfirmed() bool { return false }

// RES-L4: a component whose exit is unconfirmed keeps its lease record
// through the sweep, a yield, LoseAll and Close, whatever its own Stop
// reported; the holder owns the rule.
func TestAnUnconfirmedExitKeepsTheLease(t *testing.T) {
	leases := &testLeases{}
	h := newTestHolder(t, leases)
	owned := &unconfirmedOwned{testOwned{alive: true, stop: true}}
	if err := h.Ensure(context.Background(), "tree", func() (Component, error) {
		return Component{Amount: 4096, Start: func() (Owned, error) { return owned, nil }}, nil
	}); err != nil {
		t.Fatal(err)
	}
	lease, _ := h.Snapshot("tree")
	owned.mu.Lock()
	owned.alive = false
	owned.mu.Unlock()
	h.sweep()
	if answer, _ := h.Yield(res.YieldRequest{Resource: "card:0", Lease: lease, Amount: 4096}); answer == res.YieldAnswerYielded {
		t.Fatal("an unconfirmed exit yielded")
	}
	if lost := h.LoseAll("reset"); len(lost) != 1 || lost[0].Released {
		t.Fatalf("LoseAll %+v", lost)
	}
	h.Close()
	leases.mu.Lock()
	defer leases.mu.Unlock()
	if slices.Contains(leases.released, lease) {
		t.Fatalf("the lease %s was released: %q", lease, leases.released)
	}
}
