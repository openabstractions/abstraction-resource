package service

import (
	"context"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// pausedYield is an attached holder whose Yield blocks until the test closes
// release, so two Acquire calls can be forced onto the very same yield before
// either one's grant is decided (TODO.md resource §6; CONTRACT.md RES-L2's
// added sentence). It frees the instrument's bytes exactly once: a second
// concurrent caller finds the reduction already made and returns having
// freed nothing further of its own, the way one physical unload frees bytes
// once no matter how many askers are waiting on it.
type pausedYield struct {
	name    string
	in      *fake
	frees   int64
	release chan struct{}
	entered chan struct{}

	mu      sync.Mutex
	holding []string
	done    bool
}

func newPausedYield(name, model string, frees int64, in *fake) *pausedYield {
	return &pausedYield{name: name, in: in, frees: frees, holding: []string{model},
		release: make(chan struct{}), entered: make(chan struct{}, 2)}
}

func (h *pausedYield) Holder() string   { return h.name }
func (h *pausedYield) Resource() string { return instrument.Card0 }

func (h *pausedYield) Holding(context.Context) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.holding, nil
}

// Yield records that a caller has arrived, parks it until the test releases
// it, and reduces the instrument once.
func (h *pausedYield) Yield(ctx context.Context) error {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	h.mu.Lock()
	already := h.done
	h.done = true
	h.holding = nil
	h.mu.Unlock()
	if already {
		return nil
	}
	h.in.mu.Lock()
	for i := range h.in.samples {
		if h.in.samples[i].Amount >= h.frees {
			h.in.samples[i].Amount -= h.frees
			break
		}
	}
	h.in.at = time.Now()
	h.in.mu.Unlock()
	return nil
}

func askerNamed(name string) Subject {
	return Subject{Program: `C:\askers\` + name + `.exe`, Account: "S-1-5-21-7-" + name}
}

// Two askers reach the same attached holder's yield at the same moment and
// are both parked inside Yield until the test releases it together, proving
// two waiters on one yield rather than assuming a scheduling accident. The
// pool fits only one of the two 60 GiB requests once the holder's 70 GiB
// comes back, so both see enough free the instant the yield settles.
// Book.grant decides against the bytes free under its own lock at that
// moment: the ask that runs first finds them, and the other's check, run
// after the winner's lease is already written, reads not enough. The loser
// re-enters the ask sequence, finds only the winner's own fresh lease left to
// ask, gets no answer from it before its deadline, and reads
// holders_refused.
func TestGrantIsDecidedUnderTheLockAgainstAConcurrentGrant(t *testing.T) {
	in := card(100*gib, 70*gib)
	paused := newPausedYield("host:paused", "gemma", 70*gib, in)
	book := cardBook(t, in, BookOptions{Attached: []Attached{paused}, Settle: 200 * time.Millisecond})
	x, y := askerNamed("x"), askerNamed("y")
	const waitMs = 300

	var results [2]wire.AcquireResult
	var run sync.WaitGroup
	run.Add(2)
	go func() {
		defer run.Done()
		results[0] = book.Acquire(context.Background(), x, nil, instrument.Card0, 60*gib, waitMs)
	}()
	go func() {
		defer run.Done()
		results[1] = book.Acquire(context.Background(), y, nil, instrument.Card0, 60*gib, waitMs)
	}()

	// Both askers are provably parked inside the one yield before it is let
	// to complete.
	<-paused.entered
	<-paused.entered
	close(paused.release)
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
