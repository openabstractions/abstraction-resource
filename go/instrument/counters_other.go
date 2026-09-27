//go:build !windows && !linux

package instrument

// Detect is the instrument of this platform. Windows and Linux each have one
// (counters_windows.go, fdinfo_linux.go); everywhere else a machine with no
// instrument reports every holder as claimed rather than inventing a number.
func Detect() Instrument { return None() }
