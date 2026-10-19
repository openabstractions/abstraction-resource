package instrument

import "time"

// metalInstrument is the Mac's instrument. It reads card:0's capacity from
// the system's own figure for the Metal device, its recommended working-set
// size (MTLDevice.recommendedMaxWorkingSetSize): on the owner's M5 Pro with
// 48 GiB it is 40,200,896,512 bytes, 78% of memory, and it follows
// iogpu.wired_limit_mb when the person raises it. A model larger than it does
// not stay resident on the card, so Fit reads it as the card's pool. Every
// card:<n> stays carved from memory, so card and memory leases count against
// each other (CONTRACT.md RES-L2). It measures no holds, so every row is
// claimed (RES-T2). This file is platform-neutral so every platform's tests
// reach it; detect_darwin.go binds it to Metal.
type metalInstrument struct {
	workingSet func() int64
}

func (metalInstrument) Name() string                  { return NameMetalWorkingSet }
func (metalInstrument) Resources() []string           { return []string{Card0} }
func (metalInstrument) VerifiedProcessSamples() bool  { return false }
func (metalInstrument) PoolOf(resource string) string { return sharedWithMemory(resource) }

// Read reports card:0's working-set size as its capacity and no samples.
// Every other resource reads empty: memory's pool is the machine's memory.
func (m metalInstrument) Read(resource string) (Reading, error) {
	reading := Reading{Resource: resource, At: time.Now()}
	if resource == Card0 {
		if bytes := m.workingSet(); bytes > 0 {
			reading.Capacity = bytes
		}
	}
	return reading, nil
}
