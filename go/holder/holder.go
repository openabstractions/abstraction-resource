// Package holder coordinates a provider's owned components with resource leases.
// The resource service remains the authority for grants and verified release.
package holder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	res "github.com/openabstractions/abstraction-resource/go/client"
)

// Leases is the holder side of resource/leases@1.
type Leases interface {
	Acquire(string, int64, time.Duration) (res.AcquireResult, error)
	BindProcess(string, int) (res.ProcessBindResult, error)
	Renew(string) (res.LeaseChange, error)
	Release(string) (res.LeaseChange, error)
	// Label names what a lease holds for a person reading the machine's
	// memory (CONTRACT.md RES-L10). It is display only.
	Label(string, string) (res.LeaseChange, error)
	Observe(context.Context, string, time.Duration) (res.YieldPage, error)
	Answer(context.Context, string, res.YieldAnswer, string) (res.AnswerResult, error)
}

// tableReader is a lease service that also reads the resource table, which
// table@1 serves on the leases@1 endpoint. The resource client is one; an
// insufficient acquire reads it to name foreign memory.
type tableReader interface {
	Holders(string, bool) (res.ResourceState, error)
}

// FreeLeases is a lease service that also grants from what is free alone,
// asking nobody (CONTRACT.md RES-L6). The resource client is one;
// EnsureSpeculative needs it.
type FreeLeases interface {
	AcquireFree(string, int64) (res.AcquireFreeResult, error)
}

// Owned is one component started by this provider. Stop must target only this
// instance and return true only after its resource use is confirmed to end.
// PID is zero when no directly spawned process can be bound to the lease.
type Owned interface {
	PID() int
	Ready(context.Context) error
	Alive() bool
	Stop() bool
}

// ExitConfirmer is an Owned that tells an exit it saw from one whose
// resource use is confirmed to end: a process tree whose supervisor could
// not confirm every descendant gone. The holder owns the rule for every
// component (CONTRACT.md RES-L4): a component whose exit is unconfirmed is
// not stopped, keeps its lease record, and answers no yield, whatever its
// own Stop reported.
type ExitConfirmer interface {
	ExitConfirmed() bool
}

// stopped stops o and reports whether its resource use is confirmed to end:
// its Stop returned true and, for an ExitConfirmer, its exit is confirmed.
func stopped(o Owned) bool {
	if !o.Stop() {
		return false
	}
	if c, ok := o.(ExitConfirmer); ok && !c.ExitConfirmed() {
		return false
	}
	return true
}

// Component is the adapter's resolved load. Amount is a configured admission
// estimate. Start executes only after Acquire succeeds.
//
// Smaller are the smaller loads the same component can run instead, largest
// first, each with its own Amount and Start: a chat engine at a smaller
// context. With Smaller set the holder takes the largest load that fits what
// is free, asking nobody (AcquireFree), and asks holders to yield only for
// the smallest when none fits (CONTRACT.md RES-L9). A Smaller entry's own
// Smaller is ignored.
type Component struct {
	Amount  int64
	Start   func() (Owned, error)
	Smaller []Component
}

type Options struct {
	Leases            Leases
	Resource          string
	Idle, Renew, Wait time.Duration
	// IdleFor, when set, answers one component's own idle time; zero or less
	// keeps Idle. A longer idle time changes no yield: Yield still stops an
	// unused component at once.
	IdleFor func(name string) time.Duration
	Now     func() time.Time
	// Active must include pending admissions, executing calls and retained replies.
	// An adapter using Active must reserve activity before Ensure and every call
	// must traverse Ensure before using the component. Ensure and yield serialize
	// under the same component lock; future admissions reload after a yield.
	Active func(string) bool
	// InFlight, when set, counts the requests that run on a component now:
	// admissions not yet finished and calls still executing. A reply kept
	// only for observation is not in flight. A yield waits for this count,
	// not for Active, and while it waits every admission that calls Pass
	// waits too, so the yield happens at the component's next request
	// boundary (CONTRACT.md RES-L3). An adapter setting InFlight calls Pass
	// in each admission before Ensure.
	InFlight func(string) int
	OnError  func(error)
	// Admit, when set, is asked before every new load, ahead of prepare and
	// Acquire, and again after the grant and before Start; a refusal ends
	// Ensure with Admit's own error, and a grant already made goes back
	// unused. A resident component is not asked again. GPU fault
	// handling refuses a load while the adapter is resetting or the machine
	// is in Modern Standby (CONTRACT.md RES-G4, RES-G5).
	Admit func(ctx context.Context, name string) error
	// YieldWait bounds how long a yield waits for a component's active
	// requests to end before it answers refused; zero is half of Wait, so
	// an asker waiting as long as this holder would is answered before its
	// own deadline and asks again (acquireWaiting). A yield never stops a
	// component while a request uses it (RES-L3).
	YieldWait time.Duration
	// Label, when set, is the words a component's lease is labelled with
	// when it is granted, as app: model; "" leaves it unlabelled. A label
	// set through Holder.Label for that component wins (RES-L10).
	Label func(name string) string
}

// Lost is one component LoseAll found started: its name, whether a call was
// using it, and whether its stop was confirmed and its lease released.
type Lost struct {
	Name     string
	Active   bool
	Released bool
}

type Stage string

const (
	StageAcquire Stage = "acquire"
	StageStart   Stage = "start"
	StageReady   Stage = "ready"
	StageCancel  Stage = "cancel"
)

// Failure keeps the acquire outcome available for a provider's public refusal
// mapping. The lease refusal words below are the kit's; every other word a
// provider gives a failed load is its own.
type Failure struct {
	Stage      Stage
	Result     res.AcquireResult
	FreeResult *res.AcquireFreeResult
	Err        error
	// ForeignProgram and ForeignAmount are the largest foreign hold of the
	// resource, memory a program holds under no lease (CONTRACT.md RES-T1),
	// when the acquire read insufficient and the table names one; empty
	// otherwise.
	ForeignProgram string
	ForeignAmount  int64
}

// The refusal words every holder-backed provider gives a load its lease
// refused (modelhost CONTRACT.md MODELHOST-H2, CONTRACT.md RES-L4). This
// package is their one owner.
const (
	WordNotPermitted   = "lease:not_permitted"
	WordInsufficient   = "lease:insufficient"
	WordHoldersRefused = "lease:holders_refused"
	// WordEnded is a load whose lease the service ended while its
	// component readied, on the one reload too (ErrLeaseEnded).
	WordEnded = "lease:ended"
	// WordHeld is a speculative load the resource could not take without
	// a yield request: the no-yield acquire read held, the lease service
	// has none, or a yield cancelled the load (RES-L6).
	WordHeld = "lease:held"
)

// ErrSpeculativeCancelled is a speculative load a yield request for its
// lease cancelled (RES-L6).
var ErrSpeculativeCancelled = errors.New("a yield request cancelled the speculative load")

// ErrNoFreeAcquire is a speculative load against a lease service with no
// no-yield acquire.
var ErrNoFreeAcquire = errors.New("the lease service grants nothing without asking holders")

// Word is the lease refusal word of an acquire failure, or "" for a failure
// at another stage or an outcome no word names.
func (f *Failure) Word() string {
	if f == nil || f.Stage != StageAcquire {
		return ""
	}
	if errors.Is(f.Err, ErrLeaseEnded) {
		return WordEnded
	}
	if errors.Is(f.Err, ErrSpeculativeCancelled) || errors.Is(f.Err, ErrNoFreeAcquire) {
		return WordHeld
	}
	if f.FreeResult != nil {
		switch f.FreeResult.Outcome {
		case res.AcquireFreeOutcomeHeld:
			return WordHeld
		case res.AcquireFreeOutcomeNotPermitted:
			return WordNotPermitted
		case res.AcquireFreeOutcomeInsufficient:
			return WordInsufficient
		}
		return ""
	}
	switch f.Result.Outcome {
	case res.AcquireOutcomeNotPermitted:
		return WordNotPermitted
	case res.AcquireOutcomeInsufficient:
		return WordInsufficient
	case res.AcquireOutcomeHoldersRefused:
		return WordHoldersRefused
	}
	return ""
}

func (f *Failure) Error() string {
	if f.Err != nil {
		return fmt.Sprintf("holder %s: %v", f.Stage, f.Err)
	}
	if f.FreeResult != nil {
		return fmt.Sprintf("holder %s: %s", f.Stage, f.FreeResult.Outcome)
	}
	return fmt.Sprintf("holder %s: %s", f.Stage, f.Result.Outcome)
}
func (f *Failure) Unwrap() error { return f.Err }

type resident struct {
	owned  Owned
	lease  string
	amount int64
	last   time.Time
}
type slot struct {
	mu       sync.Mutex
	resident *resident
	active   int
	// started is the component Start returned, readable without mu so
	// LoseAll can stop one whose Ensure is still waiting for readiness.
	startedMu sync.Mutex
	started   Owned
	// speculative cancels the speculative load under way, under startedMu;
	// nil when none runs. A yield request calls it before it waits for the
	// component lock the load holds (RES-L6).
	speculative context.CancelFunc
	// liveWaiters includes detached callers waiting for the component lock.
	// Their trusted projections run outside holder locks.
	liveWaiters map[*liveWaiter]struct{}
	// gate, under startedMu, is open (nil) unless a yield waits for the
	// component's next request boundary; yielding counts those yields and
	// gated the admissions Pass holds back meanwhile (RES-L3).
	gate     chan struct{}
	yielding int
	gated    int
	// waiting counts the Ensure and Begin calls waiting for or holding the
	// component lock: real work that a speculative load under way now
	// serves, so a yield no longer cancels that load (RES-L6).
	waiting atomic.Int32
}

type liveWaiter struct{ live func() bool }

// setSpeculative records cancel as the speculative load under way, or none.
func (s *slot) setSpeculative(cancel context.CancelFunc) {
	s.startedMu.Lock()
	s.speculative = cancel
	s.startedMu.Unlock()
}

// cancelSpeculative cancels a speculative load under way and reports
// whether there was one.
func (s *slot) cancelSpeculative() bool {
	s.startedMu.Lock()
	defer s.startedMu.Unlock()
	if s.speculative == nil {
		return false
	}
	s.speculative()
	return true
}

func (s *slot) setStarted(o Owned) {
	s.startedMu.Lock()
	s.started = o
	s.startedMu.Unlock()
}

func (s *slot) startedOwned() Owned {
	s.startedMu.Lock()
	defer s.startedMu.Unlock()
	return s.started
}

// held is one lease this holder renews, from its grant until its release.
// Its lock spans one Renew or one Release call, never a load, so a renewal
// and a release of the same lease never overlap and no renewal follows a
// confirmed release.
type held struct {
	name string
	mu   sync.Mutex
	// gone is set, under mu, once the lease is released or handed back
	// unused; it is never renewed again.
	gone bool
	// ended is set, under the holder's mu, when the service refused a
	// renewal. The component unloads when its own lock is next free.
	ended bool
}

// Holder owns the lease lifecycle for independently loaded components. The
// configured amount is an admission estimate, never a measurement of use.
type Holder struct {
	opts    Options
	mu      sync.Mutex
	retryMu sync.Mutex
	slots   map[string]*slot
	// leases is every lease granted to a component and not yet released,
	// keyed by lease id. Renewal reads it under mu and never takes a
	// component lock: a component lock is held through a whole load.
	leases map[string]*held
	// loaded is every component name whose load finished and which has not
	// been unloaded since, under mu. Residents reads only it, so a
	// description never waits for a component lock a load holds.
	loaded map[string]bool
	// renewMu keeps renewal passes one at a time.
	renewMu sync.Mutex
	// sweeping is true while an idle and liveness pass runs.
	sweeping atomic.Bool
	// unused holds grants acquired before a component could start when
	// Release returned an error. They remain visible here for retry.
	unused map[string]struct{}
	// labels is each component's label set through Label, under mu; each
	// new lease of that component is labelled with it.
	labels    map[string]string
	closed    chan struct{}
	closeOnce sync.Once
}

func New(opts Options) (*Holder, error) {
	if opts.Leases == nil {
		return nil, errors.New("holder: leases are required")
	}
	if opts.Resource == "" {
		return nil, errors.New("holder: resource is required")
	}
	if opts.Idle <= 0 {
		opts.Idle = 5 * time.Minute
	}
	if opts.Renew <= 0 {
		opts.Renew = 8 * time.Second
	}
	if opts.Wait <= 0 {
		opts.Wait = 60 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.YieldWait <= 0 {
		opts.YieldWait = opts.Wait / 2
	}
	return &Holder{opts: opts, slots: map[string]*slot{}, leases: map[string]*held{}, loaded: map[string]bool{}, unused: map[string]struct{}{}, labels: map[string]string{}, closed: make(chan struct{})}, nil
}

// hold registers a granted lease for renewal from the moment of its grant,
// so a slow start or readiness keeps it alive.
func (h *Holder) hold(lease, name string) {
	h.mu.Lock()
	h.leases[lease] = &held{name: name}
	h.mu.Unlock()
}

// Label sets the words name's lease is labelled with, as app: model, for a
// person reading the machine's memory: its live lease now, and each lease a
// later load of name is granted (RES-L10). A label that cannot be written is
// reported and changes nothing else; it is display only.
func (h *Holder) Label(name, text string) {
	h.mu.Lock()
	h.labels[name] = text
	var live []string
	for lease, entry := range h.leases {
		if entry.name == name && !entry.gone {
			live = append(live, lease)
		}
	}
	h.mu.Unlock()
	for _, lease := range live {
		h.writeLabel(lease, name, text)
	}
}

// labelLease labels a lease just granted to name: the label set through
// Label, else Options.Label's words, else none.
func (h *Holder) labelLease(lease, name string) {
	h.mu.Lock()
	text, set := h.labels[name]
	h.mu.Unlock()
	if !set && h.opts.Label != nil {
		text = h.opts.Label(name)
	}
	if text == "" {
		return
	}
	h.writeLabel(lease, name, text)
}

func (h *Holder) writeLabel(lease, name, text string) {
	if _, err := h.opts.Leases.Label(lease, text); err != nil {
		h.report(fmt.Errorf("holder: labelling %s: %w", name, err))
	}
}

func (h *Holder) entry(lease string) *held {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.leases[lease]
}

// leaseEnded reports whether the service refused this lease's renewal.
func (h *Holder) leaseEnded(lease string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.leases[lease]
	return e != nil && e.ended
}

// release gives a lease back through its entry, so it never overlaps a
// renewal of the same lease. A lease released successfully leaves the
// registry; one whose release failed stays and is still renewed unless
// keep is false.
func (h *Holder) release(lease string, keep bool) error {
	e := h.entry(lease)
	if e != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.gone {
			return nil
		}
	}
	_, err := h.opts.Leases.Release(lease)
	if err == nil || !keep {
		if e != nil {
			e.gone = true
		}
		h.mu.Lock()
		if h.leases[lease] == e {
			delete(h.leases, lease)
		}
		h.mu.Unlock()
	}
	return err
}

func (h *Holder) get(name string) *slot {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slots[name]
	if s == nil {
		s = &slot{}
		h.slots[name] = s
	}
	return s
}

func (h *Holder) names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.slots))
	for name := range h.slots {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *Holder) Snapshot(name string) (lease string, loaded bool) {
	s := h.get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resident == nil {
		return "", false
	}
	return s.resident.lease, true
}
func (h *Holder) PID(name string) int {
	s := h.get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resident == nil {
		return 0
	}
	return s.resident.owned.PID()
}

// Residents names the loaded components without taking any component lock.
func (h *Holder) Residents() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var resident []string
	for name := range h.loaded {
		resident = append(resident, name)
	}
	sort.Strings(resident)
	return resident
}

// Begin reserves one use before Ensure. An adapter without an external
// admission tracker keeps end until its call and retained response finish.
// Begin waits for an in-progress yield; the caller must then pass through
// Ensure before touching the component.
func (h *Holder) Begin(ctx context.Context, name string) (end func(), err error) {
	if name == "" {
		return nil, errors.New("holder: component name is required")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s := h.get(name)
	s.waiting.Add(1)
	defer s.waiting.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-h.closed:
		return nil, errors.New("holder: closed")
	default:
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.active--
			s.mu.Unlock()
		})
	}, nil
}

// Pass is an admission's way past a yield in progress: it returns at once
// unless a yield waits for the component's next request boundary, and then
// once that yield is answered (RES-L3). The admission is counted by
// InFlight from before Pass until it ends; while Pass holds it back, the
// yield does not count it as using the component.
func (h *Holder) Pass(ctx context.Context, name string) error {
	s := h.get(name)
	s.startedMu.Lock()
	for s.gate != nil {
		gate := s.gate
		s.gated++
		s.startedMu.Unlock()
		var err error
		select {
		case <-gate:
		case <-ctx.Done():
			err = ctx.Err()
		case <-h.closed:
			err = errors.New("holder: closed")
		}
		s.startedMu.Lock()
		s.gated--
		if err != nil {
			s.startedMu.Unlock()
			return err
		}
	}
	s.startedMu.Unlock()
	return nil
}

// raiseGate holds back admissions that call Pass until lowerGate.
func (s *slot) raiseGate() {
	s.startedMu.Lock()
	if s.yielding == 0 {
		s.gate = make(chan struct{})
	}
	s.yielding++
	s.startedMu.Unlock()
}

func (s *slot) lowerGate() {
	s.startedMu.Lock()
	if s.yielding--; s.yielding == 0 {
		close(s.gate)
		s.gate = nil
	}
	s.startedMu.Unlock()
}

// inUse reports whether a request runs on the component now, for a yield
// and for an eviction: a use Begin reserved, or, with InFlight, a request
// in flight that Pass does not hold back; without InFlight, Active. The
// caller holds s.mu. The held-back count is read before InFlight, so an
// admission that passes between the two reads is still counted.
func (h *Holder) inUse(name string, s *slot) bool {
	if s.active > 0 {
		return true
	}
	if h.opts.InFlight == nil {
		return h.opts.Active != nil && h.opts.Active(name)
	}
	s.startedMu.Lock()
	gated := s.gated
	s.startedMu.Unlock()
	return h.opts.InFlight(name) > gated
}

// Ensure serializes a component's acquisition and load with its yield. start
// runs only after a grant, and must return an instance owned by this provider.
func (h *Holder) Ensure(ctx context.Context, name string, prepare func() (Component, error)) error {
	return h.ensure(ctx, name, prepare, nil)
}

// EnsureWithWaiter retains Ensure's acquisition policy while projecting actual
// foreground waiters for a detached load. live must be nonblocking, perform no
// I/O and return true for unknown state. Active work also prevents yielding.
// A new waiter arriving after speculative cancellation uses normal Ensure again.
func (h *Holder) EnsureWithWaiter(ctx context.Context, name string, prepare func() (Component, error), live func() bool) error {
	return h.ensure(ctx, name, prepare, live)
}

func (h *Holder) ensure(ctx context.Context, name string, prepare func() (Component, error), live func() bool) error {
	ownerCtx := ctx
	if name == "" || prepare == nil {
		return &Failure{Stage: StageStart, Err: errors.New("name and prepare are required")}
	}
	s := h.get(name)
	if live == nil {
		s.waiting.Add(1)
		defer s.waiting.Add(-1)
	} else {
		waiter := &liveWaiter{live: live}
		s.startedMu.Lock()
		if s.liveWaiters == nil {
			s.liveWaiters = make(map[*liveWaiter]struct{})
		}
		s.liveWaiters[waiter] = struct{}{}
		s.startedMu.Unlock()
		defer func() { s.startedMu.Lock(); delete(s.liveWaiters, waiter); s.startedMu.Unlock() }()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if live != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		s.startedMu.Lock()
		s.speculative = cancel
		s.startedMu.Unlock()
		defer s.setSpeculative(nil)
	}
	select {
	case <-h.closed:
		return &Failure{Stage: StageCancel, Err: errors.New("holder closed")}
	default:
	}
	if ctx.Err() != nil {
		return &Failure{Stage: StageCancel, Err: ctx.Err()}
	}
	if r := s.resident; r != nil {
		why := "component exited"
		if h.leaseEnded(r.lease) {
			why = "lease ended"
		} else if r.owned.Alive() {
			r.last = h.opts.Now()
			return nil
		}
		if !h.unloadLocked(name, s, why) {
			return &Failure{Stage: StageStart, Err: errors.New("previous lease could not be released")}
		}
	}
	// A lease the service refuses while the component readies is unloaded
	// here and loaded once more under a new lease; a second refusal is an
	// acquire failure wrapping ErrLeaseEnded.
	for attempt := 1; ; attempt++ {
		lease, err := h.loadLocked(ctx, name, s, prepare, func(amount int64) (*res.Lease, error) {
			return h.acquireWaiting(ctx, amount)
		}, false)
		if err != nil {
			if live != nil && ctx.Err() != nil && ownerCtx.Err() == nil {
				return &Failure{Stage: StageCancel, Err: ErrSpeculativeCancelled}
			}
			return err
		}
		if !h.leaseEnded(lease) {
			return nil
		}
		if !h.unloadLocked(name, s, "lease ended") {
			return &Failure{Stage: StageStart, Err: errors.New("lease ended and the component could not be released")}
		}
		if attempt == 2 {
			return &Failure{Stage: StageAcquire, Err: ErrLeaseEnded}
		}
	}
}

// acquireWaiting is Ensure's acquire: holders are asked to yield within the
// configured wait (RES-L2). A holder that refused only because a request was
// still using its component yields once that request ends (RES-L3), so the
// acquire is asked again while ctx lasts, up to busyRetries more times; any
// other refusal is the answer.
func (h *Holder) acquireWaiting(ctx context.Context, amount int64) (*res.Lease, error) {
	result, err := h.opts.Leases.Acquire(h.opts.Resource, amount, h.opts.Wait)
	for retry := 0; retry < busyRetries && err == nil && ctx.Err() == nil && heldByARequest(result); retry++ {
		result, err = h.opts.Leases.Acquire(h.opts.Resource, amount, h.opts.Wait)
	}
	if err != nil || result.Outcome != res.AcquireOutcomeAcquired {
		failure := &Failure{Stage: StageAcquire, Result: result, Err: err}
		if err == nil && result.Outcome == res.AcquireOutcomeInsufficient {
			failure.ForeignProgram, failure.ForeignAmount = h.foreign(ctx)
		}
		return nil, failure
	}
	if result.Lease == nil || result.Lease.ID == "" {
		return nil, &Failure{Stage: StageAcquire, Result: result, Err: errors.New("acquired without lease ID")}
	}
	return result.Lease, nil
}

// foreign is the largest measured row of the holder's resource that no lease
// covers and that is not this program's own, or "" when the table cannot be
// read, shows only this program's own rows, or shows no foreign memory.
func (h *Holder) foreign(ctx context.Context) (program string, amount int64) {
	table, ok := h.opts.Leases.(tableReader)
	if !ok {
		return "", 0
	}
	state, err := table.Holders(h.opts.Resource, false)
	if err != nil {
		return "", 0
	}
	//unchecked: without its own path the holder excludes no row of its own
	executable, _ := os.Executable()
	var self fileid.FileID
	if executable != "" {
		if image, err := fileid.Open(ctx, fileid.Self, executable, fileid.Options{}); err == nil {
			defer image.Close()
			self = image.FileID()
		}
	}
	for _, row := range state.Holders {
		if row.Evidence != res.EvidenceVerified || row.Lease != "" || row.Amount <= 0 || row.Program == "" {
			continue
		}
		if !self.IsZero() && filepath.IsAbs(row.Program) {
			if observed, err := fileid.Resolve(ctx, fileid.Self, row.Program, fileid.Options{}); err == nil && observed.Equal(self) {
				continue
			}
		}
		if row.Amount > amount {
			program, amount = row.Program, row.Amount
		}
	}
	return program, amount
}

// busyReason is a holder's refusal of a yield while a request uses the
// component; busyRetries bounds how many more acquires follow such refusals.
const (
	busyReason  = "component has an active request"
	busyRetries = 10
)

// heldByARequest reports whether result was refused only by holders whose
// components were in use: every refusal it records is busyReason.
func heldByARequest(result res.AcquireResult) bool {
	if result.Outcome != res.AcquireOutcomeHoldersRefused || len(result.Asked) == 0 {
		return false
	}
	busy := false
	for _, asked := range result.Asked {
		switch {
		case strings.HasSuffix(asked.Answer, busyReason):
			busy = true
		case asked.Answer == "yielded" || asked.Answer == "unanswered":
		default:
			return false
		}
	}
	return busy
}

// EnsureSpeculative makes name resident for speculative work, which holds
// the lowest standing (CONTRACT.md RES-L6). A resident component is left as
// it is: its idle clock is not touched. A component not resident is loaded
// only through the no-yield acquire, under the component lock, so nothing
// between a residency check and the acquire can turn into a yield request.
// Any failure refuses, a prepare error and a refused acquire alike; nothing
// falls back to Ensure. A yield request for the load's lease cancels the
// load, and the yield is answered once its lease is released. The load
// reserves no activity: callers do not call Begin for it.
func (h *Holder) EnsureSpeculative(ctx context.Context, name string, prepare func() (Component, error)) error {
	if name == "" || prepare == nil {
		return &Failure{Stage: StageStart, Err: errors.New("name and prepare are required")}
	}
	free, ok := h.opts.Leases.(FreeLeases)
	if !ok {
		return &Failure{Stage: StageAcquire, Err: ErrNoFreeAcquire}
	}
	s := h.get(name)
	loadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-h.closed:
		return &Failure{Stage: StageCancel, Err: errors.New("holder closed")}
	default:
	}
	if ctx.Err() != nil {
		return &Failure{Stage: StageCancel, Err: ctx.Err()}
	}
	if r := s.resident; r != nil {
		if !h.leaseEnded(r.lease) && r.owned.Alive() {
			return nil
		}
		why := "component exited"
		if h.leaseEnded(r.lease) {
			why = "lease ended"
		}
		if !h.unloadLocked(name, s, why) {
			return &Failure{Stage: StageStart, Err: errors.New("previous lease could not be released")}
		}
	}
	s.setSpeculative(cancel)
	defer s.setSpeculative(nil)
	lease, err := h.loadLocked(loadCtx, name, s, prepare, func(amount int64) (*res.Lease, error) {
		return h.acquireFree(free, amount)
	}, true)
	if err != nil {
		if loadCtx.Err() != nil && ctx.Err() == nil {
			return &Failure{Stage: StageAcquire, Err: ErrSpeculativeCancelled}
		}
		return err
	}
	if h.leaseEnded(lease) {
		if !h.unloadLocked(name, s, "lease ended") {
			return &Failure{Stage: StageStart, Err: errors.New("lease ended and the component could not be released")}
		}
		return &Failure{Stage: StageAcquire, Err: ErrLeaseEnded}
	}
	return nil
}

// acquireFree is the no-yield acquire of amount (RES-L6) as a holder
// Failure: a resource service without it reads ErrNoFreeAcquire.
func (h *Holder) acquireFree(free FreeLeases, amount int64) (*res.Lease, error) {
	result, err := free.AcquireFree(h.opts.Resource, amount)
	var refused *res.ServiceError
	if errors.As(err, &refused) && (refused.Code == res.ServiceErrorCodeUnknownMethod || refused.Code == res.ServiceErrorCodeUnknownService) {
		// A resource service older than RES-L6 has no no-yield acquire.
		return nil, &Failure{Stage: StageAcquire, FreeResult: &result, Err: ErrNoFreeAcquire}
	}
	if err != nil || result.Outcome != res.AcquireFreeOutcomeAcquired {
		return nil, &Failure{Stage: StageAcquire, FreeResult: &result, Err: err}
	}
	if result.Lease == nil || result.Lease.ID == "" {
		return nil, &Failure{Stage: StageAcquire, FreeResult: &result, Err: errors.New("acquired without lease ID")}
	}
	return result.Lease, nil
}

// ErrLeaseEnded is wrapped by the acquire Failure Ensure returns when the
// service ended the lease while its component was loading, twice in a row.
var ErrLeaseEnded = errors.New("the lease ended while the component was loading")

// loadLocked acquires a lease and starts and readies one component under
// s.mu. It returns the lease the resident component holds. speculative says
// acquire asks nobody already, so a component's smaller loads are each tried
// through it alone.
func (h *Holder) loadLocked(ctx context.Context, name string, s *slot, prepare func() (Component, error), acquire func(int64) (*res.Lease, error), speculative bool) (string, error) {
	if h.opts.Admit != nil {
		if err := h.opts.Admit(ctx, name); err != nil {
			return "", err
		}
	}
	component, err := prepare()
	if err != nil {
		return "", err
	}
	component, result, err := h.admitSized(component, acquire, speculative)
	if err != nil {
		return "", err
	}
	amount := component.Amount
	lease := result.ID
	h.hold(lease, name)
	h.labelLease(lease, name)
	if ctx.Err() != nil {
		h.releaseUnused(lease)
		return "", &Failure{Stage: StageCancel, Err: ctx.Err()}
	}
	// The lease wait can be long; a reset or a standby entry that began
	// during it refuses the start, and the grant goes back unused
	// (RES-G4, RES-G5).
	if h.opts.Admit != nil {
		if err := h.opts.Admit(ctx, name); err != nil {
			h.releaseUnused(lease)
			return "", err
		}
	}
	owned, err := component.Start()
	if err != nil {
		h.releaseUnused(lease)
		return "", &Failure{Stage: StageStart, Err: err}
	}
	if owned == nil {
		h.releaseUnused(lease)
		return "", &Failure{Stage: StageStart, Err: errors.New("start returned no owned component")}
	}
	r := &resident{owned: owned, lease: lease, amount: amount, last: h.opts.Now()}
	s.resident = r
	s.setStarted(owned)
	if pid := owned.PID(); pid > 0 {
		binding, bindErr := h.opts.Leases.BindProcess(lease, pid)
		if bindErr != nil {
			h.report(fmt.Errorf("holder: binding %s: %w", name, bindErr))
		} else if binding.Outcome != res.ProcessBindOutcomeBound {
			h.report(fmt.Errorf("holder: binding %s: %s", name, binding.Outcome))
		}
	}
	if err := owned.Ready(ctx); err != nil {
		if !h.unloadLocked(name, s, "readiness failed") {
			h.report(fmt.Errorf("holder: %s could not be released after failed readiness", name))
		}
		return "", &Failure{Stage: StageReady, Err: err}
	}
	// The idle clock starts when the component is ready, so a load longer
	// than Idle is not unloaded as idle before its first use.
	r.last = h.opts.Now()
	h.mu.Lock()
	h.loaded[name] = true
	h.mu.Unlock()
	return lease, nil
}

// admitSized acquires a lease for component or, where it names smaller
// loads, for the largest of them that fits (RES-L9): each load largest first
// through the no-yield acquire, and only when none fits, acquire's own ask
// for the smallest. A component without smaller loads is acquired as it is.
// A lease service without the no-yield acquire, or a refusal other than
// held or insufficient, goes straight to acquire with the largest load.
func (h *Holder) admitSized(component Component, acquire func(int64) (*res.Lease, error), speculative bool) (Component, *res.Lease, error) {
	loads := []Component{{Amount: component.Amount, Start: component.Start}}
	for _, smaller := range component.Smaller {
		loads = append(loads, Component{Amount: smaller.Amount, Start: smaller.Start})
	}
	for _, load := range loads {
		if load.Amount <= 0 || load.Start == nil {
			return Component{}, nil, &Failure{Stage: StageStart, Err: errors.New("positive amount and start are required")}
		}
	}
	if len(loads) == 1 {
		lease, err := acquire(loads[0].Amount)
		return loads[0], lease, err
	}
	free, ok := h.opts.Leases.(FreeLeases)
	if speculative || !ok {
		free = nil
	}
	var last error
	for _, load := range loads {
		var lease *res.Lease
		var err error
		if speculative {
			lease, err = acquire(load.Amount)
		} else if free != nil {
			lease, err = h.acquireFree(free, load.Amount)
		} else {
			break
		}
		if err == nil {
			return load, lease, nil
		}
		last = err
		var failure *Failure
		if !errors.As(err, &failure) || failure.FreeResult == nil ||
			failure.FreeResult.Outcome != res.AcquireFreeOutcomeHeld && failure.FreeResult.Outcome != res.AcquireFreeOutcomeInsufficient {
			if speculative {
				return Component{}, nil, err
			}
			lease, err := acquire(loads[0].Amount)
			return loads[0], lease, err
		}
	}
	if speculative {
		return Component{}, nil, last
	}
	if free == nil {
		lease, err := acquire(loads[0].Amount)
		return loads[0], lease, err
	}
	smallest := loads[len(loads)-1]
	lease, err := acquire(smallest.Amount)
	return smallest, lease, err
}

// releaseUnused hands back a grant no component used. It is never renewed
// again; a failed release is retried by keep.
func (h *Holder) releaseUnused(lease string) {
	if err := h.release(lease, false); err != nil {
		h.report(fmt.Errorf("holder: releasing unused %s: %w", lease, err))
		h.mu.Lock()
		h.unused[lease] = struct{}{}
		h.mu.Unlock()
	}
}

func (h *Holder) retryUnused() {
	h.retryMu.Lock()
	defer h.retryMu.Unlock()
	h.mu.Lock()
	leases := make([]string, 0, len(h.unused))
	for lease := range h.unused {
		leases = append(leases, lease)
	}
	h.mu.Unlock()
	for _, lease := range leases {
		if _, err := h.opts.Leases.Release(lease); err != nil {
			h.report(fmt.Errorf("holder: retry releasing unused %s: %w", lease, err))
			continue
		}
		h.mu.Lock()
		delete(h.unused, lease)
		h.mu.Unlock()
	}
}

// unloadLocked preserves the lease record when stopping or release cannot be
// confirmed. In particular, no yielded answer follows a failed Release.
func (h *Holder) unloadLocked(name string, s *slot, why string) bool {
	r := s.resident
	if r == nil {
		return false
	}
	if !stopped(r.owned) {
		h.report(fmt.Errorf("holder: %s did not stop after %s, or its exit is unconfirmed; its lease stays held", name, why))
		return false
	}
	if err := h.release(r.lease, true); err != nil {
		h.report(fmt.Errorf("holder: releasing %s after %s: %w", name, why, err))
		return false
	}
	s.resident = nil
	s.setStarted(nil)
	h.mu.Lock()
	delete(h.loaded, name)
	h.mu.Unlock()
	return true
}
func (h *Holder) Unload(name, why string) bool {
	s := h.get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	return h.unloadLocked(name, s, why)
}

// UnloadIdle unloads name like Unload only while nothing uses it: no load or
// call holds the component, and Active reports no request. The check and the
// unload happen under the component lock, so a call that reserved activity
// first keeps its component. busy reports a component in use, which the
// caller asks again later; unloaded false with busy false means nothing was
// resident or the stop could not be confirmed.
func (h *Holder) UnloadIdle(name, why string) (unloaded, busy bool) {
	s := h.get(name)
	if !s.mu.TryLock() {
		return false, true
	}
	defer s.mu.Unlock()
	if s.resident == nil {
		return false, false
	}
	if h.active(name, s) {
		return false, true
	}
	return h.unloadLocked(name, s, why), false
}

// LoseAll treats every started component as lost, the way a GPU reset
// leaves every engine on the adapter (CONTRACT.md RES-G2). Each is first
// stopped by its own Owned.Stop, outside the component lock, so a load still
// waiting for readiness ends at once; then, under the lock, its lease is
// released. A component whose stop or release cannot be confirmed keeps its
// lease record, as Unload does, and reads Released false.
func (h *Holder) LoseAll(why string) []Lost {
	var lost []Lost
	for _, name := range h.names() {
		s := h.get(name)
		owned := s.startedOwned()
		if owned == nil {
			continue
		}
		//unchecked: the confirmation is read under the lock below, where the lease is released only after a confirmed stop
		owned.Stop()
		s.mu.Lock()
		entry := Lost{Name: name, Active: h.active(name, s)}
		if s.resident == nil || s.resident.owned != owned {
			// The load's own readiness failure already stopped it and gave
			// its lease back, or a later load replaced it.
			entry.Released = s.startedOwned() != owned
		} else {
			entry.Released = h.unloadLocked(name, s, why)
		}
		s.mu.Unlock()
		lost = append(lost, entry)
	}
	return lost
}

// renew renews every granted lease once, from the registry alone. A
// component in the middle of a load, a start or a stop never delays it. A
// lease the service refuses is marked ended and is not renewed again; sweep
// or the next Ensure unloads its component.
func (h *Holder) renew() {
	h.renewMu.Lock()
	defer h.renewMu.Unlock()
	h.mu.Lock()
	ids := make([]string, 0, len(h.leases))
	for id, e := range h.leases {
		if !e.ended {
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		e := h.entry(id)
		if e == nil {
			continue
		}
		e.mu.Lock()
		if e.gone {
			e.mu.Unlock()
			continue
		}
		change, err := h.opts.Leases.Renew(id)
		e.mu.Unlock()
		if err != nil {
			h.report(fmt.Errorf("holder: renewing %s: %w", e.name, err))
			continue
		}
		if !change.Applied {
			h.mu.Lock()
			if h.leases[id] == e {
				e.ended = true
			}
			h.mu.Unlock()
		}
	}
}

// sweep checks each component whose lock is free. A component in the middle
// of Ensure, a yield or an unload is skipped and checked on the next pass.
func (h *Holder) sweep() {
	for _, name := range h.names() {
		s := h.get(name)
		if !s.mu.TryLock() {
			continue
		}
		h.check(name, s)
		s.mu.Unlock()
	}
}

func (h *Holder) check(name string, s *slot) {
	r := s.resident
	if r == nil {
		return
	}
	if h.leaseEnded(r.lease) {
		h.unloadLocked(name, s, "lease ended")
		return
	}
	if !r.owned.Alive() {
		h.unloadLocked(name, s, "component exited")
		return
	}
	if h.opts.Now().Sub(r.last) >= h.idleFor(name) {
		if h.active(name, s) {
			r.last = h.opts.Now()
		} else {
			h.unloadLocked(name, s, "idle")
		}
	}
}

// idleFor is name's idle time: IdleFor's answer when positive, else Idle.
func (h *Holder) idleFor(name string) time.Duration {
	if h.opts.IdleFor != nil {
		if d := h.opts.IdleFor(name); d > 0 {
			return d
		}
	}
	return h.opts.Idle
}

func (h *Holder) active(name string, s *slot) bool {
	return s.active > 0 || (h.opts.Active != nil && h.opts.Active(name))
}

// Yield answers only for the exact requested lease after the component stops
// and its lease release call succeeds. The service then checks its instrument.
func (h *Holder) Yield(request res.YieldRequest) (res.YieldAnswer, string) {
	if request.Resource != h.opts.Resource || request.Lease == "" {
		return res.YieldAnswerRefused, "resource or lease does not match this provider"
	}
	// The registry names the one component that holds the lease, so a yield
	// waits only for that component's lock.
	e := h.entry(request.Lease)
	if e == nil {
		return res.YieldAnswerRefused, "lease is not resident"
	}
	name := e.name
	s := h.get(name)
	// A speculative load holds the component lock for its whole load and
	// yields to anyone (RES-L6): cancel it first, so this yield waits only
	// for it to stop. Once real work waits on the component, the load is
	// that work's own and holds a normal standing: it is not cancelled, and
	// the yield is decided below as for any holder.
	cancelled := false
	s.startedMu.Lock()
	live := make([]func() bool, 0, len(s.liveWaiters))
	for waiter := range s.liveWaiters {
		live = append(live, waiter.live)
	}
	s.startedMu.Unlock()
	foreground := false
	for _, waiting := range live {
		if waiting() {
			foreground = true
			break
		}
	}
	if !foreground && s.waiting.Load() == 0 && (h.opts.Active == nil || !h.opts.Active(name)) {
		cancelled = s.cancelSpeculative()
	}
	// A request using the component is never cut short: the yield waits,
	// without the component lock, for a moment no request uses it, up to
	// YieldWait (RES-L3). With InFlight, admissions that call Pass wait
	// behind the yield meanwhile, so it happens when the running request
	// ends, before the next one starts; a reply kept only for observation
	// does not hold it.
	if h.opts.InFlight != nil {
		s.raiseGate()
		defer s.lowerGate()
	}
	deadline := time.Now().Add(h.opts.YieldWait)
	for {
		s.mu.Lock()
		r := s.resident
		if cancelled && r == nil && h.entry(request.Lease) == nil {
			// The cancelled load stopped its component and released the lease.
			s.mu.Unlock()
			return res.YieldAnswerYielded, ""
		}
		if r == nil || r.lease != request.Lease {
			s.mu.Unlock()
			return res.YieldAnswerRefused, "lease is not resident"
		}
		if r.amount != request.Amount {
			s.mu.Unlock()
			return res.YieldAnswerRefused, "amount does not match the lease"
		}
		if !h.inUse(name, s) {
			stoppedOK := h.unloadLocked(name, s, "yield")
			s.mu.Unlock()
			if !stoppedOK {
				return res.YieldAnswerRefused, "component did not stop or lease release failed"
			}
			return res.YieldAnswerYielded, ""
		}
		s.mu.Unlock()
		if !time.Now().Before(deadline) {
			return res.YieldAnswerRefused, busyReason
		}
		select {
		case <-h.closed:
			return res.YieldAnswerRefused, "holder closed"
		case <-time.After(yieldPoll):
		}
	}
}

// yieldPoll is how often a waiting yield looks for a moment the component's
// requests have ended.
var yieldPoll = 50 * time.Millisecond

func (h *Holder) Run(ctx context.Context) {
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); h.keep(ctx) }()
	go func() { defer group.Done(); h.observe(ctx) }()
	group.Wait()
}

// keep renews every lease each Renew interval and starts one sweep at a
// time beside it, so a slow stop in a sweep never delays a renewal.
func (h *Holder) keep(ctx context.Context) {
	ticker := time.NewTicker(h.opts.Renew)
	defer ticker.Stop()
	var sweeps sync.WaitGroup
	defer sweeps.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.closed:
			return
		case <-ticker.C:
		}
		h.renew()
		if h.sweeping.CompareAndSwap(false, true) {
			sweeps.Add(1)
			go func() {
				defer sweeps.Done()
				defer h.sweeping.Store(false)
				h.sweep()
				h.retryUnused()
			}()
		}
	}
}
func (h *Holder) observe(ctx context.Context) {
	cursor := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.closed:
			return
		default:
		}
		page, err := h.opts.Leases.Observe(ctx, cursor, 25*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var refusal *res.ServiceError
			if errors.As(err, &refusal) && refusal.Code == res.ServiceErrorCodeInvalidRequest {
				if cursor != "" {
					// A service restart expires its opaque observation cursor.
					// Reobserve once from the beginning under the same admission.
					cursor = ""
					continue
				}
				h.report(fmt.Errorf("holder: observing yield without a cursor: %w", err))
				return
			}
			h.report(fmt.Errorf("holder: observing yield: %w", err))
			select {
			case <-ctx.Done():
				return
			case <-h.closed:
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if page.Next != "" {
			cursor = page.Next
		}
		// Each request is answered on its own: a yield that waits for one
		// component's request to end delays no other component's answer.
		for _, request := range page.Requests {
			go func(request res.YieldRequest) {
				answer, reason := h.Yield(request)
				if _, err := h.opts.Leases.Answer(ctx, request.ID, answer, reason); err != nil {
					h.report(fmt.Errorf("holder: answering %s: %w", request.ID, err))
				}
			}(request)
		}
	}
}
func (h *Holder) Close() {
	h.closeOnce.Do(func() { close(h.closed) })
	for _, name := range h.names() {
		h.Unload(name, "provider stopping")
	}
	h.retryUnused()
}
func (h *Holder) report(err error) {
	if h.opts.OnError != nil && err != nil {
		h.opts.OnError(err)
	}
}
