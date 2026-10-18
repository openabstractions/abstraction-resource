package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// RES-L1 applies to the new no-yield method before its table read or grant.
func TestAcquireFreeDecidesTheRuleBeforeItReadsTheTable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		peer     *identity.Peer
		decision error
		want     wire.AcquireFreeOutcome
	}{
		{"refused", &identity.Peer{}, errors.New("no rule"), wire.AcquireFreeOutcomeNotPermitted},
		{"unavailable", &identity.Peer{}, ErrPolicyUnavailable, wire.AcquireFreeOutcomeUnavailable},
		{"unbound", nil, nil, wire.AcquireFreeOutcomeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := card(120*gib, 0)
			book := cardBook(t, in, BookOptions{Hold: func(_ context.Context, _ *identity.Peer, action, resource string) error {
				if action != ActionHold || resource != instrument.Card0 {
					t.Fatalf("decided %s on %s", action, resource)
				}
				return tc.decision
			}})
			result := book.AcquireFree(context.Background(), asker(), tc.peer, instrument.Card0, gib)
			if result.Outcome != tc.want || result.Lease != nil || len(result.Asked) != 0 || in.reads != 0 || len(book.Leases()) != 0 {
				t.Fatalf("policy refusal read table or granted: result=%+v reads=%d", result, in.reads)
			}
		})
	}
}

// RES-L6: the no-yield acquire grants what fits in what is free and asks
// nobody: inside what is free it is acquired with an empty asked, and where
// a holder would have to yield it reads held, with the holder never asked.
func TestAcquireFreeGrantsOnlyWhatIsFree(t *testing.T) {
	in := card(120*gib, 20*gib)
	lm := &holder{name: "host:lmstudio", resource: instrument.Card0, holding: []string{"gemma"}, frees: 20 * gib, in: in}
	book := cardBook(t, in, BookOptions{Attached: []Attached{lm}})
	result := book.AcquireFree(context.Background(), asker(), nil, instrument.Card0, 90*gib)
	if result.Outcome != wire.AcquireFreeOutcomeAcquired || result.Lease == nil || len(result.Asked) != 0 {
		t.Fatalf("90 GiB of 100 free read %+v", result)
	}
	result = book.AcquireFree(context.Background(), asker(), nil, instrument.Card0, 20*gib)
	if result.Outcome != wire.AcquireFreeOutcomeHeld || result.Lease != nil || len(result.Asked) != 0 {
		t.Fatalf("20 GiB of the 10 left read %+v", result)
	}
	if lm.times() != 0 {
		t.Fatal("the no-yield acquire asked a holder to yield")
	}
	if result := book.AcquireFree(context.Background(), asker(), nil, instrument.Card0, 200*gib); result.Outcome != wire.AcquireFreeOutcomeInsufficient {
		t.Fatalf("more than the card read %+v", result)
	}
	if result := book.AcquireFree(context.Background(), Subject{}, nil, instrument.Card0, gib); result.Outcome != wire.AcquireFreeOutcomeInvalid {
		t.Fatalf("a caller with no program read %+v", result)
	}
}

// RES-L6: the no-yield acquire decides and grants in one step under the
// book's lock. Raced against a plain Acquire for the same free bytes, which
// only one of them fits, exactly one is granted, the no-yield acquire never
// records a yield request, and the book never grants more than was free.
func TestAcquireFreeRacesAPlainAcquireSafely(t *testing.T) {
	for round := 0; round < 50; round++ {
		in := card(100*gib, 40*gib)
		book := cardBook(t, in, BookOptions{})
		var wg sync.WaitGroup
		var free wire.AcquireFreeResult
		var plain wire.AcquireResult
		wg.Add(2)
		go func() {
			defer wg.Done()
			free = book.AcquireFree(context.Background(), asker(), nil, instrument.Card0, 40*gib)
		}()
		go func() {
			defer wg.Done()
			plain = book.Acquire(context.Background(), Subject{Program: `C:\other\app.exe`, Account: "S-1-5-21-7-1001"}, nil, instrument.Card0, 40*gib, 0)
		}()
		wg.Wait()
		granted := 0
		if free.Outcome == wire.AcquireFreeOutcomeAcquired {
			granted++
		}
		if plain.Outcome == wire.AcquireOutcomeAcquired {
			granted++
		}
		if granted != 1 {
			t.Fatalf("round %d: no-yield %s, plain %s; want exactly one grant of the 60 GiB free", round, free.Outcome, plain.Outcome)
		}
		if len(free.Asked) != 0 {
			t.Fatalf("round %d: the no-yield acquire asked %+v", round, free.Asked)
		}
		if free.Outcome != wire.AcquireFreeOutcomeAcquired && free.Outcome != wire.AcquireFreeOutcomeHeld {
			t.Fatalf("round %d: the no-yield acquire read %s", round, free.Outcome)
		}
	}
}

// RES-L6 (review Y2): a no-yield acquire holds the lowest standing in the
// book itself. While a plain Acquire of the resource asks holders to yield,
// a no-yield acquire reads held, even for bytes that are free, so it never
// takes what the plain acquirer's yields free; the plain acquire is then
// granted.
func TestAcquireFreeReadsHeldWhileAPlainAcquireAsks(t *testing.T) {
	in := card(100*gib, 70*gib)
	paused := newPausedYield("host:paused", "gemma", 70*gib, in)
	book := cardBook(t, in, BookOptions{Attached: []Attached{paused}, Settle: 200 * time.Millisecond})
	plain := make(chan wire.AcquireResult, 1)
	go func() {
		plain <- book.Acquire(context.Background(), askerNamed("plain"), nil, instrument.Card0, 60*gib, 5000)
	}()
	<-paused.entered
	free := book.AcquireFree(context.Background(), askerNamed("free"), nil, instrument.Card0, 20*gib)
	if free.Outcome != wire.AcquireFreeOutcomeHeld || len(free.Asked) != 0 {
		t.Fatalf("a no-yield acquire of 20 GiB of the 30 free, while a plain acquire asks, read %+v", free)
	}
	close(paused.release)
	if result := <-plain; result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("the plain acquire read %+v", result)
	}
	if after := book.AcquireFree(context.Background(), askerNamed("free"), nil, instrument.Card0, 20*gib); after.Outcome != wire.AcquireFreeOutcomeAcquired {
		t.Fatalf("once the asking ended, 20 GiB of the 40 free read %+v", after)
	}
}

// RES-L6 (review Y3): on a resource with no pool, a no-yield acquire is
// granted only while nothing holds it, the book's own live leases
// included, read under the lock that grants.
func TestAcquireFreeOnAResourceWithNoPoolCountsLiveLeases(t *testing.T) {
	in := &fake{name: instrument.NameWindowsGPUCounters, offers: []string{"npu:0"}, at: time.Now()}
	book := cardBook(t, in, BookOptions{})
	first := book.AcquireFree(context.Background(), askerNamed("a"), nil, "npu:0", gib)
	if first.Outcome != wire.AcquireFreeOutcomeAcquired {
		t.Fatalf("an unheld resource with no pool read %+v", first)
	}
	if second := book.AcquireFree(context.Background(), askerNamed("b"), nil, "npu:0", gib); second.Outcome != wire.AcquireFreeOutcomeHeld {
		t.Fatalf("a resource with no pool and a live lease read %+v", second)
	}
}
