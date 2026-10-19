package instrument

import "testing"

// The Mac instrument reports the Metal working-set size as card:0's capacity,
// keeps every card carved from memory and measures no holds (RES-L2, RES-T2).
func TestMetalInstrumentReportsTheWorkingSetAsTheCardCapacity(t *testing.T) {
	const workingSet = int64(40_200_896_512)
	in := metalInstrument{workingSet: func() int64 { return workingSet }}
	if in.Name() != NameMetalWorkingSet {
		t.Fatalf("name %q, want %q", in.Name(), NameMetalWorkingSet)
	}
	if got := in.Resources(); len(got) != 1 || got[0] != Card0 {
		t.Fatalf("resources %v", got)
	}
	card, err := in.Read(Card0)
	if err != nil || card.Capacity != workingSet || len(card.Samples) != 0 || card.At.IsZero() {
		t.Fatalf("card:0 reading %+v %v", card, err)
	}
	memory, err := in.Read(Memory)
	if err != nil || memory.Capacity != 0 || len(memory.Samples) != 0 {
		t.Fatalf("memory reading %+v %v", memory, err)
	}
	for _, resource := range []string{Card0, "card:1", Memory} {
		if got := PoolOf(in, resource); got != Memory {
			t.Fatalf("%s spends %q, want %q", resource, got, Memory)
		}
	}
	gone := metalInstrument{workingSet: func() int64 { return 0 }}
	if reading, _ := gone.Read(Card0); reading.Capacity != 0 {
		t.Fatalf("a device that stopped answering reported %d", reading.Capacity)
	}
}
