//go:build windows

package service

import (
	"errors"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// PDH exposes a PID for a GPU row, but no creation identity for the process
// that produced that value, so its rows never credit a lease by PID. The
// Windows instrument instead measures a bound child through a handle on
// that exact process (instrument.ChildUsage), which is what lets a lease be
// bound: binding goes on to inspect the caller and the child (RES-L2).
func TestPDHBindingRestsOnHandleMeasurement(t *testing.T) {
	reader := instrument.GPUCounters()
	proof, ok := reader.(instrument.ProcessSampleIdentity)
	if !ok || proof.VerifiedProcessSamples() {
		t.Fatal("PDH claimed process-generation evidence")
	}
	if _, ok := reader.(instrument.ChildMeasured); !ok {
		t.Fatal("the Windows instrument does not measure a bound child")
	}
	inspected := 0
	book, err := OpenBook(BookOptions{Table: NewTable(reader, nil, time.Second),
		InspectProcess: func(int) (instrument.ProcessInfo, error) {
			inspected++
			return instrument.ProcessInfo{}, errors.New("no such process")
		}})
	if err != nil {
		t.Fatal(err)
	}
	got := book.bindProcess(Subject{Program: `C:\oa\modelhost.exe`, Account: bindAccount},
		identity.Process{PID: 700, StartTime: time.Now()}, "lease-1", 701, func() error { return nil })
	if got.Outcome != wire.ProcessBindOutcomeUnverifiable || inspected == 0 {
		t.Fatalf("binding outcome %s after %d inspections", got.Outcome, inspected)
	}
}
