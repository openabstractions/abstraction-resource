package instrument

import (
	"errors"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"
)

type trackedProcReader struct {
	fakeProcReader
	starts     []string
	reads      int
	pinErr     error
	failCheck  int
	checks     int
	infoErr    error
	partialPin bool
	closed     int
}

type trackedPin struct{ reader *trackedProcReader }

func (p trackedPin) Check() error {
	p.reader.checks++
	if p.reader.failCheck == p.reader.checks {
		return errors.New("process exited")
	}
	return nil
}
func (p trackedPin) Close() { p.reader.closed++ }

func (r *trackedProcReader) pinProcess(int) (processPin, error) {
	if r.pinErr != nil {
		if r.partialPin {
			return trackedPin{reader: r}, r.pinErr
		}
		return nil, r.pinErr
	}
	return trackedPin{reader: r}, nil
}

func (r *trackedProcReader) processInfo(pid int) (ProcessInfo, error) {
	index := r.reads
	r.reads++
	if index >= len(r.starts) {
		index = len(r.starts) - 1
	}
	var err error
	if r.reads == 1 {
		err = r.infoErr
	}
	return ProcessInfo{PID: pid, StartID: r.starts[index], Account: "1000", Started: time.Now()}, err
}

func TestFdinfoFailedEvidenceCannotAttributeReservation(t *testing.T) {
	for _, kind := range []string{"pin", "process info"} {
		t.Run(kind, func(t *testing.T) {
			reader := &trackedProcReader{fakeProcReader: fakeProcReader{100: syntheticProc()[100]}, starts: []string{"start-A"}}
			if kind == "pin" {
				reader.pinErr, reader.partialPin = errors.New("pin failed"), true
			} else {
				reader.infoErr = errors.New("incomplete process evidence")
			}
			got := readFdinfo(reader, "card:0")
			if len(got.Samples) != 1 || got.Samples[0].StartID != "" {
				t.Fatalf("failed evidence gained process attribution: %+v", got.Samples)
			}
			if reader.closed != 1 {
				t.Fatalf("closed %d pins, want one", reader.closed)
			}
		})
	}
}

func TestFdinfoSampleRetainsOnlyStableProcessStart(t *testing.T) {
	stable := &trackedProcReader{fakeProcReader: fakeProcReader{100: syntheticProc()[100]}, starts: []string{"start-A", "start-A", "start-A"}}
	got := readFdinfo(stable, "card:0")
	if len(got.Samples) != 1 || got.Samples[0].StartID != "start-A" {
		t.Fatalf("stable sample lost its start identity: %+v", got.Samples)
	}
	recycled := &trackedProcReader{fakeProcReader: fakeProcReader{100: syntheticProc()[100]}, starts: []string{"start-A", "start-A", "start-B"}}
	got = readFdinfo(recycled, "card:0")
	if len(got.Samples) != 1 || got.Samples[0].StartID != "" {
		t.Fatalf("recycled process sample kept a start identity: %+v", got.Samples)
	}
	unpinned := &trackedProcReader{fakeProcReader: fakeProcReader{100: syntheticProc()[100]},
		starts: []string{"start-A"}, pinErr: errors.New("pidfd unavailable")}
	got = readFdinfo(unpinned, "card:0")
	if len(got.Samples) != 1 || got.Samples[0].StartID != "" {
		t.Fatalf("unpinable process sample gained an identity: %+v", got.Samples)
	}
	exited := &trackedProcReader{fakeProcReader: fakeProcReader{100: syntheticProc()[100]},
		starts: []string{"start-A", "start-A", "start-A"}, failCheck: 3}
	got = readFdinfo(exited, "card:0")
	if len(got.Samples) != 1 || got.Samples[0].StartID != "" {
		t.Fatalf("sample retained identity after pin reported exit: %+v", got.Samples)
	}
}

// fakeProc is a synthetic /proc tree: the walk, dedup, ordering, threshold
// and attribution logic in fdinfo.go is what these tests check, and it runs
// on every platform this way because fakeProc needs no real files, no Unix
// symlinks and no uids — none of which a Windows test runner can fabricate.
type fakeProc struct {
	fdinfo    map[string]string // "3", "4", ...
	fdinfoErr error
	exe       string
	exeErr    error
	comm      string
	commErr   error
	uid       string
	uidErr    error
}

type fakeProcReader map[int]fakeProc

func (f fakeProcReader) pids() []int {
	out := make([]int, 0, len(f))
	for pid := range f {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

func (f fakeProcReader) fdinfoNames(pid int) ([]string, error) {
	p, ok := f[pid]
	if !ok {
		return nil, errors.New("no such process")
	}
	if p.fdinfoErr != nil {
		return nil, p.fdinfoErr
	}
	names := make([]string, 0, len(p.fdinfo))
	for name := range p.fdinfo {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (f fakeProcReader) fdinfoContent(pid int, name string) (string, error) {
	p, ok := f[pid]
	if !ok {
		return "", errors.New("no such process")
	}
	content, ok := p.fdinfo[name]
	if !ok {
		return "", errors.New("no such fd")
	}
	return content, nil
}

func (f fakeProcReader) exePath(pid int) (string, error) {
	p, ok := f[pid]
	if !ok {
		return "", errors.New("no such process")
	}
	if p.exeErr != nil {
		return "", p.exeErr
	}
	return p.exe, nil
}

func (f fakeProcReader) comm(pid int) (string, error) {
	p, ok := f[pid]
	if !ok {
		return "", errors.New("no such process")
	}
	if p.commErr != nil {
		return "", p.commErr
	}
	return p.comm, nil
}

func (f fakeProcReader) accountUID(pid int) (string, error) {
	p, ok := f[pid]
	if !ok {
		return "", errors.New("no such process")
	}
	if p.uidErr != nil {
		return "", p.uidErr
	}
	return p.uid, nil
}

// stanza builds one fdinfo file's content the way the kernel writes it:
// key:\tvalue, one pair per line.
func stanza(driver, pdev, clientID, residentVRAM, residentGTT string) string {
	s := "pos:\t0\nflags:\t0100002\nmnt_id:\t21\nino:\t571\n" +
		"drm-driver:\t" + driver + "\n" +
		"drm-pdev:\t" + pdev + "\n" +
		"drm-client-id:\t" + clientID + "\n"
	if residentVRAM != "" {
		s += "drm-resident-vram:\t" + residentVRAM + "\n"
	}
	if residentGTT != "" {
		s += "drm-resident-gtt:\t" + residentGTT + "\n"
	}
	return s
}

// The synthetic tree the walk tests share:
//
//   - pid 100: two fds of one client id on card:0 (0000:03:00.0), above
//     threshold, with a GTT figure — the dedup and Detail case.
//   - pid 200: fdinfo unreadable (not this account's process) — contributes
//     nothing, per "walks fdinfo for the owner's account (readable ones)".
//   - pid 300: readable, on card:0, below Threshold — excluded from Read but
//     still names the card in Resources.
//   - pid 400: on a second card (0000:0a:00.0, sorting after 03:00.0) with an
//     unreadable exe link — the RES-T1 comm-and-no-account fallback.
func syntheticProc() fakeProcReader {
	return fakeProcReader{
		100: {
			fdinfo: map[string]string{
				"3": stanza("amdgpu", "0000:03:00.0", "7", "1048576 KiB", "10240 KiB"),
				"4": stanza("amdgpu", "0000:03:00.0", "7", "1048576 KiB", "10240 KiB"),
			},
			exe: "/usr/bin/llama-server",
			uid: "1000",
		},
		200: {
			fdinfoErr: errors.New("permission denied"),
		},
		300: {
			fdinfo: map[string]string{
				"5": stanza("amdgpu", "0000:03:00.0", "9", "1024 KiB", ""),
			},
			exe: "/usr/bin/some-other-client",
			uid: "1000",
		},
		400: {
			fdinfo: map[string]string{
				"6": stanza("amdgpu", "0000:0a:00.0", "11", "2097152 KiB", ""),
			},
			exeErr: errors.New("permission denied"),
			comm:   "compositor",
		},
	}
}

func TestReadDedupesTwoFdsOfOneClientIDAndReportsGTTAsDetail(t *testing.T) {
	got := readFdinfo(syntheticProc(), "card:0")
	if len(got.Samples) != 1 {
		t.Fatalf("card:0 samples = %+v, want exactly pid 100 (300 is below threshold, 200 is unreadable)", got.Samples)
	}
	want := Sample{
		PID: 100, Program: "/usr/bin/llama-server", Account: "1000",
		Image: "llama-server", Amount: 1048576 * 1024, Detail: "drm-resident-gtt=" + strconv.Itoa(10240*1024),
	}
	if got.Samples[0] != want {
		t.Fatalf("pid 100 sample = %+v, want %+v", got.Samples[0], want)
	}
}

func TestReadKeepsCardsSeparateByPdevOrder(t *testing.T) {
	got := readFdinfo(syntheticProc(), "card:1")
	if len(got.Samples) != 1 {
		t.Fatalf("card:1 samples = %+v, want exactly pid 400", got.Samples)
	}
	s := got.Samples[0]
	if s.PID != 400 || s.Amount != 2097152*1024 {
		t.Fatalf("pid 400 sample = %+v", s)
	}
	if s.Program != "" || s.Account != "" {
		t.Fatalf("pid 400's exe is unreadable: sample kept Program=%q Account=%q, want both empty (RES-T1)", s.Program, s.Account)
	}
	if s.Image != "compositor" {
		t.Fatalf("pid 400's sample Image = %q, want its comm %q (RES-T1)", s.Image, "compositor")
	}
}

func TestReadExcludesUnreadableAndBelowThreshold(t *testing.T) {
	for _, resource := range []string{"card:0", "card:1"} {
		got := readFdinfo(syntheticProc(), resource)
		for _, s := range got.Samples {
			if s.PID == 200 {
				t.Fatalf("%s: pid 200's fdinfo is unreadable and should contribute no sample: %+v", resource, s)
			}
			if s.PID == 300 {
				t.Fatalf("%s: pid 300 is below Threshold and should be excluded: %+v", resource, s)
			}
		}
	}
}

func TestResourcesFdinfoOrdersCardsByPdev(t *testing.T) {
	got := resourcesFdinfo(syntheticProc())
	want := []string{"card:0", "card:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resourcesFdinfo() = %v, want %v (0000:03:00.0 sorts before 0000:0a:00.0)", got, want)
	}
}

func TestResourcesFdinfoIncludesACardEvenIfEveryHolderIsBelowThreshold(t *testing.T) {
	pr := fakeProcReader{
		300: {
			fdinfo: map[string]string{
				"5": stanza("amdgpu", "0000:03:00.0", "9", "1024 KiB", ""),
			},
		},
	}
	got := resourcesFdinfo(pr)
	if len(got) != 1 || got[0] != "card:0" {
		t.Fatalf("resourcesFdinfo() = %v, want [card:0]: the card exists even where nothing on it clears the row threshold", got)
	}
}

func TestParseDRMFdinfoParsesTheStanza(t *testing.T) {
	st, ok := parseDRMFdinfo(stanza("amdgpu", "0000:03:00.0", "7", "1048576 KiB", "10240 KiB"))
	if !ok {
		t.Fatal("parseDRMFdinfo did not recognize a complete DRM stanza")
	}
	want := drmStanza{driver: "amdgpu", pdev: "0000:03:00.0", clientID: "7", residentVRAM: 1048576 * 1024, residentGTT: 10240 * 1024}
	if st != want {
		t.Fatalf("parseDRMFdinfo() = %+v, want %+v", st, want)
	}
}

func TestParseDRMFdinfoRejectsAFileWithNoDRMStanza(t *testing.T) {
	// A socket or a pipe fd has an fdinfo file too, with no drm- keys at all.
	if _, ok := parseDRMFdinfo("pos:\t0\nflags:\t02\nmnt_id:\t9\n"); ok {
		t.Fatal("parseDRMFdinfo accepted a non-DRM fdinfo file")
	}
}

func TestParseDRMFdinfoRejectsAPartialStanza(t *testing.T) {
	// drm-client-id missing: this fd cannot be deduped or attributed to a
	// client, so it must not be silently counted as one.
	if _, ok := parseDRMFdinfo("drm-driver:\tamdgpu\ndrm-pdev:\t0000:03:00.0\n"); ok {
		t.Fatal("parseDRMFdinfo accepted a stanza with no drm-client-id")
	}
}

func TestParseFdinfoSizeUnits(t *testing.T) {
	cases := []struct {
		value string
		want  int64
		ok    bool
	}{
		{"1024 KiB", 1024 * 1024, true},
		{"1 MiB", 1024 * 1024, true},
		{"512", 512, true},
		{"12 GiB", 0, false}, // the kernel's fdinfo format offers only KiB and MiB here
		{"-1 KiB", 0, false},
		{"", 0, false},
		{"not-a-number KiB", 0, false},
	}
	for _, c := range cases {
		got, ok := parseFdinfoSize(c.value)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("parseFdinfoSize(%q) = %d, %v, want %d, %v", c.value, got, ok, c.want, c.ok)
		}
	}
}

func TestResidentByPdevDedupesByClientIDAcrossFds(t *testing.T) {
	stanzas := []drmStanza{
		{driver: "amdgpu", pdev: "0000:03:00.0", clientID: "7", residentVRAM: 5, residentGTT: 1},
		{driver: "amdgpu", pdev: "0000:03:00.0", clientID: "7", residentVRAM: 5, residentGTT: 1},
		{driver: "amdgpu", pdev: "0000:03:00.0", clientID: "9", residentVRAM: 3, residentGTT: 0},
	}
	got := residentByPdev(stanzas)
	want := map[string]residentTotals{"0000:03:00.0": {vram: 8, gtt: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("residentByPdev() = %+v, want %+v (two fds of client 7 count once)", got, want)
	}
}

func TestCardOrderSortsPdevsAscending(t *testing.T) {
	got := cardOrder(map[string]bool{"0000:0a:00.0": true, "0000:03:00.0": true})
	want := []string{"0000:03:00.0", "0000:0a:00.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cardOrder() = %v, want %v", got, want)
	}
}

func TestGTTDetailIsEmptyWhenThereIsNoGTT(t *testing.T) {
	if d := gttDetail(0); d != "" {
		t.Fatalf("gttDetail(0) = %q, want empty", d)
	}
	if d := gttDetail(-1); d != "" {
		t.Fatalf("gttDetail(-1) = %q, want empty", d)
	}
}
