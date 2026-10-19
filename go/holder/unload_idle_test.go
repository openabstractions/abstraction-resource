package holder

import (
	"context"
	"testing"
	"time"
)

// UnloadIdle leaves a component that has a request and unloads it once the
// request ends; a component nothing holds reads neither unloaded nor busy.
func TestUnloadIdleWaitsForTheRequest(t *testing.T) {
	leases := &testLeases{}
	active := true
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour, Active: func(string) bool { return active }})
	if err != nil {
		t.Fatal(err)
	}
	if unloaded, busy := h.UnloadIdle("m", "context changed"); unloaded || busy {
		t.Fatalf("nothing resident: unloaded=%t busy=%t", unloaded, busy)
	}
	if err := h.Ensure(context.Background(), "m", prepare(&testOwned{alive: true, stop: true})); err != nil {
		t.Fatal(err)
	}
	if unloaded, busy := h.UnloadIdle("m", "context changed"); unloaded || !busy {
		t.Fatalf("active request: unloaded=%t busy=%t", unloaded, busy)
	}
	if _, released := leases.counts(); released != 0 {
		t.Fatal("an active component released its lease")
	}
	active = false
	if unloaded, busy := h.UnloadIdle("m", "context changed"); !unloaded || busy {
		t.Fatalf("idle: unloaded=%t busy=%t", unloaded, busy)
	}
	if _, released := leases.counts(); released != 1 {
		t.Fatalf("released %d leases", released)
	}
}
