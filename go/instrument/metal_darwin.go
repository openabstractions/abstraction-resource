//go:build darwin && cgo

package instrument

// The in-house Metal binding (metal_native_darwin.m). macOS darwin binaries
// are built with cgo, as the identity module's Security and XPC owners
// require; a build without it reads no working set (metal_nocgo_darwin.go).

/*
#cgo CFLAGS: -fno-objc-arc
#cgo LDFLAGS: -framework Foundation -framework Metal
long long oa_metal_working_set(void);
*/
import "C"

// metalWorkingSet is the Metal device's recommended working-set size in
// bytes, and 0 when the system lists no Metal device.
func metalWorkingSet() int64 { return int64(C.oa_metal_working_set()) }
