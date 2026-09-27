package instrument

import (
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Every runtime on this machine reports GPU memory per process and so calls
// the card empty while another process holds 22 GB of it; three instruments
// gave three answers at one instant and two of them were wrong by tens of
// gigabytes. These counters are the only instrument here that sees the whole
// device.
//
// The reader takes `\GPU Process Memory(*)\Total Committed` alone, per
// process. It used to sum Local Usage and Dedicated Usage instead.
// research/resources/MEASUREMENT-2026-09-22.md found that on the owner's AMD
// APU, Dedicated Usage and Shared Usage split the same bytes Local Usage
// reported for every process in that run. The old sum double-counted the
// Dedicated share: +1.5% for one loaded model, and the machine total ran
// 12.2% over the adapter-wide counter with two models loaded. Total Committed
// matched Local Usage in that measurement. A 2026-09-26 same-query APU read
// found Total Committed 1,887,002,624 bytes, Local Usage 1,354,440,704 and
// Non Local Usage zero for one process. These counters have no established
// universal arithmetic identity, including on discrete cards. We use Total
// Committed as the directly observed global figure; its PID-only instances
// cannot establish child process generation for lease credit. See counters.go
// for instance summation.
var (
	pdh         = syscall.NewLazyDLL("pdh.dll")
	openQuery   = pdh.NewProc("PdhOpenQueryW")
	addCounter  = pdh.NewProc("PdhAddEnglishCounterW")
	collect     = pdh.NewProc("PdhCollectQueryData")
	formatArray = pdh.NewProc("PdhGetFormattedCounterArrayW")
	closeQuery  = pdh.NewProc("PdhCloseQuery")
)

const (
	fmtLarge = 0x400
	moreData = 0x800007D2
)

type item struct {
	name   *uint16
	status uint32
	_      uint32
	value  int64
}

// Detect is the instrument of this platform. On Windows it is the GPU process
// memory counters; Linux has its own Detect in fdinfo_linux.go.
func Detect() Instrument { return gpuCounters{} }

// GPUCounters is the Windows GPU process memory counter set, named so a caller
// can ask for it explicitly rather than through Detect.
func GPUCounters() Instrument { return gpuCounters{} }

type gpuCounters struct{}

func (gpuCounters) Name() string        { return NameWindowsGPUCounters }
func (gpuCounters) Resources() []string { return []string{Card0} }

// PDH's GPU rows name a PID without the creation identity of the process
// instance it measured. A later handle lookup cannot prove that identity.
func (gpuCounters) VerifiedProcessSamples() bool { return false }

func (g gpuCounters) Read(resource string) (Reading, error) {
	at := time.Now()
	if resource != Card0 {
		return Reading{Resource: resource, At: at}, nil
	}
	mem, err := counters(`\GPU Process Memory(*)\Total Committed`)
	if err != nil {
		return Reading{}, err
	}
	// The process counter set is the fallback name of a process whose handle
	// this account cannot open; an elevated process holding the card is still
	// a holder, and a row under its image name is the truth available.
	//unchecked: this fallback lookup is best-effort, per the comment above; an error here just leaves byPID empty below
	named, _ := counters(`\Process(*)\ID Process`)
	byPID := map[int]string{}
	for _, c := range named {
		byPID[int(c.value)] = c.instance
	}
	out := Reading{Resource: resource, At: at}
	for pid, v := range residentByPID(mem) {
		if v < Threshold {
			continue
		}
		s := Sample{PID: pid, Amount: v, Image: byPID[pid]}
		s.Program, s.Account = subject(pid)
		if s.Image == "" {
			if s.Program != "" {
				s.Image = filepath.Base(s.Program)
			} else {
				s.Image = "pid" + strconv.Itoa(pid)
			}
		}
		out.Samples = append(out.Samples, s)
	}
	sort.Slice(out.Samples, func(i, j int) bool { return out.Samples[i].Amount > out.Samples[j].Amount })
	return out, nil
}

// InspectProcess reads a process's creation identity, owner and direct
// parent. A handle pins the process while its creation time and token are
// read; the snapshot supplies the parent PID Windows does not put on that
// handle. Failure to establish every field prevents a lease association.
func InspectProcess(pid int) (ProcessInfo, error) {
	if pid <= 0 {
		return ProcessInfo{}, errors.New("invalid process id")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ProcessInfo{}, err
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return ProcessInfo{}, err
	}
	started := time.Unix(0, creation.Nanoseconds())
	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		return ProcessInfo{}, err
	}
	defer token.Close()
	owner, err := token.GetTokenUser()
	if err != nil || owner.User.Sid == nil {
		return ProcessInfo{}, errors.New("process account unavailable")
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ProcessInfo{}, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return ProcessInfo{}, err
	}
	for {
		if entry.ProcessID == uint32(pid) {
			return ProcessInfo{PID: pid, ParentPID: int(entry.ParentProcessID),
				StartID: strconv.FormatInt(creation.Nanoseconds(), 10), Started: started,
				Account: owner.User.Sid.String()}, nil
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return ProcessInfo{}, errors.New("process was not in the snapshot")
		}
	}
}

// subject reads the program path and account of one process through a process
// handle, which is the evidence identity.SubjectProgram takes at ProofBound
// for a bound caller and the same evidence the runtime attributes a peer with.
// A process this account may not open yields two empty strings, and the row
// keeps its image name instead (CONTRACT.md RES-T1).
func subject(pid int) (program, account string) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err == nil {
		program = filepath.Clean(windows.UTF16ToString(buf[:n]))
	}
	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err == nil {
		defer token.Close()
		if user, err := token.GetTokenUser(); err == nil && user.User.Sid != nil {
			account = user.User.Sid.String()
		}
	}
	return program, account
}

func counters(paths ...string) ([]counter, error) {
	var q uintptr
	//unchecked: the PDH status primary return is checked in the same statement; Call's raw GetLastError third field carries no separate information here
	if r, _, _ := openQuery.Call(0, 0, uintptr(unsafe.Pointer(&q))); r != 0 {
		return nil, errors.New("PdhOpenQueryW refused")
	}
	defer closeQuery.Call(q)
	var hs []uintptr
	for _, p := range paths {
		w, err := syscall.UTF16PtrFromString(p)
		if err != nil {
			return nil, err
		}
		var c uintptr
		//unchecked: the PDH status primary return is checked in the same statement; Call's raw GetLastError third field carries no separate information here
		if r, _, _ := addCounter.Call(q, uintptr(unsafe.Pointer(w)), 0, uintptr(unsafe.Pointer(&c))); r == 0 {
			hs = append(hs, c)
		}
	}
	if len(hs) == 0 {
		return nil, errors.New("no counter path could be added")
	}
	// Twice. The first collect in a process enumerates the counterset and
	// returns a partial set of GPU instances: it reported the process holding
	// this machine's 24 GB model as holding 2.81 GB, and the same query a
	// moment later reported 24.01. A number that is wrong by an order of
	// magnitude on the first reading is worse than no number.
	for range 2 {
		//unchecked: the PDH status primary return is checked in the same statement; Call's raw GetLastError third field carries no separate information here
		if r, _, _ := collect.Call(q); r != 0 {
			return nil, errors.New("PdhCollectQueryData refused")
		}
	}
	var out []counter
	for _, c := range hs {
		out = append(out, array(c)...)
	}
	return out, nil
}

func array(c uintptr) []counter {
	var size, count uint32
	//unchecked: the PDH status primary return is checked below; Call's raw GetLastError third field carries no separate information here
	r, _, _ := formatArray.Call(c, fmtLarge, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != moreData {
		return nil
	}
	buf := make([]byte, size)
	//unchecked: the PDH status primary return is checked in the same statement; Call's raw GetLastError third field carries no separate information here
	if r, _, _ := formatArray.Call(c, fmtLarge, uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return nil
	}
	items := unsafe.Slice((*item)(unsafe.Pointer(&buf[0])), count)
	out := make([]counter, 0, count)
	for _, it := range items {
		out = append(out, counter{utf16String(it.name), it.value})
	}
	return out
}

func utf16String(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for q := unsafe.Pointer(p); *(*uint16)(q) != 0; q = unsafe.Add(q, 2) {
		n++
	}
	return string(utf16.Decode(unsafe.Slice(p, n)))
}
