package gpufault

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// A read handle without delete sharing can briefly block replacing the quarantine.
func retryQuarantineRename(rename func() error) error {
	waits := [...]time.Duration{25 * time.Millisecond, 75 * time.Millisecond, 150 * time.Millisecond, 250 * time.Millisecond}
	for attempt := 0; ; attempt++ {
		err := rename()
		if attempt == len(waits) || !quarantineRenameDenied(err) {
			return err
		}
		time.Sleep(waits[attempt])
	}
}

func quarantineRenameDenied(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
