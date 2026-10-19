package gpufault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func holdQuarantineWithoutDeleteShare(t *testing.T, path string) func() {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closeReader := func() {
		if handle != windows.InvalidHandle {
			if err := windows.CloseHandle(handle); err != nil {
				t.Fatal(err)
			}
			handle = windows.InvalidHandle
		}
	}
	t.Cleanup(closeReader)
	return closeReader
}

func TestQuarantineRenameAfterReaderRelease(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gpu-quarantine.json")
	staged := filepath.Join(dir, "staged")
	for name, data := range map[string]string{target: "prior", staged: "replacement"} {
		if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	release := holdQuarantineWithoutDeleteShare(t, target)
	attempts := 0
	err := retryQuarantineRename(func() error {
		attempts++
		err := os.Rename(staged, target)
		if attempts <= 3 {
			if !quarantineRenameDenied(err) {
				t.Fatalf("held destination attempt %d: %v", attempts, err)
			}
			if attempts == 3 {
				release()
			}
		}
		return err
	})
	if err != nil || attempts < 4 || attempts > 5 {
		t.Fatalf("replacement after reader release: %d attempts, %v", attempts, err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "replacement" {
		t.Fatalf("replacement readback: %q, %v", got, err)
	}
	if leaves, err := os.ReadDir(dir); err != nil || len(leaves) != 1 || leaves[0].Name() != "gpu-quarantine.json" {
		t.Fatalf("replacement left temporary state: %v, %v", leaves, err)
	}
}

func TestQuarantineSaveReportsPersistentSharingDenial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gpu-quarantine.json")
	var reports []error
	g, err := New(Options{State: path, OnError: func(err error) { reports = append(reports, err) }})
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint{Model: "fixture", Engine: "fixture", Shape: "1"}
	g.Failed(fp)
	prior, err := os.ReadFile(path)
	if err != nil || len(reports) != 0 {
		t.Fatalf("seed did not persist: %v, %v", err, reports)
	}
	holdQuarantineWithoutDeleteShare(t, path)
	if got := g.Failed(fp); got.Strikes != 2 {
		t.Fatalf("in-memory quarantine changed semantics: %+v", got)
	}
	if len(reports) != 1 || !quarantineRenameDenied(reports[0]) {
		t.Fatalf("expected a single reported refusal: %v", reports)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, prior) {
		t.Fatalf("denied replacement changed durable state: %q, %v", got, err)
	}
	if leaves, err := os.ReadDir(dir); err != nil || len(leaves) != 1 || leaves[0].Name() != "gpu-quarantine.json" {
		t.Fatalf("denied replacement left temporary state: %v, %v", leaves, err)
	}
}
