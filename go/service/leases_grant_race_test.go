package service

import (
	"context"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

func askerNamed(name string) Subject {
	return Subject{Program: `C:\askers\` + name + `.exe`, Account: "S-1-5-21-7-" + name}
}

// Two askers reach the same lease holder at the same moment and both of
// their yield requests are parked until the test releases them together,
// proving two waiters on one yield rather than assuming a scheduling
// accident. The
// pool fits only one of the two 60 GiB requests once the holder's 70 GiB
// comes back, so both see enough free the instant the yield settles.
// Book.grant decides against the bytes free under its own lock at that
// moment: the ask that runs first finds them, and the other's check, run
// after the winner's lease is already written, reads not enough. The loser
// re-enters the ask sequence, finds only the winner's own fresh lease left to
// ask, gets no answer from it before its deadline, and reads
// holders_refused.
func TestGrantIsDecidedUnderTheLockAgainstAConcurrentGrant(t *testing.T) {
	in := card(100 * gib)
	book := cardBook(t, in, BookOptions{Settle: 200 * time.Millisecond})
	paused := newHolder(t, book, in, "paused", 70*gib, func(h *holder) { h.pause = make(chan struct{}) })
	x, y := askerNamed("x"), askerNamed("y")
	const waitMs = 300

	var results [2]wire.AcquireResult
	var run sync.WaitGroup
	run.Add(2)
	go func() {
		defer run.Done()
		results[0] = book.Acquire(context.Background(), x, boundPeer(), instrument.Card0, 60*gib, waitMs)
	}()
	go func() {
		defer run.Done()
		results[1] = book.Acquire(context.Background(), y, boundPeer(), instrument.Card0, 60*gib, waitMs)
	}()

	// Both askers' requests provably reach the one holder before it is let
	// to yield.
	paused.request(t)
	paused.request(t)
	close(paused.pause)
	run.Wait()

	acquired, refused := 0, 0
	var winner *wire.AcquireResult
	for i := range results {
		switch results[i].Outcome {
		case wire.AcquireOutcomeAcquired:
			acquired++
			winner = &results[i]
		case wire.AcquireOutcomeHoldersRefused:
			refused++
		default:
			t.Fatalf("outcome %s, want acquired or holders_refused: %+v", results[i].Outcome, results[i])
		}
	}
	if acquired != 1 || refused != 1 {
		t.Fatalf("outcomes %+v; want exactly one acquired and one holders_refused, never both and never neither", results)
	}
	if winner.Lease == nil {
		t.Fatalf("the winner's own result carries no lease: %+v", winner)
	}
	live := book.Leases()
	if len(live) != 1 || live[0].ID != winner.Lease.ID || live[0].Amount != 60*gib {
		t.Fatalf("the book holds %+v after the race, want exactly the winner's own %d-byte grant", live, 60*gib)
	}
}
