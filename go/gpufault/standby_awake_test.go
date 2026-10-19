package gpufault

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// standbyRig is the provider composition (ForProvider) with a record source
// the test feeds and a power state the test sets.
type standbyRig struct {
	guard  *Guard
	source chanSource
	cancel context.CancelFunc
	mu     sync.Mutex
	awake  bool
	err    error
	logged lockedBuffer
}

func newStandbyRig(t *testing.T) *standbyRig {
	t.Helper()
	r := &standbyRig{source: chanSource{events: make(chan Event), handled: make(chan Event, 1)}}
	g, err := ForProvider(ProviderOptions{Name: "someproviderd", Records: r.source,
		Awake: func() (bool, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.awake, r.err
		},
		Logger: slog.New(slog.NewJSONHandler(&r.logged, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.guard = g
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go g.Run(ctx)
	t.Cleanup(cancel)
	return r
}

func (r *standbyRig) set(awake bool, err error) {
	r.mu.Lock()
	r.awake, r.err = awake, err
	r.mu.Unlock()
}

func (r *standbyRig) record(t *testing.T, ev Event) {
	t.Helper()
	r.source.events <- ev
	<-r.source.handled
}

// RES-G5, 2026-11-12: a Kernel-Power 506 with no 507 after it, read while the
// display is on, admits GPU work and logs one line.
func TestStandbyEntryWithoutExitWhileAwakeAdmits(t *testing.T) {
	r := newStandbyRig(t)
	r.set(true, nil)
	r.record(t, Event{Kind: StandbyEnter, At: time.Date(2026, 11, 12, 3, 51, 4, 0, time.UTC), Source: "Kernel-Power 506"})
	for range 3 {
		if err := r.guard.AdmitLoad(context.Background()); err != nil {
			t.Fatalf("an awake machine read %v", err)
		}
	}
	if n := strings.Count(r.logged.String(), "the display is on"); n != 1 {
		t.Fatalf("the set-aside 506 was logged %d times: %s", n, r.logged.String())
	}
}

// RES-G5: a standby entry while the display is off refuses GPU work with the
// plain sentence, a later standby entry after a set-aside one refuses again,
// and the 507 admits.
func TestStandbyEntryWhileDisplayOffRefuses(t *testing.T) {
	r := newStandbyRig(t)
	r.set(false, nil)
	first := time.Date(2026, 11, 13, 0, 24, 52, 0, time.UTC)
	r.record(t, Event{Kind: StandbyEnter, At: first, Source: "Kernel-Power 506"})
	var refusal *Refusal
	if err := r.guard.AdmitLoad(context.Background()); !errors.As(err, &refusal) || refusal.Word != WordStandby {
		t.Fatalf("standby read %v", err)
	}
	if refusal.Detail != "The computer is going to sleep; try again when it's awake." {
		t.Fatalf("the refusal says %q", refusal.Detail)
	}
	r.set(true, nil)
	if err := r.guard.AdmitLoad(context.Background()); err != nil {
		t.Fatalf("after the display came on: %v", err)
	}
	// The same 506, set aside while the display was on, refuses again once
	// the display turns off, and admits again when it comes back.
	r.set(false, nil)
	if err := r.guard.AdmitLoad(context.Background()); wordOf(err) != WordStandby {
		t.Fatalf("the set-aside 506 with the display off read %v", err)
	}
	r.set(true, nil)
	if err := r.guard.AdmitLoad(context.Background()); err != nil {
		t.Fatalf("the display back on: %v", err)
	}
	if n := strings.Count(r.logged.String(), "the display is on"); n != 1 {
		t.Fatalf("the set-aside 506 was logged %d times: %s", n, r.logged.String())
	}
	r.set(false, nil)
	second := first.Add(time.Minute)
	r.record(t, Event{Kind: StandbyEnter, At: second, Source: "Kernel-Power 506"})
	if err := r.guard.AdmitLoad(context.Background()); wordOf(err) != WordStandby {
		t.Fatalf("the next standby read %v", err)
	}
	r.record(t, Event{Kind: StandbyExit, Source: "Kernel-Power 507"})
	if err := r.guard.AdmitLoad(context.Background()); err != nil {
		t.Fatalf("after the 507: %v", err)
	}
	logged := r.logged.String()
	const refusalText = " has no exit record and the display is off; GPU load refused"
	if strings.Count(logged, refusalText) != 2 {
		t.Fatalf("want one refusal diagnostic per standby entry: %s", logged)
	}
	for _, at := range []time.Time{first, second} {
		if !strings.Contains(logged, "Kernel-Power 506 at "+at.Local().Format(time.RFC3339)+refusalText) {
			t.Fatalf("missing diagnostic for %s: %s", at, logged)
		}
	}
}

// RES-G5: a standby entry that begins while GPU work runs is reported once
// per entry, a set-aside entry included.
func TestStandbyDuringWorkIsReportedAfterASetAsideEntry(t *testing.T) {
	var reports []error
	awake := true
	g, err := New(Options{Awake: func() (bool, error) { return awake, nil },
		OnError: func(err error) { reports = append(reports, err) }})
	if err != nil {
		t.Fatal(err)
	}
	g.Report(Event{Kind: StandbyEnter, Source: "Kernel-Power 506"})
	if err := g.AdmitLoad(context.Background()); err != nil {
		t.Fatal(err)
	}
	reports = nil
	awake = false
	end := g.Busy("chat qwen")
	defer end()
	g.Report(Event{Kind: StandbyEnter, Source: "Kernel-Power 506"})
	if len(reports) != 1 || !strings.Contains(reports[0].Error(), "chat qwen") {
		t.Fatalf("reports %v", reports)
	}
	if err := g.AdmitLoad(context.Background()); wordOf(err) != WordStandby {
		t.Fatalf("standby during work read %v", err)
	}
}

// RES-G5, fail open: a power state that cannot be read admits the work and
// logs one line.
func TestStandbyEntryWithUnreadableStateAdmits(t *testing.T) {
	r := newStandbyRig(t)
	r.set(false, errors.New("no display state"))
	r.record(t, Event{Kind: StandbyEnter, Source: "Kernel-Power 506"})
	for range 3 {
		if err := r.guard.AdmitLoad(context.Background()); err != nil {
			t.Fatalf("an unreadable state read %v", err)
		}
	}
	if n := strings.Count(r.logged.String(), "cannot be read"); n != 1 {
		t.Fatalf("the unreadable state was logged %d times: %s", n, r.logged.String())
	}
}

// The provider composition on the platform's records reads the platform's
// power state.
func TestForProviderReadsThePlatformPowerState(t *testing.T) {
	g, err := ForProvider(ProviderOptions{Name: "someproviderd", Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if (g.opts.Records != nil) != (g.opts.Awake != nil) {
		t.Fatalf("records %v, awake reader set %v", g.opts.Records != nil, g.opts.Awake != nil)
	}
}
