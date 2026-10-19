package instrument

import "testing"

// An instrument that reports no adapter capacity declares every card carved
// from memory, so a card lease and a memory lease are admitted against one
// pool (CONTRACT.md RES-L2). The none instrument (a Mac without a Metal
// device, or a build without cgo) and the Mac instrument declare the
// same, so a machine with nothing measuring admits against its memory.
func TestPoolDeclarations(t *testing.T) {
	for _, in := range []Instrument{Detect(), None()} {
		if got := PoolOf(in, Card0); got != Memory {
			t.Fatalf("%s: card:0 spends %q, want %q", in.Name(), got, Memory)
		}
		if got := PoolOf(in, "card:1"); got != Memory {
			t.Fatalf("%s: card:1 spends %q", in.Name(), got)
		}
		if got := PoolOf(in, Memory); got != Memory {
			t.Fatalf("%s: memory spends %q", in.Name(), got)
		}
	}
}

func TestFixedReportsWhatWasSet(t *testing.T) {
	f := NewFixed()
	f.Set(Card0, FixedResource{Capacity: 24 << 30, Samples: []Sample{{PID: 1, Program: "p", Amount: 1 << 30}}})
	f.Set(Memory, FixedResource{Capacity: 64 << 30})
	reading, err := f.Read(Card0)
	if err != nil || reading.Capacity != 24<<30 || len(reading.Samples) != 1 {
		t.Fatalf("reading %+v %v", reading, err)
	}
	if got := f.Resources(); len(got) != 2 || got[0] != Card0 || got[1] != Memory {
		t.Fatalf("resources %v", got)
	}
	if PoolOf(f, Card0) != Card0 {
		t.Fatal("a card set without a pool spends another pool")
	}
}
