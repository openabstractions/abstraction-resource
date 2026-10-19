package holder

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
)

func TestDetachedLoadLiveWaiterStanding(t *testing.T) {
	for _, mode := range []string{"unobserved", "other_waiter", "provider_active"} {
		t.Run(mode, func(t *testing.T) {
			var live, active atomic.Bool
			live.Store(true)
			active.Store(mode == "provider_active")
			leases := &testLeases{}
			h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour, YieldWait: 100 * time.Millisecond, Active: func(string) bool { return active.Load() }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(h.Close)
			o := &joinedOwned{testOwned: testOwned{alive: true, stop: true}, readying: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
			loaded := make(chan error, 1)
			go func() {
				loaded <- h.EnsureWithWaiter(context.Background(), "m", func() (Component, error) {
					return Component{Amount: 4096, Start: func() (Owned, error) { return o, nil }}, nil
				}, live.Load)
			}()
			<-o.readying
			if mode != "other_waiter" {
				live.Store(false)
			}
			if answer, _ := h.Yield(res.YieldRequest{ID: "foreign", Resource: "card:0", Amount: 4096, Lease: "foreign-lease"}); answer != res.YieldAnswerRefused {
				t.Fatal("foreign lease yielded")
			}
			select {
			case <-o.cancelled:
				t.Fatal("foreign lease cancelled this load")
			default:
			}
			answered := make(chan res.YieldAnswer, 1)
			go func() {
				answer, _ := h.Yield(res.YieldRequest{ID: "yield", Resource: "card:0", Amount: 4096, Lease: "lease-1"})
				answered <- answer
			}()
			if mode == "unobserved" {
				select {
				case <-o.cancelled:
				case <-time.After(time.Second):
					t.Fatal("unobserved load did not yield")
				}
				if err := <-loaded; !errors.Is(err, ErrSpeculativeCancelled) {
					t.Fatalf("load: %v", err)
				}
				if answer := <-answered; answer != res.YieldAnswerYielded {
					t.Fatal(answer)
				}
				// A later foreground request uses the same normal acquisition policy.
				if err := h.Ensure(context.Background(), "m", prepare(&testOwned{alive: true, stop: true})); err != nil {
					t.Fatal(err)
				}
				if acquired, _ := leases.counts(); acquired != 2 {
					t.Fatalf("acquired %d", acquired)
				}
			} else {
				select {
				case <-o.cancelled:
					t.Fatal("live work was demoted")
				case <-time.After(20 * time.Millisecond):
				}
				close(o.release)
				if err := <-loaded; err != nil {
					t.Fatal(err)
				}
				answer := <-answered
				if mode == "provider_active" && answer != res.YieldAnswerRefused {
					t.Fatal(answer)
				}
			}
		})
	}
}

func TestDetachedLoadQueuedLiveWaiterProtectsSpeculativeLoad(t *testing.T) {
	leases := &freeLeases{testLeases: &testLeases{}, outcome: res.AcquireFreeOutcomeAcquired}
	h := speculativeHolder(t, leases, nil)
	o := &joinedOwned{testOwned: testOwned{alive: true, stop: true}, readying: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	loaded := make(chan error, 1)
	go func() {
		loaded <- h.EnsureSpeculative(context.Background(), "m", func() (Component, error) {
			return Component{Amount: 4096, Start: func() (Owned, error) { return o, nil }}, nil
		})
	}()
	<-o.readying
	joined := make(chan error, 1)
	go func() {
		joined <- h.EnsureWithWaiter(context.Background(), "m", prepare(&testOwned{alive: true, stop: true}), func() bool { return true })
	}()
	s := h.get("m")
	for deadline := time.Now().Add(time.Second); ; {
		s.startedMu.Lock()
		registered := len(s.liveWaiters) > 0
		s.startedMu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter did not register before component lock")
		}
		time.Sleep(time.Millisecond)
	}
	answered := make(chan struct{})
	go func() {
		h.Yield(res.YieldRequest{ID: "yield", Resource: "card:0", Amount: 4096, Lease: "free-1"})
		close(answered)
	}()
	select {
	case <-o.cancelled:
		t.Fatal("queued real waiter was ignored")
	case <-time.After(20 * time.Millisecond):
	}
	close(o.release)
	if err := <-loaded; err != nil {
		t.Fatal(err)
	}
	if err := <-joined; err != nil {
		t.Fatal(err)
	}
	<-answered
}
