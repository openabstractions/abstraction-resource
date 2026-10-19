package service

import (
	"context"
	"crypto/rand"
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
	"unicode"
	"unicode/utf8"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	"github.com/openabstractions/abstraction-rights/go/authorization/gate"
	watch "github.com/openabstractions/abstraction-watch/go"
)

// The rights rules of abstraction.resource/leases@1.
//
// ActionHold is decided for the asking program on the resource it asks for,
// before the table is read (CONTRACT.md RES-L1). ResourceAwake is the wake
// hold's resource, and AwakeRight is the word rights carried for it before the
// hold became a lease: both name this one decision (RES-A1).
//
// ActionYield is decided for the service's own program on a lease holder's
// program, the subject the lease names: the person's switch for whether that
// app's leases may be asked back (RES-L3). Only an evaluated deny keeps the
// app's memory; no rule asks it as before.
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

// DefaultSettle is how long the service waits for a holder's bytes to leave
// the instrument after it answered yielded. Both unloads measured on
// 2026-09-22 froze on Windows' counters within one poll of the engine's
// answer, 7.7-7.9 s; twice that is a bound a slower model still fits inside.
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

// A HolderPolicy decides whether the leases of one holder program, an
// absolute image path or a package family as the lease names it, may be
// asked back (RES-L3). It returns an error only for the person's evaluated
// deny; wrap ErrPolicyUnavailable when no decision could be obtained, which
// asks the holder as no rule does.
type HolderPolicy func(ctx context.Context, program string) error

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
	// Label is the holder's own words for what the lease holds, as
	// app: model (Label). Display only; it carries no authority.
	Label string `json:"label,omitempty"`
	// held marks a lease whose lifetime is its holder's connection rather
	// than renew_by: the wake hold, which the rights service keeps on the
	// holder's own connection and ends when that holder closes, exits or is
	// killed (CONTRACT.md RES-A1). Such a lease never expires on a clock and
	// is never written to the book, because the connection that owned it did
	// not survive the restart either.
	held       bool
	recordOnly bool
	controller *leaseController
}

// leaseController is receiving-side evidence for one delegated acquisition.
// It is process-local and is never serialized with the original owner's lease.
type leaseController struct {
	who     Subject
	process identity.Process
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
	audit    *Audit
	path     string
	now      func() time.Time
	lifetime time.Duration
	settle   time.Duration
	inspect  func(int) (instrument.ProcessInfo, error)
	// child measures a bound child through a handle on that process where
	// the instrument's samples carry no process identity (RES-L2); nil
	// where the instrument has no such measurement.
	child func(pid int, startID string) (int64, error)
	// keepAwake takes the platform's wake request; awake is its release
	// while an acquired lease of resource awake lives (RES-A1).
	keepAwake func(why string) (func(), error)
	awake     func()
	// awakeRefused is set while the platform refuses the wake request, so
	// the refusal is recorded once.
	awakeRefused    bool
	keepSystemAwake func(why string) (func(), error)
	systemAwake     func()
	systemRefused   bool
	systemError     error

	hold     Policy
	yield    HolderPolicy
	standing SubjectPolicy

	mu      sync.Mutex
	leases  map[string]*record
	waiting map[uint64]*question
	observe *watch.Gate
	// words is each waiting asker's waiting word, by asker and pool, with
	// the count of its Acquires still in their ask loop (RES-L8).
	words map[string]*waitingWord
	// asking counts, per pool and under mu, the plain Acquire calls in
	// their ask loop. AcquireFree reads held while any is: bytes their
	// yields free are theirs (RES-L6).
	asking map[string]int
	seq    uint64
	epoch  string
	// afterFirstTry runs in Acquire once its first grant decision has
	// failed and before it asks anybody. Tests use it to act inside that
	// window; nil in every shipped composition.
	afterFirstTry func()
}

// BookOptions configures a Book. Table is required.
type BookOptions struct {
	Table *Table
	// Audit receives every ask, yield and refusal. Nil records nothing.
	Audit *Audit
	// Path is the file the leases live in, so a restart keeps them. An empty
	// path keeps them in memory only.
	Path string
	// Hold decides ActionHold for the asking caller. Without it every
	// acquire reads unavailable (RES-L1).
	Hold Policy
	// Yield decides ActionYield for the service's own program on a lease
	// holder's program, the person's switch for that app's leases (RES-L3).
	// Nil asks every holder.
	Yield HolderPolicy
	// Lifetime is how long a lease stands without a renewal; zero is
	// DefaultLifetime. Settle is how long bytes have to leave the instrument
	// after a holder answered yielded; zero is DefaultSettle.
	Lifetime time.Duration
	Settle   time.Duration
	// Now is the service's clock. Nil is time.Now.
	Now func() time.Time
	// InspectProcess supplies process relation evidence. Nil uses the local
	// platform inspector; injection permits deterministic refusal tests.
	InspectProcess func(int) (instrument.ProcessInfo, error)
	// ChildUsage measures one bound child's use through a handle on that
	// exact process. Nil takes the instrument's own (instrument.ChildMeasured)
	// where it has one; injection permits deterministic accounting tests.
	ChildUsage func(pid int, startID string) (int64, error)
	// KeepAwake takes the platform's wake request and returns its release.
	// The book holds one request while any lease of resource awake granted
	// through Acquire lives beside its holders card lease, and releases it
	// when the last pair ends (RES-A1). KeepSystemAwake owns non-card holds.
	KeepAwake func(why string) (release func(), err error)
	// KeepSystemAwake keeps non-card wake leases awake without requiring the display.
	KeepSystemAwake func(why string) (release func(), err error)
	// Standing decides ActionHold again for a live lease's holder, by the
	// subject the lease names, with no call in flight and asking nobody.
	// Renew asks it before it extends an acquired lease, and Recheck asks it
	// for the holder of every acquired lease and ends each lease it refuses
	// (RES-L7). Without it every decision reads unavailable: Recheck ends
	// nothing and Renew leaves renew_by unmoved, so an acquired lease ends
	// within one lifetime.
	Standing SubjectPolicy
}

// A SubjectPolicy decides one action on one resource for a subject a lease
// names. Wrap ErrPolicyUnavailable when no decision could be obtained; every
// other error is a refusal.
type SubjectPolicy func(ctx context.Context, who Subject, action, resource string) error

// OpenBook reads the leases a previous run left, drops those whose renew_by
// has passed, and answers from there on. A file that cannot be read is an
// error: an unreadable book is never an empty one.
func OpenBook(options BookOptions) (*Book, error) {
	if options.Table == nil {
		return nil, errors.New("resource leases: nil table")
	}
	if options.Hold != nil {
		// The Hold gate decides resource actions only (RIGHTS-A7).
		designated, err := gate.For(actionNamespace, options.Hold)
		if err != nil {
			return nil, fmt.Errorf("resource leases: %w", err)
		}
		options.Hold = designated
	}
	b := &Book{table: options.Table, audit: options.Audit,
		path: options.Path, now: options.Now, lifetime: options.Lifetime, settle: options.Settle,
		hold: options.Hold, yield: options.Yield, standing: options.Standing, inspect: options.InspectProcess, child: options.ChildUsage,
		keepAwake: options.KeepAwake, keepSystemAwake: options.KeepSystemAwake, observe: watch.NewGate(watch.DefaultWaiters),
		epoch: rand.Text(), leases: map[string]*record{}, waiting: map[uint64]*question{}, words: map[string]*waitingWord{},
		asking: map[string]int{}}
	if b.now == nil {
		b.now = time.Now
	}
	options.Table.credit = b.credited
	if b.inspect == nil {
		b.inspect = instrument.InspectProcess
	}
	if measured, ok := options.Table.instrument.(instrument.ChildMeasured); ok && b.child == nil {
		b.child = measured.ChildUsage
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
	b.mu.Lock()
	b.syncAwake()
	b.mu.Unlock()
	return b, nil
}

// syncAwake partitions wake leases between system-only holds and the
// provisional card-paired display holds. The caller holds mu.
// cardPrefix begins every card resource, card:0 first.
const cardPrefix = "card:"

func (b *Book) syncAwake() {
	carding := map[Subject]bool{}
	for _, lease := range b.leases {
		if strings.HasPrefix(lease.Resource, cardPrefix) && !lease.held {
			carding[lease.subject()] = true
		}
	}
	live := 0
	system := 0
	for _, lease := range b.leases {
		if lease.Resource == ResourceAwake && !lease.held && carding[lease.subject()] {
			live++
		} else if lease.Resource == ResourceAwake && !lease.recordOnly {
			system++
		}
	}
	if system > 0 {
		b.syncSystemAwake(system)
	}
	switch {
	case live > 0 && b.awake == nil && b.keepAwake != nil:
		release, err := b.keepAwake(fmt.Sprintf("%d wake lease(s) of abstraction.resource", live))
		if err != nil {
			// One record per refusal, not one per save while it lasts.
			if !b.awakeRefused {
				b.awakeRefused = true
				b.record(auditRecord{Event: "refused", Resource: ResourceAwake, Answer: oneLine("platform: " + err.Error())})
			}
			return
		}
		b.awake, b.awakeRefused = release, false
	case live == 0:
		b.awakeRefused = false
		if b.awake != nil {
			b.awake()
			b.awake = nil
		}
	}
	if system == 0 {
		b.syncSystemAwake(0)
	}
}

// syncSystemAwake is called under mu alongside the provisional card/display request.
func (b *Book) syncSystemAwake(live int) {
	if live > 0 && b.systemAwake == nil && b.keepSystemAwake != nil {
		release, err := b.keepSystemAwake(fmt.Sprintf("%d system wake lease(s) of abstraction.resource", live))
		if err != nil {
			b.systemError = err
			if !b.systemRefused {
				b.record(auditRecord{Event: "refused", Resource: ResourceAwake, Answer: oneLine("platform: " + err.Error())})
			}
			b.systemRefused = true
			return
		}
		b.systemAwake, b.systemRefused = release, false
		b.systemError = nil
	}
	if live == 0 {
		b.systemRefused = false
		b.systemError = nil
		if b.systemAwake != nil {
			b.systemAwake()
			b.systemAwake = nil
		}
	}
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
	return b.acquire(ctx, who, resource, amount, waitMs, func() error { return b.holdDecision(ctx, peer, resource) })
}

// A freedYield is one yield the instrument confirmed during an Acquire. It
// is written as a freed record when that Acquire ends, so the record can name
// the lease the asker got.
type freedYield struct {
	holder, lease, label, resource string
	amount, took                   int64
}

// An arbitration is one Acquire's running state: the yields it confirmed and
// the person's switch for each holder program it met, decided once.
type arbitration struct {
	ctx   context.Context
	who   Subject
	freed []freedYield
	keeps map[string]bool
}

// keeps reports whether the person keeps live's memory from this asker. An
// asker's own leases are its own to ask back (RES-L9), so the switch never
// keeps them.
func (b *Book) keeps(run *arbitration, live *record) bool {
	if run == nil || live.subject().same(run.who) {
		return false
	}
	kept, decided := run.keeps[live.Program]
	if !decided {
		kept = b.kept(run.ctx, live.Program)
		run.keeps[live.Program] = kept
	}
	return kept
}

func (b *Book) acquire(ctx context.Context, who Subject, resource string, amount, waitMs int64, decide func() error) wire.AcquireResult {
	run := &arbitration{ctx: ctx, who: who, keeps: map[string]bool{}}
	result := b.arbitrate(ctx, who, resource, amount, waitMs, decide, run)
	b.recordEnd(who, resource, result, run)
	return result
}

// recordEnd writes what the person's Memory view reads once an Acquire ends:
// one freed record per confirmed yield, naming the lease the asker got, and,
// for an Acquire that ended insufficient or holders_refused, the largest
// memory no lease covers.
func (b *Book) recordEnd(who Subject, resource string, result wire.AcquireResult, run *arbitration) {
	askerLease := ""
	if result.Lease != nil {
		askerLease = result.Lease.ID
	}
	for _, freed := range run.freed {
		b.record(auditRecord{Event: "freed", Holder: freed.holder, Lease: freed.lease, Label: freed.label,
			Asker: who.Program, AskerLease: askerLease, Resource: freed.resource, Amount: freed.amount, TookMS: freed.took})
	}
	if result.Outcome != wire.AcquireOutcomeInsufficient && result.Outcome != wire.AcquireOutcomeHoldersRefused {
		return
	}
	if program, bytes := b.largestForeign(who, resource); program != "" {
		b.record(auditRecord{Event: "foreign", Asker: who.Program, Holder: program, Resource: resource, Amount: bytes})
	}
}

// largestForeign is the program holding the most of resource that no lease
// covers (RES-T1), other than the asker, and its bytes; "" when the table
// cannot be read or shows no such memory.
func (b *Book) largestForeign(who Subject, resource string) (program string, amount int64) {
	state, err := b.table.Holders(resource, false)
	if err != nil {
		return "", 0
	}
	by := map[string]int64{}
	for _, row := range state.Holders {
		if row.Evidence != wire.EvidenceVerified || row.Lease != "" || row.Amount <= 0 || row.Program == "" ||
			row.Program == who.Program {
			continue
		}
		by[row.Program] += row.Amount
	}
	for name, bytes := range by {
		if bytes > amount || bytes == amount && name < program {
			program, amount = name, bytes
		}
	}
	return program, amount
}

func (b *Book) arbitrate(ctx context.Context, who Subject, resource string, amount, waitMs int64, decide func() error, run *arbitration) wire.AcquireResult {
	asked := []wire.YieldRecord{}
	if ctx.Err() != nil {
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
	}
	if resource == "" || amount < 0 || waitMs < 0 || time.Duration(waitMs)*time.Millisecond > MaxAcquireWait {
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeInvalid, Asked: asked}
	}
	if who.Program == "" {
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeInvalid, Asked: asked}
	}
	switch err := decide(); {
	case errors.Is(err, ErrPolicyUnavailable):
		b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "unavailable"})
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
	case err != nil:
		b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "not_permitted"})
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeNotPermitted, Asked: asked}
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
	// Nothing free without asking makes this call an asker in the same
	// critical section that decided so. From there until it returns, the
	// bytes its yields free are its own: a no-yield acquire of the same
	// resource reads held meanwhile, with no window between (RES-L6).
	result, ok, entered := b.tryGrantOrAsk(ctx, state, samples, pool, who, resource, amount, asked)
	if ok {
		return result
	}
	if !entered {
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
	}
	defer func() {
		b.mu.Lock()
		key := b.table.poolOf(resource)
		b.leaveWaitingLocked(who, resource)
		if b.asking[key]--; b.asking[key] <= 0 {
			delete(b.asking, key)
		}
		b.mu.Unlock()
	}()
	if b.afterFirstTry != nil {
		b.afterFirstTry()
	}
	// The whole eviction is planned before anybody is asked: when no set of
	// holders yielding could make amount fit, nobody is asked, nothing is
	// unloaded, and the asker reads insufficient at once (RES-L9).
	if _, feasible, short := b.plan(resource, amount, pool, nil); !feasible {
		b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount,
			Answer: "insufficient: " + strconv.FormatInt(short, 10) + " bytes short even after every lease yields; nobody was asked"})
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeInsufficient, Asked: asked}
	}
	// The person's switch is decided before anybody is asked: a holder whose
	// memory the person keeps is never asked, and reads refused in asked
	// (RES-L3). When the holders left cannot make amount fit, nobody is.
	asked = append(asked, b.keptRecords(run, resource)...)
	if _, feasible, _ := b.plan(resource, amount, pool, run); !feasible {
		b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "holders_refused"})
		return wire.AcquireResult{Outcome: wire.AcquireOutcomeHoldersRefused, Asked: asked}
	}
	// Each pass below asks whatever RES-L2
	// still has left to ask and then decides the grant under the lock again:
	// the freed bytes a yield reports are not this asker's until nobody else
	// has spent them first (RES-L2). A pass where nothing was actually
	// yielded leaves nothing to gain from asking again, so it stops there;
	// a pass where something was taken by a concurrent grant re-enters the
	// ask sequence, bounded by the caller's own deadline.
	deadline := b.now().Add(time.Duration(waitMs) * time.Millisecond)
	for {
		if ctx.Err() != nil {
			return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
		}
		more := b.ask(ctx, who, resource, amount, pool, deadline, run)
		asked = append(asked, more...)
		if ctx.Err() != nil {
			return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
		}
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
		if result, ok := b.tryGrant(ctx, state, samples, pool, who, resource, amount, asked); ok {
			return result
		}
		if ctx.Err() != nil {
			return wire.AcquireResult{Outcome: wire.AcquireOutcomeUnavailable, Asked: asked}
		}
		if !yielded || !b.now().Before(deadline) {
			break
		}
	}
	b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "holders_refused"})
	return wire.AcquireResult{Outcome: wire.AcquireOutcomeHoldersRefused, Asked: asked}
}

// Free reports what a new reservation could take without yielding. It grants
// nothing; AcquireFree remains the atomic admission decision for later work.
func (b *Book) Free(resource string, fresh bool) (int64, error) {
	if b == nil || b.table == nil {
		return 0, errors.New("resource leases: nil book")
	}
	state, samples, err := b.table.snapshot(resource, fresh)
	if err != nil {
		return 0, err
	}
	pool := b.pool(resource, state.Capacity)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.freeLocked(state, samples, pool), nil
}

// AcquireFree is the no-yield acquire (CONTRACT.md RES-L6): Acquire's
// rights decision and table read, then one grant decision under the lock
// from what is free, and held when it does not fit. It never asks a holder,
// so asked is always empty, and the decision and the grant are one step: no
// read of the table can go stale between them.
func (b *Book) AcquireFree(ctx context.Context, who Subject, peer *identity.Peer, resource string, amount int64) wire.AcquireFreeResult {
	asked := []wire.YieldRecord{}
	if ctx.Err() != nil {
		return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeUnavailable, Asked: asked}
	}
	if resource == "" || amount < 0 || who.Program == "" {
		return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeInvalid, Asked: asked}
	}
	switch err := b.holdDecision(ctx, peer, resource); {
	case errors.Is(err, ErrPolicyUnavailable):
		b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "unavailable"})
		return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeUnavailable, Asked: asked}
	case err != nil:
		b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "not_permitted"})
		return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeNotPermitted, Asked: asked}
	}
	state, samples, err := b.table.snapshot(resource, true)
	if err != nil {
		return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeUnavailable, Asked: asked}
	}
	pool := b.pool(resource, state.Capacity)
	if pool > 0 && amount > pool {
		b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "insufficient"})
		return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeInsufficient, Asked: asked}
	}
	if result, ok := b.tryGrantFree(ctx, state, samples, pool, who, resource, amount); ok {
		return result
	}
	if ctx.Err() != nil {
		return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeUnavailable, Asked: asked}
	}
	b.record(auditRecord{Event: "refused", Asker: who.Program, Resource: resource, Amount: amount, Answer: "held"})
	return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeHeld, Asked: asked}
}

// holdDecision asks the configured policy for ActionHold on the resource. A
// book without one, and a caller with no peer, read unavailable (RES-L1).
func (b *Book) holdDecision(ctx context.Context, peer *identity.Peer, resource string) error {
	return b.hold.Check(ctx, peer, ActionHold, resource)
}

// kept reports whether the person keeps program's memory: ActionYield on
// that program, decided for the service's own program, read an evaluated
// deny. No rule, no policy and a decision that cannot be obtained all ask
// the holder (RES-L3).
func (b *Book) kept(ctx context.Context, program string) bool {
	if b.yield == nil || program == "" {
		return false
	}
	err := b.yield(ctx, program)
	return err != nil && !errors.Is(err, ErrPolicyUnavailable)
}

// pool is what the resource's whole is, for deciding what is free: the
// table's pool rule (Table.Pool) applied to the capacity the caller already
// read.
func (b *Book) pool(resource string, capacity int64) int64 {
	return b.table.pool(resource, capacity, true)
}

// samePool reports whether two resources spend one pool, so a lease on one is
// counted against a request for the other (CONTRACT.md RES-L2).
func (b *Book) samePool(a, c string) bool {
	return a == c || b.table.poolOf(a) == b.table.poolOf(c)
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
	a := b.attributeLocked(state.Resource, state.Held, samples)
	free := pool - a.held
	if free <= 0 {
		return 0
	}
	for holder, amount := range a.granted {
		if short := amount - a.measured[holder]; short > 0 {
			if short >= free {
				return 0
			}
			free -= short
		}
	}
	return free
}

// attribution is who the instrument's samples of one pool are charged to:
// held is the pool's global charge, granted and measured are per lease
// subject, and owners names, for each sample in order, the subject its bytes
// are credited to. A bound child measured through its own handle credits
// nobody through its PID-only row; its owner is the zero Subject.
type attribution struct {
	held     int64
	granted  map[Subject]int64
	measured map[Subject]int64
	owners   []Subject
}

// attributeLocked is the crediting free decides by (RES-L2), for a caller
// holding the lock.
func (b *Book) attributeLocked(resource string, held int64, samples []instrument.Sample) attribution {
	b.expire(b.now())
	granted := map[Subject]int64{}
	bound := map[string]boundCredit{}
	canCredit := b.table.verifiedProcessSamples()
	measured := map[Subject]int64{}
	// children are the bound children measured through their own handle,
	// by PID: the instrument's PID-only row for that PID is charged as it
	// stands and credits nobody, and the handle's figure is credited to
	// the lease's subject once (RES-L2).
	children := map[int]int64{}
	// engines are the bound leases measured through a handle; their engine
	// processes are resolved after every lease is read (engineProcesses).
	var engines []*record
	for _, live := range b.leases {
		if b.samePool(live.Resource, resource) {
			granted[live.subject()] += live.Amount
			if live.ProcessPID <= 0 || live.ProcessStart == "" {
				continue
			}
			key := strconv.Itoa(live.ProcessPID) + "/" + live.ProcessStart
			if canCredit {
				if _, duplicate := bound[key]; !duplicate {
					if current, err := b.inspect(live.ProcessPID); err == nil &&
						current.StartID == live.ProcessStart && current.Account == live.Account {
						bound[key] = boundCredit{live.subject(), current}
					}
				}
			} else if b.child != nil {
				engines = append(engines, live)
			}
		}
	}
	// rowOwners are engine processes whose handle measurement failed: their
	// PID-only rows credit the lease's subject, so the lease and the row
	// are charged as the larger of the two (RES-L2).
	rowOwners := map[int]Subject{}
	for _, live := range engines {
		for _, p := range b.engineProcesses(live, samples) {
			if _, duplicate := children[p.PID]; duplicate {
				continue
			}
			if _, duplicate := rowOwners[p.PID]; duplicate {
				continue
			}
			if used, err := b.child(p.PID, p.StartID); err == nil && used >= 0 {
				children[p.PID] = used
				measured[live.subject()] += used
			} else if p.confirmed {
				rowOwners[p.PID] = live.subject()
			}
		}
	}
	rows := map[int]int64{}
	for _, sample := range samples {
		if _, child := children[sample.PID]; child && sample.StartID == "" {
			rows[sample.PID] += sample.Amount
		}
	}
	for pid, used := range children {
		// The global charge is at least the larger of the two figures.
		if over := used - rows[pid]; over > 0 {
			held += over
		}
	}
	owners := make([]Subject, len(samples))
	for i, sample := range samples {
		if _, child := children[sample.PID]; child && sample.StartID == "" {
			continue
		}
		owner := Subject{Program: sample.Program, Account: sample.Account}
		if owner.Program == "" {
			owner.Program = sample.Image
		}
		if subject, ok := rowOwners[sample.PID]; ok && sample.StartID == "" {
			owner = subject
		}
		if sample.StartID != "" {
			key := strconv.Itoa(sample.PID) + "/" + sample.StartID
			if credited, ok := bound[key]; ok && sample.Account == credited.subject.Account {
				owner = credited.subject
			} else if credited, ok := b.descendantCredit(sample, bound); ok {
				owner = credited
			}
		}
		owners[i] = owner
		measured[owner] += sample.Amount
	}
	return attribution{held: held, granted: granted, measured: measured, owners: owners}
}

// credited is, for each sample of resource in order, the id of the live lease
// its bytes are credited to, or "" for memory no lease covers: foreign memory
// (RES-T1). A subject with more than one lease on the pool is named by its
// oldest.
func (b *Book) credited(resource string, samples []instrument.Sample) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	a := b.attributeLocked(resource, 0, samples)
	oldest := map[Subject]*record{}
	for _, live := range b.leases {
		if !b.samePool(live.Resource, resource) {
			continue
		}
		if was, ok := oldest[live.subject()]; !ok || live.Since.Before(was.Since) ||
			live.Since.Equal(was.Since) && live.ID < was.ID {
			oldest[live.subject()] = live
		}
	}
	out := make([]string, len(samples))
	for i, owner := range a.owners {
		if live, ok := oldest[owner]; ok {
			out[i] = live.ID
		}
	}
	return out
}

// engineProcess is one process of a bound engine: the bound process or one
// of its direct children inside the engine's folder. confirmed is set when
// the process at that PID was inspected again and is that generation, so
// its PID-only row may stand in for a failed handle measurement.
type engineProcess struct {
	PID       int
	StartID   string
	confirmed bool
}

// engineProcesses is a handle-measured lease's engine: its bound process,
// then each direct child of it that the instrument shows holding the
// resource, runs as the same account, started after it, and runs an image
// inside the engine's folder (engineFolder). A venv launcher is the bound
// process of a Python engine and the interpreter it starts holds the
// memory; without the child, that memory was charged as foreign use beside
// the lease's full reservation (RES-L2). Only one level is followed.
func (b *Book) engineProcesses(live *record, samples []instrument.Sample) []engineProcess {
	bound, err := b.inspect(live.ProcessPID)
	if err != nil || bound.PID != live.ProcessPID || bound.StartID != live.ProcessStart || bound.Account != live.Account {
		return []engineProcess{{PID: live.ProcessPID, StartID: live.ProcessStart}}
	}
	out := []engineProcess{{PID: live.ProcessPID, StartID: live.ProcessStart, confirmed: true}}
	path := engineFolder(bound.Image)
	if path == "" {
		return out
	}
	folder, err := fileid.Open(context.Background(), fileid.Self, path, fileid.Options{})
	if err != nil {
		return out
	}
	defer folder.Close()
	folderID := folder.FileID()
	resolved := folderID.Path()
	if !folderID.IsDir() || filepath.Dir(resolved) == resolved {
		return out
	}
	if base, ok := venvEngineFolder(folder); ok {
		defer base.Close()
		folder = base
	}
	for _, sample := range samples {
		if sample.PID <= 0 || sample.PID == live.ProcessPID || sample.StartID != "" {
			continue
		}
		child, err := b.inspect(sample.PID)
		if err != nil || child.PID != sample.PID || child.ParentPID != live.ProcessPID || child.StartID == "" ||
			child.Account != live.Account || child.Started.IsZero() || child.Started.Before(bound.Started) ||
			!insideFolder(child.Image, folder) {
			continue
		}
		out = append(out, engineProcess{PID: child.PID, StartID: child.StartID, confirmed: true})
	}
	return out
}

// engineFolder names the initial directory of the observed process image.
// engineProcesses resolves the directory before crediting any child.
func engineFolder(image string) string {
	if !filepath.IsAbs(image) || fileid.LocalPath(image) != nil {
		return ""
	}
	dir := filepath.Dir(filepath.Clean(image))
	// A volume root holds every program on the volume; an engine there has
	// no folder of its own, and only its bound process is its own.
	if filepath.Dir(dir) == dir {
		return ""
	}
	return dir
}

// venvEngineFolder admits the enclosing engine directory for the standard
// virtualenv layout. The launcher and marker must be reached from that
// held directory without following a link inside it.
func venvEngineFolder(launcher fileid.Handle) (fileid.Handle, bool) {
	var none fileid.Handle
	launcherID := launcher.FileID()
	name := filepath.Base(launcherID.Path())
	if name != "Scripts" && name != "bin" {
		return none, false
	}
	venv := filepath.Dir(launcherID.Path())
	parent := filepath.Dir(venv)
	if filepath.Dir(parent) == parent {
		return none, false
	}
	engine, err := fileid.Open(context.Background(), fileid.Self, parent, fileid.Options{})
	if err != nil {
		return none, false
	}
	keep := false
	defer func() {
		if !keep {
			engine.Close()
		}
	}()
	actual := engine.FileID().Path()
	if filepath.Dir(actual) == actual {
		return none, false
	}
	venvDir, err := engine.Beneath(filepath.Base(venv), fileid.Options{})
	if err != nil {
		return none, false
	}
	defer venvDir.Close()
	scripts, err := venvDir.Beneath(name, fileid.Options{})
	if err != nil {
		return none, false
	}
	defer scripts.Close()
	if !scripts.FileID().Equal(launcherID) {
		return none, false
	}
	marker, err := venvDir.Beneath("pyvenv.cfg", fileid.Options{})
	if err != nil {
		return none, false
	}
	defer marker.Close()
	info, err := marker.File().Stat()
	if err != nil || !info.Mode().IsRegular() {
		return none, false
	}
	relation, err := launcher.Relate(engine)
	if err != nil || relation.Kind != fileid.RelationInside {
		return none, false
	}
	keep = true
	return engine, true
}

// insideFolder checks the live image against the held engine directory.
func insideFolder(path string, folder fileid.Handle) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	image, err := fileid.Open(context.Background(), fileid.Self, path, fileid.Options{})
	if err != nil {
		return false
	}
	defer image.Close()
	relation, err := image.Relate(folder)
	return err == nil && !image.FileID().IsDir() && relation.Kind == fileid.RelationInside
}

// boundCredit is a live lease's rechecked process generation, local to one
// locked accounting calculation. It carries no new binding authority.
type boundCredit struct {
	subject Subject
	process instrument.ProcessInfo
}

// descendantCredit reconciles generation-bearing Linux samples with the
// reservation of their bound ancestor. PID-only instruments never populate
// bound. Global measured bytes stay charged; only reservation credit moves.
func (b *Book) descendantCredit(sample instrument.Sample, bound map[string]boundCredit) (Subject, bool) {
	var none Subject
	if len(bound) == 0 || sample.PID <= 0 || sample.StartID == "" || sample.Account == "" {
		return none, false
	}
	leaf, err := b.inspect(sample.PID)
	if err != nil || leaf.PID != sample.PID || leaf.StartID != sample.StartID ||
		leaf.Account != sample.Account || leaf.BootID == "" || leaf.StartTicks == 0 {
		return none, false
	}
	// No ancestor older than the earliest relevant bound generation can
	// match. This ends the walk at that generation instead of requiring
	// observation of unrelated ancestors above every lease's process.
	var earliest uint64
	for _, root := range bound {
		p := root.process
		if p.Account == leaf.Account && p.BootID == leaf.BootID && p.StartTicks > 0 &&
			(earliest == 0 || p.StartTicks < earliest) {
			earliest = p.StartTicks
		}
	}
	if earliest == 0 {
		return none, false
	}
	chain := make([]instrument.ProcessInfo, 0, 8)
	seen := map[int]bool{}
	owners := map[Subject]bool{}
	current := leaf
	for depth := 0; depth < 128; depth++ {
		if current.PID <= 0 || seen[current.PID] {
			return none, false
		}
		seen[current.PID] = true
		chain = append(chain, current)
		key := strconv.Itoa(current.PID) + "/" + current.StartID
		if root, ok := bound[key]; ok && root.subject.Account == current.Account &&
			sameCreditProcess(root.process, current) {
			owners[root.subject] = true
		}
		if current.StartTicks <= earliest {
			if len(owners) != 1 {
				return none, false
			}
			for i := len(chain) - 1; i >= 0; i-- {
				again, err := b.inspect(chain[i].PID)
				if err != nil || !sameCreditProcess(chain[i], again) {
					return none, false
				}
			}
			for owner := range owners {
				return owner, true
			}
		}
		parent, err := b.inspect(current.ParentPID)
		if err != nil || parent.PID != current.ParentPID || parent.StartID == "" ||
			parent.Account != leaf.Account || parent.BootID != leaf.BootID ||
			parent.StartTicks == 0 || parent.StartTicks >= current.StartTicks {
			return none, false
		}
		current = parent
	}
	return none, false
}

func sameCreditProcess(a, c instrument.ProcessInfo) bool {
	// Started is Linux's approximate wall-clock description. The raw boot
	// and ticks, including the observed parent link, establish identity.
	return a.PID == c.PID && a.ParentPID == c.ParentPID && a.StartID == c.StartID &&
		a.Account == c.Account && a.BootID == c.BootID && a.StartTicks == c.StartTicks
}

// ask puts the yield requests of one Acquire, in the contract's order: the
// leases on this resource in the order their grants say, then it stops
// (RES-L2). It returns every ask it made, whatever each answered.
func (b *Book) ask(ctx context.Context, who Subject, resource string, amount, pool int64, deadline time.Time, run *arbitration) []wire.YieldRecord {
	asked := []wire.YieldRecord{}
	set, _, _ := b.plan(resource, amount, pool, run)
	for _, live := range set {
		if !b.now().Before(deadline) {
			break
		}
		// A lease in the same pool on a sibling resource is asked about
		// its own resource, whose instrument shows the bytes leave.
		asked = append(asked, b.askLease(ctx, who, live, live.Resource, deadline, run))
		if state, samples, err := b.table.snapshot(resource, true); err == nil && b.free(state, samples, pool) >= amount {
			return asked
		}
	}
	return asked
}

// measured is what the instrument attributes to this resource now. A holder's
// engine can run in processes the instrument does not attribute to its lease,
// so the bytes a yield freed are read from the resource's total, not from one
// row.
func (b *Book) measured(resource, _ string) int64 {
	state, err := b.table.Holders(resource, true)
	if err != nil {
		return 0
	}
	return state.Held
}

// settled waits for the bytes to leave the instrument and returns how many
// did. It returns as soon as the figure falls, which the measurement showed
// happens within one poll of the engine's own answer.
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

// plan is the eviction a request for amount needs before anybody is asked
// (RES-L9): the smallest set of live leases on this resource's pool whose
// grants cover what is short, the asker's own other leases among them, so
// a provider swapping one model for another asks its own idle model first.
// A lease frees at least its grant once its holder yields: its unoccupied
// part leaves the reservation, its measured part leaves the instrument.
// The set is the one freeing the fewest bytes, then the one with the fewest
// leases, asked in the order their grants say (leaseOrder).
//
// feasible is false when even every lease yielding leaves amount short;
// short is then how many bytes are missing. A non-nil run leaves out the
// leases whose memory the person keeps (RES-L3).
func (b *Book) plan(resource string, amount, pool int64, run *arbitration) (set []*record, feasible bool, short int64) {
	state, samples, err := b.table.snapshot(resource, true)
	if err != nil || pool <= 0 {
		// A machine with no pool never reads insufficient (RES-L2): every
		// lease is asked in order.
		return b.askable(resource, run), true, 0
	}
	short = amount - b.free(state, samples, pool)
	if short <= 0 {
		return nil, true, 0
	}
	order := b.askable(resource, run)
	var all int64
	for _, live := range order {
		all += live.Amount
	}
	if all < short {
		return nil, false, short - all
	}
	return smallestCover(order, short), true, short
}

// smallestCover is the subset of order whose grants sum to at least short
// with the fewest bytes, then the fewest leases, kept in order. A long
// order is covered greedily in order instead.
func smallestCover(order []*record, short int64) []*record {
	const exact = 16
	if len(order) > exact {
		var out []*record
		var sum int64
		for _, live := range order {
			if sum >= short {
				break
			}
			out = append(out, live)
			sum += live.Amount
		}
		return out
	}
	best, bestSum, bestCount := -1, int64(0), 0
	for mask := 1; mask < 1<<len(order); mask++ {
		var sum int64
		count := 0
		for i, live := range order {
			if mask&(1<<i) != 0 {
				sum += live.Amount
				count++
			}
		}
		if sum < short {
			continue
		}
		if best < 0 || sum < bestSum || sum == bestSum && count < bestCount {
			best, bestSum, bestCount = mask, sum, count
		}
	}
	var out []*record
	for i, live := range order {
		if best&(1<<i) != 0 {
			out = append(out, live)
		}
	}
	return out
}

// askable is leaseOrder less the leases whose memory the person keeps from
// run's asker; a nil run leaves out none.
func (b *Book) askable(resource string, run *arbitration) []*record {
	order := b.leaseOrder(resource)
	if run == nil {
		return order
	}
	out := order[:0:0]
	for _, live := range order {
		if !b.keeps(run, live) {
			out = append(out, live)
		}
	}
	return out
}

// keptRecords is one refused record for each lease on resource's pool whose
// memory the person keeps from run's asker: the holder is never asked, and
// the record says why (RES-L3).
func (b *Book) keptRecords(run *arbitration, resource string) []wire.YieldRecord {
	var out []wire.YieldRecord
	for _, live := range b.leaseOrder(resource) {
		if !b.keeps(run, live) {
			continue
		}
		word := AnswerRefused + ": the person keeps " + programTitle(live.Program) + "'s memory"
		b.record(auditRecord{Event: "ask", Asker: run.who.Program, Holder: live.Program, Lease: live.ID,
			Resource: live.Resource, Requested: live.Amount, Answer: word})
		out = append(out, wire.YieldRecord{Holder: live.Program, Answer: word})
	}
	return out
}

// programTitle is how a sentence names a program the book names by its image
// path: the file name without .exe. A package family reads as its package
// name.
func programTitle(program string) string {
	name := program
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	} else {
		name = strings.TrimPrefix(name, identity.PackagedProgramPrefix)
		if i := strings.LastIndexByte(name, '_'); i > 0 && len(name)-i-1 == publisherIDLength {
			name = name[:i]
		}
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".exe"), ".EXE")
	if name == "" {
		return program
	}
	return name
}

// leaseOrder is the live leases on this resource's pool, in the order their
// grants say. A grant that names no order is asked after the grants that do,
// and two leases under one grant are asked oldest first.
func (b *Book) leaseOrder(resource string) []*record {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(b.now())
	var out []*record
	for _, live := range b.leases {
		if b.samePool(live.Resource, resource) {
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
func (b *Book) askLease(ctx context.Context, who Subject, live *record, resource string, deadline time.Time, run *arbitration) wire.YieldRecord {
	began := b.now()
	b.mu.Lock()
	label := live.Label
	b.mu.Unlock()
	before := b.measured(resource, live.Program)
	b.noteWaiting(who, resource, live.Program)
	b.mu.Lock()
	b.seq++
	q := &question{holder: live.subject(), lease: live.ID, seq: b.seq, done: make(chan struct{}), before: before,
		request: wire.YieldRequest{ID: "ask-" + strconv.FormatUint(b.seq, 10), Resource: resource,
			Amount: live.Amount, Reason: who.Program, At: stamp(began), Lease: live.ID}}
	b.waiting[q.seq] = q
	b.mu.Unlock()
	b.observe.Advance()
	defer func() {
		b.mu.Lock()
		delete(b.waiting, q.seq)
		b.mu.Unlock()
		b.observe.Advance()
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
	record := answer(AnswerYielded, freed)
	if run != nil {
		run.freed = append(run.freed, freedYield{holder: live.Program, lease: live.ID, label: label,
			resource: resource, amount: freed, took: record.TookMs})
	}
	return record
}

// tryGrant decides and writes a grant as one step under the lock: the bytes
// free at that moment (freeLocked, against the same state and pool the
// caller already read) and the lease that spends them are one critical
// section, so a concurrent tryGrant on the same book cannot also spend them
// (RES-L2). It reports whether the grant was made; a caller that loses the
// race asks again or reads holders_refused.
func (b *Book) tryGrant(ctx context.Context, state wire.ResourceState, samples []instrument.Sample, pool int64, who Subject, resource string, amount int64, asked []wire.YieldRecord) (wire.AcquireResult, bool) {
	b.mu.Lock()
	if ctx.Err() != nil {
		b.mu.Unlock()
		return wire.AcquireResult{}, false
	}
	if b.freeLocked(state, samples, pool) < amount {
		b.mu.Unlock()
		return wire.AcquireResult{}, false
	}
	lease := b.grantLocked(who, resource, amount)
	b.mu.Unlock()
	b.record(auditRecord{Event: "acquired", Asker: who.Program, Lease: lease.ID, Resource: resource, Amount: amount, Answer: "acquired"})
	return wire.AcquireResult{Outcome: wire.AcquireOutcomeAcquired, Lease: &lease, Asked: asked}, true
}

// tryGrantOrAsk is Acquire's first grant decision. When the grant does not
// fit and the call is still live, it counts the caller as asking on resource
// before it releases the lock, so no no-yield acquire can be decided between
// the failed decision and the ask (RES-L6). entered reports that count; the
// caller removes it when it returns.
func (b *Book) tryGrantOrAsk(ctx context.Context, state wire.ResourceState, samples []instrument.Sample, pool int64, who Subject, resource string, amount int64, asked []wire.YieldRecord) (result wire.AcquireResult, ok, entered bool) {
	b.mu.Lock()
	if ctx.Err() != nil {
		b.mu.Unlock()
		return wire.AcquireResult{}, false, false
	}
	if b.freeLocked(state, samples, pool) < amount {
		b.asking[b.table.poolOf(resource)]++
		b.enterWaitingLocked(who, resource)
		b.mu.Unlock()
		return wire.AcquireResult{}, false, true
	}
	lease := b.grantLocked(who, resource, amount)
	b.mu.Unlock()
	b.record(auditRecord{Event: "acquired", Asker: who.Program, Lease: lease.ID, Resource: resource, Amount: amount, Answer: "acquired"})
	return wire.AcquireResult{Outcome: wire.AcquireOutcomeAcquired, Lease: &lease, Asked: asked}, true, false
}

// tryGrantFree is AcquireFree's one step under the lock: nothing granted
// while a plain Acquire of the resource asks holders to yield, else
// tryGrant's arithmetic (RES-L6).
func (b *Book) tryGrantFree(ctx context.Context, state wire.ResourceState, samples []instrument.Sample, pool int64, who Subject, resource string, amount int64) (wire.AcquireFreeResult, bool) {
	b.mu.Lock()
	if ctx.Err() != nil || b.asking[b.table.poolOf(resource)] > 0 || pool <= 0 && b.leasedLocked(resource) || b.freeLocked(state, samples, pool) < amount {
		b.mu.Unlock()
		return wire.AcquireFreeResult{}, false
	}
	lease := b.grantLocked(who, resource, amount)
	b.mu.Unlock()
	b.record(auditRecord{Event: "acquired", Asker: who.Program, Lease: lease.ID, Resource: resource, Amount: amount, Answer: "acquired"})
	return wire.AcquireFreeResult{Outcome: wire.AcquireFreeOutcomeAcquired, Lease: &lease, Asked: []wire.YieldRecord{}}, true
}

// leasedLocked reports whether a live lease of resource exists. A resource
// with no pool is free to a no-yield acquire only while nothing holds it,
// and a lease granted since the table was read holds it (RES-L6). The
// caller holds the lock.
func (b *Book) leasedLocked(resource string) bool {
	b.expire(b.now())
	for _, live := range b.leases {
		if b.samePool(live.Resource, resource) {
			return true
		}
	}
	return false
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

// Renew extends the caller's own lease (RES-L4). An acquired lease, of a card
// or of awake alike, is renewed only while ActionHold on its resource still
// permits its holder, decided again for each renewal and fail-closed
// (RES-L7): a refusal ends the lease now. A decision that cannot be obtained
// leaves the lease standing with its renew_by unmoved and reports it applied,
// so one transient outage costs the holder nothing, and the lease ends at
// that renew_by only when every decision in the lifetime fails.
func (b *Book) Renew(who Subject, id string) wire.LeaseChange {
	unavailable := false
	if err := b.standingOf(who, id); err != nil {
		if !errors.Is(err, ErrPolicyUnavailable) {
			b.release(id, "revoked")
			return wire.LeaseChange{}
		}
		unavailable = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(b.now())
	live, ok := b.leases[id]
	if !ok || !live.subject().same(who) {
		return wire.LeaseChange{}
	}
	if unavailable {
		lease := live.lease()
		return wire.LeaseChange{Applied: true, Lease: &lease}
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
	if !b.table.verifiedProcessSamples() && b.child == nil {
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

// MaxLabel bounds a lease label in bytes (Label).
const MaxLabel = 200

// Label names what the caller's own lease holds, as app: model, for a person
// reading the machine's memory. The text is kept to one line of at most
// MaxLabel bytes; empty clears it. It is display only: it rides on the caller
// owning the lease and carries no authority. A changed label is one audit
// record.
func (b *Book) Label(who Subject, id, text string) wire.LeaseChange {
	text = labelText(text)
	b.mu.Lock()
	b.expire(b.now())
	live, ok := b.leases[id]
	if !ok || !live.subject().same(who) {
		b.mu.Unlock()
		return wire.LeaseChange{}
	}
	changed := live.Label != text
	if changed {
		live.Label = text
		b.save()
	}
	lease := live.lease()
	b.mu.Unlock()
	if changed {
		b.record(auditRecord{Event: "label", Lease: id, Holder: live.Program, Label: text})
	}
	return wire.LeaseChange{Applied: true, Lease: &lease}
}

// labelText is text as one line of at most MaxLabel bytes, cut at a
// character boundary.
func labelText(text string) string {
	// A label is display text in another program's Panel line: format
	// characters (bidirectional overrides among them) and every control
	// character go.
	text = oneLine(strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, text))
	if len(text) <= MaxLabel {
		return text
	}
	cut := MaxLabel
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut])
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
// Cursors expire when the Book is reopened. Malformed and expired cursors
// return InvalidRequest; the existing leases@1 wire has no distinct gap
// outcome. An empty cursor reads the current pending requests.
func (b *Book) Observe(ctx context.Context, who Subject, cursor string, waitMs int64) (wire.YieldPage, error) {
	return b.observeContext(ctx, who, cursor, waitMs, func() error { return nil })
}

var errObserveBusy = errors.New("resource: observation waiters full")

func (b *Book) observeContext(ctx context.Context, who Subject, cursor string, waitMs int64, recheck func() error) (wire.YieldPage, error) {
	if waitMs < 0 || time.Duration(waitMs)*time.Millisecond > MaxObserveWait {
		return wire.YieldPage{}, &wire.ServiceError{Code: wire.ServiceErrorCodeInvalidRequest, Message: "wait_ms is 0..30000"}
	}
	from := uint64(0)
	if cursor != "" {
		epoch, position, ok := strings.Cut(cursor, ":")
		var err error
		from, err = strconv.ParseUint(position, 10, 64)
		if !ok || epoch != b.epoch || err != nil || position != strconv.FormatUint(from, 10) {
			return wire.YieldPage{}, &wire.ServiceError{Code: wire.ServiceErrorCodeInvalidRequest, Message: "invalid or expired yield cursor"}
		}
	}
	var page wire.YieldPage
	outcome, err := b.observe.Wait(ctx, time.Duration(waitMs)*time.Millisecond, watch.Probe{
		Read:    func() (bool, error) { page = b.page(who, from); return len(page.Requests) > 0, nil },
		Recheck: recheck,
	})
	if ctx.Err() != nil {
		return wire.YieldPage{Requests: []wire.YieldRequest{}, Next: b.cursor(from)}, nil
	}
	if err != nil {
		return wire.YieldPage{}, err
	}
	if outcome == watch.Busy {
		return wire.YieldPage{}, errObserveBusy
	}
	return page, nil
}

func (b *Book) cursor(position uint64) string {
	return b.epoch + ":" + strconv.FormatUint(position, 10)
}

func (b *Book) page(who Subject, from uint64) wire.YieldPage {
	b.mu.Lock()
	defer b.mu.Unlock()
	page := wire.YieldPage{Requests: []wire.YieldRequest{}, Next: b.cursor(from)}
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
	page.Next = b.cursor(last)
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

// WithNoLease executes a short native effect under lease admission for the
// exact resource. release and acquisition cannot cross this check and effect.
func (b *Book) WithNoLease(resource string, effect func() error) (busy bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for _, live := range b.leases {
		if live.Resource == resource && (live.held || live.RenewBy.After(now)) {
			return true, nil
		}
	}
	return false, effect()
}

// save writes the book. The caller holds the lock. A write that fails is
// reported through the audit and does not end the call: a lease the service is
// keeping in memory is still a lease until it expires.
func (b *Book) save() {
	b.syncAwake()
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
