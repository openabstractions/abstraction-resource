package instrument

// MachineMemory is this machine's total physical memory in bytes, and 0 where
// the platform will not say.
//
// It exists for the lease service, never for the table: ResourceState.capacity
// stays what the instrument measured, which is 0 on a shared-memory adapter
// (CONTRACT.md RES-T2). A lease still has to know what the pool is, and
// research/resources/MEASUREMENT-2026-09-22.md settled what the pool is on
// such a machine: "there is no separate video memory pool: the adapter's local
// segment is carved from system RAM". Machine memory is that segment's pool,
// read from the platform rather than configured by anyone.
func MachineMemory() int64 { return machineMemory() }
