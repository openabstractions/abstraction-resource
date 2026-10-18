package holder

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
)

type testLeases struct {
	mu           sync.Mutex
	acquired     []string
	released     []string
	releaseErr   error
	releaseCalls int
	onRelease    func()
	onAcquire    func()
}

func (f *testLeases) Acquire(resource string, amount int64, wait time.Duration) (res.AcquireResult, error) {
	f.mu.Lock()
	id := "lease-" + string(rune('1'+len(f.acquired)))
	f.acquired = append(f.acquired, id)
	fn := f.onAcquire
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
	return res.AcquireResult{Outcome: res.AcquireOutcomeAcquired, Lease: &res.Lease{ID: id, Resource: resource, Amount: amount}}, nil
}
func (f *testLeases) BindProcess(string, int) (res.ProcessBindResult, error) {
	return res.ProcessBindResult{Outcome: res.ProcessBindOutcomeBound}, nil
}
func (f *testLeases) Renew(string) (res.LeaseChange, error) {
	return res.LeaseChange{Applied: true}, nil
}
func (f *testLeases) Release(id string) (res.LeaseChange, error) {
	f.mu.Lock()
	f.releaseCalls++
	hook := f.onRelease
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return res.LeaseChange{}, f.releaseErr
	}
	f.released = append(f.released, id)
	return res.LeaseChange{Applied: true}, nil
}

func TestUnusedReleaseRetriesAreSerialized(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	leases := &testLeases{onAcquire: cancel, releaseErr: errors.New("temporary")}
	h := newTestHolder(t, leases)
	_ = h.Ensure(ctx, "checkpoint", prepare(&testOwned{alive: true}))
	entered := make(chan struct{})
	continueRelease := make(chan struct{})
	leases.mu.Lock()
	leases.releaseErr = nil
	leases.onRelease = func() { close(entered); <-continueRelease }
	before := leases.releaseCalls
	leases.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { h.retryUnused(); done <- struct{}{} }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("retry did not begin")
	}
	go func() { h.retryUnused(); done <- struct{}{} }()
	time.Sleep(20 * time.Millisecond)
	leases.mu.Lock()
	during := leases.releaseCalls
	leases.mu.Unlock()
	if during != before+1 {
		t.Fatalf("concurrent retries issued %d releases", during-before)
	}
	close(continueRelease)
	<-done
	<-done
	leases.mu.Lock()
	after := leases.releaseCalls
	leases.mu.Unlock()
	if after != before+1 {
		t.Fatalf("released same grant %d times", after-before)
	}
}
func (f *testLeases) Observe(ctx context.Context, _ string, _ time.Duration) (res.YieldPage, error) {
	<-ctx.Done()
	return res.YieldPage{}, ctx.Err()
}
func (f *testLeases) Answer(context.Context, string, res.YieldAnswer, string) (res.AnswerResult, error) {
	return res.AnswerResult{Recorded: true}, nil
}
func (f *testLeases) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acquired), len(f.released)
}

type testOwned struct {
	mu           sync.Mutex
	alive        bool
	stop         bool
	entered      chan struct{}
	continueStop chan struct{}
}

func (o *testOwned) PID() int                    { return 0 }
func (o *testOwned) Ready(context.Context) error { return nil }
func (o *testOwned) Alive() bool                 { o.mu.Lock(); defer o.mu.Unlock(); return o.alive }
func (o *testOwned) Stop() bool {
	if o.entered != nil {
		close(o.entered)
		<-o.continueStop
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stop {
		o.alive = false
	}
	return !o.alive
}
func prepare(owned *testOwned) func() (Component, error) {
	return func() (Component, error) {
		return Component{Amount: 4096, Start: func() (Owned, error) { return owned, nil }}, nil
	}
}
func newTestHolder(t *testing.T, leases *testLeases) *Holder {
	t.Helper()
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAdmissionWaitsForConfirmedYieldAndReloads(t *testing.T) {
	leases := &testLeases{}
	h := newTestHolder(t, leases)
	first := &testOwned{alive: true, stop: true, entered: make(chan struct{}), continueStop: make(chan struct{})}
	if err := h.Ensure(context.Background(), "checkpoint", prepare(first)); err != nil {
		t.Fatal(err)
	}
	lease, _ := h.Snapshot("checkpoint")
	yielded := make(chan res.YieldAnswer, 1)
	go func() {
		answer, _ := h.Yield(res.YieldRequest{Resource: "card:0", Lease: lease, Amount: 4096})
		yielded <- answer
	}()
	select {
	case <-first.entered:
	case <-time.After(time.Second):
		t.Fatal("yield did not reach stop")
	}
	second := &testOwned{alive: true, stop: true}
	prepareEntered := make(chan struct{}, 1)
	ensured := make(chan error, 1)
	go func() {
		ensured <- h.Ensure(context.Background(), "checkpoint", func() (Component, error) {
			prepareEntered <- struct{}{}
			return prepare(second)()
		})
	}()
	select {
	case <-prepareEntered:
		t.Fatal("new admission passed a pending yield")
	case <-time.After(30 * time.Millisecond):
	}
	if got, _ := leases.counts(); got != 1 {
		t.Fatalf("acquired %d leases before stop", got)
	}
	close(first.continueStop)
	select {
	case answer := <-yielded:
		if answer != res.YieldAnswerYielded {
			t.Fatalf("yield %s", answer)
		}
	case <-time.After(time.Second):
		t.Fatal("yield stayed blocked")
	}
	select {
	case err := <-ensured:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("new admission stayed blocked")
	}
	if acquired, released := leases.counts(); acquired != 2 || released != 1 {
		t.Fatalf("acquired=%d released=%d", acquired, released)
	}
	if newLease, _ := h.Snapshot("checkpoint"); newLease == lease {
		t.Fatal("admission reused the released lease")
	}
}

func TestYieldRequiresConfirmedStopAndRelease(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stop       bool
		releaseErr error
	}{
		{name: "stop unconfirmed", stop: false},
		{name: "release failed", stop: true, releaseErr: errors.New("service unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leases := &testLeases{releaseErr: tc.releaseErr}
			h := newTestHolder(t, leases)
			owned := &testOwned{alive: true, stop: tc.stop}
			if err := h.Ensure(context.Background(), "checkpoint", prepare(owned)); err != nil {
				t.Fatal(err)
			}
			lease, _ := h.Snapshot("checkpoint")
			answer, _ := h.Yield(res.YieldRequest{Resource: "card:0", Lease: lease, Amount: 4096})
			if answer != res.YieldAnswerRefused {
				t.Fatalf("answered %s", answer)
			}
			if held, _ := h.Snapshot("checkpoint"); held != lease {
				t.Fatalf("lost lease record: %s", held)
			}
			if _, released := leases.counts(); released != 0 {
				t.Fatalf("released %d", released)
			}
		})
	}
}

func TestCancelledAcquireReleasesGrantWithoutStarting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	leases := &testLeases{onAcquire: cancel}
	h := newTestHolder(t, leases)
	started := false
	err := h.Ensure(ctx, "checkpoint", func() (Component, error) {
		return Component{Amount: 4096, Start: func() (Owned, error) { started = true; return &testOwned{alive: true}, nil }}, nil
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Stage != StageCancel {
		t.Fatalf("failure %v", err)
	}
	if started {
		t.Fatal("started after cancellation")
	}
	if acquired, released := leases.counts(); acquired != 1 || released != 1 {
		t.Fatalf("acquired=%d released=%d", acquired, released)
	}
}

func TestBeginProtectsActiveComponentUntilEnded(t *testing.T) {
	leases := &testLeases{}
	h := newTestHolder(t, leases)
	end, err := h.Begin(context.Background(), "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	owned := &testOwned{alive: true, stop: true}
	if err := h.Ensure(context.Background(), "checkpoint", prepare(owned)); err != nil {
		t.Fatal(err)
	}
	lease, _ := h.Snapshot("checkpoint")
	request := res.YieldRequest{Resource: "card:0", Lease: lease, Amount: 4096}
	if answer, _ := h.Yield(request); answer != res.YieldAnswerRefused {
		t.Fatalf("active yield %s", answer)
	}
	end()
	end()
	if answer, _ := h.Yield(request); answer != res.YieldAnswerYielded {
		t.Fatalf("ended yield %s", answer)
	}
}
