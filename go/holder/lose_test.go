package holder

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// loadingOwned is an engine whose readiness waits until it is stopped, the
// way a load in progress meets a driver reset.
type loadingOwned struct {
	once    sync.Once
	stopped chan struct{}
}

func (o *loadingOwned) PID() int { return 0 }
func (o *loadingOwned) Ready(ctx context.Context) error {
	select {
	case <-o.stopped:
		return errors.New("the engine exited before it was ready")
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (o *loadingOwned) Alive() bool {
	select {
	case <-o.stopped:
		return false
	default:
		return true
	}
}
func (o *loadingOwned) Stop() bool { o.once.Do(func() { close(o.stopped) }); return true }

// RES-G2: LoseAll stops every resident component by its own Stop, releases
// its lease, and says whether a call was using it.
func TestLoseAllStopsResidentsAndReleasesTheirLeases(t *testing.T) {
	leases := &testLeases{}
	h := newTestHolder(t, leases)
	busy := &testOwned{alive: true, stop: true}
	idle := &testOwned{alive: true, stop: true}
	if err := h.Ensure(context.Background(), "video", prepare(busy)); err != nil {
		t.Fatal(err)
	}
	if err := h.Ensure(context.Background(), "chat", prepare(idle)); err != nil {
		t.Fatal(err)
	}
	end, err := h.Begin(context.Background(), "video")
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	lost := h.LoseAll("engine:gpu-reset")
	if len(lost) != 2 || lost[0] != (Lost{Name: "chat", Released: true}) || lost[1] != (Lost{Name: "video", Active: true, Released: true}) {
		t.Fatalf("lost %+v", lost)
	}
	if busy.Alive() || idle.Alive() || len(h.Residents()) != 0 {
		t.Fatalf("components still resident: %v", h.Residents())
	}
	if acquired, released := leases.counts(); acquired != 2 || released != 2 {
		t.Fatalf("acquired %d released %d", acquired, released)
	}
	if again := h.LoseAll("engine:gpu-reset"); len(again) != 0 {
		t.Fatalf("a second LoseAll found %+v", again)
	}
}

// RES-G2: a component still waiting for readiness is stopped too, without
// waiting for its load to give up on its own.
func TestLoseAllEndsALoadInProgress(t *testing.T) {
	leases := &testLeases{}
	h := newTestHolder(t, leases)
	owned := &loadingOwned{stopped: make(chan struct{})}
	ensured := make(chan error, 1)
	go func() {
		ensured <- h.Ensure(context.Background(), "video", func() (Component, error) {
			return Component{Amount: 4096, Start: func() (Owned, error) { return owned, nil }}, nil
		})
	}()
	deadline := time.Now().Add(time.Second)
	for h.get("video").startedOwned() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the load did not start")
		}
		time.Sleep(time.Millisecond)
	}
	lost := h.LoseAll("engine:gpu-reset")
	if len(lost) != 1 || lost[0].Name != "video" || !lost[0].Released {
		t.Fatalf("lost %+v", lost)
	}
	var failure *Failure
	select {
	case err := <-ensured:
		if !errors.As(err, &failure) || failure.Stage != StageReady {
			t.Fatalf("the load ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the load in progress did not end")
	}
	if _, released := leases.counts(); released != 1 {
		t.Fatalf("released %d", released)
	}
}

// RES-G4: a reset that begins while the load waits for its lease refuses the
// start; the grant goes back unused and nothing starts.
func TestAResetDuringTheLeaseWaitReleasesTheUnusedGrant(t *testing.T) {
	var mu sync.Mutex
	resetting := false
	refuse := errors.New("gpu:resetting")
	leases := &testLeases{onAcquire: func() { mu.Lock(); resetting = true; mu.Unlock() }}
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour, Admit: func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		if resetting {
			return refuse
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	started := false
	err = h.Ensure(context.Background(), "video", func() (Component, error) {
		return Component{Amount: 4096, Start: func() (Owned, error) { started = true; return &testOwned{alive: true}, nil }}, nil
	})
	if !errors.Is(err, refuse) {
		t.Fatalf("ensure gave %v", err)
	}
	if started {
		t.Fatal("the engine started after a reset began")
	}
	if acquired, released := leases.counts(); acquired != 1 || released != 1 {
		t.Fatalf("acquired %d released %d", acquired, released)
	}
}

// RES-G4, RES-G5: Admit is asked before a new load, and a refusal asks for no
// lease; a resident component is not asked again.
func TestAdmitRefusesALoadBeforeAnyLease(t *testing.T) {
	leases := &testLeases{}
	refuse := errors.New("gpu:standby")
	var asked int
	var mu sync.Mutex
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour, Admit: func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		asked++
		return refuse
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Ensure(context.Background(), "video", prepare(&testOwned{alive: true})); !errors.Is(err, refuse) {
		t.Fatalf("ensure gave %v", err)
	}
	if acquired, _ := leases.counts(); acquired != 0 {
		t.Fatalf("a refused load asked for %d leases", acquired)
	}
	h.opts.Admit = nil
	if err := h.Ensure(context.Background(), "video", prepare(&testOwned{alive: true})); err != nil {
		t.Fatal(err)
	}
	h.opts.Admit = func(context.Context, string) error { mu.Lock(); defer mu.Unlock(); asked++; return refuse }
	if err := h.Ensure(context.Background(), "video", prepare(&testOwned{alive: true})); err != nil {
		t.Fatalf("a resident component was refused: %v", err)
	}
	if asked != 1 {
		t.Fatalf("admit asked %d times", asked)
	}
}
