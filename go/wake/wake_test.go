package wake

import (
	"context"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	res "github.com/openabstractions/abstraction-resource/go/client"
)

type fakeLeases struct {
	mu              sync.Mutex
	taken, released int
	deny            bool
	renew           chan struct{}
}

func (f *fakeLeases) AcquireContext(context.Context, string, int64, time.Duration) (res.AcquireResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.taken++
	if f.deny {
		return res.AcquireResult{Outcome: res.AcquireOutcomeNotPermitted}, nil
	}
	return res.AcquireResult{Outcome: res.AcquireOutcomeAcquired, Lease: &wire.Lease{ID: "wake", RenewBy: time.Now().Add(90 * time.Millisecond).UTC().Format(time.RFC3339Nano)}}, nil
}
func (f *fakeLeases) RenewContext(ctx context.Context, id string) (res.LeaseChange, error) {
	select {
	case f.renew <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return res.LeaseChange{}, ctx.Err()
}
func (f *fakeLeases) ReleaseContext(context.Context, string) (res.LeaseChange, error) {
	f.mu.Lock()
	f.released++
	f.mu.Unlock()
	return res.LeaseChange{Applied: true}, nil
}

func TestWakeCancellationReleasesAndJoinsBlockedRenewal(t *testing.T) {
	f := &fakeLeases{renew: make(chan struct{}, 1)}
	held, err := (Holder{Client: f}).Hold("download", "job")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.renew:
	case <-time.After(time.Second):
		t.Fatal("no renewal")
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	held.Release()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.taken != 1 || f.released != 1 {
		t.Fatal(f.taken, f.released)
	}
}
func TestWakeDenialNeverReacquiresOrFallsBack(t *testing.T) {
	f := &fakeLeases{deny: true}
	if held, err := (Holder{Client: f}).Hold("cli", "reason"); err == nil || held != nil {
		t.Fatal(held, err)
	}
	if f.taken != 1 || f.released != 0 {
		t.Fatal(f.taken, f.released)
	}
}
