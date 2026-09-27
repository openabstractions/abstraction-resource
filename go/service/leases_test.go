package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// holder is an attached holder the test drives: what it says it holds, what
// its yield does to the instrument, and whether its host refuses.
type holder struct {
	name     string
	resource string
	holding  []string
	frees    int64
	refuse   error
	in       *fake

	mu    sync.Mutex
	asked int
	order *[]string
}

func (h *holder) Holder() string   { return h.name }
func (h *holder) Resource() string { return h.resource }

func (h *holder) Holding(context.Context) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.holding, nil
}

func (h *holder) Yield(context.Context) error {
	h.mu.Lock()
	h.asked++
	if h.order != nil {
		*h.order = append(*h.order, h.name)
	}
	refuse := h.refuse
	h.mu.Unlock()
	if refuse != nil {
		return refuse
	}
	// A yield through a host's own API frees what the instrument then shows.
	h.in.mu.Lock()
	for i := range h.in.samples {
		if h.in.samples[i].Amount >= h.frees {
			h.in.samples[i].Amount -= h.frees
			break
		}
	}
	h.in.at = time.Now()
	h.in.mu.Unlock()
	h.mu.Lock()
	h.holding = nil
	h.mu.Unlock()
	return nil
}

func (h *holder) times() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.asked
}

// The card of the measurement: a pool of 120 GiB, one process holding 20.
func cardBook(t *testing.T, in *fake, options BookOptions) *Book {
	t.Helper()
	options.Table = NewTable(in, nil, time.Nanosecond)
	if options.Settle == 0 {
		options.Settle = 2 * time.Second
	}
	book, err := OpenBook(options)
	if err != nil {
		t.Fatal(err)
	}
	// The instrument here reports a capacity, so the pool is the instrument's
	// and never the machine's own memory: the test's arithmetic is its own.
	return book
}

func card(capacity int64, held ...int64) *fake {
	in := &fake{name: instrument.NameWindowsGPUCounters, offers: []string{instrument.Card0},
		at: time.Now(), cap: capacity}
	for i, amount := range held {
		in.samples = append(in.samples, instrument.Sample{PID: 100 + i, Program: `C:\lms\llama-server.exe`,
			Account: "S-1-5-21-7-1001", Amount: amount})
	}
	return in
}

const gib = int64(1) << 30

func asker() Subject { return Subject{Program: `C:\comfy\python.exe`, Account: "S-1-5-21-7-1001"} }

// A request inside what is free is granted at once and asks nobody: the
// service exists to arbitrate scarcity, and there is no scarcity here.
func TestAcquireInsideWhatIsFreeAsksNobody(t *testing.T) {
	in := card(120*gib, 20*gib)
	asked := []string{}
	lm := &holder{name: "host:lmstudio", resource: instrument.Card0, holding: []string{"gemma"}, frees: 20 * gib, in: in, order: &asked}
	book := cardBook(t, in, BookOptions{Attached: []Attached{lm}})
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 10*gib, 1000)
	if result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("10 GiB of 100 free read %s", result.Outcome)
	}
	if len(result.Asked) != 0 {
		t.Fatalf("a request inside what is free asked %+v", result.Asked)
	}
	if lm.times() != 0 {
		t.Fatal("a host was unloaded although nothing was scarce")
	}
	if result.Lease == nil || result.Lease.Amount != 10*gib {
		t.Fatalf("lease %+v", result.Lease)
	}
}

// More than the instrument says the card is, is insufficient before any
// holder is asked: nobody yielding could make it fit.
func TestAcquireBeyondCapacityIsInsufficient(t *testing.T) {
	in := card(120*gib, 20*gib)
	lm := &holder{name: "host:lmstudio", resource: instrument.Card0, holding: []string{"gemma"}, frees: 20 * gib, in: in}
	book := cardBook(t, in, BookOptions{Attached: []Attached{lm}})
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 200*gib, 1000)
	if result.Outcome != wire.AcquireOutcomeInsufficient {
		t.Fatalf("200 GiB of a 120 GiB card read %s", result.Outcome)
	}
	if len(result.Asked) != 0 || lm.times() != 0 {
		t.Fatal("a request larger than the card asked a holder anyway")
	}
}

// Attached holders are asked idle longest first, and the asking stops as soon
// as the bytes are there (CONTRACT.md RES-L2).
func TestAttachedHoldersAreAskedIdleLongestFirst(t *testing.T) {
	in := card(120*gib, 40*gib, 40*gib)
	asked := []string{}
	clock := time.Now()
	older := &holder{name: "host:ollama", resource: instrument.Card0, holding: []string{"llama"}, frees: 40 * gib, in: in, order: &asked}
	newer := &holder{name: "host:lmstudio", resource: instrument.Card0, holding: []string{"gemma"}, frees: 40 * gib, in: in, order: &asked}
	book := cardBook(t, in, BookOptions{Attached: []Attached{newer, older},
		Now: func() time.Time { return clock }})
	// Both are seen holding at the same instant, then lmstudio's list changes
	// and ollama's does not: ollama has been idle longer.
	book.mark(context.Background(), instrument.Card0)
	clock = clock.Add(time.Minute)
	newer.holding = []string{"gemma", "qwen"}
	book.mark(context.Background(), instrument.Card0)
	book.now = time.Now

	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 60*gib, 5000)
	if result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("outcome %s after %+v", result.Outcome, result.Asked)
	}
	if len(asked) == 0 || asked[0] != "host:ollama" {
		t.Fatalf("asked in order %v; the holder idle longest is not first", asked)
	}
	if len(result.Asked) != 1 || result.Asked[0].Answer != AnswerYielded {
		t.Fatalf("asked %+v; one yield covered the request and a second holder was asked anyway", result.Asked)
	}
	if result.Asked[0].Amount != 40*gib {
		t.Fatalf("the record says %d bytes freed, not what the instrument showed", result.Asked[0].Amount)
	}
}

// A host whose own API refuses is recorded as a refusal with its reason, and
// the asker is told holders_refused before it allocates anything.
func TestRefusingHolderIsRecordedAndTheAskerAllocatesNothing(t *testing.T) {
	in := card(120*gib, 100*gib)
	lm := &holder{name: "host:lmstudio", resource: instrument.Card0, holding: []string{"gemma"},
		frees: 100 * gib, in: in, refuse: errors.New("model is generating")}
	book := cardBook(t, in, BookOptions{Attached: []Attached{lm}})
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 60*gib, 2000)
	if result.Outcome != wire.AcquireOutcomeHoldersRefused {
		t.Fatalf("a refusing holder read %s", result.Outcome)
	}
	if len(result.Asked) != 1 {
		t.Fatalf("asked %+v", result.Asked)
	}
	if !strings.HasPrefix(result.Asked[0].Answer, AnswerRefused) || !strings.Contains(result.Asked[0].Answer, "generating") {
		t.Fatalf("the refusal lost its reason: %q", result.Asked[0].Answer)
	}
	if len(book.Leases()) != 0 {
		t.Fatal("a refused acquire left a lease behind")
	}
}

// A host that answers and frees nothing inside the wait is unanswered, not
// yielded: the instrument decides, never the host's own reply
// (CONTRACT.md RES-L3).
func TestHolderThatFreesNothingInsideTheWaitIsUnanswered(t *testing.T) {
	in := card(120*gib, 100*gib)
	lm := &holder{name: "host:lmstudio", resource: instrument.Card0, holding: []string{"gemma"}, frees: 0, in: in}
	book := cardBook(t, in, BookOptions{Attached: []Attached{lm}, Settle: 300 * time.Millisecond})
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 60*gib, 2000)
	if result.Outcome != wire.AcquireOutcomeHoldersRefused {
		t.Fatalf("outcome %s", result.Outcome)
	}
	if len(result.Asked) != 1 || result.Asked[0].Answer != AnswerUnanswered {
		t.Fatalf("a host that freed nothing answered %+v", result.Asked)
	}
	if lm.times() != 1 {
		t.Fatalf("the host was asked %d times", lm.times())
	}
}

// A lease not renewed by renew_by ends; a renewed one stands (RES-L4).
func TestLeaseEndsUnlessItIsRenewed(t *testing.T) {
	in := card(120*gib, 0)
	clock := time.Now()
	book := cardBook(t, in, BookOptions{Lifetime: time.Minute, Now: func() time.Time { return clock }})
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 10*gib, 0)
	if result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatal(result.Outcome)
	}
	id := result.Lease.ID
	clock = clock.Add(30 * time.Second)
	change := book.Renew(asker(), id)
	if !change.Applied {
		t.Fatal("the holder could not renew its own lease")
	}
	if book.Renew(Subject{Program: `C:\other.exe`}, id).Applied {
		t.Fatal("another program renewed a lease that is not its own")
	}
	clock = clock.Add(30 * time.Second)
	if len(book.Leases()) != 1 {
		t.Fatal("a renewed lease ended at the original renew_by")
	}
	clock = clock.Add(61 * time.Second)
	book.Expire()
	if len(book.Leases()) != 0 {
		t.Fatal("a lease nobody renewed is still standing")
	}
}

// Leases live in the runtime's state, so a restart keeps them and an expired
// one does not come back.
func TestLeasesSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources", "leases.json")
	in := card(120*gib, 0)
	clock := time.Now()
	book := cardBook(t, in, BookOptions{Path: path, Lifetime: time.Minute, Now: func() time.Time { return clock }})
	kept := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 10*gib, 0)
	short := book.Acquire(context.Background(), Subject{Program: `C:\brief.exe`}, nil, instrument.Card0, gib, 0)
	if kept.Outcome != wire.AcquireOutcomeAcquired || short.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("%s %s", kept.Outcome, short.Outcome)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the lease book was not written: %v", err)
	}

	later := clock.Add(30 * time.Second)
	restarted := cardBook(t, in, BookOptions{Path: path, Lifetime: time.Minute, Now: func() time.Time { return later }})
	live := restarted.Leases()
	if len(live) != 2 {
		t.Fatalf("a restart kept %d of two leases", len(live))
	}
	// A new lease after the restart never reissues an id the book already had.
	again := restarted.Acquire(context.Background(), asker(), nil, instrument.Card0, gib, 0)
	for _, was := range live {
		if again.Lease.ID == was.ID {
			t.Fatalf("the restart reissued lease id %s", was.ID)
		}
	}

	past := clock.Add(2 * time.Minute)
	ended := cardBook(t, in, BookOptions{Path: path, Lifetime: time.Minute, Now: func() time.Time { return past }})
	if len(ended.Leases()) != 0 {
		t.Fatalf("a restart revived leases nobody renewed: %+v", ended.Leases())
	}
}

// A program with no rule for abstraction.resource/hold holds nothing, and a
// decision point that cannot answer is never a permit (RES-L1).
func TestAcquireDecidesTheRuleBeforeItReadsTheTable(t *testing.T) {
	in := card(120*gib, 0)
	refused := cardBook(t, in, BookOptions{Hold: func(context.Context, *identity.Peer, string, string) error {
		return errors.New("no rule")
	}})
	peer := &identity.Peer{}
	result := refused.Acquire(context.Background(), asker(), peer, instrument.Card0, gib, 0)
	if result.Outcome != wire.AcquireOutcomeNotPermitted {
		t.Fatalf("a program with no rule read %s", result.Outcome)
	}
	if in.reads != 0 {
		t.Fatal("the table was read before the rule was decided")
	}

	unavailable := cardBook(t, in, BookOptions{Hold: func(context.Context, *identity.Peer, string, string) error {
		return ErrPolicyUnavailable
	}})
	if out := unavailable.Acquire(context.Background(), asker(), peer, instrument.Card0, gib, 0); out.Outcome != wire.AcquireOutcomeUnavailable {
		t.Fatalf("a decision point that cannot answer read %s", out.Outcome)
	}

	// A caller the host could not bind has no peer, and an unbindable caller
	// is not a permitted one.
	if out := refused.Acquire(context.Background(), asker(), nil, instrument.Card0, gib, 0); out.Outcome != wire.AcquireOutcomeUnavailable {
		t.Fatalf("an unbound caller read %s", out.Outcome)
	}

	permitted := cardBook(t, in, BookOptions{Hold: func(_ context.Context, _ *identity.Peer, action, resource string) error {
		if action != ActionHold || resource != instrument.Card0 {
			t.Fatalf("decided %s on %s", action, resource)
		}
		return nil
	}})
	if out := permitted.Acquire(context.Background(), asker(), peer, instrument.Card0, gib, 0); out.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("a permitted program read %s", out.Outcome)
	}
}

// A rule that refuses yields for one host leaves that host holding, and the
// refusal carries the host's name (RES-L3).
func TestARuleAgainstYieldingOneHostLeavesItHolding(t *testing.T) {
	in := card(120*gib, 100*gib)
	lm := &holder{name: "host:lmstudio", resource: instrument.Card0, holding: []string{"gemma"}, frees: 100 * gib, in: in}
	book := cardBook(t, in, BookOptions{Attached: []Attached{lm}, Yield: func(_ context.Context, holder string) error {
		if holder != "host:lmstudio" {
			return nil
		}
		return errors.New("denied")
	}})
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 60*gib, 2000)
	if result.Outcome != wire.AcquireOutcomeHoldersRefused {
		t.Fatalf("outcome %s", result.Outcome)
	}
	if lm.times() != 0 {
		t.Fatal("a host with a rule against yielding was unloaded anyway")
	}
	if len(result.Asked) != 1 || !strings.Contains(result.Asked[0].Answer, "host:lmstudio") {
		t.Fatalf("the refusal does not name the host: %+v", result.Asked)
	}
}

// The wake hold is a lease of resource awake like any other, and the table
// reports it as a row of that resource (CONTRACT.md RES-A1).
func TestAwakeIsALeaseAndShowsInTheTable(t *testing.T) {
	in := &fake{name: instrument.NameNone, offers: nil, at: time.Now()}
	table := NewTable(in, nil, time.Nanosecond)
	book, err := OpenBook(BookOptions{Table: table, Hold: func(_ context.Context, _ *identity.Peer, action, resource string) error {
		if action != ActionHold || resource != ResourceAwake {
			t.Fatalf("the wake hold decided %s on %s", action, resource)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	table.claims = book.Claims()
	who := Subject{Program: `C:\downloader.exe`, Account: "S-1-5-21-7-1001"}
	result := book.Acquire(context.Background(), who, &identity.Peer{}, ResourceAwake, 0, 0)
	if result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("the wake hold read %s", result.Outcome)
	}
	state, err := table.Holders(ResourceAwake, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 {
		t.Fatalf("the awake rows are %+v", state.Holders)
	}
	row := state.Holders[0]
	if row.Program != who.Program || row.Lease != result.Lease.ID || row.Evidence != wire.EvidenceClaimed {
		t.Fatalf("the awake row is not the lease: %+v", row)
	}
	if state.Held != 0 {
		t.Fatal("a granted lease was added to the bytes the instrument measured")
	}
	if !book.Release(who, result.Lease.ID).Applied {
		t.Fatal("the holder could not release its own wake hold")
	}
	state, _ = table.Holders(ResourceAwake, true)
	if len(state.Holders) != 0 {
		t.Fatalf("a released wake hold is still in the table: %+v", state.Holders)
	}
}

// A holder that implements the contract observes its yield request and
// answers; a refusal carries its reason into the record, and a yield is
// confirmed against the instrument before the asker is told.
func TestLeaseHolderObservesAndAnswers(t *testing.T) {
	in := card(120 * gib)
	book := cardBook(t, in, BookOptions{Settle: 2 * time.Second})
	other := Subject{Program: `C:\lms\lmstudio.exe`, Account: "S-1-5-21-7-1001"}
	granted := book.Acquire(context.Background(), other, nil, instrument.Card0, 100*gib, 0)
	if granted.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatal(granted.Outcome)
	}
	// The holder then occupies what it was granted, and the card is full.
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: 100, Program: other.Program, Account: other.Account, Amount: 100 * gib}}
	in.at = time.Now()
	in.mu.Unlock()
	answered := make(chan struct{})
	observed := make(chan string, 1)
	go func() {
		lease := ""
		defer func() { observed <- lease; close(answered) }()
		page, err := book.Observe(context.Background(), other, "", 5000)
		if err != nil || len(page.Requests) != 1 {
			return
		}
		lease = page.Requests[0].Lease
		// The holder lets the bytes go, then says so.
		in.mu.Lock()
		in.samples[0].Amount = 0
		in.at = time.Now()
		in.mu.Unlock()
		book.Answer(other, page.Requests[0].ID, wire.YieldAnswerYielded, "")
	}()
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 60*gib, 10000)
	<-answered
	if lease := <-observed; lease != granted.Lease.ID {
		t.Fatalf("yield request named lease %q, wanted %q", lease, granted.Lease.ID)
	}
	if result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("outcome %s after %+v", result.Outcome, result.Asked)
	}
	if len(result.Asked) != 1 || result.Asked[0].Answer != AnswerYielded || result.Asked[0].Holder != other.Program {
		t.Fatalf("asked %+v", result.Asked)
	}
	for _, live := range book.Leases() {
		if live.ID == granted.Lease.ID {
			t.Fatal("a holder that yielded kept its lease")
		}
	}
}

func TestRepeatedYieldAnswerKeepsFirstRecordedDecision(t *testing.T) {
	holder := Subject{Program: `C:\holder.exe`, Account: "S-1-5-21-7-1001"}
	for _, first := range []wire.YieldAnswer{wire.YieldAnswerRefused, wire.YieldAnswerYielded} {
		request := &question{request: wire.YieldRequest{ID: "ask-1"}, holder: holder,
			done: make(chan struct{})}
		book := &Book{waiting: map[uint64]*question{1: request}}
		if !book.Answer(holder, "ask-1", first, "first reason").Recorded {
			t.Fatal("first answer was not recorded")
		}
		<-request.done
		other := wire.YieldAnswerYielded
		if first == wire.YieldAnswerYielded {
			other = wire.YieldAnswerRefused
		}
		if !book.Answer(holder, "ask-1", other, "changed reason").Recorded {
			t.Fatal("repeated answer did not remain idempotently recorded")
		}
		if request.answer != first || request.reason != "first reason" {
			t.Fatalf("repeated answer changed published decision to %q, %q", request.answer, request.reason)
		}
	}
}

// A holder that never observes is unanswered within the wait, and nothing is
// killed to make room (RES-L5).
func TestLeaseHolderThatNeverObservesIsUnanswered(t *testing.T) {
	in := card(120 * gib)
	book := cardBook(t, in, BookOptions{})
	other := Subject{Program: `C:\lms\lmstudio.exe`, Account: "S-1-5-21-7-1001"}
	if out := book.Acquire(context.Background(), other, nil, instrument.Card0, 100*gib, 0); out.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatal(out.Outcome)
	}
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: 100, Program: other.Program, Account: other.Account, Amount: 100 * gib}}
	in.at = time.Now()
	in.mu.Unlock()
	began := time.Now()
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 60*gib, 500)
	if result.Outcome != wire.AcquireOutcomeHoldersRefused {
		t.Fatalf("outcome %s", result.Outcome)
	}
	if len(result.Asked) != 1 || result.Asked[0].Answer != AnswerUnanswered {
		t.Fatalf("asked %+v", result.Asked)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the wait of 500 ms took %s", took)
	}
	if len(book.Leases()) != 1 {
		t.Fatal("the unanswered holder's lease was taken away from it")
	}
}

// leases@1 answers on the table's own endpoint, and the caller it grants to is
// the program the binding named, never one the caller asserted.
func TestLeasesAnswerOnTheTableEndpoint(t *testing.T) {
	in := &fake{name: instrument.NameNone, at: time.Now()}
	table := NewTable(in, nil, time.Nanosecond)
	book, err := OpenBook(BookOptions{Table: table, Lifetime: time.Minute,
		Hold: func(_ context.Context, peer *identity.Peer, action, resource string) error {
			if peer == nil {
				t.Error("the lease host decided without the caller's binding")
			}
			if action != ActionHold {
				t.Errorf("decided %s", action)
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	table.claims = book.Claims()
	client := servedWithLeases(t, table, book)

	result, err := client.Acquire(ResourceAwake, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != wire.AcquireOutcomeAcquired || result.Lease == nil {
		t.Fatalf("acquire over the wire read %s", result.Outcome)
	}
	state, err := client.Holders(ResourceAwake, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 || state.Holders[0].Program != self(t) || state.Holders[0].Lease != result.Lease.ID {
		t.Fatalf("the table's awake rows are %+v, not this caller's lease", state.Holders)
	}
	renewed, err := client.Renew(result.Lease.ID)
	if err != nil || !renewed.Applied {
		t.Fatalf("renew %+v %v", renewed, err)
	}
	released, err := client.Release(result.Lease.ID)
	if err != nil || !released.Applied {
		t.Fatalf("release %+v %v", released, err)
	}
	repeated, err := client.Release(result.Lease.ID)
	if err != nil || repeated.Applied {
		t.Fatalf("repeated release changed the existing applied=false result: %+v %v", repeated, err)
	}
	stale, err := client.Renew(result.Lease.ID)
	if err != nil || stale.Applied {
		t.Fatalf("renew after release changed the existing applied=false result: %+v %v", stale, err)
	}
	unmatched, err := client.Answer(context.Background(), "missing-request", wire.YieldAnswerRefused, "")
	if err != nil || unmatched.Recorded {
		t.Fatalf("unmatched answer changed the existing recorded=false result: %+v %v", unmatched, err)
	}
	_, err = client.Observe(context.Background(), "", -time.Millisecond)
	var invalid *wire.ServiceError
	if !errors.As(err, &invalid) || invalid.Code != wire.ServiceErrorCodeInvalidRequest {
		t.Fatalf("invalid observe wait gave %v", err)
	}
	if state, _ = client.Holders(ResourceAwake, true); len(state.Holders) != 0 {
		t.Fatalf("a released lease is still a row: %+v", state.Holders)
	}
}

// Every ask, yield and refusal is a record with the asker, the holder, the
// amount and the time (leases@1's doc, CONTRACT.md RES-L2).
func TestEveryAskIsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	audit, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer audit.Close()
	in := card(120*gib, 100*gib)
	lm := &holder{name: "host:lmstudio", resource: instrument.Card0, holding: []string{"gemma"}, frees: 100 * gib, in: in}
	book := cardBook(t, in, BookOptions{Attached: []Attached{lm}, Audit: audit})
	if out := book.Acquire(context.Background(), asker(), nil, instrument.Card0, 60*gib, 5000); out.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("outcome %s", out.Outcome)
	}
	lines, err := ReadAudit(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	var sawAsk, sawLease bool
	for _, line := range lines {
		switch {
		case strings.Contains(line, `"event":"ask"`) && strings.Contains(line, "host:lmstudio") &&
			strings.Contains(line, `"answer":"yielded"`) && strings.Contains(line, `"took_ms"`):
			sawAsk = true
		case strings.Contains(line, `"event":"acquired"`):
			sawLease = true
		}
	}
	if !sawAsk || !sawLease {
		t.Fatalf("the record does not carry the ask and the lease: %v", lines)
	}
}
