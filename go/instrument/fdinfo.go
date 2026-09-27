package instrument

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The part of the Linux DRM fdinfo instrument that is arithmetic and walking
// order rather than platform. It lives outside fdinfo_linux.go so every
// platform compiles it and its tests: fdinfo_linux.go reads real files
// through syscalls proc_test.go cannot fake on Windows, and residentByPdev
// and its callers here have no such dependency.
//
// The format is the kernel's own (https://docs.kernel.org/gpu/drm-usage-stats.html):
// one `key:\tvalue` pair per line, colon-delimited, whitespace after the
// colon ignored. drm-pdev, drm-client-id and drm-driver identify the device
// and the client; drm-resident-vram and drm-resident-gtt are `<uint> [KiB|MiB]`
// and bytes when no unit is given.

// A drmStanza is one fdinfo file's DRM lines. A file with no drm-pdev,
// drm-client-id or drm-driver line is not a DRM client's fdinfo — a socket or
// a pipe has an fdinfo file too — and parseDRMFdinfo reports it as such rather
// than a zero-valued stanza that would be silently counted.
type drmStanza struct {
	driver       string
	pdev         string
	clientID     string
	residentVRAM int64
	residentGTT  int64
}

// parseDRMFdinfo reads one /proc/<pid>/fdinfo/<fd> file's content. ok is false
// when the file has no DRM stanza at all, which every non-DRM fd's fdinfo is.
func parseDRMFdinfo(content string) (drmStanza, bool) {
	var st drmStanza
	for _, line := range strings.Split(content, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "drm-driver":
			st.driver = value
		case "drm-pdev":
			st.pdev = value
		case "drm-client-id":
			st.clientID = value
		case "drm-resident-vram":
			if n, ok := parseFdinfoSize(value); ok {
				st.residentVRAM = n
			}
		case "drm-resident-gtt":
			if n, ok := parseFdinfoSize(value); ok {
				st.residentGTT = n
			}
		}
	}
	if st.driver == "" || st.pdev == "" || st.clientID == "" {
		return drmStanza{}, false
	}
	return st, true
}

// parseFdinfoSize reads a `<uint> [KiB|MiB]` value. The default unit is
// bytes, per the kernel's format: a bare number carries no suffix to strip.
func parseFdinfoSize(value string) (int64, bool) {
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	if len(fields) == 1 {
		return n, true
	}
	switch fields[1] {
	case "KiB":
		return n * 1024, true
	case "MiB":
		return n * 1024 * 1024, true
	default:
		return 0, false
	}
}

// residentTotals is one process's resident bytes on one card.
type residentTotals struct {
	vram int64
	gtt  int64
}

// residentByPdev dedupes stanzas by drm-client-id and sums what remains by
// drm-pdev. Several fds of one process share a client id — an open handle per
// mapped buffer object, not per client — and summing every fd would multiply
// one client's residency by however many fds happen to be open on it.
func residentByPdev(stanzas []drmStanza) map[string]residentTotals {
	byClient := map[string]drmStanza{}
	for _, st := range stanzas {
		if _, seen := byClient[st.clientID]; !seen {
			byClient[st.clientID] = st
		}
	}
	out := map[string]residentTotals{}
	for _, st := range byClient {
		t := out[st.pdev]
		t.vram += st.residentVRAM
		t.gtt += st.residentGTT
		out[st.pdev] = t
	}
	return out
}

// cardOrder assigns card:<n> by drm-pdev sorted ascending, so the same set of
// devices always numbers the same way rather than by discovery order, which a
// process exiting between two reads would change.
func cardOrder(pdevs map[string]bool) []string {
	order := make([]string, 0, len(pdevs))
	for pdev := range pdevs {
		order = append(order, pdev)
	}
	sort.Strings(order)
	return order
}

func cardName(index int) string { return fmt.Sprintf("card:%d", index) }

// gttDetail is the Sample.Detail a resident GTT figure becomes: reported
// beside Amount (the resident VRAM) rather than summed into it, because on an
// APU VRAM is a carve-out of system memory and its GTT window is a second,
// distinct figure (CONTRACT.md RES-T5).
func gttDetail(gtt int64) string {
	if gtt <= 0 {
		return ""
	}
	return "drm-resident-gtt=" + strconv.FormatInt(gtt, 10)
}

// A procReader is every read the fdinfo walk needs from one process, kept
// separate from the file-system and syscalls that answer it in
// fdinfo_linux.go. Tests supply a synthetic one so this file's walk — and its
// dedup, ordering and threshold rules — run and are checked on every
// platform, not only Linux: Windows has no /proc to point a real reader at,
// and no privilege to fabricate the Unix symlinks and uids a real one reads.
type procReader interface {
	// pids lists every process to consider.
	pids() []int
	// fdinfoNames lists one process's open fds by fdinfo file name. An error
	// is the fdinfo directory refusing this account — not this process's own,
	// on a machine where this account is not root — and that process
	// contributes no sample: there is nothing here to measure it with.
	fdinfoNames(pid int) ([]string, error)
	// fdinfoContent reads one fd's fdinfo file.
	fdinfoContent(pid int, name string) (string, error)
	// exePath reads /proc/<pid>/exe's target: the absolute program path. An
	// error is this account being unable to resolve it, and the row falls
	// back to comm with no account (RES-T1).
	exePath(pid int) (string, error)
	// comm reads /proc/<pid>/comm, the kernel's own name for the process.
	comm(pid int) (string, error)
	// accountUID reads the process's owning uid, formatted as the POSIX uid
	// Sample.Account documents. It is read only when exePath succeeds: RES-T1
	// ties comm and an empty account together rather than half-attributing a
	// row.
	accountUID(pid int) (string, error)
}

// processPin keeps one kernel process identity available while its /proc
// files are read. Check confirms that the original instance is still live;
// a retained pidfd alone can outlive that instance and its numeric PID.
type processPin interface {
	Check() error
	Close()
}

// readFdinfo is fdinfoInstrument.Read's platform-free half: it walks every
// process procReader offers, dedupes and sums by card, applies Threshold and
// attributes each row, all without touching a file system itself.
func readFdinfo(pr procReader, resource string) Reading {
	at := time.Now()
	type proc struct {
		pid     int
		totals  map[string]residentTotals
		start   string
		account string
		pin     processPin
	}
	var procs []proc
	allPdevs := map[string]bool{}
	for _, pid := range pr.pids() {
		var pin processPin
		if pinner, ok := pr.(interface{ pinProcess(int) (processPin, error) }); ok {
			candidate, err := pinner.pinProcess(pid)
			if err == nil {
				pin = candidate
			} else if candidate != nil {
				candidate.Close()
			}
		}
		var before ProcessInfo
		if inspector, ok := pr.(interface {
			processInfo(int) (ProcessInfo, error)
		}); ok && pin != nil && pin.Check() == nil {
			if observed, err := inspector.processInfo(pid); err == nil {
				before = observed
			}
		}
		names, err := pr.fdinfoNames(pid)
		if err != nil {
			if pin != nil {
				pin.Close()
			}
			continue
		}
		var stanzas []drmStanza
		for _, name := range names {
			content, err := pr.fdinfoContent(pid, name)
			if err != nil {
				continue
			}
			if st, ok := parseDRMFdinfo(content); ok {
				stanzas = append(stanzas, st)
			}
		}
		if len(stanzas) == 0 {
			if pin != nil {
				pin.Close()
			}
			continue
		}
		totals := residentByPdev(stanzas)
		for pdev := range totals {
			allPdevs[pdev] = true
		}
		candidate := proc{pid: pid, totals: totals, pin: pin}
		if before.StartID != "" && pin.Check() == nil {
			if inspector, ok := pr.(interface {
				processInfo(int) (ProcessInfo, error)
			}); ok {
				if after, err := inspector.processInfo(pid); err == nil && after.StartID == before.StartID && after.Account == before.Account {
					candidate.start, candidate.account = before.StartID, before.Account
				}
			}
		}
		procs = append(procs, candidate)
	}
	index := map[string]int{}
	for i, pdev := range cardOrder(allPdevs) {
		index[pdev] = i
	}
	out := Reading{Resource: resource, At: at}
	for _, p := range procs {
		var samples []Sample
		for pdev, totals := range p.totals {
			i, ok := index[pdev]
			if !ok || cardName(i) != resource || totals.vram < Threshold {
				continue
			}
			sample := attribute(pr, p.pid, totals)
			if p.start != "" && p.account == sample.Account {
				if inspector, ok := pr.(interface {
					processInfo(int) (ProcessInfo, error)
				}); ok {
					if after, err := inspector.processInfo(p.pid); err == nil && after.StartID == p.start && after.Account == p.account {
						sample.StartID = p.start
					}
				}
			}
			samples = append(samples, sample)
		}
		if p.pin != nil {
			if err := p.pin.Check(); err != nil {
				for i := range samples {
					samples[i].StartID = ""
				}
			}
			p.pin.Close()
		}
		out.Samples = append(out.Samples, samples...)
	}
	sort.Slice(out.Samples, func(i, j int) bool { return out.Samples[i].Amount > out.Samples[j].Amount })
	return out
}

// resourcesFdinfo is Resources(): every card:<n> any process's fdinfo names,
// in the same drm-pdev order readFdinfo attributes rows by.
func resourcesFdinfo(pr procReader) []string {
	allPdevs := map[string]bool{}
	for _, pid := range pr.pids() {
		names, err := pr.fdinfoNames(pid)
		if err != nil {
			continue
		}
		for _, name := range names {
			content, err := pr.fdinfoContent(pid, name)
			if err != nil {
				continue
			}
			if st, ok := parseDRMFdinfo(content); ok {
				allPdevs[st.pdev] = true
			}
		}
	}
	order := cardOrder(allPdevs)
	out := make([]string, len(order))
	for i := range order {
		out[i] = cardName(i)
	}
	return out
}

// attribute builds one Sample from a process's per-card totals. The program
// path is read the same way the Windows instrument documents its Program:
// unresolvable keeps the row's Image as comm and leaves Program and Account
// empty (CONTRACT.md RES-T1).
func attribute(pr procReader, pid int, totals residentTotals) Sample {
	s := Sample{PID: pid, Amount: totals.vram, Detail: gttDetail(totals.gtt)}
	program, err := pr.exePath(pid)
	if err != nil || program == "" {
		comm, err := pr.comm(pid)
		if err != nil || comm == "" {
			s.Image = "pid" + strconv.Itoa(pid)
		} else {
			s.Image = comm
		}
		return s
	}
	s.Program = program
	s.Image = program
	if idx := strings.LastIndexAny(program, "/\\"); idx >= 0 && idx+1 < len(program) {
		s.Image = program[idx+1:]
	}
	if account, err := pr.accountUID(pid); err == nil {
		s.Account = account
	}
	return s
}
