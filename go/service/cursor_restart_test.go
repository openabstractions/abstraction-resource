package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

func TestYieldCursorRejectsPreviousBookEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	in := card(120 * gib)
	holder := asker()
	book := cardBook(t, in, BookOptions{Path: path})
	lease := book.Acquire(context.Background(), holder, boundPeer(), instrument.Card0, gib, 0)
	if lease.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatal(lease.Outcome)
	}
	ask := func(b *Book, cursor string, observed func(wire.YieldPage)) wire.YieldPage {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.askLease(ctx, Subject{Program: `C:\requester.exe`}, b.leases[lease.Lease.ID], instrument.Card0, time.Now().Add(time.Second), nil)
		}()
		page, err := b.Observe(context.Background(), holder, cursor, 1000)
		if err == nil && len(page.Requests) == 1 && observed != nil {
			observed(page)
		}
		cancel()
		<-done
		if err != nil || len(page.Requests) != 1 {
			t.Fatalf("observe current ask: %+v, %v", page, err)
		}
		return page
	}
	first := ask(book, "", nil)
	last := ask(book, first.Next, nil)
	// Loading the actual persisted book retains the lease and drops the
	// memory-only asks. Its sequence resumes below the issued cursor.
	restarted := cardBook(t, in, BookOptions{Path: path})
	ask(restarted, "", func(newAsk wire.YieldPage) {
		page, err := restarted.Observe(context.Background(), holder, last.Next, 0)
		if err == nil {
			t.Errorf("previous epoch cursor %q silently accepted during new ask %q: %+v", last.Next, newAsk.Next, page)
		}
		var refusal *wire.ServiceError
		if !errors.As(err, &refusal) || refusal.Code != wire.ServiceErrorCodeInvalidRequest {
			t.Errorf("expired cursor error: %v", err)
		}
		reset, err := restarted.Observe(context.Background(), holder, "", 0)
		if err != nil || len(reset.Requests) != 1 || reset.Requests[0].ID != newAsk.Requests[0].ID {
			t.Errorf("same holder reset missed pending request: %+v %v", reset, err)
		}
	})
}

func TestYieldCursorRejectsMalformedPositions(t *testing.T) {
	book := cardBook(t, card(120*gib), BookOptions{})
	for _, cursor := range []string{"garbage", "1", "lease-1", book.epoch + ":", book.epoch + ":01", book.epoch + ":-1", book.epoch + ":18446744073709551616"} {
		_, err := book.Observe(context.Background(), asker(), cursor, 0)
		var refusal *wire.ServiceError
		if !errors.As(err, &refusal) || refusal.Code != wire.ServiceErrorCodeInvalidRequest {
			t.Errorf("cursor %q: %v", cursor, err)
		}
	}
	page, err := book.Observe(context.Background(), asker(), "", 0)
	if err != nil || page.Next == "" {
		t.Fatalf("initial cursor: %+v %v", page, err)
	}
	if _, err := book.Observe(context.Background(), asker(), page.Next, 0); err != nil {
		t.Fatal(err)
	}
}

func TestYieldCursorInvalidUsesExistingWireEnvelope(t *testing.T) {
	previous := cardBook(t, card(120*gib), BookOptions{})
	page, err := previous.Observe(context.Background(), asker(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	book := cardBook(t, card(120*gib), BookOptions{})
	client := servedWithLeases(t, book.table, book)
	for _, cursor := range []string{page.Next, "malformed"} {
		_, err := client.Observe(context.Background(), cursor, 0)
		var refusal *wire.ServiceError
		if !errors.As(err, &refusal) || refusal.Code != wire.ServiceErrorCodeInvalidRequest {
			t.Fatalf("cursor %q wire refusal: %v", cursor, err)
		}
	}
	if _, err := client.Observe(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
}
