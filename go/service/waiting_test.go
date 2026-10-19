package service

import (
	"context"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// A second acquire that waits on a holder's yield reads card:held_by:<program>
// while it waits, and reads nothing once the holder has released, with no
// timer between (P-J3a, JOB-A15, RES-L2).
func TestWaitingAcquireNamesTheHolderAndClearsOnRelease(t *testing.T) {
	in := card(120 * gib)
	book := cardBook(t, in, BookOptions{Settle: 2 * time.Second})
	fixture := Subject{Program: `C:\fixture\Slow Render.exe`, Account: "S-1-5-21-7-1001"}
	if got := book.Acquire(context.Background(), fixture, boundPeer(), instrument.Card0, 100*gib, 0); got.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatal(got.Outcome)
	}
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: 100, Program: fixture.Program, Account: fixture.Account, Amount: 100 * gib}}
	in.at = time.Now()
	in.mu.Unlock()
	if word := book.Waiting(asker(), instrument.Card0); word != "" {
		t.Fatalf("waiting %q before any acquire waits", word)
	}

	results := make(chan wire.AcquireResult, 1)
	go func() {
		results <- book.Acquire(context.Background(), asker(), boundPeer(), instrument.Card0, 60*gib, 10000)
	}()
	// The fixture holder delays release: it sees the request and holds on.
	page, err := book.Observe(context.Background(), fixture, "", 5000)
	if err != nil || len(page.Requests) != 1 {
		t.Fatalf("observe %+v, %v", page, err)
	}
	// One recheck after the request reached the holder reads its name.
	if word := book.Waiting(asker(), instrument.Card0); word != "card:held_by:slow_render" {
		t.Fatalf("waiting %q, wanted card:held_by:slow_render", word)
	}
	if word := book.Waiting(fixture, instrument.Card0); word != "" {
		t.Fatalf("the holder itself reads waiting %q", word)
	}
	in.mu.Lock()
	in.samples[0].Amount = 0
	in.at = time.Now()
	in.mu.Unlock()
	book.Answer(fixture, page.Requests[0].ID, wire.YieldAnswerYielded, "")
	if result := <-results; result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("outcome %s after %+v", result.Outcome, result.Asked)
	}
	if word := book.Waiting(asker(), instrument.Card0); word != "" {
		t.Fatalf("waiting %q after the holder released", word)
	}
}

// Two acquires of the same asker on the same pool wait on one delaying
// holder. The first one ending, here by its caller giving up, leaves the
// other one's word in place; it clears when the second is granted (RES-L8).
func TestWaitingWordOutlivesAConcurrentAcquireOfTheSameAsker(t *testing.T) {
	in := card(120 * gib)
	book := cardBook(t, in, BookOptions{Settle: 2 * time.Second})
	fixture := Subject{Program: `C:\fixture\slow_render.exe`, Account: "S-1-5-21-7-1001"}
	if got := book.Acquire(context.Background(), fixture, boundPeer(), instrument.Card0, 100*gib, 0); got.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatal(got.Outcome)
	}
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: 100, Program: fixture.Program, Account: fixture.Account, Amount: 100 * gib}}
	in.at = time.Now()
	in.mu.Unlock()

	first, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan wire.AcquireResult, 1)
	secondDone := make(chan wire.AcquireResult, 1)
	go func() { firstDone <- book.Acquire(first, asker(), boundPeer(), instrument.Card0, 60*gib, 10000) }()
	go func() {
		secondDone <- book.Acquire(context.Background(), asker(), boundPeer(), instrument.Card0, 60*gib, 10000)
	}()
	// The fixture holder sees both requests and holds on.
	var requests []wire.YieldRequest
	for deadline := time.Now().Add(5 * time.Second); len(requests) < 2; {
		if time.Now().After(deadline) {
			t.Fatalf("the holder saw %d requests, wanted 2", len(requests))
		}
		page, err := book.Observe(context.Background(), fixture, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		requests = page.Requests
		time.Sleep(10 * time.Millisecond)
	}
	if word := book.Waiting(asker(), instrument.Card0); word != "card:held_by:slow_render" {
		t.Fatalf("waiting %q with two acquires waiting", word)
	}
	cancelFirst()
	if result := <-firstDone; result.Outcome == wire.AcquireOutcomeAcquired {
		t.Fatalf("the abandoned acquire was granted: %+v", result)
	}
	if word := book.Waiting(asker(), instrument.Card0); word != "card:held_by:slow_render" {
		t.Fatalf("waiting %q while the second acquire still waits", word)
	}
	in.mu.Lock()
	in.samples[0].Amount = 0
	in.at = time.Now()
	in.mu.Unlock()
	for _, request := range requests {
		book.Answer(fixture, request.ID, wire.YieldAnswerYielded, "")
	}
	if result := <-secondDone; result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("outcome %s after %+v", result.Outcome, result.Asked)
	}
	if word := book.Waiting(asker(), instrument.Card0); word != "" {
		t.Fatalf("waiting %q after both acquires returned", word)
	}
}

// The word names the holders that occur: an image path by its base name, a
// package family by its package name (RES-L8).
func TestHeldByWordFitsTheWaitingGrammar(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files\Blender Foundation\blender.exe`: "card:held_by:blender",
		`C:\Tools\Render.EXE`:                             "card:held_by:render",
		"/usr/bin/ollama":                                 "card:held_by:ollama",
		// A dotted POSIX binary keeps its whole name.
		"/usr/bin/python3.12": "card:held_by:python3.12",
		// A package family, as the table names it and as identity does.
		"Microsoft.WindowsTerminal_8wekyb3d8bbwe":      "card:held_by:microsoft.windowsterminal",
		"msix:Microsoft.WindowsTerminal_8wekyb3d8bbwe": "card:held_by:microsoft.windowsterminal",
		"":                 "card:held_by:unknown",
		`C:\x\` + "ÄÖ.exe": "card:held_by:__",
	}
	for program, want := range cases {
		if got := heldBy(program); got != want {
			t.Errorf("heldBy(%q) = %q, want %q", program, got, want)
		}
	}
	long := heldBy(`C:\` + "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij.exe")
	if len(long) > 64 {
		t.Fatalf("word %q is %d bytes", long, len(long))
	}
}
