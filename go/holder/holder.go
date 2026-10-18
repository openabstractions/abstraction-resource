// Package holder coordinates a provider's owned components with resource leases.
// The resource service remains the authority for grants and verified release.
package holder

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
)

// Leases is the holder side of resource/leases@1.
type Leases interface {
	Acquire(string, int64, time.Duration) (res.AcquireResult, error)
	BindProcess(string, int) (res.ProcessBindResult, error)
	Renew(string) (res.LeaseChange, error)
	Release(string) (res.LeaseChange, error)
	Observe(context.Context, string, time.Duration) (res.YieldPage, error)
	Answer(context.Context, string, res.YieldAnswer, string) (res.AnswerResult, error)
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
type Component struct {
	Amount int64
	Start  func() (Owned, error)
}

type Options struct {
	Leases            Leases
	Resource          string
	Idle, Renew, Wait time.Duration
	Now               func() time.Time
	// Active must include pending admissions, executing calls and retained replies.
	// An adapter using Active must reserve activity before Ensure and every call
	// must traverse Ensure before using the component. Ensure and yield serialize
	// under the same component lock; future admissions reload after a yield.
	Active  func(string) bool
	OnError func(error)
	// Admit, when set, is asked before every new load, ahead of prepare and
	// Acquire, and again after the grant and before Start; a refusal ends
	// Ensure with Admit's own error, and a grant already made goes back
	// unused. A resident component is not asked again. GPU fault
	// handling refuses a load while the adapter is resetting or the machine
	// is in Modern Standby (CONTRACT.md RES-G4, RES-G5).
	Admit func(ctx context.Context, name string) error
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
	// waiting counts the Ensure and Begin calls waiting for or holding the
	// component lock: real work that a speculative load under way now
	// serves, so a yield no longer cancels that load (RES-L6).
	waiting atomic.Int32
}

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
	// renewMu keeps renewal passes one at a time.
	renewMu sync.Mutex
	// sweeping is true while an idle and liveness pass runs.
	sweeping atomic.Bool
	// unused holds grants acquired before a component could start when
	// Release returned an error. They remain visible here for retry.
	unused    map[string]struct{}
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
	return &Holder{opts: opts, slots: map[string]*slot{}, leases: map[string]*held{}, unused: map[string]struct{}{}, closed: make(chan struct{})}, nil
}

// hold registers a granted lease for renewal from the moment of its grant,
// so a slow start or readiness keeps it alive.
func (h *Holder) hold(lease, name string) {
	h.mu.Lock()
	h.leases[lease] = &held{name: name}
	h.mu.Unlock()
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
func (h *Holder) Residents() []string {
	var resident []string
	for _, name := range h.names() {
		_, loaded := h.Snapshot(name)
		if loaded {
			resident = append(resident, name)
		}
	}
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

// Ensure serializes a component's acquisition and load with its yield. start
// runs only after a grant, and must return an instance owned by this provider.
func (h *Holder) Ensure(ctx context.Context, name string, prepare func() (Component, error)) error {
	if name == "" || prepare == nil {
		return &Failure{Stage: StageStart, Err: errors.New("name and prepare are required")}
	}
	s := h.get(name)
	s.waiting.Add(1)
	defer s.waiting.Add(-1)
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
		lease, err := h.loadLocked(ctx, name, s, prepare, h.acquireWaiting)
		if err != nil {
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
// configured wait (RES-L2).
func (h *Holder) acquireWaiting(amount int64) (*res.Lease, error) {
	result, err := h.opts.Leases.Acquire(h.opts.Resource, amount, h.opts.Wait)
	if err != nil || result.Outcome != res.AcquireOutcomeAcquired {
		return nil, &Failure{Stage: StageAcquire, Result: result, Err: err}
	}
	if result.Lease == nil || result.Lease.ID == "" {
		return nil, &Failure{Stage: StageAcquire, Result: result, Err: errors.New("acquired without lease ID")}
	}
	return result.Lease, nil
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
	})
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

// ErrLeaseEnded is wrapped by the acquire Failure Ensure returns when the
// service ended the lease while its component was loading, twice in a row.
var ErrLeaseEnded = errors.New("the lease ended while the component was loading")

// loadLocked acquires a lease and starts and readies one component under
// s.mu. It returns the lease the resident component holds.
func (h *Holder) loadLocked(ctx context.Context, name string, s *slot, prepare func() (Component, error), acquire func(int64) (*res.Lease, error)) (string, error) {
	if h.opts.Admit != nil {
		if err := h.opts.Admit(ctx, name); err != nil {
			return "", err
		}
	}
	component, err := prepare()
	if err != nil {
		return "", err
	}
	if component.Amount <= 0 || component.Start == nil {
		return "", &Failure{Stage: StageStart, Err: errors.New("positive amount and start are required")}
	}
	amount := component.Amount
	result, err := acquire(amount)
	if err != nil {
		return "", err
	}
	lease := result.ID
	h.hold(lease, name)
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
	return lease, nil
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
	return true
}
func (h *Holder) Unload(name, why string) bool {
	s := h.get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	return h.unloadLocked(name, s, why)
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
	if h.opts.Now().Sub(r.last) >= h.opts.Idle {
		if h.active(name, s) {
			r.last = h.opts.Now()
		} else {
			h.unloadLocked(name, s, "idle")
		}
	}
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
	if s.waiting.Load() == 0 && (h.opts.Active == nil || !h.opts.Active(name)) {
		cancelled = s.cancelSpeculative()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.resident
	if cancelled && r == nil && h.entry(request.Lease) == nil {
		// The cancelled load stopped its component and released the lease.
		return res.YieldAnswerYielded, ""
	}
	if r == nil || r.lease != request.Lease {
		return res.YieldAnswerRefused, "lease is not resident"
	}
	if r.amount != request.Amount {
		return res.YieldAnswerRefused, "amount does not match the lease"
	}
	if h.active(name, s) {
		return res.YieldAnswerRefused, "component has an active request"
	}
	if !h.unloadLocked(name, s, "yield") {
		return res.YieldAnswerRefused, "component did not stop or lease release failed"
	}
	return res.YieldAnswerYielded, ""
}

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
		for _, request := range page.Requests {
			answer, reason := h.Yield(request)
			if _, err := h.opts.Leases.Answer(ctx, request.ID, answer, reason); err != nil {
				h.report(fmt.Errorf("holder: answering %s: %w", request.ID, err))
			}
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
