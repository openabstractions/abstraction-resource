//go:build !windows

package gpufault

func retryQuarantineRename(rename func() error) error { return rename() }
