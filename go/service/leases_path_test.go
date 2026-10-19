package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/placetest"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

func TestEngineChildCreditFollowsObservedImageIdentity(t *testing.T) {
	root := t.TempDir()
	engine, external := filepath.Join(root, "engine"), filepath.Join(root, "external")
	launcher, interpreter := filepath.Join(engine, "engine.exe"), filepath.Join(engine, "interpreter.exe")
	for _, path := range []string{launcher, interpreter, filepath.Join(external, "unrelated.exe")} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("observed image"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(alias, target string) {
		t.Helper()
		var err error
		if runtime.GOOS == "windows" {
			err = placetest.Junction(alias, target)
		} else {
			err = os.Symlink(target, alias)
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Remove(alias); err != nil {
				t.Error(err)
			}
		})
	}
	aliasIntoEngine, aliasOutOfEngine := filepath.Join(root, "engine-alias"), filepath.Join(engine, "external-alias")
	link(aliasIntoEngine, engine)
	link(aliasOutOfEngine, external)
	now := time.Now()
	processes := map[int]instrument.ProcessInfo{
		1: {PID: 1, StartID: "launcher", Account: "owner", Started: now, Image: launcher},
		2: {PID: 2, ParentPID: 1, StartID: "inside", Account: "owner", Started: now.Add(time.Second),
			Image: filepath.Join(aliasIntoEngine, filepath.Base(interpreter))},
		3: {PID: 3, ParentPID: 1, StartID: "outside", Account: "owner", Started: now.Add(time.Second),
			Image: filepath.Join(aliasOutOfEngine, "unrelated.exe")},
	}
	book := &Book{inspect: func(pid int) (instrument.ProcessInfo, error) { return processes[pid], nil }}
	lease := &record{ProcessPID: 1, ProcessStart: "launcher", Account: "owner"}
	got := book.engineProcesses(lease, []instrument.Sample{{PID: 2}, {PID: 3}})
	if len(got) != 2 || got[0].PID != 1 || got[1].PID != 2 || !got[1].confirmed {
		t.Fatalf("which child consumed the engine lease: %+v", got)
	}
}

func TestEngineChildCreditRequiresHeldVirtualenvMarker(t *testing.T) {
	for _, row := range []struct {
		name, marker, launcherDir string
		wantCredit, wantMarker    bool
	}{
		{name: "plain marker with Scripts", marker: "plain", launcherDir: "Scripts", wantCredit: true, wantMarker: true},
		{name: "plain marker with bin", marker: "plain", launcherDir: "bin", wantCredit: true, wantMarker: true},
		{name: "unrelated folder", marker: "plain", launcherDir: "worker"},
		{name: "linked marker with Scripts", marker: "link", launcherDir: "Scripts", wantMarker: true},
		{name: "linked marker with bin", marker: "link", launcherDir: "bin", wantMarker: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := t.TempDir()
			venv := filepath.Join(root, "engine", "venv")
			launcher := filepath.Join(venv, row.launcherDir, "launcher.exe")
			interpreter := filepath.Join(root, "engine", "python", "python.exe")
			for _, image := range []string{launcher, interpreter} {
				if err := os.MkdirAll(filepath.Dir(image), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(image, []byte("observed image"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(venv, "pyvenv.cfg")
			switch {
			case row.marker == "plain":
				if err := os.WriteFile(marker, []byte("home = python"), 0600); err != nil {
					t.Fatal(err)
				}
			case runtime.GOOS == "windows":
				if err := placetest.ShareJunction(marker); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Symlink(interpreter, marker); err != nil {
					t.Fatal(err)
				}
			}
			if row.marker == "link" {
				t.Cleanup(func() {
					if err := os.Remove(marker); err != nil {
						t.Error(err)
					}
				})
			}
			var openedMarker, openedRemote bool
			stop, ok := fileid.ObserveOpens(func(path string) {
				openedMarker = openedMarker || filepath.Base(path) == "pyvenv.cfg"
				openedRemote = openedRemote || strings.Contains(path, placetest.TestNetHost)
			})
			if !ok {
				t.Fatal("Identity open observer already in use")
			}
			defer stop()
			now := time.Now()
			processes := map[int]instrument.ProcessInfo{
				1: {PID: 1, StartID: "launcher", Account: "owner", Started: now, Image: launcher},
				2: {PID: 2, ParentPID: 1, StartID: "child", Account: "owner", Started: now.Add(time.Second), Image: interpreter},
			}
			book := &Book{inspect: func(pid int) (instrument.ProcessInfo, error) { return processes[pid], nil }}
			lease := &record{ProcessPID: 1, ProcessStart: "launcher", Account: "owner"}
			got := book.engineProcesses(lease, []instrument.Sample{{PID: 2}})
			if (len(got) == 2) != row.wantCredit || openedMarker != row.wantMarker || openedRemote {
				t.Fatalf("credit=%+v, marker opened=%v, remote opened=%v", got, openedMarker, openedRemote)
			}
		})
	}
}
