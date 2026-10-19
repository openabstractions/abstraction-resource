//go:build darwin

package instrument

import "golang.org/x/sys/unix"

// machineMemory reads hw.memsize, the kernel's figure for installed memory.
func machineMemory() int64 {
	bytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil || bytes > 1<<62 {
		return 0
	}
	return int64(bytes)
}
