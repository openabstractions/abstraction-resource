package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	watch "github.com/openabstractions/abstraction-watch/go"
)

func TestYieldObserveBoundsWaitersAndRechecksAfterWake(t *testing.T) {
	b := cardBook(t, card(120*gib), BookOptions{})
	b.observe = watch.NewGate(1)
	refused := errors.New("binding withdrawn")
	var withdrawn atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, err := b.observeContext(t.Context(), asker(), "", 5000, func() error {
			if withdrawn.Load() {
				return refused
			}
			return nil
		})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for b.observe.Waiting() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("waiter did not enter gate")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := b.Observe(context.Background(), asker(), "", 1); !errors.Is(err, errObserveBusy) {
		t.Fatalf("full gate: %v", err)
	}
	withdrawn.Store(true)
	b.observe.Advance()
	select {
	case err := <-done:
		if !errors.Is(err, refused) {
			t.Fatalf("wake authority: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("advance did not wake observer")
	}
}
