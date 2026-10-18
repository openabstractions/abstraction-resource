package holder

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
)

// lifetimeLeases is a lease service whose leases end when not renewed within
// lifetime, the way the resource service's DefaultLifetime ends them
// (RES-L4). It records every renewal and release per lease and whether a
// call on one lease overlapped another or a renewal followed a release.
type lifetimeLeases struct {
	mu       sync.Mutex
	lifetime time.Duration
	seq      int
	renewBy  map[string]time.Time
	renews   map[string]int
	releases map[string]int
	refuse   map[string]bool
	inFlight map[string]bool
	acquired []string
	expired  []string
	faults   []string
}

func newLifetimeLeases(lifetime time.Duration) *lifetimeLeases {
	return &lifetimeLeases{lifetime: lifetime, renewBy: map[string]time.Time{}, renews: map[string]int{},
		releases: map[string]int{}, refuse: map[string]bool{}, inFlight: map[string]bool{}}
}

func (f *lifetimeLeases) enter(id, call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inFlight[id] {
		f.faults = append(f.faults, call+" "+id+" overlapped another call on the same lease")
	}
	f.inFlight[id] = true
}

func (f *lifetimeLeases) leave(id string) {
	f.mu.Lock()
	delete(f.inFlight, id)
	f.mu.Unlock()
}

func (f *lifetimeLeases) Acquire(resource string, amount int64, _ time.Duration) (res.AcquireResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("lease-%d", f.seq)
	f.renewBy[id] = time.Now().Add(f.lifetime)
	f.acquired = append(f.acquired, id)
	return res.AcquireResult{Outcome: res.AcquireOutcomeAcquired, Lease: &res.Lease{ID: id, Resource: resource, Amount: amount}}, nil
}

func (f *lifetimeLeases) BindProcess(string, int) (res.ProcessBindResult, error) {
	return res.ProcessBindResult{Outcome: res.ProcessBindOutcomeBound}, nil
}

func (f *lifetimeLeases) Renew(id string) (res.LeaseChange, error) {
	f.enter(id, "renew")
	defer f.leave(id)
	// A call takes a moment, so an overlapping call on the same lease is seen.
	time.Sleep(200 * time.Microsecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews[id]++
	if f.releases[id] > 0 {
		f.faults = append(f.faults, "renew "+id+" after its release")
	}
	by, live := f.renewBy[id]
	if !live || f.refuse[id] {
		return res.LeaseChange{Applied: false}, nil
	}
	if time.Now().After(by) {
		delete(f.renewBy, id)
		f.expired = append(f.expired, id)
		return res.LeaseChange{Applied: false}, nil
	}
	f.renewBy[id] = time.Now().Add(f.lifetime)
	return res.LeaseChange{Applied: true}, nil
}

func (f *lifetimeLeases) Release(id string) (res.LeaseChange, error) {
	f.enter(id, "release")
	defer f.leave(id)
	time.Sleep(200 * time.Microsecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases[id]++
	if f.releases[id] > 1 {
		f.faults = append(f.faults, fmt.Sprintf("release %s %d times", id, f.releases[id]))
	}
	_, live := f.renewBy[id]
	delete(f.renewBy, id)
	return res.LeaseChange{Applied: live}, nil
}

func (f *lifetimeLeases) Observe(ctx context.Context, _ string, _ time.Duration) (res.YieldPage, error) {
	<-ctx.Done()
	return res.YieldPage{}, ctx.Err()
}

func (f *lifetimeLeases) Answer(context.Context, string, res.YieldAnswer, string) (res.AnswerResult, error) {
	return res.AnswerResult{Recorded: true}, nil
}

// live reports whether the service still holds the lease unexpired.
func (f *lifetimeLeases) live(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	by, ok := f.renewBy[id]
	return ok && time.Now().Before(by)
}

func (f *lifetimeLeases) count(id string) (renews, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renews[id], f.releases[id]
}

// gatedOwned is a component whose readiness waits until its gate opens,
// the way a first launch's file proof or a large model load waits.
type gatedOwned struct {
	gate    chan struct{}
	mu      sync.Mutex
	stopped bool
}

func (o *gatedOwned) PID() int { return 0 }
func (o *gatedOwned) Ready(ctx context.Context) error {
	select {
	case <-o.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (o *gatedOwned) Alive() bool { o.mu.Lock(); defer o.mu.Unlock(); return !o.stopped }
func (o *gatedOwned) Stop() bool  { o.mu.Lock(); defer o.mu.Unlock(); o.stopped = true; return true }

func ownedBy(o Owned) func() (Component, error) {
	return func() (Component, error) {
		return Component{Amount: 4096, Start: func() (Owned, error) { return o, nil }}, nil
	}
}

func within(t *testing.T, what string, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s waited for a component lock", what)
	}
}

// A readiness that blocks for several lease lifetimes leaves every other
// resident lease renewed, and its own lease too.
func TestSlowReadinessKeepsEveryLeaseRenewed(t *testing.T) {
	const lifetime = 150 * time.Millisecond
	leases := newLifetimeLeases(lifetime)
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour, Renew: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	chat := &testOwned{alive: true, stop: true}
	if err := h.Ensure(context.Background(), "chat", prepare(chat)); err != nil {
		t.Fatal(err)
	}
	chatLease, _ := h.Snapshot("chat")
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { h.Run(ctx); close(ran) }()
	video := &gatedOwned{gate: make(chan struct{})}
	var open sync.Once
	opened := func() { open.Do(func() { close(video.gate) }) }
	// A failure still opens the gate, so Run can return.
	defer func() { opened(); cancel(); <-ran }()
	ensured := make(chan error, 1)
	go func() { ensured <- h.Ensure(context.Background(), "video", ownedBy(video)) }()
	for deadline := time.Now().Add(time.Second); h.get("video").startedOwned() == nil; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the video load did not start")
		}
	}
	time.Sleep(5 * lifetime)
	leases.mu.Lock()
	videoLease := leases.acquired[1]
	expired := append([]string(nil), leases.expired...)
	leases.mu.Unlock()
	if len(expired) != 0 {
		t.Fatalf("leases expired during a slow readiness: %v", expired)
	}
	if !leases.live(chatLease) || !leases.live(videoLease) {
		t.Fatalf("chat live %v, video live %v", leases.live(chatLease), leases.live(videoLease))
	}
	if !chat.Alive() {
		t.Fatal("the resident chat component was unloaded during another load")
	}
	opened()
	if err := <-ensured; err != nil {
		t.Fatal(err)
	}
	if got := h.Residents(); len(got) != 2 {
		t.Fatalf("residents %v", got)
	}
	if renews, _ := leases.count(chatLease); renews < 5 {
		t.Fatalf("chat renewed %d times", renews)
	}
}

// A sweep skips a component in the middle of Ensure instead of waiting for
// it, and does not idle it; the idle clock starts when the load is ready.
func TestSweepSkipsAComponentInTheMiddleOfEnsure(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }
	leases := newLifetimeLeases(time.Hour)
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Minute, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	video := &gatedOwned{gate: make(chan struct{})}
	ensured := make(chan error, 1)
	go func() { ensured <- h.Ensure(context.Background(), "video", ownedBy(video)) }()
	for deadline := time.Now().Add(time.Second); h.get("video").startedOwned() == nil; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the video load did not start")
		}
	}
	advance(2 * time.Minute)
	within(t, "sweep", time.Second, h.sweep)
	within(t, "renew", time.Second, h.renew)
	if !video.Alive() {
		t.Fatal("a component in the middle of Ensure was idled")
	}
	close(video.gate)
	if err := <-ensured; err != nil {
		t.Fatal(err)
	}
	h.sweep()
	if _, loaded := h.Snapshot("video"); !loaded {
		t.Fatal("a load longer than Idle was idled before its first use")
	}
	advance(2 * time.Minute)
	h.sweep()
	if _, loaded := h.Snapshot("video"); loaded || video.Alive() {
		t.Fatal("an idle component stayed resident")
	}
}

// A renewal the service refuses marks the component; it unloads once its
// lock is free, exactly once, and the lease is not renewed again.
func TestRefusedRenewalUnloadsOnceTheComponentIsFree(t *testing.T) {
	leases := newLifetimeLeases(time.Hour)
	h := newHolderWith(t, leases)
	video := &testOwned{alive: true, stop: true}
	if err := h.Ensure(context.Background(), "video", prepare(video)); err != nil {
		t.Fatal(err)
	}
	lease, _ := h.Snapshot("video")
	leases.mu.Lock()
	leases.refuse[lease] = true
	leases.mu.Unlock()
	s := h.get("video")
	s.mu.Lock() // a load, yield or unload of this component is under way
	within(t, "renew", time.Second, h.renew)
	within(t, "renew", time.Second, h.renew)
	within(t, "sweep", time.Second, h.sweep)
	if renews, releases := leases.count(lease); renews != 1 || releases != 0 || !video.Alive() {
		t.Fatalf("while busy: renews %d releases %d alive %v", renews, releases, video.Alive())
	}
	s.mu.Unlock()
	h.sweep()
	if _, loaded := h.Snapshot("video"); loaded || video.Alive() {
		t.Fatal("a component whose lease ended stayed resident")
	}
	h.sweep()
	h.renew()
	if renews, releases := leases.count(lease); renews != 1 || releases != 1 {
		t.Fatalf("after unload: renews %d releases %d", renews, releases)
	}
}

// Ensure on a resident whose lease ended loads it again under a new lease.
func TestEnsureReloadsAComponentWhoseLeaseEnded(t *testing.T) {
	leases := newLifetimeLeases(time.Hour)
	h := newHolderWith(t, leases)
	first := &testOwned{alive: true, stop: true}
	if err := h.Ensure(context.Background(), "video", prepare(first)); err != nil {
		t.Fatal(err)
	}
	lease, _ := h.Snapshot("video")
	leases.mu.Lock()
	leases.refuse[lease] = true
	leases.mu.Unlock()
	h.renew()
	second := &testOwned{alive: true, stop: true}
	if err := h.Ensure(context.Background(), "video", prepare(second)); err != nil {
		t.Fatal(err)
	}
	if now, _ := h.Snapshot("video"); now == lease || first.Alive() {
		t.Fatalf("lease %s, first alive %v", now, first.Alive())
	}
	if _, releases := leases.count(lease); releases != 1 {
		t.Fatalf("released the ended lease %d times", releases)
	}
}

// Under concurrent loads, unloads, yields, idling and renewals, no lease is
// renewed and released at once, renewed after its release, or released twice.
// Run with -race.
func TestRenewalAndReleaseNeverOverlap(t *testing.T) {
	leases := newLifetimeLeases(time.Hour)
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: 3 * time.Millisecond, Renew: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { h.Run(ctx); close(ran) }()
	stop := time.Now().Add(400 * time.Millisecond)
	var work sync.WaitGroup
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("component-%d", i%2)
		work.Add(1)
		go func(i int) {
			defer work.Done()
			for n := 0; time.Now().Before(stop); n++ {
				//unchecked: a concurrent unload or yield may refuse this load; the service's record is what is checked
				h.Ensure(context.Background(), name, prepare(&testOwned{alive: true, stop: true}))
				switch (i + n) % 3 {
				case 0:
					h.Unload(name, "test")
				case 1:
					if lease, ok := h.Snapshot(name); ok {
						h.Yield(res.YieldRequest{Resource: "card:0", Lease: lease, Amount: 4096})
					}
				}
			}
		}(i)
	}
	work.Add(1)
	go func() {
		defer work.Done()
		for time.Now().Before(stop) {
			h.renew()
		}
	}()
	work.Wait()
	cancel()
	<-ran
	h.Close()
	leases.mu.Lock()
	defer leases.mu.Unlock()
	if len(leases.faults) != 0 {
		t.Fatalf("lease faults: %v", leases.faults)
	}
	for _, id := range leases.acquired {
		if leases.releases[id] != 1 {
			t.Fatalf("lease %s released %d times", id, leases.releases[id])
		}
	}
	if len(leases.acquired) < 4 {
		t.Fatalf("only %d loads ran", len(leases.acquired))
	}
}

func newHolderWith(t *testing.T, leases Leases) *Holder {
	t.Helper()
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
