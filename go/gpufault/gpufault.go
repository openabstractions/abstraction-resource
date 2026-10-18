// Package gpufault is a provider's GPU fault handling: it reads the
// platform's driver-reset and standby records, stops every resident engine of
// the attached holders when the driver resets, quarantines the request that
// was running, refuses new loads until the adapter is back and while the
// machine is in Modern Standby, and keeps the machine awake through the wake
// lease while GPU work runs (CONTRACT.md RES-G1..RES-G7).
//
// The resource kit owns it because the holder owns the engines' leases: a
// reset is a lease event first. Each provider composes one Guard, attaches
// its holders and asks the Guard before every load and every GPU job.
package gpufault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openabstractions/abstraction-resource/go/holder"
)

// EventKind is what a platform record says happened to the GPU or the
// machine.
type EventKind string

const (
	// Reset is a driver timeout and recovery (TDR) the kernel recorded: a
	// WerKernel WATCHDOG live dump written by the kernel (records.go).
	Reset EventKind = "reset"
	// ResetCorroboration is a record that agrees with a reset but cannot
	// trigger one: the Application log's LiveKernelEvent, which any
	// interactive program can write, and the Display 4101 record, which
	// names no provider GUID to check. It joins an open fault's record.
	ResetCorroboration EventKind = "reset-corroboration"
	// DeviceLost is an engine reporting its device removed or lost, after
	// the provider confirmed the engine is gone; it is handled as a reset
	// of the adapter the engine used.
	DeviceLost EventKind = "device-lost"
	// StandbyEnter and StandbyExit are Kernel-Power 506 and 507.
	StandbyEnter EventKind = "standby-enter"
	StandbyExit  EventKind = "standby-exit"
	// Watching is a source saying it subscribed and reads the platform's
	// records; it clears a blind Guard (RES-G1).
	Watching EventKind = "watching"
	// bootRecord is Kernel-General 12, read by a source at start only.
	bootRecord EventKind = "boot"
)

// Event is one platform record: what happened, when the platform wrote it,
// and which record said so ("WerKernel 1001 WATCHDOG").
type Event struct {
	Kind   EventKind
	At     time.Time
	Source string
}

// Records delivers platform records until ctx ends, first Watching once its
// subscription stands. An error is a source that cannot read the platform's
// records; the Guard reports it, is blind until a resubscription delivers
// Watching again, and resubscribes with backoff (RES-G1).
type Records interface {
	Watch(ctx context.Context, deliver func(Event)) error
}

// The words a GPU fault carries. WordReset fails the request that was
// running; WordEngineReset is why its engine was stopped; the other three
// refuse a load or a request (RES-G2..RES-G5).
const (
	WordReset       = "gpu:reset"
	WordEngineReset = "engine:gpu-reset"
	WordResetting   = "gpu:resetting"
	WordStandby     = "gpu:standby"
	WordQuarantined = "gpu:quarantined"
)

// Refusal is a load or request the Guard refused. Until is when a quarantine
// or a settle ends; zero when the refusal has no known end.
type Refusal struct {
	Word   string
	Detail string
	Until  time.Time
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return r.Word
	}
	return r.Word + ": " + r.Detail
}

// Fingerprint names a request by what reaches the GPU: the model, the engine
// and the request's shape (size, length, dispatch settings), never its prompt
// or seed. Two requests with one fingerprint dispatch the same work.
type Fingerprint struct {
	Model  string
	Engine string
	Shape  string
}

// Key is the fingerprint's stable id: the first 12 bytes of the SHA-256 of
// its three fields, hex.
func (f Fingerprint) Key() string {
	sum := sha256.Sum256([]byte(f.Model + "\x00" + f.Engine + "\x00" + f.Shape))
	return hex.EncodeToString(sum[:12])
}

// QuarantineEntry is one quarantined fingerprint. Held is a fingerprint that reached
// Options.Strikes and stays refused until a person clears it.
type QuarantineEntry struct {
	Key         string      `json:"key"`
	Fingerprint Fingerprint `json:"fingerprint"`
	Strikes     int         `json:"strikes"`
	Last        time.Time   `json:"last"`
	Until       time.Time   `json:"until"`
	Held        bool        `json:"held"`
}

// Fault is one driver reset as this provider saw it: the record that said so,
// the engines it stopped, the requests it quarantined, and the one Panel line
// that tells a person (RES-G6).
type Fault struct {
	At          time.Time
	Source      string
	Engines     []string
	Unreleased  []string
	Quarantined []QuarantineEntry
	// Corroborated are the records that agreed with the reset and cannot
	// trigger one (ResetCorroboration).
	Corroborated []string
	Line         string
}

// LogValue is the fault's one log record: a group of string attributes, so the
// logging service's records stay flat (logging LOG-R5, LOG-R6).
func (f Fault) LogValue() slog.Value {
	keys := make([]string, 0, len(f.Quarantined))
	for _, e := range f.Quarantined {
		keys = append(keys, e.Key)
	}
	return slog.GroupValue(
		slog.String("at", f.At.UTC().Format(time.RFC3339Nano)),
		slog.String("source", f.Source),
		slog.String("engines", strings.Join(f.Engines, ",")),
		slog.String("unreleased", strings.Join(f.Unreleased, ",")),
		slog.String("quarantined", strings.Join(keys, ",")),
		slog.String("corroborated", strings.Join(f.Corroborated, ",")),
		slog.String("line", f.Line),
	)
}

// Log writes the fault as one record at warning level.
func (f Fault) Log(logger *slog.Logger) {
	if logger == nil {
		return
	}
	logger.LogAttrs(context.Background(), slog.LevelWarn, "gpu fault", slog.Any("gpu_fault", f))
}

// Loser is a holder whose components a reset ends; holder.Holder is one.
type Loser interface {
	LoseAll(why string) []holder.Lost
}

// WakeFunc takes the wake lease for why and returns its release (RES-A1).
type WakeFunc func(why string) (release func(), err error)

// Options configures a Guard. Zero durations read the defaults below.
type Options struct {
	// Records is the platform's records; nil reads none, and the Guard acts
	// only on what Report is given.
	Records Records
	// Resubscribe is the first wait before Records is watched again after
	// it failed; each further failure doubles it up to a minute.
	Resubscribe time.Duration
	// Probe reports whether the adapter is back after a reset. Nil reads
	// AdapterProbe.
	Probe func(context.Context) error
	// Settle is how long after the last reset record no load starts.
	Settle time.Duration
	// Window is how close to a reset an engine's exit is the reset's, and
	// how long the fault's record gathers the requests it failed.
	Window time.Duration
	// Grace is how long Classify waits after an engine's exit for a reset
	// record to arrive; a plain crash reads engine:lost no later than this.
	// It is at most Window.
	Grace time.Duration
	// Backoff is the first quarantine; each further strike doubles it up to
	// MaxBackoff. Strikes is the count at which a fingerprint is held until
	// cleared.
	Backoff, MaxBackoff time.Duration
	Strikes             int
	// State is the file the quarantine is kept in across restarts; empty
	// keeps it in memory.
	State string
	// Wake takes the wake lease while GPU work runs; nil takes none.
	Wake WakeFunc
	Now  func() time.Time
	// OnFault receives each fault once its window closes: its log record
	// and its Panel line (RES-G6).
	OnFault func(Fault)
	OnError func(error)
}

const (
	DefaultResetSettle = 10 * time.Second
	DefaultWindow      = 30 * time.Second
	DefaultGrace       = 10 * time.Second
	DefaultBackoff     = 10 * time.Minute
	DefaultBackoffCap  = 24 * time.Hour
	DefaultStrikes     = 3
	maxFaults          = 64
	defaultResubscribe = time.Second
	maxResubscribe     = time.Minute
)

// Guard is one provider's GPU fault handling.
type Guard struct {
	opts Options

	mu         sync.Mutex
	losers     []Loser
	standby    bool
	resetting  bool
	lastReset  time.Time
	resetGen   uint64
	resetCh    chan struct{}
	open       *Fault
	faults     []Fault
	quarantine map[string]*QuarantineEntry
	stateSeen  stamp
	// damaged is where a damaged quarantine file was moved when the Guard
	// was made, or "".
	damaged string
	blind   error
	busy    int
	release func()
}

// New makes a Guard and reads the quarantine kept in Options.State.
func New(opts Options) (*Guard, error) {
	if opts.Probe == nil {
		opts.Probe = AdapterProbe
	}
	if opts.Settle <= 0 {
		opts.Settle = DefaultResetSettle
	}
	if opts.Window <= 0 {
		opts.Window = DefaultWindow
	}
	if opts.Grace <= 0 {
		opts.Grace = min(DefaultGrace, opts.Window)
	}
	opts.Grace = min(opts.Grace, opts.Window)
	if opts.Backoff <= 0 {
		opts.Backoff = DefaultBackoff
	}
	if opts.MaxBackoff < opts.Backoff {
		opts.MaxBackoff = max(DefaultBackoffCap, opts.Backoff)
	}
	if opts.Strikes <= 0 {
		opts.Strikes = DefaultStrikes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Resubscribe <= 0 {
		opts.Resubscribe = defaultResubscribe
	}
	g := &Guard{opts: opts, resetCh: make(chan struct{}), quarantine: map[string]*QuarantineEntry{}}
	entries, moved, damage, err := openState(opts.State, opts.Now())
	if err != nil {
		return nil, err
	}
	if moved != "" {
		g.damaged = moved
		g.report(fmt.Errorf("gpufault: %v; it was moved to %s and the quarantine starts empty", damage, moved))
	}
	for i := range entries {
		e := entries[i]
		g.quarantine[e.Key] = &e
	}
	if opts.State != "" {
		g.stateSeen = stateStamp(opts.State)
	}
	return g, nil
}

// Attach adds a holder whose components a reset ends.
func (g *Guard) Attach(l Loser) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.losers = append(g.losers, l)
}

// Run reads the Records until ctx ends. Records that fail make the Guard
// blind: it is reported through OnError and by Blind, the Guard goes on
// acting on what Report is given, and Records is watched again after a
// backoff that starts at Resubscribe and doubles up to a minute. The next
// Watching clears the blindness and the backoff (RES-G1).
func (g *Guard) Run(ctx context.Context) {
	if g.opts.Records == nil {
		<-ctx.Done()
		return
	}
	wait := g.opts.Resubscribe
	for {
		var watched atomic.Bool
		err := g.opts.Records.Watch(ctx, func(ev Event) {
			if ev.Kind == Watching {
				watched.Store(true)
			}
			g.Report(ev)
		})
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("the subscription ended")
		}
		err = fmt.Errorf("gpufault: the platform's GPU and standby records cannot be read: %w", err)
		g.mu.Lock()
		g.blind = err
		g.mu.Unlock()
		g.report(err)
		if watched.Load() {
			wait = g.opts.Resubscribe
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		wait = min(wait*2, maxResubscribe)
	}
}

// Blind is why the platform's records are not being read, or nil.
func (g *Guard) Blind() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.blind
}

// Damaged is where a damaged quarantine file was moved when the Guard was
// made, or "" (RES-G3).
func (g *Guard) Damaged() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.damaged
}

// Report acts on one record: a source's, or an engine's own report of a lost
// device (RES-G1, RES-G2).
func (g *Guard) Report(ev Event) {
	if ev.At.IsZero() {
		ev.At = g.opts.Now()
	}
	switch ev.Kind {
	case StandbyEnter:
		g.mu.Lock()
		g.standby = true
		g.mu.Unlock()
	case StandbyExit:
		g.mu.Lock()
		g.standby = false
		g.mu.Unlock()
	case Watching:
		g.mu.Lock()
		g.blind = nil
		g.mu.Unlock()
	case Reset, DeviceLost:
		g.reset(ev)
	case ResetCorroboration:
		// It never triggers a reset; it joins the open fault's record.
		g.mu.Lock()
		if g.open != nil {
			g.open.Corroborated = append(g.open.Corroborated, ev.Source)
		}
		g.mu.Unlock()
	}
}

// reset stops every attached holder's components once per reset: records
// inside one Window of an open fault extend its settle and add no fault.
func (g *Guard) reset(ev Event) {
	g.mu.Lock()
	if ev.At.After(g.lastReset) {
		g.lastReset = ev.At
	}
	g.resetting = true
	g.resetGen++
	close(g.resetCh)
	g.resetCh = make(chan struct{})
	if g.open != nil {
		g.mu.Unlock()
		return
	}
	fault := &Fault{At: ev.At, Source: ev.Source}
	g.open = fault
	losers := append([]Loser(nil), g.losers...)
	g.mu.Unlock()

	for _, l := range losers {
		for _, lost := range l.LoseAll(WordEngineReset) {
			g.mu.Lock()
			fault.Engines = append(fault.Engines, lost.Name)
			if !lost.Released {
				fault.Unreleased = append(fault.Unreleased, lost.Name)
			}
			g.mu.Unlock()
		}
	}
	time.AfterFunc(g.opts.Window, g.close)
}

// close ends the open fault's window and hands its one record to OnFault.
func (g *Guard) close() {
	g.mu.Lock()
	fault := g.open
	g.open = nil
	if fault == nil {
		g.mu.Unlock()
		return
	}
	sort.Strings(fault.Engines)
	fault.Line = panelLine(*fault)
	g.faults = append(g.faults, *fault)
	if len(g.faults) > maxFaults {
		g.faults = g.faults[len(g.faults)-maxFaults:]
	}
	done := *fault
	g.mu.Unlock()
	if g.opts.OnFault != nil {
		g.opts.OnFault(done)
	}
}

// Flush closes an open fault now, for a provider that is stopping.
func (g *Guard) Flush() { g.close() }

// Faults are the faults closed so far, oldest first, at most 64.
func (g *Guard) Faults() []Fault {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Fault(nil), g.faults...)
}

// panelLine is the fault's one short sentence for the Panel (Panel text
// rules: one sentence, names the engine, no ids).
func panelLine(f Fault) string {
	if len(f.Engines) == 0 && len(f.Quarantined) == 0 {
		return ""
	}
	names := strings.Join(f.Engines, ", ")
	switch {
	case len(f.Quarantined) > 0 && names != "":
		return "The graphics driver reset; " + names + " was stopped and the request that was running is paused."
	case len(f.Quarantined) > 0:
		return "The graphics driver reset; the request that was running is paused."
	default:
		return "The graphics driver reset; " + names + " was stopped and restarts on the next request."
	}
}

// AdmitLoad refuses a new GPU load while the machine is in Modern Standby
// (RES-G5), and after a reset until the settle passed and the probe found
// the adapter back (RES-G4). A refusal is a *Refusal.
func (g *Guard) AdmitLoad(ctx context.Context) error {
	g.mu.Lock()
	if g.standby {
		g.mu.Unlock()
		return &Refusal{Word: WordStandby, Detail: "the machine is in Modern Standby; GPU work starts once it wakes"}
	}
	if !g.resetting {
		g.mu.Unlock()
		return nil
	}
	ready := g.lastReset.Add(g.opts.Settle)
	gen := g.resetGen
	g.mu.Unlock()
	if now := g.opts.Now(); now.Before(ready) {
		return &Refusal{Word: WordResetting, Detail: "the graphics driver reset moments ago; engines reload once it settles", Until: ready}
	}
	if err := g.opts.Probe(ctx); err != nil {
		return &Refusal{Word: WordResetting, Detail: fmt.Sprintf("the graphics adapter is not back after its reset: %v", err)}
	}
	g.mu.Lock()
	if g.resetGen == gen {
		g.resetting = false
	}
	g.mu.Unlock()
	return nil
}

// AdmitRequest is AdmitLoad for one request, and refuses a fingerprint in
// quarantine (RES-G3).
func (g *Guard) AdmitRequest(ctx context.Context, fp Fingerprint) error {
	if err := g.AdmitLoad(ctx); err != nil {
		return err
	}
	return g.Quarantine(fp)
}

// Quarantine is fp's quarantine refusal, or nil: the check a provider makes
// when it admits a request, before the request waits for any load (RES-G3).
func (g *Guard) Quarantine(fp Fingerprint) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshLocked()
	e := g.quarantine[fp.Key()]
	if e == nil {
		return nil
	}
	if e.Held {
		return &Refusal{Word: WordQuarantined, Detail: fmt.Sprintf("this request reset the graphics driver %d times and waits until it is cleared", e.Strikes)}
	}
	if now := g.opts.Now(); now.Before(e.Until) {
		return &Refusal{Word: WordQuarantined, Detail: "this request reset the graphics driver and is paused", Until: e.Until}
	}
	return nil
}

// Classify reports whether an engine that ended at ended ended in a reset:
// a reset record stamped within Window of it. It waits for a record to
// arrive no longer than Grace after ended, or ctx.
func (g *Guard) Classify(ctx context.Context, ended time.Time) (Event, bool) {
	deadline := ended.Add(g.opts.Window)
	waitUntil := ended.Add(g.opts.Grace)
	for {
		g.mu.Lock()
		last, ch, source := g.lastReset, g.resetCh, ""
		if g.open != nil {
			source = g.open.Source
		}
		g.mu.Unlock()
		if !last.IsZero() && !last.Before(ended.Add(-g.opts.Window)) && !last.After(deadline) {
			return Event{Kind: Reset, At: last, Source: source}, true
		}
		wait := waitUntil.Sub(g.opts.Now())
		if wait <= 0 {
			return Event{}, false
		}
		timer := time.NewTimer(wait)
		select {
		case <-ch:
			timer.Stop()
		case <-timer.C:
			return Event{}, false
		case <-ctx.Done():
			timer.Stop()
			return Event{}, false
		}
	}
}

// Failed quarantines fp after the request it names failed in a reset:
// Backoff after the first strike, doubled after each next one up to
// MaxBackoff, held until cleared at Strikes (RES-G3). The entry joins the
// open fault's record.
func (g *Guard) Failed(fp Fingerprint) QuarantineEntry {
	g.mu.Lock()
	g.refreshLocked()
	now := g.opts.Now()
	key := fp.Key()
	e := g.quarantine[key]
	if e == nil {
		e = &QuarantineEntry{Key: key, Fingerprint: fp}
		g.quarantine[key] = e
	}
	e.Strikes++
	e.Last = now
	backoff := g.opts.Backoff
	for i := 1; i < e.Strikes && backoff < g.opts.MaxBackoff; i++ {
		backoff *= 2
	}
	backoff = min(backoff, g.opts.MaxBackoff)
	e.Until = now.Add(backoff)
	e.Held = e.Strikes >= g.opts.Strikes
	done := *e
	if g.open != nil {
		g.open.Quarantined = append(g.open.Quarantined, done)
	}
	err := g.saveLocked()
	g.mu.Unlock()
	if err != nil {
		g.report(fmt.Errorf("gpufault: keeping the quarantine: %w", err))
	}
	return done
}

// Quarantined is every quarantined fingerprint, by key.
func (g *Guard) Quarantined() []QuarantineEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshLocked()
	out := make([]QuarantineEntry, 0, len(g.quarantine))
	for _, e := range g.quarantine {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Clear lets a quarantined fingerprint run again; it is the owner's or the
// user's act, never automatic (RES-G3).
func (g *Guard) Clear(key string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshLocked()
	if _, ok := g.quarantine[key]; !ok {
		return false, nil
	}
	delete(g.quarantine, key)
	return true, g.saveLocked()
}

// Busy marks GPU work running and returns its end. While any runs the Guard
// holds the wake lease, taken once for the first and released after the last
// (RES-G5, RES-A1).
func (g *Guard) Busy(why string) (end func()) {
	g.mu.Lock()
	g.busy++
	first := g.busy == 1
	g.mu.Unlock()
	if first && g.opts.Wake != nil {
		release, err := g.opts.Wake(why)
		if err != nil {
			g.report(fmt.Errorf("gpufault: the wake lease: %w", err))
		}
		g.mu.Lock()
		if g.busy > 0 && release != nil && g.release == nil {
			g.release = release
			release = nil
		}
		g.mu.Unlock()
		if release != nil {
			release()
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.busy--
			var release func()
			if g.busy == 0 {
				release, g.release = g.release, nil
			}
			g.mu.Unlock()
			if release != nil {
				release()
			}
		})
	}
}

func (g *Guard) report(err error) {
	if g.opts.OnError != nil && err != nil {
		g.opts.OnError(err)
	}
}

// ErrNoAdapter is a probe that found no graphics adapter.
var ErrNoAdapter = errors.New("no graphics adapter is listed")
