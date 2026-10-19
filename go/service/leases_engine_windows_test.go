//go:build windows

package service

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	resourceclient "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// engineRole makes this test binary a stand-in engine process: the venv
// launcher, which starts the interpreter and prints its PID, or the
// interpreter, which runs until its standard input closes.
const (
	engineRole        = "OA_RESOURCE_TEST_ENGINE_ROLE"
	engineInterpreter = "OA_RESOURCE_TEST_ENGINE_INTERPRETER"
)

func TestEngineHelperProcess(t *testing.T) {
	switch os.Getenv(engineRole) {
	case "launcher":
		cmd := exec.Command(os.Getenv(engineInterpreter), "-test.run=^TestEngineHelperProcess$")
		cmd.Env = append(os.Environ(), engineRole+"=interpreter")
		cmd.Stdin = os.Stdin
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		//unchecked: the parent reads this line; a failed write leaves it waiting for its own deadline
		os.Stdout.WriteString(strconv.Itoa(cmd.Process.Pid) + "\n")
		//unchecked: the launcher exits with the interpreter, whatever it returned
		cmd.Wait()
		os.Exit(0)
	case "interpreter":
		//unchecked: end of input, read error or not, is the signal to stop
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

// pythonEngine lays out a PyTorch-engine folder in a temporary folder:
// engine\venv\Scripts\python.exe, the launcher, beside engine\venv\pyvenv.cfg,
// and engine\python\python.exe, the base interpreter, both copies of this
// test binary. outside puts the interpreter in a folder beside engine.
func pythonEngine(t *testing.T, outside bool) (launcher, interpreter string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	launcher = filepath.Join(root, "engine", "venv", "Scripts", "python.exe")
	interpreter = filepath.Join(root, "engine", "python", "python.exe")
	if outside {
		interpreter = filepath.Join(root, "elsewhere", "python.exe")
	}
	for _, path := range []string{launcher, interpreter} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := "home = " + filepath.Dir(interpreter) + "\ninclude-system-site-packages = false\nversion = 3.12.10\n"
	if err := os.WriteFile(filepath.Join(root, "engine", "venv", "pyvenv.cfg"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return launcher, interpreter
}

// startEngine starts the launcher as this process's child and returns its
// PID and the PID of the interpreter it started. Both end at cleanup.
func startEngine(t *testing.T, launcher, interpreter string) (int, int) {
	t.Helper()
	cmd := exec.Command(launcher, "-test.run=^TestEngineHelperProcess$")
	cmd.Env = append(os.Environ(), engineRole+"=launcher", engineInterpreter+"="+interpreter)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	var child int
	select {
	case line := <-lines:
		child, err = strconv.Atoi(line)
		if err != nil {
			t.Fatalf("launcher said %q", line)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the launcher did not start its interpreter")
	}
	t.Cleanup(func() {
		//unchecked: closing input ends both processes; Kill below is the fallback
		stdin.Close()
		if p, err := os.FindProcess(child); err == nil {
			//unchecked: the interpreter may already have ended
			p.Kill()
		}
		//unchecked: the launcher may already have ended
		cmd.Process.Kill()
		//unchecked: the exit status of a stopped stand-in tells nothing
		cmd.Wait()
	})
	return cmd.Process.Pid, child
}

// engineBook serves a lease book on a 100 GiB card through the real
// resource host. The instrument is PDH-like: its rows carry a PID and no
// creation identity. usage stands in for the D3DKMT handle measurement.
func engineBook(t *testing.T, usage func(int, string) (int64, error)) (*Book, *fake, *servedClient) {
	t.Helper()
	in := card(100 * gib)
	table := NewTable(pdhFake{in}, nil, time.Nanosecond)
	book, err := OpenBook(BookOptions{Table: table, Hold: permitGate, Yield: permitYield, Standing: permitStanding,
		Settle: time.Second, ChildUsage: usage})
	if err != nil {
		t.Fatal(err)
	}
	return book, in, &servedClient{servedWithLeases(t, table, book)}
}

type servedClient struct{ leases *resourceclient.Client }

// boundEngine acquires the picture engine's 24 GiB lease over the endpoint,
// starts the engine and binds its launcher, as the PyTorch provider's holder
// does, and shows the interpreter holding 20 GiB on the instrument.
func boundEngine(t *testing.T, c *servedClient, in *fake, launcher, interpreter string) (int, int) {
	t.Helper()
	acquired, err := c.leases.Acquire(instrument.Card0, 24*gib, 0)
	if err != nil || acquired.Outcome != wire.AcquireOutcomeAcquired || acquired.Lease == nil {
		t.Fatalf("picture lease: %+v %v", acquired, err)
	}
	launcherPID, interpreterPID := startEngine(t, launcher, interpreter)
	bound, err := c.leases.BindProcess(acquired.Lease.ID, launcherPID)
	if err != nil || bound.Outcome != wire.ProcessBindOutcomeBound {
		t.Fatalf("binding the launcher: %+v %v", bound, err)
	}
	self, err := instrument.InspectProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	in.mu.Lock()
	in.samples = []instrument.Sample{{PID: interpreterPID, Program: interpreter, Image: "python.exe", Account: self.Account, Amount: 20 * gib}}
	in.mu.Unlock()
	return launcherPID, interpreterPID
}

// The owner's Strix Halo: pytorchd binds the venv launcher it starts, and
// the interpreter the launcher starts holds the picture model. The
// interpreter is the engine's own process, so its memory occupies the
// engine's lease instead of standing beside it as foreign use. Before, the
// card was charged 20 GiB for the interpreter and 24 GiB for the lease.
func TestEngineInterpreterOccupiesItsLauncherLease(t *testing.T) {
	launcher, interpreter := pythonEngine(t, false)
	var interpreterPID int
	book, in, c := engineBook(t, func(pid int, start string) (int64, error) {
		if pid != interpreterPID {
			// The launcher has opened no adapter, and the D3DKMT query
			// fails for it on the owner's machine.
			return 0, errors.New("D3DKMTQueryVideoMemoryInfo failed for an adapter")
		}
		info, err := instrument.InspectProcess(pid)
		if err != nil || info.StartID != start {
			return 0, errors.New("not the interpreter's generation")
		}
		return 20 * gib, nil
	})
	_, interpreterPID = boundEngine(t, c, in, launcher, interpreter)
	// 100 - 20 measured - (24 - 20) unoccupied = 76.
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 76*gib {
		t.Fatalf("free %d GiB, %v; want 76", free/gib, err)
	}
	if got, err := c.leases.AcquireFree(instrument.Card0, 70*gib); err != nil || got.Outcome != wire.AcquireFreeOutcomeAcquired {
		t.Fatalf("a chat model that fits beside the picture engine: %+v %v", got, err)
	}
}

// A binding the handle cannot measure is charged as its lease or its
// observed rows, whichever is larger, never both: the interpreter's PDH row
// stands in for the failed measurement once its PID is rechecked as the
// launcher's child in the engine's folder.
func TestUnmeasuredEngineIsChargedTheLargerOnce(t *testing.T) {
	launcher, interpreter := pythonEngine(t, false)
	book, in, c := engineBook(t, func(int, string) (int64, error) {
		return 0, errors.New("D3DKMTQueryVideoMemoryInfo failed for an adapter")
	})
	boundEngine(t, c, in, launcher, interpreter)
	// max(24 lease, 20 row) = 24 charged: 100 - 24 = 76.
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 76*gib {
		t.Fatalf("free %d GiB, %v; want 76", free/gib, err)
	}
	// Above the lease the row is charged as it stands.
	in.mu.Lock()
	in.samples[0].Amount = 30 * gib
	in.mu.Unlock()
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 70*gib {
		t.Fatalf("free %d GiB, %v; want 70", free/gib, err)
	}
}

// A child of the bound launcher whose image is outside the engine's folder
// is not the engine's: its memory stays beside the lease.
func TestLauncherChildOutsideTheEngineFolderIsForeign(t *testing.T) {
	launcher, interpreter := pythonEngine(t, true)
	book, in, c := engineBook(t, func(int, string) (int64, error) {
		return 0, errors.New("D3DKMTQueryVideoMemoryInfo failed for an adapter")
	})
	boundEngine(t, c, in, launcher, interpreter)
	// 100 - 20 foreign - 24 unoccupied lease = 56.
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 56*gib {
		t.Fatalf("free %d GiB, %v; want 56", free/gib, err)
	}
}

// A child of the bound launcher that runs as another account is not the
// engine's, whatever folder its image is in: its memory stays beside the
// lease, and no lease of this account is credited with it.
func TestLauncherChildOfAnotherAccountIsForeign(t *testing.T) {
	launcher, interpreter := pythonEngine(t, false)
	var interpreterPID int
	in := card(100 * gib)
	table := NewTable(pdhFake{in}, nil, time.Nanosecond)
	book, err := OpenBook(BookOptions{Table: table, Hold: permitGate, Yield: permitYield, Standing: permitStanding,
		Settle: time.Second,
		ChildUsage: func(int, string) (int64, error) {
			return 0, errors.New("D3DKMTQueryVideoMemoryInfo failed for an adapter")
		},
		InspectProcess: func(pid int) (instrument.ProcessInfo, error) {
			info, err := instrument.InspectProcess(pid)
			if err == nil && pid == interpreterPID {
				info.Account = "S-1-5-21-0-0-0-1001"
			}
			return info, err
		}})
	if err != nil {
		t.Fatal(err)
	}
	c := &servedClient{servedWithLeases(t, table, book)}
	_, interpreterPID = boundEngine(t, c, in, launcher, interpreter)
	// 100 - 20 foreign - 24 unoccupied lease = 56.
	if free, err := book.Free(instrument.Card0, true); err != nil || free != 56*gib {
		t.Fatalf("free %d GiB, %v; want 56", free/gib, err)
	}
}

// An engine bound at a volume root has no folder of its own: no child is
// counted as its own process.
func TestAnEngineAtAVolumeRootHasNoFolder(t *testing.T) {
	for _, image := range []string{`C:\engine.exe`, `\nas\share\engine.exe`} {
		if folder := engineFolder(image); folder != "" {
			t.Fatalf("%s: engine folder %q", image, folder)
		}
	}
	if folder := engineFolder(`C:\Engines\Picture\engine.exe`); folder != `C:\Engines\Picture` {
		t.Fatalf("engine folder %q", folder)
	}
}
