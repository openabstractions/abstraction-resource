//go:build !windows

package gpufault

import "context"

// PlatformRecords is this platform's records. Only Windows has a source
// today; elsewhere Watch reports that it reads nothing, and the Guard is
// blind rather than quiet (RES-G1).
func PlatformRecords() Records { return noSource{} }

type noSource struct{}

func (noSource) Watch(context.Context, func(Event)) error { return errNoSource }
