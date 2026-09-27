//go:build linux

package service

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// The test binary supplies a live direct child without depending on a GPU or
// an installed helper program. Closing stdin lets the parent reap it.
func TestBindProcessChildHelper(t *testing.T) {
	if os.Getenv("OA_RESOURCE_BIND_CHILD_HELPER") != "1" {
		return
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func bindProcessTestChild(t *testing.T) (int, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBindProcessChildHelper$")
	cmd.Env = append(os.Environ(), "OA_RESOURCE_BIND_CHILD_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	waited := false
	stop := func() {
		_ = stdin.Close()
		if !waited {
			if err := cmd.Wait(); err != nil {
				t.Errorf("child exited: %v", err)
			}
			waited = true
		}
		cancel()
	}
	t.Cleanup(stop)
	return cmd.Process.Pid, stop
}

// The lease holder calls through the real local endpoint. The service binds
// that call to this test process and inspects the child's kernel identity.
func TestBindProcessRealLinuxChildOverIPC(t *testing.T) {
	fdinfo := instrument.Fdinfo()
	if !fdinfo.(instrument.ProcessSampleIdentity).VerifiedProcessSamples() {
		t.Skip("kernel cannot pin process identities with pidfd")
	}
	in := &fake{name: instrument.NameLinuxFdinfo, offers: []string{instrument.Card0},
		at: time.Now(), cap: 8 * gib}
	table := NewTable(in, nil, time.Nanosecond)
	book, err := OpenBook(BookOptions{Table: table})
	if err != nil {
		t.Fatal(err)
	}
	table.claims = book.Claims()
	client := servedWithLeases(t, table, book)
	grant, err := client.Acquire(instrument.Card0, gib, 0)
	if err != nil || grant.Outcome != wire.AcquireOutcomeAcquired || grant.Lease == nil {
		t.Fatalf("lease for bound IPC caller: %+v, %v", grant, err)
	}
	lease := grant.Lease.ID

	// The caller's own parent is live and inspectable, but is not its child.
	unrelated := os.Getppid()
	info, err := instrument.InspectProcess(unrelated)
	if err != nil || info.ParentPID == os.Getpid() {
		t.Fatalf("unrelated process cannot be inspected as nonchild: %+v, %v", info, err)
	}
	got, err := client.BindProcess(lease, unrelated)
	if err != nil || got.Outcome != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("unrelated process binding: %+v, %v", got, err)
	}

	exitedPID, stopExited := bindProcessTestChild(t)
	stopExited()
	if _, err := instrument.InspectProcess(exitedPID); err == nil {
		t.Fatal("reaped child still inspectable")
	}
	got, err = client.BindProcess(lease, exitedPID)
	if err != nil || got.Outcome != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("exited child binding: %+v, %v", got, err)
	}

	childPID, _ := bindProcessTestChild(t)
	child, err := instrument.InspectProcess(childPID)
	if err != nil || child.ParentPID != os.Getpid() || child.StartID == "" {
		t.Fatalf("live direct child identity: %+v, %v", child, err)
	}
	got, err = client.BindProcess(lease, childPID)
	if err != nil || got.Outcome != wire.ProcessBindOutcomeBound {
		t.Fatalf("direct child binding: %+v, %v", got, err)
	}
}
