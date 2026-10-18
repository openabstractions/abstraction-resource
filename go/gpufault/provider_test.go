package gpufault

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-resource/go/holder"
)

// lockedBuffer is a buffer two goroutines write.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// blindSource is a platform whose records cannot be read.
type blindSource struct{}

func (blindSource) Watch(context.Context, func(Event)) error { return errors.New("no event log") }

// ForProvider is the one composition every local-engine provider uses
// (RES-G1, RES-G3, RES-G6): a reset stops the attached holder's engine, the
// request it failed is quarantined in the provider's state, the fault is one
// log record and one stderr line named for the provider, and a record source
// that cannot be read is reported and logged.
func TestForProviderComposesTheProvidersGuard(t *testing.T) {
	state := t.TempDir()
	var logged, stderr lockedBuffer
	var reported []error
	var mu sync.Mutex
	g, err := ForProvider(ProviderOptions{Name: "someproviderd", State: state, Records: blindSource{},
		Logger: slog.New(slog.NewJSONHandler(&logged, nil)), Stderr: &stderr,
		Report: func(err error) { mu.Lock(); reported = append(reported, err); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	leases := newFakeLeases()
	h, err := holder.New(holder.Options{Leases: leases, Resource: "card:0", Idle: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	g.Attach(h)
	e := newEngine()
	if err := h.Ensure(context.Background(), "engine", func() (holder.Component, error) {
		return holder.Component{Amount: 1 << 20, Start: func() (holder.Owned, error) { return e, nil }}, nil
	}); err != nil {
		t.Fatal(err)
	}
	g.Report(Event{Kind: Reset, At: time.Now(), Source: "WerKernel 1001 WATCHDOG"})
	if e.Alive() || leases.liveOn("card:0") != 0 {
		t.Fatal("the reset left the engine or its lease")
	}
	g.Failed(Fingerprint{Model: "m", Engine: "sd-server", Shape: "512x512"})
	g.Flush()
	if _, err := os.Stat(filepath.Join(state, QuarantineFile)); err != nil {
		t.Fatalf("the quarantine is not in the provider's state: %v", err)
	}
	if line := stderr.String(); !strings.HasPrefix(line, "someproviderd: The graphics driver reset; engine was stopped") {
		t.Fatalf("stderr %q", line)
	}
	if !strings.Contains(logged.String(), `"msg":"gpu fault"`) {
		t.Fatalf("no fault record: %s", logged.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	g.Run(ctx)
	mu.Lock()
	n := len(reported)
	mu.Unlock()
	if n == 0 || g.Blind() == nil || !strings.Contains(logged.String(), `"msg":"gpu fault handling"`) {
		t.Fatalf("a blind source: %d report(s), blind %v, log %s", n, g.Blind(), logged.String())
	}
}
