package holder

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-resource/go/instrument"
	"github.com/openabstractions/abstraction-resource/go/service"
)

// holderWith is a running holder of program on the machine with options
// the test chooses (RES-L3, RES-L9).
func (m *machine) holderWith(t *testing.T, program string, opts Options) *Holder {
	t.Helper()
	opts.Leases = &provider{m: m, who: service.Subject{Program: program, Account: arbitrationAccount}}
	opts.Resource = instrument.Card0
	if opts.Idle == 0 {
		opts.Idle = time.Hour
	}
	if opts.Wait == 0 {
		opts.Wait = 20 * time.Second
	}
	if opts.YieldWait == 0 {
		opts.YieldWait = 20 * time.Second
	}
	h, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); h.Close() })
	return h
}

func fixed(m *machine, name, program string, amount int64) func() (Component, error) {
	return func() (Component, error) {
		return Component{Amount: amount, Start: m.start(name, program, amount)}, nil
	}
}

func ensureWithin(t *testing.T, h *Holder, name string, load func() (Component, error), bound time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.Ensure(context.Background(), name, load) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s did not load: %v", name, err)
		}
	case <-time.After(bound):
		t.Fatalf("%s did not load within %s", name, bound)
	}
}

// An idle model of the asking provider gives way the moment another of its
// models needs the room, and the plan takes the smallest lease that covers
// what is short: the idle Klein set, never the larger chat model beside it.
func TestAnIdleSiblingGivesWayForTheNextModel(t *testing.T) {
	m := newMachine(t, 70*g)
	chat := m.holderWith(t, chatProgram, Options{})
	pictures := m.holderWith(t, picturesProgram, Options{})
	if err := chat.Ensure(context.Background(), "qwen", fixed(m, "qwen", chatProgram, 30*g)); err != nil {
		t.Fatal(err)
	}
	if err := pictures.Ensure(context.Background(), "klein", fixed(m, "klein", picturesProgram, 20*g)); err != nil {
		t.Fatal(err)
	}
	ensureWithin(t, pictures, "z-image", fixed(m, "z-image", picturesProgram, 31*g), 5*time.Second)
	if _, loaded := pictures.Snapshot("klein"); loaded {
		t.Fatal("the idle Klein set stayed resident although Z-Image needed its room")
	}
	if _, loaded := chat.Snapshot("qwen"); !loaded {
		t.Fatal("chat was unloaded although the smaller idle Klein set covered the shortfall")
	}
}

// An idle chat model yields at once for a picture, even while its last reply
// is still kept for observation (Active true, nothing in flight).
func TestAnIdleChatModelYieldsAtOnceForAPicture(t *testing.T) {
	m := newMachine(t, 60*g)
	chat := m.holderWith(t, chatProgram, Options{
		Active:   func(string) bool { return true }, // a reply kept for observation
		InFlight: func(string) int { return 0 },
	})
	pictures := m.holderWith(t, picturesProgram, Options{})
	if err := chat.Ensure(context.Background(), "qwen", fixed(m, "qwen", chatProgram, 36*g)); err != nil {
		t.Fatal(err)
	}
	ensureWithin(t, pictures, "z-image", fixed(m, "z-image", picturesProgram, 31*g), 3*time.Second)
	if _, loaded := chat.Snapshot("qwen"); loaded {
		t.Fatal("the idle chat model did not yield")
	}
}

// A client that starts each chat turn before the previous one ends leaves
// no idle moment; the picture still loads, at the next turn boundary: the
// next turn waits behind the yield, chat yields, the picture loads and the
// held-back turn loads chat again.
func TestABusyChatYieldsAtItsNextTurnBoundary(t *testing.T) {
	m := newMachine(t, 70*g)
	var inFlight atomic.Int32
	var chat *Holder
	chat = m.holderWith(t, chatProgram, Options{
		Active:   func(string) bool { return inFlight.Load() > 0 },
		InFlight: func(string) int { return int(inFlight.Load()) },
	})
	pictures := m.holderWith(t, picturesProgram, Options{})
	var size atomic.Int64
	chatLoad := func() (Component, error) {
		at := func(amount int64) func() (Owned, error) {
			start := m.start("qwen", chatProgram, amount)
			return func() (Owned, error) { size.Store(amount); return start() }
		}
		return Component{Amount: 50 * g, Start: at(50 * g), Smaller: []Component{{Amount: 30 * g, Start: at(30 * g)}}}, nil
	}
	if err := chat.Ensure(context.Background(), "qwen", chatLoad); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	turns := make(chan error, 1)
	inFlight.Add(1) // the first turn runs
	go func() {
		for {
			select {
			case <-stop:
				inFlight.Add(-1)
				turns <- nil
				return
			default:
			}
			time.Sleep(20 * time.Millisecond)
			// The next turn is admitted before the running one ends.
			inFlight.Add(1)
			inFlight.Add(-1)
			if err := chat.Pass(context.Background(), "qwen"); err != nil {
				turns <- err
				return
			}
			if err := chat.Ensure(context.Background(), "qwen", chatLoad); err != nil {
				turns <- err
				return
			}
		}
	}()
	ensureWithin(t, pictures, "z-image", fixed(m, "z-image", picturesProgram, 31*g), 5*time.Second)
	close(stop)
	if err := <-turns; err != nil {
		t.Fatalf("a chat turn failed: %v", err)
	}
	if size.Load() != 30*g {
		t.Fatalf("chat reloaded at %d GiB, want the 30 GiB size beside the picture", size.Load()/g)
	}
	if _, loaded := pictures.Snapshot("z-image"); !loaded {
		t.Fatal("chat's reload evicted the picture")
	}
}

// A load that would not fit even after every lease yields unloads nothing
// and is refused at once, insufficient, without asking anybody.
func TestAnEvictionThatCannotMakeRoomUnloadsNothing(t *testing.T) {
	m := newMachine(t, 60*g)
	m.set("browser", &instrument.Sample{PID: 9, Program: `C:\browser.exe`, Account: arbitrationAccount, Amount: 25 * g})
	chat := m.holderWith(t, chatProgram, Options{})
	pictures := m.holderWith(t, picturesProgram, Options{})
	if err := chat.Ensure(context.Background(), "qwen", fixed(m, "qwen", chatProgram, 30*g)); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	err := pictures.Ensure(context.Background(), "z-image", fixed(m, "z-image", picturesProgram, 40*g))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Word() != WordInsufficient {
		t.Fatalf("got %v, want %s", err, WordInsufficient)
	}
	if len(failure.Result.Asked) != 0 {
		t.Fatalf("asked %+v; a load that cannot fit asks nobody", failure.Result.Asked)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("the refusal took %s; it must come at once", took)
	}
	if _, loaded := chat.Snapshot("qwen"); !loaded {
		t.Fatal("chat was unloaded for a load that could not fit anyway")
	}
}
