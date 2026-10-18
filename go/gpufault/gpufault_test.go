package gpufault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/holder"
)

type fakeLeases struct {
	mu       sync.Mutex
	next     int
	live     map[string]string
	released []string
	renewed  int
}

func newFakeLeases() *fakeLeases { return &fakeLeases{live: map[string]string{}} }

func (f *fakeLeases) Acquire(resource string, amount int64, _ time.Duration) (res.AcquireResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("lease-%d", f.next)
	f.live[id] = resource
	return res.AcquireResult{Outcome: res.AcquireOutcomeAcquired, Lease: &res.Lease{ID: id, Resource: resource, Amount: amount}}, nil
}
func (f *fakeLeases) BindProcess(string, int) (res.ProcessBindResult, error) {
	return res.ProcessBindResult{Outcome: res.ProcessBindOutcomeUnverifiable}, nil
}
func (f *fakeLeases) Renew(id string) (res.LeaseChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewed++
	_, ok := f.live[id]
	return res.LeaseChange{Applied: ok}, nil
}
func (f *fakeLeases) Release(id string) (res.LeaseChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.live[id]
	delete(f.live, id)
	f.released = append(f.released, id)
	return res.LeaseChange{Applied: ok}, nil
}
func (f *fakeLeases) Observe(ctx context.Context, _ string, _ time.Duration) (res.YieldPage, error) {
	<-ctx.Done()
	return res.YieldPage{}, ctx.Err()
}
func (f *fakeLeases) Answer(context.Context, string, res.YieldAnswer, string) (res.AnswerResult, error) {
	return res.AnswerResult{Recorded: true}, nil
}
func (f *fakeLeases) liveOn(resource string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.live {
		if r == resource {
			n++
		}
	}
	return n
}

// engine stands in for an engine process: Stop ends it, and Exited closes
// when it is gone, as a process handle's wait does.
type engine struct {
	once      sync.Once
	exited    chan struct{}
	at        time.Time
	stopAfter <-chan struct{}
}

func newEngine() *engine                      { return &engine{exited: make(chan struct{})} }
func (e *engine) PID() int                    { return 0 }
func (e *engine) Ready(context.Context) error { return nil }
func (e *engine) Alive() bool {
	select {
	case <-e.exited:
		return false
	default:
		return true
	}
}
func (e *engine) Stop() bool {
	e.once.Do(func() {
		if e.stopAfter != nil {
			<-e.stopAfter
		}
		e.at = time.Now()
		close(e.exited)
	})
	return true
}

// chanSource is a fake platform: what is sent on events is delivered.
type chanSource struct {
	events  chan Event
	handled chan Event
}

func (s chanSource) Watch(ctx context.Context, deliver func(Event)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-s.events:
			deliver(ev)
			select {
			case s.handled <- ev:
			default:
			}
		}
	}
}

type rig struct {
	guard   *Guard
	holder  *holder.Holder
	leases  *fakeLeases
	source  chanSource
	probe   func() error
	faults  chan Fault
	cancel  context.CancelFunc
	probeMu sync.Mutex
}

func newRig(t *testing.T, opts Options) *rig {
	t.Helper()
	r := &rig{leases: newFakeLeases(), source: chanSource{events: make(chan Event), handled: make(chan Event, 1)}, faults: make(chan Fault, 4)}
	r.probe = func() error { return nil }
	opts.Records = r.source
	opts.Probe = func(context.Context) error { r.probeMu.Lock(); defer r.probeMu.Unlock(); return r.probe() }
	opts.OnFault = func(f Fault) { r.faults <- f }
	if opts.Settle == 0 {
		opts.Settle = 50 * time.Millisecond
	}
	if opts.Window == 0 {
		opts.Window = 200 * time.Millisecond
	}
	g, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	h, err := holder.New(holder.Options{Leases: r.leases, Resource: "card:0", Idle: time.Hour,
		Admit: func(ctx context.Context, _ string) error { return g.AdmitLoad(ctx) }})
	if err != nil {
		t.Fatal(err)
	}
	g.Attach(h)
	r.guard, r.holder = g, h
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go g.Run(ctx)
	t.Cleanup(cancel)
	return r
}

func (r *rig) setProbe(fn func() error) {
	r.probeMu.Lock()
	r.probe = fn
	r.probeMu.Unlock()
}

func (r *rig) load(name string) (*engine, error) {
	e := newEngine()
	err := r.holder.Ensure(context.Background(), name, func() (holder.Component, error) {
		return holder.Component{Amount: 1 << 30, Start: func() (holder.Owned, error) { return e, nil }}, nil
	})
	return e, err
}

func wordOf(err error) string {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Word
	}
	return ""
}

// RES-G1..RES-G4 and RES-G6 with stand-ins: a reset record stops every
// resident engine and releases its lease, the request that was running fails
// with gpu:reset and its fingerprint is quarantined, an identical request is
// refused, a different one is not, engines reload only after the settle and
// a passing probe, and the fault is one record with one Panel line.
func TestResetStopsEnginesFailsAndQuarantinesTheRequestAndReloadsAfterProbe(t *testing.T) {
	r := newRig(t, Options{})
	video, err := r.load("fastwan")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := r.load("qwen")
	if err != nil {
		t.Fatal(err)
	}
	// One engine's exit is observable while the next engine's Stop is pending.
	chatStop := make(chan struct{})
	chat.stopAfter = chatStop
	var releaseOnce sync.Once
	releaseChat := func() { releaseOnce.Do(func() { close(chatStop) }) }
	t.Cleanup(releaseChat)
	end, err := r.holder.Begin(context.Background(), "fastwan")
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint{Model: "fastwan", Engine: "sd-server master-843", Shape: "1280x704x121 fa"}
	if err := r.guard.AdmitRequest(context.Background(), fp); err != nil {
		t.Fatalf("a request before any fault was refused: %v", err)
	}

	// The request runner waits on its engine, as the video worker does.
	failed := make(chan string, 1)
	go func() {
		<-video.exited
		if _, reset := r.guard.Classify(context.Background(), video.at); reset {
			r.guard.Failed(fp)
			failed <- WordReset
			return
		}
		failed <- "engine:lost"
	}()

	reset := Event{Kind: Reset, At: time.Now(), Source: "WerKernel 1001 AMD_WATCHDOG"}
	r.source.events <- reset
	deadline := time.After(5 * time.Second)
	select {
	case word := <-failed:
		if word != WordReset {
			t.Fatalf("the running request failed with %s", word)
		}
	case <-deadline:
		t.Fatal("the reset did not stop the running engine")
	}
	end()
	select {
	case <-r.source.handled:
		t.Fatal("the reset callback completed before the second engine stopped")
	default:
	}
	releaseChat()
	// Request failure observes the first exit. The source acknowledges only
	// after the entire callback has stopped every engine and released leases.
	select {
	case handled := <-r.source.handled:
		if handled != reset {
			t.Fatalf("another callback completed: %+v", handled)
		}
	case <-deadline:
		t.Fatal("the reset did not finish stopping every engine")
	}
	if video.Alive() || chat.Alive() {
		t.Fatal("an engine on the reset adapter is still running")
	}
	if n := r.leases.liveOn("card:0"); n != 0 {
		t.Fatalf("%d leases still live after the reset", n)
	}
	if err := r.guard.AdmitRequest(context.Background(), fp); wordOf(err) != WordResetting {
		t.Fatalf("a request inside the settle read %v", err)
	}
	if _, err := r.load("qwen"); wordOf(err) != WordResetting {
		t.Fatalf("a load inside the settle read %v", err)
	}

	fault := nextFault(t, r.faults)
	if fault.Source != "WerKernel 1001 AMD_WATCHDOG" || strings.Join(fault.Engines, ",") != "fastwan,qwen" ||
		len(fault.Quarantined) != 1 || fault.Quarantined[0].Key != fp.Key() || len(fault.Unreleased) != 0 {
		t.Fatalf("fault %+v", fault)
	}
	if !strings.Contains(fault.Line, "fastwan, qwen") || strings.Contains(fault.Line, fp.Key()) {
		t.Fatalf("panel line %q", fault.Line)
	}
	select {
	case extra := <-r.faults:
		t.Fatalf("a second record for one fault: %+v", extra)
	default:
	}

	time.Sleep(60 * time.Millisecond)
	r.setProbe(func() error { return ErrNoAdapter })
	if _, err := r.load("qwen"); wordOf(err) != WordResetting {
		t.Fatalf("a load with the adapter missing read %v", err)
	}
	r.setProbe(func() error { return nil })
	reloaded, err := r.load("qwen")
	if err != nil || !reloaded.Alive() {
		t.Fatalf("no reload after a healthy probe: %v", err)
	}
	if err := r.guard.AdmitRequest(context.Background(), fp); wordOf(err) != WordQuarantined {
		t.Fatalf("the identical request read %v", err)
	}
	other := fp
	other.Shape = "832x480x49 fa"
	if err := r.guard.AdmitRequest(context.Background(), other); err != nil {
		t.Fatalf("a different request was refused: %v", err)
	}
	if cleared, err := r.guard.Clear(fp.Key()); !cleared || err != nil {
		t.Fatalf("clear %v %v", cleared, err)
	}
	if err := r.guard.AdmitRequest(context.Background(), fp); err != nil {
		t.Fatalf("a cleared request was refused: %v", err)
	}
}

// RES-G5: a standby entry refuses a new load before any lease; the exit
// admits it again.
func TestStandbyEntryBlocksANewLoad(t *testing.T) {
	r := newRig(t, Options{})
	r.source.events <- Event{Kind: StandbyEnter, Source: "Kernel-Power 506"}
	waitFor(t, func() bool { return wordOf(r.guard.AdmitLoad(context.Background())) == WordStandby })
	if _, err := r.load("fastwan"); wordOf(err) != WordStandby {
		t.Fatalf("a load in standby read %v", err)
	}
	if n := r.leases.liveOn("card:0"); n != 0 {
		t.Fatalf("a refused load holds %d leases", n)
	}
	r.source.events <- Event{Kind: StandbyExit, Source: "Kernel-Power 507"}
	waitFor(t, func() bool { return r.guard.AdmitLoad(context.Background()) == nil })
	if _, err := r.load("fastwan"); err != nil {
		t.Fatalf("a load after standby read %v", err)
	}
}

// No record, no action: engines stay, nothing is refused, an engine exit with
// no reset near it is not a reset, and no fault is written.
func TestNoEventNoAction(t *testing.T) {
	r := newRig(t, Options{Window: 50 * time.Millisecond})
	e, err := r.load("fastwan")
	if err != nil {
		t.Fatal(err)
	}
	if _, reset := r.guard.Classify(context.Background(), time.Now()); reset {
		t.Fatal("an exit with no reset record was classified as a reset")
	}
	if !e.Alive() || r.leases.liveOn("card:0") != 1 {
		t.Fatal("an engine was stopped with no record")
	}
	if err := r.guard.AdmitRequest(context.Background(), Fingerprint{Model: "fastwan"}); err != nil {
		t.Fatalf("refused with no record: %v", err)
	}
	select {
	case f := <-r.faults:
		t.Fatalf("a fault with no record: %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
	if len(r.guard.Faults()) != 0 || r.guard.Blind() != nil {
		t.Fatal("the guard recorded something")
	}
}

// RES-G1: records inside one window are one fault.
func TestRecordsOfOneResetAreOneFault(t *testing.T) {
	r := newRig(t, Options{})
	if _, err := r.load("fastwan"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.source.events <- Event{Kind: Reset, At: now, Source: "WerKernel 1001 WATCHDOG"}
	r.source.events <- Event{Kind: Reset, At: now.Add(5 * time.Millisecond), Source: "WerKernel 1001 AMD_WATCHDOG"}
	r.source.events <- Event{Kind: ResetCorroboration, At: now.Add(10 * time.Millisecond), Source: "LiveKernelEvent 141"}
	f := nextFault(t, r.faults)
	if len(f.Corroborated) != 1 || f.Corroborated[0] != "LiveKernelEvent 141" {
		t.Fatalf("the corroborating record did not join the fault: %+v", f)
	}
	select {
	case f := <-r.faults:
		t.Fatalf("one reset wrote two faults: %+v", f)
	case <-time.After(300 * time.Millisecond):
	}
}

// flakySource fails its first fails watches, then subscribes and delivers
// Watching until ctx ends.
type flakySource struct {
	mu      sync.Mutex
	fails   int
	watches int
}

func (s *flakySource) Watch(ctx context.Context, deliver func(Event)) error {
	s.mu.Lock()
	s.watches++
	n := s.watches
	s.mu.Unlock()
	if n <= s.fails {
		return errors.New("EvtNext: the handle is invalid")
	}
	deliver(Event{Kind: Watching})
	<-ctx.Done()
	return nil
}

func (s *flakySource) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.watches }

// RES-G1: a source that cannot read the platform is blind, never quiet; the
// Guard watches again with backoff, and a subscription that stands clears the
// blindness.
func TestAFailedSourceIsBlindUntilItSubscribesAgain(t *testing.T) {
	var mu sync.Mutex
	var reported []error
	source := &flakySource{fails: 2}
	g, err := New(Options{Records: source, Resubscribe: 20 * time.Millisecond,
		OnError: func(err error) { mu.Lock(); reported = append(reported, err); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	waitFor(t, func() bool { return source.count() >= 1 && g.Blind() != nil })
	waitFor(t, func() bool { return source.count() == 3 && g.Blind() == nil })
	mu.Lock()
	failures := len(reported)
	mu.Unlock()
	if failures != 2 {
		t.Fatalf("%d failures reported for two failed watches", failures)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end with its context")
	}
}

// RES-G3: backoff doubles to its cap, the third strike holds until cleared,
// and the quarantine outlives a restart.
func TestQuarantineBacksOffHoldsAndPersists(t *testing.T) {
	now := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	state := filepath.Join(t.TempDir(), "gpu-quarantine.json")
	g, err := New(Options{Now: clock, State: state, Probe: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint{Model: "hummingbird", Engine: "pytorch-rocm", Shape: "512x512x81"}
	for strike, want := range []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute} {
		e := g.Failed(fp)
		if e.Strikes != strike+1 || e.Until != now.Add(want) || e.Held != (strike == 2) {
			t.Fatalf("strike %d: %+v", strike+1, e)
		}
	}
	now = now.Add(48 * time.Hour)
	if err := g.AdmitRequest(context.Background(), fp); wordOf(err) != WordQuarantined {
		t.Fatalf("a held fingerprint read %v after its time", err)
	}
	reopened, err := New(Options{Now: clock, State: state, Probe: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.AdmitRequest(context.Background(), fp); wordOf(err) != WordQuarantined {
		t.Fatalf("the quarantine did not survive a restart: %v", err)
	}
	// A person's clear from another process reaches the running guard.
	if removed, _, err := ClearQuarantine(state, fp.Key()); removed != 1 || err != nil {
		t.Fatalf("clear removed %d: %v", removed, err)
	}
	if err := reopened.AdmitRequest(context.Background(), fp); err != nil {
		t.Fatalf("the running guard did not read the clear: %v", err)
	}
	reopened.Failed(fp)
	if entries, err := ReadQuarantine(state); err != nil || len(entries) != 1 || entries[0].Strikes != 1 {
		t.Fatalf("after a new strike the file holds %+v %v", entries, err)
	}
	if cleared, err := reopened.Clear(fp.Key()); !cleared || err != nil {
		t.Fatal(err)
	}
	again, err := New(Options{Now: clock, State: state})
	if err != nil || len(again.Quarantined()) != 0 {
		t.Fatalf("a cleared entry came back: %v %+v", err, again.Quarantined())
	}
}

// RES-G5, RES-A1: overlapping GPU work takes the wake lease once and gives it
// back after the last ends.
func TestBusyHoldsOneWakeLeaseThroughOverlappingWork(t *testing.T) {
	leases := newFakeLeases()
	g, err := New(Options{Wake: LeaseWake(leases, 10*time.Millisecond, nil)})
	if err != nil {
		t.Fatal(err)
	}
	first := g.Busy("video")
	second := g.Busy("video")
	if leases.liveOn(res.ResourceAwake) != 1 {
		t.Fatalf("%d wake leases for two jobs", leases.liveOn(res.ResourceAwake))
	}
	time.Sleep(30 * time.Millisecond)
	first()
	first()
	if leases.liveOn(res.ResourceAwake) != 1 {
		t.Fatal("the wake lease ended while work still ran")
	}
	second()
	if leases.liveOn(res.ResourceAwake) != 0 {
		t.Fatal("the wake lease outlived the work")
	}
	leases.mu.Lock()
	renewed := leases.renewed
	leases.mu.Unlock()
	if renewed == 0 {
		t.Fatal("the wake lease was never renewed")
	}
}

// RES-G5: the dispatch rows are data a provider applies.
func TestDispatchRows(t *testing.T) {
	sd, ok := DispatchFor(EngineSDCpp)
	if !ok {
		t.Fatal("no sd.cpp row")
	}
	if got := sd.WithArgs([]string{"--offload-to-cpu"}); strings.Join(got, " ") != "--offload-to-cpu --diffusion-fa" {
		t.Fatalf("args %q", got)
	}
	if got := sd.WithArgs([]string{"--diffusion-fa"}); len(got) != 1 {
		t.Fatalf("a present argument was added again: %q", got)
	}
	torch, _ := DispatchFor(EnginePyTorchROCm)
	env := strings.Join(torch.Environment(), " ")
	for _, want := range []string{"MIOPEN_FIND_MODE=FAST", "MIOPEN_DEBUG_CONV_DIRECT=0", "TORCH_ROCM_AOTRITON_ENABLE_EXPERIMENTAL=1",
		"MIOPEN_DEBUG_CONV_DIRECT_NAIVE_CONV_FWD=0", "MIOPEN_DEBUG_CONV_DIRECT_NAIVE_CONV_BWD=0", "MIOPEN_DEBUG_CONV_DIRECT_NAIVE_CONV_WRW=0",
		"MIOPEN_DEBUG_AMD_WINOGRAD_FURY_RXS_F2X3=0", "MIOPEN_DEBUG_AMD_WINOGRAD_FURY_RXS_F3X2=0"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env %q lacks %s", env, want)
		}
	}
	if err := torch.Check(map[string]int64{"math-attention-tokens": 27280}); wordOf(err) != WordKnownBad {
		t.Fatalf("the known-bad shape read %v", err)
	}
	if err := torch.Check(map[string]int64{"math-attention-tokens": 8192}); err != nil {
		t.Fatal(err)
	}
}

// RES-G5: a row handed out is a copy; changing it changes no other caller's.
func TestDispatchForHandsOutCopies(t *testing.T) {
	torch, _ := DispatchFor(EnginePyTorchROCm)
	torch.Env["MIOPEN_FIND_MODE"] = "NORMAL"
	torch.KnownBad["math-attention-tokens"] = 1 << 40
	torch.Fixes[0] = "changed"
	sd, _ := DispatchFor(EngineSDCpp)
	sd.Args[0] = "--fa"
	again, _ := DispatchFor(EnginePyTorchROCm)
	if again.Env["MIOPEN_FIND_MODE"] != "FAST" || again.KnownBad["math-attention-tokens"] != 27280 || again.Fixes[0] == "changed" {
		t.Fatalf("a caller changed the row: %+v", again)
	}
	if sdAgain, _ := DispatchFor(EngineSDCpp); sdAgain.Args[0] != "--diffusion-fa" {
		t.Fatalf("a caller changed the sd.cpp row: %+v", sdAgain)
	}
}

// RES-G3: a damaged or newer quarantine file is moved aside beside itself,
// reported once, and the Guard starts empty; clearing every entry of a
// damaged file is done.
func TestADamagedQuarantineIsMovedAsideAndStartsEmpty(t *testing.T) {
	for name, content := range map[string]string{
		"not json": "{ this is not json",
		"newer":    `{"version": 2, "entries": []}`,
		"bad key":  `{"version": 1, "entries": [{"key": "x", "fingerprint": {"Model": "m"}}]}`,
	} {
		dir := t.TempDir()
		state := filepath.Join(dir, "gpu-quarantine.json")
		if err := os.WriteFile(state, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadQuarantine(state); !errors.Is(err, ErrDamagedQuarantine) {
			t.Fatalf("%s: read %v", name, err)
		}
		var reported []error
		g, err := New(Options{State: state, OnError: func(err error) { reported = append(reported, err) }})
		if err != nil {
			t.Fatalf("%s: a damaged quarantine stopped the Guard: %v", name, err)
		}
		if len(reported) != 1 || len(g.Quarantined()) != 0 || g.Damaged() == "" {
			t.Fatalf("%s: reported %v, %d entries, moved to %q", name, reported, len(g.Quarantined()), g.Damaged())
		}
		kept, err := os.ReadFile(g.Damaged())
		if err != nil || string(kept) != content || filepath.Dir(g.Damaged()) != dir {
			t.Fatalf("%s: the damaged file was not kept beside the quarantine: %v", name, err)
		}
		if found := DamagedQuarantines(state); len(found) != 1 || found[0] != g.Damaged() {
			t.Fatalf("%s: damaged files listed %v", name, found)
		}
		g.Failed(Fingerprint{Model: "m", Engine: "e", Shape: "s"})
		if entries, err := ReadQuarantine(state); err != nil || len(entries) != 1 {
			t.Fatalf("%s: the new quarantine reads %v %v", name, entries, err)
		}

		other := filepath.Join(t.TempDir(), "gpu-quarantine.json")
		if err := os.WriteFile(other, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		removed, moved, err := ClearQuarantine(other, "")
		if err != nil || removed != 0 || moved == "" {
			t.Fatalf("%s: clear --all on a damaged file: %d %q %v", name, removed, moved, err)
		}
		if entries, err := ReadQuarantine(other); err != nil || len(entries) != 0 {
			t.Fatalf("%s: after clear the quarantine reads %v %v", name, entries, err)
		}
	}
}

// renewFails ends its first live lease at the first renewal.
type renewFails struct {
	*fakeLeases
	once sync.Once
}

func (r *renewFails) Renew(id string) (res.LeaseChange, error) {
	ended := false
	r.once.Do(func() { ended = true })
	if ended {
		r.fakeLeases.Release(id)
		return res.LeaseChange{Applied: false}, nil
	}
	return r.fakeLeases.Renew(id)
}

// RES-A1: a wake lease that ended under running work is taken again.
func TestTheWakeLeaseIsTakenAgainAfterAFailedRenewal(t *testing.T) {
	leases := &renewFails{fakeLeases: newFakeLeases()}
	var mu sync.Mutex
	var reported []error
	wake := LeaseWake(leases, 10*time.Millisecond, func(err error) { mu.Lock(); reported = append(reported, err); mu.Unlock() })
	release, err := wake("video")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		leases.mu.Lock()
		defer leases.mu.Unlock()
		return leases.next >= 2
	})
	waitFor(t, func() bool { return leases.liveOn(res.ResourceAwake) == 1 })
	release()
	if leases.liveOn(res.ResourceAwake) != 0 {
		t.Fatal("the retaken wake lease outlived the work")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) == 0 {
		t.Fatal("the ended lease was not reported")
	}
}

// nextFault is the next closed fault, or a failed test after 10 s.
func nextFault(t *testing.T, faults <-chan Fault) Fault {
	t.Helper()
	select {
	case f := <-faults:
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("no fault was recorded")
		return Fault{}
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
