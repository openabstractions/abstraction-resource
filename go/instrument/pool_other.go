//go:build !windows && !linux

package instrument

// machineMemory has no portable reading here; the lease service then works
// from the instrument's own capacity and asks holders when it has none.
func machineMemory() int64 { return 0 }
