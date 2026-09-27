//go:build !windows && !linux

package instrument

import "errors"

// InspectProcess refuses attribution where no process relation inspector is
// available. The lease remains conservatively reserved on these platforms.
func InspectProcess(int) (ProcessInfo, error) {
	return ProcessInfo{}, errors.New("process relation inspection unsupported")
}
