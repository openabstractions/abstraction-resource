//go:build !windows

package gpufault

import "errors"

// PlatformAwake reports whether the machine is demonstrably awake now. Only
// Windows has a reader today; elsewhere no standby record is read either.
func PlatformAwake() (bool, error) {
	return false, errors.New("gpufault: this platform has no display state this package reads")
}

// startDisplayWatch has nothing to register here.
func startDisplayWatch() error { return nil }
