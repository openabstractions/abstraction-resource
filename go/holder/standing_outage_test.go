package holder

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/service"
)

// bookLeases is the holder's lease service over a real lease book for one
// subject, with a switchable rights outage and the book's clock in the
// test's hands.
type bookLeases struct {
	book   *service.Book
	who    service.Subject
	mu     sync.Mutex
	outage bool
	now    time.Time
}

func newBookLeases(t *testing.T) *bookLeases {
	t.Helper()
	l := &bookLeases{who: service.Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}, now: time.Now()}
	book, err := service.OpenBook(service.BookOptions{Table: service.NewTable(nil, nil, time.Nanosecond),
		Now:  l.clock,
		Hold: func(context.Context, *identity.Peer, string, string) error { return nil },
		Standing: func(context.Context, service.Subject, string, string) error {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.outage {
				return fmt.Errorf("%w: rights unavailable", service.ErrPolicyUnavailable)
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	l.book = book
	return l
}

func (l *bookLeases) clock() time.Time { l.mu.Lock(); defer l.mu.Unlock(); return l.now }

func (l *bookLeases) set(outage bool, advance time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outage, l.now = outage, l.now.Add(advance)
}

func (l *bookLeases) Acquire(resource string, amount int64, _ time.Duration) (res.AcquireResult, error) {
	return l.book.Acquire(context.Background(), l.who, &identity.Peer{}, resource, amount, 0), nil
}
func (l *bookLeases) BindProcess(string, int) (res.ProcessBindResult, error) {
	return res.ProcessBindResult{Outcome: res.ProcessBindOutcomeBound}, nil
}
func (l *bookLeases) Renew(id string) (res.LeaseChange, error) { return l.book.Renew(l.who, id), nil }
func (l *bookLeases) Release(id string) (res.LeaseChange, error) {
	return l.book.Release(l.who, id), nil
}
func (l *bookLeases) Observe(ctx context.Context, _ string, _ time.Duration) (res.YieldPage, error) {
	<-ctx.Done()
	return res.YieldPage{}, ctx.Err()
}
func (l *bookLeases) Answer(context.Context, string, res.YieldAnswer, string) (res.AnswerResult, error) {
	return res.AnswerResult{}, nil
}

// RES-L7 through the provider kit's holder: one renewal the book cannot
// decide leaves the loaded component loaded, and renewals decided again
// extend the lease as before. Only an outage that lasts a whole lease
// lifetime ends the lease, and the holder then unloads the component.
func TestARightsOutageUnloadsOnlyAfterAWholeLifetime(t *testing.T) {
	leases := newBookLeases(t)
	h, err := New(Options{Leases: leases, Resource: "card:0", Idle: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	owned := &testOwned{alive: true, stop: true}
	if err := h.Ensure(context.Background(), "model", prepare(owned)); err != nil {
		t.Fatal(err)
	}
	loaded := func() bool {
		h.sweep()
		_, ok := h.Snapshot("model")
		return ok && owned.Alive()
	}

	// One transient outage, then decisions again.
	leases.set(true, 8*time.Second)
	h.renew()
	if !loaded() {
		t.Fatal("one unavailable renewal unloaded the component")
	}
	leases.set(false, 8*time.Second)
	h.renew()
	if !loaded() {
		t.Fatal("the component did not stay loaded once renewals were decided again")
	}

	// An outage through a whole lifetime ends the lease at its renew_by.
	step := service.DefaultLifetime / 4
	for elapsed := time.Duration(0); elapsed <= service.DefaultLifetime; elapsed += step {
		leases.set(true, step)
		h.renew()
	}
	if loaded() {
		t.Fatal("an outage longer than one lifetime left the component loaded")
	}
}
