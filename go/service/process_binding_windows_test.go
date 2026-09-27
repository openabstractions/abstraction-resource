//go:build windows

package service

import (
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// PDH exposes a PID for a GPU row, but no creation identity for the process
// that produced that value. The service must keep both the measured row and
// the full unoccupied lease reservation instead of crediting by PID alone.
func TestPDHProcessSampleCannotBindLease(t *testing.T) {
	reader := instrument.GPUCounters()
	proof, ok := reader.(instrument.ProcessSampleIdentity)
	if !ok || proof.VerifiedProcessSamples() {
		t.Fatal("PDH claimed process-generation evidence")
	}
	book, err := OpenBook(BookOptions{Table: NewTable(reader, nil, time.Second),
		InspectProcess: func(int) (instrument.ProcessInfo, error) {
			t.Fatal("unsupported instrument must refuse before process inspection")
			return instrument.ProcessInfo{}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	got := book.bindProcess(Subject{Program: `C:\oa\modelhost.exe`, Account: bindAccount},
		identity.Process{PID: 700, StartTime: time.Now()}, "lease-1", 701, func() error { return nil })
	if got.Outcome != wire.ProcessBindOutcomeUnverifiable {
		t.Fatalf("PDH binding outcome %s", got.Outcome)
	}
}
