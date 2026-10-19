package instrument

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeTwoAdapters replaces the PDH reader and the card:0 read with a machine
// holding two adapters: card:0, the larger, at LUID 0x0000CB0F, and a second
// adapter at LUID 0x00004321. The PIDs are far above any this machine issues,
// so no live process lends them a program.
func fakeTwoAdapters(t *testing.T, adapter string) {
	t.Helper()
	const giB = int64(1) << 30
	rows := map[string][]counter{
		`\GPU Process Memory(*)\Total Committed`: {
			{instance: "pid_2147483640_luid_0x00000000_0x0000CB0F_phys_0", value: 12 * giB},
			{instance: "pid_2147483640_luid_0x00000000_0x00004321_phys_0", value: 6 * giB},
			{instance: "pid_2147483644_luid_0x00000000_0x00004321_phys_0", value: 8 * giB},
		},
		`\Process(*)\ID Process`: {
			{instance: "engine-a", value: 2147483640},
			{instance: "engine-b", value: 2147483644},
		},
	}
	savedRead, savedCard := readCounters, card0
	t.Cleanup(func() { readCounters, card0 = savedRead, savedCard })
	readCounters = func(paths ...string) ([]counter, error) {
		var out []counter
		for _, p := range paths {
			out = append(out, rows[p]...)
		}
		return out, nil
	}
	card0 = func() (int64, string) {
		if adapter == "" {
			return 0, ""
		}
		return 16 * giB, adapter
	}
}

// Ruling 7 (0.8.1): card:0's usage is read from the rows of the adapter its
// capacity describes. A process's hold on the second adapter, and a process
// holding only the second adapter, are left out of card:0.
func TestCard0UsageIsReadOnCard0sAdapter(t *testing.T) {
	const giB = int64(1) << 30
	luid := windows.LUID{HighPart: 0, LowPart: 0x0000CB0F}
	fakeTwoAdapters(t, adapterInstance(luid.HighPart, luid.LowPart))
	reading, err := GPUCounters().Read(Card0)
	if err != nil {
		t.Fatal(err)
	}
	if reading.Capacity != 16*giB {
		t.Fatalf("card:0 capacity %d, want the adapter's 16 GiB", reading.Capacity)
	}
	if len(reading.Samples) != 1 {
		t.Fatalf("card:0 lists %d holders, want the one on its adapter: %+v", len(reading.Samples), reading.Samples)
	}
	if s := reading.Samples[0]; s.PID != 2147483640 || s.Amount != 12*giB || s.Image != "engine-a" {
		t.Fatalf("card:0 holder %+v, want engine-a with its 12 GiB on card:0's adapter", s)
	}
}

// An adapter DXGI did not answer leaves the reading as it was before the
// filter: every adapter's rows, and no capacity.
func TestCard0UsageWithAnUnreadAdapterKeepsEveryRow(t *testing.T) {
	const giB = int64(1) << 30
	fakeTwoAdapters(t, "")
	reading, err := GPUCounters().Read(Card0)
	if err != nil {
		t.Fatal(err)
	}
	if reading.Capacity != 0 || len(reading.Samples) != 2 || reading.Samples[0].Amount != 18*giB || reading.Samples[1].Amount != 8*giB {
		t.Fatalf("unread adapter: %+v", reading)
	}
}

// card:0 is the largest hardware adapter, and its LUID comes with it; a
// software adapter is never card:0.
func TestLargestAdapterNamesItsLUID(t *testing.T) {
	descs := []dxgiAdapterDesc1{
		{DedicatedVideoMemory: 8 << 30, AdapterLuid: windows.LUID{LowPart: 0x4321}},
		{DedicatedVideoMemory: 4 << 30, SharedSystemMemory: 61 << 30, AdapterLuid: windows.LUID{LowPart: 0xCB0F}},
		{SharedSystemMemory: 120 << 30, Flags: dxgiAdapterSoftware, AdapterLuid: windows.LUID{LowPart: 0x1}},
	}
	bytes, luid, err := largestAdapter(descs)
	if err != nil || bytes != 65<<30 || luid.LowPart != 0xCB0F {
		t.Fatalf("largestAdapter = %d, %+v, %v", bytes, luid, err)
	}
	if _, _, err := largestAdapter(descs[2:]); !errors.Is(err, errAdapterUnavailable) {
		t.Fatalf("a software adapter alone answered %v", err)
	}
}
