package instrument

import "testing"

// round is the GiB rounding these samples are judged at; the instrument itself
// reports bytes and rounds nothing.
func round(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// The pid on the owner's AMD APU carrying one loaded model
// (research/resources/MEASUREMENT-2026-09-22.md, state (b), pid 50932):
// Local Usage 17.286 GiB, Dedicated Usage 0.256 GiB, Shared Usage 17.032
// GiB, Total Committed 17.286 GiB. Total Committed matched Local Usage in
// that measurement, while Non Local Usage was zero; the old
// Local Usage + Dedicated Usage formula would have summed to 17.542 GiB, a
// 1.5% double count of the Dedicated share that is already inside Local
// Usage. residentByPID, reading Total Committed alone, must land on the
// measured 17.286 GiB, not the inflated 17.542.
func TestResidentByPIDAPUSample(t *testing.T) {
	giB := float64(1 << 30)
	mem := []counter{
		{instance: "pid_50932_luid_0x00000000_0x0000CB0F_phys_0", value: int64(17.286 * giB)},
	}
	total := residentByPID(mem)
	got := round(float64(total[50932]) / giB)
	if got != 17.29 {
		t.Fatalf("APU sample: residentByPID gave %.2f GiB, want 17.29 (measured Total Committed, not the old 17.54 double count)", got)
	}
}

// A discrete-card measurement is not on file. These are synthetic Total
// Committed counter instances for one PID. The test checks summation across
// instances; it does not assert a relation to Local, Non Local, Dedicated or
// Shared Usage on discrete hardware.
func TestResidentByPIDDiscreteSample(t *testing.T) {
	giB := float64(1 << 30)
	mem := []counter{
		{instance: "pid_9001_luid_0x00000000_0x00004321_phys_0_eng_0_engtype_3D", value: int64(3.9 * giB)},
		{instance: "pid_9001_luid_0x00000000_0x00004321_phys_0_eng_1_engtype_Copy", value: int64(1.8 * giB)},
	}
	total := residentByPID(mem)
	got := round(float64(total[9001]) / giB)
	if got != 5.7 {
		t.Fatalf("discrete sample: residentByPID gave %.2f GiB, want 5.70 (Total Committed summed across the process's engine instances)", got)
	}
}

// An instance name with no pid_ segment is not a process and contributes to no
// row. The adapter-wide counter sets are named by LUID alone, and a reader
// that folded one of those into a pid would report the machine's total as one
// process's hold.
func TestResidentByPIDIgnoresInstancesWithNoProcess(t *testing.T) {
	total := residentByPID([]counter{
		{instance: "luid_0x00000000_0x0000CB0F_phys_0", value: 42 << 30},
		{instance: "pid_notanumber_luid_0x0", value: 7 << 30},
	})
	if len(total) != 0 {
		t.Fatalf("residentByPID attributed %v to a process; neither instance names one", total)
	}
}

// The none instrument answers every resource with an empty reading, which is
// what makes every row of a machine with no instrument claimed.
func TestNoneMeasuresNothing(t *testing.T) {
	n := None()
	if n.Name() != NameNone {
		t.Fatalf("instrument name %q", n.Name())
	}
	if len(n.Resources()) != 0 {
		t.Fatalf("the none instrument offers resources: %v", n.Resources())
	}
	r, err := n.Read(Card0)
	if err != nil || len(r.Samples) != 0 || r.Resource != Card0 {
		t.Fatalf("none.Read(%q) = %+v, %v", Card0, r, err)
	}
}
