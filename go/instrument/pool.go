package instrument

// MachineMemory is this machine's total physical memory in bytes, and 0 where
// the platform will not say.
//
// research/resources/MEASUREMENT-2026-09-22.md settled what a shared-memory
// adapter's pool is: "there is no separate video memory pool: the adapter's
// local segment is carved from system RAM". The table reads machine memory as
// memory's capacity, and as a carved card's source where no instrument states
// the card's own GPU-visible share (CONTRACT.md RES-L2).
func MachineMemory() int64 { return machineMemory() }

// SharedCardCapacity is a card's capacity carved from memory bytes of
// physical memory when no instrument states the card's own share: memory less
// the system's reserve, an eighth of memory and at least 4 GiB, so the
// operating system and the person's programs keep room beside a full card.
// Below 8 GiB the card gets half. Zero memory is zero.
func SharedCardCapacity(memory int64) int64 {
	if memory <= 0 {
		return 0
	}
	reserve := max(memory/8, 4<<30)
	if reserve > memory/2 {
		return memory - memory/2
	}
	return memory - reserve
}

// ChildMeasured is an instrument that measures one bound child process's use
// through a handle on that exact process, the generation-bound measurement the
// lease book credits to the child's lease where the instrument's own samples
// carry no process identity (CONTRACT.md RES-L2). startID is the creation
// identity InspectProcess read when the child was bound.
type ChildMeasured interface {
	ChildUsage(pid int, startID string) (int64, error)
}
