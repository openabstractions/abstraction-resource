package holder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	"github.com/openabstractions/abstraction-resource/go/service"
)

var memoryEndpointSequence atomic.Uint64

// The person's Memory record (resource RES-L10), written by a served lease
// book that a provider's holder reaches through the resource client, as an
// installed provider does. The holder labels its lease as Options.Label says
// and again through Label; a yield the instrument confirms is one freed
// record naming the holder's lease, its label and the asker's new lease; an
// acquire that ends insufficient while memory no lease covers stands is one
// foreign record naming the largest such program.
func TestTheMemoryRecordFollowsLabelsYieldsAndForeignMemory(t *testing.T) {
	const gib = int64(1) << 30
	in := instrument.NewFixed()
	var mu sync.Mutex
	samples := map[int]instrument.Sample{}
	measure := func(change func()) {
		mu.Lock()
		defer mu.Unlock()
		change()
		var rows []instrument.Sample
		for _, sample := range samples {
			rows = append(rows, sample)
		}
		in.Set(instrument.Card0, instrument.FixedResource{Capacity: 48 * gib, Samples: rows})
	}
	measure(func() {})
	table := service.NewTable(in, nil, time.Nanosecond)
	auditPath := filepath.Join(t.TempDir(), "leases.audit")
	audit, err := service.OpenAudit(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { audit.Close() })
	permit := func(context.Context, *identity.Peer, string, string) error { return nil }
	book, err := service.OpenBook(service.BookOptions{Table: table, Audit: audit, Hold: permit, Settle: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	leases := servedBook(t, table, book, permit)

	h, err := New(Options{Leases: leases, Resource: instrument.Card0, Idle: time.Hour, Wait: 10 * time.Second,
		Label: func(name string) string { return name }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)
	engine := &stoppingOwned{stopped: func() { measure(func() { delete(samples, 9) }) }}
	err = h.Ensure(context.Background(), "flux-schnell", func() (Component, error) {
		return Component{Amount: 20 * gib, Start: func() (Owned, error) {
			measure(func() { samples[9] = instrument.Sample{PID: 9, Program: `C:\oa\engine.exe`, Amount: 20 * gib} })
			return engine, nil
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	held, _ := h.Snapshot("flux-schnell")
	h.Label("flux-schnell", "Krita: flux-schnell")

	asker := service.Subject{Program: `C:\Program Files\Krita\krita.exe`, Account: "S-1-5-21-7-1001"}
	got := book.Acquire(context.Background(), asker, &identity.Peer{}, instrument.Card0, 20*gib, 20000)
	if got.Lease == nil {
		t.Fatalf("the asker read %s, asked %+v", got.Outcome, got.Asked)
	}
	measure(func() {
		samples[7] = instrument.Sample{PID: 7, Program: `C:\Programs\Ollama\ollama.exe`, Amount: 30 * gib}
	})
	other := service.Subject{Program: `C:\Program Files\Blender\blender.exe`, Account: "S-1-5-21-7-1001"}
	if out := book.Acquire(context.Background(), other, &identity.Peer{}, instrument.Card0, 30*gib, 0); out.Outcome != res.AcquireOutcomeInsufficient {
		t.Fatalf("the blocked acquire read %s", out.Outcome)
	}

	lines, err := service.ReadAudit(auditPath, 256)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	var freed, foreign map[string]any
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		switch record["event"] {
		case "label":
			if record["lease"] != held || record["holder"] == "" {
				t.Fatalf("label record %s", line)
			}
			labels = append(labels, record["label"].(string))
		case "freed":
			freed = record
		case "foreign":
			foreign = record
		}
	}
	if fmt.Sprint(labels) != "[flux-schnell Krita: flux-schnell]" {
		t.Fatalf("labels %q", labels)
	}
	if freed == nil || freed["lease"] != held || freed["label"] != "Krita: flux-schnell" || freed["asker"] != asker.Program ||
		freed["asker_lease"] != got.Lease.ID || freed["resource"] != instrument.Card0 || freed["amount"] != float64(20*gib) {
		t.Fatalf("freed record %v", freed)
	}
	if foreign == nil || foreign["asker"] != other.Program || foreign["holder"] != `C:\Programs\Ollama\ollama.exe` ||
		foreign["resource"] != instrument.Card0 || foreign["amount"] != float64(30*gib) {
		t.Fatalf("foreign record %v", foreign)
	}
}

// stoppingOwned is a component whose stop takes its bytes off the instrument.
type stoppingOwned struct {
	mu      sync.Mutex
	gone    bool
	stopped func()
}

func (o *stoppingOwned) PID() int                    { return 0 }
func (o *stoppingOwned) Ready(context.Context) error { return nil }
func (o *stoppingOwned) Alive() bool                 { o.mu.Lock(); defer o.mu.Unlock(); return !o.gone }
func (o *stoppingOwned) Stop() bool {
	o.mu.Lock()
	o.gone = true
	o.mu.Unlock()
	o.stopped()
	return true
}

func servedBook(t *testing.T, table *service.Table, book *service.Book, permit service.Policy) *res.Client {
	t.Helper()
	endpoint := listen.Endpoint(fmt.Sprintf("holder-memory-test-%d-%d", os.Getpid(), memoryEndpointSequence.Add(1)))
	if err := listen.CanEver(endpoint, service.Bound); err != nil {
		t.Skip("this platform cannot bind callers here: ", err)
	}
	host, err := service.Listen(endpoint, table)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.EnablePolicy(permit); err != nil {
		t.Fatal(err)
	}
	if err := host.EnableLeases(book); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		host.Close()
		<-done
	})
	return res.New(endpoint)
}
