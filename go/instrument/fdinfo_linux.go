//go:build linux

package instrument

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Detect is the instrument of this platform: /proc/<pid>/fdinfo, the DRM
// client accounting CONTRACT.md RES-T2 names (research/resources/PROPOSAL.md
// §3, https://docs.kernel.org/gpu/drm-usage-stats.html). It walks the
// processes this account's own /proc lets it read fdinfo for; a machine
// running as a different, unprivileged account than the one holding the card
// sees fewer rows than root would, the same shape of gap the Windows
// instrument has for a process it cannot open a handle to.
func Detect() Instrument { return Fdinfo() }

// Fdinfo is the Linux fdinfo instrument, named so a caller can ask for it
// explicitly rather than through Detect.
func Fdinfo() Instrument { return fdinfoInstrument{pr: realProc{root: "/proc"}} }

type fdinfoInstrument struct{ pr procReader }

func (fdinfoInstrument) Name() string          { return NameLinuxFdinfo }
func (f fdinfoInstrument) Resources() []string { return resourcesFdinfo(f.pr) }
func (fdinfoInstrument) VerifiedProcessSamples() bool {
	pin, err := pinPID(os.Getpid())
	if err != nil {
		return false
	}
	defer pin.Close()
	return pin.Check() == nil
}
func (f fdinfoInstrument) Read(resource string) (Reading, error) {
	return readFdinfo(f.pr, resource), nil
}

// realProc reads /proc itself. It is the only piece of this instrument that
// is Linux-specific rather than arithmetic; fdinfo.go's readFdinfo and
// resourcesFdinfo take it through the procReader interface so their walk,
// dedup and threshold logic is what fdinfo_test.go runs on every platform.
type realProc struct{ root string }

func (r realProc) processInfo(pid int) (ProcessInfo, error) { return inspectProc(r.root, pid) }
func (r realProc) pinProcess(pid int) (processPin, error)   { return pinPID(pid) }

// InspectProcess uses the same /proc process start field as identity's
// processStartTime. Its raw boot-relative ticks are the exact identity used
// for sample matching. Started is an approximate wall-clock description;
// the lease book relies on the caller's retained pidfd rather than using
// that approximation to promote an identity proof.
func InspectProcess(pid int) (ProcessInfo, error) {
	pin, err := pinPID(pid)
	if err != nil {
		return ProcessInfo{}, err
	}
	defer pin.Close()
	if err := pin.Check(); err != nil {
		return ProcessInfo{}, err
	}
	info, err := inspectProc("/proc", pid)
	if err != nil {
		return ProcessInfo{}, err
	}
	if err := pin.Check(); err != nil {
		return ProcessInfo{}, err
	}
	return info, nil
}

// pinPID retains a reference to a process instance. The number may become
// available after that process is reaped, so Check must follow every group
// of numeric /proc reads before its identity is credited.
func pinPID(pid int) (processPin, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	pin := &pidPin{fd: fd, pid: pid}
	if err := pin.Check(); err != nil {
		pin.Close()
		return nil, err
	}
	return pin, nil
}

type pidPin struct{ fd, pid int }

func (p *pidPin) Close() { _ = unix.Close(p.fd) }

func (p *pidPin) Check() error {
	data, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(p.fd))
	if err != nil {
		return err
	}
	matched := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Pid:") {
			got, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pid:")))
			matched = err == nil && got == p.pid
			break
		}
	}
	if !matched {
		return errors.New("pidfd no longer names inspected process")
	}
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	if _, err := unix.Poll(fds, 0); err != nil {
		return err
	}
	if fds[0].Revents != 0 {
		return errors.New("inspected process has exited")
	}
	return nil
}

func inspectProc(root string, pid int) (ProcessInfo, error) {
	if pid <= 0 {
		return ProcessInfo{}, errors.New("invalid process id")
	}
	path := filepath.Join(root, strconv.Itoa(pid))
	readStat := func() (int, string, error) {
		data, err := os.ReadFile(filepath.Join(path, "stat"))
		if err != nil {
			return 0, "", err
		}
		line := string(data)
		close := strings.LastIndexByte(line, ')')
		if close < 0 {
			return 0, "", errors.New("process stat has no command terminator")
		}
		fields := strings.Fields(line[close+1:])
		if len(fields) <= 19 {
			return 0, "", errors.New("process stat lacks parent or start")
		}
		parent, err := strconv.Atoi(fields[1])
		return parent, fields[19], err
	}
	parent, start, err := readStat()
	if err != nil {
		return ProcessInfo{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ProcessInfo{}, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ProcessInfo{}, errors.New("process owner unavailable")
	}
	if sameParent, same, err := readStat(); err != nil || same != start || sameParent != parent {
		return ProcessInfo{}, errors.New("process identity changed while inspecting")
	}
	boot, err := os.ReadFile(filepath.Join(root, "sys", "kernel", "random", "boot_id"))
	if err != nil || strings.TrimSpace(string(boot)) == "" {
		return ProcessInfo{}, errors.New("process boot identity unavailable")
	}
	// /proc/uptime and stat cannot be read atomically. This wall time is
	// deliberately only a rejection aid, with tolerance in the lease book.
	up, err := os.ReadFile(filepath.Join(root, "uptime"))
	if err != nil {
		return ProcessInfo{}, err
	}
	parts := strings.Fields(string(up))
	if len(parts) == 0 {
		return ProcessInfo{}, errors.New("uptime unavailable")
	}
	seconds, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return ProcessInfo{}, err
	}
	ticks, err := strconv.ParseUint(start, 10, 64)
	if err != nil {
		return ProcessInfo{}, err
	}
	// Match identity's AT_CLKTCK lookup. Raw ticks, not this approximate
	// wall-clock conversion, establish equality across samples.
	rate := float64(100)
	if aux, err := unix.Auxv(); err == nil {
		for _, pair := range aux {
			if pair[0] == 17 && pair[1] > 0 { // AT_CLKTCK
				rate = float64(pair[1])
				break
			}
		}
	}
	started := time.Now().Add(-time.Duration((seconds - float64(ticks)/rate) * float64(time.Second)))
	bootID := strings.TrimSpace(string(boot))
	return ProcessInfo{PID: pid, ParentPID: parent, StartID: bootID + "/" + start,
		BootID: bootID, StartTicks: ticks, Started: started,
		Account: strconv.FormatUint(uint64(owner.Uid), 10)}, nil
}

func (r realProc) pids() []int {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			out = append(out, pid)
		}
	}
	return out
}

func (r realProc) fdinfoNames(pid int) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(r.root, strconv.Itoa(pid), "fdinfo"))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out, nil
}

func (r realProc) fdinfoContent(pid int, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(r.root, strconv.Itoa(pid), "fdinfo", name))
	return string(b), err
}

// exePath reads the absolute program path the same evidence Windows'
// subject() reads through a process handle: /proc/<pid>/exe is a symlink to
// the running binary, readable under the same ptrace-access check the kernel
// applies to fdinfo itself.
func (r realProc) exePath(pid int) (string, error) {
	return os.Readlink(filepath.Join(r.root, strconv.Itoa(pid), "exe"))
}

func (r realProc) comm(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join(r.root, strconv.Itoa(pid), "comm"))
	if err != nil {
		return "", err
	}
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s, nil
}

// accountUID reads the process's owning uid, formatted the way Sample.Account
// documents a POSIX uid: the /proc/<pid> directory's own owner, which is the
// kernel's answer to "whose process is this" and is set once at exec, not
// read from a token that can be impersonated.
func (r realProc) accountUID(pid int) (string, error) {
	info, err := os.Stat(filepath.Join(r.root, strconv.Itoa(pid)))
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("linux fdinfo instrument: no uid for " + strconv.Itoa(pid))
	}
	return strconv.FormatUint(uint64(st.Uid), 10), nil
}
