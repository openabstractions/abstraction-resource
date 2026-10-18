package gpufault

import (
	"context"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// RES-G1: the platform source subscribes without elevation, says so with
// Watching, and then delivers the standby state at start when the System log
// holds one since the boot.
func TestPlatformRecordsReadWithoutElevation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var events []Event
	if err := PlatformRecords().Watch(ctx, func(ev Event) { events = append(events, ev) }); err != nil {
		t.Fatalf("the platform's records could not be read: %v", err)
	}
	t.Logf("delivered %+v", events)
	if len(events) == 0 || events[0].Kind != Watching {
		t.Fatalf("the subscription did not say it stands: %+v", events)
	}
	if len(events) > 1 && events[1].Kind != StandbyEnter && events[1].Kind != StandbyExit {
		t.Fatalf("the second record is %+v, not the last standby change", events[1])
	}
}

// RES-G4: the default probe reads the adapter list the counters give.
func TestAdapterProbeReadsTheCounters(t *testing.T) {
	if _, err := instrument.GraphicsAdapters(); err != nil {
		t.Skipf("this machine's counters list no adapter: %v", err)
	}
	if err := AdapterProbe(context.Background()); err != nil {
		t.Fatal(err)
	}
}
