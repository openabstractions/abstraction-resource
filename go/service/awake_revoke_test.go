package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// wakeRules is a stand-in rights policy: the programs it permits to hold
// awake, and an outage switch. It decides both the acquire (Hold) and the
// recheck of a live lease (Standing), as one policy does in a composition.
type wakeRules struct {
	mu      sync.Mutex
	permit  map[string]bool
	outage  bool
	decided int
}

func (w *wakeRules) set(program string, permit bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.permit[program] = permit
}

func (w *wakeRules) decide(program, action, resource string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.decided++
	if w.outage {
		return fmt.Errorf("%w: rights unavailable", ErrPolicyUnavailable)
	}
	if resource == ResourceAwake && !w.permit[program] {
		return errors.New("not_granted")
	}
	return nil
}

// RES-A1: revoking a program's "may hold awake" grant ends every wake lease it
// holds and the platform wake request they kept; another program's wake lease
// stays, an outage ends nothing, and the program's next acquire is decided
// again and refused.
func TestRevokingWakeEndsTheProgramsWakeLeases(t *testing.T) {
	gpu := Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}
	other := Subject{Program: `C:\oa\thenoise.exe`, Account: "S-1-5-21-7-1001"}
	rules := &wakeRules{permit: map[string]bool{gpu.Program: true, other.Program: true}}
	// The test's peers carry no identity, so Hold reads the program from the
	// subject the test acquires as: one stand-in policy for both paths.
	var asking Subject
	wake := &platformWake{}
	book := cardBook(t, card(120*gib), BookOptions{KeepAwake: wake.keep,
		Hold: func(_ context.Context, _ *identity.Peer, action, resource string) error {
			return rules.decide(asking.Program, action, resource)
		},
		Standing: func(_ context.Context, who Subject, action, resource string) error {
			return rules.decide(who.Program, action, resource)
		}})
	acquire := func(who Subject) string {
		t.Helper()
		asking = who
		return gpuLeases(t, book, who)
	}
	first := acquire(gpu)
	second := book.Acquire(context.Background(), gpu, &identity.Peer{}, ResourceAwake, 0, 0)
	if second.Lease == nil {
		t.Fatalf("second wake acquire read %s", second.Outcome)
	}
	kept := acquire(other)
	if taken, _ := wake.counts(); taken != 1 {
		t.Fatalf("wake leases took %d platform requests", taken)
	}

	// An outage is no revocation.
	rules.mu.Lock()
	rules.outage = true
	rules.mu.Unlock()
	book.Recheck(context.Background())
	if n := len(wakeLeasesOf(book, gpu)); n != 2 {
		t.Fatalf("an unavailable decision ended wake leases: %d left", n)
	}
	rules.mu.Lock()
	rules.outage = false
	rules.mu.Unlock()

	rules.set(gpu.Program, false)
	book.Recheck(context.Background())
	if left := wakeLeasesOf(book, gpu); len(left) != 0 {
		t.Fatalf("revoked wake grant left leases %v (first %s, second %s)", left, first, second.Lease.ID)
	}
	if book.Renew(gpu, first).Applied {
		t.Fatal("a revoked wake lease renewed")
	}
	if left := wakeLeasesOf(book, other); len(left) != 1 || left[0] != kept {
		t.Fatalf("another program's wake lease did not stay: %v", left)
	}
	if _, released := wake.counts(); released != 0 {
		t.Fatal("the platform request ended while another program's wake lease kept it")
	}

	rules.set(other.Program, false)
	book.Recheck(context.Background())
	if taken, released := wake.counts(); taken != 1 || released != 1 {
		t.Fatalf("after every wake grant was revoked: taken %d released %d", taken, released)
	}
	asking = gpu
	if again := book.Acquire(context.Background(), gpu, &identity.Peer{}, ResourceAwake, 0, 0); again.Outcome != wire.AcquireOutcomeNotPermitted {
		t.Fatalf("the next acquire after the revoke read %s", again.Outcome)
	}
}

// RES-A1, RES-L4: a revoke the book cannot decide, because rights is
// unavailable, still ends the wake lease within one lifetime: each renewal of
// a wake lease is decided again and does not move its renew_by while the
// decision is missing, so the lease ends at that renew_by and the platform
// request with it. The card lease beside it is kept the same way (RES-L7):
// every acquired lease is extended only on a decision obtained for that
// renewal.
func TestARevokedWakeLeaseEndsWithinOneLifetimeThroughAnOutage(t *testing.T) {
	gpu := Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}
	rules := &wakeRules{permit: map[string]bool{gpu.Program: true}}
	var clock sync.Mutex
	now := time.Now()
	wake := &platformWake{}
	book := cardBook(t, card(120*gib), BookOptions{KeepAwake: wake.keep,
		Now: func() time.Time { clock.Lock(); defer clock.Unlock(); return now },
		Hold: func(_ context.Context, _ *identity.Peer, action, resource string) error {
			return rules.decide(gpu.Program, action, resource)
		},
		Standing: func(_ context.Context, who Subject, action, resource string) error {
			return rules.decide(who.Program, action, resource)
		}})
	cardLease := book.Acquire(context.Background(), gpu, &identity.Peer{}, instrument.Card0, gib, 0)
	if cardLease.Lease == nil {
		t.Fatalf("card acquire read %s", cardLease.Outcome)
	}
	id := gpuWake(t, book, gpu)
	if taken, _ := wake.counts(); taken != 1 {
		t.Fatalf("the wake lease took %d platform requests", taken)
	}

	rules.mu.Lock()
	rules.permit[gpu.Program] = false
	rules.outage = true
	rules.mu.Unlock()
	book.Recheck(context.Background())
	if n := len(wakeLeasesOf(book, gpu)); n != 1 {
		t.Fatalf("an unavailable decision ended the wake lease at once: %d left", n)
	}

	leases := map[string]string{"card": cardLease.Lease.ID, "wake": id}
	before := map[string]string{}
	for name, lease := range leases {
		before[name] = renewByOf(book, lease)
	}
	step := DefaultLifetime / 3
	for elapsed := time.Duration(0); elapsed <= DefaultLifetime; elapsed += step {
		clock.Lock()
		now = now.Add(step)
		clock.Unlock()
		for name, lease := range leases {
			// While the lease stands, an outage keeps it without moving its
			// renew_by; once that passes, the renewal finds it ended.
			change := book.Renew(gpu, lease)
			if change.Applied && (change.Lease == nil || change.Lease.RenewBy != before[name]) {
				t.Fatalf("the %s lease's renew_by moved while its decision was unavailable: %+v", name, change.Lease)
			}
		}
		book.Expire()
	}
	if left := wakeLeasesOf(book, gpu); len(left) != 0 {
		t.Fatalf("one lifetime after the decision stopped, wake leases %v remain", left)
	}
	if left := leasesOf(book, gpu, instrument.Card0); len(left) != 0 {
		t.Fatalf("one lifetime after the decision stopped, card leases %v remain", left)
	}
	if taken, released := wake.counts(); taken != 1 || released != 1 {
		t.Fatalf("after one lifetime: taken %d released %d", taken, released)
	}
}

// gpuWake acquires one wake lease for who and returns its id.
func gpuWake(t *testing.T, book *Book, who Subject) string {
	t.Helper()
	lease := book.Acquire(context.Background(), who, &identity.Peer{}, ResourceAwake, 0, 0)
	if lease.Lease == nil {
		t.Fatalf("wake acquire read %s", lease.Outcome)
	}
	return lease.Lease.ID
}

// renewByOf is a live lease's renew_by as the wire reports it, or "".
func renewByOf(book *Book, id string) string {
	book.mu.Lock()
	defer book.mu.Unlock()
	if live, ok := book.leases[id]; ok {
		return live.lease().RenewBy
	}
	return ""
}

// wakeLeasesOf is the ids of who's live wake leases.
func wakeLeasesOf(book *Book, who Subject) []string {
	book.mu.Lock()
	defer book.mu.Unlock()
	var out []string
	for id, live := range book.leases {
		if live.Resource == ResourceAwake && live.subject().same(who) {
			out = append(out, id)
		}
	}
	return out
}
