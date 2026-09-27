package instrument

import (
	"syscall"
	"unsafe"
)

var globalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// memoryStatusEx is MEMORYSTATUSEX. Only ullTotalPhys is read here; the rest
// of the record is named so the structure's size and layout are the ones the
// call expects.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// machineMemory reads the physical memory the OS reports installed.
func machineMemory() int64 {
	var status memoryStatusEx
	status.length = uint32(unsafe.Sizeof(status))
	//unchecked: the BOOL primary return is checked below; Call's raw GetLastError third field carries no separate information here
	ok, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 || status.totalPhys == 0 || status.totalPhys > 1<<62 {
		return 0
	}
	return int64(status.totalPhys)
}
