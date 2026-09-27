package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// The rights rules of abstraction.resource/leases@1.
//
// ActionHold is decided for the asking program on the resource it asks for,
// before the table is read (CONTRACT.md RES-L1). ResourceAwake is the wake
// hold's resource, and AwakeRight is the word rights carried for it before the
// hold became a lease: both name this one decision (RES-A1).
//
// ActionYield is decided for the service's own program on host:<name> before
// an attached holder is yielded on its behalf. The holder itself proves
// nothing to this service — it predates OA and answers no lease call — so the
// rule that says whether its models may be unloaded is a rule about that host,
// held by the service that would do the unloading (RES-L3).
const (
	ActionHold    = "abstraction.resource/hold"
	ActionYield   = "abstraction.resource/yield"
	ResourceAwake = "awake"
	AwakeRight    = "awake"
)

// The answers a YieldRecord carries. A refusal appends ": " and its reason.
const (
	AnswerYielded    = "yielded"
	AnswerRefused    = "refused"
	AnswerUnanswered = "unanswered"
)

// MaxAcquireWait and MaxObserveWait are the contract's bounds on wait_ms.
const (
	MaxAcquireWait = 120000 * time.Millisecond
	MaxObserveWait = 30000 * time.Millisecond
)

// DefaultLifetime is how long a lease stands without a renewal (RES-L4). It is
// four times the slowest unload
// research/resources/MEASUREMENT-2026-09-22.md timed, so a holder that has
// just been granted bytes has time to occupy them before it must say it is
// still there.
const DefaultLifetime = 30 * time.Second

// DefaultSettle is how long the service waits for an attached holder's bytes
// to leave the instrument after its host answered. Both unloads measured on
// 2026-09-22 froze on Windows' counters within one poll of the HTTP response,
// 7.7-7.9 s; twice that is a bound a slower model still fits inside.
const DefaultSettle = 16 * time.Second

// settlePoll is how often the instrument is re-read while bytes are leaving.
const settlePoll = 300 * time.Millisecond

// A Subject is a holder as rights names it: an absolute image path or a
// package family, and a Windows SID or a POSIX uid.
type Subject struct {
	Program string
	Account string
}

func (s Subject) same(other Subject) bool {
	return s.Program == other.Program && s.Account == other.Account
}

// An Attached holder predates OA. It holds a resource, answers no lease call,
// and is yielded on its behalf through its host's own mechanism; its answer is
// what the instrument shows afterwards (CONTRACT.md RES-L3).
type Attached interface {
	// Holder is the row the table reports it under, host:<name>.
	Holder() string
	// Resource is what it holds.
	Resource() string
	// Holding is what the host says it holds now, one name per resident
	// model. An empty list is a holder with nothing to give back. An error is
	// a host that is not answering, which claims nothing and is not asked.
	Holding(ctx context.Context) ([]string, error)
	// Yield asks the host's own API to let go of what it holds. It returns
	// when the host answered, not when the bytes are gone.
	Yield(ctx context.Context) error
}

// A HolderPolicy authorizes yielding one attached holder on its behalf. Wrap
// ErrPolicyUnavailable when no decision could be obtained; every other error
// is a refusal, recorded as the holder's own refusal with its reason.
type HolderPolicy func(ctx context.Context, holder string) error

// A record is one live lease.
type record struct {
	ID       string    `json:"id"`
	Resource string    `json:"resource"`
	Amount   int64     `json:"amount"`
	Program  string    `json:"program"`
	Account  string    `json:"account,omitempty"`
	Grant    string    `json:"grant,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Since    time.Time `json:"since"`
	RenewBy  time.Time `json:"renew_by"`
	// A verified child may spend this lease's reservation. The start identity
	// prevents a later process reusing its PID from inheriting the credit.
	ProcessPID   int    `json:"process_pid,omitempty"`
	ProcessStart string `json:"process_start,omitempty"`
	// held marks a lease whose lifetime is its holder's connection rather
	// than renew_by: the wake hold, which the rights service keeps on the
	// holder's own connection and ends when that holder closes, exits or is
	// killed (CONTRACT.md RES-A1). Such a lease never expires on a clock and
	// is never written to the book, because the connection that owned it did
	// not survive the restart either.
	held bool
}

func (r *record) subject() Subject { return Subject{Program: r.Program, Account: r.Account} }

func (r *record) lease() wire.Lease {
	return wire.Lease{ID: r.ID, Resource: r.Resource, Amount: r.Amount,
		Since: stamp(r.Since), RenewBy: stamp(r.RenewBy)}
}

// A question is one yield request put to a holder that implements the
// contract and not yet answered.
type question struct {
	request wire.YieldRequest
	holder  Subject
	lease   string
	seq     uint64
	done    chan struct{}
	answer  wire.YieldAnswer
	reason  string
	before  int64
}

// persisted is the lease book on disk.
type persisted struct {
	Profile string    `json:"profile"`
	Leases  []*record `json:"leases"`
}

const leaseProfile = "abstraction.resource/leases@1"

// maxBook bounds the persisted book. A lease is a few hundred bytes; a machine
// with more leases than this fits has a different problem than arbitration.
const maxBook = 1 << 20

// A Book is the lease record of abstraction.resource/leases@1: who holds a
// grant of a resource, who was asked to give bytes back, and what they
// answered. It measures nothing itself — the table's instrument does — and it
// kills nothing (RES-L5).
type Book struct {
	table    *Table
	attached []Attached
	audit    *Audit
	path     string
	now      func() time.Time
	lifetime time.Duration
	settle   time.Duration
	inspect  func(int) (instrument.ProcessInfo, error)

	hold  Policy
	yield HolderPolicy

	mu      sync.Mutex
	leases  map[string]*record
	waiting map[uint64]*question
	held    map[string]string
	changed map[string]time.Time
	seq     uint64
}

// BookOptions configures a Book. Table is required.
type BookOptions struct {
	Table *Table
	// Attached are the holders that predate OA, in no particular order: the
	// service asks them idle longest first (RES-L2).
	Attached []Attached
	// Audit receives every ask, yield and refusal. Nil records nothing.
	Audit *Audit
	// Path is the file the leases live in, so a restart keeps them. An empty
	// path keeps them in memory only.
	Path string
	// Hold decides ActionHold for the asking caller. Without it every bound
	// caller may hold, which is a composition without rights and not a
	// default anybody ships.
	Hold Policy
	// Yield decides ActionYield for the service's own program on each
	// attached holder. Without it every attached holder may be yielded.
	Yield HolderPolicy
	// Lifetime is how long a lease stands without a renewal; zero is
	// DefaultLifetime. Settle is how long bytes have to leave the instrument
	// after a host answered; zero is DefaultSettle.
	Lifetime time.Duration
	Settle   time.Duration
	// Now is the service's clock. Nil is time.Now.
	Now func() time.Time
	// InspectProcess supplies process relation evidence. Nil uses the local
	// platform inspector; injection permits deterministic refusal tests.
	InspectProcess func(int) (instrument.ProcessInfo, error)
}

// OpenBook reads the leases a previous run left, drops those whose renew_by
// has passed, and answers from there on. A file that cannot be read is an
// error: an unreadable book is never an empty one.
func OpenBook(options BookOptions) (*Book, error) {
	if options.Table == nil {
		return nil, errors.New("resource leases: nil table")
	}
	b := &Book{table: options.Table, attached: options.Attached, audit: options.Audit,
		path: options.Path, now: options.Now, lifetime: options.Lifetime, settle: options.Settle,
		hold: options.Hold, yield: options.Yield, inspect: options.InspectProcess,
		leases: map[string]*record{}, waiting: map[uint64]*question{},
		held: map[string]string{}, changed: map[string]time.Time{}}
	if b.now == nil {
		b.now = time.Now
	}
	if b.inspect == nil {
		b.inspect = instrument.InspectProcess
	}
	if b.lifetime <= 0 {
		b.lifetime = DefaultLifetime
	}
	if b.settle <= 0 {
		b.settle = DefaultSettle
	}
	if b.path == "" {
		return b, nil
	}
	if !filepath.IsAbs(b.path) {
		return nil, errors.New("resource leases: absolute book path required")
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resource leases: %s: %w", b.path, err)
	}
	if len(data) > maxBook {
		return nil, fmt.Errorf("resource leases: %s is larger than %d bytes", b.path, maxBook)
	}
	var book persisted
	if err := json.Unmarshal(data, &book); err != nil || book.Profile != leaseProfile {
		return nil, fmt.Errorf("resource leases: %s is not %s; repair or remove it", b.path, leaseProfile)
	}
	now := b.now()
	for _, kept := range book.Leases {
		if kept == nil || kept.ID == "" || kept.Resource == "" || !kept.RenewBy.After(now) {
			continue
		}
		b.leases[kept.ID] = kept
		if n := serial(kept.ID); n > b.seq {
			b.seq = n
		}
	}
	return b, nil
}

// serial reads the counter out of a lease id so a restart never reissues one.
func serial(id string) uint64 {
	_, digits, found := strings.Cut(id, "-")
	if !found {
		return 0
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// Claims is the live leases as rows of the table, so a lease is visible to
// every reader of who holds the resource, the awake hold included (RES-A1).
// The row carries the lease id and the rule it sits under; its evidence is
// claimed, because a grant is what was promised and the instrument measures
// what is held.
func (b *Book) Claims() Claims {
	return func() []Claim {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.expire(b.now())
		out := make([]Claim, 0, len(b.leases))
		for _, live := range b.leases {
			out = append(out, Claim{Resource: live.Resource, Program: live.Program, Account: live.Account,
				Grant: live.Grant, Detail: live.Detail, Since: live.Since, Lease: live.ID, Amount: live.Amount})
		}
		return out
	}
}

// Acquire is the contract's order: the rights decision, then the table, then
// the holders (CONTRACT.md RES-L1, RES-L2). Nothing is killed and nothing is
// allocated on the asker's behalf; the lease is a grant against the table.
func (b *Book) Acquire(ctx context.Context, who Subject, peer *identity.Peer, resource string, amount, waitMs int64) wire.AcquireResult {
	asked := []wire.YieldRecord{}
	if resource == "" || amount < 0 || waitMs < 0 || time.Duration(waitMs)*time.Millisecond > MaxAcquireWait {
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeInvalid, Asked: asked}
	}
	if who.Program == "" {
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeInvalid, Asked: asked}
	}
	if b.hold != nil {
		err := b.holdDecision(ctx, peer, resource)
		switch {
		case errors.Is(err, ErrPolicyUnavailable):
			b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "unavailable"})
			return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
		case err != nil:
			b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "not_permitted"})
			return wire.AcquireResult{Outcome: wire.AcquireOutcomeNotPermitted, Asked: asked}
		}
	}
	state, samples, err := b.table.snapshot(resource, true)
	if err != nil {
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
	}
	pool := b.pool(resource, state.Capacity)
	if pool > 0 && amount > pool {
		b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "insufficient"})
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeInsufficient, Asked: asked}
	}
	b.mark(ctx, resource)
	if result, ok := b.tryGrant(state, samples, pool, who, resource, amount, asked); ok {
		return result
	}
	// Nothing was free without asking. Each pass below asks whatever RES-L2
	// still has left to ask and then decides the grant under the lock again:
	// the freed bytes a yield reports are not this asker's until nobody else
	// has spent them first (RES-L2). A pass where nothing was actually
	// yielded leaves nothing to gain from asking again, so it stops there;
	// a pass where something was taken by a concurrent grant re-enters the
	// ask sequence, bounded by the caller's own deadline.
	deadline := b.now().Add(time.Duration(waitMs) * time.Millisecond)
	for {
		more := b.ask(ctx, who, resource, amount, pool, deadline)
		asked = append(asked, more...)
		yielded := false
		for _, entry := range more {
			if entry.Answer == AnswerYielded && entry.Amount > 0 {
				yielded = true
				break
			}
		}
		state, samples, err = b.table.snapshot(resource, true)
		if err != nil {
			return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
		}
		if result, ok := b.tryGrant(state, samples, pool, who, resource, amount, asked); ok {
			return result
		}
		if !yielded || !b.now().Before(deadline) {
			break
		}
	}
	b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "holders_refused"})
	return wire.AcquireResult{Outcome: wire.AcquireOutcomeHoldersRefused, Asked: asked}
}

// holdDecision asks the configured policy for ActionHold on the resource.
func (b *Book) holdDecision(ctx context.Context, peer *identity.Peer, resource string) error {
	if peer == nil {
		return ErrPolicyUnavailable
	}
	return b.hold(ctx, peer, ActionHold, resource)
}

// pool is what the resource's whole is, for deciding what is free. The
// instrument's capacity stands where it has one. Where it has none — a
// shared-memory adapter carved out of system RAM — the machine's memory is
// that pool, which is what the 2026-09-22 measurement found it to be. A
// machine that will say neither has no pool, and every request there asks the
// holders.
func (b *Book) pool(resource string, capacity int64) int64 {
	if capacity > 0 {
		return capacity
	}
	if resource == instrument.Card0 || resource == "memory" || strings.HasPrefix(resource, "card:") {
		return instrument.MachineMemory()
	}
	return 0
}

// free is what a new lease could take without asking anybody: the pool, less
// what the instrument measured held, less the unoccupied part of every live
// lease, including earlier leases of the same subject. Measured bytes are
// credited once against that subject's combined grants.
func (b *Book) free(state wire.ResourceState, samples []instrument.Sample, pool int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.freeLocked(state, samples, pool)
}

// freeLocked is free's arithmetic for a caller already holding the lock, so a
// decision that reads it and a write that spends it happen as one step
// (RES-L2's tryGrant).
func (b *Book) freeLocked(state wire.ResourceState, samples []instrument.Sample, pool int64) int64 {
	if pool <= 0 {
		if state.Held == 0 && len(state.Holders) == 0 {
			// Nothing holds the resource and nothing says what the whole is.
			// There is nobody to ask, and refusing a request against an empty
			// table would make the service useless on a machine with no
			// capacity figure.
			return 1<<62 - 1
		}
		return 0
	}
	b.expire(b.now())
	granted := map[Subject]int64{}
	bound := map[string]Subject{}
	canCredit := b.table.verifiedProcessSamples()
	for _, live := range b.leases {
		if live.Resource == state.Resource {
			granted[live.subject()] += live.Amount
			if canCredit && live.ProcessPID > 0 && live.ProcessStart != "" {
				key := strconv.Itoa(live.ProcessPID) + "/" + live.ProcessStart
				if _, duplicate := bound[key]; !duplicate {
					if current, err := b.inspect(live.ProcessPID); err == nil &&
						current.StartID == live.ProcessStart && current.Account == live.Account {
						bound[key] = live.subject()
					}
				}
			}
		}
	}
	measured := map[Subject]int64{}
	for _, sample := range samples {
		owner := Subject{Program: sample.Program, Account: sample.Account}
		if owner.Program == "" {
			owner.Program = sample.Image
		}
		if sample.StartID != "" {
			key := strconv.Itoa(sample.PID) + "/" + sample.StartID
			if credited, ok := bound[key]; ok && sample.Account == credited.Account {
				owner = credited
			}
		}
		measured[owner] += sample.Amount
	}
	free := pool - state.Held
	if free <= 0 {
		return 0
	}
	for holder, amount := range granted {
		if short := amount - measured[holder]; short > 0 {
			if short >= free {
				return 0
			}
			free -= short
		}
	}
	return free
}

// ask puts the yield requests of one Acquire, in the contract's order:
// attached holders idle longest first, then the leases on this resource in the
// order their grants say, then it stops (RES-L2). It returns every ask it
// made, whatever each answered.
func (b *Book) ask(ctx context.Context, who Subject, resource string, amount, pool int64, deadline time.Time) []wire.YieldRecord {
	asked := []wire.YieldRecord{}
	for _, holder := range b.order(ctx, resource) {
		if !b.now().Before(deadline) {
			break
		}
		asked = append(asked, b.askAttached(ctx, who, holder, resource, amount, deadline))
		if state, samples, err := b.table.snapshot(resource, true); err == nil && b.free(state, samples, pool) >= amount {
			return asked
		}
	}
	for _, live := range b.leaseOrder(resource, who) {
		if !b.now().Before(deadline) {
			break
		}
		asked = append(asked, b.askLease(ctx, who, live, resource, deadline))
		if state, samples, err := b.table.snapshot(resource, true); err == nil && b.free(state, samples, pool) >= amount {
			return asked
		}
	}
	return asked
}

// order is the attached holders of this resource that are holding something,
// idle longest first. Idleness a service can see is the time since the
// holder's own list of what it holds last changed: a host that has held the
// same model untouched the longest is the one asked first.
func (b *Book) order(ctx context.Context, resource string) []Attached {
	var out []Attached
	for _, holder := range b.attached {
		if holder.Resource() != resource {
			continue
		}
		holding, err := holder.Holding(ctx)
		if err != nil || len(holding) == 0 {
			continue
		}
		out = append(out, holder)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		a, z := b.changed[out[i].Holder()], b.changed[out[j].Holder()]
		if !a.Equal(z) {
			return a.Before(z)
		}
		return out[i].Holder() < out[j].Holder()
	})
	return out
}

// mark notes what each attached holder holds now, so the next Acquire can tell
// which of them has held the same thing longest.
func (b *Book) mark(ctx context.Context, resource string) {
	for _, holder := range b.attached {
		if holder.Resource() != resource {
			continue
		}
		holding, err := holder.Holding(ctx)
		if err != nil {
			continue
		}
		sort.Strings(holding)
		fingerprint := strings.Join(holding, "\x00")
		b.mu.Lock()
		if b.held[holder.Holder()] != fingerprint {
			b.held[holder.Holder()] = fingerprint
			b.changed[holder.Holder()] = b.now()
		}
		b.mu.Unlock()
	}
}

// askAttached yields one holder on its behalf and reports what the instrument
// showed afterwards (RES-L3).
func (b *Book) askAttached(ctx context.Context, who Subject, holder Attached, resource string, amount int64, deadline time.Time) wire.YieldRecord {
	began := b.now()
	name := holder.Holder()
	answer := func(word string, freed int64) wire.YieldRecord {
		took := b.now().Sub(began).Milliseconds()
		b.record(auditRecord{Event: "ask", Asker: who.Program, Holder: name, Resource: resource,
			Amount: freed, Requested: amount, Answer: word, TookMS: took})
		return wire.YieldRecord{Holder: name, Amount: freed, Answer: word, TookMs: took}
	}
	if b.yield != nil {
		switch err := b.yield(ctx, name); {
		case errors.Is(err, ErrPolicyUnavailable):
			return answer(AnswerRefused+": the decision on yielding "+name+" is unavailable", 0)
		case err != nil:
			return answer(AnswerRefused+": no rule permits yielding "+name, 0)
		}
	}
	before := b.measured(resource, name)
	yieldCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := holder.Yield(yieldCtx); err != nil {
		return answer(AnswerRefused+": "+oneLine(err.Error()), 0)
	}
	freed := b.settled(yieldCtx, resource, before, deadline)
	if freed <= 0 {
		return answer(AnswerUnanswered, 0)
	}
	b.mu.Lock()
	delete(b.held, name)
	b.changed[name] = b.now()
	b.mu.Unlock()
	return answer(AnswerYielded, freed)
}

// measured is what the instrument attributes to this resource now. An attached
// holder has no process of its own the instrument can name — it is an HTTP
// engine whose models live in whatever processes it spawned — so the bytes a
// yield freed are read from the resource's total, not from one row.
func (b *Book) measured(resource, _ string) int64 {
	state, err := b.table.Holders(resource, true)
	if err != nil {
		return 0
	}
	return state.Held
}

// settled waits for the bytes to leave the instrument and returns how many
// did. It returns as soon as the figure falls, which the measurement showed
// happens within one poll of the host's own answer.
func (b *Book) settled(ctx context.Context, resource string, before int64, deadline time.Time) int64 {
	bound := b.now().Add(b.settle)
	if bound.After(deadline) {
		bound = deadline
	}
	for {
		after := b.measured(resource, "")
		if freed := before - after; freed > 0 {
			return freed
		}
		if !b.now().Before(bound) {
			return 0
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(settlePoll):
		}
	}
}

// leaseOrder is the other holders' leases on this resource, in the order their
// grants say. A grant that names no order is asked after the grants that do,
// and two leases under one grant are asked oldest first.
func (b *Book) leaseOrder(resource string, who Subject) []*record {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(b.now())
	var out []*record
	for _, live := range b.leases {
		if live.Resource == resource && !live.subject().same(who) {
			out = append(out, live)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Grant != out[j].Grant {
			if out[i].Grant == "" || out[j].Grant == "" {
				return out[j].Grant == ""
			}
			return out[i].Grant < out[j].Grant
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}

// askLease puts one yield request to a holder that implements the contract and
// waits for its answer. A holder that never observes is recorded unanswered
// (RES-L3).
func (b *Book) askLease(ctx context.Context, who Subject, live *record, resource string, deadline time.Time) wire.YieldRecord {
	began := b.now()
	before := b.measured(resource, live.Program)
	b.mu.Lock()
	b.seq++
	q := &question{holder: live.subject(), lease: live.ID, seq: b.seq, done: make(chan struct{}), before: before,
		request: wire.YieldRequest{ID: "ask-" + strconv.FormatUint(b.seq, 10), Resource: resource,
			Amount: live.Amount, Reason: who.Program, At: stamp(began), Lease: live.ID}}
	b.waiting[q.seq] = q
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waiting, q.seq)
		b.mu.Unlock()
	}()
	answer := func(word string, freed int64) wire.YieldRecord {
		took := b.now().Sub(began).Milliseconds()
		b.record(auditRecord{Event: "ask", Asker: who.Program, Holder: live.Program, Lease: live.ID,
			Resource: resource, Amount: freed, Requested: live.Amount, Answer: word, TookMS: took})
		return wire.YieldRecord{Holder: live.Program, Amount: freed, Answer: word, TookMs: took}
	}
	wait := time.Until(deadline)
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-q.done:
	case <-timer.C:
		return answer(AnswerUnanswered, 0)
	case <-ctx.Done():
		return answer(AnswerUnanswered, 0)
	}
	b.mu.Lock()
	recordedAnswer, recordedReason := q.answer, q.reason
	b.mu.Unlock()
	if recordedAnswer == wire.YieldAnswerRefused {
		word := AnswerRefused
		if recordedReason != "" {
			word += ": " + oneLine(recordedReason)
		}
		return answer(word, 0)
	}
	// yielded is confirmed against the instrument before the asker is told.
	freed := b.settled(ctx, resource, before, deadline)
	if freed <= 0 {
		return answer(AnswerUnanswered, 0)
	}
	b.release(live.ID, "yielded")
	return answer(AnswerYielded, freed)
}

// tryGrant decides and writes a grant as one step under the lock: the bytes
// free at that moment (freeLocked, against the same state and pool the
// caller already read) and the lease that spends them are one critical
// section, so a concurrent tryGrant on the same book cannot also spend them
// (RES-L2). It reports whether the grant was made; a caller that loses the
// race asks again or reads holders_refused.
func (b *Book) tryGrant(state wire.ResourceState, samples []instrument.Sample, pool int64, who Subject, resource string, amount int64, asked []wire.YieldRecord) (wire.AcquireResult, bool) {
	b.mu.Lock()
	if b.freeLocked(state, samples, pool) < amount {
		b.mu.Unlock()
		return wire.AcquireResult{}, false
	}
	lease := b.grantLocked(who, resource, amount)
	b.mu.Unlock()
	b.record(auditRecord{Event: "acquired", Asker: who.Program, Lease: lease.ID, Resource: resource, Amount: amount, Answer: "acquired"})
	return wire.AcquireResult{Outcome: wire.AcquireOutcomeAcquired, Lease: &lease, Asked: asked}, true
}

// grantLocked writes one lease and reports it. The caller holds the lock.
func (b *Book) grantLocked(who Subject, resource string, amount int64) wire.Lease {
	b.seq++
	now := b.now()
	live := &record{ID: "lease-" + strconv.FormatUint(b.seq, 10), Resource: resource, Amount: amount,
		Program: who.Program, Account: who.Account, Since: now, RenewBy: now.Add(b.lifetime)}
	b.leases[live.ID] = live
	b.save()
	return live.lease()
}

// Renew extends the caller's own lease (RES-L4).
func (b *Book) Renew(who Subject, id string) wire.LeaseChange {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(b.now())
	live, ok := b.leases[id]
	if !ok || !live.subject().same(who) {
		return wire.LeaseChange{}
	}
	live.RenewBy = b.now().Add(b.lifetime)
	b.save()
	lease := live.lease()
	return wire.LeaseChange{Applied: true, Lease: &lease}
}

// bindProcess associates one directly spawned process with the caller's own
// lease. A binding is accounting evidence only: failure leaves the full
// requested reservation in place, and does not revoke an already granted
// lease. recheck keeps the caller's bound process pinned while its parent
// relation is inspected; on Linux the peer exposes no raw start key, and
// this kernel-held pidfd is what establishes that the inspected PID is it.
func (b *Book) bindProcess(who Subject, caller identity.Process, id string, pid int64, recheck func() error) wire.ProcessBindResult {
	result := func(outcome wire.ProcessBindOutcome) wire.ProcessBindResult {
		return wire.ProcessBindResult{Outcome: outcome}
	}
	if id == "" || pid <= 0 || pid > int64(^uint(0)>>1) || int(pid) == caller.PID {
		return result(wire.ProcessBindOutcomeInvalid)
	}
	if !b.table.verifiedProcessSamples() {
		return result(wire.ProcessBindOutcomeUnverifiable)
	}
	if caller.PID <= 0 || caller.Recycled || who.Account == "" ||
		(runtime.GOOS == "windows" && caller.StartTime.IsZero()) || recheck == nil || recheck() != nil {
		return result(wire.ProcessBindOutcomeUnverifiable)
	}
	parent, err := b.inspect(caller.PID)
	if err != nil || parent.StartID == "" || parent.Started.IsZero() || parent.Account != who.Account || parent.PID != caller.PID {
		return result(wire.ProcessBindOutcomeUnverifiable)
	}
	// Windows compares the exact creation FILETIME. Linux's peer has no
	// raw start field; recheck retains its pidfd while we inspect the raw
	// boot/tick identity, avoiding a wall-clock tolerance as a proof.
	if runtime.GOOS == "windows" && !parent.Started.Equal(caller.StartTime) ||
		runtime.GOOS == "linux" && (parent.BootID == "" || parent.StartTicks == 0) {
		return result(wire.ProcessBindOutcomeUnverifiable)
	}
	child, err := b.inspect(int(pid))
	if err != nil || child.StartID == "" || child.Account != who.Account || child.ParentPID != caller.PID || child.PID != int(pid) {
		return result(wire.ProcessBindOutcomeUnverifiable)
	}
	if runtime.GOOS == "linux" {
		if child.BootID == "" || child.BootID != parent.BootID || child.StartTicks < parent.StartTicks {
			return result(wire.ProcessBindOutcomeUnverifiable)
		}
	} else if child.Started.IsZero() || child.Started.Before(parent.Started) {
		return result(wire.ProcessBindOutcomeUnverifiable)
	}
	if recheck() != nil {
		return result(wire.ProcessBindOutcomeUnverifiable)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(b.now())
	live, ok := b.leases[id]
	if !ok || !live.subject().same(who) {
		return result(wire.ProcessBindOutcomeRefused)
	}
	if live.ProcessPID != 0 {
		if live.ProcessPID == int(pid) && live.ProcessStart == child.StartID {
			return result(wire.ProcessBindOutcomeBound)
		}
		return result(wire.ProcessBindOutcomeRefused)
	}
	for _, other := range b.leases {
		if other.ID != id && other.ProcessPID == int(pid) && other.ProcessStart == child.StartID {
			return result(wire.ProcessBindOutcomeRefused)
		}
	}
	live.ProcessPID, live.ProcessStart = int(pid), child.StartID
	b.save()
	return result(wire.ProcessBindOutcomeBound)
}

// Release ends the caller's own lease now.
func (b *Book) Release(who Subject, id string) wire.LeaseChange {
	b.mu.Lock()
	live, ok := b.leases[id]
	if !ok || !live.subject().same(who) {
		b.mu.Unlock()
		return wire.LeaseChange{}
	}
	lease := live.lease()
	delete(b.leases, id)
	b.save()
	b.mu.Unlock()
	b.record(auditRecord{Event: "released", Asker: live.Program, Lease: id, Resource: live.Resource, Amount: live.Amount, Answer: "released"})
	return wire.LeaseChange{Applied: true, Lease: &lease}
}

// release ends a lease the service ended itself.
func (b *Book) release(id, why string) {
	b.mu.Lock()
	live, ok := b.leases[id]
	if ok {
		delete(b.leases, id)
		b.save()
	}
	b.mu.Unlock()
	if ok {
		b.record(auditRecord{Event: "released", Asker: live.Program, Lease: id, Resource: live.Resource, Amount: live.Amount, Answer: why})
	}
}

// Observe is the yield requests addressed to this caller since cursor. A
// caller that has none waits up to wait_ms for one.
func (b *Book) Observe(ctx context.Context, who Subject, cursor string, waitMs int64) (wire.YieldPage, error) {
	if waitMs < 0 || time.Duration(waitMs)*time.Millisecond > MaxObserveWait {
		return wire.YieldPage{}, &wire.ServiceError{Code: wire.ServiceErrorCodeInvalidRequest, Message: "wait_ms is 0..30000"}
	}
	from := serial(cursor)
	if cursor != "" && from == 0 {
		from = parseCursor(cursor)
	}
	deadline := b.now().Add(time.Duration(waitMs) * time.Millisecond)
	for {
		page := b.page(who, from)
		if len(page.Requests) > 0 || !b.now().Before(deadline) {
			return page, nil
		}
		select {
		case <-ctx.Done():
			return b.page(who, from), nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func parseCursor(cursor string) uint64 {
	n, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (b *Book) page(who Subject, from uint64) wire.YieldPage {
	b.mu.Lock()
	defer b.mu.Unlock()
	page := wire.YieldPage{Requests: []wire.YieldRequest{}, Next: strconv.FormatUint(from, 10)}
	var keys []uint64
	for seq, q := range b.waiting {
		if seq > from && q.holder.same(who) {
			keys = append(keys, seq)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	last := from
	for _, seq := range keys {
		page.Requests = append(page.Requests, b.waiting[seq].request)
		last = seq
	}
	page.Next = strconv.FormatUint(last, 10)
	return page
}

// Answer records this caller's answer to one yield request.
func (b *Book) Answer(who Subject, request string, answer wire.YieldAnswer, reason string) wire.AnswerResult {
	b.mu.Lock()
	var found *question
	for _, q := range b.waiting {
		if q.request.ID == request && q.holder.same(who) {
			found = q
			break
		}
	}
	if found == nil {
		b.mu.Unlock()
		return wire.AnswerResult{}
	}
	select {
	case <-found.done:
		b.mu.Unlock()
		return wire.AnswerResult{Recorded: true}
	default:
		found.answer, found.reason = answer, reason
		close(found.done)
	}
	b.mu.Unlock()
	if answer == wire.YieldAnswerRefused {
		b.record(auditRecord{Event: "refusal", Holder: who.Program, Lease: found.lease,
			Resource: found.request.Resource, Asker: found.request.Reason, Answer: oneLine(reason)})
	}
	return wire.AnswerResult{Recorded: true}
}

// Expire ends every lease past its renew_by. The host calls it on a clock so a
// lease nobody renews stops standing between the next asker and the bytes.
func (b *Book) Expire() {
	b.mu.Lock()
	ended := b.expire(b.now())
	b.mu.Unlock()
	for _, live := range ended {
		b.record(auditRecord{Event: "released", Asker: live.Program, Lease: live.ID,
			Resource: live.Resource, Amount: live.Amount, Answer: "expired"})
	}
}

// expire drops the ended leases. The caller holds the lock.
func (b *Book) expire(now time.Time) []*record {
	var ended []*record
	for id, live := range b.leases {
		if live.held {
			continue
		}
		if !live.RenewBy.After(now) {
			ended = append(ended, live)
			delete(b.leases, id)
		}
	}
	if len(ended) > 0 {
		b.save()
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].ID < ended[j].ID })
	return ended
}

// Leases is every live lease, oldest first. It is the reader a CLI or a Panel
// uses without going through the table.
func (b *Book) Leases() []wire.Lease {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(b.now())
	out := make([]wire.Lease, 0, len(b.leases))
	for _, live := range b.leases {
		out = append(out, live.lease())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since < out[j].Since })
	return out
}

// save writes the book. The caller holds the lock. A write that fails is
// reported through the audit and does not end the call: a lease the service is
// keeping in memory is still a lease until it expires.
func (b *Book) save() {
	if b.path == "" {
		return
	}
	book := persisted{Profile: leaseProfile, Leases: make([]*record, 0, len(b.leases))}
	for _, live := range b.leases {
		if live.held {
			continue
		}
		book.Leases = append(book.Leases, live)
	}
	sort.Slice(book.Leases, func(i, j int) bool { return book.Leases[i].ID < book.Leases[j].ID })
	data, err := json.MarshalIndent(book, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	temporary := b.path + ".writing"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(temporary, b.path); err != nil {
		//unchecked: best-effort temp-file cleanup after a rename failure this function has no return value to report either way
		os.Remove(temporary)
	}
}

func (b *Book) record(r auditRecord) {
	if b.audit == nil {
		return
	}
	r.At = stamp(b.now())
	b.audit.Write(r)
}

// oneLine keeps a reason to one bounded line, so a record stays one record.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
	if len(s) > 256 {
		s = s[:256]
	}
	return strings.TrimSpace(s)
}
