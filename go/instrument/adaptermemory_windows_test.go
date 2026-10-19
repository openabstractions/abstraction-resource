package instrument

import (
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"
)

// A Windows machine with a hardware graphics adapter reports card:0's
// capacity from DXGI: never unknown, never above what the machine has plus
// a dedicated carve-out (CONTRACT.md RES-T2). Read-only: no GPU work.
func TestWindowsCardCapacityReported(t *testing.T) {
	adapter, err := AdapterCapacity()
	if err == errAdapterUnavailable {
		t.Skip("no hardware graphics adapter on this machine")
	}
	if err != nil {
		t.Fatalf("AdapterCapacity: %v", err)
	}
	reading, err := GPUCounters().Read(Card0)
	if err != nil {
		t.Skipf("GPU counters unreadable here: %v", err)
	}
	if reading.Capacity != adapter || reading.Capacity <= 0 {
		t.Fatalf("card:0 capacity = %d, adapter figure %d", reading.Capacity, adapter)
	}
	t.Logf("card:0 capacity %.1f GiB, machine memory %.1f GiB", float64(reading.Capacity)/(1<<30), float64(MachineMemory())/(1<<30))
}

// The DXGI description sums the GPU-visible figures of a hardware adapter
// and leaves a software adapter out.
func TestAdapterVisible(t *testing.T) {
	apu := dxgiAdapterDesc1{DedicatedVideoMemory: 4 << 30, SharedSystemMemory: 61 << 30}
	if got := adapterVisible(apu); got != 65<<30 {
		t.Fatalf("APU visible = %d", got)
	}
	software := dxgiAdapterDesc1{SharedSystemMemory: 61 << 30, Flags: dxgiAdapterSoftware}
	if got := adapterVisible(software); got != 0 {
		t.Fatalf("software adapter visible = %d", got)
	}
}

// ChildUsage measures a live process through its own handle and refuses a
// creation identity that is not that process's.
func TestChildUsageIdentity(t *testing.T) {
	self, err := InspectProcess(os.Getpid())
	if err != nil {
		t.Fatalf("InspectProcess: %v", err)
	}
	used, err := ChildUsage(self.PID, self.StartID)
	if err == errAdapterUnavailable {
		t.Skip("no graphics adapter enumerated")
	}
	if err != nil {
		t.Fatalf("ChildUsage(self): %v", err)
	}
	if used < 0 {
		t.Fatalf("ChildUsage(self) = %d", used)
	}
	start, _ := strconv.ParseInt(self.StartID, 10, 64)
	if _, err := ChildUsage(self.PID, strconv.FormatInt(start+1, 10)); err == nil {
		t.Fatal("a different creation identity was measured")
	}
	if _, err := ChildUsage(int(windows.GetCurrentProcessId()), ""); err == nil {
		t.Fatal("an empty creation identity was measured")
	}
}
