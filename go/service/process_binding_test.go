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

const bindAccount = "S-1-5-21-7-1001"

func boundBook(t *testing.T, capacity, grant int64) (*Book, *fake, Subject, identity.Process, map[int]instrument.ProcessInfo, string) {
	t.Helper()
	started := time.Now().Add(-time.Minute)
	parent := identity.Process{PID: 700, StartTime: started}
	who := Subject{Program: `C:\oa\modelhost.exe`, Account: bindAccount}
	processes := map[int]instrument.ProcessInfo{
		700: {PID: 700, StartID: "parent-A", BootID: "test-boot", StartTicks: 100, Started: started, Account: bindAccount},
		701: {PID: 701, ParentPID: 700, StartID: "child-A", BootID: "test-boot", StartTicks: 101, Started: started.Add(time.Second), Account: bindAccount},
	}
	in := card(capacity)
	book := cardBook(t, in, BookOptions{InspectProcess: func(pid int) (instrument.ProcessInfo, error) {
		info, ok := processes[pid]
		if !ok {
			return instrument.ProcessInfo{}, errors.New("process cannot be inspected")
		}
		return info, nil
	}})
	result := book.Acquire(context.Background(), who, boundPeer(), instrument.Card0, grant, 0)
	if result.Outcome != wire.AcquireOutcomeAcquired || result.Lease == nil {
		t.Fatalf("initial reservation: %+v", result)
	}
	return book, in, who, parent, processes, result.Lease.ID
}

func bindForTest(book *Book, who Subject, parent identity.Process, lease string, pid int64) wire.ProcessBindResult {
	return book.bindProcess(who, parent, lease, pid, func() error { return nil })
}

func measuredFree(t *testing.T, book *Book, in *fake) int64 {
	t.Helper()
	state, samples, err := book.table.snapshot(instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	want := book.free(state, samples, state.Capacity)
	got, err := book.Free(instrument.Card0, true)
	if err != nil || got != want {
		t.Fatalf("public Free=%d %v; admission free=%d", got, err, want)
	}
	return got
}

func TestBoundChildOffsetsItsOwnReservationOnce(t *testing.T) {
	book, in, who, parent, _, lease := boundBook(t, 100*gib, 60*gib)
	if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeBound {
		t.Fatalf("bind outcome %s", got)
	}
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: 701, StartID: "child-A", Program: `C:\engine\llama-server.exe`, Account: bindAccount, Amount: 50 * gib}}
	in.mu.Unlock()
	if got := measuredFree(t, book, in); got != 40*gib {
		t.Fatalf("free = %d GiB, want 40: child measurement offsets reservation", got/gib)
	}
	if got := book.Acquire(context.Background(), asker(), boundPeer(), instrument.Card0, 40*gib, 0).Outcome; got != wire.AcquireOutcomeAcquired {
		t.Fatalf("remaining 40 GiB could not be granted: %s", got)
	}
}

func TestMeasuredChildAboveEstimateStaysCharged(t *testing.T) {
	book, in, who, parent, _, lease := boundBook(t, 100*gib, 40*gib)
	if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeBound {
		t.Fatalf("bind outcome %s", got)
	}
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: 701, StartID: "child-A", Program: `C:\engine\llama-server.exe`, Account: bindAccount, Amount: 60 * gib}}
	in.mu.Unlock()
	if got := measuredFree(t, book, in); got != 40*gib {
		t.Fatalf("free = %d GiB, want 40: measured use exceeds estimate", got/gib)
	}
}

func TestBindingRequiresVerifiedDirectChildAndOwner(t *testing.T) {
	book, _, who, parent, processes, lease := boundBook(t, 100*gib, 40*gib)
	child := processes[701]
	child.ParentPID = 12
	processes[701] = child
	if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("nonchild bound: %s", got)
	}
	child.ParentPID, child.Account = 700, "another account"
	processes[701] = child
	if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("cross-account child bound: %s", got)
	}
	child.Account = bindAccount
	processes[701] = child
	child.StartTicks = 99
	child.Started = parent.StartTime.Add(-time.Second)
	processes[701] = child
	if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("child created before parent bound: %s", got)
	}
	child.StartTicks = 101
	child.Started = parent.StartTime.Add(time.Second)
	processes[701] = child
	if got := book.bindProcess(who, parent, lease, 701, func() error { return errors.New("caller pin invalid") }).Outcome; got != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("unbound parent accepted: %s", got)
	}
	rechecks := 0
	if got := book.bindProcess(who, parent, lease, 701, func() error {
		rechecks++
		if rechecks == 2 {
			return errors.New("caller exited during inspection")
		}
		return nil
	}).Outcome; got != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("parent that exited during inspection bound: %s", got)
	}
	other := Subject{Program: `C:\other\provider.exe`, Account: bindAccount}
	if got := bindForTest(book, other, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeRefused {
		t.Fatalf("another program bound lease: %s", got)
	}
	delete(processes, 701)
	if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("uninspectable child bound: %s", got)
	}
}

func TestStaleSampleOrReusedChildPIDGetsNoCredit(t *testing.T) {
	book, in, who, parent, processes, lease := boundBook(t, 100*gib, 40*gib)
	if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeBound {
		t.Fatalf("bind outcome %s", got)
	}
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: 701, StartID: "old-sample", Program: `C:\engine\llama-server.exe`, Account: bindAccount, Amount: 35 * gib}}
	in.mu.Unlock()
	if got := measuredFree(t, book, in); got != 25*gib {
		t.Fatalf("stale sample credited: free %d GiB", got/gib)
	}
	child := processes[701]
	child.StartID = "child-B"
	processes[701] = child
	in.mu.Lock()
	in.samples[0].StartID = "child-A"
	in.mu.Unlock()
	if got := measuredFree(t, book, in); got != 25*gib {
		t.Fatalf("reused PID credited: free %d GiB", got/gib)
	}
}

func TestOneChildCannotBindTwoLeases(t *testing.T) {
	book, _, who, parent, _, first := boundBook(t, 100*gib, 30*gib)
	second := book.Acquire(context.Background(), who, boundPeer(), instrument.Card0, 30*gib, 0)
	if second.Lease == nil {
		t.Fatalf("second lease: %+v", second)
	}
	if got := bindForTest(book, who, parent, first, 701).Outcome; got != wire.ProcessBindOutcomeBound {
		t.Fatalf("first bind %s", got)
	}
	if got := bindForTest(book, who, parent, second.Lease.ID, 701).Outcome; got != wire.ProcessBindOutcomeRefused {
		t.Fatalf("second bind %s", got)
	}
}

// A measured engine below the directly bound host spends the same reservation.
// The fake inspector supplies the kernel generation/parent observations; no
// process, GPU counter or platform power request is started.
func TestBoundDescendantOffsetsReservationWithVerifiedAncestry(t *testing.T) {
	book, in, who, parent, processes, lease := boundBook(t, 100*gib, 60*gib)
	if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeBound {
		t.Fatal(got)
	}
	processes[702] = instrument.ProcessInfo{PID: 702, ParentPID: 701, StartID: "grandchild-A", BootID: "test-boot", StartTicks: 102, Account: bindAccount}
	in.samples = []instrument.Sample{{PID: 702, StartID: "grandchild-A", Program: `C:\engine\worker.exe`, Account: bindAccount, Amount: 50 * gib}}
	if got := measuredFree(t, book, in); got != 40*gib {
		t.Fatalf("free = %d GiB, want 40: descendant measured use must offset the reservation once", got/gib)
	}
}

func TestDescendantCreditRefusesUnprovenEdges(t *testing.T) {
	for _, fault := range []string{"unrelated", "account", "boot", "equal-tick", "older-child", "reused-leaf", "missing-parent", "empty-generation", "cycle", "recheck", "depth"} {
		t.Run(fault, func(t *testing.T) {
			book, in, who, parent, processes, lease := boundBook(t, 100*gib, 60*gib)
			if got := bindForTest(book, who, parent, lease, 701).Outcome; got != wire.ProcessBindOutcomeBound {
				t.Fatal(got)
			}
			processes[702] = instrument.ProcessInfo{PID: 702, ParentPID: 701, StartID: "middle", BootID: "test-boot", StartTicks: 102, Account: bindAccount}
			leaf := instrument.ProcessInfo{PID: 703, ParentPID: 702, StartID: "leaf", BootID: "test-boot", StartTicks: 103, Account: bindAccount}
			processes[703] = leaf
			in.samples = []instrument.Sample{{PID: 703, StartID: "leaf", Program: `C:\engine\worker.exe`, Account: bindAccount, Amount: 50 * gib}}
			middle := processes[702]
			switch fault {
			case "unrelated":
				middle.ParentPID = 0
			case "account":
				middle.Account = "other"
			case "boot":
				middle.BootID = "other"
			case "equal-tick":
				middle.StartTicks = leaf.StartTicks
			case "older-child":
				middle.StartTicks = leaf.StartTicks + 1
			case "reused-leaf":
				leaf.StartID = "new-leaf"
				processes[703] = leaf
			case "missing-parent":
				middle.ParentPID = 999
			case "empty-generation":
				middle.StartID = ""
			case "cycle":
				middle.ParentPID = 703
			case "recheck":
				calls := 0
				original := book.inspect
				book.inspect = func(pid int) (instrument.ProcessInfo, error) {
					p, err := original(pid)
					if pid == 702 {
						calls++
						if calls > 1 {
							p.ParentPID = 999
						}
					}
					return p, err
				}
			case "depth":
				for pid := 703; pid <= 831; pid++ {
					processes[pid] = instrument.ProcessInfo{PID: pid, ParentPID: pid - 1, StartID: "deep-" + string(rune(pid)), BootID: "test-boot", StartTicks: uint64(pid), Account: bindAccount}
				}
				in.samples[0].PID, in.samples[0].StartID = 831, processes[831].StartID
			}
			processes[702] = middle
			if got := measuredFree(t, book, in); got != 0 {
				t.Fatalf("unproven %s credited: free %d GiB, want 0", fault, got/gib)
			}
		})
	}
}

func TestDescendantOverlappingBindingsCreditOneSubjectOnce(t *testing.T) {
	for _, mode := range []string{"same-subject", "conflicting-subject", "exact-direct"} {
		t.Run(mode, func(t *testing.T) {
			book, in, who, parent, processes, first := boundBook(t, 100*gib, 40*gib)
			if got := bindForTest(book, who, parent, first, 701).Outcome; got != wire.ProcessBindOutcomeBound {
				t.Fatal(got)
			}
			other := who
			if mode != "same-subject" {
				other.Program = `C:\other\host.exe`
			}
			second := book.Acquire(context.Background(), other, boundPeer(), instrument.Card0, 20*gib, 0)
			if second.Lease == nil {
				t.Fatal(second)
			}
			processes[702] = instrument.ProcessInfo{PID: 702, ParentPID: 701, StartID: "middle", BootID: "test-boot", StartTicks: 102, Started: processes[701].Started.Add(time.Second), Account: bindAccount}
			if got := bindForTest(book, other, identity.Process{PID: 701, StartTime: processes[701].Started}, second.Lease.ID, 702).Outcome; got != wire.ProcessBindOutcomeBound {
				t.Fatal(got)
			}
			processes[703] = instrument.ProcessInfo{PID: 703, ParentPID: 702, StartID: "leaf", BootID: "test-boot", StartTicks: 103, Account: bindAccount}
			in.samples = []instrument.Sample{{PID: 703, StartID: "leaf", Program: `C:\engine\worker.exe`, Account: bindAccount, Amount: 50 * gib}}
			want := int64(40 * gib)
			if mode == "conflicting-subject" {
				want = 0
			} else if mode == "exact-direct" {
				in.samples[0].PID, in.samples[0].StartID = 702, "middle"
				want = 10 * gib
			}
			if got := measuredFree(t, book, in); got != want {
				t.Fatalf("%s: free %d GiB, want %d", mode, got/gib, want/gib)
			}
		})
	}
}
