package service

import (
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// fake is an instrument whose readings the test writes, counting how often it
// was asked so the age bound and fresh can be told apart.
// A lease test changes the samples while the book is reading them, which is
// what a real unload does to a real instrument, so the readings are guarded.
type fake struct {
	mu      sync.Mutex
	name    string
	offers  []string
	reads   int
	samples []instrument.Sample
	cap     int64
	at      time.Time
	err     error
}

func (f *fake) Name() string                 { return f.name }
func (f *fake) Resources() []string          { return f.offers }
func (f *fake) VerifiedProcessSamples() bool { return true }
func (f *fake) Read(resource string) (instrument.Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return instrument.Reading{}, f.err
	}
	samples := append([]instrument.Sample(nil), f.samples...)
	return instrument.Reading{Resource: resource, Capacity: f.cap, Samples: samples, At: f.at}, nil
}

// The measured rows of the APU run: two loaded models, two llama-server
// processes, one row each (research/resources/MEASUREMENT-2026-09-22.md state
// (c)). held is their sum and nothing else's, and both carry evidence
// verified with the account the process token named.
func TestHoldersReportsMeasuredRowsAsVerified(t *testing.T) {
	at := time.Date(2026, 9, 22, 10, 17, 57, 0, time.UTC)
	in := &fake{name: instrument.NameWindowsGPUCounters, offers: []string{instrument.Card0}, at: at,
		samples: []instrument.Sample{
			{PID: 28188, Program: `C:\lms\llama-server.exe`, Account: "S-1-5-21-7-1001", Amount: 21247127552},
			{PID: 50932, Program: `C:\lms\llama-server.exe`, Account: "S-1-5-21-7-1001", Amount: 18560939827},
		}}
	table := NewTable(in, nil, time.Second)
	state, err := table.Holders(instrument.Card0, false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Instrument != instrument.NameWindowsGPUCounters || state.Resource != instrument.Card0 {
		t.Fatalf("state %+v", state)
	}
	if len(state.Holders) != 2 {
		t.Fatalf("two loaded models gave %d rows: %+v", len(state.Holders), state.Holders)
	}
	if state.Held != 21247127552+18560939827 {
		t.Fatalf("held %d is not the sum of the two verified rows", state.Held)
	}
	for _, row := range state.Holders {
		if row.Evidence != wire.EvidenceVerified {
			t.Fatalf("a measured row is %s: %+v", row.Evidence, row)
		}
		if row.Account != "S-1-5-21-7-1001" || row.Program != `C:\lms\llama-server.exe` {
			t.Fatalf("row lost its subject: %+v", row)
		}
	}
	if state.Observed != "2026-09-22T10:17:57.000000Z" {
		t.Fatalf("observed %q is not the instrument's own reading time", state.Observed)
	}
}

// A process the platform would not name keeps its process image and an empty
// account. Nothing is invented (CONTRACT.md RES-T1).
func TestHoldersKeepsTheImageOfAnUnattributableProcess(t *testing.T) {
	in := &fake{name: instrument.NameWindowsGPUCounters, at: time.Now(),
		samples: []instrument.Sample{{PID: 4, Image: "System", Amount: 1 << 30}}}
	state, err := NewTable(in, nil, time.Second).Holders(instrument.Card0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 || state.Holders[0].Program != "System" || state.Holders[0].Account != "" {
		t.Fatalf("unattributable row: %+v", state.Holders)
	}
	if state.Holders[0].Evidence != wire.EvidenceVerified {
		t.Fatalf("the instrument measured it, so it is verified: %+v", state.Holders[0])
	}
}

// A claim carries no amount the instrument did not measure, and it does not
// move held (CONTRACT.md RES-T3).
func TestClaimedRowsCarryNoAmountAndDoNotMoveHeld(t *testing.T) {
	in := &fake{name: instrument.NameWindowsGPUCounters, at: time.Now(),
		samples: []instrument.Sample{{PID: 1, Program: `C:\lms\llama-server.exe`, Amount: 17 << 30}}}
	since := time.Date(2026, 9, 22, 10, 15, 41, 0, time.UTC)
	claims := func() []Claim {
		return []Claim{{Resource: instrument.Card0, Program: "host:lmstudio", Detail: "gemma-4-26b", Since: since},
			{Resource: "memory", Program: "host:ollama", Detail: "qwen3"}}
	}
	state, err := NewTable(in, claims, time.Second).Holders(instrument.Card0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 2 {
		t.Fatalf("one measured row and one claim of card:0 give %d rows: %+v", len(state.Holders), state.Holders)
	}
	claimed := state.Holders[1]
	if claimed.Evidence != wire.EvidenceClaimed || claimed.Amount != 0 || claimed.Detail != "gemma-4-26b" {
		t.Fatalf("claimed row: %+v", claimed)
	}
	if claimed.Since != "2026-09-22T10:15:41.000000Z" {
		t.Fatalf("claimed since %q", claimed.Since)
	}
	if state.Held != 17<<30 {
		t.Fatalf("held %d moved with a claim", state.Held)
	}
}

// A sample inside the age bound stands; fresh reads the instrument again
// whatever its age.
func TestFreshReadsAgainAndTheAgeBoundDoesNot(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	in := &fake{name: instrument.NameWindowsGPUCounters, at: now}
	table := NewTable(in, nil, 2*time.Second)
	table.now = func() time.Time { return now }

	if _, err := table.Holders(instrument.Card0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Holders(instrument.Card0, false); err != nil {
		t.Fatal(err)
	}
	if in.reads != 1 {
		t.Fatalf("a second read inside the age bound asked the instrument %d times", in.reads)
	}
	if _, err := table.Holders(instrument.Card0, true); err != nil {
		t.Fatal(err)
	}
	if in.reads != 2 {
		t.Fatalf("fresh did not read the instrument again: %d reads", in.reads)
	}
	now = now.Add(3 * time.Second)
	in.at = now
	if _, err := table.Holders(instrument.Card0, false); err != nil {
		t.Fatal(err)
	}
	if in.reads != 3 {
		t.Fatalf("a sample past the age bound was served anyway: %d reads", in.reads)
	}
}

// A fresh read asks the refresh hook for a new survey before it reads claims,
// so a claimed host: row the survey no longer reports is gone from a fresh
// read; a caller that did not ask for fresh still sees the row the last
// survey found (CONTRACT.md RES-T3).
func TestFreshAsksRefreshBeforeItReadsClaims(t *testing.T) {
	loaded := true
	claims := func() []Claim {
		if !loaded {
			return nil
		}
		return []Claim{{Resource: instrument.Card0, Program: "host:lmstudio", Detail: "gemma-4-26b"}}
	}
	surveys := 0
	table := NewTable(instrument.None(), claims, time.Second)
	table.SetRefresh(func() {
		surveys++
		// The survey this stands in for is what a real yield would make the
		// next one see: the host no longer lists the model resident.
		loaded = false
	})

	state, err := table.Holders(instrument.Card0, false)
	if err != nil {
		t.Fatal(err)
	}
	if surveys != 0 {
		t.Fatalf("a read that did not ask for fresh provoked %d surveys", surveys)
	}
	if len(state.Holders) != 1 {
		t.Fatalf("the row from the last survey should still stand: %+v", state.Holders)
	}

	state, err = table.Holders(instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	if surveys != 1 {
		t.Fatalf("fresh should have asked the refresh hook once: %d", surveys)
	}
	if len(state.Holders) != 0 {
		t.Fatalf("fresh should show the claim gone after the survey cleared it: %+v", state.Holders)
	}
}

// A table given no refresh hook behaves exactly as before: fresh still
// rereads the instrument, and claims are still read on every call.
func TestFreshWithNoRefreshHookStillReadsClaims(t *testing.T) {
	calls := 0
	claims := func() []Claim {
		calls++
		return []Claim{{Resource: instrument.Card0, Program: "host:lmstudio", Detail: "gemma-4-26b"}}
	}
	table := NewTable(instrument.None(), claims, time.Second)
	if _, err := table.Holders(instrument.Card0, true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("claims should be read once per Holders call: %d", calls)
	}
}

// With no instrument the table is the claims and says so, and every row is
// claimed (CONTRACT.md RES-T2).
func TestNoInstrumentLeavesEveryRowClaimed(t *testing.T) {
	claims := func() []Claim {
		return []Claim{{Resource: instrument.Card0, Program: "host:lmstudio", Detail: "gemma-4-26b"}}
	}
	table := NewTable(nil, claims, time.Second)
	state, err := table.Holders(instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Instrument != instrument.NameNone || state.Held != 0 || state.Capacity != 0 {
		t.Fatalf("state %+v", state)
	}
	if len(state.Holders) != 1 || state.Holders[0].Evidence != wire.EvidenceClaimed {
		t.Fatalf("rows %+v", state.Holders)
	}
}

// Resources is what the instrument measures and what the claims name, each
// once.
func TestResourcesUnionOfInstrumentAndClaims(t *testing.T) {
	in := &fake{name: instrument.NameWindowsGPUCounters, offers: []string{instrument.Card0}, at: time.Now()}
	claims := func() []Claim {
		return []Claim{{Resource: instrument.Card0}, {Resource: "memory"}, {Resource: "memory"}}
	}
	list, err := NewTable(in, claims, time.Second).Resources()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Resources) != 2 || list.Resources[0] != instrument.Card0 || list.Resources[1] != "memory" {
		t.Fatalf("resources %v", list.Resources)
	}
}

// The own-rows rule, as the host applies it to a refused caller: the rows of
// the caller's program and nothing else (CONTRACT.md RES-T4).
func TestOwnKeepsOnlyTheCallersProgram(t *testing.T) {
	rows := []wire.Holder{
		{Program: `C:\lms\llama-server.exe`, Amount: 17 << 30, Evidence: wire.EvidenceVerified},
		{Program: `C:\comfy\python.exe`, Amount: 5 << 30, Evidence: wire.EvidenceVerified},
		{Program: `C:\lms\llama-server.exe`, Amount: 19 << 30, Evidence: wire.EvidenceVerified},
	}
	kept := own(rows, `C:\lms\llama-server.exe`)
	if len(kept) != 2 {
		t.Fatalf("a program with two processes sees %d of its own rows: %+v", len(kept), kept)
	}
	if len(own(rows, `C:\other\app.exe`)) != 0 {
		t.Fatal("a program with no hold saw somebody else's row")
	}
}
