package service

import (
	"context"
	"testing"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

func TestAcquireReservesEarlierLeaseOfSameSubject(t *testing.T) {
	book := cardBook(t, card(10*gib), BookOptions{})
	who := asker()
	first := book.Acquire(context.Background(), who, nil, instrument.Card0, 8*gib, 0)
	if first.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("first acquire: %s", first.Outcome)
	}
	second := book.Acquire(context.Background(), who, nil, instrument.Card0, 8*gib, 0)
	if second.Outcome != wire.AcquireOutcomeHoldersRefused || second.Lease != nil {
		t.Fatalf("second acquire spent the first lease's reservation: %+v", second)
	}
}

func TestAcquireCountsMeasuredUseOncePerSubject(t *testing.T) {
	who := asker()
	in := card(18 * gib)
	book := cardBook(t, in, BookOptions{})
	for range 2 {
		result := book.Acquire(context.Background(), who, nil, instrument.Card0, 8*gib, 0)
		if result.Outcome != wire.AcquireOutcomeAcquired {
			t.Fatalf("initial acquire: %s", result.Outcome)
		}
	}
	in.mu.Lock()
	in.samples = append(in.samples, instrument.Sample{PID: 222, Program: who.Program, Account: who.Account, Amount: 4 * gib})
	in.mu.Unlock()
	other := Subject{Program: `C:\other\program.exe`, Account: who.Account}
	result := book.Acquire(context.Background(), other, nil, instrument.Card0, 4*gib, 0)
	if result.Outcome != wire.AcquireOutcomeHoldersRefused {
		t.Fatalf("measured bytes discounted once per lease: %+v", result)
	}
}

func TestAcquireMatchesMeasuredUseToProgramAndAccount(t *testing.T) {
	program := `C:\shared\program.exe`
	a := Subject{Program: program, Account: "account-a"}
	b := Subject{Program: program, Account: "account-b"}
	in := card(20 * gib)
	book := cardBook(t, in, BookOptions{})
	for _, who := range []Subject{a, b} {
		result := book.Acquire(context.Background(), who, nil, instrument.Card0, 8*gib, 0)
		if result.Outcome != wire.AcquireOutcomeAcquired {
			t.Fatalf("initial acquire for %s: %s", who.Account, result.Outcome)
		}
	}
	in.mu.Lock()
	in.samples = append(in.samples, instrument.Sample{PID: 222, Program: program, Account: a.Account, Amount: 4 * gib})
	in.mu.Unlock()
	other := Subject{Program: `C:\other\program.exe`, Account: a.Account}
	result := book.Acquire(context.Background(), other, nil, instrument.Card0, 5*gib, 0)
	if result.Outcome != wire.AcquireOutcomeHoldersRefused {
		t.Fatalf("account A's bytes discounted account B's lease: %+v", result)
	}
}

func TestAcquireBeyondDerivedPoolIsInsufficient(t *testing.T) {
	pool := instrument.MachineMemory()
	if pool <= 0 {
		t.Skip("this machine does not report physical memory")
	}
	book := cardBook(t, card(0), BookOptions{})
	result := book.Acquire(context.Background(), asker(), nil, instrument.Card0, pool+1, 0)
	if result.Outcome != wire.AcquireOutcomeInsufficient || len(result.Asked) != 0 {
		t.Fatalf("request larger than derived pool: %+v", result)
	}
}
