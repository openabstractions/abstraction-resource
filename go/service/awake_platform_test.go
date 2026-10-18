package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// platformWake counts the platform wake requests the book holds.
type platformWake struct {
	mu       sync.Mutex
	taken    int
	released int
}

func (p *platformWake) keep(string) (func(), error) {
	p.mu.Lock()
	p.taken++
	p.mu.Unlock()
	return func() { p.mu.Lock(); p.released++; p.mu.Unlock() }, nil
}

func (p *platformWake) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.taken, p.released
}

// gpuLeases acquires a card lease and then a wake lease for who, as a GPU
// provider does before a video runs, and returns the wake lease's id.
func gpuLeases(t *testing.T, book *Book, who Subject) string {
	t.Helper()
	if card := book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, gib, 0); card.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("card acquire read %s", card.Outcome)
	}
	wake := book.Acquire(context.Background(), who, &identity.Peer{}, ResourceAwake, 0, 0)
	if wake.Outcome != wire.AcquireOutcomeAcquired || wake.Lease == nil {
		t.Fatalf("wake acquire read %s", wake.Outcome)
	}
	return wake.Lease.ID
}

// RES-A1: a platform that refuses the wake request is recorded once per
// refusal, not once per save while the lease is renewed.
func TestARefusedPlatformWakeRequestIsRecordedOnce(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	audit, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { audit.Close() })
	book := cardBook(t, card(120*gib), BookOptions{Audit: audit, Now: func() time.Time { return now },
		KeepAwake: func(string) (func(), error) { return nil, errors.New("no power request") }})
	gpu := Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}
	id := gpuLeases(t, book, gpu)
	for range 5 {
		book.Renew(gpu, id)
	}
	records, err := ReadAudit(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	refused := 0
	for _, line := range records {
		if strings.Contains(line, `"event":"refused"`) && strings.Contains(line, `"resource":"awake"`) {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("%d refused records for one refusal", refused)
	}
}

// RES-A1: acquired wake leases of GPU holders keep one platform wake request
// while any lives; the last release or expiry gives it back, and a
// connection-owned hold takes none.
func TestAcquiredWakeLeasesHoldOnePlatformRequest(t *testing.T) {
	now := time.Now()
	wake := &platformWake{}
	book := cardBook(t, card(120*gib), BookOptions{Now: func() time.Time { return now }, KeepAwake: wake.keep})
	gpu := Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}
	other := Subject{Program: `C:\oa\thenoise.exe`, Account: "S-1-5-21-7-1001"}
	first := gpuLeases(t, book, gpu)
	gpuLeases(t, book, other)
	if taken, released := wake.counts(); taken != 1 || released != 0 {
		t.Fatalf("two wake leases took %d platform requests and released %d", taken, released)
	}
	if !book.Release(gpu, first).Applied {
		t.Fatal("release")
	}
	if _, released := wake.counts(); released != 0 {
		t.Fatal("the platform request ended while a wake lease still lived")
	}
	now = now.Add(2 * DefaultLifetime)
	book.Expire()
	if taken, released := wake.counts(); taken != 1 || released != 1 {
		t.Fatalf("after the last lease expired: taken %d released %d", taken, released)
	}
	held, release := book.HoldAwake(gpu, "a connection-owned hold keeps its own request in rights")
	if held == "" {
		t.Fatal("no connection-owned hold")
	}
	release()
	if taken, _ := wake.counts(); taken != 1 {
		t.Fatalf("a connection-owned hold took a second platform request: %d", taken)
	}
}

// RES-A1, RES-G5: the wake request keeps the display on, which also holds
// off the screen saver and the lock, so the book holds it only while the
// wake lease's holder holds a card lease: a wake lease alone takes none, the
// card lease that joins it takes it, and the card's release gives it back
// while the wake lease still lives. Another program's card lease keeps no
// other program's wake lease.
func TestTheWakeRequestNeedsACardLease(t *testing.T) {
	wake := &platformWake{}
	book := cardBook(t, card(120*gib), BookOptions{KeepAwake: wake.keep})
	who := Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}
	if lease := book.Acquire(context.Background(), who, &identity.Peer{}, ResourceAwake, 0, 0); lease.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("wake acquire read %s", lease.Outcome)
	}
	if taken, _ := wake.counts(); taken != 0 {
		t.Fatal("a wake lease with no card lease took the platform request")
	}
	other := Subject{Program: `C:\oa\other.exe`, Account: "S-1-5-21-7-1001"}
	if lease := book.Acquire(context.Background(), other, &identity.Peer{}, instrument.Card0, gib, 0); lease.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("other card acquire read %s", lease.Outcome)
	}
	if taken, _ := wake.counts(); taken != 0 {
		t.Fatal("another program's card lease kept this program's wake lease awake")
	}
	cardLease := book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, gib, 0)
	if cardLease.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("card acquire read %s", cardLease.Outcome)
	}
	if taken, released := wake.counts(); taken != 1 || released != 0 {
		t.Fatalf("with the card lease: taken %d released %d", taken, released)
	}
	if !book.Release(who, cardLease.Lease.ID).Applied {
		t.Fatal("card release")
	}
	if taken, released := wake.counts(); taken != 1 || released != 1 {
		t.Fatalf("after the card's release: taken %d released %d", taken, released)
	}
}
