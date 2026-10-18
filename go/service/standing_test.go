package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// holdRules is a stand-in rights policy for ActionHold on any resource: the
// (program, resource) pairs it refuses, and an outage switch. It decides both
// the acquire (Hold) and the recheck of a live lease (Standing), as one policy
// does in a composition.
type holdRules struct {
	mu      sync.Mutex
	refused map[[2]string]bool
	outage  bool
}

func (h *holdRules) revoke(program, resource string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refused[[2]string{program, resource}] = true
}

func (h *holdRules) decide(program, resource string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.outage {
		return fmt.Errorf("%w: rights unavailable", ErrPolicyUnavailable)
	}
	if h.refused[[2]string{program, resource}] {
		return errors.New("denied")
	}
	return nil
}

func ruledCardBook(t *testing.T, rules *holdRules, asking *Subject) *Book {
	t.Helper()
	return cardBook(t, card(120*gib), BookOptions{
		Hold: func(_ context.Context, _ *identity.Peer, _, resource string) error {
			return rules.decide(asking.Program, resource)
		},
		Standing: func(_ context.Context, who Subject, _, resource string) error {
			return rules.decide(who.Program, resource)
		}})
}

// RES-L7: revoking or Blocking a program's hold on a card ends its card
// leases on the next policy change (Recheck), another program's card lease
// stays, an outage ends nothing at once, and the program's next renewal and
// next acquire are refused.
func TestRevokingCardHoldEndsTheProgramsCardLeases(t *testing.T) {
	gpu := Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}
	other := Subject{Program: `C:\oa\thenoise.exe`, Account: "S-1-5-21-7-1001"}
	rules := &holdRules{refused: map[[2]string]bool{}}
	var asking Subject
	book := ruledCardBook(t, rules, &asking)
	acquire := func(who Subject) string {
		t.Helper()
		asking = who
		got := book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, gib, 0)
		if got.Lease == nil {
			t.Fatalf("card acquire read %s", got.Outcome)
		}
		return got.Lease.ID
	}
	first := acquire(gpu)
	kept := acquire(other)
	if !book.Renew(gpu, first).Applied {
		t.Fatal("a permitted card lease did not renew")
	}

	rules.mu.Lock()
	rules.outage = true
	rules.mu.Unlock()
	book.Recheck(context.Background())
	if n := len(leasesOf(book, gpu, instrument.Card0)); n != 1 {
		t.Fatalf("an unavailable decision ended card leases at once: %d left", n)
	}
	rules.mu.Lock()
	rules.outage = false
	rules.mu.Unlock()

	rules.revoke(gpu.Program, instrument.Card0)
	book.Recheck(context.Background())
	if left := leasesOf(book, gpu, instrument.Card0); len(left) != 0 {
		t.Fatalf("a revoked card hold left leases %v", left)
	}
	if book.Renew(gpu, first).Applied {
		t.Fatal("a revoked card lease renewed")
	}
	if left := leasesOf(book, other, instrument.Card0); len(left) != 1 || left[0] != kept {
		t.Fatalf("another program's card lease did not stay: %v", left)
	}
	asking = gpu
	if again := book.Acquire(context.Background(), gpu, &identity.Peer{}, instrument.Card0, gib, 0); again.Outcome != wire.AcquireOutcomeNotPermitted {
		t.Fatalf("the next acquire after the revoke read %s", again.Outcome)
	}
}

// RES-L7, RES-L4: a card lease's renewal is decided again by itself, with no
// policy-change notice in between: the first renewal after a revoke is refused
// and ends the lease, as an expiry the book never heard about would.
func TestACardLeaseRenewalIsDecidedAgain(t *testing.T) {
	gpu := Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}
	rules := &holdRules{refused: map[[2]string]bool{}}
	asking := gpu
	book := ruledCardBook(t, rules, &asking)
	got := book.Acquire(context.Background(), gpu, &identity.Peer{}, instrument.Card0, gib, 0)
	if got.Lease == nil {
		t.Fatalf("card acquire read %s", got.Outcome)
	}
	rules.revoke(gpu.Program, instrument.Card0)
	if book.Renew(gpu, got.Lease.ID).Applied {
		t.Fatal("a card lease renewed after its hold was revoked")
	}
	if left := leasesOf(book, gpu, instrument.Card0); len(left) != 0 {
		t.Fatalf("a refused renewal left the card lease: %v", left)
	}
}

// leasesOf is the ids of who's live leases of resource.
func leasesOf(book *Book, who Subject, resource string) []string {
	book.mu.Lock()
	defer book.mu.Unlock()
	var out []string
	for id, live := range book.leases {
		if live.Resource == resource && live.subject().same(who) {
			out = append(out, id)
		}
	}
	return out
}
