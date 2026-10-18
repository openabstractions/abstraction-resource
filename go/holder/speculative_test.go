package holder

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	res "github.com/openabstractions/abstraction-resource/go/client"
)

// freeLeases is the test book with the no-yield acquire (RES-L6): it answers
// outcome and counts its calls apart from Acquire's.
type freeLeases struct {
	*testLeases
	mu      sync.Mutex
	outcome res.AcquireFreeOutcome
	frees   int
}

func (f *freeLeases) AcquireFree(resource string, amount int64) (res.AcquireFreeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frees++
	if f.outcome != res.AcquireFreeOutcomeAcquired {
		return res.AcquireFreeResult{Outcome: f.outcome, Asked: []res.YieldRecord{}}, nil
	}
	return res.AcquireFreeResult{Outcome: res.AcquireFreeOutcomeAcquired, Lease: &res.Lease{ID: "free-1", Resource: resource, Amount: amount},
		Asked: []res.YieldRecord{}}, nil
}

func (f *freeLeases) freeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frees
}

func speculativeHolder(t *testing.T, leases Leases, now func() time.Time) *Holder {
	t.Helper()
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

// RES-L6: a speculative ensure of a resident component leaves its idle
// clock alone. A component used at t0 and ensured speculatively at
// t0+Idle-1s is still unloaded as idle at t0+Idle.
func TestASpeculativeEnsureLeavesTheIdleClockAlone(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
	leases := &freeLeases{testLeases: &testLeases{}, outcome: res.AcquireFreeOutcomeAcquired}
	h := speculativeHolder(t, leases, now)
	owned := &testOwned{alive: true, stop: true}
	if err := h.Ensure(context.Background(), "m", prepare(owned)); err != nil {
		t.Fatal(err)
	}
	advance(time.Hour - time.Second)
	if err := h.EnsureSpeculative(context.Background(), "m", prepare(&testOwned{alive: true})); err != nil {
		t.Fatal(err)
	}
	if leases.freeCalls() != 0 {
		t.Fatal("a resident component was acquired again")
	}
	h.sweep()
	if _, loaded := h.Snapshot("m"); !loaded {
		t.Fatal("unloaded before its idle time")
	}
	advance(time.Second)
	h.sweep()
	if _, loaded := h.Snapshot("m"); loaded {
		t.Fatal("the speculative ensure kept the component past its idle time")
	}
}

// RES-L6: a speculative ensure of a missing component acquires only through
// the no-yield acquire. A held resource refuses lease:held; a component
// evicted before the ensure is the same missing component; a failed
// prepare refuses; and a book without the no-yield acquire refuses. None
// falls back to Acquire, which asks holders to yield.
func TestASpeculativeEnsureNeverAsksAHolder(t *testing.T) {
	leases := &freeLeases{testLeases: &testLeases{}, outcome: res.AcquireFreeOutcomeHeld}
	h := speculativeHolder(t, leases, nil)
	err := h.EnsureSpeculative(context.Background(), "m", prepare(&testOwned{alive: true}))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Word() != WordHeld || failure.FreeResult == nil || failure.FreeResult.Outcome != res.AcquireFreeOutcomeHeld {
		t.Fatalf("a held resource: %v", err)
	}
	// Evicted between residency and the ensure.
	leases.outcome = res.AcquireFreeOutcomeAcquired
	if err := h.Ensure(context.Background(), "m", prepare(&testOwned{alive: true, stop: true})); err != nil {
		t.Fatal(err)
	}
	h.Unload("m", "evicted")
	leases.mu.Lock()
	leases.outcome = res.AcquireFreeOutcomeHeld
	leases.mu.Unlock()
	if err := h.EnsureSpeculative(context.Background(), "m", prepare(&testOwned{alive: true})); !errors.As(err, &failure) || failure.Word() != WordHeld {
		t.Fatalf("an evicted component: %v", err)
	}
	failing := func() (Component, error) { return Component{}, errors.New("no such object") }
	if err := h.EnsureSpeculative(context.Background(), "other", failing); err == nil || err.Error() != "no such object" {
		t.Fatalf("a failed prepare: %v", err)
	}
	if acquires, _ := leases.counts(); acquires != 1 {
		t.Fatalf("Acquire was called %d times, want the one plain Ensure", acquires)
	}
	if leases.freeCalls() != 2 {
		t.Fatalf("the no-yield acquire was called %d times, want 2", leases.freeCalls())
	}
	plain := speculativeHolder(t, &testLeases{}, nil)
	if err := plain.EnsureSpeculative(context.Background(), "m", prepare(&testOwned{alive: true})); !errors.As(err, &failure) || failure.Word() != WordHeld {
		t.Fatalf("a book with no no-yield acquire: %v", err)
	}
}

// slowOwned readies only when its context ends, as an engine still reading
// its weights.
type slowOwned struct {
	testOwned
	readying chan struct{}
}

func (o *slowOwned) Ready(ctx context.Context) error {
	close(o.readying)
	<-ctx.Done()
	return ctx.Err()
}

// RES-L6: a yield request for the lease of a speculative load under way
// cancels the load at once and is answered yielded once the component has
// stopped and its lease is released; the load refuses lease:held.
func TestAYieldCancelsASpeculativeLoad(t *testing.T) {
	leases := &freeLeases{testLeases: &testLeases{}, outcome: res.AcquireFreeOutcomeAcquired}
	h := speculativeHolder(t, leases, nil)
	owned := &slowOwned{testOwned: testOwned{alive: true, stop: true}, readying: make(chan struct{})}
	loaded := make(chan error, 1)
	go func() {
		loaded <- h.EnsureSpeculative(context.Background(), "m", func() (Component, error) {
			return Component{Amount: 4096, Start: func() (Owned, error) { return owned, nil }}, nil
		})
	}()
	<-owned.readying
	began := time.Now()
	answer, reason := h.Yield(res.YieldRequest{ID: "ask-1", Resource: "card:0", Amount: 4096, Lease: "free-1"})
	if answer != res.YieldAnswerYielded {
		t.Fatalf("the yield was answered %s: %s", answer, reason)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("the yield took %s", took)
	}
	var failure *Failure
	if err := <-loaded; !errors.As(err, &failure) || failure.Word() != WordHeld || !errors.Is(err, ErrSpeculativeCancelled) {
		t.Fatalf("the cancelled load returned %v", err)
	}
	if _, released := leases.counts(); released != 1 {
		t.Fatalf("released %d leases, want the speculative one", released)
	}
	if owned.Alive() {
		t.Fatal("the cancelled load left its component running")
	}
}

// joinedOwned readies when release closes or its context ends, and records
// whether its context was cancelled.
type joinedOwned struct {
	testOwned
	readying  chan struct{}
	release   chan struct{}
	cancelled chan struct{}
}

func (o *joinedOwned) Ready(ctx context.Context) error {
	close(o.readying)
	select {
	case <-o.release:
		return nil
	case <-ctx.Done():
		close(o.cancelled)
		return ctx.Err()
	}
}

// RES-L6 (review Y1): once real work waits on the component a speculative
// load is bringing up, the load is that work's own and holds a normal
// standing: a yield request does not cancel it, and is refused while the
// work is active, as for any holder.
func TestASpeculativeLoadRealWorkWaitsOnIsNotCancelled(t *testing.T) {
	leases := &freeLeases{testLeases: &testLeases{}, outcome: res.AcquireFreeOutcomeAcquired}
	var active sync.Map
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour,
		Active: func(name string) bool { _, ok := active.Load(name); return ok }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	owned := &joinedOwned{testOwned: testOwned{alive: true}, readying: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	speculative := make(chan error, 1)
	go func() {
		speculative <- h.EnsureSpeculative(context.Background(), "m", func() (Component, error) {
			return Component{Amount: 4096, Start: func() (Owned, error) { return owned, nil }}, nil
		})
	}()
	<-owned.readying
	// A real request: its activity is reserved, then it waits in Ensure.
	active.Store("m", true)
	plain := make(chan error, 1)
	go func() { plain <- h.Ensure(context.Background(), "m", prepare(&testOwned{alive: true})) }()
	s := h.get("m")
	for deadline := time.Now().Add(5 * time.Second); s.waiting.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the plain Ensure never waited")
		}
		time.Sleep(time.Millisecond)
	}
	answered := make(chan res.YieldAnswer, 1)
	go func() {
		answer, _ := h.Yield(res.YieldRequest{ID: "ask-1", Resource: "card:0", Amount: 4096, Lease: "free-1"})
		answered <- answer
	}()
	select {
	case <-owned.cancelled:
		t.Fatal("the yield cancelled a load real work waits on")
	case <-time.After(100 * time.Millisecond):
	}
	close(owned.release)
	if err := <-speculative; err != nil {
		t.Fatalf("the speculative load: %v", err)
	}
	if err := <-plain; err != nil {
		t.Fatalf("the plain Ensure: %v", err)
	}
	if answer := <-answered; answer != res.YieldAnswerRefused {
		t.Fatalf("the yield of an active component was answered %s", answer)
	}
	if _, loaded := h.Snapshot("m"); !loaded {
		t.Fatal("the component real work waited on was unloaded")
	}
}

// oldLeasesTransport is a resource service from before RES-L6: it serves
// leases@1 without AcquireFree, so a call of it is answered unknown_method
// through the generated dispatcher.
type oldLeasesTransport struct{}

func (oldLeasesTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	renamed := bytes.ReplaceAll(frame, []byte(`"AcquireFree"`), []byte(`"Unlisted"`))
	reply, err := wire.ServeEndpoint(renamed, "test", "", &wire.LeasesDispatcher{})
	return bytes.ReplaceAll(reply, []byte(`"Unlisted"`), []byte(`"AcquireFree"`)), err
}

// oldBook is the holder's lease service over that old transport.
type oldBook struct{ *testLeases }

func (oldBook) AcquireFree(resource string, amount int64) (res.AcquireFreeResult, error) {
	return wire.NewLeasesClient(oldLeasesTransport{}).AcquireFree(resource, amount)
}

// RES-L6 (review Y4): against a resource service without the no-yield
// acquire, a speculative load refuses lease:held, so a preload of a model
// not yet loaded answers resources:held, and nothing falls back to Acquire.
func TestASpeculativeLoadAgainstAnOlderServiceIsHeld(t *testing.T) {
	_, err := wire.NewLeasesClient(oldLeasesTransport{}).AcquireFree("card:0", 4096)
	var refused *res.ServiceError
	if !errors.As(err, &refused) || refused.Code != res.ServiceErrorCodeUnknownMethod {
		t.Fatalf("the old service answered %v, want unknown_method", err)
	}
	book := oldBook{&testLeases{}}
	h := speculativeHolder(t, book, nil)
	err = h.EnsureSpeculative(context.Background(), "m", prepare(&testOwned{alive: true}))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Word() != WordHeld || !errors.Is(err, ErrNoFreeAcquire) {
		t.Fatalf("a speculative load against an old service: %v", err)
	}
	if acquires, _ := book.counts(); acquires != 0 {
		t.Fatalf("Acquire was called %d times", acquires)
	}
}
