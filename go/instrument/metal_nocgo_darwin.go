//go:build darwin && !cgo

package instrument

// metalWorkingSet reads nothing in a build without cgo: the Metal binding is
// Objective-C. Detect then answers None, and the card's pool is the machine's
// memory, as before the binding existed.
func metalWorkingSet() int64 { return 0 }
