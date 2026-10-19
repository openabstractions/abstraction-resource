package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// Missing policy follows the contract default, never universal Allow
// (ARCHITECTURE.md). These tests are the regression for a host and a lease
// book nobody installed a gate on: every gated operation reads unavailable
// (RES-T4, RES-L1, RES-L3, RES-L7; RIGHTS-R26).

// TestHostWithoutGateRefusesTableRead: reading the machine's rows without a
// gate is refused as a decision that cannot be obtained, neither a permit nor
// a narrowing (RES-T4).
func TestHostWithoutGateRefusesTableRead(t *testing.T) {
	c := served(t, cardTable(t), nil)
	_, err := c.Holders(instrument.Card0, false)
	var refusal *wire.ServiceError
	if !errors.As(err, &refusal) || refusal.Code != wire.ServiceErrorCodePolicyUnavailable {
		t.Fatalf("a table read without a gate gave %v, want %s", err, wire.ServiceErrorCodePolicyUnavailable)
	}
}

// ungatedBook opens a lease book with no Hold, Yield or Standing gate, and
// any options set reaches it unchanged.
func ungatedBook(t *testing.T, in *fake, options BookOptions) *Book {
	t.Helper()
	options.Table = NewTable(in, nil, time.Nanosecond)
	options.Settle = 2 * time.Second
	book, err := OpenBook(options)
	if err != nil {
		t.Fatal(err)
	}
	return book
}

// TestBookWithoutHoldGateRefusesAcquire: a book without a Hold gate grants no
// lease to anybody (RES-L1).
func TestBookWithoutHoldGateRefusesAcquire(t *testing.T) {
	in := card(120*gib, 0)
	book := ungatedBook(t, in, BookOptions{})
	peer := &identity.Peer{}
	if out := book.Acquire(context.Background(), asker(), peer, instrument.Card0, gib, 0); out.Outcome != wire.AcquireOutcomeUnavailable || out.Lease != nil {
		t.Fatalf("Acquire without a gate read %s", out.Outcome)
	}
	if out := book.AcquireFree(context.Background(), asker(), peer, instrument.Card0, gib); out.Outcome != wire.AcquireFreeOutcomeUnavailable || out.Lease != nil {
		t.Fatalf("AcquireFree without a gate read %s", out.Outcome)
	}
	if in.reads != 0 {
		t.Fatal("the table was read for a caller no gate decided")
	}
}

// TestBookWithoutStandingGateKeepsRenewByUnmoved: a renewal no Standing gate
// decides does not extend the lease, so it ends at the renew_by its grant
// gave (RES-L7).
func TestBookWithoutStandingGateKeepsRenewByUnmoved(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	book := ungatedBook(t, card(120*gib), BookOptions{Hold: permitGate, Now: clock})
	got := book.Acquire(context.Background(), asker(), &identity.Peer{}, instrument.Card0, gib, 0)
	if got.Lease == nil {
		t.Fatalf("card acquire read %s", got.Outcome)
	}
	before := renewByOf(book, got.Lease.ID)
	mu.Lock()
	now = now.Add(DefaultLifetime / 2)
	mu.Unlock()
	change := book.Renew(asker(), got.Lease.ID)
	if after := renewByOf(book, got.Lease.ID); after != before {
		t.Fatalf("a renewal no gate decided moved renew_by from %s to %s (%+v)", before, after, change)
	}
}

// gateEndpoints numbers this process's gate test endpoints.
var gateEndpoints atomic.Int64

// TestInstalledGatesDecideResourceActionsOnly: a permit-all gate installed on
// the table host or as the book's Hold gate refuses every action outside the
// resource namespace (RIGHTS-A7).
func TestInstalledGatesDecideResourceActionsOnly(t *testing.T) {
	endpoint := listen.Endpoint(fmt.Sprintf("resource-gate-test-%d-%d", os.Getpid(), gateEndpoints.Add(1)))
	if err := listen.CanEver(endpoint, Bound); err != nil {
		t.Skip("this platform cannot bind callers here: ", err)
	}
	host, err := Listen(endpoint, cardTable(t))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if err := host.EnablePolicy(permitGate); err != nil {
		t.Fatal(err)
	}
	book := ungatedBook(t, card(120*gib), BookOptions{Hold: permitGate})
	for name, installed := range map[string]Policy{"table": host.policy, "hold": book.hold} {
		for _, action := range []string{"abstraction.router/route", "abstraction.resourcex/hold", "abstraction.resource"} {
			if err := installed.Check(context.Background(), boundPeer(), action, instrument.Card0); err == nil || errors.Is(err, ErrPolicyUnavailable) {
				t.Fatalf("the installed %s gate decided %s as %v, want a refusal", name, action, err)
			}
		}
	}
	if err := host.policy.Check(context.Background(), boundPeer(), ActionTableRead, ResourceAccount); err != nil {
		t.Fatalf("the table's own action read %v", err)
	}
	if err := book.hold.Check(context.Background(), boundPeer(), ActionHold, instrument.Card0); err != nil {
		t.Fatalf("the book's own action read %v", err)
	}
}

// permitGate is an explicit gate that permits every caller.
func permitGate(context.Context, *identity.Peer, string, string) error { return nil }

// permitYield and permitStanding are explicit Yield and Standing gates that
// permit every holder and every live lease.
func permitYield(context.Context, string) error                     { return nil }
func permitStanding(context.Context, Subject, string, string) error { return nil }

// boundPeer is a caller the host has bound. A gate decides only for a bound
// caller; a nil peer reads unavailable.
func boundPeer() *identity.Peer { return &identity.Peer{} }
