package service

import (
	"context"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// fixedBook is a lease book over a Fixed instrument and the given claims:
// the capacity of every test here is the instrument's value, never this
// machine's memory or GPU (CONTRACT.md RES-L2).
func fixedBook(t *testing.T, in *instrument.Fixed, claims Claims) *Book {
	t.Helper()
	book, err := OpenBook(BookOptions{Table: NewTable(in, claims, time.Nanosecond),
		Hold: permitGate, Yield: permitYield, Standing: permitStanding, Settle: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return book
}

func otherHolder() Subject {
	return Subject{Program: `C:\ollama\ollama.exe`, Account: "S-1-5-21-7-1001"}
}

// A 24 GiB discrete card refuses a 40 GiB request before anybody is asked:
// its pool is the card's own capacity, never the machine's memory (audit
// resource P0-1).
func TestDiscreteCardRefusesMoreThanItsCapacity(t *testing.T) {
	in := instrument.NewFixed()
	in.Set(instrument.Memory, instrument.FixedResource{Capacity: 64 * gib})
	in.Set(instrument.Card0, instrument.FixedResource{Capacity: 24 * gib})
	book := fixedBook(t, in, nil)
	result := book.Acquire(context.Background(), asker(), boundPeer(), instrument.Card0, 40*gib, 200)
	if result.Outcome != wire.AcquireOutcomeInsufficient {
		t.Fatalf("40 GiB of a 24 GiB card read %s", result.Outcome)
	}
	if len(result.Asked) != 0 {
		t.Fatalf("an impossible request asked %+v", result.Asked)
	}
	free := book.AcquireFree(context.Background(), asker(), boundPeer(), instrument.Card0, 40*gib)
	if free.Outcome != wire.AcquireFreeOutcomeInsufficient {
		t.Fatalf("a no-yield 40 GiB of a 24 GiB card read %s", free.Outcome)
	}
}

// On a shared-memory adapter card:0 and memory are one pool: a 100 GiB card
// lease and a 100 GiB memory lease do not both fit 128 GiB, and the second is
// refused after its holder was asked (audit resource P0-2).
func TestSharedPoolRefusesSecondLeaseAfterAsking(t *testing.T) {
	in := instrument.NewFixed()
	in.Set(instrument.Memory, instrument.FixedResource{Capacity: 128 * gib})
	in.Set(instrument.Card0, instrument.FixedResource{Pool: instrument.Memory})
	book := fixedBook(t, in, nil)
	first := book.Acquire(context.Background(), otherHolder(), boundPeer(), instrument.Card0, 100*gib, 0)
	if first.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("100 GiB of an empty 128 GiB pool read %s", first.Outcome)
	}
	second := book.Acquire(context.Background(), asker(), boundPeer(), instrument.Memory, 100*gib, 200)
	if second.Outcome != wire.AcquireOutcomeHoldersRefused {
		t.Fatalf("a second 100 GiB lease on the same 128 GiB read %s", second.Outcome)
	}
	if len(second.Asked) != 1 || second.Asked[0].Holder != otherHolder().Program {
		t.Fatalf("the card lease's holder was not asked: %+v", second.Asked)
	}
	free := book.AcquireFree(context.Background(), asker(), boundPeer(), instrument.Card0, 100*gib)
	if free.Outcome != wire.AcquireFreeOutcomeHeld {
		t.Fatalf("a no-yield second 100 GiB card lease read %s", free.Outcome)
	}
}

// RES-L6 across a pool: while a memory Acquire asks a card:0 lease holder to
// yield in a shared pool, a no-yield acquire on card:0 reads held, as one on
// memory does. The bytes the yield frees belong to the asker.
func TestAcquireFreeOnAPoolSiblingReadsHeldDuringAnAsk(t *testing.T) {
	in := instrument.NewFixed()
	in.Set(instrument.Memory, instrument.FixedResource{Capacity: 128 * gib})
	in.Set(instrument.Card0, instrument.FixedResource{Pool: instrument.Memory})
	book := fixedBook(t, in, nil)
	if first := book.Acquire(context.Background(), otherHolder(), boundPeer(), instrument.Card0, 100*gib, 0); first.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("100 GiB of an empty 128 GiB pool read %s", first.Outcome)
	}
	var card, memory wire.AcquireFreeOutcome
	book.afterFirstTry = func() {
		card = book.AcquireFree(context.Background(), askerNamed("free"), boundPeer(), instrument.Card0, 20*gib).Outcome
		memory = book.AcquireFree(context.Background(), askerNamed("free"), boundPeer(), instrument.Memory, 20*gib).Outcome
	}
	book.Acquire(context.Background(), asker(), boundPeer(), instrument.Memory, 100*gib, 200)
	if card != wire.AcquireFreeOutcomeHeld || memory != wire.AcquireFreeOutcomeHeld {
		t.Fatalf("during a memory ask card:0 no-yield read %s, memory no-yield read %s; want held for both", card, memory)
	}
}

// On a discrete card the live instruments still declare card:0 carved from
// memory (CONTRACT.md RES-L2 limit): a 20 GiB card lease and a 50 GiB memory
// request share a 64 GiB pool, so the memory request asks the card holder.
func TestDiscreteCardLeasesShareTheMachineMemoryPool(t *testing.T) {
	in := instrument.NewFixed()
	in.Set(instrument.Memory, instrument.FixedResource{Capacity: 64 * gib})
	in.Set(instrument.Card0, instrument.FixedResource{Pool: instrument.Memory})
	book := fixedBook(t, in, nil)
	book.Acquire(context.Background(), otherHolder(), boundPeer(), instrument.Card0, 20*gib, 0)
	result := book.Acquire(context.Background(), asker(), boundPeer(), instrument.Memory, 50*gib, 200)
	if result.Outcome == wire.AcquireOutcomeAcquired || len(result.Asked) != 1 {
		t.Fatalf("a 50 GiB memory request beside a 20 GiB card lease in 64 GiB read %+v", result)
	}
}

// The Mac instrument reports Metal's working-set size as card:0's capacity
// and keeps the card carved from memory (instrument/metal.go, RES-L2). With
// the owner's M5 Pro figures, Fit's card pool is 40,200,896,512 bytes, not
// 48 GiB; a larger card request is insufficient before anyone is asked; and
// a card lease still counts against a memory request.
func TestAMacCardPoolIsItsWorkingSetCarvedFromMemory(t *testing.T) {
	const workingSet = int64(40_200_896_512)
	in := instrument.NewFixed()
	in.Set(instrument.Memory, instrument.FixedResource{Capacity: 48 * gib})
	in.Set(instrument.Card0, instrument.FixedResource{Capacity: workingSet, Pool: instrument.Memory})
	book := fixedBook(t, in, nil)
	if pool := book.table.Pool(instrument.Card0); pool != workingSet {
		t.Fatalf("the card's pool = %d, want the working set %d", pool, workingSet)
	}
	if pool := book.table.Pool(instrument.Memory); pool != 48*gib {
		t.Fatalf("memory's pool = %d", pool)
	}
	if result := book.Acquire(context.Background(), asker(), boundPeer(), instrument.Card0, 40*gib, 200); result.Outcome != wire.AcquireOutcomeInsufficient || len(result.Asked) != 0 {
		t.Fatalf("40 GiB of a 37.4 GiB working set read %+v", result)
	}
	if first := book.Acquire(context.Background(), otherHolder(), boundPeer(), instrument.Card0, 30*gib, 0); first.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("30 GiB of an empty card read %s", first.Outcome)
	}
	result := book.Acquire(context.Background(), asker(), boundPeer(), instrument.Memory, 20*gib, 200)
	if result.Outcome == wire.AcquireOutcomeAcquired || len(result.Asked) != 1 {
		t.Fatalf("a 20 GiB memory request beside a 30 GiB card lease in 48 GiB read %+v", result)
	}
}

// A card the instrument declares its own pool with no capacity is no pool:
// it is never admitted against the machine's memory.
func TestCardWithoutCapacityIsNotMachineMemory(t *testing.T) {
	in := instrument.NewFixed()
	in.Set(instrument.Card0, instrument.FixedResource{})
	book := fixedBook(t, in, nil)
	if pool := book.pool(instrument.Card0, 0); pool != 0 {
		t.Fatalf("a card of unknown capacity read a pool of %d", pool)
	}
}

// An engine's report is never a capacity source: a claim stating an amount
// moves neither ResourceState.capacity nor what a lease is admitted against.
// No code path parses a claim's amount today; this guards against a future
// capacity parser. The router's comfyResident /system_stats reading stays a
// hold estimate (claimed) and never becomes capacity.
func TestEngineReportIsNotCapacity(t *testing.T) {
	in := instrument.NewFixed()
	in.Set(instrument.Card0, instrument.FixedResource{Capacity: 24 * gib})
	engine := func() []Claim {
		return []Claim{{Resource: instrument.Card0, Program: "host:lmstudio", Amount: 96 * gib,
			Detail: "capacity 96 GiB"}}
	}
	book := fixedBook(t, in, engine)
	state, err := book.table.Holders(instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Capacity != 24*gib {
		t.Fatalf("capacity read %d with an engine claiming 96 GiB", state.Capacity)
	}
	result := book.Acquire(context.Background(), asker(), boundPeer(), instrument.Card0, 40*gib, 0)
	if result.Outcome != wire.AcquireOutcomeInsufficient {
		t.Fatalf("40 GiB of a 24 GiB card with an engine claiming 96 GiB read %s", result.Outcome)
	}

	unknown := instrument.NewFixed()
	unknown.Set(instrument.Card0, instrument.FixedResource{})
	book = fixedBook(t, unknown, engine)
	if pool := book.pool(instrument.Card0, 0); pool != 0 {
		t.Fatalf("a card of unknown capacity with an engine claiming 96 GiB read a pool of %d", pool)
	}
}
