package holder

import (
	"context"
	"testing"
	"time"
)

// Residents answers while another component's load waits for readiness.
func TestResidentsDoesNotWaitForALoadInProgress(t *testing.T) {
	h := newTestHolder(t, &testLeases{})
	if err := h.Ensure(context.Background(), "a", prepare(&testOwned{alive: true, stop: true})); err != nil {
		t.Fatal(err)
	}
	b := &gatedOwned{gate: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- h.Ensure(context.Background(), "b", ownedBy(b)) }()
	defer func() { close(b.gate); <-done }()
	time.Sleep(20 * time.Millisecond)
	var got []string
	within(t, "Residents", 100*time.Millisecond, func() { got = h.Residents() })
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("Residents = %v, want [a]", got)
	}
}
