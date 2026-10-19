package service

import (
	"errors"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// unreadableCard is an instrument that lists a card it cannot read, as a
// basic display adapter or a virtual machine's leaves it, and declares no
// pool for it.
type unreadableCard struct{}

func (unreadableCard) Name() string        { return "unreadable" }
func (unreadableCard) Resources() []string { return []string{instrument.Card0} }
func (unreadableCard) Read(string) (instrument.Reading, error) {
	return instrument.Reading{}, errors.New("no GPU counters")
}

// Table.Pool is the lease book's one pool rule (RES-L2), shared with other
// readers such as the model catalogue's fit rule.
func TestTablePoolRule(t *testing.T) {
	memory := instrument.MachineMemory()

	measured := instrument.NewFixed()
	measured.Set(instrument.Card0, instrument.FixedResource{Capacity: 24 << 30})
	if got := NewTable(measured, nil, time.Minute).Pool(instrument.Card0); got != 24<<30 {
		t.Fatalf("a measured card's pool = %d", got)
	}

	carved := instrument.NewFixed()
	carved.Set(instrument.Card0, instrument.FixedResource{Pool: instrument.Memory})
	carved.Set(instrument.Memory, instrument.FixedResource{Capacity: 96 << 30})
	if got := NewTable(carved, nil, time.Minute).Pool(instrument.Card0); got != 96<<30 {
		t.Fatalf("a card carved from measured memory = %d", got)
	}

	// An instrument that states no card capacity: the card is carved from
	// memory, memory is the machine's physical memory, and the card is that
	// memory less the system's reserve. memory itself is the whole.
	card := instrument.SharedCardCapacity(memory)
	shared := instrument.NewFixed()
	shared.Set(instrument.Card0, instrument.FixedResource{Pool: instrument.Memory})
	if got := NewTable(shared, nil, time.Minute).Pool(instrument.Card0); got != card {
		t.Fatalf("a shared card's pool = %d, want %d of machine memory %d", got, card, memory)
	}
	if got := NewTable(shared, nil, time.Minute).Pool(instrument.Memory); got != memory {
		t.Fatalf("memory's pool = %d, machine memory %d", got, memory)
	}

	// A platform with no instrument (a Mac with no Metal device) reads a card as carved
	// from memory too.
	if got := NewTable(instrument.None(), nil, time.Minute).Pool(instrument.Card0); got != card {
		t.Fatalf("no instrument's card pool = %d, want %d", got, card)
	}

	if got := NewTable(unreadableCard{}, nil, time.Minute).Pool(instrument.Card0); got != card {
		t.Fatalf("an unreadable card's pool = %d, want %d", got, card)
	}

	own := instrument.NewFixed()
	own.Set(instrument.Card0, instrument.FixedResource{})
	if got := NewTable(own, nil, time.Minute).Pool(instrument.Card0); got != 0 {
		t.Fatalf("a readable card that is its own pool with no capacity = %d", got)
	}
}

// A unified-memory machine reports card:0's capacity in the table every
// client reads, never unknown, and the figure is the pool the lease book
// admits against (RES-L2). Holders over the wire and Pool in process agree.
func TestUnifiedMemoryCapacityReported(t *testing.T) {
	memory := instrument.MachineMemory()
	if memory <= 0 {
		t.Skip("this platform states no physical memory")
	}
	shared := instrument.NewFixed()
	shared.Set(instrument.Card0, instrument.FixedResource{Pool: instrument.Memory})
	table := NewTable(shared, nil, time.Minute)
	state, err := table.Holders(instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Capacity <= 0 || state.Capacity >= memory || state.Capacity != table.Pool(instrument.Card0) {
		t.Fatalf("card:0 capacity %d, pool %d, memory %d", state.Capacity, table.Pool(instrument.Card0), memory)
	}
	// An instrument with the adapter's own figure (DXGI on Windows, Metal
	// on a Mac) is reported as it stands.
	adapter := instrument.NewFixed()
	adapter.Set(instrument.Card0, instrument.FixedResource{Pool: instrument.Memory, Capacity: 102 << 30})
	if state, err := NewTable(adapter, nil, time.Minute).Holders(instrument.Card0, false); err != nil || state.Capacity != 102<<30 {
		t.Fatalf("adapter capacity %d, %v", state.Capacity, err)
	}
	for _, tc := range []struct{ memory, card int64 }{{0, 0}, {6 << 30, 3 << 30}, {16 << 30, 12 << 30}, {128 << 30, 112 << 30}} {
		if got := instrument.SharedCardCapacity(tc.memory); got != tc.card {
			t.Fatalf("SharedCardCapacity(%d) = %d, want %d", tc.memory, got, tc.card)
		}
	}
}
