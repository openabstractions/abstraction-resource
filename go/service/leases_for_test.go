package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestAcquireForKeepsAppsSeparateThroughRevocationAndFinalRelease(t *testing.T) {
	me, err := identity.Self()
	if err != nil {
		t.Fatal(err)
	}
	account, err := me.Account()
	if err != nil {
		t.Fatal(err)
	}
	a := rights.Subject{Account: account, Program: filepath.Join(t.TempDir(), "app-a.exe")}
	b := rights.Subject{Account: account, Program: filepath.Join(t.TempDir(), "app-b.exe")}
	permit := map[string]bool{a.Program: true, b.Program: true}
	var policyMu sync.RWMutex
	system, display := &platformWake{}, &platformWake{}
	book := cardBook(t, card(120*gib), BookOptions{KeepSystemAwake: system.keep, KeepAwake: display.keep,
		Standing: func(_ context.Context, s Subject, action, resource string) error {
			policyMu.RLock()
			defer policyMu.RUnlock()
			if action != ActionHold || resource != ResourceAwake || s.Account != account || !permit[s.Program] {
				return errors.New("not granted")
			}
			return nil
		}})
	var designated atomic.Bool
	designated.Store(true)
	c := serve(t, book.table, permitGate, book, func(context.Context, *identity.Peer, string, string) bool { return designated.Load() })
	acquire := func(s rights.Subject) wire.AcquireResult {
		r, e := c.AcquireForContext(context.Background(), s, ResourceAwake, 0, 0)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	first, second := acquire(a), acquire(b)
	if first.Outcome != wire.AcquireOutcomeAcquired || second.Outcome != wire.AcquireOutcomeAcquired || first.Lease.ID == second.Lease.ID {
		t.Fatal(first, second)
	}
	rows := book.AwakeHolds()
	if len(rows) != 2 || rows[0].Program != a.Program || rows[1].Program != b.Program {
		t.Fatal(rows)
	}
	if changed, e := c.Renew(first.Lease.ID); e != nil || !changed.Applied {
		t.Fatal(changed, e)
	}
	policyMu.Lock()
	permit[a.Program] = false
	policyMu.Unlock()
	book.Recheck(context.Background())
	if rows = book.AwakeHolds(); len(rows) != 1 || rows[0].Program != b.Program {
		t.Fatal(rows)
	}
	if _, released := system.counts(); released != 0 {
		t.Fatal("A revoke ended B system wake")
	}
	designated.Store(false)
	if changed, e := c.Release(second.Lease.ID); e != nil || changed.Applied {
		t.Fatal("undesignated runner maintained another subject", changed, e)
	}
	designated.Store(true)
	if changed, e := c.Release(second.Lease.ID); e != nil || !changed.Applied {
		t.Fatal(changed, e)
	}
	if taken, released := system.counts(); taken != 1 || released != 1 {
		t.Fatal(taken, released)
	}
	if taken, _ := display.counts(); taken != 0 {
		t.Fatal("download woke display")
	}
}

func TestAcquireForRefusesOrdinaryCallerCrossAccountAndUngrantableSubject(t *testing.T) {
	me, _ := identity.Self()
	account, _ := me.Account()
	book := cardBook(t, card(120*gib), BookOptions{Standing: func(context.Context, Subject, string, string) error { return errors.New("not granted") }})
	ordinary := servedWithLeases(t, book.table, book)
	s := rights.Subject{Account: account, Program: filepath.Join(t.TempDir(), "app.exe")}
	r, err := ordinary.AcquireForContext(context.Background(), s, ResourceAwake, 0, 0)
	if err != nil || r.Outcome != wire.AcquireOutcomeNotPermitted {
		t.Fatal(r, err)
	}
	c := serve(t, book.table, permitGate, book, func(context.Context, *identity.Peer, string, string) bool { return true })
	for _, subject := range []rights.Subject{{Account: "another-account", Program: s.Program}, s} {
		r, err = c.AcquireForContext(context.Background(), subject, ResourceAwake, 0, 0)
		if err != nil || r.Outcome != wire.AcquireOutcomeNotPermitted {
			t.Fatal(r, err)
		}
	}
	if len(book.Leases()) != 0 {
		t.Fatal("an assertion granted a lease")
	}
}
