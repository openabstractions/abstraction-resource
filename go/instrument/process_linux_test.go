//go:build linux

package instrument

import (
	"os"
	"strconv"
	"testing"
)

// This uses the test process itself. It exercises the kernel pidfd, fdinfo,
// /proc start-tick and boot-ID path without a GPU or a separate runtime.
func TestInspectProcessSelfHasPinnedCreationIdentity(t *testing.T) {
	if !Fdinfo().(ProcessSampleIdentity).VerifiedProcessSamples() {
		t.Skip("this kernel cannot pin a process with pidfd")
	}
	got, err := InspectProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != os.Getpid() || got.ParentPID <= 0 || got.Account == "" ||
		got.BootID == "" || got.StartTicks == 0 ||
		got.StartID != got.BootID+"/"+strconv.FormatUint(got.StartTicks, 10) {
		t.Fatalf("incomplete pinned process identity: %+v", got)
	}
}
