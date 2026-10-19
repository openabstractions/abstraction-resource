package service

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// pdhFake is an instrument like Windows' counters: its rows name a PID and
// carry no process creation identity.
type pdhFake struct{ *fake }

func (pdhFake) VerifiedProcessSamples() bool { return false }

// childBook is a modelhost lease of grant on a 100 GiB card whose engine
// child 701 is bound, with the child's handle-bound measurement in the
// test's hands.
func childBook(t *testing.T, grant int64, child func(int, string) (int64, error)) (*Book, *fake) {
	t.Helper()
	started := time.Now().Add(-time.Minute)
	parent := identity.Process{PID: 700, StartTime: started}
	who := Subject{Program: `C:\oa\modelhostd.exe`, Account: bindAccount}
	processes := map[int]instrument.ProcessInfo{
		700: {PID: 700, StartID: "parent-A", BootID: "test-boot", StartTicks: 100, Started: started, Account: bindAccount},
		701: {PID: 701, ParentPID: 700, StartID: "child-A", BootID: "test-boot", StartTicks: 101, Started: started.Add(time.Second), Account: bindAccount},
	}
	in := card(100 * gib)
	options := BookOptions{Table: NewTable(pdhFake{in}, nil, time.Nanosecond), Hold: permitGate, Yield: permitYield,
		Standing: permitStanding, Settle: time.Second, ChildUsage: child,
		InspectProcess: func(pid int) (instrument.ProcessInfo, error) {
			info, ok := processes[pid]
			if !ok {
				return instrument.ProcessInfo{}, errors.New("process cannot be inspected")
			}
			return info, nil
		}}
	book, err := OpenBook(options)
	if err != nil {
		t.Fatal(err)
	}
	result := book.Acquire(context.Background(), who, boundPeer(), instrument.Card0, grant, 0)
	if result.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatalf("modelhost lease: %+v", result)
	}
	if got := book.bindProcess(who, parent, result.Lease.ID, 701, func() error { return nil }).Outcome; got != wire.ProcessBindOutcomeBound {
		t.Fatalf("bind outcome %s", got)
	}
	// The engine's PDH row: its PID, its image, no creation identity.
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: 701, Program: `C:\oa\engines\llama-server.exe`, Account: bindAccount, Amount: 30 * gib}}
	in.mu.Unlock()
	return book, in
}

// A bound engine measured through its own handle offsets its lease once: the
// lease and the engine's PDH row are one occupancy, not two (RES-L2). On the
// owner's machine the double count left a loaded chat model taking twice its
// size from the pool, so no picture fit beside it.
func TestHandleMeasuredChildIsChargedOnce(t *testing.T) {
	book, _ := childBook(t, 32*gib, func(pid int, start string) (int64, error) {
		if pid != 701 || start != "child-A" {
			return 0, errors.New("not the bound child")
		}
		return 29 * gib, nil
	})
	// 100 - 30 measured - (32 - 29) unoccupied = 67.
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 67*gib {
		t.Fatalf("free %d GiB, %v; want 67", free/gib, err)
	}
	picture := Subject{Program: `C:\oa\pytorchd.exe`, Account: bindAccount}
	result := book.Acquire(context.Background(), picture, boundPeer(), instrument.Card0, 31*gib, 0)
	if result.Outcome != wire.AcquireOutcomeAcquired || len(result.Asked) != 0 {
		t.Fatalf("a picture that fits beside chat: %+v", result)
	}
}

// The handle's figure above the PDH row is charged too: the global charge is
// at least the larger of the two.
func TestHandleMeasurementAboveTheRowIsCharged(t *testing.T) {
	book, _ := childBook(t, 32*gib, func(int, string) (int64, error) { return 34 * gib, nil })
	// held 30 + (34 - 30) = 34; the lease is fully occupied: 100 - 34 = 66.
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 66*gib {
		t.Fatalf("free %d GiB, %v; want 66", free/gib, err)
	}
}

// A child the handle cannot measure (failed adapter query) that is still
// the bound generation is charged as its lease or its row, whichever is
// larger: max(32, 30) = 32, and 100 - 32 = 68.
func TestUnmeasuredChildIsChargedTheLargerOnce(t *testing.T) {
	book, _ := childBook(t, 32*gib, func(int, string) (int64, error) { return 0, errors.New("adapter query failed") })
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 68*gib {
		t.Fatalf("free %d GiB, %v; want 68", free/gib, err)
	}
}

// A bound child that exited, or whose PID now names another process, proves
// nothing about the row at its PID: the full reservation stays beside it.
func TestGoneChildKeepsTheReservation(t *testing.T) {
	book, _ := childBook(t, 32*gib, func(int, string) (int64, error) { return 0, errors.New("process exited") })
	book.inspect = func(int) (instrument.ProcessInfo, error) {
		return instrument.ProcessInfo{}, errors.New("process exited")
	}
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 38*gib {
		t.Fatalf("free %d GiB, %v; want 38", free/gib, err)
	}
}

// Without a handle measurement a PDH instrument binds nothing.
func TestPDHWithoutChildMeasurementBindsNothing(t *testing.T) {
	in := card(100 * gib)
	book, err := OpenBook(BookOptions{Table: NewTable(pdhFake{in}, nil, time.Nanosecond), Hold: permitGate,
		InspectProcess: func(int) (instrument.ProcessInfo, error) {
			t.Fatal("an instrument with no child measurement must refuse before inspection")
			return instrument.ProcessInfo{}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	got := book.bindProcess(Subject{Program: `C:\oa\modelhostd.exe`, Account: bindAccount},
		identity.Process{PID: 700, StartTime: time.Now()}, "lease-1", 701, func() error { return nil })
	if got.Outcome != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("binding outcome %s", got.Outcome)
	}
}
