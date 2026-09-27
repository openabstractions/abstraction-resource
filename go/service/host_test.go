package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	resourceclient "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

type resourceRequestCapture struct{ frame []byte }

func (c *resourceRequestCapture) ExchangeFrame(frame []byte) ([]byte, error) {
	c.frame = append([]byte(nil), frame...)
	return nil, errors.New("request captured")
}

func TestResourceHostServesSharedSessions(t *testing.T) {
	endpoint := listen.Endpoint(fmt.Sprintf("resource-session-test-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err := listen.CanEver(endpoint, Bound); err != nil {
		t.Skip("this platform cannot bind callers here: ", err)
	}
	host, err := Listen(endpoint, cardTable(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() { cancel(); host.Close(); <-done })
	capture := &resourceRequestCapture{}
	wire.NewTableClient(capture).Resources()
	if len(capture.frame) == 0 {
		t.Fatal("generated client did not encode a resource request")
	}

	conn, err := listen.Dial(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var header [4]byte
	for i := 0; i < 2; i++ {
		binary.BigEndian.PutUint32(header[:], 0x80000000|uint32(len(capture.frame)))
		if n, err := conn.Write(header[:]); err != nil || n != len(header) {
			t.Fatalf("request %d header: %d bytes, %v", i, n, err)
		}
		if i == 0 {
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				t.Fatal(err)
			}
			if got := binary.BigEndian.Uint32(header[:]); got != 0x80000000 {
				t.Fatalf("host session response %#x, want acceptance", got)
			}
		}
		if n, err := conn.Write(capture.frame); err != nil || n != len(capture.frame) {
			t.Fatalf("request %d body: %d bytes, %v", i, n, err)
		}
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			t.Fatalf("request %d response header: %v", i, err)
		}
		response := binary.BigEndian.Uint32(header[:])
		if response&0x80000000 == 0 || response&0x3fffffff == 0 {
			t.Fatalf("request %d response header %#x, want continuing reply", i, response)
		}
		if _, err := io.CopyN(io.Discard, conn, int64(response&0x3fffffff)); err != nil {
			t.Fatalf("request %d response body: %v", i, err)
		}
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, header[:]); err != nil || binary.BigEndian.Uint32(header[:]) != 0xffffffff {
		t.Fatalf("idle session after shutdown: marker %#x, error %v", binary.BigEndian.Uint32(header[:]), err)
	}
}

// served starts a host over a real endpoint and returns a client bound to it.
// The test process is its own caller, so the peer the policy and the own-rows
// rule see is this test binary.
func served(t *testing.T, table *Table, policy Policy) *resourceclient.Client {
	t.Helper()
	return serve(t, table, policy, nil)
}

// servedWithLeases starts a host serving the table and the lease book beside
// it on one endpoint, which is how the runtime composes them.
func servedWithLeases(t *testing.T, table *Table, book *Book) *resourceclient.Client {
	t.Helper()
	return serve(t, table, nil, book)
}

func serve(t *testing.T, table *Table, policy Policy, book *Book) *resourceclient.Client {
	t.Helper()
	endpoint := listen.Endpoint(fmt.Sprintf("resource-table-test-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000))
	if err := listen.CanEver(endpoint, Bound); err != nil {
		t.Skip("this platform cannot bind callers here: ", err)
	}
	host, err := Listen(endpoint, table)
	if err != nil {
		t.Fatal(err)
	}
	if policy != nil {
		if err := host.EnablePolicy(policy); err != nil {
			t.Fatal(err)
		}
	}
	if book != nil {
		if err := host.EnableLeases(book); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		host.Close()
		<-done
	})
	return resourceclient.New(endpoint)
}

// self is this test binary's path as rights names a subject program.
func self(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(path)
}

func cardTable(t *testing.T) *Table {
	t.Helper()
	in := &fake{name: instrument.NameWindowsGPUCounters, offers: []string{instrument.Card0}, at: time.Now(),
		samples: []instrument.Sample{
			{PID: 1, Program: `C:\lms\llama-server.exe`, Account: "S-1-5-21-7-1001", Amount: 17 << 30},
			{PID: os.Getpid(), Program: self(t), Account: "S-1-5-21-7-1001", Amount: 1 << 30},
		}}
	return NewTable(in, nil, time.Second)
}

// A permitted caller reads the machine's table.
func TestPermittedCallerReadsEveryRow(t *testing.T) {
	var asked [][2]string
	c := served(t, cardTable(t), func(_ context.Context, _ *identity.Peer, action, resource string) error {
		asked = append(asked, [2]string{action, resource})
		return nil
	})
	state, err := c.Holders(instrument.Card0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 2 {
		t.Fatalf("a permitted caller read %d rows: %+v", len(state.Holders), state.Holders)
	}
	if len(asked) != 1 || asked[0] != [2]string{ActionTableRead, ResourceAccount} {
		t.Fatalf("the gate was asked %v; RES-T4 is table.read on account", asked)
	}
}

// A refused caller reads its own rows and keeps the machine's held and
// capacity, which name no program (CONTRACT.md RES-T4).
func TestRefusedCallerReadsOnlyItsOwnRows(t *testing.T) {
	c := served(t, cardTable(t), func(context.Context, *identity.Peer, string, string) error {
		return errors.New("no rule")
	})
	state, err := c.Holders(instrument.Card0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 {
		t.Fatalf("a refused caller read %d rows: %+v", len(state.Holders), state.Holders)
	}
	if state.Holders[0].Program != self(t) {
		t.Fatalf("the row a refused caller kept is not its own: %+v", state.Holders[0])
	}
	if state.Held != (17<<30)+(1<<30) {
		t.Fatalf("held %d stopped describing the machine", state.Held)
	}
}

// A decision point that cannot answer is never a permit and never a
// narrowing: the caller is told.
func TestUnavailablePolicyRefusesTheCall(t *testing.T) {
	c := served(t, cardTable(t), func(context.Context, *identity.Peer, string, string) error {
		return ErrPolicyUnavailable
	})
	_, err := c.Holders(instrument.Card0, false)
	var refusal *wire.ServiceError
	if !errors.As(err, &refusal) || refusal.Code != wire.ServiceErrorCodePolicyUnavailable {
		t.Fatalf("an unavailable decision gave %v", err)
	}
}

// Resources names what can be reported; it names no holder and is not gated.
func TestResourcesAnswersARefusedCaller(t *testing.T) {
	c := served(t, cardTable(t), func(context.Context, *identity.Peer, string, string) error {
		return errors.New("no rule")
	})
	list, err := c.Resources()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Resources) != 1 || list.Resources[0] != instrument.Card0 {
		t.Fatalf("resources %v", list.Resources)
	}
}

// An empty resource name is a bad request, not an empty table.
func TestEmptyResourceIsInvalid(t *testing.T) {
	c := served(t, cardTable(t), nil)
	_, err := c.Holders("", false)
	var refusal *wire.ServiceError
	if !errors.As(err, &refusal) || refusal.Code != wire.ServiceErrorCodeInvalidRequest {
		t.Fatalf("an empty resource gave %v", err)
	}
}
