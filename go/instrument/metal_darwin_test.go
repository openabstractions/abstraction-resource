//go:build darwin && cgo

package instrument

import "testing"

// On a Mac with a Metal device, Detect reads the system's own working-set
// figure as card:0's capacity: above zero and no more than the machine's
// memory. The owner's M5 Pro with 48 GiB reads 40,200,896,512 bytes.
func TestDetectReadsTheMetalWorkingSet(t *testing.T) {
	in := Detect()
	if _, ok := in.(metalInstrument); !ok {
		t.Skip("this Mac lists no Metal device")
	}
	reading, err := in.Read(Card0)
	if err != nil {
		t.Fatal(err)
	}
	memory := MachineMemory()
	if reading.Capacity <= 0 || reading.Capacity > memory {
		t.Fatalf("card:0 capacity %d with %d bytes of memory", reading.Capacity, memory)
	}
	if got := PoolOf(in, Card0); got != Memory {
		t.Fatalf("card:0 spends %q", got)
	}
	t.Logf("card:0 capacity %d bytes (%.1f%% of %d bytes of memory)", reading.Capacity,
		100*float64(reading.Capacity)/float64(memory), memory)
}
