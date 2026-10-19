package holder

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	"github.com/openabstractions/abstraction-resource/go/service"
)

// machine is one unified-memory card with a real lease book: each started
// engine adds its measured row under its provider's program, and a stopped
// engine's row leaves, as the instrument shows a real load and unload.
type machine struct {
	in       *instrument.Fixed
	capacity int64
	book     *service.Book
	mu       sync.Mutex
	rows     map[string]instrument.Sample
}

func newMachine(t *testing.T, capacity int64) *machine {
	t.Helper()
	m := &machine{in: instrument.NewFixed(), capacity: capacity, rows: map[string]instrument.Sample{}}
	m.in.Set(instrument.Card0, instrument.FixedResource{Capacity: capacity, Pool: instrument.Memory})
	book, err := service.OpenBook(service.BookOptions{Table: service.NewTable(m.in, nil, time.Nanosecond),
		Hold:   func(context.Context, *identity.Peer, string, string) error { return nil },
		Settle: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	m.book = book
	return m
}

func (m *machine) set(name string, row *instrument.Sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row == nil {
		delete(m.rows, name)
	} else {
		m.rows[name] = *row
	}
	samples := []instrument.Sample{}
	for _, s := range m.rows {
		samples = append(samples, s)
	}
	m.in.Set(instrument.Card0, instrument.FixedResource{Capacity: m.capacity, Pool: instrument.Memory, Samples: samples})
}

// provider is one program's lease service over the machine's book.
type provider struct {
	m   *machine
	who service.Subject
}

func (p *provider) Acquire(resource string, amount int64, wait time.Duration) (res.AcquireResult, error) {
	return p.m.book.Acquire(context.Background(), p.who, &identity.Peer{}, resource, amount, wait.Milliseconds()), nil
}
func (p *provider) AcquireFree(resource string, amount int64) (res.AcquireFreeResult, error) {
	return p.m.book.AcquireFree(context.Background(), p.who, &identity.Peer{}, resource, amount), nil
}
func (p *provider) BindProcess(string, int) (res.ProcessBindResult, error) {
	return res.ProcessBindResult{Outcome: res.ProcessBindOutcomeUnverifiable}, nil
}
func (p *provider) Renew(id string) (res.LeaseChange, error) { return p.m.book.Renew(p.who, id), nil }
func (*provider) Label(string, string) (res.LeaseChange, error) {
	return res.LeaseChange{Applied: true}, nil
}

func (p *provider) Release(id string) (res.LeaseChange, error) {
	return p.m.book.Release(p.who, id), nil
}
func (p *provider) Observe(ctx context.Context, cursor string, wait time.Duration) (res.YieldPage, error) {
	return p.m.book.Observe(ctx, p.who, cursor, wait.Milliseconds())
}
func (p *provider) Answer(_ context.Context, id string, answer res.YieldAnswer, reason string) (res.AnswerResult, error) {
	return p.m.book.Answer(p.who, id, answer, reason), nil
}

// engine is a started component whose measured row the machine shows while
// it runs.
type engine struct {
	m     *machine
	name  string
	alive atomic.Bool
}

func (e *engine) PID() int                    { return 0 }
func (e *engine) Ready(context.Context) error { return nil }
func (e *engine) Alive() bool                 { return e.alive.Load() }
func (e *engine) Stop() bool {
	e.alive.Store(false)
	e.m.set(e.name, nil)
	return true
}

func (m *machine) start(name, program string, amount int64) func() (Owned, error) {
	return func() (Owned, error) {
		e := &engine{m: m, name: name}
		e.alive.Store(true)
		m.set(name, &instrument.Sample{PID: 1, Program: program, Account: arbitrationAccount, Amount: amount})
		return e, nil
	}
}

const arbitrationAccount = "S-1-5-21-7-1001"

var (
	chatProgram     = `C:\oa\modelhostd.exe`
	picturesProgram = `C:\oa\pytorchd.exe`
)

func (m *machine) holder(t *testing.T, program string, active func(string) bool) *Holder {
	t.Helper()
	h, err := New(Options{Leases: &provider{m: m, who: service.Subject{Program: program, Account: arbitrationAccount}},
		Resource: instrument.Card0, Idle: time.Hour, Wait: 20 * time.Second, YieldWait: 20 * time.Second, Active: active})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); h.Close() })
	return h
}

const g = int64(1) << 30

// chatLoad is a chat engine at two context sizes: the full plan and a
// smaller one, each leasing its own weights plus KV (RES-L9).
func chatLoad(m *machine, started *atomic.Int64) func() (Component, error) {
	at := func(amount int64) func() (Owned, error) {
		start := m.start("chat", chatProgram, amount)
		return func() (Owned, error) { started.Store(amount); return start() }
	}
	return func() (Component, error) {
		return Component{Amount: 60 * g, Start: at(60 * g), Smaller: []Component{{Amount: 40 * g, Start: at(40 * g)}}}, nil
	}
}

func pictureLoad(m *machine) func() (Component, error) {
	return func() (Component, error) {
		return Component{Amount: 50 * g, Start: m.start("picture", picturesProgram, 50*g)}, nil
	}
}

// Chat and picture leases coexist when both fit: neither is asked to yield,
// and chat runs its full plan.
func TestChatAndPictureCoexistWhenTheyFit(t *testing.T) {
	m := newMachine(t, 120*g)
	var size atomic.Int64
	chat := m.holder(t, chatProgram, nil)
	pictures := m.holder(t, picturesProgram, nil)
	if err := chat.Ensure(context.Background(), "chat", chatLoad(m, &size)); err != nil {
		t.Fatal(err)
	}
	if err := pictures.Ensure(context.Background(), "picture", pictureLoad(m)); err != nil {
		t.Fatal(err)
	}
	if size.Load() != 60*g {
		t.Fatalf("chat loaded at %d GiB, want its full 60", size.Load()/g)
	}
	if _, loaded := chat.Snapshot("chat"); !loaded {
		t.Fatal("chat was asked to yield although both fit")
	}
	if len(m.book.Leases()) != 2 {
		t.Fatalf("leases %+v", m.book.Leases())
	}
}

// A picture load that does not fit beside chat makes chat yield once its
// running turn ends; the turn is never cut, the picture load succeeds, and
// chat's next load takes the largest size that fits beside the picture.
func TestPictureMakesChatShrinkNeverFail(t *testing.T) {
	m := newMachine(t, 100*g)
	var size atomic.Int64
	var turns atomic.Int32
	chat := m.holder(t, chatProgram, func(string) bool { return turns.Load() > 0 })
	pictures := m.holder(t, picturesProgram, nil)
	if err := chat.Ensure(context.Background(), "chat", chatLoad(m, &size)); err != nil {
		t.Fatal(err)
	}
	if size.Load() != 60*g {
		t.Fatalf("chat alone loaded at %d GiB", size.Load()/g)
	}
	turns.Add(1) // a chat turn is running
	done := make(chan error, 1)
	go func() { done <- pictures.Ensure(context.Background(), "picture", pictureLoad(m)) }()
	select {
	case err := <-done:
		t.Fatalf("picture load finished during the chat turn: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, loaded := chat.Snapshot("chat"); !loaded {
		t.Fatal("the running chat turn lost its engine")
	}
	turns.Add(-1) // the turn ends
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("picture load failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("picture load did not follow the chat turn's end")
	}
	if _, loaded := pictures.Snapshot("picture"); !loaded {
		t.Fatal("picture not resident")
	}
	if err := chat.Ensure(context.Background(), "chat", chatLoad(m, &size)); err != nil {
		t.Fatalf("chat reload beside the picture failed: %v", err)
	}
	if size.Load() != 40*g {
		t.Fatalf("chat reloaded at %d GiB, want the 40 GiB size that fits beside the picture", size.Load()/g)
	}
	if _, loaded := pictures.Snapshot("picture"); !loaded {
		t.Fatal("chat's smaller reload evicted the picture")
	}
}

// A chat turn longer than the picture's acquire wait still never fails the
// picture: chat refuses each yield while its turn runs, the picture asks
// again, and the next ask after the turn ends is granted.
func TestALongTurnDelaysThePictureWithoutFailingIt(t *testing.T) {
	m := newMachine(t, 100*g)
	var size atomic.Int64
	var turns atomic.Int32
	chat, err := New(Options{Leases: &provider{m: m, who: service.Subject{Program: chatProgram, Account: arbitrationAccount}},
		Resource: instrument.Card0, Idle: time.Hour, Wait: 20 * time.Second, YieldWait: 100 * time.Millisecond,
		Active: func(string) bool { return turns.Load() > 0 }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go chat.Run(ctx)
	t.Cleanup(func() { cancel(); chat.Close() })
	pictures, err := New(Options{Leases: &provider{m: m, who: service.Subject{Program: picturesProgram, Account: arbitrationAccount}},
		Resource: instrument.Card0, Idle: time.Hour, Wait: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pictures.Close)
	if err := chat.Ensure(context.Background(), "chat", chatLoad(m, &size)); err != nil {
		t.Fatal(err)
	}
	turns.Add(1)
	done := make(chan error, 1)
	go func() { done <- pictures.Ensure(context.Background(), "picture", pictureLoad(m)) }()
	time.Sleep(500 * time.Millisecond) // longer than the picture's own wait
	select {
	case err := <-done:
		t.Fatalf("picture load ended during the turn: %v", err)
	default:
	}
	turns.Add(-1)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("picture load failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("picture load did not follow the turn")
	}
}
